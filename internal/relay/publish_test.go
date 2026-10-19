package relay

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
)

type publishBackend struct{ Backend }

func (publishBackend) SessionIDs() []string     { return nil }
func (publishBackend) SessionDir(string) string { return "" }
func (publishBackend) MachineMeta() *pb.MachineMeta {
	return &pb.MachineMeta{Hostname: "publish-test"}
}

type publishMachine struct {
	remoteagentv1connect.UnimplementedMachineServiceHandler
	puts  int
	onPut func()
}

func (m *publishMachine) PutMachine(context.Context, *connect.Request[pb.PutMachineRequest]) (*connect.Response[pb.PutMachineResponse], error) {
	m.puts++
	if m.onPut != nil {
		m.onPut()
	}
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
	// Every content epoch must have its own current checkpoint.
	if err := writePublishedEpoch(head.Core.Epoch - 1); err != nil {
		t.Fatal(err)
	}
	if err := c.ensurePublished(ctx); err != nil {
		t.Fatal(err)
	}
	if service.puts != 2 {
		t.Fatalf("content epoch did not republish: %d", service.puts)
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
	lease, _ := deviceauth.BuildLease(state, newEd, state.Core.IssuedAt)
	if err := c.security.endpoint.InstallLease(lease, uint64(time.Now().UnixMilli())); err != nil {
		t.Fatal(err)
	}
	if err := c.ensurePublished(ctx); err != nil {
		t.Fatal(err)
	}
	if service.puts != 3 {
		t.Fatalf("recovery did not republish: %d", service.puts)
	}
	// The marker now covers the recovery, so the next check is quiet.
	if err := c.ensurePublished(ctx); err != nil {
		t.Fatal(err)
	}
	if service.puts != 3 {
		t.Fatalf("recovery republished twice: %d", service.puts)
	}
}

func TestPublishMarkerCapturesStartingHead(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	c := &conn{r: New(publishBackend{}), acct: &Account{}}
	keys := initializeTestSecurityKeys(t, c)
	_, head, _ := c.security.endpoint.Snapshot()
	_, ed, _ := ed25519.GenerateKey(nil)
	_, xp, _ := deviceauth.NewXKey()
	member, _ := deviceauth.NewMember(ed.Public().(ed25519.PublicKey), xp, deviceauth.Client)
	secret := make([]byte, 32)
	secret[0] = 9
	var rec [32]byte
	rec[0] = 8
	next, proof, err := deviceauth.BuildRecovery(head, member, []deviceauth.Member{keys.machine, member}, secret, ed, keys.recovery, rec, uint64(time.Now().UnixMilli()))
	if err != nil {
		t.Fatal(err)
	}
	service := &publishMachine{onPut: func() {
		if err := c.security.endpoint.Install([]deviceauth.State{next}, []*deviceauth.RecoveryStatement{&proof}); err != nil {
			t.Error(err)
		}
	}}
	mux := http.NewServeMux()
	path, h := remoteagentv1connect.NewMachineServiceHandler(service)
	mux.Handle(path, h)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c.machine = remoteagentv1connect.NewMachineServiceClient(http.DefaultClient, srv.URL)
	if err := c.ensurePublished(context.Background()); err != nil {
		t.Fatal(err)
	}
	marker, err := readPublishedEpoch()
	if err != nil || marker != head.Core.Epoch {
		t.Fatalf("marker covered an unrepublished recovery: %d %v", marker, err)
	}
	service.onPut = nil
	lease, _ := deviceauth.BuildLease(next, ed, next.Core.IssuedAt)
	if err := c.security.endpoint.InstallLease(lease, uint64(time.Now().UnixMilli())); err != nil {
		t.Fatal(err)
	}
	if err := c.ensurePublished(context.Background()); err != nil {
		t.Fatal(err)
	}
	if service.puts != 2 {
		t.Fatal("skipped recovery republish")
	}
}

type sessionRevisionMachine struct {
	remoteagentv1connect.UnimplementedMachineServiceHandler
	revisions []uint64
	floor     uint64
	validate  func(*pb.PutSessionRequest) error
}

func (m *sessionRevisionMachine) ListSessionHeads(context.Context, *connect.Request[pb.ListSessionHeadsRequest]) (*connect.Response[pb.ListSessionHeadsResponse], error) {
	floor := m.floor
	if len(m.revisions) > 0 {
		floor = m.revisions[len(m.revisions)-1]
	}
	return connect.NewResponse(&pb.ListSessionHeadsResponse{Sessions: []*pb.SessionHead{{SessionId: "synthetic-session", MetaRevision: floor}}}), nil
}
func (m *sessionRevisionMachine) PutSession(_ context.Context, r *connect.Request[pb.PutSessionRequest]) (*connect.Response[pb.PutSessionResponse], error) {
	h, err := deviceauth.ParseHeader(r.Msg.Meta.SecurityHeader)
	if err != nil {
		return nil, err
	}
	if m.validate != nil {
		if err := m.validate(r.Msg); err != nil {
			return nil, err
		}
	}
	m.revisions = append(m.revisions, h.Seq)
	return connect.NewResponse(&pb.PutSessionResponse{}), nil
}
func TestSessionMetadataRevisionsAdvanceForRepublishes(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	c := &conn{r: New(publishBackend{}), acct: &Account{}}
	initializeTestSecurityKeys(t, c)
	service := &sessionRevisionMachine{floor: 1 << 60}
	mux := http.NewServeMux()
	path, h := remoteagentv1connect.NewMachineServiceHandler(service)
	mux.Handle(path, h)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c.machine = remoteagentv1connect.NewMachineServiceClient(http.DefaultClient, srv.URL)
	for i := 0; i < 3; i++ {
		if _, err := c.putSession(context.Background(), "synthetic-session", make([]byte, 32), &pb.Session{Id: "synthetic-session"}, service.floor); err != nil {
			t.Fatal(err)
		}
	}
	if len(service.revisions) != 3 || service.revisions[0] <= service.floor || service.revisions[1] <= service.revisions[0] || service.revisions[2] <= service.revisions[1] {
		t.Fatalf("non-increasing session metadata revisions: %v", service.revisions)
	}
}

type batchPublishBackend struct {
	publishBackend
	dirs map[string]string
}

func (b batchPublishBackend) SessionIDs() []string {
	return []string{"batch-session-1", "batch-session-2", "batch-session-3"}
}
func (b batchPublishBackend) SessionInfo(id string) (*pb.Session, error) {
	return &pb.Session{Id: id}, nil
}
func (b batchPublishBackend) SessionDir(id string) string { return b.dirs[id] }

type batchPublishMachine struct {
	publishMachine
	lists    int
	putsByID map[string]uint64
}

func (m *batchPublishMachine) ListSessionHeads(context.Context, *connect.Request[pb.ListSessionHeadsRequest]) (*connect.Response[pb.ListSessionHeadsResponse], error) {
	m.lists++
	var heads []*pb.SessionHead
	for id, floor := range m.putsByID {
		heads = append(heads, &pb.SessionHead{SessionId: id, MetaRevision: floor})
	}
	return connect.NewResponse(&pb.ListSessionHeadsResponse{Sessions: heads}), nil
}
func (m *batchPublishMachine) PutSession(_ context.Context, r *connect.Request[pb.PutSessionRequest]) (*connect.Response[pb.PutSessionResponse], error) {
	h, err := deviceauth.ParseHeader(r.Msg.Meta.SecurityHeader)
	if err != nil {
		return nil, err
	}
	if h.Seq <= m.putsByID[r.Msg.SessionId] {
		return nil, fmt.Errorf("revision did not exceed batch floor")
	}
	m.putsByID[r.Msg.SessionId] = h.Seq
	return connect.NewResponse(&pb.PutSessionResponse{}), nil
}
func TestRecoveryRepublishFetchesRevisionFloorsOncePerBatch(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	backend := batchPublishBackend{dirs: map[string]string{}}
	for _, id := range backend.SessionIDs() {
		backend.dirs[id] = t.TempDir()
	}
	c := &conn{r: New(backend), acct: &Account{}}
	initializeTestSecurityKeys(t, c)
	service := &batchPublishMachine{putsByID: map[string]uint64{}}
	for _, id := range backend.SessionIDs() {
		service.putsByID[id] = 1 << 60
	}
	mux := http.NewServeMux()
	path, h := remoteagentv1connect.NewMachineServiceHandler(service)
	mux.Handle(path, h)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c.machine = remoteagentv1connect.NewMachineServiceClient(http.DefaultClient, srv.URL)
	for _, id := range backend.SessionIDs() {
		if _, err := c.sessionKey(id, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.ensurePublished(context.Background()); err != nil {
		t.Fatal(err)
	}
	if service.lists != 1 {
		t.Fatalf("downloaded full session heads %d times for one batch", service.lists)
	}
}

// A checkpoint failure after the epoch marker was written must still be retried,
// even when the session metadata has not changed.
func TestEnsurePublishedRetriesCheckpointWithCurrentMarker(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	backend := batchPublishBackend{dirs: map[string]string{}}
	for _, id := range backend.SessionIDs() {
		backend.dirs[id] = t.TempDir()
	}
	c := &conn{r: New(backend), acct: &Account{}}
	initializeTestSecurityKeys(t, c)
	service := &batchPublishMachine{putsByID: map[string]uint64{}}
	mux := http.NewServeMux()
	path, h := remoteagentv1connect.NewMachineServiceHandler(service)
	mux.Handle(path, h)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c.machine = remoteagentv1connect.NewMachineServiceClient(http.DefaultClient, srv.URL)
	for _, id := range backend.SessionIDs() {
		if _, err := c.sessionKey(id, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.ensurePublished(context.Background()); err != nil {
		t.Fatal(err)
	}
	id := backend.SessionIDs()[0]
	p := filepath.Join(backend.SessionDir(id), "current-checkpoint.pb")
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(p, 0700); err != nil {
		t.Fatal(err)
	}
	key, err := c.sessionKey(id, false)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := backend.SessionInfo(id)
	if _, err := c.putSession(context.Background(), id, key, info, service.putsByID[id]); err != nil {
		t.Fatal(err)
	}
	if err := c.ensurePublished(context.Background()); err == nil {
		t.Fatal("failed checkpoint marked complete")
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	before := service.lists
	if err := c.ensurePublished(context.Background()); err != nil {
		t.Fatal(err)
	}
	if service.lists != before+1 || c.checkpointPending.Load() {
		t.Fatal("unchanged marker skipped pending checkpoint")
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
}

type checkpointBudgetBackend struct {
	publishBackend
	questionBytes int
}

func (b checkpointBudgetBackend) LiveSnapshot(id string) (*pb.LiveSnapshot, error) {
	return &pb.LiveSnapshot{Session: &pb.Session{Id: id}, Actions: []*pb.Event{{SessionId: id, Seq: 1, Kind: &pb.Event_QuestionAsked{QuestionAsked: &pb.QuestionAsked{QuestionId: "budget", Questions: []*pb.Question{{Text: strings.Repeat("q", b.questionBytes)}}}}}}}, nil
}

// The encrypted metadata contains both the current snapshot and the signed
// replacement lineage. A legal snapshot alone need not fit their combined cap.
func TestCheckpointCombinedMetadataBudgetKeepsOrdinarySyncAvailable(t *testing.T) {
	for _, size := range []int{5 << 10, 40 << 10} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			c := &conn{r: New(checkpointBudgetBackend{questionBytes: size}), acct: &Account{}}
			initializeTestSecurityKeys(t, c)
			for i := 0; i < 64; i++ {
				c.acct.Replacements = append(c.acct.Replacements, &pb.MachineReplacementProof{Approval: make([]byte, 512), Signature: make([]byte, 64)})
			}
			service := &sessionRevisionMachine{validate: func(request *pb.PutSessionRequest) error {
				wire, err := deviceauth.FromEnvelope(request.Meta)
				if err != nil {
					return err
				}
				_, _, err = c.security.endpoint.Open(wire, nil, false, uint64(time.Now().UnixMilli()))
				return err
			}}
			mux := http.NewServeMux()
			path, h := remoteagentv1connect.NewMachineServiceHandler(service)
			mux.Handle(path, h)
			srv := httptest.NewServer(mux)
			defer srv.Close()
			c.machine = remoteagentv1connect.NewMachineServiceClient(http.DefaultClient, srv.URL)
			if _, err := c.putSession(context.Background(), "budget-session", make([]byte, 32), &pb.Session{Id: "budget-session"}, 0); err != nil {
				t.Fatalf("checkpoint budget blocked ordinary metadata: %v", err)
			}
			if c.checkpointPending.Load() != (size == 40<<10) || len(service.revisions) != 1 {
				t.Fatal("oversize checkpoint not retained independently of metadata")
			}
		})
	}
}
