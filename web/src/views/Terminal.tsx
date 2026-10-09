import { usePageVisible, useRemote } from "../ui";
import { Terminal as XTerm } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { errorText, type Remote, type SessionView, type TermConn } from "../remote";
import { ArrowDown, ArrowLeft, ArrowRight, ArrowUp, CornerDownLeft, Keyboard, Maximize2 } from "../icons";

const font = 'ui-monospace, Menlo, "SF Mono", monospace';
const enc = new TextEncoder();

/** Width of a monospace character as a fraction of the font size. */
function charRatio(): number {
  const ctx = document.createElement("canvas").getContext("2d");
  if (!ctx) return 0.6;
  ctx.font = `100px ${font}`;
  return ctx.measureText("M").width / 100;
}

// Special keys for phones, in two rows as in the mockup: the arrows and
// Enter first, then Tab, Ctrl+C and the keyboard. None hides in a scroll.
const keys: [ReactNode, string, string][] = [
  ["Esc", "\x1b", "Esc"],
  [<ArrowLeft />, "\x1b[D", "左"],
  [<ArrowUp />, "\x1b[A", "上"],
  [<ArrowDown />, "\x1b[B", "下"],
  [<ArrowRight />, "\x1b[C", "右"],
  [<CornerDownLeft />, "\r", "回车"],
];
const keys2: [ReactNode, string, string][] = [
  ["Tab", "\t", "Tab"],
  ["Ctrl+C", "\x03", "Ctrl+C"],
];

/** The session's terminal, streamed live. Typing goes to the session like
 * the local keyboard. Opening it fits the tmux window to this screen. */
