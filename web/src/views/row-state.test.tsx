// @vitest-environment happy-dom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { SessionToolCallItemSchema, SessionToolGroupItemSchema } from "../gen/remoteagent/core/v1/session_view_pb";
import { TextSchema } from "../gen/remoteagent/core/v1/text_pb";
import { RowState, RememberDetails } from "./RowState";
import { ToolGroupView } from "./Tools";
import { GenCard } from "./Images";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
it("preserves expanded tools, prompts and details when a row leaves the viewport", async () => {
  const host = document.createElement("div"); document.body.append(host); const root = createRoot(host);
  const states = new Map<string, boolean>();
  const row = <RowState id="row" states={states}>
    <ToolGroupView group={create(SessionToolGroupItemSchema, { summary: create(TextSchema, { literal: "2 calls" }), calls: [create(SessionToolCallItemSchema, { title: "one" }), create(SessionToolCallItemSchema, { title: "two" })] })} />
    <GenCard refs={[{ preview: new Uint8Array(), previewFormat: 0 } as any]} prompt="image prompt" />
    <RememberDetails name="thought"><summary>thought</summary>reasoning text</RememberDetails>
  </RowState>;
  try {
    await act(async () => root.render(row));
    await act(async () => { host.querySelector<HTMLButtonElement>(".tgrp")!.click(); host.querySelector<HTMLButtonElement>(".gen-card button.link")!.click(); });
    await act(async () => { const details = host.querySelector("details")!; details.open = true; details.dispatchEvent(new Event("toggle")); });
    await act(async () => root.render(null));
    await act(async () => root.render(row));
    expect(host.querySelector(".tgrp")?.getAttribute("aria-expanded")).toBe("true");
    expect(host.querySelector(".gen-prompt")?.textContent).toBe("image prompt");
    expect(host.querySelector("details")?.open).toBe(true);
  } finally { await act(async () => root.unmount()); host.remove(); }
});

it("preserves each code block's expansion independently across viewport remounts", async () => {
  const { Markdown } = await import("../markdown");
  const host = document.createElement("div"); document.body.append(host); const root = createRoot(host);
  const states = new Map<string, boolean>();
  const code = Array.from({ length: 120 }, (_, i) => "line " + i).join("\n");
  const row = <RowState id="row" states={states}><Markdown text={'```go\n' + code + '\n```\n\n> ```js\n' + code.split('\n').map(x => '> ' + x).join('\n') + '\n> ```'} /></RowState>;
  try {
    await act(async () => root.render(row));
    expect(host.querySelectorAll(".cbmore")).toHaveLength(2);
    await act(async () => host.querySelector<HTMLButtonElement>(".cbmore")!.click());
    await act(async () => root.render(null));
    await act(async () => root.render(row));
    expect([...host.querySelectorAll(".cbmore")].map(b => b.getAttribute("aria-expanded"))).toEqual(["true", "false"]);
  } finally { await act(async () => root.unmount()); host.remove(); }
});
