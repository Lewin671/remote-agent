import { Terminal as XTerm } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";
import { useEffect, useRef, useState } from "react";
import { errorText, type Remote, type SessionView, type TermConn } from "../remote";

const font = 'ui-monospace, Menlo, "SF Mono", monospace';
const enc = new TextEncoder();

/** Width of a monospace character as a fraction of the font size. */
function charRatio(): number {
  const ctx = document.createElement("canvas").getContext("2d");
  if (!ctx) return 0.6;
  ctx.font = `100px ${font}`;
  return ctx.measureText("M").width / 100;
}

// Special keys for phones, as in the mockup.
const keys: [string, string][] = [
  ["esc", "\x1b"],
  ["tab", "\t"],
  ["^C", "\x03"],
  ["↑", "\x1b[A"],
  ["↓", "\x1b[B"],
  ["⏎", "\r"],
];

/** The session's terminal, streamed live. Typing goes to the session like
 * the local keyboard. The tmux window keeps its size unless fitted. */
export function TerminalView({ remote, session, compact }: { remote: Remote; session: SessionView; compact?: boolean }) {
  const box = useRef<HTMLDivElement>(null);
  const host = useRef<HTMLDivElement>(null);
  const term = useRef<XTerm>(null);
  const conn = useRef<TermConn | null>(null);
  const size = useRef({ cols: 80, rows: 24 });
  const [state, setState] = useState<"connecting" | "live" | "closed">("connecting");
  const [reason, setReason] = useState("");
  const [fitted, setFitted] = useState(false);
  const [attempt, setAttempt] = useState(0);

  // Scale the font so the whole width of the tmux window fits.
  const scale = () => {
    const t = term.current;
    const el = box.current;
    if (!t || !el) return;
    const width = el.clientWidth - 12;
    const fs = Math.max(5, Math.min(14, Math.floor((10 * width) / (size.current.cols * charRatio())) / 10));
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
    const onResize = () => scale();
    addEventListener("resize", onResize);
    return () => {
      typed.dispose();
      removeEventListener("resize", onResize);
      t.dispose();
      term.current = null;
    };
  }, []);

  useEffect(() => {
    let alive = true;
    setState("connecting");
    setReason("");
    setFitted(false);
    remote
      .openTerminal(
        session.id,
        (f) => {
          const t = term.current;
          if (!t) return;
          switch (f.kind.case) {
            case "snapshot":
              size.current = { cols: f.kind.value.cols, rows: f.kind.value.rows };
              t.resize(f.kind.value.cols, f.kind.value.rows);
              t.reset();
              t.write(f.kind.value.data);
              scale();
              setState("live");
              break;
            case "output":
              t.write(f.kind.value);
              break;
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
      )
      .then(
        (c) => {
          if (alive) conn.current = c;
          else c.close();
        },
        (e) => {
          if (!alive) return;
          setState("closed");
          setReason(errorText(e));
        },
      );
    return () => {
      alive = false;
      conn.current?.close();
      conn.current = null;
    };
  }, [remote, session.id, attempt]); // eslint-disable-line react-hooks/exhaustive-deps

  const send = (s: string) => conn.current?.send({ kind: { case: "input", value: enc.encode(s) } });

  const toggleFit = () => {
    const el = box.current;
    if (!el) return;
    if (fitted) {
      conn.current?.send({ kind: { case: "resize", value: { fit: false } } });
      setFitted(false);
      return;
    }
    // At a readable size, how much fits here.
    const fs = 12;
    const cols = Math.floor((el.clientWidth - 12) / (fs * charRatio()));
    const rows = Math.floor((el.clientHeight - 12) / (fs * 1.2));
    conn.current?.send({ kind: { case: "resize", value: { cols, rows, fit: true } } });
    setFitted(true);
  };

  return (
    <div className={"termview" + (compact ? " compact" : "")}>
      <div className="term" ref={box} onClick={() => term.current?.focus()}>
        <div ref={host} />
        {state === "live" && (
          <button className="fit" onClick={(e) => (e.stopPropagation(), toggleFit())}>
            {fitted ? "取消适配" : "⤢ 适配屏幕"}
          </button>
        )}
        {state !== "live" && (
          <div className="term-state">
            {state === "connecting" ? "连接终端…" : reason || "终端已断开"}
            {state === "closed" && (
              <button className="link" onClick={() => setAttempt((a) => a + 1)}>
                重连
              </button>
            )}
          </div>
        )}
      </div>
      {!compact && (
        <div className="keys">
          {keys.map(([label, seq]) => (
            <button key={label} onClick={() => send(seq)} disabled={state !== "live"}>
              {label}
            </button>
          ))}
          <button onClick={() => term.current?.focus()} aria-label="键盘">
            ⌨
          </button>
        </div>
      )}
    </div>
  );
}
