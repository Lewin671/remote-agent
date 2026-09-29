import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { Markdown, mdBlocks, Rich, safeHref, tableCells } from "./markdown";

const html = (md: string) => renderToStaticMarkup(<Markdown text={md} />);

describe("safety", () => {
  it("never renders HTML from the text", () => {
    const out = html('<img src=x onerror=alert(1)>\n\n<script>alert(1)</script>\n\n- <b onclick="x">b</b>\n\n```\n<iframe>\n```');
    expect(out).not.toMatch(/<(img|script|iframe|b onclick)/);
    expect(out).toContain("&lt;img");
    expect(out).toContain("&lt;script&gt;");
  });

  it("links only to web pages", () => {
    expect(safeHref("https://example.com/a?b=1")).toBe("https://example.com/a?b=1");
    expect(safeHref("javascript:alert(1)")).toBeUndefined();
    expect(safeHref("JAVASCRIPT:alert(1)")).toBeUndefined();
    expect(safeHref("data:text/html,<b>")).toBeUndefined();
    expect(safeHref("/Users/me/foo/a.go:58")).toBeUndefined();
    const out = renderToStaticMarkup(<Rich text={"[docs](https://example.com) [x](javascript:alert(1)) <https://a.b/c>"} />);
    expect(out).toContain('<a href="https://example.com" target="_blank" rel="noopener noreferrer nofollow">docs</a>');
    expect(out).toContain('<a href="https://a.b/c"');
    expect(out).not.toContain("javascript:alert(1)\"");
    expect(out).not.toMatch(/href="javascript/);
  });

  it("shows file references as monospace, not links", () => {
    const out = renderToStaticMarkup(<Rich text={"问题在 [auth/login.go:58](/Users/me/foo/auth/login.go:58)"} />);
    expect(out).toContain('<span class="ref" title="/Users/me/foo/auth/login.go:58">auth/login.go:58</span>');
    expect(out).not.toContain("<a");
  });
});

describe("blocks", () => {
  it("renders inline marks", () => {
    const out = renderToStaticMarkup(<Rich text={"见 `a.go` 和 **注意**，*斜体*，但 a*b*c 和 2 * 3 * 4 不是"} />);
    expect(out).toContain("<code>a.go</code>");
    expect(out).toContain("<b>注意</b>");
    expect(out).toContain("<em>斜体</em>");
    expect(out).toContain("a*b*c 和 2 * 3 * 4 不是");
  });

  it("renders lists as lists", () => {
    const out = html("原因：\n- 每次登录都新建连接\n- 连接没设超时\n  池子耗尽后挂住\n\n1. 改\n2. 测");
    expect(out).toBe(
      "<p><span>原因：</span></p><ul><li><span>每次登录都新建连接</span></li><li><span>连接没设超时</span><span><br/>池子耗尽后挂住</span></li></ul>" +
        '<ol start="1"><li><span>改</span></li><li><span>测</span></li></ol>',
    );
  });

  it("nests lists and code in list items", () => {
    const md = ["- 运行：", "", "    ```sh", "    ls | wc -l", "    ```", "- 完成", "  - 子项"].join("\n");
    const b = mdBlocks(md);
    expect(b.map((x) => x.kind)).toEqual(["list"]);
    const list = b[0];
    if (list.kind !== "list") throw new Error();
    expect(list.items.map((i) => i.text)).toEqual(["运行：", "完成"]);
    expect(list.items[0].children).toEqual([{ kind: "code", text: "ls | wc -l" }]);
    expect(list.items[1].children.map((c) => c.kind)).toEqual(["list"]);
  });

  it("renders tables, code, headings, rules and quotes", () => {
    const md = [
      "查过官方文档后：",
      "",
      "| 使用入口 | 官方说明 |",
      "| --- | :---: |",
      "| ChatGPT Plus、Pro | 包括 **Codex CLI** |",
      "| OpenAI API | 已有 `gpt-6.1-sol` 模型页，`a|b` 不拆 \\| 这里也不拆 |",
      "",
      "## 结论",
      "```sh",
      "echo a | wc -l",
      "```",
      "---",
      "> 引用",
      "完。",
    ].join("\n");
    expect(mdBlocks(md).map((b) => b.kind)).toEqual(["text", "table", "heading", "code", "rule", "quote", "text"]);
    const out = html(md);
    expect(out).toContain('<th style="text-align:center">官方说明</th>');
    expect(out).toContain('<td style="text-align:center">包括 <b>Codex CLI</b></td>');
    expect(out).toContain("<code>a|b</code> 不拆 | 这里也不拆");
    expect(out).toContain("<pre>echo a | wc -l</pre>");
    expect(out).toContain("<blockquote>");
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

  it("does not hang on unclosed blocks", () => {
    expect(mdBlocks("```\nopen").map((b) => b.kind)).toEqual(["code"]);
    expect(mdBlocks("> a\n> b").map((b) => b.kind)).toEqual(["quote"]);
    expect(mdBlocks("- a\n\n\n").map((b) => b.kind)).toEqual(["list"]);
  });
});
