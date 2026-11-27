import "fake-indexeddb/auto";
import { test, expect } from "vitest";
import { readFileSync, writeFileSync, realpathSync } from "node:fs";
import { Remote } from "./remote";
import { getCoreHost } from "./core/host";
import { ScreenTarget } from "./gen/remoteagent/core/v1/app_pb";
import { createTestSecurityStore } from "./test/security-fixture";

test.skipIf(!process.env.RA_1B_FLOW_ROOT)("full snapshot and approval flow under daemon saturation", async () => {
  const root = process.env.RA_1B_FLOW_ROOT!;
  expect(realpathSync(root).startsWith("/private/tmp/") || realpathSync(root).startsWith("/tmp/")).toBe(true);
  const config = JSON.parse(readFileSync(root + "/bootstrap.json", "utf8"));
  expect(new URL(config.server).hostname).toBe("127.0.0.1");
  const seed = Uint8Array.from(Buffer.from(config.seed, "base64"));
  const { store, record } = await createTestSecurityStore(config.record, seed, config.server);
  const settled = new Map<string, () => void>();
  class MeasurementRemote extends Remote {
    constructor() { super({ server: config.server, deviceId: record.memberID, accountId: record.pin.account }, undefined, undefined, undefined, undefined, store); }
    override async executeCommandAttempt(eff: Parameters<Remote["executeCommandAttempt"]>[0], signal?: AbortSignal) {
      const out = await super.executeCommandAttempt(eff, signal);
      for (const [key, resolve] of settled) {
        if (eff.sessionId === "flow-web") { settled.delete(key); resolve(); break; }
      }
      return out;
    }
  }
  const remote = new MeasurementRemote();
  const control = async (action: string) => {
    const response = await fetch(config.control, { method: "POST", headers: { "Content-Type": "application/json", "X-1b-Measure": config.controlToken }, body: JSON.stringify({ action, client: "web" }) });
    expect(response.ok).toBe(true); return await response.json();
  };
  const host = getCoreHost(remote);
  const until = (condition: () => boolean) => new Promise<void>((resolve, reject) => {
    let unsubConn = () => {};
    let unsubSess = () => {};
    const cleanup = () => { unsubConn(); unsubSess(); };
    const timeout = setTimeout(() => { cleanup(); reject(Error("client did not reach its ready state")); }, 10_000);
    const check = () => { if (condition()) { clearTimeout(timeout); cleanup(); resolve(); } };
    unsubConn = host.subscribe(ScreenTarget.CONNECTION, check);
    unsubSess = host.subscribe(ScreenTarget.SESSION, check);
    check();
  });
  const sid = "flow-web";
  const resolveApproval = (i: number, timeoutMs = 1500) => new Promise<void>((resolve, reject) => {
    const approvalId = `${sid}-approval-${String(i).padStart(3, "0")}`;
    const timer = setTimeout(() => { settled.delete(approvalId); reject(Error("approval timed out")); }, timeoutMs);
    settled.set(approvalId, () => { clearTimeout(timer); resolve(); });
    host.sessionResolveApproval(sid, approvalId, true, false);
  });
  let loaded = false;
  try {
    await control("reset");
    remote.start();
    host.watchSession(sid);
    await until(() => host.viewConnection().connected && !!host.viewSession(sid).header?.found);
    for (let i = 0; i < 20; i++) await resolveApproval(i, 5000);
    const load = await control("start"); loaded = true;
    expect(load).toMatchObject({ normal: 4, bulk: 2, input: 1, input_overdue: true });
    const timings: number[] = []; let completed = 0; let failed = 0;
    for (let i = 20; i < 120; i++) {
      const start = performance.now();
      try {
        await resolveApproval(i, 1500);
        completed++;
      } catch { failed++; } finally { timings.push(performance.now() - start); }
      if (failed) break;
    }
    const counters = await control("stop"); loaded = false;
    timings.sort((a,b)=>a-b);
    const q = (p: number) => timings[Math.ceil(timings.length*p)-1] ?? -1;
    writeFileSync(root + "/web-flow.json", JSON.stringify({ client:"web", attempts:timings.length, warmup:20, completed, failed, completed_fraction:completed/timings.length, full_flow_ms:{p50:q(.5),p95:q(.95),p99:q(.99)}, ...counters }, null, 2));
    expect(completed).toBe(100); expect(failed).toBe(0);
    expect(counters.normal).toBe(4); expect(counters.bulk).toBe(2);
    expect(counters).toMatchObject({ input: 1, input_overdue: true, flood_workers_before_release: 4 });
    expect(counters.flood_calls).toBeGreaterThan(0);
    expect(counters.snapshot_calls).toBeGreaterThanOrEqual(100); expect(counters.approval_calls).toBe(100);
    expect(q(.99)).toBeLessThanOrEqual(100);
  } finally { if (loaded) await control("stop"); remote.stop(); }
}, 120_000);
