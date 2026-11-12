// @vitest-environment happy-dom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { Agent, ApprovalMode } from "../gen/remoteagent/v1/event_pb";
import { ListModelsResponseSchema, DaemonSettingsSchema } from "../gen/remoteagent/v1/local_pb";
import { chooseNewTaskModel, NewTaskTools } from "./NewTaskModels";
import { ConfigSummary } from "./NewSession";

it("keeps a supported effective effort, including defaults, otherwise uses the destination default", () => {
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
  expect(chooseNewTaskModel(cfg, "b", info).effort).toBe("high");
  expect(chooseNewTaskModel(cfg, "c", info).effort).toBe("medium");
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
