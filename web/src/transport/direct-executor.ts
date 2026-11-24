import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { EnvelopeSchema, HeaderSchema, PayloadKind, type Envelope, type Header } from "../gen/remoteagent/v1/envelope_pb";
import { PeerFrameSchema } from "../gen/remoteagent/v1/relay_pb";
import { EventSchema as CoreEventSchema, type Event as CoreEvent } from "../gen/remoteagent/core/v1/app_pb";
import {
  DirectDcStateEventSchema,
  DirectDcStatsEventSchema,
  DirectPcCandidatesAddedEventSchema,
  DirectPcClosedEventSchema,
  DirectPcCreatedEventSchema,
  DirectPcDescriptionEventSchema,
  DirectPcLocalCandidateEventSchema,
  DirectPcProbeReceivedEventSchema,
  DirectPcRemoteSetEventSchema,
  DirectPcStateEventSchema,
  DirectPcStunErrorEventSchema,
  type DirectDcReadStatsEffect,
  type DirectPcAddCandidatesEffect,
  type DirectPcCloseEffect,
  type DirectPcConfirmEffect,
  type DirectPcCreateEffect,
  type DirectPcDescribeEffect,
  type DirectPcSendProbeEffect,
  type DirectPcSetRemoteEffect,
  type DirectSignalSendEffect,
} from "../gen/remoteagent/core/v1/transport_pb";
import type { FrameStream } from "./path";
import { DirectBudget, DirectChannel } from "./direct-channel";
import { PathChanged, abortable } from "./recovery";

const utf8Dec = new TextDecoder("utf-8", { fatal: true });

/** Reads the bounded ServerFrame body without re-encoding signed peer bytes. */
export function peerFrameBody(raw: Uint8Array<ArrayBuffer>, field: 11 | 12): Uint8Array<ArrayBuffer> | undefined {
  if (raw.length > 65540 || raw[0] !== ((field << 3) | 2)) return;
  let length = 0;
  let shift = 0;
  let at = 1;
  for (; at < Math.min(raw.length, 5); at++, shift += 7) {
    const v = raw[at];
    length |= (v & 127) << shift;
    if (!(v & 128)) {
      if ((at > 1 && !v) || length > 65536 || length !== raw.length - at - 1) return;
      return raw.slice(at + 1);
    }
  }
}

interface PcEntry {
  channel: DirectChannel;
  machineId: string;
  use: number;
  confirmed: boolean;
  closedByCore: boolean;
}

interface PendingRpc {
  pcId: bigint;
  machineId: string;
  resolve: (env: Envelope) => void;
  reject: (err: Error) => void;
}

function errorName(err: unknown): string {
  if (err instanceof Error && err.name) return err.name;
  if (err && typeof err === "object" && "name" in err && typeof (err as { name: unknown }).name === "string") {
    return (err as { name: string }).name;
  }
  return "Error";
}

/**
 * Platform executor for Core's direct-path coordinator: owns WebRTC PCs
 * and main-WS signaling framing.
 */
export class DirectExecutor {
  readonly budget = new DirectBudget();
  private readonly pcs = new Map<bigint, PcEntry>();
  private readonly pendingRpc = new Map<string, PendingRpc>();
  private leg?: FrameStream;
  private closed = false;

  constructor(
    private readonly options: {
      emit: (ev: CoreEvent) => void;
      onPushedEvent: (machineId: string, pcId: bigint, env: Envelope, header: Header, size: number) => void;
      factory?: (config: RTCConfiguration) => RTCPeerConnection;
    },
  ) {}

  get isClosed() { return this.closed; }

  private emit(ev: CoreEvent["event"]) {
    if (!this.closed) this.options.emit(create(CoreEventSchema, { event: ev }));
  }

  attachLeg(leg: FrameStream) {
    this.leg = leg;
  }

  detachLeg(leg: FrameStream) {
    if (this.leg === leg) this.leg = undefined;
  }

  signalSend(eff: DirectSignalSendEffect): boolean {
    const leg = this.leg;
    if (this.closed || !leg?.ready) return false;
    const raw = toBinary(PeerFrameSchema, create(PeerFrameSchema, {
      kind: {
        case: "directedPeerFrame",
        value: {
          toDevice: eff.toDevice,
          frameClass: eff.frameClass,
          frameId: eff.frameId,
          sealed: eff.sealed,
        },
      },
    })) as Uint8Array<ArrayBuffer>;
    try {
      leg.send(raw);
      return true;
    } catch {
      return false;
    }
  }

