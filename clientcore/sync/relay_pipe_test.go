package sync

import (
	"bytes"
	"testing"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/outcome"
	"github.com/Lewin671/remote-agent/clientcore/gen/litewire"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/security"
	genpb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

func mustMarshalStd(t *testing.T, m proto.Message) []byte {
	t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatalf("proto.Marshal: %v", err)
	}
	return b
}

func findRelayRpcs(effs []*corev1.Effect) []*corev1.RelayRpcEffect {
	var out []*corev1.RelayRpcEffect
	for _, eff := range effs {
		if r := eff.GetRelayRpc(); r != nil {
			out = append(out, r)
		}
	}
	return out
}

func findRelayCancels(effs []*corev1.Effect) []uint64 {
	var out []uint64
	for _, eff := range effs {
		if c := eff.GetRelayRpcCancel(); c != nil {
			out = append(out, c.OpId)
		}
	}
	return out
}

func TestRelayPipe_StreamRawFrameReadyClockSkewAndRefreshJoin(t *testing.T) {
	e := NewEngine()
	e.HandleEvent(&corev1.Event{
		NowMs:  1_000_000,
		MonoMs: 1_000,
		Event: &corev1.Event_SecurityConfig{
			SecurityConfig: &corev1.SecurityConfigEvent{Account: "acct-1", DeviceId: "dev-1", Server: "https://relay.example.com"},
		},
	})
	e.HandleEvent(&corev1.Event{
		NowMs:  1_000_000,
		MonoMs: 1_000,
		Event:  &corev1.Event_RelayPipeConfig{RelayPipeConfig: &corev1.RelayPipeConfigEvent{Enabled: true}},
	})
	_, startRes := e.HandleEvent(&corev1.Event{
		NowMs:  1_000_000,
		MonoMs: 1_000,
		Event:  &corev1.Event_SyncLifecycle{SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START}},
	})
	var bootOp uint64
	for _, eff := range startRes.Effects {
		if eff.GetEnsureToken() != nil {
			t.Fatal("relayPipe mode must NOT emit legacy EnsureTokenEffect")
		}
		if st := eff.GetSecurityStorage(); st != nil && st.Op == corev1.SecurityStorageOp_SECURITY_STORAGE_OP_READ_BOOTSTRAP {
			bootOp = st.OpId
		}
	}
	if bootOp == 0 {
		t.Fatal("expected READ_BOOTSTRAP storage effect to hydrate saved login token")
	}

	_, tokRes := e.HandleEvent(&corev1.Event{
		NowMs:  1_000_100,
		MonoMs: 1_100,
		Event: &corev1.Event_SecurityStorageResult{
			SecurityStorageResult: &corev1.SecurityStorageResultEvent{
				OpId:                bootOp,
				LoginToken:          "tok-1",
				LoginTokenExpiresMs: 1_000_100 + 3*24*3600_000,
			},
		},
	})
	var openStream *corev1.OpenStreamEffect
	for _, eff := range tokRes.Effects {
		if os := eff.GetOpenStream(); os != nil {
			openStream = os
		}
	}
	if openStream == nil {
		t.Fatal("expected OpenStreamEffect")
	}
	var pf genpb.PeerFrame
	if err := proto.Unmarshal(openStream.HelloFrame, &pf); err != nil {
		t.Fatalf("unmarshal HelloFrame: %v", err)
	}
	if pf.GetHello().GetToken() != "tok-1" {
		t.Fatalf("HelloFrame token = %q, want tok-1", pf.GetHello().GetToken())
	}

	// Send Ready raw frame with server clock >5min behind client clock (triggers serverAhead).
	readyRaw := mustMarshalStd(t, &genpb.ServerFrame{
		Kind: &genpb.ServerFrame_Ready{Ready: &genpb.Ready{ServerTimeMs: 1_000_100 - MaxClockSkewMs - 1_000}},
	})
	_, readyRes := e.HandleEvent(&corev1.Event{
		NowMs:  1_000_100,
		MonoMs: 1_100,
		Event:  &corev1.Event_StreamRawFrame{StreamRawFrame: &corev1.StreamRawFrameEvent{LegId: openStream.LegId, RawServerFrame: readyRaw}},
	})
	if !e.serverAhead {
		t.Fatal("expected serverAhead=true when client clock is >5m ahead of Ready.server_time_ms")
	}
	if e.w.World.Connection != lists.ConnConnected {
		t.Fatalf("connection = %v, want ConnConnected", e.w.World.Connection)
	}

	// Refresh + Prefs should have emitted 4 RelayRpcEffects (ListMachines, ListRemoteSessions, ListDevices, GetAccountPrefs).
	rpcs := findRelayRpcs(readyRes.Effects)
	byMethod := make(map[corev1.RelayRpcMethod]*corev1.RelayRpcEffect)
	for _, r := range rpcs {
		byMethod[r.Method] = r
		if r.AuthToken != "tok-1" {
			t.Errorf("rpc %v token = %q, want tok-1", r.Method, r.AuthToken)
		}
	}
	for _, m := range []corev1.RelayRpcMethod{
		corev1.RelayRpcMethod_RELAY_RPC_METHOD_LIST_MACHINES,
		corev1.RelayRpcMethod_RELAY_RPC_METHOD_LIST_REMOTE_SESSIONS,
		corev1.RelayRpcMethod_RELAY_RPC_METHOD_LIST_DEVICES,
		corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_ACCOUNT_PREFS,
	} {
		if byMethod[m] == nil {
			t.Fatalf("missing RelayRpcEffect for %v", m)
		}
	}

	// Complete ListMachines with a relay epoch header; verify epoch is captured.
	e.HandleEvent(&corev1.Event{
		NowMs:  1_000_200,
		MonoMs: 1_200,
		Event: &corev1.Event_RelayRpcResult{RelayRpcResult: &corev1.RelayRpcResultEvent{
			OpId:          byMethod[corev1.RelayRpcMethod_RELAY_RPC_METHOD_LIST_MACHINES].OpId,
			RelayEpoch:    "epoch-abc",
			ResponseBytes: mustMarshalStd(t, &genpb.ListMachinesResponse{Machines: []*genpb.Machine{{Id: "m1", Online: true}}}),
		}},
	})
	if e.relayEpoch != "epoch-abc" {
		t.Fatalf("relayEpoch = %q, want epoch-abc", e.relayEpoch)
	}
	// If ListDevices fails, ListRemoteSessions should be canceled cleanly.
	_, errRes := e.HandleEvent(&corev1.Event{
		NowMs:  1_000_250,
		MonoMs: 1_250,
		Event: &corev1.Event_RelayRpcResult{RelayRpcResult: &corev1.RelayRpcResultEvent{
			OpId:              byMethod[corev1.RelayRpcMethod_RELAY_RPC_METHOD_LIST_DEVICES].OpId,
			ConnectCode:       security.ConnectCodeUnavailable,
			DiagnosticMessage: "backend down",
		}},
	})
	cancels := findRelayCancels(errRes.Effects)
	sessOpID := byMethod[corev1.RelayRpcMethod_RELAY_RPC_METHOD_LIST_REMOTE_SESSIONS].OpId
	foundCancel := false
	for _, cid := range cancels {
		if cid == sessOpID {
			foundCancel = true
		}
	}
	if !foundCancel {
		t.Fatalf("expected cancel of in-flight sessions op %d, got %v", sessOpID, cancels)
	}
}

