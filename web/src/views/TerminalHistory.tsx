import { memo, useLayoutEffect, useMemo, useRef, useState, type CSSProperties } from "react";
import type { HistoryPage, TerminalHistory } from "../terminal-history";
import { isWide, parseSgrLine, widthSegments, type Run } from "../sgr";
import { DEFAULT_BG, DEFAULT_FG, ansiColor } from "../ansi";
import { ArrowDown } from "../icons";
import { wheelAxis } from "../wheel";

/** The monospace stack the live xterm is opened with; the reader uses it
 * too, so the two show the same picture. */
export const TERM_FONT = 'ui-monospace, Menlo, "SF Mono", monospace';

function css(v: number | string | undefined, bold = false): string | undefined {
  if (v === undefined) return undefined;
  return typeof v === "string" ? v : ansiColor(v, bold);
}

/** One run of a history line, in the live terminal's colours and attributes. */
function RunSpan({ run }: { run: Run }) {
  const style: CSSProperties = {};
  let fg = css(run.fg, run.bold);
  let bg = css(run.bg);
  if (run.inverse) [fg, bg] = [bg ?? DEFAULT_BG, fg ?? DEFAULT_FG];
  if (fg) style.color = fg;
  if (bg) style.background = bg;
  if (run.bold) style.fontWeight = 700;
  if (run.italic) style.fontStyle = "italic";
  if (run.underline) style.textDecoration = "underline";
  if (run.dim) style.opacity = 0.55;
  return (
    <span style={style}>
      {widthSegments(run.text).map((s, i) => s.wide
        // Each wide character sits centred in its two cells, as xterm draws it.
        ? [...s.text].map((c, j) => <span key={i + "." + j} className="wide">{c}</span>)
        : <span key={i}>{s.text}</span>)}
    </span>
  );
}

interface ReaderLine { key: string; runs: Run[]; }
const formattedPages = new WeakMap<HistoryPage, { lines: ReaderLine[]; columns: number }>();
function formatPage(page: HistoryPage) {
  const cached = formattedPages.get(page);
  if (cached) return cached;
  let columns = 0;
  const lines = page.lines.map((text, i) => {
    const runs = parseSgrLine(text);
    let cells = 0;
    for (const run of runs) for (const ch of run.text)
      cells += ch === "\t" ? 8 - cells % 8 : isWide(ch.codePointAt(0)!) ? 2 : 1;
    columns = Math.max(columns, cells);
    return { key: page.id + ":" + i, runs };
  });
  const value = { lines, columns };
  formattedPages.set(page, value);
  return value;
}

/** Immutable pages keep parsed runs even when a line leaves the viewport. */
const Line = memo(function Line({ runs, height }: { runs: Run[]; height: number }) {
  return (
    <div className="thline" style={{ height, lineHeight: `${height}px` }}>
      {runs.map((run, i) => <RunSpan key={i} run={run} />)}
    </div>
  );
});

function status(history: TerminalHistory, online: boolean, load: () => void) {
  if (history.pending) return <>读取更早的记录…</>;
  if (history.closed) return <>终端断开了，已读到的内容还能看；重新连接后再往上滑读取更早的</>;
  if (history.error === "expired") return <>记录已过期，回到实时后重新往上滑</>;
  if (history.error === "failed") return <>读取失败<button disabled={!online} onClick={load}>重试</button></>;
  if (!history.cursor && history.pages.length) return <>已经是保留的最早记录</>;
  return null;
}

/** The read-only reader over the live terminal: a fresh capture, scrolled
 * natively, coloured like the live screen and never focused (so a tap does
 * not open the keyboard). */
