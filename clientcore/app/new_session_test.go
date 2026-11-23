package app

import (
	"testing"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

func TestNewSessionProjectAddAndLatePrefsReloadModels(t *testing.T) {
	c := New()
	defer c.Close()

	seedOneMachine(c)

	// Adding a project via directory picker for New Task must resolve a concrete non-unspecified agent.
	c.Dispatch(&corev1.Event{
		NowMs: 1050,
		Event: &corev1.Event_ProjectOpenDirPicker{
			ProjectOpenDirPicker: &corev1.DirPickerOpenAction{PickerKey: "new-task", MachineId: "m-1", Path: "/Users/alice", ForNewTask: true},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1060,
		Event: &corev1.Event_ProjectAdd{
			ProjectAdd: &corev1.ProjectAddAction{Path: "/Users/alice", PickerKey: "new-task"},
		},
	})
	vm, err := c.View(&corev1.Screen{
		Screen: &corev1.Screen_NewSession{NewSession: &corev1.NewSessionScreenRequest{ProjectKey: "/Users/alice"}},
	})
	if err != nil {
		t.Fatalf("view after projectAdd: %v", err)
	}
	ns := vm.GetNewSession()
	if ns.GetConfig().GetAgent() != v1.Agent_AGENT_CODEX {
		t.Fatalf("expected AGENT_CODEX after ProjectAdd, got %v", ns.GetConfig().GetAgent())
	}
	if len(ns.GetAvailableAgents()) != 3 {
		t.Fatalf("expected 3 available agents, got %d", len(ns.GetAvailableAgents()))
	}

	// Late AccountPrefsFetched for a clean opened project must apply saved agent + model and fetch its models.
	c.Dispatch(&corev1.Event{
		NowMs: 1070,
		Event: &corev1.Event_NewSessionOpen{
			NewSessionOpen: &corev1.NewSessionOpenAction{ProjectKey: "/Users/alice"},
		},
	})
	effs := c.Dispatch(&corev1.Event{
		NowMs: 1080,
		Event: &corev1.Event_AccountPrefsFetched{
			AccountPrefsFetched: &corev1.AccountPrefsFetchedEvent{
				OpId: 4,
				Prefs: &corev1.PlainAccountPrefs{
					Revision: 2,
					Prefs: &v1.ProjectPrefs{
						Schema: 1,
						Settings: []*v1.ProjectSettings{
							{
								Path:      "/Users/alice",
								MachineId: "m-1",
								Agent:     v1.Agent_AGENT_CLAUDE,
								Agents: []*v1.AgentSettings{
									{Agent: v1.Agent_AGENT_CLAUDE, Model: "claude-sonnet-5-5"},
								},
							},
						},
					},
				},
			},
		},
	})
	var fetchedClaudeModels bool
	for _, eff := range effs.GetEffects() {
		dc := eff.GetDispatchCommand()
		if dc == nil {
			continue
		}
		var cmd v1.Command
		if err := cmd.Unmarshal(dc.CommandBytes); err != nil {
			t.Fatalf("unmarshal command: %v", err)
		}
		if lm := cmd.GetListModels(); lm != nil && lm.Agent == v1.Agent_AGENT_CLAUDE {
			fetchedClaudeModels = true
		}
	}
	if !fetchedClaudeModels {
		t.Fatalf("expected ListModels(AGENT_CLAUDE) dispatch after late AccountPrefsFetched")
	}

	// PlanStartSettings must normalize AGENT_UNSPECIFIED to DefaultAgent.
	op := lists.PlanStartSettings(
		lists.StartConfig{MachineID: "m-1", Cwd: "/Users/alice", Agent: v1.Agent_AGENT_UNSPECIFIED, Model: "gpt-6-luna"},
		nil,
		nil,
	)
	if op == nil || op.SetSettings == nil || *op.SetSettings.Agent != v1.Agent_AGENT_CODEX {
		t.Fatalf("expected PlanStartSettings to normalize agent to CODEX, got %+v", op)
	}
	if len(op.SetSettings.Sets) != 1 || op.SetSettings.Sets[0].Agent != v1.Agent_AGENT_CODEX {
		t.Fatalf("expected PlanStartSettings Agents entry to use CODEX, got %+v", op.SetSettings.Sets)
	}
}

func seedOneMachine(c *Core) {
	// Seed entropy, lifecycle, security, stream, and one multi-agent machine.
	c.Dispatch(&corev1.Event{
		NowMs: 1000,
		Event: &corev1.Event_EntropyProvided{
			EntropyProvided: &corev1.EntropyProvided{
				RandomIds:  []string{"id-1", "id-2", "id-3", "id-4", "id-5", "id-6", "id-7", "id-8", "id-9", "id-10"},
				UnitFloats: []float64{0.5, 0.5, 0.5, 0.5},
			},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1000,
		Event: &corev1.Event_SyncLifecycle{
			SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1010,
		Event: &corev1.Event_SeedDemoWorld{
			SeedDemoWorld: &corev1.SeedDemoWorldEvent{
				Security: &corev1.DemoSecurity{
					IsMember:          true,
					SelfDeviceId:      "dev-1",
					AuthorityDeviceId: "dev-1",
					LeaseEndMs:        99999999,
					TrustedMachineIds: []string{"m-1"},
				},
			},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1020,
		Event: &corev1.Event_TokenResult{
			TokenResult: &corev1.TokenResultEvent{OpId: 2, Token: "tok-1"},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1030,
		Event: &corev1.Event_StreamReady{
			StreamReady: &corev1.StreamReadyEvent{LegId: 1, ServerTimeMs: 1030},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1040,
		Event: &corev1.Event_ListsFetched{
			ListsFetched: &corev1.ListsFetchedEvent{
				OpId: 3,
				Machines: []*corev1.PlainMachine{
					{
						MachineId: "m-1",
						Online:    true,
						Meta: &v1.MachineMeta{
							Hostname:        "Workstation",
							ProtocolVersion: 17,
							Agents: []*v1.AgentInfo{
								{Agent: v1.Agent_AGENT_CODEX, DefaultMode: v1.ApprovalMode_APPROVAL_MODE_ASK},
								{Agent: v1.Agent_AGENT_CLAUDE, DefaultMode: v1.ApprovalMode_APPROVAL_MODE_ASK},
								{Agent: v1.Agent_AGENT_ANTIGRAVITY, DefaultMode: v1.ApprovalMode_APPROVAL_MODE_ASK},
							},
						},
					},
				},
			},
		},
	})

}

func TestStandaloneNewTaskTakesCLIAndModelPicks(t *testing.T) {
	c := New()
	defer c.Close()
	seedOneMachine(c)
	// A saved project lists it; nothing has opened a new task on it by key.
	c.Dispatch(&corev1.Event{NowMs: 1050, Event: &corev1.Event_AccountPrefsFetched{AccountPrefsFetched: &corev1.AccountPrefsFetchedEvent{OpId: 4, Prefs: &corev1.PlainAccountPrefs{Revision: 1, Prefs: &v1.ProjectPrefs{Schema: 1, Settings: []*v1.ProjectSettings{{Path: "/Users/alice", MachineId: "m-1", Agent: v1.Agent_AGENT_CODEX}}}}}}})
	c.Dispatch(&corev1.Event{NowMs: 1060, Event: &corev1.Event_NewSessionOpen{NewSessionOpen: &corev1.NewSessionOpenAction{}}})
	view := func() *corev1.NewSessionViewModel {
		vm, err := c.View(&corev1.Screen{Screen: &corev1.Screen_NewSession{NewSession: &corev1.NewSessionScreenRequest{}}})
		if err != nil {
			t.Fatal(err)
		}
		return vm.GetNewSession()
	}
	if got := view().GetProjectKey(); got != "/Users/alice" {
		t.Fatalf("standalone new task shows project %q", got)
	}
	c.Dispatch(&corev1.Event{NowMs: 1070, Event: &corev1.Event_NewSessionSelectAgent{NewSessionSelectAgent: &corev1.NewSessionSelectAgentAction{Agent: v1.Agent_AGENT_CLAUDE}}})
	if got := view().GetConfig().GetAgent(); got != v1.Agent_AGENT_CLAUDE {
		t.Fatalf("CLI pick did not show: %v", got)
	}
	c.Dispatch(&corev1.Event{NowMs: 1080, Event: &corev1.Event_NewSessionSelectModel{NewSessionSelectModel: &corev1.NewSessionSelectModelAction{Model: "claude-sonnet-5-5"}}})
	if got := view().GetConfig().GetModel(); got != "claude-sonnet-5-5" {
		t.Fatalf("model pick did not show: %q", got)
	}
}

func TestDirPickerKeyedIsolation(t *testing.T) {
	c := New()
	defer c.Close()
	seedOneMachine(c)

	c.Dispatch(&corev1.Event{
		NowMs: 1100,
		Event: &corev1.Event_ProjectOpenDirPicker{
			ProjectOpenDirPicker: &corev1.DirPickerOpenAction{
				PickerKey:  "manage",
				MachineId:  "m-1",
				Path:       "/Users/alice/manage",
				ForNewTask: false,
			},
		},
	})
	schedBatch := c.Dispatch(&corev1.Event{
		NowMs: 1110,
		Event: &corev1.Event_ProjectOpenDirPicker{
			ProjectOpenDirPicker: &corev1.DirPickerOpenAction{
				PickerKey:  "schedule",
				MachineId:  "m-1",
				Path:       "/Users/alice/schedule",
				ForNewTask: true,
			},
		},
	})
	var schedDC *corev1.DispatchCommandEffect
	for _, eff := range schedBatch.Effects {
		if dc := eff.GetDispatchCommand(); dc != nil {
			schedDC = dc
		}
	}
	if schedDC == nil {
		t.Fatalf("expected DispatchCommand for schedule ListDirectories")
	}

	viewPicker := func(key string) *corev1.DirPickerView {
		vm, err := c.View(&corev1.Screen{
			Screen: &corev1.Screen_Project{
				Project: &corev1.ProjectScreenRequest{Manage: true, PickerKey: key},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return vm.GetProject().GetManage().GetDirPicker()
	}

	pManage := viewPicker("manage")
	pSched := viewPicker("schedule")
	pDefault := viewPicker("")
	if !pManage.GetOpen() || pManage.GetCurrentPath() != "/Users/alice/manage" || pManage.GetForNewTask() || pManage.GetPickerKey() != "manage" {
		t.Fatalf("unexpected manage picker: %+v", pManage)
	}
	if !pSched.GetOpen() || pSched.GetCurrentPath() != "/Users/alice/schedule" || !pSched.GetForNewTask() || pSched.GetPickerKey() != "schedule" {
		t.Fatalf("unexpected schedule picker: %+v", pSched)
	}
	if pDefault.GetOpen() {
		t.Fatalf("default picker slot must remain closed while keyed pickers are open")
	}

	// Closing "schedule" must not close or clear "manage".
	c.Dispatch(&corev1.Event{
		NowMs: 1120,
		Event: &corev1.Event_ProjectCloseDirPicker{
			ProjectCloseDirPicker: &corev1.DirPickerCloseAction{PickerKey: "schedule"},
		},
	})
	if viewPicker("schedule").GetOpen() {
		t.Fatalf("expected schedule picker closed")
	}
	if got := viewPicker("manage"); !got.GetOpen() || got.GetCurrentPath() != "/Users/alice/manage" {
		t.Fatalf("closing schedule picker affected manage picker: %+v", got)
	}

	// A late ListDirectories response for the closed "schedule" picker must be dropped without recreating it.
	c.Dispatch(&corev1.Event{
		NowMs: 1130,
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:         schedDC.RequestId,
				AttemptId:         schedDC.AttemptId,
				Source:            corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON,
				AttemptBound:      true,
				Signed:            true,
				HashesMatch:       true,
				OuterMatchesInner: true,
				OuterOutcome:      "executed",
				OuterReason:       "ok",
				Result: &v1.CommandResult{
					Outcome:         "executed",
					Reason:          "ok",
					ResultAvailable: true,
					Kind: &v1.CommandResult_ListDirectories{
						ListDirectories: &v1.ListDirectoriesResponse{
							Path:   "/Users/alice/schedule",
							Parent: "/Users/alice",
							Dirs:   []string{"late-dir"},
						},
					},
				},
			},
		},
	})
	if _, stillPending := c.m.pendingCmdMeta[schedDC.RequestId]; stillPending {
		t.Fatalf("expected late ListDirectories command outcome to settle and consume pendingCmdMeta")
	}
	if c.m.state.DirPickers["schedule"] != nil {
		t.Fatalf("late ListDirectories response recreated closed picker: %+v", c.m.state.DirPickers["schedule"])
	}
	if p := viewPicker("schedule"); p.GetOpen() || len(p.GetDirs()) != 0 {
		t.Fatalf("closed schedule picker should stay empty after late ListDirectories, got %+v", p)
	}

	// Empty picker_key actions must be ignored and never allocate an implicit "" slot.
	emptyOpen := c.Dispatch(&corev1.Event{
		NowMs: 1140,
		Event: &corev1.Event_ProjectOpenDirPicker{
			ProjectOpenDirPicker: &corev1.DirPickerOpenAction{MachineId: "m-1", Path: "/Users/alice/bad", ForNewTask: true},
		},
	})
	for _, eff := range emptyOpen.GetEffects() {
		if eff.GetDispatchCommand() != nil {
			t.Fatalf("empty picker_key open must not dispatch ListDirectories: %+v", eff)
		}
	}
	c.Dispatch(&corev1.Event{
		NowMs: 1145,
		Event: &corev1.Event_ProjectDirNavigate{
			ProjectDirNavigate: &corev1.DirNavigateAction{MachineId: "m-1", Path: "/Users/alice/bad2"},
		},
	})
	if c.m.state.DirPickers[""] != nil {
		t.Fatalf("empty picker_key allocated an implicit default slot: %+v", c.m.state.DirPickers[""])
	}
	if p := viewPicker(""); p.GetOpen() || p.GetCurrentPath() != "" {
		t.Fatalf("empty picker_key view must stay closed/empty, got %+v", p)
	}
}

func TestNewSessionCreateSessionDiskFullLifecycle(t *testing.T) {
	c := New()
	defer c.Close()
	seedOneMachine(c)

	const projKey = "/Users/alice"
	c.Dispatch(&corev1.Event{
		NowMs: 1050,
		Event: &corev1.Event_AccountPrefsFetched{
			AccountPrefsFetched: &corev1.AccountPrefsFetchedEvent{
				OpId: 4,
				Prefs: &corev1.PlainAccountPrefs{
					Revision: 1,
					Prefs: &v1.ProjectPrefs{
						Schema:   1,
						Settings: []*v1.ProjectSettings{{Path: projKey, MachineId: "m-1", Agent: v1.Agent_AGENT_CODEX}},
					},
				},
			},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1060,
		Event: &corev1.Event_NewSessionOpen{NewSessionOpen: &corev1.NewSessionOpenAction{ProjectKey: projKey}},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1070,
		Event: &corev1.Event_NewSessionSetPrompt{NewSessionSetPrompt: &corev1.NewSessionSetPromptAction{Prompt: "hello"}},
	})

	viewNS := func() *corev1.NewSessionViewModel {
		vm, err := c.View(&corev1.Screen{Screen: &corev1.Screen_NewSession{NewSession: &corev1.NewSessionScreenRequest{ProjectKey: projKey}}})
		if err != nil {
			t.Fatal(err)
		}
		return vm.GetNewSession()
	}

	submitAndFindCmd := func(nowMs int64) *corev1.DispatchCommandEffect {
		batch := c.Dispatch(&corev1.Event{
			NowMs: nowMs,
			Event: &corev1.Event_NewSessionSubmit{NewSessionSubmit: &corev1.NewSessionSubmitAction{ProjectKey: projKey}},
		})
		for _, eff := range batch.GetEffects() {
			if dc := eff.GetDispatchCommand(); dc != nil {
				return dc
			}
		}
		t.Fatalf("expected DispatchCommandEffect on NewSessionSubmit")
		return nil
	}

	settleErr := func(nowMs int64, dc *corev1.DispatchCommandEffect, code, msg string) {
		c.Dispatch(&corev1.Event{
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
					OuterReason:       code,
					Result: &v1.CommandResult{
						Outcome:         "executed",
						Reason:          code,
						ResultAvailable: true,
						ErrorCode:       code,
						ErrorMessage:    msg,
					},
				},
			},
		})
	}

	// 1. Failing createSession with errorCode=="disk_full" sets DiskFull and emits NOTICE_KIND_DISK_FULL.
	dc1 := submitAndFindCmd(1080)
	settleErr(1090, dc1, "disk_full", "no space left on device")

	ns := viewNS()
	if !ns.GetDiskFull() || ns.GetCanStart() {
		t.Fatalf("expected DiskFull=true and CanStart=false after disk_full failure, got diskFull=%v canStart=%v", ns.GetDiskFull(), ns.GetCanStart())
	}
	var hasDiskFullNotice bool
	for _, n := range ns.GetNotices() {
		if n.GetKind() == corev1.NoticeKind_NOTICE_KIND_DISK_FULL && n.GetTargetMachineId() == "m-1" {
			hasDiskFullNotice = true
		}
	}
	if !hasDiskFullNotice {
		t.Fatalf("expected NOTICE_KIND_DISK_FULL targeting m-1, got notices=%+v", ns.GetNotices())
	}

	// 2. Retrying clears DiskFull while in flight; a non-disk_full failure stays DiskFull=false (ordinary FAILED notice).
	dc2 := submitAndFindCmd(1100)
	if viewNS().GetDiskFull() {
		t.Fatalf("expected DiskFull cleared on retry submit")
	}
	settleErr(1110, dc2, "storage_exhausted", "quota exceeded")
	ns2 := viewNS()
	if ns2.GetDiskFull() {
		t.Fatalf("expected DiskFull=false after storage_exhausted failure")
	}
	for _, n := range ns2.GetNotices() {
		if n.GetKind() == corev1.NoticeKind_NOTICE_KIND_DISK_FULL {
			t.Fatalf("unexpected NOTICE_KIND_DISK_FULL on storage_exhausted: %+v", n)
		}
	}

	// 3. Re-trigger disk_full and verify switching machine or abandoning clears DiskFull.
	dc3 := submitAndFindCmd(1120)
	settleErr(1130, dc3, "disk_full", "no space left on device")
	if !viewNS().GetDiskFull() {
		t.Fatalf("expected DiskFull=true after second disk_full failure")
	}
	c.Dispatch(&corev1.Event{
		NowMs: 1140,
		Event: &corev1.Event_NewSessionAbandonStart{NewSessionAbandonStart: &corev1.NewSessionProjectAction{ProjectKey: projKey}},
	})
	if viewNS().GetDiskFull() {
		t.Fatalf("expected DiskFull cleared on NewSessionAbandonStart")
	}

	dc4 := submitAndFindCmd(1150)
	settleErr(1160, dc4, "disk_full", "no space left on device")
	if !viewNS().GetDiskFull() {
		t.Fatalf("expected DiskFull=true after third disk_full failure")
	}
	c.Dispatch(&corev1.Event{
		NowMs: 1170,
		Event: &corev1.Event_NewSessionSelectMachine{NewSessionSelectMachine: &corev1.NewSessionSelectMachineAction{MachineId: "m-1"}},
	})
	if viewNS().GetDiskFull() {
		t.Fatalf("expected DiskFull cleared on NewSessionSelectMachine")
	}
}
