export type Bytes = Uint8Array<ArrayBuffer>;
export type Integer = number | bigint;
export interface Field { n: number; bytes: boolean; value: Bytes | bigint }
export interface Public { id: Bytes; role: bigint; instance: Bytes; boot: Bytes; signPub: CryptoKey; boxPub: CryptoKey }
export interface Identity extends Public { sign: CryptoKey; box: CryptoKey }
export interface Context {
  domain: Bytes; from: Bytes; to: Bytes; machine: Bytes;
  fromRole: bigint; toRole: bigint; senderInstance: Bytes; receiverInstance: Bytes;
  senderBoot: Bytes; receiverBoot: Bytes; connection: Bytes;
  use: bigint; generation: bigint; seq: bigint; issued: bigint; deadline: bigint;
  cls: bigint; recipientUnknown: bigint;
}
export interface Opened { context: Context; type: number; payload: Bytes; frameID: Bytes; hash: Bytes; raw: Bytes }
export interface Scope { account: string; accountDomain: Bytes; head: Bytes; epoch: bigint }
export interface Codec {
  seal(from: Identity, to: Public, context: Context, type: Integer, payload: Bytes, id?: Bytes, contextNonce?: Bytes, bodyNonce?: Bytes, omit?: boolean): Promise<Bytes>;
  open(raw: Bytes, from: Public, to: Identity, now: Integer): Promise<Opened>;
  openSent(raw: Bytes, from: Identity, to: Public, now: Integer): Promise<Opened>;
  encodeContext(context: Context): Bytes;
}
export function codec(scope: Scope): Codec;
export function utf8(value: string): Bytes;
export function text(value: Bytes): string;
export function concat(...values: Bytes[]): Bytes;
export function equal(a: Bytes, b: Bytes): boolean;
export function hex(value: Bytes): string;
export function unhex(value: string): Bytes;
export function hash(value: Bytes): Promise<Bytes>;
export function num(value: Integer): Bytes;
export function lp(...values: Bytes[]): Bytes;
export function vint(value: Integer): Bytes;
export function b(n: number, value: Bytes): Field;
export function u(n: number, value: Integer): Field;
export function pb(...fields: Field[]): Bytes;
export function parse(raw: Bytes, maxFields: number): Map<number, Field>;
export function fields(raw: Bytes, name: string): Map<number, Field>;
export function need(fields: Map<number, Field>, schema: string): void;
export function getBytes(fields: Map<number, Field>, n: number): Bytes;
export function getUint(fields: Map<number, Field>, n: number): bigint;
export function context(from: Identity, to: Public, connection: Bytes, use: Integer, generation: Integer, seq: Integer, now: Integer, cls?: number, bootstrap?: boolean): Context;
export function probeContext(from: Identity, to: Public, connection: Bytes, seq: Integer, now: Integer): Context;
export function member(peer: Public): Public;
export function cacheKey(context: Context): string;
export function validatePolicyReply(request: Opened, reply: Opened, now: Integer): void;
