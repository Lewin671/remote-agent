package app

import (
	"slices"
	"strings"
	"testing"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/sync"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

func decodeDispatchedCommands(t *testing.T, batch *corev1.EffectBatch) ([]*corev1.DispatchCommandEffect, []*v1.Command) {
	t.Helper()
	var effs []*corev1.DispatchCommandEffect
	var cmds []*v1.Command
	if batch == nil {
		return nil, nil
	}
	for _, eff := range batch.Effects {
		dc := eff.GetDispatchCommand()
		if dc == nil {
			continue
		}
		var cmd v1.Command
		if err := cmd.Unmarshal(dc.CommandBytes); err != nil {
			t.Fatalf("unmarshal CommandBytes: %v", err)
		}
		effs = append(effs, dc)
		cmds = append(cmds, &cmd)
	}
	return effs, cmds
}

func findUITransientTimer(batch *corev1.EffectBatch, key string) *corev1.ScheduleTimerEffect {
	if batch == nil {
		return nil
	}
	for _, eff := range batch.Effects {
		st := eff.GetScheduleTimer()
		if st != nil && st.Kind == corev1.TimerKind_TIMER_KIND_UI_TRANSIENT && (key == "" || st.Key == key) {
			return st
		}
	}
	return nil
}

func settleCommandOK(c *Core, nowMs int64, dc *corev1.DispatchCommandEffect, res *v1.CommandResult) *corev1.EffectBatch {
	if res == nil {
		res = &v1.CommandResult{}
	}
	if res.Outcome == "" {
		res.Outcome = "executed"
	}
	if res.Reason == "" {
		res.Reason = "ok"
	}
	res.ResultAvailable = true
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
				OuterOutcome:      "executed",
				OuterReason:       "ok",
				Result:            res,
			},
		},
	})
}

func settleCommandFailed(c *Core, nowMs int64, dc *corev1.DispatchCommandEffect, code string) *corev1.EffectBatch {
	return c.Dispatch(&corev1.Event{
		NowMs: nowMs,
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:     dc.RequestId,
				AttemptId:     dc.AttemptId,
				Source:        corev1.OutcomeSource_OUTCOME_SOURCE_SERVER,
				NotForwarded:  true,
				AttemptBound:  true,
				TransportCode: code,
			},
		},
	})
}

func settleCommandUnknown(c *Core, nowMs int64, dc *corev1.DispatchCommandEffect) *corev1.EffectBatch {
	return c.Dispatch(&corev1.Event{
		NowMs: nowMs,
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:     dc.RequestId,
				AttemptId:     dc.AttemptId,
				Source:        corev1.OutcomeSource_OUTCOME_SOURCE_TRANSPORT,
				NotForwarded:  false,
				TransportCode: "network",
			},
		},
	})
}

func makeSemanticTestCore(t *testing.T, sess *corev1.PlainSession, models []*v1.ModelInfo) *Core {
	t.Helper()
	c := bootstrapReadyApp(t, []*corev1.PlainMachine{
		{
			MachineId: "m-on",
			Online:    true,
			Meta: &v1.MachineMeta{
				Hostname:        "DevBox",
				ProtocolVersion: sync.PeerVersion,
				Capabilities: &v1.MachineCapabilities{
					ApprovalAlways: true,
					ScheduledTasks: true,
				},
				Agents: []*v1.AgentInfo{
					{Agent: v1.Agent_AGENT_CLAUDE, DefaultMode: v1.ApprovalMode_APPROVAL_MODE_ASK},
				},
			},
		},
	}, []*corev1.PlainSession{sess})

	if len(models) > 0 {
		openBatch := c.Dispatch(&corev1.Event{
			NowMs: 1_791_078_060_100,
			Event: &corev1.Event_SessionOpen{
				SessionOpen: &corev1.SessionOpenAction{SessionId: sess.SessionId},
			},
		})
		effs, cmds := decodeDispatchedCommands(t, openBatch)
		for i, cmd := range cmds {
			if cmd.GetListModels() != nil {
				settleCommandOK(c, 1_791_078_060_120, effs[i], &v1.CommandResult{
					Outcome: "executed",
					Kind: &v1.CommandResult_ListModels{
						ListModels: &v1.ListModelsResponse{
							DefaultModel: models[0].Slug,
							Models:       models,
						},
					},
				})
			}
		}
	}
	return c
}

