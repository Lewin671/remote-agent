import * as l10n from "../i18n";
import { t } from "../i18n";
import { AgentMark } from "../agent-mark";
import { Switch } from "../ui";
import { useEffect, useState } from "react";
import { ApprovalMode, type Agent } from "../gen/remoteagent/v1/event_pb";
import type {
  DaemonSettings,
  ListModelsResponse,
} from "../gen/remoteagent/v1/local_pb";
import { agentInfo, defaultsFor, machineVersions, withDefaults } from "../agents";
import {
  agentName,
  ago,
  allAutoUnavailable,
  autoUnavailable,
  defaultModeOf,
  findModel,
  machineAgents,
  modelLabel,
  machineName,
  modeLabel,
  modeName,
  projects,
  shortPath,
} from "../model";
import { machineQuotaViews, quotaSummary } from "../quota";
import { pushPrefs, pushState, setPushPrefs, type PushPrefs } from "../push";
import { errorText, type MachineView, type Remote } from "../remote";
import { DeviceActions, ManagementEntry } from "./DeviceManagement";
import { RejoinButton } from "../Rejoin";
import { LeaveAlert, SecurityGroup } from "./Security";
import { RecoveryNotice } from "./authority-notice";
import { getTheme, setTheme, type Theme } from "../theme";
import { applyUpdate, checkUpdate, useUpdate } from "../update";
import {
  Alert,
  Banner,
  GlassButton,
  Segmented,
  StateMark,
  NavBar,
  PathText,
  Sheet,
  useNow,
  useRemote,
} from "../ui";
import {
  clientDetail,
  clientName,
  clientUsed,
  dayText,
  defaultClientName,
  lastUsed,
  shortId,
  sortClients,
  type DeviceInfo,
} from "../devices";
import { DeviceKind } from "../gen/remoteagent/v1/relay_pb";
import { Check, ChevronLeft, ChevronRight, Globe, Info, Laptop, LoaderCircle, Moon, Smartphone, Tablet } from "../icons";

import { ConnCheck, connWord } from "./ConnCheck";

export function Settings({
  remote,
  go,
  onBack,
}: {
  remote: Remote;
  go: (r: string) => void;
  /** Back to the home, on a phone; a wide window has the gear instead. */
  onBack?: () => void;
}) {
  useRemote(remote);
  useNow(30_000); // for the machines' quota lines
  const [leaving, setLeaving] = useState(false);
  const [checking, setChecking] = useState(false);
  const conn = connWord(remote);
  const machines = [...remote.machines.values()].sort((a, b) =>
    machineName(a).localeCompare(machineName(b)),
  );
  const push = usePush(remote);
  const me = remote.account.deviceId;
  const clients = [...remote.devices.values()].filter(
    (d) => d.kind === DeviceKind.CLIENT,
  );
  const others = clients.filter((d) => d.id !== me);
  return (
    <div className="body set settings">
      {onBack && (
        <div className="tbar">
          <GlassButton label={t("common.back")} onClick={onBack}>
            <ChevronLeft />
          </GlassButton>
        </div>
      )}
      <div className="large">
        <h3>{l10n.t("common.settings")}</h3>
      </div>
      <div className="set-banners">
        {push.top}
        <RecoveryNotice remote={remote} go={go} />
        <ClockBanner remote={remote} />
      </div>
      <div className="label">
        {l10n.t("settings.computer")}<button className="link" onClick={() => go("devices")}>
          {l10n.t("settings.all_devices")}<ChevronRight />
        </button>
      </div>
      {machines.map((m) => (
        <MachineCard key={m.id} m={m} remote={remote} go={go} />
      ))}
      {machines.length === 0 && <p className="gf">{l10n.t("settings.no_computers_yet")}</p>}
      <ManagementEntry remote={remote} go={go} />
      <div className="label">{l10n.t("security.client")}</div>
      <div className="grp">
        <button className="row" onClick={() => go("c/" + me)}>
          <ClientIcon d={remote.devices.get(me)} />
          <span className="row-name">{clientName(remote.devices.get(me), remote.devices, me)}</span>
          <span className="v">{l10n.t("settings.current")}</span>
          <ChevronRight className="chev" />
        </button>
        {others.slice(0, 3).map((d) => (
          <button key={d.id} className="row" onClick={() => go("c/" + d.id)}>
            <ClientIcon d={d} />
            <span className="row-name">{clientName(d, remote.devices)}</span>
            <span className="v">{l10n.t("security.paired")}</span>
            <ChevronRight className="chev" />
          </button>
        ))}
        {others.length > 3 && (
          <button className="row act" onClick={() => go("devices")}>
            {l10n.t("settings.all_clients")}<ChevronRight className="chev" />
          </button>
        )}
      </div>
      <SecurityGroup remote={remote} go={go} />
      {push.group}
      <div className="label">{l10n.t("settings.appearance")}</div>
      <div className="grp">
        <div className="row">
          <span className="ico">
            <Moon />
          </span>
          {l10n.t("settings.theme")}<ThemePicker />
        </div>
      </div>
      <div className="label">{l10n.t("settings.about")}</div>
      <div className="grp">
        <div className="row">
          <span className="ico">
            <Info />
          </span>
          remote-agent
          <span className="v">
            {versionText(
              import.meta.env.VITE_PRODUCT_VERSION,
              import.meta.env.VITE_BUILD_COMMIT,
            )}
          </span>
        </div>
        <UpdateRow />
      </div>
      <div className="label">{l10n.t("settings.account")}</div>
      <div className="grp">
        <div className="row">
          {l10n.t("security.server")}<span className="v">{new URL(remote.account.server).host}</span>
        </div>
        <button className="row" onClick={() => setChecking(true)}>
          {l10n.t("home.connection_check")}<span className="v">
            {conn.text && <StateMark kind={conn.tone} />}
            {conn.text}
          </span>
          <ChevronRight className="chev" />
        </button>
        <button className="row danger" onClick={() => setLeaving(true)}>
          {l10n.t("settings.exit_and_delete_local_keys")}</button>
      </div>
      {leaving && <LeaveAlert onCancel={() => setLeaving(false)} />}
      {checking && (
        <ConnCheck remote={remote} onClose={() => setChecking(false)} />
      )}
    </div>
  );
}

