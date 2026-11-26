import "../../build/core/web/wasm_exec.js";
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { RemoteAgentCore } from "./core/projection";
import { readV4RecordForNotify } from "./core/security-store-v4";
import { renderText } from "./core/text";
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
import { NotifyKind } from "./gen/remoteagent/v1/relay_pb";
import * as l10n from "./i18n";

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
  rawEnvelope: Uint8Array,
  expectedKind: NotifyKind,
  sessionId: string,
  machineId: string,
  tag: string,
  eventKey: string,
  fallbackBody: string,
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
            rawEnvelope: new Uint8Array(rawEnvelope),
            nowMs: BigInt(Date.now()),
            sessionId,
            tag,
            machineId,
            expectedKind,
            eventKey,
            fallbackBody,
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
  const expectedKind = d.tag.endsWith("/NOTIFY_KIND_SCHEDULED")
    ? NotifyKind.SCHEDULED
    : d.tag.endsWith("/NOTIFY_KIND_TURN_ENDED")
      ? NotifyKind.TURN_ENDED
      : d.tag.endsWith("/NOTIFY_KIND_MACHINE_DISK")
        ? NotifyKind.MACHINE_DISK
        : NotifyKind.UNSPECIFIED;
  if (expectedKind === NotifyKind.UNSPECIFIED) return generic;
  if (expectedKind === NotifyKind.MACHINE_DISK) generic.body = l10n.t("machine_status.home_warning");
  if (expectedKind === NotifyKind.SCHEDULED) generic.body = l10n.t("notifications.scheduled_tasks_have_new_results");
  try {
    if (typeof d.content !== "string" || !d.content.length ||
        new TextEncoder().encode(JSON.stringify(d)).length > MAX_PAYLOAD) return generic;
    const core = await ensureSwCore();
    const rawEnv = Uint8Array.from(atob(d.content), (c) => c.charCodeAt(0));
    const v4 = await readV4RecordForNotify();
    if (!v4) throw new Error("missing v4 record");

    const sessionId = d.session ?? "";
    let eventKey = "";
    if (expectedKind === NotifyKind.SCHEDULED) {
      const prefix = `${sessionId}/`;
      const suffix = "/NOTIFY_KIND_SCHEDULED";
      if (d.tag.startsWith(prefix) && d.tag.endsWith(suffix) && d.tag.length > prefix.length + suffix.length) {
        eventKey = d.tag.slice(prefix.length, d.tag.length - suffix.length);
      }
    }

    const opened = await openNotifyViaCore(
      core,
      v4.recordBlob,
      v4.xPrivate,
      rawEnv,
      expectedKind,
      sessionId,
      d.machine ?? "",
      d.tag,
      eventKey,
      generic.body,
    );
    const display = opened.display;
    if (opened.error || !display?.valid) {
      if (expectedKind !== NotifyKind.TURN_ENDED && display?.fullGenericBody) {
        return { ...generic, body: renderText(display.fullGenericBody) };
      }
      return generic;
    }
    if (opened.verifyRecordUnchanged) {
      const check = await readV4RecordForNotify();
      if (!check || check.revision !== v4.revision) throw new Error("stale");
    }

    if (display.kind === NotifyKind.MACHINE_DISK) {
      return {
        title: renderText(display.title),
        body: renderText(display.body),
        machineId: display.machineId,
        tag: display.tag,
      };
    }
    if (display.kind === NotifyKind.TURN_ENDED) {
      return { ...generic, body: renderText(display.fullGenericBody) };
    }
    if (display.kind === NotifyKind.SCHEDULED) {
      return { title: renderText(display.title), body: renderText(display.body) };
    }
    return generic;
  } catch {
    return generic;
  }
}
