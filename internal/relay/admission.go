package relay

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/contextio"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/transport"
	"github.com/Lewin671/remote-agent/shared/devicesec"
	"github.com/Lewin671/remote-agent/shared/linkcore"
	"google.golang.org/protobuf/proto"
)

type callRoute struct {
	binding   *deviceauth.PeerBinding
	directUse uint64
	allow     func() bool // local atomic flags only; callable under endpoint admission lock

	callID    uint64
	attemptID string
	send      func(*pb.ForwardReply)
	once      sync.Once
}

func (r *callRoute) reply(v *pb.ForwardReply) { r.once.Do(func() { r.send(v) }) }

type authFrame struct {
	wire    devicesec.Wire
	peer    string
	size    int
	waiting context.Context
	route   *callRoute
	retried bool
}
type callResult struct {
	route  *callRoute
	header devicesec.Header
	body   []byte
	err    error
	raw    bool
	entry  string
}
type inflightLeg struct {
	waiting context.Context
	binding *deviceauth.PeerBinding
	allow   func() bool
	active  bool
}

type pendingExecution struct {
	key       string
	payload   devicesec.Hash
	raw       bool
	done      chan struct{}
	cancel    context.CancelFunc
	active    int
	waiters   int
	legs      []*inflightLeg
	stops     []func() bool
	abandoned bool
	finished  bool
	body      []byte
	err       error
	retry     bool
}

type commandAdmission struct {
	authenticate     func(devicesec.Wire) (*deviceauth.Authenticated, error)
	reserveWaiter    func(string) (func(), bool)
	testBeforeFinish func(*pendingExecution)
	endpoint         *endpoint
	ctx              context.Context
	cancel           context.CancelFunc
	workers          sync.WaitGroup
	mu               sync.Mutex
	frames           linkcore.FairQueue[*authFrame]
	results          linkcore.FairQueue[*callResult]
	wake, replies    chan struct{}
	syncing          map[string]bool
	inflight         map[string]*pendingExecution
	inflightWaiters  int
	scheduler        *transport.Scheduler
	closed           bool
}

