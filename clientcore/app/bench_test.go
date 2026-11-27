package app

import (
	"fmt"
	"testing"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/gen/timestamppb"
	"github.com/Lewin671/remote-agent/clientcore/internal/test/testrelay"
	"github.com/Lewin671/remote-agent/clientcore/sync"
	stdcorev1 "github.com/Lewin671/remote-agent/gen/remoteagent/core/v1"
	stdv1 "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

func seedBenchmarkCore(tb testing.TB, numMachines, numProjects, numSessions int) (*Core, *testrelay.Fixture, uint64) {
	tb.Helper()
	c := New()
	nowMs := int64(1_776_100_000_000)
	fx := testrelay.New(tb, nowMs)

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

	fx.Bootstrap(func(ev *corev1.Event) []*corev1.Effect { return c.Dispatch(ev).GetEffects() }, nowMs)
	c.m.sync.SecurityEngine().SetAuthToken("tok-bench", nowMs+7*86_400_000)

	startBatch := c.Dispatch(&corev1.Event{
		NowMs:  nowMs,
		MonoMs: 1_000,
		Event: &corev1.Event_SyncLifecycle{
			SyncLifecycle: &corev1.SyncLifecycleAction{
				Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START,
			},
		},
	})
	legID := testrelay.FindOpenStreamLeg(startBatch.Effects)
	readyBatch := c.Dispatch(&corev1.Event{
		NowMs: nowMs + 20,
		Event: &corev1.Event_StreamReady{
			StreamReady: &corev1.StreamReadyEvent{LegId: legID, ServerTimeMs: nowMs + 20},
		},
	})
	ops := testrelay.MustFindRefreshOps(tb, readyBatch.Effects)

	if numMachines > len(fx.Machines) {
		numMachines = len(fx.Machines)
	}
	machines := make([]*v1.Machine, 0, numMachines)
	for i := 0; i < numMachines; i++ {
		summaries := make([]*v1.ProjectSummary, 0, max(1, numProjects/numMachines))
		for p := i; p < numProjects; p += numMachines {
			summaries = append(summaries, &v1.ProjectSummary{
				Path:  fmt.Sprintf("/Users/dev/work/proj-%02d", p),
				Ended: uint32(numSessions / max(1, numProjects)),
			})
		}
		machines = append(machines, fx.MachineWire(
			fx.Machines[i].ID,
			i%2 == 0,
			0,
			&v1.MachineMeta{
				Hostname:        fmt.Sprintf("host-%d.local", i),
				ProtocolVersion: sync.PeerVersion,
				Agents: []*v1.AgentInfo{
					{Agent: v1.Agent_AGENT_CODEX, DefaultMode: v1.ApprovalMode_APPROVAL_MODE_ASK},
				},
				Projects: summaries,
			},
		))
	}

	sessions := make([]*v1.RemoteSession, 0, numSessions)
	for i := 0; i < numSessions; i++ {
		mid := fx.Machines[i%numMachines].ID
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
		sessions = append(sessions, fx.SessionWire(
			fmt.Sprintf("s-%04d", i),
			mid,
			uint64(20+i),
			updated,
			updated,
			&v1.Session{
				Id:        fmt.Sprintf("s-%04d", i),
				Cwd:       proj,
				Agent:     v1.Agent_AGENT_CODEX,
				Status:    status,
				Title:     fmt.Sprintf("Session %d", i),
				Preview:   "Running unit tests and checking protocol invariants",
				CreatedAt: &timestamppb.Timestamp{Seconds: (updated - 300_000) / 1000},
				EndedAt:   endedAt,
			},
			nil,
		))
	}

	c.Dispatch(&corev1.Event{
		NowMs: nowMs + 30,
		Event: &corev1.Event_RelayRpcResult{RelayRpcResult: &corev1.RelayRpcResultEvent{
			OpId: ops.Machines, ResponseBytes: testrelay.MarshalListMachinesResp(machines),
		}},
	})
	c.Dispatch(&corev1.Event{
		NowMs: nowMs + 30,
		Event: &corev1.Event_RelayRpcResult{RelayRpcResult: &corev1.RelayRpcResultEvent{
			OpId: ops.Sessions, ResponseBytes: testrelay.MarshalListSessionsResp(sessions, nil),
		}},
	})
	c.Dispatch(&corev1.Event{
		NowMs: nowMs + 30,
		Event: &corev1.Event_RelayRpcResult{RelayRpcResult: &corev1.RelayRpcResultEvent{
			OpId: ops.Devices, ResponseBytes: testrelay.MarshalListDevicesResp(nil),
		}},
	})

	return c, fx, legID
}

func BenchmarkViewHome1000Sessions(b *testing.B) {
	c, _, _ := seedBenchmarkCore(b, 4, 20, 1000)
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
	c, fx, legID := seedBenchmarkCore(b, 4, 20, 1000)
	defer c.Close()

	baseNow := int64(1_776_100_000_000)
	sessMeta := &v1.Session{
		Id:      "s-0001",
		Cwd:     "/Users/dev/work/proj-01",
		Agent:   v1.Agent_AGENT_CODEX,
		Status:  v1.SessionStatus_SESSION_STATUS_RUNNING,
		Title:   "Session 1",
		Preview: "Applying patch",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ev := &corev1.Event{
			NowMs: baseNow + int64(i),
			Event: testrelay.SessionUpdatedFrame(legID, fx.SessionWire("s-0001", fx.M2, uint64(500+i), baseNow+int64(i), 0, sessMeta, nil)),
		}
		batch := c.Dispatch(ev)
		if len(batch.Effects) == 0 {
			b.Fatal("expected render effect")
		}
	}
}

func BenchmarkMarshalViewHome1000Sessions(b *testing.B) {
	c, _, _ := seedBenchmarkCore(b, 4, 20, 1000)
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
	c, fx, legID := seedBenchmarkCore(b, 4, 20, 1000)
	defer c.Close()

	baseNow := int64(1_776_100_000_000)
	sessMeta := &v1.Session{
		Id:      "s-0001",
		Cwd:     "/Users/dev/work/proj-01",
		Agent:   v1.Agent_AGENT_CODEX,
		Status:  v1.SessionStatus_SESSION_STATUS_RUNNING,
		Title:   "Session 1",
		Preview: "Applying patch",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sfBytes := testrelay.MarshalStd(&stdv1.ServerFrame{
			Kind: &stdv1.ServerFrame_SessionUpdated{
				SessionUpdated: &stdv1.SessionUpdated{
					Session: testrelay.ToStdSession(fx.SessionWire("s-0001", fx.M2, uint64(500+i), baseNow+int64(i), 0, sessMeta, nil)),
				},
			},
		})
		ev := &stdcorev1.Event{
			NowMs: baseNow + int64(i),
			Event: &stdcorev1.Event_StreamRawFrame{
				StreamRawFrame: &stdcorev1.StreamRawFrameEvent{
					LegId:          legID,
					RawServerFrame: sfBytes,
				},
			},
		}
		evBytes, err := proto.Marshal(ev)
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

func TestSessionStreamingDoesNotTriggerHomeAggregation(t *testing.T) {
	c, fx, legID := seedBenchmarkCore(t, 4, 10, 200)
	defer c.Close()

	baseNow := int64(1_776_100_000_000)

	// Watch s-0001 and stream 100 text deltas + 100 SessionUpdated pushes with no pending approvals/questions.
	c.Dispatch(&corev1.Event{
		NowMs: baseNow + 100,
		Event: &corev1.Event_SessionWatch{
			SessionWatch: &corev1.SessionWatchEvent{SessionId: "s-0001", Watched: true},
		},
	})
	beforeCount := c.m.homeFocusReconcileCount
	for i := 0; i < 100; i++ {
		seq := uint64(100 + i)
		c.Dispatch(&corev1.Event{
			NowMs: baseNow + 200 + int64(i),
			Event: testrelay.EventsAppendedFrame(legID, "s-0001", fx.StoredEventWire("s-0001", seq, &v1.Event{
				Seq: seq,
				Kind: &v1.Event_AgentMessage{
					AgentMessage: &v1.AgentMessage{Text: "chunk "},
				},
			})),
		})
		c.Dispatch(&corev1.Event{
			NowMs: baseNow + 300 + int64(i),
			Event: testrelay.SessionUpdatedFrame(legID, fx.SessionWire("s-0001", fx.M2, seq, baseNow+300+int64(i), 0, &v1.Session{
				Id:      "s-0001",
				Cwd:     "/Users/dev/work/proj-01",
				Agent:   v1.Agent_AGENT_CODEX,
				Status:  v1.SessionStatus_SESSION_STATUS_RUNNING,
				Title:   "Session 1",
				Preview: "Streaming output",
			}, nil)),
		})
	}
	if c.m.homeFocusReconcileCount != beforeCount {
		t.Fatalf("streaming session events with no pending attention triggered Home aggregation %d times (want 0)",
			c.m.homeFocusReconcileCount-beforeCount)
	}

	// Now introduce an approval on s-0002 -> MUST trigger Home aggregation once!
	c.Dispatch(&corev1.Event{
		NowMs: baseNow + 1000,
		Event: testrelay.SessionUpdatedFrame(legID, fx.SessionWire("s-0002", fx.M3, 200, baseNow+1000, 0, &v1.Session{
			Id:     "s-0002",
			Cwd:    "/Users/dev/work/proj-02",
			Agent:  v1.Agent_AGENT_CODEX,
			Status: v1.SessionStatus_SESSION_STATUS_RUNNING,
			Title:  "Session 2",
		}, &v1.LiveSnapshot{
			Actions: []*v1.Event{
				{
					Seq: 200,
					Kind: &v1.Event_ApprovalRequested{
						ApprovalRequested: &v1.ApprovalRequested{ApprovalId: "ap-bg", Command: "ls"},
					},
				},
			},
		})),
	})
	if c.m.homeFocusReconcileCount != beforeCount+1 || c.m.state.HomeLastTopItemID != "ap-bg" {
		t.Fatalf("expected 1 Home aggregation on new approval (count=%d->%d, top=%q)",
			beforeCount, c.m.homeFocusReconcileCount, c.m.state.HomeLastTopItemID)
	}

	// Continue streaming deltas on unrelated s-0001 while s-0002's approval sits pending -> 0 additional aggregations!
	afterApCount := c.m.homeFocusReconcileCount
	for i := 0; i < 50; i++ {
		seq := uint64(300 + i)
		c.Dispatch(&corev1.Event{
			NowMs: baseNow + 2000 + int64(i),
			Event: testrelay.SessionUpdatedFrame(legID, fx.SessionWire("s-0001", fx.M2, seq, baseNow+2000+int64(i), 0, &v1.Session{
				Id:      "s-0001",
				Cwd:     "/Users/dev/work/proj-01",
				Agent:   v1.Agent_AGENT_CODEX,
				Status:  v1.SessionStatus_SESSION_STATUS_RUNNING,
				Title:   "Session 1",
				Preview: "More streaming output",
			}, nil)),
		})
	}
	if c.m.homeFocusReconcileCount != afterApCount {
		t.Fatalf("unrelated streaming session updates triggered Home aggregation %d times (want 0)",
			c.m.homeFocusReconcileCount-afterApCount)
	}
}

func BenchmarkDispatchSessionStreaming1000Sessions(b *testing.B) {
	c, fx, legID := seedBenchmarkCore(b, 4, 20, 1000)
	defer c.Close()

	baseNow := int64(1_776_100_000_000)
	c.Dispatch(&corev1.Event{
		NowMs: baseNow + 50,
		Event: &corev1.Event_SessionWatch{
			SessionWatch: &corev1.SessionWatchEvent{SessionId: "s-0001", Watched: true},
		},
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		seq := uint64(500 + i)
		c.Dispatch(&corev1.Event{
			NowMs: baseNow + 100 + int64(i),
			Event: testrelay.EventsAppendedFrame(legID, "s-0001", fx.StoredEventWire("s-0001", seq, &v1.Event{
				Seq:  seq,
				Kind: &v1.Event_AgentMessage{AgentMessage: &v1.AgentMessage{Text: "token "}},
			})),
		})
	}
}
