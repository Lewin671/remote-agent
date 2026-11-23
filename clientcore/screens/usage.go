package screens

import (
	"math"
	"strconv"
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
		vm.Subtitle = text.Plain(vm.MachineName)
		vm.Loading = waitingForMachineList(st)
		if vm.Loading {
			vm.EmptyKind = corev1.UsageEmptyKind_USAGE_EMPTY_KIND_LOADING
		} else {
			vm.EmptyKind = corev1.UsageEmptyKind_USAGE_EMPTY_KIND_NOT_FOUND
			vm.EmptyTitle = text.Msg(text.KeySettingsDeviceNotFound)
		}
		vm.Hero.TotalTokensText = text.Plain("0")
		vm.Hero.ScopeText = text.Msg(text.KeyUsageScopeAllEmpty)
		return vm
	}

	vm.Found = true
	vm.MachineName = lists.MachineName(m, req.MachineId)
	vm.Online = lists.MachineReachable(m)
	if !vm.Online {
		vm.Subtitle = text.Msg(text.KeyUsageSubtitleOffline, text.Str("name", vm.MachineName))
	} else {
		vm.Subtitle = text.Plain(vm.MachineName)
	}
	vm.Supported = m.Meta != nil && m.Meta.Capabilities != nil && m.Meta.Capabilities.UsageStats
	if !vm.Supported {
		vm.UnsupportedFootnote = text.Msg(text.KeyUsageUnsupportedBody, text.Str("name", vm.MachineName))
		vm.EmptyKind = corev1.UsageEmptyKind_USAGE_EMPTY_KIND_UNSUPPORTED
		vm.EmptyTitle = text.Msg(text.KeyUsageUnsupportedTitle)
		vm.EmptyBody = vm.UnsupportedFootnote
		vm.Hero.TotalTokensText = text.Plain("0")
		vm.Hero.ScopeText = text.Msg(text.KeyUsageScopeAllEmpty)
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

	populateUsageDecorations(vm)
	return vm
}

