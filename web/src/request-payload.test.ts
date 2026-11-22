import { test, expect } from "vitest";
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import fixture from "../../testdata/transport/prepared-command.json";
import { SyntheticRemote } from "./test/remote-fixture";
import { webMember } from "./test/security-fixture";
import { parseTestHeader, unpackTestBody } from "./test/stored-security-fixture";
import { CommandSchema, CommandResultSchema } from "./gen/remoteagent/v1/remote_pb";
import { type Envelope } from "./gen/remoteagent/v1/envelope_pb";
import { newSlot, restoreSlot } from "./remote";

const raw = Uint8Array.from(atob(fixture.plain), (c) => c.charCodeAt(0));

test("create retries and a fresh client preserve the first payload bytes across rekey", async () => {
  const r = await SyntheticRemote.create();
  let fresh: SyntheticRemote | undefined;
  try {
    const plain = fromBinary(CommandSchema, raw);
    expect(plain.kind.case).toBe("createSession");
    expect(toBinary(CommandSchema, plain)).not.toEqual(raw);
    const slot = restoreSlot({ ...newSlot(), prepared: { machineId: r.machine, sid: "", plain: raw.slice() } }, false);
    const bodies: Uint8Array[] = [];
    const epochs: bigint[] = [];
    function install(target: SyntheticRemote) {
      (target as unknown as { api: unknown }).api = {
        forward: async (q: { command: Envelope }) => {
          const h = parseTestHeader(new Uint8Array(q.command.securityHeader));
          const opened = await r.world.fixture.open(r.machine, q.command);
          bodies.push(unpackTestBody(h, opened.plaintext));
          epochs.push(h.contentEpoch);
          return { result: await target.seal({ kind: 6, requestId: h.request }, CommandResultSchema, create(CommandResultSchema)) };
        },
      };
    }
    install(r);
    await r.machineCommand(r.machine, plain.kind, slot);
    await webMember(r.world);
    await r.syncSecurityNow();
    await r.machineCommand(r.machine, { case: "createSession", value: { env: { different: "ignored" } } }, slot);
    fresh = new SyntheticRemote(r.world, r.member, r.seed);
    await fresh.bootstrapSecurity();
    install(fresh);
    const saved = JSON.parse(JSON.stringify({ ...slot, prepared: { ...slot.prepared, plain: Array.from(slot.prepared!.plain) } }));
    saved.prepared.plain = new Uint8Array(saved.prepared.plain);
    await fresh.machineCommand(r.machine, plain.kind, restoreSlot(saved, true));
    expect(bodies).toHaveLength(3);
    for (const b of bodies) expect(b).toEqual(raw);
    expect(epochs[1]).toBeGreaterThan(epochs[0]);
    // Fresh prepares freeze once too; mutating the caller's map cannot change a retry.
    const first = newSlot();
    const env = { z: "three", a: "one", m: "two" };
    await r.machineCommand(r.machine, { case: "createSession", value: { env } }, first);
    env.z = "changed";
    await r.machineCommand(r.machine, { case: "createSession", value: { env } }, first);
    expect(bodies[4]).toEqual(bodies[3]);
  } finally {
    fresh?.stop();
    await r.dispose();
  }
});
