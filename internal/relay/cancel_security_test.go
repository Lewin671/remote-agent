package relay

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/protocol"
	"google.golang.org/protobuf/proto"
)

func TestInFlightDuplicateDoesNotSynchronizeSecurityState(t *testing.T) {
	c := &conn{}
	keys := initializeTestSecurityKeys(t, c)
	pin, _, _ := c.security.endpoint.Snapshot()
	phone := keys.states[0].Core.Members[0]
	lease, err := deviceauth.BuildLease(keys.states[1], keys.phone, keys.states[1].Core.IssuedAt)
	if err != nil {
		t.Fatal(err)
	}
	client, err := deviceauth.InitEndpoint("", pin, keys.states, []*deviceauth.RecoveryStatement{nil, nil}, phone.ID, keys.phone.Seed(), keys.phoneX, lease)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	now := uint64(time.Now().UnixMilli())
	body, _ := proto.Marshal(&pb.Command{ProtocolVersion: protocol.Version, Kind: &pb.Command_Interrupt{Interrupt: &pb.InterruptRequest{SessionId: "test-session"}}})
	wire, err := client.Prepare(deviceauth.Header{Kind: 5, Purpose: deviceauth.Command, Machine: c.acct.DeviceID, Session: "test-session", Request: "concurrent-request", Deadline: now + 60000}, nil, body)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := c.security.accept(context.Background(), wire, nil, now, func(deviceauth.Header, []byte) (deviceauth.Completion, error) {
			close(entered)
			<-release
			return deviceauth.Final([]byte("done")), nil
		})
		done <- err
	}()
	<-entered
	// No directory client is configured: any unintended synchronization panics.
	// Cleanup still unblocks the original operation if that regression returns.
	defer func() {
		close(release)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	duplicateCtx, cancelDuplicate := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancelDuplicate()
	_, err = c.security.accept(duplicateCtx, wire, nil, now, func(deviceauth.Header, []byte) (deviceauth.Completion, error) {
		t.Error("duplicate executed")
		return deviceauth.Final(nil), nil
	})
	if !errors.Is(err, deviceauth.ErrOutcomeUnknown) {
		t.Fatalf("duplicate: %v", err)
	}
	if len(c.security.syncNow) != 0 {
		t.Fatal("outcome unknown requested a security-state sync")
	}
}

type interruptedScheduleBackend struct {
	Backend
	cause error
}

func (b interruptedScheduleBackend) Local() remoteagentv1connect.LocalServiceHandler {
	return interruptedScheduleLocal{cause: b.cause}
}

type interruptedScheduleLocal struct {
	remoteagentv1connect.UnimplementedLocalServiceHandler
	cause error
}

func (l interruptedScheduleLocal) Schedule(context.Context, *connect.Request[pb.ScheduleRequest]) (*connect.Response[pb.ScheduleResponse], error) {
	return nil, l.cause
}
func TestCancellationTakesPriorityOverBackendStatus(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		for _, wrap := range []bool{false, true} {
			err := cause
			if wrap {
				err = connect.NewError(connect.CodeAborted, err)
			}
			c := &conn{r: New(interruptedScheduleBackend{cause: err})}
			out := c.execute(context.Background(), "", &pb.Command{Kind: &pb.Command_Schedule{Schedule: &pb.ScheduleRequest{Operation: "pause"}}})
			want := "canceled"
			if cause == context.DeadlineExceeded {
				want = "deadline_exceeded"
			}
			if out.ErrorCode != want {
				t.Errorf("%v wrapped=%v: %s want %s", cause, wrap, out.ErrorCode, want)
			}
		}
	}
}

type canceledTerminalBackend struct {
	interruptedScheduleBackend
	dir string
}

func (canceledTerminalBackend) SessionIDs() []string                  { return []string{"test-session"} }
func (canceledTerminalBackend) OpenTerminal(string) (Terminal, error) { return nil, context.Canceled }
func (b canceledTerminalBackend) SessionDir(string) string            { return b.dir }
func TestOpenTerminalBeforeStreamIsDefinite(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "relay.key"), make([]byte, 32), 0600); err != nil {
		t.Fatal(err)
	}
	c := &conn{r: New(canceledTerminalBackend{dir: dir})}
	initializeTestSecurityKeys(t, c)
	out := c.execute(context.Background(), "test-session", &pb.Command{Kind: &pb.Command_OpenTerminal{OpenTerminal: &pb.OpenTerminalRequest{SessionId: "test-session", StreamId: "terminal-stream-request"}}})
	if out.ErrorCode != "failed_precondition" {
		t.Fatalf("pre-stream cancellation: %s", out.ErrorCode)
	}
}
