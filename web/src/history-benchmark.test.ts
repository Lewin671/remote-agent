// Reproducible Node/fake-indexeddb microbenchmark. No relay/network latency.
// Benchmark explicitly: RA_HISTORY_BENCHMARK=1 pnpm test src/history-benchmark.test.ts --no-file-parallelism
import { IDBObjectStore } from "fake-indexeddb";
import { afterEach, beforeAll, expect, it, vi } from "vitest";
import { DeviceSecurityClient } from "./device-security-client";
import * as security from "./device-security";
import * as storage from "./device-security-store";
import { storedSecurity, storedEvent, rawSecurity } from "./test/stored-security-fixture";

const count = process.env.RA_HISTORY_BENCHMARK ? 300 : 8;
let f: Awaited<ReturnType<typeof storedSecurity>>;
const events: Awaited<ReturnType<typeof storedEvent>>[] = [];
beforeAll(async () => {
  f = await storedSecurity();
  for (let seq = 1n; seq <= BigInt(count); seq++) events.push(await storedEvent(f, seq));
});
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.useRealTimers(); });
it("keeps record reads and verification constant across distinct signed events", async () => {
  const reads = vi.spyOn(IDBObjectStore.prototype, "get");
  const chain = vi.spyOn(security, "verifyChain");
  const grant = vi.spyOn(security, "openGrant");
  const content = vi.spyOn(storage, "contentSecretOf");
  const measure = async (cached: boolean) => {
    reads.mockClear(); chain.mockClear(); grant.mockClear(); content.mockClear();
    const sdk = new DeviceSecurityClient(f.memberID, f.ed, async () => { throw new Error("unexpected network"); }, () => true,
      cached ? undefined : {
        // Original loader: structured clone + full chain verification each time.
        loadSecurity: async () => storage.verifyStoredSecurity(await rawSecurity("readonly", s => s.get("current"))),
        installSecurity: storage.installSecurity, touchSecurity: storage.touchSecurity,
      });
    const start = performance.now();
    for (const { envelope, seed } of events) await sdk.openHistory(envelope, seed);
    return { ms: Math.round(performance.now() - start),
      recordReads: reads.mock.calls.filter(([k]) => k === "current").length,
      revisionReads: reads.mock.calls.filter(([k]) => k === "record-revision").length,
      chainVerifications: chain.mock.calls.length, grantOpens: grant.mock.calls.length,
      epochKeyLookups: content.mock.calls.length };
  };
  const before = await measure(false);
  const after = await measure(true);
  if (process.env.RA_HISTORY_BENCHMARK) console.log(JSON.stringify({ events: events.length, before, after }));
  expect(before.recordReads).toBe(count);
  expect(before.chainVerifications).toBe(2 * count);
  expect(after.recordReads).toBe(1);
  expect(after.revisionReads).toBe(count);
  expect(after.chainVerifications).toBe(2);
  expect(after.grantOpens).toBe(1);
  expect(after.epochKeyLookups).toBe(1);
}, process.env.RA_HISTORY_BENCHMARK ? 60_000 : undefined);
