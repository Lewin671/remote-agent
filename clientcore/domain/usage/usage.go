// Package usage provides pure-Go, tzdata-free aggregation, delta calculation,
// and formatting helpers for model usage, cache hit rate, output speed, and TTFT.
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

// Deltas summarizes period-over-period comparisons following design-system.md rules.
type Deltas struct {
	HasTokens         bool
	TokensPercent     float64
	HasCacheHit       bool
	CacheHitPoints    float64
	HasSpeed          bool
	SpeedPercent      float64
	HasTtft           bool
	TtftDeltaPercent  float64
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

// OrderForScreen promotes focused series first while preserving descending token order for the rest.
func OrderForScreen(active []*v1.UsageSeries, focusAgent v1.Agent, focusModel string) []*v1.UsageSeries {
	if len(active) <= 1 || (focusAgent == v1.Agent_AGENT_UNSPECIFIED && focusModel == "") {
		return active
	}
	var head, tail []*v1.UsageSeries
	for _, s := range active {
		if MatchesSeries(s.Agent, s.Model, focusAgent, focusModel) {
			head = append(head, s)
		} else {
			tail = append(tail, s)
		}
	}
	if len(head) == 0 {
		return active
	}
	return append(head, tail...)
}

// BuildSeriesViews constructs UsageSeriesView protos sorted and assigned pattern indices (0..3 for default top 3 + 4th overlay slot, -1 beyond).
func BuildSeriesViews(resp *v1.UsageStatsResponse, focusAgent v1.Agent, focusModel string) []*corev1.UsageSeriesView {
	active := OrderForScreen(ActiveSeries(resp), focusAgent, focusModel)
	out := make([]*corev1.UsageSeriesView, 0, len(active))
	prevBF := resp != nil && resp.PreviousPeriodBackfilling
	for idx, s := range active {
		deltas := ComputeDeltas(s, prevBF)
		pat := int32(-1)
		if idx < 4 {
			pat = int32(idx)
		}
		buckets := make([]*v1.UsageBucketPoint, 0, len(s.Buckets))
		for _, b := range s.Buckets {
			if b != nil {
				buckets = append(buckets, b.Clone())
			}
		}
		var curClone, prevClone *v1.UsagePeriodSummary
		if s.Current != nil {
			curClone = s.Current.Clone()
		}
		if s.Previous != nil {
			prevClone = s.Previous.Clone()
		}
		effAgent := lists.EffectiveAgent(s.Agent)
		out = append(out, &corev1.UsageSeriesView{
			Agent:               effAgent,
			AgentName:           lists.AgentInfo(effAgent).Name,
			Model:               s.Model,
			SeriesKey:           SeriesKey(effAgent, s.Model),
			PatternIndex:        pat,
			Focused:             MatchesSeries(effAgent, s.Model, focusAgent, focusModel),
			CacheWriteSupported: s.CacheWriteSupported,
			FewSpeedSamples:     s.Current.SpeedRequestCount > 0 && s.Current.SpeedRequestCount < MinTrendRequests,
			Current:             curClone,
			Previous:            prevClone,
			HasTokensDelta:      deltas.HasTokens,
			TokensDeltaPercent:  deltas.TokensPercent,
			HasCacheHitDelta:    deltas.HasCacheHit,
			CacheHitDeltaPoints: deltas.CacheHitPoints,
			HasSpeedDelta:       deltas.HasSpeed,
			SpeedDeltaPercent:   deltas.SpeedPercent,
			HasTtftDelta:        deltas.HasTtft,
			TtftDeltaPercent:    deltas.TtftDeltaPercent,
			PreviousBackfilling: prevBF,
			Buckets:             buckets,
		})
	}
	return out
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

// FormatTokens formats token counts per design-system.md (zh-CN: 万/亿, en: k/M/B)
// with overflow-safe rounding (e.g. 99,960,000 -> 1亿; 999,600 -> 1M).
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
		if n < 10_000_000_000 {
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
	if n < 100_000_000_000 {
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
