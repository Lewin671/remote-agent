package app

import (
	"slices"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/outcome"
	"github.com/Lewin671/remote-agent/clientcore/domain/usage"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/screens"
	"github.com/Lewin671/remote-agent/clientcore/sync"
)

const usageStaleMs int64 = 60_000

func usageCacheKey(machineID string, preset v1.UsageRangePreset, cStart, cEnd int64) screens.UsageCacheKey {
	if preset == v1.UsageRangePreset_USAGE_RANGE_PRESET_UNSPECIFIED {
		preset = v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS
	}
	if preset != v1.UsageRangePreset_USAGE_RANGE_PRESET_CUSTOM {
		cStart, cEnd = 0, 0
	}
	return screens.UsageCacheKey{
		MachineID:     machineID,
		Preset:        preset,
		CustomStartMs: cStart,
		CustomEndMs:   cEnd,
	}
}

func (m *Model) ensureUsageEntry(key screens.UsageCacheKey) *screens.UsageSnapshotEntry {
	if m.state.UsageSnapshots == nil {
		m.state.UsageSnapshots = make(map[screens.UsageCacheKey]*screens.UsageSnapshotEntry)
	}
	entry := m.state.UsageSnapshots[key]
	if entry == nil {
		entry = &screens.UsageSnapshotEntry{}
		m.state.UsageSnapshots[key] = entry
	}
	return entry
}

func (m *Model) ensureUsageUI(machineID string) *screens.UsageUIState {
	if m.state.UsageUI == nil {
		m.state.UsageUI = make(map[string]*screens.UsageUIState)
	}
	ui := m.state.UsageUI[machineID]
	if ui == nil {
		ui = &screens.UsageUIState{
			SortBy: corev1.UsageSortColumn_USAGE_SORT_COLUMN_TOKENS,
		}
		m.state.UsageUI[machineID] = ui
	}
	return ui
}

func (c *Core) viewUsage(req *corev1.UsageScreenRequest) (*corev1.ViewModel, error) {
	sv := c.buildSyncStateView()
	vm := screens.BuildUsageView(c.m.state, sv, req)
	return &corev1.ViewModel{
		View: &corev1.ViewModel_Usage{
			Usage: vm,
		},
	}, nil
}

func (c *Core) appendUsageRender(effects []*corev1.Effect, syncDirty sync.DirtyFlags) []*corev1.Effect {
	want := c.m.usageDirty || (c.m.usageActive && (syncDirty.SyncState || syncDirty.Home))
	c.m.usageDirty = false
	if !want {
		return effects
	}
	for _, eff := range effects {
		if eff == nil {
			continue
		}
		if r := eff.GetRender(); r != nil {
			if !slices.Contains(r.Screens, corev1.ScreenTarget_SCREEN_TARGET_USAGE) {
				r.Screens = append(r.Screens, corev1.ScreenTarget_SCREEN_TARGET_USAGE)
			}
			return effects
		}
	}
	return append(effects, &corev1.Effect{
		Effect: &corev1.Effect_Render{
			Render: &corev1.RenderEffect{
				Screens: []corev1.ScreenTarget{corev1.ScreenTarget_SCREEN_TARGET_USAGE},
			},
		},
	})
}

