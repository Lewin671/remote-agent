package app

import (
	"slices"
	"strings"

	"github.com/Lewin671/remote-agent/clientcore/domain/usage"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/screens"
	"github.com/Lewin671/remote-agent/clientcore/security"
	"github.com/Lewin671/remote-agent/clientcore/sync"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

const (
	internalSecActionBase       uint64 = 1 << 60
	exitPushUnregisterTimerKey         = "exit-push-unregister"
	exitPushUnregisterTimeoutMs int64  = 3_000
)

// Static init-time client configuration preserved across in-place account exit resets.
// Runtime state (locale, foreground, network) is read directly from the outgoing Model/Engine.
type clientInitConfig struct {
	platform              string
	deviceDescription     string
	inlineCrypto          bool
	defaultServer         string
	requestStoragePersist bool
}

type securityDirtyFlags struct {
	AppRoot         bool
	Onboarding      bool
	AddDevice       bool
	AccountSecurity bool
	Handoff         bool
	ExitConfirm     bool
}

func (d *securityDirtyFlags) markAll() {
	d.AppRoot = true
	d.Onboarding = true
	d.AddDevice = true
	d.AccountSecurity = true
	d.Handoff = true
	d.ExitConfirm = true
}

var (
	clearBanner screens.SecurityBannerState
	emptySec    = &corev1.SecurityActionEmpty{}
)

func secStr(v string) *corev1.SecurityStringAction { return &corev1.SecurityStringAction{Value: v} }
func secBool(v bool) *corev1.SecurityBoolAction    { return &corev1.SecurityBoolAction{Flag: v} }

func errBanner(t *corev1.Text) screens.SecurityBannerState {
	return screens.SecurityBannerState{Visible: true, Tone: corev1.SecurityBannerTone_SECURITY_BANNER_TONE_ERROR, Text: t}
}

func errBannerAct(t *corev1.Text, act corev1.SecurityBannerAction) screens.SecurityBannerState {
	return screens.SecurityBannerState{Visible: true, Tone: corev1.SecurityBannerTone_SECURITY_BANNER_TONE_ERROR, Text: t, Action: act}
}

func warnBanner(t *corev1.Text) screens.SecurityBannerState {
	return screens.SecurityBannerState{Visible: true, Tone: corev1.SecurityBannerTone_SECURITY_BANNER_TONE_WARN, Text: t}
}

func infoBanner(t *corev1.Text) screens.SecurityBannerState {
	return screens.SecurityBannerState{Visible: true, Tone: corev1.SecurityBannerTone_SECURITY_BANNER_TONE_INFO, Text: t}
}

type secDoneFn func(ctx *sync.StepContext, res *corev1.SecurityWorkflowResultEffect, meta security.WorkflowMeta, errTxt *corev1.Text) []*corev1.Effect

type secPendingPersist struct {
	opID    uint64
	kind    string // "create", "recovery", "handoff"
	server  string
	oldCode string
}

type secControllerState struct {
	ui                      screens.SecurityUIState
	dirty                   securityDirtyFlags
	pendingActions          map[uint64]secDoneFn
	pendingPersist          *secPendingPersist
	initialPairCode         string
	initialPairServer       string
	initialPairCommit       []byte
	initialPairRawLink      string
	hasInitialPairLink      bool
	refreshDevicesRequested bool
	lastRuntimeEmitted      bool
	lastRuntimeActive       bool
	lastRuntimeRemoved      bool
	lastRuntimeDeviceID     string
	lastRuntimeServer       string
}

func newSecControllerState() *secControllerState {
	return &secControllerState{
		ui: screens.SecurityUIState{
			Stage:                corev1.AppRootStage_APP_ROOT_STAGE_LOADING,
			Mode:                 corev1.OnboardingMode_ONBOARDING_MODE_ENTRY,
			JoinStep:             corev1.JoinPairingStep_JOIN_PAIRING_STEP_FORM,
			AddStep:              corev1.AddDeviceStep_ADD_DEVICE_STEP_IDLE,
			AddSelectedRemoveIDs: make(map[string]bool),
			SecSubpage:           corev1.AccountSecuritySubpage_ACCOUNT_SECURITY_SUBPAGE_OVERVIEW,
		},
		pendingActions: make(map[uint64]secDoneFn),
	}
}

func (m *Model) ensureSec() *secControllerState {
	if m.sec == nil {
		m.sec = newSecControllerState()
	}
	return m.sec
}

func (c *Core) allocSecActionID() uint64 {
	if c.nextSecActionID < internalSecActionBase {
		c.nextSecActionID = internalSecActionBase
	}
	c.nextSecActionID++
	return c.nextSecActionID
}

func (c *Core) allocSecEffectOpID() uint64 {
	c.nextSecEffectOp++
	return c.nextSecEffectOp
}

func (c *Core) securityViewContext() screens.SecurityViewContext {
	w := c.m.sync.World()
	snap := c.m.sync.SecuritySnapshot()
	selfID := ""
	if sc := c.m.sec; sc != nil && sc.ui.Status != nil {
		selfID = sc.ui.Status.DeviceId
	}
	if selfID == "" {
		selfID = snap.SelfDeviceID
	}
	return screens.SecurityViewContext{
		NowMs:               c.m.sync.NowMs(),
		SyncLoaded:          snap.MachinesListed || snap.Loaded,
		SyncIsManager:       snap.IsManager,
		SyncLeaseEndMs:      snap.LeaseEndMs,
		SyncRecoveryPending: snap.RecoveryPending,
		SyncRecoveryReadyMs: snap.RecoveryReadyMs,
		SyncRecoveryStale:   snap.RecoveryStatusStale,
		SelfDeviceID:        selfID,
		Devices:             snap.Devices,
		ManagerDisplay:      screens.ManagerNameText(w),
		MachineDisplayName:  c.m.sync.MachineDisplayName,
	}
}

func (c *Core) viewSecurity(screen *corev1.Screen) (*corev1.ViewModel, bool, error) {
	sc := c.m.ensureSec()
	switch screen.Screen.(type) {
	case *corev1.Screen_AppRoot:
		return &corev1.ViewModel{View: &corev1.ViewModel_AppRoot{AppRoot: screens.BuildAppRoot(&sc.ui)}}, true, nil
	case *corev1.Screen_Onboarding:
		return &corev1.ViewModel{View: &corev1.ViewModel_Onboarding{Onboarding: screens.BuildOnboarding(&sc.ui)}}, true, nil
	case *corev1.Screen_AddDevice:
		return &corev1.ViewModel{View: &corev1.ViewModel_AddDevice{AddDevice: screens.BuildAddDevice(&sc.ui, c.securityViewContext())}}, true, nil
	case *corev1.Screen_AccountSecurity:
		return &corev1.ViewModel{View: &corev1.ViewModel_AccountSecurity{AccountSecurity: screens.BuildAccountSecurity(&sc.ui, c.securityViewContext())}}, true, nil
	case *corev1.Screen_Handoff:
		return &corev1.ViewModel{View: &corev1.ViewModel_Handoff{Handoff: screens.BuildHandoff(&sc.ui, c.securityViewContext())}}, true, nil
	case *corev1.Screen_ExitConfirm:
		return &corev1.ViewModel{View: &corev1.ViewModel_ExitConfirm{ExitConfirm: screens.BuildExitConfirm(&sc.ui)}}, true, nil
	default:
		return nil, false, nil
	}
}

// runSecOp dispatches an async I/O security workflow action to the in-process security.Engine
// and invokes onDone when its SecurityWorkflowResultEffect completes.
func (c *Core) runSecOp(ctx *sync.StepContext, ev *corev1.SecurityWorkflowActionEvent, onDone secDoneFn) {
	sc := c.m.ensureSec()
	id := c.allocSecActionID()
	ev.ActionId = id
	if onDone != nil {
		sc.pendingActions[id] = onDone
	} else {
		sc.pendingActions[id] = func(*sync.StepContext, *corev1.SecurityWorkflowResultEffect, security.WorkflowMeta, *corev1.Text) []*corev1.Effect {
			return nil
		}
	}
	effs := c.m.sync.SecurityEngine().HandleWorkflowAction(c.m.sync.NowMs(), ev, c.m.sync.SecurityDeviceOffers(), nil)
	c.m.sync.EmitSecurityEffects(ctx, effs)
}

func (c *Core) runAccountOp(ctx *sync.StepContext, ev *corev1.SecurityWorkflowActionEvent, onOK func(ctx *sync.StepContext, res *corev1.SecurityWorkflowResultEffect)) {
	sc := c.m.ensureSec()
	ui := &sc.ui
	ui.SecBusy, ui.SecSuccessNotice, ui.SecErrorBanner, sc.dirty.AccountSecurity = true, nil, clearBanner, true
	c.runSecOp(ctx, ev, func(ctx *sync.StepContext, res *corev1.SecurityWorkflowResultEffect, _ security.WorkflowMeta, errTxt *corev1.Text) []*corev1.Effect {
		ui.SecBusy, sc.dirty.AccountSecurity = false, true
		if !res.Ok {
			ui.SecErrorBanner = errBanner(errTxt)
			return nil
		}
		if onOK != nil {
			onOK(ctx, res)
		}
		return nil
	})
}

func (c *Core) runHandoffOp(ctx *sync.StepContext, ev *corev1.SecurityWorkflowActionEvent, onOK func(ctx *sync.StepContext, res *corev1.SecurityWorkflowResultEffect)) {
	sc := c.m.ensureSec()
	ui := &sc.ui
	ui.HandoffBusy, ui.HandoffMessageBanner, ui.HandoffErrorBanner, sc.dirty.Handoff = true, nil, clearBanner, true
	c.runSecOp(ctx, ev, func(ctx *sync.StepContext, res *corev1.SecurityWorkflowResultEffect, _ security.WorkflowMeta, errTxt *corev1.Text) []*corev1.Effect {
		ui.HandoffBusy, sc.dirty.Handoff = false, true
		if !res.Ok {
			ui.HandoffErrorBanner = errBanner(errTxt)
			return nil
		}
		if onOK != nil {
			onOK(ctx, res)
		}
		return nil
	})
}

func (c *Core) runRecOp(ctx *sync.StepContext, ev *corev1.SecurityWorkflowActionEvent, onOK func(ctx *sync.StepContext, res *corev1.SecurityWorkflowResultEffect) []*corev1.Effect) {
	sc := c.m.ensureSec()
	ui := &sc.ui
	ui.RecBusy, ui.RecBanner, sc.dirty.Onboarding = true, clearBanner, true
	c.runSecOp(ctx, ev, func(ctx *sync.StepContext, res *corev1.SecurityWorkflowResultEffect, _ security.WorkflowMeta, errTxt *corev1.Text) []*corev1.Effect {
		ui.RecBusy, sc.dirty.Onboarding = false, true
		if !res.Ok {
			ui.RecBanner = errBanner(errTxt)
			return nil
		}
		if onOK != nil {
			return onOK(ctx, res)
		}
		return nil
	})
}

func (c *Core) requestPersistEffect(kind, server, oldCode string) []*corev1.Effect {
	opID := c.allocSecEffectOpID()
	c.m.ensureSec().pendingPersist = &secPendingPersist{opID: opID, kind: kind, server: server, oldCode: oldCode}
	return []*corev1.Effect{{Effect: &corev1.Effect_RequestStoragePersist{RequestStoragePersist: &corev1.RequestStoragePersistEffect{OpId: opID}}}}
}

// OnSecurityPollUpdate implements security.PollObserver.
func (c *Core) OnSecurityPollUpdate(u security.PollUpdate) {
	sc := c.m.ensureSec()
	ui := &sc.ui
	switch u.Kind {
	case security.PollInitiatorJoined:
		if u.PairingID != "" && ui.AddPairingID != "" && u.PairingID != ui.AddPairingID {
			return
		}
		if u.Err != nil {
			errTxt := screens.SecurityWorkflowReasonText(u.Meta.ErrReason, u.Meta.ErrRaw)
			if !u.Retryable ||
				u.ErrCode == corev1.SecurityErrorCode_SECURITY_ERROR_CODE_CANCELED ||
				u.ErrCode == corev1.SecurityErrorCode_SECURITY_ERROR_CODE_NOT_FOUND {
				ui.AddStep = corev1.AddDeviceStep_ADD_DEVICE_STEP_FAILED
				ui.AddBanner = errBanner(errTxt)
			} else {
				ui.AddBanner = warnBanner(errTxt)
			}
			sc.dirty.AddDevice = true
			return
		}
		if u.Pairing != nil && u.Pairing.Joined && len(u.Pairing.SasEmoji) > 0 {
			c.applyInitiatorPairingInfo(u.Pairing)
			if ui.AddStep != corev1.AddDeviceStep_ADD_DEVICE_STEP_COMPARE_SAS {
				ui.AddSASConfirmed = false
			}
			ui.AddStep = corev1.AddDeviceStep_ADD_DEVICE_STEP_COMPARE_SAS
			ui.AddBanner = clearBanner
			sc.dirty.AddDevice = true
		}

	case security.PollInitiatorJoinerMeta:
		if u.PairingID != "" && ui.AddPairingID != "" && u.PairingID != ui.AddPairingID {
			return
		}
		if u.Pairing != nil {
			c.applyInitiatorPairingInfo(u.Pairing)
		}
		sc.dirty.AddDevice = true

	case security.PollInitiatorDelivery:
		if u.Err != nil {
			if ui.AddOpen && ui.AddStep == corev1.AddDeviceStep_ADD_DEVICE_STEP_DELIVERING {
				ui.AddBanner = warnBanner(screens.SecurityWorkflowReasonText(u.Meta.ErrReason, u.Meta.ErrRaw))
				sc.dirty.AddDevice = true
			}
			return
		}
		if u.Delivered {
			if ui.AddOpen && (ui.AddStep == corev1.AddDeviceStep_ADD_DEVICE_STEP_DELIVERING || ui.AddStep == corev1.AddDeviceStep_ADD_DEVICE_STEP_APPROVING) {
				ui.AddStep = corev1.AddDeviceStep_ADD_DEVICE_STEP_DONE
				ui.AddBusy = false
				ui.AddBanner = clearBanner
				if u.Pairing != nil && !text.IsEmpty(u.Pairing.RemoveOldNotice) {
					ui.AddRemoveOldWarning = u.Pairing.RemoveOldNotice
				}
				sc.dirty.AddDevice = true
			}
			sc.dirty.AccountSecurity = true
		}

	case security.PollRecoveryWindow:
		if u.Err != nil {
			ui.RecBanner = warnBanner(screens.SecurityWorkflowReasonText(u.Meta.ErrReason, u.Meta.ErrRaw))
			sc.dirty.Onboarding = true
			return
		}
		if u.Recovery != nil {
			if u.Recovery.ReadyMs > 0 {
				ui.RecReadyMs = u.Recovery.ReadyMs
			}
			ui.RecPhase = screens.DeriveRecoveryPhase(u.Recovery, nil)
			ui.RecBanner = clearBanner
			sc.dirty.Onboarding = true
		}
	}
}

// OnSecurityDevicesMayHaveChanged implements security.PollObserver.
func (c *Core) OnSecurityDevicesMayHaveChanged() {
	c.m.ensureSec().refreshDevicesRequested = true
}

func (c *Core) applyInitiatorPairingInfo(info *corev1.SecurityPairingInfo) {
	if info == nil {
		return
	}
	sc := c.m.ensureSec()
	ui := &sc.ui
	if info.PairingId != "" {
		ui.AddPairingID = info.PairingId
	}
	if info.Code != "" {
		ui.AddRawCode = info.Code
	}
	if info.Url != "" {
		ui.AddQRURL = info.Url
	}
	if info.ExpiresMs > 0 {
		ui.AddExpiresMs = info.ExpiresMs
	}
	if len(info.SasEmoji) > 0 {
		ui.AddSASEmoji = append([]string(nil), info.SasEmoji...)
	}
	if info.JoinerKind != v1.DeviceKind_DEVICE_KIND_UNSPECIFIED {
		ui.AddJoinerKind = info.JoinerKind
	}
	if !text.IsEmpty(info.ReplaceOldLabel) {
		ui.AddReplaceOldLabel = info.ReplaceOldLabel
	}
	if !text.IsEmpty(info.ReplaceOldHint) {
		ui.AddReplaceOldHint = info.ReplaceOldHint
	}
	if len(info.ReplacementCandidates) > 0 {
		ui.AddCandidates = info.ReplacementCandidates
		if !ui.AddUserToggledCandidates || ui.AddSelectedRemoveIDs == nil {
			ui.AddSelectedRemoveIDs = make(map[string]bool, len(info.ReplacementCandidates))
		}
		for _, cand := range info.ReplacementCandidates {
			if cand != nil && cand.DeviceId != "" {
				if _, exists := ui.AddSelectedRemoveIDs[cand.DeviceId]; !ui.AddUserToggledCandidates || !exists {
					ui.AddSelectedRemoveIDs[cand.DeviceId] = cand.DefaultSelected
				}
			}
		}
	}
	if !text.IsEmpty(info.RemoveOldNotice) {
		ui.AddRemoveOldWarning = info.RemoveOldNotice
	}
}

func (c *Core) dispatchSecurityUI(ctx *sync.StepContext, ev *corev1.Event) ([]*corev1.Effect, bool) {
	if ev == nil {
		return nil, false
	}
	switch k := ev.Event.(type) {
	case *corev1.Event_ClientInit:
		return c.handleClientInit(ctx, k.ClientInit), true
	case *corev1.Event_StoragePersistResult:
		return c.handleStoragePersistResult(ctx, k.StoragePersistResult), true
	case *corev1.Event_PushUnregistered:
		return c.handlePushUnregistered(ctx, k.PushUnregistered), true
	case *corev1.Event_AccountCachesCleared:
		return c.handleAccountCachesCleared(ctx, k.AccountCachesCleared), true
	case *corev1.Event_ExternalLogout:
		return c.handleExternalLogout(ctx), true
	case *corev1.Event_AppRootRetryBootstrap:
		return c.handleAppRootRetryBootstrap(ctx), true
	case *corev1.Event_AppRootVerifyPendingOp:
		return c.handleAppRootVerifyPendingOp(ctx), true
	case *corev1.Event_AppRootDismissUpgradeNotice:
		return c.handleAppRootDismissUpgradeNotice(ctx), true
	case *corev1.Event_OnboardingSetMode:
		return c.handleOnboardingSetMode(ctx, k.OnboardingSetMode), true
	case *corev1.Event_OnboardingCreateEdit:
		return c.handleOnboardingCreateEdit(k.OnboardingCreateEdit), true
	case *corev1.Event_OnboardingCreateSubmit:
		return c.handleOnboardingCreateSubmit(ctx, k.OnboardingCreateSubmit), true
	case *corev1.Event_OnboardingJoinEdit:
		return c.handleOnboardingJoinEdit(k.OnboardingJoinEdit), true
	case *corev1.Event_OnboardingJoinSubmit:
		return c.handleOnboardingJoinSubmit(ctx, k.OnboardingJoinSubmit), true
	case *corev1.Event_OnboardingJoinConfirmSas:
		return c.handleOnboardingJoinConfirmSAS(ctx), true
	case *corev1.Event_OnboardingJoinCancel:
		return c.handleOnboardingJoinCancel(ctx), true
	case *corev1.Event_OnboardingJoinBannerAction:
		return c.handleOnboardingJoinBannerAction(ctx), true
	case *corev1.Event_OnboardingRecoveryEdit:
		return c.handleOnboardingRecoveryEdit(k.OnboardingRecoveryEdit), true
	case *corev1.Event_OnboardingRecoveryAction:
		return c.handleOnboardingRecoveryAction(ctx, k.OnboardingRecoveryAction), true
	case *corev1.Event_AddDeviceOpen:
		return c.handleAddDeviceOpen(ctx, k.AddDeviceOpen), true
	case *corev1.Event_AddDeviceClose:
		return c.handleAddDeviceClose(ctx, k.AddDeviceClose), true
	case *corev1.Event_AddDeviceAction:
		return c.handleAddDeviceAction(ctx, k.AddDeviceAction), true
	case *corev1.Event_AccountSecurityOpen:
		return c.handleAccountSecurityOpen(ctx, k.AccountSecurityOpen), true
	case *corev1.Event_AccountSecurityClose:
		return c.handleAccountSecurityClose(), true
	case *corev1.Event_AccountSecurityAction:
		return c.handleAccountSecurityAction(ctx, k.AccountSecurityAction), true
	case *corev1.Event_HandoffAction:
		return c.handleHandoffAction(ctx, k.HandoffAction), true
	case *corev1.Event_ExitRequest:
		return c.handleExitRequest(ctx, k.ExitRequest), true
	case *corev1.Event_ExitConfirm:
		return c.handleExitConfirm(ctx, k.ExitConfirm), true
	case *corev1.Event_TimerFired:
		if k.TimerFired != nil &&
			k.TimerFired.Kind == corev1.TimerKind_TIMER_KIND_UI_TRANSIENT &&
			k.TimerFired.Key == exitPushUnregisterTimerKey {
			if c.m.sync.ConsumeAppTimer(k.TimerFired) {
				return c.handleExitPushUnregisterTimeout(ctx), true
			}
			return nil, true
		}
		return nil, false
	default:
		return nil, false
	}
}

// ---- Bootstrap & AppRoot Handlers ----

func (c *Core) handleClientInit(ctx *sync.StepContext, ev *corev1.ClientInitEvent) []*corev1.Effect {
	if ev == nil {
		return nil
	}
	c.clientCfg = &clientInitConfig{
		platform:              ev.Platform,
		deviceDescription:     ev.DeviceDescription,
		inlineCrypto:          ev.InlineCrypto,
		defaultServer:         strings.TrimSpace(ev.DefaultServer),
		requestStoragePersist: ev.RequestStoragePersist,
	}
	c.applyRuntimeConfigToModel(
		usage.NormalizeUILocale(ev.UiLocale),
		ev.Foreground,
		ev.NetworkAvailable,
		ev.NetworkSignature,
	)

	sc := c.m.ensureSec()
	sc.ui.Initialized = true
	sc.ui.EntryDefaultServer = c.clientCfg.defaultServer
	sc.ui.CreateServerInput = c.clientCfg.defaultServer
	sc.ui.JoinServerInput = c.clientCfg.defaultServer
	sc.ui.RecServerInput = c.clientCfg.defaultServer
	sc.ui.Stage = corev1.AppRootStage_APP_ROOT_STAGE_LOADING
	sc.ui.BootstrapErrText = nil

	sc.hasInitialPairLink = false
	sc.initialPairCode, sc.initialPairServer, sc.initialPairCommit, sc.initialPairRawLink = "", "", nil, ""
	if ev.InitialUrl != "" {
		if code, srv, commit, ok := security.ParsePairLink(ev.InitialUrl); ok {
			sc.hasInitialPairLink = true
			sc.initialPairCode = code
			sc.initialPairServer = srv
			sc.initialPairCommit = commit
			sc.initialPairRawLink = ev.InitialUrl
		}
	}

	c.runBootstrap(ctx)
	sc.dirty.markAll()
	return nil
}

func (c *Core) applyRuntimeConfigToModel(uiLocale string, foreground, networkAvailable bool, networkSignature string) {
	c.m.state.UILocale = usage.NormalizeUILocale(uiLocale)
	c.m.sync.SetInitialNetworkState(networkAvailable, networkSignature)
	secEng := c.m.sync.SecurityEngine()
	if cfg := c.clientCfg; cfg != nil {
		c.m.sync.SetPlatformIdentity(cfg.platform, cfg.deviceDescription)
		secEng.Configure(&corev1.SecurityConfigEvent{
			InlineCrypto:      cfg.inlineCrypto,
			Foreground:        foreground,
			Platform:          cfg.platform,
			DeviceDescription: cfg.deviceDescription,
		})
		c.m.ensureSec().ui.EntryDefaultServer = cfg.defaultServer
	} else {
		secEng.SetForeground(foreground)
	}
}

func (c *Core) runBootstrap(ctx *sync.StepContext) {
	sc := c.m.ensureSec()
	ui := &sc.ui
	c.runSecOp(ctx, &corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: emptySec}},
		func(ctx *sync.StepContext, res *corev1.SecurityWorkflowResultEffect, _ security.WorkflowMeta, errTxt *corev1.Text) []*corev1.Effect {
			sc.dirty.AppRoot, sc.dirty.Onboarding = true, true
			if !res.Ok {
				ui.Stage = corev1.AppRootStage_APP_ROOT_STAGE_BOOTSTRAP_ERROR
				ui.BootstrapErrText = text.Msg(text.KeyErrorsSetupRequestPreserved, text.ArgText("reason", errTxt))
				ui.BootstrapCanRetry = true
				ui.BootstrapCanReset = res.StructuredErrorCode != corev1.SecurityErrorCode_SECURITY_ERROR_CODE_STORAGE
				return nil
			}
			st := res.Status
			if st == nil {
				ui.Stage, ui.Mode = corev1.AppRootStage_APP_ROOT_STAGE_ONBOARDING, corev1.OnboardingMode_ONBOARDING_MODE_ENTRY
				return nil
			}
			switch st.Bootstrap {
			case corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_READY:
				return c.enterReadyAccount(ctx, st, nil)
			case corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_REMOVED:
				ui.Stage, ui.RemovalVerified = corev1.AppRootStage_APP_ROOT_STAGE_REMOVED, true
				var out []*corev1.Effect
				c.emitRuntimeChangedIfNeeded(&out, false, true, st.Server, st.DeviceId, st.AccountId, false)
				return out
			case corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_CLOCK_SKEWED:
				ui.Stage, ui.BootstrapCanRetry, ui.BootstrapCanReset = corev1.AppRootStage_APP_ROOT_STAGE_BOOTSTRAP_ERROR, true, false
				ui.BootstrapErrText = text.Msg(text.KeyErrorsThisDeviceSClockIsBehindTheAccountState)
			case corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_INVALID:
				ui.Stage, ui.BootstrapCanRetry, ui.BootstrapCanReset = corev1.AppRootStage_APP_ROOT_STAGE_BOOTSTRAP_ERROR, true, true
				ui.BootstrapErrText = st.ErrorText
				if text.IsEmpty(ui.BootstrapErrText) {
					ui.BootstrapErrText = text.Msg(text.KeyErrorsThisDeviceSSecurityStateCouldNotBeVerified)
				}
			case corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_PENDING_CREATE:
				ui.Stage, ui.Mode = corev1.AppRootStage_APP_ROOT_STAGE_ONBOARDING, corev1.OnboardingMode_ONBOARDING_MODE_CREATE
				if st.Server != "" {
					ui.CreateServerInput = st.Server
				}
			case corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_PENDING_RECOVERY:
				ui.Stage, ui.Mode = corev1.AppRootStage_APP_ROOT_STAGE_ONBOARDING, corev1.OnboardingMode_ONBOARDING_MODE_RECOVERY
				if st.Server != "" {
					ui.RecServerInput = st.Server
				}
				if st.PendingOp != nil && st.PendingOp.RecoveryId != "" {
					c.runRecOp(ctx, &corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_ResumeRecovery{ResumeRecovery: emptySec}},
						func(ctx *sync.StepContext, res *corev1.SecurityWorkflowResultEffect) []*corev1.Effect {
							c.syncRecoveryPhase(ctx, res)
							return nil
						})
				}
			case corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_PENDING_REGISTRATION:
				ui.Stage, ui.Mode = corev1.AppRootStage_APP_ROOT_STAGE_ONBOARDING, corev1.OnboardingMode_ONBOARDING_MODE_JOIN
				ui.JoinPendingRegistration, ui.JoinStep = true, corev1.JoinPairingStep_JOIN_PAIRING_STEP_WORKING
				if st.Server != "" {
					ui.JoinServerInput = st.Server
				}
				c.resumePendingRegistration(ctx)
			default: // EMPTY
				if st.DeviceId != "" && st.Server != "" {
					srv := st.Server
					ui.RetainedServer = srv
					c.runSecOp(ctx, &corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_CheckRetainedDenied{CheckRetainedDenied: emptySec}},
						func(ctx *sync.StepContext, r *corev1.SecurityWorkflowResultEffect, _ security.WorkflowMeta, _ *corev1.Text) []*corev1.Effect {
							sc.dirty.AppRoot, sc.dirty.Onboarding = true, true
							if r.Ok && r.Flag {
								ui.Stage = corev1.AppRootStage_APP_ROOT_STAGE_RETAINED_DENIED
								return nil
							}
							c.landInEmptyOnboarding(ctx, true, srv)
							return nil
						})
					return nil
				}
				c.landInEmptyOnboarding(ctx, false, "")
			}
			return nil
		})
}

