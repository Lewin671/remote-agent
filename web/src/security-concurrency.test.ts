import { IDBFactory } from "fake-indexeddb";
import { afterEach, expect, it, vi } from "vitest";
import vectors from "../../internal/deviceauth/testdata/domain-v3.json";
import { concat, fromHex } from "./crypto";
import { parseState, parseLease, unb64 } from "./device-security";
import {
  initializeSecurity,
  installSecurity,
  loadSecurity,
  touchSecurity,
} from "./device-security-store";
afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});
it("compares same-head leases with the transaction's record and preserves concurrent floor updates", async () => {
  vi.stubGlobal("indexedDB", new IDBFactory());
  const states = await Promise.all(
    [vectors.genesis, vectors.added].map((v) => parseState(unb64(v.state))),
  );
  const x = await crypto.subtle.importKey(
    "pkcs8",
    concat(
      fromHex("302e020100300506032b656e04220420"),
      fromHex(vectors.phone_x),
    ),
    "X25519",
    false,
    ["deriveBits"],
  );
  const first = parseLease(unb64(vectors.lease));
  const renewed = parseLease(unb64(vectors.renewed_lease));
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(Number(renewed.issuedAt));
  await initializeSecurity(
    {
      account: vectors.account,
      domain: fromHex(vectors.domain),
      genesis: fromHex(vectors.genesis.hash),
    },
    states,
    [undefined, undefined],
    states[0].core.authorityID,
    x,
    false,
    first,
  );
  const stale = await loadSecurity();
  await installSecurity(stale, [], [], renewed);
  await touchSecurity(renewed.notAfter);
  await expect(installSecurity(stale, [], [], first)).rejects.toThrow();
  expect((await loadSecurity()).lease).toEqual(renewed);
  await installSecurity(stale, [], []);
  expect((await loadSecurity()).lease).toEqual(renewed);
  expect((await loadSecurity()).floor).toBe(renewed.notAfter);
  const pin = (await loadSecurity()).pin;
  const later = await parseState(unb64(vectors.renewal.state));
  await initializeSecurity(
    pin,
    [...states, later],
    [undefined, undefined, undefined],
    states[0].core.authorityID,
    x,
    true,
  );
  expect((await loadSecurity()).floor).toBe(renewed.notAfter);
  await initializeSecurity(
    pin,
    states,
    [undefined, undefined],
    states[0].core.authorityID,
    x,
    true,
  );
  expect((await loadSecurity()).states).toHaveLength(3);
  expect((await loadSecurity()).floor).toBe(renewed.notAfter);
});