func TestSessionSemanticSimpleAndModeCommands(t *testing.T) {
	sess := &corev1.PlainSession{
		SessionId: "s-1",
		MachineId: "m-on",
		UpdatedMs: 1_791_078_060_000,
		Meta: &v1.Session{
			Id:     "s-1",
			Cwd:    "/Users/me/proj",
			Agent:  v1.Agent_AGENT_CLAUDE,
			Mode:   v1.ApprovalMode_APPROVAL_MODE_ASK,
			Status: v1.SessionStatus_SESSION_STATUS_RUNNING,
		},
	}

	cases := []struct {
		name      string
		event     *corev1.Event
		checkCmd  func(t *testing.T, cmd *v1.Command)
		slotField func(vm *corev1.SessionViewModel) *corev1.SessionCommandState
	}{
		{
			name: "interrupt",
			event: &corev1.Event{
				NowMs: 1_791_078_061_000,
				Event: &corev1.Event_SessionSimpleCommand{
					SessionSimpleCommand: &corev1.SessionSimpleCommandAction{
						SessionId: "s-1",
						Kind:      corev1.SessionSimpleCommandKind_SESSION_SIMPLE_COMMAND_KIND_INTERRUPT,
					},
				},
			},
			checkCmd: func(t *testing.T, cmd *v1.Command) {
				if cmd.GetInterrupt().GetSessionId() != "s-1" {
					t.Fatalf("expected Interrupt(s-1), got %+v", cmd.Kind)
				}
			},
			slotField: func(vm *corev1.SessionViewModel) *corev1.SessionCommandState { return vm.Actions.Interrupt },
		},
		{
			name: "stop",
			event: &corev1.Event{
				NowMs: 1_791_078_061_000,
				Event: &corev1.Event_SessionSimpleCommand{
					SessionSimpleCommand: &corev1.SessionSimpleCommandAction{
						SessionId: "s-1",
						Kind:      corev1.SessionSimpleCommandKind_SESSION_SIMPLE_COMMAND_KIND_STOP,
					},
				},
			},
			checkCmd: func(t *testing.T, cmd *v1.Command) {
				if cmd.GetStopSession().GetSessionId() != "s-1" {
					t.Fatalf("expected StopSession(s-1), got %+v", cmd.Kind)
				}
			},
			slotField: func(vm *corev1.SessionViewModel) *corev1.SessionCommandState { return vm.Actions.End },
		},
		{
			name: "resume",
			event: &corev1.Event{
				NowMs: 1_791_078_061_000,
				Event: &corev1.Event_SessionSimpleCommand{
					SessionSimpleCommand: &corev1.SessionSimpleCommandAction{
						SessionId: "s-1",
						Kind:      corev1.SessionSimpleCommandKind_SESSION_SIMPLE_COMMAND_KIND_RESUME,
					},
				},
			},
			checkCmd: func(t *testing.T, cmd *v1.Command) {
				if cmd.GetResumeSession().GetSessionId() != "s-1" {
					t.Fatalf("expected ResumeSession(s-1), got %+v", cmd.Kind)
				}
			},
			slotField: func(vm *corev1.SessionViewModel) *corev1.SessionCommandState { return vm.Actions.Resume },
		},
		{
			name: "fork",
			event: &corev1.Event{
				NowMs: 1_791_078_061_000,
				Event: &corev1.Event_SessionSimpleCommand{
					SessionSimpleCommand: &corev1.SessionSimpleCommandAction{
						SessionId: "s-1",
						Kind:      corev1.SessionSimpleCommandKind_SESSION_SIMPLE_COMMAND_KIND_FORK,
					},
				},
			},
			checkCmd: func(t *testing.T, cmd *v1.Command) {
				if cmd.GetForkSession().GetSessionId() != "s-1" {
					t.Fatalf("expected ForkSession(s-1), got %+v", cmd.Kind)
				}
			},
			slotField: func(vm *corev1.SessionViewModel) *corev1.SessionCommandState { return vm.Actions.Fork },
		},
		{
			name: "compact",
			event: &corev1.Event{
				NowMs: 1_791_078_061_000,
				Event: &corev1.Event_SessionSimpleCommand{
					SessionSimpleCommand: &corev1.SessionSimpleCommandAction{
						SessionId: "s-1",
						Kind:      corev1.SessionSimpleCommandKind_SESSION_SIMPLE_COMMAND_KIND_COMPACT,
					},
				},
			},
			checkCmd: func(t *testing.T, cmd *v1.Command) {
				if cmd.GetCompact().GetSessionId() != "s-1" {
					t.Fatalf("expected Compact(s-1), got %+v", cmd.Kind)
				}
			},
			slotField: func(vm *corev1.SessionViewModel) *corev1.SessionCommandState { return vm.Actions.Compact },
		},
		{
			name: "set_approval_mode",
			event: &corev1.Event{
				NowMs: 1_791_078_061_000,
				Event: &corev1.Event_SessionSetApprovalMode{
					SessionSetApprovalMode: &corev1.SessionSetApprovalModeAction{
						SessionId: "s-1",
						Mode:      v1.ApprovalMode_APPROVAL_MODE_AUTO,
					},
				},
			},
			checkCmd: func(t *testing.T, cmd *v1.Command) {
				if cmd.GetSetApprovalMode().GetMode() != v1.ApprovalMode_APPROVAL_MODE_AUTO {
					t.Fatalf("expected SetApprovalMode(AUTO), got %+v", cmd.Kind)
				}
			},
			slotField: func(vm *corev1.SessionViewModel) *corev1.SessionCommandState { return vm.Actions.SetMode },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := makeSemanticTestCore(t, sess, nil)
			batch := c.Dispatch(tc.event)
			effs, cmds := decodeDispatchedCommands(t, batch)
			if len(cmds) != 1 {
				t.Fatalf("expected 1 dispatched command, got %d", len(cmds))
			}
			if effs[0].MachineId != "m-on" {
				t.Fatalf("expected auto-resolved machine_id m-on, got %q", effs[0].MachineId)
			}
			tc.checkCmd(t, cmds[0])

			// Duplicate click while in flight is deduplicated.
			dupBatch := c.Dispatch(tc.event)
			_, dupCmds := decodeDispatchedCommands(t, dupBatch)
			if len(dupCmds) != 0 {
				t.Fatalf("expected in-flight duplicate to be dropped, got %d cmds", len(dupCmds))
			}

			// Settling OK increments succeeded_seq.
			settleCommandOK(c, 1_791_078_061_200, effs[0], &v1.CommandResult{Outcome: "executed"})
			vm, err := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-1"}}})
			if err != nil {
				t.Fatalf("View(Session): %v", err)
			}
			slot := tc.slotField(vm.GetSession())
			if slot.SucceededSeq != 1 {
				t.Fatalf("expected succeeded_seq=1, got %d", slot.SucceededSeq)
			}
		})
	}
}

