import * as l10n from "../i18n";
// 连接检测 (client-v34): how long a round trip takes now on each stretch of
// the way, the nearest first: this device's network, the server, then
// each computer through the server. Opened by hand when things feel slow
// or stuck; nothing is measured in the background.

import { Fragment, useCallback, useEffect, useRef, useState } from "react";
import { ConnProbeState, useCoreHost, useSettingsView } from "../core/host";
import { renderText } from "../core/text";
import { ConnectionGroupStage } from "../gen/remoteagent/core/v1/settings_view_pb";
import { machineName } from "../model";
import { errorText, type Remote } from "../remote";
import { Banner, StateMark, Sheet, useRemote, type Mark } from "../ui";
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
export function connWord(remote: Remote, showWaiting = remote.showWaiting): { tone: Mark; text: string } {
  if (remote.conn === "online") return { tone: "online", text: l10n.t("connection.connected") };
  if (remote.link === "waiting" && !showWaiting) return { tone: "off", text: "" };
  if (remote.everOnline) return { tone: "run", text: l10n.t("connection.reconnecting_conn_check") };
  return { tone: "off", text: remote.failures >= 2 ? l10n.t("connection.unreachable") : l10n.t("connection.connecting_conn_check") };
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

export function ConnCheck({ remote, onClose }: { remote: Remote; onClose: () => void }) {
  useRemote(remote);
  const host = useCoreHost(remote);
  const settingsVm = useSettingsView(host);
  const [server, setServer] = useState<Ping>();
  const [machines, setMachines] = useState(new Map<string, Ping>());
  const [busy, setBusy] = useState(false);
  const [now, setNow] = useState(Date.now);
  const startedAt = remote.connStartedAt;
  const connecting = remote.conn !== "online" && !!remote.connStep && startedAt !== undefined;
  useEffect(() => {
    remote.refreshDirectStatuses();
  }, [remote]);
  useEffect(() => {
    if (!connecting) return;
    setNow(Date.now());
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [connecting, startedAt]);
  const stream = connWord(remote, true);
  const run = useRef(0);
  const list = [...remote.machines.values()].sort((a, b) => Number(remote.peer(b.id).reachable) - Number(remote.peer(a.id).reachable) || machineName(a).localeCompare(machineName(b)));
  const check = useCallback(async () => {
    const mine = ++run.current;
    const online = [...remote.machines.values()].filter((m) => remote.peer(m.id).reachable).map((m) => m.id);
    setBusy(true);
    setServer({ state: "wait" });
    setMachines(new Map(online.map((id) => [id, { state: "wait" }])));
    host.reportConnCheckStatus(ConnProbeState.CHECKING);
    let reached = true;
    try {
      const samples: number[] = [];
      for (let i = 0; i < 3; i++) samples.push(await remote.pingServer());
      if (mine !== run.current) return;
      const ms = median(samples);
      setServer({ state: "ok", ms });
      host.reportConnCheckStatus(ms >= SLOW_SERVER_MS ? ConnProbeState.SLOW : ConnProbeState.WORKING, ms);
    } catch (e) {
      if (mine !== run.current) return;
      reached = false;
      const msg = errorText(e) || l10n.t("connection.no_response");
      setServer({ state: "fail", error: msg });
      host.reportConnCheckStatus(ConnProbeState.UNAVAILABLE, 0, msg);
      setMachines(new Map(online.map((id) => [id, { state: remote.peer(id).canProbe(false) ? "wait" : "skip" }])));
    }
    await Promise.all(
        online.filter(id => remote.peer(id).canProbe(reached)).map(async (id) => {
          const p: Ping = await remote.pingMachine(id).then(
            (ms) => ({ state: "ok", ms }),
            (e) => ({ state: "fail", error: errorText(e) || l10n.t("connection.no_response") }),
          );
          if (mine === run.current) setMachines((m) => new Map(m).set(id, p));
        }),
      );
    if (mine === run.current) setBusy(false);
  }, [remote, host]);
  useEffect(() => {
    void check();
    return () => void run.current++;
  }, [check]);
  const down = remote.conn !== "online";
  return (
    <Sheet onClose={onClose} title={l10n.t("settings.server_and_connection")} action={{ label: l10n.t("common.done"), onClick: onClose }}>
      {down && (
        <div className="set-banners">
          <Banner>
            {connecting && <LoaderCircle />}
            {connecting
              ? l10n.t("connection.sec_elapsed", { value: String(remote.everOnline ? l10n.t("connection.reconnecting_conn_check_label") : l10n.t("connection.connecting_conn_check_label")), detail: String(remote.connStep), count: Math.max(0, Math.floor((now - startedAt!) / 1000)), previousFailure: String(remote.connError && l10n.t("connection.last_failure", { value: String(remote.connError) })) })
              : remote.connError
                ? l10n.t("connection.live_push_is_not_connected_session_changes_may_be", { value: String(remote.connError) })
                : remote.network ? l10n.t("connection.live_push_is_not_connected") : l10n.t("connection.this_device_has_no_network_it_will_reconnect_when")}
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
            <StateMark kind={remote.network ? "online" : "off"} />
            <em>{remote.network ? l10n.t("connection.working") : l10n.t("connection.unavailable")}</em>
          </span>
        </div>
        <div className="mi">
          <span className="mt">
            {l10n.t("security.server")}<small>{new URL(remote.account.server).host}</small>
          </span>
          <Result p={server} slow={SLOW_SERVER_MS} />
        </div>
        {list.map((m) => {
          const reachable = remote.peer(m.id).reachable;
          const directRow = reachable ? settingsVm.machines.find((x) => { return x.machineId === m.id; })?.directRow : undefined;
          const status = directRow ? renderText(directRow.statusText) : "";
          const detail = directRow ? renderText(directRow.detailText) : "";
          const summary = [status, detail].filter(Boolean).join(" · ");
          return (
            <Fragment key={m.id}>
              <div className="mi">
                <span className="mt">{machineName(m)}</span>
                {reachable ? <Result p={machines.get(m.id)} slow={SLOW_MACHINE_MS} /> : <span className="mv"><StateMark kind="off" /><em>{l10n.t("common.ios_offline")}</em></span>}
              </div>
              {directRow?.visible && summary && (
                <div className="mi conn-direct" data-machine-id={m.id}>
                  <span className="mt">{summary}</span>
                </div>
              )}
            </Fragment>
          );
        })}
      </div>
      <p className="gf">{connFooter(settingsVm.machines.some((x) => x.directRow?.stage === ConnectionGroupStage.UNSUPPORTED))}</p>
      <div className="btns conn-again">
        {down && <button className="btn pri" onClick={() => remote.reconnectNow(true)}>{l10n.t("connection.reconnect_now")}</button>}
        <button className={down ? "btn" : "btn pri"} disabled={busy} onClick={() => void check()}>
          {l10n.t("connection.test_again")}</button>
      </div>
    </Sheet>
  );
}
