/** Protocol 15 evidence; signed includes signature, hashes, scope and outer consistency. */
export type Outcome = "not_sent" | "rejected" | "executed" | "unknown" | "conflict";
export interface Evidence {
  source: string; code: string; signed: boolean; prior: boolean; readOnly?: boolean;
  declared?: string; reason?: string; conflictKind?: string; dispatched?: boolean;
  notForwarded?: boolean; attemptBound?: boolean; admitted?: boolean;
}
export function classifyOutcome(e: Evidence): Outcome {
  if (e.source === "local") return e.prior || e.dispatched ? "unknown" : "not_sent";
  if (e.source === "server") return !e.prior && e.notForwarded && e.attemptBound ? "not_sent" : "unknown";
  if (e.source !== "daemon" || !e.signed) return "unknown";
  switch (e.declared) {
    case "executed": return "executed";
    case "rejected": return !e.admitted && ["invalid_envelope", "invalid_body", "invalid_scope", "protocol_mismatch", "member_removed"].includes(e.reason ?? "") ? "rejected" : "unknown";
    case "conflict": return ["identity", "business"].includes(e.conflictKind ?? "") ? "conflict" : "unknown";
    case "not_sent": return !e.prior && !e.admitted ? "not_sent" : "unknown";
    default: return "unknown";
  }
}

import type { CommandResult } from "./gen/remoteagent/v1/remote_pb";
import { equal } from "./device-security";
export function verifiedOutcome(r:CommandResult,key:Uint8Array,payload:Uint8Array,outerOutcome:string,outerReason:string,prior=false):Outcome {
 if(key.length!==32 || payload.length!==32 || !equal(r.requestKeyHash,key) || !equal(r.payloadHash,payload) || r.outcome!==outerOutcome || r.reason!==outerReason || !r.reason || r.outcome!=="conflict" && r.conflictKind!=="" || !r.resultAvailable && r.kind.case!==undefined || r.resultAvailable && r.outcome!=="executed" && !(r.outcome==="conflict" && r.conflictKind==="business")) return "unknown";
 return classifyOutcome({source:"daemon",code:r.errorCode,signed:true,prior,declared:r.outcome,reason:r.reason,conflictKind:r.conflictKind});
}
