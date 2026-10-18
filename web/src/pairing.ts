import { PROTOCOL_VERSION } from "./protocol";
import {
  initializeSecurity,
  parseChain,
  securityPin,
  securitySetupStatus,
} from "./device-security-store";
import {
  MAX_PAIR_CHAIN_BYTES,
  MAX_PAIR_STATES,
  newSecurityKey,
  openGrant,
  pairDomainCommit,
  pairTrustDescriptor,
  parsePairTrustDescriptor,
  verifyChain,
  type Pin,
} from "./device-security";
// Joining an account for the web client, as in internal/relay/pair.go: only
// the phone starts and approves a pairing; a browser types or scans its code,
// both sides show four emoji, and the phone sends the signed state chain.

import { fromBinary } from "@bufbuild/protobuf";
import {
  Code,
  ConnectError,
  createClient,
  type Interceptor,
} from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { relayFetch } from "./transport";
import {
  concat,
  deviceID,
  signLogin,
  newDeviceKey,
  checkServer,
  importSessionKey,
  openMessage,
} from "./crypto";
import { PayloadKind } from "./gen/remoteagent/v1/envelope_pb";
import {
  AuthService,
  DeviceKind,
  PairApprovalSchema,
  PairingService,
  PairingState,
  type GetPairingResponse,
} from "./gen/remoteagent/v1/relay_pb";
import { PairingPayloadSchema } from "./gen/remoteagent/v1/remote_pb";
import { derivePair, newPairKey, pairDomainTranscript } from "./pair";
import { apiBase, joinWithApproval } from "./remote";
import type { StoredAccount } from "./storage";

const fromB64url = (s: string) =>
  Uint8Array.from(
    atob(
      s.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((s.length + 3) % 4),
    ),
    (c) => c.charCodeAt(0),
  );
const eq = (a: Uint8Array, b: Uint8Array) =>
  a.length === b.length && a.every((x, i) => x === b[i]);
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
  return createClient(
    PairingService,
    createConnectTransport({
      fetch: relayFetch,
      baseUrl: apiBase(server),
      useBinaryFormat: true,
      interceptors,
    }),
  );
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
    if (!/^\d{8}$/.test(code) || (commit && commit.length !== 32))
      return undefined;
    return { code, server: checkServer(server), commit };
  } catch {
    return undefined;
  }
}

/** The pairing payload boundary: more than 256 states or 800 KiB of
 * chain material cannot be paired; the account must be rebuilt. */
export function checkPairChainSize(
  states: readonly { state: Uint8Array; recovery: Uint8Array }[],
): void {
  if (
    states.length === 0 ||
    states.length > MAX_PAIR_STATES ||
    states.reduce((n, s) => n + s.state.length + s.recovery.length, 0) >
      MAX_PAIR_CHAIN_BYTES
  )
    throw new Error(
      "security: security chain is too large to pair; rebuild the account",
    );
}

type Poll = (want: PairingState) => Promise<GetPairingResponse>;

/** The user stopped waiting on this device (not a refusal by the other). */
export class PairCanceled extends Error {
  constructor() {
    super("已取消");
  }
}

function poller(
  get: () => Promise<GetPairingResponse>,
  stopped: () => boolean,
): Poll {
  return async (want) => {
    for (;;) {
      if (stopped()) throw new PairCanceled();
      let res: GetPairingResponse;
      try {
        res = await get();
      } catch (e) {
        if (ConnectError.from(e).code === Code.NotFound)
          throw new Error("配对已过期或被取消");
        throw e;
      }
      if (res.state === PairingState.CANCELED)
        throw new Error("另一台设备取消了配对");
      if (res.state >= want) return res;
      await sleep(1000);
    }
  };
}

/** Joins an account with a code or link from the phone. onEmoji gets the
 * emoji to compare with the phone's. */