func (c *Core) dispatchUsageUI(ctx *sync.StepContext, ev *corev1.Event) []*corev1.Effect {
	switch k := ev.Event.(type) {
	case *corev1.Event_UsageScreenOpen:
		if k.UsageScreenOpen == nil || k.UsageScreenOpen.MachineId == "" {
			return nil
		}
		src := k.UsageScreenOpen
		u := &corev1.UsageScreenOpenEvent{
			MachineId:       src.MachineId,
			Preset:          src.Preset,
			CustomStartMs:   src.CustomStartMs,
			CustomEndMs:     src.CustomEndMs,
			Timezone:        src.Timezone,
			TzOffsetMinutes: src.TzOffsetMinutes,
			BucketBoundsMs:  slices.Clone(src.BucketBoundsMs),
			PrevStartMs:     src.PrevStartMs,
			ApplyFocus:      src.ApplyFocus,
			FocusAgent:      src.FocusAgent,
			FocusModel:      src.FocusModel,
		}
		if u.Preset == v1.UsageRangePreset_USAGE_RANGE_PRESET_UNSPECIFIED {
			u.Preset = v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS
		}
		if c.m.lastUsageParams == nil {
			c.m.lastUsageParams = make(map[string]*corev1.UsageScreenOpenEvent)
		}
		c.m.lastUsageParams[u.MachineId] = u
		ui := c.m.ensureUsageUI(u.MachineId)
		ui.HasScrub = false
		if u.ApplyFocus {
			ui.SortBy = corev1.UsageSortColumn_USAGE_SORT_COLUMN_TOKENS
			if u.FocusAgent != v1.Agent_AGENT_UNSPECIFIED && u.FocusModel != "" {
				ui.SelectedSeriesKey = usage.SeriesKey(u.FocusAgent, u.FocusModel)
				ui.HasSelectionOverride = true
			} else if u.FocusAgent == v1.Agent_AGENT_UNSPECIFIED && u.FocusModel == "" {
				ui.SelectedSeriesKey = ""
				ui.HasSelectionOverride = true
			} else {
				ui.SelectedSeriesKey = ""
				ui.HasSelectionOverride = false
			}
		}
		c.m.usageActive = true
		var effects []*corev1.Effect
		c.maybeHydrateUsageCache(u.MachineId, &effects)
		c.fetchUsage(ctx, u.MachineId, u.Preset, u.CustomStartMs, u.CustomEndMs, u)
		c.m.usageDirty = true
		return effects

	case *corev1.Event_UsageScreenRetry:
		if k.UsageScreenRetry == nil || k.UsageScreenRetry.MachineId == "" {
			return nil
		}
		id := k.UsageScreenRetry.MachineId
		c.m.usageActive = true
		params := c.m.lastUsageParams[id]
		if params != nil {
			c.fetchUsage(ctx, id, params.Preset, params.CustomStartMs, params.CustomEndMs, params)
		} else {
			c.fetchUsage(ctx, id, v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS, 0, 0, nil)
		}
		c.m.usageDirty = true

	case *corev1.Event_UsageScreenSelectSort:
		if k.UsageScreenSelectSort == nil || k.UsageScreenSelectSort.MachineId == "" {
			return nil
		}
		ui := c.m.ensureUsageUI(k.UsageScreenSelectSort.MachineId)
		sortBy := k.UsageScreenSelectSort.SortBy
		if sortBy == corev1.UsageSortColumn_USAGE_SORT_COLUMN_UNSPECIFIED {
			sortBy = corev1.UsageSortColumn_USAGE_SORT_COLUMN_TOKENS
		}
		ui.SortBy = sortBy
		c.m.usageActive = true
		c.m.usageDirty = true

	case *corev1.Event_UsageScreenSelectSeries:
		if k.UsageScreenSelectSeries == nil || k.UsageScreenSelectSeries.MachineId == "" {
			return nil
		}
		sel := k.UsageScreenSelectSeries
		ui := c.m.ensureUsageUI(sel.MachineId)
		ui.HasSelectionOverride = true
		ui.HasScrub = false
		if sel.Toggle && ui.SelectedSeriesKey == sel.SeriesKey {
			ui.SelectedSeriesKey = ""
		} else {
			ui.SelectedSeriesKey = sel.SeriesKey
		}
		c.m.usageActive = true
		c.m.usageDirty = true

	case *corev1.Event_UsageScreenScrub:
		if k.UsageScreenScrub == nil || k.UsageScreenScrub.MachineId == "" {
			return nil
		}
		sc := k.UsageScreenScrub
		ui := c.m.ensureUsageUI(sc.MachineId)
		if sc.Clear {
			ui.HasScrub = false
		} else {
			ui.HasScrub = true
			ui.ScrubBucketIndex = sc.BucketIndex
		}
		c.m.usageActive = true
		c.m.usageDirty = true

	case *corev1.Event_UsageCacheLoaded:
		if k.UsageCacheLoaded == nil || k.UsageCacheLoaded.MachineId == "" {
			return nil
		}
		if !c.m.sync.CanUseMachineCache(k.UsageCacheLoaded.MachineId) {
			return []*corev1.Effect{{
				Effect: &corev1.Effect_UsageCacheRequest{
					UsageCacheRequest: &corev1.UsageCacheRequestEffect{
						MachineId:   k.UsageCacheLoaded.MachineId,
						DeleteCache: true,
					},
				},
			}}
		}
		var snap *v1.UsageStatsResponse
		if len(k.UsageCacheLoaded.RawSnapshot) > 0 {
			var parsed v1.UsageStatsResponse
			if parsed.Unmarshal(k.UsageCacheLoaded.RawSnapshot) != nil {
				return nil
			}
			snap = &parsed
		} else if k.UsageCacheLoaded.Snapshot != nil {
			snap = k.UsageCacheLoaded.Snapshot
		} else {
			return nil
		}
		preset := snap.Preset
		if preset == v1.UsageRangePreset_USAGE_RANGE_PRESET_UNSPECIFIED {
			preset = k.UsageCacheLoaded.Preset
		}
		key := usageCacheKey(k.UsageCacheLoaded.MachineId, preset, 0, 0)
		entry := c.m.ensureUsageEntry(key)
		entry.Cached = snap.Clone()
		entry.CachedAtMs = snap.GeneratedAtMs
		if c.m.usageActive {
			c.m.usageDirty = true
		}
		if c.m.settings != nil && c.m.settings.Active {
			c.m.settingsDirty.MachineStatus = true
			c.m.settingsDirty.MachineSettings = true
		}
		if len(c.m.ensureSessionStore().opened) > 0 {
			c.m.ensureSessionStore().pendingDirty = true
		}

	case *corev1.Event_UsageCacheWritten:
		if k.UsageCacheWritten == nil || k.UsageCacheWritten.MachineId == "" {
			return nil
		}
		mid := k.UsageCacheWritten.MachineId
		if !c.m.sync.CanUseMachineCache(mid) {
			c.forgetUsageMachine(mid)
			return []*corev1.Effect{{
				Effect: &corev1.Effect_UsageCacheRequest{
					UsageCacheRequest: &corev1.UsageCacheRequestEffect{
						MachineId:   mid,
						DeleteCache: true,
					},
				},
			}}
		}
	}
	return nil
}