func (c *Core) resumePendingRegistration(ctx *sync.StepContext) {
	sc := c.m.ensureSec()
	ui := &sc.ui
	ui.JoinBusy, ui.JoinStep = true, corev1.JoinPairingStep_JOIN_PAIRING_STEP_WORKING
	c.runSecOp(ctx, &corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_ResumePendingOp{ResumePendingOp: emptySec}},
		func(ctx *sync.StepContext, res *corev1.SecurityWorkflowResultEffect, meta security.WorkflowMeta, errTxt *corev1.Text) []*corev1.Effect {
			ui.JoinBusy, sc.dirty.AppRoot, sc.dirty.Onboarding = false, true, true
			if res.Ok && res.Status != nil {
				if res.Status.Bootstrap == corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_READY {
					return c.enterReadyAccount(ctx, res.Status, res.AccountAdopted)
				}
				if res.Status.Bootstrap == corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_EMPTY {
					ui.JoinPendingRegistration, ui.JoinStep, ui.Mode = false, corev1.JoinPairingStep_JOIN_PAIRING_STEP_FORM, corev1.OnboardingMode_ONBOARDING_MODE_ENTRY
					return nil
				}
			}
			ui.JoinBanner = screens.BuildJoinBannerState(errTxt, res.StructuredErrorCode, meta.ErrReason, res.Retryable, res.Uncertain, res.ErrorAction)
			return nil
		})
}

