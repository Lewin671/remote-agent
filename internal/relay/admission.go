package relay

import (
	"context"
	"errors"
	"sync"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/contextio"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/transport"
	"google.golang.org/protobuf/proto"
)

type callRoute struct {
	callID    uint64
	attemptID string
	send      func(*pb.ForwardReply)
	once      sync.Once
}

func (r *callRoute) reply(v *pb.ForwardReply) { r.once.Do(func() { r.send(v) }) }

type authFrame struct {
	wire    deviceauth.Wire
	peer    string
	size    int
	waiting context.Context
	route   *callRoute
	retried bool
}
type callResult struct {
	route  *callRoute
	header deviceauth.Header
	body   []byte
	err    error
	raw    bool
	entry  string
}
type commandAdmission struct {
	authenticate  func(deviceauth.Wire) (*deviceauth.Authenticated, error)
	endpoint      *endpoint
	ctx           context.Context
	cancel        context.CancelFunc
	workers       sync.WaitGroup
	mu            sync.Mutex
	frames        transport.FairQueue[*authFrame]
	results       transport.FairQueue[*callResult]
	wake, replies chan struct{}
	syncing       map[string]bool
	scheduler     *transport.Scheduler
	closed        bool
}

func (c *endpoint) admissionOwner() *commandAdmission {
	c.admissionOnce.Do(func() {
		ctx, cancel := context.WithCancel(c.link.Context())
		a := &commandAdmission{endpoint: c, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 2), replies: make(chan struct{}, 2), syncing: map[string]bool{}, scheduler: transport.NewScheduler(c.link)}
		a.frames = transport.FairQueue[*authFrame]{Entries: 64, Bytes: 4 << 20, PeerEntries: 8, PeerBytes: 1 << 20}
		a.results = transport.FairQueue[*callResult]{Entries: 64, Bytes: 4 << 20, PeerEntries: 64, PeerBytes: 4 << 20}
		a.authenticate = func(w deviceauth.Wire) (*deviceauth.Authenticated, error) {
			return c.security.endpoint.Authenticate(w, nil, uint64(time.Now().UnixMilli()))
		}
		c.admission = a
		c.r.scheduling.Store(a.scheduler)
		for range 2 {
			a.workers.Go(a.authenticateLoop)
			a.workers.Go(a.resultLoop)
		}
	})
	return c.admission
}

