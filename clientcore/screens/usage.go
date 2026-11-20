package screens

import (
	"math"
	"strings"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/machine"
	"github.com/Lewin671/remote-agent/clientcore/domain/usage"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

func computeRangeDays(preset v1.UsageRangePreset, startMs, endMs, customStartMs, customEndMs int64) int32 {
	if startMs > 0 && endMs > startMs {
		d := int32(math.Round(float64(endMs-startMs) / 86_400_000.0))
		if d < 1 {
			return 1
		}
		return d
	}
	switch preset {
	case v1.UsageRangePreset_USAGE_RANGE_PRESET_TODAY:
		return 1
	case v1.UsageRangePreset_USAGE_RANGE_PRESET_THIRTY_DAYS:
		return 30
	case v1.UsageRangePreset_USAGE_RANGE_PRESET_CUSTOM:
		if customStartMs > 0 && customEndMs >= customStartMs {
			d := int32(math.Round(float64(customEndMs-customStartMs)/86_400_000.0)) + 1
			if d < 1 {
				return 1
			}
			return d
		}
		return 7
	default:
		return 7
	}
}

// BuildUsageView constructs the UsageViewModel for the Usage & Speed page.
func BuildUsageView(st *State, syncView *corev1.SyncStateView, req *corev1.UsageScreenRequest) *corev1.UsageViewModel {
	if st == nil {
		st = NewState(nil)
	}
	if req == nil {
		req = &corev1.UsageScreenRequest{}
	}
	preset := req.Preset
	if preset == v1.UsageRangePreset_USAGE_RANGE_PRESET_UNSPECIFIED {
		preset = v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS
	}

	var ui *UsageUIState
	if st.UsageUI != nil && req.MachineId != "" {
		ui = st.UsageUI[req.MachineId]
	}
	sortBy := corev1.UsageSortColumn_USAGE_SORT_COLUMN_TOKENS
	if ui != nil && ui.SortBy != corev1.UsageSortColumn_USAGE_SORT_COLUMN_UNSPECIFIED {
		sortBy = ui.SortBy
	}

	m := findMachineByID(st, req.MachineId)
	vm := &corev1.UsageViewModel{
		MachineId: req.MachineId,
		Preset:    preset,
		RangeDays: computeRangeDays(preset, 0, 0, req.CustomStartMs, req.CustomEndMs),
		SortBy:    sortBy,
		Hero: &corev1.UsageHeroView{
			Composition: &corev1.UsageCompositionView{},
		},
		BarChart:         &corev1.UsageBarChartView{},
		CacheHitStrip:    &corev1.UsageMetricStripView{LastValidIndex: -1},
		OutputSpeedStrip: &corev1.UsageMetricStripView{LastValidIndex: -1},
		TtftStrip:        &corev1.UsageMetricStripView{LastValidIndex: -1},
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

	if vm.Online && rawEntry != nil && !loading {
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
		vm.RangeDays = computeRangeDays(preset, snap.StartMs, snap.EndMs, req.CustomStartMs, req.CustomEndMs)
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

		filteredSeries := filterDisabledSeries(m.Meta, snap.Series)
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
			Series:                    filteredSeries,
		}

		selectedKey := ""
		if ui != nil && ui.HasSelectionOverride {
			selectedKey = ui.SelectedSeriesKey
		} else if req.FocusAgent != v1.Agent_AGENT_UNSPECIFIED && req.FocusModel != "" {
			selectedKey = usage.SeriesKey(req.FocusAgent, req.FocusModel)
		} else if req.FocusAgent != v1.Agent_AGENT_UNSPECIFIED || req.FocusModel != "" {
			for _, s := range usage.ActiveSeries(filteredResp) {
				if usage.MatchesSeries(s.Agent, s.Model, req.FocusAgent, req.FocusModel) {
					selectedKey = usage.SeriesKey(s.Agent, s.Model)
					break
				}
			}
		}

		var hasScrub bool
		var scrubIdx int32
		if ui != nil {
			hasScrub = ui.HasScrub
			scrubIdx = ui.ScrubBucketIndex
		}

		proj := usage.BuildUsageProjection(filteredResp, usage.ProjectionOptions{
			SortBy:            sortBy,
			SelectedSeriesKey: selectedKey,
			HasScrub:          hasScrub,
			ScrubBucketIndex:  scrubIdx,
		})
		vm.HasAnyUsage = proj.HasAnyUsage
		vm.Hero = proj.Hero
		vm.BarChart = proj.BarChart
		vm.CacheHitStrip = proj.CacheHitStrip
		vm.OutputSpeedStrip = proj.OutputSpeedStrip
		vm.TtftStrip = proj.TtftStrip
		vm.Rows = proj.Rows

		if len(unavailNames) > 0 {
			joined := strings.Join(unavailNames, ", ")
			if availCount == 0 && len(vm.Rows) == 0 {
				vm.UnsupportedFootnote = text.Msg(text.KeyUsageOnlyUnavailableTitle, text.Str("agents", joined))
			} else {
				vm.UnsupportedFootnote = text.Msg(text.KeyUsageMixedUnavailableFootnote, text.Str("agents", joined))
			}
		}
	}
	return vm
}
