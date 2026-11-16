import { describe, expect, it, vi } from "vitest";
import {
  AttemptDiag, DirectDiag, MAX_ATTEMPTS, MAX_ROUNDS, MAX_SAMPLES, RoundDiag, classifyAddress, classifyCandidate, leadPc, stageOf, tally, verdictOf,
  type EndReason, type RoundData, type StatsLike, type Verdict,
} from "./direct-diag";
import { DirectBudget, DirectChannel } from "./direct-channel";
import { fakeRTC } from "../test/peer/fake-rtc";

// Documentation addresses and a port nothing else uses, so a leak is visible.
const L6 = "2001:db8:1::5", L4 = "192.168.1.20", LS = "203.0.113.7", R6 = "2001:db8:2::9", R4 = "192.168.50.3", RS = "198.51.100.4";
const PORT = 54321;
const line = (ip: string, type: string, n = 1) => `candidate:${n} 1 udp 2113937151 ${ip} ${PORT} typ ${type}`;
const stats = (entries: Record<string, unknown>[]): StatsLike => ({ forEach: (cb) => entries.forEach(cb) });
const pair = (l: string, r: string, state: string, sent: number, got: number, rtt?: number) => ({ type: "candidate-pair", localCandidateId: l, remoteCandidateId: r, state, requestsSent: sent, responsesReceived: got, nominated: state === "succeeded", currentRoundTripTime: rtt });
const known = [
  { type: "local-candidate", id: "L6", candidateType: "host", address: L6 }, { type: "local-candidate", id: "L4", candidateType: "host", address: L4 }, { type: "local-candidate", id: "LS", candidateType: "srflx", address: LS },
  { type: "remote-candidate", id: "R6", candidateType: "host", address: R6 }, { type: "remote-candidate", id: "R4", candidateType: "host", address: R4 }, { type: "remote-candidate", id: "RS", candidateType: "srflx", address: RS },
];

describe("masking: type, family and range only", () => {
  it.each([
    ["10.1.2.3", "4", "private"], ["172.16.0.1", "4", "private"], ["172.31.255.1", "4", "private"], ["172.32.0.1", "4", "public"], ["192.168.0.9", "4", "private"],
    ["100.64.0.1", "4", "cgnat"], ["100.127.255.254", "4", "cgnat"], ["100.128.0.1", "4", "public"], ["203.0.113.7", "4", "public"], ["127.0.0.1", "4", "loopback"], ["169.254.3.4", "4", "link_local"],
    ["2001:db8::1", "6", "global"], ["2a00:1450::1", "6", "global"], ["fd12:3456::1", "6", "ula"], ["fc00::1", "6", "ula"], ["fe80::1%en0", "6", "link_local"], ["febf::1", "6", "link_local"], ["::1", "6", "loopback"], ["::ffff:10.0.0.1", "4", "private"],
    ["abcd-1234.local", "x", "mdns"], ["not an address", "x", "other"],
  ])("%s is family %s, %s", (address, family, cls) => {
    expect(classifyAddress(address)).toEqual({ family, cls });
  });

  it("reads a candidate line's type and range and nothing else", () => {
    expect(classifyCandidate(line(L6, "host"))).toEqual({ type: "host", family: "6", cls: "global" });
    expect(classifyCandidate(`a=${line(LS, "srflx")} raddr 0.0.0.0 rport 0`)).toEqual({ type: "srflx", family: "4", cls: "public" });
    expect(classifyCandidate(line("abcd.local", "host"))).toEqual({ type: "host", family: "x", cls: "mdns" });
    expect(classifyCandidate(line(L4, "weird"))?.type).toBe("other");
    expect(classifyCandidate("garbage")).toBeUndefined();
    expect(classifyCandidate(undefined)).toBeUndefined();
  });

  it("keeps no address, port or error text anywhere in what it stores", () => {
    const round = new RoundDiag(1), a = round.pc(1).begin();
    for (const [ip, type] of [[L6, "host"], [L4, "host"], [LS, "srflx"], ["abcd-1234.local", "host"]] as const) a.local(line(ip, type));
    for (const [ip, type] of [[R6, "host"], [R4, "host"], [RS, "srflx"]] as const) a.remote(line(ip, type));
    a.stunError(701, L4);
    a.reject("browser", Object.assign(new Error(`bad candidate ${R6}`), { name: "OperationError" }));
    round.pc(1).rejectSignal(5, new Error(`candidate cap ${R4}`));
    a.take(stats([...known, pair("L6", "R6", "failed", 5, 0), pair("L4", "R4", "succeeded", 5, 5, 0.02)]));
    const text = JSON.stringify(round.data);
    for (const secret of [L6, L4, LS, R6, R4, RS, "abcd", String(PORT), "2001", "db8", "192.168", "203.0", "bad candidate"]) expect(text).not.toContain(secret);
    // What it does keep is the counts by type, family and range.
    expect(round.data.pcs[0].attempts[0].local).toEqual({ "host|6|global": 1, "host|4|private": 1, "srflx|4|public": 1, "host|x|mdns": 1 });
    expect(round.data.pcs[0].attempts[0].stun).toEqual([{ code: 701, family: "4", n: 1 }]);
  });
});

