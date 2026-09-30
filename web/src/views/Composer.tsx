// The session's input (client-v7 输入区与消息队列): one row that grows
// with the draft, the ＋ menu (images, mode, model, context) on its left and
// send on its right; above it a line with the queue and a fixed interrupt.

import { useEffect, useLayoutEffect, useRef, useState, type HTMLAttributes, type ReactNode, type RefObject } from "react";
import type { Tray } from "../attach";
import { agentOf, CONTEXT_WARN, contextPercent, modeOf, queuedIds, queueReason, type Derived } from "../model";
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

/** The line above the input: the queue and why it waits, and the
 * interrupt, which stays put while typing. What runs is in the chat and the
 * title, not here (client-v7). */
export function ComposeStatus({
  remote,
  session,
  d,
  onQueue,
  onInterrupt,
}: {
  remote: Remote;
  session: SessionView;
  d: Derived;
  onQueue: () => void;
  onInterrupt: () => void;
}) {
  const online = remote.conn === "online";
  // Every queued message, the first one of a session started here too: it
  // can be canceled here like anywhere else.
  const queued = queuedIds(d);
  const queuedImages = queued.reduce((n, id) => n + (d.accepted.get(id)?.imageIds.length ?? remote.outbox(session.id).find((o) => o.sendId === id)?.imageIds?.length ?? 0), 0);
  const running = d.running;
  const reason = queueReason(session, d);
  if (!running && queued.length === 0) return null;
  return (
    <div className="compose-status">
      {queued.length > 0 && (
        <button className="queue-link" onClick={onQueue}>
          排队 {queued.length} 条{queuedImages > 0 && ` · ${queuedImages} 张图`} ›
        </button>
      )}
      {!running && queued.length > 0 && reason && <span className="qreason">{reason}</span>}
      {running && (
        <button className="interrupt" onClick={onInterrupt} disabled={!online} aria-label="中断本轮">
          ■ 中断
        </button>
      )}
    </div>
  );
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
  onSend: (text: string) => void;
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
  const [slash, setSlash] = useState(false);
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
  const submit = (force = false) => {
    if (!canSend) return;
    // Slash commands typed as text go to the model; menus they would open
    // are not visible here.
    if (t.startsWith("/") && !force && !images) return setSlash(true);
    setText("");
    onSend(t);
  };
  const mode = modeOf(session);
  const pct = contextPercent(session);
  const i = session.info;
  const model = i?.model ? [remote.modelName(session.machineId, agentOf(session), i.model), i.effort].filter(Boolean).join(" · ") : "";
  const placeholder = d.questions.size > 0 ? "排队发送，回答后继续" : d.tools.length > 0 ? "消息…" : i?.planOpen ? "补充你对计划的要求…" : wide ? "消息 · ⌘↵ 发送" : "消息";
  const high = pct !== undefined && pct > CONTEXT_WARN;
  // Full access is the one setting always in view (client-v7).
  const full = mode?.text === "完全放行";
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
          }
        }}
        aria-label="消息"
      />
      <button className={"go44" + (canSend ? "" : " off")} disabled={!canSend} onMouseDown={(e) => e.preventDefault()} onClick={() => submit()} aria-label="发送">
        ↑
      </button>
      {full && <div className="access-warning">完全放行</div>}
      {slash && (
        <Alert
          title="作为普通消息发送？"
          text="以 / 开头的内容会作为普通消息发给模型，而不是执行斜杠命令。"
          ok="发送"
          onCancel={() => setSlash(false)}
          onOk={() => {
            setSlash(false);
            submit(true);
          }}
        />
      )}
    </div>
  );
}
