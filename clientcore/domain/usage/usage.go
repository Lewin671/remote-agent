// Package usage provides pure-Go, tzdata-free aggregation, delta calculation,
// projection building, and formatting helpers for model usage, cache hit rate,
// output speed, and TTFT.
package usage

import (
	"math"
	"sort"
	"strconv"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

const MinTrendRequests = 3

// TotalTokens returns the mutually-exclusive token sum for a period summary.
// Reasoning output tokens are already included in OutputTokens and never added twice.
func TotalTokens(p *v1.UsagePeriodSummary) uint64 {
	if p == nil {
		return 0
	}
	return p.UncachedInputTokens + p.CacheReadTokens + p.CacheWriteTokens + p.OutputTokens
}

// BucketTotalTokens returns the mutually-exclusive token sum for one time bucket.
func BucketTotalTokens(b *v1.UsageBucketPoint) uint64 {
	if b == nil {
		return 0
	}
	return b.UncachedInputTokens + b.CacheReadTokens + b.CacheWriteTokens + b.OutputTokens
}

// SeriesKey identifies a unique (agent, model) pair. Never merged across CLIs.
func SeriesKey(agent v1.Agent, model string) string {
	return lists.AgentInfo(lists.EffectiveAgent(agent)).ID + ":" + model
}

// MatchesSeries checks whether an (agent, model) pair matches focusAgent and focusModel.
func MatchesSeries(agent v1.Agent, model string, focusAgent v1.Agent, focusModel string) bool {
	if focusAgent == v1.Agent_AGENT_UNSPECIFIED && focusModel == "" {
		return false
	}
	if focusAgent != v1.Agent_AGENT_UNSPECIFIED && lists.EffectiveAgent(agent) != lists.EffectiveAgent(focusAgent) {
		return false
	}
	if focusModel != "" && model != focusModel {
		return false
	}
	return true
}

// Deltas summarizes period-over-period comparisons.
type Deltas struct {
	HasTokens        bool
	TokensPercent    float64
	HasCacheHit      bool
	CacheHitPoints   float64
	HasSpeed         bool
	SpeedPercent     float64
	HasTtft          bool
	TtftDeltaPercent float64
}

// ComputeDeltas computes vs-previous deltas for s. Returns false flags when
// previous data is still backfilling or either period has < 3 relevant requests.
func ComputeDeltas(s *v1.UsageSeries, prevBackfilling bool) Deltas {
	if s == nil || s.Current == nil || s.Previous == nil || prevBackfilling {
		return Deltas{}
	}
	cur, prev := s.Current, s.Previous
	var out Deltas

	if cur.UsageRequestCount >= MinTrendRequests && prev.UsageRequestCount >= MinTrendRequests {
		cTok := TotalTokens(cur)
		pTok := TotalTokens(prev)
		if pTok > 0 {
			out.HasTokens = true
			out.TokensPercent = (float64(cTok) - float64(pTok)) / float64(pTok) * 100.0
		}
		if cur.HasCacheHitRate && prev.HasCacheHitRate {
			out.HasCacheHit = true
			out.CacheHitPoints = (cur.CacheHitRate - prev.CacheHitRate) * 100.0
		}
	}

	if cur.SpeedRequestCount >= MinTrendRequests && prev.SpeedRequestCount >= MinTrendRequests {
		if cur.HasSpeed && prev.HasSpeed && prev.SpeedP50 > 0 {
			out.HasSpeed = true
			out.SpeedPercent = (cur.SpeedP50 - prev.SpeedP50) / prev.SpeedP50 * 100.0
		}
		if cur.HasTtft && prev.HasTtft && prev.TtftP50Ms > 0 {
			out.HasTtft = true
			out.TtftDeltaPercent = (cur.TtftP50Ms - prev.TtftP50Ms) / prev.TtftP50Ms * 100.0
		}
	}
	return out
}

// ActiveSeries returns all series with at least 1 request in the current period,
// sorted by current total tokens descending (tie-broken by agent, model).
func ActiveSeries(resp *v1.UsageStatsResponse) []*v1.UsageSeries {
	if resp == nil || len(resp.Series) == 0 {
		return nil
	}
	out := make([]*v1.UsageSeries, 0, len(resp.Series))
	for _, s := range resp.Series {
		if s == nil || s.Current == nil || s.Current.UsageRequestCount == 0 {
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		ti := TotalTokens(out[i].Current)
		tj := TotalTokens(out[j].Current)
		if ti != tj {
			return ti > tj
		}
		if out[i].Agent != out[j].Agent {
			return out[i].Agent < out[j].Agent
		}
		return out[i].Model < out[j].Model
	})
	return out
}

// NiceCeiling picks the smallest clean chart ceiling from {1, 1.5, 2, 3, 4, 5, 8, 10} * 10^p >= peak.
func NiceCeiling(peak uint64) uint64 {
	if peak == 0 {
		return 1
	}
	v := float64(peak)
	exp := math.Floor(math.Log10(v))
	pow := math.Pow(10, exp)
	frac := v / pow
	var mult float64
	switch {
	case frac <= 1:
		mult = 1
	case frac <= 1.5:
		mult = 1.5
	case frac <= 2:
		mult = 2
	case frac <= 3:
		mult = 3
	case frac <= 4:
		mult = 4
	case frac <= 5:
		mult = 5
	case frac <= 8:
		mult = 8
	default:
		mult = 10
	}
	res := uint64(math.Round(mult * pow))
	if res < peak {
		return peak
	}
	return res
}

// ProjectionOptions controls sorting, single-model filtering, and bucket scrubbing.
type ProjectionOptions struct {
	SortBy            corev1.UsageSortColumn
	SelectedSeriesKey string
	HasScrub          bool
	ScrubBucketIndex  int32
}

// UsageProjection holds all derived view models for the Usage & Speed page.
type UsageProjection struct {
	HasAnyUsage      bool
	Hero             *corev1.UsageHeroView
	BarChart         *corev1.UsageBarChartView
	CacheHitStrip    *corev1.UsageMetricStripView
	OutputSpeedStrip *corev1.UsageMetricStripView
	TtftStrip        *corev1.UsageMetricStripView
	Rows             []*corev1.UsageModelRowView
}

func countCompositionRows(c *corev1.UsageCompositionView) int32 {
	if c == nil {
		return 0
	}
	var count int32
	if c.UncachedInputTokens > 0 {
		count++
	}
	if c.CacheReadTokens > 0 {
		count++
	}
	if c.CacheWriteTokens > 0 {
		count++
	}
	if c.OutputTokens > 0 {
		count++
	}
	if c.ReasoningOutputTokens > 0 {
		count++
	}
	return count
}

func buildStripView(
	allPts []*corev1.UsageStripPointView,
	selPts []*corev1.UsageStripPointView,
	hasSelected bool,
	hasPeriod bool,
	periodVal float64,
	minPad float64,
	scrubbing bool,
	scrubIdx int,
) *corev1.UsageMetricStripView {
	activePts := allPts
	if hasSelected {
		activePts = selPts
	}
	var minVal, maxVal float64
	var validVals []float64
	lastValid := int32(-1)
	for i, pt := range activePts {
		if pt == nil || !pt.HasValue {
			continue
		}
		if len(validVals) == 0 || pt.Value < minVal {
			minVal = pt.Value
		}
		if len(validVals) == 0 || pt.Value > maxVal {
			maxVal = pt.Value
		}
		validVals = append(validVals, pt.Value)
		lastValid = int32(i)
	}
	if len(validVals) == 0 {
		return &corev1.UsageMetricStripView{
			LastValidIndex: -1,
		}
	}

	var bothLo, bothHi float64
	hasBoth := false
	for _, list := range [][]*corev1.UsageStripPointView{allPts, selPts} {
		for _, pt := range list {
			if pt == nil || !pt.HasValue {
				continue
			}
			if !hasBoth || pt.Value < bothLo {
				bothLo = pt.Value
			}
			if !hasBoth || pt.Value > bothHi {
				bothHi = pt.Value
			}
			hasBoth = true
		}
	}
	pad := (bothHi - bothLo) * 0.15
	if pad <= 0 {
		pad = math.Abs(bothHi) * 0.05
	}
	if pad <= 0 {
		pad = minPad
	}
	scaleMin := bothLo - pad
	scaleMax := bothHi + pad

	var hasValue bool
	var curValue float64
	if scrubbing && scrubIdx >= 0 && scrubIdx < len(activePts) {
		if activePts[scrubIdx].HasValue {
			hasValue = true
			curValue = activePts[scrubIdx].Value
		}
	} else if hasPeriod {
		hasValue = true
		curValue = periodVal
	} else {
		sorted := make([]float64, len(validVals))
		copy(sorted, validVals)
		sort.Float64s(sorted)
		hasValue = true
		curValue = sorted[len(sorted)/2]
	}

	out := &corev1.UsageMetricStripView{
		HasValue:       hasValue,
		CurrentValue:   curValue,
		HasRange:       true,
		MinValue:       minVal,
		MaxValue:       maxVal,
		ScaleMin:       scaleMin,
		ScaleMax:       scaleMax,
		LastValidIndex: lastValid,
		AllPoints:      allPts,
	}
	if hasSelected {
		out.SelectedPoints = selPts
	}
	return out
}

// BuildUsageProjection computes all screen-ready view models for the redesigned Usage & Speed page.
func BuildUsageProjection(resp *v1.UsageStatsResponse, opts ProjectionOptions) UsageProjection {
	active := ActiveSeries(resp)
	if len(active) == 0 {
		return UsageProjection{
			HasAnyUsage: false,
			Hero: &corev1.UsageHeroView{
				Composition: &corev1.UsageCompositionView{},
			},
			BarChart:         &corev1.UsageBarChartView{},
			CacheHitStrip:    &corev1.UsageMetricStripView{LastValidIndex: -1},
			OutputSpeedStrip: &corev1.UsageMetricStripView{LastValidIndex: -1},
			TtftStrip:        &corev1.UsageMetricStripView{LastValidIndex: -1},
		}
	}

	prevBF := resp != nil && resp.PreviousPeriodBackfilling
	var allPrevTok, allPrevReqs uint64
	if resp != nil {
		for _, s := range resp.Series {
			if s != nil && s.Previous != nil {
				allPrevTok += TotalTokens(s.Previous)
				allPrevReqs += s.Previous.UsageRequestCount
			}
		}
	}

	var allCurTok, allCurReqs uint64
	allPeriodComp := &corev1.UsageCompositionView{}
	var allSpeedNum, allSpeedDenom float64
	var allTtftNum, allTtftDenom float64
	var sel *v1.UsageSeries

	for _, s := range active {
		eff := lists.EffectiveAgent(s.Agent)
		key := SeriesKey(eff, s.Model)
		if opts.SelectedSeriesKey != "" && key == opts.SelectedSeriesKey {
			sel = s
		}
		c := s.Current
		allCurTok += TotalTokens(c)
		allCurReqs += c.UsageRequestCount
		allPeriodComp.UncachedInputTokens += c.UncachedInputTokens
		allPeriodComp.CacheReadTokens += c.CacheReadTokens
		allPeriodComp.CacheWriteTokens += c.CacheWriteTokens
		allPeriodComp.OutputTokens += c.OutputTokens
		allPeriodComp.ReasoningOutputTokens += c.ReasoningOutputTokens
		if c.HasSpeed && c.SpeedP50 > 0 && c.SpeedRequestCount > 0 {
			w := float64(c.SpeedRequestCount)
			allSpeedNum += c.SpeedP50 * w
			allSpeedDenom += w
		}
		if c.HasTtft && c.TtftP50Ms > 0 && c.SpeedRequestCount > 0 {
			w := float64(c.SpeedRequestCount)
			allTtftNum += c.TtftP50Ms * w
			allTtftDenom += w
		}
	}

	selectedKey := ""
	if sel != nil {
		selectedKey = SeriesKey(lists.EffectiveAgent(sel.Agent), sel.Model)
	}

	maxRowTok := TotalTokens(active[0].Current)
	rows := make([]*corev1.UsageModelRowView, 0, len(active))
	for _, s := range active {
		eff := lists.EffectiveAgent(s.Agent)
		key := SeriesKey(eff, s.Model)
		c := s.Current
		tot := TotalTokens(c)
		deltas := ComputeDeltas(s, prevBF)
		var share float64
		if maxRowTok > 0 {
			share = float64(tot) / float64(maxRowTok)
		}
		rows = append(rows, &corev1.UsageModelRowView{
			Agent:               eff,
			AgentName:           lists.AgentInfo(eff).Name,
			Model:               s.Model,
			SeriesKey:           key,
			Selected:            selectedKey != "" && key == selectedKey,
			ShareRatio:          share,
			TotalTokens:         tot,
			HasCacheHit:         c.HasCacheHitRate,
			CacheHitRate:        c.CacheHitRate,
			HasSpeed:            c.HasSpeed && c.SpeedP50 > 0,
			SpeedP50:            c.SpeedP50,
			HasTtft:             c.HasTtft && c.TtftP50Ms > 0,
			TtftP50Ms:           c.TtftP50Ms,
			HasTokensDelta:      deltas.HasTokens,
			TokensDeltaPercent:  deltas.TokensPercent,
			PreviousBackfilling: prevBF,
			UsageRequestCount:   c.UsageRequestCount,
			SpeedRequestCount:   c.SpeedRequestCount,
		})
	}

	sortBy := opts.SortBy
	if sortBy == corev1.UsageSortColumn_USAGE_SORT_COLUMN_UNSPECIFIED {
		sortBy = corev1.UsageSortColumn_USAGE_SORT_COLUMN_TOKENS
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		switch sortBy {
		case corev1.UsageSortColumn_USAGE_SORT_COLUMN_CACHE_HIT:
			if a.HasCacheHit != b.HasCacheHit {
				return a.HasCacheHit
			}
			if a.HasCacheHit && a.CacheHitRate != b.CacheHitRate {
				return a.CacheHitRate > b.CacheHitRate
			}
		case corev1.UsageSortColumn_USAGE_SORT_COLUMN_SPEED:
			if a.HasSpeed != b.HasSpeed {
				return a.HasSpeed
			}
			if a.HasSpeed && a.SpeedP50 != b.SpeedP50 {
				return a.SpeedP50 > b.SpeedP50
			}
		case corev1.UsageSortColumn_USAGE_SORT_COLUMN_TTFT:
			if a.HasTtft != b.HasTtft {
				return a.HasTtft
			}
			if a.HasTtft && a.TtftP50Ms != b.TtftP50Ms {
				return a.TtftP50Ms < b.TtftP50Ms
			}
		}
		if a.TotalTokens != b.TotalTokens {
			return a.TotalTokens > b.TotalTokens
		}
		if a.Agent != b.Agent {
			return a.Agent < b.Agent
		}
		return a.Model < b.Model
	})

	bucketCount := 0
	for _, s := range active {
		if len(s.Buckets) > bucketCount {
			bucketCount = len(s.Buckets)
		}
	}

	scrubbing := opts.HasScrub && bucketCount > 0 && opts.ScrubBucketIndex >= 0 && int(opts.ScrubBucketIndex) < bucketCount
	scrubIdx := int(opts.ScrubBucketIndex)

	barBuckets := make([]*corev1.UsageBarBucketView, bucketCount)
	allCachePts := make([]*corev1.UsageStripPointView, bucketCount)
	allSpeedPts := make([]*corev1.UsageStripPointView, bucketCount)
	allTtftPts := make([]*corev1.UsageStripPointView, bucketCount)
	var selCachePts, selSpeedPts, selTtftPts []*corev1.UsageStripPointView
	if sel != nil {
		selCachePts = make([]*corev1.UsageStripPointView, bucketCount)
		selSpeedPts = make([]*corev1.UsageStripPointView, bucketCount)
		selTtftPts = make([]*corev1.UsageStripPointView, bucketCount)
	}

	var peakAllTok uint64
	var scrubStartMs, scrubEndMs int64
	var scrubTok, scrubReqs uint64
	scrubComp := &corev1.UsageCompositionView{}

	for i := 0; i < bucketCount; i++ {
		var startMs, endMs int64
		var bAllTok, bInDenom, bCacheRead uint64
		var bUncached, bWrite, bOut, bReas, bReqs uint64
		var bSpNum, bSpDenom, bTtNum, bTtDenom float64

		for _, s := range active {
			if i >= len(s.Buckets) || s.Buckets[i] == nil {
				continue
			}
			b := s.Buckets[i]
			if startMs == 0 && endMs == 0 {
				startMs, endMs = b.StartMs, b.EndMs
			}
			t := BucketTotalTokens(b)
			bAllTok += t
			bUncached += b.UncachedInputTokens
			bCacheRead += b.CacheReadTokens
			bWrite += b.CacheWriteTokens
			bOut += b.OutputTokens
			bReas += b.ReasoningOutputTokens
			bReqs += b.UsageRequestCount
			bInDenom += b.UncachedInputTokens + b.CacheReadTokens + b.CacheWriteTokens
			if b.SpeedRequestCount > 0 && b.SpeedP50 > 0 {
				w := float64(b.SpeedRequestCount)
				bSpNum += b.SpeedP50 * w
				bSpDenom += w
			}
			if b.SpeedRequestCount > 0 && b.TtftP50Ms > 0 {
				w := float64(b.SpeedRequestCount)
				bTtNum += b.TtftP50Ms * w
				bTtDenom += w
			}
		}

		var bSelTok uint64
		if sel != nil && i < len(sel.Buckets) && sel.Buckets[i] != nil {
			sb := sel.Buckets[i]
			bSelTok = BucketTotalTokens(sb)
			selDen := sb.UncachedInputTokens + sb.CacheReadTokens + sb.CacheWriteTokens
			if selDen > 0 {
				selCachePts[i] = &corev1.UsageStripPointView{
					HasValue: true,
					Value:    float64(sb.CacheReadTokens) / float64(selDen),
				}
			} else {
				selCachePts[i] = &corev1.UsageStripPointView{}
			}
			if sb.SpeedRequestCount > 0 && sb.SpeedP50 > 0 {
				selSpeedPts[i] = &corev1.UsageStripPointView{HasValue: true, Value: sb.SpeedP50}
			} else {
				selSpeedPts[i] = &corev1.UsageStripPointView{}
			}
			if sb.SpeedRequestCount > 0 && sb.TtftP50Ms > 0 {
				selTtftPts[i] = &corev1.UsageStripPointView{HasValue: true, Value: sb.TtftP50Ms}
			} else {
				selTtftPts[i] = &corev1.UsageStripPointView{}
			}
		} else if sel != nil {
			selCachePts[i] = &corev1.UsageStripPointView{}
			selSpeedPts[i] = &corev1.UsageStripPointView{}
			selTtftPts[i] = &corev1.UsageStripPointView{}
		}

		if bAllTok > peakAllTok {
			peakAllTok = bAllTok
		}
		barBuckets[i] = &corev1.UsageBarBucketView{
			StartMs:        startMs,
			EndMs:          endMs,
			AllTokens:      bAllTok,
			SelectedTokens: bSelTok,
			HasData:        bAllTok > 0,
		}

		if bInDenom > 0 {
			allCachePts[i] = &corev1.UsageStripPointView{
				HasValue: true,
				Value:    float64(bCacheRead) / float64(bInDenom),
			}
		} else {
			allCachePts[i] = &corev1.UsageStripPointView{}
		}
		if bSpDenom > 0 {
			allSpeedPts[i] = &corev1.UsageStripPointView{HasValue: true, Value: bSpNum / bSpDenom}
		} else {
			allSpeedPts[i] = &corev1.UsageStripPointView{}
		}
		if bTtDenom > 0 {
			allTtftPts[i] = &corev1.UsageStripPointView{HasValue: true, Value: bTtNum / bTtDenom}
		} else {
			allTtftPts[i] = &corev1.UsageStripPointView{}
		}

		if scrubbing && i == scrubIdx {
			scrubStartMs = startMs
			scrubEndMs = endMs
			if sel != nil {
				if i < len(sel.Buckets) && sel.Buckets[i] != nil {
					sb := sel.Buckets[i]
					scrubTok = bSelTok
					scrubReqs = sb.UsageRequestCount
					scrubComp = &corev1.UsageCompositionView{
						UncachedInputTokens:   sb.UncachedInputTokens,
						CacheReadTokens:       sb.CacheReadTokens,
						CacheWriteTokens:      sb.CacheWriteTokens,
						OutputTokens:          sb.OutputTokens,
						ReasoningOutputTokens: sb.ReasoningOutputTokens,
					}
				}
			} else {
				scrubTok = bAllTok
				scrubReqs = bReqs
				scrubComp = &corev1.UsageCompositionView{
					UncachedInputTokens:   bUncached,
					CacheReadTokens:       bCacheRead,
					CacheWriteTokens:      bWrite,
					OutputTokens:          bOut,
					ReasoningOutputTokens: bReas,
				}
			}
		}
	}

	yMax := NiceCeiling(peakAllTok)
	barChart := &corev1.UsageBarChartView{
		YMax:    yMax,
		YMid:    yMax / 2,
		Buckets: barBuckets,
	}

	var periodCacheHas bool
	var periodCacheVal float64
	var periodSpeedHas bool
	var periodSpeedVal float64
	var periodTtftHas bool
	var periodTtftVal float64

	if sel != nil {
		c := sel.Current
		periodCacheHas = c.HasCacheHitRate
		periodCacheVal = c.CacheHitRate
		periodSpeedHas = c.HasSpeed && c.SpeedP50 > 0
		periodSpeedVal = c.SpeedP50
		periodTtftHas = c.HasTtft && c.TtftP50Ms > 0
		periodTtftVal = c.TtftP50Ms
	} else {
		allInDenom := allPeriodComp.UncachedInputTokens + allPeriodComp.CacheReadTokens + allPeriodComp.CacheWriteTokens
		if allInDenom > 0 {
			periodCacheHas = true
			periodCacheVal = float64(allPeriodComp.CacheReadTokens) / float64(allInDenom)
		}
		if allSpeedDenom > 0 {
			periodSpeedHas = true
			periodSpeedVal = allSpeedNum / allSpeedDenom
		}
		if allTtftDenom > 0 {
			periodTtftHas = true
			periodTtftVal = allTtftNum / allTtftDenom
		}
	}

	cacheStrip := buildStripView(allCachePts, selCachePts, sel != nil, periodCacheHas, periodCacheVal, 0.05, scrubbing, scrubIdx)
	speedStrip := buildStripView(allSpeedPts, selSpeedPts, sel != nil, periodSpeedHas, periodSpeedVal, 1.0, scrubbing, scrubIdx)
	ttftStrip := buildStripView(allTtftPts, selTtftPts, sel != nil, periodTtftHas, periodTtftVal, 1.0, scrubbing, scrubIdx)

	periodComp := allPeriodComp
	if sel != nil {
		periodComp = &corev1.UsageCompositionView{
			UncachedInputTokens:   sel.Current.UncachedInputTokens,
			CacheReadTokens:       sel.Current.CacheReadTokens,
			CacheWriteTokens:      sel.Current.CacheWriteTokens,
			OutputTokens:          sel.Current.OutputTokens,
			ReasoningOutputTokens: sel.Current.ReasoningOutputTokens,
		}
	}

	hero := &corev1.UsageHeroView{
		Scrubbing:             scrubbing,
		ScrubBucketIndex:      opts.ScrubBucketIndex,
		ScrubStartMs:          scrubStartMs,
		ScrubEndMs:            scrubEndMs,
		TotalModelCount:       int32(len(active)),
		PreviousBackfilling:   prevBF,
		PeriodCompositionRows: countCompositionRows(periodComp),
	}
	if sel != nil {
		eff := lists.EffectiveAgent(sel.Agent)
		hero.HasSelectedModel = true
		hero.SelectedAgent = eff
		hero.SelectedAgentName = lists.AgentInfo(eff).Name
		hero.SelectedModel = sel.Model
		hero.SelectedSeriesKey = selectedKey
	}
	if scrubbing {
		hero.TotalTokens = scrubTok
		hero.RequestCount = scrubReqs
		hero.Composition = scrubComp
	} else {
		hero.Composition = periodComp
		if sel != nil {
			selTok := TotalTokens(sel.Current)
			hero.TotalTokens = selTok
			hero.RequestCount = sel.Current.UsageRequestCount
			if allCurTok > 0 {
				hero.HasSharePercent = true
				hero.ShareRatio = float64(selTok) / float64(allCurTok)
			}
			deltas := ComputeDeltas(sel, prevBF)
			hero.HasTokensDelta = deltas.HasTokens
			hero.TokensDeltaPercent = deltas.TokensPercent
		} else {
			hero.TotalTokens = allCurTok
			hero.RequestCount = allCurReqs
			if !prevBF && allCurReqs >= MinTrendRequests && allPrevReqs >= MinTrendRequests && allPrevTok > 0 {
				hero.HasTokensDelta = true
				hero.TokensDeltaPercent = (float64(allCurTok) - float64(allPrevTok)) / float64(allPrevTok) * 100.0
			}
		}
	}

	return UsageProjection{
		HasAnyUsage:      true,
		Hero:             hero,
		BarChart:         barChart,
		CacheHitStrip:    cacheStrip,
		OutputSpeedStrip: speedStrip,
		TtftStrip:        ttftStrip,
		Rows:             rows,
	}
}

