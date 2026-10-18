// Vectors made by the Android client (android/core, KotlinVectorsTest),
// checked here: the reverse of vectors.json.
import { create, fromBinary } from "@bufbuild/protobuf";
import { ed25519 } from "@noble/curves/ed25519.js";
import { expect as want, it } from "vitest";
import v from "../../internal/e2e/testdata/kotlin-vectors.json";
import { accountKey, concat, deriveRaw, deviceID, expect, fromHex, hex, importRoot, importSessionKey, LOGIN_CONTEXT, open, REGISTER_CONTEXT, utf8 } from "./crypto";
import { Compression, EnvelopeSchema, PayloadKind } from "./gen/remoteagent/v1/envelope_pb";
import { PairingPayloadSchema } from "./gen/remoteagent/v1/remote_pb";
import { derivePair, newPairKey, pairCommit, pairTranscript } from "./pair";

it("derives the same keys as Kotlin", async () => {
  const root = await importRoot(fromHex(v.root));
  want(hex(await deriveRaw(root, "wrap"))).toBe(v.wrap);
  want(hex(await deriveRaw(root, "machine-meta"))).toBe(v.meta);
  want(hex(await deriveRaw(root, "command"))).toBe(v.command);
  want(hex(await deriveRaw(root, "notify"))).toBe(v.notify);
  const acct = await accountKey(root);
  want(hex(acct.publicKey)).toBe(v.account_public_key);
  want(await deviceID(fromHex(v.account_public_key))).toBe(v.device_id);
});

it("opens a Kotlin envelope with a uint64 seq", async () => {
  const e = v.envelope;
  const env = create(EnvelopeSchema, { header: fromHex(e.header), nonce: fromHex(e.nonce), ciphertext: fromHex(e.ciphertext) });
  const { header, plaintext } = await open(await importSessionKey(fromHex(e.key)), env);
  want(new TextDecoder().decode(plaintext)).toBe(e.plaintext);
  want(header.seq).toBe(BigInt(e.seq));
  want(header.seq).toBe(18446744073709551600n);
  expect(header, { kind: PayloadKind.EVENT, machineId: "machine-k", sessionId: "session-k", seq: BigInt(e.seq), requestId: "req-k" });
});

it("opens a packed Kotlin envelope", async () => {
  const e = v.packed;
  const env = create(EnvelopeSchema, { header: fromHex(e.header), nonce: fromHex(e.nonce), ciphertext: fromHex(e.ciphertext) });
  const { header, plaintext } = await open(await importSessionKey(fromHex(e.key)), env);
  want(header.compression).toBe(Compression.DEFLATE);
  want(new TextDecoder().decode(plaintext)).toBe(e.plaintext);
  want(env.ciphertext.length).toBeLessThan(plaintext.length);
});

it("verifies Kotlin signatures", async () => {
  const acct = await accountKey(await importRoot(fromHex(v.root)));
  const reg = concat(utf8(REGISTER_CONTEXT), fromHex(v.register_signature.message));
  want(hex(acct.sign(reg))).toBe(v.register_signature.signature);
  const l = v.login_signature;
  want(hex(ed25519.getPublicKey(fromHex(l.device_seed)))).toBe(l.device_public_key);
  want(await deviceID(fromHex(l.device_public_key))).toBe(l.device_id);
  want(ed25519.verify(fromHex(l.signature), concat(utf8(LOGIN_CONTEXT), fromHex(l.challenge)), fromHex(l.device_public_key))).toBe(true);
});

it("pairs with Kotlin", async () => {
  const p = v.pairing;
  const init = newPairKey(fromHex(p.initiator_private));
  const join = newPairKey(fromHex(p.joiner_private));
  want(hex(init.pub)).toBe(p.initiator_public);
  want(hex(join.pub)).toBe(p.joiner_public);
  want(hex(await pairCommit(init.pub))).toBe(p.commit);
  const tr = await pairTranscript(p.pairing_id, init.pub, join.pub, p.joiner_kind);
  want(hex(tr)).toBe(p.transcript);
  const s = await derivePair(join, init.pub, tr);
  want(hex(s.sas)).toBe(p.sas);
  want(hex(s.key)).toBe(p.key);
  want(s.emoji).toEqual(p.emoji);
  const env = create(EnvelopeSchema, { header: fromHex(p.payload.header), nonce: fromHex(p.payload.nonce), ciphertext: fromHex(p.payload.ciphertext) });
  const { header, plaintext } = await open(await importSessionKey(s.key as Uint8Array<ArrayBuffer>), env);
  expect(header, { kind: PayloadKind.PAIRING, requestId: p.pairing_id });
  const payload = fromBinary(PairingPayloadSchema, plaintext);
  want(payload.server).toBe("https://ra.example.com");
  want(hex(payload.accountPublicKey)).toBe(v.account_public_key);
});
