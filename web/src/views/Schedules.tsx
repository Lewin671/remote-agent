import * as l10n from "../i18n";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { Agent, ApprovalMode } from "../gen/remoteagent/v1/event_pb";
import {
  ScheduleDisplayGroup, ScheduleFormMode, ScheduleFormNoticeKind, ScheduleFormNoticeSeverity,
  ScheduleIntervalUnit, ScheduleOperationKind, ScheduleRunStatus, ScheduleSource,
  ScheduleTargetType, ScheduleTimingEnd, ScheduleTimingKind, ScheduleTone,
  type ScheduleFormViewModel, type ScheduleMutationView, type ScheduleResultCardView,
  type ScheduleDetailViewModel, type ScheduleSessionStripViewModel, type SchedulesViewModel,
  type ScheduleTaskActionsView, type ScheduleTaskItemView, type ScheduleTimingDraftView,
} from "../gen/remoteagent/core/v1/schedules_view_pb";
import { ConnState } from "../gen/remoteagent/core/v1/home_view_pb";
import {
  routeForMachineSettings, routeForSession, routeForTask, useCoreHost, useProjectView, useScheduleDetailView,
  useScheduleFormView, useScheduleSessionStripView, useSchedulesView, type CoreHost, type Go,
} from "../core/host";
import { renderText } from "../core/text";
import { Markdown } from "../markdown";
import { type Remote } from "../remote";
import { Alert, Banner, Exp, NavBar, Segmented, Sheet, Switch, useDialogFocus, useEscape, useOverlayExit } from "../ui";
import { Check, ChevronDown, ChevronRight, Clock, Ellipsis, Info, LoaderCircle, Plus, X } from "../icons";
import { DirPickerSheet, ProjectItem, ProjectList } from "./Projects";
import { ScheduleMessageEditor, ScheduleText, ScheduleTextarea } from "./ScheduleText";

const { t } = l10n;
const noticeKind = (s: ScheduleFormNoticeSeverity): "err" | "warn" => s === ScheduleFormNoticeSeverity.ERROR ? "err" : "warn";
const unreadDot = (label: string) => <i className="mark unread schedule-unread-dot" role="img" aria-label={label} />;

function Field({ name, value, onClick, disabled }: { name: string; value: ReactNode; onClick?: () => void; disabled?: boolean }) {
  return onClick
    ? <button className="row schedule-field" disabled={disabled} onClick={onClick}><span>{name}</span><span className="v">{value}</span>{!disabled && <ChevronRight className="chev" />}</button>
    : <div className="row schedule-field"><span>{name}</span><span className="v">{value}</span></div>;
}

function ChoiceRow({ label, sub, selected, disabled, danger, end = "check", onClick }: {
  label: ReactNode; sub?: string; selected?: boolean; disabled?: boolean; danger?: boolean; end?: "check" | "chev"; onClick: () => void;
}) {
  return (
    <button className={"mi" + (disabled ? " dis" : "")} disabled={disabled} onClick={onClick}>
      <div className="mt"><span className={danger ? "red" : ""}>{label}</span>{sub && <div className="md">{sub}</div>}</div>
      {end === "chev" ? <ChevronRight className="chev" /> : selected && <span className="ck"><Check /></span>}
    </button>
  );
}

function FullAccessAlert({ onCancel, onConfirm }: { onCancel: () => void; onConfirm: () => void }) {
  return <Alert title={t("schedule.run_with_full_access")} text={t("schedule.no_approval_will_be_required_for_future_runs_existing")} ok={t("modes.full_access")} danger onCancel={onCancel} onOk={onConfirm} />;
}

// --- Browser WASM Intl timezone & DST resolution (WASM builds lack tzdata) ---

const zoneFormatters = new Map<string, Intl.DateTimeFormat>();
function zonePartsFormatter(zone: string): Intl.DateTimeFormat {
  let fmt = zoneFormatters.get(zone);
  if (!fmt) {
    fmt = new Intl.DateTimeFormat("en-CA", {
      timeZone: zone, year: "numeric", month: "2-digit", day: "2-digit",
      hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23",
    });
    zoneFormatters.set(zone, fmt);
  }
  return fmt;
}

export function isValidZone(zone: string): boolean {
  const z = zone.trim();
  if (!z) return false;
  try { zonePartsFormatter(z); return true; } catch { return false; }
}

function zoneWallParts(ms: number, zone: string) {
  const out: Record<string, number> = {};
  for (const part of zonePartsFormatter(zone).formatToParts(new Date(ms))) {
    if (part.type !== "literal") out[part.type] = Number(part.value);
  }
  return { year: out.year, month: out.month, day: out.day, hour: out.hour === 24 ? 0 : out.hour, minute: out.minute, second: out.second };
}

const pad2 = (n: number) => String(n).padStart(2, "0");

export function formatZonedDate(ms: bigint | number, zone: string): string {
  const n = Number(ms);
  if (!Number.isFinite(n) || n <= 0 || !isValidZone(zone)) return "";
  const p = zoneWallParts(n, zone);
  return `${p.year}-${pad2(p.month)}-${pad2(p.day)}`;
}

export function formatZonedDatetimeLocal(ms: bigint | number, zone: string): string {
  const n = Number(ms);
  if (!Number.isFinite(n) || n <= 0 || !isValidZone(zone)) return "";
  const p = zoneWallParts(n, zone);
  return `${p.year}-${pad2(p.month)}-${pad2(p.day)}T${pad2(p.hour)}:${pad2(p.minute)}`;
}

export function resolveZonedWall(dateStr: string, timeStr: string, zone: string): { valid: boolean; ms?: bigint; gap?: boolean } {
  const dm = /^(\d{4})-(\d{2})-(\d{2})$/.exec(dateStr.trim()), tm = /^(\d{2}):(\d{2})$/.exec(timeStr.trim()), z = zone.trim();
  if (!dm || !tm || !isValidZone(z)) return { valid: false };
  const year = Number(dm[1]), month = Number(dm[2]), day = Number(dm[3]), hour = Number(tm[1]), minute = Number(tm[2]);
  const check = new Date(Date.UTC(year, month - 1, day));
  if (check.getUTCFullYear() !== year || check.getUTCMonth() + 1 !== month || check.getUTCDate() !== day || hour < 0 || hour > 23 || minute < 0 || minute > 59) {
    return { valid: false };
  }
  const utcWall = Date.UTC(year, month - 1, day, hour, minute, 0);
  const offsetAt = (probeMs: number) => {
    const p = zoneWallParts(probeMs, z);
    return Date.UTC(p.year, p.month - 1, p.day, p.hour, p.minute, p.second) - probeMs;
  };
  const cand = utcWall - offsetAt(utcWall - offsetAt(utcWall));
  const matchesWall = (ms: number) => {
    const p = zoneWallParts(ms, z);
    return p.year === year && p.month === month && p.day === day && p.hour === hour && p.minute === minute;
  };
  if (!matchesWall(cand)) return { valid: true, gap: true };
  // Prefer the earlier instant when a DST fall-back repeats the same wall time.
  let best = cand;
  for (const offset of [3600_000, 1800_000]) {
    const earlier = cand - offset;
    if (matchesWall(earlier) && earlier < best) best = earlier;
  }
  return { valid: true, ms: BigInt(best), gap: false };
}

function weekdayShortLabel(day: number): string {
  return new Intl.DateTimeFormat(l10n.localeStore.snapshot(), { weekday: "short", timeZone: "UTC" }).format(new Date(Date.UTC(2024, 0, 7 + (day % 7))));
}

function runStatusIcon(status: ScheduleRunStatus, manual: boolean) {
  if (status === ScheduleRunStatus.RUNNING || status === ScheduleRunStatus.DISPATCHING || status === ScheduleRunStatus.QUEUED) return <LoaderCircle className="spin" />;
  if (status === ScheduleRunStatus.COMPLETED) return manual ? <Check /> : <span className="schedule-lead" />;
  if (status === ScheduleRunStatus.FAILED || status === ScheduleRunStatus.CANCELLED || status === ScheduleRunStatus.INTERRUPTED) return <X />;
  return <span className="schedule-lead" />;
}

// --- List & Menu ---

export interface ScheduleListActions {
  onSelectTask(machineId: string, taskId: string): void;
  onCreateTask(): void;
  onToggleEnded(): void;
  onMarkAllRead(): void;
  onMarkRead(machineId: string, taskId: string): void;
  onMarkUnread(machineId: string, taskId: string, leaving?: boolean): void;
  onOperate(machineId: string, taskId: string, op: ScheduleOperationKind): void;
  onRetryMachine(machineId: string): void;
}

