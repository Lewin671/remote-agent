package direct

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Lewin671/remote-agent/shared/linkcore"
	"github.com/Lewin671/remote-agent/shared/peerwire"
	"github.com/pion/webrtc/v4"
)

// SessionConfig configures one authenticated connection round and both PC
// uses. Its callbacks are nonblocking queue admission, never socket writes or
// business execution. A process owner keeps retired sessions until their
// replay window ends, and owns membership, persistent policy and resource
// limits above it.
type SessionConfig struct {
	Chosen      func()
	BeforeOffer func()

	// Control handles supported authenticated types. A zero reply type means
	// unsupported. It cannot execute a Command through this queue.
	Control func(peerwire.Opened, uint64) (uint64, []byte)
	Policy  func() bool

	Self         *Identity
	Peer         peerwire.Public
	Codec        *peerwire.Codec
	Connection   []byte
	Deadline     uint64
	Pool         *Pool
	Allow        func() bool
	// AllowReason optionally refines a failed Allow gate for local diagnostics.
	AllowReason  func() string
	Send         func(Outbound) bool
	Ready        func(use uint64, path *Path)
	End          func()
	STUN         []string
	LoopbackOnly bool
	Port         *PortMux
	Now          func() time.Time
}

// Session executes a linkcore.Session with Pion: one goroutine owns the core,
// feeds it inputs, timer ticks and SDK callbacks as events, and performs its
// effects (signing with the device key at once, SDK calls, sends to the main
// connection, owner callbacks), handing each result straight back. Other
// goroutines read only the View mirrored after every step.
type Session struct {
	cfg       SessionConfig
	core      *linkcore.Session // run goroutine only
	ctx       context.Context
	cancel    context.CancelFunc
	finished  chan struct{}
	roundDone chan struct{}
	roundOnce sync.Once
	born      time.Time
	endReason atomic.Value // first reason the round ended, a fixed word

	input    chan []byte
	held     atomic.Int64
	events   chan linkcore.Event
	delivery chan deliveryResult

	// The core's View after the latest step, for SDK callbacks, the owner and
	// frames queued on the main connection. retired is also set by Retire.
	retired     atomic.Bool
	established atomic.Bool
	retainUntil atomic.Uint64
	generation  [2]atomic.Uint64
	business    [2]atomic.Bool
	current     [2]atomic.Uint64
	peer        atomic.Pointer[peerwire.Public]

	paths   map[uint64]*Path // every PC created, by ID; run goroutine only
	open    map[uint64]*Path // PCs not yet reported closed
	stopped bool

	traceMu sync.Mutex
	trace   []string // signal messages by kind and use, with ms since the round began (bounded)
}

type deliveryResult struct {
	current func() bool
	id      []byte
	status  uint32
	retry   uint64
}

// Outbound freezes the original authorization and deadline alongside queue
// ownership. The main-WS writer must validate it again at actual dispatch.
type Outbound struct {
	Raw      []byte
	Deadline uint64
	Critical bool
	Valid    func() bool
}

func (s *Session) nowTime() time.Time {
	if s.cfg.Now != nil {
		return s.cfg.Now()
	}
	return time.Now()
}

func (s *Session) nowMs() uint64 { return uint64(s.nowTime().UnixMilli()) }

func NewSession(parent context.Context, cfg SessionConfig, initiate bool) (*Session, error) {
	s, initial, err := newSession(parent, cfg, initiate)
	if err != nil {
		return nil, err
	}
	go s.run(initial)
	return s, nil
}

// newSession builds the session without starting it; initial are the
// core's first effects.
func newSession(parent context.Context, cfg SessionConfig, initiate bool) (*Session, []linkcore.Effect, error) {
	if cfg.Self == nil || cfg.Allow == nil || cfg.Send == nil {
		return nil, nil, ErrClosed
	}
	shared, err := cfg.Self.Shared(cfg.Peer)
	if err != nil {
		return nil, nil, ErrClosed
	}
	s := &Session{cfg: cfg, finished: make(chan struct{}), roundDone: make(chan struct{}), input: make(chan []byte, 128), events: make(chan linkcore.Event, 160), delivery: make(chan deliveryResult, 64), paths: map[uint64]*Path{}, open: map[uint64]*Path{}}
	s.born = s.nowTime()
	core, initial, err := linkcore.NewSession(linkcore.SessionConfig{Self: cfg.Self.Public(), Peer: cfg.Peer, Shared: shared, Codec: cfg.Codec, Connection: cfg.Connection, Deadline: cfg.Deadline, Entropy: rand.Reader, Allow: cfg.Allow, Policy: cfg.Policy}, initiate, s.nowMs())
	if err != nil {
		return nil, nil, ErrClosed
	}
	s.core = core
	s.ctx, s.cancel = context.WithCancel(parent)
	s.publish()
	return s, initial, nil
}

