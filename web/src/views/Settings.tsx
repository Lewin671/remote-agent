import { Fragment, useEffect, useRef, useState, type ReactNode } from "react";
import * as l10n from "../i18n";
import { t } from "../i18n";
import { DeviceKind } from "../gen/remoteagent/v1/relay_pb";
import {
  MachineStatusDot,
  QuotaBannerSeverity,
  QuotaSectionState,
  QuotaTone,
  type QuotaCliSection,
  type QuotaRowView,
  type QuotaViewModel,
} from "../gen/remoteagent/core/v1/machine_view_pb";
import type {
  ClientDetailViewModel,
  ClientRowView,
  DevicesViewModel,
  MachineListCardView,
  NotificationsView,
  SettingsViewModel,
} from "../gen/remoteagent/core/v1/settings_view_pb";
import {
  ConnProbeState,
  NotificationPermissionState,
  PushRegistrationState,
  ThemePreference,
  routeForMachineSettings,
  useClientDetailView,
  useClientRenameState,
  useCoreHost,
  useDevicesView,
  useQuotaView,
  useSettingsView,
  type Go,
} from "../core/host";
import { renderText } from "../core/text";
import type { Remote } from "../remote";
import { DeviceActions } from "./DeviceManagement";
import { RejoinButton } from "../Rejoin";
import { LeaveAlert } from "./Security";
import { RecoveryNotice } from "./authority-notice";
import { applyUpdate, useUpdate } from "../update";
import {
  Banner,
  NavBar,
  Sheet,
  Switch,
  GlassButton,
  useRemote,
  type Mark,
} from "../ui";
import { ConnCheck, connWord } from "./ConnCheck";
import {
  ChevronLeft,
  ChevronRight,
  Globe,
  Laptop,
  LoaderCircle,
  Plus,
  Smartphone,
  Tablet,
} from "../icons";
import { StatusDotMark } from "./MachineSettings";

export {
  AttachmentStorageScreenView,
  MachinePage,
  MachineSettingsScreenView,
} from "./MachineSettings";
export { LicensesPage } from "./Licenses";

function quotaToneClass(tone: QuotaTone): string {
  switch (tone) {
    case QuotaTone.WARN: return "warn";
    case QuotaTone.FULL: return "full";
    default: return "";
  }
}

function bannerKind(sev: QuotaBannerSeverity): "warn" | "err" {
  return sev === QuotaBannerSeverity.FULL ? "err" : "warn";
}

function ClientIcon({ description }: { description?: string }) {
  const s = (description ?? "").toLowerCase();
  if (/ipad|tablet/.test(s)) return <Tablet />;
  if (/iphone|ios|android|pixel|phone|mobile/.test(s)) return <Smartphone />;
  if (/macos|mac os|windows|linux|desktop/.test(s)) return <Laptop />;
  return <Globe />;
}

function SyncClientEffects({ remote }: { remote: Remote }) {
  const host = useCoreHost(remote);
  const update = useUpdate();

  useEffect(() => {
    host.syncBrowserPrefs();
  }, [host]);

  useEffect(() => {
    const ver = versionText(import.meta.env.VITE_PRODUCT_VERSION, import.meta.env.VITE_BUILD_COMMIT);
    host.reportClientUpdateState({
      currentVersion: ver,
      checking: update === "checking",
      availableVersion: update === "ready" ? "next" : "",
      downloadedVersion: update === "ready" ? "next" : "",
      errorMessage: update === "failed" ? l10n.t("settings.could_not_download_retry_later") : "",
    });
  }, [host, update]);

  return null;
}

function serverConnDotFor(vm: SettingsViewModel): MachineStatusDot {
  const s = vm.connCheck?.serverState;
  if (!vm.online || s === ConnProbeState.UNAVAILABLE) return MachineStatusDot.RED_STATIC;
  if (s === ConnProbeState.SLOW || s === ConnProbeState.CHECKING) return MachineStatusDot.ORANGE_STATIC;
  if (s === ConnProbeState.WORKING) return MachineStatusDot.GREEN_LIVE;
  return MachineStatusDot.UNSPECIFIED;
}

function MachineRow({ m, go }: { m: MachineListCardView; go: Go }) {
  const host = routeForMachineSettings(m.machineId);
  const status = renderText(m.statusText);
  return (
    <button type="button" className="row has-ic" onClick={() => go(host)}>
      <span className="lead"><Laptop /></span>
      <span className="row-name">
        <b>{m.name}</b>
        {m.subtitleParts.length > 0 && (
          <small>
            {m.subtitleParts.map((part, idx) => (
              <Fragment key={idx}>
                {idx > 0 && " · "}
                <span className={part.warn ? "warn-txt" : undefined}>{renderText(part.text)}</span>
              </Fragment>
            ))}
          </small>
        )}
      </span>
      <span className="v">
        <StatusDotMark dot={m.statusDot} fallback={m.online ? "online" : "off"} />
        <span className={m.statusWarn ? "warn-txt" : undefined}>{status}</span>
      </span>
      <ChevronRight className="chev" />
    </button>
  );
}

