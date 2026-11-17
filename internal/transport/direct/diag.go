package direct

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// pathDiag keeps what is needed to say, once, how a path ended: the first
// reason given and when each stage was reached, in milliseconds after the path
// was made. Only fixed words and small counts; never an address, SDP, ICE
// credential or payload. One log line per path, so the volume is bounded by
// the number of paths, which the pool and the retry back-off already bound.
type pathDiag struct {
	born                       time.Time
	reasonOnce                 sync.Once
	reason                     string
	iceMs, dtlsMs, openMs, cfm atomic.Int64 // 0 = not reached, else ms+1
	probeMs                    atomic.Int64 // first probe from the peer, ms+1
	echoed, unready            atomic.Int64 // probes answered / dropped because this side was not yet confirmed
	iceState, connState        atomic.Value // last state word
}

func (d *pathDiag) since() int64      { return time.Since(d.born).Milliseconds() + 1 }
func (d *pathDiag) why(reason string) { d.reasonOnce.Do(func() { d.reason = reason }) }
func (d *pathDiag) ice(state string) {
	d.iceState.Store(state)
	if state == "connected" || state == "completed" {
		d.iceMs.CompareAndSwap(0, d.since())
	}
}
func (d *pathDiag) conn(state string) {
	d.connState.Store(state)
	if state == "connected" {
		d.dtlsMs.CompareAndSwap(0, d.since())
	}
}
func (d *pathDiag) probe(answered bool) {
	d.probeMs.CompareAndSwap(0, d.since())
	if answered {
		d.echoed.Add(1)
	} else {
		d.unready.Add(1)
	}
}
func shortPeerID(id string) string {
	if len(id) <= 4 {
		return id
	}
	return "…" + id[len(id)-4:]
}

func (d *pathDiag) open()    { d.openMs.CompareAndSwap(0, d.since()) }
func (d *pathDiag) confirm() { d.cfm.CompareAndSwap(0, d.since()) }
func (d *pathDiag) end(use, generation uint64, peer string) {
	d.why("closed_by_owner")
	word := func(v *atomic.Value) string {
		if s, ok := v.Load().(string); ok {
			return s
		}
		return "none"
	}
	slog.Info("direct path ended", "peer", shortPeerID(peer), "use", use, "generation", generation, "reason", d.reason, "at_ms", d.since()-1,
		"ice_ms", d.iceMs.Load()-1, "dtls_ms", d.dtlsMs.Load()-1, "channel_ms", d.openMs.Load()-1, "confirm_ms", d.cfm.Load()-1,
		"probe_ms", d.probeMs.Load()-1, "probes_echoed", d.echoed.Load(), "probes_unready", d.unready.Load(),
		"ice", word(&d.iceState), "conn", word(&d.connState))
}
