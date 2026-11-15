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
	Self                                                                               Identity
	Peer                                                                               Public
	Connection                                                                         []byte
	Use, Generation, Next                                                              uint64
	BootReady, LocalSDP, RemoteSDP, DTLS, Confirmed, Closed, Retired, Ended            bool
	ChallengeHash                                                                      []byte
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

func NewSignal(self Identity, peer Public, connection []byte, use uint64) *Signal {
	return &Signal{Self: self, Peer: peer, Connection: connection, Use: use, Generation: 1, Next: 1, Seen: map[string][]byte{}, Pending: map[uint64]Opened{}, PendingAt: map[uint64]uint64{}, CacheUntil: map[string]uint64{}}
}
func (s *Signal) Offerer() bool { return bytes.Compare(s.Self.ID, s.Peer.ID) < 0 }
func (s *Signal) Ready() bool   { return !s.Closed && s.LocalSDP && s.RemoteSDP && s.DTLS && s.Confirmed }
func (s *Signal) SetLocal(raw []byte, offer bool, generation uint64) error {
	if generation != s.Generation || s.Closed || !s.BootReady || offer != s.Offerer() || (offer && s.LocalSDP) || (!offer && !s.RemoteSDP) {
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
	if s.Closed && !s.Retired || gen != s.Generation+1 || gen == 0 {
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
	s.PendingAt = map[uint64]uint64{}
	s.GapDeadline = 0
	s.Candidates = 0
	s.Batches = 0
	s.Ended = false
	return nil
}
func (s *Signal) Receive(packet []byte, now uint64) ([]Action, error) {
	opened, e := Open(packet, s.Peer, s.Self, now)
	if e != nil {
		s.Rejected++
		return nil, e
	}
	return s.Accept(opened, now)
}
func (s *Signal) Accept(o Opened, now uint64) ([]Action, error) {
	c := o.Context
	if !bytes.Equal(c.Connection, s.Connection) || !bytes.Equal(c.From, s.Peer.ID) {
		s.Rejected++
		return nil, ErrSignal
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
	if len(s.Seen) >= 2048 {
		s.Rejected++
		return nil, ErrSignal
	}
	if c.Class == 2 {
		if !s.BootReady && o.Type == 4 && len(s.ChallengeHash) > 0 {
			f, e := Parse(o.Payload, 5)
			if e != nil || len(f) != 5 || f[4].U != 1 || f[5].U != 1 || !bytes.Equal(f[6].B, s.ChallengeHash) || f[7].U != 0 || f[8].U != s.Use {
				return nil, ErrSignal
			}
			s.BootReady = true
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
		if string(c.Domain) != "bootstrap" {
			s.Rejected++
			return nil, ErrSignal
		}
		f, e := Parse(o.Payload, 5)
		if e != nil {
			return nil, e
		}
		if o.Type == 1 {
			if need(f, "bbb") != nil || !bytes.Equal(f[1].B, c.Connection) || !bytes.Equal(f[2].B, c.SenderInstance) || !bytes.Equal(f[3].B, c.SenderBoot) {
				return nil, ErrSignal
			}
		} else {
			schema := "bbbb"
			if len(f) == 5 {
				schema = "bbbbu"
			}
			if need(f, schema) != nil || (len(f) == 5 && f[5].U > 1) || !bytes.Equal(f[1].B, c.Connection) || !bytes.Equal(f[2].B, c.SenderInstance) || !bytes.Equal(f[3].B, c.SenderBoot) || !bytes.Equal(f[4].B, s.Self.Boot) {
				return nil, ErrSignal
			}
		}
		if !s.LocalSDP && !s.RemoteSDP {
			s.Peer.Instance = append([]byte(nil), c.SenderInstance...)
			s.Peer.Boot = append([]byte(nil), c.SenderBoot...)
		}
		s.Seen[key] = o.Hash
		s.CacheUntil[key] = c.Deadline + 300000
		if o.Type == 2 {
			s.BootReady = true
			if len(f) == 5 && f[5].U == 1 {
				s.StaleChallenges++
			}
		}
		return []Action{{1, o.Type, o.Payload, 0}}, nil
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
		key = Key(o.Context)
		s.Seen[key] = o.Hash
		s.CacheUntil[key] = o.Context.Deadline + 300000
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
		f, e := Parse(o.Payload, 3)
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
		s.PendingAt = map[uint64]uint64{}
		s.GapDeadline = 0
	default:
		s.Unsupported++
		action = Action{2, 10, PB(U(1, o.Context.Class), U(2, o.Type), U(3, o.Context.Seq), B(4, o.Hash)), o.Context.Generation}
	}
	return action, nil
}
func (s *Signal) Tick(now uint64) bool {
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
	for _, at := range s.PendingAt {
		due := at + 2500
		if s.GapDeadline == 0 || due < s.GapDeadline {
			s.GapDeadline = due
		}
	}
}