export function TerminalView({ remote, session, compact, wide, running }: { remote: Remote; session: SessionView; compact?: boolean; wide?: boolean; running: boolean }) {
  useRemote(remote);
  const online = remote.conn === "online";
  const visible = usePageVisible();
  const box = useRef<HTMLDivElement>(null);
  const host = useRef<HTMLDivElement>(null);
  const term = useRef<XTerm>(null);
  const conn = useRef<TermConn | null>(null);
  const size = useRef({ cols: 80, rows: 24 });
  const dimsSeen = useRef(false);
  const [dims, setDims] = useState<{ cols: number; rows: number }>();
  const [state, setState] = useState<"connecting" | "live" | "closed">("connecting");
  const [reason, setReason] = useState("");
  const [details, setDetails] = useState(false);
  const [fitted, setFitted] = useState(false);
  // The window is shared: the size this screen was fitted to, as the first
  // snapshot after fitting showed it. A later snapshot of another size
  // means the computer or another client changed it, and "fitted" is no
  // longer true.
  const fittedTo = useRef<{ cols: number; rows: number } | "pending">(undefined);
  // After "还原" the next size change is this screen's own doing.
  const releasing = useRef(false);
  const [changed, setChanged] = useState(false);
  const [attempt, setAttempt] = useState(0);
  // Each connection fits once on its own, unless "还原" was chosen here.
  const [linked, setLinked] = useState(false);
  const autoFitted = useRef(false);
  const released = useRef(false);

  // Scale the font so the whole width of the tmux window fits.
  const scale = () => {
    const t = term.current;
    const el = box.current;
    if (!t || !el) return;
    const width = el.clientWidth - 12;
    const fs = Math.max(5, Math.min(fittedTo.current ? 12 : 14, Math.floor((10 * width) / (size.current.cols * charRatio())) / 10));
    if (t.options.fontSize !== fs) t.options.fontSize = fs;
  };

  useEffect(() => {
    const t = new XTerm({
      fontFamily: font,
      fontSize: 12,
      cursorBlink: true,
      scrollback: 2000,
      theme: { background: "#0d1117", foreground: "#c9d1d9", cursor: "#c9d1d9" },
      allowProposedApi: false,
    });
    term.current = t;
    t.open(host.current!);
    const typed = t.onData((d) => conn.current?.send({ kind: { case: "input", value: enc.encode(d) } }));
    // A fitted window follows the width of its pane: the browser window, or
    // the switch between the terminal alone and side by side. Height is left
    // alone, so a phone's keyboard never resizes the remote window.
    let width = box.current!.clientWidth;
    let refit: ReturnType<typeof setTimeout> | undefined;
    const sized = new ResizeObserver(() => {
      scale();
      const now = box.current?.clientWidth ?? width;
      if (now === width) return;
      width = now;
      clearTimeout(refit);
      refit = setTimeout(() => fittedTo.current && fitNow.current(), 200);
    });
    sized.observe(box.current!);
    return () => {
      typed.dispose();
      sized.disconnect();
      clearTimeout(refit);
      t.dispose();
      term.current = null;
    };
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (!visible || !online) return;
    let alive = true;
    const opening = new AbortController();
    setState("connecting");
    setReason("");
    setDetails(false);
    setFitted(false);
    fittedTo.current = undefined;
    dimsSeen.current = false;
    releasing.current = false;
    autoFitted.current = false;
    setLinked(false);
    remote
      .openTerminal(
        session.id,
        (f) => {
          if (!alive) return;
          const t = term.current;
          if (!t) return;
          switch (f.kind.case) {
            case "snapshot": {
              const next = { cols: f.kind.value.cols, rows: f.kind.value.rows };
              const was = size.current;
              const target = fittedTo.current;
              if (target === "pending") fittedTo.current = next;
              else if (!dimsSeen.current || (next.cols === was.cols && next.rows === was.rows)) {
                // The first snapshot, or the same size again: nothing changed.
              } else if (releasing.current) releasing.current = false;
              else {
                // Resized elsewhere: whatever this screen fitted no longer holds.
                fittedTo.current = undefined;
                setFitted(false);
                setChanged(true);
              }
              dimsSeen.current = true;
              size.current = next;
              setDims(size.current);
              t.resize(f.kind.value.cols, f.kind.value.rows);
              t.reset();
              const data = f.kind.value.data;
              const written = new Promise<void>((resolve) => t.write(data, resolve));
              scale();
              setState("live");
              return written;
            }
            case "output": {
              const data = f.kind.value;
              return new Promise<void>((resolve) => t.write(data, resolve));
            }
            case "closed":
              setReason(f.kind.value);
              break;
          }
        },
        (why) => {
          if (!alive) return;
          setState("closed");
          setReason((r) => r || why);
        },
        opening.signal,
      )
      .then(
        (c) => {
          if (!alive) return c.close();
          conn.current = c;
          setLinked(true);
        },
        (e) => {
          if (!alive) return;
          setState("closed");
          setReason(errorText(e));
        },
      );
    return () => {
      alive = false;
      opening.abort();
      conn.current?.close();
      conn.current = null;
    };
  }, [remote, session.id, attempt, visible, online]); // eslint-disable-line react-hooks/exhaustive-deps

  const send = (s: string) => conn.current?.send({ kind: { case: "input", value: enc.encode(s) } });

  const fit = () => {
    const el = box.current;
    if (!el) return;
    // At a readable size, how much fits here.
    const fs = 12;
    const cols = Math.floor((el.clientWidth - 12) / (fs * charRatio()));
    const rows = Math.floor((el.clientHeight - 12) / (fs * 1.2));
    if (cols < 20 || rows < 5) return;
    setChanged(false);
    // A window already this size sends no new snapshot to confirm it.
    const same = cols === size.current.cols && rows === size.current.rows;
    if (same && fittedTo.current) return;
    fittedTo.current = same ? { cols, rows } : "pending";
    releasing.current = false;
    conn.current?.send({ kind: { case: "resize", value: { cols, rows, fit: true } } });
    setFitted(true);
  };
  const fitNow = useRef(fit);
  fitNow.current = fit;

  const toggleFit = () => {
    released.current = fitted;
    if (!fitted) return fit();
    setChanged(false);
    conn.current?.send({ kind: { case: "resize", value: { fit: false } } });
    fittedTo.current = undefined;
    releasing.current = true;
    setFitted(false);
  };

  useEffect(() => {
    if (state !== "live" || !linked || autoFitted.current) return;
    autoFitted.current = true;
    if (!released.current) fit();
  }, [state, linked]); // eslint-disable-line react-hooks/exhaustive-deps

  const live = state === "live";
  return (
    <div className={"termview" + (compact ? " compact" : "")}>
      <div className="ttool">
        <span>
          {!live ? (state === "connecting" ? "连接终端…" : "终端已断开") : fitted ? `已适配这块屏幕 · ${dims?.cols}×${dims?.rows}` : `${changed ? "远端" : "电脑上"} ${dims?.cols}×${dims?.rows}`}
        </span>
        {live && <button onClick={toggleFit}>{fitted ? "还原" : <><Maximize2 /> {wide ? "适配" : "适配我的屏幕"}</>}</button>}
      </div>
      <div className="term" ref={box} onClick={() => term.current?.focus()}>
        <div ref={host} className={state === "closed" ? "stale" : ""} />
        {changed && state === "live" && (
          <div className="tstate">
            <b>远端尺寸已变化</b>
            <p>窗口尺寸已变化，可能来自电脑或另一客户端。这里按最新尺寸显示；打开键盘不改远端尺寸。</p>
            <div className="btns">
              <button className="btn" onClick={() => setChanged(false)}>
                知道了
              </button>
            </div>
          </div>
        )}
        {state === "closed" && (
          <div className="tstate">
            <b>终端连接断开了</b>
            <p>下面是断开前的画面。{running ? "会话还在运行，聊天照常更新。" : "会话已经结束。"}</p>
            <div className="btns">
              <button className="btn" onClick={() => setAttempt((a) => a + 1)}>
                重新连接
              </button>
            </div>
            {reason &&
              (details ? (
                <span className="det mono">{reason}</span>
              ) : (
                <button className="det" onClick={() => setDetails(true)}>
                  查看详情
                </button>
              ))}
          </div>
        )}
      </div>
      {!compact && (
        <div className={"keys" + (live ? "" : " off")}>
          <div className="keyrow">
            {keys.map(([label, seq, name]) => (
              <button key={name} onClick={() => send(seq)} disabled={!live} aria-label={name}>
                {label}
              </button>
            ))}
          </div>
          <div className="keyrow">
            {keys2.map(([label, seq, name]) => (
              <button key={name} onClick={() => send(seq)} disabled={!live} aria-label={name}>
                {label}
              </button>
            ))}
            <button onClick={() => term.current?.focus()} disabled={!live} aria-label="键盘">
              <Keyboard /> 键盘
            </button>
          </div>
        </div>
      )}
    </div>
  );
}
