package app

import (
	"testing"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/screens"
	"github.com/Lewin671/remote-agent/clientcore/sync"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

func bootstrapTrayApp(t *testing.T, agent v1.Agent, status v1.SessionStatus, running bool) (*Core, string) {
	t.Helper()
	const (
		machID = "m-on"
		sessID = "s-tray"
	)
	machines := []*corev1.PlainMachine{
		{
			MachineId: machID,
			Online:    true,
			Meta: &v1.MachineMeta{
				Hostname:        "MacStudio",
				ProtocolVersion: sync.ProtocolVersion,
				Capabilities: &v1.MachineCapabilities{
					ImageUpload:         true,
					MaxImagesPerMessage: 6,
					FileUpload:          true,
				},
				Agents: []*v1.AgentInfo{
					{Agent: agent, DefaultMode: v1.ApprovalMode_APPROVAL_MODE_ASK},
				},
			},
		},
	}
	sessions := []*corev1.PlainSession{
		{
			SessionId: sessID,
			MachineId: machID,
			Meta: &v1.Session{
				Id:     sessID,
				Cwd:    "/Users/me/proj",
				Agent:  agent,
				Status: status,
				Title:  "Tray Test",
			},
			Checkpoint: &v1.LiveSnapshot{
				Running: running,
			},
		},
	}
	c := bootstrapReadyApp(t, machines, sessions)
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_050,
		Event: &corev1.Event_SessionOpen{SessionOpen: &corev1.SessionOpenAction{SessionId: sessID}},
	})
	watchBatch := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_060,
		Event: &corev1.Event_SessionWatch{SessionWatch: &corev1.SessionWatchEvent{SessionId: sessID, Watched: true}},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_070,
		Event: &corev1.Event_CacheEventsLoaded{
			CacheEventsLoaded: &corev1.CacheEventsLoadedEvent{
				OpId:      watchCacheReadOpID(watchBatch),
				SessionId: sessID,
				Events: []*corev1.PlainEvent{
					{
						Seq:      1,
						Verified: true,
						Event: &v1.Event{
							Seq:  1,
							Kind: &v1.Event_UserMessage{UserMessage: &v1.UserMessage{Text: "initial task"}},
						},
					},
				},
			},
		},
	})
	return c, sessID
}

type dispatchedCmd struct {
	reqID string
	attID string
	cmd   *v1.Command
}

func extractCommands(batch *corev1.EffectBatch) []dispatchedCmd {
	var out []dispatchedCmd
	for _, eff := range batch.GetEffects() {
		if dc := eff.GetDispatchCommand(); dc != nil {
			var cmd v1.Command
			if err := cmd.Unmarshal(dc.CommandBytes); err == nil {
				out = append(out, dispatchedCmd{reqID: dc.RequestId, attID: dc.AttemptId, cmd: &cmd})
			}
		}
	}
	return out
}

func extractSendCommands(batch *corev1.EffectBatch) []dispatchedCmd {
	var out []dispatchedCmd
	for _, dc := range extractCommands(batch) {
		if dc.cmd.GetSend() != nil {
			out = append(out, dc)
		}
	}
	return out
}

func extractCancelCommands(batch *corev1.EffectBatch) []dispatchedCmd {
	var out []dispatchedCmd
	for _, dc := range extractCommands(batch) {
		if dc.cmd.GetCancelInput() != nil {
			out = append(out, dc)
		}
	}
	return out
}

func findScheduleTimer(batch *corev1.EffectBatch, kind corev1.TimerKind) *corev1.ScheduleTimerEffect {
	for _, eff := range batch.GetEffects() {
		if st := eff.GetScheduleTimer(); st != nil && st.Kind == kind {
			return st
		}
	}
	return nil
}

func hasCancelTimer(batch *corev1.EffectBatch, kind corev1.TimerKind) bool {
	for _, eff := range batch.GetEffects() {
		if ct := eff.GetCancelTimer(); ct != nil && ct.Kind == kind {
			return true
		}
	}
	return false
}

func dispatchExecutedOutcome(c *Core, nowMs int64, dc dispatchedCmd, errCode string) *corev1.EffectBatch {
	res := &v1.CommandResult{Outcome: "executed", Reason: "ok", ResultAvailable: true}
	if errCode != "" {
		res.ErrorCode = errCode
		res.ErrorMessage = errCode
	}
	return c.Dispatch(&corev1.Event{
		NowMs: nowMs,
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:         dc.reqID,
				AttemptId:         dc.attID,
				Source:            corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON,
				Signed:            true,
				HashesMatch:       true,
				AttemptBound:      true,
				OuterMatchesInner: true,
				OuterOutcome:      res.Outcome,
				OuterReason:       res.Reason,
				Result:            res,
			},
		},
	})
}

func appendStreamEvents(c *Core, sessID string, nowMs int64, events []*v1.Event) {
	pe := make([]*corev1.PlainEvent, 0, len(events))
	for _, ev := range events {
		pe = append(pe, &corev1.PlainEvent{Seq: ev.Seq, Verified: true, Event: ev})
	}
	c.Dispatch(&corev1.Event{
		NowMs: nowMs,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: 1,
				Frame: &corev1.StreamFrameEvent_EventsAppended{
					EventsAppended: &corev1.EventsAppendedFrame{
						SessionId: sessID,
						Events:    pe,
					},
				},
			},
		},
	})
}

