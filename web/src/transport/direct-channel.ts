import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { DirectCandFamily, DirectCandType, DirectStatsPairSchema, DirectStatsReportSchema, DirectStateKind, type DirectStatsPair, type DirectStatsReport } from "../gen/remoteagent/core/v1/transport_pb";
import { FragmentSchema } from "../gen/remoteagent/v1/transport_pb";
import { cleanup } from "../cleanup";
import { PathChanged, abortable } from "./recovery";

const FRAME_BYTES = 16 << 10;
const PAYLOAD_BYTES = FRAME_BYTES - 128;
const textEnc = new TextEncoder();
const textDec = new TextDecoder("utf-8", { fatal: true });

function hexBytes(b: Uint8Array): string {
  return Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
}

function equalBytes(a: Uint8Array, b: Uint8Array): boolean {
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) if (a[i] !== b[i]) return false;
  return true;
}

async function sha256(b: Uint8Array<ArrayBuffer>): Promise<Uint8Array<ArrayBuffer>> {
  return new Uint8Array(await crypto.subtle.digest("SHA-256", b));
}

/** Minimal platform sanity check before calling WebRTC SDP APIs; semantic validation lives in shared/peerwire. */
function validateMinimalSDP(bytes: Uint8Array<ArrayBuffer>): string {
  if (bytes.byteLength === 0 || bytes.byteLength > 60 * 1024) throw new PathChanged();
  if (bytes[0] === 0xef && bytes[1] === 0xbb && bytes[2] === 0xbf) throw new PathChanged();
  const raw = textDec.decode(bytes);
  if (!raw.endsWith("\r\n") || raw.includes("\0") || /[\v\f\u0085\u2028\u2029]/.test(raw) || /(^|[^\r])\n|\r(?!\n)/.test(raw)) {
    throw new PathChanged();
  }
  if (!raw.includes("m=application ") || !raw.includes("a=fingerprint:sha-256 ")) {
    throw new PathChanged();
  }
  return raw;
}

function statsCandType(value: unknown): DirectCandType {
  switch (value) {
    case "host": return DirectCandType.HOST;
    case "srflx": return DirectCandType.SRFLX;
    case "prflx": return DirectCandType.PRFLX;
    case "relay": return DirectCandType.RELAY;
    default: return typeof value === "string" ? DirectCandType.OTHER : DirectCandType.UNSPECIFIED;
  }
}

function statsCandFamily(address: unknown): DirectCandFamily {
  if (typeof address !== "string" || !address) return DirectCandFamily.UNSPECIFIED;
  const a = address.trim().toLowerCase().replace(/%.*$/, "");
  if (a.endsWith(".local")) return DirectCandFamily.UNSPECIFIED;
  if (/^\d{1,3}(?:\.\d{1,3}){3}$/.test(a) || /^::ffff:\d{1,3}(?:\.\d{1,3}){3}$/.test(a)) {
    return DirectCandFamily.V4;
  }
  return a.includes(":") ? DirectCandFamily.V6 : DirectCandFamily.UNSPECIFIED;
}

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
    current.uses.add(use);
    return true;
  }
  ownerAvailable(peer: string, owner: string) { return !this.peers.has(peer) || this.peers.get(peer)!.owner === owner; }
  release(peer: string, use: number) {
    const current = this.peers.get(peer);
    if (!current) return;
    current.uses.delete(use);
    this.prune(peer, current);
  }
  reserve(peer: string, direction: 0 | 1, bytes: number, frames: number) {
    const current = this.peers.get(peer);
    if (!current || bytes < 0 || frames < 0 || current.bytes[direction] + bytes > 2 << 20 || current.frames[direction] + frames > 128 || this.bytes[direction] + bytes > 16 << 20) return false;
    current.bytes[direction] += bytes;
    current.frames[direction] += frames;
    this.bytes[direction] += bytes;
    return true;
  }
  free(peer: string, direction: 0 | 1, bytes: number, frames: number) {
    const current = this.peers.get(peer);
    if (!current) return;
    current.bytes[direction] -= bytes;
    current.frames[direction] -= frames;
    this.bytes[direction] -= bytes;
    this.prune(peer, current);
  }
  private prune(peer: string, current: Usage) {
    if (!current.uses.size && !current.bytes[0] && !current.bytes[1] && !current.frames[0] && !current.frames[1]) this.peers.delete(peer);
  }
}

