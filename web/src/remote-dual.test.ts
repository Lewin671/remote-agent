// @vitest-environment happy-dom
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { expect, test, vi } from "vitest";
import { parseHeader, requestIdentity } from "./device-security";
import { unpackSecurity } from "./device-security-client";
import { EnvelopeSchema, type Envelope } from "./gen/remoteagent/v1/envelope_pb";
import { NotForwardedSchema } from "./gen/remoteagent/v1/relay_pb";
import { CommandResultSchema } from "./gen/remoteagent/v1/remote_pb";
import { SyntheticRemote } from "./test/remote-fixture";

function installDirectReadPath(
  r: SyntheticRemote,
  writeImpl: (raw: Uint8Array, handoff: () => void, signal?: AbortSignal) => Promise<void>,
) {
  const pathCtrl = new AbortController();
  const peer = {
    id: r.machine,
    head: "synthetic-head",
    live: true,
    session: { active: true, close: vi.fn() },
    paths: new Map([
      [
        1,
        {
          ready: true,
          signal: pathCtrl.signal,
          write: writeImpl,
        },
      ],
    ]),
    snapshotUntil: Date.now() + 60_000,
    downloadUntil: Date.now() + 60_000,
  };
  const direct = (r as unknown as {
    direct: {
      self: unknown;
      codec: unknown;
      head: string;
      freshUntil: number;
      record: unknown;
      peers: Map<string, unknown>;
      pending: Map<string, { resolve: (env: Envelope) => void }>;
      setEnabled: (id: string, enabled: boolean) => void;
    };
  }).direct;
  direct.setEnabled(r.machine, true);
  direct.self = {};
  direct.codec = {};
  direct.head = "synthetic-head";
  direct.freshUntil = Date.now() + 60_000;
  direct.record = { member: true };
  direct.peers.set(r.machine, peer);
  return {
    abortPath: () => pathCtrl.abort(),
    deliverDirectResult: (requestId: string, env: Envelope) => {
      direct.pending.get(requestId)?.resolve(env);
    },
  };
}

async function sealExecutedDirectReply(r: SyntheticRemote, rawReq: Uint8Array, sessionId: string) {
  const env = fromBinary(EnvelopeSchema, rawReq);
  const h = parseHeader(new Uint8Array(env.securityHeader));
  const original = await r.world.fixture.open(r.machine, env);
  const binding = await requestIdentity(h, unpackSecurity(h, original.plaintext));
  const reply = await r.seal(
    { kind: 6, sessionId, requestId: h.request },
    CommandResultSchema,
    create(CommandResultSchema, {
      outcome: "executed",
      reason: "completed",
      resultAvailable: true,
      requestKeyHash: binding.key,
      payloadHash: binding.payload,
    }),
  );
  return { requestId: h.request, reply };
}

test("Remote dual-sends interactive command with single seal, identical bytes, and fast direct win", async () => {
  const r = await SyntheticRemote.create();
  const sealSpy = vi.spyOn(r as unknown as { sealSecureMessage: (...args: unknown[]) => Promise<unknown> }, "sealSecureMessage");
  let directBytes: Uint8Array | undefined;
  let relayBytes: Uint8Array | undefined;
  let relayAborted = false;

  const directHandle = installDirectReadPath(r, async (raw, handoff) => {
    directBytes = new Uint8Array(raw);
    handoff();
    const { requestId, reply } = await sealExecutedDirectReply(r, raw, "s");
    queueMicrotask(() => directHandle.deliverDirectResult(requestId, reply));
  });

  Object.defineProperty(r, "api", {
    configurable: true,
    get: () => ({
      forward: (req: { command: Envelope }, opts?: { signal?: AbortSignal }) =>
        new Promise((_, reject) => {
          relayBytes = new Uint8Array(toBinary(EnvelopeSchema, req.command));
          opts?.signal?.addEventListener("abort", () => {
            relayAborted = true;
            reject(new DOMException("Aborted", "AbortError"));
          });
        }),
    }),
  });

  try {
    r.send("s", "dual-direct-wins");
    await vi.waitFor(() => expect(r.outbox("s")[0]?.status).toBe("sent"));
    expect(sealSpy).toHaveBeenCalledTimes(1);
    expect(directBytes).toBeDefined();
    expect(relayBytes).toBeDefined();
    expect(directBytes).toEqual(relayBytes);
    await vi.waitFor(() => expect(relayAborted).toBe(true));
  } finally {
    sealSpy.mockRestore();
    await r.dispose();
  }
});

test("Remote dual-send falls back to Relay EXECUTED cleanly when Direct drops post-handoff", async () => {
  const r = await SyntheticRemote.create();
  installDirectReadPath(r, async (_raw, handoff) => {
    handoff();
    throw new Error("dc1 closed mid-flight");
  });

  let relayCalls = 0;
  (r as unknown as { api: unknown }).api = {
    forward: async (q: { command: Envelope }) => {
      relayCalls++;
      const h = parseHeader(new Uint8Array(q.command.securityHeader));
      return {
        result: await r.seal({ kind: 6, sessionId: "s", requestId: h.request }, CommandResultSchema, create(CommandResultSchema)),
      };
    },
  };

  try {
    r.send("s", "relay-fallback-after-drop");
    await vi.waitFor(() => expect(r.outbox("s")[0]?.status).toBe("sent"));
    expect(relayCalls).toBe(1);
  } finally {
    await r.dispose();
  }
});

test("Remote dual-send never marks not_sent when Direct handed off and Relay returns NotForwarded", async () => {
  const r = await SyntheticRemote.create();
  installDirectReadPath(r, async (_raw, handoff) => {
    handoff();
    throw new Error("dc1 dropped after handoff");
  });

  (r as unknown as { api: unknown }).api = {
    forward: async (q: { attemptId: string }) => {
      throw new ConnectError("refused", Code.Unavailable, undefined, [
        { desc: NotForwardedSchema, value: { attemptId: q.attemptId, relayEpoch: "fixture-epoch", reason: "machine_offline" } },
      ]);
    },
  };

  try {
    const sendId = r.send("s", "handoff-then-not-forwarded");
    await vi.waitFor(() => expect(r.outbox("s")[0]?.status).toBe("failed"));
    expect(r.outbox("s")[0]).toMatchObject({
      sendId,
      unknown: true,
      text: "handoff-then-not-forwarded",
    });
  } finally {
    await r.dispose();
  }
});

test("Remote dual-send ignores late Relay settlement after Direct already won", async () => {
  const r = await SyntheticRemote.create();
  let resolveLateRelay!: (v: unknown) => void;
  let capturedReqId = "";

  const directHandle = installDirectReadPath(r, async (raw, handoff) => {
    handoff();
    const { requestId, reply } = await sealExecutedDirectReply(r, raw, "s");
    capturedReqId = requestId;
    queueMicrotask(() => directHandle.deliverDirectResult(requestId, reply));
  });

  (r as unknown as { api: unknown }).api = {
    forward: () =>
      new Promise(resolve => {
        resolveLateRelay = resolve;
      }),
  };

  try {
    r.send("s", "ignore-late-relay");
    await vi.waitFor(() => expect(r.outbox("s")[0]?.status).toBe("sent"));
    resolveLateRelay({
      result: await r.seal(
        { kind: 6, sessionId: "s", requestId: capturedReqId },
        CommandResultSchema,
        create(CommandResultSchema),
      ),
    });
    await new Promise(resolve => setTimeout(resolve, 20));
    expect(r.outbox("s")[0]?.status).toBe("sent");
  } finally {
    await r.dispose();
  }
});
