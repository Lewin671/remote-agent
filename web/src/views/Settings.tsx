import { useEffect, useState } from "react";
import { Agent, ApprovalMode } from "../gen/remoteagent/v1/event_pb";
import type { AgentDefaults, DaemonSettings, ListModelsResponse } from "../gen/remoteagent/v1/local_pb";
import { agentName, agentOf, ago, exited, findModel, machineAgents, modelLabel, machineName, modeName, projects, shortPath } from "../model";
import { quotaRow } from "../quota";
import { pushPrefs, pushState, setPushPrefs, type PushPrefs } from "../push";
import { errorText, type MachineView, type Remote } from "../remote";
import { forgetAccount } from "../storage";
import { getTheme, setTheme, type Theme } from "../theme";
import { Alert, Banner, Dot, NavBar, Sheet, useRemote } from "../ui";
import { clientName, shortId } from "../devices";
import { DeviceKind } from "../gen/remoteagent/v1/relay_pb";

export function Settings({ remote, go }: { remote: Remote; go: (r: string) => void }) {
  useRemote(remote);
  const [leaving, setLeaving] = useState(false);
  const machines = [...remote.machines.values()].sort((a, b) => machineName(a).localeCompare(machineName(b)));
  const push = usePush(remote);
  const me = remote.account.deviceId;
  const clients = [...remote.devices.values()].filter((d) => d.kind === DeviceKind.CLIENT);
  const others = clients.filter((d) => d.id !== me);
  return (
    <div className="body gray">
      <div className="large">
        <h3>设置</h3>
      </div>
      {push.top}
      <div className="glabel with-link">
        电脑
        <button className="link" onClick={() => go("devices")}>
          全部设备 ›
        </button>
      </div>
      <div className="grp">
        {machines.map((m) => (
          <button key={m.id} className="row machine-summary" onClick={() => go("m/" + m.id)}>
            <span className="ico" style={{ background: "#5b8def" }}>
              ⌘
            </span>
            <span className="machine-info">
              <span className="machine-name">{machineName(m)}</span>
              <span className="machine-status">
                <Dot tone={m.online ? "idle" : "off"} />
                {m.online ? "在线" : ago(m.lastSeenMs) || "离线"}
                {quotaLine(m)}
              </span>
            </span>
            <span className="machine-chevron">›</span>
          </button>
        ))}
        <button className="row add" onClick={() => go("add")}>
          ＋ 添加设备
        </button>
      </div>
      <div className="glabel">客户端</div>
      <div className="grp">
        <button className="row" onClick={() => go("c/" + me)}>
          {clientName(remote.devices.get(me), me)}
          <span className="v">当前 ›</span>
        </button>
        {others.slice(0, 3).map((d) => (
          <button key={d.id} className="row" onClick={() => go("c/" + d.id)}>
            {clientName(d)}
            <span className="v">已配对 ›</span>
          </button>
        ))}
        {remote.devicesStatus === "unsupported" ? (
          <div className="row help">服务器版本较旧，看不到其他客户端。</div>
        ) : (
          others.length > 3 && (
            <button className="row add" onClick={() => go("devices")}>
              全部客户端 ›
            </button>
          )
        )}
      </div>
      {push.group}
      <div className="glabel">外观</div>
      <div className="grp">
        <div className="row">
          <ThemePicker />
        </div>
      </div>
      <div className="glabel">版本</div>
      <div className="grp">
        <div className="row">remote-agent<span className="v">{import.meta.env.VITE_PRODUCT_VERSION} · {import.meta.env.VITE_BUILD_COMMIT?.slice(0, 7)}</span></div>
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
      {leaving && <LeaveAlert onCancel={() => setLeaving(false)} />}
    </div>
  );
}

/** The tightest Codex window of a machine, for its row: "Codex · 7 天已用 34%". */
function quotaLine(m: MachineView): string {
  const l = m.meta?.secondaryLimit ?? m.meta?.primaryLimit;
  if (!l) return "";
  const r = quotaRow(l, "", Date.now(), m.online);
  return r.tone === "stale" ? " · Codex 额度（上次记录）" : ` · Codex · ${r.label}${r.used}`;
}

