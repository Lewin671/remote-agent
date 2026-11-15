package labwire

import (
	"bytes"
	"errors"
	"fmt"
)

// Signal is a transport-independent receiver. SDK methods are invoked by its
// adapter only for returned actions; authentication and sequence decisions
// happen here first. All callbacks must retain the returned generation token.
type Action struct {
	Class, Type uint64
	Payload     []byte
	Generation  uint64
}
type Signal struct {
	LeaseUntil, AuthorizationRevision                                                  uint64
	MembersAuthorized                                                                  bool
	Self                                                                               Identity
	Peer                                                                               Public
	Connection                                                                         []byte
	Use, Generation, Next                                                              uint64
	BootReady, LocalSDP, RemoteSDP, DTLS, Confirmed, Closed, Retired, Ended            bool
	Challenge, Probe, ReceivedProbe                                                    *Opened
	Clock, RoundDeadline                                                               uint64
	Allowed, Dispatched                                                                map[string][]byte
	Dispatches                                                                         int
	LocalHash, RemoteHash, RemoteUfrag, RemoteMid, LocalFingerprint, RemoteFingerprint []byte
	Candidates, Batches                                                                int
	Seen                                                                               map[string][]byte
	Pending                                                                            map[uint64]Opened
	PendingAt                                                                          map[uint64]uint64
	GapDeadline                                                                        uint64
	CacheUntil                                                                         map[string]uint64
	SDKCalls, Duplicates, Conflicts, Rejected, Unsupported, StaleChallenges            int
}

var ErrSignal = errors.New("invalid signal transition")

