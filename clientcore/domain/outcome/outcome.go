// Package outcome classifies verified execution evidence without reflection.
// Diagnostic error codes never decide whether a command ran.
package outcome

// Category identifies the verified execution outcome of a command attempt.
type Category string

const (
	NotSent  Category = "not_sent"
	Rejected Category = "rejected"
	Executed Category = "executed"
	Unknown  Category = "unknown"
	Conflict Category = "conflict"
)

// Evidence holds the authenticated outcome inputs from local, server, or daemon sources.
type Evidence struct {
	Source       string
	Code         string
	Signed       bool
	Prior        bool
	ReadOnly     bool
	Declared     Category
	Reason       string
	ConflictKind string
	Dispatched   bool
	NotForwarded bool
	AttemptBound bool
	Admitted     bool
}

// Classify maps authenticated Evidence to a Category.
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
			// The current daemon has no durable terminal-rejection proof. A
			// pre-admission refusal alone cannot retire a replayable request.
			return Unknown
		}
	}
	return Unknown
}