/** On a machine's page: each CLI's quota in a line, and the way to all. */
function QuotaSummary({ m, go }: { m: MachineView; go: (r: string) => void }) {
  const agents = machineAgents(m);
  const now = Date.now();
  const codex = m.meta?.primaryLimit ?? m.meta?.secondaryLimit;
  return (
    <div className="grp">
      {agents.includes(Agent.CODEX) && (
        <div className="row">
          Codex 额度
          <span className="v">
            {codex
              ? [m.meta?.primaryLimit, m.meta?.secondaryLimit]
                  .filter((l) => !!l)
                  .map((l) => {
                    const r = quotaRow(l, "", now, true);
                    return `${r.label} ${r.used}`;
                  })
                  .join(" · ") + (m.online ? "" : "（上次记录）")
              : "暂未获取额度"}
          </span>
        </div>
      )}
      {agents.includes(Agent.CLAUDE) && (
        <div className="row">
          Claude Code 额度<span className="v">暂不支持读取额度</span>
        </div>
      )}
      <button className="row" onClick={() => go("q/" + m.id)}>
        查看全部额度<span className="v">›</span>
      </button>
    </div>
  );
}

/** 账户额度: each CLI's account quota on one machine, apart from context
 * use and token counts. Codex reports it in its rollout; Claude Code's is
 * not collected yet, and says so rather than showing anything made up. */
export function QuotaPage({ remote, id, go, onBack }: { remote: Remote; id: string; go: (r: string) => void; onBack: () => void }) {
  useRemote(remote);
  const m = remote.machines.get(id);
  if (!m)
    return (
      <div className="page">
        <NavBar onBack={onBack} />
        <div className="center muted">没有这台设备</div>
      </div>
    );
  const agents = machineAgents(m);
  const now = Date.now();
  const meta = m.meta;
  const rows = [quotaRow(meta?.primaryLimit, "5 小时", now, m.online), quotaRow(meta?.secondaryLimit, "7 天", now, m.online)];
  const got = !!(meta?.primaryLimit || meta?.secondaryLimit);
  // A Claude Code session on this machine, to read /usage in its terminal.
  const claude = [...remote.sessions.values()].find((s) => s.machineId === id && agentOf(s) === Agent.CLAUDE && !exited(s));
  return (
    <div className="page gray">
      <NavBar onBack={onBack} title={<span className="tname">账户额度</span>} sub={`${machineName(m)} · ${m.online ? "在线" : "离线"}`} />
      <div className="body">
        {!m.online && (
          <div className="gbanner">
            <Banner kind="warn">机器离线，显示上次记录</Banner>
          </div>
        )}
        {agents.includes(Agent.CODEX) && (
          <>
            <div className="glabel">
              Codex{meta?.planType ? ` · ${meta.planType}` : ""}
              {!m.online && got ? " · 上次记录" : ""}
            </div>
            <div className="grp">
              {got ? (
                rows.map((r) => (
                  <div className={"row col quota " + r.tone} key={r.label}>
                    <div className="line">
                      <span>{r.label}</span>
                      <span className="v">
                        {r.used}
                        {r.reset && ` · ${r.reset}`}
                      </span>
                    </div>
                    {r.percent !== undefined && (
                      <div className="bar">
                        <i style={{ width: `${Math.min(100, r.percent)}%` }} />
                      </div>
                    )}
                    {r.note && <div className="hint">{r.note}</div>}
                  </div>
                ))
              ) : (
                <div className="row col">
                  <b>暂未获取额度</b>
                  <div className="hint">Codex 在这台机器上下一次回复后会带上额度。</div>
                </div>
              )}
            </div>
            {rows.some((r) => r.tone === "warn" || r.tone === "full") && (
              <div className="gbanner">
                <Banner kind="warn">{rows.some((r) => r.tone === "full") ? "有额度已经用尽" : "有额度即将用尽"}，不会自动切换 CLI 或模型</Banner>
              </div>
            )}
          </>
        )}
        {agents.includes(Agent.CLAUDE) && (
          <>
            <div className="glabel">Claude Code</div>
            <div className="grp">
              <div className="row col">
                <b>暂不支持读取额度</b>
                <div className="hint">电脑上的 remote-agent 还没有采集 Claude Code 的额度。可在本机 Claude Code 中查看 /usage。</div>
              </div>
              <button
                className="row"
                disabled={!m.online || !claude}
                onClick={() => {
                  if (!claude) return;
                  remote.termFirst.add(claude.id);
                  go("s/" + claude.id);
                }}
              >
                去终端查看 /usage<span className="v">{claude ? "›" : "没有进行中的会话"}</span>
              </button>
            </div>
            <div className="manage-hint">只打开终端，不自动输入命令。</div>
          </>
        )}
        <div className="manage-hint">额度属于 CLI 登录账户，该账户的其他使用也可能计入。</div>
      </div>
    </div>
  );
}

