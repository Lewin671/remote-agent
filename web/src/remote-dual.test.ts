import { describe, expect, it, vi } from "vitest";
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { Code, ConnectError } from "./test/remote-fixture";
import { getCoreHost } from "./core/host";
import { SyntheticRemote } from "./test/remote-fixture";
import { parseTestHeader, testRequestIdentity } from "./test/stored-security-fixture";
import { EnvelopeSchema, PayloadKind, type Envelope } from "./gen/remoteagent/v1/envelope_pb";
import { NotForwardedSchema } from "./gen/remoteagent/v1/relay_pb";
import { CommandResultSchema, CommandSchema } from "./gen/remoteagent/v1/remote_pb";
import { DispatchCommandEffectSchema, OutcomeSource } from "./gen/remoteagent/core/v1/app_pb";
import { CommandRouteLeg, DirectSignalSendEffectSchema } from "./gen/remoteagent/core/v1/transport_pb";
import type { FrameStream } from "./transport/path";

function makeCmdBytes(): Uint8Array<ArrayBuffer> {
  return toBinary(
    CommandSchema,
    create(CommandSchema, {
      kind: { case: "createSession", value: { cwd: "/work/example" } },
    }),
  ) as Uint8Array<ArrayBuffer>;
}

async function sealCmd(
  r: SyntheticRemote,
  requestId: string,
  deadlineMs: bigint,
  commandBytes: Uint8Array<ArrayBuffer>,
): Promise<Envelope> {
  return r.world.fixture.seal(
    r.member.account.deviceId,
    { kind: PayloadKind.COMMAND, machineId: r.machine, requestId, deadlineMs },
    commandBytes,
  );
}

