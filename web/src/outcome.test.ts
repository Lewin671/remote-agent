import { expect, test } from "vitest";
import fixture from "../../testdata/transport/outcomes.json";
import { create, fromJson } from "@bufbuild/protobuf";
import { CommandSchema } from "./gen/remoteagent/v1/remote_pb";
import { readOnlyCommand, CommandError, uncertain } from "./remote";
import { stillUnknown } from "./attempt";
import { classifyOutcome } from "./outcome";
test.each(fixture.vectors)("outcome $source/$code signed=$signed prior=$prior readOnly=$readOnly", v => {
 expect(classifyOutcome(v)).toBe(v.outcome);
 // Unsigned daemon/server results are normalized by Remote.forward before
 // becoming CommandError. Exercise actual post-verification/local helpers.
 if (v.source === "local" || (v.source === "daemon" && v.signed)) {
   const error = new CommandError(v.code, "fixture", v.source === "daemon", v.readOnly);
   expect(stillUnknown(error, v.prior)).toBe(v.outcome === "unknown");
   if (!v.prior) expect(uncertain(error)).toBe(v.outcome === "unknown");
 }
});
test("protocol 14 coverage", () => {expect(fixture.protocol).toBe(14); expect(fixture.vectors).toHaveLength(552);});

test.each(fixture.commands)("command evidence $kind/$operation", v => {
 const body: Record<string,string> = v.kind === "schedule" ? {operation: ("operation" in v ? v.operation : "") ?? ""} : {};
 const cmd = v.kind ? fromJson(CommandSchema, {[v.kind]: body}) : create(CommandSchema);
 const readOnly = readOnlyCommand(cmd.kind);
 expect(readOnly).toBe(v.readOnly);
 for (const code of ["canceled", "deadline_exceeded", "aborted"])
  expect(stillUnknown(new CommandError(code, "fixture", true, readOnly), true)).toBe(!v.readOnly);
});
