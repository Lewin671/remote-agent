package daemon

import (
	"context"
	"log/slog"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

// Adapters whose CLI quota can be probed (agent.UsageProbeSource) are probed
// when their log directory shows activity, or once on startup if no record is
// saved. What a CLI's logs look like, what its probe returns and how its
// failures read all belong to the adapter.
const (
	probeLogPollInterval  = 5 * time.Second
	probeLogQuietDebounce = 20 * time.Second
	probeLogMaxWait       = 3 * time.Minute
)

type probeMonitor struct {
	now             func() time.Time
	baseline        agent.LogSnapshot
	baselineReady   bool
	startupChecked  bool
	pending         bool
	firstActivityAt time.Time
	lastActivityAt  time.Time
}

func newProbeMonitor() *probeMonitor {
	return &probeMonitor{now: time.Now}
}

func (m *probeMonitor) clockNow() time.Time {
	if m != nil && m.now != nil {
		return m.now()
	}
	return time.Now()
}

func (q *quotaService) probeLoop(ctx context.Context) {
	monitors := map[pb.Agent]*probeMonitor{}
	step := func() {
		for _, a := range q.adapters() {
			if _, ok := a.(agent.UsageProbeSource); !ok {
				continue
			}
			m := monitors[a.Kind()]
			if m == nil {
				m = newProbeMonitor()
				monitors[a.Kind()] = m
			}
			q.probeStep(ctx, a.Kind(), m)
		}
	}
	step()
	t := time.NewTicker(probeLogPollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			step()
		}
	}
}

func (q *quotaService) probeSource(kind pb.Agent) (agent.UsageProbeSource, bool) {
	ag, ok := q.byKind(kind)
	if !ok || ag == nil || ag.Bin() == "" {
		return nil, false
	}
	src, ok := ag.(agent.UsageProbeSource)
	if !ok {
		return nil, false
	}
	return src, true
}

func (q *quotaService) probeStep(ctx context.Context, kind pb.Agent, m *probeMonitor) {
	if ctx.Err() != nil {
		return
	}
	src, ok := q.probeSource(kind)
	if !ok {
		return
	}
	now := m.clockNow()

	if !m.baselineReady {
		m.baseline, _ = src.ScanLogs(nil)
		m.baselineReady = true
		if !m.startupChecked {
			m.startupChecked = true
			if !q.hasSaved(kind) {
				q.runProbe(ctx, kind, m, src)
			}
		}
		return
	}

	snap, changed := src.ScanLogs(m.baseline)
	m.baseline = snap
	if changed {
		if !m.pending {
			m.pending = true
			m.firstActivityAt = now
		}
		m.lastActivityAt = now
	}

	if !m.pending {
		return
	}
	quietReady := now.Sub(m.lastActivityAt) >= probeLogQuietDebounce
	maxWaitReady := now.Sub(m.firstActivityAt) >= probeLogMaxWait
	if !quietReady && !maxWaitReady {
		return
	}
	q.runProbe(ctx, kind, m, src)
}

func (q *quotaService) runProbe(ctx context.Context, kind pb.Agent, m *probeMonitor, src agent.UsageProbeSource) {
	started := m.clockNow()
	quota, err := src.ProbeUsage(ctx)
	finished := m.clockNow()
	elapsed := finished.Sub(started)
	if elapsed < 0 {
		elapsed = 0
	}

	// Re-baseline after the probe exits so the probe's own log file
	// cannot self-trigger a follow-up probe.
	m.baseline, _ = src.ScanLogs(nil)
	m.baselineReady = true
	m.pending = false

	if ctx.Err() != nil {
		return
	}
	q.recordProbeResult(kind, src, quota, err, finished, elapsed)
}

func (q *quotaService) recordProbeResult(kind pb.Agent, src agent.UsageProbeSource, quota *pb.AgentQuota, err error, now time.Time, elapsed time.Duration) {
	if err != nil {
		reasonClass, detail := src.ClassifyProbeError(err)
		q.setError(kind, detail)
		slog.Warn("usage probe failed", "agent", kind.String(), "reason", reasonClass, "duration_ms", elapsed.Milliseconds())
		return
	}
	if quota == nil {
		return
	}
	q.setError(kind, "")
	// Live probe observation (not an idle status-line replay); credits
	// may recover within the same reset window, so monotonic must be false.
	q.record(src.ProbeSourceID(), quota, now, false)
	slog.Info("usage probe ok", "agent", kind.String(), "duration_ms", elapsed.Milliseconds())
}
