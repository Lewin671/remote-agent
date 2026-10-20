// Foreground-only durable authority coordination. No service-worker imports.
import { equal, SecurityError } from "./device-security";
import { securityEntry, updateSecurityEntry, type SecurityRecord } from "./device-security-store";

type B = Uint8Array<ArrayBuffer>;
export type OperationPhase = "PREPARED" | "PUBLISHED" | "DELIVERED";
export interface SecurityOperation<L = unknown> {
  id: string;
  revision: number;
  server: string;
  memberID: string;
  kind: "state" | "lease" | "create" | "recovery" | "offer" | "delete-records";
  parent: B;
  phase: OperationPhase;
  // Missing only while a durable, unsigned draft is being prepared.
  signed?: B;
  delivery?: B;
  local: L;
  codeSaved: boolean;
  // An independent certificate may be replaced; signed states never are.
  retry?: B;
}
const phases: OperationPhase[] = ["PREPARED", "PUBLISHED", "DELIVERED"];
export async function readSecurityOperation<L = unknown>(): Promise<SecurityOperation<L> | undefined> {
  const op = await securityEntry<SecurityOperation<L>>("pending");
  if (!op) return undefined;
  if (typeof op.id !== "string" || !op.id || !Number.isSafeInteger(op.revision) || op.revision < 0 ||
      typeof op.server !== "string" || typeof op.memberID !== "string" || !op.memberID ||
      !["state", "lease", "create", "recovery", "offer", "delete-records"].includes(op.kind) ||
      !phases.includes(op.phase) || typeof op.codeSaved !== "boolean" ||
      !(op.parent instanceof Uint8Array) || op.parent.length !== 32 ||
      !op.local || typeof op.local !== "object" ||
      [op.signed, op.retry, op.delivery].some((b) => b !== undefined && (!(b instanceof Uint8Array) || !b.length)))
    throw new SecurityError("storage");
  return op;
}
export async function claimSecurityOperation<L>(op: SecurityOperation<L>, expectedHead?: B, expectedMemberID = op.memberID): Promise<void> {
  await updateSecurityEntry<SecurityOperation<L>>("pending", (old, record) => {
    if (old) throw new Error("还有未完成的安全操作，请先继续处理");
    if (expectedHead && (!record || record.memberID !== expectedMemberID || !equal(record.stateHash, expectedHead)))
      throw new SecurityError("stale");
    return op;
  });
}
export async function saveSecurityOperation<L>(old: SecurityOperation<L>, next: SecurityOperation<L>, expectedHead?: B): Promise<SecurityOperation<L>> {
  const saved = { ...next, revision: old.revision + 1 };
  await updateSecurityEntry<SecurityOperation<L>>("pending", (stored, record) => {
    if (!stored || stored.id !== old.id || stored.revision !== old.revision ||
        saved.id !== old.id || saved.kind !== old.kind || saved.server !== old.server ||
        saved.memberID !== old.memberID || !equal(saved.parent, old.parent) ||
        phases.indexOf(saved.phase) < phases.indexOf(stored.phase) ||
        (stored.codeSaved && !saved.codeSaved) ||
        (stored.signed && (!saved.signed || !equal(stored.signed, saved.signed))) ||
        (stored.delivery && (!saved.delivery || !equal(stored.delivery, saved.delivery))))
      throw new SecurityError("conflict");
    if (expectedHead && (!record || !equal(record.stateHash, expectedHead))) throw new SecurityError("stale");
    return saved;
  });
  return saved;
}
/** Call only after verified completion, or an explicitly verified terminal exit. */
export async function clearSecurityOperation(op: SecurityOperation): Promise<void> {
  await updateSecurityEntry<SecurityOperation>("pending", (old) => {
    if (!old || old.id !== op.id || old.revision !== op.revision) throw new SecurityError("conflict");
    return undefined;
  });
}
export function operation<L>(kind: SecurityOperation["kind"], server: string, memberID: string, parent: B, local: L): SecurityOperation<L> {
  return { id: crypto.randomUUID(), revision: 0, kind, server, memberID, parent: new Uint8Array(parent), local, phase: "PREPARED", codeSaved: false };
}
export function isAuthority(record: SecurityRecord): boolean {
  return record.member && record.memberID === record.states.at(-1)!.core.authorityID;
}