func (c *endpoint) admissionOwner() *commandAdmission {
	c.admissionOnce.Do(func() {
		ctx, cancel := context.WithCancel(c.link.Context())
		a := &commandAdmission{endpoint: c, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 2), replies: make(chan struct{}, 2), syncing: map[string]bool{}, inflight: map[string]*pendingExecution{}, scheduler: transport.NewSchedulerWithClock(c.link, c.r.SchedulerNow, c.r.SchedulerAfter)}
		a.frames = linkcore.FairQueue[*authFrame]{Entries: 64, Bytes: 4 << 20, PeerEntries: 8, PeerBytes: 1 << 20}
		a.results = linkcore.FairQueue[*callResult]{Entries: 64, Bytes: 4 << 20, PeerEntries: 64, PeerBytes: 4 << 20}
		a.authenticate = func(w devicesec.Wire) (*deviceauth.Authenticated, error) {
			return c.security.endpoint.Authenticate(w, nil, uint64(time.Now().UnixMilli()))
		}
		a.reserveWaiter = c.security.endpoint.ReserveWaiter
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
	route := &callRoute{callID: call.CallId, attemptID: call.AttemptId, send: reply}
	c.dispatchRoute(waiting, call, route)
}
func (c *endpoint) dispatchDirect(waiting context.Context, binding deviceauth.PeerBinding, use uint64, allow func() bool, env *pb.Envelope, reply func(*pb.ForwardReply)) {
	route := &callRoute{binding: &binding, directUse: use, allow: allow, send: reply}
	c.dispatchRoute(waiting, &pb.ForwardCall{Command: env}, route)
}
func (c *endpoint) dispatchRoute(waiting context.Context, call *pb.ForwardCall, route *callRoute) {
	a := c.admissionOwner()
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
	h, err := devicesec.ParseHeader(wire.Header)
	if err != nil || h.Purpose != devicesec.Command {
		refuse("invalid_scope")
		return
	}
	if route.binding != nil && (h.Sender != route.binding.Peer.ID || route.allow == nil || !route.allow()) {
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
		if frame.route.binding != nil && (p.Header().Sender != frame.route.binding.Peer.ID || !frame.route.allow()) {
			continue
		}
		// Do not retain ciphertext in an execution closure or an auxiliary map.
		frame.wire = devicesec.Wire{}
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

func isInteractiveCommand(cmd *pb.Command) bool {
	switch cmd.Kind.(type) {
	case *pb.Command_Send,
		*pb.Command_Interrupt,
		*pb.Command_ResolveApproval,
		*pb.Command_AnswerQuestion,
		*pb.Command_AnswerPlan,
		*pb.Command_CancelInput,
		*pb.Command_ResumeInput:
		return true
	}
	return false
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

func commandResources(h devicesec.Header, cmd *pb.Command) []string {
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
	options := a.acceptOptions(route)
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
		a.result(route, p, nil, p.Decision("outcome_unknown", devicesec.ErrInvalid), false)
		return
	}
	class, approval := classifyCommand(cmd)
	if route.binding != nil {
		bulk := cmd.GetPutImageChunk() != nil || cmd.GetGetSharedFileChunk() != nil
		allowed := route.directUse == 2 && bulk || route.directUse == 1 && (deviceauth.IsReadOnlyCommand(cmd) || isInteractiveCommand(cmd)) && !bulk
		if !allowed {
			a.result(route, p, nil, p.Decision("path_unavailable", devicesec.ErrUntrusted), false)
			return
		}
	}
	raw := cmd.GetGetSharedFileChunk() != nil
	pc, waitCtx, owned := a.claimInflight(waiting, route, p, raw)
	if !owned {
		return
	}
	if a.existing(waiting, route, p) {
		a.finishInflight(pc, nil, nil, raw, true)
		return
	}
	j := &transport.Job{Class: class, Peer: h.Sender, Session: h.Session, Resources: commandResources(h, cmd), Bytes: p.Size(), Deadline: time.UnixMilli(int64(h.Deadline)), Approval: approval, Snapshot: cmd.GetGetLiveSnapshot() != nil, Priority: controlPriority(cmd)}
	queuedAt := time.Now()
	observe := func(acquired bool, at time.Time) {
		if approval && a.endpoint.r.observeApproval != nil {
			a.endpoint.r.observeApproval(acquired, at.Sub(queuedAt))
		}
	}
	j.Reject = func() {
		observe(false, time.Now())
		err := p.Decision("busy", devicesec.ErrCapacity)
		a.finishInflight(pc, nil, err, raw, false)
		a.result(route, p, nil, err, raw)
	}
	j.Run = func(ctx context.Context, execution *transport.Execution) {
		observe(true, execution.GrantedAt())
		a.execute(ctx, waitCtx, waiting, route, p, raw, execution, pc)
	}
	if !a.scheduler.Submit(waitCtx, j) {
		j.Reject()
	}
}

func (a *commandAdmission) abandonInflightLocked(pc *pendingExecution) {
	pc.abandoned = true
	if a.inflight[pc.key] == pc {
		delete(a.inflight, pc.key)
	}
}

func (a *commandAdmission) watchInflight(waiting context.Context, pc *pendingExecution) func() bool {
	stop := context.AfterFunc(waiting, func() {
		a.mu.Lock()
		pc.active--
		shouldCancel := pc.active == 0 && !pc.finished
		if shouldCancel {
			a.abandonInflightLocked(pc)
		}
		a.mu.Unlock()
		if shouldCancel {
			pc.cancel()
		}
	})
	a.mu.Lock()
	if pc.finished {
		a.mu.Unlock()
		stop()
		return func() bool { return false }
	}
	pc.stops = append(pc.stops, stop)
	a.mu.Unlock()
	return stop
}

func (a *commandAdmission) claimInflight(waiting context.Context, route *callRoute, p *deviceauth.Authenticated, raw bool) (*pendingExecution, context.Context, bool) {
	for {
		a.mu.Lock()
		if a.closed || waiting.Err() != nil {
			a.mu.Unlock()
			return nil, nil, false
		}
		if pc := a.inflight[p.Key()]; pc != nil && !pc.abandoned {
			if pc.payload != p.Payload() {
				a.mu.Unlock()
				a.result(route, p, nil, p.Conflict(), false)
				return nil, nil, false
			}
			a.mu.Unlock()
			// Reserve against Endpoint's shared waiter table outside a.mu so e.mu -> a.mu lock order is never inverted.
			releaseWaiter, ok := a.reserveWaiter(p.Key())
			if !ok {
				a.result(route, p, nil, p.Decision("busy", deviceauth.ErrOutcomeUnknown), raw)
				return nil, nil, false
			}
			a.mu.Lock()
			if a.closed || waiting.Err() != nil {
				a.mu.Unlock()
				releaseWaiter()
				return nil, nil, false
			}
			pc = a.inflight[p.Key()]
			if pc == nil || pc.abandoned {
				a.mu.Unlock()
				releaseWaiter()
				if a.existing(waiting, route, p) {
					return nil, nil, false
				}
				// Prior owner aborted pre-admission (or exited) without durable state in Endpoint; retry to claim execution.
				continue
			}
			if pc.payload != p.Payload() {
				a.mu.Unlock()
				releaseWaiter()
				a.result(route, p, nil, p.Conflict(), false)
				return nil, nil, false
			}
			leg := &inflightLeg{waiting: waiting, binding: route.binding, allow: route.allow, active: true}
			pc.legs = append(pc.legs, leg)
			pc.waiters++
			a.inflightWaiters++
			pc.active++
			a.mu.Unlock()
			stop := a.watchInflight(waiting, pc)
			timeout := min(max(time.Until(time.UnixMilli(int64(p.Header().Deadline))), 0), 30*time.Second)
			if !a.endpoint.link.Run(func(runCtx context.Context) {
				defer func() {
					stop()
					releaseWaiter()
					a.mu.Lock()
					leg.active = false
					pc.waiters--
					a.inflightWaiters--
					a.mu.Unlock()
				}()
				timer := time.NewTimer(timeout)
				defer timer.Stop()
				select {
				case <-pc.done:
					if pc.retry {
						if !a.existing(waiting, route, p) {
							a.result(route, p, nil, p.Decision("outcome_unknown", deviceauth.ErrOutcomeUnknown), raw)
						}
						return
					}
					a.result(route, p, bytes.Clone(pc.body), pc.err, pc.raw)
				case <-waiting.Done():
					a.result(route, p, nil, p.Decision("outcome_unknown", deviceauth.ErrOutcomeUnknown), raw)
				case <-runCtx.Done():
					a.result(route, p, nil, p.Decision("outcome_unknown", deviceauth.ErrOutcomeUnknown), raw)
				case <-a.ctx.Done():
					a.result(route, p, nil, p.Decision("outcome_unknown", deviceauth.ErrOutcomeUnknown), raw)
				case <-timer.C:
					a.result(route, p, nil, p.Decision("outcome_unknown", deviceauth.ErrOutcomeUnknown), raw)
				}
			}) {
				stop()
				releaseWaiter()
				a.mu.Lock()
				leg.active = false
				pc.waiters--
				a.inflightWaiters--
				a.mu.Unlock()
			}
			return nil, nil, false
		}
		waitCtx, cancel := context.WithCancel(a.ctx)
		pc := &pendingExecution{
			key:     p.Key(),
			payload: p.Payload(),
			raw:     raw,
			done:    make(chan struct{}),
			cancel:  cancel,
			active:  1,
			legs:    []*inflightLeg{{waiting: waiting, binding: route.binding, allow: route.allow, active: true}},
		}
		a.inflight[pc.key] = pc
		a.mu.Unlock()
		a.watchInflight(waiting, pc)
		return pc, waitCtx, true
	}
}

func (a *commandAdmission) finishInflight(pc *pendingExecution, body []byte, err error, raw, retry bool) {
	if a.testBeforeFinish != nil {
		a.testBeforeFinish(pc)
	}
	a.mu.Lock()
	if pc.finished {
		a.mu.Unlock()
		return
	}
	pc.finished = true
	pc.body = bytes.Clone(body)
	pc.err = err
	pc.raw = raw
	pc.retry = retry
	if a.inflight[pc.key] == pc {
		delete(a.inflight, pc.key)
	}
	stops := pc.stops
	pc.stops = nil
	close(pc.done)
	a.mu.Unlock()
	for _, stop := range stops {
		stop()
	}
	pc.cancel()
}

func (a *commandAdmission) execute(ctx, waitCtx, waiting context.Context, route *callRoute, p *deviceauth.Authenticated, raw bool, execution *transport.Execution, pc *pendingExecution) {
	if ctx.Err() != nil {
		err := p.Decision("outcome_unknown", deviceauth.ErrOutcomeUnknown)
		a.finishInflight(pc, nil, err, raw, false)
		a.result(route, p, nil, err, raw)
		return
	}
	ctx, work := contextio.Track(ctx)
	admitting, cancel := context.WithCancel(waitCtx)
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
		options := a.acceptOptionsForInflight(pc)
		options.NoWait = true
		options.Running = func(fn func()) {
			runningMu.Lock()
			abandon = fn
			if abandoned {
				fn()
			}
			runningMu.Unlock()
		}
		b, err := a.endpoint.security.endpoint.AcceptAuthenticated(admitting, p, uint64(time.Now().UnixMilli()), options, func(h devicesec.Header, plain []byte) (deviceauth.Completion, error) {
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
		err := p.Decision("outcome_unknown", deviceauth.ErrOutcomeUnknown)
		a.finishInflight(pc, nil, err, raw, false)
		a.result(route, p, nil, err, raw)
		<-done // Keep the original slot and resource quarantine until real exit.
		return
	case <-work.Violated():
		markUnknown()
		execution.Quarantine()
		cancel()
		err := p.Decision("outcome_unknown", deviceauth.ErrOutcomeUnknown)
		a.finishInflight(pc, nil, err, raw, false)
		a.result(route, p, nil, err, raw)
		<-done
		return
	}
	if errors.Is(r.err, deviceauth.ErrRunning) {
		execution.Finish()
		a.finishInflight(pc, nil, nil, raw, true)
		if !a.existing(waiting, route, p) {
			a.result(route, p, nil, p.Decision("outcome_unknown", deviceauth.ErrOutcomeUnknown), raw)
		}
		return
	}
	a.finishInflight(pc, r.body, r.err, raw, false)
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
	a.mu.Lock()
	remaining := make([]*pendingExecution, 0, len(a.inflight))
	for _, pc := range a.inflight {
		remaining = append(remaining, pc)
	}
	a.mu.Unlock()
	for _, pc := range remaining {
		a.finishInflight(pc, nil, deviceauth.ErrOutcomeUnknown, pc.raw, false)
	}
	a.workers.Wait()
}

// Internal handoff only; never sent as a new wire reason or user message.
type awaitExecution struct{}

func (*awaitExecution) Error() string { return devicesec.ErrCapacity.Error() }

var errAwaitExecution error = &awaitExecution{}

func (a *commandAdmission) acceptOptionsForInflight(pc *pendingExecution) deviceauth.AcceptOptions {
	options := a.endpoint.security.acceptOptions()
	options.DynamicPeerBinding = func() (*deviceauth.PeerBinding, bool) {
		a.mu.Lock()
		defer a.mu.Unlock()
		var liveDirect *deviceauth.PeerBinding
		for _, leg := range pc.legs {
			if !leg.active || leg.waiting.Err() != nil {
				continue
			}
			if leg.binding == nil {
				return nil, true
			}
			if leg.allow != nil && leg.allow() && liveDirect == nil {
				liveDirect = leg.binding
			}
		}
		if liveDirect != nil {
			return liveDirect, true
		}
		a.abandonInflightLocked(pc)
		return nil, false
	}
	return options
}

func (a *commandAdmission) acceptOptions(route *callRoute) deviceauth.AcceptOptions {
	options := a.endpoint.security.acceptOptions()
	if route.binding != nil {
		options.PeerBinding = route.binding
		original := options.AllowNew
		options.AllowNew = func() bool { return original() && route.allow != nil && route.allow() }
	}
	return options
}