  pcCreate(eff: DirectPcCreateEffect) {
    if (this.closed) {
      this.emit({ case: "directPcCreated", value: create(DirectPcCreatedEventSchema, { pcId: eff.pcId, ok: false }) });
      return;
    }
    const existing = this.pcs.get(eff.pcId);
    if (existing) {
      existing.closedByCore = true;
      this.pcs.delete(eff.pcId);
      existing.channel.close();
    }
    const entry: PcEntry = {
      channel: undefined as unknown as DirectChannel,
      machineId: eff.machineId,
      use: eff.use,
      confirmed: false,
      closedByCore: false,
    };
    this.pcs.set(eff.pcId, entry);
    try {
      entry.channel = new DirectChannel({
        peer: eff.machineId,
        roundId: eff.roundId,
        use: eff.use,
        generation: eff.generation,
        budget: this.budget,
        stun: eff.stunServers,
        factory: this.options.factory,
        valid: () => !this.closed && this.pcs.get(eff.pcId) === entry,
        candidate: (cand) => {
          if (!cand) {
            this.emit({
              case: "directPcLocalCandidate",
              value: create(DirectPcLocalCandidateEventSchema, { pcId: eff.pcId, endOfCandidates: true }),
            });
            return;
          }
          this.emit({
            case: "directPcLocalCandidate",
            value: create(DirectPcLocalCandidateEventSchema, {
              pcId: eff.pcId,
              candidate: cand.candidate ?? "",
            }),
          });
        },
        state: (kind, value) => {
          this.emit({ case: "directPcState", value: create(DirectPcStateEventSchema, { pcId: eff.pcId, kind, value }) });
        },
        dcState: (open) => {
          this.emit({ case: "directDcState", value: create(DirectDcStateEventSchema, { pcId: eff.pcId, open }) });
        },
        stunError: (code, address) => {
          this.emit({ case: "directPcStunError", value: create(DirectPcStunErrorEventSchema, { pcId: eff.pcId, code, address }) });
        },
        probeReply: (raw) => {
          this.emit({ case: "directPcProbeReceived", value: create(DirectPcProbeReceivedEventSchema, { pcId: eff.pcId, raw }) });
        },
        frame: (raw) => this.handleDcFrame(eff.pcId, entry, raw),
        closed: () => {
          const cur = this.pcs.get(eff.pcId);
          if (cur === entry) this.pcs.delete(eff.pcId);
          this.rejectPendingForPc(eff.pcId);
          if (!entry.closedByCore) {
            this.emit({ case: "directPcClosed", value: create(DirectPcClosedEventSchema, { pcId: eff.pcId }) });
          }
        },
      });
      this.pcs.set(eff.pcId, entry);
      this.emit({ case: "directPcCreated", value: create(DirectPcCreatedEventSchema, { pcId: eff.pcId, ok: true }) });
    } catch {
      this.pcs.delete(eff.pcId);
      this.emit({ case: "directPcCreated", value: create(DirectPcCreatedEventSchema, { pcId: eff.pcId, ok: false }) });
    }
  }

  async pcDescribe(eff: DirectPcDescribeEffect): Promise<void> {
    const entry = this.pcs.get(eff.pcId);
    if (!entry) {
      this.emit({ case: "directPcDescription", value: create(DirectPcDescriptionEventSchema, { pcId: eff.pcId, ok: false }) });
      return;
    }
    try {
      const sdp = await entry.channel.description(eff.offer, eff.iceRestart);
      this.emit({ case: "directPcDescription", value: create(DirectPcDescriptionEventSchema, { pcId: eff.pcId, sdp, ok: true }) });
    } catch {
      this.emit({ case: "directPcDescription", value: create(DirectPcDescriptionEventSchema, { pcId: eff.pcId, ok: false }) });
    }
  }

  async pcSetRemote(eff: DirectPcSetRemoteEffect): Promise<void> {
    const entry = this.pcs.get(eff.pcId);
    if (!entry) {
      this.emit({ case: "directPcRemoteSet", value: create(DirectPcRemoteSetEventSchema, { pcId: eff.pcId, ok: false, error: "closed" }) });
      return;
    }
    try {
      await entry.channel.remote(eff.sdp as Uint8Array<ArrayBuffer>, eff.offer);
      this.emit({ case: "directPcRemoteSet", value: create(DirectPcRemoteSetEventSchema, { pcId: eff.pcId, ok: true }) });
    } catch (err) {
      this.emit({ case: "directPcRemoteSet", value: create(DirectPcRemoteSetEventSchema, { pcId: eff.pcId, ok: false, error: errorName(err) }) });
    }
  }

  async pcAddCandidates(eff: DirectPcAddCandidatesEffect): Promise<void> {
    const entry = this.pcs.get(eff.pcId);
    if (!entry) {
      this.emit({ case: "directPcCandidatesAdded", value: create(DirectPcCandidatesAddedEventSchema, { pcId: eff.pcId, ok: false, error: "closed" }) });
      return;
    }
    try {
      for (const c of eff.candidates) {
        const line = utf8Dec.decode(c.line);
        const ufrag = c.ufrag.byteLength ? utf8Dec.decode(c.ufrag) : undefined;
        const mid = c.mid.byteLength ? utf8Dec.decode(c.mid) : undefined;
        await entry.channel.addCandidate({
          candidate: line,
          ...(ufrag ? { usernameFragment: ufrag } : {}),
          ...(mid !== undefined ? { sdpMid: mid } : {}),
          sdpMLineIndex: c.mlineIndex,
        });
      }
      if (eff.endOfCandidates) {
        await entry.channel.addCandidate(null);
      }
      this.emit({ case: "directPcCandidatesAdded", value: create(DirectPcCandidatesAddedEventSchema, { pcId: eff.pcId, ok: true }) });
    } catch (err) {
      this.emit({ case: "directPcCandidatesAdded", value: create(DirectPcCandidatesAddedEventSchema, { pcId: eff.pcId, ok: false, error: errorName(err) }) });
    }
  }