func TestSessionRenameSanitizationAndNoOpShortCircuit(t *testing.T) {
	sess := &corev1.PlainSession{
		SessionId: "s-ren",
		MachineId: "m-on",
		UpdatedMs: 1_791_078_060_000,
		Meta: &v1.Session{
			Id:    "s-ren",
			Cwd:   "/Users/me/proj",
			Title: "Existing Title",
			Agent: v1.Agent_AGENT_CLAUDE,
		},
	}
	c := makeSemanticTestCore(t, sess, nil)

	// 1. Same title after trim/newline normalization short-circuits without network command and increments succeeded_seq.
	bNoop := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_061_000,
		Event: &corev1.Event_SessionRename{
			SessionRename: &corev1.SessionRenameAction{SessionId: "s-ren", Title: "  Existing\nTitle  "},
		},
	})
	if _, cmds := decodeDispatchedCommands(t, bNoop); len(cmds) != 0 {
		t.Fatalf("expected no-op rename to skip network, got %d commands", len(cmds))
	}
	vm, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-ren"}}})
	if vm.GetSession().GetActions().GetRename().GetSucceededSeq() != 1 {
		t.Fatalf("expected succeeded_seq=1 on no-op rename, got %d", vm.GetSession().GetActions().GetRename().GetSucceededSeq())
	}

	// 2. >60 Unicode code points (CJK + emoji + newlines) is clamped to 60 code points.
	longInput := "  第一行\r\n" + strings.Repeat("🔥", 70)
	bLong := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_061_100,
		Event: &corev1.Event_SessionRename{
			SessionRename: &corev1.SessionRenameAction{SessionId: "s-ren", Title: longInput},
		},
	})
	effs, cmds := decodeDispatchedCommands(t, bLong)
	if len(cmds) != 1 || cmds[0].GetRenameSession() == nil {
		t.Fatalf("expected RenameSession command, got %d", len(cmds))
	}
	gotTitle := cmds[0].GetRenameSession().Title
	if gotRunes := len([]rune(gotTitle)); gotRunes != 60 {
		t.Fatalf("expected 60 runes, got %d (%q)", gotRunes, gotTitle)
	}
	if strings.ContainsAny(gotTitle, "\r\n") {
		t.Fatalf("expected newlines collapsed, got %q", gotTitle)
	}
	settleCommandOK(c, 1_791_078_061_200, effs[0], &v1.CommandResult{Outcome: "executed"})
	vm2, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-ren"}}})
	if vm2.GetSession().GetActions().GetRename().GetSucceededSeq() != 2 {
		t.Fatalf("expected succeeded_seq=2 after settled rename, got %d", vm2.GetSession().GetActions().GetRename().GetSucceededSeq())
	}
}

