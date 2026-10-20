// Creation and emergency recovery share the same foreground lock and pending
// slot as normal authority changes. Recovery seeds/codes are NEVER persisted.
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { Code, ConnectError, createClient, type Client } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { Authority, authoritySigningTime } from "./authority";
import { acceptRecoveryPublicProfile } from "./account-public";
import { accountKey, checkServer, concat, deviceID, importRoot, newDeviceKey, signLogin, utf8, REGISTER_CONTEXT } from "./crypto";
import { buildRecord, initializeSecurity, loadSecurity, parseChain, securitySetupStatus, type SecurityRecord } from "./device-security-store";
import { equal, leaseBytes, newSecurityKey, pinOf, recoveryBytes, SecurityError, signLease, stateBytes, stateHash, type Pin, type RecoveryStatement, type SecurityState, type Member } from "./device-security";
import { RecoveryCode } from "./recovery";
import { AuthorityLifecycle, claimSecurityOperation, clearSecurityOperation, operation, readSecurityOperation, saveSecurityOperation, type SecurityOperation, requestAuthorityPersistence, markRecoveryCodeRotation } from "./security-operation";
import { loadAccount, loadRegistration, saveAccount, saveAccountPublicProfile, type StoredAccount } from "./storage";
import { apiBase } from "./remote";
import { relayFetch } from "./transport";
import { PROTOCOL_VERSION } from "./protocol";
import { AuthService, ClientService, RecoveryService, DeviceKind, DeviceRegistrationSchema, RegisterDeviceRequestSchema, SubmitRecoveryRequestSchema, CompleteRecoveryRequestSchema, GetRecoveryContextResponse_Phase as RecoveryPhase, type SignupProof, type GetRecoveryContextResponse } from "./gen/remoteagent/v1/relay_pb";

type B = Uint8Array<ArrayBuffer>;
interface CreationDraft {
  account: StoredAccount;
  accountPublicKey: B;
  device: CryptoKeyPair;
  x: CryptoKeyPair;
  domain: B;
  recoveryEd: B;
  secret: B;
  now: bigint;
  storagePersisted: boolean;
  record?: SecurityRecord;
}
interface RecoveryDraft {
  account: StoredAccount;
  accountPublicKey: B;
  x: CryptoKeyPair;
  pin: Pin;
  states: SecurityState[];
  proofs: (RecoveryStatement | undefined)[];
  target: SecurityState;
  proof: RecoveryStatement;
  recoveryId: string;
  readyMs: bigint;
  completeRequest?: B;
}
interface UnsignedRecoveryDraft extends Omit<RecoveryDraft, "target" | "proof"> {
  member: Member;
  recoveryEd: B;
  secret: B;
  now: bigint;
}
export type AuthorityStartup =
  | { kind: "create" | "recovery"; phase: SecurityOperation["phase"]; codeSaved: boolean; recoveryId?: string; readyMs?: bigint }
  | { kind: "registration" }
  | { kind: "account"; pending: boolean }
  | { kind: "empty" };
