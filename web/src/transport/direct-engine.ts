import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { EnvelopeSchema, HeaderSchema, type Envelope } from "../gen/remoteagent/v1/envelope_pb";
import { ForwardResponseSchema, PeerFrameSchema } from "../gen/remoteagent/v1/relay_pb";
import { CapabilitySnapshotSchema, DirectedPeerFrameSchema, PeerFrameResultSchema } from "../gen/remoteagent/v1/transport_pb";
import type { MachineCapabilities } from "../gen/remoteagent/v1/capabilities_pb";
import { validLease } from "../device-security";
import type { SecurityRecord } from "../device-security-store";
import type { StoredAccount } from "../storage";
import { newRequestID } from "../crypto";
import type { FrameStream, PathAttempt } from "./path";
import { DirectBudget, DirectChannel } from "./direct-channel";
import { DirectSession, type DirectedOutput } from "./direct-session";
import { DirectRouting, type RouteHint } from "./direct-routing";
import { PathChanged, abortable } from "./recovery";
import * as w from "./peer/wire";

type Peer = { id: string; head: string; session: DirectSession; paths: Map<number, DirectChannel>; live: boolean; capabilities?: MachineCapabilities; snapshotUntil: number; downloadUntil: number; snapshotSeq: bigint; downloadSeq: bigint; redialAt?: number; selected?: boolean; };
type Pending = { peer: Peer; use: number; resolve: (env: Envelope) => void };

/** Account owner for optional direct paths. It never owns a logical request or
 * its outcome: Link and Remote retain authorization and delivery evidence. */