func viewSession(t *testing.T, c *Core, sessID string) *corev1.SessionViewModel {
	t.Helper()
	vm, err := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: sessID}}})
	if err != nil {
		t.Fatalf("View(%s): %v", sessID, err)
	}
	return vm.GetSession()
}

func TestPendingTrayAllSevenGroupsOrderingHeadersAndNumbering(t *testing.T) {
	baseSession := func(status v1.SessionStatus) *lists.Session {
		return &lists.Session{
			ID:        "s1",
			MachineID: "m1",
			Info:      &v1.Session{Id: "s1", Agent: v1.Agent_AGENT_CODEX, Status: status},
		}
	}
	ui := screens.NewSessionUIState()
	ui.PendingStartPrompt = &screens.PendingStartPrompt{
		RequestID: "start-req",
		Text:      "bootstrapping task",
		CreatedMs: 1000,
	}
	// Tag unrelated terminal inputs with another request ID so FindStartPromptInput doesn't mistake them for PendingStartPrompt.
	otherOrigin := &v1.Origin{Kind: v1.OriginKind_ORIGIN_KIND_CLIENT, DeviceId: "dev-self", RequestId: "other-req"}
	in := screens.SessionViewInput{
		Found:        true,
		Session:      baseSession(v1.SessionStatus_SESSION_STATUS_RUNNING),
		ThisDeviceID: "dev-self",
		DeviceNames:  map[string]string{"dev-ipad": "iPad"},
		Conn:         lists.ConnConnected,
		Machine: &lists.Machine{
			ID:     "m1",
			Online: true,
			Meta: &v1.MachineMeta{
				Hostname:        "Mac",
				ProtocolVersion: sync.ProtocolVersion,
			},
		},
		Projection: &corev1.ProjectionState{
			Running: true,
			PendingInputs: []*corev1.PendingInput{
				{RequestId: "p1", Text: "paused one", Mode: v1.SendMode_SEND_MODE_QUEUE, Paused: true},
				{RequestId: "p2", Text: "paused two", Mode: v1.SendMode_SEND_MODE_QUEUE, Paused: true},
				{RequestId: "q1", Text: "queued one", Mode: v1.SendMode_SEND_MODE_QUEUE},
				{RequestId: "q2", Text: "queued two", Mode: v1.SendMode_SEND_MODE_QUEUE},
				{RequestId: "s1", Text: "steer one", Mode: v1.SendMode_SEND_MODE_STEER},
			},
			Inputs: []*corev1.InputProjection{
				{
					RequestId: "f1",
					Status:    v1.InputStatus_INPUT_STATUS_FAILED,
					Accepted:  &corev1.AcceptedInput{Origin: otherOrigin},
					Detail:    &v1.InputState{RequestId: "f1", Status: v1.InputStatus_INPUT_STATUS_FAILED, Reason: "image_not_recognized", Text: "bad img"},
				},
				{
					RequestId: "u1",
					Status:    v1.InputStatus_INPUT_STATUS_UNCONFIRMED,
					Accepted:  &corev1.AcceptedInput{Origin: otherOrigin},
					Detail:    &v1.InputState{RequestId: "u1", Status: v1.InputStatus_INPUT_STATUS_UNCONFIRMED, Text: "maybe sent"},
				},
			},
		},
	}

	st := screens.NewState()
	st.Cal.NowMs = 1_791_078_060_000
	in.Machine.LastSeenMs = st.Cal.NowMs

	vm := screens.BuildSessionView(in, st, &corev1.SessionScreenRequest{SessionId: "s1"}, ui)
	tray := vm.GetComposer().GetPendingTray()
	// Groups expected here: FAILED, UNSURE, WAIT_READY, STEER, QUEUE, PAUSED (WAIT_ANSWER below).
	wantOrder := []corev1.SessionTrayGroupKind{
		corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_FAILED,
		corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_UNSURE,
		corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_WAIT_READY,
		corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_STEER,
		corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_QUEUE,
		corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_PAUSED,
	}
	if len(tray.GetGroups()) != len(wantOrder) {
		t.Fatalf("expected %d groups, got %d: %+v", len(wantOrder), len(tray.GetGroups()), tray.GetGroups())
	}
	for i, kind := range wantOrder {
		if tray.GetGroups()[i].GetKind() != kind {
			t.Fatalf("group[%d] expected %v, got %v", i, kind, tray.GetGroups()[i].GetKind())
		}
	}
	// Check tones, counts, single vs plural headers, cancel_all, and action flags.
	gFailed := tray.GetGroups()[0]
	if gFailed.GetTone() != corev1.SessionTrayTone_SESSION_TRAY_TONE_RED || gFailed.GetHeaderText().GetKey() != text.KeyQueueGroupFailed {
		t.Fatalf("unexpected failed group: %+v", gFailed)
	}
	if fItem := gFailed.GetItems()[0]; fItem.GetReasonText().GetKey() != text.KeyComposerImagesUnrecognized || !fItem.GetCanRetry() || !fItem.GetCanCancel() || !fItem.GetCanRestore() {
		t.Fatalf("unexpected failed item: %+v", fItem)
	}
	gUnsure := tray.GetGroups()[1]
	if gUnsure.GetTone() != corev1.SessionTrayTone_SESSION_TRAY_TONE_ORANGE || gUnsure.GetItems()[0].GetCanCancel() || !gUnsure.GetItems()[0].GetCanOpenTerminal() || !gUnsure.GetItems()[0].GetCanIgnore() {
		t.Fatalf("unexpected unsure item: %+v", gUnsure)
	}
	gSteer := tray.GetGroups()[3]
	if !gSteer.GetItems()[0].GetShowSpinner() || gSteer.GetItems()[0].GetCanPromote() || !gSteer.GetItems()[0].GetCanRestore() {
		t.Fatalf("unexpected steer item: %+v", gSteer.GetItems()[0])
	}
	gQueue := tray.GetGroups()[4]
	if gQueue.GetHeaderText().GetKey() != text.KeyQueueGroupCount || gQueue.GetCanCancelAll() {
		t.Fatalf("unexpected queue header: %+v", gQueue)
	}
	if gQueue.GetItems()[0].GetQueueNumber() != 1 || gQueue.GetItems()[1].GetQueueNumber() != 2 || !gQueue.GetItems()[0].GetCanPromote() {
		t.Fatalf("unexpected queue items: %+v", gQueue.GetItems())
	}
	gPaused := tray.GetGroups()[5]
	if !gPaused.GetCanCancelAll() || !gPaused.GetItems()[0].GetCanResume() || !gPaused.GetItems()[0].GetCanCancel() {
		t.Fatalf("unexpected paused group: %+v", gPaused)
	}
	if tray.GetTotalCount() != 8 || tray.GetFailedCount() != 2 {
		t.Fatalf("expected total=8 failed=2, got total=%d failed=%d", tray.GetTotalCount(), tray.GetFailedCount())
	}

	// WAIT_ANSWER + composer placeholder check when approval is active.
	ui.PendingStartPrompt = nil
	in.Projection.Approvals = []*corev1.PendingApproval{{ApprovalId: "ap-1", Request: &v1.ApprovalRequested{ApprovalId: "ap-1", ToolName: "Bash", Command: "rm -rf"}}}
	vmAsk := screens.BuildSessionView(in, st, &corev1.SessionScreenRequest{SessionId: "s1"}, ui)
	if vmAsk.GetComposer().GetPlaceholder().GetKey() != text.KeyComposerAnswerFirstPlaceholder {
		t.Fatalf("expected answer_first_placeholder, got %+v", vmAsk.GetComposer().GetPlaceholder())
	}
	var foundWaitAnswer bool
	for _, g := range vmAsk.GetComposer().GetPendingTray().GetGroups() {
		if g.GetKind() == corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_WAIT_ANSWER {
			foundWaitAnswer = true
			if g.GetItems()[0].GetRequestId() != "s1" {
				t.Fatalf("expected steer item in WAIT_ANSWER, got %+v", g.GetItems())
			}
		}
	}
	if !foundWaitAnswer {
		t.Fatalf("expected WAIT_ANSWER group when approval is pending")
	}

	in.Projection.Inputs[0].Status = v1.InputStatus_INPUT_STATUS_NEEDS_TERMINAL
	vmUnsure := screens.BuildSessionView(in, st, &corev1.SessionScreenRequest{SessionId: "s1"}, ui)
	var foundNeedsTerm bool
	for _, it := range vmUnsure.GetComposer().GetPendingTray().GetOrderedItems() {
		if it.GetRequestId() == "f1" {
			foundNeedsTerm = true
			if it.GetGroup() != corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_UNSURE ||
				it.GetTone() != corev1.SessionTrayTone_SESSION_TRAY_TONE_ORANGE ||
				!it.GetCanOpenTerminal() || !it.GetCanIgnore() || it.GetCanCancel() || it.GetCanRetry() {
				t.Fatalf("expected NEEDS_TERMINAL in UNSURE orange without cancel/retry, got %+v", it)
			}
		}
	}
	if !foundNeedsTerm {
		t.Fatalf("expected NEEDS_TERMINAL item f1 in pending tray")
	}
}

