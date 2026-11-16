// What this browser can tell about why a computer's direct path did not come
// up (docs/transport.md §6, §7), kept per computer for its last three rounds.
// Browser side only: nothing here is sent anywhere or changes a message.
//
// Privacy: an address is looked at once, to learn its type, family and range
// (IPv6 global / unique-local / link-local; IPv4 private / public / carrier
// shared), and then dropped. No address, port, ICE credential, SDP or SDK error
// text is ever stored. Everything kept is a small count, a state word or a
// time since the round began, in tables of bounded size.
//
// Cost: the counters are updated by events the page already receives. The only
// extra work is `RTCPeerConnection.getStats()`, asked at most `MAX_SAMPLES`
// times per attempt (at the state changes that decide it, shortly before the
// round's deadline, and when the diagnostics sheet is open and asks).

export type Family = "4" | "6" | "x";
export type CandType = "host" | "srflx" | "prflx" | "relay" | "other";
/** v4: private, public, cgnat, link_local, loopback. v6: global, ula, link_local, loopback. x: mdns (a host name instead of an address), other. */
export type AddrClass = "private" | "public" | "cgnat" | "link_local" | "loopback" | "global" | "ula" | "mdns" | "other";
export interface CandKind { type: CandType; family: Family; cls: AddrClass }
/** Candidates seen, counted by `type|family|class`. */
export type Tally = Record<string, number>;

export type StateKind = "gather" | "ice" | "conn" | "dtls" | "sctp" | "dc";
export interface StateEvent { t: number; k: StateKind; v: string }
export interface PairGroup { lt: CandType; lf: Family; rt: CandType; rf: Family; ok: number; failed: number; waiting: number; sent: number; got: number; rtt?: number; nominated: boolean }
export interface Rejection { k: string; n: number }
export interface StunError { code: number; family: Family; n: number }

export type Stage = "handshake" | "sdp" | "ice" | "dtls" | "channel" | "confirm" | "ready";
const STAGES: Stage[] = ["handshake", "sdp", "ice", "dtls", "channel", "confirm", "ready"];
export const stageRank = (s: Stage) => STAGES.indexOf(s);

export const MAX_ROUNDS = 3;
export const MAX_ATTEMPTS = 3;
export const MAX_SAMPLES = 6;
const MAX_STATES = 40, MAX_KEYS = 24, MAX_PAIRS = 12, MAX_ERRORS = 8, MAX_REJECTS = 8;

export interface AttemptData {
  /** Milliseconds after the round began that this peer connection was made. */
  at: number;
  local: Tally; remote: Tally;
  stun: StunError[];
  states: StateEvent[];
  pairs: PairGroup[];
  /** Times `getStats` was asked, and whether any answer has been read. */
  samples: number; sampled: boolean;
}
export interface Marks { boot?: number; sdpLocal?: number; sdpRemote?: number; peerConfirm?: number; ready?: number }
export interface PcData {
  use: 1 | 2;
  marks: Marks;
  attempts: AttemptData[];
  /** Signals from the computer this side refused, by `kind:reason`. */
  rejects: Rejection[];
  /** The browser could not make the connection at all. */
  blocked: boolean;
}
export type EndReason = "timeout" | "no_response" | "no_traversal" | "peer_unsupported" | "retired" | "cancelled" | "lost";
export interface RoundData {
  n: number;
  /** Wall clock, only to say how long ago. */
  startedAt: number;
  /** Milliseconds after the start. */
  established?: number;
  endedAt?: number;
  end?: EndReason;
  signals: { unsupported: boolean; limited: number; offline: number };
  /** The round also asked the public STUN server, not only the relay's own. */
  publicStun: boolean;
  pcs: [PcData, PcData];
}

// ---- masking ---------------------------------------------------------------