export async function joinByPairing(
  link: PairLink,
  onEmoji: (emoji: string[]) => void,
  stopped: () => boolean,
  onRegistering: () => void = () => undefined,
  existing?: StoredAccount,
  confirm: (emoji: string[]) => Promise<boolean> = async () => false,
): Promise<StoredAccount> {
  link = { ...link, server: checkServer(link.server) };
  const pc = client(link.server);
  const key = newPairKey();
  if (existing && existing.server !== link.server)
    throw new Error("安全设置必须使用原账户的服务器");
  const device = existing
    ? await retainedDeviceKey(existing)
    : await newDeviceKey();
  const devicePublic = new Uint8Array(
    await crypto.subtle.exportKey("raw", device.publicKey),
  );
  const x = await newSecurityKey();
  const xPublic = new Uint8Array(
    await crypto.subtle.exportKey("raw", x.publicKey),
  );
  const peek = await pc.peekPairing({
    code: link.code.replace(/\s/g, ""),
    protocolVersion: PROTOCOL_VERSION,
  });
  if (
    peek.commit.length !== 32 ||
    !peek.pairingId ||
    (link.commit && !eq(link.commit, peek.commit))
  )
    throw new Error("配对承诺不匹配，已停止配对");
  const joined = await pc.joinPairing({
    protocolVersion: PROTOCOL_VERSION,
    devicePublicKey: devicePublic,
    deviceHpkePublicKey: xPublic,
    code: link.code.replace(/\s/g, ""),
    joinerPublic: key.pub,
    joinerKind: DeviceKind.CLIENT,
  });
  if (joined.pairingId !== peek.pairingId || !eq(joined.commit, peek.commit))
    throw new Error("服务器在获得临时公钥后替换了配对承诺，已停止配对");
  const id = joined.pairingId;
  const token = joined.candidateToken;
  if (link.commit && !eq(link.commit, joined.commit))
    throw new Error("服务器返回的不是扫码那台设备的信息，已停止配对");
  const poll = poller(
    () => pc.getPairing({ pairingId: id, candidateToken: token }),
    stopped,
  );
  const st = await poll(PairingState.REVEALED);
  // The phone's public descriptor binds the immutable genesis anchor and
  // the approver, never a mutable authority key.
  const trust = parsePairTrustDescriptor(new Uint8Array(st.trust));
  if (
    !eq(
      await pairDomainCommit(
        new Uint8Array(st.initiatorPublic),
        new Uint8Array(st.trust),
      ),
      peek.commit,
    )
  )
    throw new Error("手机的信任信息与配对承诺不匹配");
  let replace = false;
  if (existing) {
    const status = await securitySetupStatus();
    if (status === "clock") throw new Error("security: clock");
    if (status === "invalid")
      throw new Error("本机安全状态已损坏，不能用重新配对覆盖；请先导出并清空后重试");
    if (status === "ready") {
      const current = await securityPin();
      if (!current || !pinEqual(current, trust.pin))
        throw new Error("配对码属于另一个安全域，原密钥已保留");
      replace = true;
    }
  }
  const descriptor = pairTrustDescriptor(trust.pin, trust.approver);
  if (!eq(descriptor, new Uint8Array(st.trust)))
    throw new Error("无效的配对信任信息");
  const tr = await pairDomainTranscript(
    id,
    st.initiatorPublic,
    key.pub,
    devicePublic,
    xPublic,
    DeviceKind.CLIENT,
    st.trust,
  );
  const s = await derivePair(key, st.initiatorPublic, tr);
  onEmoji(s.emoji);
  if (stopped() || !(await confirm(s.emoji))) throw new PairCanceled();
  onRegistering();
  const done = await poll(PairingState.APPROVED);
  const { msg } = await openMessage(
    await importSessionKey(s.key as Uint8Array<ArrayBuffer>),
    done.payload,
    { kind: PayloadKind.PAIRING, requestId: id },
    PairingPayloadSchema,
  );
  if (msg.server !== link.server) throw new Error("账户属于另一个服务器");
  // Canceled while the phone allowed it: the payload is dropped unused.
  // Past this point this device registers, and a cancel can no longer
  // undo it; the page then waits for the outcome instead.
  if (stopped()) throw new PairCanceled();
  const accountPublicKey = new Uint8Array(msg.accountPublicKey);
  if (
    accountPublicKey.length !== 32 ||
    (await deviceID(accountPublicKey)) !== trust.pin.account
  )
    throw new Error("账户与手机的信任信息不匹配");
  const approval = new Uint8Array(msg.approval);
  if (
    !(await verifyApproval(
      approval,
      msg.approvalSignature,
      id,
      trust.approver,
      trust.pin.account,
      devicePublic,
    ))
  )
    throw new Error("配对批准与这台设备不匹配或已过期");
  checkPairChainSize(msg.securityStates);
  const { states, proofs } = await parseChain(msg.securityStates);
  await verifyChain(trust.pin, states, proofs);
  const head = states[states.length - 1];
  if (head.core.authorityID !== trust.approver)
    throw new Error("配对回执的批准设备不是当前管理端");
  const candidateID = await deviceID(devicePublic);
  const candidate = head.core.members.find((m) => m.id === candidateID);
  if (
    !candidate ||
    !eq(candidate.ed, devicePublic) ||
    !eq(candidate.x, xPublic) ||
    candidate.kind !== DeviceKind.CLIENT
  )
    throw new Error("加入回执没有本机的正确身份");
  await openGrant(trust.pin, head, candidate.id, x.privateKey);
  const acct = await joinWithApproval(accountPublicKey, msg.server, {
    device,
    pairingId: id,
    approval,
    approvalSignature: new Uint8Array(msg.approvalSignature),
  }, existing);
  await initializeSecurity(
    trust.pin,
    states,
    proofs,
    candidate.id,
    x.privateKey,
    replace,
  );
  // Tells the phone it went through. If this is lost, the phone reports
  // the outcome as unknown; the join itself stands.
  await pc
    .getPairing({ pairingId: id, candidateToken: token, received: true })
    .catch(() => undefined);
  return acct;
}