// dispatch performs only bounded outer parsing. Neither queue overflow nor a
// suspended backend can make the socket reader wait for a worker or a write.
func (c *endpoint) dispatch(waiting context.Context, call *pb.ForwardCall, reply func(*pb.ForwardReply)) {
	a := c.admissionOwner()
	route := &callRoute{callID: call.CallId, attemptID: call.AttemptId, send: reply}
	refuse := func(reason string) { a.respond(&callResult{route: route, entry: reason}) }
	size := proto.Size(call)
	if size > 1<<20 {
		refuse("busy")
		return
	}
	wire, err := deviceauth.FromEnvelope(call.Command)
	if err != nil {
		refuse("invalid_envelope")
		return
	}
	h, err := deviceauth.ParseHeader(wire.Header)
	if err != nil || h.Purpose != deviceauth.Command {
		refuse("invalid_scope")
		return
	}
	f := &authFrame{wire: wire, peer: h.Sender, size: size, waiting: waiting, route: route}
	if !a.enqueue(f) {
		refuse("busy")
	}
}
func notify(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
func (a *commandAdmission) enqueue(f *authFrame) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || f.waiting.Err() != nil || !a.frames.Push(f.peer, f.peer, f.size, f) {
		return false
	}
	notify(a.wake)
	return true
}
func (a *commandAdmission) authenticateLoop() {
	for {
		a.mu.Lock()
		frame, ok := a.frames.Pop(func(*authFrame) bool { return true })
		if a.frames.Len() > 0 {
			notify(a.wake)
		}
		closed := a.closed
		a.mu.Unlock()
		if closed {
			return
		}
		if !ok {
			select {
			case <-a.ctx.Done():
				return
			case <-a.wake:
			}
			continue
		}
		if frame.waiting.Err() != nil {
			continue
		}
		if a.endpoint.security == nil {
			a.respond(&callResult{route: frame.route, entry: "security_unavailable"})
			continue
		}
		p, err := a.authenticate(frame.wire)
		if errors.Is(err, deviceauth.ErrUnknown) && !frame.retried {
			a.syncEpoch(frame)
			continue
		}
		if err != nil {
			a.respond(&callResult{route: frame.route, entry: "authentication_failed"})
			continue
		}
		// Do not retain ciphertext in an execution closure or an auxiliary map.
		frame.wire = deviceauth.Wire{}
		a.classify(frame.waiting, frame.route, p)
	}
}
func (a *commandAdmission) syncEpoch(f *authFrame) {
	a.mu.Lock()
	if a.closed || a.syncing[f.peer] || len(a.syncing) >= 8 {
		a.mu.Unlock()
		a.respond(&callResult{route: f.route, entry: "busy"})
		return
	}
	a.syncing[f.peer] = true
	a.workers.Go(func() {
		ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
		defer cancel()
		stop := context.AfterFunc(f.waiting, cancel)
		defer stop()
		err := a.endpoint.security.refresh(ctx)
		a.mu.Lock()
		delete(a.syncing, f.peer)
		a.mu.Unlock()
		f.retried = true
		if err != nil || !a.enqueue(f) {
			a.respond(&callResult{route: f.route, entry: "authentication_failed"})
		}
	})
	a.mu.Unlock()
}

func (a *commandAdmission) existing(waiting context.Context, route *callRoute, p *deviceauth.Authenticated) bool {
	body, err, wait, found := a.endpoint.security.endpoint.Lookup(waiting, p, uint64(time.Now().UnixMilli()))
	if !found {
		return false
	}
	if wait != nil {
		stop := context.AfterFunc(a.ctx, wait.Cancel)
		header := p.Header()
		// Lookup registered one of at most 32/key and 256/global waiters.
		// Always discharge it, including when shutdown refuses a new task.
		if !a.endpoint.link.Run(func(context.Context) {
			defer wait.Cancel()
			defer stop()
			b, e := wait.Wait()
			a.respond(&callResult{route: route, header: header, body: b, err: e})
		}) {
			wait.Cancel()
			stop()
			_, _ = wait.Wait()
		}
	} else {
		a.result(route, p, body, err, false)
	}
	return true
}

func classifyCommand(cmd *pb.Command) (transport.Class, bool) {
	switch cmd.Kind.(type) {
	case *pb.Command_ResolveApproval:
		return transport.Control, true
	case *pb.Command_Interrupt, *pb.Command_AnswerQuestion, *pb.Command_AnswerPlan, *pb.Command_CancelInput, *pb.Command_ResumeInput, *pb.Command_GetLiveSnapshot:
		return transport.Control, false
	case *pb.Command_Send:
		return transport.Input, false
	case *pb.Command_BeginImageUpload, *pb.Command_PutImageChunk, *pb.Command_CompleteImageUpload, *pb.Command_AbortImageUpload, *pb.Command_GetSharedFileChunk:
		return transport.Bulk, false
	default:
		return transport.Ordinary, false
	}
}
func controlPriority(cmd *pb.Command) uint8 {
	switch cmd.Kind.(type) {
	case *pb.Command_ResolveApproval:
		return 0
	case *pb.Command_AnswerQuestion, *pb.Command_AnswerPlan, *pb.Command_Interrupt:
		return 1
	case *pb.Command_CancelInput, *pb.Command_ResumeInput:
		return 2
	case *pb.Command_GetLiveSnapshot:
		return 3
	default:
		return 0
	}
}

