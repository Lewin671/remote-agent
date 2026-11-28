// Package latency implements pure connection-check latency splitting rules:
// median sampling, splitting each probe into network, computer and local time, per-segment slow thresholds,
// computer queue-vs-execute attribution, and thousands-separated ms formatting.
package latency

import (
	"math"
	"strconv"

	"github.com/Lewin671/remote-agent/clientcore/domain/sortutil"
)

// Initial connection-check latency thresholds and probe parameters.
// All thresholds are initial values to be tuned against real-world measurements.
const (
	// Per-segment slow thresholds (milliseconds, inclusive >=).
	SlowDirectNetworkMs int64 = 500
	SlowRelayNetworkMs  int64 = 800
	SlowComputerMs      int64 = 500
	SlowWaitDirectMs    int64 = 500
	SlowLocalMs         int64 = 500
	// Fallback total-latency slow threshold (milliseconds, inclusive >=).
	SlowTotalMs int64 = 1500

	// Probe timeouts and sampling windows (milliseconds / counts).
	EchoTimeoutMs         int64 = 5_000
	WorkTimeoutMs         int64 = 15_000
	DirectSampleWaitMs    int64 = 3_000
	RelayEchoSamples      int   = 3
	WorkSamples           int   = 5
	DirectProbeMaxSamples int   = 5
	DirectProbeWindowMs   int64 = 10_000
)

// ComputerSlowCause identifies the dominant contributor when computer processing is slow.
type ComputerSlowCause uint8

const (
	CauseNone ComputerSlowCause = iota
	CauseQueue
	CauseExecute
)

// EvalInput holds the measured millisecond values for one machine check.
type EvalInput struct {
	HasNetwork      bool
	NetworkMs       int64
	NetworkIsDirect bool

	HasComputer bool
	ComputerMs  int64
	QueueMs     int64
	ExecuteMs   int64

	HasWaitDirect bool
	WaitDirectMs  int64

	HasLocal bool
	LocalMs  int64

	HasTotal bool
	TotalMs  int64
}

// EvalResult holds the per-segment and overall slow verdicts plus computer cause.
type EvalResult struct {
	NetworkSlow     bool
	ComputerSlow    bool
	WaitDirectSlow  bool
	LocalSlow       bool
	TotalSlow       bool
	ComputerCause   ComputerSlowCause
	ComputerCauseMs int64
}

// MedianInt64 returns the middle element (upper middle for even length) of a non-empty slice.
// Matches routing.go and web ConnCheck median convention.
func MedianInt64(samples []int64) (int64, bool) {
	if len(samples) == 0 {
		return 0, false
	}
	s := make([]int64, len(samples))
	copy(s, samples)
	sortutil.Int64s(s)
	return s[len(s)/2], true
}

// MedianFloat64Ms returns the rounded millisecond median of float64 ms samples.
func MedianFloat64Ms(samples []float64) (int64, bool) {
	if len(samples) == 0 {
		return 0, false
	}
	s := make([]float64, len(samples))
	copy(s, samples)
	sortutil.Float64s(s)
	v := s[len(s)/2]
	if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, true
	}
	return int64(math.Round(v)), true
}

// MicrosToMs rounds a microsecond duration to milliseconds, clamping negatives to 0.
func MicrosToMs(us int64) int64 {
	if us <= 0 {
		return 0
	}
	return (us + 500) / 1000
}

// NetRoundTripMs computes wire round-trip minus daemon self-reported processing time, clamped at 0.
func NetRoundTripMs(wireElapsedMs float64, daemonProcessUs int64) int64 {
	if math.IsNaN(wireElapsedMs) || math.IsInf(wireElapsedMs, 0) || wireElapsedMs <= 0 {
		return 0
	}
	procMs := float64(max(int64(0), daemonProcessUs)) / 1000.0
	diff := wireElapsedMs - procMs
	if diff <= 0 {
		return 0
	}
	return int64(math.Round(diff))
}

// NetworkThresholdMs returns the slow threshold for the given network route.
func NetworkThresholdMs(direct bool) int64 {
	if direct {
		return SlowDirectNetworkMs
	}
	return SlowRelayNetworkMs
}

// EvaluateComputerCause determines whether computer processing is slow and which phase dominated.
func EvaluateComputerCause(processMs, queueMs, executeMs int64) (slow bool, cause ComputerSlowCause, causeMs int64) {
	if processMs < SlowComputerMs {
		return false, CauseNone, 0
	}
	if queueMs > executeMs {
		return true, CauseQueue, max(int64(0), queueMs)
	}
	return true, CauseExecute, max(int64(0), executeMs)
}

