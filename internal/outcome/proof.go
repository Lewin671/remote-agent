package outcome

import (
	"bytes"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

// Verify applies binding/consistency checks after envelope authentication.
func Verify(r *pb.CommandResult, key, payload []byte, outerOutcome, outerReason string, prior bool) Category {
	if r == nil || len(key) != 32 || len(payload) != 32 || !bytes.Equal(r.RequestKeyHash, key) || !bytes.Equal(r.PayloadHash, payload) || r.Outcome != outerOutcome || r.Reason != outerReason || r.Reason == "" || r.Outcome != "conflict" && r.ConflictKind != "" || !r.ResultAvailable && r.Kind != nil || r.ResultAvailable && r.Outcome != "executed" && !(r.Outcome == "conflict" && r.ConflictKind == "business") {
		return Unknown
	}
	return Classify(Evidence{Source: "daemon", Signed: true, Prior: prior, Declared: Category(r.Outcome), Reason: r.Reason, ConflictKind: r.ConflictKind})
}
