import { IDBFactory } from "fake-indexeddb";
import { afterAll, afterEach, beforeAll, beforeEach, expect, it, vi } from "vitest";
import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { AuthorityServer } from "./test/authority-server";
import { AuthoritySetup, authorityServices, authorityStartup } from "./authority-setup";
import { AuthorityLifecycle, readSecurityOperation, isAuthority, operation, claimSecurityOperation, saveSecurityOperation } from "./security-operation";
import { AuthorityClient, handoffCandidates } from "./authority-client";
import { AuthorityPairing } from "./authority-pairing";
import { loadAccount, type StoredAccount } from "./storage";
import * as securityStore from "./device-security-store";
import * as accountStore from "./storage";
import { loadSecurity, securitySetupStatus } from "./device-security-store";
import { DeviceSecurityClient } from "./device-security-client";
import { signLogin, deviceID, newDeviceKey, openMessage, importSessionKey } from "./crypto";
import { newSecurityKey, pairDomainCommit, equal, stateBytes, stateHash, leaseBytes, signLease, Purpose, parseLease } from "./device-security";
import { derivePair, newPairKey, pairDomainTranscript } from "./pair";
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { Authority } from "./authority";
import { PairingService, DeviceKind, PutSecurityStateRequestSchema, DeviceRegistrationSchema, MachineService } from "./gen/remoteagent/v1/relay_pb";
import { SessionSchema } from "./gen/remoteagent/v1/local_pb";
import { MachineMetaSchema, PairingPayloadSchema } from "./gen/remoteagent/v1/remote_pb";
import { PayloadKind } from "./gen/remoteagent/v1/envelope_pb";
import { PROTOCOL_VERSION } from "./protocol";
import { Remote, joinWithApproval } from "./remote";
import { RecoveryCode } from "./recovery";

let built: ReturnType<typeof AuthorityServer.build>;
let server: AuthorityServer;
let visible = true;
let setup: AuthoritySetup;
let local: IDBFactory;
const lifecycles: AuthorityLifecycle[] = [];
function lifecycle() { const l = new AuthorityLifecycle(() => visible); lifecycles.push(l); return l; }
function locks() {
  let tail = Promise.resolve();
  return { request: (_name: string, _options: object, work: () => Promise<unknown>) => {
    const next = tail.then(work); tail = next.then(() => undefined, () => undefined); return next;
  } };
}
beforeAll(() => { built = AuthorityServer.build(); }, 120000);
afterAll(() => built.cleanup());
beforeEach(async () => {
  local = new IDBFactory(); visible = true;
  vi.stubGlobal("indexedDB", local);
  vi.stubGlobal("navigator", { locks: locks() });
  vi.stubGlobal("localStorage", { setItem: vi.fn(), getItem: () => null });
  setup = new AuthoritySetup(lifecycle());
  server = await AuthorityServer.start(built.binary);
}, 30000);
afterEach(async () => { for (const l of lifecycles.splice(0)) l.dispose(); await server?.close(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });
async function createAccount(token = "test-setup") {
  const prepared = await setup.prepareCreation(server.url);
  await setup.continueCreation(token);
  await setup.confirmCreationCodeSaved();
  return { account: (await loadAccount())!, code: prepared.code! };
}
async function clients(account: StoredAccount) {
  const services = authorityServices(account.server);
  const { challenge } = await services.auth.getChallenge({ deviceId: account.deviceId });
  const { token } = await services.auth.login({ deviceId: account.deviceId, challenge, signature: await signLogin(account.devicePrivate, challenge), protocolVersion: PROTOCOL_VERSION });
  const api = services.client(token);
  const pairing = createClient(PairingService, createConnectTransport({ baseUrl: account.server, useBinaryFormat: true, interceptors: [(next) => (req) => { req.header.set("Authorization", "Bearer " + token); return next(req); }] }));
  const security = new DeviceSecurityClient(account.deviceId, account.devicePrivate, (afterEpoch, limit) => api.getSecurityStates({ afterEpoch, limit }), () => true, {
    loadSecurity: () => securityStore.loadSecurity(),
    installSecurity: (...args) => securityStore.installSecurity(...args),
    touchSecurity: (...args) => securityStore.touchSecurity(...args),
  });
  const authority = new AuthorityClient(account, security, api, pairing, async (publicKey, check, persist, request) => { if (!request) await persist(publicKey); check(); return true; }, lifecycle());
  return { api, pairing, security, authority, token };
}
function dropAck(path: string) {
  const fetch = globalThis.fetch;
  let dropped = false;
  return vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const response = await fetch(input, init);
    const url = input instanceof Request ? input.url : String(input);
    if (!dropped && url.endsWith("/" + path) && response.ok) { dropped = true; throw new Error("injected lost ack"); }
    return response;
  });
}
it("allows explicit exit from unsigned and uncertain signed offers but retains delivered offers and confirmed rotations", async () => {
  const { account } = await createAccount();
  const c = await clients(account), record = await loadSecurity();
  let op = operation("offer", account.server, account.deviceId, record.stateHash, { recoveryEd: new Uint8Array(32).fill(7) });
  await claimSecurityOperation(op, record.stateHash);
  op = await saveSecurityOperation(op, { ...op, codeSaved: true });
  await c.authority.discardUnpublishedCode();
  expect(await readSecurityOperation()).toBeUndefined();
  await claimSecurityOperation(op, record.stateHash);
  op = await saveSecurityOperation(op, { ...op, signed: new Uint8Array([1]) });
  await c.authority.discardUnpublishedCode();
  expect(await readSecurityOperation()).toBeUndefined();
  await claimSecurityOperation(op, record.stateHash);
  op = await saveSecurityOperation(op, { ...op, phase: "DELIVERED" });
  await expect(c.authority.discardUnpublishedCode()).rejects.toThrow();
  expect((await readSecurityOperation())!.signed).toEqual(new Uint8Array([1]));
  expect((await readSecurityOperation())!.phase).toBe("DELIVERED");
  await securityStore.updateSecurityEntry("pending", () => undefined);
  const rotation = operation("state", account.server, account.deviceId, record.stateHash, { action: "recovery" });
  await claimSecurityOperation(rotation, record.stateHash);
  await saveSecurityOperation(rotation, { ...rotation, codeSaved: true });
  await expect(c.authority.discardUnpublishedCode()).rejects.toThrow();
  expect((await readSecurityOperation())!.codeSaved).toBe(true);
});
it("continues the exact creation draft after registration and pin acknowledgements are lost; token can be corrected", async () => {
  expect(await securitySetupStatus()).toBe("missing");
  const { code } = await setup.prepareCreation(server.url);
  const original = await readSecurityOperation();
  await expect(setup.continueCreation("wrong-token")).rejects.toThrow();
  expect((await readSecurityOperation())!.signed).toEqual(original!.signed);
  const ack = dropAck("RegisterDevice");
  await expect(setup.continueCreation("test-setup")).rejects.toThrow(); ack.mockRestore();
  setup = new AuthoritySetup(lifecycle());
  expect(await setup.prepareCreation(server.url)).toMatchObject({ code: undefined, resumed: true });
  const pin = dropAck("PinSecurityDomain");
  await expect(setup.continueCreation()).rejects.toThrow(); pin.mockRestore();
  await setup.continueCreation();
  expect((await authorityStartup()).kind).toBe("create");
  expect((await readSecurityOperation())!.signed).toEqual(original!.signed);
  expect(await loadAccount()).toBeUndefined();
  await setup.confirmCreationCodeSaved();
  expect(await readSecurityOperation()).toBeUndefined();
  expect(isAuthority(await loadSecurity())).toBe(true);
  expect(RecoveryCode.decode(code!)!.genesis).toEqual((await loadSecurity()).pin.genesis.slice(0, 10));
});
it("creates on an open relay with real PoW while retaining its signed registration", async () => {
  await server.close(); server = await AuthorityServer.start(built.binary, true);
  await createAccount("");
  expect(isAuthority(await loadSecurity())).toBe(true);
  expect((await loadSecurity()).lease).toBeDefined();
});
it("replays an accepted state after a lost ack, preserves unchanged-head recovery hints and renews without growing the chain", async () => {
  const { account } = await createAccount();
  const c = await clients(account);
  const old = await loadSecurity();
  const prepared = await c.authority.prepareRecovery();
  expect(prepared.code).toBeTruthy();
  const ack = dropAck("PutSecurityState");
  await expect(c.authority.publishRecovery()).rejects.toThrow(); ack.mockRestore();
  const pending = await readSecurityOperation();
  expect(pending!.signed).toBeDefined();
  await c.authority.resumeSecurityOperation();
  expect(await readSecurityOperation()).toBeUndefined();
  const next = await loadSecurity();
  expect(next.states).toHaveLength(old.states.length + 1);
  expect(next.states.at(-1)!.core.contentEpoch).toBe(old.states.at(-1)!.core.contentEpoch);
  const states = next.states.length;
  await c.authority.renewLeaseNow();
  expect((await loadSecurity()).states).toHaveLength(states);
  expect(c.security.recoveryStatusStale).toBe(false);
  const fetch = vi.spyOn(c.api, "getSecurityStates").mockRejectedValueOnce(new Error("offline"));
  await expect(c.security.sync()).rejects.toThrow();
  expect(c.security.recoveryStatusStale).toBe(true); fetch.mockRestore();
});

