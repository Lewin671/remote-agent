// Small building blocks shared by the views.

import { useEffect, useState, useSyncExternalStore, type ReactNode } from "react";
import { avatarClass, type Tone } from "./model";
import type { Remote } from "./remote";

export function useRemote(remote: Remote) {
  useSyncExternalStore(remote.subscribe, remote.snapshot);
}

/** Re-renders every ms, for countdowns and relative times. */
export function useNow(ms: number, enabled = true) {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    let timer: ReturnType<typeof setInterval> | undefined;
    const followVisibility = () => {
      clearInterval(timer);
      timer = undefined;
      if (document.hidden || !enabled) return;
      setNow(Date.now());
      timer = setInterval(() => setNow(Date.now()), ms);
    };
    document.addEventListener("visibilitychange", followVisibility);
    followVisibility();
    return () => { clearInterval(timer); document.removeEventListener("visibilitychange", followVisibility); };
  }, [ms, enabled]);
  return now;
}

export function Dot({ tone }: { tone: Tone }) {
  return <span className={"dot " + tone} />;
}

export function Avatar({ name, size = "" }: { name: string; size?: "" | "sm" | "xs" | "lg" }) {
  const letter = [...name].find((c) => /\p{L}|\p{N}/u.test(c)) ?? "?";
  return <div className={`av ${size} ${avatarClass(name)}`}>{letter.toLowerCase()}</div>;
}

export function Sheet({ onClose, children, className = "" }: { onClose: () => void; children: ReactNode; className?: string }) {
  useEscape(onClose);
  return (
    <div className="overlay">
      <div className="dim" onClick={onClose} />
      <div className={"sheet " + className} role="dialog" aria-modal="true">
        <div className="grab" />
        {children}
      </div>
    </div>
  );
}

function useEscape(fn: () => void) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && fn();
    addEventListener("keydown", onKey);
    return () => removeEventListener("keydown", onKey);
  }, [fn]);
}

export function Alert({
  title,
  text,
  ok,
  danger,
  titleRed,
  onOk,
  onCancel,
}: {
  title: string;
  text: string;
  ok: string;
  danger?: boolean;
  titleRed?: boolean;
  onOk: () => void;
  onCancel: () => void;
}) {
  useEscape(onCancel);
  return (
    <div className="overlay">
      <div className="dim" onClick={onCancel} />
      <div className="alert" role="alertdialog" aria-modal="true">
        <h5 className={titleRed ? "red" : ""}>{title}</h5>
        <p>{text}</p>
        <div className="btns">
          <button className="btn" onClick={onCancel}>
            取消
          </button>
          <button className={"btn " + (danger ? "danger" : "pri")} onClick={onOk}>
            {ok}
          </button>
        </div>
      </div>
    </div>
  );
}

export interface Action {
  label: string;
  red?: boolean;
  onClick: () => void;
}

/** An iOS-style action sheet: an info line, actions, and 取消. */
export function ActionSheet({ info, actions, onClose }: { info?: ReactNode; actions: Action[]; onClose: () => void }) {
  useEscape(onClose);
  return (
    <div className="overlay">
      <div className="dim" onClick={onClose} />
      <div className="actsheet" role="menu">
        <div className="grp2">
          {info && <div className="info">{info}</div>}
          {actions.map((a) => (
            <button
              key={a.label}
              className={"ai2" + (a.red ? " red" : "")}
              onClick={() => {
                onClose();
                a.onClick();
              }}
            >
              {a.label}
            </button>
          ))}
        </div>
        <div className="grp2">
          <button className="ai2 cancel" onClick={onClose}>
            取消
          </button>
        </div>
      </div>
    </div>
  );
}

export function Banner({ kind = "", children, action, onAction }: { kind?: "" | "warn" | "err"; children: ReactNode; action?: string; onAction?: () => void }) {
  return (
    <div className={"banner " + kind}>
      <span>{children}</span>
      {action && <button onClick={onAction}>{action}</button>}
    </div>
  );
}

/** The link to the server, apart from any session's state: shown only
 * while it is not up. */
export function ConnBar({ remote, className = "connbar" }: { remote: Remote; className?: string }) {
  const [details, setDetails] = useState(false);
  if (remote.conn === "online") return null;
  // Failing again and again before ever connecting: say why.
  if (!remote.everOnline && remote.failures >= 2 && remote.connError)
    return (
      <div className={className + " err"}>
        <span>✕ 连不上服务器{details ? "：" + remote.connError : ""}</span>
        {!details && <button onClick={() => setDetails(true)}>详情</button>}
      </div>
    );
  return <div className={className}>◌ {remote.everOnline ? "连接断了，正在重连 · 内容可能不是最新的" : "连接中…"}</div>;
}

export function Skeleton({ lines = 3 }: { lines?: number }) {
  const widths = ["40%", "100%", "80%", "70%", "90%", "40%"];
  return (
    <div className="skels" aria-label="加载中">
      {Array.from({ length: lines }, (_, i) => (
        <div key={i} className="skel" style={{ width: widths[i % widths.length] }} />
      ))}
    </div>
  );
}

/** A navigation bar: back, a title with a second line, and a right
 * button, each at least 44×44. */
export function NavBar({ onBack, title, sub, right, className = "" }: { onBack?: () => void; title?: ReactNode; sub?: ReactNode; right?: ReactNode; className?: string }) {
  return (
    <header className={"navbar " + className}>
      {onBack ? (
        <button className="back" onClick={onBack} aria-label="返回">
          ‹
        </button>
      ) : (
        <span className="back" />
      )}
      <span className="title">
        {title && <b>{title}</b>}
        {sub && <span>{sub}</span>}
      </span>
      {right ?? <span className="more" />}
    </header>
  );
}

/** Background pages stop live connections; visibility restoration takes a fresh snapshot. */
export function usePageVisible() {
  return useSyncExternalStore(
    (notify) => { document.addEventListener("visibilitychange", notify); return () => document.removeEventListener("visibilitychange", notify); },
    () => !document.hidden,
  );
}
