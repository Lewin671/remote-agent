// Package app hosts the shared Core state machine and Dispatch/View entrypoints
// exposed to Web, Android, and iOS shells.
package app

import (
	"errors"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/outcome"
	"github.com/Lewin671/remote-agent/clientcore/domain/usage"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/screens"
	"github.com/Lewin671/remote-agent/clientcore/sync"
)

// Core wraps the unified sync Engine and screen ViewModel builders behind a single Dispatch/View API.
type Core struct {
	m *Model
}

// New constructs a Core instance with fresh sync state and screen caches.
func New() *Core {
	c := &Core{m: newModel()}
	c.m.state.DraftViewFn = func(scope *corev1.DraftScope) *corev1.AttachmentDraftView {
		return c.buildAttachmentDraftView(scope, false)
	}
	return c
}

// Close releases references held by Core.
func (c *Core) Close() {}

// View renders the requested screen from the current synchronized state.
func (c *Core) View(screen *corev1.Screen) (*corev1.ViewModel, error) {
	if screen == nil {
		return nil, errors.New("app: nil screen request")
	}
	c.m.decodeScopeIfReady()

	switch s := screen.Screen.(type) {
	case *corev1.Screen_Home:
		var endedLimit int
		if s.Home != nil {
			endedLimit = int(s.Home.EndedLimit)
		}
		return &corev1.ViewModel{
			View: &corev1.ViewModel_Home{
				Home: screens.BuildHome(c.m.state, endedLimit),
			},
		}, nil

	case *corev1.Screen_Project:
		var key string
		var manage bool
		var endedLimit int
		var machineID string
		var pickerKey string
		if s.Project != nil {
			key = s.Project.ProjectKey
			manage = s.Project.Manage
			endedLimit = int(s.Project.EndedLimit)
			machineID = s.Project.MachineId
			pickerKey = s.Project.PickerKey
		}
		return &corev1.ViewModel{
			View: &corev1.ViewModel_Project{
				Project: screens.BuildProject(c.m.state, key, manage, endedLimit, machineID, pickerKey),
			},
		}, nil

	case *corev1.Screen_NewSession:
		var key string
		if s.NewSession != nil {
			key = s.NewSession.ProjectKey
		}
		if key == "" {
			key = c.m.state.StandaloneProjectKey
		}
		return &corev1.ViewModel{
			View: &corev1.ViewModel_NewSession{
				NewSession: screens.BuildNewSession(c.m.state, key),
			},
		}, nil

	case *corev1.Screen_SyncState:
		return &corev1.ViewModel{
			View: &corev1.ViewModel_SyncState{
				SyncState: c.m.sync.BuildSyncStateView(),
			},
		}, nil

	case *corev1.Screen_SessionData:
		var sid string
		if s.SessionData != nil {
			sid = s.SessionData.SessionId
		}
		return &corev1.ViewModel{
			View: &corev1.ViewModel_SessionData{
				SessionData: c.m.sync.BuildSessionDataView(sid),
			},
		}, nil

	case *corev1.Screen_Session:
		return c.viewSession(s.Session)

	case *corev1.Screen_Usage:
		return c.viewUsage(s.Usage)

	case *corev1.Screen_AttachmentEval:
		return &corev1.ViewModel{
			View: &corev1.ViewModel_AttachmentEval{
				AttachmentEval: screens.BuildAttachmentEval(c.m.state, s.AttachmentEval),
			},
		}, nil

	case *corev1.Screen_AttachmentDraft:
		return c.viewAttachmentDraft(s.AttachmentDraft)

	default:
		if vm, ok, err := c.viewSettings(screen); ok || err != nil {
			return vm, err
		}
		if vm, handled, err := c.viewSchedules(screen); handled {
			return vm, err
		}
		return nil, errors.New("app: unsupported screen")
	}
}

// Dispatch advances sync and UI state for ev and returns ordered side effects for the shell.
func (c *Core) Dispatch(ev *corev1.Event) *corev1.EffectBatch {
	if ev == nil {
		return &corev1.EffectBatch{}
	}

	consumed, syncRes := c.m.sync.HandleEvent(ev)
	c.m.adoptHomeScopeToken()
	if consumed {
		var hookEffects []*corev1.Effect
		c.onSyncEventConsumed(ev, &hookEffects)
		extra, extraDirty := c.processSyncStep(syncRes)
		if extraDirty.Home || extraDirty.Project || extraDirty.NewSession {
			syncRes.Dirty.Add(extraDirty.Home, extraDirty.Project, extraDirty.NewSession)
		}
		if extraDirty.SessionData {
			syncRes.Dirty.MarkSessionData()
		}
		effects := append(syncRes.Effects, hookEffects...)
		effects = append(effects, extra...)
		if r := renderEffect(syncRes.Dirty); r != nil {
			effects = append(effects, r)
		}
		effects = c.appendSettingsRender(effects, syncRes.Dirty)
		effects = c.appendUsageRender(effects, syncRes.Dirty)
		effects = c.appendSessionRenderTarget(syncRes.Dirty, effects)
		effects = c.appendAttachmentDraftRenderTarget(effects)
		effects = c.m.mergeScheduleRender(effects)
		return &corev1.EffectBatch{Effects: effects}
	}

	stepPrev, stepCtx := c.m.sync.BeginStep()
	uiEffects := c.dispatchUI(stepCtx, ev)
	stepRes := c.m.sync.EndStep(stepPrev, stepCtx)
	extra, extraDirty := c.processSyncStep(stepRes)
	stepRes.Dirty.Add(extraDirty.Home, extraDirty.Project, extraDirty.NewSession)
	if extraDirty.SessionData {
		stepRes.Dirty.MarkSessionData()
	}

	effects := make([]*corev1.Effect, 0, len(stepRes.Effects)+len(uiEffects)+len(extra)+1)
	effects = append(effects, stepRes.Effects...)
	effects = append(effects, uiEffects...)
	effects = append(effects, extra...)
	if r := renderEffect(stepRes.Dirty); r != nil {
		effects = append(effects, r)
	}
	effects = c.appendSettingsRender(effects, stepRes.Dirty)
	effects = c.appendUsageRender(effects, stepRes.Dirty)
	effects = c.appendSessionRenderTarget(stepRes.Dirty, effects)
	effects = c.appendAttachmentDraftRenderTarget(effects)
	effects = c.m.mergeScheduleRender(effects)
	return &corev1.EffectBatch{Effects: effects}
}

