package direct

import (
	"bytes"
	"github.com/Lewin671/remote-agent/internal/transport/peerwire"
)

type peerScopeWindow struct {
	next    uint64
	pending map[uint64]peerwire.Opened
	at      map[uint64]uint64
	gap     uint64
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
		if bytes.Equal(old, o.Hash) {
			if reply := s.receipts[string(o.Hash)]; reply != nil {
				s.transmit(reply)
			}
		}
		return
	}
	if !s.validPeerScope(o) {
		return
	}
	if s.peerScopes == nil {
		s.peerScopes = map[uint64]*peerScopeWindow{}
	}
	window := s.peerScopes[o.Context.Generation]
	if window == nil {
		if len(s.peerScopes) >= 32 {
			return
		}
		window = &peerScopeWindow{next: 1, pending: map[uint64]peerwire.Opened{}, at: map[uint64]uint64{}}
		s.peerScopes[o.Context.Generation] = window
	}
	if o.Context.Seq < window.next || len(window.pending) >= 32 {
		return
	}
	s.controlSeen[key] = append([]byte(nil), o.Hash...)
	if o.Context.Seq > window.next {
		window.pending[o.Context.Seq] = o
		window.at[o.Context.Seq] = now
		if window.gap == 0 {
			window.gap = min(now+2500, o.Context.Deadline, s.cfg.Deadline)
		}
		return
	}
	for {
		fresh, err := s.cfg.Codec.Open(o.Raw, s.pcs[0].signal.Peer, s.cfg.Self, now)
		if err != nil || !s.validPeerScope(fresh) {
			s.Retire()
			return
		}
		s.acknowledge(fresh, now)
		if fresh.Type == 9 {
			s.Retire()
			return
		}
		targets, _ := peerwire.ReadRestart(fresh.Payload)
		for use := range targets {
			s.restartPC(int(use-1), now)
		}
		window.next++
		next, ok := window.pending[window.next]
		if !ok {
			break
		}
		delete(window.pending, window.next)
		delete(window.at, window.next)
		o = next
	}
	window.gap = 0
	for seq, pending := range window.pending {
		due := min(window.at[seq]+2500, pending.Context.Deadline, s.cfg.Deadline)
		if window.gap == 0 || due < window.gap {
			window.gap = due
		}
	}
}
func (s *Session) validPeerScope(o peerwire.Opened) bool {
	if o.Type == 9 {
		return true
	}
	if o.Type != 8 || !s.pcs[0].signal.Offerer() {
		return false
	}
	targets, err := peerwire.ReadRestart(o.Payload)
	if err != nil {
		return false
	}
	for use, gen := range targets {
		if use < 1 || use > 2 || s.pcs[use-1].signal.Generation != gen {
			return false
		}
	}
	return true
}
