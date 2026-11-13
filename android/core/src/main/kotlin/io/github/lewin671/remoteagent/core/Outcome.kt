package io.github.lewin671.remoteagent.core

// Protocol-14 evidence only. A server diagnostic has no not_forwarded proof.
internal enum class Outcome { NOT_SENT, REJECTED, EXECUTED, UNKNOWN, CONFLICT }
internal fun classifyOutcome(source: String, code: String, signed: Boolean, prior: Boolean = false, readOnly: Boolean = false): Outcome {
    if (source == "local") return if (prior || code == "unknown_outcome") Outcome.UNKNOWN else Outcome.NOT_SENT
    if (source != "daemon" || !signed || code == "unknown_outcome" || (prior && code == "expired")) return Outcome.UNKNOWN
    if (!readOnly && code in setOf("canceled", "deadline_exceeded", "aborted")) return Outcome.UNKNOWN
    return if (code in setOf("revision_conflict", "generation_conflict")) Outcome.CONFLICT else Outcome.EXECUTED
}
