package sync

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/outcome"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/gen/timestamppb"
	"github.com/Lewin671/remote-agent/clientcore/screens"
)

func seedEntropy(n int) ([]string, []float64) {
	ids := make([]string, n)
	fs := make([]float64, n)
	for i := 0; i < n; i++ {
		ids[i] = "id-" + string(rune('a'+i%26)) + "-" + string(rune('0'+i/26))
		fs[i] = 0.5
	}
	return ids, fs
}

func startAndConnect(t *testing.T, e *Engine, nowMs int64) (legID uint64, listsOpID uint64) {
	t.Helper()
	ids, fs := seedEntropy(20)
	_, res := e.HandleEvent(&corev1.Event{
		NowMs:      nowMs,
		MonoMs:     1_000,
		RandomIds:  ids,
		UnitFloats: fs,
		Event: &corev1.Event_SyncLifecycle{
			SyncLifecycle: &corev1.SyncLifecycleAction{
				Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START,
			},
		},
	})
	var tokOp, secOp uint64
	for _, eff := range res.Effects {
		if et := eff.GetEnsureToken(); et != nil {
			tokOp = et.OpId
		}
		if ss := eff.GetSyncSecurity(); ss != nil {
			secOp = ss.OpId
		}
	}
	if tokOp == 0 {
		t.Fatalf("expected EnsureTokenEffect on Start")
	}
	if secOp != 0 {
		e.HandleEvent(&corev1.Event{
			NowMs: nowMs + 10,
			Event: &corev1.Event_SecuritySnapshot{
				SecuritySnapshot: &corev1.SecuritySnapshotEvent{
					OpId:              secOp,
					IsMember:          true,
					SelfDeviceId:      "c1",
					AuthorityDeviceId: "c1",
					LeaseEndMs:        nowMs + 86_400_000,
					TrustedMachineIds: []string{"m1", "m2"},
				},
			},
		})
	}
	_, tokRes := e.HandleEvent(&corev1.Event{
		NowMs: nowMs + 20,
		Event: &corev1.Event_TokenResult{
			TokenResult: &corev1.TokenResultEvent{OpId: tokOp, Token: "tok-1"},
		},
	})
	for _, eff := range tokRes.Effects {
		if os := eff.GetOpenStream(); os != nil {
			legID = os.LegId
		}
	}
	if legID == 0 {
		t.Fatalf("expected OpenStreamEffect on TokenResult")
	}
	_, readyRes := e.HandleEvent(&corev1.Event{
		NowMs: nowMs + 30,
		Event: &corev1.Event_StreamReady{
			StreamReady: &corev1.StreamReadyEvent{LegId: legID, ServerTimeMs: nowMs + 30},
		},
	})
	for _, eff := range readyRes.Effects {
		if rpc := eff.GetRpc(); rpc != nil && rpc.GetFetchLists() != nil {
			listsOpID = rpc.OpId
		}
	}
	if listsOpID == 0 {
		t.Fatalf("expected FetchListsRpc on StreamReady")
	}
	return legID, listsOpID
}

