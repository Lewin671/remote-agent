import { afterEach, describe, expect, it, vi } from "vitest";
import { create, toBinary } from "@bufbuild/protobuf";
import { DirectCandFamily, DirectCandType, DirectStateKind } from "../gen/remoteagent/core/v1/transport_pb";
import { FragmentSchema } from "../gen/remoteagent/v1/transport_pb";
import { DirectBudget, DirectChannel } from "./direct-channel";
import { PathChanged } from "./recovery";
import { fakeRTC } from "../test/peer/fake-rtc";

describe("DirectChannel", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it("rejects mismatched STUN or invalid configuration synchronously", () => {
    const sdk = fakeRTC();
    const budget = new DirectBudget();
    expect(() =>
      new DirectChannel({
        peer: "peer",
        roundId: 1n,
        use: 1,
        generation: 1n,
        budget,
        valid: () => true,
        stun: ["turn:relay.example.com:3478"],
        factory: sdk.factory,
        candidate: () => {},
        state: () => {},
        frame: () => {},
        closed: () => {},
      }),
    ).toThrow(PathChanged);
  });

  it("forwards ICE/DTLS/DC states, stun errors, minimal SDP, framing, and stats without raw IPs", async () => {
    const sdk = fakeRTC();
    const budget = new DirectBudget();
    const states: Array<[DirectStateKind, string]> = [];
    const dcStates: boolean[] = [];
    const stunErrors: Array<[number, string]> = [];
    const received: Uint8Array[] = [];
    let closedCalls = 0;

    const channel = new DirectChannel({
      peer: "peer",
      roundId: 1n,
      use: 1,
      generation: 1n,
      budget,
      valid: () => true,
      stun: ["stun:stun.example.com:3478"],
      factory: sdk.factory,
      candidate: () => {},
      state: (kind, val) => states.push([kind, val]),
      dcState: (open) => dcStates.push(open),
      stunError: (code, addr) => stunErrors.push([code, addr]),
      frame: (raw) => received.push(raw),
      closed: () => { closedCalls++; },
    });

    const pc = sdk.pcs[0]!;
    const validSdp = "v=0\r\no=- 0 0 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\nm=application 9 UDP/DTLS/SCTP webrtc-datachannel\r\na=fingerprint:sha-256 AA:BB\r\n";
    pc.createOffer.mockResolvedValue({ type: "offer", sdp: validSdp });
    const rawSdp = await channel.description(true, true);
    expect(pc.createOffer).toHaveBeenCalledWith({ iceRestart: true });
    expect(new TextDecoder().decode(rawSdp)).toBe(validSdp);

    // Reject malformed SDP
    await expect(channel.remote(new TextEncoder().encode("invalid-sdp\r\n"), false)).rejects.toThrow();

    // Open data channel, confirm, write & receive fragmented message
    const dc = pc.channels[0]!;
    dc.readyState = "open";
    dc.onopen?.();
    pc.ice("checking");
    pc.state("connected");
    pc.onicecandidateerror?.({ errorCode: 701, address: "203.0.113.5" });
    expect(states).toContainEqual([DirectStateKind.DC, "open"]);
    expect(states).toContainEqual([DirectStateKind.ICE, "checking"]);
    expect(states).toContainEqual([DirectStateKind.CONN, "connected"]);
    expect(dcStates).toEqual([true]);
    expect(stunErrors).toEqual([[701, "203.0.113.5"]]);

    channel.confirm();
    const payload = new Uint8Array([1, 2, 3, 4]);
    const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", payload));
    const { toBinary } = await import("@bufbuild/protobuf");
    const frag = toBinary(FragmentSchema, create(FragmentSchema, {
      messageId: new Uint8Array(16).fill(9),
      index: 0,
      count: 1,
      ciphertextLength: payload.byteLength,
      ciphertextSha256: digest,
      pathGeneration: 1n,
      fragment: payload,
    }));
    dc.onmessage?.({ data: frag.buffer as ArrayBuffer });
    await vi.waitFor(() => expect(received).toHaveLength(1));
    expect(received[0]).toEqual(payload);

    // readStats translates addresses to family + type with zero raw IP/port fields
    pc.report = [
      { id: "lc1", type: "local-candidate", candidateType: "srflx", address: "198.51.100.42", port: 51234 },
      { id: "rc1", type: "remote-candidate", candidateType: "host", address: "2001:db8::1", port: 60000 },
      { id: "cp1", type: "candidate-pair", localCandidateId: "lc1", remoteCandidateId: "rc1", nominated: true, state: "succeeded", requestsSent: 3, responsesReceived: 2, currentRoundTripTime: 0.015 },
      { id: "tr1", type: "transport", dtlsState: "connected" },
      { id: "dc1", type: "data-channel", dataChannelIdentifier: 0, state: "open" },
    ];
    const stats = await channel.readStats();
    expect(stats.dtlsState).toBe("connected");
    expect(stats.dcState).toBe("open");
    expect(stats.pairs).toHaveLength(1);
    expect(stats.pairs[0]).toMatchObject({
      localType: DirectCandType.SRFLX,
      localFamily: DirectCandFamily.V4,
      remoteType: DirectCandType.HOST,
      remoteFamily: DirectCandFamily.V6,
      nominated: true,
      state: "succeeded",
      requestsSent: 3,
      responsesReceived: 2,
      hasCurrentRtt: true,
      currentRttSeconds: 0.015,
    });
    const serialized = JSON.stringify(stats);
    expect(serialized).not.toContain("198.51.100.42");
    expect(serialized).not.toContain("2001:db8::1");
    expect(serialized).not.toContain("51234");

    channel.close();
    expect(closedCalls).toBe(1);
  });

  it("suppresses cleanup errors during close and releases budget", () => {
    const sdk = fakeRTC();
    const budget = new DirectBudget();
    const channel = new DirectChannel({
      peer: "peer",
      roundId: 2n,
      use: 1,
      generation: 1n,
      budget,
      valid: () => true,
      stun: [],
      factory: sdk.factory,
      candidate: () => {},
      state: () => {},
      frame: () => {},
      closed: () => { throw Error("fixture close observer"); },
    });
    expect(() => channel.close()).toThrow("fixture close observer");
    expect(budget.ownerAvailable("peer", "3")).toBe(true);
  });

  it("keeps PC open on transient disconnected state and defers close on failed state (audit #2)", () => {
    const sdk = fakeRTC();
    const budget = new DirectBudget();
    const states: Array<[DirectStateKind, string]> = [];
    let closedCalls = 0;

    const channel = new DirectChannel({
      peer: "peer",
      roundId: 1n,
      use: 1,
      generation: 1n,
      budget,
      valid: () => true,
      stun: [],
      factory: sdk.factory,
      candidate: () => {},
      state: (kind, val) => states.push([kind, val]),
      frame: () => {},
      closed: () => { closedCalls++; },
    });

    const pc = sdk.pcs[0]!;
    const dc = pc.channels[0]!;
    dc.readyState = "open";
    dc.onopen?.();
    pc.state("connected");
    channel.confirm();
    expect(channel.ready).toBe(true);

    // Transient disconnected must not close the PC and must allow self-healing.
    pc.state("disconnected");
    expect(closedCalls).toBe(0);
    expect(pc.connectionState).toBe("disconnected");
    pc.state("connected");
    expect(channel.ready).toBe(true);
    expect(closedCalls).toBe(0);

    // Terminal failed marks channel unready without synchronously closing PC.
    pc.state("failed");
    expect(channel.ready).toBe(false);
    expect(closedCalls).toBe(0);
    expect(pc.connectionState).toBe("failed");

    // Browser-initiated closed cleans up synchronously.
    pc.state("closed");
    expect(closedCalls).toBe(1);
  });

  it("delivers ordered DataChannel messages in FIFO order even when SHA-256 resolves out of order (audit #5)", async () => {
    const sdk = fakeRTC();
    const budget = new DirectBudget();
    const received: number[] = [];

    const channel = new DirectChannel({
      peer: "peer",
      roundId: 1n,
      use: 1,
      generation: 1n,
      budget,
      valid: () => true,
      stun: [],
      factory: sdk.factory,
      candidate: () => {},
      state: () => {},
      frame: (raw) => received.push(raw[0]!),
      closed: () => {},
    });

    const pc = sdk.pcs[0]!;
    const dc = pc.channels[0]!;
    dc.readyState = "open";
    dc.onopen?.();
    pc.state("connected");
    channel.confirm();

    const p1 = new Uint8Array([1, 10, 20]);
    const p2 = new Uint8Array([2, 30, 40]);
    const d1 = new Uint8Array(await crypto.subtle.digest("SHA-256", p1));
    const d2 = new Uint8Array(await crypto.subtle.digest("SHA-256", p2));

    const f1 = toBinary(FragmentSchema, create(FragmentSchema, {
      messageId: new Uint8Array(16).fill(1),
      index: 0,
      count: 1,
      ciphertextLength: p1.byteLength,
      ciphertextSha256: d1,
      pathGeneration: 1n,
      fragment: p1,
    }));
    const f2 = toBinary(FragmentSchema, create(FragmentSchema, {
      messageId: new Uint8Array(16).fill(2),
      index: 0,
      count: 1,
      ciphertextLength: p2.byteLength,
      ciphertextSha256: d2,
      pathGeneration: 1n,
      fragment: p2,
    }));

    // Hold message 1's SHA-256 digest until after message 2 arrives.
    let releaseFirst!: () => void;
    let calls = 0;
    const realDigest = crypto.subtle.digest.bind(crypto.subtle);
    vi.spyOn(crypto.subtle, "digest").mockImplementation((algo, data) => {
      calls++;
      if (calls === 1) {
        return new Promise((resolve) => {
          releaseFirst = () => resolve(realDigest(algo, data));
        });
      }
      return realDigest(algo, data);
    });

    dc.onmessage?.({ data: f1.buffer as ArrayBuffer });
    dc.onmessage?.({ data: f2.buffer as ArrayBuffer });
    await new Promise((r) => setTimeout(r, 15));
    expect(received).toEqual([]);
    releaseFirst();
    await vi.waitFor(() => expect(received).toEqual([1, 2]));
    channel.close();
  });

  it("releases in-flight receive verification budget immediately on close so a new round can acquire (audit #5)", async () => {
    const sdk = fakeRTC();
    const budget = new DirectBudget();

    const channel = new DirectChannel({
      peer: "peer",
      roundId: 1n,
      use: 1,
      generation: 1n,
      budget,
      valid: () => true,
      stun: [],
      factory: sdk.factory,
      candidate: () => {},
      state: () => {},
      frame: () => {},
      closed: () => {},
    });

    const pc = sdk.pcs[0]!;
    const dc = pc.channels[0]!;
    dc.readyState = "open";
    dc.onopen?.();
    pc.state("connected");
    channel.confirm();

    const payload = new Uint8Array(64).fill(7);
    const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", payload));
    const frag = toBinary(FragmentSchema, create(FragmentSchema, {
      messageId: new Uint8Array(16).fill(5),
      index: 0,
      count: 1,
      ciphertextLength: payload.byteLength,
      ciphertextSha256: digest,
      pathGeneration: 1n,
      fragment: payload,
    }));

    let resolveDigest!: (buf: ArrayBuffer) => void;
    vi.spyOn(crypto.subtle, "digest").mockImplementationOnce(() => new Promise((res) => { resolveDigest = res; }));

    dc.onmessage?.({ data: frag.buffer as ArrayBuffer });
    await vi.waitFor(() => expect(resolveDigest).toBeDefined());

    channel.close();
    // Old round must not retain budget ownership while SHA-256 is still pending.
    expect(budget.ownerAvailable("peer", "2")).toBe(true);
    expect(budget.acquire("peer", "2", 1)).toBe(true);

    // Settling the aborted digest must not corrupt the new round's budget.
    resolveDigest(digest.buffer as ArrayBuffer);
    await new Promise((r) => setTimeout(r, 10));
    budget.release("peer", 1);
    expect(budget.ownerAvailable("peer", "3")).toBe(true);
  });

  it("transfers reassembly reservation without doubling peak receive budget for 768 KiB messages (audit #6)", async () => {
    const sdk = fakeRTC();
    const budget = new DirectBudget();
    const received: Uint8Array[] = [];

    const channel = new DirectChannel({
      peer: "peer",
      roundId: 1n,
      use: 2,
      generation: 1n,
      budget,
      valid: () => true,
      stun: [],
      factory: sdk.factory,
      candidate: () => {},
      state: () => {},
      frame: (raw) => received.push(raw),
      closed: () => {},
    });

    const pc = sdk.pcs[0]!;
    const dc = pc.channels[0]!;
    dc.readyState = "open";
    dc.onopen?.();
    pc.state("connected");
    channel.confirm();

    // Occupy 768 KiB of the 2 MiB per-peer receive budget (e.g. concurrent use=1 traffic).
    const preReserved = 768 << 10;
    expect(budget.reserve("peer", 1, preReserved, 1)).toBe(true);

    // Send a 768 KiB message split into 16 KiB-128 fragments (total 1.5 MiB <= 2 MiB, but 2x would be 2.25 MiB > 2 MiB).
    const payload = new Uint8Array(768 << 10);
    for (let i = 0; i < payload.byteLength; i += 4096) payload[i] = (i >>> 12) & 0xff;
    const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", payload));
    const chunkSize = (16 << 10) - 128;
    const count = Math.ceil(payload.byteLength / chunkSize);
    const msgId = new Uint8Array(16).fill(6);

    for (let index = 0; index < count; index++) {
      const frag = toBinary(FragmentSchema, create(FragmentSchema, {
        messageId: msgId,
        index,
        count,
        ciphertextLength: payload.byteLength,
        ciphertextSha256: digest,
        pathGeneration: 1n,
        fragment: payload.subarray(index * chunkSize, Math.min(payload.byteLength, (index + 1) * chunkSize)),
      }));
      dc.onmessage?.({ data: frag.buffer as ArrayBuffer });
    }

    await vi.waitFor(() => expect(received).toHaveLength(1));
    expect(received[0]!.byteLength).toBe(payload.byteLength);
    budget.free("peer", 1, preReserved, 1);
    channel.close();
    expect(budget.ownerAvailable("peer", "2")).toBe(true);
  });
});
