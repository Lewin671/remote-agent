package app

import (
	"errors"
	"slices"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/screens"
)

var (
	errNilScreen    = errors.New("app: nil screen request")
	errCoreClosed   = errors.New("app: core closed")
	errUnknownRoute = errors.New("app: unsupported screen")
)

// Core is the stateful client core implementing Dispatch(Event) -> Effects and View(Screen) -> ViewModel.
type Core struct {
	model     *Model
	opPaths   map[string]string
	startKeys map[string]string
}

// New constructs an initialized Core instance.
func New() *Core {
	return &Core{
		model:     newModel(),
		opPaths:   make(map[string]string),
		startKeys: make(map[string]string),
	}
}

// Close releases internal state references.
func (c *Core) Close() {
	if c == nil || c.model == nil {
		return
	}
	c.model.closed = true
	c.opPaths = nil
	c.startKeys = nil
}

// View computes the screen-ready ViewModel for screen from current Model state without side effects.
func (c *Core) View(screen *corev1.Screen) (*corev1.ViewModel, error) {
	if c == nil || c.model == nil || c.model.closed {
		return nil, errCoreClosed
	}
	if screen == nil {
		return nil, errNilScreen
	}
	switch s := screen.Screen.(type) {
	case *corev1.Screen_Home:
		var endedLimit int
		if s.Home != nil {
			endedLimit = int(s.Home.EndedLimit)
		}
		return &corev1.ViewModel{
			View: &corev1.ViewModel_Home{
				Home: screens.BuildHome(c.model.state, endedLimit),
			},
		}, nil
	case *corev1.Screen_Project:
		var key string
		var manage bool
		var endedLimit int
		if s.Project != nil {
			key = s.Project.ProjectKey
			manage = s.Project.Manage
			endedLimit = int(s.Project.EndedLimit)
		}
		return &corev1.ViewModel{
			View: &corev1.ViewModel_Project{
				Project: screens.BuildProject(c.model.state, key, manage, endedLimit),
			},
		}, nil
	case *corev1.Screen_NewSession:
		var key string
		if s.NewSession != nil {
			key = s.NewSession.ProjectKey
		}
		return &corev1.ViewModel{
			View: &corev1.ViewModel_NewSession{
				NewSession: screens.BuildNewSession(c.model.state, key),
			},
		}, nil
	default:
		return nil, errUnknownRoute
	}
}

