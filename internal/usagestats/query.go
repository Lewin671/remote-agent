package usagestats

import (
	"context"
	"slices"
	"sort"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

type QueryOptions struct {
	Installed []pb.Agent
	Disabled  []pb.Agent
}

type seriesKey struct {
	agent pb.Agent
	model string
}

type bucketAcc struct {
	uncached        uint64
	cacheRead       uint64
	cacheWrite      uint64
	output          uint64
	reasoning       uint64
	usageReqCount   uint64
	speedReqCount   uint64
	cwSupported     bool
	speeds          []float64
	ttfts           []float64
}

func (a *bucketAcc) addSample(uncached, cacheRead, cacheWrite, output, reasoning, usageReq, speedReq uint64, cwSup bool, sp, tt []float64) {
	a.uncached += uncached
	a.cacheRead += cacheRead
	a.cacheWrite += cacheWrite
	a.output += output
	a.reasoning += reasoning
	a.usageReqCount += usageReq
	a.speedReqCount += speedReq
	if cwSup {
		a.cwSupported = true
	}
	a.speeds = append(a.speeds, sp...)
	a.ttfts = append(a.ttfts, tt...)
}

func (a *bucketAcc) toPeriodSummary() *pb.UsagePeriodSummary {
	out := &pb.UsagePeriodSummary{
		UncachedInputTokens:   a.uncached,
		CacheReadTokens:       a.cacheRead,
		CacheWriteTokens:      a.cacheWrite,
		OutputTokens:          a.output,
		ReasoningOutputTokens: a.reasoning,
		UsageRequestCount:     a.usageReqCount,
		SpeedRequestCount:     a.speedReqCount,
	}
	inDenom := a.uncached + a.cacheRead + a.cacheWrite
	if inDenom > 0 {
		out.HasCacheHitRate = true
		out.CacheHitRate = float64(a.cacheRead) / float64(inDenom)
	}
	if p10, p50, p90, ok := computePercentiles(a.speeds); ok {
		out.HasSpeed = true
		out.SpeedP10, out.SpeedP50, out.SpeedP90 = p10, p50, p90
	}
	if p10, p50, p90, ok := computePercentiles(a.ttfts); ok {
		out.HasTtft = true
		out.TtftP10Ms, out.TtftP50Ms, out.TtftP90Ms = p10, p50, p90
	}
	return out
}

func (c *Collector) Query(ctx context.Context, req *pb.GetUsageStatsRequest, opts QueryOptions) (*pb.UsageStatsResponse, error) {
	if req == nil {
		req = &pb.GetUsageStatsRequest{}
	}
	c.RefreshNow(ctx)

	now := c.cfg.Now()
	preset := req.Preset
	if preset == pb.UsageRangePreset_USAGE_RANGE_PRESET_UNSPECIFIED {
		preset = pb.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS
	}

	loc := resolveLocation(req.Timezone, req.TzOffsetMinutes)
	bounds, gran, prevStartMs := resolveBounds(now, loc, preset, req)
	startMs := bounds[0]
	endMs := bounds[len(bounds)-1]

	enabledMap := map[pb.Agent]bool{}
	for _, a := range opts.Installed {
		if req.IncludeDisabled || !slices.Contains(opts.Disabled, a) {
			enabledMap[a] = true
		}
	}

	sources := buildAgentSources(c, enabledMap)

	rawCutoffMs := now.Add(-rawRetentionDays * 24 * time.Hour).UnixMilli()
	curBuckets, err := c.loadRangeBuckets(prevStartMs, bounds, rawCutoffMs, enabledMap)
	if err != nil {
		return nil, err
	}

	seriesKeys := make([]seriesKey, 0, len(curBuckets))
	for k := range curBuckets {
		seriesKeys = append(seriesKeys, k)
	}

	recent10BySeries := c.loadRecent10Speeds(enabledMap)

	backfilling := c.backfilling.Load()
	scannedDays := c.scannedDays.Load()
	totalDays := c.totalDays.Load()
	prevBackfilling := false
	if backfilling {
		oldestScanned := now.Add(-time.Duration(scannedDays) * 24 * time.Hour).UnixMilli()
		if prevStartMs < oldestScanned {
			prevBackfilling = true
		}
	}

	seriesList := make([]*pb.UsageSeries, 0, len(seriesKeys))
	for _, k := range seriesKeys {
		sb := curBuckets[k]
		curSum := &bucketAcc{}
		bucketProtos := make([]*pb.UsageBucketPoint, len(bounds)-1)
		cwSupported := sb.prev.cwSupported
		for i := 0; i < len(bounds)-1; i++ {
			b := sb.buckets[i]
			if b == nil {
				bucketProtos[i] = &pb.UsageBucketPoint{StartMs: bounds[i], EndMs: bounds[i+1]}
				continue
			}
			if b.cwSupported {
				cwSupported = true
			}
			curSum.addSample(b.uncached, b.cacheRead, b.cacheWrite, b.output, b.reasoning, b.usageReqCount, b.speedReqCount, b.cwSupported, b.speeds, b.ttfts)
			bp := &pb.UsageBucketPoint{
				StartMs:               bounds[i],
				EndMs:                 bounds[i+1],
				UncachedInputTokens:   b.uncached,
				CacheReadTokens:       b.cacheRead,
				CacheWriteTokens:      b.cacheWrite,
				OutputTokens:          b.output,
				ReasoningOutputTokens: b.reasoning,
				UsageRequestCount:     b.usageReqCount,
				SpeedRequestCount:     b.speedReqCount,
			}
			if p10, p50, p90, ok := computePercentiles(b.speeds); ok {
				bp.SpeedP10, bp.SpeedP50, bp.SpeedP90 = p10, p50, p90
			}
			if p10, p50, p90, ok := computePercentiles(b.ttfts); ok {
				bp.TtftP10Ms, bp.TtftP50Ms, bp.TtftP90Ms = p10, p50, p90
			}
			bucketProtos[i] = bp
		}

		curSummary := curSum.toPeriodSummary()
		if r10 := recent10BySeries[k]; len(r10) > 0 {
			if _, med, _, ok := computePercentiles(r10); ok {
				curSummary.Recent10SpeedCount = uint64(len(r10))
				curSummary.Recent10SpeedP50 = med
			}
		}
		prevSummary := sb.prev.toPeriodSummary()

		if curSummary.UsageRequestCount == 0 && prevSummary.UsageRequestCount == 0 {
			continue
		}

		seriesList = append(seriesList, &pb.UsageSeries{
			Agent:               k.agent,
			Model:               k.model,
			CacheWriteSupported: cwSupported,
			Current:             curSummary,
			Previous:            prevSummary,
			Buckets:             bucketProtos,
		})
	}

	sort.Slice(seriesList, func(i, j int) bool {
		ti := totalTokensOf(seriesList[i].Current)
		tj := totalTokensOf(seriesList[j].Current)
		if ti != tj {
			return ti > tj
		}
		if seriesList[i].Agent != seriesList[j].Agent {
			return seriesList[i].Agent < seriesList[j].Agent
		}
		return seriesList[i].Model < seriesList[j].Model
	})

	return &pb.UsageStatsResponse{
		Preset:                  preset,
		Granularity:             gran,
		StartMs:                 startMs,
		EndMs:                   endMs,
		GeneratedAtMs:           now.UnixMilli(),
		Backfilling:             backfilling,
		BackfillScannedDays:     scannedDays,
		BackfillTotalDays:       totalDays,
		PreviousPeriodBackfilling: prevBackfilling,
		Sources:                 sources,
		Series:                  seriesList,
	}, nil
}

func totalTokensOf(p *pb.UsagePeriodSummary) uint64 {
	if p == nil {
		return 0
	}
	return p.UncachedInputTokens + p.CacheReadTokens + p.CacheWriteTokens + p.OutputTokens
}

func buildAgentSources(c *Collector, enabled map[pb.Agent]bool) []*pb.UsageAgentSource {
	order := []pb.Agent{pb.Agent_AGENT_CODEX, pb.Agent_AGENT_CLAUDE, pb.Agent_AGENT_ANTIGRAVITY}
	var out []*pb.UsageAgentSource
	for _, ag := range order {
		if !enabled[ag] {
			continue
		}
		src := &pb.UsageAgentSource{Agent: ag}
		switch ag {
		case pb.Agent_AGENT_ANTIGRAVITY:
			src.State = pb.UsageSourceState_USAGE_SOURCE_STATE_UNAVAILABLE
			src.Detail = "antigravity transcript lacks per-request usage and timing telemetry"
		default:
			if msg := c.agentErr(ag); msg != "" {
				src.State = pb.UsageSourceState_USAGE_SOURCE_STATE_FAILED
				src.Detail = msg
			} else {
				src.State = pb.UsageSourceState_USAGE_SOURCE_STATE_OK
			}
		}
		out = append(out, src)
	}
	return out
}

func resolveLocation(tz string, offsetMinutes int32) *time.Location {
	if tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			return l
		}
	}
	if offsetMinutes != 0 {
		// offsetMinutes is minutes east of UTC (e.g. +480 for Asia/Shanghai, -480 for America/Los_Angeles).
		return time.FixedZone("client", int(offsetMinutes)*60)
	}
	return time.Local
}

