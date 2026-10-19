package relay

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"google.golang.org/protobuf/proto"
)

type checkpointBackend struct {
	publishBackend
	dir string
}

func (b checkpointBackend) SessionDir(string) string { return b.dir }
func (b checkpointBackend) LiveSnapshot(id string) (*pb.LiveSnapshot, error) {
	return &pb.LiveSnapshot{Session: &pb.Session{Id: id, Title: "private-current-state", LastSeq: 99}, Running: true}, nil
}

func TestCheckpointIsDurableEncryptedAndIndependentOfHistory(t *testing.T) {
	dir := t.TempDir()
	c := &conn{r: New(checkpointBackend{dir: dir}), acct: &Account{}}
	initializeTestSecurityKeys(t, c)
	raw, err := c.currentCheckpoint("session-checkpoint", &pb.Session{Id: "session-checkpoint"})
	if err != nil {
		t.Fatal(err)
	}
	var snapshot pb.LiveSnapshot
	if err := proto.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Session.LastSeq != 99 || !snapshot.Running {
		t.Fatal("current snapshot needs historical prefix")
	}
	env, err := c.security.sealMessage(&pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_SESSION_META, MachineId: c.acct.DeviceID, SessionId: "session-checkpoint", Seq: 1}, deviceauth.SessionMeta, nil, &pb.Session{Id: "session-checkpoint", CurrentCheckpoint: raw})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.persistCheckpoint("session-checkpoint", &pb.PutSessionRequest{SessionId: "session-checkpoint", Meta: env}); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(filepath.Join(dir, "current-checkpoint.pb"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte("private-current-state")) {
		t.Fatal("plaintext checkpoint on disk")
	}
	var request pb.PutSessionRequest
	if err := proto.Unmarshal(stored, &request); err != nil {
		t.Fatal(err)
	}
	wire, err := deviceauth.FromEnvelope(request.Meta)
	if err != nil {
		t.Fatal(err)
	}
	_, plain, err := c.security.endpoint.Open(wire, nil, false, uint64(time.Now().UnixMilli()))
	if err != nil {
		t.Fatal(err)
	}
	var info pb.Session
	if err := proto.Unmarshal(plain, &info); err != nil || !bytes.Equal(info.CurrentCheckpoint, raw) {
		t.Fatal("checkpoint cannot bootstrap")
	}
}

func TestExpiredLeasePausesEveryContentPublisherUntilRenewed(t *testing.T) {
	c := &conn{r: New(publishBackend{}), acct: &Account{}}
	keys := initializeTestSecurityKeys(t, c)
	_, head, _ := c.security.endpoint.Snapshot()
	endpoint := c.security.endpoint
	expired := endpoint.Lease().NotAfter + 1
	c.security.clock = func() uint64 { return expired }
	if err := endpoint.Touch(expired); err != nil {
		t.Fatal(err)
	}
	for _, purpose := range []deviceauth.Purpose{deviceauth.Event, deviceauth.Image, deviceauth.Notify, deviceauth.TerminalOutput, deviceauth.SessionMeta, deviceauth.Result} {
		if _, err := c.security.seal(&pb.Header{}, purpose, nil, []byte("buffered")); err == nil {
			t.Fatalf("purpose %v published after expiry", purpose)
		}
	}
	// Pure local history remains available and preparing a native session never
	// depends on a relay upload. The accepted log still answers cached results.
	if _, err := endpoint.ContentSecret(head.Core.ContentEpoch); err != nil {
		t.Fatal(err)
	}
	renewed, err := deviceauth.BuildLease(head, keys.phone, expired)
	if err != nil {
		t.Fatal(err)
	}
	if err := endpoint.InstallLease(renewed, expired); err != nil {
		t.Fatal(err)
	}
	if _, err := c.security.seal(&pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_MACHINE_META, MachineId: c.acct.DeviceID, Seq: 1}, deviceauth.MachineMeta, nil, []byte("buffered")); err != nil {
		t.Fatalf("renewal did not resume buffered publishing: %v", err)
	}
}
