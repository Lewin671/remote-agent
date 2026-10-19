package relay_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"github.com/Lewin671/remote-agent/internal/eventlog"
	"github.com/Lewin671/remote-agent/internal/protocol"
	"github.com/Lewin671/remote-agent/internal/relay"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
	"github.com/Lewin671/remote-agent/internal/server"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// marker is plaintext that must never reach the server's database.
const marker = "SECRET-MARKER-7f3a"

type fakeBackend struct {
	dir string

	mu            sync.Mutex
	sessions      map[string]*eventlog.Log
	titles        map[string]string
	exited        map[string]bool
	forked        map[string]uint64 // the seq of a fork's SessionForked event
	sends         []*pb.SendRequest
	schedules     []*pb.ScheduleRequest
	term          *fakeTerminal
	resolved      []*pb.ResolveApprovalRequest
	stopDeadlines chan time.Time
	// images are display copies by image ID; imageResults the IDs whose
	// upload finished, in order.
	origins []*pb.Origin
	// machineMeta replaces the machine's metadata when set.
	machineMeta  func() *pb.MachineMeta
	images       map[string]relay.ImageUpload
	imageResults []string
}

func newFakeBackend(t *testing.T) *fakeBackend {
	return &fakeBackend{dir: t.TempDir(), sessions: map[string]*eventlog.Log{}, titles: map[string]string{}, exited: map[string]bool{}, forked: map[string]uint64{}, images: map[string]relay.ImageUpload{}, stopDeadlines: make(chan time.Time, 1)}
}

func (b *fakeBackend) add(t *testing.T, id string) *eventlog.Log {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(b.dir, id), 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := eventlog.Open(filepath.Join(b.dir, id, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	b.mu.Lock()
	b.sessions[id] = l
	b.mu.Unlock()
	return l
}

func (b *fakeBackend) SessionIDs() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var ids []string
	for id := range b.sessions {
		ids = append(ids, id)
	}
	return ids
}

func (b *fakeBackend) log(id string) (*eventlog.Log, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	l, ok := b.sessions[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return l, nil
}

func (b *fakeBackend) SessionInfo(id string) (*pb.Session, error) {
	l, err := b.log(id)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	status := pb.SessionStatus_SESSION_STATUS_IDLE
	if b.exited[id] {
		status = pb.SessionStatus_SESSION_STATUS_EXITED
	}
	return &pb.Session{Id: id, Cwd: "/work/" + marker, Status: status, LastSeq: l.LastSeq(), Title: b.titles[id], ForkedSeq: b.forked[id]}, nil
}

func (b *fakeBackend) EachEvents(id string, after uint64, fn func(*pb.Event) error) (<-chan struct{}, error) {
	l, err := b.log(id)
	if err != nil {
		return nil, err
	}
	return l.EachSince(after, fn)
}

func (b *fakeBackend) SessionDir(id string) string { return filepath.Join(b.dir, id) }

func (b *fakeBackend) OpenTerminal(id string) (relay.Terminal, error) {
	if _, err := b.log(id); err != nil {
		return nil, err
	}
	t := &fakeTerminal{out: make(chan []byte, 16), done: make(chan struct{}), input: make(chan []byte, 16), resized: make(chan string, 4)}
	b.mu.Lock()
	b.term = t
	b.mu.Unlock()
	return t, nil
}

type fakeTerminal struct {
	out     chan []byte
	done    chan struct{}
	input   chan []byte
	resized chan string
	once    sync.Once
}

func (t *fakeTerminal) History(req *pb.TerminalHistoryRequest) (*pb.TerminalHistoryPage, error) {
	return &pb.TerminalHistoryPage{RequestId: req.RequestId, Lines: []string{"history " + marker}}, nil
}
func (t *fakeTerminal) Snapshot() (*pb.TerminalSnapshot, error) {
	return &pb.TerminalSnapshot{Data: []byte("screen " + marker), Cols: 80, Rows: 24}, nil
}
func (t *fakeTerminal) Output() <-chan []byte        { return t.out }
func (t *fakeTerminal) SizeChanged() <-chan struct{} { return nil }
func (t *fakeTerminal) Done() <-chan struct{}        { return t.done }
func (t *fakeTerminal) Reason() string               { return "gone" }
func (t *fakeTerminal) Input(b []byte) error         { t.input <- b; return nil }
func (t *fakeTerminal) Resize(c, r int, fit bool) error {
	t.resized <- fmt.Sprintf("%dx%d %v", c, r, fit)
	return nil
}
func (t *fakeTerminal) Close() error { t.once.Do(func() { close(t.done) }); return nil }

func (b *fakeBackend) MachineMeta() *pb.MachineMeta {
	b.mu.Lock()
	meta := b.machineMeta
	b.mu.Unlock()
	if meta != nil {
		return meta()
	}
	host, _ := os.Hostname()
	return &pb.MachineMeta{Hostname: host}
}

func (b *fakeBackend) Local() remoteagentv1connect.LocalServiceHandler { return &fakeLocal{b: b} }

func (b *fakeBackend) Image(sessionID, imageID string) relay.ImageUpload {
	b.mu.Lock()
	defer b.mu.Unlock()
	u, ok := b.images[imageID]
	if !ok {
		return relay.ImageUpload{Failed: true}
	}
	return u
}

func (b *fakeBackend) ImageUploaded(sessionID, imageID string, ok bool, expiresMs int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.imageResults = append(b.imageResults, imageID)
	if ok {
		b.images[imageID] = relay.ImageUpload{Done: true}
	} else {
		b.images[imageID] = relay.ImageUpload{Failed: true}
	}
}

type fakeLocal struct {
	remoteagentv1connect.UnimplementedLocalServiceHandler
	b *fakeBackend
}

func (f *fakeLocal) Send(ctx context.Context, req *connect.Request[pb.SendRequest]) (*connect.Response[pb.SendResponse], error) {
	f.b.mu.Lock()
	f.b.sends = append(f.b.sends, req.Msg)
	f.b.origins = append(f.b.origins, agent.OriginFrom(ctx))
	n := len(f.b.sends)
	f.b.mu.Unlock()
	return connect.NewResponse(&pb.SendResponse{QueuePosition: int32(n)}), nil
}

func (f *fakeLocal) RenameSession(ctx context.Context, req *connect.Request[pb.RenameSessionRequest]) (*connect.Response[pb.RenameSessionResponse], error) {
	l, err := f.b.log(req.Msg.SessionId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	f.b.mu.Lock()
	f.b.titles[req.Msg.SessionId] = req.Msg.Title
	f.b.mu.Unlock()
	l.Poke()
	return connect.NewResponse(&pb.RenameSessionResponse{}), nil
}

func (f *fakeLocal) ResolveApproval(ctx context.Context, req *connect.Request[pb.ResolveApprovalRequest]) (*connect.Response[pb.ResolveApprovalResponse], error) {
	f.b.mu.Lock()
	defer f.b.mu.Unlock()
	for _, r := range f.b.resolved {
		if r.ApprovalId == req.Msg.ApprovalId {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("approval not pending"))
		}
	}
	f.b.resolved = append(f.b.resolved, req.Msg)
	return connect.NewResponse(&pb.ResolveApprovalResponse{}), nil
}

func (f *fakeLocal) StopSession(ctx context.Context, _ *connect.Request[pb.StopSessionRequest]) (*connect.Response[pb.StopSessionResponse], error) {
	deadline, _ := ctx.Deadline()
	select {
	case f.b.stopDeadlines <- deadline:
	default:
	}
	<-ctx.Done()
	return nil, connect.NewError(connect.CodeDeadlineExceeded, errors.New("stop reached its command deadline"))
}

func (b *fakeBackend) sendCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.sends)
}

