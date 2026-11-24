package screens

import (
	"slices"

	attdomain "github.com/Lewin671/remote-agent/clientcore/domain/attachments"
	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

func findMachine(st *State, id string) *lists.Machine {
	if st == nil || id == "" {
		return nil
	}
	for _, m := range st.World.Machines {
		if m != nil && m.ID == id {
			return m
		}
	}
	return nil
}

func modeNameText(m v1.ApprovalMode) *corev1.Text {
	switch m {
	case v1.ApprovalMode_APPROVAL_MODE_ASK:
		return text.Msg(text.KeyModesAsk)
	case v1.ApprovalMode_APPROVAL_MODE_PLAN:
		return text.Msg(text.KeyModesPlan)
	case v1.ApprovalMode_APPROVAL_MODE_AUTO:
		return text.Msg(text.KeyModesAuto)
	case v1.ApprovalMode_APPROVAL_MODE_FULL_ACCESS:
		return text.Msg(text.KeyModesFullAccess)
	default:
		return nil
	}
}

func defaultModeView(dm lists.DefaultMode, agent v1.Agent) *corev1.DefaultModeInfo {
	agentName := lists.AgentInfo(agent).Name
	if !dm.Known {
		return &corev1.DefaultModeInfo{
			Mode:   v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED,
			Known:  false,
			Varies: dm.Varies,
			Label:  text.Msg(text.KeyModesDefaultAgent, text.Str("agent", agentName)),
		}
	}
	var label *corev1.Text
	if dm.Mode != v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED {
		mn := modeNameText(dm.Mode)
		if dm.Varies {
			label = text.Msg(text.KeyModesDefaultVaries, text.ArgText("mode", mn))
		} else {
			label = text.Msg(text.KeyModesDefaultNamed, text.ArgText("mode", mn))
		}
	} else {
		own := text.Msg(text.KeyModesAgentOwn, text.Str("agent", agentName))
		if dm.Varies {
			label = text.Msg(text.KeyModesDefaultVaries, text.ArgText("mode", own))
		} else {
			label = text.Msg(text.KeyModesDefaultAgent, text.Str("agent", agentName))
		}
	}
	return &corev1.DefaultModeInfo{
		Mode:   dm.Mode,
		Known:  true,
		Varies: dm.Varies,
		Label:  label,
	}
}

func clampRunes(s string, max int) string {
	n := 0
	for i := range s {
		if n == max {
			return s[:i]
		}
		n++
	}
	return s
}

// BuildNewSession constructs the layout-agnostic NewSessionViewModel.
// projectKey empty selects the active selectable project; non-empty fixes the project context.
func BuildNewSession(st *State, projectKey string) *corev1.NewSessionViewModel {
	if st == nil {
		st = NewState(nil)
	}
	projRes := lists.BuildProjects(&st.World, st.ProjectSearch, st.RememberedScope, st.RememberedScopeToken, nil, st.Scope)

	projectSelectable := projectKey == ""
	key := projectKey
	if key == "" {
		key = standaloneKey(st, projRes)
	}

	var draft *NewSessionDraft
	if key != "" {
		draft = st.NewSessions[key]
	}

	var pickMachine string
	var pickAgent v1.Agent
	if draft != nil && (draft.ConfigChosen || draft.MachineChosen || draft.ConfigInitialized) {
		pickMachine = draft.Config.MachineID
	}
	if draft != nil && draft.AgentOverride != v1.Agent_AGENT_UNSPECIFIED {
		pickAgent = draft.AgentOverride
	} else if draft != nil && draft.ConfigChosen {
		pickAgent = draft.Config.Agent
	}

	res := lists.ResolveProjectStart(&st.World, key, pickMachine, pickAgent, nil, false)
	cfg := res.Config
	if !res.Found {
		cfg.MachineID = pickMachine
		cfg.Cwd = key
		cfg.Agent = lists.EffectiveAgent(pickAgent)
	} else {
		cfg.Agent = lists.EffectiveAgent(cfg.Agent)
	}
	if draft != nil && draft.ConfigInitialized {
		cfg.Model = draft.Config.Model
		cfg.Effort = draft.Config.Effort
		cfg.StandardContext = draft.Config.StandardContext
		cfg.Mode = draft.Config.Mode
	}

	machine := findMachine(st, cfg.MachineID)
	machineName := lists.MachineName(machine, cfg.MachineID)
	if machineName == "" {
		machineName = lists.ProjectName(key)
	}
	var mMeta *v1.MachineMeta
	if machine != nil {
		mMeta = machine.Meta
	}
	installedAgents := lists.MachineAgents(mMeta)
	agentCap := lists.AgentInfo(cfg.Agent)

	modelsEntry := st.MachineModels[MachineAgentKey{MachineID: cfg.MachineID, Agent: lists.EffectiveAgent(cfg.Agent)}]
	settingsEntry := st.MachineSettings[cfg.MachineID]
	var modelsResp *v1.ListModelsResponse
	var hasModels, modelsLoading bool
	var modelsErr string
	var modelsErrText *corev1.Text
	if modelsEntry != nil {
		modelsResp = modelsEntry.Response
		hasModels = modelsEntry.Loaded && modelsResp != nil
		modelsLoading = modelsEntry.Loading
		modelsErr = modelsEntry.Error
		modelsErrText = modelsEntry.ErrorText
	}
	var daemonSettings *v1.DaemonSettings
	if settingsEntry != nil {
		daemonSettings = settingsEntry.Settings
	}

	var modelList []*v1.ModelInfo
	if hasModels {
		modelList = modelsResp.Models
	}
	pickedModel := lists.ProjectModel(cfg, modelsResp, daemonSettings)
	efforts, selectedEffort := lists.EffortChoices(cfg, modelsResp, daemonSettings)

	defaultModelSlug := lists.DefaultsFor(daemonSettings, cfg.Agent).Model
	if defaultModelSlug == "" && modelsResp != nil {
		defaultModelSlug = modelsResp.DefaultModel
	}
	defaultModelName := lists.ModelLabel(modelList, hasModels, defaultModelSlug, cfg.Agent)
	var defaultModelLabel *corev1.Text
	if defaultModelName != "" {
		defaultModelLabel = text.Msg(text.KeyModelsDefaultNamed, text.Str("model", defaultModelName))
	} else {
		defaultModelLabel = text.Msg(text.KeyModelsDefault)
	}

	var modelLabel *corev1.Text
	if cfg.Model == "" {
		modelLabel = text.Msg(text.KeyModelsDefault)
	} else {
		modelLabel = text.Plain(lists.ModelLabel(modelList, hasModels, cfg.Model, cfg.Agent))
	}

	dmView := defaultModeView(res.DefaultMode, cfg.Agent)
	autoIssue := lists.AutoIssue(cfg.Agent, cfg.Model, res.DefaultMode.Mode, modelList, hasModels)

	projNameStr := lists.ProjectName(key)
	var projNameText *corev1.Text
	if projNameStr != "" {
		projNameText = text.Plain(projNameStr)
	} else {
		projNameText = text.Msg(text.KeyProjectsUnknown)
	}

	agentOpts := make([]*corev1.AgentOptionView, 0, len(installedAgents))
	for _, a := range installedAgents {
		agentOpts = append(agentOpts, &corev1.AgentOptionView{
			Agent:       a,
			DisplayName: lists.AgentInfo(a).Name,
			Installed:   true,
			Selected:    a == cfg.Agent,
		})
	}

	machineSelectable := len(st.World.Machines) > 1
	showCLI := len(installedAgents) > 1
	var locationText *corev1.Text
	switch {
	case machineSelectable && showCLI:
		locationText = text.Msg(text.KeyNewSessionLocationMachineCli, text.Str("machine", machineName), text.Str("project", projNameStr), text.Str("cli", agentCap.Name))
	case machineSelectable:
		locationText = text.Msg(text.KeyNewSessionLocationMachine, text.Str("machine", machineName), text.Str("project", projNameStr))
	case showCLI:
		locationText = text.Msg(text.KeyNewSessionLocationCli, text.Str("project", projNameStr), text.Str("cli", agentCap.Name))
	default:
		locationText = text.Msg(text.KeyNewSessionLocation, text.Str("project", projNameStr))
	}

	var prompt string
	var attachments int32
	var busy, frozen bool
	if draft != nil {
		prompt = draft.Prompt
		attachments = draft.AttachmentCount
		busy = draft.Busy
		frozen = draft.Frozen
	}
	drafting := prompt != "" || attachments > 0
	listed := st.World.MachinesListed
	machineOnline := lists.MachineReachable(machine)
	offline := listed && machine != nil && !machineOnline
	hidden := lists.IsHidden(st.World.Prefs, key)
	// diskCritical blocks starting while the computer reports critical disk
	// pressure. A previous start that failed with disk_full only raises a notice
	// the user can retry from once space is freed, so it does not block.
	diskCritical := machineOnline && machine != nil && machine.Meta != nil && machine.Meta.DiskStatus != nil && machine.Meta.DiskStatus.Pressure == v1.ResourcePressure_RESOURCE_PRESSURE_CRITICAL
	diskFull := (draft != nil && draft.DiskFull) || diskCritical

	var notices []*corev1.NewSessionNoticeView
	if draft != nil && draft.AttachmentsMoved && machineName != "" {
		notices = append(notices, &corev1.NewSessionNoticeView{
			Kind:     corev1.NoticeKind_NOTICE_KIND_ATTACHMENTS_MOVED,
			Severity: corev1.NoticeSeverity_NOTICE_SEVERITY_INFO,
			Text:     text.Msg(text.KeyNewSessionAttachmentsMoved, text.Str("name", machineName)),
		})
	}
	if !res.Settled && !offline && !frozen {
		notices = append(notices, &corev1.NewSessionNoticeView{
			Kind:     corev1.NoticeKind_NOTICE_KIND_READING_PREVIOUS,
			Severity: corev1.NoticeSeverity_NOTICE_SEVERITY_INFO,
			Text:     text.Msg(text.KeyNewSessionReadingPrevious),
		})
	}
	if offline {
		if drafting && len(st.World.Machines) > 1 {
			choices := make([]*corev1.OfflineMachineChoiceView, 0, len(res.OfflineChoices))
			for _, c := range res.OfflineChoices {
				var note *corev1.Text
				if c.FallbackAgent != cfg.Agent {
					note = text.Msg(text.KeyNewSessionCliFallback, text.Str("name", c.Name), text.Str("missing", agentCap.Name), text.Str("agent", lists.AgentInfo(c.FallbackAgent).Name))
				}
				choices = append(choices, &corev1.OfflineMachineChoiceView{
					MachineId: c.ID,
					Title:     text.Msg(text.KeyNewSessionUseMachine, text.Str("name", c.Name)),
					Note:      note,
					Config: &corev1.NewSessionConfig{
						MachineId:       c.Config.MachineID,
						Agent:           c.Config.Agent,
						Model:           c.Config.Model,
						Effort:          c.Config.Effort,
						StandardContext: c.Config.StandardContext,
					},
				})
			}
			var noneText *corev1.Text
			if len(choices) == 0 {
				noneText = text.Msg(text.KeyNewSessionOthersOffline, text.Str("name", projNameStr))
			}
			notices = append(notices, &corev1.NewSessionNoticeView{
				Kind:            corev1.NoticeKind_NOTICE_KIND_OFFLINE_DRAFT_CHOICES,
				Severity:        corev1.NoticeSeverity_NOTICE_SEVERITY_WARN,
				Text:            text.Msg(text.KeyNewSessionOfflineDraft, text.Str("name", machineName)),
				HasDraftChoices: true,
				DraftTitle:      text.Msg(text.KeyNewSessionOfflineDraft, text.Str("name", machineName)),
				DraftSubtitle:   text.Msg(text.KeyNewSessionDraftKept),
				DraftChoices:    choices,
				DraftNoneText:   noneText,
			})
		} else {
			sev := corev1.NoticeSeverity_NOTICE_SEVERITY_INFO
			msg := text.Msg(text.KeyNewSessionMachineOffline, text.Str("name", machineName))
			if drafting {
				sev = corev1.NoticeSeverity_NOTICE_SEVERITY_WARN
				msg = text.Msg(text.KeyNewSessionOfflineWithDraft, text.Str("name", machineName))
			}
			notices = append(notices, &corev1.NewSessionNoticeView{
				Kind:     corev1.NoticeKind_NOTICE_KIND_MACHINE_OFFLINE,
				Severity: sev,
				Text:     msg,
				Action:   corev1.NoticeActionKind_NOTICE_ACTION_KIND_CHANGE_MACHINE,
			})
		}
	}
	if res.ProtocolMismatch < 0 && machineName != "" {
		notices = append(notices, &corev1.NewSessionNoticeView{
			Kind:     corev1.NoticeKind_NOTICE_KIND_PROTOCOL_MISMATCH,
			Severity: corev1.NoticeSeverity_NOTICE_SEVERITY_WARN,
			Text:     text.Msg(text.KeyErrorsRemoteAgentOnIsOutOfDateUpdateIt, text.Str("value", machineName)),
		})
	} else if res.ProtocolMismatch > 0 {
		notices = append(notices, &corev1.NewSessionNoticeView{
			Kind:     corev1.NoticeKind_NOTICE_KIND_PROTOCOL_MISMATCH,
			Severity: corev1.NoticeSeverity_NOTICE_SEVERITY_WARN,
			Text:     text.Msg(text.KeyErrorsClientOutOfDate),
		})
	}
	disabledFallback := res.DisabledAgent
	if disabledFallback == v1.Agent_AGENT_UNSPECIFIED && draft != nil && draft.DisabledFromAgent != v1.Agent_AGENT_UNSPECIFIED {
		disabledFallback = draft.DisabledFromAgent
	}
	if disabledFallback != v1.Agent_AGENT_UNSPECIFIED && !offline {
		notices = append(notices, &corev1.NewSessionNoticeView{
			Kind:            corev1.NoticeKind_NOTICE_KIND_AGENT_DISABLED_FALLBACK,
			Severity:        corev1.NoticeSeverity_NOTICE_SEVERITY_INFO,
			Text:            text.Msg(text.KeyNewSessionCliDisabledFallback, text.Str("name", machineName), text.Str("missing", lists.AgentInfo(disabledFallback).Name), text.Str("agent", agentCap.Name)),
			Action:          corev1.NoticeActionKind_NOTICE_ACTION_KIND_ENABLE_AGENT,
			TargetMachineId: cfg.MachineID,
			TargetAgent:     disabledFallback,
		})
	} else if res.MissingAgent != v1.Agent_AGENT_UNSPECIFIED && !offline {
		notices = append(notices, &corev1.NewSessionNoticeView{
			Kind:     corev1.NoticeKind_NOTICE_KIND_MISSING_AGENT,
			Severity: corev1.NoticeSeverity_NOTICE_SEVERITY_INFO,
			Text:     text.Msg(text.KeyNewSessionCliFallback, text.Str("name", machineName), text.Str("missing", lists.AgentInfo(res.MissingAgent).Name), text.Str("agent", agentCap.Name)),
			Action:   corev1.NoticeActionKind_NOTICE_ACTION_KIND_CHANGE_MACHINE,
		})
	}
	switch autoIssue {
	case lists.AutoIssueModel:
		notices = append(notices, &corev1.NewSessionNoticeView{
			Kind:     corev1.NoticeKind_NOTICE_KIND_AUTO_UNAVAILABLE,
			Severity: corev1.NoticeSeverity_NOTICE_SEVERITY_WARN,
			Text:     text.Msg(text.KeyNewSessionModelNoAuto, text.Str("name", machineName)),
			Action:   corev1.NoticeActionKind_NOTICE_ACTION_KIND_CHANGE_MODEL,
		})
	case lists.AutoIssueAccount:
		notices = append(notices, &corev1.NewSessionNoticeView{
			Kind:            corev1.NoticeKind_NOTICE_KIND_AUTO_UNAVAILABLE,
			Severity:        corev1.NoticeSeverity_NOTICE_SEVERITY_WARN,
			Text:            text.Msg(text.KeyNewSessionAccountNoAuto, text.Str("name", machineName)),
			Action:          corev1.NoticeActionKind_NOTICE_ACTION_KIND_OPEN_MACHINE_SETTINGS,
			TargetMachineId: cfg.MachineID,
		})
	}
	if draft != nil && st.PrefsSettingsErrorText[key] != nil {
		notices = append(notices, &corev1.NewSessionNoticeView{
			Kind:     corev1.NoticeKind_NOTICE_KIND_CONFIG_SAVE_ERROR,
			Severity: corev1.NoticeSeverity_NOTICE_SEVERITY_WARN,
			Text:     text.Msg(text.KeyNewSessionSaveFailed),
			Action:   corev1.NoticeActionKind_NOTICE_ACTION_KIND_RETRY_SAVE_CONFIG,
		})
	}
	if hidden {
		notices = append(notices, &corev1.NewSessionNoticeView{
			Kind:     corev1.NoticeKind_NOTICE_KIND_PROJECT_REMOVED,
			Severity: corev1.NoticeSeverity_NOTICE_SEVERITY_WARN,
			Text:     text.Msg(text.KeyNewSessionRemovedBlocked),
			Action:   corev1.NoticeActionKind_NOTICE_ACTION_KIND_READD_PROJECT,
		})
	}
	if busy {
		notices = append(notices, &corev1.NewSessionNoticeView{
			Kind:     corev1.NoticeKind_NOTICE_KIND_STARTING,
			Severity: corev1.NoticeSeverity_NOTICE_SEVERITY_INFO,
			Text:     text.Msg(text.KeyNewSessionStartingOn, text.Str("name", machineName)),
		})
	} else if frozen && draft != nil {
		msg := text.Msg(text.KeyNewSessionUnknownStartRetry)
		if draft.Expired {
			if prompt != "" {
				msg = text.Msg(text.KeyNewSessionExpiredStartPrompt, text.Str("prompt", clampRunes(prompt, 24)))
			} else {
				msg = text.Msg(text.KeyNewSessionExpiredStart)
			}
		} else if hasReason(draft.StartErrorText, draft.StartError) {
			msg = text.Msg(text.KeyNewSessionStartUnknown, reasonArg("reason", draft.StartErrorText, draft.StartError))
		}
		notices = append(notices, &corev1.NewSessionNoticeView{
			Kind:     corev1.NoticeKind_NOTICE_KIND_UNKNOWN_START,
			Severity: corev1.NoticeSeverity_NOTICE_SEVERITY_WARN,
			Text:     msg,
			Action:   corev1.NoticeActionKind_NOTICE_ACTION_KIND_ABANDON_START,
		})
	} else if draft != nil && draft.DiskFull {
		notices = append(notices, &corev1.NewSessionNoticeView{
			Kind:            corev1.NoticeKind_NOTICE_KIND_DISK_FULL,
			Severity:        corev1.NoticeSeverity_NOTICE_SEVERITY_ERROR,
			Text:            text.Msg(text.KeyMachineStatusDiskFull),
			Action:          corev1.NoticeActionKind_NOTICE_ACTION_KIND_RETRY_START,
			TargetMachineId: cfg.MachineID,
		})
	} else if draft != nil && hasReason(draft.StartErrorText, draft.StartError) {
		notices = append(notices, &corev1.NewSessionNoticeView{
			Kind:     corev1.NoticeKind_NOTICE_KIND_FAILED,
			Severity: corev1.NoticeSeverity_NOTICE_SEVERITY_ERROR,
			Text:     text.Msg(text.KeyNewSessionStartFailed, reasonArg("reason", draft.StartErrorText, draft.StartError)),
			Action:   corev1.NoticeActionKind_NOTICE_ACTION_KIND_RETRY_START,
		})
	}

	canStart := true
	var disabledReason *corev1.Text
	switch {
	case st.World.Connection != lists.ConnConnected:
		canStart = false
		disabledReason = text.Msg(text.KeyErrorsNotConnected)
	case !res.Found || machine == nil:
		canStart = false
		disabledReason = text.Msg(text.KeyProjectsNoOnline)
	case !machineOnline && listed:
		canStart = false
		disabledReason = text.Msg(text.KeyNewSessionOfflineNoStartNamed, text.Str("name", machineName))
	case res.ProtocolMismatch < 0:
		canStart = false
		disabledReason = text.Msg(text.KeyErrorsRemoteAgentOnIsOutOfDateUpdateIt, text.Str("value", machineName))
	case res.ProtocolMismatch > 0:
		canStart = false
		disabledReason = text.Msg(text.KeyErrorsClientOutOfDate)
	case hidden:
		canStart = false
		disabledReason = text.Msg(text.KeyNewSessionRemovedBlocked)
	case draft != nil && draft.Expired:
		canStart = false
		disabledReason = text.Msg(text.KeyNewSessionExpiredStart)
	case busy:
		canStart = false
		disabledReason = text.Msg(text.KeyNewSessionStartingOn, text.Str("name", machineName))
	case autoIssue == lists.AutoIssueModel:
		canStart = false
		disabledReason = text.Msg(text.KeyModesModelNoAuto)
	case autoIssue == lists.AutoIssueAccount:
		canStart = false
		disabledReason = text.Msg(text.KeyModesAccountNoAuto)
	case diskCritical:
		canStart = false
		disabledReason = text.Msg(text.KeyMachineStatusDiskFull)
	case !drafting:
		canStart = false
		disabledReason = text.Msg(text.KeyNewSessionFirstMessage)
	}

	var protoAutoIssue corev1.AutoIssueKind
	switch autoIssue {
	case lists.AutoIssueModel:
		protoAutoIssue = corev1.AutoIssueKind_AUTO_ISSUE_KIND_MODEL
	case lists.AutoIssueAccount:
		protoAutoIssue = corev1.AutoIssueKind_AUTO_ISSUE_KIND_ACCOUNT
	default:
		protoAutoIssue = corev1.AutoIssueKind_AUTO_ISSUE_KIND_NONE
	}

	computerOnline := machineOnline || (st.DisplayGrace && machine != nil && machine.Online)
	unlinked := !st.DisplayGrace && (lists.WorldUnlinked(&st.World) || lists.MachineUnlinked(machine, &st.World))
	onlineReady := machineOnlineReady(
		st.World.Connection,
		st.DisplayGrace,
		computerOnline,
		unlinked,
		res.ProtocolMismatch != 0,
		st.World.AccountControlLost,
		st.Removed,
	)
	attCaps := attdomain.ResolveCaps(machine, cfg.MachineID, computerOnline, onlineReady)
	prefsBlocked := st.World.AccountControlLost || st.World.AccountClockSkewed || lists.WorldUnlinked(&st.World)
	var prefsBlockedReason *corev1.Text
	switch {
	case st.World.AccountClockSkewed:
		prefsBlockedReason = text.Msg(text.KeyErrorsThisDeviceSClockIsBehindTheAccountStateRemote)
	case st.World.AccountControlLost || lists.WorldUnlinked(&st.World):
		prefsBlockedReason = text.Msg(text.KeyErrorsRemoteControlExpiredSavingChangesSessionControlAndNew)
	}

	modelPanel := buildModelPanelView(st, cfg, modelsResp, hasModels, modelsLoading, modelsErrText, modelsErr, pickedModel, efforts, selectedEffort, defaultModelLabel, frozen, busy, machineOnline)

	var draftView *corev1.AttachmentDraftView
	if st != nil && st.DraftViewFn != nil && key != "" {
		draftView = st.DraftViewFn(&corev1.DraftScope{
			Kind: corev1.DraftScopeKind_DRAFT_SCOPE_KIND_NEW_SESSION,
			Id:   key,
		})
	}
	return &corev1.NewSessionViewModel{
		ProjectSelectable: projectSelectable,
		ProjectKey:        key,
		ProjectName:       projNameText,
		ShortPath:         lists.ShortPath(key),
		HasProjects:       len(projRes.Listed) > 0,
		Prompt:            prompt,
		Drafting:          drafting,
		Busy:              busy,
		Settled:           res.Settled,
		Frozen:            frozen,
		Config: &corev1.NewSessionConfig{
			MachineId:       cfg.MachineID,
			Agent:           cfg.Agent,
			Model:           cfg.Model,
			Effort:          cfg.Effort,
			StandardContext: cfg.StandardContext,
			Mode:            cfg.Mode,
		},
		MachineName:              machineName,
		MachineSelectable:        machineSelectable,
		AgentName:                agentCap.Name,
		AvailableAgents:          agentOpts,
		ModelLabel:               modelLabel,
		EffortLabel:              selectedEffort,
		DefaultMode:              dmView,
		LocationText:             locationText,
		Notices:                  notices,
		CanStart:                 canStart,
		DisabledReason:           disabledReason,
		ModelPanel:               modelPanel,
		ProjectRemoved:           hidden,
		AutoIssue:                protoAutoIssue,
		PrefsBlocked:             prefsBlocked,
		PrefsBlockedReason:       prefsBlockedReason,
		CapabilitiesLoaded:       attCaps.CapabilitiesLoaded,
		SupportsImages:           attCaps.SupportsImages,
		SupportsFiles:            attCaps.SupportsFiles,
		MaxImages:                attCaps.MaxImages,
		MaxFiles:                 attCaps.MaxFiles,
		MaxFileBytes:             attCaps.MaxFileBytes,
		ImageUploadBlockedReason: attCaps.ImageUploadBlockedReason,
		DiskFull:                 diskFull,
		FileUploadBlockedReason:  attCaps.FileUploadBlockedReason,
		GalleryAllowsVideo:       attCaps.SupportsFiles,
		GalleryAcceptMime:        attdomain.GalleryAcceptMime(attCaps.SupportsFiles),
		MaxFileNameBytes:         attdomain.MaxFileNameUTF8Bytes,
		MaxFileSizeText:          attCaps.MaxFileSizeText,
		OriginalImageMaxBytes:    attdomain.OriginalImageMaxBytes,
		OriginalImageHint:        text.Msg(text.KeyImageOriginalHint),
		FileMetadataNotice:       text.Msg(text.KeyAttachmentMetadata),
		Draft:                    draftView,
	}
}

func buildModelPanelView(
	st *State,
	cfg lists.StartConfig,
	modelsResp *v1.ListModelsResponse,
	hasModels bool,
	modelsLoading bool,
	modelsErrText *corev1.Text,
	modelsErr string,
	picked *v1.ModelInfo,
	efforts []string,
	selectedEffort string,
	defaultModelLabel *corev1.Text,
	frozen bool,
	busy bool,
	machineOnline bool,
) *corev1.NewSessionModelPanelView {
	agentCap := lists.AgentInfo(cfg.Agent)
	var modelList []*v1.ModelInfo
	if modelsResp != nil {
		modelList = modelsResp.Models
	}
	modelViews := make([]*corev1.ModelOptionView, 0, len(modelList))
	for _, m := range modelList {
		if m == nil {
			continue
		}
		var fixedNote *corev1.Text
		if len(m.Efforts) == 1 {
			fixedNote = text.Msg(text.KeyModelsFixedEffort, text.Str("effort", m.Efforts[0]))
		}
		modelViews = append(modelViews, &corev1.ModelOptionView{
			Slug:            m.Slug,
			DisplayName:     m.DisplayName,
			FixedEffortNote: fixedNote,
			Checked:         cfg.Model == m.Slug,
			SupportsAuto:    !m.AutoUnavailable,
			Supports_1M:     m.ContextWindow > lists.StandardContextWindow,
		})
	}

	var errText *corev1.Text
	if hasReason(modelsErrText, modelsErr) {
		errText = text.Msg(text.KeyModelsLoadFailedReason, text.Str("agent", agentCap.Name), reasonArg("reason", modelsErrText, modelsErr))
	} else if modelsLoading {
		machine := findMachine(st, cfg.MachineID)
		errText = text.Msg(text.KeyModelsReadingMachine, text.Str("name", lists.MachineName(machine, cfg.MachineID)))
	}

	var effortTitle *corev1.Text
	if len(efforts) > 0 {
		if cfg.Effort == "" {
			effortTitle = text.Msg(text.KeyModelsEffortDefault)
		} else {
			effortTitle = text.Msg(text.KeyModelsEffort)
		}
	}

	showCtx := agentCap.StandardContextOption
	ctxFixed := false
	var ctxNote *corev1.Text
	if showCtx {
		if picked != nil && picked.ContextWindow > 0 && picked.ContextWindow <= lists.StandardContextWindow {
			ctxFixed = true
			ctxNote = text.Msg(text.KeyModelsStandardOnly)
		} else if cfg.StandardContext {
			ctxNote = text.Msg(text.KeyModelsStandardWindow)
		} else {
			ctxNote = text.Msg(text.KeyModelsExtendedWindow)
		}
	}

	machineChoices := make([]*corev1.MachineChoiceView, 0, len(st.World.Machines))
	for _, m := range st.World.Machines {
		if m == nil {
			continue
		}
		online := lists.MachineReachable(m)
		var note *corev1.Text
		if !online {
			note = text.Msg(text.KeyCommonOffline)
		} else if !slices.Contains(lists.MachineAgents(m.Meta), cfg.Agent) {
			note = text.Join(text.KeyCommonNoteSeparator, text.Msg(text.KeyCommonOnline))
		} else {
			note = text.Join(text.KeyCommonNoteSeparator, text.Msg(text.KeyCommonOnline), text.Msg(text.KeyNewSessionReadingCli, text.Str("agent", agentCap.Name)))
		}
		machineChoices = append(machineChoices, &corev1.MachineChoiceView{
			MachineId: m.ID,
			Name:      lists.MachineName(m, m.ID),
			Online:    online,
			Selected:  m.ID == cfg.MachineID,
			Note:      note,
		})
	}

	canSelectAuto := !lists.AutoUnavailable(modelList, hasModels, cfg.Model)

	return &corev1.NewSessionModelPanelView{
		ModelsLoading:         modelsLoading,
		ModelsError:           errText,
		DefaultModelLabel:     defaultModelLabel,
		DefaultModelChecked:   cfg.Model == "",
		Models:                modelViews,
		EffortChoices:         efforts,
		SelectedEffort:        selectedEffort,
		EffortTitle:           effortTitle,
		SupportsContextWindow: showCtx,
		ContextFixed:          ctxFixed,
		StandardContext:       cfg.StandardContext || ctxFixed,
		ContextNote:           ctxNote,
		PanelEnabled:          !busy && !frozen && machineOnline,
		MachineChoices:        machineChoices,
		CanSelectAutoMode:     canSelectAuto,
	}
}

// StandaloneNewSessionKey is the project a new task without one in its route
// works on: the one picked there, else the first listed. Actions and the view
// must agree on it, or picks land on a draft the view never shows.
func StandaloneNewSessionKey(st *State) string {
	return standaloneKey(st, lists.BuildProjects(&st.World, st.ProjectSearch, st.RememberedScope, st.RememberedScopeToken, nil, st.Scope))
}

func standaloneKey(st *State, projRes *lists.ProjectsViewResult) string {
	if st.StandaloneProjectKey != "" {
		return st.StandaloneProjectKey
	}
	if projRes != nil && len(projRes.Listed) > 0 {
		return projRes.Listed[0].Key
	}
	return ""
}
