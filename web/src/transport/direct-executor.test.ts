import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { afterEach, describe, expect, it, vi } from "vitest";
import { EnvelopeSchema, HeaderSchema, PayloadKind } from "../gen/remoteagent/v1/envelope_pb";
import { PeerFrameSchema, ServerFrameSchema } from "../gen/remoteagent/v1/relay_pb";
import { FragmentSchema, PeerFrameDeliverySchema } from "../gen/remoteagent/v1/transport_pb";
import type { Event as CoreEvent } from "../gen/remoteagent/core/v1/app_pb";
import {
  DirectCandFamily,
  DirectCandType,
  DirectDcReadStatsEffectSchema,
  DirectPcAddCandidatesEffectSchema,
  DirectPcCloseEffectSchema,
  DirectPcConfirmEffectSchema,
  DirectPcCreateEffectSchema,
  DirectPcDescribeEffectSchema,
  DirectPcSetRemoteEffectSchema,
  DirectSignalSendEffectSchema,
  DirectStateKind,
} from "../gen/remoteagent/core/v1/transport_pb";
import { DirectExecutor, peerFrameBody } from "./direct-executor";
import { PathChanged } from "./recovery";
import { fakeRTC } from "../test/peer/fake-rtc";

describe("DirectExecutor", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it("extracts bounded ServerFrame peer bodies", () => {
    const delivery = create(PeerFrameDeliverySchema, {
      fromDevice: new Uint8Array(16).fill(1),
      toDevice: new Uint8Array(16).fill(2),
      sealed: Uint8Array.from([1, 2, 3, 4]),
    });
    const raw = toBinary(ServerFrameSchema, create(ServerFrameSchema, {
      kind: { case: "peerFrameDelivery", value: delivery },
    })) as Uint8Array<ArrayBuffer>;
    const body = peerFrameBody(raw, 11);
    expect(body).toBeDefined();
    expect(fromBinary(PeerFrameDeliverySchema, body!).sealed).toEqual(Uint8Array.from([1, 2, 3, 4]));
    expect(peerFrameBody(raw, 12)).toBeUndefined();
  });

  it("bridges signaling, PC lifecycle, stats without IPs, RPCs, and confirmed-only pushed events", async () => {
    const sdk = fakeRTC();
    const events: CoreEvent[] = [];
    const pushed: Array<{ machineId: string; pcId: bigint; seq: bigint }> = [];
    const sentFrames: Uint8Array[] = [];

    const exec = new DirectExecutor({
      emit: (ev) => events.push(ev),
      onPushedEvent: (machineId, pcId, _env, header) => pushed.push({ machineId, pcId, seq: header.seq }),
      factory: sdk.factory,
    });

    // Attach leg and verify signalSend wraps DirectedPeerFrame
    const leg = {
      ready: true,
      send: (raw: Uint8Array<ArrayBuffer>) => sentFrames.push(raw),
      close: () => {},
    };
    const toDevice = new Uint8Array(16).fill(2);
    exec.attachLeg(leg);
    expect(exec.signalSend(create(DirectSignalSendEffectSchema, {
      toDevice,
      frameClass: 1,
      frameId: Uint8Array.from([1, 2, 3]),
      sealed: Uint8Array.from([4, 5, 6]),
    }))).toBe(true);
    expect(sentFrames).toHaveLength(1);
    const parsedFrame = fromBinary(PeerFrameSchema, sentFrames[0]!);
    expect(parsedFrame.kind.case).toBe("directedPeerFrame");
    if (parsedFrame.kind.case === "directedPeerFrame") {
      expect(parsedFrame.kind.value.toDevice).toEqual(toDevice);
      expect(parsedFrame.kind.value.sealed).toEqual(Uint8Array.from([4, 5, 6]));
    }

    // Create PC & describe/remote/candidates
    exec.pcCreate(create(DirectPcCreateEffectSchema, {
      pcId: 11n,
      machineId: "mac",
      roundId: 1n,
      use: 1,
      generation: 1n,
      stunServers: ["stun:example.com:3478"],
    }));
    const pc = sdk.pcs[0]!;
    const validSdp = "v=0\r\no=- 0 0 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\nm=application 9 UDP/DTLS/SCTP webrtc-datachannel\r\na=fingerprint:sha-256 AA:BB\r\n";
    pc.createOffer.mockResolvedValue({ type: "offer", sdp: validSdp });
    await exec.pcDescribe(create(DirectPcDescribeEffectSchema, { pcId: 11n, offer: true, iceRestart: false }));
    await exec.pcSetRemote(create(DirectPcSetRemoteEffectSchema, { pcId: 11n, offer: false, sdp: new TextEncoder().encode(validSdp) }));
    await exec.pcAddCandidates(create(DirectPcAddCandidatesEffectSchema, {
      pcId: 11n,
      candidates: [{ line: new TextEncoder().encode("candidate:1 1 udp 2122260223 192.0.2.1 5000 typ host"), ufrag: new Uint8Array(), mid: new TextEncoder().encode("0"), mlineIndex: 0 }],
      endOfCandidates: true,
    }));
    expect(pc.addIceCandidate).toHaveBeenCalledTimes(2);
    expect(events.some((e) => e.event.case === "directPcCreated" && e.event.value.ok)).toBe(true);
    expect(events.some((e) => e.event.case === "directPcDescription" && e.event.value.ok)).toBe(true);
    expect(events.some((e) => e.event.case === "directPcRemoteSet" && e.event.value.ok)).toBe(true);
    expect(events.some((e) => e.event.case === "directPcCandidatesAdded" && e.event.value.ok)).toBe(true);

    // Pushed event BEFORE confirm must wait; delivered AFTER pcConfirm
    const dc = pc.channels[0]!;
    dc.readyState = "open";
    dc.onopen?.();
    pc.state("connected");
    expect(events.some((e) => e.event.case === "directDcState" && e.event.value.open)).toBe(true);
    expect(events.some((e) => e.event.case === "directPcState" && e.event.value.kind === DirectStateKind.CONN)).toBe(true);

    const makeDcFragment = async (envBytes: Uint8Array<ArrayBuffer>) => {
      const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", envBytes));
      return toBinary(FragmentSchema, create(FragmentSchema, {
        messageId: crypto.getRandomValues(new Uint8Array(16)),
        index: 0,
        count: 1,
        ciphertextLength: envBytes.byteLength,
        ciphertextSha256: digest,
        pathGeneration: 1n,
        fragment: envBytes,
      }));
    };

    const pushHeader = toBinary(HeaderSchema, create(HeaderSchema, {
      machineId: "mac",
      sender: "mac",
      kind: PayloadKind.EVENT,
      seq: 42n,
    }));
    const pushEnv = toBinary(EnvelopeSchema, create(EnvelopeSchema, { header: pushHeader })) as Uint8Array<ArrayBuffer>;
    dc.onmessage?.({ data: (await makeDcFragment(pushEnv)).buffer as ArrayBuffer });
    await new Promise((r) => setTimeout(r, 10));
    expect(pushed).toHaveLength(0);

    // Unconfirmed sendRpc must throw PathChanged
    await expect(exec.sendRpc(11n, "req-0", Uint8Array.from([1]), 1000n, () => {})).rejects.toThrow(PathChanged);

    exec.pcConfirm(create(DirectPcConfirmEffectSchema, { pcId: 11n, machineId: "mac", use: 1 }));
    await vi.waitFor(() => expect(pushed).toEqual([{ machineId: "mac", pcId: 11n, seq: 42n }]));

    // Confirmed RPC request + matching response
    let handedOff = false;
    const rpcPromise = exec.sendRpc(11n, "req-1", Uint8Array.from([9, 8, 7]), 2000n, () => { handedOff = true; });
    await vi.waitFor(() => expect(dc.send).toHaveBeenCalledTimes(1));
    expect(handedOff).toBe(true);

    const respHeader = toBinary(HeaderSchema, create(HeaderSchema, {
      machineId: "mac",
      sender: "mac",
      kind: PayloadKind.COMMAND_RESULT,
      requestId: "req-1",
    }));
    const respEnv = toBinary(EnvelopeSchema, create(EnvelopeSchema, { header: respHeader, ciphertext: Uint8Array.from([3, 2, 1]) })) as Uint8Array<ArrayBuffer>;
    dc.onmessage?.({ data: (await makeDcFragment(respEnv)).buffer as ArrayBuffer });
    const resolved = await rpcPromise;
    expect(resolved.ciphertext).toEqual(Uint8Array.from([3, 2, 1]));

    // dcReadStats forwards sanitized stats without IPs
    pc.report = [
      { id: "lc", type: "local-candidate", candidateType: "host", address: "10.0.0.2" },
      { id: "rc", type: "remote-candidate", candidateType: "srflx", address: "203.0.113.9" },
      { id: "cp", type: "candidate-pair", localCandidateId: "lc", remoteCandidateId: "rc", nominated: true, state: "succeeded" },
    ];
    await exec.dcReadStats(create(DirectDcReadStatsEffectSchema, { pcId: 11n }));
    const statsEv = events.find((e) => e.event.case === "directDcStats");
    expect(statsEv?.event.case).toBe("directDcStats");
    if (statsEv?.event.case === "directDcStats") {
      expect(statsEv.event.value.report?.pairs[0]?.localType).toBe(DirectCandType.HOST);
      expect(statsEv.event.value.report?.pairs[0]?.remoteFamily).toBe(DirectCandFamily.V4);
      expect(JSON.stringify(statsEv.event.value.report)).not.toContain("203.0.113.9");
    }

    // Core-initiated pcClose rejects pending RPC without emitting directPcClosed back
    const pendingBeforeClose = exec.sendRpc(11n, "req-2", Uint8Array.from([5]), 5000n, () => {});
    await vi.waitFor(() => expect(dc.send).toHaveBeenCalledTimes(2));
    exec.pcClose(create(DirectPcCloseEffectSchema, { pcId: 11n }));
    await expect(pendingBeforeClose).rejects.toThrow(PathChanged);
    expect(events.some((e) => e.event.case === "directPcClosed")).toBe(false);
    exec.close();
  });

  it("preserves getStats before closing a failed confirmed PC and does not close on disconnected (audit #2)", async () => {
    const sdk = fakeRTC();
    const events: CoreEvent[] = [];
    let resolveStats!: (val: { forEach: (cb: (v: Record<string, unknown>) => void) => void }) => void;

    const exec = new DirectExecutor({
      emit: (ev) => {
        events.push(ev);
        if (ev.event.case === "directPcState" && ev.event.value.kind === DirectStateKind.CONN && ev.event.value.value === "failed") {
          void exec.dcReadStats(create(DirectDcReadStatsEffectSchema, { pcId: ev.event.value.pcId }));
        }
      },
      onPushedEvent: () => {},
      factory: sdk.factory,
    });

    exec.pcCreate(create(DirectPcCreateEffectSchema, {
      pcId: 21n,
      machineId: "mac",
      roundId: 1n,
      use: 1,
      generation: 1n,
      stunServers: [],
    }));
    const pc = sdk.pcs[0]!;
    const dc = pc.channels[0]!;
    dc.readyState = "open";
    dc.onopen?.();
    pc.state("connected");
    exec.pcConfirm(create(DirectPcConfirmEffectSchema, { pcId: 21n, machineId: "mac", use: 1 }));

    // Transient disconnected must not emit directPcClosed or close the PC.
    pc.state("disconnected");
    expect(events.some((e) => e.event.case === "directPcClosed")).toBe(false);
    expect(pc.connectionState).toBe("disconnected");

    // Failed on confirmed PC must wait for in-flight dcReadStats before closing and emitting directPcClosed.
    let closedBeforeStats = false;
    const origClose = pc.close.bind(pc);
    pc.close = () => {
      if (!events.some((e) => e.event.case === "directDcStats")) closedBeforeStats = true;
      origClose();
    };
    pc.getStats.mockImplementationOnce(() => new Promise((res) => { resolveStats = res; }));

    pc.state("failed");
    expect(closedBeforeStats).toBe(false);
    expect(events.some((e) => e.event.case === "directPcClosed")).toBe(false);

    const rows = [
      { id: "lc", type: "local-candidate", candidateType: "host", address: "10.0.0.2" },
      { id: "rc", type: "remote-candidate", candidateType: "srflx", address: "203.0.113.9" },
      { id: "cp", type: "candidate-pair", localCandidateId: "lc", remoteCandidateId: "rc", nominated: true, state: "failed" },
    ];
    resolveStats({ forEach: (cb) => rows.forEach(cb) });

    await vi.waitFor(() => expect(events.some((e) => e.event.case === "directPcClosed")).toBe(true));
    const statsIdx = events.findIndex((e) => e.event.case === "directDcStats");
    const closedIdx = events.findIndex((e) => e.event.case === "directPcClosed");
    expect(statsIdx).toBeGreaterThan(-1);
    expect(statsIdx).toBeLessThan(closedIdx);
    expect(closedBeforeStats).toBe(false);
    exec.close();
  });

  it("leaves unconfirmed failed PC open for Core restart/close while allowing getStats (audit #2)", async () => {
    const sdk = fakeRTC();
    const events: CoreEvent[] = [];
    const exec = new DirectExecutor({
      emit: (ev) => {
        events.push(ev);
        if (ev.event.case === "directPcState" && ev.event.value.kind === DirectStateKind.CONN && ev.event.value.value === "failed") {
          void exec.dcReadStats(create(DirectDcReadStatsEffectSchema, { pcId: ev.event.value.pcId }));
        }
      },
      onPushedEvent: () => {},
      factory: sdk.factory,
    });

    exec.pcCreate(create(DirectPcCreateEffectSchema, {
      pcId: 22n,
      machineId: "mac",
      roundId: 1n,
      use: 1,
      generation: 1n,
      stunServers: [],
    }));
    const pc = sdk.pcs[0]!;
    pc.state("failed");
    await vi.waitFor(() => expect(events.some((e) => e.event.case === "directDcStats")).toBe(true));
    expect(events.some((e) => e.event.case === "directPcClosed")).toBe(false);
    expect(pc.connectionState).toBe("failed");
    exec.close();
  });

  it("maps W3C empty-string candidate to a single endOfCandidates event (audit #4)", () => {
    const sdk = fakeRTC();
    const events: CoreEvent[] = [];
    const exec = new DirectExecutor({
      emit: (ev) => events.push(ev),
      onPushedEvent: () => {},
      factory: sdk.factory,
    });

    exec.pcCreate(create(DirectPcCreateEffectSchema, {
      pcId: 23n,
      machineId: "mac",
      roundId: 1n,
      use: 1,
      generation: 1n,
      stunServers: [],
    }));
    const pc = sdk.pcs[0]!;
    pc.onicecandidate?.({
      candidate: {
        type: "",
        candidate: "",
        toJSON: () => ({ candidate: "", sdpMid: "0", sdpMLineIndex: 0 }),
      },
    });
    // A subsequent null end-of-candidates event must be deduplicated.
    pc.onicecandidate?.({ candidate: null });

    const candEvents = events.filter((e) => e.event.case === "directPcLocalCandidate");
    expect(candEvents).toHaveLength(1);
    expect(candEvents[0]!.event.case === "directPcLocalCandidate" && candEvents[0]!.event.value.endOfCandidates).toBe(true);
    expect(candEvents[0]!.event.case === "directPcLocalCandidate" && candEvents[0]!.event.value.candidate).toBe("");
    exec.close();
  });

  it("allows same-batch pcClose followed by pcCreate on generation restart while an old write is in flight (audit #7)", async () => {
    const sdk = fakeRTC();
    const events: CoreEvent[] = [];
    const exec = new DirectExecutor({
      emit: (ev) => events.push(ev),
      onPushedEvent: () => {},
      factory: sdk.factory,
    });

    exec.pcCreate(create(DirectPcCreateEffectSchema, {
      pcId: 31n,
      machineId: "mac",
      roundId: 7n,
      use: 1,
      generation: 1n,
      stunServers: [],
    }));
    const pc1 = sdk.pcs[0]!;
    const dc1 = pc1.channels[0]!;
    dc1.readyState = "open";
    dc1.bufferedAmount = 128 << 10; // hold write() in backpressure wait
    dc1.onopen?.();
    pc1.state("connected");
    exec.pcConfirm(create(DirectPcConfirmEffectSchema, { pcId: 31n, machineId: "mac", use: 1 }));

    expect(exec.sendTerminalFrame(31n, new Uint8Array(1024).fill(3))).toBe(true);

    // Same EffectBatch synchronous ClosePC + CreatePC for gen+1 of the same round/use.
    exec.pcClose(create(DirectPcCloseEffectSchema, { pcId: 31n }));
    exec.pcCreate(create(DirectPcCreateEffectSchema, {
      pcId: 32n,
      machineId: "mac",
      roundId: 7n,
      use: 1,
      generation: 2n,
      stunServers: [],
    }));

    const created2 = events.find((e) => e.event.case === "directPcCreated" && e.event.value.pcId === 32n);
    expect(created2?.event.case === "directPcCreated" && created2.event.value.ok).toBe(true);

    // Let the aborted writer settle and ensure it does not release pc32's use slot.
    await new Promise((r) => setTimeout(r, 10));
    expect(exec.budget.acquire("mac", "7", 1)).toBe(false);
    exec.close();
    expect(exec.budget.ownerAvailable("mac", "8")).toBe(true);
  });
});
