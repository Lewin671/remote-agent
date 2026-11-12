import * as i18n from "../i18n";
import { TerminalHistory } from "../terminal-history";
import { wheelAxis } from "../wheel";
import { BackToLive, TerminalReader, TERM_FONT } from "./TerminalHistory";
import { usePointerFine, useRemote } from "../ui";
import { Terminal as XTerm } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { errorText, type Remote, type SessionView, type TermConn } from "../remote";
import { ArrowDown, ArrowLeft, ArrowRight, ArrowUp, CornerDownLeft, Keyboard, Maximize2 } from "../icons";

const enc = new TextEncoder();

/** Width of a monospace character as a fraction of the font size. */
function charRatio(): number {
  const ctx = document.createElement("canvas").getContext("2d");
  if (!ctx) return 0.6;
  ctx.font = `100px ${TERM_FONT}`;
  return ctx.measureText("M").width / 100;
}

/** The height of one terminal row, as the live terminal lays it out. */
function cellHeight(t: XTerm): number {
  const screen = t.element?.querySelector<HTMLElement>(".xterm-screen");
  if (screen && screen.clientHeight > 0 && t.rows > 0) return screen.clientHeight / t.rows;
  return (t.options.fontSize ?? 12) * 1.2;
}

// Special keys for phones, in two rows as in the mockup: the arrows and
// Enter first, then Tab, Ctrl+C and the keyboard. None hides in a scroll.
function keys(): [ReactNode, string, string][] { return [
  ["Esc", "\x1b", "Esc"],
  [<ArrowLeft />, "\x1b[D", i18n.t("terminal.left")],
  [<ArrowUp />, "\x1b[A", i18n.t("terminal.up")],
  [<ArrowDown />, "\x1b[B", i18n.t("terminal.down")],
  [<ArrowRight />, "\x1b[C", i18n.t("terminal.right")],
  [<CornerDownLeft />, "\r", i18n.t("terminal.enter")],
]; }
const keys2: [ReactNode, string, string][] = [
  ["Tab", "\t", "Tab"],
  ["Ctrl+C", "\x03", "Ctrl+C"],
];

/** The session's terminal, streamed live. Typing goes to the session like
 * the local keyboard. Opening it fits the tmux window to this screen.
 * Scrolling up opens the read-only reader over the same area. */
