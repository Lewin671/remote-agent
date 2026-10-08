// Markdown as the CLIs write it, rendered as React elements. Text is
// always text: nothing is inserted as HTML, and only http(s) links become
// links. Other targets, mostly local file paths, show as monospace refs.
//
// The Android client parses the same way (ui/Markdown.kt): both run the
// cases in testdata/markdown, and how each element looks is
// docs/design-system.md §7.

import { memo, useEffect, useMemo, useRef, useState } from "react";
import { Box, BoxChecked, Check, ChevronDown, ChevronUp, Copy, X } from "./icons";

/** Whether a link target may be opened: web pages only, so javascript:,
 * data: and file paths never become clickable. */
export function safeHref(url: string): string | undefined {
  return /^https?:\/\/[^\s]+$/i.test(url.trim()) ? url.trim() : undefined;
}

function escapedAt(text: string, index: number): boolean {
  let slashes = 0;
  while (index > 0 && text[--index] === "\\") slashes++;
  return slashes % 2 === 1;
}

/** Index link delimiters once. A failed '[' or autolink opener must not
 * rescan the remaining attacker-controlled CLI text on every character. */
function inlineLinks(text: string): Map<number, { close: number; end: number; auto?: boolean }> {
  const links = new Map<number, { close: number; end: number; auto?: boolean }>();
  let close = -1, end = -1, validTarget = false;
  let paren = -1, angle = -1, space = text.length, newline = text.length;
  for (let i = text.length - 1; i >= 0; i--) {
    const c = text[i];
    if (c === "[") {
      if (close > i + 1 && close < newline && validTarget) links.set(i, { close, end });
    } else if (c === "<" && angle < space) {
      const prefix = text.startsWith("https://", i + 1) ? 8 : text.startsWith("http://", i + 1) ? 7 : 0;
      if (prefix && angle > i + 1 + prefix) links.set(i, { close: angle, end: angle, auto: true });
    }
    if (c === "]") {
      close = i;
      end = paren;
      validTarget = text[i + 1] === "(" && paren > i + 2 && paren < space;
    }
    if (c === ")") paren = i;
    if (c === ">") angle = i;
    if (/\s/.test(c)) space = i;
    if (c === "\n") newline = i;
  }
  return links;
}

/** Inline Markdown as a tree, so that both clients can be held to the
 * same reading of it. */
export type Span =
  | { t: "text"; v: string }
  | { t: "code"; v: string }
  | { t: "b" | "i" | "s"; c: Span[] }
  | { t: "link"; v: string; href: string }
  | { t: "ref"; v: string; target: string };

/** A bare URL without the sentence's punctuation after it; a closing
 * bracket stays only while the URL has an opening one for it. Empty when
 * nothing but the scheme is left. */
function bareUrl(raw: string): string {
  // How many closing brackets have no opening one: those end the sentence.
  let paren = 0, bracket = 0;
  for (const c of raw) {
    if (c === "(") paren--;
    else if (c === ")") paren++;
    else if (c === "[") bracket--;
    else if (c === "]") bracket++;
  }
  let end = raw.length;
  for (; end > 0; end--) {
    const c = raw[end - 1];
    if (c === ")" && paren > 0) paren--;
    else if (c === "]" && bracket > 0) bracket--;
    else if (!".,;:!?'\"*_~".includes(c)) break;
  }
  const url = raw.slice(0, end);
  return /^https?:\/\/$/i.test(url) ? "" : url;
}