func NewSignal(self Identity, peer Public, connection []byte, use uint64, now uint64) *Signal {
	return &Signal{LeaseUntil: now + 10000, AuthorizationRevision: 1, MembersAuthorized: true, Self: self, Peer: peer.Member(), Clock: now, RoundDeadline: now + 10000, Allowed: map[string][]byte{}, Dispatched: map[string][]byte{}, Connection: connection, Use: use, Generation: 1, Next: 1, Seen: map[string][]byte{}, Pending: map[uint64]Opened{}, PendingAt: map[uint64]uint64{}, CacheUntil: map[string]uint64{}}
}
func (s *Signal) Offerer() bool { return bytes.Compare(s.Self.ID, s.Peer.ID) < 0 }
func (s *Signal) Ready() bool   { return !s.Closed && s.LocalSDP && s.RemoteSDP && s.DTLS && s.Confirmed }
func (s *Signal) SetLocal(raw []byte, offer bool, generation uint64) error {
	if !s.authorized() || s.Clock >= s.RoundDeadline || generation != s.Generation || s.Closed || !s.BootReady || offer != s.Offerer() || (offer && s.LocalSDP) || (!offer && !s.RemoteSDP) {
		return ErrSignal
	}
	info, e := InspectSDP(raw)
	if e != nil {
		return e
	}
	s.LocalSDP = true
	s.LocalHash = Hash(raw)
	s.LocalFingerprint = info.Fingerprint
	return nil
}
func (s *Signal) NextGeneration(gen uint64) error {
	if s.Clock >= s.RoundDeadline || s.Closed && !s.Retired || gen != s.Generation+1 || gen == 0 {
		return ErrSignal
	}
	s.Generation = gen
	s.Next = 1
	s.Closed = false
	s.Retired = false
	s.LocalSDP = false
	s.RemoteSDP = false
	s.DTLS = false
	s.Confirmed = false
	s.Pending = map[uint64]Opened{}
	s.Allowed = map[string][]byte{}
	s.Dispatched = map[string][]byte{}
	s.PendingAt = map[uint64]uint64{}
	s.GapDeadline = 0
	s.Candidates = 0
	s.Batches = 0
	s.Ended = false
	return nil
}
func (s *Signal) Receive(packet []byte, now uint64) ([]Action, error) {
	s.Tick(now)
	opened, e := Open(packet, s.Peer, s.Self, s.Clock)
	if e != nil {
		s.Rejected++
		return nil, e
	}
	return s.Accept(opened, now)
}
func (s *Signal) Accept(o Opened, now uint64) ([]Action, error) {
	s.Tick(now)
	fresh, e := Open(o.Raw, s.Peer, s.Self, s.Clock)
	if e != nil {
		return nil, e
	}
	o = fresh
	c := o.Context
	if !s.authorized() || s.Clock >= s.RoundDeadline {
		return nil, ErrSignal
	}
	if !bytes.Equal(c.Connection, s.Connection) || !bytes.Equal(c.From, s.Peer.ID) {
		s.Rejected++
		return nil, ErrSignal
	}
	if e := s.preflight(&o); e != nil {
		return nil, e
	}
	key := Key(c)
	if old, ok := s.Seen[key]; ok {
		if !bytes.Equal(old, o.Hash) {
			s.Conflicts++
			return nil, ErrSignal
		}
		s.Duplicates++
		return nil, nil
	}
	if s.Closed && !s.Retired {
		return nil, ErrSignal
	}
	if len(s.Seen) >= 2048 {
		s.Rejected++
		return nil, ErrSignal
	}
	if c.Class == 2 {
		if o.Type == 4 {
			if e := s.acceptACK(o); e != nil {
				return nil, e
			}
		}

		// ACK/Unsupported are idempotent controls, never ACKed back.
		if o.Type != 4 && o.Type != 10 {
			s.Unsupported++
			s.Seen[key] = o.Hash
			s.CacheUntil[key] = c.Deadline + 300000
			return []Action{{2, 10, PB(U(1, c.Class), U(2, o.Type), U(3, c.Seq), B(4, o.Hash)), c.Generation}}, nil
		}
		s.Seen[key] = o.Hash
		s.CacheUntil[key] = c.Deadline + 300000
		return []Action{{2, o.Type, o.Payload, c.Generation}}, nil
	}
	if o.Type == 1 || o.Type == 2 {
		if string(c.Domain) != "bootstrap" || c.Generation != 0 || c.Use != 0 {
			s.Rejected++
			return nil, ErrSignal
		}
		f, e := ParseMessage(o.Payload, signalSchemas[o.Type])
		if e != nil {
			return nil, e
		}
		if o.Type == 1 {
			if need(f, "bbb") != nil || !bytes.Equal(f[1].B, c.Connection) || !bytes.Equal(f[2].B, c.SenderInstance) || !bytes.Equal(f[3].B, c.SenderBoot) {
				return nil, ErrSignal
			}
		} else {
			if s.Probe != nil {
				if _, err := OpenSent(s.Probe.Raw, s.Self, s.Peer, s.Clock); err != nil {
					return nil, err
				}
			}
			if need(f, "bbbbub") != nil || f[5].U > 1 || len(f[6].B) != 32 || s.Probe == nil || s.Clock >= s.Probe.Context.Deadline || !bytes.Equal(f[6].B, s.Probe.Hash) || !bytes.Equal(f[1].B, c.Connection) || !bytes.Equal(f[2].B, c.SenderInstance) || !bytes.Equal(f[3].B, c.SenderBoot) || !bytes.Equal(f[4].B, s.Self.Boot) {
				return nil, ErrSignal
			}
		}
		if len(s.Peer.Boot) > 0 && (!bytes.Equal(s.Peer.Boot, c.SenderBoot) || !bytes.Equal(s.Peer.Instance, c.SenderInstance)) {
			s.retire(false)
			return nil, ErrSignal
		}
		if o.Type == 1 {
			s.ReceivedProbe = &o
		}
		s.Peer.Instance = append([]byte(nil), c.SenderInstance...)
		s.Peer.Boot = append([]byte(nil), c.SenderBoot...)
		s.Seen[key] = o.Hash
		s.CacheUntil[key] = c.Deadline + 300000
		if o.Type == 2 {
			s.BootReady = true
			if f[5].U == 1 {
				s.StaleChallenges++
			}
		}

		return []Action{{1, o.Type, o.Payload, 0}}, nil
	}
	// Receive has authenticated both current instances/nonces before Accept.
	// An Offer can overtake Control: its nonce proof is bootstrap admission,
	// not a substitute for a hash-bound transport sequence ACK.
	if !s.BootReady && !s.Closed && !s.Offerer() && o.Type == 3 && c.Class == 1 && string(c.Domain) == "signal" && c.Use == s.Use && c.Generation == s.Generation && c.Seq == 1 && bytes.Equal(c.ReceiverBoot, s.Self.Boot) && bytes.Equal(c.SenderBoot, s.Peer.Boot) && bytes.Equal(c.ReceiverInstance, s.Self.Instance) && bytes.Equal(c.SenderInstance, s.Peer.Instance) {
		if _, _, e := ReadSDPBody(o.Payload); e != nil {
			return nil, e
		}
		s.BootReady = true
	}
	if (s.Closed && !(s.Retired && c.Generation == s.Generation+1)) || !s.BootReady || c.Use != s.Use || c.Generation < s.Generation || c.Generation > s.Generation+1 {
		s.Rejected++
		return nil, ErrSignal
	}
	if c.Generation == s.Generation+1 {
		if s.Offerer() {
			s.Rejected++
			return nil, ErrSignal
		}
		if e := s.NextGeneration(c.Generation); e != nil {
			return nil, e
		}
	}
	if c.Seq < s.Next {
		s.Rejected++
		return nil, ErrSignal
	}
	if c.Seq > s.Next {
		if len(s.Pending) >= 32 {
			s.Rejected++
			return nil, ErrSignal
		}
		s.Seen[key] = o.Hash
		s.CacheUntil[key] = c.Deadline + 300000
		s.Pending[c.Seq] = o
		s.PendingAt[c.Seq] = now
		s.refreshGap()
		return nil, nil
	}
	var actions []Action
	for {
		live, e := Open(o.Raw, s.Peer, s.Self, s.Clock)
		if e != nil || live.Context.Generation != s.Generation || live.Context.Use != s.Use || !bytes.Equal(live.Context.Connection, s.Connection) {
			s.retire(true)
			return nil, ErrSignal
		}
		o = live
		key = Key(o.Context)
		if e := s.preflight(&o); e != nil {
			return nil, e
		}
		action, e := s.apply(o)
		if e != nil {
			s.Rejected++
			return actions, e
		}
		key = Key(o.Context)
		s.Seen[key] = o.Hash
		s.CacheUntil[key] = o.Context.Deadline + 300000
		s.Next++
		if action.Type != 0 {
			actions = append(actions, action)
			if action.Class == 1 && action.Type >= 3 && action.Type <= 6 {
				s.Allowed[key] = append([]byte(nil), o.Hash...)
			}
		}
		next, ok := s.Pending[s.Next]
		if !ok {
			break
		}
		delete(s.Pending, s.Next)
		delete(s.PendingAt, s.Next)
		o = next
	}
	s.refreshGap()
	return actions, nil
}
func (s *Signal) apply(o Opened) (Action, error) {
	action := Action{1, o.Type, o.Payload, o.Context.Generation}
	switch o.Type {
	case 3: // Offer: only the smaller device may originate it.
		if s.Offerer() || s.RemoteSDP {
			return Action{}, ErrSignal
		}
		raw, info, e := ReadSDPBody(o.Payload)
		if e != nil {
			return Action{}, e
		}
		s.RemoteSDP = true
		s.RemoteHash = Hash(raw)
		s.RemoteFingerprint = info.Fingerprint
		s.RemoteUfrag = info.Ufrag
		s.RemoteMid = info.Mid
		s.SDKCalls++
		action.Payload = raw
	case 4:
		if !s.Offerer() || !s.LocalSDP || s.RemoteSDP {
			return Action{}, ErrSignal
		}
		raw, info, e := ReadSDPBody(o.Payload)
		if e != nil {
			return Action{}, e
		}
		s.RemoteSDP = true
		s.RemoteHash = Hash(raw)
		s.RemoteFingerprint = info.Fingerprint
		s.RemoteUfrag = info.Ufrag
		s.RemoteMid = info.Mid
		s.SDKCalls++
		action.Payload = raw
	case 5:
		if !s.RemoteSDP || s.Ended {
			return Action{}, ErrSignal
		}
		candidates, index, e := ReadCandidates(o.Payload)
		if e != nil || index != uint64(s.Batches) || len(candidates) == 0 || len(candidates) > 16 || s.Batches == 4 || s.Candidates+len(candidates) > 64 {
			return Action{}, ErrSignal
		}
		for _, candidate := range candidates {
			if candidate.Generation != s.Generation || !bytes.Equal(candidate.Ufrag, s.RemoteUfrag) || !bytes.Equal(candidate.Mid, s.RemoteMid) || candidate.Index != 0 {
				return Action{}, ErrSignal
			}
		}
		s.Candidates += len(candidates)
		s.Batches++
		s.SDKCalls += len(candidates)
	case 6:
		if !s.RemoteSDP || s.Ended {
			return Action{}, ErrSignal
		}
		f, e := ParseMessage(o.Payload, "EndCandidates")
		if e != nil || need(f, "buu") != nil || !bytes.Equal(f[1].B, s.RemoteUfrag) || f[2].U != uint64(s.Candidates) || f[3].U != uint64(s.Batches) {
			return Action{}, ErrSignal
		}
		s.SDKCalls++
		s.Ended = true
	case 7:
		if !s.LocalSDP || !s.RemoteSDP {
			return Action{}, ErrSignal
		}
		expected := s.ConfirmBody()
		if !bytes.Equal(o.Payload, expected) {
			return Action{}, ErrSignal
		}
		s.Confirmed = true
	case 8:
		if !s.Offerer() {
			return Action{}, ErrSignal
		}
	case 9:
		s.Closed = true
		s.Pending = map[uint64]Opened{}
		s.Allowed = map[string][]byte{}
		s.Dispatched = map[string][]byte{}
		s.PendingAt = map[uint64]uint64{}
		s.GapDeadline = 0
	default:
		s.Unsupported++
		action = Action{2, 10, PB(U(1, o.Context.Class), U(2, o.Type), U(3, o.Context.Seq), B(4, o.Hash)), o.Context.Generation}
	}
	return action, nil
}
func (s *Signal) Tick(now uint64) bool {
	s.Clock = max(s.Clock, now)
	now = s.Clock
	if now >= s.RoundDeadline {
		s.retire(false)
		return true
	}
	for key, until := range s.CacheUntil {
		if now > until {
			delete(s.Seen, key)
			delete(s.CacheUntil, key)
		}
	}
	if s.GapDeadline != 0 && now >= s.GapDeadline {
		s.Closed = true
		s.Retired = true
		s.Pending = map[uint64]Opened{}
		s.Allowed = map[string][]byte{}
		s.Dispatched = map[string][]byte{}
		s.PendingAt = map[uint64]uint64{}
		s.GapDeadline = 0
		return true
	}
	return false
}
func (s *Signal) String() string {
	return fmt.Sprintf("gen=%d next=%d sdk=%d duplicate=%d conflict=%d rejected=%d unsupported=%d", s.Generation, s.Next, s.SDKCalls, s.Duplicates, s.Conflicts, s.Rejected, s.Unsupported)
}

