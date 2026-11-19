// @vitest-environment happy-dom
// Interaction coverage for client-v63 Web features.

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { DemoRemote } from "../dev/demo";
import { localeStore, t as message } from "../i18n";
import { CommandError } from "../remote";
import { SendMode } from "../gen/remoteagent/v1/event_pb";
import { getCoreHost } from "../core/host";
import { Sheet } from "../ui";
import { Home } from "./Home";
import { SessionPage } from "./Session";

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | undefined;
let host: HTMLElement;
const remotes: DemoRemote[] = [];

beforeEach(() => {
  localeStore.overrideForTesting("zh-CN");
  vi.stubGlobal("RTCPeerConnection", class {});
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = undefined;
  for (const r of remotes.splice(0)) r.dispose();
  localeStore.overrideForTesting(undefined);
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  vi.useRealTimers();
  localStorage.clear();
});

function createDemo(query = ""): DemoRemote {
  const r = DemoRemote.create(new URLSearchParams(query), async () => undefined);
  remotes.push(r);
  r.start();
  return r;
}

function mount(node: React.ReactElement) {
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
  act(() => root!.render(node));
}

async function mountAsync(node: React.ReactElement) {
  mount(node);
  await act(async () => {
    await Promise.resolve();
  });
}

async function click(el: Element | null | undefined) {
  if (!el) throw new Error("missing clickable element");
  await act(async () => {
    (el as HTMLElement).click();
  });
}

function setTextarea(el: HTMLTextAreaElement, value: string) {
  Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(el, value);
  el.dispatchEvent(new Event("input", { bubbles: true }));
}

describe("Home inline single-select question dark focus card", () => {
  it("renders inline question, animates picked + 150ms spinner + 900ms confirmation, and crossfades 1 -> 0", async () => {
    vi.useFakeTimers();
    const r = createDemo("question=only");
    let resolveCmd!: (v: unknown) => void;
    const origCmd = r.command.bind(r);
    vi.spyOn(r, "command").mockImplementation(
      (sid, cmd, slot) =>
        new Promise((res) => {
          resolveCmd = () => res(origCmd(sid, cmd, slot));
        }),
    );

    const go = vi.fn();
    mount(<Home remote={r} go={go} />);
    await act(async () => {
      await Promise.resolve();
    });

    expect(host.querySelector(".tc")?.textContent).toContain("大版本也一起升吗？");
    const opts = [...host.querySelectorAll<HTMLButtonElement>(".q-opts .q-opt")];
    expect(opts.length).toBeGreaterThanOrEqual(2);
    expect(opts.length).toBeLessThanOrEqual(3);

    await click(opts[0]);
    expect(opts[0].classList.contains("picking")).toBe(true);
    expect(opts[0].querySelector(".spin")).toBeNull();

    await act(async () => {
      vi.advanceTimersByTime(160);
    });
    expect(opts[0].querySelector(".spin")).not.toBeNull();

    await act(async () => {
      resolveCmd(undefined);
      await Promise.resolve();
    });
    expect(host.querySelector(".hl-wrap")?.classList.contains("crossfade")).toBe(true);
    expect(host.querySelector(".tc.line")?.textContent).toContain("已回答 · 只升小版本");
    expect(host.querySelector(".tc.line svg")).not.toBeNull();

    await act(async () => {
      vi.advanceTimersByTime(910);
    });
    await act(async () => {
      vi.advanceTimersByTime(200);
    });
    expect(host.querySelector(".tc")).toBeNull();
  });

  it("rolls back picked state on send failure and displays retry, plus terminal fallback link", async () => {
    const r = createDemo("question=1");
    const cmdSpy = vi.spyOn(r, "command").mockRejectedValueOnce(new CommandError("failed_precondition", "网络断开"));
    const go = vi.fn();
    mount(<Home remote={r} go={go} />);
    await act(async () => {
      await Promise.resolve();
    });

    await click(host.querySelector(".q-opts .q-opt"));
    await vi.waitFor(() => expect(host.querySelector(".q-err")?.textContent).toContain("重试"));

    await click(host.querySelector(".q-foot button"));
    expect(r.termFirst.has("s-ask")).toBe(true);
    expect(go).toHaveBeenCalledWith("s/s-ask");

    await click(host.querySelector(".q-err button"));
    expect(cmdSpy).toHaveBeenCalledTimes(2);
    await vi.waitFor(() => expect(host.querySelector(".tc.line")?.textContent).toContain("已回答"));
  });

  it("shows external resolution notice when question is answered elsewhere while visible", async () => {
    vi.useFakeTimers();
    const r = createDemo("question=only");
    mount(<Home remote={r} go={() => undefined} />);
    await act(async () => {
      await Promise.resolve();
    });
    expect(host.querySelector(".tc .q-opts")).not.toBeNull();

    await act(async () => {
      (r as unknown as { push: (sid: string, kind: unknown) => void; patch: (sid: string, p: unknown) => void }).push("s-ask", {
        case: "questionAnswered",
        value: { questionId: "q1", answers: [{ answers: ["已选"] }] },
      });
      (r as unknown as { patch: (sid: string, p: unknown) => void }).patch("s-ask", { pendingQuestions: 0 });
    });

    expect(host.querySelector(".tc.line")?.textContent).toContain(message("home.question_answered_elsewhere"));
    await act(async () => {
      vi.advanceTimersByTime(910);
    });
    expect(host.querySelector(".tc.line")?.textContent).toContain(message("home.question_answered_elsewhere"));
    await act(async () => {
      vi.advanceTimersByTime(200);
    });
    expect(host.querySelector(".tc")).toBeNull();
  });
});

