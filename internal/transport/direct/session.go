package direct

import (
	"bytes"
	"context"
	"sync/atomic"
	"time"

	"github.com/Lewin671/remote-agent/internal/transport/peerwire"
	"github.com/pion/webrtc/v4"
)

// Session owns one authenticated connection round and both PC uses. Its I/O
// callbacks are nonblocking queue admission, never socket writes or business
// execution. A process owner keeps retired sessions until their replay window
// ends, and owns membership, persistent policy and resource limits above it.
type SessionConfig struct {
	Chosen      func()
	BeforeOffer func()

	// Control handles supported authenticated types. A zero reply type means
	// unsupported. It cannot execute a Command through this queue.
	Control func(peerwire.Opened, uint64) (uint64, []byte)
	Policy  func() bool

	Self         peerwire.Identity
	Peer         peerwire.Public
	Codec        *peerwire.Codec
	Connection   []byte
	Deadline     uint64
	Pool         *Pool
	Allow        func() bool
	Send         func(Outbound) bool
	Ready        func(use uint64, path *Path)
	End          func()
	STUN         []string
	LoopbackOnly bool
}
type sessionEvent struct {
	use         uint64
	generation  uint64
	path        *Path
	candidate   *webrtc.ICECandidateInit
	state       webrtc.PeerConnectionState
	kind        int
	controlType uint64
	body        []byte
}
type sessionPC struct {
	business                    atomic.Bool
	generation                  uint64
	currentGeneration           atomic.Uint64
	currentPath                 atomic.Pointer[Path]
	actions                     []peerwire.Action
	restartSent                 bool
	signal                      *peerwire.Signal
	sender                      *peerwire.Sender
	seq                         uint64
	path                        *Path
	batch                       peerwire.CandidateBatcher
	localUfrag, localMid        []byte
	confirmed, announced, ended bool
}
type Session struct {
	retired     atomic.Bool
	established atomic.Bool
	retainUntil atomic.Uint64
	finished    chan struct{}
	closedPaths []*Path

	cfg           SessionConfig
	ctx           context.Context
	cancel        context.CancelFunc
	input         chan []byte
	held          atomic.Int64
	events        chan sessionEvent
	delivery      chan deliveryResult
	boot          *peerwire.Sender
	controlWindow *peerwire.Sender
	pcs           [2]sessionPC
	controlSeq    uint64
	challenge     []byte
	receipts      map[string][]byte
	controlSeen   map[string][]byte
	peerScopes    map[uint64]*peerScopeWindow
}
type deliveryResult struct {
	current func() bool
	id      []byte
	status  uint32
	retry   uint64
}

