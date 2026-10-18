import { fromBinary } from "@bufbuild/protobuf";
import { expect, it } from "vitest";
import vectors from "../../internal/e2e/testdata/pair-approval-vectors.json";
import { concat, fromHex, hex, utf8 } from "./crypto";
import { PairApprovalSchema } from "./gen/remoteagent/v1/relay_pb";
import { pairIdentityTranscript } from "./pair";

it("verifies Go's device approval and binds the candidate identity into the SAS", async () => {
  const raw = fromHex(vectors.approval), sig = fromHex(vectors.signature);
  const a = fromBinary(PairApprovalSchema, raw);
  const key = await crypto.subtle.importKey("raw", a.approverPublicKey as Uint8Array<ArrayBuffer>, "Ed25519", false, ["verify"]);
  const signed = concat(utf8("remote-agent/v1/pair-approval\0"), raw);
  expect(await crypto.subtle.verify("Ed25519", key, sig, signed)).toBe(true);
  signed[signed.length - 1] ^= 1;
  expect(await crypto.subtle.verify("Ed25519", key, sig, signed)).toBe(false);
  const pub = fromHex(vectors.candidate_public);
  const transcript = await pairIdentityTranscript(vectors.pairing_id, fromHex(vectors.initiator_public), fromHex(vectors.joiner_public), pub, 2);
  expect(hex(transcript)).toBe(vectors.transcript);
  pub[0] ^= 1;
  expect(hex(await pairIdentityTranscript(vectors.pairing_id, fromHex(vectors.initiator_public), fromHex(vectors.joiner_public), pub, 2))).not.toBe(vectors.transcript);
});