func commandResources(h deviceauth.Header, cmd *pb.Command) []string {
	var resources []string
	if h.Session != "" {
		resources = append(resources, "session:"+h.Session)
	}
	class, _ := classifyCommand(cmd)
	if class == transport.Bulk || cmd.GetGetImageUploadStatus() != nil || cmd.GetUpdateSettings() != nil || cmd.GetGetSettings() != nil || cmd.GetSend() != nil && (len(cmd.GetSend().ImageIds) > 0 || len(cmd.GetSend().FileIds) > 0) {
		resources = append(resources, "uploads")
	}
	if len(resources) == 0 {
		// Independent read backends must not quarantine the whole machine.
		kind := cmd.ProtoReflect().WhichOneof(cmd.ProtoReflect().Descriptor().Oneofs().ByName("kind"))
		resources = append(resources, "machine:"+string(kind.Name()))
	}
	return resources
}
func (a *commandAdmission) classify(waiting context.Context, route *callRoute, p *deviceauth.Authenticated) {
	if a.existing(waiting, route, p) {
		return
	}
	// Run exactly the pre-admission checks (in their original order), but
	// never persist acceptance or wait for a slot on an authentication worker.
	options := a.endpoint.security.acceptOptions()
	options.NoWait = true
	options.Acquire = func(context.Context) (func(), error) { return nil, errAwaitExecution }
	cached, err := a.endpoint.security.endpoint.AcceptAuthenticated(waiting, p, uint64(time.Now().UnixMilli()), options, nil)
	if !errors.Is(err, errAwaitExecution) {
		if errors.Is(err, deviceauth.ErrRunning) {
			if !a.existing(waiting, route, p) {
				a.result(route, p, nil, p.Decision("outcome_unknown", deviceauth.ErrOutcomeUnknown), false)
			}
		} else {
			a.result(route, p, cached, err, false)
		}
		return
	}
	h, body := p.Header(), p.Body()
	cmd := &pb.Command{}
	if err = proto.Unmarshal(body, cmd); err != nil {
		a.result(route, p, nil, p.Decision("outcome_unknown", deviceauth.ErrInvalid), false)
		return
	}
	class, approval := classifyCommand(cmd)
	raw := cmd.GetGetSharedFileChunk() != nil
	j := &transport.Job{Class: class, Peer: h.Sender, Session: h.Session, Resources: commandResources(h, cmd), Bytes: p.Size(), Deadline: time.UnixMilli(int64(h.Deadline)), Approval: approval, Priority: controlPriority(cmd)}
	queuedAt := time.Now()
	observe := func(acquired bool, at time.Time) {
		if approval && a.endpoint.r.observeApproval != nil {
			a.endpoint.r.observeApproval(acquired, at.Sub(queuedAt))
		}
	}
	j.Reject = func() {
		observe(false, time.Now())
		a.result(route, p, nil, p.Decision("busy", deviceauth.ErrCapacity), raw)
	}
	j.Run = func(ctx context.Context, execution *transport.Execution) {
		observe(true, execution.GrantedAt())
		a.execute(ctx, waiting, route, p, raw, execution)
	}
	if !a.scheduler.Submit(waiting, j) {
		j.Reject()
	}
}
func (a *commandAdmission) execute(ctx, waiting context.Context, route *callRoute, p *deviceauth.Authenticated, raw bool, execution *transport.Execution) {
	if ctx.Err() != nil {
		a.result(route, p, nil, p.Decision("outcome_unknown", deviceauth.ErrOutcomeUnknown), raw)
		return
	}
	ctx, work := contextio.Track(ctx)
	admitting, cancel := context.WithCancel(waiting)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	type result struct {
		body []byte
		err  error
	}
	done := make(chan result, 1)
	var runningMu sync.Mutex
	var abandon func()
	abandoned := false
	markUnknown := func() {
		runningMu.Lock()
		abandoned = true
		if abandon != nil {
			abandon()
		}
		runningMu.Unlock()
	}
	// Exactly one backend goroutine per reserved execution slot. The owner
	// below cannot release the slot until this callback and tracked I/O exit.
	go func() {
		options := a.endpoint.security.acceptOptions()
		options.NoWait = true
		options.Running = func(fn func()) {
			runningMu.Lock()
			abandon = fn
			if abandoned {
				fn()
			}
			runningMu.Unlock()
		}
		b, err := a.endpoint.security.endpoint.AcceptAuthenticated(admitting, p, uint64(time.Now().UnixMilli()), options, func(h deviceauth.Header, plain []byte) (deviceauth.Completion, error) {
			completion, err := a.endpoint.commandCompletion(ctx, h, plain)
			work.Wait()
			select {
			case <-work.Violated():
				return deviceauth.Undetermined(), deviceauth.ErrOutcomeUnknown
			default:
			}
			return completion, err
		})
		work.Wait()
		done <- result{b, err}
	}()
	var r result
	select {
	case r = <-done:
		select {
		case <-work.Violated():
			markUnknown()
			r = result{nil, p.Decision("outcome_unknown", deviceauth.ErrOutcomeUnknown)}
		default:
		}
	case <-ctx.Done():
		markUnknown()
		execution.Quarantine()
		a.result(route, p, nil, p.Decision("outcome_unknown", deviceauth.ErrOutcomeUnknown), raw)
		<-done // Keep the original slot and resource quarantine until real exit.
		return
	case <-work.Violated():
		markUnknown()
		execution.Quarantine()
		cancel()
		a.result(route, p, nil, p.Decision("outcome_unknown", deviceauth.ErrOutcomeUnknown), raw)
		<-done
		return
	}
	if errors.Is(r.err, deviceauth.ErrRunning) {
		execution.Finish()
		if !a.existing(waiting, route, p) {
			a.result(route, p, nil, p.Decision("outcome_unknown", deviceauth.ErrOutcomeUnknown), raw)
		}
		return
	}
	a.result(route, p, r.body, r.err, raw)
}

