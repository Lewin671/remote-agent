import { afterEach, describe, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
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
});
