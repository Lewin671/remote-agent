// The session's unified input (client-v2 输入区与消息队列): a multi-line
// draft with the mode, model, context and send in one card, and above it a
// status line with what runs, the queue and a fixed interrupt.

import { useLayoutEffect, useState, type HTMLAttributes, type ReactNode, type RefObject } from "react";
import type { Tray } from "../attach";
import { Agent } from "../gen/remoteagent/v1/event_pb";
import { agentOf, CONTEXT_WARN, contextPercent, modeOf, queuedIds, queueReason, toolPeek, type Derived } from "../model";
import type { Remote, SessionView } from "../remote";
import { Alert, Dot, useNow } from "../ui";

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

/** The line above the input: what runs and for how long, the queue and
 * why it waits, and the interrupt, which stays put while typing. */
export function ComposeStatus({
  remote,
  session,
  d,
  onQueue,
  onTool,
  onInterrupt,
}: {
  remote: Remote;
  session: SessionView;
  d: Derived;
  onQueue: () => void;
  onTool: () => void;
  onInterrupt: () => void;
}) {
  const now = useNow(1000);
  const online = remote.conn === "online";
  const lostAt = online ? undefined : (remote.lostAt ?? now);
  const peek = toolPeek(d, now, lostAt);
  // Every queued message, the first one of a session started here too: it
  // can be canceled here like anywhere else.
  const queued = queuedIds(d);
  const queuedImages = queued.reduce((n, id) => n + (d.accepted.get(id)?.imageIds.length ?? remote.outbox(session.id).find((o) => o.sendId === id)?.imageIds?.length ?? 0), 0);
  const running = d.running;
  const reason = queueReason(session, d);
  if (!peek && !running && queued.length === 0) return null;
  // Claude Code takes a message sent while it works into the running turn.
  const claude = agentOf(session) === Agent.CLAUDE;
  return (
    <div className="compose-status">
      {peek ? (
        <button className={"tool-peek" + (peek.off ? " off" : "")} onClick={onTool}>
          <em>{peek.text}</em> ›
        </button>
      ) : (
        running && (
          <>
            <Dot tone="run" />
            <span>{claude ? "加入当前轮" : "运行中"}</span>
          </>
        )
      )}
      {queued.length > 0 && (
        <button className="queue-link" onClick={onQueue}>
          排队 {queued.length}
          {peek ? "" : " 条"}
          {queuedImages > 0 && ` · ${queuedImages} 张图`} ›
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

export function Composer({
  ref,
  remote,
  session,
  d,
  wide,
  slim,
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
  // The keyboard is up on a phone: only the mode and send stay, the model
  // and context fold into 设置.
  slim: boolean;
  onFocus: (typing: boolean) => void;
  onSend: (text: string) => void;
  draft: string;
  onDraft: (text: string) => void;
  onMode: () => void;
  onModel: () => void;
  // Images waiting to go with the message, the button that adds them, and
  // dropping or pasting them on the card.
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
  const tool = d.tools.length > 0;
  const placeholder = d.questions.size > 0 ? "排队发送，回答后继续" : tool ? "继续输入，不会打断当前工具…" : i?.planOpen ? "补充你对计划的要求…" : wide ? "消息 · ⌘↵ 发送" : "消息";
  const high = pct !== undefined && pct > CONTEXT_WARN;
  return (
    <div className={"compose-card" + (slim ? " slim" : "") + (dropping ? " drop" : "")} {...cardProps}>
      {trayView}
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
      <div className="controls">
        {attach}
        {mode && (
          <button className={"ctl mode" + (mode.text === "完全放行" ? " danger" : "")} onMouseDown={(e) => e.preventDefault()} onClick={onMode} disabled={!online} aria-label={`审批模式：${mode.text}`}>
            {mode.text} ▾
          </button>
        )}
        {slim ? (
          <button className="ctl model" onMouseDown={(e) => e.preventDefault()} onClick={onModel} disabled={!online}>
            <em>设置…</em>
            {high && <span className="hot" aria-label={`上下文 ${pct}%`} />}
          </button>
        ) : (
          <>
            <button className="ctl model" onClick={onModel} disabled={!online}>
              <em>{model || "模型读取中"} ▾</em>
            </button>
            {pct !== undefined && (
              <button className={"ctl context" + (high ? " warn" : "")} onClick={onModel} disabled={!online} aria-label={`上下文 ${pct}%`}>
                {pct}%
              </button>
            )}
          </>
        )}
        <button className={"go44" + (canSend ? "" : " off")} disabled={!canSend} onMouseDown={(e) => e.preventDefault()} onClick={() => submit()} aria-label="发送">
          ↑
        </button>
      </div>
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
