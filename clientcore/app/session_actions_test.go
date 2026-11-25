package app

import (
	"slices"
	"strings"
	"testing"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/screens"
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
		running   bool
		event     *corev1.Event
		checkCmd  func(t *testing.T, cmd *v1.Command)
		slotField func(vm *corev1.SessionViewModel) *corev1.SessionCommandState
	}{
		{
			name:    "interrupt",
			running: true,
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
			s := *sess
			s.Checkpoint = &v1.LiveSnapshot{Running: tc.running}
			c := makeSemanticTestCore(t, &s, nil)
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
			name:        "system_neutral_handled",
			origin:      &v1.Origin{Kind: v1.OriginKind_ORIGIN_KIND_SYSTEM},
			wantOutcome: corev1.ResolutionOutcome_RESOLUTION_OUTCOME_SYSTEM,
			wantTextKey: text.KeyApprovalHandled,
			wantNotice:  true,
		},
		{
			name:        "unspecified_no_external_banner",
			origin:      &v1.Origin{Kind: v1.OriginKind_ORIGIN_KIND_UNSPECIFIED},
			wantOutcome: corev1.ResolutionOutcome_RESOLUTION_OUTCOME_UNSPECIFIED,
			wantNotice:  false,
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
	tmr := findUITransientTimer(bOK, homeFocusHoldKey(screens.HomeFocusQuestion, "s-hq", "hq-1"))
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

func TestSessionFrozenSlotPayloadChangesAndExpiry(t *testing.T) {
	sess := &corev1.PlainSession{
		SessionId: "s-frz",
		MachineId: "m-on",
		UpdatedMs: 1_791_078_060_000,
		Meta: &v1.Session{
			Id:           "s-frz",
			Cwd:          "/Users/me/proj",
			Agent:        v1.Agent_AGENT_CLAUDE,
			Title:        "Original",
			Model: "claude-haiku-4-5",
			Mode:  v1.ApprovalMode_APPROVAL_MODE_ASK,
		},
		Checkpoint: &v1.LiveSnapshot{},
	}
	models := []*v1.ModelInfo{
		{Slug: "claude-haiku-4-5", DisplayName: "Haiku 4.5"},
		{Slug: "claude-opus-4-6", DisplayName: "Opus 4.6", Efforts: []string{"medium", "high"}, DefaultEffort: "medium"},
		{Slug: "claude-sonnet-4-6", DisplayName: "Sonnet 4.6", Efforts: []string{"medium", "high"}, DefaultEffort: "high"},
	}
	c := makeSemanticTestCore(t, sess, models)

	// 1) SetModel A -> UNKNOWN (frozen) -> same payload A reuses request_id; different payload B abandons A & allocates new request_id.
	bModA := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_061_000,
		Event: &corev1.Event_SessionSetModel{
			SessionSetModel: &corev1.SessionSetModelAction{SessionId: "s-frz", Model: "claude-opus-4-6"},
		},
	})
	effsA, cmdsA := decodeDispatchedCommands(t, bModA)
	if len(effsA) != 1 || cmdsA[0].GetSetModel().GetModel() != "claude-opus-4-6" {
		t.Fatalf("expected SetModel Opus, got %+v", cmdsA)
	}
	settleCommandUnknown(c, 1_791_078_061_100, effsA[0])

	// Re-submitting identical model A replays frozen attempt with the SAME request_id.
	bModARetry := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_061_200,
		Event: &corev1.Event_SessionSetModel{
			SessionSetModel: &corev1.SessionSetModelAction{SessionId: "s-frz", Model: "claude-opus-4-6"},
		},
	})
	effsARetry, cmdsARetry := decodeDispatchedCommands(t, bModARetry)
	if len(effsARetry) != 1 || effsARetry[0].RequestId != effsA[0].RequestId || cmdsARetry[0].GetSetModel().GetModel() != "claude-opus-4-6" {
		t.Fatalf("same model payload on frozen slot must replay same request_id; prev=%s got=%+v", effsA[0].RequestId, effsARetry)
	}
	settleCommandUnknown(c, 1_791_078_061_300, effsARetry[0])

	// Submitting DIFFERENT model B must abandon A and dispatch B with a NEW request_id!
	bModB := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_061_400,
		Event: &corev1.Event_SessionSetModel{
			SessionSetModel: &corev1.SessionSetModelAction{SessionId: "s-frz", Model: "claude-sonnet-4-6"},
		},
	})
	effsB, cmdsB := decodeDispatchedCommands(t, bModB)
	if len(effsB) != 1 || effsB[0].RequestId == effsA[0].RequestId || cmdsB[0].GetSetModel().GetModel() != "claude-sonnet-4-6" {
		t.Fatalf("different model payload on frozen slot must abandon & dispatch new request_id; prev=%s got=%+v cmds=%+v", effsA[0].RequestId, effsB, cmdsB)
	}
	vmModB, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-frz"}}})
	if vmModB.GetSession().GetActions().GetSetModel().GetSubmittedSummary().GetLiteral() != "Sonnet 4.6" {
		t.Fatalf("expected SubmittedSummary='Sonnet 4.6', got %+v", vmModB.GetSession().GetActions().GetSetModel().GetSubmittedSummary())
	}

	// Expire model B's frozen slot (settle UNKNOWN past RecoveryDeadlineMs = 360s).
	settleCommandUnknown(c, 1_791_078_061_500, effsB[0])
	// Retry after recovery deadline triggers Expired=true.
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_500_000,
		Event: &corev1.Event_SessionRetryCommand{
			SessionRetryCommand: &corev1.SessionRetryCommandAction{SessionId: "s-frz", SlotKey: "set_model:s-frz"},
		},
	})
	vmExp, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-frz"}}})
	actExp := vmExp.GetSession().GetActions()
	if actExp.CanSwitchModel || actExp.GetModelSwitchBlockedReason() == nil {
		t.Fatalf("expired frozen set_model slot must set CanSwitchModel=false with blocked reason, got can=%v reason=%+v",
			actExp.CanSwitchModel, actExp.GetModelSwitchBlockedReason())
	}
	bModAfterExp := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_500_100,
		Event: &corev1.Event_SessionSetModel{
			SessionSetModel: &corev1.SessionSetModelAction{SessionId: "s-frz", Model: "claude-opus-4-6"},
		},
	})
	if effs, _ := decodeDispatchedCommands(t, bModAfterExp); len(effs) != 0 {
		t.Fatalf("expired frozen set_model slot must reject new input until abandoned, got %d cmds", len(effs))
	}

	// 2) Rename frozen -> changing title abandons old frozen attempt and dispatches new title.
	bRenA := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_500_200,
		Event: &corev1.Event_SessionRename{
			SessionRename: &corev1.SessionRenameAction{SessionId: "s-frz", Title: "Title A"},
		},
	})
	effsRenA, _ := decodeDispatchedCommands(t, bRenA)
	settleCommandUnknown(c, 1_791_078_500_300, effsRenA[0])
	bRenB := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_500_400,
		Event: &corev1.Event_SessionRename{
			SessionRename: &corev1.SessionRenameAction{SessionId: "s-frz", Title: "Title B"},
		},
	})
	effsRenB, cmdsRenB := decodeDispatchedCommands(t, bRenB)
	if len(effsRenB) != 1 || effsRenB[0].RequestId == effsRenA[0].RequestId || cmdsRenB[0].GetRenameSession().GetTitle() != "Title B" {
		t.Fatalf("different rename payload on frozen slot must abandon & send new request_id; prev=%s got=%+v", effsRenA[0].RequestId, effsRenB)
	}

	// 3) SetApprovalMode frozen -> changing mode abandons old frozen attempt and dispatches new mode.
	bModeA := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_500_500,
		Event: &corev1.Event_SessionSetApprovalMode{
			SessionSetApprovalMode: &corev1.SessionSetApprovalModeAction{SessionId: "s-frz", Mode: v1.ApprovalMode_APPROVAL_MODE_AUTO},
		},
	})
	effsModeA, _ := decodeDispatchedCommands(t, bModeA)
	settleCommandUnknown(c, 1_791_078_500_600, effsModeA[0])
	bModeB := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_500_700,
		Event: &corev1.Event_SessionSetApprovalMode{
			SessionSetApprovalMode: &corev1.SessionSetApprovalModeAction{SessionId: "s-frz", Mode: v1.ApprovalMode_APPROVAL_MODE_PLAN},
		},
	})
	effsModeB, cmdsModeB := decodeDispatchedCommands(t, bModeB)
	if len(effsModeB) != 1 || effsModeB[0].RequestId == effsModeA[0].RequestId || cmdsModeB[0].GetSetApprovalMode().GetMode() != v1.ApprovalMode_APPROVAL_MODE_PLAN {
		t.Fatalf("different mode payload on frozen slot must abandon & send new request_id; prev=%s got=%+v", effsModeA[0].RequestId, effsModeB)
	}

	// 4) Compact frozen and expired -> CanCompact=false with CompactBlockedReason, rejects dispatch.
	bCmp := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_500_800,
		Event: &corev1.Event_SessionSimpleCommand{
			SessionSimpleCommand: &corev1.SessionSimpleCommandAction{
				SessionId: "s-frz",
				Kind:      corev1.SessionSimpleCommandKind_SESSION_SIMPLE_COMMAND_KIND_COMPACT,
			},
		},
	})
	effsCmp, _ := decodeDispatchedCommands(t, bCmp)
	settleCommandUnknown(c, 1_791_078_500_900, effsCmp[0])
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_900_000,
		Event: &corev1.Event_SessionRetryCommand{
			SessionRetryCommand: &corev1.SessionRetryCommandAction{SessionId: "s-frz", SlotKey: "compact:s-frz"},
		},
	})
	vmCmpExp, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-frz"}}})
	if vmCmpExp.GetSession().GetActions().CanCompact || vmCmpExp.GetSession().GetActions().GetCompactBlockedReason() == nil {
		t.Fatalf("expired frozen compact slot must set CanCompact=false with CompactBlockedReason, got can=%v reason=%+v",
			vmCmpExp.GetSession().GetActions().CanCompact, vmCmpExp.GetSession().GetActions().GetCompactBlockedReason())
	}
	bCmpAfter := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_900_100,
		Event: &corev1.Event_SessionSimpleCommand{
			SessionSimpleCommand: &corev1.SessionSimpleCommandAction{
				SessionId: "s-frz",
				Kind:      corev1.SessionSimpleCommandKind_SESSION_SIMPLE_COMMAND_KIND_COMPACT,
			},
		},
	})
	if effs, _ := decodeDispatchedCommands(t, bCmpAfter); len(effs) != 0 {
		t.Fatalf("expired frozen compact slot must reject dispatch, got %d cmds", len(effs))
	}
}

