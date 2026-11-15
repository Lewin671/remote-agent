package labwire

import "bytes"

type Pending struct {
	Raw, Hash        []byte
	First, NotBefore uint64
	Attempts         int
}
type Sender struct {
	Deadline                   uint64
	Pending                    map[string]*Pending
	TimedOut                   bool
	Frames, Bytes              int
	RetryAt, NetworkGeneration uint64
	Failures                   uint32
}

func NewSender(deadline uint64) *Sender {
	return &Sender{Deadline: deadline, Pending: map[string]*Pending{}}
}
func (s *Sender) Enqueue(raw []byte, now uint64) bool {
	if s.TimedOut || now >= s.Deadline || len(s.Pending) >= 32 {
		return false
	}
	hash := Hash(raw)
	key := string(hash)
	if _, ok := s.Pending[key]; ok {
		return true
	}
	s.Pending[key] = &Pending{Raw: append([]byte(nil), raw...), Hash: hash, First: now}
	return true
}
func (s *Sender) Tick(now uint64) [][]byte {
	if now >= s.Deadline {
		s.Pending = map[string]*Pending{}
		s.TimedOut = true
		return nil
	}
	var out [][]byte
	for _, p := range s.Pending {
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
			out = append(out, p.Raw)
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
type CandidateBatcher struct {
	Generation     uint64
	Due            uint64
	Pending        [][]byte
	Count, Batches int
	Ended          bool
}

func (b *CandidateBatcher) Add(raw []byte, g, now uint64) bool {
	if b.Ended || g != b.Generation || b.Count >= 64 || len(raw) > 1024 {
		return false
	}
	if len(b.Pending) == 0 {
		b.Due = now + 50
	}
	b.Pending = append(b.Pending, append([]byte(nil), raw...))
	b.Count++
	return true
}
func (b *CandidateBatcher) Flush(now uint64, end bool) [][][]byte {
	if end {
		b.Ended = true
	}
	if !end && now < b.Due {
		return nil
	}
	var out [][][]byte
	for len(b.Pending) >= 16 || (end && len(b.Pending) > 0) {
		n := min(16, len(b.Pending))
		out = append(out, b.Pending[:n])
		b.Pending = b.Pending[n:]
		b.Batches++
		if b.Batches > 4 {
			panic("candidate batches")
		}
	}
	b.Due = 0
	if len(b.Pending) > 0 {
		b.Due = now + 50
	}
	return out
}
