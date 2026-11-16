package outcome

import (
	"bytes"

	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

// Verify applies binding and outer/inner consistency checks after envelope authentication.
func Verify(r *v1.CommandResult, key, payload []byte, outerOutcome, outerReason string, prior bool) Category {
	if r == nil ||
		len(key) != 32 ||
		len(payload) != 32 ||
		!bytes.Equal(r.RequestKeyHash, key) ||
		!bytes.Equal(r.PayloadHash, payload) ||
		r.Outcome != outerOutcome ||
		r.Reason != outerReason ||
		r.Reason == "" ||
		(r.Outcome != "conflict" && r.ConflictKind != "") ||
		(!r.ResultAvailable && r.Kind != nil) ||
		(r.ResultAvailable && r.Outcome != "executed" && !(r.Outcome == "conflict" && r.ConflictKind == "business")) {
		return Unknown
	}
	return Classify(Evidence{
		Source:       "daemon",
		Code:         r.ErrorCode,
		Signed:       true,
		Prior:        prior,
		Declared:     Category(r.Outcome),
		Reason:       r.Reason,
		ConflictKind: r.ConflictKind,
	})
}
