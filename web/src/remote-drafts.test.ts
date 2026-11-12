import { afterEach, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { Remote, CommandError } from "./remote";
import { Tray } from "./attach";
import { ComposerDraftStore, type RestoredDraft } from "./composer-drafts";
import type { StoredAccount } from "./storage";
import { EventSchema } from "./gen/remoteagent/v1/event_pb";
import type { Outgoing } from "./model";
class Probe extends Remote { constructor() { super({ server: "https://relay.example.com", deviceId: "client" } as StoredAccount); } }
const remotes: Remote[] = [];
afterEach(() => { for (const r of remotes.splice(0)) r.stop(); vi.restoreAllMocks(); });
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(r => resolve = r); return { promise, resolve }; }
function setup() {
  const r = new Probe(); remotes.push(r);
  const store = new ComposerDraftStore(r.account);
  const read = vi.spyOn(store, "read").mockResolvedValue(undefined);
  const readOutbox = vi.spyOn(store, "readOutbox").mockResolvedValue([]);
  const save = vi.spyOn(store, "save").mockResolvedValue();
  const saveOutbox = vi.spyOn(store, "saveOutbox").mockResolvedValue();
  Object.defineProperty(r, "localDrafts", { value: store });
  const tray = new Tray(async () => ({ bytes: new Uint8Array([1]), format: "png", width: 1, height: 1 }), async () => ({ imageId: "uploaded", expiresMs: Date.now() + 60000 }), () => { r.persistDraft("s"); });
  tray.machineId = "computer"; r.trays.set("s", tray);
  const forward = vi.spyOn(r, "sendMessage").mockRejectedValue(new CommandError("unavailable", "offline"));
  return { r, store, tray, read, readOutbox, save, saveOutbox, forward };
}
it("forwards sends and retries despite failed outbox reads and writes, and retries a failed read", async () => {
  const t = setup(); t.readOutbox.mockRejectedValueOnce(new Error("temporary read failure"));
  t.saveOutbox.mockRejectedValue(new DOMException("full", "QuotaExceededError"));
  const id = t.r.send("s", "first");
  await vi.waitFor(() => expect(t.r.outbox("s")[0].status).toBe("failed"));
  t.r.retrySend("s", id); await vi.waitFor(() => expect(t.forward).toHaveBeenCalledTimes(2));
  t.r.send("s", "next"); await vi.waitFor(() => expect(t.forward).toHaveBeenCalledTimes(3));
  expect(t.readOutbox.mock.calls.length).toBeGreaterThan(1);
});
it("keeps saved pictures and merges text typed during restoration before writing anything", async () => {
  const t = setup(), gate = deferred<RestoredDraft>(); t.read.mockReturnValue(gate.promise);
  const restoring = t.r.restoreDraft("s", t.tray); t.r.drafts.set("s", "typed now");
  expect(t.save).not.toHaveBeenCalled();
  gate.resolve({ text: "saved text", original: true, items: [{ file: new Blob(["source"]) }] }); await restoring;
  expect(t.r.drafts.get("s")).toBe("typed now"); expect(t.tray.items).toHaveLength(1); expect(t.tray.original).toBe(true);
  await vi.waitFor(() => expect(t.save.mock.calls.at(-1)?.[1]).toBe("typed now")); expect(t.save.mock.calls.at(-1)?.[2]?.items).toHaveLength(1);
});
it("does not cache failed draft reads", async () => {
  const t = setup(); t.read.mockRejectedValueOnce(new Error("read failed"));
  await expect(t.r.restoreDraft("s", t.tray)).rejects.toThrow(); expect(t.tray.restoring).toBe(false);
  t.read.mockResolvedValue({ text: "saved", original: false, items: [] }); await t.r.restoreDraft("s", t.tray);
  expect(t.read).toHaveBeenCalledTimes(2); expect(t.r.drafts.get("s")).toBe("saved");
});
it("keeps the text draft until the pending write succeeds and retains it when writing fails", async () => {
  const t = setup(); await t.r.restoreDraft("s", t.tray); t.r.drafts.set("s", "message");
  t.save.mockClear(); const gate = deferred<void>(); t.saveOutbox.mockReturnValue(gate.promise);
  t.r.send("s", "message"); t.r.drafts.set("s", ""); await Promise.resolve(); await Promise.resolve();
  expect(t.save).not.toHaveBeenCalled(); gate.resolve();
  await vi.waitFor(() => expect(t.save).toHaveBeenCalledWith("s", "", expect.anything()));
  t.r.drafts.set("s", "next message"); t.save.mockClear(); t.saveOutbox.mockRejectedValue(new Error("disk full"));
  t.r.send("s", "next message"); t.r.drafts.set("s", "");
  await vi.waitFor(() => expect(t.tray.note).toContain("本机")); expect(t.save).not.toHaveBeenCalled();
  t.saveOutbox.mockResolvedValue();
  t.r.drafts.set("s", "editing after storage recovers");
  await vi.waitFor(() => expect(t.save).toHaveBeenCalledWith("s", "editing after storage recovers", expect.anything()));
});
it("does not discard a put-back owner before the draft copy lands, including text-only sends", async () => {
  const t = setup(); await t.r.restoreDraft("s", t.tray); const id = t.r.send("s", "text only");
  expect(t.r.canRestoreAttachments("s", id)).toBe(true);
  const gate = deferred<void>(); t.save.mockReturnValue(gate.promise);
  t.r.drafts.set("s", "text only"); t.r.dismissSend("s", id, true); expect(t.r.outbox("s")).toHaveLength(1);
  gate.resolve(); await vi.waitFor(() => expect(t.r.outbox("s")).toEqual([]));
});
it("reconciles restored sends against verified tail events before offering them again", async () => {
  const t = setup(); const event = create(EventSchema, { seq: 99n, kind: { case: "userMessage", value: { requestId: "sent" } } });
  Object.defineProperty(t.r, "buffers", { value: new Map([["s", { events: [], pending: new Map([[99n, event]]), recent: [event], verifiedSeqs: new Set([99n]) }]]) });
  t.readOutbox.mockResolvedValue([{ sendId: "sent", requestId: "cmd", text: "already sent", deadline: 1, status: "sending" } as Outgoing]);
  await t.r.restoreDraft("s", t.tray); await vi.waitFor(() => expect(t.readOutbox).toHaveBeenCalled()); expect(t.r.outbox("s")).toEqual([]);
});
it("never persists schedule editor text", async () => {
  const t = setup(); t.r.trays.set("schedule:job", t.tray); t.r.drafts.set("schedule:job", "edit once");
  await t.r.restoreDraft("schedule:job", t.tray); expect(t.read).not.toHaveBeenCalled(); expect(t.save).not.toHaveBeenCalled();
});


it("forgets local drafts and pending sends with a removed session, even during restoration", async () => {
  const t = setup(), gate = deferred<Outgoing[]>(); t.readOutbox.mockReturnValue(gate.promise);
  const remove = vi.spyOn(t.store, "removeSession").mockResolvedValue();
  const restored = t.r.restoreDraft("s", t.tray); await restored;
  t.r.drafts.set("s", "draft"); t.r.send("s", "pending");
  (t.r as unknown as { forget(id: string): void }).forget("s");
  gate.resolve([{ sendId: "old", requestId: "cmd", text: "old", deadline: 1, status: "sending" }]);
  await vi.waitFor(() => expect(remove).toHaveBeenCalledWith("s"));
  expect(t.r.outbox("s")).toEqual([]); expect(t.r.drafts.has("s")).toBe(false); expect(t.r.trays.has("s")).toBe(false);
});
