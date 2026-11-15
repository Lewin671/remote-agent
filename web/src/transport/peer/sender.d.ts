import type { Bytes } from "./wire";
export interface Pending { raw: Bytes; hash: Bytes; first: number; notBefore: number; attempts: number; expiry: number; binding: number }
export class Sender {
  constructor(deadline: number);
  pending: Map<string, Pending>; timedOut: boolean; paused: boolean; frames: number; bytes: number;
  retryAt: number; networkGeneration: number; failures: number; binding: number; clock: number; deadline: number;
  enqueue(raw: Bytes, now: number, expiry: number, binding: number): Promise<boolean>;
  tick(now: number): Promise<Bytes[]>;
  admit(raw: Bytes, now: number): Promise<boolean>;
  setAuthorization(until: number, members: boolean, revision: number): void;
  ack(hash: Bytes): boolean;
  limited(hash: Bytes, now: number, delay: number): void;
  backoff(now: number, fraction: number): number;
  rebind(binding: number): void;
  networkChanged(generation: number): void;
  offline(): void;
  discovered(): void;
}
export class CandidateBatcher {
  constructor(generation: bigint, deadline: number, binding: number);
  generation: bigint; deadline: number; binding: number; due: number; pending: Bytes[]; count: number; batches: number; ended: boolean;
  add(raw: Bytes, generation: bigint, now: number, binding: number): boolean;
  flush(now: number, end: boolean, generation: bigint, binding: number): Bytes[][];
}
