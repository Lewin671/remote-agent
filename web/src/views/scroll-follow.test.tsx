// @vitest-environment happy-dom
// A reader who has scrolled up is never pulled back to the end by what
// arrives from the other side; near the end counts as being at the end.

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { VirtuosoMockContext } from "react-virtuoso";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { DemoRemote } from "../dev/demo";
import { EventSchema } from "../gen/remoteagent/v1/event_pb";
import { SessionPage } from "./Session";
import { followsEnd, NEAR_END, type Scrolled } from "./scroll-follow";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

describe("followsEnd", () => {
  const at = (top: number, height = 2000, client = 400): Scrolled => ({ top, height, client });
  const now = 10_000;

  it("follows within a line or so of the end, however it got there", () => {
    expect(followsEnd(false, at(1500), at(2000 - 400 - (NEAR_END - 1)), 0, now)).toBe(true);
    expect(followsEnd(true, at(1600), at(1600 - 30), now, now)).toBe(true);
  });

  it("stops when the reader scrolled away", () => {
    expect(followsEnd(true, at(1600), at(1200), now - 100, now)).toBe(false);
  });

  it("stops at a scroll up nothing else explains, without a wheel or a key", () => {
    expect(followsEnd(true, at(1600), at(1200), 0, now)).toBe(false);
    expect(followsEnd(true, at(1600), at(1600 - (NEAR_END + 1)), 0, now)).toBe(false);
  });

  it("is not stopped by the list measuring rows or older records joining", () => {
    expect(followsEnd(true, at(1600, 2000), at(1500, 2100), 0, now)).toBe(true);
    expect(followsEnd(true, at(1600, 2000, 400), at(1500, 2000, 300), 0, now)).toBe(true);
    expect(followsEnd(true, undefined, at(1200), 0, now)).toBe(true);
  });

  it("stays off until the reader is back near the end", () => {
    expect(followsEnd(false, at(1200), at(1250), 0, now)).toBe(false);
    expect(followsEnd(false, at(1250), at(1500), 0, now)).toBe(false);
    expect(followsEnd(false, at(1500), at(1560), 0, now)).toBe(true);
  });
});

describe("the chat of a session that is still talking", () => {
  let root: Root | undefined;
  let host: HTMLElement;
  const observers: { cb: (entries: unknown[]) => void; seen: Element[] }[] = [];
  const realObserver = globalThis.ResizeObserver;
  beforeEach(() => {
    globalThis.ResizeObserver = class {
      private me: (typeof observers)[number];
      constructor(cb: (entries: unknown[]) => void) { this.me = { cb, seen: [] }; observers.push(this.me); }
      observe(el: Element) { this.me.seen.push(el); }
      unobserve() {}
      disconnect() {}
    } as unknown as typeof ResizeObserver;
  });
  afterEach(() => {
    act(() => root?.unmount());
    host?.remove();
    root = undefined;
    localStorage.clear();
    observers.length = 0;
    globalThis.ResizeObserver = realObserver;
  });

  /** A chat of `turns` turns, scrolled to the end, with the layout the browser would give it. */
  async function open(turns: number) {
    const r = DemoRemote.create(new URLSearchParams(), async () => undefined);
    r.start();
    const sid = "s-idle";
    const list = r.events(sid);
    const add = (kind: object) => list.push(create(EventSchema, { seq: BigInt(list.length + 1), kind: kind as never }));
    for (let i = 0; i < turns; i++) {
      add({ case: "userMessage", value: { text: `question ${i}` } });
      add({ case: "assistantMessage", value: { text: `answer ${i}` } });
    }
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    await act(async () => root!.render(
      <VirtuosoMockContext.Provider value={{ viewportHeight: 400, itemHeight: 50 }}>
        <SessionPage remote={r} session={r.sessions.get(sid)!} onBack={() => undefined} wide={false} />
      </VirtuosoMockContext.Provider>));
    const el = host.querySelector<HTMLElement>(".body.chat")!;
    let top = 0;
    const sets: number[] = [];
    // Plain lists are as tall as their rows; the virtual one says so.
    const height = () => Number.parseFloat((el.querySelector(".virtual-history") as HTMLElement | null)?.style.height ?? "") || list.length * 50 + 200;
    Object.defineProperties(el, {
      clientHeight: { configurable: true, get: () => 400 },
      scrollHeight: { configurable: true, get: () => height() },
      scrollTop: { configurable: true, get: () => top, set: (v: number) => { top = Math.min(v, height() - 400); sets.push(top); } },
    });
    const scrollTo = async (to: number, how: "script" | "wheel" = "script") => {
      await act(async () => {
        if (how === "wheel") el.dispatchEvent(new Event("wheel", { bubbles: true }));
        top = to;
        el.dispatchEvent(new Event("scroll", { bubbles: true }));
      });
    };
    const theyWrite = async (text: string) => {
      add({ case: "assistantMessage", value: { text } });
      await act(async () => { (r as unknown as { changed(): void }).changed(); });
    };
    await scrollTo(height() - 400); // at the end: following
    /** The browser reports a resize of the content, with no scroll event. */
    const resized = async () => { await act(async () => { for (const o of observers) if (o.seen.includes(el)) o.cb([{ target: el, contentRect: { height: 400 } }]); }); };
    const stick = () => r.scroll.get(sid)?.stick;
    return { el, scrollTo, theyWrite, resized, stick, top: () => top, sets, height };
  }

  for (const [label, turns] of [["plain list", 20], ["virtual list", 120]] as const) {
    describe(label, () => {
      it("is not pulled back when messages arrive after it was scrolled up by a script", async () => {
        const c = await open(turns);
        expect(!!c.el.querySelector(".virtual-history")).toBe(turns > 50);
        expect(c.stick()).toBe(true);
        const before = c.height() - 400 - 400;
        await c.scrollTo(before);
        expect(c.stick()).toBe(false);
        c.sets.length = 0;
        await c.theyWrite("a new reply");
        await c.theyWrite("another one");
        expect(c.top()).toBe(before);
        expect(c.sets).toEqual([]);
      });

      it("is not pulled back when the page had grown since the last scroll event", async () => {
        const c = await open(turns);
        await c.theyWrite("a reply while it was at the end");
        await c.theyWrite("and another");
        await c.resized();
        const before = c.height() - 400 - 400;
        await c.scrollTo(before);
        expect(c.stick()).toBe(false);
        await c.theyWrite("a new reply");
        expect(c.top()).toBe(before);
      });

      it("is not pulled back after a wheel scroll either", async () => {
        const c = await open(turns);
        const before = c.height() - 400 - 400;
        await c.scrollTo(before, "wheel");
        expect(c.stick()).toBe(false);
        await c.theyWrite("a new reply");
        expect(c.top()).toBe(before);
      });

      it("still follows when it is only a few lines from the end", async () => {
        const c = await open(turns);
        const end = c.height() - 400;
        await c.scrollTo(end - 30, "wheel");
        expect(c.stick()).toBe(true);
        await c.scrollTo(end - 30);
        expect(c.stick()).toBe(true);
      });
    });
  }
});