export function ScheduleMenu({
  title, subtitle, actions, contextMenu = false, mutation, anchor, onClose,
  onRunNow, onToggle, onMarkRead, onMarkUnread, onDelete, onAbandonMutation,
}: {
  title: string; subtitle: string; actions?: ScheduleTaskActionsView; contextMenu?: boolean;
  mutation?: ScheduleMutationView; anchor?: React.RefObject<HTMLElement | null>; onClose: () => void;
  onRunNow: () => void; onToggle: () => void; onMarkRead: () => void; onMarkUnread: () => void; onDelete: () => void; onAbandonMutation?: () => void;
}) {
  const [deleting, setDeleting] = useState(false);
  const busy = !!mutation?.busy, frozen = !!mutation?.frozen;
  if (deleting) {
    return (
      <Alert title={t("schedule.delete_task_schedules")} text={t("schedule.stops_automatic_runs_existing_conversations_remain")} ok={t("schedule.delete")} danger
        onCancel={() => !busy && setDeleting(false)} onOk={() => { setDeleting(false); onDelete(); onClose(); }} />
    );
  }
  const readAction = (actions?.canMarkRead || actions?.canMarkUnread) ? (
    <button className="mi" role="menuitem" disabled={busy} onClick={() => { if (actions.canMarkRead) onMarkRead(); else onMarkUnread(); onClose(); }}>
      <span className="mt">{actions.canMarkRead ? t("schedule.mark_as_read") : t("schedule.mark_as_unread")}</span>
    </button>
  ) : null;
  const toggleLabel = renderText(actions?.toggleLabel) || (actions?.isResumeAction ? t("schedule.resume") : t("schedule.pause"));
  return (
    <Sheet onClose={() => !busy && onClose()} className="menu schedule-menu" anchor={anchor}>
      <div className="menu-info"><b>{title}</b>{subtitle}</div>
      <div className="list" role="menu">
        {contextMenu && readAction}
        <button className="mi" role="menuitem" disabled={busy || frozen || !actions?.canRunNow} onClick={() => { onClose(); onRunNow(); }}>
          <span className="mt">{t("schedule.run_once_now")}</span>
        </button>
        {!contextMenu && readAction}
        {actions?.showToggleAction && (
          <button className="mi" role="menuitem" disabled={busy || frozen || !actions.canToggle} onClick={() => { onClose(); onToggle(); }}>
            <span className="mt">{toggleLabel}</span>
          </button>
        )}
      </div>
      {actions?.canDelete !== undefined && (
        <div className="list" role="menu">
          <button className="mi danger" role="menuitem" disabled={busy || frozen || !actions.canDelete} onClick={() => setDeleting(true)}>
            <span className="mt">{t("schedule.delete_task")}</span>
          </button>
        </div>
      )}
      {frozen && (
        <Banner kind="warn" action={onAbandonMutation ? t("common.cancel") : undefined} onAction={onAbandonMutation}>
          {t("schedule.the_previous_operation_s_result_is_unconfirmed_verify_the")}
        </Banner>
      )}
    </Sheet>
  );
}

export function ScheduleItemRow({ item, layout, gut = false, selected: selectedProp = false, actions }: {
  item: ScheduleTaskItemView; layout: "card" | "row"; gut?: boolean; selected?: boolean; actions: ScheduleListActions;
}) {
  const wrap = useRef<HTMLDivElement>(null);
  const [menuOpen, setMenuOpen] = useState(false);
  const attention = item.group === ScheduleDisplayGroup.ATTENTION || item.tone === ScheduleTone.BAD || item.tone === ScheduleTone.WARN;
  const fresh = item.hasUnread, isSelected = item.selected || selectedProp, card = layout === "card" && (attention || fresh);
  const dot = (attention || fresh) && <i className={"mark schedule-unread-dot " + (attention ? "wait attention-dot" : "unread")} role="img" aria-label={attention ? t("schedule.needs_your_attention") : t("home.ios_new_results")} />;
  const leadText = renderText(item.attentionLead), reasonText = renderText(item.attentionReason), previewText = renderText(item.preview);
  const row1 = <span className="row1">{card && dot}<b>{item.title}</b><span>{renderText(item.timeText)}</span></span>;
  const pv = <span className="pv">{attention ? <><strong>{leadText}</strong>{reasonText ? ` · ${reasonText}` : ""}</> : item.previewIsPlaceholder ? <span className="none">{previewText}</span> : previewText}</span>;
  const mc = <span className="mc">{item.inProgress && <><LoaderCircle className="spin" />{t("schedule.running_schedules")}</>}{renderText(item.metadata)}</span>;
  const flags = (fresh ? " unread" : "") + (attention ? " attention" : "") + (item.paused ? " paused" : "") + (item.machineOffline ? " off" : "") + (isSelected ? " sel" : "");
  const btnProps = {
    onContextMenu: (e: React.MouseEvent) => { if (matchMedia("(min-width: 900px)").matches) { e.preventDefault(); setMenuOpen(true); } },
    onClick: () => actions.onSelectTask(item.machineId, item.taskId),
  };
  const showReadBtn = !!item.actions?.canMarkRead || !!item.actions?.canMarkUnread;
  return (
    <div className={"schedule-row-wrap" + (isSelected ? " schedule-selected" : "")} ref={wrap}>
      {card ? (
        <button className={"rc schedule-line" + (attention ? " warn" : "") + flags} {...btnProps}>
          {row1}{pv}<span className="f">{mc}<span className="go">{attention ? t("schedule.take_action") : t("schedule.read_full_text")}<ChevronRight /></span></span>
        </button>
      ) : (
        <button className={"sess schedule-line" + flags} {...btnProps}>{gut && <span className="gut">{dot}</span>}<span className="sm">{row1}{pv}{mc}</span></button>
      )}
      {showReadBtn && (
        <button className="schedule-read-action" aria-label={fresh ? t("schedule.mark_as_read") : t("schedule.mark_as_unread")}
          onClick={() => (fresh ? actions.onMarkRead(item.machineId, item.taskId) : actions.onMarkUnread(item.machineId, item.taskId, isSelected))}>
          {fresh ? t("schedule.mark_as_read") : t("schedule.mark_as_unread")}
        </button>
      )}
      {menuOpen && (
        <ScheduleMenu title={item.title} subtitle={renderText(item.menuSubtitle)} actions={item.actions} contextMenu anchor={wrap} onClose={() => setMenuOpen(false)}
          onRunNow={() => actions.onOperate(item.machineId, item.taskId, ScheduleOperationKind.RUN)}
          onToggle={() => actions.onOperate(item.machineId, item.taskId, item.actions?.isResumeAction ? ScheduleOperationKind.RECOVER : ScheduleOperationKind.PAUSE)}
          onMarkRead={() => actions.onMarkRead(item.machineId, item.taskId)}
          onMarkUnread={() => actions.onMarkUnread(item.machineId, item.taskId, isSelected)}
          onDelete={() => actions.onOperate(item.machineId, item.taskId, ScheduleOperationKind.CANCEL)} />
      )}
    </div>
  );
}

export function SchedulesListView({ vm, sidebar = false, selected, actions }: {
  vm: SchedulesViewModel; sidebar?: boolean; selected?: string; actions: ScheduleListActions;
}) {
  const layout = sidebar ? "row" : "card";
  const renderRows = (items: ScheduleTaskItemView[], l: "card" | "row" = layout) =>
    items.map((item) => <ScheduleItemRow key={`${item.machineId}/${item.taskId}`} item={item} layout={l} gut={sidebar} selected={selected === routeForTask(item.machineId, item.taskId)} actions={actions} />);

  const list = (
    <div className="schedule-list">
      {vm.machineErrors.map((err) => (
        <Banner key={err.machineId} kind="err" action={err.canRetry ? t("common.retry") : undefined} onAction={err.canRetry ? () => actions.onRetryMachine(err.machineId) : undefined}>
          {renderText(err.error)}
        </Banner>
      ))}
      {!vm.anyMachineSupported && vm.unsupportedNotice && <Banner kind="warn">{renderText(vm.unsupportedNotice)}</Banner>}
      {vm.offlineNotice && <Banner kind="warn">{renderText(vm.offlineNotice)}</Banner>}
      {vm.loading && <p className="home-note" data-late="1"><LoaderCircle className="spin" />{t("schedule.reading_tasks")}</p>}
      {vm.showEmptyHint && vm.emptyHint && <p className="home-note">{renderText(vm.emptyHint)}</p>}
      {vm.attention.length > 0 && <section className="schedule-group attention-group">{renderRows(vm.attention, "card")}</section>}
      {vm.fresh.length > 0 && (
        <section className="schedule-group">
          <div className="sec">
            {t("schedule.new_results_schedules")}
            {vm.canMarkAllRead && <button className="sec-act" onClick={actions.onMarkAllRead}>{t("schedule.mark_all_as_read")}</button>}
          </div>
          {renderRows(vm.fresh)}
        </section>
      )}
      {vm.others.length > 0 && (
        <section className="schedule-group">
          {vm.othersSectionTitle && <div className="sec">{renderText(vm.othersSectionTitle)}</div>}
          {renderRows(vm.others, "row")}
        </section>
      )}
      {vm.ended.length > 0 && (
        <section className="schedule-group">
          <button className="tail schedule-ended" aria-expanded={vm.endedExpanded} onClick={actions.onToggleEnded}>
            <span>{renderText(vm.endedLabel) || t("schedule.ended")} ({vm.totalEnded})</span>
            <ChevronDown />
          </button>
          {vm.endedExpanded && renderRows(vm.ended, "row")}
        </section>
      )}
    </div>
  );

  const connected = vm.connection?.state === ConnState.CONNECTED;
  const title = (() => {
    const emptyLoading = vm.loading && vm.attention.length === 0 && vm.fresh.length === 0 && vm.others.length === 0 && vm.ended.length === 0;
    return (!emptyLoading && connected && renderText(vm.headline?.title)) || t("home.schedules_tab");
  })();
  const subTitle = connected ? renderText(vm.headline?.sub) : "";
  if (sidebar) return <><h1 className="whl">{title}</h1>{subTitle && <div className="wsub">{subTitle}</div>}<div className="scroll schedule-sidebar">{list}</div></>;
  return (
    <div className="page schedules-page">
      <div className="body under-bar">
        <div className="tbar"><span className="tsp" /><button className="act" aria-label={t("home.new_schedule")} onClick={actions.onCreateTask} disabled={!vm.canCreate}><Plus /></button></div>
        <h1 className="hl">{title}</h1>
        {subTitle && <div className="hl-sub">{subTitle}</div>}
        {list}
      </div>
    </div>
  );
}

