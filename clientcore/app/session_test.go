package app

import (
	"bytes"
	"testing"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/screens"
	"github.com/Lewin671/remote-agent/clientcore/sync"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

func bootstrapReadyApp(t *testing.T, machines []*corev1.PlainMachine, sessions []*corev1.PlainSession) *Core {
	t.Helper()
	c := New()
	t.Cleanup(c.Close)

	ids := make([]string, 0, 40)
	for i := 1; i <= 40; i++ {
		ids = append(ids, "t-id-"+string(rune('a'+i/26))+string(rune('a'+i%26)))
	}
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_000,
		Event: &corev1.Event_EntropyProvided{
			EntropyProvided: &corev1.EntropyProvided{
				RandomIds:  ids,
				UnitFloats: []float64{0.5, 0.5, 0.5, 0.5, 0.5, 0.5},
			},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_000,
		Event: &corev1.Event_SyncLifecycle{
			SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_005,
		Event: &corev1.Event_CacheBootstrapLoaded{
			CacheBootstrapLoaded: &corev1.CacheBootstrapLoadedEvent{OpId: 1},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_010,
		Event: &corev1.Event_SecuritySnapshot{
			SecuritySnapshot: &corev1.SecuritySnapshotEvent{
				OpId:              3,
				IsMember:          true,
				SelfDeviceId:      "dev-1",
				AuthorityDeviceId: "dev-1",
				LeaseEndMs:        1_791_164_460_000,
				TrustedMachineIds: []string{"m-on", "m-off", "m-old", "m-new"},
				TrustedClientIds:  []string{"dev-1"},
			},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_020,
		Event: &corev1.Event_TokenResult{
			TokenResult: &corev1.TokenResultEvent{OpId: 2, Token: "tok-1"},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_030,
		Event: &corev1.Event_StreamReady{
			StreamReady: &corev1.StreamReadyEvent{LegId: 1, ServerTimeMs: 1_791_078_060_030},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_040,
		Event: &corev1.Event_ListsFetched{
			ListsFetched: &corev1.ListsFetchedEvent{
				OpId:     4,
				Machines: machines,
				Sessions: sessions,
			},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_042,
		Event: &corev1.Event_AccountPrefsFetched{
			AccountPrefsFetched: &corev1.AccountPrefsFetchedEvent{
				OpId: 5,
				Prefs: &corev1.PlainAccountPrefs{
					Revision: 1,
					Prefs: &v1.ProjectPrefs{
						Schema: 1,
						Pinned: []*v1.ProjectPref{{Path: "/Users/me/proj", SinceMs: 1_791_000_000_000}},
					},
				},
			},
		},
	})
	return c
}

func watchCacheReadOpID(batch *corev1.EffectBatch) uint64 {
	if batch == nil {
		return 0
	}
	for _, eff := range batch.Effects {
		if cr := eff.GetCacheRead(); cr != nil && cr.GetEvents() != nil {
			return cr.GetEvents().OpId
		}
	}
	return 0
}

func TestSessionStartPromptDeduplicationAndForkNavigation(t *testing.T) {
	c := bootstrapReadyApp(t, []*corev1.PlainMachine{
		{
			MachineId: "m-on",
			Online:    true,
			Meta: &v1.MachineMeta{
				Hostname:        "DevBox",
				ProtocolVersion: sync.ProtocolVersion,
				Capabilities: &v1.MachineCapabilities{
					ImageUpload:    true,
					FileUpload:     true,
					SharedFiles:    true,
					ScheduledTasks: true,
				},
				Agents: []*v1.AgentInfo{
					{Agent: v1.Agent_AGENT_CODEX, DefaultMode: v1.ApprovalMode_APPROVAL_MODE_ASK},
				},
			},
		},
	}, nil)

	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_043,
		Event: &corev1.Event_ProjectOpenDirPicker{
			ProjectOpenDirPicker: &corev1.DirPickerOpenAction{MachineId: "m-on", Path: "/Users/me/proj", ForNewTask: true},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_044,
		Event: &corev1.Event_ProjectAdd{
			ProjectAdd: &corev1.ProjectAddAction{Path: "/Users/me/proj"},
		},
	})
	// Start a new session via NewSession flow and capture PendingStartPrompt.
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_045,
		Event: &corev1.Event_NewSessionOpen{
			NewSessionOpen: &corev1.NewSessionOpenAction{
				ProjectKey: "/Users/me/proj",
			},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_050,
		Event: &corev1.Event_NewSessionSetPrompt{
			NewSessionSetPrompt: &corev1.NewSessionSetPromptAction{
				Prompt: "Build core session tests",
			},
		},
	})
	submitBatch := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_060,
		Event: &corev1.Event_NewSessionSubmit{
			NewSessionSubmit: &corev1.NewSessionSubmitAction{
				ProjectKey:        "/Users/me/proj",
				AutoModeSupported: true,
			},
		},
	})
	var createReqID, createAttID string
	for _, eff := range submitBatch.Effects {
		if dc := eff.GetDispatchCommand(); dc != nil {
			var cmd v1.Command
			if err := cmd.Unmarshal(dc.CommandBytes); err == nil && cmd.GetCreateSession() != nil {
				createReqID = dc.RequestId
				createAttID = dc.AttemptId
			}
		}
	}
	if createReqID == "" {
		nsVM, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_NewSession{NewSession: &corev1.NewSessionScreenRequest{ProjectKey: "/Users/me/proj"}}})
		t.Fatalf("expected CreateSession dispatch command (newSession=%+v)", nsVM.GetNewSession())
	}

	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_100,
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:         createReqID,
				AttemptId:         createAttID,
				Source:            corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON,
				Signed:            true,
				HashesMatch:       true,
				AttemptBound:      true,
				OuterMatchesInner: true,
				OuterOutcome:      "executed",
				OuterReason:       "ok",
				Result: &v1.CommandResult{
					Outcome:         "executed",
					Reason:          "ok",
					ResultAvailable: true,
					Kind: &v1.CommandResult_CreateSession{
						CreateSession: &v1.CreateSessionResponse{
							Session: &v1.Session{
								Id:     "s-created",
								Cwd:    "/Users/me/proj",
								Agent:  v1.Agent_AGENT_CODEX,
								Status: v1.SessionStatus_SESSION_STATUS_IDLE,
							},
						},
					},
				},
			},
		},
	})

	// Make session visible in list/stream so Buffer can load.
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_110,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: 1,
				Frame: &corev1.StreamFrameEvent_SessionUpdated{
					SessionUpdated: &corev1.PlainSession{
						SessionId: "s-created",
						MachineId: "m-on",
						Meta: &v1.Session{
							Id:     "s-created",
							Cwd:    "/Users/me/proj",
							Agent:  v1.Agent_AGENT_CODEX,
							Status: v1.SessionStatus_SESSION_STATUS_IDLE,
						},
					},
				},
			},
		},
	})

	vmBefore, err := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{
		Session: &corev1.SessionScreenRequest{SessionId: "s-created"},
	}})
	if err != nil {
		t.Fatalf("View before UserMessage: %v", err)
	}
	itemsBefore := vmBefore.GetSession().GetTimeline().GetItems()
	if len(itemsBefore) != 0 {
		t.Fatalf("expected empty timeline before UserMessage, got %+v", itemsBefore)
	}
	trayBefore := vmBefore.GetSession().GetComposer().GetPendingTray()
	if trayBefore.GetTotalCount() != 1 || trayBefore.GetOrderedItems()[0].GetGroup() != corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_WAIT_READY || trayBefore.GetOrderedItems()[0].GetText() != "Build core session tests" {
		t.Fatalf("expected startup prompt in WAIT_READY tray before UserMessage, got %+v", trayBefore)
	}

	// Once real UserMessage arrives in events, synthetic start-prompt deduplicates.
	watchBatch := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_120,
		Event: &corev1.Event_SessionWatch{
			SessionWatch: &corev1.SessionWatchEvent{SessionId: "s-created", Watched: true},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_130,
		Event: &corev1.Event_CacheEventsLoaded{
			CacheEventsLoaded: &corev1.CacheEventsLoadedEvent{
				OpId:      watchCacheReadOpID(watchBatch),
				SessionId: "s-created",
				Events: []*corev1.PlainEvent{
					{
						Seq:      1,
						Verified: true,
						Event: &v1.Event{
							Seq: 1,
							Kind: &v1.Event_UserMessage{
								UserMessage: &v1.UserMessage{Text: "Build core session tests"},
							},
						},
					},
				},
			},
		},
	})
	vmAfter, err := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{
		Session: &corev1.SessionScreenRequest{SessionId: "s-created"},
	}})
	if err != nil {
		t.Fatalf("View after UserMessage: %v", err)
	}
	itemsAfter := vmAfter.GetSession().GetTimeline().GetItems()
	if len(itemsAfter) != 1 || itemsAfter[0].Id != "e1" {
		t.Fatalf("expected synthetic start-prompt deduplicated to e1 only, got %+v", itemsAfter)
	}

	// Now test ForkSession command emitting NavigateEffect on Executed.
	forkBatch := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_140,
		Event: &corev1.Event_SessionSubmitCommand{
			SessionSubmitCommand: &corev1.SessionSubmitCommandAction{
				SessionId: "s-created",
				Command: &v1.Command{
					Kind: &v1.Command_ForkSession{
						ForkSession: &v1.ForkSessionRequest{SessionId: "s-created"},
					},
				},
			},
		},
	})
	var forkReqID, forkAttID string
	for _, eff := range forkBatch.Effects {
		if dc := eff.GetDispatchCommand(); dc != nil {
			var cmd v1.Command
			if err := cmd.Unmarshal(dc.CommandBytes); err == nil && cmd.GetForkSession() != nil {
				forkReqID = dc.RequestId
				forkAttID = dc.AttemptId
			}
		}
	}
	if forkReqID == "" {
		t.Fatal("expected ForkSession dispatch command")
	}
	forkSettle := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_150,
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:         forkReqID,
				AttemptId:         forkAttID,
				Source:            corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON,
				Signed:            true,
				HashesMatch:       true,
				AttemptBound:      true,
				OuterMatchesInner: true,
				OuterOutcome:      "executed",
				OuterReason:       "ok",
				Result: &v1.CommandResult{
					Outcome:         "executed",
					Reason:          "ok",
					ResultAvailable: true,
					Kind: &v1.CommandResult_ForkSession{
						ForkSession: &v1.ForkSessionResponse{
							Session: &v1.Session{
								Id:  "s-forked",
								Cwd: "/Users/me/proj",
							},
						},
					},
				},
			},
		},
	})
	var navigated string
	for _, eff := range forkSettle.Effects {
		if nav := eff.GetNavigate(); nav != nil && nav.Route == corev1.NavigateRoute_NAVIGATE_ROUTE_SESSION {
			navigated = nav.SessionId
		}
	}
	if navigated != "s-forked" {
		t.Fatalf("expected NavigateEffect to s-forked, got %q", navigated)
	}
}