func TestSessionSetModelEffortFallbackAndFolding(t *testing.T) {
	models := []*v1.ModelInfo{
		{Slug: "m1", DisplayName: "Model 1", Efforts: []string{"low", "medium", "high"}, DefaultEffort: "medium"},
		{Slug: "m2", DisplayName: "Model 2", Efforts: []string{"medium", "max"}, DefaultEffort: "max"},
		{Slug: "m3", DisplayName: "Model 3", Efforts: nil, DefaultEffort: ""},
		{Slug: "m4", DisplayName: "Model 4", Efforts: []string{"low", "high"}, DefaultEffort: "low"},
		{Slug: "m5", DisplayName: "Model 5", Efforts: []string{"low", "high"}, DefaultEffort: "high"},
		{Slug: "m6", DisplayName: "Model 6", Efforts: []string{"low", "high"}, DefaultEffort: "high"},
	}
	sess := &corev1.PlainSession{
		SessionId: "s-mod",
		MachineId: "m-on",
		UpdatedMs: 1_791_078_060_000,
		Meta: &v1.Session{
			Id:     "s-mod",
			Cwd:    "/Users/me/proj",
			Agent:  v1.Agent_AGENT_CLAUDE,
			Model:  "m6",
			Effort: "high",
		},
	}
	c := makeSemanticTestCore(t, sess, models)

	// Verify folded model list (>5 -> first 3 + selected m6).
	vm, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-mod"}}})
	acts := vm.GetSession().GetActions()
	if !acts.HasMoreModels {
		t.Fatalf("expected has_more_models=true for 6 models")
	}
	var foldedSlugs []string
	for _, mc := range acts.FoldedModelChoices {
		foldedSlugs = append(foldedSlugs, mc.Slug)
	}
	wantFolded := []string{"m1", "m2", "m3", "m6"}
	if !slices.Equal(foldedSlugs, wantFolded) {
		t.Fatalf("folded slugs = %v, want %v", foldedSlugs, wantFolded)
	}

	// Table-driven effort fallback tests when effort=="" in SessionSetModelAction:
	cases := []struct {
		name       string
		model      string
		effortIn   string
		wantEffort string
	}{
		{name: "keep_supported_current", model: "m1", effortIn: "", wantEffort: "high"},
		{name: "fallback_to_default", model: "m2", effortIn: "", wantEffort: "max"},
		{name: "no_efforts_empty", model: "m3", effortIn: "", wantEffort: ""},
		{name: "explicit_effort_honored", model: "m2", effortIn: "medium", wantEffort: "medium"},
	}
	now := int64(1_791_078_062_000)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := c.Dispatch(&corev1.Event{
				NowMs: now,
				Event: &corev1.Event_SessionSetModel{
					SessionSetModel: &corev1.SessionSetModelAction{
						SessionId: "s-mod",
						Model:     tc.model,
						Effort:    tc.effortIn,
					},
				},
			})
			effs, cmds := decodeDispatchedCommands(t, b)
			if len(cmds) != 1 || cmds[0].GetSetModel() == nil {
				t.Fatalf("expected SetModel command, got %d", len(cmds))
			}
			if got := cmds[0].GetSetModel().Effort; got != tc.wantEffort {
				t.Fatalf("SetModel.Effort = %q, want %q", got, tc.wantEffort)
			}
			now += 100
			settleCommandOK(c, now, effs[0], &v1.CommandResult{Outcome: "executed"})
			now += 100
		})
	}
}

