import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { Markdown, mdBlocks, numericColumn, Rich, safeHref, tableCells } from "./markdown";

const html = (md: string) => renderToStaticMarkup(<Markdown text={md} />);

// Complexity is checked with operation counts and regex analysis in
// markdown.complexity.test.ts; these large cases check the rendered result.
describe("safety", () => {
  it("renders many unclosed link openers without quadratic scans", () => {
    for (const text of ["[".repeat(100_000), "[x](".repeat(25_000), "<https://".repeat(12_000)]) {
      const out = renderToStaticMarkup(<Rich text={text} />);
      expect(out).not.toContain("<a ");
    }
  });

  it("renders unclosed emphasis, strikes and URLs correctly", () => {
    // Rendering correctness needs representative token counts, not 45,000
    // React nodes. The parser's growing-input work counts remain covered by
    // markdown.complexity.test.ts, independently of CPU speed and scheduling.
    for (const [text, links, strikes] of [
      ["_a ".repeat(128), 0, 0],
      ["~~a ".repeat(128), 0, 64],
      ["*a ".repeat(128), 0, 0],
      ["https://a.b/" + "(".repeat(1024), 1, 0],
      ["https://a.b/" + ")".repeat(1024), 1, 0],
      ["https://a.b ".repeat(128) + ".".repeat(1024), 128, 0],
    ] as const) {
      const out = renderToStaticMarkup(<Rich text={text} />);
      expect(out.match(/<a /g)?.length ?? 0).toBe(links);
      expect(out.match(/<s>/g)?.length ?? 0).toBe(strikes);
      if (!links && !strikes) expect(out).toBe(text);
    }
  });

  it("reads long hostile lines once", () => {
    expect(mdBlocks("# " + "#".repeat(100_000) + "x")).toEqual([{ kind: "heading", level: 1, text: "#".repeat(100_000) + "x" }]);
    expect(mdBlocks("# " + "# ".repeat(50_000) + "x")[0].kind).toBe("heading");
    expect(mdBlocks("a|b\n" + " ".repeat(100_000) + "x")[0].kind).toBe("text");
    expect(mdBlocks("a|b\n" + "| ".repeat(50_000) + "x")[0].kind).toBe("text");
    expect(mdBlocks("- " + " ".repeat(100_000) + "x")[0].kind).toBe("list");
    for (const [cell, want] of [["1" + " 1".repeat(50_000) + " x", true], ["1" + " ".repeat(100_000) + "x", false], ["1" + "a ".repeat(50_000), false]] as const) {
      expect(numericColumn([cell])).toBe(want);
    }
  });

  it("parses long lists without suffix copies or argument-stack overflow", () => {
    const separate = mdBlocks("- x\n\n".repeat(20_000));
    expect(separate[0].kind).toBe("list");
    const indented = mdBlocks("- x\n\n" + "  y\n".repeat(150_000));
    expect(indented[0].kind).toBe("list");
  });

  it("keeps invalid openers as text and finds later valid links", () => {
    const out = renderToStaticMarkup(<Rich text={"[unclosed] text [ok](https://a.b) <https://> [bad](with space)"} />);
    expect(out).toContain('[unclosed] text <a href="https://a.b"');
    expect(out).toContain("&lt;https://&gt; [bad](with space)");
  });

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
      "<p><span>原因：</span></p><ul class=\"loose\"><li><span>每次登录都新建连接</span></li><li><span>连接没设超时</span><span><br/>池子耗尽后挂住</span></li></ul>" +
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
    expect(list.items[0].children).toEqual([{ kind: "code", lang: "sh", text: "ls | wc -l" }]);
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
    expect(out).toContain('<th style="text-align:center"><div class="mdcell nowrap">官方说明</div></th>');
    expect(out).toContain('<td style="text-align:center"><div class="mdcell">包括 <b>Codex CLI</b></div></td>');
    expect(out).toContain("<code>a|b</code> 不拆 | 这里也不拆");
    expect(out).toContain('<pre><span class="cbt">echo a | wc -l</span></pre>');
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
      { kind: "code", lang: "md", text: "```sh\nls\n```" },
      { kind: "text", text: "after" },
    ]);
  });

  it("does not hang on unclosed blocks", () => {
    expect(mdBlocks("```\nopen").map((b) => b.kind)).toEqual(["code"]);
    expect(mdBlocks("> a\n> b").map((b) => b.kind)).toEqual(["quote"]);
    expect(mdBlocks("- a\n\n\n").map((b) => b.kind)).toEqual(["list"]);
  });
});

