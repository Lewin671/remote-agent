/** Evidence available on protocol 14; signature includes request binding.
 * There is no explicit outcome or not_forwarded field on this wire. */
export type Outcome = "not_sent" | "rejected" | "executed" | "unknown" | "conflict";
export interface Evidence { source: string; code: string; signed: boolean; prior: boolean; readOnly: boolean }
export function classifyOutcome(e: Evidence): Outcome {
  if (e.source === "local") return e.prior || e.code === "unknown_outcome" ? "unknown" : "not_sent";
  if (e.source !== "daemon" || !e.signed || e.code === "unknown_outcome" || (e.prior && e.code === "expired")) return "unknown";
  if (!e.readOnly && ["canceled", "deadline_exceeded", "aborted"].includes(e.code)) return "unknown";
  return ["revision_conflict", "generation_conflict"].includes(e.code) ? "conflict" : "executed";
}
