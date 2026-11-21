import { readSecurityOperation, type SecurityOperation } from "./security-operation";
import { loadAccount, loadRegistration } from "./storage";

interface RecoveryStartupLocal {
  recoveryId: string;
  readyMs: bigint;
}

export type AuthorityStartup =
  | { kind: "create" | "recovery"; phase: SecurityOperation["phase"]; codeSaved: boolean; recoveryId?: string; readyMs?: bigint }
  | { kind: "registration" }
  | { kind: "account"; pending: boolean }
  | { kind: "empty" };

export async function authorityStartup(): Promise<AuthorityStartup> {
  const pending = await readSecurityOperation<RecoveryStartupLocal>();
  if (pending?.kind === "create" || pending?.kind === "recovery") {
    return {
      kind: pending.kind,
      phase: pending.phase,
      codeSaved: pending.codeSaved,
      recoveryId: pending.kind === "recovery" ? pending.local.recoveryId : undefined,
      readyMs: pending.kind === "recovery" ? pending.local.readyMs : undefined,
    };
  }
  if (await loadRegistration()) return { kind: "registration" };
  return (await loadAccount()) ? { kind: "account", pending: !!pending } : { kind: "empty" };
}
