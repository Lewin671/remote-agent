package io.github.lewin671.remoteagent.core

/** Signed means signature, hashes, scope and outer consistency have all passed. */
enum class Outcome { NOT_SENT, REJECTED, EXECUTED, UNKNOWN, CONFLICT }
internal fun classifyOutcome(source: String, code: String, signed: Boolean, prior: Boolean = false, readOnly: Boolean = false,
    declared: String = "", reason: String = "", conflictKind: String = "", dispatched: Boolean = false,
    notForwarded: Boolean = false, attemptBound: Boolean = false, admitted: Boolean = false): Outcome {
    if (source == "local") return if (prior || dispatched) Outcome.UNKNOWN else Outcome.NOT_SENT
    if (source == "server") return if (!prior && notForwarded && attemptBound) Outcome.NOT_SENT else Outcome.UNKNOWN
    if (source != "daemon" || !signed) return Outcome.UNKNOWN
    return when (declared) {
        "executed" -> Outcome.EXECUTED
        "rejected" -> if (!admitted && reason in setOf("invalid_envelope", "invalid_body", "invalid_scope", "protocol_mismatch", "member_removed")) Outcome.REJECTED else Outcome.UNKNOWN
        "conflict" -> if (conflictKind in setOf("identity", "business")) Outcome.CONFLICT else Outcome.UNKNOWN
        // No current daemon proof durably retires a refused authorization.
        "not_sent" -> Outcome.UNKNOWN
        else -> Outcome.UNKNOWN
    }
}

internal fun verifiedOutcome(r:io.github.lewin671.remoteagent.proto.CommandResult,key:ByteArray,payload:ByteArray,outerOutcome:String,outerReason:String,prior:Boolean=false):Outcome {
 if(key.size!=32 || payload.size!=32 || !r.requestKeyHash.toByteArray().contentEquals(key) || !r.payloadHash.toByteArray().contentEquals(payload) || r.outcome!=outerOutcome || r.reason!=outerReason || r.reason.isEmpty() || (r.outcome!="conflict" && r.conflictKind.isNotEmpty()) || (!r.resultAvailable && r.kindCase!=io.github.lewin671.remoteagent.proto.CommandResult.KindCase.KIND_NOT_SET) || (r.resultAvailable && r.outcome!="executed" && !(r.outcome=="conflict" && r.conflictKind=="business"))) return Outcome.UNKNOWN
 return classifyOutcome("daemon",r.errorCode,true,prior,declared=r.outcome,reason=r.reason,conflictKind=r.conflictKind)
}
