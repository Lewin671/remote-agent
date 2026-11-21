import { afterEach, expect, it, vi } from "vitest";
import vectors from "../../internal/deviceauth/testdata/domain-v3.json";
import { concat, fromHex } from "./crypto";
import { AuthoritySecurity } from "./authority-security";
import { buildRecord } from "./device-security-store";
import { MemorySecurityStorage } from "./test/security-fixture";
import {
  SecurityError,
  parseLease,
  parseState,
  unb64,
  validLease,
} from "./device-security";
import { errorText } from "./remote";

afterEach(() => vi.useRealTimers());

it("enforces lease expiry and persisted floor through AuthoritySecurity sync", async () => {
  const states = await Promise.all(
    [vectors.genesis, vectors.added].map((s) => parseState(unb64(s.state))),
  );
  const head = states[1];
  const member = head.core.members.find((m) => m.kind === 2)!;
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
  const record = await buildRecord(
    {
      account: vectors.account,
      domain: fromHex(vectors.domain),
      genesis: fromHex(vectors.genesis.hash),
    },
    states,
    [undefined, undefined],
    member.id,
    x,
  );
  const first = parseLease(unb64(vectors.lease));
  record.lease = first;
  record.floor = first.issuedAt;
  const storage = new MemorySecurityStorage(record);
  const sdk = new AuthoritySecurity(
    member.id,
    undefined,
    async () => ({
      states: [],
      headEpoch: head.core.epoch,
      lease: unb64(vectors.renewed_lease),
    }),
    () => true,
    storage,
  );

  vi.useFakeTimers();
  vi.setSystemTime(Number(first.issuedAt));
  await expect(validLease(first, head, first.issuedAt)).resolves.toBeUndefined();
  await expect(validLease(first, head, first.notAfter)).rejects.toMatchObject({
    code: "lease",
  });
  // A persisted floor prevents a clock rollback from reopening publication.
  await storage.touchSecurity(first.notAfter);
  const rolledBack = await sdk.verifiedRecord();
  await expect(
    validLease(rolledBack.lease, rolledBack.states.at(-1)!, rolledBack.floor),
  ).rejects.toMatchObject({ code: "lease" });

  vi.setSystemTime(Number(first.notAfter));
  const synced = await sdk.sync();
  await expect(
    validLease(synced.lease, synced.states.at(-1)!, synced.floor),
  ).resolves.toBeUndefined();
  expect((await storage.loadSecurity()).states).toHaveLength(2);
});

it("explains that expired leases prevent saving changes while history stays readable", () => {
  const text = errorText(new SecurityError("lease"));
  expect(text).toContain("无法保存");
  expect(text).toContain("续期");
  expect(text).toContain("历史");
});