func TestSessionMissingLookupMismatchOfflineAndRichTimeline(t *testing.T) {
	c := bootstrapReadyApp(t, []*corev1.PlainMachine{
		{
			MachineId:  "m-off",
			Online:     false,
			LastSeenMs: 1_791_000_000_000,
			Meta: &v1.MachineMeta{
				Hostname:        "SleepyMac",
				ProtocolVersion: sync.ProtocolVersion,
			},
		},
		{
			MachineId: "m-old",
			Online:    true,
			Meta: &v1.MachineMeta{
				Hostname:        "OldDaemon",
				ProtocolVersion: sync.ProtocolVersion - 1,
			},
		},
		{
			MachineId: "m-on",
			Online:    true,
			Meta: &v1.MachineMeta{
				Hostname:        "MainMac",
				Home:            "/Users/me",
				ProtocolVersion: sync.ProtocolVersion,
				Capabilities: &v1.MachineCapabilities{
					SharedFiles: true,
				},
			},
		},
	}, []*corev1.PlainSession{
		{
			SessionId: "s-off",
			MachineId: "m-off",
			Meta: &v1.Session{
				Id:     "s-off",
				Cwd:    "/Users/me/proj",
				Agent:  v1.Agent_AGENT_CODEX,
				Status: v1.SessionStatus_SESSION_STATUS_IDLE,
			},
		},
		{
			SessionId: "s-mismatch",
			MachineId: "m-old",
			Meta: &v1.Session{
				Id:     "s-mismatch",
				Cwd:    "/Users/me/proj",
				Agent:  v1.Agent_AGENT_CLAUDE,
				Status: v1.SessionStatus_SESSION_STATUS_IDLE,
			},
		},
		{
			SessionId: "s-rich",
			MachineId: "m-on",
			LastSeq:   7,
			Meta: &v1.Session{
				Id:     "s-rich",
				Cwd:    "/Users/me/proj",
				Agent:  v1.Agent_AGENT_CODEX,
				Status: v1.SessionStatus_SESSION_STATUS_BLOCKED,
			},
			Checkpoint: &v1.LiveSnapshot{
				Running: false,
				Session: &v1.Session{
					Id:     "s-rich",
					Status: v1.SessionStatus_SESSION_STATUS_BLOCKED,
				},
			},
		},
	})

	// 1. Opening an unknown session emits FetchSessionByIdRpc and reports Missing once listed and not found.
	openMissing := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_050,
		Event: &corev1.Event_SessionOpen{
			SessionOpen: &corev1.SessionOpenAction{SessionId: "s-missing"},
		},
	})
	var fetchedByID string
	for _, eff := range openMissing.Effects {
		if rpc := eff.GetRpc(); rpc != nil && rpc.GetFetchSessionById() != nil {
			fetchedByID = rpc.GetFetchSessionById().SessionId
		}
	}
	if fetchedByID != "s-missing" {
		t.Fatalf("expected FetchSessionByIdRpc for s-missing, got %q", fetchedByID)
	}
	vmLookup, err := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-missing"}}})
	if err != nil {
		t.Fatalf("View missing lookup: %v", err)
	}
	if vmLookup.GetSession().GetHeader().GetFound() {
		t.Fatal("expected Found=false for missing session")
	}

	// 2. Offline session disables composer with computer offline reason.
	vmOff, err := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-off"}}})
	if err != nil {
		t.Fatalf("View offline: %v", err)
	}
	if vmOff.GetSession().GetComposer().GetEnabled() || vmOff.GetSession().GetComposer().GetDisabledReason().GetKey() != "errors.the_computer_is_offline_the_command_was_not_sent" {
		t.Fatalf("expected offline disabled composer, got %+v", vmOff.GetSession().GetComposer())
	}

	// 3. Protocol mismatch flags daemon update message.
	vmMis, err := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-mismatch"}}})
	if err != nil {
		t.Fatalf("View mismatch: %v", err)
	}
	hdrMis := vmMis.GetSession().GetHeader()
	if !hdrMis.GetProtocolMismatch() || hdrMis.GetProtocolMismatchText().GetKey() != "session.update_computer" {
		t.Fatalf("expected update_computer mismatch, got %+v", hdrMis)
	}

	// 4. Rich session with High Context, SharedFiles, GeneratedImages, AsyncQuestion, TerminalPopup, and TokenUsage.
	richWatch := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_060,
		Event: &corev1.Event_SessionWatch{
			SessionWatch: &corev1.SessionWatchEvent{SessionId: "s-rich", Watched: true},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_070,
		Event: &corev1.Event_CacheEventsLoaded{
			CacheEventsLoaded: &corev1.CacheEventsLoadedEvent{
				OpId:      watchCacheReadOpID(richWatch),
				SessionId: "s-rich",
				Events: []*corev1.PlainEvent{
					{
						Seq: 1, Verified: true,
						Event: &v1.Event{Seq: 1, Kind: &v1.Event_UserMessage{UserMessage: &v1.UserMessage{
							Text:  "Look at main.go\n\nFile: /Users/me/proj/main.go",
							Files: []*v1.SentFile{{FileId: "f1", Name: "main.go", Path: "/Users/me/proj/main.go", SizeBytes: 120}},
						}}},
					},
					{
						Seq: 2, Verified: true,
						Event: &v1.Event{Seq: 2, Kind: &v1.Event_Reasoning{Reasoning: &v1.Reasoning{Text: "Checking symbols..."}}},
					},
					{
						Seq: 3, Verified: true,
						Event: &v1.Event{Seq: 3, Kind: &v1.Event_ToolCall{ToolCall: &v1.ToolCall{
							CallId: "sf1", Kind: v1.ToolKind_TOOL_KIND_OTHER, Title: "Share", Status: "completed",
							SharedFiles: []*v1.SharedFile{{FileId: "sh1", Name: "report.pdf", Path: "/Users/me/proj/report.pdf", SizeBytes: 4096}},
						}}},
					},
					{
						Seq: 4, Verified: true,
						Event: &v1.Event{Seq: 4, Kind: &v1.Event_ImageGenerated{ImageGenerated: &v1.ImageGenerated{
							RevisedPrompt: "Architecture diagram",
							Images:        []*v1.ImageRef{{ImageId: "img-gen-1", Width: 512, Height: 512}},
						}}},
					},
					{
						Seq: 5, Verified: true,
						Event: &v1.Event{Seq: 5, Kind: &v1.Event_QuestionAsked{QuestionAsked: &v1.QuestionAsked{
							QuestionId: "aq-1", Async: true,
							Questions: []*v1.Question{{Header: "Followup", Text: "Deploy now?"}},
						}}},
					},
					{
						Seq: 6, Verified: true,
						Event: &v1.Event{Seq: 6, Kind: &v1.Event_TokenUsage{TokenUsage: &v1.TokenUsage{
							InputTokens: 150_000, OutputTokens: 20_000, LastTotalTokens: 170_000, ContextWindow: 200_000,
						}}},
					},
					{
						Seq: 7, Verified: true,
						Event: &v1.Event{Seq: 7, Kind: &v1.Event_InputState{InputState: &v1.InputState{
							RequestId: "bad-in", Status: v1.InputStatus_INPUT_STATUS_FAILED, Text: "dropped", Reason: "pane_closed",
						}}},
					},
				},
			},
		},
	})

	vmRich, err := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-rich"}}})
	if err != nil {
		t.Fatalf("View rich: %v", err)
	}
	s := vmRich.GetSession()
	if !s.GetHeader().GetHasContext() || s.GetHeader().GetContextPercent() != 85 || !s.GetHeader().GetHighContextWarning() {
		t.Fatalf("expected 85%% high context warning, got %+v", s.GetHeader())
	}
	if !s.GetDock().GetHasItem() || s.GetDock().GetKind() != corev1.SessionDockKind_SESSION_DOCK_KIND_TERMINAL_POPUP {
		t.Fatalf("expected TERMINAL_POPUP dock for blocked session, got %+v", s.GetDock())
	}
	tlItems := s.GetTimeline().GetItems()
	if len(tlItems) != 5 {
		t.Fatalf("expected 5 timeline items (user, reasoning, sharedFiles, genImg, asyncQuestion), got %d", len(tlItems))
	}
	if tlItems[0].GetUserMessage().GetText() != "Look at main.go" {
		t.Fatalf("expected injected File line stripped from UserMessage, got %q", tlItems[0].GetUserMessage().GetText())
	}
	if tlItems[2].GetSharedFiles() == nil || tlItems[3].GetGeneratedImages() == nil || tlItems[4].GetAsyncQuestion() == nil {
		t.Fatalf("expected rich timeline rows present and typed cleanly: %+v", tlItems)
	}
	if trayItems := s.GetComposer().GetPendingTray().GetOrderedItems(); len(trayItems) != 1 || trayItems[0].GetGroup() != corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_FAILED {
		t.Fatalf("expected non-exited FAILED input in pending tray, got %+v", s.GetComposer().GetPendingTray())
	}
	var _ = lists.DefaultAgent
}

