import { useEffect, useState } from "react";
import type { RateLimit } from "../gen/remoteagent/v1/event_pb";
import { ApprovalMode } from "../gen/remoteagent/v1/event_pb";
import type { DaemonSettings, ListModelsResponse } from "../gen/remoteagent/v1/local_pb";
import { machineName, projects, shortPath, when } from "../model";
import { pushPrefs, pushSupported, setPushPrefs, type PushPrefs } from "../push";
import { errorText, join, type Remote } from "../remote";
import { forgetAccount } from "../storage";
import { getTheme, setTheme, type Theme } from "../theme";
import { Dot, useRemote } from "../ui";

export function Settings({ remote, go }: { remote: Remote; go: (r: string) => void }) {
  useRemote(remote);
  const machines = [...remote.machines.values()].sort((a, b) => machineName(a).localeCompare(machineName(b)));
  return (
    <div className="body settings">
      <div className="large">
        <h3>设置</h3>
      </div>
      <div className="glabel">设备</div>
      <div className="grp">
        <div className="row">
          🌐 这个浏览器<span className="v">本机</span>
        </div>
        {machines.map((m) => (
          <button key={m.id} className="row" onClick={() => go("m/" + m.id)}>
            💻 {machineName(m)}
            <span className="v">
              {quota(m.meta?.primaryLimit) && <>额度 {quota(m.meta?.primaryLimit)} · </>}
              <Dot tone={m.online ? "idle" : "off"} />
              {m.online ? "在线" : when(m.lastSeenMs) || "离线"} ›
            </span>
          </button>
        ))}
      </div>
      <div className="glabel">通知</div>
      <Notifications remote={remote} />
      <div className="glabel">外观</div>
      <div className="grp">
        <div className="row">
          <ThemePicker />
        </div>
      </div>
      <div className="glabel">添加设备</div>
      <div className="grp">
        <div className="row help">
          在已加入的电脑上运行 <code>remote-agent account secret</code>，把输出粘贴到新设备的网页，或在新电脑上运行 <code>remote-agent account join</code>。
        </div>
      </div>
      <div className="glabel">账户</div>
      <div className="grp">
        <div className="row">
          服务器<span className="v">{new URL(remote.account.server).host}</span>
        </div>
        <button
          className="row danger"
          onClick={async () => {
            if (confirm("在这个浏览器里删除账户密钥？之后需要重新粘贴。")) {
              await forgetAccount();
              location.hash = "";
              location.reload();
            }
          }}
        >
          退出并删除本机密钥
        </button>
      </div>
      {remote.error && <p className="empty">最近的错误：{remote.error}</p>}
    </div>
  );
}

function quota(l?: RateLimit): string {
  return l ? `${Math.round(l.usedPercent)}%` : "";
}

function Switch({ on, disabled, onChange }: { on: boolean; disabled?: boolean; onChange: (v: boolean) => void }) {
  return <button className={"sw" + (on ? " on" : "")} disabled={disabled} role="switch" aria-checked={on} onClick={() => onChange(!on)} />;
}

