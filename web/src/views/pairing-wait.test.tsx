import { t as message } from "../i18n";
// @vitest-environment happy-dom
// Giving up while waiting for the other device to allow a join.
import { create } from "@bufbuild/protobuf";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import {
  SecurityBootstrapKind,
  SecurityPairingInfoSchema,
  SecurityWorkflowStatusSchema,
} from "../gen/remoteagent/core/v1/security_pb";
import { SecurityWorkflowError, type SecurityClient } from "../core/security-client";
import { Setup } from "./Pairing";

const calls: {
  canceled: boolean;
  confirming: boolean;
  releaseJoin: (ok: boolean) => void;
  releaseConfirm?: (ok: boolean) => void;
}[] = [];

function makeClient(): SecurityClient {
  let nextId = 1;
  return {
    bootstrap: vi.fn(async () => create(SecurityWorkflowStatusSchema, { bootstrap: SecurityBootstrapKind.EMPTY })),
    startJoinPairing: vi.fn(async () => {
      const idx = nextId++;
      const pid = `pair-${idx}`;
      const call = {
        canceled: false,
        confirming: false,
        releaseJoin: (_ok: boolean) => {},
        releaseConfirm: (_ok: boolean) => {},
      };
      calls.push(call);
      return await new Promise((res, rej) => {
        call.releaseJoin = (ok) =>
          ok
            ? res(create(SecurityPairingInfoSchema, { pairingId: pid, sasEmoji: ["🦊", "🌵", "🚲", "🎈"] }))
            : rej(new SecurityWorkflowError("canceled", "canceled", false, false, ""));
      });
    }),
    confirmJoinSas: vi.fn(async () => {
      const cur = calls[calls.length - 1];
      cur.confirming = true;
      return await new Promise((res, rej) => {
        cur.releaseConfirm = (ok) =>
          ok ? res({} as never) : rej(new SecurityWorkflowError("canceled", "canceled", false, false, ""));
      });
    }),
    cancelJoinPairing: vi.fn(async () => {
      const cur = calls[calls.length - 1];
      if (cur) cur.canceled = true;
    }),
  } as unknown as SecurityClient;
}

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
  const client = makeClient();
  act(() => root!.render(<Setup client={client} onDone={done} />));
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
      .find((b) => b.textContent === message("security.join"))!
      .click(),
  );
  await act(async () => {
    calls[calls.length - 1].releaseJoin(true);
  });
  return { done, input, client };
}

const give = () =>
  [...host.querySelectorAll("button")].find(
    (b) => b.textContent === message("security.patterns_differ_return_to_code_entry"),
  )!;

const confirm = () =>
  [...host.querySelectorAll("button")].find(
    (b) => b.textContent === message("security.patterns_match_trust_and_continue"),
  )!;

it("goes back to the code, keeping the server, and stops the join", async () => {
  const { input } = await start();
  expect(host.textContent).toContain("🦊 🌵 🚲 🎈");
  await act(async () => give().click());
  expect(calls[0].canceled).toBe(true);
  expect(input(message("security.pairing_code")).value).toBe("");
  expect(input(message("security.server")).value).toBe("https://ra.example.com");
  expect(host.textContent).toContain("已停止这次等待");
  expect(host.textContent).not.toContain(message("security.pairing_canceled"));
});

it("cannot give up once this device began to register, and reports the outcome", async () => {
  const { done } = await start();
  await act(async () => confirm().click());
  expect(give().disabled).toBe(true);
  expect(host.textContent).toContain("本机已确认，等待管理端批准并完成绑定");
  await act(async () => calls[0].releaseConfirm?.(true));
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
      .find((b) => b.textContent === message("security.join"))!
      .click(),
  );
  await act(async () => calls[1].releaseJoin(true));
  expect(calls.length).toBe(2);
  expect(calls[0].canceled).toBe(true);
  expect(calls[1].canceled).toBe(false);
  expect(give().disabled).toBe(false);
});
