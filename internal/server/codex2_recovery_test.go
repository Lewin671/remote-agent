package server

import (
	"bytes"
	"context"
	"fmt"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"net/http"
	"testing"
	"time"
)

func TestRecoveryRateLimiterExpiresInactiveAddresses(t *testing.T) {
	rs := newRecoveryService(nil)
	old := time.Now().Add(-2 * time.Minute)
	for i := 0; i < 10000; i++ {
		rs.byAddr[fmt.Sprintf("expired-%d", i)] = []time.Time{old}
	}
	rs.byAddr["active"] = []time.Time{time.Now()}
	if !rs.allow("new") {
		t.Fatal("fresh request rejected")
	}
	if len(rs.byAddr) != 2 {
		t.Fatalf("inactive address buckets retained: %d", len(rs.byAddr))
	}
	for i := 0; i < recoveryPerMinute; i++ {
		rs.allow("active")
	}
	if rs.allow("active") {
		t.Fatal("active address limit was bypassed")
	}
}

// A request validated before another upload can reach its transaction later.
// Hold its validated rows, advance the actual relay, submit recovery against
// that head, then resume the old commit: a duplicate must have no side effects.
func TestOverlappingStateUploadDoesNotRewindRecoveryKeyOrVetoPending(t *testing.T) {
	for _, advanceAgain := range []bool{false, true} {
		t.Run(fmt.Sprint(advanceAgain), func(t *testing.T) {
			ts, parent, _, c := recoveryFixture(t)
			key4, pub4 := newRecoveryKey(t, 0xd1)
			fourth, err := deviceauth.BuildRenewalRotating(parent, c.secret, pub4, c.phoneEd, uint64(time.Now().UnixMilli()))
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := fourth.MarshalBinary()
			hash, _ := fourth.Hash()
			validated := []securityRow{{epoch: fourth.Core.Epoch, state: fourth, raw: raw, hash: hash}}
			if err := ts.putState(t, fourth); err != nil {
				t.Fatal(err)
			}
			current, key := fourth, key4
			if advanceAgain {
				key5, pub5 := newRecoveryKey(t, 0xd2)
				current, err = deviceauth.BuildRenewalRotating(fourth, c.secret, pub5, c.phoneEd, uint64(time.Now().UnixMilli()))
				if err != nil {
					t.Fatal(err)
				}
				key = key5
				if err := ts.putState(t, current); err != nil {
					t.Fatal(err)
				}
			}
			api := remoteagentv1connect.NewRecoveryServiceClient(http.DefaultClient, ts.url)
			pending := buildRecovery(t, current, key, 0xd3)
			id := submitRecovery(t, api, pending)
			got, _, err := ts.tenant().commitSecurityStates(context.Background(), validated)
			if err != nil {
				t.Fatal(err)
			}
			var recoveryEd []byte
			var rejected int64
			if err := ts.srv.store.db.QueryRow(`SELECT recovery_ed FROM accounts WHERE id=?`, ts.acct()).Scan(&recoveryEd); err != nil {
				t.Fatal(err)
			}
			if err := ts.srv.store.db.QueryRow(`SELECT rejected_ms FROM security_recovery WHERE recovery_id=?`, id).Scan(&rejected); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(recoveryEd, current.Core.RecoveryEd[:]) || rejected != 0 || got.epoch != current.Core.Epoch {
				t.Fatalf("stale upload rewrote head effects: key=%x rejected=%d response epoch=%d want %d", recoveryEd, rejected, got.epoch, current.Core.Epoch)
			}
		})
	}
}
