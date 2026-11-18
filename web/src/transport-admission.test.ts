import "fake-indexeddb/auto";
import { test, expect } from "vitest";
import { readFileSync, writeFileSync, realpathSync } from "node:fs";
import { create } from "@bufbuild/protobuf";
import { ResolveApprovalRequestSchema } from "./gen/remoteagent/v1/local_pb";
import { GetLiveSnapshotRequestSchema } from "./gen/remoteagent/v1/remote_pb";
import { Remote } from "./remote";
import { MemorySecurityStorage, recordFromJSON } from "./test/security-fixture";
import { concat, fromHex } from "./crypto";

test.skipIf(!process.env.RA_1B_FLOW_ROOT)("full snapshot and approval flow under daemon saturation", async () => {
  const root = process.env.RA_1B_FLOW_ROOT!;
  expect(realpathSync(root).startsWith("/private/tmp/") || realpathSync(root).startsWith("/tmp/")).toBe(true);
  const config = JSON.parse(readFileSync(root + "/bootstrap.json", "utf8"));
  expect(new URL(config.server).hostname).toBe("127.0.0.1");
  const { record } = await recordFromJSON(config.record);
  const seed = Uint8Array.from(Buffer.from(config.seed, "base64"));
  const key = await crypto.subtle.importKey("pkcs8", concat(fromHex("302e020100300506032b657004220420"), seed), "Ed25519", false, ["sign"]);
  class MeasurementRemote extends Remote {
    constructor() { super({ server: config.server, deviceId: record.memberID, devicePrivate: key }, undefined, new MemorySecurityStorage(record)); }
  }
  const remote = new MeasurementRemote();
  const control = async (action: string) => {
    const response = await fetch(config.control, { method: "POST", headers: { "Content-Type": "application/json", "X-1b-Measure": config.controlToken }, body: JSON.stringify({ action, client: "web" }) });
    expect(response.ok).toBe(true); return await response.json();
  };
  const until = (condition: () => boolean) => new Promise<void>((resolve, reject) => {
    let unsubscribe = () => {};
    const timeout = setTimeout(() => { unsubscribe(); reject(Error("client did not reach its ready state")); }, 10_000);
    const check = () => { if (condition()) { clearTimeout(timeout); unsubscribe(); resolve(); } };
    unsubscribe = remote.subscribe(check); check();
  });
  const sid = "flow-web";
  const approval = (i: number) => ({ case: "resolveApproval" as const, value: create(ResolveApprovalRequestSchema, { sessionId:sid, approvalId:`${sid}-approval-${String(i).padStart(3,"0")}`, allow:true }) });
  let loaded = false;
  try {
    await control("reset");
    remote.start();
    await until(() => remote.conn === "online" && remote.sessions.has(sid));
    await remote.command(sid, {case:"getLiveSnapshot",value:create(GetLiveSnapshotRequestSchema,{sessionId:sid})});
    for (let i = 0; i < 20; i++) await remote.command(sid, approval(i));
    const load = await control("start"); loaded = true;
    expect(load).toMatchObject({ normal: 4, bulk: 2, input: 1, input_overdue: true });
    const timings: number[] = []; let completed = 0; let failed = 0;
    for (let i = 20; i < 120; i++) {
      const start = performance.now();
      let timer: ReturnType<typeof setTimeout> | undefined;
      try {
        await Promise.race([
          remote.command(sid, approval(i)),
          new Promise<never>((_, reject) => { timer = setTimeout(() => reject(Error("full approval flow timed out")), 1500); }),
        ]);
        completed++;
      } catch { failed++; } finally { clearTimeout(timer); timings.push(performance.now() - start); }
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