func (c *Core) handleAppRootRetryBootstrap(ctx *sync.StepContext) []*corev1.Effect {
	sc := c.m.ensureSec()
	sc.ui.Stage, sc.ui.BootstrapErrText, sc.dirty.AppRoot = corev1.AppRootStage_APP_ROOT_STAGE_LOADING, nil, true
	c.runBootstrap(ctx)
	return nil
}

func (c *Core) handleAppRootVerifyPendingOp(ctx *sync.StepContext) []*corev1.Effect {
	sc := c.m.ensureSec()
	ui := &sc.ui
	if ui.RemovedVerifyBusy {
		return nil
	}
	ui.RemovedVerifyBusy, ui.RemovedVerifyResultText, sc.dirty.AppRoot = true, nil, true
	c.runSecOp(ctx, &corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_ResumePendingOp{ResumePendingOp: emptySec}},
		func(ctx *sync.StepContext, res *corev1.SecurityWorkflowResultEffect, _ security.WorkflowMeta, errTxt *corev1.Text) []*corev1.Effect {
			ui.RemovedVerifyBusy, sc.dirty.AppRoot = false, true
			if !res.Ok {
				ui.RemovedVerifyResultText = errTxt
				return nil
			}
			if res.Status != nil && res.Status.Bootstrap == corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_READY {
				return c.enterReadyAccount(ctx, res.Status, res.AccountAdopted)
			}
			if res.Status != nil && res.Status.PendingOp != nil {
				ui.RemovedVerifyResultText = text.Msg(text.KeySettingsTheOriginalOperationIsUnfinishedContinueVerifying)
			} else {
				ui.RemovedVerifyResultText = text.Msg(text.KeySettingsTheOriginalSecurityOperationIsVerifiedCompleteYouCan)
			}
			return nil
		})
	return nil
}

func (c *Core) handleAppRootDismissUpgradeNotice(ctx *sync.StepContext) []*corev1.Effect {
	sc := c.m.ensureSec()
	if sc.ui.Status != nil {
		sc.ui.Status.LegacyRepairNotice = false
	}
	sc.dirty.AppRoot = true
	sc.dirty.Onboarding = true
	c.runSecOp(ctx, &corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_DismissUpgradeNotice{DismissUpgradeNotice: emptySec}}, func(_ *sync.StepContext, _ *corev1.SecurityWorkflowResultEffect, _ security.WorkflowMeta, _ *corev1.Text) []*corev1.Effect {
		sc.dirty.AppRoot = true
		sc.dirty.Onboarding = true
		return nil
	})
	return []*corev1.Effect{{Effect: &corev1.Effect_DismissUpgradeNotice{DismissUpgradeNotice: &corev1.DismissUpgradeNoticeEffect{}}}}
}

func (c *Core) handleStoragePersistResult(ctx *sync.StepContext, ev *corev1.StoragePersistResultEvent) []*corev1.Effect {
	sc := c.m.ensureSec()
	pp := sc.pendingPersist
	if ev == nil || pp == nil || pp.opID != ev.OpId {
		return nil
	}
	sc.pendingPersist = nil
	switch pp.kind {
	case "create":
		sc.ui.CreateHasPersist = true
		sc.ui.CreateStoragePersisted = ev.Persisted
		c.prepareCreationOp(ctx, pp.server, ev.Persisted)
		sc.dirty.Onboarding = true
	case "recovery":
		sc.ui.RecHasPersist = true
		sc.ui.RecStoragePersisted = ev.Persisted
		c.prepareRecoveryOp(ctx, pp.server, pp.oldCode)
		sc.dirty.Onboarding = true
	case "handoff":
		sc.ui.HandoffHasPersist = true
		sc.ui.HandoffStoragePersisted = ev.Persisted
		c.prepareHandoffCodeOp(ctx, ev.Persisted)
		sc.dirty.Handoff = true
	}
	return nil
}

// ---- Onboarding Handlers ----

func (c *Core) handleOnboardingSetMode(ctx *sync.StepContext, act *corev1.OnboardingSetModeAction) []*corev1.Effect {
	if act == nil {
		return nil
	}
	sc := c.m.ensureSec()
	ui := &sc.ui
	if ui.Mode == corev1.OnboardingMode_ONBOARDING_MODE_JOIN &&
		act.Mode != corev1.OnboardingMode_ONBOARDING_MODE_JOIN &&
		ui.JoinPairingID != "" && !ui.JoinRegistering {
		pid := ui.JoinPairingID
		ui.JoinPairingID = ""
		c.runSecOp(ctx, &corev1.SecurityWorkflowActionEvent{
			Action: &corev1.SecurityWorkflowActionEvent_CancelJoinPairing{CancelJoinPairing: &corev1.SecurityStringAction{Value: pid}},
		}, nil)
	}
	if ui.Mode == corev1.OnboardingMode_ONBOARDING_MODE_RECOVERY && act.Mode != corev1.OnboardingMode_ONBOARDING_MODE_RECOVERY {
		c.m.sync.EmitSecurityEffects(ctx, c.m.sync.SecurityEngine().StopManagedPoll(security.PollRecoveryWindow))
	}

	mode := act.Mode
	if mode == corev1.OnboardingMode_ONBOARDING_MODE_UNSPECIFIED {
		mode = corev1.OnboardingMode_ONBOARDING_MODE_ENTRY
	}
	ui.Mode = mode
	if act.ExistingRepair {
		ui.JoinExistingRepair = true
		if act.ExistingServer != "" {
			ui.JoinExistingServer = act.ExistingServer
			ui.JoinServerInput = act.ExistingServer
		}
	}
	switch mode {
	case corev1.OnboardingMode_ONBOARDING_MODE_JOIN:
		ui.JoinBanner = clearBanner
		if ui.JoinServerInput == "" {
			ui.JoinServerInput = ui.EntryDefaultServer
		}
		if act.InitialUrl != "" {
			if code, srv, commit, ok := security.ParsePairLink(act.InitialUrl); ok {
				ui.JoinCodeDigits = code
				if !ui.JoinExistingRepair {
					ui.JoinServerInput = srv
				}
				ui.JoinCommitHint = commit
				ui.JoinRawLink = act.InitialUrl
				c.startJoinPairing(ctx)
			}
		}
	case corev1.OnboardingMode_ONBOARDING_MODE_CREATE:
		ui.CreateBanner = clearBanner
		if ui.CreateServerInput == "" {
			ui.CreateServerInput = ui.EntryDefaultServer
		}
	case corev1.OnboardingMode_ONBOARDING_MODE_RECOVERY:
		ui.RecBanner = clearBanner
		if ui.RecServerInput == "" {
			ui.RecServerInput = ui.EntryDefaultServer
		}
	}
	sc.dirty.Onboarding = true
	return nil
}

func (c *Core) handleOnboardingCreateEdit(act *corev1.OnboardingCreateEditAction) []*corev1.Effect {
	if act == nil {
		return nil
	}
	sc := c.m.ensureSec()
	if act.HasServerInput {
		sc.ui.CreateServerInput = act.ServerInput
	}
	if act.HasCodeSavedChecked {
		sc.ui.CreateCodeSaved = act.CodeSavedChecked
	}
	sc.dirty.Onboarding = true
	return nil
}

func (c *Core) prepareCreationOp(ctx *sync.StepContext, server string, persisted bool) {
	sc := c.m.ensureSec()
	ui := &sc.ui
	c.runSecOp(ctx, &corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PrepareCreation{
			PrepareCreation: &corev1.SecurityPrepareCreationAction{Server: server, StoragePersisted: persisted},
		},
	}, func(_ *sync.StepContext, res *corev1.SecurityWorkflowResultEffect, _ security.WorkflowMeta, errTxt *corev1.Text) []*corev1.Effect {
		ui.CreateBusy = false
		sc.dirty.Onboarding = true
		if !res.Ok {
			ui.CreateBanner = errBanner(errTxt)
			return nil
		}
		if res.RecoveryCode != "" {
			ui.CreateRecoveryCode = res.RecoveryCode
			ui.CreateCodeSaved = false
		} else if res.ResumedDraft {
			ui.CreateMissingCodeAck = res.Status != nil && res.Status.PendingOp != nil &&
				res.Status.PendingOp.Phase == security.PendingOpPhasePublished && !res.Status.PendingOp.CodeSaved
		}
		return nil
	})
}

func (c *Core) continueCreationOp(ctx *sync.StepContext, ev *corev1.SecurityWorkflowActionEvent) {
	sc := c.m.ensureSec()
	ui := &sc.ui
	c.runSecOp(ctx, ev, func(ctx *sync.StepContext, res *corev1.SecurityWorkflowResultEffect, _ security.WorkflowMeta, errTxt *corev1.Text) []*corev1.Effect {
		ui.CreateBusy = false
		sc.dirty.Onboarding = true
		if !res.Ok {
			ui.CreateBanner = errBanner(errTxt)
			return nil
		}
		if res.AccountAdopted != nil || (res.Status != nil && res.Status.Bootstrap == corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_READY) {
			return c.enterReadyAccount(ctx, res.Status, res.AccountAdopted)
		}
		if res.Status != nil && res.Status.PendingOp != nil &&
			res.Status.PendingOp.Phase == security.PendingOpPhasePublished && !res.Status.PendingOp.CodeSaved {
			ui.CreateMissingCodeAck = true
		}
		return nil
	})
}

func (c *Core) handleOnboardingCreateSubmit(ctx *sync.StepContext, act *corev1.OnboardingCreateSubmitAction) []*corev1.Effect {
	if act == nil {
		return nil
	}
	sc := c.m.ensureSec()
	ui := &sc.ui
	if ui.CreateBusy {
		return nil
	}
	ui.CreateBanner = clearBanner
	if act.ServerInput != "" {
		ui.CreateServerInput = strings.TrimSpace(act.ServerInput)
	}
	var pendingOp *corev1.SecurityPendingOpSummary
	if ui.Status != nil {
		pendingOp = ui.Status.PendingOp
	}
	hasPendingCreate := pendingOp != nil && pendingOp.Kind == security.PendingOpKindCreate

	if ui.CreateMissingCodeAck || (hasPendingCreate && pendingOp.Phase == security.PendingOpPhasePublished && !pendingOp.CodeSaved && ui.CreateRecoveryCode == "") {
		ui.CreateBusy, sc.dirty.Onboarding = true, true
		c.continueCreationOp(ctx, &corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_AdoptCreationWithoutCode{AdoptCreationWithoutCode: emptySec}})
		return nil
	}

	if ui.CreateRecoveryCode != "" || act.ConfirmCodeSaved {
		if !ui.CreateCodeSaved && !act.ConfirmCodeSaved {
			ui.CreateBanner, sc.dirty.Onboarding = warnBanner(text.Msg(text.KeySecurityConfirmRecoveryCodeSavedFirst)), true
			return nil
		}
		ui.CreateCodeSaved, ui.CreateBusy, sc.dirty.Onboarding = true, true, true
		setupToken := act.SetupToken
		c.runSecOp(ctx, &corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_ConfirmCreationCodeSaved{ConfirmCreationCodeSaved: emptySec}},
			func(ctx *sync.StepContext, res *corev1.SecurityWorkflowResultEffect, _ security.WorkflowMeta, errTxt *corev1.Text) []*corev1.Effect {
				sc.dirty.Onboarding = true
				if !res.Ok {
					ui.CreateBusy, ui.CreateBanner = false, errBanner(errTxt)
					return nil
				}
				if res.AccountAdopted != nil || (res.Status != nil && res.Status.Bootstrap == corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_READY) {
					return c.enterReadyAccount(ctx, res.Status, res.AccountAdopted)
				}
				c.continueCreationOp(ctx, &corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_ContinueCreation{ContinueCreation: secStr(setupToken)}})
				return nil
			})
		return nil
	}

	if hasPendingCreate {
		ui.CreateBusy, sc.dirty.Onboarding = true, true
		c.continueCreationOp(ctx, &corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_ContinueCreation{ContinueCreation: secStr(act.SetupToken)}})
		return nil
	}

	server := strings.TrimSpace(ui.CreateServerInput)
	if server == "" {
		return nil
	}
	ui.CreateBusy, sc.dirty.Onboarding = true, true
	if c.clientCfg != nil && c.clientCfg.requestStoragePersist {
		return c.requestPersistEffect("create", server, "")
	}
	c.prepareCreationOp(ctx, server, true)
	return nil
}