// Dispatch applies one Event transition to Model and returns ordered Effects for the host shell.
func (c *Core) Dispatch(ev *corev1.Event) *corev1.EffectBatch {
	batch := &corev1.EffectBatch{}
	if c == nil || c.model == nil || c.model.closed || ev == nil {
		return batch
	}
	m := c.model
	st := m.state

	var effects []*corev1.Effect
	emit := func(e *corev1.Effect) {
		if e != nil {
			effects = append(effects, e)
		}
	}

	switch k := ev.Event.(type) {
	case *corev1.Event_ClockUpdated:
		if k.ClockUpdated != nil {
			st.Cal = lists.Calendar{
				NowMs:            k.ClockUpdated.NowMs,
				TodayStartMs:     k.ClockUpdated.TodayStartMs,
				YesterdayStartMs: k.ClockUpdated.YesterdayStartMs,
				WeekStartMs:      k.ClockUpdated.WeekStartMs,
				NowYear:          k.ClockUpdated.NowYear,
				UTCOffsetMinutes: k.ClockUpdated.UtcOffsetMinutes,
			}
			emit(renderEffect(corev1.ScreenTarget_SCREEN_TARGET_HOME, corev1.ScreenTarget_SCREEN_TARGET_PROJECT))
		}

	case *corev1.Event_ConnectionChanged:
		if k.ConnectionChanged != nil {
			cc := k.ConnectionChanged
			st.World.Connection = mapConnState(cc.State)
			st.ConnectionReason = cc.Reason
			st.DisconnectedAtMs = cc.DisconnectedAtMs
			st.LivePushConnected = cc.LivePushConnected
			emit(renderEffect(
				corev1.ScreenTarget_SCREEN_TARGET_HOME,
				corev1.ScreenTarget_SCREEN_TARGET_PROJECT,
				corev1.ScreenTarget_SCREEN_TARGET_NEW_SESSION,
			))
		}

	case *corev1.Event_AccountRoleChanged:
		if k.AccountRoleChanged != nil {
			ar := k.AccountRoleChanged
			st.World.IsManager = ar.IsManager
			st.World.ManagerName = ar.ManagerDeviceName
			st.World.AccountControlLost = ar.ControlExpired
			st.World.AccountClockSkewed = ar.ClockSkewed
			emit(renderEffect(corev1.ScreenTarget_SCREEN_TARGET_HOME, corev1.ScreenTarget_SCREEN_TARGET_PROJECT))
		}

	case *corev1.Event_MachinesUpdated:
		if k.MachinesUpdated != nil {
			mu := k.MachinesUpdated
			st.World.MachinesListed = mu.Listed
			if mu.ClientProtocolVersion > 0 {
				st.World.ClientVersion = mu.ClientProtocolVersion
			}
			st.World.Machines = make([]*lists.Machine, 0, len(mu.Machines))
			for _, mi := range mu.Machines {
				if lm := machineFromInput(mi); lm != nil {
					st.World.Machines = append(st.World.Machines, lm)
				}
			}
			if key := m.activeNewSessionKey(""); key != "" {
				d := st.NewSessions[key]
				res := m.resolveDraftConfig(key, d)
				if res.Found {
					for _, eff := range m.maybeFetchMachineConfig(res.Config.MachineID, res.Config.Agent) {
						emit(eff)
					}
				}
			}
			emit(renderEffect(
				corev1.ScreenTarget_SCREEN_TARGET_HOME,
				corev1.ScreenTarget_SCREEN_TARGET_PROJECT,
				corev1.ScreenTarget_SCREEN_TARGET_NEW_SESSION,
			))
		}

	case *corev1.Event_SessionsPageReceived:
		if k.SessionsPageReceived != nil {
			p := k.SessionsPageReceived
			st.World.LoadingMoreSessions = false
			if p.Error != "" {
				st.World.SessionListFailed = true
				st.World.SessionListError = p.Error
			} else {
				st.World.LiveLoaded = true
				st.World.SessionListFailed = false
				st.World.SessionListError = ""
				st.World.SessionsListed = true
				st.World.HasMoreSessions = p.HasMore
				st.World.SessionsCursor = p.NextCursor
				if p.NextEndedMs > 0 || p.NextSessionId != "" {
					st.World.SessionsNext = &lists.SessionPlace{
						EndedMs:   p.NextEndedMs,
						SessionID: p.NextSessionId,
					}
				} else if !p.HasMore {
					st.World.SessionsNext = nil
				}
				if p.IsRefresh && p.NextCursor == "" && len(p.Sessions) == 0 {
					st.World.Sessions = nil
					m.sessionIndex = make(map[string]int)
				}
				for _, si := range p.Sessions {
					m.upsertSession(sessionFromInput(si))
				}
			}
			emit(renderEffect(
				corev1.ScreenTarget_SCREEN_TARGET_HOME,
				corev1.ScreenTarget_SCREEN_TARGET_PROJECT,
				corev1.ScreenTarget_SCREEN_TARGET_NEW_SESSION,
			))
		}

	case *corev1.Event_SessionUpserted:
		if k.SessionUpserted != nil {
			st.World.LiveLoaded = true
			ls := sessionFromInput(k.SessionUpserted)
			m.upsertSession(ls)
			emit(renderEffect(corev1.ScreenTarget_SCREEN_TARGET_HOME, corev1.ScreenTarget_SCREEN_TARGET_PROJECT))
		}

	case *corev1.Event_SessionRemoved:
		if k.SessionRemoved != nil {
			m.removeSession(k.SessionRemoved.SessionId)
			emit(renderEffect(corev1.ScreenTarget_SCREEN_TARGET_HOME, corev1.ScreenTarget_SCREEN_TARGET_PROJECT))
		}

	case *corev1.Event_PrefsUpdated:
		if k.PrefsUpdated != nil {
			pu := k.PrefsUpdated
			st.World.Prefs = pu.Prefs
			st.World.PrefsLoaded = pu.Loaded
			st.World.PrefsLoadFailed = pu.LoadFailed
			if pu.ChangedElsewhere {
				st.ProjectChangedElsewhere = true
			}
			if pu.HomeScope == "all" {
				st.RememberedScope = &lists.Scope{AllProjects: true}
				st.RememberedScopeToken = "all"
				st.Scope = st.RememberedScope
			} else if pu.HomeScope != "" {
				if len(pu.HomeScope) > 8 && pu.HomeScope[:8] == "project:" {
					st.RememberedScopeToken = pu.HomeScope
					st.RememberedScope = nil
					st.Scope = nil
				} else {
					st.RememberedScope = &lists.Scope{Key: pu.HomeScope, Name: lists.ProjectName(pu.HomeScope), ShortPath: lists.ShortPath(pu.HomeScope)}
					st.Scope = st.RememberedScope
				}
			}
			if pu.Prefs != nil {
				for _, opID := range pu.Prefs.RecentOps {
					if path, ok := c.opPaths[opID]; ok {
						delete(st.PrefsPendingPath, path)
						delete(st.PrefsSaveErrorByPath, path)
						delete(st.PrefsSavingSettings, path)
						delete(st.PrefsSettingsError, path)
						delete(c.opPaths, opID)
					}
				}
			}
			emit(renderEffect(
				corev1.ScreenTarget_SCREEN_TARGET_HOME,
				corev1.ScreenTarget_SCREEN_TARGET_PROJECT,
				corev1.ScreenTarget_SCREEN_TARGET_NEW_SESSION,
			))
		}

	case *corev1.Event_PrefsSyncResult:
		if k.PrefsSyncResult != nil {
			pr := k.PrefsSyncResult
			st.World.PrefsSyncing = false
			if pr.ChangedElsewhere {
				st.ProjectChangedElsewhere = true
			}
			path := c.opPaths[pr.OpId]
			delete(c.opPaths, pr.OpId)
			if path != "" {
				delete(st.PrefsPendingPath, path)
				delete(st.PrefsSavingSettings, path)
			}
			if pr.Error != "" {
				if path != "" {
					st.PrefsSaveErrorByPath[path] = pr.Error
				}
				st.PrefsGlobalError = pr.Error
			} else {
				if path != "" {
					delete(st.PrefsSaveErrorByPath, path)
					delete(st.PrefsSettingsError, path)
				}
				st.PrefsGlobalError = ""
			}
			emit(renderEffect(
				corev1.ScreenTarget_SCREEN_TARGET_HOME,
				corev1.ScreenTarget_SCREEN_TARGET_PROJECT,
				corev1.ScreenTarget_SCREEN_TARGET_NEW_SESSION,
			))
		}

	case *corev1.Event_MachineModelsReceived:
		if k.MachineModelsReceived != nil {
			mr := k.MachineModelsReceived
			st.MachineModels[screens.MachineAgentKey{
				MachineID: mr.MachineId,
				Agent:     lists.EffectiveAgent(mr.Agent),
			}] = &screens.ModelsCacheEntry{
				Response: mr.Response,
				Loaded:   mr.Error == "",
				Loading:  false,
				Error:    mr.Error,
			}
			emit(renderEffect(corev1.ScreenTarget_SCREEN_TARGET_NEW_SESSION))
		}

	case *corev1.Event_MachineSettingsReceived:
		if k.MachineSettingsReceived != nil {
			sr := k.MachineSettingsReceived
			st.MachineSettings[sr.MachineId] = &screens.SettingsCacheEntry{
				Settings: sr.Settings,
				Loaded:   sr.Error == "",
				Loading:  false,
				Error:    sr.Error,
			}
			emit(renderEffect(corev1.ScreenTarget_SCREEN_TARGET_NEW_SESSION))
		}

	case *corev1.Event_DirListingReceived:
		if k.DirListingReceived != nil {
			dl := k.DirListingReceived
			if dl.RequestId == "" || dl.RequestId == st.DirPicker.ActiveRequestID {
				st.DirPicker.Loading = false
				if dl.Error != "" {
					st.DirPicker.Error = dl.Error
				} else {
					st.DirPicker.Error = ""
					if dl.Path != "" {
						st.DirPicker.CurrentPath = dl.Path
					}
					st.DirPicker.ParentPath = dl.ParentPath
					st.DirPicker.Dirs = slices.Clone(dl.Dirs)
				}
				emit(renderEffect(corev1.ScreenTarget_SCREEN_TARGET_PROJECT))
			}
		}

	case *corev1.Event_CreateSessionResult:
		if k.CreateSessionResult != nil {
			cr := k.CreateSessionResult
			key := c.startKeys[cr.RequestId]
			if key == "" {
				key = m.activeNewSessionKey("")
			}
			d := st.NewSessions[key]
			if d != nil && (cr.RequestId == "" || d.ActiveStartRequestID == cr.RequestId) {
				d.Busy = false
				switch {
				case cr.Outcome == "executed":
					delete(c.startKeys, cr.RequestId)
					d.Prompt = ""
					d.AttachmentCount = 0
					d.AttachmentsMoved = false
					d.Frozen = false
					d.Expired = false
					d.UnknownStart = false
					d.StartError = ""
					d.DiskFull = false
					d.ActiveStartRequestID = ""
					if cr.SessionId != "" {
						st.HomeSelectedSessionID = cr.SessionId
						emit(&corev1.Effect{
							Effect: &corev1.Effect_Navigate{
								Navigate: &corev1.NavigateEffect{
									Route:     corev1.NavigateRoute_NAVIGATE_ROUTE_SESSION,
									SessionId: cr.SessionId,
								},
							},
						})
					}
				case cr.Unknown || cr.Expired:
					d.Frozen = true
					d.Expired = cr.Expired
					d.UnknownStart = true
					d.StartError = cr.Error
					if d.StartError == "" {
						d.StartError = cr.Reason
					}
				default:
					delete(c.startKeys, cr.RequestId)
					d.Frozen = false
					d.DiskFull = cr.DiskFull
					d.StartError = cr.Error
					if d.StartError == "" {
						d.StartError = cr.Reason
					}
				}
				emit(renderEffect(corev1.ScreenTarget_SCREEN_TARGET_NEW_SESSION))
			}
		}

	case *corev1.Event_HomeSetScope:
		if k.HomeSetScope != nil {
			key := k.HomeSetScope.ScopeKey
			if key == "" {
				st.RememberedScope = &lists.Scope{AllProjects: true}
			} else {
				st.RememberedScope = &lists.Scope{
					Key:       key,
					Name:      lists.ProjectName(key),
					ShortPath: lists.ShortPath(key),
				}
			}
			st.Scope = st.RememberedScope
			st.RememberedScopeToken = ""
			emit(&corev1.Effect{
				Effect: &corev1.Effect_SaveHomeScope{
					SaveHomeScope: &corev1.SaveHomeScopeEffect{
						ScopeKey: key,
					},
				},
			})
			emit(renderEffect(corev1.ScreenTarget_SCREEN_TARGET_HOME))
		}

	case *corev1.Event_HomeSetFilter:
		if k.HomeSetFilter != nil {
			st.HomeFilterQuery = k.HomeSetFilter.Query
			if st.HomeFilterQuery != "" && lists.MoreSessions(&st.World) && !st.World.LoadingMoreSessions {
				st.World.LoadingMoreSessions = true
				emit(&corev1.Effect{
					Effect: &corev1.Effect_FetchSessionsPage{
						FetchSessionsPage: &corev1.FetchSessionsPageEffect{
							RequestId: m.nextID("page-"),
							Cursor:    st.World.SessionsCursor,
							Limit:     lists.EndedPage,
						},
					},
				})
			}
			emit(renderEffect(corev1.ScreenTarget_SCREEN_TARGET_HOME))
		}

	case *corev1.Event_HomeLoadMoreEnded:
		if k.HomeLoadMoreEnded != nil {
			step := int(k.HomeLoadMoreEnded.Step)
			if step <= 0 {
				step = lists.EndedPage
			}
			homeData := lists.BuildHome(&st.World, st.Scope, true, st.HomeFilterQuery, st.Cal, lists.HomeShown{Ended: int(^uint(0)>>1) - step})
			if (homeData.WantPage || lists.MoreSessions(&st.World)) && !st.World.LoadingMoreSessions {
				st.World.LoadingMoreSessions = true
				emit(&corev1.Effect{
					Effect: &corev1.Effect_FetchSessionsPage{
						FetchSessionsPage: &corev1.FetchSessionsPageEffect{
							RequestId: m.nextID("page-"),
							Cursor:    st.World.SessionsCursor,
							Limit:     int32(step),
						},
					},
				})
			}
			emit(renderEffect(corev1.ScreenTarget_SCREEN_TARGET_HOME))
		}

	case *corev1.Event_HomeRetryLoad:
		st.World.SessionListError = ""
		st.World.SessionListFailed = false
		st.World.LoadingMoreSessions = true
		emit(&corev1.Effect{
			Effect: &corev1.Effect_FetchSessionsPage{
				FetchSessionsPage: &corev1.FetchSessionsPageEffect{
					RequestId: m.nextID("page-"),
					Cursor:    st.World.SessionsCursor,
					Limit:     lists.EndedPage,
				},
			},
		})
		emit(renderEffect(corev1.ScreenTarget_SCREEN_TARGET_HOME))

	case *corev1.Event_HomeSelectSession:
		if k.HomeSelectSession != nil {
			st.HomeSelectedSessionID = k.HomeSelectSession.SessionId
			emit(renderEffect(corev1.ScreenTarget_SCREEN_TARGET_HOME, corev1.ScreenTarget_SCREEN_TARGET_PROJECT))
		}

	case *corev1.Event_ProjectSetSearch:
		if k.ProjectSetSearch != nil {
			st.ProjectSearch = k.ProjectSetSearch.Query
			emit(renderEffect(corev1.ScreenTarget_SCREEN_TARGET_HOME, corev1.ScreenTarget_SCREEN_TARGET_PROJECT))
		}

	case *corev1.Event_ProjectTogglePin:
		if k.ProjectTogglePin != nil {
			key := k.ProjectTogglePin.ProjectKey
			kind := lists.PrefsOpPin
			if lists.IsPinned(st.World.Prefs, key) {
				kind = lists.PrefsOpUnpin
			}
			emit(c.applyAndEmitPrefsOp(key, &lists.FullPrefsOp{Kind: kind, Path: key}, nil))
			emit(renderEffect(corev1.ScreenTarget_SCREEN_TARGET_HOME, corev1.ScreenTarget_SCREEN_TARGET_PROJECT))
		}

	case *corev1.Event_ProjectToggleHide:
		if k.ProjectToggleHide != nil {
			key := k.ProjectToggleHide.ProjectKey
			kind := lists.PrefsOpHide
			if lists.IsHidden(st.World.Prefs, key) {
				kind = lists.PrefsOpUnhide
			}
			emit(c.applyAndEmitPrefsOp(key, &lists.FullPrefsOp{Kind: kind, Path: key}, nil))
			emit(renderEffect(corev1.ScreenTarget_SCREEN_TARGET_HOME, corev1.ScreenTarget_SCREEN_TARGET_PROJECT))
		}

	case *corev1.Event_ProjectAdd:
		if k.ProjectAdd != nil {
			path := k.ProjectAdd.Path
			machineID := st.DirPicker.MachineID
			if machineID == "" {
				for _, mach := range st.World.Machines {
					if lists.MachineReachable(mach) {
						machineID = mach.ID
						break
					}
				}
			}
			op := lists.PlanAddProject(&st.World, machineID, path)
			if op != nil {
				var settings *v1.ProjectSettings
				if op.SetSettings != nil {
					settings = lists.ApplySettings(lists.SavedSettings(st.World.Prefs, path), op.SetSettings, st.Cal.NowMs)
				}
				emit(c.applyAndEmitPrefsOp(path, op, settings))
			}
			st.DirPicker.Open = false
			st.StandaloneProjectKey = path
			emit(renderEffect(corev1.ScreenTarget_SCREEN_TARGET_HOME, corev1.ScreenTarget_SCREEN_TARGET_PROJECT, corev1.ScreenTarget_SCREEN_TARGET_NEW_SESSION))
		}

	case *corev1.Event_ProjectLoadMoreEnded:
		if k.ProjectLoadMoreEnded != nil {
			key := k.ProjectLoadMoreEnded.ProjectKey
			page := lists.BuildProjectPage(&st.World, key, int(^uint(0)>>1), st.Cal)
			if page.WantPage && !st.World.LoadingMoreSessions {
				st.World.LoadingMoreSessions = true
				emit(&corev1.Effect{
					Effect: &corev1.Effect_FetchSessionsPage{
						FetchSessionsPage: &corev1.FetchSessionsPageEffect{
							RequestId: m.nextID("page-"),
							Cursor:    st.World.SessionsCursor,
							Limit:     lists.EndedPage,
						},
					},
				})
			}
			emit(renderEffect(corev1.ScreenTarget_SCREEN_TARGET_PROJECT))
		}

	case *corev1.Event_ProjectOpenDirPicker:
		if k.ProjectOpenDirPicker != nil {
			dp := k.ProjectOpenDirPicker
			machID := dp.MachineId
			if machID == "" {
				for _, mach := range st.World.Machines {
					if lists.MachineReachable(mach) {
						machID = mach.ID
						break
					}
				}
			}
			path := dp.Path
			if path == "" {
				if mach := m.findMachine(machID); mach != nil && mach.Meta != nil {
					path = mach.Meta.Home
				}
			}
			reqID := m.nextID("dir-")
			st.DirPicker = screens.DirPickerState{
				Open:            true,
				ForNewTask:      dp.ForNewTask,
				MachineID:       machID,
				CurrentPath:     path,
				Loading:         machID != "",
				ActiveRequestID: reqID,
			}
			if machID != "" {
				emit(&corev1.Effect{
					Effect: &corev1.Effect_SendCommand{
						SendCommand: &corev1.SendCommandEffect{
							RequestId: reqID,
							MachineId: machID,
							Command: &v1.Command{
								Kind: &v1.Command_ListDirectories{
									ListDirectories: &v1.ListDirectoriesRequest{Path: path},
								},
							},
						},
					},
				})
			}
			emit(renderEffect(corev1.ScreenTarget_SCREEN_TARGET_PROJECT))
		}

	case *corev1.Event_ProjectDirNavigate:
		if k.ProjectDirNavigate != nil {
			dn := k.ProjectDirNavigate
			machID := dn.MachineId
			if machID == "" {
				machID = st.DirPicker.MachineID
			}
			path := dn.Path
			if path == "" {
				if mach := m.findMachine(machID); mach != nil && mach.Meta != nil {
					path = mach.Meta.Home
				}
			}
			reqID := m.nextID("dir-")
			st.DirPicker.MachineID = machID
			st.DirPicker.CurrentPath = path
			st.DirPicker.Loading = machID != ""
			st.DirPicker.Error = ""
			st.DirPicker.ActiveRequestID = reqID
			if machID != "" {
				emit(&corev1.Effect{
					Effect: &corev1.Effect_SendCommand{
						SendCommand: &corev1.SendCommandEffect{
							RequestId: reqID,
							MachineId: machID,
							Command: &v1.Command{
								Kind: &v1.Command_ListDirectories{
									ListDirectories: &v1.ListDirectoriesRequest{Path: path},
								},
							},
						},
					},
				})
			}
			emit(renderEffect(corev1.ScreenTarget_SCREEN_TARGET_PROJECT))
		}

	case *corev1.Event_ProjectCloseDirPicker:
		st.DirPicker.Open = false
		emit(renderEffect(corev1.ScreenTarget_SCREEN_TARGET_PROJECT))

	case *corev1.Event_NewSessionOpen:
		if k.NewSessionOpen != nil {
			key := k.NewSessionOpen.ProjectKey
			if key == "" {
				key = m.activeNewSessionKey("")
			} else {
				st.StandaloneProjectKey = key
			}
			if key != "" {
				d := m.ensureDraft(key)
				res := m.resolveDraftConfig(key, d)
				if res.Found {
					if !d.ConfigInitialized {
						d.Config = res.Config
						d.ConfigInitialized = true
					}
					for _, eff := range m.maybeFetchMachineConfig(d.Config.MachineID, d.Config.Agent) {
						emit(eff)
					}
				}
			}
			emit(renderEffect(corev1.ScreenTarget_SCREEN_TARGET_NEW_SESSION))
		}

	case *corev1.Event_NewSessionSelectProject:
		if k.NewSessionSelectProject != nil {
			key := k.NewSessionSelectProject.ProjectKey
			st.StandaloneProjectKey = key
			if key != "" {
				d := m.ensureDraft(key)
				res := m.resolveDraftConfig(key, d)
				if res.Found {
					if !d.ConfigInitialized {
						d.Config = res.Config
						d.ConfigInitialized = true
					}
					for _, eff := range m.maybeFetchMachineConfig(d.Config.MachineID, d.Config.Agent) {
						emit(eff)
					}
				}
			}
			emit(renderEffect(corev1.ScreenTarget_SCREEN_TARGET_NEW_SESSION))
		}

	case *corev1.Event_NewSessionSelectMachine:
		if k.NewSessionSelectMachine != nil {
			key := m.activeNewSessionKey("")
			if key != "" {
				d := m.ensureDraft(key)
				if !d.ConfigInitialized {
					d.Config = m.resolveDraftConfig(key, d).Config
				}
				mach := m.findMachine(k.NewSessionSelectMachine.MachineId)
				var meta *v1.MachineMeta
				if mach != nil {
					meta = mach.Meta
				}
				agents := lists.MachineAgents(meta)
				if d.Config.MachineID != k.NewSessionSelectMachine.MachineId && d.AttachmentCount > 0 {
					d.AttachmentsMoved = true
				}
				d.Config = lists.SwitchMachine(d.Config, k.NewSessionSelectMachine.MachineId, agents)
				d.Config.Cwd = key
				d.ConfigInitialized = true
				for _, eff := range m.maybeFetchMachineConfig(d.Config.MachineID, d.Config.Agent) {
					emit(eff)
				}
			}
			emit(renderEffect(corev1.ScreenTarget_SCREEN_TARGET_NEW_SESSION))
		}

	case *corev1.Event_NewSessionSelectAgent:
		if k.NewSessionSelectAgent != nil {
			key := m.activeNewSessionKey("")
			if key != "" {
				d := m.ensureDraft(key)
				base := m.resolveDraftConfig(key, d)
				if !d.ConfigInitialized {
					d.Config = base.Config
				}
				var projCfg *lists.StartConfig
				if saved := lists.SavedSettings(st.World.Prefs, key); saved != nil {
					r := lists.ResolveProjectStart(&st.World, key, "", k.NewSessionSelectAgent.Agent, nil, false)
					if r.Found {
						projCfg = &r.Config
					}
				}
				d.Config = lists.SwitchAgent(d.Config, projCfg, k.NewSessionSelectAgent.Agent)
				d.Config.Cwd = key
				d.ConfigInitialized = true
				d.AgentOverride = k.NewSessionSelectAgent.Agent
				for _, eff := range m.maybeFetchMachineConfig(d.Config.MachineID, d.Config.Agent) {
					emit(eff)
				}
			}
			emit(renderEffect(corev1.ScreenTarget_SCREEN_TARGET_NEW_SESSION))
		}

	case *corev1.Event_NewSessionSelectModel:
		if k.NewSessionSelectModel != nil {
			key := m.activeNewSessionKey("")
			if key != "" {
				d := m.ensureDraft(key)
				if !d.ConfigInitialized {
					d.Config = m.resolveDraftConfig(key, d).Config
				}
				sel := k.NewSessionSelectModel
				mc := st.MachineModels[screens.MachineAgentKey{
					MachineID: d.Config.MachineID,
					Agent:     lists.EffectiveAgent(d.Config.Agent),
				}]
				if sel.Model == "" {
					d.Config.Model = ""
					d.Config.Effort = sel.Effort
				} else if mc != nil && mc.Response != nil {
					if mi := lists.FindModel(mc.Response.Models, sel.Model); mi != nil {
						d.Config = lists.PickModel(d.Config, mi)
						if sel.Effort != "" {
							d.Config.Effort = sel.Effort
						}
					} else {
						d.Config.Model = sel.Model
						d.Config.Effort = sel.Effort
					}
				} else {
					d.Config.Model = sel.Model
					d.Config.Effort = sel.Effort
				}
				if sel.StandardContext != nil {
					d.Config.StandardContext = *sel.StandardContext
				}
				d.ConfigInitialized = true
			}
			emit(renderEffect(corev1.ScreenTarget_SCREEN_TARGET_NEW_SESSION))
		}

	case *corev1.Event_NewSessionSetPrompt:
		if k.NewSessionSetPrompt != nil {
			key := m.activeNewSessionKey("")
			if key != "" {
				d := m.ensureDraft(key)
				if !d.ConfigInitialized && (k.NewSessionSetPrompt.Prompt != "" || k.NewSessionSetPrompt.AttachmentCount > 0) {
					if res := m.resolveDraftConfig(key, d); res.Found {
						d.Config = res.Config
						d.ConfigInitialized = true
					}
				}
				d.Prompt = k.NewSessionSetPrompt.Prompt
				d.AttachmentCount = k.NewSessionSetPrompt.AttachmentCount
				d.AttachmentsMoved = k.NewSessionSetPrompt.AttachmentsMoved
			}
			emit(renderEffect(corev1.ScreenTarget_SCREEN_TARGET_NEW_SESSION))
		}

	case *corev1.Event_NewSessionRetryModels:
		key := ""
		if k.NewSessionRetryModels != nil {
			key = k.NewSessionRetryModels.ProjectKey
		}
		key = m.activeNewSessionKey(key)
		if key != "" {
			d := m.ensureDraft(key)
			if !d.ConfigInitialized {
				d.Config = m.resolveDraftConfig(key, d).Config
			}
			delete(st.MachineModels, screens.MachineAgentKey{
				MachineID: d.Config.MachineID,
				Agent:     lists.EffectiveAgent(d.Config.Agent),
			})
			for _, eff := range m.maybeFetchMachineConfig(d.Config.MachineID, d.Config.Agent) {
				emit(eff)
			}
		}
		emit(renderEffect(corev1.ScreenTarget_SCREEN_TARGET_NEW_SESSION))

	case *corev1.Event_NewSessionRetrySaveConfig:
		key := ""
		if k.NewSessionRetrySaveConfig != nil {
			key = k.NewSessionRetrySaveConfig.ProjectKey
		}
		key = m.activeNewSessionKey(key)
		if key != "" {
			d := m.ensureDraft(key)
			res := m.resolveDraftConfig(key, d)
			cfg := d.Config
			if !d.ConfigInitialized {
				cfg = res.Config
			}
			var projCfg *lists.StartConfig
			if res.Found {
				projCfg = &res.Config
			}
			if op := lists.PlanStartSettings(cfg, projCfg, lists.SavedSettings(st.World.Prefs, key)); op != nil {
				d.ConfigSaving = true
				d.ConfigSaveError = ""
				var settings *v1.ProjectSettings
				if op.SetSettings != nil {
					settings = lists.ApplySettings(lists.SavedSettings(st.World.Prefs, key), op.SetSettings, st.Cal.NowMs)
				}
				emit(c.applyAndEmitPrefsOp(key, op, settings))
			}
		}
		emit(renderEffect(corev1.ScreenTarget_SCREEN_TARGET_NEW_SESSION))

	case *corev1.Event_NewSessionSubmit:
		var reqKey string
		if k.NewSessionSubmit != nil {
			reqKey = k.NewSessionSubmit.ProjectKey
		}
		key := m.activeNewSessionKey(reqKey)
		if key != "" {
			vm := screens.BuildNewSession(st, reqKey)
			if vm.CanStart {
				d := m.ensureDraft(key)
				reqID := d.ActiveStartRequestID
				if reqID == "" || !d.Frozen {
					reqID = m.nextID("create-")
				}
				d.Busy = true
				d.StartError = ""
				d.DiskFull = false
				d.ActiveStartRequestID = reqID
				c.startKeys[reqID] = key

				cfg := d.Config
				if !d.ConfigInitialized {
					cfg = m.resolveDraftConfig(key, d).Config
				}
				res := lists.ResolveProjectStart(&st.World, key, "", v1.Agent_AGENT_UNSPECIFIED, nil, false)
				var projCfg *lists.StartConfig
				if res.Found {
					projCfg = &res.Config
				}
				if op := lists.PlanStartSettings(cfg, projCfg, lists.SavedSettings(st.World.Prefs, key)); op != nil {
					var settings *v1.ProjectSettings
					if op.SetSettings != nil {
						settings = lists.ApplySettings(lists.SavedSettings(st.World.Prefs, key), op.SetSettings, st.Cal.NowMs)
					}
					emit(c.applyAndEmitPrefsOp(key, op, settings))
				}

				emit(&corev1.Effect{
					Effect: &corev1.Effect_SendCommand{
						SendCommand: &corev1.SendCommandEffect{
							RequestId: reqID,
							MachineId: vm.Config.MachineId,
							Command: &v1.Command{
								Kind: &v1.Command_CreateSession{
									CreateSession: &v1.CreateSessionRequest{
										Cwd:             key,
										Prompt:          d.Prompt,
										Agent:           vm.Config.Agent,
										Model:           vm.Config.Model,
										Effort:          vm.Config.Effort,
										StandardContext: vm.Config.StandardContext,
									},
								},
							},
						},
					},
				})
			}
		}
		emit(renderEffect(corev1.ScreenTarget_SCREEN_TARGET_NEW_SESSION))

	case *corev1.Event_NewSessionAbandonStart:
		var reqKey string
		if k.NewSessionAbandonStart != nil {
			reqKey = k.NewSessionAbandonStart.ProjectKey
		}
		key := m.activeNewSessionKey(reqKey)
		if key != "" {
			if d := st.NewSessions[key]; d != nil {
				reqID := d.ActiveStartRequestID
				d.Frozen = false
				d.Expired = false
				d.UnknownStart = false
				d.StartError = ""
				d.ActiveStartRequestID = ""
				if reqID != "" {
					delete(c.startKeys, reqID)
					emit(&corev1.Effect{
						Effect: &corev1.Effect_AbandonRequest{
							AbandonRequest: &corev1.AbandonRequestEffect{
								RequestId: reqID,
							},
						},
					})
				}
			}
		}
		emit(renderEffect(corev1.ScreenTarget_SCREEN_TARGET_NEW_SESSION))
	}

	batch.Effects = effects
	return batch
}

func (c *Core) applyAndEmitPrefsOp(path string, op *lists.FullPrefsOp, settings *v1.ProjectSettings) *corev1.Effect {
	opID := c.model.nextID("op-")
	c.opPaths[opID] = path
	c.model.state.PrefsPendingPath[path] = true
	delete(c.model.state.PrefsSaveErrorByPath, path)
	next := lists.ApplyPrefsOp(c.model.state.World.Prefs, op, opID, c.model.state.Cal.NowMs)
	c.model.state.World.Prefs = next
	return &corev1.Effect{
		Effect: &corev1.Effect_SaveProjectPrefs{
			SaveProjectPrefs: &corev1.SaveProjectPrefsEffect{
				OpId:      opID,
				Kind:      mapPrefsOpKind(op.Kind),
				Path:      path,
				Settings:  settings,
				AtMs:      c.model.state.Cal.NowMs,
				NextPrefs: next,
			},
		},
	}
}
