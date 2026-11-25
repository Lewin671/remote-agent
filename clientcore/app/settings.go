package app

import (
	"strings"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/machine"
	"github.com/Lewin671/remote-agent/clientcore/domain/outcome"
	"github.com/Lewin671/remote-agent/clientcore/domain/sortutil"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/screens"
	"github.com/Lewin671/remote-agent/clientcore/sync"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

const (
	cmdKindGetMachineStatus cmdKind = 100
	cmdKindUpdateSettings   cmdKind = 101
)

type settingsDirtyFlags struct {
	Settings          bool
	MachineStatus     bool
	MachineSettings   bool
	DirectDiagnostics bool
	Quota             bool
	Devices           bool
	ClientDetail      bool
}

func (d *settingsDirtyFlags) markAll() {
	d.Settings = true
	d.MachineStatus = true
	d.MachineSettings = true
	d.DirectDiagnostics = true
	d.Quota = true
	d.Devices = true
	d.ClientDetail = true
}

func (m *Model) ensureSettings() *screens.SettingsExtra {
	if m.settings == nil {
		m.settings = screens.NewSettingsExtra()
	}
	return m.settings
}

func (c *Core) viewSettings(screen *corev1.Screen) (*corev1.ViewModel, bool, error) {
	st := c.m.state
	extra := c.m.settings
	if extra == nil {
		extra = screens.NewSettingsExtra()
	}

	switch s := screen.Screen.(type) {
	case *corev1.Screen_Settings:
		sv := c.buildSyncStateView()
		return &corev1.ViewModel{
			View: &corev1.ViewModel_Settings{
				Settings: screens.BuildSettings(st, sv, extra),
			},
		}, true, nil

	case *corev1.Screen_MachineStatus:
		var id string
		if s.MachineStatus != nil {
			id = s.MachineStatus.MachineId
		}
		var feed *screens.MachinePollFeed
		if extra.PollFeeds != nil {
			feed = extra.PollFeeds[id]
		}
		return &corev1.ViewModel{
			View: &corev1.ViewModel_MachineStatus{
				MachineStatus: screens.BuildMachineStatus(st, feed, id),
			},
		}, true, nil

	case *corev1.Screen_MachineSettings:
		var id string
		if s.MachineSettings != nil {
			id = s.MachineSettings.MachineId
		}
		sv := c.buildSyncStateView()
		return &corev1.ViewModel{
			View: &corev1.ViewModel_MachineSettings{
				MachineSettings: screens.BuildMachineSettings(st, sv, extra, id),
			},
		}, true, nil

	case *corev1.Screen_Quota:
		var id string
		var filter v1.Agent
		if s.Quota != nil {
			id = s.Quota.MachineId
			filter = s.Quota.FilterAgent
		}
		sv := c.buildSyncStateView()
		return &corev1.ViewModel{
			View: &corev1.ViewModel_Quota{
				Quota: screens.BuildQuota(st, sv, id, filter),
			},
		}, true, nil

	case *corev1.Screen_Devices:
		sv := c.buildSyncStateView()
		return &corev1.ViewModel{
			View: &corev1.ViewModel_Devices{
				Devices: screens.BuildDevices(st, sv, extra),
			},
		}, true, nil

	case *corev1.Screen_ClientDetail:
		var id string
		if s.ClientDetail != nil {
			id = s.ClientDetail.DeviceId
		}
		sv := c.buildSyncStateView()
		return &corev1.ViewModel{
			View: &corev1.ViewModel_ClientDetail{
				ClientDetail: screens.BuildClientDetail(st, sv, extra, id),
			},
		}, true, nil

	case *corev1.Screen_DirectDiagnostics:
		var id string
		if s.DirectDiagnostics != nil {
			id = s.DirectDiagnostics.MachineId
		}
		prefEnabled, daemonDisabled, status, rounds, blockedAt, blocked := c.m.sync.DirectMachineReport(id)
		return &corev1.ViewModel{
			View: &corev1.ViewModel_DirectDiagnostics{
				DirectDiagnostics: screens.BuildDirectDiagnosticsView(
					id,
					status,
					prefEnabled,
					daemonDisabled,
					rounds,
					blockedAt,
					blocked,
					c.m.state.Cal.NowMs,
					c.m.sync.PlatformAgent(),
				),
			},
		}, true, nil

	default:
		return nil, false, nil
	}
}

func (c *Core) appendSettingsRender(effects []*corev1.Effect, syncDirty sync.DirtyFlags) []*corev1.Effect {
	sd := c.m.settingsDirty
	c.m.settingsDirty = settingsDirtyFlags{}
	if (syncDirty.SyncState || syncDirty.Home) && c.m.settings != nil && c.m.settings.Active {
		sd.markAll()
	}
	if syncDirty.Direct {
		sd.Settings = true
		sd.MachineSettings = true
		sd.DirectDiagnostics = true
	}

	var targets []corev1.ScreenTarget
	if sd.Settings {
		targets = append(targets, corev1.ScreenTarget_SCREEN_TARGET_SETTINGS)
	}
	if sd.MachineStatus {
		targets = append(targets, corev1.ScreenTarget_SCREEN_TARGET_MACHINE_STATUS)
	}
	if sd.MachineSettings {
		targets = append(targets, corev1.ScreenTarget_SCREEN_TARGET_MACHINE_SETTINGS)
	}
	if sd.DirectDiagnostics {
		targets = append(targets, corev1.ScreenTarget_SCREEN_TARGET_DIRECT_DIAGNOSTICS)
	}
	if sd.Quota {
		targets = append(targets, corev1.ScreenTarget_SCREEN_TARGET_QUOTA)
	}
	if sd.Devices {
		targets = append(targets, corev1.ScreenTarget_SCREEN_TARGET_DEVICES)
	}
	if sd.ClientDetail {
		targets = append(targets, corev1.ScreenTarget_SCREEN_TARGET_CLIENT_DETAIL)
	}
	if len(targets) == 0 {
		return effects
	}
	for _, eff := range effects {
		if eff == nil {
			continue
		}
		if r, ok := eff.Effect.(*corev1.Effect_Render); ok && r.Render != nil {
			for _, t := range targets {
				found := false
				for _, existing := range r.Render.Screens {
					if existing == t {
						found = true
						break
					}
				}
				if !found {
					r.Render.Screens = append(r.Render.Screens, t)
				}
			}
			return effects
		}
	}
	return append(effects, &corev1.Effect{
		Effect: &corev1.Effect_Render{
			Render: &corev1.RenderEffect{Screens: targets},
		},
	})
}

func (c *Core) onGetSettingsSettled(sc sync.SettledCommand, meta cmdMeta) []*corev1.Effect {
	extra := c.m.ensureSettings()
	if sc.Category == outcome.Executed && sc.Result != nil && sc.Result.GetGetSettings() != nil {
		me := extra.EnsureMachine(meta.machineID)
		me.ImageStorage = sc.Result.GetGetSettings().GetImageStorage().Clone()
		if me.SaveUnknown {
			me.SaveUnknown = false
			me.SavePending = false
		}
	}
	if extra.Active {
		c.m.settingsDirty.MachineSettings = true
	}
	return nil
}

func (c *Core) onOtherSettingsCmdSettled(sc sync.SettledCommand, meta cmdMeta) []*corev1.Effect {
	extra := c.m.ensureSettings()
	var out []*corev1.Effect

	switch meta.kind {
	case cmdKindGetMachineStatus:
		feed := extra.EnsureFeed(meta.machineID)
		if !feed.Busy || (feed.ActiveReqID != "" && sc.RequestID != feed.ActiveReqID) {
			return nil
		}
		feed.Busy = false
		feed.ActiveReqID = ""
		out = append(out, &corev1.Effect{
			Effect: &corev1.Effect_ScheduleMachinePoll{
				ScheduleMachinePoll: &corev1.ScheduleMachinePollEffect{
					MachineId:  meta.machineID,
					Generation: feed.Generation,
					Cancel:     true,
				},
			},
		})
		if sc.Category == outcome.Executed && sc.Result != nil && sc.Result.GetMachineStatus() != nil {
			val := sc.Result.GetMachineStatus().Clone()
			if feed.State == corev1.MachinePollState_MACHINE_POLL_STATE_STALE ||
				feed.State == corev1.MachinePollState_MACHINE_POLL_STATE_UNAVAILABLE {
				feed.CurveSinceMs = val.ObservedMs
			}
			feed.Last = val
			feed.Failures = 0
			feed.State = corev1.MachinePollState_MACHINE_POLL_STATE_LIVE
			if c.m.state.Cal.NowMs > 0 {
				feed.ReceivedAtMs = c.m.state.Cal.NowMs
			} else {
				feed.ReceivedAtMs = val.ObservedMs
			}
		} else {
			if feed.Failures < machine.MaxFailures {
				feed.Failures++
			}
			if feed.Last == nil {
				feed.State = corev1.MachinePollState_MACHINE_POLL_STATE_UNAVAILABLE
			} else if feed.Failures >= machine.MaxFailures {
				feed.State = corev1.MachinePollState_MACHINE_POLL_STATE_STALE
			}
		}
		if eff := c.scheduleNextPollIfActive(meta.machineID, feed); eff != nil {
			out = append(out, eff)
		}
		c.m.settingsDirty.MachineStatus = true
		c.m.settingsDirty.MachineSettings = true

	case cmdKindUpdateSettings:
		me := extra.EnsureMachine(meta.machineID)
		me.SavePending = false
		me.CleaningImages = false
		switch sc.Category {
		case outcome.Executed:
			me.SaveUnknown = false
			me.SaveError = nil
			if sc.Result != nil && sc.Result.GetUpdateSettings() != nil {
				resp := sc.Result.GetUpdateSettings()
				se := c.m.state.MachineSettings[meta.machineID]
				if se == nil {
					se = &screens.SettingsCacheEntry{}
					c.m.state.MachineSettings[meta.machineID] = se
				}
				se.Settings = resp.GetSettings().Clone()
				se.Loaded = true
				se.Error = ""
				se.ErrorText = nil
				me.ImageStorage = resp.GetImageStorage().Clone()
			}
		case outcome.Unknown:
			me.SaveUnknown = true
			me.SaveError = nil
			prevStep, ctx := c.m.sync.BeginStep()
			c.submitGetSettings(ctx, meta.machineID)
			res := c.m.sync.EndStep(prevStep, ctx)
			out = append(out, res.Effects...)
		default:
			me.SaveUnknown = false
			if errTxt := screens.ReasonText(sc.ErrorReason); errTxt != nil {
				me.SaveError = text.Msg(text.KeySettingsSaveError, text.ArgText("reason", errTxt))
			} else if sc.ErrorCode != "" {
				me.SaveError = text.Msg(text.KeySettingsSaveError, text.Str("reason", sc.ErrorCode))
			} else {
				me.SaveError = text.Msg(text.KeySettingsSaveError, text.Str("reason", "failed"))
			}
		}
		c.m.settingsDirty.MachineSettings = true
	}

	return out
}

func (c *Core) pollIntervalForMode(mode corev1.MachinePollMode) int64 {
	if mode == corev1.MachinePollMode_MACHINE_POLL_MODE_OVERVIEW {
		return machine.PollIntervalOverviewMs
	}
	return machine.PollIntervalLiveMs
}

func (c *Core) isPollActive(machineID string, feed *screens.MachinePollFeed) bool {
	if feed == nil || !feed.Visible || feed.Mode == corev1.MachinePollMode_MACHINE_POLL_MODE_OFF ||
		feed.Mode == corev1.MachinePollMode_MACHINE_POLL_MODE_UNSPECIFIED {
		return false
	}
	if c.m.sync.Connection() != lists.ConnConnected {
		return false
	}
	mac := c.m.findMachine(machineID)
	if mac == nil || !lists.MachineReachable(mac) || mac.Meta == nil || mac.Meta.Capabilities == nil {
		return false
	}
	return mac.Meta.Capabilities.MachineStatus
}

func (c *Core) scheduleNextPollIfActive(machineID string, feed *screens.MachinePollFeed) *corev1.Effect {
	if !c.isPollActive(machineID, feed) {
		return nil
	}
	interval := c.pollIntervalForMode(feed.Mode)
	elapsed := int64(0)
	if feed.StartedMs > 0 && c.m.state.Cal.NowMs > feed.StartedMs {
		elapsed = c.m.state.Cal.NowMs - feed.StartedMs
	}
	delay := interval - elapsed
	if delay < 0 {
		delay = 0
	}
	return &corev1.Effect{
		Effect: &corev1.Effect_ScheduleMachinePoll{
			ScheduleMachinePoll: &corev1.ScheduleMachinePollEffect{
				MachineId:  machineID,
				DelayMs:    delay,
				Generation: feed.Generation,
			},
		},
	}
}

func (c *Core) startMachineStatusPoll(ctx *sync.StepContext, machineID string, feed *screens.MachinePollFeed) []*corev1.Effect {
	if !c.isPollActive(machineID, feed) || feed.Busy {
		return nil
	}
	reqID, ok := c.m.sync.SubmitCommand(ctx, sync.SubmitCommandParams{
		MachineID: machineID,
		Command: &v1.Command{
			Kind: &v1.Command_GetMachineStatus{
				GetMachineStatus: &v1.GetMachineStatusRequest{},
			},
		},
	})
	if !ok {
		feed.Polling = false
		if feed.Failures < machine.MaxFailures {
			feed.Failures++
		}
		if feed.Last == nil {
			feed.State = corev1.MachinePollState_MACHINE_POLL_STATE_UNAVAILABLE
		} else if feed.Failures >= machine.MaxFailures {
			feed.State = corev1.MachinePollState_MACHINE_POLL_STATE_STALE
		}
		c.m.settingsDirty.MachineStatus = true
		c.m.settingsDirty.MachineSettings = true
		return nil
	}
	if reqID != "" {
		c.m.pendingCmdMeta[reqID] = cmdMeta{
			kind:      cmdKindGetMachineStatus,
			machineID: machineID,
		}
	}
	feed.Busy = true
	feed.ActiveReqID = reqID
	feed.StartedMs = c.m.state.Cal.NowMs
	if feed.Last == nil && (feed.State == corev1.MachinePollState_MACHINE_POLL_STATE_UNSPECIFIED ||
		feed.State == corev1.MachinePollState_MACHINE_POLL_STATE_UNAVAILABLE) {
		feed.State = corev1.MachinePollState_MACHINE_POLL_STATE_LOADING
	}
	c.m.settingsDirty.MachineStatus = true
	c.m.settingsDirty.MachineSettings = true
	return []*corev1.Effect{
		{
			Effect: &corev1.Effect_ScheduleMachinePoll{
				ScheduleMachinePoll: &corev1.ScheduleMachinePollEffect{
					MachineId:  machineID,
					DelayMs:    machine.RPCTimeoutMs,
					Generation: feed.Generation,
				},
			},
		},
	}
}

func (c *Core) submitGetSettings(ctx *sync.StepContext, machineID string) {
	se := c.m.state.MachineSettings[machineID]
	if se == nil {
		se = &screens.SettingsCacheEntry{}
		c.m.state.MachineSettings[machineID] = se
	}
	se.Loading = true
	reqID, ok := c.m.sync.SubmitCommand(ctx, sync.SubmitCommandParams{
		MachineID: machineID,
		Command: &v1.Command{
			Kind: &v1.Command_GetSettings{
				GetSettings: &v1.GetSettingsRequest{},
			},
		},
	})
	if !ok {
		se.Loading = false
		return
	}
	if reqID != "" {
		c.m.pendingCmdMeta[reqID] = cmdMeta{
			kind:      cmdKindGetSettings,
			machineID: machineID,
		}
	}
}

func (c *Core) reconcilePollFeed(ctx *sync.StepContext, machineID string, feed *screens.MachinePollFeed, modeChanged bool) []*corev1.Effect {
	if feed == nil {
		return nil
	}
	wasActive := feed.Polling
	nowActive := c.isPollActive(machineID, feed)

	if feed.Visible && !nowActive {
		mac := c.m.findMachine(machineID)
		if mac != nil && mac.Meta != nil && mac.Meta.Capabilities != nil && mac.Meta.Capabilities.MachineStatus {
			if feed.Last != nil {
				feed.State = corev1.MachinePollState_MACHINE_POLL_STATE_STALE
			} else {
				feed.State = corev1.MachinePollState_MACHINE_POLL_STATE_UNAVAILABLE
			}
		}
		c.m.settingsDirty.MachineStatus = true
		c.m.settingsDirty.MachineSettings = true
	}

	if nowActive != wasActive || (nowActive && modeChanged) {
		feed.Polling = nowActive
		feed.Generation++
		feed.Busy = false
		feed.ActiveReqID = ""
		effects := []*corev1.Effect{
			{
				Effect: &corev1.Effect_ScheduleMachinePoll{
					ScheduleMachinePoll: &corev1.ScheduleMachinePollEffect{
						MachineId:  machineID,
						Generation: feed.Generation - 1,
						Cancel:     true,
					},
				},
			},
		}
		if nowActive {
			effects = append(effects, c.startMachineStatusPoll(ctx, machineID, feed)...)
		} else {
			c.m.settingsDirty.MachineStatus = true
			c.m.settingsDirty.MachineSettings = true
		}
		return effects
	}
	return nil
}

func (c *Core) reconcileWatchedMachinePolls(effects *[]*corev1.Effect) {
	if c.m.settings == nil || len(c.m.settings.PollFeeds) == 0 {
		return
	}
	ids := make([]string, 0, len(c.m.settings.PollFeeds))
	for id, feed := range c.m.settings.PollFeeds {
		if feed != nil && (feed.Visible || feed.Polling) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return
	}
	sortutil.Strings(ids)
	prevStep, ctx := c.m.sync.BeginStep()
	for _, id := range ids {
		*effects = append(*effects, c.reconcilePollFeed(ctx, id, c.m.settings.PollFeeds[id], false)...)
	}
	res := c.m.sync.EndStep(prevStep, ctx)
	*effects = append(*effects, res.Effects...)
}

func (c *Core) dispatchSettingsUI(ctx *sync.StepContext, ev *corev1.Event) []*corev1.Effect {
	extra := c.m.ensureSettings()
	var effects []*corev1.Effect
	handled := true

	switch a := ev.Event.(type) {
	case *corev1.Event_MachineStatusWatch:
		if a.MachineStatusWatch == nil {
			return nil
		}
		w := a.MachineStatusWatch
		feed := extra.EnsureFeed(w.MachineId)
		prevMode := feed.Mode
		feed.Visible = w.Visible
		feed.Mode = w.Mode
		effects = append(effects, c.reconcilePollFeed(ctx, w.MachineId, feed, prevMode != w.Mode)...)
		if w.Visible {
			c.maybeHydrateUsageCache(w.MachineId, &effects)
			c.maybeFetchUsage(ctx, w.MachineId, v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS, nil)
		}

	case *corev1.Event_MachineStatusRetry:
		if a.MachineStatusRetry == nil {
			return nil
		}
		id := a.MachineStatusRetry.MachineId
		feed := extra.EnsureFeed(id)
		feed.Generation++
		feed.Busy = false
		feed.ActiveReqID = ""
		feed.Failures = 0
		effects = append(effects, &corev1.Effect{
			Effect: &corev1.Effect_ScheduleMachinePoll{
				ScheduleMachinePoll: &corev1.ScheduleMachinePollEffect{
					MachineId:  id,
					Generation: feed.Generation - 1,
					Cancel:     true,
				},
			},
		})
		effects = append(effects, c.startMachineStatusPoll(ctx, id, feed)...)

	case *corev1.Event_MachineStatusPollTick:
		if a.MachineStatusPollTick == nil {
			return nil
		}
		t := a.MachineStatusPollTick
		feed := extra.EnsureFeed(t.MachineId)
		if t.Generation != feed.Generation {
			return nil
		}
		if feed.Busy {
			// Timeout deadline elapsed before RPC settled.
			feed.Busy = false
			feed.ActiveReqID = ""
			if feed.Failures < machine.MaxFailures {
				feed.Failures++
			}
			if feed.Last == nil {
				feed.State = corev1.MachinePollState_MACHINE_POLL_STATE_UNAVAILABLE
			} else if feed.Failures >= machine.MaxFailures {
				feed.State = corev1.MachinePollState_MACHINE_POLL_STATE_STALE
			}
			c.m.settingsDirty.MachineStatus = true
			c.m.settingsDirty.MachineSettings = true
			if eff := c.scheduleNextPollIfActive(t.MachineId, feed); eff != nil {
				effects = append(effects, eff)
			}
			return effects
		}
		effects = append(effects, c.startMachineStatusPoll(ctx, t.MachineId, feed)...)

	case *corev1.Event_MachineStatusTab:
		if a.MachineStatusTab == nil {
			return nil
		}
		feed := extra.EnsureFeed(a.MachineStatusTab.MachineId)
		if feed.Tab != a.MachineStatusTab.Tab {
			feed.Tab = a.MachineStatusTab.Tab
			c.m.settingsDirty.MachineStatus = true
		}

	case *corev1.Event_MachineStatusScrub:
		if a.MachineStatusScrub == nil {
			return nil
		}
		s := a.MachineStatusScrub
		feed := extra.EnsureFeed(s.MachineId)
		switch s.Kind {
		case corev1.MachineChartKind_MACHINE_CHART_KIND_CPU:
			feed.HasScrubCPU = !s.Clear
			feed.ScrubCPU = s.PointIndex
		case corev1.MachineChartKind_MACHINE_CHART_KIND_MEMORY:
			feed.HasScrubMem = !s.Clear
			feed.ScrubMem = s.PointIndex
		}
		c.m.settingsDirty.MachineStatus = true

	case *corev1.Event_MachineStatusCleanup:
		if a.MachineStatusCleanup == nil {
			return nil
		}
		id := a.MachineStatusCleanup.MachineId
		mac := c.m.findMachine(id)
		var meta *v1.MachineMeta
		if mac != nil {
			meta = mac.Meta
		}
		cwd := machine.RecentProjectPath(meta)
		prompt := machine.BuildCleanupPrompt("", "")
		_ = prompt
		// Lookup canonical cleanup prompt literal or prepare effect.
		cleanupKey := text.KeyMachineStatusCleanupPrompt
		if cwd != "" {
			key := lists.ProjectKey(&lists.Session{MachineID: id, Info: &v1.Session{Cwd: cwd}})
			d := c.m.ensureDraft(key)
			if d != nil {
				d.Config.MachineID = id
				d.Config.Cwd = cwd
			}
			effects = append(effects, &corev1.Effect{
				Effect: &corev1.Effect_PrepareCleanupSession{
					PrepareCleanupSession: &corev1.PrepareCleanupSessionEffect{
						MachineId:            id,
						ProjectKey:           key,
						Cwd:                  cwd,
						Prompt:               cleanupKey,
						NeedsDirectoryPicker: false,
					},
				},
			})
		} else {
			extra.PendingCleanupMachineID = id
			effects = append(effects, &corev1.Effect{
				Effect: &corev1.Effect_PrepareCleanupSession{
					PrepareCleanupSession: &corev1.PrepareCleanupSessionEffect{
						MachineId:            id,
						Prompt:               cleanupKey,
						NeedsDirectoryPicker: true,
					},
				},
			})
		}

	case *corev1.Event_MachineSettingsOpen:
		if a.MachineSettingsOpen == nil {
			return nil
		}
		id := a.MachineSettingsOpen.MachineId
		me := extra.EnsureMachine(id)
		me.ToggleBanner = nil
		if mac := c.m.findMachine(id); mac != nil {
			installed := lists.MachineInstalledAgents(mac.Meta)
			for _, ag := range installed {
				c.m.maybeFetchMachineConfig(ctx, id, ag)
			}
			if len(installed) == 0 && me.SelectedAgent != v1.Agent_AGENT_UNSPECIFIED {
				c.m.maybeFetchMachineConfig(ctx, id, me.SelectedAgent)
			}
		} else {
			c.m.maybeFetchMachineConfig(ctx, id, me.SelectedAgent)
		}
		if se := c.m.state.MachineSettings[id]; se == nil || (!se.Loading && !se.Loaded) {
			c.submitGetSettings(ctx, id)
		}
		c.maybeHydrateUsageCache(id, &effects)
		c.maybeFetchUsage(ctx, id, v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS, nil)
		c.m.settingsDirty.MachineSettings = true

	case *corev1.Event_MachineSettingsSelectAgent:
		if a.MachineSettingsSelectAgent == nil {
			return nil
		}
		s := a.MachineSettingsSelectAgent
		me := extra.EnsureMachine(s.MachineId)
		me.SelectedAgent = s.Agent
		c.m.maybeFetchMachineConfig(ctx, s.MachineId, s.Agent)
		c.m.settingsDirty.MachineSettings = true

	case *corev1.Event_UpdateMachineAgentDefaults:
		if a.UpdateMachineAgentDefaults == nil {
			return nil
		}
		u := a.UpdateMachineAgentDefaults
		se := c.m.state.MachineSettings[u.MachineId]
		var cur *v1.DaemonSettings
		if se != nil && se.Settings != nil {
			cur = se.Settings.Clone()
		} else {
			cur = &v1.DaemonSettings{}
		}
		effort := u.Effort
		if u.Model != nil && effort == nil {
			empty := ""
			effort = &empty
		}
		next := lists.WithDefaults(cur, u.Agent, u.Model, effort, u.Mode)
		c.dispatchUpdateSettings(ctx, u.MachineId, next, false)

	case *corev1.Event_UpdateMachineDaemonSettings:
		if a.UpdateMachineDaemonSettings == nil {
			return nil
		}
		u := a.UpdateMachineDaemonSettings
		se := c.m.state.MachineSettings[u.MachineId]
		var next *v1.DaemonSettings
		if se != nil && se.Settings != nil {
			next = se.Settings.Clone()
		} else {
			next = &v1.DaemonSettings{}
		}
		if u.ApprovalTimeoutSeconds != nil {
			next.ApprovalTimeoutSeconds = *u.ApprovalTimeoutSeconds
		}
		if u.IdleCloseMinutes != nil {
			next.IdleCloseMinutes = *u.IdleCloseMinutes
		}
		if u.ImageUploadQuotaBytes != nil {
			next.ImageUploadQuotaBytes = *u.ImageUploadQuotaBytes
		}
		c.dispatchUpdateSettings(ctx, u.MachineId, next, u.CleanImages)

	case *corev1.Event_ToggleMachineAgent:
		if a.ToggleMachineAgent == nil {
			return nil
		}
		t := a.ToggleMachineAgent
		id := t.MachineId
		mac := c.m.findMachine(id)
		if mac == nil || mac.Meta == nil || mac.Meta.Capabilities == nil || !mac.Meta.Capabilities.CliSwitches {
			return nil
		}
		if !lists.MachineReachable(mac) || lists.WorldUnlinked(&c.m.state.World) || c.m.sync.LeaseExpired() {
			return nil
		}
		agent := lists.EffectiveAgent(t.Agent)
		me := extra.EnsureMachine(id)
		disabled := lists.MachineDisabledSet(mac.Meta)
		if disabled == nil {
			disabled = make(map[v1.Agent]bool)
		}
		for ag, en := range me.TogglePendingValue {
			if me.ToggleSaving[ag] {
				disabled[ag] = !en
			}
		}
		if !t.Enabled {
			enabledAfter := 0
			for _, inst := range lists.MachineInstalledAgents(mac.Meta) {
				isDisabled := disabled[inst]
				if inst == agent {
					isDisabled = true
				}
				if !isDisabled {
					enabledAfter++
				}
			}
			if enabledAfter == 0 {
				return nil
			}
		}
		if me.ToggleSaving == nil {
			me.ToggleSaving = make(map[v1.Agent]bool)
		}
		if me.TogglePendingValue == nil {
			me.TogglePendingValue = make(map[v1.Agent]bool)
		}
		me.ToggleSaving[agent] = true
		me.TogglePendingValue[agent] = t.Enabled
		me.ToggleBanner = nil

		enabledVal := t.Enabled
		reqID, ok := c.m.sync.SubmitCommand(ctx, sync.SubmitCommandParams{
			MachineID: id,
			Command: &v1.Command{
				Kind: &v1.Command_UpdateSettings{
					UpdateSettings: &v1.UpdateSettingsRequest{
						ToggleAgent:   &agent,
						ToggleEnabled: &enabledVal,
					},
				},
			},
		})
		if !ok {
			delete(me.ToggleSaving, agent)
			delete(me.TogglePendingValue, agent)
			me.ToggleBanner = text.Msg(text.KeySettingsCliSwitchesUpdateError)
		} else if reqID != "" {
			c.m.pendingCmdMeta[reqID] = cmdMeta{
				kind:          cmdKindToggleMachineAgent,
				machineID:     id,
				agent:         agent,
				toggleEnabled: enabledVal,
			}
		}
		c.m.settingsDirty.MachineSettings = true

	case *corev1.Event_UpdateDirectPreference:
		if a.UpdateDirectPreference == nil {
			return nil
		}
		u := a.UpdateDirectPreference
		c.m.sync.SetDirectEnabled(ctx, u.MachineId, u.Enabled)
		c.m.settingsDirty.MachineSettings = true
		c.m.settingsDirty.Settings = true
		c.m.settingsDirty.DirectDiagnostics = true

	case *corev1.Event_UpdateClientName:
		if a.UpdateClientName == nil {
			return nil
		}
		u := a.UpdateClientName
		name := strings.TrimSpace(u.Name)
		if u.RestoreDefault {
			name = ""
		}
		if runes := []rune(name); len(runes) > int(machine.MaxClientNameLength) {
			name = string(runes[:machine.MaxClientNameLength])
		}
		extra.RenameError = nil
		extra.RenameSaving = false
		c.m.settingsDirty.ClientDetail = true
		effects = append(effects, &corev1.Effect{
			Effect: &corev1.Effect_RenameClient{
				RenameClient: &corev1.RenameClientEffect{
					DeviceId: u.DeviceId,
					Name:     name,
				},
			},
		})

	case *corev1.Event_SetNotificationPref:
		if a.SetNotificationPref == nil {
			return nil
		}
		p := a.SetNotificationPref
		if p.Supported != nil {
			extra.Notifications.Supported = *p.Supported
		}
		if p.Permission != nil {
			extra.Notifications.Permission = *p.Permission
		}
		if p.PushState != nil {
			extra.Notifications.PushState = *p.PushState
		}
		if p.NotifyAttention != nil {
			extra.Notifications.NotifyAttention = *p.NotifyAttention
		}
		if p.NotifyTurnEnded != nil {
			extra.Notifications.NotifyTurnEnded = *p.NotifyTurnEnded
		}
		if p.NotifyScheduled != nil {
			extra.Notifications.NotifyScheduled = *p.NotifyScheduled
		}
		if p.ShowContent != nil {
			extra.Notifications.ShowContent = *p.ShowContent
		}
		if p.ErrorDetail != nil {
			extra.Notifications.ErrorDetail = *p.ErrorDetail
		}
		c.m.settingsDirty.Settings = true
		effects = append(effects, c.emitPersistPrefs())

	case *corev1.Event_SetAppearancePref:
		if a.SetAppearancePref == nil {
			return nil
		}
		extra.Theme = a.SetAppearancePref.Theme
		c.m.settingsDirty.Settings = true
		effects = append(effects, c.emitPersistPrefs())

	case *corev1.Event_ConnCheckStatus:
		if a.ConnCheckStatus == nil {
			return nil
		}
		cc := a.ConnCheckStatus
		extra.ConnCheck.ServerState = cc.ServerState
		extra.ConnCheck.LatencyMs = cc.LatencyMs
		extra.ConnCheck.LastFailure = cc.LastFailure
		c.m.settingsDirty.Settings = true

	case *corev1.Event_ClientUpdateState:
		if a.ClientUpdateState == nil {
			return nil
		}
		u := a.ClientUpdateState
		extra.Update.CurrentVersion = u.CurrentVersion
		extra.Update.ProtocolVersion = u.ProtocolVersion
		extra.Update.Channel = u.Channel
		extra.Update.Checking = u.Checking
		extra.Update.AvailableVersion = u.AvailableVersion
		extra.Update.DownloadedVersion = u.DownloadedVersion
		extra.Update.DownloadProgressPercent = u.DownloadProgressPercent
		extra.Update.ErrorMessage = u.ErrorMessage
		extra.Update.Releases = append([]*corev1.ReleaseCandidateInfo(nil), u.Releases...)
		c.m.settingsDirty.Settings = true

	case *corev1.Event_CheckForUpdates:
		extra.Update.Checking = true
		extra.Update.ErrorMessage = ""
		c.m.settingsDirty.Settings = true
		effects = append(effects, &corev1.Effect{
			Effect: &corev1.Effect_CheckUpdates{
				CheckUpdates: &corev1.CheckUpdatesEffect{},
			},
		})
	default:
		handled = false
	}

	if handled {
		extra.Active = true
	}
	return effects
}

func (c *Core) dispatchUpdateSettings(ctx *sync.StepContext, machineID string, next *v1.DaemonSettings, cleanImages bool) {
	extra := c.m.ensureSettings()
	me := extra.EnsureMachine(machineID)
	me.SavePending = true
	me.SaveError = nil
	if cleanImages {
		me.CleaningImages = true
	}
	reqID, ok := c.m.sync.SubmitCommand(ctx, sync.SubmitCommandParams{
		MachineID: machineID,
		Command: &v1.Command{
			Kind: &v1.Command_UpdateSettings{
				UpdateSettings: &v1.UpdateSettingsRequest{
					Settings:    next,
					CleanImages: cleanImages,
				},
			},
		},
	})
	if !ok {
		me.SavePending = false
		me.CleaningImages = false
		me.SaveError = text.Msg(text.KeySettingsSaveError, text.Str("reason", "offline"))
		c.m.settingsDirty.MachineSettings = true
		return
	}
	if reqID != "" {
		c.m.pendingCmdMeta[reqID] = cmdMeta{
			kind:      cmdKindUpdateSettings,
			machineID: machineID,
		}
	}
	c.m.settingsDirty.MachineSettings = true
}

func (c *Core) emitPersistPrefs() *corev1.Effect {
	extra := c.m.ensureSettings()
	return &corev1.Effect{
		Effect: &corev1.Effect_PersistClientPrefs{
			PersistClientPrefs: &corev1.PersistClientPrefsEffect{
				NotifyAttention: extra.Notifications.NotifyAttention,
				NotifyTurnEnded: extra.Notifications.NotifyTurnEnded,
				NotifyScheduled: extra.Notifications.NotifyScheduled,
				ShowContent:     extra.Notifications.ShowContent,
				Theme:           extra.Theme,
			},
		},
	}
}

func (c *Core) onToggleMachineAgentSettled(sc sync.SettledCommand, meta cmdMeta, dirty *sync.DirtyFlags) []*corev1.Effect {
	extra := c.m.ensureSettings()
	me := extra.EnsureMachine(meta.machineID)
	delete(me.ToggleSaving, meta.agent)
	delete(me.TogglePendingValue, meta.agent)

	var out []*corev1.Effect
	if sc.Category == outcome.Executed && sc.ErrorCode == "" && sc.Result != nil && sc.Result.GetUpdateSettings() != nil {
		resp := sc.Result.GetUpdateSettings()
		if st := resp.GetSettings(); st != nil {
			se := c.m.state.MachineSettings[meta.machineID]
			if se == nil {
				se = &screens.SettingsCacheEntry{}
				c.m.state.MachineSettings[meta.machineID] = se
			}
			se.Settings = st.Clone()
			se.Loaded = true
			se.Error = ""
			se.ErrorText = nil
			me.ImageStorage = resp.GetImageStorage().Clone()
			c.m.sync.ApplyMachineSettingsSnapshot(meta.machineID, st)
		}
		me.ToggleBanner = nil

		if !meta.toggleEnabled && me.SelectedAgent == meta.agent {
			if mac := c.m.findMachine(meta.machineID); mac != nil {
				enabled := lists.MachineAgents(mac.Meta)
				if len(enabled) > 0 && enabled[0] != meta.agent {
					me.SelectedAgent = enabled[0]
					fetchPrev, fetchCtx := c.m.sync.BeginStep()
					c.m.maybeFetchMachineConfig(fetchCtx, meta.machineID, enabled[0])
					fetchRes := c.m.sync.EndStep(fetchPrev, fetchCtx)
					out = append(out, fetchRes.Effects...)
				}
			}
		}

		c.m.settingsDirty.markAll()
		dirty.Add(true, true, true)
		c.m.markAllScheduleScreensDirty()
		c.m.usageDirty = true
		return out
	}

	isConflict := sc.ErrorCode == "cannot_disable_last_agent" ||
		sc.ErrorCode == "failed_precondition" ||
		sc.ErrorCode == "settings_revision_conflict" ||
		sc.Category == outcome.Conflict ||
		sc.Category == outcome.Unknown
	if isConflict {
		me.ToggleBanner = text.Msg(text.KeySettingsCliSwitchesConflict)
		prevStep, ctx := c.m.sync.BeginStep()
		c.submitGetSettings(ctx, meta.machineID)
		res := c.m.sync.EndStep(prevStep, ctx)
		out = append(out, res.Effects...)
	} else {
		me.ToggleBanner = text.Msg(text.KeySettingsCliSwitchesUpdateError)
	}
	c.m.settingsDirty.MachineSettings = true
	return out
}