func (s *Session) Close()                     { s.endFor("closed_by_owner"); s.cancel() }
func (s *Session) Done() <-chan struct{}      { return s.finished }
func (s *Session) RoundDone() <-chan struct{} { return s.roundDone }
func (s *Session) Wait()                      { <-s.finished }

func (s *Session) finishRound() { s.roundOnce.Do(func() { close(s.roundDone) }) }

func pathAlive(p *Path) bool {
	if p == nil {
		return false
	}
	select {
	case <-p.Done():
		return false
	default:
		return true
	}
}
func (s *Session) Active() bool        { return s.ctx.Err() == nil && !s.retired.Load() }
func (s *Session) RetainUntil() uint64 { return s.retainUntil.Load() }
func (s *Session) Established() bool   { return s.established.Load() }
func (s *Session) Peer() peerwire.Public {
	if p := s.peer.Load(); p != nil {
		return *p
	}
	return s.cfg.Peer
}
func (s *Session) Retire() {
	s.retired.Store(true)
	s.finishRound()
	s.post(linkcore.RetireRound{})
}
func (s *Session) Receive(raw []byte) bool {
	if len(raw) > 64<<10 || s.ctx.Err() != nil {
		return false
	}
	if s.held.Add(int64(len(raw))) > 1<<20 {
		s.held.Add(-int64(len(raw)))
		return false
	}
	copy := append([]byte(nil), raw...)
	select {
	case s.input <- copy:
		return true
	default:
		s.held.Add(-int64(len(raw)))
		return false
	}
}

// Delivery reports the server's result for a relayed frame. current is
// checked when the result is applied, so a main connection replaced in
// between cannot pause or delay the session's senders.
func (s *Session) Delivery(id []byte, status uint32, retry uint64, current func() bool) {
	select {
	case s.delivery <- deliveryResult{current, append([]byte(nil), id...), status, retry}:
	default:
	}
}
func (s *Session) Discovered() { s.post(linkcore.PeerDiscovered{}) }
func (s *Session) SendControl(typ uint64, body []byte) bool {
	if len(body) > 14<<10 || !s.Active() {
		return false
	}
	return s.post(linkcore.QueueControl{Type: typ, Body: append([]byte(nil), body...)})
}

func (s *Session) post(e linkcore.Event) bool {
	select {
	case s.events <- e:
		return true
	default:
		return false
	}
}

func (s *Session) run(initial []linkcore.Effect) {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	defer s.finish()
	s.apply(initial)
	for !s.stopped {
		select {
		case <-s.ctx.Done():
			return
		case raw := <-s.input:
			s.held.Add(-int64(len(raw)))
			s.handle(linkcore.FrameReceived{Raw: raw})
		case e := <-s.events:
			s.handle(s.admit(e))
		case r := <-s.delivery:
			s.deliver(r)
		case <-ticker.C:
			s.tick()
		}
	}
}

func (s *Session) deliver(r deliveryResult) {
	if r.current != nil && r.current() {
		s.handle(linkcore.DeliveryResult{ID: r.id, Status: r.status, Retry: r.retry})
	}
}

// tick reports PCs that have closed since the last tick, then the tick.
func (s *Session) tick() {
	var closed []uint64
	for id, p := range s.open {
		if !pathAlive(p) {
			closed = append(closed, id)
		}
	}
	for _, id := range closed {
		delete(s.open, id)
		s.handle(linkcore.PCClosed{ID: id})
	}
	s.handle(linkcore.Tick{})
}

// admit adds the PC's own admission to an SDK callback as it is applied.
func (s *Session) admit(e linkcore.Event) linkcore.Event {
	switch v := e.(type) {
	case linkcore.PCLocalCandidate:
		v.Valid = s.paths[v.ID] != nil && s.paths[v.ID].valid()
		return v
	case linkcore.PCStateChanged:
		v.Valid = s.paths[v.ID] != nil && s.paths[v.ID].valid()
		return v
	}
	return e
}

// handle runs one core step and performs its effects.
func (s *Session) handle(e linkcore.Event) {
	if s.stopped {
		return
	}
	if s.retired.Load() && !s.core.View().Retired {
		// Retire ran on another goroutine: the core hears it first.
		s.step(linkcore.RetireRound{})
		if _, ok := e.(linkcore.RetireRound); ok || s.stopped {
			return
		}
	}
	s.step(e)
}

func (s *Session) step(e linkcore.Event) {
	effects := s.core.Handle(e, s.nowMs())
	s.publish()
	s.apply(effects)
}

