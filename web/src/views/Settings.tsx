import * as l10n from "../i18n";
import { t } from "../i18n";
import { AgentMark } from "../agent-mark";
import { Switch } from "../ui";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { ApprovalMode, Agent } from "../gen/remoteagent/v1/event_pb";
import { DeviceKind } from "../gen/remoteagent/v1/relay_pb";
import {
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
  MachineSettingsViewModel,
  NotificationsView,
  SettingsViewModel,
} from "../gen/remoteagent/core/v1/settings_view_pb";
import {
  NotificationPermissionState,
  PushRegistrationState,
  ThemePreference,
  routeForMachineSettings,
  routeForProject,
  useClientDetailView,
  useClientRenameState,
  useCoreHost,
  useDevicesView,
  useMachineSettingsView,
  useQuotaView,
  useSettingsView,
  type Go,
} from "../core/host";
import { renderText } from "../core/text";
import type { Remote } from "../remote";
import { MachineRings } from "./MachineRings";
import { ConnectionGroup } from "./ConnectionGroup";
import { DeviceActions, ManagementEntry } from "./DeviceManagement";
import { RejoinButton } from "../Rejoin";
import { LeaveAlert, SecurityGroup } from "./Security";
import { RecoveryNotice } from "./authority-notice";
import { applyUpdate, useUpdate } from "../update";
import {
  Alert,
  Banner,
  NavBar,
  PathText,
  Segmented,
  Sheet,
  StateMark,
  GlassButton,
  useRemote,
  type Mark,
} from "../ui";
import { ConnCheck, connWord } from "./ConnCheck";
import { Check, ChevronLeft, ChevronRight, Globe, Info, Laptop, LoaderCircle, Moon, Smartphone, Tablet } from "../icons";

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
  return (
    <span className="ico">
      {/ipad|tablet/.test(s)
        ? <Tablet />
        : /iphone|ios|android|pixel|phone|mobile/.test(s)
          ? <Smartphone />
          : /macos|mac os|windows|linux|desktop/.test(s)
            ? <Laptop />
            : <Globe />}
    </span>
  );
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

