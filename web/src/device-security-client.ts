import {
  saveRemoval,
  loadRemovals,
  cacheSecurityEpoch,
  readSecurityEpoch,
  type StoredRemoval,
} from "./device-security-store";
import {
  signDeleteRequest,
  verifyCompletion,
  type Completion,
  type RevocationGroup,
} from "./device-security-removal";
import { newRequestID } from "./crypto";
import {
  advance,
  attachAuthorization,
  authRequestJSON,
  commitment,
  equal,
  formCipher,
  fromEnvelope,
  jsonHash,
  Method,
  openGrant,
  openWire,
  parseHeader,
  parseState,
  Purpose,
  replyRPC,
  requestRPC,
  SecurityError,
  stateFromJSON,
  stateHash,
  toEnvelope,
  verifyState,
  stateBytes,
  type Capsule,
  type SecureWire,
  type SecurityHeader,
} from "./device-security";
import {
  installSecurity,
  loadSecurity,
  reserveCounter,
  type SecurityRecord,
} from "./device-security-store";
import {
  fromBinary,
  type DescMessage,
  type MessageShape,
} from "@bufbuild/protobuf";
import type { Envelope } from "./gen/remoteagent/v1/envelope_pb";
import { Inflate } from "fflate";
import { concat } from "./crypto";

type B = Uint8Array<ArrayBuffer>;
type StateJSON = Parameters<typeof stateFromJSON>[0];
export type ExchangeSecurity = (capsule: Capsule) => Promise<Capsule>;
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
/** Foreground client only. A service worker is never given a full content key,
 * member signing key or recipient private key to implement background sync. */
/** Storage operations preserve atomic counter/head changes and strict
 * durability in production. Explicit in-memory implementations belong to tests. */
