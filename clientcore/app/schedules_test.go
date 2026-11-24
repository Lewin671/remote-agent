package app

import (
	"testing"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/gen/timestamppb"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

func bootConnectedScheduleHost(t *testing.T, caps bool, rev uint64, defaultMode v1.ApprovalMode) (*Core, *corev1.EffectBatch) {
	t.Helper()
	c := New()
	c.Dispatch(&corev1.Event{
		NowMs: 1791633600000, // 2026-10-10T12:00:00Z
		Event: &corev1.Event_EntropyProvided{
			EntropyProvided: &corev1.EntropyProvided{
				RandomIds: []string{
					"id-1", "id-2", "id-3", "id-4", "id-5",
					"id-6", "id-7", "id-8", "id-9", "id-10",
					"id-11", "id-12", "id-13", "id-14", "id-15",
				},
				UnitFloats: []float64{0.5, 0.5, 0.5, 0.5, 0.5, 0.5},
			},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1791633600000,
		Event: &corev1.Event_ScheduleLocalContext{
			ScheduleLocalContext: &corev1.ScheduleLocalContextEvent{
				LocalTimezone:           "UTC",
				TomorrowStartMs:         1791676800000,
				DayAfterTomorrowStartMs: 1791763200000,
			},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1791633600000,
		Event: &corev1.Event_SyncLifecycle{
			SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1791633600010,
		Event: &corev1.Event_SeedDemoWorld{
			SeedDemoWorld: &corev1.SeedDemoWorldEvent{
				Security: &corev1.DemoSecurity{
					IsMember:          true,
					SelfDeviceId:      "dev-1",
					AuthorityDeviceId: "dev-1",
					LeaseEndMs:        1799999999000,
					TrustedMachineIds: []string{"m-1"},
				},
			},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1791633600020,
		Event: &corev1.Event_TokenResult{
			TokenResult: &corev1.TokenResultEvent{OpId: 2, Token: "tok-1"},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1791633600030,
		Event: &corev1.Event_StreamReady{
			StreamReady: &corev1.StreamReadyEvent{LegId: 1, ServerTimeMs: 1791633600030},
		},
	})
	batch := c.Dispatch(&corev1.Event{
		NowMs: 1791633600040,
		Event: &corev1.Event_ListsFetched{
			ListsFetched: &corev1.ListsFetchedEvent{
				OpId: 3,
				Machines: []*corev1.PlainMachine{
					{
						MachineId: "m-1",
						Online:    true,
						Meta: &v1.MachineMeta{
							Hostname:         "MacStudio",
							ProtocolVersion:  18,
							ScheduleRevision: rev,
							Capabilities:     &v1.MachineCapabilities{ScheduledTasks: caps},
							Agents: []*v1.AgentInfo{
								{Agent: v1.Agent_AGENT_CODEX, DefaultMode: defaultMode, DefaultModeSource: v1.DefaultModeSource_DEFAULT_MODE_SOURCE_DAEMON_SETTINGS},
								{Agent: v1.Agent_AGENT_CLAUDE, DefaultMode: v1.ApprovalMode_APPROVAL_MODE_ASK, DefaultModeSource: v1.DefaultModeSource_DEFAULT_MODE_SOURCE_DAEMON_SETTINGS},
							},
						},
					},
				},
				Sessions: []*corev1.PlainSession{
					{
						SessionId: "s-run-1",
						MachineId: "m-1",
						LastSeq:   3,
						UpdatedMs: 1791633500000,
						Meta: &v1.Session{
							Id:     "s-run-1",
							Cwd:    "/Users/me/app",
							Status: v1.SessionStatus_SESSION_STATUS_EXITED,
							Agent:  v1.Agent_AGENT_CODEX,
						},
					},
					{
						SessionId: "s-resume",
						MachineId: "m-1",
						LastSeq:   1,
						UpdatedMs: 1791633400000,
						Meta: &v1.Session{
							Id:     "s-resume",
							Cwd:    "/Users/me/app",
							Status: v1.SessionStatus_SESSION_STATUS_IDLE,
							Title:  "Nightly check",
							Agent:  v1.Agent_AGENT_CODEX,
						},
					},
				},
			},
		},
	})
	return c, batch
}

func findScheduleDispatches(batch *corev1.EffectBatch, op string) []*corev1.DispatchCommandEffect {
	var out []*corev1.DispatchCommandEffect
	if batch == nil {
		return nil
	}
	for _, eff := range batch.Effects {
		dc := eff.GetDispatchCommand()
		if dc == nil {
			continue
		}
		var cmd v1.Command
		if err := cmd.Unmarshal(dc.CommandBytes); err != nil {
			continue
		}
		if s := cmd.GetSchedule(); s != nil && (op == "" || s.Operation == op) {
			out = append(out, dc)
		}
	}
	return out
}

func settleScheduleCommand(t *testing.T, c *Core, dc *corev1.DispatchCommandEffect, nowMs int64, jsonPayload []byte, conflictKind, errCode string) *corev1.EffectBatch {
	t.Helper()
	res := &v1.CommandResult{
		Outcome:         "executed",
		Reason:          "ok",
		ResultAvailable: jsonPayload != nil,
	}
	if jsonPayload != nil {
		res.Kind = &v1.CommandResult_Schedule{
			Schedule: &v1.ScheduleResponse{ResultJson: jsonPayload},
		}
	}
	outerOutcome := "executed"
	outerReason := "ok"
	if conflictKind != "" {
		res.Outcome = "conflict"
		res.ConflictKind = conflictKind
		res.ErrorCode = errCode
		res.Reason = errCode
		outerOutcome = "conflict"
		outerReason = errCode
	}
	return c.Dispatch(&corev1.Event{
		NowMs: nowMs,
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:         dc.RequestId,
				AttemptId:         dc.AttemptId,
				Source:            corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON,
				AttemptBound:      true,
				Signed:            true,
				HashesMatch:       true,
				OuterMatchesInner: true,
				OuterOutcome:      outerOutcome,
				OuterReason:       outerReason,
				Result:            res,
			},
		},
	})
}

func settleUnknownCommand(c *Core, dc *corev1.DispatchCommandEffect, nowMs int64) *corev1.EffectBatch {
	return c.Dispatch(&corev1.Event{
		NowMs: nowMs,
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:    dc.RequestId,
				AttemptId:    dc.AttemptId,
				Source:       corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON,
				Signed:       true,
				HashesMatch:  false,
				AttemptBound: true,
			},
		},
	})
}

func viewSchedulesVM(t *testing.T, c *Core) *corev1.SchedulesViewModel {
	t.Helper()
	vm, err := c.View(&corev1.Screen{Screen: &corev1.Screen_Schedules{Schedules: &corev1.SchedulesScreenRequest{}}})
	if err != nil {
		t.Fatalf("View(Schedules): %v", err)
	}
	return vm.GetSchedules()
}

func viewDetailVM(t *testing.T, c *Core, mid, tid string) *corev1.ScheduleDetailViewModel {
	t.Helper()
	vm, err := c.View(&corev1.Screen{Screen: &corev1.Screen_ScheduleDetail{ScheduleDetail: &corev1.ScheduleDetailScreenRequest{MachineId: mid, TaskId: tid}}})
	if err != nil {
		t.Fatalf("View(ScheduleDetail): %v", err)
	}
	return vm.GetScheduleDetail()
}

func viewFormVM(t *testing.T, c *Core, key string) *corev1.ScheduleFormViewModel {
	t.Helper()
	vm, err := c.View(&corev1.Screen{Screen: &corev1.Screen_ScheduleForm{ScheduleForm: &corev1.ScheduleFormScreenRequest{FormKey: key}}})
	if err != nil {
		t.Fatalf("View(ScheduleForm): %v", err)
	}
	return vm.GetScheduleForm()
}

func TestScheduleDiscoveryCacheRevisionAndUnsupportedCleanup(t *testing.T) {
	c, batch := bootConnectedScheduleHost(t, true, 5, v1.ApprovalMode_APPROVAL_MODE_ASK)
	defer c.Close()

	var readOp uint64
	var sawLoadSeen bool
	for _, eff := range batch.Effects {
		if eff.GetScheduleLoadSeen() != nil && eff.GetScheduleLoadSeen().MachineId == "m-1" {
			sawLoadSeen = true
		}
		if rc := eff.GetScheduleReadCache(); rc != nil && rc.MachineId == "m-1" {
			readOp = rc.OpId
		}
	}
	if !sawLoadSeen || readOp == 0 {
		t.Fatalf("expected ScheduleLoadSeen & ScheduleReadCache, got effects=%d", len(batch.Effects))
	}
	listCmds := findScheduleDispatches(batch, "list")
	if len(listCmds) != 1 {
		t.Fatalf("expected 1 list dispatch, got %d", len(listCmds))
	}

	// Restore cached tasks before live response settles.
	cachedJSON := []byte(`{"tasks":[{"id":"t-cached","revision":2,"status":"active","source":"client","created_at":"2026-10-09T08:00:00Z","updated_at":"2026-10-09T08:00:00Z","spec":{"title":"Cached check","message":"hi","trigger":{"type":"daily","at":"08:00","timezone":"UTC"},"target":{"type":"new_session","cwd":"/Users/me/app","agent":"codex","mode":"ask"}}}]}`)
	c.Dispatch(&corev1.Event{
		NowMs: 1791633600050,
		Event: &corev1.Event_ScheduleCacheLoaded{
			ScheduleCacheLoaded: &corev1.ScheduleCacheLoadedEvent{OpId: readOp, MachineId: "m-1", ResultJson: cachedJSON},
		},
	})
	vm := viewSchedulesVM(t, c)
	if len(vm.Others) != 1 || vm.Others[0].Title != "Cached check" {
		t.Fatalf("expected cached task in Others, got fresh=%d others=%d (%+v)", len(vm.Fresh), len(vm.Others), vm.Others)
	}

	// Settle live list at revision 5.
	liveJSON := []byte(`{"tasks":[{"id":"t-live","revision":5,"status":"active","source":"client","created_at":"2026-10-09T08:00:00Z","updated_at":"2026-10-10T08:00:00Z","spec":{"title":"Live check","message":"run","trigger":{"type":"calendar","time":"08:00","timezone":"UTC"},"target":{"type":"new_session","cwd":"/Users/me/app","agent":"codex","mode":"ask"}}}]}`)
	settleScheduleCommand(t, c, listCmds[0], 1791633600060, liveJSON, "", "")
	vm = viewSchedulesVM(t, c)
	if len(vm.Others) != 1 || vm.Others[0].Title != "Live check" {
		t.Fatalf("expected live task after settle, got %+v", vm.Others)
	}

	// Bump ScheduleRevision via stream frame -> triggers second list fetch.
	revBatch := c.Dispatch(&corev1.Event{
		NowMs: 1791633600070,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: 1,
				Frame: &corev1.StreamFrameEvent_MachineUpdated{
					MachineUpdated: &corev1.PlainMachine{
						MachineId: "m-1",
						Online:    true,
						Meta: &v1.MachineMeta{
							Hostname:         "MacStudio",
							ProtocolVersion:  18,
							ScheduleRevision: 6,
							Capabilities:     &v1.MachineCapabilities{ScheduledTasks: true},
						},
					},
				},
			},
		},
	})
	revList := findScheduleDispatches(revBatch, "list")
	if len(revList) != 1 {
		t.Fatalf("expected re-list on ScheduleRevision bump 5->6, got %d", len(revList))
	}
	// Failing a list for a non-zero ScheduleRevision must not immediately re-dispatch list in a loop.
	failBatch := settleScheduleCommand(t, c, revList[0], 1791633600075, nil, "unavailable", "unavailable")
	if got := findScheduleDispatches(failBatch, "list"); len(got) != 0 {
		t.Fatalf("expected no retry list loop after failed list, got %d", len(got))
	}
	// Explicit ScheduleRefresh still retries the list.
	retryBatch := c.Dispatch(&corev1.Event{
		NowMs: 1791633600078,
		Event: &corev1.Event_ScheduleRefresh{ScheduleRefresh: &corev1.ScheduleStringAction{Value: "m-1"}},
	})
	retryList := findScheduleDispatches(retryBatch, "list")
	if len(retryList) != 1 {
		t.Fatalf("expected explicit refresh to re-list, got %d", len(retryList))
	}
	settleScheduleCommand(t, c, retryList[0], 1791633600079, liveJSON, "", "")

	// Machine loses scheduled_tasks capability -> emits ScheduleDeleteCacheEffect & forgets tasks.
	unBatch := c.Dispatch(&corev1.Event{
		NowMs: 1791633600080,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: 1,
				Frame: &corev1.StreamFrameEvent_MachineUpdated{
					MachineUpdated: &corev1.PlainMachine{
						MachineId: "m-1",
						Online:    true,
						Meta: &v1.MachineMeta{
							Hostname:        "MacStudio",
							ProtocolVersion: 18,
							Capabilities:    &v1.MachineCapabilities{ScheduledTasks: false},
						},
					},
				},
			},
		},
	})
	var deleted bool
	for _, eff := range unBatch.Effects {
		if dc := eff.GetScheduleDeleteCache(); dc != nil && dc.MachineId == "m-1" {
			deleted = true
		}
	}
	if !deleted {
		t.Fatalf("expected ScheduleDeleteCacheEffect when machine becomes unsupported")
	}
	vm = viewSchedulesVM(t, c)
	if len(vm.Others) != 0 {
		t.Fatalf("expected tasks cleared after unsupported cleanup")
	}
}

