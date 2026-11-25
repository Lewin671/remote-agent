package app

import (
	"slices"
	"strconv"
	"strings"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/projection"
	"github.com/Lewin671/remote-agent/clientcore/domain/sortutil"
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
	homeFocusHoldTimerKeyPrefix = "home-focus-hold:"
	sessionExtTimerKeyPrefix    = "session-ext:"
)

func homeFocusHoldKey(kind screens.HomeFocusKind, sid, itemID string) string {
	tag := "a"
	if kind == screens.HomeFocusQuestion {
		tag = "q"
	}
	return homeFocusHoldTimerKeyPrefix + tag + ":" + sid + ":" + itemID
}

func approvalSlotKey(sid, aid string) string {
	return "approval:" + sid + ":" + aid
}

func isSlotInFlight(ui *screens.SessionUIState, slotKey string) bool {
	if ui == nil || slotKey == "" {
		return false
	}
	rec := ui.Commands[slotKey]
	return rec != nil && (rec.Busy || rec.Status == corev1.SessionCommandStatus_SESSION_COMMAND_STATUS_PENDING)
}

func sameQuestionIndex(a, b *int32) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func sameCommandPayload(a, b *v1.Command) bool {
	if a == nil || b == nil {
		return false
	}
	switch ak := a.Kind.(type) {
	case *v1.Command_ResolveApproval:
		bk := b.GetResolveApproval()
		if ak.ResolveApproval == nil || bk == nil {
			return false
		}
		return ak.ResolveApproval.ApprovalId == bk.ApprovalId &&
			ak.ResolveApproval.Allow == bk.Allow &&
			ak.ResolveApproval.Always == bk.Always &&
			(ak.ResolveApproval.Allow || ak.ResolveApproval.Message == bk.Message)
	case *v1.Command_AnswerQuestion:
		bk := b.GetAnswerQuestion()
		if ak.AnswerQuestion == nil || bk == nil {
			return false
		}
		return ak.AnswerQuestion.QuestionId == bk.QuestionId &&
			ak.AnswerQuestion.Option == bk.Option &&
			slices.Equal(ak.AnswerQuestion.Options, bk.Options) &&
			sameQuestionIndex(ak.AnswerQuestion.QuestionIndex, bk.QuestionIndex)
	case *v1.Command_AnswerPlan:
		bk := b.GetAnswerPlan()
		if ak.AnswerPlan == nil || bk == nil {
			return false
		}
		return ak.AnswerPlan.Choice == bk.Choice
	case *v1.Command_RenameSession:
		bk := b.GetRenameSession()
		if ak.RenameSession == nil || bk == nil {
			return false
		}
		return ak.RenameSession.Title == bk.Title
	case *v1.Command_SetApprovalMode:
		bk := b.GetSetApprovalMode()
		if ak.SetApprovalMode == nil || bk == nil {
			return false
		}
		return ak.SetApprovalMode.Mode == bk.Mode
	case *v1.Command_SetModel:
		bk := b.GetSetModel()
		if ak.SetModel == nil || bk == nil {
			return false
		}
		return ak.SetModel.Model == bk.Model && ak.SetModel.Effort == bk.Effort
	case *v1.Command_Compact:
		return b.GetCompact() != nil
	case *v1.Command_Interrupt:
		return b.GetInterrupt() != nil
	case *v1.Command_StopSession:
		return b.GetStopSession() != nil
	case *v1.Command_ResumeSession:
		return b.GetResumeSession() != nil
	case *v1.Command_ForkSession:
		return b.GetForkSession() != nil
	default:
		return false
	}
}

