package transport

import "strings"

// Budgets for signaling over the main connection (docs/transport.md 「非持久双向信令」).
const (
	SignalFramesPerSec = 256     // outgoing frames, also the burst
	SignalBytesPerSec  = 4 << 20 // outgoing bytes, at most one second's worth held
	InboundFrameMax    = 65536   // one incoming frame
	InboundClass2Max   = 16384   // one incoming class-2 (bound) frame
	InboundFrames      = 64      // frames being opened at once
	InboundBytes       = 4 << 20
	InboundPeerFrames  = 8 // per sending computer, so one cannot starve another
	InboundPeerBytes   = 1 << 20
)

// StunServers is the STUN list a device asks for its public address: a
// deployment's own (comma separated, at most two, so one entry can be an IPv6
// literal) or else the relay's own. No third-party service is used.
func StunServers(host, configured string) []string {
	var list []string
	for _, u := range strings.Split(configured, ",") {
		if u = strings.TrimSpace(u); u != "" && len(list) < 2 {
			list = append(list, u)
		}
	}
	if len(list) > 0 {
		return list
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return []string{"stun:" + host + ":3478"}
}

type usage struct{ frames, bytes int }

type inbound struct {
	frames, bytes int
	per           map[string]*usage
}

// AllowSignal charges one outgoing signaling frame of n bytes for the round.
// A refused frame is not sent; the session retries it later.
func (e *Engine) AllowSignal(now int64, id RoundID, n int) bool {
	p := e.rounds[id]
	if p == nil || !e.valid(p, now) || !e.legReady {
		return false
	}
	t, ok := e.signal.Take(now, n, SignalFramesPerSec, SignalBytesPerSec, SignalFramesPerSec)
	e.signal = t
	return ok
}

// AdmitSignal decides whether a frame that arrived on the main connection
// from source may be opened now; class is its outer frame class. An admitted
// frame is held until SignalDone with the same source and size.
func (e *Engine) AdmitSignal(now int64, source string, size, class int) bool {
	in := &e.inbound
	if !e.foreground || !e.legReady || !e.current(now) || size > InboundFrameMax || in.frames >= InboundFrames || in.bytes+size > InboundBytes {
		return false
	}
	if class < 1 || class > 2 || class == 2 && size > InboundClass2Max || !e.isMachine(source) {
		return false
	}
	if in.per == nil {
		in.per = map[string]*usage{}
	}
	u := in.per[source]
	if u == nil {
		u = &usage{}
		in.per[source] = u
	}
	if u.frames >= InboundPeerFrames || u.bytes+size > InboundPeerBytes {
		if u.frames == 0 {
			delete(in.per, source)
		}
		return false
	}
	u.frames++
	u.bytes += size
	in.frames++
	in.bytes += size
	return true
}

// SignalDone releases an admitted frame.
func (e *Engine) SignalDone(source string, size int) {
	in := &e.inbound
	u := in.per[source]
	if u == nil {
		return
	}
	u.frames--
	u.bytes -= size
	in.frames--
	in.bytes -= size
	if u.frames <= 0 {
		delete(in.per, source)
	}
}