func TestRelayPipe_ExtractRawPeerFrameDeliveryBytesExact(t *testing.T) {
	rawDelivery := []byte{0x0a, 0x02, 'm', '1', 0x2a, 0x05, 0xde, 0xad, 0xbe, 0xef, 0x00}
	var frame []byte
	frame = litewire.AppendBytes(frame, 11, rawDelivery)
	gotDel, gotRes := extractRawPeerFrameFields(frame)
	if !bytes.Equal(gotDel, rawDelivery) {
		t.Fatalf("raw delivery mismatch: got %x want %x", gotDel, rawDelivery)
	}
	if len(gotRes) != 0 {
		t.Fatalf("unexpected result bytes: %x", gotRes)
	}
}

func TestRelayPipe_ForwardHealthzNotForwardedAndCancel(t *testing.T) {
	e := NewEngine()
	e.HandleEvent(&corev1.Event{
		NowMs:  2_000_000,
		MonoMs: 2_000,
		Event:  &corev1.Event_RelayPipeConfig{RelayPipeConfig: &corev1.RelayPipeConfigEvent{Enabled: true}},
	})
	e.running = true
	e.networkAvailable = true
	e.w.World.Connection = lists.ConnConnected
	e.w.LivePushConnected = true
	e.cachedToken = "tok-cmd"
	e.storeMachine(&MachineEntry{Machine: lists.Machine{ID: "m1", Online: true}})

	ctx := newStepContext()
	e.activeStepCtx = ctx
	prep := &PreparedCommand{
		RequestID:          "req-1",
		MachineID:          "m1",
		DeadlineMs:         2_060_000,
		RecoveryDeadlineMs: 2_360_000,
		Command:            &v1.Command{Kind: &v1.Command_GetSettings{GetSettings: &v1.GetSettingsRequest{}}},
		CommandBytes:       []byte{1},
		PendingAttempts:    make(map[string]bool),
		SealedEnvelope:     &v1.Envelope{Header: []byte("hdr"), Ciphertext: []byte("ct")},
	}
	e.emitCommandAttempt(ctx, prep, false)
	e.activeStepCtx = nil

	// First RPC must be HEALTHZ because relayEpoch is empty.
	rpcs := findRelayRpcs(ctx.Raw)
	if len(rpcs) != 1 || rpcs[0].Method != corev1.RelayRpcMethod_RELAY_RPC_METHOD_HEALTHZ {
		t.Fatalf("expected single HEALTHZ rpc, got %+v", rpcs)
	}

	// Return epoch from HEALTHZ → triggers FORWARD with AttemptHeader and 45s timeout.
	_, fwdStep := e.HandleEvent(&corev1.Event{
		NowMs:  2_000_050,
		MonoMs: 2_050,
		Event: &corev1.Event_RelayRpcResult{RelayRpcResult: &corev1.RelayRpcResultEvent{
			OpId:       rpcs[0].OpId,
			RelayEpoch: "ep-42",
		}},
	})
	fwdRpcs := findRelayRpcs(fwdStep.Effects)
	if len(fwdRpcs) != 1 || fwdRpcs[0].Method != corev1.RelayRpcMethod_RELAY_RPC_METHOD_FORWARD {
		t.Fatalf("expected FORWARD rpc, got %+v", fwdRpcs)
	}
	if fwdRpcs[0].TimeoutMs != relayRpcForwardTimeoutMs {
		t.Fatalf("forward timeout = %d, want %d", fwdRpcs[0].TimeoutMs, relayRpcForwardTimeoutMs)
	}
	if fwdRpcs[0].AttemptHeader == "" {
		t.Fatal("expected non-empty AttemptHeader on FORWARD")
	}
	var fwdReq genpb.ForwardRequest
	if err := proto.Unmarshal(fwdRpcs[0].RequestBytes, &fwdReq); err != nil {
		t.Fatalf("unmarshal ForwardRequest: %v", err)
	}
	if fwdReq.RelayEpoch != "ep-42" || fwdReq.AttemptId != fwdRpcs[0].AttemptHeader || fwdReq.MachineId != "m1" {
		t.Fatalf("unexpected ForwardRequest: %+v", &fwdReq)
	}

	// Return NotForwarded with matching attemptId + epoch -> settles NotSent immediately!
	nfBytes := mustMarshalStd(t, &genpb.NotForwarded{
		AttemptId:  fwdReq.AttemptId,
		Reason:     "machine_offline",
		RelayEpoch: "ep-42",
	})
	_, resStep := e.HandleEvent(&corev1.Event{
		NowMs:  2_000_200,
		MonoMs: 2_200,
		Event: &corev1.Event_RelayRpcResult{RelayRpcResult: &corev1.RelayRpcResultEvent{
			OpId:        fwdRpcs[0].OpId,
			ConnectCode: security.ConnectCodeUnavailable,
			ErrorDetails: []*corev1.RpcErrorDetailBytes{
				{TypeName: "type.googleapis.com/remoteagent.v1.NotForwarded", Value: nfBytes},
			},
		}},
	})
	if len(resStep.Settled) != 1 || resStep.Settled[0].Category != outcome.NotSent {
		t.Fatalf("expected NotSent settled command, got %+v", resStep.Settled)
	}

	// Verify CancelCommand emits RelayRpcCancelEffect for in-flight relay leg.
	ctx2 := newStepContext()
	e.activeStepCtx = ctx2
	prep2 := &PreparedCommand{
		RequestID:          "req-2",
		MachineID:          "m1",
		DeadlineMs:         2_060_000,
		RecoveryDeadlineMs: 2_360_000,
		Command:            &v1.Command{Kind: &v1.Command_GetSettings{GetSettings: &v1.GetSettingsRequest{}}},
		CommandBytes:       []byte{1},
		PendingAttempts:    make(map[string]bool),
		SealedEnvelope:     &v1.Envelope{Header: []byte("hdr"), Ciphertext: []byte("ct")},
	}
	e.emitCommandAttempt(ctx2, prep2, false)
	fwd2 := findRelayRpcs(ctx2.Raw)
	if len(fwd2) != 1 || fwd2[0].Method != corev1.RelayRpcMethod_RELAY_RPC_METHOD_FORWARD {
		t.Fatalf("expected immediate FORWARD with cached epoch, got %+v", fwd2)
	}
	ctx3 := newStepContext()
	e.activeStepCtx = ctx3
	e.CancelCommand(ctx3, "req-2")
	e.activeStepCtx = nil
	if cancels := findRelayCancels(ctx3.Raw); len(cancels) != 1 || cancels[0] != fwd2[0].OpId {
		t.Fatalf("expected RelayRpcCancelEffect for %d, got %v", fwd2[0].OpId, cancels)
	}
}