func appendText(t *testing.T, l *eventlog.Log, id, text string) {
	t.Helper()
	err := l.Append(id, []*pb.Event{{Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Text: text}}}}, "", 0)
	if err != nil {
		t.Fatal(err)
	}
}

type env struct {
	srv       *server.Server
	http      *httptest.Server
	dbPath    string
	backend   *fakeBackend
	acct      *relay.Account
	client    *remoteclient.Client
	stop      context.CancelFunc
	done      chan struct{}
	streamsMu sync.Mutex
	streams   map[net.Conn]bool
}

func setup(t *testing.T) *env {
	return setupWithHandler(t, func(h http.Handler) http.Handler { return h })
}

func setupWithHandler(t *testing.T, wrap func(http.Handler) http.Handler, configure ...func(*server.Config)) *env {
	t.Helper()
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	e := &env{dbPath: filepath.Join(t.TempDir(), "ra.db"), backend: newFakeBackend(t)}
	var err error
	cfg := server.Config{DBPath: e.dbPath, SetupToken: "setup"}
	for _, f := range configure {
		f(&cfg)
	}
	e.srv, err = server.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	e.streams = map[net.Conn]bool{}
	e.http = httptest.NewUnstartedServer(wrap(e.srv.Handler()))
	e.http.Config.ConnState = func(c net.Conn, state http.ConnState) {
		if state == http.StateHijacked {
			e.streamsMu.Lock()
			e.streams[c] = true
			e.streamsMu.Unlock()
		}
	}
	e.http.Start()
	t.Cleanup(func() {
		e.stopRelay()
		e.dropStreams()
		e.http.Close()
		e.srv.Close()
	})
	ctx := context.Background()
	if _, err := remoteclient.NewAuthority(ctx, e.http.URL, "wrong"); err == nil {
		t.Fatal("made an account with a wrong setup token")
	}
	authority, err := remoteclient.NewAuthority(ctx, e.http.URL, "setup")
	if err != nil {
		t.Fatal(err)
	}
	_, med, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	mx, _, err := deviceauth.NewXKey()
	if err != nil {
		t.Fatal(err)
	}
	machine, err := authority.Enroll(ctx, med, mx, deviceauth.Machine)
	if err != nil {
		t.Fatal(err)
	}
	e.acct = &relay.Account{Server: authority.Server, Root: authority.Root, DeviceSeed: med.Seed(), DeviceID: machine.ID,
		Security: &relay.SecurityConfig{Pin: authority.Pin, XPrivate: mx}}
	if err := machine.Endpoint.Save(filepath.Join(os.Getenv("REMOTE_AGENT_HOME"), "security-v3.db")); err != nil {
		t.Fatal(err)
	}
	if err := e.acct.Save(); err != nil {
		t.Fatal(err)
	}
	_, ced, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	cx, _, err := deviceauth.NewXKey()
	if err != nil {
		t.Fatal(err)
	}
	client, err := authority.Enroll(ctx, ced, cx, deviceauth.Client)
	if err != nil {
		t.Fatal(err)
	}
	e.client = client.Client(authority)
	t.Cleanup(func() {
		if e.client != nil {
			e.client.Close()
		}
	})
	return e
}

// httptest.CloseClientConnections excludes hijacked WebSocket connections.
// Close them explicitly when a test needs an actual stream disconnect.
func (e *env) dropStreams() {
	e.streamsMu.Lock()
	defer e.streamsMu.Unlock()
	for c := range e.streams {
		_ = c.Close()
		delete(e.streams, c)
	}
}

func TestManySessionsSyncWithinServerRequestLimit(t *testing.T) {
	var active, peak atomic.Int32
	e := setupWithHandler(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/remoteagent.v1.MachineService/") {
				n := active.Add(1)
				defer active.Add(-1)
				for old := peak.Load(); n > old; old = peak.Load() {
					if peak.CompareAndSwap(old, n) {
						break
					}
				}
				select {
				case <-r.Context().Done():
					return
				case <-time.After(25 * time.Millisecond):
				}
			}
			next.ServeHTTP(w, r)
		})
	})
	for i := range 64 {
		id := fmt.Sprintf("history-%03d", i)
		appendText(t, e.backend.add(t, id), id, "history")
	}
	e.startRelay()
	waitFor(t, "all historical sessions uploaded", func() bool {
		ss, err := e.client.Sessions(context.Background())
		if err != nil || len(ss) != 64 {
			return false
		}
		for _, s := range ss {
			if s.LastSeq != 1 {
				return false
			}
		}
		return true
	})
	if n := peak.Load(); n > 2 {
		t.Fatalf("concurrent machine RPCs = %d; want at most 2", n)
	}
	// A live session's long-lived watcher must not retain an RPC slot.
	appendText(t, e.backend.add(t, "new-session"), "new-session", "new")
	e.session(t, "new-session", 1)
	log, err := e.backend.log("history-000")
	if err != nil {
		t.Fatal(err)
	}
	appendText(t, log, "history-000", "updated")
	e.session(t, "history-000", 2)
}

// Keep the same scheduling paths with a shorter per-instance interval;
// integration tests synchronize on effects rather than five-second sleeps.
const testSyncInterval = 20 * time.Millisecond

