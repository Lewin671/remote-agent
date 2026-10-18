import { describe, it, expect } from "vitest";
import vectors from "../../internal/deviceauth/testdata/domain-v2.json";
import { concat, fromHex, hex } from "./crypto";
import {
  advance,
  attachAuthorization,
  b64,
  capsuleBytes,
  commitment,
  deriveContentKey,
  equal,
  formCipher,
  fromEnvelope,
  headerBytes,
  newSecurityKey,
  openGrant,
  openWire,
  parseAuthorization,
  parseCapsule,
  parseHeader,
  parseState,
  parseWire,
  pinFromJSON,
  replyRPC,
  requestRPC,
  SecurityError,
  Purpose,
  stateBytes,
  stateHash,
  toEnvelope,
  unb64,
  verifyState,
  verifyWire,
  wireBytes,
  Writer,
  type RPCWaiter,
  type SecureWire,
} from "./device-security";
const pin = pinFromJSON(vectors.Pin);
async function xKey(): Promise<CryptoKey> {
  return crypto.subtle.importKey(
    "pkcs8",
    concat(
      fromHex("302e020100300506032b656e04220420"),
      fromHex(vectors.WriterX),
    ),
    "X25519",
    false,
    ["deriveBits"],
  );
}
async function edKey(): Promise<CryptoKey> {
  return crypto.subtle.importKey(
    "pkcs8",
    concat(
      fromHex("302e020100300506032b657004220420"),
      fromHex(vectors.WriterSeed),
    ),
    "Ed25519",
    false,
    ["sign"],
  );
}
async function state() {
  return parseState(unb64(vectors.NextState));
}
function wire() {
  return parseWire(unb64(vectors.Wire));
}