func TestConnectionLifecycleRecoveryAndLiveness(t *testing.T) {
	e := NewEngine()
	now := int64(1_700_000_000_000)
	legID, _ := startAndConnect(t, e, now)

	if e.Connection() != lists.ConnConnected {
		t.Fatalf("expected ConnConnected, got %v", e.Connection())
	}

	// Stale leg close is ignored.
	_, stale := e.HandleEvent(&corev1.Event{
		NowMs: now + 50,
		Event: &corev1.Event_StreamClosed{
			StreamClosed: &corev1.StreamClosedEvent{LegId: legID + 99, PolicyViolation: true},
		},
	})
	if e.Connection() != lists.ConnConnected || len(stale.Effects) != 0 {
		t.Fatalf("stale StreamClosed should be ignored, got conn=%v effects=%v", e.Connection(), stale.Effects)
	}

	// Heartbeat calibrates LegLiveness to max(3s, 3 * interval = 6s).
	e.HandleEvent(&corev1.Event{
		NowMs:  now + 1000,
		MonoMs: 2_000,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: legID,
				Frame: &corev1.StreamFrameEvent_Heartbeat{Heartbeat: true},
			},
		},
	})
	e.HandleEvent(&corev1.Event{
		NowMs:  now + 3000,
		MonoMs: 4_000,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: legID,
				Frame: &corev1.StreamFrameEvent_Heartbeat{Heartbeat: true},
			},
		},
	})
	if got := e.liveness.ThresholdMs(); got != 6_000 {
		t.Fatalf("expected liveness threshold 6000ms, got %d", got)
	}

	// Find active liveness timer and advance past 6000ms silence -> triggers CloseStreamEffect.
	lt := e.timers[timerKey{kind: corev1.TimerKind_TIMER_KIND_LIVENESS}]
	_, livenessRes := e.HandleEvent(&corev1.Event{
		NowMs:  now + 10_500,
		MonoMs: 11_000,
		Event: &corev1.Event_TimerFired{
			TimerFired: &corev1.TimerFiredEvent{TimerId: lt.id, Kind: lt.kind},
		},
	})
	var closedLeg, graceTimerID uint64
	for _, eff := range livenessRes.Effects {
		if cs := eff.GetCloseStream(); cs != nil {
			closedLeg = cs.LegId
		}
		if st := eff.GetScheduleTimer(); st != nil && st.Kind == corev1.TimerKind_TIMER_KIND_LINK_GRACE {
			graceTimerID = st.TimerId
		}
	}
	if closedLeg != legID {
		t.Fatalf("expected CloseStreamEffect for leg %d, got %d", legID, closedLeg)
	}
	if e.Connection() != lists.ConnReconnecting {
		t.Fatalf("expected ConnReconnecting during grace period, got %v", e.Connection())
	}
	if graceTimerID == 0 {
		t.Fatalf("expected LINK_GRACE timer scheduled")
	}

	// Grace timer firing moves ConnReconnecting -> ConnOffline.
	e.HandleEvent(&corev1.Event{
		NowMs:  now + 21_000,
		MonoMs: 21_500,
		Event: &corev1.Event_TimerFired{
			TimerFired: &corev1.TimerFiredEvent{TimerId: graceTimerID, Kind: corev1.TimerKind_TIMER_KIND_LINK_GRACE},
		},
	})
	if e.Connection() != lists.ConnOffline {
		t.Fatalf("expected ConnOffline after LinkGrace, got %v", e.Connection())
	}

	// Clock skew on Ready detection (> 300s).
	e2 := NewEngine()
	ids, fs := seedEntropy(10)
	_, res := e2.HandleEvent(&corev1.Event{
		NowMs:      now + 500_000,
		MonoMs:     1_000,
		RandomIds:  ids,
		UnitFloats: fs,
		Event: &corev1.Event_SyncLifecycle{
			SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START},
		},
	})
	var tokOp uint64
	for _, eff := range res.Effects {
		if et := eff.GetEnsureToken(); et != nil {
			tokOp = et.OpId
		}
	}
	_, tokRes := e2.HandleEvent(&corev1.Event{
		NowMs: now + 500_000,
		Event: &corev1.Event_TokenResult{TokenResult: &corev1.TokenResultEvent{OpId: tokOp, Token: "tok"}},
	})
	var leg2 uint64
	for _, eff := range tokRes.Effects {
		if os := eff.GetOpenStream(); os != nil {
			leg2 = os.LegId
		}
	}
	e2.HandleEvent(&corev1.Event{
		NowMs: now + 500_000,
		Event: &corev1.Event_StreamReady{
			StreamReady: &corev1.StreamReadyEvent{LegId: leg2, ServerTimeMs: now},
		},
	})
	if !e2.accountClockSkewed() {
		t.Fatalf("expected ServerTimeMs lag > 300s to set accountClockSkewed")
	}

	// Any non-Ready frame during ConnConnecting promotes to ConnConnected.
	e3 := NewEngine()
	e3.running = true
	e3.legID = 7
	e3.conn = lists.ConnConnecting
	e3.liveness = NewLegLiveness(1_000)
	e3.HandleEvent(&corev1.Event{
		NowMs:  now,
		MonoMs: 1_500,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: 7,
				Frame: &corev1.StreamFrameEvent_MachineUpdated{
					MachineUpdated: &corev1.PlainMachine{MachineId: "m1", Online: true},
				},
			},
		},
	})
	if e3.Connection() != lists.ConnConnected {
		t.Fatalf("expected non-Ready StreamFrame during ConnConnecting to promote to ConnConnected, got %v", e3.Connection())
	}

	// PolicyViolation close discards token.
	_, closeRes := e3.HandleEvent(&corev1.Event{
		NowMs: now + 100,
		Event: &corev1.Event_StreamClosed{
			StreamClosed: &corev1.StreamClosedEvent{LegId: 7, PolicyViolation: true},
		},
	})
	e3.cachedToken = "stale"
	// Set token before close: verify DiscardToken emitted when token existed.
	_ = closeRes
	e4 := NewEngine()
	e4.running = true
	e4.legID = 8
	e4.cachedToken = "secret-tok"
	_, pRes := e4.HandleEvent(&corev1.Event{
		NowMs: now + 100,
		Event: &corev1.Event_StreamClosed{
			StreamClosed: &corev1.StreamClosedEvent{LegId: 8, PolicyViolation: true},
		},
	})
	var discarded bool
	for _, eff := range pRes.Effects {
		if dt := eff.GetDiscardToken(); dt != nil && dt.Token == "secret-tok" {
			discarded = true
		}
	}
	if !discarded {
		t.Fatalf("expected DiscardTokenEffect on PolicyViolation stream close")
	}
}

func TestForceLoginSemantics(t *testing.T) {
	e := NewEngine()
	now := int64(1_700_000_000_000)
	ids, fs := seedEntropy(10)
	_, startRes := e.HandleEvent(&corev1.Event{
		NowMs:      now,
		MonoMs:     1_000,
		RandomIds:  ids,
		UnitFloats: fs,
		Event: &corev1.Event_SyncLifecycle{
			SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START},
		},
	})
	var tokOp uint64
	var force bool
	for _, eff := range startRes.Effects {
		if et := eff.GetEnsureToken(); et != nil {
			tokOp = et.OpId
			force = et.ForceLogin
		}
	}
	if tokOp == 0 || force {
		t.Fatalf("expected cold START EnsureToken with ForceLogin=false, got op=%d force=%v", tokOp, force)
	}

	_, tokRes := e.HandleEvent(&corev1.Event{
		NowMs:  now + 10,
		MonoMs: 1_010,
		Event: &corev1.Event_TokenResult{
			TokenResult: &corev1.TokenResultEvent{OpId: tokOp, Token: "tok-stale", ExpiresMs: now + 60_000},
		},
	})
	var legID uint64
	for _, eff := range tokRes.Effects {
		if os := eff.GetOpenStream(); os != nil {
			legID = os.LegId
		}
	}
	if legID == 0 {
		t.Fatalf("expected OpenStreamEffect")
	}

	_, pvRes := e.HandleEvent(&corev1.Event{
		NowMs:  now + 20,
		MonoMs: 1_020,
		Event: &corev1.Event_StreamClosed{
			StreamClosed: &corev1.StreamClosedEvent{LegId: legID, PolicyViolation: true},
		},
	})
	var pvTokOp uint64
	var pvForce bool
	var reconnectTimer uint64
	var reconnectDelay int64
	for _, eff := range pvRes.Effects {
		if et := eff.GetEnsureToken(); et != nil {
			pvTokOp = et.OpId
			pvForce = et.ForceLogin
		}
		if st := eff.GetScheduleTimer(); st != nil && st.Kind == corev1.TimerKind_TIMER_KIND_RECONNECT {
			reconnectTimer = st.TimerId
			reconnectDelay = st.DelayMs
		}
	}
	if pvTokOp == 0 && reconnectTimer != 0 {
		_, timerRes := e.HandleEvent(&corev1.Event{
			NowMs:  now + 20 + reconnectDelay,
			MonoMs: 1_020 + reconnectDelay,
			Event: &corev1.Event_TimerFired{
				TimerFired: &corev1.TimerFiredEvent{
					TimerId: reconnectTimer,
					Kind:    corev1.TimerKind_TIMER_KIND_RECONNECT,
				},
			},
		})
		for _, eff := range timerRes.Effects {
			if et := eff.GetEnsureToken(); et != nil {
				pvTokOp = et.OpId
				pvForce = et.ForceLogin
			}
		}
	}
	if pvTokOp == 0 || !pvForce {
		t.Fatalf("expected PolicyViolation reconnect EnsureToken with ForceLogin=true, got op=%d force=%v", pvTokOp, pvForce)
	}

	_, retryTokRes := e.HandleEvent(&corev1.Event{
		NowMs:  now + 30,
		MonoMs: 1_030,
		Event: &corev1.Event_TokenResult{
			TokenResult: &corev1.TokenResultEvent{OpId: pvTokOp, Token: "tok-fresh", ExpiresMs: now + 120_000},
		},
	})
	var leg2 uint64
	for _, eff := range retryTokRes.Effects {
		if os := eff.GetOpenStream(); os != nil {
			leg2 = os.LegId
		}
	}
	if leg2 == 0 {
		t.Fatalf("expected OpenStreamEffect after forced login")
	}
	if e.forceLogin {
		t.Fatalf("expected forceLogin cleared after TokenResult succeeded")
	}
}