/** Family and range of an address; the address itself goes no further. */
export function classifyAddress(address: string): { family: Family; cls: AddrClass } {
  const a = address.trim().toLowerCase().replace(/%.*$/, "");
  if (a.endsWith(".local")) return { family: "x", cls: "mdns" };
  const v4 = (text: string) => {
    const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(text);
    if (!m) return undefined;
    const [x, y] = [Number(m[1]), Number(m[2])];
    if (Number(m[3]) > 255 || Number(m[4]) > 255 || x > 255 || y > 255) return undefined;
    const cls: AddrClass = x === 10 || x === 172 && y >= 16 && y <= 31 || x === 192 && y === 168 ? "private"
      : x === 100 && y >= 64 && y <= 127 ? "cgnat" : x === 127 ? "loopback" : x === 169 && y === 254 ? "link_local" : "public";
    return { family: "4" as const, cls };
  };
  const plain = v4(a);
  if (plain) return plain;
  if (!a.includes(":")) return { family: "x", cls: "other" };
  const mapped = /^::ffff:(\d+\.\d+\.\d+\.\d+)$/.exec(a);
  if (mapped) return v4(mapped[1]) ?? { family: "x", cls: "other" };
  if (a === "::1") return { family: "6", cls: "loopback" };
  const head = a.startsWith("::") ? 0 : parseInt(a.split(":")[0], 16);
  if (Number.isNaN(head)) return { family: "x", cls: "other" };
  const cls: AddrClass = head >= 0xfe80 && head <= 0xfebf ? "link_local" : head >= 0xfc00 && head <= 0xfdff ? "ula" : head >= 0x2000 && head <= 0x3fff ? "global" : "other";
  return { family: "6", cls };
}

const TYPES = new Set(["host", "srflx", "prflx", "relay"]);
const typeOf = (value: unknown): CandType => (typeof value === "string" && TYPES.has(value) ? (value as CandType) : "other");

/** The kind of an ICE candidate line (`candidate:… 4 addr port typ type …`). */
export function classifyCandidate(line: unknown): CandKind | undefined {
  if (typeof line !== "string") return undefined;
  const tokens = line.replace(/^a=/, "").trim().split(/ +/);
  if (!tokens[0]?.startsWith("candidate:") || tokens.length < 8) return undefined;
  const at = tokens.indexOf("typ");
  if (at < 0) return undefined;
  return { type: typeOf(tokens[at + 1]), ...classifyAddress(tokens[4]) };
}

export const kindKey = (k: CandKind) => `${k.type}|${k.family}|${k.cls}`;
export function parseKey(key: string): CandKind { const [type, family, cls] = key.split("|"); return { type: type as CandType, family: family as Family, cls: cls as AddrClass }; }
function add(t: Tally, k: CandKind) {
  let key = kindKey(k);
  if (!(key in t) && Object.keys(t).length >= MAX_KEYS) key = "other|x|other";
  t[key] = (t[key] ?? 0) + 1;
}
/** Candidates in the tally matching a test. */
export function tally(t: Tally, match: (k: CandKind) => boolean = () => true): number {
  let n = 0;
  for (const [key, count] of Object.entries(t)) if (match(parseKey(key))) n += count;
  return n;
}

// ---- one peer connection ---------------------------------------------------

type Entry = Record<string, unknown>;
export interface StatsLike { forEach(cb: (value: Entry) => void): void }
const word = (v: unknown, max = 32) => (typeof v === "string" && /^[A-Za-z0-9_-]{1,32}$/.test(v) ? v.slice(0, max) : "other");
const int = (v: unknown) => (typeof v === "number" && Number.isFinite(v) && v >= 0 ? Math.floor(v) : 0);

/** Never lets a diagnostics fault reach the transport. */
function safe(work: () => void) { try { work(); } catch { /* Diagnostics are display only. */ } }

