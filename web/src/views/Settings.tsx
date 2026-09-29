import { useEffect, useState } from "react";
import type { RateLimit } from "../gen/remoteagent/v1/event_pb";
import { Agent, ApprovalMode } from "../gen/remoteagent/v1/event_pb";
import type { AgentDefaults, DaemonSettings, ListModelsResponse } from "../gen/remoteagent/v1/local_pb";
import { agentName, ago, findModel, machineAgents, machineName, modeName, projects, shortPath } from "../model";
import { pushPrefs, pushState, setPushPrefs, type PushPrefs } from "../push";
import { errorText, type Remote } from "../remote";
import { forgetAccount } from "../storage";
import { getTheme, setTheme, type Theme } from "../theme";
import { Alert, Banner, Dot, NavBar, Sheet, useRemote } from "../ui";

export function Settings({ remote, go }: { remote: Remote; go: (r: string) => void }) {
  useRemote(remote);
  const [leaving, setLeaving] = useState(false);
  const machines = [...remote.machines.values()].sort((a, b) => machineName(a).localeCompare(machineName(b)));
  const push = usePush(remote);
  return (
    <div className="body gray">
      <div className="large">
        <h3>设置</h3>
      </div>
      {push.top}
      <div className="glabel">设备</div>
      <div className="grp">
        <div className="row">
          <span className="ico" style={{ background: "#8e99a6" }}>
            ▢
          </span>
          这个浏览器<span className="v">本机</span>
        </div>
        {machines.map((m) => (
          <button key={m.id} className="row" onClick={() => go("m/" + m.id)}>
            <span className="ico" style={{ background: "#5b8def" }}>
              ⌘
            </span>
            {machineName(m)}
            <span className="v">
              <Dot tone={m.online ? "idle" : "off"} />
              {m.online ? "在线" : ago(m.lastSeenMs) || "离线"}
              {m.online && m.meta?.secondaryLimit ? ` · 已用 ${Math.round(m.meta.secondaryLimit.usedPercent)}%` : ""} ›
            </span>
          </button>
        ))}
        <button className="row add" onClick={() => go("add")}>
          ＋ 添加设备
        </button>
      </div>
      {push.group}
      <div className="glabel">外观</div>
      <div className="grp">
        <div className="row">
          <ThemePicker />
        </div>
      </div>
      <div className="glabel">账户</div>
      <div className="grp">
        <div className="row">
          服务器<span className="v">{new URL(remote.account.server).host}</span>
        </div>
        <button className="row danger" onClick={() => setLeaving(true)}>
          退出并删除本机密钥
        </button>
      </div>
      {leaving && (
        <Alert
          title="退出这个浏览器？"
          titleRed
          text="这个浏览器里的账户密钥会被删除，要重新配对才能再用。电脑上的会话不受影响。"
          ok="退出"
          danger
          onCancel={() => setLeaving(false)}
          onOk={async () => {
            await forgetAccount();
            location.hash = "";
            location.reload();
          }}
        />
      )}
    </div>
  );
}

function Switch({ on, disabled, onChange, label }: { on: boolean; disabled?: boolean; onChange: (v: boolean) => void; label: string }) {
  return <button className={"sw" + (on ? "" : " off")} disabled={disabled} role="switch" aria-checked={on} aria-label={label} onClick={() => onChange(!on)} />;
}

