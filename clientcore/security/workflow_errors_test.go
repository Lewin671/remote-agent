package security

import (
	"testing"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/shared/devicesec"
	"google.golang.org/protobuf/proto"
)

func TestClassifyWorkflowErrorRelayReasonsAndFallback(t *testing.T) {
	detail := func(r pb.RelayErrorReason) *v1.RelayErrorDetail {
		b, err := proto.Marshal(&pb.RelayErrorDetail{Reason: r})
		if err != nil {
			t.Fatal(err)
		}
		return ParseRelayErrorDetail([]*corev1.RpcErrorDetailBytes{{
			TypeName: RelayErrorDetailTypeName,
			Value:    b,
		}})
	}

	cases := []struct {
		name          string
		err           error
		wantReason    WorkflowReason
		wantCode      corev1.SecurityErrorCode
		wantRetryable bool
		wantUncertain bool
		wantAction    string
	}{
		{
			name:          "protocol_mismatch_overrides_failed_precondition",
			err:           &rpcError{code: connectCodeFailedPrecondition, msg: "arbitrary text", detail: detail(pb.RelayErrorReason_RELAY_ERROR_REASON_PROTOCOL_MISMATCH)},
			wantReason:    WorkflowReasonProtocolMismatch,
			wantCode:      corev1.SecurityErrorCode_SECURITY_ERROR_CODE_FAILED_PRECONDITION,
			wantRetryable: false,
			wantAction:    "refill",
		},
		{
			name:          "pairing_invalid_or_expired",
			err:           &rpcError{code: connectCodeNotFound, msg: "gone", detail: detail(pb.RelayErrorReason_RELAY_ERROR_REASON_PAIRING_INVALID_OR_EXPIRED)},
			wantReason:    WorkflowReasonPairingCodeInvalidOrUsed,
			wantCode:      corev1.SecurityErrorCode_SECURITY_ERROR_CODE_NOT_FOUND,
			wantRetryable: false,
			wantAction:    "refill",
		},
		{
			name:          "pairing_already_joined",
			err:           &rpcError{code: connectCodeFailedPrecondition, msg: "used", detail: detail(pb.RelayErrorReason_RELAY_ERROR_REASON_PAIRING_ALREADY_JOINED)},
			wantReason:    WorkflowReasonPairingCodeInvalidOrUsed,
			wantCode:      corev1.SecurityErrorCode_SECURITY_ERROR_CODE_NOT_FOUND,
			wantRetryable: false,
			wantAction:    "refill",
		},
		{
			name:          "rate_limited",
			err:           &rpcError{code: connectCodeResourceExhausted, msg: "slow down", detail: detail(pb.RelayErrorReason_RELAY_ERROR_REASON_RATE_LIMITED)},
			wantReason:    WorkflowReasonRateLimited,
			wantCode:      corev1.SecurityErrorCode_SECURITY_ERROR_CODE_RESOURCE_EXHAUSTED,
			wantRetryable: true,
			wantAction:    "refill",
		},
		{
			name:          "clock_ahead",
			err:           &rpcError{code: connectCodeFailedPrecondition, msg: "clock", detail: detail(pb.RelayErrorReason_RELAY_ERROR_REASON_CLOCK_AHEAD)},
			wantReason:    WorkflowReasonClockBehind,
			wantCode:      corev1.SecurityErrorCode_SECURITY_ERROR_CODE_CLOCK_SKEWED,
			wantRetryable: true,
			wantAction:    "retry",
		},
		{
			name:          "security_chain_full",
			err:           &rpcError{code: connectCodeResourceExhausted, msg: "full", detail: detail(pb.RelayErrorReason_RELAY_ERROR_REASON_SECURITY_CHAIN_FULL)},
			wantReason:    WorkflowReasonSecurityChainFull,
			wantCode:      corev1.SecurityErrorCode_SECURITY_ERROR_CODE_CAPACITY,
			wantRetryable: false,
			wantAction:    "",
		},
		{
			name:          "setup_token_required",
			err:           &rpcError{code: connectCodeFailedPrecondition, msg: "need token", detail: detail(pb.RelayErrorReason_RELAY_ERROR_REASON_SETUP_TOKEN_REQUIRED)},
			wantReason:    WorkflowReasonSetupTokenRequired,
			wantCode:      corev1.SecurityErrorCode_SECURITY_ERROR_CODE_FAILED_PRECONDITION,
			wantRetryable: false,
			wantAction:    "refill",
		},
		{
			name:          "login_failed",
			err:           &rpcError{code: connectCodePermissionDenied, msg: "bad sig", detail: detail(pb.RelayErrorReason_RELAY_ERROR_REASON_LOGIN_FAILED)},
			wantReason:    WorkflowReasonPermissionDenied,
			wantCode:      corev1.SecurityErrorCode_SECURITY_ERROR_CODE_PERMISSION_DENIED,
			wantRetryable: false,
			wantAction:    "restart",
		},
		{
			name:          "recovery_not_ready",
			err:           &rpcError{code: connectCodeFailedPrecondition, msg: "wait", detail: detail(pb.RelayErrorReason_RELAY_ERROR_REASON_RECOVERY_NOT_READY)},
			wantReason:    WorkflowReasonRecoveryNotReady,
			wantCode:      corev1.SecurityErrorCode_SECURITY_ERROR_CODE_FAILED_PRECONDITION,
			wantRetryable: true,
			wantAction:    "retry",
		},
		{
			name:          "recovery_rejected",
			err:           &rpcError{code: connectCodeFailedPrecondition, msg: "vetoed", detail: detail(pb.RelayErrorReason_RELAY_ERROR_REASON_RECOVERY_REJECTED)},
			wantReason:    WorkflowReasonRecoveryRejected,
			wantCode:      corev1.SecurityErrorCode_SECURITY_ERROR_CODE_FAILED_PRECONDITION,
			wantRetryable: false,
			wantAction:    "restart",
		},
		{
			name:          "unspecified_network_error_fallback",
			err:           &rpcError{network: true, msg: "connection reset"},
			wantReason:    WorkflowReasonSuccessUncertain,
			wantCode:      corev1.SecurityErrorCode_SECURITY_ERROR_CODE_UNAVAILABLE,
			wantRetryable: true,
			wantUncertain: true,
			wantAction:    "retry",
		},
		{
			name:          "unspecified_not_found_fallback",
			err:           &rpcError{code: connectCodeNotFound, msg: "missing"},
			wantReason:    WorkflowReasonNotFound,
			wantCode:      corev1.SecurityErrorCode_SECURITY_ERROR_CODE_NOT_FOUND,
			wantRetryable: false,
			wantAction:    "refill",
		},
		{
			name:          "devicesec_clock_error",
			err:           devicesec.ErrClock,
			wantReason:    WorkflowReasonClockBehind,
			wantCode:      corev1.SecurityErrorCode_SECURITY_ERROR_CODE_CLOCK_SKEWED,
			wantRetryable: true,
			wantAction:    "retry",
		},
		{
			name:          "devicesec_capacity_error",
			err:           devicesec.ErrCapacity,
			wantReason:    WorkflowReasonSecurityChainFull,
			wantCode:      corev1.SecurityErrorCode_SECURITY_ERROR_CODE_CAPACITY,
			wantRetryable: false,
			wantAction:    "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta, code, retryable, uncertain, action := classifyWorkflowError(tc.err, WorkflowMeta{})
			if meta.ErrReason != tc.wantReason {
				t.Errorf("ErrReason = %v, want %v", meta.ErrReason, tc.wantReason)
			}
			if code != tc.wantCode {
				t.Errorf("code = %v, want %v", code, tc.wantCode)
			}
			if retryable != tc.wantRetryable {
				t.Errorf("retryable = %v, want %v", retryable, tc.wantRetryable)
			}
			if uncertain != tc.wantUncertain {
				t.Errorf("uncertain = %v, want %v", uncertain, tc.wantUncertain)
			}
			if action != tc.wantAction {
				t.Errorf("action = %q, want %q", action, tc.wantAction)
			}
		})
	}
}
