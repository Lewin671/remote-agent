package relay_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
)

// The backend has admitted the message before returning an interrupted result.
// Exercise real HTTP Forward, encrypted WS, accepted storage and Go client.
func TestInterruptedResultRemainsUnknownAcrossRestart(t *testing.T) {
	for _, code := range []connect.Code{connect.CodeCanceled, connect.CodeDeadlineExceeded, connect.CodeAborted} {
		t.Run(code.String(), func(t *testing.T) {
			e := setup(t)
			e.backend.add(t, "sess1")
			e.backend.sendResult = func(context.Context) error { return connect.NewError(code, errors.New("interrupted after admission")) }
			e.startRelay()
			s := e.session(t, "sess1", 0)
			e.online(t, true)
			cmd, err := e.client.Seal(s.MachineID, s.ID, remoteclient.NewRequestID(), time.Now().Add(time.Minute), &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: s.ID, Text: "hello", RequestId: "stable-message"}}})
			if err != nil {
				t.Fatal(err)
			}
			for i := range 3 {
				if i == 2 {
					e.stopRelay()
					e.online(t, false)
					e.startRelay()
					e.online(t, true)
				}
				_, err := e.client.Forward(context.Background(), s.MachineID, cmd)
				var ce *remoteclient.CommandError
				if !errors.As(err, &ce) || ce.Code != "unknown" {
					t.Errorf("attempt %d: want unknown, got %v", i, err)
				}
			}
			if n := e.backend.sendCount(); n != 1 {
				t.Fatalf("side effect repeated %d times", n)
			}
		})
	}
}

// The schedule backend's rejected CAS is final, even though local RPC uses
// Aborted for it. Remote callers must not confuse it with interrupted work.
type revisionConflictBackend struct{ *fakeBackend }
type revisionConflictLocal struct{ fakeLocal }

func (b revisionConflictBackend) Local() remoteagentv1connect.LocalServiceHandler {
	return &revisionConflictLocal{fakeLocal: fakeLocal{b: b.fakeBackend}}
}
func (revisionConflictLocal) Schedule(context.Context, *connect.Request[pb.ScheduleRequest]) (*connect.Response[pb.ScheduleResponse], error) {
	return nil, connect.NewError(connect.CodeAborted, errors.New("revision changed"))
}
func TestScheduleRevisionConflictIsAuthenticatedAndFinal(t *testing.T) {
	e := setup(t)
	e.startRelayBackend(revisionConflictBackend{e.backend})
	e.online(t, true)
	_, err := e.client.DoMachine(context.Background(), e.acct.DeviceID, &pb.Command{Kind: &pb.Command_Schedule{Schedule: &pb.ScheduleRequest{Operation: "pause", TaskId: "test-task", ExpectedRevision: 1}}})
	var ce *remoteclient.CommandError
	if !errors.As(err, &ce) || ce.Code != "revision_conflict" || !ce.FromDaemon || remoteclient.Uncertain(err) {
		t.Fatalf("conflict must be final: %v", err)
	}
}