/** Push: the warning on top for the state it is in, and the switches. */
function usePush(remote: Remote) {
  const [prefs, setPrefs] = useState<PushPrefs>(pushPrefs);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [help, setHelp] = useState(false);
  const [last, setLast] = useState<PushPrefs>();
  const st = pushState();
  const change = async (next: PushPrefs) => {
    setBusy(true);
    setError("");
    setLast(next);
    try {
      await setPushPrefs(remote, next);
      setPrefs(next);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };
  let warn;
  if (st === "ios-browser")
    warn = (
      <div className="gbanner">
        <Banner action={help ? undefined : "说明"} onAction={() => setHelp(true)}>
          iPhone 要先「添加到主屏幕」，从主屏幕打开才能收推送
          {help && "：在 Safari 里点分享按钮，选「添加到主屏幕」，再从主屏幕上的图标打开。"}
        </Banner>
      </div>
    );
  else if (st === "unsupported") warn = <div className="gbanner"><Banner>这个浏览器不支持推送</Banner></div>;
  else if (st === "denied")
    warn = (
      <div className="gwarn">
        <span>浏览器拒绝了通知权限{help && "。在地址栏左边的网站设置里把「通知」改成允许，再回来打开开关。"}</span>
        {!help && <button onClick={() => setHelp(true)}>怎么打开</button>}
      </div>
    );
  else if (!prefs.attention && !prefs.turns)
    warn = (
      <div className="gwarn">
        <span>推送没开，审批来了不会提醒你</span>
        <button disabled={busy} onClick={() => void change({ attention: true, turns: prefs.turns })}>
          开启
        </button>
      </div>
    );
  const usable = st === "ok" || st === "default";
  const top = (
    <>
      {warn}
      {error && (
        <div className="gbanner">
          <Banner kind="err" action="重试" onAction={() => last && void change(last)}>
            订阅失败：{error}
          </Banner>
        </div>
      )}
    </>
  );
  const group = (
    <>
      <div className="glabel">通知</div>
      <div className="grp">
        <div className="row">
          等我处理时
          <Switch label="等我处理时" on={prefs.attention} disabled={busy || !usable} onChange={(v) => void change({ ...prefs, attention: v })} />
        </div>
        <div className="row">
          一轮结束时
          <Switch label="一轮结束时" on={prefs.turns} disabled={busy || !usable} onChange={(v) => void change({ ...prefs, turns: v })} />
        </div>
      </div>
    </>
  );
  return { top, group };
}

const timeouts = [60, 300, 600, 1800, 3000];

function limitRow(key: string, l?: RateLimit) {
  if (!l) return null;
  const reset = l.resetsAt ? new Date(Number(l.resetsAt) * 1000) : undefined;
  const days = ["周日", "周一", "周二", "周三", "周四", "周五", "周六"];
  const mins = Number(l.windowMinutes);
  const label = mins >= 7 * 24 * 60 ? "本周" : mins >= 60 ? `${Math.round(mins / 60)} 小时` : `${mins} 分钟`;
  const hm = reset ? `${reset.getHours()}:${String(reset.getMinutes()).padStart(2, "0")}` : "";
  const sameDay = reset && reset.toDateString() === new Date().toDateString();
  return (
    <div className="row col" key={key}>
      <div className="line">
        <span>{label}</span>
        <span className="v">
          已用 {Math.round(l.usedPercent)}%{reset && ` · ${sameDay ? "" : days[reset.getDay()] + " "}${hm} 重置`}
        </span>
      </div>
      <div className="bar">
        <i style={{ width: `${Math.min(100, l.usedPercent)}%` }} />
      </div>
    </div>
  );
}

type Field = { agent: Agent; what: "model" | "effort" | "mode" } | { what: "timeout" };

function defaultsFor(st: DaemonSettings, agent: Agent): { model: string; effort: string; mode: ApprovalMode } {
  if (agent === Agent.CLAUDE) return { model: st.claude?.model ?? "", effort: st.claude?.effort ?? "", mode: st.claude?.mode ?? ApprovalMode.UNSPECIFIED };
  return { model: st.defaultModel, effort: st.defaultEffort, mode: st.defaultMode };
}

function withDefaults(st: DaemonSettings, agent: Agent, patch: Partial<Omit<AgentDefaults, "$typeName">>): DaemonSettings {
  if (agent === Agent.CLAUDE) {
    const cur = defaultsFor(st, agent);
    return { ...st, claude: { $typeName: "remoteagent.v1.AgentDefaults", ...cur, ...patch } };
  }
  return {
    ...st,
    ...(patch.model !== undefined && { defaultModel: patch.model }),
    ...(patch.effort !== undefined && { defaultEffort: patch.effort }),
    ...(patch.mode !== undefined && { defaultMode: patch.mode }),
  };
}

/** One machine: usage, CLI versions, new-session defaults per CLI, its
 * projects, and removing it. */
export function MachinePage({ remote, id, go, onBack }: { remote: Remote; id: string; go: (r: string) => void; onBack: () => void }) {
  useRemote(remote);
  const m = remote.machines.get(id);
  const [st, setSt] = useState<DaemonSettings>();
  const [models, setModels] = useState<Map<Agent, ListModelsResponse>>(new Map());
  const [pick, setPick] = useState<Field>();
  const [error, setError] = useState("");
  const [saveError, setSaveError] = useState<{ text: string; next: DaemonSettings }>();
  const [removing, setRemoving] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const agents = machineAgents(m);
  useEffect(() => {
    if (!m?.online) return;
    setError("");
    remote.settings(id, true).then(setSt, (e) => setError(errorText(e)));
    for (const a of agents)
      remote.listModels(id, a).then(
        (r) => setModels((x) => new Map(x).set(a, r)),
        () => undefined,
      );
  }, [remote, id, m?.online, attempt, agents.join()]); // eslint-disable-line react-hooks/exhaustive-deps
  if (!m)
    return (
      <div className="page">
        <NavBar onBack={onBack} />
        <div className="center muted">没有这台设备</div>
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
  const projs = projects(remote.sessions.values()).filter((p) => p.sessions.some((s) => s.machineId === id));
  const name = machineName(m);
  const meta = m.meta;
  const codex = meta?.agents.find((a) => a.agent === Agent.CODEX) ?? (meta?.codexVersion ? { agent: Agent.CODEX, version: meta.codexVersion, verified: meta.codexVerified } : undefined);
  const versions = [codex, ...(meta?.agents.filter((a) => a.agent !== Agent.CODEX) ?? [])].filter((v): v is NonNullable<typeof v> => !!v?.version);
  const modelName = (a: Agent, slug: string) => findModel(models.get(a)?.models, slug)?.displayName ?? slug;

  let sheet;
  if (pick && st) {
    if (pick.what === "timeout")
      sheet = (
        <Sheet onClose={() => setPick(undefined)}>
          <h4>审批超时</h4>
          <div className="list">
            {timeouts.map((t) => (
              <button key={t} className="mi" onClick={() => void save({ ...st, approvalTimeoutSeconds: t })}>
                <div className="mt">{t / 60} 分钟</div>
                {t === st.approvalTimeoutSeconds && <span className="ck">✓</span>}
              </button>
            ))}
          </div>
          <div className="hint">没人回复的审批过了这个时间就拒绝</div>
        </Sheet>
      );
    else {
      const a = pick.agent;
      const def = defaultsFor(st, a);
      const list = models.get(a);
      const cur = findModel(list?.models, def.model || list?.defaultModel || "");
      const follow = `跟随 ${agentName(a)} 配置`;
      const opts: { key: string; label: string; on: boolean; next: DaemonSettings }[] =
        pick.what === "model"
          ? [
              { key: "", label: follow, on: !def.model, next: withDefaults(st, a, { model: "", effort: "" }) },
              ...(list?.models ?? []).map((x) => ({ key: x.slug, label: x.displayName || x.slug, on: x.slug === def.model, next: withDefaults(st, a, { model: x.slug, effort: "" }) })),
            ]
          : pick.what === "effort"
            ? [
                { key: "", label: follow, on: !def.effort, next: withDefaults(st, a, { effort: "" }) },
                ...(cur ? cur.efforts : ["low", "medium", "high", "xhigh"]).map((e) => ({ key: e, label: e, on: e === def.effort, next: withDefaults(st, a, { effort: e }) })),
              ]
            : [ApprovalMode.UNSPECIFIED, ApprovalMode.ASK, ApprovalMode.AUTO, ApprovalMode.FULL_ACCESS].map((x) => ({
                key: String(x),
                label: modeName(x) || follow,
                on: x === def.mode,
                next: withDefaults(st, a, { mode: x }),
              }));
      sheet = (
        <Sheet onClose={() => setPick(undefined)}>
          <h4>
            {agents.length > 1 ? agentName(a) + " · " : ""}
            {{ model: "默认模型", effort: "默认推理强度", mode: "默认审批模式" }[pick.what]}
          </h4>
          {pick.what === "model" && !list && <div className="hint">读取模型列表…</div>}
          <div className="list">
            {opts.map((o) => (
              <button key={o.key} className="mi" onClick={() => void save(o.next)}>
                <div className="mt">{o.label}</div>
                {o.on && <span className="ck">✓</span>}
              </button>
            ))}
          </div>
        </Sheet>
      );
    }
  }

  return (
    <div className="page gray">
      <NavBar onBack={onBack} title={name} />
      <div className="body">
        {(meta?.primaryLimit || meta?.secondaryLimit) && (
          <>
            <div className="glabel">Codex 额度{meta?.planType ? ` · ${meta.planType}` : ""}</div>
            <div className="grp">
              {limitRow("p", meta?.primaryLimit)}
              {limitRow("s", meta?.secondaryLimit)}
            </div>
          </>
        )}
        {versions.length > 0 && (
          <div className="grp">
            {versions.map((v) => (
              <div className="row" key={v.agent}>
                {agentName(v.agent)} 版本
                <span className="v">
                  {!v.verified && <span className="warn-text">未验证 ·</span>} {v.version}
                </span>
              </div>
            ))}
            {versions.some((v) => !v.verified) && (
              <div className="row help">版本未验证：在这台电脑上运行 remote-agent doctor，它会用一个临时会话检查各项功能。</div>
            )}
          </div>
        )}
        {!m.online && <div className="glabel">{name} 不在线，暂时看不到它的设置。</div>}
        {error && (
          <div className="gbanner">
            <Banner kind="err" action="重试" onAction={() => setAttempt((a) => a + 1)}>
              没能读取设置：{error}
            </Banner>
          </div>
        )}
        {saveError && (
          <div className="gbanner">
            <Banner kind="err" action="重试" onAction={() => void save(saveError.next)}>
              没能保存：{saveError.text}
            </Banner>
          </div>
        )}
        {st &&
          agents.map((a) => {
            const def = defaultsFor(st, a);
            const follow = "跟随配置";
            return (
              <div key={a}>
                <div className="glabel">新会话默认设置{agents.length > 1 ? ` · ${agentName(a)}` : ""}</div>
                <div className="grp">
                  <button className="row" onClick={() => setPick({ agent: a, what: "model" })}>
                    模型<span className="v">{def.model ? modelName(a, def.model) : follow} ›</span>
                  </button>
                  <button className="row" onClick={() => setPick({ agent: a, what: "effort" })}>
                    推理强度<span className="v">{def.effort || follow} ›</span>
                  </button>
                  <button className="row" onClick={() => setPick({ agent: a, what: "mode" })}>
                    审批模式<span className="v">{modeName(def.mode) || follow} ›</span>
                  </button>
                </div>
              </div>
            );
          })}
        {st && (
          <div className="grp">
            <button className="row" onClick={() => setPick({ what: "timeout" })}>
              审批超时<span className="v">{Math.round(st.approvalTimeoutSeconds / 60)} 分钟 ›</span>
            </button>
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
          <button className="row danger" onClick={() => setRemoving(true)}>
            移除这台设备
          </button>
        </div>
      </div>
      {sheet}
      {removing && (
        <Alert
          title={`移除 ${name}？`}
          titleRed
          text="它的会话记录会从服务器上删除，它要重新加入账户才能再连上。"
          ok="移除"
          danger
          onCancel={() => setRemoving(false)}
          onOk={async () => {
            setRemoving(false);
            try {
              await remote.removeDevice(id);
              go("settings");
            } catch (e) {
              setError(errorText(e));
            }
          }}
        />
      )}
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
