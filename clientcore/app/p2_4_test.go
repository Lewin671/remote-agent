package app

import (
	"testing"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

func TestWatchedSessionsEffectAndUsageHydration(t *testing.T) {
	c := New()
	defer c.Close()

	// Start & connect with one machine and session.
	b := c.Dispatch(&corev1.Event{
		NowMs:  1_000,
		MonoMs: 1_000,
		Event: &corev1.Event_SyncLifecycle{
			SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START},
		},
	})
	var tokenOp uint64
	for _, eff := range b.Effects {
		if et := eff.GetEnsureToken(); et != nil {
			tokenOp = et.OpId
		}
	}
	b = c.Dispatch(&corev1.Event{
		NowMs:  1_010,
		MonoMs: 1_010,
		Event: &corev1.Event_TokenResult{
			TokenResult: &corev1.TokenResultEvent{OpId: tokenOp, Token: "tok-1"},
		},
	})
	var legID uint64
	for _, eff := range b.Effects {
		if os := eff.GetOpenStream(); os != nil {
			legID = os.LegId
		}
	}
	b = c.Dispatch(&corev1.Event{
		NowMs:  1_020,
		MonoMs: 1_020,
		Event: &corev1.Event_StreamReady{
			StreamReady: &corev1.StreamReadyEvent{LegId: legID, ServerTimeMs: 1_020},
		},
	})

	// Connecting emits SyncPushSubscriptionEffect once.
	sawPush := false
	var listOp uint64
	for _, eff := range b.Effects {
		if eff.GetSyncPushSubscription() != nil {
			sawPush = true
		}
		if rpc := eff.GetRpc(); rpc != nil && rpc.GetFetchLists() != nil {
			listOp = rpc.OpId
		}
	}
	if !sawPush {
		t.Fatal("expected SyncPushSubscriptionEffect on StreamReady")
	}

	// Watch before session list arrives -> route has empty machine_id.
	b = c.Dispatch(&corev1.Event{
		NowMs:  1_030,
		MonoMs: 1_030,
		Event: &corev1.Event_SessionWatch{
			SessionWatch: &corev1.SessionWatchEvent{SessionId: "s1", Watched: true},
		},
	})
	var watched *corev1.WatchedSessionsEffect
	for _, eff := range b.Effects {
		if ws := eff.GetWatchedSessions(); ws != nil {
			watched = ws
		}
	}
	if watched == nil || len(watched.Sessions) != 1 || watched.Sessions[0].SessionId != "s1" || watched.Sessions[0].MachineId != "" {
		t.Fatalf("expected watched route (s1, \"\"), got %+v", watched)
	}

	// Session arrives via ListsFetched -> WatchedSessionsEffect updates with machine_id!
	b = c.Dispatch(&corev1.Event{
		NowMs:  1_040,
		MonoMs: 1_040,
		Event: &corev1.Event_ListsFetched{
			ListsFetched: &corev1.ListsFetchedEvent{
				OpId: listOp,
				Machines: []*corev1.PlainMachine{
					{
						MachineId: "m1",
						Online:    true,
						Meta: &v1.MachineMeta{
							Hostname: "Mac",
							Projects: []*v1.ProjectSummary{
								{Path: "/repo/new", Ended: 2},
							},
						},
					},
				},
				Sessions: []*corev1.PlainSession{
					{SessionId: "s1", MachineId: "m1", LastSeq: 5, Meta: &v1.Session{Id: "s1", Cwd: "/repo"}},
				},
			},
		},
	})
	watched = nil
	for _, eff := range b.Effects {
		if ws := eff.GetWatchedSessions(); ws != nil {
			watched = ws
		}
	}
	if watched == nil || len(watched.Sessions) != 1 || watched.Sessions[0].MachineId != "m1" {
		t.Fatalf("expected updated watched route (s1, m1), got %+v", watched)
	}

	// SessionOpen hydrates usage cache for m1 once.
	b = c.Dispatch(&corev1.Event{
		NowMs:  1_050,
		MonoMs: 1_050,
		Event: &corev1.Event_SessionOpen{
			SessionOpen: &corev1.SessionOpenAction{SessionId: "s1"},
		},
	})
	hydrateCount := 0
	for _, eff := range b.Effects {
		if u := eff.GetUsageCacheRequest(); u != nil && !u.DeleteCache && u.MachineId == "m1" {
			hydrateCount++
		}
	}
	if hydrateCount != 1 {
		t.Fatalf("expected 1 UsageCacheRequestEffect(read) for m1, got %d", hydrateCount)
	}

	// Opening again does not duplicate read request.
	b = c.Dispatch(&corev1.Event{
		NowMs:  1_060,
		MonoMs: 1_060,
		Event: &corev1.Event_SessionOpen{
			SessionOpen: &corev1.SessionOpenAction{SessionId: "s1"},
		},
	})
	for _, eff := range b.Effects {
		if u := eff.GetUsageCacheRequest(); u != nil && !u.DeleteCache {
			t.Fatalf("unexpected duplicate UsageCacheRequestEffect: %+v", u)
		}
	}

	// ConnectionViewModel reports connected and no banner.
	vm, err := c.View(&corev1.Screen{Screen: &corev1.Screen_Connection{Connection: &corev1.ConnectionScreenRequest{}}})
	if err != nil {
		t.Fatalf("View(Connection): %v", err)
	}
	connVM := vm.GetConnection()
	if connVM == nil || !connVM.Connected || connVM.Banner != nil {
		t.Fatalf("unexpected ConnectionViewModel: %+v", connVM)
	}
}

