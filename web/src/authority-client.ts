import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { Code, ConnectError, type Client } from "@connectrpc/connect";
import { Authority, authoritySigningTime } from "./authority";
import { deviceID, importSessionKey, sealMessage } from "./crypto";
import { DeviceSecurityClient } from "./device-security-client";
import { installSecurity, type SecurityRecord } from "./device-security-store";
import { equal, leaseBytes, parseLease, parseState, recoveryBytes, SecurityError, signLease, stateBytes, stateHash, verifyLease, type Member } from "./device-security";
import { makePairApproval } from "./pair-approval";
import { RecoveryCode } from "./recovery";
import { AuthorityLifecycle, claimSecurityOperation, clearSecurityOperation, isAuthority, operation, readSecurityOperation, saveSecurityOperation, type SecurityOperation, requestAuthorityPersistence, markRecoveryCodeRotation, needsRecoveryCodeRotation } from "./security-operation";
import { loadAccountPublicProfile, type StoredAccount } from "./storage";
import { ApprovePairingRequestSchema, ClientService, PairingService, PairingState, PutSecurityStateRequestSchema, DeviceKind, PairApprovalSchema, DeleteMachineRecordsRequestSchema } from "./gen/remoteagent/v1/relay_pb";
import { PairingPayloadSchema } from "./gen/remoteagent/v1/remote_pb";
import { PayloadKind } from "./gen/remoteagent/v1/envelope_pb";

