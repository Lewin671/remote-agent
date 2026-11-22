import "../../build/core/web/wasm_exec.js";
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { RemoteAgentCore } from "./core/projection";
import { readV4RecordForNotify } from "./core/security-store-v4";
import {
  EffectBatchSchema,
  EventSchema,
  type Event,
} from "./gen/remoteagent/core/v1/app_pb";
import {
  SecurityKeyAgreedEventSchema,
  SecurityOpenNotifyEventSchema,
  type SecurityNotifyOpenedEffect,
} from "./gen/remoteagent/core/v1/security_pb";
import { EnvelopeSchema, HeaderSchema } from "./gen/remoteagent/v1/envelope_pb";
import { NotifyKind } from "./gen/remoteagent/v1/relay_pb";
import * as l10n from "./i18n";
import { machineDiskNotice } from "./machine-notifications";

const MAX_PAYLOAD = 3993;
const SHELL_CACHE = "shell-v1";
const ANY_VARY = { ignoreVary: true, ignoreSearch: true };
type PushData = { title?: string; body?: string; tag?: string; session?: string; machine?: string; content?: string };

let swCore: RemoteAgentCore | undefined;
let swCoreLoading: Promise<RemoteAgentCore> | undefined;

async function ensureSwCore(): Promise<RemoteAgentCore> {
  if (globalThis.__remoteAgentCore) {
    return RemoteAgentCore.fromGlobal();
  }
  if (swCore) return swCore;
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
      if (!res) throw new Error("core wasm unavailable");
      const core = await RemoteAgentCore.load(Promise.resolve(res));
      swCore = core;
      return core;
    })().catch((err) => {
      swCoreLoading = undefined;
      throw err;
    });
  }
  return swCoreLoading;
}

async function openNotifyViaCore(
  core: RemoteAgentCore,
  recordBlob: Uint8Array,
  xPrivate: CryptoKey,
  env: ReturnType<typeof fromBinary<typeof EnvelopeSchema>>,
  sessionId: string,
  machineId: string,
  tag: string,
): Promise<SecurityNotifyOpenedEffect> {
  const appCore = core.newAppCore();
  try {
    let settled: SecurityNotifyOpenedEffect | undefined;
    const queue: Event[] = [
      create(EventSchema, {
        nowMs: BigInt(Date.now()),
        monoMs: 0n,
        event: {
          case: "securityOpenNotify",
          value: create(SecurityOpenNotifyEventSchema, {
            opId: 1n,
            recordBlob: new Uint8Array(recordBlob),
            envelope: env,
            nowMs: BigInt(Date.now()),
            sessionId,
            tag,
            machineId,
          }),
        },
      }),
    ];

    while (queue.length > 0 && !settled) {
      const ev = queue.shift()!;
      const batch = fromBinary(EffectBatchSchema, appCore.dispatch(toBinary(EventSchema, ev)));
      for (const eff of batch.effects) {
        if (eff.effect.case === "securityKeyAgree") {
          const ka = eff.effect.value;
          try {
            if (ka.peerPublic.byteLength !== 32) throw new Error("invalid peer public");
            const peerKey = await crypto.subtle.importKey("raw", new Uint8Array(ka.peerPublic), "X25519", false, []);
            const shared = new Uint8Array(await crypto.subtle.deriveBits({ name: "X25519", public: peerKey }, xPrivate, 256));
            queue.push(
              create(EventSchema, {
                nowMs: BigInt(Date.now()),
                event: {
                  case: "securityKeyAgreed",
                  value: create(SecurityKeyAgreedEventSchema, { opId: ka.opId, shared }),
                },
              }),
            );
          } catch (err) {
            queue.push(
              create(EventSchema, {
                nowMs: BigInt(Date.now()),
                event: {
                  case: "securityKeyAgreed",
                  value: create(SecurityKeyAgreedEventSchema, {
                    opId: ka.opId,
                    error: err instanceof Error ? err.message : String(err),
                  }),
                },
              }),
            );
          }
        } else if (eff.effect.case === "securityNotifyOpened") {
          settled = eff.effect.value;
          break;
        }
      }
    }
    if (!settled) throw new Error("missing notify result");
    return settled;
  } finally {
    appCore.close();
  }
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
    if (ph.sessionId !== (d.session ?? "")) throw new Error("session mismatch");
    const v4 = await readV4RecordForNotify();
    if (!v4) throw new Error("missing v4 record");

    const opened = await openNotifyViaCore(
      core,
      v4.recordBlob,
      v4.xPrivate,
      env,
      d.session ?? "",
      d.machine ?? "",
      d.tag,
    );
    if (opened.error || !opened.content) throw new Error(opened.error || "open notify failed");
    if (opened.verifyRecordUnchanged) {
      const check = await readV4RecordForNotify();
      if (!check || check.revision !== v4.revision) throw new Error("stale");
    }

    const c = opened.content;
    const openedHeader = fromBinary(HeaderSchema, opened.headerBytes.byteLength ? opened.headerBytes : env.header);
    if (disk) {
      const notice = machineDiskNotice(c);
      if (!notice || d.tag !== notice.tag || d.machine !== notice.machineId || openedHeader.sessionId || openedHeader.requestId !== `disk:${c.diskStatus!.observedMs}:${c.diskStatus!.pressure}`) {
        throw new Error("disk check failed");
      }
      return notice;
    }
    if (turnEnded) {
      if (c.kind !== NotifyKind.TURN_ENDED || c.approval || c.sourceEventSeq <= 0n ||
          openedHeader.requestId !== `notify:${NotifyKind.TURN_ENDED}:${c.sourceEventSeq}` ||
          d.tag !== `${c.sessionId}/NOTIFY_KIND_TURN_ENDED`) {
        throw new Error("turn check failed");
      }
      if (c.sharedFileCount > 0) {
        const suffix = l10n.t("shared_file.notification_files", { count: c.sharedFileCount });
        return { ...generic, body: generic.body ? `${generic.body} ${suffix}` : suffix };
      }
      return generic;
    }
    if (c.kind !== NotifyKind.SCHEDULED || c.approval || c.sourceEventSeq <= 0n ||
        openedHeader.requestId !== `scheduled:${c.sourceEventSeq}` ||
        d.tag !== `${c.sessionId}/scheduled:${c.sourceEventSeq}/NOTIFY_KIND_SCHEDULED` ||
        !c.title.trim() || [...c.title].length > 60 || !c.text.trim() || [...c.text].length > 200) {
      throw new Error("scheduled check failed");
    }
    return { title: c.title, body: c.text };
  } catch {
    return generic;
  }
}