async function verifyApproval(
  approval: Uint8Array,
  signature: Uint8Array,
  pairingId: string,
  approver: string,
  account: string,
  devicePublic: Uint8Array,
): Promise<boolean> {
  try {
    const a = fromBinaryApproval(approval);
    if (
      a.accountId !== account ||
      a.pairingId !== pairingId ||
      a.candidateKind !== DeviceKind.CLIENT ||
      !eq(new Uint8Array(a.candidateDevicePublicKey), devicePublic) ||
      a.nonce.length !== 16 ||
      a.expiresMs <= BigInt(Date.now()) ||
      a.approverPublicKey.length !== 32 ||
      (await deviceID(new Uint8Array(a.approverPublicKey))) !==
        a.approverDeviceId ||
      a.approverDeviceId !== approver ||
      a.approverDeviceId === (await deviceID(devicePublic))
    )
      return false;
    const signer = await crypto.subtle.importKey(
      "raw",
      new Uint8Array(a.approverPublicKey),
      "Ed25519",
      false,
      ["verify"],
    );
    return await crypto.subtle.verify(
      "Ed25519",
      signer,
      new Uint8Array(signature),
      concat(
        new TextEncoder().encode("remote-agent/v1/pair-approval\0"),
        approval,
      ),
    );
  } catch {
    return false;
  }
}

function fromBinaryApproval(raw: Uint8Array) {
  return fromBinary(PairApprovalSchema, raw);
}

function pinEqual(a: Pin, b: Pin): boolean {
  return (
    a.account === b.account &&
    eq(a.domain, b.domain) &&
    eq(a.genesis, b.genesis)
  );
}

// Public-key recovery for old browser records: the private CryptoKey remains
// nonextractable. A relay-supplied public key must match both the retained ID
// and a local signature before it can enter the SAS candidate transcript.
async function retainedDeviceKey(
  account: StoredAccount,
): Promise<CryptoKeyPair> {
  const api = createClient(
    AuthService,
    createConnectTransport({
      fetch: relayFetch,
      baseUrl: apiBase(account.server),
      useBinaryFormat: true,
      defaultTimeoutMs: 15000,
    }),
  );
  const challenge = await api.getChallenge({ deviceId: account.deviceId });
  const reply = await api.login({
    protocolVersion: PROTOCOL_VERSION,
    deviceId: account.deviceId,
    challenge: challenge.challenge,
    signature: await signLogin(account.devicePrivate, challenge.challenge),
  });
  const publicBytes = new Uint8Array(reply.devicePublicKey);
  if (
    publicBytes.length !== 32 ||
    (await deviceID(publicBytes)) !== account.deviceId
  )
    throw new Error("无法核实这台浏览器原来的公钥，原密钥已保留");
  const publicKey = await crypto.subtle.importKey(
    "raw",
    publicBytes,
    "Ed25519",
    true,
    ["verify"],
  );
  const probe = crypto.getRandomValues(new Uint8Array(32));
  const signature = await crypto.subtle.sign(
    "Ed25519",
    account.devicePrivate,
    probe,
  );
  if (!(await crypto.subtle.verify("Ed25519", publicKey, signature, probe)))
    throw new Error("原设备公钥与本机签名密钥不匹配");
  return { privateKey: account.devicePrivate, publicKey };
}