export function SchedulesPage({ remote, go, sidebar = false, selected }: { remote: Remote; go: Go; sidebar?: boolean; selected?: string }) {
  const host = useCoreHost(remote, go);
  const vm = useSchedulesView(host);
  const [rearmKey, setRearmKey] = useState("");
  const rearmForm = useScheduleFormView(host, rearmKey);

  useEffect(() => {
    if (sidebar) return;
    host.scheduleListOpen(true);
    return () => host.scheduleListOpen(false);
  }, [host, sidebar]);
  useEffect(() => host.onScheduleFormOpenTiming("", (formKey) => setRearmKey(formKey)), [host]);
  useEffect(() => (rearmKey ? host.onScheduleFormClose(rearmKey, () => setRearmKey("")) : undefined), [host, rearmKey]);

  const actions: ScheduleListActions = {
    onSelectTask: (mid, tid) => go(routeForTask(mid, tid)),
    onCreateTask: () => go("tasks/new"),
    onToggleEnded: () => host.scheduleToggleEnded(),
    onMarkAllRead: () => host.scheduleMarkRead([], true),
    onMarkRead: (mid, tid) => host.scheduleMarkRead([{ machineId: mid, taskId: tid }], false),
    onMarkUnread: (mid, tid, leaving = false) => { host.scheduleMarkUnread(mid, tid, leaving); if (leaving) go("tasks"); },
    onOperate: (mid, tid, op) => host.scheduleOperate(mid, tid, op),
    onRetryMachine: (mid) => host.scheduleRefresh(mid),
  };
  return (
    <>
      <SchedulesListView vm={vm} sidebar={sidebar} selected={selected} actions={actions} />
      {rearmKey && rearmForm.open && rearmForm.mode === ScheduleFormMode.EDIT_TIMING && rearmForm.timing && (
        <TimingSheetForm host={host} form={rearmForm} submitOnApply onClose={() => { host.scheduleFormClose(rearmForm.formKey, true); setRearmKey(""); }} />
      )}
    </>
  );
}

// --- Timing Sheet ---

export type TimingUpdatePatch = Omit<Parameters<CoreHost["scheduleFormUpdateTiming"]>[0], "formKey">;

export function TimingSheetView({ draft, formError, onUpdate, onApplied, onClose }: {
  draft: ScheduleTimingDraftView; formError?: string; onUpdate: (patch: TimingUpdatePatch) => void; onApplied?: () => void; onClose: () => void;
}) {
  const [zoneValid, setZoneValid] = useState(() => isValidZone(draft.zone));
  const [untilLocal, setUntilLocal] = useState(() => draft.untilMs > 0n ? formatZonedDatetimeLocal(draft.untilMs, draft.zone) : draft.untilDate ? `${draft.untilDate}T23:59` : "");
  const [untilDirty, setUntilDirty] = useState(false);
  const [waitingApply, setWaitingApply] = useState(false);

  useEffect(() => { setZoneValid(isValidZone(draft.zone)); }, [draft.zone]);
  useEffect(() => {
    if (draft.untilMs > 0n && isValidZone(draft.zone)) {
      setUntilLocal(formatZonedDatetimeLocal(draft.untilMs, draft.zone));
      setUntilDirty(false);
    }
  }, [draft.untilMs, draft.zone]);
  useEffect(() => {
    if (!waitingApply) return;
    if (draft.timingError) setWaitingApply(false);
    else if (draft.timingApplied) { setWaitingApply(false); onApplied?.(); }
  }, [waitingApply, draft.timingApplied, draft.timingError, onApplied]);

  const kind = draft.kind || ScheduleTimingKind.DAILY;
  const onceValue = draft.date && draft.time ? `${draft.date}T${draft.time}` : draft.date || "";
  const errText = formError || renderText(draft.timingError);
  const changeZone = (nextZone: string) => {
    const ok = isValidZone(nextZone);
    setZoneValid(ok);
    const resolvedDate = ok && draft.end === ScheduleTimingEnd.DATE && draft.untilMs > 0n ? formatZonedDate(draft.untilMs, nextZone) : undefined;
    if (resolvedDate) { setUntilLocal(formatZonedDatetimeLocal(draft.untilMs, nextZone)); setUntilDirty(false); }
    onUpdate({ zone: nextZone, ...(resolvedDate ? { untilDate: resolvedDate, untilMs: draft.untilMs } : {}) });
  };
  const apply = () => {
    const validZone = isValidZone(draft.zone);
    setZoneValid(validZone);
    const atRes = kind === ScheduleTimingKind.ONCE ? resolveZonedWall(draft.date, draft.time, draft.zone) : undefined;
    let untilMs: bigint | undefined = draft.untilMs > 0n ? draft.untilMs : undefined;
    let untilDstGap = false;
    if (kind !== ScheduleTimingKind.ONCE && draft.end === ScheduleTimingEnd.DATE && (untilDirty || !untilMs)) {
      const [uDate = "", uTime = "23:59"] = untilLocal.split("T");
      const res = resolveZonedWall(uDate, uTime, draft.zone);
      untilDstGap = !!res.gap;
      untilMs = res.ms;
    }
    setWaitingApply(true);
    onUpdate({
      zoneValid: validZone,
      ...(atRes?.ms !== undefined ? { atMs: atRes.ms } : {}),
      atDstGap: !!atRes?.gap,
      ...(untilMs !== undefined ? { untilMs } : {}),
      untilDstGap,
      apply: true,
    });
  };
  const kindOptions = [
    { value: String(ScheduleTimingKind.ONCE), label: t("schedule.once_schedules") },
    { value: String(ScheduleTimingKind.DAILY), label: t("schedule.daily") },
    { value: String(ScheduleTimingKind.WEEKLY), label: t("schedule.weekly_on") },
    { value: String(ScheduleTimingKind.INTERVAL), label: t("schedule.every") },
    ...(kind === ScheduleTimingKind.AFTER_TURN ? [{ value: String(ScheduleTimingKind.AFTER_TURN), label: t("schedule.after_turn_ends") }] : []),
  ];
  const unitOptions: Array<[ScheduleIntervalUnit, string]> = [
    [ScheduleIntervalUnit.SECONDS, t("schedule.android_seconds")], [ScheduleIntervalUnit.MINUTES, t("schedule.android_minutes")],
    [ScheduleIntervalUnit.HOURS, t("schedule.android_hours")], [ScheduleIntervalUnit.DAYS, t("schedule.android_days")],
  ];

  const selectedJitter = draft.jitterOptions.find((o) => o.selected) ?? draft.jitterOptions[0];
  const timingSnap = JSON.stringify({
    k: kind, d: draft.date, tm: draft.time, w: draft.weekdays, iv: draft.interval,
    u: draft.unit, z: draft.zone, wd: draft.workdays, e: draft.end, ud: draft.untilDate,
    um: String(draft.untilMs), mc: draft.count, jm: String(selectedJitter?.minutes ?? 0n),
  });
  const [initialTimingSnap] = useState(timingSnap);

  return (
    <Sheet onClose={onClose} dirty={timingSnap !== initialTimingSnap} className="timing-sheet" title={t("schedule.when")}
      action={{ label: t("common.done"), onClick: apply, disabled: !draft.timingDoneEnabled || (kind === ScheduleTimingKind.WEEKLY && draft.weekdays.length === 0) }}>
      <Segmented className="full" label={t("schedule.frequency")} value={String(kind)} onChange={(v) => onUpdate({ kind: Number(v) as ScheduleTimingKind })} options={kindOptions} />
      <div className="schedule-inputs">
        {kind === ScheduleTimingKind.ONCE && (
          <label>{t("schedule.date_and_time")}<input type="datetime-local" value={onceValue} onChange={(e) => { const [d = "", tm = ""] = e.target.value.split("T"); onUpdate({ date: d, time: tm }); }} /></label>
        )}
        {(kind === ScheduleTimingKind.DAILY || kind === ScheduleTimingKind.WEEKLY) && (
          <label>{t("schedule.time")}<input type="time" value={draft.time} onChange={(e) => onUpdate({ time: e.target.value })} /></label>
        )}
        {draft.showJitter && draft.jitterOptions.length > 0 && (
          <>
            <label>{renderText(draft.jitterLabel)}
              <select aria-label={renderText(draft.jitterLabel)} value={String(selectedJitter.minutes)}
                onChange={(e) => onUpdate({ jitterMinutes: BigInt(e.target.value) })}>
                {draft.jitterOptions.map((o) => <option key={String(o.minutes)} value={String(o.minutes)}>{renderText(o.label)}</option>)}
              </select>
            </label>
            {draft.jitterHint && <p className="hint">{renderText(draft.jitterHint)}</p>}
          </>
        )}
        {kind === ScheduleTimingKind.DAILY && (
          <label className="check-field">{t("schedule.weekdays_only")}<input type="checkbox" checked={draft.workdays} onChange={(e) => onUpdate({ workdays: e.target.checked })} /></label>
        )}
        {kind === ScheduleTimingKind.WEEKLY && (
          <div className="weekdays">
            {[1, 2, 3, 4, 5, 6, 7].map((d) => {
              const pressed = draft.weekdays.includes(d);
              return (
                <button key={d} aria-pressed={pressed} className={pressed ? "on" : ""}
                  onClick={() => onUpdate({ replaceWeekdays: true, weekdays: pressed ? draft.weekdays.filter((x) => x !== d) : [...draft.weekdays, d] })}>
                  {weekdayShortLabel(d)}
                </button>
              );
            })}
          </div>
        )}
        {kind === ScheduleTimingKind.INTERVAL && (
          <>
            <label>{t("schedule.android_interval")}<input type="number" min="1" step="1" value={draft.interval} onChange={(e) => onUpdate({ interval: e.target.value })} /></label>
            <label>{t("schedule.android_unit")}
              <select aria-label={t("schedule.android_unit")} value={String(draft.unit || ScheduleIntervalUnit.HOURS)} onChange={(e) => onUpdate({ unit: Number(e.target.value) as ScheduleIntervalUnit })}>
                {unitOptions.map(([val, label]) => <option key={val} value={String(val)}>{label}</option>)}
              </select>
            </label>
            {draft.showIntervalHint && draft.intervalHint && <p className="hint">{renderText(draft.intervalHint)}</p>}
          </>
        )}
        {kind === ScheduleTimingKind.AFTER_TURN && draft.afterTurnHint && <p>{renderText(draft.afterTurnHint)}</p>}
        {kind !== ScheduleTimingKind.ONCE && (
          <>
            <label>{t("session.end")}
              <select aria-label={t("session.end")} value={String(draft.end || ScheduleTimingEnd.NEVER)} onChange={(e) => onUpdate({ end: Number(e.target.value) as ScheduleTimingEnd })}>
                <option value={String(ScheduleTimingEnd.NEVER)}>{t("schedule.never")}</option>
                <option value={String(ScheduleTimingEnd.COUNT)}>{t("schedule.after_a_run_count")}</option>
                <option value={String(ScheduleTimingEnd.DATE)}>{t("schedule.end_date")}</option>
              </select>
            </label>
            {draft.end === ScheduleTimingEnd.COUNT && (
              <label>{t("schedule.count")}<input type="number" min="1" value={draft.count} onChange={(e) => onUpdate({ count: e.target.value })} /></label>
            )}
            {draft.end === ScheduleTimingEnd.DATE && (
              <label>{t("schedule.end_time")}
                <input type="datetime-local" value={untilLocal} onChange={(e) => {
                  const v = e.target.value;
                  setUntilLocal(v); setUntilDirty(true);
                  const [d = "", tm = "23:59"] = v.split("T");
                  const res = resolveZonedWall(d, tm, draft.zone);
                  onUpdate({ untilDate: d, ...(res.ms !== undefined ? { untilMs: res.ms } : {}) });
                }} />
              </label>
            )}
          </>
        )}
        {kind !== ScheduleTimingKind.INTERVAL && (
          <label>{t("schedule.time_zone")}<input aria-label={t("schedule.time_zone")} aria-invalid={!zoneValid} value={draft.zone} onChange={(e) => changeZone(e.target.value)} placeholder="Asia/Shanghai" /></label>
        )}
      </div>
      {errText && <Banner kind="err">{errText}</Banner>}
      <div className="hint">{t("schedule.tasks_do_not_run_while_the_computer_sleeps_or")}</div>
    </Sheet>
  );
}

