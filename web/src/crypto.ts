import * as l10n from "./i18n";
import { fromBinary } from "@bufbuild/protobuf";
import { HeaderSchema, type Envelope } from "./gen/remoteagent/v1/envelope_pb";

export const VERSION = 1;
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

/** All entry points use the same transport policy, including pairing. */
export function checkServer(server: string): string {
  const u = new URL(server.trim());
  const local = ["localhost", "127.0.0.1", "[::1]"].includes(u.hostname);
  if (u.protocol !== "https:" && !(u.protocol === "http:" && local)) {
    throw new Error(l10n.t("errors.the_server_must_use_https"));
  }
  if (u.username || u.password) {
    throw new Error(l10n.t("errors.the_server_address_cannot_include_a_username_or_password"));
  }
  return u.origin;
}

export function newRequestID(): string {
  return hex(crypto.getRandomValues(new Uint8Array(16)));
}

/** The old history envelope is recognized only to report its retirement. */
export const RETIRED_HISTORY_ERROR = () => l10n.t("errors.this_history_uses_an_old_encryption_format_and_can");
export class UnsupportedHistoryError extends Error {
  constructor() { super(RETIRED_HISTORY_ERROR()); }
}
export function rejectRetiredHistory(env: Envelope | undefined): void {
  if (env && fromBinary(HeaderSchema, env.header).version === 1) throw new UnsupportedHistoryError();
}