func TestSessionMultiStepQuestionAndRetrySemantics(t *testing.T) {
	sess := &corev1.PlainSession{
		SessionId: "s-q",
		MachineId: "m-on",
		UpdatedMs: 1_791_078_060_000,
		Meta: &v1.Session{
			Id:    "s-q",
			Cwd:   "/Users/me/proj",
			Agent: v1.Agent_AGENT_CLAUDE,
		},
		Checkpoint: &v1.LiveSnapshot{
			Actions: []*v1.Event{
				{
					Seq: 42,
					Kind: &v1.Event_QuestionAsked{
						QuestionAsked: &v1.QuestionAsked{
							QuestionId: "q-1",
							Questions: []*v1.Question{
								{
									Header:      "Step 1",
									Text:        "Pick many",
									MultiSelect: true,
									Options: []*v1.QuestionOption{
										{Label: "Alpha"},
										{Label: "Beta"},
										{Label: "Gamma"},
									},
								},
								{
									Header: "Step 2",
									Text:   "Pick one",
									Options: []*v1.QuestionOption{
										{Label: "Yes"},
										{Label: "No"},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	c := makeSemanticTestCore(t, sess, nil)

	// Toggle options 3 and 1 -> sorted [1, 3].
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_061_000,
		Event: &corev1.Event_SessionToggleQuestionOption{
			SessionToggleQuestionOption: &corev1.SessionToggleQuestionOptionAction{SessionId: "s-q", QuestionId: "q-1", OptionNum: 3},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_061_010,
		Event: &corev1.Event_SessionToggleQuestionOption{
			SessionToggleQuestionOption: &corev1.SessionToggleQuestionOptionAction{SessionId: "s-q", QuestionId: "q-1", OptionNum: 1},
		},
	})
	vm0, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-q"}}})
	qDock0 := vm0.GetSession().GetDock().GetQuestion()
	if !slices.Equal(qDock0.DraftedOptionNums, []int32{1, 3}) || !qDock0.CanSubmitMulti || qDock0.IsLastStep {
		t.Fatalf("unexpected multi draft state: drafted=%v canSubmit=%v isLast=%v", qDock0.DraftedOptionNums, qDock0.CanSubmitMulti, qDock0.IsLastStep)
	}

	// Submit step 0 (using drafted options) -> does NOT advance index yet!
	b1 := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_061_050,
		Event: &corev1.Event_SessionAnswerQuestion{
			SessionAnswerQuestion: &corev1.SessionAnswerQuestionAction{SessionId: "s-q", QuestionId: "q-1"},
		},
	})
	effs1, cmds1 := decodeDispatchedCommands(t, b1)
	if len(cmds1) != 1 || cmds1[0].GetAnswerQuestion() == nil {
		t.Fatalf("expected AnswerQuestion cmd")
	}
	aq1 := cmds1[0].GetAnswerQuestion()
	if !slices.Equal(aq1.Options, []int32{1, 3}) || aq1.ObservedEventSeq != 42 {
		t.Fatalf("unexpected AnswerQuestion payload: options=%v seq=%d", aq1.Options, aq1.ObservedEventSeq)
	}
	vmMid, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-q"}}})
	if vmMid.GetSession().GetDock().GetQuestion().ActiveIndex != 0 {
		t.Fatalf("must not advance active_index before settle")
	}

	// Fail (not forwarded) -> still index 0; retry allocates NEW request_id.
	settleCommandFailed(c, 1_791_078_061_100, effs1[0], "failed_precondition")
	bRetryFail := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_061_200,
		Event: &corev1.Event_SessionRetryCommand{
			SessionRetryCommand: &corev1.SessionRetryCommandAction{
				SessionId: "s-q",
				SlotKey:   "question:s-q:q-1#0",
			},
		},
	})
	effs2, _ := decodeDispatchedCommands(t, bRetryFail)
	if len(effs2) != 1 || effs2[0].RequestId == effs1[0].RequestId {
		t.Fatalf("FAILED retry should allocate a new request_id (prev=%s next=%v)", effs1[0].RequestId, effs2)
	}

	// Unknown outcome -> retry reuses SAME request_id!
	settleCommandUnknown(c, 1_791_078_061_300, effs2[0])
	bRetryUnk := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_061_400,
		Event: &corev1.Event_SessionRetryCommand{
			SessionRetryCommand: &corev1.SessionRetryCommandAction{
				SessionId: "s-q",
				SlotKey:   "question:s-q:q-1#0",
			},
		},
	})
	effs3, _ := decodeDispatchedCommands(t, bRetryUnk)
	if len(effs3) != 1 || effs3[0].RequestId != effs2[0].RequestId {
		t.Fatalf("UNKNOWN retry must reuse same request_id (want=%s got=%v)", effs2[0].RequestId, effs3)
	}

	// Settle OK -> NOW advances to step 1 (is_last_step=true)!
	settleCommandOK(c, 1_791_078_061_500, effs3[0], &v1.CommandResult{Outcome: "executed"})
	vmStep1, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-q"}}})
	qDock1 := vmStep1.GetSession().GetDock().GetQuestion()
	if qDock1.ActiveIndex != 1 || !qDock1.IsLastStep || qDock1.Answered {
		t.Fatalf("expected active_index=1 & is_last_step=true & answered=false after step 0 settle, got idx=%d last=%v answered=%v", qDock1.ActiveIndex, qDock1.IsLastStep, qDock1.Answered)
	}

	// Submit last step (step 1) and settle OK BEFORE QuestionAnswered removes the question from projection.
	// Must stay on last step with is_last_step=true, answered=true, and succeeded_seq incremented — never wrap back to step 0!
	bLast := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_061_600,
		Event: &corev1.Event_SessionAnswerQuestion{
			SessionAnswerQuestion: &corev1.SessionAnswerQuestionAction{
				SessionId:  "s-q",
				QuestionId: "q-1",
				OptionNum:  1,
			},
		},
	})
	effsLast, cmdsLast := decodeDispatchedCommands(t, bLast)
	if len(effsLast) != 1 || len(cmdsLast) != 1 || cmdsLast[0].GetAnswerQuestion() == nil {
		t.Fatalf("expected AnswerQuestion cmd for step 1")
	}
	settleCommandOK(c, 1_791_078_061_700, effsLast[0], &v1.CommandResult{Outcome: "executed"})

	vmAfterLast, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-q"}}})
	qDockAfterLast := vmAfterLast.GetSession().GetDock().GetQuestion()
	if qDockAfterLast == nil {
		t.Fatalf("expected question dock still visible before projection removes q-1")
	}
	if qDockAfterLast.ActiveIndex != 1 || !qDockAfterLast.IsLastStep || !qDockAfterLast.Answered {
		t.Fatalf("last step after settle must stay on index 1 with is_last_step=true & answered=true, got idx=%d last=%v answered=%v",
			qDockAfterLast.ActiveIndex, qDockAfterLast.IsLastStep, qDockAfterLast.Answered)
	}
	if qDockAfterLast.GetAnsweredText().GetKey() != text.KeyDockQuestionAnswered {
		t.Fatalf("expected answered_text key %q, got %+v", text.KeyDockQuestionAnswered, qDockAfterLast.GetAnsweredText())
	}
	if vmAfterLast.GetSession().GetDock().GetCommand().GetSucceededSeq() < 1 {
		t.Fatalf("expected last step command succeeded_seq >= 1, got %d", vmAfterLast.GetSession().GetDock().GetCommand().GetSucceededSeq())
	}
	// Further clicks/submits while answered must be ignored.
	bDup := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_061_800,
		Event: &corev1.Event_SessionAnswerQuestion{
			SessionAnswerQuestion: &corev1.SessionAnswerQuestionAction{
				SessionId:  "s-q",
				QuestionId: "q-1",
				OptionNum:  2,
			},
		},
	})
	if effsDup, _ := decodeDispatchedCommands(t, bDup); len(effsDup) != 0 {
		t.Fatalf("expected no command dispatched after last step already answered, got %d", len(effsDup))
	}
}