func TestCodexSteerVisibilityGraceAndNonCodexImmediate(t *testing.T) {
	c, sessID := bootstrapTrayApp(t, v1.Agent_AGENT_CODEX, v1.SessionStatus_SESSION_STATUS_RUNNING, true)

	// Send steer at t=1_791_078_060_100: must stay hidden until t+1000ms.
	sendBatch := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_100,
		Event: &corev1.Event_SessionSubmitCommand{
			SessionSubmitCommand: &corev1.SessionSubmitCommandAction{
				SessionId: sessID,
				Command: &v1.Command{
					Kind: &v1.Command_Send{
						Send: &v1.SendRequest{SessionId: sessID, RequestId: "steer-fast", Text: "focus auth", Mode: v1.SendMode_SEND_MODE_STEER},
					},
				},
			},
		},
	})
	graceTimer := findScheduleTimer(sendBatch, corev1.TimerKind_TIMER_KIND_SESSION_STEER_GRACE)
	if graceTimer == nil || graceTimer.DelayMs != 1000 {
		t.Fatalf("expected 1000ms steer grace timer, got %+v", graceTimer)
	}
	dcs := extractSendCommands(sendBatch)
	if len(dcs) != 1 {
		t.Fatalf("expected 1 Send command, got %d", len(dcs))
	}
	dispatchExecutedOutcome(c, 1_791_078_060_120, dcs[0], "")
	appendStreamEvents(c, sessID, 1_791_078_060_130, []*v1.Event{
		{
			Seq: 2,
			Kind: &v1.Event_InputState{
				InputState: &v1.InputState{RequestId: "steer-fast", Status: v1.InputStatus_INPUT_STATUS_QUEUED, Text: "focus auth", Mode: v1.SendMode_SEND_MODE_STEER},
			},
		},
	})

	// Before 1000ms elapses, Codex steer is hidden from tray to avoid flash.
	vmBefore := viewSession(t, c, sessID)
	if vmBefore.GetComposer().GetPlaceholder().GetKey() != text.KeyComposerRunningPlaceholder {
		t.Fatalf("expected running_placeholder, got %+v", vmBefore.GetComposer().GetPlaceholder())
	}
	if vmBefore.GetComposer().GetPendingTray().GetTotalCount() != 0 {
		t.Fatalf("expected Codex steer hidden during 1s grace window, got %+v", vmBefore.GetComposer().GetPendingTray())
	}

	// Send QUEUE item on Codex: appears immediately without 1s delay.
	qBatch := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_200,
		Event: &corev1.Event_SessionSubmitCommand{
			SessionSubmitCommand: &corev1.SessionSubmitCommandAction{
				SessionId: sessID,
				Command: &v1.Command{
					Kind: &v1.Command_Send{
						Send: &v1.SendRequest{SessionId: sessID, RequestId: "q-immed", Text: "afterwards", Mode: v1.SendMode_SEND_MODE_QUEUE},
					},
				},
			},
		},
	})
	if findScheduleTimer(qBatch, corev1.TimerKind_TIMER_KIND_SESSION_STEER_GRACE) != nil {
		t.Fatalf("did not expect steer grace timer for QUEUE mode")
	}
	if viewSession(t, c, sessID).GetComposer().GetPendingTray().GetTotalCount() != 1 {
		t.Fatalf("expected QUEUE item visible immediately")
	}

	// Timer fires at t+1000ms -> Codex steer becomes visible in STEER group.
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_061_100,
		Event: &corev1.Event_TimerFired{
			TimerFired: &corev1.TimerFiredEvent{
				TimerId: graceTimer.TimerId,
				Kind:    corev1.TimerKind_TIMER_KIND_SESSION_STEER_GRACE,
				Key:     sessID,
			},
		},
	})
	vmAfter := viewSession(t, c, sessID)
	if vmAfter.GetComposer().GetPendingTray().GetTotalCount() != 2 {
		t.Fatalf("expected both STEER and QUEUE visible after grace timer, got %+v", vmAfter.GetComposer().GetPendingTray())
	}
	if vmAfter.GetComposer().GetPendingTray().GetGroups()[0].GetKind() != corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_STEER {
		t.Fatalf("expected STEER group first, got %+v", vmAfter.GetComposer().GetPendingTray().GetGroups())
	}
}

