import type { Bytes, Integer } from "./wire";
export function normalize(raw: Bytes, name: string, depth?: number, shape?: boolean): Bytes;
export function knownPayload(cls: Integer, type: number, raw: Bytes): Bytes;
export function restart(raw: Bytes): Map<bigint, bigint>;
export const signalSchemas: Record<number, string>;
export const controlSchemas: Record<number, string>;
