// Made-up direct-path states of the computers for ?demo (never compiled into a build).
//   direct=direct          Using direct, both purposes (no second line)
//   direct=file | read     Using direct, only for files / only for loading
//   direct=measuring       Direct connected, measuring speed (the first seconds; only the relay measured so far)
//   direct=relay           connected, Using relay, "Direct isn't clearly faster" (48 vs 160 would not qualify)
//   direct=relay-faster    connected, Using relay, "Relay is faster"
//   direct=trying          Setting up direct
//   direct=none            Using relay, no reason known
//   direct=peer_unsupported | no_response | no_traversal | failed   Using relay, "Direct unavailable: <reason>"
//   direct=off             the switch is off, no status row
//   direct=unsupported     this browser has no WebRTC
//   direct=mix             the MacBook direct, VPS-dev unreachable by the networks, the rest on the relay
//   speeds=both | direct | relay | none   what was measured, for every state that has a path
//   (without speeds the state's own sample is shown; a file nobody has transferred says
//   "Compared on the next transfer")
//   diag=ipv6|all|nolocal|nostun|noremote|dtls|channel|refused|noreply|limited|budget|connected|trying
//                          the 「诊断信息」 sheet of the 「连接」 group: one failure class per value, three recorded rounds
//                          where the class is a repeating one (ipv6, all). Built with the real collector from made-up
//                          candidates (documentation addresses). Without direct= the page's status follows the class.
//                          「重新尝试」 shows a new round in progress for three seconds, then the old list again.
// The switch works: turning it on shows Setting up direct for a moment, then measuring for two
// seconds (when the state it was opened with is connected), then that state.

import type { DirectEngine, DirectStatus } from "../transport/direct-engine";
import { RoundDiag, type PcDiag, type RoundData, type StatsLike } from "../transport/direct-diag";

type Scenario = Omit<DirectStatus, "fileMiBps" | "readMs"> & { off?: boolean };
const relay = { file: "relay", read: "relay" } as const;

function scenario(name: string): Scenario {
  switch (name) {
    case "direct": return { state: "connected", file: "direct", read: "direct" };
    case "file": return { state: "connected", file: "direct", read: "relay" };
    case "read": return { state: "connected", file: "relay", read: "direct" };
    case "file-fallback": return { state: "connected", file: "relay", read: "direct", fileSlow: true };
    case "relay": return { state: "connected", ...relay, readSlow: true, fileSlow: true };
    case "trying": return { state: "connecting", ...relay };
    case "peer_unsupported": case "no_response": case "no_traversal": case "failed": return { state: "none", reason: name, ...relay };
    case "off": return { state: "none", reason: "disabled", ...relay, off: true };
    case "unsupported": return { state: "unsupported", ...relay };
    default: return { state: "none", ...relay };
  }
}

function measured(name: string, speeds: string | null): Pick<DirectStatus, "fileMiBps" | "readMs"> {
  if (speeds === "none") return { fileMiBps: {}, readMs: {} };
  if (speeds === "both") return { fileMiBps: { direct: 6.3, relay: 4.1 }, readMs: { direct: 48, relay: 175 } };
  if (speeds === "direct") return { fileMiBps: { direct: 6.3 }, readMs: { direct: 48 } };
  if (speeds === "relay") return { fileMiBps: { relay: 4.1 }, readMs: { relay: 217 } };
  switch (name) {
    case "direct": return { fileMiBps: { direct: 6.3, relay: 4.1 }, readMs: { direct: 48, relay: 175 } };
    case "file": return { fileMiBps: { direct: 6.3, relay: 4.1 }, readMs: { relay: 175 } };
    case "read": return { fileMiBps: {}, readMs: { direct: 48, relay: 175 } };
    case "file-fallback": return { fileMiBps: { direct: 2.1, relay: 4.1 }, readMs: { direct: 48, relay: 175 } };
    case "relay": return { fileMiBps: { direct: 2.1, relay: 4.1 }, readMs: { direct: 320, relay: 175 } };
    default: return { fileMiBps: {}, readMs: {} };
  }
}

// ---- diag= -------------------------------------------------------------------

type Entry = Record<string, unknown>;
const cand = (ip: string, type: string, n = 1) => `candidate:${n} 1 udp 2113937151 ${ip} 5000 typ ${type}`;
const L6 = "2001:db8:1::5", L4 = "192.168.1.20", LS = "203.0.113.7", R6 = "2001:db8:2::9", R4 = "192.168.50.3", RS = "198.51.100.4";
const stats = (entries: Entry[]): StatsLike => ({ forEach: (cb) => entries.forEach(cb) });
const pair = (l: string, r: string, state: string, sent: number, got: number, rtt?: number): Entry => ({ type: "candidate-pair", localCandidateId: l, remoteCandidateId: r, state, requestsSent: sent, responsesReceived: got, nominated: state === "succeeded", currentRoundTripTime: rtt });
const cands: Entry[] = [
  { type: "local-candidate", id: "L6", candidateType: "host", address: L6 }, { type: "local-candidate", id: "L4", candidateType: "host", address: L4 }, { type: "local-candidate", id: "LS", candidateType: "srflx", address: LS },
  { type: "remote-candidate", id: "R6", candidateType: "host", address: R6 }, { type: "remote-candidate", id: "R4", candidateType: "host", address: R4 }, { type: "remote-candidate", id: "RS", candidateType: "srflx", address: RS },
];

