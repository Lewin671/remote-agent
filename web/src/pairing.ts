// Pairing flows for the web client, as in internal/relay/pair.go: an
// existing device shows a code, a new one joins with it, both show four
// emoji, and after the user checks them the existing device sends the
// root key sealed for the new one.

import { create } from "@bufbuild/protobuf";
import { Code, ConnectError, createClient, type Interceptor } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { importSessionKey, openMessage, sealMessage } from "./crypto";
import { PayloadKind } from "./gen/remoteagent/v1/envelope_pb";
import { DeviceKind, PairingService, PairingState, type GetPairingResponse } from "./gen/remoteagent/v1/relay_pb";
import { PairingPayloadSchema } from "./gen/remoteagent/v1/remote_pb";
import { derivePair, newPairKey, pairCommit, pairTranscript } from "./pair";
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
  return createClient(PairingService, createConnectTransport({ baseUrl: apiBase(server), useBinaryFormat: true, interceptors }));
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
  return { code, server, commit: c ? fromB64url(c) : undefined };
}

type Poll = (want: PairingState) => Promise<GetPairingResponse>;

function poller(get: () => Promise<GetPairingResponse>, stopped: () => boolean): Poll {
  return async (want) => {
    for (;;) {
      if (stopped()) throw new Error("已取消");
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
  if (!acct.rootRaw) throw new Error("这个浏览器是用旧方式加入的，拿不到账户密钥，不能添加设备。请在设置里退出，再重新加入一次。");
  const pc = client(acct.server, () => remote.authHeader());
  const key = newPairKey();
  const commit = await pairCommit(key.pub);
  const created = await pc.createPairing({ commit });
  const id = created.pairingId;
  let stopped = false;
  const poll = poller(() => pc.getPairing({ pairingId: id }), () => stopped);
  let secretsKey: Uint8Array | undefined;
  const joined = (async () => {
    const st = await poll(PairingState.JOINED);
    await pc.revealPairing({ pairingId: id, initiatorPublic: key.pub });
    const tr = await pairTranscript(id, key.pub, st.joinerPublic, st.joinerKind);
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
      if (!secretsKey) throw new Error("还没有设备加入");
      const env = await sealMessage(
        await importSessionKey(secretsKey as Uint8Array<ArrayBuffer>),
        { kind: PayloadKind.PAIRING, requestId: id, sender: acct.deviceId },
        PairingPayloadSchema,
        create(PairingPayloadSchema, { root: acct.rootRaw, server: acct.server }),
      );
      await pc.approvePairing({ pairingId: id, payload: env });
      // The pairing disappears once the new device took the key.
      for (let i = 0; i < 120 && !stopped; i++) {
        try {
          await pc.getPairing({ pairingId: id });
        } catch {
          return;
        }
        await sleep(1000);
      }
    },
    cancel: () => {
      stopped = true;
      pc.cancelPairing({ pairingId: id }).catch(() => undefined);
    },
  };
}

/** Joins an account with a code or link from an existing device. onEmoji
 * gets the emoji to compare with the other device's. */
export async function joinByPairing(link: PairLink, onEmoji: (emoji: string[]) => void, stopped: () => boolean): Promise<StoredAccount> {
  const pc = client(link.server);
  const key = newPairKey();
  const joined = await pc.joinPairing({ code: link.code.replace(/\s/g, ""), joinerPublic: key.pub, joinerKind: DeviceKind.CLIENT });
  const id = joined.pairingId;
  const token = joined.candidateToken;
  if (link.commit && !eq(link.commit, joined.commit)) throw new Error("服务器返回的不是扫码那台设备的信息，已停止配对");
  const poll = poller(() => pc.getPairing({ pairingId: id, candidateToken: token }), stopped);
  const st = await poll(PairingState.REVEALED);
  if (!eq(await pairCommit(st.initiatorPublic), joined.commit)) throw new Error("另一台设备的密钥和它的承诺对不上，已停止配对");
  const tr = await pairTranscript(id, st.initiatorPublic, key.pub, DeviceKind.CLIENT);
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
  return joinWithRoot(msg.root as Uint8Array<ArrayBuffer>, msg.server);
}