type PartialMessage = { count: number; length: number; hash: Uint8Array<ArrayBuffer>; parts: Map<number, Uint8Array<ArrayBuffer>>; bytes: number; until: number };

export interface ChannelOptions {
  peer: string;
  roundId: bigint;
  use: number;
  generation: bigint;
  budget: DirectBudget;
  valid: () => boolean;
  stun: string[];
  candidate: (candidate: RTCIceCandidateInit | null) => void;
  state: (kind: DirectStateKind, value: string) => void;
  dcState?: (open: boolean) => void;
  stunError?: (code: number, address: string) => void;
  frame: (raw: Uint8Array<ArrayBuffer>) => void;
  closed: () => void;
  open?: () => void;
  probe?: (milliseconds: number) => void;
  factory?: (config: RTCConfiguration) => RTCPeerConnection;
}

/** Browser-native WebRTC peer connection & DataChannel framing adapter. */
export class DirectChannel {
  private readonly pc: RTCPeerConnection;
  private readonly data: RTCDataChannel;
  private readonly probe: RTCDataChannel;
  private readonly owner: string;
  private readonly lifetime = new AbortController();
  private confirmed = false;
  private stunReady = false;
  private ended = false;
  private chain = Promise.resolve();
  private partial = new Map<string, PartialMessage>();
  private waiting: Uint8Array<ArrayBuffer>[] = [];
  private probes = new Map<string, number>();
  private tick: ReturnType<typeof setInterval>;
  private lastProbe = 0;
  private missedProbes = 0;
  private writers = 0;
  private reservationReleased = false;
  private listeners = new Set<() => void>();

  constructor(private readonly options: ChannelOptions) {
    this.owner = String(options.roundId);
    if (
      !options.valid() ||
      options.stun.length > 2 ||
      options.stun.some((url) => !url.startsWith("stun:") || (url.includes("?") && !url.endsWith("?transport=udp"))) ||
      !options.budget.acquire(options.peer, this.owner, options.use)
    ) {
      throw new PathChanged();
    }
    let created: RTCPeerConnection | undefined;
    try {
      this.pc = created = (options.factory ?? ((config) => new RTCPeerConnection(config)))({
        iceServers: options.stun.length ? [{ urls: options.stun }] : [],
      });
      this.data = this.pc.createDataChannel("remote-agent", { id: 0, negotiated: true, ordered: true });
      this.probe = this.pc.createDataChannel("remote-agent", { id: 1, negotiated: true, ordered: false, maxRetransmits: 0 });
    } catch {
      created?.close();
      options.budget.release(options.peer, options.use);
      throw new PathChanged();
    }
    this.data.binaryType = this.probe.binaryType = "arraybuffer";
    this.data.bufferedAmountLowThreshold = 32 << 10;
    const wake = () => { for (const listener of [...this.listeners]) listener(); };
    this.data.onbufferedamountlow = wake;
    this.data.onopen = () => {
      if (this.ended) return;
      options.state(DirectStateKind.DC, "open");
      options.dcState?.(true);
      wake();
      options.open?.();
    };
    this.data.onclose = () => {
      options.state(DirectStateKind.DC, "closed");
      options.dcState?.(false);
      this.close();
    };
    this.probe.onclose = () => this.close();
    this.data.onerror = this.probe.onerror = () => this.close();
    this.pc.ondatachannel = (event) => { event.channel.close(); this.close(); };
    this.pc.onicecandidate = (event) => {
      if (event.candidate?.type === "srflx") this.stunReady = true;
      if (this.valid()) options.candidate(event.candidate?.toJSON() ?? null);
    };
    this.pc.onconnectionstatechange = () => {
      if (this.ended) return;
      options.state(DirectStateKind.CONN, this.pc.connectionState);
      if (this.pc.sctp?.state) options.state(DirectStateKind.SCTP, this.pc.sctp.state);
      if (this.pc.sctp?.transport?.state) options.state(DirectStateKind.DTLS, this.pc.sctp.transport.state);
      if (["failed", "closed", "disconnected"].includes(this.pc.connectionState)) this.close();
    };
    this.pc.onicegatheringstatechange = () => {
      if (!this.ended) options.state(DirectStateKind.GATHER, this.pc.iceGatheringState);
    };
    this.pc.oniceconnectionstatechange = () => {
      if (!this.ended) options.state(DirectStateKind.ICE, this.pc.iceConnectionState);
    };
    // Only forward the numeric error code and raw address for Core-side family classification; Core drops addresses.
    this.pc.onicecandidateerror = (event) => {
      if (this.ended) return;
      const e = event as Partial<RTCPeerConnectionIceErrorEvent>;
      options.stunError?.(typeof e.errorCode === "number" ? e.errorCode : 0, typeof e.address === "string" ? e.address : "");
    };
    this.data.onmessage = (event) => {
      if (!this.valid()) return;
      if (!(event.data instanceof ArrayBuffer) || event.data.byteLength > FRAME_BYTES) {
        this.close();
        return;
      }
      void this.receive(new Uint8Array(event.data)).catch(() => this.close());
    };
    this.probe.onmessage = (event) => {
      if (!this.ready || !(event.data instanceof ArrayBuffer) || event.data.byteLength !== 17) return;
      const raw = new Uint8Array(event.data);
      if (raw[0] === 0) {
        raw[0] = 1;
        this.probe.send(raw);
      } else if (raw[0] === 1) {
        const id = hexBytes(raw.subarray(1));
        const start = this.probes.get(id);
        if (start !== undefined) {
          this.probes.delete(id);
          this.missedProbes = 0;
          options.probe?.(performance.now() - start);
        }
      }
    };
    this.tick = setInterval(() => {
      if (!this.valid()) {
        this.close();
        return;
      }
      const now = performance.now();
      for (const [key, part] of this.partial) if (now >= part.until) this.drop(key, part);
      for (const [id, start] of this.probes) {
        if (now - start >= 1000) {
          this.probes.delete(id);
          if (++this.missedProbes >= 3) {
            this.close();
            return;
          }
        }
      }
      if (this.ready && this.probe.readyState === "open" && now - this.lastProbe >= 1000) {
        this.lastProbe = now;
        const raw = crypto.getRandomValues(new Uint8Array(17));
        raw[0] = 0;
        this.probes.set(hexBytes(raw.subarray(1)), now);
        try {
          this.probe.send(raw);
        } catch {
          this.close();
        }
      }
    }, 100);
  }

