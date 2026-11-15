import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { FragmentSchema } from "../gen/remoteagent/v1/transport_pb";
import * as w from "./peer/wire";
import { normalize } from "./peer/schema";
import { validateSDP } from "./peer/sdp";
import { PathChanged, abortable } from "./recovery";

const FRAME_BYTES = 16 << 10;
const PAYLOAD_BYTES = FRAME_BYTES - 128;
type Usage = { owner: string; uses: Set<number>; bytes: [number, number]; frames: [number, number] };
/** Shared by both PC uses and replacement connections; hints never reset it. */
export class DirectBudget {
  private peers = new Map<string, Usage>();
  private bytes: [number, number] = [0, 0];
  constructor(readonly maxPeers = 4) {}
  acquire(peer: string, owner: string, use: number) {
    let current = this.peers.get(peer);
    if (!current) {
      if (this.peers.size >= this.maxPeers) return false;
      this.peers.set(peer, current = { owner, uses: new Set(), bytes: [0, 0], frames: [0, 0] });
    }
    if (current.owner !== owner || current.uses.has(use) || current.uses.size >= 2) return false;
    current.uses.add(use); return true;
  }
  ownerAvailable(peer: string, owner: string) { return !this.peers.has(peer) || this.peers.get(peer)!.owner === owner; }
  release(peer: string, use: number) { const current = this.peers.get(peer)!; current.uses.delete(use); this.prune(peer, current); }
  reserve(peer: string, direction: 0 | 1, bytes: number, frames: number) {
    const current = this.peers.get(peer);
    if (!current || bytes < 0 || frames < 0 || current.bytes[direction] + bytes > 2 << 20 || current.frames[direction] + frames > 128 || this.bytes[direction] + bytes > 16 << 20) return false;
    current.bytes[direction] += bytes; current.frames[direction] += frames; this.bytes[direction] += bytes; return true;
  }
  free(peer: string, direction: 0 | 1, bytes: number, frames: number) {
    const current = this.peers.get(peer)!;
    current.bytes[direction] -= bytes; current.frames[direction] -= frames; this.bytes[direction] -= bytes; this.prune(peer, current);
  }
  private prune(peer: string, current: Usage) { if (!current.uses.size && !current.bytes[0] && !current.bytes[1] && !current.frames[0] && !current.frames[1]) this.peers.delete(peer); }
}