func TestSessionApprovalExternalResolutionByOrigin(t *testing.T) {
	cases := []struct {
		name           string
		origin         *v1.Origin
		wantOutcome    corev1.ResolutionOutcome
		wantTextKey    string
		wantNotice     bool
	}{
		{
			name:        "other_client",
			origin:      &v1.Origin{Kind: v1.OriginKind_ORIGIN_KIND_CLIENT, DeviceId: "dev-other"},
			wantOutcome: corev1.ResolutionOutcome_RESOLUTION_OUTCOME_OTHER_CLIENT,
			wantTextKey: text.KeyApprovalHandledOtherDevice,
			wantNotice:  true,
		},
		{
			name:        "terminal_local",
			origin:      &v1.Origin{Kind: v1.OriginKind_ORIGIN_KIND_LOCAL},
			wantOutcome: corev1.ResolutionOutcome_RESOLUTION_OUTCOME_TERMINAL,
			wantTextKey: text.KeyApprovalHandledInTerminal,
			wantNotice:  true,
		},
		{
			name:        "this_client_no_external_banner",
			origin:      &v1.Origin{Kind: v1.OriginKind_ORIGIN_KIND_CLIENT, DeviceId: "dev-1"},
			wantOutcome: corev1.ResolutionOutcome_RESOLUTION_OUTCOME_UNSPECIFIED,
			wantNotice:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sess := &corev1.PlainSession{
				SessionId: "s-ap",
				MachineId: "m-on",
				UpdatedMs: 1_791_078_060_000,
				LastSeq:   1,
				Meta: &v1.Session{
					Id:    "s-ap",
					Cwd:   "/Users/me/proj",
					Agent: v1.Agent_AGENT_CLAUDE,
				},
			}
			c := makeSemanticTestCore(t, sess, nil)
			// Watch session and load initial event with pending approval.
			watchBatch := c.Dispatch(&corev1.Event{
				NowMs: 1_791_078_060_100,
				Event: &corev1.Event_SessionWatch{SessionWatch: &corev1.SessionWatchEvent{SessionId: "s-ap", Watched: true}},
			})
			cacheOp := watchCacheReadOpID(watchBatch)
			c.Dispatch(&corev1.Event{
				NowMs: 1_791_078_060_110,
				Event: &corev1.Event_CacheEventsLoaded{
					CacheEventsLoaded: &corev1.CacheEventsLoadedEvent{
						OpId:      cacheOp,
						SessionId: "s-ap",
						Events: []*corev1.PlainEvent{
							{
								Seq:      1,
								Verified: true,
								Event: &v1.Event{
									Seq: 1,
									Kind: &v1.Event_ApprovalRequested{
										ApprovalRequested: &v1.ApprovalRequested{
											ApprovalId: "ap-1",
											Command:    "rm -rf build",
										},
									},
								},
							},
						},
					},
				},
			})

			// User opens deny form (no wall clock needed!).
			c.Dispatch(&corev1.Event{
				NowMs: 1_791_078_060_200,
				Event: &corev1.Event_SessionEditApprovalDeny{
					SessionEditApprovalDeny: &corev1.SessionEditApprovalDenyAction{
						SessionId:   "s-ap",
						ApprovalId:  "ap-1",
						DenyingOpen: true,
					},
				},
			})

			// Event arrives resolving approval with tc.origin.
			evBatch := c.Dispatch(&corev1.Event{
				NowMs: 1_791_078_060_300,
				Event: &corev1.Event_StreamFrame{
					StreamFrame: &corev1.StreamFrameEvent{
						LegId: 1,
						Frame: &corev1.StreamFrameEvent_EventsAppended{
							EventsAppended: &corev1.EventsAppendedFrame{
								SessionId: "s-ap",
								Events: []*corev1.PlainEvent{
									{
										Seq:      2,
										Verified: true,
										Event: &v1.Event{
											Seq:    2,
											Origin: tc.origin,
											Kind: &v1.Event_ApprovalResolved{
												ApprovalResolved: &v1.ApprovalResolved{
													ApprovalId: "ap-1",
													Decision:   v1.ApprovalDecision_APPROVAL_DECISION_ALLOW,
												},
											},
										},
									},
								},
							},
						},
					},
				},
			})

			vm, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-ap"}}})
			ext := vm.GetSession().GetDock().GetExternalResolution()
			if !tc.wantNotice {
				if ext != nil {
					t.Fatalf("expected no external_resolution, got %+v", ext)
				}
				return
			}
			if ext == nil || ext.Outcome != tc.wantOutcome || ext.GetText().GetKey() != tc.wantTextKey {
				t.Fatalf("unexpected external_resolution: %+v", ext)
			}
			tmr := findUITransientTimer(evBatch, "session-ext:s-ap")
			if tmr == nil || tmr.DelayMs != 1200 {
				t.Fatalf("expected 1200ms TIMER_KIND_UI_TRANSIENT, got %+v", tmr)
			}

			// Firing the timer clears the external notice.
			c.Dispatch(&corev1.Event{
				NowMs: 1_791_078_061_500,
				Event: &corev1.Event_TimerFired{
					TimerFired: &corev1.TimerFiredEvent{
						TimerId: tmr.TimerId,
						Kind:    corev1.TimerKind_TIMER_KIND_UI_TRANSIENT,
						Key:     tmr.Key,
					},
				},
			})
			vmAfter, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-ap"}}})
			if vmAfter.GetSession().GetDock().GetExternalResolution() != nil {
				t.Fatalf("expected external_resolution cleared after timer")
			}
		})
	}
}