function Notifications({ remote }: { remote: Remote }) {
  const [prefs, setPrefs] = useState<PushPrefs>(pushPrefs);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  if (!pushSupported())
    return (
      <div className="grp">
        <div className="row help">这个浏览器不支持推送。iPhone 上要先把网页「添加到主屏幕」，再从主屏幕打开。</div>
      </div>
    );
  const change = async (next: PushPrefs) => {
    setBusy(true);
    setError("");
    try {
      await setPushPrefs(remote, next);
      setPrefs(next);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="grp">
      <div className="row">
        等我处理时（审批、提问、弹窗）
        <Switch on={prefs.attention} disabled={busy} onChange={(v) => void change({ ...prefs, attention: v })} />
      </div>
      <div className="row">
        一轮结束时
        <Switch on={prefs.turns} disabled={busy} onChange={(v) => void change({ ...prefs, turns: v })} />
      </div>
      {error && <div className="row help error">{error}</div>}
    </div>
  );
}

const modeNames: [ApprovalMode, string][] = [
  [ApprovalMode.UNSPECIFIED, "跟随 Codex 配置"],
  [ApprovalMode.ASK, "询问"],
  [ApprovalMode.AUTO, "自动"],
  [ApprovalMode.FULL_ACCESS, "完全放行"],
];
const timeouts = [60, 300, 600, 1800, 3000];

function limitRow(name: string, l?: RateLimit) {
  if (!l) return null;
  const reset = l.resetsAt ? new Date(Number(l.resetsAt) * 1000) : undefined;
  const days = ["周日", "周一", "周二", "周三", "周四", "周五", "周六"];
  const label = l.windowMinutes >= 7 * 24 * 60 ? "本周" : l.windowMinutes >= 60 ? `${Math.round(Number(l.windowMinutes) / 60)} 小时` : name;
  return (
    <div className="row block" key={name}>
      <div className="line">
        <span>{label}</span>
        <span className="v">
          {Math.round(l.usedPercent)}%{reset && ` · ${days[reset.getDay()]} ${reset.getHours()}:${String(reset.getMinutes()).padStart(2, "0")} 重置`}
        </span>
      </div>
      <div className="bar">
        <i style={{ width: `${Math.min(100, l.usedPercent)}%` }} />
      </div>
    </div>
  );
}

/** One machine: Codex usage, defaults for new sessions, its projects. */
export function MachinePage({ remote, id, go, onBack }: { remote: Remote; id: string; go: (r: string) => void; onBack: () => void }) {
  useRemote(remote);
  const m = remote.machines.get(id);
  const [st, setSt] = useState<DaemonSettings>();
  const [models, setModels] = useState<ListModelsResponse>();
  const [pick, setPick] = useState("");
  const [error, setError] = useState("");
  useEffect(() => {
    if (!m?.online) return;
    remote.machineCommand(id, { case: "getSettings", value: {} }).then(
      (r) => r.kind.case === "getSettings" && setSt(r.kind.value.settings),
      (e) => setError(errorText(e)),
    );
    remote.listModels(id).then(setModels, () => undefined);
  }, [remote, id, m?.online]);
  if (!m) return <div className="center muted">没有这台设备</div>;
  const save = async (next: DaemonSettings) => {
    setError("");
    try {
      const r = await remote.machineCommand(id, { case: "updateSettings", value: { settings: next } });
      if (r.kind.case === "updateSettings") setSt(r.kind.value.settings);
      setPick("");
    } catch (e) {
      setError(errorText(e));
    }
  };
  const projs = projects(remote).filter((p) => p.sessions.some((s) => s.machineId === id));
  const model = models?.models.find((x) => x.slug === (st?.defaultModel || models.defaultModel));
  return (
    <>
      <header className="navbar">
        <button className="back" onClick={onBack} aria-label="返回">
          ‹
        </button>
        <span className="title">
          <b>{machineName(m)}</b>
        </span>
        <span className="more" />
      </header>
      <div className="body settings">
        {(m.meta?.primaryLimit || m.meta?.secondaryLimit) && (
          <>
            <div className="glabel">Codex 额度{m.meta?.planType ? ` · ${m.meta.planType}` : ""}</div>
            <div className="grp">
              {limitRow("主额度", m.meta?.primaryLimit)}
              {limitRow("次额度", m.meta?.secondaryLimit)}
            </div>
          </>
        )}
        <div className="glabel">默认设置（新会话）</div>
        {!m.online && <p className="empty">电脑不在线，暂时看不到它的设置。</p>}
        {error && <p className="empty error">{error}</p>}
        {st && (
          <div className="grp">
            <button className="row" onClick={() => setPick(pick === "model" ? "" : "model")}>
              模型<span className="v">{st.defaultModel ? model?.displayName ?? st.defaultModel : "跟随 Codex 配置"} ›</span>
            </button>
            {pick === "model" && models && (
              <div className="row block">
                <div className="opts">
                  <button className={"opt" + (!st.defaultModel ? " on" : "")} onClick={() => void save({ ...st, defaultModel: "", defaultEffort: "" })}>
                    跟随 Codex 配置
                  </button>
                  {models.models.map((x) => (
                    <button key={x.slug} className={"opt" + (x.slug === st.defaultModel ? " on" : "")} onClick={() => void save({ ...st, defaultModel: x.slug, defaultEffort: "" })}>
                      {x.displayName}
                    </button>
                  ))}
                </div>
              </div>
            )}
            <button className="row" onClick={() => setPick(pick === "effort" ? "" : "effort")}>
              Reasoning effort<span className="v">{st.defaultEffort || "跟随 Codex 配置"} ›</span>
            </button>
            {pick === "effort" && (
              <div className="row block">
                <div className="opts">
                  {(model?.efforts ?? ["low", "medium", "high", "xhigh"]).map((e) => (
                    <button key={e} className={"opt" + (e === st.defaultEffort ? " on" : "")} onClick={() => void save({ ...st, defaultEffort: e })}>
                      {e}
                    </button>
                  ))}
                </div>
              </div>
            )}
            <button className="row" onClick={() => setPick(pick === "mode" ? "" : "mode")}>
              审批模式<span className="v">{modeNames.find(([k]) => k === st.defaultMode)?.[1]} ›</span>
            </button>
            {pick === "mode" && (
              <div className="row block">
                <div className="opts">
                  {modeNames.map(([k, name]) => (
                    <button key={k} className={"opt" + (k === st.defaultMode ? " on" : "")} onClick={() => void save({ ...st, defaultMode: k })}>
                      {name}
                    </button>
                  ))}
                </div>
              </div>
            )}
            <button className="row" onClick={() => setPick(pick === "timeout" ? "" : "timeout")}>
              审批超时<span className="v">{Math.round(st.approvalTimeoutSeconds / 60)} 分钟 ›</span>
            </button>
            {pick === "timeout" && (
              <div className="row block">
                <div className="opts">
                  {timeouts.map((t) => (
                    <button key={t} className={"opt" + (t === st.approvalTimeoutSeconds ? " on" : "")} onClick={() => void save({ ...st, approvalTimeoutSeconds: t })}>
                      {t / 60} 分钟
                    </button>
                  ))}
                </div>
              </div>
            )}
          </div>
        )}
        {projs.length > 0 && (
          <>
            <div className="glabel">项目</div>
            <div className="grp">
              {projs.map((p) => (
                <button key={p.key} className="row" onClick={() => go("p/" + encodeURIComponent(p.key))}>
                  {p.name}
                  <span className="v">{shortPath(p.key)}</span>
                </button>
              ))}
            </div>
          </>
        )}
        <div className="grp">
          <button
            className="row danger"
            onClick={async () => {
              if (!confirm(`移除 ${machineName(m)}？它的会话记录会从服务器上删除，它需要重新加入账户才能再连上。`)) return;
              try {
                await remote.removeDevice(id);
                go("settings");
              } catch (e) {
                setError(errorText(e));
              }
            }}
          >
            移除这台设备
          </button>
        </div>
      </div>
    </>
  );
}

export function Setup({ onDone }: { onDone: () => void }) {
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submit = async () => {
    setBusy(true);
    setError("");
    try {
      await join(text);
      setText("");
      onDone();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="setup">
      <div className="large">
        <h3>添加设备</h3>
      </div>
      <p className="muted">
        在已加入的电脑上运行 <code>remote-agent account secret</code>，把输出的账户密钥粘贴到这里。密钥只保存在这个浏览器里，不会发给服务器。
      </p>
      <textarea className="ta" value={text} onChange={(e) => setText(e.target.value)} placeholder="ra1.…" rows={4} autoComplete="off" spellCheck={false} aria-label="账户密钥" />
      {error && <p className="error">{error}</p>}
      <button className="big" disabled={busy || !text.trim()} onClick={submit}>
        {busy ? "连接中…" : "连接"}
      </button>
    </div>
  );
}

function ThemePicker() {
  const [theme, set] = useState<Theme>(getTheme);
  const opts: [Theme, string][] = [
    ["auto", "自动"],
    ["light", "亮色"],
    ["dark", "暗色"],
  ];
  return (
    <div className="seg full">
      {opts.map(([t, name]) => (
        <button
          key={t}
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