func TestPromoteTrayItemThreeOutcomes(t *testing.T) {
	// Test all 3 promote outcomes: (1) cancel fails, (2) cancel+resend succeed, (3) cancel succeeds + resend fails.
	c, sessID := bootstrapTrayApp(t, v1.Agent_AGENT_CLAUDE, v1.SessionStatus_SESSION_STATUS_RUNNING, true)

	// Submit queued text message (t-id-ab / etc. will come from entropy).
	sBatch := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_100,
		Event: &corev1.Event_SessionSubmitCommand{
			SessionSubmitCommand: &corev1.SessionSubmitCommandAction{
				SessionId: sessID,
				Command: &v1.Command{
					Kind: &v1.Command_Send{
						Send: &v1.SendRequest{SessionId: sessID, RequestId: "q-orig", Text: "promote me", Mode: v1.SendMode_SEND_MODE_QUEUE},
					},
				},
			},
		},
	})
	dcs := extractSendCommands(sBatch)
	dispatchExecutedOutcome(c, 1_791_078_060_110, dcs[0], "")
	appendStreamEvents(c, sessID, 1_791_078_060_120, []*v1.Event{
		{
			Seq: 2,
			Kind: &v1.Event_InputState{
				InputState: &v1.InputState{RequestId: "q-orig", Status: v1.InputStatus_INPUT_STATUS_QUEUED, Text: "promote me", Mode: v1.SendMode_SEND_MODE_QUEUE},
			},
		},
	})

	// 1. Promote -> CancelInput FAILS: item remains in QUEUE with cancel_failed reason, NO Send dispatched.
	p1 := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_200,
		Event: &corev1.Event_SessionPromoteTrayItem{
			SessionPromoteTrayItem: &corev1.SessionTrayItemAction{SessionId: sessID, RequestId: "q-orig"},
		},
	})
	cancelCmds1 := extractCancelCommands(p1)
	if len(cancelCmds1) != 1 {
		t.Fatalf("expected 1 CancelInput command on promote, got %+v", cancelCmds1)
	}
	// While canceling, row activity is CANCELING and actions disabled.
	vmCanceling := viewSession(t, c, sessID)
	rowC := vmCanceling.GetComposer().GetPendingTray().GetOrderedItems()[0]
	if rowC.GetActivity() != corev1.SessionTrayRowActivity_SESSION_TRAY_ROW_ACTIVITY_CANCELING || rowC.GetCanPromote() || rowC.GetCanCancel() {
		t.Fatalf("expected CANCELING row with locked actions, got %+v", rowC)
	}
	failCancelBatch := dispatchExecutedOutcome(c, 1_791_078_060_210, cancelCmds1[0], "already_submitted")
	if cmds := extractSendCommands(failCancelBatch); len(cmds) != 0 {
		t.Fatalf("must NOT resend when CancelInput fails, got %+v", cmds)
	}
	rowFail := viewSession(t, c, sessID).GetComposer().GetPendingTray().GetOrderedItems()[0]
	if rowFail.GetGroup() != corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_QUEUE || rowFail.GetReasonText().GetKey() != text.KeyQueueCancelFailed || !rowFail.GetCanPromote() {
		t.Fatalf("expected QUEUE row with cancel_failed and promotable again, got %+v", rowFail)
	}

	// 2 & 3. Promote -> CancelInput SUCCEEDS -> dispatches STEER Send; then test Send failing -> lands in FAILED.
	p2 := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_300,
		Event: &corev1.Event_SessionPromoteTrayItem{
			SessionPromoteTrayItem: &corev1.SessionTrayItemAction{SessionId: sessID, RequestId: "q-orig"},
		},
	})
	cancelCmds2 := extractCancelCommands(p2)
	okCancelBatch := dispatchExecutedOutcome(c, 1_791_078_060_310, cancelCmds2[0], "")
	resendCmds := extractSendCommands(okCancelBatch)
	if len(resendCmds) != 1 {
		t.Fatalf("expected 1 STEER Send after CancelInput succeeds, got %+v", resendCmds)
	}
	sr := resendCmds[0].cmd.GetSend()
	if sr.GetMode() != v1.SendMode_SEND_MODE_STEER || sr.GetText() != "promote me" {
		t.Fatalf("unexpected promoted Send payload: %+v", sr)
	}
	// While resend is in flight, row is in STEER with SENDING activity.
	rowSending := viewSession(t, c, sessID).GetComposer().GetPendingTray().GetOrderedItems()[0]
	if rowSending.GetGroup() != corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_STEER || rowSending.GetActivity() != corev1.SessionTrayRowActivity_SESSION_TRAY_ROW_ACTIVITY_SENDING {
		t.Fatalf("expected STEER SENDING row after promote, got %+v", rowSending)
	}

	// Case 3 sub-check: promoted Send fails -> lands in FAILED with retry & restore.
	dispatchExecutedOutcome(c, 1_791_078_060_320, resendCmds[0], "write_failed")
	rowFailed := viewSession(t, c, sessID).GetComposer().GetPendingTray().GetOrderedItems()[0]
	if rowFailed.GetGroup() != corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_FAILED ||
		!rowFailed.GetCanRetry() || !rowFailed.GetCanRestore() ||
		rowFailed.GetReasonText().GetKey() != text.KeyComposerWriteFailed {
		t.Fatalf("expected FAILED retriable row after promoted send fails, got %+v", rowFailed)
	}
}