async function pairCandidate(c: Awaited<ReturnType<typeof clients>>, kind = DeviceKind.CLIENT, replacesDeviceId = "", loseApprovalAck = false) {
  const initiation = await AuthorityPairing.start(c.authority, c.pairing, "https://app.example.com", replacesDeviceId);
  expect(initiation.code).toMatch(/^\d{8}$/);
  const link = new URL(initiation.url); expect(link.hash).toContain("c=");
  const api = createClient(PairingService, createConnectTransport({ baseUrl: server.url, useBinaryFormat: true }));
  const device = await newDeviceKey(), x = await newSecurityKey(), key = newPairKey();
  const ed = new Uint8Array(await crypto.subtle.exportKey("raw", device.publicKey));
  const xp = new Uint8Array(await crypto.subtle.exportKey("raw", x.publicKey));
  const joined = await api.joinPairing({ code: initiation.code, joinerPublic: key.pub, joinerKind: kind, devicePublicKey: ed, deviceHpkePublicKey: xp, protocolVersion: PROTOCOL_VERSION });
  const sas = await initiation.joined(); expect(sas!.emoji).toHaveLength(4);
  const status = await api.getPairing({ pairingId: joined.pairingId, candidateToken: joined.candidateToken });
  expect(await pairDomainCommit(new Uint8Array(status.initiatorPublic), new Uint8Array(status.trust))).toEqual(new URLSearchParams(link.hash.split("?")[1]).has("c") ? joined.commit : null);
  const secrets = await derivePair(key, status.initiatorPublic, await pairDomainTranscript(joined.pairingId, status.initiatorPublic, key.pub, ed, xp, kind, status.trust));
  expect(secrets.emoji).toEqual(sas!.emoji);
  await expect(initiation.approve(false)).rejects.toThrow();
  if (loseApprovalAck) {
    const ack = dropAck("ApprovePairing");
    await expect(initiation.approve(true)).rejects.toThrow(); ack.mockRestore();
    const saved = await readSecurityOperation();
    await initiation.approve(true);
    expect((await readSecurityOperation())!.delivery).toEqual(saved!.delivery);
  } else await initiation.approve(true);
  const pending = await readSecurityOperation(); expect(pending!.phase).toBe("PUBLISHED");
  expect(pending!.delivery).toBeDefined();
  const approved = await api.getPairing({ pairingId: joined.pairingId, candidateToken: joined.candidateToken });
  const { msg } = await openMessage(await importSessionKey(new Uint8Array(secrets.key)), approved.payload, { kind: PayloadKind.PAIRING, requestId: joined.pairingId }, PairingPayloadSchema);
  return { initiation, api, joined, device, x, msg, ed };
}
async function enrolledClient() {
  const { account } = await createAccount(), c = await clients(account);
  const candidate = await pairCandidate(c);
  const admin = await loadSecurity();
  const chain = await securityStore.parseChain(candidate.msg.securityStates);
  const id = await deviceID(candidate.ed);
  const record = await securityStore.buildRecord(admin.pin, chain.states, chain.proofs, id, candidate.x.privateKey);
  record.lease = parseLease(new Uint8Array(candidate.msg.lease));
  const incomingDB = new IDBFactory(); vi.stubGlobal("indexedDB", incomingDB);
  const incoming = await joinWithApproval(new Uint8Array(candidate.msg.accountPublicKey), server.url,
    { device: candidate.device, pairingId: candidate.joined.pairingId, approval: candidate.msg.approval, approvalSignature: candidate.msg.approvalSignature }, undefined, record);
  await candidate.api.getPairing({ pairingId: candidate.joined.pairingId, candidateToken: candidate.joined.candidateToken, received: true });
  const target = await clients(incoming);
  vi.stubGlobal("indexedDB", local);
  await candidate.initiation.finishPairingOperation();
  vi.stubGlobal("indexedDB", incomingDB);
  return { c, target, incoming, incomingDB };
}
it("signs a confirmed handoff offer against the current head after an unrelated transition", async () => {
  const { c, target, incoming, incomingDB } = await enrolledClient();
  vi.stubGlobal("document", { visibilityState: "visible" });
  class OfferRemote extends Remote { constructor(a: StoredAccount) { super(a, undefined); } }
  const remote = new OfferRemote(incoming);
  (remote as unknown as { token: string }).token = target.token;
  try {
    await remote.authority.prepareHandoffCode();
    vi.stubGlobal("indexedDB", local);
    await c.authority.prepareRecovery(); await c.authority.publishRecovery();
    const head = (await loadSecurity()).stateHash;
    vi.stubGlobal("indexedDB", incomingDB);
    await target.security.sync(); // normal foreground sync has installed the new head
    await remote.authority.offerHandoff();
    expect(await readSecurityOperation()).toBeUndefined();
    expect((await loadSecurity()).stateHash).toEqual(head);
    expect((await target.api.listDevices({})).devices.find((d) => d.id === incoming.deviceId)!.metaRevision).toBeGreaterThan(0n);
  } finally { remote.stop(); }
});
it("allows explicit abandonment of a confirmed offer whose publication is uncertain", async () => {
  const { target, incoming } = await enrolledClient();
  const authority = new AuthorityClient(incoming, target.security, target.api, target.pairing, async (key, _check, persist) => {
    await persist(key); throw new Error("lost metadata response");
  }, lifecycle());
  await authority.prepareHandoffCode();
  await expect(authority.offerHandoff()).rejects.toThrow("lost metadata response");
  expect((await readSecurityOperation())!.codeSaved).toBe(true);
  expect((await readSecurityOperation())!.signed).toBeDefined();
  await authority.discardUnpublishedCode();
  expect(await readSecurityOperation()).toBeUndefined();
});
it("renews a new authority head while preserving an unrelated handoff offer", async () => {
  const { c, target, incomingDB } = await enrolledClient();
  const a = await Authority.newRecovery();
  const r = await loadSecurity();
  const pending = operation("offer", server.url, r.memberID, r.stateHash, { recoveryEd: (await Authority.newRecovery()).publicKey });
  pending.codeSaved = true;
  await claimSecurityOperation(pending, r.stateHash);
  vi.stubGlobal("indexedDB", local);
  await c.authority.handoffAuthority(r.memberID, [{ id: r.memberID, recoveryEd: a.publicKey }]); a.seed.fill(0);
  vi.stubGlobal("indexedDB", incomingDB);
  await target.security.sync();
  const put = target.api.putSecurityState.bind(target.api);
  const lost = vi.spyOn(target.api, "putSecurityState").mockImplementationOnce(async (request) => {
    await put(request); throw new Error("lost independent lease response");
  });
  await expect(target.authority.renewLeaseIfNeeded()).rejects.toThrow("lost independent lease response");
  const retry = (await readSecurityOperation())!.retry!;
  expect(retry).toBeDefined(); lost.mockRestore();
  await target.authority.renewLeaseIfNeeded();
  const adopted = await loadSecurity();
  expect(leaseBytes(adopted.lease!)).toEqual(fromBinary(PutSecurityStateRequestSchema, retry).lease);
  expect(isAuthority(adopted)).toBe(true);
  expect(adopted.lease!.head).toEqual(adopted.stateHash);
  expect((await readSecurityOperation())!.id).toBe(pending.id);
  expect((await readSecurityOperation())!.local).toEqual(pending.local);
});
it("initiates real pairing and retains delivery until candidate registration/receipt, then hands off to a platform-neutral Client", async () => {
  const { account } = await createAccount();
  const c = await clients(account), candidate = await pairCandidate(c, DeviceKind.CLIENT, "", true);
  await c.authority.resumeSecurityOperation();
  expect(await readSecurityOperation()).toBeDefined();
  const adminRecord = await loadSecurity();
  const { parseChain, buildRecord } = await import("./device-security-store");
  const chain = await parseChain(candidate.msg.securityStates);
  const candidateId = await deviceID(candidate.ed);
  const record = await buildRecord(adminRecord.pin, chain.states, chain.proofs, candidateId, candidate.x.privateKey);
  const { parseLease } = await import("./device-security"); record.lease = parseLease(new Uint8Array(candidate.msg.lease));
  const incomingDB = new IDBFactory(); vi.stubGlobal("indexedDB", incomingDB);
  const incoming = await joinWithApproval(new Uint8Array(candidate.msg.accountPublicKey), server.url, { device: candidate.device, pairingId: candidate.joined.pairingId, approval: candidate.msg.approval, approvalSignature: candidate.msg.approvalSignature }, undefined, record);
  const receipt = dropAck("GetPairing");
  await expect(candidate.api.getPairing({ pairingId: candidate.joined.pairingId, candidateToken: candidate.joined.candidateToken, received: true })).rejects.toThrow();
  receipt.mockRestore();
  const target = await clients(incoming);
  vi.stubGlobal("document", { visibilityState: "visible" });
  class OfferRemote extends Remote { constructor(a: StoredAccount) { super(a, undefined); } }
  const targetRemote = new OfferRemote(incoming);
  (targetRemote as unknown as { token: string }).token = target.token;
  const code = await targetRemote.authority.prepareHandoffCode(); expect(code.code).toBeTruthy();
  const offerAck = dropAck("PutDeviceMeta");
  await expect(targetRemote.authority.offerHandoff()).rejects.toThrow(); offerAck.mockRestore();
  const offerBytes = (await readSecurityOperation())!.signed; expect(offerBytes).toBeDefined();
  await targetRemote.authority.offerHandoff();
  expect(await readSecurityOperation()).toBeUndefined();
  targetRemote.stop();
  // The real Remote writer above signed and durably saved the public offer;
  // the coordinator below consumes the corresponding verified public key.
  const decoded = RecoveryCode.decode(code.code)!;
  const { ed25519 } = await import("@noble/curves/ed25519.js");
  const recoveryEd = new Uint8Array(ed25519.getPublicKey(decoded.seed)); decoded.seed.fill(0);
  vi.stubGlobal("indexedDB", local);
  await candidate.initiation.finishPairingOperation();
  expect(await readSecurityOperation()).toBeUndefined();
  const candidates = handoffCandidates(await loadSecurity(), [{ id: candidateId, recoveryEd }, { id: account.deviceId, recoveryEd }]);
  expect(candidates.map((o) => o.id)).toEqual([candidateId]);
  await c.authority.handoffAuthority(candidateId, candidates);
  const retired = await loadSecurity();
  expect(retired.member).toBe(false); expect(retired.states.at(-1)!.prevSecret.length).toBeGreaterThan(0);
  // The stream/login can stop as soon as the handoff revokes the old token.
  // Reconcile the exact installed handoff in the foreground without logging
  // that device in again or accepting a denial as proof of removal.
  const retiredRemote = new OfferRemote(account);
  retiredRemote.stop();
  const login = vi.spyOn(globalThis, "fetch");
  await retiredRemote.resumeSecurityOperation();
  expect(retiredRemote.removalVerified).toBe(true);
  expect(retiredRemote.removed).toBe(true);
  expect(login).not.toHaveBeenCalled();
  await expect(retiredRemote.authority.lifecycle.run(async () => {})).rejects.toThrow("页面已暂停");
  vi.stubGlobal("document", { visibilityState: "hidden" });
  await expect(retiredRemote.resumeSecurityOperation()).rejects.toThrow("页面已暂停");
  vi.stubGlobal("document", { visibilityState: "visible" });
  login.mockRestore();
  vi.stubGlobal("indexedDB", incomingDB);
  await target.security.sync();
  await target.authority.renewLeaseIfNeeded();
  expect(isAuthority(await loadSecurity())).toBe(true);
  expect(equal((await loadSecurity()).lease!.head, (await loadSecurity()).stateHash)).toBe(true);
  // Public material can be recovered with challenge/context only.
  const submit = vi.spyOn(globalThis, "fetch");
  await setup.completeAccountPublicProfile(server.url, code.code);
  expect(submit.mock.calls.some(([input]) => String(input instanceof Request ? input.url : input).endsWith("/SubmitRecovery"))).toBe(false);
});
it("submits exact emergency recovery, rejects early completion, observes unchanged-head hints, and adopts after debug advancement of 48h", async () => {
  const { account, code } = await createAccount();
  const oldDB = local, c = await clients(account);
  const recoveredDB = new IDBFactory(); vi.stubGlobal("indexedDB", recoveredDB);
  const recovery = new AuthoritySetup(lifecycle());
  const prepared = await recovery.prepareEmergencyRecovery(server.url, code);
  expect(prepared.code).toBeTruthy();
  const pending = await readSecurityOperation();
  expect(JSON.stringify(pending, (_k, v) => typeof v === "bigint" ? v.toString() : v)).not.toContain(code);
  const ack = dropAck("SubmitRecovery");
  await expect(recovery.submitRecovery()).rejects.toThrow(); ack.mockRestore();
  const status = await recovery.resumeRecovery();
  expect(status.phase).toBe(1); expect((await readSecurityOperation())!.signed).toEqual(pending!.signed);
  await expect(recovery.completeRecovery()).rejects.toThrow("48");
  vi.stubGlobal("indexedDB", oldDB); await c.security.sync();
  expect(c.security.recoveryPending).toBe(true); expect(c.security.recoveryReadyMs).toBe(status.readyMs);
  await c.authority.renewLeaseNow(); // Renewal cannot veto.
  vi.stubGlobal("indexedDB", recoveredDB);
  expect((await recovery.resumeRecovery()).phase).toBe(1);
  await recovery.confirmRecoveryCodeSaved();
  await server.ready(status.recoveryId);
  const complete = dropAck("CompleteRecovery");
  await expect(recovery.completeRecovery()).rejects.toThrow(); complete.mockRestore();
  expect((await recovery.resumeRecovery()).phase).toBe(4);
  expect(await recovery.completeRecovery()).toEqual({ needsRecoveryRotation: false });
  expect(await readSecurityOperation()).toBeUndefined();
  const adopted = await loadSecurity(); expect(isAuthority(adopted)).toBe(true);
  expect(adopted.states.at(-1)!.prevSecret).toHaveLength(0);
  const newClient = await clients((await loadAccount())!); await newClient.authority.renewLeaseIfNeeded();
  expect((await loadSecurity()).lease!.head).toEqual(adopted.stateHash);
});
it("keeps a vetoed recovery until an explicit discard and permits a new request with the current code", async () => {
  const { account, code } = await createAccount(); const c = await clients(account);
  const recoveryDB = new IDBFactory(); vi.stubGlobal("indexedDB", recoveryDB);
  const recovery = new AuthoritySetup(lifecycle());
  await recovery.prepareEmergencyRecovery(server.url, code); await recovery.resumeRecovery();
  await expect(recovery.discardRejectedRecovery()).rejects.toThrow();
  vi.stubGlobal("indexedDB", local);
  const replacement = await c.authority.prepareRecovery(); await c.authority.publishRecovery();
  vi.stubGlobal("indexedDB", recoveryDB);
  expect((await recovery.resumeRecovery()).phase).toBe(3);
  await expect(recovery.prepareEmergencyRecovery(server.url, replacement.code)).rejects.toThrow();
  await recovery.discardRejectedRecovery();
  expect(await readSecurityOperation()).toBeUndefined();
  await recovery.prepareEmergencyRecovery(server.url, replacement.code);
  expect((await recovery.resumeRecovery()).phase).toBe(1);
});