// A bare URL runs to the next blank, angle bracket or piece of the
// sentence's own punctuation: a full-width mark, a dash or a curly quote.
// Other scripts stay in it, as in https://example.com/文档/安装.
const inlineRe =
  /(`+)|\*\*([^*\n]+)\*\*|(?<![\w*])\*([^*\s][^*\n]*?)\*(?![\w*])|(?<![\p{L}\p{N}_])_([^_\s][^_\n]*?)_(?![\p{L}\p{N}_])|~~([^~\n]+)~~|(\[)|(<https?:\/\/)|(?<![\w/])(https?:\/\/[^\s<>‐-‧　-〿＀-￯]+)/gu;

/** Inline Markdown: `code`, **bold**, *italic*, _italic_, ~~struck~~,
 * [links](target), <https://autolinks> and bare https:// URLs. */
export function inlineSpans(text: string): Span[] {
  const out: Span[] = [];
  const push = (v: string) => {
    if (!v) return;
    const last = out[out.length - 1];
    if (last?.t === "text") last.v += v;
    else out.push({ t: "text", v });
  };
  const links = inlineLinks(text);
  const runs = [...text.matchAll(/`+/g)];
  const closers = new Map<number, number>();
  const next = new Map<number, number>();
  for (const run of runs.reverse()) {
    const end = next.get(run[0].length);
    if (end !== undefined) closers.set(run.index, end);
    next.set(run[0].length, run.index);
  }
  const re = new RegExp(inlineRe);
  let last = 0;
  let m: RegExpExecArray | null;
  while ((m = re.exec(text))) {
    if (m.index < last) continue;
    const escapedTick = m[1] !== undefined && escapedAt(text, m.index);
    if (m.index > last) push(text.slice(last, escapedTick ? m.index - 1 : m.index));
    if (m[1] !== undefined) {
      const end = escapedTick ? undefined : closers.get(m.index);
      if (end !== undefined) {
        out.push({ t: "code", v: text.slice(m.index + m[1].length, end) });
        last = end + m[1].length;
        re.lastIndex = last;
        continue;
      }
      push(m[1]);
    }
    // Emphasis may wrap code and links, so its content is inline Markdown too.
    else if (m[2] !== undefined) out.push({ t: "b", c: inlineSpans(m[2]) });
    else if (m[3] !== undefined) out.push({ t: "i", c: inlineSpans(m[3]) });
    else if (m[4] !== undefined) out.push({ t: "i", c: inlineSpans(m[4]) });
    else if (m[5] !== undefined) out.push({ t: "s", c: inlineSpans(m[5]) });
    else if (m[8] !== undefined) {
      const url = bareUrl(m[8]);
      if (url) {
        out.push({ t: "link", v: url, href: url });
        last = m.index + url.length;
        re.lastIndex = last;
        continue;
      }
      push(m[0]);
    } else {
      const link = links.get(m.index);
      if (link) {
        const label = text.slice(m.index + 1, link.close);
        const target = link.auto ? label : text.slice(link.close + 2, link.end);
        const href = safeHref(target);
        out.push(href ? { t: "link", v: label, href } : { t: "ref", v: label, target });
        last = link.end + 1;
        re.lastIndex = last;
        continue;
      }
      push(m[0]);
    }
    last = m.index + m[0].length;
  }
  if (last < text.length) push(text.slice(last));
  return out;
}

function Spans({ spans }: { spans: Span[] }) {
  return (
    <>
      {spans.map((s, i) => {
        switch (s.t) {
          case "text":
            return s.v;
          case "code":
            return <code key={i}>{s.v}</code>;
          case "b":
            return <b key={i}><Spans spans={s.c} /></b>;
          case "i":
            return <em key={i}><Spans spans={s.c} /></em>;
          case "s":
            return <s key={i}><Spans spans={s.c} /></s>;
          case "link":
            return (
              <a key={i} href={s.href} target="_blank" rel="noopener noreferrer nofollow">
                {s.v}
              </a>
            );
          case "ref":
            return (
              <span key={i} className="ref" title={s.target}>
                {s.v}
              </span>
            );
        }
      })}
    </>
  );
}

export function Rich({ text }: { text: string }) {
  return <Spans spans={inlineSpans(text)} />;
}

function tableCellClass(text: string): string {
  // Keep short quantities and their units together; long descriptions can wrap.
  return text.length > 0 && text.length <= 12 ? "mdcell nowrap" : "mdcell";
}