interface Shape { local?: string[]; remote?: string[]; stun?: [number, string][]; checks?: Entry[]; ice?: [number, string][]; dtls?: string; dc?: string; marks?: string[] }
/** One path's attempt, played through the real collector with a clock the demo moves. */
function play(pc: PcDiag, clock: { t: number }, s: Shape) {
  const marks = new Set(s.marks ?? ["boot", "sdp"]);
  pc.observe({ boot: marks.has("boot"), sdpLocal: marks.has("sdp"), sdpRemote: marks.has("sdp"), peerConfirm: marks.has("confirm") });
  if (!marks.has("boot")) return;
  const a = pc.begin();
  clock.t += 20; a.state("gather", "gathering");
  for (const line of s.local ?? []) a.local(line);
  for (const [code, address] of s.stun ?? []) a.stunError(code, address);
  for (const line of s.remote ?? []) a.remote(line);
  clock.t += 350; a.state("gather", "complete");
  for (const [at, state] of s.ice ?? []) { clock.t += at; a.state("ice", state); }
  if (s.dtls) a.state("dtls", s.dtls);
  if (s.dc) a.state("dc", s.dc);
  if (s.checks) a.take(stats([...cands, ...s.checks]));
}
const v6all = [cand(L6, "host"), cand(L4, "host", 2), cand("abcd1234-0000.local", "host", 3), cand(LS, "srflx", 4)];
const v6checks = [pair("L6", "R6", "failed", 14, 0), pair("L4", "R4", "failed", 12, 0), pair("LS", "RS", "failed", 12, 0)];
const failedIce: [number, string][] = [[80, "checking"], [8400, "failed"]];

/** Rounds of a scenario, newest first; the clock ends at `now`. */
function rounds(name: string, now: number): { rounds: RoundData[]; blocked: boolean } {
  const make = (n: number, ageMs: number, lasted: number, shape: (r: RoundDiag, c: { t: number }) => void, end?: Parameters<RoundDiag["finish"]>[0]) => {
    const c = { t: now - ageMs }, r = new RoundDiag(n, () => c.t);
    shape(r, c); c.t = now - ageMs + lasted; if (end) r.finish(end);
    return r.data;
  };
  // Both paths run in the same stretch of time: the second starts where the first did.
  const both = (s: Shape, bulk: Shape = s) => (r: RoundDiag, c: { t: number }) => { const start = c.t; play(r.pc(1), c, s); const end = c.t; c.t = start; play(r.pc(2), c, bulk); c.t = Math.max(c.t, end); };
  const ipv6 = both({ local: v6all, remote: [cand(R6, "host"), cand(R4, "host", 2), cand(RS, "srflx", 3)], checks: v6checks, ice: failedIce });
  const silent = (n: number, age: number) => make(n, age, 9900, ipv6, "timeout");
  const none: [] = [];
  switch (name) {
    case "ipv6": return { rounds: [silent(5, 20_000), silent(4, 70_000), make(3, 190_000, 10_000, () => undefined, "no_response")], blocked: false };
    case "all": return { rounds: [1, 2, 3].map((i) => make(6 - i, i * 60_000, 9900, both({ local: [cand(L4, "host"), cand(LS, "srflx", 2)], remote: [cand(R4, "host"), cand(RS, "srflx", 2)], checks: [pair("L4", "R4", "failed", 12, 0), pair("LS", "RS", "failed", 12, 0)], ice: failedIce }), "timeout")), blocked: false };
    case "nolocal": return { rounds: [make(1, 30_000, 9900, both({ local: none, remote: [cand(R4, "host")], ice: none }), "timeout")], blocked: false };
    case "nostun": return { rounds: [make(1, 30_000, 9900, both({ local: [cand(L4, "host"), cand("abcd1234-0000.local", "host", 2)], stun: [[701, L4], [701, L4]], remote: [cand(R4, "host"), cand(RS, "srflx", 2)], ice: failedIce }), "no_traversal")], blocked: false };
    case "noremote": return { rounds: [make(1, 30_000, 9900, both({ local: v6all, remote: none, ice: none }), "timeout")], blocked: false };
    case "dtls": return { rounds: [make(1, 30_000, 9900, both({ local: v6all, remote: [cand(R4, "host"), cand(RS, "srflx", 2)], checks: [pair("L4", "R4", "succeeded", 6, 5, 0.048)], ice: [[60, "checking"], [90, "connected"]], dtls: "failed" }), "timeout")], blocked: false };
    case "channel": return { rounds: [make(1, 30_000, 9900, both({ local: v6all, remote: [cand(R4, "host"), cand(RS, "srflx", 2)], checks: [pair("L4", "R4", "succeeded", 6, 5, 0.048)], ice: [[60, "checking"], [90, "connected"]], dtls: "connected", dc: "connecting" }), "timeout")], blocked: false };
    case "refused": return { rounds: [make(1, 30_000, 1200, (r, c) => { play(r.pc(1), c, { marks: [] }); r.note("unsupported"); }, "peer_unsupported")], blocked: false };
    case "noreply": return { rounds: [make(1, 30_000, 10_000, () => undefined, "no_response")], blocked: false };
    case "limited": return { rounds: [make(1, 30_000, 10_000, (r) => { r.note("limited"); r.note("limited"); }, "timeout")], blocked: false };
    case "budget": return { rounds: [], blocked: true };
    case "connected": return { rounds: [make(1, 40_000, 3000, (r, c) => { play(r.pc(1), c, { local: v6all, remote: [cand(R4, "host"), cand(RS, "srflx", 2)], checks: [pair("L4", "R4", "succeeded", 6, 6, 0.012)], ice: [[40, "checking"], [60, "connected"]], dtls: "connected", dc: "open", marks: ["boot", "sdp", "confirm"] }); r.pc(1).observe({ ready: true }); r.establish(); })], blocked: false };
    case "trying": return { rounds: [make(1, 4000, 4000, (r, c) => play(r.pc(1), c, { local: [cand(L4, "host"), cand(LS, "srflx", 2)], remote: none, ice: none }))], blocked: false };
    default: return { rounds: [], blocked: false };
  }
}
const diagStatus = (name: string) => name === "connected" ? "direct" : name === "trying" ? "trying" : name === "noreply" ? "no_response" : name === "refused" ? "peer_unsupported" : name === "nostun" ? "no_traversal" : "failed";