describe("Running Composer two-button actions, DOM identity, and dock demotion", () => {
  it("offers 做完再发 and ↑ without sticky mode or losing textarea focus", async () => {
    vi.useFakeTimers();
    const r = DemoRemote.create(new URLSearchParams());
    remotes.push(r);
    r.start();
    const submit = vi.spyOn(getCoreHost(r), "sessionSubmitCommand");
    await mountAsync(<SessionPage remote={r} sessionId="s-run" onBack={() => undefined} wide={false} />);

    const textareaBefore = host.querySelector<HTMLTextAreaElement>("textarea.draft")!;
    expect(host.querySelector(".smode")).toBeNull();
    expect(host.querySelector(".send-menu")).toBeNull();
    expect(host.querySelector(".snd.stop")).not.toBeNull();

    await act(async () => {
      textareaBefore.focus();
      setTextarea(textareaBefore, "先排到后面");
    });

    const later = host.querySelector<HTMLButtonElement>("button.later")!;
    expect(later.textContent).toContain(message("composer.send_after"));
    expect(host.querySelector("textarea.draft")).toBe(textareaBefore);
    expect(document.activeElement).toBe(textareaBefore);

    await click(later);
    expect(submit).toHaveBeenCalled();
    expect(submit.mock.calls[0][3].kind).toMatchObject({ case: "send", value: { mode: SendMode.QUEUE } });
    expect(textareaBefore.value).toBe("");

    await act(async () => {
      setTextarea(textareaBefore, "第二句直接发送");
    });
    await click(host.querySelector<HTMLButtonElement>("button.snd")!);
    expect(submit.mock.calls[1][3].kind).toMatchObject({ case: "send", value: { mode: SendMode.STEER } });
  });

  it("demotes send button when dock is open and hides composer while denying in dock", async () => {
    const r = createDemo();
    await mountAsync(<SessionPage remote={r} sessionId="s-appr" onBack={() => undefined} wide={false} />);

    expect(host.querySelector(".snd")?.classList.contains("demote")).toBe(true);
    const denyBtn = [...host.querySelectorAll<HTMLButtonElement>(".dock .tc .btns button")].find(
      (b) => b.textContent === message("approval.deny"),
    )!;
    await click(denyBtn);
    expect(host.querySelector(".composer")).toBeNull();
  });
});

