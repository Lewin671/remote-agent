package linkcore

import (
	"bytes"
	"errors"
	"io"

	"github.com/Lewin671/remote-agent/shared/peerwire"
)

// ErrSession refuses a session configuration.
var ErrSession = errors.New("direct_session_refused")

// SessionConfig fixes one authenticated connection round.
type SessionConfig struct {
	Self       peerwire.Public // this process: member keys, Instance and Boot
	Peer       peerwire.Public // the peer member; Instance/Boot if already known
	Shared     []byte          // X25519 secret between the two members
	Codec      *peerwire.Codec
	Connection []byte
	Deadline   uint64 // establishment deadline, ms
	// Entropy supplies frame IDs and nonces (peerwire.SealEntropy per frame).
	Entropy io.Reader
	// Allow is the owner's lease and process gate; Policy, if set, the
	// direct-connection policy. Both are rechecked at every step.
	Allow, Policy func() bool
}

const (
	maxSigning  = 256 // frames waiting for a signature
	maxControls = 64  // control frames waiting for the owner
)

// Session is the sans-IO state machine of one direct connection round and
// its two PC uses (1 interactive, 2 bulk): Probe/Challenge bootstrap, each
// PC's offer/answer, candidate batches, end of candidates and Confirm, the
// ACK and receipt caches, the control window, peer-scope Restart/Close, PC
// restarts by generation and the round deadline.
//
// It reads no clock, starts nothing and holds no locks: Handle takes the
// time with each event and returns the effects to perform. Signatures are
// asynchronous: a frame is prepared, its SignFrame goes out, and only the
// signed frame enters a sender, a cache or the wire.
type Session struct {
	cfg SessionConfig
	out []Effect

	retired, established, ended bool
	roundDone                   bool
	retainUntil                 uint64
	boot, controlWindow         *peerwire.Sender
	pcs                         [2]sessionPC
	controlSeq                  uint64
	challenge                   []byte
	challengePending            bool
	receipts                    map[string]*cached
	controlSeen                 map[string]cached
	peerScope                   PeerScope
	nextOp, nextPC              uint64
	signing                     map[uint64]*sealJob
	controls                    map[uint64]peerwire.Opened
}

type cached struct {
	bytes []byte // nil while a receipt waits for its signature
	until uint64
}

// pcOp is the SDK call a PC is waiting on; one at a time per PC.
type pcOp int

const (
	opNone pcOp = iota
	opDescribeOffer
	opDescribeAnswer
	opRemoteOffer
	opRemoteAnswer
	opCandidates
	opEndCandidates
)

type sessionPC struct {
	generation       uint64 // the generation the PC (and its sender) is bound to
	business         bool
	actions          []peerwire.Action
	restartSent      bool
	restartPending   bool
	signal           *peerwire.Signal
	sender           *peerwire.Sender
	seq              uint64
	pc               uint64 // current PC ID, 0 if none
	pcDone           bool   // the current PC has reported closed
	creating         uint64 // PC ID being created
	busy             pcOp
	batch            peerwire.CandidateBatcher
	localUfrag       []byte
	localMid         []byte
	confirmed        bool
	confirming       bool
	announced, ended bool
}

type jobKind int

const (
	jobProbe jobKind = iota
	jobChallenge
	jobSignal  // a PC message for that PC's sender
	jobReceipt // an ACK, Unsupported or control reply: cached and sent once
	jobControl // a frame for the control window
)

// sealJob is a prepared frame waiting for its signature.
type sealJob struct {
	kind     jobKind
	unsigned peerwire.Unsigned
	pc       int
	sender   *peerwire.Sender // jobSignal: the sender of the generation that prepared it
	typ      uint64
	expiry   uint64
	key      string  // jobReceipt: the frame hash it answers
	entry    *cached // jobReceipt: the cache slot reserved for it, nil to overwrite
	until    uint64  // jobReceipt: cache lifetime
	deadline uint64  // jobReceipt: last moment worth transmitting
}

