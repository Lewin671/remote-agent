import { PROTOCOL_VERSION } from "./protocol";
import { initializeSecurity } from "./device-security-store";
import {
  Method,
  pairTrustDescriptor,
  pairDomainCommit,
  pairContextFromJSON,
  verifyPairContext,
  signPairContext,
  parseState,
  stateBytes,
  openGrant,
  newSecurityKey,
  type PairContext,
  type GoPairContext,
} from "./device-security";
// Pairing flows for the web client, as in internal/relay/pair.go: an
// existing device shows a code, a new one joins with it, both show four
// emoji, and after the user checks them the existing device sends the
// root key sealed for the new one.

import { create, toBinary } from "@bufbuild/protobuf";
import {
  Code,
  ConnectError,
  createClient,
  type Interceptor,
} from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { relayFetch } from "./transport";
import {
  accountKey,
  concat,
  deviceID,
  signLogin,
  newDeviceKey,
  checkServer,
  importSessionKey,
  openMessage,
  sealMessage,
} from "./crypto";
import { PayloadKind, type Envelope } from "./gen/remoteagent/v1/envelope_pb";
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
import { apiBase, joinWithRoot, type Remote } from "./remote";
import type { StoredAccount } from "./storage";

const b64url = (b: Uint8Array) =>
  btoa(String.fromCharCode(...b))
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
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

/** A retry of the same approval is safe only while the operation still exists.
 * Authentication/transport interruptions do not withdraw the user's consent. */