func TestScheduleInboxDetailPinnedResultAndFinalAnswer(t *testing.T) {
	c, batch := bootConnectedScheduleHost(t, true, 1, v1.ApprovalMode_APPROVAL_MODE_ASK)
	defer c.Close()

	listCmds := findScheduleDispatches(batch, "list")
	if len(listCmds) != 1 {
		t.Fatalf("expected list dispatch")
	}
	listJSON := []byte(`{"tasks":[
		{"id":"t1","revision":1,"status":"active","source":"client","created_at":"2026-10-09T08:00:00Z","updated_at":"2026-10-10T08:00:00Z","spec":{"title":"Daily summary","message":"summarize","trigger":{"type":"calendar","time":"08:00","timezone":"UTC"},"target":{"type":"resume_session","session":"s-resume"}},"runs":[
			{"id":"r1","session_id":"s-run-1","start_seq":0,"after_seq":0,"status":"completed","notified":true,"final_summary":"short fallback","ended_at":"2026-10-10T08:01:00Z","notification_session_id":"s-run-1","notification_seq":3,"notification_source":"daemon"}
		]},
		{"id":"t-att","revision":2,"status":"failed","reason":"execution_failed","source":"client","created_at":"2026-10-09T08:00:00Z","updated_at":"2026-10-10T09:00:00Z","spec":{"title":"Broken one-shot","message":"go","trigger":{"type":"at","at":"2026-10-10T09:00:00Z"},"target":{"type":"new_session","cwd":"/Users/me/app","agent":"codex","mode":"ask"}}},
		{"id":"t-end","revision":1,"status":"completed","source":"client","created_at":"2026-10-09T08:00:00Z","updated_at":"2026-10-09T10:00:00Z","spec":{"title":"Finished","message":"done","trigger":{"type":"at","at":"2026-10-09T10:00:00Z"},"target":{"type":"new_session","cwd":"/Users/me/app","agent":"codex","mode":"ask"}}}
	]}`)
	c.Dispatch(&corev1.Event{
		NowMs: 1791633600045,
		Event: &corev1.Event_ScheduleSeenLoaded{
			ScheduleSeenLoaded: &corev1.ScheduleSeenLoadedEvent{MachineId: "m-1", Found: true},
		},
	})
	settleScheduleCommand(t, c, listCmds[0], 1791633600050, listJSON, "", "")

	vm := viewSchedulesVM(t, c)
	if len(vm.Attention) != 1 || len(vm.Fresh) != 1 || len(vm.Ended) != 1 {
		t.Fatalf("inbox groups = attention:%d fresh:%d ended:%d", len(vm.Attention), len(vm.Fresh), len(vm.Ended))
	}

	// SessionStrip for s-resume shows t1!
	stripVM, err := c.View(&corev1.Screen{Screen: &corev1.Screen_ScheduleSessionStrip{ScheduleSessionStrip: &corev1.ScheduleSessionStripScreenRequest{SessionId: "s-resume"}}})
	if err != nil {
		t.Fatalf("View(Strip): %v", err)
	}
	if !stripVM.GetScheduleSessionStrip().Visible || stripVM.GetScheduleSessionStrip().Count != 1 {
		t.Fatalf("unexpected SessionStrip: %+v", stripVM.GetScheduleSessionStrip())
	}

	// Opening Detail emits SaveSeen, DismissNotifications, and CacheReadEvents for s-run-1!
	openBatch := c.Dispatch(&corev1.Event{
		NowMs: 1791633600060,
		Event: &corev1.Event_ScheduleDetailOpen{
			ScheduleDetailOpen: &corev1.ScheduleTaskRef{MachineId: "m-1", TaskId: "t1"},
		},
	})
	var cacheOp, getEventsOp uint64
	var sawDismiss bool
	for _, eff := range openBatch.Effects {
		if eff.GetScheduleDismissNotifications() != nil {
			sawDismiss = true
		}
		if cr := eff.GetCacheRead(); cr != nil && cr.GetEvents() != nil && cr.GetEvents().SessionId == "s-run-1" {
			cacheOp = cr.GetEvents().OpId
		}
		if rpc := eff.GetRpc(); rpc != nil && rpc.GetGetEvents() != nil && rpc.GetGetEvents().SessionId == "s-run-1" {
			getEventsOp = rpc.OpId
		}
	}
	if !sawDismiss || (cacheOp == 0 && getEventsOp == 0) {
		t.Fatalf("expected DismissNotifications and CacheReadEvents/GetEvents on DetailOpen (dismiss=%v cacheOp=%d rpcOp=%d)", sawDismiss, cacheOp, getEventsOp)
	}
	if cacheOp != 0 && getEventsOp == 0 {
		cacheBatch := c.Dispatch(&corev1.Event{
			NowMs: 1791633600065,
			Event: &corev1.Event_CacheEventsLoaded{
				CacheEventsLoaded: &corev1.CacheEventsLoadedEvent{OpId: cacheOp, SessionId: "s-run-1"},
			},
		})
		for _, eff := range cacheBatch.Effects {
			if rpc := eff.GetRpc(); rpc != nil && rpc.GetGetEvents() != nil && rpc.GetGetEvents().SessionId == "s-run-1" {
				getEventsOp = rpc.OpId
			}
		}
	}
	if getEventsOp == 0 {
		t.Fatalf("expected GetEvents RPC for s-run-1")
	}

	// Feed session events with final_answer.
	c.Dispatch(&corev1.Event{
		NowMs: 1791633600070,
		Event: &corev1.Event_EventsPageFetched{
			EventsPageFetched: &corev1.EventsPageFetchedEvent{
				OpId:      getEventsOp,
				SessionId: "s-run-1",
				Events: []*corev1.PlainEvent{
					{Seq: 1, Verified: true, Event: &v1.Event{Seq: 1, Time: &timestamppb.Timestamp{Seconds: 1791619200}, Origin: &v1.Origin{ScheduledTaskId: "t1", ScheduledRunId: "r1"}, Kind: &v1.Event_UserMessage{UserMessage: &v1.UserMessage{Text: "summarize"}}}},
					{Seq: 2, Verified: true, Event: &v1.Event{Seq: 2, Time: &timestamppb.Timestamp{Seconds: 1791619210}, Kind: &v1.Event_AgentMessage{AgentMessage: &v1.AgentMessage{Phase: "final_answer", Text: "Detailed report markdown"}}}},
					{Seq: 3, Verified: true, Event: &v1.Event{Seq: 3, Time: &timestamppb.Timestamp{Seconds: 1791619220}, Kind: &v1.Event_TurnEnded{TurnEnded: &v1.TurnEnded{ScheduledTaskId: "t1", ScheduledRunId: "r1", ScheduledNotified: true, Outcome: v1.TurnOutcome_TURN_OUTCOME_COMPLETED}}}},
				},
			},
		},
	})
	dvm := viewDetailVM(t, c, "m-1", "t1")
	if dvm.ResultCard.MarkdownBody != "Detailed report markdown" {
		t.Fatalf("ResultCard.MarkdownBody = %q, want Detailed report markdown", dvm.ResultCard.MarkdownBody)
	}
}

