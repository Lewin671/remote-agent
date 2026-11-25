import { t as message } from "./i18n";
// @vitest-environment happy-dom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { create } from "@bufbuild/protobuf";
import { expect, it, vi } from "vitest";
import {
  SessionCommandStateSchema,
  SessionCommandStatus,
} from "./gen/remoteagent/core/v1/home_view_pb";
import {
  SessionApprovalDockViewSchema,
  SessionDockKind,
  SessionDockViewSchema,
} from "./gen/remoteagent/core/v1/session_view_pb";
import { TextSchema } from "./gen/remoteagent/core/v1/text_pb";
import { ApprovalPage } from "./views/Dock";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

it("renders core-escaped approval text and submits allow decision with observed seq", async () => {
  const approval = create(SessionApprovalDockViewSchema, {
    approvalId: "security-approval",
    toolName: "Bash\\u200f",
    commandPreview: "printf '\\u202e'; dangerous",
    body: "printf '\\u202e'; dangerous",
    description: "desc\\u2066hidden\\u2069",
    canAlwaysAllow: false,
  });
  const dock = create(SessionDockViewSchema, {
    hasItem: true,
    kind: SessionDockKind.APPROVAL,
    itemId: approval.approvalId,
    observedSeq: 5n,
    title: create(TextSchema, { key: "list_state.request_command_plain" }),
    command: create(SessionCommandStateSchema, {
      slotKey: "approval:s-appr:" + approval.approvalId,
      status: SessionCommandStatus.IDLE,
    }),
    detail: { case: "approval", value: approval },
  });
  const onResolveApproval = vi.fn();
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  try {
    act(() =>
      root.render(
        <ApprovalPage
          dock={dock}
          approval={approval}
          title="session"
          onBack={() => undefined}
          onResolveApproval={onResolveApproval}
          onAbandonCommand={() => undefined}
        />,
      ),
    );
    expect(host.querySelector("pre")!.textContent).toBe("❯ " + approval.body);
    expect(host.textContent).toContain("desc\\u2066hidden\\u2069");
    expect(host.textContent).not.toContain("以后不再问");
    act(() => [...host.querySelectorAll("button")].find((b) => b.textContent === message("approval.allow"))!.click());
    expect(onResolveApproval).toHaveBeenCalledTimes(1);
    expect(onResolveApproval).toHaveBeenCalledWith(approval.approvalId, true, false, "");
  } finally {
    act(() => root.unmount());
    host.remove();
  }
});

it("blocks allow and allow-always when approval payload was truncated", async () => {
  const approval = create(SessionApprovalDockViewSchema, {
    approvalId: "oversized",
    toolName: "Bash",
    commandPreview: "safe prefix",
    body: "safe prefix",
    bodyTruncated: true,
    canAlwaysAllow: true,
    alwaysLabel: create(TextSchema, { key: "approval.always_allow" }),
  });
  const dock = create(SessionDockViewSchema, {
    hasItem: true,
    kind: SessionDockKind.APPROVAL,
    itemId: approval.approvalId,
    observedSeq: 2n,
    title: create(TextSchema, { key: "approval.title" }),
    command: create(SessionCommandStateSchema, {
      slotKey: "approval:s-appr:" + approval.approvalId,
      status: SessionCommandStatus.IDLE,
    }),
    detail: { case: "approval", value: approval },
  });
  const onResolveApproval = vi.fn();
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  try {
    act(() =>
      root.render(
        <ApprovalPage
          dock={dock}
          approval={approval}
          title="session"
          onBack={() => undefined}
          onResolveApproval={onResolveApproval}
          onAbandonCommand={() => undefined}
        />,
      ),
    );
    expect(host.textContent).toContain(message("approval.truncated"));
    for (const label of ["允许", "允许，这类请求以后不再问"]) {
      const button = [...host.querySelectorAll("button")].find((b) => b.textContent === label)!;
      expect(button.disabled).toBe(true);
      act(() => button.click());
    }
    expect(onResolveApproval).not.toHaveBeenCalled();
    // Refusing remains possible without approving an incomplete request.
    act(() => [...host.querySelectorAll("button")].find((b) => b.textContent === message("approval.deny"))!.click());
    act(() => [...host.querySelectorAll("button")].find((b) => b.textContent === message("approval.deny"))!.click());
    expect(onResolveApproval).toHaveBeenCalledWith(approval.approvalId, false, false, "");
  } finally {
    act(() => root.unmount());
    host.remove();
  }
});