func (s *Session) publish() {
	v := s.core.View()
	if v.Retired {
		s.retired.Store(true)
	}
	s.established.Store(v.Established)
	s.retainUntil.Store(v.RetainUntil)
	for i := range 2 {
		s.generation[i].Store(v.Generation[i])
		s.business[i].Store(v.Business[i])
		s.current[i].Store(v.PC[i])
	}
	peer := s.core.Peer()
	if cur := s.peer.Load(); cur == nil || !bytes.Equal(cur.Instance, peer.Instance) || !bytes.Equal(cur.Boot, peer.Boot) {
		cloned := peer
		cloned.Instance = append([]byte(nil), peer.Instance...)
		cloned.Boot = append([]byte(nil), peer.Boot...)
		s.peer.Store(&cloned)
	}
}

// view is the mirrored View, as other goroutines see it.
func (s *Session) view() linkcore.View {
	v := linkcore.View{Retired: s.retired.Load()}
	for i := range 2 {
		v.Generation[i] = s.generation[i].Load()
		v.Business[i] = s.business[i].Load()
	}
	return v
}

// apply performs effects in order. A result goes back to the core at once,
// before the next effect, as a synchronous call would have returned.
func (s *Session) apply(effects []linkcore.Effect) {
	for _, f := range effects {
		if s.stopped {
			return
		}
		switch f := f.(type) {
		case linkcore.SignFrame:
			s.handle(linkcore.FrameSigned{Op: f.Op, Signature: ed25519.Sign(s.cfg.Self.Sign, f.Message)})
		case linkcore.TransmitFrame:
			s.transmit(f)
		case linkcore.HandleControl:
			typ, body := uint64(0), []byte(nil)
			if s.cfg.Control != nil {
				typ, body = s.cfg.Control(f.Frame, f.Now)
			}
			s.handle(linkcore.ControlHandled{Op: f.Op, Type: typ, Body: body})
		case linkcore.CreatePC:
			s.handle(linkcore.PCCreated{ID: f.ID, OK: s.createPC(f)})
		case linkcore.DescribePC:
			var sdp []byte
			err := ErrClosed
			if p := s.paths[f.ID]; p != nil {
				sdp, err = p.Description(f.Offer, false)
			}
			s.handle(linkcore.PCDescribed{ID: f.ID, SDP: sdp, OK: err == nil})
		case linkcore.SetRemotePC:
			err := ErrClosed
			if p := s.paths[f.ID]; p != nil {
				err = p.Remote(f.SDP, f.Offer)
			}
			s.handle(linkcore.PCRemoteSet{ID: f.ID, OK: err == nil})
		case linkcore.AddCandidatesPC:
			s.handle(linkcore.PCCandidatesAdded{ID: f.ID, OK: s.addCandidates(f)})
		case linkcore.ConfirmPC:
			if p := s.paths[f.ID]; p != nil {
				p.Confirm()
				if s.cfg.Ready != nil {
					s.cfg.Ready(f.Use, p)
				}
			}
		case linkcore.ClosePC:
			if p := s.paths[f.ID]; p != nil {
				p.closeWhy(f.Reason)
			}
		case linkcore.BeforeOffer:
			if s.cfg.BeforeOffer != nil {
				s.cfg.BeforeOffer()
			}
		case linkcore.PCChosen:
			if s.cfg.Chosen != nil {
				s.cfg.Chosen()
			}
		case linkcore.RoundDone:
			s.finishRound()
		case linkcore.FrameObserved:
			s.traceAdd("rx", f.Type, f.Use, true)
		case linkcore.SessionEnd:
			s.endFor(f.Reason)
			s.stopped = true
		}
	}
}

func (s *Session) addCandidates(f linkcore.AddCandidatesPC) bool {
	p := s.paths[f.ID]
	if p == nil {
		return false
	}
	if f.End {
		_ = p.AddCandidate(webrtc.ICECandidateInit{})
		return true
	}
	for _, c := range f.Candidates {
		mid, ufrag, index := string(c.Mid), string(c.Ufrag), uint16(c.Index)
		if p.AddCandidate(webrtc.ICECandidateInit{Candidate: string(c.Original), SDPMid: &mid, SDPMLineIndex: &index, UsernameFragment: &ufrag}) != nil {
			return false
		}
	}
	return true
}