func newTestRelay(backend relay.Backend) *relay.Relay {
	r := relay.New(backend)
	r.Retry = 50 * time.Millisecond
	relay.SetSyncIntervalForTest(r, testSyncInterval)
	return r
}

// A second uploaded, non-notifying event proves the preceding pass (including
// notification handling) has finished. This checks no duplicate was sent on a
// later pass without guessing when a sleeping worker will wake up.
func (e *env) syncLaterPasses(t *testing.T, l *eventlog.Log, id string) {
	t.Helper()
	for range 2 {
		appendText(t, l, id, "sync barrier")
		e.session(t, id, l.LastSeq())
	}
}

func (e *env) startRelay() *relay.Relay {
	return e.startRelayBackend(e.backend)
}

func (e *env) startRelayBackend(backend relay.Backend) *relay.Relay {
	ctx, cancel := context.WithCancel(context.Background())
	e.stop = cancel
	e.done = make(chan struct{})
	r := newTestRelay(backend)
	go func() { defer close(e.done); r.Run(ctx) }()
	return r
}

func (e *env) stopRelay() {
	if e.stop != nil {
		e.stop()
		<-e.done
		e.stop = nil
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (e *env) session(t *testing.T, id string, lastSeq uint64) remoteclient.Session {
	t.Helper()
	var found remoteclient.Session
	waitFor(t, "session "+id, func() bool {
		ss, err := e.client.Sessions(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range ss {
			if s.ID == id && s.LastSeq >= lastSeq && s.Info != nil {
				found = s
				return true
			}
		}
		return false
	})
	return found
}

func (e *env) online(t *testing.T, want bool) {
	t.Helper()
	waitFor(t, "machine online state", func() bool {
		ms, err := e.client.Machines(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return len(ms) == 1 && ms[0].Online == want
	})
}

func TestEventsReachClientsInOrder(t *testing.T) {
	e := setup(t)
	l := e.backend.add(t, "sess1")
	for i := range 3 {
		appendText(t, l, "sess1", marker+string(rune('a'+i)))
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := e.client.Stream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	e.startRelay()
	s := e.session(t, "sess1", 3)
	if s.Info.Cwd != "/work/"+marker {
		t.Fatalf("metadata = %v", s.Info)
	}
	evs, err := e.client.Events(ctx, s, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 3 || evs[2].GetAgentMessage().Text != marker+"c" {
		t.Fatalf("events = %v", evs)
	}

	// New events are pushed.
	appendText(t, l, "sess1", "live")
	deadline := time.After(10 * time.Second)
	for got := false; !got; {
		select {
		case f, ok := <-stream:
			if !ok {
				t.Fatal("client stream closed before the live event")
			}
			if ea := f.GetEventsAppended(); ea != nil && ea.SessionId == "sess1" {
				for _, se := range ea.Events {
					ev, err := e.client.OpenEvent(s, se)
					if err != nil {
						t.Fatal(err)
					}
					if ev.Seq == 4 && ev.GetAgentMessage().Text == "live" {
						got = true
					}
				}
			}
		case <-deadline:
			t.Fatal("no push for the new event")
		}
	}
	if m, _ := e.client.Machines(ctx); len(m) != 1 || m[0].Meta == nil || m[0].Meta.Hostname == "" {
		t.Fatalf("machines = %v", m)
	}

	// The server only has ciphertext.
	e.stopRelay()
	e.srv.Close()
	for _, name := range []string{e.dbPath, e.dbPath + "-wal"} {
		b, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		if bytes.Contains(b, []byte(marker)) || bytes.Contains(b, []byte("live")) {
			t.Fatalf("%s contains plaintext", name)
		}
	}
}

// A restarted daemon does not send an ended session's metadata again
// while the server still holds what it sent; a change still goes up.
func TestEndedSessionMetadataIsNotSentAgainAfterRestart(t *testing.T) {
	var puts sync.Map
	count := func(id string) int {
		n, _ := puts.Load(id)
		c, _ := n.(*atomic.Int32)
		if c == nil {
			return 0
		}
		return int(c.Load())
	}
	e := setupWithHandler(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/PutSession") {
				body, _ := io.ReadAll(r.Body)
				r.Body = io.NopCloser(bytes.NewReader(body))
				for _, id := range []string{"live", "done"} {
					if bytes.Contains(body, []byte(id)) {
						n, _ := puts.LoadOrStore(id, &atomic.Int32{})
						n.(*atomic.Int32).Add(1)
					}
				}
			}
			next.ServeHTTP(w, r)
		})
	})
	e.backend.exited["done"] = true
	appendText(t, e.backend.add(t, "live"), "live", "one")
	done := e.backend.add(t, "done")
	appendText(t, done, "done", "one")
	e.startRelay()
	e.session(t, "live", 1)
	e.session(t, "done", 1)
	if count("live") != 1 || count("done") != 1 {
		t.Fatalf("puts: live %d, done %d; want 1 each", count("live"), count("done"))
	}

	e.stopRelay()
	e.startRelay()
	waitFor(t, "the live session's metadata after the restart", func() bool { return count("live") == 2 })
	// New events of the ended session still go up, without its metadata.
	appendText(t, done, "done", "two")
	e.session(t, "done", 2)
	if n := count("done"); n != 2 {
		t.Fatalf("ended session checkpoint after restart was put %d times; want 2", n)
	}

	e.backend.mu.Lock()
	e.backend.titles["done"] = "renamed"
	e.backend.mu.Unlock()
	done.Poke()
	waitFor(t, "the changed metadata", func() bool { return e.session(t, "done", 2).Info.Title == "renamed" })
	if n := count("done"); n != 3 {
		t.Fatalf("ended session checkpoint after rename was put %d times; want 3", n)
	}
}

func TestUploadResumesAfterReconnect(t *testing.T) {
	e := setup(t)
	l := e.backend.add(t, "sess1")
	appendText(t, l, "sess1", "one")
	e.startRelay()
	e.session(t, "sess1", 1)
	e.stopRelay()
	e.online(t, false)

	appendText(t, l, "sess1", "two")
	appendText(t, l, "sess1", "three")
	l2 := e.backend.add(t, "sess2")
	appendText(t, l2, "sess2", "other")
	e.startRelay()
	s := e.session(t, "sess1", 3)
	e.session(t, "sess2", 1)
	evs, err := e.client.Events(context.Background(), s, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 3 || evs[1].GetAgentMessage().Text != "two" {
		t.Fatalf("events = %v", evs)
	}
}

func TestCommandsRunOnce(t *testing.T) {
	e := setup(t)
	e.backend.add(t, "sess1")
	e.startRelay()
	s := e.session(t, "sess1", 0)
	e.online(t, true)
	ctx := context.Background()

	send := &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: "sess1", Text: "hi", RequestId: "r1"}}}
	cmd, err := e.client.Seal(s.MachineID, s.ID, remoteclient.NewRequestID(), time.Now().Add(time.Minute), send)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		res, err := e.client.Forward(ctx, s.MachineID, cmd)
		if err != nil {
			t.Fatal(err)
		}
		if res.GetSend().QueuePosition != 1 {
			t.Fatalf("result = %v", res)
		}
	}
	if n := e.backend.sendCount(); n != 1 {
		t.Fatalf("send ran %d times", n)
	}

	// The same request ID is remembered across a daemon restart.
	e.stopRelay()
	// The server observes closure asynchronously. Do not let the old online
	// state satisfy the readiness check for the replacement connection.
	e.online(t, false)
	e.startRelay()
	e.online(t, true)
	// The accepted Command log is durable: the same request returns the
	// recorded result without running again.
	if res, err := e.client.Forward(ctx, s.MachineID, cmd); err != nil || res.GetSend().QueuePosition != 1 {
		t.Fatalf("retry after restart: %v %v", res, err)
	}
	if n := e.backend.sendCount(); n != 1 {
		t.Fatalf("send ran %d times", n)
	}

	// Errors come back sealed; a retry gets the same error.
	resolve := &pb.Command{Kind: &pb.Command_ResolveApproval{ResolveApproval: &pb.ResolveApprovalRequest{SessionId: "sess1", ApprovalId: "a1", Allow: true}}}
	if _, err := e.client.Do(ctx, s, resolve); err != nil {
		t.Fatal(err)
	}
	var ce *remoteclient.CommandError
	if _, err := e.client.Do(ctx, s, resolve); !errors.As(err, &ce) || ce.Code != "not_found" {
		t.Fatalf("second resolve: %v", err)
	}

	// A command for another session than its header names is refused.
	other := &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: "sess2", Text: "x"}}}
	if _, err := e.client.Do(ctx, s, other); !errors.As(err, &ce) || ce.Code != "not_found" {
		t.Fatalf("mismatched session: %v", err)
	}
}