describe("bounds", () => {
  it("keeps the last three rounds of a computer, and a refused round does not push them out", () => {
    const diag = new DirectDiag();
    for (let i = 0; i < 5; i++) diag.begin("mac");
    expect(diag.rounds("mac").map((r) => r.n)).toEqual([5, 4, 3]);
    expect(diag.rounds("mac")).toHaveLength(MAX_ROUNDS);
    for (let i = 0; i < 50; i++) diag.blocked("mac");
    expect(diag.rounds("mac")).toHaveLength(MAX_ROUNDS);
    expect(diag.rounds("other")).toEqual([]);
  });

  it("reports a refusal only while it is newer than the newest round", () => {
    let t = 1000;
    const diag = new DirectDiag(() => t);
    diag.begin("mac"); t = 2000; diag.blocked("mac");
    expect(diag.blockedAt("mac")).toBe(2000);
    t = 3000; diag.begin("mac");
    expect(diag.blockedAt("mac")).toBeUndefined();
  });

  it("bounds attempts, states, tallies, errors, rejections and pairs", () => {
    const round = new RoundDiag(1), pc = round.pc(1);
    let a = pc.begin();
    for (let i = 0; i < 10; i++) a = pc.begin();
    expect(pc.data.attempts).toHaveLength(MAX_ATTEMPTS);
    for (let i = 0; i < 200; i++) a.state("ice", i % 2 ? "checking" : "failed");
    expect(a.data.states.length).toBeLessThanOrEqual(40);
    for (let i = 0; i < 100; i++) a.local(`candidate:1 1 udp 1 10.0.0.${i} 1 typ ${["host", "srflx", "prflx", "relay", "x"][i % 5]}`);
    expect(Object.keys(a.data.local).length).toBeLessThanOrEqual(24);
    expect(tally(a.data.local)).toBe(100);
    for (let i = 0; i < 100; i++) a.stunError(300 + i, L4);
    expect(a.data.stun).toHaveLength(8);
    for (let i = 0; i < 100; i++) pc.rejectSignal(5, new Error("x".repeat(i % 20 + 1)));
    expect(pc.data.rejects.length).toBeLessThanOrEqual(8);
    const many = Array.from({ length: 40 }, (_, i) => ({ type: "local-candidate", id: `l${i}`, candidateType: ["host", "srflx", "prflx", "relay"][i % 4], address: i % 2 ? "10.0.0.1" : "2001:db8::1" }));
    const pairs = many.map((_, i) => pair(`l${i}`, `l${(i * 7) % 40}`, "failed", 1, 0));
    a.take(stats([...many, ...many.map((m) => ({ ...m, type: "remote-candidate" })), ...pairs]));
    expect(a.data.pairs.length).toBeLessThanOrEqual(12);
  });

  it("asks getStats a bounded number of times, one at a time, and never when it is missing", async () => {
    const round = new RoundDiag(1), a = round.pc(1).begin();
    const report = stats([]);
    let release!: () => void;
    const getStats = vi.fn(() => new Promise<StatsLike>((resolve) => { release = () => resolve(report); }));
    const pc = { getStats } as unknown as RTCPeerConnection;
    a.sample(pc); a.sample(pc);
    expect(getStats).toHaveBeenCalledTimes(1);   // one in flight at a time
    for (let i = 0; i < 20; i++) { release(); await Promise.resolve(); await Promise.resolve(); a.sample(pc); }
    expect(getStats.mock.calls.length).toBeLessThanOrEqual(MAX_SAMPLES);
    expect(() => a.sample({} as RTCPeerConnection)).not.toThrow();
  });

  it("never lets a throwing SDK or a malformed report reach the caller", () => {
    const a = new RoundDiag(1).pc(1).begin();
    expect(() => a.take({ forEach: () => { throw new Error("boom"); } })).not.toThrow();
    expect(() => a.take(stats([{ type: "candidate-pair", localCandidateId: 1, state: {}, requestsSent: "x" }]))).not.toThrow();
    expect(() => a.settle({ get iceGatheringState(): never { throw new Error("closed"); } } as unknown as RTCPeerConnection)).not.toThrow();
  });
});

