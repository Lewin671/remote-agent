// The session composer (design-system §6 输入框): a glass capsule floating
// at the bottom, ＋ on the left, the draft growing from one line to about
// six, and on the right one ink button that sends, or interrupts a running
// turn while the draft is empty. Chosen images and files sit above the line.

import { useLayoutEffect, useRef, useState, type HTMLAttributes, type ReactNode, type RefObject } from "react";
import type { Tray } from "../attach";
import type { Derived } from "../model";
import { SendMode } from "../gen/remoteagent/v1/event_pb";
import type { Remote, SessionView } from "../remote";
import { Alert, sendKeys, usePointerFine } from "../ui";
import { ArrowUp, Square } from "../icons";
import { easeBetween, easing, heightNow, stopEasing, Swap } from "../motion";

/** The draft grows to about six lines (17 / 1.4, and its padding), then
 * scrolls inside. */
const DRAFT_MAX = 150;

/** Whether the browser sizes this textarea by its text itself. */
function sizesItself(el: HTMLElement): boolean {
  return typeof CSS !== "undefined" && !!CSS.supports?.("field-sizing", "content") && getComputedStyle(el).getPropertyValue("field-sizing") === "content";
}

/** A textarea that grows with its text up to max pixels. field-sizing
 * does this where the stylesheet asks for it and the browser supports it
 * (the height is then only eased here, from the size the browser reports once
 * it has laid the new text out); the height set here covers the rest, with
 * a single measure of the text per change. */
export function useAutoGrow(ref: RefObject<HTMLTextAreaElement | null>, text: string, max = DRAFT_MAX) {
  const state = useRef<{ native: boolean; height: number }>(undefined);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const s = (state.current ??= { native: sizesItself(el), height: 0 });
    if (!s.native || typeof ResizeObserver === "undefined") return;
    s.height = el.offsetHeight;
    // Growing a line, or falling back once the message is sent, eases.
    const ro = new ResizeObserver((entries) => {
      const to = entries[entries.length - 1].borderBoxSize?.[0]?.blockSize ?? el.offsetHeight;
      const from = s.height;
      s.height = to;
      if (from > 0) easeBetween(el, from, to);
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, [ref]);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const s = (state.current ??= { native: sizesItself(el), height: 0 });
    if (s.native) return;
    // What is on screen: the height set last, or where an ease has got to.
    const from = easing(el) ? heightNow(el) : s.height;
    stopEasing(el);
    el.style.height = "auto";
    const to = Math.min(max, el.scrollHeight);
    el.style.height = `${to}px`;
    s.height = to;
    // Growing a line, or falling back once the message is sent, eases;
    // the first fit, with a saved draft, is where it starts.
    if (from > 0) easeBetween(el, from, to);
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

export function Composer({
  ref,
  remote,
  session,
  d,
  onSend,
  onInterrupt,
  draft,
  onDraft,
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
  onSend: (text: string, mode: SendMode) => void;
  onInterrupt: () => void;
  draft: string;
  onDraft: (text: string) => void;
  // Images waiting to go with the message, the ＋ that adds them,
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
  const fine = usePointerFine();
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
    tray?.refreshExpired();
    if (!canSend || (images && !tray!.ready())) return;
    // Slash commands typed as text go to the model; menus they would open
    // are not visible here.
    if (t.startsWith("/") && !force && !images) return setSlash(sendMode);
    setText("");
    onSend(t, sendMode);
    setSendMenu(false);
  };
  const i = session.info;
  const keys = sendKeys(fine);
  // Not by whether a tool is running: that changes every few seconds of a turn, and the field would blink.
  const placeholder = d.questions.size > 0 ? "排队发送，回答后继续" : i?.planOpen ? "补充你对计划的要求…" : keys ? `消息 · ${keys} 发送` : "消息";
  const canQueue = d.running;
  const stopping = d.running && !t && !images;
  const send = stopping ? (
    <button className="snd stop" disabled={!online} onMouseDown={(e) => e.preventDefault()} onClick={onInterrupt} aria-label="中断本轮">
      <Square />
    </button>
  ) : (
    <div className="send-wrap">
      <button
        className={"snd" + (canSend ? "" : " off")}
        disabled={!canSend}
        onMouseDown={(e) => e.preventDefault()}
        onPointerDown={() => { if (canQueue) { held.current = false; hold.current = setTimeout(() => { held.current = true; setSendMenu(true); }, 450); } }}
        onPointerUp={() => clearTimeout(hold.current)}
        onPointerLeave={() => clearTimeout(hold.current)}
        onContextMenu={(e) => { if (canQueue) { e.preventDefault(); setSendMenu(true); held.current = true; } }}
        onClick={() => { if (held.current) { held.current = false; return; } submit(); }}
        aria-label="发送"
      >
        <ArrowUp />
      </button>
      {sendMenu && (
        <div className="send-menu">
          <button onClick={() => submit()}><b>现在插入这一轮</b><small>点 ↑ 就是这个</small></button>
          <button onClick={() => submit(false, SendMode.QUEUE)}><b>这一轮结束后再发</b><small>先排队，结束后按顺序发出</small></button>
        </div>
      )}
    </div>
  );
  return (
    <div className={"cbar glass" + (dropping ? " drop" : "")} {...cardProps}>
      {trayView}
      <div className="cline">
        {attach}
        <textarea
          ref={input}
          className={"draft" + (t ? " typed" : "")}
          value={text}
          rows={1}
          placeholder={placeholder}
          onChange={(e) => setText(e.target.value)}
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
        {/* ↑ and ■ swap by crossfade; the two are the same ink circle. */}
        <Swap k={stopping ? "stop" : "send"} className="mo-send" hold>
          {send}
        </Swap>
      </div>
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
