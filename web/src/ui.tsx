// Small building blocks shared by the views.

import { useEffect, useState, useSyncExternalStore, type ReactNode } from "react";
import { avatarClass, type Tone } from "./model";
import type { Remote } from "./remote";
import { ChevronDown, ChevronLeft, ChevronUp, CloudOff, LoaderCircle, WifiOff, X } from "./icons";

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

export function useEscape(fn: () => void) {
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
  onOk,
  onCancel,
}: {
  title: string;
  text: string;
  ok: string;
  danger?: boolean;
  onOk: () => void;
  onCancel: () => void;
}) {
  useEscape(onCancel);
  return (
    <div className="overlay">
      <div className="dim" onClick={onCancel} />
      <div className="alert" role="alertdialog" aria-modal="true">
        {/* Danger shows on the button, never the title (design-system §2). */}
        <h5>{title}</h5>
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
  warn?: boolean;
  onClick: () => void;
  /** Grayed out and not clickable. */
  disabled?: boolean;
  /** A line under the label: why it is disabled, or (red) why it failed. */
  note?: string;
  noteRed?: boolean;
  /** In progress: the label is what is happening. */
  busy?: boolean;
  /** Clicking leaves the sheet open, for the action to report in it. */
  keep?: boolean;
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
              className={"ai2" + (a.red ? " red" : a.warn ? " warn" : "") + (a.note ? " two" : "") + (a.disabled ? " dis" : "") + (a.busy ? " busy" : "")}
              disabled={a.disabled || a.busy}
              onClick={() => {
                if (!a.keep) onClose();
                a.onClick();
              }}
            >
              {a.busy && <LoaderCircle />}
              {a.note ? <span>{a.label}</span> : a.label}
              {a.note && <small className={a.noteRed ? "err" : ""}>{a.note}</small>}
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
      {action && (
        <button onClick={onAction} disabled={!onAction}>
          {action}
        </button>
      )}
    </div>
  );
}

/** The way to the server, apart from any session's state: shown only
 * while it is not up. It names the nearest stretch that is down
 * (client-v30 「断线」): this device's network, or the server. */
export function ConnBar({ remote, className = "connbar" }: { remote: Remote; className?: string }) {
  const [details, setDetails] = useState(false);
  const link = remote.link;
  if (link === "online") return null;
  // Nothing kept from before to show: the bar says what the connection is for.
  const kept = remote.listed || remote.sessions.size > 0;
  if (link === "no-network") return <div className={className}><WifiOff /> 本机网络不通 · {kept ? "显示的是上次的内容" : "联网后才能读取会话"}</div>;
  if (link === "no-server")
    return (
      <div className={className + (kept ? "" : " err")}>
        {/* A browser only knows of no network at all: one that leads nowhere looks the same as a server that is down. */}
        <span>{kept ? <CloudOff /> : <X />} 连不上服务器{details ? `：${remote.connError || "没有回应"}。也可能是这台设备的网络不通，会一直自动重试` : " · 正在重试"}</span>
        {details ? <button onClick={() => remote.reconnectNow(true)}>重试</button> : <button onClick={() => setDetails(true)}>详情</button>}
      </div>
    );
  return <div className={className}><LoaderCircle /> {remote.everOnline ? "正在重连…" : "连接中…"}</div>;
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
          <ChevronLeft />
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

/** The arrow of something that opens in place: down, or up once open
 * (design-system §5). */
export function Exp({ up }: { up?: boolean }) {
  return up ? <ChevronUp className="exp" /> : <ChevronDown className="exp" />;
}

/** What a command does to the route when it has ended, held to the page that
 * sent it: the command outlives its page, so by then the user may be on
 * another one, which is left as it is. Made when the command is sent. */
export function fromHere<A extends unknown[]>(then: (...args: A) => void): (...args: A) => void {
  const here = location.hash;
  return (...args) => {
    if (location.hash === here) then(...args);
  };
}
