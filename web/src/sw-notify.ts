import { fromBinary } from "@bufbuild/protobuf";
import { EnvelopeSchema } from "./gen/remoteagent/v1/envelope_pb";
import { NotifyContentSchema } from "./gen/remoteagent/v1/remote_pb";
import { NotifyKind } from "./gen/remoteagent/v1/relay_pb";
import { contentSecretOf, loadSecurity } from "./device-security-store";
import { equal, fromEnvelope, openGrant, openWire, parseHeader, Purpose, SecurityError,
  stateAt, stateHash, validLease } from "./device-security";
import { unpackSecurity, validateSecurityBody } from "./device-security-client";

const MAX_PAYLOAD = 3993;
type PushData = { title?: string; body?: string; tag?: string; session?: string; content?: string };

/** No network, management operations, signing keys or writes. Unknown local
 * state fails closed; the foreground page is responsible for security sync. */
export async function scheduledPushContent(d: PushData): Promise<{ title: string; body: string }> {
  const generic = { title: "remote-agent", body: typeof d.body === "string" ? d.body : "" };
  if (typeof d.tag !== "string" || !d.tag.endsWith("/NOTIFY_KIND_SCHEDULED")) return generic;
  generic.body = "定时任务有新结果";
  try {
    if (typeof d.content !== "string" || !d.content.length ||
        new TextEncoder().encode(JSON.stringify(d)).length > MAX_PAYLOAD) return generic;
    const env = fromBinary(EnvelopeSchema, Uint8Array.from(atob(d.content), (c) => c.charCodeAt(0)));
    const wire = fromEnvelope(env), h = parseHeader(wire.header);
    if (h.purpose !== Purpose.Notify || h.session !== d.session)
      throw new SecurityError();
    const record = await loadSecurity();
    const head = record.states.at(-1)!;
    if (!record.member || !head.core.members.some((m) => m.id === record.memberID && m.kind === 2))
      throw new SecurityError("untrusted");
    const now = BigInt(Date.now()) > record.floor ? BigInt(Date.now()) : record.floor;
    await validLease(record.lease, head, now);
    const state = stateAt(record.states, h.epoch);
    if (!equal(await stateHash(state), h.state)) throw new SecurityError("stale");
    // Authenticate the stored secret against the grant, as the foreground client does.
    if (!equal(await openGrant(record.pin, head, record.memberID, record.xPrivate), record.secret))
      throw new SecurityError("storage");
    const opened = await openWire(record.pin, state, head, wire,
      await contentSecretOf(record, h.contentEpoch), true, now);
    const c = fromBinary(NotifyContentSchema, unpackSecurity(opened.header, opened.plaintext));
    validateSecurityBody(opened.header, NotifyContentSchema.typeName, c);
    if (c.kind !== NotifyKind.SCHEDULED || c.approval || c.sourceEventSeq <= 0n ||
        h.request !== `scheduled:${c.sourceEventSeq}` ||
        d.tag !== `${c.sessionId}/scheduled:${c.sourceEventSeq}/NOTIFY_KIND_SCHEDULED` ||
        !c.title.trim() || [...c.title].length > 60 || !c.text.trim() || [...c.text].length > 200)
      throw new SecurityError();
    // Do not release plaintext if another tab changed membership while decrypting.
    if (await loadSecurity() !== record) throw new SecurityError("stale");
    return { title: c.title, body: c.text };
  } catch {
    return generic;
  }
}
