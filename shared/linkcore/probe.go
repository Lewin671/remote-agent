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
)

type pendingProbe struct {
	id     [16]byte
	sentAt int64
}

// ProbeTracker is the sans-IO direct DataChannel liveness state machine.
// It owns probe cadence, pending-probe timeouts, and the 3-miss close rule;
// platform shells only reflect incoming kind=0 echoes and send/forward raw 17-byte frames.
type ProbeTracker struct {
	lastSentAt int64
	hasSent    bool
	missed     int
	pending    []pendingProbe
}

// Reset clears all pending probes and liveness counters.
func (p *ProbeTracker) Reset() {
	p.lastSentAt = 0
	p.hasSent = false
	p.missed = 0
	p.pending = nil
}

// Missed returns the current consecutive missed-probe count.
func (p *ProbeTracker) Missed() int { return p.missed }

// PendingCount returns the number of in-flight probes awaiting echo.
func (p *ProbeTracker) PendingCount() int { return len(p.pending) }

// OnReply processes a received 17-byte probe echo frame (raw[0] == 1).
// Returns the measured RTT in ms and ok=true if it matched an in-flight probe.
func (p *ProbeTracker) OnReply(now int64, raw []byte) (rttMs float64, ok bool) {
	if len(raw) != ProbeFrameBytes || raw[0] != 1 {
		return 0, false
	}
	var id [16]byte
	copy(id[:], raw[1:17])
	for i, item := range p.pending {
		if item.id == id {
			p.pending = append(p.pending[:i], p.pending[i+1:]...)
			p.missed = 0
			elapsed := now - item.sentAt
			if elapsed < 0 {
				elapsed = 0
			}
			return float64(elapsed), true
		}
	}
	return 0, false
}

// Tick expires timed-out probes and, when ready and due, emits the next 17-byte
// kind=0 probe frame using allocNonce. timedOut becomes true once ProbeMaxMissed
// probes have expired without any reply.
func (p *ProbeTracker) Tick(now int64, ready bool, allocNonce func() ([16]byte, bool)) (sendFrame []byte, timedOut bool) {
	if len(p.pending) > 0 {
		kept := p.pending[:0]
		for _, item := range p.pending {
			if now-item.sentAt >= ProbeTimeoutMs {
				p.missed++
				if p.missed >= ProbeMaxMissed {
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
