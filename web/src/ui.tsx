// Small building blocks shared by the views.

import { useEffect, useState, useSyncExternalStore, type ReactNode } from "react";
import { avatarClass, contextPercent, modeOf, type Tone } from "./model";
import type { Remote, SessionView } from "./remote";

export function useRemote(remote: Remote) {
  useSyncExternalStore(remote.subscribe, remote.snapshot);
}

/** Re-renders every ms, for countdowns and relative times. */
export function useNow(ms: number) {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), ms);
    return () => clearInterval(t);
  }, [ms]);
  return now;
}

export function Dot({ tone }: { tone: Tone }) {
  return <span className={"dot " + tone} />;
}

export function Avatar({ name, size = "" }: { name: string; size?: "" | "sm" | "lg" }) {
  return <div className={`av ${size} ${avatarClass(name)}`}>{(name[0] || "?").toLowerCase()}</div>;
}

/** Text with Markdown's `code`, **bold** and [links](target), shown as
 * text: links are mostly local paths, so only their words are kept. */
export function Rich({ text }: { text: string }) {
  const parts: ReactNode[] = [];
  const re = /`([^`\n]+)`|\*\*([^*\n]+)\*\*|\[([^\]\n]+)\]\(([^)\s]+)\)/g;
  let last = 0;
  for (const m of text.matchAll(re)) {
    if (m.index > last) parts.push(text.slice(last, m.index));
    const key = m.index;
    if (m[1] !== undefined) parts.push(<code key={key}>{m[1]}</code>);
    else if (m[2] !== undefined) parts.push(<b key={key}>{m[2]}</b>);
    else parts.push(<span key={key} className="mdlink" title={m[4]}>{m[3]}</span>);
    last = m.index + m[0].length;
  }
  if (last < text.length) parts.push(text.slice(last));
  return <>{parts}</>;
}

// Any indent: Codex nests code blocks in list items.
const fence = /^\s*(`{3,}|~{3,})/;
const tableSep = /^\s*\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)*\|?\s*$/;
const heading = /^\s{0,3}(#{1,6})\s+(.*?)\s*#*\s*$/;
const rule = /^\s{0,3}([-*_])(\s*\1){2,}\s*$/;
const bullet = /^(\s*)[-*+]\s+/;

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

type Block =
  | { kind: "text"; text: string }
  | { kind: "code"; text: string }
  | { kind: "heading"; text: string }
  | { kind: "rule" }
  | { kind: "table"; head: string[]; align: ("left" | "center" | "right" | undefined)[]; rows: string[][] };

/** Splits Markdown into blocks: fenced code, tables, headings, rules and
 * text in between. */
export function mdBlocks(md: string): Block[] {
  const lines = md.split("\n");
  const blocks: Block[] = [];
  let text: string[] = [];
  const flush = () => {
    while (text.length && !text[0].trim()) text.shift();
    while (text.length && !text[text.length - 1].trim()) text.pop();
    if (text.length) blocks.push({ kind: "text", text: text.join("\n") });
    text = [];
  };
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    const f = fence.exec(line);
    if (f) {
      const body: string[] = [];
      // Closed by a fence of the same character, at least as long.
      const close = new RegExp(`^\\s*${f[1][0] === "`" ? "`" : "~"}{${f[1].length},}\\s*$`);
      let j = i + 1;
      // The fence's own indent is the list's, not the code's.
      const indent = line.length - line.trimStart().length;
      const dedent = (l: string) => l.slice(Math.min(indent, l.length - l.trimStart().length));
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
      blocks.push({ kind: "heading", text: h[2] });
      continue;
    }
    if (rule.test(line)) {
      flush();
      blocks.push({ kind: "rule" });
      continue;
    }
    text.push(line.replace(bullet, "$1• "));
  }
  flush();
  return blocks;
}

/** Markdown as Codex writes it: blocks from mdBlocks, inline parts by
 * Rich. Everything is rendered as text, never as HTML. */
export function Markdown({ text }: { text: string }) {
  return (
    <>
      {mdBlocks(text).map((b, i) => {
        switch (b.kind) {
          case "code":
            return (
              <pre key={i} className="mdcode">
                {b.text}
              </pre>
            );
          case "heading":
            return (
              <div key={i} className="mdh">
                <Rich text={b.text} />
              </div>
            );
          case "rule":
            return <hr key={i} className="mdrule" />;
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
              <div key={i} className="mdp">
                <Rich text={b.text} />
              </div>
            );
        }
      })}
    </>
  );
}

/** Approval mode, model, effort and context use, as read from Codex.
 * With handlers the chips open the sheets that change them. */
export function Chips({ s, onMode, onModel }: { s: SessionView; onMode?: () => void; onModel?: () => void }) {
  const mode = modeOf(s);
  const pct = contextPercent(s);
  const i = s.info;
  if (!mode && !i?.model && pct === undefined) return null;
  const Chip = ({ cls, onClick, children }: { cls: string; onClick?: () => void; children: ReactNode }) =>
    onClick ? (
      <button className={cls} onClick={onClick}>
        {children}
      </button>
    ) : (
      <span className={cls}>{children}</span>
    );
  return (
    <>
      {mode && (
        <Chip cls={"chip " + mode.cls} onClick={onMode}>
          {mode.text}
        </Chip>
      )}
      {i?.model && (
        <Chip cls="chip" onClick={onModel}>
          {i.model}
          {onModel && " ▾"}
        </Chip>
      )}
      {i?.effort && (
        <Chip cls="chip" onClick={onModel}>
          {i.effort}
        </Chip>
      )}
      {pct !== undefined && (
        <Chip cls={"chip" + (pct >= 80 ? " warn" : "")} onClick={onModel}>
          <span className="ring" style={{ ["--p" as string]: pct, ["--c" as string]: pct >= 80 ? "var(--orange)" : undefined }} />
          {pct}%
        </Chip>
      )}
    </>
  );
}
