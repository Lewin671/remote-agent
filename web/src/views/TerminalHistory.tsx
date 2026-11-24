import * as i18n from "../i18n";
import { memo, useLayoutEffect, useMemo, useRef, useState, type CSSProperties } from "react";
import {
  TerminalHistoryPhase,
  TerminalSgrColorKind,
  type TerminalHistoryParsedPage,
  type TerminalHistoryRun,
  type TerminalHistoryUpdatedEffect,
} from "../core/host";
import { renderText } from "../core/text";
import type { Text as CoreText } from "../gen/remoteagent/core/v1/text_pb";
import { DEFAULT_BG, DEFAULT_FG, ansiColor } from "../ansi";
import { ArrowDown } from "../icons";
import { wheelAxis } from "../wheel";

/** The monospace stack the live xterm is opened with; the reader uses it
 * too, so the two show the same picture. */
export const TERM_FONT = 'ui-monospace, Menlo, "SF Mono", monospace';

export interface TerminalHistoryViewState {
  phase: TerminalHistoryPhase;
  statusText?: CoreText;
  canRetry: boolean;
  canLoadEarlier: boolean;
  pending: boolean;
  maxCells: number;
  /** Immutable pre-parsed pages from Core, oldest first. */
  pages: TerminalHistoryParsedPage[];
}

export const EMPTY_TERMINAL_HISTORY: TerminalHistoryViewState = {
  phase: TerminalHistoryPhase.IDLE,
  canRetry: false,
  canLoadEarlier: false,
  pending: false,
  maxCells: 0,
  pages: [],
};

export function applyTerminalHistoryUpdate(
  prev: TerminalHistoryViewState,
  upd: TerminalHistoryUpdatedEffect,
): TerminalHistoryViewState {
  let pages = upd.resetPages ? [] : prev.pages;
  if (upd.addedPage) pages = [upd.addedPage, ...pages];
  return {
    phase: upd.phase,
    statusText: upd.statusText,
    canRetry: upd.canRetry,
    canLoadEarlier: upd.canLoadEarlier,
    pending: upd.pending,
    maxCells: upd.maxCells,
    pages,
  };
}

/** East Asian wide and fullwidth code points, which take two cells in CSS. */
function isWide(code: number): boolean {
  return (
    (code >= 0x1100 && code <= 0x115f) ||
    (code >= 0x2e80 && code <= 0x303e) ||
    (code >= 0x3041 && code <= 0x33ff) ||
    (code >= 0x3400 && code <= 0x4dbf) ||
    (code >= 0x4e00 && code <= 0x9fff) ||
    (code >= 0xa000 && code <= 0xa4cf) ||
    (code >= 0xac00 && code <= 0xd7a3) ||
    (code >= 0xf900 && code <= 0xfaff) ||
    (code >= 0xfe10 && code <= 0xfe19) ||
    (code >= 0xfe30 && code <= 0xfe6f) ||
    (code >= 0xff00 && code <= 0xff60) ||
    (code >= 0xffe0 && code <= 0xffe6) ||
    (code >= 0x1f300 && code <= 0x1faff) ||
    (code >= 0x20000 && code <= 0x3fffd)
  );
}

/** Splits a run's text into stretches of narrow (one cell) and wide (two
 * cells) characters, so a wide stretch can be given two `ch` per character
 * and line up with the live terminal's columns. */
export function widthSegments(text: string): { text: string; wide: boolean }[] {
  const out: { text: string; wide: boolean }[] = [];
  let wide: boolean | undefined;
  let from = 0;
  let at = 0;
  for (const ch of text) {
    const w = isWide(ch.codePointAt(0)!);
    if (w !== wide) {
      if (at > from) out.push({ text: text.slice(from, at), wide: wide! });
      wide = w;
      from = at;
    }
    at += ch.length;
  }
  if (at > from) out.push({ text: text.slice(from, at), wide: wide! });
  return out;
}

function cssColor(kind: TerminalSgrColorKind, value: number, bold = false): string | undefined {
  if (kind === TerminalSgrColorKind.PALETTE) return ansiColor(value, bold);
  if (kind === TerminalSgrColorKind.RGB) return "#" + (value & 0xffffff).toString(16).padStart(6, "0");
  return undefined;
}

/** One run of a history line, in the live terminal's colours and attributes. */
function RunSpan({ run }: { run: TerminalHistoryRun }) {
  const style: CSSProperties = {};
  let fg = cssColor(run.fgKind, run.fgValue, run.bold);
  let bg = cssColor(run.bgKind, run.bgValue);
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

/** Immutable pages keep Core's pre-parsed runs even when a line leaves the viewport. */
const Line = memo(function Line({ runs, height }: { runs: readonly TerminalHistoryRun[]; height: number }) {
  return (
    <div className="thline" style={{ height, lineHeight: `${height}px` }}>
      {runs.map((run, i) => <RunSpan key={i} run={run} />)}
    </div>
  );
});

function status(history: TerminalHistoryViewState, online: boolean, onRetry: () => void) {
  const label = renderText(history.statusText);
  if (!label) return null;
  if (history.canRetry) {
    return <>{label}<button disabled={!online} onClick={onRetry}>{i18n.t("common.retry")}</button></>;
  }
  return <>{label}</>;
}

/** The read-only reader over the live terminal: a fresh capture, scrolled
 * natively, coloured like the live screen and never focused (so a tap does
 * not open the keyboard). */
export function TerminalReader({ history, load, retry, online, font, height, offset, left, grid, onLeave }: {
  history: TerminalHistoryViewState;
  load: () => void;
  retry?: () => void;
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
  const lines = useMemo(() => {
    const out: { key: string; runs: readonly TerminalHistoryRun[] }[] = [];
    for (const page of history.pages) {
      for (let i = 0; i < page.lines.length; i++) {
        out.push({ key: page.id + ":" + i, runs: page.lines[i].runs });
      }
    }
    return out;
  }, [history.pages]);
  const first = Math.max(0, Math.floor((viewport.top - viewport.status - 6) / height) - 8);
  const end = Math.min(lines.length, first + Math.ceil(viewport.height / height) + 18);
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
    if (el.scrollTop < 80 && online && history.canLoadEarlier) load();
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
      aria-label={i18n.t("terminal.history")}
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
        <div ref={stat} className="thstat" style={{ minHeight: height }}>{status(history, online, retry ?? load)}</div>
        <div className="thlines" style={{ position: "relative", height: lines.length * height, width: `${history.maxCells}ch`, minWidth: "100%" }}>
          <div style={{ position: "absolute", top: first * height, width: "100%" }}>
            {lines.slice(first, end).map(line => <Line key={line.key} runs={line.runs} height={height} />)}
          </div>
        </div>
      </div>
    </div>
  );
}

/** The floating pill that leaves the reader. */
export function BackToLive({ fresh, onLeave }: { fresh: boolean; onLeave: () => void }) {
  return (
    <button className="tpill" onClick={onLeave} aria-label={fresh ? i18n.t("terminal.fresh_live") : i18n.t("terminal.live")}>
      {fresh ? <><i />{i18n.t("terminal.fresh")}<ArrowDown /></> : <><ArrowDown />{i18n.t("terminal.live")}</>}
    </button>
  );
}
