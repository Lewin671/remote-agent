import * as i18n from "./i18n";
// Sheets opened from a session's chips: the approval mode, and the
// model, reasoning effort and context use.

import { useEffect, useRef, useState } from "react";
import { ApprovalMode, type Agent } from "./gen/remoteagent/v1/event_pb";
import type { ListModelsResponse, ModelInfo } from "./gen/remoteagent/v1/local_pb";
import { agentInfo } from "./agents";
import { agentName, agentOf, allAutoUnavailable, autoUnavailable, CONTEXT_WARN, findModel, contextPercent, modelLabel, modeOf, titleOf } from "./model";
import { errorText, type Remote, type SessionView } from "./remote";
import { Alert, Banner, Segmented, Sheet, useDialogFocus, useEscape, useOverlayExit } from "./ui";
import { Check, ChevronRight, LoaderCircle } from "./icons";

/** A session sheet's head: what it sets in bold, and whose session. */
function SheetHead({ name, s }: { name: string; s: SessionView }) {
  return (
    <div className="menu-info">
      <b>{name}</b>
      {agentName(agentOf(s))} · {titleOf(s)}
    </div>
  );
}

export function modeList(agent: Agent): { mode: ApprovalMode; name: string; desc: string; danger?: boolean }[] {
  const info = agentInfo(agent);
  return [
    { mode: ApprovalMode.ASK, name: i18n.t("modes.ask"), desc: info.askModeDesc },
    { mode: ApprovalMode.AUTO, name: i18n.t("modes.auto"), desc: i18n.t("modes.auto_desc", { agent: agentName(agent) }) },
    { mode: ApprovalMode.PLAN, name: i18n.t("modes.plan"), desc: info.planModeDesc },
    { mode: ApprovalMode.FULL_ACCESS, name: i18n.t("modes.full_access"), desc: i18n.t("modes.skip_all"), danger: true },
  ];
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

/** Renames a session. The title the user gives wins over the automatic
 * one; an empty one goes back to it. */
export function RenameDialog({ remote, s, onClose }: { remote: Remote; s: SessionView; onClose: () => void }) {
  const current = (s.info?.title ?? "").trim();
  const [title, setTitle] = useState(() => clipTitle(current));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const input = useRef<HTMLInputElement>(null);
  useEscape(onClose);
  const root = useRef<HTMLDivElement>(null);
  useOverlayExit(root);
  useDialogFocus(root);
  useEffect(() => {
    input.current?.focus();
    input.current?.select();
  }, []);
  const save = async () => {
    const next = title.trim();
    if (next === current) return onClose();
    setBusy(true);
    setError("");
    try {
      await remote.command(s.id, { case: "renameSession", value: { sessionId: s.id, title: next } });
      onClose();
    } catch (e) {
      setError(errorText(e));
      setBusy(false);
    }
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
          if (!busy) void save();
        }}
      >
        <h5>{i18n.t("session.rename_title")}</h5>
        <input ref={input} className="field" aria-label={i18n.t("session.title_label")} value={title} enterKeyHint="done" onChange={(e) => setTitle(clipTitle(e.target.value))} />
        {error ? <p className="red">{i18n.t("common.ios_save_failed", { reason: error })}</p> : <p>{i18n.t("session.auto_title_hint")}</p>}
        <div className="btns">
          <button type="button" className="btn" disabled={busy} onClick={onClose}>
            {i18n.t("common.cancel")}</button>
          <button type="submit" className="btn pri" disabled={busy}>
            {i18n.t("common.save")}</button>
        </div>
      </form>
    </div>
  );
}

/** Why a session cannot switch to mode, or "" if it can. */
export function unavailable(s: SessionView, mode: ApprovalMode, models?: ModelInfo[]): string {
  if (s.info?.modeLocked) return i18n.t("modes.locked");
  if (mode === ApprovalMode.FULL_ACCESS && s.info?.fullAccessUnavailable) return i18n.t("modes.full_unavailable");
  if (mode === ApprovalMode.AUTO) {
    if (allAutoUnavailable(models)) return i18n.t("modes.account_unsupported");
    if (autoUnavailable(models, s.info?.model ?? "")) return i18n.t("modes.model_unsupported");
  }
  return "";
}