func resolveBounds(now time.Time, loc *time.Location, preset pb.UsageRangePreset, req *pb.GetUsageStatsRequest) (bounds []int64, gran pb.UsageBucketGranularity, prevStartMs int64) {
	if len(req.BucketBoundsMs) >= 2 && isStrictlyIncreasing(req.BucketBoundsMs) {
		n := len(req.BucketBoundsMs) - 1
		spanMs := req.BucketBoundsMs[n] - req.BucketBoundsMs[0]
		avgBucketMs := spanMs / int64(n)
		switch {
		case avgBucketMs <= 2*3600*1000:
			gran = pb.UsageBucketGranularity_USAGE_BUCKET_GRANULARITY_HOUR
		case avgBucketMs <= 12*3600*1000:
			gran = pb.UsageBucketGranularity_USAGE_BUCKET_GRANULARITY_SIX_HOURS
		default:
			gran = pb.UsageBucketGranularity_USAGE_BUCKET_GRANULARITY_DAY
		}
		prev := req.PrevStartMs
		if prev <= 0 || prev >= req.BucketBoundsMs[0] {
			prev = req.BucketBoundsMs[0] - spanMs
		}
		return req.BucketBoundsMs, gran, prev
	}

	localNow := now.In(loc)
	todayStart := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, loc)
	tomorrowStart := todayStart.AddDate(0, 0, 1)
	minDay := todayStart.AddDate(0, 0, -(maxRetentionDays - 1))

	var startDay, endExcl time.Time
	switch preset {
	case pb.UsageRangePreset_USAGE_RANGE_PRESET_TODAY:
		startDay = todayStart
		endExcl = tomorrowStart
	case pb.UsageRangePreset_USAGE_RANGE_PRESET_THIRTY_DAYS:
		startDay = todayStart.AddDate(0, 0, -29)
		endExcl = tomorrowStart
	case pb.UsageRangePreset_USAGE_RANGE_PRESET_CUSTOM:
		cs := time.UnixMilli(req.CustomStartMs).In(loc)
		ce := time.UnixMilli(req.CustomEndMs).In(loc)
		if req.CustomStartMs <= 0 || req.CustomEndMs <= 0 {
			startDay = todayStart.AddDate(0, 0, -6)
			endExcl = tomorrowStart
			break
		}
		startDay = time.Date(cs.Year(), cs.Month(), cs.Day(), 0, 0, 0, 0, loc)
		endInc := time.Date(ce.Year(), ce.Month(), ce.Day(), 0, 0, 0, 0, loc)
		if endInc.Before(startDay) {
			endInc = startDay
		}
		if startDay.Before(minDay) {
			startDay = minDay
		}
		if endInc.After(todayStart) {
			endInc = todayStart
		}
		if endInc.Before(startDay) {
			startDay = endInc
		}
		endExcl = endInc.AddDate(0, 0, 1)
	default: // SEVEN_DAYS
		startDay = todayStart.AddDate(0, 0, -6)
		endExcl = tomorrowStart
	}

	numDays := countLocalDays(startDay, endExcl)
	switch {
	case numDays <= 1:
		gran = pb.UsageBucketGranularity_USAGE_BUCKET_GRANULARITY_HOUR
		cur := startDay
		for cur.Before(endExcl) {
			bounds = append(bounds, cur.UnixMilli())
			cur = cur.Add(time.Hour)
		}
		bounds = append(bounds, endExcl.UnixMilli())
	case numDays <= 3:
		gran = pb.UsageBucketGranularity_USAGE_BUCKET_GRANULARITY_SIX_HOURS
		day := startDay
		for day.Before(endExcl) {
			nextDay := day.AddDate(0, 0, 1)
			dHours := nextDay.Sub(day)
			// Split the local calendar day cleanly into 4 quarters even across 23h/25h DST transitions.
			for q := 0; q < 4; q++ {
				t := day.Add(time.Duration(q) * dHours / 4)
				bounds = append(bounds, t.UnixMilli())
			}
			day = nextDay
		}
		bounds = append(bounds, endExcl.UnixMilli())
	default:
		gran = pb.UsageBucketGranularity_USAGE_BUCKET_GRANULARITY_DAY
		cur := startDay
		for cur.Before(endExcl) {
			bounds = append(bounds, cur.UnixMilli())
			cur = cur.AddDate(0, 0, 1)
		}
		bounds = append(bounds, endExcl.UnixMilli())
	}

	prevStartDay := startDay.AddDate(0, 0, -numDays)
	return bounds, gran, prevStartDay.UnixMilli()
}

