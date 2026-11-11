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
          <GlassButton label="返回" onClick={onBack}>
            <ChevronLeft />
          </GlassButton>
        </div>
      )}
      <div className="large">
        <h3>设置</h3>
      </div>
      <div className="set-banners">
        {push.top}
        <RecoveryNotice remote={remote} go={go} />
        <ClockBanner remote={remote} />
      </div>
      <div className="label">
        电脑
        <button className="link" onClick={() => go("devices")}>
          全部设备
          <ChevronRight />
        </button>
      </div>
      {machines.map((m) => (
        <MachineCard key={m.id} m={m} go={go} />
      ))}
      {machines.length === 0 && <p className="gf">还没有电脑。</p>}
      <ManagementEntry remote={remote} go={go} />
      <div className="label">客户端</div>
      <div className="grp">
        <button className="row" onClick={() => go("c/" + me)}>
          <ClientIcon d={remote.devices.get(me)} />
          <span className="row-name">{clientName(remote.devices.get(me), remote.devices, me)}</span>
          <span className="v">当前</span>
          <ChevronRight className="chev" />
        </button>
        {others.slice(0, 3).map((d) => (
          <button key={d.id} className="row" onClick={() => go("c/" + d.id)}>
            <ClientIcon d={d} />
            <span className="row-name">{clientName(d, remote.devices)}</span>
            <span className="v">已配对</span>
            <ChevronRight className="chev" />
          </button>
        ))}
        {others.length > 3 && (
          <button className="row act" onClick={() => go("devices")}>
            全部客户端
            <ChevronRight className="chev" />
          </button>
        )}
      </div>
      <SecurityGroup remote={remote} go={go} />
      {push.group}
      <div className="label">外观</div>
      <div className="grp">
        <div className="row">
          <span className="ico">
            <Moon />
          </span>
          主题
          <ThemePicker />
        </div>
      </div>
      <div className="label">关于</div>
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
      <div className="label">账户</div>
      <div className="grp">
        <div className="row">
          服务器<span className="v">{new URL(remote.account.server).host}</span>
        </div>
        <button className="row" onClick={() => setChecking(true)}>
          连接检测
          <span className="v">
            {conn.text && <StateMark kind={conn.tone} />}
            {conn.text}
          </span>
          <ChevronRight className="chev" />
        </button>
        <button className="row danger" onClick={() => setLeaving(true)}>
          退出并删除本机密钥…
        </button>
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
function MachineCard({ m, go }: { m: MachineView; go: (r: string) => void }) {
  const now = useNow(30_000);
  const agents = machineAgents(m);
  const views = machineQuotaViews(m.meta, agents, now, m.online);
  return (
    <div className={"mach-card" + (m.online ? "" : " offl")}>
      <button className="mach-head" onClick={() => go("m/" + m.id)}>
        <span className="mach-name">
          <b>{machineName(m)}</b>
          <ChevronRight className="chev" />
        </span>
        <span className="s">
          <StateMark kind={m.online ? "online" : "off"} />
          {m.online ? "在线" : m.lastSeenMs ? `${ago(m.lastSeenMs)}在线` : "离线"}
        </span>
      </button>
      {m.online &&
        agents.map((a) => {
          const q = tightest(views.filter((v) => v.agent === agentInfo(a).agent));
          return (
            <button key={a} className={"qt" + (q.tone ? " " + q.tone : "")} onClick={() => go("q/" + m.id)}>
              <span className="l">
                <b>{agentName(a)}</b>
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
  const rows = views.flatMap((v) => v.rows).filter((r) => r.percent !== undefined && r.left !== "未获取");
  if (!rows.length) return { left: quotaSummary(views) || "暂未获取额度", reset: "", tone: "" };
  const r = rows.reduce((a, b) => ((b.percent ?? -1) > (a.percent ?? -1) ? b : a));
  return { left: r.left, reset: r.reset.split(" · ")[0], percent: r.percent, tone: r.tone };
}

/** Checking for a newer build of this page, and moving on to it. */
function UpdateRow() {
  const update = useUpdate();
  if (update === "ready")
    return (
      <button className="row" onClick={applyUpdate}>
        新版本已下载
        <span className="v">重新加载</span>
        <ChevronRight className="chev" />
      </button>
    );
  return (
    <button
      className="row act"
      disabled={update === "checking"}
      onClick={() => void checkUpdate(true)}
    >
      检查更新
      <span className="v">
        {update === "checking" ? (
          <>
            <LoaderCircle /> 正在检查…
          </>
        ) : update === "latest" ? (
          "已是最新"
        ) : update === "failed" ? (
          "没能下载，稍后再试"
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
      <div className="label">账户额度</div>
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
              <span className="nw">{agentName(a)}</span>
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
        <div className="center muted">没有这台设备</div>
      </div>
    );
  const views = machineQuotaViews(m.meta, machineAgents(m), clock, m.online);
  return (
    <div className="page set">
      <NavBar
        onBack={onBack}
        title="账户额度"
        sub={`${machineName(m)} · ${m.online ? "在线" : "离线"}`}
      />
      <div className="body">
        {!m.online && (
          <div className="set-banners">
            <Banner kind="warn">机器离线，显示上次记录</Banner>
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
                          ? (agentInfo(v.agent).usageHint ?? "可在本机 CLI 中查看用量")
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
            {v.seen && v.rows.length > 0 && (
              <div className="gf">{v.seen}</div>
            )}
            {v.banner && (
              <div className="set-banners">
                <Banner kind="warn">{v.banner}，不会自动切换 CLI 或模型</Banner>
              </div>
            )}
          </div>
        ))}
        <div className="gf">
          时间按北京时间显示。额度属于 CLI
          登录账户，该账户的其他使用也可能计入。
        </div>
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
        <div className="et">{remote.removalVerified ? "本机已被移出账户" : "暂时无法登录中继"}</div>
        <p className="muted small">
          {remote.removalVerified
            ? "已通过账户签名状态核实本机被移除。电脑上的会话不受影响；重新加入需要管理端提供配对码。"
            : "可能是设备被移除、服务重置或连接出了问题。请先再试一次；仍无法登录时，请在管理端查看这台设备是否还在账户里。电脑上的会话不受影响。"}
        </p>
        <RejoinButton server={remote.account.server}>重新加入账户</RejoinButton>
        <p className="muted small">
          会先删除这个浏览器里的账户密钥，再用配对码加入。
        </p>
        <button className="link mid" onClick={async () => {
          try { await remote.resumeSecurityOperation(); setResult(await remote.authority.pending() ? "原操作尚未完成，请继续核实。" : "原安全操作已核实完成，可以重新加入账户。"); }
          catch (e) { setResult(errorText(e)); }
        }}>核实并继续安全操作</button>
        {result && <Banner>{result}</Banner>}
        <button className="link mid" onClick={() => remote.retryLogin()}>
          再试一次
        </button>
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
  const machines = [...remote.machines.values()].sort((a, b) =>
    machineName(a).localeCompare(machineName(b)),
  );
  const clients = sortClients(
    [...remote.devices.values()].filter((d) => d.kind === DeviceKind.CLIENT),
    me,
  );
  // The times on this page are as of opening it.
  useEffect(() => {
    void remote.loadDevices();
  }, [remote]);
  return (
    <div className="page set">
      <NavBar onBack={onBack} title="设备" />
      <div className="body">
        <div className="label">电脑 · 运行 CLI</div>
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
                    在线
                  </>
                ) : (
                  ago(m.lastSeenMs) || "离线"
                )}
              </span>
              <ChevronRight className="chev" />
            </button>
          ))}
          {machines.length === 0 && (
            <div className="row help">还没有电脑。</div>
          )}
          {remote.isAuthority && <ManagementEntry remote={remote} go={go} row />}
        </div>
        {!remote.isAuthority && <ManagementEntry remote={remote} go={go} />}
        <div className="label">客户端 · 操作会话</div>
        <div className="grp">
          {remote.devicesStatus === "error" && (
            <div className="row help">
              没能读取设备列表
              <button
                className="link"
                onClick={() => void remote.loadDevices()}
              >
                重试
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
                  {d.id === me ? "当前客户端" : d.id === remote.authorityDeviceId ? "管理端" : clientUsed(d)}
                </span>
                {d.id !== me && <ChevronRight className="chev" />}
              </button>
            ),
          )}
        </div>
        <div className="gf">
          右边是上次使用的时间。名称只能在那台设备上修改。
        </div>
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
            ? "当前客户端"
            : id === remote.authorityDeviceId
              ? "管理端"
              : "已配对的客户端"
        }
      />
      <div className="body">
        <div className="label">客户端</div>
        <div className="grp">
          <button
            className="row"
            disabled={!canEdit}
            onClick={() => {
              setName(d?.meta?.name ?? "");
              setRenaming(true);
            }}
          >
            名称
            <span className="v">{d?.meta?.name || "未命名"}</span>
            {canEdit && <ChevronRight className="chev" />}
          </button>
          {d?.meta?.name && (
            <button
              className="row act"
              disabled={!canEdit || busy}
              onClick={() => void save("")}
            >
              恢复默认名称
              <span className="v">{defaultClientName(d, remote.devices, id)}</span>
            </button>
          )}
          {d?.meta?.description && (
            <div className="row">
              运行在<span className="v">{d.meta.description}</span>
            </div>
          )}
          <div className="row">
            上次使用
            <span className="v">
              {current ? "现在" : lastUsed(d) || "没有记录"}
            </span>
          </div>
          {!!d?.createdMs && (
            <div className="row">
              添加于<span className="v">{dayText(d.createdMs)}</span>
            </div>
          )}
          <div className="row">
            设备 ID<span className="v mono">…{shortId(id)}</span>
          </div>
        </div>
        <div className="gf">
          {current
            ? "修改名称同步到其他客户端。"
            : "名称和通知开关要在那台设备上设置。"}
        </div>
        {error && (
          <div className="set-banners">
            <Banner kind="err">没能保存名称：{error}</Banner>
          </div>
        )}
        {current ? (
          <div className="grp">
            <button className="row danger" onClick={() => setLeaving(true)}>
              退出并删除本机密钥…
            </button>
          </div>
        ) : (
          <DeviceActions remote={remote} id={id} onRemoved={onBack} />
        )}
      </div>
      {renaming && (
        <Sheet onClose={() => setRenaming(false)} title="客户端名称">
          <input
            className="field"
            autoFocus
            value={name}
            placeholder={defaultClientName(d, remote.devices, id)}
            maxLength={40}
            onChange={(e) => setName(e.target.value)}
            aria-label="客户端名称"
          />
          <div className="hint">
            名称加密后同步，服务器看不到。留空就用默认名称。
          </div>
          <div className="btns sheet-foot">
            <button
              className="btn pri"
              disabled={busy}
              onClick={() => void save(name)}
            >
              {busy ? "保存中…" : "保存"}
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
          action={help ? undefined : "说明"}
          onAction={() => setHelp(true)}
        >
          iPhone 要先「添加到主屏幕」，从主屏幕打开才能收推送
          {help &&
            "：在 Safari 里点分享按钮，选「添加到主屏幕」，再从主屏幕上的图标打开。"}
        </Banner>
      </div>
    );
  else if (st === "unsupported")
    warn = (
      <div className="set-banners">
        <Banner>这个浏览器不支持推送</Banner>
      </div>
    );
  else if (st === "denied")
    warn = (
      <div className="set-banners">
        <Banner kind="warn" action={help ? undefined : "怎么打开"} onAction={() => setHelp(true)}>
          浏览器拒绝了通知权限
          {help &&
            "。在地址栏左边的网站设置里把「通知」改成允许，再回来打开开关。"}
        </Banner>
      </div>
    );
  else if (!prefs.attention && !prefs.turns)
    warn = (
      <div className="set-banners">
        <Banner kind="warn" action="开启" onAction={busy ? undefined : () => void change({ ...prefs, attention: true })}>
          会话提醒没开，审批来了不会提醒你
        </Banner>
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
            action="重试"
            onAction={() => last && void change(last)}
          >
            订阅失败：{error}
          </Banner>
        </div>
      )}
    </>
  );
  const group = (
    <>
      <div className="label">本客户端的通知</div>
      <div className="grp">
        <div className="row">
          安全通知
          <span className="v">{!usable ? "无法接收" : securitySubscribed ? "已开启" : <button className="link" disabled={busy} onClick={() => void change(prefs)}>开启安全通知</button>}</span>
        </div>
        <div className="row">
          等我处理时
          <Switch
            label="等我处理时"
            on={prefs.attention}
            disabled={busy || !usable}
            onChange={(v) => void change({ ...prefs, attention: v })}
          />
        </div>
        <div className="row">
          一轮结束时
          <Switch
            label="一轮结束时"
            on={prefs.turns}
            disabled={busy || !usable}
            onChange={(v) => void change({ ...prefs, turns: v })}
          />
        </div>
        <div className="row">
          定时任务结果
          <Switch label="定时任务结果" on={prefs.scheduled !== false} disabled={busy || !usable} onChange={(v) => void change({ ...prefs, scheduled: v })} />
        </div>
      </div>
      <div className="gf">安全通知独立于会话提醒。通知不保证送达，建议每天打开管理端核实恢复申请。</div>
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
  if (m < 0) return "关闭";
  return m % 60 === 0 ? `${m / 60} 小时` : `${m} 分钟`;
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
        <Sheet onClose={() => setPick(undefined)} title="审批超时">
          <div className="list">
            {timeouts.map((t) => (
              <button
                key={t}
                className="mi"
                onClick={() => void save({ ...st, approvalTimeoutSeconds: t })}
              >
                <div className="mt">{t / 60} 分钟</div>
                {t === st.approvalTimeoutSeconds && (
                  <span className="ck">
                    <Check />
                  </span>
                )}
              </button>
            ))}
          </div>
          <div className="hint">没人回复的审批过了这个时间就拒绝</div>
        </Sheet>
      );
    else if (pick.what === "idleClose")
      sheet = (
        <Sheet onClose={() => setPick(undefined)} title="空闲自动结束会话">
          <div className="list">
            {idleCloseChoices(st.idleCloseMinutes).map((t) => (
              <button
                key={t}
                className="mi"
                onClick={() => void save({ ...st, idleCloseMinutes: t })}
              >
                <div className="mt">
                  {idleCloseLabel(t)}
                  {t === idleCloseDefault && "（默认）"}
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
            会话这么久没有任何活动（没在运行、没有待处理、终端里没人输入）就自动结束，随时可以恢复，聊天记录都在
          </div>
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
      const follow = `跟随 ${agentName(a)} 配置`;
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
                      ? "当前账户不支持自动模式"
                      : "当前模型没有自动模式"
                    : undefined,
                off: x === ApprovalMode.AUTO && noAuto,
                on: x === def.mode,
                next: withDefaults(st, a, { mode: x }),
              }));
      sheet = (
        <Sheet
          onClose={() => setPick(undefined)}
          title={(agents.length > 1 ? agentName(a) + " · " : "") + { model: "默认模型", effort: "默认推理强度", mode: "默认审批模式" }[pick.what]}
        >
          {pick.what === "model" && !list && (
            <div className="hint top">读取模型列表…</div>
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
      <NavBar onBack={onBack} title={name} sub={m.online ? "在线" : ago(m.lastSeenMs) ? `${ago(m.lastSeenMs)}在线` : "离线"} />
      <div className="body">
        <QuotaSummary m={m} go={go} />
        {versions.length > 0 && (
          <div className="grp">
            {versions.map((v) => (
              <div className="row" key={v.agent}>
                {agentName(v.agent)} 版本
                <span className="v">
                  {!v.verified && "未验证 · "}
                  {v.version}
                </span>
              </div>
            ))}
            {versions.some((v) => !v.verified) && (
              <div className="row help">
                版本未验证：在这台电脑上运行 remote-agent
                doctor，它会用一个临时会话检查各项功能。
              </div>
            )}
          </div>
        )}
        {!m.online && (
          <div className="label">{name} 不在线，暂时看不到它的设置。</div>
        )}
        {error && (
          <div className="set-banners">
            <Banner
              kind="err"
              action="重试"
              onAction={() => setAttempt((a) => a + 1)}
            >
              没能读取设置：{error}
            </Banner>
          </div>
        )}
        {saveError && (
          <div className="set-banners">
            <Banner
              kind="err"
              action="重试"
              onAction={() => void save(saveError.next)}
            >
              没能保存：{saveError.text}
            </Banner>
          </div>
        )}
        {st &&
          agents.length > 0 &&
          (() => {
            const a = activeAgent;
            const def = defaultsFor(st, a);
            const follow = "跟随配置";
            return (
              <>
                <div className="label">
                  {agents.length > 1
                    ? "新项目默认设置"
                    : `新项目默认设置 · ${agentName(a)}`}
                </div>
                {agents.length > 1 && (
                  <Segmented
                    className="cli-tabs full"
                    label="按 CLI 的新项目默认设置"
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
                    模型
                    <span className="v">
                      {def.model ? modelName(a, def.model) : follow}
                    </span>
                    <ChevronRight className="chev" />
                  </button>
                  <button
                    className="row"
                    onClick={() => setPick({ agent: a, what: "effort" })}
                  >
                    推理强度
                    <span className="v">
                      {def.effort || follow}
                    </span>
                    <ChevronRight className="chev" />
                  </button>
                  <button
                    className="row"
                    onClick={() => setPick({ agent: a, what: "mode" })}
                  >
                    审批模式
                    <span className="v">
                      {modeName(def.mode) || follow}
                    </span>
                    <ChevronRight className="chev" />
                  </button>
                  {!def.mode && (
                    <div className="row help">
                      不指定时实际用：
                      {/* 完全放行 is red wherever it is written. */}
                      <span className={defaultModeOf(m, a).mode === ApprovalMode.FULL_ACCESS ? "red" : undefined}>
                        {modeLabel(ApprovalMode.UNSPECIFIED, defaultModeOf(m, a), a).replace(/^默认：/, "")}
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
              审批超时
              <span className="v">
                {Math.round(st.approvalTimeoutSeconds / 60)} 分钟
              </span>
              <ChevronRight className="chev" />
            </button>
            <button
              className="row"
              onClick={() => setPick({ what: "idleClose" })}
            >
              空闲自动结束会话
              <span className="v">
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
            <div className="label">项目</div>
            <div className="grp">
              {projs.map((p) => (
                <button
                  key={p.key}
                  className="row"
                  onClick={() => go("p/" + encodeURIComponent(p.key))}
                >
                  {p.name}
                  <span className="v">{shortPath(p.key)}</span>
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
    ["auto", "自动"],
    ["light", "亮色"],
    ["dark", "暗色"],
  ];
  return (
    <div className="seg" role="radiogroup" aria-label="外观">
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
  if (value > 0 && value < 104858) return "不到 0.1 MiB";
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
          `可清理 ${storageSize(usage.reclaimableBytes)}`,
        usage.reservedBytes > 0n &&
          `正在上传 ${storageSize(usage.reservedBytes)}`,
      ]
        .filter(Boolean)
        .join(" · ")
    : "";
  return (
    <>
      <div className="label">附件存储</div>
      <div className="grp">
        <div className="row col">
          <div className="line">
            <span>上传的图片和文件</span>
            <span className="v">
              {usage ? storageSize(usage.uploadBytes) : "读取中…"}
            </span>
          </div>
          {parts && <div className="hint">{parts}</div>}
        </div>
        <div className="row">
          空间上限
          <select
            aria-label="空间上限"
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
          预览缓存
          <span className="v">
            {usage ? storageSize(usage.cacheBytes) : "—"}
          </span>
        </div>
        <button
          className="row act"
          disabled={busy || !online || free === 0n}
          onClick={() => setClean(true)}
        >
          清理…
          <span className="v">
            {usage
              ? free > 0n
                ? `可释放 ${storageSize(free)}`
                : "没有可清理的"
              : ""}
          </span>
        </button>
      </div>
      <div className="gf">
        附件存在这台电脑上。会话使用过的文件保留到会话结束；正在发送和执行中用到的图片不会被清理。预览缓存最多
        150 MiB，自动回收。
      </div>
      {error && (
        <div className="set-banners">
          <Banner kind="err">{error}</Banner>
        </div>
      )}
      {clean && (
        <Alert
          title="清理可回收附件和缓存？"
          text="清除可回收原图、预览和待同步的大图缓存。排队与当前执行中的原图、会话文件和历史保留；尚未同步的大图会只显示预览。"
          ok="清理"
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