func setupPlanSessionForTest(t *testing.T, hashA []byte) *Core {
	t.Helper()
	machines := []*corev1.PlainMachine{
		{
			MachineId: "m-on",
			Online:    true,
			Meta: &v1.MachineMeta{
				Hostname: "MacStudio",
			},
		},
	}
	sessions := []*corev1.PlainSession{
		{
			SessionId: "s-plan",
			MachineId: "m-on",
			Meta: &v1.Session{
				Id:       "s-plan",
				Cwd:      "/Users/me/proj",
				Agent:    v1.Agent_AGENT_CLAUDE,
				Status:   v1.SessionStatus_SESSION_STATUS_RUNNING,
				PlanOpen: true,
			},
			UpdatedMs: 1_791_078_060_000,
			LastSeq:   10,
			Checkpoint: &v1.LiveSnapshot{
				Running:        true,
				PlanEventSeq:   10,
				PlanScreenHash: append([]byte(nil), hashA...),
				PlanScreen:     "## Initial Plan\n1. Step 1",
				Session: &v1.Session{
					Id:       "s-plan",
					Agent:    v1.Agent_AGENT_CLAUDE,
					Status:   v1.SessionStatus_SESSION_STATUS_RUNNING,
					PlanOpen: true,
				},
			},
		},
	}
	c := bootstrapReadyApp(t, machines, sessions)
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_050,
		Event: &corev1.Event_SessionOpen{
			SessionOpen: &corev1.SessionOpenAction{SessionId: "s-plan"},
		},
	})
	watch := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_055,
		Event: &corev1.Event_SessionWatch{
			SessionWatch: &corev1.SessionWatchEvent{SessionId: "s-plan", Watched: true},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_060,
		Event: &corev1.Event_CacheEventsLoaded{
			CacheEventsLoaded: &corev1.CacheEventsLoadedEvent{
				OpId:      watchCacheReadOpID(watch),
				SessionId: "s-plan",
			},
		},
	})
	return c
}

func findDispatchedCommands(batch *corev1.EffectBatch) ([]*corev1.DispatchCommandEffect, []*v1.Command) {
	if batch == nil {
		return nil, nil
	}
	var effs []*corev1.DispatchCommandEffect
	var cmds []*v1.Command
	for _, eff := range batch.Effects {
		if dc := eff.GetDispatchCommand(); dc != nil {
			var cmd v1.Command
			if err := cmd.Unmarshal(dc.CommandBytes); err == nil {
				effs = append(effs, dc)
				cmds = append(cmds, &cmd)
			}
		}
	}
	return effs, cmds
}

