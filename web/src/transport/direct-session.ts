import { DirectBudget, DirectChannel } from "./direct-channel";
import * as w from "./peer/wire";
import { Signal, candidateBody, endBody, inspectSDP, readCandidates, sdpBody, type Action, type Candidate } from "./peer/signal";
import { CandidateBatcher, Sender } from "./peer/sender";
import { knownPayload, restart } from "./peer/schema";
import { PathChanged } from "./recovery";

export interface DirectedOutput { raw: w.Bytes; deadline: number; critical: boolean; valid: () => boolean }
export interface DirectSessionOptions {
  self: w.Identity; peer: w.Public; codec: w.Codec; connection: w.Bytes; deadline: number;
  budget: DirectBudget; stun: string[]; valid: () => boolean; authorize: () => Promise<void>;
  send: (frame: DirectedOutput) => boolean;
  chosen: () => void; beforeOffer: () => void;
  ready: (use: number, channel: DirectChannel) => void;
  closed: (use?: number) => void; frame: (raw: w.Bytes, use: number) => void;
  capability: (opened: w.Opened) => void;
  probe?: (use: number, milliseconds: number) => void;
  factory?: (config: RTCConfiguration) => RTCPeerConnection;
}
type PC = { generation: bigint; restartSent: boolean; gate: Signal; sender: Sender; seq: bigint; channel?: DirectChannel; batch: CandidateBatcher; ufrag: w.Bytes; mid: w.Bytes; ended: boolean; confirmed: boolean; announced: boolean; actions: Action[] };

