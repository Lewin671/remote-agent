import { describe, it, expect } from "vitest";
import vectors from "../../internal/deviceauth/testdata/domain-v3.json";
import envelopeVector from "../../internal/e2e/testdata/v3-vectors.json";
import { concat, fromHex, hex } from "./crypto";
import { contentSecretOf } from "./device-security-store";
import {
  b64,
  fromEnvelope,
  headerBytes,
  newSecurityKey,
  openGrant,
  openPrevSecret,
  openWire,
  parseHeader,
  parseRecoveryStatement,
  parseState,
  parseWire,
  Purpose,
  recoveryBytes,
  SecurityError,
  stateBytes,
  toEnvelope,
  unb64,
  verifyGenesis,
  verifyRecovery,
  verifyStateSignature,
  verifyTransition,
  verifyWire,
  wireBytes,
  Writer,
} from "./device-security";

const state = async (s: { state: string }) => parseState(unb64(s.state));
const ph = vectors.genesis.hash;
const pin = {
  account: vectors.account,
  domain: fromHex(vectors.domain),
  genesis: fromHex(ph),
};
async function xKey(): Promise<CryptoKey> {
  return crypto.subtle.importKey(
    "pkcs8",
    concat(
      fromHex("302e020100300506032b656e04220420"),
      fromHex(vectors.phone_x),
    ),
    "X25519",
    false,
    ["deriveBits"],
  );
}

describe("protocol-10 state chain: Go interoperability", () => {
  it("verifies genesis, lease renewal and the recovery transition", async () => {
    const genesis = await state(vectors.genesis);
    const added = await state(vectors.added);
    const renewal = await state(vectors.renewal);
    const recovery = await state(vectors.recovery);
    await verifyGenesis(pin, genesis);
    expect(b64(await stateBytes(genesis))).toBe(vectors.genesis.state);
    await verifyTransition(genesis, added);
    await verifyTransition(added, renewal);
    const proof = await parseRecoveryStatement(
      unb64(vectors.recovery_statement),
    );
    expect(b64(await recoveryBytes(proof))).toBe(
      vectors.recovery_statement,
    );
    await verifyTransition(renewal, recovery, proof);
    expect(recovery.prevSecret.length).toBe(0);
    // The recovery statement binds the exact state.
    const changed = structuredClone(recovery);
    changed.core.issuedAt++;
    await expect(verifyTransition(renewal, changed, proof)).rejects.toThrow(
      SecurityError,
    );
    await expect(verifyRecovery(proof, added, recovery)).rejects.toThrow();
  });

  it("opens grants and walks PrevSecret down to the previous content key", async () => {
    const genesis = await state(vectors.genesis);
    const added = await state(vectors.added);
    const phone = genesis.core.authorityID;
    const secret2 = await openGrant(pin, added, phone, await xKey());
    expect(hex(secret2)).toBe(vectors.content_secret_2);
    const secret1 = await openPrevSecret(
      secret2,
      added.prevSecret,
    );
    expect(hex(secret1)).toBe(vectors.content_secret_1);
    await expect(
      openPrevSecret(secret2, new Uint8Array(60)),
    ).rejects.toThrow();
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
      await expect(verifyTransition(genesis, s)).rejects.toThrow();
    }
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
    const renewal = await state(vectors.renewal);
    const recovery = await state(vectors.recovery);
    const proof = await parseRecoveryStatement(
      unb64(vectors.recovery_statement),
    );
    await verifyTransition(renewal, recovery, proof);
    expect(recovery.core.authorityID).toBe(proof.newAuthorityID);
    expect(recovery.core.recoveryEd).toEqual(proof.newRecoveryEd);
  });
});

describe("protocol-10 envelope: Go interoperability", () => {
  async function vector() {
    const s = await parseState(unb64(envelopeVector.state));
    const w = parseWire(
      new Writer()
        .lp(unb64(envelopeVector.header))
        .lp(unb64(envelopeVector.nonce))
        .lp(unb64(envelopeVector.cipher))
        .lp(unb64(envelopeVector.origin))
        .bytes(),
    );
    return { s, w };
  }
  it("opens the Go header and derives the same ciphertext", async () => {
    const { s, w } = await vector();
    expect(b64(w.header)).toBe(envelopeVector.header);
    const opened = await openWire(
      {
        account: envelopeVector.account,
        domain: fromHex(envelopeVector.domain),
        genesis: new Uint8Array(32).fill(1),
      },
      s,
      s,
      w,
      fromHex(envelopeVector.content_secret),
      false,
      0n,
    );
    expect(b64(opened.plaintext)).toBe(envelopeVector.plain);
    const env = toEnvelope(w);
    expect(env.header.length).toBeGreaterThan(0);
    expect(b64(wireBytes(fromEnvelope(env)))).toBe(b64(wireBytes(w)));
    const changed = structuredClone(w);
    changed.origin[0] ^= 1;
    await expect(
      verifyWire(
        {
          account: envelopeVector.account,
          domain: fromHex(envelopeVector.domain),
          genesis: new Uint8Array(32).fill(1),
        },
        s,
        s,
        changed,
        false,
        0n,
      ),
    ).rejects.toThrow(SecurityError);
    expect(() =>
      parseWire(concat(wireBytes(w), new Uint8Array([0]))),
    ).toThrow();
  });

  it("requires a live lease for control envelopes and no lease for history", async () => {
    const { s, w } = await vector();
    const pin = {
      account: envelopeVector.account,
      domain: fromHex(envelopeVector.domain),
      genesis: new Uint8Array(32).fill(1),
    };
    const head = s.core.notAfter;
    await expect(
      verifyWire(pin, s, s, w, true, head + 1n),
    ).rejects.toMatchObject({ code: "stale" });
    await expect(verifyWire(pin, s, s, w, false, head + 1n)).resolves.toBeDefined();
  });

  it("round-trips a header and keeps every authenticated field", async () => {
    const h = parseHeader(unb64(envelopeVector.header));
    expect(h.purpose).toBe(Purpose.Command);
    expect(h.kind).toBe(5);
    expect(h.machine.length).toBe(32);
    expect(b64(headerBytes(h))).toBe(envelopeVector.header);
    const cloned = {
      ...h,
      kind: 10,
      purpose: Purpose.Notify,
      session: "session-1",
      request: "notify:1:7",
    };
    expect(parseHeader(headerBytes(cloned)).request).toBe("notify:1:7");
    expect(() => parseHeader(new Uint8Array([3]))).toThrow();
    await verifyStateSignature(await parseState(unb64(envelopeVector.state)));
    const bad = structuredClone(h) as typeof h & { contentEpoch: bigint };
    bad.contentEpoch = h.epoch + 1n;
    expect(() => headerBytes(bad)).toThrow();
  });
});
