package relay_test

import (
	"connectrpc.com/connect"
	"context"
	"errors"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/outcome"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
	"strings"
	"testing"
	"time"
)

func TestWireIdentityConflictPreservesOriginalAndBodyEviction(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "body", true: "summary"}[missing], func(t *testing.T) {
			e := setup(t)
			e.backend.add(t, "session")
			if missing {
				e.backend.sendResult = func(context.Context) error {
					return outcome.FinalError(connect.NewError(connect.CodeInternal, errors.New(strings.Repeat("x", deviceauth.MaxCipherBytes))))
				}
			}
			e.startRelay()
			s := e.session(t, "session", 0)
			e.online(t, true)
			id, deadline := remoteclient.NewRequestID(), time.Now().Add(time.Minute)
			seal := func(text string) *pb.Envelope {
				env, err := e.client.Seal(s.MachineID, s.ID, id, deadline, &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: s.ID, Text: text, RequestId: id}}})
				if err != nil {
					t.Fatal(err)
				}
				return env
			}
			original, other := seal("original"), seal("different")
			result, err := e.client.Forward(context.Background(), s.MachineID, original)
			if result == nil || result.Outcome != "executed" || result.ResultAvailable == missing {
				t.Fatalf("first result: %v", err)
			}
			if missing && !remoteclient.Uncertain(err) || !missing && err != nil {
				t.Fatal(err)
			}
			conflict, err := e.client.Forward(context.Background(), s.MachineID, other)
			if conflict == nil || conflict.Outcome != "conflict" || conflict.ConflictKind != "identity" || conflict.ResultAvailable || conflict.Kind != nil || err == nil {
				t.Fatalf("identity leaked or failed: %v", err)
			}
			again, err := e.client.Forward(context.Background(), s.MachineID, original)
			if again == nil || again.Outcome != "executed" || again.ResultAvailable == missing || e.backend.sendCount() != 1 {
				t.Fatalf("original changed/repeated: %v", err)
			}
		})
	}
}
func TestOfflineAttemptCannotOverrideEarlierSignedUnknown(t *testing.T) {
	e := setup(t)
	e.backend.add(t, "session")
	e.backend.sendResult = func(context.Context) error { return errors.New("no completion evidence") }
	e.startRelay()
	s := e.session(t, "session", 0)
	e.online(t, true)
	seal := func() *pb.Envelope {
		w, err := e.client.Seal(s.MachineID, s.ID, remoteclient.NewRequestID(), time.Now().Add(time.Minute), &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: s.ID, Text: "fixture"}}})
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	accepted := seal()
	_, err := e.client.Forward(context.Background(), s.MachineID, accepted)
	var ce *remoteclient.CommandError
	if !errors.As(err, &ce) || ce.Outcome != outcome.Unknown || !ce.FromDaemon {
		t.Fatal("unknown lacks proof", err)
	}
	e.stopRelay()
	e.online(t, false)
	for _, prior := range []bool{false, true} {
		env := accepted
		if !prior {
			env = seal()
		}
		_, err = e.client.Forward(context.Background(), s.MachineID, env)
		if !errors.As(err, &ce) {
			t.Fatal(err)
		}
		want := outcome.NotSent
		if prior {
			want = outcome.Unknown
		}
		if ce.Outcome != want {
			t.Fatalf("prior %v: %s", prior, ce.Outcome)
		}
	}
	if e.backend.sendCount() != 1 {
		t.Fatal("repeated")
	}
}

func TestExpiredPreviousProtocolRemainsSignedUnknown(t *testing.T) {
	e := setup(t)
	e.backend.add(t, "session")
	e.startRelay()
	s := e.session(t, "session", 0)
	e.online(t, true)
	// Migration discards v3 accepted rows only after their original deadlines.
	// Do not reinterpret a potentially executed old authorization as rejected.
	env, err := e.client.Seal(s.MachineID, s.ID, remoteclient.NewRequestID(), time.Now().Add(-time.Second), &pb.Command{ProtocolVersion: 14, Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: s.ID, Text: "old authorized payload"}}})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		proof, err := e.client.Forward(context.Background(), s.MachineID, env)
		var failure *remoteclient.CommandError
		if !errors.As(err, &failure) || !failure.FromDaemon || failure.Outcome != outcome.Unknown || proof == nil || proof.Outcome != "unknown" {
			t.Fatal("expired missing ID became a rejection", err)
		}
	}
	if e.backend.sendCount() != 0 {
		t.Fatal("old authorization executed again")
	}
}
