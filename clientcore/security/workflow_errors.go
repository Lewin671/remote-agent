package security

import (
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

// classifyWorkflowError classifies any workflow, devicesec, or Connect RPC error
// using structured RelayErrorDetail first and Connect status code fallback only
// when RelayErrorDetail is absent or UNSPECIFIED. Never inspects English text.
func classifyWorkflowError(err error, meta WorkflowMeta) (WorkflowMeta, corev1.SecurityErrorCode, bool, bool, string) {
	if err == nil {
		return meta, corev1.SecurityErrorCode_SECURITY_ERROR_CODE_UNSPECIFIED, false, false, ""
	}
	meta.ErrRaw = err.Error()
	if we := asWfError(err); we != nil {
		meta.ErrReason = we.reason
		return meta, StructuredErrorCodeFromString(we.code), we.retryable, we.uncertain, we.action
	}
	if re := asRpcError(err); re != nil {
		reason := re.relayReason()
		if reason != v1.RelayErrorReason_RELAY_ERROR_REASON_UNSPECIFIED {
			switch reason {
			case v1.RelayErrorReason_RELAY_ERROR_REASON_PROTOCOL_MISMATCH:
				meta.ErrReason = WorkflowReasonProtocolMismatch
				return meta, corev1.SecurityErrorCode_SECURITY_ERROR_CODE_FAILED_PRECONDITION, false, false, "refill"
			case v1.RelayErrorReason_RELAY_ERROR_REASON_PAIRING_INVALID_OR_EXPIRED,
				v1.RelayErrorReason_RELAY_ERROR_REASON_PAIRING_ALREADY_JOINED:
				meta.ErrReason = WorkflowReasonPairingCodeInvalidOrUsed
				return meta, corev1.SecurityErrorCode_SECURITY_ERROR_CODE_NOT_FOUND, false, false, "refill"
			case v1.RelayErrorReason_RELAY_ERROR_REASON_RATE_LIMITED:
				meta.ErrReason = WorkflowReasonRateLimited
				return meta, corev1.SecurityErrorCode_SECURITY_ERROR_CODE_RESOURCE_EXHAUSTED, true, false, "refill"
			case v1.RelayErrorReason_RELAY_ERROR_REASON_CLOCK_AHEAD:
				meta.ErrReason = WorkflowReasonClockBehind
				return meta, corev1.SecurityErrorCode_SECURITY_ERROR_CODE_CLOCK_SKEWED, true, false, "retry"
			case v1.RelayErrorReason_RELAY_ERROR_REASON_SECURITY_CHAIN_FULL:
				meta.ErrReason = WorkflowReasonSecurityChainFull
				return meta, corev1.SecurityErrorCode_SECURITY_ERROR_CODE_CAPACITY, false, false, ""
			case v1.RelayErrorReason_RELAY_ERROR_REASON_SETUP_TOKEN_REQUIRED,
				v1.RelayErrorReason_RELAY_ERROR_REASON_SIGNUP_TOKEN_ONLY:
				meta.ErrReason = WorkflowReasonSetupTokenRequired
				return meta, corev1.SecurityErrorCode_SECURITY_ERROR_CODE_FAILED_PRECONDITION, false, false, "refill"
			case v1.RelayErrorReason_RELAY_ERROR_REASON_SIGNUP_POW_INVALID:
				meta.ErrReason = WorkflowReasonRegPowInvalidOrExpired
				return meta, corev1.SecurityErrorCode_SECURITY_ERROR_CODE_FAILED_PRECONDITION, true, false, "retry"
			case v1.RelayErrorReason_RELAY_ERROR_REASON_RECOVERY_INVALID:
				meta.ErrReason = WorkflowReasonInvalidRecoveryCode
				return meta, corev1.SecurityErrorCode_SECURITY_ERROR_CODE_PERMISSION_DENIED, false, false, "refill"
			case v1.RelayErrorReason_RELAY_ERROR_REASON_RECOVERY_NOT_READY:
				meta.ErrReason = WorkflowReasonRecoveryNotReady
				return meta, corev1.SecurityErrorCode_SECURITY_ERROR_CODE_FAILED_PRECONDITION, true, false, "retry"
			case v1.RelayErrorReason_RELAY_ERROR_REASON_RECOVERY_REJECTED:
				meta.ErrReason = WorkflowReasonRecoveryRejected
				return meta, corev1.SecurityErrorCode_SECURITY_ERROR_CODE_FAILED_PRECONDITION, false, false, "restart"
			case v1.RelayErrorReason_RELAY_ERROR_REASON_LOGIN_FAILED,
				v1.RelayErrorReason_RELAY_ERROR_REASON_ACCOUNT_SUSPENDED,
				v1.RelayErrorReason_RELAY_ERROR_REASON_DEVICE_NOT_MEMBER:
				meta.ErrReason = WorkflowReasonPermissionDenied
				return meta, corev1.SecurityErrorCode_SECURITY_ERROR_CODE_PERMISSION_DENIED, false, false, "restart"
			case v1.RelayErrorReason_RELAY_ERROR_REASON_NOT_AUTHORITY,
				v1.RelayErrorReason_RELAY_ERROR_REASON_NO_SECURITY_DOMAIN:
				meta.ErrReason = WorkflowReasonNotAuthority
				return meta, corev1.SecurityErrorCode_SECURITY_ERROR_CODE_UNTRUSTED, false, false, ""
			case v1.RelayErrorReason_RELAY_ERROR_REASON_RELAY_DISK_LOW,
				v1.RelayErrorReason_RELAY_ERROR_REASON_RELAY_ACCOUNTS_FULL,
				v1.RelayErrorReason_RELAY_ERROR_REASON_QUOTA_DEVICES,
				v1.RelayErrorReason_RELAY_ERROR_REASON_QUOTA_MACHINES,
				v1.RelayErrorReason_RELAY_ERROR_REASON_QUOTA_SESSIONS,
				v1.RelayErrorReason_RELAY_ERROR_REASON_QUOTA_STORAGE:
				meta.ErrReason = WorkflowReasonCommandFailed
				return meta, corev1.SecurityErrorCode_SECURITY_ERROR_CODE_RESOURCE_EXHAUSTED, false, false, ""
			}
		}
		// Fallback when RelayErrorDetail is absent/UNSPECIFIED (older self-hosted relay).
		if re.network || re.code == connectCodeUnavailable || re.code == connectCodeDeadlineExceeded {
			meta.ErrReason = WorkflowReasonSuccessUncertain
			return meta, corev1.SecurityErrorCode_SECURITY_ERROR_CODE_UNAVAILABLE, true, true, "retry"
		}
		switch re.code {
		case connectCodePermissionDenied:
			meta.ErrReason = WorkflowReasonPermissionDenied
			return meta, corev1.SecurityErrorCode_SECURITY_ERROR_CODE_PERMISSION_DENIED, false, false, "restart"
		case connectCodeNotFound:
			meta.ErrReason = WorkflowReasonNotFound
			return meta, corev1.SecurityErrorCode_SECURITY_ERROR_CODE_NOT_FOUND, false, false, "refill"
		case connectCodeInvalidArgument:
			meta.ErrReason = WorkflowReasonInvalidArgument
			return meta, corev1.SecurityErrorCode_SECURITY_ERROR_CODE_INVALID, false, false, "refill"
		case connectCodeFailedPrecondition:
			meta.ErrReason = WorkflowReasonFailedPrecondition
			return meta, corev1.SecurityErrorCode_SECURITY_ERROR_CODE_FAILED_PRECONDITION, false, false, "refill"
		case connectCodeResourceExhausted:
			meta.ErrReason = WorkflowReasonRateLimited
			return meta, corev1.SecurityErrorCode_SECURITY_ERROR_CODE_RESOURCE_EXHAUSTED, true, false, "refill"
		case connectCodeCanceled:
			meta.ErrReason = WorkflowReasonOperationCanceled
			return meta, corev1.SecurityErrorCode_SECURITY_ERROR_CODE_CANCELED, false, false, ""
		default:
			meta.ErrReason = WorkflowReasonCommandFailed
			return meta, StructuredErrorCode(err), false, false, ""
		}
	}
	code := StructuredErrorCode(err)
	if code == corev1.SecurityErrorCode_SECURITY_ERROR_CODE_CLOCK_SKEWED {
		meta.ErrReason = WorkflowReasonClockBehind
		return meta, code, true, false, "retry"
	}
	if code == corev1.SecurityErrorCode_SECURITY_ERROR_CODE_CAPACITY {
		meta.ErrReason = WorkflowReasonSecurityChainFull
		return meta, code, false, false, ""
	}
	return meta, code, false, false, ""
}
