import { afterEach, expect, it, vi } from "vitest";
import { create, toBinary } from "@bufbuild/protobuf";
import { deflateSync } from "fflate";
import { IDBFactory } from "fake-indexeddb";
import { NotifyContentSchema, type NotifyContent } from "./gen/remoteagent/v1/remote_pb";
import { EnvelopeSchema, type Envelope } from "./gen/remoteagent/v1/envelope_pb";
import { NotifyKind } from "./gen/remoteagent/v1/relay_pb";
import { formCipher, fromEnvelope, parseHeader, stateHash, toEnvelope, type SecurityHeader } from "./device-security";
import { initializeSecurity, loadSecurity, touchSecurity } from "./device-security-store";
import { rawSecurity, storedSecurity } from "./test/stored-security-fixture";
import { GoSecurityFixture, recordFromJSON } from "./test/security-fixture";
import { scheduledPushContent } from "./sw-notify";
import rawSource from "./sw.js?raw";

afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.useRealTimers(); });
const generic = { title: "remote-agent", body: "定时任务有新结果" };

it("opens the actual Go sender's signed and compressed NotifyContent using stored browser keys", async () => {
  const f = await GoSecurityFixture.create();
  try {
    vi.stubGlobal("indexedDB", new IDBFactory());
    const { record: r } = await recordFromJSON(await f.fixture.record(f.phone.json.member));
    await initializeSecurity(r.pin, r.states, r.proofs, r.memberID, r.xPrivate, false, r.lease);
    const text = "结果摘要".repeat(40);
    const env = await f.fixture.seal(f.machine, { kind: 10, machineId: f.machine,
      sessionId: "session", requestId: "scheduled:7", sender: f.machine },
      new Uint8Array(toBinary(NotifyContentSchema, create(NotifyContentSchema, {
        kind: NotifyKind.SCHEDULED, machineId: f.machine, sessionId: "session",
        title: "Go 端任务名", text, sourceEventSeq: 7n,
      }))));
    expect(parseHeader(fromEnvelope(env).header).compression).toBe(1);
    expect(await scheduledPushContent({ session: "session", tag: "session/scheduled:7/NOTIFY_KIND_SCHEDULED",
      content: btoa(String.fromCharCode(...toBinary(EnvelopeSchema, env))) })).toEqual({ title: "Go 端任务名", body: text });
  } finally { await f.fixture.close(); }
}, 20000);

async function notification(fields: Partial<NotifyContent> = {}, header: Partial<SecurityHeader> = {}, compressed = false, wrongKey = false) {
  const f = await storedSecurity(), record = await loadSecurity();
  const head = record.states.at(-1)!, machine = head.core.members.find((m) => m.kind === 1)!.id;
  let body = toBinary(NotifyContentSchema, create(NotifyContentSchema, {
    machineId: machine, sessionId: "session", kind: NotifyKind.SCHEDULED,
    title: "每天的研究任务", text: "结果摘要", sourceEventSeq: 7n, ...fields,
  }));
  if (compressed) body = deflateSync(body);
  const env = toEnvelope(await formCipher(wrongKey ? new Uint8Array(32) : record.secret, undefined, {
    account: record.pin.account, domain: record.pin.domain, epoch: head.core.epoch,
    state: await stateHash(head), contentEpoch: head.core.contentEpoch, sender: machine,
    kind: 10, purpose: 10, machine, session: "session", request: "scheduled:7", seq: 0n,
    deadline: 0n, compression: compressed ? 1 : 0, ...header,
  }, new Uint8Array(body), f.machineEd));
  const payload = (envelope: Envelope = env) => ({
    title: "remote-agent", body: generic.body, session: "session",
    tag: "session/scheduled:7/NOTIFY_KIND_SCHEDULED",
    content: btoa(String.fromCharCode(...toBinary(EnvelopeSchema, envelope))),
  });
  return { env, payload };
}

it.each([false, true])("verifies and decrypts signed scheduled content from IndexedDB (compressed=%s)", async (compressed) => {
  const { payload } = await notification({}, {}, compressed);
  expect(await scheduledPushContent(payload())).toEqual({ title: "每天的研究任务", body: "结果摘要" });
});

it("the actual push handler shows decrypted title/summary and falls back after signature tampering", async () => {
  const { env, payload } = await notification();
  const handlers: Record<string, (e: any) => void> = {};
  const showNotification = vi.fn();
  const self = { addEventListener: (kind: string, fn: (e: any) => void) => { handlers[kind] = fn; },
    clients: { matchAll: async () => [] }, registration: { showNotification } };
  new Function("scheduledPushContent", "self", "caches", "fetch", rawSource.replace(/^import .*$/m, ""))(
    scheduledPushContent, self, {}, vi.fn());
  let work!: Promise<void>;
  const push = () => handlers.push({ data: { json: payload }, waitUntil: (p: Promise<void>) => { work = p; } });
  push(); await work;
  expect(showNotification).toHaveBeenLastCalledWith("每天的研究任务", expect.objectContaining({ body: "结果摘要", data: { session: "session", security: false } }));
  env.signature[0] ^= 1;
  push(); await work;
  expect(showNotification).toHaveBeenLastCalledWith("remote-agent", expect.objectContaining({ body: generic.body }));
});

it("falls back on missing storage, missing content, malformed/oversized payloads, or an invalid signature", async () => {
  const { env, payload } = await notification();
  expect(await scheduledPushContent({ ...payload(), content: undefined })).toEqual(generic);
  expect(await scheduledPushContent({ ...payload(), content: "invalid base64" })).toEqual(generic);
  expect(await scheduledPushContent({ ...payload(), content: "A".repeat(4000) })).toEqual(generic);
  env.signature[0] ^= 1;
  expect(await scheduledPushContent(payload())).toEqual(generic);
  const wrongKey = await notification({}, {}, false, true);
  expect(await scheduledPushContent(wrongKey.payload())).toEqual(generic);
  await rawSecurity("readwrite", (s) => s.delete("current"));
  expect(await scheduledPushContent(payload())).toEqual(generic);
});

it.each([
  { kind: NotifyKind.ATTENTION }, { machineId: "other" }, { sessionId: "other" },
  { sourceEventSeq: 8n }, { title: "" }, { text: "x".repeat(201) },
])("rejects signed content with the wrong type, route or event: %#", async (fields) => {
  const { payload } = await notification(fields);
  expect(await scheduledPushContent(payload())).toEqual(generic);
});

it("rejects an unknown future state, a stale lease, and an unavailable key", async () => {
  let n = await notification({}, { epoch: 3n });
  expect(await scheduledPushContent(n.payload())).toEqual(generic);
  n = await notification();
  const r = await loadSecurity();
  await touchSecurity(r.lease!.notAfter + 1n);
  expect(await scheduledPushContent(n.payload())).toEqual(generic);
  n = await notification();
  const good = await loadSecurity();
  await rawSecurity("readwrite", (s) => { s.put(crypto.randomUUID(), "record-revision"); return s.put({ ...good, secret: new Uint8Array(32) }, "current"); });
  expect(await scheduledPushContent(n.payload())).toEqual(generic);
});

it("does not decrypt other Web Push kinds even if ciphertext is attached", async () => {
  const { payload } = await notification();
  for (const kind of ["ATTENTION", "TURN_ENDED", "SECURITY"]) {
    expect(await scheduledPushContent({ ...payload(), tag: `session/NOTIFY_KIND_${kind}`, body: "generic" })).toEqual({ title: "remote-agent", body: "generic" });
  }
});