/** Every await may cross a hide/show cycle. A captured generation cannot be
 * reactivated by returning to the page, even if visibility is now visible. */
export class AuthorityLifecycle {
  private generation = 0;
  private enabled = true;
  private hidden = false;
  private change = () => { this.generation++; };
  private hide = () => { this.hidden = true; this.change(); };
  private show = () => { this.hidden = false; this.change(); };
  constructor(private readonly visible = () => typeof document !== "undefined" && document.visibilityState === "visible") {
    globalThis.addEventListener?.("visibilitychange", this.change);
    globalThis.addEventListener?.("pagehide", this.hide);
    globalThis.addEventListener?.("pageshow", this.show);
  }
  pause() { this.enabled = false; this.change(); }
  resume() { this.enabled = true; this.change(); }
  dispose() {
    this.pause();
    globalThis.removeEventListener?.("visibilitychange", this.change);
    globalThis.removeEventListener?.("pagehide", this.hide);
    globalThis.removeEventListener?.("pageshow", this.show);
  }
  async run<T>(work: (check: () => void) => Promise<T>): Promise<T> {
    const generation = this.generation;
    const check = () => {
      if (!this.enabled || this.hidden || !this.visible() || generation !== this.generation)
        throw new Error("页面已暂停，请回到前台继续安全操作");
    };
    check();
    if (!navigator.locks) throw new Error("浏览器不支持 Web Locks，无法安全管理账户");
    return navigator.locks.request("remote-agent-authority", { mode: "exclusive" }, async () => {
      check();
      return work(check);
    });
  }
}

export const BROWSER_AUTHORITY_NOTICE = "此浏览器将是唯一管理端。清理网站数据或换浏览器会丢失管理密钥；恢复码必须离线保存。日常建议用 App 管理。";
/** Expose the actual browser result to batch C; persistence cannot prevent
 * explicit site-data deletion and is never reported as a guarantee. */
export async function requestAuthorityPersistence(): Promise<boolean> {
  try { return await navigator.storage?.persist?.() ?? false; }
  catch { return false; }
}
export async function authorityExitAdvice(): Promise<{ authority: boolean; unresolved: boolean; message: string }> {
  const pending = await readSecurityOperation();
  let authority = false;
  try {
    const { loadSecurity } = await import("./device-security-store");
    authority = isAuthority(await loadSecurity());
  } catch { /* A damaged pin is retained until an explicit storage reset. */ }
  return { authority, unresolved: !!pending,
    message: pending ? "安全操作结果尚未核实，请先继续原操作；退出会删除唯一完成材料。"
      : authority ? "此浏览器是唯一管理端，建议先交接；退出会删除管理密钥，没有恢复码备份将丢失管理权。"
      : "退出会删除本机密钥，需要重新配对。" };
}

interface CodeRotationNotice { server: string; account: string; genesis: B }
export async function markRecoveryCodeRotation(server: string, pin: SecurityRecord["pin"], needed: boolean): Promise<void> {
  await updateSecurityEntry<CodeRotationNotice>("needs-code-rotation", (old) => {
    if (needed) return { server, account: pin.account, genesis: pin.genesis };
    if (old && (old.server !== server || old.account !== pin.account || !equal(old.genesis, pin.genesis))) throw new SecurityError("conflict");
    return undefined;
  });
}
export async function needsRecoveryCodeRotation(server: string, pin: SecurityRecord["pin"]): Promise<boolean> {
  const notice = await securityEntry<CodeRotationNotice>("needs-code-rotation");
  return !!notice && notice.server === server && notice.account === pin.account && equal(notice.genesis, pin.genesis);
}
