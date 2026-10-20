// Matches Android RecoveryCode: 10 genesis bytes + 32 seed bytes, Crockford
// Base32 with zero padding. This format has no additional checksum.
import { concat } from "./crypto";
import { SecurityError } from "./device-security";
type B = Uint8Array<ArrayBuffer>;
const ALPHABET = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";
function encode(seed: B, genesis: B): string {
  if (seed.length !== 32 || genesis.length !== 32) throw new SecurityError();
  let bits = 0, value = 0, out = "";
  for (const b of concat(genesis.slice(0, 10), seed)) {
    value = (value << 8) | b;
    bits += 8;
    while (bits >= 5) { bits -= 5; out += ALPHABET[(value >>> bits) & 31]; }
  }
  if (bits > 0) out += ALPHABET[(value << (5 - bits)) & 31];
  return out.match(/.{1,4}/g)!.join("-");
}
function decode(text: string): { seed: B; genesis: B } | undefined {
  let bits = 0, value = 0;
  const out: number[] = [];
  for (let c of text.trim().toUpperCase()) {
    if (c === " " || c === "-" || c === "\n" || c === "\r") continue;
    if (c === "O") c = "0";
    if (c === "I" || c === "L") c = "1";
    const n = ALPHABET.indexOf(c);
    if (n < 0) return;
    value = (value << 5) | n;
    bits += 5;
    if (bits >= 8) { bits -= 8; out.push((value >>> bits) & 255); }
    if (out.length > 42) return;
  }
  if (bits >= 5 || (value & ((1 << bits) - 1)) !== 0 || out.length !== 42) return;
  const payload = new Uint8Array(out);
  return { seed: payload.slice(10), genesis: payload.slice(0, 10) };
}
export const RecoveryCode = { encode, decode };
