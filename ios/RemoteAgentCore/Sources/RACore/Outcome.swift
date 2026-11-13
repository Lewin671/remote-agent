// Classification of current protocol-14 evidence, after signature/request verification.
enum Outcome: String { case notSent = "not_sent", rejected, executed, unknown, conflict }
func classifyOutcome(source: String, code: String, signed: Bool, prior: Bool = false, readOnly: Bool = false) -> Outcome {
    if source == "local" { return prior || code == "unknown_outcome" ? .unknown : .notSent }
    if source != "daemon" || !signed || code == "unknown_outcome" || (prior && code == "expired") { return .unknown }
    if !readOnly && ["canceled", "deadline_exceeded", "aborted"].contains(code) { return .unknown }
    return ["revision_conflict", "generation_conflict"].contains(code) ? .conflict : .executed
}
