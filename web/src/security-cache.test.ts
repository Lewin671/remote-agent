import { IDBObjectStore } from "fake-indexeddb";
import { afterEach, beforeAll, expect, it, vi } from "vitest";
import * as security from "./device-security";
import * as store from "./device-security-store";
import { DeviceSecurityClient } from "./device-security-client";
import { securityFixture, storedSecurity, rawSecurity, storedEvent } from "./test/stored-security-fixture";
beforeAll(securityFixture);
import vectors from "../../internal/deviceauth/testdata/domain-v3.json";

afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.useRealTimers(); });

it("checks only the revision on hits and invalidates on every kind of committed record write", async () => {
  const f = await storedSecurity();
  const reads = vi.spyOn(IDBObjectStore.prototype, "get");
  const chain = vi.spyOn(security, "verifyChain");
  const first = await store.loadSecurity();
  expect(await store.loadSecurity()).toBe(first);
  expect(reads.mock.calls.filter(([k]) => k === "current")).toHaveLength(1);
  expect(chain).toHaveBeenCalledTimes(1);
  await store.touchSecurity(first.floor + 1n);
  const touched = await store.loadSecurity();
  expect(touched).not.toBe(first);
  expect(touched.floor).toBe(first.floor + 1n);
  await store.installSecurity(touched, [], [], f.lease);
  const leased = await store.loadSecurity();
  expect(leased).not.toBe(touched);
  const next = await security.parseState(security.unb64(vectors.renewal.state));
  vi.setSystemTime(Number(next.core.issuedAt));
  await store.initializeSecurity(f.pin, [...f.states, next], [undefined, undefined, undefined], f.memberID, f.x, true);
  expect((await store.loadSecurity()).states).toHaveLength(3);
  await store.forgetSecurity();
  await expect(store.loadSecurity()).rejects.toThrow();
  await store.initializeSecurity(f.pin, f.states, [undefined, undefined], f.memberID, f.x);
  expect(await store.loadSecurity()).not.toBe(first);
});

it("never reuses an unmarked record or a failed verification, and notices deletion from another tab", async () => {
  await storedSecurity();
  await store.loadSecurity();
  await rawSecurity("readwrite", s => s.delete("record-revision"));
  const a = await store.loadSecurity();
  expect(await store.loadSecurity()).not.toBe(a);
  const corrupt = structuredClone(a);
  corrupt.states[0].signature[0] ^= 1;
  await rawSecurity("readwrite", s => { s.put(crypto.randomUUID(), "record-revision"); return s.put(corrupt, "current"); });
  await expect(store.loadSecurity()).rejects.toThrow();
  await expect(store.loadSecurity()).rejects.toThrow();
  await rawSecurity("readwrite", s => { s.put(crypto.randomUUID(), "record-revision"); return s.delete("current"); });
  await expect(store.loadSecurity()).rejects.toThrow();
});

it("reuses client validation and content keys per exact verified record, without caching clock or liveness", async () => {
  const f = await storedSecurity();
  let active = true;
  const sdk = new DeviceSecurityClient(f.memberID, f.ed, async () => { throw new Error("no network"); }, () => active);
  const grant = vi.spyOn(security, "openGrant");
  const key = vi.spyOn(store, "contentSecretOf");
  const { envelope: env, seed } = await storedEvent(f);
  await sdk.openHistory(env, seed);
  await Promise.all([sdk.openHistory(env, seed), sdk.openHistory(env, seed)]);
  expect(grant).toHaveBeenCalledTimes(1);
  expect(key).toHaveBeenCalledTimes(1);
  const r = await store.loadSecurity();
  await store.touchSecurity(r.floor + 1n);
  await sdk.openHistory(env, seed);
  expect(grant).toHaveBeenCalledTimes(2);
  expect(key).toHaveBeenCalledTimes(2);
  vi.setSystemTime(Number(f.states.at(-1)!.core.issuedAt) - security.MAX_CLOCK_SKEW - 1);
  await expect(sdk.openHistory(env, seed)).rejects.toMatchObject({ code: "clock" });
  vi.setSystemTime(Number(f.lease.issuedAt));
  active = false;
  await expect(sdk.openHistory(env, seed)).rejects.toMatchObject({ code: "storage" });
});

it("refreshes a cached client's membership and keys after another tab installs removal", async () => {
  const f = await storedSecurity();
  const sdk = new DeviceSecurityClient(f.memberID, f.ed, async () => { throw new Error("no network"); });
  const { envelope, seed } = await storedEvent(f);
  expect((await sdk.verifiedRecord()).member).toBe(true);
  const original = await sdk.openHistory(envelope, seed);
  vi.setSystemTime(f.removal.now);
  await store.installSecurity(await store.loadSecurity(), f.removal.states, f.removal.proofs);
  expect((await sdk.verifiedRecord()).member).toBe(false);
  expect(await sdk.openHistory(envelope, seed)).toEqual(original);
  await expect(sdk.seal({ kind: 12, purpose: 12, machine: "", session: "", request: "prefs",
    seq: 1n, deadline: 0n, compression: 0 }, new Uint8Array())).rejects.toMatchObject({ code: "untrusted" });
});