  private valid() { return !this.ended && this.options.valid(); }
  get ready() { return this.confirmed && this.valid() && this.data.readyState === "open"; }
  get traversalReady() { return this.stunReady; }
  get queuedBytes() { return this.data.bufferedAmount; }
  get signal() { return this.lifetime.signal; }

  async description(offer: boolean, restart = false): Promise<Uint8Array<ArrayBuffer>> {
    if (!this.valid()) throw new PathChanged();
    const description = offer ? await this.pc.createOffer({ iceRestart: restart }) : await this.pc.createAnswer();
    if (!this.valid() || !description.sdp) throw new PathChanged();
    const raw = textEnc.encode(description.sdp) as Uint8Array<ArrayBuffer>;
    validateMinimalSDP(raw);
    await this.pc.setLocalDescription(description);
    if (!this.valid()) throw new PathChanged();
    return raw;
  }

  async remote(raw: Uint8Array<ArrayBuffer>, offer: boolean) {
    if (!this.valid()) throw new PathChanged();
    const sdp = validateMinimalSDP(raw);
    await this.pc.setRemoteDescription({ type: offer ? "offer" : "answer", sdp });
    if (!this.valid()) throw new PathChanged();
  }

  async addCandidate(candidate?: RTCIceCandidateInit | null) {
    if (!this.valid()) throw new PathChanged();
    await this.pc.addIceCandidate(candidate);
    if (!this.valid()) throw new PathChanged();
  }

  confirm() {
    if (!this.valid()) return;
    this.confirmed = true;
    for (const raw of this.waiting.splice(0)) this.deliver(raw);
  }

