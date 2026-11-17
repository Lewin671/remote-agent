package app

import (
	"fmt"
	"testing"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/gen/timestamppb"
	"github.com/Lewin671/remote-agent/clientcore/sync"
)

func seedBenchmarkCore(b *testing.B, numMachines, numProjects, numSessions int) (*Core, uint64) {
	b.Helper()
	c := New()
	nowMs := int64(1_776_100_000_000)

	c.Dispatch(&corev1.Event{
		NowMs: nowMs,
		Event: &corev1.Event_ClockUpdated{
			ClockUpdated: &corev1.LocalCalendar{
				NowMs:            nowMs,
				TodayStartMs:     nowMs - 4*3_600_000,
				YesterdayStartMs: nowMs - 28*3_600_000,
				WeekStartMs:      nowMs - 4*24*3_600_000,
				NowYear:          2026,
				Months: []*corev1.MonthStart{
					{Year: 2026, Month: 4, StartMs: nowMs - 14*24*3_600_000},
				},
			},
		},
	})

	startBatch := c.Dispatch(&corev1.Event{
		NowMs:  nowMs,
		MonoMs: 1_000,
		Event: &corev1.Event_SyncLifecycle{
			SyncLifecycle: &corev1.SyncLifecycleAction{
				Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START,
			},
		},
	})
	var tokOp uint64
	for _, e := range startBatch.Effects {
		if et := e.GetEnsureToken(); et != nil {
			tokOp = et.OpId
		}
	}
	tokBatch := c.Dispatch(&corev1.Event{
		NowMs: nowMs + 10,
		Event: &corev1.Event_TokenResult{
			TokenResult: &corev1.TokenResultEvent{OpId: tokOp, Token: "bench-tok"},
		},
	})
	var legID uint64
	for _, e := range tokBatch.Effects {
		if os := e.GetOpenStream(); os != nil {
			legID = os.LegId
		}
	}
	readyBatch := c.Dispatch(&corev1.Event{
		NowMs: nowMs + 20,
		Event: &corev1.Event_StreamReady{
			StreamReady: &corev1.StreamReadyEvent{LegId: legID, ServerTimeMs: nowMs + 20},
		},
	})
	var listsOp uint64
	for _, e := range readyBatch.Effects {
		if rpc := e.GetRpc(); rpc != nil && rpc.GetFetchLists() != nil {
			listsOp = rpc.OpId
		}
	}

	machines := make([]*corev1.PlainMachine, 0, numMachines)
	for i := 0; i < numMachines; i++ {
		summaries := make([]*v1.ProjectSummary, 0, max(1, numProjects/numMachines))
		for p := i; p < numProjects; p += numMachines {
			summaries = append(summaries, &v1.ProjectSummary{
				Path:  fmt.Sprintf("/Users/dev/work/proj-%02d", p),
				Ended: uint32(numSessions / max(1, numProjects)),
			})
		}
		machines = append(machines, &corev1.PlainMachine{
			MachineId: fmt.Sprintf("m-%d", i),
			Online:    i%2 == 0,
			Meta: &v1.MachineMeta{
				Hostname:        fmt.Sprintf("host-%d.local", i),
				ProtocolVersion: sync.ProtocolVersion,
				Agents: []*v1.AgentInfo{
					{Agent: v1.Agent_AGENT_CODEX, DefaultMode: v1.ApprovalMode_APPROVAL_MODE_ASK},
				},
				Projects: summaries,
			},
		})
	}

	sessions := make([]*corev1.PlainSession, 0, numSessions)
	for i := 0; i < numSessions; i++ {
		mid := fmt.Sprintf("m-%d", i%numMachines)
		proj := fmt.Sprintf("/Users/dev/work/proj-%02d", i%numProjects)
		updated := nowMs - int64(i)*60_000
		status := v1.SessionStatus_SESSION_STATUS_EXITED
		if i < 6 {
			status = v1.SessionStatus_SESSION_STATUS_RUNNING
		} else if i < 12 {
			status = v1.SessionStatus_SESSION_STATUS_IDLE
		}
		var endedAt *timestamppb.Timestamp
		if status == v1.SessionStatus_SESSION_STATUS_EXITED {
			endedAt = &timestamppb.Timestamp{Seconds: updated / 1000}
		}
		sessions = append(sessions, &corev1.PlainSession{
			SessionId:     fmt.Sprintf("s-%04d", i),
			MachineId:     mid,
			LastSeq:       uint64(20 + i),
			UpdatedMs:     updated,
			ListedEndedMs: updated,
			Meta: &v1.Session{
				Id:        fmt.Sprintf("s-%04d", i),
				Cwd:       proj,
				Agent:     v1.Agent_AGENT_CODEX,
				Status:    status,
				Title:     fmt.Sprintf("Session %d", i),
				Preview:   "Running unit tests and checking protocol invariants",
				CreatedAt: &timestamppb.Timestamp{Seconds: (updated - 300_000) / 1000},
				EndedAt:   endedAt,
			},
		})
	}

	c.Dispatch(&corev1.Event{
		NowMs: nowMs + 30,
		Event: &corev1.Event_ListsFetched{
			ListsFetched: &corev1.ListsFetchedEvent{
				OpId:     listsOp,
				Machines: machines,
				Sessions: sessions,
			},
		},
	})

	return c, legID
}