func updateJoinCode(ui *screens.SecurityUIState, raw string) {
	if digits := screens.NormalizePairingCodeDigits(raw); digits != ui.JoinCodeDigits {
		ui.JoinCommitHint, ui.JoinRawLink, ui.JoinCodeDigits = nil, "", digits
	}
}

func updateJoinServer(ui *screens.SecurityUIState, srv string) {
	if !ui.JoinExistingRepair && srv != ui.JoinServerInput {
		ui.JoinCommitHint, ui.JoinRawLink, ui.JoinServerInput = nil, "", srv
	}
}

func (c *Core) handleOnboardingJoinEdit(act *corev1.OnboardingJoinEditAction) []*corev1.Effect {
	if act == nil {
		return nil
	}
	sc := c.m.ensureSec()
	if act.HasCodeInput {
		updateJoinCode(&sc.ui, act.CodeInput)
	}
	if act.HasServerInput {
		updateJoinServer(&sc.ui, act.ServerInput)
	}
	sc.dirty.Onboarding = true
	return nil
}

func (c *Core) handleOnboardingJoinSubmit(ctx *sync.StepContext, act *corev1.OnboardingJoinSubmitAction) []*corev1.Effect {
	if act == nil {
		return nil
	}
	sc := c.m.ensureSec()
	ui := &sc.ui
	if ui.JoinBusy {
		return nil
	}
	if act.RawLink != "" {
		if code, srv, commit, ok := security.ParsePairLink(act.RawLink); ok {
			ui.JoinCodeDigits, ui.JoinCommitHint, ui.JoinRawLink = code, commit, act.RawLink
			if !ui.JoinExistingRepair {
				ui.JoinServerInput = srv
			}
		} else {
			ui.JoinBanner = errBannerAct(text.Msg(text.KeySecurityAndroidThisIsNotARemoteAgentPairingQrCode), corev1.SecurityBannerAction_SECURITY_BANNER_ACTION_REFILL)
			sc.dirty.Onboarding = true
			return nil
		}
	} else {
		if act.CodeInput != "" {
			updateJoinCode(ui, act.CodeInput)
		}
		if act.ServerInput != "" {
			updateJoinServer(ui, strings.TrimSpace(act.ServerInput))
		}
	}
	c.startJoinPairing(ctx)
	return nil
}

func (c *Core) startJoinPairing(ctx *sync.StepContext) {
	sc := c.m.ensureSec()
	ui := &sc.ui
	digits := screens.NormalizePairingCodeDigits(ui.JoinCodeDigits)
	server := strings.TrimSpace(ui.JoinServerInput)
	if ui.JoinExistingRepair && ui.JoinExistingServer != "" {
		server = ui.JoinExistingServer
	}
	if len(digits) != 8 || server == "" {
		return
	}
	ui.JoinBusy = true
	ui.JoinStep = corev1.JoinPairingStep_JOIN_PAIRING_STEP_WORKING
	ui.JoinBanner = clearBanner
	ui.JoinSASEmoji = nil
	var platform, desc string
	if c.clientCfg != nil {
		platform, desc = c.clientCfg.platform, c.clientCfg.deviceDescription
	}
	c.runSecOp(ctx, &corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_StartJoinPairing{
			StartJoinPairing: &corev1.SecurityStartJoinPairingAction{
				Server:         server,
				Code:           digits,
				CommitHint:     ui.JoinCommitHint,
				ExistingRepair: ui.JoinExistingRepair,
				ExistingServer: ui.JoinExistingServer,
				RawLink:        ui.JoinRawLink,
				Platform:       platform,
				Description:    desc,
			},
		},
	}, func(_ *sync.StepContext, res *corev1.SecurityWorkflowResultEffect, meta security.WorkflowMeta, errTxt *corev1.Text) []*corev1.Effect {
		ui.JoinBusy = false
		sc.dirty.Onboarding = true
		if !res.Ok {
			if res.Status != nil && res.Status.Bootstrap == corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_PENDING_REGISTRATION {
				ui.JoinPendingRegistration = true
				ui.JoinStep = corev1.JoinPairingStep_JOIN_PAIRING_STEP_WORKING
			} else {
				ui.JoinStep = corev1.JoinPairingStep_JOIN_PAIRING_STEP_FORM
			}
			ui.JoinBanner = screens.BuildJoinBannerState(errTxt, res.StructuredErrorCode, meta.ErrReason, res.Retryable, res.Uncertain, res.ErrorAction)
			return nil
		}
		if res.Pairing != nil {
			ui.JoinPairingID = res.Pairing.PairingId
			ui.JoinSASEmoji = append([]string(nil), res.Pairing.SasEmoji...)
			ui.JoinRegistering = res.Pairing.Registering
			ui.JoinStep = corev1.JoinPairingStep_JOIN_PAIRING_STEP_COMPARE_SAS
		}
		return nil
	})
	sc.dirty.Onboarding = true
}

func (c *Core) confirmJoinSASOp(ctx *sync.StepContext) {
	sc := c.m.ensureSec()
	ui := &sc.ui
	ui.JoinBusy = true
	ui.JoinStep = corev1.JoinPairingStep_JOIN_PAIRING_STEP_WAITING_APPROVAL
	ui.JoinBanner = clearBanner
	c.runSecOp(ctx, &corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ConfirmJoinSas{
			ConfirmJoinSas: &corev1.SecurityStringAction{Value: ui.JoinPairingID},
		},
	}, func(ctx *sync.StepContext, res *corev1.SecurityWorkflowResultEffect, meta security.WorkflowMeta, errTxt *corev1.Text) []*corev1.Effect {
		ui.JoinBusy = false
		sc.dirty.Onboarding = true
		if !res.Ok {
			if res.Status != nil && res.Status.Bootstrap == corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_PENDING_REGISTRATION {
				ui.JoinPendingRegistration = true
				ui.JoinRegistering = true
				ui.JoinStep = corev1.JoinPairingStep_JOIN_PAIRING_STEP_WORKING
			}
			ui.JoinBanner = screens.BuildJoinBannerState(errTxt, res.StructuredErrorCode, meta.ErrReason, res.Retryable, res.Uncertain, res.ErrorAction)
			return nil
		}
		if res.AccountAdopted != nil || (res.Status != nil && res.Status.Bootstrap == corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_READY) {
			return c.enterReadyAccount(ctx, res.Status, res.AccountAdopted)
		}
		return nil
	})
}

func (c *Core) handleOnboardingJoinConfirmSAS(ctx *sync.StepContext) []*corev1.Effect {
	sc := c.m.ensureSec()
	ui := &sc.ui
	if ui.JoinBusy || ui.JoinPairingID == "" {
		return nil
	}
	c.confirmJoinSASOp(ctx)
	sc.dirty.Onboarding = true
	return nil
}

func (c *Core) handleOnboardingJoinCancel(ctx *sync.StepContext) []*corev1.Effect {
	sc := c.m.ensureSec()
	ui := &sc.ui
	if ui.JoinRegistering || ui.JoinPendingRegistration {
		return nil
	}
	pid := ui.JoinPairingID
	ui.JoinPairingID = ""
	ui.JoinSASEmoji = nil
	ui.JoinBusy = false
	ui.JoinStep = corev1.JoinPairingStep_JOIN_PAIRING_STEP_FORM
	ui.JoinBanner = infoBanner(text.Msg(text.KeySecurityStoppedWaitingRejectTheOldRequestOnTheManaging))
	if pid != "" {
		c.runSecOp(ctx, &corev1.SecurityWorkflowActionEvent{
			Action: &corev1.SecurityWorkflowActionEvent_CancelJoinPairing{CancelJoinPairing: &corev1.SecurityStringAction{Value: pid}},
		}, nil)
	} else {
		c.m.sync.EmitSecurityEffects(ctx, c.m.sync.SecurityEngine().StopManagedPoll(security.PollNone))
	}
	sc.dirty.Onboarding = true
	return nil
}

func (c *Core) handleOnboardingJoinBannerAction(ctx *sync.StepContext) []*corev1.Effect {
	sc := c.m.ensureSec()
	ui := &sc.ui
	act := ui.JoinBanner.Action
	ui.JoinBanner = clearBanner
	switch act {
	case corev1.SecurityBannerAction_SECURITY_BANNER_ACTION_RETRY:
		if ui.JoinPendingRegistration {
			c.resumePendingRegistration(ctx)
		} else if ui.JoinPairingID != "" && ui.JoinStep == corev1.JoinPairingStep_JOIN_PAIRING_STEP_WAITING_APPROVAL {
			c.confirmJoinSASOp(ctx)
		} else {
			c.startJoinPairing(ctx)
		}
	case corev1.SecurityBannerAction_SECURITY_BANNER_ACTION_REFILL,
		corev1.SecurityBannerAction_SECURITY_BANNER_ACTION_RESTART:
		if ui.JoinPairingID != "" && !ui.JoinRegistering {
			pid := ui.JoinPairingID
			ui.JoinPairingID = ""
			c.runSecOp(ctx, &corev1.SecurityWorkflowActionEvent{
				Action: &corev1.SecurityWorkflowActionEvent_CancelJoinPairing{CancelJoinPairing: &corev1.SecurityStringAction{Value: pid}},
			}, nil)
		}
		ui.JoinStep = corev1.JoinPairingStep_JOIN_PAIRING_STEP_FORM
		ui.JoinSASEmoji = nil
		ui.JoinCommitHint = nil
		ui.JoinRawLink = ""
		if act == corev1.SecurityBannerAction_SECURITY_BANNER_ACTION_RESTART {
			ui.JoinCodeDigits = ""
		}
	}
	sc.dirty.Onboarding = true
	return nil
}

func (c *Core) handleOnboardingRecoveryEdit(act *corev1.OnboardingRecoveryEditAction) []*corev1.Effect {
	if act == nil {
		return nil
	}
	sc := c.m.ensureSec()
	if act.HasServerInput {
		sc.ui.RecServerInput = act.ServerInput
	}
	if act.HasCodeSavedChecked {
		sc.ui.RecCodeSaved = act.CodeSavedChecked
	}
	sc.dirty.Onboarding = true
	return nil
}

func (c *Core) prepareRecoveryOp(ctx *sync.StepContext, server, oldCode string) {
	ui := &c.m.ensureSec().ui
	c.runRecOp(ctx, &corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PrepareEmergencyRecovery{
			PrepareEmergencyRecovery: &corev1.SecurityServerCodeAction{Server: server, Code: oldCode},
		},
	}, func(ctx *sync.StepContext, res *corev1.SecurityWorkflowResultEffect) []*corev1.Effect {
		if res.RecoveryCode != "" {
			ui.RecNewCode = res.RecoveryCode
			ui.RecCodeSaved = false
		}
		c.syncRecoveryPhase(ctx, res)
		return nil
	})
}

func (c *Core) handleOnboardingRecoveryAction(ctx *sync.StepContext, act *corev1.OnboardingRecoveryAction) []*corev1.Effect {
	if act == nil {
		return nil
	}
	sc := c.m.ensureSec()
	ui := &sc.ui
	if ui.RecBusy {
		return nil
	}
	if act.ServerInput != "" {
		ui.RecServerInput = strings.TrimSpace(act.ServerInput)
	}

	switch act.Kind {
	case corev1.OnboardingRecoveryActionKind_ONBOARDING_RECOVERY_ACTION_KIND_PREPARE:
		server := strings.TrimSpace(ui.RecServerInput)
		if server == "" || strings.TrimSpace(act.OldCode) == "" {
			return nil
		}
		if c.clientCfg != nil && c.clientCfg.requestStoragePersist {
			ui.RecBusy, ui.RecBanner, sc.dirty.Onboarding = true, clearBanner, true
			return c.requestPersistEffect("recovery", server, act.OldCode)
		}
		c.prepareRecoveryOp(ctx, server, act.OldCode)
	case corev1.OnboardingRecoveryActionKind_ONBOARDING_RECOVERY_ACTION_KIND_CONTINUE_SIGNING:
		c.runRecOp(ctx, &corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_ContinueRecoverySigning{ContinueRecoverySigning: secStr(act.OldCode)}},
			func(ctx *sync.StepContext, res *corev1.SecurityWorkflowResultEffect) []*corev1.Effect {
				c.syncRecoveryPhase(ctx, res)
				return nil
			})
	case corev1.OnboardingRecoveryActionKind_ONBOARDING_RECOVERY_ACTION_KIND_CONFIRM_CODE_SAVED:
		ui.RecCodeSaved = true
		c.runRecOp(ctx, &corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_ConfirmRecoveryCodeSaved{ConfirmRecoveryCodeSaved: emptySec}},
			func(_ *sync.StepContext, _ *corev1.SecurityWorkflowResultEffect) []*corev1.Effect {
				ui.RecCodeSaved = true
				return nil
			})
	case corev1.OnboardingRecoveryActionKind_ONBOARDING_RECOVERY_ACTION_KIND_REFRESH_OR_SUBMIT:
		c.runRecOp(ctx, &corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_ResumeRecovery{ResumeRecovery: emptySec}},
			func(ctx *sync.StepContext, res *corev1.SecurityWorkflowResultEffect) []*corev1.Effect {
				c.syncRecoveryPhase(ctx, res)
				return nil
			})
	case corev1.OnboardingRecoveryActionKind_ONBOARDING_RECOVERY_ACTION_KIND_COMPLETE:
		c.runRecOp(ctx, &corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_CompleteRecovery{CompleteRecovery: emptySec}},
			func(ctx *sync.StepContext, res *corev1.SecurityWorkflowResultEffect) []*corev1.Effect {
				c.m.sync.EmitSecurityEffects(ctx, c.m.sync.SecurityEngine().StopManagedPoll(security.PollRecoveryWindow))
				return c.enterReadyAccount(ctx, res.Status, res.AccountAdopted)
			})
	case corev1.OnboardingRecoveryActionKind_ONBOARDING_RECOVERY_ACTION_KIND_DISCARD_UNSIGNED,
		corev1.OnboardingRecoveryActionKind_ONBOARDING_RECOVERY_ACTION_KIND_DISCARD_REJECTED:
		ev := &corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_DiscardRejectedRecovery{DiscardRejectedRecovery: emptySec}}
		if act.Kind == corev1.OnboardingRecoveryActionKind_ONBOARDING_RECOVERY_ACTION_KIND_DISCARD_UNSIGNED {
			ev = &corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_DiscardUnsignedRecovery{DiscardUnsignedRecovery: emptySec}}
		}
		c.runRecOp(ctx, ev, func(ctx *sync.StepContext, _ *corev1.SecurityWorkflowResultEffect) []*corev1.Effect {
			c.m.sync.EmitSecurityEffects(ctx, c.m.sync.SecurityEngine().StopManagedPoll(security.PollRecoveryWindow))
			ui.RecPhase, ui.RecNewCode, ui.RecCodeSaved, ui.RecReadyMs, ui.RecBanner = corev1.SecurityRecoveryPhase_SECURITY_RECOVERY_PHASE_FORM, "", false, 0, clearBanner
			return nil
		})
	}
	return nil
}

