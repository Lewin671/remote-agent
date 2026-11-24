// @vitest-environment happy-dom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import {
  CoreHost,
  TerminalHistoryEventKind,
  TerminalHistoryPhase,
  TerminalRunWidth,
  TerminalSgrColorKind,
  type TerminalHistorySendRequestEffect,
} from "../core/host";
import {
  TerminalHistoryLineSchema,
  TerminalHistoryParsedPageSchema,
  TerminalHistoryRunSchema,
} from "../gen/remoteagent/core/v1/terminal_history_pb";
import { TerminalHistoryPageSchema } from "../gen/remoteagent/v1/remote_pb";
import {
  applyTerminalHistoryUpdate,
  EMPTY_TERMINAL_HISTORY,
  TerminalReader,
  type TerminalHistoryViewState,
} from "./TerminalHistory";

(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined, host: HTMLDivElement;
afterEach(async () => {
  if (root) await act(async () => root!.unmount());
  root = undefined;
  host?.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

function makePage(
  id: string,
  lines: { runs: { text: string; fg?: number; width?: TerminalRunWidth }[] }[],
  maxCells: number,
) {
  const protoLines = lines.map((l) =>
    create(TerminalHistoryLineSchema, {
      runs: l.runs.map((r) =>
        create(TerminalHistoryRunSchema, {
          text: r.text,
          fgKind: r.fg !== undefined ? TerminalSgrColorKind.PALETTE : TerminalSgrColorKind.DEFAULT,
          fgValue: r.fg ?? 0,
          width: r.width ?? TerminalRunWidth.NARROW,
        }),
      ),
    }),
  );
  return create(TerminalHistoryParsedPageSchema, { id, lines: protoLines, maxCells });
}

it("bounds mounted rows, renders Core-parsed pages, and anchors prepends without losing horizontal width", async () => {
  vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
  vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(100);
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockReturnValue(20);
  vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockImplementation(function (this: HTMLElement) {
    return 32 + Number.parseFloat(this.querySelector<HTMLElement>(".thlines")?.style.height || "0");
  });

  const firstPage = makePage(
    "page",
    Array.from({ length: 240 }, (_, i) => ({
      runs: [
        { text: `row ${i} `, fg: 1 },
        { text: "汉字", fg: 1, width: TerminalRunWidth.WIDE },
        ...(i === 0 ? [{ text: "x".repeat(100), fg: 1 }] : []),
      ],
    })),
    110,
  );
  let history: TerminalHistoryViewState = {
    phase: TerminalHistoryPhase.OPEN,
    canRetry: false,
    canLoadEarlier: true,
    pending: false,
    maxCells: 110,
    pages: [firstPage],
  };
  const load = vi.fn(), leave = vi.fn();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const render = () =>
    root!.render(<TerminalReader history={history} load={load} online font={14} height={20} offset={40} left={120} grid={800} onLeave={leave} />);
  await act(async () => render());
  const box = host.querySelector<HTMLElement>(".terminal-history")!;
  expect(box.scrollLeft).toBe(120);
  expect(host.querySelectorAll(".thline").length).toBeLessThan(30);
  expect(host.textContent).toContain("row 239");
  expect(host.querySelector(".wide")?.textContent).toBe("汉");
  const width = host.querySelector<HTMLElement>(".thlines")!.style.width;
  expect(width).toContain("110ch");

  await act(async () => { box.scrollTop = 500; box.dispatchEvent(new Event("scroll")); });
  const anchor = host.querySelector(".thline")!.textContent;
  const olderPage = makePage(
    "earlier",
    [
      { runs: [{ text: "older 1" }] },
      { runs: [{ text: "older 2" }] },
      { runs: [{ text: "older 3" }] },
    ],
    7,
  );
  history = { ...history, pages: [olderPage, ...history.pages] };
  await act(async () => render());
  expect(box.scrollTop).toBe(560);
  expect(host.querySelector(".thline")!.textContent).toBe(anchor);
  expect(box.scrollLeft).toBe(120);
  expect(host.querySelector<HTMLElement>(".thlines")!.style.width).toBe(width);

  await act(async () => { box.scrollTop = 0; box.dispatchEvent(new Event("scroll")); });
  expect(load).toHaveBeenCalled();
  await act(async () => {
    box.scrollTop = box.scrollHeight - box.clientHeight;
    box.dispatchEvent(new Event("scroll"));
    box.dispatchEvent(new WheelEvent("wheel", { deltaY: 10, bubbles: true }));
  });
  expect(leave).toHaveBeenCalledTimes(1);
});

it("drives terminal history paging, SGR parsing, width runs, and 10s timeout through clientcore WASM", () => {
  vi.useFakeTimers();
  const coreHost = new CoreHost();
  const tid = "term-test-1";
  const sent: TerminalHistorySendRequestEffect[] = [];
  let state = EMPTY_TERMINAL_HISTORY;
  const offReq = coreHost.onTerminalHistorySendRequest(tid, (req) => sent.push(req));
  const offUpd = coreHost.onTerminalHistoryUpdated(tid, (upd) => {
    state = applyTerminalHistoryUpdate(state, upd);
  });
  try {
    coreHost.sendTerminalHistory(tid, TerminalHistoryEventKind.OPEN);
    expect(sent).toHaveLength(1);
    expect(sent[0].cursor).toBe("");
    expect(state.phase).toBe(TerminalHistoryPhase.OPENING);

    coreHost.sendTerminalHistory(
      tid,
      TerminalHistoryEventKind.PAGE_RECEIVED,
      create(TerminalHistoryPageSchema, {
        requestId: sent[0].requestId,
        lines: ["\u001b[38;2;255;123;114mdiff\u001b[0m 中文✅👨\u200d👩\u200d👧"],
        nextCursor: "cap:200",
      }),
    );
    expect(state.phase).toBe(TerminalHistoryPhase.OPEN);
    expect(state.canLoadEarlier).toBe(true);
    expect(state.maxCells).toBe(13);
    expect(state.pages).toHaveLength(1);
    const runs = state.pages[0].lines[0].runs;
    expect(runs).toHaveLength(4);
    expect(runs[0].fgKind).toBe(TerminalSgrColorKind.RGB);
    expect(runs[0].fgValue).toBe(0xff7b72);
    expect(runs[0].width).toBe(TerminalRunWidth.NARROW);
    expect(runs[2].text).toBe("中文✅");
    expect(runs[2].width).toBe(TerminalRunWidth.WIDE);
    expect(runs[3].text).toBe("👨\u200d👩\u200d👧");
    expect(runs[3].width).toBe(TerminalRunWidth.CLUSTER_WIDE);

    coreHost.sendTerminalHistory(tid, TerminalHistoryEventKind.LOAD_EARLIER);
    expect(sent).toHaveLength(2);
    expect(sent[1].cursor).toBe("cap:200");
    vi.advanceTimersByTime(10_001);
    expect(state.canRetry).toBe(true);
    expect(state.pending).toBe(false);
    expect(state.statusText?.key).toBe("terminal.read_failed");

    coreHost.sendTerminalHistory(tid, TerminalHistoryEventKind.RETRY);
    expect(sent).toHaveLength(3);
    expect(sent[2].cursor).toBe("cap:200");
  } finally {
    offReq();
    offUpd();
    coreHost.sendTerminalHistory(tid, TerminalHistoryEventKind.DISPOSED);
    coreHost.dispose();
  }
});

describe("Core-driven wide and cluster spans", () => {
  it("renders wide runes and indivisible multi-codepoint emoji clusters as .wide spans", async () => {
    vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
    vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(100);
    vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockReturnValue(20);
    const coreHost = new CoreHost();
    const tid = "term-wide-render";
    const sent: TerminalHistorySendRequestEffect[] = [];
    let state = EMPTY_TERMINAL_HISTORY;
    const offReq = coreHost.onTerminalHistorySendRequest(tid, (req) => sent.push(req));
    const offUpd = coreHost.onTerminalHistoryUpdated(tid, (upd) => {
      state = applyTerminalHistoryUpdate(state, upd);
    });
    try {
      coreHost.sendTerminalHistory(tid, TerminalHistoryEventKind.OPEN);
      coreHost.sendTerminalHistory(
        tid,
        TerminalHistoryEventKind.PAGE_RECEIVED,
        create(TerminalHistoryPageSchema, {
          requestId: sent[0].requestId,
          lines: ["a中文✅☀\ufe0f👨\u200d👩\u200d👧e\u0301b"],
        }),
      );
      expect(state.maxCells).toBe(13);
      host = document.createElement("div");
      document.body.append(host);
      root = createRoot(host);
      await act(async () =>
        root!.render(<TerminalReader history={state} load={() => {}} online font={14} height={20} offset={0} left={0} grid={400} onLeave={() => {}} />),
      );
      const wideTexts = [...host.querySelectorAll(".wide")].map((el) => el.textContent);
      expect(wideTexts).toEqual(["中", "文", "✅", "☀\ufe0f", "👨\u200d👩\u200d👧"]);
      expect(host.querySelector(".thline")?.textContent).toBe("a中文✅☀\ufe0f👨\u200d👩\u200d👧e\u0301b");
    } finally {
      offReq();
      offUpd();
      coreHost.sendTerminalHistory(tid, TerminalHistoryEventKind.DISPOSED);
      coreHost.dispose();
    }
  });
});