func TestAnswerPlanPreflightRejectsChangedSnapshot(t *testing.T) {
	hashA := bytes.Repeat([]byte{0x11}, 32)
	hashB := bytes.Repeat([]byte{0x22}, 32)
	c := setupPlanSessionForTest(t, hashA)

	// Submit AnswerPlan: must dispatch GetLiveSnapshot first, NOT AnswerPlan.
	sub := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_100,
		Event: &corev1.Event_SessionSubmitCommand{
			SessionSubmitCommand: &corev1.SessionSubmitCommandAction{
				SessionId: "s-plan",
				Command: &v1.Command{
					Kind: &v1.Command_AnswerPlan{
						AnswerPlan: &v1.AnswerPlanRequest{
							SessionId: "s-plan",
							Choice:    v1.PlanChoice_PLAN_CHOICE_IMPLEMENT,
						},
					},
				},
			},
		},
	})
	effs, cmds := findDispatchedCommands(sub)
	if len(effs) != 1 || cmds[0].GetGetLiveSnapshot() == nil {
		t.Fatalf("expected single GetLiveSnapshot preflight dispatch, got %d cmds: %+v", len(cmds), cmds)
	}
	for _, cmd := range cmds {
		if cmd.GetAnswerPlan() != nil {
			t.Fatalf("AnswerPlan must not be dispatched before GetLiveSnapshot settles")
		}
	}

	vmPending, err := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-plan"}}})
	if err != nil {
		t.Fatalf("View pending: %v", err)
	}
	if vmPending.GetSession().GetDock().GetCommand().GetStatus() != corev1.SessionCommandStatus_SESSION_COMMAND_STATUS_PENDING {
		t.Fatalf("expected PENDING status during preflight, got %+v", vmPending.GetSession().GetDock().GetCommand())
	}

	// Settle GetLiveSnapshot with a changed hash: must NOT send AnswerPlan, must refresh UI and show review error.
	afterSnap := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_120,
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:         effs[0].RequestId,
				AttemptId:         effs[0].AttemptId,
				Source:            corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON,
				Signed:            true,
				HashesMatch:       true,
				AttemptBound:      true,
				OuterMatchesInner: true,
				OuterOutcome:      "executed",
				OuterReason:       "ok",
				Result: &v1.CommandResult{
					Outcome:         "executed",
					Reason:          "ok",
					ResultAvailable: true,
					Kind: &v1.CommandResult_LiveSnapshot{
						LiveSnapshot: &v1.LiveSnapshot{
							PlanEventSeq:   10,
							PlanScreenHash: append([]byte(nil), hashB...),
							PlanScreen:     "## Updated Plan\n1. Step X",
							Session: &v1.Session{
								Id:       "s-plan",
								Agent:    v1.Agent_AGENT_CLAUDE,
								Status:   v1.SessionStatus_SESSION_STATUS_RUNNING,
								PlanOpen: true,
							},
						},
					},
				},
			},
		},
	})
	_, afterCmds := findDispatchedCommands(afterSnap)
	for _, cmd := range afterCmds {
		if cmd.GetAnswerPlan() != nil {
			t.Fatalf("AnswerPlan must NOT be dispatched when live snapshot hash changed")
		}
	}

	vmAfter, err := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-plan"}}})
	if err != nil {
		t.Fatalf("View after mismatch: %v", err)
	}
	dock := vmAfter.GetSession().GetDock()
	if dock.GetCommand().GetStatus() != corev1.SessionCommandStatus_SESSION_COMMAND_STATUS_FAILED {
		t.Fatalf("expected FAILED dock status on changed plan snapshot, got %+v", dock.GetCommand())
	}
	if dock.GetCommand().GetErrorText().GetKey() != "errors.the_plan_display_changed_review_it_again" {
		t.Fatalf("expected the_plan_display_changed_review_it_again, got %+v", dock.GetCommand().GetErrorText())
	}
	if !bytes.Equal(dock.GetPlan().GetScreenHash(), hashB) {
		t.Fatalf("expected refreshed plan hash %x, got %x", hashB, dock.GetPlan().GetScreenHash())
	}
	if dock.GetPlan().GetMarkdown() != "## Updated Plan\n1. Step X" {
		t.Fatalf("expected refreshed plan markdown, got %q", dock.GetPlan().GetMarkdown())
	}
}

func TestAnswerPlanPreflightMatchesAndFrozenRetry(t *testing.T) {
	hashA := bytes.Repeat([]byte{0x33}, 32)
	c := setupPlanSessionForTest(t, hashA)

	// Submit AnswerPlan -> dispatches GetLiveSnapshot.
	sub := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_100,
		Event: &corev1.Event_SessionSubmitCommand{
			SessionSubmitCommand: &corev1.SessionSubmitCommandAction{
				SessionId: "s-plan",
				Command: &v1.Command{
					Kind: &v1.Command_AnswerPlan{
						AnswerPlan: &v1.AnswerPlanRequest{
							SessionId: "s-plan",
							Choice:    v1.PlanChoice_PLAN_CHOICE_CLEAR_AND_IMPLEMENT,
						},
					},
				},
			},
		},
	})
	effs, cmds := findDispatchedCommands(sub)
	if len(effs) != 1 || cmds[0].GetGetLiveSnapshot() == nil {
		t.Fatalf("expected GetLiveSnapshot preflight, got %+v", cmds)
	}

	// Settle GetLiveSnapshot with matching seq/hash -> dispatches AnswerPlan carrying fresh seq/hash.
	snapMatch := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_120,
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:         effs[0].RequestId,
				AttemptId:         effs[0].AttemptId,
				Source:            corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON,
				Signed:            true,
				HashesMatch:       true,
				AttemptBound:      true,
				OuterMatchesInner: true,
				OuterOutcome:      "executed",
				OuterReason:       "ok",
				Result: &v1.CommandResult{
					Outcome:         "executed",
					Reason:          "ok",
					ResultAvailable: true,
					Kind: &v1.CommandResult_LiveSnapshot{
						LiveSnapshot: &v1.LiveSnapshot{
							PlanEventSeq:   10,
							PlanScreenHash: append([]byte(nil), hashA...),
							PlanScreen:     "## Initial Plan\n1. Step 1",
							Session: &v1.Session{
								Id:       "s-plan",
								Agent:    v1.Agent_AGENT_CLAUDE,
								Status:   v1.SessionStatus_SESSION_STATUS_RUNNING,
								PlanOpen: true,
							},
						},
					},
				},
			},
		},
	})
	apEffs, apCmds := findDispatchedCommands(snapMatch)
	if len(apEffs) != 1 || apCmds[0].GetAnswerPlan() == nil {
		t.Fatalf("expected AnswerPlan dispatch after matching preflight, got %d cmds: %+v", len(apCmds), apCmds)
	}
	ap := apCmds[0].GetAnswerPlan()
	if ap.GetObservedEventSeq() != 10 || !bytes.Equal(ap.GetObservedScreenHash(), hashA) {
		t.Fatalf("expected AnswerPlan observed seq=10 hash=%x, got seq=%d hash=%x", hashA, ap.GetObservedEventSeq(), ap.GetObservedScreenHash())
	}
	if ap.GetChoice() != v1.PlanChoice_PLAN_CHOICE_CLEAR_AND_IMPLEMENT {
		t.Fatalf("expected choice CLEAR_AND_IMPLEMENT, got %v", ap.GetChoice())
	}

	// Simulate UNKNOWN outcome -> freezes the AnswerPlan attempt.
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_140,
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:    apEffs[0].RequestId,
				AttemptId:    apEffs[0].AttemptId,
				Source:       corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON,
				Signed:       false,
				HashesMatch:  true,
				AttemptBound: true,
			},
		},
	})
	vmFrozen, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-plan"}}})
	if !vmFrozen.GetSession().GetDock().GetCommand().GetFrozen() {
		t.Fatalf("expected frozen plan command after UNKNOWN outcome, got %+v", vmFrozen.GetSession().GetDock().GetCommand())
	}

	// Retry frozen attempt: must bypass GetLiveSnapshot and resend AnswerPlan directly.
	retryBatch := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_160,
		Event: &corev1.Event_SessionSubmitCommand{
			SessionSubmitCommand: &corev1.SessionSubmitCommandAction{
				SessionId: "s-plan",
				Command: &v1.Command{
					Kind: &v1.Command_AnswerPlan{
						AnswerPlan: &v1.AnswerPlanRequest{
							SessionId: "s-plan",
							Choice:    v1.PlanChoice_PLAN_CHOICE_CLEAR_AND_IMPLEMENT,
						},
					},
				},
			},
		},
	})
	retryEffs, retryCmds := findDispatchedCommands(retryBatch)
	if len(retryEffs) != 1 || retryCmds[0].GetAnswerPlan() == nil {
		t.Fatalf("expected frozen AnswerPlan retry without new GetLiveSnapshot, got %+v", retryCmds)
	}
	if retryEffs[0].RequestId != apEffs[0].RequestId {
		t.Fatalf("expected frozen retry to reuse requestId %q, got %q", apEffs[0].RequestId, retryEffs[0].RequestId)
	}

	// Settle retried AnswerPlan as executed.
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_180,
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:         retryEffs[0].RequestId,
				AttemptId:         retryEffs[0].AttemptId,
				Source:            corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON,
				Signed:            true,
				HashesMatch:       true,
				AttemptBound:      true,
				OuterMatchesInner: true,
				OuterOutcome:      "executed",
				OuterReason:       "ok",
				Result: &v1.CommandResult{
					Outcome:         "executed",
					Reason:          "ok",
					ResultAvailable: true,
					Kind:            &v1.CommandResult_AnswerPlan{AnswerPlan: &v1.AnswerPlanResponse{}},
				},
			},
		},
	})
	vmDone, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-plan"}}})
	if vmDone.GetSession().GetDock().GetCommand().GetStatus() != corev1.SessionCommandStatus_SESSION_COMMAND_STATUS_IDLE {
		t.Fatalf("expected IDLE command status after executed AnswerPlan, got %+v", vmDone.GetSession().GetDock().GetCommand())
	}
}

