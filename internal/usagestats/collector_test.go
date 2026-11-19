package usagestats

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
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