func TestRestoreCancelAllUndoTimerAndAttachmentRetention(t *testing.T) {
	c, sessID := bootstrapTrayApp(t, v1.Agent_AGENT_CLAUDE, v1.SessionStatus_SESSION_STATUS_RUNNING, true)

	// Send one message WITH attachments and one text-only paused message.
	b1 := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_100,
		Event: &corev1.Event_SessionSubmitCommand{
			SessionSubmitCommand: &corev1.SessionSubmitCommandAction{
				SessionId: sessID,
				Command: &v1.Command{
					Kind: &v1.Command_Send{
						Send: &v1.SendRequest{
							SessionId: sessID,
							RequestId: "att-1",
							Text:      "check screenshot",
							Mode:      v1.SendMode_SEND_MODE_QUEUE,
							ImageIds:  []string{"img-1"},
							FileIds:   []string{"file-1"},
						},
					},
				},
			},
		},
	})
	dispatchExecutedOutcome(c, 1_791_078_060_110, extractSendCommands(b1)[0], "")
	b2 := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_120,
		Event: &corev1.Event_SessionSubmitCommand{
			SessionSubmitCommand: &corev1.SessionSubmitCommandAction{
				SessionId: sessID,
				Command: &v1.Command{
					Kind: &v1.Command_Send{
						Send: &v1.SendRequest{SessionId: sessID, RequestId: "txt-2", Text: "second paused", Mode: v1.SendMode_SEND_MODE_QUEUE},
					},
				},
			},
		},
	})
	dispatchExecutedOutcome(c, 1_791_078_060_130, extractSendCommands(b2)[0], "")

	appendStreamEvents(c, sessID, 1_791_078_060_140, []*v1.Event{
		{
			Seq: 2,
			Kind: &v1.Event_InputState{
				InputState: &v1.InputState{RequestId: "att-1", Status: v1.InputStatus_INPUT_STATUS_QUEUED, Text: "check screenshot", Mode: v1.SendMode_SEND_MODE_QUEUE, ImageIds: []string{"img-1"}, Paused: true},
			},
		},
		{
			Seq: 3,
			Kind: &v1.Event_InputState{
				InputState: &v1.InputState{RequestId: "txt-2", Status: v1.InputStatus_INPUT_STATUS_QUEUED, Text: "second paused", Mode: v1.SendMode_SEND_MODE_QUEUE, Paused: true},
			},
		},
	})

	// CancelAllPaused -> cancels both; when both succeed, Undo bar shows count=2 and att-1 remains retained.
	cancelAllBatch := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_200,
		Event: &corev1.Event_SessionCancelAllPaused{
			SessionCancelAllPaused: &corev1.SessionTraySessionAction{SessionId: sessID},
		},
	})
	cCmds := extractCancelCommands(cancelAllBatch)
	if len(cCmds) != 2 {
		t.Fatalf("expected 2 CancelInput commands for CancelAllPaused, got %d", len(cCmds))
	}
	u1Batch := dispatchExecutedOutcome(c, 1_791_078_060_210, cCmds[0], "")
	if findScheduleTimer(u1Batch, corev1.TimerKind_TIMER_KIND_SESSION_UNDO) == nil {
		t.Fatalf("expected 5s TIMER_KIND_SESSION_UNDO scheduled")
	}
	u2Batch := dispatchExecutedOutcome(c, 1_791_078_060_220, cCmds[1], "")
	undoTimer := findScheduleTimer(u2Batch, corev1.TimerKind_TIMER_KIND_SESSION_UNDO)
	if undoTimer == nil || undoTimer.DelayMs != 5000 {
		t.Fatalf("expected 5000ms undo timer reset, got %+v", undoTimer)
	}
	// Stream reports them canceled.
	appendStreamEvents(c, sessID, 1_791_078_060_230, []*v1.Event{
		{Seq: 4, Kind: &v1.Event_InputState{InputState: &v1.InputState{RequestId: "att-1", Status: v1.InputStatus_INPUT_STATUS_CANCELED}}},
		{Seq: 5, Kind: &v1.Event_InputState{InputState: &v1.InputState{RequestId: "txt-2", Status: v1.InputStatus_INPUT_STATUS_CANCELED}}},
	})

	vmUndo := viewSession(t, c, sessID)
	if len(vmUndo.GetTimeline().GetItems()) != 1 {
		t.Fatalf("non-exited cancellations must NOT add timeline notices, got %+v", vmUndo.GetTimeline().GetItems())
	}
	trayUndo := vmUndo.GetComposer().GetPendingTray()
	if !trayUndo.GetUndo().GetVisible() || trayUndo.GetUndo().GetCount() != 2 || trayUndo.GetUndo().GetLabel().GetKey() != text.KeyQueueCanceledCount {
		t.Fatalf("expected undo bar with count=2, got %+v", trayUndo.GetUndo())
	}
	if ids := vmUndo.GetComposer().GetRetainedAttachmentRequestIds(); len(ids) != 1 || ids[0] != "att-1" {
		t.Fatalf("expected [att-1] retained during 5s undo window, got %+v", ids)
	}

	// UndoCancel -> emits SessionResendAttachmentsEffect for att-1 AND direct QUEUE Send for txt-2, plus keeps att-1 retained until new Send submits!
	undoBatch := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_061_000,
		Event: &corev1.Event_SessionUndoCancel{
			SessionUndoCancel: &corev1.SessionTraySessionAction{SessionId: sessID},
		},
	})
	if !hasCancelTimer(undoBatch, corev1.TimerKind_TIMER_KIND_SESSION_UNDO) {
		t.Fatalf("expected TIMER_KIND_SESSION_UNDO canceled on undo")
	}
	var resendEff *corev1.SessionResendAttachmentsEffect
	for _, eff := range undoBatch.GetEffects() {
		if r := eff.GetSessionResendAttachments(); r != nil {
			resendEff = r
		}
	}
	if resendEff == nil || resendEff.GetSourceRequestId() != "att-1" || resendEff.GetMode() != v1.SendMode_SEND_MODE_QUEUE || len(resendEff.GetImageIds()) != 1 || len(resendEff.GetFileIds()) != 1 {
		t.Fatalf("unexpected SessionResendAttachmentsEffect on undo: %+v", resendEff)
	}
	undoSendCmds := extractSendCommands(undoBatch)
	if len(undoSendCmds) != 1 || undoSendCmds[0].cmd.GetSend().GetText() != "second paused" || undoSendCmds[0].cmd.GetSend().GetMode() != v1.SendMode_SEND_MODE_QUEUE {
		t.Fatalf("expected direct QUEUE Send for text-only undo item, got %+v", undoSendCmds)
	}
	// att-1 MUST still remain in retainedAttachmentRequestIds while shell re-uploads resendEff.NewRequestId!
	if ids := viewSession(t, c, sessID).GetComposer().GetRetainedAttachmentRequestIds(); len(ids) != 1 || ids[0] != "att-1" {
		t.Fatalf("expected att-1 still retained until shell submits newReqID %q, got %+v", resendEff.GetNewRequestId(), ids)
	}

	// Shell submits resendEff.NewRequestId -> att-1 drops, newReqID is retained until UserMessage, and Undo bar can re-trigger & expire.
	reSendBatch := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_061_100,
		Event: &corev1.Event_SessionSubmitCommand{
			SessionSubmitCommand: &corev1.SessionSubmitCommandAction{
				SessionId: sessID,
				Command: &v1.Command{
					Kind: &v1.Command_Send{
						Send: &v1.SendRequest{
							SessionId: sessID,
							RequestId: resendEff.GetNewRequestId(),
							Text:      resendEff.GetText(),
							Mode:      resendEff.GetMode(),
							ImageIds:  []string{"img-2"},
							FileIds:   []string{"file-2"},
						},
					},
				},
			},
		},
	})
	dispatchExecutedOutcome(c, 1_791_078_061_110, extractSendCommands(reSendBatch)[0], "")
	if ids := viewSession(t, c, sessID).GetComposer().GetRetainedAttachmentRequestIds(); len(ids) != 1 || ids[0] != resendEff.GetNewRequestId() {
		t.Fatalf("expected retention transferred to %q, got %+v", resendEff.GetNewRequestId(), ids)
	}

	// Cancel txt-2 (from QUEUE/Sending -> mock stream QUEUED then cancel) and test 5s Undo timer expiry clears Undo + retention!
	appendStreamEvents(c, sessID, 1_791_078_061_120, []*v1.Event{
		{Seq: 6, Kind: &v1.Event_InputState{InputState: &v1.InputState{RequestId: resendEff.GetNewRequestId(), Status: v1.InputStatus_INPUT_STATUS_QUEUED, Text: "check screenshot", Mode: v1.SendMode_SEND_MODE_QUEUE, ImageIds: []string{"img-2"}}}},
	})
	restoreFailBatch := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_061_150,
		Event: &corev1.Event_SessionRestoreTrayItem{
			SessionRestoreTrayItem: &corev1.SessionTrayItemAction{SessionId: sessID, RequestId: resendEff.GetNewRequestId()},
		},
	})
	rCmd := extractCancelCommands(restoreFailBatch)[0]
	// Cancel fails -> NO SessionRestoreComposerEffect emitted!
	if effs := dispatchExecutedOutcome(c, 1_791_078_061_160, rCmd, "busy").GetEffects(); hasRestoreEffect(effs) {
		t.Fatalf("must NOT restore composer when CancelInput fails")
	}
	// Restore retry succeeds -> emits SessionRestoreComposerEffect.
	restoreOkBatch := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_061_200,
		Event: &corev1.Event_SessionRestoreTrayItem{
			SessionRestoreTrayItem: &corev1.SessionTrayItemAction{SessionId: sessID, RequestId: resendEff.GetNewRequestId()},
		},
	})
	restoreSettled := dispatchExecutedOutcome(c, 1_791_078_061_210, extractCancelCommands(restoreOkBatch)[0], "")
	if !hasRestoreEffect(restoreSettled.GetEffects()) {
		t.Fatalf("expected SessionRestoreComposerEffect after CancelInput succeeded")
	}

	// Cancel another item then fire 5s TIMER_KIND_SESSION_UNDO -> Undo disappears.
	cFail := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_061_300,
		Event: &corev1.Event_SessionSubmitCommand{
			SessionSubmitCommand: &corev1.SessionSubmitCommandAction{
				SessionId: sessID,
				Command: &v1.Command{
					Kind: &v1.Command_Send{
						Send: &v1.SendRequest{SessionId: sessID, RequestId: "fail-att", Text: "with img", Mode: v1.SendMode_SEND_MODE_QUEUE, ImageIds: []string{"img-x"}},
					},
				},
			},
		},
	})
	dispatchExecutedOutcome(c, 1_791_078_061_310, extractSendCommands(cFail)[0], "write_failed")
	cancelLocalBatch := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_061_320,
		Event: &corev1.Event_SessionCancelTrayItem{
			SessionCancelTrayItem: &corev1.SessionTrayItemAction{SessionId: sessID, RequestId: "fail-att"},
		},
	})
	undoExpTimer := findScheduleTimer(cancelLocalBatch, corev1.TimerKind_TIMER_KIND_SESSION_UNDO)
	if undoExpTimer == nil || !viewSession(t, c, sessID).GetComposer().GetPendingTray().GetUndo().GetVisible() {
		t.Fatalf("expected undo visible after dismissing local failed item")
	}
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_066_320,
		Event: &corev1.Event_TimerFired{
			TimerFired: &corev1.TimerFiredEvent{TimerId: undoExpTimer.TimerId, Kind: corev1.TimerKind_TIMER_KIND_SESSION_UNDO, Key: sessID},
		},
	})
	vmExp := viewSession(t, c, sessID)
	if vmExp.GetComposer().GetPendingTray().GetUndo().GetVisible() {
		t.Fatalf("expected undo hidden after 5s timer")
	}
	if len(vmExp.GetComposer().GetRetainedAttachmentRequestIds()) != 0 {
		t.Fatalf("expected retained attachments cleared after undo expires, got %+v", vmExp.GetComposer().GetRetainedAttachmentRequestIds())
	}
}

