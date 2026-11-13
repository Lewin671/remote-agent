package deviceauth

// Completion is evidence supplied by the execution owner, independently of
// serialization and diagnostic errors. The callback error is diagnostic only;
// a final completion is returned and cached identically even if it is non-nil. Its zero value is undetermined.
// Durable completion uses storage generation 4.
type Completion struct {
	category Category
	final    bool
	result   []byte
}

// Final asserts that the operation completed or its business result is settled.
func Final(result []byte) Completion {
	return Completion{final: true, category: Executed, result: result}
}

// Undetermined keeps the admitted ID reserved without inventing a final result.
func Undetermined() Completion { return Completion{} }

// FinalBusinessConflict records a settled backend version/object conflict.
func FinalBusinessConflict(result []byte) Completion {
	return Completion{final: true, category: BusinessConflict, result: result}
}
