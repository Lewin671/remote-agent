import { useEffect, useRef, useState, type ReactNode } from "react";
import * as l10n from "../i18n";
import { t } from "../i18n";
import { AgentMark } from "../agent-mark";
import { ApprovalMode, Agent } from "../gen/remoteagent/v1/event_pb";
import {
  MachineRingKind,
  MachineRingTone,
  MachineStatusDot,
  type MachineRingView,
} from "../gen/remoteagent/core/v1/machine_view_pb";
import {
  ConnectionGroupStage,
  type CliVersionView,
  type MachineSettingsViewModel,
} from "../gen/remoteagent/core/v1/settings_view_pb";
import {
  MachinePollMode,
  routeForMachineStatus,
  routeForMachineStorage,
  routeForProjects,
  routeForUsage,
  useCoreHost,
  useMachineSettingsView,
  useMachineStatusView,
  type Go,
} from "../core/host";
import { renderText } from "../core/text";
import type { Remote } from "../remote";
import { RejoinButton } from "../Rejoin";
import { DirectSettingsSheet } from "./ConnectionGroup";
import { DeviceActions } from "./DeviceManagement";
import { stageToMark } from "./direct-status";
import {
  Check,
  ChevronRight,
  CpuIcon,
  DiskIcon,
  Gauge,
  LoaderCircle,
  MemoryIcon,
} from "../icons";
import { Alert, Banner, NavBar, Sheet, StateMark, type Mark } from "../ui";

const RING_R = 23;
const RING_C = 2 * Math.PI * RING_R;

export function statusDotToMark(dot: MachineStatusDot): Mark | undefined {
  switch (dot) {
    case MachineStatusDot.GREEN_LIVE: return "online";
    case MachineStatusDot.GRAY_STATIC: return "off";
    case MachineStatusDot.ORANGE_STATIC: return "wait";
    default: return undefined;
  }
}

export function StatusDotMark({ dot, fallback }: { dot: MachineStatusDot; fallback?: Mark }) {
  if (dot === MachineStatusDot.RED_STATIC) {
    return <i className="mark err" aria-hidden />;
  }
  const kind = statusDotToMark(dot) ?? fallback;
  return kind ? <StateMark kind={kind} /> : null;
}

function ringToneClass(tone: MachineRingTone): string {
  switch (tone) {
    case MachineRingTone.WARN: return " warn";
    case MachineRingTone.CRITICAL: return " crit";
    default: return "";
  }
}

function ringIcon(kind: MachineRingKind) {
  switch (kind) {
    case MachineRingKind.CPU: return <CpuIcon />;
    case MachineRingKind.MEMORY: return <MemoryIcon />;
    case MachineRingKind.DISK: return <DiskIcon />;
    default: return null;
  }
}

function StatusRingTile({ r }: { r: MachineRingView }) {
  const ratio = r.hasValue ? Math.min(1, Math.max(0, r.fraction)) : 0;
  const fill = ratio * RING_C;
  return (
    <div className={`rg${ringToneClass(r.tone)}`}>
      <span className="ms-sr">{renderText(r.a11yLabel)}</span>
      <span className="w" aria-hidden="true">
        <svg className="ring-svg" viewBox="0 0 54 54">
          <circle className="t" cx="27" cy="27" r={RING_R} />
          {fill > 0 && (
            <circle
              className="p"
              cx="27"
              cy="27"
              r={RING_R}
              strokeDasharray={`${fill.toFixed(1)} ${RING_C.toFixed(1)}`}
            />
          )}
        </svg>
        <span className="g">{ringIcon(r.kind)}</span>
      </span>
      <b>{renderText(r.valueText) || "—"}</b>
      <small>{renderText(r.label)}</small>
    </div>
  );
}

function MachineStatusOverviewCard({
  vm,
  rings,
  onOpenStatus,
}: {
  vm: MachineSettingsViewModel;
  rings: MachineRingView[];
  onOpenStatus: () => void;
}) {
  if (!vm.showStatusEntry && rings.length === 0) return null;
  const active = vm.member && vm.online && vm.protocolMismatch === 0;
  const badgeText =
    renderText(vm.statusCardBadge) ||
    (!vm.member
      ? l10n.t("settings.status_removed")
      : vm.online
        ? l10n.t("machine_status.live")
        : l10n.t("settings.offline"));

  return (
    <button
      type="button"
      className={"rings-card" + (active ? "" : " off")}
      disabled={!active}
      aria-label={l10n.t("settings.status_card_title")}
      onClick={onOpenStatus}
    >
      <div className="rings-hd">
        <b>{l10n.t("settings.status_card_title")}</b>
        <span className={vm.statusCardWarn ? "warn-txt" : undefined}>
          <StatusDotMark dot={vm.statusCardDot} fallback={active ? "online" : "off"} />
          <span>{badgeText}</span>
          {active && <ChevronRight className="chev" />}
        </span>
      </div>
      {rings.length > 0 && (
        <div className="rings-row">
          {rings.map((r, i) => (
            <StatusRingTile key={r.kind || i} r={r} />
          ))}
        </div>
      )}
    </button>
  );
}