  pcConfirm(eff: DirectPcConfirmEffect) {
    const entry = this.pcs.get(eff.pcId);
    if (!entry || entry.machineId !== eff.machineId || entry.use !== eff.use) return;
    entry.confirmed = true;
    entry.channel.confirm();
  }

  pcClose(eff: DirectPcCloseEffect) {
    const entry = this.pcs.get(eff.pcId);
    if (!entry) return;
    entry.closedByCore = true;
    this.pcs.delete(eff.pcId);
    this.rejectPendingForPc(eff.pcId);
    entry.channel.close();
  }

  pcSendProbe(eff: DirectPcSendProbeEffect) {
    const entry = this.pcs.get(eff.pcId);
    if (!entry) return;
    entry.channel.sendProbe(eff.raw as Uint8Array<ArrayBuffer>);
  }

  async dcReadStats(eff: DirectDcReadStatsEffect): Promise<void> {
    const entry = this.pcs.get(eff.pcId);
    if (!entry) {
      this.emit({ case: "directDcStats", value: create(DirectDcStatsEventSchema, { pcId: eff.pcId }) });
      return;
    }
    const report = await entry.channel.readStats();
    this.emit({ case: "directDcStats", value: create(DirectDcStatsEventSchema, { pcId: eff.pcId, report }) });
  }

  async sendRpc(
    pcId: bigint,
    requestId: string,
    sealedBytes: Uint8Array<ArrayBuffer>,
    timeoutMs: bigint,
    handoff: () => void,
    signal?: AbortSignal,
  ): Promise<Envelope> {
    const entry = this.pcs.get(pcId);
    if (!entry || !entry.confirmed || !entry.channel.ready || this.pendingRpc.size >= 128) {
      throw new PathChanged();
    }
    const rpcKey = `${pcId}:${requestId}`;
    if (this.pendingRpc.has(rpcKey)) throw new PathChanged();

    const ms = timeoutMs > 0n ? Math.max(1, Math.min(30_000, Number(timeoutMs))) : 3000;
    const timeout = new AbortController();
    const timer = setTimeout(() => timeout.abort(new PathChanged()), ms);
    const canceled = AbortSignal.any([entry.channel.signal, timeout.signal, ...(signal ? [signal] : [])]);

    let resolve!: (env: Envelope) => void;
    let reject!: (err: Error) => void;
    const response = new Promise<Envelope>((res, rej) => {
      resolve = res;
      reject = rej;
    });
    const pending: PendingRpc = { pcId, machineId: entry.machineId, resolve, reject };
    this.pendingRpc.set(rpcKey, pending);

    try {
      await entry.channel.write(sealedBytes, handoff, canceled);
      return await abortable(response, canceled);
    } catch (e) {
      if (e instanceof Error && e.name === "AbortError" && !signal?.aborted) throw new PathChanged();
      throw e instanceof PathChanged ? e : new PathChanged();
    } finally {
      clearTimeout(timer);
      if (this.pendingRpc.get(rpcKey) === pending) this.pendingRpc.delete(rpcKey);
    }
  }

  private handleDcFrame(pcId: bigint, entry: PcEntry, raw: Uint8Array<ArrayBuffer>) {
    if (this.closed || this.pcs.get(pcId) !== entry) return;
    try {
      const env = fromBinary(EnvelopeSchema, raw);
      const header = fromBinary(HeaderSchema, env.header);
      if (entry.use === 1 && header.kind === PayloadKind.EVENT) {
        if (entry.confirmed && header.machineId === entry.machineId && header.sender === entry.machineId) {
          this.options.onPushedEvent(entry.machineId, pcId, env, header, raw.byteLength);
        }
        return;
      }
      const rpcKey = `${pcId}:${header.requestId}`;
      const pending = this.pendingRpc.get(rpcKey);
      if (pending && pending.machineId === entry.machineId && header.machineId === entry.machineId) {
        this.pendingRpc.delete(rpcKey);
        pending.resolve(env);
      }
    } catch {
      /* Remote authenticates every routed result. */
    }
  }

  private rejectPendingForPc(pcId: bigint) {
    for (const [k, pending] of this.pendingRpc) {
      if (pending.pcId === pcId) {
        this.pendingRpc.delete(k);
        pending.reject(new PathChanged());
      }
    }
  }

  close() {
    if (this.closed) return;
    this.closed = true;
    this.leg = undefined;
    for (const [pcId, entry] of [...this.pcs]) {
      entry.closedByCore = true;
      this.pcs.delete(pcId);
      entry.channel.close();
    }
    for (const [k, pending] of [...this.pendingRpc]) {
      this.pendingRpc.delete(k);
      pending.reject(new PathChanged());
    }
  }
}