it("replays immutable state bytes after more than 72h, refreshing only its independent lease", async () => {
  // Prepare a genesis from the past, then publish its first lease at real
  // server time. The pending successor below models an offline old request.
  const realNow = Date.now();
  const clock = vi.spyOn(Date, "now").mockReturnValue(realNow - 74 * 3600000);
  await setup.prepareCreation(server.url); clock.mockRestore();
  await setup.continueCreation("test-setup"); await setup.confirmCreationCodeSaved();
  const account = (await loadAccount())!, c = await clients(account), r = await loadSecurity();
  const oldTime = BigInt(realNow - 73 * 3600000), fresh = await Authority.newRecovery();
  const target = await Authority.veto(r.states[0], fresh.publicKey, r.secret, account.devicePrivate, oldTime);
  fresh.seed.fill(0);
  const oldLease = await signLease(target, account.devicePrivate, oldTime);
  const bytes = toBinary(PutSecurityStateRequestSchema, create(PutSecurityStateRequestSchema, { states: [{ state: await stateBytes(target) }], lease: leaseBytes(oldLease) }));
  let pending = operation("state", server.url, account.deviceId, r.stateHash, { action: "recovery" });
  await claimSecurityOperation(pending, r.stateHash);
  pending = await saveSecurityOperation(pending, { ...pending, signed: bytes, codeSaved: true });
  const calls = vi.spyOn(c.api, "putSecurityState");
  await c.authority.resumeSecurityOperation();
  const request = calls.mock.calls[0][0];
  expect(request.states?.[0]?.state).toEqual(fromBinary(PutSecurityStateRequestSchema, bytes).states[0].state);
  expect(request.lease).not.toEqual(leaseBytes(oldLease));
  expect((await loadSecurity()).stateHash).toEqual(await stateHash(target));
});
it("keeps unconfirmed recovery codes in memory while another tab renews the lease", async () => {
  const { account } = await createAccount(), c = await clients(account);
  const before = await loadSecurity();
  await c.authority.prepareRecovery();
  expect(await readSecurityOperation()).toBeUndefined();
  const otherTab = await clients(account);
  await otherTab.authority.renewLeaseNow();
  expect((await loadSecurity()).lease!.issuedAt).toBeGreaterThanOrEqual(before.lease!.issuedAt);
  expect((await loadSecurity()).states).toHaveLength(before.states.length);
  expect(await readSecurityOperation()).toBeUndefined();
  await c.authority.publishRecovery();
  expect((await loadSecurity()).states).toHaveLength(before.states.length + 1);
});
it("keeps unconfirmed handoff codes out of the durable slot and loses them on restart", async () => {
  const { target, incoming } = await enrolledClient();
  await target.authority.prepareHandoffCode();
  expect(await readSecurityOperation()).toBeUndefined();
  const restarted = (await clients(incoming)).authority;
  await expect(restarted.offerHandoff()).rejects.toThrow();
  expect(await readSecurityOperation()).toBeUndefined();
  await target.authority.offerHandoff();
  expect(await readSecurityOperation()).toBeUndefined();
});
it("stops when another verified successor changed the parent and does not sign a replacement", async () => {
  const { account } = await createAccount(), c = await clients(account);
  await c.authority.prepareRecovery();
  expect(await readSecurityOperation()).toBeUndefined();
  const record = await loadSecurity(), key = await Authority.newRecovery();
  const other = await Authority.veto(record.states.at(-1)!, key.publicKey, record.secret, account.devicePrivate, BigInt(Date.now()));
  key.seed.fill(0);
  await c.api.putSecurityState({ states: [{ state: await stateBytes(other) }], lease: leaseBytes(await signLease(other, account.devicePrivate, BigInt(Date.now()))) });
  const calls = vi.spyOn(c.api, "putSecurityState");
  await expect(c.authority.publishRecovery()).rejects.toThrow("conflict");
  expect(calls).not.toHaveBeenCalled();
  expect(await readSecurityOperation()).toBeUndefined();
});
it("keeps lease signing monotonic after a wall-clock rollback and refuses ordinary members", async () => {
  const { account } = await createAccount(), c = await clients(account);
  const old = (await loadSecurity()).lease!;
  const clock = vi.spyOn(Date, "now").mockReturnValue(Date.now() - 60000);
  await c.authority.renewLeaseNow(); clock.mockRestore();
  expect((await loadSecurity()).lease!.issuedAt).toBeGreaterThanOrEqual(old.issuedAt);
});
it.each(["before", "after"] as const)("recovers creation after every durable write and RPC (%s)", async (side) => {
  // Discover the boundaries on the real flow, then crash once at every one.
  // Signature material must be durable before any remote mutation.
  let events = 0, failure = -1, fired = false;
  const step = async <T>(work: () => Promise<T>): Promise<T> => {
    const at = events++;
    if (at === failure && side === "before") { fired = true; throw new Error("injected crash"); }
    const result = await work();
    if (at === failure && side === "after") { fired = true; throw new Error("injected crash"); }
    return result;
  };
  const update = securityStore.updateSecurityEntry;
  const initialize = securityStore.initializeSecurity;
  const profile = accountStore.saveAccountPublicProfile;
  const account = accountStore.saveAccount;
  const fetch = globalThis.fetch;
  const spies = [
    vi.spyOn(securityStore, "updateSecurityEntry").mockImplementation((...args) => step(() => update(...args))),
    vi.spyOn(securityStore, "initializeSecurity").mockImplementation((...args) => step(() => initialize(...args))),
    vi.spyOn(accountStore, "saveAccountPublicProfile").mockImplementation((...args) => step(() => profile(...args))),
    vi.spyOn(accountStore, "saveAccount").mockImplementation((...args) => step(() => account(...args))),
    vi.spyOn(globalThis, "fetch").mockImplementation((...args) => step(() => fetch(...args))),
  ];
  const run = async () => { await setup.prepareCreation(server.url); await setup.continueCreation("test-setup"); await setup.confirmCreationCodeSaved(); };
  try {
    await run(); const count = events;
    for (let index = 0; index < count; index++) {
      vi.stubGlobal("indexedDB", new IDBFactory()); events = 0; failure = index; fired = false;
      setup = new AuthoritySetup(lifecycle());
      try { await run(); } catch { /* Simulate process loss, preserving committed storage. */ }
      const pending = await readSecurityOperation();
      const signed = pending?.signed;
      failure = -1;
      setup = new AuthoritySetup(lifecycle());
      if (pending) {
        await setup.continueCreation("test-setup");
        if (await readSecurityOperation()) {
          if (signed) expect((await readSecurityOperation())!.signed).toEqual(signed);
          await setup.adoptCreationWithoutCode();
        }
      } else if (!await loadAccount()) await run();
      expect(isAuthority(await loadSecurity())).toBe(true);
      expect(await readSecurityOperation()).toBeUndefined();
      expect(fired).toBe(true);
    }
  } finally { for (const spy of spies) spy.mockRestore(); }
}, 60000);