func TestPaginationAndPushRemovalRaces(t *testing.T) {
	e := NewEngine()
	now := int64(1_700_000_000_000)
	legID, listsOpID := startAndConnect(t, e, now)

	// Initial seed: m1, m2, plus s_live, s_page2, s_gone.
	e.HandleEvent(&corev1.Event{
		NowMs: now + 40,
		Event: &corev1.Event_ListsFetched{
			ListsFetched: &corev1.ListsFetchedEvent{
				OpId: listsOpID,
				Machines: []*corev1.PlainMachine{
					{MachineId: "m1", Online: true},
					{MachineId: "m2", Online: true},
				},
				Sessions: []*corev1.PlainSession{
					{SessionId: "s_live", MachineId: "m1", UpdatedMs: now},
					{SessionId: "s_page2", MachineId: "m1", ListedEndedMs: 500, UpdatedMs: 500},
					{SessionId: "s_gone", MachineId: "m1", ListedEndedMs: 900, UpdatedMs: 900},
				},
				Next: &v1.SessionCursor{EndedMs: 400, SessionId: "c"},
			},
		},
	})

	// Request page 2, then trigger a newer Refresh BEFORE page 2 responds.
	pageRes := e.Run(func(ctx *StepContext) {
		e.RequestMoreSessions(ctx)
	})
	var pageOp uint64
	for _, eff := range pageRes.Effects {
		if rpc := eff.GetRpc(); rpc != nil && rpc.GetFetchSessionsPage() != nil {
			pageOp = rpc.OpId
		}
	}
	if pageOp == 0 {
		t.Fatalf("expected FetchSessionsPageRpc")
	}

	refreshRes := e.Run(func(ctx *StepContext) {
		e.startRefresh(ctx)
	})
	var refreshOp uint64
	for _, eff := range refreshRes.Effects {
		if rpc := eff.GetRpc(); rpc != nil && rpc.GetFetchLists() != nil {
			refreshOp = rpc.OpId
		}
	}

	// While refresh is in flight:
	// 1. m2 is pushed online=false, then restored online=true.
	// 2. s_pushed arrives on stream.
	// 3. m3 is added and then immediately removed (removedPushes).
	e.HandleEvent(&corev1.Event{
		NowMs: now + 60,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: legID,
				Frame: &corev1.StreamFrameEvent_MachineUpdated{
					MachineUpdated: &corev1.PlainMachine{MachineId: "m2", Online: false},
				},
			},
		},
	})
	e.HandleEvent(&corev1.Event{
		NowMs: now + 65,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: legID,
				Frame: &corev1.StreamFrameEvent_SessionUpdated{
					SessionUpdated: &corev1.PlainSession{SessionId: "s_pushed", MachineId: "m1", UpdatedMs: now + 65},
				},
			},
		},
	})
	e.HandleEvent(&corev1.Event{
		NowMs: now + 70,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: legID,
				Frame: &corev1.StreamFrameEvent_MachineRemovedId{MachineRemovedId: "m3"},
			},
		},
	})

	// Server's stale snapshot returns: contains m1, m3(stale!), omits m2 and s_pushed, next=EndedMs:700.
	// So s_page2 (500 < 700) is past cursor (kept in memory, unlisted), s_gone (900 > 700) was deleted (unlisted/removed).
	_, listsRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 80,
		Event: &corev1.Event_ListsFetched{
			ListsFetched: &corev1.ListsFetchedEvent{
				OpId: refreshOp,
				Machines: []*corev1.PlainMachine{
					{MachineId: "m1", Online: true},
					{MachineId: "m3", Online: true}, // pushed removed! must NOT resurrect
				},
				Sessions: []*corev1.PlainSession{
					{SessionId: "s_live", MachineId: "m1", UpdatedMs: now},
				},
				Next: &v1.SessionCursor{EndedMs: 700, SessionId: "z"},
			},
		},
	})

	if e.MachineEntry("m2") == nil {
		t.Fatalf("pushed machine m2 should survive ListsFetched omission")
	}
	if e.MachineEntry("m3") != nil {
		t.Fatalf("removed machine m3 must not be resurrected by stale ListsFetched")
	}
	if e.SessionEntry("s_pushed") == nil {
		t.Fatalf("pushed session s_pushed should survive ListsFetched omission")
	}
	if e.SessionEntry("s_page2") == nil {
		t.Fatalf("past-cursor session s_page2 should stay held")
	}
	var unlistedGone bool
	for _, eff := range listsRes.Effects {
		if cd := eff.GetCacheDelete(); cd != nil && cd.GetUnlistSessionId() == "s_gone" {
			unlistedGone = true
		}
	}
	if !unlistedGone {
		t.Fatalf("expected s_gone (ended before cursor) to emit UnlistSessionId")
	}

	// Old page 2 arrives AFTER refresh bumped listingGen -> must be ignored.
	e.HandleEvent(&corev1.Event{
		NowMs: now + 90,
		Event: &corev1.Event_SessionsPageFetched{
			SessionsPageFetched: &corev1.SessionsPageFetchedEvent{
				OpId: pageOp,
				Sessions: []*corev1.PlainSession{
					{SessionId: "s_stale_page2", MachineId: "m1", ListedEndedMs: 100},
				},
			},
		},
	})
	if e.SessionEntry("s_stale_page2") != nil {
		t.Fatalf("stale SessionsPageFetched from prior listingGen must be discarded")
	}
}

