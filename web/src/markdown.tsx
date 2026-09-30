// Markdown as the CLIs write it, rendered as React elements. Text is
// always text: nothing is inserted as HTML, and only http(s) links become
// links. Other targets, mostly local file paths, show as monospace refs.

import { memo, useMemo, type ReactNode } from "react";

/** Whether a link target may be opened: web pages only, so javascript:,
 * data: and file paths never become clickable. */
export function safeHref(url: string): string | undefined {
  return /^https?:\/\/[^\s]+$/i.test(url.trim()) ? url.trim() : undefined;
}

/** Inline Markdown: `code`, **bold**, *italic*, [links](target) and
 * <https://autolinks>. */
export function Rich({ text }: { text: string }) {
  const parts: ReactNode[] = [];
  const re = /`([^`\n]+)`|\*\*([^*\n]+)\*\*|(?<![\w*])\*([^*\s][^*\n]*?)\*(?![\w*])|\[([^\]\n]+)\]\(([^)\s]+)\)|<(https?:\/\/[^>\s]+)>/g;
  let last = 0;
  for (const m of text.matchAll(re)) {
    if (m.index > last) parts.push(text.slice(last, m.index));
    const key = m.index;
    if (m[1] !== undefined) parts.push(<code key={key}>{m[1]}</code>);
    else if (m[2] !== undefined) parts.push(<b key={key}>{m[2]}</b>);
    else if (m[3] !== undefined) parts.push(<em key={key}>{m[3]}</em>);
    else if (m[4] !== undefined) parts.push(<Link key={key} text={m[4]} target={m[5]} />);
    else parts.push(<Link key={key} text={m[6]} target={m[6]} />);
    last = m.index + m[0].length;
  }
  if (last < text.length) parts.push(text.slice(last));
  return <>{parts}</>;
}

function Link({ text, target }: { text: string; target: string }) {
  const href = safeHref(target);
  if (href)
    return (
      <a href={href} target="_blank" rel="noopener noreferrer nofollow">
        {text}
      </a>
    );
  return (
    <span className="ref" title={target}>
      {text}
    </span>
  );
}

// Any indent: the CLIs nest code blocks in list items.
const fence = /^\s*(`{3,}|~{3,})/;
const tableSep = /^\s*\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)*\|?\s*$/;
const heading = /^\s{0,3}(#{1,6})\s+(.*?)\s*#*\s*$/;
const rule = /^\s{0,3}([-*_])(\s*\1){2,}\s*$/;
const listLine = /^(\s*)([-*+]|\d{1,9}[.)])\s+(.*)$/;
const quote = /^\s{0,3}>\s?(.*)$/;

const indentOf = (l: string) => l.length - l.trimStart().length;

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
  let code = false;
  for (let i = 0; i < s.length; i++) {
    const c = s[i];
    if (c === "\\" && (s[i + 1] === "|" || s[i + 1] === "\\")) {
      cur += s[i + 1] === "|" ? "|" : "\\\\";
      i++;
    } else if (c === "`") {
      code = !code;
      cur += c;
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
  children: Block[];
}

export type Block =
  | { kind: "text"; text: string }
  | { kind: "code"; text: string }
  | { kind: "heading"; level: number; text: string }
  | { kind: "rule" }
  | { kind: "quote"; blocks: Block[] }
  | { kind: "list"; ordered: boolean; start: number; items: ListItem[] }
  | { kind: "table"; head: string[]; align: ("left" | "center" | "right" | undefined)[]; rows: string[][] };

/** Splits Markdown into blocks: fenced code, tables, headings, rules,
 * quotes, lists and paragraphs. */
export function mdBlocks(md: string): Block[] {
  const lines = md.replace(/\r\n?/g, "\n").split("\n");
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
      blocks.push({ kind: "code", text: body.join("\n") });
      i = j;
      continue;
    }
    if (line.includes("|") && i + 1 < lines.length && tableSep.test(lines[i + 1]) && lines[i + 1].includes("-")) {
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
      blocks.push({ kind: "heading", level: h[1].length, text: h[2] });
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
      blocks.push({ kind: "quote", blocks: mdBlocks(body.join("\n")) });
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
          const next = lines.slice(j + 1).find((x) => x.trim());
          if (next === undefined || (indentOf(next) <= base && !listLine.test(next))) break;
          body.push(l);
          continue;
        }
        if (indentOf(l) < base || (indentOf(l) === base && !listLine.test(l) && body.length && !lazy(body))) break;
        // Bullets after numbers, or the other way round, start a new list.
        if (j > i && indentOf(l) <= base + 1 && listLine.test(l) && ordered(l) !== kind) break;
        body.push(l);
      }
      blocks.push(parseList(body, base));
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

function parseList(lines: string[], base: number): Block {
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
    const ind = Math.min(...rest.filter((x) => x.trim()).map(indentOf));
    return rest.map((x) => x.slice(Math.min(ind, indentOf(x)))).join("\n");
  };
  return {
    kind: "list",
    ordered,
    start: ordered ? parseInt(first[2], 10) : 1,
    items: items.map((it) => ({ text: it.text.join("\n"), children: it.rest.some((x) => x.trim()) ? mdBlocks(strip(it.rest)) : [] })),
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

function Blocks({ blocks }: { blocks: Block[] }) {
  return (
    <>
      {blocks.map((b, i) => {
        switch (b.kind) {
          case "code":
            return <pre key={i}>{b.text}</pre>;
          case "heading":
            return (
              <p key={i} className="mdh">
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
              <li key={n}>
                <Lines text={it.text} />
                {it.children.length > 0 && <Blocks blocks={it.children} />}
              </li>
            ));
            return b.ordered ? (
              <ol key={i} start={b.start}>
                {items}
              </ol>
            ) : (
              <ul key={i}>{items}</ul>
            );
          }
          case "table":
            return (
              <div key={i} className="mdtable">
                <table>
                  <thead>
                    <tr>
                      {b.head.map((c, k) => (
                        <th key={k} style={{ textAlign: b.align[k] }}>
                          <Rich text={c} />
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {b.rows.map((r, n) => (
                      <tr key={n}>
                        {b.head.map((_, k) => (
                          <td key={k} style={{ textAlign: b.align[k] }}>
                            <Rich text={r[k] ?? ""} />
                          </td>
                        ))}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            );
          default:
            return (
              <p key={i}>
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
