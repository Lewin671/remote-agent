// Reproducible Node/fake-indexeddb microbenchmark. No relay/network latency.
// Run alone: pnpm test src/history-benchmark.test.ts --no-file-parallelism
import { IDBObjectStore } from "fake-indexeddb";
import { afterEach, expect, it, vi } from "vitest";
import { DeviceSecurityClient } from "./device-security-client";
import * as security from "./device-security";
import * as storage from "./device-security-store";
import { storedSecurity, storedEvent, rawSecurity } from "./test/stored-security-fixture";

afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.useRealTimers(); });
it("measures opening 300 signed events with and without record reuse", async () => {
  const f = await storedSecurity();
  const events: Awaited<ReturnType<typeof storedEvent>>[] = [];
  for (let seq = 1n; seq <= 300n; seq++) events.push(await storedEvent(f, seq));
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
  console.log(JSON.stringify({ events: events.length, before, after }));
  expect(before.recordReads).toBe(300);
  expect(before.chainVerifications).toBe(600);
  expect(after.recordReads).toBe(1);
  expect(after.revisionReads).toBe(300);
  expect(after.chainVerifications).toBe(2);
  expect(after.grantOpens).toBe(1);
  expect(after.epochKeyLookups).toBe(1);
}, 60_000);
