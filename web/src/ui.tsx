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