const blankCell = /^(?:|-|–|—|N\/A|n\/a)$/;
const digit = (c: string | undefined) => c !== undefined && c >= "0" && c <= "9";
const space = (c: string | undefined) => c !== undefined && /\s/.test(c);
const unitLetter = (c: string | undefined) => c !== undefined && ((c >= "a" && c <= "z") || (c >= "A" && c <= "Z") || c === "µ");

/** A whole cell that reads as a quantity: digits with a sign, a currency
 * mark or a short unit, or a few of them ("+84 −61", "5.2 s", "118 ms",
 * "1 / 3"). Scanned once from the left: a pattern for it backtracks. */
function quantity(v: string): boolean {
  const n = v.length;
  let i = 0;
  while (i < n && "+-−~≈<>≤≥$¥€£".includes(v[i])) i++;
  for (;;) {
    if (!digit(v[i])) return false;
    i++;
    while (i < n && (digit(v[i]) || ",.:".includes(v[i]))) i++;
    // A unit: one optional space, then a mark or up to five letters.
    const j = space(v[i]) ? i + 1 : i;
    if (j < n && "%‰°".includes(v[j])) i = j + 1;
    else {
      let k = j;
      while (unitLetter(v[k])) k++;
      if (k > j && k - j <= 5) i = k;
    }
    if (i === n) return true;
    if (!space(v[i])) return false;
    while (space(v[i])) i++;
    if (i < n && "+-−/".includes(v[i])) {
      i++;
      if (space(v[i])) i++;
    }
  }
}

/** What a cell says once its marks are gone: `**42 ms**` reads "42 ms". */
function shownText(spans: Span[]): string {
  let out = "";
  for (const s of spans) out += "c" in s ? shownText(s.c) : s.v;
  return out;
}

/** Whether a column holds quantities, and so reads better right-aligned:
 * every cell that says something is a quantity, and at least one does.
 * Cells are Markdown; what counts is the text they show. */
export function numericColumn(cells: string[]): boolean {
  let n = 0;
  for (const c of cells) {
    const v = shownText(inlineSpans(c)).trim();
    if (blankCell.test(v)) continue;
    if (!quantity(v)) return false;
    n++;
  }
  return n > 0;
}

/** Items longer than this many columns wrap on a phone: a wide character
 * (CJK and the like) takes two. */
const LOOSE_OVER = 40;

/** Whether a character takes two columns. An estimate, not Unicode's table
 * of widths: from the CJK blocks up, but for the half-width forms. One
 * column either way moves only where a list is called long. */
const wide = (c: number) => c >= 0x2e80 && !(c >= 0xff61 && c <= 0xffdc) && !(c >= 0xffe8 && c <= 0xffee);

/** Whether a list's items need more room between them: once an item runs
 * over a line, items set as close as lines read as one paragraph. Judged by
 * what the items say, not by how they happen to wrap on this screen, so
 * both clients space a list alike. */
export function looseList(items: string[]): boolean {
  for (const item of items) {
    if (item.includes("\n")) return true;
    const text = shownText(inlineSpans(item));
    let width = 0;
    for (let i = 0; i < text.length; i++) {
      const c = text.charCodeAt(i);
      // The second half of a pair adds nothing: its first half counted the character.
      if (c >= 0xdc00 && c <= 0xdfff) continue;
      width += wide(c) ? 2 : 1;
      if (width > LOOSE_OVER) return true;
    }
  }
  return false;
}

/** A paragraph that is one bold line, the way the CLIs often write a
 * small heading: it gets a heading's space, not a paragraph's. */
export function boldHeading(text: string): boolean {
  return /^\*\*[^*\n]+\*\*[:：]?$/.test(text.trim());
}

/** Code longer than this is cut to FOLD_SHOWN lines until it is opened. */
export const FOLD_OVER = 16;
export const FOLD_SHOWN = 12;