func TestCoreClearsTimelineCacheOnTrimHistoryEviction(t *testing.T) {
	c := bootstrapReadyApp(t, []*corev1.PlainMachine{
		{
			MachineId: "m-on",
			Online:    true,
			Meta: &v1.MachineMeta{
				Hostname: "MacBook",
				Agents:   []*v1.AgentInfo{{Agent: v1.Agent_AGENT_CODEX}},
			},
		},
	}, []*corev1.PlainSession{
		{
			SessionId: "s-trim",
			MachineId: "m-on",
			LastSeq:   1,
			Meta: &v1.Session{
				Id:     "s-trim",
				Agent:  v1.Agent_AGENT_CODEX,
				Status: v1.SessionStatus_SESSION_STATUS_RUNNING,
				Title:  "Trim cache",
			},
		},
	})

	watchBatch := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_100,
		Event: &corev1.Event_SessionWatch{SessionWatch: &corev1.SessionWatchEvent{SessionId: "s-trim", Watched: true}},
	})
	var sweepTimerID uint64
	for _, eff := range watchBatch.Effects {
		if st := eff.GetScheduleTimer(); st != nil && st.Kind == corev1.TimerKind_TIMER_KIND_HISTORY_SWEEP {
			sweepTimerID = st.TimerId
		}
	}
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_120,
		Event: &corev1.Event_CacheEventsLoaded{
			CacheEventsLoaded: &corev1.CacheEventsLoadedEvent{
				OpId:      watchCacheReadOpID(watchBatch),
				SessionId: "s-trim",
				Events: []*corev1.PlainEvent{{
					Seq:      1,
					Verified: true,
					Event:    &v1.Event{Seq: 1, SessionId: "s-trim", Kind: &v1.Event_UserMessage{UserMessage: &v1.UserMessage{Text: "hello"}}},
				}},
			},
		},
	})

	vm, err := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-trim"}}})
	if err != nil || len(vm.GetSession().GetTimeline().GetItems()) != 1 {
		t.Fatalf("expected 1 timeline item, err=%v vm=%+v", err, vm)
	}
	ui := c.m.ensureSessionStore().uiBySession["s-trim"]
	if !ui.HasTimelineCache() {
		t.Fatalf("expected SessionUIState to populate timeline cache")
	}

	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_200,
		Event: &corev1.Event_SessionWatch{SessionWatch: &corev1.SessionWatchEvent{SessionId: "s-trim", Watched: false}},
	})
	c.Dispatch(&corev1.Event{
		NowMs:  1_791_078_060_200 + sync.HistoryIdleMs + 1000,
		MonoMs: c.m.sync.MonoMs() + sync.HistoryIdleMs + 1000,
		Event: &corev1.Event_TimerFired{TimerFired: &corev1.TimerFiredEvent{
			TimerId: sweepTimerID,
			Kind:    corev1.TimerKind_TIMER_KIND_HISTORY_SWEEP,
		}},
	})
	if c.m.sync.Buffer("s-trim") != nil {
		t.Fatalf("expected trimHistory to evict EventBuffer")
	}
	if ui.HasTimelineCache() {
		t.Fatalf("expected timeline cache to be released when trimHistory evicts EventBuffer")
	}
}

func TestSessionTerminalPopupDockLocalizationAndTrustHint(t *testing.T) {
	cases := []struct {
		name           string
		agent          v1.Agent
		wantAgentName  string
		statusDetail   string
		wantItemID     string
		wantHasTitle   bool
		wantHintKey    string
	}{
		{
			name:          "antigravity_generic_popup",
			agent:         v1.Agent_AGENT_ANTIGRAVITY,
			wantAgentName: "Antigravity",
			statusDetail:  "MCP Servers",
			wantItemID:    "MCP Servers",
			wantHasTitle:  true,
		},
		{
			name:          "codex_unknown_panel",
			agent:         v1.Agent_AGENT_CODEX,
			wantAgentName: "Codex",
			statusDetail:  "unknown panel",
			wantItemID:    "unknown panel",
			wantHasTitle:  true,
		},
		{
			name:          "claude_trust_folder_shows_hint",
			agent:         v1.Agent_AGENT_CLAUDE,
			wantAgentName: "Claude Code",
			statusDetail:  "trust folder",
			wantItemID:    "trust folder",
			wantHasTitle:  true,
			wantHintKey:   "dock.trust_folder",
		},
		{
			name:          "codex_trust_folder_hides_claude_hint",
			agent:         v1.Agent_AGENT_CODEX,
			wantAgentName: "Codex",
			statusDetail:  "trust folder",
			wantItemID:    "trust folder",
			wantHasTitle:  true,
		},
		{
			name:          "antigravity_trust_folder_hides_claude_hint",
			agent:         v1.Agent_AGENT_ANTIGRAVITY,
			wantAgentName: "Antigravity",
			statusDetail:  "trust folder",
			wantItemID:    "trust folder",
			wantHasTitle:  true,
		},
		{
			name:          "empty_status_detail_omits_popup_title_and_hint",
			agent:         v1.Agent_AGENT_ANTIGRAVITY,
			wantAgentName: "Antigravity",
			statusDetail:  "",
			wantItemID:    "terminal-popup",
			wantHasTitle:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := bootstrapReadyApp(t,
				[]*corev1.PlainMachine{{MachineId: "m-1", Online: true, Meta: &v1.MachineMeta{Hostname: "dev"}}},
				[]*corev1.PlainSession{{
					SessionId: "s-popup",
					MachineId: "m-1",
					UpdatedMs: 1_791_078_050_000,
					Meta: &v1.Session{
						Id:           "s-popup",
						Cwd:          "/work/app",
						Agent:        tc.agent,
						Status:       v1.SessionStatus_SESSION_STATUS_BLOCKED,
						StatusDetail: tc.statusDetail,
					},
					Checkpoint: &v1.LiveSnapshot{
						Session: &v1.Session{
							Id:           "s-popup",
							Cwd:          "/work/app",
							Agent:        tc.agent,
							Status:       v1.SessionStatus_SESSION_STATUS_BLOCKED,
							StatusDetail: tc.statusDetail,
						},
					},
				}},
			)
			watchBatch := c.Dispatch(&corev1.Event{
				NowMs: 1_791_078_060_060,
				Event: &corev1.Event_SessionWatch{
					SessionWatch: &corev1.SessionWatchEvent{SessionId: "s-popup", Watched: true},
				},
			})
			c.Dispatch(&corev1.Event{
				NowMs: 1_791_078_060_070,
				Event: &corev1.Event_CacheEventsLoaded{
					CacheEventsLoaded: &corev1.CacheEventsLoadedEvent{
						OpId:      watchCacheReadOpID(watchBatch),
						SessionId: "s-popup",
					},
				},
			})

			vm, err := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-popup"}}})
			if err != nil {
				t.Fatalf("View: %v", err)
			}
			dock := vm.GetSession().GetDock()
			if !dock.GetHasItem() || dock.GetKind() != corev1.SessionDockKind_SESSION_DOCK_KIND_TERMINAL_POPUP {
				t.Fatalf("expected TERMINAL_POPUP dock, got %+v", dock)
			}
			if dock.GetItemId() != tc.wantItemID {
				t.Fatalf("ItemId = %q, want %q", dock.GetItemId(), tc.wantItemID)
			}
			if dock.GetTitle().GetKey() != "list_state.popup" {
				t.Fatalf("Title = %+v, want list_state.popup", dock.GetTitle())
			}
			if args := dock.GetMeta().GetArgs(); dock.GetMeta().GetKey() != "dock.agent_waiting" || len(args) != 1 || args[0].GetName() != "agent" || args[0].GetStrValue() != tc.wantAgentName {
				t.Fatalf("Meta = %+v, want dock.agent_waiting(%q)", dock.GetMeta(), tc.wantAgentName)
			}
			tp := dock.GetTerminalPopup()
			if tp == nil || !tp.GetCanGoTerminal() {
				t.Fatalf("expected TerminalPopup with CanGoTerminal=true, got %+v", tp)
			}
			if tc.wantHasTitle {
				if dock.GetSummary().GetKey() != "list_state.popup" {
					t.Fatalf("Summary = %+v, want list_state.popup", dock.GetSummary())
				}
				if tp.GetPopupTitle().GetKey() != "list_state.popup" {
					t.Fatalf("PopupTitle = %+v, want list_state.popup", tp.GetPopupTitle())
				}
			} else {
				if dock.GetSummary() != nil {
					t.Fatalf("expected nil Summary when statusDetail empty, got %+v", dock.GetSummary())
				}
				if tp.GetPopupTitle() != nil {
					t.Fatalf("expected nil PopupTitle when statusDetail empty, got %+v", tp.GetPopupTitle())
				}
			}
			gotHintKey := ""
			if tp.GetHint() != nil {
				gotHintKey = tp.GetHint().GetKey()
			}
			if gotHintKey != tc.wantHintKey {
				t.Fatalf("Hint key = %q, want %q (hint=%+v)", gotHintKey, tc.wantHintKey, tp.GetHint())
			}
		})
	}
}

