import { IDBFactory } from "fake-indexeddb";
import { beforeEach, afterEach, expect, it, vi } from "vitest";
import { create, toBinary } from "@bufbuild/protobuf";
import { concat, deviceID, fromHex } from "./crypto";
import { createAccountMaterial, acceptRecoveryPublicProfile } from "./account-public";
import { RecoveryCode } from "./recovery";
import { loadAccountPublicProfile, saveAccountPublicProfile, saveRegistration, loadRegistration, forgetAccount, loadAccount } from "./storage";
import { resumeRegistration } from "./remote";
import { DeviceRegistrationSchema, RegisterDeviceRequestSchema, RegisterDeviceResponseSchema, PairApprovalSchema, DeviceKind } from "./gen/remoteagent/v1/relay_pb";
import { buildRecord } from "./device-security-store";
import { parseState, parseRecoveryStatement, unb64, pinOf, stateHash } from "./device-security";
import { ed25519 } from "@noble/curves/ed25519.js";
import vectors from "./testdata/authority/vectors.json";
const server = "https://relay.example.com";
const pub = new Uint8Array(ed25519.getPublicKey(new Uint8Array(32).fill(0x61)));
const genesis = await parseState(unb64(vectors.States.genesis));
const pin = await pinOf(genesis);
beforeEach(() => {
  vi.stubGlobal("indexedDB", new IDBFactory());
  vi.stubGlobal("localStorage", { setItem: vi.fn() });
});
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); });
it("creation persists the validated account key separately and returns nonextractable device keys", async () => {
  const created = await createAccountMaterial(server);
  expect((await loadAccountPublicProfile(server, created.pin))?.accountPublicKey).toEqual(created.accountPublicKey);
  expect(await deviceID(created.accountPublicKey)).toBe(created.pin.account);
  expect(created.device.privateKey.extractable).toBe(false);
  expect(created.x.privateKey.extractable).toBe(false);
  expect(RecoveryCode.decode(created.recoveryCode)?.genesis).toEqual(created.pin.genesis.slice(0, 10));
  // No account, private draft or recovery seed is persisted by this core factory.
  expect(await loadAccount()).toBeUndefined();
  expect(await loadRegistration()).toBeUndefined();
});
it("scopes profiles by server and genesis, validates IDs and reads back after module reload", async () => {
  await expect(saveAccountPublicProfile(server, pin, new Uint8Array(32))).rejects.toThrow();
  expect(await loadAccountPublicProfile(server, pin)).toBeUndefined();
  await saveAccountPublicProfile(server, pin, pub);
  vi.resetModules();
  const store = await import("./storage");
  expect((await store.loadAccountPublicProfile(server, pin))?.accountPublicKey).toEqual(pub);
  expect(await store.loadAccountPublicProfile("https://other.example.com", pin)).toBeUndefined();
  expect(await store.loadAccountPublicProfile(server, { ...pin, genesis: new Uint8Array(32).fill(9) })).toBeUndefined();
});
it("recovery context checks the offline prefix, complete chain and local extension before saving", async () => {
  const names = ["genesis", "pairMachine", "pairClient", "remove", "rotateX", "veto", "recovery"] as const;
  const states = await Promise.all(names.map((n) => parseState(unb64(vectors.States[n]))));
  const proofs = states.map(() => undefined) as (Awaited<ReturnType<typeof parseRecoveryStatement>> | undefined)[];
  proofs[6] = await parseRecoveryStatement(unb64(vectors.Recovery));
  const receive = (key = pub, prefix = pin.genesis.slice(0, 10), chain = states, local = { pin, states: states.slice(0, 5) }) => acceptRecoveryPublicProfile(server, pin.account, key, chain, proofs, prefix, local);
  await expect(receive(new Uint8Array(32))).rejects.toThrow();
  await expect(receive(pub, new Uint8Array(10))).rejects.toThrow();
  const forged = structuredClone(states); forged[6].signature[0] ^= 1;
  await expect(receive(pub, pin.genesis.slice(0, 10), forged)).rejects.toThrow();
  await expect(receive(pub, pin.genesis.slice(0, 10), states, { pin: { ...pin, genesis: new Uint8Array(32).fill(1) }, states: states.slice(0, 1) })).rejects.toThrow();
  await expect(acceptRecoveryPublicProfile(server, pin.account, pub, states.slice(0, 4), proofs.slice(0, 4), pin.genesis.slice(0, 10), { pin, states: states.slice(0, 5) })).rejects.toThrow();
  const handoff = await parseState(unb64(vectors.States.handoff));
  await expect(receive(pub, pin.genesis.slice(0, 10), states, { pin, states: [...states.slice(0, 6), handoff] })).rejects.toThrow();
  expect(await loadAccountPublicProfile(server, pin)).toBeUndefined();
  expect(await acceptRecoveryPublicProfile(server, pin.account, pub, states, proofs, pin.genesis.slice(0, 10))).toEqual(pin);
  expect(await receive()).toEqual(pin);
  expect((await loadAccountPublicProfile(server, pin))?.accountPublicKey).toEqual(pub);
});
it("refuses corrupt public material on both load and replacement", async () => {
  await saveAccountPublicProfile(server, pin, pub);
  const db = await new Promise<IDBDatabase>((resolve) => {
    const request = indexedDB.open("remote-agent", 1);
    request.onsuccess = () => resolve(request.result);
  });
  await new Promise<void>((resolve) => {
    const tx = db.transaction("kv", "readwrite");
    const store = tx.objectStore("kv");
    const cursor = store.openCursor();
    cursor.onsuccess = () => {
      if (cursor.result) cursor.result.update({ ...cursor.result.value, accountPublicKey: "corrupt" });
    };
    tx.oncomplete = () => { db.close(); resolve(); };
  });
  await expect(loadAccountPublicProfile(server, pin)).rejects.toThrow();
  await expect(saveAccountPublicProfile(server, pin, pub)).rejects.toThrow();
});
it("pairing retry saves the account public key from the exact existing registration intent", async () => {
  // The saved identity below is the existing fixture member; this tests the
  // registration-intent boundary independently of the candidate pairing UI.
  const privateKey = await crypto.subtle.importKey("pkcs8", concat(fromHex("302e020100300506032b657004220420"), new Uint8Array(32).fill(0x11)), "Ed25519", false, ["sign"]);
  const x = await crypto.subtle.importKey("pkcs8", concat(fromHex("302e020100300506032b656e04220420"), new Uint8Array(32).fill(0x21)), "X25519", false, ["deriveBits"]);
  const security = await buildRecord(pin, [genesis], [undefined], genesis.core.authorityID, x);
  const registration = toBinary(DeviceRegistrationSchema, create(DeviceRegistrationSchema, { accountPublicKey: pub, devicePublicKey: genesis.core.members[0].ed, kind: DeviceKind.CLIENT }));
  const request = toBinary(RegisterDeviceRequestSchema, create(RegisterDeviceRequestSchema, { registration, approval: toBinary(PairApprovalSchema, create(PairApprovalSchema, { pairingId: "0123456789abcdef0123456789abcdef" })) }));
  const account = { server, deviceId: genesis.core.authorityID, devicePrivate: privateKey };
  await saveRegistration({ account, security, request, replace: false });
  const fetch = vi.fn(async () => { throw new Error("synthetic network interruption"); });
  vi.stubGlobal("fetch", fetch);
  await expect(resumeRegistration()).rejects.toThrow();
  expect((await loadAccountPublicProfile(server, pin))?.accountPublicKey).toEqual(pub);
  expect((await loadRegistration())?.request).toEqual(request);
  vi.stubGlobal("fetch", async () => new Response(toBinary(RegisterDeviceResponseSchema, create(RegisterDeviceResponseSchema, { deviceId: account.deviceId })), { headers: { "Content-Type": "application/proto" } }));
  await resumeRegistration();
  expect(await loadRegistration()).toBeUndefined();
  expect((await loadAccountPublicProfile(server, pin))?.accountPublicKey).toEqual(pub);
});
it("rejects a stored registration's mismatched account key before contacting the server", async () => {
  const created = await createAccountMaterial(server);
  const security = await buildRecord(created.pin, [created.genesis], [undefined], created.genesis.core.authorityID, created.x.privateKey);
  const request = toBinary(RegisterDeviceRequestSchema, create(RegisterDeviceRequestSchema, { registration: toBinary(DeviceRegistrationSchema, create(DeviceRegistrationSchema, { accountPublicKey: new Uint8Array(32) })) }));
  await saveRegistration({ account: { server, deviceId: created.genesis.core.authorityID, devicePrivate: created.device.privateKey }, security, request, replace: false });
  const fetch = vi.fn(); vi.stubGlobal("fetch", fetch);
  await expect(resumeRegistration()).rejects.toThrow();
  expect(fetch).not.toHaveBeenCalled();
});
it("forgetting the account deletes the independent public profiles", async () => {
  await saveAccountPublicProfile(server, pin, pub);
  await forgetAccount();
  expect(await loadAccountPublicProfile(server, pin)).toBeUndefined();
  expect(await stateHash(genesis)).toEqual(pin.genesis);
});