// Any indent: the CLIs nest code blocks in list items.
const fence = /^\s*(`{3,}|~{3,})\s*([^\s`]*)/;
const heading = /^\s{0,3}(#{1,6})\s+(.*)$/;
const rule = /^\s{0,3}([-*_])(\s*\1){2,}\s*$/;
const listLine = /^(\s*)([-*+]|\d{1,9}[.)])\s+(.*)$/;
const quote = /^\s{0,3}>\s?(.*)$/;
const taskMark = /^\[([ xX])\]\s+(.*)$/s;

const indentOf = (l: string) => l.length - l.trimStart().length;

/** A table's separator row: cells of dashes, with optional colons. Split
 * by hand: one pattern over the whole line backtracks on long blank runs. */
function tableSep(line: string): boolean {
  let s = line.trim();
  if (s.startsWith("|")) s = s.slice(1);
  if (s.endsWith("|")) s = s.slice(0, -1);
  return s.split("|").every((c) => /^:?-{3,}:?$/.test(c.trim()));
}

/** A heading's text, without the closing hashes some write after it. */
function headingText(rest: string): string {
  let end = rest.trimEnd().length;
  while (end > 0 && rest[end - 1] === "#") end--;
  return rest.slice(0, end).trimEnd();
}

/** Splits a table row into cells, at pipes outside `code` and not
 * escaped. */
export function tableCells(line: string): string[] {
  let s = line.trim();
  if (s.startsWith("|")) s = s.slice(1);
  // A pipe after an odd run of backslashes is escaped.
  const escaped = (end: number) => {
    let n = 0;
    while (end - 1 - n >= 0 && s[end - 1 - n] === "\\") n++;
    return n % 2 === 1;
  };
  if (s.endsWith("|") && !escaped(s.length - 1)) s = s.slice(0, -1);
  const cells: string[] = [];
  let cur = "";
  let code = 0;
  const ticks = new Map<number, number>();
  for (const m of s.matchAll(/`+/g)) ticks.set(m[0].length, (ticks.get(m[0].length) ?? 0) + 1);
  for (let i = 0; i < s.length; i++) {
    const c = s[i];
    if (c === "\\" && (s[i + 1] === "|" || s[i + 1] === "\\")) {
      cur += s[i + 1] === "|" ? "|" : "\\\\";
      i++;
    } else if (c === "`") {
      let end = i + 1;
      while (s[end] === "`") end++;
      const count = end - i;
      ticks.set(count, (ticks.get(count) ?? 1) - 1);
      if (!code && !escapedAt(s, i) && (ticks.get(count) ?? 0) > 0) code = count;
      else if (code === count) code = 0;
      cur += s.slice(i, end);
      i = end - 1;
    } else if (c === "|" && !code) {
      cells.push(cur.trim());
      cur = "";
    } else cur += c;
  }
  cells.push(cur.trim());
  return cells;
}

export interface ListItem {
  text: string;
  /** A task list item: checked or not. */
  task?: "done" | "todo";
  children: Block[];
}

export type Block =
  | { kind: "text"; text: string }
  | { kind: "code"; lang: string; text: string }
  | { kind: "heading"; level: number; text: string }
  | { kind: "rule" }
  | { kind: "quote"; blocks: Block[] }
  | { kind: "list"; ordered: boolean; start: number; items: ListItem[] }
  | { kind: "table"; head: string[]; align: ("left" | "center" | "right" | undefined)[]; rows: string[][] };

/** Splits Markdown into blocks: fenced code, tables, headings, rules,
 * quotes, lists and paragraphs. */
