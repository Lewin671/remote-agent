package app

import (
	"slices"
	"strconv"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/outcome"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/screens"
	"github.com/Lewin671/remote-agent/clientcore/sync"
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
	sessionID     string
	machineID     string
	kind          sessionSlotKind
	outgoingReqID string
	questionID    string
	questionIndex int32
}

type sessionStore struct {
	uiBySession          map[string]*screens.SessionUIState
	opened               map[string]bool
	slotsByReqID         map[string]string
	slotMeta             map[string]sessionSlotMeta
	pendingCreatePrompts map[string]*screens.PendingStartPrompt
	pendingDirty         bool
}

func newSessionStore() *sessionStore {
	return &sessionStore{
		uiBySession:          make(map[string]*screens.SessionUIState),
		opened:               make(map[string]bool),
		slotsByReqID:         make(map[string]string),
		slotMeta:             make(map[string]sessionSlotMeta),
		pendingCreatePrompts: make(map[string]*screens.PendingStartPrompt),
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

func reconcileSessionSnapshot(snap syncSessionInputAlias, ui *screens.SessionUIState) {
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
	recordedReqs := make(map[string]bool)
	hasUserMsg := false
	for _, ev := range snap.Events {
		if ev == nil {
			continue
		}
		if um := ev.GetUserMessage(); um != nil {
			hasUserMsg = true
			if um.RequestId != "" {
				recordedReqs[um.RequestId] = true
			}
		}
		if is := ev.GetInputState(); is != nil && is.RequestId != "" {
			recordedReqs[is.RequestId] = true
		}
	}
	if snap.Projection != nil {
		for _, ip := range snap.Projection.Inputs {
			if ip != nil && ip.RequestId != "" {
				recordedReqs[ip.RequestId] = true
			}
		}
	}
	if hasUserMsg && ui.PendingStartPrompt != nil {
		ui.PendingStartPrompt = nil
	}
	if len(ui.Outgoing) > 0 && len(recordedReqs) > 0 {
		kept := ui.Outgoing[:0]
		for _, og := range ui.Outgoing {
			if og == nil {
				continue
			}
			if og.State == corev1.SessionOutgoingState_SESSION_OUTGOING_STATE_SENDING ||
				og.State == corev1.SessionOutgoingState_SESSION_OUTGOING_STATE_SENT {
				if recordedReqs[og.RequestID] {
					continue
				}
			} else if recordedReqs[og.RequestID] && og.State == corev1.SessionOutgoingState_SESSION_OUTGOING_STATE_UNKNOWN {
				// Once observed in events or InputProjection, clear unknown local duplicate.
				continue
			}
			kept = append(kept, og)
		}
		ui.Outgoing = kept
	}
}

type syncSessionInputAlias = screens.SessionViewInput

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
			rec.ErrorText = sc.ErrorText
		} else {
			delete(ui.Commands, slotKey)
			delete(store.slotMeta, slotKey)
			if meta.kind == slotKindAnswerQuestion && meta.questionID != "" {
				ui.QuestionIndexByID[meta.questionID] = meta.questionIndex + 1
			}
			if meta.kind == slotKindFork && sc.Result != nil && sc.Result.GetForkSession() != nil {
				if ns := sc.Result.GetForkSession().GetSession(); ns != nil && ns.Id != "" {
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
		rec.ErrorText = sc.ErrorText
	case outcome.Unknown:
		rec.Status = corev1.SessionCommandStatus_SESSION_COMMAND_STATUS_UNKNOWN
		rec.Frozen = sc.Frozen
		rec.Expired = sc.Expired
		rec.ErrorCode = sc.ErrorCode
		rec.ErrorText = sc.ErrorText
	}

	if meta.kind == slotKindSend && meta.outgoingReqID != "" {
		store.settleOutgoing(ui, meta.outgoingReqID, sc)
	}
	store.pendingDirty = true
}

func (s *sessionStore) settleOutgoing(ui *screens.SessionUIState, reqID string, sc sync.SettledCommand) {
	for _, og := range ui.Outgoing {
		if og == nil || og.RequestID != reqID {
			continue
		}
		switch sc.Category {
		case outcome.Executed:
			if sc.ErrorCode != "" {
				og.State = corev1.SessionOutgoingState_SESSION_OUTGOING_STATE_FAILED
				og.Frozen = false
				og.Expired = false
				og.ErrorCode = sc.ErrorCode
				og.ErrorText = sc.ErrorText
			} else {
				og.State = corev1.SessionOutgoingState_SESSION_OUTGOING_STATE_SENT
				og.Frozen = false
				og.Expired = false
				og.ErrorCode = ""
				og.ErrorText = nil
			}
		case outcome.NotSent, outcome.Rejected, outcome.Conflict:
			og.State = corev1.SessionOutgoingState_SESSION_OUTGOING_STATE_FAILED
			og.Frozen = false
			og.Expired = false
			og.ErrorCode = sc.ErrorCode
			og.ErrorText = sc.ErrorText
		case outcome.Unknown:
			og.State = corev1.SessionOutgoingState_SESSION_OUTGOING_STATE_UNKNOWN
			og.Frozen = sc.Frozen
			og.Expired = sc.Expired
			og.ErrorCode = sc.ErrorCode
			og.ErrorText = sc.ErrorText
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
			if sess.Meta != nil && sess.Meta.Agent != v1.Agent_AGENT_UNSPECIFIED {
				agent = sess.Meta.Agent
			}
			if k.SessionOpen.RefreshModels {
				delete(c.m.state.MachineModels, screens.MachineAgentKey{MachineID: sess.MachineID, Agent: agent})
			}
			c.m.maybeFetchMachineConfig(ctx, sess.MachineID, agent)
		}
		store.pendingDirty = true
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

		reqID, dispatched := c.m.sync.SubmitCommand(ctx, sync.SubmitCommandParams{
			SlotKey:   slotKey,
			MachineID: machineID,
			SessionID: sid,
			Command:   cmd,
		})

		store.slotMeta[slotKey] = sessionSlotMeta{
			sessionID:     sid,
			machineID:     machineID,
			kind:          kind,
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
			og := findOrAppendOutgoing(ui, outReqID, slotKey, sr, c.m.sync.NowMs())
			if dispatched {
				og.State = corev1.SessionOutgoingState_SESSION_OUTGOING_STATE_SENDING
				og.ErrorCode = ""
				og.ErrorText = nil
			}
		}
		store.pendingDirty = true
		return nil

	case *corev1.Event_SessionAbandonCommand:
		if k.SessionAbandonCommand == nil || k.SessionAbandonCommand.SlotKey == "" {
			return nil
		}
		act := k.SessionAbandonCommand
		c.m.sync.AbandonAttempt(ctx, act.SlotKey)
		if ui := store.uiBySession[act.SessionId]; ui != nil {
			delete(ui.Commands, act.SlotKey)
		}
		if meta, ok := store.slotMeta[act.SlotKey]; ok {
			if meta.kind == slotKindSend && meta.outgoingReqID != "" {
				if ui := store.uiBySession[meta.sessionID]; ui != nil {
					for _, og := range ui.Outgoing {
						if og != nil && og.RequestID == meta.outgoingReqID {
							og.Frozen = false
							og.Expired = true
							og.State = corev1.SessionOutgoingState_SESSION_OUTGOING_STATE_FAILED
						}
					}
				}
			}
			delete(store.slotMeta, act.SlotKey)
		}
		store.pendingDirty = true
		return nil

	case *corev1.Event_SessionDismissOutgoing:
		if k.SessionDismissOutgoing == nil || k.SessionDismissOutgoing.SessionId == "" {
			return nil
		}
		act := k.SessionDismissOutgoing
		ui := store.ensureUI(act.SessionId)
		slotKey := removeOutgoing(ui, act.RequestId)
		if slotKey != "" {
			c.m.sync.AbandonAttempt(ctx, slotKey)
			delete(ui.Commands, slotKey)
			delete(store.slotMeta, slotKey)
		}
		store.pendingDirty = true
		return nil

	case *corev1.Event_SessionRestoreOutgoing:
		if k.SessionRestoreOutgoing == nil || k.SessionRestoreOutgoing.SessionId == "" {
			return nil
		}
		act := k.SessionRestoreOutgoing
		ui := store.ensureUI(act.SessionId)
		og := popOutgoing(ui, act.RequestId)
		if og == nil {
			return nil
		}
		if og.SlotKey != "" {
			c.m.sync.AbandonAttempt(ctx, og.SlotKey)
			delete(ui.Commands, og.SlotKey)
			delete(store.slotMeta, og.SlotKey)
		}
		store.pendingDirty = true
		return []*corev1.Effect{
			{
				Effect: &corev1.Effect_SessionRestoreComposer{
					SessionRestoreComposer: &corev1.SessionRestoreComposerEffect{
						SessionId: act.SessionId,
						RequestId: og.RequestID,
						Text:      og.Text,
						ImageIds:  append([]string(nil), og.ImageIDs...),
						FileIds:   append([]string(nil), og.FileIDs...),
					},
				},
			},
		}

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
	}
	return nil
}

func (c *Core) appendSessionRenderTarget(dirty sync.DirtyFlags, effects []*corev1.Effect) []*corev1.Effect {
	store := c.m.ensureSessionStore()
	wantSession := store.pendingDirty || (len(store.opened) > 0 && (dirty.SessionData || dirty.Home))
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
		return slotKindCancelInput, "", 0, ""
	case *v1.Command_ResumeInput:
		return slotKindResumeInput, "", 0, ""
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

func fillObservedPreconditions(cmd *v1.Command, snap screens.SessionViewInput, ui *screens.SessionUIState) {
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
		State:     corev1.SessionOutgoingState_SESSION_OUTGOING_STATE_SENDING,
		CreatedMs: nowMs,
	}
	ui.Outgoing = append(ui.Outgoing, og)
	return og
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