func NewSession(parent context.Context, cfg SessionConfig, initiate bool) (*Session, error) {
	if cfg.Codec == nil || cfg.Allow == nil || cfg.Send == nil || !cfg.Allow() || len(cfg.Connection) != 16 || len(cfg.Self.Instance) != 16 || len(cfg.Self.Boot) != 32 {
		return nil, ErrClosed
	}
	now := uint64(time.Now().UnixMilli())
	if cfg.Deadline <= now || cfg.Deadline > now+10000 {
		return nil, ErrClosed
	}
	ctx, cancel := context.WithCancel(parent)
	s := &Session{finished: make(chan struct{}), cfg: cfg, ctx: ctx, cancel: cancel, input: make(chan []byte, 8), events: make(chan sessionEvent, 160), delivery: make(chan deliveryResult, 64), boot: peerwire.NewSender(cfg.Deadline), receipts: map[string][]byte{}, controlSeen: map[string][]byte{}}
	s.retainUntil.Store(cfg.Deadline + 300000)
	for i := range s.pcs {
		g := peerwire.NewSignal(cfg.Codec, cfg.Self, cfg.Peer, cfg.Connection, uint64(i+1), now)
		g.RoundDeadline = cfg.Deadline
		g.Current = cfg.Allow
		g.LeaseUntil = ^uint64(0)
		s.pcs[i] = sessionPC{generation: 1, signal: g, sender: peerwire.NewSender(cfg.Deadline), batch: peerwire.CandidateBatcher{Generation: 1, Deadline: cfg.Deadline, Binding: 1}}
		s.pcs[i].currentGeneration.Store(1)
	}
	if initiate {
		c := peerwire.NewProbeContext(cfg.Self, cfg.Peer, cfg.Connection, 1, now)
		c.Deadline = cfg.Deadline
		raw, err := cfg.Codec.Seal(cfg.Self, cfg.Peer, c, 1, peerwire.PB(peerwire.B(1, cfg.Connection), peerwire.B(2, cfg.Self.Instance), peerwire.B(3, cfg.Self.Boot)), nil, nil, nil)
		if err != nil {
			cancel()
			return nil, err
		}
		opened, err := cfg.Codec.OpenSent(raw, cfg.Self, cfg.Peer, now)
		if err != nil {
			cancel()
			return nil, err
		}
		for i := range s.pcs {
			if err = s.pcs[i].signal.RememberProbe(opened); err != nil {
				cancel()
				return nil, err
			}
		}
		if !s.boot.Enqueue(raw, now, cfg.Deadline, 1) {
			cancel()
			return nil, ErrLimit
		}
	}
	go s.run()
	return s, nil
}
func (s *Session) Close()                { s.cancel() }
func (s *Session) Done() <-chan struct{} { return s.finished }
func (s *Session) Wait()                 { <-s.finished }
func (s *Session) Active() bool          { return s.ctx.Err() == nil && !s.retired.Load() }
func (s *Session) RetainUntil() uint64   { return s.retainUntil.Load() }
func (s *Session) Established() bool     { return s.established.Load() }
func (s *Session) Retire() {
	s.retired.Store(true)
	select {
	case s.events <- sessionEvent{kind: 5}:
	default:
	}
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
func (s *Session) Delivery(id []byte, status uint32, retry uint64, current func() bool) {
	select {
	case s.delivery <- deliveryResult{current, append([]byte(nil), id...), status, retry}:
	default:
	}
}
func (s *Session) run() {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	defer func() {
		s.cancel()
		defer close(s.finished)
		for i := range s.pcs {
			if p := s.pcs[i].path; p != nil {
				p.Close()
			}
		}
		for i := range s.pcs {
			if p := s.pcs[i].path; p != nil {
				p.Wait()
			}
		}
		for _, p := range s.closedPaths {
			p.Wait()
		}
		if s.cfg.End != nil {
			s.cfg.End()
		}
	}()
	for {
		now := uint64(time.Now().UnixMilli())
		if !s.cfg.Allow() || s.retired.Load() && now >= s.retainUntil.Load() {
			return
		}
		select {
		case <-s.ctx.Done():
			return
		case raw := <-s.input:
			s.held.Add(-int64(len(raw)))
			s.receive(raw, uint64(time.Now().UnixMilli()))
		case e := <-s.events:
			s.event(e, uint64(time.Now().UnixMilli()))
		case r := <-s.delivery:
			s.result(r, uint64(time.Now().UnixMilli()))
		case <-ticker.C:
			if now >= s.cfg.Deadline {
				complete := false
				for i := range s.pcs {
					if s.pcs[i].announced {
						complete = true
					} else if s.pcs[i].path != nil {
						s.pcs[i].path.Close()
						s.closedPaths = append(s.closedPaths, s.pcs[i].path)
						s.pcs[i].path = nil
					}
				}
				if !complete {
					return
				}
			}
			s.flush(now)
		}
	}
}
func (s *Session) flush(now uint64) {
	for _, window := range s.peerScopes {
		if window.gap != 0 && now >= window.gap {
			s.Retire()
			return
		}
	}
	if s.controlWindow != nil {
		for _, raw := range s.controlWindow.Tick(now) {
			s.transmit(raw)
		}
	}
	for _, raw := range s.boot.Tick(now) {
		if s.cfg.Allow() {
			s.transmit(raw)
		}
	}
	for i := range s.pcs {
		p := &s.pcs[i]
		p.signal.Prune(now)
		s.maybeReady(i, now)
		if !p.announced {
			p.signal.Tick(now)
		}
		if p.path != nil && !p.announced && p.signal.Closed {
			p.path.Close()
			s.closedPaths = append(s.closedPaths, p.path)
			p.path = nil
		}
		if !p.announced && p.signal.Retired && now < s.cfg.Deadline {
			s.restartPC(i, now)
		}
		if !p.signal.Closed {
			s.maybeOffer(i, now)
			s.drainActions(i, now)
		}
		batchIndex := p.batch.Batches
		for _, group := range p.batch.Flush(now, p.ended, p.signal.Generation, 1) {
			candidates := make([]peerwire.Candidate, len(group))
			for j, raw := range group {
				candidates[j] = peerwire.Candidate{Original: raw, Ufrag: p.localUfrag, Mid: p.localMid, Generation: p.signal.Generation}
			}
			if !s.sendSignal(i, 5, peerwire.CandidateBody(candidates, uint64(batchIndex)), now) {
				return
			}
			batchIndex++
		}
		// EndCandidates is sent only after all full/tail candidate batches.
		if p.ended && p.batch.Ended && len(p.batch.Pending) == 0 && p.path != nil {
			p.ended = false
			s.sendSignal(i, 6, peerwire.EndBody(p.localUfrag, uint64(p.batch.Count), uint64(p.batch.Batches)), now)
		}
		for _, raw := range p.sender.Tick(now) {
			if s.cfg.Allow() {
				s.transmit(raw)
			}
		}
	}
}
func (s *Session) sendSignal(i int, typ uint64, body []byte, now uint64) bool {
	p := &s.pcs[i]
	p.seq++
	c := peerwire.NewContext(s.cfg.Self, p.signal.Peer, s.cfg.Connection, uint64(i+1), p.signal.Generation, p.seq, now, 1, false)
	c.Deadline = s.cfg.Deadline
	raw, err := s.cfg.Codec.Seal(s.cfg.Self, p.signal.Peer, c, typ, body, nil, nil, nil)
	return err == nil && p.sender.Enqueue(raw, now, c.Deadline, 1)
}
func (s *Session) control(typ uint64, body []byte, now uint64) []byte {
	return s.controlUntil(typ, body, now, now+10000)
}
func (s *Session) controlUntil(typ uint64, body []byte, now, deadline uint64) []byte {
	s.controlSeq++
	peer := s.pcs[0].signal.Peer
	c := peerwire.NewContext(s.cfg.Self, peer, s.cfg.Connection, 0, 1, s.controlSeq, now, 2, false)
	c.Deadline = min(c.Deadline, deadline)
	raw, err := s.cfg.Codec.Seal(s.cfg.Self, peer, c, typ, body, nil, nil, nil)
	if err != nil {
		return nil
	}
	return raw
}
func (s *Session) acknowledge(o peerwire.Opened, now uint64) {
	if o.Context.Class == 1 && o.Type == 1 {
		return
	} // Challenge proves Probe receipt.
	if o.Context.Class == 2 && (o.Type == 4 || o.Type == 10 || o.Type == 11 || o.Type == 12 || o.Type == 13) {
		return
	}
	key := string(o.Hash)
	raw := s.receipts[key]
	if raw == nil {
		if len(s.receipts) >= 2048 {
			return
		}
		raw = s.controlUntil(4, peerwire.PB(peerwire.U(4, o.Context.Class), peerwire.U(5, o.Context.Seq), peerwire.B(6, o.Hash), peerwire.U(7, o.Context.Generation), peerwire.U(8, o.Context.Use)), now, o.Context.Deadline)
		if raw == nil {
			return
		}
		s.receipts[key] = raw
	}
	if s.cfg.Allow() {
		s.transmit(raw)
	}
}
func (s *Session) receive(raw []byte, now uint64) {
	if !s.cfg.Allow() {
		return
	}
	peer := s.pcs[0].signal.Peer
	o, err := s.cfg.Codec.Open(raw, peer, s.cfg.Self, now)
	if err != nil || !bytes.Equal(o.Context.Connection, s.cfg.Connection) {
		return
	}
	if len(s.pcs[0].signal.Seen)+len(s.pcs[1].signal.Seen)+len(s.controlSeen) >= 2047 {
		key := peerwire.Key(o.Context)
		known := bytes.Equal(s.controlSeen[key], o.Hash)
		for i := range s.pcs {
			known = known || bytes.Equal(s.pcs[i].signal.Seen[key], o.Hash)
		}
		if !known {
			return
		}
	}
	c := o.Context
	if s.retired.Load() {
		key := peerwire.Key(c)
		if bytes.Equal(s.controlSeen[key], o.Hash) {
			if reply := s.receipts[string(o.Hash)]; reply != nil {
				s.transmit(reply)
			}
			return
		}
		for i := range s.pcs {
			if s.pcs[i].signal.ReplyAllowed(raw, now) {
				if c.Class == 1 && o.Type == 1 {
					if s.challenge != nil {
						s.transmit(s.challenge)
					}
				} else {
					s.acknowledge(o, now)
				}
				break
			}
		}
		return
	}
	if c.Class == 2 && o.Type == 4 {
		if _, err := peerwire.KnownPayload(2, 4, o.Payload); err != nil {
			return
		}
		f, err := peerwire.ParseMessage(o.Payload, "Ack")
		if err != nil {
			return
		}
		hash := f[6].B
		// An authenticated ACK can settle only a recorded immutable sender packet.
		matched := false
		bootstrap := false
		for _, sender := range s.senders() {
			if pending := sender.Pending[string(hash)]; pending != nil {
				original, err := s.cfg.Codec.OpenSent(pending.Raw, s.cfg.Self, peer, now)
				if err != nil || original.Context.Seq != f[5].U || original.Context.Class != f[4].U || original.Context.Generation != f[7].U || original.Context.Use != f[8].U {
					continue
				}
				matched = true
				if original.Type == 2 && original.Context.Class == 1 && original.Context.Generation == 0 {
					bootstrap = true
				}
			}
		}
		if !matched {
			return
		}
		s.retainUntil.Store(max(s.retainUntil.Load(), c.Deadline+300000))
		if bootstrap {
			for i := range s.pcs {
				if _, err = s.pcs[i].signal.Receive(raw, now); err != nil {
					return
				}
			}
		}
		s.boot.Ack(hash)
		if s.controlWindow != nil {
			s.controlWindow.Ack(hash)
		}
		for i := range s.pcs {
			s.pcs[i].sender.Ack(hash)
			s.maybeOffer(i, now)
		}
		return
	}
	if c.Class == 1 && (o.Type == 1 || o.Type == 2) {
		for i := range s.pcs {
			if _, err = s.pcs[i].signal.Receive(raw, now); err != nil {
				return
			}
		}
		s.retainUntil.Store(max(s.retainUntil.Load(), c.Deadline+300000))
		if o.Type == 1 {
			if s.challenge == nil {
				peer = s.pcs[0].signal.Peer
				c := peerwire.NewContext(s.cfg.Self, peer, s.cfg.Connection, 0, 0, 1, now, 1, true)
				c.Deadline = s.cfg.Deadline
				body := peerwire.PB(peerwire.B(1, s.cfg.Connection), peerwire.B(2, s.cfg.Self.Instance), peerwire.B(3, s.cfg.Self.Boot), peerwire.B(4, peer.Boot), peerwire.B(6, o.Hash))
				s.challenge, err = s.cfg.Codec.Seal(s.cfg.Self, peer, c, 2, body, nil, nil, nil)
				if err != nil {
					return
				}
				sent, err := s.cfg.Codec.OpenSent(s.challenge, s.cfg.Self, peer, now)
				if err != nil {
					return
				}
				for i := range s.pcs {
					if err = s.pcs[i].signal.RememberChallenge(sent); err != nil {
						return
					}
				}
				s.boot.Enqueue(s.challenge, now, c.Deadline, 1)
			} else if s.pcs[0].signal.ReplyAllowed(raw, now) {
				s.transmit(s.challenge)
			}
		} else {
			if probe := s.pcs[0].signal.Probe; probe != nil {
				s.boot.Ack(probe.Hash)
			}
			s.acknowledge(o, now)
			for i := range s.pcs {
				s.maybeOffer(i, now)
			}
		}
		return
	}
	if c.Class == 2 {
		if _, err := peerwire.KnownPayload(c.Class, o.Type, o.Payload); err != nil {
			return
		}
		key := peerwire.Key(c)
		if old, ok := s.controlSeen[key]; ok {
			if !bytes.Equal(old, o.Hash) {
				return
			}
			if reply := s.receipts[string(o.Hash)]; reply != nil && s.cfg.Allow() {
				s.transmit(reply)
			}
			return
		}
		s.controlSeen[key] = append([]byte(nil), o.Hash...)
		s.retainUntil.Store(max(s.retainUntil.Load(), c.Deadline+300000))
		if o.Type == 10 {
			return
		}
		typ := uint64(0)
		var body []byte
		if s.cfg.Control != nil {
			typ, body = s.cfg.Control(o, now)
		}
		if typ == 0 {
			typ = 10
			body = peerwire.PB(peerwire.U(1, c.Class), peerwire.U(2, o.Type), peerwire.U(3, c.Seq), peerwire.B(4, o.Hash))
		}
		if !s.cfg.Allow() || uint64(time.Now().UnixMilli()) >= c.Deadline {
			return
		}
		if reply := s.controlUntil(typ, body, now, c.Deadline); reply != nil {
			s.receipts[string(o.Hash)] = reply
			s.transmit(reply)
		}
		return
	}
	if c.Class == 1 && c.Use == 0 {
		s.receivePeerScope(o, now)
		return
	}
	if c.Use < 1 || c.Use > 2 {
		return
	}
	i := int(c.Use) - 1
	p := &s.pcs[i]
	actions, err := p.signal.Receive(raw, now)
	if err != nil {
		return
	}
	s.retainUntil.Store(max(s.retainUntil.Load(), c.Deadline+300000))
	if p.signal.ReplyAllowed(raw, now) {
		s.acknowledge(o, now)
	}

	for _, a := range actions {
		if a.Class == 2 && a.Type == 10 {
			original, err := s.cfg.Codec.Open(a.Original, p.signal.Peer, s.cfg.Self, now)
			if err == nil {
				if reply := s.controlUntil(10, a.Payload, now, original.Context.Deadline); reply != nil {
					s.receipts[string(original.Hash)] = reply
					s.transmit(reply)
				}
			}
		} else if a.Class == 1 {
			if len(p.actions) >= 32 {
				s.Retire()
				return
			}
			p.actions = append(p.actions, a)
		}
	}
	s.drainActions(i, now)
	s.maybeReady(i, now)
}
func (s *Session) drainActions(i int, now uint64) {
	p := &s.pcs[i]
	for len(p.actions) > 0 && s.Active() {
		a := p.actions[0]
		if a.Generation != p.signal.Generation {
			p.actions = p.actions[1:]
			continue
		}
		if a.Type == 3 && s.cfg.BeforeOffer != nil {
			s.cfg.BeforeOffer()
		}
		if a.Type >= 3 && a.Type <= 6 && !s.makePC(i, now) {
			return
		}
		p.actions = p.actions[1:]
		var err error
		switch a.Type {
		case 3, 4:
			if !s.makePC(i, now) || !p.signal.Dispatch(a.Original, now) {
				return
			}
			if err = p.path.Remote(a.Payload, a.Type == 3); err != nil {
				p.path.Close()
				return
			}
			if a.Type == 3 {
				answer, err := p.path.Description(false, false)
				if err != nil {
					return
				}
				if err = p.signal.SetLocal(answer, false, p.signal.Generation); err != nil {
					return
				}
				info, _ := peerwire.InspectSDP(answer)
				p.localUfrag, p.localMid = info.Ufrag, info.Mid
				s.sendSignal(i, 4, peerwire.SDPBody(answer), now)
			}
		case 5:
			if p.path == nil || !p.signal.Dispatch(a.Original, now) {
				return
			}
			cs, _, err := peerwire.ReadCandidates(a.Payload)
			if err != nil {
				return
			}
			for _, c := range cs {
				mid, ufrag, index := string(c.Mid), string(c.Ufrag), uint16(c.Index)
				if err = p.path.AddCandidate(webrtc.ICECandidateInit{Candidate: string(c.Original), SDPMid: &mid, SDPMLineIndex: &index, UsernameFragment: &ufrag}); err != nil {
					return
				}
			}
		case 6:
			if p.path == nil || !p.signal.Dispatch(a.Original, now) {
				return
			}
			_ = p.path.AddCandidate(webrtc.ICECandidateInit{})
		case 8:
			s.restartPC(i, now)
		case 9:
			if p.path != nil {
				p.path.Close()
			}
		}
	}
}

func (s *Session) makePC(i int, now uint64) bool {
	p := &s.pcs[i]
	if p.generation != p.signal.Generation {
		s.resetPC(i)
	}
	if p.path != nil {
		return true
	}
	if s.retired.Load() || now >= s.cfg.Deadline || !s.cfg.Allow() || s.cfg.Policy != nil && !s.cfg.Policy() {
		return false
	}
	use := uint64(i + 1)
	expires := s.cfg.Deadline
	generation := p.signal.Generation
	live := func() bool {
		return p.currentGeneration.Load() == generation && (p.business.Load() || uint64(time.Now().UnixMilli()) < expires)
	}
	var source atomic.Pointer[Path]
	post := func(e sessionEvent) {
		path := source.Load()
		if !s.Active() || path == nil || !path.valid() || p.currentPath.Load() != path || p.currentGeneration.Load() != generation || e.state == webrtc.PeerConnectionStateClosed {
			return
		}
		e.path = path
		e.use = use
		e.generation = generation
		select {
		case s.events <- e:
		default:
			// Closing the captured path cannot cancel a replacement, even if
			// the actor changes generation between admission and this branch.
			path.Close()
		}
	}
	path, err := New(s.ctx, Config{Owner: string(s.cfg.Connection), Use: use, Peer: string(s.cfg.Peer.ID), Generation: p.signal.Generation, Pool: s.cfg.Pool, STUN: s.cfg.STUN, Allow: func() bool {
		return !s.retired.Load() && live() && (s.cfg.Policy == nil || s.cfg.Policy()) && s.cfg.Allow() && live() && !s.retired.Load() && (s.cfg.Policy == nil || s.cfg.Policy())
	}, LoopbackOnly: s.cfg.LoopbackOnly,
		Candidate: func(c *webrtc.ICECandidateInit) { post(sessionEvent{candidate: c, kind: 1}) },
		State:     func(state webrtc.PeerConnectionState) { post(sessionEvent{state: state, kind: 2}) },
	})
	if err != nil {
		return false
	}
	p.path = path
	source.Store(path)
	p.currentPath.Store(path)
	if s.cfg.Chosen != nil {
		s.cfg.Chosen()
	}
	return true
}
func (s *Session) maybeOffer(i int, now uint64) {
	p := &s.pcs[i]
	if !p.signal.BootReady || !p.signal.Offerer() || p.signal.LocalSDP || p.signal.Closed || !s.Active() || now >= s.cfg.Deadline || !s.cfg.Allow() || (s.cfg.Policy != nil && !s.cfg.Policy()) {
		return
	}
	if p.path == nil && s.cfg.BeforeOffer != nil {
		s.cfg.BeforeOffer()
	}
	if !s.makePC(i, now) {
		return
	}
	offer, err := p.path.Description(true, false)
	if err != nil {
		return
	}
	if err = p.signal.SetLocal(offer, true, p.signal.Generation); err != nil {
		return
	}
	info, _ := peerwire.InspectSDP(offer)
	p.localUfrag, p.localMid = info.Ufrag, info.Mid
	s.sendSignal(i, 3, peerwire.SDPBody(offer), now)
}
func (s *Session) event(e sessionEvent, now uint64) {
	if e.kind == 6 {
		if !s.Active() || len(s.pcs[0].signal.Peer.Boot) != 32 {
			return
		}
		raw := s.control(e.controlType, e.body, now)
		if raw == nil {
			return
		}
		if s.controlWindow == nil || s.controlWindow.TimedOut || len(s.controlWindow.Pending) == 0 {
			s.controlWindow = peerwire.NewSender(now + 10000)
		}
		s.controlWindow.Enqueue(raw, now, now+10000, 1)
		return
	}

	if e.kind == 5 {
		for i := range s.pcs {
			p := &s.pcs[i]
			p.signal.Closed = true
			if p.path != nil {
				p.path.Close()
			}
		}
		return
	}
	if e.kind == 3 {
		s.boot.Discovered()
		for i := range s.pcs {
			s.pcs[i].sender.Discovered()
		}
		return
	}
	if e.use < 1 || e.use > 2 {
		return
	}
	i := int(e.use) - 1
	p := &s.pcs[i]
	if !s.Active() || p.path == nil || e.path != p.path || e.generation != p.signal.Generation {
		return
	}
	if (e.kind == 1 || e.state == webrtc.PeerConnectionStateConnected) && !p.path.valid() {
		return
	}
	if e.kind == 1 {
		if e.candidate == nil {
			p.ended = true
		} else if !p.batch.Add([]byte(e.candidate.Candidate), p.signal.Generation, now, 1) {
			p.path.Close()
			return
		}
	} else if e.state == webrtc.PeerConnectionStateFailed || e.state == webrtc.PeerConnectionStateDisconnected {
		s.restartPC(i, now)
	} else if e.state == webrtc.PeerConnectionStateConnected {
		p.signal.DTLS = true
		s.maybeReady(i, now)
	}
}
func (s *Session) maybeReady(i int, now uint64) {
	p := &s.pcs[i]
	if p.path == nil {
		return
	}
	if !p.confirmed && p.signal.LocalSDP && p.signal.RemoteSDP && p.signal.DTLS {
		p.confirmed = s.sendSignal(i, 7, p.signal.ConfirmBody(), now)
	}
	if p.path.TraversalReady() && p.confirmed && !p.announced && p.signal.Ready() {
		p.announced = true
		p.business.Store(true)
		s.established.Store(true)
		p.path.Confirm()
		if s.cfg.Ready != nil {
			s.cfg.Ready(uint64(i+1), p.path)
		}
	}
}
func (s *Session) result(r deliveryResult, now uint64) {
	if r.current == nil || !r.current() {
		return
	}
	for _, sender := range s.senders() {
		for _, pending := range sender.Pending {
			fields, err := peerwire.Parse(pending.Raw, 5)
			if err != nil || !bytes.Equal(fields[4].B, r.id) {
				continue
			}
			if r.status == 3 {
				sender.Limited(pending.Hash, now, r.retry)
			}
			if r.status == 2 {
				sender.Offline()
			}
		}
	}
}

func (s *Session) Discovered() {
	select {
	case s.events <- sessionEvent{kind: 3}:
	default:
	}
}

// Outbound freezes the original authorization and deadline alongside queue
// ownership. The main-WS writer must validate it again at actual dispatch.
type Outbound struct {
	Raw      []byte
	Deadline uint64
	Critical bool
	Valid    func() bool
}

func (s *Session) transmit(raw []byte) bool {
	now := uint64(time.Now().UnixMilli())
	peer := s.pcs[0].signal.Peer
	original, err := s.cfg.Codec.OpenSent(raw, s.cfg.Self, peer, now)
	if err != nil || !s.cfg.Allow() {
		return false
	}
	deadline := original.Context.Deadline
	return s.cfg.Send(Outbound{Raw: raw, Deadline: deadline, Critical: original.Context.Class == 2 || original.Type != 5 && original.Type != 6,
		Valid: func() bool {
			c := original.Context
			if c.Class == 1 && (original.Type == 1 || original.Type >= 3 && original.Type <= 8) && s.retired.Load() {
				return false
			}
			if c.Class == 1 && c.Use > 0 && c.Use <= 2 && c.Generation != s.pcs[c.Use-1].currentGeneration.Load() {
				return false
			}
			if original.Context.Class == 1 && original.Type == 1 && !s.cfg.Pool.ownerAvailable(string(s.cfg.Peer.ID), string(s.cfg.Connection)) {
				return false
			}
			return s.ctx.Err() == nil && s.cfg.Allow() && uint64(time.Now().UnixMilli()) < deadline
		},
	})
}

func (s *Session) senders() []*peerwire.Sender {
	out := []*peerwire.Sender{s.boot, s.pcs[0].sender, s.pcs[1].sender}
	if s.controlWindow != nil {
		out = append(out, s.controlWindow)
	}
	return out
}
func (s *Session) SendControl(typ uint64, body []byte) bool {
	if len(body) > 14<<10 || !s.Active() {
		return false
	}
	select {
	case s.events <- sessionEvent{kind: 6, controlType: typ, body: append([]byte(nil), body...)}:
		return true
	default:
		return false
	}
}

// Replacement SDK objects are reserved only after the old PC actually closes.
// The actor retries pending actions; neither a gap nor restart resets the round.
func (s *Session) resetPC(i int) {
	p := &s.pcs[i]
	p.currentPath.Store(nil)
	p.currentGeneration.Store(p.signal.Generation)
	if p.path != nil {
		p.path.Close()
		s.closedPaths = append(s.closedPaths, p.path)
		p.path = nil
	}
	p.generation = p.signal.Generation
	p.currentGeneration.Store(p.generation)
	p.seq = 0
	p.sender = peerwire.NewSender(s.cfg.Deadline)
	p.batch = peerwire.CandidateBatcher{Generation: p.signal.Generation, Deadline: s.cfg.Deadline, Binding: 1}
	p.business.Store(false)
	p.confirmed = false
	p.announced = false
	p.ended = false
	p.restartSent = false
	p.localUfrag = nil
	p.localMid = nil
}
func (s *Session) restartPC(i int, now uint64) {
	p := &s.pcs[i]
	if now >= s.cfg.Deadline || !s.Active() {
		return
	}
	if p.signal.Offerer() {
		if p.signal.NextGeneration(p.signal.Generation+1) != nil {
			return
		}
		s.resetPC(i)
		p.actions = nil
		s.maybeOffer(i, now)
	} else if !p.restartSent {
		body := peerwire.PB(peerwire.B(1, peerwire.PB(peerwire.U(1, uint64(i+1)), peerwire.U(2, p.signal.Generation))))
		p.restartSent = s.sendSignal(i, 8, body, now)
	}
}
