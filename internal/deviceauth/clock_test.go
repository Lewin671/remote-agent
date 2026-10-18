package deviceauth

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

// TestFutureStateIsAClockError covers a state issued by a device whose clock
// is ahead: the endpoint must say the clock is wrong, not that the chain is
// merely behind, so a daemon or client can warn about it.
func TestFutureStateIsAClockError(t *testing.T) {
	now := uint64(time.Now().UnixMilli())
	phone, phoneEd, phoneX := testMember(t, 0x11, Client)
	var recoveryPub [32]byte
	recoveryPub[0] = 0x22
	secret := bytes.Repeat([]byte{0x33}, 32)
	g, err := BuildGenesis("0123456789abcdef0123456789abcdef", digest("test/domain", []byte("clock")), phone, recoveryPub, secret, phoneEd, now)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := g.Pin()
	if err != nil {
		t.Fatal(err)
	}
	ep, err := InitEndpoint("", pin, []State{g}, []*RecoveryStatement{nil}, phone.ID, phoneEd.Seed(), phoneX)
	if err != nil {
		t.Fatal(err)
	}
	defer ep.Close()
	future, err := BuildRenewal(g, secret, phoneEd, now+10*60*1000)
	if err != nil {
		t.Fatal(err)
	}
	if err := ep.Install([]State{future}, []*RecoveryStatement{nil}); !errors.Is(err, ErrClock) {
		t.Fatalf("future state install = %v, want ErrClock", err)
	}
}
