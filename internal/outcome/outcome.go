// Package outcome classifies verified execution evidence. Diagnostic error codes
// never decide whether a command ran.
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
	Source       string   `json:"source"`
	Code         string   `json:"code"`   // diagnostic only
	Signed       bool     `json:"signed"` // signature, hashes, scope and inner/outer consistency verified
	Prior        bool     `json:"prior"`
	ReadOnly     bool     `json:"readOnly"` // diagnostic only; completion is explicit
	Declared     Category `json:"declared"`
	Reason       string   `json:"reason"`
	ConflictKind string   `json:"conflictKind"`
	Dispatched   bool     `json:"dispatched"`
	NotForwarded bool     `json:"notForwarded"`
	AttemptBound bool     `json:"attemptBound"`
	Admitted     bool     `json:"admitted"`
}

func Classify(e Evidence) Category {
	switch e.Source {
	case "local":
		if !e.Prior && !e.Dispatched {
			return NotSent
		}
	case "server":
		if !e.Prior && e.NotForwarded && e.AttemptBound {
			return NotSent
		}
	case "daemon":
		if !e.Signed {
			return Unknown
		}
		switch e.Declared {
		case Executed:
			return Executed
		case Rejected:
			if !e.Admitted && (e.Reason == "invalid_envelope" || e.Reason == "invalid_body" || e.Reason == "invalid_scope" || e.Reason == "protocol_mismatch" || e.Reason == "member_removed") {
				return Rejected
			}
		case Conflict:
			if e.ConflictKind == "identity" || e.ConflictKind == "business" {
				return Conflict
			}
		case NotSent:
			if !e.Prior && !e.Admitted {
				return NotSent
			}
		}
	}
	return Unknown
}