describe("Core-driven command attempt execution (Direct & Relay legs)", () => {
  it("routes CommandRouteLeg.DIRECT through DirectExecutor.sendRpc and forwards sealed result to Core", async () => {
    const r = await SyntheticRemote.create();
    const holder = r as unknown as {
      directExec: {
        sendRpc: (
          pcId: bigint,
          requestId: string,
          reqEnvBytes: Uint8Array<ArrayBuffer>,
          timeoutMs: bigint,
          handoff: () => void,
          signal?: AbortSignal,
        ) => Promise<Envelope>;
      };
    };
    let expectedResult: Envelope | undefined;
    const sendRpcSpy = vi.spyOn(holder.directExec, "sendRpc").mockImplementation(async (_pcId, _reqId, reqEnvBytes, _timeoutMs, handoff) => {
      handoff();
      const env = fromBinary(EnvelopeSchema, reqEnvBytes);
      const h = parseTestHeader(new Uint8Array(env.securityHeader));
      const binding = await testRequestIdentity(h, makeCmdBytes());
      expectedResult = await r.seal(
        { kind: PayloadKind.COMMAND_RESULT, requestId: h.request },
        CommandResultSchema,
        create(CommandResultSchema, {
          outcome: "executed",
          requestKeyHash: binding.key,
          payloadHash: binding.payload,
        }),
      );
      return expectedResult;
    });

    try {
      const cmdBytes = makeCmdBytes();
      const requestId = "req-direct-test-0001";
      const deadlineMs = BigInt(Date.now() + 30_000);
      const sealedEnvelope = await sealCmd(r, requestId, deadlineMs, cmdBytes);
      const outcome = await r.executeCommandAttempt(create(DispatchCommandEffectSchema, {
        requestId,
        attemptId: "att-direct-1",
        machineId: r.machine,
        sessionId: "",
        deadlineMs,
        commandBytes: cmdBytes,
        rawSealedEnvelope: toBinary(EnvelopeSchema, sealedEnvelope),
        routeLeg: CommandRouteLeg.DIRECT,
        directPcId: 7n,
        directTimeoutMs: 5_000n,
        directTicketId: 99n,
      }));
      expect(sendRpcSpy).toHaveBeenCalledTimes(1);
      expect(outcome).toMatchObject({
        source: OutcomeSource.DAEMON,
        routeWasDirect: true,
      });
      expect(outcome.directTicketId).toBe(99n);
      expect(outcome.attemptBound).toBe(true);
      expect(outcome.handedOff).toBe(true);
      expect(fromBinary(EnvelopeSchema, outcome.rawSealedResult)).toEqual(expectedResult);
    } finally {
      await r.dispose();
    }
  });

  it("reports notForwarded=true before handoff and notForwarded=false after handoff when direct fails", async () => {
    const r = await SyntheticRemote.create();
    const holder = r as unknown as {
      directExec: {
        sendRpc: (
          pcId: bigint,
          requestId: string,
          reqEnvBytes: Uint8Array<ArrayBuffer>,
          timeoutMs: bigint,
          handoff: () => void,
          signal?: AbortSignal,
        ) => Promise<Envelope>;
      };
    };
    const spy = vi.spyOn(holder.directExec, "sendRpc");
    try {
      const cmdBytes = makeCmdBytes();
      const deadlineMs = BigInt(Date.now() + 30_000);
      const preEnv = await sealCmd(r, "req-direct-pre-0001", deadlineMs, cmdBytes);
      spy.mockRejectedValueOnce(new Error("dc closed before send"));
      const pre = await r.executeCommandAttempt(create(DispatchCommandEffectSchema, {
        requestId: "req-direct-pre-0001",
        attemptId: "att-direct-pre",
        machineId: r.machine,
        deadlineMs,
        commandBytes: cmdBytes,
        rawSealedEnvelope: toBinary(EnvelopeSchema, preEnv),
        routeLeg: CommandRouteLeg.DIRECT,
        directPcId: 7n,
        directTimeoutMs: 5_000n,
        directTicketId: 10n,
      }));
      expect(pre).toMatchObject({
        source: OutcomeSource.TRANSPORT,
        routeWasDirect: true,
        notForwarded: true,
        handedOff: false,
      });

      const postEnv = await sealCmd(r, "req-direct-post-0001", deadlineMs, cmdBytes);
      spy.mockImplementationOnce(async (_pcId, _reqId, _bytes, _timeout, handoff) => {
        handoff();
        throw new Error("dc dropped after write");
      });
      const post = await r.executeCommandAttempt(create(DispatchCommandEffectSchema, {
        requestId: "req-direct-post-0001",
        attemptId: "att-direct-post",
        machineId: r.machine,
        deadlineMs,
        commandBytes: cmdBytes,
        rawSealedEnvelope: toBinary(EnvelopeSchema, postEnv),
        routeLeg: CommandRouteLeg.DIRECT,
        directPcId: 7n,
        directTimeoutMs: 5_000n,
        directTicketId: 11n,
      }));
      expect(post.source).toBe(OutcomeSource.TRANSPORT);
      expect(post.routeWasDirect).toBe(true);
      expect(post.notForwarded).toBe(false);
      expect(post.handedOff).toBe(true);
    } finally {
      await r.dispose();
    }
  });

  it("routes CommandRouteLeg.RELAY through Core RelayRpcEffect and distinguishes bound NotForwarded from stale epoch", async () => {
    const r = await SyntheticRemote.create();
    const holder = r as unknown as { api: unknown };
    try {
      const cmdBytes = makeCmdBytes();
      const deadlineMs = BigInt(Date.now() + 30_000);
      const env = await sealCmd(r, "req-relay-only-0001", deadlineMs, cmdBytes);
      // Shell executeCommandAttempt only executes direct legs; relay legs run inside Core via RelayRpcEffect.
      const shellRelay = await r.executeCommandAttempt(create(DispatchCommandEffectSchema, {
        requestId: "req-relay-only-0001",
        attemptId: "att-relay-only",
        machineId: r.machine,
        deadlineMs,
        commandBytes: cmdBytes,
        rawSealedEnvelope: toBinary(EnvelopeSchema, env),
        routeLeg: CommandRouteLeg.RELAY,
      }));
      expect(shellRelay.source).toBe(OutcomeSource.LOCAL);
      expect(shellRelay.transportCode).toBe("failed_precondition");

      r.start();
      await vi.waitFor(() => expect((r as unknown as { token: string }).token).not.toBe(""));

      const boundAttempts: string[] = [];
      let seenRelayEpoch = "";
      holder.api = {
        forward: async (q: { attemptId: string; relayEpoch: string }) => {
          seenRelayEpoch = q.relayEpoch;
          boundAttempts.push(q.attemptId);
          throw new ConnectError("offline", Code.Unavailable, undefined, [
            {
              desc: NotForwardedSchema,
              value: { attemptId: q.attemptId, relayEpoch: q.relayEpoch, reason: "machine_offline" },
            },
          ]);
        },
      };
      // Bound NotForwarded proves the daemon never received the command: settles NotSent immediately on 1st attempt.
      await expect(getCoreHost(r).pingMachine(r.machine)).rejects.toThrow();
      expect(seenRelayEpoch).not.toBe("");
      expect(boundAttempts).toHaveLength(1);

      const staleAttempts: string[] = [];
      const staleAc = new AbortController();
      holder.api = {
        forward: async (q: { attemptId: string }) => {
          staleAttempts.push(q.attemptId);
          if (staleAttempts.length >= 2) staleAc.abort();
          throw new ConnectError("offline", Code.Unavailable, undefined, [
            {
              desc: NotForwardedSchema,
              value: { attemptId: q.attemptId, relayEpoch: "stale-epoch", reason: "machine_offline" },
            },
          ]);
        },
      };
      // Stale epoch invalidates the NotForwarded proof: Core must treat outcome as unknown and retry instead of settling NotSent.
      await expect(getCoreHost(r).pingMachine(r.machine, staleAc.signal)).rejects.toThrow();
      expect(staleAttempts.length).toBeGreaterThanOrEqual(2);
      expect(staleAttempts[1]).not.toBe(staleAttempts[0]);
    } finally {
      await r.dispose();
    }
  });

  // A page hidden past the grace period is stopped and later restarted; direct
  // handshakes must leave the browser again instead of being dropped forever.
  it("sends direct signals again after the page is stopped and restarted", async () => {
    const r = await SyntheticRemote.create();
    try {
      r.start();
      r.stop();
      r.start();
      const sent: Uint8Array[] = [];
      r.directExec.attachLeg({ ready: true, send: (raw: Uint8Array) => sent.push(raw) } as unknown as FrameStream);
      const ok = r.directExec.signalSend(create(DirectSignalSendEffectSchema, {
        toDevice: new Uint8Array(32),
        frameClass: 1,
        frameId: new Uint8Array(16),
        sealed: new Uint8Array([1]),
      }));
      expect(ok).toBe(true);
      expect(sent).toHaveLength(1);
    } finally {
      await r.dispose();
    }
  });
});
