package app

import (
	"bytes"
	"testing"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/world"
	"github.com/Lewin671/remote-agent/clientcore/domain/projection"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/internal/test/testrelay"
	"github.com/Lewin671/remote-agent/clientcore/screens"
	"github.com/Lewin671/remote-agent/clientcore/sync"
	"github.com/Lewin671/remote-agent/clientcore/text"
	stdv1 "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

func bootstrapReadyApp(t *testing.T, machines []*corev1.DemoMachine, sessions []*corev1.DemoSession) *Core {
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
		NowMs: 1_791_078_060_005,
		Event: &corev1.Event_SeedDemoWorld{
			SeedDemoWorld: &corev1.SeedDemoWorldEvent{
				Security: &corev1.DemoSecurity{
					IsMember:          true,
					SelfDeviceId:      "dev-1",
					AuthorityDeviceId: "dev-1",
					LeaseEndMs:        1_791_164_460_000,
					TrustedMachineIds: []string{"m-on", "m-off", "m-old", "m-new"},
					TrustedClientIds:  []string{"dev-1"},
				},
				Machines: machines,
				Sessions: sessions,
				RawPrefs: testrelay.MustRaw(&v1.ProjectPrefs{
					Schema: 1,
					Pinned: []*v1.ProjectPref{{Path: "/Users/me/proj", SinceMs: 1_791_000_000_000}},
				}),
				PrefsRevision: 1,
			},
		},
	})
	startBatch := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_010,
		Event: &corev1.Event_SyncLifecycle{
			SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START},
		},
	})
	legID := testrelay.FindOpenStreamLeg(startBatch.Effects)
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_030,
		Event: &corev1.Event_StreamReady{
			StreamReady: &corev1.StreamReadyEvent{LegId: legID, ServerTimeMs: 1_791_078_060_030},
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
	c := bootstrapReadyApp(t, []*corev1.DemoMachine{
		testrelay.DemoMachine("m-on", true, 0, &v1.MachineMeta{
			Hostname:        "DevBox",
			ProtocolVersion: sync.PeerVersion,
			Capabilities: &v1.MachineCapabilities{
				ImageUpload:    true,
				FileUpload:     true,
				SharedFiles:    true,
				ScheduledTasks: true,
			},
			Agents: []*v1.AgentInfo{
				{Agent: v1.Agent_AGENT_CODEX, DefaultMode: v1.ApprovalMode_APPROVAL_MODE_ASK},
			},
		}),
	}, nil)

	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_043,
		Event: &corev1.Event_ProjectOpenDirPicker{
			ProjectOpenDirPicker: &corev1.DirPickerOpenAction{PickerKey: "new-task", MachineId: "m-on", Path: "/Users/me/proj", ForNewTask: true},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_044,
		Event: &corev1.Event_ProjectAdd{
			ProjectAdd: &corev1.ProjectAddAction{Path: "/Users/me/proj", PickerKey: "new-task"},
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
		Event: &corev1.Event_DraftSetText{
			DraftSetText: &corev1.DraftSetTextAction{
				Scope: &corev1.DraftScope{Kind: corev1.DraftScopeKind_DRAFT_SCOPE_KIND_NEW_SESSION, Id: "/Users/me/proj"},
				Text:  "Build core session tests",
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
				RawResult: testrelay.MustRaw(&v1.CommandResult{
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
				}),
			},
		},
	})

	// Make session visible in list/stream so Buffer can load.
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_110,
		Event: testrelay.DemoSessionEvent(testrelay.DemoSession("s-created", "m-on", 1, 0, 0, &v1.Session{
			Id:     "s-created",
			Cwd:    "/Users/me/proj",
			Agent:  v1.Agent_AGENT_CODEX,
			Status: v1.SessionStatus_SESSION_STATUS_IDLE,
		}, nil)),
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
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_120,
		Event: &corev1.Event_SessionWatch{
			SessionWatch: &corev1.SessionWatchEvent{SessionId: "s-created", Watched: true},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_130,
		Event: testrelay.DemoEventsEvent("s-created", &v1.Event{
			Seq: 1,
			Kind: &v1.Event_UserMessage{
				UserMessage: &v1.UserMessage{Text: "Build core session tests"},
			},
		}),
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
		Event: &corev1.Event_SessionSimpleCommand{
			SessionSimpleCommand: &corev1.SessionSimpleCommandAction{
				SessionId: "s-created",
				Kind:      corev1.SessionSimpleCommandKind_SESSION_SIMPLE_COMMAND_KIND_FORK,
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
				RawResult: testrelay.MustRaw(&v1.CommandResult{
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
				}),
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
	c := bootstrapReadyApp(t, []*corev1.DemoMachine{
		testrelay.DemoMachine("m-off", false, 1_791_000_000_000, &v1.MachineMeta{
			Hostname:        "SleepyMac",
			ProtocolVersion: sync.PeerVersion,
		}),
		testrelay.DemoMachine("m-old", true, 0, &v1.MachineMeta{
			Hostname:        "OldDaemon",
			ProtocolVersion: sync.PeerVersion - 1,
		}),
		testrelay.DemoMachine("m-on", true, 0, &v1.MachineMeta{
			Hostname:        "MainMac",
			Home:            "/Users/me",
			ProtocolVersion: sync.PeerVersion,
			Capabilities: &v1.MachineCapabilities{
				SharedFiles: true,
			},
		}),
	}, []*corev1.DemoSession{
		testrelay.DemoSession("s-off", "m-off", 0, 0, 0, &v1.Session{
			Id:     "s-off",
			Cwd:    "/Users/me/proj",
			Agent:  v1.Agent_AGENT_CODEX,
			Status: v1.SessionStatus_SESSION_STATUS_IDLE,
		}, nil),
		testrelay.DemoSession("s-mismatch", "m-old", 0, 0, 0, &v1.Session{
			Id:     "s-mismatch",
			Cwd:    "/Users/me/proj",
			Agent:  v1.Agent_AGENT_CLAUDE,
			Status: v1.SessionStatus_SESSION_STATUS_IDLE,
		}, nil),
		testrelay.DemoSession("s-rich", "m-on", 7, 0, 0, &v1.Session{
			Id:     "s-rich",
			Cwd:    "/Users/me/proj",
			Agent:  v1.Agent_AGENT_CODEX,
			Status: v1.SessionStatus_SESSION_STATUS_BLOCKED,
		}, &v1.LiveSnapshot{
			Running: false,
			Session: &v1.Session{
				Id:     "s-rich",
				Status: v1.SessionStatus_SESSION_STATUS_BLOCKED,
			},
		}),
	})

	// 1. Opening an unknown session on a connected wire Core emits LIST_REMOTE_SESSIONS(session_id) and reports Found=false.
	wireCore := New()
	defer wireCore.Close()
	wireNow := int64(1_791_078_060_000)
	wireFx := testrelay.New(t, wireNow)
	wireFx.Bootstrap(func(ev *corev1.Event) []*corev1.Effect { return wireCore.Dispatch(ev).GetEffects() }, wireNow)
	wireCore.m.sync.SecurityEngine().SetAuthToken("tok-miss", wireNow+7*86_400_000)
	ws := wireCore.Dispatch(&corev1.Event{
		NowMs: wireNow + 10,
		Event: &corev1.Event_SyncLifecycle{SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START}},
	})
	wireLeg := testrelay.FindOpenStreamLeg(ws.Effects)
	wireCore.Dispatch(&corev1.Event{
		NowMs: wireNow + 20,
		Event: &corev1.Event_StreamReady{StreamReady: &corev1.StreamReadyEvent{LegId: wireLeg, ServerTimeMs: wireNow + 20}},
	})
	openMissing := wireCore.Dispatch(&corev1.Event{
		NowMs: wireNow + 50,
		Event: &corev1.Event_SessionOpen{
			SessionOpen: &corev1.SessionOpenAction{SessionId: "s-missing"},
		},
	})
	var fetchedByID string
	if rpc := testrelay.FindRelayRpc(openMissing.Effects, corev1.RelayRpcMethod_RELAY_RPC_METHOD_LIST_REMOTE_SESSIONS); rpc != nil {
		var req stdv1.ListRemoteSessionsRequest
		if proto.Unmarshal(rpc.RequestBytes, &req) == nil && len(req.SessionIds) > 0 {
			fetchedByID = req.SessionIds[0]
		}
	}
	if fetchedByID != "s-missing" {
		t.Fatalf("expected LIST_REMOTE_SESSIONS for s-missing, got %q", fetchedByID)
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
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_060,
		Event: &corev1.Event_SessionWatch{
			SessionWatch: &corev1.SessionWatchEvent{SessionId: "s-rich", Watched: true},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_070,
		Event: testrelay.DemoEventsEvent("s-rich",
			&v1.Event{Seq: 1, Kind: &v1.Event_UserMessage{UserMessage: &v1.UserMessage{
				Text:  "Look at main.go\n\nFile: /Users/me/proj/main.go",
				Files: []*v1.SentFile{{FileId: "f1", Name: "main.go", Path: "/Users/me/proj/main.go", SizeBytes: 120}},
			}}},
			&v1.Event{Seq: 2, Kind: &v1.Event_Reasoning{Reasoning: &v1.Reasoning{Text: "Checking symbols..."}}},
			&v1.Event{Seq: 3, Kind: &v1.Event_ToolCall{ToolCall: &v1.ToolCall{
				CallId: "sf1", Kind: v1.ToolKind_TOOL_KIND_OTHER, Title: "Share", Status: "completed",
				SharedFiles: []*v1.SharedFile{{FileId: "sh1", Name: "report.pdf", Path: "/Users/me/proj/report.pdf", SizeBytes: 4096}},
			}}},
			&v1.Event{Seq: 4, Kind: &v1.Event_ImageGenerated{ImageGenerated: &v1.ImageGenerated{
				RevisedPrompt: "Architecture diagram",
				Images:        []*v1.ImageRef{{ImageId: "img-gen-1", Width: 512, Height: 512}},
			}}},
			&v1.Event{Seq: 5, Kind: &v1.Event_QuestionAsked{QuestionAsked: &v1.QuestionAsked{
				QuestionId: "aq-1", Async: true,
				Questions: []*v1.Question{{Header: "Followup", Text: "Deploy now?"}},
			}}},
			&v1.Event{Seq: 6, Kind: &v1.Event_TokenUsage{TokenUsage: &v1.TokenUsage{
				InputTokens: 150_000, OutputTokens: 20_000, LastTotalTokens: 170_000, ContextWindow: 200_000,
			}}},
			&v1.Event{Seq: 7, Kind: &v1.Event_InputState{InputState: &v1.InputState{
				RequestId: "bad-in", Status: v1.InputStatus_INPUT_STATUS_FAILED, Text: "dropped", Reason: "pane_closed",
			}}},
		),
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
	machines := []*corev1.DemoMachine{
		testrelay.DemoMachine("m-on", true, 0, &v1.MachineMeta{Hostname: "MacStudio"}),
	}
	sessions := []*corev1.DemoSession{
		testrelay.DemoSession("s-plan", "m-on", 10, 1_791_078_060_000, 0,
			&v1.Session{
				Id:       "s-plan",
				Cwd:      "/Users/me/proj",
				Agent:    v1.Agent_AGENT_CLAUDE,
				Status:   v1.SessionStatus_SESSION_STATUS_RUNNING,
				PlanOpen: true,
			},
			&v1.LiveSnapshot{
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
		),
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
		Event: &corev1.Event_SessionAnswerPlan{
			SessionAnswerPlan: &corev1.SessionAnswerPlanAction{
				SessionId: "s-plan",
				Choice:    v1.PlanChoice_PLAN_CHOICE_IMPLEMENT,
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
				RawResult: testrelay.MustRaw(&v1.CommandResult{
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
				}),
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
		Event: &corev1.Event_SessionAnswerPlan{
			SessionAnswerPlan: &corev1.SessionAnswerPlanAction{
				SessionId: "s-plan",
				Choice:    v1.PlanChoice_PLAN_CHOICE_CLEAR_AND_IMPLEMENT,
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
				RawResult: testrelay.MustRaw(&v1.CommandResult{
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
				}),
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
		Event: &corev1.Event_SessionAnswerPlan{
			SessionAnswerPlan: &corev1.SessionAnswerPlanAction{
				SessionId: "s-plan",
				Choice:    v1.PlanChoice_PLAN_CHOICE_CLEAR_AND_IMPLEMENT,
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
				RawResult: testrelay.MustRaw(&v1.CommandResult{
					Outcome:         "executed",
					Reason:          "ok",
					ResultAvailable: true,
					Kind:            &v1.CommandResult_AnswerPlan{AnswerPlan: &v1.AnswerPlanResponse{}},
				}),
			},
		},
	})
	vmDone, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-plan"}}})
	if vmDone.GetSession().GetDock().GetCommand().GetStatus() != corev1.SessionCommandStatus_SESSION_COMMAND_STATUS_IDLE {
		t.Fatalf("expected IDLE command status after executed AnswerPlan, got %+v", vmDone.GetSession().GetDock().GetCommand())
	}
}

func TestCoreClearsTimelineCacheOnTrimHistoryEviction(t *testing.T) {
	c := New()
	defer c.Close()
	nowMs := int64(1_791_078_060_000)
	fx := testrelay.New(t, nowMs)
	fx.Bootstrap(func(ev *corev1.Event) []*corev1.Effect { return c.Dispatch(ev).GetEffects() }, nowMs)
	c.m.sync.SecurityEngine().SetAuthToken("tok-trim", nowMs+7*86_400_000)
	startBatch := c.Dispatch(&corev1.Event{
		NowMs: nowMs + 10,
		Event: &corev1.Event_SyncLifecycle{SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START}},
	})
	legID := testrelay.FindOpenStreamLeg(startBatch.Effects)
	readyBatch := c.Dispatch(&corev1.Event{
		NowMs: nowMs + 20,
		Event: &corev1.Event_StreamReady{StreamReady: &corev1.StreamReadyEvent{LegId: legID, ServerTimeMs: nowMs + 20}},
	})
	ops := testrelay.MustFindRefreshOps(t, readyBatch.Effects)
	c.Dispatch(&corev1.Event{
		NowMs: nowMs + 30,
		Event: &corev1.Event_RelayRpcResult{RelayRpcResult: &corev1.RelayRpcResultEvent{
			OpId: ops.Machines, ResponseBytes: testrelay.MarshalListMachinesResp([]*v1.Machine{fx.MachineWire(fx.M1, true, 0, &v1.MachineMeta{
				Hostname: "MacBook",
				Agents:   []*v1.AgentInfo{{Agent: v1.Agent_AGENT_CODEX}},
			})}),
		}},
	})
	c.Dispatch(&corev1.Event{
		NowMs: nowMs + 30,
		Event: &corev1.Event_RelayRpcResult{RelayRpcResult: &corev1.RelayRpcResultEvent{
			OpId: ops.Sessions, ResponseBytes: testrelay.MarshalListSessionsResp([]*v1.RemoteSession{fx.SessionWire("s-trim", fx.M1, 1, 0, 0, &v1.Session{
				Id:     "s-trim",
				Agent:  v1.Agent_AGENT_CODEX,
				Status: v1.SessionStatus_SESSION_STATUS_RUNNING,
				Title:  "Trim cache",
			}, nil)}, nil),
		}},
	})
	c.Dispatch(&corev1.Event{
		NowMs: nowMs + 30,
		Event: &corev1.Event_RelayRpcResult{RelayRpcResult: &corev1.RelayRpcResultEvent{
			OpId: ops.Devices, ResponseBytes: testrelay.MarshalListDevicesResp(nil),
		}},
	})

	watchBatch := c.Dispatch(&corev1.Event{
		NowMs: nowMs + 100,
		Event: &corev1.Event_SessionWatch{SessionWatch: &corev1.SessionWatchEvent{SessionId: "s-trim", Watched: true}},
	})
	var sweepTimerID uint64
	for _, eff := range watchBatch.Effects {
		if st := eff.GetScheduleTimer(); st != nil && st.Kind == corev1.TimerKind_TIMER_KIND_HISTORY_SWEEP {
			sweepTimerID = st.TimerId
		}
	}
	c.Dispatch(&corev1.Event{
		NowMs: nowMs + 120,
		Event: &corev1.Event_CacheEventsLoaded{
			CacheEventsLoaded: &corev1.CacheEventsLoadedEvent{
				OpId:      watchCacheReadOpID(watchBatch),
				SessionId: "s-trim",
				RawEvents: testrelay.MustRawSlice([]*v1.StoredEvent{
					fx.StoredEventWire("s-trim", 1, &v1.Event{Seq: 1, SessionId: "s-trim", Kind: &v1.Event_UserMessage{UserMessage: &v1.UserMessage{Text: "hello"}}}),
				}),
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
		name          string
		agent         v1.Agent
		wantAgentName string
		statusDetail  string
		wantItemID    string
		wantHasTitle  bool
		wantHintKey   string
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
				[]*corev1.DemoMachine{testrelay.DemoMachine("m-1", true, 0, &v1.MachineMeta{Hostname: "dev"})},
				[]*corev1.DemoSession{testrelay.DemoSession("s-popup", "m-1", 0, 1_791_078_050_000, 0,
					&v1.Session{
						Id:           "s-popup",
						Cwd:          "/work/app",
						Agent:        tc.agent,
						Status:       v1.SessionStatus_SESSION_STATUS_BLOCKED,
						StatusDetail: tc.statusDetail,
					},
					&v1.LiveSnapshot{
						Session: &v1.Session{
							Id:           "s-popup",
							Cwd:          "/work/app",
							Agent:        tc.agent,
							Status:       v1.SessionStatus_SESSION_STATUS_BLOCKED,
							StatusDetail: tc.statusDetail,
						},
					},
				)},
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
		[]*corev1.DemoMachine{
			testrelay.DemoMachine("m-on", true, 0, &v1.MachineMeta{
				Hostname:        "MacStudio",
				ProtocolVersion: sync.PeerVersion,
				Agents: []*v1.AgentInfo{
					{Agent: v1.Agent_AGENT_ANTIGRAVITY, DefaultMode: v1.ApprovalMode_APPROVAL_MODE_ASK},
				},
			}),
		},
		nil,
	)

	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_043,
		Event: &corev1.Event_ProjectOpenDirPicker{
			ProjectOpenDirPicker: &corev1.DirPickerOpenAction{PickerKey: "new-task", MachineId: "m-on", Path: "/Users/me/proj", ForNewTask: true},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_044,
		Event: &corev1.Event_ProjectAdd{
			ProjectAdd: &corev1.ProjectAddAction{Path: "/Users/me/proj", PickerKey: "new-task"},
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
		Event: &corev1.Event_DraftSetText{
			DraftSetText: &corev1.DraftSetTextAction{
				Scope: &corev1.DraftScope{Kind: corev1.DraftScopeKind_DRAFT_SCOPE_KIND_NEW_SESSION, Id: "/Users/me/proj"},
				Text:  prompt,
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
				RawResult: testrelay.MustRaw(&v1.CommandResult{
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
				}),
			},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_110,
		Event: testrelay.DemoSessionEvent(testrelay.DemoSession(sid, "m-on", 2, 0, 0, &v1.Session{
			Id:     sid,
			Cwd:    "/Users/me/proj",
			Agent:  v1.Agent_AGENT_ANTIGRAVITY,
			Status: v1.SessionStatus_SESSION_STATUS_STARTING,
		}, nil)),
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
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_120,
		Event: &corev1.Event_SessionWatch{SessionWatch: &corev1.SessionWatchEvent{SessionId: "s-start", Watched: true}},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_130,
		Event: testrelay.DemoEventsEvent("s-start",
			&v1.Event{
				Seq:  1,
				Kind: &v1.Event_SessionState{SessionState: &v1.SessionState{Status: v1.SessionStatus_SESSION_STATUS_STARTING}},
			},
			&v1.Event{
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
		),
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
		Event: testrelay.DemoEventsEvent("s-start", &v1.Event{
			Seq:  3,
			Kind: &v1.Event_InputState{InputState: &v1.InputState{RequestId: "in-1", Status: v1.InputStatus_INPUT_STATUS_SUBMITTED}},
		}),
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
		Event: testrelay.DemoEventsEvent("s-start", &v1.Event{
			Seq: 4,
			Kind: &v1.Event_UserMessage{
				UserMessage: &v1.UserMessage{Text: "hi", RequestId: "in-1"},
			},
		}),
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
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_120,
		Event: &corev1.Event_SessionWatch{SessionWatch: &corev1.SessionWatchEvent{SessionId: "s-cancel", Watched: true}},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_130,
		Event: testrelay.DemoEventsEvent("s-cancel",
			&v1.Event{
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
			&v1.Event{
				Seq: 2,
				Kind: &v1.Event_InputState{
					InputState: &v1.InputState{
						RequestId: "in-1",
						Status:    v1.InputStatus_INPUT_STATUS_CANCELED,
					},
				},
			},
		),
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
	st := screens.NewState(nil)
	ls := &lists.Session{
		ID:        "s1",
		MachineID: "m1",
		Info:      &v1.Session{Id: "s1", Agent: v1.Agent_AGENT_CODEX, Status: v1.SessionStatus_SESSION_STATUS_RUNNING},
	}
	in := world.SessionSnapshot{
		Found:        true,
		Session:      ls,
		ThisDeviceID: "dev-self",
		DeviceNames:  map[string]string{"dev-other": "MyPhone"},
		Projection: &projection.State{
			PendingInputs: []*projection.PendingInput{
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
		[]*corev1.DemoMachine{
			testrelay.DemoMachine("m-on", true, 0, &v1.MachineMeta{
				Hostname:        "MacStudio",
				ProtocolVersion: sync.PeerVersion,
				Agents: []*v1.AgentInfo{
					{Agent: v1.Agent_AGENT_ANTIGRAVITY, DefaultMode: v1.ApprovalMode_APPROVAL_MODE_ASK},
				},
			}),
		},
		nil,
	)

	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_043,
		Event: &corev1.Event_ProjectOpenDirPicker{
			ProjectOpenDirPicker: &corev1.DirPickerOpenAction{PickerKey: "new-task", MachineId: "m-on", Path: "/Users/me/proj", ForNewTask: true},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_044,
		Event: &corev1.Event_ProjectAdd{
			ProjectAdd: &corev1.ProjectAddAction{Path: "/Users/me/proj", PickerKey: "new-task"},
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
		Event: &corev1.Event_DraftSetText{
			DraftSetText: &corev1.DraftSetTextAction{
				Scope: &corev1.DraftScope{Kind: corev1.DraftScopeKind_DRAFT_SCOPE_KIND_NEW_SESSION, Id: "/Users/me/proj"},
				Text:  "hi",
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
				RawResult: testrelay.MustRaw(&v1.CommandResult{
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
				}),
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
		world.SessionSnapshot{Found: false, SessionsListed: true, LookingUp: true},
		screens.NewState(nil),
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

	// Lookup failure with error must surface LookupFailed + LookupErrorText instead of spinning on Loading.
	lookupErrVM := screens.BuildSessionView(
		world.SessionSnapshot{Found: false, SessionsListed: false, LookingUp: false, LookupFailed: true, LookupError: world.PlainReason("rpc timeout")},
		screens.NewState(nil),
		&corev1.SessionScreenRequest{SessionId: "s-err"},
		screens.NewSessionUIState(),
	)
	if !lookupErrVM.GetHeader().GetLookupFailed() || lookupErrVM.GetHeader().GetLookupErrorText().GetLiteral() != "rpc timeout" {
		t.Fatalf("expected LookupFailed with rpc timeout, got %+v", lookupErrVM.GetHeader())
	}
	if lookupErrVM.GetHeader().GetStatusText().GetKey() == text.KeyCommonLoading {
		t.Fatalf("must not stay on common.loading when LookupFailed with error")
	}

	// Lookup not-found (LookupFailed without error) before SessionsListed must still mark Missing=true.
	notFoundVM := screens.BuildSessionView(
		world.SessionSnapshot{Found: false, SessionsListed: false, LookingUp: false, LookupFailed: true},
		screens.NewState(nil),
		&corev1.SessionScreenRequest{SessionId: "s-gone"},
		screens.NewSessionUIState(),
	)
	if !notFoundVM.GetHeader().GetMissing() || !notFoundVM.GetHeader().GetLookupFailed() {
		t.Fatalf("expected Missing=true & LookupFailed=true, got missing=%v failed=%v", notFoundVM.GetHeader().GetMissing(), notFoundVM.GetHeader().GetLookupFailed())
	}

	// Antigravity running turn must set DefaultSendMode=SEND_MODE_QUEUE (even though SupportsQueueMode=false).
	agySt := screens.NewState(nil)
	agySt.World.Connection = lists.ConnConnected
	agySt.World.Machines = []*lists.Machine{
		{ID: "m1", Online: true, Meta: &v1.MachineMeta{Hostname: "Mac", ProtocolVersion: 17, Capabilities: &v1.MachineCapabilities{ImageUpload: true, FileUpload: true, MaxFilesPerMessage: 4, MaxFileUploadBytes: 50 * 1024 * 1024}}},
		{ID: "m2", Online: false, Meta: &v1.MachineMeta{Hostname: "Linux", ProtocolVersion: 17}},
	}
	agySess := &lists.Session{
		ID:        "s-agy",
		MachineID: "m1",
		Info: &v1.Session{
			Id:              "s-agy",
			Cwd:             "/Users/me/work/repo",
			Agent:           v1.Agent_AGENT_ANTIGRAVITY,
			Status:          v1.SessionStatus_SESSION_STATUS_RUNNING,
			ScheduledTaskId: "task-1",
			TaskOwned:       true,
		},
	}
	agyVM := screens.BuildSessionView(
		world.SessionSnapshot{
			Found:           true,
			SessionsListed:  true,
			LiveLoaded:      true,
			Conn:            lists.ConnConnected,
			ProtocolVersion: 17,
			Session:         agySess,
			Machine:         agySt.World.Machines[0],
			Projection: &projection.State{
				Running: true,
				PendingInputs: []*projection.PendingInput{
					{RequestId: "req-1", Text: "big\n…（remote-agent：内容过大，请在终端查看/回答）", Mode: v1.SendMode_SEND_MODE_QUEUE},
				},
			},
		},
		agySt,
		&corev1.SessionScreenRequest{SessionId: "s-agy"},
		screens.NewSessionUIState(),
	)
	if agyVM.GetComposer().GetDefaultSendMode() != v1.SendMode_SEND_MODE_QUEUE {
		t.Fatalf("expected Antigravity running DefaultSendMode=QUEUE, got %v", agyVM.GetComposer().GetDefaultSendMode())
	}
	if !agyVM.GetHeader().GetTaskOwned() || !agyVM.GetHeader().GetMultipleMachines() || agyVM.GetHeader().GetShortPath() == "" {
		t.Fatalf("expected TaskOwned, MultipleMachines, ShortPath, got %+v", agyVM.GetHeader())
	}
	if agyVM.GetHeader().GetCapabilities().GetMaxFiles() != 4 || agyVM.GetHeader().GetCapabilities().GetMaxFileBytes() != 50*1024*1024 || !agyVM.GetHeader().GetCapabilities().GetCapabilitiesLoaded() {
		t.Fatalf("unexpected capabilities: %+v", agyVM.GetHeader().GetCapabilities())
	}
	if items := agyVM.GetComposer().GetPendingTray().GetOrderedItems(); len(items) != 1 || !items[0].GetIsSkeleton() {
		t.Fatalf("expected IsSkeleton=true on skeleton tray item, got %+v", items)
	}
	if agyVM.GetComposer().GetSlashWarningTitle().GetKey() != text.KeyComposerSlashTitle || agyVM.GetComposer().GetSlashWarningText().GetKey() != text.KeyComposerSlashNote {
		t.Fatalf("expected slash warning texts on composer")
	}

	// Claimed scheduled session retains ScheduledTaskId (IsScheduled=true) while TaskOwned becomes false.
	claimedSess := &lists.Session{
		ID:        "s-agy-claimed",
		MachineID: "m1",
		Info: &v1.Session{
			Id:              "s-agy-claimed",
			Cwd:             "/Users/me/work/repo",
			Agent:           v1.Agent_AGENT_ANTIGRAVITY,
			Status:          v1.SessionStatus_SESSION_STATUS_RUNNING,
			ScheduledTaskId: "task-1",
			TaskOwned:       false,
		},
	}
	claimedVM := screens.BuildSessionView(
		world.SessionSnapshot{
			Found:           true,
			SessionsListed:  true,
			LiveLoaded:      true,
			Conn:            lists.ConnConnected,
			ProtocolVersion: 17,
			Session:         claimedSess,
			Machine:         agySt.World.Machines[0],
		},
		agySt,
		&corev1.SessionScreenRequest{SessionId: "s-agy-claimed"},
		screens.NewSessionUIState(),
	)
	if !claimedVM.GetHeader().GetIsScheduled() || claimedVM.GetHeader().GetTaskOwned() {
		t.Fatalf("expected claimed scheduled session to have IsScheduled=true and TaskOwned=false, got isScheduled=%v taskOwned=%v",
			claimedVM.GetHeader().GetIsScheduled(), claimedVM.GetHeader().GetTaskOwned())
	}
}

func TestSessionRetryLoadAction(t *testing.T) {
	c := New()
	defer c.Close()
	nowMs := int64(1_791_078_060_000)
	fx := testrelay.New(t, nowMs)
	fx.Bootstrap(func(ev *corev1.Event) []*corev1.Effect { return c.Dispatch(ev).GetEffects() }, nowMs)
	c.m.sync.SecurityEngine().SetAuthToken("tok-retry", nowMs+7*86_400_000)

	// Start sync and connect stream so LIST_REMOTE_SESSIONS(session_id) and GET_EVENTS RPCs can be issued.
	effStart := c.Dispatch(&corev1.Event{
		NowMs: nowMs + 10,
		Event: &corev1.Event_SyncLifecycle{
			SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START},
		},
	})
	legID := testrelay.FindOpenStreamLeg(effStart.Effects)
	c.Dispatch(&corev1.Event{
		NowMs: nowMs + 20,
		Event: &corev1.Event_StreamReady{
			StreamReady: &corev1.StreamReadyEvent{LegId: legID, ServerTimeMs: nowMs + 20},
		},
	})

	// 1. Open an unknown session -> issues LIST_REMOTE_SESSIONS(session_id) RPC.
	eff1 := c.Dispatch(&corev1.Event{
		NowMs: nowMs + 100,
		Event: &corev1.Event_SessionOpen{
			SessionOpen: &corev1.SessionOpenAction{SessionId: "s-retry"},
		},
	})
	var op1 uint64
	if rpc := testrelay.FindRelayRpc(eff1.Effects, corev1.RelayRpcMethod_RELAY_RPC_METHOD_LIST_REMOTE_SESSIONS); rpc != nil {
		var req stdv1.ListRemoteSessionsRequest
		if proto.Unmarshal(rpc.RequestBytes, &req) == nil && len(req.SessionIds) == 1 && req.SessionIds[0] == "s-retry" {
			op1 = rpc.OpId
		}
	}
	if op1 == 0 {
		t.Fatalf("expected LIST_REMOTE_SESSIONS(s-retry) RPC on SessionOpen")
	}

	// 2. Fail the lookup -> header reports LookupFailed + LookupErrorText.
	c.Dispatch(&corev1.Event{
		NowMs: nowMs + 200,
		Event: &corev1.Event_RelayRpcResult{
			RelayRpcResult: &corev1.RelayRpcResultEvent{
				OpId:              op1,
				ConnectCode:       14,
				DiagnosticMessage: "rpc unavailable",
			},
		},
	})
	vmFailW, err := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-retry"}}})
	if err != nil {
		t.Fatalf("View failed: %v", err)
	}
	vmFail := vmFailW.GetSession()
	if !vmFail.GetHeader().GetLookupFailed() || vmFail.GetHeader().GetLookupErrorText().GetLiteral() != "rpc unavailable" {
		t.Fatalf("expected LookupFailed with rpc unavailable, got %+v", vmFail.GetHeader())
	}

	// 3. Dispatch unified SessionRetryLoadAction -> clears failure, re-issues LIST_REMOTE_SESSIONS RPC, marks SESSION dirty.
	effRetry := c.Dispatch(&corev1.Event{
		NowMs: nowMs + 300,
		Event: &corev1.Event_SessionRetryLoad{
			SessionRetryLoad: &corev1.SessionRetryLoadAction{SessionId: "s-retry"},
		},
	})
	var op2 uint64
	var renderedSession bool
	for _, ef := range effRetry.GetEffects() {
		if rpc := ef.GetRelayRpc(); rpc != nil && rpc.Method == corev1.RelayRpcMethod_RELAY_RPC_METHOD_LIST_REMOTE_SESSIONS {
			var req stdv1.ListRemoteSessionsRequest
			if proto.Unmarshal(rpc.RequestBytes, &req) == nil && len(req.SessionIds) == 1 && req.SessionIds[0] == "s-retry" {
				op2 = rpc.OpId
			}
		}
		if r := ef.GetRender(); r != nil {
			for _, sc := range r.GetScreens() {
				if sc == corev1.ScreenTarget_SCREEN_TARGET_SESSION {
					renderedSession = true
				}
			}
		}
	}
	if op2 == 0 || op2 == op1 {
		t.Fatalf("expected fresh LIST_REMOTE_SESSIONS RPC on SessionRetryLoadAction, op1=%d op2=%d", op1, op2)
	}
	if !renderedSession {
		t.Fatalf("expected SCREEN_TARGET_SESSION render effect on SessionRetryLoadAction")
	}
	vmLoadingW, err := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-retry"}}})
	if err != nil {
		t.Fatalf("View loading failed: %v", err)
	}
	vmLoading := vmLoadingW.GetSession()
	if vmLoading.GetHeader().GetLookupFailed() {
		t.Fatalf("expected LookupFailed=false while retry lookup is in flight")
	}

	// 4. Succeed lookup with a session that has lastSeq=5, complete cache read, fail initial GetEvents, then retry via SessionRetryLoadAction.
	c.Dispatch(&corev1.Event{
		NowMs: nowMs + 400,
		Event: &corev1.Event_RelayRpcResult{
			RelayRpcResult: &corev1.RelayRpcResultEvent{
				OpId: op2,
				ResponseBytes: testrelay.MarshalListSessionsResp([]*v1.RemoteSession{
					fx.SessionWire("s-retry", fx.M1, 5, 0, 0, &v1.Session{Id: "s-retry", Agent: v1.Agent_AGENT_CODEX, Status: v1.SessionStatus_SESSION_STATUS_RUNNING}, nil),
				}, nil),
			},
		},
	})
	effWatch := c.Dispatch(&corev1.Event{
		NowMs: nowMs + 450,
		Event: &corev1.Event_SessionWatch{
			SessionWatch: &corev1.SessionWatchEvent{SessionId: "s-retry", Watched: true},
		},
	})
	var cacheOp uint64
	for _, ef := range effWatch.GetEffects() {
		if cr := ef.GetCacheRead(); cr != nil && cr.GetEvents() != nil {
			cacheOp = cr.GetEvents().GetOpId()
		}
	}
	effCache := c.Dispatch(&corev1.Event{
		NowMs: nowMs + 460,
		Event: &corev1.Event_CacheEventsLoaded{
			CacheEventsLoaded: &corev1.CacheEventsLoadedEvent{OpId: cacheOp, SessionId: "s-retry"},
		},
	})
	fetchEvOp, _ := testrelay.FindGetEventsOp(effCache.Effects, "s-retry")
	if fetchEvOp > 0 {
		c.Dispatch(&corev1.Event{
			NowMs: nowMs + 470,
			Event: &corev1.Event_RelayRpcResult{
				RelayRpcResult: &corev1.RelayRpcResultEvent{OpId: fetchEvOp, ConnectCode: 4, DiagnosticMessage: "timeout"},
			},
		})
	}
	effRetry2 := c.Dispatch(&corev1.Event{
		NowMs: nowMs + 500,
		Event: &corev1.Event_SessionRetryLoad{
			SessionRetryLoad: &corev1.SessionRetryLoadAction{SessionId: "s-retry"},
		},
	})
	if op, _ := testrelay.FindGetEventsOp(effRetry2.Effects, "s-retry"); op == 0 {
		t.Fatalf("expected GET_EVENTS RPC when retrying an already-loaded session with failed event load")
	}
}

func TestSharedFileDisplayAndLocalLifecycle(t *testing.T) {
	c := New()
	defer c.Close()

	const (
		fileSize   int64 = 10 * 1024 * 1024
		chunkBytes int32 = 2 * 1024 * 1024
	)

	c.Dispatch(&corev1.Event{
		NowMs:  890,
		MonoMs: 890,
		Event: &corev1.Event_EntropyProvided{
			EntropyProvided: &corev1.EntropyProvided{
				RandomIds:  []string{"sf-r1", "sf-r2", "sf-r3", "sf-r4", "sf-r5", "sf-r6"},
				UnitFloats: []float64{0.5, 0.5, 0.5, 0.5},
			},
		},
	})
	hash := sfTestHash(0x01)
	c.Dispatch(&corev1.Event{
		NowMs:  895,
		MonoMs: 895,
		Event: &corev1.Event_SeedDemoWorld{
			SeedDemoWorld: &corev1.SeedDemoWorldEvent{
				Security: &corev1.DemoSecurity{
					IsMember:          true,
					SelfDeviceId:      "dev-1",
					AuthorityDeviceId: "dev-1",
					LeaseEndMs:        99_999_999,
					TrustedMachineIds: []string{"m1"},
				},
				Machines: []*corev1.DemoMachine{
					{
						MachineId: "m1",
						Online:    true,
						RawMeta: testrelay.MustRaw(&v1.MachineMeta{
							Hostname:        "Studio",
							Home:            "/Users/me",
							ProtocolVersion: 17,
							Capabilities: &v1.MachineCapabilities{
								SharedFiles:          true,
								SharedFileChunkBytes: chunkBytes,
							},
						}),
					},
				},
				Sessions: []*corev1.DemoSession{
					{
						SessionId: "s-sf",
						MachineId: "m1",
						LastSeq:   1,
						RawMeta:   testrelay.MustRaw(&v1.Session{Id: "s-sf", Cwd: "/Users/me/proj", Agent: v1.Agent_AGENT_CODEX, Status: v1.SessionStatus_SESSION_STATUS_RUNNING}),
					},
				},
				SessionEvents: []*corev1.DemoSessionEvents{
					{
						SessionId: "s-sf",
						RawEvents: testrelay.MustRawSlice([]*v1.Event{
							{
								Seq: 1,
								Kind: &v1.Event_ToolCall{
									ToolCall: &v1.ToolCall{
										CallId: "c1",
										Status: "completed",
										SharedFiles: []*v1.SharedFile{
											{
												FileId:    "f1",
												Name:      "demo-clip.mp4",
												Path:      "/Users/me/proj/demo-clip.mp4",
												SizeBytes: fileSize,
												Sha256:    hash,
											},
										},
									},
								},
							},
						}),
					},
				},
			},
		},
	})
	effStart := c.Dispatch(&corev1.Event{
		NowMs:  900,
		MonoMs: 900,
		Event: &corev1.Event_SyncLifecycle{
			SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START},
		},
	})
	legID := testrelay.FindOpenStreamLeg(effStart.Effects)
	c.Dispatch(&corev1.Event{
		NowMs:  920,
		MonoMs: 920,
		Event: &corev1.Event_StreamReady{
			StreamReady: &corev1.StreamReadyEvent{LegId: legID, ServerTimeMs: 920},
		},
	})

	getCard := func() *corev1.SessionSharedFileItem {
		t.Helper()
		vm, err := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-sf"}}})
		if err != nil {
			t.Fatalf("View failed: %v", err)
		}
		items := vm.GetSession().GetTimeline().GetItems()
		if len(items) != 1 || items[0].GetSharedFiles() == nil || len(items[0].GetSharedFiles().GetFiles()) != 1 {
			t.Fatalf("expected 1 shared file item, got %+v", items)
		}
		return items[0].GetSharedFiles().GetFiles()[0]
	}

	scanLocal := func(nowMs int64, entries ...*corev1.SharedFileLocalEntry) {
		t.Helper()
		effCfg := c.Dispatch(&corev1.Event{
			NowMs:  nowMs,
			MonoMs: nowMs,
			Event: &corev1.Event_SharedFileConfig{
				SharedFileConfig: &corev1.SharedFileConfigEvent{
					StorageFullKind: corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_PHONE_STORAGE_FULL,
					ScanLocal:       true,
				},
			},
		})
		var scanOp uint64
		for _, ef := range effCfg.GetEffects() {
			if sc := ef.GetSharedFileScanLocal(); sc != nil {
				scanOp = sc.GetOpId()
			}
		}
		if scanOp == 0 {
			t.Fatalf("expected SharedFileScanLocalEffect")
		}
		c.Dispatch(&corev1.Event{
			NowMs:  nowMs + 10,
			MonoMs: nowMs + 10,
			Event: &corev1.Event_SharedFileLocalScanned{
				SharedFileLocalScanned: &corev1.SharedFileLocalScannedEvent{
					OpId:    scanOp,
					Entries: entries,
				},
			},
		})
	}

	// Initially available (demo world is connected + machine online).
	c0 := getCard()
	if c0.GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_AVAILABLE ||
		c0.GetDisplayPath() != "~/proj/demo-clip.mp4" ||
		c0.GetSizeText().GetLiteral() != "10.0 MB" ||
		len(c0.GetSha256()) != 32 {
		t.Fatalf("unexpected initial card: %+v", c0)
	}

	// Start download, inspect disk, deliver first 2 MiB chunk after 16s -> emits SESSION render and updates card with ETA.
	effStartDL := c.Dispatch(&corev1.Event{
		NowMs:  2000,
		MonoMs: 2000,
		Event: &corev1.Event_SharedFileStartDownload{
			SharedFileStartDownload: &corev1.SharedFileStartDownloadAction{
				SessionId: "s-sf",
				FileId:    "f1",
			},
		},
	})
	insp := findInspectDisk(effStartDL)
	if insp == nil {
		t.Fatalf("expected SharedFileInspectDiskEffect")
	}
	effDisp := c.Dispatch(&corev1.Event{
		NowMs:  2050,
		MonoMs: 2050,
		Event: &corev1.Event_SharedFileDiskInspected{
			SharedFileDiskInspected: &corev1.SharedFileDiskInspectedEvent{
				OpId:           insp.GetOpId(),
				SessionId:      "s-sf",
				FileId:         "f1",
				Known:          true,
				AvailableBytes: 100 * 1024 * 1024,
			},
		},
	})
	dc, _ := findDispatchChunk(effDisp)
	if dc == nil {
		t.Fatalf("expected GetSharedFileChunk dispatch")
	}
	effChunk := c.Dispatch(&corev1.Event{
		NowMs:  17_900,
		MonoMs: 17_900,
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:         dc.GetRequestId(),
				AttemptId:         dc.GetAttemptId(),
				Source:            corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON,
				Signed:            true,
				HashesMatch:       true,
				AttemptBound:      true,
				OuterMatchesInner: true,
				OuterOutcome:      "executed",
				OuterReason:       "executed",
				RawResult: testrelay.MustRaw(&v1.CommandResult{
					Outcome:         "executed",
					Reason:          "executed",
					ResultAvailable: true,
					Kind: &v1.CommandResult_GetSharedFileChunk{
						GetSharedFileChunk: &v1.GetSharedFileChunkResponse{
							Data:       make([]byte, chunkBytes),
							TotalBytes: sfUint64(uint64(fileSize)),
						},
					},
				}),
			},
		},
	})
	wc := findWriteChunk(effChunk)
	if wc == nil {
		t.Fatalf("expected SharedFileWriteChunkEffect")
	}
	effDL := c.Dispatch(&corev1.Event{
		NowMs:  18_000,
		MonoMs: 18_000,
		Event: &corev1.Event_SharedFileWriteChunkResult{
			SharedFileWriteChunkResult: &corev1.SharedFileWriteChunkResultEvent{
				OpId:      wc.GetOpId(),
				SessionId: "s-sf",
				FileId:    "f1",
				Ok:        true,
			},
		},
	})
	hasSessionRender := false
	for _, ef := range effDL.GetEffects() {
		if r := ef.GetRender(); r != nil {
			for _, sc := range r.GetScreens() {
				if sc == corev1.ScreenTarget_SCREEN_TARGET_SESSION {
					hasSessionRender = true
				}
			}
		}
	}
	if !hasSessionRender {
		t.Fatalf("expected SCREEN_TARGET_SESSION render effect after chunk write, got %+v", effDL.GetEffects())
	}
	c1 := getCard()
	if c1.GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_DOWNLOADING ||
		c1.GetDownloadedBytes() != int64(chunkBytes) ||
		c1.GetSubtitle().GetKey() != text.KeySharedFileSubtitleDownloadingEta ||
		c1.GetPrimaryAction() != corev1.SharedFilePrimaryAction_SHARED_FILE_PRIMARY_ACTION_PAUSE {
		t.Fatalf("unexpected downloading card: %+v", c1)
	}

	// Pause download -> card enters PAUSED with RESUME primary action and preserves downloaded bytes.
	c.Dispatch(&corev1.Event{
		NowMs:  18_100,
		MonoMs: 18_100,
		Event: &corev1.Event_SharedFilePauseDownload{
			SharedFilePauseDownload: &corev1.SharedFilePauseDownloadAction{SessionId: "s-sf", FileId: "f1"},
		},
	})
	cPaused := getCard()
	if cPaused.GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_PAUSED ||
		cPaused.GetDownloadedBytes() != int64(chunkBytes) ||
		cPaused.GetSubtitle().GetKey() != text.KeySharedFileSubtitlePaused ||
		cPaused.GetPrimaryAction() != corev1.SharedFilePrimaryAction_SHARED_FILE_PRIMARY_ACTION_RESUME {
		t.Fatalf("unexpected paused card: %+v", cPaused)
	}

	// Local scan reports downloaded copy, then duration probe arrives -> preserves DOWNLOADED and adds duration subtitle.
	scanLocal(18_200, &corev1.SharedFileLocalEntry{
		SessionId:         "s-sf",
		FileId:            "f1",
		Completed:         true,
		BytesOnDisk:       fileSize,
		ExpectedSizeBytes: fileSize,
	})
	c.Dispatch(&corev1.Event{
		NowMs:  18_250,
		MonoMs: 18_250,
		Event: &corev1.Event_SharedFileDurationProbed{
			SharedFileDurationProbed: &corev1.SharedFileDurationProbedEvent{
				SessionId:  "s-sf",
				FileId:     "f1",
				DurationMs: 92_000,
			},
		},
	})
	c2 := getCard()
	if c2.GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_DOWNLOADED ||
		c2.GetDurationText() != "1:32" ||
		c2.GetSubtitle().GetKey() != text.KeySharedFileSubtitleMediaSize ||
		c2.GetPrimaryAction() != corev1.SharedFilePrimaryAction_SHARED_FILE_PRIMARY_ACTION_OPEN {
		t.Fatalf("unexpected downloaded card with duration: %+v", c2)
	}

	// Delete local copy -> emits DeleteLocalEffect and returns to AVAILABLE.
	effDel := c.Dispatch(&corev1.Event{
		NowMs:  18_300,
		MonoMs: 18_300,
		Event: &corev1.Event_SharedFileDeleteLocal{
			SharedFileDeleteLocal: &corev1.SharedFileDeleteLocalAction{SessionId: "s-sf", FileId: "f1"},
		},
	})
	if findDeleteLocal(effDel) == nil {
		t.Fatalf("expected SharedFileDeleteLocalEffect")
	}
	c3 := getCard()
	if c3.GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_AVAILABLE {
		t.Fatalf("expected AVAILABLE after deleting local copy, got %v", c3.GetStatus())
	}

	// Out-of-order duration probe arriving BEFORE transfer state must be retained and merged when DOWNLOADED arrives.
	c.Dispatch(&corev1.Event{
		NowMs:  18_350,
		MonoMs: 18_350,
		Event: &corev1.Event_SharedFileDurationProbed{
			SharedFileDurationProbed: &corev1.SharedFileDurationProbedEvent{
				SessionId:  "s-sf",
				FileId:     "f1",
				DurationMs: 61_000,
			},
		},
	})
	if cPre := getCard(); cPre.GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_AVAILABLE {
		t.Fatalf("duration probe alone should leave status AVAILABLE, got %v", cPre.GetStatus())
	}
	scanLocal(18_400, &corev1.SharedFileLocalEntry{
		SessionId:         "s-sf",
		FileId:            "f1",
		Completed:         true,
		BytesOnDisk:       fileSize,
		ExpectedSizeBytes: fileSize,
	})
	if cMerged := getCard(); cMerged.GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_DOWNLOADED ||
		cMerged.GetDurationText() != "1:01" ||
		cMerged.GetSubtitle().GetKey() != text.KeySharedFileSubtitleMediaSize {
		t.Fatalf("expected pre-arrived duration merged into DOWNLOADED card: %+v", cMerged)
	}

	// Forget session clears that session's local shared file state and emits DeleteSessionEffect.
	effForget := c.Dispatch(&corev1.Event{
		NowMs:  18_450,
		MonoMs: 18_450,
		Event: &corev1.Event_SharedFileForgetSession{
			SharedFileForgetSession: &corev1.SharedFileForgetSessionAction{SessionId: "s-sf"},
		},
	})
	hasDeleteSession := false
	for _, ef := range effForget.GetEffects() {
		if ds := ef.GetSharedFileDeleteSession(); ds != nil && ds.GetSessionId() == "s-sf" {
			hasDeleteSession = true
		}
	}
	if !hasDeleteSession {
		t.Fatalf("expected SharedFileDeleteSessionEffect")
	}
	if cCleared := getCard(); cCleared.GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_AVAILABLE {
		t.Fatalf("expected AVAILABLE after forget session, got %v", cCleared.GetStatus())
	}

	// Clear all clears all sessions' local shared file state and emits ClearAllEffect.
	scanLocal(18_500, &corev1.SharedFileLocalEntry{
		SessionId:         "s-sf",
		FileId:            "f1",
		Completed:         true,
		BytesOnDisk:       fileSize,
		ExpectedSizeBytes: fileSize,
	})
	effClear := c.Dispatch(&corev1.Event{
		NowMs:  18_550,
		MonoMs: 18_550,
		Event: &corev1.Event_SharedFileClearAll{
			SharedFileClearAll: &corev1.SharedFileClearAllAction{},
		},
	})
	hasClearAll := false
	for _, ef := range effClear.GetEffects() {
		if ef.GetSharedFileClearAll() != nil {
			hasClearAll = true
		}
	}
	if !hasClearAll {
		t.Fatalf("expected SharedFileClearAllEffect")
	}
	if cReplaced := getCard(); cReplaced.GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_AVAILABLE {
		t.Fatalf("expected AVAILABLE after clear all, got %v", cReplaced.GetStatus())
	}
}

func TestSessionImageViewerLifecycleAndPlaceholders(t *testing.T) {
	c := New()
	defer c.Close()

	c.Dispatch(&corev1.Event{
		NowMs: 1000,
		Event: &corev1.Event_SeedDemoWorld{
			SeedDemoWorld: &corev1.SeedDemoWorldEvent{
				Machines: []*corev1.DemoMachine{
					{MachineId: "m1", Online: true, RawMeta: testrelay.MustRaw(&v1.MachineMeta{Hostname: "Mac", Home: "/Users/me", ProtocolVersion: 17})},
				},
				Sessions: []*corev1.DemoSession{
					{SessionId: "s-img", MachineId: "m1", LastSeq: 3, RawMeta: testrelay.MustRaw(&v1.Session{Id: "s-img", Agent: v1.Agent_AGENT_CODEX, Status: v1.SessionStatus_SESSION_STATUS_RUNNING})},
				},
				SessionEvents: []*corev1.DemoSessionEvents{
					{
						SessionId: "s-img",
						RawEvents: testrelay.MustRawSlice([]*v1.Event{
							{
								Seq: 1,
								Kind: &v1.Event_UserMessage{
									UserMessage: &v1.UserMessage{
										Text: "[Image #1]desktop, [Image #2]phone",
										Images: []*v1.ImageRef{
											{ImageId: "img1", CliIndex: 1, Source: v1.ImageSource_IMAGE_SOURCE_USER, Sync: v1.ImageSync_IMAGE_SYNC_SYNCED, Width: 800, Height: 600},
											{ImageId: "img2", CliIndex: 2, Source: v1.ImageSource_IMAGE_SOURCE_USER, Sync: v1.ImageSync_IMAGE_SYNC_SYNCED, Width: 400, Height: 800},
										},
									},
								},
							},
							{
								Seq: 2,
								Kind: &v1.Event_ImageGenerated{
									ImageGenerated: &v1.ImageGenerated{
										RevisedPrompt: "sunset",
										Images: []*v1.ImageRef{
											{ImageId: "img3", Source: v1.ImageSource_IMAGE_SOURCE_GENERATED, Sync: v1.ImageSync_IMAGE_SYNC_SYNCED, Width: 1024, Height: 1024},
										},
									},
								},
							},
						}),
					},
				},
			},
		},
	})

	view := func() *corev1.SessionViewModel {
		vm, err := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-img"}}})
		if err != nil {
			t.Fatalf("View failed: %v", err)
		}
		return vm.GetSession()
	}

	v0 := view()
	segs := v0.GetTimeline().GetItems()[0].GetUserMessage().GetTextSegments()
	if len(segs) != 4 || segs[0].GetImageIndex() != 1 || segs[1].GetText() != "desktop, " || segs[2].GetImageIndex() != 2 || segs[3].GetText() != "phone" {
		t.Fatalf("unexpected inline placeholder segments: %+v", segs)
	}
	if len(v0.GetTimeline().GetViewerImages()) != 3 {
		t.Fatalf("expected 3 viewer images, got %d", len(v0.GetTimeline().GetViewerImages()))
	}
	if v0.GetImageViewer().GetOpen() || v0.GetImageViewer().GetTotalCount() != 3 {
		t.Fatalf("unexpected closed viewer state: %+v", v0.GetImageViewer())
	}

	// Open at seq=1, image_index=1 (2nd image, index 1).
	c.Dispatch(&corev1.Event{
		NowMs: 1100,
		Event: &corev1.Event_SessionImageViewerOpen{
			SessionImageViewerOpen: &corev1.SessionImageViewerOpenAction{SessionId: "s-img", Seq: 1, ImageIndex: 1},
		},
	})
	v1m := view().GetImageViewer()
	if !v1m.GetOpen() || v1m.GetCurrentIndex() != 1 || !v1m.GetHasPrev() || !v1m.GetHasNext() ||
		v1m.GetPositionText().GetLiteral() != "2 / 3" || v1m.GetCurrentImage().GetImageId() != "img2" {
		t.Fatalf("unexpected opened viewer: %+v", v1m)
	}

	// Navigate next (+1) -> index 2 (img3), has_next=false.
	c.Dispatch(&corev1.Event{
		NowMs: 1200,
		Event: &corev1.Event_SessionImageViewerNavigate{
			SessionImageViewerNavigate: &corev1.SessionImageViewerNavigateAction{SessionId: "s-img", Delta: 1},
		},
	})
	v2m := view().GetImageViewer()
	if v2m.GetCurrentIndex() != 2 || !v2m.GetHasPrev() || v2m.GetHasNext() || v2m.GetCurrentImage().GetImageId() != "img3" {
		t.Fatalf("unexpected next viewer: %+v", v2m)
	}

	// Navigate prev (-1) -> index 1, then direct jump to index 0.
	c.Dispatch(&corev1.Event{
		NowMs: 1300,
		Event: &corev1.Event_SessionImageViewerNavigate{
			SessionImageViewerNavigate: &corev1.SessionImageViewerNavigateAction{SessionId: "s-img", Delta: -1},
		},
	})
	if got := view().GetImageViewer().GetCurrentIndex(); got != 1 {
		t.Fatalf("expected index 1 after prev, got %d", got)
	}
	c.Dispatch(&corev1.Event{
		NowMs: 1350,
		Event: &corev1.Event_SessionImageViewerNavigate{
			SessionImageViewerNavigate: &corev1.SessionImageViewerNavigateAction{SessionId: "s-img", SetIndex: true, Index: 0},
		},
	})
	v3m := view().GetImageViewer()
	if v3m.GetCurrentIndex() != 0 || v3m.GetHasPrev() || !v3m.GetHasNext() {
		t.Fatalf("unexpected jumped viewer: %+v", v3m)
	}

	// Close viewer.
	c.Dispatch(&corev1.Event{
		NowMs: 1400,
		Event: &corev1.Event_SessionImageViewerClose{
			SessionImageViewerClose: &corev1.SessionImageViewerCloseAction{SessionId: "s-img"},
		},
	})
	if view().GetImageViewer().GetOpen() {
		t.Fatalf("expected viewer closed")
	}
}

func TestSessionImageViewerOutOfWindowAnchorAndNavigation(t *testing.T) {
	c := New()
	defer c.Close()

	c.Dispatch(&corev1.Event{
		NowMs: 1000,
		Event: &corev1.Event_SeedDemoWorld{
			SeedDemoWorld: &corev1.SeedDemoWorldEvent{
				Machines: []*corev1.DemoMachine{
					{MachineId: "m1", Online: true, RawMeta: testrelay.MustRaw(&v1.MachineMeta{Hostname: "Mac", Home: "/Users/me", ProtocolVersion: 17})},
				},
				Sessions: []*corev1.DemoSession{
					{SessionId: "s-win", MachineId: "m1", LastSeq: 6, RawMeta: testrelay.MustRaw(&v1.Session{Id: "s-win", Agent: v1.Agent_AGENT_CODEX, Status: v1.SessionStatus_SESSION_STATUS_RUNNING})},
				},
				SessionEvents: []*corev1.DemoSessionEvents{
					{
						SessionId: "s-win",
						RawEvents: testrelay.MustRawSlice([]*v1.Event{
							{
								Seq:       1,
								SessionId: "s-win",
								Kind: &v1.Event_UserMessage{
									UserMessage: &v1.UserMessage{
										Text: "first",
										Images: []*v1.ImageRef{
											{ImageId: "img1", Sync: v1.ImageSync_IMAGE_SYNC_SYNCED, Format: v1.ImageFormat_IMAGE_FORMAT_PNG, Preview: []byte{1, 2, 3, 4}, PreviewWidth: 320, PreviewHeight: 240},
										},
									},
								},
							},
							{
								Seq:       2,
								SessionId: "s-win",
								Kind: &v1.Event_UserMessage{
									UserMessage: &v1.UserMessage{
										Text: "second",
										Images: []*v1.ImageRef{
											{ImageId: "img2", Sync: v1.ImageSync_IMAGE_SYNC_SYNCED, Format: v1.ImageFormat_IMAGE_FORMAT_JPEG, Preview: []byte{5, 6, 7, 8}, Width: 800, Height: 600},
										},
									},
								},
							},
							{Seq: 3, SessionId: "s-win", Kind: &v1.Event_AgentMessage{AgentMessage: &v1.AgentMessage{Text: "msg3"}}},
							{Seq: 4, SessionId: "s-win", Kind: &v1.Event_AgentMessage{AgentMessage: &v1.AgentMessage{Text: "msg4"}}},
							{Seq: 5, SessionId: "s-win", Kind: &v1.Event_AgentMessage{AgentMessage: &v1.AgentMessage{Text: "msg5"}}},
							{
								Seq:       6,
								SessionId: "s-win",
								Kind: &v1.Event_UserMessage{
									UserMessage: &v1.UserMessage{
										Text: "latest",
										Images: []*v1.ImageRef{
											{ImageId: "img3", Sync: v1.ImageSync_IMAGE_SYNC_SYNCED, Format: v1.ImageFormat_IMAGE_FORMAT_WEBP, Preview: []byte{9, 10}, Width: 600, Height: 600},
										},
									},
								},
							},
						}),
					},
				},
			},
		},
	})

	viewSmallWindow := func() *corev1.SessionViewModel {
		vm, err := c.View(&corev1.Screen{
			Screen: &corev1.Screen_Session{
				Session: &corev1.SessionScreenRequest{
					SessionId: "s-win",
					Window:    &corev1.SessionWindowRequest{Limit: 2},
				},
			},
		})
		if err != nil {
			t.Fatalf("View error: %v", err)
		}
		return vm.GetSession()
	}

	// Open and navigate BEFORE any View() call, starting from out-of-window anchor seq=2.
	c.Dispatch(&corev1.Event{
		NowMs: 1100,
		Event: &corev1.Event_SessionImageViewerOpen{
			SessionImageViewerOpen: &corev1.SessionImageViewerOpenAction{SessionId: "s-win", Seq: 2, ImageIndex: 0},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1150,
		Event: &corev1.Event_SessionImageViewerNavigate{
			SessionImageViewerNavigate: &corev1.SessionImageViewerNavigateAction{SessionId: "s-win", Delta: 1},
		},
	})
	s1 := viewSmallWindow()
	if len(s1.GetTimeline().GetItems()) != 2 {
		t.Fatalf("expected 2 windowed timeline items, got %d", len(s1.GetTimeline().GetItems()))
	}
	if len(s1.GetTimeline().GetViewerImages()) != 3 {
		t.Fatalf("expected all 3 session viewer_images despite Limit=2 window, got %d", len(s1.GetTimeline().GetViewerImages()))
	}
	for _, vi := range s1.GetTimeline().GetViewerImages() {
		if len(vi.GetPreview()) != 0 {
			t.Fatalf("viewer image %s still carries preview bytes", vi.GetImageId())
		}
	}
	if iv := s1.GetImageViewer(); !iv.GetOpen() || iv.GetCurrentIndex() != 2 || iv.GetCurrentImage().GetImageId() != "img3" {
		t.Fatalf("expected navigate from out-of-window seq=2 (+1) before View to land on index 2 (img3), got %+v", iv)
	}

	// Now open out-of-window seq=2 directly and verify it resolves to index 1 (NOT fallback 0) and steps backward to index 0.
	c.Dispatch(&corev1.Event{
		NowMs: 1200,
		Event: &corev1.Event_SessionImageViewerOpen{
			SessionImageViewerOpen: &corev1.SessionImageViewerOpenAction{SessionId: "s-win", Seq: 2, ImageIndex: 0},
		},
	})
	s2 := viewSmallWindow()
	if iv := s2.GetImageViewer(); iv.GetCurrentIndex() != 1 || iv.GetCurrentImage().GetImageId() != "img2" || !iv.GetHasPrev() || !iv.GetHasNext() {
		t.Fatalf("expected out-of-window anchor seq=2 -> index 1 (img2), got %+v", iv)
	}
	c.Dispatch(&corev1.Event{
		NowMs: 1250,
		Event: &corev1.Event_SessionImageViewerNavigate{
			SessionImageViewerNavigate: &corev1.SessionImageViewerNavigateAction{SessionId: "s-win", Delta: -1},
		},
	})
	s3 := viewSmallWindow()
	if iv := s3.GetImageViewer(); iv.GetCurrentIndex() != 0 || iv.GetCurrentImage().GetImageId() != "img1" || iv.GetHasPrev() {
		t.Fatalf("expected prev from out-of-window index 1 -> index 0 (img1), got %+v", iv)
	}
}

func newAttachmentEvalCore(t *testing.T, online bool) *Core {
	t.Helper()
	c := New()

	c.Dispatch(&corev1.Event{
		NowMs: 890,
		Event: &corev1.Event_SeedDemoWorld{
			SeedDemoWorld: &corev1.SeedDemoWorldEvent{
				Security: &corev1.DemoSecurity{
					IsMember:          true,
					SelfDeviceId:      "dev-1",
					AuthorityDeviceId: "dev-1",
					LeaseEndMs:        99_999_999,
					TrustedMachineIds: []string{"m-att"},
				},
				Machines: []*corev1.DemoMachine{
					{
						MachineId: "m-att",
						Online:    online,
						RawMeta: testrelay.MustRaw(&v1.MachineMeta{
							Hostname: "MacBook",
							Capabilities: &v1.MachineCapabilities{
								ImageUpload:         true,
								FileUpload:          true,
								MaxImagesPerMessage: 8,
								MaxFilesPerMessage:  12,                // must be clamped to 6
								MaxFileUploadBytes:  250 * 1024 * 1024, // must be clamped to 100 MiB
							},
						}),
					},
				},
				Sessions: []*corev1.DemoSession{
					{
						SessionId: "s-att",
						MachineId: "m-att",
						RawMeta:   testrelay.MustRaw(&v1.Session{Id: "s-att", Agent: v1.Agent_AGENT_CODEX, Status: v1.SessionStatus_SESSION_STATUS_RUNNING}),
					},
				},
			},
		},
	})
	start := c.Dispatch(&corev1.Event{
		NowMs: 900,
		Event: &corev1.Event_SyncLifecycle{SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START}},
	})
	legID := testrelay.FindOpenStreamLeg(start.Effects)
	c.Dispatch(&corev1.Event{
		NowMs: 920,
		Event: &corev1.Event_StreamReady{StreamReady: &corev1.StreamReadyEvent{LegId: legID, ServerTimeMs: 920}},
	})

	return c
}

func TestAttachmentEvalScreenAndCapabilities(t *testing.T) {
	c := newAttachmentEvalCore(t, true)
	defer c.Close()

	vm, err := c.View(&corev1.Screen{
		Screen: &corev1.Screen_AttachmentEval{
			AttachmentEval: &corev1.AttachmentEvalScreenRequest{
				SessionId:     "s-att",
				CurrentImages: 5,
				CurrentFiles:  5,
				Candidates: []*corev1.AttachmentCandidateInput{
					{Name: "pasted.jpg", MimeType: "image/jpeg", SizeBytes: 2048},
					{Name: "dropped.jpg", MimeType: "image/jpeg", SizeBytes: 2048},
					{Name: "clip.mov", MimeType: "", SizeBytes: 120 * 1024 * 1024},
					{Name: "short.mp4", MimeType: "video/mp4", SizeBytes: 4 * 1024 * 1024},
				},
				Problems: []*corev1.ImageProblemInput{
					{ErrorCode: "resource_exhausted"},
					{RawDiagnostic: "upload png: network timeout"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("AttachmentEval View error: %v", err)
	}
	ev := vm.GetAttachmentEval()
	if ev.GetMaxImages() != 6 || ev.GetMaxFiles() != 6 || ev.GetMaxFileBytes() != 100*1024*1024 {
		t.Fatalf("unexpected clamped limits: images=%d files=%d bytes=%d", ev.GetMaxImages(), ev.GetMaxFiles(), ev.GetMaxFileBytes())
	}
	if !ev.GetGalleryAllowsVideo() || ev.GetGalleryAcceptMime() != "image/*,video/*" || ev.GetLibraryRoom() != 2 {
		t.Fatalf("unexpected gallery caps: allows=%v accept=%q room=%d", ev.GetGalleryAllowsVideo(), ev.GetGalleryAcceptMime(), ev.GetLibraryRoom())
	}
	if len(ev.GetDecisions()) != 4 {
		t.Fatalf("expected 4 decisions, got %d", len(ev.GetDecisions()))
	}
	if !ev.Decisions[0].Accepted || ev.Decisions[1].Accepted || ev.Decisions[2].Accepted || !ev.Decisions[3].Accepted {
		t.Fatalf("unexpected acceptance vector: %+v", ev.Decisions)
	}
	if ev.Decisions[3].Route != corev1.AttachmentCandidateRoute_ATTACHMENT_CANDIDATE_ROUTE_VIDEO_FILE {
		t.Fatalf("expected VIDEO_FILE route, got %v", ev.Decisions[3].Route)
	}
	if len(ev.GetProblems()) != 2 || ev.GetFailureSummary().GetKey() != "image.attach_failed" {
		t.Fatalf("unexpected problems/summary: %+v / %+v", ev.GetProblems(), ev.GetFailureSummary())
	}
	if ev.GetImagesPickBlockedReason() != nil || ev.GetFilesPickBlockedReason() != nil {
		t.Fatalf("pick reasons must be unset below the limits: %+v / %+v", ev.GetImagesPickBlockedReason(), ev.GetFilesPickBlockedReason())
	}

	full, err := c.View(&corev1.Screen{Screen: &corev1.Screen_AttachmentEval{AttachmentEval: &corev1.AttachmentEvalScreenRequest{
		SessionId: "s-att", CurrentImages: 6, CurrentFiles: 6,
	}}})
	if err != nil {
		t.Fatalf("AttachmentEval View error: %v", err)
	}
	fev := full.GetAttachmentEval()
	if fev.GetCanPickImages() || fev.GetCanPickFiles() ||
		fev.GetImagesPickBlockedReason().GetKey() != "attachment.max_images" ||
		fev.GetFilesPickBlockedReason().GetKey() != "attachment.max_files" {
		t.Fatalf("at the limits the core must explain why picking is blocked: %+v / %+v", fev.GetImagesPickBlockedReason(), fev.GetFilesPickBlockedReason())
	}
}

func TestAttachmentEvalPutBackIgnoresOffline(t *testing.T) {
	c := newAttachmentEvalCore(t, false)
	defer c.Close()

	eval := func(putBack bool) *corev1.AttachmentEvalViewModel {
		vm, err := c.View(&corev1.Screen{Screen: &corev1.Screen_AttachmentEval{AttachmentEval: &corev1.AttachmentEvalScreenRequest{
			SessionId:     "s-att",
			CurrentImages: 5,
			PutBack:       putBack,
			Candidates: []*corev1.AttachmentCandidateInput{
				{Name: "a.png", MimeType: "image/png", SizeBytes: 1024},
				{Name: "b.png", MimeType: "image/png", SizeBytes: 1024},
			},
		}}})
		if err != nil {
			t.Fatalf("AttachmentEval View error: %v", err)
		}
		return vm.GetAttachmentEval()
	}
	if live := eval(false); live.Decisions[0].Accepted {
		t.Fatalf("an offline machine must block new images: %+v", live.Decisions)
	}
	back := eval(true)
	if !back.Decisions[0].Accepted || back.Decisions[1].Accepted {
		t.Fatalf("put-back must ignore offline but keep the 6-image limit: %+v", back.Decisions)
	}
}

func TestSessionModelChoiceMaxEffortHint(t *testing.T) {
	st := screens.NewState(nil)
	st.World.Connection = lists.ConnConnected
	st.World.Machines = []*lists.Machine{{
		ID:     "m-1",
		Online: true,
		Meta:   &v1.MachineMeta{Hostname: "Box", ProtocolVersion: sync.PeerVersion},
	}}
	st.MachineModels[screens.MachineAgentKey{MachineID: "m-1", Agent: v1.Agent_AGENT_CODEX}] = &screens.ModelsCacheEntry{
		Loaded: true,
		Response: &v1.ListModelsResponse{
			Models: []*v1.ModelInfo{
				{Slug: "gpt-5-mini", DisplayName: "Mini", Efforts: []string{"low", "medium", "high"}, DefaultEffort: "medium"},
				{Slug: "gpt-5", DisplayName: "Full", Efforts: []string{"low", "medium", "max"}, DefaultEffort: "medium"},
				{Slug: "gpt-fixed", DisplayName: "Fixed", Efforts: nil},
			},
		},
	}
	snap := world.SessionSnapshot{
		Found:          true,
		SessionsListed: true,
		Session: &lists.Session{
			ID:        "s-1",
			MachineID: "m-1",
			Info: &v1.Session{
				Id:    "s-1",
				Agent: v1.Agent_AGENT_CODEX,
				Cwd:   "/work/app",
				Model: "gpt-5-mini",
			},
		},
	}
	vm := screens.BuildSessionView(snap, st, &corev1.SessionScreenRequest{SessionId: "s-1"}, screens.NewSessionUIState())
	choices := vm.GetActions().GetModelChoices()
	if len(choices) != 3 {
		t.Fatalf("expected 3 choices, got %d", len(choices))
	}
	if choices[0].GetMaxEffortHint().GetKey() != text.KeySessionMaxEffort {
		t.Fatalf("expected max_effort_hint on high-capped Codex model, got %+v", choices[0].GetMaxEffortHint())
	}
	if choices[1].GetMaxEffortHint() != nil {
		t.Fatalf("expected no max_effort_hint when max effort is available, got %+v", choices[1].GetMaxEffortHint())
	}
	if choices[2].GetMaxEffortHint() != nil {
		t.Fatalf("expected no max_effort_hint when model has no efforts, got %+v", choices[2].GetMaxEffortHint())
	}
}

func TestPingAndCancelMachineOneShot(t *testing.T) {
	c := bootstrapReadyApp(t, []*corev1.DemoMachine{
		testrelay.DemoMachine("m-on", true, 0, &v1.MachineMeta{Hostname: "Box", ProtocolVersion: sync.PeerVersion}),
	}, nil)
	now := int64(1_791_078_061_000)

	// 1. Invalid argument (missing machine_id) settles immediately with ok=false.
	badBatch := c.Dispatch(&corev1.Event{
		NowMs: now,
		Event: &corev1.Event_PingMachine{
			PingMachine: &corev1.PingMachineAction{OpId: 1},
		},
	})
	var badRes *corev1.PingMachineResultEffect
	for _, ef := range badBatch.GetEffects() {
		if r := ef.GetPingMachineResult(); r != nil {
			badRes = r
		}
	}
	if badRes == nil || badRes.Ok || badRes.OpId != 1 || badRes.ErrorCode != "invalid_argument" || badRes.GetErrorText().GetKey() != text.KeyErrorsInvalidCommand {
		t.Fatalf("expected immediate invalid_argument result with error_text, got %+v", badRes)
	}

	// 1b. Settled daemon error without error_message still populates fallback error_text.
	runErrBatch := c.Dispatch(&corev1.Event{
		NowMs: now + 5,
		Event: &corev1.Event_PingMachine{
			PingMachine: &corev1.PingMachineAction{OpId: 11, MachineId: "m-on"},
		},
	})
	var dcErr *corev1.DispatchCommandEffect
	for _, ef := range runErrBatch.GetEffects() {
		if d := ef.GetDispatchCommand(); d != nil {
			dcErr = d
		}
	}
	if dcErr == nil {
		t.Fatalf("expected DispatchCommandEffect for op 11")
	}
	errDoneBatch := c.Dispatch(&corev1.Event{
		NowMs: now + 8,
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:         dcErr.RequestId,
				AttemptId:         dcErr.AttemptId,
				Source:            corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON,
				Signed:            true,
				HashesMatch:       true,
				AttemptBound:      true,
				OuterMatchesInner: true,
				OuterOutcome:      "executed",
				OuterReason:       "failed",
				RawResult: testrelay.MustRaw(&v1.CommandResult{
					Outcome:         "executed",
					Reason:          "failed",
					ResultAvailable: true,
					ErrorCode:       "failed",
				}),
			},
		},
	})
	var errRes *corev1.PingMachineResultEffect
	for _, ef := range errDoneBatch.GetEffects() {
		if r := ef.GetPingMachineResult(); r != nil {
			errRes = r
		}
	}
	if errRes == nil || errRes.Ok || errRes.OpId != 11 || errRes.GetErrorText() == nil {
		t.Fatalf("expected non-nil error_text on daemon error without message, got %+v", errRes)
	}

	// 2. Valid one-shot ping dispatches and settles with ok=true.
	runBatch := c.Dispatch(&corev1.Event{
		NowMs: now + 10,
		Event: &corev1.Event_PingMachine{
			PingMachine: &corev1.PingMachineAction{OpId: 2, MachineId: "m-on"},
		},
	})
	var dc *corev1.DispatchCommandEffect
	for _, ef := range runBatch.GetEffects() {
		if d := ef.GetDispatchCommand(); d != nil {
			dc = d
		}
	}
	if dc == nil {
		t.Fatalf("expected DispatchCommandEffect, got %+v", runBatch.GetEffects())
	}
	doneBatch := c.Dispatch(&corev1.Event{
		NowMs: now + 50,
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:         dc.RequestId,
				AttemptId:         dc.AttemptId,
				Source:            corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON,
				Signed:            true,
				HashesMatch:       true,
				AttemptBound:      true,
				OuterMatchesInner: true,
				OuterOutcome:      "executed",
				OuterReason:       "ok",
				RawResult: testrelay.MustRaw(&v1.CommandResult{
					Outcome:         "executed",
					Reason:          "ok",
					ResultAvailable: true,
					Kind:            &v1.CommandResult_GetSettings{GetSettings: &v1.GetSettingsResponse{}},
				}),
			},
		},
	})
	var okRes *corev1.PingMachineResultEffect
	for _, ef := range doneBatch.GetEffects() {
		if r := ef.GetPingMachineResult(); r != nil {
			okRes = r
		}
	}
	if okRes == nil || !okRes.Ok || okRes.OpId != 2 {
		t.Fatalf("expected ok result for op 2, got %+v", okRes)
	}

	// 3. Canceled one-shot ping suppresses late CommandOutcome.
	runBatch2 := c.Dispatch(&corev1.Event{
		NowMs: now + 100,
		Event: &corev1.Event_PingMachine{
			PingMachine: &corev1.PingMachineAction{OpId: 3, MachineId: "m-on"},
		},
	})
	var dc2 *corev1.DispatchCommandEffect
	for _, ef := range runBatch2.GetEffects() {
		if d := ef.GetDispatchCommand(); d != nil {
			dc2 = d
		}
	}
	if dc2 == nil {
		t.Fatalf("expected DispatchCommandEffect for op 3")
	}
	cancelBatch := c.Dispatch(&corev1.Event{
		NowMs: now + 110,
		Event: &corev1.Event_CancelPingMachine{
			CancelPingMachine: &corev1.CancelPingMachineAction{OpId: 3},
		},
	})
	var cancelEff *corev1.CancelDispatchEffect
	for _, ef := range cancelBatch.GetEffects() {
		if cd := ef.GetCancelDispatch(); cd != nil {
			cancelEff = cd
		}
	}
	if cancelEff == nil || cancelEff.RequestId != dc2.RequestId {
		t.Fatalf("expected CancelDispatchEffect for %q, got %+v", dc2.RequestId, cancelBatch.GetEffects())
	}
	lateBatch := c.Dispatch(&corev1.Event{
		NowMs: now + 120,
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:         dc2.RequestId,
				AttemptId:         dc2.AttemptId,
				Source:            corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON,
				Signed:            true,
				HashesMatch:       true,
				AttemptBound:      true,
				OuterMatchesInner: true,
				OuterOutcome:      "executed",
				OuterReason:       "ok",
				RawResult:         testrelay.MustRaw(&v1.CommandResult{Outcome: "executed", Reason: "ok", ResultAvailable: true}),
			},
		},
	})
	for _, ef := range lateBatch.GetEffects() {
		if ef.GetPingMachineResult() != nil {
			t.Fatalf("canceled one-shot ping must not emit result effect: %+v", ef)
		}
	}

	// 4. Canceling one of two concurrent identical pings cancels only its own request_id.
	runA := c.Dispatch(&corev1.Event{
		NowMs: now + 200,
		Event: &corev1.Event_PingMachine{
			PingMachine: &corev1.PingMachineAction{OpId: 4, MachineId: "m-on"},
		},
	})
	runB := c.Dispatch(&corev1.Event{
		NowMs: now + 201,
		Event: &corev1.Event_PingMachine{
			PingMachine: &corev1.PingMachineAction{OpId: 5, MachineId: "m-on"},
		},
	})
	var dcA, dcB *corev1.DispatchCommandEffect
	for _, ef := range runA.GetEffects() {
		if d := ef.GetDispatchCommand(); d != nil {
			dcA = d
		}
	}
	for _, ef := range runB.GetEffects() {
		if d := ef.GetDispatchCommand(); d != nil {
			dcB = d
		}
	}
	if dcA == nil || dcB == nil || dcA.RequestId == dcB.RequestId {
		t.Fatalf("expected two distinct dispatches, got %+v and %+v", dcA, dcB)
	}
	cancelA := c.Dispatch(&corev1.Event{
		NowMs: now + 210,
		Event: &corev1.Event_CancelPingMachine{
			CancelPingMachine: &corev1.CancelPingMachineAction{OpId: 4},
		},
	})
	var canceledIDs []string
	for _, ef := range cancelA.GetEffects() {
		if cd := ef.GetCancelDispatch(); cd != nil {
			canceledIDs = append(canceledIDs, cd.RequestId)
		}
	}
	if len(canceledIDs) != 1 || canceledIDs[0] != dcA.RequestId {
		t.Fatalf("expected CancelDispatchEffect only for %q, got %v", dcA.RequestId, canceledIDs)
	}
	doneB := c.Dispatch(&corev1.Event{
		NowMs: now + 220,
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:         dcB.RequestId,
				AttemptId:         dcB.AttemptId,
				Source:            corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON,
				Signed:            true,
				HashesMatch:       true,
				AttemptBound:      true,
				OuterMatchesInner: true,
				OuterOutcome:      "executed",
				OuterReason:       "ok",
				RawResult:         testrelay.MustRaw(&v1.CommandResult{Outcome: "executed", Reason: "ok", ResultAvailable: true}),
			},
		},
	})
	var resB *corev1.PingMachineResultEffect
	for _, ef := range doneB.GetEffects() {
		if r := ef.GetPingMachineResult(); r != nil {
			resB = r
		}
	}
	if resB == nil || !resB.Ok || resB.OpId != 5 {
		t.Fatalf("expected concurrent op 5 to still succeed, got %+v", resB)
	}
}

func TestPingMachineSkipConnectivityPreflight(t *testing.T) {
	c := bootstrapReadyApp(t, []*corev1.DemoMachine{
		testrelay.DemoMachine("m-off", false, 0, &v1.MachineMeta{Hostname: "Box", ProtocolVersion: sync.PeerVersion}),
	}, nil)
	now := int64(1_791_078_061_000)

	// Drop the stream so Connection transitions away from ConnConnected (reconnecting).
	c.Dispatch(&corev1.Event{
		NowMs: now,
		Event: &corev1.Event_StreamClosed{
			StreamClosed: &corev1.StreamClosedEvent{LegId: 1},
		},
	})
	vm, err := c.View(&corev1.Screen{
		Screen: &corev1.Screen_Connection{Connection: &corev1.ConnectionScreenRequest{}},
	})
	if err != nil {
		t.Fatalf("View(Connection): %v", err)
	}
	if got := vm.GetConnection().GetState(); got == corev1.ConnState_CONN_STATE_CONNECTED {
		t.Fatalf("expected non-connected state after StreamClosed, got %v", got)
	}

	// Normal semantic commands fail connectivity preflight while reconnecting with no DispatchCommandEffect.
	blocked := c.Dispatch(&corev1.Event{
		NowMs: now + 10,
		Event: &corev1.Event_ToggleMachineAgent{
			ToggleMachineAgent: &corev1.ToggleMachineAgentEvent{
				MachineId: "m-off",
				Agent:     v1.Agent_AGENT_CODEX,
				Enabled:   true,
			},
		},
	})
	for _, ef := range blocked.GetEffects() {
		if ef.GetDispatchCommand() != nil {
			t.Fatalf("expected preflight to block dispatch, got %+v", ef)
		}
	}

	// Semantic PingMachineAction bypasses connectivity preflight and supports cancellation.
	pinged := c.Dispatch(&corev1.Event{
		NowMs: now + 50,
		Event: &corev1.Event_PingMachine{
			PingMachine: &corev1.PingMachineAction{
				OpId:      10,
				MachineId: "m-off",
			},
		},
	})
	var pingDC *corev1.DispatchCommandEffect
	for _, ef := range pinged.GetEffects() {
		if d := ef.GetDispatchCommand(); d != nil {
			pingDC = d
		}
		if r := ef.GetPingMachineResult(); r != nil {
			t.Fatalf("expected PingMachine not to fail in preflight, got %+v", r)
		}
	}
	if pingDC == nil {
		t.Fatalf("expected DispatchCommandEffect for PingMachine, got %+v", pinged.GetEffects())
	}
	var pingCmd v1.Command
	if err := pingCmd.Unmarshal(pingDC.CommandBytes); err != nil || pingCmd.GetGetSettings() == nil {
		t.Fatalf("expected PingMachine to dispatch GetSettings, err=%v cmd=%+v", err, &pingCmd)
	}
	pingDone := c.Dispatch(&corev1.Event{
		NowMs: now + 60,
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:         pingDC.RequestId,
				AttemptId:         pingDC.AttemptId,
				Source:            corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON,
				Signed:            true,
				HashesMatch:       true,
				AttemptBound:      true,
				OuterMatchesInner: true,
				OuterOutcome:      "executed",
				OuterReason:       "ok",
				RawResult:         testrelay.MustRaw(&v1.CommandResult{Outcome: "executed", Reason: "ok", ResultAvailable: true}),
			},
		},
	})
	var pingOK *corev1.PingMachineResultEffect
	for _, ef := range pingDone.GetEffects() {
		if r := ef.GetPingMachineResult(); r != nil {
			pingOK = r
		}
	}
	if pingOK == nil || !pingOK.Ok || pingOK.OpId != 10 {
		t.Fatalf("expected ping op 10 to succeed, got %+v", pingOK)
	}

	pingCanceled := c.Dispatch(&corev1.Event{
		NowMs: now + 70,
		Event: &corev1.Event_PingMachine{
			PingMachine: &corev1.PingMachineAction{OpId: 11, MachineId: "m-off"},
		},
	})
	var cancelReqID string
	for _, ef := range pingCanceled.GetEffects() {
		if d := ef.GetDispatchCommand(); d != nil {
			cancelReqID = d.RequestId
		}
	}
	if cancelReqID == "" {
		t.Fatalf("expected DispatchCommandEffect for ping op 11")
	}
	cancelBatch := c.Dispatch(&corev1.Event{
		NowMs: now + 75,
		Event: &corev1.Event_CancelPingMachine{
			CancelPingMachine: &corev1.CancelPingMachineAction{OpId: 11},
		},
	})
	var sawCancel bool
	for _, ef := range cancelBatch.GetEffects() {
		if cd := ef.GetCancelDispatch(); cd != nil && cd.RequestId == cancelReqID {
			sawCancel = true
		}
	}
	if !sawCancel {
		t.Fatalf("expected CancelDispatchEffect for canceled ping op 11, got %+v", cancelBatch.GetEffects())
	}
}