/** Leaving the account on this browser: its keys go, nothing else. */
function LeaveAlert({ onCancel }: { onCancel: () => void }) {
  return (
    <Alert
      title="退出这个浏览器？"
      titleRed
      text="这个浏览器里的账户密钥会被删除，要重新配对才能再用。电脑上的会话不受影响。"
      ok="退出"
      danger
      onCancel={onCancel}
      onOk={async () => {
        await forgetAccount();
        location.hash = "";
        location.reload();
      }}
    />
  );
}

/** 设备: computers, which run the CLIs, apart from clients, which operate
 * sessions. Paired is not online, watching or owning anything. */
export function DevicesPage({ remote, go, onBack }: { remote: Remote; go: (r: string) => void; onBack: () => void }) {
  useRemote(remote);
  const me = remote.account.deviceId;
  const machines = [...remote.machines.values()].sort((a, b) => machineName(a).localeCompare(machineName(b)));
  const clients = [...remote.devices.values()].filter((d) => d.kind === DeviceKind.CLIENT).sort((a, b) => (a.id === me ? -1 : b.id === me ? 1 : Number(a.createdMs - b.createdMs)));
  return (
    <div className="page gray">
      <NavBar onBack={onBack} title={<span className="tname">设备</span>} />
      <div className="body">
        <div className="glabel">电脑 · 运行 CLI</div>
        <div className="grp">
          {machines.map((m) => (
            <button key={m.id} className="row" onClick={() => go("m/" + m.id)}>
              {machineName(m)}
              <span className="v">
                {m.online ? (
                  <>
                    <Dot tone="idle" />
                    在线
                  </>
                ) : (
                  ago(m.lastSeenMs) || "离线"
                )}{" "}
                ›
              </span>
            </button>
          ))}
          {machines.length === 0 && <div className="row help">还没有电脑。</div>}
        </div>
        <div className="glabel">客户端 · 操作会话</div>
        <div className="grp">
          {remote.devicesStatus === "unsupported" && <div className="row help">服务器版本较旧，只能看到这个浏览器。</div>}
          {remote.devicesStatus === "error" && (
            <div className="row help">
              没能读取设备列表
              <button className="link" onClick={() => void remote.loadDevices()}>
                重试
              </button>
            </div>
          )}
          {(clients.length ? clients : [{ id: me, meta: undefined }]).map((d) => (
            <button key={d.id} className="row" onClick={() => go("c/" + d.id)}>
              {clientName(d, d.id)}
              <span className="v">{d.id === me ? "当前客户端" : "已配对 ›"}</span>
            </button>
          ))}
        </div>
        <div className="manage-hint">客户端名称在设备详情中设置；未命名时显示类型和 ID 短码。已配对不表示现在正在查看。</div>
        <div className="grp">
          <button className="row add" onClick={() => go("add")}>
            ＋ 添加电脑或客户端
          </button>
        </div>
      </div>
    </div>
  );
}

/** A client: its name (sealed; synced to the other clients), its ID to
 * check, and removing it, or leaving on this one. */
