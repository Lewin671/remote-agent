import { renderToStaticMarkup } from "react-dom/server";
import { expect, it } from "vitest";
import { optionLabel, plain, when } from "./model";
import { Markdown, mdBlocks, Rich, tableCells } from "./ui";

it("renders a Markdown subset as text, never HTML", () => {
  const html = renderToStaticMarkup(<Rich text={"见 `a.go` 和 **注意** [README.md](/tmp/x/README.md) <img src=x onerror=alert(1)>"} />);
  expect(html).toContain("<code>a.go</code>");
  expect(html).toContain("<b>注意</b>");
  expect(html).toContain('title="/tmp/x/README.md">README.md</span>');
  expect(html).toContain("&lt;img");
  expect(html).not.toContain("<img");
});

it("makes one-line previews", () => {
  expect(plain("改了 `a.go`，见 [文档](x)")).toBe("改了 a.go，见 文档");
  expect(optionLabel("Go (Recommended)")).toEqual({ text: "Go", recommended: true });
  expect(optionLabel("Python")).toEqual({ text: "Python", recommended: false });
});

it("formats times like the mockup", () => {
  const now = new Date(2026, 8, 29, 9, 41).getTime();
  expect(when(now - 20_000, now)).toBe("刚刚");
  expect(when(now - 3 * 60_000, now)).toBe("3 分钟前");
  expect(when(new Date(2026, 8, 29, 8, 12).getTime(), now)).toBe("8:12");
  expect(when(new Date(2026, 8, 28, 20, 0).getTime(), now)).toBe("昨天");
});

it("tells running tools and terminal-typed messages apart", async () => {
  const { create } = await import("@bufbuild/protobuf");
  const { EventSchema, InputStatus } = await import("./gen/remoteagent/v1/event_pb");
  const { derive } = await import("./model");
  let seq = 0n;
  const ev = (kind: object) => create(EventSchema, { seq: ++seq, kind: kind as never });
  const events = [
    ev({ case: "userMessage", value: { text: "typed at the computer" } }),
    ev({ case: "inputState", value: { requestId: "r", status: InputStatus.SUBMITTED } }),
    ev({ case: "userMessage", value: { text: "sent from the phone" } }),
    ev({ case: "turnStarted", value: {} }),
    ev({ case: "toolStarted", value: { title: "go test" } }),
    ev({ case: "toolStarted", value: { title: "go vet" } }),
    ev({ case: "toolCall", value: { title: "go test" } }),
  ];
  const d = derive(events);
  expect([...d.typedLocally]).toEqual([1n]);
  expect(d.tools.map((t) => t.title)).toEqual(["go vet"]);
  expect(d.running).toBe(true);
});

it("notes mode changes made in the terminal", async () => {
  const { create } = await import("@bufbuild/protobuf");
  const { EventSchema, ApprovalMode } = await import("./gen/remoteagent/v1/event_pb");
  const { derive } = await import("./model");
  let seq = 0n;
  const ev = (kind: object) => create(EventSchema, { seq: ++seq, kind: kind as never });
  const ctx = (reviewer: string, collaboration = "default", model = "m", effort = "low") =>
    ev({ case: "turnContext", value: { approvalsReviewer: reviewer, approvalPolicy: "on-request", sandbox: "workspace-write", collaborationMode: collaboration, model, effort } });
  const events = [
    ctx("user"),
    ctx("auto_review"), // switched in the terminal
    ev({ case: "settingsChanged", value: { mode: ApprovalMode.ASK } }),
    ctx("user"), // switched from the client: already recorded
    ctx("user", "plan", "m", "medium"), // plan mode's own effort is not a model change
  ];
  const d = derive(events);
  expect([...d.terminalChanges.entries()]).toEqual([
    [2n, "模式切到了 自动"],
    [5n, "模式切到了 计划"],
  ]);
});

it("renders Markdown blocks: tables, code, headings", () => {
  const md = [
    "查过官方文档后：",
    "",
    "| 使用入口 | 官方说明 |",
    "| --- | :---: |",
    "| ChatGPT Plus、Pro | 包括 **Codex CLI** |",
    "| OpenAI API | 已有 `gpt-6.1-sol` 模型页，`a|b` 不拆 \\| 这里也不拆 |",
    "",
    "## 结论",
    "- 第一点",
    "```sh",
    "echo a | wc -l",
    "<img src=x onerror=alert(1)>",
    "```",
    "---",
    "完。",
  ].join("\n");
  expect(mdBlocks(md).map((b) => b.kind)).toEqual(["text", "table", "heading", "text", "code", "rule", "text"]);
  const html = renderToStaticMarkup(<Markdown text={md} />);
  expect(html).toContain("<th style=\"text-align:center\">官方说明</th>");
  expect(html).toContain("<td style=\"text-align:center\">包括 <b>Codex CLI</b></td>");
  expect(html).toContain("<code>a|b</code> 不拆 | 这里也不拆");
  expect(html).toContain("• 第一点");
  expect(html).toContain("echo a | wc -l");
  expect(html).toContain("&lt;img");
  expect(html).not.toContain("<img");
});

it("splits table rows", () => {
  expect(tableCells("| a | `x|y` | c \\| d |")).toEqual(["a", "`x|y`", "c | d"]);
  expect(tableCells("a | b")).toEqual(["a", "b"]);
  // An escaped backslash does not escape the pipe after it.
  expect(tableCells("| a\\\\| b |")).toEqual(["a\\\\", "b"]);
  expect(tableCells("| a\\\\\\| | b |")).toEqual(["a\\\\|", "b"]);
});

it("closes a fence only with one at least as long", () => {
  const md = ["````md", "```sh", "ls", "```", "````", "after"].join("\n");
  expect(mdBlocks(md)).toEqual([
    { kind: "code", text: "```sh\nls\n```" },
    { kind: "text", text: "after" },
  ]);
});
