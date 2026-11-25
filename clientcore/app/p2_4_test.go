package app

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"testing"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/security"
	"github.com/Lewin671/remote-agent/clientcore/sync"
	stdv1 "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/shared/devicesec"
	"google.golang.org/protobuf/proto"
)

type connectedCoreFixture struct {
	core        *Core
	legID       uint64
	streamBatch *corev1.EffectBatch
	listsBatch  *corev1.EffectBatch
}

func bootConnectedCore(
	t *testing.T,
	baseMs int64,
	machines []*corev1.PlainMachine,
	sessions []*corev1.PlainSession,
	next *v1.SessionCursor,
) connectedCoreFixture {
	t.Helper()
	c := New()
	t.Cleanup(c.Close)

	b := c.Dispatch(&corev1.Event{
		NowMs:  baseMs,
		MonoMs: baseMs,
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
		NowMs:  baseMs + 10,
		MonoMs: baseMs + 10,
		Event:  &corev1.Event_TokenResult{TokenResult: &corev1.TokenResultEvent{OpId: tokenOp, Token: "tok-1"}},
	})
	var legID uint64
	for _, eff := range b.Effects {
		if os := eff.GetOpenStream(); os != nil {
			legID = os.LegId
		}
	}
	streamBatch := c.Dispatch(&corev1.Event{
		NowMs:  baseMs + 20,
		MonoMs: baseMs + 20,
		Event:  &corev1.Event_StreamReady{StreamReady: &corev1.StreamReadyEvent{LegId: legID, ServerTimeMs: baseMs + 20}},
	})
	var listOp uint64
	for _, eff := range streamBatch.Effects {
		if rpc := eff.GetRpc(); rpc != nil && rpc.GetFetchLists() != nil {
			listOp = rpc.OpId
		}
	}
	var listsBatch *corev1.EffectBatch
	if machines != nil || sessions != nil || next != nil {
		listsBatch = c.Dispatch(&corev1.Event{
			NowMs:  baseMs + 30,
			MonoMs: baseMs + 30,
			Event: &corev1.Event_ListsFetched{
				ListsFetched: &corev1.ListsFetchedEvent{
					OpId:     listOp,
					Machines: machines,
					Sessions: sessions,
					Next:     next,
				},
			},
		})
	}
	return connectedCoreFixture{
		core:        c,
		legID:       legID,
		streamBatch: streamBatch,
		listsBatch:  listsBatch,
	}
}

func countRenderTargets(b *corev1.EffectBatch, target corev1.ScreenTarget) int {
	n := 0
	if b == nil {
		return 0
	}
	for _, eff := range b.Effects {
		if r := eff.GetRender(); r != nil {
			for _, st := range r.Screens {
				if st == target {
					n++
				}
			}
		}
	}
	return n
}

