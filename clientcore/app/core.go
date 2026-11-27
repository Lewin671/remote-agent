// Package app hosts the shared Core state machine and Dispatch/View entrypoints
// exposed to Web, Android, and iOS shells.
package app

import (
	"errors"

	"github.com/Lewin671/remote-agent/clientcore/domain/images"
	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/outcome"
	"github.com/Lewin671/remote-agent/clientcore/domain/usage"
	"github.com/Lewin671/remote-agent/clientcore/domain/world"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/screens"
	"github.com/Lewin671/remote-agent/clientcore/sync"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

// Core wraps the unified sync Engine and screen ViewModel builders behind a single Dispatch/View API.
type Core struct {
	m               *Model
	clientCfg       *clientInitConfig
	nextSyncOpSeq   uint64
	nextRelayOpSeq  uint64
	nextLegSeq      uint64
	nextSecOpID     uint64
	nextSecActionID uint64
	nextSecEffectOp uint64
}

// New constructs a Core instance with fresh sync state and screen caches.
func New() *Core {
	c := &Core{}
	c.initModel()
	return c
}

func (c *Core) initModel() {
	c.m = newModel()
	if c.nextSyncOpSeq > 0 {
		c.m.sync.SetNextOpSeq(c.nextSyncOpSeq)
	}
	if c.nextRelayOpSeq > 0 {
		c.m.sync.SetNextRelayOpSeq(c.nextRelayOpSeq)
	}
	if c.nextLegSeq > 0 {
		c.m.sync.SetNextLegSeq(c.nextLegSeq)
	}
	if c.nextSecOpID > 0 {
		c.m.sync.SecurityEngine().SetNextOpID(c.nextSecOpID)
	}
	c.m.state.DraftViewFn = func(scope *corev1.DraftScope) *corev1.AttachmentDraftView {
		return c.buildAttachmentDraftView(scope, false)
	}
	c.m.sync.SetSecurityPollObserver(c)
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

	case *corev1.Screen_Connection:
		w := c.m.sync.World()
		needDevices := w.World.ManagerName == "" && w.AuthorityDeviceID != ""
		return &corev1.ViewModel{
			View: &corev1.ViewModel_Connection{
				Connection: screens.BuildConnection(c.m.state, c.buildSyncState(sync.SyncStateOpts{IncludeDevices: needDevices})),
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
		if vm, handled, err := c.viewSecurity(screen); handled {
			return vm, err
		}
		return nil, errors.New("app: unsupported screen")
	}
}

// buildSyncState builds an internal SyncState snapshot on demand with localized
// Text fields populated by screens from structured sync reasons.
func (c *Core) buildSyncState(opts sync.SyncStateOpts) *world.SyncState {
	return screens.ApplySyncStateTexts(
		c.m.sync.BuildSyncState(opts),
		c.m.sync.World(),
		c.m.sync.ConnectionStep(),
		c.m.sync.DevicesError(),
	)
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
		nowMs := ev.GetNowMs()
		if nowMs <= 0 {
			nowMs = c.m.sync.NowMs()
		}
		screens.DecorateSecurityEffects(effects, syncRes.SecurityWorkflowMeta, syncRes.SecurityNotifyOutcomes, syncRes.SecurityBootstrapError, nowMs)
		decorateSessionImageEffects(effects)
		decorateTerminalStateEffects(effects)
		effects = c.processSecurityEffects(effects, syncRes.SecurityWorkflowMeta, syncRes.SecurityBootstrapError, nowMs)
		if r := renderEffect(syncRes.Dirty); r != nil {
			effects = append(effects, r)
		}
		effects = c.appendSettingsRender(effects, syncRes.Dirty)
		effects = c.appendUsageRender(effects, syncRes.Dirty)
		effects = c.appendSessionRenderTarget(syncRes.Dirty, effects)
		effects = c.appendAttachmentDraftRenderTarget(effects)
		effects = c.m.mergeScheduleRender(effects)
		effects = c.appendSecurityRender(effects, syncRes.Dirty)
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
	nowMs := ev.GetNowMs()
	if nowMs <= 0 {
		nowMs = c.m.sync.NowMs()
	}
	screens.DecorateSecurityEffects(effects, stepRes.SecurityWorkflowMeta, stepRes.SecurityNotifyOutcomes, stepRes.SecurityBootstrapError, nowMs)
	decorateSessionImageEffects(effects)
	decorateTerminalStateEffects(effects)
	effects = c.processSecurityEffects(effects, stepRes.SecurityWorkflowMeta, stepRes.SecurityBootstrapError, nowMs)
	if r := renderEffect(stepRes.Dirty); r != nil {
		effects = append(effects, r)
	}
	effects = c.appendSettingsRender(effects, stepRes.Dirty)
	effects = c.appendUsageRender(effects, stepRes.Dirty)
	effects = c.appendSessionRenderTarget(stepRes.Dirty, effects)
	effects = c.appendAttachmentDraftRenderTarget(effects)
	effects = c.m.mergeScheduleRender(effects)
	effects = c.appendSecurityRender(effects, stepRes.Dirty)
	return &corev1.EffectBatch{Effects: effects}
}

func decorateSessionImageEffects(effects []*corev1.Effect) {
	for _, eff := range effects {
		img := eff.GetSessionImageLoaded()
		if img == nil {
			continue
		}
		if img.Status == corev1.SessionImageLoadStatus_SESSION_IMAGE_LOAD_STATUS_OK {
			if img.MimeType == "" {
				img.MimeType = images.FormatMime(img.Format)
			}
			continue
		}
		if img.ErrorText != nil {
			continue
		}
		switch img.Status {
		case corev1.SessionImageLoadStatus_SESSION_IMAGE_LOAD_STATUS_BAD:
			switch {
			case img.RetiredHistory || img.ErrorCode == "retired_history":
				img.ErrorText = text.Msg(text.KeyErrorsThisHistoryUsesAnOldEncryptionFormatAndCan)
			case img.ErrorCode == "format_mismatch":
				img.ErrorText = text.Msg(text.KeyErrorsImageContentDoesNotMatchItsFormat)
			case img.ErrorCode == "lease":
				img.ErrorText = text.Msg(text.KeyErrorsRemoteControlExpiredSavingChangesSessionControlAndNew)
			case img.ErrorCode == "unavailable":
				img.ErrorText = text.Msg(text.KeyErrorsWaitingForTheOwningComputerToUpdateTheCurrentRemote)
			default:
				img.ErrorText = text.Msg(text.KeyErrorsInvalidCiphertextSignatureKeyOrContentVerificationFailed)
			}
		case corev1.SessionImageLoadStatus_SESSION_IMAGE_LOAD_STATUS_ERROR:
			switch img.ErrorCode {
			case "not_found":
				img.ErrorText = text.Msg(text.KeyCommonCannotFindThisSession)
			case "deadline_exceeded", "timeout":
				img.ErrorText = text.Msg(text.KeyErrorsConnectionTimedOut)
			case "network", "unavailable", "unresolved_host":
				img.ErrorText = text.Msg(text.KeyConnectionNoServer)
			case "unauthenticated", "permission_denied":
				img.ErrorText = text.Msg(text.KeyErrorsTheServerDoesNotRecognizeThisDeviceItMay)
			default:
				img.ErrorText = text.Msg(text.KeyErrorsCommandFailed)
			}
		}
	}
}

func decorateTerminalStateEffects(effects []*corev1.Effect) {
	for _, eff := range effects {
		st := eff.GetTerminalStateChanged()
		if st == nil {
			continue
		}
		if st.ReasonText == nil {
			switch st.State {
			case corev1.TerminalLifecycleState_TERMINAL_LIFECYCLE_STATE_REBUILD:
				st.ReasonText = text.Msg(text.KeyErrorsTerminalConnectionClosed)
			case corev1.TerminalLifecycleState_TERMINAL_LIFECYCLE_STATE_CLOSED:
				switch st.ReasonCode {
				case "conflict":
					st.ReasonText = text.Msg(text.KeyErrorsTerminalFramesAreOutOfOrder)
				case "replay_overflow":
					st.ReasonText = text.Msg(text.KeyTerminalReplayOverflow)
				case "backlog":
					st.ReasonText = text.Msg(text.KeyErrorsTerminalOutputBacklogReconnectForTheLatestDisplay)
				case "input_backlog":
					st.ReasonText = text.Msg(text.KeyErrorsTerminalInputBacklogReconnect)
				case "not_found":
					st.ReasonText = text.Msg(text.KeyCommonCannotFindThisSession)
				case "canceled":
					st.ReasonText = text.Msg(text.KeyErrorsTerminalConnectionCanceled)
				case "closed":
					st.ReasonText = text.Msg(text.KeyErrorsTerminalConnectionClosed)
				case "connect_error":
					st.ReasonText = text.Msg(text.KeyErrorsCannotConnectToTerminal)
				case "timeout":
					st.ReasonText = text.Msg(text.KeyErrorsTerminalConnectionTimedOutReconnect)
				case "lease":
					st.ReasonText = text.Msg(text.KeyErrorsRemoteControlExpiredSavingChangesSessionControlAndNew)
				case "unavailable":
					st.ReasonText = text.Msg(text.KeyErrorsWaitingForTheOwningComputerToUpdateTheCurrentRemote)
				case "invalid", "untrusted":
					st.ReasonText = text.Msg(text.KeyErrorsInvalidCiphertextSignatureKeyOrContentVerificationFailed)
				default:
					st.ReasonText = text.Msg(text.KeyErrorsTerminalConnectionDropped)
				}
			}
		} else if st.State == corev1.TerminalLifecycleState_TERMINAL_LIFECYCLE_STATE_CLOSED &&
			st.ReasonCode == "socket_error" && st.ReasonText.Key == "" && st.ReasonText.Literal != "" {
			st.ReasonText = text.Msg(text.KeyErrorsTerminalConnectionDroppedReason, text.Str("reason", st.ReasonText.Literal))
		}
		if st.OpenSettled && !st.OpenOk && st.OpenError == nil {
			st.OpenError = st.ReasonText
		}
	}
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
	if len(res.DeliveredTermHistoryPages) > 0 && c.m.termHistories != nil {
		prev, thCtx := c.m.sync.BeginStep()
		for _, item := range res.DeliveredTermHistoryPages {
			if item.TerminalID == "" || item.Page == nil {
				continue
			}
			if h := c.m.termHistories[item.TerminalID]; h != nil {
				tr := h.ReceivePage(item.Page)
				effects = append(effects, c.applyTermHistoryTransition(thCtx, item.TerminalID, h, tr)...)
			}
		}
		thRes := c.m.sync.EndStep(prev, thCtx)
		effects = append(effects, thRes.Effects...)
	}
	for _, eff := range res.Effects {
		if req := eff.GetUsageCacheRequest(); req != nil && req.DeleteCache && req.MachineId != "" {
			c.forgetUsageMachine(req.MachineId)
		}
	}
	if res.Dirty.Home && c.m.sessions != nil && len(c.m.sessions.opened) > 0 {
		for sid := range c.m.sessions.opened {
			if sess := c.m.sync.SessionEntry(sid); sess != nil && sess.MachineID != "" {
				c.maybeHydrateUsageCache(sess.MachineID, &effects)
			}
		}
	}
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
				me.ErrorText = screens.ReasonText(sc.ErrorReason)
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
				se.ErrorText = screens.ReasonText(sc.ErrorReason)
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
			errText := screens.ReasonText(sc.ErrorReason)
			if !ok {
				if errCode == "" {
					errCode = "failed"
				}
				if errText == nil {
					errText = text.Msg(text.KeyErrorsCommandFailed)
				}
			}
			effects = append(effects, &corev1.Effect{
				Effect: &corev1.Effect_RunMachineCommandResult{
					RunMachineCommandResult: &corev1.RunMachineCommandResultEffect{
						OpId:      meta.opID,
						Ok:        ok,
						ErrorCode: errCode,
						ErrorText: errText,
						Result:    sc.Result,
					},
				},
			})

		case cmdKindPingMachine:
			delete(c.m.pingReqByOp, meta.opID)
			ok := sc.Category == outcome.Executed && sc.ErrorCode == ""
			errCode := sc.ErrorCode
			errText := screens.ReasonText(sc.ErrorReason)
			if !ok {
				if errCode == "" {
					errCode = "failed"
				}
				if errText == nil {
					errText = text.Msg(text.KeyErrorsCommandFailed)
				}
			}
			effects = append(effects, &corev1.Effect{
				Effect: &corev1.Effect_PingMachineResult{
					PingMachineResult: &corev1.PingMachineResultEffect{
						OpId:      meta.opID,
						Ok:        ok,
						ErrorCode: errCode,
						ErrorText: errText,
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
				dp.ErrorText = screens.ReasonText(sc.ErrorReason)
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
				hasCoreStart := c.m.attachments != nil && c.m.attachments.newSessionStarts[meta.projectKey] != nil
				if !hasCoreStart {
					d.Prompt = ""
					d.AttachmentCount = 0
					d.AttachmentsMoved = false
				}
				d.ResetOnNextOpen = true

				savePrev, saveCtx := c.m.sync.BeginStep()
				if sess != nil && sess.Id != "" {
					c.m.sync.SeedSession(saveCtx, sc.MachineID, sess)
				}
				cfg := d.Config
				if cfg.Cwd == "" {
					cfg.Cwd = meta.projectKey
				}
				saved := lists.SavedSettings(c.m.state.World.Prefs, cfg.Cwd)
				op := lists.PlanStartSettings(cfg, &cfg, saved)
				if c.m.state.World.MachinesListed && saved != nil && saved.MachineId != "" && saved.MachineId != cfg.MachineID {
					savedMac := c.m.findMachine(saved.MachineId)
					if savedMac == nil || savedMac.Archived {
						if op == nil {
							agent := lists.EffectiveAgent(cfg.Agent)
							op = &lists.FullPrefsOp{
								Kind: lists.PrefsOpSetSettings,
								Path: cfg.Cwd,
								SetSettings: &lists.PrefsSetSettingsOp{
									Path:          cfg.Cwd,
									MachineID:     cfg.MachineID,
									UpdateMachine: true,
									Agent:         &agent,
									KeepExisting:  true,
								},
							}
						} else if op.SetSettings != nil {
							op.SetSettings.MachineID = cfg.MachineID
							op.SetSettings.UpdateMachine = true
						}
					}
				}
				if op != nil {
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
				d.StartErrorText = screens.ReasonText(sc.ErrorReason)
				d.StartError = sc.ErrorCode
			} else {
				d.Frozen = false
				d.Expired = false
				d.UnknownStart = false
				d.ActiveStartRequestID = ""
				d.DiskFull = sc.ErrorCode == "disk_full"
				d.StartErrorText = screens.ReasonText(sc.ErrorReason)
				d.StartError = sc.ErrorCode
				if d.StartError == "" && d.StartErrorText == nil {
					d.StartError = "failed"
				}
			}
			c.onNewSessionCreateSettled(meta.projectKey, sc, &effects)
			if sc.Category == outcome.Executed && c.m.activeNewSessionPagingKey == meta.projectKey {
				c.m.activeNewSessionPagingKey = ""
			}
			dirty.Add(true, true, true)
		default:
			effects = append(effects, c.onOtherSettingsCmdSettled(sc, meta)...)
		}
	}

	if res.Dirty.NewSession || dirty.NewSession || (c.m.activeNewSessionPagingKey != "" && res.Dirty.Home) {
		extraPrev, extraCtx := c.m.sync.BeginStep()
		for key, d := range c.m.state.NewSessions {
			if d == nil {
				continue
			}
			if !(res.Dirty.NewSession || dirty.NewSession) && key != c.m.activeNewSessionPagingKey {
				continue
			}
			psr := c.m.resolveDraftConfig(key, d)
			c.m.syncDraftAutoSupport(key, psr)
			if d.Config.MachineID != "" {
				c.m.maybeFetchMachineConfig(extraCtx, d.Config.MachineID, d.Config.Agent)
			}
			if key == c.m.activeNewSessionPagingKey {
				c.maybeAutoPageNewSession(extraCtx, key, psr)
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
	c.reconcilePendingNotifTap(&effects)
	c.reconcileSharedFileDownloads(&effects, &dirty)
	c.reconcileAttachments(&effects, &dirty)
	if res.Dirty.Home || res.Dirty.SessionData || dirty.Home || dirty.SessionData {
		c.reconcileSessionAndHomeTransient(&effects, &dirty)
	}

	return effects, dirty
}

func (c *Core) maybeAutoPageNewSession(ctx *sync.StepContext, key string, psr lists.ProjectStartResult) {
	if key == "" {
		return
	}
	c.m.activeNewSessionPagingKey = key
	if !psr.Settled && c.m.state.World.HasMoreSessions {
		c.m.sync.RequestMoreSessions(ctx)
	}
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
		if c.m.sec != nil {
			c.m.sec.dirty.markAll()
		}

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

	case *corev1.Event_HomeResolveApproval:
		if eff := c.handleHomeResolveApproval(ctx, k.HomeResolveApproval); len(eff) > 0 {
			effects = append(effects, eff...)
		}

	case *corev1.Event_HomeEditApprovalDeny:
		c.handleHomeEditApprovalDeny(ctx, k.HomeEditApprovalDeny)

	case *corev1.Event_HomeAnswerQuestion:
		if eff := c.handleHomeAnswerQuestion(ctx, k.HomeAnswerQuestion); len(eff) > 0 {
			effects = append(effects, eff...)
		}

	case *corev1.Event_HomeRetryAttention:
		if eff := c.handleHomeRetryAttention(ctx, k.HomeRetryAttention); len(eff) > 0 {
			effects = append(effects, eff...)
		}

	case *corev1.Event_HomeAbandonAttention:
		c.handleHomeAbandonAttention(ctx, k.HomeAbandonAttention)

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
				c.maybeAutoPageNewSession(ctx, key, psr)
			}
			ctx.Dirty.Add(false, false, true)
		}

	case *corev1.Event_NewSessionClose:
		if k.NewSessionClose != nil && k.NewSessionClose.ProjectKey != "" &&
			k.NewSessionClose.ProjectKey == c.m.activeNewSessionPagingKey {
			c.m.activeNewSessionPagingKey = ""
		}

	case *corev1.Event_ResolveNotificationTap:
		effects = append(effects, c.handleResolveNotificationTap(ctx, k.ResolveNotificationTap)...)

	case *corev1.Event_NewSessionSelectProject:
		if k.NewSessionSelectProject != nil && k.NewSessionSelectProject.ProjectKey != "" {
			st.StandaloneProjectKey = k.NewSessionSelectProject.ProjectKey
			d := c.m.ensureDraft(st.StandaloneProjectKey)
			psr := c.m.resolveDraftConfig(st.StandaloneProjectKey, d)
			c.m.syncDraftAutoSupport(st.StandaloneProjectKey, psr)
			if d.Config.MachineID != "" {
				c.m.maybeFetchMachineConfig(ctx, d.Config.MachineID, d.Config.Agent)
			}
			c.maybeAutoPageNewSession(ctx, st.StandaloneProjectKey, psr)
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
				c.maybeAutoPageNewSession(ctx, key, psr)
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
				imgIDs, fileIDs, blocked := c.prepareNewSessionAttachments(ctx, key, cfg.MachineID, "", &effects)
				if blocked {
					ctx.Dirty.Add(false, false, true)
					break
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

	case *corev1.Event_TerminalHistory:
		effects = append(effects, c.handleTerminalHistoryEvent(ctx, k.TerminalHistory)...)

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
							ErrorText: text.Msg(text.KeyErrorsInvalidCommand),
						},
					},
				})
				break
			}
			reqID, _ := c.m.sync.SubmitCommand(ctx, sync.SubmitCommandParams{
				MachineID:                 act.MachineId,
				SessionID:                 act.SessionId,
				Command:                   act.Command,
				SkipConnectivityPreflight: act.SkipConnectivityPreflight,
			})
			if reqID == "" {
				effects = append(effects, &corev1.Effect{
					Effect: &corev1.Effect_RunMachineCommandResult{
						RunMachineCommandResult: &corev1.RunMachineCommandResultEffect{
							OpId:      act.OpId,
							Ok:        false,
							ErrorCode: "failed",
							ErrorText: text.Msg(text.KeyErrorsCommandFailed),
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

	case *corev1.Event_PingMachine:
		if act := k.PingMachine; act != nil && act.OpId != 0 {
			if prevReq, exists := c.m.pingReqByOp[act.OpId]; exists {
				delete(c.m.pingReqByOp, act.OpId)
				delete(c.m.pendingCmdMeta, prevReq)
				c.m.sync.CancelCommand(ctx, prevReq)
			}
			if act.MachineId == "" {
				effects = append(effects, &corev1.Effect{
					Effect: &corev1.Effect_PingMachineResult{
						PingMachineResult: &corev1.PingMachineResultEffect{
							OpId:      act.OpId,
							Ok:        false,
							ErrorCode: "invalid_argument",
							ErrorText: text.Msg(text.KeyErrorsInvalidCommand),
						},
					},
				})
				break
			}
			reqID, _ := c.m.sync.SubmitCommand(ctx, sync.SubmitCommandParams{
				MachineID:                 act.MachineId,
				Command:                   &v1.Command{Kind: &v1.Command_GetSettings{GetSettings: &v1.GetSettingsRequest{}}},
				SkipConnectivityPreflight: true,
			})
			if reqID == "" {
				effects = append(effects, &corev1.Effect{
					Effect: &corev1.Effect_PingMachineResult{
						PingMachineResult: &corev1.PingMachineResultEffect{
							OpId:      act.OpId,
							Ok:        false,
							ErrorCode: "failed",
							ErrorText: text.Msg(text.KeyErrorsCommandFailed),
						},
					},
				})
				break
			}
			c.m.pendingCmdMeta[reqID] = cmdMeta{
				kind:      cmdKindPingMachine,
				machineID: act.MachineId,
				opID:      act.OpId,
			}
			c.m.pingReqByOp[act.OpId] = reqID
		}

	case *corev1.Event_CancelPingMachine:
		if act := k.CancelPingMachine; act != nil && act.OpId != 0 {
			if reqID, ok := c.m.pingReqByOp[act.OpId]; ok {
				delete(c.m.pingReqByOp, act.OpId)
				delete(c.m.pendingCmdMeta, reqID)
				c.m.sync.CancelCommand(ctx, reqID)
			}
		}
	default:
		if secEffects, handled := c.dispatchSecurityUI(ctx, ev); handled {
			effects = append(effects, secEffects...)
			break
		}
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
