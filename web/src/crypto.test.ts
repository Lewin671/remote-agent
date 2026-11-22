import { create, toBinary } from "@bufbuild/protobuf";
import { describe, expect as want, it } from "vitest";
import { EnvelopeSchema, HeaderSchema, PayloadKind } from "./gen/remoteagent/v1/envelope_pb";
import {
  checkServer,
  concat,
  fromHex,
  hex,
  newRequestID,
  rejectRetiredHistory,
  UnsupportedHistoryError,
  utf8,
} from "./crypto";

describe("web crypto helpers", () => {
  it("encodes hex, utf8, concat, and random request IDs", () => {
    const raw = utf8("hello");
    const joined = concat(raw, new Uint8Array([0, 255]));
    want(fromHex(hex(joined))).toEqual(joined);
    want(newRequestID()).toMatch(/^[0-9a-f]{32}$/);
  });

  it("enforces HTTPS except local origins in checkServer", () => {
    want(checkServer("https://relay.example.com/")).toBe("https://relay.example.com");
    want(checkServer("http://127.0.0.1:8080")).toBe("http://127.0.0.1:8080");
    want(() => checkServer("http://relay.example.com")).toThrow();
    want(() => checkServer("https://user:pass@relay.example.com")).toThrow();
  });

  it("rejects v1 retired history envelopes cleanly", () => {
    const v1Env = create(EnvelopeSchema, {
      header: toBinary(HeaderSchema, create(HeaderSchema, { version: 1, kind: PayloadKind.EVENT })),
    });
    const v3Env = create(EnvelopeSchema, {
      header: toBinary(HeaderSchema, create(HeaderSchema, { version: 3, kind: PayloadKind.EVENT })),
    });
    want(() => rejectRetiredHistory(v1Env)).toThrow(UnsupportedHistoryError);
    want(() => rejectRetiredHistory(v3Env)).not.toThrow();
    want(() => rejectRetiredHistory(undefined)).not.toThrow();
  });
});