export function mdBlocks(md: string, depth = 0): Block[] {
  // CLI output can contain attacker-controlled file content. Deeply nested
  // quotes/lists must not overflow the parser or React render stack.
  if (depth >= 32) return [{ kind: "text", text: md }];
  const lines = md.replace(/\r\n?/g, "\n").split("\n");
  const nextNonBlank = new Array<number>(lines.length);
  let nextLine = lines.length;
  for (let i = lines.length - 1; i >= 0; i--) {
    nextNonBlank[i] = nextLine;
    if (lines[i].trim()) nextLine = i;
  }
  const blocks: Block[] = [];
  let text: string[] = [];
  const flush = () => {
    if (text.length) blocks.push({ kind: "text", text: text.join("\n") });
    text = [];
  };
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    if (!line.trim()) {
      flush();
      continue;
    }
    const f = fence.exec(line);
    if (f) {
      const body: string[] = [];
      // Closed by a fence of the same character, at least as long.
      const close = new RegExp(`^\\s*${f[1][0] === "`" ? "`" : "~"}{${f[1].length},}\\s*$`);
      let j = i + 1;
      // The fence's own indent is the list's, not the code's.
      const indent = indentOf(line);
      const dedent = (l: string) => l.slice(Math.min(indent, indentOf(l)));
      while (j < lines.length && !close.test(lines[j])) body.push(dedent(lines[j++]));
      flush();
      blocks.push({ kind: "code", lang: f[2].toLowerCase(), text: body.join("\n") });
      i = j;
      continue;
    }
    if (line.includes("|") && i + 1 < lines.length && tableSep(lines[i + 1]) && tableCells(line).length === tableCells(lines[i + 1]).length) {
      const head = tableCells(line);
      const align = tableCells(lines[i + 1]).map((c) =>
        c.startsWith(":") && c.endsWith(":") ? "center" : c.endsWith(":") ? "right" : c.startsWith(":") ? "left" : undefined,
      );
      const rows: string[][] = [];
      let j = i + 2;
      while (j < lines.length && lines[j].includes("|") && lines[j].trim()) rows.push(tableCells(lines[j++]));
      flush();
      blocks.push({ kind: "table", head, align, rows });
      i = j - 1;
      continue;
    }
    const h = heading.exec(line);
    if (h) {
      flush();
      blocks.push({ kind: "heading", level: h[1].length, text: headingText(h[2]) });
      continue;
    }
    if (rule.test(line)) {
      flush();
      blocks.push({ kind: "rule" });
      continue;
    }
    if (quote.test(line)) {
      flush();
      const body: string[] = [];
      let j = i;
      while (j < lines.length && quote.test(lines[j])) body.push(quote.exec(lines[j++])![1]);
      blocks.push({ kind: "quote", blocks: mdBlocks(body.join("\n"), depth + 1) });
      i = j - 1;
      continue;
    }
    if (listLine.test(line)) {
      flush();
      const base = indentOf(line);
      const ordered = (l: string) => /\d/.test(listLine.exec(l)?.[2] ?? "");
      const kind = ordered(line);
      const body: string[] = [];
      let j = i;
      // The list runs over its items, lines indented under them, and blank
      // lines between; it ends at a line back at the left edge that is not
      // an item.
      for (; j < lines.length; j++) {
        const l = lines[j];
        if (!l.trim()) {
          const next = lines[nextNonBlank[j]];
          if (next === undefined || (indentOf(next) <= base && !listLine.test(next))) break;
          body.push(l);
          continue;
        }
        if (indentOf(l) < base || (indentOf(l) === base && !listLine.test(l) && body.length && !lazy(body))) break;
        // Bullets after numbers, or the other way round, start a new list.
        if (j > i && indentOf(l) <= base + 1 && listLine.test(l) && ordered(l) !== kind) break;
        body.push(l);
      }
      blocks.push(parseList(body, base, depth));
      i = j - 1;
      continue;
    }
    text.push(line);
  }
  flush();
  return blocks;
}

/** Whether the last non-blank line in body continues a paragraph, so a
 * following unindented line is a lazy continuation of the item. */
function lazy(body: string[]): boolean {
  const last = body[body.length - 1];
  return last !== undefined && last.trim() !== "";
}

