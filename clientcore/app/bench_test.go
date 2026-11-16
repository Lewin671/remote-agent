package app

import (
	"fmt"
	"testing"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/gen/timestamppb"
)

func seedBenchmarkCore(b *testing.B) *Core {
	b.Helper()
	c := New()
	const (
		numMachines = 5
		numProjects = 50
		numSessions = 1000
	)
	nowMs := int64(1791078060000)

	c.Dispatch(&corev1.Event{
		Event: &corev1.Event_ClockUpdated{
			ClockUpdated: &corev1.LocalCalendar{
				NowMs:            nowMs,
				TodayStartMs:     nowMs - 3600_000,
				YesterdayStartMs: nowMs - 90000_000,
				WeekStartMs:      nowMs - 6*86400_000,
				NowYear:          2026,
				UtcOffsetMinutes: 480,
			},
		},
	})
	c.Dispatch(&corev1.Event{
		Event: &corev1.Event_ConnectionChanged{
			ConnectionChanged: &corev1.ConnectionChanged{
				State:             corev1.ConnState_CONN_STATE_CONNECTED,
				LivePushConnected: true,
			},
		},
	})

	machines := make([]*corev1.MachineInput, numMachines)
	for i := 0; i < numMachines; i++ {
		summaries := make([]*v1.ProjectSummary, 0, numProjects/numMachines)
		for p := i; p < numProjects; p += numMachines {
			summaries = append(summaries, &v1.ProjectSummary{
				Path:            fmt.Sprintf("/Users/dev/work/project-%02d", p),
				Ended:           20,
				LatestCreatedMs: nowMs - int64(p*60_000),
			})
		}
		machines[i] = &corev1.MachineInput{
			MachineId: fmt.Sprintf("m-%d", i),
			Online:    i != 4,
			Meta: &v1.MachineMeta{
				Hostname: fmt.Sprintf("workstation-%d", i),
				Home:     "/Users/dev",
				Agents: []*v1.AgentInfo{
					{Agent: v1.Agent_AGENT_CODEX, DefaultMode: v1.ApprovalMode_APPROVAL_MODE_ASK, DefaultModeSource: v1.DefaultModeSource_DEFAULT_MODE_SOURCE_CLI_CONFIG},
					{Agent: v1.Agent_AGENT_CLAUDE, DefaultMode: v1.ApprovalMode_APPROVAL_MODE_ASK, DefaultModeSource: v1.DefaultModeSource_DEFAULT_MODE_SOURCE_CLI_CONFIG},
				},
				Projects: summaries,
			},
		}
	}
	c.Dispatch(&corev1.Event{
		Event: &corev1.Event_MachinesUpdated{
			MachinesUpdated: &corev1.MachinesUpdated{
				Machines: machines,
				Listed:   true,
			},
		},
	})

	sessions := make([]*corev1.SessionInput, numSessions)
	for i := 0; i < numSessions; i++ {
		projIdx := i % numProjects
		machIdx := projIdx % numMachines
		created := (nowMs - int64(i*120_000)) / 1000
		status := v1.SessionStatus_SESSION_STATUS_EXITED
		var pendingApprovals int32
		if i < 5 {
			status = v1.SessionStatus_SESSION_STATUS_RUNNING
			pendingApprovals = 1
		} else if i < 20 {
			status = v1.SessionStatus_SESSION_STATUS_RUNNING
		} else if i < 40 {
			status = v1.SessionStatus_SESSION_STATUS_IDLE
		}
		sessions[i] = &corev1.SessionInput{
			SessionId: fmt.Sprintf("sess-%04d", i),
			MachineId: fmt.Sprintf("m-%d", machIdx),
			UpdatedMs: created * 1000,
			Info: &v1.Session{
				Id:               fmt.Sprintf("sess-%04d", i),
				Cwd:              fmt.Sprintf("/Users/dev/work/project-%02d", projIdx),
				Title:            fmt.Sprintf("Task #%04d refactor module", i),
				Agent:            v1.Agent(1 + (i % 2)),
				Status:           status,
				PendingApprovals: pendingApprovals,
				Preview:          "Running unit tests and checking protocol invariants",
				CreatedAt:        &timestamppb.Timestamp{Seconds: created},
				EndedAt:          &timestamppb.Timestamp{Seconds: created + 30},
			},
		}
	}
	c.Dispatch(&corev1.Event{
		Event: &corev1.Event_SessionsPageReceived{
			SessionsPageReceived: &corev1.SessionsPageResult{
				Sessions:  sessions,
				IsRefresh: true,
			},
		},
	})
	return c
}

func BenchmarkViewHome1000Sessions50Projects_Window50(b *testing.B) {
	c := seedBenchmarkCore(b)
	defer c.Close()
	screen := &corev1.Screen{Screen: &corev1.Screen_Home{Home: &corev1.HomeScreenRequest{EndedLimit: 50}}}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		vm, err := c.View(screen)
		if err != nil || vm.GetHome() == nil {
			b.Fatalf("unexpected view result: %v", err)
		}
	}
}

func BenchmarkViewHome1000Sessions50Projects_Full(b *testing.B) {
	c := seedBenchmarkCore(b)
	defer c.Close()
	screen := &corev1.Screen{Screen: &corev1.Screen_Home{Home: &corev1.HomeScreenRequest{EndedLimit: -1}}}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		vm, err := c.View(screen)
		if err != nil || vm.GetHome() == nil {
			b.Fatalf("unexpected view result: %v", err)
		}
	}
}

func BenchmarkDispatchSessionUpserted1000Sessions50Projects(b *testing.B) {
	c := seedBenchmarkCore(b)
	defer c.Close()
	ev := &corev1.Event{
		Event: &corev1.Event_SessionUpserted{
			SessionUpserted: &corev1.SessionInput{
				SessionId: "sess-0010",
				MachineId: "m-0",
				UpdatedMs: 1791078090000,
				Info: &v1.Session{
					Id:        "sess-0010",
					Cwd:       "/Users/dev/work/project-10",
					Title:     "Updated live task",
					Agent:     v1.Agent_AGENT_CODEX,
					Status:    v1.SessionStatus_SESSION_STATUS_RUNNING,
					Preview:   "Applying patch",
					CreatedAt: &timestamppb.Timestamp{Seconds: 1791078000},
				},
			},
		},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		eff := c.Dispatch(ev)
		if len(eff.Effects) == 0 {
			b.Fatal("expected effects")
		}
	}
}

func BenchmarkDispatchHomeSetScope1000Sessions50Projects(b *testing.B) {
	c := seedBenchmarkCore(b)
	defer c.Close()
	evA := &corev1.Event{Event: &corev1.Event_HomeSetScope{HomeSetScope: &corev1.HomeSetScopeAction{ScopeKey: "/Users/dev/work/project-03"}}}
	evAll := &corev1.Event{Event: &corev1.Event_HomeSetScope{HomeSetScope: &corev1.HomeSetScopeAction{ScopeKey: ""}}}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if i%2 == 0 {
			c.Dispatch(evA)
		} else {
			c.Dispatch(evAll)
		}
	}
}