it("keeps code pipes in one column and literal unmatched ticks do not swallow columns", () => {
  expect(tableCells("| ``x`|y`` | z |")).toEqual(["``x`|y``", "z"]);
  expect(tableCells("| unfinished ` | z |")).toEqual(["unfinished `", "z"]);
});
it("does not render a separator missing header columns as a table", () => {
  expect(mdBlocks("| a | b |\n| --- |\n| 1 | 2 |").some((b) => b.kind === "table")).toBe(false);
});
it("renders the five-column CLI table with a surplus separator as a table", () => {
  const out = html([
    "| 技术路线 | 工作方式 | 优点 | 缺点与边界 | 代表方案 |",
    "|---|---|---|---|---|---|",
    "| **WebDriverAgent（WDA）** | XCTest runner | 控件树 | 需签名 | [文档](https://example.com) |",
  ].join("\n"));
  expect((out.match(/<th>/g) ?? []).length).toBe(5);
  expect((out.match(/<td>/g) ?? []).length).toBe(5);
  expect(out).toContain("<b>WebDriverAgent（WDA）</b>");
  expect(out).toContain('href="https://example.com"');
  expect(out).not.toContain("|---|");
});
it("keeps an empty or short row aligned to its header and preserves inline formatting", () => {
  const out = html("| label | value | empty |\n| --- | ---: | :---: |\n| long label | **74.33** |\n| `a\\|b` | [docs](https://example.com) | |");
  expect((out.match(/<td(?: |>)/g) ?? []).length).toBe(6);
  expect(out).toContain('<td style="text-align:center"><div class="mdcell"></div></td>');
  expect(out).toContain('<div class="mdcell nowrap"><b>74.33</b></div>');
  expect(out).toContain('<code>a|b</code>');
  expect(out).toContain('href="https://example.com"');
});

it("renders a multi-backtick code cell without losing literal backticks", () => {
  const out = html("| code | value |\n| --- | --- |\n| ``x`|y`` | z |");
  expect(out).toContain('<code>x`|y</code>');
  expect(out).not.toContain('`<code>');
  expect(renderToStaticMarkup(<Rich text="unfinished `" />)).toBe('unfinished `');
});

it("does not let escaped backticks swallow neighboring table columns", () => {
  expect(tableCells("| \\`not code | middle \\` | final |")).toEqual(["\\`not code", "middle \\`", "final"]);
  expect(renderToStaticMarkup(<Rich text={"\\`not code | middle \\`"} />)).toBe('`not code | middle `');
});

it("resumes inline formatting after code containing Markdown markers", () => {
  expect(renderToStaticMarkup(<Rich text={'`**` **bold**'} />)).toBe('<code>**</code> <b>bold</b>');
  expect(renderToStaticMarkup(<Rich text={'**`termFirst`**：保持 *见 `a.go`*'} />)).toBe('<b><code>termFirst</code></b>：保持 <em>见 <code>a.go</code></em>');
  expect(renderToStaticMarkup(<Rich text={'**见 [文档](https://example.com/a)**'} />)).toContain('<b>见 <a href="https://example.com/a"');
  expect(renderToStaticMarkup(<Rich text={'``* ` | **`` **after** [docs](https://example.com)'} />)).toContain('<code>* ` | **</code> <b>after</b>');
});


it("bounds nesting in attacker-controlled CLI Markdown", () => {
  const out = renderToStaticMarkup(<Markdown text={"> ".repeat(10_000) + "payload"} />);
  expect(out).toContain("payload");
  expect((out.match(/<blockquote>/g) ?? []).length).toBe(32);
});

