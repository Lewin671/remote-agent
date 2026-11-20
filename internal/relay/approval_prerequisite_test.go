package relay_test

import (
	"bytes"
	"connectrpc.com/connect"
	"context"
	"fmt"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/relay"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
	"github.com/Lewin671/remote-agent/shared/devicesec"
	"google.golang.org/protobuf/proto"
)

type prerequisiteBackend struct {
	*fakeBackend
	snapshots   atomic.Int32
	bulkEntered chan struct{}
	bulkRelease chan struct{}
}

type prerequisiteLocal struct {
	remoteagentv1connect.LocalServiceHandler
	b *prerequisiteBackend
}

func (b *prerequisiteBackend) Local() remoteagentv1connect.LocalServiceHandler {
	return prerequisiteLocal{b.fakeBackend.Local(), b}
}
func (l prerequisiteLocal) PutImageChunk(ctx context.Context, _ *connect.Request[pb.PutImageChunkRequest]) (*connect.Response[pb.PutImageChunkResponse], error) {
	l.b.bulkEntered <- struct{}{}
	select {
	case <-l.b.bulkRelease:
		return connect.NewResponse(&pb.PutImageChunkResponse{}), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *prerequisiteBackend) LiveSnapshot(ctx context.Context, sid string) (*pb.LiveSnapshot, error) {
	if agent.OriginFrom(ctx).GetKind() == pb.OriginKind_ORIGIN_KIND_CLIENT {
		b.snapshots.Add(1)
	}
	if sid != "approval-flow-session" {
		return &pb.LiveSnapshot{Session: &pb.Session{Id: sid}}, nil
	}
	b.mu.Lock()
	resolved := len(b.resolved)
	b.mu.Unlock()
	id := "fixture-approval"
	if resolved > 0 {
		id = fmt.Sprintf("fixture-approval-%d", resolved)
	}
	return &pb.LiveSnapshot{Session: &pb.Session{Id: "approval-flow-session"}, Actions: []*pb.Event{{Seq: uint64(resolved + 1), Kind: &pb.Event_ApprovalRequested{ApprovalRequested: &pb.ApprovalRequested{ApprovalId: id, Command: "true"}}}}}, nil
}

// Regression: the production Web/Android/iOS approval entry first refreshes the owner
// snapshot. Exercise those two real encrypted RPCs in order against the actual
// relay scheduler, with four ordinary callbacks held by explicit barriers.
func TestApprovalSnapshotPrerequisiteSurvivesOrdinaryAndBulkSaturation(t *testing.T) {
	const freshID = "snapshot-prerequisite-request"
	arrived := make(chan struct{})
	var arrivedOnce sync.Once
	e := setupWithHandler(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/Forward") {
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(raw))
				req := &pb.ForwardRequest{}
				if proto.Unmarshal(raw, req) == nil && req.Command != nil {
					if h, err := devicesec.ParseHeader(req.Command.SecurityHeader); err == nil && h.Request == freshID {
						arrivedOnce.Do(func() { close(arrived) })
					}
				}
			}
			next.ServeHTTP(w, r)
		})
	})
	e.backend.add(t, "approval-flow-session")
	for i := range 4 {
		e.backend.add(t, fmt.Sprintf("ordinary-session-%d", i))
	}
	backend := &prerequisiteBackend{fakeBackend: e.backend, bulkEntered: make(chan struct{}, 2), bulkRelease: make(chan struct{})}
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release); close(backend.bulkRelease) })
	entered := make(chan struct{}, 4)
	e.backend.normalResult = func(ctx context.Context) error {
		entered <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	running := e.startRelayBackend(backend)
	s := e.session(t, "approval-flow-session", 0)
	e.online(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	snapshot := &pb.Command{Kind: &pb.Command_GetLiveSnapshot{GetLiveSnapshot: &pb.GetLiveSnapshotRequest{SessionId: s.ID}}}
	if _, err := e.client.Do(ctx, s, snapshot); err != nil {
		t.Fatal(err)
	}
	normalDone := make(chan error, 4)
	for i := range 4 {
		load := e.session(t, fmt.Sprintf("ordinary-session-%d", i), 0)
		go func(session remoteclient.Session) {
			_, err := e.client.Do(ctx, session, &pb.Command{Kind: &pb.Command_RenameSession{RenameSession: &pb.RenameSessionRequest{SessionId: session.ID, Title: "ordinary load"}}})
			normalDone <- err
		}(load)
	}
	for range 4 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("ordinary pool did not fill")
		}
	}
	bulkDone := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := e.client.DoMachine(ctx, s.MachineID, &pb.Command{Kind: &pb.Command_PutImageChunk{PutImageChunk: &pb.PutImageChunkRequest{TransferId: "fixture-transfer", Data: []byte("fixture")}}})
			bulkDone <- err
		}()
	}
	for range 2 {
		select {
		case <-backend.bulkEntered:
		case <-ctx.Done():
			t.Fatal("bulk pool did not fill")
		}
	}
	if relay.ActiveCallsForTest(running) != 6 {
		t.Fatal("ordinary and bulk pools were not full")
	}
	env, err := e.client.Seal(s.MachineID, s.ID, freshID, time.Now().Add(time.Minute), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	completed := make(chan error, 1)
	go func() {
		view, err := e.client.Forward(ctx, s.MachineID, env, remoteclient.NeverDispatched)
		if err == nil {
			_, err = e.client.Do(ctx, s, &pb.Command{Kind: &pb.Command_ResolveApproval{ResolveApproval: &pb.ResolveApprovalRequest{SessionId: s.ID, ApprovalId: view.GetLiveSnapshot().Actions[0].GetApprovalRequested().ApprovalId, ObservedEventSeq: view.GetLiveSnapshot().Actions[0].Seq, Allow: true}}})
		}
		completed <- err
	}()
	select {
	case <-arrived:
	case <-ctx.Done():
		t.Fatal("snapshot prerequisite did not reach actual server")
	}
	early := false
	select {
	case err := <-completed:
		early = true
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		e.backend.mu.Lock()
		resolutions := len(e.backend.resolved)
		e.backend.mu.Unlock()
		t.Logf("while ordinary and bulk pools remain occupied: fresh snapshots=%d approvals=%d", backend.snapshots.Load()-1, resolutions)
		t.Error("approval flow is blocked before its reserved pool: GetLiveSnapshot prerequisite is queued in the saturated ordinary pool")
	}
	releaseOnce.Do(func() { close(release); close(backend.bulkRelease) })
	if !early {
		select {
		case err := <-completed:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("approval did not complete after ordinary release")
		}
	}
	for range 4 {
		if err := <-normalDone; err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := <-bulkDone; err != nil {
			t.Fatal(err)
		}
	}
	e.backend.mu.Lock()
	resolutions := len(e.backend.resolved)
	e.backend.mu.Unlock()
	if backend.snapshots.Load() != 2 || resolutions != 1 {
		t.Fatalf("after release: snapshots=%d approvals=%d", backend.snapshots.Load(), resolutions)
	}
	t.Log("after release: exactly one fresh snapshot and one approval completed through signed encrypted RPCs")
}

