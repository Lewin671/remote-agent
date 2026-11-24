import * as i18n from "./i18n";
// Sheets opened from a session's chips: the approval mode, and the
// model, reasoning effort and context use.

import { create } from "@bufbuild/protobuf";
import { useEffect, useRef, useState } from "react";
import { renderText } from "./core/text";
import {
  SessionCommandStatus,
  type SessionActionsView,
  type SessionCommandState,
  type SessionHeaderView,
} from "./gen/remoteagent/core/v1/session_view_pb";
import { ApprovalMode } from "./gen/remoteagent/v1/event_pb";
import {
  CommandSchema,
  type Command,
} from "./gen/remoteagent/v1/remote_pb";
import { Check, ChevronRight, LoaderCircle } from "./icons";
import { Alert, Banner, Segmented, Sheet, useDialogFocus, useEscape, useOverlayExit } from "./ui";

/** A session sheet's head: what it sets in bold, and whose session. */
function SheetHead({ name, header }: { name: string; header?: SessionHeaderView }) {
  return (
    <div className="menu-info">
      <b>{name}</b>
      {header?.agentName ?? ""} · {renderText(header?.title)}
    </div>
  );
}

/** Asks before switching to full access; used wherever it can be chosen. */
export function FullAccessAlert({ onOk, onCancel, forNew }: { onOk: () => void; onCancel: () => void; forNew?: boolean }) {
  return (
    <Alert
      title={i18n.t("modes.full_title")}
      text={forNew ? i18n.t("modes.full_new_note") : i18n.t("modes.full_current_note")}
      ok={i18n.t("modes.switch")}
      danger
      onCancel={onCancel}
      onOk={onOk}
    />
  );
}

/** The longest title a session can be given; the daemon checks it too. */
export const TITLE_MAX = 60;

/** Cuts a title to TITLE_MAX characters, not splitting one. */
function clipTitle(t: string): string {
  return [...t.replace(/\s*\n\s*/g, " ")].slice(0, TITLE_MAX).join("");
}

function isCommandError(cmd?: SessionCommandState): boolean {
  return !!cmd && (cmd.status === SessionCommandStatus.FAILED || cmd.status === SessionCommandStatus.UNKNOWN);
}

/** Renames a session. The title the user gives wins over the automatic
 * one; an empty one goes back to it. */
export function RenameDialog({
  sessionId,
  header,
  actions,
  onClose,
  onSubmitCommand,
}: {
  sessionId: string;
  header?: SessionHeaderView;
  actions?: SessionActionsView;
  onClose: () => void;
  onSubmitCommand: (slotKey: string, cmd: Command) => void;
}) {
  const current = renderText(header?.title).trim();
  const [title, setTitle] = useState(() => clipTitle(current));
  const [submitted, setSubmitted] = useState(false);
  const sawBusy = useRef(false);
  const input = useRef<HTMLInputElement>(null);
  useEscape(onClose);
  const root = useRef<HTMLDivElement>(null);
  useOverlayExit(root);
  useDialogFocus(root);
  useEffect(() => {
    input.current?.focus();
    input.current?.select();
  }, []);

  const cmd = actions?.rename;
  const busy = !!cmd?.busy;
  const hasError = isCommandError(cmd);
  const error = hasError ? renderText(cmd?.errorText) || renderText(cmd?.statusText) : "";

  useEffect(() => {
    if (!submitted) return;
    if (busy) {
      sawBusy.current = true;
      return;
    }
    if (cmd?.status === SessionCommandStatus.IDLE && (sawBusy.current || renderText(header?.title).trim() === title.trim())) {
      onClose();
    }
  }, [submitted, busy, cmd?.status, header?.title, title, onClose]);

  const save = () => {
    const next = title.trim();
    if (next === current) {
      onClose();
      return;
    }
    sawBusy.current = false;
    setSubmitted(true);
    onSubmitCommand(
      cmd?.slotKey ?? "",
      create(CommandSchema, {
        kind: {
          case: "renameSession",
          value: { sessionId, title: next },
        },
      }),
    );
  };

  return (
    <div className="overlay" ref={root}>
      <div className="dim" onClick={onClose} />
      <form
        className="dialog rename"
        role="dialog"
        aria-modal="true"
        aria-label={i18n.t("session.rename_title")}
        onSubmit={(e) => {
          e.preventDefault();
          if (!busy) save();
        }}
      >
        <h5>{i18n.t("session.rename_title")}</h5>
        <input
          ref={input}
          className="field"
          aria-label={i18n.t("session.title_label")}
          value={title}
          enterKeyHint="done"
          onChange={(e) => setTitle(clipTitle(e.target.value))}
        />
        {error ? <p className="red">{i18n.t("common.ios_save_failed", { reason: error })}</p> : <p>{i18n.t("session.auto_title_hint")}</p>}
        <div className="btns">
          <button type="button" className="btn" disabled={busy} onClick={onClose}>
            {i18n.t("common.cancel")}
          </button>
          <button type="submit" className="btn pri" disabled={busy}>
            {i18n.t("common.save")}
          </button>
        </div>
      </form>
    </div>
  );
}

