package sync

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"slices"
	"testing"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/outcome"
	"github.com/Lewin671/remote-agent/clientcore/domain/world"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/gen/timestamppb"
	"github.com/Lewin671/remote-agent/clientcore/security"
	stdv1 "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/shared/devicesec"
	"github.com/Lewin671/remote-agent/shared/linkcore"
	"google.golang.org/protobuf/proto"
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
	respondSyncRpc := func(effs []*corev1.Effect) {
		for _, eff := range effs {
			if sr := eff.GetSecurityRpc(); sr != nil && sr.Method == corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_SECURITY_STATES {
				emptyStatesRaw, _ := proto.Marshal(&stdv1.GetSecurityStatesResponse{HeadEpoch: e.security.HeadEpoch})
				e.HandleEvent(&corev1.Event{
					NowMs: nowMs + 25,
					Event: &corev1.Event_SecurityRpcResult{
						SecurityRpcResult: &corev1.SecurityRpcResultEvent{OpId: sr.OpId, ResponseBytes: emptyStatesRaw},
					},
				})
			}
		}
	}
	var tokOp uint64
	for _, eff := range res.Effects {
		if et := eff.GetEnsureToken(); et != nil {
			tokOp = et.OpId
		}
	}
	respondSyncRpc(res.Effects)
	if tokOp == 0 {
		t.Fatalf("expected EnsureTokenEffect on Start")
	}
	e.HandleEvent(&corev1.Event{
		NowMs: nowMs + 10,
		Event: &corev1.Event_SeedDemoWorld{
			SeedDemoWorld: &corev1.SeedDemoWorldEvent{
				Security: &corev1.DemoSecurity{
					IsMember:          true,
					SelfDeviceId:      "c1",
					AuthorityDeviceId: "c1",
					LeaseEndMs:        nowMs + 86_400_000,
					TrustedMachineIds: []string{"m1", "m2"},
					NetworkGeneration: e.networkGen,
				},
			},
		},
	})
	_, tokRes := e.HandleEvent(&corev1.Event{
		NowMs: nowMs + 20,
		Event: &corev1.Event_TokenResult{
			TokenResult: &corev1.TokenResultEvent{OpId: tokOp, Token: "tok-1", ExpiresMs: nowMs + 7*86_400_000},
		},
	})
	respondSyncRpc(tokRes.Effects)
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
	respondSyncRpc(readyRes.Effects)
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
		if st := eff.GetScheduleTimer(); st != nil && st.Kind == corev1.TimerKind_TIMER_KIND_LINK_GRACE && st.Key == "" {
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
	e3.w.World.Connection = lists.ConnConnecting
	l3 := linkcore.NewLiveness(1_000)
	e3.liveness = &l3
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
				Devices: []*corev1.PlainDevice{
					{DeviceId: "d_keep", Kind: v1.DeviceKind_DEVICE_KIND_CLIENT, MetaRev: 1, Meta: &v1.DeviceMeta{Name: "initial-name"}},
					{DeviceId: "d_removed", Kind: v1.DeviceKind_DEVICE_KIND_CLIENT, MetaRev: 1},
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
	e.HandleEvent(&corev1.Event{
		NowMs: now + 72,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: legID,
				Frame: &corev1.StreamFrameEvent_DeviceUpdated{
					DeviceUpdated: &corev1.PlainDevice{DeviceId: "d_pushed", Kind: v1.DeviceKind_DEVICE_KIND_MACHINE},
				},
			},
		},
	})
	e.HandleEvent(&corev1.Event{
		NowMs: now + 74,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: legID,
				Frame: &corev1.StreamFrameEvent_DeviceUpdated{
					DeviceUpdated: &corev1.PlainDevice{
						DeviceId: "d_keep",
						Kind:     v1.DeviceKind_DEVICE_KIND_CLIENT,
						MetaRev:  2,
						Meta:     &v1.DeviceMeta{Name: "live-name"},
					},
				},
			},
		},
	})
	e.HandleEvent(&corev1.Event{
		NowMs: now + 76,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: legID,
				Frame: &corev1.StreamFrameEvent_DeviceRemovedId{DeviceRemovedId: "d_removed"},
			},
		},
	})

	// Server's stale snapshot returns: contains m1, m3(stale!), omits m2, s_pushed, and d_pushed, next=EndedMs:700.
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
				Devices: []*corev1.PlainDevice{
					{DeviceId: "d_keep", Kind: v1.DeviceKind_DEVICE_KIND_CLIENT, MetaRev: 1, Meta: &v1.DeviceMeta{Name: "stale-name"}},
					{DeviceId: "d_removed", Kind: v1.DeviceKind_DEVICE_KIND_CLIENT, MetaRev: 1}, // pushed removed! must NOT resurrect
					{DeviceId: "d_listed", Kind: v1.DeviceKind_DEVICE_KIND_CLIENT, MetaRev: 1},
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
	if e.devices["d_pushed"] == nil {
		t.Fatalf("pushed device d_pushed should survive ListsFetched omission")
	}
	if got := e.devices["d_keep"]; got == nil || got.MetaRev != 2 || got.Meta.GetName() != "live-name" {
		t.Fatalf("live-pushed device d_keep metadata should beat stale ListsFetched, got %+v", got)
	}
	if e.devices["d_removed"] != nil {
		t.Fatalf("removed device d_removed must not be resurrected by stale ListsFetched")
	}
	if e.devices["d_listed"] == nil {
		t.Fatalf("expected d_listed from ListsFetched")
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

	// DeviceUpdated arriving while FetchDevices (opId != 0) is in flight must also survive.
	devRes := e.Run(func(ctx *StepContext) {
		e.startRefreshDevices(ctx, true)
	})
	var devicesOp uint64
	for _, eff := range devRes.Effects {
		if rpc := eff.GetRpc(); rpc != nil && rpc.GetFetchDevices() != nil {
			devicesOp = rpc.OpId
		}
	}
	if devicesOp == 0 {
		t.Fatalf("expected FetchDevicesRpc")
	}
	e.HandleEvent(&corev1.Event{
		NowMs: now + 82,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: legID,
				Frame: &corev1.StreamFrameEvent_DeviceUpdated{
					DeviceUpdated: &corev1.PlainDevice{DeviceId: "d_dev_pushed", Kind: v1.DeviceKind_DEVICE_KIND_MACHINE},
				},
			},
		},
	})
	e.HandleEvent(&corev1.Event{
		NowMs: now + 84,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: legID,
				Frame: &corev1.StreamFrameEvent_DeviceUpdated{
					DeviceUpdated: &corev1.PlainDevice{DeviceId: "d_dev_bounce", Kind: v1.DeviceKind_DEVICE_KIND_CLIENT},
				},
			},
		},
	})
	e.HandleEvent(&corev1.Event{
		NowMs: now + 86,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: legID,
				Frame: &corev1.StreamFrameEvent_DeviceRemovedId{DeviceRemovedId: "d_dev_bounce"},
			},
		},
	})
	e.HandleEvent(&corev1.Event{
		NowMs: now + 88,
		Event: &corev1.Event_DevicesFetched{
			DevicesFetched: &corev1.DevicesFetchedEvent{
				OpId: devicesOp,
				Devices: []*corev1.PlainDevice{
					{DeviceId: "d_dev_listed", Kind: v1.DeviceKind_DEVICE_KIND_CLIENT},
					{DeviceId: "d_dev_bounce", Kind: v1.DeviceKind_DEVICE_KIND_CLIENT},
				},
			},
		},
	})
	if e.devices["d_dev_pushed"] == nil {
		t.Fatalf("pushed device d_dev_pushed should survive in-flight FetchDevices response")
	}
	if e.devices["d_dev_listed"] == nil {
		t.Fatalf("expected d_dev_listed from DevicesFetched")
	}
	if e.devices["d_dev_bounce"] != nil {
		t.Fatalf("updated-then-removed device d_dev_bounce must stay removed after DevicesFetched")
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
	if !e.SessionEntry("s1").Pending.HasApproval || e.SessionEntry("s1").Pending.ApprovalID != "ap-tail" {
		t.Fatalf("expected approval projection ap-tail after draining, got %+v", e.SessionEntry("s1").Pending)
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
		Event:  &corev1.Event_SessionPin{SessionPin: &corev1.SessionPinEvent{SessionId: "s1", Pinned: true}},
	})
	e.HandleEvent(&corev1.Event{
		NowMs:  now + 100,
		MonoMs: 5_000,
		Event:  &corev1.Event_SessionWatch{SessionWatch: &corev1.SessionWatchEvent{SessionId: "s1", Watched: false}},
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
		Event:  &corev1.Event_SessionPin{SessionPin: &corev1.SessionPinEvent{SessionId: "s1", Pinned: false}},
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
		Event:  &corev1.Event_SessionWatch{SessionWatch: &corev1.SessionWatchEvent{SessionId: "s1", Watched: true}},
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
	if !e.SessionEntry("s1").Pending.HasQuestion || !e.World().World.Sessions[0].Pending.HasQuestion {
		t.Fatalf("expected Question pending from live snapshot in the world")
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
					Prefs: &v1.ProjectPrefs{
						Schema:    1,
						Pinned:    []*v1.ProjectPref{{Path: "/work/a"}},
						RecentOps: []string{recordedOpID},
					},
				},
			},
		},
	})
	if len(e.prefsQueue) != 0 || e.prefs.Revision != 5 || !e.w.ProjectChangedElsewhere {
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
							ProtocolVersion: PeerVersion,
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
				RequestId:    dc1.RequestId,
				AttemptId:    dc1.AttemptId,
				Source:       corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON,
				Signed:       true,
				HashesMatch:  false, // failed integrity binding -> must stay Unknown!
				AttemptBound: true,
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
							ProtocolVersion: PeerVersion - 1,
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

func TestSecurityAndDevicesGenerationChecks(t *testing.T) {
	e := NewEngine()
	now := int64(1_700_000_000_000)
	client := newDirectTestActor(t, 71, 1)
	m1 := newDirectTestActor(t, 72, 2)
	m2 := newDirectTestActor(t, 74, 2)
	rec := newDirectTestActor(t, 73, 1)
	dom := sha256.Sum256([]byte("sync-gen-check"))
	sec1 := bytes.Repeat([]byte{0x11}, 32)
	sec2 := bytes.Repeat([]byte{0x22}, 32)
	sec3 := bytes.Repeat([]byte{0x33}, 32)
	u0, err := devicesec.NewGenesis(client.id, dom, client.member(devicesec.Client), rec.signPub, sec1, uint64(now), syncTestHPKE{})
	if err != nil {
		t.Fatal(err)
	}
	sb0, _ := u0.SigningBytes()
	u0.Signature = ed25519.Sign(client.signPriv, sb0)
	s0Raw, _ := u0.MarshalBinary()
	s0, _ := devicesec.ParseState(s0Raw)
	genHash, _ := s0.Hash()
	u1, err := devicesec.NewMembershipState(s0, []devicesec.Member{client.member(devicesec.Client), m2.member(devicesec.Machine)}, nil, sec1, sec2, uint64(now+1), syncTestHPKE{}, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sb1, _ := u1.SigningBytes()
	u1.Signature = ed25519.Sign(client.signPriv, sb1)
	s1Raw, _ := u1.MarshalBinary()
	s1, _ := devicesec.ParseState(s1Raw)
	u2, err := devicesec.NewMembershipState(s1, []devicesec.Member{client.member(devicesec.Client), m1.member(devicesec.Machine), m2.member(devicesec.Machine)}, nil, sec2, sec3, uint64(now+2), syncTestHPKE{}, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sb2, _ := u2.SigningBytes()
	u2.Signature = ed25519.Sign(client.signPriv, sb2)
	s2Raw, _ := u2.MarshalBinary()

	e.HandleEvent(&corev1.Event{
		NowMs: now,
		Event: &corev1.Event_SecurityConfig{
			SecurityConfig: &corev1.SecurityConfigEvent{Account: client.id, DeviceId: client.id, InlineCrypto: true},
		},
	})

	// 1. Push with PushSeq=2 applies epoch=2; PushSeq=0 or stale PushSeq=1 afterwards is ignored.
	applyTestSecurityRecord(e, now+10, security.RecordUpdate{
		PushSeq: 2,
		Account: client.id,
		Domain:  dom[:],
		Genesis: genHash[:],
		States:  [][]byte{s0Raw, s1Raw},
	})
	if !e.security.Loaded || e.security.HeadEpoch != 2 {
		t.Fatalf("expected pushSeq=2 RecordUpdate to apply, got loaded=%v epoch=%d", e.security.Loaded, e.security.HeadEpoch)
	}
	applyTestSecurityRecord(e, now+15, security.RecordUpdate{
		Account: client.id,
		Domain:  dom[:],
		Genesis: genHash[:],
		States:  [][]byte{s0Raw},
	})
	if !e.security.Loaded || e.security.HeadEpoch != 2 || !e.security.IsMember {
		t.Fatalf("expected pushSeq=0 RecordUpdate after pushSeq=2 to be dropped, got member=%v epoch=%d", e.security.IsMember, e.security.HeadEpoch)
	}
	applyTestSecurityRecord(e, now+20, security.RecordUpdate{
		PushSeq: 1,
		Removed: true,
	})
	if !e.security.Loaded || e.security.HeadEpoch != 2 || !e.security.IsMember {
		t.Fatalf("expected stale pushSeq=1 RecordUpdate to be dropped, got member=%v epoch=%d", e.security.IsMember, e.security.HeadEpoch)
	}

	// 2. MachineRemovedId stream frame emits SecurityRpc(GET_SECURITY_STATES); wrong opId is rejected; matching opId updates epoch=3.
	actor := syncTestActor{id: client.id, ed: client.signPriv, edP: client.signPub[:], x: client.xPriv, xPub: client.xPub}
	secRec := &security.PersistedRecord{
		Pin:      devicesec.Pin{Account: client.id, Domain: dom, Genesis: genHash},
		MemberID: client.id,
		Member:   true,
		Server:   "https://relay.example.com",
		States:   [][]byte{s0Raw, s1Raw},
		FloorMs:  now + 100_000,
	}
	recRaw, err := secRec.Encode()
	if err != nil {
		t.Fatal(err)
	}
	bootstrapTestSecurityRecord(t, e, actor, now+25, secRec, "tok-1")
	e.running = true
	e.legID = 42
	_, mrRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 30,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: 42,
				Frame: &corev1.StreamFrameEvent_MachineRemovedId{MachineRemovedId: "m1"},
			},
		},
	})
	var secOp uint64
	for _, eff := range mrRes.Effects {
		if sr := eff.GetSecurityRpc(); sr != nil && sr.Method == corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_SECURITY_STATES {
			secOp = sr.OpId
		}
	}
	if secOp == 0 {
		t.Fatalf("expected MachineRemovedId stream frame to emit GET_SECURITY_STATES SecurityRpcEffect with non-zero OpId")
	}
	statesRespRaw, err := proto.Marshal(&stdv1.GetSecurityStatesResponse{
		HeadEpoch: 3,
		States:    []*stdv1.SecurityState{{State: s2Raw}},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.HandleEvent(&corev1.Event{
		NowMs: now + 35,
		Event: &corev1.Event_SecurityRpcResult{
			SecurityRpcResult: &corev1.SecurityRpcResultEvent{
				OpId:          secOp + 99,
				ResponseBytes: statesRespRaw,
			},
		},
	})
	if e.security.HeadEpoch != 2 {
		t.Fatalf("expected wrong opId SecurityRpcResult to be ignored, got epoch=%d", e.security.HeadEpoch)
	}
	_, rpcDone := e.HandleEvent(&corev1.Event{
		NowMs: now + 40,
		Event: &corev1.Event_SecurityRpcResult{
			SecurityRpcResult: &corev1.SecurityRpcResultEvent{
				OpId:          secOp,
				ResponseBytes: statesRespRaw,
			},
		},
	})
	// Pump CAS storage effects emitted by SyncStates so epoch=3 persists into secEngine.
	storeBlob := recRaw
	storeRev := "rev-1"
	q := append([]*corev1.Effect(nil), rpcDone.Effects...)
	for len(q) > 0 {
		eff := q[0]
		q = q[1:]
		if st := eff.GetSecurityStorage(); st != nil {
			var rep *corev1.SecurityStorageResultEvent
			switch st.Op {
			case corev1.SecurityStorageOp_SECURITY_STORAGE_OP_READ:
				rep = &corev1.SecurityStorageResultEvent{
					OpId:     st.OpId,
					Found:    st.Bucket == corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_RECORD,
					Revision: storeRev,
					Data:     storeBlob,
				}
			case corev1.SecurityStorageOp_SECURITY_STORAGE_OP_WRITE_CAS:
				if st.Bucket == corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_RECORD {
					storeBlob = st.Data
					storeRev = st.NextRevision
				}
				rep = &corev1.SecurityStorageResultEvent{OpId: st.OpId, Revision: st.NextRevision}
			case corev1.SecurityStorageOp_SECURITY_STORAGE_OP_BATCH_COMMIT:
				for _, w := range st.Batch {
					if w.Bucket == corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_RECORD && !w.Remove {
						storeBlob = w.Data
						storeRev = w.NextRevision
					}
				}
				rep = &corev1.SecurityStorageResultEvent{OpId: st.OpId}
			}
			if rep != nil {
				_, nextRes := e.HandleEvent(&corev1.Event{
					NowMs: now + 41,
					Event: &corev1.Event_SecurityStorageResult{SecurityStorageResult: rep},
				})
				q = append(q, nextRes.Effects...)
			}
		} else if sr := eff.GetSecurityRpc(); sr != nil && sr.Method == corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_SECURITY_STATES {
			emptyStates3Raw, _ := proto.Marshal(&stdv1.GetSecurityStatesResponse{HeadEpoch: 3})
			_, nextRes := e.HandleEvent(&corev1.Event{
				NowMs: now + 42,
				Event: &corev1.Event_SecurityRpcResult{
					SecurityRpcResult: &corev1.SecurityRpcResultEvent{OpId: sr.OpId, ResponseBytes: emptyStates3Raw},
				},
			})
			q = append(q, nextRes.Effects...)
		} else if ka := eff.GetSecurityKeyAgree(); ka != nil {
			peer, _ := ecdh.X25519().NewPublicKey(ka.PeerPublic)
			shared, _ := actor.x.ECDH(peer)
			_, nextRes := e.HandleEvent(&corev1.Event{
				NowMs: now + 43,
				Event: &corev1.Event_SecurityKeyAgreed{SecurityKeyAgreed: &corev1.SecurityKeyAgreedEvent{OpId: ka.OpId, Shared: shared}},
			})
			q = append(q, nextRes.Effects...)
		}
	}
	if e.security.HeadEpoch != 3 {
		t.Fatalf("expected matching opId SecurityRpcResult to apply epoch=3, got %d", e.security.HeadEpoch)
	}

	// 4. DevicesFetched: opId=0, pushSeq=0 is rejected; pushSeq=2 accepted; pushSeq=1 rejected.
	e.HandleEvent(&corev1.Event{
		NowMs: now + 50,
		Event: &corev1.Event_DevicesFetched{
			DevicesFetched: &corev1.DevicesFetchedEvent{
				Devices: []*corev1.PlainDevice{{DeviceId: "d-bad"}},
			},
		},
	})
	if _, ok := e.devices["d-bad"]; ok {
		t.Fatalf("expected DevicesFetched with opId=0 and pushSeq=0 to be dropped")
	}
	e.HandleEvent(&corev1.Event{
		NowMs: now + 55,
		Event: &corev1.Event_DevicesFetched{
			DevicesFetched: &corev1.DevicesFetchedEvent{
				PushSeq: 2,
				Devices: []*corev1.PlainDevice{{DeviceId: "d-2"}},
			},
		},
	})
	if _, ok := e.devices["d-2"]; !ok {
		t.Fatalf("expected DevicesFetched pushSeq=2 to populate d-2")
	}
	e.HandleEvent(&corev1.Event{
		NowMs: now + 60,
		Event: &corev1.Event_DevicesFetched{
			DevicesFetched: &corev1.DevicesFetchedEvent{
				PushSeq: 1,
				Devices: []*corev1.PlainDevice{{DeviceId: "d-stale"}},
			},
		},
	})
	if _, ok := e.devices["d-2"]; !ok {
		t.Fatalf("expected stale DevicesFetched pushSeq=1 to be ignored")
	}
	if _, ok := e.devices["d-stale"]; ok {
		t.Fatalf("expected stale DevicesFetched pushSeq=1 not to add d-stale")
	}

	// 5. DeviceRemovedId stream frame also emits SecurityRpc(GET_SECURITY_STATES) directly from core.
	_, drRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 70,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: 42,
				Frame: &corev1.StreamFrameEvent_DeviceRemovedId{DeviceRemovedId: "d-2"},
			},
		},
	})
	var gotSecSync bool
	for _, eff := range drRes.Effects {
		if sr := eff.GetSecurityRpc(); sr != nil && sr.Method == corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_SECURITY_STATES && sr.OpId != 0 {
			gotSecSync = true
		}
	}
	if !gotSecSync {
		t.Fatalf("expected DeviceRemovedId stream frame to emit GET_SECURITY_STATES SecurityRpcEffect")
	}
}