// A round played through the real collector.
interface Shape { local?: string[]; remote?: string[]; checks?: ReturnType<typeof pair>[]; ice?: string[]; dtls?: string; dc?: string; boot?: boolean; sdp?: boolean; confirm?: boolean; stun?: number[]; noSample?: boolean }
function round(shape: Shape, end: EndReason | undefined, extra: (r: RoundDiag) => void = () => undefined): RoundData {
  let t = 0;
  const r = new RoundDiag(1, () => t), pc = r.pc(1);
  const { boot = true, sdp = true } = shape;
  pc.observe({ boot, sdpLocal: boot && sdp, sdpRemote: boot && sdp, peerConfirm: !!shape.confirm });
  if (boot) {
    const a = pc.begin();
    for (const l of shape.local ?? []) a.local(l);
    for (const l of shape.remote ?? []) a.remote(l);
    for (const code of shape.stun ?? []) a.stunError(code, L4);
    for (const s of shape.ice ?? []) { t += 100; a.state("ice", s); }
    if (shape.dtls) a.state("dtls", shape.dtls);
    if (shape.dc) a.state("dc", shape.dc);
    if (!shape.noSample) a.take(stats([...known, ...(shape.checks ?? [])]));
  }
  extra(r); t += 9900;
  if (end) r.finish(end);
  return r.data;
}
const v6local = [line(L6, "host"), line(L4, "host", 2), line(LS, "srflx", 3)];
const v6remote = [line(R6, "host"), line(R4, "host", 2), line(RS, "srflx", 3)];
const v4local = [line(L4, "host"), line(LS, "srflx", 2)], v4remote = [line(R4, "host"), line(RS, "srflx", 2)];
const failed = ["checking", "failed"];