export function TerminalReader({ history, load, online, font, height, offset, left, grid, onLeave }: {
  history: TerminalHistory;
  load: () => void;
  online: boolean;
  font: number;
  height: number;
  offset: number;
  /** The live screen's sideways position, where a wide reader opens. */
  left: number;
  /** The live screen's width: the reader is at least this wide, so its sideways position holds even over short lines. */
  grid: number;
  onLeave: () => void;
}) {
  const box = useRef<HTMLDivElement>(null);
  const stat = useRef<HTMLDivElement>(null);
  const [viewport, setViewport] = useState({ top: 0, height: 0, status: height });
  const formatted = useMemo(() => {
    const pages = history.pages.map(formatPage);
    return { lines: pages.flatMap(p => p.lines), columns: Math.max(0, ...pages.map(p => p.columns)) };
  }, [history.pages]);
  const first = Math.max(0, Math.floor((viewport.top - viewport.status - 6) / height) - 8);
  const end = Math.min(formatted.lines.length, first + Math.ceil(viewport.height / height) + 18);
  const measure = () => {
    const el = box.current;
    if (!el) return;
    const next = { top: el.scrollTop, height: el.clientHeight, status: stat.current?.offsetHeight ?? height };
    setViewport(old => old.top === next.top && old.height === next.height && old.status === next.status ? old : next);
  };
  useLayoutEffect(() => {
    const observer = new ResizeObserver(measure);
    observer.observe(box.current!);
    observer.observe(stat.current!);
    return () => observer.disconnect();
  }, []);
  const seen = useRef({ pages: 0, height: 0 });
  const wheel = useRef(wheelAxis()).current;
  const touch = useRef<{ x: number; y: number; axis: "" | "x" | "y" }>({ x: 0, y: 0, axis: "" });
  const readEarlier = () => {
    const el = box.current!;
    if (el.scrollTop < 80 && online && !history.pending && !history.error && !history.closed && history.cursor) load();
  };
  useLayoutEffect(() => {
    const el = box.current!;
    if (seen.current.pages === 0 && history.pages.length) {
      // Opens at the newest line, then up by the gesture, so it looks like
      // the live screen kept moving and there is room to scroll down.
      el.scrollTop = Math.max(0, el.scrollHeight - el.clientHeight - offset);
      el.scrollLeft = left;
    } else if (seen.current.height && el.scrollHeight !== seen.current.height) {
      // Prepended pages and a taller status line keep the visible text still.
      el.scrollTop += el.scrollHeight - seen.current.height;
    }
    seen.current = { pages: history.pages.length, height: el.scrollHeight };
    measure();
    readEarlier();
  });
  const atBottom = () => {
    const el = box.current!;
    return el.scrollHeight - el.clientHeight - el.scrollTop <= 1;
  };
  return (
    <div
      className="terminal-history"
      ref={box}
      role="region"
      aria-label="终端记录"
      style={{ fontFamily: TERM_FONT, fontSize: font, lineHeight: `${height}px`, color: DEFAULT_FG }}
      onScroll={() => { measure(); readEarlier(); }}
      onWheel={(e) => wheel(e) === "y" && e.deltaY > 0 && atBottom() && onLeave()}
      onTouchStart={(e) => (touch.current = { x: e.touches[0].clientX, y: e.touches[0].clientY, axis: "" })}
      onTouchMove={(e) => {
        // The first clear direction holds for the whole gesture: a sideways
        // one only pans, and only an upward one at the bottom leaves.
        const t = touch.current;
        const dx = e.touches[0].clientX - t.x, dy = e.touches[0].clientY - t.y;
        if (!t.axis && Math.max(Math.abs(dx), Math.abs(dy)) > 12) t.axis = Math.abs(dx) > Math.abs(dy) ? "x" : "y";
        if (t.axis === "y" && dy < -12 && atBottom()) onLeave();
      }}
    >
      <div style={{ minWidth: grid }}>
        <div ref={stat} className="thstat" style={{ minHeight: height }}>{status(history, online, load)}</div>
        <div className="thlines" style={{ position: "relative", height: formatted.lines.length * height, width: `${formatted.columns}ch`, minWidth: "100%" }}>
          <div style={{ position: "absolute", top: first * height, width: "100%" }}>
            {formatted.lines.slice(first, end).map(line => <Line key={line.key} runs={line.runs} height={height} />)}
          </div>
        </div>
      </div>
    </div>
  );
}

/** The floating pill that leaves the reader. */
export function BackToLive({ fresh, onLeave }: { fresh: boolean; onLeave: () => void }) {
  return (
    <button className="tpill" onClick={onLeave} aria-label={fresh ? "有新输出，回到实时" : "回到实时"}>
      {fresh ? <><i />有新输出<ArrowDown /></> : <><ArrowDown />回到实时</>}
    </button>
  );
}
