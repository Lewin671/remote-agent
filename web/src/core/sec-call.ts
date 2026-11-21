import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import {
  SecCallRequestSchema,
  SecCallResponseSchema,
  SecEd25519SeedSignCallSchema,
  SecInflateBoundedCallSchema,
  SecPinSchema,
  type SecCallRequest,
  type SecCallResponse,
  type SecPin,
} from "../gen/remoteagent/core/v1/seccall_pb";
import { RemoteAgentCore } from "./projection";

export class SecurityError extends Error {
  constructor(
    readonly code:
      | "invalid"
      | "untrusted"
      | "stale"
      | "unknown"
      | "conflict"
      | "pending"
      | "capacity"
      | "storage"
      | "unavailable"
      | "lease"
      | "clock" = "invalid",
  ) {
    super(`security: ${code}`);
  }
}

const VALID_CODES = new Set<SecurityError["code"]>([
  "invalid",
  "untrusted",
  "stale",
  "unknown",
  "conflict",
  "pending",
  "capacity",
  "storage",
  "unavailable",
  "lease",
  "clock",
]);

export function toSecurityCode(code: string): SecurityError["code"] {
  if (code === "recovery_unavailable") return "unavailable";
  if (VALID_CODES.has(code as SecurityError["code"])) return code as SecurityError["code"];
  return "invalid";
}

export type SecCall = SecCallRequest["call"];

function failSecurity(code: SecurityError["code"] = "invalid"): never {
  throw new SecurityError(code);
}

let activeCore: RemoteAgentCore | undefined;

export function setActiveCore(core: RemoteAgentCore): void {
  activeCore = core;
}

function resolveCore(explicit?: RemoteAgentCore): RemoteAgentCore {
  if (explicit) return explicit;
  if (activeCore) return activeCore;
  if (globalThis.__remoteAgentCore) return RemoteAgentCore.fromGlobal();
  return failSecurity("unavailable");
}

export function secCallWith(core: RemoteAgentCore | undefined, call: SecCall): SecCallResponse {
  const reqBytes = toBinary(SecCallRequestSchema, create(SecCallRequestSchema, { call }));
  const respBytes = resolveCore(core).secCallBytes(reqBytes);
  const resp = fromBinary(SecCallResponseSchema, respBytes);
  if (resp.error) {
    throw new SecurityError(toSecurityCode(resp.errorCode));
  }
  return resp;
}

export function secCall(call: SecCall, core?: RemoteAgentCore): SecCallResponse {
  return secCallWith(core, call);
}

export function makeSecPin(account: string, domain: Uint8Array, genesis: Uint8Array): SecPin {
  return create(SecPinSchema, {
    account,
    domain: new Uint8Array(domain),
    genesis: new Uint8Array(genesis),
  });
}

export function ed25519SeedPublic(seed: Uint8Array): Uint8Array<ArrayBuffer> {
  const resp = secCall({
    case: "ed25519SeedPublic",
    value: new Uint8Array(seed),
  });
  return new Uint8Array(resp.data);
}

export function ed25519SeedSign(seed: Uint8Array, message: Uint8Array): Uint8Array<ArrayBuffer> {
  const resp = secCall({
    case: "ed25519SeedSign",
    value: create(SecEd25519SeedSignCallSchema, { seed: new Uint8Array(seed), message: new Uint8Array(message) }),
  });
  return new Uint8Array(resp.data);
}

export function inflateBounded(compressed: Uint8Array, limit: number): Uint8Array<ArrayBuffer> {
  if (!Number.isInteger(limit) || limit < 0) failSecurity();
  const resp = secCall({
    case: "inflateBounded",
    value: create(SecInflateBoundedCallSchema, { compressed: new Uint8Array(compressed), limit: limit === 0 ? 1 : limit }),
  });
  const out = new Uint8Array(resp.data);
  if (out.length > limit) failSecurity();
  return out;
}

const X25519_PKCS8_PREFIX = new Uint8Array([
  0x30, 0x2e, 0x02, 0x01, 0x00, 0x30, 0x05, 0x06,
  0x03, 0x2b, 0x65, 0x6e, 0x04, 0x22, 0x04, 0x20,
]);

function base64UrlToBytes(s: string): Uint8Array<ArrayBuffer> {
  const norm = s.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((s.length + 3) % 4);
  return Uint8Array.from(atob(norm), (c) => c.charCodeAt(0));
}

export async function x25519PrivateKeyFromSeed(seed: Uint8Array, extractable = false): Promise<CryptoKey> {
  const pkcs8 = new Uint8Array(X25519_PKCS8_PREFIX.length + seed.length);
  pkcs8.set(X25519_PKCS8_PREFIX);
  pkcs8.set(seed, X25519_PKCS8_PREFIX.length);
  return crypto.subtle.importKey("pkcs8", pkcs8, "X25519", extractable, ["deriveBits"]);
}

export async function x25519SeedPublicKey(seed: Uint8Array): Promise<Uint8Array<ArrayBuffer>> {
  const priv = await x25519PrivateKeyFromSeed(seed, true);
  const jwk = await crypto.subtle.exportKey("jwk", priv);
  if (!jwk.x) throw new SecurityError();
  return base64UrlToBytes(jwk.x);
}

export async function x25519DeriveWithPrivateKey(priv: CryptoKey, peerPublic: Uint8Array): Promise<Uint8Array<ArrayBuffer>> {
  const pub = await crypto.subtle.importKey("raw", new Uint8Array(peerPublic), "X25519", false, []);
  const bits = await crypto.subtle.deriveBits({ name: "X25519", public: pub }, priv, 256);
  return new Uint8Array(bits);
}