export function ModeSheet({
  sessionId,
  header,
  actions,
  onClose,
  onSubmitCommand,
}: {
  sessionId: string;
  header?: SessionHeaderView;
  actions?: SessionActionsView;
  onClose: () => void;
  onSubmitCommand: (slotKey: string, cmd: Command) => void;
}) {
  const [confirmFull, setConfirmFull] = useState(false);
  const [pickedMode, setPickedMode] = useState<ApprovalMode | undefined>(undefined);
  const sawBusy = useRef(false);

  const cmd = actions?.setMode;
  const busy = !!cmd?.busy;
  const hasError = isCommandError(cmd);
  const error = hasError ? renderText(cmd?.errorText) || renderText(cmd?.statusText) : "";
  const cur = header?.approvalMode || undefined;

  useEffect(() => {
    if (pickedMode === undefined) return;
    if (busy) {
      sawBusy.current = true;
      return;
    }
    if (cmd?.status === SessionCommandStatus.IDLE && (sawBusy.current || header?.approvalMode === pickedMode)) {
      onClose();
    }
  }, [pickedMode, busy, cmd?.status, header?.approvalMode, onClose]);

  const pick = (mode: ApprovalMode) => {
    sawBusy.current = false;
    setPickedMode(mode);
    onSubmitCommand(
      cmd?.slotKey ?? "",
      create(CommandSchema, {
        kind: {
          case: "setApprovalMode",
          value: { sessionId, mode },
        },
      }),
    );
  };

  if (confirmFull) {
    return (
      <FullAccessAlert
        onCancel={() => setConfirmFull(false)}
        onOk={() => {
          setConfirmFull(false);
          pick(ApprovalMode.FULL_ACCESS);
        }}
      />
    );
  }

  const choices = actions?.modeChoices ?? [];
  const anyLocked = !!actions?.modeLocked;
  const permissionNotice = renderText(actions?.modePermissionNotice);

  return (
    <Sheet onClose={onClose}>
      <SheetHead name={i18n.t("modes.title")} header={header} />
      {!cur && header?.approvalModeLabel && (
        <div className="hint top">{i18n.t("modes.current_terminal", { mode: renderText(header.approvalModeLabel) })}</div>
      )}
      <div className="list">
        {choices.map((m) => {
          const why = renderText(m.unavailableReason);
          const disabled = busy || !m.available || !!why;
          return (
            <button
              key={m.mode}
              className={"mi" + (m.mode === ApprovalMode.FULL_ACCESS ? " danger" : "") + (why ? " dis" : "")}
              disabled={disabled}
              onClick={() => (m.selected ? onClose() : m.mode === ApprovalMode.FULL_ACCESS ? setConfirmFull(true) : pick(m.mode))}
            >
              <div className="mt">
                <b>{renderText(m.label)}</b>
                <div className="md">{why || renderText(m.note)}</div>
              </div>
              {m.selected && (
                <span className="ck">
                  <Check />
                </span>
              )}
            </button>
          );
        })}
      </div>
      {permissionNotice && <div className="hint">{permissionNotice}</div>}
      {error && (
        <Banner kind="err" action={i18n.t("session.go_terminal")} onAction={onClose}>
          {i18n.t("modes.switch_failed", { reason: error })}
        </Banner>
      )}
      <div className="hint">
        {busy ? i18n.t("modes.switching") : anyLocked ? i18n.t("modes.locked_note") : i18n.t("session.only_current")}
      </div>
    </Sheet>
  );
}

export function tokens(n: bigint): string {
  return n >= 1000n ? `${Math.round(Number(n) / 1000)}k` : String(n);
}

