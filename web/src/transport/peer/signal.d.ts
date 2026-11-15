import type { Bytes, Codec, Identity, Public, Opened } from "./wire";
export interface SDPInfo { raw: Bytes; ufrag: Bytes; mid: Bytes; fingerprint: Bytes }
export interface Candidate { original: Bytes; ufrag: Bytes; mid: Bytes; index: bigint; generation: bigint }
export interface Action { cls: number; type: number; payload: Bytes; generation: bigint; original: Bytes }
export function inspectSDP(raw: Bytes): SDPInfo;
export function sdpBody(raw: Bytes): Promise<Bytes>;
export function readSDPBody(raw: Bytes): Promise<SDPInfo>;
export function candidateBody(list: Candidate[], index: number): Bytes;
export function readCandidates(raw: Bytes): { list: Candidate[]; index: number };
export function endBody(ufrag: Bytes, count: number, batches: number): Bytes;
export class Signal {
  constructor(codec: Codec, self: Identity, peer: Public, connection: Bytes, use: number, now: number);
  self: Identity; peer: Public; connection: Bytes; clock: number; roundDeadline: number; version: number;
  probe: Opened | null; receivedProbe: Opened | null; challenge: Opened | null;
  use: bigint; generation: bigint; next: bigint;
  bootReady: boolean; localSDP: boolean; remoteSDP: boolean; dtls: boolean; confirmed: boolean; closed: boolean; retired: boolean; ended: boolean;
  localHash: Bytes; remoteHash: Bytes; localFingerprint: Bytes; remoteFingerprint: Bytes; remoteUfrag: Bytes; remoteMid: Bytes;
  candidates: number; batches: number; seen: Map<string, Bytes>; pending: Map<bigint, Opened>; pendingAt: Map<bigint, number>; cacheUntil: Map<string, number>;
  gapDeadline: number; sdkActions: number; duplicates: number; conflicts: number; rejected: number; unsupported: number; staleChallenges: number; dispatches: number;
  rememberProbe(sent: Opened): Promise<void>;
  rememberChallenge(sent: Opened): Promise<void>;
  offerer(): boolean;
  ready(): boolean;
  setLocal(raw: Bytes, offer: boolean, generation: bigint): Promise<void>;
  nextGeneration(generation: bigint): void;
  setAuthorization(until: number, members: boolean, revision: number): void;
  replyAllowed(raw: Bytes, now: number): Promise<boolean>;
  receive(raw: Bytes, now: number): Promise<Action[]>;
  dispatch(raw: Bytes, now: number): Promise<boolean>;
  tick(now: number): boolean;
  confirmBody(): Bytes;
  retire(retry: boolean): void;
  forgetPeer(): void;
}