export function SettingsScreenView({
  vm,
  go,
  onBack,
  serverHost,
  connTone,
  connText,
  updateState,
  onCheckForUpdates,
  onApplyUpdate = applyUpdate,
  onSetTheme,
  onAddDevice,
  onOpenConnCheck,
  onOpenLeave,
  syncEffectsSlot,
  recoverySlot,
  overlaysSlot,
}: {
  vm: SettingsViewModel;
  go: Go;
  onBack?: () => void;
  serverHost: string;
  connTone: Mark;
  connText: string;
  updateState?: string;
  onCheckForUpdates: () => void;
  onApplyUpdate?: () => void;
  onSetTheme: (theme: ThemePreference) => void;
  onSetNotificationPref?: (patch: { notifyAttention?: boolean; notifyTurnEnded?: boolean; notifyScheduled?: boolean }) => void;
  onEnablePushNotifications?: () => void;
  onAddDevice?: () => void;
  onOpenConnCheck?: () => void;
  onOpenLeave?: () => void;
  syncEffectsSlot?: ReactNode;
  recoverySlot?: ReactNode;
  managementSlot?: ReactNode;
  securitySlot?: ReactNode;
  overlaysSlot?: ReactNode;
}) {
  const notifSummary = renderText(vm.notifications?.summaryText);
  const notifWarn = !!vm.notifications?.summaryWarn;
  const clientsSummary = renderText(vm.clientsSummaryText);
  const secSummary = renderText(vm.security?.summaryText);
  const machineCount = renderText(vm.machineCountText);
  const addHint = renderText(vm.addDeviceHint);

  const hasUpdate = vm.update?.hasUpdate || updateState === "ready";
  const checking = vm.update?.checking || updateState === "checking";
  const currentVersion =
    vm.update?.currentVersion ||
    versionText(import.meta.env.VITE_PRODUCT_VERSION, import.meta.env.VITE_BUILD_COMMIT);
  const updateError = renderText(vm.update?.errorText);
  const updateStatus = renderText(vm.update?.statusText);
  const addClick = onAddDevice ?? (() => go("settings/add-device"));

  return (
    <div className="body set settings">
      {syncEffectsSlot}
      {onBack && (
        <div className="tbar">
          <GlassButton label={t("common.back")} onClick={onBack}>
            <ChevronLeft />
          </GlassButton>
        </div>
      )}
      <div className="large"><h3>{l10n.t("common.settings")}</h3></div>

      {(recoverySlot || (vm.clockSkewed && renderText(vm.clockSkewBannerText)) || (vm.unlinked && renderText(vm.unlinkedBannerText))) && (
        <div className="set-banners">
          {recoverySlot}
          {vm.clockSkewed && renderText(vm.clockSkewBannerText) && (
            <Banner kind="warn">{renderText(vm.clockSkewBannerText)}</Banner>
          )}
          {vm.unlinked && renderText(vm.unlinkedBannerText) && (
            <Banner kind="warn">{renderText(vm.unlinkedBannerText)}</Banner>
          )}
        </div>
      )}

      {/* Group 1: Computers */}
      <div className="label">
        <span>{l10n.t("settings.section_computers")}</span>
        {machineCount && <span className="lbl-meta">{machineCount}</span>}
      </div>
      {vm.machines.length === 0 ? (
        <div className="set-empty-card" data-late={vm.machinesLoading ? "1" : undefined}>
          <b>{renderText(vm.emptyMachinesText) || l10n.t("settings.empty_computers_title")}</b>
          {renderText(vm.emptyMachinesBody) ? (
            <p>{renderText(vm.emptyMachinesBody)}</p>
          ) : (
            !vm.canAddDevice && addHint && <p>{addHint}</p>
          )}
          {vm.canAddDevice && (
            <button type="button" className="pri-btn" onClick={addClick}>
              <Plus />
              <span>{l10n.t("settings.add_first_computer")}</span>
            </button>
          )}
        </div>
      ) : (
        <>
          <div className="grp">
            {vm.machines.map((m) => (
              <MachineRow key={m.machineId} m={m} go={go} />
            ))}
            {vm.canAddDevice && (
              <button type="button" className="row has-ic act" onClick={addClick}>
                <span className="lead"><Plus /></span>
                <span className="row-name">{l10n.t("settings.add_device_item")}</span>
              </button>
            )}
          </div>
          {!vm.canAddDevice && addHint && <div className="gf">{addHint}</div>}
        </>
      )}

      {/* Group 2: This Device (Notifications, Appearance) */}
      <div className="label"><span>{l10n.t("settings.section_this_device")}</span></div>
      <div className="grp">
        <button type="button" className="row" onClick={() => go("settings/notifications")}>
          <span className="row-name">{l10n.t("settings.notifications_title")}</span>
          {notifSummary && (
            <span className={"v" + (notifWarn ? " warn-txt" : "")}>{notifSummary}</span>
          )}
          <ChevronRight className="chev" />
        </button>
        <div className="row">
          <span className="row-name">{l10n.t("settings.appearance")}</span>
          <ThemeSeg value={vm.appearance?.theme ?? ThemePreference.AUTO} onChange={onSetTheme} />
        </div>
      </div>

      {/* Group 3: Account & Devices (Clients, Security, Server & Connection) */}
      <div className="label"><span>{l10n.t("settings.section_account_and_devices")}</span></div>
      <div className="grp">
        <button type="button" className="row" onClick={() => go("settings/clients")}>
          <span className="row-name">{l10n.t("settings.clients_title")}</span>
          {clientsSummary && (
            <span className={"v" + (vm.clientsSummaryWarn ? " warn-txt" : "")}>{clientsSummary}</span>
          )}
          <ChevronRight className="chev" />
        </button>
        <button type="button" className="row" onClick={() => go("settings/security")}>
          <span className="row-name">{l10n.t("security.account_security")}</span>
          {secSummary && <span className="v">{secSummary}</span>}
          <ChevronRight className="chev" />
        </button>
        <button type="button" className="row" onClick={onOpenConnCheck}>
          <span className="row-name">{l10n.t("settings.server_and_connection")}</span>
          <span className="v">
            <StatusDotMark dot={serverConnDotFor(vm)} fallback={connTone} />
            <span className="vt">{serverHost || connText}</span>
          </span>
          <ChevronRight className="chev" />
        </button>
      </div>

      {/* Group 4: About (Version, Open-Source Licenses) */}
      <div className="label"><span>{l10n.t("settings.about")}</span></div>
      <div className="grp">
        {hasUpdate ? (
          <button type="button" className="row" onClick={onApplyUpdate}>
            <span className="row-name">
              <b>{l10n.t("settings.version_label")}</b>
              <small>{l10n.t("home.update_ready")}</small>
            </span>
            <span className="v act-txt">{l10n.t("common.reload")}</span>
            <ChevronRight className="chev" />
          </button>
        ) : (
          <button
            type="button"
            className="row"
            disabled={checking}
            aria-label={l10n.t("settings.check_for_updates")}
            onClick={onCheckForUpdates}
          >
            <span className="row-name">
              <b>{l10n.t("settings.version_label")}</b>
              {(checking || updateError || updateState === "latest" || updateStatus) && (
                <small>
                  {checking ? (
                    <>
                      <LoaderCircle />
                      {" "}
                      {updateStatus || l10n.t("settings.checking")}
                    </>
                  ) : updateError ? (
                    <span className="red-txt">{updateError}</span>
                  ) : updateState === "latest" ? (
                    l10n.t("settings.up_to_date")
                  ) : (
                    updateStatus
                  )}
                </small>
              )}
            </span>
            <span className="v mono">{currentVersion}</span>
            <ChevronRight className="chev" />
          </button>
        )}
        <button type="button" className="row" onClick={() => go("settings/licenses")}>
          <span className="row-name">{l10n.t("settings.open_source_licenses")}</span>
          <ChevronRight className="chev" />
        </button>
      </div>

      {/* Bottom Separated Danger Group */}
      <div className="grp mt">
        <button type="button" className="row danger" onClick={onOpenLeave}>
          <span className="row-name">{l10n.t("settings.exit_and_delete_local_keys")}</span>
        </button>
      </div>

      {overlaysSlot}
    </div>
  );
}