func startSessionForStartupPromptTest(t *testing.T, sid, prompt string) (*Core, string) {
	t.Helper()
	c := bootstrapReadyApp(t,
		[]*corev1.PlainMachine{
			{
				MachineId: "m-on",
				Online:    true,
				Meta: &v1.MachineMeta{
					Hostname:        "MacStudio",
					ProtocolVersion: sync.ProtocolVersion,
					Agents: []*v1.AgentInfo{
						{Agent: v1.Agent_AGENT_ANTIGRAVITY, DefaultMode: v1.ApprovalMode_APPROVAL_MODE_ASK},
					},
				},
			},
		},
		nil,
	)

	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_043,
		Event: &corev1.Event_ProjectOpenDirPicker{
			ProjectOpenDirPicker: &corev1.DirPickerOpenAction{MachineId: "m-on", Path: "/Users/me/proj", ForNewTask: true},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_044,
		Event: &corev1.Event_ProjectAdd{
			ProjectAdd: &corev1.ProjectAddAction{Path: "/Users/me/proj"},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_045,
		Event: &corev1.Event_NewSessionOpen{
			NewSessionOpen: &corev1.NewSessionOpenAction{ProjectKey: "/Users/me/proj"},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_048,
		Event: &corev1.Event_NewSessionSelectAgent{
			NewSessionSelectAgent: &corev1.NewSessionSelectAgentAction{
				Agent: v1.Agent_AGENT_ANTIGRAVITY,
			},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_050,
		Event: &corev1.Event_NewSessionSetPrompt{
			NewSessionSetPrompt: &corev1.NewSessionSetPromptAction{Prompt: prompt},
		},
	})
	submitBatch := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_060,
		Event: &corev1.Event_NewSessionSubmit{
			NewSessionSubmit: &corev1.NewSessionSubmitAction{
				ProjectKey:        "/Users/me/proj",
				AutoModeSupported: true,
			},
		},
	})
	var createReqID, createAttID string
	for _, eff := range submitBatch.Effects {
		if dc := eff.GetDispatchCommand(); dc != nil {
			var cmd v1.Command
			if err := cmd.Unmarshal(dc.CommandBytes); err == nil && cmd.GetCreateSession() != nil {
				createReqID = dc.RequestId
				createAttID = dc.AttemptId
			}
		}
	}
	if createReqID == "" {
		t.Fatal("expected CreateSession dispatch command")
	}

	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_100,
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:         createReqID,
				AttemptId:         createAttID,
				Source:            corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON,
				Signed:            true,
				HashesMatch:       true,
				AttemptBound:      true,
				OuterMatchesInner: true,
				OuterOutcome:      "executed",
				OuterReason:       "ok",
				Result: &v1.CommandResult{
					Outcome:         "executed",
					Reason:          "ok",
					ResultAvailable: true,
					Kind: &v1.CommandResult_CreateSession{
						CreateSession: &v1.CreateSessionResponse{
							Session: &v1.Session{
								Id:     sid,
								Cwd:    "/Users/me/proj",
								Agent:  v1.Agent_AGENT_ANTIGRAVITY,
								Status: v1.SessionStatus_SESSION_STATUS_STARTING,
							},
						},
					},
				},
			},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_110,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: 1,
				Frame: &corev1.StreamFrameEvent_SessionUpdated{
					SessionUpdated: &corev1.PlainSession{
						SessionId: sid,
						MachineId: "m-on",
						Meta: &v1.Session{
							Id:     sid,
							Cwd:    "/Users/me/proj",
							Agent:  v1.Agent_AGENT_ANTIGRAVITY,
							Status: v1.SessionStatus_SESSION_STATUS_STARTING,
						},
					},
				},
			},
		},
	})
	return c, createReqID
}

