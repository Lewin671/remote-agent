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
import { ArrowUp, Check, ChevronDown, Square } from "../icons";
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
  demoted = false,
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
  demoted?: boolean;
}) {
  const [text, setTextState] = useState(draft);
  useLayoutEffect(() => setTextState(draft), [draft]);
  const setText = (v: string) => {
    setTextState(v);
    onDraft(v);
  };
  const fine = usePointerFine();
  const [slash, setSlash] = useState(undefined as SendMode | undefined);
  const [sendMenu, setSendMenu] = useState(defaultSendMenu);
  const [pickedMode, setPickedMode] = useState(null as SendMode | null);
  const hold = useRef(undefined as ReturnType<typeof setTimeout> | undefined);
  const held = useRef(false);
  const sendWrap = useRef(null as HTMLDivElement | null);
  const smodeWrap = useRef(null as HTMLDivElement | null);

  const running = !!composer?.hasActiveTurn;
  const canChooseSendMode = running && !!composer?.supportsQueueMode;
  useComposerMenuDismiss(sendMenu && canChooseSendMode, smodeWrap, () => setSendMenu(false));
  useEffect(() => () => clearTimeout(hold.current), []);

  const input = ref;
  useAutoGrow(input, text);

  const online = composer?.enabled ?? false;
  const t = text.trim();
  const images = tray?.items.length ?? 0;
  const hasDraft = !!(t || images);
  const canSend = online && (images ? (tray?.ready() ?? false) : !!t);
  const defaultMode = running && !composer?.steerModeDefault ? SendMode.QUEUE : SendMode.STEER;
  const activeMode = pickedMode ?? defaultMode;
  const showModePill = canChooseSendMode && hasDraft;

  useEffect(() => {
    if (!canChooseSendMode || !hasDraft) {
      setPickedMode(null);
      setSendMenu(false);
    }
  }, [canChooseSendMode, hasDraft]);

  const submit = (force = false, sendMode = activeMode) => {
    const resolvedMode =
      running && composer && !composer.supportsQueueMode ? SendMode.QUEUE : sendMode;
    tray?.refreshExpired();
    if (!canSend || (images && !tray!.ready())) return;
    if (t.startsWith("/") && !force && !images) return setSlash(resolvedMode);
    onSend(t, resolvedMode);
    setText("");
    setPickedMode(null);
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

  const stopping = running && !hasDraft;
  const stopEnabled = (composer?.interruptVisible ?? online) && !interruptDisabled;
  const demoteCls = demoted ? " demote" : "";

  const send = stopping ? (
    <button
      className={"snd stop" + demoteCls}
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
        className={"snd" + (canSend ? "" : " off") + demoteCls}
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
    </div>
  );

  return (
    <div className={"cbar glass" + (dropping ? " drop" : "")} data-drop-label={i18n.t("attachment.release_to_add")} {...cardProps}>
      {trayView}
      <div className={"cline" + (showModePill ? " run-draft" : "")}>
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
            } else if (e.key === "Escape" && sendMenu) {
              e.preventDefault();
              setSendMenu(false);
            }
          }}
          aria-label={i18n.t("composer.message")}
        />
        {canChooseSendMode && (
          <div className="smode-wrap" ref={smodeWrap}>
            <button
              type="button"
              className={"smode" + (activeMode === SendMode.QUEUE ? " queue" : "")}
              aria-haspopup="menu"
              aria-expanded={sendMenu}
              aria-label={i18n.t("composer.mode_picker_label")}
              onMouseDown={keepComposerFocus}
              onClick={() => setSendMenu((v) => !v)}
              onKeyDown={(e) => {
                if (e.key === "Escape") {
                  e.preventDefault();
                  setSendMenu(false);
                  input.current?.focus();
                } else if (e.key === "ArrowDown" || e.key === "ArrowUp") {
                  e.preventDefault();
                  setSendMenu(true);
                }
              }}
            >
              <span>{activeMode === SendMode.QUEUE ? i18n.t("composer.mode_queue_short") : i18n.t("composer.mode_insert_short")}</span>
              <ChevronDown />
            </button>
            {sendMenu && showModePill && (
              <div className="send-menu" role="menu" aria-label={i18n.t("composer.mode_picker_label")}>
                <button
                  type="button"
                  role="menuitemradio"
                  aria-checked={activeMode === SendMode.STEER}
                  onMouseDown={keepComposerFocus}
                  onClick={() => {
                    setPickedMode(SendMode.STEER);
                    setSendMenu(false);
                  }}
                >
                  <span className="sm-row">
                    <b>{i18n.t("composer.mode_insert_short")}</b>
                    {activeMode === SendMode.STEER && <Check />}
                  </span>
                  <small>{i18n.t("composer.insert_hint")}</small>
                </button>
                <button
                  type="button"
                  role="menuitemradio"
                  aria-checked={activeMode === SendMode.QUEUE}
                  onMouseDown={keepComposerFocus}
                  onClick={() => {
                    setPickedMode(SendMode.QUEUE);
                    setSendMenu(false);
                  }}
                >
                  <span className="sm-row">
                    <b>{i18n.t("composer.after_turn")}</b>
                    {activeMode === SendMode.QUEUE && <Check />}
                  </span>
                  <small>{i18n.t("composer.queue_hint")}</small>
                </button>
              </div>
            )}
          </div>
        )}
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
