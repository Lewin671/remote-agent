package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
)

type publishBackend struct{ Backend }

func (publishBackend) SessionIDs() []string { return nil }
func (publishBackend) MachineMeta() *pb.MachineMeta {
	return &pb.MachineMeta{Hostname: "publish-test"}
}

type publishMachine struct {
	remoteagentv1connect.UnimplementedMachineServiceHandler
	puts int
}

func (m *publishMachine) PutMachine(context.Context, *connect.Request[pb.PutMachineRequest]) (*connect.Response[pb.PutMachineResponse], error) {
	m.puts++
	return connect.NewResponse(&pb.PutMachineResponse{}), nil
}

// The published-content-epoch marker is the durable trigger for a republish:
// after a crash between installing a recovery state and re-sealing metadata,
// the marker is still old and the next start republishes.
func TestEnsurePublishedAfterRecoveryMarker(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	backend := publishBackend{}
	c := &conn{r: New(backend), acct: &Account{}}
	initializeTestSecurity(t, c)
	service := &publishMachine{}
	mux := http.NewServeMux()
	path, handler := remoteagentv1connect.NewMachineServiceHandler(service)
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c.machine = remoteagentv1connect.NewMachineServiceClient(http.DefaultClient, srv.URL)
	ctx := context.Background()
	if err := c.ensurePublished(ctx); err != nil {
		t.Fatal(err)
	}
	if service.puts != 1 {
		t.Fatalf("first publish %d", service.puts)
	}
	_, head, _ := c.security.endpoint.Snapshot()
	got, err := readPublishedEpoch()
	if err != nil || got != head.Core.ContentEpoch {
		t.Fatalf("marker %d %v, head %d", got, err, head.Core.ContentEpoch)
	}
	if err := c.ensurePublished(ctx); err != nil {
		t.Fatal(err)
	}
	if service.puts != 1 {
		t.Fatalf("unchanged marker republished: %d", service.puts)
	}
	if err := writePublishedEpoch(head.Core.ContentEpoch - 1); err != nil {
		t.Fatal(err)
	}
	if err := c.ensurePublished(ctx); err != nil {
		t.Fatal(err)
	}
	if service.puts != 2 {
		t.Fatalf("stale marker did not republish: %d", service.puts)
	}
}