func TestScheduleCreateFormPlanBlockAndUnknownRetryAbandon(t *testing.T) {
	c, _ := bootConnectedScheduleHost(t, true, 1, v1.ApprovalMode_APPROVAL_MODE_PLAN)
	defer c.Close()

	c.Dispatch(&corev1.Event{
		NowMs: 1791633600050,
		Event: &corev1.Event_ScheduleFormOpen{
			ScheduleFormOpen: &corev1.ScheduleFormOpenAction{
				Mode:       corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_CREATE_NEW,
				MachineId:  "m-1",
				ProjectKey: "/Users/me/app",
			},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1791633600055,
		Event: &corev1.Event_ScheduleFormSetMessage{
			ScheduleFormSetMessage: &corev1.ScheduleFormStringAction{Value: "Check CI status."},
		},
	})

	fvm := viewFormVM(t, c, "")
	if fvm.CanSubmit || fvm.SubmitDisabledReason.GetKey() != text.KeySchedulePlanModeCannotCreateExecutionTasksChooseOtherPermissions {
		t.Fatalf("expected Plan mode block, got canSubmit=%v reason=%v", fvm.CanSubmit, fvm.SubmitDisabledReason)
	}

	// Switch permission to AUTO -> unlocks submit.
	auto := v1.ApprovalMode_APPROVAL_MODE_AUTO
	c.Dispatch(&corev1.Event{
		NowMs: 1791633600060,
		Event: &corev1.Event_ScheduleFormSelectModel{
			ScheduleFormSelectModel: &corev1.ScheduleFormSelectModelAction{Mode: &auto},
		},
	})
	fvm = viewFormVM(t, c, "")
	if !fvm.CanSubmit {
		t.Fatalf("expected CanSubmit after switching to AUTO, disabled=%v", fvm.SubmitDisabledReason)
	}

	submitBatch := c.Dispatch(&corev1.Event{
		NowMs: 1791633600070,
		Event: &corev1.Event_ScheduleFormSubmit{
			ScheduleFormSubmit: &corev1.ScheduleFormBoolAction{},
		},
	})
	dcs := findScheduleDispatches(submitBatch, "create")
	if len(dcs) != 1 {
		t.Fatalf("expected create dispatch, got %d", len(dcs))
	}

	// Unknown outcome freezes the attempt with original request ID and spec.
	settleUnknownCommand(c, dcs[0], 1791633600080)
	fvm = viewFormVM(t, c, "")
	if !fvm.Mutation.Frozen {
		t.Fatalf("expected frozen mutation after unknown")
	}

	// Retry resends with identical RequestId.
	retryBatch := c.Dispatch(&corev1.Event{
		NowMs: 1791633600090,
		Event: &corev1.Event_ScheduleRetryMutation{
			ScheduleRetryMutation: &corev1.ScheduleMutationRef{FormKey: fvm.FormKey},
		},
	})
	retryDCs := findScheduleDispatches(retryBatch, "create")
	if len(retryDCs) != 1 || retryDCs[0].RequestId != dcs[0].RequestId || !retryDCs[0].RetryOfUnknown {
		t.Fatalf("expected retry with same RequestID=%q and RetryOfUnknown=true, got %+v", dcs[0].RequestId, retryDCs)
	}

	// Settle create Executed -> closes form & emits DETAIL navigation.
	createdJSON := []byte(`{"id":"t-new","revision":1,"status":"active","source":"client","created_at":"2026-10-10T12:00:00Z","updated_at":"2026-10-10T12:00:00Z","spec":{"title":"Check CI status","message":"Check CI status.","trigger":{"type":"calendar","time":"08:00","timezone":"UTC"},"target":{"type":"new_session","cwd":"/Users/me/app","agent":"codex","mode":"auto"}}}`)
	execBatch := settleScheduleCommand(t, c, retryDCs[0], 1791633600100, createdJSON, "", "")
	var navDetail bool
	for _, eff := range execBatch.Effects {
		if nav := eff.GetScheduleNavigate(); nav != nil && nav.Route == corev1.ScheduleNavigateRoute_SCHEDULE_NAVIGATE_ROUTE_DETAIL && nav.TaskId == "t-new" {
			navDetail = true
		}
	}
	if !navDetail {
		t.Fatalf("expected DETAIL navigate effect for t-new")
	}
	if viewFormVM(t, c, fvm.FormKey).Open {
		t.Fatalf("expected form closed after creation")
	}
}

func TestScheduleBusinessConflictAndOperate(t *testing.T) {
	c, batch := bootConnectedScheduleHost(t, true, 1, v1.ApprovalMode_APPROVAL_MODE_ASK)
	defer c.Close()

	listDC := findScheduleDispatches(batch, "list")[0]
	listJSON := []byte(`{"tasks":[{"id":"t1","revision":3,"status":"active","source":"client","created_at":"2026-10-09T08:00:00Z","updated_at":"2026-10-10T08:00:00Z","spec":{"title":"Daily","message":"msg","trigger":{"type":"calendar","time":"08:00","timezone":"UTC"},"target":{"type":"new_session","cwd":"/Users/me/app","agent":"codex","mode":"ask"}}}]}`)
	settleScheduleCommand(t, c, listDC, 1791633600050, listJSON, "", "")

	// Open timing edit form and submit -> Conflict(business) closes sub-form, sets updated-to-latest error, and refreshes!
	c.Dispatch(&corev1.Event{
		NowMs: 1791633600060,
		Event: &corev1.Event_ScheduleFormOpen{
			ScheduleFormOpen: &corev1.ScheduleFormOpenAction{
				Mode:      corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_EDIT_TIMING,
				MachineId: "m-1",
				TaskId:    "t1",
			},
		},
	})
	fvm := viewFormVM(t, c, "")
	if !fvm.Open {
		t.Fatalf("expected edit timing form open")
	}
	upBatch := c.Dispatch(&corev1.Event{
		NowMs: 1791633600070,
		Event: &corev1.Event_ScheduleFormSubmit{
			ScheduleFormSubmit: &corev1.ScheduleFormBoolAction{FormKey: fvm.FormKey},
		},
	})
	upDC := findScheduleDispatches(upBatch, "update")[0]
	confBatch := settleScheduleCommand(t, c, upDC, 1791633600080, nil, "business", "conflict")

	var closedForm bool
	for _, eff := range confBatch.Effects {
		if nav := eff.GetScheduleNavigate(); nav != nil && nav.Route == corev1.ScheduleNavigateRoute_SCHEDULE_NAVIGATE_ROUTE_CLOSE_FORM {
			closedForm = true
		}
	}
	if !closedForm || len(findScheduleDispatches(confBatch, "list")) != 1 {
		t.Fatalf("expected CLOSE_FORM + refresh list on business conflict (closed=%v)", closedForm)
	}
	dvm := viewDetailVM(t, c, "m-1", "t1")
	if dvm.Mutation.Error.GetKey() != text.KeyErrorsTheTaskWasJustChangedUpdatedToTheLatest {
		t.Fatalf("expected conflict error text, got %v", dvm.Mutation.Error)
	}

	// Run now -> sets RequestedARunNow notice; Cancel -> emits LIST navigation.
	runBatch := c.Dispatch(&corev1.Event{
		NowMs: 1791633600090,
		Event: &corev1.Event_ScheduleOperate{
			ScheduleOperate: &corev1.ScheduleOperateAction{
				MachineId: "m-1",
				TaskId:    "t1",
				Operation: corev1.ScheduleOperationKind_SCHEDULE_OPERATION_KIND_RUN,
			},
		},
	})
	runDC := findScheduleDispatches(runBatch, "run")[0]
	settleScheduleCommand(t, c, runDC, 1791633600100, []byte(`{"id":"t1","revision":4,"status":"active","source":"client","created_at":"2026-10-09T08:00:00Z","updated_at":"2026-10-10T08:00:00Z","spec":{"title":"Daily","message":"msg","trigger":{"type":"calendar","time":"08:00","timezone":"UTC"},"target":{"type":"new_session","cwd":"/Users/me/app","agent":"codex","mode":"ask"}}}`), "", "")
	dvm = viewDetailVM(t, c, "m-1", "t1")
	if dvm.Mutation.Notice.GetKey() != text.KeyScheduleRequestedARunNowTheAutomaticScheduleIsUnchanged {
		t.Fatalf("expected run notice, got %v", dvm.Mutation.Notice)
	}

	cancelBatch := c.Dispatch(&corev1.Event{
		NowMs: 1791633600110,
		Event: &corev1.Event_ScheduleOperate{
			ScheduleOperate: &corev1.ScheduleOperateAction{
				MachineId: "m-1",
				TaskId:    "t1",
				Operation: corev1.ScheduleOperationKind_SCHEDULE_OPERATION_KIND_CANCEL,
			},
		},
	})
	cancelDC := findScheduleDispatches(cancelBatch, "cancel")[0]
	navBatch := settleScheduleCommand(t, c, cancelDC, 1791633600120, nil, "", "")
	var navList bool
	for _, eff := range navBatch.Effects {
		if nav := eff.GetScheduleNavigate(); nav != nil && nav.Route == corev1.ScheduleNavigateRoute_SCHEDULE_NAVIGATE_ROUTE_LIST {
			navList = true
		}
	}
	if !navList {
		t.Fatalf("expected LIST navigate on cancel")
	}
}

func TestScheduleOfflineDetailResultNotStuckLoading(t *testing.T) {
	c, batch := bootConnectedScheduleHost(t, true, 1, v1.ApprovalMode_APPROVAL_MODE_ASK)
	defer c.Close()

	listDC := findScheduleDispatches(batch, "list")[0]
	listJSON := []byte(`{"tasks":[{"id":"t1","revision":1,"status":"active","source":"client","created_at":"2026-10-09T08:00:00Z","updated_at":"2026-10-10T08:00:00Z","spec":{"title":"Daily","message":"msg","trigger":{"type":"calendar","time":"08:00","timezone":"UTC"},"target":{"type":"new_session","cwd":"/Users/me/app","agent":"codex","mode":"ask"}},"runs":[{"id":"r1","session_id":"s-run-1","status":"completed","notified":true,"final_summary":"offline summary","ended_at":"2026-10-10T08:01:00Z"}]}]}`)
	settleScheduleCommand(t, c, listDC, 1791633600050, listJSON, "", "")

	// Go offline before opening detail.
	c.Dispatch(&corev1.Event{
		NowMs: 1791633600060,
		Event: &corev1.Event_SyncLifecycle{
			SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_STOP},
		},
	})
	openBatch := c.Dispatch(&corev1.Event{
		NowMs: 1791633600070,
		Event: &corev1.Event_ScheduleDetailOpen{
			ScheduleDetailOpen: &corev1.ScheduleTaskRef{MachineId: "m-1", TaskId: "t1"},
		},
	})
	var cacheOp uint64
	for _, eff := range openBatch.Effects {
		if cr := eff.GetCacheRead(); cr != nil && cr.GetEvents() != nil && cr.GetEvents().SessionId == "s-run-1" {
			cacheOp = cr.GetEvents().OpId
		}
	}
	if cacheOp != 0 {
		c.Dispatch(&corev1.Event{
			NowMs: 1791633600075,
			Event: &corev1.Event_CacheEventsLoaded{
				CacheEventsLoaded: &corev1.CacheEventsLoadedEvent{OpId: cacheOp, SessionId: "s-run-1"},
			},
		})
	}
	dvm := viewDetailVM(t, c, "m-1", "t1")
	if dvm.ResultCard == nil || dvm.ResultCard.ResultLoading {
		t.Fatalf("expected offline detail not stuck loading, got %+v", dvm.ResultCard)
	}
	if dvm.ResultCard.MarkdownBody != "offline summary" {
		t.Fatalf("expected fallback final_summary offline, got %q", dvm.ResultCard.MarkdownBody)
	}
}

