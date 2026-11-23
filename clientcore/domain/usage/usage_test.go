package usage

import (
	"math"
	"testing"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
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
		{12_345, true, "12.3k"},
		{99_949, true, "99.9k"},
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
		{1_500_000_000, false, "15亿"},
		{3_480_000_000, false, "34.8亿"},
		{3_480_000_000, true, "3.5B"},
		{12_860_000_000, false, "128.6亿"},
		{12_860_000_000, true, "12.9B"},
		{22_950_000_000, false, "229.5亿"},
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
		{0.0001, "0.1%"},
		{0.9999, "99.9%"},
		{1.0, "100%"},
		{math.NaN(), "0%"},
	}
	for _, tc := range cases {
		if got := FormatCacheHit(tc.r); got != tc.exp {
			t.Errorf("FormatCacheHit(%v) = %q, want %q", tc.r, got, tc.exp)
		}
	}
}

func TestFormatTtftAndDelta(t *testing.T) {
	ttftCases := []struct {
		ms   float64
		want string
	}{
		{0, "0.0s"},
		{-10, "0.0s"},
		{math.NaN(), "0.0s"},
		{20, "0.1s"},
		{420, "0.4s"},
		{9940, "9.9s"},
		{12400, "12s"},
	}
	for _, tc := range ttftCases {
		if got := FormatTtftSeconds(tc.ms); got != tc.want {
			t.Errorf("FormatTtftSeconds(%v) = %q, want %q", tc.ms, got, tc.want)
		}
	}
	if got := FormatDeltaTriangle(0.2); got.GetKey() != "usage.flat" {
		t.Errorf("FormatDeltaTriangle(0.2) = %+v, want key usage.flat", got)
	}
	if got := FormatDeltaTriangle(12.6); got.GetLiteral() != "▲13%" {
		t.Errorf("FormatDeltaTriangle(12.6) = %+v, want ▲13%%", got)
	}
	if got := FormatDeltaTriangle(-1234.4); got.GetLiteral() != "▼1,234%" {
		t.Errorf("FormatDeltaTriangle(-1234.4) = %+v, want ▼1,234%%", got)
	}
	tokCases := []struct {
		n      uint64
		locale string
		want   string
	}{
		{0, "", "0"},
		{8_420, "", "8,420"},
		{8_420, "zh-CN", "8,420"},
		{8_420, "en", "8.4k"},
		{12_345, "", "1.2万"},
		{12_345, "zh-CN", "1.2万"},
		{12_345, "zh-Hans", "1.2万"},
		{12_345, "zh_CN", "1.2万"},
		{12_345, "en", "12.3k"},
		{12_345, "en-US", "12.3k"},
		{4_820_000, "zh-CN", "482万"},
		{4_820_000, "en", "4.8M"},
		{140_000_000, "zh-CN", "1.4亿"},
		{140_000_000, "en", "140M"},
		{3_480_000_000, "zh-CN", "34.8亿"},
		{3_480_000_000, "en", "3.5B"},
	}
	for _, tc := range tokCases {
		got := FormatTokensText(tc.n, tc.locale)
		if got.GetKey() != "" || got.GetLiteral() != tc.want {
			t.Errorf("FormatTokensText(%d, %q) = (key=%q, literal=%q), want literal %q", tc.n, tc.locale, got.GetKey(), got.GetLiteral(), tc.want)
		}
	}
}

func TestNiceCeiling(t *testing.T) {
	cases := []struct {
		peak uint64
		want uint64
	}{
		{0, 1},
		{1, 1},
		{820, 1000},
		{1200, 1500},
		{1600, 2000},
		{2200, 3000},
		{3500, 4000},
		{4200, 5000},
		{7500, 8000},
		{85_000_000, 100_000_000},
	}
	for _, tc := range cases {
		if got := NiceCeiling(tc.peak); got != tc.want {
			t.Errorf("NiceCeiling(%d) = %d, want %d", tc.peak, got, tc.want)
		}
	}
}