export function TerminalView({ remote, session, compact, wide, running }: { remote: Remote; session: SessionView; compact?: boolean; wide?: boolean; running: boolean }) {
  useRemote(remote);
  const online = remote.conn === "online";
  const fine = usePointerFine();
  const box = useRef<HTMLDivElement>(null);
  const host = useRef<HTMLDivElement>(null);
  // Scrolls the live screen sideways when it is wider than the pane even at the smallest size.
  const pan = useRef<HTMLDivElement>(null);
  const term = useRef<XTerm>(null);
  const conn = useRef<TermConn | null>(null);
  const size = useRef({ cols: 80, rows: 24 });
  const viewport = useRef<{ width: number; height: number }>(undefined);
  const dimsSeen = useRef(false);
  const [dims, setDims] = useState<{ cols: number; rows: number }>();
  const [state, setState] = useState<"connecting" | "live" | "closed">("connecting");
  const [reason, setReason] = useState("");
  const [details, setDetails] = useState(false);
  const [, refreshHistory] = useState(0);
  const historyRef = useRef<TerminalHistory>(null);
  if (!historyRef.current) historyRef.current = new TerminalHistory(() => refreshHistory(n => n + 1));
  const history = historyRef.current;
  const loadHistory = (fresh = false) => history.request((requestId, cursor) => conn.current?.send({ kind: { case: "historyRequest", value: { requestId, cursor } } }), fresh);
  // How the reader shows text, and how far up the first page opens: taken
  // from the live terminal at the moment the gesture opened it.
  const [reader, setReader] = useState<{ font: number; height: number; offset: number; left: number; grid: number }>();
  const [newOutput, setNewOutput] = useState(false);
  const beginReading = (distance: number) => {
    const t = term.current;
    if (history.phase === "reading" || history.pending || !conn.current || !t) return;
    setNewOutput(false);
    setReader({ font: t.options.fontSize ?? 12, height: cellHeight(t), offset: distance, left: pan.current?.scrollLeft ?? 0, grid: host.current?.offsetWidth ?? 0 });
    // Reading is passive: any open keyboard goes away.
    t.blur();
    loadHistory(true);
  };
  const beginRef = useRef(beginReading);
  beginRef.current = beginReading;
  const leaveReading = () => {
    history.close();
    setNewOutput(false);
  };
  const [fitted, setFitted] = useState(false);
  // The first screen is drawn at the size the computer has and then fitted to
  // this pane, the font scaled to it: seen, the text would jump about. It
  // shows once the fitted screen is in, or after a moment if it never is.
  const [shown, setShown] = useState(false);
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
  /** This connection's first snapshot is on the screen. */
  const pictured = useRef(false);
  const released = useRef(false);

  // Scale the font so the whole width of the tmux window fits.
  const scale = () => {
    if (document.hidden) return;
    const t = term.current;
    const el = box.current;
    if (!t || !el) return;
    const width = (viewport.current?.width ?? el.clientWidth) - 12;
    const fs = Math.max(6, Math.min(fittedTo.current ? 12 : 14, Math.floor((10 * width) / (size.current.cols * charRatio())) / 10));
    if (t.options.fontSize !== fs) t.options.fontSize = fs;
  };

  useEffect(() => {
    const t = new XTerm({
      fontFamily: TERM_FONT,
      fontSize: 12,
      cursorBlink: true,
      // The reader owns scrolling; the live screen keeps no local scrollback
      // that a wheel could turn into arrow keys.
      scrollback: 0,
      // No background of its own: the view's terminal surface shows through,
      // so no seam shows around the screen and nothing here follows the theme.
      allowTransparency: true,
      theme: { background: "rgba(0,0,0,0)", foreground: "#c9d1d9", cursor: "#c9d1d9" },
      allowProposedApi: false,
    });
    term.current = t;
    t.attachCustomWheelEventHandler(() => false);
    // Read the pane once before xterm writes its DOM. Subsequent dimensions
    // come from layout notifications, not reads after changing font size.
    viewport.current = { width: box.current!.clientWidth, height: box.current!.clientHeight };
    t.open(host.current!);
    const typed = t.onData((d) => !history.visible && conn.current?.send({ kind: { case: "input", value: enc.encode(d) } }));
    // A fitted window follows the width of its pane: the browser window, or
    // the switch between the terminal alone and side by side. Height is left
    // alone, so a phone's keyboard never resizes the remote window.
    let width = viewport.current.width;
    let paneHeight = 0, screenHeight = 0;
    let refit: ReturnType<typeof setTimeout> | undefined;
    const resize = () => {
      if (document.hidden) return;
      if (pictured.current && conn.current && !autoFitted.current) {
        autoFitted.current = true;
        if (!released.current) fitNow.current();
      }
      scale();
      // A screen taller than the pane (a shorter pane than the window, or
      // an open keyboard) shows its bottom rows, where the prompt is.
      const screen = host.current;
      if (screen) {
        const margin = `${Math.min(0, paneHeight - screenHeight)}px`;
        if (screen.style.marginTop !== margin) screen.style.marginTop = margin;
      }
      const now = viewport.current?.width ?? width;
      if (now === width) return;
      width = now;
      clearTimeout(refit);
      refit = setTimeout(() => fittedTo.current && fitNow.current(), 200);
    };
    const sized = new ResizeObserver((entries) => {
      for (const entry of entries) {
        const border = entry.borderBoxSize[0];
        if (entry.target === box.current) viewport.current = { width: Math.round(border.inlineSize), height: Math.round(border.blockSize) };
        if (entry.target === pan.current) paneHeight = Math.round(entry.contentRect.height);
        if (entry.target === host.current) screenHeight = Math.round(border.blockSize);
      }
      // A font change resizes the screen and produces another notification
      // before paint; do not force that layout synchronously to align it.
      resize();
    });
    document.addEventListener("visibilitychange", resize);
    sized.observe(box.current!);
    sized.observe(host.current!);
    sized.observe(pan.current!);
    return () => {
      typed.dispose();
      sized.disconnect();
      document.removeEventListener("visibilitychange", resize);
      clearTimeout(refit);
      t.dispose();
      term.current = null;
    };
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  // A scroll-up gesture on the live terminal opens the reader. It never
  // reaches the CLI: the wheel handler above already stops xterm, and a
  // touch that turns into a scroll is swallowed here.
  useEffect(() => {
    const el = box.current;
    if (!el) return;
    let startX = 0;
    let startY = 0;
    let tracking = false;
    const axisOf = wheelAxis();
    const onWheel = (e: WheelEvent) => {
      // A sideways scroll pans a wide screen natively; only an upward one reads.
      const axis = axisOf(e);
      // xterm cancels every wheel event while the program reports the mouse, which
      // would stop the native sideways scroll: a sideways one never reaches it.
      if (axis === "x") {
        e.stopPropagation();
        return;
      }
      if (history.phase === "reading") return;
      if (axis === "y" && e.deltaY < 0) {
        e.preventDefault();
        beginRef.current(-e.deltaY);
      }
    };
    const onStart = (e: TouchEvent) => {
      if (history.phase === "reading" || e.touches.length !== 1) return;
      tracking = true;
      startX = e.touches[0].clientX;
      startY = e.touches[0].clientY;
    };
    const onMove = (e: TouchEvent) => {
      if (!tracking) return;
      const dx = e.touches[0].clientX - startX;
      const dy = e.touches[0].clientY - startY;
      // The first clear direction decides: sideways is left to the pan.
      if (Math.abs(dx) > 12 && Math.abs(dx) > Math.abs(dy)) {
        tracking = false;
      } else if (dy > 12) {
        e.preventDefault();
        tracking = false;
        beginRef.current(dy);
      } else if (dy < -12) {
        tracking = false;
      }
    };
    const onEnd = () => { tracking = false; };
    el.addEventListener("wheel", onWheel, { capture: true, passive: false });
    el.addEventListener("touchstart", onStart, { passive: true });
    el.addEventListener("touchmove", onMove, { passive: false });
    el.addEventListener("touchend", onEnd, { passive: true });
    return () => {
      el.removeEventListener("wheel", onWheel, { capture: true });
      el.removeEventListener("touchstart", onStart);
      el.removeEventListener("touchmove", onMove);
      el.removeEventListener("touchend", onEnd);
    };
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (!online) return;
    let alive = true;
    const opening = new AbortController();
    history.close();
    setState("connecting");
    setReason("");
    setDetails(false);
    setFitted(false);
    setShown(false);
    fittedTo.current = undefined;
    dimsSeen.current = false;
    releasing.current = false;
    autoFitted.current = false;
    pictured.current = false;
    setLinked(false);
    remote
      .openTerminal(
        session.id,
        (f) => {
          if (!alive) return;
          const t = term.current;
          if (!t) return;
          switch (f.kind.case) {
            case "historyPage":
              history.receive(f.kind.value);
              return;
            case "snapshot": {
              if (history.visible) setNewOutput(true);
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
              pictured.current = true;
              if (conn.current) setState("live");
              return written;
            }
            case "output": {
              if (history.visible) setNewOutput(true);
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
          history.disconnected();
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
          // The snapshot can be here before the opening command has answered: live once both are.
          if (pictured.current) setState((s) => (s === "connecting" ? "live" : s));
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
      history.close();
    };
  }, [remote, session.id, attempt, online]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (state !== "live" || shown) return;
    // Fitted: the computer has answered with the screen of the size asked for.
    if ((fitted && fittedTo.current && fittedTo.current !== "pending") || changed) return setShown(true);
    const timer = setTimeout(() => setShown(true), 900);
    return () => clearTimeout(timer);
  }, [state, fitted, dims, changed, shown]);

  const send = (s: string) => conn.current?.send({ kind: { case: "input", value: enc.encode(s) } });
  // Any key leaves the reader first, then goes to the session as before.
  const press = (s: string) => {
    if (history.phase !== "idle") leaveReading();
    send(s);
  };
  const openKeyboard = () => {
    if (history.phase !== "idle") leaveReading();
    term.current?.focus();
  };

  const fit = () => {
    if (document.hidden) return;
    const el = box.current;
    if (!el) return;
    // At a readable size, how much fits here.
    const fs = 12;
    const cols = Math.floor(((viewport.current?.width ?? el.clientWidth) - 12) / (fs * charRatio()));
    const rows = Math.floor(((viewport.current?.height ?? el.clientHeight) - 12) / (fs * 1.2));
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
    if (document.hidden || state !== "live" || !linked || autoFitted.current) return;
    autoFitted.current = true;
    if (!released.current) fit();
  }, [state, linked]); // eslint-disable-line react-hooks/exhaustive-deps

  const live = state === "live";
  const reading = history.phase === "reading";
  return (
    <div className={"termview" + (compact ? " compact" : "")}>
      {/* The size capsule, dark glass over the terminal in either theme. */}
      <div className="ttool">
        <div className="tchip glass">
          <span>
            {reading ? i18n.t("terminal.readonly") : !live ? (state === "connecting" ? i18n.t("terminal.connecting") : i18n.t("terminal.disconnected")) : fitted ? i18n.t("terminal.fitted_size", { cols: dims!.cols, rows: dims!.rows }) : changed ? i18n.t("terminal.remote_dimensions", { cols: dims!.cols, rows: dims!.rows }) : i18n.t("terminal.computer_dimensions", { cols: dims!.cols, rows: dims!.rows })}
          </span>
          {live && !reading && <button onClick={toggleFit}>{fitted ? i18n.t("terminal.restore") : <><Maximize2 /> {wide ? i18n.t("terminal.fit_short") : i18n.t("terminal.fit")}</>}</button>}
        </div>
      </div>
      <div className={"term" + (reading ? " reading" : "")} ref={box} onClick={() => !reading && term.current?.focus()}>
        <div className={"tpan" + (shown || state === "closed" ? "" : " unfit")} ref={pan}>
          <div ref={host} className={state === "closed" ? "stale" : ""} />
        </div>
        {history.phase === "loading" && <div className="thint">{history.error ? <>{i18n.t("terminal.read_failed")}<button onClick={() => beginRef.current(0)}>{i18n.t("common.retry")}</button></> : i18n.t("terminal.reading")}</div>}
        {reading && reader && <TerminalReader history={history} load={() => loadHistory()} online={live} font={reader.font} height={reader.height} offset={Math.max(reader.offset, reader.height * 3)} left={reader.left} grid={reader.grid} onLeave={leaveReading} />}
        {reading && <BackToLive fresh={newOutput} onLeave={leaveReading} />}
        {changed && state === "live" && !reading && (
          <div className="tstate glass">
            <b>{i18n.t("terminal.size_changed")}</b>
            <p>{i18n.t("terminal.size_note")}</p>
            <div className="btns">
              <button className="btn" onClick={() => setChanged(false)}>
                {i18n.t("common.got_it")}</button>
            </div>
          </div>
        )}
        {state === "closed" && (
          <div className="tstate glass">
            <b>{i18n.t("terminal.closed")}</b>
            <p>{running ? i18n.t("terminal.closed_running_note") : i18n.t("terminal.closed_ended_note")}</p>
            <div className="btns">
              <button className="btn pri" onClick={() => setAttempt((a) => a + 1)}>
                {i18n.t("terminal.reconnect")}</button>
            </div>
            {reason &&
              (details ? (
                <span className="det mono">{reason}</span>
              ) : (
                <button className="det" onClick={() => setDetails(true)}>
                  {i18n.t("terminal.view_details")}</button>
              ))}
          </div>
        )}
      </div>
      {/* A wide window with a mouse has a keyboard: the bar is for touch. */}
      {!compact && !(wide && fine) && (
        <div className={"keys glass" + (live ? "" : " off")}>
          <div className="keyrow">
            {keys().map(([label, seq, name]) => (
              <button key={name} onClick={() => press(seq)} disabled={!live} aria-label={name}>
                {label}
              </button>
            ))}
          </div>
          <div className="keyrow">
            {keys2.map(([label, seq, name]) => (
              <button key={name} onClick={() => press(seq)} disabled={!live} aria-label={name}>
                {label}
              </button>
            ))}
            <button className="kb" onClick={openKeyboard} disabled={!live} aria-label={i18n.t("terminal.keyboard")}>
              <Keyboard /> {i18n.t("terminal.keyboard")}</button>
          </div>
        </div>
      )}
    </div>
  );
}