/** The record of one RTCPeerConnection of a PC; a restart makes a new one. */
export class AttemptDiag {
  private lastState: Partial<Record<StateKind, string>> = {};
  private sampling = false;
  constructor(readonly data: AttemptData, private readonly pc: PcDiag) {}
  private now() { return this.pc.since(); }
  local(line: unknown) { safe(() => { const k = classifyCandidate(line); if (k) add(this.data.local, k); }); }
  remote(line: unknown) { safe(() => { const k = classifyCandidate(line); if (k) add(this.data.remote, k); }); }
  /** A STUN/ICE server error: its numeric code and the family of the local interface only. */
  stunError(code: unknown, address: unknown) {
    safe(() => {
      const family = typeof address === "string" && address ? classifyAddress(address).family : "x";
      const c = int(code), existing = this.data.stun.find((e) => e.code === c && e.family === family);
      if (existing) existing.n++; else if (this.data.stun.length < MAX_ERRORS) this.data.stun.push({ code: c, family, n: 1 });
    });
  }
  /** A remote candidate the browser refused (kind "browser"); the reason is the error's name only. */
  reject(kind: string, error: unknown) { this.pc.reject(kind, error instanceof Error || typeof error === "object" && error ? (error as { name?: unknown }).name : undefined); }
  state(k: StateKind, v: unknown) {
    safe(() => {
      if (typeof v !== "string" || this.lastState[k] === v) return;
      this.lastState[k] = v;
      if (this.data.states.length < MAX_STATES) this.data.states.push({ t: this.now(), k, v: word(v) });
    });
  }
  /** Reads what the connection already knows, without asking the SDK to work. */
  settle(pc: RTCPeerConnection) {
    safe(() => {
      this.state("gather", pc.iceGatheringState); this.state("ice", pc.iceConnectionState); this.state("conn", pc.connectionState);
      this.state("sctp", pc.sctp?.state); this.state("dtls", pc.sctp?.transport?.state);
    });
  }
  /** One `getStats` at most at a time, a few per attempt. */
  sample(pc: RTCPeerConnection) {
    this.settle(pc);
    if (this.sampling || this.data.samples >= MAX_SAMPLES || typeof pc.getStats !== "function") return;
    this.sampling = true; this.data.samples++;
    let asked: Promise<StatsLike>;
    try { asked = pc.getStats() as unknown as Promise<StatsLike>; } catch { this.sampling = false; return; }
    void Promise.resolve(asked).then((report) => this.take(report), () => undefined).finally(() => { this.sampling = false; });
  }
  /** Reads a stats report: candidate pairs by kind, the DTLS and data channel states. Addresses are classified and dropped. */
  take(report: StatsLike) {
    safe(() => {
      const locals = new Map<string, CandKind>(), remotes = new Map<string, CandKind>(), pairs: Entry[] = [];
      report.forEach((s) => {
        switch (s.type) {
          case "local-candidate": case "remote-candidate": {
            const address = typeof s.address === "string" ? s.address : typeof s.ip === "string" ? s.ip : "";
            const known = address ? classifyAddress(address) : { family: "x" as Family, cls: "other" as AddrClass };
            (s.type === "local-candidate" ? locals : remotes).set(String(s.id), { type: typeOf(s.candidateType), ...known });
            break;
          }
          case "candidate-pair": pairs.push(s); break;
          case "transport": this.state("dtls", s.dtlsState); break;
          case "data-channel": if (s.dataChannelIdentifier === undefined || s.dataChannelIdentifier === 0) this.state("dc", s.state); break;
        }
      });
      const groups = new Map<string, PairGroup>();
      for (const p of pairs) {
        const l = locals.get(String(p.localCandidateId)) ?? { type: "other" as CandType, family: "x" as Family }, r = remotes.get(String(p.remoteCandidateId)) ?? { type: "other" as CandType, family: "x" as Family };
        const key = `${l.type}|${l.family}|${r.type}|${r.family}`;
        let g = groups.get(key);
        if (!g) { if (groups.size >= MAX_PAIRS) continue; g = { lt: l.type, lf: l.family, rt: r.type, rf: r.family, ok: 0, failed: 0, waiting: 0, sent: 0, got: 0, nominated: false }; groups.set(key, g); }
        if (p.state === "succeeded") { g.ok++; if (p.nominated === true) g.nominated = true; if (typeof p.currentRoundTripTime === "number") { const ms = Math.round(p.currentRoundTripTime * 1000); g.rtt = g.rtt === undefined ? ms : Math.min(g.rtt, ms); } }
        else if (p.state === "failed") g.failed++; else g.waiting++;
        g.sent += int(p.requestsSent); g.got += int(p.responsesReceived);
      }
      this.data.pairs = [...groups.values()]; this.data.sampled = true;
    });
  }
}

