// Small building blocks shared by the views.

import { useEffect, useRef, useState, useSyncExternalStore, type ReactNode, type RefObject } from "react";
import { useExit, useKnob, useMarkSwap } from "./motion";
import type { Tone } from "./model";
import type { Remote } from "./remote";
import { ChevronDown, ChevronLeft, ChevronRight, ChevronUp, CloudOff, LoaderCircle, WifiOff, X } from "./icons";

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

/** What a state graphic says (design-system §2): a session's tone, or a
 * computer that is online, or a result not read yet. */
export type Mark = Tone | "online" | "unread";

/** A state graphic: the drawn shape at the head of a row, never an icon
 * and never alone, as the state word beside it says the same. Running is
 * an ink ring that turns (still when the system asks for less motion),
 * idle a hollow gray ring, waiting an orange dot, not connected a faint
 * dot, unread a blue dot, online a green one. */
export function StateMark({ kind }: { kind: Mark }) {
  const ref = useRef<HTMLElement>(null);
  // A state that changes (turning, then a dot, then a ring) fades into the next.
  useMarkSwap(ref, kind);
  return <i ref={ref} className={"mark " + kind} aria-hidden />;
}

/** A project's avatar: its name's first letter on a gray circle, the same
 * for every project; 全 for all of them. */
export function Avatar({ name, size = "", all }: { name: string; size?: "" | "sm" | "xs" | "lg"; all?: boolean }) {
  const letter = all ? "全" : ([...name].find((c) => /\p{L}|\p{N}/u.test(c)) ?? "?").toLowerCase();
  return <span className={"av" + (size ? " " + size : "")} aria-hidden>{letter}</span>;
}

/** A round glass button floating over the content (back, ⋯, the gear):
 * 44×44, named for screen readers. */
export function GlassButton({ label, onClick, children, disabled, className = "" }: { label: string; onClick: () => void; children: ReactNode; disabled?: boolean; className?: string }) {
  return (
    <button className={"gbtn glass " + className} aria-label={label} title={label} disabled={disabled} onClick={onClick}>
      {children}
    </button>
  );
}

/** A bottom sheet: gray, with a grab handle that closes it as the dim does.
 * A title, when given, is centered in a head with at most a 取消 or 完成 on
 * the right, only where something is to be confirmed, and a round back
 * button on the left a level down. On a wide window it is centered. */
export function Sheet({ onClose, children, className = "", title, action, back }: { onClose: () => void; children: ReactNode; className?: string; title?: ReactNode; action?: { label: string; onClick: () => void; disabled?: boolean }; back?: () => void }) {
  useEscape(onClose);
  const root = useRef<HTMLDivElement>(null);
  useOverlayExit(root);
  return (
    <div className="overlay" ref={root}>
      <div className="dim" onClick={onClose} />
      <div className={"sheet " + className} role="dialog" aria-modal="true">
        <button className="grab" aria-label="关闭" onClick={onClose} />
        {title !== undefined && (
          <div className="sheet-head">
            {back ? (
              <span className="side-act">
                <button className="fbtn" aria-label="返回" onClick={back}>
                  <ChevronLeft />
                </button>
              </span>
            ) : (
              <span className="side-act" />
            )}
            <b>{title}</b>
            {action ? <button className="side-act" disabled={action.disabled} onClick={action.onClick}>{action.label}</button> : <span className="side-act" />}
          </div>
        )}
        {children}
      </div>
    </div>
  );
}

/** A sheet or a dialog that goes: its dim fades and the panel drops or fades
 * (the stylesheet's .mo-leave), whoever unmounts it. */