export interface SecurityStorage {
  loadSecurity: typeof loadSecurity;
  installSecurity: typeof installSecurity;
  reserveCounter: typeof reserveCounter;
  readSecurityEpoch: typeof readSecurityEpoch;
  cacheSecurityEpoch: typeof cacheSecurityEpoch;
  saveRemoval: typeof saveRemoval;
  loadRemovals: typeof loadRemovals;
}
const browserStorage: SecurityStorage = {
  loadSecurity,
  installSecurity,
  reserveCounter,
  readSecurityEpoch,
  cacheSecurityEpoch,
  saveRemoval,
  loadRemovals,
};
export class DeviceSecurityClient {
  private syncJob?: Promise<SecurityRecord>;
  constructor(
    readonly memberID: string,
    private readonly ed: CryptoKey,
    private readonly exchange: ExchangeSecurity,
    private readonly active: () => boolean = () => true,
    private readonly store: SecurityStorage = browserStorage,
    private readonly expectedAccount?: () => Promise<string>,
  ) {}
  private async validateLocalRecord(
    r: SecurityRecord,
  ): Promise<SecurityRecord> {
    if (!this.active()) throw new SecurityError("storage");
    if (
      r.memberID !== this.memberID ||
      (this.expectedAccount && r.pin.account !== (await this.expectedAccount()))
    )
      throw new SecurityError("untrusted");
    const state = await parseState(r.state);
    await verifyState(r.pin, state);
    if (!equal(await stateHash(state), r.stateHash))
      throw new SecurityError("storage");
    // Compare locally derived checks without exporting the cached HKDF key.
    // A stale/mixed secret must never encrypt new content under a newer label.
    const raw = await openGrant(r.pin, state, r.memberID, r.xPrivate);
    const grantKey = await crypto.subtle.importKey("raw", raw, "HKDF", false, [
      "deriveBits",
    ]);
    const parameters = {
      name: "HKDF",
      hash: "SHA-256",
      salt: new Uint8Array(32),
      info: concat(
        new TextEncoder().encode("ra2/local-secret-check\0"),
        r.stateHash,
      ),
    };
    const expected = new Uint8Array(
      await crypto.subtle.deriveBits(parameters, grantKey, 256),
    );
    const actual = new Uint8Array(
      await crypto.subtle.deriveBits(parameters, r.secret, 256),
    );
    if (!equal(expected, actual)) throw new SecurityError("storage");
    if (!this.active()) throw new SecurityError("storage");
    return r;
  }
  private async load(): Promise<SecurityRecord> {
    return this.validateLocalRecord(await this.store.loadSecurity());
  }
  async localIdentity(): Promise<{
    pin: SecurityRecord["pin"];
    state: Awaited<ReturnType<typeof parseState>>;
  }> {
    const r = await this.load();
    return { pin: r.pin, state: await parseState(r.state) };
  }
  async localStateHash(): Promise<B> {
    return (await this.load()).stateHash;
  }
  async rpc<T>(method: Method, body: unknown): Promise<T> {
    const r = await this.load();
    const q = await requestRPC(r.pin, this.memberID, this.ed, method, body);
    const response = await this.exchange(q.capsule);
    await this.load();
    return replyRPC<T>(q.waiter, response, r.xPrivate);
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
    for (let pages = 0; pages < 32768; pages++) {
      const r = await this.load(),
        old = await parseState(r.state);
      const reply = await this.rpc<{
        Current: number[];
        States: StateJSON[] | null;
      }>(Method.States, { After: Number(old.core.epoch) });
      const head = jsonHash(reply.Current);
      if (equal(head, r.stateHash)) {
        if (reply.States?.length) throw new SecurityError();
        return r;
      }
      if (!reply.States?.length || reply.States.length > 2)
        throw new SecurityError("stale");
      let record = r;
      for (const raw of reply.States) {
        const next = await stateFromJSON(raw);
        await advance(record.pin, await parseState(record.state), next);
        try {
          await this.store.installSecurity(record, next);
        } catch (err) {
          if (err instanceof SecurityError && err.code === "stale") break;
          throw err;
        }
        record = await this.load();
      }
    }
    throw new SecurityError("capacity");
  }
  /** No formed C leaves the device until its exact authority authorization has
   * arrived and both signatures have been checked. Metadata C is HPKE-only. */
  async seal(
    h: NewSecurityHeader,
    plaintext: B,
    sessionSeed?: B,
  ): Promise<Envelope> {
    for (let attempt = 0; attempt < 3; attempt++) {
      const r = await this.validateLocalRecord(
        await this.store.reserveCounter(await this.load()),
      );
      const state = await parseState(r.state);
      const header: SecurityHeader = {
        ...h,
        account: r.pin.account,
        domain: r.pin.domain,
        epoch: state.core.epoch,
        state: r.stateHash,
        contentEpoch: state.core.contentEpoch,
        sender: this.memberID,
        counter: r.counter,
      };
      const c = await formCipher(
        r.secret,
        sessionSeed,
        header,
        plaintext,
        this.ed,
      );
      try {
        const reply = await this.rpc<{
          Authorization: string;
          Signature: string;
        }>(Method.Authorize, authRequestJSON(c, await commitment(c)));
        return toEnvelope(await attachAuthorization(r.pin, state, c, reply));
      } catch (err) {
        if (err instanceof SecurityError && err.code === "stale") {
          await this.sync();
          continue;
        }
        throw err;
      }
    }
    throw new SecurityError("stale");
  }
  async openCurrent(
    env: Envelope,
    seed?: B,
  ): Promise<{ header: SecurityHeader; plaintext: B }> {
    const wire = fromEnvelope(env);
    await this.sync();
    const r = await this.load();
    const opened = await openWire(
      r.pin,
      await parseState(r.state),
      wire,
      r.secret,
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
        Purpose.Command,
        Purpose.TerminalInput,
        Purpose.TerminalResize,
        Purpose.TerminalHistory,
      ].includes(h.purpose)
    )
      throw new SecurityError();
    let r = await this.load();
    if (h.epoch > (await parseState(r.state)).core.epoch) r = await this.sync();
    const state = await parseState(r.state);
    if (h.epoch === state.core.epoch) {
      const o = await openWire(r.pin, state, w, r.secret, seed);
      return {
        header: o.header,
        plaintext: unpackSecurity(o.header, o.plaintext),
      };
    }
    if (h.epoch > state.core.epoch) throw new SecurityError("stale");
    const cached = await this.store.readSecurityEpoch(h.epoch);
    if (cached) {
      const old = await parseState(cached.state);
      const o = await openWire(r.pin, old, w, cached.key, seed);
      return {
        header: o.header,
        plaintext: unpackSecurity(o.header, o.plaintext),
      };
    }
    const sr = await this.rpc<{ States: StateJSON[] }>(Method.States, {
      After: Number(h.epoch - 1n),
    });
    if (!sr.States?.length) throw new SecurityError("untrusted");
    const old = await stateFromJSON(sr.States[0]);
    await verifyState(r.pin, old);
    if (old.core.epoch !== h.epoch) throw new SecurityError("untrusted");
    const keys = await this.rpc<{ Keys: { Epoch: number; Secret: string }[] }>(
      Method.HistoryKeys,
      { After: Number(h.contentEpoch - 1n) },
    );
    if (keys.Keys?.[0]?.Epoch !== Number(h.contentEpoch))
      throw new SecurityError("untrusted");
    const { unb64 } = await import("./device-security");
    const rawKey = unb64(keys.Keys[0].Secret);
    await this.store.cacheSecurityEpoch(old, rawKey);
    const o = await openWire(r.pin, old, w, rawKey, seed);
    return {
      header: o.header,
      plaintext: unpackSecurity(o.header, o.plaintext),
    };
  }
  async catalog(): Promise<{
    state: Awaited<ReturnType<typeof parseState>>;
    entries: { object: B; wire: SecureWire }[];
  }> {
    const r = await this.sync();
    const state = await parseState(r.state);
    const expected = await stateHash(state);
    let cursor: B = new Uint8Array();
    const entries: { object: B; wire: SecureWire }[] = [];
    const { b64, unb64 } = await import("./device-security");
    for (let page = 0; page < 4096; page++) {
      const reply = await this.rpc<{
        State: StateJSON;
        Entries: { Object: string; Wire: GoWire }[] | null;
        Next: string | null;
      }>(Method.Catalog, { After: b64(cursor), AtState: [...expected] });
      const actual = await stateFromJSON(reply.State);
      await verifyState(r.pin, actual);
      if (!equal(await stateHash(actual), expected))
        throw new SecurityError("stale");
      for (const e of reply.Entries ?? []) {
        entries.push({ object: unb64(e.Object), wire: wireFromJSON(e.Wire) });
        if (entries.length > 4096) throw new SecurityError("capacity");
      }
      if (!reply.Next) return { state, entries };
      const next = unb64(reply.Next);
      if (compareBytes(next, cursor) <= 0) throw new SecurityError();
      cursor = next;
    }
    throw new SecurityError("capacity");
  }

  async remove(
    target: string,
    binding: B,
    expectedState: B,
  ): Promise<StoredRemoval> {
    const r = await this.load();
    const request = await signDeleteRequest(
      {
        Account: r.pin.account,
        Domain: [...r.pin.domain],
        Requester: this.memberID,
        Operation: newRequestID(),
        Expected: [...expectedState],
        Nonce: [...crypto.getRandomValues(new Uint8Array(32))],
        ExactIDs: false,
        Targets: [{ ID: target, MetadataCommit: [...binding] }],
      },
      this.ed,
    );
    const op: StoredRemoval = { request, phase: "unconfirmed", waiting: [] };
    await this.store.saveRemoval(op);
    return this.submitRemoval(op);
  }
  private async submitRemoval(op: StoredRemoval): Promise<StoredRemoval> {
    try {
      const group = await this.rpc<RevocationGroup>(Method.Delete, op.request);
      if (
        !group.Operations?.some(
          (o) => o.Request.Operation === op.request.Operation,
        )
      )
        throw new SecurityError("untrusted");
      op = {
        ...op,
        phase: "pending",
        waiting: group.Required.filter(
          (id) => !group.Receipts?.some((r) => r.Executor === id),
        ),
      };
      await this.store.saveRemoval(op);
      return await this.pollRemoval(op);
    } catch (e) {
      op = { ...op, error: e instanceof SecurityError ? e.code : "offline" };
      await this.store.saveRemoval(op);
      return op;
    }
  }
  async pendingRemovals(): Promise<StoredRemoval[]> {
    return this.store.loadRemovals();
  }
  async pollRemoval(op: StoredRemoval): Promise<StoredRemoval> {
    if (op.phase === "complete") return op;
    const r = await this.load();
    try {
      const completion = await this.rpc<Completion>(Method.GetCompletion, {
        Operation: op.request.Operation,
      });
      const group = completion.Group;
      const base = await stateFromJSON(group.Base),
        head = await stateFromJSON(group.Head);
      const chain: Awaited<ReturnType<typeof parseState>>[] = [];
      let after = base.core.epoch;
      for (let page = 0; after < head.core.epoch && page < 128; page++) {
        const reply = await this.rpc<{
          Current: number[];
          States: StateJSON[];
        }>(Method.CompletionStates, {
          Operation: op.request.Operation,
          After: Number(after),
        });
        if (!reply.States?.length || reply.States.length > 2)
          throw new SecurityError("untrusted");
        for (const raw of reply.States) {
          const s = await stateFromJSON(raw);
          if (s.core.epoch > head.core.epoch)
            throw new SecurityError("untrusted");
          chain.push(s);
          after = s.core.epoch;
        }
      }
      await verifyCompletion(r.pin, completion, op.request, chain);
      const done: StoredRemoval = {
        ...op,
        phase: "complete",
        waiting: [],
        error: undefined,
        cleanup: {
          completion: new TextEncoder().encode(JSON.stringify(completion)),
          request: new TextEncoder().encode(JSON.stringify(op.request)),
          chain: await Promise.all(chain.map((s) => stateBytes(s))),
        },
      };
      await this.store.saveRemoval(done);
      return done;
    } catch (e) {
      if (e instanceof SecurityError && e.code === "pending") {
        const status = await this.rpc<{ Group: RevocationGroup }>(
          Method.CompletionStates,
          { Operation: op.request.Operation, After: 0 },
        );
        const group = status.Group;
        if (
          !group?.Operations.some(
            (o) => o.Request.Operation === op.request.Operation,
          )
        )
          throw new SecurityError("untrusted");
        const pending = {
          ...op,
          phase: "pending" as const,
          waiting: group.Required.filter(
            (id) => !group.Receipts?.some((r) => r.Executor === id),
          ),
          error: undefined,
        };
        await this.store.saveRemoval(pending);
        return pending;
      }
      throw e;
    }
  }
  async markServerCleaned(op: StoredRemoval): Promise<StoredRemoval> {
    const done = { ...op, serverCleaned: true, cleanup: undefined };
    await this.store.saveRemoval(done);
    return done;
  }
  async retryRemoval(op: StoredRemoval): Promise<StoredRemoval> {
    return this.submitRemoval(op);
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
export interface GoWire {
  Header: string;
  Nonce: string;
  Cipher: string;
  Origin: string;
  Authorization: string;
  AuthSignature: string;
}
export function wireFromJSON(w: GoWire): SecureWire {
  // unb64 is statically imported below to avoid exposing decoded secrets in errors.
  return {
    header: decode64(w.Header),
    nonce: decode64(w.Nonce),
    cipher: decode64(w.Cipher),
    origin: decode64(w.Origin),
    authorization: decode64(w.Authorization),
    authSignature: decode64(w.AuthSignature),
  };
}
import { unb64 as decode64 } from "./device-security";
function compareBytes(a: B, b: B): number {
  for (let i = 0; i < Math.min(a.length, b.length); i++) {
    if (a[i] !== b[i]) return a[i] - b[i];
  }
  return a.length - b.length;
}
export function unpackSecurity(h: SecurityHeader, p: B): B {
  const limit = [
    Purpose.SessionMeta,
    Purpose.MachineMeta,
    Purpose.DeviceMeta,
    Purpose.Prefs,
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
    h.purpose === Purpose.Event &&
    (m.sessionId !== h.session || m.seq !== h.seq || !m.kind)
  )
    throw new SecurityError();
  if (h.purpose === Purpose.SessionMeta && m.id !== h.session)
    throw new SecurityError();
  if (
    h.purpose === Purpose.Notify &&
    (m.sessionId !== h.session ||
      m.machineId !== h.machine ||
      ![1, 2].includes(m.kind as number))
  )
    throw new SecurityError();
  if (h.purpose === Purpose.Prefs && m.schema !== 1) throw new SecurityError();
  if (
    h.purpose === Purpose.Command &&
    (!m.kind || typeof m.kind !== "object" || !m.kind.case)
  )
    throw new SecurityError();
  if (
    [
      Purpose.TerminalOutput,
      Purpose.TerminalInput,
      Purpose.TerminalResize,
      Purpose.TerminalHistory,
    ].includes(h.purpose)
  ) {
    const c = typeof m.kind === "object" ? m.kind.case : undefined;
    if (
      h.purpose === Purpose.TerminalOutput
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
