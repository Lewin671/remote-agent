// @vitest-environment happy-dom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { localeStore, t, useLocale } from "../i18n";
import { DemoRemote } from "../dev/demo";
import { modeLabel } from "../model";
import { Agent, ApprovalMode } from "../gen/remoteagent/v1/event_pb";
import { SecurityPage } from "./Security";
import { Settings } from "./Settings";
import { ScheduleDetail } from "./Schedules";
import { groupTasks, taskGroup, frequency, reasonText, scheduleLimits } from "../schedules";
import { ScheduleRevisionError, errorText } from "../remote";
import { SecurityError } from "../device-security";
import { PairingStatusError } from "../pairing";
import { joinBanner } from "./Pairing";
import { ImageError } from "../imagecodec/error";
import { Tray } from "../attach";
import { imageProblem, imageProblemKind } from "../image-problem";
import { scheduledPushContent } from "../sw-notify";
import { t as launchText } from "../i18n/startup";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined, host: HTMLDivElement, remote: DemoRemote;
afterEach(() => {
  act(() => root?.unmount()); root = undefined; host?.remove(); remote?.stop();
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
  const r = demo(), task = r.taskEntries().find(e => e.task.id === "quiet")!.task;
  expect(taskGroup(r.taskEntries().find(e => e.task.id === "attention")!.task)).toBe("attention");
  expect(groupTasks(r.taskEntries()).get("attention")!.map(e => e.task.id)).toContain("attention");
  vi.spyOn(r, "changeSchedule").mockRejectedValue(new ScheduleRevisionError(t("errors.the_task_was_just_changed_updated_to_the_latest")));
  await mount(() => <ScheduleDetail remote={r} machineId="mac" id={task.id} go={() => {}} onBack={() => {}} />);
  await act(async () => button(t("tools.android_edit")).click());
  const input = host.querySelector("textarea")!;
  await act(async () => { Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, "Original user text"); input.dispatchEvent(new Event("input", { bubbles: true })); });
  await act(async () => button(t("common.save")).click());
  expect(host.querySelector(".schedule-editor")).toBeNull();
  expect(host.textContent).toContain(t("errors.the_task_was_just_changed_updated_to_the_latest"));
  expect(r.changeSchedule).toHaveBeenCalledWith("mac", task.id, expect.objectContaining({ spec: expect.objectContaining({ message: "Original user text" }) }));
});

it("keeps local image categories after an English codec error, without exposing its detail", async () => {
  localeStore.overrideForTesting("en");
  const tray = new Tray(async () => { throw new ImageError("too_large", "private codec diagnostic", t("image.image_dimensions_are_too_large"), "dimensions"); }, async () => { throw new Error("unexpected upload"); }, () => {});
  tray.retarget("example-computer");
  tray.add([new Blob(["image"])]);
  await vi.waitFor(() => expect(tray.items[0]?.state).toBe("failed"));
  expect(tray.items[0].error).toBe("dimensions");
  expect(imageProblemKind(tray.items[0].error!)).toBe("dimensions");
  expect(imageProblem(tray.items[0].error!)).toEqual({ message: "Image dimensions are too large. Resize it and retry.", retryable: false });
});

it("localizes safe error and notification shells while preserving external content", async () => {
  localeStore.overrideForTesting("en");
  expect(errorText(new SecurityError("invalid"))).toBe(t("errors.invalid_ciphertext_signature_key_or_content_verification_failed"));
  expect(errorText(new Error("CLI 原始错误"))).toBe("CLI 原始错误");
  expect(reasonText("opaque_reason")).toBe("Reason returned by the computer: opaque_reason");
  expect(joinBanner(new PairingStatusError("refused"))).toMatchObject({ action: "restart", text: "The other device refused this join request" });
  expect(await scheduledPushContent({ tag: "example/NOTIFY_KIND_SCHEDULED" })).toEqual({ title: "remote-agent", body: "Scheduled tasks have new results" });
  expect(await scheduledPushContent({ body: "Original push content" })).toEqual({ title: "remote-agent", body: "Original push content" });
  expect(launchText("startup.the_page_could_not_load_check_your_connection_and", "en")).toBe("The page could not load. Check your connection and retry.");
});

it("uses plural messages for schedule counts and delays without altering saved trigger values", () => {
  localeStore.overrideForTesting("en");
  for (const count of [0, 1, 2, 1000]) {
    const trigger = { type: "interval" as const, interval_seconds: count * 86400, max_runs: count };
    expect(frequency(trigger)).toBe(`Every ${count} ${count === 1 ? "day" : "days"}`);
    if (count) expect(scheduleLimits(trigger)).toBe(`${count} ${count === 1 ? "run" : "runs"} total`);
    expect(trigger.interval_seconds).toBe(count * 86400);
  }
  expect(frequency({ type: "after_turn", delay_seconds: 2, repeat: true })).toBe("2 sec after each turn ends");
});

it("formats the effective approval mode without trimming a translated prefix", () => {
  localeStore.overrideForTesting("en");
  expect(modeLabel(ApprovalMode.UNSPECIFIED, { known: true, mode: ApprovalMode.FULL_ACCESS, varies: false }, Agent.CODEX, false)).toBe("Full access");
  expect(modeLabel(ApprovalMode.UNSPECIFIED, { known: true, mode: ApprovalMode.UNSPECIFIED, varies: false }, Agent.CODEX, false)).toBe("Codex’s own mode");
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