export function useOverlayExit(ref: RefObject<HTMLElement | null>) {
  useExit(ref, { tier: "slow", leave: "mo-leave" });
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
  const root = useRef<HTMLDivElement>(null);
  useOverlayExit(root);
  return (
    <div className="overlay" ref={root}>
      <div className="dim" onClick={onCancel} />
      <div className="dialog" role="alertdialog" aria-modal="true">
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
  /** What it is now, at the row's end with a ›: a setting that opens
   * where it is changed (模型 …… GPT-6 ›). */
  value?: string;
  /** The value in red (完全放行), or orange (a high context). */
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
  /** Which rounded group the row is in: rows with the same number in a
   * row share one group, as 设置项 / 操作 / 结束会话. */
  group?: number;
}

/** A ⋯ menu: a sheet with what it is about in bold at the top, then the
 * actions as left-aligned rows in rounded groups, drawn as the project
 * picker is (design-system §6 弹层与对话框). A row with a value reads
 * 「名称 …… 值 ›」; red marks an action that stops or ends something.
 * `pop` (a wide window) shows the same menu by the ⋯ at the top right of
 * the nearest positioned area instead of as a sheet. */
export function ActionSheet({ info, actions, onClose, pop }: { info?: ReactNode; actions: Action[]; onClose: () => void; pop?: boolean }) {
  const groups: Action[][] = [];
  actions.forEach((a, i) => {
    if (i === 0 || (a.group ?? 0) !== (actions[i - 1].group ?? 0)) groups.push([]);
    groups[groups.length - 1].push(a);
  });
  const content = (
    <>
      {info && <div className="menu-info">{info}</div>}
      {groups.map((group, g) => (
      <div className="list" role="menu" key={g}>
        {group.map((a) => (
          <button
            key={a.label}
            role="menuitem"
            className={"mi" + (a.red && a.value === undefined ? " danger" : "") + (a.disabled ? " dis" : "") + (a.busy ? " busy" : "")}
            disabled={a.disabled || a.busy}
            onClick={() => {
              if (!a.keep) onClose();
              a.onClick();
            }}
          >
            {a.busy && <LoaderCircle />}
            <div className="mt">
              {a.label}
              {a.note && <div className={"md" + (a.noteRed ? " err" : "")}>{a.note}</div>}
            </div>
            {a.value !== undefined && (
              <>
                <span className={"mv" + (a.red ? " red" : a.warn ? " warn-text" : "")}>{a.value}</span>
                <ChevronRight className="chev" />
              </>
            )}
          </button>
        ))}
      </div>
      ))}
    </>
  );
  if (pop) return <PopMenu onClose={onClose}>{content}</PopMenu>;
  return (
    <Sheet onClose={onClose} className="menu">
      {content}
    </Sheet>
  );
}

/** A menu by the button that opened it, on a wide window: what a phone
 * shows as a bottom sheet. A click outside or Esc closes it. */
function PopMenu({ onClose, children }: { onClose: () => void; children: ReactNode }) {
  useEscape(onClose);
  const root = useRef<HTMLDivElement>(null);
  useExit(root, { leave: "mo-leave" });
  return (
    <>
      <div className="pop-back" onClick={onClose} />
      <div className="pop menu menu-pop" role="dialog" aria-modal="true" ref={root}>
        {children}
      </div>
    </>
  );
}

/** A segmented control: one setting's values, or a view, side by side in
 * a gray capsule, the picked one on a light knob (never ink). */
export function Segmented<T extends string>({
  options,
  value,
  onChange,
  label,
  className = "",
}: {
  options: { value: T; label: ReactNode; disabled?: boolean; aria?: string }[];
  value: T | undefined;
  onChange: (v: T) => void;
  label: string;
  className?: string;
}) {
  const ref = useRef<HTMLDivElement>(null);
  useKnob(ref, value);
  return (
    <div className={"seg " + className} role="group" aria-label={label} ref={ref}>
      <i className="mo-knob" aria-hidden />
      {options.map((o) => (
        <button key={o.value} className={o.value === value ? "on" : ""} aria-pressed={o.value === value} aria-label={o.aria} disabled={o.disabled} onClick={() => o.value !== value && onChange(o.value)}>
          {o.label}
        </button>
      ))}
    </div>
  );
}

/** A page's banner: gray, a line of what is the matter and at most one
 * text action. "err" writes it in red; "warn" is for what needs the user
 * (orange-soft under orange-ink). */
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
  if (link === "waiting" && !remote.showWaiting) return null;
  if (link === "no-network") return <div className={className}><WifiOff /><span>本机网络不通 · {kept ? "显示的是上次的内容" : "联网后才能读取会话"}</span></div>;
  if (link === "no-server")
    return (
      <div className={className + (kept ? "" : " err")}>
        {/* A browser only knows of no network at all: one that leads nowhere looks the same as a server that is down. */}
        {kept ? <CloudOff /> : <X />}
        <span>连不上服务器{details ? `：${remote.connError || "没有回应"}。也可能是这台设备的网络不通，会一直自动重试` : " · 正在重试"}</span>
        {details ? <button onClick={() => remote.reconnectNow(true)}>重试</button> : <button onClick={() => setDetails(true)}>详情</button>}
      </div>
    );
  return <div className={className}><LoaderCircle /><span>{remote.everOnline ? "正在重连…" : "连接中…"}</span></div>;
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

/** The top of a page: a glass back button, a centered title with a second
 * line, and a right button (a glass ⋯ when it has a class of "more"),
 * each at least 44×44, over the page with no bar of its own. */
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

function fineQuery(): MediaQueryList | undefined {
  return typeof matchMedia === "function" ? matchMedia("(pointer: fine)") : undefined;
}

function subscribeFine(notify: () => void) {
  const q = fineQuery();
  q?.addEventListener?.("change", notify);
  return () => q?.removeEventListener?.("change", notify);
}

/** Whether the main pointer is a mouse or trackpad, not a finger: such a
 * device has a keyboard of its own. */
export function usePointerFine(): boolean {
  return useSyncExternalStore(subscribeFine, () => !!fineQuery()?.matches);
}

/** The send shortcut as an input's placeholder writes it: ⌘↵ on Apple
 * devices, Ctrl+Enter elsewhere, and nothing on touch, where there is no
 * keyboard to press it on. */
export function sendKeys(fine: boolean, platform = navigator.platform): string {
  if (!fine) return "";
  return /Mac|iP/.test(platform) ? "⌘↵" : "Ctrl+Enter";
}

/** Whether foreground UI work should run; App manages the connection lifetime. */
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

export function Switch({
  on,
  disabled,
  onChange,
  label,
}: {
  on: boolean;
  disabled?: boolean;
  onChange: (v: boolean) => void;
  label: string;
}) {
  return (
    <button
      className={"sw" + (on ? "" : " off")}
      disabled={disabled}
      role="switch"
      aria-checked={on}
      aria-label={label}
      onClick={() => onChange(!on)}
    />
  );
}
