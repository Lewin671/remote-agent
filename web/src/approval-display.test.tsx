// @vitest-environment happy-dom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { create, toBinary } from "@bufbuild/protobuf";
import { expect, it, vi } from "vitest";
import { approvalDisplay } from "./approval-display";
import { ApprovalRequestedSchema, Agent } from "./gen/remoteagent/v1/event_pb";
import { CommandResultSchema } from "./gen/remoteagent/v1/remote_pb";
import { DemoRemote } from "./dev/demo";
import { ApprovalPage, approvalText, approvalTitle } from "./views/Dock";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

it("reveals approval controls while preserving ordinary language and emoji", () => {
  const text = "العربية 中文 👩‍💻\n\tcode";
  expect(approvalDisplay(text)).toBe(text);
  expect(approvalDisplay("\u061c\u200e\u200f\u202e\u2066\u2069\r\u001b\u009b"))
    .toBe("\\u061c\\u200e\\u200f\\u202e\\u2066\\u2069\\u000d\\u001b\\u009b");
});

it("shows safe approval text without changing the authenticated event or decision", async () => {
  const a = create(ApprovalRequestedSchema, { approvalId: "security-approval", command: "printf '\u202e'; dangerous", description: "desc\u2066hidden\u2069", toolName: "Bash\u200f" });
  const before = toBinary(ApprovalRequestedSchema, a);
  expect(approvalText(a)).toBe("printf '\\u202e'; dangerous");
  expect(approvalTitle({ ...a, command: "" })).toBe("请求权限 · Bash\\u200f");
  const remote = DemoRemote.create(new URLSearchParams());
  remote.start();
  const sid = "s-appr";
  remote.machines.get(remote.sessions.get(sid)!.machineId)!.meta!.capabilities!.approvalAlways = false;
  const command = vi.spyOn(remote, "command").mockResolvedValue(create(CommandResultSchema));
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  try {
    act(() => root.render(<ApprovalPage remote={remote} sid={sid} a={a} agent={Agent.CODEX} title="session" onBack={() => undefined} />));
    expect(host.querySelector("pre")!.textContent).toBe(approvalText(a));
    expect(host.textContent).toContain("desc\\u2066hidden\\u2069");
    expect(host.textContent).not.toContain("以后不再问");
    await act(async () => [...host.querySelectorAll("button")].find(b => b.textContent === "允许")!.click());
    expect(command.mock.calls[0][1]).toEqual({ case: "resolveApproval", value: { sessionId: sid, approvalId: a.approvalId, allow: true, message: "", always: false } });
    expect(toBinary(ApprovalRequestedSchema, a)).toEqual(before);
  } finally {
    act(() => root.unmount());
    host.remove();
    remote.stop();
  }
});