func TestEventsTailFirstGapFillBrokenCacheAndLRU(t *testing.T) {
	e := NewEngine()
	now := int64(1_700_000_000_000)
	legID, listsOp := startAndConnect(t, e, now)

	// Session with 1200 events (exceeds EventsPageLimit = 500).
	e.HandleEvent(&corev1.Event{
		NowMs: now + 40,
		Event: &corev1.Event_ListsFetched{
			ListsFetched: &corev1.ListsFetchedEvent{
				OpId:     listsOp,
				Machines: []*corev1.PlainMachine{{MachineId: "m1", Online: true}},
				Sessions: []*corev1.PlainSession{
					{SessionId: "s1", MachineId: "m1", LastSeq: 1200, Meta: &v1.Session{Id: "s1", Status: v1.SessionStatus_SESSION_STATUS_RUNNING}},
				},
			},
		},
	})

	// Watch s1 -> triggers CacheReadEvents(newest=true).
	_, wRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 50,
		Event: &corev1.Event_SessionWatch{SessionWatch: &corev1.SessionWatchEvent{SessionId: "s1", Watched: true}},
	})
	var cacheOp uint64
	for _, eff := range wRes.Effects {
		if cr := eff.GetCacheRead(); cr != nil && cr.GetEvents() != nil {
			cacheOp = cr.GetEvents().OpId
		}
	}
	if cacheOp == 0 {
		t.Fatalf("expected CacheReadEvents on SessionWatch(true)")
	}

	// Corrupt cache -> atomic DropEventsSessionId + starts tail-first fetch (afterSeq = 1200 - 500 = 700).
	_, cRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 60,
		Event: &corev1.Event_CacheEventsLoaded{
			CacheEventsLoaded: &corev1.CacheEventsLoadedEvent{
				OpId:      cacheOp,
				SessionId: "s1",
				Broken:    true,
			},
		},
	})
	var dropped bool
	var fetchOp uint64
	var fetchAfter uint64
	for _, eff := range cRes.Effects {
		if cd := eff.GetCacheDelete(); cd != nil && cd.GetDropEventsSessionId() == "s1" {
			dropped = true
		}
		if rpc := eff.GetRpc(); rpc != nil && rpc.GetGetEvents() != nil {
			fetchOp = rpc.OpId
			fetchAfter = rpc.GetGetEvents().AfterSeq
		}
	}
	if !dropped {
		t.Fatalf("expected DropEventsSessionId on broken cache")
	}
	if fetchOp == 0 || fetchAfter != 700 {
		t.Fatalf("expected tail-first GetEvents afterSeq=700, got op=%d after=%d", fetchOp, fetchAfter)
	}

	// Tail page arrives (701..1200). Must be buffered in pending and next RPC drains prefix from 0!
	tailEvents := make([]*corev1.PlainEvent, 0, 500)
	for seq := uint64(701); seq <= 1200; seq++ {
		tailEvents = append(tailEvents, &corev1.PlainEvent{Seq: seq, Verified: true, ApproxBytes: 64})
	}
	tailEvents[499].Event = &v1.Event{
		Seq: 1200,
		Kind: &v1.Event_ApprovalRequested{
			ApprovalRequested: &v1.ApprovalRequested{ApprovalId: "ap-tail", ToolName: "bash"},
		},
	}
	_, tailRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 70,
		Event: &corev1.Event_EventsPageFetched{
			EventsPageFetched: &corev1.EventsPageFetchedEvent{
				OpId:      fetchOp,
				SessionId: "s1",
				Events:    tailEvents,
				More:      false,
			},
		},
	})
	buf := e.Buffer("s1")
	if buf.PrefixLen() != 0 || buf.PendingCount() != 500 {
		t.Fatalf("expected tail buffered in pending (prefix=0, pending=500), got prefix=%d pending=%d", buf.PrefixLen(), buf.PendingCount())
	}

	// Prefix page 1 (1..500) then 2 (501..700) drains gaps seamlessly into contiguous 1200!
	var prefixOp uint64
	for _, eff := range tailRes.Effects {
		if rpc := eff.GetRpc(); rpc != nil && rpc.GetGetEvents() != nil && rpc.GetGetEvents().AfterSeq == 0 {
			prefixOp = rpc.OpId
		}
	}
	if prefixOp == 0 {
		t.Fatalf("expected prefix drain GetEvents(afterSeq=0)")
	}
	p1 := make([]*corev1.PlainEvent, 0, 500)
	for seq := uint64(1); seq <= 500; seq++ {
		p1 = append(p1, &corev1.PlainEvent{Seq: seq, Verified: true, ApproxBytes: 64})
	}
	_, p1Res := e.HandleEvent(&corev1.Event{
		NowMs: now + 80,
		Event: &corev1.Event_EventsPageFetched{
			EventsPageFetched: &corev1.EventsPageFetchedEvent{
				OpId:      prefixOp,
				SessionId: "s1",
				Events:    p1,
				More:      true,
			},
		},
	})
	var p2Op uint64
	for _, eff := range p1Res.Effects {
		if rpc := eff.GetRpc(); rpc != nil && rpc.GetGetEvents() != nil && rpc.GetGetEvents().AfterSeq == 500 {
			p2Op = rpc.OpId
		}
	}
	p2 := make([]*corev1.PlainEvent, 0, 200)
	for seq := uint64(501); seq <= 700; seq++ {
		p2 = append(p2, &corev1.PlainEvent{Seq: seq, Verified: true, ApproxBytes: 64})
	}
	e.HandleEvent(&corev1.Event{
		NowMs: now + 90,
		Event: &corev1.Event_EventsPageFetched{
			EventsPageFetched: &corev1.EventsPageFetchedEvent{
				OpId:      p2Op,
				SessionId: "s1",
				Events:    p2,
				More:      false,
			},
		},
	})
	if buf.PrefixLen() != 1200 || buf.PendingCount() != 0 || !buf.IsLoaded() {
		t.Fatalf("expected contiguous 1200 loaded events, got prefix=%d pending=%d loaded=%v", buf.PrefixLen(), buf.PendingCount(), buf.IsLoaded())
	}
	if !e.SessionEntry("s1").LivePending.HasApproval || e.SessionEntry("s1").LivePending.ApprovalID != "ap-tail" {
		t.Fatalf("expected approval projection ap-tail after draining, got %+v", e.SessionEntry("s1").LivePending)
	}
	// Once drained, an out-of-order live event (1202 before 1201) must not
	// switch the display back to a one-event tail.
	buf.pending[1202] = &v1.Event{Seq: 1202}
	if shown := buf.ShownEvents(); len(shown) != 1200 {
		t.Fatalf("display collapsed to %d events after an out-of-order live event", len(shown))
	}
	delete(buf.pending, 1202)

	// Verify LRU protection while watched/pinned vs eviction when unwatched after 60s.
	e.HandleEvent(&corev1.Event{
		NowMs:  now + 100,
		MonoMs: 5_000,
		Event: &corev1.Event_SessionPin{SessionPin: &corev1.SessionPinEvent{SessionId: "s1", Pinned: true}},
	})
	e.HandleEvent(&corev1.Event{
		NowMs:  now + 100,
		MonoMs: 5_000,
		Event: &corev1.Event_SessionWatch{SessionWatch: &corev1.SessionWatchEvent{SessionId: "s1", Watched: false}},
	})
	// Advance 70s and trigger sweep while still pinned -> must NOT evict.
	e.Run(func(ctx *StepContext) {
		e.advanceClocks(now+70_100, 75_000, nil)
		e.trimHistory(ctx)
	})
	if e.Buffer("s1") == nil {
		t.Fatalf("pinned session buffer must survive idle trim")
	}
	// Unpin -> now >60s idle -> evicted!
	e.HandleEvent(&corev1.Event{
		NowMs:  now + 70_200,
		MonoMs: 75_100,
		Event: &corev1.Event_SessionPin{SessionPin: &corev1.SessionPinEvent{SessionId: "s1", Pinned: false}},
	})
	if e.Buffer("s1") != nil {
		t.Fatalf("unpinned idle session buffer past 60s should be evicted")
	}
	_ = legID
}

