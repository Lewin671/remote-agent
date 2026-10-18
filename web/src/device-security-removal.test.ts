import { describe, it, expect } from "vitest";
import vector from "../../internal/deviceauth/testdata/revocation-v2.json";
import { parseState, pinFromJSON, unb64 } from "./device-security";
import { verifyCompletion, type Completion } from "./device-security-removal";
describe("revocation completion proof shared with Go", () => {
  it("requires the exact final epoch and every retained executor receipt", async () => {
    const pin = pinFromJSON(vector.Pin);
    const chain = await Promise.all(vector.Chain.map(unb64).map(parseState));
    const completion = vector.Completion as Completion;
    await verifyCompletion(pin, completion, vector.Original, chain);
    const bad = structuredClone(completion);
    bad.Group.Receipts![0].Revision--;
    await expect(
      verifyCompletion(pin, bad, vector.Original, chain),
    ).rejects.toThrow();
    const missing = structuredClone(completion);
    missing.Group.Receipts = [];
    await expect(
      verifyCompletion(pin, missing, vector.Original, chain),
    ).rejects.toThrow();
  });
  it("does not use an unrelated operation or a skipped chain as success", async () => {
    const pin = pinFromJSON(vector.Pin);
    const chain = await Promise.all(vector.Chain.map(unb64).map(parseState));
    const other = { ...vector.Original, Operation: "unrelated-operation" };
    await expect(
      verifyCompletion(pin, vector.Completion as Completion, other, chain),
    ).rejects.toThrow();
    await expect(
      verifyCompletion(
        pin,
        vector.Completion as Completion,
        vector.Original,
        chain.slice(1),
      ),
    ).rejects.toThrow();
  });
});