// FormatCommaUint formats an integer with thousands commas (e.g. 8,420).
func FormatCommaUint(n uint64) string {
	s := strconv.FormatUint(n, 10)
	if len(s) <= 3 {
		return s
	}
	out := make([]byte, 0, len(s)+(len(s)-1)/3)
	rem := len(s) % 3
	if rem == 0 {
		rem = 3
	}
	out = append(out, s[:rem]...)
	for i := rem; i < len(s); i += 3 {
		out = append(out, ',')
		out = append(out, s[i:i+3]...)
	}
	return string(out)
}

func formatOneDecimal(v float64) string {
	return trimDotZero(strconv.FormatFloat(v, 'f', 1, 64))
}

func trimDotZero(s string) string {
	if len(s) > 2 && s[len(s)-2:] == ".0" {
		return s[:len(s)-2]
	}
	return s
}

// FormatTokens formats token counts (zh-CN: 万/亿, en: k/M/B)
// with overflow-safe rounding (e.g. 99,960,000 -> 1亿; 999,600 -> 1M; 128.6亿).
func FormatTokens(n uint64, english bool) string {
	if n == 0 {
		return "0"
	}
	if !english {
		if n < 10_000 {
			return FormatCommaUint(n)
		}
		if n < 100_000_000 {
			if n >= 99_950_000 {
				return "1亿"
			}
			if n < 1_000_000 {
				v := math.Round(float64(n)/1_000.0) / 10.0
				return formatOneDecimal(v) + "万"
			}
			wan := uint64(math.Round(float64(n) / 10_000.0))
			if wan >= 10_000 {
				yi := math.Round(float64(wan)/1_000.0) / 10.0
				return formatOneDecimal(yi) + "亿"
			}
			return FormatCommaUint(wan) + "万"
		}
		if n < 99_995_000_000 {
			v := math.Round(float64(n)/10_000_000.0) / 10.0
			return formatOneDecimal(v) + "亿"
		}
		yi := uint64(math.Round(float64(n) / 100_000_000.0))
		return FormatCommaUint(yi) + "亿"
	}

	if n < 1_000 {
		return strconv.FormatUint(n, 10)
	}
	if n < 1_000_000 {
		if n < 10_000 {
			v := math.Round(float64(n)/100.0) / 10.0
			return formatOneDecimal(v) + "k"
		}
		k := uint64(math.Round(float64(n) / 1_000.0))
		if k >= 1_000 {
			return "1M"
		}
		return strconv.FormatUint(k, 10) + "k"
	}
	if n < 1_000_000_000 {
		if n < 100_000_000 {
			v := math.Round(float64(n)/100_000.0) / 10.0
			if v >= 1000 {
				return "1B"
			}
			return formatOneDecimal(v) + "M"
		}
		m := uint64(math.Round(float64(n) / 1_000_000.0))
		if m >= 1_000 {
			return "1B"
		}
		return strconv.FormatUint(m, 10) + "M"
	}
	if n < 99_950_000_000 {
		v := math.Round(float64(n)/100_000_000.0) / 10.0
		return formatOneDecimal(v) + "B"
	}
	b := uint64(math.Round(float64(n) / 1_000_000_000.0))
	return FormatCommaUint(b) + "B"
}

// FormatCacheHit formats a [0..1] cache hit rate without lying near 0% or 100%.
func FormatCacheHit(rate float64) string {
	if rate <= 0 {
		return "0%"
	}
	if rate >= 1 {
		return "100%"
	}
	pct := rate * 100.0
	if pct < 1.0 {
		r := math.Round(pct*10.0) / 10.0
		if r < 0.1 {
			r = 0.1
		}
		return formatOneDecimal(r) + "%"
	}
	if pct > 99.0 {
		r := math.Round(pct*10.0) / 10.0
		if r > 99.9 {
			r = 99.9
		}
		return formatOneDecimal(r) + "%"
	}
	return strconv.Itoa(int(math.Round(pct))) + "%"
}

// FormatSpeedValue formats output speed scalar without unit.
func FormatSpeedValue(tokPerSec float64) string {
	if tokPerSec <= 0 {
		return "0"
	}
	if tokPerSec < 10 {
		return formatOneDecimal(math.Round(tokPerSec*10.0) / 10.0)
	}
	rounded := uint64(math.Round(tokPerSec))
	return FormatCommaUint(rounded)
}
