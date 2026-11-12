import { t as message } from "../i18n";
// @vitest-environment happy-dom
// The bubble of a message this page sent is one element from sending until
// the chat holds it, and takes the queue's look only when the message really
// waits (design-system §4 动效, 发一条消息).

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { DemoRemote } from "../dev/demo";
import { EventSchema, InputStatus } from "../gen/remoteagent/v1/event_pb";
import { SessionPage } from "./Session";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | undefined;
let host: HTMLElement;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = undefined;
});

function open(sid: string, text: string, states: InputStatus[]) {
  const r = DemoRemote.create(new URLSearchParams(), async () => undefined);
  r.start();
  const s = r.sessions.get(sid)!;
  const push = (kind: object) => r.events(sid).push(create(EventSchema, { seq: BigInt(r.events(sid).length + 1), kind: kind as never }));
  (r as unknown as { outboxes: Map<string, unknown[]> }).outboxes.set(sid, [{ sendId: "m1", requestId: "x", deadline: 0, text, status: "sent" }]);
  for (const status of states) push({ case: "inputState", value: { requestId: "m1", status, text } });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />));
  return { r, push };
}

const bubbles = (text: string) => [...host.querySelectorAll(".me")].filter((m) => m.textContent?.includes(text));

it("shows nothing of the queue for a message that is only on its way to the terminal", async () => {
  vi.useFakeTimers();
  try {
    open("s-idle", "空闲时发的一句", [InputStatus.QUEUED]);
    expect(bubbles("空闲时发的一句")).toHaveLength(1);
    expect(host.querySelector(".pending-bubble")).toBeNull();
    expect(host.textContent).not.toContain(message("queue.insert"));
    // Still waiting after a moment: now it says so, in the same bubble.
    const same = bubbles("空闲时发的一句")[0];
    await act(async () => void vi.advanceTimersByTime(1050));
    expect(bubbles("空闲时发的一句")[0]).toBe(same);
    expect(same.classList.contains("pending-bubble")).toBe(true);
    expect(host.querySelector(".pending-meta > span")?.textContent).toBe(message("queue.insert"));
  } finally {
    vi.useRealTimers();
  }
});

it("goes back to a plain bubble when the terminal takes it, without a new element", async () => {
  vi.useFakeTimers();
  try {
    const { r, push } = open("s-idle", "先排队再写入", [InputStatus.QUEUED]);
    await act(async () => void vi.advanceTimersByTime(1050));
    const same = bubbles("先排队再写入")[0];
    expect(same.classList.contains("pending-bubble")).toBe(true);
    act(() => {
      push({ case: "inputState", value: { requestId: "m1", status: InputStatus.SUBMITTED } });
      r.touch();
    });
    expect(bubbles("先排队再写入")[0]).toBe(same);
    expect(same.classList.contains("pending-bubble")).toBe(false);
    expect(same.textContent).toContain(message("composer.await_record", { agent: "Codex" }));
  } finally {
    vi.useRealTimers();
  }
});

it("takes the queue's look at once while a turn runs, and is not drawn twice", () => {
  open("s-run", "运行中排队的一句", [InputStatus.QUEUED]);
  expect(bubbles("运行中排队的一句")).toHaveLength(1);
  expect(bubbles("运行中排队的一句")[0].classList.contains("pending-bubble")).toBe(true);
  expect(host.querySelector(".pending-meta > span")?.textContent).toContain(message("queue.insert"));
});
