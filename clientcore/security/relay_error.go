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