func TestBackgroundAndStopClearDirectReady(t *testing.T) {
	e := NewEngine()
	startAndConnect(t, e, 1_000)
	m := &MachineEntry{Machine: lists.Machine{ID: "m-1", Online: false}}
	e.machines["m-1"] = m
	e.directReady["m-1"] = true
	if !e.directReady["m-1"] || !e.machineReachable(m) {
		t.Fatalf("expected m-1 directReady and reachable before background/stop")
	}

	e.HandleEvent(&corev1.Event{
		NowMs: 1_200,
		Event: &corev1.Event_SyncLifecycle{
			SyncLifecycle: &corev1.SyncLifecycleAction{
				Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_BACKGROUND,
			},
		},
	})
	if e.directReady["m-1"] || e.machineReachable(m) {
		t.Fatalf("expected BACKGROUND to clear directReady for offline machine, got directReady=%v reachable=%v", e.directReady["m-1"], e.machineReachable(m))
	}

	e.directReady["m-1"] = true
	e.HandleEvent(&corev1.Event{
		NowMs: 1_400,
		Event: &corev1.Event_SyncLifecycle{
			SyncLifecycle: &corev1.SyncLifecycleAction{
				Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_STOP,
			},
		},
	})
	if e.directReady["m-1"] || e.machineReachable(m) {
		t.Fatalf("expected STOP to clear directReady for offline machine, got directReady=%v reachable=%v", e.directReady["m-1"], e.machineReachable(m))
	}
}