export async function authorityStartup(): Promise<AuthorityStartup> {
  const pending = await readSecurityOperation<RecoveryDraft>();
  if (pending?.kind === "create" || pending?.kind === "recovery") return {
    kind: pending.kind, phase: pending.phase, codeSaved: pending.codeSaved,
    recoveryId: pending.kind === "recovery" ? pending.local.recoveryId : undefined,
    readyMs: pending.kind === "recovery" ? pending.local.readyMs : undefined,
  };
  if (await loadRegistration()) return { kind: "registration" };
  return await loadAccount() ? { kind: "account", pending: !!pending } : { kind: "empty" };
}
export function authorityServices(server: string) {
  const transport = createConnectTransport({ baseUrl: apiBase(server), fetch: relayFetch, useBinaryFormat: true, defaultTimeoutMs: 15000 });
  return { auth: createClient(AuthService, transport), recovery: createClient(RecoveryService, transport),
    client: (token: string) => createClient(ClientService, createConnectTransport({ baseUrl: apiBase(server), fetch: relayFetch, useBinaryFormat: true, defaultTimeoutMs: 15000,
      interceptors: [(next) => (req) => { req.header.set("Authorization", "Bearer " + token); return next(req); }] })) };
}
type Services = ReturnType<typeof authorityServices>;
export class AuthoritySetup {
  constructor(readonly lifecycle = new AuthorityLifecycle(), private readonly services: (server: string) => Services = authorityServices) {}
  /** Returns a code once; reopening uses the same draft, never a new genesis. */
  async prepareCreation(server: string) {
    server = checkServer(server);
    return this.lifecycle.run(async (check) => {
      const existing = await readSecurityOperation<CreationDraft>();
      if (existing) {
        if (existing.kind !== "create" || existing.server !== server) throw new Error("请先完成原安全操作");
        return { code: undefined, resumed: true, storagePersisted: existing.local.storagePersisted };
      }
      if (await loadAccount() || await loadRegistration() || await securitySetupStatus() !== "missing") throw new SecurityError("conflict");
      const storagePersisted = await requestAuthorityPersistence();
      check();
      const device = await newDeviceKey(), x = await newSecurityKey(), rootRaw = Authority.newSecret(), root = await importRoot(rootRaw);
      const accountPublicKey = new Uint8Array((await accountKey(root)).publicKey);
      const member = await Authority.newMember(device.publicKey, x.publicKey, DeviceKind.CLIENT);
      const recovery = await Authority.newRecovery();
      try {
        const local: CreationDraft = { account: { server, root, rootRaw, devicePrivate: device.privateKey, deviceId: member.id }, accountPublicKey, device, x, domain: Authority.newSecret(), recoveryEd: recovery.publicKey, secret: Authority.newSecret(), now: BigInt(Date.now()), storagePersisted };
        check();
        let op = operation("create", server, member.id, new Uint8Array(32), local);
        await claimSecurityOperation(op);
        check();
        op = await this.signCreation(op, check);
        return { code: RecoveryCode.encode(recovery.seed, op.local.record!.pin.genesis), resumed: false, storagePersisted };
      } finally { recovery.seed.fill(0); }
    });
  }
  private async signCreation(op: SecurityOperation<CreationDraft>, check: () => void) {
    if (op.signed) return op;
    const l = op.local;
    const member = await Authority.newMember(l.device.publicKey, l.x.publicKey, DeviceKind.CLIENT);
    check();
    const genesis = await Authority.genesis(await deviceID(l.accountPublicKey), l.domain, member, l.recoveryEd, l.secret, l.device.privateKey, l.now, { beforeSign: check });
    check();
    const record = await buildRecord(await pinOf(genesis), [genesis], [undefined], member.id, l.x.privateKey);
    const registration = toBinary(DeviceRegistrationSchema, create(DeviceRegistrationSchema, { accountPublicKey: l.accountPublicKey, devicePublicKey: member.ed, kind: DeviceKind.CLIENT, createdMs: genesis.core.issuedAt }));
    const signer = await accountKey(l.account.root!);
    check();
    const signed = toBinary(RegisterDeviceRequestSchema, create(RegisterDeviceRequestSchema, { registration, signature: signer.sign(concat(utf8(REGISTER_CONTEXT), registration)), protocolVersion: PROTOCOL_VERSION }));
    check();
    const saved = await saveSecurityOperation(op, { ...op, signed, local: { ...l, record } });
    check();
    return saved;
  }
  /** Registration/pinning may precede code confirmation; installation and
   * clearing the draft require the explicit confirmation below. */
  async continueCreation(setupToken?: string) {
    return this.lifecycle.run(async (check) => {
      let op = await readSecurityOperation<CreationDraft>();
      if (!op || op.kind !== "create") throw new SecurityError("conflict");
      op = await this.signCreation(op, check);
      const l = op.local, api = this.services(op.server);
      let token: string;
      try { token = await login(api.auth, l.account, check); }
      catch (e) {
        if (!(e instanceof ConnectError) || ![Code.PermissionDenied, Code.NotFound, Code.Unauthenticated].includes(e.code)) throw e;
        const register = async (signupProof?: SignupProof) => {
          const request = create(RegisterDeviceRequestSchema, { ...fromBinary(RegisterDeviceRequestSchema, op!.signed!),
            setupToken: setupToken ?? (op!.retry ? fromBinary(RegisterDeviceRequestSchema, op!.retry).setupToken : ""), signupProof });
          op = await saveSecurityOperation(op!, { ...op!, retry: toBinary(RegisterDeviceRequestSchema, request) });
          check();
          const response = await api.auth.registerDevice(request);
          check();
          if (response.deviceId !== l.account.deviceId) throw new SecurityError("untrusted");
        };
        try { await register(); } catch (e) {
          if (!(e instanceof ConnectError) || e.code !== Code.FailedPrecondition) throw e;
          await register(await solveSignup(api.auth, check));
        }
        token = await login(api.auth, l.account, check);
      }
      const pinRequest = { genesisState: await stateBytes(l.record!.states[0]) };
      check();
      await api.client(token).pinSecurityDomain(pinRequest);
      check();
      // Standalone availability certificate; a long-offline draft does not
      // change its original genesis or signed device registration.
      const lease = await signLease(l.record!.states[0], l.account.devicePrivate, authoritySigningTime(l.record!.states[0], BigInt(Date.now()), l.record!.floor, l.record!.lease), 0n, check);
      check();
      op = await saveSecurityOperation(op, { ...op, local: { ...l, record: { ...l.record!, lease } } });
      check();
      await api.client(token).putSecurityState({ lease: leaseBytes(lease) });
      check();
      op = await saveSecurityOperation(op, { ...op, phase: "PUBLISHED" });
      check();
      if (op.codeSaved) await this.adoptCreation(op, check);
      return { codeSaved: op.codeSaved, needsRecoveryRotation: !op.codeSaved };
    });
  }
  async confirmCreationCodeSaved() {
    return this.lifecycle.run(async (check) => {
      const op = await readSecurityOperation<CreationDraft>();
      if (!op || op.kind !== "create") throw new SecurityError("conflict");
      const saved = await saveSecurityOperation(op, { ...op, codeSaved: true });
      check();
      if (saved.phase === "PUBLISHED") await this.adoptCreation(saved, check);
    });
  }
  /** Explicit restart path when code display was lost: retain the original
   * genesis, adopt it, and require an immediate ordinary code rotation. */
  async adoptCreationWithoutCode() {
    return this.lifecycle.run(async (check) => {
      const op = await readSecurityOperation<CreationDraft>();
      if (!op || op.kind !== "create" || op.phase !== "PUBLISHED") throw new SecurityError("conflict");
      await this.adoptCreation(op, check);
      return { needsRecoveryRotation: true };
    });
  }
  private async adoptCreation(op: SecurityOperation<CreationDraft>, check: () => void) {
    const l = op.local, r = l.record!;
    await saveAccountPublicProfile(op.server, r.pin, l.accountPublicKey);
    check();
    await initializeSecurity(r.pin, r.states, r.proofs, r.memberID, r.xPrivate, false, r.lease);
    check();
    await markRecoveryCodeRotation(op.server, r.pin, !op.codeSaved);
    check();
    await saveAccount(l.account);
    check();
    await clearSecurityOperation(op);
  }
  /** Uses a recovery code for public-profile lookup only. No submit call. */
  async completeAccountPublicProfile(server: string, code: string) {
    return this.lifecycle.run(async (check) => {
      const local = await loadSecurity();
      const decoded = RecoveryCode.decode(code);
      if (!decoded) throw new Error("恢复码无效");
      try {
        const { ed25519 } = await import("@noble/curves/ed25519.js");
        const pub = new Uint8Array(ed25519.getPublicKey(decoded.seed));
        const context = await this.context(server, pub, (b) => Promise.resolve(new Uint8Array(ed25519.sign(b, decoded.seed))), "", decoded.genesis, check, local);
        return context.pin;
      } finally { decoded.seed.fill(0); }
    });
  }
  private async context(server: string, publicKey: B, sign: (b: B) => Promise<B>, recoveryId: string, prefix: B, check: () => void, local?: SecurityRecord) {
    const api = this.services(server).recovery;
    check();
    const { challenge } = await api.getRecoveryChallenge({});
    check();
    if (challenge.length !== 32) throw new SecurityError("untrusted");
    const signature = await sign(concat(utf8("ra3/recovery-context\0"), challenge, publicKey.length ? publicKey : new Uint8Array(32), utf8(recoveryId)));
    check();
    const reply = await api.getRecoveryContext({ recoveryEd: publicKey, recoveryId, challenge, signature });
    check();
    const { states, proofs } = await parseChain(reply.states);
    const pin = await acceptRecoveryPublicProfile(server, reply.accountId, new Uint8Array(reply.accountPublicKey), states, proofs, prefix, local);
    check();
    return { ...reply, states, proofs, pin };
  }
  async prepareEmergencyRecovery(server: string, code: string) {
    server = checkServer(server);
    return this.lifecycle.run(async (check) => {
      if (await readSecurityOperation()) throw new Error("请先继续原恢复或明确放弃已否决的恢复");
      if (await loadRegistration()) throw new Error("请先完成设备注册");
      const decoded = RecoveryCode.decode(code);
      if (!decoded) throw new Error("恢复码无效");
      try {
        const { ed25519 } = await import("@noble/curves/ed25519.js");
        const publicKey = new Uint8Array(ed25519.getPublicKey(decoded.seed));
        let local: SecurityRecord | undefined;
        const status = await securitySetupStatus();
        if (status === "ready") {
          const account = await loadAccount();
          if (account?.server !== server) throw new SecurityError("conflict");
          local = await loadSecurity();
        } else if (status !== "missing") throw new SecurityError("storage");
        const chain = await this.context(server, publicKey, (b) => Promise.resolve(new Uint8Array(ed25519.sign(b, decoded.seed))), "", decoded.genesis, check, local);
        if (![RecoveryPhase.UNSPECIFIED, RecoveryPhase.PENDING, RecoveryPhase.READY].includes(chain.phase)) throw new Error("请使用当前恢复码重新申请");
        const device = await newDeviceKey(), x = await newSecurityKey(), fresh = await Authority.newRecovery();
        try {
          const member = await Authority.newMember(device.publicKey, x.publicKey, DeviceKind.CLIENT);
          const parent = chain.states.at(-1)!;
          // Occupy the slot before any recovery state or statement signature.
          // The old recovery seed is kept in this call only. A crash before
          // persisting signatures requires re-entering it, never another state.
          const op = operation<UnsignedRecoveryDraft>("recovery", server, member.id, await stateHash(parent), {
            account: { server, devicePrivate: device.privateKey, deviceId: member.id } as StoredAccount,
            accountPublicKey: new Uint8Array(chain.accountPublicKey), x, pin: chain.pin,
            states: chain.states, proofs: chain.proofs, recoveryId: "", readyMs: 0n,
            member, recoveryEd: fresh.publicKey, secret: Authority.newSecret(),
            now: authoritySigningTime(parent, BigInt(Date.now()), local?.floor, local?.lease),
          });
          check();
          await claimSecurityOperation(op, local?.stateHash);
          check();
          await this.signEmergencyDraft(op, decoded.seed, check);
          // Return code even on submit failure via separate resume API: there
          // is no network between durable preparation and first code display.
          return { code: RecoveryCode.encode(fresh.seed, chain.pin.genesis) };
        } finally { fresh.seed.fill(0); }
      } finally { decoded.seed.fill(0); }
    });
  }
  private async signEmergencyDraft(op: SecurityOperation<UnsignedRecoveryDraft>, seed: B, check: () => void) {
    const l = op.local, parent = l.states.at(-1)!;
    const { ed25519 } = await import("@noble/curves/ed25519.js");
    if (!equal(ed25519.getPublicKey(seed), parent.core.recoveryEd)) throw new SecurityError("untrusted");
    check();
    const { state: target, statement: proof } = await Authority.recovery(parent, l.member,
      parent.core.members.filter((m) => m.id !== parent.core.authorityID).concat(l.member), l.secret,
      l.account.devicePrivate, seed, l.recoveryEd, l.now, { beforeSign: check });
    check();
    const signed = toBinary(SubmitRecoveryRequestSchema, create(SubmitRecoveryRequestSchema, { state: await stateBytes(target), statement: await recoveryBytes(proof) }));
    check();
    const completed: SecurityOperation<RecoveryDraft> = { ...op, signed,
      local: { account: l.account, accountPublicKey: l.accountPublicKey, x: l.x, pin: l.pin,
        states: l.states, proofs: l.proofs, target, proof, recoveryId: "", readyMs: 0n } };
    await saveSecurityOperation<unknown>(op, completed);
    check();
  }
  /** Crash before persisting the recovery signature: reuse the saved identity,
   * recovery public key and parent; re-enter only the old code in memory. */
  async continueRecoverySigning(code: string) {
    return this.lifecycle.run(async (check) => {
      const op = await readSecurityOperation<UnsignedRecoveryDraft>();
      if (!op || op.kind !== "recovery" || op.signed) throw new SecurityError("conflict");
      const decoded = RecoveryCode.decode(code);
      if (!decoded) throw new Error("恢复码无效");
      try {
        if (!equal(decoded.genesis, op.local.pin.genesis.slice(0, 10))) throw new SecurityError("untrusted");
        const { ed25519 } = await import("@noble/curves/ed25519.js");
        const pub = new Uint8Array(ed25519.getPublicKey(decoded.seed));
        const chain = await this.context(op.server, pub, (b) => Promise.resolve(new Uint8Array(ed25519.sign(b, decoded.seed))), "", decoded.genesis, check);
        if (!equal(await stateHash(chain.states.at(-1)!), op.parent)) throw new SecurityError("conflict");
        await this.signEmergencyDraft(op, decoded.seed, check);
      } finally { decoded.seed.fill(0); }
    });
  }
  async discardUnsignedRecovery() {
    return this.lifecycle.run(async (check) => {
      const op = await readSecurityOperation();
      if (!op || op.kind !== "recovery" || op.signed || op.phase !== "PREPARED") throw new SecurityError("conflict");
      check();
      await clearSecurityOperation(op);
    });
  }
  /** Publishes the prepared exact request; resume shares this idempotent path. */
  async submitRecovery() { return this.resumeRecovery(); }
  async resumeRecovery() {
    return this.lifecycle.run(async (check) => {
      let op = await readSecurityOperation<RecoveryDraft>();
      if (!op || op.kind !== "recovery") throw new SecurityError("conflict");
      if (!op.signed) throw new Error("恢复签名尚未保存，请重新输入原恢复码继续原草稿");
      if (!op.local.recoveryId) {
        check();
        const reply = await this.services(op.server).recovery.submitRecovery(fromBinary(SubmitRecoveryRequestSchema, op.signed));
        check();
        op = await saveSecurityOperation(op, { ...op, phase: "PUBLISHED", local: { ...op.local, recoveryId: reply.recoveryId, readyMs: reply.readyMs } });
        check();
      }
      return this.recoveryContext(op, check);
    });
  }
  private async recoveryContext(op: SecurityOperation<RecoveryDraft>, check: () => void) {
    const l = op.local;
    const context = await this.context(op.server, new Uint8Array(), (b) => crypto.subtle.sign("Ed25519", l.account.devicePrivate, b).then((s) => new Uint8Array(s)), l.recoveryId, l.pin.genesis.slice(0, 10), check);
    if (context.accountId !== l.pin.account || !equal(context.pin.genesis, l.pin.genesis) ||
        context.states.length < l.states.length || !equal(await stateHash(context.states[l.states.length - 1]), op.parent)) throw new SecurityError("conflict");
    return context;
  }
  async confirmRecoveryCodeSaved() {
    return this.lifecycle.run(async (check) => {
      const op = await readSecurityOperation();
      if (!op || op.kind !== "recovery") throw new SecurityError("conflict");
      await saveSecurityOperation(op, { ...op, codeSaved: true });
      check();
    });
  }
  async completeRecovery() {
    return this.lifecycle.run(async (check) => {
      let op = await readSecurityOperation<RecoveryDraft>();
      if (!op || op.kind !== "recovery" || !op.local.recoveryId) throw new SecurityError("conflict");
      const context = await this.recoveryContext(op, check), l = op.local;
      if (context.phase === RecoveryPhase.REJECTED) throw new Error("恢复已被否决，请明确重新申请");
      if (context.phase !== RecoveryPhase.PUBLISHED && context.phase !== RecoveryPhase.READY) throw new Error("48 小时等待尚未结束");
      if (!l.completeRequest) {
        const signature = new Uint8Array(await crypto.subtle.sign("Ed25519", l.account.devicePrivate, concat(utf8("ra3/recovery-complete\0"), utf8(l.recoveryId))));
        check();
        op = await saveSecurityOperation(op, { ...op, local: { ...l, completeRequest: toBinary(CompleteRecoveryRequestSchema, create(CompleteRecoveryRequestSchema, { recoveryId: l.recoveryId, signature })) } });
        check();
      }
      const response = await this.services(op.server).recovery.completeRecovery(fromBinary(CompleteRecoveryRequestSchema, op.local.completeRequest!));
      check();
      if (response.deviceId !== l.account.deviceId || !equal(response.accountPublicKey, l.accountPublicKey)) throw new SecurityError("untrusted");
      const published = await this.recoveryContext(op, check);
      const targetHash = await stateHash(l.target);
      if (published.phase !== RecoveryPhase.PUBLISHED || !equal(await stateHash(published.states.at(-1)!), targetHash)) throw new SecurityError("conflict");
      await saveAccountPublicProfile(op.server, l.pin, l.accountPublicKey);
      check();
      // A prior member's verified prefix may be extended by this recovery.
      // New identity adoption is an explicit recovery, not initializeSecurity's
      // same-member repair path; install atomically in the dedicated helper.
      await adoptRecoveredSecurity(l.pin, published.states, published.proofs, l.account.deviceId, l.x.privateKey);
      check();
      await markRecoveryCodeRotation(op.server, l.pin, !op.codeSaved);
      check();
      await saveAccount(l.account);
      check();
      op = await saveSecurityOperation(op, { ...op, phase: "DELIVERED" });
      check();
      await clearSecurityOperation(op);
      return { needsRecoveryRotation: !op.codeSaved };
    });
  }
  async discardRejectedRecovery() {
    return this.lifecycle.run(async (check) => {
      const op = await readSecurityOperation<RecoveryDraft>();
      if (!op || op.kind !== "recovery" || !op.local.recoveryId) throw new SecurityError("conflict");
      const context = await this.recoveryContext(op, check);
      if (context.phase !== RecoveryPhase.REJECTED) throw new Error("恢复仍待处理或结果未明，不能放弃");
      check();
      await clearSecurityOperation(op);
    });
  }
}
async function login(auth: Client<typeof AuthService>, account: StoredAccount, check: () => void) {
  check();
  const { challenge } = await auth.getChallenge({ deviceId: account.deviceId });
  check();
  const signature = await signLogin(account.devicePrivate, challenge);
  check();
  const reply = await auth.login({ deviceId: account.deviceId, challenge, signature, protocolVersion: PROTOCOL_VERSION });
  check();
  return reply.token;
}
async function solveSignup(auth: Client<typeof AuthService>, check: () => void): Promise<SignupProof> {
  check();
  const challenge = await auth.getSignupChallenge({});
  check();
  if (!challenge.challenge.length || challenge.difficultyBits > 24 || challenge.expiresMs <= BigInt(Date.now())) throw new Error("注册工作量证明无效或已过期");
  const nonce = new Uint8Array(8);
  for (let n = 0; ; n++) {
    new DataView(nonce.buffer).setBigUint64(0, BigInt(n));
    const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", concat(challenge.challenge, nonce)));
    check();
    if (challenge.expiresMs <= BigInt(Date.now())) throw new Error("注册工作量证明已过期，请重试原草稿");
    if (Array.from({ length: challenge.difficultyBits }, (_, bit) => (digest[bit >> 3] & (128 >> (bit & 7))) === 0).every(Boolean))
      return { $typeName: "remoteagent.v1.SignupProof", challenge: challenge.challenge, nonce };
  }
}
// Implemented in the security DB so identity replacement and prefix validation
// are one transaction. The recovery context above supplies the verified chain.
import { adoptRecoveredSecurity } from "./device-security-store";
export type RecoveryStatus = Pick<GetRecoveryContextResponse, "phase" | "readyMs" | "recoveryId">;
