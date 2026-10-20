import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { type Client } from "@connectrpc/connect";
import { Authority, authoritySigningTime } from "./authority";
import { DeviceSecurityClient } from "./device-security-client";
import { installSecurity, type SecurityRecord } from "./device-security-store";
import { equal, leaseBytes, parseLease, SecurityError, signLease, stateHash, verifyLease } from "./device-security";
import { AuthorityLifecycle, claimSecurityOperation, clearSecurityOperation, isAuthority, operation, readSecurityOperation, saveSecurityOperation, type SecurityOperation, needsRecoveryCodeRotation } from "./security-operation";
import { type StoredAccount } from "./storage";
import { ClientService, PutSecurityStateRequestSchema } from "./gen/remoteagent/v1/relay_pb";

type B = Uint8Array<ArrayBuffer>;

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
    throw new Error("请先完成当前安全操作");
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
}
async function contains(r: SecurityRecord, hash: B) {
  for (const state of r.states) if (equal(await stateHash(state), hash)) return true;
  return false;
}
function maxTime(r: SecurityRecord) { const now = BigInt(Date.now()); return now > r.floor ? now : r.floor; }
function signingTime(r: SecurityRecord) { return authoritySigningTime(r.states.at(-1)!, maxTime(r), r.floor, r.lease); }