func TestResumeAfterStopAndBackgroundEmitsSyncStatesRpc(t *testing.T) {
	now := int64(1_700_000_000_000)
	phone := newSyncTestActor(t, 1)
	rec := newSyncTestActor(t, 2)
	dom := devicesec.Digest("resume-sync-dom", []byte{1})
	sec := bytes.Repeat([]byte{0x42}, 32)
	u0, err := devicesec.NewGenesis(phone.id, dom, phone.member(devicesec.Client), [32]byte(rec.edP), sec, uint64(now), syncTestHPKE{})
	if err != nil {
		t.Fatal(err)
	}
	sb0, _ := u0.SigningBytes()
	u0.Signature = ed25519.Sign(phone.ed, sb0)
	s0Raw, _ := u0.MarshalBinary()
	s0, _ := devicesec.ParseState(s0Raw)
	gen, _ := s0.Hash()
	lease, _ := devicesec.NewLease(s0, uint64(now))
	lb, _ := lease.SigningBytes()
	lease.Signature = ed25519.Sign(phone.ed, lb)
	leaseRaw, _ := lease.MarshalBinary()

	e := NewEngine()
	bootstrapTestSecurityRecord(t, e, phone, now, &security.PersistedRecord{
		Pin:      devicesec.Pin{Account: phone.id, Domain: dom, Genesis: gen},
		MemberID: phone.id,
		Member:   true,
		Server:   "https://relay.example.com",
		States:   [][]byte{s0Raw},
		Lease:    leaseRaw,
		FloorMs:  now + 100_000,
	}, "tok-1")
	startAndConnect(t, e, now+100)
	if !e.security.Loaded {
		t.Fatalf("expected loaded security before STOP/START")
	}

	// 1. STOP -> START on unchanged network must advance networkGen and emit GET_SECURITY_STATES SecurityRpcEffect.
	e.HandleEvent(&corev1.Event{
		NowMs: 2_000 + now,
		Event: &corev1.Event_SyncLifecycle{
			SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_STOP},
		},
	})
	_, res := e.HandleEvent(&corev1.Event{
		NowMs: 2_100 + now,
		Event: &corev1.Event_SyncLifecycle{
			SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START},
		},
	})
	var startRpcOp uint64
	for _, eff := range res.Effects {
		if sr := eff.GetSecurityRpc(); sr != nil && sr.Method == corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_SECURITY_STATES {
			startRpcOp = sr.OpId
		}
	}
	if startRpcOp == 0 || e.networkGen == 0 {
		t.Fatalf("expected START after STOP to emit GET_SECURITY_STATES SecurityRpcEffect and advance networkGen, got op=%d gen=%d", startRpcOp, e.networkGen)
	}
	emptyStatesRaw, _ := proto.Marshal(&stdv1.GetSecurityStatesResponse{HeadEpoch: 1})
	e.HandleEvent(&corev1.Event{
		NowMs: 2_110 + now,
		Event: &corev1.Event_SecurityRpcResult{
			SecurityRpcResult: &corev1.SecurityRpcResultEvent{OpId: startRpcOp, ResponseBytes: emptyStatesRaw},
		},
	})

	// 2. BACKGROUND -> FOREGROUND while running/stream open must also emit GET_SECURITY_STATES SecurityRpcEffect.
	prevGen := e.networkGen
	e.w.LivePushConnected = true
	e.HandleEvent(&corev1.Event{
		NowMs: 2_200 + now,
		Event: &corev1.Event_SyncLifecycle{
			SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_BACKGROUND},
		},
	})
	_, fgRes := e.HandleEvent(&corev1.Event{
		NowMs: 2_300 + now,
		Event: &corev1.Event_SyncLifecycle{
			SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_FOREGROUND},
		},
	})
	var fgRpcOp uint64
	for _, eff := range fgRes.Effects {
		if sr := eff.GetSecurityRpc(); sr != nil && sr.Method == corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_SECURITY_STATES {
			fgRpcOp = sr.OpId
		}
	}
	if fgRpcOp == 0 || e.networkGen <= prevGen {
		t.Fatalf("expected FOREGROUND after BACKGROUND to emit GET_SECURITY_STATES SecurityRpcEffect with networkGen>%d, got op=%d gen=%d", prevGen, fgRpcOp, e.networkGen)
	}
}

func TestDisplayGraceHoldsConnectedViewDuringBackgroundResumeAndDisconnect(t *testing.T) {
	findDisplayTimer := func(res StepResult) uint64 {
		for _, eff := range res.Effects {
			if st := eff.GetScheduleTimer(); st != nil &&
				st.Kind == corev1.TimerKind_TIMER_KIND_LINK_GRACE &&
				st.Key == displayGraceTimerKey &&
				st.DelayMs == DisplayGraceMs {
				return st.TimerId
			}
		}
		return 0
	}

	cases := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "cold_start_before_ever_ready_never_enables_display_grace",
			run: func(t *testing.T) {
				e := NewEngine()
				ids, fs := seedEntropy(8)
				e.HandleEvent(&corev1.Event{
					NowMs:      1_000,
					MonoMs:     1_000,
					RandomIds:  ids,
					UnitFloats: fs,
					Event: &corev1.Event_SyncLifecycle{
						SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START},
					},
				})
				v := e.BuildSyncStateView()
				if v.Connection != corev1.ConnState_CONN_STATE_CONNECTING || v.DisplayGrace {
					t.Fatalf("expected connecting without display grace before ready, got conn=%v grace=%v", v.Connection, v.DisplayGrace)
				}
			},
		},
		{
			name: "stop_and_start_preserve_real_conn_while_holding_display_grace_until_clock_expires",
			run: func(t *testing.T) {
				e := NewEngine()
				startAndConnect(t, e, 1_000)

				// STOP keeps real connection OFFLINE, holds DisplayGrace without unlinked.
				e.HandleEvent(&corev1.Event{
					NowMs:  2_000,
					MonoMs: 2_000,
					Event: &corev1.Event_SyncLifecycle{
						SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_STOP},
					},
				})
				stopped := e.BuildSyncStateView()
				if e.Connection() != lists.ConnOffline || stopped.Connection != corev1.ConnState_CONN_STATE_OFFLINE {
					t.Fatalf("expected real OFFLINE on STOP, got engine=%v proto=%v", e.Connection(), stopped.Connection)
				}
				if !stopped.DisplayGrace || !stopped.Unlinked {
					t.Fatalf("expected STOP after ready to hold DisplayGrace while keeping real Unlinked, got grace=%v unlinked=%v", stopped.DisplayGrace, stopped.Unlinked)
				}

				// START schedules 1500ms display timer and holds grace before deadline.
				_, startRes := e.HandleEvent(&corev1.Event{
					NowMs:  5_000,
					MonoMs: 5_000,
					Event: &corev1.Event_SyncLifecycle{
						SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START},
					},
				})
				if findDisplayTimer(startRes) == 0 {
					t.Fatalf("expected START after STOP to schedule display grace timer")
				}
				e.HandleEvent(&corev1.Event{
					NowMs:  6_400,
					MonoMs: 6_400,
					Event: &corev1.Event_ClockUpdated{
						ClockUpdated: &corev1.LocalCalendar{NowMs: 6_400},
					},
				})
				inside := e.BuildSyncStateView()
				if inside.Connection != corev1.ConnState_CONN_STATE_RECONNECTING || !inside.DisplayGrace || !inside.Unlinked {
					t.Fatalf("expected RECONNECTING + grace active + real unlinked at +1400ms, got conn=%v grace=%v unlinked=%v", inside.Connection, inside.DisplayGrace, inside.Unlinked)
				}

				// Injected clock advancing to +1500ms ends grace and exposes unlinked state.
				e.HandleEvent(&corev1.Event{
					NowMs:  6_500,
					MonoMs: 6_500,
					Event: &corev1.Event_ClockUpdated{
						ClockUpdated: &corev1.LocalCalendar{NowMs: 6_500},
					},
				})
				expired := e.BuildSyncStateView()
				if expired.DisplayGrace || !expired.Unlinked {
					t.Fatalf("expected grace expired at +1500ms, got grace=%v unlinked=%v", expired.DisplayGrace, expired.Unlinked)
				}
			},
		},
		{
			name: "stream_drop_starts_display_grace_and_timer_fired_ends_it",
			run: func(t *testing.T) {
				e := NewEngine()
				startAndConnect(t, e, 1_000)

				_, dropRes := e.HandleEvent(&corev1.Event{
					NowMs:  3_000,
					MonoMs: 3_000,
					Event: &corev1.Event_StreamClosed{
						StreamClosed: &corev1.StreamClosedEvent{LegId: e.legID, Error: "drop"},
					},
				})
				timerID := findDisplayTimer(dropRes)
				if timerID == 0 {
					t.Fatalf("expected StreamClosed from connected state to schedule display grace timer")
				}
				inside := e.BuildSyncStateView()
				if inside.Connection != corev1.ConnState_CONN_STATE_RECONNECTING || !inside.DisplayGrace {
					t.Fatalf("expected RECONNECTING + DisplayGrace inside window, got conn=%v grace=%v", inside.Connection, inside.DisplayGrace)
				}

				// Timer firing ends grace without needing another connection change.
				e.HandleEvent(&corev1.Event{
					NowMs:  4_500,
					MonoMs: 4_500,
					Event: &corev1.Event_TimerFired{
						TimerFired: &corev1.TimerFiredEvent{
							TimerId: timerID,
							Kind:    corev1.TimerKind_TIMER_KIND_LINK_GRACE,
							Key:     displayGraceTimerKey,
						},
					},
				})
				after := e.BuildSyncStateView()
				if after.DisplayGrace {
					t.Fatalf("expected DisplayGrace=false after TimerFired")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, tc.run)
	}
}

