/// Signed means signature, hashes, scope and outer consistency have all passed.
public enum Outcome: String, Sendable { case notSent = "not_sent", rejected, executed, unknown, conflict }
func classifyOutcome(source: String, code: String, signed: Bool, prior: Bool = false, readOnly: Bool = false,
    declared: String = "", reason: String = "", conflictKind: String = "", dispatched: Bool = false,
    notForwarded: Bool = false, attemptBound: Bool = false, admitted: Bool = false) -> Outcome {
    if source == "local" { return prior || dispatched ? .unknown : .notSent }
    if source == "server" { return !prior && notForwarded && attemptBound ? .notSent : .unknown }
    if source != "daemon" || !signed { return .unknown }
    switch declared {
    case "executed": return .executed
    case "rejected": return !admitted && ["invalid_envelope", "invalid_body", "invalid_scope", "protocol_mismatch", "member_removed"].contains(reason) ? .rejected : .unknown
    case "conflict": return ["identity", "business"].contains(conflictKind) ? .conflict : .unknown
    case "not_sent": return !prior && !admitted ? .notSent : .unknown
    default: return .unknown
    }
}

import Foundation
import RAProto
func verifiedOutcome(_ r:PBCommandResult,key:Data,payload:Data,outerOutcome:String,outerReason:String,prior:Bool=false)->Outcome {
 if key.count != 32 || payload.count != 32 || r.requestKeyHash != key || r.payloadHash != payload || r.outcome != outerOutcome || r.reason != outerReason || r.reason.isEmpty || (r.outcome != "conflict" && !r.conflictKind.isEmpty) || (!r.resultAvailable && r.kind != nil) || (r.resultAvailable && r.outcome != "executed" && !(r.outcome == "conflict" && r.conflictKind == "business")) { return .unknown }
 return classifyOutcome(source:"daemon",code:r.errorCode,signed:true,prior:prior,declared:r.outcome,reason:r.reason,conflictKind:r.conflictKind)
}