func TestStartupPromptLifecycleDeduplicatesAndWaitsForCLI(t *testing.T) {
	c, createReqID := startSessionForStartupPromptTest(t, "s-start", "hi")

	// 1. Before any events load: synthetic startup prompt sits in WAIT_READY tray, timeline is empty, no bogus mode label.
	vm0, err := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-start"}}})
	if err != nil {
		t.Fatalf("View step 1: %v", err)
	}
	s0 := vm0.GetSession()
	if s0.GetHeader().GetApprovalModeLabel() != nil {
		t.Fatalf("expected nil ApprovalModeLabel while STARTING, got %+v", s0.GetHeader().GetApprovalModeLabel())
	}
	if len(s0.GetTimeline().GetItems()) != 0 {
		t.Fatalf("step 1 expected empty timeline, got %+v", s0.GetTimeline().GetItems())
	}
	tray0 := s0.GetComposer().GetPendingTray()
	if len(tray0.GetGroups()) != 1 || tray0.GetGroups()[0].GetKind() != corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_WAIT_READY || tray0.GetGroups()[0].GetTitle().GetKey() != text.KeyQueueGroupWaitReady {
		t.Fatalf("step 1 expected WAIT_READY tray item, got %+v", tray0)
	}

	// 2. SessionState{STARTING} + InputState{QUEUED} arrives: PendingInputs takes over with "等 Antigravity 就绪", no duplicate start-prompt.
	watchBatch := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_120,
		Event: &corev1.Event_SessionWatch{SessionWatch: &corev1.SessionWatchEvent{SessionId: "s-start", Watched: true}},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_130,
		Event: &corev1.Event_CacheEventsLoaded{
			CacheEventsLoaded: &corev1.CacheEventsLoadedEvent{
				OpId:      watchCacheReadOpID(watchBatch),
				SessionId: "s-start",
				Events: []*corev1.PlainEvent{
					{
						Seq:      1,
						Verified: true,
						Event: &v1.Event{
							Seq:  1,
							Kind: &v1.Event_SessionState{SessionState: &v1.SessionState{Status: v1.SessionStatus_SESSION_STATUS_STARTING}},
						},
					},
					{
						Seq:      2,
						Verified: true,
						Event: &v1.Event{
							Seq:    2,
							Origin: &v1.Origin{Kind: v1.OriginKind_ORIGIN_KIND_CLIENT, DeviceId: "dev-1", RequestId: createReqID},
							Kind: &v1.Event_InputState{
								InputState: &v1.InputState{
									RequestId: "in-1",
									Status:    v1.InputStatus_INPUT_STATUS_QUEUED,
									Stage:     v1.InputStage_INPUT_STAGE_WAITING,
									Text:      "hi",
									Mode:      v1.SendMode_SEND_MODE_STEER,
								},
							},
						},
					},
				},
			},
		},
	})

	vm1, err := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-start"}}})
	if err != nil {
		t.Fatalf("View step 2: %v", err)
	}
	s1 := vm1.GetSession()
	if s1.GetHeader().GetApprovalModeLabel() != nil {
		t.Fatalf("step 2 expected nil ApprovalModeLabel while STARTING, got %+v", s1.GetHeader().GetApprovalModeLabel())
	}
	if len(s1.GetTimeline().GetItems()) != 0 {
		t.Fatalf("step 2 expected 0 timeline items while queued, got %+v", s1.GetTimeline().GetItems())
	}
	tray1 := s1.GetComposer().GetPendingTray()
	if len(tray1.GetGroups()) != 1 || len(tray1.GetOrderedItems()) != 1 {
		t.Fatalf("step 2 expected 1 pending tray item, got %+v", tray1)
	}
	if g := tray1.GetGroups()[0]; g.GetKind() != corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_WAIT_READY || g.GetTitle().GetKey() != text.KeyQueueGroupWaitReady {
		t.Fatalf("step 2 expected group %q, got %+v", text.KeyQueueGroupWaitReady, g.GetTitle())
	} else if args := g.GetTitle().GetArgs(); len(args) != 1 || args[0].GetStrValue() != "Antigravity" {
		t.Fatalf("step 2 expected agent arg Antigravity, got %+v", g.GetTitle())
	}
	if !tray1.GetOrderedItems()[0].GetServerBacked() {
		t.Fatalf("step 2 expected server-backed queued item replacing synthetic prompt")
	}

	// 3. InputState{SUBMITTED} arrives before UserMessage: PendingTray empties (written to terminal).
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_200,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: 1,
				Frame: &corev1.StreamFrameEvent_EventsAppended{
					EventsAppended: &corev1.EventsAppendedFrame{
						SessionId: "s-start",
						Events: []*corev1.PlainEvent{
							{
								Seq:      3,
								Verified: true,
								Event: &v1.Event{
									Seq:  3,
									Kind: &v1.Event_InputState{InputState: &v1.InputState{RequestId: "in-1", Status: v1.InputStatus_INPUT_STATUS_SUBMITTED}},
								},
							},
						},
					},
				},
			},
		},
	})

	vm2, err := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-start"}}})
	if err != nil {
		t.Fatalf("View step 3: %v", err)
	}
	s2 := vm2.GetSession()
	if s2.GetComposer().GetPendingTray().GetTotalCount() != 0 {
		t.Fatalf("step 3 expected empty pending tray after SUBMITTED, got %+v", s2.GetComposer().GetPendingTray())
	}
	if items := s2.GetTimeline().GetItems(); len(items) != 0 {
		t.Fatalf("step 3 expected empty timeline before UserMessage, got %+v", items)
	}

	// 4. UserMessage arrives: replaces start-prompt with real timeline event e4 and clears PendingStartPrompt.
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_250,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: 1,
				Frame: &corev1.StreamFrameEvent_EventsAppended{
					EventsAppended: &corev1.EventsAppendedFrame{
						SessionId: "s-start",
						Events: []*corev1.PlainEvent{
							{
								Seq:      4,
								Verified: true,
								Event: &v1.Event{
									Seq: 4,
									Kind: &v1.Event_UserMessage{
										UserMessage: &v1.UserMessage{Text: "hi", RequestId: "in-1"},
									},
								},
							},
						},
					},
				},
			},
		},
	})

	vm3, err := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-start"}}})
	if err != nil {
		t.Fatalf("View step 4: %v", err)
	}
	if items := vm3.GetSession().GetTimeline().GetItems(); len(items) != 1 || items[0].Id != "e4" {
		t.Fatalf("step 4 expected [e4] only, got %+v", items)
	}
	if c.m.ensureSessionStore().ensureUI("s-start").PendingStartPrompt != nil {
		t.Fatalf("step 4 expected PendingStartPrompt cleared")
	}
}

func TestCanceledStartupPromptDoesNotResurrectGhostBubble(t *testing.T) {
	c, createReqID := startSessionForStartupPromptTest(t, "s-cancel", "hi")
	watchBatch := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_120,
		Event: &corev1.Event_SessionWatch{SessionWatch: &corev1.SessionWatchEvent{SessionId: "s-cancel", Watched: true}},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_130,
		Event: &corev1.Event_CacheEventsLoaded{
			CacheEventsLoaded: &corev1.CacheEventsLoadedEvent{
				OpId:      watchCacheReadOpID(watchBatch),
				SessionId: "s-cancel",
				Events: []*corev1.PlainEvent{
					{
						Seq:      1,
						Verified: true,
						Event: &v1.Event{
							Seq:    1,
							Origin: &v1.Origin{Kind: v1.OriginKind_ORIGIN_KIND_CLIENT, DeviceId: "dev-1", RequestId: createReqID},
							Kind: &v1.Event_InputState{
								InputState: &v1.InputState{
									RequestId: "in-1",
									Status:    v1.InputStatus_INPUT_STATUS_QUEUED,
									Text:      "hi",
								},
							},
						},
					},
					{
						Seq:      2,
						Verified: true,
						Event: &v1.Event{
							Seq: 2,
							Kind: &v1.Event_InputState{
								InputState: &v1.InputState{
									RequestId: "in-1",
									Status:    v1.InputStatus_INPUT_STATUS_CANCELED,
								},
							},
						},
					},
				},
			},
		},
	})

	vm, err := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-cancel"}}})
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	for _, it := range vm.GetSession().GetTimeline().GetItems() {
		if it.Id == "start-prompt" || it.GetUserMessage() != nil {
			t.Fatalf("canceled startup prompt resurrected as user message: %+v", it)
		}
	}
	if vm.GetSession().GetComposer().GetPendingTray().GetTotalCount() != 0 {
		t.Fatalf("expected 0 pending tray items after cancel, got %+v", vm.GetSession().GetComposer().GetPendingTray())
	}
	if c.m.ensureSessionStore().ensureUI("s-cancel").PendingStartPrompt != nil {
		t.Fatalf("expected PendingStartPrompt cleared after cancel")
	}
}