func TestColdStartGraceHoldsUnknownReachabilityUntilListsOrExpiry(t *testing.T) {
	findColdStartTimer := func(res StepResult) uint64 {
		for _, eff := range res.Effects {
			if st := eff.GetScheduleTimer(); st != nil &&
				st.Kind == corev1.TimerKind_TIMER_KIND_LINK_GRACE &&
				st.Key == coldStartGraceTimerKey &&
				st.DelayMs == DisplayGraceMs {
				return st.TimerId
			}
		}
		return 0
	}

	cases := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "cold_start_connecting_keeps_reachability_unknown_until_lists_fetched",
			run: func(t *testing.T) {
				e := NewEngine()
				ids, fs := seedEntropy(8)
				_, startRes := e.HandleEvent(&corev1.Event{
					NowMs:      1_000,
					MonoMs:     1_000,
					RandomIds:  ids,
					UnitFloats: fs,
					Event: &corev1.Event_SyncLifecycle{
						SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START},
					},
				})
				if findColdStartTimer(startRes) == 0 {
					t.Fatalf("expected START to schedule cold-start link grace timer")
				}
				v0 := e.BuildSyncStateView()
				if v0.Connection != corev1.ConnState_CONN_STATE_CONNECTING || v0.DisplayGrace || !v0.ReachabilityUnknown {
					t.Fatalf("expected CONNECTING + ReachabilityUnknown without DisplayGrace on cold start, got conn=%v grace=%v unknown=%v",
						v0.Connection, v0.DisplayGrace, v0.ReachabilityUnknown)
				}

				// Cached offline machine + running session retain ReachabilityUnknown during grace.
				var bootOp uint64
				for _, eff := range startRes.Effects {
					if cr := eff.GetCacheRead(); cr != nil && cr.GetBootstrap() != nil {
						bootOp = cr.GetBootstrap().OpId
					}
				}
				var tokOp uint64
				for _, eff := range startRes.Effects {
					if et := eff.GetEnsureToken(); et != nil {
						tokOp = et.OpId
					}
				}
				e.HandleEvent(&corev1.Event{
					NowMs:  1_020,
					MonoMs: 1_020,
					Event: &corev1.Event_CacheBootstrapLoaded{
						CacheBootstrapLoaded: &corev1.CacheBootstrapLoadedEvent{
							OpId: bootOp,
							Machines: []*corev1.PlainMachine{
								{MachineId: "m1", Online: false},
							},
							Sessions: []*corev1.PlainSession{
								{SessionId: "s1", MachineId: "m1", Meta: &v1.Session{Id: "s1", Status: v1.SessionStatus_SESSION_STATUS_RUNNING}},
							},
						},
					},
				})
				vBoot := e.BuildSyncStateView()
				if len(vBoot.Machines) != 1 || !vBoot.Machines[0].ReachabilityUnknown {
					t.Fatalf("expected cached machine m1 to carry ReachabilityUnknown=true, got %+v", vBoot.Machines)
				}
				snapBoot := e.SessionSnapshot("s1")
				if !snapBoot.ReachabilityUnknown || snapBoot.Machine == nil || !snapBoot.Machine.ReachabilityUnknown {
					t.Fatalf("expected SessionSnapshot to carry ReachabilityUnknown=true, got snap=%+v", snapBoot)
				}

				// TokenResult + StreamReady before ListsFetched: connection is CONNECTED, reachability still UNKNOWN.
				_, tokRes := e.HandleEvent(&corev1.Event{
					NowMs:  1_040,
					MonoMs: 1_040,
					Event: &corev1.Event_TokenResult{
						TokenResult: &corev1.TokenResultEvent{OpId: tokOp, Token: "tok-1"},
					},
				})
				var legID uint64
				for _, eff := range tokRes.Effects {
					if osEff := eff.GetOpenStream(); osEff != nil {
						legID = osEff.LegId
					}
				}
				_, readyRes := e.HandleEvent(&corev1.Event{
					NowMs:  1_100,
					MonoMs: 1_100,
					Event: &corev1.Event_StreamReady{
						StreamReady: &corev1.StreamReadyEvent{LegId: legID},
					},
				})
				var listsOp uint64
				for _, eff := range readyRes.Effects {
					if rpc := eff.GetRpc(); rpc != nil && rpc.GetFetchLists() != nil {
						listsOp = rpc.OpId
					}
				}
				vStream := e.BuildSyncStateView()
				if vStream.Connection != corev1.ConnState_CONN_STATE_CONNECTED || !vStream.ReachabilityUnknown {
					t.Fatalf("expected CONNECTED + ReachabilityUnknown=true between StreamReady and ListsFetched, got conn=%v unknown=%v",
						vStream.Connection, vStream.ReachabilityUnknown)
				}

				// ListsFetched arrives with authoritative machine status and clears cold-start grace.
				e.HandleEvent(&corev1.Event{
					NowMs:  1_150,
					MonoMs: 1_150,
					Event: &corev1.Event_ListsFetched{
						ListsFetched: &corev1.ListsFetchedEvent{
							OpId:     listsOp,
							Machines: []*corev1.PlainMachine{{MachineId: "m1", Online: true}},
							Sessions: []*corev1.PlainSession{{SessionId: "s1", MachineId: "m1", Meta: &v1.Session{Id: "s1", Status: v1.SessionStatus_SESSION_STATUS_RUNNING}}},
						},
					},
				})
				vDone := e.BuildSyncStateView()
				if vDone.ReachabilityUnknown || vDone.Unlinked {
					t.Fatalf("expected ListsFetched to clear ReachabilityUnknown and Unlinked, got unknown=%v unlinked=%v",
						vDone.ReachabilityUnknown, vDone.Unlinked)
				}
				if len(vDone.Machines) != 1 || vDone.Machines[0].ReachabilityUnknown || !vDone.Machines[0].Online {
					t.Fatalf("expected machine m1 Online=true and ReachabilityUnknown=false after ListsFetched, got %+v", vDone.Machines)
				}
			},
		},
		{
			name: "cold_start_grace_expires_on_timer_and_exposes_offline_machine",
			run: func(t *testing.T) {
				e := NewEngine()
				ids, fs := seedEntropy(8)
				_, startRes := e.HandleEvent(&corev1.Event{
					NowMs:      1_000,
					MonoMs:     1_000,
					RandomIds:  ids,
					UnitFloats: fs,
					Event: &corev1.Event_SyncLifecycle{
						SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START},
					},
				})
				timerID := findColdStartTimer(startRes)
				var bootOp uint64
				for _, eff := range startRes.Effects {
					if cr := eff.GetCacheRead(); cr != nil && cr.GetBootstrap() != nil {
						bootOp = cr.GetBootstrap().OpId
					}
				}
				e.HandleEvent(&corev1.Event{
					NowMs:  1_020,
					MonoMs: 1_020,
					Event: &corev1.Event_CacheBootstrapLoaded{
						CacheBootstrapLoaded: &corev1.CacheBootstrapLoadedEvent{
							OpId:     bootOp,
							Machines: []*corev1.PlainMachine{{MachineId: "m1", Online: false}},
						},
					},
				})
				e.HandleEvent(&corev1.Event{
					NowMs:  2_400,
					MonoMs: 2_400,
					Event:  &corev1.Event_ClockUpdated{ClockUpdated: &corev1.LocalCalendar{NowMs: 2_400}},
				})
				inside := e.BuildSyncStateView()
				if !inside.ReachabilityUnknown {
					t.Fatalf("expected ReachabilityUnknown=true before 1500ms cold-start grace expiry")
				}

				e.HandleEvent(&corev1.Event{
					NowMs:  2_500,
					MonoMs: 2_500,
					Event: &corev1.Event_TimerFired{
						TimerFired: &corev1.TimerFiredEvent{
							TimerId: timerID,
							Kind:    corev1.TimerKind_TIMER_KIND_LINK_GRACE,
							Key:     coldStartGraceTimerKey,
						},
					},
				})
				expired := e.BuildSyncStateView()
				if expired.ReachabilityUnknown {
					t.Fatalf("expected ReachabilityUnknown=false after cold-start grace timer fired")
				}
				if len(expired.Machines) != 1 || expired.Machines[0].ReachabilityUnknown || expired.Machines[0].Online {
					t.Fatalf("expected machine m1 to revert to offline (ReachabilityUnknown=false) after expiry, got %+v", expired.Machines)
				}
				snapExpired := e.SessionSnapshot("")
				if snapExpired.ReachabilityUnknown {
					t.Fatalf("expected SessionSnapshot.ReachabilityUnknown=false after expiry")
				}
			},
		},
		{
			name: "account_removed_clears_cold_start_grace_immediately",
			run: func(t *testing.T) {
				e := NewEngine()
				ids, fs := seedEntropy(8)
				_, startRes := e.HandleEvent(&corev1.Event{
					NowMs:      1_000,
					MonoMs:     1_000,
					RandomIds:  ids,
					UnitFloats: fs,
					Event: &corev1.Event_SyncLifecycle{
						SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START},
					},
				})
				var tokOp uint64
				for _, eff := range startRes.Effects {
					if et := eff.GetEnsureToken(); et != nil {
						tokOp = et.OpId
					}
				}
				if !e.BuildSyncStateView().ReachabilityUnknown {
					t.Fatalf("expected ReachabilityUnknown=true on start")
				}
				e.HandleEvent(&corev1.Event{
					NowMs:  1_050,
					MonoMs: 1_050,
					Event: &corev1.Event_TokenResult{
						TokenResult: &corev1.TokenResultEvent{OpId: tokOp, Removed: true},
					},
				})
				after := e.BuildSyncStateView()
				if after.ReachabilityUnknown || !after.Removed {
					t.Fatalf("expected Removed=true and ReachabilityUnknown=false on account removal, got unknown=%v removed=%v",
						after.ReachabilityUnknown, after.Removed)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, tc.run)
	}
}