  async readStats(): Promise<DirectStatsReport> {
    let iceState = "";
    let dtlsState = "";
    let dcState = "";
    try {
      iceState = this.pc.iceConnectionState ?? "";
      dtlsState = this.pc.sctp?.transport?.state ?? "";
      dcState = this.data.readyState ?? "";
    } catch { /* Best-effort snapshot. */ }
    const pairs: DirectStatsPair[] = [];
    if (typeof this.pc.getStats === "function") {
      try {
        const stats = await this.pc.getStats();
        const locals = new Map<string, { type: DirectCandType; family: DirectCandFamily }>();
        const remotes = new Map<string, { type: DirectCandType; family: DirectCandFamily }>();
        const rawPairs: Array<Record<string, unknown>> = [];
        stats.forEach((s: Record<string, unknown>) => {
          switch (s.type) {
            case "local-candidate":
            case "remote-candidate": {
              const address = typeof s.address === "string" ? s.address : typeof s.ip === "string" ? s.ip : "";
              const info = { type: statsCandType(s.candidateType), family: statsCandFamily(address) };
              (s.type === "local-candidate" ? locals : remotes).set(String(s.id), info);
              break;
            }
            case "candidate-pair":
              rawPairs.push(s);
              break;
            case "transport":
              if (typeof s.dtlsState === "string" && s.dtlsState) dtlsState = s.dtlsState;
              break;
            case "data-channel":
              if ((s.dataChannelIdentifier === undefined || s.dataChannelIdentifier === 0) && typeof s.state === "string") {
                dcState = s.state;
              }
              break;
          }
        });
        for (const p of rawPairs) {
          const l = locals.get(String(p.localCandidateId)) ?? { type: DirectCandType.UNSPECIFIED, family: DirectCandFamily.UNSPECIFIED };
          const r = remotes.get(String(p.remoteCandidateId)) ?? { type: DirectCandType.UNSPECIFIED, family: DirectCandFamily.UNSPECIFIED };
          const hasRtt = typeof p.currentRoundTripTime === "number" && Number.isFinite(p.currentRoundTripTime);
          pairs.push(create(DirectStatsPairSchema, {
            localType: l.type,
            localFamily: l.family,
            remoteType: r.type,
            remoteFamily: r.family,
            nominated: p.nominated === true,
            state: typeof p.state === "string" ? p.state : "",
            requestsSent: typeof p.requestsSent === "number" && Number.isFinite(p.requestsSent) ? Math.floor(p.requestsSent) : 0,
            responsesReceived: typeof p.responsesReceived === "number" && Number.isFinite(p.responsesReceived) ? Math.floor(p.responsesReceived) : 0,
            currentRttSeconds: hasRtt ? (p.currentRoundTripTime as number) : 0,
            hasCurrentRtt: hasRtt,
          }));
        }
      } catch { /* Diagnostics stats are optional. */ }
    }
    return create(DirectStatsReportSchema, { iceState, dtlsState, dcState, pairs });
  }

  write(raw: Uint8Array<ArrayBuffer>, handoff: () => void, signal?: AbortSignal): Promise<void> {
    const count = Math.ceil(raw.byteLength / PAYLOAD_BYTES);
    if (!this.ready || !raw.byteLength || raw.byteLength > 1 << 20 || !this.options.budget.reserve(this.options.peer, 0, raw.byteLength, count)) {
      return Promise.reject(new PathChanged());
    }
    const frozen = raw.slice();
    this.writers++;
    const job = this.chain
      .then(() => this.writeNow(frozen, handoff, signal))
      .finally(() => {
        this.options.budget.free(this.options.peer, 0, frozen.byteLength, count);
        this.writers--;
        this.releaseReservation();
      });
    this.chain = job.catch(() => undefined);
    return abortable(job, signal);
  }

  private async writeNow(raw: Uint8Array<ArrayBuffer>, handoff: () => void, signal?: AbortSignal) {
    const hash = await sha256(raw);
    const id = crypto.getRandomValues(new Uint8Array(16));
    const count = Math.ceil(raw.byteLength / PAYLOAD_BYTES);
    for (let index = 0; index < count; index++) {
      while (this.valid() && (!this.ready || this.data.bufferedAmount > 64 << 10)) {
        await new Promise<void>((resolve, reject) => {
          const wake = () => { clean(); resolve(); };
          const abort = () => { clean(); reject(new PathChanged()); };
          const clean = () => {
            this.listeners.delete(wake);
            this.lifetime.signal.removeEventListener("abort", abort);
            signal?.removeEventListener("abort", abort);
          };
          this.listeners.add(wake);
          this.lifetime.signal.addEventListener("abort", abort, { once: true });
          signal?.addEventListener("abort", abort, { once: true });
          if (signal?.aborted || this.ended) abort();
        });
      }
      signal?.throwIfAborted();
      if (!this.ready) throw new PathChanged();
      const frame = toBinary(FragmentSchema, create(FragmentSchema, {
        messageId: id,
        index,
        count,
        ciphertextLength: raw.byteLength,
        ciphertextSha256: hash,
        pathGeneration: this.options.generation,
        fragment: raw.subarray(index * PAYLOAD_BYTES, Math.min(raw.byteLength, (index + 1) * PAYLOAD_BYTES)),
      }));
      const maximum = this.pc.sctp?.maxMessageSize ?? FRAME_BYTES;
      if (frame.byteLength > FRAME_BYTES || (maximum && frame.byteLength > maximum)) throw new PathChanged();
      if (index === 0) handoff();
      this.data.send(frame as Uint8Array<ArrayBuffer>);
    }
  }

