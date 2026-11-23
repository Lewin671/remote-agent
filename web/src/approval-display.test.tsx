import { t as message } from "./i18n";
// @vitest-environment happy-dom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { create, toBinary } from "@bufbuild/protobuf";
import { expect, it, vi } from "vitest";
import { approvalTruncatedMessage, approvalDisplay } from "./approval-display";
import { ApprovalRequestedSchema } from "./gen/remoteagent/v1/event_pb";
import {
  SessionApprovalDockViewSchema,
  SessionCommandStateSchema,
  SessionCommandStatus,
  SessionDockKind,
  SessionDockViewSchema,
} from "./gen/remoteagent/core/v1/session_view_pb";
import { TextSchema } from "./gen/remoteagent/core/v1/text_pb";
import { ApprovalPage } from "./views/Dock";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

it("reveals approval controls while preserving ordinary language and emoji", () => {
  const text = "العربية 中文 👩‍💻\n\tcode";
  expect(approvalDisplay(text)).toBe(text);
  expect(approvalDisplay("\u061c\u200e\u200f\u202e\u2066\u2069\r\u001b\u009b"))
    .toBe("\\u061c\\u200e\\u200f\\u202e\\u2066\\u2069\\u000d\\u001b\\u009b");
});

it("shows safe approval text without mutating the approval or decision command", async () => {
  const a = create(ApprovalRequestedSchema, { approvalId: "security-approval", command: "printf '\u202e'; dangerous", description: "desc\u2066hidden\u2069", toolName: "Bash\u200f" });
  const before = toBinary(ApprovalRequestedSchema, a);
  const approval = create(SessionApprovalDockViewSchema, {
    approvalId: a.approvalId,
    toolName: a.toolName,
    commandPreview: a.command,
    body: a.command,
    description: a.description,
    canAlwaysAllow: false,
  });
  const dock = create(SessionDockViewSchema, {
    hasItem: true,
    kind: SessionDockKind.APPROVAL,
    itemId: a.approvalId,
    observedSeq: 5n,
    title: create(TextSchema, { key: "list_state.android_request_command" }),
    command: create(SessionCommandStateSchema, {
      slotKey: "approval:s-appr:" + a.approvalId,
      status: SessionCommandStatus.IDLE,
    }),
    detail: { case: "approval", value: approval },
  });
  const onSubmitCommand = vi.fn();
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  try {
    act(() =>
      root.render(
        <ApprovalPage
          sessionId="s-appr"
          dock={dock}
          approval={approval}
          title="session"
          onBack={() => undefined}
          onSubmitCommand={onSubmitCommand}
          onAbandonCommand={() => undefined}
        />,
      ),
    );
    expect(host.querySelector("pre")!.textContent).toBe("❯ " + approvalDisplay(a.command));
    expect(host.textContent).toContain("desc\\u2066hidden\\u2069");
    expect(host.textContent).not.toContain("以后不再问");
    act(() => [...host.querySelectorAll("button")].find((b) => b.textContent === message("approval.allow"))!.click());
    expect(onSubmitCommand).toHaveBeenCalledTimes(1);
    expect(onSubmitCommand.mock.calls[0][1].kind).toEqual({
      case: "resolveApproval",
      value: expect.objectContaining({
        sessionId: "s-appr",
        approvalId: a.approvalId,
        allow: true,
        message: "",
        always: false,
        observedEventSeq: 5n,
      }),
    });
    expect(toBinary(ApprovalRequestedSchema, a)).toEqual(before);
  } finally {
    act(() => root.unmount());
    host.remove();
  }
});

it.each(["command", "description", "toolInputJson"] as const)("blocks allow and allow-always when %s was truncated", async (field) => {
  const a = create(ApprovalRequestedSchema, { approvalId: "oversized", command: "safe prefix", [field]: "cut\n…（remote-agent：内容过长，已截断）" });
  const approval = create(SessionApprovalDockViewSchema, {
    approvalId: a.approvalId,
    toolName: "Bash",
    commandPreview: a.command,
    body: a.command,
    bodyTruncated: true,
    canAlwaysAllow: true,
    alwaysLabel: create(TextSchema, { key: "approval.always_allow" }),
  });
  const dock = create(SessionDockViewSchema, {
    hasItem: true,
    kind: SessionDockKind.APPROVAL,
    itemId: a.approvalId,
    observedSeq: 2n,
    title: create(TextSchema, { key: "approval.title" }),
    command: create(SessionCommandStateSchema, {
      slotKey: "approval:s-appr:" + a.approvalId,
      status: SessionCommandStatus.IDLE,
    }),
    detail: { case: "approval", value: approval },
  });
  const onSubmitCommand = vi.fn();
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  try {
    act(() =>
      root.render(
        <ApprovalPage
          sessionId="s-appr"
          dock={dock}
          approval={approval}
          title="session"
          onBack={() => undefined}
          onSubmitCommand={onSubmitCommand}
          onAbandonCommand={() => undefined}
        />,
      ),
    );
    expect(host.textContent).toContain(approvalTruncatedMessage());
    for (const label of ["允许", "允许，这类请求以后不再问"]) {
      const button = [...host.querySelectorAll("button")].find((b) => b.textContent === label)!;
      expect(button.disabled).toBe(true);
      act(() => button.click());
    }
    expect(onSubmitCommand).not.toHaveBeenCalled();
    // Refusing remains possible without approving an incomplete request.
    act(() => [...host.querySelectorAll("button")].find((b) => b.textContent === message("approval.deny"))!.click());
    act(() => [...host.querySelectorAll("button")].find((b) => b.textContent === message("approval.deny"))!.click());
    expect(onSubmitCommand.mock.calls[0][1].kind).toMatchObject({
      case: "resolveApproval",
      value: { allow: false, approvalId: a.approvalId },
    });
  } finally {
    act(() => root.unmount());
    host.remove();
  }
});
