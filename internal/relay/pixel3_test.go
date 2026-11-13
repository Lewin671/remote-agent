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

// blockedDirectoryBackend's directory reads block until release. entered
// receives each blocked read's deadline.
type blockedDirectoryBackend struct {
	*fakeBackend
	entered chan time.Time
	release chan struct{}
}
type blockedDirectoryLocal struct {
	remoteagentv1connect.LocalServiceHandler
	owner *blockedDirectoryBackend
}

func (b *blockedDirectoryBackend) Local() remoteagentv1connect.LocalServiceHandler {
	return &blockedDirectoryLocal{b.fakeBackend.Local(), b}
}
func (l *blockedDirectoryLocal) ListDirectories(ctx context.Context, _ *connect.Request[pb.ListDirectoriesRequest]) (*connect.Response[pb.ListDirectoriesResponse], error) {
	deadline, _ := ctx.Deadline()
	_, err := contextio.Read(ctx, func() ([]os.DirEntry, error) {
		select {
		case l.owner.entered <- deadline:
		default:
		}
		<-l.owner.release
		return nil, nil
	})
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
	backend := &blockedDirectoryBackend{e.backend, make(chan time.Time, contextio.Limit), make(chan struct{})}
	defer close(backend.release)
	r := e.startRelayBackend(backend)
	waitFor(t, "connected", r.Connected)
	list := &pb.Command{Kind: &pb.Command_ListDirectories{ListDirectories: &pb.ListDirectoriesRequest{Path: "/synthetic/blocked"}}}
	// A read that blocked before its deadline reported it before the result
	// could return. One that only started as its deadline passed may report
	// it late; its deadline does not match a later attempt's.
	_, err := e.forwardUntilRun(t, "", 150*time.Millisecond, list, func(deadline time.Time) bool {
		for {
			select {
			case got := <-backend.entered:
				if got.UnixMilli() == deadline.UnixMilli() {
					return true
				}
			default:
				return false
			}
		}
	})
	var ce *remoteclient.CommandError
	if !errors.As(err, &ce) || ce.Code != "deadline_exceeded" || !ce.FromDaemon || remoteclient.Uncertain(err) {
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
