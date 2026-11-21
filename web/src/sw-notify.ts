import "../../build/core/web/wasm_exec.js";
import { machineDiskNotice } from "./machine-notifications";
import * as l10n from "./i18n";
import { create, fromBinary } from "@bufbuild/protobuf";
import { EnvelopeSchema, HeaderSchema } from "./gen/remoteagent/v1/envelope_pb";
import { NotifyContentSchema } from "./gen/remoteagent/v1/remote_pb";
import { NotifyKind } from "./gen/remoteagent/v1/relay_pb";
import {
  SecGrantChallengeCallSchema,
  SecOpenNotifyEnvelopeCallSchema,
  SecVerifyLeaseCallSchema,
} from "./gen/remoteagent/core/v1/seccall_pb";
import { RemoteAgentCore } from "./core/projection";
import { makeSecPin, secCallWith, setActiveCore, SecurityError, x25519DeriveWithPrivateKey } from "./core/sec-call";
import { loadSecurity } from "./device-security-store";
import { equal, leaseBytes, recoveryBytes, stateBytes } from "./device-security";

const MAX_PAYLOAD = 3993;
const SHELL_CACHE = "shell-v1";
const ANY_VARY = { ignoreVary: true, ignoreSearch: true };
type PushData = { title?: string; body?: string; tag?: string; session?: string; machine?: string; content?: string };

let swCore: RemoteAgentCore | undefined;
let swCoreLoading: Promise<RemoteAgentCore> | undefined;

async function ensureSwCore(): Promise<RemoteAgentCore> {
  if (globalThis.__remoteAgentCore) {
    const core = RemoteAgentCore.fromGlobal();
    setActiveCore(core);
    return core;
  }
  if (swCore) {
    setActiveCore(swCore);
    return swCore;
  }
  if (!swCoreLoading) {
    swCoreLoading = (async () => {
      const cache = await caches.open(SHELL_CACHE);
      let res: Response | undefined;
      const home = await cache.match("/", ANY_VARY);
      if (home) {
        const html = await home.text();
        const m = html.match(/\/assets\/core-[\w.-]+\.wasm/);
        if (m) res = await cache.match(m[0], ANY_VARY);
      }
      if (!res) {
        const keys = await cache.keys();
        const req = keys.find((r) => /\/assets\/core-[\w.-]+\.wasm$/.test(new URL(r.url).pathname));
        if (req) res = await cache.match(req, ANY_VARY);
      }
      if (!res) throw new SecurityError();
      const core = await RemoteAgentCore.load(Promise.resolve(res));
      swCore = core;
      setActiveCore(core);
      return core;
    })().catch((err) => {
      swCoreLoading = undefined;
      throw err;
    });
  }
  return swCoreLoading;
}

/** No network, management operations, signing keys or writes. Unknown local
 * state fails closed; the foreground page is responsible for security sync. */
export async function pushContent(d: PushData): Promise<{ title: string; body: string; machineId?: string; tag?: string }> {
  const generic = { title: "remote-agent", body: typeof d.body === "string" ? d.body : "" };
  if (typeof d.tag !== "string") return generic;
  const scheduled = d.tag.endsWith("/NOTIFY_KIND_SCHEDULED");
  const turnEnded = d.tag.endsWith("/NOTIFY_KIND_TURN_ENDED");
  const disk = d.tag.endsWith("/NOTIFY_KIND_MACHINE_DISK");
  if (!scheduled && !turnEnded && !disk) return generic;
  if (disk) generic.body = l10n.t("machine_status.home_warning");
  if (scheduled) generic.body = l10n.t("notifications.scheduled_tasks_have_new_results");
  try {
    if (typeof d.content !== "string" || !d.content.length ||
        new TextEncoder().encode(JSON.stringify(d)).length > MAX_PAYLOAD) return generic;
    const core = await ensureSwCore();
    const env = fromBinary(EnvelopeSchema, Uint8Array.from(atob(d.content), (c) => c.charCodeAt(0)));
    const ph = fromBinary(HeaderSchema, env.header);
    if (ph.sessionId !== d.session) throw new SecurityError();
    const record = await loadSecurity();
    const head = record.states.at(-1)!;
    if (!record.member || !head.core.members.some((m) => m.id === record.memberID && m.kind === 2))
      throw new SecurityError("untrusted");
    if (!record.lease) throw new SecurityError("lease");
    const now = BigInt(Date.now()) > record.floor ? BigInt(Date.now()) : record.floor;
    const rawStates = await Promise.all(record.states.map((s) => stateBytes(s)));
    const headRaw = rawStates[rawStates.length - 1];
    secCallWith(core, {
      case: "verifyLease",
      value: create(SecVerifyLeaseCallSchema, { headState: headRaw, lease: leaseBytes(record.lease), nowMs: now, checkWindow: true }),
    });
    const pin = makeSecPin(record.pin.account, record.pin.domain, record.pin.genesis);
    const chal = secCallWith(core, {
      case: "grantChallenge",
      value: create(SecGrantChallengeCallSchema, { pin, state: headRaw, memberId: record.memberID }),
    });
    const dh = await x25519DeriveWithPrivateKey(record.xPrivate, chal.data);
    const opened = secCallWith(core, {
      case: "openNotifyEnvelope",
      value: create(SecOpenNotifyEnvelopeCallSchema, {
        pin,
        states: rawStates,
        recoveryProofs: await Promise.all(record.proofs.map((p) => (p ? recoveryBytes(p) : Promise.resolve(new Uint8Array())))),
        memberId: record.memberID,
        dh,
        envelope: env,
      }),
    });
    // Authenticate the stored secret against the grant, as the foreground client does.
    if (!equal(opened.secondaryData, record.secret)) throw new SecurityError("storage");
    // Do not release plaintext if another tab changed membership while decrypting.
    if (await loadSecurity() !== record) throw new SecurityError("stale");
    const c = fromBinary(NotifyContentSchema, opened.data);
    if (disk) {
      const notice = machineDiskNotice(c);
      if (!notice || d.tag !== notice.tag || d.machine !== notice.machineId || ph.sessionId || ph.requestId !== `disk:${c.diskStatus!.observedMs}:${c.diskStatus!.pressure}`) throw new SecurityError();
      return notice;
    }
    if (turnEnded) {
      if (c.kind !== NotifyKind.TURN_ENDED || c.approval || c.sourceEventSeq <= 0n ||
          ph.requestId !== `notify:${NotifyKind.TURN_ENDED}:${c.sourceEventSeq}` ||
          d.tag !== `${c.sessionId}/NOTIFY_KIND_TURN_ENDED`)
        throw new SecurityError();
      if (c.sharedFileCount > 0) {
        const suffix = l10n.t("shared_file.notification_files", { count: c.sharedFileCount });
        return { ...generic, body: generic.body ? `${generic.body} ${suffix}` : suffix };
      }
      return generic;
    }
    if (c.kind !== NotifyKind.SCHEDULED || c.approval || c.sourceEventSeq <= 0n ||
        ph.requestId !== `scheduled:${c.sourceEventSeq}` ||
        d.tag !== `${c.sessionId}/scheduled:${c.sourceEventSeq}/NOTIFY_KIND_SCHEDULED` ||
        !c.title.trim() || [...c.title].length > 60 || !c.text.trim() || [...c.text].length > 200)
      throw new SecurityError();
    return { title: c.title, body: c.text };
  } catch {
    return generic;
  }
}