func (s *Signal) ConfirmBody() []byte {
	oh, ah, of, af := s.LocalHash, s.RemoteHash, s.LocalFingerprint, s.RemoteFingerprint
	if !s.Offerer() {
		oh, ah, of, af = ah, oh, af, of
	}
	return PB(B(1, oh), B(2, ah), B(3, of), B(4, af))
}

// Every buffered frame witnesses each earlier missing sequence. Once the head
// gap drains, only witnesses still beyond the next hole determine its age.
// At most 32 witnesses avoids allocating by an untrusted sequence distance.
func (s *Signal) refreshGap() {
	s.GapDeadline = 0
	for seq, at := range s.PendingAt {
		due := min(at+2500, s.Pending[seq].Context.Deadline, s.RoundDeadline)
		if s.GapDeadline == 0 || due < s.GapDeadline {
			s.GapDeadline = due
		}
	}
}

// RememberChallenge freezes the outgoing record after sealing, before dispatch.
func (s *Signal) RememberChallenge(sent Opened) error {
	if s.Closed || !s.authorized() {
		return ErrSignal
	}
	fresh, e := OpenSent(sent.Raw, s.Self, s.Peer, s.Clock)
	if e != nil {
		return e
	}
	sent = fresh
	if e := s.preflight(&sent); e != nil {
		return e
	}
	c := sent.Context
	if s.Clock >= c.Deadline || s.ReceivedProbe == nil || s.Clock >= s.ReceivedProbe.Context.Deadline {
		return ErrSignal
	}
	f, e := ParseMessage(sent.Payload, "Challenge")
	if e != nil || need(f, "bbbbub") != nil || !bytes.Equal(f[6].B, s.ReceivedProbe.Hash) {
		return ErrSignal
	}
	if c.RecipientUnknown != 0 || sent.Type != 2 || c.Class != 1 || string(c.Domain) != "bootstrap" || c.Generation != 0 || c.Use != 0 || c.Seq == 0 || len(sent.Hash) != 32 || !bytes.Equal(c.From, s.Self.ID) || !bytes.Equal(c.To, s.Peer.ID) || !bytes.Equal(c.Connection, s.Connection) || !bytes.Equal(c.SenderBoot, s.Self.Boot) || !bytes.Equal(c.ReceiverBoot, s.Peer.Boot) || !bytes.Equal(c.SenderInstance, s.Self.Instance) || !bytes.Equal(c.ReceiverInstance, s.Peer.Instance) {
		return ErrSignal
	}
	copyContext, e := parseContext(c.Encode())
	if e != nil {
		return e
	}
	s.Challenge = &Opened{Context: copyContext, Type: 2, Hash: append([]byte(nil), sent.Hash...), Raw: append([]byte(nil), sent.Raw...), Payload: append([]byte(nil), sent.Payload...)}
	return nil
}
func (s *Signal) acceptACK(ack Opened) error {
	raw := ack.Payload
	f, e := ParseMessage(raw, "Ack")
	if e != nil || len(f) != 7 || len(f[1].B) != 0 || f[2].U != 0 || f[3].N != 0 {
		return ErrSignal
	}
	for _, n := range []uint64{4, 5, 7, 8} {
		v, ok := f[n]
		if !ok || v.Bytes {
			return ErrSignal
		}
	}
	h, ok := f[6]
	if !ok || !h.Bytes || len(h.B) != 32 || f[4].U < 1 || f[4].U > 2 || f[5].U == 0 || f[8].U > 2 {
		return ErrSignal
	}
	challenge := s.Challenge
	// With no outgoing Challenge, this is a transport ACK (e.g. for Probe).
	// The sender matches its frozen hash; it cannot establish BootReady here.
	if challenge == nil {
		if f[7].U == 0 && (f[4].U != 1 || f[8].U != 0) {
			return ErrSignal
		}
		return nil
	}
	if f[7].U == 0 || (challenge != nil && bytes.Equal(h.B, challenge.Hash)) {
		if challenge == nil {
			return ErrSignal
		}
		if s.Clock >= challenge.Context.Deadline {
			return ErrSignal
		}
		c := challenge.Context
		a := ack.Context
		if !bytes.Equal(a.From, c.To) || !bytes.Equal(a.To, c.From) || !bytes.Equal(a.Connection, c.Connection) || !bytes.Equal(a.SenderBoot, c.ReceiverBoot) || !bytes.Equal(a.ReceiverBoot, c.SenderBoot) || !bytes.Equal(a.SenderInstance, c.ReceiverInstance) || !bytes.Equal(a.ReceiverInstance, c.SenderInstance) {
			return ErrSignal
		}
		if f[4].U != c.Class || f[5].U != c.Seq || !bytes.Equal(h.B, challenge.Hash) || f[7].U != c.Generation || f[8].U != c.Use {
			return ErrSignal
		}
		s.BootReady = true
	} else if !s.BootReady {
		return ErrSignal
	}
	return nil
}

