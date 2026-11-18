import { afterEach, expect, it, vi } from "vitest";
import { Remote } from "./remote";
import { Tray } from "./attach";
import { ComposerDraftStore, type RestoredDraft } from "./composer-drafts";
import type { StoredAccount } from "./storage";

class Probe extends Remote {
  constructor() {
    super({ server: "https://relay.example.com", deviceId: "client" } as StoredAccount);
  }
}

const remotes: Remote[] = [];
afterEach(() => {
  for (const r of remotes.splice(0)) r.stop();
  vi.restoreAllMocks();
});

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
}

function setup() {
  const r = new Probe();
  remotes.push(r);
  const store = new ComposerDraftStore(r.account);
  const read = vi.spyOn(store, "read").mockResolvedValue(undefined);
  const save = vi.spyOn(store, "save").mockResolvedValue();
  Object.defineProperty(r, "localDrafts", { value: store });
  const tray = new Tray(
    async () => ({ bytes: new Uint8Array([1]), format: "png", width: 1, height: 1 }),
    async () => ({ imageId: "uploaded", expiresMs: Date.now() + 60000 }),
    () => {
      r.persistDraft("s");
    },
  );
  tray.machineId = "computer";
  r.trays.set("s", tray);
  return { r, store, tray, read, save };
}

it("keeps saved pictures and merges text typed during restoration before writing anything", async () => {
  const t = setup();
  const gate = deferred<RestoredDraft>();
  t.read.mockReturnValue(gate.promise);
  const restoring = t.r.restoreDraft("s", t.tray);
  t.r.drafts.set("s", "typed now");
  expect(t.save).not.toHaveBeenCalled();
  gate.resolve({ text: "saved text", original: true, items: [{ file: new Blob(["source"]) }] });
  await restoring;
  expect(t.r.drafts.get("s")).toBe("typed now");
  expect(t.tray.items).toHaveLength(1);
  expect(t.tray.original).toBe(true);
  await vi.waitFor(() => expect(t.save.mock.calls.at(-1)?.[1]).toBe("typed now"));
  expect(t.save.mock.calls.at(-1)?.[2]?.items).toHaveLength(1);
});

it("does not cache failed draft reads", async () => {
  const t = setup();
  t.read.mockRejectedValueOnce(new Error("read failed"));
  await expect(t.r.restoreDraft("s", t.tray)).rejects.toThrow();
  expect(t.tray.restoring).toBe(false);
  t.read.mockResolvedValue({ text: "saved", original: false, items: [] });
  await t.r.restoreDraft("s", t.tray);
  expect(t.read).toHaveBeenCalledTimes(2);
  expect(t.r.drafts.get("s")).toBe("saved");
});

it("forgets local drafts with a removed session", async () => {
  const t = setup();
  const remove = vi.spyOn(t.store, "removeSession").mockResolvedValue();
  await t.r.restoreDraft("s", t.tray);
  t.r.drafts.set("s", "draft");
  const { CacheDeleteEffectSchema } = await import("./gen/remoteagent/core/v1/app_pb");
  await t.r.deleteCache(createDeleteEffect("s", CacheDeleteEffectSchema));
  await vi.waitFor(() => expect(remove).toHaveBeenCalledWith("s"));
  expect(t.r.drafts.has("s")).toBe(false);
  expect(t.r.trays.has("s")).toBe(false);
});

function createDeleteEffect(
  sessionId: string,
  schema: typeof import("./gen/remoteagent/core/v1/app_pb").CacheDeleteEffectSchema,
) {
  return {
    $typeName: schema.typeName,
    kind: { case: "removeSessionId" as const, value: sessionId },
  };
}