type schedulerTestTimer struct {
	at      time.Time
	fn      func()
	stopped bool
}

type schedulerTestClock struct {
	mu     sync.Mutex
	cur    time.Time
	timers []*schedulerTestTimer
}

func newSchedulerTestClock(start time.Time) *schedulerTestClock {
	return &schedulerTestClock{cur: start}
}

func (c *schedulerTestClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cur
}

func (c *schedulerTestClock) after(d time.Duration, fn func()) func() bool {
	if d <= 0 {
		go fn()
		return func() bool { return false }
	}
	c.mu.Lock()
	t := &schedulerTestTimer{at: c.cur.Add(d), fn: fn}
	c.timers = append(c.timers, t)
	c.mu.Unlock()
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		if t.stopped {
			return false
		}
		t.stopped = true
		return true
	}
}

func (c *schedulerTestClock) advance(d time.Duration) {
	c.mu.Lock()
	c.cur = c.cur.Add(d)
	var due []func()
	remaining := c.timers[:0]
	for _, t := range c.timers {
		if t.stopped {
			continue
		}
		if !t.at.After(c.cur) {
			t.stopped = true
			due = append(due, t.fn)
		} else {
			remaining = append(remaining, t)
		}
	}
	c.timers = remaining
	c.mu.Unlock()
	for _, fn := range due {
		go fn()
	}
}

