// @vitest-environment happy-dom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { localeStore, t, useLocale } from "../i18n";
import { DemoRemote } from "../dev/demo";
import { stateOf, when } from "../model";
import { ApprovalMode } from "../gen/remoteagent/v1/event_pb";
import { NewBox, useNewSession, type NewSession } from "./NewSession";
import { useRemote } from "../ui";

let root: Root | undefined;
let host: HTMLDivElement;
afterEach(() => {
  act(() => root?.unmount()); host?.remove();
  root = undefined;
  localeStore.overrideForTesting();
  localStorage.clear();
});

it.each(["zh-CN", "en"] as const)("starts the chosen model with the original draft in %s", async locale => {
  localeStore.overrideForTesting(locale);
  const remote = DemoRemote.create(new URLSearchParams(), async () => {});
  remote.start();
  const send = vi.spyOn(remote, "machineCommand");
  const go = vi.fn();
  let ns!: NewSession;
  function Form() {
    useLocale(); useRemote(remote);
    ns = useNewSession(remote, "/example/project", go);
    return <NewBox ns={ns} name="Example" variant="start" />;
  }
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  act(() => root!.render(<Form />));
  await vi.waitFor(() => expect(ns.info.loading).toBe(false));
  const input = host.querySelector<HTMLTextAreaElement>("textarea")!;
  const prompt = "Keep this user text 原文";
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, prompt);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  expect(ns.text).toBe(prompt);
  act(() => host.querySelector<HTMLButtonElement>(".project-summary")!.click());
  await vi.waitFor(() => expect(host.querySelector(".new-task-model-panel .mi")).not.toBeNull());
  const model = [...host.querySelectorAll<HTMLButtonElement>(".new-task-model-panel .mi")].find(b => b.textContent === "GPT-6-Sol")!;
  act(() => model.click());
  await vi.waitFor(() => expect(ns.cfg.model).toBe("gpt-6-sol"));
  act(() => host.querySelector<HTMLButtonElement>(".new-task-model-panel .side-act")!.click());
  expect(input.value).toBe(prompt);
  expect(input.getAttribute("aria-label")).toBe(t("new_session.first_message"));
  await act(async () => host.querySelector<HTMLButtonElement>(".snd")!.click());
  await vi.waitFor(() => expect(go).toHaveBeenCalled());
  const starts = send.mock.calls.filter(([, command]) => command?.case === "createSession");
  expect(starts).toHaveLength(1);
  expect(starts[0][1]).toMatchObject({ case: "createSession", value: { cwd: "/example/project", prompt, model: "gpt-6-sol", mode: ApprovalMode.UNSPECIFIED } });
  expect(remote.drafts.has("new:/example/project")).toBe(false);
});

it("keeps the draft, input node and status meaning when the test locale changes", async () => {
  const remote = DemoRemote.create(new URLSearchParams(), async () => {}); remote.start();
  let ns!: NewSession;
  function Form() {
    useLocale(); useRemote(remote);
    ns = useNewSession(remote, "/example/project", () => {});
    return <NewBox ns={ns} name="Example" variant="start" />;
  }
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  act(() => root!.render(<Form />));
  await vi.waitFor(() => expect(ns.info.loading).toBe(false));
  act(() => ns.setText("原始草稿"));
  const input = host.querySelector("textarea");
  const original = stateOf(remote.sessions.get("s-run")!);
  act(() => localeStore.overrideForTesting("en"));
  expect(host.querySelector("textarea")).toBe(input);
  expect(ns.text).toBe("原始草稿");
  expect(input?.getAttribute("placeholder")).toBe("What would you like to do?");
  const translated = stateOf(remote.sessions.get("s-run")!);
  expect(translated).toMatchObject({ kind: original.kind, tone: original.tone, text: "Running" });
  expect(when(Date.now() - 120_000)).toBe("2 minutes ago");
});

it.each([0, 1, 2, 1000])("formats list counts and relative times with integer plural rules (%i)", count => {
  expect(t("home.waiting_count", { count }, "zh-CN")).toBe(`${count} 件事等你`);
  expect(t("home.waiting_count", { count }, "en")).toBe(count === 1 ? "1 item needs you" : `${count} items need you`);
  expect(t("projects.count", { count }, "en")).toBe(count === 1 ? "1 project" : `${count} projects`);
  expect(t("time.minutes_ago", { count }, "en")).toBe(count === 1 ? "1 minute ago" : `${count} minutes ago`);
});
