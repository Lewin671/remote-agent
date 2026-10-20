// @vitest-environment happy-dom
// Giving up while waiting for the other device to allow a join.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { Setup } from "./Pairing";

const calls: {
  stopped: () => boolean;
  register: () => void;
  release: (ok: boolean) => void;
}[] = [];
vi.mock("../pairing", async (orig) => {
  const real = await orig<typeof import("../pairing")>();
  return {
    ...real,
    joinByPairing: (
      _l: unknown,
      onEmoji: (e: string[]) => void,
      stopped: () => boolean,
      onRegistering: () => void,
    ) =>
      new Promise((res, rej) => {
        onEmoji(["🦊", "🌵", "🚲", "🎈"]);
        calls.push({
          stopped,
          register: onRegistering,
          release: (ok) => (ok ? res({}) : rej(new real.PairCanceled())),
        });
      }),
  };
});

(
  globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }
).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined;
let host: HTMLElement;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  calls.length = 0;
});

async function start() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const done = vi.fn();
  act(() => root!.render(<Setup onDone={done} />));
  const input = (label: string) =>
    host.querySelector<HTMLInputElement>(`input[aria-label="${label}"]`)!;
  const type = (label: string, v: string) =>
    act(() => {
      Object.getOwnPropertyDescriptor(
        HTMLInputElement.prototype,
        "value",
      )!.set!.call(input(label), v);
      input(label).dispatchEvent(new Event("input", { bubbles: true }));
    });
  type("服务器", "https://ra.example.com");
  type("配对码", "48291756");
  await act(async () =>
    [...host.querySelectorAll("button")]
      .find((b) => b.textContent === "加入")!
      .click(),
  );
  return { done, input };
}

const give = () =>
  [...host.querySelectorAll("button")].find(
    (b) => b.textContent === "图案不一致，返回填码",
  )!;

it("goes back to the code, keeping the server, and stops the join", async () => {
  const { input } = await start();
  expect(host.textContent).toContain("🦊 🌵 🚲 🎈");
  await act(async () => give().click());
  expect(calls[0].stopped()).toBe(true);
  expect(input("配对码").value).toBe("");
  expect(input("服务器").value).toBe("https://ra.example.com");
  expect(host.textContent).toContain("已停止这次等待");
  // The join ends as canceled; nothing else is claimed.
  await act(async () => calls[0].release(false));
  expect(host.textContent).not.toContain("对方拒绝了这次加入");
});

it("cannot give up once this device began to register, and reports the outcome", async () => {
  const { done } = await start();
  act(() => calls[0].register());
  expect(give().disabled).toBe(true);
  expect(host.textContent).toContain("本机已确认，等待手机批准并完成绑定");
  await act(async () => calls[0].release(true));
  expect(done).toHaveBeenCalled();
});

it("keeps a given-up attempt canceled when the user pairs again", async () => {
  const { input } = await start();
  await act(async () => give().click());
  // A new attempt with a new code.
  act(() => {
    Object.getOwnPropertyDescriptor(
      HTMLInputElement.prototype,
      "value",
    )!.set!.call(input("配对码"), "11112222");
    input("配对码").dispatchEvent(new Event("input", { bubbles: true }));
  });
  await act(async () =>
    [...host.querySelectorAll("button")]
      .find((b) => b.textContent === "加入")!
      .click(),
  );
  expect(calls.length).toBe(2);
  // The old attempt must stay stopped: it must never register.
  expect(calls[0].stopped()).toBe(true);
  expect(calls[1].stopped()).toBe(false);
  // Its late callbacks do not touch the new attempt's page.
  act(() => calls[0].register());
  expect(give().disabled).toBe(false);
});
