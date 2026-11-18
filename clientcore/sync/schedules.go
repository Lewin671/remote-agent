package sync

import (
	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
)

// EnsureSessionEventsLoaded triggers metadata or history loading for sid without altering watchedSessions.
func (e *Engine) EnsureSessionEventsLoaded(ctx *StepContext, sid string) {
	if sid == "" {
		return
	}
	if e.sessions[sid] == nil {
		for _, pending := range e.sessionByIDOps {
			if pending == sid {
				return
			}
		}
		e.fetchSessionByID(ctx, sid)
		return
	}
	b := e.buffers[sid]
	if b == nil {
		e.startLoadEvents(ctx, sid)
		return
	}
	if !b.loaded && !b.awaitingCache && !b.fetching && !b.retired && b.errorText == nil && b.fetchErrorText == nil && len(b.unavailableSeqs) == 0 && e.running && e.conn == lists.ConnConnected {
		e.startFetchEvents(ctx, sid)
	}
}

// SessionEventsSettled reports whether sid's event history has reached a settled state (loaded cleanly, terminal error/retired, or offline cache read finished).
func (e *Engine) SessionEventsSettled(sid string) bool {
	if sid == "" {
		return true
	}
	offline := !e.running || e.conn != lists.ConnConnected
	if e.sessions[sid] == nil {
		return offline
	}
	b := e.buffers[sid]
	if b == nil {
		return false
	}
	if b.errorText != nil || b.fetchErrorText != nil || b.retired || len(b.unavailableSeqs) > 0 {
		return true
	}
	s := e.sessions[sid]
	hasGap := len(b.pending) > 0 || (s != nil && uint64(len(b.prefix)) < s.LastSeq)
	if b.loaded && !hasGap {
		return true
	}
	if offline {
		return !b.awaitingCache && !b.backfilling && !b.fetching
	}
	return false
}