describe("a reply's elements (design-system §7)", () => {
  it("marks heading levels, and a lone bold line as a small heading", () => {
    const out = html("# a\n\n### b\n\n###### c\n\n**小标题**\n\n**加粗**后面有字");
    expect(out).toContain('<p class="mdh h1">');
    expect(out).toContain('<p class="mdh h3">');
    expect(out).toContain('<p class="mdh h4">');
    expect(out).toContain('<p class="bh"><span><b>小标题</b></span></p>');
    expect(out).toContain("<p><span><b>加粗</b>后面有字</span></p>");
  });

  it("draws task items as boxes, without the brackets", () => {
    const out = html("- [x] 做完\n- [ ] 没做\n- 普通");
    expect(out).toMatch(/<li class="task done"><svg[^>]*lucide-square-check/);
    expect(out).toMatch(/<li class="task"><svg[^>]*lucide-square\b/);
    expect(out).not.toContain("[x]");
    expect(out).toContain("<li><span>普通</span></li>");
  });

  it("widens the gutter of a list whose numbers reach two digits", () => {
    expect(html("9. a\n10. b")).toContain('<ol start="9" class="wide">');
    expect(html("1. a\n2. b")).toContain('<ol start="1">');
  });

  it("sets a list's items further apart once one of them wraps", () => {
    expect(html("- 短\n- 也短")).toContain("<ul><li>");
    expect(html("- 短\n- 预留和提交之间进程崩溃会留下悬挂的预留，需要一个定时清理")).toContain('<ul class="loose">');
    expect(html("9. a\n10. " + "x".repeat(41))).toContain('<ol start="9" class="wide loose">');
  });

  it("gives code a copy button, and folds only long code", () => {
    const short = html("```sh\n" + "x\n".repeat(16) + "```");
    expect(short).toContain('aria-label="复制代码"');
    expect(short).not.toContain("展开全文");
    const long = html("```\n" + Array.from({ length: 17 }, (_, i) => "line" + i).join("\n") + "\n```");
    expect(long).toContain("展开全文");
    expect(long).toContain("共 17 行");
    expect(long).toContain("line11");
    expect(long).not.toContain("line12");
  });

  it("colors only a diff's added and removed lines", () => {
    const out = html("```diff\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old\n+new\n same\n---n;\n+++n;\n```");
    expect(out).toContain('<span class="del">\n---n;</span>');
    expect(out).toContain('<span class="add">\n+++n;</span>');
    expect(out).toContain('<span class="del">\n-old</span>');
    expect(out).toContain('<span class="add">\n+new</span>');
    expect(out).toContain("<span>--- a/x</span>");
    expect(out).toContain("<span>\n+++ b/x</span>");
    expect(html("```go\n+x\n```")).not.toContain("add");
  });

  it("right-aligns a column of quantities unless the table says otherwise", () => {
    const out = html("| 场景 | P99 | 备注 |\n| --- | --- | --- |\n| 50 并发 | 42 ms | 快 |\n| 200 并发 | 5.2 s | — |");
    expect(out).toContain('<th class="num"><div class="mdcell nowrap">P99</div></th>');
    expect(out).toContain('<td class="num"><div class="mdcell nowrap">42 ms</div></td>');
    expect(out).toContain('<td><div class="mdcell nowrap">快</div></td>');
    const said = html("| a | b |\n| :--- | --- |\n| 1 | 2 |");
    expect(said).toContain('<td style="text-align:left"><div class="mdcell nowrap">1</div></td>');
    expect(said).toContain('<td class="num"><div class="mdcell nowrap">2</div></td>');
  });

  it("leaves out a rule that opens the reply, and keeps the others for their space", () => {
    expect(html("---\n\n---\n说明\n\n---\n完")).toBe("<p><span>说明</span></p><hr/><p><span>完</span></p>");
  });

  it("right-aligns quantities whatever marks they wear", () => {
    const out = html("| a | b |\n| --- | --- |\n| x | **42 ms** |\n| y | `61` |");
    expect(out).toContain('<td class="num"><div class="mdcell nowrap"><b>42 ms</b></div></td>');
  });

  it("strikes text and links bare URLs", () => {
    const out = renderToStaticMarkup(<Rich text={"~~旧~~ 见 https://example.com/a。"} />);
    expect(out).toBe('<s>旧</s> 见 <a href="https://example.com/a" target="_blank" rel="noopener noreferrer nofollow">https://example.com/a</a>。');
  });
});
