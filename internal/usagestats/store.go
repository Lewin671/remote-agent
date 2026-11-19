// Package usagestats passively scans local CLI session transcripts/rollouts on
// the machine to collect per-(agent, model) token consumption, weighted cache
// hit rates, output speed (tok/s), and time to first token (TTFT).
package usagestats

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	_ "modernc.org/sqlite"
)

const (
	rawRetentionDays   = 7
	maxRetentionDays   = 90
	maxHourlyReservoir = 256
	scanInterval       = 15 * time.Second
)

type requestSample struct {
	dedupKey            string
	sourcePath          string
	agent               pb.Agent
	model               string
	tsMs                int64
	uncachedInput       uint64
	cacheRead           uint64
	cacheWrite          uint64
	output              uint64
	reasoningOutput     uint64
	cacheWriteSupported bool
	speedValid          bool
	generationMs        int64
	speedTokPerSec      float64
	ttftMs              float64
}

type store struct {
	db *sql.DB
	mu sync.Mutex
}

func openStore(dbPath string) (*store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return nil, err
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &store{db: db}
	if err := s.initSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *store) close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *store) initSchema() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS file_cursors (
	path TEXT PRIMARY KEY,
	agent INTEGER NOT NULL,
	size_bytes INTEGER NOT NULL,
	mtime_ns INTEGER NOT NULL,
	offset_bytes INTEGER NOT NULL,
	last_model TEXT NOT NULL DEFAULT '',
	last_total_tokens INTEGER NOT NULL DEFAULT -1
);