export function SettingsScreenView({ vm, go, onBack, serverHost, connTone, connText, updateState, onCheckForUpdates, onApplyUpdate = applyUpdate, onSetTheme, onSetNotificationPref, onEnablePushNotifications, onOpenConnCheck, onOpenLeave, syncEffectsSlot, recoverySlot, managementSlot, securitySlot, overlaysSlot }: {
  vm: SettingsViewModel; go: Go; onBack?: () => void; serverHost: string; connTone: Mark; connText: string; updateState?: string;
  onCheckForUpdates: () => void; onApplyUpdate?: () => void; onSetTheme: (theme: ThemePreference) => void;
  onSetNotificationPref: (patch: { notifyAttention?: boolean; notifyTurnEnded?: boolean; notifyScheduled?: boolean }) => void;
  onEnablePushNotifications: () => void; onOpenConnCheck?: () => void; onOpenLeave?: () => void;
  syncEffectsSlot?: ReactNode; recoverySlot?: ReactNode; managementSlot?: ReactNode; securitySlot?: ReactNode; overlaysSlot?: ReactNode;
}) {
  const [help, setHelp] = useState(false);
  return (
    <div className="body set settings">
      {syncEffectsSlot}
      {onBack && <div className="tbar"><GlassButton label={t("common.back")} onClick={onBack}><ChevronLeft /></GlassButton></div>}
      <div className="large"><h3>{l10n.t("common.settings")}</h3></div>
      <div className="set-banners">
        <PushBanners notif={vm.notifications} help={help} onHelp={() => setHelp(true)}
          onEnableAttention={() => onSetNotificationPref({ notifyAttention: true })} onRetryPush={onEnablePushNotifications} />
        {recoverySlot}
        {vm.clockSkewed && renderText(vm.clockSkewBannerText) && <Banner kind="warn">{renderText(vm.clockSkewBannerText)}</Banner>}
        {vm.unlinked && renderText(vm.unlinkedBannerText) && <Banner kind="warn">{renderText(vm.unlinkedBannerText)}</Banner>}
      </div>
      <div className="label">
        {l10n.t("settings.computer")}
        <button className="link" onClick={() => go("devices")}>{l10n.t("settings.devices")}</button>
      </div>
      {vm.machines.map((m) => <MachineCard key={m.machineId} m={m} go={go} />)}
      {vm.machines.length === 0 && <p className="gf" data-late={vm.machinesLoading ? "1" : undefined}>{renderText(vm.emptyMachinesText) || l10n.t("settings.no_computers_yet")}</p>}
      {managementSlot}
      <div className="label">{l10n.t("security.client")}</div>
      <div className="grp">
        {vm.recentClients.map((c) => (
          <button key={c.deviceId} className="row" onClick={() => go("c/" + encodeURIComponent(c.deviceId))}>
            <ClientIcon description={c.description} />
            <span className="row-name">{c.displayName || c.description || c.shortId}</span>
            <span className="v">{renderText(c.roleLabel) || renderText(c.lastUsedText) || l10n.t("security.paired")}</span>
            <ChevronRight className="chev" />
          </button>
        ))}
        {vm.hasMoreClients && (
          <button className="row act" onClick={() => go("devices")}>{l10n.t("settings.all_devices")}<ChevronRight className="chev" /></button>
        )}
      </div>
      {securitySlot}
      <NotificationsSection notif={vm.notifications} onSetNotificationPref={onSetNotificationPref} onEnablePushNotifications={onEnablePushNotifications} />
      <div className="label">{l10n.t("settings.appearance")}</div>
      <div className="grp">
        <div className="row"><span className="ico"><Moon /></span>{l10n.t("settings.theme")}<ThemeSeg value={vm.appearance?.theme ?? ThemePreference.AUTO} onChange={onSetTheme} /></div>
      </div>
      <div className="label">{l10n.t("settings.about")}</div>
      <div className="grp">
        <div className="row"><span className="ico"><Info /></span>remote-agent<span className="v">{vm.update?.currentVersion || versionText(import.meta.env.VITE_PRODUCT_VERSION, import.meta.env.VITE_BUILD_COMMIT)}</span></div>
        {vm.update?.hasUpdate || updateState === "ready" ? (
          <button className="row" onClick={onApplyUpdate}>{l10n.t("home.update_ready")}<span className="v">{l10n.t("common.reload")}</span><ChevronRight className="chev" /></button>
        ) : (
          <button className="row act" disabled={vm.update?.checking || updateState === "checking"} onClick={onCheckForUpdates}>
            {l10n.t("settings.check_for_updates")}
            <span className="v">
              {vm.update?.checking || updateState === "checking" ? <><LoaderCircle />{renderText(vm.update?.statusText) || l10n.t("settings.checking")}</>
                : renderText(vm.update?.errorText) ? renderText(vm.update?.errorText)
                : updateState === "latest" ? l10n.t("settings.up_to_date") : renderText(vm.update?.statusText)}
            </span>
          </button>
        )}
      </div>
      <div className="label">{l10n.t("settings.account")}</div>
      <div className="grp">
        <div className="row">{l10n.t("security.server")}<span className="v">{serverHost}</span></div>
        <button className="row" onClick={onOpenConnCheck}>
          {l10n.t("home.connection_check")}
          <span className="v">{connText && <StateMark kind={connTone} />}{connText}</span>
          <ChevronRight className="chev" />
        </button>
        <button className="row danger" onClick={onOpenLeave}>{l10n.t("settings.exit_and_delete_local_keys")}</button>
      </div>
      {overlaysSlot}
    </div>
  );
}

export function Settings({ remote, go, onBack }: { remote: Remote; go: Go; onBack?: () => void }) {
  useRemote(remote);
  const host = useCoreHost(remote), vm = useSettingsView(host), update = useUpdate(), conn = connWord(remote);
  const [checking, setChecking] = useState(false), [leaving, setLeaving] = useState(false);
  return (
    <SettingsScreenView vm={vm} go={go} onBack={onBack} serverHost={new URL(remote.account.server).host}
      connTone={conn.tone} connText={conn.text} updateState={update}
      onCheckForUpdates={() => host.checkForUpdates()} onSetTheme={(t) => host.setAppearancePref(t)}
      onSetNotificationPref={(p) => host.setNotificationPref(p)} onEnablePushNotifications={() => host.enablePushNotifications()}
      onOpenConnCheck={() => setChecking(true)} onOpenLeave={() => setLeaving(true)}
      syncEffectsSlot={<SyncClientEffects remote={remote} />} recoverySlot={<RecoveryNotice remote={remote} go={go} />}
      managementSlot={<ManagementEntry remote={remote} go={go} />} securitySlot={<SecurityGroup remote={remote} go={go} />}
      overlaysSlot={<>{leaving && <LeaveAlert onCancel={() => setLeaving(false)} />}{checking && <ConnCheck remote={remote} onClose={() => setChecking(false)} />}</>}
    />
  );
}