// ---- AddDevice (Initiator Pairing) Handlers ----

func (c *Core) resetAddDeviceSession() {
	ui := &c.m.ensureSec().ui
	ui.AddStep, ui.AddPairingID, ui.AddRawCode, ui.AddQRURL, ui.AddExpiresMs = corev1.AddDeviceStep_ADD_DEVICE_STEP_IDLE, "", "", "", 0
	ui.AddSASEmoji, ui.AddJoinerKind, ui.AddSASConfirmed, ui.AddUserToggledCandidates = nil, v1.DeviceKind_DEVICE_KIND_UNSPECIFIED, false, false
	ui.AddReplaceOldLabel, ui.AddReplaceOldHint, ui.AddCandidates, ui.AddRemoveOldWarning = nil, nil, nil, nil
	ui.AddSelectedRemoveIDs, ui.AddBusy, ui.AddBanner = make(map[string]bool), false, clearBanner
}

func (c *Core) cancelActiveAddPairing(ctx *sync.StepContext, pid string) {
	if pid == "" {
		return
	}
	c.m.sync.EmitSecurityEffects(ctx, c.m.sync.SecurityEngine().StopManagedPoll(security.PollNone))
	c.runSecOp(ctx, &corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_CancelPairing{CancelPairing: secStr(pid)}}, nil)
}

func (c *Core) handleAddDeviceOpen(ctx *sync.StepContext, act *corev1.AddDeviceOpenAction) []*corev1.Effect {
	if act == nil {
		return nil
	}
	sc := c.m.ensureSec()
	ui := &sc.ui
	replacesChanged := act.ReplacesDeviceId != ui.AddReplacesDeviceID
	ui.AddOpen = true
	ui.AddReplacesDeviceID = act.ReplacesDeviceId
	ui.AddReplacesMachineName = c.m.sync.MachineDisplayName(act.ReplacesDeviceId)
	if act.WebOrigin != "" {
		ui.AddWebOrigin = act.WebOrigin
	}
	if replacesChanged || ui.AddStep == corev1.AddDeviceStep_ADD_DEVICE_STEP_DONE || ui.AddStep == corev1.AddDeviceStep_ADD_DEVICE_STEP_FAILED {
		if ui.AddStep == corev1.AddDeviceStep_ADD_DEVICE_STEP_WAITING_JOINER || ui.AddStep == corev1.AddDeviceStep_ADD_DEVICE_STEP_COMPARE_SAS {
			c.cancelActiveAddPairing(ctx, ui.AddPairingID)
		}
		c.resetAddDeviceSession()
	}
	if ui.Status != nil && ui.Status.PendingOp != nil && ui.Status.PendingOp.HasDelivery {
		ui.AddStep = corev1.AddDeviceStep_ADD_DEVICE_STEP_DELIVERING
		ui.AddPairingID = ui.Status.PendingOp.DeliveryPairingId
		c.m.sync.EmitSecurityEffects(ctx, c.m.sync.SecurityEngine().StartInitiatorDeliveryPoll(c.m.sync.NowMs(), ui.AddPairingID))
		sc.dirty.AddDevice = true
		return nil
	}
	if act.AutoStart && ui.Status != nil && ui.Status.IsAuthority && ui.AddStep == corev1.AddDeviceStep_ADD_DEVICE_STEP_IDLE {
		c.startAddDevicePairing(ctx)
	}
	sc.dirty.AddDevice = true
	return nil
}

func (c *Core) handleAddDeviceClose(ctx *sync.StepContext, act *corev1.AddDeviceCloseAction) []*corev1.Effect {
	sc := c.m.ensureSec()
	ui := &sc.ui
	if act != nil && act.ReplacesDeviceId != "" && act.ReplacesDeviceId != ui.AddReplacesDeviceID {
		return nil
	}
	ui.AddOpen = false
	if ui.AddStep == corev1.AddDeviceStep_ADD_DEVICE_STEP_WAITING_JOINER || ui.AddStep == corev1.AddDeviceStep_ADD_DEVICE_STEP_COMPARE_SAS {
		c.cancelActiveAddPairing(ctx, ui.AddPairingID)
		c.resetAddDeviceSession()
	} else if ui.AddStep == corev1.AddDeviceStep_ADD_DEVICE_STEP_DONE || ui.AddStep == corev1.AddDeviceStep_ADD_DEVICE_STEP_FAILED {
		c.resetAddDeviceSession()
	}
	sc.dirty.AddDevice = true
	return nil
}

func (c *Core) startAddDevicePairing(ctx *sync.StepContext) {
	sc := c.m.ensureSec()
	ui := &sc.ui
	if ui.AddBusy {
		return
	}
	c.resetAddDeviceSession()
	ui.AddStep = corev1.AddDeviceStep_ADD_DEVICE_STEP_GENERATING
	ui.AddBusy = true
	sc.dirty.AddDevice = true
	c.runSecOp(ctx, &corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_StartPairing{
			StartPairing: &corev1.SecurityStartPairingAction{
				WebOrigin:        ui.AddWebOrigin,
				ReplacesDeviceId: ui.AddReplacesDeviceID,
			},
		},
	}, func(ctx *sync.StepContext, res *corev1.SecurityWorkflowResultEffect, _ security.WorkflowMeta, errTxt *corev1.Text) []*corev1.Effect {
		ui.AddBusy = false
		sc.dirty.AddDevice = true
		if !res.Ok {
			ui.AddStep = corev1.AddDeviceStep_ADD_DEVICE_STEP_FAILED
			ui.AddBanner = errBanner(errTxt)
			return nil
		}
		if res.Pairing != nil {
			c.applyInitiatorPairingInfo(res.Pairing)
			ui.AddStep = corev1.AddDeviceStep_ADD_DEVICE_STEP_WAITING_JOINER
			c.m.sync.EmitSecurityEffects(ctx, c.m.sync.SecurityEngine().StartInitiatorJoinedPoll(c.m.sync.NowMs(), res.Pairing.PairingId))
		}
		return nil
	})
}

func (c *Core) handleAddDeviceAction(ctx *sync.StepContext, act *corev1.AddDeviceAction) []*corev1.Effect {
	if act == nil {
		return nil
	}
	sc := c.m.ensureSec()
	ui := &sc.ui
	switch act.Kind {
	case corev1.AddDeviceActionKind_ADD_DEVICE_ACTION_KIND_GENERATE:
		c.startAddDevicePairing(ctx)
	case corev1.AddDeviceActionKind_ADD_DEVICE_ACTION_KIND_SET_SAS_CONFIRMED:
		if ui.AddStep != corev1.AddDeviceStep_ADD_DEVICE_STEP_COMPARE_SAS {
			return nil
		}
		ui.AddSASConfirmed = act.Flag
	case corev1.AddDeviceActionKind_ADD_DEVICE_ACTION_KIND_TOGGLE_REMOVE_CANDIDATE:
		if act.DeviceId != "" {
			if ui.AddSelectedRemoveIDs == nil {
				ui.AddSelectedRemoveIDs = make(map[string]bool)
			}
			ui.AddUserToggledCandidates = true
			ui.AddSelectedRemoveIDs[act.DeviceId] = act.Flag
		}
	case corev1.AddDeviceActionKind_ADD_DEVICE_ACTION_KIND_APPROVE:
		if ui.AddBusy || ui.AddPairingID == "" || !ui.AddSASConfirmed || ui.AddStep != corev1.AddDeviceStep_ADD_DEVICE_STEP_COMPARE_SAS {
			return nil
		}
		c.m.sync.EmitSecurityEffects(ctx, c.m.sync.SecurityEngine().StopManagedPoll(security.PollNone))
		var removeIDs []string
		for _, cand := range ui.AddCandidates {
			if cand != nil && ui.AddSelectedRemoveIDs[cand.DeviceId] {
				removeIDs = append(removeIDs, cand.DeviceId)
			}
		}
		ui.AddStep = corev1.AddDeviceStep_ADD_DEVICE_STEP_APPROVING
		ui.AddBusy = true
		ui.AddBanner = clearBanner
		c.runSecOp(ctx, &corev1.SecurityWorkflowActionEvent{
			Action: &corev1.SecurityWorkflowActionEvent_ApprovePairing{
				ApprovePairing: &corev1.SecurityApprovePairingAction{
					PairingId:       ui.AddPairingID,
					SasConfirmed:    true,
					RemoveDeviceIds: removeIDs,
				},
			},
		}, func(ctx *sync.StepContext, res *corev1.SecurityWorkflowResultEffect, _ security.WorkflowMeta, errTxt *corev1.Text) []*corev1.Effect {
			ui.AddBusy = false
			sc.dirty.AddDevice = true
			if !res.Ok {
				ui.AddStep = corev1.AddDeviceStep_ADD_DEVICE_STEP_COMPARE_SAS
				if res.Uncertain {
					ui.AddBanner = warnBanner(errTxt)
				} else {
					ui.AddBanner = errBanner(errTxt)
				}
				return nil
			}
			if res.Pairing != nil {
				c.applyInitiatorPairingInfo(res.Pairing)
				if res.Pairing.Delivered {
					ui.AddStep = corev1.AddDeviceStep_ADD_DEVICE_STEP_DONE
					c.m.sync.RefreshDevices(ctx, true)
				} else {
					ui.AddStep = corev1.AddDeviceStep_ADD_DEVICE_STEP_DELIVERING
					c.m.sync.EmitSecurityEffects(ctx, c.m.sync.SecurityEngine().StartInitiatorDeliveryPoll(c.m.sync.NowMs(), res.Pairing.PairingId))
				}
			}
			return nil
		})
	case corev1.AddDeviceActionKind_ADD_DEVICE_ACTION_KIND_CANCEL:
		c.cancelActiveAddPairing(ctx, ui.AddPairingID)
		c.resetAddDeviceSession()
	case corev1.AddDeviceActionKind_ADD_DEVICE_ACTION_KIND_RETRY:
		if ui.AddStep == corev1.AddDeviceStep_ADD_DEVICE_STEP_DELIVERING && ui.AddPairingID != "" {
			pid := ui.AddPairingID
			ui.AddBusy = true
			ui.AddBanner = clearBanner
			c.runSecOp(ctx, &corev1.SecurityWorkflowActionEvent{
				Action: &corev1.SecurityWorkflowActionEvent_FinishPairingDelivery{
					FinishPairingDelivery: &corev1.SecurityStringAction{Value: pid},
				},
			}, func(ctx *sync.StepContext, res *corev1.SecurityWorkflowResultEffect, _ security.WorkflowMeta, errTxt *corev1.Text) []*corev1.Effect {
				ui.AddBusy = false
				sc.dirty.AddDevice = true
				if !res.Ok {
					ui.AddBanner = warnBanner(errTxt)
					return nil
				}
				if res.Pairing != nil && res.Pairing.Delivered {
					c.applyInitiatorPairingInfo(res.Pairing)
					ui.AddStep = corev1.AddDeviceStep_ADD_DEVICE_STEP_DONE
					c.m.sync.EmitSecurityEffects(ctx, c.m.sync.SecurityEngine().StopManagedPoll(security.PollInitiatorDelivery))
					c.m.sync.RefreshDevices(ctx, true)
				} else {
					c.m.sync.EmitSecurityEffects(ctx, c.m.sync.SecurityEngine().StartInitiatorDeliveryPoll(c.m.sync.NowMs(), pid))
				}
				return nil
			})
		} else {
			c.startAddDevicePairing(ctx)
		}
	}
	sc.dirty.AddDevice = true
	return nil
}

// ---- AccountSecurity Handlers ----

func (c *Core) clearRotationCodeInMemory() {
	ui := &c.m.ensureSec().ui
	ui.SecRotationCode = ""
	ui.SecRotationCodeSaved = false
}

func (c *Core) handleAccountSecurityOpen(ctx *sync.StepContext, act *corev1.AccountSecurityOpenAction) []*corev1.Effect {
	sc := c.m.ensureSec()
	ui := &sc.ui
	if act != nil && act.Subpage != corev1.AccountSecuritySubpage_ACCOUNT_SECURITY_SUBPAGE_UNSPECIFIED {
		ui.SecSubpage = act.Subpage
	} else {
		ui.SecSubpage = corev1.AccountSecuritySubpage_ACCOUNT_SECURITY_SUBPAGE_OVERVIEW
	}
	ui.SecSuccessNotice = nil
	ui.SecErrorBanner = clearBanner
	if ui.Initialized || ui.Status != nil {
		c.runSecOp(ctx, &corev1.SecurityWorkflowActionEvent{
			Action: &corev1.SecurityWorkflowActionEvent_SyncNow{SyncNow: &corev1.SecuritySyncNowAction{}},
		}, func(_ *sync.StepContext, res *corev1.SecurityWorkflowResultEffect, _ security.WorkflowMeta, errTxt *corev1.Text) []*corev1.Effect {
			ui.SecBusy = false
			sc.dirty.AccountSecurity = true
			if !res.Ok {
				ui.SecErrorBanner = errBanner(errTxt)
			}
			return nil
		})
	}
	c.m.sync.RefreshDevices(ctx, false)
	sc.dirty.AccountSecurity = true
	return nil
}

