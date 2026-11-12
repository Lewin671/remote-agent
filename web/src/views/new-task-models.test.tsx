// @vitest-environment happy-dom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { Agent, ApprovalMode } from "../gen/remoteagent/v1/event_pb";
import { ListModelsResponseSchema, DaemonSettingsSchema } from "../gen/remoteagent/v1/local_pb";
import { chooseNewTaskModel, NewTaskModelPanel, NewTaskTools, shouldPinModelControls } from "./NewTaskModels";
import { ConfigSummary } from "./NewSession";

it("pins only when the remaining height fits two actual model rows and padding", () => {
  expect(shouldPinModelControls(228, 132, 44)).toBe(true);
  expect(shouldPinModelControls(227, 132, 44)).toBe(false);
  expect(shouldPinModelControls(228, 132, 52)).toBe(false);
  expect(shouldPinModelControls(204, 132, 32)).toBe(true);
});

it("keeps only explicitly configured effort across models, otherwise leaves the CLI default", () => {
  const info = {
    models: create(ListModelsResponseSchema, { defaultModel: "a", models: [
      { slug: "a", efforts: ["low", "high"], defaultEffort: "low" },
      { slug: "b", efforts: ["low", "high"], defaultEffort: "low" },
      { slug: "c", efforts: ["medium"], defaultEffort: "medium" },
      { slug: "d" },
    ] }),
    settings: create(DaemonSettingsSchema, { defaultEffort: "high" }),
  };
  const cfg = { machineId: "example", cwd: "/example", agent: Agent.CODEX, mode: ApprovalMode.UNSPECIFIED, model: "", effort: "" };
  expect(chooseNewTaskModel(cfg, "b", info).effort).toBe("");
  expect(chooseNewTaskModel(cfg, "c", info).effort).toBe("");
  expect(chooseNewTaskModel(cfg, "d", info).effort).toBe("");
  expect(chooseNewTaskModel({ ...cfg, model: "b", effort: "low" }, "", info).effort).toBe("low");
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
    expect(host.querySelector(".project-summary")?.getAttribute("aria-label")).toBe("模型：A very long model，推理强度：medium");
  } finally {
    act(() => root.unmount()); host.remove(); rect.mockRestore(); client.mockRestore(); scroll.mockRestore();
  }
});


it("selects the context window by stable option values", () => {
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  const onChange = vi.fn();
  const cfg = { machineId: "example", cwd: "/example", agent: Agent.CLAUDE, mode: ApprovalMode.UNSPECIFIED, model: "", effort: "" };
  const info = { models: create(ListModelsResponseSchema), error: "", retry: vi.fn() };
  try {
    act(() => root.render(<NewTaskModelPanel cfg={cfg} info={info} enabled pending={false} onChange={onChange} onClose={() => {}} />));
    const controls = host.querySelectorAll(".seg");
    const buttons = controls[controls.length - 1].querySelectorAll("button");
    act(() => buttons[1].click());
    expect(onChange).toHaveBeenLastCalledWith({ ...cfg, standardContext: true });
    act(() => root.render(<NewTaskModelPanel cfg={{ ...cfg, standardContext: true }} info={info} enabled pending={false} onChange={onChange} onClose={() => {}} />));
    act(() => host.querySelectorAll(".seg button")[0].dispatchEvent(new MouseEvent("click", { bubbles: true })));
    expect(onChange).toHaveBeenLastCalledWith({ ...cfg, standardContext: false });
  } finally {
    act(() => root.unmount()); host.remove();
  }
});