export function Settings({ remote, go, onBack }: { remote: Remote; go: Go; onBack?: () => void }) {
  useRemote(remote);
  const host = useCoreHost(remote);
  const vm = useSettingsView(host);
  const update = useUpdate();
  const conn = connWord(remote);
  const [checking, setChecking] = useState(false);
  const [leaving, setLeaving] = useState(false);

  return (
    <SettingsScreenView
      vm={vm}
      go={go}
      onBack={onBack}
      serverHost={new URL(remote.account.server).host}
      connTone={conn.tone}
      connText={conn.text}
      updateState={update}
      onCheckForUpdates={() => host.checkForUpdates()}
      onSetTheme={(theme) => host.setAppearancePref(theme)}
      onOpenConnCheck={() => setChecking(true)}
      onOpenLeave={() => setLeaving(true)}
      syncEffectsSlot={<SyncClientEffects remote={remote} />}
      recoverySlot={<RecoveryNotice remote={remote} go={go} />}
      overlaysSlot={
        <>
          {leaving && <LeaveAlert onCancel={() => setLeaving(false)} />}
          {checking && <ConnCheck remote={remote} onClose={() => setChecking(false)} />}
        </>
      }
    />
  );
}

function PushBanners({
  notif,
  help,
  onHelp,
  onEnableAttention,
  onRetryPush,
}: {
  notif?: NotificationsView;
  help: boolean;
  onHelp: () => void;
  onEnableAttention: () => void;
  onRetryPush: () => void;
}) {
  if (!notif) return null;
  const usable =
    notif.permission === NotificationPermissionState.GRANTED ||
    notif.permission === NotificationPermissionState.UNSPECIFIED;
  const busy = notif.pushState === PushRegistrationState.REGISTERING;
  let warn = null;
  if (notif.permission === NotificationPermissionState.NEEDS_HOME_SCREEN) {
    warn = (
      <div className="set-banners">
        <Banner kind="warn" action={help ? undefined : l10n.t("settings.details")} onAction={onHelp}>
          {help ? l10n.t("settings.iphone_push_help") : l10n.t("settings.on_iphone_add_to_home_screen_and_open_from")}
        </Banner>
      </div>
    );
  } else if (notif.permission === NotificationPermissionState.UNSUPPORTED) {
    warn = (
      <div className="set-banners">
        <Banner kind="warn">{l10n.t("settings.this_browser_does_not_support_push_notifications")}</Banner>
      </div>
    );
  } else if (notif.permission === NotificationPermissionState.DENIED) {
    warn = (
      <div className="set-banners">
        <Banner kind="warn" action={help ? undefined : l10n.t("settings.how_to_enable")} onAction={onHelp}>
          {help ? l10n.t("settings.notification_permission_help") : l10n.t("settings.the_browser_denied_notification_permission")}
        </Banner>
      </div>
    );
  } else if (!notif.notifyAttention && !notif.notifyTurnEnded) {
    warn = (
      <div className="set-banners">
        <Banner
          kind="warn"
          action={l10n.t("settings.enable")}
          onAction={busy ? undefined : onEnableAttention}
        >
          {l10n.t("settings.session_alerts_are_off_you_will_not_be_notified")}
        </Banner>
      </div>
    );
  } else if (usable && notif.permission === NotificationPermissionState.UNSPECIFIED) {
    warn = (
      <div className="set-banners">
        <Banner
          action={l10n.t("settings.enable_security_notifications")}
          onAction={busy ? undefined : onRetryPush}
        >
          {l10n.t("settings.security_notifications")}
        </Banner>
      </div>
    );
  }

  return (
    <>
      {warn}
      {notif.errorDetail && (
        <div className="set-banners">
          <Banner kind="err" action={t("common.retry")} onAction={busy ? undefined : onRetryPush}>
            {l10n.t("settings.subscription_error", { reason: notif.errorDetail })}
          </Banner>
        </div>
      )}
    </>
  );
}