/** A computer in settings (design-system §6 电脑卡与额度): a gray card of
 * its name, whether it is online, and for each CLI it has one line of
 * quota, 「剩 N%」 of the tightest window, when it resets and a thin bar of
 * what is left, orange at 20% and below. One that is not online is only
 * an outline with when it was last seen. */
function MachineCard({ m, remote, go }: { m: MachineView; remote: Remote; go: (r: string) => void }) {
  const now = useNow(30_000);
  const agents = machineAgents(m);
  const online = remote.onlineOf(m);
  const views = machineQuotaViews(m.meta, agents, now, online);
  return (
    <div className={"mach-card" + (online ? "" : " offl")}>
      <button className="mach-head" onClick={() => go("m/" + m.id)}>
        <span className="mach-name">
          <b>{machineName(m)}</b>
          <ChevronRight className="chev" />
        </span>
        <span className="s">
          <StateMark kind={online ? "online" : "off"} />
          {online ? l10n.t("common.ios_online") : m.lastSeenMs ? l10n.t("settings.online", { value: String(ago(m.lastSeenMs)) }) : l10n.t("settings.offline")}
        </span>
      </button>
      {online &&
        agents.map((a) => {
          const q = tightest(views.filter((v) => v.agent === agentInfo(a).agent));
          return (
            <button key={a} className={"qt" + (q.tone ? " " + q.tone : "")} onClick={() => go("q/" + m.id)}>
              <span className="l">
                <b><AgentMark agent={a} named />{agentName(a)}</b>
                <span className="n">{q.left}</span>
                {q.reset && <span>{q.reset}</span>}
              </span>
              {q.percent !== undefined && (
                <span className="bar">
                  <i style={{ width: `${Math.max(0, 100 - Math.min(100, q.percent))}%` }} />
                </span>
              )}
            </button>
          );
        })}
    </div>
  );
}

/** A CLI's tightest window for its card line, or what stands in its place. */
function tightest(views: ReturnType<typeof machineQuotaViews>): { left: string; reset: string; percent?: number; tone: string } {
  const rows = views.flatMap((v) => v.rows).filter((r) => r.percent !== undefined && r.left !== l10n.t("quota.unavailable"));
  if (!rows.length) return { left: quotaSummary(views) || l10n.t("quota.quota_not_available_yet"), reset: "", tone: "" };
  const r = rows.reduce((a, b) => ((b.percent ?? -1) > (a.percent ?? -1) ? b : a));
  return { left: r.left, reset: r.reset.split(" · ")[0], percent: r.percent, tone: r.tone };
}

/** Checking for a newer build of this page, and moving on to it. */
function UpdateRow() {
  const update = useUpdate();
  if (update === "ready")
    return (
      <button className="row" onClick={applyUpdate}>
        {l10n.t("home.update_ready")}<span className="v">{l10n.t("common.reload")}</span>
        <ChevronRight className="chev" />
      </button>
    );
  return (
    <button
      className="row act"
      disabled={update === "checking"}
      onClick={() => void checkUpdate(true)}
    >
      {l10n.t("settings.check_for_updates")}<span className="v">
        {update === "checking" ? (
          <>
            <LoaderCircle />{l10n.t("settings.checking")}</>
        ) : update === "latest" ? (
          l10n.t("settings.up_to_date")
        ) : update === "failed" ? (
          l10n.t("settings.could_not_download_retry_later")
        ) : null}
      </span>
    </button>
  );
}

/** On a machine's page: a line for each CLI's quota, leading to all of it. */
function QuotaSummary({ m, go }: { m: MachineView; go: (r: string) => void }) {
  const agents = machineAgents(m);
  // Reset times pass with nothing else changing.
  const now = useNow(30_000);
  const views = machineQuotaViews(m.meta, agents, now, m.online);
  return (
    <>
      <div className="label">{l10n.t("settings.account_quota")}</div>
      <div className="grp">
        {agents.map((a) => {
          const mine = views.filter((v) => v.agent === agentInfo(a).agent);
          const tone = mine.some((v) => v.rows.some((r) => r.tone === "full"))
            ? " full"
            : mine.some((v) => v.rows.some((r) => r.tone === "warn"))
              ? " warn"
              : "";
          return (
            <button
              className={"row quota-line" + tone}
              key={a}
              onClick={() => go("q/" + m.id)}
            >
              <span className="nw"><AgentMark agent={a} named />{agentName(a)}</span>
              {/* Only the window with the least left (design-system §6); the rest are a tap away. */}
              <span className="v">{quotaSummary(mine)}</span>
              <ChevronRight className="chev" />
            </button>
          );
        })}
      </div>
    </>
  );
}

/** 账户额度: each CLI's account quota on one machine, apart from context
 * use and token counts. Codex reports it in its rollout; Claude Code's is
 * not collected yet, and says so rather than showing anything made up. */