export function ModeSheet({ remote, s, onClose }: { remote: Remote; s: SessionView; onClose: () => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [confirmFull, setConfirmFull] = useState(false);
  // The mode on offer depends on the model, which the CLI's own list says.
  const [models, setModels] = useState<ListModelsResponse>();
  useEffect(() => {
    remote.listModels(s.machineId, agentOf(s)).then(setModels, () => {});
  }, [remote, s.machineId]);
  const mode = modeOf(s);
  const cur = mode?.mode || undefined;
  const online = remote.conn === "online";
  const pick = async (mode: ApprovalMode) => {
    setBusy(true);
    setError("");
    try {
      await remote.command(s.id, { case: "setApprovalMode", value: { sessionId: s.id, mode } });
      onClose();
    } catch (e) {
      setError(errorText(e));
      setBusy(false);
    }
  };
  if (confirmFull)
    return (
      <FullAccessAlert
        onCancel={() => setConfirmFull(false)}
        onOk={() => {
          setConfirmFull(false);
          void pick(ApprovalMode.FULL_ACCESS);
        }}
      />
    );
  return (
    <Sheet onClose={onClose}>
      <SheetHead name={i18n.t("modes.title")} s={s} />
      {!cur && mode && <div className="hint top">{i18n.t("modes.current_terminal", { mode: mode.text })}</div>}
      <div className="list">
        {modeList(agentOf(s)).map((m) => {
          const why = unavailable(s, m.mode, models?.models);
          return (
            <button
              key={m.mode}
              className={"mi" + (m.danger ? " danger" : "") + (why ? " dis" : "")}
              disabled={busy || !online || !!why}
              onClick={() => (m.mode === cur ? onClose() : m.danger ? setConfirmFull(true) : void pick(m.mode))}
            >
              <div className="mt">
                <b>{m.name}</b>
                <div className="md">{why || m.desc}</div>
              </div>
              {m.mode === cur && <span className="ck"><Check /></span>}
            </button>
          );
        })}
      </div>
      {agentInfo(agentOf(s)).permissionModeNotice && <div className="hint">{agentInfo(agentOf(s)).permissionModeNotice}</div>}
      {error && (
        <Banner kind="err" action={i18n.t("session.go_terminal")} onAction={onClose}>
          {i18n.t("modes.switch_failed", { reason: error })}
        </Banner>
      )}
      <div className="hint">
        {busy
          ? i18n.t("modes.switching")
          : s.info?.modeLocked
            ? i18n.t("modes.locked_note")
            : i18n.t("session.only_current")}
      </div>
    </Sheet>
  );
}

const efforts = ["low", "medium", "high", "xhigh", "max", "ultra"];

export function tokens(n: bigint): string {
  return n >= 1000n ? `${Math.round(Number(n) / 1000)}k` : String(n);
}

export function SessionSheet({ remote, s, running, onClose }: { remote: Remote; s: SessionView; running: boolean; onClose: () => void }) {
  const [models, setModels] = useState<ListModelsResponse>();
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const [all, setAll] = useState(false);
  const [switching, setSwitching] = useState("");
  const [failed, setFailed] = useState("");
  const [compacting, setCompacting] = useState(false);
  const agent = agentOf(s);
  useEffect(() => {
    setError("");
    remote.listModels(s.machineId, agent, true).then(setModels, (e) => setError(errorText(e)));
  }, [remote, s.machineId, agent, attempt]);
  const pct = contextPercent(s);
  const model = s.info?.model ?? "";
  const effort = s.info?.effort ?? "";
  const cur = findModel(models?.models, model);
  const list = models?.models ?? [];
  // A short list shows whole, so no model is out of reach behind 全部模型
  // (Claude Code offers four); a long one shows three, and the current
  // model even when it is not among them.
  const few = list.length <= 5;
  const shown = all || few ? list : [...list.slice(0, 3), ...(cur && !list.slice(0, 3).includes(cur) ? [cur] : [])];
  const offline = remote.conn !== "online";
  const locked = !!switching || running || offline;
  const set = async (m: ModelInfo, e: string) => {
    setSwitching(m.displayName || m.slug);
    setFailed("");
    try {
      await remote.command(s.id, { case: "setModel", value: { sessionId: s.id, model: m.slug, effort: e } });
    } catch (err) {
      setFailed(i18n.t("session.model_switch_failed", { model: m.displayName || m.slug, reason: errorText(err) }));
    } finally {
      setSwitching("");
    }
  };
  const compact = async () => {
    setCompacting(true);
    try {
      await remote.command(s.id, { case: "compact", value: { sessionId: s.id } });
      onClose();
    } catch (err) {
      setFailed(i18n.t("session.compact_failed", { reason: errorText(err) }));
      setCompacting(false);
    }
  };
  // Some models have no effort levels at all (Claude Code's haiku).
  const noEffort = !!cur && cur.efforts.length === 0;
  const offered = cur?.efforts.length ? cur.efforts : efforts;
  const canSwitch = agentInfo(agent).switchesModelInSession;
  return (
    <Sheet onClose={onClose}>
      <SheetHead name={i18n.t("session.settings")} s={s} />
      {pct !== undefined && (
        <div className="ctxbox">
          <div className="ctxn">
            <div className={"num" + (pct > CONTEXT_WARN ? " warn-text" : "")}>{i18n.t("session.context_used", { percent: pct! })}</div>
            <div className="muted small">
              {compacting ? <><LoaderCircle />  {i18n.t("session.compacting")}</> : i18n.t("session.context_tokens", { used: tokens(s.info!.contextTokens), total: tokens(s.info!.contextWindow) })}
            </div>
            <div className={"bar" + (pct > CONTEXT_WARN ? " warn" : "")}><i style={{ width: `${Math.min(100, pct)}%` }} /></div>
          </div>
          <button className="btn sm" disabled={compacting || running || offline} onClick={() => void compact()}>
            {i18n.t("session.compact")}</button>
        </div>
      )}
      {canSwitch ? (
        <>
          <div className="fl">{i18n.t("models.title")}</div>
          {error && (
            <Banner kind="err" action={i18n.t("common.retry")} onAction={() => setAttempt((a) => a + 1)}>
              {i18n.t("session.models_failed")}</Banner>
          )}
          {!models && !error && <div className="hint">{i18n.t("session.reading_models")}</div>}
          <div className="list">
            {shown.map((m) => (
              <button key={m.slug} className={"mi" + (locked ? " dis" : "")} disabled={locked} onClick={() => m !== cur && void set(m, m.efforts.includes(effort) ? effort : m.defaultEffort)}>
                <div className="mt">
                  {m.displayName || m.slug}
                  {m.efforts.length === 0 ? <small>{i18n.t("session.no_effort")}</small> : agentInfo(agent).showMaxEffort && m.efforts[m.efforts.length - 1] !== efforts[efforts.length - 1] && <small>{i18n.t("session.max_effort", { effort: m.efforts[m.efforts.length - 1] })}</small>}
                </div>
                {m === cur && <span className="ck"><Check /></span>}
              </button>
            ))}
            {models && !all && !few && (
              <button className="mi link-row" onClick={() => setAll(true)}>
                {i18n.t("models.all")}<ChevronRight />
              </button>
            )}
          </div>
          <div className="fl">{i18n.t("models.effort")}</div>
          {noEffort ? (
            <div className="hint">{i18n.t("session.no_effort_adjustment", { model: cur?.displayName || cur?.slug || "" })}</div>
          ) : (
            <Segmented
              className="full wrap efforts"
              label={i18n.t("models.effort")}
              value={effort}
              onChange={(e) => cur && void set(cur, e)}
              options={offered.map((e) => ({ value: e, label: e, disabled: locked || !cur }))}
            />
          )}
          {switching && <Banner>{i18n.t("session.switching_model", { model: switching })}</Banner>}
        </>
      ) : (
        <>
          <div className="fl">{i18n.t("models.title")}</div>
          <div className="list">
            <div className="mi dis" aria-disabled="true">
              <div className="mt">
                <b>{cur?.displayName || modelLabel(models?.models, model, agent) || i18n.t("new_session.default_model")}</b>
                <div className="md">{i18n.t("session.model_locked")}</div>
              </div>
            </div>
          </div>
          <div className="fl">{i18n.t("models.effort")}</div>
          <div className="list">
            <div className="mi dis" aria-disabled="true">
              <div className="mt">
                <b>{effort || cur?.defaultEffort || i18n.t("models.default")}</b>
                <div className="md">{i18n.t("session.effort_locked")}</div>
              </div>
            </div>
          </div>
        </>
      )}
      {failed && (
        <Banner kind="err" action={i18n.t("common.got_it")} onAction={() => setFailed("")}>
          {failed}
        </Banner>
      )}
      {canSwitch && (
        <div className="hint">{running ? i18n.t("session.switch_after_turn") : offline ? i18n.t("session.switch_connected") : i18n.t("session.switch_idle")}</div>
      )}
    </Sheet>
  );
}
