import { t as message } from "../i18n";
// @vitest-environment happy-dom
// A reader who has scrolled up is never pulled back to the end by what
// arrives from the other side; near the end counts as being at the end.

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { VirtuosoMockContext } from "react-virtuoso";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { DemoRemote } from "../dev/demo";
import {
  SessionHeaderViewSchema,
  SessionTimelineItemSchema,
  SessionTimelineViewSchema,
  SessionToolStatus,
  SessionViewModelSchema,
  type SessionTimelineItem,
} from "../gen/remoteagent/core/v1/session_view_pb";
import { EventSchema } from "../gen/remoteagent/v1/event_pb";
import { patchSyncState } from "../test/remote-fixture";
import { SessionPage, SessionScreenView } from "./Session";
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
    const sid = "s-idle";
    const list = (r as unknown as { demoEvents(id: string): ReturnType<typeof create<typeof EventSchema>>[] }).demoEvents(sid);
    const add = (kind: object) => list.push(create(EventSchema, { seq: BigInt(list.length + 1), kind: kind as never }));
    for (let i = 0; i < turns; i++) {
      add({ case: "userMessage", value: { text: `question ${i}` } });
      add({ case: "agentMessage", value: { text: `answer ${i}` } });
    }
    const demoSess = (r as unknown as { demoSessions: Map<string, { lastSeq: bigint }> }).demoSessions.get(sid);
    if (demoSess) demoSess.lastSeq = BigInt(list.length);
    r.start();
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    await act(async () => root!.render(
      <VirtuosoMockContext.Provider value={{ viewportHeight: 400, itemHeight: 50 }}>
        <SessionPage remote={r} sessionId={sid} onBack={() => undefined} wide={false} />
      </VirtuosoMockContext.Provider>));
    const el = host.querySelector<HTMLElement>(".body.chat")!;
    // The mock has no row layout to measure; expose the positioned list.
    el.classList.remove("mo-hold");
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
      await act(async () => {
        (r as unknown as { push(id: string, kind: object): void }).push(sid, { case: "agentMessage", value: { text } });
      });
    };
    await scrollTo(height() - 400); // at the end: following
    /** The browser reports a resize of the content, with no scroll event. */
    const resized = async () => { await act(async () => { for (const o of observers) if (o.seen.includes(el)) o.cb([{ target: el, contentRect: { height: 400 } }]); }); };
    const stick = () => r.scroll.get(sid)?.stick;
    return { el, scrollTo, theyWrite, resized, stick, top: () => top, sets, height };
  }

  it("does not replace a reading position with transient virtual-list positioning", async () => {
    const c = await open(20);
    const end = c.height() - 400;
    await c.scrollTo(end - 400, "wheel");
    expect(c.stick()).toBe(false);
    c.el.classList.add("mo-hold");
    await c.scrollTo(end);
    expect(c.stick()).toBe(false);
    c.el.classList.remove("mo-hold");
    await c.scrollTo(end);
    expect(c.stick()).toBe(true);
  });

  it("offers the latest messages while reading old history, with or without new replies", async () => {
    const c = await open(20);
    const button = () => host.querySelector<HTMLButtonElement>("button.newmsg");
    const end = () => c.height() - 400;
    expect(button()).toBeNull();
    await c.scrollTo(end() - 30, "wheel");
    expect(button()).toBeNull();
    await c.scrollTo(end() - 400, "wheel");
    expect(button()?.textContent).toContain(message("session.latest"));
    c.el.scrollTo = (options) => {
      if (typeof options === "object") c.el.scrollTop = options.top ?? 0;
      c.el.dispatchEvent(new Event("scroll", { bubbles: true }));
    };
    await act(async () => button()!.click());
    expect(c.top()).toBe(end());
    expect(button()).toBeNull();
    await c.scrollTo(end() - 400, "wheel");
    await c.theyWrite("another reply");
    expect(button()?.textContent).toContain(message("session.new_messages", {"count":1}));
    await c.scrollTo(end());
    expect(button()).toBeNull();
  });

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