export function seedDirect(engine: DirectEngine, opts: URLSearchParams) {
  const diagName = opts.get("diag");
  let againOf: Map<string, number> | undefined;
  const asked = opts.get("direct") ?? (diagName ? diagStatus(diagName) : null);
  if (!asked) return;
  if (diagName) {
    const again = new Map<string, number>();
    againOf = again;
    const notifyLater = () => (engine as unknown as { notify(): void }).notify();
    engine.diagnostics = (id) => {
      const base = rounds(diagName, Date.now()), at = again.get(id);
      if (at === undefined || Date.now() - at > 3000) return { rounds: base.rounds, blockedAt: base.blocked ? Date.now() : undefined };
      const fresh = rounds("trying", Date.now()).rounds[0];
      return { rounds: [{ ...fresh, n: (base.rounds[0]?.n ?? 0) + 1 }, ...base.rounds].slice(0, 3) };
    };
    engine.refreshDiagnostics = () => undefined;
    engine.retryNow = (id) => { again.set(id, Date.now()); notifyLater(); setTimeout(notifyLater, 3100); return true; };
  }
  const speeds = opts.get("speeds");
  const nameFor = (id: string) => (asked === "mix" ? (id === "mac" ? "direct" : id === "vps" ? "no_traversal" : "relay") : asked);
  const switched = new Map<string, boolean>();
  const since = new Map<string, number>();
  const notify = () => (engine as unknown as { notify(): void }).notify();
  engine.enabled = (id) => switched.get(id) ?? !scenario(nameFor(id)).off;
  engine.setEnabled = (id, on) => {
    switched.set(id, on);
    since.set(id, Date.now());
    notify();
    // Nothing reported for a moment, then trying, then the state the page was opened with.
    setTimeout(notify, 600);
    setTimeout(notify, 2600);
    setTimeout(notify, 4600);
  };
  engine.status = (id): DirectStatus => {
    const name = nameFor(id), base = scenario(name);
    const sample = measured(name, speeds);
    if (base.state === "unsupported") return { ...base, ...sample };
    if (!engine.enabled(id)) return { state: "none", reason: "disabled", ...relay, fileMiBps: {}, readMs: {} };
    const turnedOn = switched.get(id);
    const age = Date.now() - (since.get(id) ?? -Infinity);
    if (turnedOn && age < 600) return { state: "none", ...relay, fileMiBps: {}, readMs: {} };
    if (turnedOn && age < 2600) return { state: "connecting", ...relay, fileMiBps: {}, readMs: {} };
    // Turned on: setting up, measuring for a moment, then the state the page was opened with
    // (from off, that is direct).
    const final = base.off ? scenario("direct") : base, finalName = base.off ? "direct" : name;
    return { ...final, ...measured(finalName, speeds) };
  };
  // 「重新尝试」 in the sheet: for three seconds the computer is being tried again.
  if (againOf) {
    const again = againOf, status = engine.status;
    engine.status = (id) => (Date.now() - (again.get(id) ?? -Infinity) < 3000 ? { ...status.call(engine, id), state: "connecting", reason: undefined } : status.call(engine, id));
  }
}