func (c *Core) maybeHydrateUsageCache(machineID string, effects *[]*corev1.Effect) {
	if machineID == "" || effects == nil || !c.m.sync.CanUseMachineCache(machineID) {
		return
	}
	if c.m.usageCacheRequested == nil {
		c.m.usageCacheRequested = make(map[string]bool)
	}
	if c.m.usageCacheRequested[machineID] {
		return
	}
	c.m.usageCacheRequested[machineID] = true
	*effects = append(*effects, &corev1.Effect{
		Effect: &corev1.Effect_UsageCacheRequest{
			UsageCacheRequest: &corev1.UsageCacheRequestEffect{
				MachineId:   machineID,
				DeleteCache: false,
			},
		},
	})
}

func (c *Core) forgetUsageMachine(machineID string) {
	if machineID == "" {
		return
	}
	delete(c.m.usageCacheRequested, machineID)
	for k := range c.m.state.UsageSnapshots {
		if k.MachineID == machineID {
			delete(c.m.state.UsageSnapshots, k)
		}
	}
	for k := range c.m.lastUsageFetchedMs {
		if k.MachineID == machineID {
			delete(c.m.lastUsageFetchedMs, k)
		}
	}
}

func (c *Core) maybeFetchUsage(ctx *sync.StepContext, machineID string, preset v1.UsageRangePreset, openEv *corev1.UsageScreenOpenEvent) {
	if machineID == "" || preset == v1.UsageRangePreset_USAGE_RANGE_PRESET_CUSTOM {
		return
	}
	if preset == v1.UsageRangePreset_USAGE_RANGE_PRESET_UNSPECIFIED {
		preset = v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS
	}
	mac := c.m.findMachine(machineID)
	if !lists.MachineReachable(mac) || mac.Meta == nil || mac.Meta.Capabilities == nil || !mac.Meta.Capabilities.UsageStats {
		return
	}
	key := usageCacheKey(machineID, preset, 0, 0)
	entry := c.m.ensureUsageEntry(key)
	if entry.Loading {
		return
	}
	nowMs := c.m.sync.NowMs()
	if last := c.m.lastUsageFetchedMs[key]; last > 0 && nowMs > 0 && nowMs-last < usageStaleMs {
		return
	}
	c.fetchUsage(ctx, machineID, preset, 0, 0, openEv)
}