describe("the verdict of a round", () => {
  const cases: [string, Verdict, RoundData][] = [
    ["nothing recorded", "none", undefined as unknown as RoundData],
    ["a round still going", "trying", round({ local: v4local }, undefined)],
    ["a round that connected", "connected", round({ local: v6local, remote: v6remote, ice: ["connected"], dtls: "connected", dc: "open", confirm: true }, undefined, (r) => { r.pc(1).observe({ ready: true }); r.establish(); })],
    ["a round that connected and then dropped", "lost", round({ local: v6local, remote: v6remote }, "lost", (r) => { r.pc(1).observe({ ready: true }); r.establish(); })],
    ["cancelled by turning direct off", "cancelled", round({ local: v6local }, "cancelled")],
    ["the computer never answered", "no_reply", round({ boot: false }, "no_response")],
    ["the computer said Unsupported", "peer_refused", round({ boot: false }, "peer_unsupported", (r) => r.note("unsupported"))],
    ["the relay limited the handshake", "signal_limited", round({ boot: false }, "timeout", (r) => r.note("limited"))],
    ["the computer was offline", "signal_offline", round({ boot: false }, "timeout", (r) => r.note("offline"))],
    ["no description was exchanged", "no_sdp", round({ sdp: false }, "timeout")],
    ["no address of ours at all", "no_local", round({ local: [], remote: v4remote, ice: failed }, "timeout")],
    ["STUN gave no mapped address", "no_stun", round({ local: [line(L4, "host"), line("x.local", "host", 2)], remote: v4remote, ice: failed, stun: [701] }, "no_traversal")],
    ["no mapped address even without the session's own verdict", "no_stun", round({ local: [line(L4, "host")], remote: v4remote, ice: failed }, "timeout")],
    ["none of the computer's addresses arrived", "no_remote", round({ local: v6local, remote: [], ice: failed }, "timeout")],
    ["our IPv6 checks were never answered", "ipv6_silent", round({ local: v6local, remote: v6remote, ice: failed, checks: [pair("L6", "R6", "failed", 14, 0), pair("L4", "R4", "failed", 12, 0), pair("LS", "RS", "failed", 12, 0)] }, "timeout")],
    ["IPv6 answered but the checks still all failed", "all_failed", round({ local: v6local, remote: v6remote, ice: failed, checks: [pair("L6", "R6", "failed", 14, 3), pair("L4", "R4", "failed", 12, 0)] }, "timeout")],
    ["IPv6 present but no pair was sampled", "all_failed", round({ local: v6local, remote: v6remote, ice: failed, noSample: true }, "timeout")],
    ["IPv4 only, every check failed", "all_failed", round({ local: v4local, remote: v4remote, ice: failed, checks: [pair("L4", "R4", "failed", 12, 0)] }, "timeout")],
    ["connected but encryption never finished", "dtls", round({ local: v4local, remote: v4remote, ice: ["checking", "connected"], dtls: "failed", checks: [pair("L4", "R4", "succeeded", 6, 6, 0.04)] }, "timeout")],
    ["encrypted but the channel never opened", "channel", round({ local: v4local, remote: v4remote, ice: ["connected"], dtls: "connected", dc: "connecting" }, "timeout")],
    ["the channel opened but the computer never confirmed", "confirm", round({ local: v4local, remote: v4remote, ice: ["connected"], dtls: "connected", dc: "open" }, "timeout")],
    ["the browser could not make a connection", "create_failed", round({ boot: true, local: [] }, "timeout", (r) => { r.data.pcs[0].attempts.length = 0; r.pc(1).block(); })],
  ];
  it.each(cases)("%s", (_name, expected, data) => {
    expect(verdictOf(data)).toBe(expected);
  });

  it("says the slots were full when a refusal is newer than every round", () => {
    expect(verdictOf(undefined, true)).toBe("budget");
    expect(verdictOf(round({ local: v4local }, "timeout"), true)).toBe("budget");
  });

  it("judges by the path that got furthest, loading first when they tie", () => {
    const r = new RoundDiag(1), [read, bulk] = [r.pc(1), r.pc(2)];
    read.observe({ boot: true });
    bulk.observe({ boot: true, sdpLocal: true, sdpRemote: true });
    expect(leadPc(r.data).pc.use).toBe(2);
    read.observe({ sdpLocal: true, sdpRemote: true });
    expect(leadPc(r.data).pc.use).toBe(1);
  });

  it("takes the attempt that got furthest, not the newest one that has only just started", () => {
    const r = new RoundDiag(1), pc = r.pc(1);
    pc.observe({ boot: true, sdpLocal: true, sdpRemote: true });
    const first = pc.begin();
    for (const l of v6local) first.local(l);
    for (const l of v6remote) first.remote(l);
    first.state("ice", "checking"); first.state("ice", "failed");
    pc.begin().state("gather", "gathering");
    r.finish("timeout");
    expect(stageOf(pc.data, leadPc(r.data).attempt)).toBe("ice");
    expect(verdictOf(r.data)).toBe("all_failed");
  });
});

