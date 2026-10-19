import {
  contentSecretOf,
  installSecurity,
  loadSecurity,
  parseChain,
  touchSecurity,
  type SecurityRecord,
} from "./device-security-store";
import {
  MAX_CLOCK_SKEW,
  parseLease,
  validLease,
  verifyChain,
  control,
  equal,
  fromEnvelope,
  openGrant,
  openWire,
  parseHeader,
  SecurityError,
  stateAt,
  stateHash,
  toEnvelope,
  formCipher,
  verifyPin,
  verifyWire,
  type Purpose,
  type RecoveryStatement,
  type SecureWire,
  type SecurityHeader,
  type SecurityState,
} from "./device-security";
import {
  fromBinary,
  type DescMessage,
  type MessageShape,
} from "@bufbuild/protobuf";
import type { Envelope } from "./gen/remoteagent/v1/envelope_pb";
import { Inflate } from "fflate";
import { concat } from "./crypto";

type B = Uint8Array<ArrayBuffer>;
export type NewSecurityHeader = Pick<
  SecurityHeader,
  | "kind"
  | "purpose"
  | "machine"
  | "session"
  | "request"
  | "seq"
  | "deadline"
  | "compression"
>;
export interface StatePage {
  states: { state: Uint8Array; recovery: Uint8Array }[];
  headEpoch: bigint;
  lease?: Uint8Array;
}
/** Reads the server's signed state chain after an epoch. */
export type FetchStates = (after: bigint, limit: number) => Promise<StatePage>;
/** Foreground client only. A service worker is never given a full content key,
 * member signing key or recipient private key to implement background sync. */
/** Storage operations preserve atomic head changes and strict
 * durability in production. Explicit in-memory implementations belong to tests. */
export interface SecurityStorage {
  loadSecurity: typeof loadSecurity;
  installSecurity: typeof installSecurity;
  touchSecurity: typeof touchSecurity;
}
const browserStorage: SecurityStorage = {
  loadSecurity,
  installSecurity,
  touchSecurity,
};
/** An authenticated event whose key is absent across a verified recovery boundary.
 * It carries no plaintext and cannot establish any current action state. */