// NewSession starts a round. initiate sends the Probe; the returned effects
// begin with its SignFrame.
func NewSession(cfg SessionConfig, initiate bool, now uint64) (*Session, []Effect, error) {
	if cfg.Codec == nil || cfg.Allow == nil || cfg.Entropy == nil || !cfg.Allow() || len(cfg.Shared) != 32 || len(cfg.Connection) != 16 || len(cfg.Self.Instance) != 16 || len(cfg.Self.Boot) != 32 {
		return nil, nil, ErrSession
	}
	if cfg.Deadline <= now || cfg.Deadline > now+10000 {
		return nil, nil, ErrSession
	}
	s := &Session{cfg: cfg, retainUntil: cfg.Deadline + 300000, boot: peerwire.NewSender(cfg.Deadline), receipts: map[string]*cached{}, controlSeen: map[string]cached{}, signing: map[uint64]*sealJob{}, controls: map[uint64]peerwire.Opened{}}
	for i := range s.pcs {
		g := peerwire.NewSignal(cfg.Codec, cfg.Self, cfg.Peer, cfg.Shared, cfg.Connection, uint64(i+1), now)
		g.RoundDeadline = cfg.Deadline
		g.Current = cfg.Allow
		g.LeaseUntil = ^uint64(0)
		s.pcs[i] = sessionPC{generation: 1, signal: g, sender: peerwire.NewSender(cfg.Deadline), batch: peerwire.CandidateBatcher{Generation: 1, Deadline: cfg.Deadline, Binding: 1}}
	}
	if initiate {
		c := peerwire.NewProbeContext(cfg.Self, cfg.Peer, cfg.Connection, 1, now)
		c.Deadline = cfg.Deadline
		body := peerwire.PB(peerwire.B(1, cfg.Connection), peerwire.B(2, cfg.Self.Instance), peerwire.B(3, cfg.Self.Boot))
		if !s.seal(cfg.Peer, c, 1, body, &sealJob{kind: jobProbe}) {
			return nil, nil, ErrSession
		}
	}
	return s, s.take(), nil
}

// View returns the state an executor mirrors for its other goroutines.
func (s *Session) View() View {
	v := View{Retired: s.retired, Established: s.established, RetainUntil: s.retainUntil}
	for i := range s.pcs {
		p := &s.pcs[i]
		v.Generation[i] = p.generation
		v.Business[i] = p.business
		v.PC[i] = p.pc
		if v.PC[i] == 0 {
			v.PC[i] = p.creating
		}
	}
	return v
}

// Steps names how far each use got: boot, local SDP, remote SDP, DTLS,
// confirm sent, announced; one letter each when reached, a dash when not.
func (s *Session) Steps() [2]string {
	var out [2]string
	for i := range s.pcs {
		p := &s.pcs[i]
		out[i] = steps(p.signal.BootReady, p.signal.LocalSDP, p.signal.RemoteSDP, p.signal.DTLS, p.confirmed, p.announced)
	}
	return out
}

func steps(flags ...bool) string {
	out := make([]byte, len(flags))
	for i, ok := range flags {
		out[i] = '-'
		if ok {
			out[i] = "blrdca"[i]
		}
	}
	return string(out)
}

// Handle applies one event at now (ms) and returns the effects to perform,
// in order. After SessionEnd it returns nothing.
func (s *Session) Handle(e Event, now uint64) []Effect {
	if s.ended {
		return nil
	}
	if !s.cfg.Allow() {
		s.end("not_allowed")
		return s.take()
	}
	if s.retired && now >= s.retainUntil {
		s.end("retired_replay_window_over")
		return s.take()
	}
	switch e := e.(type) {
	case Tick:
		s.tick(now)
	case FrameReceived:
		s.receive(e.Raw, now)
	case DeliveryResult:
		s.delivered(e, now)
	case PeerDiscovered:
		s.boot.Discovered()
		for i := range s.pcs {
			s.pcs[i].sender.Discovered()
		}
	case QueueControl:
		if s.active() && len(s.pcs[0].signal.Peer.Boot) == 32 {
			s.controlUntil(e.Type, e.Body, now, now+10000, &sealJob{kind: jobControl, expiry: now + 10000})
		}
	case RetireRound:
		s.retire()
	case FrameSigned:
		s.signed(e, now)
	case ControlHandled:
		s.controlHandled(e, now)
	case PCCreated:
		s.pcCreated(e, now)
	case PCDescribed:
		s.pcDescribed(e, now)
	case PCRemoteSet:
		s.pcRemoteSet(e, now)
	case PCCandidatesAdded:
		s.pcCandidatesAdded(e, now)
	case PCLocalCandidate:
		s.pcEvent(e.ID, now, e.Valid, func(i int, p *sessionPC) {
			if e.End {
				p.ended = true
			} else if !p.batch.Add(e.Candidate, p.signal.Generation, now, 1) {
				s.emit(ClosePC{p.pc, "candidate_budget"})
			}
		})
	case PCStateChanged:
		switch e.State {
		case PCFailed, PCDisconnected:
			s.pcEvent(e.ID, now, true, func(i int, _ *sessionPC) { s.restartPC(i, now) })
		case PCConnected:
			s.pcEvent(e.ID, now, e.Valid, func(i int, p *sessionPC) {
				p.signal.DTLS = true
				s.maybeReady(i, now)
			})
		}
	case PCClosed:
		for i := range s.pcs {
			if p := &s.pcs[i]; p.pc != 0 && p.pc == e.ID {
				p.pcDone = true
			}
		}
	}
	return s.take()
}