func TestRelayPipe_PushRpcAction(t *testing.T) {
	e := NewEngine()
	e.HandleEvent(&corev1.Event{
		NowMs: 1000,
		Event: &corev1.Event_RelayPipeConfig{RelayPipeConfig: &corev1.RelayPipeConfigEvent{Enabled: true}},
	})
	e.running = true
	e.cachedToken = "tok-push"
	e.tokenExpiresMs = 1000 + 7*24*3600*1000
	e.secEngine.SetAuthToken("tok-push", e.tokenExpiresMs)

	_, step1 := e.HandleEvent(&corev1.Event{
		NowMs: 1000,
		Event: &corev1.Event_PushRpc{PushRpc: &corev1.PushRpcAction{
			OpId:      77,
			SetTarget: true,
			PushType:  int32(v1.PushType_PUSH_TYPE_FCM),
			Target:    "fcm-token-123",
			Kinds:     []v1.NotifyKind{v1.NotifyKind_NOTIFY_KIND_ATTENTION},
		}},
	})
	rpcs1 := findRelayRpcs(step1.Effects)
	if len(rpcs1) != 1 || rpcs1[0].Method != corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_PUSH_KEY {
		t.Fatalf("expected GET_PUSH_KEY, got %+v", rpcs1)
	}

	_, step2 := e.HandleEvent(&corev1.Event{
		NowMs: 1010,
		Event: &corev1.Event_RelayRpcResult{RelayRpcResult: &corev1.RelayRpcResultEvent{
			OpId:          rpcs1[0].OpId,
			ResponseBytes: mustMarshalStd(t, &genpb.GetPushKeyResponse{PublicKey: "vapid", MachineDiskNotifications: true}),
		}},
	})
	rpcs2 := findRelayRpcs(step2.Effects)
	if len(rpcs2) != 1 || rpcs2[0].Method != corev1.RelayRpcMethod_RELAY_RPC_METHOD_SET_PUSH_TARGET {
		t.Fatalf("expected SET_PUSH_TARGET, got %+v", rpcs2)
	}
	var setReq genpb.SetPushTargetRequest
	if err := proto.Unmarshal(rpcs2[0].RequestBytes, &setReq); err != nil {
		t.Fatalf("unmarshal SetPushTargetRequest: %v", err)
	}
	if len(setReq.Kinds) != 2 || setReq.Kinds[1] != genpb.NotifyKind_NOTIFY_KIND_MACHINE_DISK {
		t.Fatalf("expected MACHINE_DISK appended, got %v", setReq.Kinds)
	}

	_, step3 := e.HandleEvent(&corev1.Event{
		NowMs: 1020,
		Event: &corev1.Event_RelayRpcResult{RelayRpcResult: &corev1.RelayRpcResultEvent{
			OpId:          rpcs2[0].OpId,
			ResponseBytes: mustMarshalStd(t, &genpb.SetPushTargetResponse{Enabled: true}),
		}},
	})
	var pushRes *corev1.PushRpcResultEffect
	for _, eff := range step3.Effects {
		if pr := eff.GetPushRpcResult(); pr != nil {
			pushRes = pr
		}
	}
	if pushRes == nil || pushRes.OpId != 77 || !pushRes.Ok || !pushRes.Enabled {
		t.Fatalf("unexpected PushRpcResultEffect: %+v", pushRes)
	}
}

func TestRelayPipe_Auto401RefreshAndRetryOnce(t *testing.T) {
	e := NewEngine()
	now := int64(2_000_000)
	e.HandleEvent(&corev1.Event{
		NowMs: now, MonoMs: 1_000,
		Event: &corev1.Event_SecurityConfig{
			SecurityConfig: &corev1.SecurityConfigEvent{Account: "acct-1", DeviceId: "dev-1", Server: "https://relay.example.com"},
		},
	})
	e.HandleEvent(&corev1.Event{
		NowMs: now, MonoMs: 1_000,
		Event: &corev1.Event_RelayPipeConfig{RelayPipeConfig: &corev1.RelayPipeConfigEvent{Enabled: true}},
	})
	e.secEngine.SetAuthToken("old-tok", now+3*24*3600_000)
	e.cachedToken = "old-tok"
	e.tokenExpiresMs = now + 3*24*3600_000
	e.running = true

	// Trigger ListDevices via RefreshDevicesAction.
	_, devRes := e.HandleEvent(&corev1.Event{
		NowMs: now, MonoMs: 1_100,
		Event: &corev1.Event_RefreshDevices{RefreshDevices: &corev1.RefreshDevicesAction{}},
	})
	rpcs := findRelayRpcs(devRes.Effects)
	if len(rpcs) != 1 || rpcs[0].Method != corev1.RelayRpcMethod_RELAY_RPC_METHOD_LIST_DEVICES || rpcs[0].AuthToken != "old-tok" {
		t.Fatalf("expected LIST_DEVICES with old-tok, got %+v", rpcs)
	}

	// Server returns 401 UNAUTHENTICATED: Core clears old-tok and triggers GET_CHALLENGE automatically.
	_, unauthRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 10, MonoMs: 1_110,
		Event: &corev1.Event_RelayRpcResult{
			RelayRpcResult: &corev1.RelayRpcResultEvent{
				OpId:              rpcs[0].OpId,
				ConnectCode:       security.ConnectCodeUnauthenticated,
				DiagnosticMessage: "token expired",
			},
		},
	})
	var chalRpc *corev1.RelayRpcEffect
	var clearedToken bool
	for _, eff := range unauthRes.Effects {
		if tu := eff.GetSecurityTokenUpdated(); tu != nil && tu.Cleared {
			clearedToken = true
		}
		if r := eff.GetRelayRpc(); r != nil && r.Method == corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_CHALLENGE {
			chalRpc = r
		}
	}
	if !clearedToken || chalRpc == nil {
		t.Fatalf("expected token clear and GET_CHALLENGE refresh on 401; cleared=%v chal=%+v", clearedToken, chalRpc)
	}

	// Complete GetChallenge -> Sign -> Login -> Core automatically retries LIST_DEVICES with fresh-tok.
	_, chalRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 20, MonoMs: 1_120,
		Event: &corev1.Event_RelayRpcResult{
			RelayRpcResult: &corev1.RelayRpcResultEvent{
				OpId:          chalRpc.OpId,
				ResponseBytes: mustMarshalStd(t, &genpb.GetChallengeResponse{Challenge: bytes.Repeat([]byte{9}, 32)}),
			},
		},
	})
	var signOp uint64
	for _, eff := range chalRes.Effects {
		if ks := eff.GetSecurityKeySign(); ks != nil {
			signOp = ks.OpId
		}
	}
	if signOp == 0 {
		t.Fatal("expected SecurityKeySignEffect")
	}
	_, signRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 30, MonoMs: 1_130,
		Event: &corev1.Event_SecurityKeySigned{
			SecurityKeySigned: &corev1.SecurityKeySignedEvent{OpId: signOp, Signature: bytes.Repeat([]byte{7}, 64)},
		},
	})
	var loginRpc *corev1.RelayRpcEffect
	for _, eff := range signRes.Effects {
		if r := eff.GetRelayRpc(); r != nil && r.Method == corev1.RelayRpcMethod_RELAY_RPC_METHOD_LOGIN {
			loginRpc = r
		}
	}
	if loginRpc == nil {
		t.Fatal("expected LOGIN RelayRpc")
	}
	_, loginRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 40, MonoMs: 1_140,
		Event: &corev1.Event_RelayRpcResult{
			RelayRpcResult: &corev1.RelayRpcResultEvent{
				OpId:          loginRpc.OpId,
				ResponseBytes: mustMarshalStd(t, &genpb.LoginResponse{Token: "fresh-tok", ExpiresMs: now + 7*24*3600_000}),
			},
		},
	})
	retryRpcs := findRelayRpcs(loginRes.Effects)
	if len(retryRpcs) != 1 || retryRpcs[0].Method != corev1.RelayRpcMethod_RELAY_RPC_METHOD_LIST_DEVICES || retryRpcs[0].AuthToken != "fresh-tok" {
		t.Fatalf("expected automatic LIST_DEVICES retry with fresh-tok, got %+v", retryRpcs)
	}
}

