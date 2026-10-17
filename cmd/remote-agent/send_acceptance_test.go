package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/paths"
	"github.com/Lewin671/remote-agent/internal/protocol"
)

type sendRecordingFailure struct {
	remoteagentv1connect.UnimplementedLocalServiceHandler
	requests chan string
}

func (b *sendRecordingFailure) Send(_ context.Context, q *connect.Request[pb.SendRequest]) (*connect.Response[pb.SendResponse], error) {
	b.requests <- q.Msg.RequestId
	return nil, connect.NewError(connect.CodeUnknown, errors.New("input queue saved but event could not be recorded; do not resend with a new request ID"))
}

func TestLocalSendRecordingFailureReportsStableRequest(t *testing.T) {
	// Keep the unix socket short on macOS; no user's daemon is contacted.
	dir, err := os.MkdirTemp("", "ra-send-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	t.Setenv("REMOTE_AGENT_HOME", dir)
	ln, err := net.Listen("unix", filepath.Join(dir, "daemon.sock"))
	if err != nil {
		t.Fatal(err)
	}
	backend := &sendRecordingFailure{requests: make(chan string, 1)}
	mux := http.NewServeMux()
	mux.Handle(remoteagentv1connect.NewLocalServiceHandler(backend, connect.WithInterceptors(protocol.Local())))
	srv := &http.Server{Handler: mux}
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close(); <-done }()
	if paths.Socket() != ln.Addr().String() {
		t.Fatal("test is not connected to its isolated socket")
	}
	err = runSend([]string{"test-session", "one"})
	requestID := <-backend.requests
	if err == nil || requestID == "" || !strings.Contains(err.Error(), "send request "+requestID) || !strings.Contains(err.Error(), "queue saved but event could not be recorded") || !strings.Contains(err.Error(), "do not resend with a new request ID") {
		t.Fatalf("CLI hid the accepted request identity or recording failure: %v", err)
	}
}