func (s *Session) createPC(f linkcore.CreatePC) bool {
	use, generation, id := f.Use, f.Generation, f.ID
	gate := func() string {
		r := s.view().PCGate(use, generation, s.nowMs(), s.cfg.Deadline, s.cfg.Policy, s.cfg.Allow)
		if r == "lease_or_process" && s.cfg.AllowReason != nil {
			if detail := s.cfg.AllowReason(); detail != "" {
				return detail
			}
		}
		return r
	}
	var source atomic.Pointer[Path]
	post := func(e linkcore.Event) {
		path := source.Load()
		if !s.Active() || path == nil || !path.valid() || s.current[use-1].Load() != id || s.generation[use-1].Load() != generation {
			return
		}
		select {
		case s.events <- e:
		default:
			// Closing the captured path cannot cancel a replacement, even if
			// the core changes generation between admission and this branch.
			path.closeWhy("event_queue_full")
		}
	}
	path, err := New(s.ctx, Config{Owner: string(s.cfg.Connection), Use: use, Peer: string(s.cfg.Peer.ID), Generation: generation, Pool: s.cfg.Pool, STUN: s.cfg.STUN, Port: s.cfg.Port, Allow: func() bool {
		return gate() == ""
	}, Why: gate, Ended: func() { s.logSignals(use) }, LoopbackOnly: s.cfg.LoopbackOnly,
		Candidate: func(c *webrtc.ICECandidateInit) {
			e := linkcore.PCLocalCandidate{ID: id, End: c == nil}
			if c != nil {
				e.Candidate = []byte(c.Candidate)
			}
			post(e)
		},
		State: func(state webrtc.PeerConnectionState) {
			if state != webrtc.PeerConnectionStateClosed {
				post(linkcore.PCStateChanged{ID: id, State: pcState(state)})
			}
		},
	})
	if err != nil {
		return false
	}
	source.Store(path)
	s.paths[id] = path
	s.open[id] = path
	return true
}

func pcState(state webrtc.PeerConnectionState) linkcore.PCState {
	switch state {
	case webrtc.PeerConnectionStateConnected:
		return linkcore.PCConnected
	case webrtc.PeerConnectionStateDisconnected:
		return linkcore.PCDisconnected
	case webrtc.PeerConnectionStateFailed:
		return linkcore.PCFailed
	}
	return linkcore.PCOther
}

func (s *Session) transmit(f linkcore.TransmitFrame) {
	sent := s.cfg.Send(Outbound{Raw: f.Raw, Deadline: f.Deadline, Critical: f.Critical,
		Valid: func() bool {
			if !f.Current(s.view()) {
				return false
			}
			if f.Class == 1 && f.Type == 1 && !s.cfg.Pool.ownerAvailable(string(s.cfg.Peer.ID), string(s.cfg.Connection)) {
				return false
			}
			return s.ctx.Err() == nil && s.cfg.Allow() && s.nowMs() < f.Deadline
		},
	})
	s.traceAdd("tx", f.Type, f.Use, sent)
}

// finish ends the session: every PC closes and is waited for.
func (s *Session) finish() {
	s.endFor("ctx_done")
	s.logEnd(s.core.View().Established, s.core.Steps())
	s.cancel()
	s.finishRound()
	for _, p := range s.paths {
		p.Close()
	}
	for _, p := range s.paths {
		p.Wait()
	}
	if s.cfg.End != nil {
		s.cfg.End()
	}
	close(s.finished)
}

const maxTrace = 64

// traceAdd keeps the kind (not the content) of each signal message sent or
// received, with the use and the time since the round began, up to maxTrace.
func (s *Session) traceAdd(dir string, typ, use uint64, ok bool) {
	if use == 0 && typ == 4 {
		return // acknowledgements: too many to be of use here
	}
	s.traceMu.Lock()
	defer s.traceMu.Unlock()
	if len(s.trace) >= maxTrace {
		return
	}
	mark := ""
	if !ok {
		mark = "!"
	}
	s.trace = append(s.trace, fmt.Sprintf("%s%d.%d%s@%d", dir, typ, use, mark, time.Since(s.born).Milliseconds()))
}

// endFor records why the round ended; only the first reason is kept.
func (s *Session) endFor(reason string) { s.endReason.CompareAndSwap(nil, reason) }

// logEnd writes one line about the round: its reason and which steps each
// peer connection had reached. Fixed words and flags only.
func (s *Session) logEnd(established bool, steps [2]string) {
	reason, _ := s.endReason.Load().(string)
	args := []any{"peer", shortPeerID(string(s.cfg.Peer.ID)), "reason", reason, "at_ms", time.Since(s.born).Milliseconds(), "established", established, "pc1_steps", steps[0], "pc2_steps", steps[1]}
	s.traceMu.Lock()
	args = append(args, "signals", strings.Join(s.trace, " "))
	s.traceMu.Unlock()
	slog.Info("direct round ended", args...)
}

// logSignals writes the signal trace when one path of the round ends, so a
// round that outlives its paths still tells how far the exchange got.
func (s *Session) logSignals(use uint64) {
	s.traceMu.Lock()
	t := strings.Join(s.trace, " ")
	s.traceMu.Unlock()
	slog.Info("direct signals at path end", "peer", shortPeerID(string(s.cfg.Peer.ID)), "use", use, "at_ms", time.Since(s.born).Milliseconds(), "signals", t)
}
