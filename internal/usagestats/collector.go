package usagestats

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

type Config struct {
	StateDir         string
	ClaudeHome       string
	CodexHome        string
	AntigravityHome  string
	HasClaude        bool
	HasCodex         bool
	HasAntigravity   bool
	Now              func() time.Time
	ScanInterval     time.Duration
}

type Collector struct {
	cfg   Config
	store *store

	scanMu sync.Mutex

	backfilling   atomic.Bool
	scannedDays   atomic.Int32
	totalDays     atomic.Int32
	initialDone   atomic.Bool
	lastScanAtMs  atomic.Int64
	lastScanError sync.Mutex
	scanErrByAg   map[pb.Agent]string
	lastRefreshAt time.Time
	refreshMu     sync.Mutex
}

func New(cfg Config) (*Collector, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.ScanInterval <= 0 {
		cfg.ScanInterval = scanInterval
	}
	dbPath := filepath.Join(cfg.StateDir, "usage-stats.db")
	st, err := openStore(dbPath)
	if err != nil {
		return nil, err
	}
	return &Collector{
		cfg:         cfg,
		store:       st,
		scanErrByAg: map[pb.Agent]string{},
	}, nil
}

func (c *Collector) Close() error {
	if c == nil || c.store == nil {
		return nil
	}
	return c.store.close()
}

func (c *Collector) Run(ctx context.Context) {
	c.scanOnce(ctx)
	ticker := time.NewTicker(c.cfg.ScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.scanOnce(ctx)
		}
	}
}

// RefreshNow scans changed local files on demand (debounced) so UI opens show fresh metrics.
func (c *Collector) RefreshNow(ctx context.Context) {
	if c == nil || c.store == nil {
		return
	}
	c.refreshMu.Lock()
	tooSoon := time.Since(c.lastRefreshAt) < 2*time.Second
	if !tooSoon {
		c.lastRefreshAt = time.Now()
	}
	c.refreshMu.Unlock()
	if tooSoon {
		return
	}
	c.scanOnce(ctx)
}

type logCandidate struct {
	agent pb.Agent
	path  string
	size  int64
	mtime time.Time
}

