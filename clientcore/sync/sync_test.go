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
	"github.com/Lewin671/remote-agent/clientcore/internal/test/testrelay"
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

func dispatchEngine(e *Engine) func(*corev1.Event) []*corev1.Effect {
	return func(ev *corev1.Event) []*corev1.Effect {
		_, res := e.HandleEvent(ev)
		return res.Effects
	}
}

func deliverRefreshRelay(
	e *Engine,
	nowMs int64,
	ops testrelay.RefreshOps,
	machines []*v1.Machine,
	sessions []*v1.RemoteSession,
	devices []*v1.Device,
	next *v1.SessionCursor,
) StepResult {
	e.HandleEvent(&corev1.Event{
		NowMs: nowMs,
		Event: &corev1.Event_RelayRpcResult{RelayRpcResult: &corev1.RelayRpcResultEvent{
			OpId:          ops.Machines,
			ResponseBytes: testrelay.MarshalListMachinesResp(machines),
		}},
	})
	e.HandleEvent(&corev1.Event{
		NowMs: nowMs,
		Event: &corev1.Event_RelayRpcResult{RelayRpcResult: &corev1.RelayRpcResultEvent{
			OpId:          ops.Sessions,
			ResponseBytes: testrelay.MarshalListSessionsResp(sessions, next),
		}},
	})
	_, res := e.HandleEvent(&corev1.Event{
		NowMs: nowMs,
		Event: &corev1.Event_RelayRpcResult{RelayRpcResult: &corev1.RelayRpcResultEvent{
			OpId:          ops.Devices,
			ResponseBytes: testrelay.MarshalListDevicesResp(devices),
		}},
	})
	return res
}

func deliverEventsPageRelay(e *Engine, nowMs int64, opID uint64, events []*v1.StoredEvent, more bool) StepResult {
	_, res := e.HandleEvent(&corev1.Event{
		NowMs: nowMs,
		Event: &corev1.Event_RelayRpcResult{RelayRpcResult: &corev1.RelayRpcResultEvent{
			OpId:          opID,
			ResponseBytes: testrelay.MarshalGetEventsResp(events, more),
		}},
	})
	return res
}

func startAndConnectWithFixture(t *testing.T, e *Engine, fx *testrelay.Fixture, nowMs int64) (legID uint64, ops testrelay.RefreshOps) {
	t.Helper()
	ids, fs := seedEntropy(20)
	if !e.secEngine.Locked() {
		fx.Bootstrap(dispatchEngine(e), nowMs)
	}
	e.secEngine.SetAuthToken("tok-1", nowMs+7*86_400_000)
	respondSyncRpc := func(effs []*corev1.Effect) {
		for _, eff := range effs {
			if sr := eff.GetRelayRpc(); sr != nil && sr.Method == corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_SECURITY_STATES {
				emptyStatesRaw, _ := proto.Marshal(&stdv1.GetSecurityStatesResponse{HeadEpoch: e.security.HeadEpoch})
				e.HandleEvent(&corev1.Event{
					NowMs: nowMs + 25,
					Event: &corev1.Event_RelayRpcResult{
						RelayRpcResult: &corev1.RelayRpcResultEvent{OpId: sr.OpId, ResponseBytes: emptyStatesRaw},
					},
				})
			}
		}
	}
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
	respondSyncRpc(res.Effects)
	for _, eff := range res.Effects {
		if os := eff.GetOpenStream(); os != nil {
			legID = os.LegId
		}
	}
	if legID == 0 {
		t.Fatalf("expected OpenStreamEffect on Start")
	}
	_, readyRes := e.HandleEvent(&corev1.Event{
		NowMs: nowMs + 30,
		Event: &corev1.Event_StreamReady{
			StreamReady: &corev1.StreamReadyEvent{LegId: legID, ServerTimeMs: nowMs + 30},
		},
	})
	respondSyncRpc(readyRes.Effects)
	ops = testrelay.MustFindRefreshOps(t, readyRes.Effects)
	return legID, ops
}

func startAndConnect(t *testing.T, e *Engine, nowMs int64) (*testrelay.Fixture, uint64, testrelay.RefreshOps) {
	t.Helper()
	fx := testrelay.New(t, nowMs)
	legID, ops := startAndConnectWithFixture(t, e, fx, nowMs)
	return fx, legID, ops
}

func directActorFrom(t *testing.T, a testrelay.Actor) *directTestActor {
	d := &directTestActor{
		t:        t,
		id:       a.ID,
		signPriv: a.Ed,
		xPriv:    a.X,
		xPub:     a.XPub,
	}
	copy(d.signPub[:], a.EdPub)
	return d
}