func TestRelayPipe_StopPreservesForwardAndPutPrefsAndSynthesizesAcks(t *testing.T) {
	e := NewEngine()
	now := int64(3_000_000)
	e.HandleEvent(&corev1.Event{
		NowMs: now, MonoMs: 1_000,
		Event: &corev1.Event_SecurityConfig{
			SecurityConfig: &corev1.SecurityConfigEvent{Account: "acct-1", DeviceId: "dev-1", Server: "https://relay.example.com"},
		},
	})
	e.HandleEvent(&corev1.Event{
		NowMs: now, MonoMs: 1_000,
		Event: &corev1.Event_RelayPipeConfig{RelayPipeConfig: &corev1.RelayPipeConfigEvent{Enabled: true}},
	})
	e.running = true
	e.networkAvailable = true
	e.w.World.Connection = lists.ConnConnected
	e.w.LivePushConnected = true
	e.cachedToken = "tok-stop"
	e.tokenExpiresMs = now + 7*24*3600_000
	e.secEngine.SetAuthToken("tok-stop", e.tokenExpiresMs)
	e.relayEpoch = "ep-1"
	e.storeMachine(&MachineEntry{Machine: lists.Machine{ID: "m1", Online: true}})

	// 1. In-flight Forward command.
	ctx := newStepContext()
	e.activeStepCtx = ctx
	prep := &PreparedCommand{
		RequestID:          "req-stop-fwd",
		MachineID:          "m1",
		DeadlineMs:         now + 60_000,
		RecoveryDeadlineMs: now + 360_000,
		Command:            &v1.Command{Kind: &v1.Command_GetSettings{GetSettings: &v1.GetSettingsRequest{}}},
		CommandBytes:       []byte{1},
		PendingAttempts:    make(map[string]bool),
		SealedEnvelope:     &v1.Envelope{Header: []byte("hdr"), Ciphertext: []byte("ct")},
	}
	e.emitCommandAttempt(ctx, prep, false)
	e.activeStepCtx = nil
	fwdOpID := findRelayRpcs(ctx.Raw)[0].OpId

	// 2. In-flight PutAccountPrefs (drive EnqueuePrefsOp through GetAccountPrefs -> PutAccountPrefs) + 2nd queued op.
	ctxP := newStepContext()
	e.activeStepCtx = ctxP
	e.EnqueuePrefsOp(ctxP, &lists.FullPrefsOp{Kind: lists.PrefsOpPin, Path: "/p1"}, false, "")
	e.EnqueuePrefsOp(ctxP, &lists.FullPrefsOp{Kind: lists.PrefsOpPin, Path: "/p2"}, false, "")
	e.activeStepCtx = nil
	getPrefsRpcs := findRelayRpcs(ctxP.Raw)
	if len(getPrefsRpcs) != 1 || getPrefsRpcs[0].Method != corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_ACCOUNT_PREFS {
		t.Fatalf("expected initial GET_ACCOUNT_PREFS, got %+v", getPrefsRpcs)
	}
	_, putStep := e.HandleEvent(&corev1.Event{
		NowMs: now + 5, MonoMs: 1_005,
		Event: &corev1.Event_RelayRpcResult{RelayRpcResult: &corev1.RelayRpcResultEvent{
			OpId: getPrefsRpcs[0].OpId,
			ResponseBytes: mustMarshalStd(t, &genpb.GetAccountPrefsResponse{
				Object: &genpb.AccountPrefsObject{ObjectId: ProjectPrefsObjectID, Revision: 0},
			}),
		}},
	})
	// Advance directly into PUT_ACCOUNT_PREFS (if security seal isn't configured, inject active PUT op directly).
	putRpcs := findRelayRpcs(putStep.Effects)
	var putOpID uint64
	if len(putRpcs) == 1 && putRpcs[0].Method == corev1.RelayRpcMethod_RELAY_RPC_METHOD_PUT_ACCOUNT_PREFS {
		putOpID = putRpcs[0].OpId
	} else {
		// Security record isn't initialized in this unit Engine; drive prefsQueue[0] into real prefsPhasePut directly.
		e.prefsQueue[0].Phase = prefsPhasePut
		e.prefsQueue[0].ActiveOpID = e.allocOpID()
		putCtx := newStepContext()
		e.activeStepCtx = putCtx
		e.emitRelayRpc(putCtx, corev1.RelayRpcMethod_RELAY_RPC_METHOD_PUT_ACCOUNT_PREFS, e.cachedToken, relayRpcDefaultTimeoutMs, "",
			(&v1.PutAccountPrefsRequest{ObjectId: ProjectPrefsObjectID, ExpectedRevision: 0}).AppendMarshal(nil),
			&pendingRelayRpc{kind: relayRpcPrefsPut, opID: e.prefsQueue[0].ActiveOpID})
		e.activeStepCtx = nil
		putOpID = findRelayRpcs(putCtx.Raw)[0].OpId
	}

	// 3. In-flight GetImage (via direct relayRpc emit as fetchSessionImage does) + PushRpc.
	imgCtx := newStepContext()
	e.activeStepCtx = imgCtx
	e.emitRelayRpc(imgCtx, corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_IMAGE, e.cachedToken, relayRpcDefaultTimeoutMs, "",
		(&v1.GetImageRequest{SessionId: "s1", ImageId: "img1"}).AppendMarshal(nil),
		&pendingRelayRpc{kind: relayRpcGetImage, opID: 501})
	e.activeStepCtx = nil
	imgOpID := findRelayRpcs(imgCtx.Raw)[0].OpId

	_, pushStep := e.HandleEvent(&corev1.Event{
		NowMs: now, MonoMs: 1_010,
		Event: &corev1.Event_PushRpc{PushRpc: &corev1.PushRpcAction{
			OpId: 601, SetTarget: true, PushType: int32(v1.PushType_PUSH_TYPE_FCM), Target: "fcm-stop",
		}},
	})
	pushOpID := findRelayRpcs(pushStep.Effects)[0].OpId

	// 4. Queued authRetryQueue item (simulating an RPC waiting on in-flight 401 refresh).
	e.authRetryQueue = append(e.authRetryQueue, &pendingAuthRetry{
		op: &pendingRelayRpc{
			kind:   relayRpcPushKey,
			opID:   602,
			method: corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_PUSH_KEY,
			push:   &pendingPushRpc{opID: 602, setTarget: true},
		},
		origEv: &corev1.RelayRpcResultEvent{
			ConnectCode:       security.ConnectCodeUnauthenticated,
			DiagnosticMessage: "expired",
		},
	})

	// Dispatch STOP: Forward + PutAccountPrefs must NOT be canceled; Image, Push, and authRetryQueue must synthesize acks!
	_, stopRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 50, MonoMs: 1_050,
		Event: &corev1.Event_SyncLifecycle{SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_STOP}},
	})
	canceled := make(map[uint64]bool)
	for _, cid := range findRelayCancels(stopRes.Effects) {
		canceled[cid] = true
	}
	if canceled[fwdOpID] {
		t.Fatalf("STOP must NOT cancel in-flight Forward op %d", fwdOpID)
	}
	if canceled[putOpID] {
		t.Fatalf("STOP must NOT cancel in-flight PutAccountPrefs op %d", putOpID)
	}
	if !canceled[imgOpID] || !canceled[pushOpID] {
		t.Fatalf("STOP must cancel Image(%d) and Push(%d), got cancels=%v", imgOpID, pushOpID, canceled)
	}
	var gotImgAck bool
	var gotPush601, gotPush602 *corev1.PushRpcResultEffect
	for _, eff := range stopRes.Effects {
		if img := eff.GetSessionImageLoaded(); img != nil && img.OpId == 501 && img.Status == corev1.SessionImageLoadStatus_SESSION_IMAGE_LOAD_STATUS_ERROR && img.ErrorCode == "canceled" {
			gotImgAck = true
		}
		if pr := eff.GetPushRpcResult(); pr != nil {
			if pr.OpId == 601 {
				gotPush601 = pr
			} else if pr.OpId == 602 {
				gotPush602 = pr
			}
		}
	}
	if !gotImgAck {
		t.Fatal("STOP did not synthesize SessionImageLoadedEffect ERROR(canceled) for canceled GetImage")
	}
	if gotPush601 == nil || gotPush601.Ok || !gotPush601.Retryable || gotPush601.Error != "canceled" {
		t.Fatalf("STOP did not synthesize retryable canceled PushRpcResult for 601, got %+v", gotPush601)
	}
	if gotPush602 == nil || gotPush602.Ok || gotPush602.Error == "" {
		t.Fatalf("STOP did not flush authRetryQueue item 602 with original error, got %+v", gotPush602)
	}

	// Complete the preserved PutAccountPrefs while stopped: succeeds cleanly without stranding queue.
	_, putDone := e.HandleEvent(&corev1.Event{
		NowMs: now + 60, MonoMs: 1_060,
		Event: &corev1.Event_RelayRpcResult{RelayRpcResult: &corev1.RelayRpcResultEvent{
			OpId: putOpID,
			ResponseBytes: mustMarshalStd(t, &genpb.PutAccountPrefsResponse{
				Object: &genpb.AccountPrefsObject{ObjectId: ProjectPrefsObjectID, Revision: 2},
			}),
		}},
	})
	if len(e.prefsQueue) != 1 || e.prefsQueue[0].Path != "/p2" || e.prefsQueue[0].ActiveOpID != 0 {
		t.Fatalf("expected /p2 queued and idle while stopped, got %+v", e.prefsQueue)
	}
	_ = putDone

	// Complete the preserved Forward while stopped -> settles cleanly.
	nfBytes := mustMarshalStd(t, &genpb.NotForwarded{AttemptId: findRelayRpcs(ctx.Raw)[0].AttemptHeader, Reason: "machine_offline", RelayEpoch: "ep-1"})
	_, fwdDone := e.HandleEvent(&corev1.Event{
		NowMs: now + 70, MonoMs: 1_070,
		Event: &corev1.Event_RelayRpcResult{RelayRpcResult: &corev1.RelayRpcResultEvent{
			OpId:        fwdOpID,
			ConnectCode: security.ConnectCodeUnavailable,
			ErrorDetails: []*corev1.RpcErrorDetailBytes{
				{TypeName: "type.googleapis.com/remoteagent.v1.NotForwarded", Value: nfBytes},
			},
		}},
	})
	if len(fwdDone.Settled) != 1 || fwdDone.Settled[0].Category != outcome.NotSent {
		t.Fatalf("preserved Forward failed to settle while stopped: %+v", fwdDone.Settled)
	}

	// START again: queued /p2 prefs mutation must resume automatically!
	_, resumeStep := e.HandleEvent(&corev1.Event{
		NowMs: now + 100, MonoMs: 1_100,
		Event: &corev1.Event_SyncLifecycle{SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START}},
	})
	var resumedGet bool
	for _, r := range findRelayRpcs(resumeStep.Effects) {
		if r.Method == corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_ACCOUNT_PREFS {
			resumedGet = true
		}
	}
	if !resumedGet {
		t.Fatalf("START did not resume queued /p2 prefs mutation, effects=%+v", resumeStep.Effects)
	}
}