export class RecoveryHistoryUnavailable extends SecurityError {
  constructor(readonly header: SecurityHeader) {
    super("unavailable");
  }
}
const MAX_PAGE = 8;
export class DeviceSecurityClient {
  private syncJob?: Promise<SecurityRecord>;
  /** The verified record, kept for sealing; sync refreshes it from storage. */
  private record?: SecurityRecord;
  constructor(
    readonly memberID: string,
    private readonly ed: CryptoKey,
    private readonly fetchStates: FetchStates,
    private readonly active: () => boolean = () => true,
    private readonly store: SecurityStorage = browserStorage,
  ) {}
  private async validateLocalRecord(
    r: SecurityRecord,
  ): Promise<SecurityRecord> {
    if (!this.active()) throw new SecurityError("storage");
    if (r.memberID !== this.memberID) throw new SecurityError("untrusted");
    const head = r.states[r.states.length - 1];
    await verifyPin(r.pin, head);
    await verifyChain(r.pin, r.states, r.proofs);
    if (!equal(await stateHash(head), r.stateHash))
      throw new SecurityError("storage");
    const inHead = head.core.members.some((m) => m.id === this.memberID);
    if (inHead !== r.member) throw new SecurityError("storage");
    if (r.member) {
      // A stale/mixed secret must never encrypt new content under a newer
      // state. Compare it with the grant the head carries.
      const expected = await openGrant(r.pin, head, this.memberID, r.xPrivate);
      if (!equal(expected, r.secret)) throw new SecurityError("storage");
    }
    if (!this.active()) throw new SecurityError("storage");
    return r;
  }
  private async load(): Promise<SecurityRecord> {
    return this.validateLocalRecord(await this.store.loadSecurity());
  }
  /** The verified record kept for sealing, or the stored one on first use. */
  private async currentRecord(): Promise<SecurityRecord> {
    return this.record ?? (await this.load());
  }
  async localIdentity(): Promise<{
    pin: SecurityRecord["pin"];
    state: SecurityState;
    floor: bigint;
  }> {
    const r = await this.load();
    return {
      pin: r.pin,
      state: r.states[r.states.length - 1],
      floor: r.floor,
    };
  }
  async historicalMachines(): Promise<Set<string>> {
    const r = await this.load();
    return new Set(
      r.states.flatMap((s) =>
        s.core.members.filter((m) => m.kind === 1).map((m) => m.id),
      ),
    );
  }
  async localStateHash(): Promise<B> {
    return (await this.load()).stateHash;
  }
  async sync(): Promise<SecurityRecord> {
    if (this.syncJob) return this.syncJob;
    const job = this.syncLoop();
    this.syncJob = job;
    try {
      return await job;
    } finally {
      if (this.syncJob === job) this.syncJob = undefined;
    }
  }
  private async syncLoop(): Promise<SecurityRecord> {
    let r = await this.load();
    for (let pages = 0; pages < 512; pages++) {
      const head = r.states[r.states.length - 1];
      const reply = await this.fetchStates(head.core.epoch, MAX_PAGE);
      if (reply.headEpoch < head.core.epoch) throw new SecurityError("stale");
      if (!reply.states.length) {
        if (reply.headEpoch > head.core.epoch) throw new SecurityError("stale");
        if (reply.lease?.length)
          await this.store.installSecurity(
            r,
            [],
            [],
            parseLease(new Uint8Array(reply.lease)),
          );
        await this.store.touchSecurity(BigInt(Date.now()));
        this.record = await this.load();
        return this.record;
      }
      const { states, proofs } = await parseChain(reply.states);
      const before = head.core.epoch;
      await this.store.installSecurity(r, states, proofs);
      r = await this.load();
      this.record = r;
      if (r.states[r.states.length - 1].core.epoch <= before)
        throw new SecurityError("stale");
    }
    throw new SecurityError("capacity");
  }
  /** No formed C leaves the device before its origin signature is computed. */
  async seal(
    h: NewSecurityHeader,
    plaintext: B,
    sessionSeed?: B,
  ): Promise<Envelope> {
    for (let attempt = 0; attempt < 3; attempt++) {
      try {
        let r = await this.currentRecord();
        if (!r.member) throw new SecurityError("untrusted");
        const state = r.states[r.states.length - 1];
        if (control(h.purpose)) {
          if (state.core.issuedAt > BigInt(Date.now()) + BigInt(MAX_CLOCK_SKEW))
            throw new SecurityError("clock");
          // A clock moved back must not revive an expired lease.
          await validLease(r.lease, state, maxBig(BigInt(Date.now()), r.floor));
        }
        const header: SecurityHeader = {
          ...h,
          account: r.pin.account,
          domain: r.pin.domain,
          epoch: state.core.epoch,
          state: r.stateHash,
          contentEpoch: state.core.contentEpoch,
          sender: this.memberID,
        };
        const c = await formCipher(
          r.secret,
          sessionSeed,
          header,
          plaintext,
          this.ed,
        );
        return toEnvelope(c);
      } catch (e) {
        // Another tab installed a newer head between load and reservation.
        if (e instanceof SecurityError && e.code === "stale" && attempt < 2) {
          await this.sync().catch(() => undefined);
          continue;
        }
        throw e;
      }
    }
    throw new SecurityError("stale");
  }
  private async contentKey(
    r: SecurityRecord,
    state: SecurityState,
    wire: SecureWire,
    h: SecurityHeader,
    live: boolean,
  ): Promise<B> {
    try {
      return await contentSecretOf(r, h.contentEpoch);
    } catch (e) {
      if (!(e instanceof SecurityError) || e.code !== "unavailable") throw e;
      // Missing keys must never hide an invalid signature, state or route.
      await verifyWire(
        r.pin,
        state,
        r.states[r.states.length - 1],
        wire,
        live,
        maxBig(BigInt(Date.now()), r.floor),
      );
      const boundary = r.states.some(
        (s, i) =>
          !!r.proofs[i] &&
          s.core.epoch > h.epoch &&
          s.core.contentEpoch > h.contentEpoch &&
          s.prevSecret.length === 0,
      );
      if (h.purpose === 1 && boundary) throw new RecoveryHistoryUnavailable(h);
      throw e;
    }
  }
  async openCurrent(
    env: Envelope,
    seed?: B,
  ): Promise<{ header: SecurityHeader; plaintext: B }> {
    const wire = fromEnvelope(env);
    let r = await this.load();
    let h = parseHeader(wire.header);
    if (h.epoch > r.states[r.states.length - 1].core.epoch) {
      r = await this.sync();
      h = parseHeader(wire.header);
    }
    const head = r.states[r.states.length - 1];
    // A live frame is authorized by the state it names, provided its sender
    // is still in the current head; it need not name the head itself.
    const state = stateAt(r.states, h.epoch);
    if (!equal(await stateHash(state), h.state))
      throw new SecurityError("stale");
    if (control(h.purpose))
      await validLease(r.lease, head, maxBig(BigInt(Date.now()), r.floor));
    const secret = await this.contentKey(r, state, wire, h, true);
    const opened = await openWire(
      r.pin,
      state,
      head,
      wire,
      secret,
      true,
      maxBig(BigInt(Date.now()), r.floor),
      seed,
    );
    return {
      header: opened.header,
      plaintext: unpackSecurity(opened.header, opened.plaintext),
    };
  }
  /** Historical data is provenance-checked, but does not establish live action
   * state. UI callers render it read-only; live prompts need owner snapshots. */
  async openHistory(
    env: Envelope,
    seed?: B,
  ): Promise<{ header: SecurityHeader; plaintext: B }> {
    const w = fromEnvelope(env),
      h = parseHeader(w.header);
    if (
      [
        5, // Command
        8, // TerminalInput
        14, // TerminalResize
        15, // TerminalHistory
      ].includes(h.purpose)
    )
      throw new SecurityError();
    let r = await this.load();
    let state = r.states[Number(h.epoch - 1n)];
    if (!state) {
      if (h.epoch <= r.states[r.states.length - 1].core.epoch)
        throw new SecurityError("stale");
      r = await this.sync();
      state = r.states[Number(h.epoch - 1n)];
      if (!state) throw new SecurityError("stale");
    }
    if (!equal(await stateHash(state), h.state))
      throw new SecurityError("stale");
    const head = r.states[r.states.length - 1];
    const secret = await this.contentKey(r, state, w, h, false);
    const opened = await openWire(
      r.pin,
      state,
      head,
      w,
      secret,
      false,
      maxBig(BigInt(Date.now()), r.floor),
      seed,
    );
    return {
      header: opened.header,
      plaintext: unpackSecurity(opened.header, opened.plaintext),
    };
  }
  async openMessage<D extends DescMessage>(
    env: Envelope,
    schema: D,
    history: boolean,
    seed?: B,
  ): Promise<{ header: SecurityHeader; msg: MessageShape<D> }> {
    const out = history
      ? await this.openHistory(env, seed)
      : await this.openCurrent(env, seed);
    try {
      const msg = fromBinary(schema, out.plaintext);
      validateSecurityBody(out.header, schema.typeName, msg);
      return { header: out.header, msg };
    } catch {
      throw new SecurityError();
    }
  }
}
export function unpackSecurity(h: SecurityHeader, p: B): B {
  const limit = [
    2, // SessionMeta
    4, // MachineMeta
    13, // DeviceMeta
    12, // Prefs
  ].includes(h.purpose)
    ? 64 << 10
    : 4 << 20;
  if (h.compression === 0) {
    if (p.length > limit) throw new SecurityError("capacity");
    return p;
  }
  if (h.compression !== 1) throw new SecurityError();
  const chunks: B[] = [];
  let size = 0;
  const inf = new Inflate((b) => {
    size += b.length;
    if (size > limit) throw new SecurityError("capacity");
    chunks.push(new Uint8Array(b));
  });
  try {
    for (let at = 0; at < p.length || at === 0; at += 16384)
      inf.push(p.subarray(at, at + 16384), at + 16384 >= p.length);
    return concat(...chunks);
  } catch {
    throw new SecurityError();
  }
}
function maxBig(a: bigint, b: bigint): bigint {
  return a > b ? a : b;
}

