package screens

import (
	"slices"
	"strings"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/machine"
	"github.com/Lewin671/remote-agent/clientcore/domain/quota"
	"github.com/Lewin671/remote-agent/clientcore/domain/usage"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

// MachinePollFeed holds the runtime polling state machine for one computer's MachineStatus telemetry.
type MachinePollFeed struct {
	State        corev1.MachinePollState
	Mode         corev1.MachinePollMode
	Visible      bool
	Polling      bool
	Busy         bool
	Generation   uint64
	ActiveReqID  string
	StartedMs    int64
	Failures     int
	Last         *v1.MachineStatus
	ReceivedAtMs int64
	CurveSinceMs int64

	Tab          corev1.MachineUsageTab
	SessionOrder []string
	ProcessOrder []string
	HasScrubCPU  bool
	ScrubCPU     int32
	HasScrubMem  bool
	ScrubMem     int32
}

func findMachineByID(st *State, machineID string) *lists.Machine {
	if st == nil || machineID == "" {
		return nil
	}
	for _, m := range st.World.Machines {
		if m != nil && m.ID == machineID {
			return m
		}
	}
	return nil
}

// BuildMachineStatus renders the MachineStatus screen ViewModel.
func BuildMachineStatus(st *State, feed *MachinePollFeed, machineID string) *corev1.MachineStatusViewModel {
	m := findMachineByID(st, machineID)
	vm := &corev1.MachineStatusViewModel{
		MachineId: machineID,
	}
	if m == nil {
		vm.MachineName = machine.ShortID(machineID)
		if waitingForMachineList(st) {
			vm.PollState = corev1.MachinePollState_MACHINE_POLL_STATE_LOADING
			vm.StatusDot = corev1.MachineStatusDot_MACHINE_STATUS_DOT_GRAY_STATIC
			vm.StatusBadge = text.Msg(text.KeyMachineStatusLoading)
			vm.UnavailableTitle = text.Msg(text.KeyCommonLoading)
		} else {
			vm.PollState = corev1.MachinePollState_MACHINE_POLL_STATE_UNAVAILABLE
			vm.StatusDot = corev1.MachineStatusDot_MACHINE_STATUS_DOT_RED_STATIC
			vm.StatusBadge = text.Msg(text.KeyMachineStatusUnavailable)
			vm.UnavailableTitle = text.Msg(text.KeyMachineStatusUnavailable)
		}
		vm.Rings = machine.BuildRings(false, nil)
		vm.Cpu = machine.BuildCpuSection(nil, machine.BuildChart(nil, false, 0, false, 0, false))
		vm.Memory = machine.BuildMemorySection(nil, machine.BuildChart(nil, false, 0, true, 0, false))
		vm.Disk = machine.BuildDiskSection(nil)
		vm.TopUsage = &corev1.MachineTopUsageSection{SelectedTab: corev1.MachineUsageTab_MACHINE_USAGE_TAB_SESSIONS}
		return vm
	}

	vm.Found = true
	vm.MachineName = lists.MachineName(m, machineID)
	vm.Supported = m.Meta != nil && m.Meta.Capabilities != nil && m.Meta.Capabilities.MachineStatus
	vm.Online = lists.MachineReachable(m)

	var last *v1.MachineStatus
	var receivedAt, curveSince int64
	tab := corev1.MachineUsageTab_MACHINE_USAGE_TAB_SESSIONS
	var scrubCPUIdx, scrubMemIdx int32
	var hasScrubCPU, hasScrubMem bool
	var prevOrder []string
	if feed != nil {
		last = feed.Last
		receivedAt = feed.ReceivedAtMs
		curveSince = feed.CurveSinceMs
		if feed.Tab != corev1.MachineUsageTab_MACHINE_USAGE_TAB_UNSPECIFIED {
			tab = feed.Tab
		}
		scrubCPUIdx = feed.ScrubCPU
		hasScrubCPU = feed.HasScrubCPU
		scrubMemIdx = feed.ScrubMem
		hasScrubMem = feed.HasScrubMem
		if tab == corev1.MachineUsageTab_MACHINE_USAGE_TAB_PROCESSES {
			prevOrder = feed.ProcessOrder
		} else {
			prevOrder = feed.SessionOrder
		}
	}

	pollState := corev1.MachinePollState_MACHINE_POLL_STATE_LOADING
	if feed != nil && feed.State != corev1.MachinePollState_MACHINE_POLL_STATE_UNSPECIFIED {
		pollState = feed.State
	}
	if !vm.Online || !vm.Supported {
		if last != nil {
			pollState = corev1.MachinePollState_MACHINE_POLL_STATE_STALE
		} else {
			pollState = corev1.MachinePollState_MACHINE_POLL_STATE_UNAVAILABLE
		}
	}
	vm.PollState = pollState

	switch pollState {
	case corev1.MachinePollState_MACHINE_POLL_STATE_LIVE:
		vm.StatusDot = corev1.MachineStatusDot_MACHINE_STATUS_DOT_GREEN_LIVE
		vm.StatusBadge = text.Msg(text.KeyMachineStatusLive)
	case corev1.MachinePollState_MACHINE_POLL_STATE_STALE:
		vm.Dim = true
		vm.StatusDot = corev1.MachineStatusDot_MACHINE_STATUS_DOT_ORANGE_STATIC
		vm.StatusBadge = machine.StaleAge(receivedAt, st.Cal.NowMs)
	case corev1.MachinePollState_MACHINE_POLL_STATE_UNAVAILABLE:
		vm.StatusDot = corev1.MachineStatusDot_MACHINE_STATUS_DOT_RED_STATIC
		vm.StatusBadge = text.Msg(text.KeyMachineStatusUnavailable)
		vm.UnavailableTitle = text.Msg(text.KeyMachineStatusUnavailable)
		vm.ShowRetry = vm.Online && vm.Supported
	default:
		vm.StatusDot = corev1.MachineStatusDot_MACHINE_STATUS_DOT_GRAY_STATIC
		vm.StatusBadge = text.Msg(text.KeyMachineStatusLoading)
	}

	chartVisible := pollState == corev1.MachinePollState_MACHINE_POLL_STATE_LIVE && last != nil
	hasDisplayData := (pollState == corev1.MachinePollState_MACHINE_POLL_STATE_LIVE || pollState == corev1.MachinePollState_MACHINE_POLL_STATE_STALE) && last != nil

	var shown *v1.MachineStatus
	if hasDisplayData {
		shown = last
	}

	cpuChart := machine.BuildChart(shown, chartVisible, curveSince, false, scrubCPUIdx, hasScrubCPU)
	memChart := machine.BuildChart(shown, chartVisible, curveSince, true, scrubMemIdx, hasScrubMem)

	vm.Rings = machine.BuildRings(hasDisplayData, shown)
	vm.Cpu = machine.BuildCpuSection(shown, cpuChart)
	vm.Memory = machine.BuildMemorySection(shown, memChart)
	vm.Disk = machine.BuildDiskSection(shown)
	vm.UptimeText, vm.BatteryText = machine.BuildFooterParts(shown)

	top := &corev1.MachineTopUsageSection{SelectedTab: tab}
	if hasDisplayData {
		sessByID := make(map[string]*lists.Session, len(st.World.Sessions))
		for _, s := range st.World.Sessions {
			if s != nil {
				sessByID[s.ID] = s
			}
		}
		var raw []*v1.ResourceUsage
		sessionsTab := tab != corev1.MachineUsageTab_MACHINE_USAGE_TAB_PROCESSES
		if sessionsTab {
			raw = shown.Sessions
		} else {
			raw = shown.Processes
		}
		rows, nextOrder := machine.BuildUsageRows(prevOrder, raw, sessionsTab, sessByID, st.Cal)
		top.Rows = rows
		if feed != nil {
			if sessionsTab {
				feed.SessionOrder = nextOrder
			} else {
				feed.ProcessOrder = nextOrder
			}
		}
		if sessionsTab && len(rows) == 0 && pollState == corev1.MachinePollState_MACHINE_POLL_STATE_LIVE {
			top.EmptyText = text.Msg(text.KeyMachineStatusNoRunningSessions)
		}
	}
	vm.TopUsage = top
	vm.ModelUsage = buildMachineModelUsagePreview(st, m)
	return vm
}

func buildMachineModelUsagePreview(st *State, m *lists.Machine) *corev1.MachineModelUsagePreview {
	if st == nil || m == nil || m.Meta == nil || m.Meta.Capabilities == nil || !m.Meta.Capabilities.UsageStats {
		return nil
	}
	online := lists.MachineReachable(m)
	preview := &corev1.MachineModelUsagePreview{
		Supported: true,
	}

	snap7, cachedOffline7, cachedAt7, loading7 := lookupUsageSnapshot(st, m.ID, v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS, 0, 0, online)
	active7 := filterDisabledSeries(m.Meta, usage.ActiveSeries(snap7))
	selected := active7
	usedSnap := snap7
	cachedOffline := cachedOffline7
	cachedAt := cachedAt7
	loading := loading7

	if len(selected) == 0 {
		snap30, cachedOffline30, cachedAt30, loading30 := lookupUsageSnapshot(st, m.ID, v1.UsageRangePreset_USAGE_RANGE_PRESET_THIRTY_DAYS, 0, 0, online)
		active30 := filterDisabledSeries(m.Meta, usage.ActiveSeries(snap30))
		if len(active30) > 0 {
			selected = active30
			usedSnap = snap30
			preview.FallbackThirtyDays = true
			cachedOffline = cachedOffline30
			cachedAt = cachedAt30
		}
		loading = loading || loading30
	}

	var availCount int
	var unavailNames []string
	if usedSnap != nil {
		disabled := lists.MachineDisabledSet(m.Meta)
		for _, src := range usedSnap.Sources {
			if src == nil || (disabled != nil && disabled[lists.EffectiveAgent(src.Agent)]) {
				continue
			}
			if src.State == v1.UsageSourceState_USAGE_SOURCE_STATE_UNAVAILABLE {
				unavailNames = append(unavailNames, lists.AgentInfo(src.Agent).Name)
			} else {
				availCount++
			}
		}
	}

	preview.Loading = loading && len(selected) == 0
	preview.CachedOffline = cachedOffline
	preview.CachedAtMs = cachedAt
	if usedSnap != nil {
		preview.Backfilling = usedSnap.Backfilling
	}

	if availCount == 0 && len(unavailNames) > 0 && len(selected) == 0 {
		preview.EmptyText = text.Msg(text.KeyMachineStatusModelUsageOnlyUnavailable)
		return preview
	}

	if preview.FallbackThirtyDays {
		preview.WindowLabel = text.Msg(text.KeyUsageEntryLast30D)
	} else {
		preview.WindowLabel = text.Msg(text.KeyUsageEntryLast7D)
	}

	if len(selected) == 0 {
		if !preview.Loading {
			preview.EmptyText = text.Msg(text.KeyMachineStatusModelUsageEmpty)
		}
	} else {
		preview.HasData = true
		limit := len(selected)
		if limit > 4 {
			limit = 4
		}
		for _, s := range selected[:limit] {
			eff := lists.EffectiveAgent(s.Agent)
			agentName := lists.AgentInfo(eff).Name
			tot := usage.TotalTokens(s.Current)
			fewSpeed := s.Current.SpeedRequestCount > 0 && s.Current.SpeedRequestCount < usage.MinTrendRequests
			var hitPart *corev1.Text
			if s.Current.HasCacheHitRate {
				hitPart = text.Msg(text.KeyUsageEntryCachePct, text.Str("pct", usage.FormatCacheHit(s.Current.CacheHitRate)))
			}
			var metaText *corev1.Text
			if s.Current.HasSpeed {
				speedPart := text.JoinLiteral(" ", text.Plain(usage.FormatSpeedValue(s.Current.SpeedP50)), text.Msg(text.KeyUsageKpiUnitToks))
				reqsPart := text.Msg(text.KeyUsageEntryReqs, text.Int("count", int64(s.Current.UsageRequestCount)))
				metaText = text.JoinLiteral(" · ", hitPart, speedPart, reqsPart, preview.WindowLabel)
			} else if fewSpeed {
				detail := hitPart
				if detail == nil {
					detail = preview.WindowLabel
				}
				metaText = text.Msg(text.KeyUsageEntryFewSamples, text.ArgText("detail", detail), text.Int("count", int64(s.Current.SpeedRequestCount)))
			} else {
				detail := hitPart
				if detail == nil {
					detail = preview.WindowLabel
				}
				metaText = text.Msg(text.KeyUsageEntryNoSpeed, text.ArgText("detail", detail))
			}
			preview.Rows = append(preview.Rows, &corev1.MachineModelUsageRow{
				Agent:             eff,
				AgentName:         agentName,
				Model:             s.Model,
				TotalTokens:       tot,
				HasCacheHit:       s.Current.HasCacheHitRate,
				CacheHitRate:      s.Current.CacheHitRate,
				HasSpeed:          s.Current.HasSpeed,
				SpeedP50:          s.Current.SpeedP50,
				UsageRequestCount: s.Current.UsageRequestCount,
				SpeedRequestCount: s.Current.SpeedRequestCount,
				FewSpeedSamples:   fewSpeed,
				TitleText:         text.Plain(agentName + " · " + s.Model),
				TotalTokensText:   usage.FormatTokensText(tot, st.UILocale),
				MetaText:          metaText,
			})
		}
	}

	if len(unavailNames) > 0 && availCount > 0 {
		preview.UnsupportedFootnote = text.Msg(text.KeyMachineStatusModelUsageMixedUnavailable, text.Str("agents", strings.Join(unavailNames, ", ")))
	}
	return preview
}

func filterDisabledSeries(meta *v1.MachineMeta, in []*v1.UsageSeries) []*v1.UsageSeries {
	if meta == nil || len(meta.DisabledAgents) == 0 {
		return in
	}
	disabled := lists.MachineDisabledSet(meta)
	if len(disabled) == 0 {
		return in
	}
	out := make([]*v1.UsageSeries, 0, len(in))
	for _, s := range in {
		if s != nil && !disabled[lists.EffectiveAgent(s.Agent)] {
			out = append(out, s)
		}
	}
	return out
}

func lookupUsageSnapshot(st *State, machineID string, preset v1.UsageRangePreset, cStart, cEnd int64, online bool) (snap *v1.UsageStatsResponse, cachedOffline bool, cachedAtMs int64, loading bool) {
	if st == nil || st.UsageSnapshots == nil || machineID == "" {
		return nil, false, 0, false
	}
	if preset != v1.UsageRangePreset_USAGE_RANGE_PRESET_CUSTOM {
		cStart, cEnd = 0, 0
	}
	entry := st.UsageSnapshots[UsageCacheKey{
		MachineID:     machineID,
		Preset:        preset,
		CustomStartMs: cStart,
		CustomEndMs:   cEnd,
	}]
	if entry == nil {
		return nil, false, 0, false
	}
	loading = entry.Loading
	if online {
		if entry.Live != nil {
			return entry.Live, false, entry.LiveAtMs, loading
		}
		if entry.Cached != nil {
			return entry.Cached, false, entry.CachedAtMs, loading
		}
		return nil, false, 0, loading
	}
	if entry.Live != nil {
		ts := entry.LiveAtMs
		if ts == 0 {
			ts = entry.Live.GeneratedAtMs
		}
		return entry.Live, true, ts, false
	}
	if entry.Cached != nil {
		ts := entry.CachedAtMs
		if ts == 0 {
			ts = entry.Cached.GeneratedAtMs
		}
		return entry.Cached, true, ts, false
	}
	return nil, false, 0, false
}

// Keep slices import used for future preview filters.
var _ = slices.Contains[[]v1.Agent]

// BuildQuota renders the per-machine Account Quota screen ViewModel.
func BuildQuota(st *State, syncView *corev1.SyncStateView, machineID string, filterAgent v1.Agent) *corev1.QuotaViewModel {
	m := findMachineByID(st, machineID)
	unlinked := syncView.GetUnlinked() || lists.WorldUnlinked(&st.World)
	vm := &corev1.QuotaViewModel{
		MachineId:  machineID,
		Unlinked:   unlinked,
		FooterNote: text.Msg(text.KeySettingsTimesAreShownInBeijingTimeQuotasBelongTo),
	}
	if m == nil {
		vm.MachineName = machine.ShortID(machineID)
		switch {
		case unlinked:
			vm.EmptyText = text.Msg(text.KeySettingsThisDeviceWasRemovedFromTheAccount)
		case waitingForMachineList(st):
			vm.Loading = true
			vm.EmptyText = text.Msg(text.KeyCommonLoading)
		default:
			vm.EmptyText = text.Msg(text.KeySettingsDeviceNotFound)
		}
		return vm
	}

	vm.Found = true
	vm.Member = !unlinked
	vm.MachineName = lists.MachineName(m, machineID)
	vm.Reachable = lists.MachineReachable(m)
	if m.Meta != nil && (len(m.Meta.Agents) > 0 || len(m.Meta.Quotas) > 0) {
		vm.HasAgentsReported = true
	}

	if !vm.Reachable {
		vm.OfflineNotice = text.Msg(text.KeySettingsComputerOfflineShowingTheLastReading)
	}
	if !vm.HasAgentsReported {
		vm.EmptyText = text.Msg(text.KeyQuotaQuotaNotAvailableYet)
		return vm
	}

	vm.Sections = quota.BuildCliSections(m, filterAgent, st.Cal.NowMs)
	vm.Banner = quota.Banner([]*lists.Machine{m}, st.Cal.NowMs)
	return vm
}