func TestScheduleFormResetDraftPreservesCommittedTrigger(t *testing.T) {
	c, _ := bootConnectedScheduleHost(t, true, 1, v1.ApprovalMode_APPROVAL_MODE_ASK)
	defer c.Close()

	c.Dispatch(&corev1.Event{
		NowMs: 1791633600020,
		Event: &corev1.Event_ScheduleFormOpen{
			ScheduleFormOpen: &corev1.ScheduleFormOpenAction{
				Mode:      corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_CREATE_NEW,
				MachineId: "m-1",
			},
		},
	})
	fvm := viewFormVM(t, c, "")
	if !fvm.Timing.ShowJitter || len(fvm.Timing.JitterOptions) != 4 || !fvm.Timing.JitterOptions[0].Selected || fvm.Timing.JitterOptions[0].Minutes != 0 {
		t.Fatalf("expected default daily jitter options [0*,15,30,60], got show=%v opts=%+v", fvm.Timing.ShowJitter, fvm.Timing.JitterOptions)
	}
	jitter50 := int64(50)
	c.Dispatch(&corev1.Event{
		NowMs: 1791633600025,
		Event: &corev1.Event_ScheduleFormUpdateTiming{
			ScheduleFormUpdateTiming: &corev1.ScheduleFormUpdateTimingAction{
				FormKey:       fvm.FormKey,
				JitterMinutes: &jitter50,
			},
		},
	})
	with50 := viewFormVM(t, c, fvm.FormKey)
	if len(with50.Timing.JitterOptions) != 5 || with50.Timing.JitterOptions[3].Minutes != 50 || !with50.Timing.JitterOptions[3].Selected {
		t.Fatalf("expected 50 inserted and selected, got %+v", with50.Timing.JitterOptions)
	}
	everyKind := corev1.ScheduleTimingKind_SCHEDULE_TIMING_KIND_INTERVAL
	everyUnit := corev1.ScheduleIntervalUnit_SCHEDULE_INTERVAL_UNIT_HOURS
	everyVal := "3"
	c.Dispatch(&corev1.Event{
		NowMs: 1791633600030,
		Event: &corev1.Event_ScheduleFormUpdateTiming{
			ScheduleFormUpdateTiming: &corev1.ScheduleFormUpdateTimingAction{
				FormKey:  fvm.FormKey,
				Kind:     &everyKind,
				Interval: &everyVal,
				Unit:     &everyUnit,
				Apply:    true,
			},
		},
	})
	if iv := viewFormVM(t, c, fvm.FormKey); iv.Timing.ShowJitter || len(iv.Timing.JitterOptions) != 0 {
		t.Fatalf("expected interval timing to hide jitter, got show=%v opts=%+v", iv.Timing.ShowJitter, iv.Timing.JitterOptions)
	}
	// Now start editing another value, then ResetDraft -> must revert back to every 3 hours and keep TimingApplied=true.
	badVal := "99"
	c.Dispatch(&corev1.Event{
		NowMs: 1791633600040,
		Event: &corev1.Event_ScheduleFormUpdateTiming{
			ScheduleFormUpdateTiming: &corev1.ScheduleFormUpdateTimingAction{
				FormKey:  fvm.FormKey,
				Interval: &badVal,
			},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1791633600050,
		Event: &corev1.Event_ScheduleFormUpdateTiming{
			ScheduleFormUpdateTiming: &corev1.ScheduleFormUpdateTimingAction{
				FormKey:    fvm.FormKey,
				ResetDraft: true,
			},
		},
	})
	after := viewFormVM(t, c, fvm.FormKey)
	if after.Timing.Kind != everyKind || after.Timing.Interval != "3" {
		t.Fatalf("expected ResetDraft to restore committed every 3h timing, got kind=%v val=%q", after.Timing.Kind, after.Timing.Interval)
	}
}

func TestScheduleEditTargetSelectProjectKeepsLockedMachine(t *testing.T) {
	c, batch := bootConnectedScheduleHost(t, true, 1, v1.ApprovalMode_APPROVAL_MODE_ASK)
	defer c.Close()

	listDC := findScheduleDispatches(batch, "list")[0]
	listJSON := []byte(`{"tasks":[{"id":"t1","revision":2,"status":"active","source":"client","created_at":"2026-10-09T08:00:00Z","updated_at":"2026-10-10T08:00:00Z","spec":{"title":"Daily","message":"msg","trigger":{"type":"calendar","time":"08:00","timezone":"UTC"},"target":{"type":"new_session","cwd":"/Users/me/app","agent":"codex","mode":"ask"}}}]}`)
	settleScheduleCommand(t, c, listDC, 1791633600050, listJSON, "", "")

	// Add a second machine with a project at /Users/me/other.
	c.Dispatch(&corev1.Event{
		NowMs: 1791633600055,
		Event: &corev1.Event_SeedDemoWorld{
			SeedDemoWorld: &corev1.SeedDemoWorldEvent{
				Security: &corev1.DemoSecurity{
					IsMember:          true,
					SelfDeviceId:      "dev-1",
					AuthorityDeviceId: "dev-1",
					LeaseEndMs:        1799999999000,
					TrustedMachineIds: []string{"m-1", "m-2"},
				},
			},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1791633600060,
		Event: &corev1.Event_ListsFetched{
			ListsFetched: &corev1.ListsFetchedEvent{
				Machines: []*corev1.PlainMachine{
					{
						MachineId: "m-1",
						Online:    true,
						Meta: &v1.MachineMeta{
							Hostname:         "Mac-1",
							ProtocolVersion:  18,
							ScheduleRevision: 1,
							Capabilities:     &v1.MachineCapabilities{ScheduledTasks: true},
							Agents:           []*v1.AgentInfo{{Agent: v1.Agent_AGENT_CODEX, DefaultMode: v1.ApprovalMode_APPROVAL_MODE_ASK}},
						},
					},
					{
						MachineId: "m-2",
						Online:    true,
						Meta: &v1.MachineMeta{
							Hostname:         "Mac-2",
							ProtocolVersion:  18,
							ScheduleRevision: 1,
							Capabilities:     &v1.MachineCapabilities{ScheduledTasks: true},
							Agents:           []*v1.AgentInfo{{Agent: v1.Agent_AGENT_CODEX, DefaultMode: v1.ApprovalMode_APPROVAL_MODE_ASK}},
						},
					},
				},
				Sessions: []*corev1.PlainSession{
					{
						SessionId: "s-other",
						MachineId: "m-2",
						UpdatedMs: 1791633600055,
						Meta:      &v1.Session{Id: "s-other", Cwd: "/Users/me/other", Agent: v1.Agent_AGENT_CODEX},
					},
				},
			},
		},
	})

	c.Dispatch(&corev1.Event{
		NowMs: 1791633600070,
		Event: &corev1.Event_ScheduleFormOpen{
			ScheduleFormOpen: &corev1.ScheduleFormOpenAction{
				Mode:      corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_EDIT_TARGET,
				MachineId: "m-1",
				TaskId:    "t1",
			},
		},
	})
	fvm := viewFormVM(t, c, "")
	c.Dispatch(&corev1.Event{
		NowMs: 1791633600080,
		Event: &corev1.Event_ScheduleFormSelectProject{
			ScheduleFormSelectProject: &corev1.ScheduleFormSelectProjectAction{
				FormKey:    fvm.FormKey,
				ProjectKey: "/Users/me/other",
				MachineId:  "m-2",
			},
		},
	})
	updated := viewFormVM(t, c, fvm.FormKey)
	if updated.MachineId != "m-1" || updated.ProjectKey != "/Users/me/other" {
		t.Fatalf("expected locked machine m-1 and project /Users/me/other, got machine=%q project=%q", updated.MachineId, updated.ProjectKey)
	}
}

func TestScheduleInitialLoadingBeforeListsAndCache(t *testing.T) {
	c := New()
	defer c.Close()

	// Before MachinesListed arrives, Schedules and ScheduleDetail must report loading rather than empty/not-found.
	svm := viewSchedulesVM(t, c)
	if !svm.Loading || svm.ShowEmptyHint {
		t.Fatalf("expected initial schedules Loading=true ShowEmptyHint=false before MachinesListed, got loading=%v showEmptyHint=%v", svm.Loading, svm.ShowEmptyHint)
	}
	if svm.Headline == nil || svm.Headline.Title == nil || svm.Headline.Title.Key != text.KeyScheduleReadingTasks {
		t.Fatalf("expected reading_tasks headline before MachinesListed, got %+v", svm.Headline)
	}
	dvm := viewDetailVM(t, c, "m-1", "t1")
	if !dvm.Loading || dvm.Found || dvm.NotFoundText == nil || dvm.NotFoundText.Key != text.KeyScheduleReadingTasks {
		t.Fatalf("expected initial detail Loading=true with reading_tasks before MachinesListed, got %+v", dvm)
	}

	// Even after ListsFetched adds a supported machine, before ScheduleListOpen creates a snapshot it must stay Loading=true.
	c.Dispatch(&corev1.Event{
		NowMs: 1791633600000,
		Event: &corev1.Event_ListsFetched{
			ListsFetched: &corev1.ListsFetchedEvent{
				Machines: []*corev1.PlainMachine{
					{
						MachineId: "m-1",
						Online:    false,
						Meta: &v1.MachineMeta{
							Hostname:        "Mac-1",
							ProtocolVersion: 18,
							Capabilities:    &v1.MachineCapabilities{ScheduledTasks: true},
						},
					},
				},
			},
		},
	})
	svm = viewSchedulesVM(t, c)
	if !svm.Loading || svm.ShowEmptyHint {
		t.Fatalf("expected Loading=true ShowEmptyHint=false before ScheduleListOpen initializes machine snapshot, got loading=%v showEmptyHint=%v", svm.Loading, svm.ShowEmptyHint)
	}
	dvm = viewDetailVM(t, c, "m-1", "t1")
	if !dvm.Loading || dvm.NotFoundText == nil || dvm.NotFoundText.Key != text.KeyScheduleReadingTasks {
		t.Fatalf("expected detail Loading=true before ScheduleDetailOpen/snapshot init, got %+v", dvm)
	}
}