/** 通知设置二级页 (`settings/notifications`). */
export function NotificationsScreenView({
  notif,
  onBack,
  onSetNotificationPref,
  onEnablePushNotifications,
}: {
  notif?: NotificationsView;
  onBack: () => void;
  onSetNotificationPref: (patch: { notifyAttention?: boolean; notifyTurnEnded?: boolean; notifyScheduled?: boolean }) => void;
  onEnablePushNotifications: () => void;
}) {
  const [help, setHelp] = useState(false);
  const usable =
    !notif ||
    notif.permission === NotificationPermissionState.GRANTED ||
    notif.permission === NotificationPermissionState.UNSPECIFIED;
  const busy = notif?.pushState === PushRegistrationState.REGISTERING;

  return (
    <div className="page set">
      <NavBar
        onBack={onBack}
        title={l10n.t("settings.notifications_title")}
        sub={l10n.t("settings.notifications_subtitle")}
      />
      <div className="body set">
        <PushBanners
          notif={notif}
          help={help}
          onHelp={() => setHelp(true)}
          onEnableAttention={() => onSetNotificationPref({ notifyAttention: true })}
          onRetryPush={onEnablePushNotifications}
        />

        <div className="label">{l10n.t("settings.notifications_group_alerts")}</div>
        <div className="grp">
          <div className="row col-sw">
            <span className="row-name">
              <b>{l10n.t("settings.when_i_need_to_act")}</b>
              <small>{l10n.t("settings.notifications_when_i_need_to_act_desc")}</small>
            </span>
            <Switch
              on={usable && !!notif?.notifyAttention}
              disabled={!usable || busy}
              onChange={(v) => onSetNotificationPref({ notifyAttention: v })}
              label={l10n.t("settings.when_i_need_to_act")}
            />
          </div>
          <div className="row col-sw">
            <span className="row-name">
              <b>{l10n.t("settings.when_a_turn_ends")}</b>
              <small>{l10n.t("settings.notifications_when_a_turn_ends_desc")}</small>
            </span>
            <Switch
              on={usable && !!notif?.notifyTurnEnded}
              disabled={!usable || busy}
              onChange={(v) => onSetNotificationPref({ notifyTurnEnded: v })}
              label={l10n.t("settings.when_a_turn_ends")}
            />
          </div>
          <div className="row col-sw">
            <span className="row-name">
              <b>{l10n.t("settings.scheduled_task_results")}</b>
              <small>{l10n.t("settings.notifications_scheduled_results_desc")}</small>
            </span>
            <Switch
              on={usable && !!notif?.notifyScheduled}
              disabled={!usable || busy}
              onChange={(v) => onSetNotificationPref({ notifyScheduled: v })}
              label={l10n.t("settings.scheduled_task_results")}
            />
          </div>
        </div>
        <div className="gf">{l10n.t("settings.notifications_security_footer")}</div>
      </div>
    </div>
  );
}