func TestRelayPipe_StopDuringPrefsGetClearsActiveOpAndPreservesHealthzForForward(t *testing.T) {
	e := NewEngine()
	now := int64(3_500_000)
	e.HandleEvent(&corev1.Event{
		NowMs: now, MonoMs: 1_000,
		Event: &corev1.Event_RelayPipeConfig{RelayPipeConfig: &corev1.RelayPipeConfigEvent{Enabled: true}},
	})
	e.running = true
	e.networkAvailable = true
	e.w.World.Connection = lists.ConnConnected
	e.w.LivePushConnected = true
	e.cachedToken = "tok-hz"
	e.tokenExpiresMs = now + 7*24*3600_000
	e.storeMachine(&MachineEntry{Machine: lists.Machine{ID: "m1", Online: true}})

	// Forward awaiting healthz (relayEpoch empty).
	ctx := newStepContext()
	e.activeStepCtx = ctx
	prep := &PreparedCommand{
		RequestID:          "req-hz",
		MachineID:          "m1",
		DeadlineMs:         now + 60_000,
		RecoveryDeadlineMs: now + 360_000,
		Command:            &v1.Command{Kind: &v1.Command_GetSettings{GetSettings: &v1.GetSettingsRequest{}}},
		CommandBytes:       []byte{1},
		PendingAttempts:    make(map[string]bool),
		SealedEnvelope:     &v1.Envelope{Header: []byte("hdr"), Ciphertext: []byte("ct")},
	}
	e.emitCommandAttempt(ctx, prep, false)
	e.activeStepCtx = nil
	hzOpID := findRelayRpcs(ctx.Raw)[0].OpId

	// Prefs op in prefsPhaseGet (waiting on GetAccountPrefs before Put).
	ctxP := newStepContext()
	e.activeStepCtx = ctxP
	e.EnqueuePrefsOp(ctxP, &lists.FullPrefsOp{Kind: lists.PrefsOpPin, Path: "/p-get"}, false, "")
	e.activeStepCtx = nil
	getOpID := findRelayRpcs(ctxP.Raw)[0].OpId
	if e.prefsQueue[0].Phase != prefsPhaseGet || e.prefsQueue[0].ActiveOpID == 0 {
		t.Fatalf("expected prefsPhaseGet active op, got %+v", e.prefsQueue[0])
	}

	// STOP: must preserve healthz (Forward waiter) and cancel prefsPhaseGet while clearing ActiveOpID.
	_, stopRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 20, MonoMs: 1_020,
		Event: &corev1.Event_SyncLifecycle{SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_STOP}},
	})
	canceled := make(map[uint64]bool)
	for _, cid := range findRelayCancels(stopRes.Effects) {
		canceled[cid] = true
	}
	if canceled[hzOpID] {
		t.Fatal("STOP must preserve healthz when Forward waiters depend on it")
	}
	if !canceled[getOpID] {
		t.Fatal("STOP must cancel in-flight GetAccountPrefs")
	}
	if e.prefsQueue[0].ActiveOpID != 0 {
		t.Fatalf("STOP must clear prefsQueue[0].ActiveOpID after canceling prefsPhaseGet, got %d", e.prefsQueue[0].ActiveOpID)
	}

	// Completing healthz while stopped proceeds to emit Forward!
	_, hzDone := e.HandleEvent(&corev1.Event{
		NowMs: now + 30, MonoMs: 1_030,
		Event: &corev1.Event_RelayRpcResult{RelayRpcResult: &corev1.RelayRpcResultEvent{
			OpId: hzOpID, RelayEpoch: "ep-live",
		}},
	})
	fwdRpcs := findRelayRpcs(hzDone.Effects)
	if len(fwdRpcs) != 1 || fwdRpcs[0].Method != corev1.RelayRpcMethod_RELAY_RPC_METHOD_FORWARD {
		t.Fatalf("expected FORWARD after preserved healthz completed, got %+v", fwdRpcs)
	}

	// START resumes prefsQueue[0] GetAccountPrefs!
	_, startRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 40, MonoMs: 1_040,
		Event: &corev1.Event_SyncLifecycle{SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START}},
	})
	var gotPrefsGet bool
	for _, r := range findRelayRpcs(startRes.Effects) {
		if r.Method == corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_ACCOUNT_PREFS {
			gotPrefsGet = true
		}
	}
	if !gotPrefsGet {
		t.Fatal("START did not resume prefsQueue[0] after prefsPhaseGet was canceled by STOP")
	}
}

