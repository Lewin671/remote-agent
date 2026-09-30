// The v2 vectors made by Go (internal/e2e/testdata/v2-vectors.json): the
// new purpose keys, the device signature over command envelopes, and
// IMAGE, ACCOUNT_PREFS and DEVICE_META envelopes.
import { create, fromBinary } from "@bufbuild/protobuf";
import { ed25519 } from "@noble/curves/ed25519.js";
import { expect as want, it } from "vitest";
import v from "../../internal/e2e/testdata/v2-vectors.json";
import { deriveRaw, deviceID, envelopeSigned, expect, fromHex, hex, importRoot, importSessionKey, open, signEnvelope } from "./crypto";
import { EnvelopeSchema, HeaderSchema, PayloadKind } from "./gen/remoteagent/v1/envelope_pb";
import { CommandSchema, DeviceMetaSchema, ImagePayloadSchema, ProjectPrefsSchema } from "./gen/remoteagent/v1/remote_pb";
import { DeviceRegistrationSchema } from "./gen/remoteagent/v1/relay_pb";

const b64url = (b: Uint8Array) => btoa(String.fromCharCode(...b)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");

it("derives the v2 purpose keys as Go does", async () => {
  const root = await importRoot(fromHex(v.root));
  want(hex(await deriveRaw(root, "notify"))).toBe(v.notify);
  want(hex(await deriveRaw(root, "account-prefs"))).toBe(v.account_prefs);
  want(hex(await deriveRaw(root, "device-meta"))).toBe(v.device_meta);
});

it("signs a command envelope with the device key exactly as Go does", async () => {
  const c = v.command_signature;
  const seed = fromHex(c.device_seed);
  want(hex(ed25519.getPublicKey(seed))).toBe(c.device_public_key);
  want(await deviceID(fromHex(c.device_public_key))).toBe(c.device_id);
  // The browser holds its device key as a non-extractable WebCrypto key;
  // import the seed the same way to sign with the code the page uses.
  const key = await crypto.subtle.importKey("jwk", { kty: "OKP", crv: "Ed25519", d: b64url(seed), x: b64url(fromHex(c.device_public_key)) }, "Ed25519", false, ["sign"]);
  const env = create(EnvelopeSchema, { header: fromHex(c.header), nonce: fromHex(c.nonce), ciphertext: fromHex(c.ciphertext) });
  want(hex(envelopeSigned(env))).toBe(c.signed_message);
  const signed = await signEnvelope(key, env);
  want(hex(signed.signature)).toBe(c.signature);
  // The header names the signer as the sender.
  want(fromBinary(HeaderSchema, env.header).sender).toBe(c.device_id);
  want(fromBinary(HeaderSchema, env.header).kind).toBe(PayloadKind.COMMAND);
  // The registration the server hands the daemon is the device's own.
  want(hex(fromBinary(DeviceRegistrationSchema, fromHex(c.registration)).devicePublicKey)).toBe(c.device_public_key);
  want(fromBinary(CommandSchema, fromHex(c.command)).kind.case).toBe("send");
});

it("opens IMAGE, ACCOUNT_PREFS and DEVICE_META envelopes made by Go", async () => {
  for (const [name, schema, kind] of [
    ["image", ImagePayloadSchema, PayloadKind.IMAGE],
    ["account_prefs_object", ProjectPrefsSchema, PayloadKind.ACCOUNT_PREFS],
    ["device_meta_object", DeviceMetaSchema, PayloadKind.DEVICE_META],
  ] as const) {
    const e = v[name];
    const env = create(EnvelopeSchema, { header: fromHex(e.header), nonce: fromHex(e.nonce), ciphertext: fromHex(e.ciphertext) });
    const { header, plaintext } = await open(await importSessionKey(fromHex(e.key)), env);
    want(header.kind).toBe(kind);
    want(hex(plaintext)).toBe(e.plaintext);
    fromBinary(schema as never, plaintext);
    expect(header, { kind });
  }
});