export function NotificationsPage({ remote, onBack }: { remote: Remote; onBack: () => void }) {
  useRemote(remote);
  const host = useCoreHost(remote);
  const vm = useSettingsView(host);
  return (
    <NotificationsScreenView
      notif={vm.notifications}
      onBack={onBack}
      onSetNotificationPref={(patch) => host.setNotificationPref(patch)}
      onEnablePushNotifications={() => host.enablePushNotifications()}
    />
  );
}

/** 纯客户端列表二次页 (`settings/clients` / `#/devices`). */
export function ClientsScreenView({
  vm,
  go,
  onBack,
  onRetry,
  devicesLoadError = false,
  unfinishedSlot,
  managementStandaloneSlot,
}: {
  vm: DevicesViewModel;
  go: Go;
  onBack: () => void;
  onRetry: () => void;
  devicesLoadError?: boolean;
  unfinishedSlot?: ReactNode;
  managementInGroupSlot?: ReactNode;
  managementStandaloneSlot?: ReactNode;
}) {
  const hintText = renderText(vm.errorText);
  const clientCountText =
    renderText(vm.clientCountText) ||
    (vm.clients.length > 0 ? l10n.t("settings.clients_count", { count: vm.clients.length }) : "");
  return (
    <div className="page set">
      <NavBar
        onBack={onBack}
        title={l10n.t("settings.clients_title")}
        sub={l10n.t("settings.clients_control_sessions")}
      />
      <div className="body set">
        {unfinishedSlot}
        {!unfinishedSlot && vm.unfinishedPairings.length > 0 && (
          <>
            <div className="label">{l10n.t("settings.unfinished_pairing")}</div>
            <div className="grp">
              {vm.unfinishedPairings.map((d) => (
                <div key={d.deviceId} className="row has-ic">
                  <span className="lead"><Smartphone /></span>
                  <span className="row-name">
                    <b>{d.displayName || d.description || d.shortId || d.deviceId}</b>
                    <small>
                      {l10n.t("settings.clients_pairing_incomplete_desc")}
                      {d.shortId ? ` · ${d.shortId}` : ""}
                    </small>
                  </span>
                  <span className="v warn-txt">{l10n.t("settings.clients_pairing_incomplete_badge")}</span>
                </div>
              ))}
            </div>
          </>
        )}

        <div className="label">
          <span>{l10n.t("settings.clients_group_paired")}</span>
          {clientCountText && <span className="lbl-meta">{clientCountText}</span>}
        </div>
        <div className="grp">
          {(hintText || devicesLoadError) && (
            <div className="row help">
              {hintText || l10n.t("settings.could_not_read_the_device_list")}
              <button type="button" className="link" onClick={onRetry}>
                {t("common.retry")}
              </button>
            </div>
          )}
          {vm.clients.map((c: ClientRowView) => {
            const title = c.displayName || c.description || c.shortId;
            const sub = [
              c.displayName && c.description && c.description !== c.displayName ? c.description : "",
              c.shortId,
            ]
              .filter(Boolean)
              .join(" · ");
            const trailing =
              c.isSelf || c.isAuthority
                ? renderText(c.roleLabel) || renderText(c.lastUsedText)
                : renderText(c.lastUsedText) || renderText(c.roleLabel);
            return (
              <button
                key={c.deviceId}
                type="button"
                className="row has-ic client"
                onClick={() => go("c/" + encodeURIComponent(c.deviceId))}
              >
                <span className="lead ico"><ClientIcon description={c.description} /></span>
                <span className="row-name client-info">
                  <b>{title}</b>
                  {sub && <small>{sub}</small>}
                </span>
                <span className="v">{trailing}</span>
                <ChevronRight className="chev" />
              </button>
            );
          })}
          {vm.isManager && (
            <button type="button" className="row has-ic act" onClick={() => go("settings/add-device")}>
              <span className="lead"><Plus /></span>
              <span className="row-name">{l10n.t("settings.clients_add_client")}</span>
            </button>
          )}
        </div>
        {!vm.isManager && (managementStandaloneSlot ?? (renderText(vm.addDeviceHint) ? <p className="gf">{renderText(vm.addDeviceHint)}</p> : null))}
        <div className="gf">{l10n.t("settings.clients_footer_redesign")}</div>
      </div>
    </div>
  );
}

