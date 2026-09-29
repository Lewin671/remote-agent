import { renderToStaticMarkup } from "react-dom/server";
import { expect, it } from "vitest";
import { optionLabel, plain, when } from "./model";
import { Rich } from "./ui";

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
