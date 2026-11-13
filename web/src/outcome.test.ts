import { expect, test } from "vitest";
import fixture from "../../testdata/transport/outcomes.json";
import { CommandError } from "./remote";
import { stillUnknown } from "./attempt";
import { classifyOutcome } from "./outcome";
test.each(fixture.vectors)("outcome $source/$code signed=$signed prior=$prior readOnly=$readOnly", v => {
 expect(classifyOutcome(v)).toBe(v.outcome);
 // Unsigned daemon/server results are normalized by Remote.forward before
 // becoming CommandError. Exercise actual post-verification/local helpers.
 if (v.source === "local" || (v.source === "daemon" && v.signed))
   expect(stillUnknown(new CommandError(v.code, "fixture", v.source === "daemon", v.readOnly), v.prior)).toBe(v.outcome === "unknown");
});
test("protocol 14 coverage", () => {expect(fixture.protocol).toBe(14); expect(fixture.vectors).toHaveLength(504);});
