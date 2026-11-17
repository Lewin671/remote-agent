import { localeStore, t } from "../i18n";
// @vitest-environment happy-dom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { NewSessionModelPanelViewSchema } from "../gen/remoteagent/core/v1/new_session_view_pb";
import { NewTaskModelPanel, NewTaskTools, shouldPinModelControls } from "./NewTaskModels";
import { ConfigSummary } from "./NewSession";

it("pins only when the remaining height fits two actual model rows and padding", () => {
  expect(shouldPinModelControls(228, 132, 44)).toBe(true);
  expect(shouldPinModelControls(227, 132, 44)).toBe(false);
  expect(shouldPinModelControls(228, 132, 52)).toBe(false);
  expect(shouldPinModelControls(204, 132, 32)).toBe(true);
});

it.each([
  { available: 320, modelWidth: 130, compact: "false" },
  { available: 220, modelWidth: 130, compact: "true" },
  { available: 220, modelWidth: 260, compact: "true" },
])("collapses the CLI before truncating a model ($available/$modelWidth)", ({ available, modelWidth, compact }) => {
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  const rect = vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function(this: HTMLElement) {
    return { width: this.matches(".cli") ? 100 : 140 } as DOMRect;
  });
  const client = vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockImplementation(function(this: HTMLElement) {
    return this.matches(".new-task-tools") ? available : 130;
  });
  const scroll = vi.spyOn(HTMLElement.prototype, "scrollWidth", "get").mockImplementation(function(this: HTMLElement) {
    return this.matches(".cli-name") ? 50 : modelWidth;
  });
  try {
    act(() => root.render(<NewTaskTools>
      <button className="cli" aria-label="CLI：Codex"><span className="cli-name">Codex</span></button>
      <ConfigSummary bare summary={{ agent: "Codex", model: "A very long model", effort: "medium" }} />
    </NewTaskTools>));
    expect(host.querySelector(".new-task-tools")?.getAttribute("data-compact")).toBe(compact);
    expect(host.querySelector(".effort")?.textContent).toBe(" · medium");
    expect(host.querySelector(".project-summary")?.getAttribute("aria-label")).toBe(t("models.summary_effort_label", {"model":"A very long model","effort":"medium"}, "zh-CN"));
  } finally {
    act(() => root.unmount()); host.remove(); rect.mockRestore(); client.mockRestore(); scroll.mockRestore();
  }
});


it.each(["zh-CN", "en"] as const)("selects the context window by stable option values in %s", (locale) => {
  localeStore.overrideForTesting(locale);
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  const onSelectModel = vi.fn();
  const basePanel = (standardContext: boolean) =>
    create(NewSessionModelPanelViewSchema, {
      panelEnabled: true,
      supportsContextWindow: true,
      standardContext,
    });
  try {
    act(() => root.render(<NewTaskModelPanel panel={basePanel(false)} selectedModel="" configEffort="" onSelectModel={onSelectModel} onRetryModels={() => {}} onClose={() => {}} />));
    const controls = host.querySelectorAll(".seg");
    const buttons = controls[controls.length - 1].querySelectorAll("button");
    act(() => buttons[1].click());
    expect(onSelectModel).toHaveBeenLastCalledWith("", "", true);
    act(() => root.render(<NewTaskModelPanel panel={basePanel(true)} selectedModel="" configEffort="" onSelectModel={onSelectModel} onRetryModels={() => {}} onClose={() => {}} />));
    act(() => host.querySelectorAll(".seg button")[0].dispatchEvent(new MouseEvent("click", { bubbles: true })));
    expect(onSelectModel).toHaveBeenLastCalledWith("", "", false);
  } finally {
    act(() => root.unmount()); host.remove(); localeStore.overrideForTesting();
  }
});
