package relay

import (
	"connectrpc.com/connect"
	"context"
	"errors"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

type recoveringPublisher struct {
	remoteagentv1connect.UnimplementedMachineServiceHandler
	c    *conn
	puts atomic.Int32
}

func (p *recoveringPublisher) PutMachine(context.Context, *connect.Request[pb.PutMachineRequest]) (*connect.Response[pb.PutMachineResponse], error) {
	if p.puts.Add(1) > 1 {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("test outage"))
	}
	return connect.NewResponse(&pb.PutMachineResponse{}), nil
}
func (p *recoveringPublisher) ListSessionHeads(context.Context, *connect.Request[pb.ListSessionHeadsRequest]) (*connect.Response[pb.ListSessionHeadsResponse], error) {
	p.c.publishNow <- struct{}{}
	return connect.NewResponse(&pb.ListSessionHeadsResponse{}), nil
}
func TestPublicationOutageBackoffBoundsLoginsAndResets(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	c := &conn{r: New(publishBackend{}), lifetime: context.Background(), publishNow: make(chan struct{}, 1)}
	initializeTestSecurityKeys(t, c)
	_, head, _ := c.security.endpoint.Snapshot()
	if err := writePublishedEpoch(head.Core.ContentEpoch); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var waits []time.Duration
	c.r.timing = retryTiming{now: func() time.Time { return now }, jitter: func(time.Duration) time.Duration { return 0 }, wait: func(_ context.Context, d time.Duration) {
		waits = append(waits, d)
		now = now.Add(d)
		if len(waits) == 4 {
			cancel()
		}
	}}
	logins := 0
	cred := &credentials{now: func() time.Time { return now }, login: func(context.Context) (string, error) {
		logins++
		if logins != 4 {
			return "", connect.NewError(connect.CodeUnavailable, errors.New("test offline"))
		}
		return "test-token", nil
	}}
	p := &recoveringPublisher{c: c}
	path, h := remoteagentv1connect.NewMachineServiceHandler(p)
	mux := http.NewServeMux()
	mux.Handle(path, h)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c.machine = remoteagentv1connect.NewMachineServiceClient(http.DefaultClient, srv.URL, connect.WithInterceptors(cred.bearer()))
	c.publishLoop(ctx)
	if want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, time.Second}; !reflect.DeepEqual(waits, want) {
		t.Fatalf("waits %v", waits)
	}
	if logins != 5 {
		t.Fatalf("login attempts not bounded/shared: %d", logins)
	}
}