describe("a real DirectChannel feeds the record", () => {
  it("counts local and remote candidates, states, STUN errors and a candidate the browser refuses", async () => {
    const sdk = fakeRTC(), round = new RoundDiag(1), diag = round.pc(1).begin();
    const channel = new DirectChannel({ peer: "p", connection: new Uint8Array(16), use: 1, generation: 1n, deadline: Date.now() + 10_000, budget: new DirectBudget(), valid: () => true, stun: [], factory: sdk.factory, diag, candidate: () => {}, state: () => {}, frame: () => {}, closed: () => {} });
    const pc = sdk.pcs[0];
    pc.candidate(line(L6, "host")); pc.candidate(line(LS, "srflx", 2)); pc.candidate(null);
    pc.onicecandidateerror?.({ errorCode: 701, address: L6 });
    pc.ice("checking"); pc.state("connecting");
    await channel.addCandidate({ candidate: line(R6, "host") });
    pc.addIceCandidate.mockRejectedValueOnce(Object.assign(new Error(R4), { name: "OperationError" }));
    await expect(channel.addCandidate({ candidate: line(R4, "host") })).rejects.toThrow();
    const a = round.data.pcs[0].attempts[0];
    expect(a.local).toEqual({ "host|6|global": 1, "srflx|4|public": 1 });
    expect(a.remote).toEqual({ "host|6|global": 1, "host|4|private": 1 });
    expect(a.stun).toEqual([{ code: 701, family: "6", n: 1 }]);
    expect(a.states.map((s) => `${s.k}:${s.v}`)).toEqual(["ice:checking", "conn:connecting"]);
    expect(round.data.pcs[0].rejects).toEqual([{ k: "browser:OperationError", n: 1 }]);
    expect(JSON.stringify(round.data)).not.toContain(R4);
    // Closing reads the stats once, before the connection goes.
    pc.report = [...known, pair("L6", "R6", "failed", 4, 0)];
    channel.close();
    await vi.waitFor(() => expect(a.sampled).toBe(true));
    expect(a.pairs).toEqual([expect.objectContaining({ lf: "6", rf: "6", failed: 1, sent: 4, got: 0 })]);
  });

  it("works unchanged without a record", () => {
    const sdk = fakeRTC();
    const channel = new DirectChannel({ peer: "p", connection: new Uint8Array(16), use: 1, generation: 1n, deadline: Date.now() + 10_000, budget: new DirectBudget(), valid: () => true, stun: [], factory: sdk.factory, candidate: () => {}, state: () => {}, frame: () => {}, closed: () => {} });
    sdk.pcs[0].candidate(line(L4, "host")); sdk.pcs[0].ice("failed"); sdk.pcs[0].onicecandidateerror?.({ errorCode: 1, address: "" });
    expect(() => channel.close()).not.toThrow();
  });
});

describe("AttemptDiag", () => {
  it("times each state from the round's start and ignores repeats", () => {
    let t = 1000;
    const r = new RoundDiag(1, () => t), a: AttemptDiag = r.pc(1).begin();
    t = 1400; a.state("ice", "checking"); a.state("ice", "checking"); t = 9000; a.state("ice", "failed");
    expect(a.data.states).toEqual([{ t: 400, k: "ice", v: "checking" }, { t: 8000, k: "ice", v: "failed" }]);
  });
});