func (c *Core) handleAccountSecurityClose() []*corev1.Effect {
	sc := c.m.ensureSec()
	c.clearRotationCodeInMemory()
	sc.ui.SecSubpage = corev1.AccountSecuritySubpage_ACCOUNT_SECURITY_SUBPAGE_OVERVIEW
	sc.dirty.AccountSecurity = true
	return nil
}

func (c *Core) handleAccountSecurityAction(ctx *sync.StepContext, act *corev1.AccountSecurityAction) []*corev1.Effect {
	if act == nil {
		return nil
	}
	sc := c.m.ensureSec()
	ui := &sc.ui
	switch act.Kind {
	case corev1.AccountSecurityActionKind_ACCOUNT_SECURITY_ACTION_KIND_SET_SUBPAGE:
		if act.Subpage != corev1.AccountSecuritySubpage_ACCOUNT_SECURITY_SUBPAGE_UNSPECIFIED {
			if ui.SecSubpage == corev1.AccountSecuritySubpage_ACCOUNT_SECURITY_SUBPAGE_ROTATE_RECOVERY &&
				act.Subpage != corev1.AccountSecuritySubpage_ACCOUNT_SECURITY_SUBPAGE_ROTATE_RECOVERY {
				c.clearRotationCodeInMemory()
			}
			ui.SecSubpage = act.Subpage
		}
		if act.Subpage != corev1.AccountSecuritySubpage_ACCOUNT_SECURITY_SUBPAGE_OVERVIEW {
			ui.SecSuccessNotice = nil
		}
		ui.SecErrorBanner = clearBanner

	case corev1.AccountSecurityActionKind_ACCOUNT_SECURITY_ACTION_KIND_REFRESH:
		c.runAccountOp(ctx, &corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_SyncNow{SyncNow: &corev1.SecuritySyncNowAction{}}}, nil)
		c.m.sync.RefreshDevices(ctx, true)
	case corev1.AccountSecurityActionKind_ACCOUNT_SECURITY_ACTION_KIND_RENEW_LEASE:
		if ui.SecBusy {
			return nil
		}
		if ui.Status != nil && ui.Status.PendingOp != nil && (ui.Status.PendingOp.Kind == security.PendingOpKindState || ui.Status.PendingOp.Kind == security.PendingOpKindLease) {
			ui.SecErrorBanner, sc.dirty.AccountSecurity = errBanner(text.Msg(text.KeySecurityFinishTheCurrentSecurityOperationBeforeRenewing)), true
			return nil
		}
		c.runAccountOp(ctx, &corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_RenewLease{RenewLease: secBool(true)}},
			func(_ *sync.StepContext, res *corev1.SecurityWorkflowResultEffect) {
				if res.Status != nil && res.Status.PendingOp != nil && (res.Status.PendingOp.Kind == security.PendingOpKindState || res.Status.PendingOp.Kind == security.PendingOpKindLease) {
					ui.SecErrorBanner = errBanner(text.Msg(text.KeySecurityFinishTheCurrentSecurityOperationBeforeRenewing))
					return
				}
				ui.SecSuccessNotice = text.Msg(text.KeySecurityRemoteControlRenewed)
			})
	case corev1.AccountSecurityActionKind_ACCOUNT_SECURITY_ACTION_KIND_CONTINUE_PENDING,
		corev1.AccountSecurityActionKind_ACCOUNT_SECURITY_ACTION_KIND_RETRY_PENDING_REMOVE:
		if ui.SecBusy {
			return nil
		}
		c.runAccountOp(ctx, &corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_ResumePendingOp{ResumePendingOp: emptySec}},
			func(ctx *sync.StepContext, _ *corev1.SecurityWorkflowResultEffect) {
				c.m.sync.RefreshDevices(ctx, true)
				if (ui.Status == nil || ui.Status.PendingOp == nil) && ui.SecSubpage == corev1.AccountSecuritySubpage_ACCOUNT_SECURITY_SUBPAGE_PENDING_OP {
					ui.SecSubpage = corev1.AccountSecuritySubpage_ACCOUNT_SECURITY_SUBPAGE_OVERVIEW
				}
			})
	case corev1.AccountSecurityActionKind_ACCOUNT_SECURITY_ACTION_KIND_DISCARD_PENDING:
		if ui.SecBusy {
			return nil
		}
		// Unanimous policy: confirmed offer abandon always passes abandonHandoff=true.
		abandonHandoff := act.Flag || (ui.Status != nil && ui.Status.PendingOp != nil && ui.Status.PendingOp.Kind == security.PendingOpKindOffer)
		c.runAccountOp(ctx, &corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_DiscardPendingOp{DiscardPendingOp: secBool(abandonHandoff)}},
			func(_ *sync.StepContext, _ *corev1.SecurityWorkflowResultEffect) {
				c.clearRotationCodeInMemory()
				ui.HandoffPreparedCode, ui.HandoffCodeSaved, sc.dirty.Handoff = "", false, true
				if ui.SecSubpage == corev1.AccountSecuritySubpage_ACCOUNT_SECURITY_SUBPAGE_PENDING_OP {
					ui.SecSubpage = corev1.AccountSecuritySubpage_ACCOUNT_SECURITY_SUBPAGE_OVERVIEW
				}
			})
	case corev1.AccountSecurityActionKind_ACCOUNT_SECURITY_ACTION_KIND_PREPARE_ROTATION:
		if ui.SecBusy {
			return nil
		}
		c.runAccountOp(ctx, &corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_PrepareRecoveryRotation{PrepareRecoveryRotation: emptySec}},
			func(_ *sync.StepContext, res *corev1.SecurityWorkflowResultEffect) {
				ui.SecRotationCode, ui.SecRotationCodeSaved = res.RecoveryCode, false
			})
	case corev1.AccountSecurityActionKind_ACCOUNT_SECURITY_ACTION_KIND_SET_ROTATION_SAVED:
		ui.SecRotationCodeSaved = act.Flag
	case corev1.AccountSecurityActionKind_ACCOUNT_SECURITY_ACTION_KIND_PUBLISH_ROTATION:
		if ui.SecBusy || !ui.SecRotationCodeSaved {
			return nil
		}
		c.runAccountOp(ctx, &corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_ConfirmRecoveryRotation{ConfirmRecoveryRotation: emptySec}},
			func(_ *sync.StepContext, _ *corev1.SecurityWorkflowResultEffect) {
				c.clearRotationCodeInMemory()
				ui.SecSuccessNotice = text.Msg(text.KeySecurityRecoveryCodeUpdatedThePreviousCodeAndPendingRecovery)
			})
	}
	sc.dirty.AccountSecurity = true
	return nil
}

// ---- Handoff Handlers ----

func (c *Core) prepareHandoffCodeOp(ctx *sync.StepContext, persisted bool) {
	ui := &c.m.ensureSec().ui
	c.runHandoffOp(ctx, &corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_PrepareHandoffCode{PrepareHandoffCode: secBool(persisted)}},
		func(_ *sync.StepContext, res *corev1.SecurityWorkflowResultEffect) {
			ui.HandoffPreparedCode, ui.HandoffCodeSaved = res.RecoveryCode, false
		})
}

func (c *Core) handleHandoffAction(ctx *sync.StepContext, act *corev1.HandoffAction) []*corev1.Effect {
	if act == nil {
		return nil
	}
	sc := c.m.ensureSec()
	ui := &sc.ui
	switch act.Kind {
	case corev1.HandoffActionKind_HANDOFF_ACTION_KIND_OPEN,
		corev1.HandoffActionKind_HANDOFF_ACTION_KIND_REFRESH_CANDIDATES:
		ui.HandoffMessageBanner, ui.HandoffErrorBanner = nil, clearBanner
		if ui.Status != nil && ui.Status.IsAuthority {
			c.runHandoffOp(ctx, &corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_ListHandoffCandidates{ListHandoffCandidates: emptySec}},
				func(_ *sync.StepContext, res *corev1.SecurityWorkflowResultEffect) {
					ui.HandoffCandidates = res.HandoffCandidates
				})
			c.m.sync.RefreshDevices(ctx, act.Kind == corev1.HandoffActionKind_HANDOFF_ACTION_KIND_REFRESH_CANDIDATES)
		}
	case corev1.HandoffActionKind_HANDOFF_ACTION_KIND_REQUEST_TRANSFER:
		ui.HandoffConfirmTargetID, ui.HandoffErrorBanner = act.TargetDeviceId, clearBanner
	case corev1.HandoffActionKind_HANDOFF_ACTION_KIND_CANCEL_TRANSFER:
		ui.HandoffConfirmTargetID = ""
	case corev1.HandoffActionKind_HANDOFF_ACTION_KIND_CONFIRM_TRANSFER:
		// Core enforces REQUEST_TRANSFER prior to CONFIRM_TRANSFER.
		if ui.HandoffBusy || ui.HandoffConfirmTargetID == "" || (act.TargetDeviceId != "" && act.TargetDeviceId != ui.HandoffConfirmTargetID) {
			return nil
		}
		targetID := ui.HandoffConfirmTargetID
		ui.HandoffConfirmTargetID = ""
		c.runHandoffOp(ctx, &corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_HandoffAuthority{HandoffAuthority: secStr(targetID)}},
			func(ctx *sync.StepContext, _ *corev1.SecurityWorkflowResultEffect) {
				sc.dirty.AccountSecurity = true
				if ui.Status != nil && ui.Status.PendingOp != nil {
					ui.HandoffErrorBanner = warnBanner(text.Msg(text.KeySecurityTransferResultUnverifiedContinueTheOriginalOperation))
				} else {
					ui.HandoffMessageBanner = text.Msg(text.KeySecurityManagementTransferredWaitingForTheSuccessorToCompleteTakeover)
				}
				c.m.sync.RefreshDevices(ctx, true)
			})
	case corev1.HandoffActionKind_HANDOFF_ACTION_KIND_PREPARE_CODE:
		if ui.HandoffBusy {
			return nil
		}
		if c.clientCfg != nil && c.clientCfg.requestStoragePersist {
			ui.HandoffBusy, ui.HandoffMessageBanner, ui.HandoffErrorBanner, sc.dirty.Handoff = true, nil, clearBanner, true
			return c.requestPersistEffect("handoff", "", "")
		}
		c.prepareHandoffCodeOp(ctx, true)
	case corev1.HandoffActionKind_HANDOFF_ACTION_KIND_SET_CODE_SAVED:
		ui.HandoffCodeSaved = act.Flag
	case corev1.HandoffActionKind_HANDOFF_ACTION_KIND_PUBLISH_OFFER:
		if ui.HandoffBusy || !ui.HandoffCodeSaved {
			return nil
		}
		c.runHandoffOp(ctx, &corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_PublishHandoffOffer{PublishHandoffOffer: emptySec}},
			func(ctx *sync.StepContext, _ *corev1.SecurityWorkflowResultEffect) {
				sc.dirty.AccountSecurity, ui.HandoffPreparedCode, ui.HandoffCodeSaved = true, "", false
				ui.HandoffMessageBanner = text.Msg(text.KeySecurityRecoveryCodeSavedAndConfirmedWaitingForTheCurrent)
				c.m.sync.RefreshDevices(ctx, true)
			})
	case corev1.HandoffActionKind_HANDOFF_ACTION_KIND_RESTORE_PUBLIC_PROFILE:
		if ui.HandoffBusy || strings.TrimSpace(act.RecoveryCode) == "" {
			return nil
		}
		srv := c.m.sync.SecurityEngine().EffectiveServer()
		c.runHandoffOp(ctx, &corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_CompleteAccountPublicProfile{
			CompleteAccountPublicProfile: &corev1.SecurityServerCodeAction{Server: srv, Code: act.RecoveryCode},
		}}, func(_ *sync.StepContext, _ *corev1.SecurityWorkflowResultEffect) {
			ui.HandoffMessageBanner = text.Msg(text.KeySecurityAccountPublicKeyRestored)
		})
	}
	sc.dirty.Handoff = true
	return nil
}

// ---- Exit Flow (Two-Phase Storage Re-Read Risk Check + Push Unregister + In-Place Reset) ----

func (c *Core) classifyExitRisk(advice *corev1.SecurityExitAdvice, status *corev1.SecurityWorkflowStatus, checkFailed bool, intent corev1.ExitIntentKind) screens.ExitRisk {
	if checkFailed && intent == corev1.ExitIntentKind_EXIT_INTENT_KIND_SETUP_RESET {
		return screens.ExitRiskCorruptForceReset
	}
	if status != nil && status.Bootstrap == corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_PENDING_REGISTRATION {
		return screens.ExitRiskUnfinishedJoin
	}
	if advice != nil {
		if advice.Unresolved {
			return screens.ExitRiskUnresolvedSecurityOp
		}
		if advice.Authority {
			return screens.ExitRiskOnlyManager
		}
	}
	return screens.ExitRiskOrdinaryMember
}

func (c *Core) handleExitRequest(ctx *sync.StepContext, act *corev1.ExitRequestAction) []*corev1.Effect {
	if act == nil || act.Intent == corev1.ExitIntentKind_EXIT_INTENT_KIND_NONE {
		return nil
	}
	sc := c.m.ensureSec()
	ui := &sc.ui
	if ui.Exit.Step == corev1.ExitConfirmStep_EXIT_CONFIRM_STEP_CHECKING || ui.Exit.Step == corev1.ExitConfirmStep_EXIT_CONFIRM_STEP_EXECUTING {
		return nil
	}
	srv := strings.TrimSpace(act.Server)
	if srv == "" {
		srv = c.m.sync.SecurityEngine().EffectiveServer()
	}
	if srv == "" && ui.Status != nil {
		srv = ui.Status.Server
	}
	if srv == "" {
		srv = ui.RetainedServer
	}
	ui.Exit = screens.SecurityExitState{
		Intent:         act.Intent,
		Step:           corev1.ExitConfirmStep_EXIT_CONFIRM_STEP_CHECKING,
		Server:         srv,
		PreserveServer: srv,
	}
	sc.dirty.ExitConfirm = true
	c.runSecOp(ctx, &corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_CheckExitAdvice{CheckExitAdvice: &corev1.SecurityActionEmpty{}},
	}, func(ctx *sync.StepContext, res *corev1.SecurityWorkflowResultEffect, _ security.WorkflowMeta, errTxt *corev1.Text) []*corev1.Effect {
		sc.dirty.ExitConfirm = true
		if !res.Ok && ui.Exit.Intent != corev1.ExitIntentKind_EXIT_INTENT_KIND_SETUP_RESET {
			ui.Exit.Step = corev1.ExitConfirmStep_EXIT_CONFIRM_STEP_FAILED
			ui.Exit.ErrorText = errTxt
			return nil
		}
		risk := c.classifyExitRisk(res.ExitAdvice, ui.Status, !res.Ok, ui.Exit.Intent)
		ui.Exit.Risk = risk
		if res.ExitAdvice != nil {
			ui.Exit.AdviceMessage = res.ExitAdvice.MessageText
		}
		if ui.Exit.Intent == corev1.ExitIntentKind_EXIT_INTENT_KIND_REJOIN_PAIRING && risk == screens.ExitRiskOrdinaryMember {
			return c.beginExitExecution(ctx)
		}
		ui.Exit.Step = corev1.ExitConfirmStep_EXIT_CONFIRM_STEP_AWAITING_CONFIRM
		return nil
	})
	return nil
}