export function ChoiceSheet({
  title,
  hint,
  items,
  back,
  onClose,
}: {
  title: string;
  hint?: string;
  back?: () => void;
  onClose: () => void;
  items: {
    key: string | number;
    label: string;
    desc?: string;
    disabled?: boolean;
    danger?: boolean;
    selected: boolean;
    onPick: () => void;
  }[];
}) {
  return (
    <Sheet onClose={onClose} back={back} title={title}>
      <div className="list" role="radiogroup" aria-label={title}>
        {items.map((o) => (
          <button
            key={o.key}
            type="button"
            role="radio"
            aria-checked={o.selected}
            className={"mi" + (o.danger ? " danger" : "")}
            disabled={o.disabled}
            onClick={o.onPick}
          >
            <div className="mt">
              <b>{o.label}</b>
              {o.desc && <div className="md">{o.desc}</div>}
            </div>
            {o.selected && (
              <span className="ck">
                <Check />
              </span>
            )}
          </button>
        ))}
      </div>
      {hint && <div className="hint">{hint}</div>}
    </Sheet>
  );
}

function CliSettingsSheet({
  cli,
  vm,
  onClose,
  onToggleAgent,
  onUpdateAgentDefaults,
}: {
  cli: CliVersionView;
  vm: MachineSettingsViewModel;
  onClose: () => void;
  onToggleAgent?: (agent: Agent, enabled: boolean) => void;
  onUpdateAgentDefaults: (agent: Agent, patch: { model?: string; effort?: string; mode?: ApprovalMode }) => void;
}) {
  const [subPicker, setSubPicker] = useState<"model" | "effort" | "mode">();
  const canToggleCli = vm.cliVersions.length >= 2 && vm.showCliSwitches;
  const locked = cli.lockedLastEnabled;
  const ad = cli.defaults ?? (vm.agentDefaults?.agent === cli.agent ? vm.agentDefaults : undefined);

  if (subPicker && ad) {
    const pickerTitle =
      subPicker === "model"
        ? l10n.t("settings.cli_sheet_model")
        : subPicker === "effort"
          ? l10n.t("settings.cli_sheet_effort")
          : l10n.t("settings.cli_sheet_mode");
    const items =
      subPicker === "model"
        ? ad.modelChoices.map((o) => ({
            key: o.id,
            label: renderText(o.label),
            desc: o.desc,
            disabled: false,
            danger: false,
            selected: o.selected,
            onPick: () => {
              onUpdateAgentDefaults(cli.agent, { model: o.id });
              setSubPicker(undefined);
            },
          }))
        : subPicker === "effort"
          ? ad.effortChoices.map((e) => ({
              key: e.id,
              label: renderText(e.label),
              desc: undefined,
              disabled: false,
              danger: false,
              selected: e.selected,
              onPick: () => {
                onUpdateAgentDefaults(cli.agent, { effort: e.id });
                setSubPicker(undefined);
              },
            }))
          : ad.modeChoices.map((m) => ({
              key: m.mode,
              label: renderText(m.label),
              desc: renderText(m.disabledReason),
              disabled: m.disabled,
              danger: m.mode === ApprovalMode.FULL_ACCESS,
              selected: m.selected,
              onPick: () => {
                onUpdateAgentDefaults(cli.agent, { mode: m.mode });
                setSubPicker(undefined);
              },
            }));

    return (
      <Sheet
        onClose={onClose}
        back={() => setSubPicker(undefined)}
        title={pickerTitle}
      >
        <div className="list" role="radiogroup" aria-label={pickerTitle}>
          {items.map((o) => (
            <button
              key={o.key}
              type="button"
              role="radio"
              aria-checked={o.selected}
              className={"mi" + (o.danger ? " danger" : "")}
              disabled={o.disabled}
              onClick={o.onPick}
            >
              <div className="mt">
                <b className={o.danger ? "nowrap" : undefined}>{o.label}</b>
                {o.desc && <div className="md">{o.desc}</div>}
              </div>
              {o.selected && (
                <span className="ck">
                  <Check />
                </span>
              )}
            </button>
          ))}
        </div>
      </Sheet>
    );
  }

  const a11y = locked
    ? l10n.t("settings.cli_switches_a11y_locked", { name: cli.name, version: cli.version })
    : l10n.t("settings.cli_switches_a11y_row", {
        name: cli.name,
        version: cli.version,
        state: cli.enabled ? l10n.t("settings.cli_switches_state_on") : l10n.t("settings.cli_switches_state_off"),
      });
  const modeLabel =
    (ad && renderText(ad.modeChoices.find((m) => m.selected)?.label)) ||
    renderText(cli.defaultModeLabel) ||
    l10n.t("models.default");
  const modeDanger = cli.defaultModeDanger || ad?.selectedMode === ApprovalMode.FULL_ACCESS;

  return (
    <Sheet
      onClose={onClose}
      title={
        <span className="cli-sh-ttl">
          <AgentMark agent={cli.agent} named />
          <span>{cli.name}</span>
        </span>
      }
      action={{ label: l10n.t("common.done"), onClick: onClose }}
    >
      {canToggleCli && renderText(vm.cliSwitchesConflictBanner) && (
        <div className="set-banners">
          <Banner kind="warn">{renderText(vm.cliSwitchesConflictBanner)}</Banner>
        </div>
      )}
      {canToggleCli && renderText(vm.cliSwitchesDisabledReason) && (
        <div className="set-banners">
          <Banner kind="warn">{renderText(vm.cliSwitchesDisabledReason)}</Banner>
        </div>
      )}
      <div className="grp cli-sw-grp">
        {canToggleCli && (
          <button
            type="button"
            role="switch"
            aria-checked={cli.enabled}
            aria-label={a11y}
            className={"row cli-sw-row" + (locked ? " locked" : "")}
            disabled={!vm.cliSwitchesEditable || locked || cli.saving || !onToggleAgent}
            onClick={() => onToggleAgent?.(cli.agent, !cli.enabled)}
          >
            <span className="row-name cli-sw-nm">
              <b>{l10n.t("settings.cli_enable_on_computer")}</b>
              {locked && <small>{l10n.t("settings.cli_switches_keep_one")}</small>}
            </span>
            {cli.saving && <LoaderCircle />}
            <span className={"sw" + (cli.enabled ? "" : " off") + (locked ? " locked" : "")} aria-hidden="true" />
          </button>
        )}
        <div className="row">
          <span className="row-name">{l10n.t("settings.cli_installed_version")}</span>
          <span className="v">
            {!cli.verified && (
              <>
                <span className="warn-txt">{l10n.t("settings.cli_unverified_short")}</span>
                <span>·</span>
              </>
            )}
            <span className="mono">{cli.version || "—"}</span>
          </span>
        </div>
      </div>
      {!cli.verified && (
        <div className="gf">{l10n.t("settings.version_unverified_run_remote_agent_doctor_on_this_computer")}</div>
      )}
      {canToggleCli && renderText(vm.cliSwitchesFooter) && (
        <div className="gf">{renderText(vm.cliSwitchesFooter)}</div>
      )}
      {cli.enabled && ad ? (
        <>
          <div className="fl">{l10n.t("settings.new_project_defaults")}</div>
          <div className="grp">
            <button
              type="button"
              className="row"
              disabled={!vm.editable}
              onClick={() => setSubPicker("model")}
            >
              <span className="row-name">{l10n.t("settings.cli_sheet_model")}</span>
              <span className="v">{renderText(ad.selectedModelLabel) || l10n.t("models.default")}</span>
              <ChevronRight className="chev" />
            </button>
            {ad.effortChoices.length > 1 && (
              <button
                type="button"
                className="row"
                disabled={!vm.editable}
                onClick={() => setSubPicker("effort")}
              >
                <span className="row-name">{l10n.t("settings.cli_sheet_effort")}</span>
                <span className="v">{renderText(ad.effortChoices.find((e) => e.selected)?.label) || l10n.t("models.default")}</span>
                <ChevronRight className="chev" />
              </button>
            )}
            <button
              type="button"
              className="row"
              disabled={!vm.editable}
              onClick={() => setSubPicker("mode")}
            >
              <span className="row-name">{l10n.t("settings.cli_sheet_mode")}</span>
              <span className={"v" + (modeDanger ? " red-txt" : "")}>
                <span className="nowrap">{modeLabel}</span>
              </span>
              <ChevronRight className="chev" />
            </button>
            {renderText(ad.autoUnavailableNotice) && (
              <div className="row help">{renderText(ad.autoUnavailableNotice)}</div>
            )}
          </div>
          <div className="gf">{l10n.t("settings.cli_new_project_defaults_footer")}</div>
        </>
      ) : !cli.enabled ? (
        <div className="gf">{l10n.t("settings.cli_disabled_explanation")}</div>
      ) : null}
    </Sheet>
  );
}

