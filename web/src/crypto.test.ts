import * as inflation from "./inflate";
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { ed25519 } from "@noble/curves/ed25519.js";
import { describe, expect as want, it, vi } from "vitest";
import vectors from "../../internal/e2e/testdata/vectors.json";
import { Compression, EnvelopeSchema, HeaderSchema, PayloadKind } from "./gen/remoteagent/v1/envelope_pb";
import {
  accountKey,
  concat,
  deviceID,
  fromHex,
  hex,
  importRoot,
  importSessionKey,
  open,
  MAX_UNPACKED,
  OpenError,
  parseSecret,
  REGISTER_CONTEXT,
  seal,
  utf8,
} from "./crypto";

describe("matches the Go implementation", () => {
  it("derives the same keys", async () => {
    const root = await importRoot(fromHex(vectors.root));
    const acct = await accountKey(root);
    want(hex(acct.publicKey)).toBe(vectors.account_public_key);
    want(await deviceID(fromHex(vectors.account_public_key))).toBe(vectors.device_id);
  });

  it("signs registrations the same way", async () => {
    const root = await importRoot(fromHex(vectors.root));
    const acct = await accountKey(root);
    const msg = concat(utf8(REGISTER_CONTEXT), fromHex(vectors.register_signature.message));
    want(hex(acct.sign(msg))).toBe(vectors.register_signature.signature);
    want(ed25519.verify(fromHex(vectors.register_signature.signature), msg, acct.publicKey)).toBe(true);
  });

  it("accepts the exact decoded byte limit and rejects the next byte", async () => {
    const { deflateSync } = await import("fflate");
    for (const limit of [0, 1, 32]) {
      const exact = new Uint8Array(limit).fill(7);
      want(inflation.inflateBounded(deflateSync(exact), limit)).toEqual(exact);
      want(() => inflation.inflateBounded(deflateSync(new Uint8Array(limit + 1)), limit)).toThrow(RangeError);
    }
  });

  it("counts decoded bytes across multiple compressed input slices", async () => {
    const { deflateSync } = await import("fflate");
    // Incompressible data crosses several 16 KiB input slices. Unlike the
    // all-zero boundary cases, this catches resetting the count per callback.
    const plain = crypto.getRandomValues(new Uint8Array(64 << 10));
    const packed = deflateSync(plain);
    want(packed.length).toBeGreaterThan(3 * (16 << 10));
    want(inflation.inflateBounded(packed, plain.length)).toEqual(plain);
    want(() => inflation.inflateBounded(packed, plain.length - 1)).toThrow(RangeError);
  });

  it("parses the secret", () => {
    const s = parseSecret(vectors.secret);
    want(hex(s.root)).toBe(vectors.root);
    want(s.server).toBe("https://ra.example.com");
    want(() => parseSecret("ra1.abc")).toThrow();
  });
});

it("seals and opens", async () => {
  const key = await importSessionKey(crypto.getRandomValues(new Uint8Array(32)));
  const env = await seal(key, { kind: PayloadKind.PAIRING, requestId: "r" }, utf8("hi"));
  want(fromBinary(HeaderSchema, env.header).version).toBe(1);
  const { plaintext } = await open(key, env);
  want(new TextDecoder().decode(plaintext)).toBe("hi");
});

describe("pairing matches the Go implementation", async () => {
  const { newPairKey, pairTranscript, derivePair, SAS_EMOJI } = await import("./pair");
  const p = vectors.pairing;
  it("derives the same transcript and secrets", async () => {
    const init = newPairKey(fromHex(p.initiator_private));
    const join = newPairKey(fromHex(p.joiner_private));
    want(hex(init.pub)).toBe(p.initiator_public);
    want(hex(join.pub)).toBe(p.joiner_public);
    const tr = await pairTranscript(p.pairing_id, init.pub, join.pub, p.joiner_kind);
    want(hex(tr)).toBe(p.transcript);
    const a = await derivePair(init, join.pub, tr);
    const b = await derivePair(join, init.pub, tr);
    want(hex(a.sas)).toBe(p.sas);
    want(hex(a.key)).toBe(p.key);
    want(hex(b.key)).toBe(p.key);
  });
  it("uses the same emoji table", () => {
    want([...SAS_EMOJI]).toEqual(p.emoji);
  });
});

  it("refuses a compression it does not know and a payload that does not unpack", async () => {
    const raw = crypto.getRandomValues(new Uint8Array(32));
    const key = await importSessionKey(raw);
    const sealed = async (compression: number, plaintext: Uint8Array) => {
      const header = toBinary(HeaderSchema, create(HeaderSchema, { version: 1, kind: PayloadKind.PAIRING, compression }));
      const nonce = new Uint8Array(12);
      const k = await crypto.subtle.importKey("raw", raw, "AES-GCM", false, ["encrypt"]);
      const ct = await crypto.subtle.encrypt({ name: "AES-GCM", iv: nonce, additionalData: header }, k, plaintext as Uint8Array<ArrayBuffer>);
      return create(EnvelopeSchema, { header, nonce, ciphertext: new Uint8Array(ct) });
    };
    await want(open(key, await sealed(9, utf8("x")))).rejects.toThrow(OpenError);
    await want(open(key, await sealed(Compression.DEFLATE, utf8("not deflate at all, surely")))).rejects.toThrow(OpenError);
  });
  it("uses the production unpacking limit and surfaces decoder rejection", async () => {
    const raw = crypto.getRandomValues(new Uint8Array(32));
    const key = await importSessionKey(raw);
    const { deflateSync } = await import("fflate");
    // The decoder's exact boundary is tested below using small real streams.
    // Here check the actual open() wiring and error translation, without
    // allocating, compressing and inflating 65 MiB to cross a fixed policy.
    want(MAX_UNPACKED).toBe(64 << 20);
    const bomb = deflateSync(new Uint8Array(33));
    const unpack = inflation.inflateBounded;
    const decoder = vi.spyOn(inflation, "inflateBounded").mockImplementationOnce((packed, limit) => {
      want(limit).toBe(MAX_UNPACKED);
      return unpack(packed, 32);
    });
    try {
      const header = toBinary(HeaderSchema, create(HeaderSchema, { version: 1, kind: PayloadKind.PAIRING, compression: Compression.DEFLATE }));
      const nonce = new Uint8Array(12);
      const k = await crypto.subtle.importKey("raw", raw, "AES-GCM", false, ["encrypt"]);
      const ct = await crypto.subtle.encrypt({ name: "AES-GCM", iv: nonce, additionalData: header }, k, bomb as Uint8Array<ArrayBuffer>);
      await want(open(key, create(EnvelopeSchema, { header, nonce, ciphertext: new Uint8Array(ct) }))).rejects.toThrow(OpenError);
      want(decoder).toHaveBeenCalledExactlyOnceWith(bomb, MAX_UNPACKED);
    } finally { decoder.mockRestore(); }
  });