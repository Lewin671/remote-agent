import * as l10n from "../i18n";
// 连接检测 (client-v66): measures the nearest stretch first — this device's
// network, the server (/healthz median of 3), and each online computer via
// clientcore's probe_latency breakdown (network round-trip, computer
// processing, and waiting-for-direct when rerouted). Opened on demand;
// nothing is measured in the background.

import { useCallback, useEffect, useRef, useState } from "react";
import { ConnProbeState, useCoreHost, useSettingsView } from "../core/host";
import { renderText as fmt } from "../core/text";
import { ConnState } from "../gen/remoteagent/core/v1/home_view_pb";
import {
  MachineLatencyState,
  type ConnCheckSummaryView,
  type MachineLatencyView,
  type MachineListCardView,
} from "../gen/remoteagent/core/v1/settings_view_pb";
import { errorText, type Remote } from "../remote";
import { Banner, StateMark, Sheet, type Mark } from "../ui";
import { LoaderCircle } from "../icons";

type ServerPing =
  | { state: "wait" }
  | { state: "ok"; ms: number; slow?: boolean }
  | { state: "fail"; error: string }
  | { state: "skip" };

/** A server /healthz round trip this long is marked slow. */
export const SLOW_SERVER_MS = 500;

/** The middle one of the samples: one slow packet does not make the line slow. */
export function median(samples: number[]): number {
  const s = [...samples].sort((a, b) => a - b);
  return s[Math.floor(s.length / 2)];
}

function formatMs(ms: number): string {
  return Math.max(0, Math.round(ms)).toLocaleString("en-US");
}

/** The link to the server in a word, with its dot: what the sidebar's foot
 * and 设置 say before anything is measured. */
export function connWord(
  cc?: ConnCheckSummaryView,
  showWaiting = !(cc?.displayGrace ?? false),
): { tone: Mark; text: string } {
  if (!cc) return { tone: "off", text: "" };
  if (cc.connState === ConnState.CONNECTED) return { tone: "online", text: l10n.t("connection.connected") };
  if (cc.isConnecting && !showWaiting) return { tone: "off", text: "" };
  if (cc.everOnline) return { tone: "run", text: l10n.t("connection.reconnecting_conn_check") };
  return {
    tone: "off",
    text: cc.failures >= 2 ? l10n.t("connection.unreachable") : l10n.t("connection.connecting_conn_check"),
  };
}

function a11yGroupProps(value: string) {
  return value ? ({ role: "group" as const, "aria-label": value }) : undefined;
}

function serverPingFromView(cc?: ConnCheckSummaryView): ServerPing {
  switch (cc?.serverState) {
    case ConnProbeState.WORKING:
      return { state: "ok", ms: cc.latencyMs, slow: false };
    case ConnProbeState.SLOW:
      return { state: "ok", ms: cc.latencyMs, slow: true };
    case ConnProbeState.UNAVAILABLE:
      return { state: "fail", error: fmt(cc.lastFailureText) || l10n.t("connection.no_response") };
    case ConnProbeState.NOT_CHECKED:
      return { state: "skip" };
    default:
      return { state: "wait" };
  }
}

function ServerResult({ p }: { p: ServerPing }) {
  if (p.state === "wait") return <span className="mv"><LoaderCircle /><em>{l10n.t("connection.checking")}</em></span>;
  if (p.state === "skip") return <span className="mv">{l10n.t("connection.not_checked")}</span>;
  if (p.state === "fail") return <span className="mv red">{p.error}</span>;
  const slow = p.slow ?? p.ms >= SLOW_SERVER_MS;
  const ms = formatMs(p.ms);
  const label = slow
    ? l10n.t("connection.latency_ms_slow", { ms })
    : l10n.t("connection.latency_ms", { ms });
  return (
    <span className="mv">
      <StateMark kind={slow ? "off" : "online"} />
      <em>{label}</em>
    </span>
  );
}

function MachineLatencyValue({ lat }: { lat: MachineLatencyView }) {
  const valueText = fmt(lat.valueText);
  if (lat.state === MachineLatencyState.MEASURING) {
    return (
      <span className="mv">
        <LoaderCircle />
        <em>{valueText || l10n.t("connection.checking")}</em>
      </span>
    );
  }
  if (lat.state === MachineLatencyState.DONE) {
    return (
      <span className="mv">
        <StateMark kind={lat.slow ? "off" : "online"} />
        <em>{valueText}</em>
      </span>
    );
  }
  if (lat.state === MachineLatencyState.FAILED) {
    return <span className="mv red">{valueText}</span>;
  }
  return <span className="mv"><em>{valueText || l10n.t("connection.not_checked")}</em></span>;
}