it.each(["before", "after"] as const)("resumes emergency recovery at every durable write and RPC (%s)", async (side) => {
  let events = 0, failure = -1, fired = false, monitor = false;
  const step = async <T>(work: () => Promise<T>): Promise<T> => {
    if (!monitor) return work();
    const at = events++;
    if (at === failure && side === "before") { fired = true; throw new Error("injected crash"); }
    const result = await work();
    if (at === failure && side === "after") { fired = true; throw new Error("injected crash"); }
    return result;
  };
  const update = securityStore.updateSecurityEntry, adopt = securityStore.adoptRecoveredSecurity;
  const profile = accountStore.saveAccountPublicProfile, account = accountStore.saveAccount, fetch = globalThis.fetch;
  const spies = [
    vi.spyOn(securityStore, "updateSecurityEntry").mockImplementation((...args) => step(() => update(...args))),
    vi.spyOn(securityStore, "adoptRecoveredSecurity").mockImplementation((...args) => step(() => adopt(...args))),
    vi.spyOn(accountStore, "saveAccountPublicProfile").mockImplementation((...args) => step(() => profile(...args))),
    vi.spyOn(accountStore, "saveAccount").mockImplementation((...args) => step(() => account(...args))),
    vi.spyOn(globalThis, "fetch").mockImplementation((...args) => step(() => fetch(...args))),
  ];
  let count = 1;
  try {
    for (let index = -1; index < count; index++) {
      monitor = false; await server.close(); server = await AuthorityServer.start(built.binary);
      vi.stubGlobal("indexedDB", new IDBFactory()); setup = new AuthoritySetup(lifecycle());
      const { code } = await createAccount();
      vi.stubGlobal("indexedDB", new IDBFactory()); const recovery = new AuthoritySetup(lifecycle());
      events = 0; failure = index; fired = false; monitor = true;
      try {
        await recovery.prepareEmergencyRecovery(server.url, code);
        const status = await recovery.resumeRecovery();
        await server.ready(status.recoveryId);
        await recovery.confirmRecoveryCodeSaved();
        await recovery.completeRecovery();
      } catch { /* Restart at the committed boundary. */ }
      monitor = false;
      if (index === -1) count = events;
      else expect(fired).toBe(true);
      const pending = await readSecurityOperation(); const signed = pending?.signed;
      if (pending) {
        if (!signed) await recovery.continueRecoverySigning(code);
        const status = await recovery.resumeRecovery();
        await server.ready(status.recoveryId);
        if (signed) expect((await readSecurityOperation())!.signed).toEqual(signed);
        await recovery.completeRecovery();
      } else if (!await loadAccount()) {
        await recovery.prepareEmergencyRecovery(server.url, code);
        const status = await recovery.resumeRecovery(); await server.ready(status.recoveryId);
        await recovery.completeRecovery();
      }
      expect(await readSecurityOperation()).toBeUndefined(); expect(isAuthority(await loadSecurity())).toBe(true);
    }
  } finally { for (const spy of spies) spy.mockRestore(); }
}, 60000);
it.each(["before", "after"] as const)("keeps a single successor across every state-publication write and RPC (%s)", async (side) => {
  let events = 0, failure = -1, fired = false, monitor = false;
  const step = async <T>(work: () => Promise<T>): Promise<T> => {
    if (!monitor) return work();
    const at = events++;
    if (at === failure && side === "before") { fired = true; throw new Error("injected crash"); }
    const result = await work();
    if (at === failure && side === "after") { fired = true; throw new Error("injected crash"); }
    return result;
  };
  const update = securityStore.updateSecurityEntry, install = securityStore.installSecurity, touch = securityStore.touchSecurity;
  const fetch = globalThis.fetch;
  const spies = [
    vi.spyOn(securityStore, "updateSecurityEntry").mockImplementation((...args) => step(() => update(...args))),
    vi.spyOn(securityStore, "installSecurity").mockImplementation((...args) => step(() => install(...args))),
    vi.spyOn(securityStore, "touchSecurity").mockImplementation((...args) => step(() => touch(...args))),
    vi.spyOn(globalThis, "fetch").mockImplementation((...args) => step(() => fetch(...args))),
  ];
  let count = 1;
  try {
    for (let index = -1; index < count; index++) {
      monitor = false; await server.close(); server = await AuthorityServer.start(built.binary);
      vi.stubGlobal("indexedDB", new IDBFactory()); setup = new AuthoritySetup(lifecycle());
      const { account } = await createAccount(), c = await clients(account);
      await c.authority.prepareRecovery();
      events = 0; failure = index; fired = false; monitor = true;
      try { await c.authority.publishRecovery(); } catch { /* Restart. */ }
      monitor = false;
      if (index === -1) count = events; else expect(fired).toBe(true);
      const pending = await readSecurityOperation(); const signed = pending?.signed;
      if (pending) {
        if (!pending.codeSaved) await c.authority.publishRecovery(); else await c.authority.resumeSecurityOperation();
        if (signed) {
          const state = fromBinary(PutSecurityStateRequestSchema, signed).states[0].state;
          expect(await stateBytes((await loadSecurity()).states.at(-1)!)).toEqual(state);
        }
      }
      if (!pending && (await loadSecurity()).states.length === 1) {
        // Crash before claiming the slot: no publication was possible. The
        // display-only code is lost, so explicitly prepare and save a new one.
        await c.authority.prepareRecovery(); await c.authority.publishRecovery();
      }
      expect(await readSecurityOperation()).toBeUndefined(); expect((await loadSecurity()).states).toHaveLength(2);
    }
  } finally { for (const spy of spies) spy.mockRestore(); }
}, 60000);

