import type { Bytes } from "./wire";
export function validateSDP(raw: Bytes): { ufrag: string; mid?: string; fingerprint: string };
