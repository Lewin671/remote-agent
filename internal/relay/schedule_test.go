package relay_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
)

func (f *fakeLocal) Schedule(_ context.Context, q *connect.Request[pb.ScheduleRequest]) (*connect.Response[pb.ScheduleResponse], error) {
	f.b.mu.Lock()
	f.b.schedules = append(f.b.schedules, q.Msg)
	f.b.mu.Unlock()
	b, _ := json.Marshal(map[string]any{"id": "task-id", "spec": json.RawMessage(q.Msg.SpecJson), "status": "scheduled"})
	return connect.NewResponse(&pb.ScheduleResponse{ResultJson: b}), nil
}
func TestScheduledTaskCommandIsMachineScopedAndEncrypted(t *testing.T) {
	e := setup(t)
	e.backend.add(t, "sess1")
	e.startRelay()
	s := e.session(t, "sess1", 0)
	e.online(t, true)
	ctx := context.Background()
	spec, _ := json.Marshal(map[string]any{"title": marker, "message": marker, "target": map[string]any{"type": "resume_session", "session": "sess1"}, "trigger": map[string]any{"type": "after_turn", "delay_seconds": 60}})
	command := &pb.Command{Kind: &pb.Command_Schedule{Schedule: &pb.ScheduleRequest{Operation: "create", SessionId: "sess1", SpecJson: spec, RequestId: "creation-key"}}}
	var ce *remoteclient.CommandError
	if _, err := e.client.Do(ctx, s, command); !errors.As(err, &ce) || ce.Code != "invalid_argument" {
		t.Fatalf("session-scoped command accepted: %v", err)
	}
	res, err := e.client.DoMachine(ctx, s.MachineID, command)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(res.GetSchedule().ResultJson, []byte(marker)) {
		t.Fatal("response did not decrypt")
	}
	e.backend.mu.Lock()
	if len(e.backend.schedules) != 1 || !bytes.Equal(e.backend.schedules[0].SpecJson, spec) {
		t.Fatal("wrong daemon operation")
	}
	e.backend.mu.Unlock()
	e.stopRelay()
	e.srv.Close()
	for _, name := range []string{e.dbPath, e.dbPath + "-wal"} {
		if b, err := os.ReadFile(name); err == nil && bytes.Contains(b, []byte(marker)) {
			t.Fatal("task content reached server storage")
		}
	}
}
