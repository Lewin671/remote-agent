package linkcore

const (
	// ProbeIntervalMs is the cadence for direct DataChannel liveness probes.
	ProbeIntervalMs int64 = 1_000
	// ProbeTimeoutMs is how long a sent probe waits for an echo before counting as missed.
	ProbeTimeoutMs int64 = 1_000
	// ProbeMaxMissed is the consecutive missed-probe count that closes the direct PC.
	ProbeMaxMissed = 3
	// ProbeFrameBytes is the fixed size of an opaque echo probe frame: [kind, 16-byte nonce].
	ProbeFrameBytes = 17
	// probeExpiredKeep bounds recently timed-out probes kept to classify late echoes.
	probeExpiredKeep = 8
	// probeExpiredTTLMs drops timed-out probe records older than 10 seconds.
	probeExpiredTTLMs int64 = 10_000
)

type pendingProbe struct {
	id     [16]byte
	sentAt int64
}

// ProbeStats holds lifetime direct DataChannel liveness probe counters since the last Reset.
type ProbeStats struct {
	Sent        int
	Echoed      int
	EchoMaxMs   int64
	Late        int
	LateMaxMs   int64
	Unmatched   int
	Lost        int
	Missed      int
	HasLastEcho bool
	LastEchoAt  int64
}

// ProbeTracker is the sans-IO direct DataChannel liveness state machine.
// It owns probe cadence, pending-probe timeouts, and the 3-miss close rule;
// platform shells only reflect incoming kind=0 echoes and send/forward raw 17-byte frames.
type ProbeTracker struct {
	lastSentAt  int64
	hasSent     bool
	missed      int
	sent        int
	echoed      int
	echoMaxMs   int64
	late        int
	lateMaxMs   int64
	unmatched   int
	lost        int
	hasLastEcho bool
	lastEchoAt  int64
	pending     []pendingProbe
	expired     []pendingProbe
}

// Reset clears all pending probes and liveness counters.
func (p *ProbeTracker) Reset() {
	p.lastSentAt = 0
	p.hasSent = false
	p.missed = 0
	p.sent = 0
	p.echoed = 0
	p.echoMaxMs = 0
	p.late = 0
	p.lateMaxMs = 0
	p.unmatched = 0
	p.lost = 0
	p.hasLastEcho = false
	p.lastEchoAt = 0
	p.pending = nil
	p.expired = nil
}

// Missed returns the current consecutive missed-probe count.
func (p *ProbeTracker) Missed() int { return p.missed }

// Stats returns lifetime probe counters and the current consecutive missed-probe
// count since the last Reset.
func (p *ProbeTracker) Stats() ProbeStats {
	return ProbeStats{
		Sent:        p.sent,
		Echoed:      p.echoed,
		EchoMaxMs:   p.echoMaxMs,
		Late:        p.late,
		LateMaxMs:   p.lateMaxMs,
		Unmatched:   p.unmatched,
		Lost:        p.lost,
		Missed:      p.missed,
		HasLastEcho: p.hasLastEcho,
		LastEchoAt:  p.lastEchoAt,
	}
}

// PendingCount returns the number of in-flight probes awaiting echo.
func (p *ProbeTracker) PendingCount() int { return len(p.pending) }

func (p *ProbeTracker) pruneExpired(now int64) {
	if len(p.expired) == 0 {
		return
	}
	kept := p.expired[:0]
	for _, item := range p.expired {
		if now-item.sentAt <= probeExpiredTTLMs {
			kept = append(kept, item)
		}
	}
	p.expired = kept
}

func (p *ProbeTracker) rememberExpired(item pendingProbe) {
	p.expired = append(p.expired, item)
	if len(p.expired) > probeExpiredKeep {
		p.expired = append([]pendingProbe(nil), p.expired[len(p.expired)-probeExpiredKeep:]...)
	}
}

// OnReply processes a received 17-byte probe echo frame (raw[0] == 1).
// Returns the measured RTT in ms and ok=true if it matched an in-flight probe.
// Late replies (matching a recently timed-out probe) and unmatched replies are
// counted for diagnostics without resetting missed or affecting close decisions.
func (p *ProbeTracker) OnReply(now int64, raw []byte) (rttMs float64, ok bool) {
	if len(raw) != ProbeFrameBytes || raw[0] != 1 {
		return 0, false
	}
	p.hasLastEcho = true
	p.lastEchoAt = now
	p.pruneExpired(now)
	var id [16]byte
	copy(id[:], raw[1:17])
	for i, item := range p.pending {
		if item.id == id {
			p.pending = append(p.pending[:i], p.pending[i+1:]...)
			p.missed = 0
			p.echoed++
			elapsed := now - item.sentAt
			if elapsed < 0 {
				elapsed = 0
			}
			if elapsed > p.echoMaxMs {
				p.echoMaxMs = elapsed
			}
			return float64(elapsed), true
		}
	}
	for i, item := range p.expired {
		if item.id == id {
			p.expired = append(p.expired[:i], p.expired[i+1:]...)
			p.late++
			if p.lost > 0 {
				p.lost--
			}
			elapsed := now - item.sentAt
			if elapsed < 0 {
				elapsed = 0
			}
			if elapsed > p.lateMaxMs {
				p.lateMaxMs = elapsed
			}
			return 0, false
		}
	}
	p.unmatched++
	return 0, false
}

// Tick expires timed-out probes and, when ready and due, emits the next 17-byte
// kind=0 probe frame using allocNonce. timedOut becomes true once ProbeMaxMissed
// probes have expired without any reply.
func (p *ProbeTracker) Tick(now int64, ready bool, allocNonce func() ([16]byte, bool)) (sendFrame []byte, timedOut bool) {
	p.pruneExpired(now)
	if len(p.pending) > 0 {
		kept := p.pending[:0]
		for i, item := range p.pending {
			if now-item.sentAt >= ProbeTimeoutMs {
				p.missed++
				p.lost++
				p.rememberExpired(item)
				if p.missed >= ProbeMaxMissed {
					// The path closes now; remember the probes still in flight so their
					// echoes count as late rather than unmatched.
					rest := append(append([]pendingProbe(nil), kept...), p.pending[i+1:]...)
					for _, r := range rest {
						p.rememberExpired(r)
					}
					p.pending = nil
					return nil, true
				}
				continue
			}
			kept = append(kept, item)
		}
		p.pending = kept
	}
	if !ready {
		return nil, false
	}
	if p.hasSent && now-p.lastSentAt < ProbeIntervalMs {
		return nil, false
	}
	if allocNonce == nil {
		return nil, false
	}
	id, ok := allocNonce()
	if !ok {
		return nil, false
	}
	p.lastSentAt = now
	p.hasSent = true
	p.sent++
	p.pending = append(p.pending, pendingProbe{id: id, sentAt: now})
	frame := make([]byte, ProbeFrameBytes)
	frame[0] = 0
	copy(frame[1:], id[:])
	return frame, false
}

// NextDue returns the earliest timestamp at which Tick has timed work.
func (p *ProbeTracker) NextDue(ready bool) (int64, bool) {
	var due int64
	hasDue := false
	add := func(t int64) {
		if !hasDue || t < due {
			due, hasDue = t, true
		}
	}
	for _, item := range p.pending {
		add(item.sentAt + ProbeTimeoutMs)
	}
	if ready {
		if !p.hasSent {
			add(0)
		} else {
			add(p.lastSentAt + ProbeIntervalMs)
		}
	}
	return due, hasDue
}