func (a *commandAdmission) result(route *callRoute, p *deviceauth.Authenticated, body []byte, err error, raw bool) {
	a.respond(&callResult{route: route, header: p.Header(), body: body, err: err, raw: raw})
}
func (a *commandAdmission) respond(r *callResult) {
	a.mu.Lock()
	defer a.mu.Unlock()
	// A full return path cannot retain unbounded results or stall auth/slots.
	// Losing a reply leaves original-ID recovery to the existing protocol.
	if a.closed || !a.results.Push(r.header.Sender, "reply", max(256, len(r.body)), r) {
		return
	}
	notify(a.replies)
}
func (a *commandAdmission) resultLoop() {
	for {
		a.mu.Lock()
		r, ok := a.results.Pop(func(*callResult) bool { return true })
		if a.results.Len() > 0 {
			notify(a.replies)
		}
		closed := a.closed
		a.mu.Unlock()
		if closed {
			return
		}
		if !ok {
			select {
			case <-a.ctx.Done():
				return
			case <-a.replies:
			}
			continue
		}
		if r.entry != "" {
			r.route.reply(&pb.ForwardReply{CallId: r.route.callID, AttemptId: r.route.attemptID, Outcome: "unknown", Reason: r.entry, EntryFailure: true})
		} else {
			a.endpoint.replyResult(r.route, r.header, r.raw, r.body, r.err)
		}
	}
}
func (a *commandAdmission) close() {
	a.mu.Lock()
	a.closed = true
	a.cancel()
	a.mu.Unlock()
	a.scheduler.Close()
	a.workers.Wait()
}

// Internal handoff only; never sent as a new wire reason or user message.
type awaitExecution struct{}

func (*awaitExecution) Error() string { return deviceauth.ErrCapacity.Error() }

var errAwaitExecution error = &awaitExecution{}
