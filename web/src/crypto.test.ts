import { create, fromBinary } from "@bufbuild/protobuf";
import { ed25519 } from "@noble/curves/ed25519.js";
import { describe, expect as want, it } from "vitest";
import vectors from "../../internal/e2e/testdata/vectors.json";
import { EnvelopeSchema, HeaderSchema, PayloadKind } from "./gen/remoteagent/v1/envelope_pb";
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
