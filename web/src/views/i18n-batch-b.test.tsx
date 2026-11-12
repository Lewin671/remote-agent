// @vitest-environment happy-dom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { localeStore, t, useLocale } from "../i18n";
import * as i18n from "../i18n";
import { ANDROID, DemoRemote } from "../dev/demo";
import { Remote } from "../remote";
import { ApprovalMode, EventSchema, OriginKind, ToolKind } from "../gen/remoteagent/v1/event_pb";
import { derive, duration, modeOf, toolsSummary } from "../model";
import { imageFailureLine, imageProblemKind } from "../image-problem";
import { skeletonNote } from "../skeleton";
import { SessionPage } from "./Session";
import { useRemote } from "../ui";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | undefined, host: HTMLDivElement, remote: DemoRemote;
afterEach(() => {
  act(() => root?.unmount()); root = undefined; host?.remove(); remote?.stop();
  localeStore.overrideForTesting(); localStorage.clear(); vi.restoreAllMocks(); vi.unstubAllGlobals();
});
function mount(sid: string) {
  vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
  function Page() {
    useLocale(); useRemote(remote);
    return <SessionPage remote={remote} session={remote.sessions.get(sid)!} wide onBack={() => {}} />;
  }
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  act(() => root!.render(<Page />));
}

it.each(["zh-CN", "en"] as const)("keeps Full access red and sends the original approval in %s", async locale => {
  localeStore.overrideForTesting(locale);
  remote = DemoRemote.create(new URLSearchParams(), async () => {}); remote.start();
  const session = remote.sessions.get("s-appr")!;
  session.info!.mode = ApprovalMode.FULL_ACCESS;
  const command = vi.spyOn(remote, "command").mockResolvedValue({} as never);
  mount(session.id);
  expect(host.querySelector(".red")?.textContent).toBe(t("modes.full_access"));
  const allow = [...host.querySelectorAll<HTMLButtonElement>("button")].find(b => b.textContent === t("approval.allow"))!;
  expect(allow.disabled).toBe(false);
  await act(async () => allow.click());
  expect(command).toHaveBeenCalledWith(session.id, expect.objectContaining({ case: "resolveApproval", value: expect.objectContaining({ approvalId: "a1", allow: true }) }), expect.objectContaining({ requestId: expect.any(String), deadline: expect.any(Number) }));
});

it("updates existing history and preserves the draft and structured changes on locale override", () => {
  remote = DemoRemote.create(new URLSearchParams(), async () => {}); remote.start();
  const events = remote.events("s-idle");
  events.push(create(EventSchema, { seq: 100n, kind: { case: "turnContext", value: { permissionMode: "default", model: "old-model" } } }));
  events.push(create(EventSchema, { seq: 101n, kind: { case: "turnContext", value: { permissionMode: "auto", model: "new-model" } } }));
  const d = derive(events);
  remote.sessions.get("s-idle")!.info!.mode = ApprovalMode.AUTO;
  remote.drafts.set("s-idle", "User draft 原文");
  mount("s-idle");
  const input = host.querySelector<HTMLTextAreaElement>("textarea")!;
  const changes = d.terminalChanges.get(101n);
  expect(host.textContent).toContain(t("session.terminal_mode_change", { value: t("modes.auto") }));
  act(() => localeStore.overrideForTesting("en"));
  expect(host.querySelector("textarea")).toBe(input);
  expect(input.value).toBe("User draft 原文");
  expect(input.getAttribute("aria-label")).toBe("Message");
  expect(host.textContent).toContain("switched approval mode to Auto");
  expect(host.textContent).toContain("new-model");
  expect(d.terminalChanges.get(101n)).toBe(changes);
  expect(modeOf(remote.sessions.get("s-idle")!)?.mode).toBe(ApprovalMode.AUTO);
});