func (c *Core) processSyncStep(res sync.StepResult) ([]*corev1.Effect, sync.DirtyFlags) {
	c.m.decodeScopeIfReady()
	if c.m.sessions != nil {
		for sid, ui := range c.m.sessions.uiBySession {
			if c.m.sync.Buffer(sid) == nil {
				ui.ClearTimelineCache()
			}
		}
	}

	var effects []*corev1.Effect
	var dirty sync.DirtyFlags
	for _, sc := range res.Settled {
		if c.settlePlanPreflight(sc, &effects, &dirty) {
			continue
		}
		if c.settleSharedFileChunk(sc, &effects, &dirty) {
			continue
		}
		if c.settleAttachmentUpload(sc, &effects, &dirty) {
			continue
		}
		c.processSessionSettled(sc, &effects, &dirty)
		meta, hasMeta := c.m.pendingCmdMeta[sc.RequestID]
		if hasMeta {
			if isScheduleCmdKind(meta.kind) {
				continue
			}
			delete(c.m.pendingCmdMeta, sc.RequestID)
		} else if sc.SlotKey == "" {
			continue
		}

		kind := meta.kind
		if !hasMeta && len(sc.SlotKey) > len("create_session:") && sc.SlotKey[:len("create_session:")] == "create_session:" {
			kind = cmdKindCreateSession
			meta.projectKey = sc.SlotKey[len("create_session:"):]
		}

		switch kind {
		case cmdKindListModels:
			k := screens.MachineAgentKey{MachineID: meta.machineID, Agent: meta.agent}
			me := c.m.state.MachineModels[k]
			if me == nil {
				me = &screens.ModelsCacheEntry{}
				c.m.state.MachineModels[k] = me
			}
			me.Loading = false
			if sc.Category == outcome.Executed && sc.Result != nil && sc.Result.GetListModels() != nil {
				me.Response = sc.Result.GetListModels().Clone()
				me.Loaded = true
				me.Error = ""
				me.ErrorText = nil
			} else {
				me.ErrorText = sc.ErrorText
				me.Error = sc.ErrorCode
				if me.Error == "" && me.ErrorText == nil {
					me.Error = "failed"
				}
			}
			dirty.Add(false, false, true)
			if c.m.settings != nil && c.m.settings.Active {
				c.m.settingsDirty.MachineSettings = true
			}

		case cmdKindGetSettings:
			se := c.m.state.MachineSettings[meta.machineID]
			if se == nil {
				se = &screens.SettingsCacheEntry{}
				c.m.state.MachineSettings[meta.machineID] = se
			}
			se.Loading = false
			if sc.Category == outcome.Executed && sc.Result != nil && sc.Result.GetGetSettings() != nil {
				se.Settings = sc.Result.GetGetSettings().GetSettings().Clone()
				se.Loaded = true
				se.Error = ""
				se.ErrorText = nil
				if c.m.sync.ApplyMachineSettingsSnapshot(meta.machineID, se.Settings) {
					dirty.Add(true, true, true)
					c.m.markAllScheduleScreensDirty()
					c.m.usageDirty = true
					if c.m.settings != nil {
						c.m.settingsDirty.markAll()
					}
				}
			} else {
				se.ErrorText = sc.ErrorText
				se.Error = sc.ErrorCode
				if se.Error == "" && se.ErrorText == nil {
					se.Error = "failed"
				}
			}
			effects = append(effects, c.onGetSettingsSettled(sc, meta)...)
			dirty.Add(false, false, true)
			if c.m.settings != nil && c.m.settings.Active {
				c.m.settingsDirty.MachineSettings = true
			}

		case cmdKindToggleMachineAgent:
			effects = append(effects, c.onToggleMachineAgentSettled(sc, meta, &dirty)...)

		case cmdKindGetUsageStats:
			effects = append(effects, c.onUsageStatsSettled(sc, meta, &dirty)...)

		case cmdKindMachineOneShot:
			delete(c.m.oneShotReqByOp, meta.opID)
			ok := sc.Category == outcome.Executed && sc.ErrorCode == ""
			errCode := sc.ErrorCode
			if !ok && errCode == "" {
				errCode = "failed"
			}
			effects = append(effects, &corev1.Effect{
				Effect: &corev1.Effect_RunMachineCommandResult{
					RunMachineCommandResult: &corev1.RunMachineCommandResultEffect{
						OpId:      meta.opID,
						Ok:        ok,
						ErrorCode: errCode,
						ErrorText: sc.ErrorText,
					},
				},
			})

		case cmdKindListDirs:
			if meta.pickerKey == "" {
				continue
			}
			var dp *screens.DirPickerState
			if c.m.state.DirPickers != nil {
				dp = c.m.state.DirPickers[meta.pickerKey]
			}
			if dp == nil || !dp.Open {
				continue
			}
			if dp.ActiveRequestID != "" && sc.RequestID != "" && dp.ActiveRequestID != sc.RequestID {
				continue
			}
			dp.Loading = false
			dp.ActiveRequestID = ""
			if sc.Category == outcome.Executed && sc.Result != nil && sc.Result.GetListDirectories() != nil {
				lr := sc.Result.GetListDirectories()
				if lr.Path != "" {
					dp.CurrentPath = lr.Path
				}
				dp.ParentPath = lr.Parent
				dp.Dirs = dirEntriesFromNames(lr.Dirs, dp.CurrentPath)
				dp.Error = ""
				dp.ErrorText = nil
			} else {
				dp.ErrorText = sc.ErrorText
				dp.Error = sc.ErrorCode
				if dp.Error == "" && dp.ErrorText == nil {
					dp.Error = "failed"
				}
			}
			dirty.Add(false, true, true)

		case cmdKindCreateSession:
			d := c.m.ensureDraft(meta.projectKey)
			if d == nil {
				continue
			}
			d.Busy = false
			if sc.Category == outcome.Executed && sc.ErrorCode == "" && sc.Result != nil && sc.Result.GetCreateSession() != nil {
				sess := sc.Result.GetCreateSession().GetSession()
				sessionID := ""
				if sess != nil {
					sessionID = sess.Id
				}
				d.Frozen = false
				d.Expired = false
				d.UnknownStart = false
				d.StartError = ""
				d.StartErrorText = nil
				d.DiskFull = false
				d.ActiveStartRequestID = ""
				hasCoreStart := c.m.attachments != nil && c.m.attachments.configured && c.m.attachments.newSessionStarts[meta.projectKey] != nil
				if !hasCoreStart {
					d.Prompt = ""
					d.AttachmentCount = 0
					d.AttachmentsMoved = false
				}
				d.ResetOnNextOpen = true

				if c.m.attachments == nil || !c.m.attachments.configured {
					effects = append(effects, &corev1.Effect{
						Effect: &corev1.Effect_AttachmentsSettled{
							AttachmentsSettled: &corev1.AttachmentsSettledEffect{
								ProjectKey: meta.projectKey,
								RequestId:  sc.RequestID,
								Consumed:   true,
							},
						},
					})
				}
				savePrev, saveCtx := c.m.sync.BeginStep()
				if sess != nil && sess.Id != "" {
					c.m.sync.SeedSession(saveCtx, sc.MachineID, sess)
				}
				cfg := d.Config
				if cfg.Cwd == "" {
					cfg.Cwd = meta.projectKey
				}
				saved := lists.SavedSettings(c.m.state.World.Prefs, cfg.Cwd)
				if op := lists.PlanStartSettings(cfg, &cfg, saved); op != nil {
					c.m.sync.EnqueuePrefsOp(saveCtx, op, true, meta.projectKey)
				}
				saveRes := c.m.sync.EndStep(savePrev, saveCtx)
				effects = append(effects, saveRes.Effects...)
				if sessionID != "" {
					effects = append(effects, &corev1.Effect{
						Effect: &corev1.Effect_Navigate{
							Navigate: &corev1.NavigateEffect{
								Route:      corev1.NavigateRoute_NAVIGATE_ROUTE_SESSION,
								ProjectKey: meta.projectKey,
								SessionId:  sessionID,
								MachineId:  sc.MachineID,
							},
						},
					})
				}
			} else if sc.Category == outcome.Unknown {
				d.Frozen = true
				d.Expired = sc.Expired
				d.UnknownStart = true
				d.DiskFull = false
				d.StartErrorText = sc.ErrorText
				d.StartError = sc.ErrorCode
				if sc.Expired && (c.m.attachments == nil || !c.m.attachments.configured) {
					effects = append(effects, &corev1.Effect{
						Effect: &corev1.Effect_AttachmentsSettled{
							AttachmentsSettled: &corev1.AttachmentsSettledEffect{
								ProjectKey: meta.projectKey,
								RequestId:  sc.RequestID,
								Consumed:   false,
							},
						},
					})
				}
			} else {
				d.Frozen = false
				d.Expired = false
				d.UnknownStart = false
				d.ActiveStartRequestID = ""
				d.DiskFull = sc.ErrorCode == "disk_full"
				d.StartErrorText = sc.ErrorText
				d.StartError = sc.ErrorCode
				if d.StartError == "" && d.StartErrorText == nil {
					d.StartError = "failed"
				}
				if c.m.attachments == nil || !c.m.attachments.configured {
					effects = append(effects, &corev1.Effect{
						Effect: &corev1.Effect_AttachmentsSettled{
							AttachmentsSettled: &corev1.AttachmentsSettledEffect{
								ProjectKey: meta.projectKey,
								RequestId:  sc.RequestID,
								Consumed:   false,
							},
						},
					})
				}
			}
			c.onNewSessionCreateSettled(meta.projectKey, sc, &effects)
			dirty.Add(true, true, true)
		default:
			effects = append(effects, c.onOtherSettingsCmdSettled(sc, meta)...)
		}
	}

	if res.Dirty.NewSession || dirty.NewSession {
		extraPrev, extraCtx := c.m.sync.BeginStep()
		for key, d := range c.m.state.NewSessions {
			if d == nil {
				continue
			}
			psr := c.m.resolveDraftConfig(key, d)
			c.m.syncDraftAutoSupport(key, psr)
			if d.Config.MachineID != "" {
				c.m.maybeFetchMachineConfig(extraCtx, d.Config.MachineID, d.Config.Agent)
			}
		}
		extraRes := c.m.sync.EndStep(extraPrev, extraCtx)
		effects = append(effects, extraRes.Effects...)
	}

	if res.Dirty.Home || res.Dirty.SyncState {
		c.reconcileWatchedMachinePolls(&effects)
		if c.m.usageActive {
			c.m.usageDirty = true
		}
		if !(res.Dirty.NewSession || dirty.NewSession) {
			extraPrev, extraCtx := c.m.sync.BeginStep()
			for key, d := range c.m.state.NewSessions {
				if d == nil || !d.ConfigInitialized || d.Busy || d.Frozen || d.Config.MachineID == "" {
					continue
				}
				if mac := c.m.findMachine(d.Config.MachineID); mac != nil && lists.IsAgentDisabled(mac.Meta, d.Config.Agent) {
					psr := c.m.resolveDraftConfig(key, d)
					c.m.syncDraftAutoSupport(key, psr)
					c.m.maybeFetchMachineConfig(extraCtx, d.Config.MachineID, d.Config.Agent)
					dirty.Add(false, false, true)
				}
			}
			extraRes := c.m.sync.EndStep(extraPrev, extraCtx)
			effects = append(effects, extraRes.Effects...)
		}
	}

	c.processSchedulesSyncStep(res, &effects)
	c.reconcileSharedFileDownloads(&effects, &dirty)
	c.reconcileAttachments(&effects, &dirty)

	return effects, dirty
}

