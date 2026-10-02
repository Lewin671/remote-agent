package relay_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	"github.com/Lewin671/remote-agent/internal/e2e"
	"github.com/Lewin671/remote-agent/internal/eventlog"
	"github.com/Lewin671/remote-agent/internal/relay"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
	"github.com/Lewin671/remote-agent/internal/server"
	"google.golang.org/protobuf/proto"
)

// marker is plaintext that must never reach the server's database.
const marker = "SECRET-MARKER-7f3a"

type fakeBackend struct {
	dir string

	mu        sync.Mutex
	sessions  map[string]*eventlog.Log
	sends     []*pb.SendRequest
	schedules []*pb.ScheduleRequest
	term      *fakeTerminal
	resolved  []*pb.ResolveApprovalRequest
	// images are display copies by image ID; imageResults the IDs whose
	// upload finished, in order.
	origins      []*pb.Origin
	images       map[string]relay.ImageUpload
	imageResults []string
}

func newFakeBackend(t *testing.T) *fakeBackend {
	return &fakeBackend{dir: t.TempDir(), sessions: map[string]*eventlog.Log{}, images: map[string]relay.ImageUpload{}}
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
	return &pb.Session{Id: id, Cwd: "/work/" + marker, Status: pb.SessionStatus_SESSION_STATUS_IDLE, LastSeq: l.LastSeq()}, nil
}

func (b *fakeBackend) Events(id string, after uint64) ([]*pb.Event, <-chan struct{}, error) {
	l, err := b.log(id)
	if err != nil {
		return nil, nil, err
	}
	evs, ch := l.Since(after)
	return evs, ch, nil
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
	srv     *server.Server
	http    *httptest.Server
	dbPath  string
	backend *fakeBackend
	acct    *relay.Account
	client  *remoteclient.Client
	stop    context.CancelFunc
	done    chan struct{}
}

func setup(t *testing.T) *env {
	return setupWithHandler(t, func(h http.Handler) http.Handler { return h })
}

