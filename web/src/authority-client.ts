import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { type Client } from "@connectrpc/connect";
import { Authority, authoritySigningTime } from "./authority";
import { DeviceSecurityClient } from "./device-security-client";
import { installSecurity, type SecurityRecord } from "./device-security-store";
import { equal, leaseBytes, parseLease, parseState, SecurityError, signLease, stateBytes, stateHash, verifyLease } from "./device-security";
import { AuthorityLifecycle, claimSecurityOperation, clearSecurityOperation, isAuthority, operation, readSecurityOperation, saveSecurityOperation, type SecurityOperation, needsRecoveryCodeRotation } from "./security-operation";
import { type StoredAccount } from "./storage";
import { ClientService, PutSecurityStateRequestSchema, DeleteMachineRecordsRequestSchema } from "./gen/remoteagent/v1/relay_pb";

type B = Uint8Array<ArrayBuffer>;
interface StateDraft {
  action: "remove";
  target?: string;
}
export class AuthorityClient {
  readonly lifecycle: AuthorityLifecycle;
  constructor(
    readonly account: StoredAccount,
    readonly security: DeviceSecurityClient,
    private readonly api: Client<typeof ClientService>,
    lifecycle = new AuthorityLifecycle(),
  ) { this.lifecycle = lifecycle; }
  async isAuthority() { return isAuthority(await this.security.verifiedRecord()); }
  pending() { return readSecurityOperation(); }
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
      op = await this.signDraft(op as SecurityOperation<StateDraft>, check);
    }
    let r = await this.security.sync();
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
    op = await saveSecurityOperation(op, { ...op, phase: "DELIVERED" });
    check();
    await clearSecurityOperation(op);
  }
  private async signDraft(op: SecurityOperation<StateDraft>, check: () => void): Promise<SecurityOperation<StateDraft>> {
    const r = await this.authority(check);
    if (!equal(r.stateHash, op.parent)) throw new SecurityError("conflict");
    const head = r.states.at(-1)!, draft = op.local, key = this.account.devicePrivate, now = signingTime(r);
    check();
    const next = await Authority.remove(head, [draft.target!], r.secret, Authority.newSecret(), key, now, { beforeSign: check });
    check();
    const lease = next.core.authorityID === r.memberID ? await signLease(next, key, now, 0n, check) : undefined;
    check();
    check();
    const signed = toBinary(PutSecurityStateRequestSchema, create(PutSecurityStateRequestSchema, { states: [{ state: await stateBytes(next) }], lease: lease ? leaseBytes(lease) : undefined }));
    check();
    const saved = await saveSecurityOperation(op, { ...op, signed }, r.stateHash);
    check();
    return saved;
  }
  async renewLeaseNow() { return this.renewLease(true); }
  async renewLeaseIfNeeded() { return this.renewLease(false); }
  private async renewLease(force: boolean) {
    return this.lifecycle.run(async (check) => {
      await this.resume(check);
      if (await readSecurityOperation()) return;
      const r = await this.security.sync();
      check();
      if (!isAuthority(r)) return;
      const current = r.lease && equal(r.lease.head, r.stateHash) ? r.lease : undefined;
      if (!force && !Authority.needsRenewal(current, maxTime(r))) return;
      await this.claim("lease", {}, r);
      check();
      await this.resume(check);
    });
  }
  async removeDevice(id: string) {
    return this.lifecycle.run(async (check) => {
      await this.resume(check);
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
  async discardUnsignedStateDraft() {
    return this.lifecycle.run(async (check) => {
      const op = await readSecurityOperation();
      if (!op || op.kind !== "state" || op.signed || op.phase !== "PREPARED") throw new SecurityError("conflict");
      this.bound(op);
      check();
      await clearSecurityOperation(op);
    });
  }
}
async function contains(r: SecurityRecord, hash: B) {
  for (const state of r.states) if (equal(await stateHash(state), hash)) return true;
  return false;
}
function maxTime(r: SecurityRecord) { const now = BigInt(Date.now()); return now > r.floor ? now : r.floor; }
function signingTime(r: SecurityRecord) { return authoritySigningTime(r.states.at(-1)!, maxTime(r), r.floor, r.lease); }
