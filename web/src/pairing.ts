import * as l10n from "./i18n";
import { loadRegistration, loadAccount } from "./storage";
import { parseLease } from "./device-security";
import { PROTOCOL_VERSION } from "./protocol";
import {
  buildRecord,
  loadPairingIdentity,
  savePairingIdentity,
  clearPairingIdentity,
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
import { apiBase, joinWithApproval, resumeRegistration } from "./remote";
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
export class PairingStatusError extends Error {
  constructor(readonly code: "expired_or_canceled" | "refused") {
    super(code === "refused" ? l10n.t("security.the_managing_client_canceled_pairing") : l10n.t("security.pairing_expired_or_was_canceled"));
  }
}

export class PairCanceled extends Error {
  constructor() {
    super(l10n.t("queue.canceled"));
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
          throw new PairingStatusError("expired_or_canceled");
        throw e;
      }
      if (res.state === PairingState.CANCELED)
        throw new PairingStatusError("refused");
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
  const pending = await loadRegistration();
  if (pending) {
    if (pending.account.server !== link.server)
      throw new Error(l10n.t("security.registration_on_another_server_is_unfinished_retry_it_or"));
    onRegistering();
    await resumeRegistration();
    return (await loadAccount())!;
  }
  const pc = client(link.server);
  const key = newPairKey();
  if (existing && existing.server !== link.server)
    throw new Error(l10n.t("security.security_setup_must_use_the_original_account_s_server"));
  const peek = await pc.peekPairing({
    code: link.code.replace(/\s/g, ""),
    protocolVersion: PROTOCOL_VERSION,
  });
  if (
    peek.commit.length !== 32 ||
    !peek.pairingId ||
    (link.commit && !eq(link.commit, peek.commit))
  )
    throw new Error(l10n.t("security.pairing_commitment_mismatch_pairing_stopped"));
  const identity = await loadPairingIdentity(link.server, peek.pairingId);
  if (
    identity &&
    (identity.server !== link.server || identity.pairingId !== peek.pairingId)
  )
    throw new Error(l10n.t("security.another_pairing_draft_is_unfinished"));
  const device =
    identity?.device ??
    (existing ? await retainedDeviceKey(existing) : await newDeviceKey());
  if (
    existing &&
    (await deviceID(
      new Uint8Array(await crypto.subtle.exportKey("raw", device.publicKey)),
    )) !== existing.deviceId
  )
    throw new Error(l10n.t("security.the_pairing_draft_conflicts_with_the_current_identity"));
  const devicePublic = new Uint8Array(
    await crypto.subtle.exportKey("raw", device.publicKey),
  );
  const x = identity?.x ?? (await newSecurityKey());
  const xPublic = new Uint8Array(
    await crypto.subtle.exportKey("raw", x.publicKey),
  );
  await savePairingIdentity({
    server: link.server,
    pairingId: peek.pairingId,
    device,
    x,
  });
  const joined = await pc.joinPairing({
    protocolVersion: PROTOCOL_VERSION,
    devicePublicKey: devicePublic,
    deviceHpkePublicKey: xPublic,
    code: link.code.replace(/\s/g, ""),
    joinerPublic: key.pub,
    joinerKind: DeviceKind.CLIENT,
  });
  if (joined.pairingId !== peek.pairingId || !eq(joined.commit, peek.commit))
    throw new Error(l10n.t("security.the_server_replaced_the_pairing_commitment_after_receiving_the"));
  const id = joined.pairingId;
  const token = joined.candidateToken;
  if (link.commit && !eq(link.commit, joined.commit))
    throw new Error(l10n.t("security.the_server_returned_information_for_a_different_device_from"));
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
    throw new Error(l10n.t("security.the_managing_client_s_trust_information_does_not_match"));
  let replace = false;
  if (existing) {
    const status = await securitySetupStatus();
    if (status === "clock") throw new Error("security: clock");
    if (status === "invalid")
      throw new Error(
        l10n.t("security.the_local_security_state_is_damaged_and_cannot_be"),
      );
    if (status === "ready") {
      const current = await securityPin();
      if (!current || !pinEqual(current, trust.pin))
        throw new Error(l10n.t("security.the_pairing_code_belongs_to_another_security_domain_original"));
      replace = true;
    }
  }
  const descriptor = pairTrustDescriptor(trust.pin, trust.approver);
  if (!eq(descriptor, new Uint8Array(st.trust)))
    throw new Error(l10n.t("security.invalid_pairing_trust_information"));
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
  if (msg.server !== link.server) throw new Error(l10n.t("security.the_account_belongs_to_another_server"));
  // Canceled while the phone allowed it: the payload is dropped unused.
  // Past this point this device registers, and a cancel can no longer
  // undo it; the page then waits for the outcome instead.
  if (stopped()) throw new PairCanceled();
  const accountPublicKey = new Uint8Array(msg.accountPublicKey);
  if (
    accountPublicKey.length !== 32 ||
    (await deviceID(accountPublicKey)) !== trust.pin.account
  )
    throw new Error(l10n.t("security.the_account_does_not_match_the_managing_client_s"));
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
    throw new Error(l10n.t("security.pairing_approval_does_not_match_this_device_or_has"));
  checkPairChainSize(msg.securityStates);
  const { states, proofs } = await parseChain(msg.securityStates);
  await verifyChain(trust.pin, states, proofs);
  const head = states[states.length - 1];
  if (head.core.authorityID !== trust.approver)
    throw new Error(l10n.t("security.the_pairing_receipt_was_approved_by_a_device_other"));
  const candidateID = await deviceID(devicePublic);
  const candidate = head.core.members.find((m) => m.id === candidateID);
  if (
    !candidate ||
    !eq(candidate.ed, devicePublic) ||
    !eq(candidate.x, xPublic) ||
    candidate.kind !== DeviceKind.CLIENT
  )
    throw new Error(l10n.t("security.the_join_receipt_does_not_contain_the_correct_identity"));
  await openGrant(trust.pin, head, candidate.id, x.privateKey);
  const record = await buildRecord(
    trust.pin,
    states,
    proofs,
    candidate.id,
    x.privateKey,
  );
  record.lease = parseLease(new Uint8Array(msg.lease));
  const acct = await joinWithApproval(
    accountPublicKey,
    msg.server,
    {
      device,
      pairingId: id,
      approval,
      approvalSignature: new Uint8Array(msg.approvalSignature),
    },
    existing,
    record,
    replace,
  );
  await clearPairingIdentity(link.server, peek.pairingId);
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
      a.replacesDeviceId !== "" ||
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
  const reply = await loginRetained(account);
  const publicBytes = new Uint8Array(reply.devicePublicKey);
  if (
    publicBytes.length !== 32 ||
    (await deviceID(publicBytes)) !== account.deviceId
  )
    throw new Error(l10n.t("security.this_browser_s_original_public_key_could_not_be"));
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
    throw new Error(l10n.t("security.the_original_device_public_key_does_not_match_the"));
  return { privateKey: account.devicePrivate, publicKey };
}

/** A quiet challenge/login; never stores the token or changes the retained key. */
async function loginRetained(account: StoredAccount, signal?: AbortSignal) {
  const api = createClient(
    AuthService,
    createConnectTransport({
      fetch: relayFetch,
      baseUrl: apiBase(account.server),
      useBinaryFormat: true,
      defaultTimeoutMs: 15000,
    }),
  );
  const challenge = await api.getChallenge({ deviceId: account.deviceId }, { signal });
  return api.login({
    protocolVersion: PROTOCOL_VERSION,
    deviceId: account.deviceId,
    challenge: challenge.challenge,
    signature: await signLogin(account.devicePrivate, challenge.challenge),
  }, { signal });
}

export async function retainedIdentityDenied(account: StoredAccount): Promise<boolean> {
  const controller = new AbortController();
  let timer: ReturnType<typeof setTimeout> | undefined;
  const deadline = new Promise<boolean>((resolve) => {
    timer = setTimeout(() => { controller.abort(); resolve(false); }, 5_000);
  });
  const denied = (e: unknown) => !controller.signal.aborted
    && e instanceof ConnectError && e.code === Code.PermissionDenied;
  try {
    return await Promise.race([deadline, (async () => {
      try {
        try { await loginRetained(account, controller.signal); }
        catch (e) {
          if (!denied(e)) return false;
          // A restarted relay may have forgotten the first challenge.
          await loginRetained(account, controller.signal);
        }
        return false;
      } catch (e) { return denied(e); }
    })()]);
  } finally {
    clearTimeout(timer);
    controller.abort();
  }
}