export function QuotaPage({
  remote,
  id,
  onBack,
}: {
  remote: Remote;
  id: string;
  go: (r: string) => void;
  onBack: () => void;
}) {
  useRemote(remote);
  // Reset times pass with nothing else changing.
  const clock = useNow(30_000);
  const m = remote.machines.get(id);
  if (!m)
    return (
      <div className="page">
        <NavBar onBack={onBack} />
        <div className="center muted">{l10n.t("settings.device_not_found")}</div>
      </div>
    );
  const views = machineQuotaViews(m.meta, machineAgents(m), clock, m.online);
  return (
    <div className="page set">
      <NavBar
        onBack={onBack}
        title={l10n.t("settings.account_quota")}
        sub={`${machineName(m)} · ${m.online ? l10n.t("common.ios_online") : l10n.t("settings.offline")}`}
      />
      <div className="body">
        {!m.online && (
          <div className="set-banners">
            <Banner kind="warn">{l10n.t("settings.computer_offline_showing_the_last_reading")}</Banner>
          </div>
        )}
        {views.map((v, i) => (
          <div key={i}>
            {v.error && (
              <div className="set-banners">
                <Banner kind="err">{v.error}</Banner>
              </div>
            )}
            {v.kind === "waiting" ||
            v.kind === "unsupported" ||
            !v.rows.length ? (
              <>
                <div className="label">{v.title}</div>
                <div className="grp">
                  <div className="qrow">
                    <b>{v.head}</b>
                    <div className="rs">
                      {v.detail ||
                        (v.kind === "unsupported"
                          ? (agentInfo(v.agent).usageHint ?? l10n.t("settings.view_usage_in_the_local_cli"))
                          : "")}
                    </div>
                  </div>
                </div>
              </>
            ) : (
              (v.groups.length ? v.groups : [{ title: v.title, rows: v.rows }]).map((g) => (
                <div key={g.title}>
                  <div className="label">{g.title}</div>
                  <div className="grp">
                    {g.rows.map((r) => (
                      <div className={"qrow " + r.tone} key={r.key}>
                        <div className="l">
                          <b>{r.label}</b>
                          <span className="n">{r.left}</span>
                        </div>
                        {r.percent !== undefined && (
                          <div className="bar">
                            <i style={{ width: `${Math.max(0, 100 - Math.min(100, r.percent))}%` }} />
                          </div>
                        )}
                        {r.reset && <div className="rs">{r.reset}</div>}
                        {r.note && <div className="rs">{r.note}</div>}
                      </div>
                    ))}
                  </div>
                </div>
              ))
            )}
            {v.banner && (
              <div className="quota-warning">
                <Banner kind="warn">{v.banner}</Banner>
              </div>
            )}
            {v.freshness && v.rows.length > 0 && <div className="gf">{v.freshness}</div>}
            {v.seen && v.rows.length > 0 && <div className="gf">{v.seen}</div>}

          </div>
        ))}
        <div className="gf">
          {l10n.t("settings.times_are_shown_in_beijing_time_quotas_belong_to")}</div>
      </div>
    </div>
  );
}

/** The server turned this browser away: nothing of the account is shown
 * until it joins again. */
export function RemovedScreen({ remote }: { remote: Remote }) {
  const [result, setResult] = useState("");
  return (
    <div className="center">
      <div className="gone">
        <div className="et">{remote.removalVerified ? l10n.t("settings.this_device_was_removed_from_the_account") : l10n.t("settings.cannot_sign_in_to_the_relay_right_now")}</div>
        <p className="muted small">
          {remote.removalVerified
            ? l10n.t("settings.signed_account_state_confirms_this_device_was_removed_sessions")
            : l10n.t("settings.the_device_may_have_been_removed_the_service_reset")}
        </p>
        <RejoinButton server={remote.account.server}>{l10n.t("settings.rejoin_account")}</RejoinButton>
        <p className="muted small">
          {l10n.t("settings.the_account_keys_in_this_browser_will_be_deleted")}</p>
        <button className="link mid" onClick={async () => {
          try { await remote.resumeSecurityOperation(); setResult(await remote.authority.pending() ? l10n.t("settings.the_original_operation_is_unfinished_continue_verifying") : l10n.t("settings.the_original_security_operation_is_verified_complete_you_can")); }
          catch (e) { setResult(errorText(e)); }
        }}>{l10n.t("settings.verify_and_continue_security_operation")}</button>
        {result && <Banner>{result}</Banner>}
        <button className="link mid" onClick={() => remote.retryLogin()}>
          {l10n.t("session.try_again")}</button>
      </div>
    </div>
  );
}

/** 设备: computers, which run the CLIs, apart from clients, which operate
 * sessions. Paired is not online, watching or owning anything. */