function MachineRow({ m }: { m: MachineListCardView }) {
  const nameNode = (
    <>
      {m.name}
      {m.disambiguator && (
        <span className="mc-tag mc-tag-inline" aria-label={fmt(m.disambiguatorA11y) || undefined}>
          {m.disambiguator}
        </span>
      )}
    </>
  );

  if (!m.online) {
    return (
      <div className="mi" data-machine-id={m.machineId}>
        <span className="mt">{nameNode}</span>
        <span className="mv"><StateMark kind="off" /><em>{l10n.t("common.ios_offline")}</em></span>
      </div>
    );
  }

  const lat = m.latency;
  if (!lat) {
    return (
      <div className="mi" data-machine-id={m.machineId}>
        <span className="mt">{nameNode}</span>
        <span className="mv">
          <StateMark kind="off" />
          <em>{fmt(m.statusText) || l10n.t("connection.not_checked")}</em>
        </span>
      </div>
    );
  }

  const overrideSummary = fmt(lat.summaryOverrideText);
  const directRow = m.directRow;
  const directSummary = directRow?.visible
    ? [fmt(directRow.statusText), fmt(directRow.detailText)].filter(Boolean).join(" · ")
    : "";
  const summary = overrideSummary || directSummary;
  const segments = lat.segments;
  const note = fmt(lat.noteText);
  const a11yProps = a11yGroupProps(fmt(lat.a11yText));
  const hasBody = segments.length > 0 || !!note;

  if (!hasBody) {
    return (
      <div className="mi" data-machine-id={m.machineId} {...a11yProps}>
        <span className="mt">
          {nameNode}
          {summary && <small className="direct-line">{summary}</small>}
        </span>
        <MachineLatencyValue lat={lat} />
      </div>
    );
  }

  return (
    <div className="mi pc" data-machine-id={m.machineId} {...a11yProps}>
      <div className="conn-top">
        <span className="mt">
          {nameNode}
          {summary && <small className="direct-line">{summary}</small>}
        </span>
        <MachineLatencyValue lat={lat} />
      </div>
      {segments.length > 0 && (
        <dl className="conn-lg">
          {segments.map((seg, idx) => {
            const emphasized = seg.slow || seg.stuck;
            const slowTag = seg.slow ? fmt(seg.slowLabelText) : "";
            const detail = fmt(seg.detailText);
            return (
              <div key={idx} className={emphasized ? "slow" : undefined}>
                <dt>
                  {fmt(seg.labelText)}
                  {slowTag && <span className="tg">{slowTag}</span>}
                </dt>
                <dd>{fmt(seg.valueText)}</dd>
                {detail && <dd className="why">{detail}</dd>}
              </div>
            );
          })}
        </dl>
      )}
      {note && <p className="conn-nt">{note}</p>}
    </div>
  );
}