func (s *Signal) RememberProbe(sent Opened) error {
	if s.Closed || !s.authorized() {
		return ErrSignal
	}
	fresh, e := OpenSent(sent.Raw, s.Self, s.Peer, s.Clock)
	if e != nil {
		return e
	}
	sent = fresh
	if e := s.preflight(&sent); e != nil {
		return e
	}
	c := sent.Context
	if sent.Type != 1 || c.Class != 1 || string(c.Domain) != "bootstrap" || c.Generation != 0 || c.Use != 0 || s.Clock >= c.Deadline || !bytes.Equal(c.From, s.Self.ID) || !bytes.Equal(c.To, s.Peer.ID) || !bytes.Equal(c.Connection, s.Connection) || !bytes.Equal(c.SenderBoot, s.Self.Boot) || !bytes.Equal(c.SenderInstance, s.Self.Instance) || len(sent.Hash) != 32 {
		return ErrSignal
	}
	copyContext, e := parseContext(c.Encode())
	if e != nil {
		return e
	}
	s.Probe = &Opened{Context: copyContext, Type: 1, Hash: append([]byte(nil), sent.Hash...), Raw: append([]byte(nil), sent.Raw...)}
	return nil
}
func (s *Signal) retire(retry bool) {
	s.Closed = true
	s.Retired = retry
	s.Pending = map[uint64]Opened{}
	s.PendingAt = map[uint64]uint64{}
	s.Allowed = map[string][]byte{}
	s.Dispatched = map[string][]byte{}
	s.GapDeadline = 0
}
func (s *Signal) ForgetPeer() { s.Peer = s.Peer.Member(); s.retire(false) }
func (s *Signal) Dispatch(raw []byte, now uint64) bool {
	s.Tick(now)
	if s.Closed || !s.authorized() {
		return false
	}
	o, e := Open(raw, s.Peer, s.Self, s.Clock)
	if e != nil || o.Context.Generation != s.Generation || o.Context.Use != s.Use || !bytes.Equal(o.Context.Connection, s.Connection) {
		return false
	}
	key := Key(o.Context)
	if !bytes.Equal(s.Allowed[key], o.Hash) || s.Dispatched[key] != nil {
		return false
	}
	s.Dispatched[key] = append([]byte(nil), o.Hash...)
	s.Dispatches++
	return true
}