func TestNewSessionAutoPaginatesWhenUnsettled(t *testing.T) {
	c := New()
	defer c.Close()

	b := c.Dispatch(&corev1.Event{
		NowMs:  2_000,
		MonoMs: 2_000,
		Event: &corev1.Event_SyncLifecycle{
			SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START},
		},
	})
	var tokenOp uint64
	for _, eff := range b.Effects {
		if et := eff.GetEnsureToken(); et != nil {
			tokenOp = et.OpId
		}
	}
	b = c.Dispatch(&corev1.Event{
		NowMs:  2_010,
		MonoMs: 2_010,
		Event:  &corev1.Event_TokenResult{TokenResult: &corev1.TokenResultEvent{OpId: tokenOp, Token: "tok-1"}},
	})
	var legID uint64
	for _, eff := range b.Effects {
		if os := eff.GetOpenStream(); os != nil {
			legID = os.LegId
		}
	}
	b = c.Dispatch(&corev1.Event{
		NowMs:  2_020,
		MonoMs: 2_020,
		Event:  &corev1.Event_StreamReady{StreamReady: &corev1.StreamReadyEvent{LegId: legID, ServerTimeMs: 2_020}},
	})
	var listOp uint64
	for _, eff := range b.Effects {
		if rpc := eff.GetRpc(); rpc != nil && rpc.GetFetchLists() != nil {
			listOp = rpc.OpId
		}
	}
	c.Dispatch(&corev1.Event{
		NowMs:  2_030,
		MonoMs: 2_030,
		Event: &corev1.Event_ListsFetched{
			ListsFetched: &corev1.ListsFetchedEvent{
				OpId: listOp,
				Machines: []*corev1.PlainMachine{
					{
						MachineId: "m1",
						Online:    true,
						Meta: &v1.MachineMeta{
							Hostname: "Mac",
							Projects: []*v1.ProjectSummary{
								{Path: "/repo/new", Ended: 2},
							},
						},
					},
				},
				Next: &v1.SessionCursor{EndedMs: 100, SessionId: "s-prev"},
			},
		},
	})

	// Opening NewSession for a project without a session yet triggers FetchSessionsPage.
	b = c.Dispatch(&corev1.Event{
		NowMs:  2_040,
		MonoMs: 2_040,
		Event:  &corev1.Event_NewSessionOpen{NewSessionOpen: &corev1.NewSessionOpenAction{ProjectKey: "/repo/new"}},
	})
	sawPage := false
	for _, eff := range b.Effects {
		if rpc := eff.GetRpc(); rpc != nil && rpc.GetFetchSessionsPage() != nil {
			sawPage = true
		}
	}
	if !sawPage {
		t.Fatal("expected NewSessionOpen to request next sessions page when unsettled and hasMoreSessions=true")
	}
}

