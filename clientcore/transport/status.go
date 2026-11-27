package transport

// State is what the user can be told about a computer's direct path.
type State uint8

const (
	StateNone        State = iota // no direct path; Reason says why
	StateUnsupported              // this device cannot make direct paths
	StateConnected                // a path is up; Read and File say which route each use takes
	StateConnecting               // a round is running
)

// Reason is why there is no direct path. Only causes this side can establish:
// NoResponse means the computer never answered the handshake (its direct
// setting may be off, or it may not support it).
type Reason uint8

const (
	ReasonNone Reason = iota
	ReasonDisabled
	ReasonPeerUnsupported
	ReasonNoResponse
	ReasonNoTraversal
	ReasonFailed
	ReasonLost
	ReasonAtCap
	ReasonSecurityPending
	ReasonStarting
)

// Status is the display snapshot of one computer's direct path.
type Status struct {
	State    State
	Reason   Reason
	Read     Route
	File     Route
	Terminal Route
	ReadSlow bool // the read path fell back under rule 4 and is waiting
	FileSlow bool // the file path fell back under rule 4 and is waiting
	Measured Measured
	// RetryScheduled: a retry is really scheduled, so 「稍后会自动再试」 is true.
	RetryScheduled bool
}

func reasonOf(f Failure) Reason {
	switch f {
	case FailurePeerUnsupported:
		return ReasonPeerUnsupported
	case FailureNoResponse:
		return ReasonNoResponse
	case FailureNoTraversal:
		return ReasonNoTraversal
	case FailureLost:
		return ReasonLost
	}
	return ReasonFailed
}

// Status reports what the user can be told about the computer's direct path
// now. Numbers are present only when measured.
func (e *Engine) Status(now int64, m string) Status {
	s := Status{Read: Relay, File: Relay, Terminal: Relay}
	if r := e.routes[m]; r != nil {
		s.Measured = r.Measured(now)
	}
	if !e.supported {
		s.State = StateUnsupported
		return s
	}
	if !e.enabled(m) {
		s.Reason = ReasonDisabled
		return s
	}
	p := e.peers[m]
	live := p != nil && e.active(p, now)
	if live && (p.paths[UseRead].ready || p.paths[UseBulk].ready) {
		route := e.routing(m)
		readReady, fileReady := e.Ready(now, m, Hint{Use: UseRead}), p.paths[UseBulk].ready
		s.State = StateConnected
		if readReady {
			s.Read, s.ReadSlow = route.ReadRoute(now), route.ReadSlow(now)
			if p.terminalDirect {
				s.Terminal = Direct
			}
		}
		if fileReady {
			s.File, s.FileSlow = route.FileRoute(now), route.FileSlow(now)
		}
		return s
	}
	if live {
		s.State = StateConnecting
		return s
	}
	switch f, ok := e.failures[m]; {
	case ok:
		s.Reason = reasonOf(f)
	case !e.current(now):
		s.Reason = ReasonSecurityPending
	case len(e.peers) >= MaxPeers:
		s.Reason = ReasonAtCap
	default:
		s.Reason = ReasonStarting
	}
	s.RetryScheduled = e.retryAfter[m] > now
	return s
}
