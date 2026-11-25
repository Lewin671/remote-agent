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
