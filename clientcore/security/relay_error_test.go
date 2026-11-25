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