/** The record of one PC (1 = loading, 2 = files) over one round. */
export class PcDiag {
  constructor(readonly data: PcData, private readonly round: RoundDiag) {}
  since() { return this.round.since(); }
  begin(): AttemptDiag {
    const data: AttemptData = { at: this.since(), local: {}, remote: {}, stun: [], states: [], pairs: [], samples: 0, sampled: false };
    this.data.attempts.push(data);
    if (this.data.attempts.length > MAX_ATTEMPTS) this.data.attempts.shift();
    return new AttemptDiag(data, this);
  }
  /** Records, once each, the first moment these were true. */
  observe(now: Partial<Record<keyof Marks, boolean>>) {
    for (const [name, on] of Object.entries(now) as [keyof Marks, boolean][]) if (on && this.data.marks[name] === undefined) this.data.marks[name] = this.since();
  }
  /** A signal the session refused, by kind of message and the validator's own short reason. */
  rejectSignal(type: number, error: unknown) {
    const kind = type === 3 || type === 4 ? "sdp" : type === 5 ? "candidates" : type === 6 ? "end" : type === 7 ? "confirm" : "signal";
    this.reject(kind, error instanceof Error ? error.message : undefined);
  }
  reject(kind: string, reason: unknown) {
    safe(() => {
      const k = `${kind}:${typeof reason === "string" && /^[A-Za-z ]{1,32}$/.test(reason) ? reason : "other"}`, found = this.data.rejects.find((r) => r.k === k);
      if (found) found.n++; else if (this.data.rejects.length < MAX_REJECTS) this.data.rejects.push({ k, n: 1 });
    });
  }
  /** The browser would not make the connection (the slots were taken, or it refused). */
  block() { this.data.blocked = true; }
}

// ---- one round -------------------------------------------------------------

export class RoundDiag {
  readonly data: RoundData;
  constructor(n: number, readonly clock: () => number = Date.now, publicStun = false) {
    const pc = (use: 1 | 2): PcData => ({ use, marks: {}, attempts: [], rejects: [], blocked: false });
    this.data = { n, startedAt: clock(), signals: { unsupported: false, limited: 0, offline: 0 }, publicStun, pcs: [pc(1), pc(2)] };
    this.pcs = [new PcDiag(this.data.pcs[0], this), new PcDiag(this.data.pcs[1], this)];
  }
  readonly pcs: [PcDiag, PcDiag];
  since() { return Math.max(0, this.clock() - this.data.startedAt); }
  pc(use: number): PcDiag { return this.pcs[use === 2 ? 1 : 0]; }
  note(kind: "unsupported" | "limited" | "offline") {
    if (kind === "unsupported") this.data.signals.unsupported = true; else this.data.signals[kind]++;
  }
  /** A path is ready: the round worked. */
  establish() { this.data.established ??= this.since(); }
  finish(reason: EndReason) { if (this.data.end) return; this.data.end = reason; this.data.endedAt = this.since(); }
}

/** The last rounds of each computer, newest first, and why a round was not even started. */
export class DirectDiag {
  private rings = new Map<string, { rounds: RoundDiag[]; blocked?: { at: number; n: number }; count: number }>();
  constructor(private readonly clock: () => number = Date.now) {}
  private ring(id: string) { let r = this.rings.get(id); if (!r) { r = { rounds: [], count: 0 }; this.rings.set(id, r); } return r; }
  begin(id: string, publicStun = false): RoundDiag {
    const ring = this.ring(id), round = new RoundDiag(++ring.count, this.clock, publicStun);
    ring.rounds.unshift(round);
    if (ring.rounds.length > MAX_ROUNDS) ring.rounds.length = MAX_ROUNDS;
    return round;
  }
  /** No round was made because the slots were taken; counted in place, so retries do not push real rounds out. */
  blocked(id: string) { const r = this.ring(id); r.blocked = { at: this.clock(), n: (r.blocked?.n ?? 0) + 1 }; }
  rounds(id: string): RoundData[] { return (this.rings.get(id)?.rounds ?? []).map((r) => r.data); }
  /** When a round was last refused for lack of slots, if that is newer than the newest round. */
  blockedAt(id: string): number | undefined {
    const r = this.rings.get(id);
    return r?.blocked && (!r.rounds.length || r.blocked.at > r.rounds[0].data.startedAt) ? r.blocked.at : undefined;
  }
  clear() { this.rings.clear(); }
}

// ---- reading a round ---------------------------------------------------------

/** A round that never connected while this browser showed no IPv6 address of its
 * own (desktop browsers hide host addresses behind mDNS names, and the relay's
 * STUN answers over IPv4 only) and the computer offered a global IPv6 one. The
 * computer then never sends towards the browser's IPv6, and a stateful IPv6
 * firewall in front of it drops the browser's checks: the case a dual-stack
 * public STUN server can fix by telling the browser its public IPv6 address. */
export function hiddenIPv6(r: RoundData): boolean {
  if (r.established !== undefined) return false;
  const attempts = r.pcs.flatMap((pc) => pc.attempts);
  const local = (m: (k: CandKind) => boolean) => attempts.reduce((n, a) => n + tally(a.local, m), 0);
  const remote = attempts.reduce((n, a) => n + tally(a.remote, (k) => k.family === "6" && k.cls === "global"), 0);
  return local(() => true) > 0 && local((k) => k.family === "6") === 0 && remote > 0;
}

