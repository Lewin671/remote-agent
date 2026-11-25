package peerwire

import "bytes"

type Pending struct {
	Raw, Hash                         []byte
	First, NotBefore, Expiry, Binding uint64
	Attempts                          int
}
type Sender struct {
	LeaseUntil                 uint64
	MembersAuthorized          bool
	Deadline                   uint64
	Pending                    map[string]*Pending
	TimedOut                   bool
	Paused                     bool
	Frames, Bytes              int
	RetryAt, NetworkGeneration uint64
	Failures                   uint32
	Binding, Clock             uint64
}

func NewSender(deadline uint64) *Sender {
	return &Sender{LeaseUntil: deadline, MembersAuthorized: true, Deadline: deadline, Binding: 1, Pending: map[string]*Pending{}}
}
func (s *Sender) Enqueue(raw []byte, now, expiry, binding uint64) bool {
	s.Clock = max(s.Clock, now)
	now = s.Clock
	if !s.authorized() || s.TimedOut || binding != s.Binding || now >= expiry || now >= s.Deadline || len(s.Pending) >= 32 {
		return false
	}
	hash := Hash(raw)
	key := string(hash)
	if _, ok := s.Pending[key]; ok {
		return true
	}
	s.Pending[key] = &Pending{Raw: append([]byte(nil), raw...), Hash: hash, First: now, Expiry: min(expiry, s.Deadline), Binding: binding}
	return true
}
func (s *Sender) Tick(now uint64) [][]byte {
	s.Clock = max(s.Clock, now)
	now = s.Clock
	if now >= s.Deadline {
		s.Pending = map[string]*Pending{}
		s.TimedOut = true
		return nil
	}
	if s.Paused {
		return nil
	}
	var out [][]byte
	for key, p := range s.Pending {
		if !s.authorized() || now >= p.Expiry || p.Binding != s.Binding {
			delete(s.Pending, key)
			continue
		}
		if p.Attempts == 3 {
			continue
		}
		due := p.First + []uint64{0, 500, 1500}[p.Attempts]
		if p.NotBefore > due {
			due = p.NotBefore
		}
		if now >= due {
			if !bytes.Equal(Hash(p.Raw), p.Hash) {
				panic("mutable frozen frame")
			}
			out = append(out, append([]byte(nil), p.Raw...))
			p.Attempts++
			s.Frames++
			s.Bytes += len(p.Raw)
		}
	}
	return out
}
func (s *Sender) Ack(hash []byte) bool {
	key := string(hash)
	if _, ok := s.Pending[key]; !ok {
		return false
	}
	delete(s.Pending, key)
	return true
}
func (s *Sender) Limited(hash []byte, now, delay uint64) {
	if p := s.Pending[string(hash)]; p != nil {
		if now >= s.Deadline || delay > s.Deadline-now {
			p.NotBefore = s.Deadline
		} else {
			p.NotBefore = max(p.NotBefore, now+delay)
		}
	}
}
func (s *Sender) Backoff(now uint64, fraction float64) uint64 {
	cap := uint64(30000)
	if s.Failures < 5 {
		cap = 1000 << s.Failures
	}
	if cap > 30000 {
		cap = 30000
	}
	s.Failures++
	s.RetryAt = now + uint64(float64(cap)*fraction)
	return s.RetryAt
}
func (s *Sender) NetworkChanged(g uint64) { s.NetworkGeneration = max(s.NetworkGeneration, g) }

// CandidateBatcher owns one SDK callback generation and a 50ms collection timer.
// It returns original candidate bytes; the adapter adds the bound ufrag/mid.
// Wire protocol allows at most 4 batches of 16 candidates (index 0..3) and 64
// candidates total. Batches 0..2 flush on the 50ms timer to preserve Trickle
// ICE; batch 3 holds until 16 candidates accumulate or gathering ends so the
// 4-batch wire limit is never exceeded.
type CandidateBatcher struct {
	Generation, Deadline, Binding uint64
	Due                           uint64
	Pending                       [][]byte
	Count, Batches                int
	Seen                          int
	Ended                         bool
	noEarlyFlush                  bool // frozen 1a vector replay only
}

func (b *CandidateBatcher) Add(raw []byte, g, now, binding uint64) bool {
	if now >= b.Deadline {
		b.Ended = true
		b.Pending = nil
		b.Due = 0
	}
	if b.Ended || binding != b.Binding || g != b.Generation || b.Seen >= 64 || len(raw) > 1024 {
		return false
	}
	b.Seen++
	// Wire allows at most 4 batches of 16 candidates. If early trickle batches
	// used fewer than 16 candidates, drop candidates past the remaining wire
	// batch capacity without panicking or breaking the 64-candidate budget.
	remCap := (4 - b.Batches) * 16
	if len(b.Pending) >= remCap {
		return true
	}
	if len(b.Pending) == 0 && (b.noEarlyFlush || b.Batches < 3) {
		b.Due = now + 50
	}
	b.Pending = append(b.Pending, append([]byte(nil), raw...))
	b.Count++
	return true
}
func (b *CandidateBatcher) Flush(now uint64, end bool, g, binding uint64) [][][]byte {
	if now >= b.Deadline || g != b.Generation || binding != b.Binding {
		b.Ended = true
		b.Pending = nil
		b.Due = 0
		return nil
	}
	if end {
		b.Ended = true
	}
	if !end && now < b.Due {
		return nil
	}
	var out [][][]byte
	for len(b.Pending) >= 16 || (end && len(b.Pending) > 0) || (!b.noEarlyFlush && b.Batches < 3 && len(b.Pending) > 0) {
		if b.Batches >= 4 {
			b.Pending = nil
			break
		}
		n := min(16, len(b.Pending))
		out = append(out, b.Pending[:n])
		b.Pending = b.Pending[n:]
		b.Batches++
	}
	b.Due = 0
	if len(b.Pending) > 0 && b.Batches < 4 && (b.noEarlyFlush || b.Batches < 3) {
		b.Due = now + 50
	}
	return out
}

func (s *Sender) Rebind(binding uint64) { s.Binding = binding; s.Pending = map[string]*Pending{} }

func (s *Sender) Admit(raw []byte, now uint64) bool {
	s.Clock = max(s.Clock, now)
	p := s.Pending[string(Hash(raw))]
	return s.authorized() && p != nil && !s.TimedOut && p.Binding == s.Binding && s.Clock < p.Expiry && s.Clock < s.Deadline
}

func (s *Sender) authorized() bool { return s.MembersAuthorized && s.Clock < s.LeaseUntil }
func (s *Sender) SetAuthorization(until uint64, members bool, revision uint64) {
	s.LeaseUntil = until
	s.MembersAuthorized = members
	if revision != s.Binding {
		s.Rebind(revision)
	}
}

func (s *Sender) Offline()    { s.Paused = true }
func (s *Sender) Discovered() { s.Paused = false }