func TestHomeQuestionFocusLifecycleAndRetry(t *testing.T) {
	sess := &corev1.PlainSession{
		SessionId: "s-hq",
		MachineId: "m-on",
		UpdatedMs: 1_791_078_060_000,
		Meta: &v1.Session{
			Id:    "s-hq",
			Cwd:   "/Users/me/proj",
			Agent: v1.Agent_AGENT_CLAUDE,
		},
		Checkpoint: &v1.LiveSnapshot{
			Actions: []*v1.Event{
				{
					Seq: 77,
					Kind: &v1.Event_QuestionAsked{
						QuestionAsked: &v1.QuestionAsked{
							QuestionId: "hq-1",
							Questions: []*v1.Question{
								{
									Header: "Deploy",
									Text:   "Ship now?",
									Options: []*v1.QuestionOption{
										{Label: "Yes (Recommended)"},
										{Label: "Later"},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	c := makeSemanticTestCore(t, sess, nil)

	// Home renders inline question card in IDLE phase without watchSession.
	h0, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_Home{Home: &corev1.HomeScreenRequest{}}})
	q0 := h0.GetHome().GetTopAttentionQuestion()
	if q0 == nil || q0.QuestionId != "hq-1" || q0.Phase != corev1.HomeFocusPhase_HOME_FOCUS_PHASE_IDLE {
		t.Fatalf("expected IDLE HomeQuestionCardView, got %+v", q0)
	}

	// Answer option 1 from Home.
	bAns := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_061_000,
		Event: &corev1.Event_HomeAnswerQuestion{
			HomeAnswerQuestion: &corev1.HomeAnswerQuestionAction{
				SessionId:  "s-hq",
				QuestionId: "hq-1",
				OptionNum:  1,
			},
		},
	})
	effs, cmds := decodeDispatchedCommands(t, bAns)
	if len(cmds) != 1 || cmds[0].GetAnswerQuestion().GetOption() != 1 || cmds[0].GetAnswerQuestion().GetObservedEventSeq() != 77 {
		t.Fatalf("unexpected HomeAnswerQuestion command: %+v", cmds)
	}

	hSub, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_Home{Home: &corev1.HomeScreenRequest{}}})
	if hSub.GetHome().GetTopAttentionQuestion().GetPhase() != corev1.HomeFocusPhase_HOME_FOCUS_PHASE_SUBMITTING ||
		hSub.GetHome().GetTopAttentionQuestion().GetPickedOptionNum() != 1 {
		t.Fatalf("expected SUBMITTING phase with picked=1, got %+v", hSub.GetHome().GetTopAttentionQuestion())
	}

	// Fail -> FAILED phase -> HomeRetryAttention retries and succeeds -> RESOLVED_THIS_CLIENT + 900ms hold timer.
	settleCommandFailed(c, 1_791_078_061_100, effs[0], "failed_precondition")
	hFail, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_Home{Home: &corev1.HomeScreenRequest{}}})
	if hFail.GetHome().GetTopAttentionQuestion().GetPhase() != corev1.HomeFocusPhase_HOME_FOCUS_PHASE_FAILED {
		t.Fatalf("expected FAILED phase, got %v", hFail.GetHome().GetTopAttentionQuestion().GetPhase())
	}

	bRetry := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_061_200,
		Event: &corev1.Event_HomeRetryAttention{
			HomeRetryAttention: &corev1.HomeRetryAttentionAction{SessionId: "s-hq", ItemId: "hq-1"},
		},
	})
	effsRetry, _ := decodeDispatchedCommands(t, bRetry)
	if len(effsRetry) != 1 {
		t.Fatalf("expected 1 retry command, got %d", len(effsRetry))
	}
	bOK := settleCommandOK(c, 1_791_078_061_300, effsRetry[0], &v1.CommandResult{Outcome: "executed"})
	tmr := findUITransientTimer(bOK, homeFocusHoldTimerKey)
	if tmr == nil || tmr.DelayMs != 900 {
		t.Fatalf("expected 900ms home-focus-hold timer, got %+v", tmr)
	}

	// Even if session updates to remove pending question, Home holds RESOLVED_THIS_CLIENT card for 900ms.
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_061_350,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: 1,
				Frame: &corev1.StreamFrameEvent_SessionUpdated{
					SessionUpdated: &corev1.PlainSession{
						SessionId:  "s-hq",
						MachineId:  "m-on",
						UpdatedMs:  1_791_078_061_350,
						Meta:       sess.Meta,
						Checkpoint: &v1.LiveSnapshot{},
					},
				},
			},
		},
	})
	hHeld, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_Home{Home: &corev1.HomeScreenRequest{}}})
	qHeld := hHeld.GetHome().GetTopAttentionQuestion()
	if qHeld == nil || qHeld.Phase != corev1.HomeFocusPhase_HOME_FOCUS_PHASE_RESOLVED_THIS_CLIENT ||
		qHeld.GetResolutionText().GetKey() != text.KeyHomeQuestionAnsweredLine {
		t.Fatalf("expected held RESOLVED_THIS_CLIENT card, got %+v", qHeld)
	}

	// Timer expires -> card clears!
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_062_250,
		Event: &corev1.Event_TimerFired{
			TimerFired: &corev1.TimerFiredEvent{
				TimerId: tmr.TimerId,
				Kind:    corev1.TimerKind_TIMER_KIND_UI_TRANSIENT,
				Key:     tmr.Key,
			},
		},
	})
	hGone, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_Home{Home: &corev1.HomeScreenRequest{}}})
	if hGone.GetHome().GetTopAttentionQuestion() != nil {
		t.Fatalf("expected Home question card cleared after 900ms hold timer")
	}
}