func TestDirectEventsDedupAndCacheWriteOnRelay(t *testing.T) {
	e := NewEngine()
	now := int64(1_700_000_000_000)
	legID, listsOp := startAndConnect(t, e, now)
	client := newDirectTestActor(t, 11, 1)
	m1Actor := newDirectTestActor(t, 12, 2)
	m2Actor := newDirectTestActor(t, 13, 2)
	m1, m2 := m1Actor.id, m2Actor.id
	e.HandleEvent(&corev1.Event{
		NowMs: now + 35,
		Event: &corev1.Event_SeedDemoWorld{
			SeedDemoWorld: &corev1.SeedDemoWorldEvent{
				Security: &corev1.DemoSecurity{
					IsMember:          true,
					SelfDeviceId:      client.id,
					AuthorityDeviceId: client.id,
					LeaseEndMs:        now + 86_400_000,
					TrustedMachineIds: []string{m1, m2},
				},
			},
		},
	})

	e.HandleEvent(&corev1.Event{
		NowMs: now + 40,
		Event: &corev1.Event_ListsFetched{
			ListsFetched: &corev1.ListsFetchedEvent{
				OpId: listsOp,
				Machines: []*corev1.PlainMachine{
					{MachineId: m1, Online: true},
					{MachineId: m2, Online: true},
				},
				Sessions: []*corev1.PlainSession{
					{SessionId: "s1", MachineId: m1, LastSeq: 2, UpdatedMs: now},
				},
			},
		},
	})
	_, watchRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 50,
		Event: &corev1.Event_SessionWatch{
			SessionWatch: &corev1.SessionWatchEvent{SessionId: "s1", Watched: true},
		},
	})
	var cacheOp uint64
	for _, eff := range watchRes.Effects {
		if cr := eff.GetCacheRead().GetEvents(); cr != nil {
			cacheOp = cr.OpId
		}
	}
	if cacheOp == 0 {
		t.Fatalf("expected CacheReadEvents on SessionWatch")
	}
	_, loadedRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 60,
		Event: &corev1.Event_CacheEventsLoaded{
			CacheEventsLoaded: &corev1.CacheEventsLoadedEvent{OpId: cacheOp, SessionId: "s1"},
		},
	})
	var fetchOp uint64
	for _, eff := range loadedRes.Effects {
		if rpc := eff.GetRpc(); rpc != nil && rpc.GetGetEvents() != nil {
			fetchOp = rpc.OpId
		}
	}
	e.HandleEvent(&corev1.Event{
		NowMs: now + 70,
		Event: &corev1.Event_EventsPageFetched{
			EventsPageFetched: &corev1.EventsPageFetchedEvent{
				OpId:      fetchOp,
				SessionId: "s1",
				Events: []*corev1.PlainEvent{
					{Seq: 1, Verified: true, Event: &v1.Event{Seq: 1, SessionId: "s1"}},
					{Seq: 2, Verified: true, Event: &v1.Event{Seq: 2, SessionId: "s1"}},
				},
				More: false,
			},
		},
	})
	readPCID, hsNow := connectDirectForExistingEngine(t, e, now+75, client, m1Actor)
	now = hsNow

	// 1. Direct event seq=3 arrives before relay: updates display prefix and LastSeq,
	// does NOT advance ServerLastSeq, and emits NO CacheWriteEffect.
	_, directRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 10,
		Event: &corev1.Event_DirectEventsReceived{
			DirectEventsReceived: &corev1.DirectEventsReceivedEvent{
				MachineId: m1,
				SessionId: "s1",
				PcId:      readPCID,
				Sender:    m1,
				Events: []*corev1.PlainEvent{
					{Seq: 3, Verified: true, Event: &v1.Event{Seq: 3, SessionId: "s1"}},
				},
			},
		},
	})
	if !directRes.Dirty.SessionData {
		t.Fatalf("expected Dirty.SessionData on DirectEventsReceived")
	}
	for _, eff := range directRes.Effects {
		if cw := eff.GetCacheWrite(); cw != nil {
			t.Fatalf("direct event must not emit CacheWriteEffect")
		}
	}
	if buf := e.Buffer("s1"); buf.PrefixLen() != 3 {
		t.Fatalf("expected prefix=3 after direct seq=3, got %d", buf.PrefixLen())
	}
	if s := e.sessions["s1"]; s.LastSeq != 3 || s.ServerLastSeq != 2 {
		t.Fatalf("expected LastSeq=3 ServerLastSeq=2, got LastSeq=%d ServerLastSeq=%d", s.LastSeq, s.ServerLastSeq)
	}

	// 2. Relay EventsAppended arrives with seq=3 later: deduplicates and advances ServerLastSeq.
	e.HandleEvent(&corev1.Event{
		NowMs: now + 20,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: legID,
				Frame: &corev1.StreamFrameEvent_EventsAppended{
					EventsAppended: &corev1.EventsAppendedFrame{
						SessionId: "s1",
						Events: []*corev1.PlainEvent{
							{Seq: 3, Verified: true, Event: &v1.Event{Seq: 3, SessionId: "s1"}},
						},
					},
				},
			},
		},
	})
	if buf := e.Buffer("s1"); buf.PrefixLen() != 3 {
		t.Fatalf("expected deduplicated prefix=3, got %d", buf.PrefixLen())
	}
	if s := e.sessions["s1"]; s.ServerLastSeq != 3 {
		t.Fatalf("expected ServerLastSeq=3 after relay EventsAppended, got %d", s.ServerLastSeq)
	}

	// 3. Cross-machine spoofing, untrusted machine, and defensive Broken/Retired items are ignored.
	_, spoofRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 25,
		Event: &corev1.Event_DirectEventsReceived{
			DirectEventsReceived: &corev1.DirectEventsReceivedEvent{
				MachineId: m2, // wrong owner for s1
				SessionId: "s1",
				PcId:      readPCID,
				Sender:    m2,
				Events:    []*corev1.PlainEvent{{Seq: 4, Verified: true, Event: &v1.Event{Seq: 4, SessionId: "s1"}}},
			},
		},
	})
	if e.Buffer("s1").PrefixLen() != 3 || spoofRes.Dirty.SessionData {
		t.Fatalf("cross-machine direct event must be rejected")
	}
	e.HandleEvent(&corev1.Event{
		NowMs: now + 26,
		Event: &corev1.Event_DirectEventsReceived{
			DirectEventsReceived: &corev1.DirectEventsReceivedEvent{
				MachineId: m1,
				SessionId: "s1",
				PcId:      readPCID,
				Sender:    m1,
				Events:    []*corev1.PlainEvent{{Seq: 4, Broken: true, Retired: true}},
			},
		},
	})
	if buf := e.Buffer("s1"); buf.PrefixLen() != 3 || !buf.errorReason.IsZero() || buf.IsRetired() {
		t.Fatalf("broken/retired direct items must be discarded without tainting buffer")
	}
}

func TestDirectAheadGapDoesNotFalseErrorAndTriggersServerGapFill(t *testing.T) {
	e := NewEngine()
	now := int64(1_700_000_000_000)
	legID, listsOp := startAndConnect(t, e, now)
	client := newDirectTestActor(t, 21, 1)
	m1Actor := newDirectTestActor(t, 22, 2)
	m1 := m1Actor.id
	e.HandleEvent(&corev1.Event{
		NowMs: now + 35,
		Event: &corev1.Event_SeedDemoWorld{
			SeedDemoWorld: &corev1.SeedDemoWorldEvent{
				Security: &corev1.DemoSecurity{
					IsMember:          true,
					SelfDeviceId:      client.id,
					AuthorityDeviceId: client.id,
					LeaseEndMs:        now + 86_400_000,
					TrustedMachineIds: []string{m1},
				},
			},
		},
	})

	e.HandleEvent(&corev1.Event{
		NowMs: now + 40,
		Event: &corev1.Event_ListsFetched{
			ListsFetched: &corev1.ListsFetchedEvent{
				OpId:     listsOp,
				Machines: []*corev1.PlainMachine{{MachineId: m1, Online: true}},
				Sessions: []*corev1.PlainSession{{SessionId: "s1", MachineId: m1, LastSeq: 40, UpdatedMs: now}},
			},
		},
	})
	_, watchRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 50,
		Event: &corev1.Event_SessionWatch{SessionWatch: &corev1.SessionWatchEvent{SessionId: "s1", Watched: true}},
	})
	var cacheOp uint64
	for _, eff := range watchRes.Effects {
		if cr := eff.GetCacheRead().GetEvents(); cr != nil {
			cacheOp = cr.OpId
		}
	}
	_, loadedRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 60,
		Event: &corev1.Event_CacheEventsLoaded{CacheEventsLoaded: &corev1.CacheEventsLoadedEvent{OpId: cacheOp, SessionId: "s1"}},
	})
	var fetchOp uint64
	for _, eff := range loadedRes.Effects {
		if rpc := eff.GetRpc(); rpc != nil && rpc.GetGetEvents() != nil {
			fetchOp = rpc.OpId
		}
	}
	initEvents := make([]*corev1.PlainEvent, 40)
	for i := 0; i < 40; i++ {
		seq := uint64(i + 1)
		initEvents[i] = &corev1.PlainEvent{Seq: seq, Verified: true, Event: &v1.Event{Seq: seq, SessionId: "s1"}}
	}
	e.HandleEvent(&corev1.Event{
		NowMs: now + 70,
		Event: &corev1.Event_EventsPageFetched{
			EventsPageFetched: &corev1.EventsPageFetchedEvent{OpId: fetchOp, SessionId: "s1", Events: initEvents, More: false},
		},
	})
	readPCID, hsNow := connectDirectForExistingEngine(t, e, now+75, client, m1Actor)
	now = hsNow

	// Direct delivers seq=42 while seq=41 (>16KiB) is still uploading to server.
	// Direct gap event must be dropped rather than entering EventBuffer.pending,
	// without emitting a premature GetEvents RPC or marking a gap error.
	_, directRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 10,
		Event: &corev1.Event_DirectEventsReceived{
			DirectEventsReceived: &corev1.DirectEventsReceivedEvent{
				MachineId: m1,
				SessionId: "s1",
				PcId:      readPCID,
				Sender:    m1,
				Events:    []*corev1.PlainEvent{{Seq: 42, Verified: true, Event: &v1.Event{Seq: 42, SessionId: "s1"}}},
			},
		},
	})
	for _, eff := range directRes.Effects {
		if rpc := eff.GetRpc(); rpc != nil && rpc.GetGetEvents() != nil {
			t.Fatalf("direct gap must not emit premature GetEvents")
		}
	}
	if buf := e.Buffer("s1"); buf.PrefixLen() != 40 || buf.PendingCount() != 0 {
		t.Fatalf("gapped direct seq=42 must be dropped (prefix=40 pending=0), got prefix=%d pending=%d", buf.PrefixLen(), buf.PendingCount())
	}
	if view := e.BuildSessionDataView("s1"); view.HasGap || !view.Loaded || view.FetchError != nil {
		t.Fatalf("dropped direct gap event must not report HasGap/FetchError: %+v", view)
	}
	// ApplyLiveSnapshot (over direct) still advances LastSeq to 42 without touching ServerLastSeq.
	e.Run(func(ctx *StepContext) {
		e.ApplyLiveSnapshot(ctx, "s1", &v1.LiveSnapshot{Session: &v1.Session{Id: "s1", LastSeq: 42}})
	})
	if e.sessions["s1"].ServerLastSeq != 40 {
		t.Fatalf("ApplyLiveSnapshot must not advance ServerLastSeq, got %d", e.sessions["s1"].ServerLastSeq)
	}

	// If a GetEvents cycle finishes while ServerLastSeq==40, it must not report false gap error.
	var midOp uint64
	_, reWatchRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 85,
		Event: &corev1.Event_SessionWatch{SessionWatch: &corev1.SessionWatchEvent{SessionId: "s1", Watched: true}},
	})
	for _, eff := range reWatchRes.Effects {
		if rpc := eff.GetRpc(); rpc != nil && rpc.GetGetEvents() != nil {
			midOp = rpc.OpId
			if rpc.GetGetEvents().GetAfterSeq() != 40 {
				t.Fatalf("expected GetEvents cursor=40 (ServerLastSeq), got %d", rpc.GetGetEvents().GetAfterSeq())
			}
		}
	}
	if midOp == 0 {
		t.Fatalf("expected SessionWatch to trigger GetEvents")
	}
	e.HandleEvent(&corev1.Event{
		NowMs: now + 90,
		Event: &corev1.Event_EventsPageFetched{
			EventsPageFetched: &corev1.EventsPageFetchedEvent{OpId: midOp, SessionId: "s1", More: false},
		},
	})
	if view := e.BuildSessionDataView("s1"); view.FetchError != nil || view.HasGap || !view.Loaded {
		t.Fatalf("finishFetchCycle with empty server gap must not set FetchError/HasGap: %+v", view)
	}

	// Now server commits 41..42 and broadcasts SessionUpdated(LastSeq=42) (suppose EventsAppended was dropped).
	_, updRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 100,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: legID,
				Frame: &corev1.StreamFrameEvent_SessionUpdated{
					SessionUpdated: &corev1.PlainSession{SessionId: "s1", MachineId: "m1", LastSeq: 42, UpdatedMs: now + 100},
				},
			},
		},
	})
	var gapOp uint64
	for _, eff := range updRes.Effects {
		if rpc := eff.GetRpc(); rpc != nil && rpc.GetGetEvents() != nil {
			gapOp = rpc.OpId
			if rpc.GetGetEvents().GetAfterSeq() != 40 {
				t.Fatalf("expected gap-fill GetEvents after 40, got %d", rpc.GetGetEvents().GetAfterSeq())
			}
		}
	}
	if gapOp == 0 {
		t.Fatalf("SessionUpdated(42) must trigger GetEvents to fill missing 41")
	}
	e.HandleEvent(&corev1.Event{
		NowMs: now + 110,
		Event: &corev1.Event_EventsPageFetched{
			EventsPageFetched: &corev1.EventsPageFetchedEvent{
				OpId:      gapOp,
				SessionId: "s1",
				Events: []*corev1.PlainEvent{
					{Seq: 41, Verified: true, Event: &v1.Event{Seq: 41, SessionId: "s1"}},
					{Seq: 42, Verified: true, Event: &v1.Event{Seq: 42, SessionId: "s1"}},
				},
				More: false,
			},
		},
	})
	if buf := e.Buffer("s1"); buf.PrefixLen() != 42 || buf.PendingCount() != 0 || !buf.IsLoaded() {
		t.Fatalf("expected contiguous prefix=42 pending=0 loaded=true, got prefix=%d pending=%d loaded=%v", buf.PrefixLen(), buf.PendingCount(), buf.IsLoaded())
	}
}