function TimingSheetForm({ host, form, submitOnApply = false, onApplied, onClose }: {
  host: CoreHost; form: ScheduleFormViewModel; submitOnApply?: boolean; onApplied?: () => void; onClose: () => void;
}) {
  if (!form.timing) return null;
  return (
    <TimingSheetView draft={form.timing} formError={renderText(form.mutation?.error)}
      onUpdate={(patch) => host.scheduleFormUpdateTiming({ formKey: form.formKey, ...patch })}
      onApplied={() => (submitOnApply ? host.scheduleFormSubmit(form.formKey, true) : onApplied?.())} onClose={onClose} />
  );
}

// --- Shared Target & Config Modals ---

function ScheduleConfigSheet({ vm, focusMode = false, onSelectMachine, onSelectModel, onRetryModels, onDone, onClose }: {
  vm: ScheduleFormViewModel; focusMode?: boolean; onSelectMachine?: (machineId: string) => void;
  onSelectModel: (model: string, effort: string, mode?: ApprovalMode) => void; onRetryModels: () => void; onDone?: () => void; onClose: () => void;
}) {
  const [pickingMachine, setPickingMachine] = useState(false);
  const panel = vm.modelPanel, enabled = !!panel?.panelEnabled, busy = !!vm.mutation?.busy;
  if (pickingMachine && onSelectMachine) {
    return (
      <Sheet onClose={() => setPickingMachine(false)} title={t("common.machine")} action={{ label: t("common.cancel"), onClick: () => setPickingMachine(false) }}>
        <div className="list">
          {vm.machineChoices.map((m) => (
            <ChoiceRow key={m.machineId} label={<b>{m.name}</b>} selected={m.selected} disabled={!m.supported || m.selected || busy}
              onClick={() => { onSelectMachine(m.machineId); setPickingMachine(false); }} />
          ))}
        </div>
      </Sheet>
    );
  }
  const modelChoices = [
    ...(panel?.defaultModelLabel ? [{ value: "", label: renderText(panel.defaultModelLabel), selected: !!panel.defaultModelChecked, meta: "" }] : []),
    ...(panel?.models ?? []).map((m) => ({ value: m.slug, label: m.displayName, selected: m.checked, meta: renderText(m.fixedEffortNote) })),
  ];
  const errText = renderText(panel?.modelsError) || renderText(vm.mutation?.error);
  return (
    <Sheet onClose={() => !busy && onClose()} dirty={!!onDone && vm.hasUnsavedChanges} title={focusMode ? t("new_session.permissions") : t("schedule.model_and_permissions")}
      action={{ label: onDone ? (renderText(vm.submitLabel) || t("common.save")) : t("common.done"), disabled: busy || (!!onDone && !vm.canSubmit), onClick: onDone ?? onClose }}>
      {!focusMode && vm.machineSelectable && vm.machineChoices.length > 1 && onSelectMachine && (
        <div className="list" style={{ marginBottom: 10 }}>
          <button className="mi" disabled={!vm.editable || busy} onClick={() => setPickingMachine(true)}>
            <span className="mt">{t("common.machine")}</span><span className="v">{vm.machineName}</span><ChevronRight className="chev" />
          </button>
        </div>
      )}
      {vm.modeChoices.length > 0 && (
        <>
          {!focusMode && <div className="label">{t("new_session.permissions")}</div>}
          <div className="list">
            {vm.modeChoices.map((mc) => (
              <ChoiceRow key={mc.mode} label={renderText(mc.label)} sub={!mc.enabled ? renderText(mc.disabledReason) : undefined}
                selected={mc.selected} disabled={!mc.enabled || !vm.editable || busy} danger={mc.isDanger} onClick={() => onSelectModel(vm.model, vm.effort, mc.mode)} />
            ))}
          </div>
        </>
      )}
      {!focusMode && panel && (
        <>
          <div className="label">{t("models.title")}</div>
          {panel.modelsLoading && panel.models.length === 0 ? (
            <p className="home-note"><LoaderCircle className="spin" />{t("common.loading")}</p>
          ) : (
            <div className="list">
              {modelChoices.map((m) => (
                <ChoiceRow key={m.value || "__default"} label={m.label} sub={m.meta} selected={m.selected} disabled={!enabled}
                  onClick={() => onSelectModel(m.value, vm.effort)} />
              ))}
            </div>
          )}
          {panel.effortChoices.length > 0 && (
            <div className="schedule-inputs" style={{ marginTop: 10 }}>
              <Segmented label={t("models.effort")} value={panel.selectedEffort} onChange={(effort) => onSelectModel(vm.model, effort)}
                options={panel.effortChoices.map((v) => ({ value: v, label: v, disabled: !enabled }))} />
            </div>
          )}
          {errText && <Banner kind="warn" action={t("common.retry")} onAction={onRetryModels}>{errText}</Banner>}
        </>
      )}
      {focusMode && errText && <Banner kind="err">{errText}</Banner>}
    </Sheet>
  );
}