func (c *Collector) scanOnce(ctx context.Context) {
	if c == nil || c.store == nil {
		return
	}
	c.scanMu.Lock()
	defer c.scanMu.Unlock()

	now := c.cfg.Now()
	cutoff := now.Add(-maxRetentionDays * 24 * time.Hour)
	cutoffMs := cutoff.UnixMilli()
	rawCutoffMs := now.Add(-rawRetentionDays * 24 * time.Hour).UnixMilli()

	var files []logCandidate
	c.setAgentErr(pb.Agent_AGENT_CLAUDE, "")
	c.setAgentErr(pb.Agent_AGENT_CODEX, "")
	c.setAgentErr(pb.Agent_AGENT_ANTIGRAVITY, "")

	if c.cfg.HasClaude && c.cfg.ClaudeHome != "" {
		found, err := discoverClaudeLogs(c.cfg.ClaudeHome, cutoff)
		if err != nil {
			c.setAgentErr(pb.Agent_AGENT_CLAUDE, err.Error())
		} else {
			files = append(files, found...)
		}
	}
	if c.cfg.HasCodex && c.cfg.CodexHome != "" {
		found, err := discoverCodexLogs(c.cfg.CodexHome, cutoff)
		if err != nil {
			c.setAgentErr(pb.Agent_AGENT_CODEX, err.Error())
		} else {
			files = append(files, found...)
		}
	}
	if c.cfg.HasAntigravity && c.cfg.AntigravityHome != "" {
		found, err := discoverAntigravityLogs(c.cfg.AntigravityHome, cutoff)
		if err != nil {
			c.setAgentErr(pb.Agent_AGENT_ANTIGRAVITY, err.Error())
		} else {
			files = append(files, found...)
		}
	}

	// Process newest files first so recent 7d/today data appears immediately during initial backfill.
	sort.Slice(files, func(i, j int) bool {
		return files[i].mtime.After(files[j].mtime)
	})

	isInitial := !c.initialDone.Load()
	totalDays := int32(maxRetentionDays)
	if isInitial && len(files) > 0 {
		oldest := files[len(files)-1].mtime
		spanDays := int32(now.Sub(oldest)/(24*time.Hour)) + 1
		if spanDays < 1 {
			spanDays = 1
		}
		if spanDays > maxRetentionDays {
			spanDays = maxRetentionDays
		}
		totalDays = spanDays
		c.totalDays.Store(totalDays)
		c.scannedDays.Store(0)
		c.backfilling.Store(true)
	}

	for _, fc := range files {
		if ctx.Err() != nil {
			return
		}
		mtimeNs := fc.mtime.UnixNano()
		prev, ok, err := c.store.getCursor(fc.path)
		if err == nil && ok && prev.sizeBytes == fc.size && prev.mtimeNs == mtimeNs {
			c.updateBackfillProgress(now, fc.mtime, isInitial, totalDays)
			continue
		}

		var samples []requestSample
		var nextOff int64
		var lastModel string
		var lastTotal int64 = -1

		switch fc.agent {
		case pb.Agent_AGENT_CLAUDE:
			samples, nextOff, err = scanClaudeFile(fc.path, 0)
		case pb.Agent_AGENT_CODEX:
			samples, nextOff, lastModel, lastTotal, err = scanCodexFile(fc.path)
		case pb.Agent_AGENT_ANTIGRAVITY:
			samples, nextOff, err = scanAntigravityFile(fc.path)
		}
		if err != nil {
			logWarn("scan usage log", err)
			c.updateBackfillProgress(now, fc.mtime, isInitial, totalDays)
			continue
		}

		cur := fileCursor{
			path:            fc.path,
			agent:           fc.agent,
			sizeBytes:       fc.size,
			mtimeNs:         mtimeNs,
			offsetBytes:     nextOff,
			lastModel:       lastModel,
			lastTotalTokens: lastTotal,
		}
		if err := c.store.applyBatch(ctx, cur, samples, cutoffMs, rawCutoffMs); err != nil {
			logWarn("persist usage samples", err)
		}
		c.updateBackfillProgress(now, fc.mtime, isInitial, totalDays)
	}

	_ = c.store.prune(now)
	c.backfilling.Store(false)
	c.scannedDays.Store(totalDays)
	c.initialDone.Store(true)
	c.lastScanAtMs.Store(now.UnixMilli())
}

func (c *Collector) updateBackfillProgress(now, fileMtime time.Time, isInitial bool, totalDays int32) {
	if !isInitial {
		return
	}
	days := int32(now.Sub(fileMtime)/(24*time.Hour)) + 1
	if days < 1 {
		days = 1
	}
	if days > totalDays {
		days = totalDays
	}
	if days > c.scannedDays.Load() {
		c.scannedDays.Store(days)
	}
}

func (c *Collector) setAgentErr(ag pb.Agent, msg string) {
	c.lastScanError.Lock()
	defer c.lastScanError.Unlock()
	if msg == "" {
		delete(c.scanErrByAg, ag)
	} else {
		c.scanErrByAg[ag] = msg
	}
}

func (c *Collector) agentErr(ag pb.Agent) string {
	c.lastScanError.Lock()
	defer c.lastScanError.Unlock()
	return c.scanErrByAg[ag]
}

func discoverClaudeLogs(claudeHome string, cutoff time.Time) ([]logCandidate, error) {
	projectsDir := filepath.Join(claudeHome, "projects")
	if _, err := os.Stat(projectsDir); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var out []logCandidate
	err := filepath.WalkDir(projectsDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.ModTime().Before(cutoff) {
			return nil
		}
		out = append(out, logCandidate{
			agent: pb.Agent_AGENT_CLAUDE,
			path:  path,
			size:  info.Size(),
			mtime: info.ModTime(),
		})
		return nil
	})
	return out, err
}

func discoverCodexLogs(codexHome string, cutoff time.Time) ([]logCandidate, error) {
	sessionsDir := filepath.Join(codexHome, "sessions")
	if _, err := os.Stat(sessionsDir); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var out []logCandidate
	err := filepath.WalkDir(sessionsDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.ModTime().Before(cutoff) {
			return nil
		}
		out = append(out, logCandidate{
			agent: pb.Agent_AGENT_CODEX,
			path:  path,
			size:  info.Size(),
			mtime: info.ModTime(),
		})
		return nil
	})
	return out, err
}
