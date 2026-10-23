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

it("hashes immutable snapshots once without trusting mutable inputs or exposing cached bytes", async () => {
  const f = await storedSecurity();
  const source = structuredClone(f.states.at(-1)!);
  const snapshot = security.immutableState(source);
  const digest = vi.spyOn(crypto.subtle, "digest");
  const [a, b] = await Promise.all([security.stateHash(snapshot), security.stateHash(snapshot)]);
  expect(digest).toHaveBeenCalledTimes(source.core.members.length + 2);
  const expected = b.slice();
  a.fill(0);
  source.core.domain.fill(0);
  snapshot.core.domain.fill(0);
  snapshot.core.members[0].ed.fill(0);
  snapshot.grants[0].cipher.fill(0);
  expect(() => { snapshot.core.epoch = 99n; }).toThrow();
  expect(() => { snapshot.core.members.pop(); }).toThrow();
  expect(await security.stateHash(snapshot)).toEqual(expected);
  const mutable = structuredClone(f.states.at(-1)!);
  expect(await security.stateHash(mutable)).toEqual(expected);
  mutable.grants[0].cipher[0] ^= 1;
  expect(await security.stateHash(mutable)).not.toEqual(expected);
});

it("performs only the two envelope digests and one signature check per cached history item", async () => {
  const f = await storedSecurity();
  const sdk = new DeviceSecurityClient(f.memberID, f.ed, async () => { throw new Error("no network"); });
  const items = [];
  for (let seq = 1n; seq <= 5n; seq++) items.push(await storedEvent(f, seq));
  await sdk.openHistory(items[0].envelope, items[0].seed);
  const digest = vi.spyOn(crypto.subtle, "digest");
  const imports = vi.spyOn(crypto.subtle, "importKey");
  const verify = vi.spyOn(crypto.subtle, "verify");
  for (const item of items) await sdk.openHistory(item.envelope, item.seed);
  expect(digest).toHaveBeenCalledTimes(2 * items.length);
  expect(verify).toHaveBeenCalledTimes(items.length);
  expect(imports.mock.calls.filter(call => call[2] === "Ed25519")).toHaveLength(0);
});

it("checks the stored revision twice per replay batch while verifying every envelope", async () => {
  const f = await storedSecurity();
  const sdk = new DeviceSecurityClient(f.memberID, f.ed, async () => { throw new Error("no network"); });
  const items = [];
  for (let seq = 1n; seq <= 5n; seq++) items.push(await storedEvent(f, seq));
  await sdk.openHistory(items[0].envelope, items[0].seed);
  const transactions = vi.spyOn((await import("fake-indexeddb")).IDBDatabase.prototype, "transaction");
  const reads = vi.spyOn(IDBObjectStore.prototype, "get");
  const verify = vi.spyOn(crypto.subtle, "verify");
  const opened = await sdk.openHistoryBatch(items);
  expect(opened.map(r => r.status)).toEqual(items.map(() => "fulfilled"));
  expect(transactions).toHaveBeenCalledTimes(2);
  expect(reads.mock.calls.filter(([k]) => k === "record-revision")).toHaveLength(2);
  expect(verify).toHaveBeenCalledTimes(items.length);
  const corrupt = structuredClone(items[1]);
  corrupt.envelope.signature[0] ^= 1;
  const results = await sdk.openHistoryBatch([items[0], corrupt, items[2]]);
  expect(results.map(r => r.status)).toEqual(["fulfilled", "rejected", "fulfilled"]);
});