func TestComputeDeltasThresholdsAndBuildUsageProjection(t *testing.T) {
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

	claude := &v1.UsageSeries{
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
		Buckets: []*v1.UsageBucketPoint{
			{
				StartMs:               1000,
				EndMs:                 2000,
				UncachedInputTokens:   200_000,
				CacheReadTokens:       1_000_000,
				CacheWriteTokens:      500_000,
				OutputTokens:          300_000,
				ReasoningOutputTokens: 50_000,
				UsageRequestCount:     8,
				SpeedRequestCount:     8,
				SpeedP50:              90,
				TtftP50Ms:             900,
			},
			{
				StartMs:               2000,
				EndMs:                 3000,
				UncachedInputTokens:   300_000,
				CacheReadTokens:       2_000_000,
				OutputTokens:          520_000,
				ReasoningOutputTokens: 70_000,
				UsageRequestCount:     12,
				SpeedRequestCount:     10,
				SpeedP50:              94,
				TtftP50Ms:             700,
			},
		},
	}
	if TotalTokens(claude.Current) != 4_820_000 {
		t.Fatalf("TotalTokens double-counted reasoning? got %d", TotalTokens(claude.Current))
	}
	if d := ComputeDeltas(claude, true); d.HasTokens || d.HasCacheHit || d.HasSpeed || d.HasTtft {
		t.Fatalf("expected no deltas during previous backfilling, got %+v", d)
	}
	d := ComputeDeltas(claude, false)
	if !d.HasTokens || math.Abs(d.CacheHitPoints-10.0) > 1e-6 || !d.HasSpeed || !d.HasTtft {
		t.Fatalf("unexpected normal deltas: %+v", d)
	}

	codex := &v1.UsageSeries{
		Agent: v1.Agent_AGENT_CODEX,
		Model: "gpt-5.4",
		Current: &v1.UsagePeriodSummary{
			UncachedInputTokens: 100_000,
			CacheReadTokens:     900_000,
			OutputTokens:        80_000,
			UsageRequestCount:   10,
			SpeedRequestCount:   10,
			HasCacheHitRate:     true,
			CacheHitRate:        0.90,
			HasSpeed:            true,
			SpeedP50:            120,
			HasTtft:             true,
			TtftP50Ms:           400,
		},
		Buckets: []*v1.UsageBucketPoint{
			{
				StartMs:             1000,
				EndMs:               2000,
				UncachedInputTokens: 100_000,
				CacheReadTokens:     900_000,
				OutputTokens:        80_000,
				UsageRequestCount:   10,
				SpeedRequestCount:   10,
				SpeedP50:            120,
				TtftP50Ms:           400,
			},
			{StartMs: 2000, EndMs: 3000},
		},
	}
	noSpeed := &v1.UsageSeries{
		Agent: v1.Agent_AGENT_CODEX,
		Model: "gpt-5.4-mini",
		Current: &v1.UsagePeriodSummary{
			UncachedInputTokens: 50_000,
			OutputTokens:        5_000,
			UsageRequestCount:   4,
		},
		Buckets: []*v1.UsageBucketPoint{
			{StartMs: 1000, EndMs: 2000},
			{StartMs: 2000, EndMs: 3000, UncachedInputTokens: 50_000, OutputTokens: 5_000, UsageRequestCount: 4},
		},
	}
	// Series only active in the previous period must still contribute to all-models previous total.
	prevOnly := &v1.UsageSeries{
		Agent:   v1.Agent_AGENT_CLAUDE,
		Model:   "claude-haiku-4-5",
		Current: &v1.UsagePeriodSummary{},
		Previous: &v1.UsagePeriodSummary{
			UncachedInputTokens: 1_000_000,
			UsageRequestCount:   5,
		},
	}

	resp := &v1.UsageStatsResponse{Series: []*v1.UsageSeries{claude, codex, noSpeed, prevOnly}}

	// 1. Default all-models projection sorted by TTFT asc puts fastest TTFT first and missing TTFT last,
	// while ShareRatio stays anchored to token consumption.
	pSort := BuildUsageProjection(resp, ProjectionOptions{
		SortBy: corev1.UsageSortColumn_USAGE_SORT_COLUMN_TTFT,
	})
	if !pSort.HasAnyUsage || len(pSort.Rows) != 3 {
		t.Fatalf("expected 3 active rows (prevOnly excluded), got %+v", pSort.Rows)
	}
	if pSort.Rows[0].Model != "gpt-5.4" || pSort.Rows[1].Model != "claude-sonnet-4-6" || pSort.Rows[2].Model != "gpt-5.4-mini" {
		t.Fatalf("unexpected TTFT ascending sort order: %+v", pSort.Rows)
	}
	if math.Abs(pSort.Rows[1].ShareRatio-1.0) > 1e-6 {
		t.Fatalf("expected claude ShareRatio=1.0 even when sorted by TTFT, got %v", pSort.Rows[1].ShareRatio)
	}
	// All-models current = 4,820,000 + 1,080,000 + 55,000 = 5,955,000; previous = 3,000,000 + 1,000,000 = 4,000,000 (+48.875%).
	if !pSort.Hero.HasTokensDelta || math.Abs(pSort.Hero.TokensDeltaPercent-48.875) > 0.01 {
		t.Fatalf("expected all-models delta including prevOnly (~48.875%%), got has=%v pct=%v", pSort.Hero.HasTokensDelta, pSort.Hero.TokensDeltaPercent)
	}
	if pSort.Hero.PeriodCompositionRows != 5 {
		t.Fatalf("expected 5 non-zero composition rows, got %d", pSort.Hero.PeriodCompositionRows)
	}

	// 2. Single-model selection keeps BarChart scaled to all models and overlays selected tokens + strip curves.
	pSel := BuildUsageProjection(resp, ProjectionOptions{
		SelectedSeriesKey: SeriesKey(v1.Agent_AGENT_CODEX, "gpt-5.4"),
	})
	if !pSel.Hero.HasSelectedModel || pSel.Hero.SelectedModel != "gpt-5.4" {
		t.Fatalf("expected gpt-5.4 selected in hero, got %+v", pSel.Hero)
	}
	if pSel.Hero.PeriodCompositionRows != 3 { // uncached, cache_read, output (no cache_write, no reasoning)
		t.Fatalf("expected 3 non-zero composition rows for codex, got %d", pSel.Hero.PeriodCompositionRows)
	}
	if pSel.BarChart.YMax != pSort.BarChart.YMax {
		t.Fatalf("bar chart YMax must remain scaled to all models: got %d vs %d", pSel.BarChart.YMax, pSort.BarChart.YMax)
	}
	if pSel.BarChart.Buckets[0].AllTokens <= pSel.BarChart.Buckets[0].SelectedTokens {
		t.Fatalf("expected bucket 0 AllTokens (%d) > SelectedTokens (%d)", pSel.BarChart.Buckets[0].AllTokens, pSel.BarChart.Buckets[0].SelectedTokens)
	}
	if len(pSel.OutputSpeedStrip.AllPoints) != 2 || len(pSel.OutputSpeedStrip.SelectedPoints) != 2 {
		t.Fatalf("expected both AllPoints and SelectedPoints populated on strip")
	}

	// 3. Scrubbing an empty bucket for selected model reports 0 tokens and HasValue=false on strips while keeping SelectedModel.
	pScrub := BuildUsageProjection(resp, ProjectionOptions{
		SelectedSeriesKey: SeriesKey(v1.Agent_AGENT_CODEX, "gpt-5.4"),
		HasScrub:          true,
		ScrubBucketIndex:  1,
	})
	if !pScrub.Hero.Scrubbing || pScrub.Hero.SelectedModel != "gpt-5.4" || pScrub.Hero.TotalTokens != 0 {
		t.Fatalf("unexpected scrub hero for empty bucket: %+v", pScrub.Hero)
	}
	if pScrub.OutputSpeedStrip.HasValue || pScrub.TtftStrip.HasValue || pScrub.CacheHitStrip.HasValue {
		t.Fatalf("expected strip HasValue=false on empty scrubbed bucket")
	}

	// 4. Projection text formatting follows UILocale (default/zh-CN vs en).
	localeCases := []struct {
		locale     string
		wantHero   string
		wantRow0   string
		wantYMax   string
		wantOutput string
	}{
		{"", "596万", "482万", "400万", "90.5万"},
		{"zh-CN", "596万", "482万", "400万", "90.5万"},
		{"en", "6M", "4.8M", "4M", "905k"},
		{"en-US", "6M", "4.8M", "4M", "905k"},
	}
	for _, tc := range localeCases {
		pLoc := BuildUsageProjection(resp, ProjectionOptions{UILocale: tc.locale})
		if pLoc.Hero.GetTotalTokensText().GetLiteral() != tc.wantHero {
			t.Errorf("locale=%q Hero.TotalTokensText = %q, want %q", tc.locale, pLoc.Hero.GetTotalTokensText().GetLiteral(), tc.wantHero)
		}
		if pLoc.Rows[0].GetTotalTokensText().GetLiteral() != tc.wantRow0 {
			t.Errorf("locale=%q Rows[0].TotalTokensText = %q, want %q", tc.locale, pLoc.Rows[0].GetTotalTokensText().GetLiteral(), tc.wantRow0)
		}
		if pLoc.BarChart.GetYMaxText().GetLiteral() != tc.wantYMax {
			t.Errorf("locale=%q BarChart.YMaxText = %q, want %q", tc.locale, pLoc.BarChart.GetYMaxText().GetLiteral(), tc.wantYMax)
		}
		if pLoc.Hero.GetComposition().GetOutputText().GetLiteral() != tc.wantOutput {
			t.Errorf("locale=%q Composition.OutputText = %q, want %q", tc.locale, pLoc.Hero.GetComposition().GetOutputText().GetLiteral(), tc.wantOutput)
		}
	}
}