func TestResolveNotificationTapRoutesAndTimeout(t *testing.T) {
	c := New()
	defer c.Close()

	// Seed running engine with one task-owned session and one plain session.
	b := c.Dispatch(&corev1.Event{
		NowMs:  3_000,
		MonoMs: 3_000,
		Event: &corev1.Event_SyncLifecycle{
			SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START},
		},
	})
	var tokenOp uint64
	for _, eff := range b.Effects {
		if et := eff.GetEnsureToken(); et != nil {
			tokenOp = et.OpId
		}
	}
	b = c.Dispatch(&corev1.Event{
		NowMs:  3_010,
		MonoMs: 3_010,
		Event:  &corev1.Event_TokenResult{TokenResult: &corev1.TokenResultEvent{OpId: tokenOp, Token: "tok-1"}},
	})
	var legID uint64
	for _, eff := range b.Effects {
		if os := eff.GetOpenStream(); os != nil {
			legID = os.LegId
		}
	}
	b = c.Dispatch(&corev1.Event{
		NowMs:  3_020,
		MonoMs: 3_020,
		Event:  &corev1.Event_StreamReady{StreamReady: &corev1.StreamReadyEvent{LegId: legID, ServerTimeMs: 3_020}},
	})
	var listOp uint64
	for _, eff := range b.Effects {
		if rpc := eff.GetRpc(); rpc != nil && rpc.GetFetchLists() != nil {
			listOp = rpc.OpId
		}
	}
	c.Dispatch(&corev1.Event{
		NowMs:  3_030,
		MonoMs: 3_030,
		Event: &corev1.Event_ListsFetched{
			ListsFetched: &corev1.ListsFetchedEvent{
				OpId: listOp,
				Machines: []*corev1.PlainMachine{
					{
						MachineId: "m1",
						Online:    true,
						Meta: &v1.MachineMeta{
							Hostname: "Mac",
							Projects: []*v1.ProjectSummary{
								{Path: "/repo/new", Ended: 2},
							},
						},
					},
				},
				Sessions: []*corev1.PlainSession{
					{
						SessionId: "s-task",
						MachineId: "m1",
						Meta:      &v1.Session{Id: "s-task", TaskOwned: true, ScheduledTaskId: "task-1"},
					},
					{
						SessionId: "s-chat",
						MachineId: "m1",
						Meta:      &v1.Session{Id: "s-chat", TaskOwned: false},
					},
				},
			},
		},
	})

	// 1. Task-owned session -> ScheduleNavigate(DETAIL).
	b = c.Dispatch(&corev1.Event{
		NowMs:  3_040,
		MonoMs: 3_040,
		Event: &corev1.Event_ResolveNotificationTap{
			ResolveNotificationTap: &corev1.ResolveNotificationTapAction{SessionId: "s-task", Scheduled: true},
		},
	})
	var nav *corev1.ScheduleNavigateEffect
	for _, eff := range b.Effects {
		if sn := eff.GetScheduleNavigate(); sn != nil {
			nav = sn
		}
	}
	if nav == nil || nav.Route != corev1.ScheduleNavigateRoute_SCHEDULE_NAVIGATE_ROUTE_DETAIL || nav.MachineId != "m1" || nav.TaskId != "task-1" {
		t.Fatalf("expected DETAIL(m1, task-1), got %+v", nav)
	}

	// 2. Non-task-owned session -> ScheduleNavigate(SESSION) preserving schedule_seq.
	b = c.Dispatch(&corev1.Event{
		NowMs:  3_050,
		MonoMs: 3_050,
		Event: &corev1.Event_ResolveNotificationTap{
			ResolveNotificationTap: &corev1.ResolveNotificationTapAction{SessionId: "s-chat", Scheduled: true, ScheduleSeq: 42},
		},
	})
	nav = nil
	for _, eff := range b.Effects {
		if sn := eff.GetScheduleNavigate(); sn != nil {
			nav = sn
		}
	}
	if nav == nil || nav.Route != corev1.ScheduleNavigateRoute_SCHEDULE_NAVIGATE_ROUTE_SESSION || nav.SessionId != "s-chat" || nav.FirstSeq != 42 {
		t.Fatalf("expected SESSION(s-chat, 42), got %+v", nav)
	}

	// 3. Cold miss -> triggers lookup + 3s timer -> timeout falls back to LIST.
	b = c.Dispatch(&corev1.Event{
		NowMs:  3_060,
		MonoMs: 3_060,
		Event: &corev1.Event_ResolveNotificationTap{
			ResolveNotificationTap: &corev1.ResolveNotificationTapAction{SessionId: "s-missing", Scheduled: true},
		},
	})
	var timerID uint64
	sawLookup := false
	for _, eff := range b.Effects {
		if st := eff.GetScheduleTimer(); st != nil && st.Kind == corev1.TimerKind_TIMER_KIND_UI_TRANSIENT && st.Key == notifTapTimerKey {
			timerID = st.TimerId
		}
		if rpc := eff.GetRpc(); rpc != nil && rpc.GetFetchSessionById() != nil && rpc.GetFetchSessionById().SessionId == "s-missing" {
			sawLookup = true
		}
	}
	if !sawLookup || timerID == 0 {
		t.Fatalf("expected lookup=%v and timerID=%d", sawLookup, timerID)
	}
	b = c.Dispatch(&corev1.Event{
		NowMs:  6_060,
		MonoMs: 6_060,
		Event: &corev1.Event_TimerFired{
			TimerFired: &corev1.TimerFiredEvent{TimerId: timerID, Kind: corev1.TimerKind_TIMER_KIND_UI_TRANSIENT, Key: notifTapTimerKey},
		},
	})
	nav = nil
	for _, eff := range b.Effects {
		if sn := eff.GetScheduleNavigate(); sn != nil {
			nav = sn
		}
	}
	if nav == nil || nav.Route != corev1.ScheduleNavigateRoute_SCHEDULE_NAVIGATE_ROUTE_LIST {
		t.Fatalf("expected fallback LIST on timeout, got %+v", nav)
	}
}