it.each(["floor", "lease", "removal", "delete", "corrupt"])(
  "rechecks the whole batch after another tab commits a %s change during replay", async change => {
    const f = await storedSecurity();
    const sdk = new DeviceSecurityClient(f.memberID, f.ed, async () => { throw new Error("no network"); });
    const item = await storedEvent(f);
    await sdk.openHistory(item.envelope, item.seed);
    const record = await store.loadSecurity();
    let wrote = false;
    const originalVerify = crypto.subtle.verify.bind(crypto.subtle);
    const verify = vi.spyOn(crypto.subtle, "verify").mockImplementation(async (...args) => {
      const valid = await originalVerify(...args);
      if (!wrote) {
        wrote = true;
        if (change === "floor") await store.touchSecurity(record.floor + 1n);
        if (change === "lease") await store.installSecurity(record, [], [], f.lease);
        if (change === "removal") {
          vi.setSystemTime(f.removal.now);
          await store.installSecurity(record, f.removal.states, f.removal.proofs);
        }
        if (change === "delete") await rawSecurity("readwrite", s => s.delete("current"));
        if (change === "corrupt") {
          const bad = structuredClone(record);
          bad.states[0].signature[0] ^= 1;
          await rawSecurity("readwrite", s => {
            s.put(crypto.randomUUID(), "record-revision");
            return s.put(bad, "current");
          });
        }
      }
      return valid;
    });
    const replay = sdk.openHistoryBatch([item, item]);
    if (change === "delete" || change === "corrupt") {
      await expect(replay).rejects.toThrow();
    } else {
      expect((await replay).map(r => r.status)).toEqual(["fulfilled", "fulfilled"]);
      // Revalidation also verifies state signatures. Count just envelopes.
      const message = (verify.mock.calls[0][3] as Uint8Array).slice();
      expect(verify.mock.calls.filter(call =>
        security.equal(new Uint8Array(call[3] as ArrayBuffer), message))).toHaveLength(4);
      if (change === "removal") expect((await sdk.verifiedRecord()).member).toBe(false);
    }
  },
);

it("keeps routing, state binding, and historical-purpose checks on every batch item", async () => {
  const f = await storedSecurity();
  const sdk = new DeviceSecurityClient(f.memberID, f.ed, async () => { throw new Error("no network"); });
  const item = await storedEvent(f);
  const wire = security.fromEnvelope(item.envelope);
  const header = security.parseHeader(wire.header);
  const forgedRoute = security.toEnvelope(await security.formCipher(
    (await store.loadSecurity()).secret, item.seed, { ...header, machine: f.memberID },
    new Uint8Array(), f.machineEd,
  ));
  const staleState = security.toEnvelope(await security.formCipher(
    (await store.loadSecurity()).secret, item.seed, { ...header, state: new Uint8Array(32).fill(3) },
    new Uint8Array(), f.machineEd,
  ));
  const command = security.toEnvelope(await security.formCipher(
    (await store.loadSecurity()).secret, undefined,
    { ...header, sender: f.memberID, kind: 5, purpose: 5, request: "synthetic-command", deadline: 1n },
    new Uint8Array(), f.ed,
  ));
  const results = await sdk.openHistoryBatch([
    item, { ...item, envelope: forgedRoute }, { ...item, envelope: staleState },
    { envelope: command }, item,
  ]);
  expect(results.map(r => r.status)).toEqual(["fulfilled", "rejected", "rejected", "rejected", "fulfilled"]);
  expect(results[1]).toMatchObject({ reason: { code: "untrusted" } });
  expect(results[2]).toMatchObject({ reason: { code: "stale" } });
});

it("does not cache a failed verification-key import", async () => {
  const f = await storedSecurity();
  const sdk = new DeviceSecurityClient(f.memberID, f.ed, async () => { throw new Error("no network"); });
  const item = await storedEvent(f);
  await sdk.verifiedRecord();
  const originalImport = crypto.subtle.importKey.bind(crypto.subtle);
  let failed = false;
  vi.spyOn(crypto.subtle, "importKey").mockImplementation((...args: Parameters<typeof originalImport>) => {
    if (args[2] === "Ed25519" && !failed) {
      failed = true;
      return Promise.reject(new Error("synthetic import failure"));
    }
    return originalImport(...args);
  });
  await expect(sdk.openHistory(item.envelope, item.seed)).rejects.toThrow("synthetic import failure");
  await expect(sdk.openHistory(item.envelope, item.seed)).resolves.toHaveProperty("plaintext");
});

