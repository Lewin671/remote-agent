// Package outcome classifies the evidence available on the current protocol.
// It never treats an unsigned relay diagnostic as a daemon execution proof.
package outcome

type Category string

const (
	NotSent  Category = "not_sent"
	Rejected Category = "rejected"
	Executed Category = "executed"
	Unknown  Category = "unknown"
	Conflict Category = "conflict"
)

type Evidence struct {
	Source   string `json:"source"` // local means proven not dispatched; server means dispatch may have happened
	Code     string `json:"code"`
	Signed   bool   `json:"signed"` // verified daemon signature AND original request binding
	Prior    bool   `json:"prior"`  // an earlier attempt may have entered admission
	ReadOnly bool   `json:"readOnly"`
}

// Classify is the protocol-14 adapter, not the future explicit-outcome wire.
// No current diagnostic can establish a monotonic rejected outcome.
func Classify(e Evidence) Category {
	switch e.Source {
	case "local":
		if e.Prior || e.Code == "unknown_outcome" {
			return Unknown
		}
		return NotSent
	case "daemon":
		if !e.Signed || e.Code == "unknown_outcome" || (e.Prior && e.Code == "expired") {
			return Unknown
		}
		if !e.ReadOnly && (e.Code == "canceled" || e.Code == "deadline_exceeded" || e.Code == "aborted") {
			return Unknown
		}
		if e.Code == "revision_conflict" || e.Code == "generation_conflict" {
			return Conflict
		}
		return Executed
	default:
		return Unknown
	}
}