export function ConnCheckScreenView({
  cc,
  machines: machineCards,
  serverHost,
  pingServer,
  onReportProbe,
  onStartCheck,
  onStopCheck,
  onReconnect,
  onClose,
}: {
  cc?: ConnCheckSummaryView;
  machines: MachineListCardView[];
  serverHost: string;
  pingServer?: (signal?: AbortSignal) => Promise<number>;
  onReportProbe?: (state: ConnProbeState, latencyMs?: number, lastFailure?: string) => void;
  onStartCheck?: () => void;
  onStopCheck?: () => void;
  onReconnect: () => void;
  onClose: () => void;
}) {
  const [server, setServer] = useState<ServerPing>();
  const [serverBusy, setServerBusy] = useState(false);
  const [now, setNow] = useState(Date.now);
  const down = cc?.connState !== ConnState.CONNECTED;
  const connStep = fmt(cc?.connStep);
  const connError = fmt(cc?.lastFailureText);
  const startedAt = cc && cc.connStartedAtMs > 0n ? Number(cc.connStartedAtMs) : undefined;
  const connecting = down && !!connStep && startedAt !== undefined;
  const network = cc?.networkAvailable ?? true;
  const everOnline = cc?.everOnline ?? false;
  useEffect(() => {
    if (!connecting) return;
    setNow(Date.now());
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [connecting, startedAt]);
  const stream = connWord(cc, true);
  const run = useRef(0);
  const activeAbort = useRef<AbortController | null>(null);
  const onReportProbeRef = useRef(onReportProbe);
  onReportProbeRef.current = onReportProbe;
  const onStartCheckRef = useRef(onStartCheck);
  onStartCheckRef.current = onStartCheck;
  const onStopCheckRef = useRef(onStopCheck);
  onStopCheckRef.current = onStopCheck;

  const list = [...machineCards].sort((a, b) => Number(b.online) - Number(a.online) || a.name.localeCompare(b.name));

  const check = useCallback(async () => {
    activeAbort.current?.abort();
    const ctrl = new AbortController();
    activeAbort.current = ctrl;
    const mine = ++run.current;
    onReportProbeRef.current?.(ConnProbeState.CHECKING);
    onStartCheckRef.current?.();
    if (!pingServer) return;
    setServerBusy(true);
    setServer({ state: "wait" });
    try {
      const samples: number[] = [];
      for (let i = 0; i < 3; i++) samples.push(await pingServer(ctrl.signal));
      if (mine !== run.current) return;
      const ms = median(samples);
      setServer({ state: "ok", ms });
      onReportProbeRef.current?.(ms >= SLOW_SERVER_MS ? ConnProbeState.SLOW : ConnProbeState.WORKING, ms);
    } catch (e) {
      if (mine !== run.current) return;
      const msg = errorText(e) || l10n.t("connection.no_response");
      setServer({ state: "fail", error: msg });
      onReportProbeRef.current?.(ConnProbeState.UNAVAILABLE, 0, msg);
    } finally {
      if (mine === run.current) setServerBusy(false);
    }
  }, [pingServer]);

  useEffect(() => {
    if (pingServer || onStartCheckRef.current) {
      void check();
    }
    return () => {
      run.current++;
      activeAbort.current?.abort();
      activeAbort.current = null;
      onStopCheckRef.current?.();
    };
  }, [check, pingServer]);

  const effectiveServer = server ?? serverPingFromView(cc);
  const busy = serverBusy || !!cc?.machinesBusy;

  return (
    <Sheet className="conn-sheet" onClose={onClose} title={l10n.t("settings.server_and_connection")} action={{ label: l10n.t("common.done"), onClick: onClose }}>
      {down && (
        <div className="set-banners">
          <Banner>
            {connecting && <LoaderCircle />}
            {connecting
              ? l10n.t("connection.sec_elapsed", { value: String(everOnline ? l10n.t("connection.reconnecting_conn_check_label") : l10n.t("connection.connecting_conn_check_label")), detail: String(connStep), count: Math.max(0, Math.floor((now - startedAt!) / 1000)), previousFailure: String(connError && l10n.t("connection.last_failure", { value: String(connError) })) })
              : connError
                ? l10n.t("connection.live_push_is_not_connected_session_changes_may_be", { value: String(connError) })
                : network ? l10n.t("connection.live_push_is_not_connected") : l10n.t("connection.this_device_has_no_network_it_will_reconnect_when")}
          </Banner>
        </div>
      )}
      <div className="list conn-check">
        <div className="mi">
          <span className="mt">{l10n.t("connection.live_push")}</span>
          <span className="mv"><StateMark kind={stream.tone} /><em>{stream.text}</em></span>
        </div>
        <div className="mi">
          <span className="mt">{l10n.t("connection.local_network")}</span>
          <span className="mv">
            <StateMark kind={network ? "online" : "off"} />
            <em>{network ? l10n.t("connection.working") : l10n.t("connection.unavailable")}</em>
          </span>
        </div>
        <div className="mi">
          <span className="mt">
            {l10n.t("security.server")}<small>{serverHost}</small>
          </span>
          <ServerResult p={effectiveServer} />
        </div>
        {list.map((m) => <MachineRow key={m.machineId} m={m} />)}
      </div>
      <p className="gf">{fmt(cc?.footerText) || l10n.t("connection.latency_is_one_round_trip_the_server_row_measures")}</p>
      <div className="btns conn-again">
        {down && <button className="btn pri" onClick={onReconnect}>{l10n.t("connection.reconnect_now")}</button>}
        <button className={down ? "btn" : "btn pri"} disabled={busy} onClick={() => void check()}>
          {l10n.t("connection.test_again")}</button>
      </div>
    </Sheet>
  );
}

export function ConnCheck({ remote, onClose }: { remote: Remote; onClose: () => void }) {
  const host = useCoreHost(remote);
  const settingsVm = useSettingsView(host);
  const pingServer = useCallback((signal?: AbortSignal) => remote.pingServer(undefined, signal), [remote]);
  const onReportProbe = useCallback(
    (state: ConnProbeState, latencyMs?: number, lastFailure?: string) =>
      host.reportConnCheckStatus(state, latencyMs, lastFailure),
    [host],
  );
  const onStartCheck = useCallback(() => { host.startConnCheck(); }, [host]);
  const onStopCheck = useCallback(() => { host.stopConnCheck(); }, [host]);
  const onReconnect = useCallback(() => { host.reconnectNow(); }, [host]);
  return (
    <ConnCheckScreenView
      cc={settingsVm.connCheck}
      machines={settingsVm.machines}
      serverHost={new URL(remote.account.server).host}
      pingServer={pingServer}
      onReportProbe={onReportProbe}
      onStartCheck={onStartCheck}
      onStopCheck={onStopCheck}
      onReconnect={onReconnect}
      onClose={onClose}
    />
  );
}