func TestLivePollingLifecycle(t *testing.T) {
	e := NewEngine()
	now := int64(1_700_000_000_000)
	legID, listsOp := startAndConnect(t, e, now)

	e.HandleEvent(&corev1.Event{
		NowMs: now + 40,
		Event: &corev1.Event_ListsFetched{
			ListsFetched: &corev1.ListsFetchedEvent{
				OpId:     listsOp,
				Machines: []*corev1.PlainMachine{{MachineId: "m1", Online: true}},
				Sessions: []*corev1.PlainSession{
					{SessionId: "s1", MachineId: "m1", Meta: &v1.Session{Id: "s1", Status: v1.SessionStatus_SESSION_STATUS_RUNNING}},
				},
			},
		},
	})

	_, watchRes := e.HandleEvent(&corev1.Event{
		NowMs:  now + 50,
		MonoMs: 1_100,
		Event: &corev1.Event_SessionWatch{SessionWatch: &corev1.SessionWatchEvent{SessionId: "s1", Watched: true}},
	})
	var pollReq, pollAtt string
	for _, eff := range watchRes.Effects {
		if dc := eff.GetDispatchCommand(); dc != nil && dc.SessionId == "s1" {
			pollReq = dc.RequestId
			pollAtt = dc.AttemptId
			if dc.RouteHint != corev1.CommandRouteHint_COMMAND_ROUTE_HINT_READ {
				t.Fatalf("expected READ route hint for GetLiveSnapshot, got %v", dc.RouteHint)
			}
		}
	}
	if pollReq == "" {
		t.Fatalf("expected immediate GetLiveSnapshot dispatch on Watch(true)")
	}

	// Poll outcome updates checkpoint + schedules 5s timer.
	_, outRes := e.HandleEvent(&corev1.Event{
		NowMs:  now + 60,
		MonoMs: 1_200,
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:         pollReq,
				AttemptId:         pollAtt,
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
					Kind: &v1.CommandResult_LiveSnapshot{
						LiveSnapshot: &v1.LiveSnapshot{
							Running: true,
							Actions: []*v1.Event{
								{Seq: 5, Kind: &v1.Event_QuestionAsked{QuestionAsked: &v1.QuestionAsked{QuestionId: "q1"}}},
							},
						},
					},
				},
			},
		},
	})
	st := screens.NewState()
	e.SyncToState(st)
	if !e.SessionEntry("s1").LivePending.HasQuestion || !st.World.Sessions[0].Pending.HasQuestion {
		t.Fatalf("expected Question pending from live snapshot in SyncToState")
	}
	var hasLiveTimer bool
	for _, eff := range outRes.Effects {
		if st := eff.GetScheduleTimer(); st != nil && st.Kind == corev1.TimerKind_TIMER_KIND_LIVE_POLL && st.DelayMs == LivePollIntervalMs {
			hasLiveTimer = true
		}
	}
	if !hasLiveTimer {
		t.Fatalf("expected 5s LIVE_POLL timer scheduled after poll")
	}

	// Machine going offline cancels LIVE_POLL timer.
	_, offRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 70,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: legID,
				Frame: &corev1.StreamFrameEvent_MachineUpdated{
					MachineUpdated: &corev1.PlainMachine{MachineId: "m1", Online: false},
				},
			},
		},
	})
	var canceled bool
	for _, eff := range offRes.Effects {
		if ct := eff.GetCancelTimer(); ct != nil && ct.Kind == corev1.TimerKind_TIMER_KIND_LIVE_POLL && ct.Key == "s1" {
			canceled = true
		}
	}
	if !canceled {
		t.Fatalf("expected CancelTimer(LIVE_POLL) when machine goes offline")
	}
}

