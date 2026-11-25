package app

import (
	"fmt"
	"testing"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/gen/timestamppb"
	"github.com/Lewin671/remote-agent/clientcore/sync"
	stdcorev1 "github.com/Lewin671/remote-agent/gen/remoteagent/core/v1"
	stdv1 "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

func seedBenchmarkCore(tb testing.TB, numMachines, numProjects, numSessions int) (*Core, uint64) {
	tb.Helper()
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
				ProtocolVersion: sync.PeerVersion,
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
	ev := &stdcorev1.Event{
		NowMs: baseNow + 100,
		Event: &stdcorev1.Event_StreamFrame{
			StreamFrame: &stdcorev1.StreamFrameEvent{
				LegId: legID,
				Frame: &stdcorev1.StreamFrameEvent_SessionUpdated{
					SessionUpdated: &stdcorev1.PlainSession{
						SessionId: "s-0001",
						MachineId: "m-1",
						LastSeq:   500,
						UpdatedMs: baseNow + 100,
						Meta: &stdv1.Session{
							Id:      "s-0001",
							Cwd:     "/Users/dev/work/proj-01",
							Agent:   stdv1.Agent_AGENT_CODEX,
							Status:  stdv1.SessionStatus_SESSION_STATUS_RUNNING,
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
	c, legID := seedBenchmarkCore(t, 4, 10, 200)
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
			Event: &corev1.Event_StreamFrame{
				StreamFrame: &corev1.StreamFrameEvent{
					LegId: legID,
					Frame: &corev1.StreamFrameEvent_EventsAppended{
						EventsAppended: &corev1.EventsAppendedFrame{
							SessionId: "s-0001",
							Events: []*corev1.PlainEvent{
								{
									Seq:      seq,
									Verified: true,
									Event: &v1.Event{
										Seq: seq,
										Kind: &v1.Event_AgentMessage{
											AgentMessage: &v1.AgentMessage{Text: "chunk "},
										},
									},
								},
							},
						},
					},
				},
			},
		})
		c.Dispatch(&corev1.Event{
			NowMs: baseNow + 300 + int64(i),
			Event: &corev1.Event_StreamFrame{
				StreamFrame: &corev1.StreamFrameEvent{
					LegId: legID,
					Frame: &corev1.StreamFrameEvent_SessionUpdated{
						SessionUpdated: &corev1.PlainSession{
							SessionId: "s-0001",
							MachineId: "m-1",
							LastSeq:   seq,
							UpdatedMs: baseNow + 300 + int64(i),
							Meta: &v1.Session{
								Id:      "s-0001",
								Cwd:     "/Users/dev/work/proj-01",
								Agent:   v1.Agent_AGENT_CODEX,
								Status:  v1.SessionStatus_SESSION_STATUS_RUNNING,
								Title:   "Session 1",
								Preview: "Streaming output",
							},
						},
					},
				},
			},
		})
	}
	if c.m.homeFocusReconcileCount != beforeCount {
		t.Fatalf("streaming session events with no pending attention triggered Home aggregation %d times (want 0)",
			c.m.homeFocusReconcileCount-beforeCount)
	}

	// Now introduce an approval on s-0002 -> MUST trigger Home aggregation once!
	c.Dispatch(&corev1.Event{
		NowMs: baseNow + 1000,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: legID,
				Frame: &corev1.StreamFrameEvent_SessionUpdated{
					SessionUpdated: &corev1.PlainSession{
						SessionId: "s-0002",
						MachineId: "m-0",
						LastSeq:   200,
						UpdatedMs: baseNow + 1000,
						Meta: &v1.Session{
							Id:     "s-0002",
							Cwd:    "/Users/dev/work/proj-02",
							Agent:  v1.Agent_AGENT_CODEX,
							Status: v1.SessionStatus_SESSION_STATUS_RUNNING,
							Title:  "Session 2",
						},
						Checkpoint: &v1.LiveSnapshot{
							Actions: []*v1.Event{
								{
									Seq: 200,
									Kind: &v1.Event_ApprovalRequested{
										ApprovalRequested: &v1.ApprovalRequested{ApprovalId: "ap-bg", Command: "ls"},
									},
								},
							},
						},
					},
				},
			},
		},
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
			Event: &corev1.Event_StreamFrame{
				StreamFrame: &corev1.StreamFrameEvent{
					LegId: legID,
					Frame: &corev1.StreamFrameEvent_SessionUpdated{
						SessionUpdated: &corev1.PlainSession{
							SessionId: "s-0001",
							MachineId: "m-1",
							LastSeq:   seq,
							UpdatedMs: baseNow + 2000 + int64(i),
							Meta: &v1.Session{
								Id:      "s-0001",
								Cwd:     "/Users/dev/work/proj-01",
								Agent:   v1.Agent_AGENT_CODEX,
								Status:  v1.SessionStatus_SESSION_STATUS_RUNNING,
								Title:   "Session 1",
								Preview: "More streaming output",
							},
						},
					},
				},
			},
		})
	}
	if c.m.homeFocusReconcileCount != afterApCount {
		t.Fatalf("unrelated streaming session updates triggered Home aggregation %d times (want 0)",
			c.m.homeFocusReconcileCount-afterApCount)
	}
}

func BenchmarkDispatchSessionStreaming1000Sessions(b *testing.B) {
	c, legID := seedBenchmarkCore(b, 4, 20, 1000)
	defer c.Close()

	baseNow := int64(1_776_100_000_000)
	c.Dispatch(&corev1.Event{
		NowMs: baseNow + 50,
		Event: &corev1.Event_SessionWatch{
			SessionWatch: &corev1.SessionWatchEvent{SessionId: "s-0001", Watched: true},
		},
	})
	ev := &corev1.Event{
		NowMs: baseNow + 100,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: legID,
				Frame: &corev1.StreamFrameEvent_EventsAppended{
					EventsAppended: &corev1.EventsAppendedFrame{
						SessionId: "s-0001",
						Events: []*corev1.PlainEvent{
							{
								Seq:      500,
								Verified: true,
								Event: &v1.Event{
									Seq:  500,
									Kind: &v1.Event_AgentMessage{AgentMessage: &v1.AgentMessage{Text: "token "}},
								},
							},
						},
					},
				},
			},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		seq := uint64(500 + i)
		ev.NowMs = baseNow + 100 + int64(i)
		pe := ev.GetStreamFrame().GetEventsAppended().Events[0]
		pe.Seq = seq
		pe.Event.Seq = seq
		c.Dispatch(ev)
	}
}