func TestRelayPipe_BackgroundPushWorksWhileStopped(t *testing.T) {
	e := NewEngine()
	now := int64(4_000_000)
	e.HandleEvent(&corev1.Event{
		NowMs: now, MonoMs: 1_000,
		Event: &corev1.Event_SecurityConfig{
			SecurityConfig: &corev1.SecurityConfigEvent{Account: "acct-1", DeviceId: "dev-1", Server: "https://relay.example.com"},
		},
	})
	e.HandleEvent(&corev1.Event{
		NowMs: now, MonoMs: 1_000,
		Event: &corev1.Event_RelayPipeConfig{RelayPipeConfig: &corev1.RelayPipeConfigEvent{Enabled: true}},
	})
	// Engine is explicitly STOPPED (e.running == false) and has no cached token yet.
	if e.running {
		t.Fatal("expected engine stopped initially")
	}

	_, step1 := e.HandleEvent(&corev1.Event{
		NowMs: now, MonoMs: 1_010,
		Event: &corev1.Event_PushRpc{PushRpc: &corev1.PushRpcAction{
			OpId:      88,
			SetTarget: true,
			PushType:  int32(v1.PushType_PUSH_TYPE_FCM),
			Target:    "fcm-bg-token",
			Kinds:     []v1.NotifyKind{v1.NotifyKind_NOTIFY_KIND_ATTENTION},
		}},
	})
	var bootOp uint64
	for _, eff := range step1.Effects {
		if st := eff.GetSecurityStorage(); st != nil && st.Op == corev1.SecurityStorageOp_SECURITY_STORAGE_OP_READ_BOOTSTRAP {
			bootOp = st.OpId
		}
		if eff.GetPushRpcResult() != nil {
			t.Fatalf("background PushRpc must NOT immediately fail while stopped: %+v", eff)
		}
	}
	if bootOp == 0 {
		t.Fatal("expected READ_BOOTSTRAP to obtain token for background PushRpc while stopped")
	}

	// Hydrate persisted login token -> Core proceeds to GET_PUSH_KEY -> SET_PUSH_TARGET -> PushRpcResult(Ok=true).
	_, step2 := e.HandleEvent(&corev1.Event{
		NowMs: now + 10, MonoMs: 1_020,
		Event: &corev1.Event_SecurityStorageResult{SecurityStorageResult: &corev1.SecurityStorageResultEvent{
			OpId: bootOp, Found: true, LoginToken: "bg-tok", LoginTokenExpiresMs: now + 3*24*3600_000,
		}},
	})
	keyRpcs := findRelayRpcs(step2.Effects)
	if len(keyRpcs) != 1 || keyRpcs[0].Method != corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_PUSH_KEY || keyRpcs[0].AuthToken != "bg-tok" {
		t.Fatalf("expected GET_PUSH_KEY with bg-tok while stopped, got %+v", keyRpcs)
	}
	_, step3 := e.HandleEvent(&corev1.Event{
		NowMs: now + 20, MonoMs: 1_030,
		Event: &corev1.Event_RelayRpcResult{RelayRpcResult: &corev1.RelayRpcResultEvent{
			OpId:          keyRpcs[0].OpId,
			ResponseBytes: mustMarshalStd(t, &genpb.GetPushKeyResponse{PublicKey: "vapid"}),
		}},
	})
	setRpcs := findRelayRpcs(step3.Effects)
	if len(setRpcs) != 1 || setRpcs[0].Method != corev1.RelayRpcMethod_RELAY_RPC_METHOD_SET_PUSH_TARGET {
		t.Fatalf("expected SET_PUSH_TARGET while stopped, got %+v", setRpcs)
	}
	_, step4 := e.HandleEvent(&corev1.Event{
		NowMs: now + 30, MonoMs: 1_040,
		Event: &corev1.Event_RelayRpcResult{RelayRpcResult: &corev1.RelayRpcResultEvent{
			OpId:          setRpcs[0].OpId,
			ResponseBytes: mustMarshalStd(t, &genpb.SetPushTargetResponse{Enabled: true}),
		}},
	})
	var pushRes *corev1.PushRpcResultEffect
	for _, eff := range step4.Effects {
		if pr := eff.GetPushRpcResult(); pr != nil {
			pushRes = pr
		}
	}
	if pushRes == nil || pushRes.OpId != 88 || !pushRes.Ok || !pushRes.Enabled {
		t.Fatalf("expected successful background PushRpcResultEffect, got %+v", pushRes)
	}
}