it.each(["zh-CN", "en"] as const)("uses origin IDs even if another device is named like the self label in %s", locale => {
  localeStore.overrideForTesting(locale);
  remote = DemoRemote.create(new URLSearchParams(), async () => {}); remote.start();
  remote.devices.get(ANDROID)!.meta!.name = t("session.origin_self");
  remote.events("s-idle").push(create(EventSchema, { seq: 100n, origin: { kind: OriginKind.CLIENT, deviceId: ANDROID }, kind: { case: "settingsChanged", value: { mode: ApprovalMode.FULL_ACCESS } } }));
  mount("s-idle");
  expect(host.textContent).toContain(t("session.mode_changed_by", { origin: t("session.origin_self"), value: t("modes.full_access") }));
});

it.each(["zh-CN", "en"] as const)("never enables approval of a skeleton in %s", locale => {
  localeStore.overrideForTesting(locale);
  remote = DemoRemote.create(new URLSearchParams(), async () => {}); remote.start();
  const command = vi.spyOn(remote, "command");
  const events = remote.events("s-appr");
  const approval = events.find(e => e.kind.case === "approvalRequested")!;
  if (approval.kind.case === "approvalRequested") approval.kind.value.command += skeletonNote;
  // Invalidate the demo's presentation-only derived cache just as a new event would.
  vi.spyOn(remote, "currentDerived").mockImplementation((sid, d) => Remote.prototype.currentDerived.call(remote, sid, d));
  mount("s-appr");
  expect(host.textContent).toContain(t("session.skeleton"));
  expect([...host.querySelectorAll("button")].some(b => b.textContent === t("approval.allow"))).toBe(false);
  expect(command).not.toHaveBeenCalled();
});

it.each(["zh-CN", "en"] as const)("resumes the same paused queue item in %s", async locale => {
  localeStore.overrideForTesting(locale);
  remote = DemoRemote.create(new URLSearchParams("queue-all=1"), async () => {}); remote.start();
  const command = vi.spyOn(remote, "command").mockResolvedValue({} as never);
  mount("s-multi");
  const paused = [...host.querySelectorAll<HTMLElement>(".pending-bubble")].find(el => el.textContent?.includes(t("queue.paused")))!;
  const send = [...paused.querySelectorAll<HTMLButtonElement>("button")].find(b => b.textContent === t("composer.send"))!;
  expect(send.disabled).toBe(false);
  await act(async () => send.click());
  expect(command.mock.calls[0]?.slice(0, 2)).toEqual(["s-multi", { case: "resumeInput", value: { sessionId: "s-multi", requestId: "q-paused" } }]);
});

it("aggregates image failures by stable category and localizes the matching single cause", () => {
  const format = vi.spyOn(i18n, "t").mockReturnValue("same translation");
  imageFailureLine(["decode error", "offline"]);
  expect(format).toHaveBeenLastCalledWith("image.attach_failed", { count: 2 });
  format.mockClear(); imageFailureLine(["decode error", "unsupported PNG"]);
  expect(format).toHaveBeenLastCalledWith("image.cannot_read_this_image_try_jpeg_or_png_instead");
  expect(imageProblemKind("decode error")).toBe(imageProblemKind("unsupported PNG"));
  expect(imageProblemKind("decode error")).not.toBe(imageProblemKind("offline"));
});

it.each([0, 1, 2, 1000])("formats tool counts and durations without changing Chinese (%i)", count => {
  const tools = Array.from({ length: count }, () => ({ kind: ToolKind.COMMAND })) as Parameters<typeof toolsSummary>[0];
  expect(toolsSummary(tools)).toBe(`运行了 ${count} 条命令`);
  localeStore.overrideForTesting("en");
  expect(toolsSummary(tools)).toBe(`Ran ${count} ${count === 1 ? "command" : "commands"}`);
  expect(t("image.count", { count })).toBe(`${count} ${count === 1 ? "image" : "images"}`);
  expect(duration(59_000)).toBe("59 sec");
  expect(duration(60_000)).toBe("1 min 0 sec");
  expect(duration(3_600_000)).toBe("1 hr 0 min");
});
