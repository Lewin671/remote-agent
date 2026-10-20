import { afterEach, beforeAll, expect, it, vi } from "vitest";
import { securityFixture, storedSecurity, rawSecurity } from "./test/stored-security-fixture";
import { clearLoginToken, contentSecretOf, forgetSecurity, initializeSecurity, installSecurity, loadLoginToken, loadSecurity, saveLoginToken } from "./device-security-store";
import { forgetAccount, saveAccount } from "./storage";

beforeAll(securityFixture);

const server = "https://relay.example.com";
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.useRealTimers(); });

it("persists only encrypted credentials with a non-extractable key and reuses them after a page reload", async () => {
  const f = await storedSecurity();
  const token = { token: "synthetic-token", expiresMs: Date.now() + 10 * 86_400_000 };
  await saveLoginToken(server, f.memberID, token);
  const raw = await rawSecurity<any>("readonly", s => s.get("login-token"));
  expect(raw.token).toBeUndefined();
  expect(raw.key.extractable).toBe(false);
  await expect(crypto.subtle.exportKey("raw", raw.key)).rejects.toThrow();
  expect(new TextDecoder().decode(raw.ciphertext)).not.toContain(token.token);
  // A fresh module has no in-memory token or verified-record cache.
  vi.resetModules();
  const reloaded = await import("./device-security-store");
  expect(await reloaded.loadLoginToken(server, f.memberID)).toEqual(token);
  expect(await reloaded.loadLoginToken("https://other.example.com", f.memberID)).toBeUndefined();
  expect(await reloaded.loadLoginToken(server, "other-device")).toBeUndefined();
  await clearLoginToken(server, "other-device");
  expect(await reloaded.loadLoginToken(server, f.memberID)).toEqual(token);
});

it("discards expired, nearly expired, malformed and modified tokens", async () => {
  const f = await storedSecurity();
  for (const lifetime of [-1, 60_000, 86_400_000 - 1]) {
    await saveLoginToken(server, f.memberID, { token: "expired", expiresMs: Date.now() + lifetime });
    expect(await loadLoginToken(server, f.memberID)).toBeUndefined();
    expect(await rawSecurity("readonly", s => s.get("login-token"))).toBeUndefined();
  }
  await saveLoginToken(server, f.memberID, { token: "modified", expiresMs: Date.now() + 10 * 86_400_000 });
  const raw = await rawSecurity<any>("readonly", s => s.get("login-token"));
  new Uint8Array(raw.ciphertext)[0] ^= 1;
  await rawSecurity("readwrite", s => s.put(raw, "login-token"));
  expect(await loadLoginToken(server, f.memberID)).toBeUndefined();
});

it("clears credentials on rejection, reset/discard and explicit logout", async () => {
  const f = await storedSecurity();
  const token = { token: "synthetic-token", expiresMs: Date.now() + 10 * 86_400_000 };
  const present = () => rawSecurity("readonly", s => s.get("login-token"));
  await saveLoginToken(server, f.memberID, token);
  await clearLoginToken(server, f.memberID);
  expect(await present()).toBeUndefined();
  await saveLoginToken(server, f.memberID, token);
  await forgetSecurity();
  await initializeSecurity(f.pin, f.states, [undefined, undefined], f.memberID, f.x);
  expect(await loadLoginToken(server, f.memberID)).toBeUndefined();
  await saveLoginToken(server, f.memberID, token);
  await saveAccount({ server, deviceId: f.memberID, devicePrivate: f.ed });
  await forgetAccount();
  await expect(loadLoginToken(server, f.memberID)).rejects.toThrow();
  // A login completing after logout cannot recreate the erased database.
  await expect(saveLoginToken(server, f.memberID, token)).rejects.toThrow();
});

it("erases credentials atomically with an authenticated device removal while retaining historical keys", async () => {
  const f = await storedSecurity();
  await saveLoginToken(server, f.memberID, { token: "removed-token", expiresMs: Date.now() + 10 * 86_400_000 });
  const retained = await contentSecretOf(await loadSecurity(), 2n);
  vi.setSystemTime(f.removal.now);
  // The Go-generated recovery statement removes this member. Install it via
  // the real transaction path; neither membership nor verification is mocked.
  await installSecurity(await loadSecurity(), f.removal.states, f.removal.proofs);
  const removed = await loadSecurity();
  expect(removed.member).toBe(false);
  expect(await rawSecurity("readonly", s => s.get("login-token"))).toBeUndefined();
  expect(await loadLoginToken(server, f.memberID)).toBeUndefined();
  expect(await contentSecretOf(removed, 2n)).toEqual(retained);
  await expect(saveLoginToken(server, f.memberID, { token: "late", expiresMs: Date.now() + 10 * 86_400_000 })).rejects.toThrow();
});