/** Serial owner for one connection and both uses. Deferred work keeps original bytes. */
export class DirectSession {
  private pcs: [PC, PC];
  private boot: Sender;
  private chain = Promise.resolve();
  private timer: ReturnType<typeof setInterval>;
  private tickQueued = false;
  private ended = false;
  private retired = false;
  private incoming = 0;
  private incomingBytes = 0;
  private controlSeq = 0n;
  private challenge?: w.Bytes;
  private receipts = new Map<string, { raw: w.Bytes; until: number }>();
  private controls = new Map<string, { hash: w.Bytes; until: number }>();
  private peerScope = new Map<string, number>();
  private peerWindows = new Map<bigint, { next: bigint; pending: Map<bigint, { opened: w.Opened; at: number }>; gap: number }>();
  retainUntil: number;
  established = false;
  constructor(readonly options: DirectSessionOptions) {
    const now = Date.now();
    if (!options.valid() || options.deadline <= now || options.deadline > now + 10_000) throw new PathChanged();
    const pc = (use: number): PC => {
      const gate = new Signal(options.codec, options.self, options.peer, options.connection, use, now);
      gate.roundDeadline = options.deadline; gate.current = options.valid; gate.leaseUntil = Number.MAX_SAFE_INTEGER;
      return { generation: 1n, restartSent: false, gate, sender: new Sender(options.deadline), seq: 0n, batch: new CandidateBatcher(1n, options.deadline, 1), ufrag: new Uint8Array(), mid: new Uint8Array(), ended: false, confirmed: false, announced: false, actions: [] };
    };
    this.pcs = [pc(1), pc(2)]; this.boot = new Sender(options.deadline); this.retainUntil = options.deadline + 300_000;
    this.timer = setInterval(() => {
      if (this.ended) return;
      const now = Date.now();
      for (const p of this.pcs) {
        if (!p.announced) p.gate.tick(now); else p.gate.clock = Math.max(p.gate.clock, now);
        p.sender.clock = Math.max(p.sender.clock, now);
      }
      this.boot.clock = Math.max(this.boot.clock, now);
      if (!options.valid() || this.retired && now >= this.retainUntil) { this.close(); return; }
      if (this.tickQueued) return; this.tickQueued = true;
      void this.queue(() => this.tick()).finally(() => { this.tickQueued = false; });
    }, 25);
  }
  get peer() { return this.pcs[0].gate.peer; }
  get active() { return !this.ended && !this.retired && this.options.valid(); }
  private queue(work: () => Promise<void>): Promise<void> {
    const job = this.chain.then(async () => { if (!this.ended) await work(); });
    this.chain = job.catch(() => undefined); return job.catch(() => undefined);
  }
  async start() {
    await this.queue(async () => {
      await this.options.authorize(); if (!this.active) return;
      const now = Date.now(), context = w.probeContext(this.options.self, this.options.peer, this.options.connection, 1, now);
      context.deadline = BigInt(this.options.deadline);
      const body = w.pb(w.b(1, this.options.connection), w.b(2, this.options.self.instance), w.b(3, this.options.self.boot));
      const raw = await this.options.codec.seal(this.options.self, this.options.peer, context, 1, body);
      const opened = await this.options.codec.openSent(raw, this.options.self, this.options.peer, now);
      for (const p of this.pcs) await p.gate.rememberProbe(opened);
      await this.boot.enqueue(raw, now, this.options.deadline, 1);
    });
  }
  receive(raw: w.Bytes) {
    if (this.ended || raw.byteLength > 64 << 10 || this.incoming >= 8 || this.incomingBytes + raw.byteLength > 1 << 20) return false;
    this.incoming++; this.incomingBytes += raw.byteLength;
    const frozen = raw.slice();
    void this.queue(() => this.receiveNow(frozen)).finally(() => { this.incoming--; this.incomingBytes -= frozen.byteLength; }); return true;
  }
  private async receiveNow(raw: w.Bytes) {
    await this.options.authorize(); if (this.ended || !this.options.valid()) return;
    const peer = this.pcs[0].gate.peer, o = await this.options.codec.open(raw, peer, this.options.self, Date.now()), c = o.context, now = Date.now();
    if (this.ended || !this.options.valid() || BigInt(now) >= c.deadline) return;
    if (!w.equal(c.connection, this.options.connection)) return;
    knownPayload(c.cls, o.type, o.payload);
    const key = w.cacheKey(c), oldControl = this.controls.get(key);
    if (this.retired) {
      if (oldControl && w.equal(oldControl.hash, o.hash)) { await this.replyCached(o); return; }
      for (const p of this.pcs) if (await p.gate.replyAllowed(raw, now)) { if (c.cls === 1n && o.type === 1 && this.challenge) await this.transmit(this.challenge); else await this.ack(o); break; }
      return;
    }
    if (this.pcs[0].gate.seen.size + this.pcs[1].gate.seen.size + this.controls.size >= 2047 && !oldControl && !this.pcs.some(p => p.gate.seen.has(key))) return;
    this.retainUntil = Math.max(this.retainUntil, Number(c.deadline) + 300_000);
    if (c.cls === 2n && o.type === 4) {
      const fields = w.fields(o.payload, "Ack"), hash = w.getBytes(fields, 6);
      for (const sender of [this.boot, ...this.pcs.map(p => p.sender)]) {
        const sent = sender.pending.get(w.hex(hash)); if (!sent) continue;
        const original = await this.options.codec.openSent(sent.raw, this.options.self, peer, now), q = original.context;
        if (q.cls !== w.getUint(fields, 4) || q.seq !== w.getUint(fields, 5) || q.generation !== w.getUint(fields, 7) || q.use !== w.getUint(fields, 8)) return;
        if (original.type === 2 && q.cls === 1n && q.generation === 0n) for (const p of this.pcs) await p.gate.receive(raw, now);
        sender.ack(hash);
        for (let i = 0; i < 2; i++) await this.maybeOffer(i);
        return;
      }
      return;
    }
    if (c.cls === 1n && [1, 2].includes(o.type)) {
      for (const p of this.pcs) await p.gate.receive(raw, now);
      if (o.type === 1) {
        if (!this.challenge) {
          const learned = this.pcs[0].gate.peer;
          const context = w.context(this.options.self, learned, this.options.connection, 0, 0, 1, now, 1, true); context.deadline = BigInt(Math.min(this.options.deadline, Number(c.deadline)));
          const body = w.pb(w.b(1, this.options.connection), w.b(2, this.options.self.instance), w.b(3, this.options.self.boot), w.b(4, learned.boot), w.b(6, o.hash));
          this.challenge = await this.options.codec.seal(this.options.self, learned, context, 2, body);
          const sent = await this.options.codec.openSent(this.challenge, this.options.self, learned, now);
          for (const p of this.pcs) await p.gate.rememberChallenge(sent);
          await this.boot.enqueue(this.challenge, now, Number(context.deadline), 1);
        } else if (await this.pcs[0].gate.replyAllowed(raw, now)) await this.transmit(this.challenge);
      } else {
        const probe = this.pcs[0].gate.probe; if (probe) this.boot.ack(probe.hash);
        await this.ack(o); for (let i = 0; i < 2; i++) await this.maybeOffer(i);
      }
      return;
    }
    if (c.cls === 2n) {
      if (oldControl) { if (w.equal(oldControl.hash, o.hash)) await this.replyCached(o); return; }
      if (o.type === 1 || o.type === 14) {
        const fields = w.fields(o.payload, o.type === 1 ? "Capabilities" : "CapabilitySnapshot"), expires = w.getUint(fields, o.type === 1 ? 4 : 3);
        if (expires <= BigInt(now) || expires <= c.issued || expires - c.issued > 60_000n) return;
        this.options.capability(o);
      }
      this.controls.set(key, { hash: o.hash, until: Number(c.deadline) + 300_000 });
      if (o.type === 10) { await this.unsupported(o, now); return; }
      if ([11, 12, 13].includes(o.type)) return;
      if (o.type === 1 || o.type === 14) await this.ack(o);
      else await this.respond(o, 10, w.pb(w.u(1, c.cls), w.u(2, o.type), w.u(3, c.seq), w.b(4, o.hash)));
      return;
    }
    if (c.cls === 1n && c.use === 0n) { await this.receivePeerScope(o, now); return; }
    if (c.use < 1n || c.use > 2n) return;
    const index = Number(c.use) - 1, p = this.pcs[index];
    const actions = await p.gate.receive(raw, now);
    if (await p.gate.replyAllowed(raw, now)) await this.ack(o);
    for (const action of actions) {
      if (action.cls === 2 && action.type === 10) { const original = await this.options.codec.open(action.original, p.gate.peer, this.options.self, now); await this.respond(original, 10, action.payload); }
      else if (action.cls === 1) { if (action.type === 3) this.options.beforeOffer(); if (p.actions.length >= 32) { this.retire(); return; } p.actions.push(action); }
    }
    await this.drain(index); await this.maybeReady(index);
  }
  private validPeerScope(o: w.Opened) {
    if (o.type === 9) return true;
    if (o.type !== 8 || !this.pcs[0].gate.offerer()) return false;
    const targets = restart(o.payload);
    return [...targets].every(([use, generation]) => use >= 1n && use <= 2n && this.pcs[Number(use) - 1].gate.generation === generation);
  }
  private async receivePeerScope(o: w.Opened, now: number) {
    if (now >= this.options.deadline) return;
    const key=w.cacheKey(o.context), old=this.controls.get(key);
    if (old) { if(w.equal(old.hash,o.hash)) await this.replyCached(o);return; }
    if (!this.validPeerScope(o)) return;
    let window=this.peerWindows.get(o.context.generation);
    if(!window){if(this.peerWindows.size>=32)return;window={next:1n,pending:new Map(),gap:0};this.peerWindows.set(o.context.generation,window);}
    if(o.context.seq<window.next||window.pending.size>=32)return;
    this.controls.set(key,{hash:o.hash,until:Number(o.context.deadline)+300000});
    if(o.context.seq>window.next){window.pending.set(o.context.seq,{opened:o,at:now});if(!window.gap)window.gap=Math.min(now+2500,Number(o.context.deadline),this.options.deadline);return;}
    for(;;){
      const fresh=await this.options.codec.open(o.raw,this.peer,this.options.self,Date.now());
      if(!this.active||!this.validPeerScope(fresh)){this.retire();return;}
      await this.ack(fresh);
      if(fresh.type===9){this.retire();return;}
      for(const [use]of restart(fresh.payload))await this.restartPC(Number(use)-1);
      window.next++;const next=window.pending.get(window.next);if(!next)break;window.pending.delete(window.next);o=next.opened;
    }
    window.gap=window.pending.size?Math.min(...[...window.pending.values()].map(v=>Math.min(v.at+2500,Number(v.opened.context.deadline),this.options.deadline))):0;
  }
  private async ack(o: w.Opened) {
    if (o.context.cls === 1n && o.type === 1 || o.context.cls === 2n && [4, 10, 11, 12, 13].includes(o.type)) return;
    if (await this.replyCached(o)) return;
    if (o.context.cls === 1n && (o.type < 1 || o.type > 9)) return;
    await this.respond(o, 4, w.pb(w.u(4, o.context.cls), w.u(5, o.context.seq), w.b(6, o.hash), w.u(7, o.context.generation), w.u(8, o.context.use)));
  }
  private async replyCached(o: w.Opened) { const reply = this.receipts.get(w.hex(o.hash)); if (!reply) return false; if (Date.now() < Number(o.context.deadline)) await this.transmit(reply.raw); return true; }
  private async respond(original: w.Opened, type: number, body: w.Bytes) {
    const now = Date.now(); if (now >= Number(original.context.deadline) || this.receipts.size >= 2048 || !this.options.valid()) return;
    const context = w.context(this.options.self, this.pcs[0].gate.peer, this.options.connection, 0, 1, ++this.controlSeq, now, 2);
    context.deadline = original.context.deadline < context.deadline ? original.context.deadline : context.deadline;
    const raw = await this.options.codec.seal(this.options.self, this.pcs[0].gate.peer, context, type, body);
    this.receipts.set(w.hex(original.hash), { raw, until: Number(original.context.deadline) + 300_000 }); await this.transmit(raw);
  }
  private async unsupported(o: w.Opened, now: number) {
    const fields = w.fields(o.payload, "Unsupported"), hash = w.getBytes(fields, 4);
    for (const [index, p] of this.pcs.entries()) {
      const pending = p.sender.pending.get(w.hex(hash)); if (!pending) continue;
      const original = await this.options.codec.openSent(pending.raw, this.options.self, p.gate.peer, now);
      if (original.context.cls !== w.getUint(fields, 1) || BigInt(original.type) !== w.getUint(fields, 2) || original.context.seq !== w.getUint(fields, 3)) return;
      p.sender.ack(hash); p.channel?.close(); p.channel = undefined; p.gate.closed = true; this.peerScope.set(String(index), now); return;
    }
  }
  private async signal(index: number, type: number, body: w.Bytes) {
    const p = this.pcs[index], now = Date.now(), generation = p.gate.generation, sender = p.sender;
    const context = w.context(this.options.self, p.gate.peer, this.options.connection, index + 1, p.gate.generation, ++p.seq, now); context.deadline = BigInt(this.options.deadline);
    const raw = await this.options.codec.seal(this.options.self, p.gate.peer, context, type, body);
    if (!this.active || p.gate.generation !== generation || p.sender !== sender) return false;
    return sender.enqueue(raw, now, Number(context.deadline), 1);
  }
  private makePC(index: number) {
    const p = this.pcs[index]; if (p.generation !== p.gate.generation) this.resetPC(index); if (p.channel) return true;
    if (!this.active || p.gate.closed || Date.now() >= this.options.deadline) return false;
    const generation = p.gate.generation;
    let channel: DirectChannel | undefined;
    const current = () => channel !== undefined && p.channel === channel && p.generation === generation && p.gate.generation === generation;
    try {
      channel = new DirectChannel({ peer: w.text(this.options.peer.id), connection: this.options.connection, use: index + 1, generation, deadline: this.options.deadline, budget: this.options.budget, stun: this.options.stun, valid: () => this.active && p.gate.generation === generation && (!channel || current()), factory: this.options.factory,
        candidate: candidate => { void this.queue(async () => {
          if (!this.active || !current()) return;
          if (candidate === null) p.ended = true;
          else if (!p.batch.add(w.utf8(candidate.candidate ?? ""), generation, Date.now(), 1)) channel!.close();
        }); },
        state: state => { void this.queue(async () => {
          if (!this.active || !current()) return;
          if (["failed", "disconnected"].includes(state)) await this.restartPC(index);
          else if (state === "connected" && !channel!.signal.aborted) { p.gate.dtls = true; await this.maybeReady(index); }
        }); },
        frame: raw => { if (this.active && current()) this.options.frame(raw, index + 1); },
        closed: () => { if (current()) this.options.closed(index + 1); },
        probe: elapsed => { if (this.active && current()) this.options.probe?.(index + 1, elapsed); },
      }); p.channel = channel; this.options.chosen(); return true;
    } catch { return false; }
  }
  private async maybeOffer(index: number) {
    const p = this.pcs[index]; if (!this.active || !p.gate.bootReady || !p.gate.offerer() || p.gate.localSDP || !this.makePC(index)) return;
    await this.options.authorize(); if (!this.active) return;
    const raw = await p.channel!.description(true), info = inspectSDP(raw);
    await p.gate.setLocal(raw, true, p.gate.generation); p.ufrag = info.ufrag; p.mid = info.mid;
    await this.signal(index, 3, await sdpBody(raw));
  }
  private async drain(index: number) {
    const p = this.pcs[index];
    while (p.actions.length && this.active) {
      const action = p.actions[0];
      if (action.generation !== p.gate.generation) { p.actions.shift(); continue; }
      await this.options.authorize(); if (!this.active) return;
      if ([3, 4, 5, 6].includes(action.type)) {
        if (!this.makePC(index)) return;
        if (!await p.gate.dispatch(action.original, Date.now())) { p.actions.shift(); continue; }
      }
      p.actions.shift();
      switch (action.type) {
        case 3: case 4:
          await p.channel!.remote(action.payload, action.type === 3);
          if (action.type === 3) { const raw = await p.channel!.description(false), info = inspectSDP(raw); await p.gate.setLocal(raw, false, p.gate.generation); p.ufrag = info.ufrag; p.mid = info.mid; await this.signal(index, 4, await sdpBody(raw)); }
          break;
        case 5:
          for (const candidate of readCandidates(action.payload).list) await p.channel!.addCandidate({ candidate: w.text(candidate.original), sdpMid: w.text(candidate.mid), sdpMLineIndex: Number(candidate.index), usernameFragment: w.text(candidate.ufrag) });
          break;
        case 6: await p.channel!.addCandidate(); break;
        case 8: await this.restartPC(index); break;
        case 9: p.channel?.close(); break;
      }
    }
  }
  private async maybeReady(index: number) {
    const p = this.pcs[index], channel = p.channel, generation = p.gate.generation;
    if (!channel || !this.active || channel.signal.aborted) return;
    if (!p.confirmed && p.gate.localSDP && p.gate.remoteSDP && p.gate.dtls) {
      const confirmed = await this.signal(index, 7, p.gate.confirmBody());
      if (!this.active || p.channel !== channel || p.gate.generation !== generation || channel.signal.aborted) return;
      p.confirmed = confirmed;
    }
    if (channel.traversalReady && p.confirmed && !p.announced && p.gate.ready()) { p.announced = true; this.established = true; channel.confirm(); this.options.ready(index + 1, channel); }
  }
  private async tick() {
    const now = Date.now();
    if ([...this.peerWindows.values()].some(w => w.gap && now >= w.gap)) { this.retire(); return; }
    for (const [key, item] of this.controls) if (now > item.until) this.controls.delete(key);
    for (const [key, item] of this.receipts) if (now > item.until) this.receipts.delete(key);
    if (now >= this.options.deadline && !this.established && !this.retired) { this.close(); return; }
    for (const raw of await this.boot.tick(now)) await this.transmit(raw);
    for (const [index, p] of this.pcs.entries()) {
      for (const [key, until] of p.gate.cacheUntil) if (now > until) { p.gate.cacheUntil.delete(key); p.gate.seen.delete(key); }
      if (!p.announced && (p.gate.closed || now >= this.options.deadline)) { p.channel?.close(); p.channel = undefined; }
      if (this.active && p.gate.retired && now < this.options.deadline) await this.restartPC(index);
      if (this.active && !p.gate.closed) { await this.maybeOffer(index); await this.drain(index); await this.maybeReady(index); }
      let batch = p.batch.batches;
      for (const group of p.batch.flush(now, p.ended, p.gate.generation, 1)) {
        const candidates: Candidate[] = group.map(original => ({ original, ufrag: p.ufrag, mid: p.mid, index: 0n, generation: p.gate.generation }));
        await this.signal(index, 5, candidateBody(candidates, batch++));
      }
      if (p.ended && !p.batch.pending.length && p.channel) { p.ended = false; await this.signal(index, 6, endBody(p.ufrag, p.batch.count, p.batch.batches)); }
      for (const raw of await p.sender.tick(now)) await this.transmit(raw);
    }
  }
  private resetPC(index: number) {
    const p = this.pcs[index], old = p.channel; p.channel = undefined; old?.close();
    p.generation = p.gate.generation; p.seq = 0n; p.sender = new Sender(this.options.deadline);
    p.batch = new CandidateBatcher(p.gate.generation, this.options.deadline, 1);
    p.confirmed = p.announced = p.ended = p.restartSent = false; p.ufrag = p.mid = new Uint8Array();
  }
  private async restartPC(index: number) {
    const p = this.pcs[index]; if (!this.active || Date.now() >= this.options.deadline) return;
    if (p.gate.offerer()) { p.gate.nextGeneration(p.gate.generation + 1n); this.resetPC(index); p.actions = []; await this.maybeOffer(index); }
    else if (!p.restartSent) { p.restartSent = await this.signal(index, 8, w.pb(w.b(1, w.pb(w.u(1, index + 1), w.u(2, p.gate.generation))))); }
  }
  private async transmit(raw: w.Bytes) {
    const o = await this.options.codec.openSent(raw, this.options.self, this.pcs[0].gate.peer, Date.now()), c = o.context;
    return this.options.send({ raw, deadline: Number(c.deadline), critical: c.cls === 2n || ![5, 6].includes(o.type), valid: () => !this.ended && this.options.valid() && Date.now() < Number(c.deadline) && !(c.cls === 1n && c.use > 0n && c.use <= 2n && c.generation !== this.pcs[Number(c.use) - 1].generation) && !(this.retired && c.cls === 1n && (o.type === 1 || o.type >= 3 && o.type <= 8)) && !(c.cls === 1n && o.type === 1 && !this.options.budget.ownerAvailable(w.text(this.options.peer.id), w.hex(this.options.connection))) });
  }
  delivery(id: w.Bytes, status: number, retry: number) {
    void this.queue(async () => { for (const sender of [this.boot, ...this.pcs.map(p => p.sender)]) for (const pending of sender.pending.values()) { if (!w.equal(w.getBytes(w.parse(pending.raw, 5), 4), id)) continue; if (status === 3) sender.limited(pending.hash, Date.now(), retry); if (status === 2) sender.offline(); } });
  }
  discovered() { this.boot.discovered(); for (const p of this.pcs) p.sender.discovered(); }
  retire() { if (this.retired) return; this.retired = true; for (const p of this.pcs) { p.gate.retire(false); p.channel?.close(); p.channel = undefined; p.actions = []; } }
  close() { if (this.ended) return; this.ended = true; clearInterval(this.timer); for (const p of this.pcs) p.channel?.close(); this.options.closed(); }
}