func TestHomeFocusStateMachineEdgeCases(t *testing.T) {
	// Case A + B: Deny approval on Home -> projection resolves THIS_CLIENT before settle ->
	// ResolutionText must be KeyApprovalDenied (not KeyApprovalAllowed), AND a late failed settlement
	// must NOT overwrite RESOLVED_THIS_CLIENT back to FAILED.
	// Case C: Stale 900ms timer from item 1 must NOT clear item 2's SUBMITTING focus.
	sess1 := &corev1.PlainSession{
		SessionId: "s-hf1",
		MachineId: "m-on",
		UpdatedMs: 1_791_078_059_000,
		Meta:      &v1.Session{Id: "s-hf1", Cwd: "/Users/me/proj", Agent: v1.Agent_AGENT_CLAUDE},
		Checkpoint: &v1.LiveSnapshot{
			Actions: []*v1.Event{
				{
					Seq: 1,
					Kind: &v1.Event_ApprovalRequested{
						ApprovalRequested: &v1.ApprovalRequested{ApprovalId: "ap-1", Command: "rm -rf /tmp/x"},
					},
				},
			},
		},
	}
	sess2 := &corev1.PlainSession{
		SessionId: "s-hf2",
		MachineId: "m-on",
		UpdatedMs: 1_791_078_060_000,
		Meta:      &v1.Session{Id: "s-hf2", Cwd: "/Users/me/proj", Agent: v1.Agent_AGENT_CLAUDE},
		Checkpoint: &v1.LiveSnapshot{
			Actions: []*v1.Event{
				{
					Seq: 1,
					Kind: &v1.Event_ApprovalRequested{
						ApprovalRequested: &v1.ApprovalRequested{ApprovalId: "ap-2", Command: "echo hi"},
					},
				},
			},
		},
	}
	c := bootstrapReadyApp(t, []*corev1.PlainMachine{
		{
			MachineId: "m-on",
			Online:    true,
			Meta: &v1.MachineMeta{
				Hostname:        "DevBox",
				ProtocolVersion: sync.PeerVersion,
				Capabilities:    &v1.MachineCapabilities{ApprovalAlways: true},
			},
		},
	}, []*corev1.PlainSession{sess1, sess2})

	// Deny ap-1 from Home.
	bDeny := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_061_000,
		Event: &corev1.Event_HomeResolveApproval{
			HomeResolveApproval: &corev1.HomeResolveApprovalAction{
				SessionId:  "s-hf1",
				ApprovalId: "ap-1",
				Allow:      false,
				DenyReason: "unsafe",
			},
		},
	})
	effs1, _ := decodeDispatchedCommands(t, bDeny)
	if len(effs1) != 1 {
		t.Fatalf("expected 1 deny command, got %d", len(effs1))
	}

	// Projection resolves ap-1 (checkpoint clears pending approval) BEFORE CommandOutcome arrives.
	bRes := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_061_050,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: 1,
				Frame: &corev1.StreamFrameEvent_SessionUpdated{
					SessionUpdated: &corev1.PlainSession{
						SessionId:  "s-hf1",
						MachineId:  "m-on",
						UpdatedMs:  1_791_078_061_050,
						Meta:       sess1.Meta,
						Checkpoint: &v1.LiveSnapshot{},
					},
				},
			},
		},
	})
	tmr1 := findUITransientTimer(bRes, homeFocusHoldKey(screens.HomeFocusApproval, "s-hf1", "ap-1"))
	if tmr1 == nil {
		t.Fatalf("expected item-scoped hold timer for ap-1")
	}

	// Check ResolutionText is KeyApprovalDenied (NOT KeyApprovalAllowed).
	hAfterRes, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_Home{Home: &corev1.HomeScreenRequest{}}})
	top1 := hAfterRes.GetHome().GetTopAttentionCard()
	if top1 == nil || top1.Phase != corev1.HomeFocusPhase_HOME_FOCUS_PHASE_RESOLVED_THIS_CLIENT ||
		top1.GetResolutionText().GetKey() != text.KeyApprovalDenied {
		t.Fatalf("expected RESOLVED_THIS_CLIENT with KeyApprovalDenied, got %+v", top1)
	}

	// Late failure settlement for ap-1 must NOT overwrite RESOLVED_THIS_CLIENT to FAILED!
	settleCommandFailed(c, 1_791_078_061_100, effs1[0], "failed_precondition")
	hAfterLateFail, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_Home{Home: &corev1.HomeScreenRequest{}}})
	if hAfterLateFail.GetHome().GetTopAttentionCard().GetPhase() != corev1.HomeFocusPhase_HOME_FOCUS_PHASE_RESOLVED_THIS_CLIENT {
		t.Fatalf("late failure must not overwrite RESOLVED_THIS_CLIENT, got %v", hAfterLateFail.GetHome().GetTopAttentionCard().GetPhase())
	}

	// User starts submitting ap-2 on Home BEFORE ap-1's stale timer fires.
	bAllow2 := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_061_200,
		Event: &corev1.Event_HomeResolveApproval{
			HomeResolveApproval: &corev1.HomeResolveApprovalAction{
				SessionId:  "s-hf2",
				ApprovalId: "ap-2",
				Allow:      true,
			},
		},
	})
	if effs2, _ := decodeDispatchedCommands(t, bAllow2); len(effs2) != 1 {
		t.Fatalf("expected 1 allow command for ap-2, got %d", len(effs2))
	}

	// Now ap-1's stale timer fires -> must be ignored and keep ap-2 in SUBMITTING!
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_061_950,
		Event: &corev1.Event_TimerFired{
			TimerFired: &corev1.TimerFiredEvent{
				TimerId: tmr1.TimerId,
				Kind:    corev1.TimerKind_TIMER_KIND_UI_TRANSIENT,
				Key:     tmr1.Key,
			},
		},
	})
	if c.m.state.HomeFocus == nil || c.m.state.HomeFocus.ItemID != "ap-2" ||
		c.m.state.HomeFocus.Phase != corev1.HomeFocusPhase_HOME_FOCUS_PHASE_SUBMITTING {
		t.Fatalf("stale ap-1 timer must not clear ap-2 SUBMITTING focus, got %+v", c.m.state.HomeFocus)
	}

	// Case D: HomeResolveApproval / HomeAnswerQuestion on missing session or skeleton must NOT enter SUBMITTING.
	c.m.state.HomeFocus = nil
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_062_000,
		Event: &corev1.Event_HomeResolveApproval{
			HomeResolveApproval: &corev1.HomeResolveApprovalAction{
				SessionId:  "s-does-not-exist",
				ApprovalId: "ap-missing",
				Allow:      true,
			},
		},
	})
	if c.m.state.HomeFocus != nil {
		t.Fatalf("non-dispatched HomeResolveApproval must not set HomeFocus, got %+v", c.m.state.HomeFocus)
	}
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_062_010,
		Event: &corev1.Event_HomeAnswerQuestion{
			HomeAnswerQuestion: &corev1.HomeAnswerQuestionAction{
				SessionId:  "s-does-not-exist",
				QuestionId: "q-missing",
				OptionNum:  1,
			},
		},
	})
	if c.m.state.HomeFocus != nil {
		t.Fatalf("non-dispatched HomeAnswerQuestion must not set HomeFocus, got %+v", c.m.state.HomeFocus)
	}
}

