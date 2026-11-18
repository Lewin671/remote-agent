import * as i18n from "../i18n";
// The session composer (design-system §6 输入框): a glass capsule floating
// at the bottom, ＋ on the left, the draft growing from one line to about
// six, and on the right one ink button that sends, or interrupts a running
// turn while the draft is empty. Chosen images and files sit above the line.

import { useEffect, useLayoutEffect, useRef, useState, type HTMLAttributes, type ReactNode, type RefObject } from "react";
import type { Tray } from "../attach";
import { renderText } from "../core/text";
import type { SessionComposerView } from "../gen/remoteagent/core/v1/session_view_pb";
import { SendMode } from "../gen/remoteagent/v1/event_pb";
import { ArrowUp, Square } from "../icons";
import { easeBetween, easing, heightNow, stopEasing, Swap } from "../motion";
import { Alert, sendKeys, usePointerFine } from "../ui";
import { keepComposerFocus, useComposerMenuDismiss } from "./ComposerPicker";

/** The draft grows to about six lines (17 / 1.4, and its padding), then
 * scrolls inside. */
const DRAFT_MAX = 150;

function sizesItself(el: HTMLElement): boolean {
  return typeof CSS !== "undefined" && !!CSS.supports?.("field-sizing", "content") && getComputedStyle(el).getPropertyValue("field-sizing") === "content";
}

export function useAutoGrow(ref: RefObject<HTMLTextAreaElement | null>, text: string, max = DRAFT_MAX) {
  const state = useRef<{ native: boolean; height: number }>(undefined);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const s = (state.current ??= { native: sizesItself(el), height: 0 });
    if (!s.native || typeof ResizeObserver === "undefined") return;
    s.height = el.offsetHeight;
    const ro = new ResizeObserver((entries) => {
      if (easing(el)) return;
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
    const from = easing(el) ? heightNow(el) : s.height;
    stopEasing(el);
    el.style.height = "auto";
    const to = Math.min(max, el.scrollHeight);
    el.style.height = `${to}px`;
    s.height = to;
    if (from > 0) easeBetween(el, from, to);
  }, [ref, text, max]);
}

export function Composer({
  ref,
  composer,
  onSend,
  onInterrupt,
  draft,
  onDraft,
  tray,
  trayView,
  attach,
  cardProps,
  dropping,
  interruptDisabled = false,
  defaultSendMenu = false,
}: {
  ref: RefObject<HTMLTextAreaElement | null>;
  composer?: SessionComposerView;
  onSend: (text: string, mode: SendMode) => void;
  onInterrupt: () => void;
  draft: string;
  onDraft: (text: string) => void;
  tray?: Tray;
  trayView?: ReactNode;
  attach?: ReactNode;
  cardProps?: HTMLAttributes<HTMLDivElement>;
  dropping?: boolean;
  interruptDisabled?: boolean;
  defaultSendMenu?: boolean;
}) {
  const [text, setTextState] = useState(draft);
  useLayoutEffect(() => setTextState(draft), [draft]);
  const setText = (v: string) => {
    setTextState(v);
    onDraft(v);
  };
  const fine = usePointerFine();
  const [slash, setSlash] = useState<SendMode>();
  const [sendMenu, setSendMenu] = useState(defaultSendMenu);
  const hold = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const held = useRef(false);
  const sendWrap = useRef<HTMLDivElement>(null);

  const running = !!composer?.hasActiveTurn;
  const canChooseSendMode = running && !!composer?.supportsQueueMode;
  useComposerMenuDismiss(sendMenu && canChooseSendMode, sendWrap, () => setSendMenu(false));
  useEffect(() => () => clearTimeout(hold.current), []);

  const input = ref;
  useAutoGrow(input, text);

  const online = composer?.enabled ?? false;
  const t = text.trim();
  const images = tray?.items.length ?? 0;
  const canSend = online && (images ? (tray?.ready() ?? false) : !!t);
  const defaultMode = running && !composer?.steerModeDefault ? SendMode.QUEUE : SendMode.STEER;

  const submit = (force = false, sendMode = defaultMode) => {
    const resolvedMode =
      running && composer && !composer.supportsQueueMode ? SendMode.QUEUE : sendMode;
    tray?.refreshExpired();
    if (!canSend || (images && !tray!.ready())) return;
    if (t.startsWith("/") && !force && !images) return setSlash(resolvedMode);
    onSend(t, resolvedMode);
    setText("");
    setSendMenu(false);
  };

  const keys = sendKeys(fine);
  const corePlaceholder = renderText(composer?.placeholder);
  const placeholder =
    corePlaceholder && composer?.placeholder?.key !== "composer.message"
      ? corePlaceholder
      : keys
        ? i18n.t("composer.shortcut", { keys })
        : corePlaceholder || i18n.t("composer.message");

  const stopping = running && !t && !images;
  const stopEnabled = (composer?.interruptVisible ?? online) && !interruptDisabled;

  const send = stopping ? (
    <button
      className="snd stop"
      disabled={!stopEnabled}
      onMouseDown={(e) => e.preventDefault()}
      onClick={onInterrupt}
      aria-label={i18n.t("session.interrupt_turn")}
    >
      <Square />
    </button>
  ) : (
    <div className="send-wrap" ref={sendWrap}>
      <button
        className={"snd" + (canSend ? "" : " off")}
        disabled={!canSend}
        onMouseDown={(e) => e.preventDefault()}
        onPointerDown={(e) => {
          keepComposerFocus(e);
          held.current = false;
          if (canChooseSendMode && !sendMenu) {
            hold.current = setTimeout(() => {
              held.current = true;
              setSendMenu(true);
            }, 450);
          }
        }}
        onPointerUp={() => clearTimeout(hold.current)}
        onPointerLeave={() => clearTimeout(hold.current)}
        onPointerCancel={() => clearTimeout(hold.current)}
        onContextMenu={(e) => {
          if (canChooseSendMode) {
            e.preventDefault();
            setSendMenu(true);
            held.current = true;
          }
        }}
        onClick={() => {
          if (held.current) {
            held.current = false;
            return;
          }
          if (sendMenu && canChooseSendMode) setSendMenu(false);
          else submit();
        }}
        aria-label={i18n.t("composer.send")}
      >
        <ArrowUp />
      </button>
      {sendMenu && canChooseSendMode && (
        <div className="send-menu">
          <button onClick={() => submit(false, SendMode.STEER)}>
            <b>{i18n.t("composer.insert_now")}</b>
            <small>{i18n.t("composer.insert_hint")}</small>
          </button>
          <button onClick={() => submit(false, SendMode.QUEUE)}>
            <b>{i18n.t("composer.after_turn")}</b>
            <small>{i18n.t("composer.queue_hint")}</small>
          </button>
        </div>
      )}
    </div>
  );

  return (
    <div className={"cbar glass" + (dropping ? " drop" : "")} data-drop-label={i18n.t("attachment.release_to_add")} {...cardProps}>
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
            if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && !e.nativeEvent.isComposing) {
              e.preventDefault();
              submit();
            } else if (e.key === "Enter" && e.altKey && running && !e.nativeEvent.isComposing) {
              e.preventDefault();
              submit(false, SendMode.QUEUE);
            }
          }}
          aria-label={i18n.t("composer.message")}
        />
        <Swap k={stopping ? "stop" : "send"} className="mo-send" hold>
          {send}
        </Swap>
      </div>
      {slash !== undefined && (
        <Alert
          title={i18n.t("composer.slash_title")}
          text={i18n.t("composer.slash_note")}
          ok={i18n.t("composer.send")}
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
