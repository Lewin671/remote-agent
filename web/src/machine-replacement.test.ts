import { create } from "@bufbuild/protobuf";
import { expect, it } from "vitest";
import vectors from "../../internal/e2e/testdata/machine-replacement-vectors.json";
import { MachineReplacementProofSchema } from "./gen/remoteagent/v1/local_pb";
import { buildRecord } from "./device-security-store";
import { parseState, pinOf } from "./device-security";
import { machineReplacementOrigins } from "./machine-replacement";
import { concat } from "./crypto";
const raw = (s: string) =>
  new Uint8Array(Uint8Array.from(atob(s), (c) => c.charCodeAt(0)));
it("accepts successive authority-approved replacements and rejects missing, forged or misrouted proofs", async () => {
  const states = await Promise.all(
    vectors.states.map((s) => parseState(raw(s))),
  );
  const x = await crypto.subtle.importKey(
    "pkcs8",
    concat(
      new Uint8Array([
        48, 46, 2, 1, 0, 48, 5, 6, 3, 43, 101, 110, 4, 34, 4, 32,
      ]),
      raw(vectors.x),
    ),
    "X25519",
    false,
    ["deriveBits"],
  );
  const record = await buildRecord(
    await pinOf(states[0]),
    states,
    states.map(() => undefined),
    vectors.target,
    x,
  );
  const proofs = vectors.proofs.map((p) =>
    create(MachineReplacementProofSchema, {
      approval: raw(p.approval),
      signature: raw(p.signature),
    }),
  );
  const variant = (ps: typeof vectors.proofs) =>
    ps.map((p) =>
      create(MachineReplacementProofSchema, {
        approval: raw(p.approval),
        signature: raw(p.signature),
      }),
    );
  expect(
    (
      await machineReplacementOrigins(
        record,
        vectors.target,
        variant(vectors.clock_skew_proofs),
      )
    ).has(vectors.old),
  ).toBe(true);
  await expect(
    machineReplacementOrigins(
      record,
      vectors.target,
      variant(vectors.expired_proofs),
    ),
  ).rejects.toThrow();
  expect(
    (await machineReplacementOrigins(record, vectors.target, proofs)).has(
      vectors.old,
    ),
  ).toBe(true);
  expect(
    (await machineReplacementOrigins(record, vectors.target, [])).has(
      vectors.old,
    ),
  ).toBe(false);
  const bad = proofs.map((p) =>
    create(MachineReplacementProofSchema, {
      approval: p.approval,
      signature: new Uint8Array(p.signature),
    }),
  );
  bad[0].signature[0] ^= 1;
  await expect(
    machineReplacementOrigins(record, vectors.target, bad),
  ).rejects.toThrow();
  await expect(
    machineReplacementOrigins(record, vectors.old, proofs),
  ).rejects.toThrow();
});
