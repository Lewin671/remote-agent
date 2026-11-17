package app

import (
	"testing"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/sync"
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
	if len(itemsBefore) != 1 || itemsBefore[0].Id != "start-prompt" || itemsBefore[0].GetUserMessage().GetText() != "Build core session tests" {
		t.Fatalf("expected synthetic start-prompt item before UserMessage, found=%v ui=%+v got=%+v", vmBefore.GetSession().GetHeader().GetFound(), c.m.ensureSessionStore().ensureUI("s-created"), itemsBefore)
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
	if len(tlItems) != 6 {
		t.Fatalf("expected 6 timeline items (user, reasoning, sharedFiles, genImg, asyncQuestion, inputState), got %d", len(tlItems))
	}
	if tlItems[0].GetUserMessage().GetText() != "Look at main.go" {
		t.Fatalf("expected injected File line stripped from UserMessage, got %q", tlItems[0].GetUserMessage().GetText())
	}
	if tlItems[2].GetSharedFiles() == nil || tlItems[3].GetGeneratedImages() == nil || tlItems[4].GetAsyncQuestion() == nil || tlItems[5].GetInputState().GetKind() != corev1.SessionInputNoticeKind_SESSION_INPUT_NOTICE_KIND_FAILED {
		t.Fatalf("expected rich timeline rows present and typed cleanly: %+v", tlItems)
	}
	var _ = lists.DefaultAgent
}