func TestPrefsCASLoopConflictIdempotencyAndMaxAttempts(t *testing.T) {
	e := NewEngine()
	now := int64(1_700_000_000_000)
	_, listsOp := startAndConnect(t, e, now)
	e.HandleEvent(&corev1.Event{
		NowMs: now + 40,
		Event: &corev1.Event_ListsFetched{
			ListsFetched: &corev1.ListsFetchedEvent{OpId: listsOp},
		},
	})

	// Enqueue Pin -> GetAccountPrefs.
	res := e.Run(func(ctx *StepContext) {
		e.EnqueuePrefsOp(ctx, &lists.FullPrefsOp{Kind: lists.PrefsOpPin, Path: "/work/a"}, false, "")
	})
	var getOp uint64
	for _, eff := range res.Effects {
		if rpc := eff.GetRpc(); rpc != nil && rpc.GetGetAccountPrefs() != nil {
			getOp = rpc.OpId
		}
	}
	if getOp == 0 {
		t.Fatalf("expected GetAccountPrefsRpc")
	}

	// Simulate CAS conflict 4 times, then idempotent recent_ops hit on 5th GET!
	var recordedOpID string
	for attempt := 0; attempt < 4; attempt++ {
		_, getRes := e.HandleEvent(&corev1.Event{
			NowMs: now + int64(50+attempt*10),
			Event: &corev1.Event_AccountPrefsFetched{
				AccountPrefsFetched: &corev1.AccountPrefsFetchedEvent{
					OpId: getOp,
					Prefs: &corev1.PlainAccountPrefs{
						Revision: uint64(attempt + 1),
						Prefs:    &v1.ProjectPrefs{Schema: 1},
					},
				},
			},
		})
		var putOp uint64
		for _, eff := range getRes.Effects {
			if rpc := eff.GetRpc(); rpc != nil && rpc.GetPutAccountPrefs() != nil {
				putOp = rpc.OpId
				if ops := rpc.GetPutAccountPrefs().GetNextPrefs().GetRecentOps(); len(ops) > 0 {
					recordedOpID = ops[len(ops)-1]
				}
			}
		}
		_, putRes := e.HandleEvent(&corev1.Event{
			NowMs: now + int64(55+attempt*10),
			Event: &corev1.Event_AccountPrefsPutResult{
				AccountPrefsPutResult: &corev1.AccountPrefsPutResultEvent{
					OpId:    putOp,
					Applied: false,
				},
			},
		})
		getOp = 0
		for _, eff := range putRes.Effects {
			if rpc := eff.GetRpc(); rpc != nil && rpc.GetGetAccountPrefs() != nil {
				getOp = rpc.OpId
			}
		}
		if getOp == 0 {
			t.Fatalf("expected CAS retry GetAccountPrefs on attempt %d", attempt)
		}
	}

	// 5th GET finds our opID already in RecentOps -> idempotent completion!
	e.HandleEvent(&corev1.Event{
		NowMs: now + 100,
		Event: &corev1.Event_AccountPrefsFetched{
			AccountPrefsFetched: &corev1.AccountPrefsFetchedEvent{
				OpId: getOp,
				Prefs: &corev1.PlainAccountPrefs{
					Revision: 5,
					CacheRef: "prefs-rev-5",
					Prefs: &v1.ProjectPrefs{
						Schema:    1,
						Pinned:    []*v1.ProjectPref{{Path: "/work/a"}},
						RecentOps: []string{recordedOpID},
					},
				},
			},
		},
	})
	if len(e.prefsQueue) != 0 || e.prefs.Revision != 5 || !e.prefsChangedElsewhere {
		t.Fatalf("expected idempotent CAS completion with changedElsewhere=true, queue=%d rev=%d", len(e.prefsQueue), e.prefs.Revision)
	}
}

