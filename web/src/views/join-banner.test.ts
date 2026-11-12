import { t as message } from "../i18n";
import { PairingStatusError } from "../pairing";
import { Code, ConnectError } from "@connectrpc/connect";
import { expect, it } from "vitest";
import { joinBanner, joinExistingLead, joinLead } from "./Pairing";

it("tells a release mismatch apart from a wrong code", () => {
  const mismatch = new ConnectError("the joining device speaks protocol version 9 and this remote-agent version 10; update both to the same release", Code.FailedPrecondition);
  expect(joinBanner(mismatch).text).toMatch(/版本不同/);
  expect(joinBanner(new ConnectError("no such pairing, or it expired", Code.NotFound)).text).toBe(message("security.the_pairing_code_is_wrong_or_has_already_been"));
  expect(joinBanner(new ConnectError("already used", Code.FailedPrecondition)).text).toBe(message("security.the_pairing_code_is_wrong_or_has_already_been"));
});


it("an expired-or-canceled pairing does not accuse the other device of refusing", () => {
  expect(joinBanner(new PairingStatusError("expired_or_canceled"))).toEqual({
    kind: "err",
    text: message("security.this_pairing_expired_generate_a_new_code_on_the"),
    action: "refill",
  });
  expect(joinBanner(new PairingStatusError("refused")).text).toBe(message("security.the_other_device_refused_this_join_request"));
});

it("tells a browser to type the code the phone shows, never to scan", () => {
  expect(joinLead()).toBe(message("security.on_the_managing_client_open_settings_add_device_and"));
  for (const lead of [joinLead(), joinExistingLead()]) {
    expect(lead).toContain("在管理端打开「设置 › 添加设备」，把它显示的 8 位配对码填在这里");
    expect(lead).toContain("8 位配对码");
    expect(lead).not.toMatch(/扫|二维码/);
  }
});