func TestRenameReachesTheClients(t *testing.T) {
	e := setup(t)
	e.backend.add(t, "sess1")
	e.startRelay()
	s := e.session(t, "sess1", 0)
	e.online(t, true)

	rename := &pb.Command{Kind: &pb.Command_RenameSession{RenameSession: &pb.RenameSessionRequest{SessionId: "sess1", Title: "登录超时"}}}
	res, err := e.client.Do(context.Background(), s, rename)
	if err != nil || res.GetRenameSession() == nil {
		t.Fatalf("rename: %v %v", res, err)
	}
	// Without waiting for the next event or the periodic look.
	waitFor(t, "the new title", func() bool {
		ss, err := e.client.Sessions(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return len(ss) == 1 && ss[0].Info.GetTitle() == "登录超时"
	})
}

func TestStopUsesTheSealedCommandDeadline(t *testing.T) {
	e := setup(t)
	e.backend.add(t, "sess1")
	e.startRelay()
	s := e.session(t, "sess1", 0)
	e.online(t, true)

	deadline := time.UnixMilli(time.Now().Add(350 * time.Millisecond).UnixMilli())
	cmd, err := e.client.Seal(s.MachineID, s.ID, remoteclient.NewRequestID(), deadline,
		&pb.Command{Kind: &pb.Command_StopSession{StopSession: &pb.StopSessionRequest{SessionId: s.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := time.Now()
	_, err = e.client.Forward(ctx, s.MachineID, cmd)
	var commandErr *remoteclient.CommandError
	if !errors.As(err, &commandErr) || commandErr.Code != connect.CodeDeadlineExceeded.String() {
		t.Fatalf("stop result = %v, want command deadline exceeded", err)
	}
	select {
	case got := <-e.backend.stopDeadlines:
		if got.IsZero() || got.UnixMilli() != deadline.UnixMilli() {
			t.Fatalf("local stop deadline = %v, want %v", got, deadline)
		}
	case <-time.After(time.Second):
		t.Fatal("local stop did not receive the command deadline")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("stop took %s past its 350ms command deadline", elapsed)
	}
}

func TestCommandsRejected(t *testing.T) {
	e := setup(t)
	e.backend.add(t, "sess1")
	e.startRelay()
	s := e.session(t, "sess1", 0)
	e.online(t, true)
	ctx := context.Background()
	send := &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: "sess1", Text: "hi"}}}

	expired, err := e.client.Seal(s.MachineID, s.ID, remoteclient.NewRequestID(), time.Now().Add(-time.Second), send)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.client.Forward(ctx, s.MachineID, expired); err == nil {
		t.Fatal("expired command was executed")
	}
	future, err := e.client.Seal(s.MachineID, s.ID, remoteclient.NewRequestID(), time.Now().Add(time.Hour), send)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.client.Forward(ctx, s.MachineID, future); err == nil {
		t.Fatal("far-future command was executed")
	}

	// A tampered command does not open.
	good, _ := e.client.Seal(s.MachineID, s.ID, remoteclient.NewRequestID(), time.Now().Add(time.Minute), send)
	good.Ciphertext[0] ^= 1
	if _, err := e.client.Forward(ctx, s.MachineID, good); connect.CodeOf(err) != connect.CodeUnknown {
		t.Fatalf("tampered: %v", err)
	}
	// A command sealed with another account's key does not open.
	stranger, _ := e2e.Derive(e2e.NewKey())
	forged, _ := e2e.SealMessage(stranger.Command, &pb.Header{Version: deviceauth.Version,
		Kind: pb.PayloadKind_PAYLOAD_KIND_COMMAND, MachineId: s.MachineID, SessionId: s.ID,
		RequestId: remoteclient.NewRequestID(), DeadlineMs: time.Now().Add(time.Minute).UnixMilli(), Sender: e.client.DeviceID,
	}, send)
	if _, err := e.client.Forward(ctx, s.MachineID, forged); connect.CodeOf(err) != connect.CodeUnknown {
		t.Fatalf("forged: %v", err)
	}
	if n := e.backend.sendCount(); n != 0 {
		t.Fatalf("send ran %d times", n)
	}

	// Sealing is local; an offline machine still fails the forward at once
	// and nothing is queued for later.
	e.stopRelay()
	e.online(t, false)
	offline, err := e.client.Seal(s.MachineID, s.ID, remoteclient.NewRequestID(), time.Now().Add(time.Minute), send)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.client.Forward(ctx, s.MachineID, offline); err == nil {
		t.Fatal("offline machine accepted a command")
	}

}

func TestLostSessionKeyIsNotReplaced(t *testing.T) {
	e := setup(t)
	l := e.backend.add(t, "sess1")
	appendText(t, l, "sess1", "one")
	l2 := e.backend.add(t, "sess2")
	e.startRelay()
	s := e.session(t, "sess1", 1)
	e.session(t, "sess2", 0)
	e.stopRelay()

	// The key file is lost; new events must not be sealed with a new key.
	if err := os.Remove(filepath.Join(e.backend.SessionDir("sess1"), "relay.key")); err != nil {
		t.Fatal(err)
	}
	appendText(t, l, "sess1", "two")
	appendText(t, l2, "sess2", "other")
	e.startRelay()
	e.session(t, "sess2", 1) // other sessions keep syncing
	ss, err := e.client.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range ss {
		if x.ID == "sess1" && x.LastSeq != 1 {
			t.Fatalf("sess1 has %d events on the server", x.LastSeq)
		}
	}
	if _, err := os.Stat(filepath.Join(e.backend.SessionDir("sess1"), "relay.key")); !os.IsNotExist(err) {
		t.Fatalf("a new key was created: %v", err)
	}
	evs, err := e.client.Events(context.Background(), s, 0)
	if err != nil || len(evs) != 1 {
		t.Fatalf("events: %v %v", evs, err)
	}
}

func TestMachineCommands(t *testing.T) {
	e := setup(t)
	e.backend.add(t, "sess1")
	e.startRelay()
	s := e.session(t, "sess1", 0)
	e.online(t, true)
	ctx := context.Background()
	list := &pb.Command{Kind: &pb.Command_ListModels{ListModels: &pb.ListModelsRequest{}}}
	// A machine command sealed for a session is refused.
	var ce *remoteclient.CommandError
	if _, err := e.client.Do(ctx, s, list); !errors.As(err, &ce) || ce.Code != "invalid_argument" {
		t.Fatalf("machine command with a session: %v", err)
	}
	// Without one it reaches the daemon (whose fake does not implement it).
	if _, err := e.client.DoMachine(ctx, s.MachineID, list); !errors.As(err, &ce) || ce.Code != "unimplemented" {
		t.Fatalf("machine command: %v", err)
	}
}

func TestTerminalStream(t *testing.T) {
	e := setup(t)
	e.backend.add(t, "sess1")
	e.startRelay()
	s := e.session(t, "sess1", 0)
	e.online(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	term, err := e.client.OpenTerminal(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	f, err := term.Read(ctx)
	if err != nil || string(f.GetSnapshot().GetData()) != "screen "+marker || f.GetSnapshot().Cols != 80 {
		t.Fatalf("snapshot: %v %v", f, err)
	}
	e.backend.mu.Lock()
	ft := e.backend.term
	e.backend.mu.Unlock()
	ft.out <- []byte("live " + marker)
	if f, err = term.Read(ctx); err != nil || string(f.GetOutput()) != "live "+marker {
		t.Fatalf("output: %v %v", f, err)
	}
	if err := term.Send(ctx, &pb.TerminalFrame{Kind: &pb.TerminalFrame_Input{Input: []byte("ls\r")}}); err != nil {
		t.Fatal(err)
	}
	if got := <-ft.input; string(got) != "ls\r" {
		t.Fatalf("input %q", got)
	}
	term.Send(ctx, &pb.TerminalFrame{Kind: &pb.TerminalFrame_Resize{Resize: &pb.TerminalResize{Cols: 60, Rows: 30, Fit: true}}})
	if got := <-ft.resized; got != "60x30 true" {
		t.Fatalf("resize %q", got)
	}
	// A replayed frame ends the stream on the daemon's side.
	term.SendRaw(ctx, 1, &pb.TerminalFrame{Kind: &pb.TerminalFrame_Input{Input: []byte("again")}})
	select {
	case <-ft.done:
	case <-ctx.Done():
		t.Fatal("replayed frame accepted")
	}
	select {
	case got := <-ft.input:
		t.Fatalf("replayed input typed: %q", got)
	default:
	}
	term.Close()

	// The terminal's content never reaches the server's database.
	e.stopRelay()
	e.srv.Close()
	for _, name := range []string{e.dbPath, e.dbPath + "-wal"} {
		if b, err := os.ReadFile(name); err == nil && bytes.Contains(b, []byte(marker)) {
			t.Fatalf("%s contains terminal content", name)
		}
	}
}

func TestImagesGoUpBeforeTheirEvents(t *testing.T) {
	e := setup(t)
	l := e.backend.add(t, "sess1")
	const good, failing = "11111111111111111111111111111111", "22222222222222222222222222222222"
	payload, _ := proto.Marshal(&pb.ImagePayload{Format: pb.ImageFormat_IMAGE_FORMAT_PNG, Width: 1, Height: 1, Data: []byte("\x89PNG" + marker)})
	e.backend.mu.Lock()
	e.backend.images[good] = relay.ImageUpload{Payload: payload}
	e.backend.mu.Unlock()
	msg := &pb.UserMessage{Text: "look", Images: []*pb.ImageRef{
		{ImageId: good, Sync: pb.ImageSync_IMAGE_SYNC_SYNCED, Preview: []byte("p1"), DisplayWidth: 1},
		{ImageId: failing, Sync: pb.ImageSync_IMAGE_SYNC_SYNCED, Preview: []byte("p2"), DisplayWidth: 1},
	}}
	if err := l.Append("sess1", []*pb.Event{{Kind: &pb.Event_UserMessage{UserMessage: msg}}}, "", 0); err != nil {
		t.Fatal(err)
	}
	e.startRelay()
	ctx := context.Background()
	var s remoteclient.Session
	waitFor(t, "the event", func() bool {
		ss, _ := e.client.Sessions(ctx)
		if len(ss) == 1 && ss[0].LastSeq == 1 {
			s = ss[0]
			return true
		}
		return false
	})
	evs, err := e.client.Events(ctx, s, 0)
	if err != nil {
		t.Fatal(err)
	}
	refs := evs[0].GetUserMessage().GetImages()
	if len(refs) != 2 || refs[0].Sync != pb.ImageSync_IMAGE_SYNC_SYNCED || refs[1].Sync != pb.ImageSync_IMAGE_SYNC_PREVIEW_ONLY || refs[1].DisplayWidth != 0 {
		t.Fatalf("refs %v", refs)
	}
	img, expired, err := e.client.Image(ctx, s, good)
	if err != nil || expired || string(img.Data) != "\x89PNG"+marker {
		t.Fatalf("image %v %v %v", img, expired, err)
	}
	if _, _, err := e.client.Image(ctx, s, failing); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("failing image: %v", err)
	}
	e.backend.mu.Lock()
	done := e.backend.images[good].Done
	e.backend.mu.Unlock()
	if !done {
		t.Fatal("upload not recorded")
	}
	// The server only has ciphertext of the image too.
	e.stopRelay()
	e.srv.Close()
	for _, name := range []string{e.dbPath, e.dbPath + "-wal"} {
		if b, err := os.ReadFile(name); err == nil && bytes.Contains(b, []byte(marker)) {
			t.Fatalf("%s contains plaintext", name)
		}
	}
}

func TestCommandOrigins(t *testing.T) {
	e := setup(t)
	e.backend.add(t, "sess1")
	e.startRelay()
	s := e.session(t, "sess1", 0)
	e.online(t, true)
	ctx := context.Background()
	send := func(id string) *pb.Command {
		return &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: "sess1", Text: "hi", RequestId: id}}}
	}
	// Signed: the device is known for sure.
	if _, err := e.client.Do(ctx, s, send("a")); err != nil {
		t.Fatal(err)
	}
	// Unsigned: the server's word about the sender is not taken.
	env, _ := e.client.Seal(s.MachineID, s.ID, remoteclient.NewRequestID(), time.Now().Add(time.Minute), send("b"))
	env.Signature = nil
	if _, err := e.client.Forward(ctx, s.MachineID, env); connect.CodeOf(err) != connect.CodeUnknown {
		t.Fatalf("unsigned: %v", err)
	}
	// A signature that does not check out is refused.
	env, _ = e.client.Seal(s.MachineID, s.ID, remoteclient.NewRequestID(), time.Now().Add(time.Minute), send("c"))
	env.Signature[0] ^= 1
	if _, err := e.client.Forward(ctx, s.MachineID, env); connect.CodeOf(err) != connect.CodeUnknown {
		t.Fatalf("bad signature: %v", err)
	}
	// A client of another protocol version is told to update; nothing runs.
	other := send("d")
	other.ProtocolVersion = protocol.Version + 1
	var ce *remoteclient.CommandError
	if _, err := e.client.Do(ctx, s, other); !errors.As(err, &ce) || ce.Code != connect.CodeFailedPrecondition.String() || !strings.Contains(ce.Message, "update") {
		t.Fatalf("protocol mismatch: %v", err)
	}
	e.backend.mu.Lock()
	defer e.backend.mu.Unlock()
	o := e.backend.origins
	if len(o) != 1 || o[0].Kind != pb.OriginKind_ORIGIN_KIND_CLIENT || o[0].DeviceId != e.client.DeviceID || o[0].RequestId == "" {
		t.Fatalf("origins %v", o)
	}
}

