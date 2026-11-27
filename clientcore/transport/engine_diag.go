package transport

// Diagnostics events: what the session layer and the WebRTC executor saw of
// a round. They feed the record only and never change a decision.

// DiagMark: a use of the round reached a step (session layer).
type DiagMark struct {
	Round RoundID
	Use   Use
	Mark  Mark
}

// SignalNote is what the relay or the computer answered to a signal.
type SignalNote uint8

const (
	NoteUnsupported SignalNote = iota + 1 // the computer answered Unsupported
	NoteLimited                           // the relay limited delivery
	NoteOffline                           // the relay found the computer offline
)

// DiagSignal: a delivery receipt or refusal for the round's signals.
type DiagSignal struct {
	Round RoundID
	Note  SignalNote
}

// DiagReject: the session refused a signal (Kind from SignalKind) or the
// device refused a remote candidate (Kind "browser"); Reason is a short code.
type DiagReject struct {
	Round        RoundID
	Use          Use
	Kind, Reason string
}

// DiagAttempt: the executor made a new peer connection for the use.
type DiagAttempt struct {
	Round RoundID
	Use   Use
}

// DiagCandidate: a local or remote candidate line; only its kind is kept.
type DiagCandidate struct {
	Round  RoundID
	Use    Use
	Remote bool
	Line   string
}

// DiagStunError: an ICE server error; only the code and local family are kept.
type DiagStunError struct {
	Round   RoundID
	Use     Use
	Code    int
	Address string
}

// DiagState: a WebRTC state machine changed.
type DiagState struct {
	Round RoundID
	Use   Use
	Kind  StateKind
	Value string
}

// DiagBlocked: the device would not make the peer connection.
type DiagBlocked struct {
	Round RoundID
	Use   Use
}

// DiagSampleWanted: a moment worth a stats read (a deciding state change,
// shortly before the deadline, before closing).
type DiagSampleWanted struct {
	Round RoundID
	Use   Use
}

// DiagStats answers ReadStats; Report is nil when the read failed.
type DiagStats struct {
	Round  RoundID
	Use    Use
	Report *StatsReport
}

// DiagPathEnd records why a use's path closed and its liveness probe counters.
type DiagPathEnd struct {
	Round  RoundID
	Use    Use
	Cause  CloseCause
	Probes ProbeStats
}

// RefreshDiagnostics: the sheet is open; read the stats of the rounds being tried.
type RefreshDiagnostics struct{ Machine string }

// ReadStats: read getStats of the use's current peer connection once.
type ReadStats struct {
	Round RoundID
	Use   Use
}

func (DiagMark) event()           {}
func (DiagSignal) event()         {}
func (DiagReject) event()         {}
func (DiagAttempt) event()        {}
func (DiagCandidate) event()      {}
func (DiagStunError) event()      {}
func (DiagState) event()          {}
func (DiagBlocked) event()        {}
func (DiagSampleWanted) event()   {}
func (DiagStats) event()          {}
func (DiagPathEnd) event()        {}
func (RefreshDiagnostics) event() {}
func (ReadStats) effect()         {}

// Diagnostics is a computer's last rounds (newest first) and when a round
// was last refused for lack of slots, if that is newer.
func (e *Engine) Diagnostics(m string) (rounds []*RoundData, blockedAt int64, blocked bool) {
	blockedAt, blocked = e.diag.BlockedAt(m)
	return e.diag.Rounds(m), blockedAt, blocked
}

func (e *Engine) began(now int64, p *round) { e.diag.begin(p.machine, p.id, now) }

func (e *Engine) blocked(now int64, m string) {
	e.diag.blocked(m, now)
	e.markChanged()
}

func (e *Engine) pathUp(now int64, p *round, u Use) {
	if r := e.diag.round(p.id); r != nil {
		r.Pc(u).mark(r.since(now), MarkReady)
		r.establish(now)
	}
}

