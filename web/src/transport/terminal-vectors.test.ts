import { describe, expect, it } from "vitest";
import { fromBinary, toBinary } from "@bufbuild/protobuf";
import {
  AckSchema,
  AttachPathSchema,
  BarrierReplySchema,
  BarrierSchema,
  ClosePathSchema,
  InputStatusReplySchema,
  PeerInputStatusSchema,
  ReplaySchema,
  StreamFailedSchema,
} from "../gen/remoteagent/v1/transport_pb";
import vectors from "../../../testdata/transport/terminal/control-vectors.json";

function hexToBytes(hex: string): Uint8Array {
  const out = new Uint8Array(hex.length / 2);
  for (let i = 0; i < out.length; i++) {
    out[i] = Number.parseInt(hex.slice(i * 2, i * 2 + 2), 16);
  }
  return out;
}

const td = new TextDecoder();

function hasUnknownFields(msg: unknown): boolean {
  const unknown = (msg as { $unknown?: unknown[] }).$unknown;
  return Array.isArray(unknown) && unknown.length > 0;
}

describe("Phase 3 terminal control vectors (shared across Go/Web/Android/iOS)", () => {
  it("decodes every valid control vector with zero unknown fields and rejects invalid vectors", () => {
    expect(vectors.controls.length).toBeGreaterThanOrEqual(11);
    for (const v of vectors.controls) {
      const raw = hexToBytes(v.wire_hex);
      let ok = true;
      let stream = "";
      switch (v.message) {
        case "AttachPath": {
          const msg = fromBinary(AttachPathSchema, raw);
          ok =
            !hasUnknownFields(msg) &&
            msg.stream.length >= 16 &&
            msg.session.length >= 16 &&
            msg.sender.length === 32 &&
            msg.legId.length === 16 &&
            msg.pathGeneration >= 1n &&
            msg.state !== undefined;
          stream = td.decode(msg.stream);
          if (v.valid) {
            expect(toBinary(AttachPathSchema, msg).length).toBeGreaterThan(0);
          }
          break;
        }
        case "InputStatus": {
          const msg = fromBinary(PeerInputStatusSchema, raw);
          ok =
            !hasUnknownFields(msg) &&
            msg.stream.length >= 16 &&
            msg.queryId.length === 16 &&
            msg.fromSeq >= 1n &&
            msg.limit >= 1 &&
            msg.limit <= 128;
          stream = td.decode(msg.stream);
          break;
        }
        case "InputStatusReply": {
          const msg = fromBinary(InputStatusReplySchema, raw);
          ok =
            !hasUnknownFields(msg) &&
            msg.stream.length >= 16 &&
            msg.queryId.length === 16 &&
            (!msg.available || (msg.snapshotToken.length === 32 && msg.state !== undefined));
          stream = td.decode(msg.stream);
          break;
        }
        case "Ack": {
          const msg = fromBinary(AckSchema, raw);
          ok =
            !hasUnknownFields(msg) &&
            msg.stream.length >= 16 &&
            msg.direction !== 0 &&
            msg.state !== undefined &&
            msg.acknowledgedSha256.length === 0;
          stream = td.decode(msg.stream);
          break;
        }
        case "Replay": {
          const msg = fromBinary(ReplaySchema, raw);
          ok =
            !hasUnknownFields(msg) &&
            msg.stream.length >= 16 &&
            msg.direction !== 0 &&
            msg.fromSeq >= 1n &&
            msg.maxBytes >= 1 &&
            msg.maxBytes <= 1 << 20 &&
            msg.maxFrames >= 1 &&
            msg.maxFrames <= 128;
          stream = td.decode(msg.stream);
          break;
        }
        case "Barrier": {
          const msg = fromBinary(BarrierSchema, raw);
          ok =
            !hasUnknownFields(msg) &&
            msg.stream.length >= 16 &&
            msg.barrierId.length === 16 &&
            (msg.stopAdmission || (msg.newLegId.length === 16 && msg.pathGeneration >= 1n));
          stream = td.decode(msg.stream);
          break;
        }
        case "BarrierReply": {
          const msg = fromBinary(BarrierReplySchema, raw);
          ok =
            !hasUnknownFields(msg) &&
            msg.stream.length >= 16 &&
            msg.barrierId.length === 16 &&
            (!msg.final || msg.accepted) &&
            (!msg.accepted || msg.state !== undefined);
          stream = td.decode(msg.stream);
          break;
        }
        case "StreamFailed": {
          const msg = fromBinary(StreamFailedSchema, raw);
          ok =
            !hasUnknownFields(msg) &&
            msg.stream.length >= 16 &&
            msg.inputFailure !== undefined &&
            msg.state !== undefined;
          stream = td.decode(msg.stream);
          break;
        }
        case "ClosePath": {
          const msg = fromBinary(ClosePathSchema, raw);
          ok =
            !hasUnknownFields(msg) &&
            msg.stream.length >= 16 &&
            msg.legId.length === 16 &&
            msg.pathGeneration >= 1n;
          stream = td.decode(msg.stream);
          break;
        }
        default:
          throw new Error(`unexpected control vector message ${v.message}`);
      }
      expect(ok, v.name).toBe(v.valid);
      if (v.valid && v.stream) {
        expect(stream, v.name).toBe(v.stream);
      }
    }
  });

  it("validates 17-byte liveness probe vectors", () => {
    expect(vectors.probes.length).toBeGreaterThanOrEqual(3);
    for (const p of vectors.probes) {
      const raw = hexToBytes(p.wire_hex);
      const valid = raw.length === 17 && (raw[0] === 0 || raw[0] === 1);
      expect(valid, p.name).toBe(p.valid);
      if (p.valid) {
        expect(raw[0], p.name).toBe(p.kind);
      }
    }
  });
});
