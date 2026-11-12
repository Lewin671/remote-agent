import { create, toBinary } from "@bufbuild/protobuf";
import { afterEach, expect, it } from "vitest";
import { EnvelopeSchema, HeaderSchema, PayloadKind } from "./gen/remoteagent/v1/envelope_pb";
import { RETIRED_HISTORY_ERROR } from "./crypto";
import { SessionSchema } from "./gen/remoteagent/v1/local_pb";
import { SyntheticRemote } from "./test/remote-fixture";

let remote: SyntheticRemote | undefined;
afterEach(async () => { await remote?.dispose(); });
// No retired encryptor is needed: a v1 header must be refused before decryption.
function retired(kind: PayloadKind) {
  return create(EnvelopeSchema, { header: toBinary(HeaderSchema, create(HeaderSchema, { version: 1, kind })) });
}
it("keeps an old session visible with the explicit retirement error", async () => {
  const r = remote = await SyntheticRemote.create();
  const row = await r.sessionRow("old");
  row.key = retired(PayloadKind.SESSION_KEY);
  await (r as any).putSession(row);
  expect(r.sessions.get("old")?.historyError).toBe(RETIRED_HISTORY_ERROR());
  expect(r.history("old")).toMatchObject({ error: RETIRED_HISTORY_ERROR(), broken: true });
  expect(r.events("old")).toHaveLength(0);
  // A current record can replace the refused row without keeping its error.
  await (r as any).putSession(await r.sessionRow("old"));
  expect(r.sessions.get("old")?.historyError).toBeUndefined();
  expect(r.history("old").error).toBe("");
});
it("reports the same error for an old event and image", async () => {
  const r = remote = await SyntheticRemote.create();
  (r as any).api = { getEvents: async () => ({ events: [{ seq: 1n, envelope: retired(PayloadKind.EVENT) }], more: false }),
    getImage: async () => ({ image: retired(PayloadKind.IMAGE) }) };
  r.events("s");
  await new Promise<void>(resolve => {
    if (r.history("s").error) return resolve();
    const off = r.subscribe(() => { if (r.history("s").error) { off(); resolve(); } });
  });
  expect(r.history("s")).toMatchObject({ error: RETIRED_HISTORY_ERROR(), broken: true });
  expect(r.events("s")).toHaveLength(0);
  expect(await r.getImage("s", "old")).toEqual({ kind: "bad", why: RETIRED_HISTORY_ERROR() });
});

it("a retired header cannot erase a verified revision or allow metadata replay", async () => {
  const r = remote = await SyntheticRemote.create();
  const older = await r.sessionRow("known", "older title");
  const current = { ...older, meta: await r.seal(
    { kind: PayloadKind.SESSION_META, sessionId: "known", seq: 2n },
    SessionSchema, create(SessionSchema, { id: "known", title: "current title" })) };
  await (r as any).putSession(current);
  const verified = structuredClone(r.sessions.get("known"));
  for (const field of ["key", "meta"] as const) {
    await (r as any).putSession({ ...older, [field]: retired(field === "key" ? PayloadKind.SESSION_KEY : PayloadKind.SESSION_META) });
    expect(r.error).toBe(RETIRED_HISTORY_ERROR());
    expect(r.sessions.get("known")).toEqual(verified);
    await (r as any).putSession(older);
    expect(r.sessions.get("known")?.metaRev).toBe(2n);
    expect(r.sessions.get("known")?.info?.title).toBe("current title");
    expect(r.sessions.get("known")?.machineId).toBe(r.machine);
  }
});
