// Sheets opened from a session's chips: the approval mode, and the
// model, reasoning effort and context use.

import { useEffect, useState } from "react";
import { ApprovalMode, type Agent } from "./gen/remoteagent/v1/event_pb";
import type { ListModelsResponse, ModelInfo } from "./gen/remoteagent/v1/local_pb";
import { agentName, agentOf, CONTEXT_WARN, findModel, contextPercent, modeOf, noAutoMode } from "./model";
import { errorText, type Remote, type SessionView } from "./remote";
import { Alert, Banner, Sheet } from "./ui";

export function modeList(agent: Agent): { mode: ApprovalMode; name: string; desc: string; danger?: boolean }[] {
  return [
    { mode: ApprovalMode.ASK, name: "询问", desc: "需要审批时由你确认，超时拒绝" },
    { mode: ApprovalMode.AUTO, name: "自动", desc: `由 ${agentName(agent)} 自己判断，拿不准的在对话里问你` },
    { mode: ApprovalMode.PLAN, name: "计划", desc: "只读和出计划，你批准后才开始改" },
    { mode: ApprovalMode.FULL_ACCESS, name: "完全放行", desc: "所有操作都不审批", danger: true },
  ];
}

/** Asks before switching to full access; used wherever it can be chosen. */
export function FullAccessAlert({ onOk, onCancel, forNew }: { onOk: () => void; onCancel: () => void; forNew?: boolean }) {
  return (
    <Alert
      title="切到完全放行？"
      titleRed
      text={`之后所有操作都不会再问你，手机上也收不到审批。只对${forNew ? "这次新建的" : "这个"}会话生效。`}
      ok="切换"
      danger
      onCancel={onCancel}
      onOk={onOk}
    />
  );
}

/** Why a session cannot switch to mode, or "" if it can. */
export function unavailable(s: SessionView, mode: ApprovalMode): string {
  if (mode === ApprovalMode.FULL_ACCESS && s.info?.fullAccessUnavailable) return "这个会话启动时没有允许完全放行，Claude Code 切不过去；要用就新建会话时选它";
  if (mode === ApprovalMode.AUTO && noAutoMode(s)) return "Haiku 没有自动模式";
  return "";
}