CREATE TABLE IF NOT EXISTS raw_requests (
	dedup_key TEXT PRIMARY KEY,
	agent INTEGER NOT NULL,
	model TEXT NOT NULL,
	ts_ms INTEGER NOT NULL,
	hour_start_ms INTEGER NOT NULL,
	uncached_input INTEGER NOT NULL,
	cache_read INTEGER NOT NULL,
	cache_write INTEGER NOT NULL,
	output INTEGER NOT NULL,
	reasoning_output INTEGER NOT NULL,
	cache_write_supported INTEGER NOT NULL,
	speed_valid INTEGER NOT NULL,
	generation_ms INTEGER NOT NULL,
	speed_tok_s REAL NOT NULL,
	ttft_ms REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_raw_requests_ts ON raw_requests(ts_ms, agent, model);

CREATE TABLE IF NOT EXISTS ingested_keys (
	dedup_key TEXT PRIMARY KEY,
	ts_ms INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_ingested_keys_ts ON ingested_keys(ts_ms);

CREATE TABLE IF NOT EXISTS hourly_buckets (
	hour_start_ms INTEGER NOT NULL,
	agent INTEGER NOT NULL,
	model TEXT NOT NULL,
	uncached_input INTEGER NOT NULL,
	cache_read INTEGER NOT NULL,
	cache_write INTEGER NOT NULL,
	output INTEGER NOT NULL,
	reasoning_output INTEGER NOT NULL,
	cache_write_supported INTEGER NOT NULL,
	usage_request_count INTEGER NOT NULL,
	speed_request_count INTEGER NOT NULL,
	speed_samples BLOB NOT NULL DEFAULT X'',
	ttft_samples BLOB NOT NULL DEFAULT X'',
	PRIMARY KEY (hour_start_ms, agent, model)
);
CREATE INDEX IF NOT EXISTS idx_hourly_buckets_hour ON hourly_buckets(hour_start_ms);
`
	_, err := s.db.Exec(ddl)
	return err
}

func hourFloorMs(tsMs int64) int64 {
	const hourMs = int64(3600 * 1000)
	if tsMs >= 0 {
		return (tsMs / hourMs) * hourMs
	}
	return ((tsMs - hourMs + 1) / hourMs) * hourMs
}

type fileCursor struct {
	path            string
	agent           pb.Agent
	sizeBytes       int64
	mtimeNs         int64
	offsetBytes     int64
	lastModel       string
	lastTotalTokens int64
}

func (s *store) getCursor(path string) (fileCursor, bool, error) {
	var c fileCursor
	var ag int32
	err := s.db.QueryRow(
		`SELECT path, agent, size_bytes, mtime_ns, offset_bytes, last_model, last_total_tokens FROM file_cursors WHERE path = ?`,
		path,
	).Scan(&c.path, &ag, &c.sizeBytes, &c.mtimeNs, &c.offsetBytes, &c.lastModel, &c.lastTotalTokens)
	if errors.Is(err, sql.ErrNoRows) {
		return fileCursor{}, false, nil
	}
	if err != nil {
		return fileCursor{}, false, err
	}
	c.agent = pb.Agent(ag)
	return c, true, nil
}

func (s *store) applyBatch(ctx context.Context, cur fileCursor, samples []requestSample, maxCutoffMs, rawCutoffMs int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for _, r := range samples {
		if r.tsMs < maxCutoffMs {
			continue
		}
		if r.model == "" || r.agent == pb.Agent_AGENT_UNSPECIFIED {
			continue
		}
		hr := hourFloorMs(r.tsMs)
		if err := upsertSampleTx(tx, hr, r, rawCutoffMs); err != nil {
			return err
		}
	}

	_, err = tx.Exec(
		`INSERT INTO file_cursors(path, agent, size_bytes, mtime_ns, offset_bytes, last_model, last_total_tokens)
		 VALUES(?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(path) DO UPDATE SET
		   agent=excluded.agent,
		   size_bytes=excluded.size_bytes,
		   mtime_ns=excluded.mtime_ns,
		   offset_bytes=excluded.offset_bytes,
		   last_model=excluded.last_model,
		   last_total_tokens=excluded.last_total_tokens`,
		cur.path, int32(cur.agent), cur.sizeBytes, cur.mtimeNs, cur.offsetBytes, cur.lastModel, cur.lastTotalTokens,
	)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func upsertSampleTx(tx *sql.Tx, hr int64, r requestSample, rawCutoffMs int64) error {
	var oldUncached, oldCacheRead, oldCacheWrite, oldOutput, oldReasoning int64
	var oldCWSup, oldSpeedValid int32
	var oldSpeed, oldTtft float64
	var oldHr int64
	var oldAgent int32
	var oldModel string

	existed := true
	err := tx.QueryRow(
		`SELECT agent, model, hour_start_ms, uncached_input, cache_read, cache_write, output, reasoning_output, cache_write_supported, speed_valid, speed_tok_s, ttft_ms
		 FROM raw_requests WHERE dedup_key = ?`,
		r.dedupKey,
	).Scan(&oldAgent, &oldModel, &oldHr, &oldUncached, &oldCacheRead, &oldCacheWrite, &oldOutput, &oldReasoning, &oldCWSup, &oldSpeedValid, &oldSpeed, &oldTtft)
	if errors.Is(err, sql.ErrNoRows) {
		existed = false
	} else if err != nil {
		return err
	}

	cwFlag := int32(0)
	if r.cacheWriteSupported {
		cwFlag = 1
	}
	svFlag := int32(0)
	if r.speedValid {
		svFlag = 1
	}

	if !existed {
		// Guard against double-counting records >7d old that were pruned from raw_requests
		// when a modified file is re-scanned from offset 0.
		var seen int
		seenErr := tx.QueryRow(`SELECT 1 FROM ingested_keys WHERE dedup_key = ?`, r.dedupKey).Scan(&seen)
		if seenErr == nil {
			return nil
		} else if !errors.Is(seenErr, sql.ErrNoRows) {
			return seenErr
		}
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO ingested_keys(dedup_key, ts_ms) VALUES(?, ?)`,
			r.dedupKey, r.tsMs,
		); err != nil {
			return err
		}
		if r.tsMs >= rawCutoffMs {
			if _, err := tx.Exec(
				`INSERT INTO raw_requests(dedup_key, agent, model, ts_ms, hour_start_ms, uncached_input, cache_read, cache_write, output, reasoning_output, cache_write_supported, speed_valid, generation_ms, speed_tok_s, ttft_ms)
				 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				r.dedupKey, int32(r.agent), r.model, r.tsMs, hr,
				int64(r.uncachedInput), int64(r.cacheRead), int64(r.cacheWrite), int64(r.output), int64(r.reasoningOutput),
				cwFlag, svFlag, r.generationMs, r.speedTokPerSec, r.ttftMs,
			); err != nil {
				return err
			}
		}
		return addHourlyBucketTx(tx, hr, r.agent, r.model,
			int64(r.uncachedInput), int64(r.cacheRead), int64(r.cacheWrite), int64(r.output), int64(r.reasoningOutput),
			1, svFlag, r.cacheWriteSupported, r.speedValid, float32(r.speedTokPerSec), float32(r.ttftMs), false, 0, 0)
	}

	// Skip exact duplicates without churn.
	if oldHr == hr && pb.Agent(oldAgent) == r.agent && oldModel == r.model &&
		uint64(oldUncached) == r.uncachedInput && uint64(oldCacheRead) == r.cacheRead &&
		uint64(oldCacheWrite) == r.cacheWrite && uint64(oldOutput) == r.output &&
		uint64(oldReasoning) == r.reasoningOutput && (oldCWSup == 1) == r.cacheWriteSupported &&
		(oldSpeedValid == 1) == r.speedValid &&
		(!r.speedValid || (math.Abs(oldSpeed-r.speedTokPerSec) < 0.01 && math.Abs(oldTtft-r.ttftMs) < 0.01)) {
		return nil
	}

	if _, err := tx.Exec(
		`UPDATE raw_requests SET agent=?, model=?, ts_ms=?, hour_start_ms=?, uncached_input=?, cache_read=?, cache_write=?, output=?, reasoning_output=?, cache_write_supported=?, speed_valid=?, generation_ms=?, speed_tok_s=?, ttft_ms=?
		 WHERE dedup_key=?`,
		int32(r.agent), r.model, r.tsMs, hr,
		int64(r.uncachedInput), int64(r.cacheRead), int64(r.cacheWrite), int64(r.output), int64(r.reasoningOutput),
		cwFlag, svFlag, r.generationMs, r.speedTokPerSec, r.ttftMs, r.dedupKey,
	); err != nil {
		return err
	}

	if oldHr == hr && pb.Agent(oldAgent) == r.agent && oldModel == r.model {
		dUncached := int64(r.uncachedInput) - oldUncached
		dCacheRead := int64(r.cacheRead) - oldCacheRead
		dCacheWrite := int64(r.cacheWrite) - oldCacheWrite
		dOutput := int64(r.output) - oldOutput
		dReasoning := int64(r.reasoningOutput) - oldReasoning
		dSpeedCount := svFlag - oldSpeedValid
		replaceSpeed := oldSpeedValid == 1
		return addHourlyBucketTx(tx, hr, r.agent, r.model,
			dUncached, dCacheRead, dCacheWrite, dOutput, dReasoning,
			0, dSpeedCount, r.cacheWriteSupported || oldCWSup == 1, r.speedValid, float32(r.speedTokPerSec), float32(r.ttftMs), replaceSpeed, float32(oldSpeed), float32(oldTtft))
	}

	// Reverse old hour bucket contribution and add new one.
	if err := addHourlyBucketTx(tx, oldHr, pb.Agent(oldAgent), oldModel,
		-oldUncached, -oldCacheRead, -oldCacheWrite, -oldOutput, -oldReasoning,
		-1, -oldSpeedValid, oldCWSup == 1, false, 0, 0, oldSpeedValid == 1, float32(oldSpeed), float32(oldTtft)); err != nil {
		return err
	}
	return addHourlyBucketTx(tx, hr, r.agent, r.model,
		int64(r.uncachedInput), int64(r.cacheRead), int64(r.cacheWrite), int64(r.output), int64(r.reasoningOutput),
		1, svFlag, r.cacheWriteSupported, r.speedValid, float32(r.speedTokPerSec), float32(r.ttftMs), false, 0, 0)
}

func addHourlyBucketTx(
	tx *sql.Tx,
	hr int64, ag pb.Agent, model string,
	dUncached, dCacheRead, dCacheWrite, dOutput, dReasoning int64,
	dUsageReq, dSpeedReq int32,
	cwSupported bool,
	addSample bool, newSpeed, newTtft float32,
	removeOld bool, oldSpeed, oldTtft float32,
) error {
	var curUncached, curCacheRead, curCacheWrite, curOutput, curReasoning int64
	var curCWSup, curUsageReq, curSpeedReq int64
	var spBlob, ttBlob []byte
	err := tx.QueryRow(
		`SELECT uncached_input, cache_read, cache_write, output, reasoning_output, cache_write_supported, usage_request_count, speed_request_count, speed_samples, ttft_samples
		 FROM hourly_buckets WHERE hour_start_ms=? AND agent=? AND model=?`,
		hr, int32(ag), model,
	).Scan(&curUncached, &curCacheRead, &curCacheWrite, &curOutput, &curReasoning, &curCWSup, &curUsageReq, &curSpeedReq, &spBlob, &ttBlob)
	if errors.Is(err, sql.ErrNoRows) {
		var spList, ttList []float32
		if addSample {
			spList = []float32{newSpeed}
			ttList = []float32{newTtft}
		}
		cwInt := int64(0)
		if cwSupported {
			cwInt = 1
		}
		_, err = tx.Exec(
			`INSERT INTO hourly_buckets(hour_start_ms, agent, model, uncached_input, cache_read, cache_write, output, reasoning_output, cache_write_supported, usage_request_count, speed_request_count, speed_samples, ttft_samples)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			hr, int32(ag), model,
			max(int64(0), dUncached), max(int64(0), dCacheRead), max(int64(0), dCacheWrite), max(int64(0), dOutput), max(int64(0), dReasoning),
			cwInt, max(int64(0), int64(dUsageReq)), max(int64(0), int64(dSpeedReq)),
			encodeFloat32Slice(spList), encodeFloat32Slice(ttList),
		)
		return err
	}
	if err != nil {
		return err
	}

	spList := decodeFloat32Slice(spBlob)
	ttList := decodeFloat32Slice(ttBlob)
	if removeOld {
		spList = removeFloat32Approx(spList, oldSpeed)
		ttList = removeFloat32Approx(ttList, oldTtft)
	}
	if addSample {
		spList = appendSampleBounded(spList, newSpeed, curSpeedReq+int64(dSpeedReq), hr)
		ttList = appendSampleBounded(ttList, newTtft, curSpeedReq+int64(dSpeedReq), hr)
	}
	if cwSupported {
		curCWSup = 1
	}

	_, err = tx.Exec(
		`UPDATE hourly_buckets SET uncached_input=?, cache_read=?, cache_write=?, output=?, reasoning_output=?, cache_write_supported=?, usage_request_count=?, speed_request_count=?, speed_samples=?, ttft_samples=?
		 WHERE hour_start_ms=? AND agent=? AND model=?`,
		max(int64(0), curUncached+dUncached),
		max(int64(0), curCacheRead+dCacheRead),
		max(int64(0), curCacheWrite+dCacheWrite),
		max(int64(0), curOutput+dOutput),
		max(int64(0), curReasoning+dReasoning),
		curCWSup,
		max(int64(0), curUsageReq+int64(dUsageReq)),
		max(int64(0), curSpeedReq+int64(dSpeedReq)),
		encodeFloat32Slice(spList),
		encodeFloat32Slice(ttList),
		hr, int32(ag), model,
	)
	return err
}

func (s *store) prune(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rawCutoff := now.Add(-rawRetentionDays * 24 * time.Hour).UnixMilli()
	maxCutoff := now.Add(-maxRetentionDays * 24 * time.Hour).UnixMilli()
	hourlyCutoff := hourFloorMs(maxCutoff)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM raw_requests WHERE ts_ms < ?`, rawCutoff); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM hourly_buckets WHERE hour_start_ms < ?`, hourlyCutoff); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM ingested_keys WHERE ts_ms < ?`, maxCutoff); err != nil {
		return err
	}
	return tx.Commit()
}

func encodeFloat32Slice(vals []float32) []byte {
	if len(vals) == 0 {
		return []byte{}
	}
	buf := make([]byte, 4*len(vals))
	for i, v := range vals {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return buf
}

func decodeFloat32Slice(b []byte) []float32 {
	n := len(b) / 4
	if n == 0 {
		return nil
	}
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return out
}

func removeFloat32Approx(vals []float32, target float32) []float32 {
	bestIdx := -1
	bestDiff := float32(1e9)
	for i, v := range vals {
		d := v - target
		if d < 0 {
			d = -d
		}
		if d < bestDiff {
			bestDiff = d
			bestIdx = i
		}
	}
	if bestIdx >= 0 && bestDiff <= 0.1 {
		return append(vals[:bestIdx], vals[bestIdx+1:]...)
	}
	return vals
}

func appendSampleBounded(vals []float32, v float32, totalCount int64, seed int64) []float32 {
	if len(vals) < maxHourlyReservoir {
		return append(vals, v)
	}
	if totalCount <= 0 {
		return vals
	}
	// Deterministic reservoir replacement without math/rand allocation.
	idx := uint64(seed^totalCount*6364136223846793005) % uint64(totalCount)
	if int(idx) < maxHourlyReservoir {
		vals[int(idx)] = v
	}
	return vals
}

func percentileSorted(sorted []float64, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n == 1 {
		return sorted[0]
	}
	pos := p * float64(n-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return sorted[lo]
	}
	frac := pos - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac
}

func computePercentiles(vals []float64) (p10, p50, p90 float64, ok bool) {
	if len(vals) == 0 {
		return 0, 0, 0, false
	}
	cp := make([]float64, len(vals))
	copy(cp, vals)
	sort.Float64s(cp)
	return percentileSorted(cp, 0.10), percentileSorted(cp, 0.50), percentileSorted(cp, 0.90), true
}

func logWarn(msg string, err error) {
	if err != nil {
		slog.Warn(msg, "err", err.Error())
	}
}