export function ClientsPage({
  remote,
  go,
  onBack,
}: {
  remote: Remote;
  go: Go;
  onBack: () => void;
}) {
  useRemote(remote);
  const host = useCoreHost(remote);
  const vm = useDevicesView(host);
  useEffect(() => {
    void remote.loadDevices(true);
  }, [remote]);

  const unfinishedSlot = vm.unfinishedPairings.length > 0 && (
    <>
      <div className="label">{l10n.t("settings.unfinished_pairing")}</div>
      <p className="gf">{l10n.t("settings.the_new_device_has_not_finished_joining_if_it")}</p>
      {vm.unfinishedPairings.map((d) => {
        const kind = remote.deviceKind(d.deviceId);
        return (
          <div key={d.deviceId}>
            <div className="grp">
              <div className="row has-ic">
                <span className="lead">
                  {kind === DeviceKind.MACHINE ? <Laptop /> : <Smartphone />}
                </span>
                <span className="row-name">
                  <b>{kind === DeviceKind.MACHINE ? l10n.t("settings.computer") : l10n.t("security.client")}</b>
                  <small>{l10n.t("settings.clients_pairing_incomplete_desc")}</small>
                </span>
                <span className="v warn-txt">{l10n.t("settings.clients_pairing_incomplete_badge")}</span>
              </div>
            </div>
            <DeviceActions remote={remote} id={d.deviceId} incomplete />
          </div>
        );
      })}
    </>
  );

  return (
    <ClientsScreenView
      vm={vm}
      go={go}
      onBack={onBack}
      onRetry={() => void remote.loadDevices()}
      devicesLoadError={remote.devicesStatus === "error"}
      unfinishedSlot={unfinishedSlot}
    />
  );
}

export const DevicesScreenView = ClientsScreenView;
export const DevicesPage = ClientsPage;

export function ClientDetailScreenView({
  vm,
  rename,
  onBack,
  onUpdateName,
  onOpenLeave,
  removeSlot,
  leaveOverlaySlot,
}: {
  vm: ClientDetailViewModel;
  rename: { saving: boolean; error: string };
  onBack: () => void;
  onUpdateName: (name: string, clear: boolean) => void;
  onOpenLeave?: () => void;
  removeSlot?: ReactNode;
  leaveOverlaySlot?: ReactNode;
}) {
  const [renaming, setRenaming] = useState(false);
  const [draft, setDraft] = useState("");
  const wasSaving = useRef(false);

  useEffect(() => {
    if (wasSaving.current && !rename.saving && !rename.error) {
      setRenaming(false);
    }
    wasSaving.current = rename.saving;
  }, [rename.saving, rename.error]);

  if (!vm.found && !vm.isSelf) {
    return (
      <div className="page set">
        <NavBar onBack={onBack} />
        <div className="center muted">{l10n.t("settings.device_not_found")}</div>
      </div>
    );
  }

  const title = vm.displayName || vm.defaultName || vm.description || vm.shortId;
  const sub = renderText(vm.roleLabel) || renderText(vm.lastUsedText) || l10n.t("security.paired");

  return (
    <div className="page set">
      <NavBar onBack={onBack} title={title} sub={sub} />
      <div className="body set">
        <div className="label">{l10n.t("security.client")}</div>
        <div className="grp">
          <button
            type="button"
            className="row"
            disabled={!vm.canRename}
            onClick={() => {
              if (!vm.canRename) return;
              setDraft(vm.customName || vm.defaultName || vm.description);
              setRenaming(true);
            }}
          >
            <span className="row-name">{l10n.t("settings.name")}</span>
            <span className="v">{vm.customName || l10n.t("settings.unnamed")}</span>
            {vm.canRename && <ChevronRight className="chev" />}
          </button>
          {vm.canRestoreDefault && (
            <button
              type="button"
              className="row act"
              disabled={rename.saving}
              onClick={() => onUpdateName("", true)}
            >
              <span className="row-name">{l10n.t("settings.restore_default_name")}</span>
              <span className="v">{vm.defaultName}</span>
            </button>
          )}
          {vm.description && (
            <div className="row">
              <span className="row-name">{l10n.t("settings.runs_on")}</span>
              <span className="v">{vm.description}</span>
            </div>
          )}
          <div className="row">
            <span className="row-name">{l10n.t("settings.last_used")}</span>
            <span className="v">{renderText(vm.lastUsedText) || "—"}</span>
          </div>
          {renderText(vm.addedText) && (
            <div className="row">
              <span className="row-name">{l10n.t("settings.added")}</span>
              <span className="v">{renderText(vm.addedText)}</span>
            </div>
          )}
          <div className="row">
            <span className="row-name">{l10n.t("settings.device_id")}</span>
            <span className="v mono">{vm.shortId.startsWith("…") ? vm.shortId : `…${vm.shortId}`}</span>
          </div>
        </div>
        <div className="gf">{renderText(vm.managementHint)}</div>
        {rename.error && !renaming && (
          <div className="set-banners">
            <Banner kind="err">{rename.error}</Banner>
          </div>
        )}
        {vm.showExitAction ? (
          <div className="grp">
            <button type="button" className="row danger" onClick={onOpenLeave}>
              <span className="row-name">{l10n.t("settings.exit_and_delete_local_keys")}</span>
            </button>
          </div>
        ) : vm.canRemove ? (
          removeSlot
        ) : null}
      </div>
      {renaming && (
        <Sheet onClose={() => setRenaming(false)} title={l10n.t("settings.client_name")}>
          <input
            className="field"
            autoFocus
            value={draft}
            placeholder={vm.defaultName}
            maxLength={vm.maxNameLength || 40}
            aria-label={l10n.t("settings.client_name")}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") onUpdateName(draft, false);
            }}
          />
          <div className="hint">{l10n.t("settings.names_sync_encrypted_the_server_cannot_read_them_leave")}</div>
          {rename.error && <Banner kind="err">{rename.error}</Banner>}
          <div className="btns sheet-foot">
            <button type="button" className="btn" onClick={() => setRenaming(false)}>{l10n.t("common.cancel")}</button>
            <button type="button" className="btn pri" disabled={rename.saving} onClick={() => onUpdateName(draft, false)}>
              {rename.saving ? l10n.t("common.ios_saving") : l10n.t("common.save")}
            </button>
          </div>
        </Sheet>
      )}
      {leaveOverlaySlot}
    </div>
  );
}