/** 附件存储二级页 (`m/<id>/storage`). */
export function AttachmentStorageScreenView({
  id,
  vm,
  onBack,
  onRetrySettings,
  onUpdateDaemonSettings,
}: {
  id: string;
  vm: MachineSettingsViewModel;
  onBack: () => void;
  onRetrySettings?: () => void;
  onUpdateDaemonSettings: (patch: { imageUploadQuotaBytes?: bigint; cleanImages?: boolean }) => void;
}) {
  const [pickingLimit, setPickingLimit] = useState(false);
  const [clearConfirm, setClearConfirm] = useState(false);
  const name = vm.name || id;
  const storage = vm.attachmentStorage;
  const editable = vm.editable && !vm.savePending;

  if (!vm.found) {
    return (
      <div className="page set">
        <NavBar onBack={onBack} title={l10n.t("settings.attachment_storage")} />
        <div className="center muted" data-late={vm.loading ? "1" : undefined}>
          {renderText(vm.offlineOrMismatchBanner) || l10n.t("settings.device_not_found")}
        </div>
      </div>
    );
  }

  const limitChoice =
    storage?.quotaChoices.find((c) => c.bytes === storage.selectedQuotaBytes) ??
    storage?.quotaChoices[0];
  const limitLabel =
    (limitChoice && renderText(limitChoice.label)) ||
    renderText(storage?.limitLabel) ||
    "—";
  const usedLabel = renderText(storage?.usedTotalText) || renderText(storage?.uploadedText) || "—";

  return (
    <div className="page set">
      <NavBar onBack={onBack} title={l10n.t("settings.attachment_storage")} sub={name} />
      <div className="body set">
        {renderText(vm.offlineOrMismatchBanner) && (
          <div className="set-banners"><Banner kind="warn">{renderText(vm.offlineOrMismatchBanner)}</Banner></div>
        )}
        {renderText(vm.removedBanner) && (
          <div className="set-banners"><Banner kind="warn">{renderText(vm.removedBanner)}</Banner></div>
        )}
        {renderText(vm.saveError) && (
          <div className="set-banners"><Banner kind={vm.saveUnknown ? "warn" : "err"}>{renderText(vm.saveError)}</Banner></div>
        )}
        {!storage?.loaded ? (
          renderText(vm.settingsError) ? (
            <div className="center muted">
              {renderText(vm.settingsError)}
              {onRetrySettings && (
                <>
                  {" "}
                  <button type="button" className="link" onClick={onRetrySettings}>{t("common.retry")}</button>
                </>
              )}
            </div>
          ) : !vm.online ? (
            <div className="center muted">{l10n.t("settings.offline")}</div>
          ) : (
            <div className="center muted" data-late={vm.settingsLoading ? "1" : undefined}>
              {l10n.t("common.loading")}
            </div>
          )
        ) : (
          <>
            <div className="grp stor-card">
              <div className="stor-hd">
                <b>{l10n.t("settings.storage_used_headline", { used: usedLabel })}</b>
                <span>{l10n.t("settings.storage_limit_headline", { limit: limitLabel })}</span>
              </div>
              <div className="stor-bar" aria-hidden="true">
                {storage.uploadedPercent > 0 && <i className="u" style={{ width: `${storage.uploadedPercent}%` }} />}
                {storage.previewPercent > 0 && <i className="c" style={{ width: `${storage.previewPercent}%` }} />}
                {storage.reservePercent > 0 && <i className="r" style={{ width: `${storage.reservePercent}%` }} />}
              </div>
              <div className="stor-leg">
                <span><b className="u" />{l10n.t("settings.storage_legend_uploaded")}</span>
                <span><b className="c" />{l10n.t("settings.storage_legend_preview")}</span>
                {storage.showEmergencyReserve && (
                  <span><b className="r" />{l10n.t("settings.storage_legend_reserve")}</span>
                )}
              </div>
            </div>

            <div className="label">{l10n.t("settings.storage_section_details")}</div>
            <div className="grp">
              <div className="row col">
                <div className="line">
                  <span>{l10n.t("settings.uploaded_images_and_files")}</span>
                  <span className="v">{renderText(storage.uploadedText) || "—"}</span>
                </div>
                {renderText(storage.uploadedDetail) && (
                  <div className="hint">{renderText(storage.uploadedDetail)}</div>
                )}
              </div>
              <div className="row">
                <span className="row-name">{l10n.t("settings.preview_cache")}</span>
                <span className="v">{renderText(storage.previewCacheText) || "—"}</span>
              </div>
              {storage.showEmergencyReserve && (
                <div className="row col">
                  <div className="line">
                    <span>{l10n.t("machine_status.emergency_reserve")}</span>
                    <span className="v">{renderText(storage.emergencyReserveText)}</span>
                  </div>
                  <div className="hint">{l10n.t("machine_status.emergency_reserve_note")}</div>
                </div>
              )}
              <button
                type="button"
                className="row"
                disabled={!editable || storage.quotaChoices.length === 0}
                onClick={() => setPickingLimit(true)}
              >
                <span className="row-name">{l10n.t("settings.storage_limit_row")}</span>
                <span className="v">{limitLabel}</span>
                <ChevronRight className="chev" />
              </button>
            </div>

            <div className="grp">
              <button
                type="button"
                className="row act"
                disabled={!editable || !storage.canClear || storage.cleaning}
                onClick={() => setClearConfirm(true)}
              >
                <span className="row-name">{l10n.t("settings.storage_clear_row")}</span>
                {renderText(storage.clearButtonLabel) && (
                  <span className="v">{renderText(storage.clearButtonLabel)}</span>
                )}
              </button>
            </div>
            <div className="gf">
              {renderText(storage.footerText) || l10n.t("settings.storage_page_footer")}
            </div>
          </>
        )}
      </div>
      {pickingLimit && storage && (
        <ChoiceSheet
          title={l10n.t("settings.storage_limit_row")}
          onClose={() => setPickingLimit(false)}
          items={storage.quotaChoices.map((c) => ({
            key: String(c.bytes),
            label: renderText(c.label),
            selected: c.bytes === storage.selectedQuotaBytes,
            onPick: () => {
              onUpdateDaemonSettings({ imageUploadQuotaBytes: c.bytes });
              setPickingLimit(false);
            },
          }))}
        />
      )}
      {clearConfirm && (
        <Alert
          title={l10n.t("settings.clear_reclaimable_attachments_and_cache")}
          text={l10n.t("settings.clears_reclaimable_originals_previews_and_unsynced_large_image_cache")}
          ok={l10n.t("settings.clear_settings")}
          onCancel={() => setClearConfirm(false)}
          onOk={() => {
            setClearConfirm(false);
            onUpdateDaemonSettings({ cleanImages: true });
          }}
        />
      )}
    </div>
  );
}

