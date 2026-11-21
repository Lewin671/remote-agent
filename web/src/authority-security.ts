import { abortable, ownedRound, PathChanged } from "./transport/recovery";
import {
  installSecurity,
  loadSecurity,
  parseChain,
  touchSecurity,
  type SecurityRecord,
} from "./device-security-store";
import {
  MAX_CLOCK_SKEW,
  equal,
  immutableState,
  leaseBytes,
  openGrant,
  parseLease,
  recoveryBytes,
  SecurityError,
  stateBytes,
  stateHash,
  verifyChain,
  type LeaseCertificate,
  type Pin,
  type RecoveryStatement,
  type SecurityState,
} from "./device-security";
import { buildSecurityRecordEvent } from "./core/security-executor";
import type { SecurityRecordEvent } from "./gen/remoteagent/core/v1/security_pb";

export interface StatePage {
  states: { state: Uint8Array; recovery: Uint8Array }[];
  headEpoch: bigint;
  lease?: Uint8Array;
  recoveryPending?: boolean;
  recoveryReadyMs?: bigint;
}

/** Reads the server's signed state chain after an epoch. */
export type FetchStates = (after: bigint, limit: number, signal?: AbortSignal) => Promise<StatePage>;

/** Storage operations preserve atomic head changes and strict durability in production. */
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

const MAX_PAGE = 8;

/** Encodes a verified SecurityRecord into a SecurityRecordEvent for clientcore/security. */
export async function recordToSecurityEvent(
  record: SecurityRecord,
  opts: {
    opId?: bigint;
    pushSeq?: bigint;
    networkGeneration: number | bigint;
    recoveryPending?: boolean;
    recoveryReadyMs?: bigint;
    recoveryObjectsPending?: boolean;
    error?: string;
  },
): Promise<SecurityRecordEvent> {
  const [states, recoveryProofs] = await Promise.all([
    Promise.all(record.states.map((s) => stateBytes(s))),
    Promise.all(record.proofs.map((p) => (p ? recoveryBytes(p) : Promise.resolve(new Uint8Array())))),
  ]);
  return buildSecurityRecordEvent({
    opId: opts.opId,
    pushSeq: opts.pushSeq,
    networkGeneration: opts.networkGeneration,
    account: record.pin.account,
    domain: record.pin.domain,
    genesis: record.pin.genesis,
    states,
    recoveryProofs,
    lease: record.lease ? leaseBytes(record.lease) : undefined,
    floorMs: record.floor,
    recoveryPending: opts.recoveryPending,
    recoveryReadyMs: opts.recoveryReadyMs,
    recoveryObjectsPending: opts.recoveryObjectsPending,
    removed: !record.member,
    error: opts.error,
    contentKeys: record.contentKeys,
  });
}

/** Foreground security record manager (syncs & installs validated chains; delegates gates/envelopes to Core). */
export class AuthoritySecurity {
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

  constructor(
    readonly memberID: string,
    _ed?: CryptoKey,
    private readonly fetchStates: FetchStates = async () => ({ states: [], headEpoch: 0n }),
    private readonly active: () => boolean = () => true,
    private readonly store: SecurityStorage = browserStorage,
    private readonly roundBudget: () => number = () => 15_000,
  ) {}

  private async validateLocalRecord(r: SecurityRecord): Promise<SecurityRecord> {
    if (!this.active()) throw new SecurityError("storage");
    if (r.memberID !== this.memberID) throw new SecurityError("untrusted");
    r = { ...r, states: r.states.map(immutableState) };
    const head = r.states[r.states.length - 1];
    if (
      head.core.account !== r.pin.account ||
      !equal(head.core.domain, r.pin.domain) ||
      (head.core.epoch === 1n && !equal(await stateHash(head), r.pin.genesis))
    ) {
      throw new SecurityError("untrusted");
    }
    await verifyChain(r.pin, r.states, r.proofs);
    if (!equal(await stateHash(head), r.stateHash)) throw new SecurityError("storage");
    const inHead = head.core.members.some((m) => m.id === this.memberID);
    if (inHead !== r.member) throw new SecurityError("storage");
    if (r.member) {
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
    if (result.states.at(-1)!.core.issuedAt > BigInt(Date.now()) + BigInt(MAX_CLOCK_SKEW)) {
      throw new SecurityError("clock");
    }
    if (!this.active()) throw new SecurityError("storage");
    return result;
  }

  verifiedRecord(): Promise<SecurityRecord> {
    return this.load();
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

  async localIdentity(): Promise<{ pin: Pin; state: SecurityState; floor: bigint }> {
    const r = await this.load();
    return {
      pin: r.pin,
      state: r.states[r.states.length - 1],
      floor: r.floor,
    };
  }

  /** Persist the local time floor without waiting on a network path. */
  async touch(): Promise<void> {
    await this.load();
    await this.store.touchSecurity(BigInt(Date.now()));
  }

  networkChanged(): void {
    this.syncController?.abort(new PathChanged());
    this.syncController = undefined;
    this.syncJob = undefined;
  }

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
        const pending = ownedRound(owner, budget, (signal) => this.syncLoop(signal))
          .then((record) => {
            if (this.syncJob === pending && !owner.signal.aborted) this.onSync?.(record, started);
            return record;
          })
          .catch((error) => {
            if (this.syncJob === pending && !(owner.signal.reason instanceof PathChanged)) {
              this.recoveryStatusStale = true;
              this.onSyncFailed?.();
            }
            throw error;
          })
          .finally(() => {
            if (this.syncJob === pending) {
              this.syncJob = undefined;
              this.syncController = undefined;
            }
          });
        job = pending;
        this.syncController = owner;
        this.syncJob = pending;
        void pending.catch(() => undefined);
      }
      try {
        return await abortable(job, waiter);
      } catch (error) {
        if (controller.signal.reason instanceof PathChanged && this.active() && !waiter?.aborted) continue;
        throw error;
      }
    }
  }

  private async syncLoop(signal: AbortSignal): Promise<SecurityRecord> {
    let r = await this.load();
    for (let pages = 0; pages < 512; pages++) {
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
      try {
        reply = await Promise.race([this.fetchStates(head.core.epoch, MAX_PAGE, signal), aborted]);
      } finally {
        signal.removeEventListener("abort", cancel);
      }
      signal.throwIfAborted();
      this.recoveryPending = reply.recoveryPending ?? false;
      this.recoveryReadyMs = reply.recoveryReadyMs ?? 0n;
      this.recoveryStatusStale = false;
      if (reply.headEpoch < head.core.epoch) throw new SecurityError("stale");
      if (!reply.states.length) {
        if (reply.headEpoch > head.core.epoch) throw new SecurityError("stale");
        if (reply.lease?.length) {
          await this.store.installSecurity(r, [], [], parseLease(new Uint8Array(reply.lease)));
        }
        await this.store.touchSecurity(BigInt(Date.now()));
        return this.load();
      }
      const { states, proofs } = await parseChain(reply.states);
      signal.throwIfAborted();
      const before = head.core.epoch;
      await this.store.installSecurity(r, states, proofs);
      r = await this.load();
      if (r.states[r.states.length - 1].core.epoch <= before) throw new SecurityError("stale");
    }
    throw new SecurityError("capacity");
  }
}
