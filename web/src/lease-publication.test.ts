import { afterEach, expect, it, vi } from "vitest";
import vectors from "../../internal/deviceauth/testdata/domain-v3.json";
import { concat, fromHex } from "./crypto";
import {
  DeviceSecurityClient,
  type NewSecurityHeader,
} from "./device-security-client";
import { buildRecord } from "./device-security-store";
import { MemorySecurityStorage } from "./test/security-fixture";
import {
  Purpose,
  SecurityError,
  parseLease,
  parseState,
  unb64,
} from "./device-security";
import { errorText } from "./remote";

afterEach(() => vi.useRealTimers());
const purposes = Object.values(Purpose).filter(
  (p): p is Purpose => typeof p === "number",
);
const controls = [
  Purpose.Command,
  Purpose.TerminalInput,
  Purpose.TerminalResize,
  Purpose.TerminalHistory,
];
it.each(purposes)(
  "requires a live lease for every new publication: purpose %s",
  async (purpose) => {
    const states = await Promise.all(
      [vectors.genesis, vectors.added].map((s) => parseState(unb64(s.state))),
    );
    const head = states[1];
    const isClient = [Purpose.Prefs, Purpose.DeviceMeta, ...controls].includes(
      purpose,
    );
    const member = head.core.members.find(
      (m) => m.kind === (isClient ? 2 : 1),
    )!;
    const x = await crypto.subtle.importKey(
      "pkcs8",
      concat(
        fromHex("302e020100300506032b656e04220420"),
        fromHex(isClient ? vectors.phone_x : vectors.machine_x),
      ),
      "X25519",
      false,
      ["deriveBits"],
    );
    const signer = await crypto.subtle.importKey(
      "pkcs8",
      concat(
        fromHex("302e020100300506032b657004220420"),
        fromHex(isClient ? vectors.phone_seed : vectors.machine_seed),
      ),
      "Ed25519",
      false,
      ["sign"],
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
    const sdk = new DeviceSecurityClient(
      member.id,
      signer,
      async () => ({
        states: [],
        headEpoch: head.core.epoch,
        lease: unb64(vectors.renewed_lease),
      }),
      () => true,
      storage,
    );
    const standalone = [Purpose.Prefs, Purpose.DeviceMeta].includes(purpose);
    const noSession =
      standalone ||
      [Purpose.MachineMeta, Purpose.Command, Purpose.Result].includes(purpose);
    const noRequest = [
      Purpose.Event,
      Purpose.SessionMeta,
      Purpose.SessionKey,
      Purpose.MachineMeta,
    ].includes(purpose);
    const h: NewSecurityHeader = {
      purpose,
      kind: purpose >= 14 ? 8 : purpose,
      machine: standalone
        ? ""
        : head.core.members.find((m) => m.kind === 1)!.id,
      session: noSession ? "" : "lease-session",
      request:
        purpose === Purpose.Prefs
          ? "project-prefs"
          : purpose === Purpose.DeviceMeta
            ? member.id
            : noRequest
              ? ""
              : "lease-request-123456",
      seq: 1n,
      deadline: controls.includes(purpose) ? first.notAfter + 60000n : 0n,
      compression: 0,
    };
    const plain = new Uint8Array([1, 2, 3]);
    const seed = new Uint8Array(32).fill(7);
    vi.useFakeTimers();
    vi.setSystemTime(Number(first.issuedAt));
    const stored = await sdk.seal(h, plain, seed);
    vi.setSystemTime(Number(first.notAfter));
    await expect(sdk.seal(h, plain, seed)).rejects.toMatchObject({
      code: "lease",
    });
    if (!controls.includes(purpose))
      expect((await sdk.openHistory(stored, seed)).plaintext).toEqual(plain);
    // A persisted floor prevents a clock rollback from reopening publication.
    await storage.touchSecurity(first.notAfter);
    vi.setSystemTime(Number(first.issuedAt));
    await expect(sdk.seal(h, plain, seed)).rejects.toMatchObject({
      code: "lease",
    });
    vi.setSystemTime(Number(first.notAfter));
    await sdk.sync();
    await expect(sdk.seal(h, plain, seed)).resolves.toBeDefined();
    expect((await storage.loadSecurity()).states).toHaveLength(2);
  },
);

it("explains that expired leases prevent saving changes while history stays readable", () => {
  const text = errorText(new SecurityError("lease"));
  expect(text).toContain("无法保存");
  expect(text).toContain("续期");
  expect(text).toContain("历史");
});
