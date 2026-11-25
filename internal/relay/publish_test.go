package relay

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/shared/devicesec"
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
	newMember, err := devicesec.NewMember(newEd.Public().(ed25519.PublicKey), nxpub, devicesec.Client)
	if err != nil {
		t.Fatal(err)
	}
	secret := make([]byte, 32)
	secret[0] = 9
	var newRecovery [32]byte
	newRecovery[0] = 8
	state, stmt, err := deviceauth.BuildRecovery(head, newMember, []devicesec.Member{keys.machine, newMember}, secret, newEd, keys.recovery, newRecovery, uint64(time.Now().UnixMilli()))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.security.endpoint.Install([]devicesec.State{state}, []*devicesec.RecoveryStatement{&stmt}); err != nil {
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
	member, _ := devicesec.NewMember(ed.Public().(ed25519.PublicKey), xp, devicesec.Client)
	secret := make([]byte, 32)
	secret[0] = 9
	var rec [32]byte
	rec[0] = 8
	next, proof, err := deviceauth.BuildRecovery(head, member, []devicesec.Member{keys.machine, member}, secret, ed, keys.recovery, rec, uint64(time.Now().UnixMilli()))
	if err != nil {
		t.Fatal(err)
	}
	service := &publishMachine{onPut: func() {
		if err := c.security.endpoint.Install([]devicesec.State{next}, []*devicesec.RecoveryStatement{&proof}); err != nil {
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
	h, err := devicesec.ParseHeader(r.Msg.Meta.SecurityHeader)
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
	h, err := devicesec.ParseHeader(r.Msg.Meta.SecurityHeader)
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

func (b checkpointBudgetBackend) LiveSnapshot(ctx context.Context, id string) (*pb.LiveSnapshot, error) {
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

type capturedWarnHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *capturedWarnHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelWarn
}

func (h *capturedWarnHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.records = append(h.records, r.Clone())
	h.mu.Unlock()
	return nil
}

func (h *capturedWarnHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *capturedWarnHandler) WithGroup(string) slog.Handler      { return h }

func (h *capturedWarnHandler) reset() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.records
	h.records = nil
	return out
}

type multiSessionPublishBackend struct {
	publishBackend
	ids  []string
	dirs map[string]string
}

func (b multiSessionPublishBackend) SessionIDs() []string { return b.ids }
func (b multiSessionPublishBackend) SessionInfo(id string) (*pb.Session, error) {
	return &pb.Session{Id: id}, nil
}
func (b multiSessionPublishBackend) SessionDir(id string) string { return b.dirs[id] }

type selectivePublishMachine struct {
	publishMachine
	mu       sync.Mutex
	lists    int
	putCount map[string]int
	putsByID map[string]uint64
	onPutSes func(ctx context.Context, id string) error
}

func (m *selectivePublishMachine) ListSessionHeads(context.Context, *connect.Request[pb.ListSessionHeadsRequest]) (*connect.Response[pb.ListSessionHeadsResponse], error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lists++
	var heads []*pb.SessionHead
	for id, floor := range m.putsByID {
		heads = append(heads, &pb.SessionHead{SessionId: id, MetaRevision: floor})
	}
	return connect.NewResponse(&pb.ListSessionHeadsResponse{Sessions: heads}), nil
}

func (m *selectivePublishMachine) PutSession(ctx context.Context, r *connect.Request[pb.PutSessionRequest]) (*connect.Response[pb.PutSessionResponse], error) {
	m.mu.Lock()
	m.putCount[r.Msg.SessionId]++
	cb := m.onPutSes
	m.mu.Unlock()
	if cb != nil {
		if err := cb(ctx, r.Msg.SessionId); err != nil {
			return nil, err
		}
	}
	h, err := devicesec.ParseHeader(r.Msg.Meta.SecurityHeader)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.putsByID[r.Msg.SessionId] = h.Seq
	m.mu.Unlock()
	return connect.NewResponse(&pb.PutSessionResponse{}), nil
}

func TestEnsurePublishedIsolatesSlowSessionAndConvergesAcrossPasses(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	ids := []string{"s1", "s2", "s3", "s4"}
	backend := multiSessionPublishBackend{ids: ids, dirs: map[string]string{}}
	for _, id := range ids {
		backend.dirs[id] = t.TempDir()
	}
	r := New(backend)
	r.publishSessionTimeout = 250 * time.Millisecond
	c := &conn{r: r, acct: &Account{}}
	initializeTestSecurityKeys(t, c)

	stallS2 := true
	service := &selectivePublishMachine{
		putCount: map[string]int{},
		putsByID: map[string]uint64{},
	}
	service.onPutSes = func(ctx context.Context, id string) error {
		if id == "s2" && stallS2 {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	mux := http.NewServeMux()
	path, h := remoteagentv1connect.NewMachineServiceHandler(service)
	mux.Handle(path, h)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c.machine = remoteagentv1connect.NewMachineServiceClient(http.DefaultClient, srv.URL)
	for _, id := range ids {
		if _, err := c.sessionKey(id, false); err != nil {
			t.Fatal(err)
		}
	}

	capture := &capturedWarnHandler{}
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(capture))
	defer slog.SetDefault(prevLogger)

	// Pass 1: s2 hits its independent 40ms timeout, while s1, s3, s4 all succeed within the 2s batch ctx.
	ctx1, cancel1 := context.WithTimeout(context.Background(), 2*time.Second)
	err := c.ensurePublished(ctx1)
	cancel1()
	if err == nil {
		t.Fatal("expected pass 1 to return error while s2 timed out")
	}
	for _, id := range []string{"s1", "s3", "s4"} {
		if service.putCount[id] != 1 {
			t.Fatalf("session %s putCount = %d, want 1 (slow s2 must not starve later sessions)", id, service.putCount[id])
		}
	}
	warns := capture.reset()
	if len(warns) != 1 {
		t.Fatalf("expected exactly 1 summary WARN log for pass 1, got %d", len(warns))
	}

	// Pass 2: s2 now succeeds; already-published unchanged sessions s1/s3/s4 and PutMachine must NOT be resent!
	stallS2 = false
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	err = c.ensurePublished(ctx2)
	cancel2()
	if err != nil {
		t.Fatalf("pass 2 failed: %v", err)
	}
	if service.puts != 1 {
		t.Fatalf("PutMachine resent on pass 2: count=%d, want 1", service.puts)
	}
	for _, id := range []string{"s1", "s3", "s4"} {
		if service.putCount[id] != 1 {
			t.Fatalf("unchanged session %s was resent on pass 2: putCount=%d, want 1", id, service.putCount[id])
		}
	}
	if service.putCount["s2"] != 2 {
		t.Fatalf("session s2 putCount = %d, want 2 (1 failed + 1 retry)", service.putCount["s2"])
	}

	// Pass 3: converged -> zero RPCs.
	listsBefore := service.lists
	if err := c.ensurePublished(context.Background()); err != nil {
		t.Fatalf("pass 3 failed: %v", err)
	}
	if service.lists != listsBefore {
		t.Fatalf("converged pass 3 still called ListSessionHeads")
	}
}

func TestEnsurePublishedSummarizesBatchFailureInSingleWarn(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	ids := []string{"f1", "f2", "f3", "f4", "f5"}
	backend := multiSessionPublishBackend{ids: ids, dirs: map[string]string{}}
	for _, id := range ids {
		backend.dirs[id] = t.TempDir()
	}
	c := &conn{r: New(backend), acct: &Account{}}
	initializeTestSecurityKeys(t, c)

	service := &selectivePublishMachine{
		putCount: map[string]int{},
		putsByID: map[string]uint64{},
		onPutSes: func(context.Context, string) error {
			return connect.NewError(connect.CodeUnavailable, errors.New("link loss"))
		},
	}
	mux := http.NewServeMux()
	path, h := remoteagentv1connect.NewMachineServiceHandler(service)
	mux.Handle(path, h)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c.machine = remoteagentv1connect.NewMachineServiceClient(http.DefaultClient, srv.URL)
	for _, id := range ids {
		if _, err := c.sessionKey(id, false); err != nil {
			t.Fatal(err)
		}
	}

	capture := &capturedWarnHandler{}
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(capture))
	defer slog.SetDefault(prevLogger)

	if err := c.ensurePublished(context.Background()); err == nil {
		t.Fatal("expected error when all 5 sessions fail")
	}
	warns := capture.reset()
	if len(warns) != 1 {
		t.Fatalf("expected 1 batch summary WARN for 5 failed sessions, got %d", len(warns))
	}
	var failedAttr int64
	var hasFirstErr bool
	warns[0].Attrs(func(a slog.Attr) bool {
		if a.Key == "failed" {
			failedAttr = a.Value.Int64()
		}
		if a.Key == "first_err" {
			hasFirstErr = true
		}
		return true
	})
	if failedAttr != 5 || !hasFirstErr {
		t.Fatalf("expected summary WARN with failed=5 and first_err, got failed=%d hasFirstErr=%v", failedAttr, hasFirstErr)
	}
}

func TestEnsurePublishedDirtySessionPutFailureRetriesNextPass(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	ids := []string{"s1", "s2"}
	backend := multiSessionPublishBackend{ids: ids, dirs: map[string]string{}}
	for _, id := range ids {
		backend.dirs[id] = t.TempDir()
	}
	c := &conn{r: New(backend), acct: &Account{}}
	initializeTestSecurityKeys(t, c)

	failS1 := false
	service := &selectivePublishMachine{
		putCount: map[string]int{},
		putsByID: map[string]uint64{},
	}
	service.onPutSes = func(_ context.Context, id string) error {
		if id == "s1" && failS1 {
			return connect.NewError(connect.CodeUnavailable, errors.New("transient rpc drop"))
		}
		return nil
	}
	mux := http.NewServeMux()
	path, h := remoteagentv1connect.NewMachineServiceHandler(service)
	mux.Handle(path, h)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c.machine = remoteagentv1connect.NewMachineServiceClient(http.DefaultClient, srv.URL)
	for _, id := range ids {
		if _, err := c.sessionKey(id, false); err != nil {
			t.Fatal(err)
		}
	}

	// Pass 1: both sessions publish and get cached in publishedSessions.
	if err := c.ensurePublished(context.Background()); err != nil {
		t.Fatalf("pass 1 failed: %v", err)
	}
	if service.putCount["s1"] != 1 || service.putCount["s2"] != 1 {
		t.Fatalf("pass 1 counts = %v, want s1=1 s2=1", service.putCount)
	}

	// Mark s1 dirty (simulating a checkpoint failure during ordinary sync), then make its PutSession RPC fail once.
	c.markCheckpointDirty("s1")
	failS1 = true
	if err := c.ensurePublished(context.Background()); err == nil {
		t.Fatal("pass 2 unexpectedly succeeded while s1 PutSession failed")
	}
	if service.putCount["s1"] != 2 || service.putCount["s2"] != 1 {
		t.Fatalf("pass 2 counts = %v, want s1=2 s2=1", service.putCount)
	}

	// Pass 3: PutSession recovers -> s1 MUST still be resent (not skipped via stale publishedSessions cache!).
	failS1 = false
	if err := c.ensurePublished(context.Background()); err != nil {
		t.Fatalf("pass 3 failed: %v", err)
	}
	if service.putCount["s1"] != 3 {
		t.Fatalf("pass 3 skipped previously dirty s1 after failed PutSession: putCount=%d, want 3", service.putCount["s1"])
	}
	if service.putCount["s2"] != 1 {
		t.Fatalf("pass 3 unexpectedly resent unchanged s2: putCount=%d, want 1", service.putCount["s2"])
	}
	if c.checkpointPending.Load() || c.hasDirtyCheckpoints() {
		t.Fatal("checkpoint pending/dirty state not cleared after pass 3 succeeded")
	}
}
