// @vitest-environment happy-dom
import { expect, it, vi } from "vitest";
import { watchViewport } from "./viewport";

it("follows keyboard resize and Safari pan without reflowing pinch zoom", () => {
  const viewport = Object.assign(new EventTarget(), { height: 700, offsetTop: 0, scale: 1 });
  vi.stubGlobal("visualViewport", viewport);
  const stop = watchViewport();
  const style = document.documentElement.style;
  try {
    viewport.height = 400;
    viewport.dispatchEvent(new Event("resize"));
    expect(style.getPropertyValue("--viewport-height")).toBe("400px");
    viewport.offsetTop = 40;
    viewport.dispatchEvent(new Event("scroll"));
    expect(style.getPropertyValue("--viewport-top")).toBe("40px");
    viewport.scale = 2;
    viewport.height = 200;
    viewport.dispatchEvent(new Event("resize"));
    expect(style.getPropertyValue("--viewport-height")).toBe("400px");
    viewport.scale = 1;
    viewport.height = 700;
    viewport.offsetTop = 0;
    viewport.dispatchEvent(new Event("resize"));
    expect(style.getPropertyValue("--viewport-height")).toBe("700px");
    expect(style.getPropertyValue("--viewport-top")).toBe("0px");
  } finally {
    stop();
    vi.unstubAllGlobals();
  }
});