func setupWithHandler(t *testing.T, wrap func(http.Handler) http.Handler) *env {
	t.Helper()
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	e := &env{dbPath: filepath.Join(t.TempDir(), "ra.db"), backend: newFakeBackend(t)}
	var err error
	e.srv, err = server.New(server.Config{DBPath: e.dbPath, SetupToken: "setup", PingInterval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	e.http = httptest.NewServer(wrap(e.srv.Handler()))
	t.Cleanup(func() {
		e.stopRelay()
		e.http.Close()
		e.srv.Close()
	})
	ctx := context.Background()
	if _, err := relay.Join(ctx, e.http.URL, e2e.NewKey(), "wrong"); err == nil {
		t.Fatal("joined with a wrong setup token")
	}
	e.acct, err = relay.Join(ctx, e.http.URL, e2e.NewKey(), "setup")
	if err != nil {
		t.Fatal(err)
	}
	e.client, err = remoteclient.Register(ctx, e.acct.Secret())
	if err != nil {
		t.Fatal(err)
	}
	return e
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

func (e *env) startRelay() {
	ctx, cancel := context.WithCancel(context.Background())
	e.stop = cancel
	e.done = make(chan struct{})
	r := relay.New(e.backend)
	r.Retry = 50 * time.Millisecond
	go func() { defer close(e.done); r.Run(ctx) }()
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
		case f := <-stream:
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

func TestCommandsRejected(t *testing.T) {
	e := setup(t)
	e.backend.add(t, "sess1")
	e.startRelay()
	s := e.session(t, "sess1", 0)
	e.online(t, true)
	ctx := context.Background()
	send := &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: "sess1", Text: "hi"}}}

	expired, _ := e.client.Seal(s.MachineID, s.ID, remoteclient.NewRequestID(), time.Now().Add(-time.Second), send)
	var ce *remoteclient.CommandError
	if _, err := e.client.Forward(ctx, s.MachineID, expired); !errors.As(err, &ce) || ce.Code != "deadline_exceeded" {
		t.Fatalf("expired: %v", err)
	}
	far, _ := e.client.Seal(s.MachineID, s.ID, remoteclient.NewRequestID(), time.Now().Add(time.Hour), send)
	if _, err := e.client.Forward(ctx, s.MachineID, far); !errors.As(err, &ce) || ce.Code != "invalid_argument" {
		t.Fatalf("far deadline: %v", err)
	}

	// A tampered command does not open.
	good, _ := e.client.Seal(s.MachineID, s.ID, remoteclient.NewRequestID(), time.Now().Add(time.Minute), send)
	good.Ciphertext[0] ^= 1
	if _, err := e.client.Forward(ctx, s.MachineID, good); connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("tampered: %v", err)
	}
	// A command sealed with another account's key does not open.
	stranger, _ := e2e.Derive(e2e.NewKey())
	forged, _ := e2e.SealMessage(stranger.Command, &pb.Header{
		Kind: pb.PayloadKind_PAYLOAD_KIND_COMMAND, MachineId: s.MachineID, SessionId: s.ID,
		RequestId: remoteclient.NewRequestID(), DeadlineMs: time.Now().Add(time.Minute).UnixMilli(), Sender: e.client.DeviceID,
	}, send)
	if _, err := e.client.Forward(ctx, s.MachineID, forged); connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("forged: %v", err)
	}
	if n := e.backend.sendCount(); n != 0 {
		t.Fatalf("send ran %d times", n)
	}

	// Offline machines fail at once, and nothing is queued for later.
	e.stopRelay()
	e.online(t, false)
	start := time.Now()
	later, _ := e.client.Seal(s.MachineID, s.ID, remoteclient.NewRequestID(), time.Now().Add(time.Minute), send)
	if _, err := e.client.Forward(ctx, s.MachineID, later); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("offline: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("offline forward did not fail fast")
	}
	e.startRelay()
	e.online(t, true)
	time.Sleep(200 * time.Millisecond)
	if n := e.backend.sendCount(); n != 0 {
		t.Fatalf("a command ran after reconnecting: %d", n)
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

func TestPairingMachines(t *testing.T) {
	e := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	links := make(chan relay.PairLink, 1)
	seen := make(chan []string, 1)
	errc := make(chan error, 1)
	go func() {
		errc <- relay.Initiate(ctx, e.acct, func(l relay.PairLink) { links <- l }, func(emoji []string, kind pb.DeviceKind) bool {
			if kind != pb.DeviceKind_DEVICE_KIND_MACHINE {
				return false
			}
			// The user compares these with the new device's.
			return strings.Join(emoji, "") == strings.Join(<-seen, "")
		})
	}()
	link := <-links
	if len(link.Code) != 8 {
		t.Fatalf("code %q", link.Code)
	}
	parsed, err := relay.ParsePairLink(link.URL("https://app.example.com"))
	if err != nil || parsed.Code != link.Code || !bytes.Equal(parsed.Commit, link.Commit) {
		t.Fatalf("link round trip: %v %v", parsed, err)
	}

	// The new machine has its own state directory.
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	acct, err := relay.JoinByPairing(ctx, parsed, func(emoji []string) { seen <- emoji })
	if err != nil {
		t.Fatal(err)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(acct.Root, e.acct.Root) || acct.DeviceID == e.acct.DeviceID {
		t.Fatal("joined with the wrong key or device")
	}
}

func TestPairingRefusals(t *testing.T) {
	e := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// A link whose commitment is not the initiator's is refused by the
	// joiner before anything is revealed.
	links := make(chan relay.PairLink, 1)
	go relay.Initiate(ctx, e.acct, func(l relay.PairLink) { links <- l }, func([]string, pb.DeviceKind) bool { return true })
	l := <-links
	l.Commit = bytes.Repeat([]byte{1}, 32)
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	if _, err := relay.JoinByPairing(ctx, l, func([]string) {}); err == nil || !strings.Contains(err.Error(), "commitment") {
		t.Fatalf("swapped commitment: %v", err)
	}

	// When the user refuses, the joiner gets no key.
	links2 := make(chan relay.PairLink, 1)
	go relay.Initiate(ctx, e.acct, func(l relay.PairLink) { links2 <- l }, func([]string, pb.DeviceKind) bool { return false })
	l2 := <-links2
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	if _, err := relay.JoinByPairing(ctx, l2, func([]string) {}); err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("refused pairing: %v", err)
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
	// Unsigned, as from an older client: the server's word only.
	e.client.Unsigned = true
	if _, err := e.client.Do(ctx, s, send("b")); err != nil {
		t.Fatal(err)
	}
	e.client.Unsigned = false
	// A signature that does not check out is refused.
	env, _ := e.client.Seal(s.MachineID, s.ID, remoteclient.NewRequestID(), time.Now().Add(time.Minute), send("c"))
	env.Signature[0] ^= 1
	if _, err := e.client.Forward(ctx, s.MachineID, env); connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("bad signature: %v", err)
	}
	e.backend.mu.Lock()
	defer e.backend.mu.Unlock()
	o := e.backend.origins
	if len(o) != 2 || o[0].Kind != pb.OriginKind_ORIGIN_KIND_CLIENT || o[0].DeviceId != e.client.DeviceID || o[0].RequestId == "" ||
		o[1].Kind != pb.OriginKind_ORIGIN_KIND_UNVERIFIED_CLIENT || o[1].DeviceId != e.client.DeviceID {
		t.Fatalf("origins %v", o)
	}
}
