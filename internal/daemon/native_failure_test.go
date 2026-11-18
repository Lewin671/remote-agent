package daemon

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/relay"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
)

func TestCancelHeldAfterKeysRemainsUnknownAndDoesNotRepeat(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	h, _, stuck := heldPair(t)
	*stuck = true
	var clockNs atomic.Int64
	fixedNow := time.Now()
	clockNs.Store(fixedNow.UnixNano())
	h.s.now = func() time.Time { return time.Unix(0, clockNs.Load()) }
	h.s.sleep = func(d time.Duration) { clockNs.Add(int64(d)) }
	h.d.sessions[h.s.meta.ID] = h.s
	client, machine := startCancellationTestRelayWithOptions(
		t,
		outcomeAuditBackend{interruptedModelBackend{Daemon: h.d}},
		nil,
		[]func(*relay.Relay){
			func(r *relay.Relay) {
				r.SchedulerNow = func() time.Time { return fixedNow }
				r.SchedulerAfter = func(time.Duration, func()) func() bool {
					return func() bool { return false }
				}
			},
		},
	)
	cmd, err := client.Seal(machine, h.s.meta.ID, remoteclient.NewRequestID(), time.Now().Add(time.Minute), &pb.Command{Kind: &pb.Command_CancelInput{CancelInput: &pb.CancelInputRequest{SessionId: h.s.meta.ID, RequestId: "a"}}})
	if err != nil {
		t.Fatal(err)
	}
	keys := 0
	for n := 0; n < 2; n++ {
		_, err = client.Forward(t.Context(), machine, cmd)
		var ce *remoteclient.CommandError
		if !errors.As(err, &ce) || ce.Code != "unknown_outcome" || !remoteclient.Uncertain(err) {
			t.Fatalf("post-key failure finalized: %v", err)
		}
		if n == 0 {
			keys = len(h.term.written())
			if keys == 0 {
				t.Fatal("did not write native keys")
			}
		} else if len(h.term.written()) != keys {
			t.Fatal("unknown cancellation repeated keys")
		}
	}
}