func TestForeignScheduledOfflineAndAntigravityConstraints(t *testing.T) {
	st := screens.NewState()
	st.Cal.NowMs = 1_791_078_060_000
	ls := &lists.Session{
		ID:        "s-misc",
		MachineID: "m1",
		Info:      &v1.Session{Id: "s-misc", Agent: v1.Agent_AGENT_ANTIGRAVITY, Status: v1.SessionStatus_SESSION_STATUS_RUNNING},
	}
	in := screens.SessionViewInput{
		Found:        true,
		Session:      ls,
		ThisDeviceID: "dev-self",
		Conn:         lists.ConnOffline, // offline!
		Machine:      &lists.Machine{ID: "m1", Online: false, Meta: &v1.MachineMeta{Hostname: "Mac"}},
		Projection: &corev1.ProjectionState{
			Running: true,
			PendingInputs: []*corev1.PendingInput{
				{
					RequestId: "foreign-redacted",
					Text:      "",
					Mode:      v1.SendMode_SEND_MODE_QUEUE,
					Origin:    &v1.Origin{Kind: v1.OriginKind_ORIGIN_KIND_CLIENT, DeviceId: "dev-ipad"},
				},
				{
					RequestId: "foreign-att",
					Text:      "see attachment",
					Mode:      v1.SendMode_SEND_MODE_QUEUE,
					Origin:    &v1.Origin{Kind: v1.OriginKind_ORIGIN_KIND_CLIENT, DeviceId: "dev-ipad"},
					Files:     []*v1.SentFile{{FileId: "f1", Name: "a.png"}},
				},
				{
					RequestId: "sched-1",
					Text:      "nightly check",
					Mode:      v1.SendMode_SEND_MODE_QUEUE,
					Origin:    &v1.Origin{Kind: v1.OriginKind_ORIGIN_KIND_SYSTEM, ScheduledTaskId: "task-1"},
				},
			},
			Inputs: []*corev1.InputProjection{
				{
					RequestId: "sched-1",
					Status:    v1.InputStatus_INPUT_STATUS_QUEUED,
					Accepted:  &corev1.AcceptedInput{Text: "nightly check", TimeMs: 1_791_078_060_000, Origin: &v1.Origin{Kind: v1.OriginKind_ORIGIN_KIND_SYSTEM, ScheduledTaskId: "task-1"}},
				},
			},
		},
	}

	vm := screens.BuildSessionView(in, st, &corev1.SessionScreenRequest{SessionId: "s-misc"}, screens.NewSessionUIState())
	comp := vm.GetComposer()
	if comp.GetSupportsQueueMode() {
		t.Fatalf("expected supports_queue_mode=false for Antigravity")
	}
	tray := comp.GetPendingTray()
	if !tray.GetOffline() || len(tray.GetOrderedItems()) != 3 {
		t.Fatalf("expected offline tray with 3 items, got %+v", tray)
	}
	r0, r1, r2 := tray.GetOrderedItems()[0], tray.GetOrderedItems()[1], tray.GetOrderedItems()[2]
	if !r0.GetForeignDimmed() || r0.GetCanPromote() || r0.GetCanRestore() || !r0.GetNetworkActionsDisabled() {
		t.Fatalf("unexpected redacted foreign row: %+v", r0)
	}
	if r1.GetCanPromote() || r1.GetCanRestore() || !r1.GetNetworkActionsDisabled() {
		t.Fatalf("unexpected foreign attachment row without local record: %+v", r1)
	}
	if r2.GetSourceLabel().GetKey() != text.KeyQueueScheduledTime {
		t.Fatalf("expected scheduled_time source label, got %+v", r2.GetSourceLabel())
	}
}

func hasRestoreEffect(effs []*corev1.Effect) bool {
	for _, eff := range effs {
		if eff.GetSessionRestoreComposer() != nil {
			return true
		}
	}
	return false
}