func BenchmarkViewHome1000Sessions(b *testing.B) {
	c, _ := seedBenchmarkCore(b, 4, 20, 1000)
	defer c.Close()

	req := &corev1.Screen{
		Screen: &corev1.Screen_Home{
			Home: &corev1.HomeScreenRequest{EndedLimit: 50},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		vm, err := c.View(req)
		if err != nil {
			b.Fatal(err)
		}
		if vm.GetHome().GetTimelineTotal() == 0 {
			b.Fatal("expected timeline items")
		}
	}
}

func BenchmarkDispatchSessionPush1000Sessions(b *testing.B) {
	c, legID := seedBenchmarkCore(b, 4, 20, 1000)
	defer c.Close()

	baseNow := int64(1_776_100_000_000)
	ev := &corev1.Event{
		NowMs: baseNow + 100,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: legID,
				Frame: &corev1.StreamFrameEvent_SessionUpdated{
					SessionUpdated: &corev1.PlainSession{
						SessionId: "s-0001",
						MachineId: "m-1",
						LastSeq:   500,
						UpdatedMs: baseNow + 100,
						Meta: &v1.Session{
							Id:      "s-0001",
							Cwd:     "/Users/dev/work/proj-01",
							Agent:   v1.Agent_AGENT_CODEX,
							Status:  v1.SessionStatus_SESSION_STATUS_RUNNING,
							Title:   "Session 1",
							Preview: "Applying patch",
						},
					},
				},
			},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ev.NowMs = baseNow + int64(i)
		ev.GetStreamFrame().GetSessionUpdated().UpdatedMs = baseNow + int64(i)
		ev.GetStreamFrame().GetSessionUpdated().LastSeq = uint64(500 + i)
		batch := c.Dispatch(ev)
		if len(batch.Effects) == 0 {
			b.Fatal("expected render effect")
		}
	}
}

func BenchmarkMarshalViewHome1000Sessions(b *testing.B) {
	c, _ := seedBenchmarkCore(b, 4, 20, 1000)
	defer c.Close()

	vm, err := c.View(&corev1.Screen{
		Screen: &corev1.Screen_Home{
			Home: &corev1.HomeScreenRequest{EndedLimit: 50},
		},
	})
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		raw, err := vm.Marshal()
		if err != nil {
			b.Fatal(err)
		}
		if len(raw) == 0 {
			b.Fatal("expected non-empty marshaled ViewModel")
		}
	}
}

func BenchmarkMarshalDispatchSessionPush1000Sessions(b *testing.B) {
	c, legID := seedBenchmarkCore(b, 4, 20, 1000)
	defer c.Close()

	baseNow := int64(1_776_100_000_000)
	ev := &corev1.Event{
		NowMs: baseNow + 100,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: legID,
				Frame: &corev1.StreamFrameEvent_SessionUpdated{
					SessionUpdated: &corev1.PlainSession{
						SessionId: "s-0001",
						MachineId: "m-1",
						LastSeq:   500,
						UpdatedMs: baseNow + 100,
						Meta: &v1.Session{
							Id:      "s-0001",
							Cwd:     "/Users/dev/work/proj-01",
							Agent:   v1.Agent_AGENT_CODEX,
							Status:  v1.SessionStatus_SESSION_STATUS_RUNNING,
							Title:   "Session 1",
							Preview: "Applying patch",
						},
					},
				},
			},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ev.NowMs = baseNow + int64(i)
		ev.GetStreamFrame().GetSessionUpdated().UpdatedMs = baseNow + int64(i)
		ev.GetStreamFrame().GetSessionUpdated().LastSeq = uint64(500 + i)
		evBytes, err := ev.Marshal()
		if err != nil {
			b.Fatal(err)
		}
		var dec corev1.Event
		if err := dec.Unmarshal(evBytes); err != nil {
			b.Fatal(err)
		}
		batch := c.Dispatch(&dec)
		out, err := batch.Marshal()
		if err != nil {
			b.Fatal(err)
		}
		if len(out) == 0 {
			b.Fatal("expected non-empty EffectBatch")
		}
	}
}
