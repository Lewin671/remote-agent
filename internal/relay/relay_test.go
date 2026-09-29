package relay_test

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"github.com/Lewin671/remote-agent/internal/eventlog"
	"github.com/Lewin671/remote-agent/internal/relay"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
	"github.com/Lewin671/remote-agent/internal/server"
)

// marker is plaintext that must never reach the server's database.
const marker = "SECRET-MARKER-7f3a"

type fakeBackend struct {
	dir string

	mu       sync.Mutex
	sessions map[string]*eventlog.Log
	sends    []*pb.SendRequest
	resolved []*pb.ResolveApprovalRequest
}

func newFakeBackend(t *testing.T) *fakeBackend {
	return &fakeBackend{dir: t.TempDir(), sessions: map[string]*eventlog.Log{}}
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

func (b *fakeBackend) Local() remoteagentv1connect.LocalServiceHandler { return &fakeLocal{b: b} }

type fakeLocal struct {
	remoteagentv1connect.UnimplementedLocalServiceHandler
	b *fakeBackend
}

func (f *fakeLocal) Send(ctx context.Context, req *connect.Request[pb.SendRequest]) (*connect.Response[pb.SendResponse], error) {
	f.b.mu.Lock()
	f.b.sends = append(f.b.sends, req.Msg)
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
	t.Helper()
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	e := &env{dbPath: filepath.Join(t.TempDir(), "ra.db"), backend: newFakeBackend(t)}
	var err error
	e.srv, err = server.New(server.Config{DBPath: e.dbPath, SetupToken: "setup", PingInterval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	e.http = httptest.NewServer(e.srv.Handler())
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
		RequestId: remoteclient.NewRequestID(), DeadlineMs: time.Now().Add(time.Minute).UnixMilli(),
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