func TestConnectionLifecycleRecoveryAndLiveness(t *testing.T) {
	e := NewEngine()
	now := int64(1_700_000_000_000)
	_, legID, _ := startAndConnect(t, e, now)

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

	// Heartbeat calibrates LegLiveness: 1s cadence uses the 15s floor (tolerating
	// transient high-RTT/lossy stalls without closing the client stream).
	hbFrame := &v1.ServerFrame{Kind: &v1.ServerFrame_Heartbeat{Heartbeat: &v1.Heartbeat{}}}
	e.HandleEvent(&corev1.Event{
		NowMs:  now + 1000,
		MonoMs: 2_000,
		Event:  testrelay.RawFrame(legID, hbFrame),
	})
	e.HandleEvent(&corev1.Event{
		NowMs:  now + 2000,
		MonoMs: 3_000,
		Event:  testrelay.RawFrame(legID, hbFrame),
	})
	if got := e.liveness.ThresholdMs(); got != 15_000 {
		t.Fatalf("expected liveness threshold 15000ms, got %d", got)
	}

	// A 10s transient silence must NOT close the stream (old 3s threshold would have closed it).
	lt := e.timers[timerKey{kind: corev1.TimerKind_TIMER_KIND_LIVENESS}]
	_, transientRes := e.HandleEvent(&corev1.Event{
		NowMs:  now + 12_000,
		MonoMs: 13_000,
		Event: &corev1.Event_TimerFired{
			TimerFired: &corev1.TimerFiredEvent{TimerId: lt.id, Kind: lt.kind},
		},
	})
	for _, eff := range transientRes.Effects {
		if eff.GetCloseStream() != nil {
			t.Fatalf("client stream closed prematurely after 10s silence")
		}
	}

	// Advance past 15000ms silence -> triggers CloseStreamEffect.
	lt = e.timers[timerKey{kind: corev1.TimerKind_TIMER_KIND_LIVENESS}]
	_, livenessRes := e.HandleEvent(&corev1.Event{
		NowMs:  now + 18_500,
		MonoMs: 18_500,
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
	fx2 := testrelay.New(t, now+500_000)
	fx2.Bootstrap(dispatchEngine(e2), now+500_000)
	e2.secEngine.SetAuthToken("tok-2", now+7*86_400_000)
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
	var leg2 uint64
	for _, eff := range res.Effects {
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
	fx3 := testrelay.New(t, now)
	fx3.Bootstrap(dispatchEngine(e3), now)
	e3.running = true
	e3.legID = 7
	e3.w.World.Connection = lists.ConnConnecting
	l3 := linkcore.NewLiveness(1_000)
	e3.liveness = &l3
	e3.HandleEvent(&corev1.Event{
		NowMs:  now,
		MonoMs: 1_500,
		Event: testrelay.RawFrame(7, &v1.ServerFrame{
			Kind: &v1.ServerFrame_MachineUpdated{
				MachineUpdated: &v1.MachineUpdated{Machine: fx3.MachineWire(fx3.M1, true, 0, nil)},
			},
		}),
	})
	if e3.Connection() != lists.ConnConnected {
		t.Fatalf("expected non-Ready StreamRawFrame during ConnConnecting to promote to ConnConnected, got %v", e3.Connection())
	}

	// PolicyViolation close clears cached auth token via SecurityTokenUpdatedEffect{Cleared: true}.
	e4 := NewEngine()
	e4.running = true
	e4.legID = 8
	e4.cachedToken = "secret-tok"
	e4.secEngine.SetAuthToken("secret-tok", now+3*86_400_000)
	_, pRes := e4.HandleEvent(&corev1.Event{
		NowMs: now + 100,
		Event: &corev1.Event_StreamClosed{
			StreamClosed: &corev1.StreamClosedEvent{LegId: 8, PolicyViolation: true},
		},
	})
	var cleared bool
	for _, eff := range pRes.Effects {
		if tu := eff.GetSecurityTokenUpdated(); tu != nil && tu.Cleared {
			cleared = true
		}
	}
	if !cleared || e4.cachedToken != "" {
		t.Fatalf("expected SecurityTokenUpdatedEffect{Cleared:true} on PolicyViolation stream close, cleared=%v tok=%q", cleared, e4.cachedToken)
	}
}

func TestForceLoginSemantics(t *testing.T) {
	e := NewEngine()
	now := int64(1_700_000_000_000)
	actor := newSyncTestActor(t, 41)
	e.HandleEvent(&corev1.Event{
		NowMs: now,
		Event: &corev1.Event_SecurityConfig{
			SecurityConfig: &corev1.SecurityConfigEvent{Account: actor.id, DeviceId: actor.id, Server: "https://relay.example.com"},
		},
	})
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
	var bootOp uint64
	for _, eff := range startRes.Effects {
		if st := eff.GetSecurityStorage(); st != nil && st.Op == corev1.SecurityStorageOp_SECURITY_STORAGE_OP_READ_BOOTSTRAP {
			bootOp = st.OpId
		}
	}
	if bootOp == 0 {
		t.Fatalf("expected cold START to read warm token via READ_BOOTSTRAP (forceLogin=false)")
	}

	_, tokRes := e.HandleEvent(&corev1.Event{
		NowMs:  now + 10,
		MonoMs: 1_010,
		Event: &corev1.Event_SecurityStorageResult{
			SecurityStorageResult: &corev1.SecurityStorageResultEvent{
				OpId:                bootOp,
				LoginToken:          "tok-stale",
				LoginTokenExpiresMs: now + 3*86_400_000,
			},
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
	var chalOp uint64
	var reconnectTimer uint64
	var reconnectDelay int64
	for _, eff := range pvRes.Effects {
		if r := eff.GetRelayRpc(); r != nil && r.Method == corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_CHALLENGE {
			chalOp = r.OpId
		}
		if st := eff.GetScheduleTimer(); st != nil && st.Kind == corev1.TimerKind_TIMER_KIND_RECONNECT {
			reconnectTimer = st.TimerId
			reconnectDelay = st.DelayMs
		}
	}
	if chalOp == 0 && reconnectTimer != 0 {
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
			if r := eff.GetRelayRpc(); r != nil && r.Method == corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_CHALLENGE {
				chalOp = r.OpId
			}
		}
	}
	if chalOp == 0 || !e.forceLogin {
		t.Fatalf("expected PolicyViolation reconnect to force fresh login (GET_CHALLENGE), got chalOp=%d forceLogin=%v", chalOp, e.forceLogin)
	}

	_, chalRes := e.HandleEvent(&corev1.Event{
		NowMs:  now + 30,
		MonoMs: 1_030,
		Event: &corev1.Event_RelayRpcResult{
			RelayRpcResult: &corev1.RelayRpcResultEvent{
				OpId:          chalOp,
				ResponseBytes: testrelay.MarshalStd(&stdv1.GetChallengeResponse{Challenge: bytes.Repeat([]byte{0x21}, 32)}),
			},
		},
	})
	var signEff *corev1.SecurityKeySignEffect
	for _, eff := range chalRes.Effects {
		if ks := eff.GetSecurityKeySign(); ks != nil {
			signEff = ks
		}
	}
	if signEff == nil {
		t.Fatalf("expected SecurityKeySignEffect after GET_CHALLENGE")
	}
	_, signRes := e.HandleEvent(&corev1.Event{
		NowMs:  now + 31,
		MonoMs: 1_031,
		Event: &corev1.Event_SecurityKeySigned{
			SecurityKeySigned: &corev1.SecurityKeySignedEvent{OpId: signEff.OpId, Signature: ed25519.Sign(actor.ed, signEff.Message)},
		},
	})
	loginRpc := testrelay.FindRelayRpc(signRes.Effects, corev1.RelayRpcMethod_RELAY_RPC_METHOD_LOGIN)
	if loginRpc == nil {
		t.Fatalf("expected LOGIN RelayRpc after sign")
	}
	_, retryTokRes := e.HandleEvent(&corev1.Event{
		NowMs:  now + 32,
		MonoMs: 1_032,
		Event: &corev1.Event_RelayRpcResult{
			RelayRpcResult: &corev1.RelayRpcResultEvent{
				OpId:          loginRpc.OpId,
				ResponseBytes: testrelay.MarshalStd(&stdv1.LoginResponse{Token: "tok-fresh", ExpiresMs: now + 7*86_400_000}),
			},
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
		t.Fatalf("expected forceLogin cleared after login succeeded")
	}
}

func TestPaginationAndPushRemovalRaces(t *testing.T) {
	e := NewEngine()
	now := int64(1_700_000_000_000)
	fx, legID, listsOps := startAndConnect(t, e, now)

	// Initial seed: m1, m2, plus s_live, s_page2, s_gone.
	deliverRefreshRelay(e, now+40, listsOps,
		[]*v1.Machine{
			fx.MachineWire(fx.M1, true, 0, nil),
			fx.MachineWire(fx.M2, true, 0, nil),
		},
		[]*v1.RemoteSession{
			fx.SessionWire("s_live", fx.M1, 0, now, 0, nil, nil),
			fx.SessionWire("s_page2", fx.M1, 0, 500, 500, nil, nil),
			fx.SessionWire("s_gone", fx.M1, 0, 900, 900, nil, nil),
		},
		[]*v1.Device{
			fx.DeviceWire(fx.Client.ID, v1.DeviceKind_DEVICE_KIND_CLIENT, 0, 0, 1, &v1.DeviceMeta{Name: "initial-name"}),
			fx.DeviceWire("d_removed", v1.DeviceKind_DEVICE_KIND_CLIENT, 0, 0, 1, nil),
		},
		&v1.SessionCursor{EndedMs: 400, SessionId: "c"},
	)

	// Request page 2, then trigger a newer Refresh BEFORE page 2 responds.
	pageRes := e.Run(func(ctx *StepContext) {
		e.RequestMoreSessions(ctx)
	})
	pageRpc := testrelay.FindRelayRpc(pageRes.Effects, corev1.RelayRpcMethod_RELAY_RPC_METHOD_LIST_REMOTE_SESSIONS)
	if pageRpc == nil {
		t.Fatalf("expected LIST_REMOTE_SESSIONS RelayRpc for page 2")
	}

	refreshRes := e.Run(func(ctx *StepContext) {
		e.startRefresh(ctx)
	})
	refreshOps := testrelay.MustFindRefreshOps(t, refreshRes.Effects)

	// While refresh is in flight:
	// 1. m2 is pushed online=false, then restored online=true.
	// 2. s_pushed arrives on stream.
	// 3. m3 is added and then immediately removed (removedPushes).
	e.HandleEvent(&corev1.Event{
		NowMs: now + 60,
		Event: testrelay.RawFrame(legID, &v1.ServerFrame{
			Kind: &v1.ServerFrame_MachineUpdated{MachineUpdated: &v1.MachineUpdated{Machine: fx.MachineWire(fx.M2, false, 0, nil)}},
		}),
	})
	e.HandleEvent(&corev1.Event{
		NowMs: now + 65,
		Event: testrelay.RawFrame(legID, &v1.ServerFrame{
			Kind: &v1.ServerFrame_SessionUpdated{SessionUpdated: &v1.SessionUpdated{Session: fx.SessionWire("s_pushed", fx.M1, 0, now+65, 0, nil, nil)}},
		}),
	})
	e.HandleEvent(&corev1.Event{
		NowMs: now + 70,
		Event: testrelay.RawFrame(legID, &v1.ServerFrame{
			Kind: &v1.ServerFrame_MachineRemoved{MachineRemoved: &v1.MachineRemoved{MachineId: fx.M3}},
		}),
	})
	e.HandleEvent(&corev1.Event{
		NowMs: now + 72,
		Event: testrelay.RawFrame(legID, &v1.ServerFrame{
			Kind: &v1.ServerFrame_DeviceUpdated{DeviceUpdated: &v1.DeviceUpdated{Device: fx.DeviceWire("d_pushed", v1.DeviceKind_DEVICE_KIND_MACHINE, 0, 0, 1, nil)}},
		}),
	})
	e.HandleEvent(&corev1.Event{
		NowMs: now + 74,
		Event: testrelay.RawFrame(legID, &v1.ServerFrame{
			Kind: &v1.ServerFrame_DeviceUpdated{DeviceUpdated: &v1.DeviceUpdated{Device: fx.DeviceWire(fx.Client.ID, v1.DeviceKind_DEVICE_KIND_CLIENT, 0, 0, 2, &v1.DeviceMeta{Name: "live-name"})}},
		}),
	})
	e.HandleEvent(&corev1.Event{
		NowMs: now + 76,
		Event: testrelay.RawFrame(legID, &v1.ServerFrame{
			Kind: &v1.ServerFrame_DeviceRemoved{DeviceRemoved: &v1.DeviceRemoved{DeviceId: "d_removed"}},
		}),
	})

	// Server's stale snapshot returns: contains m1, m3(stale!), omits m2, s_pushed, and d_pushed, next=EndedMs:700.
	// So s_page2 (500 < 700) is past cursor (kept in memory, unlisted), s_gone (900 > 700) was deleted (unlisted/removed).
	dListed := fx.Machines[3].ID
	listsRes := deliverRefreshRelay(e, now+80, refreshOps,
		[]*v1.Machine{
			fx.MachineWire(fx.M1, true, 0, nil),
			fx.MachineWire(fx.M3, true, 0, nil), // pushed removed! must NOT resurrect
		},
		[]*v1.RemoteSession{
			fx.SessionWire("s_live", fx.M1, 0, now, 0, nil, nil),
		},
		[]*v1.Device{
			fx.DeviceWire(fx.Client.ID, v1.DeviceKind_DEVICE_KIND_CLIENT, 0, 0, 1, &v1.DeviceMeta{Name: "stale-name"}),
			fx.DeviceWire("d_removed", v1.DeviceKind_DEVICE_KIND_CLIENT, 0, 0, 1, nil), // pushed removed! must NOT resurrect
			fx.DeviceWire(dListed, v1.DeviceKind_DEVICE_KIND_MACHINE, 0, 0, 1, nil),
		},
		&v1.SessionCursor{EndedMs: 700, SessionId: "z"},
	)

	if e.MachineEntry(fx.M2) == nil {
		t.Fatalf("pushed machine m2 should survive ListsFetched omission")
	}
	if e.MachineEntry(fx.M3) != nil {
		t.Fatalf("removed machine m3 must not be resurrected by stale ListsFetched")
	}
	if e.devices["d_pushed"] == nil {
		t.Fatalf("pushed device d_pushed should survive ListsFetched omission")
	}
	if got := e.devices[fx.Client.ID]; got == nil || got.MetaRev != 2 || got.Meta.GetName() != "live-name" {
		t.Fatalf("live-pushed device d_keep metadata should beat stale ListsFetched, got %+v", got)
	}
	if e.devices["d_removed"] != nil {
		t.Fatalf("removed device d_removed must not be resurrected by stale ListsFetched")
	}
	if e.devices[dListed] == nil {
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
	devRpc := testrelay.FindRelayRpc(devRes.Effects, corev1.RelayRpcMethod_RELAY_RPC_METHOD_LIST_DEVICES)
	if devRpc == nil {
		t.Fatalf("expected LIST_DEVICES RelayRpc")
	}
	e.HandleEvent(&corev1.Event{
		NowMs: now + 82,
		Event: testrelay.RawFrame(legID, &v1.ServerFrame{
			Kind: &v1.ServerFrame_DeviceUpdated{DeviceUpdated: &v1.DeviceUpdated{Device: fx.DeviceWire("d_dev_pushed", v1.DeviceKind_DEVICE_KIND_MACHINE, 0, 0, 1, nil)}},
		}),
	})
	e.HandleEvent(&corev1.Event{
		NowMs: now + 84,
		Event: testrelay.RawFrame(legID, &v1.ServerFrame{
			Kind: &v1.ServerFrame_DeviceUpdated{DeviceUpdated: &v1.DeviceUpdated{Device: fx.DeviceWire("d_dev_bounce", v1.DeviceKind_DEVICE_KIND_CLIENT, 0, 0, 1, nil)}},
		}),
	})
	e.HandleEvent(&corev1.Event{
		NowMs: now + 86,
		Event: testrelay.RawFrame(legID, &v1.ServerFrame{
			Kind: &v1.ServerFrame_DeviceRemoved{DeviceRemoved: &v1.DeviceRemoved{DeviceId: "d_dev_bounce"}},
		}),
	})
	e.HandleEvent(&corev1.Event{
		NowMs: now + 88,
		Event: &corev1.Event_RelayRpcResult{
			RelayRpcResult: &corev1.RelayRpcResultEvent{
				OpId: devRpc.OpId,
				ResponseBytes: testrelay.MarshalListDevicesResp([]*v1.Device{
					fx.DeviceWire(fx.M2, v1.DeviceKind_DEVICE_KIND_MACHINE, 0, 0, 1, nil),
					fx.DeviceWire("d_dev_bounce", v1.DeviceKind_DEVICE_KIND_CLIENT, 0, 0, 1, nil),
				}),
			},
		},
	})
	if e.devices["d_dev_pushed"] == nil {
		t.Fatalf("pushed device d_dev_pushed should survive in-flight FetchDevices response")
	}
	if e.devices[fx.M2] == nil {
		t.Fatalf("expected d_dev_listed from DevicesFetched")
	}
	if e.devices["d_dev_bounce"] != nil {
		t.Fatalf("updated-then-removed device d_dev_bounce must stay removed after DevicesFetched")
	}

	// Old page 2 arrives AFTER refresh bumped listingGen -> must be ignored.
	e.HandleEvent(&corev1.Event{
		NowMs: now + 90,
		Event: &corev1.Event_RelayRpcResult{
			RelayRpcResult: &corev1.RelayRpcResultEvent{
				OpId: pageRpc.OpId,
				ResponseBytes: testrelay.MarshalListSessionsResp([]*v1.RemoteSession{
					fx.SessionWire("s_stale_page2", fx.M1, 0, 0, 100, nil, nil),
				}, nil),
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
	fx, legID, listsOps := startAndConnect(t, e, now)

	// Session with 1200 events (exceeds EventsPageLimit = 500).
	deliverRefreshRelay(e, now+40, listsOps,
		[]*v1.Machine{fx.MachineWire(fx.M1, true, 0, nil)},
		[]*v1.RemoteSession{fx.SessionWire("s1", fx.M1, 1200, 0, 0, &v1.Session{Id: "s1", Status: v1.SessionStatus_SESSION_STATUS_RUNNING}, nil)},
		nil, nil,
	)

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
	for _, eff := range cRes.Effects {
		if cd := eff.GetCacheDelete(); cd != nil && cd.GetDropEventsSessionId() == "s1" {
			dropped = true
		}
	}
	fetchOp, fetchAfter := testrelay.FindGetEventsOp(cRes.Effects, "")
	if !dropped {
		t.Fatalf("expected DropEventsSessionId on broken cache")
	}
	if fetchOp == 0 || fetchAfter != 700 {
		t.Fatalf("expected tail-first GetEvents afterSeq=700, got op=%d after=%d", fetchOp, fetchAfter)
	}

	// Tail page arrives (701..1200). Must be buffered in pending and next RPC drains prefix from 0!
	tailEvents := make([]*v1.StoredEvent, 0, 500)
	for seq := uint64(701); seq <= 1199; seq++ {
		tailEvents = append(tailEvents, fx.StoredEventWire("s1", seq, nil))
	}
	tailEvents = append(tailEvents, fx.StoredEventWire("s1", 1200, &v1.Event{
		Seq:       1200,
		SessionId: "s1",
		Kind: &v1.Event_ApprovalRequested{
			ApprovalRequested: &v1.ApprovalRequested{ApprovalId: "ap-tail", ToolName: "bash"},
		},
	}))
	tailRes := deliverEventsPageRelay(e, now+70, fetchOp, tailEvents, false)
	buf := e.Buffer("s1")
	if buf.PrefixLen() != 0 || buf.PendingCount() != 500 {
		t.Fatalf("expected tail buffered in pending (prefix=0, pending=500), got prefix=%d pending=%d", buf.PrefixLen(), buf.PendingCount())
	}

	// Prefix page 1 (1..500) then 2 (501..700) drains gaps seamlessly into contiguous 1200!
	prefixOp, prefixAfter := testrelay.FindGetEventsOp(tailRes.Effects, "")
	if prefixOp == 0 || prefixAfter != 0 {
		t.Fatalf("expected prefix drain GetEvents(afterSeq=0), got op=%d after=%d", prefixOp, prefixAfter)
	}
	p1 := make([]*v1.StoredEvent, 0, 500)
	for seq := uint64(1); seq <= 500; seq++ {
		p1 = append(p1, fx.StoredEventWire("s1", seq, nil))
	}
	p1Res := deliverEventsPageRelay(e, now+80, prefixOp, p1, true)
	p2Op, p2After := testrelay.FindGetEventsOp(p1Res.Effects, "")
	if p2Op == 0 || p2After != 500 {
		t.Fatalf("expected second prefix drain GetEvents(afterSeq=500), got op=%d after=%d", p2Op, p2After)
	}
	p2 := make([]*v1.StoredEvent, 0, 200)
	for seq := uint64(501); seq <= 700; seq++ {
		p2 = append(p2, fx.StoredEventWire("s1", seq, nil))
	}
	deliverEventsPageRelay(e, now+90, p2Op, p2, false)
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

func startDemoConnected(e *Engine, nowMs, monoMs int64) {
	_, res := e.HandleEvent(&corev1.Event{
		NowMs:  nowMs,
		MonoMs: monoMs,
		Event:  &corev1.Event_SyncLifecycle{SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START}},
	})
	if legID := testrelay.FindOpenStreamLeg(res.Effects); legID != 0 {
		e.HandleEvent(&corev1.Event{
			NowMs:  nowMs + 5,
			MonoMs: monoMs + 5,
			Event:  &corev1.Event_StreamReady{StreamReady: &corev1.StreamReadyEvent{LegId: legID, ServerTimeMs: nowMs + 5}},
		})
	}
}

func TestLivePollingLifecycle(t *testing.T) {
	e := NewEngine()
	now := int64(1_700_000_000_000)
	ids, fs := seedEntropy(20)
	e.HandleEvent(&corev1.Event{
		NowMs:      now,
		MonoMs:     1_000,
		RandomIds:  ids,
		UnitFloats: fs,
		Event: &corev1.Event_SeedDemoWorld{
			SeedDemoWorld: &corev1.SeedDemoWorldEvent{
				Security: &corev1.DemoSecurity{
					IsMember:          true,
					SelfDeviceId:      "c1",
					AuthorityDeviceId: "c1",
					LeaseEndMs:        now + 86_400_000,
					TrustedMachineIds: []string{"m1"},
				},
				Machines: []*corev1.DemoMachine{{MachineId: "m1", Online: true}},
				Sessions: []*corev1.DemoSession{{
					SessionId: "s1",
					MachineId: "m1",
					RawMeta:   testrelay.MustRaw(&v1.Session{Id: "s1", Status: v1.SessionStatus_SESSION_STATUS_RUNNING}),
				}},
			},
		},
	})
	startDemoConnected(e, now+10, 1_010)

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
				RawResult: testrelay.MustRaw(&v1.CommandResult{
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
				}),
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
		Event: &corev1.Event_SeedDemoWorld{
			SeedDemoWorld: &corev1.SeedDemoWorldEvent{
				Machines: []*corev1.DemoMachine{{MachineId: "m1", Online: false}},
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
	fx, _, listsOps := startAndConnect(t, e, now)
	deliverRefreshRelay(e, now+40, listsOps, nil, nil, nil, nil)

	// Enqueue Pin -> GET_ACCOUNT_PREFS.
	res := e.Run(func(ctx *StepContext) {
		e.EnqueuePrefsOp(ctx, &lists.FullPrefsOp{Kind: lists.PrefsOpPin, Path: "/work/a"}, false, "")
	})
	getRpc := testrelay.FindRelayRpc(res.Effects, corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_ACCOUNT_PREFS)
	if getRpc == nil {
		t.Fatalf("expected GET_ACCOUNT_PREFS RelayRpc")
	}
	getOp := getRpc.OpId

	// Simulate CAS conflict 4 times, then idempotent recent_ops hit on 5th GET!
	var recordedOpID string
	for attempt := 0; attempt < 4; attempt++ {
		_, getRes := e.HandleEvent(&corev1.Event{
			NowMs: now + int64(50+attempt*10),
			Event: &corev1.Event_RelayRpcResult{
				RelayRpcResult: &corev1.RelayRpcResultEvent{
					OpId:          getOp,
					ResponseBytes: testrelay.MarshalGetPrefsResp(fx.PrefsWire(uint64(attempt+1), &v1.ProjectPrefs{Schema: 1})),
				},
			},
		})
		pumped := fx.Pump(dispatchEngine(e), now+int64(52+attempt*10), getRes.Effects)
		putRpc := testrelay.FindRelayRpc(pumped, corev1.RelayRpcMethod_RELAY_RPC_METHOD_PUT_ACCOUNT_PREFS)
		if putRpc == nil {
			t.Fatalf("expected PUT_ACCOUNT_PREFS RelayRpc on attempt %d", attempt)
		}
		var putReq stdv1.PutAccountPrefsRequest
		_ = proto.Unmarshal(putRpc.RequestBytes, &putReq)
		if putReq.Envelope != nil {
			var nextPrefs v1.ProjectPrefs
			_ = nextPrefs.Unmarshal(fx.OpenEnvelope(putReq.Envelope))
			if len(nextPrefs.RecentOps) > 0 {
				recordedOpID = nextPrefs.RecentOps[len(nextPrefs.RecentOps)-1]
			}
		}
		_, putRes := e.HandleEvent(&corev1.Event{
			NowMs: now + int64(55+attempt*10),
			Event: &corev1.Event_RelayRpcResult{
				RelayRpcResult: &corev1.RelayRpcResultEvent{
					OpId:          putRpc.OpId,
					ResponseBytes: testrelay.MarshalPutPrefsResp(false, nil),
				},
			},
		})
		nextGet := testrelay.FindRelayRpc(putRes.Effects, corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_ACCOUNT_PREFS)
		if nextGet == nil {
			t.Fatalf("expected CAS retry GET_ACCOUNT_PREFS on attempt %d", attempt)
		}
		getOp = nextGet.OpId
	}

	// 5th GET finds our opID already in RecentOps -> idempotent completion!
	e.HandleEvent(&corev1.Event{
		NowMs: now + 100,
		Event: &corev1.Event_RelayRpcResult{
			RelayRpcResult: &corev1.RelayRpcResultEvent{
				OpId: getOp,
				ResponseBytes: testrelay.MarshalGetPrefsResp(fx.PrefsWire(5, &v1.ProjectPrefs{
					Schema:    1,
					Pinned:    []*v1.ProjectPref{{Path: "/work/a"}},
					RecentOps: []string{recordedOpID},
				})),
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
	ids, fs := seedEntropy(20)
	e.HandleEvent(&corev1.Event{
		NowMs:      now,
		MonoMs:     1_000,
		RandomIds:  ids,
		UnitFloats: fs,
		Event: &corev1.Event_SeedDemoWorld{
			SeedDemoWorld: &corev1.SeedDemoWorldEvent{
				Security: &corev1.DemoSecurity{
					IsMember:          true,
					SelfDeviceId:      "c1",
					AuthorityDeviceId: "c1",
					LeaseEndMs:        now + 86_400_000,
					TrustedMachineIds: []string{"m1"},
				},
				Machines: []*corev1.DemoMachine{{
					MachineId: "m1",
					Online:    true,
					RawMeta: testrelay.MustRaw(&v1.MachineMeta{
						Hostname:        "Dev",
						ProtocolVersion: PeerVersion,
						Capabilities:    &v1.MachineCapabilities{FileUpload: true},
					}),
				}},
				Sessions: []*corev1.DemoSession{{
					SessionId: "s1",
					MachineId: "m1",
					LastSeq:   10,
					RawCheckpoint: testrelay.MustRaw(&v1.LiveSnapshot{
						Actions: []*v1.Event{
							{Seq: 10, SessionId: "s1", Kind: &v1.Event_ApprovalRequested{ApprovalRequested: &v1.ApprovalRequested{ApprovalId: "ap-10"}}},
						},
					}),
				}},
			},
		},
	})
	startDemoConnected(e, now+10, 1_010)

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
				RawResult: testrelay.MustRaw(&v1.CommandResult{
					Outcome:         "executed",
					Reason:          "ok",
					ResultAvailable: true,
					Kind: &v1.CommandResult_CreateSession{
						CreateSession: &v1.CreateSessionResponse{Session: &v1.Session{Id: "new-s1"}},
					},
				}),
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
	fx, preRecEvent := testrelay.NewWithRecovery(t, now)
	_, listsOps := startAndConnectWithFixture(t, e, fx, now)

	listsRes := deliverRefreshRelay(e, now+40, listsOps,
		[]*v1.Machine{
			fx.MachineWire(fx.M1, true, now+35, &v1.MachineMeta{
				Hostname:        "MacStudio",
				ProtocolVersion: PeerVersion - 1,
			}),
		},
		[]*v1.RemoteSession{
			fx.SessionWire("s1", fx.M1, 3, now+30, 0, &v1.Session{
				Id:        "s1",
				Cwd:       "/work/app",
				Agent:     v1.Agent_AGENT_CODEX,
				Status:    v1.SessionStatus_SESSION_STATUS_RUNNING,
				Title:     "Transition check",
				CreatedAt: &timestamppb.Timestamp{Seconds: now / 1000},
			}, &v1.LiveSnapshot{
				Running: true,
				Actions: []*v1.Event{
					{Seq: 3, SessionId: "s1", Kind: &v1.Event_QuestionAsked{QuestionAsked: &v1.QuestionAsked{QuestionId: "q-live"}}},
				},
			}),
		},
		[]*v1.Device{
			fx.DeviceWire(fx.Client.ID, v1.DeviceKind_DEVICE_KIND_CLIENT, now-100_000, 0, 1, &v1.DeviceMeta{Name: "MainPhone", Platform: "Android"}),
			fx.DeviceWire(fx.M1, v1.DeviceKind_DEVICE_KIND_MACHINE, now-200_000, 0, 1, &v1.DeviceMeta{Name: "MacStudio"}),
		},
		&v1.SessionCursor{EndedMs: now - 10_000, SessionId: "s0"},
	)
	targets := listsRes.Dirty.ScreenTargets()
	if !slices.Contains(targets, corev1.ScreenTarget_SCREEN_TARGET_HOME) || !slices.Contains(targets, corev1.ScreenTarget_SCREEN_TARGET_CONNECTION) {
		t.Fatalf("expected Home and Connection render targets on ListsFetched, got %v", targets)
	}

	sv := e.BuildSyncState(SyncStateOpts{IncludeDevices: true})
	if sv.Connection != corev1.ConnState_CONN_STATE_CONNECTED || !sv.LivePushConnected || !sv.IsManager || !e.World().World.HasMoreSessions {
		t.Fatalf("unexpected SyncState header: conn=%v live=%v mgr=%v more=%v", sv.Connection, sv.LivePushConnected, sv.IsManager, e.World().World.HasMoreSessions)
	}
	if len(e.World().World.Machines) != 1 || !e.machines[fx.M1].DaemonOutOfDate || !e.machines[fx.M1].reachable || e.World().World.Machines[0].Meta.GetHostname() != "MacStudio" {
		t.Fatalf("unexpected machines: %+v", e.World().World.Machines)
	}
	snap1 := e.SessionSnapshot("s1")
	if !snap1.Found || !e.listed["s1"] || snap1.Session == nil || !snap1.Session.Pending.HasQuestion {
		t.Fatalf("unexpected session snapshot: %+v", snap1)
	}
	var foundM1Trusted bool
	for _, d := range sv.Devices {
		if d.DeviceID == fx.M1 && d.Trusted {
			foundM1Trusted = true
		}
	}
	if len(sv.Devices) < 2 || !foundM1Trusted {
		t.Fatalf("unexpected SyncState devices: %+v", sv.Devices)
	}

	if e.Buffer("missing") != nil || e.sessions["missing"] != nil {
		t.Fatalf("expected missing session to have no session entry or buffer")
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
	loadingBuf := e.Buffer("s1")
	if loadingBuf == nil || !loadingBuf.awaitingCache || loadingBuf.loaded {
		t.Fatalf("expected awaitingCache=true loaded=false before CacheEventsLoaded, got %+v", loadingBuf)
	}

	_, brokenRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 60,
		Event: &corev1.Event_CacheEventsLoaded{
			CacheEventsLoaded: &corev1.CacheEventsLoadedEvent{OpId: cacheOp, SessionId: "s1", Broken: true},
		},
	})
	if !loadingBuf.retryingCorruptCache || !loadingBuf.fetching {
		t.Fatalf("expected retryingCorruptCache+fetching after broken cache drop, got retry=%v fetch=%v", loadingBuf.retryingCorruptCache, loadingBuf.fetching)
	}
	getEventsOp, _ := testrelay.FindGetEventsOp(brokenRes.Effects, "")
	evRes := deliverEventsPageRelay(e, now+70, getEventsOp, []*v1.StoredEvent{
		preRecEvent("s1", fx.M1, 1),
		fx.StoredEventWire("s1", 2, &v1.Event{Seq: 2, SessionId: "s1", Kind: &v1.Event_UserMessage{UserMessage: &v1.UserMessage{Text: "hi"}}}),
		fx.StoredEventWire("s1", 3, &v1.Event{Seq: 3, SessionId: "s1", Kind: &v1.Event_AgentMessage{AgentMessage: &v1.AgentMessage{Text: "hello"}}}),
	}, false)
	if !evRes.Dirty.SessionData {
		t.Fatalf("expected Dirty.SessionData on EventsPageFetched")
	}
	s1 := e.sessions["s1"]
	if !loadingBuf.loaded || loadingBuf.awaitingCache || loadingBuf.fetching || loadingBuf.hasServerGap(s1.ServerLastSeq) || loadingBuf.retryingCorruptCache {
		t.Fatalf("unexpected EventBuffer load state: loaded=%v awaiting=%v fetching=%v gap=%v corrupt=%v",
			loadingBuf.loaded, loadingBuf.awaitingCache, loadingBuf.fetching, loadingBuf.hasServerGap(s1.ServerLastSeq), loadingBuf.retryingCorruptCache)
	}
	if len(loadingBuf.unavailableSeqs) != 1 || !loadingBuf.unavailableSeqs[1] {
		t.Fatalf("expected unavailableSeqs={1}, got %v", loadingBuf.unavailableSeqs)
	}
	if s1.Checkpoint == nil || !s1.Pending.HasQuestion || len(loadingBuf.ShownEvents()) != 3 {
		t.Fatalf("expected live checkpoint, pending question, and 3 shown events; got cp=%v pending=%+v events=%d",
			s1.Checkpoint != nil, s1.Pending, len(loadingBuf.ShownEvents()))
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
		Event: testrelay.RawFrame(42, &v1.ServerFrame{
			Kind: &v1.ServerFrame_MachineRemoved{MachineRemoved: &v1.MachineRemoved{MachineId: "m1"}},
		}),
	})
	var secOp uint64
	for _, eff := range mrRes.Effects {
		if sr := eff.GetRelayRpc(); sr != nil && sr.Method == corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_SECURITY_STATES {
			secOp = sr.OpId
		}
	}
	if secOp == 0 {
		t.Fatalf("expected MachineRemoved stream frame to emit GET_SECURITY_STATES RelayRpcEffect with non-zero OpId")
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
		Event: &corev1.Event_RelayRpcResult{
			RelayRpcResult: &corev1.RelayRpcResultEvent{
				OpId:          secOp + 99,
				ResponseBytes: statesRespRaw,
			},
		},
	})
	if e.security.HeadEpoch != 2 {
		t.Fatalf("expected wrong opId RelayRpcResult to be ignored, got epoch=%d", e.security.HeadEpoch)
	}
	_, rpcDone := e.HandleEvent(&corev1.Event{
		NowMs: now + 40,
		Event: &corev1.Event_RelayRpcResult{
			RelayRpcResult: &corev1.RelayRpcResultEvent{
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
		} else if sr := eff.GetRelayRpc(); sr != nil && sr.Method == corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_SECURITY_STATES {
			emptyStates3Raw, _ := proto.Marshal(&stdv1.GetSecurityStatesResponse{HeadEpoch: 3})
			_, nextRes := e.HandleEvent(&corev1.Event{
				NowMs: now + 42,
				Event: &corev1.Event_RelayRpcResult{
					RelayRpcResult: &corev1.RelayRpcResultEvent{OpId: sr.OpId, ResponseBytes: emptyStates3Raw},
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
		t.Fatalf("expected matching opId RelayRpcResult to apply epoch=3, got %d", e.security.HeadEpoch)
	}

	// 4. Stale LIST_DEVICES RelayRpcResult (unmatched OpId) is rejected; matching OpId populates devices.
	devStep := e.Run(func(ctx *StepContext) {
		e.startRefreshDevices(ctx, true)
	})
	devRpc := testrelay.FindRelayRpc(devStep.Effects, corev1.RelayRpcMethod_RELAY_RPC_METHOD_LIST_DEVICES)
	if devRpc == nil {
		t.Fatalf("expected LIST_DEVICES RelayRpc")
	}
	e.HandleEvent(&corev1.Event{
		NowMs: now + 50,
		Event: &corev1.Event_RelayRpcResult{
			RelayRpcResult: &corev1.RelayRpcResultEvent{
				OpId:          devRpc.OpId + 99,
				ResponseBytes: testrelay.MarshalListDevicesResp([]*v1.Device{{Id: "d-bad"}}),
			},
		},
	})
	if _, ok := e.devices["d-bad"]; ok {
		t.Fatalf("expected unmatched LIST_DEVICES RelayRpcResult to be dropped")
	}
	e.HandleEvent(&corev1.Event{
		NowMs: now + 55,
		Event: &corev1.Event_RelayRpcResult{
			RelayRpcResult: &corev1.RelayRpcResultEvent{
				OpId:          devRpc.OpId,
				ResponseBytes: testrelay.MarshalListDevicesResp([]*v1.Device{{Id: m1.id, Kind: v1.DeviceKind_DEVICE_KIND_MACHINE}}),
			},
		},
	})
	if _, ok := e.devices[m1.id]; !ok {
		t.Fatalf("expected matching LIST_DEVICES RelayRpcResult to populate trusted device %s", m1.id)
	}

	// 5. DeviceRemoved stream frame also emits RelayRpc(GET_SECURITY_STATES) directly from core.
	_, drRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 70,
		Event: testrelay.RawFrame(42, &v1.ServerFrame{
			Kind: &v1.ServerFrame_DeviceRemoved{DeviceRemoved: &v1.DeviceRemoved{DeviceId: m1.id}},
		}),
	})
	var gotSecSync bool
	for _, eff := range drRes.Effects {
		if sr := eff.GetRelayRpc(); sr != nil && sr.Method == corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_SECURITY_STATES && sr.OpId != 0 {
			gotSecSync = true
		}
	}
	if !gotSecSync {
		t.Fatalf("expected DeviceRemoved stream frame to emit GET_SECURITY_STATES RelayRpcEffect")
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
		if sr := eff.GetRelayRpc(); sr != nil && sr.Method == corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_SECURITY_STATES {
			startRpcOp = sr.OpId
		}
	}
	if startRpcOp == 0 || e.networkGen == 0 {
		t.Fatalf("expected START after STOP to emit GET_SECURITY_STATES RelayRpcEffect and advance networkGen, got op=%d gen=%d", startRpcOp, e.networkGen)
	}
	emptyStatesRaw, _ := proto.Marshal(&stdv1.GetSecurityStatesResponse{HeadEpoch: 1})
	e.HandleEvent(&corev1.Event{
		NowMs: 2_110 + now,
		Event: &corev1.Event_RelayRpcResult{
			RelayRpcResult: &corev1.RelayRpcResultEvent{OpId: startRpcOp, ResponseBytes: emptyStatesRaw},
		},
	})

	// 2. BACKGROUND -> FOREGROUND while running/stream open must also emit GET_SECURITY_STATES RelayRpcEffect.
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
		if sr := eff.GetRelayRpc(); sr != nil && sr.Method == corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_SECURITY_STATES {
			fgRpcOp = sr.OpId
		}
	}
	if fgRpcOp == 0 || e.networkGen <= prevGen {
		t.Fatalf("expected FOREGROUND after BACKGROUND to emit GET_SECURITY_STATES RelayRpcEffect with networkGen>%d, got op=%d gen=%d", prevGen, fgRpcOp, e.networkGen)
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
				v := e.BuildSyncState(SyncStateOpts{})
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
				stopped := e.BuildSyncState(SyncStateOpts{})
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
				inside := e.BuildSyncState(SyncStateOpts{})
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
				expired := e.BuildSyncState(SyncStateOpts{})
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
				inside := e.BuildSyncState(SyncStateOpts{})
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
				after := e.BuildSyncState(SyncStateOpts{})
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
				fx := testrelay.New(t, 1_000)
				fx.Bootstrap(dispatchEngine(e), 1_000)
				e.secEngine.SetAuthToken("tok-1", 1_000+7*86_400_000)
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
				v0 := e.BuildSyncState(SyncStateOpts{})
				if v0.Connection != corev1.ConnState_CONN_STATE_CONNECTING || v0.DisplayGrace || !v0.ReachabilityUnknown {
					t.Fatalf("expected CONNECTING + ReachabilityUnknown without DisplayGrace on cold start, got conn=%v grace=%v unknown=%v",
						v0.Connection, v0.DisplayGrace, v0.ReachabilityUnknown)
				}

				// Cached offline machine + running session retain ReachabilityUnknown during grace.
				var bootOp uint64
				var legID uint64
				for _, eff := range startRes.Effects {
					if cr := eff.GetCacheRead(); cr != nil && cr.GetBootstrap() != nil {
						bootOp = cr.GetBootstrap().OpId
					}
					if osEff := eff.GetOpenStream(); osEff != nil {
						legID = osEff.LegId
					}
				}
				e.HandleEvent(&corev1.Event{
					NowMs:  1_020,
					MonoMs: 1_020,
					Event: &corev1.Event_CacheBootstrapLoaded{
						CacheBootstrapLoaded: &corev1.CacheBootstrapLoadedEvent{
							OpId:        bootOp,
							RawMachines: testrelay.MustRawSlice([]*v1.Machine{fx.MachineWire(fx.M1, false, 0, nil)}),
							RawSessions: testrelay.MustRawSlice([]*v1.RemoteSession{fx.SessionWire("s1", fx.M1, 0, 0, 0, &v1.Session{Id: "s1", Status: v1.SessionStatus_SESSION_STATUS_RUNNING}, nil)}),
						},
					},
				})
				if len(e.World().World.Machines) != 1 || !e.World().World.Machines[0].ReachabilityUnknown {
					t.Fatalf("expected cached machine m1 to carry ReachabilityUnknown=true, got %+v", e.World().World.Machines)
				}
				snapBoot := e.SessionSnapshot("s1")
				if !snapBoot.ReachabilityUnknown || snapBoot.Machine == nil || !snapBoot.Machine.ReachabilityUnknown {
					t.Fatalf("expected SessionSnapshot to carry ReachabilityUnknown=true, got snap=%+v", snapBoot)
				}

				// StreamReady before lists finish: connection is CONNECTED, reachability still UNKNOWN.
				_, readyRes := e.HandleEvent(&corev1.Event{
					NowMs:  1_100,
					MonoMs: 1_100,
					Event: &corev1.Event_StreamReady{
						StreamReady: &corev1.StreamReadyEvent{LegId: legID},
					},
				})
				listsOps := testrelay.MustFindRefreshOps(t, readyRes.Effects)
				vStream := e.BuildSyncState(SyncStateOpts{})
				if vStream.Connection != corev1.ConnState_CONN_STATE_CONNECTED || !vStream.ReachabilityUnknown {
					t.Fatalf("expected CONNECTED + ReachabilityUnknown=true between StreamReady and ListsFetched, got conn=%v unknown=%v",
						vStream.Connection, vStream.ReachabilityUnknown)
				}

				// Lists arrive with authoritative machine status and clear cold-start grace.
				deliverRefreshRelay(e, 1_150, listsOps,
					[]*v1.Machine{fx.MachineWire(fx.M1, true, 0, nil)},
					[]*v1.RemoteSession{fx.SessionWire("s1", fx.M1, 0, 0, 0, &v1.Session{Id: "s1", Status: v1.SessionStatus_SESSION_STATUS_RUNNING}, nil)},
					nil, nil,
				)
				vDone := e.BuildSyncState(SyncStateOpts{})
				if vDone.ReachabilityUnknown || vDone.Unlinked {
					t.Fatalf("expected ListsFetched to clear ReachabilityUnknown and Unlinked, got unknown=%v unlinked=%v",
						vDone.ReachabilityUnknown, vDone.Unlinked)
				}
				if len(e.World().World.Machines) != 1 || e.World().World.Machines[0].ReachabilityUnknown || !e.World().World.Machines[0].Online {
					t.Fatalf("expected machine m1 Online=true and ReachabilityUnknown=false after ListsFetched, got %+v", e.World().World.Machines)
				}
			},
		},
		{
			name: "cold_start_grace_expires_on_timer_and_exposes_offline_machine",
			run: func(t *testing.T) {
				e := NewEngine()
				fx := testrelay.New(t, 1_000)
				fx.Bootstrap(dispatchEngine(e), 1_000)
				e.secEngine.SetAuthToken("tok-1", 1_000+7*86_400_000)
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
							OpId:        bootOp,
							RawMachines: testrelay.MustRawSlice([]*v1.Machine{fx.MachineWire(fx.M1, false, 0, nil)}),
						},
					},
				})
				e.HandleEvent(&corev1.Event{
					NowMs:  2_400,
					MonoMs: 2_400,
					Event:  &corev1.Event_ClockUpdated{ClockUpdated: &corev1.LocalCalendar{NowMs: 2_400}},
				})
				inside := e.BuildSyncState(SyncStateOpts{})
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
				expired := e.BuildSyncState(SyncStateOpts{})
				if expired.ReachabilityUnknown {
					t.Fatalf("expected ReachabilityUnknown=false after cold-start grace timer fired")
				}
				if len(e.World().World.Machines) != 1 || e.World().World.Machines[0].ReachabilityUnknown || e.World().World.Machines[0].Online {
					t.Fatalf("expected machine m1 to revert to offline (ReachabilityUnknown=false) after expiry, got %+v", e.World().World.Machines)
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
				actor := newSyncTestActor(t, 47)
				e.HandleEvent(&corev1.Event{
					NowMs: 1_000,
					Event: &corev1.Event_SecurityConfig{
						SecurityConfig: &corev1.SecurityConfigEvent{Account: actor.id, DeviceId: actor.id, Server: "https://relay.example.com"},
					},
				})
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
				var bootOp uint64
				for _, eff := range startRes.Effects {
					if st := eff.GetSecurityStorage(); st != nil && st.Op == corev1.SecurityStorageOp_SECURITY_STORAGE_OP_READ_BOOTSTRAP {
						bootOp = st.OpId
					}
				}
				if !e.BuildSyncState(SyncStateOpts{}).ReachabilityUnknown {
					t.Fatalf("expected ReachabilityUnknown=true on start")
				}
				// Drive two PermissionDenied logins so secEngine reports Removed=true.
				_, stepRes := e.HandleEvent(&corev1.Event{
					NowMs: 1_010,
					Event: &corev1.Event_SecurityStorageResult{SecurityStorageResult: &corev1.SecurityStorageResultEvent{OpId: bootOp}},
				})
				for attempt := 0; attempt < 2; attempt++ {
					chal := testrelay.FindRelayRpc(stepRes.Effects, corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_CHALLENGE)
					_, cr := e.HandleEvent(&corev1.Event{
						NowMs: 1_020 + int64(attempt*10),
						Event: &corev1.Event_RelayRpcResult{RelayRpcResult: &corev1.RelayRpcResultEvent{
							OpId:          chal.OpId,
							ResponseBytes: testrelay.MarshalStd(&stdv1.GetChallengeResponse{Challenge: bytes.Repeat([]byte{0x12}, 32)}),
						}},
					})
					var ks *corev1.SecurityKeySignEffect
					for _, eff := range cr.Effects {
						if s := eff.GetSecurityKeySign(); s != nil {
							ks = s
						}
					}
					_, sr := e.HandleEvent(&corev1.Event{
						NowMs: 1_025 + int64(attempt*10),
						Event: &corev1.Event_SecurityKeySigned{SecurityKeySigned: &corev1.SecurityKeySignedEvent{OpId: ks.OpId, Signature: ed25519.Sign(actor.ed, ks.Message)}},
					})
					login := testrelay.FindRelayRpc(sr.Effects, corev1.RelayRpcMethod_RELAY_RPC_METHOD_LOGIN)
					_, stepRes = e.HandleEvent(&corev1.Event{
						NowMs: 1_028 + int64(attempt*10),
						Event: &corev1.Event_RelayRpcResult{RelayRpcResult: &corev1.RelayRpcResultEvent{
							OpId:              login.OpId,
							ConnectCode:       security.ConnectCodePermissionDenied,
							DiagnosticMessage: "device removed",
						}},
					})
				}
				after := e.BuildSyncState(SyncStateOpts{})
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
	fx, legID, listsOps := startAndConnect(t, e, now)
	client := directActorFrom(t, fx.Client)
	m1Actor := directActorFrom(t, fx.Machines[0])
	m1, m2 := fx.M1, fx.M2

	deliverRefreshRelay(e, now+40, listsOps,
		[]*v1.Machine{fx.MachineWire(m1, true, 0, nil), fx.MachineWire(m2, true, 0, nil)},
		[]*v1.RemoteSession{fx.SessionWire("s1", m1, 2, now, 0, nil, nil)},
		nil, nil,
	)
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
	fetchOp, _ := testrelay.FindGetEventsOp(loadedRes.Effects, "")
	deliverEventsPageRelay(e, now+70, fetchOp, []*v1.StoredEvent{
		fx.StoredEventWire("s1", 1, nil),
		fx.StoredEventWire("s1", 2, nil),
	}, false)
	readPCID, hsNow, sealer := connectDirectForExistingEngine(t, e, now+75, client, m1Actor)
	now = hsNow

	// 1. Direct event seq=3 arrives before relay: updates display prefix and LastSeq,
	// does NOT advance ServerLastSeq, and emits NO CacheWriteEffect.
	env3 := sealer.sealDirectEvent("s1", 3, m1Actor)
	_, directRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 10,
		Event: &corev1.Event_DirectEventsReceived{
			DirectEventsReceived: &corev1.DirectEventsReceivedEvent{
				MachineId:    m1,
				SessionId:    "s1",
				PcId:         readPCID,
				RawEnvelopes: testrelay.MustRawSlice([]*v1.Envelope{env3}),
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
		Event: testrelay.RawFrame(legID, &v1.ServerFrame{
			Kind: &v1.ServerFrame_EventsAppended{
				EventsAppended: &v1.EventsAppended{
					SessionId: "s1",
					Events:    []*v1.StoredEvent{{Seq: 3, Envelope: env3}},
				},
			},
		}),
	})
	if buf := e.Buffer("s1"); buf.PrefixLen() != 3 {
		t.Fatalf("expected deduplicated prefix=3, got %d", buf.PrefixLen())
	}
	if s := e.sessions["s1"]; s.ServerLastSeq != 3 {
		t.Fatalf("expected ServerLastSeq=3 after relay EventsAppended, got %d", s.ServerLastSeq)
	}

	// 3. Cross-machine spoofing, untrusted machine, and corrupted direct items are ignored.
	_, spoofRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 25,
		Event: &corev1.Event_DirectEventsReceived{
			DirectEventsReceived: &corev1.DirectEventsReceivedEvent{
				MachineId:    m2, // wrong owner for s1
				SessionId:    "s1",
				PcId:         readPCID,
				RawEnvelopes: testrelay.MustRawSlice([]*v1.Envelope{sealer.sealDirectEvent("s1", 4, m1Actor)}),
			},
		},
	})
	if e.Buffer("s1").PrefixLen() != 3 || spoofRes.Dirty.SessionData {
		t.Fatalf("cross-machine direct event must be rejected")
	}
	badEnv4 := sealer.sealDirectEvent("s1", 4, m1Actor)
	badEnv4.Ciphertext[0] ^= 0xff
	e.HandleEvent(&corev1.Event{
		NowMs: now + 26,
		Event: &corev1.Event_DirectEventsReceived{
			DirectEventsReceived: &corev1.DirectEventsReceivedEvent{
				MachineId:    m1,
				SessionId:    "s1",
				PcId:         readPCID,
				RawEnvelopes: testrelay.MustRawSlice([]*v1.Envelope{badEnv4}),
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
	fx, legID, listsOps := startAndConnect(t, e, now)
	client := directActorFrom(t, fx.Client)
	m1Actor := directActorFrom(t, fx.Machines[0])
	m1 := fx.M1

	deliverRefreshRelay(e, now+40, listsOps,
		[]*v1.Machine{fx.MachineWire(m1, true, 0, nil)},
		[]*v1.RemoteSession{fx.SessionWire("s1", m1, 40, now, 0, nil, nil)},
		nil, nil,
	)
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
	fetchOp, _ := testrelay.FindGetEventsOp(loadedRes.Effects, "")
	initEvents := make([]*v1.StoredEvent, 40)
	for i := 0; i < 40; i++ {
		initEvents[i] = fx.StoredEventWire("s1", uint64(i+1), nil)
	}
	deliverEventsPageRelay(e, now+70, fetchOp, initEvents, false)
	readPCID, hsNow, sealer := connectDirectForExistingEngine(t, e, now+75, client, m1Actor)
	now = hsNow

	// Direct delivers seq=42 while seq=41 (>16KiB) is still uploading to server.
	// Direct gap event must be dropped rather than entering EventBuffer.pending,
	// without emitting a premature GetEvents RPC or marking a gap error.
	_, directRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 10,
		Event: &corev1.Event_DirectEventsReceived{
			DirectEventsReceived: &corev1.DirectEventsReceivedEvent{
				MachineId:    m1,
				SessionId:    "s1",
				PcId:         readPCID,
				RawEnvelopes: testrelay.MustRawSlice([]*v1.Envelope{sealer.sealDirectEvent("s1", 42, m1Actor)}),
			},
		},
	})
	if op, _ := testrelay.FindGetEventsOp(directRes.Effects, ""); op != 0 {
		t.Fatalf("direct gap must not emit premature GetEvents")
	}
	if buf := e.Buffer("s1"); buf.PrefixLen() != 40 || buf.PendingCount() != 0 {
		t.Fatalf("gapped direct seq=42 must be dropped (prefix=40 pending=0), got prefix=%d pending=%d", buf.PrefixLen(), buf.PendingCount())
	}
	if buf := e.Buffer("s1"); buf.hasServerGap(e.sessions["s1"].ServerLastSeq) || !buf.loaded || !buf.fetchErrorReason.IsZero() {
		t.Fatalf("dropped direct gap event must not report hasServerGap/fetchErrorReason: loaded=%v err=%+v", buf.loaded, buf.fetchErrorReason)
	}
	// ApplyLiveSnapshot (over direct) still advances LastSeq to 42 without touching ServerLastSeq.
	e.Run(func(ctx *StepContext) {
		e.ApplyLiveSnapshot(ctx, "s1", &v1.LiveSnapshot{Session: &v1.Session{Id: "s1", LastSeq: 42}})
	})
	if e.sessions["s1"].ServerLastSeq != 40 {
		t.Fatalf("ApplyLiveSnapshot must not advance ServerLastSeq, got %d", e.sessions["s1"].ServerLastSeq)
	}

	// If a GetEvents cycle finishes while ServerLastSeq==40, it must not report false gap error.
	_, reWatchRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 85,
		Event: &corev1.Event_SessionWatch{SessionWatch: &corev1.SessionWatchEvent{SessionId: "s1", Watched: true}},
	})
	midOp, midAfter := testrelay.FindGetEventsOp(reWatchRes.Effects, "")
	if midOp == 0 || midAfter != 40 {
		t.Fatalf("expected SessionWatch to trigger GetEvents cursor=40 (ServerLastSeq), got op=%d after=%d", midOp, midAfter)
	}
	deliverEventsPageRelay(e, now+90, midOp, nil, false)
	if buf := e.Buffer("s1"); !buf.fetchErrorReason.IsZero() || buf.hasServerGap(e.sessions["s1"].ServerLastSeq) || !buf.loaded {
		t.Fatalf("finishFetchCycle with empty server gap must not set fetchErrorReason/hasServerGap: loaded=%v err=%+v", buf.loaded, buf.fetchErrorReason)
	}

	// Now server commits 41..42 and broadcasts SessionUpdated(LastSeq=42) (suppose EventsAppended was dropped).
	_, updRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 100,
		Event: testrelay.RawFrame(legID, &v1.ServerFrame{
			Kind: &v1.ServerFrame_SessionUpdated{
				SessionUpdated: &v1.SessionUpdated{Session: sealer.sealSessionMeta("s1", 42, now+100)},
			},
		}),
	})
	gapOp, gapAfter := testrelay.FindGetEventsOp(updRes.Effects, "")
	if gapOp == 0 || gapAfter != 40 {
		t.Fatalf("SessionUpdated(42) must trigger gap-fill GetEvents after 40, got op=%d after=%d", gapOp, gapAfter)
	}
	deliverEventsPageRelay(e, now+110, gapOp, []*v1.StoredEvent{
		{Seq: 41, Envelope: sealer.sealDirectEvent("s1", 41, m1Actor)},
		{Seq: 42, Envelope: sealer.sealDirectEvent("s1", 42, m1Actor)},
	}, false)
	if buf := e.Buffer("s1"); buf.PrefixLen() != 42 || buf.PendingCount() != 0 || !buf.IsLoaded() {
		t.Fatalf("expected contiguous prefix=42 pending=0 loaded=true, got prefix=%d pending=%d loaded=%v", buf.PrefixLen(), buf.PendingCount(), buf.IsLoaded())
	}
}

func TestDirectEventsWhileRelayDisconnectedDoNotMarkRelayConnected(t *testing.T) {
	e := NewEngine()
	now := int64(1_700_000_000_000)
	fx, legID, listsOps := startAndConnect(t, e, now)
	client := directActorFrom(t, fx.Client)
	m1Actor := directActorFrom(t, fx.Machines[0])
	m1 := fx.M1

	deliverRefreshRelay(e, now+40, listsOps,
		[]*v1.Machine{fx.MachineWire(m1, true, 0, nil)},
		[]*v1.RemoteSession{fx.SessionWire("s1", m1, 1, now, 0, nil, nil)},
		nil, nil,
	)
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
	fetchOp, _ := testrelay.FindGetEventsOp(loadedRes.Effects, "")
	deliverEventsPageRelay(e, now+70, fetchOp, []*v1.StoredEvent{fx.StoredEventWire("s1", 1, nil)}, false)
	readPCID, hsNow, sealer := connectDirectForExistingEngine(t, e, now+75, client, m1Actor)
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
				MachineId:    m1,
				SessionId:    "s1",
				PcId:         readPCID,
				RawEnvelopes: testrelay.MustRawSlice([]*v1.Envelope{sealer.sealDirectEvent("s1", 2, m1Actor)}),
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
	fx, _, listsOps := startAndConnect(t, e, now)
	client := directActorFrom(t, fx.Client)
	m1Actor := directActorFrom(t, fx.Machines[0])
	m1 := fx.M1
	deliverRefreshRelay(e, now+40, listsOps,
		[]*v1.Machine{fx.MachineWire(m1, true, 0, nil)},
		[]*v1.RemoteSession{fx.SessionWire("s1", m1, 1200, now, 0, nil, nil)},
		nil, nil,
	)
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
	tailOp, _ := testrelay.FindGetEventsOp(cacheRes.Effects, "")
	// Receive tail 701..1200.
	tail := make([]*v1.StoredEvent, 0, 500)
	for seq := uint64(701); seq <= 1200; seq++ {
		tail = append(tail, fx.StoredEventWire("s1", seq, nil))
	}
	tailRes := deliverEventsPageRelay(e, now+70, tailOp, tail, false)
	prefixOp1, _ := testrelay.FindGetEventsOp(tailRes.Effects, "")
	readPCID, hsNow, sealer := connectDirectForExistingEngine(t, e, now+72, client, m1Actor)
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
				RawEnvelopes: testrelay.MustRawSlice([]*v1.Envelope{
					sealer.sealDirectEvent("s1", 1202, m1Actor),
					sealer.sealDirectEvent("s1", 1203, m1Actor),
				}),
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
	p1 := make([]*v1.StoredEvent, 0, 500)
	for seq := uint64(1); seq <= 500; seq++ {
		p1 = append(p1, &v1.StoredEvent{Seq: seq, Envelope: sealer.sealDirectEvent("s1", seq, m1Actor)})
	}
	p1Res := deliverEventsPageRelay(e, now+10, prefixOp1, p1, true)
	prefixOp2, _ := testrelay.FindGetEventsOp(p1Res.Effects, "")
	p2 := make([]*v1.StoredEvent, 0, 200)
	for seq := uint64(501); seq <= 700; seq++ {
		p2 = append(p2, &v1.StoredEvent{Seq: seq, Envelope: sealer.sealDirectEvent("s1", seq, m1Actor)})
	}
	deliverEventsPageRelay(e, now+20, prefixOp2, p2, false)
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
				MachineId:    m1,
				SessionId:    "s1",
				PcId:         readPCID,
				RawEnvelopes: testrelay.MustRawSlice([]*v1.Envelope{sealer.sealDirectEvent("s1", 1201, m1Actor)}),
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
	fx, legID, listsOps := startAndConnect(t, e, now)
	client := directActorFrom(t, fx.Client)
	m1Actor := directActorFrom(t, fx.Machines[0])
	m1 := fx.M1
	deliverRefreshRelay(e, now+40, listsOps,
		[]*v1.Machine{fx.MachineWire(m1, true, 0, nil)},
		[]*v1.RemoteSession{fx.SessionWire("s1", m1, 1, now, 0, nil, nil)},
		nil, nil,
	)
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
	fetchOp, _ := testrelay.FindGetEventsOp(loadedRes.Effects, "")
	deliverEventsPageRelay(e, now+70, fetchOp, []*v1.StoredEvent{fx.StoredEventWire("s1", 1, nil)}, false)
	readPCID, hsNow, sealer := connectDirectForExistingEngine(t, e, now+75, client, m1Actor)
	now = hsNow

	// Direct batch [1, 2, 4] when prefix=1 accepts only contiguous 2 and drops gapped 4;
	// direct [1] (already in prefix) is also ignored.
	env2 := sealer.sealDirectEvent("s1", 2, m1Actor)
	e.HandleEvent(&corev1.Event{
		NowMs: now + 10,
		Event: &corev1.Event_DirectEventsReceived{
			DirectEventsReceived: &corev1.DirectEventsReceivedEvent{
				MachineId: m1,
				SessionId: "s1",
				PcId:      readPCID,
				RawEnvelopes: testrelay.MustRawSlice([]*v1.Envelope{
					sealer.sealDirectEvent("s1", 1, m1Actor),
					env2,
					sealer.sealDirectEvent("s1", 4, m1Actor),
				}),
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
		Event: testrelay.RawFrame(legID, &v1.ServerFrame{
			Kind: &v1.ServerFrame_EventsAppended{
				EventsAppended: &v1.EventsAppended{
					SessionId: "s1",
					Events:    []*v1.StoredEvent{{Seq: 2, Envelope: env2}},
				},
			},
		}),
	})
	if !buf.fetchErrorReason.IsZero() {
		t.Fatalf("expected fetchErrorReason cleared when !hasServerGap")
	}
	if op, _ := testrelay.FindGetEventsOp(appRes.Effects, ""); op != 0 {
		t.Fatalf("must not fire premature GetEvents when ServerLastSeq=2")
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
		frame    *v1.ServerFrame
		wantSync bool
	}{
		{
			name: "new_machine_device_updated",
			frame: &v1.ServerFrame{
				Kind: &v1.ServerFrame_DeviceUpdated{
					DeviceUpdated: &v1.DeviceUpdated{Device: &v1.Device{Id: "m-new", Kind: v1.DeviceKind_DEVICE_KIND_MACHINE}},
				},
			},
			wantSync: true,
		},
		{
			name: "trusted_machine_device_updated",
			frame: &v1.ServerFrame{
				Kind: &v1.ServerFrame_DeviceUpdated{
					DeviceUpdated: &v1.DeviceUpdated{Device: &v1.Device{Id: mKnown.id, Kind: v1.DeviceKind_DEVICE_KIND_MACHINE}},
				},
			},
			wantSync: false,
		},
		{
			name: "new_client_device_updated",
			frame: &v1.ServerFrame{
				Kind: &v1.ServerFrame_DeviceUpdated{
					DeviceUpdated: &v1.DeviceUpdated{Device: &v1.Device{Id: "c-new", Kind: v1.DeviceKind_DEVICE_KIND_CLIENT}},
				},
			},
			wantSync: true,
		},
		{
			name: "trusted_client_device_updated",
			frame: &v1.ServerFrame{
				Kind: &v1.ServerFrame_DeviceUpdated{
					DeviceUpdated: &v1.DeviceUpdated{Device: &v1.Device{Id: cKnown.id, Kind: v1.DeviceKind_DEVICE_KIND_CLIENT}},
				},
			},
			wantSync: false,
		},
		{
			name: "untrusted_online_machine_updated",
			frame: &v1.ServerFrame{
				Kind: &v1.ServerFrame_MachineUpdated{
					MachineUpdated: &v1.MachineUpdated{Machine: &v1.Machine{Id: "m-new", Online: true}},
				},
			},
			wantSync: true,
		},
		{
			name: "untrusted_offline_machine_updated",
			frame: &v1.ServerFrame{
				Kind: &v1.ServerFrame_MachineUpdated{
					MachineUpdated: &v1.MachineUpdated{Machine: &v1.Machine{Id: "m-off", Online: false}},
				},
			},
			wantSync: false,
		},
		{
			name: "trusted_online_machine_updated",
			frame: &v1.ServerFrame{
				Kind: &v1.ServerFrame_MachineUpdated{
					MachineUpdated: &v1.MachineUpdated{Machine: &v1.Machine{Id: mKnown.id, Online: true}},
				},
			},
			wantSync: false,
		},
		{
			name: "archive_online_machine_updated",
			frame: &v1.ServerFrame{
				Kind: &v1.ServerFrame_MachineUpdated{
					MachineUpdated: &v1.MachineUpdated{Machine: &v1.Machine{Id: mArchive.id, Online: true}},
				},
			},
			wantSync: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, legID := setup()
			_, res := e.HandleEvent(&corev1.Event{
				NowMs: now + 10,
				Event: testrelay.RawFrame(legID, tc.frame),
			})
			var syncOp uint64
			for _, eff := range res.Effects {
				if sr := eff.GetRelayRpc(); sr != nil && sr.Method == corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_SECURITY_STATES {
					syncOp = sr.OpId
				}
			}
			if tc.wantSync && syncOp == 0 {
				t.Fatalf("expected GET_SECURITY_STATES RelayRpcEffect for %s", tc.name)
			}
			if !tc.wantSync && syncOp != 0 {
				t.Fatalf("unexpected GET_SECURITY_STATES RelayRpcEffect op=%d for %s", syncOp, tc.name)
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
		Event: testrelay.RawFrame(legID, &v1.ServerFrame{
			Kind: &v1.ServerFrame_SessionUpdated{
				SessionUpdated: &v1.SessionUpdated{Session: &v1.RemoteSession{SessionId: "s-new", MachineId: mNew, UpdatedMs: now}},
			},
		}),
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
				MachineId:    mNew,
				SessionId:    "s-new",
				RawEnvelopes: testrelay.MustRawSlice([]*v1.Envelope{{Ciphertext: (&v1.Event{Seq: 1, SessionId: "s-new"}).AppendMarshal(nil)}}),
			},
		},
	})
	buf := e.Buffer("s-new")
	if buf.PrefixLen() != 0 {
		t.Fatalf("expected untrusted %s direct event to be dropped before security refresh, got prefix=%d", mNew, buf.PrefixLen())
	}
	_, devRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 8,
		Event: testrelay.RawFrame(legID, &v1.ServerFrame{
			Kind: &v1.ServerFrame_DeviceUpdated{
				DeviceUpdated: &v1.DeviceUpdated{Device: &v1.Device{Id: mNew, Kind: v1.DeviceKind_DEVICE_KIND_MACHINE}},
			},
		}),
	})
	var secOp uint64
	for _, eff := range devRes.Effects {
		if sr := eff.GetRelayRpc(); sr != nil && sr.Method == corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_SECURITY_STATES {
			secOp = sr.OpId
		}
	}
	if secOp == 0 {
		t.Fatalf("expected GET_SECURITY_STATES RelayRpcEffect on DeviceUpdated")
	}
	readPCID, hsNow, sealer := connectDirectForExistingEngine(t, e, now+10, client, mNewActor)
	e.HandleEvent(&corev1.Event{
		NowMs: hsNow + 5,
		Event: &corev1.Event_DirectEventsReceived{
			DirectEventsReceived: &corev1.DirectEventsReceivedEvent{
				MachineId:    mNew,
				SessionId:    "s-new",
				PcId:         readPCID,
				RawEnvelopes: testrelay.MustRawSlice([]*v1.Envelope{sealer.sealDirectEvent("s-new", 1, mNewActor)}),
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
	fx, _, listsOps := startAndConnect(t, e, now)
	deliverRefreshRelay(e, now+40, listsOps,
		[]*v1.Machine{fx.MachineWire(fx.M1, true, 0, nil)},
		[]*v1.RemoteSession{fx.SessionWire("s1", fx.M1, 1, now, 0, nil, nil)},
		nil, nil,
	)
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
	cachedEv := fx.StoredEventWire("s1", 1, &v1.Event{Seq: 1, SessionId: "s1", Kind: &v1.Event_UserMessage{UserMessage: &v1.UserMessage{Text: "hi"}}})
	e.HandleEvent(&corev1.Event{
		NowMs: now + 60,
		Event: &corev1.Event_CacheEventsLoaded{
			CacheEventsLoaded: &corev1.CacheEventsLoadedEvent{
				OpId:      cacheOp,
				SessionId: "s1",
				RawEvents: testrelay.MustRawSlice([]*v1.StoredEvent{cachedEv}),
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
				RawEvents: testrelay.MustRawSlice([]*v1.StoredEvent{cachedEv}),
			},
		},
	})
	snap3 := e.SessionSnapshot("s1")
	if snap3.EventRev <= snap1.EventRev {
		t.Fatalf("expected EventRev to remain monotonic across trimHistory eviction (%d <= %d)", snap3.EventRev, snap1.EventRev)
	}
}

func TestTerminalReadyAndWaitingView(t *testing.T) {
	e := NewEngine()
	now := int64(1_700_000_000_000)
	fx, legID, listsOps := startAndConnect(t, e, now)

	deliverRefreshRelay(e, now+20, listsOps,
		[]*v1.Machine{fx.MachineWire(fx.M1, false, 0, &v1.MachineMeta{Hostname: "MacStudio"})},
		[]*v1.RemoteSession{fx.SessionWire("s1", fx.M1, 1, 0, 0, nil, nil)},
		nil, nil,
	)
	// 1. Relay connected: terminal_ready=true even if machine presence says offline.
	snap1 := e.SessionSnapshot("s1")
	if !e.machineTerminalReady(fx.M1) || !snap1.TerminalReady || snap1.TerminalWaiting {
		t.Fatalf("expected terminal_ready=true when relay connected, got snap=%+v", snap1)
	}
	if lists.MachineName(snap1.Machine, fx.M1) != "MacStudio" {
		t.Fatalf("expected DisplayName=MacStudio, got %q", lists.MachineName(snap1.Machine, fx.M1))
	}

	// 2. Relay drops -> enters displayGrace: terminal_ready=false, terminal_waiting=true.
	e.HandleEvent(&corev1.Event{
		NowMs:  now + 100,
		MonoMs: now + 100,
		Event: &corev1.Event_StreamClosed{
			StreamClosed: &corev1.StreamClosedEvent{LegId: legID, Error: "drop"},
		},
	})
	snap2 := e.SessionSnapshot("s1")
	if e.machineTerminalReady(fx.M1) || snap2.TerminalReady || !snap2.TerminalWaiting {
		t.Fatalf("expected terminal_ready=false terminal_waiting=true during displayGrace, got snap=%+v", snap2)
	}

	// 3. Direct ready overrides relay disconnect: terminal_ready=true, terminal_waiting=false.
	e.directReady[fx.M1] = true
	snap3 := e.SessionSnapshot("s1")
	if !e.machineTerminalReady(fx.M1) || !snap3.TerminalReady || snap3.TerminalWaiting {
		t.Fatalf("expected terminal_ready=true when directReady, got snap=%+v", snap3)
	}

	// 4. Direct drops and displayGrace expires -> terminal_ready=false, terminal_waiting=false.
	e.directReady[fx.M1] = false
	e.HandleEvent(&corev1.Event{
		NowMs:  now + 100 + DisplayGraceMs + 100,
		MonoMs: now + 100 + DisplayGraceMs + 100,
		Event:  &corev1.Event_ClockUpdated{ClockUpdated: &corev1.LocalCalendar{NowMs: now + 100 + DisplayGraceMs + 100}},
	})
	snap4 := e.SessionSnapshot("s1")
	if e.machineTerminalReady(fx.M1) || snap4.TerminalReady || snap4.TerminalWaiting {
		t.Fatalf("expected terminal_ready=false terminal_waiting=false after grace expires, got snap=%+v", snap4)
	}
}

func TestCommandPriorUnknownRetriesThroughMachineOfflineRefusal(t *testing.T) {
	e := NewEngine()
	now := int64(1_700_000_000_000)
	mono := int64(10_000)
	ids, fs := seedEntropy(20)
	e.HandleEvent(&corev1.Event{
		NowMs:      now,
		MonoMs:     mono,
		RandomIds:  ids,
		UnitFloats: fs,
		Event: &corev1.Event_SeedDemoWorld{
			SeedDemoWorld: &corev1.SeedDemoWorldEvent{
				Security: &corev1.DemoSecurity{
					IsMember:          true,
					SelfDeviceId:      "c1",
					AuthorityDeviceId: "c1",
					LeaseEndMs:        now + 86_400_000,
					TrustedMachineIds: []string{"m1"},
				},
				Machines: []*corev1.DemoMachine{{
					MachineId: "m1",
					Online:    true,
					RawMeta:   testrelay.MustRaw(&v1.MachineMeta{Hostname: "Mac", ProtocolVersion: PeerVersion}),
				}},
			},
		},
	})
	startDemoConnected(e, now+10, mono+10)

	prev, ctx := e.BeginStep()
	reqID, dispatched := e.SubmitCommand(ctx, SubmitCommandParams{
		SlotKey:   "upload-complete:m1:tr-1",
		MachineID: "m1",
		Command: &v1.Command{
			Kind: &v1.Command_CompleteImageUpload{
				CompleteImageUpload: &v1.CompleteImageUploadRequest{TransferId: "tr-1"},
			},
		},
	})
	step := e.EndStep(prev, ctx)
	if !dispatched || reqID == "" {
		t.Fatalf("expected CompleteImageUpload dispatched, got reqID=%q dispatched=%v", reqID, dispatched)
	}
	var att1 string
	for _, ef := range step.Effects {
		if dc := ef.GetDispatchCommand(); dc != nil {
			att1 = dc.AttemptId
		}
	}
	if att1 == "" {
		t.Fatalf("missing initial DispatchCommand")
	}

	// Attempt 1: server returns errUnknown("the machine disconnected") as deadline_exceeded.
	now += 500
	mono += 500
	_, r1 := e.HandleEvent(&corev1.Event{
		NowMs:  now,
		MonoMs: mono,
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:     reqID,
				AttemptId:     att1,
				Source:        corev1.OutcomeSource_OUTCOME_SOURCE_SERVER,
				NotForwarded:  false,
				AttemptBound:  false,
				TransportCode: "deadline_exceeded",
			},
		},
	})
	var timer1 *corev1.ScheduleTimerEffect
	for _, ef := range r1.Effects {
		if st := ef.GetScheduleTimer(); st != nil && st.Kind == corev1.TimerKind_TIMER_KIND_COMMAND_RETRY {
			timer1 = st
		}
	}
	if timer1 == nil {
		t.Fatalf("expected retry timer after deadline_exceeded")
	}

	// Fire retry timer -> Attempt 2 must reuse the exact same RequestID.
	now += timer1.DelayMs
	mono += timer1.DelayMs
	_, r2 := e.HandleEvent(&corev1.Event{
		NowMs:  now,
		MonoMs: mono,
		Event: &corev1.Event_TimerFired{
			TimerFired: &corev1.TimerFiredEvent{TimerId: timer1.TimerId, Kind: timer1.Kind, Key: timer1.Key},
		},
	})
	var dc2 *corev1.DispatchCommandEffect
	for _, ef := range r2.Effects {
		if dc := ef.GetDispatchCommand(); dc != nil {
			dc2 = dc
		}
	}
	if dc2 == nil || dc2.RequestId != reqID || !dc2.RetryOfUnknown {
		t.Fatalf("expected retry with same RequestID %q and RetryOfUnknown=true, got %+v", reqID, dc2)
	}

	// Attempt 2 hits server while daemon is mid-reconnect -> server refuses with machine_offline (NotForwarded && AttemptBound).
	// Must NOT immediately settle Unknown; must schedule another retry within unavailable window!
	now += 200
	mono += 200
	_, r3 := e.HandleEvent(&corev1.Event{
		NowMs:  now,
		MonoMs: mono,
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:     reqID,
				AttemptId:     dc2.AttemptId,
				Source:        corev1.OutcomeSource_OUTCOME_SOURCE_SERVER,
				NotForwarded:  true,
				AttemptBound:  true,
				OuterReason:   "machine_offline",
				TransportCode: "unavailable",
			},
		},
	})
	if len(r3.Settled) != 0 {
		t.Fatalf("prematurely settled command on machine_offline after prior unknown: %+v", r3.Settled)
	}
	var timer2 *corev1.ScheduleTimerEffect
	for _, ef := range r3.Effects {
		if st := ef.GetScheduleTimer(); st != nil && st.Kind == corev1.TimerKind_TIMER_KIND_COMMAND_RETRY {
			timer2 = st
		}
	}
	if timer2 == nil {
		t.Fatalf("expected retry timer after machine_offline refusal on prior-unknown command")
	}

	// Fire retry timer -> Attempt 3 reuses same RequestID and receives daemon's cached COMPLETE result!
	now += timer2.DelayMs
	mono += timer2.DelayMs
	_, r4 := e.HandleEvent(&corev1.Event{
		NowMs:  now,
		MonoMs: mono,
		Event: &corev1.Event_TimerFired{
			TimerFired: &corev1.TimerFiredEvent{TimerId: timer2.TimerId, Kind: timer2.Kind, Key: timer2.Key},
		},
	})
	var dc3 *corev1.DispatchCommandEffect
	for _, ef := range r4.Effects {
		if dc := ef.GetDispatchCommand(); dc != nil {
			dc3 = dc
		}
	}
	if dc3 == nil || dc3.RequestId != reqID || !dc3.RetryOfUnknown {
		t.Fatalf("expected 3rd attempt with same RequestID %q, got %+v", reqID, dc3)
	}
	_, r5 := e.HandleEvent(&corev1.Event{
		NowMs:  now + 100,
		MonoMs: mono + 100,
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:         reqID,
				AttemptId:         dc3.AttemptId,
				Source:            corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON,
				Signed:            true,
				HashesMatch:       true,
				AttemptBound:      true,
				OuterMatchesInner: true,
				OuterOutcome:      "executed",
				OuterReason:       "executed",
				RawResult: testrelay.MustRaw(&v1.CommandResult{
					Outcome:         "executed",
					Reason:          "executed",
					ResultAvailable: true,
					Kind: &v1.CommandResult_CompleteImageUpload{
						CompleteImageUpload: &v1.CompleteImageUploadResponse{
							Status: &v1.ImageUploadStatus{
								TransferId: "tr-1",
								State:      v1.ImageUploadState_IMAGE_UPLOAD_STATE_COMPLETE,
								ImageId:    "img-1",
							},
						},
					},
				}),
			},
		},
	})
	if len(r5.Settled) != 1 || r5.Settled[0].Category != outcome.Executed || r5.Settled[0].RequestID != reqID {
		t.Fatalf("expected settled Executed for %q, got %+v", reqID, r5.Settled)
	}
}
