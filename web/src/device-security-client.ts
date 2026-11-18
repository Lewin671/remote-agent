import { abortable, ownedRound, PathChanged } from "./transport/recovery";
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
  immutableState,
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
  type LeaseCertificate,
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
type OpenedSecurity = { header: SecurityHeader; plaintext: B };
export type HistoryResult = PromiseSettledResult<OpenedSecurity>;
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
  recoveryPending?: boolean;
  recoveryReadyMs?: bigint;
}
/** Reads the server's signed state chain after an epoch. */
export type FetchStates = (after: bigint, limit: number, signal?: AbortSignal) => Promise<StatePage>;
/** Foreground security client. The notification worker separately reads local
 * security state to open scheduled notifications; it never signs or syncs. */
/** Storage operations preserve atomic head changes and strict
 * durability in production. Explicit in-memory implementations belong to tests. */
export interface SecurityStorage {
  // Reusing an object means its complete stored snapshot is unchanged.
  // Writers must return a new snapshot, including lease/floor-only updates.
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
  onSync?: (record: SecurityRecord, started: number) => void;
  onSyncFailed?: () => void;
  onInstall?: (record: SecurityRecord) => void;

  recoveryPending = false;
  recoveryReadyMs = 0n;
  recoveryStatusStale = true;
  private syncJob?: Promise<SecurityRecord>;
  private syncController?: AbortController;
  private followUp?: Promise<SecurityRecord>;
  private validated = new WeakMap<SecurityRecord, Promise<SecurityRecord>>();
  private contentKeys = new WeakMap<SecurityRecord, Map<bigint, Promise<B>>>();
  private stateHashes = new WeakMap<SecurityState, Promise<B>>();
  constructor(
    readonly memberID: string,
    private readonly ed: CryptoKey,
    private readonly fetchStates: FetchStates,
    private readonly active: () => boolean = () => true,
    private readonly store: SecurityStorage = browserStorage,
    private readonly roundBudget: () => number = () => 15_000,
  ) {}
  private async validateLocalRecord(
    r: SecurityRecord,
  ): Promise<SecurityRecord> {
    if (!this.active()) throw new SecurityError("storage");
    if (r.memberID !== this.memberID) throw new SecurityError("untrusted");
    r = { ...r, states: r.states.map(immutableState) };
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
    if (!this.active()) throw new SecurityError("storage");
    const record = await this.store.loadSecurity();
    let job = this.validated.get(record);
    if (!job) {
      job = this.validateLocalRecord(record);
      this.validated.set(record, job);
      job.catch(() => this.validated.delete(record));
    }
    const result = await job;
    if (result.states.at(-1)!.core.issuedAt > BigInt(Date.now()) + BigInt(MAX_CLOCK_SKEW))
      throw new SecurityError("clock");
    if (!this.active()) throw new SecurityError("storage");
    return result;
  }
  /** Even sealing checks the stored revision, so another tab cannot leave a stale head cached. */
  private async currentRecord(): Promise<SecurityRecord> {
    return this.load();
  }
  async verifiedRecord(): Promise<SecurityRecord> {
    return this.currentRecord();
  }
  async install(
    expected: SecurityRecord,
    states: SecurityState[],
    proofs: (RecoveryStatement | undefined)[],
    lease?: LeaseCertificate,
  ): Promise<SecurityRecord> {
    await this.store.installSecurity(expected, states, proofs, lease);
    const updated = await this.load();
    this.onInstall?.(updated);
    return updated;
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
  /** Persist the local time floor without waiting on a network path. */
  async touch(): Promise<void> {
    await this.load();
    await this.store.touchSecurity(BigInt(Date.now()));
  }
  networkChanged() {
    this.syncController?.abort(new PathChanged());
    this.syncController = undefined;
    this.syncJob = undefined;
  }
  /**
   * A fresh sync needs a pull that starts after it was asked for: a push or an explicit
   * refresh must not take the answer of a round that fetched the head before the change.
   * It waits out a round in flight and then shares one follow-up round with other fresh callers.
   */
  async sync(waiter?: AbortSignal, budget = this.roundBudget(), fresh = false): Promise<SecurityRecord> {
    if (fresh && this.syncJob) {
      this.followUp ??= this.syncJob.catch(() => undefined).then(() => {
        this.followUp = undefined;
        return this.sync(undefined, budget);
      });
      return abortable(this.followUp, waiter);
    }
    for (;;) {
      waiter?.throwIfAborted();
      let job = this.syncJob, controller = this.syncController;
      if (!job || !controller) {
        controller = new AbortController();
        const owner = controller;
        const started = Date.now();
        const pending = ownedRound(owner, budget, signal => this.syncLoop(signal)).then(record => {
          if (this.syncJob === pending && !owner.signal.aborted) this.onSync?.(record, started);
          return record;
        }).catch(error => {
          if (this.syncJob === pending && !(owner.signal.reason instanceof PathChanged)) { this.recoveryStatusStale = true; this.onSyncFailed?.(); }
          throw error;
        }).finally(() => {
          if (this.syncJob === pending) { this.syncJob = undefined; this.syncController = undefined; }
        });
        job = pending;
        this.syncController = owner; this.syncJob = pending;
        void pending.catch(() => undefined);
      }
      try { return await abortable(job, waiter); }
      catch (error) {
        if (controller.signal.reason instanceof PathChanged && this.active() && !waiter?.aborted) continue;
        throw error;
      }
    }
  }
  private async syncLoop(signal: AbortSignal): Promise<SecurityRecord> {
    let r = await this.load();
    for (let pages = 0; pages < 512; pages++) {
      // A failed pull must not forget an observed lease expiry, including in the sealing cache.
      await this.store.touchSecurity(BigInt(Date.now()));
      r = await this.load();
      const head = r.states[r.states.length - 1];
      signal.throwIfAborted();
      let cancel!: () => void;
      const aborted = new Promise<never>((_, reject) => {
        cancel = () => reject(signal.reason);
        signal.addEventListener("abort", cancel, { once: true });
      });
      let reply: StatePage;
      try { reply = await Promise.race([this.fetchStates(head.core.epoch, MAX_PAGE, signal), aborted]); }
      finally { signal.removeEventListener("abort", cancel); }
      signal.throwIfAborted();
      this.recoveryPending = reply.recoveryPending ?? false;
      this.recoveryReadyMs = reply.recoveryReadyMs ?? 0n;
      this.recoveryStatusStale = false;
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
        return this.load();
      }
      const { states, proofs } = await parseChain(reply.states);
      signal.throwIfAborted();
      const before = head.core.epoch;
      await this.store.installSecurity(r, states, proofs);
      r = await this.load();
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
    beforeSign?: () => void,
  ): Promise<Envelope> {
    for (let attempt = 0; attempt < 3; attempt++) {
      try {
        let r = await this.currentRecord();
        if (!r.member) throw new SecurityError("untrusted");
        const state = r.states[r.states.length - 1];
        if (state.core.issuedAt > BigInt(Date.now()) + BigInt(MAX_CLOCK_SKEW))
          throw new SecurityError("clock");
        // Every new ciphertext needs a live head lease. Reading stored
        // ciphertext uses the separate history path and remains available.
        await validLease(r.lease, state, maxBig(BigInt(Date.now()), r.floor));
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
          beforeSign,
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
  private cachedStateHash(state: SecurityState): Promise<B> {
    let p = this.stateHashes.get(state);
    if (!p) {
      p = stateHash(state);
      this.stateHashes.set(state, p);
      p.catch(() => this.stateHashes.delete(state));
    }
    return p;
  }
  private async contentKey(
    r: SecurityRecord,
    state: SecurityState,
    wire: SecureWire,
    h: SecurityHeader,
    live: boolean,
  ): Promise<B> {
    try {
      let keys = this.contentKeys.get(r);
      if (!keys) this.contentKeys.set(r, (keys = new Map()));
      let key = keys.get(h.contentEpoch);
      if (!key) {
        key = contentSecretOf(r, h.contentEpoch);
        keys.set(h.contentEpoch, key);
        key.catch(() => keys!.delete(h.contentEpoch));
      }
      return await key;
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
    if (!equal(await this.cachedStateHash(state), h.state))
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
    return this.openHistorical(env, seed, await this.load());
  }
  /** Reusable snapshots require a final revision check before releasing results.
   * Concurrent writes, including lease/floor changes, invalidate the batch. */
  async openHistoryBatch(
    items: { envelope: Envelope; seed?: B }[],
  ): Promise<HistoryResult[]> {
    if (!items.length) return [];
    for (let attempt = 0; attempt < 3; attempt++) {
      const record = await this.load();
      const headEpoch = record.states[record.states.length - 1].core.epoch;
      let canParallel = true;
      for (const { envelope } of items) {
        try {
          if (parseHeader(fromEnvelope(envelope).header).epoch > headEpoch) {
            canParallel = false;
            break;
          }
        } catch {
          // Malformed headers reject locally without calling sync().
        }
      }
      let synced = false;
      const onSync = (updated: SecurityRecord) => { synced = updated !== record; };
      let results: HistoryResult[];
      if (canParallel) {
        results = [];
        for (let i = 0; i < items.length; i += 32) {
          const chunk = items.slice(i, i + 32);
          const part = await Promise.all(
            chunk.map(async ({ envelope, seed }) => {
              try {
                return { status: "fulfilled" as const, value: await this.openHistorical(envelope, seed, record, onSync) };
              } catch (reason) {
                return { status: "rejected" as const, reason };
              }
            }),
          );
          results.push(...part);
        }
      } else {
        results = [];
        for (const { envelope, seed } of items) {
          try {
            results.push({ status: "fulfilled" as const,
              value: await this.openHistorical(envelope, seed, record, onSync) });
          } catch (reason) {
            results.push({ status: "rejected" as const, reason });
          }
          // A future epoch replaces this snapshot. Restart once, rather than
          // syncing again for every remaining envelope under that epoch.
          if (synced) break;
        }
      }
      if (!synced && await this.load() === record) return results;
    }
    // Unmarked snapshots cannot be reused. Keep their per-envelope checks.
    const results = [];
    for (const { envelope, seed } of items) {
      try {
        results.push({ status: "fulfilled" as const, value: await this.openHistory(envelope, seed) });
      } catch (reason) {
        results.push({ status: "rejected" as const, reason });
      }
    }
    return results;
  }
  private async openHistorical(
    env: Envelope,
    seed: B | undefined,
    r: SecurityRecord,
    onSync?: (record: SecurityRecord) => void,
  ): Promise<{ header: SecurityHeader; plaintext: B }> {
    if (!this.active()) throw new SecurityError("storage");
    if (r.states.at(-1)!.core.issuedAt > BigInt(Date.now()) + BigInt(MAX_CLOCK_SKEW))
      throw new SecurityError("clock");
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
    let state = r.states[Number(h.epoch - 1n)];
    if (!state) {
      if (h.epoch <= r.states[r.states.length - 1].core.epoch)
        throw new SecurityError("stale");
      r = await this.sync();
      onSync?.(r);
      state = r.states[Number(h.epoch - 1n)];
      if (!state) throw new SecurityError("stale");
    }
    if (!equal(await this.cachedStateHash(state), h.state))
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
    diskStatus?: unknown; machineName?: string; title?: string; text?: string; approval?: unknown;
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
      ![1, 2, 4, 5].includes(m.kind as number))
  )
    throw new SecurityError();
  if(h.purpose===10 && (m.kind===5 ? !!h.session || !m.diskStatus || !m.machineName || !!m.title || !!m.text || !!m.approval : !h.session)) throw new SecurityError();
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