describe("sending from far up", () => {
  let root: Root | undefined;
  let host: HTMLElement;
  afterEach(() => {
    act(() => root?.unmount());
    host?.remove();
    localStorage.clear();
  });

  it("returns to the end", async () => {
    const r = DemoRemote.create(new URLSearchParams(), async () => undefined);
    const sid = "s-idle";
    const list = (r as unknown as { demoEvents(id: string): ReturnType<typeof create<typeof EventSchema>>[] }).demoEvents(sid);
    for (let i = 0; i < 40; i++) list.push(create(EventSchema, { seq: BigInt(list.length + 1), kind: { case: "agentMessage", value: { text: `answer ${i}` } } as never }));
    const demoSess = (r as unknown as { demoSessions: Map<string, { lastSeq: bigint }> }).demoSessions.get(sid);
    if (demoSess) demoSess.lastSeq = BigInt(list.length);
    r.start();
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    await act(async () => root!.render(<SessionPage remote={r} sessionId={sid} onBack={() => undefined} wide={false} />));
    const el = host.querySelector<HTMLElement>(".body.chat")!;
    // The mock has no row layout to measure; expose the positioned list.
    el.classList.remove("mo-hold");
    let top = 0;
    Object.defineProperties(el, {
      clientHeight: { configurable: true, get: () => 400 },
      scrollHeight: { configurable: true, get: () => 2600 },
      scrollTop: { configurable: true, get: () => top, set: (v: number) => { top = Math.min(v, 2200); } },
    });
    await act(async () => { top = 2200; el.dispatchEvent(new Event("scroll", { bubbles: true })); });
    await act(async () => { top = 900; el.dispatchEvent(new Event("scroll", { bubbles: true })); });
    expect(r.scroll.get(sid)?.stick).toBe(false);
    const input = host.querySelector("textarea")!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, "one more thing");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => { host.querySelector<HTMLButtonElement>(`button[aria-label="${message("composer.send")}"]`)!.click(); });
    expect(top).toBe(2200);
  });
});