// A successful HTTP upgrade is not a ready relay connection. A peer that
// never sends Ready must not hold the daemon's retry loop indefinitely.
func TestRelayRetriesWhenStreamNeverBecomesReady(t *testing.T) {
	var attempts atomic.Int32
	e := setupWithHandler(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.URL.Path != "/v1/stream" || attempts.Add(1) > 1 {
				next.ServeHTTP(w, req)
				return
			}
			ws, err := websocket.Accept(w, req, nil)
			if err != nil {
				return
			}
			defer ws.CloseNow()
			// Read the hello, then stay silent while servicing control frames.
			_, _, _ = ws.Read(req.Context())
			_, _, _ = ws.Read(req.Context())
		})
	})
	appendText(t, e.backend.add(t, "ready-retry"), "ready-retry", "caught up")
	e.startRelay()
	deadline := time.Now().Add(20 * time.Second)
	for attempts.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("relay did not retry the missing Ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.session(t, "ready-retry", 1)
}

// The machine's metadata is read once to upload it on connecting. What it
// becomes right after that is a change like any later one.
func TestMachineMetadataChangedWhileConnectingIsUploaded(t *testing.T) {
	e := setup(t)
	var reads atomic.Int32
	e.backend.machineMeta = func() *pb.MachineMeta {
		if reads.Add(1) == 1 {
			return &pb.MachineMeta{Hostname: "before"}
		}
		return &pb.MachineMeta{Hostname: "after"}
	}
	e.startRelay()
	waitFor(t, "the machine's current metadata", func() bool {
		ms, err := e.client.Machines(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return len(ms) == 1 && ms[0].Meta.GetHostname() == "after"
	})
}

// countNotifies wraps the server to count the Notify calls it is sent, and
// to refuse those refuse says to, as it does when pushes are at capacity.
func countNotifies(n *atomic.Int32, refuse func(call int32) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/remoteagent.v1.MachineService/Notify" && refuse(n.Add(1)) {
				connect.NewErrorWriter().Write(w, r, connect.NewError(connect.CodeResourceExhausted, errors.New("push capacity exhausted; try again later")))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func turnEnded() *pb.Event {
	return &pb.Event{Time: timestamppb.Now(), Kind: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{}}}
}

// A fork starts with copies of its source's events, at their own times. The
// turn that ended there a moment ago must not be announced again.
func TestForkedHistoryIsNotNotified(t *testing.T) {
	var notifies atomic.Int32
	e := setupWithHandler(t, countNotifies(&notifies, func(int32) bool { return false }))
	l := e.backend.add(t, "fork")
	forked := &pb.Event{Time: timestamppb.Now(), Kind: &pb.Event_SessionForked{SessionForked: &pb.SessionForked{SourceSessionId: "source"}}}
	if err := l.Append("fork", []*pb.Event{turnEnded(), forked}, "", 0); err != nil {
		t.Fatal(err)
	}
	e.backend.mu.Lock()
	e.backend.forked["fork"] = 2
	e.backend.mu.Unlock()
	e.startRelay()
	e.session(t, "fork", 2)
	// The fork's own turn is announced, and it is the only one.
	if err := l.Append("fork", []*pb.Event{turnEnded()}, "", 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the fork's own turn to be notified", func() bool { return notifies.Load() > 0 })
	e.syncLaterPasses(t, l, "fork")
	if n := notifies.Load(); n != 1 {
		t.Fatalf("%d notifications for a fork with one turn of its own", n)
	}
}

// The server refuses a notification while its pushes are at capacity, and
// says to try again. The event is uploaded by then and is not gone over
// again, so the relay has to remember that the notification is owed.
func TestRefusedNotificationIsOfferedAgain(t *testing.T) {
	var notifies atomic.Int32
	e := setupWithHandler(t, countNotifies(&notifies, func(call int32) bool { return call == 1 }))
	l := e.backend.add(t, "sess1")
	e.startRelay()
	if err := l.Append("sess1", []*pb.Event{turnEnded()}, "", 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the refused notification to be offered again", func() bool { return notifies.Load() >= 2 })
	// Once taken it is not offered a third time.
	e.syncLaterPasses(t, l, "sess1")
	if n := notifies.Load(); n != 2 {
		t.Fatalf("%d notify calls for one event refused once", n)
	}
}

func TestExitedSessionRetriesOwedNotificationOnScheduler(t *testing.T) {
	var notifies atomic.Int32
	var mu sync.Mutex
	var at []time.Time
	e := setupWithHandler(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/remoteagent.v1.MachineService/Notify" {
				call := notifies.Add(1)
				mu.Lock()
				at = append(at, time.Now())
				mu.Unlock()
				if call == 1 {
					connect.NewErrorWriter().Write(w, r, connect.NewError(connect.CodeResourceExhausted, errors.New("push capacity exhausted; try again later")))
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	})
	l := e.backend.add(t, "ended-notify")
	e.backend.mu.Lock()
	e.backend.exited["ended-notify"] = true
	e.backend.mu.Unlock()
	e.startRelay()
	if err := l.Append("ended-notify", []*pb.Event{turnEnded()}, "", 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the first refused notification", func() bool { return notifies.Load() >= 1 })
	waitFor(t, "the ended session's owed notification", func() bool { return notifies.Load() >= 2 })
	mu.Lock()
	delay := at[1].Sub(at[0])
	mu.Unlock()
	if delay < testSyncInterval {
		t.Fatalf("ended-session notification retried after %v, before its %v interval", delay, testSyncInterval)
	}
	e.syncLaterPasses(t, l, "ended-notify")
	if n := notifies.Load(); n != 2 {
		t.Fatalf("%d notify calls after the owed event was accepted", n)
	}
}

func TestExitedMetadataChangeDoesNotResendSyncedEvents(t *testing.T) {
	var uploads atomic.Int32
	e := setupWithHandler(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/remoteagent.v1.MachineService/UploadEvents" {
				uploads.Add(1)
			}
			next.ServeHTTP(w, r)
		})
	})
	l := e.backend.add(t, "ended-meta")
	e.backend.mu.Lock()
	e.backend.exited["ended-meta"] = true
	e.backend.mu.Unlock()
	appendText(t, l, "ended-meta", "first")
	e.startRelay()
	e.session(t, "ended-meta", 1)
	e.stopRelay()
	r := e.startRelay()
	e.online(t, true)
	appendText(t, l, "ended-meta", "second")
	e.session(t, "ended-meta", 2)
	waitFor(t, "both event batches uploaded", func() bool { return uploads.Load() >= 2 })
	// Let the exited worker finish and leave only the scheduler's completed
	// snapshot before changing metadata.
	waitFor(t, "the ended worker to release its backlog slot", func() bool { return !relay.HasBacklogWorkerForTest(r) })
	e.backend.mu.Lock()
	e.backend.titles["ended-meta"] = "renamed"
	e.backend.mu.Unlock()
	waitFor(t, "the ended session's renamed metadata", func() bool {
		ss, err := e.client.Sessions(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range ss {
			if s.ID == "ended-meta" && s.Info.GetTitle() == "renamed" {
				return true
			}
		}
		return false
	})
	// A second metadata edit must also be noticed by a later pass.
	e.backend.mu.Lock()
	e.backend.titles["ended-meta"] = "renamed again"
	e.backend.mu.Unlock()
	waitFor(t, "the next metadata pass", func() bool { return e.session(t, "ended-meta", 2).Info.GetTitle() == "renamed again" })
	if n := uploads.Load(); n != 2 {
		t.Fatalf("metadata-only update triggered %d event uploads, want 2 total", n)
	}
}

func TestRefusedNotificationSurvivesReconnect(t *testing.T) {
	var notifies atomic.Int32
	e := setupWithHandler(t, countNotifies(&notifies, func(call int32) bool { return call == 1 }))
	l := e.backend.add(t, "sess1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newTestRelay(e.backend)
	relay.SetSyncIntervalForTest(r, time.Hour)
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	defer func() { cancel(); <-done }()
	if err := l.Append("sess1", []*pb.Event{turnEnded()}, "", 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the refused notification to be retained", func() bool { return relay.HasOwedNotificationForTest(r, "sess1") })
	// The stream drops before the next pass; the event is on the server,
	// so the next connection uploads nothing for it.
	e.dropStreams()
	waitFor(t, "the refused notification to be offered again", func() bool { return notifies.Load() >= 2 })
}

// The connection drops while a refused notification is offered again. The
// server gave no answer, and the event is long uploaded, so the
// notification is still owed on the next connection.
func TestOwedNotificationSurvivesAnInterruptedRetry(t *testing.T) {
	var notifies, logins atomic.Int32
	var interrupted atomic.Pointer[env]
	e := setupWithHandler(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/Login") {
				logins.Add(1)
			}
			if r.URL.Path == "/remoteagent.v1.MachineService/Notify" {
				switch notifies.Add(1) {
				case 1:
					connect.NewErrorWriter().Write(w, r, connect.NewError(connect.CodeResourceExhausted, errors.New("push capacity exhausted; try again later")))
					return
				case 2:
					// Drop the machine stream and abort this HTTP request before any response.
					interrupted.Load().dropStreams()
					panic(http.ErrAbortHandler)
				}
			}
			next.ServeHTTP(w, r)
		})
	})
	interrupted.Store(e)
	logins.Store(0)
	l := e.backend.add(t, "sess1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newTestRelay(e.backend)
	relay.SetSyncIntervalForTest(r, time.Hour)
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	defer func() { cancel(); <-done }()
	if err := l.Append("sess1", []*pb.Event{turnEnded()}, "", 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the refused notification to be retained", func() bool { return relay.HasOwedNotificationForTest(r, "sess1") })
	l.Poke()
	waitFor(t, "the notification to be offered again after the interrupted retry", func() bool { return notifies.Load() >= 3 })
	if logins.Load() < 2 {
		t.Fatal("interrupted retry did not reconnect")
	}
	// Once taken it is not offered again.
	e.syncLaterPasses(t, l, "sess1")
	if n := notifies.Load(); n != 3 {
		t.Fatalf("%d notify calls for one event", n)
	}
}

// A notification the server answered with a refusal other than lack of
// capacity is not offered again.
func TestRejectedNotificationIsNotOfferedAgain(t *testing.T) {
	var notifies atomic.Int32
	e := setupWithHandler(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/remoteagent.v1.MachineService/Notify" {
				notifies.Add(1)
				connect.NewErrorWriter().Write(w, r, connect.NewError(connect.CodeInvalidArgument, errors.New("refused")))
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	l := e.backend.add(t, "sess1")
	e.startRelay()
	if err := l.Append("sess1", []*pb.Event{turnEnded()}, "", 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the notification to be offered", func() bool { return notifies.Load() >= 1 })
	e.syncLaterPasses(t, l, "sess1")
	if n := notifies.Load(); n != 1 {
		t.Fatalf("%d notify calls for one rejected notification", n)
	}
}

// Protocol 6 predates terminal-only skeletons and must never execute a
// structured command against a daemon that uses their answer restrictions.
func TestProtocol6CommandsRefused(t *testing.T) {
	e := setup(t)
	e.backend.add(t, "session")
	e.startRelay()
	s := e.session(t, "session", 0)
	e.online(t, true)
	cmd := &pb.Command{ProtocolVersion: 6, Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: s.ID, Text: "old-client-answer", RequestId: "old-answer"}}}
	_, err := e.client.Do(context.Background(), s, cmd)
	var ce *remoteclient.CommandError
	if !errors.As(err, &ce) || ce.Code != connect.CodeFailedPrecondition.String() || !strings.Contains(ce.Message, "update") {
		t.Fatalf("protocol 6 was not told to update: %v", err)
	}
	if e.backend.sendCount() != 0 {
		t.Fatal("protocol 6 command reached the CLI backend")
	}
}

type failingCheckpointBackend struct {
	*fakeBackend
	fixed     atomic.Bool
	oversized bool
}

func (b *failingCheckpointBackend) LiveSnapshot(id string) (*pb.LiveSnapshot, error) {
	info, err := b.SessionInfo(id)
	if err != nil {
		return nil, err
	}
	if id == "broken" && !b.fixed.Load() {
		if !b.oversized {
			return nil, errors.New("snapshot temporarily unavailable")
		}
		info.Title = strings.Repeat("x", 49<<10)
	}
	return &pb.LiveSnapshot{Session: info}, nil
}
func TestCheckpointFailureKeepsRelayAndOtherSessionsOnline(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		t.Run(fmt.Sprint(oversized), func(t *testing.T) {
			e := setup(t)
			e.backend.add(t, "broken")
			if err := os.WriteFile(filepath.Join(e.backend.SessionDir("broken"), "relay.key"), e2e.NewKey(), 0600); err != nil {
				t.Fatal(err)
			}
			log := e.backend.add(t, "healthy")
			appendText(t, log, "healthy", "available")
			backend := &failingCheckpointBackend{fakeBackend: e.backend, oversized: oversized}
			r := e.startRelayBackend(backend)
			waitFor(t, "relay connected despite pending checkpoint", r.Connected)
			e.session(t, "healthy", 1)
			_, err := e.client.Do(context.Background(), e.session(t, "healthy", 1), &pb.Command{Kind: &pb.Command_RenameSession{RenameSession: &pb.RenameSessionRequest{SessionId: "healthy", Title: "still controllable"}}})
			if err != nil {
				t.Fatal(err)
			}
			backend.fixed.Store(true)
			e.session(t, "broken", 0)
		})
	}
}

