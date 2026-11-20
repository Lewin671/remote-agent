package direct

import (
	"bytes"

	"github.com/Lewin671/remote-agent/shared/linkcore"
	"github.com/Lewin671/remote-agent/shared/peerwire"
)

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
			if reply, hasReply := s.receipts[string(o.Hash)]; hasReply && now < o.Context.Deadline {
				s.transmit(reply.bytes)
			}
		}
		return
	}
	if !s.validPeerScope(o) {
		return
	}
	admit := s.peerScope.Admit(o, now, s.cfg.Deadline)
	if admit == linkcore.ScopeDrop {
		return
	}
	s.controlSeen[key] = cachedBytes{bytes: append([]byte(nil), o.Hash...), until: o.Context.Deadline + 300000}
	if admit == linkcore.ScopeBuffered {
		return
	}
	generation := o.Context.Generation
	for {
		fresh, err := s.cfg.Self.Open(s.cfg.Codec, o.Raw, s.pcs[0].signal.Peer, now)
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
		next, ok := s.peerScope.Advance(generation)
		if !ok {
			break
		}
		o = next
	}
	s.peerScope.Settle(generation, s.cfg.Deadline)
}
func (s *Session) validPeerScope(o peerwire.Opened) bool {
	return linkcore.ValidPeerScope(o, s.pcs[0].signal.Offerer(), [2]uint64{s.pcs[0].signal.Generation, s.pcs[1].signal.Generation})
}