func (s *Session) emit(e Effect) { s.out = append(s.out, e) }
func (s *Session) take() []Effect {
	out := s.out
	s.out = nil
	return out
}
func (s *Session) active() bool { return !s.ended && !s.retired }

func (s *Session) end(reason string) {
	if s.ended {
		return
	}
	s.finishRound()
	s.ended = true
	s.emit(SessionEnd{reason})
}

func (s *Session) finishRound() {
	if !s.roundDone {
		s.roundDone = true
		s.emit(RoundDone{})
	}
}

// retire stops new SDK work for good; replies to the round continue until
// the replay window ends.
func (s *Session) retire() {
	first := !s.retired
	s.retired = true
	s.finishRound()
	for i := range s.pcs {
		p := &s.pcs[i]
		p.signal.Closed = true
		if first && p.pc != 0 {
			s.emit(ClosePC{p.pc, "peer_closed_round"})
		}
	}
}

func (s *Session) tick(now uint64) {
	if now >= s.cfg.Deadline {
		complete, live := false, false
		for i := range s.pcs {
			p := &s.pcs[i]
			if p.announced {
				complete = true
				if p.pc != 0 && !p.pcDone {
					live = true
				} else {
					p.pc = 0
				}
			} else if p.pc != 0 {
				s.emit(ClosePC{p.pc, "round_deadline"})
				p.pc = 0
			}
		}
		s.finishRound()
		if !complete {
			s.end("round_deadline")
			return
		}
		if !live && !s.retired {
			s.end("paths_closed")
			return
		}
	}
	s.flush(now)
}

func (s *Session) pruneControl(now uint64) {
	for key, item := range s.controlSeen {
		if now > item.until {
			delete(s.controlSeen, key)
		}
	}
	for key, item := range s.receipts {
		if now > item.until {
			delete(s.receipts, key)
		}
	}
}

func (s *Session) send(sender *peerwire.Sender, now uint64) {
	for _, raw := range sender.Tick(now) {
		if s.cfg.Allow() {
			s.transmit(raw, now)
		}
	}
}