const seen = (a: AttemptData | undefined, k: StateKind, ...values: string[]) => !!a?.states.some((s) => s.k === k && values.includes(s.v));

/** How far a PC got in an attempt: the first step that did not happen. */
export function stageOf(pc: PcData, a: AttemptData | undefined): Stage {
  if (pc.marks.ready !== undefined) return "ready";
  if (pc.marks.boot === undefined) return "handshake";
  if (pc.marks.sdpLocal === undefined || pc.marks.sdpRemote === undefined) return "sdp";
  const encrypted = seen(a, "conn", "connected") || seen(a, "dtls", "connected");
  if (!encrypted && !seen(a, "ice", "connected", "completed") && !a?.pairs.some((p) => p.ok > 0)) return "ice";
  if (!encrypted) return "dtls";
  if (!seen(a, "dc", "open")) return "channel";
  return pc.marks.peerConfirm === undefined ? "confirm" : "ready";
}

/** The attempt that got furthest, then the one that saw the most addresses and checks (a restart that has only just begun
 * knows less than the one it replaced); the latest of equals. */
export function bestAttempt(pc: PcData): AttemptData | undefined {
  let best: AttemptData | undefined, score = -1;
  for (const a of pc.attempts) {
    const s = stageRank(stageOf(pc, a)) * 1e6 + (tally(a.local) + tally(a.remote) + a.pairs.length) * 1e3 + a.states.length;
    if (s >= score) { best = a; score = s; }
  }
  return best;
}

/** The PC that got furthest (loading first when equal) with its best attempt and stage. */
export function leadPc(round: RoundData): { pc: PcData; attempt?: AttemptData; stage: Stage } {
  const view = (pc: PcData) => { const attempt = bestAttempt(pc); return { pc, attempt, stage: stageOf(pc, attempt) }; };
  const [a, b] = [view(round.pcs[0]), view(round.pcs[1])];
  return stageRank(b.stage) > stageRank(a.stage) ? b : a;
}

export type Verdict =
  | "none" | "trying" | "connected" | "lost" | "cancelled"
  | "no_reply" | "peer_refused" | "signal_limited" | "signal_offline"
  | "no_local" | "no_stun" | "no_remote" | "no_sdp" | "ipv6_silent" | "all_failed"
  | "dtls" | "channel" | "confirm" | "budget" | "create_failed" | "unknown";

const global6 = (k: CandKind) => k.family === "6" && k.cls === "global";

/** The one sentence a round comes down to, from what this side saw. `blocked`: a newer round was refused for lack of slots. */
export function verdictOf(round: RoundData | undefined, blocked = false): Verdict {
  if (blocked) return "budget";
  if (!round) return "none";
  if (round.established !== undefined) return round.end ? "lost" : "connected";
  if (!round.end) return "trying";
  if (round.end === "cancelled") return "cancelled";
  if (round.signals.unsupported || round.end === "peer_unsupported") return "peer_refused";
  const trouble: Verdict | undefined = round.signals.limited ? "signal_limited" : round.signals.offline ? "signal_offline" : undefined;
  const { attempt, stage } = leadPc(round);
  if (stage === "handshake") return trouble ?? "no_reply";
  if (round.pcs.some((p) => p.blocked) && !attempt) return "create_failed";
  if (stage === "sdp") return trouble ?? "no_sdp";
  if (!attempt || tally(attempt.local) === 0) return "no_local";
  if (round.end === "no_traversal" || tally(attempt.local, (k) => k.type === "srflx") === 0) return "no_stun";
  if (tally(attempt.remote) === 0) return trouble ?? "no_remote";
  if (stage === "ice") {
    // Both sides offered global IPv6, our checks over it were sent, and none was ever answered.
    const six = attempt.pairs.filter((p) => p.lf === "6" && p.rf === "6");
    if (tally(attempt.local, global6) > 0 && tally(attempt.remote, global6) > 0 && six.some((p) => p.sent > 0) && six.every((p) => p.got === 0 && p.ok === 0)) return "ipv6_silent";
    return "all_failed";
  }
  if (stage === "dtls") return "dtls";
  if (stage === "channel") return "channel";
  if (stage === "confirm") return "confirm";
  return "unknown";
}
