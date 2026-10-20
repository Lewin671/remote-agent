// Creation and emergency recovery share the same foreground lock and pending
// slot as normal authority changes. Recovery seeds/codes are NEVER persisted.
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { Code, ConnectError, createClient, type Client } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { Authority, authoritySigningTime } from "./authority";
import { accountKey, checkServer, concat, deviceID, importRoot, newDeviceKey, signLogin, utf8, REGISTER_CONTEXT } from "./crypto";
import { buildRecord, initializeSecurity, securitySetupStatus, type SecurityRecord } from "./device-security-store";
import { leaseBytes, newSecurityKey, pinOf, SecurityError, signLease, stateBytes } from "./device-security";
import { RecoveryCode } from "./recovery";
import { AuthorityLifecycle, claimSecurityOperation, clearSecurityOperation, operation, readSecurityOperation, saveSecurityOperation, type SecurityOperation, requestAuthorityPersistence, markRecoveryCodeRotation } from "./security-operation";
import { loadAccount, loadRegistration, saveAccount, saveAccountPublicProfile, type StoredAccount } from "./storage";
import { apiBase } from "./remote";
import { relayFetch } from "./transport";
import { PROTOCOL_VERSION } from "./protocol";
import { AuthService, ClientService, RecoveryService, DeviceKind, DeviceRegistrationSchema, RegisterDeviceRequestSchema, type SignupProof } from "./gen/remoteagent/v1/relay_pb";

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
export type AuthorityStartup =
  | { kind: "create" | "recovery"; phase: SecurityOperation["phase"]; codeSaved: boolean; recoveryId?: string; readyMs?: bigint }
  | { kind: "registration" }
  | { kind: "account"; pending: boolean }
  | { kind: "empty" };
export async function authorityStartup(): Promise<AuthorityStartup> {
  const pending = await readSecurityOperation<{ recoveryId?: string; readyMs?: bigint }>();
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
