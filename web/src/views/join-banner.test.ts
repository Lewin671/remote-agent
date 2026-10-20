import { Code, ConnectError } from "@connectrpc/connect";
import { expect, it } from "vitest";
import { joinBanner } from "./Pairing";

it("tells a release mismatch apart from a wrong code", () => {
  const mismatch = new ConnectError("the joining device speaks protocol version 9 and this remote-agent version 10; update both to the same release", Code.FailedPrecondition);
  expect(joinBanner(mismatch).text).toMatch(/版本不同/);
  expect(joinBanner(new ConnectError("no such pairing, or it expired", Code.NotFound)).text).toBe("配对码不对，或者已经用过了");
  expect(joinBanner(new ConnectError("already used", Code.FailedPrecondition)).text).toBe("配对码不对，或者已经用过了");
});