func TestDirectEventsWhileRelayDisconnectedDoNotMarkRelayConnected(t *testing.T) {
	e := NewEngine()
	now := int64(1_700_000_000_000)
	legID, listsOp := startAndConnect(t, e, now)
	client := newDirectTestActor(t, 31, 1)
	m1Actor := newDirectTestActor(t, 32, 2)
	m1 := m1Actor.id
	e.HandleEvent(&corev1.Event{
		NowMs: now + 35,
		Event: &corev1.Event_SeedDemoWorld{
			SeedDemoWorld: &corev1.SeedDemoWorldEvent{
				Security: &corev1.DemoSecurity{
					IsMember:          true,
					SelfDeviceId:      client.id,
					AuthorityDeviceId: client.id,
					LeaseEndMs:        now + 86_400_000,
					TrustedMachineIds: []string{m1},
				},
			},
		},
	})

	e.HandleEvent(&corev1.Event{
		NowMs: now + 40,
		Event: &corev1.Event_ListsFetched{
			ListsFetched: &corev1.ListsFetchedEvent{
				OpId:     listsOp,
				Machines: []*corev1.PlainMachine{{MachineId: m1, Online: true}},
				Sessions: []*corev1.PlainSession{{SessionId: "s1", MachineId: m1, LastSeq: 1, UpdatedMs: now}},
			},
		},
	})
	_, watchRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 50,
		Event: &corev1.Event_SessionWatch{SessionWatch: &corev1.SessionWatchEvent{SessionId: "s1", Watched: true}},
	})
	var cacheOp uint64
	for _, eff := range watchRes.Effects {
		if cr := eff.GetCacheRead().GetEvents(); cr != nil {
			cacheOp = cr.OpId
		}
	}
	_, loadedRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 60,
		Event: &corev1.Event_CacheEventsLoaded{CacheEventsLoaded: &corev1.CacheEventsLoadedEvent{OpId: cacheOp, SessionId: "s1"}},
	})
	var fetchOp uint64
	for _, eff := range loadedRes.Effects {
		if rpc := eff.GetRpc(); rpc != nil && rpc.GetGetEvents() != nil {
			fetchOp = rpc.OpId
		}
	}
	e.HandleEvent(&corev1.Event{
		NowMs: now + 70,
		Event: &corev1.Event_EventsPageFetched{
			EventsPageFetched: &corev1.EventsPageFetchedEvent{
				OpId:      fetchOp,
				SessionId: "s1",
				Events:    []*corev1.PlainEvent{{Seq: 1, Verified: true, Event: &v1.Event{Seq: 1, SessionId: "s1"}}},
			},
		},
	})
	readPCID, hsNow := connectDirectForExistingEngine(t, e, now+75, client, m1Actor)
	now = hsNow

	// Drop relay stream so e.w.World.Connection leaves ConnConnected.
	e.HandleEvent(&corev1.Event{
		NowMs: now + 10,
		Event: &corev1.Event_StreamClosed{
			StreamClosed: &corev1.StreamClosedEvent{LegId: legID, Error: "dropped"},
		},
	})
	if e.Connection() == lists.ConnConnected {
		t.Fatalf("expected relay disconnected after StreamClosed")
	}

	// Direct events arrive while relay is disconnected: buffer updates, e.w.World.Connection does NOT become ConnConnected.
	e.HandleEvent(&corev1.Event{
		NowMs: now + 20,
		Event: &corev1.Event_DirectEventsReceived{
			DirectEventsReceived: &corev1.DirectEventsReceivedEvent{
				MachineId: m1,
				SessionId: "s1",
				PcId:      readPCID,
				Sender:    m1,
				Events:    []*corev1.PlainEvent{{Seq: 2, Verified: true, Event: &v1.Event{Seq: 2, SessionId: "s1"}}},
			},
		},
	})
	if e.Buffer("s1").PrefixLen() != 2 {
		t.Fatalf("expected direct seq=2 ingested while relay disconnected")
	}
	if e.Connection() == lists.ConnConnected {
		t.Fatalf("direct events must not mark relay ConnConnected")
	}
}

func TestDirectAheadDuringTailFirstPreservesTailAndClearsTailRequested(t *testing.T) {
	e := NewEngine()
	now := int64(1_700_000_000_000)
	_, listsOp := startAndConnect(t, e, now)
	client := newDirectTestActor(t, 41, 1)
	m1Actor := newDirectTestActor(t, 42, 2)
	m1 := m1Actor.id
	e.HandleEvent(&corev1.Event{
		NowMs: now + 35,
		Event: &corev1.Event_SeedDemoWorld{
			SeedDemoWorld: &corev1.SeedDemoWorldEvent{
				Security: &corev1.DemoSecurity{
					IsMember:          true,
					SelfDeviceId:      client.id,
					AuthorityDeviceId: client.id,
					LeaseEndMs:        now + 86_400_000,
					TrustedMachineIds: []string{m1},
				},
			},
		},
	})
	e.HandleEvent(&corev1.Event{
		NowMs: now + 40,
		Event: &corev1.Event_ListsFetched{
			ListsFetched: &corev1.ListsFetchedEvent{
				OpId:     listsOp,
				Machines: []*corev1.PlainMachine{{MachineId: m1, Online: true}},
				Sessions: []*corev1.PlainSession{{SessionId: "s1", MachineId: m1, LastSeq: 1200, UpdatedMs: now}},
			},
		},
	})
	_, watchRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 50,
		Event: &corev1.Event_SessionWatch{SessionWatch: &corev1.SessionWatchEvent{SessionId: "s1", Watched: true}},
	})
	var cacheOp uint64
	for _, eff := range watchRes.Effects {
		if cr := eff.GetCacheRead().GetEvents(); cr != nil {
			cacheOp = cr.OpId
		}
	}
	_, cacheRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 60,
		Event: &corev1.Event_CacheEventsLoaded{CacheEventsLoaded: &corev1.CacheEventsLoadedEvent{OpId: cacheOp, SessionId: "s1"}},
	})
	var tailOp uint64
	for _, eff := range cacheRes.Effects {
		if rpc := eff.GetRpc(); rpc != nil && rpc.GetGetEvents() != nil {
			tailOp = rpc.OpId
		}
	}
	// Receive tail 701..1200.
	tail := make([]*corev1.PlainEvent, 0, 500)
	for seq := uint64(701); seq <= 1200; seq++ {
		tail = append(tail, &corev1.PlainEvent{Seq: seq, Verified: true, ApproxBytes: 64, Event: &v1.Event{Seq: seq, SessionId: "s1"}})
	}
	_, tailRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 70,
		Event: &corev1.Event_EventsPageFetched{
			EventsPageFetched: &corev1.EventsPageFetchedEvent{OpId: tailOp, SessionId: "s1", Events: tail},
		},
	})
	var prefixOp1 uint64
	for _, eff := range tailRes.Effects {
		if rpc := eff.GetRpc(); rpc != nil && rpc.GetGetEvents() != nil && rpc.GetGetEvents().AfterSeq == 0 {
			prefixOp1 = rpc.OpId
		}
	}
	readPCID, hsNow := connectDirectForExistingEngine(t, e, now+72, client, m1Actor)
	now = hsNow

	// Two ahead-of-tail direct events [1202, 1203] arrive while tailRequested is active:
	// must be dropped and never enter pending or blank ShownEvents [701..1200].
	e.HandleEvent(&corev1.Event{
		NowMs: now + 5,
		Event: &corev1.Event_DirectEventsReceived{
			DirectEventsReceived: &corev1.DirectEventsReceivedEvent{
				MachineId: m1,
				SessionId: "s1",
				PcId:      readPCID,
				Sender:    m1,
				Events: []*corev1.PlainEvent{
					{Seq: 1202, Verified: true, ApproxBytes: 64, Event: &v1.Event{Seq: 1202, SessionId: "s1"}},
					{Seq: 1203, Verified: true, ApproxBytes: 64, Event: &v1.Event{Seq: 1203, SessionId: "s1"}},
				},
			},
		},
	})
	if buf := e.Buffer("s1"); buf.PendingCount() != 500 {
		t.Fatalf("expected pending=500 (direct ahead-of-tail dropped), got %d", buf.PendingCount())
	}
	if shown := e.Buffer("s1").ShownEvents(); len(shown) != 500 || shown[0].Seq != 701 || shown[len(shown)-1].Seq != 1200 {
		t.Fatalf("expected ShownEvents [701..1200], got len=%d", len(shown))
	}

	// Backfill 1..700: drains 701..1200 into prefix and clears tailRequested cleanly.
	p1 := make([]*corev1.PlainEvent, 0, 500)
	for seq := uint64(1); seq <= 500; seq++ {
		p1 = append(p1, &corev1.PlainEvent{Seq: seq, Verified: true, ApproxBytes: 64, Event: &v1.Event{Seq: seq, SessionId: "s1"}})
	}
	_, p1Res := e.HandleEvent(&corev1.Event{
		NowMs: now + 10,
		Event: &corev1.Event_EventsPageFetched{
			EventsPageFetched: &corev1.EventsPageFetchedEvent{OpId: prefixOp1, SessionId: "s1", Events: p1, More: true},
		},
	})
	var prefixOp2 uint64
	for _, eff := range p1Res.Effects {
		if rpc := eff.GetRpc(); rpc != nil && rpc.GetGetEvents() != nil {
			prefixOp2 = rpc.OpId
		}
	}
	p2 := make([]*corev1.PlainEvent, 0, 200)
	for seq := uint64(501); seq <= 700; seq++ {
		p2 = append(p2, &corev1.PlainEvent{Seq: seq, Verified: true, ApproxBytes: 64, Event: &v1.Event{Seq: seq, SessionId: "s1"}})
	}
	e.HandleEvent(&corev1.Event{
		NowMs: now + 20,
		Event: &corev1.Event_EventsPageFetched{
			EventsPageFetched: &corev1.EventsPageFetchedEvent{OpId: prefixOp2, SessionId: "s1", Events: p2, More: false},
		},
	})
	buf := e.Buffer("s1")
	if buf.tailRequested || buf.PendingCount() != 0 {
		t.Fatalf("expected tailRequested=false and pending=0, got tailRequested=%v pending=%d", buf.tailRequested, buf.PendingCount())
	}
	if shown := buf.ShownEvents(); len(shown) != 1200 {
		t.Fatalf("expected ShownEvents [1..1200], got len=%d", len(shown))
	}
	// Now live contiguous direct event 1201 matches prefixLen+1 and is appended immediately.
	e.HandleEvent(&corev1.Event{
		NowMs: now + 25,
		Event: &corev1.Event_DirectEventsReceived{
			DirectEventsReceived: &corev1.DirectEventsReceivedEvent{
				MachineId: m1,
				SessionId: "s1",
				PcId:      readPCID,
				Sender:    m1,
				Events:    []*corev1.PlainEvent{{Seq: 1201, Verified: true, ApproxBytes: 64, Event: &v1.Event{Seq: 1201, SessionId: "s1"}}},
			},
		},
	})
	if shown := buf.ShownEvents(); len(shown) != 1201 {
		t.Fatalf("expected ShownEvents [1..1201] after next contiguous direct event, got len=%d", len(shown))
	}
}

