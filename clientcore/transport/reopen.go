package transport

import "github.com/Lewin671/remote-agent/shared/linkcore"

// TerminalStableMs: a relay stream live this long starts its backoff over.
const TerminalStableMs = 5_000

// Reopen decides when a closed relay stream (the terminal WebSocket) opens
// again. The backoff and the token buckets are linkcore's Recovery and the
// process-wide Budget, so every stream of the process shares one budget.
type Reopen struct {
	rec      linkcore.Recovery
	needs    bool // closed and wanting to reopen
	streak   bool // reopened since the stream was last stable
	stableAt int64
	stable   bool
	retryAt  int64
	retry    bool
}

// Wake tells the owner what to do after a call: open the stream now, and
// call Timer at At when Set.
type Wake struct {
	Open bool
	At   int64
	Set  bool
}

// OfflineClosed: the stream could not open because the main connection is down.
func (r *Reopen) OfflineClosed() {
	r.stable = false
	r.needs = true
}

// Live: the stream is live with its snapshot; stable after TerminalStableMs.
func (r *Reopen) Live(now int64) Wake {
	r.needs = false
	r.stableAt, r.stable = now+TerminalStableMs, true
	return r.next(Wake{})
}

// Closed: the stream closed or failed to open. A failure while reopening
// counts toward the backoff (unit is the jitter draw). A stream that never
// opened, or with the main connection offline, waits for MainOnline.
func (r *Reopen) Closed(now int64, opened, online bool, unit float64, b *linkcore.Budget) Wake {
	r.stable = false
	r.needs = true
	if r.streak {
		r.rec.Failed(now, unit)
	}
	if !opened || !online {
		return r.next(Wake{})
	}
	return r.try(now, online, b)
}

// MainOnline: the main connection came online; a closed stream reopens.
func (r *Reopen) MainOnline(now int64, online bool, b *linkcore.Budget) Wake {
	if !r.needs || !online {
		return r.next(Wake{})
	}
	return r.try(now, online, b)
}

// Manual: the user asked to reconnect; the backoff starts over and it opens now.
func (r *Reopen) Manual() Wake {
	*r = Reopen{rec: r.rec}
	r.rec.Ready()
	return Wake{Open: true}
}

// Timer: a time asked for by a Wake has come.
func (r *Reopen) Timer(now int64, online bool, b *linkcore.Budget) Wake {
	if r.stable && now >= r.stableAt {
		r.stable, r.streak = false, false
		r.rec.Ready()
	}
	if r.retry && now >= r.retryAt {
		r.retry = false
		return r.try(now, online, b)
	}
	return r.next(Wake{})
}

func (r *Reopen) try(now int64, online bool, b *linkcore.Budget) Wake {
	if !r.needs || !online || r.retry {
		return r.next(Wake{})
	}
	d := r.rec.DelayMs(now, b)
	if d == 0 && r.rec.Take(now, b) {
		r.needs, r.streak = false, true
		return r.next(Wake{Open: true})
	}
	r.retryAt, r.retry = now+max(1, d), true
	return r.next(Wake{})
}

// next adds the earliest pending time to w.
func (r *Reopen) next(w Wake) Wake {
	for _, t := range []struct {
		at  int64
		set bool
	}{{r.stableAt, r.stable}, {r.retryAt, r.retry}} {
		if t.set && (!w.Set || t.at < w.At) {
			w.At, w.Set = t.at, true
		}
	}
	return w
}