describe("long-session cold load, memo reactivity, and key-based unseen badge", () => {
  let root: Root | undefined;
  let host: HTMLElement | undefined;
  const remotes: DemoRemote[] = [];
  afterEach(() => {
    act(() => root?.unmount());
    host?.remove();
    root = undefined;
    host = undefined;
    for (const r of remotes.splice(0)) r.dispose();
    vi.useRealTimers();
  });

  function makeVm(
    sid: string,
    items: SessionTimelineItem[],
    tl: Partial<{
      loaded: boolean;
      awaitingCache: boolean;
      backfilling: boolean;
      fetching: boolean;
      hasGap: boolean;
      hasEarlier: boolean;
      tailRequested: boolean;
      offline: boolean;
      anchorItemId: string;
      windowLastSeq: bigint;
    }> = {},
    computerOnline = true,
  ) {
    return create(SessionViewModelSchema, {
      sessionId: sid,
      header: create(SessionHeaderViewSchema, {
        found: true,
        title: { literal: "Bench" },
        computerName: { literal: "Mac" },
        computerOnline,
      }),
      timeline: create(SessionTimelineViewSchema, {
        items,
        loaded: tl.loaded ?? false,
        awaitingCache: tl.awaitingCache ?? false,
        backfilling: tl.backfilling ?? false,
        fetching: tl.fetching ?? false,
        hasGap: tl.hasGap ?? false,
        hasEarlier: tl.hasEarlier ?? false,
        tailRequested: tl.tailRequested ?? false,
        offline: tl.offline ?? false,
        anchorItemId: tl.anchorItemId ?? "",
        windowLastSeq: tl.windowLastSeq ?? 0n,
      }),
    });
  }

  async function renderView(r: DemoRemote, vm: ReturnType<typeof makeVm>, onScheduleRevealed = () => undefined) {
    if (!root) {
      host = document.createElement("div");
      document.body.append(host);
      root = createRoot(host);
    }
    await act(async () =>
      root!.render(
        <VirtuosoMockContext.Provider value={{ viewportHeight: 400, itemHeight: 50 }}>
          <SessionScreenView
            key={vm.sessionId}
            vm={vm}
            wide={false}
            remote={r}
            onBack={() => undefined}
            draft=""
            onDraft={() => undefined}
            onSend={() => undefined}
            onSubmitCommand={() => undefined}
            onAbandonCommand={() => undefined}
            onDismissOutgoing={() => undefined}
            onRestoreOutgoing={() => undefined}
            onLoadEarlier={() => undefined}
            onExpandGroup={() => undefined}
            onScheduleRevealed={onScheduleRevealed}
          />
        </VirtuosoMockContext.Provider>,
      ),
    );
  }

  const msgItem = (seq: bigint, text: string) =>
    create(SessionTimelineItemSchema, {
      id: `e${seq}`,
      seq,
      endSeq: seq,
      kind: { case: "agentMessage", value: { markdown: text } },
    });

  it("uses virtual list immediately during backfilling without placeholder blanking, and protects needFullCache", async () => {
    const r = DemoRemote.create(new URLSearchParams(), async () => undefined);
    remotes.push(r);
    r.start();

    const tail = [msgItem(99n, "tail-1"), msgItem(100n, "tail-2")];
    await renderView(r, makeVm("s-cold", tail, { backfilling: true, loaded: false }));
    // Virtual list mounts immediately while backfilling is true (not plain list).
    expect(host!.querySelector(".virtual-history")).toBeTruthy();
    expect(host!.querySelector("[data-history-index]")).toBeNull();

    // Returning to a scrolled-up session waits during backfilling and empty cache, shows offline cache, and stays ready on reconnect.
    const r2 = DemoRemote.create(new URLSearchParams(), async () => undefined);
    remotes.push(r2);
    r2.start();
    r2.scroll.set("s-up", { top: 400, stick: false });
    patchSyncState(r2, { conn: "offline", listed: true });

    await renderView(r2, makeVm("s-up", [], { awaitingCache: false, fetching: true }));
    expect(host!.querySelector(".body.chat")!.classList.contains("mo-hold")).toBe(true);

    await renderView(r2, makeVm("s-up", tail, { backfilling: true, fetching: false }));
    expect(host!.querySelector(".body.chat")!.classList.contains("mo-hold")).toBe(true);

    // Offline / no-network (timeline.offline=true) with backfilling finished and cached rows -> reveals instead of blanking.
    await renderView(r2, makeVm("s-up", tail, { backfilling: false, fetching: false, hasGap: true, offline: true }));
    expect(host!.querySelector(".virtual-history")).toBeTruthy();

    // Reconnecting (fetching=true, offline=false) must never flip ready back to false / HistoryPlaceholder.
    await act(async () => { patchSyncState(r2, { conn: "online", listed: true }); });
    await renderView(r2, makeVm("s-up", tail, { backfilling: false, fetching: true, hasGap: true, offline: false }));
    expect(host!.querySelector(".virtual-history")).toBeTruthy();

    // Short cached session with hasGap=true on reconnect stays plain list (does not tear down to Virtuoso).
    await renderView(r2, makeVm("s-up-short", tail, { loaded: true, hasGap: false }));
    expect(host!.querySelector("[data-history-index]")).toBeTruthy();
    await renderView(r2, makeVm("s-up-short", tail, { loaded: false, hasGap: true, fetching: true }));
    expect(host!.querySelector("[data-history-index]")).toBeTruthy();
    expect(host!.querySelector(".virtual-history")).toBeNull();

    // Uncached >500-event session with <=80 tail items during network prefix fetch (tailRequested=true, backfilling=false)
    // mounts Virtuoso immediately and shows "Loading earlier..." without tearing down plain DOM when prefix completes.
    await renderView(r2, makeVm("s-net-tail", [], { awaitingCache: false, fetching: true }));
    const netChat = host!.querySelector<HTMLElement>(".body.chat")!;
    Object.defineProperties(netChat, {
      clientHeight: { configurable: true, get: () => 400 },
      offsetHeight: { configurable: true, get: () => 400 },
    });
    await renderView(r2, makeVm("s-net-tail", tail, { tailRequested: true, backfilling: false, fetching: true, hasGap: true }));
    expect(host!.querySelector(".virtual-history")).toBeTruthy();
    expect(host!.querySelector("[data-history-index]")).toBeNull();
    await vi.waitFor(async () => {
      await act(async () => { await new Promise((resolve) => setTimeout(resolve, 20)); });
      expect(host!.querySelector(".load-earlier .muted")?.textContent).toContain(message("session.loading_earlier"));
    });
    await renderView(r2, makeVm("s-net-tail", tail, { loaded: true, hasEarlier: true, tailRequested: false, fetching: false }));
    expect(host!.querySelector(".virtual-history")).toBeTruthy();
    expect(host!.querySelector("[data-history-index]")).toBeNull();

    // Scrolled-up cached session when Core reports offline=true (covering both CONN_STATE_OFFLINE and CONN_STATE_NO_NETWORK)
    // even while Remote.conn stays "connecting" must reveal cached history instead of sticking on .mo-hold.
    const rNoNet = DemoRemote.create(new URLSearchParams(), async () => undefined);
    remotes.push(rNoNet);
    rNoNet.start();
    rNoNet.scroll.set("s-nonet", { top: 200, stick: false });
    patchSyncState(rNoNet, { conn: "connecting", listed: true });
    await renderView(rNoNet, makeVm("s-nonet", tail, { backfilling: false, fetching: false, hasGap: true, offline: true }));
    expect(host!.querySelector("[data-history-index]")).toBeTruthy();
  });

  it("re-renders memoized ChatBody and TimelineRow when remote.conn or remote.lostAt changes in place", async () => {
    vi.useFakeTimers();
    const r = DemoRemote.create(new URLSearchParams(), async () => undefined);
    remotes.push(r);
    r.start();
    patchSyncState(r, { conn: "offline", listed: true });
    const base = 1_700_000_000_000;
    const setLostAt = (ms: number) => {
      (r as unknown as { syncStateView: { disconnectedAtMs: bigint } }).syncStateView.disconnectedAtMs = BigInt(ms);
    };
    setLostAt(base + 5_000);

    const runningTool = create(SessionTimelineItemSchema, {
      id: "e1",
      seq: 1n,
      endSeq: 1n,
      kind: {
        case: "toolCall",
        value: {
          title: "rg foo",
          callId: "c1",
          status: SessionToolStatus.RUNNING,
          sinceMs: BigInt(base),
        },
      },
    });
    const vm = makeVm("s-memo", [runningTool], { loaded: true }, false);
    await renderView(r, vm);
    expect(host!.textContent).toContain("0:05");

    // In-place mutation of remote.lostAt + remote.touch() updates memoized TimelineRow without new vm reference.
    setLostAt(base + 12_000);
    await act(async () => { (r as unknown as { touch(): void }).touch(); });
    expect(host!.textContent).toContain("0:12");

    // In-place mutation of remote.conn updates HistoryOpening inside memoized ChatBody without new vm reference.
    const emptyWait = makeVm("s-memo-wait", [], { awaitingCache: true, loaded: false });
    await renderView(r, emptyWait);
    await act(async () => { await vi.advanceTimersByTimeAsync(5100); });
    expect(host!.textContent).not.toContain(message("session.opening_slow"));
    await act(async () => { patchSyncState(r, { conn: "online", listed: true }); });
    expect(host!.textContent).toContain(message("session.opening_slow"));
  });

  it("tracks unseen badge accurately across ToolGroup merges, scheduleFocus jumps, and batch arrivals", async () => {
    const r = DemoRemote.create(new URLSearchParams(), async () => undefined);
    remotes.push(r);
    r.start();
    // Saved non-stick scroll so ChatBody initializes with stick=false while rows are present.
    r.scroll.set("s-badge", { top: 0, stick: false });

    const c1Running = create(SessionTimelineItemSchema, {
      id: "e1",
      seq: 1n,
      endSeq: 1n,
      kind: { case: "toolCall", value: { title: "ls", callId: "c1", status: SessionToolStatus.RUNNING } },
    });
    await renderView(r, makeVm("s-badge", [c1Running], { loaded: true, windowLastSeq: 1n }));
    expect(host!.querySelector("button.newmsg")).toBeNull();

    // 1) Running tail tool c1 completes (id shifts e1 -> e2) and c2 folds into ToolGroup -> still 0 unseen!
    const grp = create(SessionTimelineItemSchema, {
      id: "e2",
      seq: 2n,
      endSeq: 4n,
      kind: {
        case: "toolGroup",
        value: {
          calls: [
            { title: "ls", callId: "c1", status: SessionToolStatus.OK },
            { title: "pwd", callId: "c2", status: SessionToolStatus.OK },
          ],
        },
      },
    });
    await renderView(r, makeVm("s-badge", [grp], { loaded: true, windowLastSeq: 4n }));
    expect(host!.querySelector("button.newmsg")).toBeNull();

    // 2) Batch arrival of a new 3-tool ToolGroup + 1 AgentMessage counts 2 rows (1 group + 1 message), not 4.
    const grp2 = create(SessionTimelineItemSchema, {
      id: "e5",
      seq: 5n,
      endSeq: 10n,
      kind: {
        case: "toolGroup",
        value: {
          calls: [
            { title: "a", callId: "c3", status: SessionToolStatus.OK },
            { title: "b", callId: "c4", status: SessionToolStatus.OK },
            { title: "c", callId: "c5", status: SessionToolStatus.OK },
          ],
        },
      },
    });
    const m1 = msgItem(11n, "done");
    await renderView(r, makeVm("s-badge", [grp, grp2, m1], { loaded: true, windowLastSeq: 11n }));
    expect(host!.querySelector("button.newmsg")?.textContent).toContain(message("session.new_messages", { count: 2 }));

    // 3) scheduleFocus jump on mount must not seed seenWaterSeq at the mid-history anchor (seq 10) and inflate badge on return to tail.
    const r3 = DemoRemote.create(new URLSearchParams(), async () => undefined);
    remotes.push(r3);
    r3.start();
    r3.scroll.set("s-sched", { top: 0, stick: false });
    const anchored = Array.from({ length: 10 }, (_, i) => msgItem(BigInt(i + 1), `old ${i + 1}`));
    let cleared = false;
    await renderView(
      r3,
      makeVm("s-sched", anchored, { loaded: true, anchorItemId: "e5", windowLastSeq: 10n }),
      () => { cleared = true; },
    );
    expect(cleared).toBe(true);
    const tail = Array.from({ length: 10 }, (_, i) => msgItem(BigInt(41 + i), `tail ${41 + i}`));
    await renderView(r3, makeVm("s-sched", tail, { loaded: true, windowLastSeq: 50n }));
    expect(host!.querySelector("button.newmsg")).toBeNull();
    await renderView(r3, makeVm("s-sched", [...tail, msgItem(51n, "new 51")], { loaded: true, windowLastSeq: 51n }));
    expect(host!.querySelector("button.newmsg")?.textContent).toContain(message("session.new_messages", { count: 1 }));
  });
});
