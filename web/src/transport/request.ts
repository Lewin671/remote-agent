import { ConnectError } from "@connectrpc/connect";
import { DispatchState } from "./dispatch";
import { newRequestID } from "../crypto";
import type { Outcome } from "../outcome";
import * as l10n from "../i18n";
export const COMMAND_TTL_MS = 60_000;

/** A command's request ID and deadline. Sealing a retry with the same
 * pair makes the daemon return its first answer instead of running the
 * command again. */
export interface PreparedPayload {
  machineId: string;
  sid: string;
  plain: Uint8Array<ArrayBuffer>;
}
export class RequestSlot {
  evidence: DispatchState;
  beforeDispatch?: (slot: RequestSlot) => Promise<void>;
  constructor(public requestId: string, public deadline: number, public prepared: PreparedPayload | undefined, readonly prior: boolean) {
    if (typeof prior !== "boolean") throw new TypeError("dispatch_prior_required");
    this.evidence = new DispatchState(prior);
  }
}
/** Imports require an explicit statement about prior dispatch; bytes alone prove nothing. */
export function restoreSlot(saved: Pick<RequestSlot, "requestId" | "deadline" | "prepared">, prior: boolean): RequestSlot {
  return new RequestSlot(saved.requestId, saved.deadline, saved.prepared, prior);
}

export function newSlot(now = Date.now()): RequestSlot {
  return new RequestSlot(newRequestID(), now + COMMAND_TTL_MS, undefined, false);
}

export class CommandError extends Error {
  constructor(
    readonly code: string,
    message: string,
    readonly fromDaemon = false,
    readonly readOnly = false,
    readonly outcome: Outcome = fromDaemon ? "unknown" : "not_sent",
    readonly conflictKind = "",
    readonly resultAvailable = true,
  ) {
    super((outcome === "executed" && !resultAvailable) || conflictKind === "identity" ? message : code === "disk_full" ? l10n.t("machine_status.disk_full") : code === "storage_exhausted" ? l10n.t("machine_status.storage_exhausted") : message);
  }
  get needsResultRefresh(): boolean {
    return this.conflictKind === "identity" || (this.outcome === "executed" && !this.resultAvailable);
  }
}

/** Whether reconciliation must keep the original request slot. Missing bodies
 * and identity conflicts also use this flow while retaining their explicit
 * outcome; upload retry policy must inspect that outcome separately. */
export function uncertain(e: unknown): boolean {
  if (e instanceof CommandError)
    return e.outcome === "unknown" || e.needsResultRefresh;
  return e instanceof ConnectError;
}