export function canRetryPairApproval(error: unknown): boolean {
  if (
    error instanceof ConnectError &&
    [
      Code.NotFound,
      Code.PermissionDenied,
      Code.FailedPrecondition,
      Code.InvalidArgument,
      Code.Canceled,
    ].includes(error.code)
  )
    return false;
  return !/过期|取消|拒绝|无效|不匹配/.test(
    error instanceof Error ? error.message : String(error),
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
  const localSecurity = await remote.security.localIdentity();
  const trust = pairTrustDescriptor(localSecurity.pin, acct.deviceId);
  const commit = await pairDomainCommit(new Uint8Array(key.pub), trust);
  const created = await pc.createPairing({ commit });
  const id = created.pairingId;
  let stopped = false;
  const poll = poller(
    () => pc.getPairing({ pairingId: id }),
    () => stopped,
  );
  let strongContext: PairContext | undefined;
  let candidateSignature: Uint8Array | undefined;
  let secretsKey: Uint8Array | undefined;
  let candidatePublic: Uint8Array | undefined;
  let candidateKind: DeviceKind | undefined;
  let approvalRequest:
    | Promise<{
        pairingId: string;
        payload: Envelope;
        approval: Uint8Array;
        approvalSignature: Uint8Array;
      }>
    | undefined;
  const joined = (async () => {
    const st = await poll(PairingState.JOINED);
    if (st.deviceHpkePublicKey.length !== 32)
      throw new Error("候选设备需要更新到当前协议");
    const context = await remote.security.rpc<GoPairContext>(Method.BeginPair, {
      ID: id,
      Candidate: {
        ID: await deviceID(st.devicePublicKey as Uint8Array<ArrayBuffer>),
        EdPublic: [...st.devicePublicKey],
        XPublic: [...st.deviceHpkePublicKey],
        Kind: st.joinerKind,
      },
    });
    strongContext = pairContextFromJSON(context);
    await verifyPairContext(strongContext, localSecurity.pin);
    if (
      strongContext.id !== id ||
      strongContext.approver !== acct.deviceId ||
      !eq(strongContext.candidate.ed, st.devicePublicKey) ||
      !eq(strongContext.candidate.x, st.deviceHpkePublicKey) ||
      strongContext.candidate.kind !== st.joinerKind
    )
      throw new Error("配对安全上下文与候选设备不匹配");
    await pc.revealPairing({
      pairingId: id,
      initiatorPublic: key.pub,
      securityContext: new TextEncoder().encode(JSON.stringify(context)),
    });
    if (
      st.devicePublicKey.length !== 32 ||
      ![DeviceKind.CLIENT, DeviceKind.MACHINE].includes(st.joinerKind)
    )
      throw new Error("无效的候选设备身份");
    candidatePublic = st.devicePublicKey;
    candidateKind = st.joinerKind;
    const tr = await pairDomainTranscript(
      id,
      key.pub,
      st.joinerPublic,
      st.devicePublicKey,
      st.deviceHpkePublicKey,
      st.joinerKind,
      trust,
    );
    const s = await derivePair(key, st.joinerPublic, tr);
    secretsKey = s.key;
    return { emoji: s.emoji, kind: st.joinerKind };
  })();
  const q = new URLSearchParams({
    code: created.code,
    c: b64url(commit),
    s: acct.server,
  });
  return {
    code: created.code,
    link: `${location.origin}/#/pair?${q}`,
    joined,
    approve: async () => {
      if (!approvalRequest) {
        approvalRequest = (async () => {
          if (!secretsKey || !candidatePublic || candidateKind === undefined)
            throw new Error("还没有设备加入");
          if (!strongContext) throw new Error("缺少可信配对上下文");
          for (
            let attempt = 0;
            !candidateSignature && attempt < 300;
            attempt++
          ) {
            if (stopped) throw new PairCanceled();
            const current = await pc.getPairing({ pairingId: id });
            if (current.state === PairingState.CANCELED)
              throw new PairCanceled();
            if (current.candidateSignature.length === 64)
              candidateSignature = current.candidateSignature;
            else await sleep(1000);
          }
          if (!candidateSignature)
            throw new Error("新设备没有确认自己的安全身份");
          const approver = await signPairContext(
            strongContext,
            acct.devicePrivate,
            "approve",
          );
          const joinedJSON = await remote.security.rpc<
            Parameters<typeof import("./device-security").stateFromJSON>[0]
          >(Method.ApprovePair, {
            ID: id,
            ApproverSignature: b64bytes(approver),
            CandidateSignature: b64bytes(candidateSignature),
          });
          const joinedState = await (
            await import("./device-security")
          ).stateFromJSON(joinedJSON);
          const joinedBytes = await stateBytes(joinedState);
          if (
            created.approverPublicKey.length !== 32 ||
            (await deviceID(
              created.approverPublicKey as Uint8Array<ArrayBuffer>,
            )) !== acct.deviceId
          )
            throw new Error("批准设备的公钥不匹配");
          const approval = toBinary(
            PairApprovalSchema,
            create(PairApprovalSchema, {
              accountId: await deviceID(
                (await accountKey(acct.root))
                  .publicKey as Uint8Array<ArrayBuffer>,
              ),
              pairingId: id,
              approverDeviceId: acct.deviceId,
              approverPublicKey: created.approverPublicKey,
              candidateDevicePublicKey: candidatePublic,
              candidateKind,
              expiresMs: created.expiresMs,
              nonce: crypto.getRandomValues(new Uint8Array(16)),
            }),
          );
          const approvalSignature = new Uint8Array(
            await crypto.subtle.sign(
              "Ed25519",
              acct.devicePrivate,
              concat(
                new TextEncoder().encode("remote-agent/v1/pair-approval\0"),
                approval,
              ),
            ),
          );
          const env = await sealMessage(
            await importSessionKey(secretsKey as Uint8Array<ArrayBuffer>),
            { kind: PayloadKind.PAIRING, requestId: id, sender: acct.deviceId },
            PairingPayloadSchema,
            create(PairingPayloadSchema, {
              root: acct.rootRaw,
              server: acct.server,
              approval,
              approvalSignature,
              securityState: joinedBytes,
            }),
          );
          return { pairingId: id, payload: env, approval, approvalSignature };
        })().catch((e) => {
          approvalRequest = undefined;
          throw e;
        });
      }
      await pc.approvePairing(await approvalRequest);
      // Only the new device confirming it has the key counts as success. A pairing
      // that disappears meanwhile leaves the outcome unknown.
      for (let i = 0; i < 120 && !stopped; i++) {
        try {
          const res = await pc.getPairing({ pairingId: id });
          if (res.state === PairingState.DELIVERED) return;
          if (res.state === PairingState.CANCELED)
            throw new Error("另一台设备取消了配对");
        } catch (e) {
          if (ConnectError.from(e).code === Code.NotFound)
            throw new Error(
              "配对已过期，不确定新设备有没有拿到账户密钥。请在新设备上确认，必要时重新配对。",
            );
          if (!(e instanceof ConnectError)) throw e;
        }
        await sleep(1000);
      }
      throw new Error(
        stopped
          ? "已取消"
          : "新设备没有来取账户密钥，它可能没有加入成功。请在新设备上确认，必要时重新配对。",
      );
    },
    cancel: () => {
      stopped = true;
      if (strongContext) {
        const context = strongContext;
        void signPairContext(context, acct.devicePrivate, "cancel")
          .then((sig) =>
            remote.security.rpc(Method.CancelPair, {
              ID: context.id,
              Signature: b64bytes(sig),
            }),
          )
          .catch(() => undefined);
      }
      pc.cancelPairing({ pairingId: id }).catch(() => undefined);
    },
  };
}

/** Joins an account with a code or link from an existing device. onEmoji
 * gets the emoji to compare with the other device's. */
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
  let contextJSON: GoPairContext;
  try {
    if (st.securityContext.length > 8192) throw new Error();
    contextJSON = JSON.parse(
      new TextDecoder("utf-8", { fatal: true }).decode(st.securityContext),
    );
  } catch {
    throw new Error("无效的配对安全上下文");
  }
  const context = pairContextFromJSON(contextJSON);
  await verifyPairContext(context, context.pin);
  if (
    context.id !== id ||
    context.candidate.kind !== DeviceKind.CLIENT ||
    !eq(context.candidate.ed, devicePublic) ||
    !eq(context.candidate.x, xPublic)
  )
    throw new Error("配对上下文与本机身份不匹配");
  const trust = pairTrustDescriptor(context.pin, context.approver);
  if (
    !eq(
      await pairDomainCommit(new Uint8Array(st.initiatorPublic), trust),
      peek.commit,
    )
  )
    throw new Error("管理电脑的信任信息与承诺不匹配");
  const tr = await pairDomainTranscript(
    id,
    st.initiatorPublic,
    key.pub,
    devicePublic,
    xPublic,
    DeviceKind.CLIENT,
    trust,
  );
  const s = await derivePair(key, st.initiatorPublic, tr);
  onEmoji(s.emoji);
  if (stopped() || !(await confirm(s.emoji))) throw new PairCanceled();
  onRegistering();
  const candidateSig = await signPairContext(
    context,
    device.privateKey,
    "accept",
  );
  await pc.getPairing({
    pairingId: id,
    candidateToken: token,
    candidateSignature: candidateSig,
  });
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
  const rootAccount = await accountKey(
    await (await import("./crypto")).importRoot(new Uint8Array(msg.root)),
  );
  if (
    (await deviceID(new Uint8Array(rootAccount.publicKey))) !==
    context.pin.account
  )
    throw new Error("账户与管理电脑的信任信息不匹配");
  const joinedState = await parseState(new Uint8Array(msg.securityState));
  if (!eq(joinedState.core.parent, context.expected))
    throw new Error("加入回执与配对上下文不匹配");
  const own = joinedState.core.members.find(
    (m) => m.id === context.candidate.id,
  );
  if (
    !own ||
    !eq(own.ed, devicePublic) ||
    !eq(own.x, xPublic) ||
    own.kind !== DeviceKind.CLIENT
  )
    throw new Error("加入回执没有本机的正确身份");
  await openGrant(context.pin, joinedState, context.candidate.id, x.privateKey);
  if (existing && !eq(msg.root, existing.rootRaw))
    throw new Error("配对属于另一个账户，原密钥已保留");
  const acct =
    existing ??
    (await joinWithRoot(msg.root as Uint8Array<ArrayBuffer>, msg.server, {
      device,
      pairingId: id,
      approval: msg.approval,
      approvalSignature: msg.approvalSignature,
    }));
  await initializeSecurity(
    context.pin,
    joinedState,
    acct.deviceId,
    x.privateKey,
  );
  // Tells the other device it went through. If this is lost, that device
  // reports the outcome as unknown; the join itself stands.
  await pc
    .getPairing({ pairingId: id, candidateToken: token, received: true })
    .catch(() => undefined);
  return acct;
}

function b64bytes(b: Uint8Array): string {
  let s = "";
  for (const c of b) s += String.fromCharCode(c);
  return btoa(s);
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