it("does not cache a failed state-hash computation", async () => {
  const f = await storedSecurity();
  const state = security.immutableState(f.states.at(-1)!);
  vi.spyOn(crypto.subtle, "digest").mockRejectedValueOnce(new Error("synthetic digest failure"));
  await expect(security.stateHash(state)).rejects.toThrow("synthetic digest failure");
  await expect(security.stateHash(state)).resolves.toEqual(await security.stateHash(f.states.at(-1)!));
});

it.each(["clock", "inactive"])("rechecks %s before releasing a replay batch", async change => {
  const f = await storedSecurity();
  let active = true;
  const sdk = new DeviceSecurityClient(f.memberID, f.ed, async () => { throw new Error("no network"); }, () => active);
  const item = await storedEvent(f);
  await sdk.openHistory(item.envelope, item.seed);
  const originalVerify = crypto.subtle.verify.bind(crypto.subtle);
  vi.spyOn(crypto.subtle, "verify").mockImplementation(async (...args) => {
    const valid = await originalVerify(...args);
    if (change === "clock") vi.setSystemTime(Number(f.states.at(-1)!.core.issuedAt) - security.MAX_CLOCK_SKEW - 1);
    else active = false;
    return valid;
  });
  await expect(sdk.openHistoryBatch([item])).rejects.toMatchObject({
    code: change === "clock" ? "clock" : "storage",
  });
});

it("falls back to individual revision checks for unmarked snapshots", async () => {
  const f = await storedSecurity();
  await rawSecurity("readwrite", s => s.delete("record-revision"));
  const sdk = new DeviceSecurityClient(f.memberID, f.ed, async () => { throw new Error("no network"); });
  const item = await storedEvent(f);
  expect((await sdk.openHistoryBatch([item, item])).map(r => r.status)).toEqual(["fulfilled", "fulfilled"]);
});

it("syncs a newly observed epoch once before retrying the replay batch", async () => {
  const f = await storedSecurity();
  const renewal = f.removal.states[0];
  vi.setSystemTime(Number(renewal.core.issuedAt));
  const fetchStates = vi.fn(async (after: bigint) => ({
    states: after < renewal.core.epoch
      ? [{ state: await security.stateBytes(renewal), recovery: new Uint8Array() }] : [],
    headEpoch: renewal.core.epoch,
  }));
  const sdk = new DeviceSecurityClient(f.memberID, f.ed, fetchStates);
  const item = await storedEvent(f);
  const header = security.parseHeader(security.fromEnvelope(item.envelope).header);
  const secret = (await store.loadSecurity()).secret;
  const items = [];
  for (let seq = 1n; seq <= 5n; seq++) {
    items.push({ seed: item.seed, envelope: security.toEnvelope(await security.formCipher(
      secret, item.seed, { ...header, epoch: renewal.core.epoch, state: await security.stateHash(renewal), seq },
      new Uint8Array(), f.machineEd,
    )) });
  }
  const results = await sdk.openHistoryBatch(items);
  expect(results.map(r => r.status)).toEqual(items.map(() => "fulfilled"));
  expect(fetchStates).toHaveBeenCalledTimes(2); // One state page and the confirming empty page.
  expect((await sdk.verifiedRecord()).states.at(-1)!.core.epoch).toBe(renewal.core.epoch);
});

it("returns an error for every future-epoch item when sync cannot obtain its state", async () => {
  const f = await storedSecurity();
  const sdk = new DeviceSecurityClient(f.memberID, f.ed, async () => ({
    states: [], headEpoch: f.states.at(-1)!.core.epoch,
  }));
  const item = await storedEvent(f);
  const wire = security.fromEnvelope(item.envelope);
  const header = security.parseHeader(wire.header);
  wire.header = security.headerBytes({ ...header, epoch: header.epoch + 1n });
  const future = { ...item, envelope: security.toEnvelope(wire) };
  expect((await sdk.openHistoryBatch([future, future])).map(r => r.status)).toEqual(["rejected", "rejected"]);
});
