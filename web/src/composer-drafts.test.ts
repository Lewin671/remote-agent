import { afterEach, beforeAll, beforeEach, expect, it, vi } from "vitest";
import { IDBKeyRange } from "fake-indexeddb";
import { ComposerDraftStore } from "./composer-drafts";
import { Tray } from "./attach";
import { loadLocalData, saveLocalData, forgetSecurity } from "./device-security-store";
import { rawSecurity, securityFixture, storedSecurity } from "./test/stored-security-fixture";
import type { StoredAccount } from "./storage";

beforeAll(securityFixture);
beforeEach(() => vi.stubGlobal("IDBKeyRange", IDBKeyRange));
afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers(); });

it("restores text, original source bytes, order and original mode from ciphertext, then removes deleted sources", async () => {
  const f = await storedSecurity();
  const account = { server: "https://relay.example.com", deviceId: f.memberID } as StoredAccount;
  const store = new ComposerDraftStore(account);
  const tray = new Tray(async () => ({ bytes: new Uint8Array([9]), format: "png", width: 1, height: 1 }), async () => ({ imageId: "i", expiresMs: Date.now() + 10000 }), () => {});
  tray.original = true;
  // Source bytes differ from prepared bytes: restoring must keep the source.
  tray.items = [new File(["first source"], "one.png", { type: "image/png" }), new File(["second source"], "two.png", { type: "image/png" })].map((file, i) => ({ key: String(i), file, state: "done", progress: 1 }));
  await store.save("session", "private draft", tray);
  const raw = await rawSecurity<any[]>("readonly", s => s.getAll());
  const local = raw.filter(v => v?.ciphertext && v?.binding?.includes("composer"));
  expect(local).toHaveLength(3);
  expect(local.every(v => v.key.extractable === false && !new TextDecoder().decode(v.ciphertext).includes("private draft"))).toBe(true);
  const restored = await new ComposerDraftStore(account).read("session");
  expect(restored?.text).toBe("private draft");
  expect(restored?.original).toBe(true);
  expect(await Promise.all(restored!.items.map(i => i.file.text()))).toEqual(["first source", "second source"]);
  expect(await store.read("another-session")).toBeUndefined();
  tray.items.shift();
  await store.save("session", "new text", tray);
  expect((await rawSecurity<any[]>("readonly", s => s.getAll())).filter(v => v?.binding?.includes("composer"))).toHaveLength(2);
  await store.save("session", "", new Tray(null!, null!, () => {}));
  expect(await store.read("session")).toBeUndefined();
  expect((await rawSecurity<any[]>("readonly", s => s.getAll())).filter(v => v?.binding?.includes("composer"))).toHaveLength(0);
  await store.save("session", "erase on logout");
  await forgetSecurity();
  await expect(loadLocalData(account.server, account.deviceId, 'composer/"session"')).rejects.toThrow();
});

it("discards a corrupt manifest or binding without disturbing another draft", async () => {
  const f = await storedSecurity(), account = { server: "https://relay.example.com", deviceId: f.memberID } as StoredAccount;
  const store = new ComposerDraftStore(account);
  await store.save("bad", "old"); await store.save("other", "keep");
  await saveLocalData(account.server, account.deviceId, new Map([['composer/"bad"', new TextEncoder().encode("broken json")]]));
  expect(await store.read("bad")).toBeUndefined();
  expect(await loadLocalData(account.server, account.deviceId, 'composer/"bad"')).toBeUndefined();
  await store.save("bad", "new");
  await rawSecurity("readwrite", s => { const q = s.get('local-content:composer/"bad"'); q.onsuccess = () => s.put({ ...q.result, binding: "wrong account binding" }, 'local-content:composer/"bad"'); return q; });
  expect(await store.read("bad")).toBeUndefined();
  expect((await store.read("other"))?.text).toBe("keep");
});

it("drops only a missing picture source while keeping the draft text and other images", async () => {
  const f = await storedSecurity(), account = { server: "https://relay.example.com", deviceId: f.memberID } as StoredAccount;
  const store = new ComposerDraftStore(account);
  await store.save("s", "keep text", { original: true, items: ["lost", "kept"].map((text, i) => ({ key: String(i), file: new Blob([text]), state: "done", progress: 1 })) });
  const manifest = JSON.parse(new TextDecoder().decode(await loadLocalData(account.server, account.deviceId, 'composer/"s"')));
  await rawSecurity("readwrite", s => s.delete('local-content:composer/"s"/' + manifest.sources[0].id));
  const saved = await new ComposerDraftStore(account).read("s");
  expect(saved?.text).toBe("keep text"); expect(saved?.items).toHaveLength(1); expect(await saved!.items[0].file.text()).toBe("kept");
});

it("cleans a deleted session's draft sources without disturbing other sessions", async () => {
  const f = await storedSecurity(), account = { server: "https://relay.example.com", deviceId: f.memberID } as StoredAccount;
  const store = new ComposerDraftStore(account);
  await store.save("s", "draft", { original: true, items: [{ key: "1", file: new Blob(["img"]), state: "done", progress: 1 }] });
  await store.save("other", "other");
  await store.removeSession("s");
  expect(await store.read("s")).toBeUndefined();
  expect((await store.read("other"))?.text).toBe("other");
});