// preflight has no effects: every known body is decoded before caches, queues,
// generation, bootstrap, or SDK state can change. Unknown IDs remain opaque.
func (s *Signal) preflight(o *Opened) error {
	raw, e := KnownPayload(o.Context.Class, o.Type, o.Payload)
	if e != nil {
		return e
	}
	o.Payload = raw
	if o.Context.Class != 1 {
		return nil
	}
	switch o.Type {
	case 1, 2:
		f, err := ParseMessage(raw, signalSchemas[o.Type])
		if err != nil {
			return err
		}
		c := o.Context
		if !bytes.Equal(f[1].B, c.Connection) || !bytes.Equal(f[2].B, c.SenderInstance) || !bytes.Equal(f[3].B, c.SenderBoot) || (o.Type == 2 && !bytes.Equal(f[4].B, c.ReceiverBoot)) {
			return ErrSignal
		}
	case 3, 4:
		_, _, e = ReadSDPBody(raw)
	case 5:
		var cs []Candidate
		cs, _, e = ReadCandidates(raw)
		if e == nil {
			if len(cs) == 0 {
				return ErrSignal
			}
			for _, c := range cs {
				if c.Generation != o.Context.Generation || len(c.Ufrag) < 4 || len(c.Ufrag) > 256 || len(c.Mid) == 0 || c.Index != 0 {
					return ErrSignal
				}
			}
		}
	case 6:
		f, err := ParseMessage(raw, "EndCandidates")
		if err != nil {
			return err
		}
		if len(f[1].B) < 4 || f[2].U > 64 || f[3].U > 4 {
			return ErrSignal
		}
	case 7:
		f, err := ParseMessage(raw, "Confirm")
		if err != nil {
			return err
		}
		for _, v := range f {
			if len(v.B) != 32 {
				return ErrSignal
			}
		}
	case 8:
		targets, err := ReadRestart(raw)
		if err != nil {
			return err
		}
		if !s.Offerer() || o.Context.Use != s.Use || o.Context.Generation != s.Generation || len(targets) != 1 || targets[s.Use] != s.Generation {
			return ErrSignal
		}
	}
	return e
}

// The lab supplies a live authority view; real adapters must update it on every
// lease/member/owner change. Revision 1 is the immutable round grant.
func (s *Signal) SetAuthorization(until uint64, members bool, revision uint64) {
	s.LeaseUntil = until
	s.MembersAuthorized = members
	s.AuthorizationRevision = revision
}
func (s *Signal) authorized() bool {
	return s.Clock < s.LeaseUntil && s.MembersAuthorized && s.AuthorizationRevision == 1
}
func (s *Signal) ReplyAllowed(raw []byte, now uint64) bool {
	s.Tick(now)
	if s.Closed || !s.authorized() {
		return false
	}
	o, e := Open(raw, s.Peer, s.Self, s.Clock)
	if e != nil {
		return false
	}
	c := o.Context
	if !bytes.Equal(c.Connection, s.Connection) || !bytes.Equal(s.Seen[Key(c)], o.Hash) || (c.Generation != 0 && c.Generation != s.Generation) {
		return false
	}
	if e = s.preflight(&o); e != nil {
		return false
	}
	return !(c.Class == 2 && (o.Type == 4 || o.Type == 10 || o.Type == 11 || o.Type == 12 || o.Type == 13))
}