function SessionUsageEntryRow({
  ue,
  onClose,
  onOpenUsage,
}: {
  ue: NonNullable<SessionActionsView["usageEntry"]>;
  onClose: () => void;
  onOpenUsage?: () => void;
}) {
  const titleText = i18n.t("session.usage_entry_title");
  const val = renderText(ue.valueText);
  const sub = renderText(ue.subtitle);
  const foot = renderText(ue.footnote);
  const canNav = ue.supported && !!onOpenUsage;
  const a11yLabel = [titleText, sub, val].filter(Boolean).join(" · ");
  const mtCls = "mt";
  return (
    <>
      <div className="list session-usage-entry">
        {canNav ? (
          <button
            type="button"
            className="mi"
            aria-label={a11yLabel}
            onClick={() => {
              onClose();
              onOpenUsage();
            }}
          >
            <div className={mtCls}>
              <b>{titleText}</b>
              {sub && <div className="md">{sub}</div>}
            </div>
            {val && <span className="mv">{val}</span>}
            <ChevronRight />
          </button>
        ) : (
          <div className="mi dis" aria-disabled="true" aria-label={a11yLabel}>
            <div className={mtCls}>
              <b>{titleText}</b>
              {sub && <div className="md">{sub}</div>}
            </div>
            {val && <span className="mv">{val}</span>}
          </div>
        )}
      </div>
      {foot && <div className="hint">{foot}</div>}
    </>
  );
}

