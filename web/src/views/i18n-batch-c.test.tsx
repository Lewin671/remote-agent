// @vitest-environment happy-dom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { localeStore, t, useLocale } from "../i18n";
import { CommandError, DemoRemote } from "../dev/demo";
import { SecurityPage } from "./Security";
import { Settings } from "./Settings";
import { ScheduleDetail } from "./Schedules";
import { getCoreHost, sessionDraftScope } from "../core/host";
import { errorText, SecurityError } from "../remote";
import { SecurityWorkflowError } from "../core/security-client";
import { SecurityErrorCode } from "../gen/remoteagent/core/v1/security_pb";
import { joinBanner } from "./Pairing";
import { ImageError } from "../imagecodec/error";
import { renderText } from "../core/text";
import { AttachmentItemState } from "../gen/remoteagent/core/v1/attachments_pb";
import { ImageProblemKind } from "../gen/remoteagent/core/v1/attachments_view_pb";
import { pushContent } from "../sw-notify";
import { t as launchText } from "../i18n/startup";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined, host: HTMLDivElement, remote: DemoRemote;
afterEach(() => {
  act(() => root?.unmount()); root = undefined; host?.remove(); remote?.dispose();
  localeStore.overrideForTesting(); localStorage.clear(); vi.restoreAllMocks();
});
function demo() {
  remote = DemoRemote.create(new URLSearchParams("scheduled-final&role=authority"), async () => {});
  remote.start();
  return remote;
}
async function mount(render: () => React.ReactNode) {
  function Page() { useLocale(); return render(); }
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  await act(async () => root!.render(<Page />));
}
function button(label: string) { return [...host.querySelectorAll("button")].find(b => b.textContent?.trim() === label)!; }

it("updates existing settings without changing the theme or device identity", async () => {
  const r = demo(); localStorage.setItem("ra-theme", "dark");
  await mount(() => <Settings remote={r} go={() => {}} />);
  expect(host.querySelector('[role="radio"][aria-checked="true"]')?.textContent).toBe(t("settings.dark"));
  await act(async () => localeStore.overrideForTesting("en"));
  expect(host.textContent).toContain("Appearance");
  expect(host.querySelector('[role="radio"][aria-checked="true"]')?.textContent).toBe("Dark");
  expect(localStorage.getItem("ra-theme")).toBe("dark");
  expect(r.account.deviceId).toBe(remote.account.deviceId);
});

it.each(["zh-CN", "en"] as const)("uses schedule codes and closes a conflicted editor in %s", async locale => {
  localeStore.overrideForTesting(locale);
  const r = demo();
  const hostCore = getCoreHost(r);
  await vi.waitFor(() => expect(hostCore.viewSchedules().attention.map(t => t.taskId)).toContain("attention"));
  const orig = r.machineCommand.bind(r);
  const spy = vi.spyOn(r, "machineCommand").mockImplementation(async (machineId, kind, slot) => {
    if (kind?.case === "schedule" && kind.value?.operation === "update") {
      throw new CommandError("conflict", t("errors.the_task_was_just_changed_updated_to_the_latest"), true, false, "conflict", "business");
    }
    return orig(machineId, kind, slot);
  });
  await mount(() => <ScheduleDetail remote={r} machineId="mac" id="quiet" go={() => {}} onBack={() => {}} />);
  await vi.waitFor(() => expect(button(t("tools.android_edit"))).toBeTruthy());
  await act(async () => button(t("tools.android_edit")).click());
  const input = host.querySelector("textarea")!;
  await act(async () => { Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, "Original user text"); input.dispatchEvent(new Event("input", { bubbles: true })); });
  await act(async () => button(t("common.save")).click());
  await vi.waitFor(() => expect(host.querySelector(".schedule-editor")).toBeNull());
  expect(host.textContent).toContain(t("errors.the_task_was_just_changed_updated_to_the_latest"));
  expect(spy).toHaveBeenCalledWith("mac", expect.objectContaining({ case: "schedule", value: expect.objectContaining({ operation: "update" }) }), expect.anything());
});

it("keeps local image categories after an English codec error, without exposing its detail", async () => {
  localeStore.overrideForTesting("en");
  const r = demo();
  r.prepareImageOverride = async () => {
    throw new ImageError("too_large", "private codec diagnostic", t("image.image_dimensions_are_too_large"), "dimensions");
  };
  const core = getCoreHost(r);
  const scope = sessionDraftScope("s-idle");
  core.draftOpen(scope, "mac");
  await core.attachments.whenIdle();
  const img = new File(["image"], "huge.png", { type: "image/png" });
  core.stageAndAddDraftItems(scope, [{ file: img, name: img.name, mimeType: img.type, sizeBytes: img.size }], false);
  await vi.waitFor(() => {
    const item = core.viewAttachmentDraft(scope).draft?.items[0];
    expect(item?.state).toBe(AttachmentItemState.FAILED);
  });
  const item = core.viewAttachmentDraft(scope).draft!.items[0];
  expect(item.problem?.kind).toBe(ImageProblemKind.DIMENSIONS);
  expect(item.problem?.retryable).toBe(false);
  expect(renderText(item.problem?.message)).toBe("Image dimensions are too large. Resize it and retry.");
});

it("localizes safe error and notification shells while preserving external content", async () => {
  localeStore.overrideForTesting("en");
  expect(errorText(new SecurityError("invalid"))).toBe(t("errors.invalid_ciphertext_signature_key_or_content_verification_failed"));
  expect(errorText(new Error("CLI 原始错误"))).toBe("CLI 原始错误");
  expect(joinBanner(new SecurityWorkflowError(t("security.pairing_canceled"), SecurityErrorCode.CANCELED, false, false, "restart"))).toMatchObject({ action: "restart", text: "Pairing canceled" });
  expect(await pushContent({ tag: "example/NOTIFY_KIND_SCHEDULED" })).toEqual({ title: "remote-agent", body: "Scheduled tasks have new results" });
  expect(await pushContent({ body: "Original push content" })).toEqual({ title: "remote-agent", body: "Original push content" });
  expect(launchText("startup.the_page_could_not_load_check_your_connection_and", "en")).toBe("The page could not load. Check your connection and retry.");
});

it("reformats an already loaded remote-control expiry when the locale changes", async () => {
  vi.spyOn(Date, "now").mockReturnValue(Date.UTC(2026, 10, 12, 12));
  const r = demo();
  await mount(() => <SecurityPage remote={r} onBack={() => {}} />);
  const expiry = () => [...host.querySelectorAll(".row")].find(el => el.textContent?.includes(t("security.remote_control_expiry")))!.querySelector(".v")!.textContent;
  expect(expiry()).toContain("月");
  await act(async () => localeStore.overrideForTesting("en"));
  expect(expiry()).not.toMatch(/[月日]/);
  expect(expiry()).toMatch(/11\/14/);
});
