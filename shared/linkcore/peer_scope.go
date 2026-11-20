package linkcore

import "github.com/Lewin671/remote-agent/shared/peerwire"

// PeerScope orders peer-scope (use 0) Restart/Close frames: each generation
// has an independent window that buffers out-of-order frames and retires the
// round if a gap outlives its deadline.
type PeerScope struct{ windows map[uint64]*scopeWindow }

type scopeWindow struct {
	next    uint64
	pending map[uint64]peerwire.Opened
	at      map[uint64]uint64
	gap     uint64
}

// ScopeAdmit is the outcome of offering an authenticated, valid frame.
type ScopeAdmit int

const (
	ScopeDrop     ScopeAdmit = iota // over a window limit or already passed; not recorded
	ScopeBuffered                   // recorded, waiting for earlier sequence numbers
	ScopeReady                      // recorded and next in order: process it, then Advance
)

// Admit places o in its generation's window. deadline bounds a gap together
// with the frame's own deadline and a 2.5 s reorder allowance.
func (p *PeerScope) Admit(o peerwire.Opened, now, deadline uint64) ScopeAdmit {
	if p.windows == nil {
		p.windows = map[uint64]*scopeWindow{}
	}
	w := p.windows[o.Context.Generation]
	if w == nil {
		if len(p.windows) >= 32 {
			return ScopeDrop
		}
		w = &scopeWindow{next: 1, pending: map[uint64]peerwire.Opened{}, at: map[uint64]uint64{}}
		p.windows[o.Context.Generation] = w
	}
	if o.Context.Seq < w.next || len(w.pending) >= 32 {
		return ScopeDrop
	}
	if o.Context.Seq > w.next {
		w.pending[o.Context.Seq] = o
		w.at[o.Context.Seq] = now
		if w.gap == 0 {
			w.gap = min(now+2500, o.Context.Deadline, deadline)
		}
		return ScopeBuffered
	}
	return ScopeReady
}

// Advance marks the in-order frame of generation as processed and returns
// the next buffered frame, if it is now in order.
func (p *PeerScope) Advance(generation uint64) (peerwire.Opened, bool) {
	w := p.windows[generation]
	w.next++
	next, ok := w.pending[w.next]
	if ok {
		delete(w.pending, w.next)
		delete(w.at, w.next)
	}
	return next, ok
}

// Settle recomputes generation's gap deadline once its in-order run ends.
func (p *PeerScope) Settle(generation, deadline uint64) {
	w := p.windows[generation]
	w.gap = 0
	for seq, pending := range w.pending {
		due := min(w.at[seq]+2500, pending.Context.Deadline, deadline)
		if w.gap == 0 || due < w.gap {
			w.gap = due
		}
	}
}

// Expired reports whether any window's gap deadline has passed.
func (p *PeerScope) Expired(now uint64) bool {
	for _, w := range p.windows {
		if w.gap != 0 && now >= w.gap {
			return true
		}
	}
	return false
}

// ValidPeerScope checks a peer-scope Close (9) or Restart (8). Only the
// offerer may receive Restart, and every target must name the current
// generation of its use.
func ValidPeerScope(o peerwire.Opened, offerer bool, generations [2]uint64) bool {
	if o.Type == 9 {
		return true
	}
	if o.Type != 8 || !offerer {
		return false
	}
	targets, err := peerwire.ReadRestart(o.Payload)
	if err != nil {
		return false
	}
	for use, gen := range targets {
		if use < 1 || use > 2 || generations[use-1] != gen {
			return false
		}
	}
	return true
}