const SCHEDULE_FORM_PICKER_KEY = "schedule-form";

function ScheduleProjectSheet({ host, vm, onPickProject, onClose }: {
  host: CoreHost; vm: ScheduleFormViewModel; onPickProject: (projectKey: string, machineId: string) => void; onClose: () => void;
}) {
  const manage = useProjectView(host, "", true, 30, "", SCHEDULE_FORM_PICKER_KEY).manage;
  const [step, setStep] = useState<"project" | "dir">("project");
  useEffect(() => {
    if (step !== "dir") return;
    host.projectOpenDirPicker(false, vm.lockMachine ? vm.machineId : "", "", SCHEDULE_FORM_PICKER_KEY);
    return () => host.projectCloseDirPicker(SCHEDULE_FORM_PICKER_KEY);
  }, [host, step, vm.lockMachine, vm.machineId]);

  if (step === "dir" && manage?.dirPicker) {
    return (
      <DirPickerSheet picker={manage.dirPicker} onBack={() => setStep("project")} onClose={onClose} onNavigate={(mid, path) => host.projectDirNavigate(mid, path, SCHEDULE_FORM_PICKER_KEY)}
        onConfirm={(path) => {
          const mid = manage.dirPicker?.machineId || vm.machineId;
          if (mid && host.remote) host.remote.draftMachines.set(path, mid);
          host.projectAdd(path, SCHEDULE_FORM_PICKER_KEY);
          onPickProject(path, mid);
          onClose();
        }} />
    );
  }
  if (vm.lockMachine || vm.projectChoices.length > 0) {
    return (
      <Sheet onClose={onClose} title={t("projects.choose")} action={{ label: t("common.cancel"), onClick: onClose }}>
        <div className="list">
          {vm.projectChoices.map((p) => (
            <ChoiceRow key={p.projectKey} label={<b>{renderText(p.name)}</b>} sub={p.shortPath} selected={p.selected} end={p.selected ? "check" : "chev"}
              onClick={() => { onPickProject(p.projectKey, ""); onClose(); }} />
          ))}
          <ChoiceRow label={t("projects.new")} end="chev" onClick={() => setStep("dir")} />
        </div>
      </Sheet>
    );
  }
  return (
    <Sheet onClose={onClose} title={t("projects.choose")} action={{ label: t("common.cancel"), onClick: onClose }}>
      {manage?.listing && (
        <ProjectList listing={manage.listing} onSearch={host.projectSetSearch} onNew={() => setStep("dir")}
          item={(p) => <ProjectItem key={p.key} p={p} onClick={() => { onPickProject(p.key, ""); onClose(); }} end={<ChevronRight className="chev" />} />} />
      )}
    </Sheet>
  );
}

function TargetFields({ vm, showMachine = false, onOpenProject, onSelectAgent, onOpenConfig }: {
  vm: ScheduleFormViewModel; showMachine?: boolean; onOpenProject: () => void; onSelectAgent?: (a: Agent) => void; onOpenConfig: () => void;
}) {
  const modelPermLabel = [
    ...(showMachine ? [vm.machineName] : []),
    renderText(vm.modelLabel) || t("new_session.default_model"),
    renderText(vm.approvalModeLabel) || t("schedule.select_permissions"),
  ].filter(Boolean).join(" · ");
  return (
    <>
      <Field name={t("schedule.project")} value={renderText(vm.projectName) || t("schedule.android_choose_a_project")} onClick={onOpenProject} />
      {vm.hasProject && (
        <>
          <Field name="CLI" value={vm.availableAgents.length > 1 && onSelectAgent
            ? <Segmented label="CLI" value={String(vm.agent)} onChange={(v) => onSelectAgent(Number(v) as Agent)} options={vm.availableAgents.map((a) => ({ value: String(a.agent), label: a.displayName }))} />
            : vm.agentName} />
          <Field name={showMachine ? t("schedule.computer_and_model") : t("schedule.model_and_permissions")} value={modelPermLabel} onClick={onOpenConfig} />
        </>
      )}
    </>
  );
}

function ScheduleTargetEditorSheet({ host, vm, onClose }: { host: CoreHost; vm: ScheduleFormViewModel; onClose: () => void }) {
  const [sub, setSub] = useState<"" | "project" | "config">(vm.mode === ScheduleFormMode.EDIT_MODE ? "config" : "");
  const [confirmFull, setConfirmFull] = useState(false);
  const busy = !!vm.mutation?.busy, errText = renderText(vm.mutation?.error);
  const submit = () => (vm.requiresFullAccessConfirm ? setConfirmFull(true) : host.scheduleFormSubmit(vm.formKey, false));
  const confirmModal = confirmFull && <FullAccessAlert onCancel={() => setConfirmFull(false)} onConfirm={() => { setConfirmFull(false); host.scheduleFormSubmit(vm.formKey, true); }} />;

  if (sub === "project") return <ScheduleProjectSheet host={host} vm={vm} onPickProject={(key, mid) => host.scheduleFormSelectProject(vm.formKey, key, mid)} onClose={() => setSub("")} />;
  if (sub === "config") {
    return (
      <>
        <ScheduleConfigSheet vm={vm} focusMode={vm.mode === ScheduleFormMode.EDIT_MODE}
          onSelectModel={(model, effort, mode) => host.scheduleFormSelectModel(vm.formKey, model, effort, mode)}
          onRetryModels={() => host.scheduleFormRetryModels(vm.formKey)}
          onDone={vm.mode === ScheduleFormMode.EDIT_MODE ? submit : undefined}
          onClose={() => (vm.mode === ScheduleFormMode.EDIT_MODE ? onClose() : setSub(""))} />
        {confirmModal}
      </>
    );
  }
  return (
    <Sheet onClose={() => !busy && onClose()} dirty={vm.hasUnsavedChanges} title={t("schedule.run_in")} action={{ label: renderText(vm.submitLabel) || t("common.save"), disabled: busy || !vm.editable || !vm.canSubmit, onClick: submit }}>
      <div className="grp">
        <TargetFields vm={vm} onOpenProject={() => setSub("project")} onSelectAgent={(a) => host.scheduleFormSelectAgent(vm.formKey, a)} onOpenConfig={() => setSub("config")} />
      </div>
      <p className="hint">{t("schedule.only_changes_this_task_it_still_runs_on_the")}</p>
      {errText && <Banner kind="err">{errText}</Banner>}
      {confirmModal}
    </Sheet>
  );
}

// --- Detail View ---

function TaskNameDialog({ vm, onSetTitle, onSave, onClose }: { vm: ScheduleFormViewModel; onSetTitle: (title: string) => void; onSave: () => void; onClose: () => void }) {
  const input = useRef<HTMLInputElement>(null), root = useRef<HTMLDivElement>(null);
  const busy = !!vm.mutation?.busy, errText = renderText(vm.mutation?.error);
  useEscape(() => !busy && onClose());
  useOverlayExit(root);
  useDialogFocus(root);
  useEffect(() => { input.current?.focus(); input.current?.select(); }, []);
  return (
    <div className="overlay" ref={root}>
      <div className="dim" onClick={() => !busy && onClose()} />
      <form className="dialog rename" role="dialog" aria-modal="true" aria-label={t("schedule.rename_task")}
        onSubmit={(e) => { e.preventDefault(); if (!busy) onSave(); }}>
        <h5>{t("schedule.rename_task")}</h5>
        <input ref={input} className="field" aria-label={t("schedule.task_name")} placeholder={t("schedule.leave_blank_to_use_the_message_s_first_sentence")}
          enterKeyHint="done" value={vm.title} onChange={(e) => onSetTitle(e.target.value)} />
        {errText ? <p className="red">{t("settings.save_error", { reason: errText })}</p> : <p>{t("schedule.saving_blank_uses_the_message_s_first_sentence")}</p>}
        <div className="btns">
          <button type="button" className="btn" disabled={busy} onClick={onClose}>{t("common.cancel")}</button>
          <button type="submit" className="btn pri" disabled={busy}>{busy ? t("common.saving") : t("common.save")}</button>
        </div>
      </form>
    </div>
  );
}