  private async receive(raw: Uint8Array<ArrayBuffer>) {
    if (!this.valid()) return;
    const frame = fromBinary(FragmentSchema, raw);
    if (
      frame.messageId.byteLength !== 16 ||
      frame.ciphertextSha256.byteLength !== 32 ||
      frame.pathGeneration !== this.options.generation ||
      !frame.count ||
      frame.count > 128 ||
      frame.index >= frame.count ||
      !frame.ciphertextLength ||
      frame.ciphertextLength > 1 << 20 ||
      !frame.fragment.byteLength
    ) {
      return;
    }
    const key = hexBytes(frame.messageId);
    let part = this.partial.get(key);
    if (!part) {
      part = {
        count: frame.count,
        length: frame.ciphertextLength,
        hash: new Uint8Array(frame.ciphertextSha256),
        parts: new Map(),
        bytes: 0,
        until: performance.now() + 5000,
      };
    }
    if (part.count !== frame.count || part.length !== frame.ciphertextLength || !equalBytes(part.hash, frame.ciphertextSha256)) return;
    const old = part.parts.get(frame.index);
    if (old) {
      if (!equalBytes(old, frame.fragment)) this.close();
      return;
    }
    if (part.bytes + frame.fragment.byteLength > part.length || !this.options.budget.reserve(this.options.peer, 1, frame.fragment.byteLength, 1)) return;
    part.parts.set(frame.index, new Uint8Array(frame.fragment));
    part.bytes += frame.fragment.byteLength;
    this.partial.set(key, part);
    if (part.parts.size !== part.count) return;
    if (part.bytes !== part.length || !this.options.budget.reserve(this.options.peer, 1, part.bytes, 1)) {
      this.drop(key, part);
      return;
    }
    const completed = new Uint8Array(part.length);
    let offset = 0;
    for (let index = 0; index < part.count; index++) {
      const chunk = part.parts.get(index)!;
      completed.set(chunk, offset);
      offset += chunk.byteLength;
    }
    const hash = part.hash;
    this.drop(key, part);
    let valid: boolean;
    try {
      valid = equalBytes(hash, await sha256(completed));
    } catch (error) {
      this.options.budget.free(this.options.peer, 1, completed.byteLength, 1);
      throw error;
    }
    if (!valid || !this.valid()) {
      this.options.budget.free(this.options.peer, 1, completed.byteLength, 1);
      return;
    }
    if (this.confirmed) this.deliver(completed);
    else this.waiting.push(completed);
  }

  private deliver(raw: Uint8Array<ArrayBuffer>) {
    this.options.budget.free(this.options.peer, 1, raw.byteLength, 1);
    if (this.valid() && this.confirmed) this.options.frame(raw);
  }

  private drop(key: string, part: PartialMessage) {
    this.partial.delete(key);
    this.options.budget.free(this.options.peer, 1, part.bytes, part.parts.size);
  }

  close() {
    if (this.ended) return;
    this.ended = true;
    this.lifetime.abort(new PathChanged());
    clearInterval(this.tick);
    for (const [key, part] of this.partial) this.drop(key, part);
    for (const raw of this.waiting.splice(0)) this.options.budget.free(this.options.peer, 1, raw.byteLength, 1);
    this.probes.clear();
    cleanup(() => this.pc.close(), () => this.options.closed(), () => this.releaseReservation());
  }

  private releaseReservation() {
    if (this.ended && !this.writers && !this.reservationReleased) {
      this.reservationReleased = true;
      this.options.budget.release(this.options.peer, this.options.use);
    }
  }
}
