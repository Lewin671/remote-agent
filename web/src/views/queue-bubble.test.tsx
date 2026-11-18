import { t as message } from "../i18n";
// @vitest-environment happy-dom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import {
  SessionCommandStateSchema,
  SessionCommandStatus,
  SessionOutgoingState,
  SessionOutgoingViewSchema,
  SessionPendingInputViewSchema,
} from "../gen/remoteagent/core/v1/session_view_pb";
import { TextSchema } from "../gen/remoteagent/core/v1/text_pb";
import { InputStatus, SendMode } from "../gen/remoteagent/v1/event_pb";
import { OutgoingBubbles, PendingBubbles } from "./Queue";

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

it("renders pending queued/held/paused items from SessionPendingInputView and dispatches cancel/resume commands", () => {
  const onSubmit = vi.fn();
  const queued = create(SessionPendingInputViewSchema, {
    requestId: "m1",
    text: "运行中排队的一句",
    mode: SendMode.STEER,
    status: InputStatus.QUEUED,
    rank: 1,
    totalPending: 2,
    label: create(TextSchema, { key: "queue.insert" }),
    caption: create(TextSchema, { key: "queue.insert" }),
    canCancel: true,
    cancel: create(SessionCommandStateSchema, {
      slotKey: "cancel_input:s-run:m1",
      status: SessionCommandStatus.IDLE,
    }),
  });
  const paused = create(SessionPendingInputViewSchema, {
    requestId: "m2",
    text: "Paused message",
    mode: SendMode.QUEUE,
    status: InputStatus.QUEUED,
    paused: true,
    rank: 2,
    totalPending: 2,
    label: create(TextSchema, { key: "queue.paused" }),
    caption: create(TextSchema, { key: "queue.paused" }),
    canCancel: true,
    canResume: true,
    cancel: create(SessionCommandStateSchema, { slotKey: "cancel_input:s-run:m2", status: SessionCommandStatus.IDLE }),
    resume: create(SessionCommandStateSchema, { slotKey: "resume_input:s-run:m2", status: SessionCommandStatus.IDLE }),
  });

  render(
    <PendingBubbles
      sessionId="s-run"
      pendingInputs={[queued, paused]}
      onSubmitCommand={onSubmit}
    />,
  );

  const bubbles = [...host.querySelectorAll(".pending-bubble")];
  expect(bubbles).toHaveLength(2);
  expect(bubbles[0].textContent).toContain("运行中排队的一句");
  expect(bubbles[0].querySelector(".pending-meta > span")?.textContent).toContain(message("queue.insert"));

  const sendBtn = [...bubbles[1].querySelectorAll("button")].find((b) => b.textContent === message("composer.send"))!;
  act(() => sendBtn.click());
  expect(onSubmit).toHaveBeenCalledTimes(1);
  expect(onSubmit.mock.calls[0][1].kind).toMatchObject({
    case: "resumeInput",
    value: { sessionId: "s-run", requestId: "m2" },
  });

  const cancelBtn = [...bubbles[0].querySelectorAll("button")].find((b) => b.textContent === message("common.cancel"))!;
  act(() => cancelBtn.click());
  expect(onSubmit).toHaveBeenCalledTimes(2);
  expect(onSubmit.mock.calls[1][1].kind).toMatchObject({
    case: "cancelInput",
    value: { sessionId: "s-run", requestId: "m1" },
  });
});

it("renders outgoing sending, failed, and unknown items with retry, restore, and dismiss actions", () => {
  const onRetry = vi.fn();
  const onRestore = vi.fn();
  const onDismiss = vi.fn();
  const failed = create(SessionOutgoingViewSchema, {
    requestId: "req-fail",
    slotKey: "send:s-qr:req-fail",
    text: "failed text",
    mode: SendMode.STEER,
    state: SessionOutgoingState.FAILED,
    canRetry: true,
    canRestore: true,
    canDismiss: true,
    caption: create(TextSchema, { literal: "disconnected" }),
    feedback: create(TextSchema, { literal: "disconnected" }),
  });
  const unknown = create(SessionOutgoingViewSchema, {
    requestId: "req-unk",
    slotKey: "send:s-qr:req-unk",
    text: "unknown text",
    mode: SendMode.QUEUE,
    state: SessionOutgoingState.UNKNOWN,
    frozen: true,
    canRetry: true,
    canRestore: true,
    canDismiss: true,
    caption: create(TextSchema, { literal: "unknown" }),
    feedback: create(TextSchema, { literal: "unknown" }),
    feedbackUncertain: true,
  });

  render(
    <OutgoingBubbles
      sessionId="s-qr"
      outgoing={[failed, unknown]}
      onRetry={onRetry}
      onRestore={onRestore}
      onDismiss={onDismiss}
    />,
  );

  expect(host.querySelector(".me.fail")?.textContent).toContain("failed text");
  expect(host.querySelector(".me.warn")?.textContent).toContain("unknown text");
  act(() => [...host.querySelectorAll("button")].find((b) => b.textContent === message("common.retry"))!.click());
  expect(onRetry).toHaveBeenCalledTimes(1);
  act(() => [...host.querySelectorAll("button")].find((b) => b.textContent === message("composer.restore"))!.click());
  expect(onRestore).toHaveBeenCalledWith("req-fail");
  act(() => [...host.querySelectorAll("button")].find((b) => b.textContent === message("session.dismiss_notice"))!.click());
  expect(onDismiss).toHaveBeenCalledWith("req-fail");
});