it("retains canceled delivery until explicit candidate removal, and refuses Machine handoff even with an offer", async () => {
  const { account } = await createAccount(), c = await clients(account);
  const candidate = await pairCandidate(c, DeviceKind.MACHINE), id = await deviceID(candidate.ed);
  expect(handoffCandidates(await loadSecurity(), [{ id, recoveryEd: Authority.newSecret() }])).toEqual([]);
  await candidate.api.cancelPairing({ pairingId: candidate.joined.pairingId, candidateToken: candidate.joined.candidateToken });
  await expect(c.authority.resumeSecurityOperation()).rejects.toThrow("交付已失效");
  expect(await readSecurityOperation()).toBeDefined();
  await c.authority.removeDevice(id);
  expect(await readSecurityOperation()).toBeUndefined();
  const r = await loadSecurity();
  expect(r.states).toHaveLength(3); expect(r.states.at(-1)!.core.removed).toContain(id);
  await expect(AuthorityPairing.start(c.authority, c.pairing, "https://app.example.com", id)).rejects.toThrow("档案已删除");
});
it("rechecks the foreground generation immediately before signing and after storing the exact request", async () => {
  const { account } = await createAccount(), c = await clients(account);
  await c.authority.prepareRecovery();
  const encrypt = crypto.subtle.encrypt.bind(crypto.subtle);
  const hold = vi.spyOn(crypto.subtle, "encrypt").mockImplementation(async (...args) => {
    const bytes = await encrypt(...args); c.authority.lifecycle.pause(); return bytes;
  });
  const sign = vi.spyOn(crypto.subtle, "sign"); const put = vi.spyOn(c.api, "putSecurityState");
  await expect(c.authority.publishRecovery()).rejects.toThrow("暂停");
  expect(sign.mock.calls.filter(([algorithm]) => algorithm === "Ed25519")).toHaveLength(0); expect(put).not.toHaveBeenCalled(); hold.mockRestore();
  c.authority.lifecycle.resume();
  const update = securityStore.updateSecurityEntry;
  const stored = vi.spyOn(securityStore, "updateSecurityEntry").mockImplementation(async (...args) => {
    await update(...args);
    if ((await readSecurityOperation())?.signed) c.authority.lifecycle.pause();
  });
  await expect(c.authority.resumeSecurityOperation()).rejects.toThrow("暂停");
  const pending = await readSecurityOperation(); expect(pending!.signed).toBeDefined();
  expect(put).not.toHaveBeenCalled(); stored.mockRestore();
  c.authority.lifecycle.resume(); await c.authority.resumeSecurityOperation();
  expect(await stateBytes((await loadSecurity()).states.at(-1)!)).toEqual(fromBinary(PutSecurityStateRequestSchema, pending!.signed!).states[0].state);
});