function parseList(lines: string[], base: number, depth: number): Block {
  const first = listLine.exec(lines[0])!;
  const ordered = /\d/.test(first[2]);
  const items: { text: string[]; rest: string[] }[] = [];
  for (const l of lines) {
    const m = listLine.exec(l);
    if (m && indentOf(l) <= base + 1) {
      items.push({ text: [m[3]], rest: [] });
      continue;
    }
    const cur = items[items.length - 1];
    // Text right under an item, before anything nested, continues it.
    if (!cur.rest.length && l.trim() && !listLine.test(l) && !fence.test(l)) cur.text.push(l.trim());
    else cur.rest.push(l);
  }
  const strip = (rest: string[]) => {
    const ind = rest.reduce((smallest, line) => line.trim() ? Math.min(smallest, indentOf(line)) : smallest, Infinity);
    return rest.map((x) => x.slice(Math.min(ind, indentOf(x)))).join("\n");
  };
  return {
    kind: "list",
    ordered,
    start: ordered ? parseInt(first[2], 10) : 1,
    items: items.map((it) => {
      const text = it.text.join("\n");
      const task = taskMark.exec(text);
      const children = it.rest.some((x) => x.trim()) ? mdBlocks(strip(it.rest), depth + 1) : [];
      return task ? { text: task[2], task: task[1] === " " ? "todo" : "done", children } : { text, children };
    }),
  };
}

/** Lines of a paragraph, keeping the single line breaks the CLIs use. */
function Lines({ text }: { text: string }) {
  const lines = text.split("\n");
  return (
    <>
      {lines.map((l, i) => (
        <span key={i}>
          {i > 0 && <br />}
          <Rich text={l} />
        </span>
      ))}
    </>
  );
}

/** Copies a code block; the icon says for a moment how it went. */
function CopyButton({ text }: { text: string }) {
  const [state, setState] = useState<"" | "ok" | "fail">("");
  useEffect(() => {
    if (!state) return;
    const t = setTimeout(() => setState(""), 1500);
    return () => clearTimeout(t);
  }, [state]);
  const copy = () => {
    navigator.clipboard.writeText(text).then(
      () => setState("ok"),
      () => setState("fail"),
    );
  };
  return (
    <button className="cp" onClick={copy} aria-label={state === "ok" ? "已复制" : state === "fail" ? "复制失败" : "复制代码"}>
      {state === "ok" ? <Check /> : state === "fail" ? <X /> : <Copy />}
    </button>
  );
}

/** A diff line's class: added, removed or neither. "+++ b/x" and "--- a/x"
 * name the files; "+++n;" is an added "++n;". */
function diffLine(l: string): string | undefined {
  if (/^(\+\+\+|---)(\s|$)/.test(l)) return undefined;
  return l.startsWith("+") ? "add" : l.startsWith("-") ? "del" : undefined;
}

function Code({ lang, text }: { lang: string; text: string }) {
  const lines = useMemo(() => text.split("\n"), [text]);
  const long = lines.length > FOLD_OVER;
  const [open, setOpen] = useState(false);
  const shown = long && !open ? lines.slice(0, FOLD_SHOWN) : lines;
  // Only a diff is colored: added and removed lines mean something.
  const diff = lang === "diff" || lang === "patch";
  return (
    <div className="cb">
      <pre>
        {/* As wide as its longest line and the copy button: scrolled to the end, no code is left under the button. */}
        <span className="cbt">
          {diff
            ? shown.map((l, i) => (
                <span key={i} className={diffLine(l)}>
                  {i > 0 && "\n"}
                  {l}
                </span>
              ))
            : shown.join("\n")}
        </span>
      </pre>
      <CopyButton text={text} />
      {long && (
        <button className="cbmore" onClick={() => setOpen(!open)} aria-expanded={open}>
          <span>
            {open ? "收起" : "展开全文"}
            {open ? <ChevronUp /> : <ChevronDown />}
          </span>
          <i>共 {lines.length} 行</i>
        </button>
      )}
    </div>
  );
}