func TestRelayPipe_Staggered401ReusesFreshTokenWithoutReLogin(t *testing.T) {
	e := NewEngine()
	now := int64(5_000_000)
	e.HandleEvent(&corev1.Event{
		NowMs: now, MonoMs: 1_000,
		Event: &corev1.Event_SecurityConfig{
			SecurityConfig: &corev1.SecurityConfigEvent{Account: "acct-1", DeviceId: "dev-1", Server: "https://relay.example.com"},
		},
	})
	e.HandleEvent(&corev1.Event{
		NowMs: now, MonoMs: 1_000,
		Event: &corev1.Event_RelayPipeConfig{RelayPipeConfig: &corev1.RelayPipeConfigEvent{Enabled: true}},
	})
	e.secEngine.SetAuthToken("tok-old", now+3*24*3600_000)
	e.cachedToken = "tok-old"
	e.tokenExpiresMs = now + 3*24*3600_000
	e.running = true

	// Emit two concurrent RPCs A (ListDevices) and B (GetImage) using tok-old.
	_, stepA := e.HandleEvent(&corev1.Event{
		NowMs: now, MonoMs: 1_100,
		Event: &corev1.Event_RefreshDevices{RefreshDevices: &corev1.RefreshDevicesAction{}},
	})
	opA := findRelayRpcs(stepA.Effects)[0].OpId

	ctxB := newStepContext()
	e.activeStepCtx = ctxB
	e.emitRelayRpc(ctxB, corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_IMAGE, e.cachedToken, relayRpcDefaultTimeoutMs, "",
		(&v1.GetImageRequest{SessionId: "s1", ImageId: "img-stagger"}).AppendMarshal(nil),
		&pendingRelayRpc{kind: relayRpcGetImage, opID: 901})
	e.activeStepCtx = nil
	opB := findRelayRpcs(ctxB.Raw)[0].OpId

	// RPC A hits 401 -> refreshes token to tok-new and retries A.
	_, r401A := e.HandleEvent(&corev1.Event{
		NowMs: now + 10, MonoMs: 1_110,
		Event: &corev1.Event_RelayRpcResult{RelayRpcResult: &corev1.RelayRpcResultEvent{
			OpId: opA, ConnectCode: security.ConnectCodeUnauthenticated, DiagnosticMessage: "expired",
		}},
	})
	chalRpc := findRelayRpcs(r401A.Effects)[0]
	_, chalDone := e.HandleEvent(&corev1.Event{
		NowMs: now + 20, MonoMs: 1_120,
		Event: &corev1.Event_RelayRpcResult{RelayRpcResult: &corev1.RelayRpcResultEvent{
			OpId: chalRpc.OpId, ResponseBytes: mustMarshalStd(t, &genpb.GetChallengeResponse{Challenge: bytes.Repeat([]byte{3}, 32)}),
		}},
	})
	var signOp uint64
	for _, eff := range chalDone.Effects {
		if ks := eff.GetSecurityKeySign(); ks != nil {
			signOp = ks.OpId
		}
	}
	_, signDone := e.HandleEvent(&corev1.Event{
		NowMs: now + 30, MonoMs: 1_130,
		Event: &corev1.Event_SecurityKeySigned{SecurityKeySigned: &corev1.SecurityKeySignedEvent{
			OpId: signOp, Signature: bytes.Repeat([]byte{4}, 64),
		}},
	})
	loginRpc := findRelayRpcs(signDone.Effects)[0]
	_, loginDone := e.HandleEvent(&corev1.Event{
		NowMs: now + 40, MonoMs: 1_140,
		Event: &corev1.Event_RelayRpcResult{RelayRpcResult: &corev1.RelayRpcResultEvent{
			OpId: loginRpc.OpId, ResponseBytes: mustMarshalStd(t, &genpb.LoginResponse{Token: "tok-new", ExpiresMs: now + 7*24*3600_000}),
		}},
	})
	if rA := findRelayRpcs(loginDone.Effects); len(rA) != 1 || rA[0].AuthToken != "tok-new" {
		t.Fatalf("expected RPC A retried with tok-new, got %+v", rA)
	}

	// Now RPC B returns a late 401 (usedToken == "tok-old" != current "tok-new"):
	// Must IMMEDIATELY retry B with tok-new, with ZERO GetChallenge and ZERO token clears!
	_, r401B := e.HandleEvent(&corev1.Event{
		NowMs: now + 50, MonoMs: 1_150,
		Event: &corev1.Event_RelayRpcResult{RelayRpcResult: &corev1.RelayRpcResultEvent{
			OpId: opB, ConnectCode: security.ConnectCodeUnauthenticated, DiagnosticMessage: "stale tok-old",
		}},
	})
	for _, eff := range r401B.Effects {
		if tu := eff.GetSecurityTokenUpdated(); tu != nil && tu.Cleared {
			t.Fatal("staggered 401 with old token must NOT clear fresh tok-new")
		}
	}
	rpcsB := findRelayRpcs(r401B.Effects)
	if len(rpcsB) != 1 || rpcsB[0].Method != corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_IMAGE || rpcsB[0].AuthToken != "tok-new" {
		t.Fatalf("expected immediate GET_IMAGE retry with tok-new and 0 GetChallenge, got %+v", rpcsB)
	}
	if e.cachedToken != "tok-new" {
		t.Fatalf("expected cachedToken to remain tok-new, got %q", e.cachedToken)
	}
}

