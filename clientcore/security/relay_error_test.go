package security

import (
	"testing"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

func mustMarshalDetail(t *testing.T, d *pb.RelayErrorDetail) []byte {
	t.Helper()
	b, err := proto.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseRelayErrorDetail(t *testing.T) {
	mismatchBytes := mustMarshalDetail(t, &pb.RelayErrorDetail{
		Reason:             pb.RelayErrorReason_RELAY_ERROR_REASON_PROTOCOL_MISMATCH,
		ServerRelayVersion: 17,
	})
	clockAheadBytes := mustMarshalDetail(t, &pb.RelayErrorDetail{
		Reason:       pb.RelayErrorReason_RELAY_ERROR_REASON_CLOCK_AHEAD,
		ServerTimeMs: 1_760_000_000_123,
	})
	unspecifiedBytes := mustMarshalDetail(t, &pb.RelayErrorDetail{
		Reason: pb.RelayErrorReason_RELAY_ERROR_REASON_UNSPECIFIED,
	})

	cases := []struct {
		name           string
		details        []*corev1.RpcErrorDetailBytes
		wantReason     v1.RelayErrorReason
		wantVersion    uint32
		wantServerTime int64
		wantNil        bool
	}{
		{
			name:    "empty",
			wantNil: true,
		},
		{
			name: "protocol_mismatch_connect_type",
			details: []*corev1.RpcErrorDetailBytes{
				{TypeName: RelayErrorDetailTypeName, Value: mismatchBytes},
			},
			wantReason:  v1.RelayErrorReason_RELAY_ERROR_REASON_PROTOCOL_MISMATCH,
			wantVersion: 17,
		},
		{
			name: "clock_ahead_type_googleapis_prefix",
			details: []*corev1.RpcErrorDetailBytes{
				{TypeName: "type.googleapis.com/" + RelayErrorDetailTypeName, Value: clockAheadBytes},
			},
			wantReason:     v1.RelayErrorReason_RELAY_ERROR_REASON_CLOCK_AHEAD,
			wantServerTime: 1_760_000_000_123,
		},
		{
			name: "skips_unrelated_and_corrupt_before_valid",
			details: []*corev1.RpcErrorDetailBytes{
				nil,
				{TypeName: "remoteagent.v1.NotForwarded", Value: []byte{0x0a, 0x01, 'x'}},
				{TypeName: RelayErrorDetailTypeName, Value: []byte{0xff, 0xff, 0xff}},
				{TypeName: RelayErrorDetailTypeName, Value: mismatchBytes},
			},
			wantReason:  v1.RelayErrorReason_RELAY_ERROR_REASON_PROTOCOL_MISMATCH,
			wantVersion: 17,
		},
		{
			name: "unspecified_returns_nil_for_fallback",
			details: []*corev1.RpcErrorDetailBytes{
				{TypeName: RelayErrorDetailTypeName, Value: unspecifiedBytes},
			},
			wantNil: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseRelayErrorDetail(tc.details)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("got %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("got nil, want non-nil detail")
			}
			if got.Reason != tc.wantReason || got.ServerRelayVersion != tc.wantVersion || got.ServerTimeMs != tc.wantServerTime {
				t.Fatalf("got %+v, want reason=%v ver=%d time=%d", got, tc.wantReason, tc.wantVersion, tc.wantServerTime)
			}
		})
	}
}

func TestRpcErrorRelayReasonPrecedenceAndFallback(t *testing.T) {
	detailOf := func(r pb.RelayErrorReason) []*corev1.RpcErrorDetailBytes {
		return []*corev1.RpcErrorDetailBytes{
			{TypeName: RelayErrorDetailTypeName, Value: mustMarshalDetail(t, &pb.RelayErrorDetail{Reason: r})},
		}
	}

	// Verify HandleRpcResult parses detail bytes into rpcError and hands them to the waiting task.
	e := NewEngine(nil, 17)
	var captured *rpcError
	capture := &captureRPCTask{onStep: func(ev *secEvent) { captured = ev.rpcErr }}
	e.waitRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_REGISTER_DEVICE, "https://example.com", "", nil, capture, 0)
	effs := e.DrainEffects()
	if len(effs) != 1 || effs[0].GetSecurityRpc() == nil {
		t.Fatalf("expected 1 SecurityRpcEffect, got %d", len(effs))
	}
	opID := effs[0].GetSecurityRpc().OpId
	e.HandleRpcResult(1000, &corev1.SecurityRpcResultEvent{
		OpId:         opID,
		ConnectCode:  connectCodeFailedPrecondition,
		Error:        "relay protocol version mismatch",
		ErrorDetails: detailOf(pb.RelayErrorReason_RELAY_ERROR_REASON_PROTOCOL_MISMATCH),
	})
	if captured == nil || captured.relayReason() != v1.RelayErrorReason_RELAY_ERROR_REASON_PROTOCOL_MISMATCH {
		t.Fatalf("captured rpcErr = %+v, want PROTOCOL_MISMATCH", captured)
	}
	if captured.isSignupPowRequired() {
		t.Fatal("PROTOCOL_MISMATCH with FailedPrecondition must not trigger PoW retry")
	}

	// Explicit SIGNUP_POW_REQUIRED triggers PoW, and UNSPECIFIED falls back to Connect code.
	powExplicit := &rpcError{
		code:   connectCodeFailedPrecondition,
		detail: ParseRelayErrorDetail(detailOf(pb.RelayErrorReason_RELAY_ERROR_REASON_SIGNUP_POW_REQUIRED)),
	}
	if !powExplicit.isSignupPowRequired() {
		t.Fatal("SIGNUP_POW_REQUIRED must trigger PoW")
	}
	powFallback := &rpcError{code: connectCodeFailedPrecondition}
	if !powFallback.isSignupPowRequired() {
		t.Fatal("UNSPECIFIED FailedPrecondition must fall back to PoW")
	}

	// Token refresh: explicit TOKEN_INVALID vs UNSPECIFIED Unauthenticated vs unrelated reason.
	if !(&rpcError{code: connectCodeUnauthenticated, detail: ParseRelayErrorDetail(detailOf(pb.RelayErrorReason_RELAY_ERROR_REASON_TOKEN_INVALID))}).isTokenInvalid() {
		t.Fatal("TOKEN_INVALID must trigger token refresh")
	}
	if !(&rpcError{code: connectCodeUnauthenticated}).isTokenInvalid() {
		t.Fatal("UNSPECIFIED Unauthenticated must fall back to token refresh")
	}
	if (&rpcError{code: connectCodeUnauthenticated, detail: ParseRelayErrorDetail(detailOf(pb.RelayErrorReason_RELAY_ERROR_REASON_ACCOUNT_SUSPENDED))}).isTokenInvalid() {
		t.Fatal("ACCOUNT_SUSPENDED must not be treated as TOKEN_INVALID")
	}

	// Create-account initial login: LOGIN_FAILED proceeds to RegisterDevice; ACCOUNT_SUSPENDED does not.
	if !(&rpcError{code: connectCodePermissionDenied, detail: ParseRelayErrorDetail(detailOf(pb.RelayErrorReason_RELAY_ERROR_REASON_LOGIN_FAILED))}).isCreateInitLoginMissing() {
		t.Fatal("LOGIN_FAILED must proceed to RegisterDevice")
	}
	if (&rpcError{code: connectCodePermissionDenied, detail: ParseRelayErrorDetail(detailOf(pb.RelayErrorReason_RELAY_ERROR_REASON_ACCOUNT_SUSPENDED))}).isCreateInitLoginMissing() {
		t.Fatal("ACCOUNT_SUSPENDED must not fall through to RegisterDevice")
	}
	if !(&rpcError{code: connectCodePermissionDenied}).isCreateInitLoginMissing() {
		t.Fatal("UNSPECIFIED PermissionDenied must fall back to existing behavior")
	}
}