func (c *Core) handleExitConfirm(ctx *sync.StepContext, act *corev1.ExitConfirmAction) []*corev1.Effect {
	if act == nil {
		return nil
	}
	sc := c.m.ensureSec()
	ui := &sc.ui
	if !act.Confirm {
		if ui.Exit.Step == corev1.ExitConfirmStep_EXIT_CONFIRM_STEP_EXECUTING {
			return nil
		}
		ui.Exit = screens.SecurityExitState{Step: corev1.ExitConfirmStep_EXIT_CONFIRM_STEP_CLOSED}
		sc.dirty.ExitConfirm = true
		return nil
	}
	if ui.Exit.Step != corev1.ExitConfirmStep_EXIT_CONFIRM_STEP_AWAITING_CONFIRM &&
		ui.Exit.Step != corev1.ExitConfirmStep_EXIT_CONFIRM_STEP_FAILED {
		return nil
	}
	if ui.Exit.Intent == corev1.ExitIntentKind_EXIT_INTENT_KIND_SETUP_RESET {
		return c.beginExitExecution(ctx)
	}
	ui.Exit.Step = corev1.ExitConfirmStep_EXIT_CONFIRM_STEP_CHECKING
	ui.Exit.ErrorText = nil
	sc.dirty.ExitConfirm = true
	c.runSecOp(ctx, &corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_CheckExitAdvice{CheckExitAdvice: &corev1.SecurityActionEmpty{}},
	}, func(ctx *sync.StepContext, res *corev1.SecurityWorkflowResultEffect, _ security.WorkflowMeta, errTxt *corev1.Text) []*corev1.Effect {
		sc.dirty.ExitConfirm = true
		if !res.Ok {
			ui.Exit.Step = corev1.ExitConfirmStep_EXIT_CONFIRM_STEP_FAILED
			ui.Exit.ErrorText = errTxt
			return nil
		}
		nextRisk := c.classifyExitRisk(res.ExitAdvice, ui.Status, false, ui.Exit.Intent)
		if res.ExitAdvice != nil {
			ui.Exit.AdviceMessage = res.ExitAdvice.MessageText
		}
		if nextRisk != ui.Exit.Risk {
			ui.Exit.Risk = nextRisk
			ui.Exit.Step = corev1.ExitConfirmStep_EXIT_CONFIRM_STEP_AWAITING_CONFIRM
			return nil
		}
		return c.beginExitExecution(ctx)
	})
	return nil
}

func (c *Core) beginExitExecution(ctx *sync.StepContext) []*corev1.Effect {
	sc := c.m.ensureSec()
	ui := &sc.ui
	ui.Exit.Step = corev1.ExitConfirmStep_EXIT_CONFIRM_STEP_EXECUTING
	ui.Exit.ErrorText = nil
	sc.dirty.ExitConfirm = true

	srv := ui.Exit.Server
	if srv == "" {
		srv = c.m.sync.SecurityEngine().EffectiveServer()
	}
	if srv != "" {
		pushOpID := c.allocSecEffectOpID()
		ui.Exit.PushOpID = pushOpID
		c.m.sync.ScheduleAppTimer(ctx, corev1.TimerKind_TIMER_KIND_UI_TRANSIENT, exitPushUnregisterTimerKey, exitPushUnregisterTimeoutMs)
		tok := c.m.sync.SecurityEngine().CachedAuthToken()
		// When leaving from RETAINED_DENIED / REMOVED / cold SETUP_RESET without a cached token,
		// attempt a best-effort SyncNow (bounded by the 3s exit timer) to obtain a bearer token so
		// the relay push subscription can be unregistered before clearing local keys.
		if tok == "" && ui.Status != nil && ui.Status.DeviceId != "" {
			c.runSecOp(ctx, &corev1.SecurityWorkflowActionEvent{
				Action: &corev1.SecurityWorkflowActionEvent_SyncNow{SyncNow: &corev1.SecuritySyncNowAction{}},
			}, func(_ *sync.StepContext, _ *corev1.SecurityWorkflowResultEffect, _ security.WorkflowMeta, _ *corev1.Text) []*corev1.Effect {
				if ui.Exit.Step != corev1.ExitConfirmStep_EXIT_CONFIRM_STEP_EXECUTING || ui.Exit.PushOpID != pushOpID {
					return nil
				}
				return []*corev1.Effect{{
					Effect: &corev1.Effect_UnregisterPushOnExit{
						UnregisterPushOnExit: &corev1.UnregisterPushOnExitEffect{
							OpId:      pushOpID,
							Server:    srv,
							AuthToken: c.m.sync.SecurityEngine().CachedAuthToken(),
						},
					},
				}}
			})
			return nil
		}
		return []*corev1.Effect{{
			Effect: &corev1.Effect_UnregisterPushOnExit{
				UnregisterPushOnExit: &corev1.UnregisterPushOnExitEffect{
					OpId:      pushOpID,
					Server:    srv,
					AuthToken: tok,
				},
			},
		}}
	}
	c.proceedExitClearSecurity(ctx)
	return nil
}

func (c *Core) handlePushUnregistered(ctx *sync.StepContext, ev *corev1.PushUnregisteredEvent) []*corev1.Effect {
	ui := &c.m.ensureSec().ui
	if ev == nil || ui.Exit.Step != corev1.ExitConfirmStep_EXIT_CONFIRM_STEP_EXECUTING || ui.Exit.PushOpID == 0 || ui.Exit.PushOpID != ev.OpId {
		return nil
	}
	ui.Exit.PushOpID = 0
	c.m.sync.CancelAppTimer(ctx, corev1.TimerKind_TIMER_KIND_UI_TRANSIENT, exitPushUnregisterTimerKey)
	c.proceedExitClearSecurity(ctx)
	return nil
}

func (c *Core) handleExitPushUnregisterTimeout(ctx *sync.StepContext) []*corev1.Effect {
	ui := &c.m.ensureSec().ui
	if ui.Exit.Step != corev1.ExitConfirmStep_EXIT_CONFIRM_STEP_EXECUTING || ui.Exit.PushOpID == 0 {
		return nil
	}
	ui.Exit.PushOpID = 0
	c.proceedExitClearSecurity(ctx)
	return nil
}

func (c *Core) proceedExitClearSecurity(ctx *sync.StepContext) {
	sc := c.m.ensureSec()
	ui := &sc.ui
	// Invalidate in-flight login/sync RPCs from the outgoing account so late responses
	// arriving after the 3s push-unregister timeout cannot write old tokens back to memory/storage.
	c.m.sync.InvalidateAccountAuthForExit(ctx)
	c.runSecOp(ctx, &corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_ClearSecurity{ClearSecurity: emptySec}},
		func(_ *sync.StepContext, res *corev1.SecurityWorkflowResultEffect, _ security.WorkflowMeta, errTxt *corev1.Text) []*corev1.Effect {
			sc.dirty.ExitConfirm = true
			if !res.Ok {
				c.m.sync.ResumeAccountAuthAfterFailedExit()
				ui.Exit.Step, ui.Exit.ErrorText = corev1.ExitConfirmStep_EXIT_CONFIRM_STEP_FAILED, errTxt
				return nil
			}
			cacheOpID := c.allocSecEffectOpID()
			ui.Exit.CacheOpID = cacheOpID
			var out []*corev1.Effect
			if ui.Exit.PreserveServer != "" {
				out = append(out, &corev1.Effect{Effect: &corev1.Effect_PersistLastServer{PersistLastServer: &corev1.PersistLastServerEffect{Server: ui.Exit.PreserveServer}}})
			}
			return append(out, &corev1.Effect{Effect: &corev1.Effect_ClearAccountCaches{ClearAccountCaches: &corev1.ClearAccountCachesEffect{OpId: cacheOpID, PreserveServer: ui.Exit.PreserveServer}}})
		})
}

func (c *Core) handleAccountCachesCleared(ctx *sync.StepContext, ev *corev1.AccountCachesClearedEvent) []*corev1.Effect {
	sc := c.m.ensureSec()
	ui := &sc.ui
	if ev == nil || ui.Exit.Step != corev1.ExitConfirmStep_EXIT_CONFIRM_STEP_EXECUTING || ui.Exit.CacheOpID == 0 || ui.Exit.CacheOpID != ev.OpId {
		return nil
	}
	ui.Exit.CacheOpID = 0
	if ev.Error != "" {
		ui.Exit.Step = corev1.ExitConfirmStep_EXIT_CONFIRM_STEP_FAILED
		ui.Exit.ErrorText = text.Msg(text.KeyErrorsCommandFailed)
		sc.dirty.ExitConfirm = true
		return nil
	}
	return c.executeInPlaceExitReset(ctx, ui.Exit.Intent, ui.Exit.PreserveServer)
}

func (c *Core) handleExternalLogout(ctx *sync.StepContext) []*corev1.Effect {
	return c.executeInPlaceExitReset(ctx, corev1.ExitIntentKind_EXIT_INTENT_KIND_LEAVE_ACCOUNT, "")
}

// executeInPlaceExitReset stops sync, cancels every active timer/stream/direct route,
// discards the old Model completely while preserving monotonic op_id counters, and
// re-initializes Core in place into ONBOARDING with non-account client config preserved.
func (c *Core) executeInPlaceExitReset(ctx *sync.StepContext, intent corev1.ExitIntentKind, preserveServer string) []*corev1.Effect {
	c.stopConnCheck(ctx, true)
	c.m.sync.StopAllForLogout(ctx)
	stopEffects := append([]*corev1.Effect(nil), ctx.Raw...)
	ctx.Raw = nil
	for _, sc := range ctx.Settled {
		meta, ok := c.m.pendingCmdMeta[sc.RequestID]
		if !ok || meta.kind != cmdKindPingMachine {
			continue
		}
		delete(c.m.pendingCmdMeta, sc.RequestID)
		errCode := sc.ErrorCode
		if errCode == "" {
			errCode = "canceled"
		}
		errText := screens.ReasonText(sc.ErrorReason)
		if errText == nil {
			errText = text.Msg(text.KeyErrorsCommandFailed)
		}
		delete(c.m.pingReqByOp, meta.opID)
		stopEffects = append(stopEffects, &corev1.Effect{
			Effect: &corev1.Effect_PingMachineResult{
				PingMachineResult: &corev1.PingMachineResultEffect{
					OpId:      meta.opID,
					Ok:        false,
					ErrorCode: errCode,
					ErrorText: errText,
				},
			},
		})
	}
	ctx.Settled = nil

	// Read live non-account state and monotonic high-water counters from the outgoing Model/Engines.
	nowMs := c.m.sync.NowMs()
	monoMs := c.m.sync.MonoMs()
	cal := c.m.sync.Calendar()
	uiLocale := c.m.state.UILocale
	fg := c.m.sync.SecurityEngine().IsForeground()
	netAvail, netSig := c.m.sync.NetworkState()
	if v := c.m.sync.NextOpSeq(); v > c.nextSyncOpSeq {
		c.nextSyncOpSeq = v
	}
	if v := c.m.sync.NextRelayOpSeq(); v > c.nextRelayOpSeq {
		c.nextRelayOpSeq = v
	}
	if v := c.m.sync.NextLegSeq(); v > c.nextLegSeq {
		c.nextLegSeq = v
	}
	if v := c.m.sync.SecurityEngine().NextOpID(); v > c.nextSecOpID {
		c.nextSecOpID = v
	}

	c.initModel()
	if nowMs > 0 || monoMs > 0 {
		c.m.sync.HandleEvent(&corev1.Event{
			NowMs:  nowMs,
			MonoMs: monoMs,
			Event: &corev1.Event_ClockUpdated{
				ClockUpdated: &corev1.LocalCalendar{
					NowMs:            nowMs,
					TodayStartMs:     cal.TodayStartMs,
					YesterdayStartMs: cal.YesterdayStartMs,
					WeekStartMs:      cal.WeekStartMs,
					NowYear:          int32(cal.NowYear),
				},
			},
		})
	}
	c.applyRuntimeConfigToModel(uiLocale, fg, netAvail, netSig)

	sc := c.m.ensureSec()
	ui := &sc.ui
	ui.Initialized = true
	ui.Stage = corev1.AppRootStage_APP_ROOT_STAGE_ONBOARDING
	defaultSrv := preserveServer
	if defaultSrv == "" && c.clientCfg != nil {
		defaultSrv = c.clientCfg.defaultServer
	}
	ui.EntryDefaultServer = defaultSrv
	ui.CreateServerInput = defaultSrv
	ui.JoinServerInput = defaultSrv
	ui.RecServerInput = defaultSrv
	if intent == corev1.ExitIntentKind_EXIT_INTENT_KIND_REJOIN_PAIRING {
		ui.Mode = corev1.OnboardingMode_ONBOARDING_MODE_JOIN
	} else {
		ui.Mode = corev1.OnboardingMode_ONBOARDING_MODE_ENTRY
	}
	sc.lastRuntimeEmitted = true
	sc.lastRuntimeActive = false
	sc.lastRuntimeRemoved = false
	sc.dirty.markAll()

	out := make([]*corev1.Effect, 0, len(stopEffects)+2)
	out = append(out, stopEffects...)
	out = append(out, &corev1.Effect{
		Effect: &corev1.Effect_AccountRuntimeChanged{
			AccountRuntimeChanged: &corev1.AccountRuntimeChangedEffect{
				Active:  false,
				Removed: false,
				Server:  defaultSrv,
			},
		},
	})
	return out
}

