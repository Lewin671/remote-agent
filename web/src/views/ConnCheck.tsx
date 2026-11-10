// 连接检测 (client-v34): how long a round trip takes now on each stretch of
// the way, the nearest first: this device's network, the server, then
// each computer through the server. Opened by hand when things feel slow
// or stuck; nothing is measured in the background.

import { useCallback, useEffect, useRef, useState } from "react";
import { machineName } from "../model";
import { errorText, type Remote } from "../remote";
import { StateMark, Sheet, useRemote, type Mark } from "../ui";
import { LoaderCircle } from "../icons";

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
  if (remote.conn === "online") return { tone: "online", text: "已连接" };
  if (remote.link === "waiting" && !showWaiting) return { tone: "off", text: "" };
  if (remote.everOnline) return { tone: "run", text: "重连中" };
  return { tone: "off", text: remote.failures >= 2 ? "连不上" : "连接中" };
}

function Result({ p, slow }: { p?: Ping; slow: number }) {
  if (!p || p.state === "wait") return <span className="v muted"><LoaderCircle /> 检测中…</span>;
  if (p.state === "skip") return <span className="v muted">未检测</span>;
  if (p.state === "fail") return <span className="v red">{p.error}</span>;
  return (
    <span className="v">
      <StateMark kind={p.ms >= slow ? "off" : "online"} />
      <em>{p.ms} ms{p.ms >= slow && " · 较慢"}</em>
    </span>
  );
}

export function ConnCheck({ remote, onClose }: { remote: Remote; onClose: () => void }) {
  useRemote(remote);
  const [server, setServer] = useState<Ping>();
  const [machines, setMachines] = useState(new Map<string, Ping>());
  const [busy, setBusy] = useState(false);
  const [now, setNow] = useState(Date.now);
  const startedAt = remote.connStartedAt;
  const connecting = remote.conn !== "online" && !!remote.connStep && startedAt !== undefined;
  useEffect(() => {
    if (!connecting) return;
    setNow(Date.now());
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [connecting, startedAt]);
  const stream = connWord(remote, true);
  const run = useRef(0);
  const list = [...remote.machines.values()].sort((a, b) => Number(b.online) - Number(a.online) || machineName(a).localeCompare(machineName(b)));
  const check = useCallback(async () => {
    const mine = ++run.current;
    const online = [...remote.machines.values()].filter((m) => m.online).map((m) => m.id);
    setBusy(true);
    setServer({ state: "wait" });
    setMachines(new Map(online.map((id) => [id, { state: "wait" }])));
    let reached = true;
    try {
      const samples: number[] = [];
      for (let i = 0; i < 3; i++) samples.push(await remote.pingServer());
      if (mine !== run.current) return;
      setServer({ state: "ok", ms: median(samples) });
    } catch (e) {
      if (mine !== run.current) return;
      reached = false;
      setServer({ state: "fail", error: errorText(e) || "没有回应" });
      setMachines(new Map(online.map((id) => [id, { state: "skip" }])));
    }
    if (reached)
      await Promise.all(
        online.map(async (id) => {
          const p: Ping = await remote.pingMachine(id).then(
            (ms) => ({ state: "ok", ms }),
            (e) => ({ state: "fail", error: errorText(e) || "没有回应" }),
          );
          if (mine === run.current) setMachines((m) => new Map(m).set(id, p));
        }),
      );
    if (mine === run.current) setBusy(false);
  }, [remote]);
  useEffect(() => {
    void check();
    return () => void run.current++;
  }, [check]);
  return (
    <Sheet onClose={onClose}>
      <div className="ns-head">
        <b>连接检测</b>
        <button className="link" onClick={onClose}>
          完成
        </button>
      </div>
      <div className="setrows conn-check">
        <div className="sr">
          <span>实时推送</span>
          <span className="v"><StateMark kind={stream.tone} /><em>{stream.text}</em></span>
        </div>
        <div className="sr">
          <span>本机网络</span>
          <span className="v">
            <StateMark kind={remote.network ? "online" : "off"} />
            <em>{remote.network ? "正常" : "不通"}</em>
          </span>
        </div>
        <div className="sr">
          <span className="who">
            服务器<small>{new URL(remote.account.server).host}</small>
          </span>
          <Result p={server} slow={SLOW_SERVER_MS} />
        </div>
        {list.map((m) => (
          <div key={m.id} className="sr">
            <span className="who">{machineName(m)}</span>
            {m.online ? <Result p={machines.get(m.id)} slow={SLOW_MACHINE_MS} /> : <span className="v muted"><StateMark kind="off" /><em>不在线</em></span>}
          </div>
        ))}
      </div>
      {remote.conn !== "online" && (
        <div className="hint">{connecting
          ? `正在${remote.everOnline ? "重连" : "连接"}：${remote.connStep}，已 ${Math.max(0, Math.floor((now - startedAt!) / 1000))} 秒。${remote.connError && `上一次失败：${remote.connError}`}`
          : remote.connError
            ? `实时推送未连接，会话变化可能延迟。最近一次失败：${remote.connError}`
            : remote.network ? "实时推送未连接。" : "本机网络不通，联网后会重新连接。"}</div>
      )}
      <div className="hint">延时是一次往返的时间。服务器一行是这台设备到服务器；电脑一行是经服务器到电脑再回来，所以总比服务器慢。</div>
      <div className="btns conn-again">
        {remote.conn !== "online" && <button className="btn" onClick={() => remote.reconnectNow(true)}>立即重连</button>}
        <button className="btn" disabled={busy} onClick={() => void check()}>
          再测一次
        </button>
      </div>
    </Sheet>
  );
}