func TestSessionTrackingMapsStayBounded(t *testing.T) {
	sess := &corev1.PlainSession{
		SessionId: "s-bnd",
		MachineId: "m-on",
		UpdatedMs: 1_791_078_060_000,
		Meta: &v1.Session{
			Id:    "s-bnd",
			Cwd:   "/Users/me/proj",
			Agent: v1.Agent_AGENT_CLAUDE,
			Title: "Initial",
		},
	}
	c := makeSemanticTestCore(t, sess, nil)
	// Provide plenty of entropy IDs for 30 messages + 30 approvals + 1 rename.
	extraIDs := make([]string, 0, 200)
	for i := 0; i < 200; i++ {
		extraIDs = append(extraIDs, "bnd-id-"+string(rune('a'+(i/26)%26))+string(rune('a'+i%26))+string(rune('0'+(i/676)%10)))
	}
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_050,
		Event: &corev1.Event_EntropyProvided{
			EntropyProvided: &corev1.EntropyProvided{RandomIds: extraIDs},
		},
	})
	watchBatch := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_100,
		Event: &corev1.Event_SessionWatch{SessionWatch: &corev1.SessionWatchEvent{SessionId: "s-bnd", Watched: true}},
	})
	cacheOp := watchCacheReadOpID(watchBatch)
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_110,
		Event: &corev1.Event_CacheEventsLoaded{
			CacheEventsLoaded: &corev1.CacheEventsLoadedEvent{OpId: cacheOp, SessionId: "s-bnd"},
		},
	})

	now := int64(1_791_078_061_000)
	var seq uint64 = 1

	// Send 30 messages and settle each OK.
	for i := 0; i < 30; i++ {
		now += 10
		msgReqID := "msg-" + string(rune('a'+i/26)) + string(rune('a'+i%26))
		b := c.Dispatch(&corev1.Event{
			NowMs: now,
			Event: &corev1.Event_SessionSubmitCommand{
				SessionSubmitCommand: &corev1.SessionSubmitCommandAction{
					SessionId: "s-bnd",
					Command: &v1.Command{
						ProtocolVersion: sync.PeerVersion,
						Kind: &v1.Command_Send{
							Send: &v1.SendRequest{
								SessionId: "s-bnd",
								RequestId: msgReqID,
								Text:      "msg",
							},
						},
					},
				},
			},
		})
		effs, _ := decodeDispatchedCommands(t, b)
		if len(effs) != 1 {
			t.Fatalf("send %d: expected 1 command, got %d", i, len(effs))
		}
		now += 10
		settleCommandOK(c, now, effs[0], &v1.CommandResult{Outcome: "executed"})
	}

	// Request, approve (without opening deny box), settle, and resolve 30 approvals.
	for i := 0; i < 30; i++ {
		aid := "ap-" + string(rune('a'+i/26)) + string(rune('a'+i%26))
		apEv := &v1.Event{
			Seq: seq,
			Kind: &v1.Event_ApprovalRequested{
				ApprovalRequested: &v1.ApprovalRequested{ApprovalId: aid, Command: "echo ok"},
			},
		}
		now += 10
		c.Dispatch(&corev1.Event{
			NowMs: now,
			Event: &corev1.Event_StreamFrame{
				StreamFrame: &corev1.StreamFrameEvent{
					LegId: 1,
					Frame: &corev1.StreamFrameEvent_SessionUpdated{
						SessionUpdated: &corev1.PlainSession{
							SessionId:  "s-bnd",
							MachineId:  "m-on",
							LastSeq:    seq,
							UpdatedMs:  now,
							Meta:       sess.Meta,
							Checkpoint: &v1.LiveSnapshot{Actions: []*v1.Event{apEv}},
						},
					},
				},
			},
		})
		c.Dispatch(&corev1.Event{
			NowMs: now,
			Event: &corev1.Event_StreamFrame{
				StreamFrame: &corev1.StreamFrameEvent{
					LegId: 1,
					Frame: &corev1.StreamFrameEvent_EventsAppended{
						EventsAppended: &corev1.EventsAppendedFrame{
							SessionId: "s-bnd",
							Events:    []*corev1.PlainEvent{{Seq: seq, Verified: true, Event: apEv}},
						},
					},
				},
			},
		})
		seq++

		now += 10
		b := c.Dispatch(&corev1.Event{
			NowMs: now,
			Event: &corev1.Event_SessionResolveApproval{
				SessionResolveApproval: &corev1.SessionResolveApprovalAction{
					SessionId:  "s-bnd",
					ApprovalId: aid,
					Allow:      true,
				},
			},
		})
		effs, _ := decodeDispatchedCommands(t, b)
		if len(effs) != 1 {
			t.Fatalf("approval %d: expected 1 command, got %d", i, len(effs))
		}
		now += 10
		settleCommandOK(c, now, effs[0], &v1.CommandResult{Outcome: "executed"})

		now += 10
		c.Dispatch(&corev1.Event{
			NowMs: now,
			Event: &corev1.Event_StreamFrame{
				StreamFrame: &corev1.StreamFrameEvent{
					LegId: 1,
					Frame: &corev1.StreamFrameEvent_SessionUpdated{
						SessionUpdated: &corev1.PlainSession{
							SessionId:  "s-bnd",
							MachineId:  "m-on",
							LastSeq:    seq,
							UpdatedMs:  now,
							Meta:       sess.Meta,
							Checkpoint: &v1.LiveSnapshot{},
						},
					},
				},
			},
		})
		c.Dispatch(&corev1.Event{
			NowMs: now,
			Event: &corev1.Event_StreamFrame{
				StreamFrame: &corev1.StreamFrameEvent{
					LegId: 1,
					Frame: &corev1.StreamFrameEvent_EventsAppended{
						EventsAppended: &corev1.EventsAppendedFrame{
							SessionId: "s-bnd",
							Events: []*corev1.PlainEvent{
								{
									Seq:      seq,
									Verified: true,
									Event: &v1.Event{
										Seq:    seq,
										Origin: &v1.Origin{Kind: v1.OriginKind_ORIGIN_KIND_CLIENT, DeviceId: "dev-1"},
										Kind: &v1.Event_ApprovalResolved{
											ApprovalResolved: &v1.ApprovalResolved{
												ApprovalId: aid,
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
		seq++
	}

	ui := c.m.sessions.uiBySession["s-bnd"]
	if ui == nil {
		t.Fatalf("expected session UI state")
	}
	if len(ui.SlotSucceededSeq) != 0 {
		t.Fatalf("SlotSucceededSeq must not accumulate per-message/approval keys, got %v", ui.SlotSucceededSeq)
	}
	if len(ui.LocalResolvedRequestID) != 0 {
		t.Fatalf("LocalResolvedRequestID must be pruned after resolution, got %v", ui.LocalResolvedRequestID)
	}
	if len(ui.LocalSucceededItems) != 0 {
		t.Fatalf("LocalSucceededItems must be pruned after resolution, got %v", ui.LocalSucceededItems)
	}
	if len(ui.Commands) != 0 || len(c.m.sessions.slotMeta) != 0 {
		t.Fatalf("Commands/slotMeta must be empty after settlement, got cmds=%d meta=%d", len(ui.Commands), len(c.m.sessions.slotMeta))
	}

	// Fixed slot (rename) still increments SlotSucceededSeq as expected.
	bRen := c.Dispatch(&corev1.Event{
		NowMs: now + 10,
		Event: &corev1.Event_SessionRename{
			SessionRename: &corev1.SessionRenameAction{SessionId: "s-bnd", Title: "Renamed"},
		},
	})
	effsRen, _ := decodeDispatchedCommands(t, bRen)
	settleCommandOK(c, now+20, effsRen[0], &v1.CommandResult{Outcome: "executed"})
	if ui.SlotSucceededSeq["rename:s-bnd"] != 1 {
		t.Fatalf("expected fixed rename slot succeeded_seq=1, got %v", ui.SlotSucceededSeq)
	}
}

func TestSessionQuestionSelectedOptionNums(t *testing.T) {
	sess := &corev1.PlainSession{
		SessionId: "s-sel",
		MachineId: "m-on",
		UpdatedMs: 1_791_078_060_000,
		Meta:      &v1.Session{Id: "s-sel", Cwd: "/Users/me/proj", Agent: v1.Agent_AGENT_CLAUDE},
		Checkpoint: &v1.LiveSnapshot{
			Actions: []*v1.Event{
				{
					Seq: 10,
					Kind: &v1.Event_QuestionAsked{
						QuestionAsked: &v1.QuestionAsked{
							QuestionId: "q-sel",
							Questions: []*v1.Question{
								{
									Header:      "Features",
									Text:        "Pick features",
									MultiSelect: true,
									Options:     []*v1.QuestionOption{{Label: "A"}, {Label: "B"}, {Label: "C"}},
								},
								{
									Header: "Confirm",
									Text:   "Proceed?",
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
	getSelected := func() []int32 {
		vm, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-sel"}}})
		return vm.GetSession().GetDock().GetQuestion().GetSelectedOptionNums()
	}

	// 1) Idle multi-select: empty -> tick 1 & 3 -> [1, 3].
	if got := getSelected(); len(got) != 0 {
		t.Fatalf("initial selected = %v, want empty", got)
	}
	c.Dispatch(&corev1.Event{NowMs: 1_791_078_061_000, Event: &corev1.Event_SessionToggleQuestionOption{SessionToggleQuestionOption: &corev1.SessionToggleQuestionOptionAction{SessionId: "s-sel", QuestionId: "q-sel", OptionNum: 1}}})
	c.Dispatch(&corev1.Event{NowMs: 1_791_078_061_010, Event: &corev1.Event_SessionToggleQuestionOption{SessionToggleQuestionOption: &corev1.SessionToggleQuestionOptionAction{SessionId: "s-sel", QuestionId: "q-sel", OptionNum: 3}}})
	if got := getSelected(); !slices.Equal(got, []int32{1, 3}) {
		t.Fatalf("drafted selected = %v, want [1 3]", got)
	}

	// 2) Submit -> busy -> selected stays [1, 3]; fail -> selected stays [1, 3].
	bSub := c.Dispatch(&corev1.Event{NowMs: 1_791_078_061_020, Event: &corev1.Event_SessionAnswerQuestion{SessionAnswerQuestion: &corev1.SessionAnswerQuestionAction{SessionId: "s-sel", QuestionId: "q-sel"}}})
	effs, _ := decodeDispatchedCommands(t, bSub)
	if got := getSelected(); !slices.Equal(got, []int32{1, 3}) {
		t.Fatalf("busy selected = %v, want [1 3]", got)
	}
	settleCommandFailed(c, 1_791_078_061_030, effs[0], "failed_precondition")
	if got := getSelected(); !slices.Equal(got, []int32{1, 3}) {
		t.Fatalf("failed selected = %v, want [1 3]", got)
	}

	// 3) Toggle after failure clears error & updates selected; unticking ALL yields empty (not stale submitted!).
	c.Dispatch(&corev1.Event{NowMs: 1_791_078_061_040, Event: &corev1.Event_SessionToggleQuestionOption{SessionToggleQuestionOption: &corev1.SessionToggleQuestionOptionAction{SessionId: "s-sel", QuestionId: "q-sel", OptionNum: 1}}})
	if got := getSelected(); !slices.Equal(got, []int32{3}) {
		t.Fatalf("after unticking 1, selected = %v, want [3]", got)
	}
	c.Dispatch(&corev1.Event{NowMs: 1_791_078_061_050, Event: &corev1.Event_SessionToggleQuestionOption{SessionToggleQuestionOption: &corev1.SessionToggleQuestionOptionAction{SessionId: "s-sel", QuestionId: "q-sel", OptionNum: 3}}})
	if got := getSelected(); len(got) != 0 {
		t.Fatalf("after unticking all post-failure, selected = %v, want empty", got)
	}

	// 4) Submit [2] -> UNKNOWN (frozen) -> selected = [2]; settle OK advances to single-select step 1.
	c.Dispatch(&corev1.Event{NowMs: 1_791_078_061_060, Event: &corev1.Event_SessionToggleQuestionOption{SessionToggleQuestionOption: &corev1.SessionToggleQuestionOptionAction{SessionId: "s-sel", QuestionId: "q-sel", OptionNum: 2}}})
	bSub2 := c.Dispatch(&corev1.Event{NowMs: 1_791_078_061_070, Event: &corev1.Event_SessionAnswerQuestion{SessionAnswerQuestion: &corev1.SessionAnswerQuestionAction{SessionId: "s-sel", QuestionId: "q-sel"}}})
	effs2, _ := decodeDispatchedCommands(t, bSub2)
	settleCommandUnknown(c, 1_791_078_061_080, effs2[0])
	if got := getSelected(); !slices.Equal(got, []int32{2}) {
		t.Fatalf("frozen multi selected = %v, want [2]", got)
	}
	bRetry := c.Dispatch(&corev1.Event{NowMs: 1_791_078_061_090, Event: &corev1.Event_SessionRetryCommand{SessionRetryCommand: &corev1.SessionRetryCommandAction{SessionId: "s-sel", SlotKey: "question:s-sel:q-sel#0"}}})
	effsR, _ := decodeDispatchedCommands(t, bRetry)
	settleCommandOK(c, 1_791_078_061_100, effsR[0], &v1.CommandResult{Outcome: "executed"})

	// 5) Single-select step 1: idle -> empty; submit option 2 -> busy/failed -> [2].
	if got := getSelected(); len(got) != 0 {
		t.Fatalf("single-select idle selected = %v, want empty", got)
	}
	bSingle := c.Dispatch(&corev1.Event{NowMs: 1_791_078_061_110, Event: &corev1.Event_SessionAnswerQuestion{SessionAnswerQuestion: &corev1.SessionAnswerQuestionAction{SessionId: "s-sel", QuestionId: "q-sel", OptionNum: 2}}})
	effsS, _ := decodeDispatchedCommands(t, bSingle)
	if got := getSelected(); !slices.Equal(got, []int32{2}) {
		t.Fatalf("single-select busy selected = %v, want [2]", got)
	}
	settleCommandFailed(c, 1_791_078_061_120, effsS[0], "failed_precondition")
	if got := getSelected(); !slices.Equal(got, []int32{2}) {
		t.Fatalf("single-select failed selected = %v, want [2]", got)
	}
}

func TestSessionSimpleCommandGuardsAndRenameAutoTitleNoop(t *testing.T) {
	// 1) Idle session: INTERRUPT must be rejected (!CanInterrupt), COMPACT allowed.
	idleSess := &corev1.PlainSession{
		SessionId: "s-guard",
		MachineId: "m-on",
		UpdatedMs: 1_791_078_060_000,
		Meta: &v1.Session{
			Id:     "s-guard",
			Cwd:    "/Users/me/proj",
			Agent:  v1.Agent_AGENT_CLAUDE,
			Status: v1.SessionStatus_SESSION_STATUS_IDLE,
			// Title longer than 60 codepoints (TitleInput truncates to 60 codepoints).
			Title: strings.Repeat("a", 80),
		},
		Checkpoint: &v1.LiveSnapshot{Running: false},
	}
	c := makeSemanticTestCore(t, idleSess, nil)
	bIntIdle := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_061_000,
		Event: &corev1.Event_SessionSimpleCommand{
			SessionSimpleCommand: &corev1.SessionSimpleCommandAction{
				SessionId: "s-guard",
				Kind:      corev1.SessionSimpleCommandKind_SESSION_SIMPLE_COMMAND_KIND_INTERRUPT,
			},
		},
	})
	if effs, _ := decodeDispatchedCommands(t, bIntIdle); len(effs) != 0 {
		t.Fatalf("expected INTERRUPT rejected on idle session (!CanInterrupt), got %d cmds", len(effs))
	}

	// Saving the 60-codepoint capped auto-title unchanged must be a no-op (no RenameSession command)!
	bRenCap := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_061_050,
		Event: &corev1.Event_SessionRename{
			SessionRename: &corev1.SessionRenameAction{
				SessionId: "s-guard",
				Title:     strings.Repeat("a", 60),
			},
		},
	})
	if effs, _ := decodeDispatchedCommands(t, bRenCap); len(effs) != 0 {
		t.Fatalf("saving 60-codepoint capped auto-title must be no-op, got %d cmds", len(effs))
	}

	// 2) Running session: COMPACT rejected (!CanCompact), INTERRUPT allowed.
	runSess := &corev1.PlainSession{
		SessionId: "s-run-g",
		MachineId: "m-on",
		UpdatedMs: 1_791_078_060_000,
		Meta: &v1.Session{
			Id:     "s-run-g",
			Cwd:    "/Users/me/proj",
			Agent:  v1.Agent_AGENT_CLAUDE,
			Status: v1.SessionStatus_SESSION_STATUS_RUNNING,
		},
		Checkpoint: &v1.LiveSnapshot{Running: true},
	}
	cRun := makeSemanticTestCore(t, runSess, nil)
	bCmpRun := cRun.Dispatch(&corev1.Event{
		NowMs: 1_791_078_061_000,
		Event: &corev1.Event_SessionSimpleCommand{
			SessionSimpleCommand: &corev1.SessionSimpleCommandAction{
				SessionId: "s-run-g",
				Kind:      corev1.SessionSimpleCommandKind_SESSION_SIMPLE_COMMAND_KIND_COMPACT,
			},
		},
	})
	if effs, _ := decodeDispatchedCommands(t, bCmpRun); len(effs) != 0 {
		t.Fatalf("expected COMPACT rejected while running (!CanCompact), got %d cmds", len(effs))
	}

	// 3) Exited session: STOP rejected (!CanEnd).
	exitedSess := &corev1.PlainSession{
		SessionId: "s-exit-g",
		MachineId: "m-on",
		UpdatedMs: 1_791_078_060_000,
		Meta: &v1.Session{
			Id:     "s-exit-g",
			Cwd:    "/Users/me/proj",
			Agent:  v1.Agent_AGENT_CLAUDE,
			Status: v1.SessionStatus_SESSION_STATUS_EXITED,
		},
	}
	cExit := makeSemanticTestCore(t, exitedSess, nil)
	bStopExit := cExit.Dispatch(&corev1.Event{
		NowMs: 1_791_078_061_000,
		Event: &corev1.Event_SessionSimpleCommand{
			SessionSimpleCommand: &corev1.SessionSimpleCommandAction{
				SessionId: "s-exit-g",
				Kind:      corev1.SessionSimpleCommandKind_SESSION_SIMPLE_COMMAND_KIND_STOP,
			},
		},
	})
	if effs, _ := decodeDispatchedCommands(t, bStopExit); len(effs) != 0 {
		t.Fatalf("expected STOP rejected on exited session (!CanEnd), got %d cmds", len(effs))
	}
}

func TestQuestionOptionSummaryTextUsesI18nSeparatorAndFallback(t *testing.T) {
	sess := &corev1.PlainSession{
		SessionId: "s-qi18n",
		MachineId: "m-on",
		UpdatedMs: 1_791_078_060_000,
		Meta:      &v1.Session{Id: "s-qi18n", Cwd: "/Users/me/proj", Agent: v1.Agent_AGENT_CLAUDE},
		Checkpoint: &v1.LiveSnapshot{
			Actions: []*v1.Event{
				{
					Seq: 3,
					Kind: &v1.Event_QuestionAsked{
						QuestionAsked: &v1.QuestionAsked{
							QuestionId: "q-i18n",
							Questions: []*v1.Question{
								{
									Header:      "Opt",
									Text:        "Pick",
									MultiSelect: true,
									Options:     []*v1.QuestionOption{{Label: "Alpha"}, {Label: ""}},
								},
							},
						},
					},
				},
			},
		},
	}
	c := makeSemanticTestCore(t, sess, nil)
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_061_000,
		Event: &corev1.Event_SessionAnswerQuestion{
			SessionAnswerQuestion: &corev1.SessionAnswerQuestionAction{
				SessionId:  "s-qi18n",
				QuestionId: "q-i18n",
				OptionNums: []int32{1, 2},
			},
		},
	})
	vm, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-qi18n"}}})
	sum := vm.GetSession().GetDock().GetCommand().GetSubmittedSummary()
	if sum == nil || sum.SeparatorKey != text.KeyCommonListSeparator {
		t.Fatalf("expected i18n Join with KeyCommonListSeparator, got %+v", sum)
	}
	parts := sum.Join
	if len(parts) != 2 || parts[0].GetLiteral() != "Alpha" || parts[1].GetKey() != text.KeyDockOptionNumber {
		t.Fatalf("expected [Plain(Alpha), Msg(dock.option_number)], got %+v", parts)
	}
}