func (s *Session) flush(now uint64) {
	s.pruneControl(now)
	if s.peerScope.Expired(now) {
		s.retire()
		return
	}
	if s.controlWindow != nil {
		for _, raw := range s.controlWindow.Tick(now) {
			s.transmit(raw, now)
		}
	}
	s.send(s.boot, now)
	for i := range s.pcs {
		p := &s.pcs[i]
		p.signal.Prune(now)
		s.maybeReady(i, now)
		if !p.announced {
			p.signal.Tick(now)
		}
		if p.pc != 0 && !p.announced && p.signal.Closed {
			s.emit(ClosePC{p.pc, "signal_closed"})
			p.pc = 0
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
		if p.ended && p.batch.Ended && len(p.batch.Pending) == 0 && p.pc != 0 {
			p.ended = false
			s.sendSignal(i, 6, peerwire.EndBody(p.localUfrag, uint64(p.batch.Count), uint64(p.batch.Batches)), now)
		}
		s.send(p.sender, now)
	}
}

// seal prepares a frame and asks for its signature; job says where the
// signed frame goes.
func (s *Session) seal(to peerwire.Public, c peerwire.Context, typ uint64, body []byte, job *sealJob) bool {
	if len(s.signing) >= maxSigning {
		return false
	}
	entropy := make([]byte, peerwire.SealEntropy)
	if _, err := io.ReadFull(s.cfg.Entropy, entropy); err != nil {
		return false
	}
	u, err := s.cfg.Codec.Prepare(s.cfg.Self, to, s.cfg.Shared, c, typ, body, entropy)
	if err != nil {
		return false
	}
	s.nextOp++
	job.unsigned = u
	s.signing[s.nextOp] = job
	s.emit(SignFrame{s.nextOp, u.Message})
	return true
}

func (s *Session) openSent(raw []byte, now uint64) (peerwire.Opened, error) {
	return s.cfg.Codec.OpenSent(raw, s.cfg.Self, s.pcs[0].signal.Peer, s.cfg.Shared, now)
}

// signed completes a prepared frame and puts it where it was meant to go.
func (s *Session) signed(e FrameSigned, now uint64) {
	job := s.signing[e.Op]
	if job == nil {
		return
	}
	delete(s.signing, e.Op)
	raw, err := job.unsigned.Finish(e.Signature)
	switch job.kind {
	case jobProbe:
		if err == nil {
			var sent peerwire.Opened
			if sent, err = s.cfg.Codec.OpenSent(raw, s.cfg.Self, s.cfg.Peer, s.cfg.Shared, now); err == nil {
				for i := range s.pcs {
					if err = s.pcs[i].signal.RememberProbe(sent); err != nil {
						break
					}
				}
			}
		}
		if err != nil || !s.boot.Enqueue(raw, now, s.cfg.Deadline, 1) {
			s.end("probe_failed")
			return
		}
		s.send(s.boot, now)
	case jobChallenge:
		s.challengePending = false
		if err != nil {
			return
		}
		s.challenge = raw
		sent, err := s.openSent(raw, now)
		if err != nil {
			return
		}
		for i := range s.pcs {
			if err = s.pcs[i].signal.RememberChallenge(sent); err != nil {
				return
			}
		}
		s.boot.Enqueue(raw, now, job.expiry, 1)
		s.send(s.boot, now)
	case jobSignal:
		p := &s.pcs[job.pc]
		if p.sender != job.sender {
			return // prepared for a generation since reset
		}
		ok := err == nil && p.sender.Enqueue(raw, now, job.expiry, 1)
		switch job.typ {
		case 7:
			p.confirming = false
			p.confirmed = ok
		case 8:
			p.restartPending = false
			p.restartSent = ok
		}
		if !ok {
			return
		}
		if job.typ == 7 {
			s.maybeReady(job.pc, now)
		}
		s.send(p.sender, now)
	case jobReceipt:
		if job.entry != nil {
			if s.receipts[job.key] == job.entry {
				if err != nil {
					delete(s.receipts, job.key)
				} else {
					job.entry.bytes = raw
				}
			}
		} else if err == nil {
			s.receipts[job.key] = &cached{bytes: raw, until: job.until}
		}
		if err == nil && s.cfg.Allow() && now < job.deadline {
			s.transmit(raw, now)
		}
	case jobControl:
		if err != nil || !s.active() {
			return
		}
		if s.controlWindow == nil || s.controlWindow.TimedOut || len(s.controlWindow.Pending) == 0 {
			s.controlWindow = peerwire.NewSender(now + 10000)
		}
		s.controlWindow.Enqueue(raw, now, job.expiry, 1)
		for _, raw := range s.controlWindow.Tick(now) {
			s.transmit(raw, now)
		}
	}
}

func (s *Session) sendSignal(i int, typ uint64, body []byte, now uint64) bool {
	p := &s.pcs[i]
	p.seq++
	c := peerwire.NewContext(s.cfg.Self, p.signal.Peer, s.cfg.Connection, uint64(i+1), p.signal.Generation, p.seq, now, 1, false)
	c.Deadline = s.cfg.Deadline
	return s.seal(p.signal.Peer, c, typ, body, &sealJob{kind: jobSignal, pc: i, sender: p.sender, typ: typ, expiry: c.Deadline})
}

// controlUntil prepares a control-class frame expiring by deadline.
func (s *Session) controlUntil(typ uint64, body []byte, now, deadline uint64, job *sealJob) bool {
	s.controlSeq++
	peer := s.pcs[0].signal.Peer
	c := peerwire.NewContext(s.cfg.Self, peer, s.cfg.Connection, 0, 1, s.controlSeq, now, 2, false)
	c.Deadline = min(c.Deadline, deadline)
	job.deadline = c.Deadline
	return s.seal(peer, c, typ, body, job)
}

// reply prepares a receipt for the frame with hash, replacing any cached one.
func (s *Session) reply(typ uint64, body []byte, hash []byte, frameDeadline, now uint64) {
	s.controlUntil(typ, body, now, frameDeadline, &sealJob{kind: jobReceipt, key: string(hash), until: frameDeadline + 300000})
}

func (s *Session) acknowledge(o peerwire.Opened, now uint64) {
	if o.Context.Class == 1 && o.Type == 1 {
		return
	} // Challenge proves Probe receipt.
	if o.Context.Class == 2 && (o.Type == 4 || o.Type == 10 || o.Type == 11 || o.Type == 12 || o.Type == 13) {
		return
	}
	key := string(o.Hash)
	entry, ok := s.receipts[key]
	if !ok {
		if len(s.receipts) >= 2048 {
			s.pruneControl(now)
		}
		if len(s.receipts) >= 2048 {
			return
		}
		entry = &cached{until: o.Context.Deadline + 300000}
		body := peerwire.PB(peerwire.U(4, o.Context.Class), peerwire.U(5, o.Context.Seq), peerwire.B(6, o.Hash), peerwire.U(7, o.Context.Generation), peerwire.U(8, o.Context.Use))
		if s.controlUntil(4, body, now, o.Context.Deadline, &sealJob{kind: jobReceipt, key: key, entry: entry}) {
			s.receipts[key] = entry
		}
		return // sent once signed
	}
	if entry.bytes != nil && s.cfg.Allow() && now < o.Context.Deadline {
		s.transmit(entry.bytes, now)
	}
}

// resend transmits the cached receipt for hash, if it is signed and current.
func (s *Session) resend(hash []byte, deadline, now uint64) {
	if r, ok := s.receipts[string(hash)]; ok && r.bytes != nil && now < deadline && s.cfg.Allow() {
		s.transmit(r.bytes, now)
	}
}

func (s *Session) transmit(raw []byte, now uint64) {
	original, err := s.openSent(raw, now)
	if err != nil || !s.cfg.Allow() {
		return
	}
	c := original.Context
	s.emit(TransmitFrame{Raw: raw, Deadline: c.Deadline, Critical: c.Class == 2 || original.Type != 5 && original.Type != 6, Class: c.Class, Type: original.Type, Use: c.Use, Generation: c.Generation})
}

func (s *Session) senders() []*peerwire.Sender {
	out := []*peerwire.Sender{s.boot, s.pcs[0].sender, s.pcs[1].sender}
	if s.controlWindow != nil {
		out = append(out, s.controlWindow)
	}
	return out
}

func (s *Session) delivered(r DeliveryResult, now uint64) {
	for _, sender := range s.senders() {
		for _, pending := range sender.Pending {
			fields, err := peerwire.Parse(pending.Raw, 5)
			if err != nil || !bytes.Equal(fields[4].B, r.ID) {
				continue
			}
			if r.Status == 3 {
				sender.Limited(pending.Hash, now, r.Retry)
			}
			if r.Status == 2 {
				sender.Offline()
			}
		}
	}
}

func (s *Session) receive(raw []byte, now uint64) {
	if !s.cfg.Allow() {
		return
	}
	peer := s.pcs[0].signal.Peer
	o, err := s.cfg.Codec.Open(raw, peer, s.cfg.Self, s.cfg.Shared, now)
	if err != nil || !bytes.Equal(o.Context.Connection, s.cfg.Connection) {
		return
	}
	s.emit(FrameObserved{o.Type, o.Context.Use})
	if len(s.pcs[0].signal.Seen)+len(s.pcs[1].signal.Seen)+len(s.controlSeen) >= 2047 {
		s.pruneControl(now)
	}
	if len(s.pcs[0].signal.Seen)+len(s.pcs[1].signal.Seen)+len(s.controlSeen) >= 2047 {
		key := peerwire.Key(o.Context)
		known := bytes.Equal(s.controlSeen[key].bytes, o.Hash)
		for i := range s.pcs {
			known = known || bytes.Equal(s.pcs[i].signal.Seen[key], o.Hash)
		}
		if !known {
			return
		}
	}
	c := o.Context
	if s.retired {
		key := peerwire.Key(c)
		if bytes.Equal(s.controlSeen[key].bytes, o.Hash) {
			s.resend(o.Hash, c.Deadline, now)
			return
		}
		for i := range s.pcs {
			if s.pcs[i].signal.ReplyAllowed(raw, now) {
				if c.Class == 1 && o.Type == 1 {
					if s.challenge != nil {
						s.transmit(s.challenge, now)
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
		s.receiveACK(raw, o, now)
		return
	}
	if c.Class == 1 && (o.Type == 1 || o.Type == 2) {
		s.receiveBootstrap(raw, o, now)
		return
	}
	if c.Class == 2 {
		s.receiveControl(o, now)
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
	s.retainUntil = max(s.retainUntil, c.Deadline+300000)
	if p.signal.ReplyAllowed(raw, now) {
		s.acknowledge(o, now)
	}
	for _, a := range actions {
		if a.Class == 2 && a.Type == 10 {
			if original, err := s.cfg.Codec.Open(a.Original, p.signal.Peer, s.cfg.Self, s.cfg.Shared, now); err == nil {
				s.reply(10, a.Payload, original.Hash, original.Context.Deadline, now)
			}
		} else if a.Class == 1 {
			if len(p.actions) >= 32 {
				s.retire()
				return
			}
			p.actions = append(p.actions, a)
		}
	}
	s.drainActions(i, now)
	s.maybeReady(i, now)
}

// receiveACK settles only a recorded immutable sender packet.
func (s *Session) receiveACK(raw []byte, o peerwire.Opened, now uint64) {
	if _, err := peerwire.KnownPayload(2, 4, o.Payload); err != nil {
		return
	}
	f, err := peerwire.ParseMessage(o.Payload, "Ack")
	if err != nil {
		return
	}
	hash := f[6].B
	matched, bootstrap := false, false
	for _, sender := range s.senders() {
		if pending := sender.Pending[string(hash)]; pending != nil {
			original, err := s.openSent(pending.Raw, now)
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
	s.retainUntil = max(s.retainUntil, o.Context.Deadline+300000)
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
}

// receiveBootstrap handles Probe (answered by one Challenge, prepared once)
// and Challenge (which proves this side's Probe and opens offers).
func (s *Session) receiveBootstrap(raw []byte, o peerwire.Opened, now uint64) {
	for i := range s.pcs {
		if _, err := s.pcs[i].signal.Receive(raw, now); err != nil {
			return
		}
	}
	s.retainUntil = max(s.retainUntil, o.Context.Deadline+300000)
	if o.Type == 2 {
		if probe := s.pcs[0].signal.Probe; probe != nil {
			s.boot.Ack(probe.Hash)
		}
		s.acknowledge(o, now)
		for i := range s.pcs {
			s.maybeOffer(i, now)
		}
		return
	}
	if s.challenge == nil && !s.challengePending {
		peer := s.pcs[0].signal.Peer
		c := peerwire.NewContext(s.cfg.Self, peer, s.cfg.Connection, 0, 0, 1, now, 1, true)
		c.Deadline = s.cfg.Deadline
		body := peerwire.PB(peerwire.B(1, s.cfg.Connection), peerwire.B(2, s.cfg.Self.Instance), peerwire.B(3, s.cfg.Self.Boot), peerwire.B(4, peer.Boot), peerwire.B(6, o.Hash))
		s.challengePending = s.seal(peer, c, 2, body, &sealJob{kind: jobChallenge, expiry: c.Deadline})
	} else if s.challenge != nil && s.pcs[0].signal.ReplyAllowed(raw, now) {
		s.transmit(s.challenge, now)
	}
}

// receiveControl records a control frame once and asks the owner to handle it.
func (s *Session) receiveControl(o peerwire.Opened, now uint64) {
	c := o.Context
	if _, err := peerwire.KnownPayload(c.Class, o.Type, o.Payload); err != nil {
		return
	}
	key := peerwire.Key(c)
	if old, ok := s.controlSeen[key]; ok {
		if bytes.Equal(old.bytes, o.Hash) {
			s.resend(o.Hash, c.Deadline, now)
		}
		return
	}
	if o.Type != 10 && len(s.controls) >= maxControls {
		return // the peer retransmits
	}
	s.controlSeen[key] = cached{bytes: append([]byte(nil), o.Hash...), until: c.Deadline + 300000}
	s.retainUntil = max(s.retainUntil, c.Deadline+300000)
	if o.Type == 10 {
		return
	}
	s.nextOp++
	s.controls[s.nextOp] = o
	s.emit(HandleControl{s.nextOp, o, now})
}

func (s *Session) controlHandled(e ControlHandled, now uint64) {
	o, ok := s.controls[e.Op]
	if !ok {
		return
	}
	delete(s.controls, e.Op)
	c := o.Context
	typ, body := e.Type, e.Body
	if typ == 0 {
		typ = 10
		body = peerwire.PB(peerwire.U(1, c.Class), peerwire.U(2, o.Type), peerwire.U(3, c.Seq), peerwire.B(4, o.Hash))
	}
	if !s.cfg.Allow() || now >= c.Deadline {
		return
	}
	s.reply(typ, body, o.Hash, c.Deadline, now)
}

// Peer-scope Restart/Close use an independent ordered window. All target
// generations are checked before any SDK object is retired.
func (s *Session) receivePeerScope(o peerwire.Opened, now uint64) {
	if now >= s.cfg.Deadline || o.Context.Use != 0 || o.Context.Class != 1 {
		return
	}
	if _, err := peerwire.KnownPayload(1, o.Type, o.Payload); err != nil {
		return
	}
	key := peerwire.Key(o.Context)
	if old, ok := s.controlSeen[key]; ok {
		if bytes.Equal(old.bytes, o.Hash) {
			s.resend(o.Hash, o.Context.Deadline, now)
		}
		return
	}
	if !s.validPeerScope(o) {
		return
	}
	admit := s.peerScope.Admit(o, now, s.cfg.Deadline)
	if admit == ScopeDrop {
		return
	}
	s.controlSeen[key] = cached{bytes: append([]byte(nil), o.Hash...), until: o.Context.Deadline + 300000}
	if admit == ScopeBuffered {
		return
	}
	generation := o.Context.Generation
	for {
		fresh, err := s.cfg.Codec.Open(o.Raw, s.pcs[0].signal.Peer, s.cfg.Self, s.cfg.Shared, now)
		if err != nil || !s.validPeerScope(fresh) {
			s.retire()
			return
		}
		s.acknowledge(fresh, now)
		if fresh.Type == 9 {
			s.retire()
			return
		}
		targets, _ := peerwire.ReadRestart(fresh.Payload)
		for use := range targets {
			s.restartPC(int(use-1), now)
		}
		next, ok := s.peerScope.Advance(generation)
		if !ok {
			break
		}
		o = next
	}
	s.peerScope.Settle(generation, s.cfg.Deadline)
}

func (s *Session) validPeerScope(o peerwire.Opened) bool {
	return ValidPeerScope(o, s.pcs[0].signal.Offerer(), [2]uint64{s.pcs[0].signal.Generation, s.pcs[1].signal.Generation})
}

// drainActions runs accepted PC messages in order, one SDK call at a time;
// the call's result event resumes it.
func (s *Session) drainActions(i int, now uint64) {
	p := &s.pcs[i]
	for len(p.actions) > 0 && s.active() && p.busy == opNone {
		a := p.actions[0]
		if a.Generation != p.signal.Generation {
			p.actions = p.actions[1:]
			continue
		}
		if a.Type == 3 {
			s.emit(BeforeOffer{})
		}
		if a.Type >= 3 && a.Type <= 6 && !s.makePC(i, now) {
			return
		}
		p.actions = p.actions[1:]
		switch a.Type {
		case 3, 4:
			if !p.signal.Dispatch(a.Original, now) {
				return
			}
			p.busy = opRemoteAnswer
			if a.Type == 3 {
				p.busy = opRemoteOffer
			}
			s.emit(SetRemotePC{p.pc, a.Payload, a.Type == 3})
			return
		case 5:
			if p.pc == 0 || !p.signal.Dispatch(a.Original, now) {
				return
			}
			cs, _, err := peerwire.ReadCandidates(a.Payload)
			if err != nil {
				return
			}
			p.busy = opCandidates
			s.emit(AddCandidatesPC{ID: p.pc, Candidates: cs})
			return
		case 6:
			if p.pc == 0 || !p.signal.Dispatch(a.Original, now) {
				return
			}
			p.busy = opEndCandidates
			s.emit(AddCandidatesPC{ID: p.pc, End: true})
			return
		case 8:
			s.restartPC(i, now)
		case 9:
			if p.pc != 0 {
				s.emit(ClosePC{p.pc, "signal_close_action"})
			}
		}
	}
}

// makePC reports whether the PC of the current generation exists, asking
// for it if it may be created.
func (s *Session) makePC(i int, now uint64) bool {
	p := &s.pcs[i]
	if p.generation != p.signal.Generation {
		s.resetPC(i)
	}
	if p.pc != 0 {
		return true
	}
	if p.creating != 0 || s.retired || now >= s.cfg.Deadline || !s.cfg.Allow() || s.cfg.Policy != nil && !s.cfg.Policy() {
		return false
	}
	s.nextPC++
	p.creating = s.nextPC
	s.emit(CreatePC{s.nextPC, uint64(i + 1), p.signal.Generation})
	return false
}

func (s *Session) pcCreated(e PCCreated, now uint64) {
	for i := range s.pcs {
		p := &s.pcs[i]
		if p.creating == 0 || p.creating != e.ID {
			continue
		}
		p.creating = 0
		if !e.OK {
			return
		}
		if s.retired {
			s.emit(ClosePC{e.ID, "peer_closed_round"})
			return
		}
		p.pc, p.pcDone = e.ID, false
		s.emit(PCChosen{})
		s.maybeOffer(i, now)
		s.drainActions(i, now)
		return
	}
	if e.OK {
		s.emit(ClosePC{e.ID, "generation_reset"}) // created for a generation since reset
	}
}

// current returns the PC whose result this is, if it is still waiting on op.
func (s *Session) current(id uint64, ops ...pcOp) (int, *sessionPC) {
	for i := range s.pcs {
		p := &s.pcs[i]
		if p.pc != 0 && p.pc == id {
			for _, op := range ops {
				if p.busy == op {
					return i, p
				}
			}
		}
	}
	return -1, nil
}

func (s *Session) pcDescribed(e PCDescribed, now uint64) {
	i, p := s.current(e.ID, opDescribeOffer, opDescribeAnswer)
	if p == nil {
		return
	}
	offer := p.busy == opDescribeOffer
	p.busy = opNone
	if !e.OK || p.signal.SetLocal(e.SDP, offer, p.signal.Generation) != nil {
		return
	}
	info, _ := peerwire.InspectSDP(e.SDP)
	p.localUfrag, p.localMid = info.Ufrag, info.Mid
	typ := uint64(4)
	if offer {
		typ = 3
	}
	s.sendSignal(i, typ, peerwire.SDPBody(e.SDP), now)
	s.drainActions(i, now)
}

func (s *Session) pcRemoteSet(e PCRemoteSet, now uint64) {
	i, p := s.current(e.ID, opRemoteOffer, opRemoteAnswer)
	if p == nil {
		return
	}
	offer := p.busy == opRemoteOffer
	p.busy = opNone
	if !e.OK {
		s.emit(ClosePC{p.pc, "remote_description_rejected"})
		return
	}
	if offer {
		p.busy = opDescribeAnswer
		s.emit(DescribePC{p.pc, false})
		return
	}
	s.drainActions(i, now)
}

func (s *Session) pcCandidatesAdded(e PCCandidatesAdded, now uint64) {
	i, p := s.current(e.ID, opCandidates, opEndCandidates)
	if p == nil {
		return
	}
	end := p.busy == opEndCandidates
	p.busy = opNone
	if e.OK || end {
		s.drainActions(i, now)
	}
}

// pcEvent runs f for an SDK callback of the current PC of the current
// generation; valid is the PC's own admission.
func (s *Session) pcEvent(id, now uint64, valid bool, f func(int, *sessionPC)) {
	for i := range s.pcs {
		p := &s.pcs[i]
		if p.pc == 0 || p.pc != id {
			continue
		}
		if !s.active() || p.generation != p.signal.Generation || !valid {
			return
		}
		f(i, p)
		return
	}
}

func (s *Session) maybeOffer(i int, now uint64) {
	p := &s.pcs[i]
	if p.busy != opNone || !p.signal.BootReady || !p.signal.Offerer() || p.signal.LocalSDP || p.signal.Closed || !s.active() || now >= s.cfg.Deadline || !s.cfg.Allow() || (s.cfg.Policy != nil && !s.cfg.Policy()) {
		return
	}
	if p.pc == 0 && p.creating == 0 {
		s.emit(BeforeOffer{})
	}
	if !s.makePC(i, now) {
		return
	}
	p.busy = opDescribeOffer
	s.emit(DescribePC{p.pc, true})
}

func (s *Session) maybeReady(i int, now uint64) {
	p := &s.pcs[i]
	if p.pc == 0 {
		return
	}
	if !p.confirmed && !p.confirming && p.signal.LocalSDP && p.signal.RemoteSDP && p.signal.DTLS {
		p.confirming = s.sendSignal(i, 7, p.signal.ConfirmBody(), now)
	}
	// A connected DTLS transport and the peer's confirmation are the proof that
	// the pair works; a server-reflexive candidate is not required. Requiring
	// one would keep a computer with a public address and no NAT, or any pair
	// whose STUN is unreachable, on the relay although the path is up.
	if p.confirmed && !p.announced && p.signal.Ready() {
		p.announced = true
		p.business = true
		s.established = true
		s.emit(ConfirmPC{p.pc, uint64(i + 1)})
		if s.pcs[0].announced && s.pcs[1].announced {
			s.finishRound()
		}
	}
}

// Replacement SDK objects are reserved only after the old PC actually closes.
// Pending actions are retried; neither a gap nor restart resets the round.
func (s *Session) resetPC(i int) {
	p := &s.pcs[i]
	if p.pc != 0 {
		s.emit(ClosePC{p.pc, "generation_reset"})
	}
	p.pc, p.pcDone, p.creating, p.busy = 0, false, 0, opNone
	p.generation = p.signal.Generation
	p.seq = 0
	p.sender = peerwire.NewSender(s.cfg.Deadline)
	p.batch = peerwire.CandidateBatcher{Generation: p.signal.Generation, Deadline: s.cfg.Deadline, Binding: 1}
	p.business = false
	p.confirmed, p.confirming = false, false
	p.announced = false
	p.ended = false
	p.restartSent, p.restartPending = false, false
	p.localUfrag = nil
	p.localMid = nil
}

func (s *Session) restartPC(i int, now uint64) {
	p := &s.pcs[i]
	if now >= s.cfg.Deadline || !s.active() {
		return
	}
	if p.signal.Offerer() {
		if p.signal.NextGeneration(p.signal.Generation+1) != nil {
			return
		}
		s.resetPC(i)
		p.actions = nil
		s.maybeOffer(i, now)
	} else if !p.restartSent && !p.restartPending {
		body := peerwire.PB(peerwire.B(1, peerwire.PB(peerwire.U(1, uint64(i+1)), peerwire.U(2, p.signal.Generation))))
		p.restartPending = s.sendSignal(i, 8, body, now)
	}
}