describe("pinned authority security protocol: Go interoperability", () => {
  it("verifies the same canonical genesis, contiguous state and grants", async () => {
    const genesis = await parseState(unb64(vectors.State));
    const next = await state();
    await verifyState(pin, genesis);
    await advance(pin, genesis, next);
    expect(equal(await stateHash(genesis), pin.genesis)).toBe(true);
    expect(b64(await stateBytes(next))).toBe(vectors.NextState);
    const secret = await openGrant(
      pin,
      next,
      vectors.RecipientID,
      await xKey(),
    );
    expect(hex(secret)).toBe(vectors.Secret);
    await expect(advance(pin, next, genesis)).rejects.toThrow(SecurityError);
    const other = await newSecurityKey();
    await expect(
      openGrant(pin, next, vectors.RecipientID, other.privateKey),
    ).rejects.toThrow();
    await expect(
      crypto.subtle.exportKey("pkcs8", other.privateKey),
    ).rejects.toThrow();
    const changed = structuredClone(next);
    changed.core.authorityEd[0] ^= 1;
    await expect(verifyState(pin, changed)).rejects.toThrow(SecurityError);
  });

  it("opens Go ciphertext and derives exactly the same isolated message key", async () => {
    const s = await state(),
      w = wire();
    expect(b64(wireBytes(w))).toBe(vectors.Wire);
    expect(b64(headerBytes(parseHeader(unb64(vectors.Header))))).toBe(
      vectors.Header,
    );
    const opened = await openWire(
      pin,
      s,
      w,
      fromHex(vectors.Secret),
      fromHex(vectors.SessionSeed),
    );
    expect(b64(opened.plaintext)).toBe(vectors.Plain);
    const key = await deriveContentKey(
      fromHex(vectors.Secret),
      fromHex(vectors.SessionSeed),
      opened.header,
    );
    const exact = await crypto.subtle.encrypt(
      { name: "AES-GCM", iv: w.nonce, additionalData: w.header },
      key,
      opened.plaintext,
    );
    expect(b64(new Uint8Array(exact))).toBe(b64(w.cipher));
    const expected = await crypto.subtle.importKey(
      "raw",
      fromHex(vectors.Key),
      "AES-GCM",
      false,
      ["decrypt"],
    );
    expect(
      b64(
        new Uint8Array(
          await crypto.subtle.decrypt(
            { name: "AES-GCM", iv: w.nonce, additionalData: w.header },
            expected,
            w.cipher,
          ),
        ),
      ),
    ).toBe(vectors.Plain);
    await expect(crypto.subtle.exportKey("raw", key)).rejects.toThrow();
    const nonextractableSecret = await crypto.subtle.importKey(
      "raw",
      fromHex(vectors.Secret),
      "HKDF",
      false,
      ["deriveBits"],
    );
    const held = await openWire(
      pin,
      s,
      w,
      nonextractableSecret,
      fromHex(vectors.SessionSeed),
    );
    expect(b64(held.plaintext)).toBe(vectors.Plain);
  });

  it("checks both exact signatures, including a signature-valid wrong scope", async () => {
    const s = await state();
    const mutations: ((w: SecureWire) => void)[] = [
      (w) => {
        w.nonce[0] ^= 1;
      },
      (w) => {
        w.cipher[0] ^= 1;
      },
      (w) => {
        w.origin[0] ^= 1;
      },
      (w) => {
        w.authSignature[0] ^= 1;
      },
      (w) => {
        w.authorization[20] ^= 1;
      },
    ];
    for (const mutate of mutations) {
      const w = wire();
      mutate(w);
      await expect(verifyWire(pin, s, w)).rejects.toThrow();
    }
    const w = wire(),
      auth = parseAuthorization(w.authorization);
    auth.header.seq++;
    // Supply a real authority signature for the SAME commitment but a different
    // signed header. A commit-only verifier would incorrectly accept this.
    const hb = headerBytes(auth.header);
    const hh = new Uint8Array(await crypto.subtle.digest("SHA-256", hb));
    w.authorization = new Writer()
      .lp(hb)
      .u32(auth.writerKind)
      .raw(hh)
      .raw(auth.commit)
      .raw(auth.nonce)
      .bytes();
    const fakeAuthority = await crypto.subtle.importKey(
      "pkcs8",
      concat(
        fromHex("302e020100300506032b657004220420"),
        new Uint8Array(32).fill(0x41),
      ),
      "Ed25519",
      false,
      ["sign"],
    );
    w.authSignature = new Uint8Array(
      await crypto.subtle.sign(
        "Ed25519",
        fakeAuthority,
        concat(new TextEncoder().encode("ra2/write-auth\0"), w.authorization),
      ),
    );
    await expect(verifyWire(pin, s, w)).rejects.toThrow(SecurityError);
    const env = toEnvelope(wire());
    expect(b64(wireBytes(fromEnvelope(env)))).toBe(vectors.Wire);
    const badEnv = structuredClone(env);
    badEnv.securityHeader[0] ^= 1;
    expect(() => fromEnvelope(badEnv)).toThrow();
  });

  it("decrypts a Go HPKE RPC reply with the exact nonce and request binding", async () => {
    const capsule = {
      from: vectors.Reply.From,
      to: vectors.Reply.To,
      method: vectors.Reply.Method,
      enc: unb64(vectors.Reply.Enc),
      cipher: unb64(vectors.Reply.Cipher),
    };
    expect(b64(capsuleBytes(parseCapsule(capsuleBytes(capsule))))).toBe(
      b64(capsuleBytes(capsule)),
    );
    const waiter: RPCWaiter = {
      pin,
      sender: vectors.Waiter.Sender,
      method: vectors.Waiter.Method,
      nonce: new Uint8Array(vectors.Waiter.Nonce),
      requestHash: new Uint8Array(vectors.Waiter.RequestHash),
    };
    const reply = await replyRPC<{ Authorization: string; Signature: string }>(
      waiter,
      capsule,
      await xKey(),
    );
    await attachAuthorization(pin, await state(), wire(), reply);
    const other = structuredClone(waiter);
    other.nonce[0] ^= 1;
    await expect(replyRPC(other, capsule, await xKey())).rejects.toThrow(
      SecurityError,
    );
    const key = await edKey();
    const request = await requestRPC(pin, vectors.RecipientID, key, 1, {
      Header: vectors.Header,
    });
    expect(request.capsule.from).toBe(vectors.RecipientID);
    expect(request.capsule.cipher.length).toBeGreaterThan(16);
  });

  it("uses distinct keys on every durable message counter and rejects malformed encodings", async () => {
    const h = parseHeader(unb64(vectors.Header));
    const secret = fromHex(vectors.Secret),
      seed = fromHex(vectors.SessionSeed);
    const c = await formCipher(
      secret,
      seed,
      h,
      unb64(vectors.Plain),
      await edKey(),
    );
    expect((await commitment(c)).length).toBe(32);
    h.counter++;
    const c2 = await formCipher(
      secret,
      seed,
      h,
      unb64(vectors.Plain),
      await edKey(),
    );
    expect(equal(c.header, c2.header)).toBe(false);
    const raw = unb64(vectors.Header);
    for (let i = 0; i < raw.length; i++)
      expect(() => parseHeader(raw.slice(0, i))).toThrow();
    expect(() => parseHeader(concat(raw, new Uint8Array([0])))).toThrow();
    const malformed = unb64(vectors.NextState);
    malformed[0] = 255;
    await expect(parseState(malformed)).rejects.toThrow();
  });
});

it("accepts the same colon-separated notification routing ID as Go", () => {
  const original = parseHeader(unb64(vectors.Header));
  const header = {
    ...original,
    kind: 10,
    purpose: Purpose.Notify,
    request: "notify:1:7",
  };
  expect(parseHeader(headerBytes(header)).request).toBe("notify:1:7");
});