export function ClientPage({
  remote,
  id,
  onBack,
}: {
  remote: Remote;
  id: string;
  onBack: () => void;
}) {
  useRemote(remote);
  const host = useCoreHost(remote);
  const vm = useClientDetailView(host, id);
  const rename = useClientRenameState(host, id);
  const [leaving, setLeaving] = useState(false);

  useEffect(() => {
    void remote.loadDevices(false);
  }, [remote]);

  return (
    <ClientDetailScreenView
      vm={vm}
      rename={rename}
      onBack={onBack}
      onUpdateName={(name, clear) => host.updateClientName(id, name, clear)}
      onOpenLeave={() => setLeaving(true)}
      removeSlot={<DeviceActions remote={remote} id={id} onRemoved={onBack} />}
      leaveOverlaySlot={leaving ? <LeaveAlert onCancel={() => setLeaving(false)} /> : undefined}
    />
  );
}

export function QuotaScreenView({
  vm,
  onBack,
}: {
  vm: QuotaViewModel;
  onBack: () => void;
}) {
  if (!vm.found) {
    return (
      <div className="page set">
        <NavBar onBack={onBack} />
        <div className="center muted" data-late={vm.loading ? "1" : undefined}>
          {renderText(vm.emptyText) || l10n.t("settings.device_not_found")}
        </div>
      </div>
    );
  }

  return (
    <div className="page set">
      <NavBar
        onBack={onBack}
        title={l10n.t("settings.account_quota")}
        sub={`${vm.machineName} · ${vm.reachable ? l10n.t("common.ios_online") : l10n.t("settings.offline")}`}
      />
      <div className="body set">
        {!vm.reachable && (
          <div className="set-banners">
            <Banner kind="warn">{renderText(vm.offlineNotice) || l10n.t("settings.computer_offline_showing_the_last_reading")}</Banner>
          </div>
        )}
        {vm.banner && renderText(vm.banner.message) && (
          <div className="set-banners">
            <Banner kind={bannerKind(vm.banner.severity)}>{renderText(vm.banner.message)}</Banner>
          </div>
        )}
        {vm.sections.map((sec) => (
          <QuotaSectionView key={sec.agent} sec={sec} />
        ))}
        <div className="gf">
          {l10n.t("settings.times_are_shown_in_beijing_time_quotas_belong_to")}
        </div>
      </div>
    </div>
  );
}

export function QuotaPage({
  remote,
  id,
  onBack,
}: {
  remote: Remote;
  id: string;
  go?: Go;
  onBack: () => void;
}) {
  useRemote(remote);
  const host = useCoreHost(remote);
  const vm = useQuotaView(host, id);
  return <QuotaScreenView vm={vm} onBack={onBack} />;
}