// prepareFrozenSlot reconciles a slot that holds a frozen (UNKNOWN) or expired attempt
// before submitting newCmd:
//   - If the frozen attempt is expired (non-retryable), reject new input until explicitly abandoned.
//   - If newCmd has the same payload as the frozen attempt, keep it so sync.SubmitCommand replays the same request_id.
//   - If the payload changed on a retryable frozen attempt, abandon the old attempt so newCmd gets a fresh request_id.
func (c *Core) prepareFrozenSlot(
	ctx *sync.StepContext,
	store *sessionStore,
	ui *screens.SessionUIState,
	slotKey string,
	newCmd *v1.Command,
) bool {
	syncFrozen, syncExpired, _ := c.m.sync.AttemptState(slotKey)
	prev := ui.Commands[slotKey]
	isFrozen := syncFrozen || syncExpired || (prev != nil && (prev.Frozen || prev.Expired || prev.Status == corev1.SessionCommandStatus_SESSION_COMMAND_STATUS_UNKNOWN))
	if !isFrozen {
		return true
	}
	isExpired := syncExpired || (prev != nil && prev.Expired)
	if isExpired {
		return false
	}
	if prev != nil && prev.Command != nil && sameCommandPayload(prev.Command, newCmd) {
		return true
	}
	c.m.sync.AbandonAttempt(ctx, slotKey)
	delete(ui.Commands, slotKey)
	delete(store.slotMeta, slotKey)
	return true
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

func questionOptionSummaryText(snap world.SessionSnapshot, qID string, activeIdx int32, nums []int32) *corev1.Text {
	if len(nums) == 0 {
		return nil
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
	parts := make([]*corev1.Text, 0, len(nums))
	if len(opts) == 0 && snap.Session != nil && snap.Session.Pending.Question != nil &&
		snap.Session.Pending.Question.QuestionID == qID && activeIdx == 0 {
		for _, num := range nums {
			idx := int(num - 1)
			if idx >= 0 && idx < len(snap.Session.Pending.Question.Options) {
				lbl, _ := lists.OptionLabel(snap.Session.Pending.Question.Options[idx].Label)
				lbl = strings.TrimSpace(lbl)
				if lbl != "" {
					parts = append(parts, text.Plain(lbl))
					continue
				}
			}
			parts = append(parts, text.Msg(text.KeyDockOptionNumber, text.Int("num", int64(num))))
		}
		return text.Join(text.KeyCommonListSeparator, parts...)
	}
	for _, num := range nums {
		idx := int(num - 1)
		if idx >= 0 && idx < len(opts) && opts[idx] != nil {
			lbl, _ := lists.OptionLabel(opts[idx].Label)
			lbl = strings.TrimSpace(lbl)
			if lbl != "" {
				parts = append(parts, text.Plain(lbl))
				continue
			}
		}
		parts = append(parts, text.Msg(text.KeyDockOptionNumber, text.Int("num", int64(num))))
	}
	return text.Join(text.KeyCommonListSeparator, parts...)
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

func (c *Core) handleSessionResolveApproval(ctx *sync.StepContext, store *sessionStore, act *corev1.SessionResolveApprovalAction) ([]*corev1.Effect, bool) {
	if act == nil || act.SessionId == "" || act.ApprovalId == "" {
		return nil, false
	}
	machineID, snap, ui, ok := c.resolveSessionActionTarget(store, act.SessionId)
	if !ok || isPendingApprovalSkeleton(snap, act.ApprovalId) {
		return nil, false
	}
	slotKey := "approval:" + act.SessionId + ":" + act.ApprovalId
	if isSlotInFlight(ui, slotKey) {
		return nil, false
	}
	denyReason := ""
	if !act.Allow {
		denyReason = strings.TrimSpace(act.DenyReason)
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
	if !c.prepareFrozenSlot(ctx, store, ui, slotKey, cmd) {
		return nil, false
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
	return nil, true
}

func (c *Core) handleSessionToggleQuestionOption(store *sessionStore, act *corev1.SessionToggleQuestionOptionAction) {
	if act == nil || act.SessionId == "" || act.QuestionId == "" || act.OptionNum <= 0 {
		return
	}
	ui := store.ensureUI(act.SessionId)
	if ui.QuestionCompleted[act.QuestionId] {
		return
	}
	activeIdx := ui.QuestionIndexByID[act.QuestionId]
	qKey := act.QuestionId + "#" + strconv.Itoa(int(activeIdx))
	slotKey := "question:" + act.SessionId + ":" + qKey
	if isSlotInFlight(ui, slotKey) {
		return
	}
	base := ui.QuestionDraftByKey[qKey]
	if rec := ui.Commands[slotKey]; rec != nil {
		if rec.Frozen {
			return
		}
		if rec.Status == corev1.SessionCommandStatus_SESSION_COMMAND_STATUS_FAILED {
			if len(base) == 0 && len(rec.SubmittedOptionNums) > 0 {
				base = rec.SubmittedOptionNums
			}
			delete(ui.Commands, slotKey)
			delete(store.slotMeta, slotKey)
		}
	}
	next := toggleInt32Sorted(base, act.OptionNum)
	if len(next) == 0 {
		delete(ui.QuestionDraftByKey, qKey)
	} else {
		ui.QuestionDraftByKey[qKey] = next
	}
	store.pendingDirty = true
}

func (c *Core) handleSessionAnswerQuestion(ctx *sync.StepContext, store *sessionStore, act *corev1.SessionAnswerQuestionAction, fallbackObservedSeq uint64) ([]*corev1.Effect, bool) {
	if act == nil || act.SessionId == "" || act.QuestionId == "" {
		return nil, false
	}
	machineID, snap, ui, ok := c.resolveSessionActionTarget(store, act.SessionId)
	if !ok {
		return nil, false
	}
	if ui.QuestionCompleted[act.QuestionId] {
		return nil, false
	}
	activeIdx := ui.QuestionIndexByID[act.QuestionId]
	multiSelect, _, isSkeleton := findPendingQuestionMeta(snap, act.QuestionId, activeIdx)
	if isSkeleton {
		return nil, false
	}
	qKey := act.QuestionId + "#" + strconv.Itoa(int(activeIdx))
	slotKey := "question:" + act.SessionId + ":" + qKey
	if isSlotInFlight(ui, slotKey) {
		return nil, false
	}

	var picked []int32
	if multiSelect {
		if len(act.OptionNums) > 0 {
			picked = dedupInt32Sorted(act.OptionNums)
		} else {
			picked = dedupInt32Sorted(ui.QuestionDraftByKey[qKey])
		}
		if len(picked) == 0 {
			return nil, false
		}
	} else {
		if act.OptionNum <= 0 {
			return nil, false
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
	if !c.prepareFrozenSlot(ctx, store, ui, slotKey, cmd) {
		return nil, false
	}
	fillObservedPreconditions(cmd, snap, ui)
	ui.ExternalResolution = nil
	c.submitSessionCommandDirect(ctx, store, ui, act.SessionId, machineID, slotKey, slotKindAnswerQuestion, act.QuestionId, activeIdx, "", cmd)
	if rec := ui.Commands[slotKey]; rec != nil {
		rec.SubmittedOptionNums = append([]int32(nil), picked...)
		if sum := questionOptionSummaryText(snap, act.QuestionId, activeIdx, picked); sum != nil {
			rec.SubmittedSummary = sum
		}
		if rec.RequestID != "" {
			ui.LocalResolvedRequestID["question:"+act.QuestionId] = rec.RequestID
		}
	}
	ctx.Dirty.Add(true, false, false)
	return nil, true
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
	cmd := &v1.Command{
		ProtocolVersion: sync.PeerVersion,
		Kind: &v1.Command_AnswerPlan{
			AnswerPlan: &v1.AnswerPlanRequest{
				SessionId: act.SessionId,
				Choice:    act.Choice,
			},
		},
	}
	if !c.prepareFrozenSlot(ctx, store, ui, slotKey, cmd) {
		return nil
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
	machineID, snap, ui, ok := c.resolveSessionActionTarget(store, act.SessionId)
	if !ok {
		return nil
	}
	if !screens.SessionSimpleCommandAllowed(snap, c.m.state, ui, act.Kind) {
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
	if !c.prepareFrozenSlot(ctx, store, ui, slotKey, cmd) {
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
	plainTitle, hasCustom, resolvedTitle := lists.ResolveTitle(snap.Session, c.m.state.Cal)
	normalizedPlain := strings.TrimSpace(lists.TitleInput(strings.TrimSpace(plainTitle)))
	normalizedResolved := strings.TrimSpace(lists.TitleInput(strings.TrimSpace(resolvedTitle)))
	if clean == normalizedPlain || (!hasCustom && (clean == "" || clean == normalizedResolved)) {
		syncFrozen, syncExpired, _ := c.m.sync.AttemptState(slotKey)
		prev := ui.Commands[slotKey]
		if syncExpired || (prev != nil && prev.Expired) {
			return nil
		}
		if syncFrozen || (prev != nil && prev.Frozen) {
			c.m.sync.AbandonAttempt(ctx, slotKey)
		}
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
	if !c.prepareFrozenSlot(ctx, store, ui, slotKey, cmd) {
		return nil
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
		syncFrozen, syncExpired, _ := c.m.sync.AttemptState(slotKey)
		prev := ui.Commands[slotKey]
		if syncExpired || (prev != nil && prev.Expired) {
			return nil
		}
		if syncFrozen || (prev != nil && prev.Frozen) {
			c.m.sync.AbandonAttempt(ctx, slotKey)
		}
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
	if !c.prepareFrozenSlot(ctx, store, ui, slotKey, cmd) {
		return nil
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
	if !c.prepareFrozenSlot(ctx, store, ui, slotKey, cmd) {
		return nil
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

func (c *Core) handleSessionRetryCommand(ctx *sync.StepContext, store *sessionStore, act *corev1.SessionRetryCommandAction) ([]*corev1.Effect, bool) {
	if act == nil || act.SessionId == "" || act.SlotKey == "" {
		return nil, false
	}
	machineID, snap, ui, ok := c.resolveSessionActionTarget(store, act.SessionId)
	if !ok {
		return nil, false
	}
	rec := ui.Commands[act.SlotKey]
	if rec == nil || rec.Command == nil || isSlotInFlight(ui, act.SlotKey) {
		return nil, false
	}
	if rec.Status == corev1.SessionCommandStatus_SESSION_COMMAND_STATUS_UNKNOWN && rec.Expired {
		return nil, false
	}
	if rec.Status != corev1.SessionCommandStatus_SESSION_COMMAND_STATUS_FAILED &&
		rec.Status != corev1.SessionCommandStatus_SESSION_COMMAND_STATUS_UNKNOWN {
		return nil, false
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
	return nil, true
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
	eff, submitted := c.handleSessionResolveApproval(ctx, store, &corev1.SessionResolveApprovalAction{
		SessionId:  act.SessionId,
		ApprovalId: act.ApprovalId,
		Allow:      act.Allow,
		Always:     act.Always,
		DenyReason: act.DenyReason,
	})
	if !submitted {
		return eff
	}
	ui := store.ensureUI(act.SessionId)
	summary := approvalSummaryText(act.Allow, act.Always)
	if rec := ui.Commands[approvalSlotKey(act.SessionId, act.ApprovalId)]; rec != nil && rec.SubmittedSummary != nil {
		summary = rec.SubmittedSummary
	}
	c.m.state.HomeFocus = &screens.HomeFocusState{
		Kind:             screens.HomeFocusApproval,
		SessionID:        act.SessionId,
		ItemID:           act.ApprovalId,
		Phase:            corev1.HomeFocusPhase_HOME_FOCUS_PHASE_SUBMITTING,
		SubmittedSummary: summary,
		DenyingOpen:      !act.Allow && ui.ApprovalDenyOpen[act.ApprovalId],
		HeldApproval:     held,
	}
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
	var pickedText *corev1.Text
	var observedSeq uint64
	if held != nil {
		observedSeq = held.ObservedEventSeq
		for _, opt := range held.Options {
			if opt != nil && opt.OptionNum == act.OptionNum && strings.TrimSpace(opt.Label) != "" {
				pickedText = text.Plain(strings.TrimSpace(opt.Label))
				break
			}
		}
	}
	if pickedText == nil {
		snap := c.m.sync.SessionSnapshot(act.SessionId)
		pickedText = questionOptionSummaryText(snap, act.QuestionId, 0, []int32{act.OptionNum})
	}
	eff, submitted := c.handleSessionAnswerQuestion(ctx, store, &corev1.SessionAnswerQuestionAction{
		SessionId:  act.SessionId,
		QuestionId: act.QuestionId,
		OptionNum:  act.OptionNum,
	}, observedSeq)
	if !submitted {
		return eff
	}
	c.m.state.HomeFocus = &screens.HomeFocusState{
		Kind:            screens.HomeFocusQuestion,
		SessionID:       act.SessionId,
		ItemID:          act.QuestionId,
		Phase:           corev1.HomeFocusPhase_HOME_FOCUS_PHASE_SUBMITTING,
		PickedOptionNum: act.OptionNum,
		PickedText:      pickedText,
		HeldQuestion:    held,
	}
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
	eff, submitted := c.handleSessionRetryCommand(ctx, store, &corev1.SessionRetryCommandAction{
		SessionId: act.SessionId,
		SlotKey:   slotKey,
	})
	if !submitted {
		return eff
	}
	if hf := c.m.state.HomeFocus; hf != nil && hf.SessionID == act.SessionId && hf.ItemID == act.ItemId {
		hf.Phase = corev1.HomeFocusPhase_HOME_FOCUS_PHASE_SUBMITTING
	}
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
		case v1.OriginKind_ORIGIN_KIND_LOCAL:
			return corev1.ResolutionOutcome_RESOLUTION_OUTCOME_TERMINAL
		case v1.OriginKind_ORIGIN_KIND_SYSTEM:
			return corev1.ResolutionOutcome_RESOLUTION_OUTCOME_SYSTEM
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
	case corev1.ResolutionOutcome_RESOLUTION_OUTCOME_SYSTEM:
		return text.Msg(text.KeyApprovalHandled)
	default:
		return nil
	}
}

func isExternalNoticeOutcome(outcome corev1.ResolutionOutcome) bool {
	return outcome == corev1.ResolutionOutcome_RESOLUTION_OUTCOME_OTHER_CLIENT ||
		outcome == corev1.ResolutionOutcome_RESOLUTION_OUTCOME_TERMINAL ||
		outcome == corev1.ResolutionOutcome_RESOLUTION_OUTCOME_SYSTEM
}

func sortStringsDet(ss []string) {
	sortutil.Sort(len(ss), func(i, j int) bool { return ss[i] < ss[j] }, func(i, j int) { ss[i], ss[j] = ss[j], ss[i] })
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
	if hf.Phase == corev1.HomeFocusPhase_HOME_FOCUS_PHASE_RESOLVED_THIS_CLIENT ||
		hf.Phase == corev1.HomeFocusPhase_HOME_FOCUS_PHASE_RESOLVED_ELSEWHERE {
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
		answerText := hf.PickedText
		if answerText == nil {
			answerText = summary
		}
		if answerText != nil {
			hf.ResolutionText = text.Msg(text.KeyHomeQuestionAnsweredLine, text.ArgText("answer", answerText))
		} else {
			hf.ResolutionText = text.Msg(text.KeyHomeQuestionAnsweredLine, text.Str("answer", ""))
		}
	} else {
		if summary != nil {
			hf.ResolutionText = summary
		} else if hf.SubmittedSummary != nil {
			hf.ResolutionText = hf.SubmittedSummary
		} else {
			hf.ResolutionText = text.Msg(text.KeyApprovalAllowed)
		}
	}
	stepPrev, ctx := c.m.sync.BeginStep()
	c.m.sync.ScheduleAppTimer(ctx, corev1.TimerKind_TIMER_KIND_UI_TRANSIENT, homeFocusHoldKey(hf.Kind, hf.SessionID, hf.ItemID), homeFocusHoldMs)
	stepRes := c.m.sync.EndStep(stepPrev, ctx)
	*effects = append(*effects, stepRes.Effects...)
	dirty.Add(true, false, false)
}

const (
	fnvOffset64 = uint64(14695981039346656037)
	fnvPrime64  = uint64(1099511628211)
)

func fnvMixByte(h uint64, b byte) uint64 {
	h ^= uint64(b)
	return h * fnvPrime64
}

func fnvMixUint64(h, v uint64) uint64 {
	for i := 0; i < 8; i++ {
		h ^= uint64(byte(v))
		h *= fnvPrime64
		v >>= 8
	}
	return h
}

func fnvMixString(h uint64, s string) uint64 {
	h = fnvMixUint64(h, uint64(len(s)))
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= fnvPrime64
	}
	return h
}

func fnvMixBool(h uint64, b bool) uint64 {
	if b {
		return fnvMixByte(h, 1)
	}
	return fnvMixByte(h, 0)
}

// homePendingAttentionSignature computes a zero-allocation fingerprint of the world's
// pending approvals/questions plus scope and machine reachability inputs that affect
// Home's top attention card. Unrelated session streaming deltas leave this unchanged.
func homePendingAttentionSignature(st *screens.State) (digest uint64, hasAny bool) {
	if st == nil {
		return 0, false
	}
	h := fnvOffset64
	for _, s := range st.World.Sessions {
		if s == nil {
			continue
		}
		p := s.Pending
		if !p.HasApproval && !p.HasQuestion {
			continue
		}
		hasAny = true
		h = fnvMixString(h, s.ID)
		h = fnvMixString(h, s.MachineID)
		h = fnvMixUint64(h, uint64(s.UpdatedMs))
		if s.Info != nil {
			h = fnvMixString(h, s.Info.Cwd)
			h = fnvMixUint64(h, uint64(s.Info.Status))
		}
		h = fnvMixBool(h, p.HasApproval)
		if p.HasApproval {
			h = fnvMixString(h, p.ApprovalID)
			h = fnvMixBool(h, p.ApprovalSkeleton)
			h = fnvMixString(h, p.ApprovalCommand)
		}
		h = fnvMixBool(h, p.HasQuestion)
		if p.HasQuestion && p.Question != nil {
			h = fnvMixString(h, p.Question.QuestionID)
			h = fnvMixUint64(h, uint64(p.Question.QuestionCount))
			h = fnvMixBool(h, p.Question.MultiSelect)
		}
	}
	if !hasAny {
		return 0, false
	}
	h = fnvMixBool(h, st.DisplayGrace)
	if st.Scope != nil {
		h = fnvMixBool(h, st.Scope.AllProjects)
		h = fnvMixString(h, st.Scope.Key)
	}
	if st.RememberedScope != nil {
		h = fnvMixBool(h, st.RememberedScope.AllProjects)
		h = fnvMixString(h, st.RememberedScope.Key)
	}
	for _, m := range st.World.Machines {
		if m == nil {
			continue
		}
		h = fnvMixString(h, m.ID)
		h = fnvMixBool(h, m.Online)
		h = fnvMixBool(h, m.Unlinked != nil && *m.Unlinked)
		if m.Meta != nil {
			h = fnvMixUint64(h, uint64(m.Meta.ProtocolVersion))
		}
	}
	return h, true
}

func hasPendingExternalWatchers(store *sessionStore) bool {
	if store == nil {
		return false
	}
	for _, ui := range store.uiBySession {
		if ui != nil && (len(ui.ApprovalDenyOpen) > 0 || len(ui.QuestionDraftByKey) > 0) {
			return true
		}
	}
	return false
}

func hasSessionTrackingToPrune(store *sessionStore) bool {
	if store == nil {
		return false
	}
	for _, ui := range store.uiBySession {
		if ui != nil && (len(ui.LocalResolvedRequestID) > 0 || len(ui.LocalSucceededItems) > 0 || len(ui.QuestionCompleted) > 0 || len(ui.QuestionIndexByID) > 0) {
			return true
		}
	}
	return false
}

func (c *Core) pruneAllSessionResolvedTracking() {
	store := c.m.sessions
	if store == nil {
		return
	}
	st := c.m.state
	keepSID := st.HomeLastTopSessionID
	keepItemID := st.HomeLastTopItemID
	if st.HomeFocus != nil && st.HomeFocus.SessionID != "" && st.HomeFocus.ItemID != "" {
		keepSID = st.HomeFocus.SessionID
		keepItemID = st.HomeFocus.ItemID
	}
	for sid, ui := range store.uiBySession {
		if ui == nil {
			continue
		}
		if len(ui.LocalResolvedRequestID) == 0 && len(ui.LocalSucceededItems) == 0 &&
			len(ui.QuestionCompleted) == 0 && len(ui.QuestionIndexByID) == 0 && len(ui.SlotSucceededSeq) == 0 {
			continue
		}
		snap := c.m.sync.SessionSnapshot(sid)
		pruneSessionResolvedTracking(sid, snap, ui, keepSID, keepItemID)
	}
}

func (c *Core) reconcileSessionAndHomeTransient(effects *[]*corev1.Effect, dirty *sync.DirtyFlags) {
	needsExt := hasPendingExternalWatchers(c.m.sessions)

	st := c.m.state
	needsHome := false
	var homeDigest uint64
	if st.HomeFocus == nil ||
		(st.HomeFocus.Phase != corev1.HomeFocusPhase_HOME_FOCUS_PHASE_RESOLVED_THIS_CLIENT &&
			st.HomeFocus.Phase != corev1.HomeFocusPhase_HOME_FOCUS_PHASE_RESOLVED_ELSEWHERE) {
		digest, hasAny := homePendingAttentionSignature(st)
		homeDigest = digest
		if !hasAny && st.HomeFocus == nil && st.HomeLastTopKind == screens.HomeFocusNone {
			c.m.homeFocusDigest = 0
			c.m.homeFocusDigestValid = true
		} else if !c.m.homeFocusDigestValid || digest != c.m.homeFocusDigest {
			needsHome = true
		}
	}

	if needsExt || needsHome {
		stepPrev, ctx := c.m.sync.BeginStep()
		if needsExt {
			c.reconcileSessionExternalResolutions(ctx)
		}
		if needsHome {
			c.reconcileHomeFocusState(ctx)
			c.m.homeFocusDigest = homeDigest
			c.m.homeFocusDigestValid = true
		}
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
	if hasSessionTrackingToPrune(c.m.sessions) {
		c.pruneAllSessionResolvedTracking()
	}
}

func (c *Core) reconcileSessionExternalResolutions(ctx *sync.StepContext) {
	store := c.m.sessions
	if store == nil {
		return
	}
	sids := make([]string, 0, len(store.uiBySession))
	for sid := range store.uiBySession {
		sids = append(sids, sid)
	}
	sortStringsDet(sids)
	for _, sid := range sids {
		ui := store.uiBySession[sid]
		if ui == nil || (len(ui.ApprovalDenyOpen) == 0 && len(ui.QuestionDraftByKey) == 0) {
			continue
		}
		snap := c.m.sync.SessionSnapshot(sid)
		if !snap.Found || snap.Projection == nil {
			continue
		}
		// Check approvals that had denying_open (in deterministic order).
		aids := make([]string, 0, len(ui.ApprovalDenyOpen))
		for aid := range ui.ApprovalDenyOpen {
			aids = append(aids, aid)
		}
		sortStringsDet(aids)
		for _, aid := range aids {
			open := ui.ApprovalDenyOpen[aid]
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
			delete(ui.LocalResolvedRequestID, itemKey)
			delete(ui.LocalSucceededItems, itemKey)
			if isExternalNoticeOutcome(resOutcome) {
				ui.ExternalResolution = &corev1.SessionResolutionNoticeView{
					Outcome: resOutcome,
					Text:    externalResolutionNoticeText(resOutcome),
				}
				c.m.sync.ScheduleAppTimer(ctx, corev1.TimerKind_TIMER_KIND_UI_TRANSIENT, sessionExtTimerKeyPrefix+sid, sessionExternalNoticeHoldMs)
				store.pendingDirty = true
			}
		}

		// Check multi-select questions that had active drafted ticks (in deterministic order).
		qKeys := make([]string, 0, len(ui.QuestionDraftByKey))
		for qKey := range ui.QuestionDraftByKey {
			qKeys = append(qKeys, qKey)
		}
		sortStringsDet(qKeys)
		for _, qKey := range qKeys {
			draft := ui.QuestionDraftByKey[qKey]
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
			delete(ui.LocalResolvedRequestID, itemKey)
			delete(ui.LocalSucceededItems, itemKey)
			if isExternalNoticeOutcome(resOutcome) {
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
			// For Home approval: show hold bar if this client was submitting, or had deny open with a known external origin.
			isThisClient := resOutcome == corev1.ResolutionOutcome_RESOLUTION_OUTCOME_THIS_CLIENT ||
				(resOutcome == corev1.ResolutionOutcome_RESOLUTION_OUTCOME_UNSPECIFIED && wasHomeSubmitting)
			shouldHold := prevKind == screens.HomeFocusQuestion || wasHomeSubmitting || (wasHomeDenying && isExternalNoticeOutcome(resOutcome))
			if shouldHold {
				var phase corev1.HomeFocusPhase
				var resText *corev1.Text
				if isThisClient {
					phase = corev1.HomeFocusPhase_HOME_FOCUS_PHASE_RESOLVED_THIS_CLIENT
					if prevKind == screens.HomeFocusQuestion {
						var picked *corev1.Text
						if st.HomeFocus != nil {
							picked = st.HomeFocus.PickedText
						}
						if picked != nil {
							resText = text.Msg(text.KeyHomeQuestionAnsweredLine, text.ArgText("answer", picked))
						} else {
							resText = text.Msg(text.KeyHomeQuestionAnsweredLine, text.Str("answer", ""))
						}
					} else {
						if st.HomeFocus != nil && st.HomeFocus.SessionID == prevSID && st.HomeFocus.ItemID == prevItemID && st.HomeFocus.SubmittedSummary != nil {
							resText = st.HomeFocus.SubmittedSummary
						} else if ui != nil {
							if rec := ui.Commands[approvalSlotKey(prevSID, prevItemID)]; rec != nil && rec.SubmittedSummary != nil {
								resText = rec.SubmittedSummary
							}
						}
						if resText == nil {
							resText = text.Msg(text.KeyApprovalAllowed)
						}
					}
				} else {
					phase = corev1.HomeFocusPhase_HOME_FOCUS_PHASE_RESOLVED_ELSEWHERE
					switch resOutcome {
					case corev1.ResolutionOutcome_RESOLUTION_OUTCOME_TERMINAL:
						resText = text.Msg(text.KeyApprovalHandledInTerminal)
					case corev1.ResolutionOutcome_RESOLUTION_OUTCOME_SYSTEM:
						resText = text.Msg(text.KeyApprovalHandled)
					default:
						if prevKind == screens.HomeFocusQuestion {
							resText = text.Msg(text.KeyHomeQuestionAnsweredElsewhere)
						} else {
							resText = text.Msg(text.KeyApprovalHandledOtherDevice)
						}
					}
				}
				var pickedNum int32
				var pickedTxt *corev1.Text
				var submittedSummary *corev1.Text
				if st.HomeFocus != nil && st.HomeFocus.SessionID == prevSID && st.HomeFocus.ItemID == prevItemID {
					pickedNum = st.HomeFocus.PickedOptionNum
					pickedTxt = st.HomeFocus.PickedText
					submittedSummary = st.HomeFocus.SubmittedSummary
				}
				st.HomeFocus = &screens.HomeFocusState{
					Kind:             prevKind,
					SessionID:        prevSID,
					ItemID:           prevItemID,
					Phase:            phase,
					PickedOptionNum:  pickedNum,
					PickedText:       pickedTxt,
					SubmittedSummary: submittedSummary,
					ResolutionText:   resText,
					HeldApproval:     st.HomeLastTopApproval,
					HeldQuestion:     st.HomeLastTopQuestion,
				}
				st.HomeLastTopKind = screens.HomeFocusNone
				st.HomeLastTopSessionID = ""
				st.HomeLastTopItemID = ""
				st.HomeLastTopApproval = nil
				st.HomeLastTopQuestion = nil
				c.m.sync.ScheduleAppTimer(ctx, corev1.TimerKind_TIMER_KIND_UI_TRANSIENT, homeFocusHoldKey(prevKind, prevSID, prevItemID), homeFocusHoldMs)
				ctx.Dirty.Add(true, false, false)
				return
			}
		}
	}

	// Update live top attention snapshot.
	c.m.homeFocusReconcileCount++
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
	if key == notifTapTimerKey {
		return c.handleNotifTapTimeout()
	}
	if strings.HasPrefix(key, homeFocusHoldTimerKeyPrefix) {
		hf := c.m.state.HomeFocus
		if hf == nil {
			return nil
		}
		if key != homeFocusHoldKey(hf.Kind, hf.SessionID, hf.ItemID) {
			return nil
		}
		if hf.Phase != corev1.HomeFocusPhase_HOME_FOCUS_PHASE_RESOLVED_THIS_CLIENT &&
			hf.Phase != corev1.HomeFocusPhase_HOME_FOCUS_PHASE_RESOLVED_ELSEWHERE {
			return nil
		}
		c.m.state.HomeFocus = nil
		// Refresh live top snapshot now that hold finished.
		c.m.homeFocusReconcileCount++
		kind, sid, itemID, approval, question := screens.CurrentHomeTopAttention(c.m.state)
		c.m.state.HomeLastTopKind = kind
		c.m.state.HomeLastTopSessionID = sid
		c.m.state.HomeLastTopItemID = itemID
		c.m.state.HomeLastTopApproval = approval
		c.m.state.HomeLastTopQuestion = question
		c.m.homeFocusDigest, _ = homePendingAttentionSignature(c.m.state)
		c.m.homeFocusDigestValid = true
		ctx.Dirty.Add(true, false, false)
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
