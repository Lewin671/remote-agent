// @vitest-environment happy-dom
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { getCoreHost } from "../core/host";
import { localeStore, t, useLocale } from "../i18n";
import * as i18n from "../i18n";
import { ANDROID, DemoRemote } from "../dev/demo";
import { ApprovalMode, OriginKind, OriginSchema, type EventSchema } from "../gen/remoteagent/v1/event_pb";
import { modeOf } from "../model";
import { imageFailureLine, imageProblemKind } from "../image-problem";
import { skeletonNote } from "../skeleton";
import { SessionPage } from "./Session";
import { useRemote } from "../ui";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | undefined, host: HTMLDivElement, remote: DemoRemote;
afterEach(() => {
  act(() => root?.unmount()); root = undefined; host?.remove(); remote?.dispose();
  localeStore.overrideForTesting(); localStorage.clear(); vi.restoreAllMocks(); vi.unstubAllGlobals();
});
async function mount(sid: string) {
  vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
  function Page() {
    useLocale(); useRemote(remote);
    return <SessionPage remote={remote} sessionId={sid} wide onBack={() => {}} />;
  }
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  await act(async () => root!.render(<Page />));
}
function demoPush(sid: string, kind: MessageInitShape<typeof EventSchema>["kind"]) {
  (remote as unknown as { push(sid: string, kind: MessageInitShape<typeof EventSchema>["kind"]): void }).push(sid, kind);
}

it.each(["zh-CN", "en"] as const)("keeps Full access red and sends the original approval in %s", async locale => {
  localeStore.overrideForTesting(locale);
  remote = DemoRemote.create(new URLSearchParams(), async () => {}); remote.start();
  const session = remote.sessions.get("s-appr")!;
  (remote as unknown as { demoSessions: Map<string, { info?: { mode: number } }>; syncDemoToCore(): void }).demoSessions.get("s-appr")!.info!.mode = ApprovalMode.FULL_ACCESS;
  (remote as unknown as { syncDemoToCore(): void }).syncDemoToCore();
  const submit = vi.spyOn(getCoreHost(remote), "sessionSubmitCommand");
  await mount(session.id);
  expect(host.querySelector(".red")?.textContent).toBe(t("modes.full_access"));
  const allow = [...host.querySelectorAll<HTMLButtonElement>("button")].find(b => b.textContent === t("approval.allow"))!;
  expect(allow.disabled).toBe(false);
  await act(async () => allow.click());
  expect(submit).toHaveBeenCalledWith(expect.any(String), session.machineId, session.id, expect.objectContaining({
    kind: expect.objectContaining({ case: "resolveApproval", value: expect.objectContaining({ approvalId: "a1", allow: true }) }),
  }));
});

it("updates existing history and preserves the draft and structured changes on locale override", async () => {
  remote = DemoRemote.create(new URLSearchParams(), async () => {}); remote.start();
  demoPush("s-idle", { case: "turnContext", value: { permissionMode: "default", model: "old-model" } });
  demoPush("s-idle", { case: "turnContext", value: { permissionMode: "auto", model: "new-model" } });
  (remote as unknown as { demoSessions: Map<string, { info?: { mode: number } }>; syncDemoToCore(): void }).demoSessions.get("s-idle")!.info!.mode = ApprovalMode.AUTO;
  (remote as unknown as { syncDemoToCore(): void }).syncDemoToCore();
  remote.drafts.set("s-idle", "User draft 原文");
  await mount("s-idle");
  const input = host.querySelector<HTMLTextAreaElement>("textarea")!;
  expect(host.textContent).toContain(t("session.terminal_mode_change", { value: t("modes.auto") }));
  act(() => localeStore.overrideForTesting("en"));
  expect(host.querySelector("textarea")).toBe(input);
  expect(input.value).toBe("User draft 原文");
  expect(input.getAttribute("aria-label")).toBe("Message");
  expect(host.textContent).toContain("switched approval mode to Auto");
  expect(host.textContent).toContain("new-model");
  expect(modeOf(remote.sessions.get("s-idle")!)?.mode).toBe(ApprovalMode.AUTO);
});

it.each(["zh-CN", "en"] as const)("uses origin IDs even if another device is named like the self label in %s", async locale => {
  localeStore.overrideForTesting(locale);
  remote = DemoRemote.create(new URLSearchParams(), async () => {}); remote.start();
  const selfLabel = locale === "en" ? "This client" : "本客户端";
  (remote as unknown as { metas: Map<string, { meta: { name: string } }> }).metas.get(ANDROID)!.meta.name = selfLabel;
  await remote.loadDevices();
  demoPush("s-idle", { case: "settingsChanged", value: { mode: ApprovalMode.FULL_ACCESS } });
  const list = (remote as unknown as { evs: Map<string, { origin?: ReturnType<typeof create<typeof OriginSchema>> }[]> }).evs.get("s-idle")!;
  list.at(-1)!.origin = create(OriginSchema, { kind: OriginKind.CLIENT, deviceId: ANDROID });
  (remote as unknown as { syncDemoToCore(force?: boolean): void }).syncDemoToCore(true);
  await mount("s-idle");
  expect(host.textContent).toContain(t("session.mode_changed_by", { origin: selfLabel, value: t("modes.full_access") }));
});

it.each(["zh-CN", "en"] as const)("never enables approval of a skeleton in %s", async locale => {
  localeStore.overrideForTesting(locale);
  remote = DemoRemote.create(new URLSearchParams(), async () => {}); remote.start();
  const submit = vi.spyOn(getCoreHost(remote), "sessionSubmitCommand");
  demoPush("s-idle", { case: "approvalRequested", value: { approvalId: "a-skel", description: "run cmd" + skeletonNote, command: "rm -rf" + skeletonNote } });
  await mount("s-idle");
  expect(host.textContent).toContain(t("session.skeleton"));
  expect([...host.querySelectorAll("button")].some(b => b.textContent === t("approval.allow"))).toBe(false);
  expect(submit).not.toHaveBeenCalled();
});

it.each(["zh-CN", "en"] as const)("resumes the same paused queue item in %s", async locale => {
  localeStore.overrideForTesting(locale);
  remote = DemoRemote.create(new URLSearchParams("queue-all=1"), async () => {}); remote.start();
  const cmd = vi.spyOn(remote, "command");
  await mount("s-multi");
  const more = host.querySelector<HTMLButtonElement>(".ptray-more");
  if (more) await act(async () => more.click());
  const paused = [...host.querySelectorAll<HTMLElement>(".ptray-row")].find(el => el.textContent?.includes("中断前排着的这条消息需要手动发送。"))!;
  const send = [...paused.querySelectorAll<HTMLButtonElement>("button")].find(b => b.textContent === t("composer.send"))!;
  expect(send.disabled).toBe(false);
  await act(async () => send.click());
  await vi.waitFor(() => expect(cmd).toHaveBeenCalledTimes(1));
  expect(cmd.mock.calls[0][0]).toBe("s-multi");
  expect(cmd.mock.calls[0][1]).toMatchObject({
    case: "resumeInput",
    value: { sessionId: "s-multi", requestId: "q-paused" },
  });
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

it.each([0, 1, 2, 1000])("formats image count plurals in en (%i)", count => {
  localeStore.overrideForTesting("en");
  expect(t("image.count", { count })).toBe(`${count} ${count === 1 ? "image" : "images"}`);
});
