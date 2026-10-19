import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { ed25519 } from "@noble/curves/ed25519.js";
import { describe, expect as want, it } from "vitest";
import vectors from "../../internal/e2e/testdata/vectors.json";
import { Compression, EnvelopeSchema, HeaderSchema, PayloadKind } from "./gen/remoteagent/v1/envelope_pb";
import {
  accountKey,
  concat,
  deriveRaw,
  deviceID,
  expect,
  fromHex,
  hex,
  importRoot,
  importSessionKey,
  open,
  OpenError,
  PACK_MIN,
  parseSecret,
  REGISTER_CONTEXT,
  seal,
  utf8,
} from "./crypto";

describe("matches the Go implementation", () => {
  it("derives the same keys", async () => {
    const root = await importRoot(fromHex(vectors.root));
    want(hex(await deriveRaw(root, "wrap"))).toBe(vectors.wrap);
    want(hex(await deriveRaw(root, "machine-meta"))).toBe(vectors.meta);
    want(hex(await deriveRaw(root, "command"))).toBe(vectors.command);
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

  it("opens a Go envelope", async () => {
    const v = vectors.envelope;
    const key = await importSessionKey(fromHex(v.key));
    const env = create(EnvelopeSchema, { header: fromHex(v.header), nonce: fromHex(v.nonce), ciphertext: fromHex(v.ciphertext) });
    const { header, plaintext } = await open(key, env);
    want(new TextDecoder().decode(plaintext)).toBe(v.plaintext);
    expect(header, { kind: PayloadKind.EVENT, machineId: "machine", sessionId: "session", seq: 3n });
    want(() => expect(header, { kind: PayloadKind.EVENT, seq: 4n })).toThrow(OpenError);
    // Tampering with the header breaks it.
    const moved = fromHex(v.header);
    moved[moved.length - 1] ^= 1;
    await want(open(key, create(EnvelopeSchema, { ...env, header: moved }))).rejects.toThrow(OpenError);
  });

  it("opens a packed Go envelope", async () => {
    const v = vectors.packed;
    const key = await importSessionKey(fromHex(v.key));
    const env = create(EnvelopeSchema, { header: fromHex(v.header), nonce: fromHex(v.nonce), ciphertext: fromHex(v.ciphertext) });
    const { header, plaintext } = await open(key, env);
    want(header.compression).toBe(Compression.DEFLATE);
    want(new TextDecoder().decode(plaintext)).toBe(v.plaintext);
    want(env.ciphertext.length).toBeLessThan(plaintext.length);
  });

  it("packs what gets smaller, and nothing else", async () => {
    const key = await importSessionKey(crypto.getRandomValues(new Uint8Array(32)));
    const big = utf8("the same line again\n".repeat(500));
    const env = await seal(key, { kind: PayloadKind.EVENT, sessionId: "s" }, big);
    want(env.ciphertext.length).toBeLessThan(big.length / 4);
    const got = await open(key, env);
    want(got.header.compression).toBe(Compression.DEFLATE);
    want(hex(got.plaintext)).toBe(hex(big));
    const plain = async (kind: PayloadKind, bytes: Uint8Array) => {
      const e = await seal(key, { kind }, bytes);
      const o = await open(key, e);
      want(o.header.compression).toBe(Compression.NONE);
      want(hex(o.plaintext)).toBe(hex(bytes));
    };
    await plain(PayloadKind.EVENT, crypto.getRandomValues(new Uint8Array(4096)));
    await plain(PayloadKind.EVENT, utf8("a".repeat(PACK_MIN - 1)));
    await plain(PayloadKind.IMAGE, big);
  });

  it("refuses a compression it does not know and a payload that does not unpack", async () => {
    const raw = crypto.getRandomValues(new Uint8Array(32));
    const key = await importSessionKey(raw);
    const sealed = async (compression: number, plaintext: Uint8Array) => {
      const header = toBinary(HeaderSchema, create(HeaderSchema, { version: 1, kind: PayloadKind.EVENT, compression }));
      const nonce = new Uint8Array(12);
      const k = await crypto.subtle.importKey("raw", raw, "AES-GCM", false, ["encrypt"]);
      const ct = await crypto.subtle.encrypt({ name: "AES-GCM", iv: nonce, additionalData: header }, k, plaintext as Uint8Array<ArrayBuffer>);
      return create(EnvelopeSchema, { header, nonce, ciphertext: new Uint8Array(ct) });
    };
    await want(open(key, await sealed(9, utf8("x")))).rejects.toThrow(OpenError);
    await want(open(key, await sealed(Compression.DEFLATE, utf8("not deflate at all, surely")))).rejects.toThrow(OpenError);
  });

  it("refuses a payload that unpacks past the limit", async () => {
    const raw = crypto.getRandomValues(new Uint8Array(32));
    const key = await importSessionKey(raw);
    // 65 MiB of zeros deflate to a few kilobytes.
    const { deflateSync } = await import("fflate");
    const bomb = deflateSync(new Uint8Array(65 << 20));
    want(bomb.length).toBeLessThan(1 << 20);
    const header = toBinary(HeaderSchema, create(HeaderSchema, { version: 1, kind: PayloadKind.EVENT, compression: Compression.DEFLATE }));
    const nonce = new Uint8Array(12);
    const k = await crypto.subtle.importKey("raw", raw, "AES-GCM", false, ["encrypt"]);
    const ct = await crypto.subtle.encrypt({ name: "AES-GCM", iv: nonce, additionalData: header }, k, bomb as Uint8Array<ArrayBuffer>);
    await want(open(key, create(EnvelopeSchema, { header, nonce, ciphertext: new Uint8Array(ct) }))).rejects.toThrow(OpenError);
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
  const env = await seal(key, { kind: PayloadKind.COMMAND, requestId: "r" }, utf8("hi"));
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