// A real, legal action can be much larger than a metadata checkpoint.
type largeActionCheckpointBackend struct {
	*fakeBackend
	fixed         atomic.Bool
	snapshotError bool
}

func (b *largeActionCheckpointBackend) LiveSnapshot(id string) (*pb.LiveSnapshot, error) {
	info, err := b.SessionInfo(id)
	if err != nil {
		return nil, err
	}
	if !b.fixed.Load() && b.snapshotError {
		return nil, errors.New("synthetic snapshot unavailable")
	}
	snapshot := &pb.LiveSnapshot{Session: info}
	if !b.fixed.Load() {
		snapshot.Actions = []*pb.Event{{SessionId: id, Seq: 1, Kind: &pb.Event_QuestionAsked{QuestionAsked: &pb.QuestionAsked{QuestionId: "large-question", Questions: []*pb.Question{{Header: "pending", Text: strings.Repeat("prompt ", 20000)}}}}}}
	}
	return snapshot, nil
}
func TestCheckpointFailureDoesNotBlockItsOwnSessionSync(t *testing.T) {
	for _, failure := range []string{"oversized", "snapshot", "disk"} {
		t.Run(failure, func(t *testing.T) {
			e := setup(t)
			log := e.backend.add(t, "pending-session")
			appendText(t, log, "pending-session", "first")
			backend := &largeActionCheckpointBackend{fakeBackend: e.backend, snapshotError: failure == "snapshot"}
			checkpointPath := filepath.Join(e.backend.SessionDir("pending-session"), "current-checkpoint.pb")
			if failure == "disk" {
				backend.fixed.Store(true)
				if err := os.Mkdir(checkpointPath, 0700); err != nil {
					t.Fatal(err)
				}
			}
			e.startRelayBackend(backend)
			s := e.session(t, "pending-session", 1)
			if len(s.Info.CurrentCheckpoint) != 0 {
				t.Fatal("failed checkpoint advertised as complete")
			}
			appendText(t, log, "pending-session", "second")
			e.session(t, "pending-session", 2)
			_, err := e.client.Do(context.Background(), s, &pb.Command{Kind: &pb.Command_RenameSession{RenameSession: &pb.RenameSessionRequest{SessionId: s.ID, Title: "metadata still syncs"}}})
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, "same session metadata despite failed checkpoint", func() bool { return e.session(t, "pending-session", 2).Info.Title == "metadata still syncs" })
			backend.fixed.Store(true)
			if failure == "disk" {
				if err := os.Remove(checkpointPath); err != nil {
					t.Fatal(err)
				}
			}
			e.stopRelay()
			e.startRelayBackend(backend)
			waitFor(t, "checkpoint repaired after reconnect", func() bool { return len(e.session(t, "pending-session", 2).Info.CurrentCheckpoint) > 0 })
		})
	}
}