export class DirectEngine {
  private readonly instance = crypto.getRandomValues(new Uint8Array(16));
  private readonly boot = crypto.getRandomValues(new Uint8Array(32));
  private readonly budget = new DirectBudget();
  private record?: SecurityRecord;
  private self?: w.Identity;
  private codec?: w.Codec;
  private head = "";
  private freshUntil = 0;
  private syncRevision = 0;
  private lifetime = 0;
  private routingOwners = new Map<string, object>();
  private peers = new Map<string, Peer>();
  private sessions = new Map<string, Peer>();
  private routes = new Map<string, DirectRouting>();
  private pending = new Map<string, Pending>();
  private incoming = 0;
  private inboundPeers = new Map<string, {frames:number;bytes:number}>();
  private incomingBytes = 0;
  private chain = Promise.resolve();
  private leg?: FrameStream;
  private timer?: ReturnType<typeof setInterval>;
  private preferences = new Map<string, boolean>();
  private frameTokens = 256;
  private byteTokens = 4 << 20;
  private rateAt = performance.now();
  private retryAfter = new Map<string, number>();
  private readonly stun: string[];
  constructor(private readonly account: StoredAccount, private readonly authorize: () => Promise<void>, private readonly changed: () => void) {
    const host = new URL(account.server).hostname;
    this.stun = [import.meta.env.VITE_DIRECT_STUN_URL || `stun:${host.includes(":") ? `[${host}]` : host}:3478`];
  }
  private notify() { try { this.changed(); } catch { /* Observers do not own transport cleanup. */ } }
  private key(id: string) { return `remote-agent-direct:${this.account.server}:${this.account.deviceId}:${id}`; }
  enabled(id: string) {
    if (!this.preferences.has(id)) { let enabled = true; try { enabled = localStorage.getItem(this.key(id)) !== "off"; } catch { /* Default is automatic. */ } this.preferences.set(id, enabled); }
    return this.preferences.get(id)!;
  }
  setEnabled(id: string, enabled: boolean) {
    // Persistence must succeed before reporting a changed preference.
    localStorage.setItem(this.key(id), enabled ? "auto" : "off"); this.preferences.set(id, enabled);
    this.lifetime++; this.routingOwners.set(id, {});
    for (const p of this.sessions.values()) if (p.id === id) { p.live = false; p.session.close(); }
    this.peers.delete(id); this.routing(id).fail(); this.notify();
    if (enabled) void this.discover();
  }
  install(record: SecurityRecord) {
    if (!record.member || record.pin.account !== this.record?.pin.account || w.hex(record.stateHash) !== this.head) this.invalidate();
  }
  invalidate() {
    this.syncRevision++; this.lifetime++; this.routingOwners.clear(); this.freshUntil = 0;
    for (const p of this.sessions.values()) { p.live = false; p.session.close(); }
    this.peers.clear(); this.sessions.clear(); for (const r of this.routes.values()) r.fail(); this.notify();
  }
  async synced(record: SecurityRecord, started: number) {
    const revision = ++this.syncRevision;
    try {
      const head = record.states.at(-1)!;
      await validLease(record.lease, head, BigInt(Math.max(Date.now(), Number(record.floor))));
      if (!record.member || record.memberID !== this.account.deviceId || Date.now() >= started + 20_000) throw new PathChanged();
      const member = head.core.members.find(m => m.id === record.memberID)!;
      const signPub = await crypto.subtle.importKey("raw", member.ed, "Ed25519", false, ["verify"]);
      const boxPub = await crypto.subtle.importKey("raw", member.x, "X25519", false, []);
      if (revision !== this.syncRevision) return;
      const hash = w.hex(record.stateHash);
      if (this.head !== hash) { this.lifetime++; this.routingOwners.clear(); for (const p of this.sessions.values()) { p.live = false; p.session.close(); } this.peers.clear(); this.sessions.clear(); }
      this.record = record; this.head = hash; this.freshUntil = Math.min(started + 20_000, Number(record.lease!.notAfter));
      this.self = { id: w.utf8(member.id), role: 1n, instance: this.instance, boot: this.boot, sign: this.account.devicePrivate, box: record.xPrivate, signPub, boxPub };
      this.codec = w.codec({ account: record.pin.account, accountDomain: record.pin.domain, head: record.stateHash, epoch: head.core.epoch });
      await this.discover();
    } catch { if (revision === this.syncRevision) this.invalidate(); }
  }
  attach(leg: FrameStream) {
    this.leg = leg;
    if (!this.timer) {
      const timer = setInterval(() => { if (this.timer !== timer) return; if (!this.current()) { if (this.peers.size) this.invalidate(); } else void this.discover(); }, 1000);
      this.timer = timer;
    }
    for (const p of this.sessions.values()) p.session.discovered(); void this.discover();
  }
  detach(leg: FrameStream) { if (this.leg === leg) this.leg = undefined; }
  private current() { return !!this.record?.member && !!this.self && !!this.codec && Date.now() < this.freshUntil; }
  private valid(p: Peer) { return p.live && this.current() && p.head === this.head && this.enabled(p.id); }
  private async public(id: string): Promise<w.Public> {
    const member = this.record!.states.at(-1)!.core.members.find(m => m.id === id && m.kind === 1);
    if (!member) throw new PathChanged();
    return { id: w.utf8(id), role: 2n, instance: new Uint8Array(), boot: new Uint8Array(), signPub: await crypto.subtle.importKey("raw", member.ed, "Ed25519", false, ["verify"]), boxPub: await crypto.subtle.importKey("raw", member.x, "X25519", false, []) };
  }
  private async discover() {
    if (!this.current() || !this.leg?.ready) return;
    const head = this.head, leg = this.leg, lifetime = this.lifetime;
    const current = () => this.current() && head === this.head && leg === this.leg && lifetime === this.lifetime;
    for (const member of this.record!.states.at(-1)!.core.members) {
      if (!current()) return;
      const prior=this.peers.get(member.id);
      if(prior?.redialAt && Date.now()>=prior.redialAt && prior.session.active){
        // A failed use starts a new bounded pair round after cooldown. Closing
        // the old SDK objects remains charged to the shared budget.
        prior.session.retire();this.peers.delete(member.id);this.retryAfter.delete(member.id);
      }
      if (member.kind !== 1 || !this.enabled(member.id) || this.peers.get(member.id)?.session.active || Date.now() < (this.retryAfter.get(member.id) ?? 0)) continue;
      if (this.peers.size >= 4) break;
      try { const peer = await this.public(member.id); if (!current()) return; if (this.peers.get(member.id)?.session.active) continue; const p = this.create(peer, crypto.getRandomValues(new Uint8Array(16)), Date.now() + 10_000); if (p) await p.session.start(); } catch { /* Direct discovery is optional. */ }
    }
  }
  private create(peer: w.Public, connection: w.Bytes, deadline: number): Peer | undefined {
    const id = w.text(peer.id), now = Date.now();
    for (const [key, p] of this.sessions) if (now > p.session.retainUntil) { p.live = false; p.session.close(); this.sessions.delete(key); }
    if (!this.current() || !this.enabled(id) || this.sessions.size >= 128 || [...this.sessions.values()].filter(p => p.id === id).length >= 32 || !this.peers.has(id) && this.peers.size >= 4) return;
    const p = { id, head: this.head, paths: new Map<number, DirectChannel>(), live: true, snapshotUntil: 0, downloadUntil: 0, snapshotSeq: 0n, downloadSeq: 0n } as Peer;
    const choose = () => { if (!this.valid(p) || !p.session.active || this.sessions.get(w.hex(connection)) !== p) return; p.selected = true; this.select(p); for (const old of this.sessions.values()) if (old !== p && old.id === id) old.session.retire(); };
    p.session = new DirectSession({ self: this.self!, peer, codec: this.codec!, connection, deadline: Math.min(deadline, now + 10_000), budget: this.budget, stun: this.stun,
      valid: () => this.valid(p), authorize: async () => { await this.authorize(); if (!this.valid(p)) throw new PathChanged(); },
      send: out => this.send(p, out), chosen: choose, beforeOffer: choose,
      ready: (use, path) => { if (!this.valid(p) || !p.session.active || this.peers.get(id) !== p) { path.close(); return; } p.paths.set(use, path); this.notify(); },
      closed: use => { if (this.peers.get(id) !== p) return; if (use && p.session.active && p.session.established) {p.redialAt ??= Date.now()+5000;this.routing(id).fail();} if (![...p.paths.values()].some(path => path.ready)) { this.routing(id).fail(); if (this.peers.get(id) === p) this.peers.delete(id); this.retryAfter.set(id, Date.now() + 5000); } this.notify(); },
      frame: (raw, use) => this.frame(p, raw, use), capability: opened => this.capability(p, opened),
    });
    this.sessions.set(w.hex(connection), p);
    const prior = this.peers.get(id);
    if (!prior?.selected || !prior.session.active) this.select(p);
    return p;
  }
  private select(peer: Peer) {
    if (this.peers.get(peer.id) === peer) return;
    if (this.peers.has(peer.id)) this.routing(peer.id).fail();
    this.routingOwners.set(peer.id, {}); this.peers.set(peer.id, peer);
  }
  /** Completion ownership survives awaits without borrowing a replacement's
   * routing state. Callers still verify and return their original result. */
  track(id: string, hint: RouteHint) {
    const peer = this.peers.get(id), path = peer?.paths.get(hint.kind === "read" ? 1 : 2), lifetime = this.lifetime;
    let owner = this.routingOwners.get(id); if (!owner) { owner = {}; this.routingOwners.set(id, owner); }
    const current = () => lifetime === this.lifetime && this.routingOwners.get(id) === owner && this.peers.get(id) === peer && peer?.paths.get(hint.kind === "read" ? 1 : 2) === path;
    return {
      observe: (direct: boolean, bytes: number, milliseconds: number) => { if (current()) { this.routing(id).observe(hint, direct ? "direct" : "relay", bytes, milliseconds, path?.queuedBytes ?? 0); this.notify(); } },
      failed: () => { if (current()) { this.routing(id).fail(hint); this.notify(); } },
    };
  }
  private capability(p: Peer, opened: w.Opened) {
    if (!this.valid(p)) return;
    if (opened.type === 14) {
      if (opened.context.seq <= p.snapshotSeq) return;
      const snapshot = fromBinary(CapabilitySnapshotSchema, opened.payload);
      if (!snapshot.capabilities) return;
      p.snapshotSeq = opened.context.seq; p.capabilities = snapshot.capabilities; p.snapshotUntil = Number(snapshot.expiresAtMs);
    } else { if (opened.context.seq <= p.downloadSeq) return; p.downloadSeq = opened.context.seq; const fields = w.fields(opened.payload, "Capabilities"); p.downloadUntil = w.getUint(fields, 5) === 1n ? Number(w.getUint(fields, 4)) : 0; }
    this.notify();
  }
  private send(p: Peer, out: DirectedOutput) {
    if (!this.valid(p) || !out.valid() || !this.leg?.ready) return false;
    const now = performance.now(), elapsed = Math.max(0, now - this.rateAt) / 1000; this.rateAt = now;
    this.frameTokens = Math.min(256, this.frameTokens + elapsed * 256); this.byteTokens = Math.min(4 << 20, this.byteTokens + elapsed * (4 << 20));
    if (this.frameTokens < 1 || this.byteTokens < out.raw.length) return false;
    const fields = w.parse(out.raw, 5);
    const directed = create(DirectedPeerFrameSchema, { toDevice: w.getBytes(fields, 2), frameClass: Number(w.getUint(fields, 3)), frameId: w.getBytes(fields, 4), sealed: w.getBytes(fields, 5) });
    const raw = toBinary(PeerFrameSchema, create(PeerFrameSchema, { kind: { case: "directedPeerFrame", value: directed } }));
    try { this.leg.send(raw); this.frameTokens--; this.byteTokens -= out.raw.length; return true; } catch { return false; }
  }
  receive(raw: w.Bytes, leg: FrameStream) {
    if (leg !== this.leg || !this.current() || raw.byteLength > 65536 || this.incoming >= 64 || this.incomingBytes + raw.length > 4 << 20) return;
    let source: string;
    try {
      const f=w.parse(raw,5), cls=w.getUint(f,3); source=w.text(w.getBytes(f,1));
      if(f.size!==5||w.getBytes(f,1).length!==32||!w.equal(w.getBytes(f,2),this.self!.id)||w.getBytes(f,4).length!==16||cls<1n||cls>2n||cls===2n&&raw.length>16384||!this.record!.states.at(-1)!.core.members.some(m=>m.id===source&&m.kind===1))return;
    } catch {return;}
    const usage=this.inboundPeers.get(source)??{frames:0,bytes:0};
    if(usage.frames>=8||usage.bytes+raw.length>1<<20)return;
    usage.frames++;usage.bytes+=raw.length;this.inboundPeers.set(source,usage);
    this.incoming++; this.incomingBytes += raw.length;
    const copy = raw.slice(), lifetime = this.lifetime;
    this.chain = this.chain.then(async () => {
      if (!this.current() || leg !== this.leg || lifetime !== this.lifetime) return;
      const fields = w.parse(copy, 5), id = w.text(w.getBytes(fields, 1));
      if (!w.equal(w.getBytes(fields, 2), this.self!.id) || !this.enabled(id)) return;
      const head = this.head, codec = this.codec!, self = this.self!;
      const current = () => this.current() && head === this.head && leg === this.leg && lifetime === this.lifetime;
      const peer = await this.public(id);
      if (!current()) return;
      let opened: w.Opened | undefined, target: Peer | undefined;
      try { opened = await codec.open(copy, peer, self, Date.now()); } catch { /* Bound packets need the proven bootstrap nonce. */ }
      if (!current()) return;
      if (opened && opened.context.cls === 1n && opened.type === 1) {
        target = this.sessions.get(w.hex(opened.context.connection));
        if (!target) {
          peer.instance = opened.context.senderInstance; peer.boot = opened.context.senderBoot;
          for (const old of this.sessions.values()) if (old.id === id && old.session.peer.boot.length && !w.equal(old.session.peer.boot, peer.boot)) { old.live = false; old.session.close(); }
          target = this.create(peer, opened.context.connection, Number(opened.context.deadline));
        }
      } else {
        for (const p of this.sessions.values()) {
          if (p.id !== id || p.head !== head) continue;
          try { const o = await codec.open(copy, p.session.peer, self, Date.now()); if (!current()) return; if (w.equal(o.context.connection, p.session.options.connection)) { target = p; break; } } catch { /* Try only bounded current scopes. */ }
        }
      }
      if (current() && target && this.valid(target)) target.session.receive(copy);
    }).catch(() => undefined).finally(() => { this.incoming--; this.incomingBytes -= copy.length;usage.frames--;usage.bytes-=copy.length;if(!usage.frames)this.inboundPeers.delete(source); });
  }
  result(raw: w.Bytes, leg: FrameStream) {
    if (leg !== this.leg) return;
    try { const result = fromBinary(PeerFrameResultSchema, raw); for (const p of this.sessions.values()) p.session.delivery(new Uint8Array(result.frameId), result.status, result.retryAfterMs); } catch { /* An unauthenticated receipt cannot authorize a path. */ }
  }
  private frame(peer: Peer, raw: w.Bytes, use: number) {
    if (!this.valid(peer)) return;
    try {
      const env = fromBinary(EnvelopeSchema, raw), header = fromBinary(HeaderSchema, env.header), pending = this.pending.get(header.requestId);
      if (pending?.peer === peer && pending.use === use && header.machineId === peer.id) pending.resolve(env);
    } catch { /* Remote authenticates every routed result. */ }
  }
  routing(id: string) { let route = this.routes.get(id); if (!route) { route = new DirectRouting(); this.routes.set(id, route); } return route; }
  capabilities(id: string) { const p = this.peers.get(id); return p && this.valid(p) && Date.now() < p.snapshotUntil ? p.capabilities : undefined; }
  ready(id: string, hint: RouteHint) {
    const p = this.peers.get(id); if (!p || !this.valid(p) || !p.session.active || !p.paths.get(hint.kind === "read" ? 1 : 2)?.ready) return false;
    if (hint.kind === "bulk" && hint.upload) { const caps = this.capabilities(id); return !!caps && !!hint.bytes && hint.bytes <= caps.maxChunkBytes && (hint.upload === "file" ? caps.fileUpload : caps.imageUpload); }
    return hint.kind !== "bulk" || Date.now() < p.downloadUntil;
  }
  needsReadSamples(id: string) { return this.ready(id, {kind:"read"}) && this.routing(id).needsReadSamples(); }
  attempt(id: string, hint: RouteHint, handoff: () => void, sample = false): PathAttempt | undefined {
    if (sample ? hint.kind !== "read" || !this.needsReadSamples(id) : this.routing(id).choose(hint, this.ready(id, hint)) !== "direct") return;
    const peer = this.peers.get(id)!, use = hint.kind === "read" ? 1 : 2, path = peer.paths.get(use)!;
    const attemptId = newRequestID(), tracking = this.track(id, hint);
    const current = () => this.peers.get(id) === peer && peer.paths.get(use) === path && this.ready(id, hint);
    return { direct: true, attemptId, prepare: async () => { if (!current()) throw new PathChanged(); }, refusal: () => undefined, refreshAuth: async () => this.authorize(),
      send: async (_machine, command, signal) => {
        const header = fromBinary(HeaderSchema, command.header), request = header.requestId;
        if (!current() || this.pending.size >= 128 || this.pending.has(request)) throw new PathChanged();
        const timeout = new AbortController(), timer = setTimeout(() => timeout.abort(new PathChanged()), Math.max(1, Math.min(3000, Number(header.deadlineMs) - Date.now())));
        const canceled = AbortSignal.any([path.signal, timeout.signal, ...(signal ? [signal] : [])]);
        let resolve!: (env: Envelope) => void; const response = new Promise<Envelope>(r => { resolve = r; });
        const pending = { peer, use, resolve }; this.pending.set(request, pending);
        try { await path.write(toBinary(EnvelopeSchema, command), handoff, canceled); const result = await abortable(response, canceled); return create(ForwardResponseSchema, { result, attemptId }); }
        catch { tracking.failed(); throw new PathChanged(); }
        finally { clearTimeout(timer); if (this.pending.get(request) === pending) this.pending.delete(request); }
      },
    };
  }
  filePath(id: string) { const p=this.peers.get(id);return p && this.valid(p) && p.paths.get(2)?.ready ? this.routing(id).filePath : "relay"; }
  close() { clearInterval(this.timer); this.timer = undefined; this.leg = undefined; this.invalidate(); }
}