func TestApprovalPrerequisiteSurvivesRunningAndOverdueSend(t *testing.T) {
	e := setup(t)
	e.backend.add(t, "approval-flow-session")
	e.backend.add(t, "stalled-input")
	backend := &prerequisiteBackend{fakeBackend: e.backend}
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	e.backend.sendResult = func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-release
		return ctx.Err()
	}
	clk := newSchedulerTestClock(time.Now())
	running := e.startRelayBackend(backend, func(r *relay.Relay) {
		relay.SetSchedulerClockForTest(r, clk.now, clk.after)
	})
	e.online(t, true)
	input := e.session(t, "stalled-input", 0)
	approval := e.session(t, "approval-flow-session", 0)
	env, err := e.client.Seal(input.MachineID, input.ID, "stalled-send-reservation", time.Now().Add(time.Minute), &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: input.ID, Text: "load"}}})
	if err != nil {
		t.Fatal(err)
	}
	sendDone := make(chan error, 1)
	ctx := t.Context()
	go func() {
		_, err := e.client.Forward(ctx, input.MachineID, env, remoteclient.NeverDispatched)
		sendDone <- err
	}()
	select {
	case <-entered:
	case err := <-sendDone:
		t.Fatalf("Send finished before starting: %v", err)
	case <-ctx.Done():
		t.Fatal("Send did not start")
	}
	flow := func(label string) {
		t.Helper()
		view, err := e.client.Do(ctx, approval, &pb.Command{Kind: &pb.Command_GetLiveSnapshot{GetLiveSnapshot: &pb.GetLiveSnapshotRequest{SessionId: approval.ID}}})
		if err != nil {
			t.Fatalf("%s: prerequisite could not use reserved slot: %v", label, err)
		}
		_, err = e.client.Do(ctx, approval, &pb.Command{Kind: &pb.Command_ResolveApproval{ResolveApproval: &pb.ResolveApprovalRequest{SessionId: approval.ID, ApprovalId: view.GetLiveSnapshot().Actions[0].GetApprovalRequested().ApprovalId, ObservedEventSeq: view.GetLiveSnapshot().Actions[0].Seq, Allow: true}}})
		if err != nil {
			t.Fatalf("%s: approval failed: %v", label, err)
		}
		select {
		case <-release:
			t.Fatal("input was released before approval")
		default:
		}
	}
	flow("running Send")
	select {
	case <-canceled:
		t.Fatal("first approval was not completed before the Send deadline")
	default:
	}
	clk.advance(6 * time.Second)
	select {
	case <-canceled:
	case err := <-sendDone:
		t.Fatalf("Send returned before deadline cancellation: %v", err)
	case <-ctx.Done():
		t.Fatal("Send deadline did not fire")
	}
	flow("overdue Send still running")
	if relay.ActivePoolsForTest(running)[0] != 1 {
		t.Fatal("overdue Send did not retain its original slot")
	}
	e.backend.mu.Lock()
	approvals := len(e.backend.resolved)
	e.backend.mu.Unlock()
	if backend.snapshots.Load() != 2 || approvals != 2 {
		t.Fatalf("snapshot/approval executions=%d/%d", backend.snapshots.Load(), approvals)
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-sendDone; err == nil {
		t.Fatal("overdue Send returned success")
	}
}
