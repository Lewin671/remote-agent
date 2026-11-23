import * as i18n from "../i18n";
// The session composer (design-system §6 输入框): optional PendingTray at the
// top of the same card, chosen attachments, auto-growing draft, and up to two
// send actions when running ("做完再发" + ↑, or ■ when empty).

import { useLayoutEffect, useRef, useState, type HTMLAttributes, type ReactNode, type RefObject } from "react";
import type { Tray } from "../attach";
import { renderText } from "../core/text";
import type { SessionComposerView } from "../gen/remoteagent/core/v1/session_view_pb";
import { SendMode } from "../gen/remoteagent/v1/event_pb";
import { ArrowUp, Square } from "../icons";
import { easeBetween, easing, heightNow, stopEasing, Swap } from "../motion";
import { Alert, queueKeys, sendKeys, usePointerFine } from "../ui";
import { keepComposerFocus } from "./ComposerPicker";

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
  pendingTray,
  attach,
  cardProps,
  dropping,
  interruptDisabled = false,
  demoted = false,
  wide = false,
  onFocusChange,
}: {
  ref: RefObject<HTMLTextAreaElement | null>;
  composer?: SessionComposerView;
  onSend: (text: string, mode: SendMode) => void;
  onInterrupt: () => void;
  draft: string;
  onDraft: (text: string) => void;
  tray?: Tray;
  trayView?: ReactNode;
  pendingTray?: ReactNode;
  attach?: ReactNode;
  cardProps?: HTMLAttributes<HTMLDivElement>;
  dropping?: boolean;
  interruptDisabled?: boolean;
  demoted?: boolean;
  wide?: boolean;
  onFocusChange?: (focused: boolean) => void;
}) {
  const [text, setTextState] = useState(draft);
  useLayoutEffect(() => setTextState(draft), [draft]);
  const setText = (v: string) => {
    setTextState(v);
    onDraft(v);
  };
  const fine = usePointerFine();
  const [slash, setSlash] = useState(undefined as SendMode | undefined);

  const running = !!composer?.hasActiveTurn;
  const supportsQueueMode = !!composer?.supportsQueueMode;

  const input = ref;
  useAutoGrow(input, text);

  const online = composer?.enabled ?? false;
  const offline = composer ? !composer.enabled : false;
  const t = text.trim();
  const images = tray?.items.length ?? 0;
  const hasDraft = !!(t || images);
  const canSend = online && (images ? (tray?.ready() ?? false) : !!t);
  const showSendAfter = running && hasDraft && supportsQueueMode;
  const twoRow = showSendAfter || t.length > 20 || text.includes("\n");

  const defaultMode = composer?.defaultSendMode || SendMode.STEER;
  const submit = (force = false, sendMode: SendMode = defaultMode) => {
    tray?.refreshExpired();
    if (!canSend || (images && !tray!.ready())) return;
    if (t.startsWith("/") && !force && !images) return setSlash(sendMode);
    onSend(t, sendMode);
    setText("");
  };

  const keys = sendKeys(fine);
  const qKeys = wide ? queueKeys(fine) : "";
  const corePlaceholder = renderText(composer?.placeholder);
  const placeholder =
    corePlaceholder && !composer?.isDefaultPlaceholder
      ? corePlaceholder
      : keys
        ? i18n.t("composer.shortcut", { keys })
        : corePlaceholder || i18n.t("composer.message");

  const stopping = running && !hasDraft;
  const stopEnabled = (composer?.interruptVisible ?? online) && !interruptDisabled;
  const demoteCls = demoted ? " demote" : "";

  const send = stopping ? (
    <button
      className={"snd stop" + demoteCls}
      disabled={!stopEnabled}
      onMouseDown={keepComposerFocus}
      onPointerDown={keepComposerFocus}
      onClick={onInterrupt}
      aria-label={i18n.t("session.interrupt_turn")}
    >
      <Square />
    </button>
  ) : (
    <div className="send-wrap">
      <button
        className={"snd" + (canSend ? "" : " off") + demoteCls}
        disabled={!canSend}
        onMouseDown={keepComposerFocus}
        onPointerDown={keepComposerFocus}
        onClick={() => submit(false, defaultMode)}
        aria-label={i18n.t("composer.send")}
      >
        <ArrowUp />
      </button>
    </div>
  );

  return (
    <div
      className={"cbar glass" + (offline ? " offline" : "") + (dropping ? " drop" : "")}
      data-drop-label={i18n.t("attachment.release_to_add")}
      {...cardProps}
    >
      {pendingTray}
      {trayView}
      <div className={"cline" + (showSendAfter ? " two" : twoRow ? " wrap" : "")}>
        {attach}
        <textarea
          ref={input}
          className={"draft" + (t ? " typed" : "")}
          value={text}
          rows={1}
          placeholder={placeholder}
          onFocus={() => onFocusChange?.(true)}
          onBlur={() => onFocusChange?.(false)}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && !e.nativeEvent.isComposing) {
              e.preventDefault();
              submit(false, defaultMode);
            } else if (e.key === "Enter" && e.altKey && running && !e.nativeEvent.isComposing) {
              e.preventDefault();
              submit(false, SendMode.QUEUE);
            }
          }}
          aria-label={i18n.t("composer.message")}
        />
        {showSendAfter && (
          <button
            type="button"
            className="later"
            disabled={!canSend}
            onMouseDown={keepComposerFocus}
            onPointerDown={keepComposerFocus}
            onClick={() => submit(false, SendMode.QUEUE)}
          >
            <span>{i18n.t("composer.send_after")}</span>
            {qKeys && <kbd>{qKeys}</kbd>}
          </button>
        )}
        <Swap k={stopping ? "stop" : "send"} className="mo-send" hold>
          {send}
        </Swap>
      </div>
      {slash !== undefined && (
        <Alert
          title={renderText(composer?.slashWarningTitle) || i18n.t("composer.slash_title")}
          text={renderText(composer?.slashWarningText) || i18n.t("composer.slash_note")}
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
