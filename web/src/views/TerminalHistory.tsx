import { useLayoutEffect, useRef } from "react";
import type { TerminalHistory } from "../terminal-history";

export function TerminalHistoryPanel({ history, load, online }: { history: TerminalHistory; load: () => void; online: boolean }) {
  const box = useRef<HTMLDivElement>(null);
  const previous = useRef({ count: 0, height: 0 });
  useLayoutEffect(() => { box.current?.focus({ preventScroll: true }); }, []);
  useLayoutEffect(() => {
    const el = box.current!;
    if (history.pages.length !== previous.current.count) {
      // Prepending preserves what was in sight; first opening starts at the newest line.
      el.scrollTop = previous.current.count ? el.scrollTop + el.scrollHeight - previous.current.height : el.scrollHeight;
    }
    previous.current = { count: history.pages.length, height: el.scrollHeight };
  });
  return <div className="terminal-history" role="region" aria-label="终端历史" tabIndex={0} ref={box} onScroll={() => {
    const el = box.current!;
    if (el.scrollTop < 80 && online && !history.pending && !history.error && history.cursor) load();
  }}>
    <div className="terminal-history-status">
      {history.pending ? "读取历史…" : history.error ? <><span role="alert">{history.error}</span> <button disabled={!online} onClick={load}>重试</button></> : history.cursor ? <button disabled={!online} onClick={load}>读取更早历史</button> : history.pages.length ? "已到保留历史的开头" : "暂无历史"}
    </div>
    {history.pages.map(page => <pre key={page.id}>{page.text}{"\n"}</pre>)}
  </div>;
}