func (c *Core) dispatchUI(ctx *sync.StepContext, ev *corev1.Event) []*corev1.Effect {
	var effects []*corev1.Effect
	st := c.m.state

	switch k := ev.Event.(type) {
	case *corev1.Event_LocaleChanged:
		if k.LocaleChanged == nil {
			return nil
		}
		prev := usage.NormalizeUILocale(st.UILocale)
		next := usage.NormalizeUILocale(k.LocaleChanged.UiLocale)
		st.UILocale = next
		if next == prev {
			return nil
		}
		c.m.usageDirty = true
		c.m.settingsDirty.MachineStatus = true
		c.m.settingsDirty.MachineSettings = true
		c.m.ensureSessionStore().pendingDirty = true

	case *corev1.Event_HomeSetScope:
		if k.HomeSetScope == nil {
			return nil
		}
		projects := lists.BuildProjects(&st.World, "", nil, "", nil, nil)
		target := lists.ResolveScope(&lists.Scope{
			AllProjects: k.HomeSetScope.ScopeKey == "",
			Key:         k.HomeSetScope.ScopeKey,
		}, projects.Listed)
		st.Scope = &target
		st.RememberedScope = &target
		tok := lists.EncodeScope(target, c.m.sync.ScopeHMACKey())
		if tok != "" {
			st.RememberedScopeToken = tok
			effects = append(effects, &corev1.Effect{
				Effect: &corev1.Effect_SaveHomeScope{
					SaveHomeScope: &corev1.SaveHomeScopeEffect{
						ScopeKey:   target.Key,
						ScopeToken: tok,
					},
				},
			})
		}
		ctx.Dirty.Add(true, false, false)

	case *corev1.Event_HomeSetFilter:
		if k.HomeSetFilter == nil {
			return nil
		}
		st.HomeFilterQuery = k.HomeSetFilter.Query
		if st.HomeFilterQuery != "" {
			c.m.sync.RequestAllSessions(ctx)
		} else {
			c.m.sync.ClearWantAllSessions()
		}
		ctx.Dirty.Add(true, false, false)

	case *corev1.Event_HomeLoadMoreEnded:
		c.m.sync.RequestMoreSessions(ctx)

	case *corev1.Event_HomeRetryLoad:
		c.m.sync.RequestMoreSessions(ctx)

	case *corev1.Event_HomeSelectSession:
		if k.HomeSelectSession != nil {
			st.HomeSelectedSessionID = k.HomeSelectSession.SessionId
			ctx.Dirty.Add(true, false, false)
		}

	case *corev1.Event_ProjectSetSearch:
		if k.ProjectSetSearch != nil {
			st.ProjectSearch = k.ProjectSetSearch.Query
			ctx.Dirty.Add(false, true, false)
		}

	case *corev1.Event_ProjectTogglePin:
		if k.ProjectTogglePin != nil && k.ProjectTogglePin.ProjectKey != "" {
			key := k.ProjectTogglePin.ProjectKey
			kind := lists.PrefsOpPin
			if lists.IsPinned(st.World.Prefs, key) {
				kind = lists.PrefsOpUnpin
			}
			c.m.sync.EnqueuePrefsOp(ctx, &lists.FullPrefsOp{Kind: kind, Path: key}, false, "")
			ctx.Dirty.Add(true, true, false)
		}

	case *corev1.Event_ProjectToggleHide:
		if k.ProjectToggleHide != nil && k.ProjectToggleHide.ProjectKey != "" {
			key := k.ProjectToggleHide.ProjectKey
			kind := lists.PrefsOpHide
			if lists.IsHidden(st.World.Prefs, key) {
				kind = lists.PrefsOpUnhide
			}
			c.m.sync.EnqueuePrefsOp(ctx, &lists.FullPrefsOp{Kind: kind, Path: key}, false, "")
			ctx.Dirty.Add(true, true, false)
		}

	case *corev1.Event_ProjectAdd:
		if k.ProjectAdd != nil && k.ProjectAdd.Path != "" {
			var machID string
			var forNewTask bool
			if pickerKey := k.ProjectAdd.PickerKey; pickerKey != "" && st.DirPickers != nil {
				if dp := st.DirPickers[pickerKey]; dp != nil {
					machID = dp.MachineID
					forNewTask = dp.ForNewTask
					dp.Open = false
					dp.ForNewTask = false
					dp.Loading = false
					dp.Error = ""
					dp.ErrorText = nil
					dp.ActiveRequestID = ""
				}
			}
			if lists.IsHidden(st.World.Prefs, k.ProjectAdd.Path) {
				c.m.sync.EnqueuePrefsOp(ctx, &lists.FullPrefsOp{Kind: lists.PrefsOpUnhide, Path: k.ProjectAdd.Path}, false, "")
			}
			if op := lists.PlanAddProject(&st.World, machID, k.ProjectAdd.Path); op != nil && op.Kind != lists.PrefsOpUnhide {
				c.m.sync.EnqueuePrefsOp(ctx, op, false, "")
			}
			if forNewTask {
				d := c.m.ensureDraft(k.ProjectAdd.Path)
				d.MachineChosen = machID != ""
				d.ConfigChosen = false
				d.ResetOnNextOpen = false
				d.DiskFull = false
				d.AgentOverride = v1.Agent_AGENT_UNSPECIFIED
				res := lists.ResolveProjectStart(&st.World, k.ProjectAdd.Path, machID, 0, nil, false)
				if res.Found {
					d.Config = res.Config
					d.Config.Agent = lists.EffectiveAgent(d.Config.Agent)
					d.ConfigInitialized = true
				} else {
					d.Config = lists.StartConfig{
						MachineID: machID,
						Cwd:       k.ProjectAdd.Path,
						Agent:     lists.EffectiveAgent(0),
					}
					d.ConfigInitialized = machID != ""
				}
				c.m.syncDraftAutoSupport(k.ProjectAdd.Path, res)
				if d.Config.MachineID != "" {
					c.m.maybeFetchMachineConfig(ctx, d.Config.MachineID, d.Config.Agent)
				}
				effects = append(effects, &corev1.Effect{
					Effect: &corev1.Effect_Navigate{
						Navigate: &corev1.NavigateEffect{
							Route:      corev1.NavigateRoute_NAVIGATE_ROUTE_PROJECT,
							ProjectKey: k.ProjectAdd.Path,
							MachineId:  machID,
						},
					},
				})
			}
			ctx.Dirty.Add(true, true, true)
		}

	case *corev1.Event_ProjectLoadMoreEnded:
		c.m.sync.RequestMoreSessions(ctx)

	case *corev1.Event_ProjectOpenDirPicker:
		if k.ProjectOpenDirPicker != nil && k.ProjectOpenDirPicker.PickerKey != "" {
			pickerKey := k.ProjectOpenDirPicker.PickerKey
			dp := st.EnsureDirPicker(pickerKey)
			machID := k.ProjectOpenDirPicker.MachineId
			if machID == "" {
				for _, m := range st.World.Machines {
					if m != nil && lists.MachineReachable(m) && (machID == "" || m.ID < machID) {
						machID = m.ID
					}
				}
			}
			dp.Open = true
			dp.ForNewTask = k.ProjectOpenDirPicker.ForNewTask
			dp.MachineID = machID
			dp.CurrentPath = k.ProjectOpenDirPicker.Path
			dp.Error = ""
			dp.ErrorText = nil
			if machID != "" {
				dp.Loading = true
				c.submitListDirs(ctx, pickerKey, machID, k.ProjectOpenDirPicker.Path)
			} else {
				dp.Loading = false
				dp.ActiveRequestID = ""
			}
			ctx.Dirty.Add(false, true, true)
		}

	case *corev1.Event_ProjectDirNavigate:
		if k.ProjectDirNavigate != nil && k.ProjectDirNavigate.PickerKey != "" {
			pickerKey := k.ProjectDirNavigate.PickerKey
			dp := st.EnsureDirPicker(pickerKey)
			machID := k.ProjectDirNavigate.MachineId
			if machID == "" {
				machID = dp.MachineID
			}
			dp.MachineID = machID
			dp.CurrentPath = k.ProjectDirNavigate.Path
			dp.Error = ""
			dp.ErrorText = nil
			dp.Loading = true
			c.submitListDirs(ctx, pickerKey, machID, k.ProjectDirNavigate.Path)
			ctx.Dirty.Add(false, true, true)
		}

	case *corev1.Event_ProjectCloseDirPicker:
		if k.ProjectCloseDirPicker != nil && k.ProjectCloseDirPicker.PickerKey != "" {
			if st.DirPickers != nil {
				delete(st.DirPickers, k.ProjectCloseDirPicker.PickerKey)
			}
			ctx.Dirty.Add(false, true, true)
		}

	case *corev1.Event_NewSessionOpen:
		if k.NewSessionOpen != nil {
			if k.NewSessionOpen.ProjectKey != "" {
				st.StandaloneProjectKey = k.NewSessionOpen.ProjectKey
			}
			key := c.m.activeNewSessionKey(k.NewSessionOpen.ProjectKey)
			if key != "" {
				d := c.m.ensureDraft(key)
				if d.ResetOnNextOpen && !d.Busy && !d.Frozen && d.Prompt == "" && d.AttachmentCount == 0 {
					d.ConfigChosen = false
					d.MachineChosen = false
					d.AgentOverride = v1.Agent_AGENT_UNSPECIFIED
					d.DisabledFromAgent = v1.Agent_AGENT_UNSPECIFIED
					d.ConfigInitialized = false
					d.ResetOnNextOpen = false
					d.DiskFull = false
				}
				psr := c.m.resolveDraftConfig(key, d)
				c.m.syncDraftAutoSupport(key, psr)
				if d.Config.MachineID != "" {
					c.m.maybeFetchMachineConfig(ctx, d.Config.MachineID, d.Config.Agent)
				}
			}
			ctx.Dirty.Add(false, false, true)
		}

	case *corev1.Event_NewSessionSelectProject:
		if k.NewSessionSelectProject != nil && k.NewSessionSelectProject.ProjectKey != "" {
			st.StandaloneProjectKey = k.NewSessionSelectProject.ProjectKey
			d := c.m.ensureDraft(st.StandaloneProjectKey)
			psr := c.m.resolveDraftConfig(st.StandaloneProjectKey, d)
			c.m.syncDraftAutoSupport(st.StandaloneProjectKey, psr)
			if d.Config.MachineID != "" {
				c.m.maybeFetchMachineConfig(ctx, d.Config.MachineID, d.Config.Agent)
			}
			ctx.Dirty.Add(false, false, true)
		}

	case *corev1.Event_NewSessionSelectMachine:
		if k.NewSessionSelectMachine != nil {
			key := c.m.activeNewSessionKey("")
			if d := c.m.ensureDraft(key); d != nil {
				mac := c.m.findMachine(k.NewSessionSelectMachine.MachineId)
				var agents []v1.Agent
				if mac != nil {
					agents = lists.MachineAgents(mac.Meta)
				}
				d.Config = lists.SwitchMachine(d.Config, k.NewSessionSelectMachine.MachineId, agents)
				d.Config.Agent = lists.EffectiveAgent(d.Config.Agent)
				d.ConfigInitialized = true
				d.ConfigChosen = true
				d.MachineChosen = true
				d.ResetOnNextOpen = false
				d.DiskFull = false
				d.DisabledFromAgent = v1.Agent_AGENT_UNSPECIFIED
				d.AgentOverride = d.Config.Agent
				c.m.maybeFetchMachineConfig(ctx, d.Config.MachineID, d.Config.Agent)
				op := buildFullSettingsOp(d.Config, true)
				c.m.sync.EnqueuePrefsOp(ctx, op, true, key)
				if c.m.attachments != nil {
					if ad := c.m.attachments.drafts["new:"+key]; ad != nil {
						if c.retargetDraftLocked(ctx, ad, d.Config.MachineID, &effects) {
							c.pumpAttachmentQueues(ctx, &effects)
						}
					}
				}
				ctx.Dirty.Add(false, false, true)
			}
		}

	case *corev1.Event_NewSessionSelectAgent:
		if k.NewSessionSelectAgent != nil {
			key := c.m.activeNewSessionKey("")
			if d := c.m.ensureDraft(key); d != nil {
				d.AgentOverride = lists.EffectiveAgent(k.NewSessionSelectAgent.Agent)
				psr := lists.ResolveProjectStart(&st.World, key, d.Config.MachineID, d.AgentOverride, nil, false)
				d.Config = lists.SwitchAgent(psr.Config, &d.Config, d.AgentOverride)
				d.Config.Agent = lists.EffectiveAgent(d.Config.Agent)
				d.ConfigInitialized = true
				d.ConfigChosen = true
				d.ResetOnNextOpen = false
				d.DisabledFromAgent = v1.Agent_AGENT_UNSPECIFIED
				c.m.maybeFetchMachineConfig(ctx, d.Config.MachineID, d.Config.Agent)
				op := buildFullSettingsOp(d.Config, false)
				c.m.sync.EnqueuePrefsOp(ctx, op, true, key)
				ctx.Dirty.Add(false, false, true)
			}
		}

	case *corev1.Event_NewSessionSelectModel:
		if k.NewSessionSelectModel != nil {
			key := c.m.activeNewSessionKey("")
			if d := c.m.ensureDraft(key); d != nil {
				act := k.NewSessionSelectModel
				d.Config.Agent = lists.EffectiveAgent(d.Config.Agent)
				d.Config.Model = act.Model
				if act.Effort != "" || act.Model != "" {
					d.Config.Effort = act.Effort
				}
				if act.StandardContext != nil {
					d.Config.StandardContext = *act.StandardContext
				}
				if act.Mode != nil {
					d.Config.Mode = *act.Mode
				}
				d.ConfigInitialized = true
				d.ConfigChosen = true
				d.ResetOnNextOpen = false
				op := buildFullSettingsOp(d.Config, false)
				c.m.sync.EnqueuePrefsOp(ctx, op, true, key)
				ctx.Dirty.Add(false, false, true)
			}
		}

	case *corev1.Event_NewSessionSetPrompt:
		if k.NewSessionSetPrompt != nil {
			key := c.m.activeNewSessionKey("")
			if d := c.m.ensureDraft(key); d != nil {
				d.Prompt = k.NewSessionSetPrompt.Prompt
				d.AttachmentCount = k.NewSessionSetPrompt.AttachmentCount
				d.AttachmentsMoved = k.NewSessionSetPrompt.AttachmentsMoved
				ctx.Dirty.Add(false, false, true)
			}
		}

	case *corev1.Event_NewSessionRetryModels:
		key := ""
		if k.NewSessionRetryModels != nil {
			key = k.NewSessionRetryModels.ProjectKey
		}
		key = c.m.activeNewSessionKey(key)
		if d := c.m.ensureDraft(key); d != nil && d.Config.MachineID != "" {
			delete(st.MachineModels, screens.MachineAgentKey{
				MachineID: d.Config.MachineID,
				Agent:     lists.EffectiveAgent(d.Config.Agent),
			})
			delete(st.MachineSettings, d.Config.MachineID)
			c.m.maybeFetchMachineConfig(ctx, d.Config.MachineID, d.Config.Agent)
			ctx.Dirty.Add(false, false, true)
		}

	case *corev1.Event_NewSessionRetrySaveConfig:
		key := ""
		if k.NewSessionRetrySaveConfig != nil {
			key = k.NewSessionRetrySaveConfig.ProjectKey
		}
		key = c.m.activeNewSessionKey(key)
		if d := c.m.ensureDraft(key); d != nil && d.Config.Cwd != "" {
			op := buildFullSettingsOp(d.Config, false)
			c.m.sync.EnqueuePrefsOp(ctx, op, true, key)
			ctx.Dirty.Add(false, false, true)
		}

	case *corev1.Event_NewSessionSubmit:
		if k.NewSessionSubmit != nil {
			key := c.m.activeNewSessionKey(k.NewSessionSubmit.ProjectKey)
			if d := c.m.ensureDraft(key); d != nil {
				d.AutoModeSupported = k.NewSessionSubmit.AutoModeSupported
				if !d.ConfigInitialized {
					c.m.resolveDraftConfig(key, d)
				}
				cfg := d.Config
				if cfg.Cwd == "" {
					cfg.Cwd = key
				}
				imgIDs := k.NewSessionSubmit.ImageIds
				fileIDs := k.NewSessionSubmit.FileIds
				coreImgs, coreFiles, blocked := c.prepareNewSessionAttachments(ctx, key, cfg.MachineID, "", &effects)
				if blocked {
					ctx.Dirty.Add(false, false, true)
					break
				}
				if c.m.attachments != nil && c.m.attachments.configured {
					// Core-configured path exclusively uses Core draft attachments (never dual-path fallback).
					imgIDs = coreImgs
					fileIDs = coreFiles
				} else if len(coreImgs) > 0 || len(coreFiles) > 0 {
					imgIDs = coreImgs
					fileIDs = coreFiles
				}
				cmd := &v1.Command{
					Kind: &v1.Command_CreateSession{
						CreateSession: &v1.CreateSessionRequest{
							Cwd:             cfg.Cwd,
							Prompt:          d.Prompt,
							Model:           cfg.Model,
							Effort:          cfg.Effort,
							Mode:            cfg.Mode,
							Agent:           lists.EffectiveAgent(cfg.Agent),
							ImageIds:        imgIDs,
							FileIds:         fileIDs,
							StandardContext: cfg.StandardContext,
						},
					},
				}
				reqID, dispatched := c.m.sync.SubmitCommand(ctx, sync.SubmitCommandParams{
					SlotKey:   "create_session:" + key,
					MachineID: cfg.MachineID,
					Command:   cmd,
				})
				if reqID != "" {
					c.m.pendingCmdMeta[reqID] = cmdMeta{
						kind:       cmdKindCreateSession,
						machineID:  cfg.MachineID,
						projectKey: key,
					}
				}
				if dispatched {
					if c.m.attachments != nil {
						if stStart := c.m.attachments.newSessionStarts[key]; stStart != nil {
							stStart.requestID = reqID
						}
					}
					d.Busy = true
					d.DiskFull = false
					d.StartError = ""
					d.StartErrorText = nil
					d.ActiveStartRequestID = reqID
				} else {
					c.releaseNewSessionStartProtection(key)
				}
				c.noteCreateSessionSubmitted(key, cmd.GetCreateSession())
				ctx.Dirty.Add(true, true, true)
			}
		}

	case *corev1.Event_NewSessionAbandonStart:
		key := ""
		if k.NewSessionAbandonStart != nil {
			key = k.NewSessionAbandonStart.ProjectKey
		}
		key = c.m.activeNewSessionKey(key)
		c.m.sync.AbandonAttempt(ctx, "create_session:"+key)
		c.releaseNewSessionStartProtection(key)
		if d := c.m.state.NewSessions[key]; d != nil {
			d.Busy = false
			d.Frozen = false
			d.Expired = false
			d.UnknownStart = false
			d.DiskFull = false
			d.StartError = ""
			d.StartErrorText = nil
			d.ActiveStartRequestID = ""
		}
		ctx.Dirty.Add(false, false, true)

	case *corev1.Event_RunMachineCommand:
		if act := k.RunMachineCommand; act != nil && act.OpId != 0 {
			if prevReq, exists := c.m.oneShotReqByOp[act.OpId]; exists {
				delete(c.m.oneShotReqByOp, act.OpId)
				delete(c.m.pendingCmdMeta, prevReq)
				c.m.sync.CancelCommand(ctx, prevReq)
			}
			if act.MachineId == "" || act.Command == nil {
				effects = append(effects, &corev1.Effect{
					Effect: &corev1.Effect_RunMachineCommandResult{
						RunMachineCommandResult: &corev1.RunMachineCommandResultEffect{
							OpId:      act.OpId,
							Ok:        false,
							ErrorCode: "invalid_argument",
						},
					},
				})
				break
			}
			reqID, _ := c.m.sync.SubmitCommand(ctx, sync.SubmitCommandParams{
				MachineID: act.MachineId,
				SessionID: act.SessionId,
				Command:   act.Command,
			})
			if reqID == "" {
				effects = append(effects, &corev1.Effect{
					Effect: &corev1.Effect_RunMachineCommandResult{
						RunMachineCommandResult: &corev1.RunMachineCommandResultEffect{
							OpId:      act.OpId,
							Ok:        false,
							ErrorCode: "failed",
						},
					},
				})
				break
			}
			c.m.pendingCmdMeta[reqID] = cmdMeta{
				kind:      cmdKindMachineOneShot,
				machineID: act.MachineId,
				opID:      act.OpId,
			}
			c.m.oneShotReqByOp[act.OpId] = reqID
		}

	case *corev1.Event_CancelMachineCommand:
		if act := k.CancelMachineCommand; act != nil && act.OpId != 0 {
			if reqID, ok := c.m.oneShotReqByOp[act.OpId]; ok {
				delete(c.m.oneShotReqByOp, act.OpId)
				delete(c.m.pendingCmdMeta, reqID)
				c.m.sync.CancelCommand(ctx, reqID)
			}
		}

	default:
		effects = append(effects, c.dispatchSettingsUI(ctx, ev)...)
		effects = append(effects, c.dispatchSessionUI(ctx, ev)...)
		effects = append(effects, c.dispatchUsageUI(ctx, ev)...)
		if schedEffects, handled := c.dispatchSchedulesUI(ctx, ev); handled {
			effects = append(effects, schedEffects...)
		}
	}

	return effects
}

func (c *Core) submitListDirs(ctx *sync.StepContext, pickerKey, machineID, dirPath string) {
	if pickerKey == "" {
		return
	}
	reqID, ok := c.m.sync.SubmitCommand(ctx, sync.SubmitCommandParams{
		MachineID: machineID,
		Command: &v1.Command{
			Kind: &v1.Command_ListDirectories{
				ListDirectories: &v1.ListDirectoriesRequest{Path: dirPath},
			},
		},
	})
	if reqID != "" {
		c.m.pendingCmdMeta[reqID] = cmdMeta{
			kind:      cmdKindListDirs,
			machineID: machineID,
			pickerKey: pickerKey,
		}
	}
	dp := c.m.state.EnsureDirPicker(pickerKey)
	if ok {
		dp.ActiveRequestID = reqID
	} else {
		dp.Loading = false
		dp.ActiveRequestID = ""
	}
}