it("separates machine revocation, archive rejoin with retained sessions, and permanent archive deletion", async () => {
  const { account } = await createAccount(), c = await clients(account);
  const enroll = async (candidate: Awaited<ReturnType<typeof pairCandidate>>) => {
    const auth = authorityServices(server.url).auth, id = await deviceID(candidate.ed);
    await auth.registerDevice({ protocolVersion: PROTOCOL_VERSION,
      registration: toBinary(DeviceRegistrationSchema, create(DeviceRegistrationSchema, { accountPublicKey: candidate.msg.accountPublicKey, devicePublicKey: candidate.ed, kind: DeviceKind.MACHINE, createdMs: BigInt(Date.now()) })),
      approval: candidate.msg.approval, approvalSignature: candidate.msg.approvalSignature });
    await candidate.api.getPairing({ pairingId: candidate.joined.pairingId, candidateToken: candidate.joined.candidateToken, received: true });
    await candidate.initiation.finishPairingOperation();
    const challenge = await auth.getChallenge({ deviceId: id });
    const { token } = await auth.login({ deviceId: id, challenge: challenge.challenge, signature: await signLogin(candidate.device.privateKey, challenge.challenge), protocolVersion: PROTOCOL_VERSION });
    const machine = createClient(MachineService, createConnectTransport({ baseUrl: server.url, useBinaryFormat: true,
      interceptors: [(next) => (req) => { req.header.set("Authorization", "Bearer " + token); return next(req); }] }));
    return { id, machine };
  };
  const candidate = await pairCandidate(c, DeviceKind.MACHINE), first = await enroll(candidate);
  const { buildRecord, parseChain } = await import("./device-security-store");
  const chain = await parseChain(candidate.msg.securityStates), admin = await loadSecurity();
  const record = await buildRecord(admin.pin, chain.states, chain.proofs, first.id, candidate.x.privateKey);
  record.lease = parseLease(new Uint8Array(candidate.msg.lease));
  const { MemorySecurityStorage } = await import("./test/security-fixture");
  const security = new DeviceSecurityClient(first.id, candidate.device.privateKey, async () => { throw new Error("no machine pull required"); }, () => true, new MemorySecurityStorage(record));
  const meta = await security.seal({ kind: 4, purpose: Purpose.MachineMeta, machine: first.id, session: "", request: "", seq: 1n, deadline: 0n, compression: 0 },
    toBinary(MachineMetaSchema, create(MachineMetaSchema, { hostname: "fixture-computer", protocolVersion: PROTOCOL_VERSION })));
  await first.machine.putMachine({ meta });
  const sessionId = "retained-session", seed = Authority.newSecret();
  const sessionMeta = await security.seal({ kind: 2, purpose: Purpose.SessionMeta, machine: first.id, session: sessionId, request: "", seq: 1n, deadline: 0n, compression: 0 },
    toBinary(SessionSchema, create(SessionSchema, { id: sessionId })), seed);
  const sessionKey = await security.seal({ kind: 3, purpose: Purpose.SessionKey, machine: first.id, session: sessionId, request: "", seq: 1n, deadline: 0n, compression: 0 }, seed);
  await first.machine.putSession({ sessionId, meta: sessionMeta, key: sessionKey });
  await expect(c.authority.deleteMachineRecords(first.id)).rejects.toThrow();
  await c.authority.removeDevice(first.id);
  expect((await c.api.listMachines({})).machines.some((m) => m.id === first.id)).toBe(true);
  await expect(first.machine.putMachine({ meta })).rejects.toThrow();
  const replacement = await pairCandidate(c, DeviceKind.MACHINE, first.id), second = await enroll(replacement);
  expect(second.id).not.toBe(first.id);
  const sessions = (await c.api.listRemoteSessions({})).sessions;
  expect(sessions.find((s) => s.sessionId === sessionId)?.machineId).toBe(second.id);
  expect(sessions.find((s) => s.sessionId === sessionId)?.meta).toEqual(sessionMeta);
  await c.authority.removeDevice(second.id);
  expect((await c.api.listRemoteSessions({})).sessions.some((s) => s.sessionId === sessionId)).toBe(true);
  await c.authority.deleteMachineRecords(second.id);
  expect((await c.api.listRemoteSessions({})).sessions.some((s) => s.sessionId === sessionId)).toBe(false);
  expect(await readSecurityOperation()).toBeUndefined();
});

it("retains a surviving pin after partial storage loss and refuses initialization or a new operation", async () => {
  const { account } = await createAccount(), record = await loadSecurity();
  const { rawSecurity } = await import("./test/stored-security-fixture");
  await rawSecurity("readwrite", (store) => store.delete("current"));
  expect(await securitySetupStatus()).toBe("invalid");
  await expect(securityStore.initializeSecurity(record.pin, record.states, record.proofs, record.memberID, record.xPrivate, false, record.lease)).rejects.toThrow("storage");
  await expect(claimSecurityOperation(operation("lease", server.url, account.deviceId, record.stateHash, {}))).rejects.toThrow("storage");
  expect(await rawSecurity("readonly", (store) => store.get("pinned-identity"))).toEqual(record.pin);
});