/** A table that scrolls sideways when it is wider than the reply, its
 * right edge fading out while there is more to scroll to. */
function Table({ b }: { b: Extract<Block, { kind: "table" }> }) {
  const ref = useRef<HTMLDivElement>(null);
  const [more, setMore] = useState(false);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const mark = () => setMore(el.scrollLeft + el.clientWidth < el.scrollWidth - 1);
    mark();
    el.addEventListener("scroll", mark, { passive: true });
    const ro = typeof ResizeObserver === "undefined" ? undefined : new ResizeObserver(mark);
    ro?.observe(el);
    return () => {
      el.removeEventListener("scroll", mark);
      ro?.disconnect();
    };
  }, []);
  // The separator row's alignment wins; without one, quantities go right.
  const num = useMemo(() => b.head.map((_, k) => b.align[k] === undefined && numericColumn(b.rows.map((r) => r[k] ?? ""))), [b]);
  const cell = (k: number) => ({ className: num[k] ? "num" : undefined, style: b.align[k] ? { textAlign: b.align[k] } : undefined });
  return (
    <div ref={ref} className={more ? "mdtable more" : "mdtable"}>
      <table className={b.head.length <= 3 ? "compact" : undefined}>
        <thead>
          <tr>
            {b.head.map((c, k) => (
              <th key={k} {...cell(k)}>
                <div className={tableCellClass(c)}><Rich text={c} /></div>
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {b.rows.map((r, n) => (
            <tr key={n}>
              {b.head.map((_, k) => (
                <td key={k} {...cell(k)}>
                  <div className={tableCellClass(r[k] ?? "")}><Rich text={r[k] ?? ""} /></div>
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function Blocks({ blocks }: { blocks: Block[] }) {
  // A rule draws nothing but the space after it, so one that opens the
  // reply is left out: nothing is above it to be set apart from.
  let first = 0;
  while (blocks[first]?.kind === "rule") first++;
  return (
    <>
      {blocks.map((b, i) => {
        if (i < first) return null;
        switch (b.kind) {
          case "code":
            return <Code key={i} lang={b.lang} text={b.text} />;
          case "heading":
            return (
              <p key={i} className={"mdh h" + Math.min(b.level, 4)}>
                <Rich text={b.text} />
              </p>
            );
          case "rule":
            return <hr key={i} />;
          case "quote":
            return (
              <blockquote key={i}>
                <Blocks blocks={b.blocks} />
              </blockquote>
            );
          case "list": {
            const items = b.items.map((it, n) => (
              <li key={n} className={it.task === "done" ? "task done" : it.task ? "task" : undefined}>
                {it.task && (it.task === "done" ? <BoxChecked /> : <Box />)}
                <Lines text={it.text} />
                {it.children.length > 0 && <Blocks blocks={it.children} />}
              </li>
            ));
            const loose = looseList(b.items.map((it) => it.text));
            // Two-digit numbers need a wider gutter, or they hang out of the reply.
            const wide = b.ordered && b.start + b.items.length - 1 >= 10;
            const cls = [wide && "wide", loose && "loose"].filter(Boolean).join(" ") || undefined;
            return b.ordered ? (
              <ol key={i} start={b.start} className={cls}>
                {items}
              </ol>
            ) : (
              <ul key={i} className={cls}>
                {items}
              </ul>
            );
          }
          case "table":
            return <Table key={i} b={b} />;
          default:
            return (
              <p key={i} className={boldHeading(b.text) ? "bh" : undefined}>
                <Lines text={b.text} />
              </p>
            );
        }
      })}
    </>
  );
}

/** Markdown rendered as elements, never as HTML. */
export const Markdown = memo(function Markdown({ text }: { text: string }) {
  const blocks = useMemo(() => mdBlocks(text), [text]);
  return <Blocks blocks={blocks} />;
});
