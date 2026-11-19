import { t as message } from "../i18n";
// @vitest-environment happy-dom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import {
  SessionPendingTrayViewSchema,
  SessionTrayGroupKind,
  SessionTrayGroupViewSchema,
  SessionTrayItemViewSchema,
  SessionTrayRowActivity,
  SessionTrayTone,
  SessionTrayUndoViewSchema,
} from "../gen/remoteagent/core/v1/session_view_pb";
import { TextSchema } from "../gen/remoteagent/core/v1/text_pb";
import { SendMode } from "../gen/remoteagent/v1/event_pb";
import { PendingTray } from "./Queue";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | undefined;
let host: HTMLElement;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = undefined;
});

function render(node: React.ReactNode) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(node));
}

it("renders pending tray groups, expands rows for promote/restore, and forwards actions", () => {
  const onCancelItem = vi.fn();
  const onIgnoreItem = vi.fn();
  const onPromoteItem = vi.fn();
  const onRestoreItem = vi.fn();
  const onRetryItem = vi.fn();
  const onResumeItem = vi.fn();
  const onCancelAllPaused = vi.fn();
  const onUndo = vi.fn();

  const steer = create(SessionTrayItemViewSchema, {
    requestId: "m1",
    group: SessionTrayGroupKind.STEER,
    tone: SessionTrayTone.NEUTRAL,
    activity: SessionTrayRowActivity.IDLE,
    showSpinner: true,
    text: "运行中插一句",
    mode: SendMode.STEER,
    canCancel: true,
    canRestore: true,
    serverBacked: true,
  });
  const queued = create(SessionTrayItemViewSchema, {
    requestId: "m2",
    group: SessionTrayGroupKind.QUEUE,
    tone: SessionTrayTone.NEUTRAL,
    activity: SessionTrayRowActivity.IDLE,
    queueNumber: 1,
    text: "排队消息",
    mode: SendMode.QUEUE,
    canCancel: true,
    canPromote: true,
    canRestore: true,
    serverBacked: true,
  });
  const paused1 = create(SessionTrayItemViewSchema, {
    requestId: "m3",
    group: SessionTrayGroupKind.PAUSED,
    tone: SessionTrayTone.NEUTRAL,
    activity: SessionTrayRowActivity.IDLE,
    text: "Paused message",
    mode: SendMode.QUEUE,
    canCancel: true,
    canResume: true,
    canRestore: true,
    serverBacked: true,
  });

  const tray = create(SessionPendingTrayViewSchema, {
    totalCount: 3,
    orderedItems: [steer, queued, paused1],
    groups: [
      create(SessionTrayGroupViewSchema, {
        kind: SessionTrayGroupKind.STEER,
        tone: SessionTrayTone.NEUTRAL,
        headerText: create(TextSchema, { key: "queue.group_steer" }),
        totalCount: 1,
        items: [steer],
      }),
      create(SessionTrayGroupViewSchema, {
        kind: SessionTrayGroupKind.QUEUE,
        tone: SessionTrayTone.NEUTRAL,
        headerText: create(TextSchema, { key: "queue.group_queue" }),
        totalCount: 1,
        items: [queued],
      }),
      create(SessionTrayGroupViewSchema, {
        kind: SessionTrayGroupKind.PAUSED,
        tone: SessionTrayTone.NEUTRAL,
        headerText: create(TextSchema, { key: "queue.group_paused" }),
        totalCount: 1,
        canCancelAll: true,
        items: [paused1],
      }),
    ],
  });

  render(
    <PendingTray
      tray={tray}
      onCancelItem={onCancelItem}
      onIgnoreItem={onIgnoreItem}
      onPromoteItem={onPromoteItem}
      onRestoreItem={onRestoreItem}
      onRetryItem={onRetryItem}
      onResumeItem={onResumeItem}
      onCancelAllPaused={onCancelAllPaused}
      onUndo={onUndo}
    />,
  );

  const rows = [...host.querySelectorAll(".ptray-row")];
  expect(rows).toHaveLength(3);
  expect(rows[0].textContent).toContain("运行中插一句");
  expect(rows[1].querySelector(".ptray-lead")?.textContent).toBe("1");

  // Tap queued row to reveal 现在发 / 放回输入框
  act(() => rows[1].querySelector<HTMLElement>(".ptray-hit")!.click());
  const opts = rows[1].querySelectorAll(".ptray-opts button");
  expect(opts).toHaveLength(2);
  act(() => (opts[0] as HTMLButtonElement).click());
  expect(onPromoteItem).toHaveBeenCalledWith("m2");

  // Re-open to click 放回输入框
  act(() => rows[1].querySelector<HTMLElement>(".ptray-hit")!.click());
  const restoreBtn = [...rows[1].querySelectorAll("button")].find((b) => b.textContent === message("composer.restore"))!;
  act(() => restoreBtn.click());
  expect(onRestoreItem).toHaveBeenCalledWith("m2");

  // Paused row: 发送 + 全部取消 + ×
  const sendBtn = [...rows[2].querySelectorAll("button")].find((b) => b.textContent === message("composer.send"))!;
  act(() => sendBtn.click());
  expect(onResumeItem).toHaveBeenCalledWith("m3");

  const cancelAllBtn = [...host.querySelectorAll(".ptray-gl button")].find((b) => b.textContent === message("queue.cancel_all"))!;
  act(() => (cancelAllBtn as HTMLButtonElement).click());
  expect(onCancelAllPaused).toHaveBeenCalledTimes(1);

  const xBtn = rows[0].querySelector<HTMLButtonElement>(".ptray-acts button.x")!;
  act(() => xBtn.click());
  expect(onCancelItem).toHaveBeenCalledWith("m1");
});