func populateUsageDecorations(vm *corev1.UsageViewModel) {
	if vm == nil {
		return
	}
	name := vm.MachineName
	if name == "" {
		name = vm.MachineId
	}

	// Banners (shown when found && supported)
	if vm.Found && vm.Supported {
		if vm.CachedOffline {
			var tArg *corev1.TextArg
			if vm.CachedAtMs > 0 {
				tArg = text.ArgTimestamp("time", vm.CachedAtMs, corev1.TimestampStyle_TIMESTAMP_STYLE_TIME)
			} else {
				tArg = text.Str("time", "")
			}
			vm.Banners = append(vm.Banners, &corev1.UsageBannerView{
				Kind: corev1.UsageBannerKind_USAGE_BANNER_KIND_OFFLINE_CACHED,
				Text: text.Msg(text.KeyUsageOfflineBanner, text.Str("name", name), tArg),
			})
		}
		if vm.Backfilling {
			total := vm.BackfillTotalDays
			if total <= 0 {
				total = 90
			}
			vm.Banners = append(vm.Banners, &corev1.UsageBannerView{
				Kind: corev1.UsageBannerKind_USAGE_BANNER_KIND_BACKFILLING,
				Text: text.Msg(text.KeyUsageBackfillingBanner, text.Int("scanned", int64(vm.BackfillScannedDays)), text.Int("total", int64(total))),
			})
		}
		if !text.IsEmpty(vm.ErrorText) && vm.HasAnyUsage {
			vm.Banners = append(vm.Banners, &corev1.UsageBannerView{
				Kind:     corev1.UsageBannerKind_USAGE_BANNER_KIND_LOAD_ERROR,
				IsError:  true,
				CanRetry: true,
				Text:     text.JoinLiteral("：", text.Msg(text.KeyUsageLoadErrorTitle), vm.ErrorText),
			})
		}
	}

	// Empty state matrix
	hasErr := !text.IsEmpty(vm.ErrorText)
	allSourcesUnavailable := false
	var unavailNames []string
	if len(vm.Sources) > 0 {
		allUnavail := true
		for _, s := range vm.Sources {
			if s == nil {
				continue
			}
			if s.State == v1.UsageSourceState_USAGE_SOURCE_STATE_UNAVAILABLE {
				unavailNames = append(unavailNames, lists.AgentInfo(s.Agent).Name)
			} else {
				allUnavail = false
			}
		}
		allSourcesUnavailable = allUnavail && len(unavailNames) > 0
	}

	switch {
	case vm.HasAnyUsage:
		vm.EmptyKind = corev1.UsageEmptyKind_USAGE_EMPTY_KIND_NONE
	case !vm.Online && !vm.CachedOffline && !vm.Loading:
		vm.EmptyKind = corev1.UsageEmptyKind_USAGE_EMPTY_KIND_OFFLINE_EMPTY
		vm.EmptyTitle = text.Msg(text.KeyUsageOfflineEmptyTitle, text.Str("name", name))
		vm.EmptyBody = text.Msg(text.KeyUsageOfflineEmptyBody, text.Str("name", name))
	case hasErr && !vm.Loading:
		vm.EmptyKind = corev1.UsageEmptyKind_USAGE_EMPTY_KIND_LOAD_ERROR
		vm.EmptyTitle = text.Msg(text.KeyUsageLoadErrorTitle)
		vm.EmptyBody = vm.ErrorText
		vm.EmptyCanRetry = true
	case vm.Loading:
		vm.EmptyKind = corev1.UsageEmptyKind_USAGE_EMPTY_KIND_LOADING
	case (vm.Online || vm.CachedOffline) && allSourcesUnavailable:
		joined := strings.Join(unavailNames, ", ")
		vm.EmptyKind = corev1.UsageEmptyKind_USAGE_EMPTY_KIND_ALL_SOURCES_UNAVAILABLE
		vm.EmptyTitle = text.Msg(text.KeyUsageOnlyUnavailableTitle, text.Str("agents", joined))
		vm.EmptyBody = text.Msg(text.KeyUsageOnlyUnavailableBody, text.Str("agents", joined))
	default:
		vm.EmptyKind = corev1.UsageEmptyKind_USAGE_EMPTY_KIND_NO_RECORDS
		vm.EmptyTitle = text.Msg(text.KeyUsageEmptyNoRecords)
	}

	// Hero formatted scope & subline + charts a11y
	if vm.Hero == nil {
		vm.Hero = &corev1.UsageHeroView{Composition: &corev1.UsageCompositionView{}}
	}
	if !vm.HasAnyUsage {
		vm.Hero.TotalTokensText = text.Plain("0")
		vm.Hero.ScopeText = text.Msg(text.KeyUsageScopeAllEmpty)
	} else {
		if vm.Hero.TotalTokensText == nil {
			vm.Hero.TotalTokensText = usage.FormatTokensText(vm.Hero.TotalTokens)
		}
		if !vm.Hero.Scrubbing {
			if vm.Hero.HasSelectedModel {
				vm.Hero.ScopeText = text.Plain(vm.Hero.SelectedModel)
			} else {
				vm.Hero.ScopeText = text.Msg(text.KeyUsageScopeAllModels, text.Int("count", int64(vm.Hero.TotalModelCount)))
			}
			vm.Hero.SublineText = buildUsageHeroSubline(vm, vm.Hero)
		}
		vm.ChartsA11YLabel = text.Msg(
			text.KeyUsageChartsA11Y,
			text.Str("name", name),
			text.ArgText("total", vm.Hero.TotalTokensText),
			text.Int("count", int64(len(vm.Rows))),
		)
	}
}

func buildUsageHeroSubline(vm *corev1.UsageViewModel, hero *corev1.UsageHeroView) *corev1.Text {
	if hero == nil || hero.Scrubbing {
		return nil
	}
	parts := []*corev1.Text{
		text.Msg(
			text.KeyUsageHeroReqs,
			text.Int("count", int64(hero.RequestCount)),
			text.Str("formatted", usage.FormatCommaUint(hero.RequestCount)),
		),
	}
	if hero.HasSharePercent {
		shareStr := "<1%"
		if hero.ShareRatio >= 0.01 {
			shareStr = strconv.Itoa(int(math.Round(hero.ShareRatio*100.0))) + "%"
		}
		parts = append(parts, text.Msg(text.KeyUsageHeroShare, text.Str("pct", shareStr)))
	}
	if hero.PreviousBackfilling {
		parts = append(parts, text.Msg(text.KeyUsageBackfillingCell))
	} else if hero.HasTokensDelta {
		if math.IsNaN(hero.TokensDeltaPercent) || math.IsInf(hero.TokensDeltaPercent, 0) || math.Abs(hero.TokensDeltaPercent) < 0.5 {
			parts = append(parts, text.Msg(text.KeyUsageFlat))
		} else {
			var period *corev1.Text
			if vm.RangeDays <= 1 {
				period = text.Msg(text.KeyUsageVsYesterday)
			} else {
				period = text.Msg(text.KeyUsageVsPrevDays, text.Int("days", int64(vm.RangeDays)))
			}
			parts = append(parts, text.JoinLiteral(" ", usage.FormatDeltaTriangle(hero.TokensDeltaPercent), period))
		}
	}
	return text.JoinLiteral(" · ", parts...)
}
