import { describe, it, expect } from "vitest";
import vectors from "../../internal/deviceauth/testdata/domain-v3.json";
import { concat, fromHex, hex } from "./crypto";
import {
  buildRecord,
  contentSecretOf,
  extendRecord,
} from "./device-security-store";
import {
  b64,
  newSecurityKey,
  openGrant,
  openPrevSecret,
  parseLease,
  parseRecoveryStatement,
  parseState,
  recoveryBytes,
  SecurityError,
  stateBytes,
  unb64,
  validLease,
  verifyChain,
  verifyLease,
  verifyRecovery,
} from "./device-security";

const state = async (s: { state: string }) => parseState(unb64(s.state));
const ph = vectors.genesis.hash;
const pin = {
  account: vectors.account,
  domain: fromHex(vectors.domain),
  genesis: fromHex(ph),
};
async function xKey(): Promise<CryptoKey> {
  return xFromHex(vectors.phone_x);
}
async function xFromHex(h: string): Promise<CryptoKey> {
  return crypto.subtle.importKey(
    "pkcs8",
    concat(fromHex("302e020100300506032b656e04220420"), fromHex(h)),
    "X25519",
    false,
    ["deriveBits"],
  );
}

describe("ordinary authority handoff", () => {
  it("accepts a retiring-authority signature and preserves the historical key walk", async () => {
    const genesis = await state(vectors.genesis),
      added = await state(vectors.added),
      parent = await state(vectors.handoff_parent),
      next = await state(vectors.handoff);
    await verifyChain(pin, [genesis, added, parent, next]);
    await verifyLease(parseLease(unb64(vectors.handoff_lease)), next);
    expect(
      next.core.members.some((m) => m.id === parent.core.authorityID),
    ).toBe(false);
    const secret = await openGrant(
      pin,
      next,
      next.core.authorityID,
      await xFromHex(vectors.handoff_x),
    );
    expect(await openPrevSecret(secret, next.prevSecret)).toEqual(
      new Uint8Array(32).fill(0xaa),
    );
  });
});
describe("independent signed leases", () => {
  it("renews the same head without changing membership and enforces the 72 hour bound", async () => {
    const head = await state(vectors.added),
      first = parseLease(unb64(vectors.lease)),
      renewed = parseLease(unb64(vectors.renewed_lease));
    await expect(
      validLease(first, await state(vectors.handoff), first.issuedAt),
    ).rejects.toMatchObject({ code: "lease" });
    await verifyLease(first, head);
    await verifyLease(renewed, head);
    expect(first.head).toEqual(renewed.head);
    expect(renewed.issuedAt).toBeGreaterThan(first.issuedAt);
    await expect(validLease(first, head, first.notAfter)).rejects.toMatchObject(
      { code: "lease" },
    );
    await expect(
      verifyLease(
        {
          ...first,
          notAfter: first.issuedAt + BigInt(72 * 60 * 60 * 1000) + 1n,
        },
        head,
      ),
    ).rejects.toThrow();
  });
});
describe("protocol-10 state chain: Go interoperability", () => {
  it("verifies genesis, lease renewal and the recovery transition", async () => {
    const genesis = await state(vectors.genesis);
    const added = await state(vectors.added);
    const renewal = await state(vectors.renewal);
    const recovery = await state(vectors.recovery);
    await verifyChain(pin, [genesis]);
    expect(b64(await stateBytes(genesis))).toBe(vectors.genesis.state);
    await verifyChain(pin, [genesis, added, renewal]);
    const proof = await parseRecoveryStatement(
      unb64(vectors.recovery_statement),
    );
    expect(b64(await recoveryBytes(proof))).toBe(vectors.recovery_statement);
    await verifyChain(pin, [genesis, added, renewal, recovery], [undefined, undefined, undefined, proof]);
    expect(recovery.prevSecret.length).toBe(0);
    // The recovery statement binds the exact state.
    const changed = structuredClone(recovery);
    changed.core.issuedAt++;
    await expect(
      verifyChain(pin, [genesis, added, renewal, changed], [undefined, undefined, undefined, proof]),
    ).rejects.toThrow(SecurityError);
    await expect(verifyRecovery(proof, added, recovery)).rejects.toThrow();
  });

  it("opens grants and walks PrevSecret down to the previous content key", async () => {
    const genesis = await state(vectors.genesis);
    const added = await state(vectors.added);
    const phone = genesis.core.authorityID;
    const secret2 = await openGrant(pin, added, phone, await xKey());
    expect(hex(secret2)).toBe(vectors.content_secret_2);
    const secret1 = await openPrevSecret(secret2, added.prevSecret);
    expect(hex(secret1)).toBe(vectors.content_secret_1);
    await expect(openPrevSecret(secret2, new Uint8Array(60))).rejects.toThrow();
    const other = await newSecurityKey();
    await expect(
      openGrant(pin, added, phone, other.privateKey),
    ).rejects.toThrow();
  });

  it("rejects a changed signature, member list or previous secret", async () => {
    const genesis = await state(vectors.genesis);
    const added = await state(vectors.added);
    for (const mutate of [
      (s: typeof added) => {
        s.core.members[1].x[0] ^= 1;
      },
      (s: typeof added) => {
        s.prevSecret[0] ^= 1;
      },
      (s: typeof added) => {
        s.signature[0] ^= 1;
      },
      (s: typeof added) => {
        s.grants[1].cipher[0] ^= 1;
      },
    ]) {
      const s = structuredClone(added);
      mutate(s);
      await expect(verifyChain(pin, [genesis, s])).rejects.toThrow();
    }
  });

  it("caches the keys it can derive before a recovery boundary", async () => {
    const genesis = await state(vectors.genesis);
    const added = await state(vectors.added);
    const renewal = await state(vectors.renewal);
    const recovery = await state(vectors.recovery);
    const machine = added.core.members.find((m) => m.kind === 1)!;
    const record = await buildRecord(
      pin,
      [genesis, added, renewal],
      [undefined, undefined, undefined],
      machine.id,
      await xFromHex(vectors.machine_x),
    );
    const proof = await parseRecoveryStatement(
      unb64(vectors.recovery_statement),
    );
    const next = await extendRecord(record, [recovery], [proof]);
    // The retained device keeps the keys it could read before the boundary,
    // so pre-recovery history stays readable.
    expect(next.contentKeys.map((k) => k.epoch)).toContain(2n);
    expect(next.contentKeys.map((k) => k.epoch)).toContain(3n);
    expect(hex(await contentSecretOf(next, 2n))).toBe(vectors.content_secret_2);
    expect(next.floor).toBeGreaterThanOrEqual(record.floor);
  });

  it("stops the key walk at the recovery boundary", async () => {
    const renewal = await state(vectors.renewal);
    const recovery = await state(vectors.recovery);
    const secret3 = fromHex(vectors.content_secret_3);
    await expect(
      contentSecretOf(
        {
          states: [renewal, recovery],
          secret: secret3,
          contentKeys: [],
        },
        renewal.core.contentEpoch,
      ),
    ).rejects.toMatchObject({ code: "unavailable" });
    expect(
      hex(
        await contentSecretOf(
          {
            states: [renewal, recovery],
            secret: secret3,
            contentKeys: [
              {
                epoch: renewal.core.contentEpoch,
                secret: fromHex(vectors.content_secret_2),
              },
            ],
          },
          renewal.core.contentEpoch,
        ),
      ),
    ).toBe(vectors.content_secret_2);
  });

  it("merges a recovery proof and checks the new authority", async () => {
    const genesis = await state(vectors.genesis);
    const added = await state(vectors.added);
    const renewal = await state(vectors.renewal);
    const recovery = await state(vectors.recovery);
    const proof = await parseRecoveryStatement(
      unb64(vectors.recovery_statement),
    );
    await verifyChain(pin, [genesis, added, renewal, recovery], [undefined, undefined, undefined, proof]);
    expect(recovery.core.authorityID).toBe(proof.newAuthorityID);
    expect(recovery.core.recoveryEd).toEqual(proof.newRecoveryEd);
  });
});
