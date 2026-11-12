import { afterEach, beforeAll, expect, it, vi } from "vitest";
import { ComposerDraftStore } from "./composer-drafts";
import { Tray } from "./attach";
import { loadLocalData, forgetSecurity } from "./device-security-store";
import { rawSecurity, securityFixture, storedSecurity } from "./test/stored-security-fixture";
import type { StoredAccount } from "./storage";

beforeAll(securityFixture);
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
