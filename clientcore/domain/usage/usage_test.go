package usage

import (
	"math"
	"testing"

	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

func TestFormatTokens(t *testing.T) {
	cases := []struct {
		n   uint64
		en  bool
		exp string
	}{
		{0, false, "0"},
		{840, false, "840"},
		{840, true, "840"},
		{8_000, false, "8,000"},
		{8_000, true, "8k"},
		{8_420, false, "8,420"},
		{8_420, true, "8.4k"},
		{12_000, false, "1.2万"},
		{12_000, true, "12k"},
		{486_000, false, "48.6万"},
		{486_000, true, "486k"},
		{999_600, true, "1M"},
		{4_820_000, false, "482万"},
		{4_820_000, true, "4.8M"},
		{34_200_000, false, "3,420万"},
		{34_200_000, true, "34.2M"},
		{99_960_000, false, "1亿"},
		{140_000_000, false, "1.4亿"},
		{140_000_000, true, "140M"},
		{999_600_000, true, "1B"},
		{3_480_000_000, false, "34.8亿"},
		{3_480_000_000, true, "3.5B"},
	}
	for _, tc := range cases {
		if got := FormatTokens(tc.n, tc.en); got != tc.exp {
			t.Errorf("FormatTokens(%d, en=%v) = %q, want %q", tc.n, tc.en, got, tc.exp)
		}
	}
}

func TestFormatCacheHit(t *testing.T) {
	cases := []struct {
		r   float64
		exp string
	}{
		{0, "0%"},
		{0.004, "0.4%"},
		{0.884, "88%"},
		{0.996, "99.6%"},
		{1.0, "100%"},
	}
	for _, tc := range cases {
		if got := FormatCacheHit(tc.r); got != tc.exp {
			t.Errorf("FormatCacheHit(%v) = %q, want %q", tc.r, got, tc.exp)
		}
	}
}

func TestComputeDeltasThresholdsAndBuildSeriesViews(t *testing.T) {
	few := &v1.UsageSeries{
		Agent: v1.Agent_AGENT_CODEX,
		Model: "gpt-5",
		Current: &v1.UsagePeriodSummary{
			UncachedInputTokens: 1000,
			OutputTokens:        200,
			UsageRequestCount:   2,
			SpeedRequestCount:   2,
			HasCacheHitRate:     true,
			CacheHitRate:        0.5,
			HasSpeed:            true,
			SpeedP50:            80,
			HasTtft:             true,
			TtftP50Ms:           600,
		},
		Previous: &v1.UsagePeriodSummary{
			UncachedInputTokens: 500,
			OutputTokens:        100,
			UsageRequestCount:   5,
			SpeedRequestCount:   5,
			HasCacheHitRate:     true,
			CacheHitRate:        0.4,
			HasSpeed:            true,
			SpeedP50:            70,
			HasTtft:             true,
			TtftP50Ms:           700,
		},
	}
	if d := ComputeDeltas(few, false); d.HasTokens || d.HasCacheHit || d.HasSpeed || d.HasTtft {
		t.Fatalf("expected all deltas false when current requests < 3, got %+v", d)
	}

	normal := &v1.UsageSeries{
		Agent:               v1.Agent_AGENT_CLAUDE,
		Model:               "claude-sonnet-4-6",
		CacheWriteSupported: true,
		Current: &v1.UsagePeriodSummary{
			UncachedInputTokens:   500_000,
			CacheReadTokens:       3_000_000,
			CacheWriteTokens:      500_000,
			OutputTokens:          820_000,
			ReasoningOutputTokens: 120_000,
			UsageRequestCount:     20,
			SpeedRequestCount:     18,
			HasCacheHitRate:       true,
			CacheHitRate:          0.75,
			HasSpeed:              true,
			SpeedP50:              92,
			HasTtft:               true,
			TtftP50Ms:             800,
		},
		Previous: &v1.UsagePeriodSummary{
			UncachedInputTokens: 400_000,
			CacheReadTokens:     2_000_000,
			OutputTokens:        600_000,
			UsageRequestCount:   15,
			SpeedRequestCount:   12,
			HasCacheHitRate:     true,
			CacheHitRate:        0.65,
			HasSpeed:            true,
			SpeedP50:            80,
			HasTtft:             true,
			TtftP50Ms:           1000,
		},
	}
	if TotalTokens(normal.Current) != 4_820_000 {
		t.Fatalf("TotalTokens double-counted reasoning? got %d", TotalTokens(normal.Current))
	}
	if d := ComputeDeltas(normal, true); d.HasTokens || d.HasCacheHit || d.HasSpeed || d.HasTtft {
		t.Fatalf("expected no deltas during previous backfilling, got %+v", d)
	}
	d := ComputeDeltas(normal, false)
	if !d.HasTokens || math.Abs(d.CacheHitPoints-10.0) > 1e-6 || !d.HasSpeed || !d.HasTtft {
		t.Fatalf("unexpected normal deltas: %+v", d)
	}

	// Focus ordering puts secondary series first with PatternIndex 0.
	resp := &v1.UsageStatsResponse{Series: []*v1.UsageSeries{normal, {
		Agent: v1.Agent_AGENT_CODEX,
		Model: "gpt-5.4",
		Current: &v1.UsagePeriodSummary{
			UncachedInputTokens: 1000,
			OutputTokens:        100,
			UsageRequestCount:   2,
			SpeedRequestCount:   2,
		},
	}}}
	views := BuildSeriesViews(resp, v1.Agent_AGENT_CODEX, "gpt-5.4")
	if len(views) != 2 || !views[0].Focused || views[0].PatternIndex != 0 || views[1].PatternIndex != 1 {
		t.Fatalf("unexpected focused series views: %+v", views)
	}
	if !views[0].FewSpeedSamples {
		t.Fatal("expected FewSpeedSamples=true when SpeedRequestCount=2")
	}
}
