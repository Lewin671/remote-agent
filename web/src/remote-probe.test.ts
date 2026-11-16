// A warm-up sample is a real authorized read-only request of its own: sealed,
// sent on the path asked for, and the daemon's signed answer verified before it
// counts (docs/transport.md 「升级与热备」 · 只读选路).
import { expect, test, vi } from "vitest";
import { create, fromBinary } from "@bufbuild/protobuf";
import { SyntheticRemote } from "./test/remote-fixture";
import { CommandResultSchema, CommandSchema } from "./gen/remoteagent/v1/remote_pb";
import { ForwardResponseSchema } from "./gen/remoteagent/v1/relay_pb";
import { parseHeader } from "./device-security";
import { unpackSecurity } from "./device-security-client";
import { type Envelope } from "./gen/remoteagent/v1/envelope_pb";
import type { PathAttempt } from "./transport/path";

type Probe = { probeRead(machine: string, path: "relay" | "direct"): Promise<void>; api: { forward(q: unknown): Promise<unknown> } };

async function answering(r: SyntheticRemote, seen: string[]) {
  (r as unknown as { api: unknown }).api = {
    forward: async (q: { command: Envelope }) => {
      const header = parseHeader(new Uint8Array(q.command.securityHeader));
      const opened = await r.world.fixture.open(r.machine, q.command);
      const command = fromBinary(CommandSchema, unpackSecurity(header, opened.plaintext));
      seen.push(command.kind.case ?? "");
      return { result: await r.seal({ kind: 6, requestId: header.request }, CommandResultSchema, create(CommandResultSchema)) };
    },
  };
}
const readMs = (r: SyntheticRemote) => r.direct.routing(r.machine).measured().readMs;

test("a relay warm-up sample sends a signed getMachineStatus and records its verified completion time", async () => {
  const r = await SyntheticRemote.create(), seen: string[] = [];
  try {
    await answering(r, seen);
    const probe = r as unknown as Probe;
    for (let i = 0; i < 3; i++) await probe.probeRead(r.machine, "relay");
    expect(seen).toEqual(["getMachineStatus", "getMachineStatus", "getMachineStatus"]);
    expect(readMs(r).relay).toBeGreaterThan(0);
    expect(readMs(r).direct).toBeUndefined();
  } finally { await r.dispose(); }
});

test("a failed relay sample is only a missing sample; it never demotes anything", async () => {
  const r = await SyntheticRemote.create();
  try {
    (r as unknown as { api: unknown }).api = { forward: async () => { throw new Error("offline"); } };
    const route = r.direct.routing(r.machine), fail = vi.spyOn(route, "fail");
    await (r as unknown as Probe).probeRead(r.machine, "relay");
    expect(fail).not.toHaveBeenCalled(); expect(readMs(r)).toEqual({});
  } finally { await r.dispose(); }
});

test("a direct warm-up sample goes through the direct attempt only, and a failed one demotes direct", async () => {
  const r = await SyntheticRemote.create(), seen: string[] = [];
  try {
    await answering(r, seen);
    const probe = r as unknown as Probe, forward = probe.api.forward.bind(probe.api);
    let fails = false, asked = 0;
    r.direct.attempt = (_id, _hint, _handoff, sample): PathAttempt => {
      expect(sample).toBe(true); asked++;
      const attemptId = "direct-attempt";
      return { direct: true, attemptId, prepare: async () => undefined, refusal: () => undefined, refreshAuth: async () => undefined,
        send: async (_machine, command) => { if (fails) throw new Error("path lost"); const res = await forward({ command, attemptId }) as { result: Envelope }; return create(ForwardResponseSchema, { result: res.result, attemptId }); } };
    };
    for (let i = 0; i < 3; i++) await probe.probeRead(r.machine, "direct");
    expect(asked).toBe(3); expect(seen).toEqual(["getMachineStatus", "getMachineStatus", "getMachineStatus"]);
    expect(readMs(r).direct).toBeGreaterThan(0);
    const fail = vi.spyOn(r.direct.routing(r.machine), "fail"); fails = true;
    await probe.probeRead(r.machine, "direct");
    expect(fail).toHaveBeenCalled();
  } finally { await r.dispose(); }
});

test("a sample on the path not asked for is dropped: no direct attempt means no direct sample, and nothing is sent", async () => {
  const r = await SyntheticRemote.create(), seen: string[] = [];
  try {
    await answering(r, seen);
    r.direct.attempt = () => undefined; // direct not ready
    await (r as unknown as Probe).probeRead(r.machine, "direct");
    expect(seen).toEqual([]); expect(readMs(r)).toEqual({});
  } finally { await r.dispose(); }
});
