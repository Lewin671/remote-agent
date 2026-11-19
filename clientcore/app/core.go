// Package app hosts the shared Core state machine and Dispatch/View entrypoints
// exposed to Web, Android, and iOS shells.
package app

import (
	"errors"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/outcome"
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
	return &Core{m: newModel()}
}

// Close releases references held by Core.
func (c *Core) Close() {}

// View renders the requested screen from the current synchronized state.
func (c *Core) View(screen *corev1.Screen) (*corev1.ViewModel, error) {
	if screen == nil {
		return nil, errors.New("app: nil screen request")
	}
	c.m.sync.SyncToState(c.m.state)
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
		if s.Project != nil {
			key = s.Project.ProjectKey
			manage = s.Project.Manage
			endedLimit = int(s.Project.EndedLimit)
			machineID = s.Project.MachineId
		}
		return &corev1.ViewModel{
			View: &corev1.ViewModel_Project{
				Project: screens.BuildProject(c.m.state, key, manage, endedLimit, machineID),
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
		effects = c.m.mergeScheduleRender(effects)
		return &corev1.EffectBatch{Effects: effects}
	}

	var uiEffects []*corev1.Effect
	stepRes := c.m.sync.Run(func(ctx *sync.StepContext) {
		uiEffects = c.dispatchUI(ctx, ev)
	})
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
	effects = c.m.mergeScheduleRender(effects)
	return &corev1.EffectBatch{Effects: effects}
}

func (c *Core) processSyncStep(res sync.StepResult) ([]*corev1.Effect, sync.DirtyFlags) {
	c.m.sync.SyncToState(c.m.state)
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

		case cmdKindListDirs:
			dp := &c.m.state.DirPicker
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
				d.ActiveStartRequestID = ""
				d.Prompt = ""
				d.AttachmentCount = 0
				d.AttachmentsMoved = false
				d.ResetOnNextOpen = true

				effects = append(effects, &corev1.Effect{
					Effect: &corev1.Effect_AttachmentsSettled{
						AttachmentsSettled: &corev1.AttachmentsSettledEffect{
							ProjectKey: meta.projectKey,
							RequestId:  sc.RequestID,
							Consumed:   true,
						},
					},
				})
				saveRes := c.m.sync.Run(func(ctx *sync.StepContext) {
					if sess != nil && sess.Id != "" {
						c.m.sync.SeedSession(ctx, sc.MachineID, sess)
					}
					cfg := d.Config
					if cfg.Cwd == "" {
						cfg.Cwd = meta.projectKey
					}
					saved := lists.SavedSettings(c.m.state.World.Prefs, cfg.Cwd)
					if op := lists.PlanStartSettings(cfg, &cfg, saved); op != nil {
						c.m.sync.EnqueuePrefsOp(ctx, op, true, meta.projectKey)
					}
				})
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
				d.StartErrorText = sc.ErrorText
				d.StartError = sc.ErrorCode
				if sc.Expired {
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
				d.StartErrorText = sc.ErrorText
				d.StartError = sc.ErrorCode
				if d.StartError == "" && d.StartErrorText == nil {
					d.StartError = "failed"
				}
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
			dirty.Add(true, true, true)
		default:
			effects = append(effects, c.onOtherSettingsCmdSettled(sc, meta)...)
		}
	}

	if res.Dirty.NewSession || dirty.NewSession {
		extraRes := c.m.sync.Run(func(ctx *sync.StepContext) {
			for key, d := range c.m.state.NewSessions {
				if d == nil {
					continue
				}
				psr := c.m.resolveDraftConfig(key, d)
				c.m.syncDraftAutoSupport(key, psr)
				if d.Config.MachineID != "" {
					c.m.maybeFetchMachineConfig(ctx, d.Config.MachineID, d.Config.Agent)
				}
			}
		})
		effects = append(effects, extraRes.Effects...)
	}

	if res.Dirty.Home || res.Dirty.SyncState {
		c.reconcileWatchedMachinePolls(&effects)
		if c.m.usageActive {
			c.m.usageDirty = true
		}
		if !(res.Dirty.NewSession || dirty.NewSession) {
			extraRes := c.m.sync.Run(func(ctx *sync.StepContext) {
				for key, d := range c.m.state.NewSessions {
					if d == nil || !d.ConfigInitialized || d.Busy || d.Frozen || d.Config.MachineID == "" {
						continue
					}
					if mac := c.m.findMachine(d.Config.MachineID); mac != nil && lists.IsAgentDisabled(mac.Meta, d.Config.Agent) {
						psr := c.m.resolveDraftConfig(key, d)
						c.m.syncDraftAutoSupport(key, psr)
						c.m.maybeFetchMachineConfig(ctx, d.Config.MachineID, d.Config.Agent)
						dirty.Add(false, false, true)
					}
				}
			})
			effects = append(effects, extraRes.Effects...)
		}
	}

	c.processSchedulesSyncStep(res, &effects)

	return effects, dirty
}

