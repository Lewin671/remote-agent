package direct

import (
	"log/slog"
	"math"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"
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
	iceMs, dtlsMs, openMs, cfm atomic.Int64  // 0 = not reached, else ms+1
	probeMs                    atomic.Int64  // first probe from the peer, ms+1
	echoed, unready            atomic.Int64  // probes answered / dropped because the path was no longer valid
	echoSendErr                atomic.Int64  // dc1 echo Send errors
	dc1BufMax, dc0BufMax       atomic.Uint64 // max buffered bytes seen on dc1/dc0 when sending an echo
	cwndMin                    atomic.Uint64 // minimum positive SCTP cwnd observed during lifetime (0 = none)
	txFrames, txBytes          atomic.Int64  // confirmed dc0 frames and bytes sent by daemon
	lastRxMs                   atomic.Int64  // last DataChannel rx from peer, ms+1 (0 = never)
	pairChanges                atomic.Int64  // OnSelectedCandidatePairChange count
	pairTag                    atomic.Value  // last selected pair tag (e.g. v6host-v6prflx)
	iceState, connState        atomic.Value  // last state word
}

type endSnapshot struct {
	dc1Buffered uint64
	dc0Buffered uint64
	sctpSrttMs  int64
	sctpCwnd    uint32
	sctpRwnd    uint32
	pair        string
}

func atomicMaxUint64(dst *atomic.Uint64, v uint64) {
	for {
		cur := dst.Load()
		if v <= cur {
			return
		}
		if dst.CompareAndSwap(cur, v) {
			return
		}
	}
}

func atomicMinPosUint64(dst *atomic.Uint64, v uint64) {
	if v == 0 {
		return
	}
	for {
		cur := dst.Load()
		if cur != 0 && v >= cur {
			return
		}
		if dst.CompareAndSwap(cur, v) {
			return
		}
	}
}

func formatCandTag(c *webrtc.ICECandidate) string {
	if c == nil {
		return "none"
	}
	addr := strings.TrimSpace(c.Address)
	if i := strings.IndexByte(addr, '%'); i >= 0 {
		addr = addr[:i]
	}
	fam := "unk"
	if ip := net.ParseIP(addr); ip != nil {
		if ip.To4() != nil {
			fam = "v4"
		} else {
			fam = "v6"
		}
	} else if strings.Contains(addr, ":") {
		fam = "v6"
	}
	var typ string
	switch c.Typ {
	case webrtc.ICECandidateTypeHost:
		typ = "host"
	case webrtc.ICECandidateTypeSrflx:
		typ = "srflx"
	case webrtc.ICECandidateTypePrflx:
		typ = "prflx"
	case webrtc.ICECandidateTypeRelay:
		typ = "relay"
	default:
		typ = "other"
	}
	return fam + typ
}

func formatPairTag(pair *webrtc.ICECandidatePair) string {
	if pair == nil || (pair.Local == nil && pair.Remote == nil) {
		return "none"
	}
	return formatCandTag(pair.Local) + "-" + formatCandTag(pair.Remote)
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
func (d *pathDiag) echoSent(failed bool, dc1Buf, dc0Buf uint64) {
	if failed {
		d.echoSendErr.Add(1)
	}
	atomicMaxUint64(&d.dc1BufMax, dc1Buf)
	atomicMaxUint64(&d.dc0BufMax, dc0Buf)
}
func (d *pathDiag) sampleCwnd(cwnd uint32) {
	if cwnd > 0 {
		atomicMinPosUint64(&d.cwndMin, uint64(cwnd))
	}
}
func (d *pathDiag) txFrame(n int) {
	d.txFrames.Add(1)
	if n > 0 {
		d.txBytes.Add(int64(n))
	}
}
func (d *pathDiag) rx() {
	d.lastRxMs.Store(d.since())
}
func (d *pathDiag) pairChanged(tag string) {
	d.pairChanges.Add(1)
	if tag != "" {
		d.pairTag.Store(tag)
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
func (d *pathDiag) end(use, generation uint64, peer string, snap endSnapshot) {
	d.why("closed_by_owner")
	word := func(v *atomic.Value) string {
		if s, ok := v.Load().(string); ok {
			return s
		}
		return "none"
	}
	endSince := d.since()
	lastRxAgo := int64(-1)
	if last := d.lastRxMs.Load(); last > 0 {
		lastRxAgo = max(0, endSince-last)
	}
	pair := snap.pair
	if pair == "" || pair == "none" {
		if prev := word(&d.pairTag); prev != "none" {
			pair = prev
		} else {
			pair = "none"
		}
	}
	slog.Info("direct path ended", "peer", shortPeerID(peer), "use", use, "generation", generation, "reason", d.reason, "at_ms", endSince-1,
		"ice_ms", d.iceMs.Load()-1, "dtls_ms", d.dtlsMs.Load()-1, "channel_ms", d.openMs.Load()-1, "confirm_ms", d.cfm.Load()-1,
		"probe_ms", d.probeMs.Load()-1, "probes_echoed", d.echoed.Load(), "probes_unready", d.unready.Load(),
		"ice", word(&d.iceState), "conn", word(&d.connState),
		"echo_send_err", d.echoSendErr.Load(),
		"dc1_buffered", snap.dc1Buffered, "dc0_buffered", snap.dc0Buffered,
		"dc1_buffered_max", d.dc1BufMax.Load(), "dc0_buffered_max", d.dc0BufMax.Load(),
		"sctp_srtt_ms", snap.sctpSrttMs, "sctp_cwnd", snap.sctpCwnd, "sctp_rwnd", snap.sctpRwnd,
		"sctp_cwnd_min", uint32(d.cwndMin.Load()),
		"dc0_tx_frames", d.txFrames.Load(), "dc0_tx_bytes", d.txBytes.Load(),
		"last_rx_ms", lastRxAgo, "pair", pair, "pair_changes", d.pairChanges.Load())
}

func srttSecondsToMs(sec float64) int64 {
	if sec <= 0 || math.IsNaN(sec) || math.IsInf(sec, 0) {
		return -1
	}
	return int64(math.Round(sec * 1000))
}