export function DevicesPage({
  remote,
  go,
  onBack,
}: {
  remote: Remote;
  go: (r: string) => void;
  onBack: () => void;
}) {
  useRemote(remote);
  const me = remote.account.deviceId;
  const incomplete = [...remote.devices.values()].filter(d => d.pairingIncomplete);
  const machines = [...remote.machines.values()].filter(m => !remote.devices.get(m.id)?.pairingIncomplete).sort((a, b) =>
    machineName(a).localeCompare(machineName(b)),
  );
  const clients = sortClients(
    [...remote.devices.values()].filter((d) => d.kind === DeviceKind.CLIENT && !d.pairingIncomplete),
    me,
  );
  // The times on this page are as of opening it.
  useEffect(() => {
    void remote.loadDevices();
  }, [remote]);
  return (
    <div className="page set">
      <NavBar onBack={onBack} title={l10n.t("settings.devices")} />
      <div className="body">
        {incomplete.length > 0 && <>
          <div className="label">{l10n.t("settings.unfinished_pairing")}</div>
          <p className="gf">{l10n.t("settings.the_new_device_has_not_finished_joining_if_it")}</p>
          {incomplete.map(d => <div key={d.id}>
            <p className="gf">{d.kind === DeviceKind.MACHINE ? l10n.t("settings.computer") : l10n.t("security.client")}{l10n.t("settings.unfinished_pairing_settings")}</p>
            <DeviceActions remote={remote} id={d.id} incomplete />
          </div>)}
        </>}
        <div className="label">{l10n.t("settings.computers_run_clis")}</div>
        <div className="grp">
          {machines.map((m) => (
            <button key={m.id} className="row" onClick={() => go("m/" + m.id)}>
              <span className="ico">
                <Laptop />
              </span>
              <span className="row-name">{machineName(m)}</span>
              <span className="v">
                {m.online ? (
                  <>
                    <StateMark kind="online" />
                    {l10n.t("common.ios_online")}</>
                ) : (
                  ago(m.lastSeenMs) || l10n.t("settings.offline")
                )}
              </span>
              <ChevronRight className="chev" />
            </button>
          ))}
          {machines.length === 0 && (
            <div className="row help">{l10n.t("settings.no_computers_yet")}</div>
          )}
          {remote.isAuthority && <ManagementEntry remote={remote} go={go} row />}
        </div>
        {!remote.isAuthority && <ManagementEntry remote={remote} go={go} />}
        <div className="label">{l10n.t("settings.clients_control_sessions")}</div>
        <div className="grp">
          {remote.devicesStatus === "error" && (
            <div className="row help">
              {l10n.t("settings.could_not_read_the_device_list")}<button
                className="link"
                onClick={() => void remote.loadDevices()}
              >
                {t("common.retry")}
              </button>
            </div>
          )}
          {(clients.length ? clients : [{ id: me }]).map(
            (d: Partial<DeviceInfo> & { id: string }) => (
              <button
                key={d.id}
                className="row client"
                onClick={() => go("c/" + d.id)}
              >
                <ClientIcon d={d} />
                <span className="client-info">
                  {clientName(d, remote.devices, d.id)}
                  <small>{clientDetail(d)}</small>
                </span>
                <span className="v">
                  {d.id === me ? l10n.t("security.this_device") : d.id === remote.authorityDeviceId ? l10n.t("settings.managing_client") : clientUsed(d)}
                </span>
                {d.id !== me && <ChevronRight className="chev" />}
              </button>
            ),
          )}
        </div>
        <div className="gf">
          {l10n.t("settings.the_time_on_the_right_is_the_last_use")}</div>
      </div>
    </div>
  );
}

/** What a client is, drawn as a computer's row draws its laptop: a
 * browser, a tablet or a phone. */
function ClientIcon({ d }: { d?: Pick<DeviceInfo, "meta"> }) {
  const meta = d?.meta;
  const Icon = meta?.platform === "web" ? Globe : /iPad/.test(meta?.description ?? "") ? Tablet : Smartphone;
  return (
    <span className="ico">
      <Icon />
    </span>
  );
}

/** A client: its name (sealed; synced to the other clients), its ID to
 * check, and leaving on this one. */
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
  const d = remote.devices.get(id);
  const current = id === remote.account.deviceId;
  const [renaming, setRenaming] = useState(false);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [leaving, setLeaving] = useState(false);
  const canEdit =
    current && remote.devicesStatus === "ready" && remote.conn === "online";
  const save = async (name: string) => {
    setBusy(true);
    setError("");
    try {
      await remote.renameDevice(id, name);
      setRenaming(false);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="page set">
      <NavBar
        onBack={onBack}
        title={clientName(d, remote.devices, id)}
        sub={
          current
            ? l10n.t("security.this_device")
            : id === remote.authorityDeviceId
              ? l10n.t("settings.managing_client")
              : l10n.t("settings.paired_client")
        }
      />
      <div className="body">
        <div className="label">{l10n.t("security.client")}</div>
        <div className="grp">
          <button
            className="row"
            disabled={!canEdit}
            onClick={() => {
              setName(d?.meta?.name ?? "");
              setRenaming(true);
            }}
          >
            {l10n.t("settings.name")}<span className="v">{d?.meta?.name || l10n.t("settings.unnamed")}</span>
            {canEdit && <ChevronRight className="chev" />}
          </button>
          {d?.meta?.name && (
            <button
              className="row act"
              disabled={!canEdit || busy}
              onClick={() => void save("")}
            >
              {l10n.t("settings.restore_default_name")}<span className="v">{defaultClientName(d, remote.devices, id)}</span>
            </button>
          )}
          {d?.meta?.description && (
            <div className="row">
              {l10n.t("settings.runs_on")}<span className="v">{d.meta.description}</span>
            </div>
          )}
          <div className="row">
            {l10n.t("settings.last_used")}<span className="v">
              {current ? l10n.t("settings.now") : lastUsed(d) || l10n.t("settings.no_record")}
            </span>
          </div>
          {!!d?.createdMs && (
            <div className="row">
              {l10n.t("settings.added")}<span className="v">{dayText(d.createdMs)}</span>
            </div>
          )}
          <div className="row">
            {l10n.t("settings.device_id")}<span className="v mono">…{shortId(id)}</span>
          </div>
        </div>
        <div className="gf">
          {current
            ? l10n.t("settings.name_changes_sync_to_other_clients")
            : l10n.t("settings.set_the_name_and_notification_preferences_on_that_device")}
        </div>
        {error && (
          <div className="set-banners">
            <Banner kind="err">{l10n.t("settings.name_save_error", { reason: error })}</Banner>
          </div>
        )}
        {current ? (
          <div className="grp">
            <button className="row danger" onClick={() => setLeaving(true)}>
              {l10n.t("settings.exit_and_delete_local_keys")}</button>
          </div>
        ) : (
          <DeviceActions remote={remote} id={id} onRemoved={onBack} />
        )}
      </div>
      {renaming && (
        <Sheet onClose={() => setRenaming(false)} title={l10n.t("settings.client_name")}>
          <input
            className="field"
            autoFocus
            value={name}
            placeholder={defaultClientName(d, remote.devices, id)}
            maxLength={40}
            onChange={(e) => setName(e.target.value)}
            aria-label={l10n.t("settings.client_name")}
          />
          <div className="hint">
            {l10n.t("settings.names_sync_encrypted_the_server_cannot_read_them_leave")}</div>
          <div className="btns sheet-foot">
            <button
              className="btn pri"
              disabled={busy}
              onClick={() => void save(name)}
            >
              {busy ? l10n.t("common.ios_saving") : t("common.save")}
            </button>
          </div>
        </Sheet>
      )}
      {leaving && <LeaveAlert onCancel={() => setLeaving(false)} />}
    </div>
  );
}

