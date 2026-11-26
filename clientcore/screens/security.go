package screens

import (
	"strings"

	"github.com/Lewin671/remote-agent/clientcore/domain/machine"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/security"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

// ExitRisk is Core's internal two-phase exit risk classification (never serialized on the wire).
type ExitRisk uint8

const (
	ExitRiskUnspecified ExitRisk = iota
	ExitRiskOrdinaryMember
	ExitRiskOnlyManager
	ExitRiskUnresolvedSecurityOp
	ExitRiskUnfinishedJoin
	ExitRiskCorruptForceReset
)

// SecurityBannerState stores a banner produced by security UI actions.
type SecurityBannerState struct {
	Visible bool
	Tone    corev1.SecurityBannerTone
	Text    *corev1.Text
	Action  corev1.SecurityBannerAction
}

// SecurityExitState stores the active exit confirmation flow state.
type SecurityExitState struct {
	Intent         corev1.ExitIntentKind
	Step           corev1.ExitConfirmStep
	Risk           ExitRisk
	Server         string
	AdviceMessage  *corev1.Text
	ErrorText      *corev1.Text
	PushOpID       uint64
	CacheOpID      uint64
	PreserveServer string
}

// SecurityUIState stores all Core-owned pre-login, pairing, recovery, handoff, and exit UI state.
type SecurityUIState struct {
	Initialized bool

	Stage                   corev1.AppRootStage
	BootstrapErrText        *corev1.Text
	BootstrapCanRetry       bool
	BootstrapCanReset       bool
	RetainedServer          string
	RemovalVerified         bool
	RemovedVerifyBusy       bool
	RemovedVerifyResultText *corev1.Text

	Status *corev1.SecurityWorkflowStatus

	// Onboarding
	Mode                   corev1.OnboardingMode
	EntryDefaultServer     string
	CreateServerInput      string
	CreateRecoveryCode     string
	CreateCodeSaved        bool
	CreateHasPersist       bool
	CreateStoragePersisted bool
	CreateBusy             bool
	CreateBanner           SecurityBannerState
	CreateMissingCodeAck   bool

	JoinStep                corev1.JoinPairingStep
	JoinExistingRepair      bool
	JoinExistingServer      string
	JoinPendingRegistration bool
	JoinCodeDigits          string
	JoinServerInput         string
	JoinCommitHint          []byte
	JoinRawLink             string
	JoinPairingID           string
	JoinSASEmoji            []string
	JoinRegistering         bool
	JoinBusy                bool
	JoinBanner              SecurityBannerState

	RecPhase            corev1.SecurityRecoveryPhase
	RecServerInput      string
	RecNewCode          string
	RecCodeSaved        bool
	RecHasPersist       bool
	RecStoragePersisted bool
	RecReadyMs          int64
	RecBusy             bool
	RecBanner           SecurityBannerState

	// AddDevice (initiator pairing)
	AddOpen                  bool
	AddStep                  corev1.AddDeviceStep
	AddReplacesDeviceID      string
	AddReplacesMachineName   string
	AddWebOrigin             string
	AddPairingID             string
	AddRawCode               string
	AddQRURL                 string
	AddExpiresMs             int64
	AddSASEmoji              []string
	AddJoinerKind            v1.DeviceKind
	AddSASConfirmed          bool
	AddUserToggledCandidates bool
	AddReplaceOldLabel       *corev1.Text
	AddReplaceOldHint        *corev1.Text
	AddCandidates            []*corev1.SecurityPairingReplacementCandidate
	AddSelectedRemoveIDs     map[string]bool
	AddRemoveOldWarning      *corev1.Text
	AddBusy                  bool
	AddBanner                SecurityBannerState

	// AccountSecurity
	SecSubpage           corev1.AccountSecuritySubpage
	SecBusy              bool
	SecSuccessNotice     *corev1.Text
	SecErrorBanner       SecurityBannerState
	SecRotationCode      string
	SecRotationCodeSaved bool

	// Handoff
	HandoffCandidates       []*corev1.SecurityHandoffCandidate
	HandoffConfirmTargetID  string
	HandoffPreparedCode     string
	HandoffCodeSaved        bool
	HandoffHasPersist       bool
	HandoffStoragePersisted bool
	HandoffBusy             bool
	HandoffMessageBanner    *corev1.Text
	HandoffErrorBanner      SecurityBannerState

	// Exit confirmation
	Exit SecurityExitState
}

// SecurityViewContext provides lightweight read-only lookups from sync/world needed by security VM builders.
type SecurityViewContext struct {
	NowMs               int64
	SyncLoaded          bool
	SyncIsManager       bool
	SyncLeaseEndMs      int64
	SyncRecoveryPending bool
	SyncRecoveryReadyMs int64
	SyncRecoveryStale   bool
	SelfDeviceID        string
	Devices             []*corev1.SyncDeviceView
	ManagerDisplay      *corev1.Text
	MachineDisplayName  func(id string) string
}

// FormatSpacedPairingCode formats an 8-digit pairing code as "1234 5678" (or returns trimmed partial digits).
func FormatSpacedPairingCode(digits string) string {
	clean := NormalizePairingCodeDigits(digits)
	if len(clean) <= 4 {
		return clean
	}
	return clean[:4] + " " + clean[4:]
}

// NormalizePairingCodeDigits strips non-digits and clamps to 8 digits.
func NormalizePairingCodeDigits(raw string) string {
	var b strings.Builder
	b.Grow(8)
	for i := 0; i < len(raw) && b.Len() < 8; i++ {
		if raw[i] >= '0' && raw[i] <= '9' {
			b.WriteByte(raw[i])
		}
	}
	return b.String()
}

// DeriveRecoveryPhase maps live SecurityRecoveryInfo and/or persisted pendingOp into
// Core's single SecurityRecoveryPhase enum.
func DeriveRecoveryPhase(rec *corev1.SecurityRecoveryInfo, pendingOp *corev1.SecurityPendingOpSummary) corev1.SecurityRecoveryPhase {
	if rec != nil && rec.RecoveryPhase != corev1.SecurityRecoveryPhase_SECURITY_RECOVERY_PHASE_UNSPECIFIED {
		return rec.RecoveryPhase
	}
	if pendingOp != nil && pendingOp.Kind == security.PendingOpKindRecovery {
		switch {
		case pendingOp.Rejected:
			return corev1.SecurityRecoveryPhase_SECURITY_RECOVERY_PHASE_REJECTED
		case !pendingOp.Signed:
			return corev1.SecurityRecoveryPhase_SECURITY_RECOVERY_PHASE_UNSIGNED_DRAFT
		default:
			return corev1.SecurityRecoveryPhase_SECURITY_RECOVERY_PHASE_SUBMITTED_WAITING
		}
	}
	return corev1.SecurityRecoveryPhase_SECURITY_RECOVERY_PHASE_FORM
}

func buildSecurityBanner(b SecurityBannerState) *corev1.SecurityBannerView {
	if !b.Visible || text.IsEmpty(b.Text) {
		return nil
	}
	return &corev1.SecurityBannerView{Visible: true, Tone: b.Tone, Text: b.Text, Action: b.Action}
}

func makeBanner(tone corev1.SecurityBannerTone, msg *corev1.Text, act corev1.SecurityBannerAction) SecurityBannerState {
	return SecurityBannerState{Visible: true, Tone: tone, Text: msg, Action: act}
}

func coalesceMsg(t, fallback *corev1.Text) *corev1.Text {
	if !text.IsEmpty(t) {
		return t
	}
	return fallback
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// BuildJoinBannerState classifies a pairing join error into a structured SecurityBannerState.
// Never inspects English text; relies on structured SecurityErrorCode, WorkflowReason, and flags.
func BuildJoinBannerState(
	errText *corev1.Text,
	code corev1.SecurityErrorCode,
	reason security.WorkflowReason,
	retryable, uncertain bool,
	errAction string,
) SecurityBannerState {
	const (
		warnTone     = corev1.SecurityBannerTone_SECURITY_BANNER_TONE_WARN
		errTone      = corev1.SecurityBannerTone_SECURITY_BANNER_TONE_ERROR
		actionRefill = "refill"
		actionRetry  = "retry"
	)
	if code == corev1.SecurityErrorCode_SECURITY_ERROR_CODE_CLOCK_SKEWED || reason == security.WorkflowReasonClockBehind {
		return makeBanner(warnTone, text.Msg(text.KeySecurityThisDeviceSClockIsBehindTheManagingClient), corev1.SecurityBannerAction_SECURITY_BANNER_ACTION_RETRY)
	}
	if code == corev1.SecurityErrorCode_SECURITY_ERROR_CODE_CAPACITY || reason == security.WorkflowReasonSecurityChainFull {
		return makeBanner(errTone, text.Msg(text.KeySecurityTheAccountSStateChainIsTooLongTo), corev1.SecurityBannerAction_SECURITY_BANNER_ACTION_NONE)
	}
	msg := coalesceMsg(coalesceMsg(errText, SecurityWorkflowReasonText(reason, "")), text.Msg(text.KeyErrorsCommandFailed))
	if errAction == actionRefill || code == corev1.SecurityErrorCode_SECURITY_ERROR_CODE_NOT_FOUND || code == corev1.SecurityErrorCode_SECURITY_ERROR_CODE_INVALID {
		return makeBanner(errTone, msg, corev1.SecurityBannerAction_SECURITY_BANNER_ACTION_REFILL)
	}
	if uncertain || retryable || errAction == actionRetry || code == corev1.SecurityErrorCode_SECURITY_ERROR_CODE_UNAVAILABLE {
		return makeBanner(warnTone, msg, corev1.SecurityBannerAction_SECURITY_BANNER_ACTION_RETRY)
	}
	return makeBanner(errTone, msg, corev1.SecurityBannerAction_SECURITY_BANNER_ACTION_RESTART)
}

// BuildAppRoot builds Screen 50 (AppRootViewModel).
func BuildAppRoot(ui *SecurityUIState) *corev1.AppRootViewModel {
	if ui == nil {
		return &corev1.AppRootViewModel{Stage: corev1.AppRootStage_APP_ROOT_STAGE_LOADING}
	}
	stage := ui.Stage
	if stage == corev1.AppRootStage_APP_ROOT_STAGE_UNSPECIFIED {
		stage = corev1.AppRootStage_APP_ROOT_STAGE_LOADING
	}
	vm := &corev1.AppRootViewModel{Stage: stage}
	if ui.Status != nil {
		vm.Server, vm.DeviceId, vm.AccountId, vm.IsManager = ui.Status.Server, ui.Status.DeviceId, ui.Status.AccountId, ui.Status.IsAuthority
	}
	vm.Server = firstNonEmpty(vm.Server, ui.RetainedServer)

	switch stage {
	case corev1.AppRootStage_APP_ROOT_STAGE_BOOTSTRAP_ERROR:
		vm.BootstrapError = &corev1.BootstrapErrorView{
			Message:  coalesceMsg(ui.BootstrapErrText, text.Msg(text.KeyErrorsThisDeviceSSecurityStateCouldNotBeVerified)),
			CanRetry: ui.BootstrapCanRetry,
			CanReset: ui.BootstrapCanReset,
		}
	case corev1.AppRootStage_APP_ROOT_STAGE_RETAINED_DENIED:
		vm.RetainedDenied = &corev1.RetainedDeniedView{
			Server:                 firstNonEmpty(ui.RetainedServer, vm.Server),
			ShowLegacyRepairBanner: ui.Status != nil && ui.Status.LegacyRepairNotice,
		}
	case corev1.AppRootStage_APP_ROOT_STAGE_REMOVED:
		hasPending := ui.Status != nil && ui.Status.PendingOp != nil
		vm.Removed = &corev1.RemovedDeviceView{
			RemovalVerified:  ui.RemovalVerified,
			Server:           vm.Server,
			HasPendingOp:     hasPending,
			CanVerifyPending: hasPending && !ui.RemovedVerifyBusy,
			Busy:             ui.RemovedVerifyBusy,
			VerifyResultText: ui.RemovedVerifyResultText,
			CanRetryLogin:    !ui.RemovalVerified,
		}
	}
	return vm
}

// BuildOnboarding builds Screen 51 (OnboardingViewModel).
func BuildOnboarding(ui *SecurityUIState) *corev1.OnboardingViewModel {
	if ui == nil {
		return &corev1.OnboardingViewModel{Mode: corev1.OnboardingMode_ONBOARDING_MODE_ENTRY}
	}
	mode := ui.Mode
	if mode == corev1.OnboardingMode_ONBOARDING_MODE_UNSPECIFIED {
		mode = corev1.OnboardingMode_ONBOARDING_MODE_ENTRY
	}
	var pendingOp *corev1.SecurityPendingOpSummary
	var legacy bool
	var bootKind corev1.SecurityBootstrapKind
	if ui.Status != nil {
		pendingOp = ui.Status.PendingOp
		legacy = ui.Status.LegacyRepairNotice
		bootKind = ui.Status.Bootstrap
	}
	hasCreatePending := pendingOp != nil && pendingOp.Kind == security.PendingOpKindCreate
	hasRecoveryPending := pendingOp != nil && pendingOp.Kind == security.PendingOpKindRecovery

	canBack := false
	switch mode {
	case corev1.OnboardingMode_ONBOARDING_MODE_CREATE:
		canBack = !hasCreatePending && !ui.CreateBusy && ui.CreateRecoveryCode == ""
	case corev1.OnboardingMode_ONBOARDING_MODE_JOIN:
		canBack = !ui.JoinExistingRepair && !ui.JoinPendingRegistration && ui.JoinStep == corev1.JoinPairingStep_JOIN_PAIRING_STEP_FORM && !ui.JoinBusy
	case corev1.OnboardingMode_ONBOARDING_MODE_RECOVERY:
		canBack = !hasRecoveryPending && !ui.RecBusy
	}

	return &corev1.OnboardingViewModel{
		Mode:                   mode,
		CanBack:                canBack,
		ShowLegacyRepairBanner: legacy,
		Create:                 buildCreateAccountView(ui, pendingOp, bootKind),
		Join:                   buildJoinPairingView(ui),
		Recovery:               buildEmergencyRecoveryView(ui, pendingOp),
	}
}

func buildCreateAccountView(ui *SecurityUIState, pendingOp *corev1.SecurityPendingOpSummary, bootKind corev1.SecurityBootstrapKind) *corev1.CreateAccountView {
	hasPendingCreate := pendingOp != nil && pendingOp.Kind == security.PendingOpKindCreate
	step := corev1.CreateAccountStep_CREATE_ACCOUNT_STEP_FORM
	switch {
	case ui.CreateRecoveryCode != "":
		step = corev1.CreateAccountStep_CREATE_ACCOUNT_STEP_BACKUP_CODE
	case ui.CreateMissingCodeAck || (hasPendingCreate && pendingOp.Phase == security.PendingOpPhasePublished && !pendingOp.CodeSaved):
		step = corev1.CreateAccountStep_CREATE_ACCOUNT_STEP_MISSING_CODE_ACK
	}
	server := ui.CreateServerInput
	if server == "" && hasPendingCreate && pendingOp.Server != "" {
		server = pendingOp.Server
	}
	canSubmit := !ui.CreateBusy
	switch step {
	case corev1.CreateAccountStep_CREATE_ACCOUNT_STEP_FORM:
		canSubmit = canSubmit && strings.TrimSpace(server) != ""
	case corev1.CreateAccountStep_CREATE_ACCOUNT_STEP_BACKUP_CODE:
		canSubmit = canSubmit && ui.CreateCodeSaved
	}

	return &corev1.CreateAccountView{
		Step:                 step,
		ServerInput:          server,
		RecoveryCode:         ui.CreateRecoveryCode,
		CodeSavedChecked:     ui.CreateCodeSaved,
		HasPersistenceResult: ui.CreateHasPersist,
		StoragePersisted:     ui.CreateStoragePersisted,
		CanSubmit:            canSubmit,
		Busy:                 ui.CreateBusy,
		Banner:               buildSecurityBanner(ui.CreateBanner),
		ShowSetupReset:       hasPendingCreate || bootKind == corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_PENDING_CREATE,
		HasPendingCreate:     hasPendingCreate,
	}
}

func buildJoinPairingView(ui *SecurityUIState) *corev1.JoinPairingView {
	step := ui.JoinStep
	if step == corev1.JoinPairingStep_JOIN_PAIRING_STEP_UNSPECIFIED {
		step = corev1.JoinPairingStep_JOIN_PAIRING_STEP_FORM
	}
	digits := NormalizePairingCodeDigits(ui.JoinCodeDigits)
	canSubmit := step == corev1.JoinPairingStep_JOIN_PAIRING_STEP_FORM &&
		!ui.JoinBusy &&
		!ui.JoinPendingRegistration &&
		len(digits) == 8 &&
		strings.TrimSpace(ui.JoinServerInput) != ""

	return &corev1.JoinPairingView{
		Step:                step,
		IsExistingRepair:    ui.JoinExistingRepair,
		PendingRegistration: ui.JoinPendingRegistration,
		FormattedCode:       FormatSpacedPairingCode(digits),
		ServerInput:         ui.JoinServerInput,
		ServerEditable:      !ui.JoinExistingRepair && step == corev1.JoinPairingStep_JOIN_PAIRING_STEP_FORM && !ui.JoinPendingRegistration,
		SasEmoji:            append([]string(nil), ui.JoinSASEmoji...),
		CanSubmit:           canSubmit,
		CanConfirmSas:       step == corev1.JoinPairingStep_JOIN_PAIRING_STEP_COMPARE_SAS && !ui.JoinBusy && len(ui.JoinSASEmoji) > 0,
		CanCancel:           step != corev1.JoinPairingStep_JOIN_PAIRING_STEP_FORM && !ui.JoinRegistering && !ui.JoinPendingRegistration,
		Busy:                ui.JoinBusy,
		Banner:              buildSecurityBanner(ui.JoinBanner),
		ShowSetupReset:      ui.JoinPendingRegistration || ui.JoinExistingRepair,
	}
}

func buildEmergencyRecoveryView(ui *SecurityUIState, pendingOp *corev1.SecurityPendingOpSummary) *corev1.EmergencyRecoveryView {
	hasPendingRec := pendingOp != nil && pendingOp.Kind == security.PendingOpKindRecovery
	phase := ui.RecPhase
	if phase == corev1.SecurityRecoveryPhase_SECURITY_RECOVERY_PHASE_UNSPECIFIED {
		phase = DeriveRecoveryPhase(nil, pendingOp)
	}

	readyMs := ui.RecReadyMs
	if readyMs == 0 && hasPendingRec {
		readyMs = pendingOp.RecoveryReadyMs
	}
	server := ui.RecServerInput
	if server == "" && hasPendingRec && pendingOp.Server != "" {
		server = pendingOp.Server
	}

	var statusHint *corev1.Text
	switch phase {
	case corev1.SecurityRecoveryPhase_SECURITY_RECOVERY_PHASE_FORM:
		statusHint = text.Msg(text.KeySecurityAfterSubmissionWait48HoursTheCurrentManagingClient)
	case corev1.SecurityRecoveryPhase_SECURITY_RECOVERY_PHASE_UNSIGNED_DRAFT:
		statusHint = text.Msg(text.KeySecurityTheOriginalRecoveryDraftIsPreservedTheNewRecovery)
	case corev1.SecurityRecoveryPhase_SECURITY_RECOVERY_PHASE_SUBMITTED_WAITING:
		if readyMs > 0 {
			statusHint = text.Msg(text.KeySecurityRecoveryRequestPendingEligibleAt, text.ArgTimestamp("value", readyMs, corev1.TimestampStyle_TIMESTAMP_STYLE_DATE_TIME))
		} else {
			statusHint = text.Msg(text.KeySecurityRecoveryRequestPending)
		}
	case corev1.SecurityRecoveryPhase_SECURITY_RECOVERY_PHASE_READY_TO_COMPLETE:
		if readyMs > 0 {
			statusHint = text.Msg(text.KeySecurityRecoveryRequestPendingEligibleAt, text.ArgTimestamp("value", readyMs, corev1.TimestampStyle_TIMESTAMP_STYLE_DATE_TIME))
		}
	case corev1.SecurityRecoveryPhase_SECURITY_RECOVERY_PHASE_REJECTED:
		statusHint = text.Msg(text.KeySecurityRecoveryWasRejectedYouCanApplyAgain)
	}

	codeSavedPersisted := hasPendingRec && pendingOp.CodeSaved

	return &corev1.EmergencyRecoveryView{
		Phase:                phase,
		ServerInput:          server,
		NewRecoveryCode:      ui.RecNewCode,
		CodeSavedChecked:     ui.RecCodeSaved || codeSavedPersisted,
		HasPersistenceResult: ui.RecHasPersist,
		StoragePersisted:     ui.RecStoragePersisted,
		ReadyMs:              readyMs,
		StatusHintText:       statusHint,
		CanConfirmCodeSaved:  hasPendingRec && !codeSavedPersisted && ui.RecNewCode != "" && ui.RecCodeSaved && !ui.RecBusy,
		CanPrepare:           phase == corev1.SecurityRecoveryPhase_SECURITY_RECOVERY_PHASE_FORM && strings.TrimSpace(server) != "" && !ui.RecBusy,
		CanContinueSigning:   phase == corev1.SecurityRecoveryPhase_SECURITY_RECOVERY_PHASE_UNSIGNED_DRAFT && !ui.RecBusy,
		CanSubmitOrRefresh:   (phase == corev1.SecurityRecoveryPhase_SECURITY_RECOVERY_PHASE_SUBMITTED_WAITING || phase == corev1.SecurityRecoveryPhase_SECURITY_RECOVERY_PHASE_READY_TO_COMPLETE) && !ui.RecBusy,
		CanComplete:          phase == corev1.SecurityRecoveryPhase_SECURITY_RECOVERY_PHASE_READY_TO_COMPLETE && !ui.RecBusy,
		CanDiscard:           (phase == corev1.SecurityRecoveryPhase_SECURITY_RECOVERY_PHASE_UNSIGNED_DRAFT || phase == corev1.SecurityRecoveryPhase_SECURITY_RECOVERY_PHASE_REJECTED) && !ui.RecBusy,
		Busy:                 ui.RecBusy,
		Banner:               buildSecurityBanner(ui.RecBanner),
		ShowSetupReset:       hasPendingRec,
	}
}

// BuildAddDevice builds Screen 52 (AddDeviceViewModel).
func BuildAddDevice(ui *SecurityUIState, vc SecurityViewContext) *corev1.AddDeviceViewModel {
	if ui == nil {
		return &corev1.AddDeviceViewModel{Step: corev1.AddDeviceStep_ADD_DEVICE_STEP_IDLE}
	}
	isManager := vc.SyncIsManager
	if ui.Status != nil {
		isManager = ui.Status.IsAuthority
	}
	vm := &corev1.AddDeviceViewModel{
		ReplacesDeviceId:    ui.AddReplacesDeviceID,
		RawCode:             ui.AddRawCode,
		FormattedCode:       FormatSpacedPairingCode(ui.AddRawCode),
		QrUrl:               ui.AddQRURL,
		ExpiresMs:           ui.AddExpiresMs,
		SasEmoji:            append([]string(nil), ui.AddSASEmoji...),
		JoinerKind:          ui.AddJoinerKind,
		SasConfirmedChecked: ui.AddSASConfirmed,
		ReplaceOldLabel:     ui.AddReplaceOldLabel,
		ReplaceOldHint:      ui.AddReplaceOldHint,
		RemoveOldWarning:    ui.AddRemoveOldWarning,
		Busy:                ui.AddBusy,
		Banner:              buildSecurityBanner(ui.AddBanner),
	}
	if ui.AddReplacesDeviceID != "" {
		name := ui.AddReplacesMachineName
		if name == "" && vc.MachineDisplayName != nil {
			name = vc.MachineDisplayName(ui.AddReplacesDeviceID)
		}
		if name != "" {
			vm.ReplacesMachineName = text.Plain(name)
		}
	}
	if !isManager {
		vm.Step = corev1.AddDeviceStep_ADD_DEVICE_STEP_NOT_MANAGER
		vm.NonManagerHint = text.Msg(text.KeySecurityAddOnAuthority, text.ArgText("name", coalesceMsg(vc.ManagerDisplay, text.Msg(text.KeySecurityClient))))
		return vm
	}

	step := ui.AddStep
	if step == corev1.AddDeviceStep_ADD_DEVICE_STEP_UNSPECIFIED {
		step = corev1.AddDeviceStep_ADD_DEVICE_STEP_IDLE
	}
	vm.Step = step
	if ui.AddExpiresMs > 0 {
		vm.ValidUntilText = text.Msg(text.KeySecurityPairValidUntil, text.ArgTimestamp("time", ui.AddExpiresMs, corev1.TimestampStyle_TIMESTAMP_STYLE_TIME))
	}
	if len(ui.AddCandidates) > 0 {
		vm.ReplaceOldLabel = coalesceMsg(vm.ReplaceOldLabel, text.Msg(text.KeySecurityAlsoRemoveOldDevices))
		vm.ReplaceOldHint = coalesceMsg(vm.ReplaceOldHint, text.Msg(text.KeySecurityAlsoRemoveOldDevicesHint))
		vm.ReplacementCandidates = make([]*corev1.PairingCandidateItemView, 0, len(ui.AddCandidates))
		for _, c := range ui.AddCandidates {
			if c == nil || c.DeviceId == "" {
				continue
			}
			title := firstNonEmpty(c.DisplayName, c.Description, c.ShortId)
			sub := coalesceMsg(c.LastActiveText, FormatCandidateLastActive(c.LastSeenMs, vc.NowMs))
			if c.ShortId != "" && c.ShortId != title {
				sub = text.JoinLiteral(" · ", text.Plain(c.ShortId), sub)
			}
			vm.ReplacementCandidates = append(vm.ReplacementCandidates, &corev1.PairingCandidateItemView{
				DeviceId: c.DeviceId, Title: title, Subtitle: sub, Selected: ui.AddSelectedRemoveIDs[c.DeviceId],
			})
		}
	}

	vm.CanGenerate = !ui.AddBusy && (step == corev1.AddDeviceStep_ADD_DEVICE_STEP_IDLE || step == corev1.AddDeviceStep_ADD_DEVICE_STEP_DONE || step == corev1.AddDeviceStep_ADD_DEVICE_STEP_FAILED)
	vm.CanApprove = !ui.AddBusy && step == corev1.AddDeviceStep_ADD_DEVICE_STEP_COMPARE_SAS && ui.AddSASConfirmed && len(ui.AddSASEmoji) > 0
	vm.CanCancel = !ui.AddBusy && (step == corev1.AddDeviceStep_ADD_DEVICE_STEP_WAITING_JOINER || step == corev1.AddDeviceStep_ADD_DEVICE_STEP_COMPARE_SAS)
	vm.CanRetry = !ui.AddBusy && (step == corev1.AddDeviceStep_ADD_DEVICE_STEP_FAILED || step == corev1.AddDeviceStep_ADD_DEVICE_STEP_DELIVERING)
	return vm
}

// PendingOpKindFromString maps the internal persisted op kind/action to PendingSecurityOpKind enum.
func PendingOpKindFromString(p *corev1.SecurityPendingOpSummary) corev1.PendingSecurityOpKind {
	if p == nil {
		return corev1.PendingSecurityOpKind_PENDING_SECURITY_OP_KIND_NONE
	}
	switch p.Kind {
	case security.PendingOpKindLease:
		return corev1.PendingSecurityOpKind_PENDING_SECURITY_OP_KIND_LEASE
	case security.PendingOpKindOffer:
		return corev1.PendingSecurityOpKind_PENDING_SECURITY_OP_KIND_HANDOFF_OFFER
	case security.PendingOpKindDeleteRecords:
		return corev1.PendingSecurityOpKind_PENDING_SECURITY_OP_KIND_DELETE_RECORDS
	case security.PendingOpKindState:
		if p.HasDelivery {
			return corev1.PendingSecurityOpKind_PENDING_SECURITY_OP_KIND_PAIRING_DELIVERY
		}
		if p.Action == security.PendingOpActionRecovery {
			return corev1.PendingSecurityOpKind_PENDING_SECURITY_OP_KIND_RECOVERY_ROTATION
		}
		return corev1.PendingSecurityOpKind_PENDING_SECURITY_OP_KIND_STATE_CHANGE
	default:
		return corev1.PendingSecurityOpKind_PENDING_SECURITY_OP_KIND_STATE_CHANGE
	}
}

// EvaluatePendingOpDiscard decides whether a pending security operation may be discarded
// and whether discarding requires explicit user confirmation.
// Matches clientcore/security discardPendingTask invariants so the UI never offers a button
// that the workflow would reject with ErrStale, and enforces unanimous confirmation on offers.
func EvaluatePendingOpDiscard(p *corev1.SecurityPendingOpSummary) (canDiscard bool, requiresConfirm bool) {
	if p == nil || p.Phase != security.PendingOpPhasePrepared {
		return false, false
	}
	switch p.Kind {
	case security.PendingOpKindOffer:
		// Unanimous policy across Web/Android/iOS: abandoning an offer always requires confirmation.
		return true, true
	case security.PendingOpKindState:
		if p.Signed || p.HasDelivery {
			return false, false
		}
		if p.Action == security.PendingOpActionRecovery && p.CodeSaved {
			return false, false
		}
		return true, false
	default:
		return false, false
	}
}

// BuildAccountSecurity builds Screen 53 (AccountSecurityViewModel).
func BuildAccountSecurity(ui *SecurityUIState, vc SecurityViewContext) *corev1.AccountSecurityViewModel {
	if ui == nil {
		ui = &SecurityUIState{}
	}
	sub := ui.SecSubpage
	if sub == corev1.AccountSecuritySubpage_ACCOUNT_SECURITY_SUBPAGE_UNSPECIFIED {
		sub = corev1.AccountSecuritySubpage_ACCOUNT_SECURITY_SUBPAGE_OVERVIEW
	}
	st := ui.Status
	isManager := vc.SyncIsManager
	leaseEndMs := vc.SyncLeaseEndMs
	var pendingOp *corev1.SecurityPendingOpSummary
	if st != nil {
		isManager = st.IsAuthority
		leaseEndMs = st.LeaseEndMs
		pendingOp = st.PendingOp
	}
	mgrDisplay := vc.ManagerDisplay
	if isManager {
		mgrDisplay = text.Msg(text.KeySecurityThisDevice)
	} else if text.IsEmpty(mgrDisplay) {
		mgrDisplay = text.Msg(text.KeySecurityClient)
	}

	var leaseExpiryText *corev1.Text
	switch {
	case st == nil && !vc.SyncLoaded:
		leaseExpiryText = text.Msg(text.KeySecurityReading)
	case leaseEndMs <= 0:
		leaseExpiryText = text.Msg(text.KeySecurityRemoteControlExpiryNotReceivedYet)
	default:
		leaseExpiryText = text.Time(leaseEndMs, corev1.TimestampStyle_TIMESTAMP_STYLE_DATE_TIME)
	}

	leaseAdvice := leaseRenewalAdvice(st != nil || vc.SyncLoaded, isManager, mgrDisplay)
	canOpenHandoff := isManager || canPrepareHandoff(isManager, vc.SelfDeviceID, vc.Devices)

	vm := &corev1.AccountSecurityViewModel{
		Subpage:            sub,
		IsManager:          isManager,
		ManagerDisplay:     mgrDisplay,
		LeaseEndMs:         leaseEndMs,
		LeaseExpiryText:    leaseExpiryText,
		LeaseRenewalAdvice: leaseAdvice,
		CanRenewNow:        isManager && !ui.SecBusy,
		CanOpenRecovery:    isManager,
		CanOpenHandoff:     canOpenHandoff,
		SuccessNotice:      ui.SecSuccessNotice,
		ErrorBanner:        buildSecurityBanner(ui.SecErrorBanner),
		PreparedCode:       ui.SecRotationCode,
		CodeSavedChecked:   ui.SecRotationCodeSaved,
		CanPrepareRotation: isManager && ui.SecRotationCode == "" && !ui.SecBusy && pendingOp == nil,
		CanPublishRotation: isManager && ui.SecRotationCode != "" && ui.SecRotationCodeSaved && !ui.SecBusy,
		Busy:               ui.SecBusy,
	}

	recPending := vc.SyncRecoveryPending
	recReadyMs := vc.SyncRecoveryReadyMs
	recStale := vc.SyncRecoveryStale
	if st != nil {
		recPending = st.RecoveryPending
		recReadyMs = st.RecoveryReadyMs
		recStale = st.RecoveryStatusStale
		vm.TakingOver = st.TakingOver
		vm.NeedsRotationBanner = st.NeedsRecoveryCodeRotation
		vm.PendingRemoveBanner = st.PendingRemoveNotice
		vm.AuthorityErrorBanner = st.ErrorText
	}
	vm.RecoveryPending = recPending
	if recPending {
		vm.RecoveryTakeoverBanner = buildRecoveryBannerText(isManager, recReadyMs)
		vm.CanVetoRecovery = isManager
	}
	vm.RecoveryStatusStale = recStale

	if pendingOp != nil {
		vm.HasPendingOp = true
		vm.PendingKind = PendingOpKindFromString(pendingOp)
		vm.CanContinuePending = !ui.SecBusy
		canDiscard, reqConfirm := EvaluatePendingOpDiscard(pendingOp)
		vm.CanDiscardPending = canDiscard && !ui.SecBusy
		vm.DiscardRequiresConfirm = reqConfirm
	}

	return vm
}

// BuildHandoff builds Screen 54 (HandoffViewModel).
func BuildHandoff(ui *SecurityUIState, vc SecurityViewContext) *corev1.HandoffViewModel {
	if ui == nil {
		ui = &SecurityUIState{}
	}
	st := ui.Status
	isManager := vc.SyncIsManager
	hasPubProfile := true
	hasPublishedOffer := false
	hasPendingOp := false
	if st != nil {
		isManager = st.IsAuthority
		hasPubProfile = st.HasAccountPublicProfile
		hasPublishedOffer = st.HasHandoffOffer
		hasPendingOp = st.PendingOp != nil
	}

	role := corev1.HandoffRole_HANDOFF_ROLE_UNAVAILABLE
	if isManager {
		role = corev1.HandoffRole_HANDOFF_ROLE_OUTGOING_MANAGER
	} else if canPrepareHandoff(isManager, vc.SelfDeviceID, vc.Devices) {
		role = corev1.HandoffRole_HANDOFF_ROLE_INCOMING_CANDIDATE
	}

	vm := &corev1.HandoffViewModel{
		Role:                      role,
		ConfirmingTargetId:        ui.HandoffConfirmTargetID,
		CanRefresh:                role == corev1.HandoffRole_HANDOFF_ROLE_OUTGOING_MANAGER && !ui.HandoffBusy,
		CanTransfer:               role == corev1.HandoffRole_HANDOFF_ROLE_OUTGOING_MANAGER && !ui.HandoffBusy && !hasPendingOp,
		PreparedCode:              ui.HandoffPreparedCode,
		CodeSavedChecked:          ui.HandoffCodeSaved,
		HasPersistenceResult:      ui.HandoffHasPersist,
		StoragePersisted:          ui.HandoffStoragePersisted,
		HasPublishedOffer:         hasPublishedOffer,
		CanPrepareCode:            role == corev1.HandoffRole_HANDOFF_ROLE_INCOMING_CANDIDATE && hasPubProfile && ui.HandoffPreparedCode == "" && !hasPublishedOffer && !hasPendingOp && !ui.HandoffBusy,
		CanPublishOffer:           role == corev1.HandoffRole_HANDOFF_ROLE_INCOMING_CANDIDATE && ui.HandoffPreparedCode != "" && ui.HandoffCodeSaved && !ui.HandoffBusy,
		NeedsPublicProfileRestore: role == corev1.HandoffRole_HANDOFF_ROLE_INCOMING_CANDIDATE && !hasPubProfile,
		Busy:                      ui.HandoffBusy,
		MessageBanner:             ui.HandoffMessageBanner,
		ErrorBanner:               buildSecurityBanner(ui.HandoffErrorBanner),
	}

	// Reuse shared readable client naming from settings/machine for candidate display names.
	byDeviceName := make(map[string]*corev1.Text)
	for _, hc := range buildHandoffCandidatesFromDevices(vc.Devices, vc.SelfDeviceID, vc.SyncLoaded) {
		byDeviceName[hc.DeviceId] = hc.DisplayNameText
	}
	if len(ui.HandoffCandidates) > 0 {
		sorted := machine.SortClients(vc.Devices, vc.SelfDeviceID)
		vm.Candidates = make([]*corev1.HandoffCandidateItemView, 0, len(ui.HandoffCandidates))
		for _, c := range ui.HandoffCandidates {
			if c == nil || c.DeviceId == "" {
				continue
			}
			name := byDeviceName[c.DeviceId]
			if text.IsEmpty(name) && c.DisplayName != "" {
				name = text.Plain(c.DisplayName)
			}
			if text.IsEmpty(name) {
				for _, d := range sorted {
					if d != nil && d.DeviceId == c.DeviceId {
						name = machine.ReadableClientNameText(d, sorted)
						break
					}
				}
			}
			if text.IsEmpty(name) {
				name = text.Msg(text.KeySecurityClient)
			}
			vm.Candidates = append(vm.Candidates, &corev1.HandoffCandidateItemView{
				DeviceId:    c.DeviceId,
				DisplayName: name,
			})
		}
	}

	if ui.HandoffConfirmTargetID != "" {
		targetName := text.Msg(text.KeySecurityClient)
		for _, c := range vm.Candidates {
			if c.DeviceId == ui.HandoffConfirmTargetID && !text.IsEmpty(c.DisplayName) {
				targetName = c.DisplayName
				break
			}
		}
		vm.ConfirmDialogBody = text.Msg(text.KeySecurityConfirmThatTheSuccessorDeviceSavedTheNewRecovery, text.ArgText("value", targetName))
	}
	return vm
}

// BuildExitConfirm builds Screen 55 (ExitConfirmView).
func BuildExitConfirm(ui *SecurityUIState) *corev1.ExitConfirmView {
	if ui == nil || ui.Exit.Step == corev1.ExitConfirmStep_EXIT_CONFIRM_STEP_CLOSED {
		return &corev1.ExitConfirmView{
			Intent: corev1.ExitIntentKind_EXIT_INTENT_KIND_NONE,
			Step:   corev1.ExitConfirmStep_EXIT_CONFIRM_STEP_CLOSED,
		}
	}
	ex := ui.Exit
	vm := &corev1.ExitConfirmView{
		Intent:    ex.Intent,
		Step:      ex.Step,
		Busy:      ex.Step == corev1.ExitConfirmStep_EXIT_CONFIRM_STEP_CHECKING || ex.Step == corev1.ExitConfirmStep_EXIT_CONFIRM_STEP_EXECUTING,
		ErrorText: ex.ErrorText,
	}

	if ex.Step == corev1.ExitConfirmStep_EXIT_CONFIRM_STEP_CHECKING {
		vm.WarningBody = text.Msg(text.KeySecurityVerifyingManagementIdentityAndUnfinishedSecurityOperations)
		return vm
	}
	if !text.IsEmpty(ex.ErrorText) {
		vm.WarningBody = ex.ErrorText
		return vm
	}

	rejoinNote := ex.Intent == corev1.ExitIntentKind_EXIT_INTENT_KIND_REJOIN_PAIRING
	switch ex.Risk {
	case ExitRiskUnfinishedJoin:
		vm.WarningBody = text.Msg(text.KeySecurityThisDeviceHasAnUnfinishedJoinRequestDeletingIt)
		vm.ShowRejoinDeletesCacheNote = rejoinNote
	case ExitRiskUnresolvedSecurityOp:
		vm.WarningBody = coalesceMsg(ex.AdviceMessage, text.Msg(text.KeySecurityTheSecurityOperationSOutcomeIsUnverifiedContinueThe))
		vm.ShowRejoinDeletesCacheNote = rejoinNote
	case ExitRiskOnlyManager:
		vm.WarningBody = coalesceMsg(ex.AdviceMessage, text.Msg(text.KeySecurityThisDeviceIsTheOnlyManagingClientTransferManagement))
		vm.ShowRejoinDeletesCacheNote = rejoinNote
	case ExitRiskCorruptForceReset:
		vm.WarningBody = text.Msg(text.KeySecurityTheLocalAccountUnfinishedRegistrationAndSecurityOperationsWill)
	default:
		vm.WarningBody = coalesceMsg(ex.AdviceMessage, text.Msg(text.KeySecurityExitingDeletesLocalKeysYouWillNeedToPair))
	}
	return vm
}