function MachineCard({ m, go }: { m: MachineListCardView; go: Go }) {
  const host = routeForMachineSettings(m.machineId);
  const status = renderText(m.statusText);
  return (
    <div className={"mach-card" + (m.online ? "" : " offl")}>
      <button className="mach-head" onClick={() => go(host)}>
        <span className="mach-name">
          <b>{m.name}</b>
          <ChevronRight className="chev" />
        </span>
        <span className="s">
          <StateMark kind={m.online ? "online" : "off"} />
          {status}
        </span>
      </button>
      {m.quotaSummaries.map((sec) => {
        const row = sec.summaryRow;
        if (!row) return null;
        const tone = quotaToneClass(sec.summaryTone || row.tone);
        const reset = renderText(row.resetText);
        const remaining = renderText(row.remainingText) || renderText(sec.summaryLine);
        return (
          <button
            key={sec.agent}
            className={"qt" + (tone ? " " + tone : "")}
            onClick={() => go("q/" + encodeURIComponent(m.machineId))}
          >
            <span className="l">
              <b>{sec.agentName}</b>
              <span className="n">{remaining}</span>
              {reset && <span>{reset}</span>}
            </span>
            {row.hasPercent && (
              <span className="bar">
                <i style={{ width: `${Math.max(0, Math.min(100, row.remainingPercent))}%` }} />
              </span>
            )}
          </button>
        );
      })}
    </div>
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
  let warn = null;
  if (notif.permission === NotificationPermissionState.NEEDS_HOME_SCREEN) {
    warn = (
      <div className="set-banners">
        <Banner action={help ? undefined : l10n.t("settings.details")} onAction={onHelp}>
          {help ? l10n.t("settings.iphone_push_help") : l10n.t("settings.on_iphone_add_to_home_screen_and_open_from")}
        </Banner>
      </div>
    );
  } else if (notif.permission === NotificationPermissionState.UNSUPPORTED) {
    warn = (
      <div className="set-banners">
        <Banner>{l10n.t("settings.this_browser_does_not_support_push_notifications")}</Banner>
      </div>
    );
  } else if (notif.permission === NotificationPermissionState.DENIED) {
    warn = (
      <div className="set-banners">
        <Banner action={help ? undefined : l10n.t("settings.how_to_enable")} onAction={onHelp}>
          {help ? l10n.t("settings.notification_permission_help") : l10n.t("settings.the_browser_denied_notification_permission")}
        </Banner>
      </div>
    );
  } else if (!notif.notifyAttention && !notif.notifyTurnEnded) {
    warn = (
      <div className="set-banners">
        <Banner
          action={l10n.t("settings.enable")}
          onAction={notif.pushState === PushRegistrationState.REGISTERING ? undefined : onEnableAttention}
        >
          {l10n.t("settings.session_alerts_are_off_you_will_not_be_notified")}
        </Banner>
      </div>
    );
  }

  return (
    <>
      {warn}
      {notif.errorDetail && (
        <div className="set-banners">
          <Banner
            kind="err"
            action={t("common.retry")}
            onAction={onRetryPush}
          >
            {l10n.t("settings.subscription_error", { reason: notif.errorDetail })}
          </Banner>
        </div>
      )}
    </>
  );
}

function NotificationsSection({
  notif,
  onSetNotificationPref,
  onEnablePushNotifications,
}: {
  notif?: NotificationsView;
  onSetNotificationPref: (patch: { notifyAttention?: boolean; notifyTurnEnded?: boolean; notifyScheduled?: boolean }) => void;
  onEnablePushNotifications: () => void;
}) {
  if (!notif) return null;
  const usable = notif.permission === NotificationPermissionState.GRANTED || notif.permission === NotificationPermissionState.UNSPECIFIED;
  const securitySubscribed = notif.permission === NotificationPermissionState.GRANTED;
  const busy = notif.pushState === PushRegistrationState.REGISTERING;

  return (
    <>
      <div className="label">{l10n.t("settings.notifications_on_this_client")}</div>
      <div className="grp">
        <div className="row">
          {l10n.t("settings.security_notifications")}
          <span className="v">
            {!usable ? (
              l10n.t("settings.cannot_receive")
            ) : securitySubscribed ? (
              l10n.t("settings.enabled")
            ) : (
              <button className="link" disabled={busy} onClick={onEnablePushNotifications}>
                {l10n.t("settings.enable_security_notifications")}
              </button>
            )}
          </span>
        </div>
        <div className="row">
          {l10n.t("settings.when_i_need_to_act")}
          <Switch
            on={usable && notif.notifyAttention}
            disabled={!usable || busy}
            onChange={(v) => onSetNotificationPref({ notifyAttention: v })}
            label={l10n.t("settings.when_i_need_to_act")}
          />
        </div>
        <div className="row">
          {l10n.t("settings.when_a_turn_ends")}
          <Switch
            on={usable && notif.notifyTurnEnded}
            disabled={!usable || busy}
            onChange={(v) => onSetNotificationPref({ notifyTurnEnded: v })}
            label={l10n.t("settings.when_a_turn_ends")}
          />
        </div>
        <div className="row">
          {l10n.t("settings.scheduled_task_results")}
          <Switch
            on={usable && notif.notifyScheduled}
            disabled={!usable || busy}
            onChange={(v) => onSetNotificationPref({ notifyScheduled: v })}
            label={l10n.t("settings.scheduled_task_results")}
          />
        </div>
      </div>
      <div className="gf">{l10n.t("settings.security_notifications_are_independent_of_session_alerts_delivery_is")}</div>
    </>
  );
}

function QuotaSummary({ sections, go, machineId }: { sections: QuotaCliSection[]; go: Go; machineId: string }) {
  if (!sections.length) return null;
  return (
    <>
      <div className="label">{l10n.t("settings.account_quota")}</div>
      <div className="grp">
        {sections.map((sec) => {
          const tone = quotaToneClass(sec.summaryTone || sec.summaryRow?.tone || QuotaTone.UNSPECIFIED);
          const line = renderText(sec.summaryLine) || renderText(sec.emptyOrErrorText);
          return (
            <button
              key={sec.agent}
              className={"row quota-line" + (tone ? " " + tone : "")}
              onClick={() => go("q/" + encodeURIComponent(machineId))}
            >
              <span className="nw"><AgentMark agent={sec.agent} named />{sec.agentName}</span>
              <span className="v">{line}</span>
              <ChevronRight className="chev" />
            </button>
          );
        })}
      </div>
    </>
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
      <div className="page">
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
      <div className="body">
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
              setResult(
                await remote.authority.pending()
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

export function DevicesScreenView({
  vm,
  go,
  onBack,
  onRetry,
  devicesLoadError = false,
  unfinishedSlot,
  managementInGroupSlot,
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
  return (
    <div className="page set">
      <NavBar onBack={onBack} title={l10n.t("settings.devices")} />
      <div className="body">
        {unfinishedSlot}
        <div className="label">{l10n.t("settings.computers_run_clis")}</div>
        <div className="grp">
          {vm.computers.map((m) => (
            <button key={m.machineId} className="row" onClick={() => go(routeForMachineSettings(m.machineId))}>
              <span className="ico"><Laptop /></span>
              <span className="row-name">{m.name}</span>
              <span className="v">
                {m.online && <StateMark kind="online" />}
                {renderText(m.statusText)}
              </span>
              <ChevronRight className="chev" />
            </button>
          ))}
          {vm.computers.length === 0 && (
            <div className="row help" data-late={vm.machinesLoading ? "1" : undefined}>
              {renderText(vm.emptyComputersText) || l10n.t("settings.no_computers_yet")}
            </div>
          )}
          {vm.isManager && managementInGroupSlot}
        </div>
        {!vm.isManager && managementStandaloneSlot}
        <div className="label">{l10n.t("settings.clients_control_sessions")}</div>
        <div className="grp">
          {(vm.errorText || devicesLoadError) && (
            <div className="row help">
              {renderText(vm.errorText) || l10n.t("settings.could_not_read_the_device_list")}
              <button className="link" onClick={onRetry}>
                {t("common.retry")}
              </button>
            </div>
          )}
          {vm.clients.map((c: ClientRowView) => (
            <button key={c.deviceId} className="row client" onClick={() => go("c/" + encodeURIComponent(c.deviceId))}>
              <ClientIcon description={c.description} />
              <span className="client-info">
                {c.displayName || c.description || c.shortId}
                {c.description && <small>{c.description}</small>}
              </span>
              <span className="v">{renderText(c.roleLabel) || renderText(c.lastUsedText)}</span>
              {!c.isSelf && <ChevronRight className="chev" />}
            </button>
          ))}
        </div>
        <div className="gf">{l10n.t("settings.the_time_on_the_right_is_the_last_use")}</div>
      </div>
    </div>
  );
}

export function DevicesPage({
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
        const kind = remote.devices.get(d.deviceId)?.kind;
        return (
          <div key={d.deviceId}>
            <p className="gf">
              {kind === DeviceKind.MACHINE ? l10n.t("settings.computer") : l10n.t("security.client")}
              {l10n.t("settings.unfinished_pairing_settings")}
            </p>
            <DeviceActions remote={remote} id={d.deviceId} incomplete />
          </div>
        );
      })}
    </>
  );

  return (
    <DevicesScreenView
      vm={vm}
      go={go}
      onBack={onBack}
      onRetry={() => void remote.loadDevices()}
      devicesLoadError={remote.devicesStatus === "error"}
      unfinishedSlot={unfinishedSlot}
      managementInGroupSlot={<ManagementEntry remote={remote} go={go} row />}
      managementStandaloneSlot={<ManagementEntry remote={remote} go={go} />}
    />
  );
}

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
      <div className="page">
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
      <div className="body">
        <div className="label">{l10n.t("security.client")}</div>
        <div className="grp">
          <button
            className="row"
            disabled={!vm.canRename}
            onClick={() => {
              if (!vm.canRename) return;
              setDraft(vm.customName || vm.defaultName || vm.description);
              setRenaming(true);
            }}
          >
            {l10n.t("settings.name")}
            <span className="v">{vm.customName || l10n.t("settings.unnamed")}</span>
            {vm.canRename && <ChevronRight className="chev" />}
          </button>
          {vm.canRestoreDefault && (
            <button
              className="row act"
              disabled={rename.saving}
              onClick={() => onUpdateName("", true)}
            >
              {l10n.t("settings.restore_default_name")}
              <span className="v">{vm.defaultName}</span>
            </button>
          )}
          {vm.description && (
            <div className="row">
              {l10n.t("settings.runs_on")}
              <span className="v">{vm.description}</span>
            </div>
          )}
          <div className="row">
            {l10n.t("settings.last_used")}
            <span className="v">{renderText(vm.lastUsedText) || "—"}</span>
          </div>
          {renderText(vm.addedText) && (
            <div className="row">
              {l10n.t("settings.added")}
              <span className="v">{renderText(vm.addedText)}</span>
            </div>
          )}
          <div className="row">
            {l10n.t("settings.device_id")}
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
            <button className="row danger" onClick={onOpenLeave}>
              {l10n.t("settings.exit_and_delete_local_keys")}
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
            <button className="btn" onClick={() => setRenaming(false)}>{l10n.t("common.cancel")}</button>
            <button className="btn pri" disabled={rename.saving} onClick={() => onUpdateName(draft, false)}>
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

type MachineSheet = "model" | "effort" | "mode" | "timeout" | "idle" | "image-quota";

export function MachineSettingsScreenView({ id, vm, section, go, onBack, onRetrySettings, onSelectAgent, onUpdateAgentDefaults, onUpdateDaemonSettings, removedSlot, ringsSlot, connectionSlot, rejoinSlot, deviceActionsSlot }: {
  id: string; vm: MachineSettingsViewModel; section?: string; go: Go; onBack: () => void; onRetrySettings: () => void;
  onSelectAgent: (agent: Agent) => void;
  onUpdateAgentDefaults: (agent: Agent, patch: { model?: string; effort?: string; mode?: ApprovalMode }) => void;
  onUpdateDaemonSettings: (patch: { approvalTimeoutSeconds?: number; idleCloseMinutes?: number; imageUploadQuotaBytes?: bigint; cleanImages?: boolean }) => void;
  removedSlot?: ReactNode; ringsSlot?: ReactNode; connectionSlot?: ReactNode; rejoinSlot?: ReactNode; deviceActionsSlot?: ReactNode;
}) {
  const [sheet, setSheet] = useState<MachineSheet>();
  const [clearConfirm, setClearConfirm] = useState(false);

  useEffect(() => {
    if (section === "storage" && vm.attachmentStorage?.loaded) {
      document.getElementById("attachment-storage")?.scrollIntoView({ behavior: "smooth" });
    }
  }, [section, vm.attachmentStorage?.loaded]);

  if (!vm.found) {
    return (
      <div className="page">
        <NavBar onBack={onBack} />
        <div className="center muted" data-late={vm.loading ? "1" : undefined}>
          {renderText(vm.offlineOrMismatchBanner) || l10n.t("settings.device_not_found")}
        </div>
      </div>
    );
  }
  if (!vm.member && removedSlot) return <>{removedSlot}</>;

  const ad = vm.agentDefaults;
  const timeoutChoice = vm.approvalTimeoutChoices.find((c) => c.selected) ?? vm.approvalTimeoutChoices.find((c) => c.seconds === vm.selectedApprovalTimeoutSeconds);
  const idleChoice = vm.idleCloseChoices.find((c) => c.selected) ?? vm.idleCloseChoices.find((c) => c.minutes === vm.selectedIdleCloseMinutes);
  const storage = vm.attachmentStorage, name = vm.name || id;

  return (
    <div className="page set">
      <NavBar onBack={onBack} title={name} sub={vm.online ? l10n.t("common.ios_online") : l10n.t("settings.offline")} />
      <div className="body">
        {renderText(vm.offlineOrMismatchBanner) && <div className="set-banners"><Banner kind="warn">{renderText(vm.offlineOrMismatchBanner)}</Banner></div>}
        {renderText(vm.removedBanner) && <div className="set-banners"><Banner kind="warn">{renderText(vm.removedBanner)}</Banner></div>}
        {renderText(vm.settingsError) && <div className="set-banners"><Banner kind="err" action={t("common.retry")} onAction={onRetrySettings}>{renderText(vm.settingsError)}</Banner></div>}
        {renderText(vm.saveError) && <div className="set-banners"><Banner kind={vm.saveUnknown ? "warn" : "err"}>{renderText(vm.saveError)}</Banner></div>}
        {vm.showStatusEntry && ringsSlot}
        <QuotaSummary sections={vm.quotaRows} go={go} machineId={id} />
        {connectionSlot}
        {vm.cliVersions.length > 0 && (
          <div className="grp">
            {vm.cliVersions.map((v) => (
              <div className="row" key={v.agent}>{l10n.t("settings.cli_version", { name: v.name })}<span className="v">{!v.verified && l10n.t("settings.unverified")}{v.version}</span></div>
            ))}
            {vm.hasUnverifiedCli && <div className="row help">{l10n.t("settings.version_unverified_run_remote_agent_doctor_on_this_computer")}</div>}
          </div>
        )}
        {vm.availableAgents.length > 0 && (
          <div className="label">{vm.availableAgents.length > 1 ? l10n.t("settings.new_project_defaults") : l10n.t("settings.new_project_defaults_settings", { value: agentDisplayName(vm.availableAgents[0]) })}</div>
        )}
        {vm.availableAgents.length > 1 && (
          <Segmented className="cli-tabs full" label={l10n.t("settings.new_project_defaults")} value={String(vm.selectedAgent)}
            onChange={(next) => onSelectAgent(Number(next) as Agent)}
            options={vm.availableAgents.map((a) => ({ value: String(a), label: ad && ad.agent === a && ad.agentName ? ad.agentName : agentDisplayName(a) }))}
          />
        )}
        {ad && (
          <div className="grp">
            <button className="row" disabled={!vm.editable} onClick={() => setSheet("model")}>
              {l10n.t("models.title")}<span className="v">{renderText(ad.selectedModelLabel) || l10n.t("models.default")}</span><ChevronRight className="chev" />
            </button>
            {ad.effortChoices.length > 0 && (
              <button className="row" disabled={!vm.editable} onClick={() => setSheet("effort")}>
                {l10n.t("models.effort")}<span className="v">{renderText(ad.effortChoices.find((e) => e.selected)?.label) || l10n.t("models.default")}</span><ChevronRight className="chev" />
              </button>
            )}
            <button className="row" disabled={!vm.editable} onClick={() => setSheet("mode")}>
              {l10n.t("modes.title")}<span className="v">{renderText(ad.modeChoices.find((mc) => mc.selected)?.label) || l10n.t("models.default")}</span><ChevronRight className="chev" />
            </button>
            {renderText(ad.autoUnavailableNotice) && <div className="row help">{renderText(ad.autoUnavailableNotice)}</div>}
          </div>
        )}
        <div className="grp">
          <button className="row" disabled={!vm.editable} onClick={() => setSheet("timeout")}>
            {l10n.t("settings.approval_timeout")}<span className="v">{timeoutChoice ? renderText(timeoutChoice.label) : "—"}</span><ChevronRight className="chev" />
          </button>
          <button className="row" disabled={!vm.editable} onClick={() => setSheet("idle")}>
            {l10n.t("settings.automatically_end_idle_sessions")}<span className="v">{idleChoice ? renderText(idleChoice.label) : "—"}</span><ChevronRight className="chev" />
          </button>
        </div>
        {storage?.loaded && (
          <AttachmentStorageGroup storage={storage} editable={vm.editable && !vm.savePending}
            onSelectQuota={(bytes) => onUpdateDaemonSettings({ imageUploadQuotaBytes: bytes })} onOpenClear={() => setClearConfirm(true)} />
        )}
        {vm.projects.length > 0 && (
          <>
            <div className="label">{l10n.t("schedule.project")}</div>
            <div className="grp">
              {vm.projects.map((p) => (
                <button key={p.projectKey} className="row" onClick={() => go(routeForProject(p.projectKey))}>
                  {p.name}<span className="v"><PathText path={p.path} /></span><ChevronRight className="chev" />
                </button>
              ))}
            </div>
          </>
        )}
        {vm.management?.canRejoin && rejoinSlot}
        {deviceActionsSlot}
      </div>
      {sheet && renderMachineSheet(sheet, vm, () => setSheet(undefined),
        (model) => { onUpdateAgentDefaults(vm.selectedAgent, { model }); setSheet(undefined); },
        (effort) => { onUpdateAgentDefaults(vm.selectedAgent, { effort }); setSheet(undefined); },
        (mode) => { onUpdateAgentDefaults(vm.selectedAgent, { mode }); setSheet(undefined); },
        (seconds) => { onUpdateDaemonSettings({ approvalTimeoutSeconds: seconds }); setSheet(undefined); },
        (minutes) => { onUpdateDaemonSettings({ idleCloseMinutes: minutes }); setSheet(undefined); },
      )}
      {clearConfirm && (
        <Alert title={l10n.t("settings.clear_reclaimable_attachments_and_cache")} text={l10n.t("settings.clears_reclaimable_originals_previews_and_unsynced_large_image_cache")}
          ok={l10n.t("settings.clear_settings")} onCancel={() => setClearConfirm(false)}
          onOk={() => { setClearConfirm(false); onUpdateDaemonSettings({ cleanImages: true }); }} />
      )}
    </div>
  );
}

export function MachinePage({ remote, id, section, go, onBack }: { remote: Remote; id: string; section?: string; go: Go; onBack: () => void }) {
  useRemote(remote);
  const host = useCoreHost(remote), vm = useMachineSettingsView(host, id);
  return (
    <MachineSettingsScreenView id={id} vm={vm} section={section} go={go} onBack={onBack}
      onRetrySettings={() => host.machineSettingsOpen(id)} onSelectAgent={(a) => host.machineSettingsSelectAgent(id, a)}
      onUpdateAgentDefaults={(a, p) => host.updateMachineAgentDefaults(id, a, p)}
      onUpdateDaemonSettings={(p) => host.updateMachineDaemonSettings(id, p)}
      removedSlot={<RemovedScreen remote={remote} />} ringsSlot={<MachineRings remote={remote} id={id} go={go} />}
      connectionSlot={<ConnectionGroup remote={remote} id={id} connection={vm.connection} />}
      rejoinSlot={<div className="set-banners"><RejoinButton server={remote.account.server}>{l10n.t("settings.rejoin_account")}</RejoinButton></div>}
      deviceActionsSlot={<DeviceActions remote={remote} id={id} machine go={go} onRemoved={onBack} />}
    />
  );
}

function ChoiceSheet({ title, hint, items, onClose }: {
  title: string; hint?: string; onClose: () => void;
  items: { key: string | number; label: string; desc?: string; disabled?: boolean; selected: boolean; onPick: () => void }[];
}) {
  return (
    <Sheet onClose={onClose} title={title}>
      <div className="list">
        {items.map((o) => (
          <button key={o.key} className="mi" disabled={o.disabled} onClick={o.onPick}>
            <div className="mt">{o.label}{o.desc && <div className="md">{o.desc}</div>}</div>
            {o.selected && <span className="ck"><Check /></span>}
          </button>
        ))}
      </div>
      {hint && <div className="hint">{hint}</div>}
    </Sheet>
  );
}

function renderMachineSheet(
  sheet: MachineSheet, vm: MachineSettingsViewModel, onClose: () => void,
  onSelectModel: (m: string) => void, onSelectEffort: (e: string) => void, onSelectMode: (m: ApprovalMode) => void,
  onSelectTimeout: (s: number) => void, onSelectIdle: (m: number) => void,
) {
  const ad = vm.agentDefaults;
  if (sheet === "model" && ad) {
    return <ChoiceSheet onClose={onClose} title={l10n.t("models.title")} items={ad.modelChoices.map((o) => ({ key: o.id, label: renderText(o.label), desc: o.desc, selected: o.selected, onPick: () => onSelectModel(o.id) }))} />;
  }
  if (sheet === "effort" && ad) {
    return <ChoiceSheet onClose={onClose} title={l10n.t("models.effort")} items={ad.effortChoices.map((e) => ({ key: e.id, label: renderText(e.label), selected: e.selected, onPick: () => onSelectEffort(e.id) }))} />;
  }
  if (sheet === "mode" && ad) {
    return <ChoiceSheet onClose={onClose} title={l10n.t("modes.title")} items={ad.modeChoices.map((m) => ({ key: m.mode, label: renderText(m.label), desc: renderText(m.disabledReason), disabled: m.disabled, selected: m.selected, onPick: () => onSelectMode(m.mode) }))} />;
  }
  if (sheet === "timeout") {
    return <ChoiceSheet onClose={onClose} title={l10n.t("settings.approval_timeout")} hint={l10n.t("settings.unanswered_approvals_are_denied_after_this_time")}
      items={vm.approvalTimeoutChoices.map((c) => ({ key: c.seconds, label: renderText(c.label), selected: c.selected, onPick: () => onSelectTimeout(c.seconds) }))} />;
  }
  if (sheet === "idle") {
    return <ChoiceSheet onClose={onClose} title={l10n.t("settings.automatically_end_idle_sessions")} hint={l10n.t("settings.sessions_end_automatically_after_this_much_inactivity_not_running")}
      items={vm.idleCloseChoices.map((c) => ({ key: c.minutes, label: renderText(c.label), selected: c.selected, onPick: () => onSelectIdle(c.minutes) }))} />;
  }
  return null;
}

function agentDisplayName(a: Agent): string {
  switch (a) {
    case Agent.CLAUDE: return "Claude Code";
    case Agent.CODEX: return "Codex";
    case Agent.ANTIGRAVITY: return "Antigravity";
    default: return "";
  }
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

function AttachmentStorageGroup({
  storage,
  editable,
  onSelectQuota,
  onOpenClear,
}: {
  storage: NonNullable<MachineSettingsViewModel["attachmentStorage"]>;
  editable: boolean;
  onSelectQuota: (bytes: bigint) => void;
  onOpenClear: () => void;
}) {
  return (
    <>
      <div className="label" id="attachment-storage">{l10n.t("settings.attachment_storage")}</div>
      <div className="grp">
        <div className="row col">
          <div className="line">
            <span>{l10n.t("settings.uploaded_images_and_files")}</span>
            <span className="v">{renderText(storage.uploadedText) || "—"}</span>
          </div>
          {renderText(storage.uploadedDetail) && <div className="hint">{renderText(storage.uploadedDetail)}</div>}
        </div>
        <div className="row">
          {l10n.t("settings.storage_limit")}
          <select
            aria-label={l10n.t("settings.storage_limit")}
            className="v"
            value={String(storage.selectedQuotaBytes)}
            disabled={!editable}
            onChange={(e) => onSelectQuota(BigInt(e.target.value))}
          >
            {storage.quotaChoices.map((c) => (
              <option key={String(c.bytes)} value={String(c.bytes)}>
                {renderText(c.label)}
              </option>
            ))}
          </select>
        </div>
        <div className="row">
          {l10n.t("settings.preview_cache")}
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
        <button className="row act" disabled={!editable || !storage.canClear || storage.cleaning} onClick={onOpenClear}>
          {l10n.t("settings.clear")}
          {renderText(storage.clearButtonLabel) && <span className="v">{renderText(storage.clearButtonLabel)}</span>}
        </button>
      </div>
      <div className="gf">{renderText(storage.footerText) || l10n.t("settings.attachments_are_stored_on_this_computer_session_files_remain")}</div>
    </>
  );
}
