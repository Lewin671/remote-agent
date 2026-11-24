import * as l10n from "../i18n";
// 连接检测 (client-v34): how long a round trip takes now on each stretch of
// the way, the nearest first: this device's network, the server, then
// each computer through the server. Opened by hand when things feel slow
// or stuck; nothing is measured in the background.

import { Fragment, useCallback, useEffect, useRef, useState } from "react";
import { ConnProbeState, useCoreHost, useSettingsView } from "../core/host";
import { renderText } from "../core/text";
import { ConnState } from "../gen/remoteagent/core/v1/home_view_pb";
import {
  ConnectionGroupStage,
  type ConnCheckSummaryView,
  type MachineListCardView,
} from "../gen/remoteagent/core/v1/settings_view_pb";
import { errorText, type Remote } from "../remote";
import { Banner, StateMark, Sheet, type Mark } from "../ui";
import { LoaderCircle } from "../icons";
import { connFooter } from "./direct-status";

type Ping = { state: "wait" } | { state: "ok"; ms: number } | { state: "fail"; error: string } | { state: "skip" };

/** A round trip this long is called slow. A computer's goes through the
 * server twice and is sealed and opened on the way. */
export const SLOW_SERVER_MS = 500;
export const SLOW_MACHINE_MS = 1500;

/** The middle one of the samples: one slow packet does not make the line slow. */
export function median(samples: number[]): number {
  const s = [...samples].sort((a, b) => a - b);
  return s[Math.floor(s.length / 2)];
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

function Result({ p, slow }: { p?: Ping; slow: number }) {
  if (!p || p.state === "wait") return <span className="mv"><LoaderCircle /><em>{l10n.t("connection.checking")}</em></span>;
  if (p.state === "skip") return <span className="mv">{l10n.t("connection.not_checked")}</span>;
  if (p.state === "fail") return <span className="mv red">{p.error}</span>;
  return (
    <span className="mv">
      <StateMark kind={p.ms >= slow ? "off" : "online"} />
      <em>{p.ms} ms{p.ms >= slow && l10n.t("connection.slow")}</em>
    </span>
  );
}

export function ConnCheckScreenView({
  cc,
  machines: machineCards,
  serverHost,
  pingServer,
  pingMachine,
  onReportProbe,
  onReconnect,
  onClose,
}: {
  cc?: ConnCheckSummaryView;
  machines: MachineListCardView[];
  serverHost: string;
  pingServer: (signal?: AbortSignal) => Promise<number>;
  pingMachine: (id: string, signal?: AbortSignal) => Promise<number>;
  onReportProbe?: (state: ConnProbeState, latencyMs?: number, lastFailure?: string) => void;
  onReconnect: () => void;
  onClose: () => void;
}) {
  const [server, setServer] = useState<Ping>();
  const [machines, setMachines] = useState(new Map<string, Ping>());
  const [busy, setBusy] = useState(false);
  const [now, setNow] = useState(Date.now);
  const down = cc?.connState !== ConnState.CONNECTED;
  const connStep = renderText(cc?.connStep);
  const connError = renderText(cc?.lastFailureText);
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
  const cardsRef = useRef(machineCards);
  cardsRef.current = machineCards;
  const list = [...machineCards].sort((a, b) => Number(b.online) - Number(a.online) || a.name.localeCompare(b.name));
  const check = useCallback(async () => {
    activeAbort.current?.abort();
    const ctrl = new AbortController();
    activeAbort.current = ctrl;
    const mine = ++run.current;
    const onlineCards = cardsRef.current.filter((m) => m.online);
    const online = onlineCards.map((m) => m.machineId);
    const directReadyById = new Map(onlineCards.map((m) => [m.machineId, m.directReady]));
    setBusy(true);
    setServer({ state: "wait" });
    setMachines(new Map(online.map((id) => [id, { state: "wait" }])));
    onReportProbe?.(ConnProbeState.CHECKING);
    let reached = true;
    try {
      const samples: number[] = [];
      for (let i = 0; i < 3; i++) samples.push(await pingServer(ctrl.signal));
      if (mine !== run.current) return;
      const ms = median(samples);
      setServer({ state: "ok", ms });
      onReportProbe?.(ms >= SLOW_SERVER_MS ? ConnProbeState.SLOW : ConnProbeState.WORKING, ms);
    } catch (e) {
      if (mine !== run.current) return;
      reached = false;
      const msg = errorText(e) || l10n.t("connection.no_response");
      setServer({ state: "fail", error: msg });
      onReportProbe?.(ConnProbeState.UNAVAILABLE, 0, msg);
      setMachines(new Map(online.map((id) => [id, { state: directReadyById.get(id) ? "wait" : "skip" }])));
    }
    await Promise.all(
      online.filter((id) => reached || directReadyById.get(id)).map(async (id) => {
        const p: Ping = await pingMachine(id, ctrl.signal).then(
          (ms) => ({ state: "ok", ms }),
          (e) => ({ state: "fail", error: errorText(e) || l10n.t("connection.no_response") }),
        );
        if (mine === run.current) setMachines((m) => new Map(m).set(id, p));
      }),
    );
    if (mine === run.current) setBusy(false);
  }, [pingServer, pingMachine, onReportProbe]);
  useEffect(() => {
    void check();
    return () => {
      run.current++;
      activeAbort.current?.abort();
      activeAbort.current = null;
    };
  }, [check]);
  return (
    <Sheet onClose={onClose} title={l10n.t("settings.server_and_connection")} action={{ label: l10n.t("common.done"), onClick: onClose }}>
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
          <Result p={server} slow={SLOW_SERVER_MS} />
        </div>
        {list.map((m) => {
          const directRow = m.online ? m.directRow : undefined;
          const status = directRow ? renderText(directRow.statusText) : "";
          const detail = directRow ? renderText(directRow.detailText) : "";
          const summary = [status, detail].filter(Boolean).join(" · ");
          return (
            <Fragment key={m.machineId}>
              <div className="mi">
                <span className="mt">
                  {m.name}
                  {m.disambiguator && <span className="mc-tag mc-tag-inline">{m.disambiguator}</span>}
                </span>
                {m.online ? <Result p={machines.get(m.machineId)} slow={SLOW_MACHINE_MS} /> : <span className="mv"><StateMark kind="off" /><em>{l10n.t("common.ios_offline")}</em></span>}
              </div>
              {directRow?.visible && summary && (
                <div className="mi conn-direct" data-machine-id={m.machineId}>
                  <span className="mt">{summary}</span>
                </div>
              )}
            </Fragment>
          );
        })}
      </div>
      <p className="gf">{connFooter(renderText(cc?.footerText), machineCards.some((x) => x.directRow?.stage === ConnectionGroupStage.UNSUPPORTED))}</p>
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
  const pingMachine = useCallback((id: string, signal?: AbortSignal) => remote.pingMachine(id, undefined, signal), [remote]);
  const onReportProbe = useCallback(
    (state: ConnProbeState, latencyMs?: number, lastFailure?: string) =>
      host.reportConnCheckStatus(state, latencyMs, lastFailure),
    [host],
  );
  const onReconnect = useCallback(() => { host.reconnectNow(); }, [host]);
  return (
    <ConnCheckScreenView
      cc={settingsVm.connCheck}
      machines={settingsVm.machines}
      serverHost={new URL(remote.account.server).host}
      pingServer={pingServer}
      pingMachine={pingMachine}
      onReportProbe={onReportProbe}
      onReconnect={onReconnect}
      onClose={onClose}
    />
  );
}