func TestDirectStrictNextAndServerGapErrorClear(t *testing.T) {
	e := NewEngine()
	now := int64(1_700_000_000_000)
	legID, listsOp := startAndConnect(t, e, now)
	client := newDirectTestActor(t, 51, 1)
	m1Actor := newDirectTestActor(t, 52, 2)
	m1 := m1Actor.id
	e.HandleEvent(&corev1.Event{
		NowMs: now + 35,
		Event: &corev1.Event_SeedDemoWorld{
			SeedDemoWorld: &corev1.SeedDemoWorldEvent{
				Security: &corev1.DemoSecurity{
					IsMember:          true,
					SelfDeviceId:      client.id,
					AuthorityDeviceId: client.id,
					LeaseEndMs:        now + 86_400_000,
					TrustedMachineIds: []string{m1},
				},
			},
		},
	})
	e.HandleEvent(&corev1.Event{
		NowMs: now + 40,
		Event: &corev1.Event_ListsFetched{
			ListsFetched: &corev1.ListsFetchedEvent{
				OpId:     listsOp,
				Machines: []*corev1.PlainMachine{{MachineId: m1, Online: true}},
				Sessions: []*corev1.PlainSession{{SessionId: "s1", MachineId: m1, LastSeq: 1, UpdatedMs: now}},
			},
		},
	})
	_, watchRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 50,
		Event: &corev1.Event_SessionWatch{SessionWatch: &corev1.SessionWatchEvent{SessionId: "s1", Watched: true}},
	})
	var cacheOp uint64
	for _, eff := range watchRes.Effects {
		if cr := eff.GetCacheRead().GetEvents(); cr != nil {
			cacheOp = cr.OpId
		}
	}
	_, loadedRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 60,
		Event: &corev1.Event_CacheEventsLoaded{CacheEventsLoaded: &corev1.CacheEventsLoadedEvent{OpId: cacheOp, SessionId: "s1"}},
	})
	var fetchOp uint64
	for _, eff := range loadedRes.Effects {
		if rpc := eff.GetRpc(); rpc != nil && rpc.GetGetEvents() != nil {
			fetchOp = rpc.OpId
		}
	}
	e.HandleEvent(&corev1.Event{
		NowMs: now + 70,
		Event: &corev1.Event_EventsPageFetched{
			EventsPageFetched: &corev1.EventsPageFetchedEvent{
				OpId:      fetchOp,
				SessionId: "s1",
				Events:    []*corev1.PlainEvent{{Seq: 1, Verified: true, ApproxBytes: 64, Event: &v1.Event{Seq: 1, SessionId: "s1"}}},
			},
		},
	})
	readPCID, hsNow := connectDirectForExistingEngine(t, e, now+75, client, m1Actor)
	now = hsNow

	// Direct batch [2, 4] when prefix=1 accepts only contiguous 2 and drops gapped 4;
	// direct [1] (already in prefix) is also ignored; direct [3] later succeeds.
	e.HandleEvent(&corev1.Event{
		NowMs: now + 10,
		Event: &corev1.Event_DirectEventsReceived{
			DirectEventsReceived: &corev1.DirectEventsReceivedEvent{
				MachineId: m1,
				SessionId: "s1",
				PcId:      readPCID,
				Sender:    m1,
				Events: []*corev1.PlainEvent{
					{Seq: 1, Verified: true, ApproxBytes: 64, Event: &v1.Event{Seq: 1, SessionId: "s1"}},
					{Seq: 2, Verified: true, ApproxBytes: 64, Event: &v1.Event{Seq: 2, SessionId: "s1"}},
					{Seq: 4, Verified: true, ApproxBytes: 64, Event: &v1.Event{Seq: 4, SessionId: "s1"}},
				},
			},
		},
	})
	buf := e.Buffer("s1")
	if buf.PrefixLen() != 2 || buf.PendingCount() != 0 {
		t.Fatalf("expected prefix=2 pending=0, got prefix=%d pending=%d", buf.PrefixLen(), buf.PendingCount())
	}

	// Relay EventsAppended([2]) clears fetchErrorReason when ServerLastSeq=2 and !hasServerGap(2).
	buf.fetchErrorReason = world.PlainReason("fetch error")
	_, appRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 90,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: legID,
				Frame: &corev1.StreamFrameEvent_EventsAppended{
					EventsAppended: &corev1.EventsAppendedFrame{
						SessionId: "s1",
						Events:    []*corev1.PlainEvent{{Seq: 2, Verified: true, ApproxBytes: 64, Event: &v1.Event{Seq: 2, SessionId: "s1"}}},
					},
				},
			},
		},
	})
	if !buf.fetchErrorReason.IsZero() {
		t.Fatalf("expected fetchErrorReason cleared when !hasServerGap")
	}
	for _, eff := range appRes.Effects {
		if eff.GetRpc().GetGetEvents() != nil {
			t.Fatalf("must not fire premature GetEvents when ServerLastSeq=2")
		}
	}
}