export function ModeSheet({ remote, s, onClose }: { remote: Remote; s: SessionView; onClose: () => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [confirmFull, setConfirmFull] = useState(false);
  const mode = modeOf(s);
  const cur = mode?.mode || undefined;
  const online = remote.conn === "online";
  const pick = async (mode: ApprovalMode) => {
    setBusy(true);
    setError("");
    try {
      await remote.command(s.id, { case: "setApprovalMode", value: { sessionId: s.id, mode } });
      onClose();
    } catch (e) {
      setError(errorText(e));
      setBusy(false);
    }
  };
  if (confirmFull)
    return (
      <FullAccessAlert
        onCancel={() => setConfirmFull(false)}
        onOk={() => {
          setConfirmFull(false);
          void pick(ApprovalMode.FULL_ACCESS);
        }}
      />
    );
  return (
    <Sheet onClose={onClose}>
      <h4>审批模式</h4>
      <div className="list">
        {modeList(agentOf(s)).map((m) => {
          const why = unavailable(s, m.mode);
          return (
            <button
              key={m.mode}
              className={"mi" + (m.danger ? " danger" : "") + (why ? " dis" : "")}
              disabled={busy || !online || !!why}
              onClick={() => (m.mode === cur ? onClose() : m.danger ? setConfirmFull(true) : void pick(m.mode))}
            >
              <div className="mt">
                <b>{m.name}</b>
                <div className="md">{why || m.desc}</div>
              </div>
              {m.mode === cur && <span className="ck">✓</span>}
            </button>
          );
        })}
      </div>
      {!cur && mode && <div className="hint">现在是 {agentName(agentOf(s))} 的 {mode.text} 模式，不在这四种里；选一种就会切过去</div>}
      {error && (
        <Banner kind="err" action="去终端" onAction={onClose}>
          没能切换：{error}
        </Banner>
      )}
      <div className="hint">{busy ? "正在切换…" : "只改当前会话"}</div>
    </Sheet>
  );
}

const efforts = ["low", "medium", "high", "xhigh", "max", "ultra"];

export function tokens(n: bigint): string {
  return n >= 1000n ? `${Math.round(Number(n) / 1000)}k` : String(n);
}

export function SessionSheet({ remote, s, running, onClose }: { remote: Remote; s: SessionView; running: boolean; onClose: () => void }) {
  const [models, setModels] = useState<ListModelsResponse>();
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const [all, setAll] = useState(false);
  const [switching, setSwitching] = useState("");
  const [failed, setFailed] = useState("");
  const [compacting, setCompacting] = useState(false);
  const agent = agentOf(s);
  useEffect(() => {
    setError("");
    remote.listModels(s.machineId, agent).then(setModels, (e) => setError(errorText(e)));
  }, [remote, s.machineId, agent, attempt]);
  const pct = contextPercent(s);
  const model = s.info?.model ?? "";
  const effort = s.info?.effort ?? "";
  const cur = findModel(models?.models, model);
  const list = models?.models ?? [];
  // A short list shows whole, so no model is out of reach behind 全部模型
  // (Claude Code offers four); a long one shows three, and the current
  // model even when it is not among them.
  const few = list.length <= 5;
  const shown = all || few ? list : [...list.slice(0, 3), ...(cur && !list.slice(0, 3).includes(cur) ? [cur] : [])];
  const offline = remote.conn !== "online";
  const locked = !!switching || running || offline;
  const set = async (m: ModelInfo, e: string) => {
    setSwitching(m.displayName || m.slug);
    setFailed("");
    try {
      await remote.command(s.id, { case: "setModel", value: { sessionId: s.id, model: m.slug, effort: e } });
    } catch (err) {
      setFailed(`没能切到 ${m.displayName || m.slug}：${errorText(err)}`);
    } finally {
      setSwitching("");
    }
  };
  const compact = async () => {
    setCompacting(true);
    try {
      await remote.command(s.id, { case: "compact", value: { sessionId: s.id } });
      onClose();
    } catch (err) {
      setFailed("没能压缩：" + errorText(err));
      setCompacting(false);
    }
  };
  // Some models have no effort levels at all (Claude Code's haiku).
  const noEffort = !!cur && cur.efforts.length === 0;
  const offered = cur?.efforts.length ? cur.efforts : efforts;
  const at = Math.max(0, offered.indexOf(effort));
  const pos = (i: number) => `${(i / Math.max(1, offered.length - 1)) * 100}%`;
  return (
    <Sheet onClose={onClose}>
      <h4>会话设置</h4>
      {pct !== undefined && (
        <div className="ctxbox">
          <div className={"ring big" + (pct > CONTEXT_WARN ? " warn" : "")} style={{ ["--p" as string]: pct }} />
          <div>
            <div className="num">{pct}%</div>
            <div className="muted small">
              {compacting ? "◌ 正在压缩…" : `上下文 ${tokens(s.info!.contextTokens)} / ${tokens(s.info!.contextWindow)}`}
            </div>
          </div>
          <button className="act" disabled={compacting || running || offline} onClick={() => void compact()}>
            压缩
          </button>
        </div>
      )}
      <div className="fl">模型</div>
      {error && (
        <Banner kind="err" action="重试" onAction={() => setAttempt((a) => a + 1)}>
          没能读取模型列表
        </Banner>
      )}
      {!models && !error && <div className="hint">读取模型列表…</div>}
      <div className="list">
        {shown.map((m) => (
          <button key={m.slug} className={"mi" + (locked ? " dis" : "")} disabled={locked} onClick={() => m !== cur && void set(m, m.efforts.includes(effort) ? effort : m.defaultEffort)}>
            <div className="mt">
              {m.displayName || m.slug}
              {m.efforts.length > 0 && m.efforts[m.efforts.length - 1] !== efforts[efforts.length - 1] && <small>最高 {m.efforts[m.efforts.length - 1]}</small>}
            </div>
            {m === cur && <span className="ck">✓</span>}
          </button>
        ))}
        {models && !all && !few && (
          <button className="mi link-row" onClick={() => setAll(true)}>
            全部模型 ›
          </button>
        )}
      </div>
      <div className="fl">推理强度</div>
      {noEffort ? (
        <div className="hint">{cur?.displayName || cur?.slug} 没有推理强度可调</div>
      ) : (
        <>
      <div className={"slider" + (locked || !cur ? " dis" : "")}>
        <div className="track" />
        <div className="fill" style={{ width: pos(at) }} />
        {offered.map((e, i) => (
          <button key={e} className="tick" style={{ left: pos(i) }} disabled={locked || !cur} onClick={() => cur && e !== effort && void set(cur, e)} aria-label={e} />
        ))}
        <div className="knob" style={{ left: pos(at) }} />
      </div>
      <div className="slabels">
        {offered.map((e) => (
          <button key={e} className={e === effort ? "on" : ""} disabled={locked || !cur} onClick={() => cur && e !== effort && void set(cur, e)}>
            {e}
          </button>
        ))}
      </div>
        </>
      )}
      {switching && <Banner>正在切换到 {switching}…</Banner>}
      {failed && (
        <Banner kind="err" action="知道了" onAction={() => setFailed("")}>
          {failed}
        </Banner>
      )}
      <div className="hint">{running ? "这一轮在运行，结束后才能切换模型和推理强度" : offline ? "和服务器断线了，连上后才能切换" : "只在空闲时能切换，只改当前会话"}</div>
    </Sheet>
  );
}