describe("ApprovalCard deny draft preservation & docked scroll-to-bottom guard", () => {
  it("preserves deny draft on Escape/Cancel/failure, shows 正在拒绝… / 重试拒绝, and shows 已在终端处理 on external resolution", async () => {
    vi.useFakeTimers();
    const r = createDemo();
    let rejectCmd!: (err: Error) => void;
    vi.spyOn(r, "command").mockImplementation(
      () =>
        new Promise((_, rej) => {
          rejectCmd = rej;
        }),
    );

    await mountAsync(<SessionPage remote={r} sessionId="s-appr" onBack={() => undefined} wide={false} />);
    const findDenyBtn = () =>
      [...host.querySelectorAll<HTMLButtonElement>(".dock .tc .btns button")].find(
        (b) => b.textContent === message("approval.deny") || b.textContent === message("approval.retry_deny") || b.textContent === message("approval.denying"),
      );

    await click(findDenyBtn());
    const ta = host.querySelector<HTMLTextAreaElement>(".dock .tc textarea.inp")!;
    await act(async () => {
      setTextarea(ta, "不要删库");
    });

    // Escape closes deny form but keeps draft intact
    await act(async () => {
      ta.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    expect(host.querySelector(".dock .tc textarea.inp")).toBeNull();

    await click(findDenyBtn());
    expect(host.querySelector<HTMLTextAreaElement>(".dock .tc textarea.inp")!.value).toBe("不要删库");

    // Submit failure holds card and displays 重试拒绝 with intact draft
    await click(findDenyBtn());
    expect(findDenyBtn()?.textContent).toBe(message("approval.denying"));
    await act(async () => {
      rejectCmd(new CommandError("failed_precondition", "超时"));
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(findDenyBtn()?.textContent).toBe(message("approval.retry_deny"));
    expect(host.querySelector<HTMLTextAreaElement>(".dock .tc textarea.inp")!.value).toBe("不要删库");

    // External resolution during deny shows 1.2s banner 已在终端处理
    await act(async () => {
      vi.advanceTimersByTime(2500);
      (r as unknown as { push: (sid: string, kind: unknown) => void }).push("s-appr", {
        case: "approvalResolved",
        value: { approvalId: "a1", decision: 3 },
      });
    });
    expect(host.querySelector(".dock")?.textContent).toContain(message("approval.handled_in_terminal"));
  });

  it("gates docked allow behind 往下看完整命令 until scrolled to bottom (latched per approvalId)", async () => {
    const r = createDemo("longcmd=1");
    await mountAsync(<SessionPage remote={r} sessionId="s-appr" onBack={() => undefined} wide={false} />);

    const scroller = host.querySelector<HTMLElement>(".dock .pending-scroll")!;
    expect(scroller).not.toBeNull();
    Object.defineProperty(scroller, "scrollHeight", { configurable: true, value: 600 });
    Object.defineProperty(scroller, "clientHeight", { configurable: true, value: 180 });
    Object.defineProperty(scroller, "scrollTop", { configurable: true, writable: true, value: 0 });
    scroller.scrollTo = vi.fn(({ top }: { top?: number } = {}) => {
      if (typeof top === "number") scroller.scrollTop = top;
    }) as unknown as typeof scroller.scrollTo;

    await act(async () => {
      scroller.dispatchEvent(new Event("scroll"));
    });

    const primaryBtn = () =>
      [...host.querySelectorAll<HTMLButtonElement>(".dock .tc .btns button")].find(
        (b) => b.textContent === message("approval.scroll_to_view") || b.textContent === message("approval.allow"),
      )!;

    expect(primaryBtn().textContent).toBe(message("approval.scroll_to_view"));

    // Simulate scrolling to bottom -> unlocks 允许 and latches even if scrolled back up
    await act(async () => {
      scroller.scrollTop = 420;
      scroller.dispatchEvent(new Event("scroll"));
    });
    expect(primaryBtn().textContent).toBe(message("approval.allow"));

    await act(async () => {
      scroller.scrollTop = 0;
      scroller.dispatchEvent(new Event("scroll"));
    });
    expect(primaryBtn().textContent).toBe(message("approval.allow"));
  });
});

describe("Session header ⚡ 直连 chip & light popover", () => {
  it("opens light popover, survives mid-open relay fallback, and navigates to Connection settings", async () => {
    const r = createDemo("direct=direct");
    const go = vi.fn();
    await mountAsync(<SessionPage remote={r} sessionId="s-run" onBack={() => undefined} wide={false} go={go} />);

    const chip = host.querySelector<HTMLButtonElement>(".dchip")!;
    expect(chip).not.toBeNull();
    expect(chip.textContent).toContain(message("transport.direct"));

    await click(chip);
    const pop = () => document.querySelector<HTMLElement>(".dpop");
    expect(pop()).not.toBeNull();
    expect(pop()!.textContent).toContain(message("transport.popover_stable"));

    // Simulate dropping to relay mid-open: popover must remain open and swap copy
    await act(async () => {
      vi.spyOn(r.direct, "status").mockReturnValue({
        state: "none",
        file: "relay",
        read: "relay",
        reason: "failed",
        fileMiBps: {},
        readMs: {},
      });
      (r.direct as unknown as { notify(): void }).notify();
    });
    expect(pop()).not.toBeNull();
    expect(pop()!.textContent).toContain(message("transport.popover_switched_relay"));

    const settingsBtn = pop()!.querySelector<HTMLButtonElement>(".dpop-foot button")!;
    await click(settingsBtn);
    expect(go).toHaveBeenCalledWith("m/mac/direct");
  });
});

describe("Sheet dirty discard confirmation & pull-to-dismiss", () => {
  it("prompts 放弃还没保存的修改？ when closing a dirty sheet", async () => {
    const onClose = vi.fn();
    mount(
      <Sheet title="编辑" dirty onClose={onClose}>
        <div>内容</div>
      </Sheet>,
    );

    await click(document.querySelector(".dim"));
    const dialog = document.querySelector('[role="alertdialog"]');
    expect(dialog).not.toBeNull();
    expect(dialog!.textContent).toContain(message("common.discard_changes_title"));
    expect(onClose).not.toHaveBeenCalled();

    const confirmBtn = [...dialog!.querySelectorAll<HTMLButtonElement>("button")].find(
      (b) => b.textContent === message("common.discard_changes"),
    )!;
    await click(confirmBtn);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("supports pull-to-dismiss only when sheet content is at scrollTop === 0", () => {
    vi.stubGlobal("matchMedia", () => ({
      matches: false,
      media: "(min-width: 900px)",
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
    }));
    const onClose = vi.fn();
    mount(
      <Sheet title="可拖拽" onClose={onClose}>
        <div style={{ height: 400 }}>内容</div>
      </Sheet>,
    );
    const sheet = document.querySelector<HTMLElement>(".sheet")!;
    Object.defineProperty(sheet, "scrollTop", { configurable: true, writable: true, value: 40 });

    const touch = (type: string, y?: number) => {
      const ev = new Event(type, { bubbles: true, cancelable: true });
      Object.defineProperty(ev, "touches", {
        value: y === undefined ? [] : [{ clientY: y }],
      });
      return ev;
    };

    act(() => {
      sheet.dispatchEvent(touch("touchstart", 100));
      sheet.dispatchEvent(touch("touchmove", 240));
      sheet.dispatchEvent(touch("touchend"));
    });
    expect(onClose).not.toHaveBeenCalled();

    sheet.scrollTop = 0;
    act(() => {
      sheet.dispatchEvent(touch("touchstart", 100));
      sheet.dispatchEvent(touch("touchmove", 220));
      sheet.dispatchEvent(touch("touchend"));
    });
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