func TestPendingTrayGroupingNumberingAndOrigin(t *testing.T) {
	st := screens.NewState()
	ls := &lists.Session{
		ID:        "s1",
		MachineID: "m1",
		Info:      &v1.Session{Id: "s1", Agent: v1.Agent_AGENT_CODEX, Status: v1.SessionStatus_SESSION_STATUS_RUNNING},
	}
	in := screens.SessionViewInput{
		Found:        true,
		Session:      ls,
		ThisDeviceID: "dev-self",
		DeviceNames:  map[string]string{"dev-other": "MyPhone"},
		Projection: &corev1.ProjectionState{
			PendingInputs: []*corev1.PendingInput{
				{
					RequestId: "r1",
					Text:      "first",
					Mode:      v1.SendMode_SEND_MODE_STEER,
					Origin:    &v1.Origin{Kind: v1.OriginKind_ORIGIN_KIND_CLIENT, DeviceId: "dev-other"},
				},
				{
					RequestId: "r2",
					Text:      "second",
					Mode:      v1.SendMode_SEND_MODE_QUEUE,
				},
				{
					RequestId: "r3",
					Text:      "third",
					Mode:      v1.SendMode_SEND_MODE_QUEUE,
				},
			},
		},
	}
	vm := screens.BuildSessionView(in, st, &corev1.SessionScreenRequest{SessionId: "s1"}, screens.NewSessionUIState())
	tray := vm.GetComposer().GetPendingTray()
	if len(tray.GetGroups()) != 2 {
		t.Fatalf("expected 2 tray groups (STEER, QUEUE), got %+v", tray.GetGroups())
	}
	steerGroup := tray.GetGroups()[0]
	if steerGroup.GetKind() != corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_STEER ||
		steerGroup.GetHeaderText().GetKey() != text.KeyQueueGroupSteer ||
		len(steerGroup.GetItems()) != 1 {
		t.Fatalf("unexpected steer group: %+v", steerGroup)
	}
	if steerGroup.GetItems()[0].GetSourceLabel().GetLiteral() != "MyPhone" {
		t.Fatalf("expected origin MyPhone on steer item, got %+v", steerGroup.GetItems()[0].GetSourceLabel())
	}
	queueGroup := tray.GetGroups()[1]
	if queueGroup.GetKind() != corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_QUEUE ||
		queueGroup.GetHeaderText().GetKey() != text.KeyQueueGroupCount ||
		len(queueGroup.GetItems()) != 2 {
		t.Fatalf("unexpected queue group: %+v", queueGroup)
	}
	if queueGroup.GetItems()[0].GetQueueNumber() != 1 || queueGroup.GetItems()[1].GetQueueNumber() != 2 {
		t.Fatalf("expected queue numbers 1 and 2, got %d, %d", queueGroup.GetItems()[0].GetQueueNumber(), queueGroup.GetItems()[1].GetQueueNumber())
	}
}

func TestCreateSessionSeedsSessionBeforeStreamUpdatedAndLoadingHidesMissingText(t *testing.T) {
	c := bootstrapReadyApp(t,
		[]*corev1.PlainMachine{
			{
				MachineId: "m-on",
				Online:    true,
				Meta: &v1.MachineMeta{
					Hostname:        "MacStudio",
					ProtocolVersion: sync.ProtocolVersion,
					Agents: []*v1.AgentInfo{
						{Agent: v1.Agent_AGENT_ANTIGRAVITY, DefaultMode: v1.ApprovalMode_APPROVAL_MODE_ASK},
					},
				},
			},
		},
		nil,
	)

	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_043,
		Event: &corev1.Event_ProjectOpenDirPicker{
			ProjectOpenDirPicker: &corev1.DirPickerOpenAction{MachineId: "m-on", Path: "/Users/me/proj", ForNewTask: true},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_044,
		Event: &corev1.Event_ProjectAdd{
			ProjectAdd: &corev1.ProjectAddAction{Path: "/Users/me/proj"},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_045,
		Event: &corev1.Event_NewSessionOpen{
			NewSessionOpen: &corev1.NewSessionOpenAction{ProjectKey: "/Users/me/proj"},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_048,
		Event: &corev1.Event_NewSessionSelectAgent{
			NewSessionSelectAgent: &corev1.NewSessionSelectAgentAction{Agent: v1.Agent_AGENT_ANTIGRAVITY},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_050,
		Event: &corev1.Event_NewSessionSetPrompt{
			NewSessionSetPrompt: &corev1.NewSessionSetPromptAction{Prompt: "hi"},
		},
	})
	submitBatch := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_060,
		Event: &corev1.Event_NewSessionSubmit{
			NewSessionSubmit: &corev1.NewSessionSubmitAction{
				ProjectKey:        "/Users/me/proj",
				AutoModeSupported: true,
			},
		},
	})
	var createReqID, createAttID string
	for _, eff := range submitBatch.Effects {
		if dc := eff.GetDispatchCommand(); dc != nil {
			var cmd v1.Command
			if err := cmd.Unmarshal(dc.CommandBytes); err == nil && cmd.GetCreateSession() != nil {
				createReqID = dc.RequestId
				createAttID = dc.AttemptId
			}
		}
	}
	if createReqID == "" {
		t.Fatal("expected CreateSession dispatch command")
	}

	// CommandOutcome(CreateSession) arrives WITHOUT any StreamFrameEvent_SessionUpdated yet.
	outcomeBatch := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_100,
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:         createReqID,
				AttemptId:         createAttID,
				Source:            corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON,
				Signed:            true,
				HashesMatch:       true,
				AttemptBound:      true,
				OuterMatchesInner: true,
				OuterOutcome:      "executed",
				OuterReason:       "ok",
				Result: &v1.CommandResult{
					Outcome:         "executed",
					Reason:          "ok",
					ResultAvailable: true,
					Kind: &v1.CommandResult_CreateSession{
						CreateSession: &v1.CreateSessionResponse{
							Session: &v1.Session{
								Id:     "s-seeded",
								Cwd:    "/Users/me/proj",
								Agent:  v1.Agent_AGENT_ANTIGRAVITY,
								Status: v1.SessionStatus_SESSION_STATUS_STARTING,
							},
						},
					},
				},
			},
		},
	})
	var navigated bool
	for _, eff := range outcomeBatch.Effects {
		if nav := eff.GetNavigate(); nav != nil && nav.Route == corev1.NavigateRoute_NAVIGATE_ROUTE_SESSION && nav.SessionId == "s-seeded" {
			navigated = true
		}
	}
	if !navigated {
		t.Fatalf("expected Navigate to s-seeded on CreateSession outcome")
	}

	// Immediately viewing the session must show Found=true, Missing=false, MissingText=nil, empty timeline, and WAIT_READY tray item.
	vm, err := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-seeded"}}})
	if err != nil {
		t.Fatalf("View seeded session: %v", err)
	}
	hdr := vm.GetSession().GetHeader()
	if !hdr.GetFound() || hdr.GetMissing() || hdr.GetMissingText() != nil {
		t.Fatalf("expected Found=true, Missing=false, MissingText=nil immediately after CreateSession, got found=%v missing=%v missingText=%+v", hdr.GetFound(), hdr.GetMissing(), hdr.GetMissingText())
	}
	if items := vm.GetSession().GetTimeline().GetItems(); len(items) != 0 {
		t.Fatalf("expected empty timeline immediately after CreateSession, got %+v", items)
	}
	if trayItems := vm.GetSession().GetComposer().GetPendingTray().GetOrderedItems(); len(trayItems) != 1 || trayItems[0].GetGroup() != corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_WAIT_READY {
		t.Fatalf("expected WAIT_READY item in pending tray, got %+v", vm.GetSession().GetComposer().GetPendingTray())
	}

	// Unknown session while LookingUp=true must populate StatusText=common.loading with Missing=false and MissingText=nil.
	loadingVM := screens.BuildSessionView(
		screens.SessionViewInput{Found: false, SessionsListed: true, LookingUp: true},
		screens.NewState(),
		&corev1.SessionScreenRequest{SessionId: "s-unknown"},
		screens.NewSessionUIState(),
	)
	if loadingVM.GetHeader().GetMissing() {
		t.Fatalf("expected Missing=false while LookingUp=true")
	}
	if loadingVM.GetHeader().GetMissingText() != nil {
		t.Fatalf("expected nil MissingText while LookingUp=true, got %+v", loadingVM.GetHeader().GetMissingText())
	}
	if loadingVM.GetHeader().GetStatusText().GetKey() != text.KeyCommonLoading {
		t.Fatalf("expected StatusText %q while LookingUp=true, got %+v", text.KeyCommonLoading, loadingVM.GetHeader().GetStatusText())
	}
	if loadingVM.GetComposer().GetDisabledReason().GetKey() != text.KeyCommonLoading {
		t.Fatalf("expected Composer.DisabledReason %q while LookingUp=true, got %+v", text.KeyCommonLoading, loadingVM.GetComposer().GetDisabledReason())
	}
}