func TestStreamFrameDeviceAndMachineSecuritySync(t *testing.T) {
	now := int64(1700000000000)
	cSelf := newSyncTestActor(t, 51)
	cKnown := newSyncTestActor(t, 52)
	mKnown := newSyncTestActor(t, 53)
	mArchive := newSyncTestActor(t, 54)
	recActor := newSyncTestActor(t, 55)
	dom := devicesec.Digest("stream-sync-dom", []byte{1})
	sec1 := bytes.Repeat([]byte{0x11}, 32)
	sec2 := bytes.Repeat([]byte{0x22}, 32)
	sec3 := bytes.Repeat([]byte{0x33}, 32)
	sec4 := bytes.Repeat([]byte{0x44}, 32)
	sec5 := bytes.Repeat([]byte{0x55}, 32)
	u0, err := devicesec.NewGenesis(cSelf.id, dom, cSelf.member(devicesec.Client), [32]byte(recActor.edP), sec1, uint64(now), syncTestHPKE{})
	if err != nil {
		t.Fatal(err)
	}
	sb0, _ := u0.SigningBytes()
	u0.Signature = ed25519.Sign(cSelf.ed, sb0)
	s0Raw, _ := u0.MarshalBinary()
	s0, _ := devicesec.ParseState(s0Raw)
	gen, _ := s0.Hash()
	u1, err := devicesec.NewMembershipState(s0, []devicesec.Member{
		cSelf.member(devicesec.Client),
		cKnown.member(devicesec.Client),
	}, nil, sec1, sec2, uint64(now+1), syncTestHPKE{}, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sb1, _ := u1.SigningBytes()
	u1.Signature = ed25519.Sign(cSelf.ed, sb1)
	s1Raw, _ := u1.MarshalBinary()
	s1, _ := devicesec.ParseState(s1Raw)
	u2, err := devicesec.NewMembershipState(s1, []devicesec.Member{
		cSelf.member(devicesec.Client),
		cKnown.member(devicesec.Client),
		mKnown.member(devicesec.Machine),
	}, nil, sec2, sec3, uint64(now+2), syncTestHPKE{}, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sb2, _ := u2.SigningBytes()
	u2.Signature = ed25519.Sign(cSelf.ed, sb2)
	s2Raw, _ := u2.MarshalBinary()
	s2, _ := devicesec.ParseState(s2Raw)
	u3, err := devicesec.NewMembershipState(s2, []devicesec.Member{
		cSelf.member(devicesec.Client),
		cKnown.member(devicesec.Client),
		mKnown.member(devicesec.Machine),
		mArchive.member(devicesec.Machine),
	}, nil, sec3, sec4, uint64(now+3), syncTestHPKE{}, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sb3, _ := u3.SigningBytes()
	u3.Signature = ed25519.Sign(cSelf.ed, sb3)
	s3Raw, _ := u3.MarshalBinary()
	s3, _ := devicesec.ParseState(s3Raw)
	u4, err := devicesec.NewMembershipState(s3, []devicesec.Member{
		cSelf.member(devicesec.Client),
		cKnown.member(devicesec.Client),
		mKnown.member(devicesec.Machine),
	}, []string{mArchive.id}, sec4, sec5, uint64(now+4), syncTestHPKE{}, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sb4, _ := u4.SigningBytes()
	u4.Signature = ed25519.Sign(cSelf.ed, sb4)
	s4Raw, _ := u4.MarshalBinary()

	setup := func() (*Engine, uint64) {
		e := NewEngine()
		bootstrapTestSecurityRecord(t, e, cSelf, now, &security.PersistedRecord{
			Pin:      devicesec.Pin{Account: cSelf.id, Domain: dom, Genesis: gen},
			MemberID: cSelf.id,
			Member:   true,
			Server:   "https://relay.example.com",
			States:   [][]byte{s0Raw, s1Raw, s2Raw, s3Raw, s4Raw},
			FloorMs:  now + 100_000,
		}, "tok-stream")
		e.running = true
		e.legID = 7
		return e, 7
	}

	cases := []struct {
		name     string
		frame    *corev1.StreamFrameEvent
		wantSync bool
	}{
		{
			name: "new_machine_device_updated",
			frame: &corev1.StreamFrameEvent{
				Frame: &corev1.StreamFrameEvent_DeviceUpdated{
					DeviceUpdated: &corev1.PlainDevice{DeviceId: "m-new", Kind: v1.DeviceKind_DEVICE_KIND_MACHINE},
				},
			},
			wantSync: true,
		},
		{
			name: "trusted_machine_device_updated",
			frame: &corev1.StreamFrameEvent{
				Frame: &corev1.StreamFrameEvent_DeviceUpdated{
					DeviceUpdated: &corev1.PlainDevice{DeviceId: mKnown.id, Kind: v1.DeviceKind_DEVICE_KIND_MACHINE},
				},
			},
			wantSync: false,
		},
		{
			name: "new_client_device_updated",
			frame: &corev1.StreamFrameEvent{
				Frame: &corev1.StreamFrameEvent_DeviceUpdated{
					DeviceUpdated: &corev1.PlainDevice{DeviceId: "c-new", Kind: v1.DeviceKind_DEVICE_KIND_CLIENT},
				},
			},
			wantSync: true,
		},
		{
			name: "trusted_client_device_updated",
			frame: &corev1.StreamFrameEvent{
				Frame: &corev1.StreamFrameEvent_DeviceUpdated{
					DeviceUpdated: &corev1.PlainDevice{DeviceId: cKnown.id, Kind: v1.DeviceKind_DEVICE_KIND_CLIENT},
				},
			},
			wantSync: false,
		},
		{
			name: "untrusted_online_machine_updated",
			frame: &corev1.StreamFrameEvent{
				Frame: &corev1.StreamFrameEvent_MachineUpdated{
					MachineUpdated: &corev1.PlainMachine{MachineId: "m-new", Online: true},
				},
			},
			wantSync: true,
		},
		{
			name: "untrusted_offline_machine_updated",
			frame: &corev1.StreamFrameEvent{
				Frame: &corev1.StreamFrameEvent_MachineUpdated{
					MachineUpdated: &corev1.PlainMachine{MachineId: "m-off", Online: false},
				},
			},
			wantSync: false,
		},
		{
			name: "trusted_online_machine_updated",
			frame: &corev1.StreamFrameEvent{
				Frame: &corev1.StreamFrameEvent_MachineUpdated{
					MachineUpdated: &corev1.PlainMachine{MachineId: mKnown.id, Online: true},
				},
			},
			wantSync: false,
		},
		{
			name: "archive_online_machine_updated",
			frame: &corev1.StreamFrameEvent{
				Frame: &corev1.StreamFrameEvent_MachineUpdated{
					MachineUpdated: &corev1.PlainMachine{MachineId: mArchive.id, Online: true},
				},
			},
			wantSync: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, legID := setup()
			frame := tc.frame
			frame.LegId = legID
			_, res := e.HandleEvent(&corev1.Event{
				NowMs: now + 10,
				Event: &corev1.Event_StreamFrame{StreamFrame: frame},
			})
			var syncOp uint64
			for _, eff := range res.Effects {
				if sr := eff.GetSecurityRpc(); sr != nil && sr.Method == corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_SECURITY_STATES {
					syncOp = sr.OpId
				}
			}
			if tc.wantSync && syncOp == 0 {
				t.Fatalf("expected GET_SECURITY_STATES SecurityRpcEffect for %s", tc.name)
			}
			if !tc.wantSync && syncOp != 0 {
				t.Fatalf("unexpected GET_SECURITY_STATES SecurityRpcEffect op=%d for %s", syncOp, tc.name)
			}
		})
	}

	// Verify end-to-end core flow: untrusted direct events are dropped before sync refresh,
	// and accepted with a valid confirmed direct PC after security refresh adds the newly paired machine.
	e, legID := setup()
	client := newDirectTestActor(t, 61, 1)
	mNewActor := newDirectTestActor(t, 62, 2)
	mNew := mNewActor.id
	e.w.LivePushConnected = true
	e.HandleEvent(&corev1.Event{
		NowMs: now + 5,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: legID,
				Frame: &corev1.StreamFrameEvent_SessionUpdated{
					SessionUpdated: &corev1.PlainSession{SessionId: "s-new", MachineId: mNew, UpdatedMs: now},
				},
			},
		},
	})
	_, watchRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 6,
		Event: &corev1.Event_SessionWatch{SessionWatch: &corev1.SessionWatchEvent{SessionId: "s-new", Watched: true}},
	})
	var cacheOp uint64
	for _, eff := range watchRes.Effects {
		if cr := eff.GetCacheRead().GetEvents(); cr != nil {
			cacheOp = cr.OpId
		}
	}
	e.HandleEvent(&corev1.Event{
		NowMs: now + 7,
		Event: &corev1.Event_CacheEventsLoaded{CacheEventsLoaded: &corev1.CacheEventsLoadedEvent{OpId: cacheOp, SessionId: "s-new"}},
	})
	e.HandleEvent(&corev1.Event{
		NowMs: now + 8,
		Event: &corev1.Event_DirectEventsReceived{
			DirectEventsReceived: &corev1.DirectEventsReceivedEvent{
				MachineId: mNew,
				SessionId: "s-new",
				Events:    []*corev1.PlainEvent{{Seq: 1, Verified: true, ApproxBytes: 32, Event: &v1.Event{Seq: 1, SessionId: "s-new"}}},
			},
		},
	})
	buf := e.Buffer("s-new")
	if buf.PrefixLen() != 0 {
		t.Fatalf("expected untrusted %s direct event to be dropped before security refresh, got prefix=%d", mNew, buf.PrefixLen())
	}
	_, devRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 8,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: legID,
				Frame: &corev1.StreamFrameEvent_DeviceUpdated{
					DeviceUpdated: &corev1.PlainDevice{DeviceId: mNew, Kind: v1.DeviceKind_DEVICE_KIND_MACHINE},
				},
			},
		},
	})
	var secOp uint64
	for _, eff := range devRes.Effects {
		if sr := eff.GetSecurityRpc(); sr != nil && sr.Method == corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_SECURITY_STATES {
			secOp = sr.OpId
		}
	}
	if secOp == 0 {
		t.Fatalf("expected GET_SECURITY_STATES SecurityRpcEffect on DeviceUpdated")
	}
	readPCID, hsNow := connectDirectForExistingEngine(t, e, now+10, client, mNewActor)
	e.HandleEvent(&corev1.Event{
		NowMs: hsNow + 5,
		Event: &corev1.Event_DirectEventsReceived{
			DirectEventsReceived: &corev1.DirectEventsReceivedEvent{
				MachineId: mNew,
				SessionId: "s-new",
				PcId:      readPCID,
				Sender:    mNew,
				Events:    []*corev1.PlainEvent{{Seq: 1, Verified: true, ApproxBytes: 32, Event: &v1.Event{Seq: 1, SessionId: "s-new"}}},
			},
		},
	})
	if buf.PrefixLen() != 1 {
		t.Fatalf("expected direct event from %s to be ingested after SecuritySnapshot refresh, got prefix=%d", mNew, buf.PrefixLen())
	}
}

func TestEventRevMonotonicAcrossTrimHistoryAndSnapshotCache(t *testing.T) {
	now := int64(1_700_000_000_000)
	e := NewEngine()
	_, listsOp := startAndConnect(t, e, now)
	e.HandleEvent(&corev1.Event{
		NowMs: now + 40,
		Event: &corev1.Event_ListsFetched{
			ListsFetched: &corev1.ListsFetchedEvent{
				OpId:     listsOp,
				Machines: []*corev1.PlainMachine{{MachineId: "m1", Online: true}},
				Sessions: []*corev1.PlainSession{{SessionId: "s1", MachineId: "m1", LastSeq: 1, UpdatedMs: now}},
			},
		},
	})
	_, watchRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 50,
		Event: &corev1.Event_SessionWatch{SessionWatch: &corev1.SessionWatchEvent{SessionId: "s1", Watched: true}},
	})
	var cacheOp uint64
	for _, eff := range watchRes.Effects {
		if cr := eff.GetCacheRead().GetEvents(); cr != nil {
			cacheOp = cr.OpId
		}
	}
	e.HandleEvent(&corev1.Event{
		NowMs: now + 60,
		Event: &corev1.Event_CacheEventsLoaded{
			CacheEventsLoaded: &corev1.CacheEventsLoadedEvent{
				OpId:      cacheOp,
				SessionId: "s1",
				Events: []*corev1.PlainEvent{{
					Seq: 1, Verified: true, ApproxBytes: 64,
					Event: &v1.Event{Seq: 1, SessionId: "s1", Kind: &v1.Event_UserMessage{UserMessage: &v1.UserMessage{Text: "hi"}}},
				}},
			},
		},
	})

	snap1 := e.SessionSnapshot("s1")
	snap2 := e.SessionSnapshot("s1")
	if snap1.EventRev == 0 || snap1.EventRev != snap2.EventRev {
		t.Fatalf("expected positive stable EventRev, got %d vs %d", snap1.EventRev, snap2.EventRev)
	}
	if snap1.Projection == nil || snap1.Projection != snap2.Projection {
		t.Fatalf("expected cached ProjectionState pointer reuse across snapshots")
	}

	// Unwatch and advance monotonic clock past HistoryIdleMs so trimHistory evicts buffer.
	e.HandleEvent(&corev1.Event{
		NowMs: now + 70,
		Event: &corev1.Event_SessionWatch{SessionWatch: &corev1.SessionWatchEvent{SessionId: "s1", Watched: false}},
	})
	e.HandleEvent(&corev1.Event{
		NowMs:  now + HistoryIdleMs + 1000,
		MonoMs: e.MonoMs() + HistoryIdleMs + 1000,
		Event: &corev1.Event_TimerFired{TimerFired: &corev1.TimerFiredEvent{
			TimerId: e.timers[timerKey{kind: corev1.TimerKind_TIMER_KIND_HISTORY_SWEEP}].id,
			Kind:    corev1.TimerKind_TIMER_KIND_HISTORY_SWEEP,
		}},
	})
	if e.Buffer("s1") != nil {
		t.Fatalf("expected trimHistory to evict idle buffer")
	}

	// Re-watch s1: new EventBuffer must receive strictly higher EventRev than snap1.EventRev.
	_, reWatchRes := e.HandleEvent(&corev1.Event{
		NowMs: now + HistoryIdleMs + 1001,
		Event: &corev1.Event_SessionWatch{SessionWatch: &corev1.SessionWatchEvent{SessionId: "s1", Watched: true}},
	})
	var cacheOp2 uint64
	for _, eff := range reWatchRes.Effects {
		if cr := eff.GetCacheRead().GetEvents(); cr != nil {
			cacheOp2 = cr.OpId
		}
	}
	e.HandleEvent(&corev1.Event{
		NowMs: now + HistoryIdleMs + 1002,
		Event: &corev1.Event_CacheEventsLoaded{
			CacheEventsLoaded: &corev1.CacheEventsLoadedEvent{
				OpId:      cacheOp2,
				SessionId: "s1",
				Events: []*corev1.PlainEvent{{
					Seq: 1, Verified: true, ApproxBytes: 64,
					Event: &v1.Event{Seq: 1, SessionId: "s1", Kind: &v1.Event_UserMessage{UserMessage: &v1.UserMessage{Text: "hi"}}},
				}},
			},
		},
	})
	snap3 := e.SessionSnapshot("s1")
	if snap3.EventRev <= snap1.EventRev {
		t.Fatalf("expected EventRev to remain monotonic across trimHistory eviction (%d <= %d)", snap3.EventRev, snap1.EventRev)
	}
}
