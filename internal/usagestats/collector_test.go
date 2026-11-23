package usagestats

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestClaudeParserDedupAndThinkingInclusion(t *testing.T) {
	dir := t.TempDir()
	claudeProj := filepath.Join(dir, "claude", "projects", "demo")
	if err := os.MkdirAll(claudeProj, 0o700); err != nil {
		t.Fatal(err)
	}
	// Two content blocks sharing req_1: thinking + text, preceded by api-request.
	// output_tokens (100) already includes thinking_tokens (40).
	// Also a short reply (output=10) that counts in usage & cache hit rate, NOT speed/TTFT.
	jsonl := "" +
		`{"type":"api-request","timestamp":"2026-11-18T10:00:00.000Z","uuid":"u0"}` + "\n" +
		`{"type":"assistant","timestamp":"2026-11-18T10:00:00.500Z","uuid":"u1","parentUuid":"u0","requestId":"req_1","message":{"id":"msg_1","model":"claude-opus-5-5","usage":{"input_tokens":100,"cache_creation_input_tokens":200,"cache_read_input_tokens":700,"output_tokens":100,"output_tokens_details":{"thinking_tokens":40}},"content":[{"type":"thinking"}]}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-11-18T10:00:01.500Z","uuid":"u2","parentUuid":"u1","requestId":"req_1","message":{"id":"msg_1","model":"claude-opus-5-5","usage":{"input_tokens":100,"cache_creation_input_tokens":200,"cache_read_input_tokens":700,"output_tokens":100,"output_tokens_details":{"thinking_tokens":40}},"content":[{"type":"text"}]}}` + "\n" +
		`{"type":"api-request","timestamp":"2026-11-18T10:01:00.000Z","uuid":"u3"}` + "\n" +
		`{"type":"assistant","timestamp":"2026-11-18T10:01:00.800Z","uuid":"u4","parentUuid":"u3","requestId":"req_2","message":{"id":"msg_2","model":"claude-opus-5-5","usage":{"input_tokens":50,"cache_creation_input_tokens":0,"cache_read_input_tokens":950,"output_tokens":10},"content":[{"type":"tool_use"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(claudeProj, "s1.jsonl"), []byte(jsonl), 0o600); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	c, err := New(Config{
		StateDir:       filepath.Join(dir, "state"),
		ClaudeHome:     filepath.Join(dir, "claude"),
		HasClaude:      true,
		HasAntigravity: true,
		Now:            func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	resp, err := c.Query(context.Background(), &pb.GetUsageStatsRequest{
		Preset:   pb.UsageRangePreset_USAGE_RANGE_PRESET_TODAY,
		Timezone: "UTC",
	}, QueryOptions{
		Installed: []pb.Agent{pb.Agent_AGENT_CLAUDE, pb.Agent_AGENT_ANTIGRAVITY},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Series) != 1 {
		t.Fatalf("expected 1 series, got %d", len(resp.Series))
	}
	s := resp.Series[0]
	cur := s.Current
	// Total tokens = (100+200+700+100) + (50+0+950+10) = 1100 + 1010 = 2110 (thinking NOT double counted).
	if cur.UncachedInputTokens != 150 || cur.CacheWriteTokens != 200 || cur.CacheReadTokens != 1650 || cur.OutputTokens != 110 || cur.ReasoningOutputTokens != 40 {
		t.Fatalf("unexpected token breakdown: %+v", cur)
	}
	if cur.UsageRequestCount != 2 || cur.SpeedRequestCount != 1 {
		t.Fatalf("unexpected request counts: usage=%d speed=%d", cur.UsageRequestCount, cur.SpeedRequestCount)
	}
	// Weighted cache hit = 1650 / (150 + 200 + 1650) = 1650 / 2000 = 0.825
	if !cur.HasCacheHitRate || cur.CacheHitRate < 0.824 || cur.CacheHitRate > 0.826 {
		t.Fatalf("unexpected cache hit rate: %v", cur.CacheHitRate)
	}
	// Speed = 100 tok / 1.0s = 100 tok/s; TTFT = 500ms
	if !cur.HasSpeed || cur.SpeedP50 < 99.9 || cur.SpeedP50 > 100.1 {
		t.Fatalf("unexpected speed p50: %v", cur.SpeedP50)
	}
	if !cur.HasTtft || cur.TtftP50Ms < 499 || cur.TtftP50Ms > 501 {
		t.Fatalf("unexpected ttft p50: %v", cur.TtftP50Ms)
	}
	// Antigravity must report OK source state when HasAntigravity is true.
	var agySrc *pb.UsageAgentSource
	for _, src := range resp.Sources {
		if src.Agent == pb.Agent_AGENT_ANTIGRAVITY {
			agySrc = src
		}
	}
	if agySrc == nil || agySrc.State != pb.UsageSourceState_USAGE_SOURCE_STATE_OK {
		t.Fatalf("expected antigravity source OK, got %+v", agySrc)
	}
}

func TestAntigravityParserUsageCacheNormalizationAndNoSpeed(t *testing.T) {
	dir := t.TempDir()
	agyLogs := filepath.Join(dir, "agy", "brain", "conv-123", ".system_generated", "logs")
	if err := os.MkdirAll(agyLogs, 0o700); err != nil {
		t.Fatal(err)
	}

	transcript := "" +
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-11-18T10:00:00Z","content":"hi"}` + "\n" +
		// Pre-1.5.x step without model field -> skipped.
		`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-18T10:00:01Z","model":"","input_tokens":500,"cache_read_tokens":0,"output_tokens":50}` + "\n" +
		// Still running step -> skipped.
		`{"step_index":2,"source":"MODEL","type":"PLANNER_RESPONSE","status":"RUNNING","created_at":"2026-11-18T10:00:02Z","model":"gemini-4-hydrogen-high","input_tokens":100,"cache_read_tokens":0,"output_tokens":10}` + "\n" +
		// Two efforts of gemini-4-hydrogen normalize into "gemini-4-hydrogen".
		`{"step_index":3,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-18T10:00:05Z","model":"gemini-4-hydrogen-high","input_tokens":2000,"cache_read_tokens":0,"output_tokens":500}` + "\n" +
		`{"step_index":4,"source":"SYSTEM","type":"CHECKPOINT","status":"DONE","created_at":"2026-11-18T10:00:06Z","input_tokens":999,"cache_read_tokens":999,"output_tokens":99}` + "\n" +
		`{"step_index":5,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-18T10:01:05Z","model":"gemini-4-hydrogen-medium","input_tokens":1000,"cache_read_tokens":7000,"output_tokens":300}` + "\n" +
		// Non-effort hyphenated suffix stays intact.
		`{"step_index":6,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-18T10:02:05Z","model":"gemini-flash-worker-agy","input_tokens":400,"cache_read_tokens":600,"output_tokens":100}` + "\n" +
		// Incomplete trailing line without newline -> ignored safely.
		`{"step_index":7,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","model":"gemini-4-hydrogen-low"`

	logPath := filepath.Join(agyLogs, "transcript.jsonl")
	if err := os.WriteFile(logPath, []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	c, err := New(Config{
		StateDir:        filepath.Join(dir, "state"),
		AntigravityHome: filepath.Join(dir, "agy"),
		HasAntigravity:  true,
		Now:             func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	resp, err := c.Query(context.Background(), &pb.GetUsageStatsRequest{
		Preset:   pb.UsageRangePreset_USAGE_RANGE_PRESET_TODAY,
		Timezone: "UTC",
	}, QueryOptions{
		Installed: []pb.Agent{pb.Agent_AGENT_ANTIGRAVITY},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Sources) != 1 || resp.Sources[0].Agent != pb.Agent_AGENT_ANTIGRAVITY || resp.Sources[0].State != pb.UsageSourceState_USAGE_SOURCE_STATE_OK {
		t.Fatalf("expected Antigravity source OK, got %+v", resp.Sources)
	}
	if len(resp.Series) != 2 {
		t.Fatalf("expected 2 antigravity series, got %d: %+v", len(resp.Series), resp.Series)
	}

	byModel := map[string]*pb.UsageSeries{}
	for _, s := range resp.Series {
		byModel[s.Model] = s
	}
	h := byModel["gemini-4-hydrogen"]
	if h == nil {
		t.Fatalf("expected normalized model gemini-4-hydrogen, got keys: %+v", byModel)
	}
	if h.CacheWriteSupported {
		t.Fatalf("expected CacheWriteSupported=false for Antigravity")
	}
	cur := h.Current
	if cur.UncachedInputTokens != 3000 || cur.CacheReadTokens != 7000 || cur.CacheWriteTokens != 0 || cur.OutputTokens != 800 || cur.ReasoningOutputTokens != 0 {
		t.Fatalf("unexpected antigravity token breakdown: %+v", cur)
	}
	if cur.UsageRequestCount != 2 || cur.SpeedRequestCount != 0 {
		t.Fatalf("unexpected antigravity request counts: usage=%d speed=%d", cur.UsageRequestCount, cur.SpeedRequestCount)
	}
	if !cur.HasCacheHitRate || cur.CacheHitRate < 0.699 || cur.CacheHitRate > 0.701 {
		t.Fatalf("unexpected antigravity cache hit rate: %v", cur.CacheHitRate)
	}
	if cur.HasSpeed || cur.HasTtft {
		t.Fatalf("expected HasSpeed=false and HasTtft=false for Antigravity, got %+v", cur)
	}

	worker := byModel["gemini-flash-worker-agy"]
	if worker == nil || worker.Current.UsageRequestCount != 1 {
		t.Fatalf("expected gemini-flash-worker-agy preserved intact, got %+v", worker)
	}

	// Complete the trailing line and verify earlier steps are NOT double-counted across rescans.
	completed := transcript + `,"created_at":"2026-11-18T10:03:05Z","input_tokens":100,"cache_read_tokens":0,"output_tokens":50}` + "\n"
	if err := os.WriteFile(logPath, []byte(completed), 0o600); err != nil {
		t.Fatal(err)
	}
	futureMtime := now.Add(3 * time.Second)
	_ = os.Chtimes(logPath, futureMtime, futureMtime)

	c.scanOnce(context.Background())
	resp2, err := c.Query(context.Background(), &pb.GetUsageStatsRequest{
		Preset:   pb.UsageRangePreset_USAGE_RANGE_PRESET_TODAY,
		Timezone: "UTC",
	}, QueryOptions{
		Installed: []pb.Agent{pb.Agent_AGENT_ANTIGRAVITY},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range resp2.Series {
		if s.Model == "gemini-4-hydrogen" {
			if s.Current.UsageRequestCount != 3 || s.Current.UncachedInputTokens != 3100 || s.Current.OutputTokens != 850 {
				t.Fatalf("unexpected rescan totals after appending step 7: %+v", s.Current)
			}
		}
	}
}

func TestCodexParserDedupAndRollupAndBillionTokens(t *testing.T) {
	dir := t.TempDir()
	codexSess := filepath.Join(dir, "codex", "sessions", "2026", "11", "01")
	if err := os.MkdirAll(codexSess, 0o700); err != nil {
		t.Fatal(err)
	}
	// Put historical samples 16 days ago (beyond 7d raw retention -> stored in hourly_buckets only)
	// using large billion-scale cached counts to verify uint64 fidelity!
	rollout := "" +
		`{"timestamp":"2026-11-02T10:00:00.000Z","type":"event_msg","payload":{"type":"task_started"}}` + "\n" +
		`{"timestamp":"2026-11-02T10:00:00.200Z","type":"turn_context","payload":{"model":"gpt-6-astra"}}` + "\n" +
		`{"timestamp":"2026-11-02T10:00:00.600Z","type":"response_item","payload":{"type":"reasoning"}}` + "\n" +
		// First API call: token_usage_record followed by token_count with same total_tokens -> deduplicated!
		`{"timestamp":"2026-11-02T10:00:02.600Z","type":"token_usage_record","payload":{"response_id":"resp_1","usage":{"input_tokens":3000000000,"cached_input_tokens":2500000000,"cache_write_input_tokens":0,"output_tokens":200,"reasoning_output_tokens":80,"total_tokens":3000000200},"thread_token_usage":{"total_tokens":3000000200}}}` + "\n" +
		`{"timestamp":"2026-11-02T10:00:02.605Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":3000000000,"cached_input_tokens":2500000000,"cache_write_input_tokens":0,"output_tokens":200,"reasoning_output_tokens":80,"total_tokens":3000000200},"total_token_usage":{"total_tokens":3000000200}}}}` + "\n" +
		// Null info rate-limit refresh -> skipped
		`{"timestamp":"2026-11-02T10:00:03.000Z","type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"limit_id":"codex"}}}` + "\n" +
		// Anomalous zero in/out with positive total -> skipped
		`{"timestamp":"2026-11-02T10:00:03.500Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":0,"cached_input_tokens":0,"output_tokens":0,"total_tokens":999},"total_token_usage":{"total_tokens":3000001199}}}}` + "\n"

	rolloutPath := filepath.Join(codexSess, "rollout-old.jsonl")
	if err := os.WriteFile(rolloutPath, []byte(rollout), 0o600); err != nil {
		t.Fatal(err)
	}
	tsOld := time.Date(2026, 11, 2, 10, 0, 5, 0, time.UTC)
	_ = os.Chtimes(rolloutPath, tsOld, tsOld)

	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	c, err := New(Config{
		StateDir:  filepath.Join(dir, "state"),
		CodexHome: filepath.Join(dir, "codex"),
		HasCodex:  true,
		Now:       func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// Scan first while "now" makes rawCutoff 7 days before 2026-11-18, and then query 30d so 2026-11-02 comes from hourly_buckets.
	resp, err := c.Query(context.Background(), &pb.GetUsageStatsRequest{
		Preset:   pb.UsageRangePreset_USAGE_RANGE_PRESET_THIRTY_DAYS,
		Timezone: "UTC",
	}, QueryOptions{
		Installed: []pb.Agent{pb.Agent_AGENT_CODEX},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Series) != 1 {
		t.Fatalf("expected 1 codex series, got %d", len(resp.Series))
	}
	s := resp.Series[0]
	if s.CacheWriteSupported {
		t.Fatalf("expected CacheWriteSupported=false when all cache_write_input_tokens==0")
	}
	cur := s.Current
	if cur.UncachedInputTokens != 500000000 || cur.CacheReadTokens != 2500000000 || cur.OutputTokens != 200 || cur.ReasoningOutputTokens != 80 {
		t.Fatalf("unexpected billion-scale codex breakdown: %+v", cur)
	}
	if cur.UsageRequestCount != 1 || cur.SpeedRequestCount != 1 {
		t.Fatalf("unexpected dedup request counts: usage=%d speed=%d", cur.UsageRequestCount, cur.SpeedRequestCount)
	}
	if cur.SpeedP50 < 99.9 || cur.SpeedP50 > 100.1 {
		t.Fatalf("expected ~100 tok/s, got %v", cur.SpeedP50)
	}

	// Test DST spring-forward 23h day in America/Los_Angeles (2026-03-08) resolves 23 hourly buckets cleanly.
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	dstNow := time.Date(2026, 3, 8, 15, 0, 0, 0, loc)
	bounds, gran, prevStart := resolveBounds(dstNow, loc, pb.UsageRangePreset_USAGE_RANGE_PRESET_TODAY, &pb.GetUsageStatsRequest{})
	if gran != pb.UsageBucketGranularity_USAGE_BUCKET_GRANULARITY_HOUR || len(bounds)-1 != 23 {
		t.Fatalf("expected 23 hourly buckets on spring-forward DST day, got gran=%v len=%d", gran, len(bounds)-1)
	}
	if prevStart >= bounds[0] {
		t.Fatalf("expected prevStart < bounds[0], got prevStart=%d bounds[0]=%d", prevStart, bounds[0])
	}
}

func TestNoDoubleCountAcrossRawPruneBoundary(t *testing.T) {
	dir := t.TempDir()
	claudeProj := filepath.Join(dir, "claude", "projects", "demo")
	if err := os.MkdirAll(claudeProj, 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(claudeProj, "long.jsonl")
	// Record 1 is 10 days old (>7d raw retention, <=90d max retention); Record 2 is today.
	initial := "" +
		`{"type":"api-request","timestamp":"2026-11-08T10:00:00.000Z","uuid":"u0"}` + "\n" +
		`{"type":"assistant","timestamp":"2026-11-08T10:00:00.500Z","uuid":"u1","parentUuid":"u0","requestId":"req_old","message":{"id":"msg_old","model":"claude-opus-5-5","usage":{"input_tokens":100,"cache_creation_input_tokens":0,"cache_read_input_tokens":900,"output_tokens":50},"content":[{"type":"text"}]}}` + "\n" +
		`{"type":"api-request","timestamp":"2026-11-18T10:00:00.000Z","uuid":"u2"}` + "\n" +
		`{"type":"assistant","timestamp":"2026-11-18T10:00:00.500Z","uuid":"u3","parentUuid":"u2","requestId":"req_new","message":{"id":"msg_new","model":"claude-opus-5-5","usage":{"input_tokens":20,"cache_creation_input_tokens":0,"cache_read_input_tokens":80,"output_tokens":10},"content":[{"type":"text"}]}}` + "\n"
	if err := os.WriteFile(logPath, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	c, err := New(Config{
		StateDir:   filepath.Join(dir, "state"),
		ClaudeHome: filepath.Join(dir, "claude"),
		HasClaude:  true,
		Now:        func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// Scan 1 + prune: req_old is in hourly_buckets & ingested_keys, NOT raw_requests.
	c.scanOnce(context.Background())

	// Append a new request to the same file and bump mtime so collector re-scans long.jsonl from offset 0.
	extra := `{"type":"api-request","timestamp":"2026-11-18T11:00:00.000Z","uuid":"u4"}` + "\n" +
		`{"type":"assistant","timestamp":"2026-11-18T11:00:00.500Z","uuid":"u5","parentUuid":"u4","requestId":"req_extra","message":{"id":"msg_extra","model":"claude-opus-5-5","usage":{"input_tokens":30,"cache_creation_input_tokens":0,"cache_read_input_tokens":70,"output_tokens":15},"content":[{"type":"text"}]}}` + "\n"
	if err := os.WriteFile(logPath, []byte(initial+extra), 0o600); err != nil {
		t.Fatal(err)
	}
	futureMtime := now.Add(5 * time.Second)
	_ = os.Chtimes(logPath, futureMtime, futureMtime)

	c.scanOnce(context.Background())

	resp, err := c.Query(context.Background(), &pb.GetUsageStatsRequest{
		Preset:   pb.UsageRangePreset_USAGE_RANGE_PRESET_THIRTY_DAYS,
		Timezone: "UTC",
	}, QueryOptions{
		Installed: []pb.Agent{pb.Agent_AGENT_CLAUDE},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Series) != 1 {
		t.Fatalf("expected 1 series, got %d", len(resp.Series))
	}
	cur := resp.Series[0].Current
	// Exact sum across req_old (100,900,50) + req_new (20,80,10) + req_extra (30,70,15):
	if cur.UsageRequestCount != 3 || cur.UncachedInputTokens != 150 || cur.CacheReadTokens != 1050 || cur.OutputTokens != 75 {
		t.Fatalf("double-counted across 7d prune boundary: %+v", cur)
	}
}

func encodeTestProtoTimestamp(ts time.Time) []byte {
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.VarintType)
	b = protowire.AppendVarint(b, uint64(ts.Unix()))
	b = protowire.AppendTag(b, 2, protowire.VarintType)
	b = protowire.AppendVarint(b, uint64(ts.Nanosecond()))
	return b
}

func encodeTestProtoDuration(d time.Duration) []byte {
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.VarintType)
	b = protowire.AppendVarint(b, uint64(d/time.Second))
	b = protowire.AppendTag(b, 2, protowire.VarintType)
	b = protowire.AppendVarint(b, uint64(d%time.Second))
	return b
}

func encodeTestAgyUsage(modelEnum, inTok, outTok, cacheReadTok, thinkingTok uint64, botID, respID string) []byte {
	var b []byte
	for _, kv := range []struct {
		num protowire.Number
		val uint64
	}{
		{1, modelEnum},
		{2, inTok},
		{3, outTok},
		{5, cacheReadTok},
		{9, thinkingTok},
	} {
		if kv.val > 0 || kv.num == 1 {
			b = protowire.AppendTag(b, kv.num, protowire.VarintType)
			b = protowire.AppendVarint(b, kv.val)
		}
	}
	if botID != "" {
		b = protowire.AppendTag(b, 7, protowire.BytesType)
		b = protowire.AppendString(b, botID)
	}
	if respID != "" {
		b = protowire.AppendTag(b, 11, protowire.BytesType)
		b = protowire.AppendString(b, respID)
	}
	return b
}

func encodeTestAgyGenMeta(modelEnum uint64, model string, ttft, genDur time.Duration, usage []byte) []byte {
	return encodeTestAgyGenMetaWithCascade(modelEnum, model, "", ttft, genDur, usage)
}

func encodeTestAgyGenMetaWithCascade(modelEnum uint64, model, cascadeID string, ttft, genDur time.Duration, usage []byte) []byte {
	var inner []byte
	inner = protowire.AppendTag(inner, 3, protowire.VarintType)
	inner = protowire.AppendVarint(inner, modelEnum)
	inner = protowire.AppendTag(inner, 4, protowire.BytesType)
	inner = protowire.AppendBytes(inner, usage)
	inner = protowire.AppendTag(inner, 11, protowire.BytesType)
	inner = protowire.AppendBytes(inner, encodeTestProtoDuration(ttft))
	inner = protowire.AppendTag(inner, 12, protowire.BytesType)
	inner = protowire.AppendBytes(inner, encodeTestProtoDuration(genDur))
	inner = protowire.AppendTag(inner, 19, protowire.BytesType)
	inner = protowire.AppendString(inner, model)
	if cascadeID != "" {
		var entry []byte
		entry = protowire.AppendTag(entry, 1, protowire.BytesType)
		entry = protowire.AppendString(entry, "cascade_id")
		entry = protowire.AppendTag(entry, 2, protowire.BytesType)
		entry = protowire.AppendString(entry, cascadeID)
		inner = protowire.AppendTag(inner, 20, protowire.BytesType)
		inner = protowire.AppendBytes(inner, entry)
	}

	var outer []byte
	outer = protowire.AppendTag(outer, 1, protowire.BytesType)
	outer = protowire.AppendBytes(outer, inner)
	return outer
}

func encodeTestAgyStepMeta(tCreate, tFirst, tEnd time.Time, usage []byte) []byte {
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendBytes(b, encodeTestProtoTimestamp(tCreate))
	b = protowire.AppendTag(b, 6, protowire.BytesType)
	b = protowire.AppendBytes(b, encodeTestProtoTimestamp(tFirst))
	b = protowire.AppendTag(b, 7, protowire.BytesType)
	b = protowire.AppendBytes(b, encodeTestProtoTimestamp(tEnd))
	b = protowire.AppendTag(b, 9, protowire.BytesType)
	b = protowire.AppendBytes(b, usage)
	return b
}

func TestAntigravityDBThinkingSpeedTTFTAndUpgradeFromTranscript(t *testing.T) {
	dir := t.TempDir()
	convID := "conv-wal-upgrade"
	agyHome := filepath.Join(dir, "agy")
	logsDir := filepath.Join(agyHome, "brain", convID, ".system_generated", "logs")
	convDir := filepath.Join(agyHome, "conversations")
	if err := os.MkdirAll(logsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(convDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Phase 1: simulate v0.89.3 state where only transcript.jsonl was scanned (no thinking, no speed).
	transcript := "" +
		`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-18T10:00:05Z","model":"gemini-4-hydrogen-high","input_tokens":2000,"cache_read_tokens":0,"output_tokens":500}` + "\n" +
		`{"step_index":4,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-18T10:01:05Z","model":"gemini-4-hydrogen-medium","input_tokens":1000,"cache_read_tokens":7000,"output_tokens":300}` + "\n"
	if err := os.WriteFile(filepath.Join(logsDir, "transcript.jsonl"), []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	c, err := New(Config{
		StateDir:        filepath.Join(dir, "state"),
		AntigravityHome: agyHome,
		HasAntigravity:  true,
		Now:             func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	c.scanOnce(context.Background())

	// Phase 2: write conversations/<convID>.db in WAL mode with:
	// - steps 1 & 4 matching transcript steps (must upgrade thinking/speed in-place WITHOUT double counting)
	// - historical 10d-old step for "gemini-3.8-flash-n"
	// - short output step (output=10) that counts in usage, not speed/TTFT
	dbPath := filepath.Join(convDir, convID+".db")
	wdb, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=wal_autocheckpoint(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer wdb.Close()
	if _, err := wdb.Exec(`
		CREATE TABLE steps(idx INTEGER PRIMARY KEY, step_type INTEGER NOT NULL, status INTEGER NOT NULL, metadata BLOB);
		CREATE TABLE gen_metadata(idx INTEGER PRIMARY KEY, data BLOB, size INTEGER NOT NULL DEFAULT 0);
	`); err != nil {
		t.Fatal(err)
	}

	insertStep := func(gmIdx, stepIdx int64, modelEnum uint64, model string, botID string, inTok, cacheRead, outTok, thinkTok uint64, tCreate time.Time, ttft, genDur time.Duration) {
		tFirst := tCreate.Add(ttft)
		tEnd := tFirst.Add(genDur)
		u := encodeTestAgyUsage(modelEnum, inTok, outTok, cacheRead, thinkTok, botID, "")
		gm := encodeTestAgyGenMeta(modelEnum, model, ttft, genDur, u)
		sm := encodeTestAgyStepMeta(tCreate, tFirst, tEnd, u)
		if _, err := wdb.Exec(`INSERT INTO gen_metadata(idx, data, size) VALUES(?, ?, 0)`, gmIdx, gm); err != nil {
			t.Fatal(err)
		}
		if _, err := wdb.Exec(`INSERT INTO steps(idx, step_type, status, metadata) VALUES(?, 15, 5, ?)`, stepIdx, sm); err != nil {
			t.Fatal(err)
		}
	}

	tToday1 := time.Date(2026, 11, 18, 10, 0, 5, 0, time.UTC)
	tToday2 := time.Date(2026, 11, 18, 10, 1, 5, 0, time.UTC)
	tHist := time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)
	// Keep autocheckpoint=0 so rows remain in -wal file while scanAntigravityDB reads mode=ro.
	insertStep(0, 1, 1525, "gemini-4-hydrogen", "bot-1", 2000, 0, 500, 300, tToday1, 1500*time.Millisecond, 2500*time.Millisecond)
	insertStep(1, 4, 1525, "gemini-4-hydrogen", "bot-4", 1000, 7000, 300, 120, tToday2, 500*time.Millisecond, 1500*time.Millisecond)
	// Short output reply on claude-opus-5-5-high (suffix stripped -> claude-opus-5-5), counts usage not speed.
	insertStep(2, 6, 1402, "claude-opus-5-5-high", "bot-6", 100, 900, 10, 0, tToday2.Add(time.Minute), 400*time.Millisecond, 300*time.Millisecond)
	// Historical 10d-old request
	insertStep(3, 8, 1318, "gemini-3.8-flash-n", "bot-8", 400, 1600, 200, 120, tHist, 800*time.Millisecond, 1000*time.Millisecond)

	// Append a WAL-only un-checkpointed write and touch brain transcript mtime to verify transcript is skipped when .db exists.
	futureMtime := now.Add(5 * time.Second)
	_ = os.Chtimes(filepath.Join(logsDir, "transcript.jsonl"), futureMtime, futureMtime)

	c.scanOnce(context.Background())

	// Verify discovery excludes transcript.jsonl when conversations/<convID>.db exists.
	cands, err := discoverAntigravityLogs(agyHome, now.Add(-90*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || filepath.Ext(cands[0].path) != ".db" {
		t.Fatalf("expected only .db candidate when both .db and transcript exist, got %+v", cands)
	}

	resp, err := c.Query(context.Background(), &pb.GetUsageStatsRequest{
		Preset:   pb.UsageRangePreset_USAGE_RANGE_PRESET_THIRTY_DAYS,
		Timezone: "UTC",
	}, QueryOptions{
		Installed: []pb.Agent{pb.Agent_AGENT_ANTIGRAVITY},
	})
	if err != nil {
		t.Fatal(err)
	}
	byModel := map[string]*pb.UsageSeries{}
	for _, s := range resp.Series {
		byModel[s.Model] = s
	}

	h := byModel["gemini-4-hydrogen"]
	if h == nil {
		t.Fatalf("missing gemini-4-hydrogen series: %+v", byModel)
	}
	if h.CacheWriteSupported {
		t.Fatalf("expected CacheWriteSupported=false")
	}
	if h.Current.UsageRequestCount != 2 || h.Current.UncachedInputTokens != 3000 || h.Current.CacheReadTokens != 7000 || h.Current.OutputTokens != 800 {
		t.Fatalf("expected in-place upgrade without double count, got %+v", h.Current)
	}
	if h.Current.ReasoningOutputTokens != 420 || h.Current.SpeedRequestCount != 2 || !h.Current.HasSpeed || !h.Current.HasTtft {
		t.Fatalf("expected thinking=420, speedReqs=2, HasSpeed & HasTtft true, got %+v", h.Current)
	}
	// Median of (200 tok/s, 200 tok/s) = 200; median of (500ms, 1500ms) = 1000ms.
	if h.Current.SpeedP50 < 199.9 || h.Current.SpeedP50 > 200.1 || h.Current.TtftP50Ms < 999 || h.Current.TtftP50Ms > 1001 {
		t.Fatalf("unexpected gemini-4-hydrogen speed/ttft medians: speed=%v ttft=%v", h.Current.SpeedP50, h.Current.TtftP50Ms)
	}

	hist := byModel["gemini-3.8-flash-n"]
	if hist == nil || hist.Current.UsageRequestCount != 1 || hist.Current.SpeedRequestCount != 1 || hist.Current.ReasoningOutputTokens != 120 {
		t.Fatalf("expected 10d-old historical gemini-3.8-flash-n row, got %+v", hist)
	}
	if hist.Current.SpeedP50 < 199.9 || hist.Current.SpeedP50 > 200.1 || hist.Current.TtftP50Ms < 799 || hist.Current.TtftP50Ms > 801 {
		t.Fatalf("unexpected historical speed/ttft: %+v", hist.Current)
	}

	cl := byModel["claude-opus-5-5"]
	if cl == nil || cl.Current.UsageRequestCount != 1 || cl.Current.SpeedRequestCount != 0 || cl.Current.HasSpeed {
		t.Fatalf("expected claude-opus-5-5 (suffix stripped) with usage=1 speed=0, got %+v", cl)
	}
}

func TestAntigravityDBCheckpointForkDedupAndSpeedGuard(t *testing.T) {
	dir := t.TempDir()
	agyHome := filepath.Join(dir, "agy")
	convDir := filepath.Join(agyHome, "conversations")
	if err := os.MkdirAll(convDir, 0o700); err != nil {
		t.Fatal(err)
	}

	initDB := func(name string) *sql.DB {
		db, err := sql.Open("sqlite", filepath.Join(convDir, name+".db"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`
			CREATE TABLE steps(idx INTEGER PRIMARY KEY, step_type INTEGER NOT NULL, status INTEGER NOT NULL, metadata BLOB);
			CREATE TABLE gen_metadata(idx INTEGER PRIMARY KEY, data BLOB, size INTEGER NOT NULL DEFAULT 0);
		`); err != nil {
			t.Fatal(err)
		}
		return db
	}

	origID := "orig-conv"
	forkID := "fork-conv"
	t0 := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)

	origDB := initDB(origID)
	defer origDB.Close()
	// Normal step 1: 150 tok/s (300 tok / 2s), ttft=2s
	u1 := encodeTestAgyUsage(1533, 1000, 300, 4000, 100, "bot-1", "")
	gm1 := encodeTestAgyGenMetaWithCascade(1533, "gemini-4-neon", origID, 2*time.Second, 2*time.Second, u1)
	sm1 := encodeTestAgyStepMeta(t0, t0.Add(2*time.Second), t0.Add(4*time.Second), u1)
	if _, err := origDB.Exec(`INSERT INTO gen_metadata(idx, data, size) VALUES(0, ?, 0)`, gm1); err != nil {
		t.Fatal(err)
	}
	if _, err := origDB.Exec(`INSERT INTO steps(idx, step_type, status, metadata) VALUES(1, 15, 5, ?)`, sm1); err != nil {
		t.Fatal(err)
	}
	// Buffered tool-call outlier step 2: ttft=60s, gen=250ms, out=15000 (60000 tok/s) -> usage counted, speed dropped
	u2 := encodeTestAgyUsage(1533, 500, 15000, 5000, 50, "bot-2", "")
	gm2 := encodeTestAgyGenMetaWithCascade(1533, "gemini-4-neon", origID, 60*time.Second, 250*time.Millisecond, u2)
	sm2 := encodeTestAgyStepMeta(t0.Add(time.Minute), t0.Add(2*time.Minute), t0.Add(2*time.Minute+250*time.Millisecond), u2)
	if _, err := origDB.Exec(`INSERT INTO gen_metadata(idx, data, size) VALUES(1, ?, 0)`, gm2); err != nil {
		t.Fatal(err)
	}
	if _, err := origDB.Exec(`INSERT INTO steps(idx, step_type, status, metadata) VALUES(2, 15, 5, ?)`, sm2); err != nil {
		t.Fatal(err)
	}
	// Buffered burst step 3: ttft=10s, gen=400ms, out=240 (600 tok/s, ttft > 4*gen & gen < 1000 & speed > 500) -> speed dropped
	u3 := encodeTestAgyUsage(1533, 200, 240, 2000, 40, "bot-3", "")
	gm3 := encodeTestAgyGenMetaWithCascade(1533, "gemini-4-neon", origID, 10*time.Second, 400*time.Millisecond, u3)
	sm3 := encodeTestAgyStepMeta(t0.Add(3*time.Minute), t0.Add(3*time.Minute+10*time.Second), t0.Add(3*time.Minute+10*time.Second+400*time.Millisecond), u3)
	if _, err := origDB.Exec(`INSERT INTO gen_metadata(idx, data, size) VALUES(2, ?, 0)`, gm3); err != nil {
		t.Fatal(err)
	}
	if _, err := origDB.Exec(`INSERT INTO steps(idx, step_type, status, metadata) VALUES(3, 15, 5, ?)`, sm3); err != nil {
		t.Fatal(err)
	}
	// CHECKPOINT compaction step 4 (step_type=23, no gen_metadata row, resolves via modelEnum=1533)
	u4 := encodeTestAgyUsage(1533, 10000, 1000, 90000, 200, "bot-4", "")
	sm4 := encodeTestAgyStepMeta(t0.Add(4*time.Minute), t0.Add(4*time.Minute+time.Second), t0.Add(4*time.Minute+5*time.Second), u4)
	if _, err := origDB.Exec(`INSERT INTO steps(idx, step_type, status, metadata) VALUES(4, 23, 5, ?)`, sm4); err != nil {
		t.Fatal(err)
	}

	// Forked DB clones step 1 with origID as cascade_id (must NOT double count) plus a new step 5
	forkDB := initDB(forkID)
	defer forkDB.Close()
	if _, err := forkDB.Exec(`INSERT INTO gen_metadata(idx, data, size) VALUES(0, ?, 0)`, gm1); err != nil {
		t.Fatal(err)
	}
	if _, err := forkDB.Exec(`INSERT INTO steps(idx, step_type, status, metadata) VALUES(1, 15, 5, ?)`, sm1); err != nil {
		t.Fatal(err)
	}
	u5 := encodeTestAgyUsage(1533, 300, 200, 1000, 50, "bot-5", "")
	gm5 := encodeTestAgyGenMetaWithCascade(1533, "gemini-4-neon", forkID, time.Second, time.Second, u5)
	sm5 := encodeTestAgyStepMeta(t0.Add(5*time.Minute), t0.Add(5*time.Minute+time.Second), t0.Add(5*time.Minute+2*time.Second), u5)
	if _, err := forkDB.Exec(`INSERT INTO gen_metadata(idx, data, size) VALUES(1, ?, 0)`, gm5); err != nil {
		t.Fatal(err)
	}
	if _, err := forkDB.Exec(`INSERT INTO steps(idx, step_type, status, metadata) VALUES(5, 15, 5, ?)`, sm5); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	c, err := New(Config{
		StateDir:        filepath.Join(dir, "state"),
		AntigravityHome: agyHome,
		HasAntigravity:  true,
		Now:             func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	c.scanOnce(context.Background())

	resp, err := c.Query(context.Background(), &pb.GetUsageStatsRequest{
		Preset:   pb.UsageRangePreset_USAGE_RANGE_PRESET_TODAY,
		Timezone: "UTC",
	}, QueryOptions{
		Installed: []pb.Agent{pb.Agent_AGENT_ANTIGRAVITY},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Series) != 1 || resp.Series[0].Model != "gemini-4-neon" {
		t.Fatalf("unexpected series: %+v", resp.Series)
	}
	cur := resp.Series[0].Current
	// 5 unique requests (steps 1..4 from orig + step 5 from fork; forked step 1 deduplicated).
	if cur.UsageRequestCount != 5 {
		t.Fatalf("expected 5 unique requests, got %d", cur.UsageRequestCount)
	}
	if cur.UncachedInputTokens != 12000 || cur.CacheReadTokens != 102000 || cur.OutputTokens != 16740 {
		t.Fatalf("unexpected token totals: uncached=%d cacheRead=%d out=%d", cur.UncachedInputTokens, cur.CacheReadTokens, cur.OutputTokens)
	}
	// Only step 1 (150 tok/s) and step 5 (200 tok/s) pass speed guard; steps 2, 3 (outliers) and 4 (checkpoint) excluded.
	if cur.SpeedRequestCount != 2 {
		t.Fatalf("expected 2 valid speed samples, got %d", cur.SpeedRequestCount)
	}
	if cur.SpeedP50 < 174.9 || cur.SpeedP50 > 175.1 {
		t.Fatalf("expected speed P50=175, got %v", cur.SpeedP50)
	}
}
