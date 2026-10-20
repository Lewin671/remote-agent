import type { Client } from "@connectrpc/connect";
import { AuthorityClient } from "./authority-client";
import { Authority } from "./authority";
import { pairDomainCommit, pairTrustDescriptor, equal, SecurityError } from "./device-security";
import { derivePair, newPairKey, pairDomainTranscript, type PairKey, type PairSecrets } from "./pair";
import { PairingService, PairingState, DeviceKind, type GetPairingResponse } from "./gen/remoteagent/v1/relay_pb";
import { isAuthority, readSecurityOperation } from "./security-operation";

type B = Uint8Array<ArrayBuffer>;
export function pairLinkURL(web: string, server: string, code: string, commit: B): string {
  if (!/^\d{8}$/.test(code) || commit.length !== 32) throw new SecurityError();
  const base = new URL(web);
  const encoded = btoa(String.fromCharCode(...commit)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  base.hash = "/pair?" + new URLSearchParams({ code, c: encoded, s: server });
  return base.href;
}
/** Ephemeral SAS state lasts five minutes. Once approved, the durable
 * coordinator owns state publication and delivery, even after page death. */
export class AuthorityPairing {
  private stopped = false;
  private approved = false;
  private candidate?: GetPairingResponse;
  private secrets?: PairSecrets;
  private constructor(
    readonly code: string, readonly url: string, readonly pairingId: string,
    readonly expires: bigint, private readonly authority: AuthorityClient,
    private readonly api: Client<typeof PairingService>, private readonly key: PairKey,
    private readonly trust: B, private readonly replacesDeviceId: string,
  ) {}
  static async start(authority: AuthorityClient, api: Client<typeof PairingService>, web: string, replacesDeviceId = "") {
    await authority.resumeSecurityOperation();
    return authority.lifecycle.run(async (check) => {
      if (await readSecurityOperation()) throw new Error("请先完成当前安全操作");
      const record = await authority.security.sync();
      check();
      if (!isAuthority(record)) throw new SecurityError("untrusted");
      if (replacesDeviceId && (record.states.at(-1)!.core.members.some((m) => m.id === replacesDeviceId) ||
          !record.states.at(-1)!.core.removed.includes(replacesDeviceId) ||
          !record.states.some((s) => s.core.members.some((m) => m.id === replacesDeviceId && m.kind === DeviceKind.MACHINE))))
        throw new Error("只能从已移除电脑的档案重新加入");
      if (replacesDeviceId && !await authority.hasMachineArchive(replacesDeviceId)) throw new Error("电脑档案已删除，请按新设备加入");
      check();
      const key = newPairKey(), trust = pairTrustDescriptor(record.pin, record.memberID);
      const commit = await pairDomainCommit(new Uint8Array(key.pub), trust);
      check();
      const reply = await api.createPairing({ commit });
      check();
      if (!/^[0-9a-f]{32}$/.test(reply.pairingId) || reply.expiresMs <= BigInt(Date.now()) ||
          reply.expiresMs > BigInt(Date.now() + 5 * 60_000 + 5_000)) throw new SecurityError("untrusted");
      return new AuthorityPairing(reply.code, pairLinkURL(web, authority.account.server, reply.code, commit), reply.pairingId, reply.expiresMs, authority, api, key, trust, replacesDeviceId);
    });
  }
  private active() {
    if (this.stopped || BigInt(Date.now()) >= this.expires) throw new Error("配对已取消或过期");
  }
  /** Call again while waiting; no hidden-page timer and no background signing. */
  async joined(): Promise<{ emoji: string[]; kind: DeviceKind } | undefined> {
    this.active();
    if (this.secrets && this.candidate) return { emoji: this.secrets.emoji, kind: this.candidate.joinerKind };
    return this.authority.lifecycle.run(async (check) => {
      const r = await this.authority.security.sync();
      check();
      if (!isAuthority(r)) throw new SecurityError("untrusted");
      const status = await this.api.getPairing({ pairingId: this.pairingId });
      check(); this.active();
      if (status.state === PairingState.CANCELED) throw new Error("配对已取消");
      if (status.state === PairingState.OPEN) return;
      if (status.devicePublicKey.length !== 32 || status.deviceHpkePublicKey.length !== 32 || status.joinerPublic.length !== 32 ||
          ![DeviceKind.CLIENT, DeviceKind.MACHINE].includes(status.joinerKind) ||
          (this.replacesDeviceId && status.joinerKind !== DeviceKind.MACHINE)) throw new SecurityError("untrusted");
      if (status.state === PairingState.JOINED) {
        check();
        await this.api.revealPairing({ pairingId: this.pairingId, initiatorPublic: this.key.pub, trust: this.trust });
        check();
      } else if (!equal(status.initiatorPublic, this.key.pub) || !equal(status.trust, this.trust)) throw new SecurityError("conflict");
      const transcript = await pairDomainTranscript(this.pairingId, this.key.pub, status.joinerPublic, status.devicePublicKey, status.deviceHpkePublicKey, status.joinerKind, this.trust);
      this.secrets = await derivePair(this.key, status.joinerPublic, transcript);
      check(); this.active();
      this.candidate = status;
      return { emoji: this.secrets.emoji, kind: status.joinerKind };
    });
  }
  /** UI must wait for the candidate's local SAS confirmation, then obtain the
   * authority's explicit approval. The protocol has no candidate-confirm RPC. */
  async approve(sasConfirmed: boolean) {
    this.active();
    if (this.approved) { await this.authority.resumeSecurityOperation(); return; }
    const c = this.candidate, s = this.secrets;
    if (!sasConfirmed || !c || !s) throw new Error("请先由新设备确认图案，再在管理端批准");
    await this.authority.approvePairing({ pairingId: this.pairingId,
      candidate: await Authority.newMember(
        await crypto.subtle.importKey("raw", new Uint8Array(c.devicePublicKey), "Ed25519", true, ["verify"]),
        await crypto.subtle.importKey("raw", new Uint8Array(c.deviceHpkePublicKey), "X25519", true, []),
        c.joinerKind as 1 | 2),
      key: new Uint8Array(s.key), expires: this.expires, replacesDeviceId: this.replacesDeviceId });
    this.approved = true;
  }
  async finishPairingOperation() { await this.authority.resumeSecurityOperation(); }
  async cancel() {
    this.stopped = true;
    this.key.priv.fill(0);
    this.secrets?.key.fill(0);
    await this.authority.lifecycle.run(async (check) => {
      check();
      await this.api.cancelPairing({ pairingId: this.pairingId });
      check();
      // Published candidate intent stays until receipt or explicit removal.
    });
  }
}