func TestSessionFrozenDenyReasonChangeAutoAbandons(t *testing.T) {
	sess := &corev1.PlainSession{
		SessionId: "s-deny",
		MachineId: "m-on",
		UpdatedMs: 1_791_078_060_000,
		Meta:      &v1.Session{Id: "s-deny", Cwd: "/Users/me/proj", Agent: v1.Agent_AGENT_CLAUDE},
		Checkpoint: &v1.LiveSnapshot{
			Actions: []*v1.Event{
				{
					Seq: 5,
					Kind: &v1.Event_ApprovalRequested{
						ApprovalRequested: &v1.ApprovalRequested{ApprovalId: "ap-d", Command: "sudo reboot"},
					},
				},
			},
		},
	}
	c := makeSemanticTestCore(t, sess, nil)
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_061_000,
		Event: &corev1.Event_SessionEditApprovalDeny{
			SessionEditApprovalDeny: &corev1.SessionEditApprovalDenyAction{SessionId: "s-deny", ApprovalId: "ap-d", DenyingOpen: true},
		},
	})
	b1 := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_061_010,
		Event: &corev1.Event_SessionResolveApproval{
			SessionResolveApproval: &corev1.SessionResolveApprovalAction{
				SessionId:  "s-deny",
				ApprovalId: "ap-d",
				Allow:      false,
				DenyReason: "  first reason  ",
			},
		},
	})
	effs1, cmds1 := decodeDispatchedCommands(t, b1)
	if len(cmds1) != 1 || cmds1[0].GetResolveApproval().GetMessage() != "first reason" {
		t.Fatalf("expected trimmed deny reason 'first reason', got %+v", cmds1)
	}
	settleCommandUnknown(c, 1_791_078_061_100, effs1[0])

	// Hold open card must be true during failed/unknown deny.
	vm, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-deny"}}})
	if !vm.GetSession().GetDock().GetApproval().GetHoldOpenCard() {
		t.Fatalf("expected hold_open_card=true on failed deny")
	}

	// Submitting a different deny reason auto-abandons old frozen attempt and allocates new request_id.
	b2 := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_061_200,
		Event: &corev1.Event_SessionResolveApproval{
			SessionResolveApproval: &corev1.SessionResolveApprovalAction{
				SessionId:  "s-deny",
				ApprovalId: "ap-d",
				Allow:      false,
				DenyReason: "second reason",
			},
		},
	})
	effs2, cmds2 := decodeDispatchedCommands(t, b2)
	if len(effs2) != 1 || effs2[0].RequestId == effs1[0].RequestId || cmds2[0].GetResolveApproval().GetMessage() != "second reason" {
		t.Fatalf("expected fresh request_id with updated deny reason, prev=%s got=%+v", effs1[0].RequestId, effs2)
	}
}