func (c *Core) dispatchUI(ctx *sync.StepContext, ev *corev1.Event) []*corev1.Effect {
	c.m.sync.SyncToState(c.m.state)
	var effects []*corev1.Effect
	st := c.m.state

	switch k := ev.Event.(type) {
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
			c.m.sync.SyncToState(st)
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
			c.m.sync.SyncToState(st)
			ctx.Dirty.Add(true, true, false)
		}

	case *corev1.Event_ProjectAdd:
		if k.ProjectAdd != nil && k.ProjectAdd.Path != "" {
			machID := st.DirPicker.MachineID
			forNewTask := st.DirPicker.ForNewTask
			if op := lists.PlanAddProject(&st.World, machID, k.ProjectAdd.Path); op != nil {
				c.m.sync.EnqueuePrefsOp(ctx, op, false, "")
			}
			st.DirPicker.Open = false
			st.DirPicker.ForNewTask = false
			st.DirPicker.Loading = false
			st.DirPicker.Error = ""
			st.DirPicker.ErrorText = nil
			st.DirPicker.ActiveRequestID = ""
			c.m.sync.SyncToState(st)
			if forNewTask {
				d := c.m.ensureDraft(k.ProjectAdd.Path)
				d.MachineChosen = machID != ""
				d.ConfigChosen = false
				d.ResetOnNextOpen = false
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
		if k.ProjectOpenDirPicker != nil {
			machID := k.ProjectOpenDirPicker.MachineId
			if machID == "" {
				c.m.sync.SyncToState(st)
				for _, m := range st.World.Machines {
					if m != nil && lists.MachineReachable(m) && (machID == "" || m.ID < machID) {
						machID = m.ID
					}
				}
			}
			st.DirPicker.Open = true
			st.DirPicker.ForNewTask = k.ProjectOpenDirPicker.ForNewTask
			st.DirPicker.MachineID = machID
			st.DirPicker.CurrentPath = k.ProjectOpenDirPicker.Path
			st.DirPicker.Error = ""
			st.DirPicker.ErrorText = nil
			if machID != "" {
				st.DirPicker.Loading = true
				c.submitListDirs(ctx, machID, k.ProjectOpenDirPicker.Path)
			} else {
				st.DirPicker.Loading = false
				st.DirPicker.ActiveRequestID = ""
			}
			ctx.Dirty.Add(false, true, true)
		}

	case *corev1.Event_ProjectDirNavigate:
		if k.ProjectDirNavigate != nil {
			machID := k.ProjectDirNavigate.MachineId
			if machID == "" {
				machID = st.DirPicker.MachineID
			}
			st.DirPicker.MachineID = machID
			st.DirPicker.CurrentPath = k.ProjectDirNavigate.Path
			st.DirPicker.Error = ""
			st.DirPicker.ErrorText = nil
			st.DirPicker.Loading = true
			c.submitListDirs(ctx, machID, k.ProjectDirNavigate.Path)
			ctx.Dirty.Add(false, true, true)
		}

	case *corev1.Event_ProjectCloseDirPicker:
		st.DirPicker = screens.DirPickerState{}
		ctx.Dirty.Add(false, true, true)

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
				d.DisabledFromAgent = v1.Agent_AGENT_UNSPECIFIED
				d.AgentOverride = d.Config.Agent
				c.m.maybeFetchMachineConfig(ctx, d.Config.MachineID, d.Config.Agent)
				op := buildFullSettingsOp(d.Config, true)
				c.m.sync.EnqueuePrefsOp(ctx, op, true, key)
				c.m.sync.SyncToState(st)
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
				c.m.sync.SyncToState(st)
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
				c.m.sync.SyncToState(st)
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
			c.m.sync.SyncToState(st)
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
				cmd := &v1.Command{
					Kind: &v1.Command_CreateSession{
						CreateSession: &v1.CreateSessionRequest{
							Cwd:             cfg.Cwd,
							Prompt:          d.Prompt,
							Model:           cfg.Model,
							Effort:          cfg.Effort,
							Mode:            cfg.Mode,
							Agent:           lists.EffectiveAgent(cfg.Agent),
							ImageIds:        k.NewSessionSubmit.ImageIds,
							FileIds:         k.NewSessionSubmit.FileIds,
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
					d.Busy = true
					d.StartError = ""
					d.StartErrorText = nil
					d.ActiveStartRequestID = reqID
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
		if d := c.m.state.NewSessions[key]; d != nil {
			d.Busy = false
			d.Frozen = false
			d.Expired = false
			d.UnknownStart = false
			d.StartError = ""
			d.StartErrorText = nil
			d.ActiveStartRequestID = ""
		}
		ctx.Dirty.Add(false, false, true)
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

func (c *Core) submitListDirs(ctx *sync.StepContext, machineID, dirPath string) {
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
		}
	}
	if ok {
		c.m.state.DirPicker.ActiveRequestID = reqID
	} else {
		c.m.state.DirPicker.Loading = false
		c.m.state.DirPicker.ActiveRequestID = ""
	}
}