func TestCacheEventsNewestExceedingKnownLastSeqDropsCache(t *testing.T) {
	c := New()
	defer c.Close()

	b := c.Dispatch(&corev1.Event{
		NowMs:  4_000,
		MonoMs: 4_000,
		Event: &corev1.Event_SyncLifecycle{
			SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START},
		},
	})
	var bootOp uint64
	for _, eff := range b.Effects {
		if cr := eff.GetCacheRead(); cr != nil && cr.GetBootstrap() != nil {
			bootOp = cr.GetBootstrap().OpId
		}
	}
	c.Dispatch(&corev1.Event{
		NowMs:  4_010,
		MonoMs: 4_010,
		Event: &corev1.Event_CacheBootstrapLoaded{
			CacheBootstrapLoaded: &corev1.CacheBootstrapLoadedEvent{
				OpId: bootOp,
				Sessions: []*corev1.PlainSession{
					{SessionId: "s1", MachineId: "m1", LastSeq: 2, Meta: &v1.Session{Id: "s1"}},
				},
			},
		},
	})
	b = c.Dispatch(&corev1.Event{
		NowMs:  4_020,
		MonoMs: 4_020,
		Event:  &corev1.Event_SessionWatch{SessionWatch: &corev1.SessionWatchEvent{SessionId: "s1", Watched: true}},
	})
	var evOp uint64
	for _, eff := range b.Effects {
		if cr := eff.GetCacheRead(); cr != nil && cr.GetEvents() != nil {
			evOp = cr.GetEvents().OpId
		}
	}
	if evOp == 0 {
		t.Fatal("expected CacheReadEvents effect")
	}
	b = c.Dispatch(&corev1.Event{
		NowMs:  4_030,
		MonoMs: 4_030,
		Event: &corev1.Event_CacheEventsLoaded{
			CacheEventsLoaded: &corev1.CacheEventsLoadedEvent{
				OpId:      evOp,
				SessionId: "s1",
				WireEvents: []*v1.StoredEvent{
					{Seq: 5},
				},
			},
		},
	})
	dropped := false
	for _, eff := range b.Effects {
		if cd := eff.GetCacheDelete(); cd != nil && cd.GetDropEventsSessionId() == "s1" {
			dropped = true
		}
	}
	if !dropped {
		t.Fatal("expected CacheDelete(drop_events_session_id=s1) when cached seq > known lastSeq")
	}
}