type B = Uint8Array<ArrayBuffer>;
export interface PairingDelivery {
  pairingId: string;
  candidate: Member;
  key: B;
  expires: bigint;
  replacesDeviceId: string;
}
interface StateDraft {
  action: "remove" | "recovery" | "handoff" | "pair";
  target?: string;
  recoveryEd?: B;
  pair?: PairingDelivery;
}
export interface HandoffOffer { id: string; recoveryEd: B }
/** Uses only verified member kinds and already verified, self-signed metadata. */
export function handoffCandidates(record: SecurityRecord, offers: HandoffOffer[]): HandoffOffer[] {
  return offers.filter((o) => o.recoveryEd.length === 32 && o.id !== record.memberID &&
    record.states.at(-1)!.core.members.some((m) => m.id === o.id && m.kind === DeviceKind.CLIENT));
}
export class AuthorityClient {
  readonly lifecycle: AuthorityLifecycle;
  // Display drafts never occupy the durable publication slot. No recovery seed
  // is retained; confirmation claims the slot before any state signature.
  private codeDraft?: SecurityOperation<StateDraft | { recoveryEd: B; storagePersisted: boolean }>;
  constructor(
    readonly account: StoredAccount,
    readonly security: DeviceSecurityClient,
    private readonly api: Client<typeof ClientService>,
    private readonly pairing: Client<typeof PairingService>,
    private readonly writeOffer: (publicKey: B, check: () => void, persist: (request: B) => Promise<void>, request?: B) => Promise<boolean>,
    lifecycle = new AuthorityLifecycle(),
  ) { this.lifecycle = lifecycle; }
  async isAuthority() { return isAuthority(await this.security.verifiedRecord()); }
  pending() { return readSecurityOperation(); }
  async hasAccountPublicProfile() {
    return !!await loadAccountPublicProfile(this.account.server, (await this.security.verifiedRecord()).pin);
  }
  async needsRecoveryCodeRotation() {
    return needsRecoveryCodeRotation(this.account.server, (await this.security.verifiedRecord()).pin);
  }
  private async authority(check: () => void) {
    const record = await this.security.sync();
    check();
    if (!isAuthority(record)) throw new SecurityError("untrusted");
    return record;
  }
  private async claim(kind: SecurityOperation["kind"], local: unknown, record: SecurityRecord) {
    const op = operation(kind, this.account.server, this.account.deviceId, record.stateHash, local);
    await claimSecurityOperation(op, record.stateHash);
    return op;
  }
  private bound(op: SecurityOperation) {
    if (op.server !== this.account.server || op.memberID !== this.account.deviceId) throw new SecurityError("conflict");
  }
  async resumeSecurityOperation(): Promise<void> {
    return this.lifecycle.run((check) => this.resume(check));
  }
  private async resume(check: () => void): Promise<void> {
    let op = await readSecurityOperation();
    if (!op) return;
    this.bound(op);
    check();
    if (op.kind === "create" || op.kind === "recovery") throw new Error("请回到创建或恢复流程继续原草稿");
    if (op.kind === "offer") {
      if (!op.codeSaved) return;
      const local = op.local as { recoveryEd: B };
      const r = await this.security.sync();
      check();
      if (isAuthority(r) && equal(r.states.at(-1)!.core.recoveryEd, local.recoveryEd)) {
        await clearSecurityOperation(op);
        return;
      }
      if (!r.member) throw new SecurityError("untrusted");
      // Self-signed metadata is built against the current verified head, not
      // the head at code generation. An earlier offer may already have been
      // consumed; retain a different draft without republishing it as authority.
      if (isAuthority(r)) return;
      check();
      const published = await this.writeOffer(local.recoveryEd, check, async (signed) => {
        op = await saveSecurityOperation(op!, { ...op!, signed });
        check();
      }, op.signed);
      check();
      if (!published) {
        op = await saveSecurityOperation(op, { ...op, local: { ...local, rejected: true } });
        check();
        throw new Error("设备信息已变化，原交接申请未应用；可明确放弃后重新准备");
      }
      op = await saveSecurityOperation(op, { ...op, phase: "DELIVERED" });
      check();
      await clearSecurityOperation(op);
      return;
    }
    if (op.kind === "delete-records") {
      const r = await this.authority(check);
      if (!equal(r.stateHash, op.parent)) throw new SecurityError("conflict");
      check();
      if (!op.signed) {
        op = await saveSecurityOperation(op, { ...op, signed: toBinary(DeleteMachineRecordsRequestSchema, create(DeleteMachineRecordsRequestSchema, { machineId: (op.local as { machineId: string }).machineId })) }, r.stateHash);
        check();
      }
      await this.api.deleteMachineRecords(fromBinary(DeleteMachineRecordsRequestSchema, op.signed!));
      check();
      await clearSecurityOperation(op);
      return;
    }
    if (op.kind === "state" && !op.signed) {
      if ((op.local as StateDraft).action === "recovery" && !op.codeSaved) return;
      op = await this.signDraft(op as SecurityOperation<StateDraft>, check);
    }
    // A handoff can revoke our token before its ack arrives. Never infer
    // success from that error; keep the exact intent for explicit reconciliation.
    let r = await this.security.verifiedRecord();
    if (op.kind === "state" && op.signed) {
      const target = await parseState(new Uint8Array(fromBinary(PutSecurityStateRequestSchema, op.signed).states.at(-1)!.state));
      if (!r.member && await contains(r, await stateHash(target)) && target.core.authorityID !== r.memberID) {
        op = await saveSecurityOperation(op, { ...op, phase: "DELIVERED" });
        check();
        await clearSecurityOperation(op);
        return;
      }
    }
    r = await this.security.sync();
    check();
    if (op.kind === "lease") {
      if (!equal(r.stateHash, op.parent)) {
        if (!await contains(r, op.parent)) throw new SecurityError("conflict");
        await clearSecurityOperation(op);
        return;
      }
      if (!isAuthority(r)) throw new SecurityError("untrusted");
      if (!op.signed) {
        const lease = await signLease(r.states.at(-1)!, this.account.devicePrivate, signingTime(r), 0n, check);
        check();
        op = await saveSecurityOperation(op, { ...op, signed: toBinary(PutSecurityStateRequestSchema, create(PutSecurityStateRequestSchema, { lease: leaseBytes(lease) })) }, r.stateHash);
        check();
      }
      let request = fromBinary(PutSecurityStateRequestSchema, op.retry ?? op.signed!);
      const cert = parseLease(new Uint8Array(request.lease));
      await verifyLease(cert, r.states.at(-1)!);
      check();
      if (maxTime(r) >= cert.notAfter || (r.lease && equal(r.lease.head, r.stateHash) && r.lease.issuedAt > cert.issuedAt)) {
        const fresh = await signLease(r.states.at(-1)!, this.account.devicePrivate, signingTime(r), 0n, check);
        check();
        request = create(PutSecurityStateRequestSchema, { lease: leaseBytes(fresh) });
        op = await saveSecurityOperation(op, { ...op, retry: toBinary(PutSecurityStateRequestSchema, request) }, r.stateHash);
        check();
      }
      check();
      await this.api.putSecurityState(request);
      check();
      await installSecurity(r, [], [], parseLease(new Uint8Array(request.lease)));
      check();
      await clearSecurityOperation(op);
      return;
    }
    const original = fromBinary(PutSecurityStateRequestSchema, op.signed!);
    const target = await parseState(new Uint8Array(original.states.at(-1)!.state));
    const targetHash = await stateHash(target);
    const installed = r.states.find((s) => s.core.epoch === target.core.epoch);
    if (installed && !equal(await stateHash(installed), targetHash)) throw new SecurityError("conflict");
    if (!installed && !equal(r.stateHash, op.parent)) throw new SecurityError("conflict");
    if (op.phase === "DELIVERED") {
      if (!installed) throw new SecurityError("conflict");
      if ((op.local as StateDraft).action === "recovery" && op.codeSaved) {
        await markRecoveryCodeRotation(op.server, r.pin, false);
        check();
      }
      await clearSecurityOperation(op);
      return;
    }
    let request = fromBinary(PutSecurityStateRequestSchema, op.retry ?? op.signed!);
    let lease = request.lease.length ? parseLease(new Uint8Array(request.lease)) : undefined;
    if (lease) await verifyLease(lease, target);
    // Refresh only the independent lease, never the signed state/payload.
    if ((!installed || equal(r.stateHash, targetHash)) &&
        (!lease || maxTime(r) >= lease.notAfter || (r.lease && equal(r.lease.head, targetHash) && r.lease.issuedAt > lease.issuedAt))) {
      lease = target.core.authorityID === this.account.deviceId
        ? await signLease(target, this.account.devicePrivate, authoritySigningTime(target, maxTime(r), r.floor, r.lease), 0n, check) : undefined;
      check();
      request = create(PutSecurityStateRequestSchema, { states: original.states, lease: lease ? leaseBytes(lease) : undefined });
      op = await saveSecurityOperation(op, { ...op, retry: toBinary(PutSecurityStateRequestSchema, request) }, r.stateHash);
      check();
    }
    if (!installed) {
      check();
      const response = await this.api.putSecurityState(request);
      if (response.headEpoch !== target.core.epoch || !equal(await stateHash(await parseState(new Uint8Array(response.head))), targetHash)) throw new SecurityError("conflict");
      check();
      await installSecurity(r, [target], [undefined], lease);
      check();
      r = await this.security.verifiedRecord();
    } else if (equal(r.stateHash, targetHash) && lease && (!r.lease || r.lease.issuedAt < lease.issuedAt)) {
      check();
      await this.api.putSecurityState(request);
      check();
      await installSecurity(r, [], [], lease);
      check();
    }
    op = await saveSecurityOperation(op, { ...op, phase: "PUBLISHED" });
    check();
    if (op.delivery) {
      const delivery = fromBinary(ApprovePairingRequestSchema, op.delivery);
      try {
        check();
        await this.pairing.approvePairing(delivery);
        check();
        const status = await this.pairing.getPairing({ pairingId: delivery.pairingId });
        check();
        if (status.state !== PairingState.DELIVERED) {
          if (status.state === PairingState.CANCELED) throw new Error("配对已取消，请明确移除未完成候选后重新开始");
          return;
        }
      } catch (e) {
        if (!(e instanceof ConnectError) || ![Code.NotFound, Code.FailedPrecondition, Code.Canceled].includes(e.code)) throw e;
        // A closed ephemeral pairing can have a committed registration and a
        // lost receipt. Membership alone never counts as candidate delivery.
        const devices = await this.api.listDevices({});
        check();
        const approval = fromBinary(PairApprovalSchema, delivery.approval);
        const candidate = await deviceID(approval.candidateDevicePublicKey);
        if (!devices.devices.some((d) => d.id === candidate))
          throw new Error("配对交付已失效，请明确移除未完成候选后重新开始");
      }
    }
    op = await saveSecurityOperation(op, { ...op, phase: "DELIVERED" });
    check();
    if ((op.local as StateDraft).action === "recovery" && op.codeSaved) {
      await markRecoveryCodeRotation(op.server, r.pin, false);
      check();
    }
    await clearSecurityOperation(op);
  }
  private async signDraft(op: SecurityOperation<StateDraft>, check: () => void): Promise<SecurityOperation<StateDraft>> {
    const r = await this.authority(check);
    if (!equal(r.stateHash, op.parent)) throw new SecurityError("conflict");
    const head = r.states.at(-1)!, draft = op.local, key = this.account.devicePrivate, now = signingTime(r);
    if (draft.pair) {
      if (draft.pair.candidate.id === r.memberID) throw new SecurityError("untrusted");
      if (draft.pair.expires <= maxTime(r)) throw new Error("配对草稿已过期，尚未发布，可明确放弃后重新开始");
      check();
      const status = await this.pairing.getPairing({ pairingId: draft.pair.pairingId });
      check();
      if (status.state !== PairingState.REVEALED || status.joinerKind !== draft.pair.candidate.kind ||
          !equal(status.devicePublicKey, draft.pair.candidate.ed) || !equal(status.deviceHpkePublicKey, draft.pair.candidate.x))
        throw new Error("配对草稿已取消或候选改变，尚未发布，可明确放弃后重新开始");
    }
    check();
    let next;
    if (draft.action === "remove") next = await Authority.remove(head, [draft.target!], r.secret, Authority.newSecret(), key, now, { beforeSign: check });
    else if (draft.action === "recovery") next = await Authority.veto(head, draft.recoveryEd!, r.secret, key, now, { beforeSign: check });
    else if (draft.action === "handoff") next = await Authority.handoff(head, draft.target!, draft.recoveryEd!, r.secret, Authority.newSecret(), key, now, { beforeSign: check });
    else {
      const member = draft.pair!.candidate;
      const old = head.core.members.find((m) => m.id === member.id);
      if (old && (old.kind !== member.kind || !equal(old.ed, member.ed))) throw new SecurityError("conflict");
      next = old ? await Authority.rotateX(head, member.id, member.x, r.secret, Authority.newSecret(), key, now, { beforeSign: check })
        : await Authority.pair(head, member, r.secret, Authority.newSecret(), key, now, { beforeSign: check });
    }
    check();
    const lease = next.core.authorityID === r.memberID ? await signLease(next, key, now, 0n, check) : undefined;
    check();
    let delivery: B | undefined;
    if (draft.pair) {
      const pair = draft.pair;
      const profile = await loadAccountPublicProfile(this.account.server, r.pin);
      if (!profile) throw new Error("缺少账户公钥，请重新配对或用恢复码补齐");
      const approval = await makePairApproval(head, key, pair.pairingId, pair.candidate.ed, pair.candidate.kind as DeviceKind.CLIENT | DeviceKind.MACHINE, pair.expires, pair.replacesDeviceId, undefined, check);
      check();
      const states = await Promise.all([...r.states, next].map(async (s, i) => ({ state: await stateBytes(s), recovery: r.proofs[i] ? await recoveryBytes(r.proofs[i]!) : new Uint8Array() })));
      const { checkPairChainSize } = await import("./pairing");
      check();
      checkPairChainSize(states);
      const payload = await sealMessage(await importSessionKey(pair.key), { kind: PayloadKind.PAIRING, requestId: pair.pairingId, sender: r.memberID }, PairingPayloadSchema,
        create(PairingPayloadSchema, { server: this.account.server, ...approval, approvalSignature: approval.signature, securityStates: states, accountPublicKey: profile.accountPublicKey, lease: leaseBytes(lease!) }));
      delivery = toBinary(ApprovePairingRequestSchema, create(ApprovePairingRequestSchema, { pairingId: pair.pairingId, payload, approval: approval.approval, approvalSignature: approval.signature }));
    }
    check();
    const signed = toBinary(PutSecurityStateRequestSchema, create(PutSecurityStateRequestSchema, { states: [{ state: await stateBytes(next) }], lease: lease ? leaseBytes(lease) : undefined }));
    check();
    const saved = await saveSecurityOperation(op, { ...op, signed, delivery }, r.stateHash);
    check();
    return saved;
  }
  async renewLeaseNow() { return this.renewLease(true); }
  async renewLeaseIfNeeded() { return this.renewLease(false); }
  private async renewLease(force: boolean) {
    return this.lifecycle.run(async (check) => {
      const before = await readSecurityOperation();
      if (before?.kind !== "delete-records") await this.resume(check);
      let pending = await readSecurityOperation();
      if (pending && pending.kind !== "offer" && pending.kind !== "delete-records") return;
      const r = await this.security.sync();
      check();
      if (!isAuthority(r)) return;
      const current = r.lease && equal(r.lease.head, r.stateHash) ? r.lease : undefined;
      if (!force && !pending?.retry && !Authority.needsRenewal(current, maxTime(r))) return;
      if (pending) {
        this.bound(pending);
        let cert = pending.retry ? parseLease(new Uint8Array(fromBinary(PutSecurityStateRequestSchema, pending.retry).lease)) : undefined;
        if (cert && equal(cert.head, r.stateHash)) await verifyLease(cert, r.states.at(-1)!);
        if (!cert || !equal(cert.head, r.stateHash) || maxTime(r) >= cert.notAfter || (current && current.issuedAt > cert.issuedAt))
          cert = await signLease(r.states.at(-1)!, this.account.devicePrivate, signingTime(r), 0n, check);
        check();
        const request = create(PutSecurityStateRequestSchema, { lease: leaseBytes(cert) });
        // Preserve the offer/deletion bytes and phase. Only this independent
        // certificate can change after expiry or a verified head extension.
        pending = await saveSecurityOperation(pending, { ...pending, retry: toBinary(PutSecurityStateRequestSchema, request) }, r.stateHash);
        check();
        await this.api.putSecurityState(request);
        check();
        await installSecurity(r, [], [], cert);
        check();
        await saveSecurityOperation(pending, { ...pending, retry: undefined });
        return;
      }
      await this.claim("lease", {}, r);
      check();
      await this.resume(check);
    });
  }
  async removeDevice(id: string) {
    return this.lifecycle.run(async (check) => {
      const pending = await readSecurityOperation<StateDraft>();
      if (pending?.delivery && pending.local.pair?.candidate.id === id) {
        const r = await this.authority(check);
        const request = fromBinary(PutSecurityStateRequestSchema, pending.signed!);
        const target = await parseState(new Uint8Array(request.states.at(-1)!.state));
        if (!await contains(r, await stateHash(target))) throw new SecurityError("conflict");
        // This explicit removal ends delivery, then signs a child of the
        // verified current head. It never signs a competing parent.
        await clearSecurityOperation(pending);
      } else await this.resume(check);
      if (await readSecurityOperation()) throw new Error("请先完成当前安全操作");
      const r = await this.authority(check);
      if (id === r.memberID) throw new Error("管理端不能被普通删除，请先交接");
      if (!r.states.at(-1)!.core.members.some((m) => m.id === id)) return;
      await this.claim("state", { action: "remove", target: id } satisfies StateDraft, r);
      check();
      await this.resume(check);
    });
  }
  async deleteMachineRecords(id: string) {
    return this.lifecycle.run(async (check) => {
      await this.resume(check);
      const r = await this.authority(check);
      if (r.states.at(-1)!.core.members.some((m) => m.id === id) || !r.states.some((s) => s.core.members.some((m) => m.id === id && m.kind === 1))) throw new SecurityError("untrusted");
      let op = await this.claim("delete-records", { machineId: id }, r);
      op = await saveSecurityOperation(op, { ...op, signed: toBinary(DeleteMachineRecordsRequestSchema, create(DeleteMachineRecordsRequestSchema, { machineId: id })) }, r.stateHash);
      check();
      await this.resume(check);
    });
  }
  async prepareRecovery() {
    return this.lifecycle.run(async (check) => {
      await this.resume(check);
      const r = await this.authority(check), recovery = await Authority.newRecovery();
      try {
        if (await readSecurityOperation()) throw new Error("请先完成当前安全操作");
        check();
        this.codeDraft = operation("state", this.account.server, this.account.deviceId, r.stateHash,
          { action: "recovery", recoveryEd: recovery.publicKey } satisfies StateDraft);
        return { code: RecoveryCode.encode(recovery.seed, r.pin.genesis) };
      } finally { recovery.seed.fill(0); }
    });
  }
  async prepareHandoffCode() {
    return this.lifecycle.run(async (check) => {
      if (await readSecurityOperation()) throw new Error("请先完成当前安全操作");
      const r = await this.security.sync();
      check();
      if (!r.member || isAuthority(r)) throw new SecurityError("untrusted");
      const storagePersisted = await requestAuthorityPersistence();
      check();
      const recovery = await Authority.newRecovery();
      try {
        check();
        this.codeDraft = operation("offer", this.account.server, this.account.deviceId, r.stateHash,
          { recoveryEd: recovery.publicKey, storagePersisted });
        return { code: RecoveryCode.encode(recovery.seed, r.pin.genesis), storagePersisted };
      } finally { recovery.seed.fill(0); }
    });
  }
  /** UI calls only after explicit offline-save confirmation. Same API vetoes. */
  async publishRecovery() { return this.confirmCode("state"); }
  async offerHandoff() { return this.confirmCode("offer"); }
  private async confirmCode(kind: "state" | "offer") {
    return this.lifecycle.run(async (check) => {
      let op = await readSecurityOperation<StateDraft>();
      if (!op) {
        const draft = this.codeDraft;
        if (!draft || draft.kind !== kind) throw new SecurityError("conflict");
        const r = kind === "state" ? await this.authority(check) : await this.security.sync();
        check();
        if (kind === "state" && !equal(r.stateHash, draft.parent)) throw new SecurityError("conflict");
        await claimSecurityOperation({ ...draft, codeSaved: true }, r.stateHash);
        this.codeDraft = undefined;
      } else {
        if (op.kind !== kind || (kind === "state" && op.local.action !== "recovery")) throw new SecurityError("conflict");
        this.bound(op);
        if (!op.codeSaved) await saveSecurityOperation(op, { ...op, codeSaved: true });
      }
      check();
      await this.resume(check);
    });
  }
  /** A metadata offer can be explicitly abandoned even if its write result is
   * uncertain; the UI explains that its public key may already be published.
   * Signed member states and creation/recovery never use this shortcut. */
  async discardUnpublishedCode() {
    return this.lifecycle.run(async (check) => {
      const op = await readSecurityOperation<StateDraft>();
      if (!op) { this.codeDraft = undefined; return; }
      this.bound(op);
      if (op.phase !== "PREPARED" || !(op.kind === "offer" ||
          (op.kind === "state" && op.local.action === "recovery" && !op.signed && !op.codeSaved))) throw new SecurityError("conflict");
      check();
      await clearSecurityOperation(op);
    });
  }
  async discardUnsignedStateDraft() {
    return this.lifecycle.run(async (check) => {
      const op = await readSecurityOperation();
      if (!op || op.kind !== "state" || op.signed || op.phase !== "PREPARED") throw new SecurityError("conflict");
      this.bound(op);
      check();
      await clearSecurityOperation(op);
    });
  }
  async handoffAuthority(incoming: string, verifiedOffers: HandoffOffer[]) {
    return this.lifecycle.run(async (check) => {
      await this.resume(check);
      const r = await this.authority(check);
      const offer = handoffCandidates(r, verifiedOffers).find((o) => o.id === incoming);
      if (!offer) throw new Error("请先在接任设备生成并保存恢复码");
      await this.claim("state", { action: "handoff", target: incoming, recoveryEd: new Uint8Array(offer.recoveryEd) } satisfies StateDraft, r);
      check();
      await this.resume(check);
    });
  }
  async hasMachineArchive(id: string) {
    return (await this.api.listMachines({})).machines.some((m) => m.id === id);
  }
  async approvePairing(pair: PairingDelivery) {
    return this.lifecycle.run(async (check) => {
      if (pair.candidate.id === this.account.deviceId) throw new SecurityError("untrusted");
      const old = await readSecurityOperation<StateDraft>();
      if (old?.kind === "state" && old.local.pair?.pairingId === pair.pairingId) {
        await this.resume(check);
        return;
      }
      await this.resume(check);
      if (await readSecurityOperation()) throw new Error("另一次安全操作尚未完成");
      const r = await this.authority(check);
      if (pair.expires <= maxTime(r)) throw new Error("配对已过期");
      await this.claim("state", { action: "pair", pair } satisfies StateDraft, r);
      check();
      await this.resume(check);
    });
  }
}
async function contains(r: SecurityRecord, hash: B) {
  for (const state of r.states) if (equal(await stateHash(state), hash)) return true;
  return false;
}
function maxTime(r: SecurityRecord) { const now = BigInt(Date.now()); return now > r.floor ? now : r.floor; }
function signingTime(r: SecurityRecord) { return authoritySigningTime(r.states.at(-1)!, maxTime(r), r.floor, r.lease); }
