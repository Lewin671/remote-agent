// End-to-end encryption, matching internal/e2e in Go: HKDF-SHA256 key
// derivation from the account root key, AES-256-GCM envelopes whose
// serialized header is the additional data, and Ed25519 signatures.
// internal/e2e/testdata/vectors.json keeps the two implementations in step.

import { create, fromBinary, toBinary, type DescMessage, type MessageShape } from "@bufbuild/protobuf";
import { ed25519 } from "@noble/curves/ed25519.js";
import { EnvelopeSchema, HeaderSchema, PayloadKind, type Envelope, type Header } from "./gen/remoteagent/v1/envelope_pb";

export const VERSION = 1;
export const REGISTER_CONTEXT = "remote-agent/v1/register\0";
export const LOGIN_CONTEXT = "remote-agent/v1/login\0";
export const ENVELOPE_SIGNATURE_CONTEXT = "remote-agent/v1/envelope-signature\0";

const enc = new TextEncoder();

export function utf8(s: string): Uint8Array<ArrayBuffer> {
  return enc.encode(s) as Uint8Array<ArrayBuffer>;
}

export function concat(...parts: Uint8Array[]): Uint8Array<ArrayBuffer> {
  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
  let i = 0;
  for (const p of parts) {
    out.set(p, i);
    i += p.length;
  }
  return out;
}

export function hex(b: Uint8Array): string {
  return Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
}

export function fromHex(s: string): Uint8Array<ArrayBuffer> {
  const out = new Uint8Array(s.length / 2);
  for (let i = 0; i < out.length; i++) out[i] = parseInt(s.slice(2 * i, 2 * i + 2), 16);
  return out;
}

function b64urlDecode(s: string): Uint8Array<ArrayBuffer> {
  const b64 = s.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((s.length + 3) % 4);
  const bin = atob(b64);
  return Uint8Array.from(bin, (c) => c.charCodeAt(0));
}

/** What a new device needs to join the account. */
export interface Secret {
  root: Uint8Array<ArrayBuffer>;
  server: string;
}

/** Parses "ra1.<root key>.<server URL>", both parts base64url. */
export function parseSecret(text: string): Secret {
  const bad = new Error("这不是 remote-agent 的账户密钥");
  const t = text.trim();
  if (!t.startsWith("ra1.")) throw bad;
  const parts = t.slice(4).split(".");
  if (parts.length !== 2) throw bad;
  let root: Uint8Array<ArrayBuffer>, server: string;
  try {
    root = b64urlDecode(parts[0]);
    server = new TextDecoder("utf-8", { fatal: true }).decode(b64urlDecode(parts[1]));
  } catch {
    throw bad;
  }
  if (root.length !== 32 || !server) throw bad;
  const u = new URL(server);
  const local = ["localhost", "127.0.0.1", "[::1]"].includes(u.hostname);
  if (u.protocol !== "https:" && !(u.protocol === "http:" && local)) throw new Error("server 必须使用 https");
  return { root, server: u.origin };
}

/** Imports the root key; it cannot be read back out of the browser. */
export function importRoot(root: Uint8Array<ArrayBuffer>): Promise<CryptoKey> {
  return crypto.subtle.importKey("raw", root, "HKDF", false, ["deriveBits"]);
}

async function deriveBits(root: CryptoKey, info: string): Promise<Uint8Array<ArrayBuffer>> {
  const bits = await crypto.subtle.deriveBits(
    { name: "HKDF", hash: "SHA-256", salt: new Uint8Array(), info: utf8("remote-agent/v1/" + info) },
    root,
    256,
  );
  return new Uint8Array(bits);
}

function aesKey(raw: Uint8Array<ArrayBuffer>): Promise<CryptoKey> {
  return crypto.subtle.importKey("raw", raw, "AES-GCM", false, ["encrypt", "decrypt"]);
}

/** Keys derived from the root key, one per purpose. */
export interface Keys {
  wrap: CryptoKey;
  meta: CryptoKey;
  command: CryptoKey;
  // The account's project list preferences, and client device metadata.
  prefs: CryptoKey;
  deviceMeta: CryptoKey;
}

export async function deriveKeys(root: CryptoKey): Promise<Keys> {
  return {
    wrap: await aesKey(await deriveBits(root, "wrap")),
    meta: await aesKey(await deriveBits(root, "machine-meta")),
    command: await aesKey(await deriveBits(root, "command")),
    prefs: await aesKey(await deriveBits(root, "account-prefs")),
    deviceMeta: await aesKey(await deriveBits(root, "device-meta")),
  };
}

/** Raw derived key bytes, for the test vectors only. */
export async function deriveRaw(root: CryptoKey, info: string): Promise<Uint8Array> {
  return deriveBits(root, info);
}

/** The account key pair, which signs device registrations. */
export async function accountKey(root: CryptoKey): Promise<{ publicKey: Uint8Array; sign(msg: Uint8Array): Uint8Array }> {
  const seed = await deriveBits(root, "account-sign");
  return {
    publicKey: ed25519.getPublicKey(seed),
    sign: (msg) => ed25519.sign(msg, seed),
  };
}