function ClockBanner({ remote }: { remote: Remote }) {
  useRemote(remote);
  if (!remote.clockWarning) return null;
  return (
    <div className="pad">
      <Banner kind="warn">{remote.clockWarning}</Banner>
    </div>
  );
}

/** Push: the warning on top for the state it is in, and the switches. */
function usePush(remote: Remote) {
  const [prefs, setPrefs] = useState<PushPrefs>(pushPrefs);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [help, setHelp] = useState(false);
  const [last, setLast] = useState<PushPrefs>();
  const [securitySubscribed, setSecuritySubscribed] = useState(false);
  useEffect(() => {
    if (pushState() !== "ok") return;
    void navigator.serviceWorker.getRegistration().then(reg => reg?.pushManager.getSubscription()).then(sub => setSecuritySubscribed(!!sub)).catch(() => {});
  }, []);
  const st = pushState();
  const change = async (next: PushPrefs) => {
    setBusy(true);
    setError("");
    setLast(next);
    try {
      await setPushPrefs(remote, next);
      setPrefs(next);
      setSecuritySubscribed(true);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };
  let warn;
  if (st === "ios-browser")
    warn = (
      <div className="set-banners">
        <Banner
          action={help ? undefined : l10n.t("settings.details")}
          onAction={() => setHelp(true)}
        >
          {help ? l10n.t("settings.iphone_push_help") : l10n.t("settings.on_iphone_add_to_home_screen_and_open_from")}
        </Banner>
      </div>
    );
  else if (st === "unsupported")
    warn = (
      <div className="set-banners">
        <Banner>{l10n.t("settings.this_browser_does_not_support_push_notifications")}</Banner>
      </div>
    );
  else if (st === "denied")
    warn = (
      <div className="set-banners">
        <Banner kind="warn" action={help ? undefined : l10n.t("settings.how_to_enable")} onAction={() => setHelp(true)}>
          {help ? l10n.t("settings.notification_permission_help") : l10n.t("settings.the_browser_denied_notification_permission")}
        </Banner>
      </div>
    );
  else if (!prefs.attention && !prefs.turns)
    warn = (
      <div className="set-banners">
        <Banner kind="warn" action={l10n.t("settings.enable")} onAction={busy ? undefined : () => void change({ ...prefs, attention: true })}>
          {l10n.t("settings.session_alerts_are_off_you_will_not_be_notified")}</Banner>
      </div>
    );
  const usable = st === "ok" || st === "default";
  const top = (
    <>
      {warn}
      {error && (
        <div className="set-banners">
          <Banner
            kind="err"
            action={t("common.retry")}
            onAction={() => last && void change(last)}
          >
            {l10n.t("settings.subscription_error", { reason: error })}
          </Banner>
        </div>
      )}
    </>
  );
  const group = (
    <>
      <div className="label">{l10n.t("settings.notifications_on_this_client")}</div>
      <div className="grp">
        <div className="row">
          {l10n.t("settings.security_notifications")}<span className="v">{!usable ? l10n.t("settings.cannot_receive") : securitySubscribed ? l10n.t("settings.enabled") : <button className="link" disabled={busy} onClick={() => void change(prefs)}>{l10n.t("settings.enable_security_notifications")}</button>}</span>
        </div>
        <div className="row">
          {l10n.t("settings.when_i_need_to_act")}<Switch
            label={l10n.t("settings.when_i_need_to_act")}
            on={prefs.attention}
            disabled={busy || !usable}
            onChange={(v) => void change({ ...prefs, attention: v })}
          />
        </div>
        <div className="row">
          {l10n.t("settings.when_a_turn_ends")}<Switch
            label={l10n.t("settings.when_a_turn_ends")}
            on={prefs.turns}
            disabled={busy || !usable}
            onChange={(v) => void change({ ...prefs, turns: v })}
          />
        </div>
        <div className="row">
          {l10n.t("settings.scheduled_task_results")}<Switch label={l10n.t("settings.scheduled_task_results")} on={prefs.scheduled !== false} disabled={busy || !usable} onChange={(v) => void change({ ...prefs, scheduled: v })} />
        </div>
      </div>
      <div className="gf">{l10n.t("settings.security_notifications_are_independent_of_session_alerts_delivery_is")}</div>
    </>
  );
  return { top, group };
}

const timeouts = [60, 300, 600, 1800, 3000];

/** Idle close choices in minutes; negative turns it off. */
const idleCloses = [-1, 60, 120, 360, 720, 1440];
/** What the daemon uses for 0. */
const idleCloseDefault = 360;

/** The idle close setting as the daemon applies it: 0 is its default and
 * any negative value is off. */
function idleCloseValue(minutes: number): number {
  if (minutes < 0) return -1;
  return minutes || idleCloseDefault;
}

export function idleCloseLabel(minutes: number): string {
  const m = idleCloseValue(minutes);
  if (m < 0) return l10n.t("settings.idle_close_off");
  return m % 60 === 0 ? l10n.t("quota.hr", { count: m / 60 }) : l10n.t("quota.min", { count: m });
}

/** The choices to show: the fixed ones plus the current value when it is
 * something else, in order. */
export function idleCloseChoices(minutes: number): number[] {
  const cur = idleCloseValue(minutes);
  if (idleCloses.includes(cur)) return idleCloses;
  return [...idleCloses, cur].sort((a, b) => a - b);
}

type Field =
  | { agent: Agent; what: "model" | "effort" | "mode" }
  | { what: "timeout" }
  | { what: "idleClose" };

/** One machine: usage, CLI versions, new-session defaults per CLI, its
 * projects. */
export function MachinePage({
  remote,
  id,
  go,
  onBack,
}: {
  remote: Remote;
  id: string;
  go: (r: string) => void;
  onBack: () => void;
}) {
  useRemote(remote);
  const m = remote.machines.get(id);
  const [st, setSt] = useState<DaemonSettings>();
  const [models, setModels] = useState<Map<Agent, ListModelsResponse>>(
    new Map(),
  );
  const [pick, setPick] = useState<Field>();
  const [error, setError] = useState("");
  const [saveError, setSaveError] = useState<{
    text: string;
    next: DaemonSettings;
  }>();
  const [attempt, setAttempt] = useState(0);
  const agents = machineAgents(m);
  const [defaultAgent, setDefaultAgent] = useState<Agent>(agents[0]);
  const activeAgent = agents.includes(defaultAgent) ? defaultAgent : agents[0];
  useEffect(() => {
    if (!m?.online) return;
    setError("");
    remote.settings(id, true).then(setSt, (e) => setError(errorText(e)));
    for (const a of agents)
      remote.listModels(id, a, true).then(
        (r) => setModels((x) => new Map(x).set(a, r)),
        () => undefined,
      );
  }, [remote, id, m?.online, attempt, agents.join()]); // eslint-disable-line react-hooks/exhaustive-deps
  if (!m)
    return (
      <div className="page">
        <NavBar onBack={onBack} />
        <div className="center muted">{l10n.t("settings.device_not_found")}</div>
      </div>
    );
  const save = async (next: DaemonSettings) => {
    setSaveError(undefined);
    try {
      setSt(await remote.saveSettings(id, next));
      setPick(undefined);
    } catch (e) {
      setSaveError({ text: errorText(e), next });
      setPick(undefined);
    }
  };
  const projs = projects(
    remote.sessions.values(),
    remote.machines.values(),
    remote.projectPrefs().settings,
  ).filter((p) => p.machines.includes(id));
  const name = machineName(m);
  const meta = m.meta;
  const versions = machineVersions(meta);
  const modelName = (a: Agent, slug: string) =>
    modelLabel(models.get(a)?.models, slug, a);

  let sheet;
  if (pick && st) {
    if (pick.what === "timeout")
      sheet = (
        <Sheet onClose={() => setPick(undefined)} title={l10n.t("settings.approval_timeout")}>
          <div className="list">
            {timeouts.map((t) => (
              <button
                key={t}
                className="mi"
                onClick={() => void save({ ...st, approvalTimeoutSeconds: t })}
              >
                <div className="mt">{t / 60}{l10n.t("settings.min")}</div>
                {t === st.approvalTimeoutSeconds && (
                  <span className="ck">
                    <Check />
                  </span>
                )}
              </button>
            ))}
          </div>
          <div className="hint">{l10n.t("settings.unanswered_approvals_are_denied_after_this_time")}</div>
        </Sheet>
      );
    else if (pick.what === "idleClose")
      sheet = (
        <Sheet onClose={() => setPick(undefined)} title={l10n.t("settings.automatically_end_idle_sessions")}>
          <div className="list">
            {idleCloseChoices(st.idleCloseMinutes).map((t) => (
              <button
                key={t}
                className="mi"
                onClick={() => void save({ ...st, idleCloseMinutes: t })}
              >
                <div className="mt">
                  {idleCloseLabel(t)}
                  {t === idleCloseDefault && l10n.t("settings.default")}
                </div>
                {t === idleCloseValue(st.idleCloseMinutes) && (
                  <span className="ck">
                    <Check />
                  </span>
                )}
              </button>
            ))}
          </div>
          <div className="hint">
            {l10n.t("settings.sessions_end_automatically_after_this_much_inactivity_not_running")}</div>
        </Sheet>
      );
    else {
      const a = pick.agent;
      const def = defaultsFor(st, a);
      const list = models.get(a);
      const cur = findModel(
        list?.models,
        def.model || list?.defaultModel || "",
      );
      const follow = l10n.t("settings.follow_configuration", { value: String(agentName(a)) });
      const allNoAuto =
        agentInfo(a).autoMayBeUnavailable && allAutoUnavailable(list?.models);
      const noAuto =
        allNoAuto ||
        (agentInfo(a).autoMayBeUnavailable &&
          autoUnavailable(list?.models, def.model || list?.defaultModel || ""));
      const opts: {
        key: string;
        label: string;
        desc?: string;
        off?: boolean;
        on: boolean;
        next: DaemonSettings;
      }[] =
        pick.what === "model"
          ? [
              {
                key: "",
                label: follow,
                on: !def.model,
                next: withDefaults(st, a, { model: "", effort: "" }),
              },
              ...(list?.models ?? []).map((x) => ({
                key: x.slug,
                label: x.displayName || x.slug,
                on: x.slug === def.model,
                next: withDefaults(st, a, { model: x.slug, effort: "" }),
              })),
            ]
          : pick.what === "effort"
            ? [
                {
                  key: "",
                  label: follow,
                  on: !def.effort,
                  next: withDefaults(st, a, { effort: "" }),
                },
                ...(cur ? cur.efforts : ["low", "medium", "high", "xhigh"]).map(
                  (e) => ({
                    key: e,
                    label: e,
                    on: e === def.effort,
                    next: withDefaults(st, a, { effort: e }),
                  }),
                ),
              ]
            : [
                ApprovalMode.UNSPECIFIED,
                ApprovalMode.ASK,
                ApprovalMode.AUTO,
                ApprovalMode.FULL_ACCESS,
              ].map((x) => ({
                key: String(x),
                label: modeName(x) || follow,
                desc:
                  x === ApprovalMode.AUTO && noAuto
                    ? allNoAuto
                      ? l10n.t("modes.account_unsupported")
                      : l10n.t("new_session.this_model_has_no_auto_mode")
                    : undefined,
                off: x === ApprovalMode.AUTO && noAuto,
                on: x === def.mode,
                next: withDefaults(st, a, { mode: x }),
              }));
      sheet = (
        <Sheet
          onClose={() => setPick(undefined)}
          title={(agents.length > 1 ? agentName(a) + " · " : "") + { model: l10n.t("new_session.default_model"), effort: l10n.t("settings.default_reasoning_effort"), mode: l10n.t("settings.default_approval_mode") }[pick.what]}
        >
          {pick.what === "model" && !list && (
            <div className="hint top">{l10n.t("session.reading_models")}</div>
          )}
          <div className="list">
            {opts.map((o) => (
              <button
                key={o.key}
                className="mi"
                disabled={o.off}
                onClick={() => void save(o.next)}
              >
                <div className="mt">
                  {o.label}
                  {o.desc && <div className="md">{o.desc}</div>}
                </div>
                {o.on && (
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
  }

  return (
    <div className="page set">
      <NavBar onBack={onBack} title={name} sub={m.online ? l10n.t("common.ios_online") : ago(m.lastSeenMs) ? l10n.t("settings.online", { value: String(ago(m.lastSeenMs)) }) : l10n.t("settings.offline")} />
      <div className="body">
        <QuotaSummary m={m} go={go} />
        {versions.length > 0 && (
          <div className="grp">
            {versions.map((v) => (
              <div className="row" key={v.agent}>
                {l10n.t("settings.cli_version", { name: agentName(v.agent) })}<span className="v">
                  {!v.verified && l10n.t("settings.unverified")}
                  {v.version}
                </span>
              </div>
            ))}
            {versions.some((v) => !v.verified) && (
              <div className="row help">
                {l10n.t("settings.version_unverified_run_remote_agent_doctor_on_this_computer")}</div>
            )}
          </div>
        )}
        {!m.online && (
          <div className="label">{l10n.t("settings.computer_settings_offline", { name })}</div>
        )}
        {error && (
          <div className="set-banners">
            <Banner
              kind="err"
              action={t("common.retry")}
              onAction={() => setAttempt((a) => a + 1)}
            >
              {l10n.t("settings.read_error", { reason: error })}
            </Banner>
          </div>
        )}
        {saveError && (
          <div className="set-banners">
            <Banner
              kind="err"
              action={t("common.retry")}
              onAction={() => void save(saveError.next)}
            >
              {l10n.t("settings.save_error", { reason: saveError.text })}
            </Banner>
          </div>
        )}
        {st &&
          agents.length > 0 &&
          (() => {
            const a = activeAgent;
            const def = defaultsFor(st, a);
            const follow = l10n.t("settings.follow_configuration_settings");
            return (
              <>
                <div className="label">
                  {agents.length > 1
                    ? l10n.t("settings.new_project_defaults")
                    : l10n.t("settings.new_project_defaults_settings", { value: String(agentName(a)) })}
                </div>
                {agents.length > 1 && (
                  <Segmented
                    className="cli-tabs full"
                    label={l10n.t("settings.use_cli_defaults_for_new_projects")}
                    value={String(a)}
                    onChange={(v) => setDefaultAgent(Number(v) as Agent)}
                    options={agents.map((x) => ({ value: String(x), label: agentName(x) }))}
                  />
                )}
                <div className="grp">
                  <button
                    className="row"
                    onClick={() => setPick({ agent: a, what: "model" })}
                  >
                    {l10n.t("models.title")}<span className="v">
                      {def.model ? modelName(a, def.model) : follow}
                    </span>
                    <ChevronRight className="chev" />
                  </button>
                  <button
                    className="row"
                    onClick={() => setPick({ agent: a, what: "effort" })}
                  >
                    {l10n.t("models.effort")}<span className="v">
                      {def.effort || follow}
                    </span>
                    <ChevronRight className="chev" />
                  </button>
                  <button
                    className="row"
                    onClick={() => setPick({ agent: a, what: "mode" })}
                  >
                    {l10n.t("modes.title")}<span className="v">
                      {modeName(def.mode) || follow}
                    </span>
                    <ChevronRight className="chev" />
                  </button>
                  {!def.mode && (
                    <div className="row help">
                      {l10n.t("settings.when_unspecified_uses")}{/* 完全放行 is red wherever it is written. */}
                      <span className={defaultModeOf(m, a).mode === ApprovalMode.FULL_ACCESS ? "red" : undefined}>
                        {modeLabel(ApprovalMode.UNSPECIFIED, defaultModeOf(m, a), a, false)}
                      </span>
                    </div>
                  )}
                </div>
              </>
            );
          })()}
        {st && (
          <div className="grp">
            <button
              className="row"
              onClick={() => setPick({ what: "timeout" })}
            >
              {l10n.t("settings.approval_timeout")}<span className="v">
                {l10n.t("quota.min", { count: Math.round(st.approvalTimeoutSeconds / 60) })}</span>
              <ChevronRight className="chev" />
            </button>
            <button
              className="row"
              onClick={() => setPick({ what: "idleClose" })}
            >
              {l10n.t("settings.automatically_end_idle_sessions")}<span className="v">
                {idleCloseLabel(st.idleCloseMinutes)}
              </span>
              <ChevronRight className="chev" />
            </button>
          </div>
        )}
        {st && (
          <ImageStorageSettings
            remote={remote}
            id={id}
            settings={st}
            onSave={setSt}
            online={m.online}
          />
        )}
        {projs.length > 0 && (
          <>
            <div className="label">{l10n.t("schedule.project")}</div>
            <div className="grp">
              {projs.map((p) => (
                <button
                  key={p.key}
                  className="row"
                  onClick={() => go("p/" + encodeURIComponent(p.key))}
                >
                  {p.name}
                  <span className="v"><PathText path={shortPath(p.key)} /></span>
                </button>
              ))}
            </div>
          </>
        )}
        <DeviceActions remote={remote} id={id} machine go={go} onRemoved={onBack} />
      </div>
      {sheet}
    </div>
  );
}

/** The build as the settings show it: a development build's version
 * already ends in its commit, so the commit is not written twice. */
export function versionText(version: string, commit?: string): string {
  const short = commit?.slice(0, 7) ?? "";
  return !short || version.includes(short) ? version : `${version} · ${short}`;
}

function ThemePicker() {
  const [theme, set] = useState<Theme>(getTheme);
  const opts: [Theme, string][] = [
    ["auto", l10n.t("settings.theme_auto")],
    ["light", l10n.t("settings.light")],
    ["dark", l10n.t("settings.dark")],
  ];
  return (
    <div className="seg" role="radiogroup" aria-label={l10n.t("settings.appearance")}>
      {opts.map(([t, name]) => (
        <button
          key={t}
          role="radio"
          aria-checked={t === theme}
          className={t === theme ? "on" : ""}
          onClick={() => {
            setTheme(t);
            set(t);
          }}
        >
          {name}
        </button>
      ))}
    </div>
  );
}

function storageSize(n: bigint | number): string {
  const value = Number(n);
  if (value > 0 && value < 104858) return l10n.t("settings.less_than_0_1_mib");
  return value >= 1073741824
    ? `${(value / 1073741824).toFixed(1)} GiB`
    : `${(value / 1048576).toFixed(1)} MiB`;
}

function ImageStorageSettings({
  remote,
  id,
  settings,
  onSave,
  online,
}: {
  remote: Remote;
  id: string;
  settings: DaemonSettings;
  onSave: (s: DaemonSettings | undefined) => void;
  online: boolean;
}) {
  const usage = remote.imageStorage.get(id);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [clean, setClean] = useState(false);
  const run = async (next: DaemonSettings, clear = false) => {
    setBusy(true);
    setError("");
    try {
      onSave(await remote.saveSettings(id, next, clear));
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };
  const free = usage ? usage.reclaimableBytes + usage.cacheBytes : 0n;
  // What is worth a line: sizes that are not zero.
  const parts = usage
    ? [
        usage.reclaimableBytes > 0n &&
          l10n.t("settings.can_clear", { value: String(storageSize(usage.reclaimableBytes)) }),
        usage.reservedBytes > 0n &&
          l10n.t("settings.uploading", { value: String(storageSize(usage.reservedBytes)) }),
      ]
        .filter(Boolean)
        .join(" · ")
    : "";
  return (
    <>
      <div className="label">{l10n.t("settings.attachment_storage")}</div>
      <div className="grp">
        <div className="row col">
          <div className="line">
            <span>{l10n.t("settings.uploaded_images_and_files")}</span>
            <span className="v">
              {usage ? storageSize(usage.uploadBytes) : l10n.t("projects.reading_directory")}
            </span>
          </div>
          {parts && <div className="hint">{parts}</div>}
        </div>
        <div className="row">
          {l10n.t("settings.storage_limit")}<select
            aria-label={l10n.t("settings.storage_limit")}
            className="v"
            value={String(settings.imageUploadQuotaBytes)}
            disabled={busy || !online}
            onChange={(e) =>
              void run({
                ...settings,
                imageUploadQuotaBytes: BigInt(e.target.value),
              })
            }
          >
            {[
              settings.imageUploadQuotaBytes,
              256n << 20n,
              1n << 30n,
              5n << 30n,
              10n << 30n,
            ]
              .filter((x, i, a) => a.indexOf(x) === i)
              .map((n) => (
                <option key={String(n)} value={String(n)}>
                  {storageSize(n)}
                </option>
              ))}
          </select>
        </div>
        <div className="row">
          {l10n.t("settings.preview_cache")}<span className="v">
            {usage ? storageSize(usage.cacheBytes) : "—"}
          </span>
        </div>
        <button
          className="row act"
          disabled={busy || !online || free === 0n}
          onClick={() => setClean(true)}
        >
          {l10n.t("settings.clear")}<span className="v">
            {usage
              ? free > 0n
                ? l10n.t("settings.can_free", { value: String(storageSize(free)) })
                : l10n.t("settings.nothing_to_clear")
              : ""}
          </span>
        </button>
      </div>
      <div className="gf">
        {l10n.t("settings.attachments_are_stored_on_this_computer_session_files_remain")}</div>
      {error && (
        <div className="set-banners">
          <Banner kind="err">{error}</Banner>
        </div>
      )}
      {clean && (
        <Alert
          title={l10n.t("settings.clear_reclaimable_attachments_and_cache")}
          text={l10n.t("settings.clears_reclaimable_originals_previews_and_unsynced_large_image_cache")}
          ok={l10n.t("settings.clear_settings")}
          onCancel={() => setClean(false)}
          onOk={async () => {
            setClean(false);
            await run(settings, true);
          }}
        />
      )}
    </>
  );
}
