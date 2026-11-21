import { create } from "@bufbuild/protobuf";
import type { MachineReplacementProof } from "./gen/remoteagent/v1/local_pb";
import { SecMachineReplacementOriginsCallSchema } from "./gen/remoteagent/core/v1/seccall_pb";
import type { SecurityRecord } from "./device-security-store";
import { stateBytes } from "./device-security";
import { secCall } from "./core/sec-call";

// A historical-origin proof never authorizes live control as the retired ID.
export async function machineReplacementOrigins(
  record: SecurityRecord,
  target: string,
  proofs: MachineReplacementProof[],
) {
  const resp = secCall({
    case: "machineReplacementOrigins",
    value: create(SecMachineReplacementOriginsCallSchema, {
      account: record.pin.account,
      targetMachineId: target,
      states: await Promise.all(record.states.map((s) => stateBytes(s))),
      proofs,
    }),
  });
  return new Set(resp.strings);
}
