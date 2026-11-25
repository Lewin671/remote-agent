package app

import (
	"slices"
	"strconv"
	"strings"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/projection"
	"github.com/Lewin671/remote-agent/clientcore/domain/world"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/screens"
	"github.com/Lewin671/remote-agent/clientcore/sync"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

const (
	sessionExternalNoticeHoldMs = 1200
	homeFocusHoldMs             = 900
	homeFocusHoldTimerKey       = "home-focus-hold"
	sessionExtTimerKeyPrefix    = "session-ext:"
)

func isSlotInFlight(ui *screens.SessionUIState, slotKey string) bool {
	if ui == nil || slotKey == "" {
		return false
	}
	rec := ui.Commands[slotKey]
	return rec != nil && (rec.Busy || rec.Status == corev1.SessionCommandStatus_SESSION_COMMAND_STATUS_PENDING)
}

func toggleInt32Sorted(in []int32, val int32) []int32 {
	out := make([]int32, 0, len(in)+1)
	found := false
	for _, x := range in {
		if x == val {
			found = true
			continue
		}
		out = append(out, x)
	}
	if !found {
		out = append(out, val)
	}
	for i := 1; i < len(out); i++ {
		cur := out[i]
		j := i - 1
		for j >= 0 && out[j] > cur {
			out[j+1] = out[j]
			j--
		}
		out[j+1] = cur
	}
	return out
}

func dedupInt32Sorted(in []int32) []int32 {
	if len(in) == 0 {
		return nil
	}
	out := make([]int32, 0, len(in))
	for _, x := range in {
		if x <= 0 {
			continue
		}
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	for i := 1; i < len(out); i++ {
		cur := out[i]
		j := i - 1
		for j >= 0 && out[j] > cur {
			out[j+1] = out[j]
			j--
		}
		out[j+1] = cur
	}
	return out
}

func (c *Core) resolveSessionActionTarget(store *sessionStore, sid string) (machineID string, snap world.SessionSnapshot, ui *screens.SessionUIState, ok bool) {
	if sid == "" {
		return "", world.SessionSnapshot{}, nil, false
	}
	snap = c.m.sync.SessionSnapshot(sid)
	ui = store.ensureUI(sid)
	reconcileSessionSnapshot(snap, ui)
	machineID = c.sessionMachineID(sid, snap)
	if !snap.Found || machineID == "" {
		return "", snap, ui, false
	}
	return machineID, snap, ui, true
}

func approvalSummaryText(allow, always bool) *corev1.Text {
	if allow {
		if always {
			return text.Msg(text.KeyApprovalAlwaysAllowed)
		}
		return text.Msg(text.KeyApprovalAllowed)
	}
	return text.Msg(text.KeyApprovalDenied)
}

func planChoiceSummaryText(choice v1.PlanChoice) *corev1.Text {
	switch choice {
	case v1.PlanChoice_PLAN_CHOICE_CLEAR_AND_IMPLEMENT:
		return text.Msg(text.KeyDockPlanClearRecord)
	case v1.PlanChoice_PLAN_CHOICE_IMPLEMENT:
		return text.Msg(text.KeyDockPlanImplementRecord)
	case v1.PlanChoice_PLAN_CHOICE_KEEP_PLANNING:
		return text.Msg(text.KeyDockPlanKeep)
	default:
		return nil
	}
}

func questionOptionLabels(snap world.SessionSnapshot, qID string, activeIdx int32, nums []int32) string {
	if len(nums) == 0 {
		return ""
	}
	var opts []*v1.QuestionOption
	if snap.Projection != nil {
		for _, pq := range snap.Projection.Questions {
			if pq != nil && pq.QuestionId == qID && pq.Request != nil &&
				activeIdx >= 0 && int(activeIdx) < len(pq.Request.Questions) && pq.Request.Questions[activeIdx] != nil {
				opts = pq.Request.Questions[activeIdx].Options
				break
			}
		}
	}
	if len(opts) == 0 && snap.Session != nil && snap.Session.Pending.Question != nil &&
		snap.Session.Pending.Question.QuestionID == qID && activeIdx == 0 {
		labels := make([]string, 0, len(nums))
		for _, num := range nums {
			idx := int(num - 1)
			if idx >= 0 && idx < len(snap.Session.Pending.Question.Options) {
				lbl, _ := lists.OptionLabel(snap.Session.Pending.Question.Options[idx].Label)
				lbl = strings.TrimSpace(lbl)
				if lbl != "" {
					labels = append(labels, lbl)
					continue
				}
			}
			labels = append(labels, "#"+strconv.Itoa(int(num)))
		}
		return strings.Join(labels, "、")
	}
	labels := make([]string, 0, len(nums))
	for _, num := range nums {
		idx := int(num - 1)
		if idx >= 0 && idx < len(opts) && opts[idx] != nil {
			lbl, _ := lists.OptionLabel(opts[idx].Label)
			lbl = strings.TrimSpace(lbl)
			if lbl != "" {
				labels = append(labels, lbl)
				continue
			}
		}
		labels = append(labels, "#"+strconv.Itoa(int(num)))
	}
	return strings.Join(labels, "、")
}

func isPendingApprovalSkeleton(snap world.SessionSnapshot, approvalID string) bool {
	if snap.Projection != nil {
		for _, pa := range snap.Projection.Approvals {
			if pa != nil && pa.ApprovalId == approvalID {
				return lists.IsApprovalSkeleton(pa.Request)
			}
		}
	}
	if snap.Session != nil && snap.Session.Pending.HasApproval && snap.Session.Pending.ApprovalID == approvalID {
		return snap.Session.Pending.ApprovalSkeleton
	}
	return false
}

func findPendingQuestionMeta(snap world.SessionSnapshot, qID string, activeIdx int32) (multiSelect bool, total int32, isSkeleton bool) {
	if snap.Projection != nil {
		for _, pq := range snap.Projection.Questions {
			if pq != nil && pq.QuestionId == qID {
				if pq.Request == nil {
					return false, 0, false
				}
				if projection.IsQuestionSkeleton(pq.Request) {
					return false, int32(len(pq.Request.Questions)), true
				}
				total = int32(len(pq.Request.Questions))
				if activeIdx >= 0 && int(activeIdx) < len(pq.Request.Questions) && pq.Request.Questions[activeIdx] != nil {
					multiSelect = pq.Request.Questions[activeIdx].MultiSelect
				}
				return multiSelect, total, false
			}
		}
	}
	if snap.Session != nil && snap.Session.Pending.Question != nil && snap.Session.Pending.Question.QuestionID == qID {
		q := snap.Session.Pending.Question
		return q.MultiSelect, q.QuestionCount, false
	}
	return false, 0, false
}

func (c *Core) handleSessionEditApprovalDeny(ctx *sync.StepContext, store *sessionStore, act *corev1.SessionEditApprovalDenyAction) {
	if act == nil || act.SessionId == "" || act.ApprovalId == "" {
		return
	}
	ui := store.ensureUI(act.SessionId)
	if act.DenyingOpen {
		ui.ApprovalDenyOpen[act.ApprovalId] = true
	} else {
		delete(ui.ApprovalDenyOpen, act.ApprovalId)
	}
	if hf := c.m.state.HomeFocus; hf != nil && hf.Kind == screens.HomeFocusApproval &&
		hf.SessionID == act.SessionId && hf.ItemID == act.ApprovalId {
		hf.DenyingOpen = act.DenyingOpen
	}
	store.pendingDirty = true
	ctx.Dirty.Add(true, false, false)
}

func (c *Core) handleSessionResolveApproval(ctx *sync.StepContext, store *sessionStore, act *corev1.SessionResolveApprovalAction) []*corev1.Effect {
	if act == nil || act.SessionId == "" || act.ApprovalId == "" {
		return nil
	}
	machineID, snap, ui, ok := c.resolveSessionActionTarget(store, act.SessionId)
	if !ok || isPendingApprovalSkeleton(snap, act.ApprovalId) {
		return nil
	}
	slotKey := "approval:" + act.SessionId + ":" + act.ApprovalId
	if isSlotInFlight(ui, slotKey) {
		return nil
	}
	denyReason := ""
	if !act.Allow {
		denyReason = strings.TrimSpace(act.DenyReason)
	}
	// If a frozen/unknown attempt exists and the user changed their deny reason (or decision),
	// abandon the old frozen attempt automatically before creating a fresh request_id.
	if prev := ui.Commands[slotKey]; prev != nil && (prev.Frozen || c.m.sync.HasFrozenAttempt(slotKey)) {
		if prev.Expired {
			return nil
		}
		prevRA := prev.Command.GetResolveApproval()
		changed := prevRA == nil || prevRA.Allow != act.Allow || prevRA.Always != act.Always || (!act.Allow && prevRA.Message != denyReason)
		if changed {
			c.m.sync.AbandonAttempt(ctx, slotKey)
			delete(ui.Commands, slotKey)
			delete(store.slotMeta, slotKey)
		}
	}

	cmd := &v1.Command{
		ProtocolVersion: sync.PeerVersion,
		Kind: &v1.Command_ResolveApproval{
			ResolveApproval: &v1.ResolveApprovalRequest{
				SessionId:  act.SessionId,
				ApprovalId: act.ApprovalId,
				Allow:      act.Allow,
				Always:     act.Always,
				Message:    denyReason,
			},
		},
	}
	fillObservedPreconditions(cmd, snap, ui)
	ui.ExternalResolution = nil
	c.submitSessionCommandDirect(ctx, store, ui, act.SessionId, machineID, slotKey, slotKindResolveApproval, "", 0, "", cmd)
	if rec := ui.Commands[slotKey]; rec != nil {
		rec.SubmittedSummary = approvalSummaryText(act.Allow, act.Always)
		rec.DenySubmit = !act.Allow
		if rec.RequestID != "" {
			ui.LocalResolvedRequestID["approval:"+act.ApprovalId] = rec.RequestID
		}
	}
	ctx.Dirty.Add(true, false, false)
	return nil
}

func (c *Core) handleSessionToggleQuestionOption(store *sessionStore, act *corev1.SessionToggleQuestionOptionAction) {
	if act == nil || act.SessionId == "" || act.QuestionId == "" || act.OptionNum <= 0 {
		return
	}
	ui := store.ensureUI(act.SessionId)
	activeIdx := ui.QuestionIndexByID[act.QuestionId]
	qKey := act.QuestionId + "#" + strconv.Itoa(int(activeIdx))
	slotKey := "question:" + act.SessionId + ":" + qKey
	if isSlotInFlight(ui, slotKey) {
		return
	}
	if rec := ui.Commands[slotKey]; rec != nil {
		if rec.Frozen {
			return
		}
		if rec.Status == corev1.SessionCommandStatus_SESSION_COMMAND_STATUS_FAILED {
			delete(ui.Commands, slotKey)
			delete(store.slotMeta, slotKey)
		}
	}
	next := toggleInt32Sorted(ui.QuestionDraftByKey[qKey], act.OptionNum)
	if len(next) == 0 {
		delete(ui.QuestionDraftByKey, qKey)
	} else {
		ui.QuestionDraftByKey[qKey] = next
	}
	store.pendingDirty = true
}

func (c *Core) handleSessionAnswerQuestion(ctx *sync.StepContext, store *sessionStore, act *corev1.SessionAnswerQuestionAction, fallbackObservedSeq uint64) []*corev1.Effect {
	if act == nil || act.SessionId == "" || act.QuestionId == "" {
		return nil
	}
	machineID, snap, ui, ok := c.resolveSessionActionTarget(store, act.SessionId)
	if !ok {
		return nil
	}
	activeIdx := ui.QuestionIndexByID[act.QuestionId]
	multiSelect, _, isSkeleton := findPendingQuestionMeta(snap, act.QuestionId, activeIdx)
	if isSkeleton {
		return nil
	}
	qKey := act.QuestionId + "#" + strconv.Itoa(int(activeIdx))
	slotKey := "question:" + act.SessionId + ":" + qKey
	if isSlotInFlight(ui, slotKey) {
		return nil
	}
	if prev := ui.Commands[slotKey]; prev != nil && prev.Expired {
		return nil
	}

	var picked []int32
	if multiSelect {
		if len(act.OptionNums) > 0 {
			picked = dedupInt32Sorted(act.OptionNums)
		} else {
			picked = dedupInt32Sorted(ui.QuestionDraftByKey[qKey])
		}
		if len(picked) == 0 {
			return nil
		}
	} else {
		if act.OptionNum <= 0 {
			return nil
		}
		picked = []int32{act.OptionNum}
	}

	req := &v1.AnswerQuestionRequest{
		SessionId:        act.SessionId,
		QuestionId:       act.QuestionId,
		ObservedEventSeq: fallbackObservedSeq,
	}
	if multiSelect {
		req.Options = append([]int32(nil), picked...)
	} else {
		req.Option = picked[0]
	}
	idx := activeIdx
	req.QuestionIndex = &idx
	cmd := &v1.Command{
		ProtocolVersion: sync.PeerVersion,
		Kind: &v1.Command_AnswerQuestion{
			AnswerQuestion: req,
		},
	}
	fillObservedPreconditions(cmd, snap, ui)
	ui.ExternalResolution = nil
	c.submitSessionCommandDirect(ctx, store, ui, act.SessionId, machineID, slotKey, slotKindAnswerQuestion, act.QuestionId, activeIdx, "", cmd)
	if rec := ui.Commands[slotKey]; rec != nil {
		rec.SubmittedOptionNums = append([]int32(nil), picked...)
		if lbl := questionOptionLabels(snap, act.QuestionId, activeIdx, picked); lbl != "" {
			rec.SubmittedSummary = text.Plain(lbl)
		}
		if rec.RequestID != "" {
			ui.LocalResolvedRequestID["question:"+act.QuestionId] = rec.RequestID
		}
	}
	ctx.Dirty.Add(true, false, false)
	return nil
}

func (c *Core) handleSessionAnswerPlan(ctx *sync.StepContext, store *sessionStore, act *corev1.SessionAnswerPlanAction) []*corev1.Effect {
	if act == nil || act.SessionId == "" || act.Choice == v1.PlanChoice_PLAN_CHOICE_UNSPECIFIED {
		return nil
	}
	machineID, snap, ui, ok := c.resolveSessionActionTarget(store, act.SessionId)
	if !ok {
		return nil
	}
	if snap.Projection != nil && snap.Projection.Plan != nil && projection.IsSkeletonText(snap.Projection.Plan.Text) {
		return nil
	}
	slotKey := "plan:" + act.SessionId
	if isSlotInFlight(ui, slotKey) {
		return nil
	}
	if prev := ui.Commands[slotKey]; prev != nil && prev.Expired {
		return nil
	}
	cmd := &v1.Command{
		ProtocolVersion: sync.PeerVersion,
		Kind: &v1.Command_AnswerPlan{
			AnswerPlan: &v1.AnswerPlanRequest{
				SessionId: act.SessionId,
				Choice:    act.Choice,
			},
		},
	}
	fillObservedPreconditions(cmd, snap, ui)
	if !c.m.sync.HasFrozenAttempt(slotKey) {
		c.submitPlanPreflight(ctx, store, ui, act.SessionId, machineID, slotKey, cmd)
	} else {
		c.submitSessionCommandDirect(ctx, store, ui, act.SessionId, machineID, slotKey, slotKindAnswerPlan, "", 0, "", cmd)
	}
	if rec := ui.Commands[slotKey]; rec != nil {
		rec.SubmittedPlanChoice = act.Choice
		rec.SubmittedSummary = planChoiceSummaryText(act.Choice)
	}
	return nil
}

func (c *Core) handleSessionSimpleCommand(ctx *sync.StepContext, store *sessionStore, act *corev1.SessionSimpleCommandAction) []*corev1.Effect {
	if act == nil || act.SessionId == "" {
		return nil
	}
	machineID, _, ui, ok := c.resolveSessionActionTarget(store, act.SessionId)
	if !ok {
		return nil
	}
	var cmd *v1.Command
	var kind sessionSlotKind = slotKindGeneric
	switch act.Kind {
	case corev1.SessionSimpleCommandKind_SESSION_SIMPLE_COMMAND_KIND_INTERRUPT:
		cmd = &v1.Command{
			ProtocolVersion: sync.PeerVersion,
			Kind:            &v1.Command_Interrupt{Interrupt: &v1.InterruptRequest{SessionId: act.SessionId}},
		}
	case corev1.SessionSimpleCommandKind_SESSION_SIMPLE_COMMAND_KIND_STOP:
		cmd = &v1.Command{
			ProtocolVersion: sync.PeerVersion,
			Kind:            &v1.Command_StopSession{StopSession: &v1.StopSessionRequest{SessionId: act.SessionId}},
		}
	case corev1.SessionSimpleCommandKind_SESSION_SIMPLE_COMMAND_KIND_RESUME:
		cmd = &v1.Command{
			ProtocolVersion: sync.PeerVersion,
			Kind:            &v1.Command_ResumeSession{ResumeSession: &v1.ResumeSessionRequest{SessionId: act.SessionId}},
		}
	case corev1.SessionSimpleCommandKind_SESSION_SIMPLE_COMMAND_KIND_FORK:
		kind = slotKindFork
		cmd = &v1.Command{
			ProtocolVersion: sync.PeerVersion,
			Kind:            &v1.Command_ForkSession{ForkSession: &v1.ForkSessionRequest{SessionId: act.SessionId}},
		}
	case corev1.SessionSimpleCommandKind_SESSION_SIMPLE_COMMAND_KIND_COMPACT:
		cmd = &v1.Command{
			ProtocolVersion: sync.PeerVersion,
			Kind:            &v1.Command_Compact{Compact: &v1.CompactRequest{SessionId: act.SessionId}},
		}
	default:
		return nil
	}
	slotKey := canonicalSlotKey(cmd, act.SessionId, 0)
	if isSlotInFlight(ui, slotKey) {
		return nil
	}
	c.submitSessionCommandDirect(ctx, store, ui, act.SessionId, machineID, slotKey, kind, "", 0, "", cmd)
	return nil
}

func (c *Core) handleSessionRename(ctx *sync.StepContext, store *sessionStore, act *corev1.SessionRenameAction) []*corev1.Effect {
	if act == nil || act.SessionId == "" {
		return nil
	}
	machineID, snap, ui, ok := c.resolveSessionActionTarget(store, act.SessionId)
	if !ok {
		return nil
	}
	slotKey := "rename:" + act.SessionId
	if isSlotInFlight(ui, slotKey) {
		return nil
	}
	clean := strings.TrimSpace(lists.TitleInput(strings.TrimSpace(act.Title)))
	plainTitle, hasCustom, _ := lists.ResolveTitle(snap.Session, c.m.state.Cal)
	if clean == strings.TrimSpace(plainTitle) || (!hasCustom && clean == "") {
		ui.SlotSucceededSeq[slotKey]++
		delete(ui.Commands, slotKey)
		delete(store.slotMeta, slotKey)
		store.pendingDirty = true
		return nil
	}
	cmd := &v1.Command{
		ProtocolVersion: sync.PeerVersion,
		Kind: &v1.Command_RenameSession{
			RenameSession: &v1.RenameSessionRequest{
				SessionId: act.SessionId,
				Title:     clean,
			},
		},
	}
	c.submitSessionCommandDirect(ctx, store, ui, act.SessionId, machineID, slotKey, slotKindGeneric, "", 0, "", cmd)
	return nil
}

func (c *Core) handleSessionSetApprovalMode(ctx *sync.StepContext, store *sessionStore, act *corev1.SessionSetApprovalModeAction) []*corev1.Effect {
	if act == nil || act.SessionId == "" || act.Mode == v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED {
		return nil
	}
	machineID, snap, ui, ok := c.resolveSessionActionTarget(store, act.SessionId)
	if !ok {
		return nil
	}
	slotKey := "set_mode:" + act.SessionId
	if isSlotInFlight(ui, slotKey) {
		return nil
	}
	curMode, _ := lists.SessionMode(snap.Session)
	if curMode == act.Mode {
		ui.SlotSucceededSeq[slotKey]++
		delete(ui.Commands, slotKey)
		delete(store.slotMeta, slotKey)
		store.pendingDirty = true
		return nil
	}
	cmd := &v1.Command{
		ProtocolVersion: sync.PeerVersion,
		Kind: &v1.Command_SetApprovalMode{
			SetApprovalMode: &v1.SetApprovalModeRequest{
				SessionId: act.SessionId,
				Mode:      act.Mode,
			},
		},
	}
	c.submitSessionCommandDirect(ctx, store, ui, act.SessionId, machineID, slotKey, slotKindGeneric, "", 0, "", cmd)
	return nil
}

func (c *Core) handleSessionSetModel(ctx *sync.StepContext, store *sessionStore, act *corev1.SessionSetModelAction) []*corev1.Effect {
	if act == nil || act.SessionId == "" || act.Model == "" {
		return nil
	}
	machineID, snap, ui, ok := c.resolveSessionActionTarget(store, act.SessionId)
	if !ok {
		return nil
	}
	slotKey := "set_model:" + act.SessionId
	if isSlotInFlight(ui, slotKey) {
		return nil
	}
	agent := lists.SessionAgent(snap.Session)
	var curEffort string
	if snap.Session != nil && snap.Session.Info != nil {
		curEffort = snap.Session.Info.Effort
	}
	var modelSlice []*v1.ModelInfo
	if entry := c.m.state.MachineModels[screens.MachineAgentKey{MachineID: machineID, Agent: agent}]; entry != nil && entry.Response != nil {
		modelSlice = entry.Response.Models
	}
	target := lists.FindModel(modelSlice, act.Model)
	effort := act.Effort
	if effort == "" && target != nil {
		switch {
		case len(target.Efforts) == 0:
			effort = ""
		case curEffort != "" && slices.Contains(target.Efforts, curEffort):
			effort = curEffort
		default:
			effort = target.DefaultEffort
		}
	}
	cmd := &v1.Command{
		ProtocolVersion: sync.PeerVersion,
		Kind: &v1.Command_SetModel{
			SetModel: &v1.SetModelRequest{
				SessionId: act.SessionId,
				Model:     act.Model,
				Effort:    effort,
			},
		},
	}
	c.submitSessionCommandDirect(ctx, store, ui, act.SessionId, machineID, slotKey, slotKindGeneric, "", 0, "", cmd)
	if rec := ui.Commands[slotKey]; rec != nil {
		label := act.Model
		if target != nil && target.DisplayName != "" {
			label = target.DisplayName
		}
		rec.SubmittedSummary = text.Plain(label)
	}
	return nil
}

func (c *Core) handleSessionRetryCommand(ctx *sync.StepContext, store *sessionStore, act *corev1.SessionRetryCommandAction) []*corev1.Effect {
	if act == nil || act.SessionId == "" || act.SlotKey == "" {
		return nil
	}
	machineID, snap, ui, ok := c.resolveSessionActionTarget(store, act.SessionId)
	if !ok {
		return nil
	}
	rec := ui.Commands[act.SlotKey]
	if rec == nil || rec.Command == nil || isSlotInFlight(ui, act.SlotKey) {
		return nil
	}
	if rec.Status == corev1.SessionCommandStatus_SESSION_COMMAND_STATUS_UNKNOWN && rec.Expired {
		return nil
	}
	if rec.Status != corev1.SessionCommandStatus_SESSION_COMMAND_STATUS_FAILED &&
		rec.Status != corev1.SessionCommandStatus_SESSION_COMMAND_STATUS_UNKNOWN {
		return nil
	}
	cmd := rec.Command.Clone()
	summary := rec.SubmittedSummary
	denySubmit := rec.DenySubmit
	submittedOpts := append([]int32(nil), rec.SubmittedOptionNums...)
	submittedPlan := rec.SubmittedPlanChoice

	kind, qID, qIdx, outReqID := classifySessionCommand(cmd, act.SessionId)
	if kind == slotKindAnswerPlan && !c.m.sync.HasFrozenAttempt(act.SlotKey) {
		fillObservedPreconditions(cmd, snap, ui)
		c.submitPlanPreflight(ctx, store, ui, act.SessionId, machineID, act.SlotKey, cmd)
	} else {
		// For UNKNOWN (frozen), sync.SubmitCommand replays the frozen PreparedCommand with the SAME request_id.
		// For FAILED (not frozen), sync.SubmitCommand allocates a NEW request_id for the cloned command.
		if rec.Status == corev1.SessionCommandStatus_SESSION_COMMAND_STATUS_FAILED {
			fillObservedPreconditions(cmd, snap, ui)
		}
		c.submitSessionCommandDirect(ctx, store, ui, act.SessionId, machineID, act.SlotKey, kind, qID, qIdx, outReqID, cmd)
	}
	if nextRec := ui.Commands[act.SlotKey]; nextRec != nil {
		nextRec.SubmittedSummary = summary
		nextRec.DenySubmit = denySubmit
		nextRec.SubmittedOptionNums = submittedOpts
		nextRec.SubmittedPlanChoice = submittedPlan
		if kind == slotKindResolveApproval && cmd.GetResolveApproval() != nil && nextRec.RequestID != "" {
			ui.LocalResolvedRequestID["approval:"+cmd.GetResolveApproval().ApprovalId] = nextRec.RequestID
		} else if kind == slotKindAnswerQuestion && qID != "" && nextRec.RequestID != "" {
			ui.LocalResolvedRequestID["question:"+qID] = nextRec.RequestID
		}
	}
	ctx.Dirty.Add(true, false, false)
	return nil
}

// --- Home attention semantic actions ---

func (c *Core) handleHomeEditApprovalDeny(ctx *sync.StepContext, act *corev1.HomeEditApprovalDenyAction) {
	if act == nil || act.ApprovalId == "" {
		return
	}
	sid := act.SessionId
	if sid == "" {
		if c.m.state.HomeLastTopKind == screens.HomeFocusApproval && c.m.state.HomeLastTopItemID == act.ApprovalId {
			sid = c.m.state.HomeLastTopSessionID
		}
	}
	if sid == "" {
		_, topSID, topItemID, _, _ := screens.CurrentHomeTopAttention(c.m.state)
		if topItemID == act.ApprovalId {
			sid = topSID
		}
	}
	if sid == "" {
		return
	}
	store := c.m.ensureSessionStore()
	c.handleSessionEditApprovalDeny(ctx, store, &corev1.SessionEditApprovalDenyAction{
		SessionId:   sid,
		ApprovalId:  act.ApprovalId,
		DenyingOpen: act.DenyingOpen,
	})
	if c.m.state.HomeFocus == nil || c.m.state.HomeFocus.SessionID != sid || c.m.state.HomeFocus.ItemID != act.ApprovalId {
		if act.DenyingOpen {
			_, _, _, topApproval, _ := screens.CurrentHomeTopAttention(c.m.state)
			c.m.state.HomeFocus = &screens.HomeFocusState{
				Kind:         screens.HomeFocusApproval,
				SessionID:    sid,
				ItemID:       act.ApprovalId,
				Phase:        corev1.HomeFocusPhase_HOME_FOCUS_PHASE_IDLE,
				DenyingOpen:  true,
				HeldApproval: topApproval,
			}
		}
	} else {
		c.m.state.HomeFocus.DenyingOpen = act.DenyingOpen
	}
	ctx.Dirty.Add(true, false, false)
}

func (c *Core) handleHomeResolveApproval(ctx *sync.StepContext, act *corev1.HomeResolveApprovalAction) []*corev1.Effect {
	if act == nil || act.SessionId == "" || act.ApprovalId == "" {
		return nil
	}
	store := c.m.ensureSessionStore()
	_, topSID, topID, topApproval, _ := screens.CurrentHomeTopAttention(c.m.state)
	var held *corev1.AttentionCardView
	if topApproval != nil && topSID == act.SessionId && topID == act.ApprovalId {
		held = topApproval
	} else if c.m.state.HomeLastTopApproval != nil && c.m.state.HomeLastTopSessionID == act.SessionId && c.m.state.HomeLastTopItemID == act.ApprovalId {
		held = c.m.state.HomeLastTopApproval
	}
	ui := store.ensureUI(act.SessionId)
	c.m.state.HomeFocus = &screens.HomeFocusState{
		Kind:         screens.HomeFocusApproval,
		SessionID:    act.SessionId,
		ItemID:       act.ApprovalId,
		Phase:        corev1.HomeFocusPhase_HOME_FOCUS_PHASE_SUBMITTING,
		DenyingOpen:  !act.Allow && ui.ApprovalDenyOpen[act.ApprovalId],
		HeldApproval: held,
	}
	eff := c.handleSessionResolveApproval(ctx, store, &corev1.SessionResolveApprovalAction{
		SessionId:  act.SessionId,
		ApprovalId: act.ApprovalId,
		Allow:      act.Allow,
		Always:     act.Always,
		DenyReason: act.DenyReason,
	})
	ctx.Dirty.Add(true, false, false)
	return eff
}

func (c *Core) handleHomeAnswerQuestion(ctx *sync.StepContext, act *corev1.HomeAnswerQuestionAction) []*corev1.Effect {
	if act == nil || act.SessionId == "" || act.QuestionId == "" || act.OptionNum <= 0 {
		return nil
	}
	store := c.m.ensureSessionStore()
	_, topSID, topID, _, topQuestion := screens.CurrentHomeTopAttention(c.m.state)
	var held *corev1.HomeQuestionCardView
	if topQuestion != nil && topSID == act.SessionId && topID == act.QuestionId {
		held = topQuestion
	} else if c.m.state.HomeLastTopQuestion != nil && c.m.state.HomeLastTopSessionID == act.SessionId && c.m.state.HomeLastTopItemID == act.QuestionId {
		held = c.m.state.HomeLastTopQuestion
	}
	var pickedLabel string
	var observedSeq uint64
	if held != nil {
		observedSeq = held.ObservedEventSeq
		for _, opt := range held.Options {
			if opt != nil && opt.OptionNum == act.OptionNum {
				pickedLabel = opt.Label
				break
			}
		}
	}
	if pickedLabel == "" {
		snap := c.m.sync.SessionSnapshot(act.SessionId)
		pickedLabel = questionOptionLabels(snap, act.QuestionId, 0, []int32{act.OptionNum})
	}
	c.m.state.HomeFocus = &screens.HomeFocusState{
		Kind:            screens.HomeFocusQuestion,
		SessionID:       act.SessionId,
		ItemID:          act.QuestionId,
		Phase:           corev1.HomeFocusPhase_HOME_FOCUS_PHASE_SUBMITTING,
		PickedOptionNum: act.OptionNum,
		PickedLabel:     pickedLabel,
		HeldQuestion:    held,
	}
	eff := c.handleSessionAnswerQuestion(ctx, store, &corev1.SessionAnswerQuestionAction{
		SessionId:  act.SessionId,
		QuestionId: act.QuestionId,
		OptionNum:  act.OptionNum,
	}, observedSeq)
	ctx.Dirty.Add(true, false, false)
	return eff
}

func (c *Core) resolveHomeAttentionSlotKey(ui *screens.SessionUIState, sid, itemID string) string {
	if ui == nil || sid == "" || itemID == "" {
		return ""
	}
	aKey := "approval:" + sid + ":" + itemID
	if ui.Commands[aKey] != nil {
		return aKey
	}
	qKey := "question:" + sid + ":" + itemID + "#0"
	if ui.Commands[qKey] != nil {
		return qKey
	}
	if hf := c.m.state.HomeFocus; hf != nil && hf.SessionID == sid && hf.ItemID == itemID {
		if hf.Kind == screens.HomeFocusQuestion {
			return qKey
		}
		if hf.Kind == screens.HomeFocusApproval {
			return aKey
		}
	}
	return aKey
}

func (c *Core) handleHomeRetryAttention(ctx *sync.StepContext, act *corev1.HomeRetryAttentionAction) []*corev1.Effect {
	if act == nil || act.SessionId == "" || act.ItemId == "" {
		return nil
	}
	store := c.m.ensureSessionStore()
	ui := store.ensureUI(act.SessionId)
	slotKey := c.resolveHomeAttentionSlotKey(ui, act.SessionId, act.ItemId)
	if slotKey == "" {
		return nil
	}
	if hf := c.m.state.HomeFocus; hf != nil && hf.SessionID == act.SessionId && hf.ItemID == act.ItemId {
		hf.Phase = corev1.HomeFocusPhase_HOME_FOCUS_PHASE_SUBMITTING
	}
	eff := c.handleSessionRetryCommand(ctx, store, &corev1.SessionRetryCommandAction{
		SessionId: act.SessionId,
		SlotKey:   slotKey,
	})
	ctx.Dirty.Add(true, false, false)
	return eff
}

func (c *Core) handleHomeAbandonAttention(ctx *sync.StepContext, act *corev1.HomeAbandonAttentionAction) {
	if act == nil || act.SessionId == "" || act.ItemId == "" {
		return
	}
	store := c.m.ensureSessionStore()
	ui := store.ensureUI(act.SessionId)
	slotKey := c.resolveHomeAttentionSlotKey(ui, act.SessionId, act.ItemId)
	if slotKey == "" {
		return
	}
	c.m.sync.AbandonAttempt(ctx, slotKey)
	delete(ui.Commands, slotKey)
	delete(store.slotMeta, slotKey)
	if hf := c.m.state.HomeFocus; hf != nil && hf.SessionID == act.SessionId && hf.ItemID == act.ItemId {
		hf.Phase = corev1.HomeFocusPhase_HOME_FOCUS_PHASE_IDLE
	}
	store.pendingDirty = true
	ctx.Dirty.Add(true, false, false)
}

// --- Deterministic Origin resolution & transient UI timers (B.4) ---

func classifyResolutionOrigin(origin *v1.Origin, selfDeviceID, localReqID string, localSucceeded bool) corev1.ResolutionOutcome {
	if origin != nil {
		switch origin.Kind {
		case v1.OriginKind_ORIGIN_KIND_CLIENT:
			if (selfDeviceID != "" && origin.DeviceId == selfDeviceID) ||
				(localReqID != "" && origin.RequestId == localReqID) {
				return corev1.ResolutionOutcome_RESOLUTION_OUTCOME_THIS_CLIENT
			}
			return corev1.ResolutionOutcome_RESOLUTION_OUTCOME_OTHER_CLIENT
		case v1.OriginKind_ORIGIN_KIND_LOCAL, v1.OriginKind_ORIGIN_KIND_SYSTEM:
			return corev1.ResolutionOutcome_RESOLUTION_OUTCOME_TERMINAL
		}
	}
	if localSucceeded {
		return corev1.ResolutionOutcome_RESOLUTION_OUTCOME_THIS_CLIENT
	}
	return corev1.ResolutionOutcome_RESOLUTION_OUTCOME_UNSPECIFIED
}

func externalResolutionNoticeText(outcome corev1.ResolutionOutcome) *corev1.Text {
	switch outcome {
	case corev1.ResolutionOutcome_RESOLUTION_OUTCOME_TERMINAL:
		return text.Msg(text.KeyApprovalHandledInTerminal)
	case corev1.ResolutionOutcome_RESOLUTION_OUTCOME_OTHER_CLIENT:
		return text.Msg(text.KeyApprovalHandledOtherDevice)
	default:
		return nil
	}
}

func (c *Core) notifyHomeAttentionSettled(
	meta sessionSlotMeta,
	succeeded bool,
	summary *corev1.Text,
	effects *[]*corev1.Effect,
	dirty *sync.DirtyFlags,
) {
	hf := c.m.state.HomeFocus
	if hf == nil || hf.SessionID != meta.sessionID {
		return
	}
	matched := (hf.Kind == screens.HomeFocusApproval && meta.kind == slotKindResolveApproval && hf.ItemID == meta.approvalID) ||
		(hf.Kind == screens.HomeFocusQuestion && meta.kind == slotKindAnswerQuestion && hf.ItemID == meta.questionID)
	if !matched {
		return
	}
	if !succeeded {
		hf.Phase = corev1.HomeFocusPhase_HOME_FOCUS_PHASE_FAILED
		dirty.Add(true, false, false)
		return
	}
	hf.Phase = corev1.HomeFocusPhase_HOME_FOCUS_PHASE_RESOLVED_THIS_CLIENT
	hf.DenyingOpen = false
	if hf.Kind == screens.HomeFocusQuestion {
		answerText := hf.PickedLabel
		if answerText == "" && summary != nil {
			answerText = summary.Literal
		}
		hf.ResolutionText = text.Msg(text.KeyHomeQuestionAnsweredLine, text.Str("answer", answerText))
	} else {
		if summary != nil {
			hf.ResolutionText = summary
		} else {
			hf.ResolutionText = text.Msg(text.KeyApprovalAllowed)
		}
	}
	stepPrev, ctx := c.m.sync.BeginStep()
	c.m.sync.ScheduleAppTimer(ctx, corev1.TimerKind_TIMER_KIND_UI_TRANSIENT, homeFocusHoldTimerKey, homeFocusHoldMs)
	stepRes := c.m.sync.EndStep(stepPrev, ctx)
	*effects = append(*effects, stepRes.Effects...)
	dirty.Add(true, false, false)
}

func (c *Core) reconcileSessionAndHomeTransient(effects *[]*corev1.Effect, dirty *sync.DirtyFlags) {
	stepPrev, ctx := c.m.sync.BeginStep()
	c.reconcileSessionExternalResolutions(ctx)
	c.reconcileHomeFocusState(ctx)
	stepRes := c.m.sync.EndStep(stepPrev, ctx)
	if len(stepRes.Effects) > 0 {
		*effects = append(*effects, stepRes.Effects...)
	}
	if stepRes.Dirty.Home || stepRes.Dirty.Project || stepRes.Dirty.NewSession {
		dirty.Add(stepRes.Dirty.Home, stepRes.Dirty.Project, stepRes.Dirty.NewSession)
	}
	if stepRes.Dirty.SessionData {
		dirty.MarkSessionData()
	}
}

func (c *Core) reconcileSessionExternalResolutions(ctx *sync.StepContext) {
	store := c.m.sessions
	if store == nil {
		return
	}
	for sid, ui := range store.uiBySession {
		if ui == nil || (len(ui.ApprovalDenyOpen) == 0 && len(ui.QuestionDraftByKey) == 0) {
			continue
		}
		snap := c.m.sync.SessionSnapshot(sid)
		if !snap.Found || snap.Projection == nil {
			continue
		}
		// Check approvals that had denying_open.
		for aid, open := range ui.ApprovalDenyOpen {
			if !open {
				delete(ui.ApprovalDenyOpen, aid)
				continue
			}
			stillPending := false
			for _, pa := range snap.Projection.Approvals {
				if pa != nil && pa.ApprovalId == aid {
					stillPending = true
					break
				}
			}
			if stillPending {
				continue
			}
			delete(ui.ApprovalDenyOpen, aid)
			var origin *v1.Origin
			if ra := snap.Projection.Resolved[aid]; ra != nil {
				origin = ra.Origin
			}
			itemKey := "approval:" + aid
			resOutcome := classifyResolutionOrigin(origin, snap.ThisDeviceID, ui.LocalResolvedRequestID[itemKey], ui.LocalSucceededItems[itemKey])
			if resOutcome == corev1.ResolutionOutcome_RESOLUTION_OUTCOME_UNSPECIFIED {
				resOutcome = corev1.ResolutionOutcome_RESOLUTION_OUTCOME_TERMINAL
			}
			delete(ui.LocalResolvedRequestID, itemKey)
			delete(ui.LocalSucceededItems, itemKey)
			if resOutcome == corev1.ResolutionOutcome_RESOLUTION_OUTCOME_OTHER_CLIENT ||
				resOutcome == corev1.ResolutionOutcome_RESOLUTION_OUTCOME_TERMINAL {
				ui.ExternalResolution = &corev1.SessionResolutionNoticeView{
					Outcome: resOutcome,
					Text:    externalResolutionNoticeText(resOutcome),
				}
				c.m.sync.ScheduleAppTimer(ctx, corev1.TimerKind_TIMER_KIND_UI_TRANSIENT, sessionExtTimerKeyPrefix+sid, sessionExternalNoticeHoldMs)
				store.pendingDirty = true
			}
		}

		// Check multi-select questions that had active drafted ticks.
		for qKey, draft := range ui.QuestionDraftByKey {
			if len(draft) == 0 {
				delete(ui.QuestionDraftByKey, qKey)
				continue
			}
			qID, _, _ := strings.Cut(qKey, "#")
			stillPending := false
			for _, pq := range snap.Projection.Questions {
				if pq != nil && pq.QuestionId == qID {
					stillPending = true
					break
				}
			}
			if stillPending {
				continue
			}
			delete(ui.QuestionDraftByKey, qKey)
			var origin *v1.Origin
			if aq := snap.Projection.Answered[qID]; aq != nil {
				origin = aq.Origin
			}
			itemKey := "question:" + qID
			resOutcome := classifyResolutionOrigin(origin, snap.ThisDeviceID, ui.LocalResolvedRequestID[itemKey], ui.LocalSucceededItems[itemKey])
			if resOutcome == corev1.ResolutionOutcome_RESOLUTION_OUTCOME_UNSPECIFIED {
				resOutcome = corev1.ResolutionOutcome_RESOLUTION_OUTCOME_TERMINAL
			}
			delete(ui.LocalResolvedRequestID, itemKey)
			delete(ui.LocalSucceededItems, itemKey)
			if resOutcome == corev1.ResolutionOutcome_RESOLUTION_OUTCOME_OTHER_CLIENT ||
				resOutcome == corev1.ResolutionOutcome_RESOLUTION_OUTCOME_TERMINAL {
				ui.ExternalResolution = &corev1.SessionResolutionNoticeView{
					Outcome: resOutcome,
					Text:    externalResolutionNoticeText(resOutcome),
				}
				c.m.sync.ScheduleAppTimer(ctx, corev1.TimerKind_TIMER_KIND_UI_TRANSIENT, sessionExtTimerKeyPrefix+sid, sessionExternalNoticeHoldMs)
				store.pendingDirty = true
			}
		}
	}
}

func (c *Core) findWorldSession(sid string) *lists.Session {
	for _, s := range c.m.state.World.Sessions {
		if s != nil && s.ID == sid {
			return s
		}
	}
	return nil
}

func (c *Core) reconcileHomeFocusState(ctx *sync.StepContext) {
	st := c.m.state
	// If Home is already holding a resolved confirmation bar, wait for the 900ms timer.
	if st.HomeFocus != nil &&
		(st.HomeFocus.Phase == corev1.HomeFocusPhase_HOME_FOCUS_PHASE_RESOLVED_THIS_CLIENT ||
			st.HomeFocus.Phase == corev1.HomeFocusPhase_HOME_FOCUS_PHASE_RESOLVED_ELSEWHERE) {
		return
	}

	prevKind := st.HomeLastTopKind
	prevSID := st.HomeLastTopSessionID
	prevItemID := st.HomeLastTopItemID

	if prevKind != screens.HomeFocusNone && prevSID != "" && prevItemID != "" {
		ws := c.findWorldSession(prevSID)
		stillPending := false
		if ws != nil {
			switch prevKind {
			case screens.HomeFocusApproval:
				stillPending = ws.Pending.HasApproval && ws.Pending.ApprovalID == prevItemID
			case screens.HomeFocusQuestion:
				stillPending = ws.Pending.HasQuestion && ws.Pending.Question != nil && ws.Pending.Question.QuestionID == prevItemID
			}
		}
		if ws != nil && !stillPending {
			// The previously displayed top item on Home has been resolved on this session.
			snap := c.m.sync.SessionSnapshot(prevSID)
			var ui *screens.SessionUIState
			if c.m.sessions != nil {
				ui = c.m.sessions.uiBySession[prevSID]
			}
			var origin *v1.Origin
			var itemKey string
			if prevKind == screens.HomeFocusApproval {
				itemKey = "approval:" + prevItemID
				if snap.Projection != nil && snap.Projection.Resolved[prevItemID] != nil {
					origin = snap.Projection.Resolved[prevItemID].Origin
				}
			} else {
				itemKey = "question:" + prevItemID
				if snap.Projection != nil && snap.Projection.Answered[prevItemID] != nil {
					origin = snap.Projection.Answered[prevItemID].Origin
				}
			}
			var localReqID string
			var localSucceeded bool
			if ui != nil {
				localReqID = ui.LocalResolvedRequestID[itemKey]
				localSucceeded = ui.LocalSucceededItems[itemKey]
			}
			resOutcome := classifyResolutionOrigin(origin, snap.ThisDeviceID, localReqID, localSucceeded)
			wasHomeSubmitting := st.HomeFocus != nil && st.HomeFocus.SessionID == prevSID && st.HomeFocus.ItemID == prevItemID &&
				st.HomeFocus.Phase == corev1.HomeFocusPhase_HOME_FOCUS_PHASE_SUBMITTING
			wasHomeDenying := st.HomeFocus != nil && st.HomeFocus.SessionID == prevSID && st.HomeFocus.ItemID == prevItemID &&
				st.HomeFocus.DenyingOpen

			// For Home inline question: always show confirmation / elsewhere hold bar.
			// For Home approval: show hold bar if this client was submitting or had deny open.
			shouldHold := prevKind == screens.HomeFocusQuestion || wasHomeSubmitting || wasHomeDenying
			if shouldHold {
				var phase corev1.HomeFocusPhase
				var resText *corev1.Text
				if resOutcome == corev1.ResolutionOutcome_RESOLUTION_OUTCOME_THIS_CLIENT ||
					(resOutcome == corev1.ResolutionOutcome_RESOLUTION_OUTCOME_UNSPECIFIED && wasHomeSubmitting) {
					phase = corev1.HomeFocusPhase_HOME_FOCUS_PHASE_RESOLVED_THIS_CLIENT
					if prevKind == screens.HomeFocusQuestion {
						lbl := ""
						if st.HomeFocus != nil {
							lbl = st.HomeFocus.PickedLabel
						}
						resText = text.Msg(text.KeyHomeQuestionAnsweredLine, text.Str("answer", lbl))
					} else {
						resText = text.Msg(text.KeyApprovalAllowed)
					}
				} else {
					phase = corev1.HomeFocusPhase_HOME_FOCUS_PHASE_RESOLVED_ELSEWHERE
					if prevKind == screens.HomeFocusQuestion {
						if resOutcome == corev1.ResolutionOutcome_RESOLUTION_OUTCOME_TERMINAL {
							resText = text.Msg(text.KeyApprovalHandledInTerminal)
						} else {
							resText = text.Msg(text.KeyHomeQuestionAnsweredElsewhere)
						}
					} else {
						if resOutcome == corev1.ResolutionOutcome_RESOLUTION_OUTCOME_TERMINAL {
							resText = text.Msg(text.KeyApprovalHandledInTerminal)
						} else {
							resText = text.Msg(text.KeyApprovalHandledOtherDevice)
						}
					}
				}
				var pickedNum int32
				var pickedLbl string
				if st.HomeFocus != nil && st.HomeFocus.SessionID == prevSID && st.HomeFocus.ItemID == prevItemID {
					pickedNum = st.HomeFocus.PickedOptionNum
					pickedLbl = st.HomeFocus.PickedLabel
				}
				st.HomeFocus = &screens.HomeFocusState{
					Kind:            prevKind,
					SessionID:       prevSID,
					ItemID:          prevItemID,
					Phase:           phase,
					PickedOptionNum: pickedNum,
					PickedLabel:     pickedLbl,
					ResolutionText:  resText,
					HeldApproval:    st.HomeLastTopApproval,
					HeldQuestion:    st.HomeLastTopQuestion,
				}
				st.HomeLastTopKind = screens.HomeFocusNone
				st.HomeLastTopSessionID = ""
				st.HomeLastTopItemID = ""
				st.HomeLastTopApproval = nil
				st.HomeLastTopQuestion = nil
				c.m.sync.ScheduleAppTimer(ctx, corev1.TimerKind_TIMER_KIND_UI_TRANSIENT, homeFocusHoldTimerKey, homeFocusHoldMs)
				ctx.Dirty.Add(true, false, false)
				return
			}
		}
	}

	// Update live top attention snapshot.
	kind, sid, itemID, approval, question := screens.CurrentHomeTopAttention(st)
	st.HomeLastTopKind = kind
	st.HomeLastTopSessionID = sid
	st.HomeLastTopItemID = itemID
	st.HomeLastTopApproval = approval
	st.HomeLastTopQuestion = question
	if st.HomeFocus != nil && (st.HomeFocus.Kind != kind || st.HomeFocus.SessionID != sid || st.HomeFocus.ItemID != itemID) {
		st.HomeFocus = nil
	}
}

func (c *Core) handleUITransientTimer(ctx *sync.StepContext, store *sessionStore, key string) []*corev1.Effect {
	if key == homeFocusHoldTimerKey {
		if c.m.state.HomeFocus != nil {
			c.m.state.HomeFocus = nil
			// Refresh live top snapshot now that hold finished.
			kind, sid, itemID, approval, question := screens.CurrentHomeTopAttention(c.m.state)
			c.m.state.HomeLastTopKind = kind
			c.m.state.HomeLastTopSessionID = sid
			c.m.state.HomeLastTopItemID = itemID
			c.m.state.HomeLastTopApproval = approval
			c.m.state.HomeLastTopQuestion = question
			ctx.Dirty.Add(true, false, false)
		}
		return nil
	}
	if strings.HasPrefix(key, sessionExtTimerKeyPrefix) {
		sid := strings.TrimPrefix(key, sessionExtTimerKeyPrefix)
		if ui := store.uiBySession[sid]; ui != nil && ui.ExternalResolution != nil {
			ui.ExternalResolution = nil
			store.pendingDirty = true
		}
	}
	return nil
}
