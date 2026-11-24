import { describe, expect, it, vi } from "vitest";
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
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

describe("Core-driven command attempt execution (Direct & Relay legs)", () => {
  it("routes CommandRouteLeg.DIRECT through DirectExecutor.sendRpc and validates sealed result hashes", async () => {
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
    const sendRpcSpy = vi.spyOn(holder.directExec, "sendRpc").mockImplementation(async (_pcId, _reqId, reqEnvBytes, _timeoutMs, handoff) => {
      handoff();
      const env = fromBinary(EnvelopeSchema, reqEnvBytes);
      const h = parseTestHeader(new Uint8Array(env.securityHeader));
      const binding = await testRequestIdentity(h, makeCmdBytes());
      return await r.seal(
        { kind: PayloadKind.COMMAND_RESULT, requestId: h.request },
        CommandResultSchema,
        create(CommandResultSchema, {
          outcome: "executed",
          requestKeyHash: binding.key,
          payloadHash: binding.payload,
        }),
      );
    });

    try {
      const cmdBytes = makeCmdBytes();
      const outcome = await r.executeCommandAttempt(create(DispatchCommandEffectSchema, {
        requestId: "req-direct-test-0001",
        attemptId: "att-direct-1",
        machineId: r.machine,
        sessionId: "",
        deadlineMs: BigInt(Date.now() + 30_000),
        commandBytes: cmdBytes,
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
      expect(outcome.signed).toBe(true);
      expect(outcome.hashesMatch).toBe(true);
      expect(outcome.attemptBound).toBe(true);
      expect(outcome.handedOff).toBe(true);
      expect(outcome.outerOutcome).toBe("executed");
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
      spy.mockRejectedValueOnce(new Error("dc closed before send"));
      const pre = await r.executeCommandAttempt(create(DispatchCommandEffectSchema, {
        requestId: "req-direct-pre-0001",
        attemptId: "att-direct-pre",
        machineId: r.machine,
        deadlineMs: BigInt(Date.now() + 30_000),
        commandBytes: makeCmdBytes(),
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

      spy.mockImplementationOnce(async (_pcId, _reqId, _bytes, _timeout, handoff) => {
        handoff();
        throw new Error("dc dropped after write");
      });
      const post = await r.executeCommandAttempt(create(DispatchCommandEffectSchema, {
        requestId: "req-direct-post-0001",
        attemptId: "att-direct-post",
        machineId: r.machine,
        deadlineMs: BigInt(Date.now() + 30_000),
        commandBytes: makeCmdBytes(),
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

  it("routes CommandRouteLeg.RELAY via api.forward and distinguishes bound NotForwarded from stale epoch", async () => {
    const r = await SyntheticRemote.create();
    const holder = r as unknown as { api: unknown };
    try {
      holder.api = {
        forward: async (q: { attemptId: string; relayEpoch: string }) => {
          throw new ConnectError("offline", Code.Unavailable, undefined, [
            {
              desc: NotForwardedSchema,
              value: { attemptId: q.attemptId, relayEpoch: q.relayEpoch, reason: "machine_offline" },
            },
          ]);
        },
      };
      const bound = await r.executeCommandAttempt(create(DispatchCommandEffectSchema, {
        requestId: "req-relay-bound-0001",
        attemptId: "att-relay-bound",
        machineId: r.machine,
        deadlineMs: BigInt(Date.now() + 30_000),
        commandBytes: makeCmdBytes(),
        routeLeg: CommandRouteLeg.RELAY,
      }));
      expect(bound).toMatchObject({
        source: OutcomeSource.SERVER,
        routeWasDirect: false,
        notForwarded: true,
      });
      expect(bound.attemptBound).toBe(true);
      expect(bound.outerReason).toBe("machine_offline");

      holder.api = {
        forward: async (q: { attemptId: string }) => {
          throw new ConnectError("offline", Code.Unavailable, undefined, [
            {
              desc: NotForwardedSchema,
              value: { attemptId: q.attemptId, relayEpoch: "stale-epoch", reason: "machine_offline" },
            },
          ]);
        },
      };
      const stale = await r.executeCommandAttempt(create(DispatchCommandEffectSchema, {
        requestId: "req-relay-stale-0001",
        attemptId: "att-relay-stale",
        machineId: r.machine,
        deadlineMs: BigInt(Date.now() + 30_000),
        commandBytes: makeCmdBytes(),
        routeLeg: CommandRouteLeg.RELAY,
      }));
      expect(stale.source).toBe(OutcomeSource.SERVER);
      expect(stale.notForwarded).toBe(false);
      expect(stale.attemptBound).toBe(false);
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
