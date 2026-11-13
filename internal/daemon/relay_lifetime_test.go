package daemon

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
	"google.golang.org/protobuf/proto"
)

type continuingModelBackend struct {
	interruptedModelBackend
	calls    atomic.Int32
	finished chan error
}
type continuingModelLocal struct {
	remoteagentv1connect.LocalServiceHandler
	owner *continuingModelBackend
}

func (b *continuingModelBackend) Local() remoteagentv1connect.LocalServiceHandler {
	return continuingModelLocal{b.Daemon.Local(), b}
}
func (l continuingModelLocal) SetModel(ctx context.Context, req *connect.Request[pb.SetModelRequest]) (*connect.Response[pb.SetModelResponse], error) {
	l.owner.calls.Add(1)
	res, err := l.LocalServiceHandler.SetModel(ctx, req)
	if err == nil {
		err = ctx.Err()
	}
	l.owner.finished <- err
	return res, err
}

// Real local server, encrypted WS, endpoint, and daemon native-menu handler.
// Only the terminal/CLI is simulated; it is not a real-CLI acceptance claim.
func TestMainWSLossDoesNotCancelNativeHandler(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		t.Run(fmt.Sprint("disconnect=", disconnect), func(t *testing.T) { testRunningNativeRetry(t, disconnect) })
	}
}
func testRunningNativeRetry(t *testing.T, disconnect bool) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	h, footer := controlHarness(t)
	h.d.sessions[h.s.meta.ID] = h.s
	entered, release := make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	defer unblock.Do(func() { close(release) })
	original := h.term.onWrite
	h.term.onWrite = func(f *fakeTerm, b string) {
		original(f, b)
		if b == "/model" {
			close(entered)
			<-release
		}
	}
	b := &continuingModelBackend{interruptedModelBackend: interruptedModelBackend{h.d, h.term}, finished: make(chan error, 1)}
	streams := make(chan net.Conn, 8)
	client, machine := startCancellationTestRelay(t, b, func(s *httptest.Server) {
		s.Config.ConnState = func(c net.Conn, state http.ConnState) {
			if state == http.StateHijacked {
				streams <- c
			}
		}
	})
	first := <-streams
	env, err := client.Seal(machine, h.s.meta.ID, remoteclient.NewRequestID(), time.Now().Add(time.Minute), &pb.Command{Kind: &pb.Command_SetModel{SetModel: &pb.SetModelRequest{SessionId: h.s.meta.ID, Model: "gpt-6-luna", Effort: "high"}}})
	if err != nil {
		t.Fatal(err)
	}
	firstReply := make(chan error, 1)
	go func() { _, err := client.Forward(context.Background(), machine, env); firstReply <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not enter native I/O")
	}
	if disconnect {
		if err := first.Close(); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-firstReply:
			if !remoteclient.Uncertain(err) {
				t.Fatalf("lost path gave final result: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("old waiter did not retire")
		}
		select {
		case <-streams:
		case <-time.After(5 * time.Second):
			t.Fatal("new WS waited for old execution")
		}
		// HTTP hijack precedes the encrypted authentication handshake. Observe
		// the real relay's machine registration before issuing the retry.
		eventually(t, "replacement machine authenticated", func() bool {
			machines, err := client.Machines(context.Background())
			if err != nil {
				return false
			}
			for _, m := range machines {
				if m.ID == machine && m.Online {
					return true
				}
			}
			return false
		})
	}
	// The duplicate is submitted while the native handler is held, on either
	// the original WS or the replacement WS. It waits for the same completion.
	type reply struct {
		result *pb.CommandResult
		err    error
	}
	retry := make(chan reply, 1)
	go func() { r, e := client.Forward(context.Background(), machine, env); retry <- reply{r, e} }()
	select {
	case early := <-retry:
		t.Fatalf("running retry returned before completion: %v", early.err)
	case <-time.After(50 * time.Millisecond):
	}
	unblock.Do(func() { close(release) })
	select {
	case err := <-b.finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("native operation did not complete")
	}
	var result *pb.CommandResult
	select {
	case out := <-retry:
		result, err = out.result, out.err
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("retry did not receive original result")
	}
	if !disconnect {
		if err := <-firstReply; err != nil {
			t.Fatal(err)
		}
	}
	again, err := client.Forward(context.Background(), machine, env)
	if err != nil || !proto.Equal(result, again) || result.GetSetModel() == nil {
		t.Fatalf("cached result mismatch: %v", err)
	}
	if b.calls.Load() != 1 {
		t.Fatalf("handler ran %d times", b.calls.Load())
	}
	if !strings.Contains(*footer, "GPT-6-Luna high") {
		t.Fatal("native menu did not finish")
	}
	keys := h.term.written()
	n := 0
	for _, key := range keys {
		if key == "/model" {
			n++
		}
	}
	if n != 1 || len(keys) < 2 {
		t.Fatalf("native key sequence repeated/stopped: count=%d keys=%d", n, len(keys))
	}
}
