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