// Evaluate computes slow verdicts for each segment and the overall total row.
func Evaluate(in EvalInput) EvalResult {
	var out EvalResult
	if in.HasNetwork {
		out.NetworkSlow = in.NetworkMs >= NetworkThresholdMs(in.NetworkIsDirect)
	}
	if in.HasComputer {
		out.ComputerSlow, out.ComputerCause, out.ComputerCauseMs = EvaluateComputerCause(in.ComputerMs, in.QueueMs, in.ExecuteMs)
	}
	if in.HasWaitDirect {
		out.WaitDirectSlow = in.WaitDirectMs >= SlowWaitDirectMs
	}
	if in.HasLocal {
		out.LocalSlow = in.LocalMs >= SlowLocalMs
	}
	out.TotalSlow = out.NetworkSlow || out.ComputerSlow || out.WaitDirectSlow || out.LocalSlow || (in.HasTotal && in.TotalMs >= SlowTotalMs)
	return out
}

// FormatMs formats a non-negative millisecond value with ASCII comma thousands separators.
func FormatMs(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	raw := strconv.FormatInt(ms, 10)
	n := len(raw)
	if n <= 3 {
		return raw
	}
	commas := (n - 1) / 3
	buf := make([]byte, n+commas)
	dst := len(buf) - 1
	digits := 0
	for i := n - 1; i >= 0; i-- {
		if digits == 3 {
			buf[dst] = ','
			dst--
			digits = 0
		}
		buf[dst] = raw[i]
		dst--
		digits++
	}
	return string(buf)
}

// ClampNetworkMs keeps the network figure within the measured total: it is
// at most totalMs minus the computer's own processing time, and never
// negative.
func ClampNetworkMs(networkMs, totalMs, computerMs int64) int64 {
	limit := max(int64(0), totalMs-max(int64(0), computerMs))
	return min(max(int64(0), networkMs), limit)
}

// WorkSample is one processing probe's measurements, split so the parts add
// up to the total: NetworkMs + ComputerMs + RestMs == TotalMs. RestMs is time
// spent on this device (sealing, scheduling, result handling) or, for a
// rerouted probe, waiting for direct before the relay retry.
type WorkSample struct {
	TotalMs    int64
	NetworkMs  int64
	ComputerMs int64
	QueueMs    int64
	ExecuteMs  int64
	RestMs     int64

	NetworkIsDirect bool
	Rerouted        bool
}

// SplitWork splits one processing probe. totalMs is the client's wall time
// from submit to settle, wireElapsedMs the winning attempt's transport time
// (0 when unknown), processUs the daemon's self-reported processing time.
func SplitWork(totalMs int64, wireElapsedMs float64, processUs int64) (networkMs, computerMs, restMs int64) {
	computerMs = MicrosToMs(processUs)
	totalMs = max(totalMs, computerMs)
	if math.IsNaN(wireElapsedMs) || math.IsInf(wireElapsedMs, 0) || wireElapsedMs <= 0 {
		wireElapsedMs = float64(totalMs)
	}
	networkMs = ClampNetworkMs(NetRoundTripMs(wireElapsedMs, processUs), totalMs, computerMs)
	restMs = totalMs - computerMs - networkMs
	return networkMs, computerMs, restMs
}

// PickSample returns the index of the sample to show: a rerouted probe when
// there is one (it explains the slow experience), otherwise the probe with
// the median total (upper middle for even counts).
func PickSample(samples []WorkSample) (int, bool) {
	if len(samples) == 0 {
		return 0, false
	}
	for i, s := range samples {
		if s.Rerouted {
			return i, true
		}
	}
	idx := make([]int, len(samples))
	for i := range idx {
		idx[i] = i
	}
	// Stable insertion sort by total keeps equal totals in run order.
	for i := 1; i < len(idx); i++ {
		for j := i; j > 0 && samples[idx[j]].TotalMs < samples[idx[j-1]].TotalMs; j-- {
			idx[j], idx[j-1] = idx[j-1], idx[j]
		}
	}
	return idx[len(idx)/2], true
}

// TotalRange returns the smallest and largest totals across samples.
func TotalRange(samples []WorkSample) (minMs, maxMs int64, ok bool) {
	if len(samples) == 0 {
		return 0, 0, false
	}
	minMs, maxMs = samples[0].TotalMs, samples[0].TotalMs
	for _, s := range samples[1:] {
		minMs = min(minMs, s.TotalMs)
		maxMs = max(maxMs, s.TotalMs)
	}
	return minMs, maxMs, true
}