type captureRPCTask struct {
	onStep func(*secEvent)
}

func (c *captureRPCTask) step(_ *Engine, ev *secEvent) error {
	c.onStep(ev)
	return nil
}

func TestUnknownRelayErrorReasonFallsBackToConnectCode(t *testing.T) {
	// Every generated non-zero RelayErrorReason must be recognized as known.
	for num, name := range pb.RelayErrorReason_name {
		r := v1.RelayErrorReason(num)
		want := num != 0
		if got := isKnownRelayErrorReason(r); got != want {
			t.Fatalf("isKnownRelayErrorReason(%s=%d) = %v, want %v", name, num, got, want)
		}
	}
	for _, unknown := range []v1.RelayErrorReason{99, -1} {
		if isKnownRelayErrorReason(unknown) {
			t.Fatalf("isKnownRelayErrorReason(%d) = true, want false", unknown)
		}
	}

	unknownDetail := ParseRelayErrorDetail([]*corev1.RpcErrorDetailBytes{
		{
			TypeName: RelayErrorDetailTypeName,
			Value:    mustMarshalDetail(t, &pb.RelayErrorDetail{Reason: pb.RelayErrorReason(99)}),
		},
	})
	if unknownDetail == nil || unknownDetail.Reason != v1.RelayErrorReason(99) {
		t.Fatalf("ParseRelayErrorDetail(99) = %+v, want raw reason 99 preserved", unknownDetail)
	}

	codes := []int32{
		connectCodeCanceled,
		connectCodeInvalidArgument,
		connectCodeNotFound,
		connectCodeAlreadyExists,
		connectCodePermissionDenied,
		connectCodeResourceExhausted,
		connectCodeFailedPrecondition,
		connectCodeUnavailable,
		connectCodeUnauthenticated,
	}
	predicates := []struct {
		name string
		fn   func(*rpcError) bool
	}{
		{"isTokenInvalid", (*rpcError).isTokenInvalid},
		{"isSignupPowRequired", (*rpcError).isSignupPowRequired},
		{"isCreateInitLoginMissing", (*rpcError).isCreateInitLoginMissing},
		{"isRetainedLoginDenied", (*rpcError).isRetainedLoginDenied},
		{"isPairingGone", (*rpcError).isPairingGone},
		{"isJoinPairingGone", (*rpcError).isJoinPairingGone},
		{"isDeliveryFallback", (*rpcError).isDeliveryFallback},
		{"isResumeRegFallbackLogin", (*rpcError).isResumeRegFallbackLogin},
	}

	for _, code := range codes {
		noDetail := &rpcError{code: code}
		withUnknown := &rpcError{code: code, detail: unknownDetail}
		if withUnknown.relayReason() != v1.RelayErrorReason_RELAY_ERROR_REASON_UNSPECIFIED {
			t.Fatalf("code=%d: relayReason() = %v, want UNSPECIFIED for unknown reason 99", code, withUnknown.relayReason())
		}
		for _, p := range predicates {
			want := p.fn(noDetail)
			got := p.fn(withUnknown)
			if got != want {
				t.Errorf("code=%d predicate=%s: unknown reason 99 gave %v, want %v (same as no detail)", code, p.name, got, want)
			}
		}
	}
}