export function ScheduleResultCard({ card, onOpenSession, onShowLatestResult }: {
  card: ScheduleResultCardView; onOpenSession: (sessionId: string, runId: string, firstSeq: bigint) => void; onShowLatestResult: () => void;
}) {
  const [expanded, setExpanded] = useState(false), [long, setLong] = useState(false), body = useRef<HTMLDivElement>(null);
  const text = card.markdownBody || renderText(card.fallbackReasonText);
  useEffect(() => {
    const el = body.current;
    if (!el) return;
    const measure = () => {
      const children = [...el.children] as HTMLElement[];
      children.forEach((c) => { c.hidden = false; c.classList.remove("schedule-first-block-clipped"); });
      if (expanded || matchMedia("(min-width: 900px)").matches) { setLong(false); return; }
      const limit = (parseFloat(getComputedStyle(el).lineHeight) || 24) * 8;
      el.style.setProperty("--schedule-result-limit", `${limit}px`);
      let height = 0, cut = false;
      for (const [index, child] of children.entries()) {
        height += child.getBoundingClientRect().height + (parseFloat(getComputedStyle(child).marginTop) || 0);
        if (height > limit) cut = true;
        child.hidden = cut && index > 0;
        if (cut && index === 0) child.classList.add("schedule-first-block-clipped");
      }
      setLong(cut);
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => observer.disconnect();
  }, [text, expanded]);

  return (
    <>
      <section className="schedule-result">
        <div className="schedule-result-head">
          {card.unreadOnEntry && unreadDot(t("schedule.unread_on_entry"))}
          <span>{renderText(card.timeText)}</span>
          {card.showConversationAction && (
            <button onClick={() => onOpenSession(card.sessionId, card.runId, card.firstSeq)}>{t("schedule.conversation")}<ChevronRight /></button>
          )}
        </div>
        {card.resultTitle && <h4>{card.resultTitle}</h4>}
        {text && (
          <>
            <div className="ai" ref={body}><Markdown text={text} /></div>
            {long && !expanded && <button className="schedule-expand" onClick={() => setExpanded(true)}>{t("markdown.expand")}<ChevronDown /></button>}
          </>
        )}
        {!text && card.resultLoading && <LoaderCircle className="spin" />}
      </section>
      {card.hasNewerResult && <button className="schedule-more" onClick={onShowLatestResult}>{t("home.ios_new_results")}</button>}
    </>
  );
}

export interface ScheduleDetailActions {
  onBack(): void;
  onOperate(op: ScheduleOperationKind): void;
  onMarkRead(): void;
  onMarkUnread(): void;
  onOpenSession(sessionId: string, runId?: string, firstSeq?: bigint): void;
  onShowLatestResult(): void;
  onToggleQuietGroup(groupKey: string): void;
  onShowAllRuns(): void;
  onOpenRun(runId: string): void;
  onSetNotify(value: boolean): void;
  onRetryMachine(): void;
  onOpenEdit(mode: ScheduleFormMode): void;
  onRetryMutation(): void;
  onAbandonMutation(): void;
  onOpenMachineSettings?(): void;
}

const ATTN_BANNER_CLS = "schedule-attention";
const AGENT_DISABLED_PRE_RUN_KEY = "schedule.agent_disabled_pre_run";

export function ScheduleDetailView({ vm, actions }: { vm: ScheduleDetailViewModel; actions: ScheduleDetailActions }) {
  const [menuOpen, setMenuOpen] = useState(false), moreRef = useRef<HTMLButtonElement>(null);
  if (vm.loading && !vm.found) {
    return (
      <div className="page schedules-page schedule-detail">
        <NavBar title={t("queue.android_scheduled")} onBack={actions.onBack} />
        <div className="body schedule-body"><p className="home-note" data-late="1"><LoaderCircle className="spin" />{t("schedule.reading_tasks")}</p></div>
      </div>
    );
  }
  if (!vm.found) {
    return (
      <div className="page schedules-page schedule-detail">
        <NavBar title={t("queue.android_scheduled")} onBack={actions.onBack} />
        <div className="body schedule-body">
          <p className="home-note">
            {renderText(vm.notFoundText)}
            {vm.canRetryMachineList && <> <button className="link" onClick={actions.onRetryMachine}>{t("common.retry")}</button></>}
          </p>
        </div>
      </div>
    );
  }
  const busy = !!vm.mutation?.busy, frozen = !!vm.mutation?.frozen;
  const mutErr = renderText(vm.mutation?.error), machineErr = renderText(vm.machineError), takeover = vm.takeover;
  const canEditDef = !!vm.actions?.canEditDefinition && !busy && !frozen;
  const canRename = !!vm.actions?.canRename && !busy && !frozen;
  const canToggleNotify = !!vm.actions?.canToggleNotify && !busy && !frozen;

  return (
    <div className="page schedules-page schedule-detail">
      <NavBar title={vm.title} sub={renderText(vm.subtitle)} onBack={actions.onBack}
        right={<button ref={moreRef} className="more" aria-label={t("common.ios_more")} onClick={() => setMenuOpen(true)}><Ellipsis /></button>} />
      <div className="body schedule-body schedule-detail-body">
        <div className="schedule-detail-content">
          {vm.readOnly && vm.readOnlyNotice && <Banner kind="warn">{renderText(vm.readOnlyNotice)}</Banner>}
          {machineErr && <Banner kind="err" action={vm.canRetryMachineList ? t("common.retry") : undefined} onAction={actions.onRetryMachine}>{machineErr}</Banner>}
          {frozen && <Banner kind="warn" action={t("schedule.retry_same_request")} onAction={actions.onRetryMutation}>{t("schedule.the_previous_operation_s_result_is_unconfirmed_verify_the")}</Banner>}
          {mutErr && !frozen && <Banner kind="err">{mutErr}</Banner>}
          {(vm.attention?.show || renderText(vm.attention?.message)) && (
            <>
              <div className={ATTN_BANNER_CLS}>
                <Banner
                  kind="warn"
                  action={vm.attention?.message?.key === AGENT_DISABLED_PRE_RUN_KEY && actions.onOpenMachineSettings ? t("schedule.enable_cli") : undefined}
                  onAction={vm.attention?.message?.key === AGENT_DISABLED_PRE_RUN_KEY ? actions.onOpenMachineSettings : undefined}
                >
                  <Info className="lead" />{renderText(vm.attention?.message)}
                </Banner>
              </div>
              {vm.attention?.show && vm.attention.showPrimaryAction && (
                <div className="schedule-recovery">
                  <button className="btn pri" disabled={!vm.attention.primaryActionEnabled || busy || frozen} onClick={() => actions.onOperate(ScheduleOperationKind.RECOVER)}>
                    {renderText(vm.attention.primaryActionLabel)}
                  </button>
                </div>
              )}
            </>
          )}
          {vm.activeRun?.hasActive && (
            <div className="schedule-result schedule-running">
              <div className="schedule-result-head">
                <span><LoaderCircle className="spin" />{renderText(vm.activeRun.statusText)}</span>
                {vm.activeRun.canOpenSession && vm.activeRun.sessionId && (
                  <button onClick={() => actions.onOpenSession(vm.activeRun!.sessionId, vm.activeRun!.runId, vm.activeRun!.firstSeq)}>{t("schedule.conversation")}<ChevronRight /></button>
                )}
              </div>
            </div>
          )}
          {vm.resultCard?.hasResult ? (
            <ScheduleResultCard card={vm.resultCard} onOpenSession={actions.onOpenSession} onShowLatestResult={actions.onShowLatestResult} />
          ) : !vm.activeRun?.hasActive && vm.earlierGroups.length === 0 ? (
            <p className="home-note">{t("schedule.has_not_run_yet")}</p>
          ) : null}

          {vm.earlierGroups.length > 0 && (
            <section className="schedule-history">
              <div className="label">{t("schedule.earlier")}<span>{renderText(vm.earlierCountText)}</span></div>
              <div className="grp">
                {vm.earlierGroups.map((g) => g.foldedQuiet && !g.expanded ? (
                  <button key={g.groupKey} className="row schedule-run" onClick={() => actions.onToggleQuietGroup(g.groupKey)}>
                    <span className="schedule-lead" />
                    <span className="schedule-run-text"><span className="quiet">{renderText(g.foldedTitle)}</span><small>{renderText(g.foldedSummary)}</small></span>
                    <Exp up={g.expanded} />
                  </button>
                ) : g.runs.map((r) => (
                  <button key={r.runId} className="row schedule-run" disabled={!r.canOpenSession} onClick={() => r.canOpenSession && actions.onOpenRun(r.runId)}>
                    <span className="schedule-lead">{r.unreadOnEntry ? unreadDot(t("schedule.unread_on_entry")) : runStatusIcon(r.status, r.manual)}</span>
                    <span className="schedule-run-text"><time>{renderText(r.timeText)}</time><small className={r.notified ? "" : "quiet"}>{renderText(r.summaryText)}</small></span>
                    {r.canOpenSession && <ChevronRight />}
                  </button>
                )))}
                {vm.hasMoreEarlierRuns && !vm.showAllRuns && <button className="row act schedule-more" onClick={actions.onShowAllRuns}>{renderText(vm.viewAllText)}</button>}
              </div>
            </section>
          )}

          <div className="label">{t("schedule.what_should_the_agent_do")}<button className="link" disabled={!canEditDef} onClick={() => actions.onOpenEdit(ScheduleFormMode.EDIT_MESSAGE)}>{t("tools.android_edit")}</button></div>
          <section className="schedule-description"><ScheduleText key={vm.message} text={vm.message} lines={3} /></section>

          <div className="label">{t("common.settings")}</div>
          <div className="grp">
            <Field name={t("schedule.when")} value={renderText(vm.timingText)} disabled={!canEditDef} onClick={() => actions.onOpenEdit(ScheduleFormMode.EDIT_TIMING)} />
            {vm.hasLimits && <Field name={t("session.end")} value={renderText(vm.limitsText)} disabled={!canEditDef} onClick={() => actions.onOpenEdit(ScheduleFormMode.EDIT_TIMING)} />}
            <Field name={t("schedule.notify_on_completion")} value={<Switch label={t("schedule.notify_on_completion")} on={vm.notifyOnFinish} disabled={!canToggleNotify} onChange={actions.onSetNotify} />} />
            {vm.targetType === ScheduleTargetType.NEW_SESSION ? (
              <>
                <Field name={renderText(vm.targetLabel) || t("schedule.project")} value={renderText(vm.targetValue)} disabled={!canEditDef} onClick={() => actions.onOpenEdit(ScheduleFormMode.EDIT_TARGET)} />
                <Field name={t("new_session.permissions")} value={<span className={vm.permissionsIsDanger ? "red" : ""}>{renderText(vm.permissionsText)}</span>} disabled={!canEditDef} onClick={() => actions.onOpenEdit(ScheduleFormMode.EDIT_MODE)} />
              </>
            ) : (
              <Field name={renderText(vm.targetLabel) || t("schedule.conversation")} value={renderText(vm.targetValue)} disabled={!vm.canOpenTargetSession} onClick={() => vm.canOpenTargetSession && actions.onOpenSession(vm.targetSessionId)} />
            )}
            <Field name={t("schedule.task_name")} value={<span className="task-title">{vm.taskName}</span>} disabled={!canRename} onClick={() => actions.onOpenEdit(ScheduleFormMode.EDIT_RENAME)} />
          </div>

          {vm.creationFooter && <p className="foot">{renderText(vm.creationFooter)}</p>}
          {vm.runOnceFooter && <p className="foot">{renderText(vm.runOnceFooter)}</p>}
          {!vm.machineOnline && !vm.readOnly && vm.readOnlyNotice && <p className="foot">{renderText(vm.readOnlyNotice)}</p>}
          {vm.source === ScheduleSource.AGENT && takeover?.hasTakeover && (
            <p className="foot">{takeover.sessionId ? <button className="link" onClick={() => actions.onOpenSession(takeover.sessionId)}>{renderText(takeover.label)}</button> : renderText(takeover.label)}</p>
          )}
        </div>
      </div>
      {menuOpen && (
        <ScheduleMenu title={vm.title} subtitle={renderText(vm.menuSubtitle)} actions={vm.actions} mutation={vm.mutation} anchor={moreRef} onClose={() => setMenuOpen(false)}
          onRunNow={() => actions.onOperate(ScheduleOperationKind.RUN)}
          onToggle={() => actions.onOperate(vm.actions?.isResumeAction ? ScheduleOperationKind.RECOVER : ScheduleOperationKind.PAUSE)}
          onMarkRead={actions.onMarkRead} onMarkUnread={actions.onMarkUnread}
          onDelete={() => actions.onOperate(ScheduleOperationKind.CANCEL)} onAbandonMutation={actions.onAbandonMutation} />
      )}
    </div>
  );
}

export function ScheduleDetail({ remote, machineId, id, go, onBack }: { remote: Remote; machineId: string; id: string; go: Go; onBack: () => void }) {
  const host = useCoreHost(remote, go);
  const [earlierLimit, setEarlierLimit] = useState(0);
  const vm = useScheduleDetailView(host, machineId, id, earlierLimit);
  const [editMode, setEditMode] = useState<ScheduleFormMode | 0>(0);
  const [editFormKey, setEditFormKey] = useState("");
  const editForm = useScheduleFormView(host, editFormKey);

  useEffect(() => { host.scheduleDetailOpen(machineId, id); return () => host.scheduleDetailClose(machineId, id); }, [host, machineId, id]);
  useEffect(() => host.onScheduleFormOpenTiming("", (formKey) => {
    if (formKey.startsWith(`edit:${machineId}/${id}`)) { setEditFormKey(formKey); setEditMode(ScheduleFormMode.EDIT_TIMING); }
  }), [host, machineId, id]);
  useEffect(() => (editFormKey ? host.onScheduleFormClose(editFormKey, () => { setEditMode(0); setEditFormKey(""); }) : undefined), [host, editFormKey]);

  const closeEdit = () => { if (editFormKey) host.scheduleFormClose(editFormKey, true); setEditMode(0); setEditFormKey(""); };
  const detailActions: ScheduleDetailActions = {
    onBack,
    onOperate: (op) => host.scheduleOperate(machineId, id, op),
    onMarkRead: () => host.scheduleMarkRead([{ machineId, taskId: id }]),
    onMarkUnread: () => host.scheduleMarkUnread(machineId, id, true),
    onOpenSession: (sid, runId = "") => { if (sid) { if (runId && vm.resultCard?.runId === runId) host.scheduleDetailOpenRun(machineId, id, runId); else go(routeForSession(sid)); } },
    onShowLatestResult: () => host.scheduleDetailShowLatestResult(machineId, id),
    onToggleQuietGroup: (gk) => host.scheduleDetailToggleQuietGroup(machineId, id, gk),
    onShowAllRuns: () => { host.scheduleDetailShowAllRuns(machineId, id, true); setEarlierLimit(Math.max(100, vm.totalEarlierRuns)); },
    onOpenRun: (runId) => host.scheduleDetailOpenRun(machineId, id, runId),
    onSetNotify: (on) => host.scheduleSetNotify(machineId, id, on),
    onRetryMachine: () => host.scheduleRefresh(machineId),
    onOpenEdit: (mode) => { setEditFormKey(host.scheduleFormOpen(mode, { machineId, taskId: id })); setEditMode(mode); },
    onRetryMutation: () => host.scheduleRetryMutation(machineId, id, ""),
    onAbandonMutation: () => host.scheduleAbandonMutation(machineId, id, ""),
    onOpenMachineSettings: () => go(routeForMachineSettings(machineId)),
  };
  return (
    <>
      <ScheduleDetailView vm={vm} actions={detailActions} />
      {editMode === ScheduleFormMode.EDIT_RENAME && editForm.open && <TaskNameDialog vm={editForm} onSetTitle={(v) => host.scheduleFormSetTitle(editForm.formKey, v)} onSave={() => host.scheduleFormSubmit(editForm.formKey, false)} onClose={closeEdit} />}
      {editMode === ScheduleFormMode.EDIT_MESSAGE && editForm.open && <ScheduleMessageEditor vm={editForm} onSetMessage={(v) => host.scheduleFormSetMessage(editForm.formKey, v)} onSave={() => host.scheduleFormSubmit(editForm.formKey, false)} onClose={closeEdit} />}
      {editMode === ScheduleFormMode.EDIT_TIMING && editForm.open && <TimingSheetForm host={host} form={editForm} submitOnApply onClose={closeEdit} />}
      {(editMode === ScheduleFormMode.EDIT_TARGET || editMode === ScheduleFormMode.EDIT_MODE) && editForm.open && <ScheduleTargetEditorSheet host={host} vm={editForm} onClose={closeEdit} />}
    </>
  );
}

// --- Create Form ---

export interface ScheduleFormActions {
  onSetMessage(value: string): void;
  onSetTitle(value: string): void;
  onSetNotify(value: boolean): void;
  onOpenTiming(): void;
  onOpenProjectPicker?(): void;
  onSelectAgent?(agent: Agent): void;
  onOpenConfig?(): void;
  onSubmit(allowFullAccess?: boolean): void;
  onRetryMutation(): void;
  onAbandonMutation(): void;
}

export function ScheduleFormView({ vm, sessionScoped = false, actions, children }: {
  vm: ScheduleFormViewModel; sessionScoped?: boolean; actions: ScheduleFormActions;
  children?: (parts: { rows: ReactNode; notes: ReactNode; button: ReactNode }) => ReactNode;
}) {
  const [confirmFull, setConfirmFull] = useState(false);
  const busy = !!vm.mutation?.busy, frozen = !!vm.mutation?.frozen, errText = renderText(vm.mutation?.error);
  const titleHint = renderText(vm.titlePlaceholder) || t("schedule.leave_blank_to_use_the_message_s_first_sentence");
  const isCreate = vm.mode === ScheduleFormMode.CREATE_NEW || vm.mode === ScheduleFormMode.CREATE_FOR_SESSION || vm.mode === ScheduleFormMode.UNSPECIFIED;
  const submit = () => (vm.requiresFullAccessConfirm ? setConfirmFull(true) : actions.onSubmit(false));

  const rows = (
    <>
      <label className="field big schedule-prompt">
        <small>{sessionScoped ? t("composer.message") : t("schedule.what_should_the_agent_do")}</small>
        <ScheduleTextarea aria-label={t("schedule.what_should_the_agent_do")} placeholder={t("schedule.describe_the_scheduled_work_and_ask_the_agent_to")}
          value={vm.message} onChange={(e) => actions.onSetMessage(e.target.value)} />
      </label>
      {vm.messageTooLong && <p className="gf red">{renderText(vm.messageError) || t("schedule.too_long_maximum_32_kb")}</p>}
      <label className="field schedule-name" data-name-hint={titleHint}>
        <small>{t("schedule.task_name")}</small>
        <input aria-label={t("schedule.task_name")} placeholder={titleHint} value={vm.title} onChange={(e) => actions.onSetTitle(e.target.value)} />
      </label>
      <div className="grp">
        {!sessionScoped && <Field name={t("schedule.notify_on_completion")} value={<Switch label={t("schedule.notify_on_completion")} on={vm.notifyOnFinish} onChange={actions.onSetNotify} />} />}
        <Field name={t("schedule.when")} value={renderText(vm.timingSummary)} onClick={actions.onOpenTiming} />
        {sessionScoped && <Field name={t("schedule.notify_on_completion")} value={<Switch label={t("schedule.notify_on_completion")} on={vm.notifyOnFinish} onChange={actions.onSetNotify} />} />}
        {!sessionScoped && actions.onOpenProjectPicker && actions.onOpenConfig && (
          <TargetFields vm={vm} showMachine={vm.machineChoices.length > 1 || vm.machineSelectable}
            onOpenProject={actions.onOpenProjectPicker} onSelectAgent={actions.onSelectAgent} onOpenConfig={actions.onOpenConfig} />
        )}
      </div>
    </>
  );
  const notes = (
    <>
      {vm.notices.map((n, i) =>
        n.kind === ScheduleFormNoticeKind.TARGET_INFO_HINT ? (
          <p className="gf" key={i}>{renderText(n.text)} {t("schedule.notify_on_completion_by_default_when_off_only_the")}</p>
        ) : n.kind === ScheduleFormNoticeKind.MODE_REQUIRED ? (
          <p className="gf" key={i}>{renderText(n.text)}</p>
        ) : (
          <Banner key={i} kind={noticeKind(n.severity)}>{renderText(n.text)}</Banner>
        ),
      )}
      {frozen && (
        <Banner kind="warn" action={t("schedule.retry_same_request")} onAction={actions.onAbandonMutation}>
          {t("schedule.the_previous_creation_result_is_unconfirmed_retrying_uses_the")}{isCreate ? " " : ""}
        </Banner>
      )}
      {errText && !frozen && <Banner kind="err">{errText}</Banner>}
      {confirmFull && <FullAccessAlert onCancel={() => setConfirmFull(false)} onConfirm={() => { setConfirmFull(false); actions.onSubmit(true); }} />}
    </>
  );
  const button = (
    <button className={"btn pri" + (sessionScoped ? "" : " sm")} disabled={busy || !vm.editable || !vm.canSubmit} onClick={submit}>
      {busy ? t("schedule.creating") : frozen ? t("common.retry") : renderText(vm.submitLabel) || t("schedule.create")}
    </button>
  );
  return children ? <>{children({ rows, notes, button })}</> : <>{rows}{notes}{button}</>;
}

export function ScheduleCreate({ remote, go, onBack, sessionId }: { remote: Remote; go: Go; onBack: () => void; sessionId?: string }) {
  const host = useCoreHost(remote, go);
  const [formKey, setFormKey] = useState("");
  const vm = useScheduleFormView(host, formKey);
  const [timingOpen, setTimingOpen] = useState(false), [choosingProject, setChoosingProject] = useState(false), [configOpen, setConfigOpen] = useState(false);
  const sessionScoped = !!sessionId;

  useEffect(() => {
    setFormKey(host.scheduleFormOpen(sessionId ? ScheduleFormMode.CREATE_FOR_SESSION : ScheduleFormMode.CREATE_NEW, { sessionId }));
  }, [host, sessionId]);
  useEffect(() => {
    if (!formKey) return;
    const offClose = host.onScheduleFormClose(formKey, onBack), offTiming = host.onScheduleFormOpenTiming(formKey, () => setTimingOpen(true));
    return () => { offClose(); offTiming(); };
  }, [host, formKey, onBack]);

  const actions: ScheduleFormActions = {
    onSetMessage: (v) => host.scheduleFormSetMessage(vm.formKey, v),
    onSetTitle: (v) => host.scheduleFormSetTitle(vm.formKey, v),
    onSetNotify: (v) => host.scheduleFormSetNotify(vm.formKey, v),
    onOpenTiming: () => setTimingOpen(true),
    onOpenProjectPicker: () => setChoosingProject(true),
    onSelectAgent: (a) => host.scheduleFormSelectAgent(vm.formKey, a),
    onOpenConfig: () => setConfigOpen(true),
    onSubmit: (allowFull = false) => host.scheduleFormSubmit(vm.formKey, allowFull),
    onRetryMutation: () => host.scheduleRetryMutation(vm.machineId, "", vm.formKey),
    onAbandonMutation: () => host.scheduleAbandonMutation(vm.machineId, "", vm.formKey),
  };
  return (
    <ScheduleFormView vm={vm} sessionScoped={sessionScoped} actions={actions}>
      {({ rows, notes, button }) => (
        <>
          {sessionScoped ? (
            <div className="schedule-session-form">
              <div className="sheet-head"><span className="side-act" /><b>{t("schedule.schedule_messages_to_this_conversation")}</b><button className="side-act" onClick={onBack}>{t("common.cancel")}</button></div>
              <div className="schedule-body">{rows}{notes}<div className="schedule-create-foot">{button}</div></div>
            </div>
          ) : (
            <div className="page rise schedules-page schedule-create">
              <header className="nt-hd"><button className="fbtn" aria-label={t("common.close")} onClick={onBack}><X /></button><b>{t("home.new_schedule")}</b>{button}</header>
              <div className="body schedule-body">{rows}{notes}</div>
            </div>
          )}
          {timingOpen && <TimingSheetForm host={host} form={vm} onApplied={() => setTimingOpen(false)} onClose={() => setTimingOpen(false)} />}
          {choosingProject && <ScheduleProjectSheet host={host} vm={vm} onPickProject={(k, m) => host.scheduleFormSelectProject(vm.formKey, k, m)} onClose={() => setChoosingProject(false)} />}
          {configOpen && (
            <ScheduleConfigSheet vm={vm} onSelectMachine={(m) => host.scheduleFormSelectMachine(vm.formKey, m)}
              onSelectModel={(m, e, md) => host.scheduleFormSelectModel(vm.formKey, m, e, md)} onRetryModels={() => host.scheduleFormRetryModels(vm.formKey)} onClose={() => setConfigOpen(false)} />
          )}
        </>
      )}
    </ScheduleFormView>
  );
}

// --- Session Strip & TimingSheet Wrapper ---

export function ScheduleStripView({ vm, onSelectTask }: { vm: ScheduleSessionStripViewModel; onSelectTask: (machineId: string, taskId: string) => void }) {
  const [choosing, setChoosing] = useState(false);
  if (!vm.visible || vm.tasks.length === 0) return null;
  return (
    <>
      <button className="schedule-strip glass" onClick={() => (vm.tasks.length === 1 ? onSelectTask(vm.tasks[0].machineId, vm.tasks[0].taskId) : setChoosing(true))}>
        <Clock /><span>{renderText(vm.summaryText)}</span><span className="act">{t("schedule.manage")}<ChevronRight /></span>
      </button>
      {choosing && (
        <Sheet onClose={() => setChoosing(false)} title={t("schedule.this_conversation_s_scheduled_tasks")}>
          <div className="list schedule-choices">
            {vm.tasks.map((task) => (
              <button key={`${task.machineId}/${task.taskId}`} className={"sess schedule-line" + (task.hasUnread ? " unread" : "")}
                onClick={() => { setChoosing(false); onSelectTask(task.machineId, task.taskId); }}>
                {task.hasUnread && <span className="gut">{unreadDot(t("home.ios_new_results"))}</span>}
                <span className="sm"><span className="row1"><b>{task.title}</b></span><span className="mc">{[renderText(task.timingText), renderText(task.nextRunText)].filter(Boolean).join(" · ")}</span></span>
              </button>
            ))}
          </div>
        </Sheet>
      )}
    </>
  );
}

export function ScheduleStrip({ remote, sessionId, go }: { remote: Remote; sessionId: string; go: Go }) {
  const vm = useScheduleSessionStripView(useCoreHost(remote, go), sessionId);
  return <ScheduleStripView vm={vm} onSelectTask={(mid, tid) => go(routeForTask(mid, tid))} />;
}

export function TimingSheet({ host, formKey = "", submitOnApply = false, onApplied, onClose }: {
  host: CoreHost; formKey?: string; submitOnApply?: boolean; onApplied?: () => void; onClose: () => void;
}) {
  return <TimingSheetForm host={host} form={useScheduleFormView(host, formKey)} submitOnApply={submitOnApply} onApplied={onApplied} onClose={onClose} />;
}
