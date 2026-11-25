package security

import (
	"strings"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

// RelayErrorDetailTypeName is the protobuf full name used by Connect error details
// for remoteagent.v1.RelayErrorDetail.
const RelayErrorDetailTypeName = "remoteagent.v1.RelayErrorDetail"

// ParseRelayErrorDetail decodes the first valid non-unspecified RelayErrorDetail
// from opaque Connect error detail bytes using litewire. Returns nil when absent,
// malformed, or UNSPECIFIED so callers fall back to Connect status code semantics.
func ParseRelayErrorDetail(details []*corev1.RpcErrorDetailBytes) *v1.RelayErrorDetail {
	for _, d := range details {
		if d == nil || len(d.Value) == 0 {
			continue
		}
		if strings.TrimPrefix(d.TypeName, "type.googleapis.com/") != RelayErrorDetailTypeName {
			continue
		}
		var detail v1.RelayErrorDetail
		if detail.Unmarshal(d.Value) != nil {
			continue
		}
		if detail.Reason == v1.RelayErrorReason_RELAY_ERROR_REASON_UNSPECIFIED {
			continue
		}
		return &detail
	}
	return nil
}

// isKnownRelayErrorReason reports whether r is a non-zero RelayErrorReason
// defined in this client's schema. Unrecognized future enum values return false
// so callers fall back to Connect status code semantics just like UNSPECIFIED.
func isKnownRelayErrorReason(r v1.RelayErrorReason) bool {
	switch r {
	case v1.RelayErrorReason_RELAY_ERROR_REASON_LOGIN_FAILED,
		v1.RelayErrorReason_RELAY_ERROR_REASON_PROTOCOL_MISMATCH,
		v1.RelayErrorReason_RELAY_ERROR_REASON_TOKEN_INVALID,
		v1.RelayErrorReason_RELAY_ERROR_REASON_ACCOUNT_SUSPENDED,
		v1.RelayErrorReason_RELAY_ERROR_REASON_DEVICE_NOT_MEMBER,
		v1.RelayErrorReason_RELAY_ERROR_REASON_NO_SECURITY_DOMAIN,
		v1.RelayErrorReason_RELAY_ERROR_REASON_NOT_AUTHORITY,
		v1.RelayErrorReason_RELAY_ERROR_REASON_CLOCK_AHEAD,
		v1.RelayErrorReason_RELAY_ERROR_REASON_SECURITY_CHAIN_FULL,
		v1.RelayErrorReason_RELAY_ERROR_REASON_SETUP_TOKEN_REQUIRED,
		v1.RelayErrorReason_RELAY_ERROR_REASON_SIGNUP_POW_REQUIRED,
		v1.RelayErrorReason_RELAY_ERROR_REASON_SIGNUP_POW_INVALID,
		v1.RelayErrorReason_RELAY_ERROR_REASON_SIGNUP_TOKEN_ONLY,
		v1.RelayErrorReason_RELAY_ERROR_REASON_RATE_LIMITED,
		v1.RelayErrorReason_RELAY_ERROR_REASON_RELAY_DISK_LOW,
		v1.RelayErrorReason_RELAY_ERROR_REASON_RELAY_ACCOUNTS_FULL,
		v1.RelayErrorReason_RELAY_ERROR_REASON_QUOTA_DEVICES,
		v1.RelayErrorReason_RELAY_ERROR_REASON_QUOTA_MACHINES,
		v1.RelayErrorReason_RELAY_ERROR_REASON_QUOTA_SESSIONS,
		v1.RelayErrorReason_RELAY_ERROR_REASON_QUOTA_STORAGE,
		v1.RelayErrorReason_RELAY_ERROR_REASON_PAIRING_INVALID_OR_EXPIRED,
		v1.RelayErrorReason_RELAY_ERROR_REASON_PAIRING_ALREADY_JOINED,
		v1.RelayErrorReason_RELAY_ERROR_REASON_RECOVERY_INVALID,
		v1.RelayErrorReason_RELAY_ERROR_REASON_RECOVERY_NOT_READY,
		v1.RelayErrorReason_RELAY_ERROR_REASON_RECOVERY_REJECTED:
		return true
	default:
		return false
	}
}