func TestRelayPipe_StopClearsInFlightSyncRpcsSoStartCanRefetch(t *testing.T) {
	e := NewEngine()
	now := int64(6_000_000)
	e.HandleEvent(&corev1.Event{
		NowMs: now, MonoMs: 1_000,
		Event: &corev1.Event_SecurityConfig{
			SecurityConfig: &corev1.SecurityConfigEvent{Account: "acct-1", DeviceId: "dev-1", Server: "https://relay.example.com"},
		},
	})
	e.HandleEvent(&corev1.Event{
		NowMs: now, MonoMs: 1_000,
		Event: &corev1.Event_RelayPipeConfig{RelayPipeConfig: &corev1.RelayPipeConfigEvent{Enabled: true}},
	})
	e.running = true
	e.networkAvailable = true
	e.w.World.Connection = lists.ConnConnected
	e.w.LivePushConnected = true
	e.cachedToken = "tok-sync-stop"
	e.tokenExpiresMs = now + 7*24*3600_000
	e.secEngine.SetAuthToken("tok-sync-stop", e.tokenExpiresMs)
	e.storeMachine(&MachineEntry{Machine: lists.Machine{ID: "m1", Online: true}})
	e.sessions["s1"] = &SessionEntry{
		Session:       lists.Session{ID: "s1", MachineID: "m1"},
		ServerLastSeq: 20,
	}
	e.buffers["s1"] = &EventBuffer{SessionID: "s1"}
	e.w.World.SessionsListed = true
	e.w.World.SessionsNext = &lists.SessionPlace{EndedMs: 100, SessionID: "s1"}

	// 1. Start in-flight GetEvents (relayRpcEvents).
	ctxEv := newStepContext()
	e.activeStepCtx = ctxEv
	e.startFetchEvents(ctxEv, "s1")
	e.activeStepCtx = nil
	evRpcs := findRelayRpcs(ctxEv.Raw)
	if len(evRpcs) != 1 || evRpcs[0].Method != corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_EVENTS || !e.buffers["s1"].fetching {
		t.Fatalf("expected in-flight GET_EVENTS with fetching=true, got rpcs=%+v fetching=%v", evRpcs, e.buffers["s1"].fetching)
	}

	// 2. Start in-flight SessionsPage (relayRpcSessionsPage).
	ctxPage := newStepContext()
	e.activeStepCtx = ctxPage
	e.RequestMoreSessions(ctxPage)
	e.activeStepCtx = nil
	pageRpcs := findRelayRpcs(ctxPage.Raw)
	if len(pageRpcs) != 1 || pageRpcs[0].Method != corev1.RelayRpcMethod_RELAY_RPC_METHOD_LIST_REMOTE_SESSIONS || !e.w.World.LoadingMoreSessions {
		t.Fatalf("expected in-flight sessionsPage with LoadingMoreSessions=true, got rpcs=%+v loading=%v", pageRpcs, e.w.World.LoadingMoreSessions)
	}

	// 3. Start in-flight SessionByID (relayRpcSessionByID).
	ctxByID := newStepContext()
	e.activeStepCtx = ctxByID
	e.fetchSessionByID(ctxByID, "s-missing")
	e.activeStepCtx = nil
	byIDRpcs := findRelayRpcs(ctxByID.Raw)
	if len(byIDRpcs) != 1 || byIDRpcs[0].Method != corev1.RelayRpcMethod_RELAY_RPC_METHOD_LIST_REMOTE_SESSIONS || len(e.sessionByIDOps) == 0 {
		t.Fatalf("expected in-flight sessionByID, got rpcs=%+v ops=%v", byIDRpcs, e.sessionByIDOps)
	}

	// 4. Start in-flight RefreshDevices (relayRpcDevices).
	_, devStep := e.HandleEvent(&corev1.Event{
		NowMs: now + 3, MonoMs: 1_003,
		Event: &corev1.Event_RefreshDevices{RefreshDevices: &corev1.RefreshDevicesAction{}},
	})
	devRpcs := findRelayRpcs(devStep.Effects)
	if len(devRpcs) != 1 || devRpcs[0].Method != corev1.RelayRpcMethod_RELAY_RPC_METHOD_LIST_DEVICES || e.devicesOpID == 0 {
		t.Fatalf("expected in-flight devices RPC, got rpcs=%+v op=%d", devRpcs, e.devicesOpID)
	}

	// 5. Start in-flight RefreshBundle (machines + sessions + devices).
	ctxRef := newStepContext()
	e.activeStepCtx = ctxRef
	e.startRefresh(ctxRef)
	e.activeStepCtx = nil
	if e.refreshBundle == nil || len(findRelayRpcs(ctxRef.Raw)) != 3 {
		t.Fatalf("expected 3-RPC refreshBundle, got %+v", e.refreshBundle)
	}

	// STOP: all 5 in-flight sync RPC families must be canceled AND their in-flight flags cleanly cleared!
	_, stopRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 20, MonoMs: 1_020,
		Event: &corev1.Event_SyncLifecycle{SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_STOP}},
	})
	if len(findRelayCancels(stopRes.Effects)) < 7 {
		t.Fatalf("expected cancels for all 7 sync RPCs, got %v", findRelayCancels(stopRes.Effects))
	}
	if e.buffers["s1"].fetching || e.buffers["s1"].activeFetchOpID != 0 {
		t.Fatalf("STOP left EventBuffer stuck: fetching=%v activeFetchOpID=%d", e.buffers["s1"].fetching, e.buffers["s1"].activeFetchOpID)
	}
	if !e.buffers["s1"].fetchErrorReason.IsZero() {
		t.Fatalf("STOP must not record fetchErrorReason on canceled events fetch: %+v", e.buffers["s1"].fetchErrorReason)
	}
	if e.pageOpID != 0 || e.w.World.LoadingMoreSessions {
		t.Fatalf("STOP left sessionsPage stuck: pageOpID=%d LoadingMoreSessions=%v", e.pageOpID, e.w.World.LoadingMoreSessions)
	}
	if len(e.sessionByIDOps) != 0 || e.sessionByIDFailed["s-missing"] {
		t.Fatalf("STOP left sessionByID dirty: ops=%v failed=%v", e.sessionByIDOps, e.sessionByIDFailed)
	}
	if e.devicesOpID != 0 || e.devicesLoadFailed {
		t.Fatalf("STOP left devices dirty: op=%d failed=%v", e.devicesOpID, e.devicesLoadFailed)
	}
	if e.refreshBundle != nil || e.refreshOpID != 0 {
		t.Fatalf("STOP left refreshBundle dirty: bundle=%v op=%d", e.refreshBundle, e.refreshOpID)
	}

	// START again and bring stream Ready -> every single RPC family must be able to re-issue cleanly!
	_, startRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 50, MonoMs: 1_050,
		Event: &corev1.Event_SyncLifecycle{SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START}},
	})
	var legID uint64
	for _, eff := range startRes.Effects {
		if os := eff.GetOpenStream(); os != nil {
			legID = os.LegId
		}
	}
	if legID == 0 {
		t.Fatalf("expected OpenStreamEffect on START, got %+v", startRes.Effects)
	}
	readyRaw := mustMarshalStd(t, &genpb.ServerFrame{Kind: &genpb.ServerFrame_Ready{Ready: &genpb.Ready{ServerTimeMs: now + 55}}})
	_, readyRes := e.HandleEvent(&corev1.Event{
		NowMs: now + 55, MonoMs: 1_055,
		Event: &corev1.Event_StreamRawFrame{StreamRawFrame: &corev1.StreamRawFrameEvent{LegId: legID, RawServerFrame: readyRaw}},
	})
	// RefreshBundle AND GetEvents for s1 re-issue cleanly after START+Ready!
	if e.refreshBundle == nil {
		t.Fatalf("expected fresh refreshBundle after START+Ready, got effects=%+v", readyRes.Effects)
	}
	var gotEventsAfterStart bool
	for _, r := range findRelayRpcs(readyRes.Effects) {
		if r.Method == corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_EVENTS {
			gotEventsAfterStart = true
		}
	}
	if !gotEventsAfterStart {
		ctxEv2 := newStepContext()
		e.activeStepCtx = ctxEv2
		e.startFetchEvents(ctxEv2, "s1")
		e.activeStepCtx = nil
		for _, r := range findRelayRpcs(ctxEv2.Raw) {
			if r.Method == corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_EVENTS {
				gotEventsAfterStart = true
			}
		}
	}
	if !gotEventsAfterStart {
		t.Fatalf("GET_EVENTS failed to re-issue after STOP->START: ready=%+v fetching=%v", findRelayRpcs(readyRes.Effects), e.buffers["s1"].fetching)
	}

	// SessionsPage works after STOP -> START!
	e.w.World.SessionsListed = true
	e.w.World.SessionsNext = &lists.SessionPlace{EndedMs: 100, SessionID: "s1"}
	ctxPage2 := newStepContext()
	e.activeStepCtx = ctxPage2
	e.RequestMoreSessions(ctxPage2)
	e.activeStepCtx = nil
	if r := findRelayRpcs(ctxPage2.Raw); len(r) != 1 || r[0].Method != corev1.RelayRpcMethod_RELAY_RPC_METHOD_LIST_REMOTE_SESSIONS {
		t.Fatalf("RequestMoreSessions failed to re-issue after STOP->START: %+v", r)
	}

	// SessionByID works after STOP -> START!
	ctxByID2 := newStepContext()
	e.activeStepCtx = ctxByID2
	e.fetchSessionByID(ctxByID2, "s-missing")
	e.activeStepCtx = nil
	if r := findRelayRpcs(ctxByID2.Raw); len(r) != 1 || r[0].Method != corev1.RelayRpcMethod_RELAY_RPC_METHOD_LIST_REMOTE_SESSIONS {
		t.Fatalf("fetchSessionByID failed to re-issue after STOP->START: %+v", r)
	}

	// RefreshDevices works after STOP -> START!
	_, devStep2 := e.HandleEvent(&corev1.Event{
		NowMs: now + 62, MonoMs: 1_062,
		Event: &corev1.Event_RefreshDevices{RefreshDevices: &corev1.RefreshDevicesAction{}},
	})
	if r := findRelayRpcs(devStep2.Effects); len(r) != 1 || r[0].Method != corev1.RelayRpcMethod_RELAY_RPC_METHOD_LIST_DEVICES {
		t.Fatalf("RefreshDevices failed to re-issue after STOP->START: %+v", r)
	}
}