func TestWatchedSessionsEffectAndUsageHydration(t *testing.T) {
	fx := bootConnectedCore(t, 1_000, nil, nil, nil)
	c := fx.core

	// Connecting emits SyncPushSubscriptionEffect once.
	sawPush := false
	var listOp uint64
	for _, eff := range fx.streamBatch.Effects {
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
	b := c.Dispatch(&corev1.Event{
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

	// Session arrives via ListsFetched -> WatchedSessionsEffect updates with machine_id.
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
		Event:  &corev1.Event_SessionOpen{SessionOpen: &corev1.SessionOpenAction{SessionId: "s1"}},
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
		Event:  &corev1.Event_SessionOpen{SessionOpen: &corev1.SessionOpenAction{SessionId: "s1"}},
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
	fx := bootConnectedCore(
		t,
		2_000,
		[]*corev1.PlainMachine{
			{
				MachineId: "m1",
				Online:    true,
				Meta: &v1.MachineMeta{
					Hostname: "Mac",
					Projects: []*v1.ProjectSummary{
						{Path: "/repo/a", Ended: 2},
						{Path: "/repo/b", Ended: 2},
					},
				},
			},
		},
		nil,
		&v1.SessionCursor{EndedMs: 100, SessionId: "s-prev"},
	)
	c := fx.core

	// Open A -> requests page 1.
	b := c.Dispatch(&corev1.Event{
		NowMs:  2_040,
		MonoMs: 2_040,
		Event:  &corev1.Event_NewSessionOpen{NewSessionOpen: &corev1.NewSessionOpenAction{ProjectKey: "/repo/a"}},
	})
	var pageOp1 uint64
	for _, eff := range b.Effects {
		if rpc := eff.GetRpc(); rpc != nil && rpc.GetFetchSessionsPage() != nil {
			pageOp1 = rpc.OpId
		}
	}
	if pageOp1 == 0 {
		t.Fatal("expected NewSessionOpen(/repo/a) to request next sessions page when unsettled")
	}

	// Navigate A -> B where A's onDispose fires AFTER B's open: closing A must NOT cancel B's paging.
	c.Dispatch(&corev1.Event{
		NowMs:  2_042,
		MonoMs: 2_042,
		Event:  &corev1.Event_NewSessionOpen{NewSessionOpen: &corev1.NewSessionOpenAction{ProjectKey: "/repo/b"}},
	})
	c.Dispatch(&corev1.Event{
		NowMs:  2_044,
		MonoMs: 2_044,
		Event:  &corev1.Event_NewSessionClose{NewSessionClose: &corev1.NewSessionCloseAction{ProjectKey: "/repo/a"}},
	})

	// Receiving page 1 (still unsettled for B, HasMoreSessions=true) continuously requests page 2.
	b = c.Dispatch(&corev1.Event{
		NowMs:  2_050,
		MonoMs: 2_050,
		Event: &corev1.Event_SessionsPageFetched{
			SessionsPageFetched: &corev1.SessionsPageFetchedEvent{
				OpId: pageOp1,
				Sessions: []*corev1.PlainSession{
					{SessionId: "s-other", MachineId: "m1", Meta: &v1.Session{Id: "s-other", Cwd: "/repo/other"}},
				},
				Next: &v1.SessionCursor{EndedMs: 50, SessionId: "s-other"},
			},
		},
	})
	var pageOp2 uint64
	for _, eff := range b.Effects {
		if rpc := eff.GetRpc(); rpc != nil && rpc.GetFetchSessionsPage() != nil {
			pageOp2 = rpc.OpId
		}
	}
	if pageOp2 == 0 {
		t.Fatal("expected continuous paging for /repo/b after stale close of /repo/a")
	}

	// Closing /repo/b stops further auto-paging on subsequent page arrivals.
	c.Dispatch(&corev1.Event{
		NowMs:  2_060,
		MonoMs: 2_060,
		Event:  &corev1.Event_NewSessionClose{NewSessionClose: &corev1.NewSessionCloseAction{ProjectKey: "/repo/b"}},
	})
	b = c.Dispatch(&corev1.Event{
		NowMs:  2_070,
		MonoMs: 2_070,
		Event: &corev1.Event_SessionsPageFetched{
			SessionsPageFetched: &corev1.SessionsPageFetchedEvent{
				OpId: pageOp2,
				Sessions: []*corev1.PlainSession{
					{SessionId: "s-other-2", MachineId: "m1", Meta: &v1.Session{Id: "s-other-2", Cwd: "/repo/other"}},
				},
				Next: &v1.SessionCursor{EndedMs: 20, SessionId: "s-other-2"},
			},
		},
	})
	for _, eff := range b.Effects {
		if rpc := eff.GetRpc(); rpc != nil && rpc.GetFetchSessionsPage() != nil {
			t.Fatalf("expected no FetchSessionsPage after matching NewSessionClose(/repo/b), got op %d", rpc.OpId)
		}
	}
}

func TestResolveNotificationTapRoutesAndTimeout(t *testing.T) {
	fx := bootConnectedCore(
		t,
		3_000,
		[]*corev1.PlainMachine{
			{
				MachineId: "m1",
				Online:    true,
				Meta: &v1.MachineMeta{
					Hostname:        "Mac",
					ProtocolVersion: sync.PeerVersion,
					Capabilities:    &v1.MachineCapabilities{ScheduledTasks: true},
				},
			},
		},
		[]*corev1.PlainSession{
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
		nil,
	)
	c := fx.core
	c.Dispatch(&corev1.Event{
		NowMs: 3_035,
		Event: &corev1.Event_EntropyProvided{
			EntropyProvided: &corev1.EntropyProvided{
				RandomIds:  []string{"id-1", "id-2", "id-3", "id-4", "id-5", "id-6"},
				UnitFloats: []float64{0.5, 0.5, 0.5},
			},
		},
	})

	// 1. Non-task-owned session resolved BEFORE schedActive=true -> navigates to SESSION
	//    AND still triggers schedule.list refresh for m1.
	b := c.Dispatch(&corev1.Event{
		NowMs:  3_040,
		MonoMs: 3_040,
		Event: &corev1.Event_ResolveNotificationTap{
			ResolveNotificationTap: &corev1.ResolveNotificationTapAction{SessionId: "s-chat", ScheduleSeq: 42},
		},
	})
	var nav *corev1.ScheduleNavigateEffect
	sawRefreshCmd := false
	for _, eff := range b.Effects {
		if sn := eff.GetScheduleNavigate(); sn != nil {
			nav = sn
		}
		if dc := eff.GetDispatchCommand(); dc != nil && dc.MachineId == "m1" {
			var cmd v1.Command
			if cmd.Unmarshal(dc.CommandBytes) == nil && cmd.GetSchedule().GetOperation() == "list" {
				sawRefreshCmd = true
			}
		}
	}
	if nav == nil || nav.Route != corev1.ScheduleNavigateRoute_SCHEDULE_NAVIGATE_ROUTE_SESSION || nav.SessionId != "s-chat" || nav.FirstSeq != 42 {
		t.Fatalf("expected SESSION(s-chat, 42), got %+v", nav)
	}
	if !sawRefreshCmd {
		t.Fatal("expected timely schedule.list refresh on notification tap even when schedActive=false")
	}

	// 2. Task-owned session -> ScheduleNavigate(DETAIL).
	b = c.Dispatch(&corev1.Event{
		NowMs:  3_050,
		MonoMs: 3_050,
		Event: &corev1.Event_ResolveNotificationTap{
			ResolveNotificationTap: &corev1.ResolveNotificationTapAction{SessionId: "s-task"},
		},
	})
	nav = nil
	for _, eff := range b.Effects {
		if sn := eff.GetScheduleNavigate(); sn != nil {
			nav = sn
		}
	}
	if nav == nil || nav.Route != corev1.ScheduleNavigateRoute_SCHEDULE_NAVIGATE_ROUTE_DETAIL || nav.MachineId != "m1" || nav.TaskId != "task-1" {
		t.Fatalf("expected DETAIL(m1, task-1), got %+v", nav)
	}

	// 3. Cold miss -> immediately followed by ScheduleListOpen(true) (Android cold-start LaunchedEffect(route))
	//    -> SessionByIdFetched arrives -> MUST still resolve and navigate to DETAIL!
	b = c.Dispatch(&corev1.Event{
		NowMs:  3_055,
		MonoMs: 3_055,
		Event: &corev1.Event_ResolveNotificationTap{
			ResolveNotificationTap: &corev1.ResolveNotificationTapAction{SessionId: "s-lookup"},
		},
	})
	var lookupOp uint64
	for _, eff := range b.Effects {
		if rpc := eff.GetRpc(); rpc != nil && rpc.GetFetchSessionById() != nil && rpc.GetFetchSessionById().SessionId == "s-lookup" {
			lookupOp = rpc.OpId
		}
	}
	if lookupOp == 0 {
		t.Fatal("expected FetchSessionById RPC for s-lookup")
	}
	openTrue := true
	c.Dispatch(&corev1.Event{
		NowMs:  3_056,
		MonoMs: 3_056,
		Event:  &corev1.Event_ScheduleListOpen{ScheduleListOpen: &corev1.ScheduleBoolAction{Value: &openTrue}},
	})
	b = c.Dispatch(&corev1.Event{
		NowMs:  3_058,
		MonoMs: 3_058,
		Event: &corev1.Event_SessionByIdFetched{
			SessionByIdFetched: &corev1.SessionByIdFetchedEvent{
				OpId: lookupOp,
				Session: &corev1.PlainSession{
					SessionId: "s-lookup",
					MachineId: "m1",
					Meta:      &v1.Session{Id: "s-lookup", TaskOwned: true, ScheduledTaskId: "task-9"},
				},
			},
		},
	})
	nav = nil
	for _, eff := range b.Effects {
		if sn := eff.GetScheduleNavigate(); sn != nil {
			nav = sn
		}
	}
	if nav == nil || nav.Route != corev1.ScheduleNavigateRoute_SCHEDULE_NAVIGATE_ROUTE_DETAIL || nav.MachineId != "m1" || nav.TaskId != "task-9" {
		t.Fatalf("expected lookup resolution to DETAIL(m1, task-9) despite ScheduleListOpen, got %+v", nav)
	}

	// 4. Cold miss -> followed by ScheduleListOpen(true) & SessionWatch(other) -> timeout STILL falls back to LIST.
	b = c.Dispatch(&corev1.Event{
		NowMs:  7_000,
		MonoMs: 7_000,
		Event: &corev1.Event_ResolveNotificationTap{
			ResolveNotificationTap: &corev1.ResolveNotificationTapAction{SessionId: "s-missing"},
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
	c.Dispatch(&corev1.Event{
		NowMs:  7_005,
		MonoMs: 7_005,
		Event:  &corev1.Event_ScheduleListOpen{ScheduleListOpen: &corev1.ScheduleBoolAction{Value: &openTrue}},
	})
	c.Dispatch(&corev1.Event{
		NowMs:  7_006,
		MonoMs: 7_006,
		Event:  &corev1.Event_SessionWatch{SessionWatch: &corev1.SessionWatchEvent{SessionId: "s-chat", Watched: true}},
	})
	b = c.Dispatch(&corev1.Event{
		NowMs:  10_000,
		MonoMs: 10_000,
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
		t.Fatalf("expected fallback LIST on timeout despite ScheduleListOpen/SessionWatch, got %+v", nav)
	}

	// 5. Cold miss -> STOP cancels pending tap so timeout does NOT navigate.
	b = c.Dispatch(&corev1.Event{
		NowMs:  11_000,
		MonoMs: 11_000,
		Event: &corev1.Event_ResolveNotificationTap{
			ResolveNotificationTap: &corev1.ResolveNotificationTapAction{SessionId: "s-stop"},
		},
	})
	var stopTimer uint64
	for _, eff := range b.Effects {
		if st := eff.GetScheduleTimer(); st != nil && st.Kind == corev1.TimerKind_TIMER_KIND_UI_TRANSIENT && st.Key == notifTapTimerKey {
			stopTimer = st.TimerId
		}
	}
	c.Dispatch(&corev1.Event{
		NowMs:  11_010,
		MonoMs: 11_010,
		Event: &corev1.Event_SyncLifecycle{
			SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_STOP},
		},
	})
	b = c.Dispatch(&corev1.Event{
		NowMs:  14_010,
		MonoMs: 14_010,
		Event: &corev1.Event_TimerFired{
			TimerFired: &corev1.TimerFiredEvent{TimerId: stopTimer, Kind: corev1.TimerKind_TIMER_KIND_UI_TRANSIENT, Key: notifTapTimerKey},
		},
	})
	for _, eff := range b.Effects {
		if sn := eff.GetScheduleNavigate(); sn != nil {
			t.Fatalf("expected no fallback navigation after STOP, got %+v", sn)
		}
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

func TestCacheEventsNewestExceedingZeroLastSeqDropsCache(t *testing.T) {
	c := New()
	defer c.Close()

	b := c.Dispatch(&corev1.Event{
		NowMs:  4_500,
		MonoMs: 4_500,
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
		NowMs:  4_510,
		MonoMs: 4_510,
		Event: &corev1.Event_CacheBootstrapLoaded{
			CacheBootstrapLoaded: &corev1.CacheBootstrapLoadedEvent{
				OpId: bootOp,
				Sessions: []*corev1.PlainSession{
					{SessionId: "s0", MachineId: "m1", LastSeq: 0, Meta: &v1.Session{Id: "s0"}},
				},
			},
		},
	})
	b = c.Dispatch(&corev1.Event{
		NowMs:  4_520,
		MonoMs: 4_520,
		Event:  &corev1.Event_SessionWatch{SessionWatch: &corev1.SessionWatchEvent{SessionId: "s0", Watched: true}},
	})
	var evOp uint64
	for _, eff := range b.Effects {
		if cr := eff.GetCacheRead(); cr != nil && cr.GetEvents() != nil {
			evOp = cr.GetEvents().OpId
		}
	}
	b = c.Dispatch(&corev1.Event{
		NowMs:  4_530,
		MonoMs: 4_530,
		Event: &corev1.Event_CacheEventsLoaded{
			CacheEventsLoaded: &corev1.CacheEventsLoadedEvent{
				OpId:      evOp,
				SessionId: "s0",
				WireEvents: []*v1.StoredEvent{
					{Seq: 1},
				},
			},
		},
	})
	dropped := false
	for _, eff := range b.Effects {
		if cd := eff.GetCacheDelete(); cd != nil && cd.GetDropEventsSessionId() == "s0" {
			dropped = true
		}
	}
	if !dropped {
		t.Fatal("expected CacheDelete(drop_events_session_id=s0) when cached seq > known lastSeq=0")
	}
}

func TestSessionRenderTargetOnConnectionChangeVsSingleSessionEvent(t *testing.T) {
	fx := bootConnectedCore(
		t,
		5_000,
		[]*corev1.PlainMachine{
			{MachineId: "m1", Online: true, Meta: &v1.MachineMeta{Hostname: "Mac"}},
		},
		[]*corev1.PlainSession{
			{SessionId: "s1", MachineId: "m1", LastSeq: 1, Meta: &v1.Session{Id: "s1", Cwd: "/repo"}},
		},
		nil,
	)
	c := fx.core

	// Seed security lease expiring at 6_000ms so a pure ClockTick can flip AccountControlLost
	// without triggering MarkAll or Dirty.Home.
	c.Dispatch(&corev1.Event{
		NowMs:  5_035,
		MonoMs: 5_035,
		Event: &corev1.Event_SeedDemoWorld{
			SeedDemoWorld: &corev1.SeedDemoWorldEvent{
				Security: &corev1.DemoSecurity{
					SelfDeviceId:      "self-1",
					IsMember:          true,
					TrustedMachineIds: []string{"m1"},
					LeaseEndMs:        6_000,
				},
			},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs:  5_040,
		MonoMs: 5_040,
		Event:  &corev1.Event_SessionWatch{SessionWatch: &corev1.SessionWatchEvent{SessionId: "s1", Watched: true}},
	})
	c.Dispatch(&corev1.Event{
		NowMs:  5_045,
		MonoMs: 5_045,
		Event:  &corev1.Event_SessionOpen{SessionOpen: &corev1.SessionOpenAction{SessionId: "s1"}},
	})

	// 1. Single streaming session event -> emits SESSION exactly once, and does NOT emit CONNECTION.
	b := c.Dispatch(&corev1.Event{
		NowMs:  5_050,
		MonoMs: 5_050,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: fx.legID,
				Frame: &corev1.StreamFrameEvent_EventsAppended{
					EventsAppended: &corev1.EventsAppendedFrame{
						SessionId: "s1",
						Events: []*corev1.PlainEvent{
							{Seq: 1, Event: &v1.Event{Seq: 1, Kind: &v1.Event_AgentMessage{AgentMessage: &v1.AgentMessage{Text: "hi"}}}},
						},
					},
				},
			},
		},
	})
	if got := countRenderTargets(b, corev1.ScreenTarget_SCREEN_TARGET_SESSION); got != 1 {
		t.Fatalf("expected SCREEN_TARGET_SESSION once on single session event, got %d", got)
	}
	if got := countRenderTargets(b, corev1.ScreenTarget_SCREEN_TARGET_CONNECTION); got != 0 {
		t.Fatalf("expected no SCREEN_TARGET_CONNECTION on single session event, got %d", got)
	}

	// 2. Non-MarkAll connection change with dirty.Home=false (liveness timer tick crossing LeaseEndMs):
	//    detectConnectionChange sets ONLY dirty.Connection -> emits BOTH CONNECTION and SESSION.
	//    Removing dirty.Connection from wantSession in session.go breaks this assertion.
	var livenessTimer uint64
	for _, eff := range fx.streamBatch.Effects {
		if st := eff.GetScheduleTimer(); st != nil && st.Kind == corev1.TimerKind_TIMER_KIND_LIVENESS {
			livenessTimer = st.TimerId
		}
	}
	if livenessTimer == 0 {
		t.Fatal("expected liveness timer from StreamReady")
	}
	b = c.Dispatch(&corev1.Event{
		NowMs:  6_100,
		MonoMs: 5_500,
		Event: &corev1.Event_TimerFired{
			TimerFired: &corev1.TimerFiredEvent{TimerId: livenessTimer, Kind: corev1.TimerKind_TIMER_KIND_LIVENESS},
		},
	})
	if got := countRenderTargets(b, corev1.ScreenTarget_SCREEN_TARGET_HOME); got != 0 {
		t.Fatalf("expected non-MarkAll ClockTick not to dirty HOME, got %d", got)
	}
	if got := countRenderTargets(b, corev1.ScreenTarget_SCREEN_TARGET_CONNECTION); got != 1 {
		t.Fatalf("expected SCREEN_TARGET_CONNECTION=1 on non-MarkAll lease expiry, got %d", got)
	}
	if got := countRenderTargets(b, corev1.ScreenTarget_SCREEN_TARGET_SESSION); got != 1 {
		t.Fatalf("expected SCREEN_TARGET_SESSION=1 on non-MarkAll connection change, got %d", got)
	}

	// 3. NetworkChanged(false) -> MarkAll no longer sets Connection, yet detectConnectionChange
	//    still refreshes CONNECTION and SESSION.
	b = c.Dispatch(&corev1.Event{
		NowMs:  6_200,
		MonoMs: 6_200,
		Event: &corev1.Event_NetworkChanged{
			NetworkChanged: &corev1.NetworkChangedEvent{Available: false},
		},
	})
	if got := countRenderTargets(b, corev1.ScreenTarget_SCREEN_TARGET_CONNECTION); got != 1 {
		t.Fatalf("expected SCREEN_TARGET_CONNECTION=1 on NetworkChanged(false), got %d", got)
	}
	if got := countRenderTargets(b, corev1.ScreenTarget_SCREEN_TARGET_SESSION); got != 1 {
		t.Fatalf("expected SCREEN_TARGET_SESSION=1 on NetworkChanged(false), got %d", got)
	}
}

// --- Real cryptographic helpers for sealed-result Core verification tests ---

type appTestActor struct {
	id   string
	ed   ed25519.PrivateKey
	edP  ed25519.PublicKey
	x    *ecdh.PrivateKey
	xPub [32]byte
}

func newAppTestActor(t *testing.T, tag byte) appTestActor {
	t.Helper()
	edPriv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{tag}, ed25519.SeedSize))
	edPub := edPriv.Public().(ed25519.PublicKey)
	xPriv, err := ecdh.X25519().NewPrivateKey(bytes.Repeat([]byte{tag + 0x50}, 32))
	if err != nil {
		t.Fatal(err)
	}
	var xp [32]byte
	copy(xp[:], xPriv.PublicKey().Bytes())
	return appTestActor{
		id:   devicesec.ID(edPub),
		ed:   edPriv,
		edP:  edPub,
		x:    xPriv,
		xPub: xp,
	}
}

func (a appTestActor) member(k devicesec.Kind) devicesec.Member {
	return devicesec.Member{ID: a.id, EdPublic: [32]byte(a.edP), XPublic: a.xPub, Kind: k}
}

type appTestHPKE struct{}

func (appTestHPKE) Seal(recipient, info, aad, plaintext []byte) ([]byte, []byte, error) {
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	rec, err := ecdh.X25519().NewPublicKey(recipient)
	if err != nil {
		return nil, nil, err
	}
	dh, err := eph.ECDH(rec)
	if err != nil {
		return nil, nil, err
	}
	ct, err := devicesec.HPKESealWithDH(recipient, eph.PublicKey().Bytes(), dh, info, aad, plaintext)
	if err != nil {
		return nil, nil, err
	}
	return eph.PublicKey().Bytes(), ct, nil
}

func sealAppWireObj(
	t *testing.T,
	signer appTestActor,
	dom devicesec.Hash,
	st devicesec.State,
	sec, plain []byte,
	purpose devicesec.Purpose,
	machineID, sessionID, reqID string,
	seq uint64,
) *v1.Envelope {
	t.Helper()
	sh, _ := st.Hash()
	hdr := devicesec.Header{
		Account:      st.Core.Account,
		Domain:       dom,
		Epoch:        st.Core.Epoch,
		State:        sh,
		ContentEpoch: st.Core.ContentEpoch,
		Sender:       signer.id,
		Kind:         purpose.Kind(),
		Purpose:      purpose,
		Machine:      machineID,
		Session:      sessionID,
		Request:      reqID,
		Seq:          seq,
	}
	hb, key, nonce, err := devicesec.PrepareCiphertext(sec, nil, hdr, len(plain), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	blk, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(blk)
	ct, om, err := devicesec.AssembleCiphertext(hb, nonce, aead.Seal(nil, nonce, plain, hb))
	if err != nil {
		t.Fatal(err)
	}
	ct.Origin = ed25519.Sign(signer.ed, om)
	env, err := security.ToProtoEnvelope(ct)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

type appTestSecStore struct {
	blob []byte
	rev  string
}

func pumpCoreSecurityEffects(t *testing.T, c *Core, actor appTestActor, store *appTestSecStore, nowMs int64, initBatch *corev1.EffectBatch) []*corev1.Effect {
	t.Helper()
	var all []*corev1.Effect
	if initBatch != nil {
		all = append(all, initBatch.Effects...)
	}
	for i := 0; i < len(all); i++ {
		eff := all[i]
		switch k := eff.Effect.(type) {
		case *corev1.Effect_SecurityStorage:
			if store == nil {
				continue
			}
			st := k.SecurityStorage
			var rep *corev1.SecurityStorageResultEvent
			switch st.Op {
			case corev1.SecurityStorageOp_SECURITY_STORAGE_OP_READ:
				rep = &corev1.SecurityStorageResultEvent{
					OpId:     st.OpId,
					Found:    st.Bucket == corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_RECORD && len(store.blob) > 0,
					Revision: store.rev,
					Data:     store.blob,
				}
			case corev1.SecurityStorageOp_SECURITY_STORAGE_OP_WRITE_CAS:
				if st.Bucket == corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_RECORD {
					store.blob = append([]byte(nil), st.Data...)
					store.rev = st.NextRevision
				}
				rep = &corev1.SecurityStorageResultEvent{OpId: st.OpId, Revision: st.NextRevision}
			case corev1.SecurityStorageOp_SECURITY_STORAGE_OP_BATCH_COMMIT:
				for _, w := range st.Batch {
					if w.Bucket == corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_RECORD && !w.Remove {
						store.blob = append([]byte(nil), w.Data...)
						store.rev = w.NextRevision
					}
				}
				rep = &corev1.SecurityStorageResultEvent{OpId: st.OpId}
			}
			if rep != nil {
				b := c.Dispatch(&corev1.Event{
					NowMs: nowMs,
					Event: &corev1.Event_SecurityStorageResult{SecurityStorageResult: rep},
				})
				all = append(all, b.Effects...)
			}
		case *corev1.Effect_SecurityKeySign:
			sig := ed25519.Sign(actor.ed, k.SecurityKeySign.Message)
			b := c.Dispatch(&corev1.Event{
				NowMs: nowMs,
				Event: &corev1.Event_SecurityKeySigned{
					SecurityKeySigned: &corev1.SecurityKeySignedEvent{OpId: k.SecurityKeySign.OpId, Signature: sig},
				},
			})
			all = append(all, b.Effects...)
		case *corev1.Effect_SecurityKeyAgree:
			peer, err := ecdh.X25519().NewPublicKey(k.SecurityKeyAgree.PeerPublic)
			if err != nil {
				t.Fatal(err)
			}
			shared, err := actor.x.ECDH(peer)
			if err != nil {
				t.Fatal(err)
			}
			b := c.Dispatch(&corev1.Event{
				NowMs: nowMs,
				Event: &corev1.Event_SecurityKeyAgreed{
					SecurityKeyAgreed: &corev1.SecurityKeyAgreedEvent{OpId: k.SecurityKeyAgree.OpId, Shared: shared},
				},
			})
			all = append(all, b.Effects...)
		case *corev1.Effect_SecurityCryptoBatch:
			results := make([]*corev1.SecurityCryptoBatchResult, len(k.SecurityCryptoBatch.Items))
			for j, it := range k.SecurityCryptoBatch.Items {
				switch v := it.Kind.(type) {
				case *corev1.SecurityCryptoBatchItem_Verify:
					ok := len(v.Verify.PublicKey) == ed25519.PublicKeySize && ed25519.Verify(v.Verify.PublicKey, v.Verify.Message, v.Verify.Signature)
					results[j] = &corev1.SecurityCryptoBatchResult{Ok: ok}
				case *corev1.SecurityCryptoBatchItem_AeadOpen:
					blk, _ := aes.NewCipher(v.AeadOpen.Key)
					aead, _ := cipher.NewGCM(blk)
					pt, err := aead.Open(nil, v.AeadOpen.Nonce, v.AeadOpen.Ciphertext, v.AeadOpen.Aad)
					if err != nil {
						results[j] = &corev1.SecurityCryptoBatchResult{Ok: false, Error: err.Error()}
					} else {
						results[j] = &corev1.SecurityCryptoBatchResult{Ok: true, Data: pt}
					}
				case *corev1.SecurityCryptoBatchItem_AeadSeal:
					blk, _ := aes.NewCipher(v.AeadSeal.Key)
					aead, _ := cipher.NewGCM(blk)
					ct := aead.Seal(nil, v.AeadSeal.Nonce, v.AeadSeal.Plaintext, v.AeadSeal.Aad)
					results[j] = &corev1.SecurityCryptoBatchResult{Ok: true, Data: ct}
				}
			}
			b := c.Dispatch(&corev1.Event{
				NowMs: nowMs,
				Event: &corev1.Event_SecurityCryptoBatchDone{
					SecurityCryptoBatchDone: &corev1.SecurityCryptoBatchDoneEvent{OpId: k.SecurityCryptoBatch.OpId, Results: results},
				},
			})
			all = append(all, b.Effects...)
		}
	}
	return all
}

func TestScheduleCacheGateAndRevocationDuringWrite(t *testing.T) {
	nowMs := int64(1_791_000_000_000)
	phone := newAppTestActor(t, 0x11)
	trusted := newAppTestActor(t, 0x22)
	untrusted := newAppTestActor(t, 0x33)
	recActor := newAppTestActor(t, 0x44)
	dom := devicesec.Digest("sync-domain", []byte{1})
	sec := bytes.Repeat([]byte{0x44}, 32)

	u0, err := devicesec.NewGenesis(phone.id, dom, phone.member(devicesec.Client), [32]byte(recActor.edP), sec, uint64(nowMs), appTestHPKE{})
	if err != nil {
		t.Fatal(err)
	}
	sb0, _ := u0.SigningBytes()
	u0.Signature = ed25519.Sign(phone.ed, sb0)
	s0Raw, _ := u0.MarshalBinary()
	s0, _ := devicesec.ParseState(s0Raw)
	gen, _ := s0.Hash()

	u1, err := devicesec.NewMembershipState(s0, []devicesec.Member{phone.member(devicesec.Client), trusted.member(devicesec.Machine)}, nil, sec, sec, uint64(nowMs+10), appTestHPKE{}, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sb1, _ := u1.SigningBytes()
	u1.Signature = ed25519.Sign(phone.ed, sb1)
	s1Raw, _ := u1.MarshalBinary()
	s1, _ := devicesec.ParseState(s1Raw)
	lease1, _ := devicesec.NewLease(s1, uint64(nowMs+10))
	lb1, _ := lease1.SigningBytes()
	lease1.Signature = ed25519.Sign(phone.ed, lb1)
	lease1Raw, _ := lease1.MarshalBinary()

	// Epoch 2 revokes trusted machine.
	sec2 := bytes.Repeat([]byte{0x55}, 32)
	u2, err := devicesec.NewMembershipState(s1, []devicesec.Member{phone.member(devicesec.Client)}, []string{trusted.id}, sec, sec2, uint64(nowMs+20), appTestHPKE{}, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sb2, _ := u2.SigningBytes()
	u2.Signature = ed25519.Sign(phone.ed, sb2)
	s2Raw, _ := u2.MarshalBinary()
	s2, _ := devicesec.ParseState(s2Raw)
	lease2, _ := devicesec.NewLease(s2, uint64(nowMs+20))
	lb2, _ := lease2.SigningBytes()
	lease2.Signature = ed25519.Sign(phone.ed, lb2)
	lease2Raw, _ := lease2.MarshalBinary()

	c := New()
	defer c.Close()

	detEntropy := make([]byte, 4096)
	for off := 0; off < len(detEntropy); off += sha256.Size {
		var ctr [8]byte
		binary.LittleEndian.PutUint64(ctr[:], uint64(off/sha256.Size+1))
		sum := sha256.Sum256(ctr[:])
		copy(detEntropy[off:], sum[:])
	}
	c.Dispatch(&corev1.Event{
		NowMs: nowMs + 50,
		Event: &corev1.Event_EntropyProvided{
			EntropyProvided: &corev1.EntropyProvided{
				RandomIds:   []string{"r1", "r2", "r3", "r4", "r5", "r6", "r7", "r8"},
				UnitFloats:  []float64{0.5, 0.5, 0.5, 0.5},
				RandomBytes: detEntropy,
			},
		},
	})
	rec := &security.PersistedRecord{
		Pin:      devicesec.Pin{Account: phone.id, Domain: dom, Genesis: gen},
		MemberID: phone.id,
		Member:   true,
		Server:   "https://relay.example.com",
		States:   [][]byte{s0Raw, s1Raw},
		Lease:    lease1Raw,
	}
	recRaw, err := rec.Encode()
	if err != nil {
		t.Fatal(err)
	}
	c.Dispatch(&corev1.Event{
		NowMs: nowMs + 50,
		Event: &corev1.Event_SecurityConfig{
			SecurityConfig: &corev1.SecurityConfigEvent{
				Account:      phone.id,
				DeviceId:     phone.id,
				Server:       "https://relay.example.com",
				InlineCrypto: true,
				Foreground:   true,
			},
		},
	})
	bootBatch := c.Dispatch(&corev1.Event{
		NowMs: nowMs + 50,
		Event: &corev1.Event_SecurityAction{
			SecurityAction: &corev1.SecurityWorkflowActionEvent{
				ActionId: 1,
				Action:   &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
			},
		},
	})
	var storOp uint64
	for _, eff := range bootBatch.Effects {
		if st := eff.GetSecurityStorage(); st != nil {
			storOp = st.OpId
		}
	}
	secStore := &appTestSecStore{blob: recRaw, rev: "rev-1"}
	storBatch := c.Dispatch(&corev1.Event{
		NowMs: nowMs + 50,
		Event: &corev1.Event_SecurityStorageResult{
			SecurityStorageResult: &corev1.SecurityStorageResultEvent{
				OpId:        storOp,
				HasDeviceEd: true,
				HasDeviceX:  true,
				Entries: []*corev1.SecurityStorageEntry{{
					Bucket:   corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_RECORD,
					Key:      "record",
					Revision: secStore.rev,
					Data:     secStore.blob,
				}},
			},
		},
	})
	pumpCoreSecurityEffects(t, c, phone, secStore, nowMs+50, storBatch)

	// Connect sync engine and feed wire machine list with trusted + untrusted machines.
	bStart := c.Dispatch(&corev1.Event{
		NowMs:  nowMs + 60,
		MonoMs: nowMs + 60,
		Event: &corev1.Event_SyncLifecycle{
			SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START},
		},
	})
	var tokenOp uint64
	for _, eff := range bStart.Effects {
		if et := eff.GetEnsureToken(); et != nil {
			tokenOp = et.OpId
		}
	}
	bTok := c.Dispatch(&corev1.Event{
		NowMs:  nowMs + 65,
		MonoMs: nowMs + 65,
		Event:  &corev1.Event_TokenResult{TokenResult: &corev1.TokenResultEvent{OpId: tokenOp, Token: "tok-1"}},
	})
	tokEffs := pumpCoreSecurityEffects(t, c, phone, secStore, nowMs+65, bTok)
	var legID uint64
	for _, eff := range tokEffs {
		if os := eff.GetOpenStream(); os != nil {
			legID = os.LegId
		}
		if rpc := eff.GetSecurityRpc(); rpc != nil && rpc.Method == corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_SECURITY_STATES {
			emptyResp, _ := proto.Marshal(&stdv1.GetSecurityStatesResponse{HeadEpoch: 1})
			pumpCoreSecurityEffects(t, c, phone, secStore, nowMs+66, c.Dispatch(&corev1.Event{
				NowMs: nowMs + 66,
				Event: &corev1.Event_SecurityRpcResult{
					SecurityRpcResult: &corev1.SecurityRpcResultEvent{OpId: rpc.OpId, ResponseBytes: emptyResp},
				},
			}))
		}
	}
	bReady := c.Dispatch(&corev1.Event{
		NowMs:  nowMs + 70,
		MonoMs: nowMs + 70,
		Event:  &corev1.Event_StreamReady{StreamReady: &corev1.StreamReadyEvent{LegId: legID, ServerTimeMs: nowMs + 70}},
	})
	var listOp uint64
	for _, eff := range bReady.Effects {
		if rpc := eff.GetRpc(); rpc != nil && rpc.GetFetchLists() != nil {
			listOp = rpc.OpId
		}
	}
	mmRaw, _ := (&v1.MachineMeta{
		Hostname:        "TrustedMac",
		ProtocolVersion: sync.PeerVersion,
		Capabilities:    &v1.MachineCapabilities{ScheduledTasks: true},
	}).Marshal()
	trustedMetaEnv := sealAppWireObj(t, trusted, dom, s1, sec, mmRaw, devicesec.MachineMeta, trusted.id, "", "", 1)
	untrustedMetaEnv := sealAppWireObj(t, untrusted, dom, s1, sec, mmRaw, devicesec.MachineMeta, untrusted.id, "", "", 1)
	bLists := c.Dispatch(&corev1.Event{
		NowMs:  nowMs + 80,
		MonoMs: nowMs + 80,
		Event: &corev1.Event_ListsFetched{
			ListsFetched: &corev1.ListsFetchedEvent{
				OpId: listOp,
				WireMachines: []*v1.Machine{
					{Id: trusted.id, Online: true, Meta: trustedMetaEnv},
					{Id: untrusted.id, Online: true, Meta: untrustedMetaEnv},
				},
			},
		},
	})
	pumpCoreSecurityEffects(t, c, phone, secStore, nowMs+80, bLists)

	// Opening schedule list reads cache ONLY for trusted, NEVER for untrusted, and seals schedule.list command.
	openTrue := true
	bOpen := c.Dispatch(&corev1.Event{
		NowMs:  nowMs + 90,
		MonoMs: nowMs + 90,
		Event:  &corev1.Event_ScheduleListOpen{ScheduleListOpen: &corev1.ScheduleBoolAction{Value: &openTrue}},
	})
	openEffs := pumpCoreSecurityEffects(t, c, phone, secStore, nowMs+90, bOpen)
	var readMachines []string
	var dc *corev1.DispatchCommandEffect
	for _, eff := range openEffs {
		if rc := eff.GetScheduleReadCache(); rc != nil {
			readMachines = append(readMachines, rc.MachineId)
		}
		if got := eff.GetDispatchCommand(); got != nil && got.MachineId == trusted.id {
			dc = got
		}
	}
	if len(readMachines) != 1 || readMachines[0] != trusted.id {
		t.Fatalf("expected ScheduleReadCache only for trusted (%s), got %v", trusted.id, readMachines)
	}
	if dc == nil || dc.SealedEnvelope == nil {
		t.Fatal("expected sealed schedule.list DispatchCommand for trusted machine")
	}

	// Build real signed & bound CommandResult envelope (NO plaintext Result shortcut).
	openedCmd, err := security.FromProtoEnvelope(dc.SealedEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	hdr, err := devicesec.ParseHeader(openedCmd.Header)
	if err != nil {
		t.Fatal(err)
	}
	keyHash := devicesec.RequestIdentity(hdr)
	payloadHash, err := devicesec.AuthorizedPayloadHash(hdr, dc.CommandBytes)
	if err != nil {
		t.Fatal(err)
	}
	resRaw, err := (&v1.CommandResult{
		Outcome:         "executed",
		Reason:          "ok",
		RequestKeyHash:  keyHash[:],
		PayloadHash:     payloadHash[:],
		ResultAvailable: true,
		Kind: &v1.CommandResult_Schedule{
			Schedule: &v1.ScheduleResponse{ResultJson: []byte(`{"tasks":[]}`)},
		},
	}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	sealedRes := sealAppWireObj(t, trusted, dom, s1, sec, resRaw, devicesec.Result, trusted.id, "", dc.RequestId, 0)

	// Negative check: a tampered SealedResult MUST NOT be accepted or trigger ScheduleWriteCacheEffect.
	tamperedRes := &v1.Envelope{
		Header:     append([]byte(nil), sealedRes.Header...),
		Ciphertext: append([]byte(nil), sealedRes.Ciphertext...),
		Signature:  append([]byte(nil), sealedRes.Signature...),
	}
	tamperedRes.Signature[0] ^= 0xff
	bTampered := c.Dispatch(&corev1.Event{
		NowMs:  nowMs + 95,
		MonoMs: nowMs + 95,
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:    dc.RequestId,
				AttemptId:    dc.AttemptId,
				Source:       corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON,
				AttemptBound: true,
				OuterOutcome: "executed",
				OuterReason:  "ok",
				SealedResult: tamperedRes,
			},
		},
	})
	tamperedEffs := pumpCoreSecurityEffects(t, c, phone, secStore, nowMs+95, bTampered)
	for _, eff := range tamperedEffs {
		if eff.GetScheduleWriteCache() != nil {
			t.Fatal("tampered SealedResult must NEVER emit ScheduleWriteCacheEffect")
		}
	}

	// Trigger a fresh schedule.list command now that the tampered attempt settled,
	// then deliver a valid signed & bound SealedResult -> Core verifies and emits ScheduleWriteCacheEffect.
	bRefresh := c.Dispatch(&corev1.Event{
		NowMs:  nowMs + 98,
		MonoMs: nowMs + 98,
		Event:  &corev1.Event_ScheduleRefresh{ScheduleRefresh: &corev1.ScheduleStringAction{Value: trusted.id}},
	})
	refreshEffs := pumpCoreSecurityEffects(t, c, phone, secStore, nowMs+98, bRefresh)
	var dc2 *corev1.DispatchCommandEffect
	for _, eff := range refreshEffs {
		if got := eff.GetDispatchCommand(); got != nil && got.MachineId == trusted.id {
			dc2 = got
		}
	}
	if dc2 == nil || dc2.SealedEnvelope == nil {
		t.Fatal("expected second sealed schedule.list DispatchCommand after refresh")
	}
	openedCmd2, err := security.FromProtoEnvelope(dc2.SealedEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	hdr2, err := devicesec.ParseHeader(openedCmd2.Header)
	if err != nil {
		t.Fatal(err)
	}
	keyHash2 := devicesec.RequestIdentity(hdr2)
	payloadHash2, err := devicesec.AuthorizedPayloadHash(hdr2, dc2.CommandBytes)
	if err != nil {
		t.Fatal(err)
	}
	resRaw2, err := (&v1.CommandResult{
		Outcome:         "executed",
		Reason:          "ok",
		RequestKeyHash:  keyHash2[:],
		PayloadHash:     payloadHash2[:],
		ResultAvailable: true,
		Kind: &v1.CommandResult_Schedule{
			Schedule: &v1.ScheduleResponse{ResultJson: []byte(`{"tasks":[]}`)},
		},
	}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	sealedRes2 := sealAppWireObj(t, trusted, dom, s1, sec, resRaw2, devicesec.Result, trusted.id, "", dc2.RequestId, 0)

	bOut := c.Dispatch(&corev1.Event{
		NowMs:  nowMs + 100,
		MonoMs: nowMs + 100,
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:    dc2.RequestId,
				AttemptId:    dc2.AttemptId,
				Source:       corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON,
				AttemptBound: true,
				OuterOutcome: "executed",
				OuterReason:  "ok",
				SealedResult: sealedRes2,
			},
		},
	})
	outEffs := pumpCoreSecurityEffects(t, c, phone, secStore, nowMs+100, bOut)
	wroteCache := false
	for _, eff := range outEffs {
		if wc := eff.GetScheduleWriteCache(); wc != nil && wc.MachineId == trusted.id && len(wc.SealedResultBytes) > 0 {
			wroteCache = true
		}
	}
	if !wroteCache {
		t.Fatal("expected ScheduleWriteCacheEffect after real Core verification of SealedResult")
	}

	// Revoke trusted machine via signed epoch-2 SecurityStates while the cache write is in flight.
	bSync := c.Dispatch(&corev1.Event{
		NowMs:  nowMs + 110,
		MonoMs: nowMs + 110,
		Event: &corev1.Event_SecurityAction{
			SecurityAction: &corev1.SecurityWorkflowActionEvent{
				ActionId: 2,
				Action:   &corev1.SecurityWorkflowActionEvent_SyncNow{SyncNow: &corev1.SecuritySyncNowAction{Touch: false, AuthToken: "tok-1"}},
			},
		},
	})
	syncEffs := pumpCoreSecurityEffects(t, c, phone, secStore, nowMs+110, bSync)
	var rpcOp uint64
	for _, eff := range syncEffs {
		if rpc := eff.GetSecurityRpc(); rpc != nil && rpc.Method == corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_SECURITY_STATES {
			rpcOp = rpc.OpId
		}
	}
	if rpcOp == 0 {
		t.Fatal("expected GET_SECURITY_STATES RPC on security sync")
	}
	respBytes, err := proto.Marshal(&stdv1.GetSecurityStatesResponse{
		HeadEpoch: 2,
		States:    []*stdv1.SecurityState{{State: s2Raw}},
		Lease:     lease2Raw,
	})
	if err != nil {
		t.Fatal(err)
	}
	bRevoke := c.Dispatch(&corev1.Event{
		NowMs:  nowMs + 120,
		MonoMs: nowMs + 120,
		Event: &corev1.Event_SecurityRpcResult{
			SecurityRpcResult: &corev1.SecurityRpcResultEvent{
				OpId:          rpcOp,
				ResponseBytes: respBytes,
			},
		},
	})
	revokeEffs := pumpCoreSecurityEffects(t, c, phone, secStore, nowMs+120, bRevoke)
	pruneDeleted := false
	for _, eff := range revokeEffs {
		if dc := eff.GetScheduleDeleteCache(); dc != nil && dc.MachineId == trusted.id {
			pruneDeleted = true
		}
	}
	if !pruneDeleted {
		t.Fatal("expected initial ScheduleDeleteCacheEffect upon epoch-2 revocation")
	}

	// Now the in-flight cache write finishes after revocation -> Core MUST emit a second delete.
	bWritten := c.Dispatch(&corev1.Event{
		NowMs:  nowMs + 130,
		MonoMs: nowMs + 130,
		Event: &corev1.Event_ScheduleCacheWritten{
			ScheduleCacheWritten: &corev1.ScheduleCacheWrittenEvent{
				MachineId: trusted.id,
			},
		},
	})
	postWriteDeleted := false
	for _, eff := range bWritten.Effects {
		if dc := eff.GetScheduleDeleteCache(); dc != nil && dc.MachineId == trusted.id {
			postWriteDeleted = true
		}
	}
	if !postWriteDeleted {
		t.Fatal("expected second ScheduleDeleteCacheEffect when write finishes after revocation")
	}
}

func TestUsageCacheRevocationDuringWrite(t *testing.T) {
	fx := bootConnectedCore(t, 8_000, nil, nil, nil)
	c := fx.core
	c.Dispatch(&corev1.Event{
		NowMs: 8_035,
		Event: &corev1.Event_EntropyProvided{
			EntropyProvided: &corev1.EntropyProvided{
				RandomIds:  []string{"u1", "u2", "u3", "u4"},
				UnitFloats: []float64{0.5, 0.5},
			},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs:  8_040,
		MonoMs: 8_040,
		Event: &corev1.Event_SeedDemoWorld{
			SeedDemoWorld: &corev1.SeedDemoWorldEvent{
				Security: &corev1.DemoSecurity{
					SelfDeviceId:      "self-1",
					IsMember:          true,
					TrustedMachineIds: []string{"m-usage"},
				},
				Machines: []*corev1.DemoMachine{
					{
						MachineId: "m-usage",
						Online:    true,
						Meta: &v1.MachineMeta{
							Hostname:        "UsageMac",
							ProtocolVersion: sync.PeerVersion,
							Capabilities:    &v1.MachineCapabilities{UsageStats: true},
						},
					},
				},
			},
		},
	})

	b := c.Dispatch(&corev1.Event{
		NowMs:  8_050,
		MonoMs: 8_050,
		Event: &corev1.Event_UsageScreenOpen{
			UsageScreenOpen: &corev1.UsageScreenOpenEvent{
				MachineId: "m-usage",
				Preset:    v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS,
			},
		},
	})
	var dc *corev1.DispatchCommandEffect
	for _, eff := range b.Effects {
		if got := eff.GetDispatchCommand(); got != nil && got.MachineId == "m-usage" {
			dc = got
		}
	}
	if dc == nil {
		t.Fatal("expected GetUsageStats DispatchCommand for m-usage")
	}

	b = c.Dispatch(&corev1.Event{
		NowMs:  8_060,
		MonoMs: 8_060,
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
				Result: &v1.CommandResult{
					Outcome:         "executed",
					Reason:          "ok",
					ResultAvailable: true,
					Kind: &v1.CommandResult_UsageStats{
						UsageStats: &v1.UsageStatsResponse{
							Preset:        v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS,
							GeneratedAtMs: 8_060,
						},
					},
				},
			},
		},
	})
	wroteCache := false
	for _, eff := range b.Effects {
		if wc := eff.GetUsageCacheWrite(); wc != nil && wc.MachineId == "m-usage" {
			wroteCache = true
		}
	}
	if !wroteCache {
		t.Fatal("expected UsageCacheWriteEffect")
	}

	// Revoke m-usage while write is in flight.
	b = c.Dispatch(&corev1.Event{
		NowMs:  8_070,
		MonoMs: 8_070,
		Event: &corev1.Event_SeedDemoWorld{
			SeedDemoWorld: &corev1.SeedDemoWorldEvent{
				Security: &corev1.DemoSecurity{
					SelfDeviceId:      "self-1",
					IsMember:          true,
					TrustedMachineIds: []string{},
				},
				Machines: []*corev1.DemoMachine{{MachineId: "m-usage", Online: true}},
			},
		},
	})
	pruneDeleted := false
	for _, eff := range b.Effects {
		if u := eff.GetUsageCacheRequest(); u != nil && u.DeleteCache && u.MachineId == "m-usage" {
			pruneDeleted = true
		}
	}
	if !pruneDeleted {
		t.Fatal("expected initial UsageCacheRequestEffect(delete) on revocation")
	}

	// In-flight usage write finishes after revocation -> Core MUST emit a second delete.
	b = c.Dispatch(&corev1.Event{
		NowMs:  8_080,
		MonoMs: 8_080,
		Event: &corev1.Event_UsageCacheWritten{
			UsageCacheWritten: &corev1.UsageCacheWrittenEvent{
				MachineId: "m-usage",
			},
		},
	})
	postWriteDeleted := false
	for _, eff := range b.Effects {
		if u := eff.GetUsageCacheRequest(); u != nil && u.DeleteCache && u.MachineId == "m-usage" {
			postWriteDeleted = true
		}
	}
	if !postWriteDeleted {
		t.Fatal("expected second UsageCacheRequestEffect(delete) when usage write finishes after revocation")
	}
}

func TestPreSecurityScheduleCacheEvictedWhenMachineUntrusted(t *testing.T) {
	c := New()
	defer c.Close()

	// Cold start before security loads: cached machine + cached schedules load offline.
	b := c.Dispatch(&corev1.Event{
		NowMs:  9_000,
		MonoMs: 9_000,
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
		NowMs:  9_010,
		MonoMs: 9_010,
		Event: &corev1.Event_CacheBootstrapLoaded{
			CacheBootstrapLoaded: &corev1.CacheBootstrapLoadedEvent{
				OpId: bootOp,
				Machines: []*corev1.PlainMachine{
					{
						MachineId: "m-cold",
						Online:    false,
						Meta: &v1.MachineMeta{
							Hostname:        "ColdMac",
							ProtocolVersion: sync.PeerVersion,
							Capabilities:    &v1.MachineCapabilities{ScheduledTasks: true},
						},
					},
				},
			},
		},
	})
	openTrue := true
	b = c.Dispatch(&corev1.Event{
		NowMs:  9_020,
		MonoMs: 9_020,
		Event:  &corev1.Event_ScheduleListOpen{ScheduleListOpen: &corev1.ScheduleBoolAction{Value: &openTrue}},
	})
	var readOp uint64
	for _, eff := range b.Effects {
		if rc := eff.GetScheduleReadCache(); rc != nil && rc.MachineId == "m-cold" {
			readOp = rc.OpId
		}
	}
	if readOp == 0 {
		t.Fatal("expected ScheduleReadCacheEffect before security loads")
	}
	c.Dispatch(&corev1.Event{
		NowMs:  9_030,
		MonoMs: 9_030,
		Event: &corev1.Event_ScheduleCacheLoaded{
			ScheduleCacheLoaded: &corev1.ScheduleCacheLoadedEvent{
				OpId:       readOp,
				MachineId:  "m-cold",
				ResultJson: []byte(`{"tasks":[{"id":"t-secret","spec":{"title":"Secret cached task"}}]}`),
			},
		},
	})
	vmBefore, err := c.View(&corev1.Screen{Screen: &corev1.Screen_Schedules{Schedules: &corev1.SchedulesScreenRequest{EndedLimit: 10}}})
	if err != nil {
		t.Fatal(err)
	}
	totalTasks := func(vm *corev1.SchedulesViewModel) int {
		if vm == nil {
			return 0
		}
		return len(vm.Attention) + len(vm.Fresh) + len(vm.Others) + len(vm.Ended)
	}
	if totalTasks(vmBefore.GetSchedules()) == 0 {
		t.Fatal("expected pre-security cached schedule task visible in SchedulesViewModel")
	}

	// Security loads and marks m-cold untrusted -> in-memory snapshot MUST be evicted immediately!
	b = c.Dispatch(&corev1.Event{
		NowMs:  9_040,
		MonoMs: 9_040,
		Event: &corev1.Event_SeedDemoWorld{
			SeedDemoWorld: &corev1.SeedDemoWorldEvent{
				Security: &corev1.DemoSecurity{
					SelfDeviceId:      "self-1",
					IsMember:          true,
					TrustedMachineIds: []string{},
				},
			},
		},
	})
	sawDelete := false
	for _, eff := range b.Effects {
		if dc := eff.GetScheduleDeleteCache(); dc != nil && dc.MachineId == "m-cold" {
			sawDelete = true
		}
		if ls := eff.GetScheduleLoadSeen(); ls != nil && ls.MachineId == "m-cold" {
			t.Fatal("reconcileSchedules must not re-load seen state for untrusted m-cold")
		}
		if dc := eff.GetDispatchCommand(); dc != nil && dc.MachineId == "m-cold" {
			t.Fatal("reconcileSchedules must not dispatch schedule.list for untrusted m-cold")
		}
	}
	if !sawDelete {
		t.Fatal("expected ScheduleDeleteCacheEffect when security marks m-cold untrusted")
	}
	if _, exists := c.m.state.Schedules.Machines["m-cold"]; exists {
		t.Fatal("expected m-cold schedule snapshot absent from memory after eviction")
	}
	vmAfter, err := c.View(&corev1.Screen{Screen: &corev1.Screen_Schedules{Schedules: &corev1.SchedulesScreenRequest{EndedLimit: 10}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := totalTasks(vmAfter.GetSchedules()); got != 0 {
		t.Fatalf("expected pre-security cached tasks evicted from memory when machine untrusted, got %d", got)
	}
}
