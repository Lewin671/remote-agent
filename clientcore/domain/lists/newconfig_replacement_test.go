package lists

import (
	"testing"

	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

func TestResolveProjectStartRemapsReplacedMachine(t *testing.T) {
	w := &World{
		MachinesListed: true,
		Machines: []*Machine{
			{ID: "m-new", Online: true, Meta: &v1.MachineMeta{Hostname: "Workstation", Agents: []*v1.AgentInfo{
				{Agent: v1.Agent_AGENT_CODEX},
				{Agent: v1.Agent_AGENT_CLAUDE},
			}}},
		},
		Prefs: &v1.ProjectPrefs{
			Schema: 1,
			Settings: []*v1.ProjectSettings{
				{
					Path:      "/work/app",
					MachineId: "m-old",
					Agent:     v1.Agent_AGENT_CLAUDE,
					Agents: []*v1.AgentSettings{
						{Agent: v1.Agent_AGENT_CLAUDE, Model: "claude-opus", Effort: "high"},
					},
				},
			},
		},
		MachineReplacements: map[string]string{
			"m-old": "m-mid",
			"m-mid": "m-new",
		},
	}

	res := ResolveProjectStart(w, "/work/app", "", v1.Agent_AGENT_UNSPECIFIED, nil, false)
	if !res.Found {
		t.Fatal("expected ResolveProjectStart to find replaced machine")
	}
	if res.Config.MachineID != "m-new" {
		t.Fatalf("expected remapped machine m-new, got %q", res.Config.MachineID)
	}
	if res.Config.Agent != v1.Agent_AGENT_CLAUDE || res.Config.Model != "claude-opus" || res.Config.Effort != "high" {
		t.Fatalf("expected saved agent settings preserved, got %+v", res.Config)
	}

	// Project only present in Prefs.Settings should remain visible after replacement.
	projRes := BuildProjects(w, "", nil, "", nil, nil)
	if len(projRes.All) != 1 || projRes.All[0].Key != "/work/app" {
		t.Fatalf("expected replaced project in BuildProjects, got %+v", projRes.All)
	}
	if len(projRes.All[0].MachineIDs) != 1 || projRes.All[0].MachineIDs[0] != "m-new" {
		t.Fatalf("expected project machine IDs [m-new], got %v", projRes.All[0].MachineIDs)
	}
}

func TestResolveProjectStartFallsBackWhenSavedMachineInvalidOrArchived(t *testing.T) {
	t.Run("missing saved machine falls back to single online machine", func(t *testing.T) {
		w := &World{
			MachinesListed: true,
			Machines: []*Machine{
				{ID: "m-fresh", Online: true, Meta: &v1.MachineMeta{Hostname: "DevBox", Agents: []*v1.AgentInfo{{Agent: v1.Agent_AGENT_CODEX}}}},
			},
			Prefs: &v1.ProjectPrefs{
				Schema: 1,
				Settings: []*v1.ProjectSettings{
					{Path: "/work/old-proj", MachineId: "m-deleted", Agent: v1.Agent_AGENT_CODEX},
				},
			},
		}
		res := ResolveProjectStart(w, "/work/old-proj", "", v1.Agent_AGENT_UNSPECIFIED, nil, false)
		if !res.Found || res.Config.MachineID != "m-fresh" {
			t.Fatalf("expected fallback to m-fresh, got %+v", res)
		}
	})

	t.Run("missing saved machine prefers reachable machine over offline", func(t *testing.T) {
		w := &World{
			MachinesListed: true,
			Machines: []*Machine{
				{ID: "m-a-offline", Online: false, Meta: &v1.MachineMeta{Hostname: "BoxA"}},
				{ID: "m-b-online", Online: true, Meta: &v1.MachineMeta{Hostname: "BoxB"}},
			},
			Prefs: &v1.ProjectPrefs{
				Schema: 1,
				Settings: []*v1.ProjectSettings{
					{Path: "/work/old-proj", MachineId: "m-gone"},
				},
			},
		}
		res := ResolveProjectStart(w, "/work/old-proj", "", v1.Agent_AGENT_UNSPECIFIED, nil, false)
		if !res.Found || res.Config.MachineID != "m-b-online" {
			t.Fatalf("expected reachable fallback m-b-online, got %+v", res)
		}
	})

	t.Run("archived saved machine falls back to active machine", func(t *testing.T) {
		w := &World{
			MachinesListed: true,
			Machines: []*Machine{
				{ID: "m-archived", Online: false, Archived: true, Meta: &v1.MachineMeta{Hostname: "OldMac"}},
				{ID: "m-active", Online: true, Meta: &v1.MachineMeta{Hostname: "NewMac"}},
			},
			Prefs: &v1.ProjectPrefs{
				Schema: 1,
				Settings: []*v1.ProjectSettings{
					{Path: "/work/old-proj", MachineId: "m-archived"},
				},
			},
		}
		res := ResolveProjectStart(w, "/work/old-proj", "", v1.Agent_AGENT_UNSPECIFIED, nil, false)
		if !res.Found || res.Config.MachineID != "m-active" {
			t.Fatalf("expected fallback to active m-active, got %+v", res)
		}
	})

	t.Run("cycle in replacement map terminates cleanly", func(t *testing.T) {
		w := &World{
			MachineReplacements: map[string]string{"a": "b", "b": "a"},
		}
		if got := ResolveMachineReplacement(w, "a"); got != "b" {
			t.Fatalf("expected cycle-safe result b, got %q", got)
		}
	})
}

func TestApplySettingsUpdateMachinePreservesAgents(t *testing.T) {
	stored := &v1.ProjectSettings{
		Path:      "/work/app",
		MachineId: "m-old",
		Agent:     v1.Agent_AGENT_CLAUDE,
		Agents: []*v1.AgentSettings{
			{Agent: v1.Agent_AGENT_CLAUDE, Model: "claude-opus", Effort: "high"},
		},
	}
	ag := v1.Agent_AGENT_CLAUDE
	next := ApplySettings(stored, &PrefsSetSettingsOp{
		Path:          "/work/app",
		MachineID:     "m-new",
		UpdateMachine: true,
		Agent:         &ag,
		KeepExisting:  true,
	}, 12345)
	if next.MachineId != "m-new" {
		t.Fatalf("expected updated MachineId m-new, got %q", next.MachineId)
	}
	if len(next.Agents) != 1 || next.Agents[0].Model != "claude-opus" || next.Agents[0].Effort != "high" {
		t.Fatalf("expected existing agent settings preserved, got %+v", next.Agents)
	}
}