func (c *Core) fetchUsage(ctx *sync.StepContext, machineID string, preset v1.UsageRangePreset, cStart, cEnd int64, openEv *corev1.UsageScreenOpenEvent) {
	if machineID == "" {
		return
	}
	if preset == v1.UsageRangePreset_USAGE_RANGE_PRESET_UNSPECIFIED {
		preset = v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS
	}
	mac := c.m.findMachine(machineID)
	if !lists.MachineReachable(mac) || mac.Meta == nil || mac.Meta.Capabilities == nil || !mac.Meta.Capabilities.UsageStats {
		return
	}
	key := usageCacheKey(machineID, preset, cStart, cEnd)
	entry := c.m.ensureUsageEntry(key)

	req := &v1.GetUsageStatsRequest{
		Preset:        preset,
		CustomStartMs: key.CustomStartMs,
		CustomEndMs:   key.CustomEndMs,
	}
	if openEv != nil {
		req.Timezone = openEv.Timezone
		req.TzOffsetMinutes = openEv.TzOffsetMinutes
		if len(openEv.BucketBoundsMs) > 0 {
			req.BucketBoundsMs = slices.Clone(openEv.BucketBoundsMs)
		}
		req.PrevStartMs = openEv.PrevStartMs
	}

	reqID, ok := c.m.sync.SubmitCommand(ctx, sync.SubmitCommandParams{
		MachineID: machineID,
		Command: &v1.Command{
			Kind: &v1.Command_GetUsageStats{
				GetUsageStats: req,
			},
		},
	})
	if !ok {
		entry.Loading = false
		entry.ActiveReqID = ""
		return
	}
	entry.Loading = true
	entry.ActiveReqID = reqID
	entry.Error = ""
	entry.ErrorText = nil
	if reqID != "" {
		c.m.pendingCmdMeta[reqID] = cmdMeta{
			kind:      cmdKindGetUsageStats,
			machineID: machineID,
			usageKey:  key,
		}
	}
}

func (c *Core) onUsageStatsSettled(sc sync.SettledCommand, meta cmdMeta, dirty *sync.DirtyFlags) []*corev1.Effect {
	entry := c.m.ensureUsageEntry(meta.usageKey)
	if entry.ActiveReqID != "" && sc.RequestID != "" && entry.ActiveReqID != sc.RequestID {
		return nil
	}
	entry.Loading = false
	entry.ActiveReqID = ""

	var out []*corev1.Effect
	nowMs := c.m.sync.NowMs()

	if sc.Category == outcome.Executed && sc.ErrorCode == "" && sc.Result != nil && sc.Result.GetUsageStats() != nil {
		resp := sc.Result.GetUsageStats().Clone()
		entry.Live = resp
		entry.LiveAtMs = nowMs
		entry.Error = ""
		entry.ErrorText = nil
		if c.m.lastUsageFetchedMs == nil {
			c.m.lastUsageFetchedMs = make(map[screens.UsageCacheKey]int64)
		}
		c.m.lastUsageFetchedMs[meta.usageKey] = nowMs

		if meta.usageKey.Preset != v1.UsageRangePreset_USAGE_RANGE_PRESET_CUSTOM && c.m.sync.CanUseMachineCache(meta.machineID) {
			entry.Cached = resp.Clone()
			entry.CachedAtMs = resp.GeneratedAtMs
			if entry.CachedAtMs == 0 {
				entry.CachedAtMs = nowMs
			}
			eff := &corev1.UsageCacheWriteEffect{
				MachineId: meta.machineID,
				Preset:    resp.Preset,
			}
			if c.m.sync.RelayPipeEnabled() {
				eff.RawSnapshot = resp.AppendMarshal(nil)
			} else {
				eff.Snapshot = resp.Clone()
			}
			out = append(out, &corev1.Effect{
				Effect: &corev1.Effect_UsageCacheWrite{UsageCacheWrite: eff},
			})
		}

		if meta.usageKey.Preset == v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS && len(usage.ActiveSeries(resp)) == 0 {
			prevStep, ctx := c.m.sync.BeginStep()
			c.maybeFetchUsage(ctx, meta.machineID, v1.UsageRangePreset_USAGE_RANGE_PRESET_THIRTY_DAYS, nil)
			res := c.m.sync.EndStep(prevStep, ctx)
			out = append(out, res.Effects...)
		}
	} else {
		entry.ErrorText = screens.ReasonText(sc.ErrorReason)
		entry.Error = sc.ErrorCode
		if entry.Error == "" && entry.ErrorText == nil {
			entry.Error = "failed"
		}
	}

	if c.m.usageActive {
		c.m.usageDirty = true
	}
	if c.m.settings != nil && c.m.settings.Active {
		c.m.settingsDirty.MachineStatus = true
		c.m.settingsDirty.MachineSettings = true
	}
	if len(c.m.ensureSessionStore().opened) > 0 {
		c.m.ensureSessionStore().pendingDirty = true
	}
	dirty.MarkSessionData()
	return out
}