// ---- Security Effect Interception & Routing ----

func (c *Core) processSecurityEffects(
	effects []*corev1.Effect,
	wfMeta map[uint64]security.WorkflowMeta,
	_ security.BootstrapErrorMeta,
	nowMs int64,
) []*corev1.Effect {
	sc := c.m.sec
	if sc == nil {
		return effects
	}
	if sc.refreshDevicesRequested {
		sc.refreshDevicesRequested = false
		prev, stepCtx := c.m.sync.BeginStep()
		c.m.sync.RefreshDevices(stepCtx, true)
		stepRes := c.m.sync.EndStep(prev, stepCtx)
		effects = append(effects, stepRes.Effects...)
	}

	out := make([]*corev1.Effect, 0, len(effects))
	queue := effects
	for pass := 0; pass < 8 && len(queue) > 0; pass++ {
		var nextQueue []*corev1.Effect
		for _, eff := range queue {
			if eff == nil {
				continue
			}
			switch k := eff.Effect.(type) {
			case *corev1.Effect_SecurityStatusChanged:
				if k.SecurityStatusChanged != nil && k.SecurityStatusChanged.Status != nil {
					c.onSecurityStatusUpdated(k.SecurityStatusChanged.Status, &nextQueue)
				}
				out = append(out, eff)

			case *corev1.Effect_SecurityWorkflowResult:
				res := k.SecurityWorkflowResult
				if res != nil && res.ActionId > internalSecActionBase {
					if onDone, ok := sc.pendingActions[res.ActionId]; ok {
						delete(sc.pendingActions, res.ActionId)
						meta := wfMeta[res.ActionId]
						if res.Status != nil {
							sc.ui.Status = res.Status
						}
						errTxt := res.ErrorText
						if text.IsEmpty(errTxt) && !res.Ok {
							errTxt = screens.SecurityWorkflowReasonText(meta.ErrReason, meta.ErrRaw)
						}
						prev, stepCtx := c.m.sync.BeginStep()
						extra := onDone(stepCtx, res, meta, errTxt)
						stepRes := c.m.sync.EndStep(prev, stepCtx)
						if len(stepRes.Effects) > 0 {
							screens.DecorateSecurityEffects(stepRes.Effects, stepRes.SecurityWorkflowMeta, stepRes.SecurityNotifyOutcomes, stepRes.SecurityBootstrapError, nowMs)
							for kID, m := range stepRes.SecurityWorkflowMeta {
								if wfMeta == nil {
									wfMeta = make(map[uint64]security.WorkflowMeta)
								}
								wfMeta[kID] = m
							}
							nextQueue = append(nextQueue, stepRes.Effects...)
						}
						if len(extra) > 0 {
							nextQueue = append(nextQueue, extra...)
						}
						continue
					}
					// Late internal result from before an in-place reset: discard silently.
					continue
				}
				if res != nil && res.Status != nil {
					c.onSecurityStatusUpdated(res.Status, &nextQueue)
				}
				out = append(out, eff)

			default:
				out = append(out, eff)
			}
		}
		queue = nextQueue
	}
	if len(queue) > 0 {
		out = append(out, queue...)
	}

	if c.reconcileAppRootFromSync(&out) {
		sc.dirty.AppRoot = true
	}
	return out
}

func (c *Core) onSecurityStatusUpdated(st *corev1.SecurityWorkflowStatus, emit *[]*corev1.Effect) {
	if st == nil {
		return
	}
	sc := c.m.ensureSec()
	ui := &sc.ui
	ui.Status = st
	if ui.Stage == corev1.AppRootStage_APP_ROOT_STAGE_READY && (st.Removed || st.Bootstrap == corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_REMOVED) {
		ui.Stage = corev1.AppRootStage_APP_ROOT_STAGE_REMOVED
		ui.RemovalVerified = true
		c.emitRuntimeChangedIfNeeded(emit, false, true, st.Server, st.DeviceId, st.AccountId, false)
	}
	sc.dirty.AppRoot = true
	sc.dirty.AccountSecurity = true
	sc.dirty.Handoff = true
	if ui.AddOpen {
		sc.dirty.AddDevice = true
	}
}

func (c *Core) reconcileAppRootFromSync(emit *[]*corev1.Effect) bool {
	sc := c.m.ensureSec()
	ui := &sc.ui
	if !ui.Initialized {
		return false
	}
	syncRemoved := c.m.sync.SyncRemoved()
	if ui.Stage == corev1.AppRootStage_APP_ROOT_STAGE_READY && syncRemoved {
		ui.Stage = corev1.AppRootStage_APP_ROOT_STAGE_REMOVED
		if ui.Status != nil && ui.Status.Removed {
			ui.RemovalVerified = true
		}
		var srv, devID, accID string
		if ui.Status != nil {
			srv, devID, accID = ui.Status.Server, ui.Status.DeviceId, ui.Status.AccountId
		}
		c.emitRuntimeChangedIfNeeded(emit, false, true, srv, devID, accID, false)
		return true
	}
	if ui.Stage == corev1.AppRootStage_APP_ROOT_STAGE_REMOVED && !ui.RemovalVerified && !syncRemoved {
		ui.Stage = corev1.AppRootStage_APP_ROOT_STAGE_READY
		var srv, devID, accID string
		var needsRot bool
		if ui.Status != nil {
			srv, devID, accID, needsRot = ui.Status.Server, ui.Status.DeviceId, ui.Status.AccountId, ui.Status.NeedsRecoveryCodeRotation
		}
		c.emitRuntimeChangedIfNeeded(emit, true, false, srv, devID, accID, needsRot)
		return true
	}
	return false
}

func (c *Core) emitRuntimeChangedIfNeeded(emit *[]*corev1.Effect, active, removed bool, server, deviceID, accountID string, needsRotation bool) {
	sc := c.m.ensureSec()
	if sc.lastRuntimeEmitted &&
		sc.lastRuntimeActive == active &&
		sc.lastRuntimeRemoved == removed &&
		sc.lastRuntimeDeviceID == deviceID &&
		sc.lastRuntimeServer == server {
		return
	}
	sc.lastRuntimeEmitted = true
	sc.lastRuntimeActive = active
	sc.lastRuntimeRemoved = removed
	sc.lastRuntimeDeviceID = deviceID
	sc.lastRuntimeServer = server
	*emit = append(*emit, &corev1.Effect{
		Effect: &corev1.Effect_AccountRuntimeChanged{
			AccountRuntimeChanged: &corev1.AccountRuntimeChangedEffect{
				Active:                active,
				Removed:               removed,
				Server:                server,
				DeviceId:              deviceID,
				AccountId:             accountID,
				NeedsRecoveryRotation: needsRotation,
			},
		},
	})
}

func (c *Core) enterReadyAccount(ctx *sync.StepContext, st *corev1.SecurityWorkflowStatus, adopted *corev1.SecurityAccountAdopted) []*corev1.Effect {
	sc := c.m.ensureSec()
	ui := &sc.ui
	if st == nil && adopted != nil {
		st = c.m.sync.SecurityEngine().BuildWorkflowStatus()
		if st == nil {
			st = &corev1.SecurityWorkflowStatus{Bootstrap: corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_READY}
		}
	}
	var srv, devID, accID string
	var needsRot bool
	if st != nil {
		srv, devID, accID, needsRot = st.Server, st.DeviceId, st.AccountId, st.NeedsRecoveryCodeRotation
	}
	if adopted != nil {
		if adopted.Server != "" {
			srv = adopted.Server
		}
		if adopted.DeviceId != "" {
			devID = adopted.DeviceId
		}
		if adopted.AccountId != "" {
			accID = adopted.AccountId
		}
		needsRot = needsRot || adopted.NeedsRecoveryRotation
		if st != nil {
			st.Server, st.DeviceId, st.AccountId, st.NeedsRecoveryCodeRotation = srv, devID, accID, needsRot
		}
	}
	if st != nil {
		ui.Status = st
	}
	ui.Stage, ui.RemovalVerified = corev1.AppRootStage_APP_ROOT_STAGE_READY, false
	ui.CreateBusy, ui.CreateRecoveryCode, ui.CreateMissingCodeAck = false, "", false
	ui.JoinBusy, ui.JoinPendingRegistration, ui.JoinRegistering, ui.JoinPairingID = false, false, false, ""
	ui.RecBusy, ui.RecNewCode = false, ""
	sc.dirty.markAll()

	if st != nil && st.PendingOp != nil && st.PendingOp.HasDelivery && c.m.sync.SecurityEngine().IsForeground() {
		c.m.sync.EmitSecurityEffects(ctx, c.m.sync.SecurityEngine().StartInitiatorDeliveryPoll(c.m.sync.NowMs(), st.PendingOp.DeliveryPairingId))
	}
	var out []*corev1.Effect
	if st != nil {
		out = append(out, &corev1.Effect{Effect: &corev1.Effect_SecurityStatusChanged{SecurityStatusChanged: &corev1.SecurityStatusChangedEffect{Status: st}}})
	}
	if srv != "" {
		out = append(out, &corev1.Effect{Effect: &corev1.Effect_PersistLastServer{PersistLastServer: &corev1.PersistLastServerEffect{Server: srv}}})
	}
	c.emitRuntimeChangedIfNeeded(&out, true, false, srv, devID, accID, needsRot)
	return out
}

func (c *Core) landInEmptyOnboarding(ctx *sync.StepContext, existingRepair bool, existingServer string) {
	sc := c.m.ensureSec()
	ui := &sc.ui
	ui.Stage = corev1.AppRootStage_APP_ROOT_STAGE_ONBOARDING
	ui.JoinExistingRepair = existingRepair
	ui.JoinExistingServer = existingServer
	if sc.hasInitialPairLink {
		ui.Mode = corev1.OnboardingMode_ONBOARDING_MODE_JOIN
		ui.JoinCodeDigits = sc.initialPairCode
		if existingRepair && existingServer != "" {
			ui.JoinServerInput = existingServer
		} else {
			ui.JoinServerInput = sc.initialPairServer
		}
		ui.JoinCommitHint = sc.initialPairCommit
		ui.JoinRawLink = sc.initialPairRawLink
		sc.hasInitialPairLink = false
		c.startJoinPairing(ctx)
		return
	}
	if existingRepair && existingServer != "" {
		ui.Mode = corev1.OnboardingMode_ONBOARDING_MODE_JOIN
		ui.JoinServerInput = existingServer
		return
	}
	ui.Mode = corev1.OnboardingMode_ONBOARDING_MODE_ENTRY
}

func (c *Core) syncRecoveryPhase(ctx *sync.StepContext, res *corev1.SecurityWorkflowResultEffect) {
	ui := &c.m.ensureSec().ui
	var po *corev1.SecurityPendingOpSummary
	if res.Status != nil {
		po = res.Status.PendingOp
	}
	if res.Recovery != nil && res.Recovery.ReadyMs > 0 {
		ui.RecReadyMs = res.Recovery.ReadyMs
	} else if po != nil && po.RecoveryReadyMs > 0 {
		ui.RecReadyMs = po.RecoveryReadyMs
	}
	phase := screens.DeriveRecoveryPhase(res.Recovery, po)
	ui.RecPhase = phase
	switch phase {
	case corev1.SecurityRecoveryPhase_SECURITY_RECOVERY_PHASE_READY_TO_COMPLETE,
		corev1.SecurityRecoveryPhase_SECURITY_RECOVERY_PHASE_REJECTED:
		c.m.sync.EmitSecurityEffects(ctx, c.m.sync.SecurityEngine().StopManagedPoll(security.PollRecoveryWindow))
	case corev1.SecurityRecoveryPhase_SECURITY_RECOVERY_PHASE_SUBMITTED_WAITING:
		if res.Recovery != nil || (po != nil && po.RecoveryId != "") {
			c.m.sync.EmitSecurityEffects(ctx, c.m.sync.SecurityEngine().StartRecoveryWindowPoll(c.m.sync.NowMs(), ui.RecReadyMs))
		}
	}
}

func (c *Core) appendSecurityRender(effects []*corev1.Effect, syncDirty sync.DirtyFlags) []*corev1.Effect {
	sc := c.m.sec
	if sc == nil {
		return effects
	}
	sd := sc.dirty
	sc.dirty = securityDirtyFlags{}
	if syncDirty.Security && sc.ui.Stage == corev1.AppRootStage_APP_ROOT_STAGE_READY {
		sd.AccountSecurity, sd.Handoff = true, true
		if sc.ui.AddOpen {
			sd.AddDevice = true
		}
	}
	var targets []corev1.ScreenTarget
	for _, pair := range []struct {
		dirty  bool
		target corev1.ScreenTarget
	}{
		{sd.AppRoot, corev1.ScreenTarget_SCREEN_TARGET_APP_ROOT},
		{sd.Onboarding, corev1.ScreenTarget_SCREEN_TARGET_ONBOARDING},
		{sd.AddDevice, corev1.ScreenTarget_SCREEN_TARGET_ADD_DEVICE},
		{sd.AccountSecurity, corev1.ScreenTarget_SCREEN_TARGET_ACCOUNT_SECURITY},
		{sd.Handoff, corev1.ScreenTarget_SCREEN_TARGET_HANDOFF},
		{sd.ExitConfirm, corev1.ScreenTarget_SCREEN_TARGET_EXIT_CONFIRM},
	} {
		if pair.dirty {
			targets = append(targets, pair.target)
		}
	}
	if len(targets) == 0 {
		return effects
	}
	for _, eff := range effects {
		if r := eff.GetRender(); r != nil {
			for _, t := range targets {
				if !slices.Contains(r.Screens, t) {
					r.Screens = append(r.Screens, t)
				}
			}
			return effects
		}
	}
	return append(effects, &corev1.Effect{Effect: &corev1.Effect_Render{Render: &corev1.RenderEffect{Screens: targets}}})
}