type RuntimeSheet = "timeout" | "idle";

function resolveCliAgentParam(subParam: string, cliVersions: CliVersionView[]): Agent | undefined {
  if (!subParam || !cliVersions.length) return undefined;
  const num = Number(subParam);
  if (Number.isFinite(num) && num > 0) {
    const found = cliVersions.find((v) => v.agent === num);
    if (found) return found.agent;
  }
  const slug = subParam.trim().toLowerCase();
  const bySlug = cliVersions.find((v) => {
    const n = v.name.trim().toLowerCase();
    return (
      n === slug ||
      (slug === "codex" && v.agent === Agent.CODEX) ||
      ((slug === "claude" || slug === "claude_code" || slug === "claude-code") && v.agent === Agent.CLAUDE) ||
      (slug === "antigravity" && v.agent === Agent.ANTIGRAVITY)
    );
  });
  return bySlug?.agent;
}

export function MachineSettingsScreenView({
  id,
  vm,
  section,
  subParam,
  go,
  onBack,
  onRetrySettings,
  onToggleAgent,
  onUpdateAgentDefaults,
  onUpdateDaemonSettings,
  statusRingsOverride,
  directOpenDefault = false,
  renderDirectSheet,
  rejoinSlot,
  deviceActionsSlot,
}: {
  id: string;
  vm: MachineSettingsViewModel;
  section?: string;
  subParam?: string;
  go: Go;
  onBack: () => void;
  onRetrySettings: () => void;
  onSelectAgent?: (agent: Agent) => void;
  onToggleAgent?: (agent: Agent, enabled: boolean) => void;
  onUpdateAgentDefaults: (agent: Agent, patch: { model?: string; effort?: string; mode?: ApprovalMode }) => void;
  onUpdateDaemonSettings: (patch: { approvalTimeoutSeconds?: number; idleCloseMinutes?: number; imageUploadQuotaBytes?: bigint; cleanImages?: boolean }) => void;
  statusRingsOverride?: MachineRingView[];
  directOpenDefault?: boolean;
  renderDirectSheet?: (close: () => void) => ReactNode;
  rejoinSlot?: ReactNode;
  deviceActionsSlot?: ReactNode;
  // Kept for caller compatibility; removed machines render inline archive view cleanly.
  removedSlot?: ReactNode;
  ringsSlot?: ReactNode;
  connectionSlot?: ReactNode;
}) {
  const [activeCliAgent, setActiveCliAgent] = useState<Agent>();
  const [directOpen, setDirectOpen] = useState(directOpenDefault);
  const [runtimeSheet, setRuntimeSheet] = useState<RuntimeSheet>();
  const handledDeepLinkRef = useRef("");

  useEffect(() => {
    const key = `${id}:${section ?? ""}:${subParam ?? ""}`;
    if (handledDeepLinkRef.current === key) return;
    if (section === "direct" || section === "connection") {
      handledDeepLinkRef.current = key;
      setDirectOpen(true);
      return;
    }
    if (section === "cli" && vm.found && vm.cliVersions.length > 0) {
      const target = resolveCliAgentParam(subParam ?? "", vm.cliVersions) ?? vm.cliVersions[0].agent;
      handledDeepLinkRef.current = key;
      setActiveCliAgent(target);
    }
  }, [id, section, subParam, vm.found, vm.cliVersions]);

  if (section === "storage") {
    return (
      <AttachmentStorageScreenView
        id={id}
        vm={vm}
        onBack={onBack}
        onRetrySettings={onRetrySettings}
        onUpdateDaemonSettings={onUpdateDaemonSettings}
      />
    );
  }

  if (!vm.found) {
    return (
      <div className="page set">
        <NavBar onBack={onBack} />
        <div className="center muted" data-late={vm.loading ? "1" : undefined}>
          {renderText(vm.offlineOrMismatchBanner) || l10n.t("settings.device_not_found")}
        </div>
      </div>
    );
  }

  const name = vm.name || id;
  const headerSubText = renderText(vm.headerSubtitle) || (vm.online ? l10n.t("common.ios_online") : l10n.t("settings.offline"));
  const headerSub = (
    <span className="set-nav-sub">
      <StatusDotMark dot={vm.headerDot} fallback={vm.online ? "online" : "off"} />
      <span>{headerSubText}</span>
    </span>
  );

  const rings = vm.online && vm.member && vm.protocolMismatch === 0 && statusRingsOverride?.length
    ? statusRingsOverride
    : vm.statusRings;

  const cg = vm.connection;
  const directUnsupported = cg?.stage === ConnectionGroupStage.UNSUPPORTED;
  const directSpinning = !!cg?.rowSpinning || cg?.stage === ConnectionGroupStage.CONNECTING;
  const directValue = renderText(cg?.rowValueText) || renderText(cg?.statusText) || "—";
  const directSub = renderText(cg?.rowSubtitleText);

  const timeoutChoice =
    vm.approvalTimeoutChoices.find((c) => c.selected) ??
    vm.approvalTimeoutChoices.find((c) => c.seconds === vm.selectedApprovalTimeoutSeconds);
  const idleChoice =
    vm.idleCloseChoices.find((c) => c.selected) ??
    vm.idleCloseChoices.find((c) => c.minutes === vm.selectedIdleCloseMinutes);
  const storage = vm.attachmentStorage;
  const storageSummary = storage
    ? renderText(storage.summaryText) || (storage.loaded ? renderText(storage.limitLabel) : "") || "—"
    : "";
  const canOpenStorage = !!storage && vm.member && vm.protocolMismatch === 0;

  let usageSummaryText = "";
  if (vm.showUsageEntry) {
    const val = renderText(vm.usageTokensText);
    if (val) {
      if (vm.usageCachedOffline) usageSummaryText = l10n.t("settings.usage_summary_cached", { value: val });
      else if (vm.usageFallbackThirtyDays) usageSummaryText = l10n.t("settings.usage_summary_30d", { value: val });
      else usageSummaryText = l10n.t("settings.usage_summary_7d", { value: val });
    } else {
      usageSummaryText = renderText(vm.usageEmptyText) || l10n.t("settings.usage_summary_empty");
    }
  }

  const projectCountText =
    renderText(vm.projectCountText) ||
    (vm.projectCount > 0 ? l10n.t("settings.project_count_value", { count: vm.projectCount }) : "");

  const shortIdText = vm.shortId
    ? vm.shortId.startsWith("…")
      ? vm.shortId
      : `…${vm.shortId}`
    : "—";

  const activeCli = activeCliAgent !== undefined
    ? vm.cliVersions.find((v) => v.agent === activeCliAgent)
    : undefined;

  return (
    <div className="page set">
      <NavBar onBack={onBack} title={name} sub={headerSub} />
      <div className="body set">
        {renderText(vm.offlineOrMismatchBanner) && (
          <div className="set-banners"><Banner kind="warn">{renderText(vm.offlineOrMismatchBanner)}</Banner></div>
        )}
        {renderText(vm.removedBanner) && (
          <div className="set-banners"><Banner kind="warn">{renderText(vm.removedBanner)}</Banner></div>
        )}
        {renderText(vm.settingsError) && (
          <div className="set-banners"><Banner kind="err" action={t("common.retry")} onAction={onRetrySettings}>{renderText(vm.settingsError)}</Banner></div>
        )}
        {renderText(vm.saveError) && (
          <div className="set-banners"><Banner kind={vm.saveUnknown ? "warn" : "err"}>{renderText(vm.saveError)}</Banner></div>
        )}

        {/* Module 0: Computer status overview rings */}
        <MachineStatusOverviewCard
          vm={vm}
          rings={rings}
          onOpenStatus={() => go(routeForMachineStatus(id))}
        />

        {/* Module 1: Quota & Usage */}
        {(vm.quotaRows.length > 0 || vm.showUsageEntry) && (
          <>
            <div className="label">{l10n.t("settings.section_quota_and_usage")}</div>
            <div className="grp">
              {vm.quotaRows.map((sec) => {
                const primary =
                  renderText(sec.summaryPrimaryText) ||
                  renderText(sec.summaryLine) ||
                  renderText(sec.emptyOrErrorText);
                const win = renderText(sec.summaryWindowText);
                return (
                  <button
                    key={sec.agent}
                    type="button"
                    className="row has-ic quota-line"
                    onClick={() => go("q/" + encodeURIComponent(id))}
                  >
                    <span className="lead"><AgentMark agent={sec.agent} named /></span>
                    <span className="row-name"><b>{sec.agentName}</b></span>
                    <span className="v">
                      <span className={sec.summaryPrimaryWarn ? "warn-txt" : undefined}>{primary}</span>
                      {win && <span className="qt-win">{` · ${win}`}</span>}
                    </span>
                    <ChevronRight className="chev" />
                  </button>
                );
              })}
              {vm.showUsageEntry && (
                <button
                  type="button"
                  className="row has-ic"
                  onClick={() => go(routeForUsage(id))}
                >
                  <span className="lead"><Gauge /></span>
                  <span className="row-name"><b>{l10n.t("settings.usage_and_speed")}</b></span>
                  <span className="v">{usageSummaryText}</span>
                  <ChevronRight className="chev" />
                </button>
              )}
            </div>
          </>
        )}

        {/* Module 2: Coding CLIs */}
        {vm.cliVersions.length > 0 && (
          <>
            <div className="label">{l10n.t("settings.section_coding_clis")}</div>
            {vm.showCliSwitches && renderText(vm.cliSwitchesConflictBanner) && (
              <div className="set-banners"><Banner kind="warn">{renderText(vm.cliSwitchesConflictBanner)}</Banner></div>
            )}
            <div className="grp">
              {vm.cliVersions.map((v) => {
                const modelLbl = renderText(v.defaultModelLabel) || l10n.t("models.default");
                const modeLbl = renderText(v.defaultModeLabel);
                const disabledLbl = renderText(v.disabledLabel) || l10n.t("settings.cli_disabled");
                return (
                  <button
                    key={v.agent}
                    type="button"
                    className="row has-ic cli-row"
                    onClick={() => setActiveCliAgent(v.agent)}
                  >
                    <span className={"lead" + (v.enabled ? "" : " muted-ic")}>
                      <AgentMark agent={v.agent} named />
                    </span>
                    <span className="row-name">
                      <b className={v.enabled ? undefined : "muted-lbl"}>{v.name}</b>
                      {!v.verified && (
                        <small>
                          <span className="warn-txt">{l10n.t("settings.cli_unverified_short")}</span>
                          {v.version ? ` · ${v.version}` : ""}
                        </small>
                      )}
                    </span>
                    {v.enabled ? (
                      <span className="v">
                        <span className="vt">{modelLbl}</span>
                        {modeLbl && (
                          <>
                            <span className="cli-sep">·</span>
                            <span className={(v.defaultModeDanger ? "red-txt " : "") + "nowrap"}>{modeLbl}</span>
                          </>
                        )}
                      </span>
                    ) : (
                      <span className="v">{disabledLbl}</span>
                    )}
                    <ChevronRight className="chev" />
                  </button>
                );
              })}
            </div>
          </>
        )}

        {/* Module 3: Runtime & Storage */}
        <div className="label">{l10n.t("settings.section_runtime_and_storage")}</div>
        <div className="grp">
          {cg && (
            <button
              type="button"
              className="row"
              onClick={() => setDirectOpen(true)}
            >
              <span className="row-name">
                <b>{l10n.t("transport.direct")}</b>
                {directSub && <small>{directSub}</small>}
              </span>
              <span className="v">
                {directSpinning ? (
                  <StateMark kind="run" />
                ) : (
                  !directUnsupported && cg && (
                    cg.rowDot ? <StatusDotMark dot={cg.rowDot} /> : <StateMark kind={stageToMark(cg.stage)} />
                  )
                )}
                <span className="vt">{directValue}</span>
              </span>
              <ChevronRight className="chev" />
            </button>
          )}
          <button
            type="button"
            className="row"
            disabled={!vm.editable}
            onClick={() => setRuntimeSheet("timeout")}
          >
            <span className="row-name">{l10n.t("settings.approval_timeout")}</span>
            <span className="v">{timeoutChoice ? renderText(timeoutChoice.label) : "—"}</span>
            <ChevronRight className="chev" />
          </button>
          <button
            type="button"
            className="row"
            disabled={!vm.editable}
            onClick={() => setRuntimeSheet("idle")}
          >
            <span className="row-name">{l10n.t("settings.automatically_end_idle_sessions")}</span>
            <span className="v">{idleChoice ? renderText(idleChoice.label) : "—"}</span>
            <ChevronRight className="chev" />
          </button>
          {storage && (
            <button
              type="button"
              className="row"
              disabled={!canOpenStorage}
              onClick={() => go(routeForMachineStorage(id))}
            >
              <span className="row-name">{l10n.t("settings.attachment_storage")}</span>
              <span className={"v" + (storage.summaryWarn && storage.loaded && canOpenStorage ? " warn-txt" : "")}>
                {storageSummary}
              </span>
              <ChevronRight className="chev" />
            </button>
          )}
        </div>

        {/* Module 4: Management */}
        <div className="label">{l10n.t("settings.section_management")}</div>
        <div className="grp">
          {(vm.projectCount > 0 || !!projectCountText) && (
            <button
              type="button"
              className="row"
              onClick={() => go(routeForProjects(id))}
            >
              <span className="row-name">{l10n.t("settings.projects_row")}</span>
              <span className="v">{projectCountText}</span>
              <ChevronRight className="chev" />
            </button>
          )}
          <div className="row">
            <span className="row-name">{l10n.t("settings.device_id")}</span>
            <span className="v mono">{shortIdText}</span>
          </div>
        </div>

        {vm.management?.canRejoin && rejoinSlot}
        {deviceActionsSlot}
      </div>

      {activeCli && (
        <CliSettingsSheet
          cli={activeCli}
          vm={vm}
          onClose={() => setActiveCliAgent(undefined)}
          onToggleAgent={onToggleAgent}
          onUpdateAgentDefaults={onUpdateAgentDefaults}
        />
      )}
      {directOpen && renderDirectSheet?.(() => setDirectOpen(false))}
      {runtimeSheet === "timeout" && (
        <ChoiceSheet
          title={l10n.t("settings.approval_timeout")}
          hint={l10n.t("settings.unanswered_approvals_are_denied_after_this_time")}
          onClose={() => setRuntimeSheet(undefined)}
          items={vm.approvalTimeoutChoices.map((c) => ({
            key: c.seconds,
            label: renderText(c.label),
            selected: c.selected,
            onPick: () => {
              onUpdateDaemonSettings({ approvalTimeoutSeconds: c.seconds });
              setRuntimeSheet(undefined);
            },
          }))}
        />
      )}
      {runtimeSheet === "idle" && (
        <ChoiceSheet
          title={l10n.t("settings.automatically_end_idle_sessions")}
          hint={l10n.t("settings.sessions_end_automatically_after_this_much_inactivity_not_running")}
          onClose={() => setRuntimeSheet(undefined)}
          items={vm.idleCloseChoices.map((c) => ({
            key: c.minutes,
            label: renderText(c.label),
            selected: c.selected,
            onPick: () => {
              onUpdateDaemonSettings({ idleCloseMinutes: c.minutes });
              setRuntimeSheet(undefined);
            },
          }))}
        />
      )}
    </div>
  );
}

