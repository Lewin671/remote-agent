import { Code, ConnectError } from "@connectrpc/connect";
import { expect, it } from "vitest";
import { joinBanner, joinExistingLead, joinLead } from "./Pairing";

it("tells a release mismatch apart from a wrong code", () => {
  const mismatch = new ConnectError("the joining device speaks protocol version 9 and this remote-agent version 10; update both to the same release", Code.FailedPrecondition);
  expect(joinBanner(mismatch).text).toMatch(/版本不同/);
  expect(joinBanner(new ConnectError("no such pairing, or it expired", Code.NotFound)).text).toBe("配对码不对，或者已经用过了");
  expect(joinBanner(new ConnectError("already used", Code.FailedPrecondition)).text).toBe("配对码不对，或者已经用过了");
});


it("an expired-or-canceled pairing does not accuse the other device of refusing", () => {
  expect(joinBanner(new Error("配对已过期或被取消"))).toEqual({
    kind: "err",
    text: "这次配对已失效，请在管理端手机上重新生成配对码",
    action: "重填",
  });
  expect(joinBanner(new Error("管理端手机取消了配对")).text).toBe("对方拒绝了这次加入");
});

it("tells a browser to type the code the phone shows, never to scan", () => {
  expect(joinLead).toBe("在管理端手机上打开「设置 › 添加设备」，把它显示的 8 位配对码填在这里。");
  for (const lead of [joinLead, joinExistingLead]) {
    expect(lead).toContain("在管理端手机上打开「设置 › 添加设备」，把它显示的 8 位配对码填在这里");
    expect(lead).toContain("8 位配对码");
    expect(lead).not.toMatch(/扫|二维码/);
  }
});