it("renders failed/unsure rows, keyboard summary, and undo bar", () => {
  const onRetryItem = vi.fn();
  const onIgnoreItem = vi.fn();
  const onTerminal = vi.fn();
  const onUndo = vi.fn();

  const failed = create(SessionTrayItemViewSchema, {
    requestId: "req-fail",
    slotKey: "send:s-qr:req-fail",
    text: "failed text",
    group: SessionTrayGroupKind.FAILED,
    tone: SessionTrayTone.RED,
    activity: SessionTrayRowActivity.IDLE,
    mode: SendMode.STEER,
    canRetry: true,
    canRestore: true,
    canCancel: true,
    reasonText: create(TextSchema, { literal: "disconnected" }),
  });
  const unsure = create(SessionTrayItemViewSchema, {
    requestId: "req-unk",
    slotKey: "send:s-qr:req-unk",
    text: "unknown text",
    group: SessionTrayGroupKind.UNSURE,
    tone: SessionTrayTone.ORANGE,
    activity: SessionTrayRowActivity.IDLE,
    mode: SendMode.QUEUE,
    canOpenTerminal: true,
    canIgnore: true,
    canRestore: true,
    reasonText: create(TextSchema, { literal: "unknown" }),
  });

  const tray = create(SessionPendingTrayViewSchema, {
    totalCount: 2,
    failedCount: 2,
    orderedItems: [failed, unsure],
    groups: [
      create(SessionTrayGroupViewSchema, {
        kind: SessionTrayGroupKind.FAILED,
        tone: SessionTrayTone.RED,
        headerText: create(TextSchema, { key: "queue.group_failed" }),
        totalCount: 1,
        items: [failed],
      }),
      create(SessionTrayGroupViewSchema, {
        kind: SessionTrayGroupKind.UNSURE,
        tone: SessionTrayTone.ORANGE,
        headerText: create(TextSchema, { key: "queue.group_unsure" }),
        totalCount: 1,
        items: [unsure],
      }),
    ],
    undo: create(SessionTrayUndoViewSchema, {
      visible: true,
      count: 1,
      canUndo: true,
      label: create(TextSchema, { key: "queue.canceled_count", args: [{ name: "count", value: { case: "intValue", value: 1n } }] }),
    }),
  });

  render(
    <PendingTray
      tray={tray}
      onTerminal={onTerminal}
      onCancelItem={vi.fn()}
      onIgnoreItem={onIgnoreItem}
      onPromoteItem={vi.fn()}
      onRestoreItem={vi.fn()}
      onRetryItem={onRetryItem}
      onResumeItem={vi.fn()}
      onCancelAllPaused={vi.fn()}
      onUndo={onUndo}
    />,
  );

  expect(host.querySelector(".ptray-gl.red")).toBeTruthy();
  expect(host.querySelector(".ptray-gl.orange")).toBeTruthy();
  expect(host.querySelector(".ptray-why.red")?.textContent).toBe("disconnected");
  expect(host.querySelector(".ptray-why.orange")?.textContent).toBe("unknown");

  const rows = [...host.querySelectorAll(".ptray-row")];
  // Unsure row has 去终端 & 忽略, NO ×
  expect(rows[1].querySelector("button.x")).toBeNull();
  act(() => [...rows[0].querySelectorAll("button")].find((b) => b.textContent === message("common.retry"))!.click());
  expect(onRetryItem).toHaveBeenCalledWith("req-fail");
  act(() => [...rows[1].querySelectorAll("button")].find((b) => b.textContent === message("session.go_terminal"))!.click());
  expect(onTerminal).toHaveBeenCalledTimes(1);
  act(() => [...rows[1].querySelectorAll("button")].find((b) => b.textContent === message("queue.ignore"))!.click());
  expect(onIgnoreItem).toHaveBeenCalledWith("req-unk");

  // Undo footer
  act(() => host.querySelector<HTMLButtonElement>(".ptray-undo button")!.click());
  expect(onUndo).toHaveBeenCalledTimes(1);

  // Keyboard collapsed summary
  render(
    <PendingTray
      tray={tray}
      keyboardCollapsed
      onCancelItem={vi.fn()}
      onIgnoreItem={vi.fn()}
      onPromoteItem={vi.fn()}
      onRestoreItem={vi.fn()}
      onRetryItem={onRetryItem}
      onResumeItem={vi.fn()}
      onCancelAllPaused={vi.fn()}
      onUndo={onUndo}
    />,
  );
  const sum = host.querySelector(".ptray-sum")!;
  expect(sum.textContent).toContain(message("queue.keyboard_summary", { count: 2 }));
  expect(sum.querySelector("b")?.textContent).toContain(message("queue.keyboard_failed", { count: 2 }));
});