function QuotaSectionView({ sec }: { sec: QuotaCliSection }) {
  if (sec.unsupported || sec.state === QuotaSectionState.UNSUPPORTED) {
    return (
      <>
        <div className="label">{sec.agentName}</div>
        <div className="grp">
          <div className="qrow">
            <b>{renderText(sec.emptyOrErrorText)}</b>
            {renderText(sec.usageHint) && <div className="rs">{renderText(sec.usageHint)}</div>}
          </div>
        </div>
      </>
    );
  }
  if (!sec.accounts.length) {
    return (
      <>
        <div className="label">{sec.agentName}</div>
        <div className="grp">
          <div className="qrow">
            <b>{renderText(sec.emptyOrErrorText)}</b>
          </div>
        </div>
      </>
    );
  }
  return (
    <>
      {sec.accounts.map((acc, ai) => (
        <div key={acc.account || ai}>
          {acc.groups.length === 0 ? (
            <>
              <div className="label">{acc.plan ? `${sec.agentName} · ${acc.plan}` : sec.agentName}</div>
              <div className="grp">
                <div className="qrow">
                  <b>{renderText(acc.statusText) || renderText(acc.errorText)}</b>
                  {(renderText(acc.errorText) || renderText(acc.usageHint)) && (
                    <div className="rs">{renderText(acc.errorText) || renderText(acc.usageHint)}</div>
                  )}
                </div>
              </div>
            </>
          ) : (
            acc.groups.map((g, gi) => (
              <div key={gi}>
                <div className="label">
                  {renderText(g.groupLabel) || (acc.plan ? `${sec.agentName} · ${acc.plan}` : sec.agentName)}
                </div>
                <div className="grp">
                  {g.rows.map((r: QuotaRowView) => {
                    const tone = quotaToneClass(r.tone);
                    const cls = ["qrow", tone, (r.stale || r.pastReset) ? "stale" : ""].filter(Boolean).join(" ");
                    const reset = [renderText(r.resetText), renderText(r.resetClockText)].filter(Boolean).join(" · ");
                    return (
                      <div className={cls} key={r.windowName || renderText(r.label)}>
                        <div className="l">
                          <b>{renderText(r.label)}</b>
                          <span className="n">{renderText(r.remainingText)}</span>
                        </div>
                        {r.hasPercent && (
                          <div className="bar">
                            <i style={{ width: `${Math.max(0, Math.min(100, r.remainingPercent))}%` }} />
                          </div>
                        )}
                        {reset && <div className="rs">{reset}</div>}
                        {renderText(r.footnote) && <div className="rs">{renderText(r.footnote)}</div>}
                      </div>
                    );
                  })}
                </div>
              </div>
            ))
          )}
          {acc.groups.length > 0 && renderText(acc.freshnessText) && <div className="gf">{renderText(acc.freshnessText)}</div>}
          {acc.groups.length > 0 && renderText(acc.statusText) && <div className="gf">{renderText(acc.statusText)}</div>}
        </div>
      ))}
    </>
  );
}

export function RemovedScreen({ remote }: { remote: Remote }) {
  useRemote(remote);
  const [result, setResult] = useState("");
  return (
    <div className="center">
      <div className="gone">
        <div className="et">
          {remote.removalVerified ? l10n.t("settings.this_device_was_removed_from_the_account") : l10n.t("settings.cannot_sign_in_to_the_relay_right_now")}
        </div>
        <p className="muted">
          {remote.removalVerified
            ? l10n.t("settings.signed_account_state_confirms_this_device_was_removed_sessions")
            : l10n.t("settings.the_device_may_have_been_removed_the_service_reset")}
        </p>
        <RejoinButton server={remote.account.server}>{l10n.t("settings.rejoin_account")}</RejoinButton>
        <p className="muted small">{l10n.t("settings.the_account_keys_in_this_browser_will_be_deleted")}</p>
        <button
          className="link mid"
          onClick={async () => {
            try {
              await remote.resumeSecurityOperation();
              const st = await remote.getSecurityStatus();
              setResult(
                st.pendingOp
                  ? l10n.t("settings.the_original_operation_is_unfinished_continue_verifying")
                  : l10n.t("settings.the_original_security_operation_is_verified_complete_you_can"),
              );
            } catch (e) {
              setResult(e instanceof Error ? e.message : String(e));
            }
          }}
        >
          {l10n.t("settings.verify_and_continue_security_operation")}
        </button>
        {result && <p className="muted small">{result}</p>}
      </div>
    </div>
  );
}

export function versionText(version: string, commit?: string): string {
  const hash = commit && !version.includes("dev.") ? commit.slice(0, 7) : "";
  return hash ? `${version} · ${hash}` : version;
}

function ThemeSeg({ value, onChange }: { value: ThemePreference; onChange: (t: ThemePreference) => void }) {
  const choices: Array<[ThemePreference, string]> = [
    [ThemePreference.AUTO, l10n.t("settings.theme_auto")],
    [ThemePreference.LIGHT, l10n.t("settings.light")],
    [ThemePreference.DARK, l10n.t("settings.dark")],
  ];
  return (
    <div className="seg" role="radiogroup" aria-label={l10n.t("settings.appearance")}>
      {choices.map(([pref, label]) => (
        <button
          key={pref}
          role="radio"
          aria-checked={value === pref}
          className={value === pref ? "on" : ""}
          onClick={() => onChange(pref)}
        >
          {label}
        </button>
      ))}
    </div>
  );
}