export function MachinePage({
  remote,
  id,
  section,
  subParam,
  go,
  onBack,
}: {
  remote: Remote;
  id: string;
  section?: string;
  subParam?: string;
  go: Go;
  onBack: () => void;
}) {
  const host = useCoreHost(remote);
  const vm = useMachineSettingsView(host, id);
  const statusVm = useMachineStatusView(host, id, MachinePollMode.OVERVIEW);

  return (
    <MachineSettingsScreenView
      id={id}
      vm={vm}
      section={section}
      subParam={subParam}
      go={go}
      onBack={onBack}
      onRetrySettings={() => host.machineSettingsOpen(id)}
      onSelectAgent={(a) => host.machineSettingsSelectAgent(id, a)}
      onToggleAgent={(a, enabled) => host.toggleMachineAgent(id, a, enabled)}
      onUpdateAgentDefaults={(a, p) => host.updateMachineAgentDefaults(id, a, p)}
      onUpdateDaemonSettings={(p) => host.updateMachineDaemonSettings(id, p)}
      statusRingsOverride={statusVm.rings}
      directOpenDefault={remote.demoFlag("diagsheet")}
      renderDirectSheet={(close) => (
        <DirectSettingsSheet remote={remote} id={id} connection={vm.connection} onClose={close} />
      )}
      rejoinSlot={
        <div className="set-banners">
          <RejoinButton server={remote.account.server} remote={remote}>{l10n.t("settings.rejoin_account")}</RejoinButton>
        </div>
      }
      deviceActionsSlot={<DeviceActions remote={remote} id={id} machine go={go} onRemoved={onBack} />}
    />
  );
}
