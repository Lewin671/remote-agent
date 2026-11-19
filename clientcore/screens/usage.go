package screens

import (
	"strings"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/machine"
	"github.com/Lewin671/remote-agent/clientcore/domain/usage"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

// BuildUsageView constructs the UsageViewModel for the Usage & Speed page.
func BuildUsageView(st *State, syncView *corev1.SyncStateView, req *corev1.UsageScreenRequest) *corev1.UsageViewModel {
	if st == nil {
		st = NewState()
	}
	if req == nil {
		req = &corev1.UsageScreenRequest{}
	}
	preset := req.Preset
	if preset == v1.UsageRangePreset_USAGE_RANGE_PRESET_UNSPECIFIED {
		preset = v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS
	}
	metric := req.Metric
	if metric == corev1.UsageMetricTab_USAGE_METRIC_TAB_UNSPECIFIED {
		metric = corev1.UsageMetricTab_USAGE_METRIC_TAB_TOKENS
	}

	m := findMachineByID(st, req.MachineId)
	vm := &corev1.UsageViewModel{
		MachineId:  req.MachineId,
		Preset:     preset,
		Metric:     metric,
		FocusAgent: req.FocusAgent,
		FocusModel: req.FocusModel,
	}
	_ = syncView

	if m == nil {
		vm.MachineName = machine.ShortID(req.MachineId)
		vm.Loading = waitingForMachineList(st)
		return vm
	}

	vm.Found = true
	vm.MachineName = lists.MachineName(m, req.MachineId)
	vm.Online = lists.MachineReachable(m)
	vm.Supported = m.Meta != nil && m.Meta.Capabilities != nil && m.Meta.Capabilities.UsageStats
	if !vm.Supported {
		vm.UnsupportedFootnote = text.Msg(text.KeyUsageUnsupportedBody, text.Str("name", vm.MachineName))
		return vm
	}

	key := UsageCacheKey{MachineID: m.ID, Preset: preset}
	if preset == v1.UsageRangePreset_USAGE_RANGE_PRESET_CUSTOM {
		key.CustomStartMs = req.CustomStartMs
		key.CustomEndMs = req.CustomEndMs
	}
	var rawEntry *UsageSnapshotEntry
	if st.UsageSnapshots != nil {
		rawEntry = st.UsageSnapshots[key]
	}

	snap, cachedOffline, cachedAtMs, loading := lookupUsageSnapshot(st, m.ID, preset, key.CustomStartMs, key.CustomEndMs, vm.Online)
	vm.Loading = loading
	vm.CachedOffline = cachedOffline
	vm.CachedAtMs = cachedAtMs

	if vm.Online && snap == nil && rawEntry != nil && !loading {
		if !text.IsEmpty(rawEntry.ErrorText) {
			vm.ErrorText = rawEntry.ErrorText
		} else if rawEntry.Error != "" {
			vm.ErrorText = text.Plain(rawEntry.Error)
		}
	} else if vm.Online && snap != nil && rawEntry != nil && !loading {
		if !text.IsEmpty(rawEntry.ErrorText) {
			vm.ErrorText = rawEntry.ErrorText
		} else if rawEntry.Error != "" {
			vm.ErrorText = text.Plain(rawEntry.Error)
		}
	}

	if snap != nil {
		vm.Granularity = snap.Granularity
		vm.StartMs = snap.StartMs
		vm.EndMs = snap.EndMs
		vm.Backfilling = snap.Backfilling
		vm.BackfillScannedDays = snap.BackfillScannedDays
		vm.BackfillTotalDays = snap.BackfillTotalDays
		vm.PreviousPeriodBackfilling = snap.PreviousPeriodBackfilling

		// Filter sources & series against current disabled_agents immediately.
		disabled := lists.MachineDisabledSet(m.Meta)
		var availCount int
		var unavailNames []string
		for _, src := range snap.Sources {
			if src == nil || (disabled != nil && disabled[lists.EffectiveAgent(src.Agent)]) {
				continue
			}
			vm.Sources = append(vm.Sources, src.Clone())
			if src.State == v1.UsageSourceState_USAGE_SOURCE_STATE_UNAVAILABLE {
				unavailNames = append(unavailNames, lists.AgentInfo(src.Agent).Name)
			} else {
				availCount++
			}
		}

		// Filter disabled agents from series before building view models.
		filteredResp := &v1.UsageStatsResponse{
			Preset:                    snap.Preset,
			Granularity:               snap.Granularity,
			StartMs:                   snap.StartMs,
			EndMs:                     snap.EndMs,
			GeneratedAtMs:             snap.GeneratedAtMs,
			Backfilling:               snap.Backfilling,
			BackfillScannedDays:       snap.BackfillScannedDays,
			BackfillTotalDays:         snap.BackfillTotalDays,
			PreviousPeriodBackfilling: snap.PreviousPeriodBackfilling,
			Series:                    filterDisabledSeries(m.Meta, snap.Series),
		}
		vm.Series = usage.BuildSeriesViews(filteredResp, req.FocusAgent, req.FocusModel)

		for _, s := range vm.Series {
			if s.Current != nil && s.Current.UsageRequestCount > 0 {
				vm.HasAnyUsage = true
			}
			if s.Current != nil && s.Current.SpeedRequestCount > 0 {
				vm.HasAnySpeed = true
			}
			if !s.CacheWriteSupported {
				vm.ShowCacheWriteFootnote = true
			}
		}

		if len(unavailNames) > 0 {
			joined := strings.Join(unavailNames, ", ")
			if availCount == 0 && len(vm.Series) == 0 {
				vm.UnsupportedFootnote = text.Msg(text.KeyUsageOnlyUnavailableTitle, text.Str("agents", joined))
			} else {
				vm.UnsupportedFootnote = text.Msg(text.KeyUsageMixedUnavailableFootnote, text.Str("agents", joined))
			}
		}
	} else {
		// No snapshot yet: still derive Antigravity-only notice if only unavailable CLI is enabled.
		enabled := lists.MachineAgents(m.Meta)
		var avail int
		var unavail []string
		for _, a := range enabled {
			if a == v1.Agent_AGENT_ANTIGRAVITY {
				unavail = append(unavail, lists.AgentInfo(a).Name)
			} else {
				avail++
			}
		}
		if avail == 0 && len(unavail) > 0 && !vm.Loading {
			vm.UnsupportedFootnote = text.Msg(text.KeyUsageOnlyUnavailableTitle, text.Str("agents", strings.Join(unavail, ", ")))
		}
	}
	return vm
}
