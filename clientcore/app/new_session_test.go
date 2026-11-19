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
			ProjectOpenDirPicker: &corev1.DirPickerOpenAction{MachineId: "m-1", Path: "/Users/alice", ForNewTask: true},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1060,
		Event: &corev1.Event_ProjectAdd{
			ProjectAdd: &corev1.ProjectAddAction{Path: "/Users/alice"},
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
				OpId: 5,
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
		Event: &corev1.Event_SecuritySnapshot{
			SecuritySnapshot: &corev1.SecuritySnapshotEvent{
				OpId:              3,
				IsMember:          true,
				SelfDeviceId:      "dev-1",
				AuthorityDeviceId: "dev-1",
				LeaseEndMs:        99999999,
				TrustedMachineIds: []string{"m-1"},
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
				OpId: 4,
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
	c.Dispatch(&corev1.Event{NowMs: 1050, Event: &corev1.Event_AccountPrefsFetched{AccountPrefsFetched: &corev1.AccountPrefsFetchedEvent{OpId: 5, Prefs: &corev1.PlainAccountPrefs{Revision: 1, Prefs: &v1.ProjectPrefs{Schema: 1, Settings: []*v1.ProjectSettings{{Path: "/Users/alice", MachineId: "m-1", Agent: v1.Agent_AGENT_CODEX}}}}}}})
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
