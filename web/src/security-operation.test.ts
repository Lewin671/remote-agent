import { IDBFactory } from "fake-indexeddb";
import { afterEach, expect, it, vi } from "vitest";
import { AuthorityLifecycle, claimSecurityOperation, clearSecurityOperation, operation, readSecurityOperation, saveSecurityOperation } from "./security-operation";
import { securityEntry, updateSecurityEntry } from "./device-security-store";

afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); });
function lockManager() {
  let tail = Promise.resolve();
  return { request: (_name: string, _options: object, callback: () => Promise<unknown>) => {
    const work = tail.then(callback); tail = work.then(() => undefined, () => undefined); return work;
  } };
}
it("claims exactly one transaction slot across two tabs before either signs", async () => {
  vi.stubGlobal("indexedDB", new IDBFactory());
  vi.stubGlobal("navigator", { locks: lockManager() });
  const a = new AuthorityLifecycle(() => true), b = new AuthorityLifecycle(() => true);
  let signatures = 0;
  const prepare = (gate: AuthorityLifecycle) => gate.run(async (check) => {
    const op = operation("lease", "https://relay.example.com", "member", new Uint8Array(32), {});
    await claimSecurityOperation(op);
    check(); signatures++;
    await saveSecurityOperation(op, { ...op, signed: new Uint8Array([signatures]) });
  });
  try {
    const results = await Promise.allSettled([prepare(a), prepare(b)]);
    expect(results.filter((r) => r.status === "fulfilled")).toHaveLength(1);
    expect(signatures).toBe(1);
    expect((await readSecurityOperation())!.signed).toEqual(new Uint8Array([1]));
  } finally { a.dispose(); b.dispose(); }
});
it("enforces request immutability, phase/revision CAS and monotonic confirmation", async () => {
  vi.stubGlobal("indexedDB", new IDBFactory());
  let op = operation("state", "https://relay.example.com", "member", new Uint8Array(32), {});
  await claimSecurityOperation(op);
  const original = op;
  op = await saveSecurityOperation(op, { ...op, signed: new Uint8Array([1]), codeSaved: true });
  await expect(saveSecurityOperation(original, { ...original, signed: new Uint8Array([2]) })).rejects.toThrow();
  await expect(saveSecurityOperation(op, { ...op, signed: new Uint8Array([2]) })).rejects.toThrow();
  await expect(saveSecurityOperation(op, { ...op, codeSaved: false })).rejects.toThrow();
  op = await saveSecurityOperation(op, { ...op, phase: "PUBLISHED" });
  await expect(saveSecurityOperation(op, { ...op, phase: "PREPARED" })).rejects.toThrow();
  await expect(clearSecurityOperation(original)).rejects.toThrow();
  await clearSecurityOperation(op); expect(await readSecurityOperation()).toBeUndefined();
});
it("invalidates an in-flight generation across hide/show and while waiting for the cross-tab lock", async () => {
  vi.stubGlobal("navigator", { locks: lockManager() });
  const gate = new AuthorityLifecycle(() => true);
  let release!: () => void;
  const waiting = new Promise<void>((r) => { release = r; });
  const signature = vi.fn();
  try {
    const first = gate.run(async (check) => { await waiting; check(); signature(); });
    const second = gate.run(async (check) => { check(); signature(); });
    await Promise.resolve(); gate.pause(); gate.resume(); release();
    expect((await Promise.allSettled([first, second])).every((r) => r.status === "rejected")).toBe(true);
    expect(signature).not.toHaveBeenCalled();
    await gate.run(async (check) => { check(); signature(); });
    expect(signature).toHaveBeenCalledTimes(1);
  } finally { gate.dispose(); }
});
it("aborts an entry and preserves the old value if a transaction callback fails", async () => {
  vi.stubGlobal("indexedDB", new IDBFactory());
  await updateSecurityEntry("test", () => ({ n: 1 }));
  await expect(updateSecurityEntry("test", () => { throw new Error("injected write failure"); })).rejects.toThrow();
  expect(await securityEntry("test")).toEqual({ n: 1 });
});

it("stops on a malformed persisted operation rather than replacing it", async () => {
  vi.stubGlobal("indexedDB", new IDBFactory());
  await updateSecurityEntry("pending", () => ({ kind: "state", phase: "unknown" }));
  await expect(readSecurityOperation()).rejects.toThrow("storage");
});