// finished records how a round ended: lost after a path came up, else the
// session's cause, else a timeout, a retirement or a cancellation.
func (e *Engine) finished(now int64, p *round, timedOut bool, cause CloseCause) {
	r := e.diag.round(p.id)
	if r == nil {
		return
	}
	end := EndCancelled
	switch {
	case p.established:
		end = EndLost
	case p.failure == FailurePeerUnsupported:
		end = EndPeerUnsupported
	case p.failure == FailureNoResponse:
		end = EndNoResponse
	case p.failure == FailureNoTraversal:
		end = EndNoTraversal
	case timedOut:
		end = EndTimeout
	case p.retired:
		end = EndRetired
	}
	if cause == CloseNone && timedOut && !p.established {
		cause = CloseTimeout
	}
	if cause != CloseNone {
		r.recordCloseCause(cause, false)
		t := r.since(now)
		for i := range r.Pcs {
			if r.Pcs[i].HasMark[MarkReady] && !r.Pcs[i].HasClosed {
				r.Pcs[i].recordClose(t, cause, ProbeStats{})
			}
		}
	}
	r.finish(now, end)
}

func (e *Engine) handleDiag(now int64, ev Event) bool {
	var id RoundID
	var u Use
	switch ev := ev.(type) {
	case DiagMark:
		id, u = ev.Round, ev.Use
	case DiagSignal:
		id = ev.Round
	case DiagReject:
		id, u = ev.Round, ev.Use
	case DiagAttempt:
		id, u = ev.Round, ev.Use
	case DiagCandidate:
		id, u = ev.Round, ev.Use
	case DiagStunError:
		id, u = ev.Round, ev.Use
	case DiagState:
		id, u = ev.Round, ev.Use
	case DiagBlocked:
		id, u = ev.Round, ev.Use
	case DiagSampleWanted:
		id, u = ev.Round, ev.Use
	case DiagStats:
		id, u = ev.Round, ev.Use
	case DiagPathEnd:
		id, u = ev.Round, ev.Use
	case RefreshDiagnostics:
		for _, p := range e.sortedRounds() {
			if p.machine == ev.Machine && e.active(p, now) {
				e.sample(p.id, UseRead)
				e.sample(p.id, UseBulk)
			}
		}
		return true
	default:
		return false
	}
	r := e.diag.round(id)
	if r == nil {
		return true
	}
	pc, t := r.Pc(u), r.since(now)
	a := pc.current()
	switch ev := ev.(type) {
	case DiagMark:
		pc.mark(t, ev.Mark)
	case DiagSignal:
		switch ev.Note {
		case NoteUnsupported:
			r.Unsupported = true
		case NoteLimited:
			r.Limited++
		case NoteOffline:
			r.Offline++
		}
	case DiagReject:
		pc.reject(ev.Kind, ev.Reason)
	case DiagAttempt:
		pc.begin(t)
	case DiagBlocked:
		pc.Blocked = true
	case DiagSampleWanted:
		e.sample(id, u)
	case DiagCandidate:
		if a != nil {
			a.candidate(ev.Remote, ev.Line)
		}
	case DiagStunError:
		if a != nil {
			a.stunError(ev.Code, ev.Address)
		}
	case DiagState:
		if a != nil {
			a.state(t, ev.Kind, ev.Value)
		}
	case DiagStats:
		if a != nil {
			a.sampling = false
			if ev.Report != nil {
				a.take(t, *ev.Report)
			}
		}
	case DiagPathEnd:
		pc.recordClose(t, ev.Cause, ev.Probes)
		r.recordCloseCause(ev.Cause, pc.HasMark[MarkReady])
	}
	e.markChanged()
	return true
}

// sample asks for one stats read: one at a time, DiagSamples per attempt.
func (e *Engine) sample(id RoundID, u Use) {
	r := e.diag.round(id)
	if r == nil {
		return
	}
	a := r.Pc(u).current()
	if a == nil || a.sampling || a.Samples >= DiagSamples {
		return
	}
	a.Samples++
	a.sampling = true
	e.emit(ReadStats{Round: id, Use: u})
}