export function SessionSheet({
  sessionId,
  header,
  actions,
  onClose,
  onRefreshModels,
  onSubmitCommand,
  onAbandonCommand,
  onOpenUsage,
}: {
  sessionId: string;
  header?: SessionHeaderView;
  actions?: SessionActionsView;
  onClose: () => void;
  onRefreshModels: () => void;
  onSubmitCommand: (slotKey: string, cmd: Command) => void;
  onAbandonCommand?: (slotKey: string) => void;
  onOpenUsage?: () => void;
}) {
  const [all, setAll] = useState(false);
  const [switchingName, setSwitchingName] = useState("");
  const [compactSubmitted, setCompactSubmitted] = useState(false);
  const sawCompactBusy = useRef(false);

  const modelCmd = actions?.setModel;
  const modelBusy = !!modelCmd?.busy;
  const modelErr = isCommandError(modelCmd) ? renderText(modelCmd?.errorText) || renderText(modelCmd?.statusText) : "";

  const compactCmd = actions?.compact;
  const compactBusy = !!compactCmd?.busy;
  const compactErr = isCommandError(compactCmd) ? renderText(compactCmd?.errorText) || renderText(compactCmd?.statusText) : "";

  useEffect(() => {
    if (!compactSubmitted) return;
    if (compactBusy) {
      sawCompactBusy.current = true;
      return;
    }
    if (compactCmd?.status === SessionCommandStatus.IDLE && sawCompactBusy.current) {
      onClose();
    }
  }, [compactSubmitted, compactBusy, compactCmd?.status, onClose]);

  const choices = actions?.modelChoices ?? [];
  const effortChoices = actions?.effortChoices ?? [];
  const cur = choices.find((c) => c.selected);
  const few = choices.length <= 5;
  const shown = all || few ? choices : [...choices.slice(0, 3), ...(cur && !choices.slice(0, 3).includes(cur) ? [cur] : [])];
  const canSwitch = header?.capabilities?.supportsModelSwitch ?? false;
  const locked = modelBusy || !actions?.canSwitchModel;
  const modelsError = renderText(actions?.modelsError);
  const pct = header?.hasContext ? header.contextPercent : undefined;
  const activeEffort = effortChoices.find((e) => e.selected)?.id ?? header?.effort ?? "";

  const setModel = (slug: string, label: string, effort: string) => {
    setSwitchingName(label);
    onSubmitCommand(
      modelCmd?.slotKey ?? "",
      create(CommandSchema, {
        kind: {
          case: "setModel",
          value: { sessionId, model: slug, effort },
        },
      }),
    );
  };

  const compact = () => {
    sawCompactBusy.current = false;
    setCompactSubmitted(true);
    onSubmitCommand(
      compactCmd?.slotKey ?? "",
      create(CommandSchema, {
        kind: {
          case: "compact",
          value: { sessionId },
        },
      }),
    );
  };

  const noEffort = !!cur && cur.efforts.length === 0;
  const bannerError =
    modelErr
      ? i18n.t("session.model_switch_failed", { model: switchingName || header?.modelLabel || "", reason: modelErr })
      : compactErr
        ? i18n.t("session.compact_failed", { reason: compactErr })
        : "";
  const errorSlot = modelErr ? modelCmd?.slotKey : compactErr ? compactCmd?.slotKey : undefined;

  return (
    <Sheet onClose={onClose}>
      <SheetHead name={i18n.t("session.settings")} header={header} />
      {pct !== undefined && header && (
        <div className="ctxbox">
          <div className="ctxn">
            <div className={"num" + (header.highContextWarning ? " warn-text" : "")}>{i18n.t("session.context_used", { percent: pct })}</div>
            <div className="muted small">
              {compactBusy ? (
                <>
                  <LoaderCircle /> {i18n.t("session.compacting")}
                </>
              ) : (
                renderText(header.contextText) || i18n.t("session.context_tokens", { used: tokens(header.contextTokens), total: tokens(header.contextWindow) })
              )}
            </div>
            <div className={"bar" + (header.highContextWarning ? " warn" : "")}>
              <i style={{ width: `${Math.min(100, pct)}%` }} />
            </div>
          </div>
          <button className="btn sm" disabled={compactBusy || !actions?.canCompact} onClick={compact}>
            {i18n.t("session.compact")}
          </button>
        </div>
      )}
      {canSwitch ? (
        <>
          <div className="fl">{i18n.t("models.title")}</div>
          {modelsError && (
            <Banner kind="err" action={i18n.t("common.retry")} onAction={onRefreshModels}>
              {i18n.t("session.models_failed")}
            </Banner>
          )}
          {actions?.modelsLoading && !choices.length && !modelsError && <div className="hint">{i18n.t("session.reading_models")}</div>}
          <div className="list">
            {shown.map((m) => (
              <button
                key={m.slug}
                className={"mi" + (locked ? " dis" : "")}
                disabled={locked}
                onClick={() => !m.selected && setModel(m.slug, m.displayName || m.slug, m.efforts.includes(activeEffort) ? activeEffort : m.defaultEffort)}
              >
                <div className="mt">
                  {m.displayName || m.slug}
                  {m.efforts.length === 0 && (
                    <small>{i18n.t("session.no_effort")}</small>
                  )}
                  {m.efforts.length !== 0 && m.maxEffortHint && (
                    <small>{renderText(m.maxEffortHint)}</small>
                  )}
                </div>
                {m.selected && (
                  <span className="ck">
                    <Check />
                  </span>
                )}
              </button>
            ))}
            {actions?.modelsLoaded && !all && !few && (
              <button className="mi link-row" onClick={() => setAll(true)}>
                {i18n.t("models.all")}
                <ChevronRight />
              </button>
            )}
          </div>
          <div className="fl">{i18n.t("models.effort")}</div>
          {noEffort ? (
            <div className="hint">
              {renderText(cur?.fixedEffortNote) || i18n.t("session.no_effort_adjustment", { model: cur?.displayName || cur?.slug || "" })}
            </div>
          ) : (
            <Segmented
              className="full wrap efforts"
              label={i18n.t("models.effort")}
              value={activeEffort}
              onChange={(e) => cur && setModel(cur.slug, cur.displayName || cur.slug, e)}
              options={effortChoices.map((e) => ({
                value: e.id,
                label: renderText(e.label) || e.id,
                disabled: locked || !cur,
              }))}
            />
          )}
          {modelBusy && <Banner>{i18n.t("session.switching_model", { model: switchingName || header?.modelLabel || "" })}</Banner>}
        </>
      ) : (
        <>
          <div className="fl">{i18n.t("models.title")}</div>
          <div className="list">
            <div className="mi dis" aria-disabled="true">
              <div className="mt">
                <b>{cur?.displayName || header?.modelLabel || i18n.t("new_session.default_model")}</b>
                <div className="md">{i18n.t("session.model_locked")}</div>
              </div>
            </div>
          </div>
          <div className="fl">{i18n.t("models.effort")}</div>
          <div className="list">
            <div className="mi dis" aria-disabled="true">
              <div className="mt">
                <b>{activeEffort || cur?.defaultEffort || i18n.t("models.default")}</b>
                <div className="md">{i18n.t("session.effort_locked")}</div>
              </div>
            </div>
          </div>
        </>
      )}
      {bannerError && (
        <Banner kind="err" action={errorSlot && onAbandonCommand ? i18n.t("common.got_it") : undefined} onAction={errorSlot && onAbandonCommand ? () => onAbandonCommand(errorSlot) : undefined}>
          {bannerError}
        </Banner>
      )}
      {canSwitch && (
        <div className="hint">{renderText(actions?.modelSwitchBlockedReason) || i18n.t("session.switch_idle")}</div>
      )}
      {actions?.usageEntry?.visible && (
        <SessionUsageEntryRow ue={actions.usageEntry} onClose={onClose} onOpenUsage={onOpenUsage} />
      )}
    </Sheet>
  );
}