type PartialMessage = { count: number; length: number; hash: w.Bytes; parts: Map<number, w.Bytes>; bytes: number; until: number };
export interface ChannelOptions {
  peer: string; connection: w.Bytes; use: number; generation: bigint; deadline: number;
  budget: DirectBudget; valid: () => boolean; stun: string[];
  candidate: (candidate: RTCIceCandidateInit | null) => void;
  state: (state: RTCPeerConnectionState) => void;
  frame: (raw: w.Bytes) => void; closed: () => void;
  probe?: (milliseconds: number) => void;
  factory?: (config: RTCConfiguration) => RTCPeerConnection;
}
/** Browser-native adapter. SDK objects, SDP and candidates stay below Link. */
export class DirectChannel {
  private readonly pc: RTCPeerConnection;
  private readonly data: RTCDataChannel;
  private readonly probe: RTCDataChannel;
  private readonly lifetime = new AbortController();
  private confirmed = false;
  private ended = false;
  private chain = Promise.resolve();
  private partial = new Map<string, PartialMessage>();
  private waiting: w.Bytes[] = [];
  private probes = new Map<string, number>();
  private tick: ReturnType<typeof setInterval>;
  private lastProbe = 0;
  private missedProbes = 0;
  private writers = 0;
  private reservationReleased = false;
  private listeners = new Set<() => void>();
  constructor(private readonly options: ChannelOptions) {
    const owner = w.hex(options.connection);
    if (!options.valid() || options.stun.length > 2 || options.stun.some(url => !url.startsWith("stun:") || url.includes("?") && !url.endsWith("?transport=udp")) || !options.budget.acquire(options.peer, owner, options.use)) throw new PathChanged();
    let created: RTCPeerConnection | undefined;
    try {
      this.pc = created = (options.factory ?? (config => new RTCPeerConnection(config)))({ iceServers: options.stun.length ? [{ urls: options.stun }] : [] });
      this.data = this.pc.createDataChannel("remote-agent", { id: 0, negotiated: true, ordered: true });
      this.probe = this.pc.createDataChannel("remote-agent", { id: 1, negotiated: true, ordered: false, maxRetransmits: 0 });
    } catch { created?.close(); options.budget.release(options.peer, options.use); throw new PathChanged(); }
    this.data.binaryType = this.probe.binaryType = "arraybuffer";
    this.data.bufferedAmountLowThreshold = 32 << 10;
    const wake = () => { for (const listener of [...this.listeners]) listener(); };
    this.data.onbufferedamountlow = this.data.onopen = wake;
    this.data.onclose = this.probe.onclose = () => this.close();
    this.data.onerror = this.probe.onerror = () => this.close();
    this.pc.ondatachannel = event => { event.channel.close(); this.close(); };
    this.pc.onicecandidate = event => { if (this.valid()) options.candidate(event.candidate?.toJSON() ?? null); };
    this.pc.onconnectionstatechange = () => {
      if (this.ended) return;
      options.state(this.pc.connectionState);
      if (["failed", "closed", "disconnected"].includes(this.pc.connectionState)) this.close();
    };
    this.pc.onicecandidateerror = () => { /* Never log SDK error objects or addresses. */ };
    this.data.onmessage = event => {
      if (!this.valid()) return;
      if (!(event.data instanceof ArrayBuffer) || event.data.byteLength > FRAME_BYTES) { this.close(); return; }
      // Parsing and fragment admission finish synchronously. Only the final
      // hash awaits WebCrypto, with its complete buffer already reserved.
      void this.receive(new Uint8Array(event.data)).catch(() => this.close());
    };
    this.probe.onmessage = event => {
      if (!this.ready || !(event.data instanceof ArrayBuffer) || event.data.byteLength !== 17) return;
      const raw = new Uint8Array(event.data);
      if (raw[0] === 0) { raw[0] = 1; this.probe.send(raw); }
      else if (raw[0] === 1) {
        const id = w.hex(raw.subarray(1)), start = this.probes.get(id);
        if (start !== undefined) { this.probes.delete(id); this.missedProbes=0; options.probe?.(performance.now() - start); }
      }
    };
    this.tick = setInterval(() => {
      if (!this.valid()) { this.close(); return; }
      const now = performance.now();
      for (const [key, part] of this.partial) if (now >= part.until) this.drop(key, part);
      for (const [id, start] of this.probes) if (now - start >= 1000) { this.probes.delete(id); if (++this.missedProbes >= 3) { this.close(); return; } }
      if (this.ready && this.probe.readyState === "open" && now - this.lastProbe >= 1000) {
        this.lastProbe = now;
        const raw = crypto.getRandomValues(new Uint8Array(17)); raw[0] = 0;
        this.probes.set(w.hex(raw.subarray(1)), now); this.probe.send(raw);
      }
    }, 100);
  }
  private valid() { return !this.ended && this.options.valid() && (this.confirmed || Date.now() < this.options.deadline); }
  get ready() { return this.confirmed && this.valid() && this.data.readyState === "open"; }
  get signal() { return this.lifetime.signal; }
  async description(offer: boolean, restart = false): Promise<w.Bytes> {
    if (!this.valid()) throw new PathChanged();
    const description = offer ? await this.pc.createOffer({ iceRestart: restart }) : await this.pc.createAnswer();
    if (!this.valid() || !description.sdp) throw new PathChanged();
    const raw = w.utf8(description.sdp); validateSDP(raw);
    // Freeze the SDK original before collection. Never trim LocalDescription.
    await this.pc.setLocalDescription(description);
    if (!this.valid()) throw new PathChanged(); return raw;
  }
  async remote(raw: w.Bytes, offer: boolean) {
    if (!this.valid()) throw new PathChanged(); validateSDP(raw);
    await this.pc.setRemoteDescription({ type: offer ? "offer" : "answer", sdp: w.text(raw) });
    if (!this.valid()) throw new PathChanged();
  }
  async addCandidate(candidate?: RTCIceCandidateInit) {
    if (!this.valid()) throw new PathChanged(); await this.pc.addIceCandidate(candidate);
    if (!this.valid()) throw new PathChanged();
  }
  confirm() { if (!this.valid()) return; this.confirmed = true; for (const raw of this.waiting.splice(0)) this.deliver(raw); }
  write(raw: w.Bytes, handoff: () => void, signal?: AbortSignal): Promise<void> {
    const count = Math.ceil(raw.byteLength / PAYLOAD_BYTES);
    if (!this.ready || !raw.byteLength || raw.byteLength > 1 << 20 || !this.options.budget.reserve(this.options.peer, 0, raw.byteLength, count)) return Promise.reject(new PathChanged());
    const frozen = raw.slice(); this.writers++;
    const job = this.chain.then(() => this.writeNow(frozen, handoff, signal)).finally(() => { this.options.budget.free(this.options.peer, 0, frozen.byteLength, count); this.writers--; this.releaseReservation(); });
    this.chain = job.catch(() => undefined); return abortable(job, signal);
  }
  private async writeNow(raw: w.Bytes, handoff: () => void, signal?: AbortSignal) {
    const hash = await w.hash(raw), id = crypto.getRandomValues(new Uint8Array(16)), count = Math.ceil(raw.byteLength / PAYLOAD_BYTES);
    for (let index = 0; index < count; index++) {
      while (this.valid() && (!this.ready || this.data.bufferedAmount > 64 << 10)) {
        await new Promise<void>((resolve, reject) => {
          const wake = () => { cleanup(); resolve(); }, abort = () => { cleanup(); reject(new PathChanged()); };
          const cleanup = () => { this.listeners.delete(wake); this.lifetime.signal.removeEventListener("abort", abort); signal?.removeEventListener("abort", abort); };
          this.listeners.add(wake); this.lifetime.signal.addEventListener("abort", abort, { once: true }); signal?.addEventListener("abort", abort, { once: true });
          if (signal?.aborted || this.ended) abort();
        });
      }
      signal?.throwIfAborted(); if (!this.ready) throw new PathChanged();
      const frame = toBinary(FragmentSchema, create(FragmentSchema, { messageId: id, index, count, ciphertextLength: raw.byteLength, ciphertextSha256: hash, pathGeneration: this.options.generation, fragment: raw.subarray(index * PAYLOAD_BYTES, Math.min(raw.byteLength, (index + 1) * PAYLOAD_BYTES)) }));
      const maximum = this.pc.sctp?.maxMessageSize ?? FRAME_BYTES;
      if (frame.byteLength > FRAME_BYTES || maximum && frame.byteLength > maximum) throw new PathChanged();
      if (index === 0) handoff(); this.data.send(frame);
    }
  }
  private async receive(raw: w.Bytes) {
    if (!this.valid()) return;
    normalize(raw, "Fragment"); const frame = fromBinary(FragmentSchema, raw);
    if (frame.messageId.byteLength !== 16 || frame.ciphertextSha256.byteLength !== 32 || frame.pathGeneration !== this.options.generation || !frame.count || frame.count > 128 || frame.index >= frame.count || !frame.ciphertextLength || frame.ciphertextLength > 1 << 20 || !frame.fragment.byteLength) return;
    const key = w.hex(new Uint8Array(frame.messageId));
    let part = this.partial.get(key);
    if (!part) part = { count: frame.count, length: frame.ciphertextLength, hash: new Uint8Array(frame.ciphertextSha256), parts: new Map(), bytes: 0, until: performance.now() + 5000 };
    if (part.count !== frame.count || part.length !== frame.ciphertextLength || !w.equal(part.hash, new Uint8Array(frame.ciphertextSha256))) return;
    const old = part.parts.get(frame.index);
    if (old) { if (!w.equal(old, new Uint8Array(frame.fragment))) this.close(); return; }
    if (part.bytes + frame.fragment.byteLength > part.length || !this.options.budget.reserve(this.options.peer, 1, frame.fragment.byteLength, 1)) return;
    part.parts.set(frame.index, new Uint8Array(frame.fragment)); part.bytes += frame.fragment.byteLength; this.partial.set(key, part);
    if (part.parts.size !== part.count) return;
    if (part.bytes !== part.length || !this.options.budget.reserve(this.options.peer, 1, part.bytes, 1)) { this.drop(key, part); return; }
    const completed = new Uint8Array(part.length); let offset = 0;
    for (let index = 0; index < part.count; index++) { completed.set(part.parts.get(index)!, offset); offset += part.parts.get(index)!.byteLength; }
    const hash = part.hash; this.drop(key, part);
    const valid = w.equal(hash, await w.hash(completed));
    if (!valid || !this.valid()) { this.options.budget.free(this.options.peer, 1, completed.byteLength, 1); return; }
    if (this.confirmed) this.deliver(completed); else this.waiting.push(completed);
  }
  private deliver(raw: w.Bytes) { this.options.budget.free(this.options.peer, 1, raw.byteLength, 1); if (this.valid() && this.confirmed) this.options.frame(raw); }
  private drop(key: string, part: PartialMessage) { this.partial.delete(key); this.options.budget.free(this.options.peer, 1, part.bytes, part.parts.size); }
  close() {
    if (this.ended) return; this.ended = true; this.lifetime.abort(new PathChanged()); clearInterval(this.tick);
    for (const [key, part] of this.partial) this.drop(key, part);
    for (const raw of this.waiting.splice(0)) this.options.budget.free(this.options.peer, 1, raw.byteLength, 1);
    this.probes.clear(); this.pc.close(); this.options.closed(); this.releaseReservation();
  }
  private releaseReservation() {
    if (this.ended && !this.writers && !this.reservationReleased) { this.reservationReleased = true; this.options.budget.release(this.options.peer, this.options.use); }
  }
}
