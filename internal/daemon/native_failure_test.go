package daemon

import (
	"context"
	"errors"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
	"testing"
	"time"
)

func TestCancelHeldAfterKeysRemainsUnknownAndDoesNotRepeat(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	h, _, stuck := heldPair(t)
	*stuck = true
	h.d.sessions[h.s.meta.ID] = h.s
	client, machine := startCancellationTestRelay(t, outcomeAuditBackend{interruptedModelBackend{Daemon: h.d}})
	cmd, err := client.Seal(machine, h.s.meta.ID, remoteclient.NewRequestID(), time.Now().Add(time.Minute), &pb.Command{Kind: &pb.Command_CancelInput{CancelInput: &pb.CancelInputRequest{SessionId: h.s.meta.ID, RequestId: "a"}}})
	if err != nil {
		t.Fatal(err)
	}
	keys := 0
	for n := 0; n < 2; n++ {
		_, err = client.Forward(context.Background(), machine, cmd)
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
