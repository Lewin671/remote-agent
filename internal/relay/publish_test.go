package relay

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
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

// The published marker is the durable trigger for a republish: after a crash
// between installing a recovery state and re-sealing metadata, the marker is
// still old and the next start republishes. An ordinary epoch change (a
// renewal or a membership add/remove) must not republish everything.
func TestEnsurePublishedAfterRecoveryMarker(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	backend := publishBackend{}
	c := &conn{r: New(backend), acct: &Account{}}
	keys := initializeTestSecurityKeys(t, c)
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
	if err != nil || got != head.Core.Epoch {
		t.Fatalf("marker %d %v, head epoch %d", got, err, head.Core.Epoch)
	}
	if err := c.ensurePublished(ctx); err != nil {
		t.Fatal(err)
	}
	if service.puts != 1 {
		t.Fatalf("unchanged marker republished: %d", service.puts)
	}
	// A normal epoch without a recovery does not need a full republish.
	if err := writePublishedEpoch(head.Core.Epoch - 1); err != nil {
		t.Fatal(err)
	}
	if err := c.ensurePublished(ctx); err != nil {
		t.Fatal(err)
	}
	if service.puts != 1 {
		t.Fatalf("an ordinary epoch change republished: %d", service.puts)
	}

	// Install a recovery transition; now the marker must trigger a republish.
	_, newEd, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, nxpub, err := deviceauth.NewXKey()
	if err != nil {
		t.Fatal(err)
	}
	newMember, err := deviceauth.NewMember(newEd.Public().(ed25519.PublicKey), nxpub, deviceauth.Client)
	if err != nil {
		t.Fatal(err)
	}
	secret := make([]byte, 32)
	secret[0] = 9
	var newRecovery [32]byte
	newRecovery[0] = 8
	state, stmt, err := deviceauth.BuildRecovery(head, newMember, []deviceauth.Member{keys.machine, newMember}, secret, newEd, keys.recovery, newRecovery, uint64(time.Now().UnixMilli()))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.security.endpoint.Install([]deviceauth.State{state}, []*deviceauth.RecoveryStatement{&stmt}); err != nil {
		t.Fatal(err)
	}
	if err := c.ensurePublished(ctx); err != nil {
		t.Fatal(err)
	}
	if service.puts != 2 {
		t.Fatalf("recovery did not republish: %d", service.puts)
	}
	// The marker now covers the recovery, so the next check is quiet.
	if err := c.ensurePublished(ctx); err != nil {
		t.Fatal(err)
	}
	if service.puts != 2 {
		t.Fatalf("recovery republished twice: %d", service.puts)
	}
}
