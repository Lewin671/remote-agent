package machine

import (
	"math"
	"strconv"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/sortutil"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

const (
	PollIntervalLiveMs     int64 = 2_000
	PollIntervalOverviewMs int64 = 10_000
	RPCTimeoutMs           int64 = 15_000
	MaxFailures                  = 3

	ChartWindowMs      int64   = 116_000
	ChartGapMs         int64   = 10_000
	CPUHysteresis      float64 = 2.0
	MemHysteresisBytes uint64  = 50_000_000

	DiskReminderThresholdBytes uint64 = 10_000_000_000
	DiskCriticalThresholdBytes uint64 = 2_000_000_000

	TopUsageLimit = 8

	gibBytes = 1024 * 1024 * 1024
	mibBytes = 1024 * 1024
	gbBytes  = 1_000_000_000
	mbBytes  = 1_000_000
)

func gbFixed(bytes uint64, binary bool) string {
	unit := float64(gbBytes)
	if binary {
		unit = float64(gibBytes)
	}
	return strconv.FormatFloat(float64(bytes)/unit, 'f', 1, 64)
}

func gbNumber(bytes uint64, binary bool) string {
	unit := float64(gbBytes)
	if binary {
		unit = float64(gibBytes)
	}
	v := float64(bytes) / unit
	if v >= 9.95 {
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
	s := strconv.FormatFloat(v, 'f', 1, 64)
	if len(s) > 2 && s[len(s)-2:] == ".0" {
		return s[:len(s)-2]
	}
	return s
}

func formatSize(bytes uint64, binary bool) *corev1.Text {
	gi := uint64(gbBytes)
	mi := uint64(mbBytes)
	if binary {
		gi = gibBytes
		mi = mibBytes
	}
	if bytes >= gi {
		return text.Msg(text.KeyMachineStatusSizeGb, text.Str("size", gbNumber(bytes, binary)))
	}
	mb := math.Round(float64(bytes) / float64(mi))
	if !binary && mb >= 1000 {
		return text.Msg(text.KeyMachineStatusSizeGb, text.Str("size", "1"))
	}
	if binary && mb >= 1024 {
		return text.Msg(text.KeyMachineStatusSizeGb, text.Str("size", "1"))
	}
	return text.Msg(text.KeyMachineStatusSizeMb, text.Str("size", strconv.FormatFloat(mb, 'f', 0, 64)))
}

// FormatBinarySize formats RAM/swap/process/attachment bytes using 1024-based units displayed as GB/MB.
func FormatBinarySize(bytes uint64) *corev1.Text {
	return formatSize(bytes, true)
}

// FormatDecimalSize formats disk volume sizes using 1000-based units displayed as GB/MB.
func FormatDecimalSize(bytes uint64) *corev1.Text {
	return formatSize(bytes, false)
}

// FormatAttachmentSize formats uploaded image/file byte counts in MiB/GiB, with "< 0.1 MiB" for small non-zero values.
func FormatAttachmentSize(bytes int64) *corev1.Text {
	if bytes <= 0 {
		return text.Msg(text.KeyMachineStatusSizeMb, text.Str("size", "0"))
	}
	u := uint64(bytes)
	if u < mibBytes/10 {
		return text.Msg(text.KeySettingsLessThan01Mib)
	}
	return formatSize(u, true)
}

// RecentProjectPath returns the path of the most recently active project reported by the daemon.
func RecentProjectPath(meta *v1.MachineMeta) string {
	if meta == nil || len(meta.Projects) == 0 {
		return ""
	}
	best := ""
	var bestMs int64 = -1
	for _, p := range meta.Projects {
		if p == nil || p.Path == "" {
			continue
		}
		ts := p.LatestCreatedMs
		if p.LatestEndedMs > ts {
			ts = p.LatestEndedMs
		}
		if best == "" || ts > bestMs || (ts == bestMs && p.Path < best) {
			best = p.Path
			bestMs = ts
		}
	}
	return best
}

// StaleAge returns how old stale telemetry is ("{N}秒前的数据" or "{N}分钟前的数据").
func StaleAge(receivedAtMs, nowMs int64) *corev1.Text {
	if receivedAtMs <= 0 || nowMs <= receivedAtMs {
		return text.Msg(text.KeyMachineStatusStaleSeconds, text.Int("count", 0))
	}
	secs := (nowMs - receivedAtMs) / 1000
	if secs < 60 {
		return text.Msg(text.KeyMachineStatusStaleSeconds, text.Int("count", secs))
	}
	return text.Msg(text.KeyMachineStatusStaleMinutes, text.Int("count", secs/60))
}

// BuildRings builds the 3 summary rings (CPU, Memory, Available Disk) shown on MachineSettings and MachineStatus.
func BuildRings(online bool, status *v1.MachineStatus) []*corev1.MachineRingView {
	cpuVal := text.Plain("—")
	memVal := text.Plain("—")
	diskVal := text.Plain("—")
	var hasCPU, hasMem, hasDisk bool
	var cpuFrac, memFrac, diskFrac float64
	memTone := corev1.MachineRingTone_MACHINE_RING_TONE_NORMAL
	diskTone := corev1.MachineRingTone_MACHINE_RING_TONE_NORMAL

	if online && status != nil {
		pct := clampUnit(status.CpuPercent / 100.0)
		cpuVal = text.Plain(pctText(status.CpuPercent))
		cpuFrac = pct
		hasCPU = true

		if status.MemoryTotalBytes > 0 {
			hasMem = true
			memFrac = clampUnit(float64(status.MemoryUsedBytes) / float64(status.MemoryTotalBytes))
			memVal = text.Plain(pctText(memFrac * 100))
			memTone = ringToneFromPressure(status.MemoryPressure)
		}
		if status.Disk != nil && status.Disk.TotalBytes > 0 {
			hasDisk = true
			availFrac := clampUnit(float64(status.Disk.AvailableBytes) / float64(status.Disk.TotalBytes))
			diskFrac = clampUnit(1.0 - availFrac)
			diskVal = FormatDecimalSize(status.Disk.AvailableBytes)
			diskTone = ringToneFromPressure(status.Disk.Pressure)
		}
	}

	cpuLabel := text.Msg(text.KeyMachineStatusCpu)
	memLabel := text.Msg(text.KeyMachineStatusMemory)
	diskLabel := text.Msg(text.KeyMachineStatusDiskAvailable)

	return []*corev1.MachineRingView{
		{
			Kind:      corev1.MachineRingKind_MACHINE_RING_KIND_CPU,
			Label:     cpuLabel,
			HasValue:  hasCPU,
			Fraction:  cpuFrac,
			ValueText: cpuVal,
			Tone:      corev1.MachineRingTone_MACHINE_RING_TONE_NORMAL,
			A11YLabel: text.Msg(text.KeyMachineStatusA11YRing, text.ArgText("name", cpuLabel), text.ArgText("value", cpuVal)),
		},
		{
			Kind:      corev1.MachineRingKind_MACHINE_RING_KIND_MEMORY,
			Label:     memLabel,
			HasValue:  hasMem,
			Fraction:  memFrac,
			ValueText: memVal,
			Tone:      memTone,
			A11YLabel: text.Msg(text.KeyMachineStatusA11YRing, text.ArgText("name", memLabel), text.ArgText("value", memVal)),
		},
		{
			Kind:      corev1.MachineRingKind_MACHINE_RING_KIND_DISK,
			Label:     diskLabel,
			HasValue:  hasDisk,
			Fraction:  diskFrac,
			ValueText: diskVal,
			Tone:      diskTone,
			A11YLabel: text.Msg(text.KeyMachineStatusA11YRing, text.ArgText("name", diskLabel), text.ArgText("value", diskVal)),
		},
	}
}

func clampUnit(v float64) float64 {
	if v < 0 || math.IsNaN(v) {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func ringToneFromPressure(p v1.ResourcePressure) corev1.MachineRingTone {
	switch p {
	case v1.ResourcePressure_RESOURCE_PRESSURE_WARNING:
		return corev1.MachineRingTone_MACHINE_RING_TONE_WARN
	case v1.ResourcePressure_RESOURCE_PRESSURE_CRITICAL:
		return corev1.MachineRingTone_MACHINE_RING_TONE_CRITICAL
	default:
		return corev1.MachineRingTone_MACHINE_RING_TONE_NORMAL
	}
}

func BuildCpuSection(status *v1.MachineStatus, chart *corev1.MachineChartView) *corev1.MachineCpuSection {
	if status == nil {
		return &corev1.MachineCpuSection{
			ValueText: "—",
			Chart:     chart,
		}
	}
	pct := int(math.Round(status.CpuPercent))
	valStr := strconv.FormatInt(int64(pct), 10)
	var detail, detail2 *corev1.Text
	if status.Cores > 0 {
		detail = text.Msg(text.KeyMachineStatusCores, text.Int("count", int64(status.Cores)))
	}
	if status.LoadOne != nil {
		detail2 = text.Msg(text.KeyMachineStatusLoad, text.Str("load", strconv.FormatFloat(*status.LoadOne, 'f', 1, 64)))
	}
	return &corev1.MachineCpuSection{
		HasValue:  true,
		Detail:    detail,
		Detail2:   detail2,
		A11YLabel: text.Msg(text.KeyMachineStatusA11YCpuCard, text.Str("value", valStr+"%")),
		Chart:     chart,
		Value:     status.CpuPercent,
		ValueText: valStr,
		Unit:      "%",
	}
}

func BuildMemorySection(status *v1.MachineStatus, chart *corev1.MachineChartView) *corev1.MachineMemorySection {
	if status == nil {
		return &corev1.MachineMemorySection{
			Caption:   text.Msg(text.KeyMachineStatusGbUsed),
			Chart:     chart,
			ValueText: "—",
		}
	}
	valStr := gbFixed(status.MemoryUsedBytes, true)
	usedTxt := text.Msg(text.KeyMachineStatusSizeGb, text.Str("size", valStr))
	var totalText, swapText *corev1.Text
	totalFormatted := FormatBinarySize(status.MemoryTotalBytes)
	if status.SwapUsedBytes != nil && *status.SwapUsedBytes > 0 {
		swapFormatted := FormatBinarySize(*status.SwapUsedBytes)
		totalText = text.Msg(text.KeyMachineStatusTotalSwap, text.ArgText("size", totalFormatted), text.ArgText("swap", swapFormatted))
		swapText = text.Msg(text.KeyMachineStatusSwap, text.ArgText("size", swapFormatted))
	} else {
		totalText = text.Msg(text.KeyMachineStatusTotal, text.ArgText("size", totalFormatted))
	}

	var pressure corev1.MachinePressureTone
	var pressureText *corev1.Text
	switch status.MemoryPressure {
	case v1.ResourcePressure_RESOURCE_PRESSURE_NORMAL:
		pressure = corev1.MachinePressureTone_MACHINE_PRESSURE_TONE_NORMAL
		pressureText = text.Msg(text.KeyMachineStatusPressureNormal)
	case v1.ResourcePressure_RESOURCE_PRESSURE_WARNING:
		pressure = corev1.MachinePressureTone_MACHINE_PRESSURE_TONE_HIGH
		pressureText = text.Msg(text.KeyMachineStatusPressureHigh)
	case v1.ResourcePressure_RESOURCE_PRESSURE_CRITICAL:
		pressure = corev1.MachinePressureTone_MACHINE_PRESSURE_TONE_CRITICAL
		pressureText = text.Msg(text.KeyMachineStatusPressureCritical)
	}

	var hasPos bool
	var pos float64
	if pressure != corev1.MachinePressureTone_MACHINE_PRESSURE_TONE_UNSPECIFIED && status.MemoryPressurePosition != nil {
		hasPos = true
		pos = clampUnit(*status.MemoryPressurePosition)
	}

	var a11y *corev1.Text
	if pressureText != nil {
		a11y = text.Msg(text.KeyMachineStatusA11YMemoryCard, text.ArgText("used", usedTxt), text.ArgText("pressure", pressureText))
	} else {
		a11y = text.Msg(text.KeyMachineStatusA11YMemoryCardPlain, text.ArgText("used", usedTxt))
	}

	return &corev1.MachineMemorySection{
		HasValue:            true,
		Caption:             text.Msg(text.KeyMachineStatusGbUsed),
		Pressure:            pressure,
		PressureText:        pressureText,
		HasPressurePosition: hasPos,
		PressurePosition:    pos,
		TotalText:           totalText,
		SwapText:            swapText,
		A11YLabel:           a11y,
		Chart:               chart,
		Value:               float64(status.MemoryUsedBytes) / float64(gibBytes),
		ValueText:           valStr,
		Unit:                "GB",
		UsedBytes:           status.MemoryUsedBytes,
		TotalBytes:          status.MemoryTotalBytes,
	}
}

func BuildDiskSection(status *v1.MachineStatus) *corev1.MachineDiskSection {
	if status == nil || status.Disk == nil {
		return &corev1.MachineDiskSection{
			Caption:   text.Msg(text.KeyMachineStatusGbAvailable),
			ValueText: "—",
		}
	}
	d := status.Disk
	availTxt := FormatDecimalSize(d.AvailableBytes)
	var diskVal float64
	var diskValStr, diskUnit string
	if d.AvailableBytes >= gbBytes {
		diskVal = float64(d.AvailableBytes) / float64(gbBytes)
		diskValStr = gbNumber(d.AvailableBytes, false)
		diskUnit = "GB"
	} else {
		mb := math.Round(float64(d.AvailableBytes) / float64(mbBytes))
		if mb >= 1000 {
			diskVal = float64(d.AvailableBytes) / float64(gbBytes)
			diskValStr = "1"
			diskUnit = "GB"
		} else {
			diskVal = mb
			diskValStr = strconv.FormatFloat(mb, 'f', 0, 64)
			diskUnit = "MB"
		}
	}

	var state corev1.DiskPressureState
	var badge *corev1.Text
	switch d.Pressure {
	case v1.ResourcePressure_RESOURCE_PRESSURE_WARNING:
		state = corev1.DiskPressureState_DISK_PRESSURE_STATE_LOW
		badge = text.Msg(text.KeyMachineStatusDiskLow)
	case v1.ResourcePressure_RESOURCE_PRESSURE_CRITICAL:
		state = corev1.DiskPressureState_DISK_PRESSURE_STATE_CRITICAL
		badge = text.Msg(text.KeyMachineStatusDiskCritical)
	default:
		state = corev1.DiskPressureState_DISK_PRESSURE_STATE_OK
		badge = text.Msg(text.KeyMachineStatusDiskOk)
	}

	var hasFrac bool
	var usedFrac float64
	if d.TotalBytes > 0 {
		hasFrac = true
		usedFrac = clampUnit(1.0 - float64(d.AvailableBytes)/float64(d.TotalBytes))
	}

	var detail, detail2 *corev1.Text
	if state == corev1.DiskPressureState_DISK_PRESSURE_STATE_CRITICAL {
		detail = text.Msg(text.KeyMachineStatusDiskCriticalHint)
	} else {
		detail = text.Msg(text.KeyMachineStatusDiskTotal, text.ArgText("size", FormatDecimalSize(d.TotalBytes)))
		if status.AgentBytes != nil {
			detail2 = text.Msg(text.KeyMachineStatusDiskAgentShare, text.ArgText("size", FormatDecimalSize(*status.AgentBytes)))
		}
	}

	var agentVal *corev1.Text
	if status.AgentBytes != nil {
		agentVal = FormatDecimalSize(*status.AgentBytes)
	}

	caption := text.Msg(text.KeyMachineStatusGbAvailable)
	if diskUnit == "MB" {
		caption = text.Msg(text.KeyMachineStatusMbAvailable)
	}

	return &corev1.MachineDiskSection{
		HasValue:           true,
		Caption:            caption,
		State:              state,
		StateBadge:         badge,
		HasUsedFraction:    hasFrac,
		UsedFraction:       usedFrac,
		Detail:             detail,
		Detail2:            detail2,
		ShowActions:        state == corev1.DiskPressureState_DISK_PRESSURE_STATE_LOW || state == corev1.DiskPressureState_DISK_PRESSURE_STATE_CRITICAL,
		CleanupActionLabel: text.Msg(text.KeyMachineStatusCleanupAction),
		AgentUsageLabel:    text.Msg(text.KeyMachineStatusAgentUsage),
		AgentUsageValue:    agentVal,
		A11YLabel:          text.Msg(text.KeyMachineStatusA11YDiskCard, text.ArgText("size", availTxt), text.ArgText("state", badge)),
		Value:              diskVal,
		ValueText:          diskValStr,
		Unit:               diskUnit,
		AvailableBytes:     d.AvailableBytes,
		TotalBytes:         d.TotalBytes,
	}
}

// BuildChart creates ChartView from samples starting at or after curveSinceMs.
// When samples has no qualifying points, a single point at observedMs is emitted so the curve starts smoothly.
func BuildChart(
	status *v1.MachineStatus,
	visible bool,
	curveSinceMs int64,
	memory bool,
	scrubIndex int32,
	hasScrub bool,
) *corev1.MachineChartView {
	out := &corev1.MachineChartView{
		Visible:        visible && status != nil,
		GapThresholdMs: ChartGapMs,
	}
	if !out.Visible {
		return out
	}

	pts := make([]*corev1.MachineChartPoint, 0, len(status.Samples)+1)
	for _, s := range status.Samples {
		if s == nil || s.TimeMs < curveSinceMs {
			continue
		}
		v := s.CpuPercent
		if memory {
			v = float64(s.MemoryUsedBytes)
		}
		pts = append(pts, &corev1.MachineChartPoint{TimeMs: s.TimeMs, Value: v})
	}
	sortutil.Sort(len(pts), func(i, j int) bool { return pts[i].TimeMs < pts[j].TimeMs }, func(i, j int) { pts[i], pts[j] = pts[j], pts[i] })
	if len(pts) == 0 && status.ObservedMs > 0 {
		v := status.CpuPercent
		if memory {
			v = float64(status.MemoryUsedBytes)
		}
		pts = append(pts, &corev1.MachineChartPoint{TimeMs: status.ObservedMs, Value: v})
	}
	out.Points = pts

	if memory {
		c := float64(status.MemoryTotalBytes)
		if c < 1 {
			c = 1
		}
		out.Ceiling = c
	} else {
		out.Ceiling = 100.0
	}

	if len(pts) > 0 {
		endMs := pts[len(pts)-1].TimeMs
		out.WindowEndMs = endMs
		out.WindowStartMs = endMs - ChartWindowMs

		var maxVal float64
		for _, p := range pts {
			if p.Value > maxVal {
				maxVal = p.Value
			}
		}
		nowVal := pts[len(pts)-1].Value
		out.A11YSummary = text.Msg(
			text.KeyMachineStatusA11YChart,
			text.ArgText("max", formatChartValue(maxVal, memory)),
			text.ArgText("now", formatChartValue(nowVal, memory)),
		)

		if hasScrub && scrubIndex >= 0 && int(scrubIndex) < len(pts) {
			out.HasScrub = true
			out.ScrubIndex = scrubIndex
			p := pts[scrubIndex]
			out.ScrubValue = formatChartValue(p.Value, memory)
			secs := int64(math.Round(float64(endMs-p.TimeMs)/2000.0)) * 2
			if secs <= 0 {
				out.ScrubAge = text.Msg(text.KeyMachineStatusAxisNow)
			} else {
				out.ScrubAge = text.Msg(text.KeyMachineStatusSecondsAgo, text.Int("count", secs))
			}
			out.CanStepPrev = scrubIndex > 0
			out.CanStepNext = int(scrubIndex) < len(pts)-1
		} else {
			out.CanStepPrev = len(pts) > 0
		}
	}
	return out
}

func formatChartValue(v float64, memory bool) *corev1.Text {
	if !memory {
		return text.Plain(pctText(v))
	}
	if v < 0 {
		v = 0
	}
	return FormatBinarySize(uint64(math.Round(v)))
}

// StableUsage orders rows preserving previous IDs unless a neighbor leads by > threshold
// (2% CPU or 50 MB RAM), keeping at most all sorted rows so caller may slice TopUsageLimit.
func StableUsage(previousIDs []string, rows []*v1.ResourceUsage, memory bool) []*v1.ResourceUsage {
	if len(rows) == 0 {
		return nil
	}
	byID := make(map[string]*v1.ResourceUsage, len(rows))
	for _, r := range rows {
		if r != nil {
			byID[r.Id] = r
		}
	}
	ordered := make([]*v1.ResourceUsage, 0, len(byID))
	for _, id := range previousIDs {
		if r, ok := byID[id]; ok {
			ordered = append(ordered, r)
			delete(byID, id)
		}
	}
	metric := func(r *v1.ResourceUsage) float64 {
		if memory {
			return float64(r.MemoryBytes)
		}
		return r.CpuPercent
	}
	remaining := make([]*v1.ResourceUsage, 0, len(byID))
	for _, r := range byID {
		remaining = append(remaining, r)
	}
	sortutil.Sort(len(remaining), func(i, j int) bool {
		di, dj := metric(remaining[i]), metric(remaining[j])
		if di != dj {
			return di > dj
		}
		return remaining[i].Id < remaining[j].Id
	}, func(i, j int) { remaining[i], remaining[j] = remaining[j], remaining[i] })
	ordered = append(ordered, remaining...)

	threshold := CPUHysteresis
	if memory {
		threshold = float64(MemHysteresisBytes)
	}
	for i := 1; i < len(ordered); i++ {
		for j := i; j > 0; j-- {
			if metric(ordered[j])-metric(ordered[j-1]) <= threshold {
				break
			}
			ordered[j-1], ordered[j] = ordered[j], ordered[j-1]
		}
	}
	return ordered
}

// BuildUsageRows formats up to TopUsageLimit resource rows using hysteresis ordering.
func BuildUsageRows(
	previousIDs []string,
	raw []*v1.ResourceUsage,
	sessionsTab bool,
	sessionsByID map[string]*lists.Session,
	cal lists.Calendar,
) ([]*corev1.MachineUsageRow, []string) {
	ordered := StableUsage(previousIDs, raw, !sessionsTab)
	newOrder := make([]string, len(ordered))
	for i, r := range ordered {
		newOrder[i] = r.Id
	}
	if len(ordered) > TopUsageLimit {
		ordered = ordered[:TopUsageLimit]
	}

	var maxCPU float64
	var maxMem uint64
	for _, r := range ordered {
		if r.CpuPercent > maxCPU {
			maxCPU = r.CpuPercent
		}
		if r.MemoryBytes > maxMem {
			maxMem = r.MemoryBytes
		}
	}

	out := make([]*corev1.MachineUsageRow, 0, len(ordered))
	for _, r := range ordered {
		sess := sessionsByID[r.SessionId]
		var knownID string
		var agent v1.Agent
		var sessTitle *corev1.Text
		if sess != nil {
			knownID = sess.ID
			agent = lists.SessionAgent(sess)
			_, _, resolved := lists.ResolveTitle(sess, cal)
			if resolved != "" {
				sessTitle = text.Plain(resolved)
			}
		}

		var title *corev1.Text
		if sessionsTab {
			if sessTitle != nil {
				title = sessTitle
			} else if r.Executable != "" {
				title = text.Plain(r.Executable)
			} else {
				title = text.Plain(r.Id)
			}
		} else {
			if r.Executable != "" {
				title = text.Plain(r.Executable)
			} else {
				title = text.Plain(r.Id)
			}
		}

		cpuTxt := text.Plain(pctText(r.CpuPercent))
		memTxt := FormatBinarySize(r.MemoryBytes)
		var sec *corev1.Text
		if !sessionsTab && sessTitle != nil {
			sec = text.Join(text.KeyCommonNoteSeparator, cpuTxt, memTxt, sessTitle)
		} else {
			sec = text.Join(text.KeyCommonNoteSeparator, cpuTxt, memTxt)
		}

		var cpuFrac, memFrac float64
		if maxCPU > 0 {
			cpuFrac = clampUnit(r.CpuPercent / maxCPU)
		}
		if maxMem > 0 {
			memFrac = clampUnit(float64(r.MemoryBytes) / float64(maxMem))
		}
		out = append(out, &corev1.MachineUsageRow{
			Id:                r.Id,
			Title:             title,
			Secondary:         sec,
			CpuPercent:        r.CpuPercent,
			MemoryBytes:       r.MemoryBytes,
			Agent:             agent,
			SessionId:         knownID,
			CpuBarFraction:    cpuFrac,
			MemoryBarFraction: memFrac,
			CpuValueText:      cpuTxt,
			MemoryValueText:   memTxt,
		})
	}
	return out, newOrder
}

// BuildFooterParts builds uptime and battery footer texts when present.
func BuildFooterParts(status *v1.MachineStatus) (uptime *corev1.Text, battery *corev1.Text) {
	if status == nil {
		return nil, nil
	}
	if status.UptimeSeconds > 0 {
		mins := status.UptimeSeconds / 60
		hours := mins / 60
		days := hours / 24
		var dur *corev1.Text
		if days >= 1 {
			dur = text.Msg(text.KeyMachineStatusDurationDaysHours, text.Int("days", int64(days)), text.Int("hours", int64(hours%24)))
		} else if hours >= 1 {
			dur = text.Msg(text.KeyTimeDurationHoursMinutes, text.Int("hours", int64(hours)), text.Int("minutes", int64(mins%60)))
		} else {
			dur = text.Msg(text.KeyMachineStatusDurationMinutes, text.Int("count", int64(mins)))
		}
		uptime = text.Msg(text.KeyMachineStatusUptime, text.ArgText("duration", dur))
	}
	if status.Battery != nil {
		pct := int64(math.Round(status.Battery.Percent))
		if status.Battery.Charging {
			battery = text.Msg(text.KeyMachineStatusBatteryCharging, text.Int("percent", pct))
		} else {
			battery = text.Msg(text.KeyMachineStatusBattery, text.Int("percent", pct))
		}
	}
	return uptime, battery
}

// BuildDiskReminders returns disk reminders across machines below 10 GB, critical (< 2 GB) first,
// then ascending available bytes, then machine ID.
func BuildDiskReminders(machines []*lists.Machine) []*corev1.DiskReminderView {
	var out []*corev1.DiskReminderView
	for _, m := range machines {
		if m == nil || m.Meta == nil || !lists.MachineReachable(m) {
			continue
		}
		if m.Meta.Capabilities == nil || !m.Meta.Capabilities.MachineStatus {
			continue
		}
		d := m.Meta.DiskStatus
		if d == nil || d.AvailableBytes >= DiskReminderThresholdBytes {
			continue
		}
		critical := d.AvailableBytes < DiskCriticalThresholdBytes
		name := lists.MachineName(m, m.ID)
		size := FormatDecimalSize(d.AvailableBytes)
		title := text.Msg(text.KeyMachineStatusHomeWarning)
		detail := text.Msg(text.KeyMachineStatusHomeWarningDetail, text.Str("computer", name), text.ArgText("size", size))
		if critical {
			title = text.Msg(text.KeyMachineStatusHomeCritical)
			detail = text.Msg(text.KeyMachineStatusHomeCriticalDetail, text.Str("computer", name), text.ArgText("size", size))
		}
		out = append(out, &corev1.DiskReminderView{
			MachineId:      m.ID,
			MachineName:    name,
			Critical:       critical,
			AvailableBytes: d.AvailableBytes,
			Title:          title,
			Detail:         detail,
			ActionLabel:    text.Msg(text.KeyMachineStatusCleanupAction),
		})
	}
	sortutil.Sort(len(out), func(i, j int) bool {
		if out[i].Critical != out[j].Critical {
			return out[i].Critical
		}
		if out[i].AvailableBytes != out[j].AvailableBytes {
			return out[i].AvailableBytes < out[j].AvailableBytes
		}
		return out[i].MachineId < out[j].MachineId
	}, func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

func pctText(v float64) string {
	return strconv.Itoa(int(math.Round(v))) + "%"
}
