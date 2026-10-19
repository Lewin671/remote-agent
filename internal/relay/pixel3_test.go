package relay_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/contextio"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
)

type blockedDirectoryBackend struct {
	*fakeBackend
	entered, release chan struct{}
}
type blockedDirectoryLocal struct {
	remoteagentv1connect.LocalServiceHandler
	owner *blockedDirectoryBackend
}

func (b *blockedDirectoryBackend) Local() remoteagentv1connect.LocalServiceHandler {
	return &blockedDirectoryLocal{b.fakeBackend.Local(), b}
}
func (l *blockedDirectoryLocal) ListDirectories(ctx context.Context, _ *connect.Request[pb.ListDirectoriesRequest]) (*connect.Response[pb.ListDirectoriesResponse], error) {
	_, err := contextio.Read(ctx, func() ([]os.DirEntry, error) { close(l.owner.entered); <-l.owner.release; return nil, nil })
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, connect.NewError(connect.CodeDeadlineExceeded, err)
		}
		return nil, connect.NewError(connect.CodeCanceled, err)
	}
	return connect.NewResponse(&pb.ListDirectoriesResponse{}), nil
}
func TestBlockedDirectoryReadReturnsDeadlineAndDoesNotPreventReconnect(t *testing.T) {
	var streams atomic.Int32
	e := setupWithHandler(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/stream" {
				streams.Add(1)
			}
			next.ServeHTTP(w, r)
		})
	})
	backend := &blockedDirectoryBackend{e.backend, make(chan struct{}), make(chan struct{})}
	defer close(backend.release)
	r := e.startRelayBackend(backend)
	waitFor(t, "connected", r.Connected)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request, err := e.client.Seal(e.acct.DeviceID, "", "blocking-dir-request", time.Now().Add(150*time.Millisecond), &pb.Command{Kind: &pb.Command_ListDirectories{ListDirectories: &pb.ListDirectoriesRequest{Path: "/synthetic/blocked"}}})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, err := e.client.Forward(ctx, e.acct.DeviceID, request); result <- err }()
	select {
	case <-backend.entered:
	case <-ctx.Done():
		t.Fatal("reader never started")
	}
	var ce *remoteclient.CommandError
	if err := <-result; !errors.As(err, &ce) || ce.Code != "deadline_exceeded" {
		t.Fatalf("phone result=%v", err)
	}
	// The raw OS-reader replacement remains blocked through stream teardown/reconnect.
	e.dropStreams()
	waitFor(t, "replacement stream while OS read is still blocked", func() bool { return streams.Load() >= 2 && r.Connected() })
	stopped := make(chan struct{})
	go func() { e.stopRelay(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("relay Close waited for directory OS call")
	}
}