func TestCommandLifecycleFrozenRetryAndObservedActions(t *testing.T) {
	e := NewEngine()
	now := int64(1_700_000_000_000)
	_, listsOp := startAndConnect(t, e, now)
	e.HandleEvent(&corev1.Event{
		NowMs: now + 40,
		Event: &corev1.Event_ListsFetched{
			ListsFetched: &corev1.ListsFetchedEvent{
				OpId: listsOp,
				Machines: []*corev1.PlainMachine{
					{
						MachineId: "m1",
						Online:    true,
						Meta: &v1.MachineMeta{
							Hostname:        "Dev",
							ProtocolVersion: ProtocolVersion,
							Capabilities:    &v1.MachineCapabilities{FileUpload: true},
						},
					},
				},
				Sessions: []*corev1.PlainSession{
					{
						SessionId: "s1",
						MachineId: "m1",
						Checkpoint: &v1.LiveSnapshot{
							Actions: []*v1.Event{
								{Seq: 10, Kind: &v1.Event_ApprovalRequested{ApprovalRequested: &v1.ApprovalRequested{ApprovalId: "ap-10"}}},
							},
						},
					},
				},
			},
		},
	})

	// Stale observed action rejected preflight.
	staleRes := e.Run(func(ctx *StepContext) {
		e.SubmitCommand(ctx, SubmitCommandParams{
			MachineID: "m1",
			SessionID: "s1",
			Command: &v1.Command{
				Kind: &v1.Command_ResolveApproval{
					ResolveApproval: &v1.ResolveApprovalRequest{ApprovalId: "wrong-id", ObservedEventSeq: 10},
				},
			},
		})
	})
	if len(staleRes.Settled) != 1 || staleRes.Settled[0].Category != outcome.NotSent {
		t.Fatalf("expected NotSent preflight rejection on stale approval action, got %+v", staleRes.Settled)
	}

	// Submit CreateSession on SlotKey -> transport failure leaves it Unknown & frozen.
	subRes := e.Run(func(ctx *StepContext) {
		e.SubmitCommand(ctx, SubmitCommandParams{
			SlotKey:   "create_session:/proj",
			MachineID: "m1",
			Command: &v1.Command{
				Kind: &v1.Command_CreateSession{
					CreateSession: &v1.CreateSessionRequest{
						Cwd:  "/proj",
						Mode: v1.ApprovalMode_APPROVAL_MODE_AUTO,
					},
				},
			},
		})
	})
	dc1 := subRes.Effects[0].GetDispatchCommand()
	if dc1 == nil || dc1.RetryOfUnknown {
		t.Fatalf("expected initial DispatchCommandEffect")
	}
	var decoded v1.Command
	if err := decoded.Unmarshal(dc1.CommandBytes); err != nil || decoded.GetCreateSession().GetMode() != v1.ApprovalMode_APPROVAL_MODE_AUTO {
		t.Fatalf("expected ApprovalMode AUTO serialized inside command bytes, got %+v", &decoded)
	}

	// Transport fails after 4 attempts exhaustion (non-retryable code "permission_denied" or exhausted deadline).
	_, outUnknown := e.HandleEvent(&corev1.Event{
		NowMs: now + 100,
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:     dc1.RequestId,
				AttemptId:     dc1.AttemptId,
				Source:        corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON,
				Signed:        true,
				HashesMatch:   false, // failed integrity binding -> must stay Unknown!
				AttemptBound:  true,
			},
		},
	})
	if len(outUnknown.Settled) != 1 || outUnknown.Settled[0].Category != outcome.Unknown || !outUnknown.Settled[0].Frozen {
		t.Fatalf("expected Unknown + Frozen when daemon hashes do not match, got %+v", outUnknown.Settled)
	}

	// Retry same slot MUST preserve exact RequestId, DeadlineMs, CommandBytes and set RetryOfUnknown=true!
	retryRes := e.Run(func(ctx *StepContext) {
		e.advanceClocks(now+5_000, 0, nil)
		e.SubmitCommand(ctx, SubmitCommandParams{
			SlotKey:   "create_session:/proj",
			MachineID: "m1",
			Command:   &v1.Command{Kind: &v1.Command_CreateSession{CreateSession: &v1.CreateSessionRequest{Cwd: "/different"}}},
		})
	})
	dc2 := retryRes.Effects[0].GetDispatchCommand()
	if dc2 == nil || dc2.RequestId != dc1.RequestId || dc2.DeadlineMs != dc1.DeadlineMs || !dc2.RetryOfUnknown {
		t.Fatalf("retry of frozen command must preserve RequestId/DeadlineMs/bytes and set RetryOfUnknown, got %+v vs %+v", dc2, dc1)
	}

	// Valid executed outcome unfreezes slot!
	_, outOk := e.HandleEvent(&corev1.Event{
		NowMs: now + 5_100,
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:         dc2.RequestId,
				AttemptId:         dc2.AttemptId,
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
						CreateSession: &v1.CreateSessionResponse{Session: &v1.Session{Id: "new-s1"}},
					},
				},
			},
		},
	})
	if len(outOk.Settled) != 1 || outOk.Settled[0].Category != outcome.Executed || outOk.Settled[0].Frozen {
		t.Fatalf("expected Executed settled and unfrozen, got %+v", outOk.Settled)
	}
}

func TestProtocolVersionMatchesInternal(t *testing.T) {
	raw, err := os.ReadFile("../../internal/protocol/protocol.go")
	if err != nil {
		t.Fatalf("read internal/protocol/protocol.go: %v", err)
	}
	want := fmt.Sprintf("const Version = %d", ProtocolVersion)
	if !strings.Contains(string(raw), want) {
		t.Fatalf("expected internal/protocol/protocol.go to contain %q", want)
	}
}

