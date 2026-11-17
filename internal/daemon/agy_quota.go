package daemon

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/antigravity"
	"github.com/Lewin671/remote-agent/internal/i18n"
)

const (
	agyLogPollInterval  = 5 * time.Second
	agyLogQuietDebounce = 20 * time.Second
	agyLogMaxProbeWait  = 3 * time.Minute
	agyUsageProbeSource = "agy-usage-probe"
)

type agyFileStamp struct {
	modTime time.Time
	size    int64
}

type agyQuotaMonitor struct {
	now             func() time.Time
	baseline        map[string]agyFileStamp
	baselineReady   bool
	startupChecked  bool
	pending         bool
	firstActivityAt time.Time
	lastActivityAt  time.Time
	latestLogPath   string
}

func newAgyQuotaMonitor() *agyQuotaMonitor {
	return &agyQuotaMonitor{now: time.Now}
}

func (m *agyQuotaMonitor) clockNow() time.Time {
	if m != nil && m.now != nil {
		return m.now()
	}
	return time.Now()
}

func (d *Daemon) agyQuotaLoop(ctx context.Context) {
	m := newAgyQuotaMonitor()
	d.stepAgyQuota(ctx, m)
	t := time.NewTicker(agyLogPollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.stepAgyQuota(ctx, m)
		}
	}
}

func (d *Daemon) agyQuotaSource() (agent.UsageProbeSource, bool) {
	ag, ok := d.byKind(pb.Agent_AGENT_ANTIGRAVITY)
	if !ok || ag == nil || ag.Bin() == "" {
		return nil, false
	}
	src, ok := ag.(agent.UsageProbeSource)
	if !ok {
		return nil, false
	}
	return src, true
}

func (d *Daemon) hasSavedQuota(kind pb.Agent) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, q := range d.quotas {
		if q.Agent == kind {
			return true
		}
	}
	return false
}

func (d *Daemon) stepAgyQuota(ctx context.Context, m *agyQuotaMonitor) {
	if ctx.Err() != nil {
		return
	}
	src, ok := d.agyQuotaSource()
	if !ok {
		return
	}
	logDir := src.LogDir()
	now := m.clockNow()

	if !m.baselineReady {
		m.baseline, _, _ = scanAgyLogDir(logDir, nil)
		m.baselineReady = true
		if !m.startupChecked {
			m.startupChecked = true
			if !d.hasSavedQuota(pb.Agent_AGENT_ANTIGRAVITY) {
				d.runAgyQuotaProbe(ctx, m, src, "")
			}
		}
		return
	}

	snap, changed, newestPath := scanAgyLogDir(logDir, m.baseline)
	m.baseline = snap
	if changed {
		if !m.pending {
			m.pending = true
			m.firstActivityAt = now
		}
		m.lastActivityAt = now
		if newestPath != "" {
			m.latestLogPath = newestPath
		}
	}

	if !m.pending {
		return
	}
	quietReady := now.Sub(m.lastActivityAt) >= agyLogQuietDebounce
	maxWaitReady := now.Sub(m.firstActivityAt) >= agyLogMaxProbeWait
	if !quietReady && !maxWaitReady {
		return
	}
	triggerLog := m.latestLogPath
	d.runAgyQuotaProbe(ctx, m, src, triggerLog)
}

func (d *Daemon) runAgyQuotaProbe(ctx context.Context, m *agyQuotaMonitor, src agent.UsageProbeSource, triggerLog string) {
	started := m.clockNow()
	q, err := src.ProbeUsage(ctx, triggerLog)
	finished := m.clockNow()
	elapsed := finished.Sub(started)
	if elapsed < 0 {
		elapsed = 0
	}

	// Re-baseline after the probe exits so the probe's own cli-*.log file
	// cannot self-trigger a follow-up probe.
	m.baseline, _, _ = scanAgyLogDir(src.LogDir(), nil)
	m.baselineReady = true
	m.pending = false
	m.latestLogPath = ""

	if ctx.Err() != nil {
		return
	}
	d.recordAgyProbeResult(q, err, finished, elapsed)
}

func (d *Daemon) recordAgyProbeResult(q *pb.AgentQuota, err error, now time.Time, elapsed time.Duration) {
	if err != nil {
		reasonClass, detail := classifyAgyProbeError(err)
		d.mu.Lock()
		if d.quotaErrors == nil {
			d.quotaErrors = map[pb.Agent]string{}
		}
		d.quotaErrors[pb.Agent_AGENT_ANTIGRAVITY] = detail
		d.mu.Unlock()
		slog.Warn("agy usage probe failed", "reason", reasonClass, "duration_ms", elapsed.Milliseconds())
		return
	}
	if q == nil {
		return
	}
	d.mu.Lock()
	if d.quotaErrors != nil {
		delete(d.quotaErrors, pb.Agent_AGENT_ANTIGRAVITY)
	}
	d.mu.Unlock()
	// Live `/usage` RPC observation (not an idle status-line replay); credits
	// may recover within the same reset window, so monotonic must be false.
	d.recordQuotaWithPolicy(agyUsageProbeSource, q, now, false)
	slog.Info("agy usage probe ok", "duration_ms", elapsed.Milliseconds())
}

func classifyAgyProbeError(err error) (reasonClass, detail string) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout", i18n.QuotaAgyProbeTimeout("zh-CN")
	case errors.Is(err, antigravity.ErrUsageNotLoggedIn):
		return "not_logged_in", i18n.QuotaAgyProbeNotLoggedIn("zh-CN")
	case errors.Is(err, antigravity.ErrUsageUnsupported):
		return "unsupported", i18n.QuotaAgyProbeUnsupported("zh-CN")
	case errors.Is(err, antigravity.ErrUsageOutputLimit):
		return "output_too_large", i18n.QuotaAgyProbeFailed("zh-CN")
	default:
		return "failed", i18n.QuotaAgyProbeFailed("zh-CN")
	}
}

func scanAgyLogDir(logDir string, prev map[string]agyFileStamp) (snap map[string]agyFileStamp, changed bool, newestChangedPath string) {
	snap = map[string]agyFileStamp{}
	if logDir == "" {
		return snap, false, ""
	}
	entries, err := os.ReadDir(logDir)
	if err != nil {
		return snap, false, ""
	}
	var newestTime time.Time
	var newestName string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "cli-") || !strings.HasSuffix(name, ".log") {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		st := agyFileStamp{modTime: info.ModTime(), size: info.Size()}
		snap[name] = st
		if prev == nil {
			continue
		}
		old, ok := prev[name]
		if !ok || !old.modTime.Equal(st.modTime) || old.size != st.size {
			changed = true
			if newestName == "" || st.modTime.After(newestTime) || (st.modTime.Equal(newestTime) && name > newestName) {
				newestTime = st.modTime
				newestName = name
			}
		}
	}
	if newestName != "" {
		newestChangedPath = filepath.Join(logDir, newestName)
	}
	return snap, changed, newestChangedPath
}