func countLocalDays(startDay, endExcl time.Time) int {
	n := 0
	for cur := startDay; cur.Before(endExcl); cur = cur.AddDate(0, 0, 1) {
		n++
		if n > maxRetentionDays+2 {
			break
		}
	}
	if n < 1 {
		return 1
	}
	return n
}

func isStrictlyIncreasing(xs []int64) bool {
	for i := 1; i < len(xs); i++ {
		if xs[i] <= xs[i-1] {
			return false
		}
	}
	return true
}

type seriesBuckets struct {
	buckets map[int]*bucketAcc
	prev    *bucketAcc
}

func (c *Collector) loadRangeBuckets(prevStartMs int64, bounds []int64, rawCutoffMs int64, enabled map[pb.Agent]bool) (map[seriesKey]*seriesBuckets, error) {
	c.store.mu.Lock()
	defer c.store.mu.Unlock()

	startMs := bounds[0]
	endMs := bounds[len(bounds)-1]
	res := map[seriesKey]*seriesBuckets{}
	ensure := func(k seriesKey) *seriesBuckets {
		sb := res[k]
		if sb == nil {
			sb = &seriesBuckets{buckets: map[int]*bucketAcc{}, prev: &bucketAcc{}}
			res[k] = sb
		}
		return sb
	}
	addPoint := func(k seriesKey, tsMs int64, uncached, cacheRead, cacheWrite, output, reasoning, usageReq, speedReq uint64, cwSup bool, sp, tt []float64) {
		if !enabled[k.agent] || k.model == "" {
			return
		}
		sb := ensure(k)
		if tsMs < startMs {
			if tsMs >= prevStartMs {
				sb.prev.addSample(uncached, cacheRead, cacheWrite, output, reasoning, usageReq, speedReq, cwSup, sp, tt)
			}
			return
		}
		idx := sort.Search(len(bounds)-1, func(i int) bool { return bounds[i+1] > tsMs })
		if idx < 0 || idx >= len(bounds)-1 {
			return
		}
		ba := sb.buckets[idx]
		if ba == nil {
			ba = &bucketAcc{}
			sb.buckets[idx] = ba
		}
		ba.addSample(uncached, cacheRead, cacheWrite, output, reasoning, usageReq, speedReq, cwSup, sp, tt)
	}

	// Partition cleanly on an exact UTC hour boundary inside raw retention so
	// hourly_buckets and raw_requests never overlap or skip a partial boundary hour.
	rawSplitHour := hourFloorMs(rawCutoffMs) + 3600*1000
	if rawCutoffMs == hourFloorMs(rawCutoffMs) {
		rawSplitHour = rawCutoffMs
	}

	// 1. Query raw_requests for [max(prevStartMs, rawSplitHour), endMs)
	rawFrom := max(prevStartMs, rawSplitHour)
	if rawFrom < endMs {
		rows, err := c.store.db.Query(
			`SELECT agent, model, ts_ms, uncached_input, cache_read, cache_write, output, reasoning_output, cache_write_supported, speed_valid, speed_tok_s, ttft_ms
			 FROM raw_requests WHERE ts_ms >= ? AND ts_ms < ?`,
			rawFrom, endMs,
		)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var ag int32
			var model string
			var tsMs, unc, cr, cw, outTok, reas, cwSup, sv int64
			var sp, tt float64
			if err := rows.Scan(&ag, &model, &tsMs, &unc, &cr, &cw, &outTok, &reas, &cwSup, &sv, &sp, &tt); err != nil {
				_ = rows.Close()
				return nil, err
			}
			var sps, tts []float64
			var sCnt uint64
			if sv == 1 {
				sCnt = 1
				sps = []float64{sp}
				tts = []float64{tt}
			}
			addPoint(seriesKey{agent: pb.Agent(ag), model: model}, tsMs, uint64(unc), uint64(cr), uint64(cw), uint64(outTok), uint64(reas), 1, sCnt, cwSup == 1, sps, tts)
		}
		_ = rows.Close()
	}

	// 2. Query hourly_buckets strictly before rawFrom: [hourFloorMs(prevStartMs), min(rawSplitHour, endMs))
	hourlyUpper := min(rawSplitHour, endMs)
	if prevStartMs < hourlyUpper {
		rows, err := c.store.db.Query(
			`SELECT hour_start_ms, agent, model, uncached_input, cache_read, cache_write, output, reasoning_output, cache_write_supported, usage_request_count, speed_request_count, speed_samples, ttft_samples
			 FROM hourly_buckets WHERE hour_start_ms >= ? AND hour_start_ms < ?`,
			hourFloorMs(prevStartMs), hourlyUpper,
		)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var hr int64
			var ag int32
			var model string
			var unc, cr, cw, outTok, reas, cwSup, uCnt, sCnt int64
			var spB, ttB []byte
			if err := rows.Scan(&hr, &ag, &model, &unc, &cr, &cw, &outTok, &reas, &cwSup, &uCnt, &sCnt, &spB, &ttB); err != nil {
				_ = rows.Close()
				return nil, err
			}
			effTs := hr
			if effTs < prevStartMs && hr+3600*1000 > prevStartMs {
				// Half-hour timezone boundary (e.g. UTC+5:30): place straddling hour into prevStartMs.
				effTs = prevStartMs
			}
			if effTs >= prevStartMs && effTs < hourlyUpper {
				addPoint(seriesKey{agent: pb.Agent(ag), model: model}, effTs, uint64(unc), uint64(cr), uint64(cw), uint64(outTok), uint64(reas), uint64(uCnt), uint64(sCnt), cwSup == 1, f32ToF64(decodeFloat32Slice(spB)), f32ToF64(decodeFloat32Slice(ttB)))
			}
		}
		_ = rows.Close()
	}

	return res, nil
}

func (c *Collector) loadRecent10Speeds(enabled map[pb.Agent]bool) map[seriesKey][]float64 {
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	out := map[seriesKey][]float64{}
	rows, err := c.store.db.Query(
		`SELECT agent, model, speed_tok_s FROM raw_requests WHERE speed_valid = 1 ORDER BY ts_ms DESC`,
	)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var ag int32
		var model string
		var sp float64
		if rows.Scan(&ag, &model, &sp) != nil {
			continue
		}
		k := seriesKey{agent: pb.Agent(ag), model: model}
		if !enabled[k.agent] || len(out[k]) >= 10 {
			continue
		}
		out[k] = append(out[k], sp)
	}
	return out
}

func f32ToF64(in []float32) []float64 {
	if len(in) == 0 {
		return nil
	}
	out := make([]float64, len(in))
	for i, v := range in {
		out[i] = float64(v)
	}
	return out
}