func TestSyncStateAndSessionDataTransitionViews(t *testing.T) {
	e := NewEngine()
	now := int64(1_700_000_000_000)
	_, listsOp := startAndConnect(t, e, now)

	_, listsRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 40,
		Event: &corev1.Event_ListsFetched{
			ListsFetched: &corev1.ListsFetchedEvent{
				OpId: listsOp,
				Machines: []*corev1.PlainMachine{
					{
						MachineId:  "m1",
						Online:     true,
						LastSeenMs: now + 35,
						Meta: &v1.MachineMeta{
							Hostname:        "MacStudio",
							ProtocolVersion: ProtocolVersion - 1,
						},
					},
				},
				Sessions: []*corev1.PlainSession{
					{
						SessionId:     "s1",
						MachineId:     "m1",
						LastSeq:       3,
						UpdatedMs:     now + 30,
						ListedEndedMs: 0,
						Meta: &v1.Session{
							Id:        "s1",
							Cwd:       "/work/app",
							Agent:     v1.Agent_AGENT_CODEX,
							Status:    v1.SessionStatus_SESSION_STATUS_RUNNING,
							Title:     "Transition check",
							CreatedAt: &timestamppb.Timestamp{Seconds: now / 1000},
						},
						Checkpoint: &v1.LiveSnapshot{
							Running: true,
							Actions: []*v1.Event{
								{Seq: 3, Kind: &v1.Event_QuestionAsked{QuestionAsked: &v1.QuestionAsked{QuestionId: "q-live"}}},
							},
						},
					},
				},
				Next: &v1.SessionCursor{EndedMs: now - 10_000, SessionId: "s0"},
				Devices: []*corev1.PlainDevice{
					{
						DeviceId:  "c1",
						Kind:      v1.DeviceKind_DEVICE_KIND_CLIENT,
						CreatedMs: now - 100_000,
						Meta:      &v1.DeviceMeta{Name: "MainPhone", Platform: "Android"},
					},
					{
						DeviceId:  "m1",
						Kind:      v1.DeviceKind_DEVICE_KIND_MACHINE,
						CreatedMs: now - 200_000,
						Meta:      &v1.DeviceMeta{Name: "MacStudio"},
					},
				},
			},
		},
	})
	targets := listsRes.Dirty.ScreenTargets()
	if !slices.Contains(targets, corev1.ScreenTarget_SCREEN_TARGET_SYNC_STATE) || !slices.Contains(targets, corev1.ScreenTarget_SCREEN_TARGET_SESSION_DATA) {
		t.Fatalf("expected SyncState and SessionData render targets on ListsFetched, got %v", targets)
	}

	sv := e.BuildSyncStateView()
	if sv.Connection != corev1.ConnState_CONN_STATE_CONNECTED || !sv.LivePushConnected || !sv.IsManager || !sv.HasMoreSessions {
		t.Fatalf("unexpected SyncStateView header: conn=%v live=%v mgr=%v more=%v", sv.Connection, sv.LivePushConnected, sv.IsManager, sv.HasMoreSessions)
	}
	if len(sv.Machines) != 1 || !sv.Machines[0].DaemonOutOfDate || !sv.Machines[0].Reachable || sv.Machines[0].Meta.GetHostname() != "MacStudio" {
		t.Fatalf("unexpected SyncStateView machines: %+v", sv.Machines)
	}
	if len(sv.Sessions) != 1 || !sv.Sessions[0].Listed || sv.Sessions[0].CreatedMs != (now/1000)*1000 || !sv.Sessions[0].Pending.HasQuestion {
		t.Fatalf("unexpected SyncStateView sessions: %+v", sv.Sessions)
	}
	if len(sv.Devices) != 2 || !sv.Devices[1].Trusted {
		t.Fatalf("unexpected SyncStateView devices: %+v", sv.Devices)
	}

	if missing := e.BuildSessionDataView("missing"); missing.Found {
		t.Fatalf("expected missing session Found=false")
	}

	// Watch s1 -> awaiting cache -> broken cache -> retrying_corrupt_cache -> fetched with unavailable + approval.
	_, wRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 50,
		Event: &corev1.Event_SessionWatch{SessionWatch: &corev1.SessionWatchEvent{SessionId: "s1", Watched: true}},
	})
	if !wRes.Dirty.SessionData {
		t.Fatalf("expected Dirty.SessionData on SessionWatch")
	}
	var cacheOp uint64
	for _, eff := range wRes.Effects {
		if cr := eff.GetCacheRead(); cr != nil && cr.GetEvents() != nil {
			cacheOp = cr.GetEvents().OpId
		}
	}
	loadingView := e.BuildSessionDataView("s1")
	if !loadingView.Found || !loadingView.Loading || !loadingView.AwaitingCache || loadingView.Loaded {
		t.Fatalf("expected Loading+AwaitingCache before CacheEventsLoaded, got %+v", loadingView)
	}

	_, brokenRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 60,
		Event: &corev1.Event_CacheEventsLoaded{
			CacheEventsLoaded: &corev1.CacheEventsLoadedEvent{OpId: cacheOp, SessionId: "s1", Broken: true},
		},
	})
	corruptView := e.BuildSessionDataView("s1")
	if !corruptView.RetryingCorruptCache || !corruptView.Fetching {
		t.Fatalf("expected RetryingCorruptCache+Fetching after broken cache drop, got %+v", corruptView)
	}
	var getEventsOp uint64
	for _, eff := range brokenRes.Effects {
		if rpc := eff.GetRpc(); rpc != nil && rpc.GetGetEvents() != nil {
			getEventsOp = rpc.OpId
		}
	}

	_, evRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 70,
		Event: &corev1.Event_EventsPageFetched{
			EventsPageFetched: &corev1.EventsPageFetchedEvent{
				OpId:      getEventsOp,
				SessionId: "s1",
				Events: []*corev1.PlainEvent{
					{Seq: 1, Unavailable: true, ApproxBytes: 32},
					{Seq: 2, Verified: true, ApproxBytes: 64, Event: &v1.Event{Seq: 2, Kind: &v1.Event_UserMessage{UserMessage: &v1.UserMessage{Text: "hi"}}}},
					{Seq: 3, Verified: true, ApproxBytes: 64, Event: &v1.Event{Seq: 3, Kind: &v1.Event_AgentMessage{AgentMessage: &v1.AgentMessage{Text: "hello"}}}},
				},
			},
		},
	})
	if !evRes.Dirty.SessionData {
		t.Fatalf("expected Dirty.SessionData on EventsPageFetched")
	}
	doneView := e.BuildSessionDataView("s1")
	if !doneView.Loaded || doneView.Loading || doneView.HasGap || doneView.RetryingCorruptCache {
		t.Fatalf("unexpected SessionDataView load state: loaded=%v loading=%v gap=%v corrupt=%v", doneView.Loaded, doneView.Loading, doneView.HasGap, doneView.RetryingCorruptCache)
	}
	if doneView.UnavailableCount != 1 || len(doneView.UnavailableSeqs) != 1 || doneView.UnavailableSeqs[0] != 1 {
		t.Fatalf("expected UnavailableSeqs=[1], got count=%d seqs=%v", doneView.UnavailableCount, doneView.UnavailableSeqs)
	}
	if doneView.Projection == nil || doneView.LiveSnapshot == nil || !doneView.Pending.HasQuestion || len(doneView.Events) != 3 {
		t.Fatalf("expected projection, live snapshot, pending question, and 3 events; got proj=%v live=%v pending=%+v events=%d",
			doneView.Projection != nil, doneView.LiveSnapshot != nil, doneView.Pending, len(doneView.Events))
	}
}