export function validateSecurityBody(
  h: SecurityHeader,
  type: string,
  msg: unknown,
): void {
  const expected: Partial<Record<Purpose, string>> = {
    1: "Event",
    2: "Session",
    4: "MachineMeta",
    5: "Command",
    6: "CommandResult",
    7: "TerminalFrame",
    8: "TerminalFrame",
    10: "NotifyContent",
    11: "ImagePayload",
    12: "ProjectPrefs",
    13: "DeviceMeta",
    14: "TerminalFrame",
    15: "TerminalFrame",
  };
  if (type !== "remoteagent.v1." + expected[h.purpose])
    throw new SecurityError();
  const m = msg as {
    sessionId?: string;
    machineId?: string;
    seq?: bigint;
    id?: string;
    schema?: number;
    kind?: { case?: string } | number;
  };
  if (
    h.purpose === 1 &&
    (m.sessionId !== h.session || m.seq !== h.seq || !m.kind)
  )
    throw new SecurityError();
  if (h.purpose === 2 && m.id !== h.session) throw new SecurityError();
  if (
    h.purpose === 10 &&
    (m.sessionId !== h.session ||
      m.machineId !== h.machine ||
      ![1, 2].includes(m.kind as number))
  )
    throw new SecurityError();
  if (h.purpose === 12 && m.schema !== 1) throw new SecurityError();
  if (
    h.purpose === 5 &&
    (!m.kind || typeof m.kind !== "object" || !m.kind.case)
  )
    throw new SecurityError();
  if ([7, 8, 14, 15].includes(h.purpose)) {
    const c = typeof m.kind === "object" ? m.kind.case : undefined;
    if (
      h.purpose === 7
        ? !["snapshot", "output", "historyPage", "closed"].includes(c ?? "")
        : c !==
          (
            { 8: "input", 14: "resize", 15: "historyRequest" } as Record<
              number,
              string
            >
          )[h.purpose]
    )
      throw new SecurityError();
  }
}
export type { RecoveryStatement, SecureWire };
