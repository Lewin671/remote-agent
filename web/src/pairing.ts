// Pairing flows for the web client, as in internal/relay/pair.go: an
// existing device shows a code, a new one joins with it, both show four
// emoji, and after the user checks them the existing device sends the
// root key sealed for the new one.

import { create, toBinary } from "@bufbuild/protobuf";
import { Code, ConnectError, createClient, type Interceptor } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { relayFetch } from "./transport";
import { accountKey, concat, deviceID, newDeviceKey, checkServer, importSessionKey, openMessage, sealMessage } from "./crypto";
import { PayloadKind } from "./gen/remoteagent/v1/envelope_pb";
import { DeviceKind, PairApprovalSchema, PairingService, PairingState, type GetPairingResponse } from "./gen/remoteagent/v1/relay_pb";
import { PairingPayloadSchema } from "./gen/remoteagent/v1/remote_pb";
import { derivePair, newPairKey, pairCommit, pairIdentityTranscript } from "./pair";
import { apiBase, joinWithRoot, type Remote } from "./remote";
import type { StoredAccount } from "./storage";

const b64url = (b: Uint8Array) => btoa(String.fromCharCode(...b)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
const fromB64url = (s: string) => Uint8Array.from(atob(s.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((s.length + 3) % 4)), (c) => c.charCodeAt(0));
const eq = (a: Uint8Array, b: Uint8Array) => a.length === b.length && a.every((x, i) => x === b[i]);
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

function client(server: string, auth?: () => string) {
  const interceptors: Interceptor[] = auth
    ? [
        (next) => (req) => {
          req.header.set("Authorization", auth());
          return next(req);
        },
      ]
    : [];
  return createClient(PairingService, createConnectTransport({ fetch: relayFetch, baseUrl: apiBase(server), useBinaryFormat: true, interceptors }));
}

export interface PairLink {
  code: string;
  commit?: Uint8Array;
  server: string;
}

/** Reads the query of a #/pair?... link. */
export function parsePairLink(query: string): PairLink | undefined {
  const q = new URLSearchParams(query);
  const code = q.get("code");
  const server = q.get("s");
  if (!code || !server) return undefined;
  const c = q.get("c");
  try {
    const commit = c ? fromB64url(c) : undefined;
    if (!/^\d{8}$/.test(code) || (commit && commit.length !== 32)) return undefined;
    return { code, server: checkServer(server), commit };
  } catch {
    return undefined;
  }
}

type Poll = (want: PairingState) => Promise<GetPairingResponse>;

/** The user stopped waiting on this device (not a refusal by the other). */
export class PairCanceled extends Error {
  constructor() {
    super("已取消");
  }
}

function poller(get: () => Promise<GetPairingResponse>, stopped: () => boolean): Poll {
  return async (want) => {
    for (;;) {
      if (stopped()) throw new PairCanceled();
      let res: GetPairingResponse;
      try {
        res = await get();
      } catch (e) {
        if (ConnectError.from(e).code === Code.NotFound) throw new Error("配对已过期或被取消");
        throw e;
      }
      if (res.state === PairingState.CANCELED) throw new Error("另一台设备取消了配对");
      if (res.state >= want) return res;
      await sleep(1000);
    }
  };
}

export interface Initiation {
  code: string;
  link: string;
  /** Resolves when a device asks to join, with the emoji to compare. */
  joined: Promise<{ emoji: string[]; kind: DeviceKind }>;
  /** Sends the root key; resolves when the new device has it. */
  approve(): Promise<void>;
  cancel(): void;
}

/** Starts adding a device to this browser's account. */
export async function initiate(remote: Remote): Promise<Initiation> {
  const acct = remote.account;
  const pc = client(acct.server, () => remote.authHeader());
  const key = newPairKey();
  const commit = await pairCommit(key.pub);
  const created = await pc.createPairing({ commit });
  const id = created.pairingId;
  let stopped = false;
  const poll = poller(() => pc.getPairing({ pairingId: id }), () => stopped);
  let secretsKey: Uint8Array | undefined;
 let candidatePublic: Uint8Array | undefined;
 let candidateKind: DeviceKind | undefined;
  const joined = (async () => {
    const st = await poll(PairingState.JOINED);
    await pc.revealPairing({ pairingId: id, initiatorPublic: key.pub });
    if (st.devicePublicKey.length !== 32 || ![DeviceKind.CLIENT, DeviceKind.MACHINE].includes(st.joinerKind)) throw new Error("无效的候选设备身份");
 candidatePublic = st.devicePublicKey; candidateKind = st.joinerKind;
 const tr = await pairIdentityTranscript(id, key.pub, st.joinerPublic, st.devicePublicKey, st.joinerKind);
    const s = await derivePair(key, st.joinerPublic, tr);
    secretsKey = s.key;
    return { emoji: s.emoji, kind: st.joinerKind };
  })();
  const q = new URLSearchParams({ code: created.code, c: b64url(commit), s: acct.server });
  return {
    code: created.code,
    link: `${location.origin}/#/pair?${q}`,
    joined,
    approve: async () => {
      if (!secretsKey || !candidatePublic || candidateKind === undefined) throw new Error("还没有设备加入");
      if (created.approverPublicKey.length !== 32 || await deviceID(created.approverPublicKey as Uint8Array<ArrayBuffer>) !== acct.deviceId) throw new Error("批准设备的公钥不匹配");
      const approval = toBinary(PairApprovalSchema, create(PairApprovalSchema, {accountId: await deviceID((await accountKey(acct.root)).publicKey as Uint8Array<ArrayBuffer>), pairingId: id, approverDeviceId: acct.deviceId, approverPublicKey: created.approverPublicKey, candidateDevicePublicKey: candidatePublic, candidateKind, expiresMs: created.expiresMs, nonce: crypto.getRandomValues(new Uint8Array(16))}));
      const approvalSignature = new Uint8Array(await crypto.subtle.sign("Ed25519", acct.devicePrivate, concat(new TextEncoder().encode("remote-agent/v1/pair-approval\0"), approval)));
      const env = await sealMessage(
        await importSessionKey(secretsKey as Uint8Array<ArrayBuffer>),
        { kind: PayloadKind.PAIRING, requestId: id, sender: acct.deviceId },
        PairingPayloadSchema,
        create(PairingPayloadSchema, { root: acct.rootRaw, server: acct.server, approval, approvalSignature }),
      );
      await pc.approvePairing({ pairingId: id, payload: env, approval, approvalSignature });
      // Only the new device confirming it has the key counts as success. A pairing
      // that disappears meanwhile leaves the outcome unknown.
      for (let i = 0; i < 120 && !stopped; i++) {
        try {
          const res = await pc.getPairing({ pairingId: id });
          if (res.state === PairingState.DELIVERED) return;
          if (res.state === PairingState.CANCELED) throw new Error("另一台设备取消了配对");
        } catch (e) {
          if (ConnectError.from(e).code === Code.NotFound) throw new Error("配对已过期，不确定新设备有没有拿到账户密钥。请在新设备上确认，必要时重新配对。");
          if (!(e instanceof ConnectError)) throw e;
        }
        await sleep(1000);
      }
      throw new Error(stopped ? "已取消" : "新设备没有来取账户密钥，它可能没有加入成功。请在新设备上确认，必要时重新配对。");
    },
    cancel: () => {
      stopped = true;
      pc.cancelPairing({ pairingId: id }).catch(() => undefined);
    },
  };
}

/** Joins an account with a code or link from an existing device. onEmoji
 * gets the emoji to compare with the other device's. */
export async function joinByPairing(link: PairLink, onEmoji: (emoji: string[]) => void, stopped: () => boolean, onRegistering: () => void = () => undefined): Promise<StoredAccount> {
  link = { ...link, server: checkServer(link.server) };
  const pc = client(link.server);
  const key = newPairKey();
  const device = await newDeviceKey();
 const devicePublic = new Uint8Array(await crypto.subtle.exportKey("raw", device.publicKey));
 const joined = await pc.joinPairing({ devicePublicKey: devicePublic, code: link.code.replace(/\s/g, ""), joinerPublic: key.pub, joinerKind: DeviceKind.CLIENT });
  const id = joined.pairingId;
  const token = joined.candidateToken;
  if (link.commit && !eq(link.commit, joined.commit)) throw new Error("服务器返回的不是扫码那台设备的信息，已停止配对");
  const poll = poller(() => pc.getPairing({ pairingId: id, candidateToken: token }), stopped);
  const st = await poll(PairingState.REVEALED);
  if (!eq(await pairCommit(st.initiatorPublic), joined.commit)) throw new Error("另一台设备的密钥和它的承诺对不上，已停止配对");
  const tr = await pairIdentityTranscript(id, st.initiatorPublic, key.pub, devicePublic, DeviceKind.CLIENT);
  const s = await derivePair(key, st.initiatorPublic, tr);
  onEmoji(s.emoji);
  const done = await poll(PairingState.APPROVED);
  const { msg } = await openMessage(
    await importSessionKey(s.key as Uint8Array<ArrayBuffer>),
    done.payload,
    { kind: PayloadKind.PAIRING, requestId: id },
    PairingPayloadSchema,
  );
  if (msg.server !== link.server) throw new Error("账户属于另一个服务器");
  // Canceled while the other device allowed it: the key is dropped unused.
  // Past this point this device registers, and a cancel can no longer
  // undo it; the page then waits for the outcome instead.
  if (stopped()) throw new PairCanceled();
  onRegistering();
  const acct = await joinWithRoot(msg.root as Uint8Array<ArrayBuffer>, msg.server, { device, pairingId: id, approval: msg.approval, approvalSignature: msg.approvalSignature });
  // Tells the other device it went through. If this is lost, that device
  // reports the outcome as unknown; the join itself stands.
  await pc.getPairing({ pairingId: id, candidateToken: token, received: true }).catch(() => undefined);
  return acct;
}
