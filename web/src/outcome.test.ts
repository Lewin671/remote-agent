import { expect, test } from "vitest";
import fixture from "../../testdata/transport/outcomes.json";
import { create, fromJson } from "@bufbuild/protobuf";
import { CommandSchema } from "./gen/remoteagent/v1/remote_pb";
import { readOnlyCommand, CommandError, uncertain } from "./remote";
import { stillUnknown } from "./attempt";
import { classifyOutcome, type Outcome } from "./outcome";
test.each(fixture.vectors)("outcome $source/$code signed=$signed prior=$prior readOnly=$readOnly", v => {
 expect(classifyOutcome(v)).toBe(v.outcome);
 const category=v.outcome as Outcome;
 const error=new CommandError(v.code,"fixture",v.source==="daemon"&&v.signed,v.readOnly,category,category==="conflict"?v.conflictKind:"",v.resultAvailable);
 const frozen=category==="unknown" || category==="conflict"&&v.conflictKind==="identity" || category==="executed"&&!v.resultAvailable;
 expect(uncertain(error)).toBe(frozen);
 expect(stillUnknown(error,v.prior)).toBe(frozen);

});
test("protocol 15 coverage", () => {expect(fixture.protocol).toBe(15); expect(fixture.vectors).toHaveLength(3432);});

test.each(fixture.commands)("command evidence $kind/$operation", v => {
 const body: Record<string,string> = v.kind === "schedule" ? {operation: ("operation" in v ? v.operation : "") ?? ""} : {};
 const cmd = v.kind ? fromJson(CommandSchema, {[v.kind]: body}) : create(CommandSchema);
 const readOnly = readOnlyCommand(cmd.kind);
 expect(readOnly).toBe(v.readOnly);
 for (const code of ["canceled", "deadline_exceeded", "aborted"])
  expect(stillUnknown(new CommandError(code, "fixture", true, readOnly,"executed"), true)).toBe(false);
});