export async function deviceID(publicKey: Uint8Array<ArrayBuffer>): Promise<string> {
  const sum = new Uint8Array(await crypto.subtle.digest("SHA-256", publicKey));
  return hex(sum.slice(0, 16));
}

/** A device key pair; the private key cannot leave the browser. */
export async function newDeviceKey(): Promise<CryptoKeyPair> {
  return (await crypto.subtle.generateKey({ name: "Ed25519" }, false, ["sign", "verify"])) as CryptoKeyPair;
}

export async function signLogin(device: CryptoKey, challenge: Uint8Array): Promise<Uint8Array> {
  return new Uint8Array(await crypto.subtle.sign("Ed25519", device, concat(utf8(LOGIN_CONTEXT), challenge)));
}

export async function importSessionKey(raw: Uint8Array<ArrayBuffer>): Promise<CryptoKey> {
  if (raw.length !== 32) throw new Error("bad session key");
  return aesKey(raw);
}

/** What a device signs of an envelope: its bytes as transmitted, each
 * part length-prefixed, after a context (Envelope.signature). */
export function envelopeSigned(env: Envelope): Uint8Array<ArrayBuffer> {
  const len = (b: Uint8Array) => {
    const out = new Uint8Array(4);
    new DataView(out.buffer).setUint32(0, b.length);
    return out;
  };
  return concat(utf8(ENVELOPE_SIGNATURE_CONTEXT), len(env.header), env.header, len(env.nonce), env.nonce, len(env.ciphertext), env.ciphertext);
}

/** Signs a command envelope with this device's key, so that the daemon
 * can tell which device sent it even though the server is not trusted.
 * The header's sender must be this device's ID. */
export async function signEnvelope(device: CryptoKey, env: Envelope): Promise<Envelope> {
  const sig = new Uint8Array(await crypto.subtle.sign("Ed25519", device, envelopeSigned(env)));
  return create(EnvelopeSchema, { ...env, signature: sig });
}

export class OpenError extends Error {}

/** Seals plaintext; the header is serialized once and authenticated as sent. */
export async function seal(key: CryptoKey, header: Omit<Partial<Header>, "$typeName">, plaintext: Uint8Array): Promise<Envelope> {
  const h = toBinary(HeaderSchema, create(HeaderSchema, { version: VERSION, ...header }));
  const nonce = crypto.getRandomValues(new Uint8Array(12));
  const ct = await crypto.subtle.encrypt({ name: "AES-GCM", iv: nonce, additionalData: h }, key, plaintext as Uint8Array<ArrayBuffer>);
  return create(EnvelopeSchema, { header: h, nonce, ciphertext: new Uint8Array(ct) });
}

export async function sealMessage<D extends DescMessage>(
  key: CryptoKey,
  header: Omit<Partial<Header>, "$typeName">,
  schema: D,
  msg: MessageShape<D>,
): Promise<Envelope> {
  return seal(key, header, toBinary(schema, msg));
}

/** Opens an envelope and returns the header exactly as sealed. */
export async function open(key: CryptoKey, env: Envelope | undefined): Promise<{ header: Header; plaintext: Uint8Array }> {
  if (!env || env.nonce.length !== 12) throw new OpenError("envelope does not open");
  let pt: ArrayBuffer;
  try {
    pt = await crypto.subtle.decrypt(
      { name: "AES-GCM", iv: env.nonce as Uint8Array<ArrayBuffer>, additionalData: env.header as Uint8Array<ArrayBuffer> },
      key,
      env.ciphertext as Uint8Array<ArrayBuffer>,
    );
  } catch {
    throw new OpenError("envelope does not open");
  }
  const header = fromBinary(HeaderSchema, env.header);
  if (header.version !== VERSION) throw new OpenError("unsupported envelope version");
  return { header, plaintext: new Uint8Array(pt) };
}

/** Header fields to check; unset fields are not checked, except kind. */
export interface Place {
  kind: PayloadKind;
  machineId?: string;
  sessionId?: string;
  seq?: bigint;
  requestId?: string;
}

export function expect(h: Header, want: Place): void {
  if (
    h.kind !== want.kind ||
    (want.machineId && h.machineId !== want.machineId) ||
    (want.sessionId && h.sessionId !== want.sessionId) ||
    (want.seq && h.seq !== want.seq) ||
    (want.requestId && h.requestId !== want.requestId)
  ) {
    throw new OpenError("envelope is not where it belongs");
  }
}

export async function openMessage<D extends DescMessage>(
  key: CryptoKey,
  env: Envelope | undefined,
  want: Place,
  schema: D,
): Promise<{ header: Header; msg: MessageShape<D> }> {
  const { header, plaintext } = await open(key, env);
  expect(header, want);
  let msg: MessageShape<D>;
  try {
    msg = fromBinary(schema, plaintext);
  } catch {
    throw new OpenError("malformed content");
  }
  return { header, msg };
}

export function newRequestID(): string {
  return hex(crypto.getRandomValues(new Uint8Array(16)));
}

export { PayloadKind };
