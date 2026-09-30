// The session composer: one growing input with images and settings on the
// left; its right button sends, or interrupts an empty running turn.

import { useEffect, useLayoutEffect, useRef, useState, type HTMLAttributes, type ReactNode, type RefObject } from "react";
import type { Tray } from "../attach";
import { agentOf, CONTEXT_WARN, contextPercent, modeOf, type Derived } from "../model";
import { SendMode } from "../gen/remoteagent/v1/event_pb";
import type { Remote, SessionView } from "../remote";
import { Alert } from "../ui";

/** The draft grows to about six lines, then scrolls inside. */
const DRAFT_MAX = 132;

/** A textarea that grows with its text up to max pixels. field-sizing
 * does this where supported; the height set here covers the rest. */
export function useAutoGrow(ref: RefObject<HTMLTextAreaElement | null>, text: string, max = DRAFT_MAX) {
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(max, el.scrollHeight)}px`;
  }, [ref, text, max]);
}

/** The request ID of the first message of a session started here: the
 * daemon queues it before anything else can reach the session, so it is
 * the session's first input, whatever other clients or this page sent
 * after it. */
export function promptIdOf(remote: Remote, sid: string, d: Derived): string | undefined {
  if (!remote.prompts.has(sid)) return undefined;
  return d.inputs.keys().next().value;
}

/** The ＋ on the input's left: a small menu of what the input can do
 * besides text. A high context shows as an orange dot beside it. */
export function ComposeMenu({ children, high, disabled, label }: { children: ReactNode; high?: boolean; disabled?: boolean; label: string }) {
  const [open, setOpen] = useState(false);
  const box = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const away = (e: PointerEvent) => {
      if (!box.current?.contains(e.target as Node)) setOpen(false);
    };
    const esc = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    document.addEventListener("pointerdown", away);
    document.addEventListener("keydown", esc);
    return () => {
      document.removeEventListener("pointerdown", away);
      document.removeEventListener("keydown", esc);
    };
  }, [open]);
  const shown = open && !disabled;
  return (
    <div className="compose-menu" ref={box}>
      <button
        className={"cm-plus" + (high ? " context-high" : "")}
        aria-label={label}
        aria-expanded={shown}
        aria-haspopup="menu"
        disabled={disabled}
        onMouseDown={(e) => e.preventDefault()}
        onClick={() => setOpen((o) => !o)}
      >
        ＋
      </button>
      {/* Kept mounted while closed: the image picker's input lives in it. */}
      <div className="compose-pop" role="menu" hidden={!shown} onClick={(e) => (e.target as HTMLElement).closest("button") && setOpen(false)}>
        {children}
      </div>
    </div>
  );
}

export function Composer({
  ref,
  remote,
  session,
  d,
  wide,
  onFocus,
  onSend,
  onInterrupt,
  draft,
  onDraft,
  onMode,
  onModel,
  tray,
  trayView,
  attach,
  cardProps,
  dropping,
}: {
  ref: RefObject<HTMLTextAreaElement | null>;
  remote: Remote;
  session: SessionView;
  d: Derived;
  wide: boolean;
  onFocus: (typing: boolean) => void;
  onSend: (text: string, mode: SendMode) => void;
  onInterrupt: () => void;
  draft: string;
  onDraft: (text: string) => void;
  onMode: () => void;
  onModel: () => void;
  // Images waiting to go with the message, the menu row that adds them,
  // and dropping or pasting them on the card.
  tray?: Tray;
  trayView?: ReactNode;
  attach?: ReactNode;
  cardProps?: HTMLAttributes<HTMLDivElement>;
  dropping?: boolean;
}) {
  // The draft is the session's, not this input's: it survives opening an
  // approval, plan, queue or tool page and coming back.
  const [text, setTextState] = useState(draft);
  const setText = (v: string) => {
    setTextState(v);
    onDraft(v);
  };
  const [slash, setSlash] = useState<SendMode>();
  const [sendMenu, setSendMenu] = useState(() => remote.demoFlag("sendmenu"));
  const hold = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const held = useRef(false);
  const input = ref;
  useAutoGrow(input, text);
  const online = remote.conn === "online";
  const t = text.trim();
  // While the server is away, nothing is queued behind the user's back:
  // the text stays here until they send it themselves.
  // With images, every one must be on the machine first; the text may
  // then be empty.
  const images = tray?.items.length ?? 0;
  const canSend = online && (images ? tray!.ready() : !!t);
  const submit = (force = false, sendMode = SendMode.STEER) => {
    if (!canSend) return;
    // Slash commands typed as text go to the model; menus they would open
    // are not visible here.
    if (t.startsWith("/") && !force && !images) return setSlash(sendMode);
    setText("");
    onSend(t, sendMode);
    setSendMenu(false);
  };
  const mode = modeOf(session);
  const pct = contextPercent(session);
  const i = session.info;
  const model = i?.model ? [remote.modelName(session.machineId, agentOf(session), i.model), i.effort].filter(Boolean).join(" · ") : "";
  const placeholder = d.questions.size > 0 ? "排队发送，回答后继续" : d.tools.length > 0 ? "消息…" : i?.planOpen ? "补充你对计划的要求…" : wide ? "消息 · ⌘↵ 发送" : "消息";
  const high = pct !== undefined && pct > CONTEXT_WARN;
  // Full access is the one setting always in view (client-v7).
  const full = mode?.text === "完全放行";
  const canQueue = d.running && !!remote.machines.get(session.machineId)?.meta?.capabilities?.sendModes;
  return (
    <div className={"compose-card" + (dropping ? " drop" : "")} {...cardProps}>
      {trayView}
      <ComposeMenu label={high ? `附件与会话设置，上下文 ${pct}%` : "附件与会话设置"} high={high} disabled={!online}>
        {attach}
        {mode && (
          <button className={full ? "red" : ""} onClick={onMode} aria-label={`审批模式：${mode.text}`}>
            模式：{mode.text} ▾
          </button>
        )}
        <button className={model ? "" : "nav"} onClick={onModel}>
          {model ? `模型：${model} ▾` : "会话设置 ›"}
        </button>
        {pct !== undefined && (
          <button className={high ? "warn-text" : ""} onClick={onModel} aria-label={`上下文 ${pct}%`}>
            上下文：{pct}%
          </button>
        )}
      </ComposeMenu>
      <textarea
        ref={input}
        className={"draft" + (t ? " typed" : "")}
        value={text}
        rows={1}
        placeholder={placeholder}
        onChange={(e) => setText(e.target.value)}
        onFocus={() => onFocus(true)}
        onBlur={() => onFocus(false)}
        onKeyDown={(e) => {
          // Enter is a new line; ⌘/Ctrl+Enter sends, but never while an
          // input method is still composing.
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && !e.nativeEvent.isComposing) {
            e.preventDefault();
            submit();
          } else if (e.key === "Enter" && e.altKey && canQueue && !e.nativeEvent.isComposing) {
            e.preventDefault();
            submit(false, SendMode.QUEUE);
          }
        }}
        aria-label="消息"
      />
      {d.running && !t && !images ? <button className="go44 stop" disabled={!online} onMouseDown={(e) => e.preventDefault()} onClick={onInterrupt} aria-label="中断本轮">■</button> : <div className="send-wrap"><button className={"go44" + (canSend ? "" : " off")} disabled={!canSend} onMouseDown={(e) => e.preventDefault()} onPointerDown={() => { if (canQueue) { held.current = false; hold.current = setTimeout(() => { held.current = true; setSendMenu(true); }, 450); } }} onPointerUp={() => clearTimeout(hold.current)} onPointerLeave={() => clearTimeout(hold.current)} onContextMenu={(e) => { if (canQueue) { e.preventDefault(); setSendMenu(true); held.current = true; } }} onClick={() => { if (held.current) { held.current = false; return; } submit(); }} aria-label="发送">↑</button>{sendMenu && <div className="send-menu"><button onClick={() => submit()}><b>现在插入这一轮</b><small>点 ↑ 就是这个</small></button><button onClick={() => submit(false, SendMode.QUEUE)}><b>这一轮结束后再发</b><small>先排队，结束后按顺序发出</small></button></div>}</div>}
      {slash !== undefined && (
        <Alert
          title="作为普通消息发送？"
          text="以 / 开头的内容会作为普通消息发给模型，而不是执行斜杠命令。"
          ok="发送"
          onCancel={() => setSlash(undefined)}
          onOk={() => {
            const choice = slash;
            setSlash(undefined);
            submit(true, choice);
          }}
        />
      )}
    </div>
  );
}