export function ClientPage({ remote, id, go, onBack }: { remote: Remote; id: string; go: (r: string) => void; onBack: () => void }) {
  useRemote(remote);
  const d = remote.devices.get(id);
  const current = id === remote.account.deviceId;
  const [renaming, setRenaming] = useState(false);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [removing, setRemoving] = useState(false);
  const [leaving, setLeaving] = useState(false);
  const canEdit = remote.devicesStatus === "ready" && remote.conn === "online";
  const save = async () => {
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
    <div className="page gray">
      <NavBar onBack={onBack} title={<span className="tname">{clientName(d, id)}</span>} sub={current ? "当前客户端" : "已配对的客户端"} />
      <div className="body">
        <div className="glabel">客户端</div>
        <div className="grp">
          <button
            className="row"
            disabled={!canEdit}
            onClick={() => {
              setName(d?.meta?.name ?? "");
              setRenaming(true);
            }}
          >
            名称<span className="v">{d?.meta?.name || "未命名"} ›</span>
          </button>
          {d?.meta?.description && (
            <div className="row">
              运行在<span className="v">{d.meta.description}</span>
            </div>
          )}
          <div className="row">
            设备 ID<span className="v mono">…{shortId(id)}</span>
          </div>
        </div>
        <div className="manage-hint">修改名称同步到其他客户端。{current ? "" : "通知开关要在那台设备上设置，不在这里远程修改。"}</div>
        {error && (
          <div className="gbanner">
            <Banner kind="err">没能保存名称：{error}</Banner>
          </div>
        )}
        <div className="grp">
          {current ? (
            <button className="row danger" onClick={() => setLeaving(true)}>
              退出并删除本机密钥
            </button>
          ) : (
            <button className="row danger" disabled={remote.conn !== "online"} onClick={() => setRemoving(true)}>
              移除这个客户端…
            </button>
          )}
        </div>
        {!current && <div className="manage-hint">移除会断开它的登录并清除推送订阅，不结束电脑上的会话，不删除会话历史。持有账户密钥的设备仍可重新加入。</div>}
      </div>
      {renaming && (
        <Sheet onClose={() => setRenaming(false)}>
          <h4>客户端名称</h4>
          <input className="field" autoFocus value={name} placeholder={clientName(undefined, id)} maxLength={40} onChange={(e) => setName(e.target.value)} aria-label="客户端名称" />
          <div className="hint">名称加密后同步，服务器看不到。留空就用类型和 ID 短码。</div>
          <div className="btns" style={{ marginTop: 12 }}>
            <button className="btn pri" disabled={busy} onClick={() => void save()}>
              {busy ? "保存中…" : "保存"}
            </button>
          </div>
        </Sheet>
      )}
      {removing && (
        <Alert
          title={`移除 ${clientName(d, id)}？`}
          titleRed
          text="它的登录会失效、推送订阅会删除；电脑上的会话和历史不受影响。它持有账户密钥，仍可以重新加入。"
          ok="移除"
          danger
          onCancel={() => setRemoving(false)}
          onOk={async () => {
            setRemoving(false);
            try {
              await remote.removeDevice(id);
              go("devices");
            } catch (e) {
              setError(errorText(e));
            }
          }}
        />
      )}
      {leaving && <LeaveAlert onCancel={() => setLeaving(false)} />}
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
      <div className="glabel">本客户端的通知</div>
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
  const modelName = (a: Agent, slug: string) => modelLabel(models.get(a)?.models, slug, a);

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
        <QuotaSummary m={m} go={go} />
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
                <div className="glabel">{agentName(a)} · 新会话默认设置</div>
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
          <button className="row danger" disabled={remote.conn !== "online"} onClick={() => setRemoving(true)}>
            移除这台机器…
          </button>
        </div>
        <div className="manage-hint">移除会删除这台机器在服务器上的会话记录，并断开连接；它需要重新加入账户才能连接。</div>
      </div>
      {sheet}
      {removing && (
        <Alert
          title={`移除 ${name}？`}
          titleRed
          text="它在服务器上的会话记录会被删除，连接会断开；它要重新加入账户才能再连上。电脑上的会话和文件不受影响。"
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
