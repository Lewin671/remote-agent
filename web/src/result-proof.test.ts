import { expect, test } from "vitest";
import { fromBinary } from "@bufbuild/protobuf";
import fixture from "../../testdata/transport/proofs.json";
import domain from "../../internal/deviceauth/testdata/domain-v3.json";
import { EnvelopeSchema } from "./gen/remoteagent/v1/envelope_pb";
import { CommandResultSchema } from "./gen/remoteagent/v1/remote_pb";
import { openWire, fromEnvelope, parseState, stateHash, requestIdentity } from "./device-security";
import { fromHex, hex } from "./crypto";

const decode = (s: string) => Uint8Array.from(atob(s), (c) => c.charCodeAt(0));
const eq = (a: Uint8Array, b: Uint8Array) => a.length === b.length && a.every((v, i) => v === b[i]);

test("Go signed result proofs and original payload bindings", async () => {
  const state = await parseState(decode(fixture.State));
  const genesis = await parseState(decode(domain.genesis.state));
  const pin = { account: state.core.account, domain: state.core.domain, genesis: await stateHash(genesis) };
  const open = async (s: string) =>
    openWire(pin, state, state, fromEnvelope(fromBinary(EnvelopeSchema, decode(s))), fromHex(fixture.Secret), false, 0n);
  const request = await open(fixture.Command);
  const binding = await requestIdentity(request.header, request.plaintext);
  expect(hex(binding.key)).toBe(fixture.Key);
  expect(hex(binding.payload)).toBe(fixture.Payload);
  for (const v of fixture.Vectors) {
    let result = "unknown";
    try {
      const opened = await open(v.Envelope);
      const msg = fromBinary(CommandResultSchema, opened.plaintext);
      const hashesMatch =
        binding.key.length === 32 &&
        binding.payload.length === 32 &&
        eq(msg.requestKeyHash, binding.key) &&
        eq(msg.payloadHash, binding.payload) &&
        !!msg.reason &&
        (msg.outcome === "conflict" || msg.conflictKind === "") &&
        (msg.resultAvailable || msg.kind.case === undefined) &&
        (!msg.resultAvailable || msg.outcome === "executed" || (msg.outcome === "conflict" && msg.conflictKind === "business"));
      const outerMatches = v.OuterOutcome === msg.outcome && v.OuterReason === msg.reason;
      const validOutcome =
        msg.outcome === "executed" ||
        (msg.outcome === "conflict" && (msg.conflictKind === "business" || msg.conflictKind === "identity")) ||
        (msg.outcome === "rejected" &&
          !msg.resultAvailable &&
          ["invalid_envelope", "invalid_body", "invalid_scope", "protocol_mismatch", "member_removed"].includes(msg.reason));
      if (hashesMatch && outerMatches && validOutcome) {
        result = msg.outcome;
      }
    } catch {
      // Invalid envelope or signature keeps outcome unknown.
    }
    expect(result, v.Name).toBe(v.Expected);
  }
});
