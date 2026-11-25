package app

import (
	"bytes"
	"slices"
	"strconv"

	"github.com/Lewin671/remote-agent/clientcore/domain/images"
	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/outcome"
	"github.com/Lewin671/remote-agent/clientcore/domain/world"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/screens"
	"github.com/Lewin671/remote-agent/clientcore/sync"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

type sessionSlotKind uint8

const (
	slotKindGeneric sessionSlotKind = iota
	slotKindSend
	slotKindResolveApproval
	slotKindAnswerQuestion
	slotKindAnswerPlan
	slotKindFork
	slotKindCancelInput
	slotKindResumeInput
)

type sessionSlotMeta struct {
	_             [0]func()
	sessionID     string
	machineID     string
	kind          sessionSlotKind
	approvalID    string
	outgoingReqID string
	questionID    string
	questionIndex int32
}

type pendingPlanPreflight struct {
	sessionID string
	machineID string
	slotKey   string
	command   *v1.Command
	shownSeq  uint64
	shownHash []byte
}

type sessionStore struct {
	uiBySession           map[string]*screens.SessionUIState
	opened                map[string]bool
	slotsByReqID          map[string]string
	slotMeta              map[string]sessionSlotMeta
	pendingPlanPreflights map[string]*pendingPlanPreflight
	pendingCreatePrompts  map[string]*screens.PendingStartPrompt
	pendingDirty          bool
}

func newSessionStore() *sessionStore {
	return &sessionStore{
		uiBySession:           make(map[string]*screens.SessionUIState),
		opened:                make(map[string]bool),
		slotsByReqID:          make(map[string]string),
		slotMeta:              make(map[string]sessionSlotMeta),
		pendingPlanPreflights: make(map[string]*pendingPlanPreflight),
		pendingCreatePrompts:  make(map[string]*screens.PendingStartPrompt),
	}
}

func (m *Model) ensureSessionStore() *sessionStore {
	if m.sessions == nil {
		m.sessions = newSessionStore()
	}
	return m.sessions
}

func (s *sessionStore) ensureUI(sid string) *screens.SessionUIState {
	ui := s.uiBySession[sid]
	if ui == nil {
		ui = screens.NewSessionUIState()
		s.uiBySession[sid] = ui
	}
	return ui
}

func (c *Core) viewSession(req *corev1.SessionScreenRequest) (*corev1.ViewModel, error) {
	var sid string
	if req != nil {
		sid = req.SessionId
	}
	store := c.m.ensureSessionStore()
	snap := c.m.sync.SessionSnapshot(sid)
	ui := store.ensureUI(sid)
	reconcileSessionSnapshot(snap, ui)
	vm := screens.BuildSessionView(snap, c.m.state, req, ui)
	return &corev1.ViewModel{
		View: &corev1.ViewModel_Session{
			Session: vm,
		},
	}, nil
}

func reconcileSessionSnapshot(snap world.SessionSnapshot, ui *screens.SessionUIState) {
	if ui == nil {
		return
	}
	if snap.Checkpoint != nil {
		for _, a := range snap.Checkpoint.Actions {
			if a == nil {
				continue
			}
			if ap := a.GetApprovalRequested(); ap != nil && ap.ApprovalId != "" {
				ui.LastApprovalSeq[ap.ApprovalId] = a.Seq
			}
			if q := a.GetQuestionAsked(); q != nil && q.QuestionId != "" {
				ui.LastQuestionSeq[q.QuestionId] = a.Seq
			}
		}
		if snap.Checkpoint.PlanEventSeq > 0 {
			ui.LastPlanSeq = snap.Checkpoint.PlanEventSeq
		}
		if len(snap.Checkpoint.PlanScreenHash) > 0 {
			ui.LastPlanHash = slices.Clone(snap.Checkpoint.PlanScreenHash)
		}
	}

	// Reconcile pendingCreatePrompt and outgoing sends against recorded events and projection inputs.
	userMsgReqs := make(map[string]bool)
	recordedReqs := make(map[string]bool)
	activeInputReqs := make(map[string]bool)
	hasUserMsg := false
	for _, ev := range snap.Events {
		if ev == nil {
			continue
		}
		if um := ev.GetUserMessage(); um != nil {
			hasUserMsg = true
			if um.RequestId != "" {
				recordedReqs[um.RequestId] = true
				userMsgReqs[um.RequestId] = true
			}
		}
		if is := ev.GetInputState(); is != nil && is.RequestId != "" {
			recordedReqs[is.RequestId] = true
		}
	}
	if snap.Projection != nil {
		for _, pi := range snap.Projection.PendingInputs {
			if pi != nil && pi.RequestId != "" {
				activeInputReqs[pi.RequestId] = true
			}
		}
		for _, ip := range snap.Projection.Inputs {
			if ip == nil || ip.RequestId == "" {
				continue
			}
			recordedReqs[ip.RequestId] = true
			if !ui.DismissedInputIDs[ip.RequestId] {
				switch ip.Status {
				case v1.InputStatus_INPUT_STATUS_FAILED,
					v1.InputStatus_INPUT_STATUS_NEEDS_TERMINAL,
					v1.InputStatus_INPUT_STATUS_UNCONFIRMED:
					activeInputReqs[ip.RequestId] = true
				}
			}
		}
	}
	if ui.PendingStartPrompt != nil {
		if hasUserMsg {
			ui.PendingStartPrompt = nil
		} else if startInput := screens.FindStartPromptInput(snap.Projection, ui); startInput != nil {
			switch startInput.Status {
			case v1.InputStatus_INPUT_STATUS_CANCELED,
				v1.InputStatus_INPUT_STATUS_FAILED,
				v1.InputStatus_INPUT_STATUS_NEEDS_TERMINAL,
				v1.InputStatus_INPUT_STATUS_UNCONFIRMED:
				ui.PendingStartPrompt = nil
			}
		}
	}
	if len(ui.Outgoing) > 0 && len(recordedReqs) > 0 {
		kept := ui.Outgoing[:0]
		for _, og := range ui.Outgoing {
			if og == nil {
				continue
			}
			if og.State == screens.OutgoingSending || og.State == screens.OutgoingSent {
				if recordedReqs[og.RequestID] {
					continue
				}
			} else if recordedReqs[og.RequestID] && og.State == screens.OutgoingUnknown {
				// Once observed in events or InputProjection, clear unknown local duplicate.
				continue
			}
			kept = append(kept, og)
		}
		ui.Outgoing = kept
	}
	for reqID := range userMsgReqs {
		delete(ui.SteerVisibleAfterMs, reqID)
	}
	pruneSessionLocalContents(ui, userMsgReqs, activeInputReqs)
}

func pruneSessionLocalContents(ui *screens.SessionUIState, userMsgReqs, activeInputReqs map[string]bool) {
	if ui == nil {
		return
	}
	keep := make(map[string]bool, len(activeInputReqs))
	for reqID := range activeInputReqs {
		keep[reqID] = true
	}
	for _, og := range ui.Outgoing {
		if og != nil && !og.Restored && og.RequestID != "" {
			keep[og.RequestID] = true
		}
	}
	for _, intent := range ui.CancelIntents {
		if intent != nil && intent.SourceReqID != "" {
			keep[intent.SourceReqID] = true
		}
	}
	if ui.Undo != nil {
		for _, it := range ui.Undo.Items {
			if it != nil && it.SourceReqID != "" {
				keep[it.SourceReqID] = true
			}
		}
	}
	for reqID := range ui.LocalContents {
		if userMsgReqs[reqID] && !keep[reqID] {
			delete(ui.LocalContents, reqID)
			continue
		}
		if !keep[reqID] {
			delete(ui.LocalContents, reqID)
		}
	}
}

func hasOutgoingReq(ui *screens.SessionUIState, reqID string) bool {
	for _, og := range ui.Outgoing {
		if og != nil && og.RequestID == reqID {
			return true
		}
	}
	return false
}

func (c *Core) noteCreateSessionSubmitted(projectKey string, req *v1.CreateSessionRequest) {
	if req == nil || projectKey == "" {
		return
	}
	store := c.m.ensureSessionStore()
	store.pendingCreatePrompts[projectKey] = &screens.PendingStartPrompt{
		Text:       req.Prompt,
		ImageCount: int32(len(req.ImageIds)),
		FileCount:  int32(len(req.FileIds)),
		CreatedMs:  c.m.sync.NowMs(),
	}
}

func (c *Core) processSessionSettled(
	sc sync.SettledCommand,
	effects *[]*corev1.Effect,
	dirty *sync.DirtyFlags,
) {
	store := c.m.ensureSessionStore()

	// Capture startup prompt onto newly created session.
	if len(sc.SlotKey) > len("create_session:") && sc.SlotKey[:len("create_session:")] == "create_session:" {
		projKey := sc.SlotKey[len("create_session:"):]
		if sc.Category == outcome.Executed && sc.ErrorCode == "" && sc.Result != nil && sc.Result.GetCreateSession() != nil {
			if sess := sc.Result.GetCreateSession().GetSession(); sess != nil && sess.Id != "" {
				if psp := store.pendingCreatePrompts[projKey]; psp != nil && psp.Text != "" {
					ui := store.ensureUI(sess.Id)
					cp := *psp
					cp.RequestID = sc.RequestID
					ui.PendingStartPrompt = &cp
					delete(store.pendingCreatePrompts, projKey)
				}
			}
		}
		return
	}

	slotKey := sc.SlotKey
	if slotKey == "" {
		slotKey = store.slotsByReqID[sc.RequestID]
	}
	if slotKey == "" {
		return
	}
	meta, tracked := store.slotMeta[slotKey]
	if !tracked {
		return
	}
	delete(store.slotsByReqID, sc.RequestID)

	ui := store.ensureUI(meta.sessionID)
	rec := ui.Commands[slotKey]
	if rec == nil {
		rec = &screens.SessionCommandRecord{SlotKey: slotKey, RequestID: sc.RequestID}
		ui.Commands[slotKey] = rec
	}
	rec.Busy = false

	switch sc.Category {
	case outcome.Executed:
		if sc.ErrorCode != "" {
			rec.Status = corev1.SessionCommandStatus_SESSION_COMMAND_STATUS_FAILED
			rec.Frozen = false
			rec.Expired = false
			rec.ErrorCode = sc.ErrorCode
			rec.ErrorText = screens.ReasonText(sc.ErrorReason)
			c.notifyHomeAttentionSettled(meta, false, nil, effects, dirty)
		} else {
			summary := rec.SubmittedSummary
			ui.SlotSucceededSeq[slotKey]++
			delete(ui.Commands, slotKey)
			delete(store.slotMeta, slotKey)
			if meta.kind == slotKindResolveApproval && meta.approvalID != "" {
				ui.LocalSucceededItems["approval:"+meta.approvalID] = true
				delete(ui.ApprovalDenyOpen, meta.approvalID)
			}
			if meta.kind == slotKindAnswerQuestion && meta.questionID != "" {
				qKey := meta.questionID + "#" + strconv.Itoa(int(meta.questionIndex))
				delete(ui.QuestionDraftByKey, qKey)
				ui.QuestionIndexByID[meta.questionID] = meta.questionIndex + 1
				ui.LocalSucceededItems["question:"+meta.questionID] = true
			}
			c.notifyHomeAttentionSettled(meta, true, summary, effects, dirty)
			if meta.kind == slotKindFork && sc.Result != nil && sc.Result.GetForkSession() != nil {
				if ns := sc.Result.GetForkSession().GetSession(); ns != nil && ns.Id != "" {
					seedPrev, seedCtx := c.m.sync.BeginStep()
					c.m.sync.SeedSession(seedCtx, meta.machineID, ns)
					seedRes := c.m.sync.EndStep(seedPrev, seedCtx)
					*effects = append(*effects, seedRes.Effects...)
					*effects = append(*effects, &corev1.Effect{
						Effect: &corev1.Effect_Navigate{
							Navigate: &corev1.NavigateEffect{
								Route:      corev1.NavigateRoute_NAVIGATE_ROUTE_SESSION,
								SessionId:  ns.Id,
								MachineId:  meta.machineID,
								ProjectKey: ns.Cwd,
							},
						},
					})
					dirty.Add(true, true, false)
				}
			}
		}
	case outcome.NotSent, outcome.Rejected, outcome.Conflict:
		rec.Status = corev1.SessionCommandStatus_SESSION_COMMAND_STATUS_FAILED
		rec.Frozen = false
		rec.Expired = false
		rec.ErrorCode = sc.ErrorCode
		rec.ErrorText = screens.ReasonText(sc.ErrorReason)
		c.notifyHomeAttentionSettled(meta, false, nil, effects, dirty)
	case outcome.Unknown:
		rec.Status = corev1.SessionCommandStatus_SESSION_COMMAND_STATUS_UNKNOWN
		rec.Frozen = sc.Frozen
		rec.Expired = sc.Expired
		rec.ErrorCode = sc.ErrorCode
		rec.ErrorText = screens.ReasonText(sc.ErrorReason)
		c.notifyHomeAttentionSettled(meta, false, nil, effects, dirty)
	}

	if meta.kind == slotKindSend && meta.outgoingReqID != "" {
		store.settleOutgoing(ui, meta.outgoingReqID, sc)
	}
	if meta.kind == slotKindCancelInput && meta.outgoingReqID != "" {
		c.settleCancelInput(store, ui, meta, sc, effects, dirty)
	}
	store.pendingDirty = true
}

func (c *Core) submitSessionCommandDirect(
	ctx *sync.StepContext,
	store *sessionStore,
	ui *screens.SessionUIState,
	sid, machineID, slotKey string,
	kind sessionSlotKind,
	qID string,
	qIdx int32,
	outReqID string,
	cmd *v1.Command,
) {
	reqID, dispatched := c.m.sync.SubmitCommand(ctx, sync.SubmitCommandParams{
		SlotKey:   slotKey,
		MachineID: machineID,
		SessionID: sid,
		Command:   cmd,
	})

	var approvalID string
	if ra := cmd.GetResolveApproval(); ra != nil {
		approvalID = ra.ApprovalId
	}
	store.slotMeta[slotKey] = sessionSlotMeta{
		sessionID:     sid,
		machineID:     machineID,
		kind:          kind,
		approvalID:    approvalID,
		outgoingReqID: outReqID,
		questionID:    qID,
		questionIndex: qIdx,
	}
	if reqID != "" {
		store.slotsByReqID[reqID] = slotKey
	}

	rec := ui.Commands[slotKey]
	if rec == nil {
		rec = &screens.SessionCommandRecord{SlotKey: slotKey}
		ui.Commands[slotKey] = rec
	}
	rec.RequestID = reqID
	rec.Command = cmd
	if rec.SubmittedAtMs == 0 {
		rec.SubmittedAtMs = c.m.sync.NowMs()
	}
	if dispatched {
		rec.Status = corev1.SessionCommandStatus_SESSION_COMMAND_STATUS_PENDING
		rec.Busy = true
		rec.ErrorCode = ""
		rec.ErrorText = nil
	}

	if kind == slotKindSend && cmd.GetSend() != nil && outReqID != "" {
		sr := cmd.GetSend()
		ui.LocalContents[outReqID] = &screens.LocalSentContent{
			RequestID: outReqID,
			Text:      sr.Text,
			ImageIDs:  append([]string(nil), sr.ImageIds...),
			FileIDs:   append([]string(nil), sr.FileIds...),
			Mode:      sr.Mode,
		}
		og := findOrAppendOutgoing(ui, outReqID, slotKey, sr, c.m.sync.NowMs())
		if dispatched {
			og.State = screens.OutgoingSending
			og.ErrorCode = ""
			og.ErrorText = nil
		}
		c.maybeStartCodexSteerGrace(ctx, ui, og, sid)
	}
	store.pendingDirty = true
}

func setPlanPreflightFailed(rec *screens.SessionCommandRecord, code string, errText *corev1.Text) {
	rec.Busy = false
	rec.Status = corev1.SessionCommandStatus_SESSION_COMMAND_STATUS_FAILED
	rec.Frozen = false
	rec.Expired = false
	rec.ErrorCode = code
	rec.ErrorText = errText
}

func (c *Core) submitPlanPreflight(
	ctx *sync.StepContext,
	store *sessionStore,
	ui *screens.SessionUIState,
	sid, machineID, slotKey string,
	cmd *v1.Command,
) {
	rec := ui.Commands[slotKey]
	if rec == nil {
		rec = &screens.SessionCommandRecord{SlotKey: slotKey}
		ui.Commands[slotKey] = rec
	}
	rec.Command = cmd
	if rec.SubmittedAtMs == 0 {
		rec.SubmittedAtMs = c.m.sync.NowMs()
	}

	ap := cmd.GetAnswerPlan()
	if ap == nil || ap.ObservedEventSeq == 0 || len(ap.ObservedScreenHash) == 0 {
		setPlanPreflightFailed(rec, "failed_precondition", text.Msg(text.KeyErrorsThePlanDisplayChangedReviewItAgain))
		store.pendingDirty = true
		return
	}

	liveCmd := &v1.Command{
		ProtocolVersion: sync.PeerVersion,
		Kind: &v1.Command_GetLiveSnapshot{
			GetLiveSnapshot: &v1.GetLiveSnapshotRequest{SessionId: sid},
		},
	}
	liveReqID, dispatched := c.m.sync.SubmitCommand(ctx, sync.SubmitCommandParams{
		SlotKey:   "",
		MachineID: machineID,
		SessionID: sid,
		Command:   liveCmd,
	})
	if !dispatched || liveReqID == "" {
		setPlanPreflightFailed(rec, "failed_precondition", nil)
		store.pendingDirty = true
		return
	}

	for reqID, prev := range store.pendingPlanPreflights {
		if prev != nil && prev.slotKey == slotKey {
			delete(store.pendingPlanPreflights, reqID)
		}
	}
	store.pendingPlanPreflights[liveReqID] = &pendingPlanPreflight{
		sessionID: sid,
		machineID: machineID,
		slotKey:   slotKey,
		command:   cmd.Clone(),
		shownSeq:  ap.ObservedEventSeq,
		shownHash: slices.Clone(ap.ObservedScreenHash),
	}
	rec.RequestID = liveReqID
	rec.Status = corev1.SessionCommandStatus_SESSION_COMMAND_STATUS_PENDING
	rec.Busy = true
	rec.Frozen = false
	rec.Expired = false
	rec.ErrorCode = ""
	rec.ErrorText = nil
	store.pendingDirty = true
}

func (c *Core) settlePlanPreflight(
	sc sync.SettledCommand,
	effects *[]*corev1.Effect,
	dirty *sync.DirtyFlags,
) bool {
	store := c.m.ensureSessionStore()
	pf, ok := store.pendingPlanPreflights[sc.RequestID]
	if !ok || pf == nil {
		return false
	}
	delete(store.pendingPlanPreflights, sc.RequestID)

	stepPrev, ctx := c.m.sync.BeginStep()
	ui := store.ensureUI(pf.sessionID)
	rec := ui.Commands[pf.slotKey]
	if rec == nil {
		rec = &screens.SessionCommandRecord{SlotKey: pf.slotKey}
		ui.Commands[pf.slotKey] = rec
	}

	var fresh *v1.LiveSnapshot
	if sc.Category == outcome.Executed && sc.ErrorCode == "" && sc.Result != nil {
		fresh = sc.Result.GetLiveSnapshot()
	}
	if fresh == nil {
		errTxt := screens.ReasonText(sc.ErrorReason)
		code := sc.ErrorCode
		if code == "" && errTxt == nil {
			code = "failed_precondition"
		}
		setPlanPreflightFailed(rec, code, errTxt)
		store.pendingDirty = true
		ctx.Dirty.Add(true, true, false)
		ctx.Dirty.MarkSessionData()
	} else {
		c.m.sync.ApplyLiveSnapshot(ctx, pf.sessionID, fresh)
		snap := c.m.sync.SessionSnapshot(pf.sessionID)
		reconcileSessionSnapshot(snap, ui)

		planOpen := fresh.GetSession() == nil || fresh.GetSession().GetPlanOpen()
		matches := planOpen &&
			fresh.PlanEventSeq > 0 &&
			fresh.PlanEventSeq == pf.shownSeq &&
			len(fresh.PlanScreenHash) == 32 &&
			bytes.Equal(fresh.PlanScreenHash, pf.shownHash)
		if !matches {
			setPlanPreflightFailed(rec, "failed_precondition", text.Msg(text.KeyErrorsThePlanDisplayChangedReviewItAgain))
			store.pendingDirty = true
		} else {
			cmd := pf.command.Clone()
			if ap := cmd.GetAnswerPlan(); ap != nil {
				ap.ObservedEventSeq = fresh.PlanEventSeq
				ap.ObservedScreenHash = slices.Clone(fresh.PlanScreenHash)
			}
			c.submitSessionCommandDirect(ctx, store, ui, pf.sessionID, pf.machineID, pf.slotKey, slotKindAnswerPlan, "", 0, "", cmd)
		}
	}
	stepRes := c.m.sync.EndStep(stepPrev, ctx)

	*effects = append(*effects, stepRes.Effects...)
	dirty.Add(stepRes.Dirty.Home, stepRes.Dirty.Project, stepRes.Dirty.NewSession)
	if stepRes.Dirty.SessionData {
		dirty.MarkSessionData()
	}
	for _, nested := range stepRes.Settled {
		c.processSessionSettled(nested, effects, dirty)
	}
	return true
}

func (s *sessionStore) settleOutgoing(ui *screens.SessionUIState, reqID string, sc sync.SettledCommand) {
	for _, og := range ui.Outgoing {
		if og == nil || og.RequestID != reqID {
			continue
		}
		switch sc.Category {
		case outcome.Executed:
			if sc.ErrorCode != "" {
				og.State = screens.OutgoingFailed
				og.Frozen = false
				og.Expired = false
				og.ErrorCode = sc.ErrorCode
				og.ErrorText = screens.ReasonText(sc.ErrorReason)
			} else {
				og.State = screens.OutgoingSent
				og.Frozen = false
				og.Expired = false
				og.ErrorCode = ""
				og.ErrorText = nil
			}
		case outcome.NotSent, outcome.Rejected, outcome.Conflict:
			og.State = screens.OutgoingFailed
			og.Frozen = false
			og.Expired = false
			og.ErrorCode = sc.ErrorCode
			og.ErrorText = screens.ReasonText(sc.ErrorReason)
		case outcome.Unknown:
			og.State = screens.OutgoingUnknown
			og.Frozen = sc.Frozen
			og.Expired = sc.Expired
			og.ErrorCode = sc.ErrorCode
			og.ErrorText = screens.ReasonText(sc.ErrorReason)
		}
		return
	}
}

func (c *Core) dispatchSessionUI(ctx *sync.StepContext, ev *corev1.Event) []*corev1.Effect {
	store := c.m.ensureSessionStore()
	switch k := ev.Event.(type) {
	case *corev1.Event_SessionOpen:
		if k.SessionOpen == nil || k.SessionOpen.SessionId == "" {
			return nil
		}
		sid := k.SessionOpen.SessionId
		store.opened[sid] = true
		c.m.sync.LookupSessionIfMissing(ctx, sid)
		if sess := c.m.sync.SessionEntry(sid); sess != nil && sess.MachineID != "" {
			agent := lists.DefaultAgent
			if sess.Info != nil && sess.Info.Agent != v1.Agent_AGENT_UNSPECIFIED {
				agent = sess.Info.Agent
			}
			if k.SessionOpen.RefreshModels {
				delete(c.m.state.MachineModels, screens.MachineAgentKey{MachineID: sess.MachineID, Agent: agent})
			}
			c.m.maybeFetchMachineConfig(ctx, sess.MachineID, agent)
			c.maybeFetchUsage(ctx, sess.MachineID, v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS, nil)
		}
		store.pendingDirty = true
		return nil

	case *corev1.Event_SessionRetryLoad:
		if k.SessionRetryLoad == nil || k.SessionRetryLoad.SessionId == "" {
			return nil
		}
		sid := k.SessionRetryLoad.SessionId
		store.opened[sid] = true
		if sess := c.m.sync.SessionEntry(sid); sess == nil {
			c.m.sync.LookupSessionIfMissing(ctx, sid)
		} else {
			c.m.sync.RetrySessionHistory(ctx, sid)
			if sess.MachineID != "" {
				agent := lists.DefaultAgent
				if sess.Info != nil && sess.Info.Agent != v1.Agent_AGENT_UNSPECIFIED {
					agent = sess.Info.Agent
				}
				c.m.maybeFetchMachineConfig(ctx, sess.MachineID, agent)
				c.maybeFetchUsage(ctx, sess.MachineID, v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS, nil)
			}
		}
		store.pendingDirty = true
		return nil

	case *corev1.Event_SessionImageViewerOpen:
		if k.SessionImageViewerOpen == nil || k.SessionImageViewerOpen.SessionId == "" {
			return nil
		}
		act := k.SessionImageViewerOpen
		ui := store.ensureUI(act.SessionId)
		ui.ViewerOpen = true
		if act.Seq > 0 {
			ui.ViewerUseSeq = true
			ui.ViewerSeq = act.Seq
			ui.ViewerEventIndex = act.ImageIndex
			ui.ViewerIndex = 0
		} else {
			ui.ViewerUseSeq = false
			ui.ViewerSeq = 0
			ui.ViewerEventIndex = 0
			ui.ViewerIndex = act.ViewerIndex
		}
		store.pendingDirty = true
		return nil

	case *corev1.Event_SessionImageViewerNavigate:
		if k.SessionImageViewerNavigate == nil || k.SessionImageViewerNavigate.SessionId == "" {
			return nil
		}
		act := k.SessionImageViewerNavigate
		ui := store.ensureUI(act.SessionId)
		anchors := screens.CollectSessionViewerAnchors(c.m.sync.SessionSnapshot(act.SessionId))
		total := int32(len(anchors))
		if total == 0 {
			return nil
		}
		var curIdx int32
		if ui.ViewerUseSeq {
			curIdx = 0
			for i, a := range anchors {
				if a.Seq == ui.ViewerSeq {
					curIdx = images.ClampViewerIndex(int32(i)+ui.ViewerEventIndex, total)
					break
				}
			}
		} else {
			curIdx = images.ClampViewerIndex(ui.ViewerIndex, total)
		}
		var nextIdx int32
		if act.SetIndex {
			nextIdx = images.ClampViewerIndex(act.Index, total)
		} else {
			nextIdx = images.StepViewerIndex(curIdx, act.Delta, total)
		}
		target := anchors[nextIdx]
		ui.ViewerOpen = true
		ui.ViewerUseSeq = true
		ui.ViewerSeq = target.Seq
		ui.ViewerEventIndex = target.EventImageIndex
		ui.ViewerIndex = nextIdx
		store.pendingDirty = true
		return nil

	case *corev1.Event_SessionImageViewerClose:
		if k.SessionImageViewerClose == nil || k.SessionImageViewerClose.SessionId == "" {
			return nil
		}
		ui := store.ensureUI(k.SessionImageViewerClose.SessionId)
		if ui.ViewerOpen {
			ui.ViewerOpen = false
			ui.ViewerUseSeq = false
			ui.ViewerSeq = 0
			ui.ViewerEventIndex = 0
			ui.ViewerIndex = 0
			store.pendingDirty = true
		}
		return nil

	case *corev1.Event_SessionSubmitCommand:
		if k.SessionSubmitCommand == nil || k.SessionSubmitCommand.Command == nil {
			return nil
		}
		act := k.SessionSubmitCommand
		cmd := act.Command.Clone()
		sid := act.SessionId
		if sid == "" {
			sid = sessionIDFromCommand(cmd)
		}
		machineID := act.MachineId
		if machineID == "" && sid != "" {
			if se := c.m.sync.SessionEntry(sid); se != nil {
				machineID = se.MachineID
			}
		}

		ui := store.ensureUI(sid)
		snap := c.m.sync.SessionSnapshot(sid)
		reconcileSessionSnapshot(snap, ui)
		fillObservedPreconditions(cmd, snap, ui)

		slotKey := act.SlotKey
		kind, qID, qIdx, outReqID := classifySessionCommand(cmd, sid)
		if slotKey == "" {
			slotKey = canonicalSlotKey(cmd, sid, qIdx)
		}

		if kind == slotKindAnswerPlan && !c.m.sync.HasFrozenAttempt(slotKey) {
			c.submitPlanPreflight(ctx, store, ui, sid, machineID, slotKey, cmd)
			return nil
		}

		c.submitSessionCommandDirect(ctx, store, ui, sid, machineID, slotKey, kind, qID, qIdx, outReqID, cmd)
		return nil

	case *corev1.Event_SessionAbandonCommand:
		if k.SessionAbandonCommand == nil || k.SessionAbandonCommand.SlotKey == "" {
			return nil
		}
		act := k.SessionAbandonCommand
		for reqID, pf := range store.pendingPlanPreflights {
			if pf != nil && pf.slotKey == act.SlotKey {
				delete(store.pendingPlanPreflights, reqID)
			}
		}
		c.m.sync.AbandonAttempt(ctx, act.SlotKey)
		if ui := store.uiBySession[act.SessionId]; ui != nil {
			delete(ui.Commands, act.SlotKey)
		}
		if meta, ok := store.slotMeta[act.SlotKey]; ok {
			if meta.kind == slotKindSend && meta.outgoingReqID != "" {
				var abortEff []*corev1.Effect
				c.abortResendJob(ctx, meta.outgoingReqID, false, &abortEff)
				if ui := store.uiBySession[meta.sessionID]; ui != nil {
					for _, og := range ui.Outgoing {
						if og != nil && og.RequestID == meta.outgoingReqID {
							og.Frozen = false
							og.Expired = true
							og.Resending = false
							og.State = screens.OutgoingFailed
						}
					}
				}
				delete(store.slotMeta, act.SlotKey)
				store.pendingDirty = true
				return abortEff
			}
			delete(store.slotMeta, act.SlotKey)
		}
		store.pendingDirty = true
		return nil

	case *corev1.Event_SessionCancelTrayItem:
		if k.SessionCancelTrayItem == nil || k.SessionCancelTrayItem.SessionId == "" || k.SessionCancelTrayItem.RequestId == "" {
			return nil
		}
		return c.handleCancelTrayItem(ctx, store, k.SessionCancelTrayItem.SessionId, k.SessionCancelTrayItem.RequestId)

	case *corev1.Event_SessionIgnoreTrayItem:
		if k.SessionIgnoreTrayItem == nil || k.SessionIgnoreTrayItem.SessionId == "" || k.SessionIgnoreTrayItem.RequestId == "" {
			return nil
		}
		return c.handleIgnoreTrayItem(ctx, store, k.SessionIgnoreTrayItem.SessionId, k.SessionIgnoreTrayItem.RequestId)

	case *corev1.Event_SessionPromoteTrayItem:
		if k.SessionPromoteTrayItem == nil || k.SessionPromoteTrayItem.SessionId == "" || k.SessionPromoteTrayItem.RequestId == "" {
			return nil
		}
		return c.handlePromoteTrayItem(ctx, store, k.SessionPromoteTrayItem.SessionId, k.SessionPromoteTrayItem.RequestId)

	case *corev1.Event_SessionRestoreTrayItem:
		if k.SessionRestoreTrayItem == nil || k.SessionRestoreTrayItem.SessionId == "" || k.SessionRestoreTrayItem.RequestId == "" {
			return nil
		}
		return c.handleRestoreTrayItem(ctx, store, k.SessionRestoreTrayItem.SessionId, k.SessionRestoreTrayItem.RequestId)

	case *corev1.Event_SessionRetryTrayItem:
		if k.SessionRetryTrayItem == nil || k.SessionRetryTrayItem.SessionId == "" || k.SessionRetryTrayItem.RequestId == "" {
			return nil
		}
		return c.handleRetryTrayItem(ctx, store, k.SessionRetryTrayItem.SessionId, k.SessionRetryTrayItem.RequestId)

	case *corev1.Event_SessionResumeTrayItem:
		if k.SessionResumeTrayItem == nil || k.SessionResumeTrayItem.SessionId == "" || k.SessionResumeTrayItem.RequestId == "" {
			return nil
		}
		return c.handleResumeTrayItem(ctx, store, k.SessionResumeTrayItem.SessionId, k.SessionResumeTrayItem.RequestId)

	case *corev1.Event_SessionCancelAllPaused:
		if k.SessionCancelAllPaused == nil || k.SessionCancelAllPaused.SessionId == "" {
			return nil
		}
		return c.handleCancelAllPaused(ctx, store, k.SessionCancelAllPaused.SessionId)

	case *corev1.Event_SessionUndoCancel:
		if k.SessionUndoCancel == nil || k.SessionUndoCancel.SessionId == "" {
			return nil
		}
		return c.handleUndoCancel(ctx, store, k.SessionUndoCancel.SessionId)

	case *corev1.Event_TimerFired:
		return c.handleSessionTimerFired(ctx, store, k.TimerFired)

	case *corev1.Event_SessionSetQuestionIndex:
		if k.SessionSetQuestionIndex == nil || k.SessionSetQuestionIndex.SessionId == "" {
			return nil
		}
		act := k.SessionSetQuestionIndex
		ui := store.ensureUI(act.SessionId)
		ui.QuestionIndexByID[act.QuestionId] = act.QuestionIndex
		store.pendingDirty = true
		return nil

	case *corev1.Event_SessionLoadEarlier:
		if k.SessionLoadEarlier == nil || k.SessionLoadEarlier.SessionId == "" {
			return nil
		}
		c.m.sync.RetrySessionHistory(ctx, k.SessionLoadEarlier.SessionId)
		store.pendingDirty = true
		return nil

	case *corev1.Event_SessionEditApprovalDeny:
		c.handleSessionEditApprovalDeny(ctx, store, k.SessionEditApprovalDeny)
		return nil

	case *corev1.Event_SessionResolveApproval:
		return c.handleSessionResolveApproval(ctx, store, k.SessionResolveApproval)

	case *corev1.Event_SessionToggleQuestionOption:
		c.handleSessionToggleQuestionOption(store, k.SessionToggleQuestionOption)
		return nil

	case *corev1.Event_SessionAnswerQuestion:
		return c.handleSessionAnswerQuestion(ctx, store, k.SessionAnswerQuestion, 0)

	case *corev1.Event_SessionAnswerPlan:
		return c.handleSessionAnswerPlan(ctx, store, k.SessionAnswerPlan)

	case *corev1.Event_SessionSimpleCommand:
		return c.handleSessionSimpleCommand(ctx, store, k.SessionSimpleCommand)

	case *corev1.Event_SessionRename:
		return c.handleSessionRename(ctx, store, k.SessionRename)

	case *corev1.Event_SessionSetApprovalMode:
		return c.handleSessionSetApprovalMode(ctx, store, k.SessionSetApprovalMode)

	case *corev1.Event_SessionSetModel:
		return c.handleSessionSetModel(ctx, store, k.SessionSetModel)

	case *corev1.Event_SessionRetryCommand:
		return c.handleSessionRetryCommand(ctx, store, k.SessionRetryCommand)

	default:
		if eff, ok := c.dispatchAttachmentEvent(ctx, ev); ok {
			return eff
		}
		return c.dispatchSharedFileEvent(ctx, ev)
	}
}

func (c *Core) appendSessionRenderTarget(dirty sync.DirtyFlags, effects []*corev1.Effect) []*corev1.Effect {
	store := c.m.ensureSessionStore()
	wantSession := store.pendingDirty || (len(store.opened) > 0 && (dirty.SessionData || dirty.Home || dirty.Direct))
	store.pendingDirty = false
	if !wantSession {
		return effects
	}
	for _, eff := range effects {
		if eff != nil && eff.GetRender() != nil {
			r := eff.GetRender()
			if !slices.Contains(r.Screens, corev1.ScreenTarget_SCREEN_TARGET_SESSION) {
				r.Screens = append(r.Screens, corev1.ScreenTarget_SCREEN_TARGET_SESSION)
			}
			return effects
		}
	}
	return append(effects, &corev1.Effect{
		Effect: &corev1.Effect_Render{
			Render: &corev1.RenderEffect{
				Screens: []corev1.ScreenTarget{corev1.ScreenTarget_SCREEN_TARGET_SESSION},
			},
		},
	})
}

func sessionIDFromCommand(cmd *v1.Command) string {
	if cmd == nil {
		return ""
	}
	switch k := cmd.Kind.(type) {
	case *v1.Command_Send:
		return k.Send.GetSessionId()
	case *v1.Command_Interrupt:
		return k.Interrupt.GetSessionId()
	case *v1.Command_ResolveApproval:
		return k.ResolveApproval.GetSessionId()
	case *v1.Command_AnswerQuestion:
		return k.AnswerQuestion.GetSessionId()
	case *v1.Command_StopSession:
		return k.StopSession.GetSessionId()
	case *v1.Command_CancelInput:
		return k.CancelInput.GetSessionId()
	case *v1.Command_SetModel:
		return k.SetModel.GetSessionId()
	case *v1.Command_SetApprovalMode:
		return k.SetApprovalMode.GetSessionId()
	case *v1.Command_Compact:
		return k.Compact.GetSessionId()
	case *v1.Command_AnswerPlan:
		return k.AnswerPlan.GetSessionId()
	case *v1.Command_OpenTerminal:
		return k.OpenTerminal.GetSessionId()
	case *v1.Command_ResumeSession:
		return k.ResumeSession.GetSessionId()
	case *v1.Command_ResumeInput:
		return k.ResumeInput.GetSessionId()
	case *v1.Command_RenameSession:
		return k.RenameSession.GetSessionId()
	case *v1.Command_ForkSession:
		return k.ForkSession.GetSessionId()
	default:
		return ""
	}
}

func classifySessionCommand(cmd *v1.Command, sid string) (kind sessionSlotKind, qID string, qIdx int32, outReqID string) {
	switch k := cmd.Kind.(type) {
	case *v1.Command_Send:
		if k.Send != nil {
			return slotKindSend, "", 0, k.Send.RequestId
		}
	case *v1.Command_ResolveApproval:
		return slotKindResolveApproval, "", 0, ""
	case *v1.Command_AnswerQuestion:
		if k.AnswerQuestion != nil {
			var idx int32
			if k.AnswerQuestion.QuestionIndex != nil {
				idx = *k.AnswerQuestion.QuestionIndex
			}
			return slotKindAnswerQuestion, k.AnswerQuestion.QuestionId, idx, ""
		}
	case *v1.Command_AnswerPlan:
		return slotKindAnswerPlan, "", 0, ""
	case *v1.Command_ForkSession:
		return slotKindFork, "", 0, ""
	case *v1.Command_CancelInput:
		if k.CancelInput != nil {
			return slotKindCancelInput, "", 0, k.CancelInput.RequestId
		}
	case *v1.Command_ResumeInput:
		if k.ResumeInput != nil {
			return slotKindResumeInput, "", 0, k.ResumeInput.RequestId
		}
	}
	return slotKindGeneric, "", 0, ""
}

func canonicalSlotKey(cmd *v1.Command, sid string, qIdx int32) string {
	switch k := cmd.Kind.(type) {
	case *v1.Command_Send:
		if k.Send != nil {
			return "send:" + sid + ":" + k.Send.RequestId
		}
	case *v1.Command_ResolveApproval:
		if k.ResolveApproval != nil {
			return "approval:" + sid + ":" + k.ResolveApproval.ApprovalId
		}
	case *v1.Command_AnswerQuestion:
		if k.AnswerQuestion != nil {
			return "question:" + sid + ":" + k.AnswerQuestion.QuestionId + "#" + strconv.Itoa(int(qIdx))
		}
	case *v1.Command_AnswerPlan:
		return "plan:" + sid
	case *v1.Command_ForkSession:
		return "fork:" + sid
	case *v1.Command_Interrupt:
		return "interrupt:" + sid
	case *v1.Command_StopSession:
		return "stop:" + sid
	case *v1.Command_ResumeSession:
		return "resume:" + sid
	case *v1.Command_RenameSession:
		return "rename:" + sid
	case *v1.Command_SetModel:
		return "set_model:" + sid
	case *v1.Command_SetApprovalMode:
		return "set_mode:" + sid
	case *v1.Command_Compact:
		return "compact:" + sid
	case *v1.Command_CancelInput:
		if k.CancelInput != nil {
			return "cancel_input:" + sid + ":" + k.CancelInput.RequestId
		}
	case *v1.Command_ResumeInput:
		if k.ResumeInput != nil {
			return "resume_input:" + sid + ":" + k.ResumeInput.RequestId
		}
	case *v1.Command_OpenTerminal:
		return "open_terminal:" + sid
	}
	return "session:" + sid
}

func fillObservedPreconditions(cmd *v1.Command, snap world.SessionSnapshot, ui *screens.SessionUIState) {
	switch k := cmd.Kind.(type) {
	case *v1.Command_ResolveApproval:
		if r := k.ResolveApproval; r != nil && r.ObservedEventSeq == 0 {
			if snap.Checkpoint != nil {
				for _, a := range snap.Checkpoint.Actions {
					if a != nil && a.GetApprovalRequested() != nil && a.GetApprovalRequested().ApprovalId == r.ApprovalId {
						r.ObservedEventSeq = a.Seq
						return
					}
				}
			}
			for i := len(snap.Events) - 1; i >= 0; i-- {
				if e := snap.Events[i]; e != nil && e.GetApprovalRequested() != nil && e.GetApprovalRequested().ApprovalId == r.ApprovalId {
					r.ObservedEventSeq = e.Seq
					return
				}
			}
			if seq := ui.LastApprovalSeq[r.ApprovalId]; seq > 0 {
				r.ObservedEventSeq = seq
			}
		}
	case *v1.Command_AnswerQuestion:
		if q := k.AnswerQuestion; q != nil && q.ObservedEventSeq == 0 {
			if snap.Checkpoint != nil {
				for _, a := range snap.Checkpoint.Actions {
					if a != nil && a.GetQuestionAsked() != nil && a.GetQuestionAsked().QuestionId == q.QuestionId {
						q.ObservedEventSeq = a.Seq
						return
					}
				}
			}
			for i := len(snap.Events) - 1; i >= 0; i-- {
				if e := snap.Events[i]; e != nil && e.GetQuestionAsked() != nil && e.GetQuestionAsked().QuestionId == q.QuestionId {
					q.ObservedEventSeq = e.Seq
					return
				}
			}
			if snap.Session != nil && snap.Session.Pending.Question != nil &&
				snap.Session.Pending.Question.QuestionID == q.QuestionId &&
				snap.Session.Pending.Question.ObservedEventSeq > 0 {
				q.ObservedEventSeq = snap.Session.Pending.Question.ObservedEventSeq
				return
			}
			if seq := ui.LastQuestionSeq[q.QuestionId]; seq > 0 {
				q.ObservedEventSeq = seq
			}
		}
	case *v1.Command_AnswerPlan:
		if p := k.AnswerPlan; p != nil {
			if p.ObservedEventSeq == 0 {
				if snap.Checkpoint != nil && snap.Checkpoint.PlanEventSeq > 0 {
					p.ObservedEventSeq = snap.Checkpoint.PlanEventSeq
				} else if ui.LastPlanSeq > 0 {
					p.ObservedEventSeq = ui.LastPlanSeq
				}
			}
			if len(p.ObservedScreenHash) == 0 {
				if snap.Checkpoint != nil && len(snap.Checkpoint.PlanScreenHash) > 0 {
					p.ObservedScreenHash = slices.Clone(snap.Checkpoint.PlanScreenHash)
				} else if len(ui.LastPlanHash) > 0 {
					p.ObservedScreenHash = slices.Clone(ui.LastPlanHash)
				}
			}
		}
	}
}

func findOrAppendOutgoing(
	ui *screens.SessionUIState,
	reqID, slotKey string,
	sr *v1.SendRequest,
	nowMs int64,
) *screens.SessionOutgoingRecord {
	for _, og := range ui.Outgoing {
		if og != nil && og.RequestID == reqID {
			og.Restored = false
			og.SlotKey = slotKey
			return og
		}
	}
	og := &screens.SessionOutgoingRecord{
		RequestID: reqID,
		SlotKey:   slotKey,
		Text:      sr.Text,
		ImageIDs:  append([]string(nil), sr.ImageIds...),
		FileIDs:   append([]string(nil), sr.FileIds...),
		Mode:      sr.Mode,
		State:     screens.OutgoingSending,
		CreatedMs: nowMs,
	}
	ui.Outgoing = append(ui.Outgoing, og)
	return og
}

func (c *Core) sessionMachineID(sid string, snap world.SessionSnapshot) string {
	if snap.Session != nil && snap.Session.MachineID != "" {
		return snap.Session.MachineID
	}
	if se := c.m.sync.SessionEntry(sid); se != nil && se.MachineID != "" {
		return se.MachineID
	}
	return ""
}

func (c *Core) findTrayViewAndItem(store *sessionStore, sid, reqID string) (*screens.SessionUIState, world.SessionSnapshot, *corev1.SessionPendingTrayView, *corev1.SessionTrayItemView) {
	ui := store.ensureUI(sid)
	snap := c.m.sync.SessionSnapshot(sid)
	reconcileSessionSnapshot(snap, ui)
	vm := screens.BuildSessionView(snap, c.m.state, &corev1.SessionScreenRequest{SessionId: sid}, ui)
	tray := vm.GetComposer().GetPendingTray()
	var found *corev1.SessionTrayItemView
	if tray != nil && reqID != "" {
		for _, it := range tray.GetOrderedItems() {
			if it != nil && it.RequestId == reqID {
				found = it
				break
			}
		}
	}
	return ui, snap, tray, found
}

func (c *Core) submitCancelInput(ctx *sync.StepContext, store *sessionStore, ui *screens.SessionUIState, snap world.SessionSnapshot, sid, reqID string) {
	cmd := &v1.Command{
		ProtocolVersion: sync.PeerVersion,
		Kind: &v1.Command_CancelInput{
			CancelInput: &v1.CancelInputRequest{
				SessionId: sid,
				RequestId: reqID,
			},
		},
	}
	fillObservedPreconditions(cmd, snap, ui)
	slotKey := canonicalSlotKey(cmd, sid, 0)
	c.submitSessionCommandDirect(ctx, store, ui, sid, c.sessionMachineID(sid, snap), slotKey, slotKindCancelInput, "", 0, reqID, cmd)
}

func (c *Core) clearOutgoingAndSlot(ctx *sync.StepContext, store *sessionStore, ui *screens.SessionUIState, reqID, slotKey string, restored bool) {
	if restored {
		if og := popOutgoing(ui, reqID); og != nil && slotKey == "" {
			slotKey = og.SlotKey
		}
	} else {
		if removed := removeOutgoing(ui, reqID); removed != "" && slotKey == "" {
			slotKey = removed
		}
	}
	if slotKey != "" && ctx != nil {
		c.m.sync.AbandonAttempt(ctx, slotKey)
		delete(ui.Commands, slotKey)
		delete(store.slotMeta, slotKey)
	}
}

func (c *Core) scheduleUndoBatch(ctx *sync.StepContext, ui *screens.SessionUIState, sid, batchID string, item *screens.SessionUndoItem) {
	if ui == nil || item == nil {
		return
	}
	if ui.Undo != nil && ui.Undo.BatchID == batchID {
		ui.Undo.Items = append(ui.Undo.Items, item)
	} else {
		ui.Undo = &screens.SessionUndoBatch{
			BatchID: batchID,
			Items:   []*screens.SessionUndoItem{item},
		}
	}
	c.m.sync.ScheduleAppTimer(ctx, corev1.TimerKind_TIMER_KIND_SESSION_UNDO, sid, 5000)
}

func (c *Core) resendTrayContent(
	ctx *sync.StepContext,
	store *sessionStore,
	ui *screens.SessionUIState,
	sid, machineID, sourceReqID, textStr string,
	mode v1.SendMode,
	imgIDs, fileIDs []string,
) []*corev1.Effect {
	newReqID := c.m.sync.NextID(ctx)
	if len(imgIDs) > 0 || len(fileIDs) > 0 {
		var resendEff []*corev1.Effect
		c.startOutboxResend(ctx, sid, machineID, sourceReqID, newReqID, textStr, mode, &resendEff)
		return resendEff
	}
	cmd := &v1.Command{
		ProtocolVersion: sync.PeerVersion,
		Kind: &v1.Command_Send{
			Send: &v1.SendRequest{
				SessionId: sid,
				RequestId: newReqID,
				Text:      textStr,
				Mode:      mode,
			},
		},
	}
	snap := c.m.sync.SessionSnapshot(sid)
	fillObservedPreconditions(cmd, snap, ui)
	slotKey := canonicalSlotKey(cmd, sid, 0)
	c.submitSessionCommandDirect(ctx, store, ui, sid, machineID, slotKey, slotKindSend, "", 0, newReqID, cmd)
	return nil
}

func (c *Core) handleCancelTrayItem(ctx *sync.StepContext, store *sessionStore, sid, reqID string) []*corev1.Effect {
	ui, snap, _, item := c.findTrayViewAndItem(store, sid, reqID)
	if item == nil || !item.CanCancel {
		return nil
	}
	hasLocal := !item.ForeignDimmed && (item.Text != "" || len(item.ImageIds) > 0 || len(item.FileIds) > 0)
	if !item.ServerBacked {
		var effects []*corev1.Effect
		c.abortResendJob(ctx, reqID, hasLocal, &effects)
		c.clearOutgoingAndSlot(ctx, store, ui, reqID, item.SlotKey, false)
		ui.DismissedInputIDs[reqID] = true
		if hasLocal {
			batchID := c.m.sync.NextID(ctx)
			c.scheduleUndoBatch(ctx, ui, sid, batchID, &screens.SessionUndoItem{
				SourceReqID: reqID,
				Text:        item.Text,
				ImageIDs:    append([]string(nil), item.ImageIds...),
				FileIDs:     append([]string(nil), item.FileIds...),
				Mode:        item.Mode,
			})
		}
		pruneSessionLocalContents(ui, map[string]bool{}, activeInputSetFromSnap(snap, ui))
		store.pendingDirty = true
		return effects
	}
	if item.NetworkActionsDisabled {
		return nil
	}
	ui.CancelIntents[reqID] = &screens.SessionCancelIntent{
		Kind:            screens.CancelIntentCancel,
		BatchID:         c.m.sync.NextID(ctx),
		SourceReqID:     reqID,
		Text:            item.Text,
		ImageIDs:        append([]string(nil), item.ImageIds...),
		FileIDs:         append([]string(nil), item.FileIds...),
		Mode:            item.Mode,
		HasLocalContent: hasLocal,
		HasAttachments:  len(item.ImageIds) > 0 || len(item.FileIds) > 0,
	}
	c.submitCancelInput(ctx, store, ui, snap, sid, reqID)
	return nil
}

func (c *Core) handleIgnoreTrayItem(ctx *sync.StepContext, store *sessionStore, sid, reqID string) []*corev1.Effect {
	ui, snap, _, item := c.findTrayViewAndItem(store, sid, reqID)
	if item == nil || !item.CanIgnore {
		return nil
	}
	var effects []*corev1.Effect
	c.abortResendJob(ctx, reqID, false, &effects)
	c.clearOutgoingAndSlot(ctx, store, ui, reqID, item.SlotKey, false)
	ui.DismissedInputIDs[reqID] = true
	pruneSessionLocalContents(ui, map[string]bool{}, activeInputSetFromSnap(snap, ui))
	store.pendingDirty = true
	return effects
}

func (c *Core) handlePromoteTrayItem(ctx *sync.StepContext, store *sessionStore, sid, reqID string) []*corev1.Effect {
	ui, snap, _, item := c.findTrayViewAndItem(store, sid, reqID)
	if item == nil || !item.CanPromote || !item.ServerBacked || item.NetworkActionsDisabled {
		return nil
	}
	ui.CancelIntents[reqID] = &screens.SessionCancelIntent{
		Kind:            screens.CancelIntentPromote,
		SourceReqID:     reqID,
		Text:            item.Text,
		ImageIDs:        append([]string(nil), item.ImageIds...),
		FileIDs:         append([]string(nil), item.FileIds...),
		Mode:            v1.SendMode_SEND_MODE_STEER,
		HasLocalContent: true,
		HasAttachments:  len(item.ImageIds) > 0 || len(item.FileIds) > 0,
	}
	c.submitCancelInput(ctx, store, ui, snap, sid, reqID)
	return nil
}

func (c *Core) handleRestoreTrayItem(ctx *sync.StepContext, store *sessionStore, sid, reqID string) []*corev1.Effect {
	ui, snap, _, item := c.findTrayViewAndItem(store, sid, reqID)
	if item == nil || !item.CanRestore {
		return nil
	}
	attActive := c.m.attachments != nil && (c.m.attachments.configured || c.m.attachments.outbox[reqID] != nil || c.m.attachments.drafts["session:"+sid] != nil)
	if attActive {
		if reason, ok := c.canPutBackRequest(sid, reqID, item.ImageIds, item.FileIds); !ok {
			scope := &corev1.DraftScope{Kind: corev1.DraftScopeKind_DRAFT_SCOPE_KIND_SESSION, Id: sid}
			d := c.ensureDraftState(scope)
			d.notice = reason
			c.markDraftDirty(ctx, d.scope)
			return nil
		}
	}
	if !item.ServerBacked {
		var effects []*corev1.Effect
		c.abortResendJob(ctx, reqID, true, &effects)
		c.clearOutgoingAndSlot(ctx, store, ui, reqID, item.SlotKey, true)
		ui.DismissedInputIDs[reqID] = true
		c.applyPutBackToDraft(ctx, sid, reqID, item.Text, item.ImageIds, item.FileIds, false, &effects)
		pruneSessionLocalContents(ui, map[string]bool{}, activeInputSetFromSnap(snap, ui))
		store.pendingDirty = true
		effects = append(effects, &corev1.Effect{
			Effect: &corev1.Effect_SessionRestoreComposer{
				SessionRestoreComposer: &corev1.SessionRestoreComposerEffect{
					SessionId: sid,
					RequestId: reqID,
				},
			},
		})
		return effects
	}
	if item.NetworkActionsDisabled {
		return nil
	}
	ui.CancelIntents[reqID] = &screens.SessionCancelIntent{
		Kind:            screens.CancelIntentRestore,
		SourceReqID:     reqID,
		Text:            item.Text,
		ImageIDs:        append([]string(nil), item.ImageIds...),
		FileIDs:         append([]string(nil), item.FileIds...),
		Mode:            item.Mode,
		HasLocalContent: true,
		HasAttachments:  len(item.ImageIds) > 0 || len(item.FileIds) > 0,
	}
	c.submitCancelInput(ctx, store, ui, snap, sid, reqID)
	return nil
}

func (c *Core) handleRetryTrayItem(ctx *sync.StepContext, store *sessionStore, sid, reqID string) []*corev1.Effect {
	ui, snap, _, item := c.findTrayViewAndItem(store, sid, reqID)
	if item == nil || !item.CanRetry || item.NetworkActionsDisabled {
		return nil
	}
	c.clearOutgoingAndSlot(ctx, store, ui, reqID, item.SlotKey, false)
	ui.DismissedInputIDs[reqID] = true
	effs := c.resendTrayContent(ctx, store, ui, sid, c.sessionMachineID(sid, snap), reqID, item.Text, item.Mode, item.ImageIds, item.FileIds)
	pruneSessionLocalContents(ui, map[string]bool{}, activeInputSetFromSnap(snap, ui))
	store.pendingDirty = true
	return effs
}

func (c *Core) handleResumeTrayItem(ctx *sync.StepContext, store *sessionStore, sid, reqID string) []*corev1.Effect {
	ui, snap, _, item := c.findTrayViewAndItem(store, sid, reqID)
	if item == nil || !item.CanResume || item.NetworkActionsDisabled {
		return nil
	}
	cmd := &v1.Command{
		ProtocolVersion: sync.PeerVersion,
		Kind: &v1.Command_ResumeInput{
			ResumeInput: &v1.ResumeInputRequest{
				SessionId: sid,
				RequestId: reqID,
			},
		},
	}
	fillObservedPreconditions(cmd, snap, ui)
	slotKey := canonicalSlotKey(cmd, sid, 0)
	c.submitSessionCommandDirect(ctx, store, ui, sid, c.sessionMachineID(sid, snap), slotKey, slotKindResumeInput, "", 0, reqID, cmd)
	return nil
}

func (c *Core) handleCancelAllPaused(ctx *sync.StepContext, store *sessionStore, sid string) []*corev1.Effect {
	ui, snap, tray, _ := c.findTrayViewAndItem(store, sid, "")
	if tray == nil || tray.Offline {
		return nil
	}
	var pausedItems []*corev1.SessionTrayItemView
	for _, g := range tray.GetGroups() {
		if g != nil && g.Kind == corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_PAUSED {
			pausedItems = g.GetItems()
			break
		}
	}
	if len(pausedItems) == 0 {
		return nil
	}
	batchID := c.m.sync.NextID(ctx)
	for _, item := range pausedItems {
		if item == nil || !item.CanCancel || !item.ServerBacked || item.NetworkActionsDisabled {
			continue
		}
		hasLocal := !item.ForeignDimmed && (item.Text != "" || len(item.ImageIds) > 0 || len(item.FileIds) > 0)
		ui.CancelIntents[item.RequestId] = &screens.SessionCancelIntent{
			Kind:            screens.CancelIntentCancelAll,
			BatchID:         batchID,
			SourceReqID:     item.RequestId,
			Text:            item.Text,
			ImageIDs:        append([]string(nil), item.ImageIds...),
			FileIDs:         append([]string(nil), item.FileIds...),
			Mode:            item.Mode,
			HasLocalContent: hasLocal,
			HasAttachments:  len(item.ImageIds) > 0 || len(item.FileIds) > 0,
		}
		c.submitCancelInput(ctx, store, ui, snap, sid, item.RequestId)
	}
	return nil
}

func (c *Core) handleUndoCancel(ctx *sync.StepContext, store *sessionStore, sid string) []*corev1.Effect {
	ui, snap, tray, _ := c.findTrayViewAndItem(store, sid, "")
	if ui.Undo == nil || len(ui.Undo.Items) == 0 || (tray != nil && tray.Offline) {
		return nil
	}
	c.m.sync.CancelAppTimer(ctx, corev1.TimerKind_TIMER_KIND_SESSION_UNDO, sid)
	items := ui.Undo.Items
	ui.Undo = nil
	machineID := c.sessionMachineID(sid, snap)
	var outEffects []*corev1.Effect
	for _, it := range items {
		if it == nil {
			continue
		}
		if effs := c.resendTrayContent(ctx, store, ui, sid, machineID, it.SourceReqID, it.Text, it.Mode, it.ImageIDs, it.FileIDs); len(effs) > 0 {
			outEffects = append(outEffects, effs...)
		}
	}
	pruneSessionLocalContents(ui, map[string]bool{}, activeInputSetFromSnap(snap, ui))
	store.pendingDirty = true
	return outEffects
}

func (c *Core) handleSessionTimerFired(ctx *sync.StepContext, store *sessionStore, ev *corev1.TimerFiredEvent) []*corev1.Effect {
	if ev == nil || !c.m.sync.ConsumeAppTimer(ev) || ev.Key == "" {
		return nil
	}
	switch ev.Kind {
	case corev1.TimerKind_TIMER_KIND_SHARED_FILE_RECONNECT:
		return c.handleSharedFileReconnectTimer(ctx, ev.Key)
	case corev1.TimerKind_TIMER_KIND_ATTACHMENT_UPLOAD_POLL:
		return c.handleUploadPollTimer(ctx, ev.Key)
	case corev1.TimerKind_TIMER_KIND_ATTACHMENT_UPLOAD_RECONNECT:
		return c.handleUploadReconnectTimer(ctx, ev.Key)
	case corev1.TimerKind_TIMER_KIND_DRAFT_EXPIRY:
		return c.handleDraftExpiryTimer(ctx, ev.Key)
	case corev1.TimerKind_TIMER_KIND_TERMINAL_HISTORY_TIMEOUT:
		return c.handleTerminalHistoryTimeout(ctx, ev.Key)
	case corev1.TimerKind_TIMER_KIND_UI_TRANSIENT:
		return c.handleUITransientTimer(ctx, store, ev.Key)
	}
	sid := ev.Key
	ui := store.ensureUI(sid)
	switch ev.Kind {
	case corev1.TimerKind_TIMER_KIND_SESSION_UNDO:
		ui.Undo = nil
		snap := c.m.sync.SessionSnapshot(sid)
		reconcileSessionSnapshot(snap, ui)
		store.pendingDirty = true
	case corev1.TimerKind_TIMER_KIND_SESSION_STEER_GRACE:
		nowMs := c.m.sync.NowMs()
		ui.SteerGraceNextTargetMs = 0
		for reqID, expMs := range ui.SteerVisibleAfterMs {
			if nowMs <= 0 || expMs <= nowMs {
				delete(ui.SteerVisibleAfterMs, reqID)
			}
		}
		for _, og := range ui.Outgoing {
			if og != nil && og.VisibleAfterMs > 0 && (nowMs <= 0 || og.VisibleAfterMs <= nowMs) {
				og.VisibleAfterMs = 0
			}
		}
		var earliest int64
		for _, expMs := range ui.SteerVisibleAfterMs {
			if nowMs > 0 && expMs > nowMs && (earliest == 0 || expMs < earliest) {
				earliest = expMs
			}
		}
		if earliest > 0 {
			ui.SteerGraceNextTargetMs = earliest
			c.m.sync.ScheduleAppTimer(ctx, corev1.TimerKind_TIMER_KIND_SESSION_STEER_GRACE, sid, earliest-nowMs)
		}
		store.pendingDirty = true
	}
	return nil
}

func (c *Core) settleCancelInput(
	store *sessionStore,
	ui *screens.SessionUIState,
	meta sessionSlotMeta,
	sc sync.SettledCommand,
	effects *[]*corev1.Effect,
	dirty *sync.DirtyFlags,
) {
	reqID := meta.outgoingReqID
	intent := ui.CancelIntents[reqID]
	delete(ui.CancelIntents, reqID)
	if sc.Category != outcome.Executed || sc.ErrorCode != "" {
		return
	}
	ui.DismissedInputIDs[reqID] = true
	if intent == nil {
		return
	}
	snap := c.m.sync.SessionSnapshot(meta.sessionID)
	machineID := meta.machineID
	if machineID == "" {
		machineID = c.sessionMachineID(meta.sessionID, snap)
	}
	stepPrev, ctx := c.m.sync.BeginStep()
	switch intent.Kind {
	case screens.CancelIntentPromote:
		if effs := c.resendTrayContent(ctx, store, ui, meta.sessionID, machineID, intent.SourceReqID, intent.Text, v1.SendMode_SEND_MODE_STEER, intent.ImageIDs, intent.FileIDs); len(effs) > 0 {
			*effects = append(*effects, effs...)
		}
	case screens.CancelIntentRestore:
		c.applyPutBackToDraft(ctx, meta.sessionID, intent.SourceReqID, intent.Text, intent.ImageIDs, intent.FileIDs, true, effects)
		*effects = append(*effects, &corev1.Effect{
			Effect: &corev1.Effect_SessionRestoreComposer{
				SessionRestoreComposer: &corev1.SessionRestoreComposerEffect{
					SessionId: meta.sessionID,
					RequestId: intent.SourceReqID,
				},
			},
		})
	case screens.CancelIntentCancel, screens.CancelIntentCancelAll:
		if intent.HasLocalContent {
			c.scheduleUndoBatch(ctx, ui, meta.sessionID, intent.BatchID, &screens.SessionUndoItem{
				SourceReqID: intent.SourceReqID,
				Text:        intent.Text,
				ImageIDs:    append([]string(nil), intent.ImageIDs...),
				FileIDs:     append([]string(nil), intent.FileIDs...),
				Mode:        intent.Mode,
			})
		}
	}
	pruneSessionLocalContents(ui, map[string]bool{}, activeInputSetFromSnap(snap, ui))
	stepRes := c.m.sync.EndStep(stepPrev, ctx)
	*effects = append(*effects, stepRes.Effects...)
	dirty.Add(stepRes.Dirty.Home, stepRes.Dirty.Project, stepRes.Dirty.NewSession)
	if stepRes.Dirty.SessionData {
		dirty.MarkSessionData()
	}
}

func (c *Core) maybeStartCodexSteerGrace(ctx *sync.StepContext, ui *screens.SessionUIState, og *screens.SessionOutgoingRecord, sid string) {
	if ui == nil || og == nil || og.Mode == v1.SendMode_SEND_MODE_QUEUE || sid == "" {
		return
	}
	snap := c.m.sync.SessionSnapshot(sid)
	var agent v1.Agent
	var status v1.SessionStatus
	if snap.Session != nil && snap.Session.Info != nil {
		agent = snap.Session.Info.Agent
		status = snap.Session.Info.Status
	} else if se := c.m.sync.SessionEntry(sid); se != nil && se.Info != nil {
		agent = se.Info.Agent
		status = se.Info.Status
	}
	if agent != v1.Agent_AGENT_CODEX || status == v1.SessionStatus_SESSION_STATUS_STARTING {
		return
	}
	running := snap.Projection != nil && snap.Projection.Running
	waitingAnswer := snap.Projection != nil && (len(snap.Projection.Approvals) > 0 || len(snap.Projection.Questions) > 0)
	if !running || waitingAnswer {
		return
	}
	nowMs := c.m.sync.NowMs()
	if nowMs <= 0 {
		return
	}
	expMs := nowMs + 1000
	og.VisibleAfterMs = expMs
	ui.SteerVisibleAfterMs[og.RequestID] = expMs
	if ui.SteerGraceNextTargetMs == 0 || ui.SteerGraceNextTargetMs <= nowMs || expMs < ui.SteerGraceNextTargetMs {
		ui.SteerGraceNextTargetMs = expMs
		c.m.sync.ScheduleAppTimer(ctx, corev1.TimerKind_TIMER_KIND_SESSION_STEER_GRACE, sid, 1000)
	}
}

func activeInputSetFromSnap(snap world.SessionSnapshot, ui *screens.SessionUIState) map[string]bool {
	out := make(map[string]bool)
	if snap.Projection == nil {
		return out
	}
	for _, pi := range snap.Projection.PendingInputs {
		if pi != nil && pi.RequestId != "" && (ui == nil || !ui.DismissedInputIDs[pi.RequestId]) {
			out[pi.RequestId] = true
		}
	}
	for _, ip := range snap.Projection.Inputs {
		if ip == nil || ip.RequestId == "" || (ui != nil && ui.DismissedInputIDs[ip.RequestId]) {
			continue
		}
		switch ip.Status {
		case v1.InputStatus_INPUT_STATUS_FAILED,
			v1.InputStatus_INPUT_STATUS_NEEDS_TERMINAL,
			v1.InputStatus_INPUT_STATUS_UNCONFIRMED:
			out[ip.RequestId] = true
		}
	}
	return out
}

func removeOutgoing(ui *screens.SessionUIState, reqID string) string {
	for i, og := range ui.Outgoing {
		if og != nil && og.RequestID == reqID {
			slotKey := og.SlotKey
			ui.Outgoing = append(ui.Outgoing[:i], ui.Outgoing[i+1:]...)
			return slotKey
		}
	}
	return ""
}

func popOutgoing(ui *screens.SessionUIState, reqID string) *screens.SessionOutgoingRecord {
	for i, og := range ui.Outgoing {
		if og != nil && og.RequestID == reqID {
			ui.Outgoing = append(ui.Outgoing[:i], ui.Outgoing[i+1:]...)
			og.Restored = true
			return og
		}
	}
	return nil
}