func findRPCEffect(effs []*corev1.Effect, method corev1.SecurityRpcMethod) *corev1.SecurityRpcEffect {
	for _, eff := range effs {
		if r := eff.GetSecurityRpc(); r != nil && r.Method == method {
			return r
		}
	}
	return nil
}

func findWorkflowResult(effs []*corev1.Effect, actionID uint64) *corev1.SecurityWorkflowResultEffect {
	for _, eff := range effs {
		if r := eff.GetSecurityWorkflowResult(); r != nil && r.ActionId == actionID {
			return r
		}
	}
	return nil
}

func hasSecurityPollTimer(effs []*corev1.Effect) bool {
	for _, eff := range effs {
		if tm := eff.GetScheduleTimer(); tm != nil && tm.Kind == corev1.TimerKind_TIMER_KIND_SECURITY_POLL {
			return true
		}
	}
	return false
}

func TestRelayErrorWorkflowDecisions(t *testing.T) {
	makeDetails := func(reason pb.RelayErrorReason) []*corev1.RpcErrorDetailBytes {
		if reason == pb.RelayErrorReason_RELAY_ERROR_REASON_UNSPECIFIED {
			return nil
		}
		return []*corev1.RpcErrorDetailBytes{
			{TypeName: RelayErrorDetailTypeName, Value: mustMarshalDetail(t, &pb.RelayErrorDetail{Reason: reason})},
		}
	}

	// Drive forceLoginToken's GET_CHALLENGE -> SecurityKeySign -> LOGIN sequence
	// and fail the LOGIN RPC with the specified connect code and RelayErrorReason.
	failLoginRPC := func(t *testing.T, e *Engine, effs []*corev1.Effect, code int32, reason pb.RelayErrorReason) []*corev1.Effect {
		t.Helper()
		chalEff := findRPCEffect(effs, corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_CHALLENGE)
		if chalEff == nil {
			t.Fatalf("expected GET_CHALLENGE effect, got %d effects", len(effs))
		}
		chalBytes, err := proto.Marshal(&pb.GetChallengeResponse{Challenge: make([]byte, 32)})
		if err != nil {
			t.Fatal(err)
		}
		effs = e.HandleRpcResult(1000, &corev1.SecurityRpcResultEvent{OpId: chalEff.OpId, ResponseBytes: chalBytes})
		var signEff *corev1.SecurityKeySignEffect
		for _, eff := range effs {
			if s := eff.GetSecurityKeySign(); s != nil {
				signEff = s
				break
			}
		}
		if signEff == nil {
			t.Fatalf("expected SecurityKeySignEffect after GET_CHALLENGE")
		}
		effs = e.HandleKeySigned(1000, &corev1.SecurityKeySignedEvent{OpId: signEff.OpId, Signature: make([]byte, 64)})
		loginEff := findRPCEffect(effs, corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_LOGIN)
		if loginEff == nil {
			t.Fatalf("expected LOGIN effect after signing challenge")
		}
		return e.HandleRpcResult(1000, &corev1.SecurityRpcResultEvent{
			OpId:         loginEff.OpId,
			ConnectCode:  code,
			Error:        "login error",
			ErrorDetails: makeDetails(reason),
		})
	}

	t.Run("continueCreationTask_RegisterDevice_PoW", func(t *testing.T) {
		cases := []struct {
			name        string
			reason      pb.RelayErrorReason
			wantPowCall bool
		}{
			{"protocol_mismatch_blocks_pow", pb.RelayErrorReason_RELAY_ERROR_REASON_PROTOCOL_MISMATCH, false},
			{"signup_pow_required_triggers_pow", pb.RelayErrorReason_RELAY_ERROR_REASON_SIGNUP_POW_REQUIRED, true},
			{"no_detail_legacy_falls_back_to_pow", pb.RelayErrorReason_RELAY_ERROR_REASON_UNSPECIFIED, true},
			{"unknown_reason_falls_back_to_pow", pb.RelayErrorReason(99), true},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				e := NewEngine(nil, 17)
				e.SetForeground(true)
				const actionID uint64 = 101
				op := &PersistedOperation{Server: "https://example.com", MemberID: "dev-new"}
				task := &continueCreationTask{
					taskBase: taskBase{fgGen: e.ensureWF().fgGen, target: actionTarget(actionID, targetAction), stage: contCreateRegRPC},
					op:       op,
				}
				e.waitRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_REGISTER_DEVICE, op.Server, "", nil, task, 0)
				regEff := findRPCEffect(e.DrainEffects(), corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_REGISTER_DEVICE)
				if regEff == nil {
					t.Fatal("missing REGISTER_DEVICE effect")
				}
				out := e.HandleRpcResult(1000, &corev1.SecurityRpcResultEvent{
					OpId:         regEff.OpId,
					ConnectCode:  connectCodeFailedPrecondition,
					Error:        "failed precondition",
					ErrorDetails: makeDetails(tc.reason),
				})
				gotPow := findRPCEffect(out, corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_SIGNUP_CHALLENGE) != nil
				if gotPow != tc.wantPowCall {
					t.Fatalf("GET_SIGNUP_CHALLENGE emitted = %v, want %v", gotPow, tc.wantPowCall)
				}
				res := findWorkflowResult(out, actionID)
				if tc.wantPowCall && res != nil {
					t.Fatalf("expected workflow to continue to PoW, got terminal result %+v", res)
				}
				if !tc.wantPowCall && (res == nil || res.Ok) {
					t.Fatalf("expected immediate failed result, got %+v", res)
				}
			})
		}
	})

	t.Run("continueCreationTask_InitLogin", func(t *testing.T) {
		cases := []struct {
			name         string
			reason       pb.RelayErrorReason
			wantRegister bool
		}{
			{"login_failed_proceeds_to_register", pb.RelayErrorReason_RELAY_ERROR_REASON_LOGIN_FAILED, true},
			{"account_suspended_stops_creation", pb.RelayErrorReason_RELAY_ERROR_REASON_ACCOUNT_SUSPENDED, false},
			{"no_detail_falls_back_to_register", pb.RelayErrorReason_RELAY_ERROR_REASON_UNSPECIFIED, true},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				e := NewEngine(nil, 17)
				e.SetForeground(true)
				const actionID uint64 = 102
				wf := e.ensureWF()
				signedReq := (&v1.RegisterDeviceRequest{ProtocolVersion: 17}).AppendMarshal(nil)
				wf.pendingOp = &PersistedOperation{
					ID:       "op-1",
					Revision: 1,
					Phase:    "PREPARED",
					Kind:     "create",
					Server:   "https://example.com",
					MemberID: "dev-new",
					Signed:   signedReq,
					Create:   &PersistedCreateDraft{States: [][]byte{{1}}, EdSlot: "ed-1"},
				}
				task := &continueCreationTask{}
				e.stepTask(task, &secEvent{kind: evStart, fgGen: wf.fgGen, target: actionTarget(actionID, targetAction)})
				out := failLoginRPC(t, e, e.DrainEffects(), connectCodePermissionDenied, tc.reason)
				var savedRetry bool
				for _, eff := range out {
					if st := eff.GetSecurityStorage(); st != nil && st.Op == corev1.SecurityStorageOp_SECURITY_STORAGE_OP_WRITE_CAS {
						savedRetry = true
					}
				}
				if savedRetry != tc.wantRegister {
					t.Fatalf("proceeded to register retry write = %v, want %v", savedRetry, tc.wantRegister)
				}
				res := findWorkflowResult(out, actionID)
				if !tc.wantRegister && (res == nil || res.Ok) {
					t.Fatalf("expected immediate failure without register, got %+v", res)
				}
			})
		}
	})

	t.Run("authedRPCTask_TokenRefresh", func(t *testing.T) {
		cases := []struct {
			name        string
			reason      pb.RelayErrorReason
			wantRefresh bool
		}{
			{"token_invalid_refreshes_token", pb.RelayErrorReason_RELAY_ERROR_REASON_TOKEN_INVALID, true},
			{"account_suspended_does_not_refresh", pb.RelayErrorReason_RELAY_ERROR_REASON_ACCOUNT_SUSPENDED, false},
			{"no_detail_unauthenticated_refreshes", pb.RelayErrorReason_RELAY_ERROR_REASON_UNSPECIFIED, true},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				e := NewEngine(nil, 17)
				e.selfDeviceID = "dev-1"
				wf := e.ensureWF()
				wf.cachedToken = "cached-tok"
				wf.tokenExpiresMs = 1_000_000_000_000
				const actionID uint64 = 103
				e.callAuthedRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_LIST_DEVICES, "https://example.com", "", false, 0, nil, actionTarget(actionID, targetAction))
				listEff := findRPCEffect(e.DrainEffects(), corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_LIST_DEVICES)
				if listEff == nil {
					t.Fatal("missing LIST_DEVICES effect")
				}
				out := e.HandleRpcResult(1000, &corev1.SecurityRpcResultEvent{
					OpId:         listEff.OpId,
					ConnectCode:  connectCodeUnauthenticated,
					Error:        "unauthenticated",
					ErrorDetails: makeDetails(tc.reason),
				})
				gotRefresh := findRPCEffect(out, corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_CHALLENGE) != nil
				if gotRefresh != tc.wantRefresh {
					t.Fatalf("token refresh GET_CHALLENGE = %v, want %v", gotRefresh, tc.wantRefresh)
				}
				if tc.wantRefresh && wf.cachedToken != "" {
					t.Fatalf("expected cachedToken cleared on refresh, got %q", wf.cachedToken)
				}
			})
		}
	})

	t.Run("checkRetainedDeniedTask", func(t *testing.T) {
		cases := []struct {
			name       string
			reason     pb.RelayErrorReason
			wantDenied bool
		}{
			{"login_failed_reports_denied", pb.RelayErrorReason_RELAY_ERROR_REASON_LOGIN_FAILED, true},
			{"not_authority_does_not_report_denied", pb.RelayErrorReason_RELAY_ERROR_REASON_NOT_AUTHORITY, false},
			{"no_detail_permission_denied_reports_denied", pb.RelayErrorReason_RELAY_ERROR_REASON_UNSPECIFIED, true},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				e := NewEngine(nil, 17)
				e.selfDeviceID = "dev-retained"
				e.ensureWF().server = "https://example.com"
				const actionID uint64 = 104
				e.checkRetainedDeniedOp(actionID)
				out := failLoginRPC(t, e, e.DrainEffects(), connectCodePermissionDenied, tc.reason)
				if tc.wantDenied {
					// First denial triggers one confirmation retry before reporting Flag=true.
					out = failLoginRPC(t, e, out, connectCodePermissionDenied, tc.reason)
				}
				res := findWorkflowResult(out, actionID)
				if res == nil || !res.Ok || res.Flag != tc.wantDenied {
					t.Fatalf("result = %+v, want Ok=true Flag=%v", res, tc.wantDenied)
				}
			})
		}
	})

	t.Run("pollInitiatorPairing_PairingGone", func(t *testing.T) {
		cases := []struct {
			name     string
			reason   pb.RelayErrorReason
			wantGone bool
		}{
			{"pairing_invalid_or_expired_drops_session", pb.RelayErrorReason_RELAY_ERROR_REASON_PAIRING_INVALID_OR_EXPIRED, true},
			{"recovery_invalid_keeps_session", pb.RelayErrorReason_RELAY_ERROR_REASON_RECOVERY_INVALID, false},
			{"no_detail_not_found_drops_session", pb.RelayErrorReason_RELAY_ERROR_REASON_UNSPECIFIED, true},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				e := NewEngine(nil, 17)
				e.SetForeground(true)
				e.loaded = true
				e.hasHead = true
				e.isMember = true
				e.selfDeviceID = "auth-1"
				e.head.Core.AuthorityID = "auth-1"
				wf := e.ensureWF()
				wf.cachedToken = "tok"
				wf.tokenExpiresMs = 1_000_000_000_000
				wf.initPairings["p1"] = &initiatorPairingSession{pairingID: "p1", server: "https://example.com"}
				const actionID uint64 = 105
				e.startPollPairingInitiator(wf.fgGen, "p1", actionTarget(actionID, targetAction))
				effs := e.DrainEffects()
				getEff := findRPCEffect(effs, corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_PAIRING)
				if getEff == nil {
					t.Fatal("missing GET_PAIRING effect")
				}
				out := e.HandleRpcResult(1000, &corev1.SecurityRpcResultEvent{
					OpId:         getEff.OpId,
					ConnectCode:  connectCodeNotFound,
					Error:        "not found",
					ErrorDetails: makeDetails(tc.reason),
				})
				_, stillPresent := wf.initPairings["p1"]
				if stillPresent == tc.wantGone {
					t.Fatalf("initPairings[p1] present = %v, wantGone = %v", stillPresent, tc.wantGone)
				}
				meta := e.workflowMeta[actionID]
				if tc.wantGone && meta.ErrReason != WorkflowReasonPairingExpiredOrCanceled {
					t.Fatalf("meta.ErrReason = %v, want WorkflowReasonPairingExpiredOrCanceled", meta.ErrReason)
				}
				if !tc.wantGone && meta.ErrReason == WorkflowReasonPairingExpiredOrCanceled {
					t.Fatalf("unexpected WorkflowReasonPairingExpiredOrCanceled for unrelated reason %v", tc.reason)
				}
				if findWorkflowResult(out, actionID) == nil {
					t.Fatal("expected workflow result")
				}
			})
		}
	})

	t.Run("pollJoinTask_JoinPairingGone", func(t *testing.T) {
		cases := []struct {
			name     string
			code     int32
			reason   pb.RelayErrorReason
			wantGone bool
		}{
			{"pairing_invalid_or_expired_cancels", connectCodeNotFound, pb.RelayErrorReason_RELAY_ERROR_REASON_PAIRING_INVALID_OR_EXPIRED, true},
			{"rate_limited_schedules_retry", connectCodePermissionDenied, pb.RelayErrorReason_RELAY_ERROR_REASON_RATE_LIMITED, false},
			{"no_detail_not_found_cancels", connectCodeNotFound, pb.RelayErrorReason_RELAY_ERROR_REASON_UNSPECIFIED, true},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				e := NewEngine(nil, 17)
				const actionID uint64 = 106
				wf := e.ensureWF()
				wf.activePoll = &activePoll{kind: PollJoinRevealed, pairingID: "p2", actionID: actionID}
				sess := &joinerPairingSession{pairingID: "p2", server: "https://example.com"}
				task := &pollJoinTask{
					taskBase:  taskBase{target: actionTarget(actionID, targetAction), stage: pollJoinGet},
					pairingID: "p2",
					sess:      sess,
					want:      v1.PairingState_PAIRING_STATE_REVEALED,
				}
				e.waitRPC(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_PAIRING, sess.server, "", nil, task, 0)
				getEff := findRPCEffect(e.DrainEffects(), corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_PAIRING)
				if getEff == nil {
					t.Fatal("missing GET_PAIRING effect")
				}
				out := e.HandleRpcResult(1000, &corev1.SecurityRpcResultEvent{
					OpId:         getEff.OpId,
					ConnectCode:  tc.code,
					Error:        "rpc error",
					ErrorDetails: makeDetails(tc.reason),
				})
				gotTimer := hasSecurityPollTimer(out)
				if gotTimer == tc.wantGone {
					t.Fatalf("poll timer scheduled = %v, wantGone = %v", gotTimer, tc.wantGone)
				}
				res := findWorkflowResult(out, actionID)
				if tc.wantGone && (res == nil || res.Ok) {
					t.Fatalf("expected terminal canceled error, got %+v", res)
				}
				if !tc.wantGone && res != nil {
					t.Fatalf("expected retry timer instead of terminal result %+v", res)
				}
			})
		}
	})

	t.Run("resumeRegistrationTask_FallbackLogin", func(t *testing.T) {
		cases := []struct {
			name              string
			reason            pb.RelayErrorReason
			wantFallbackLogin bool
		}{
			{"no_detail_permission_denied_probes_login", pb.RelayErrorReason_RELAY_ERROR_REASON_UNSPECIFIED, true},
			{"account_suspended_fails_without_login_probe", pb.RelayErrorReason_RELAY_ERROR_REASON_ACCOUNT_SUSPENDED, false},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				e := NewEngine(nil, 17)
				const actionID uint64 = 107
				e.ensureWF().regIntent = &PersistedRegistrationIntent{
					Server:   "https://example.com",
					DeviceID: "dev-join",
					EdSlot:   "cand-ed",
					Record:   &PersistedRecord{},
				}
				e.startResumeRegistration(0, actionTarget(actionID, targetAction))
				regEff := findRPCEffect(e.DrainEffects(), corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_REGISTER_DEVICE)
				if regEff == nil {
					t.Fatal("missing REGISTER_DEVICE effect")
				}
				out := e.HandleRpcResult(1000, &corev1.SecurityRpcResultEvent{
					OpId:         regEff.OpId,
					ConnectCode:  connectCodePermissionDenied,
					Error:        "permission denied",
					ErrorDetails: makeDetails(tc.reason),
				})
				gotProbe := findRPCEffect(out, corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_CHALLENGE) != nil
				if gotProbe != tc.wantFallbackLogin {
					t.Fatalf("fallback login GET_CHALLENGE = %v, want %v", gotProbe, tc.wantFallbackLogin)
				}
				res := findWorkflowResult(out, actionID)
				if !tc.wantFallbackLogin && (res == nil || res.Ok) {
					t.Fatalf("expected immediate failure, got %+v", res)
				}
			})
		}
	})

	t.Run("continueStateOpTask_DeliveryFallback", func(t *testing.T) {
		cases := []struct {
			name             string
			code             int32
			reason           pb.RelayErrorReason
			wantListFallback bool
		}{
			{"pairing_invalid_or_expired_falls_back_to_list_devices", connectCodeNotFound, pb.RelayErrorReason_RELAY_ERROR_REASON_PAIRING_INVALID_OR_EXPIRED, true},
			{"not_authority_fails_without_list_devices", connectCodeFailedPrecondition, pb.RelayErrorReason_RELAY_ERROR_REASON_NOT_AUTHORITY, false},
			{"no_detail_not_found_falls_back_to_list_devices", connectCodeNotFound, pb.RelayErrorReason_RELAY_ERROR_REASON_UNSPECIFIED, true},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				e := NewEngine(nil, 17)
				e.SetForeground(true)
				wf := e.ensureWF()
				wf.cachedToken = "tok"
				wf.tokenExpiresMs = 1_000_000_000_000
				const actionID uint64 = 108
				op := &PersistedOperation{Server: "https://example.com"}
				task := &continueStateOpTask{
					taskBase: taskBase{fgGen: wf.fgGen, target: actionTarget(actionID, targetAction), stage: contStateApprovePair},
					op:       op,
				}
				e.callAuthedRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_APPROVE_PAIRING, op.Server, "", true, wf.fgGen, nil, parentTarget(task, 0))
				approveEff := findRPCEffect(e.DrainEffects(), corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_APPROVE_PAIRING)
				if approveEff == nil {
					t.Fatal("missing APPROVE_PAIRING effect")
				}
				out := e.HandleRpcResult(1000, &corev1.SecurityRpcResultEvent{
					OpId:         approveEff.OpId,
					ConnectCode:  tc.code,
					Error:        "approve error",
					ErrorDetails: makeDetails(tc.reason),
				})
				gotList := findRPCEffect(out, corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_LIST_DEVICES) != nil
				if gotList != tc.wantListFallback {
					t.Fatalf("fallback LIST_DEVICES = %v, want %v", gotList, tc.wantListFallback)
				}
				res := findWorkflowResult(out, actionID)
				if !tc.wantListFallback && (res == nil || res.Ok) {
					t.Fatalf("expected immediate failure, got %+v", res)
				}
			})
		}
	})
}
