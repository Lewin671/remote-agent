// Bottom sheets and dialogs from the mockup: approval mode, session
// settings (model, effort, context), the plan card and new sessions.

import { useEffect, useState, type ReactNode } from "react";
import { ApprovalMode, type PlanProposed } from "./gen/remoteagent/v1/event_pb";
import { PlanChoice, type ListDirectoriesResponse, type ListModelsResponse, type ModelInfo } from "./gen/remoteagent/v1/local_pb";
import { contextPercent, machineName, modeOf, projectName, shortPath } from "./model";
import { errorText, type CommandKind, type Remote, type SessionView } from "./remote";
import { Avatar, Dot, Rich } from "./ui";

export function Sheet({ onClose, children }: { onClose: () => void; children: ReactNode }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    addEventListener("keydown", onKey);
    return () => removeEventListener("keydown", onKey);
  }, [onClose]);
  return (
    <div className="overlay">
      <div className="dim" onClick={onClose} />
      <div className="sheet" role="dialog">
        <div className="grab" />
        {children}
      </div>
    </div>
  );
}

export function Alert({ title, text, ok, danger, onOk, onCancel }: { title: string; text: string; ok: string; danger?: boolean; onOk: () => void; onCancel: () => void }) {
  return (
    <div className="overlay">
      <div className="dim" onClick={onCancel} />
      <div className="alert" role="alertdialog">
        <h5 className={danger ? "danger" : ""}>{title}</h5>
        <p>{text}</p>
        <div className="btns">
          <button className="btn" onClick={onCancel}>
            取消
          </button>
          <button className={"btn " + (danger ? "red" : "pri")} onClick={onOk}>
            {ok}
          </button>
        </div>
      </div>
    </div>
  );
}

type Run = (label: string, cmd: CommandKind) => Promise<unknown>;

const modes: { mode: ApprovalMode; name: string; desc: string; danger?: boolean }[] = [
  { mode: ApprovalMode.ASK, name: "询问", desc: "需要审批的操作都转到手机，超时拒绝" },
  { mode: ApprovalMode.AUTO, name: "自动", desc: "由 Codex 自己代为判断，拿不准的在对话里问你" },
  { mode: ApprovalMode.PLAN, name: "计划", desc: "只读和出计划，你批准后才开始改" },
  { mode: ApprovalMode.FULL_ACCESS, name: "完全放行", desc: "所有操作都不审批", danger: true },
];

function currentMode(s: SessionView): ApprovalMode | undefined {
  const text = modeOf(s)?.text;
  return modes.find((m) => m.name === text)?.mode;
}

export function ModeSheet({ s, run, onClose }: { s: SessionView; run: Run; onClose: () => void }) {
  const [busy, setBusy] = useState(false);
  const [confirmFull, setConfirmFull] = useState(false);
  const cur = currentMode(s);
  const pick = async (mode: ApprovalMode) => {
    setBusy(true);
    const ok = await run("切换模式", { case: "setApprovalMode", value: { sessionId: s.id, mode } });
    setBusy(false);
    if (ok) onClose();
  };
  if (confirmFull)
    return (
      <Alert
        title="切到完全放行？"
        text="之后所有操作都不会再问你，手机上也收不到审批。只对这个会话生效。"
        ok="切换"
        danger
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
      <div className="mode">
        {modes.map((m) => (
          <button
            key={m.mode}
            className={"mi" + (m.danger ? " danger" : "")}
            disabled={busy}
            onClick={() => (m.mode === cur ? onClose() : m.danger ? setConfirmFull(true) : void pick(m.mode))}
          >
            <div className="mt">
              <b>{m.name}</b>
              <div className="md">{m.desc}</div>
            </div>
            {m.mode === cur && <span className="ck">✓</span>}
          </button>
        ))}
      </div>
      <div className="hint">{busy ? "正在切换…" : "只改当前会话。运行中也能切，下一轮起生效"}</div>
    </Sheet>
  );
}

const efforts = ["low", "medium", "high", "xhigh", "max", "ultra"];

export function SessionSheet({ remote, s, run, running, onClose }: { remote: Remote; s: SessionView; run: Run; running: boolean; onClose: () => void }) {
  const [models, setModels] = useState<ListModelsResponse>();
  const [error, setError] = useState("");
  const [all, setAll] = useState(false);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    remote.listModels(s.machineId).then(setModels, (e) => setError(errorText(e)));
  }, [remote, s.machineId]);
  const pct = contextPercent(s);
  const model = s.info?.model ?? "";
  const effort = s.info?.effort ?? "";
  const cur = models?.models.find((m) => m.slug === model);
  const shown = all ? models?.models : models?.models.slice(0, 3);
  const set = async (m: ModelInfo, e: string) => {
    setBusy(true);
    await run("切换模型", { case: "setModel", value: { sessionId: s.id, model: m.slug, effort: e } });
    setBusy(false);
  };
  const tokens = (n: bigint) => (n >= 1000n ? `${Math.round(Number(n) / 1000)}k` : String(n));
  const offered = cur?.efforts ?? efforts;
  const at = Math.max(0, offered.indexOf(effort));
  return (
    <Sheet onClose={onClose}>
      <h4>会话设置</h4>
      {pct !== undefined && (
        <div className="ctxbox">
          <div className={"ring ring-lg" + (pct >= 80 ? " warn" : "")} style={{ ["--p" as string]: pct }} />
          <div>
            <div className="num">{pct}%</div>
            <div className="muted">
              上下文 {tokens(s.info!.contextTokens)} / {tokens(s.info!.contextWindow)}
            </div>
          </div>
          <button className="act" disabled={busy || running} onClick={() => void run("压缩", { case: "compact", value: { sessionId: s.id } }).then(onClose)}>
            压缩
          </button>
        </div>
      )}
      <div className="fl">模型</div>
      {error && <p className="error">{error}</p>}
      {!models && !error && <p className="muted small">读取中…</p>}
      <div className="mlist">
        {shown?.map((m) => (
          <button
            key={m.slug}
            className="mi"
            disabled={busy || running}
            onClick={() => m.slug !== model && void set(m, m.efforts.includes(effort) ? effort : m.defaultEffort)}
          >
            {m.displayName}
            {!m.efforts.includes("ultra") && m.efforts.length > 0 && <small>最高 {m.efforts[m.efforts.length - 1]}</small>}
            {m.slug === model && <span className="ck">✓</span>}
          </button>
        ))}
        {models && !all && models.models.length > 3 && (
          <button className="mi more" onClick={() => setAll(true)}>
            全部模型 ›
          </button>
        )}
      </div>
      <div className="fl">Reasoning effort</div>
      <div className="slider">
        <div className="track" />
        <div className="fill" style={{ width: `${(at / Math.max(1, offered.length - 1)) * 100}%` }} />
        {offered.map((e, i) => (
          <button
            key={e}
            className="tick"
            style={{ left: `${(i / Math.max(1, offered.length - 1)) * 100}%` }}
            disabled={busy || running || !cur}
            onClick={() => cur && e !== effort && void set(cur, e)}
            aria-label={e}
          />
        ))}
        <div className="knob" style={{ left: `${(at / Math.max(1, offered.length - 1)) * 100}%` }} />
      </div>
      <div className="slabels">
        {offered.map((e) => (
          <button key={e} className={e === effort ? "on" : ""} disabled={busy || running || !cur} onClick={() => cur && e !== effort && void set(cur, e)}>
            {e}
          </button>
        ))}
      </div>
      <div className="hint">{busy ? "正在切换…" : running ? "运行中不能切换，等这一轮结束" : "只改当前会话，立即生效"}</div>
    </Sheet>
  );
}

export function PlanCard({ plan, run, sessionId }: { plan: PlanProposed; run: Run; sessionId: string }) {
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const answer = async (choice: PlanChoice) => {
    setBusy(true);
    await run("回答计划", { case: "answerPlan", value: { sessionId, choice } });
    setBusy(false);
  };
  return (
    <div className="appr plan">
      <div className="h">
        <b>计划待批准</b>
        <button className="muted" onClick={() => setOpen((o) => !o)}>
          {open ? "收起" : "展开全文 ›"}
        </button>
      </div>
      <div className={"plan-body" + (open ? " open" : "")}>
        <Rich text={plan.text} />
      </div>
      <div className="btns">
        <button className="btn" disabled={busy} onClick={() => void answer(PlanChoice.KEEP_PLANNING)}>
          继续规划…
        </button>
        <button className="btn pri" disabled={busy} onClick={() => void answer(PlanChoice.IMPLEMENT)}>
          批准执行
        </button>
      </div>
      <div className="foot">
        想从干净的上下文开始？<b onClick={() => !busy && void answer(PlanChoice.CLEAR_AND_IMPLEMENT)}>清空上下文后执行 ›</b>
      </div>
    </div>
  );
}

// --- new session ---

const modeChoices = [
  { mode: ApprovalMode.ASK, name: "询问" },
  { mode: ApprovalMode.AUTO, name: "自动" },
  { mode: ApprovalMode.FULL_ACCESS, name: "完全放行" },
];

export function NewSessionSheet({ remote, project, onClose, onCreated }: { remote: Remote; project?: string; onClose: () => void; onCreated: (id: string) => void }) {
  const online = [...remote.machines.values()].filter((m) => m.online);
  // Prefer a machine that already has sessions in this project.
  const projectMachine = project ? [...remote.sessions.values()].find((s) => s.info?.cwd === project && remote.machines.get(s.machineId)?.online)?.machineId : undefined;
  const [machineId, setMachineId] = useState(projectMachine ?? online[0]?.id ?? "");
  const machine = remote.machines.get(machineId);
  const [cwd, setCwd] = useState(project ?? "");
  const [prompt, setPrompt] = useState("");
  const [models, setModels] = useState<ListModelsResponse>();
  const [model, setModel] = useState("");
  const [effort, setEffort] = useState("");
  const [mode, setMode] = useState<ApprovalMode>(ApprovalMode.UNSPECIFIED);
  const [picking, setPicking] = useState<"" | "machine" | "dir" | "model" | "mode">("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!machineId) return;
    setModels(undefined);
    remote.listModels(machineId).then(setModels, () => undefined);
    if (!project) setCwd(machine?.meta?.home ?? "");
  }, [remote, machineId]); // eslint-disable-line react-hooks/exhaustive-deps

  const modelInfo = models?.models.find((m) => m.slug === (model || models.defaultModel));
  const submit = async () => {
    setBusy(true);
    setError("");
    try {
      const res = await remote.machineCommand(machineId, { case: "createSession", value: { cwd, prompt: prompt.trim(), model, effort, mode } });
      if (res.kind.case !== "createSession" || !res.kind.value.session) throw new Error("意外的回复");
      onCreated(res.kind.value.session.id);
    } catch (e) {
      setError(errorText(e));
      setBusy(false);
    }
  };

  if (picking === "dir")
    return (
      <DirectoryPicker
        remote={remote}
        machineId={machineId}
        start={cwd || machine?.meta?.home || ""}
        onPick={(p) => {
          setCwd(p);
          setPicking("");
        }}
        onClose={() => setPicking("")}
      />
    );

  return (
    <Sheet onClose={onClose}>
      <div className="ns-head">
        <Avatar name={projectName(cwd) || "新"} size="sm" />
        <b>新会话</b>
        <button className="muted" onClick={onClose}>
          取消
        </button>
      </div>
      <div className="prompt">
        <textarea autoFocus value={prompt} onChange={(e) => setPrompt(e.target.value)} placeholder="要 Codex 做什么？" rows={4} />
        <div className="pf">
          <button className="go" disabled={busy || !machineId || !cwd} onClick={() => void submit()} aria-label="开始">
            ↑
          </button>
        </div>
      </div>
      {error && <p className="error">{error}</p>}
      {online.length === 0 && <p className="error">没有在线的电脑。</p>}
      <div className="setrows">
        <button className="sr" onClick={() => setPicking(picking === "machine" ? "" : "machine")}>
          <span className="ico" style={{ background: "#5b8def" }}>
            ⌘
          </span>
          机器
          <span className="v">
            <Dot tone={machine?.online ? "idle" : "off"} />
            {machineName(machine)}
          </span>
        </button>
        {picking === "machine" &&
          online.map((m) => (
            <button key={m.id} className="sr sub" onClick={() => (setMachineId(m.id), setPicking(""))}>
              {machineName(m)}
              {m.id === machineId && <span className="ck">✓</span>}
            </button>
          ))}
        <button className="sr" onClick={() => setPicking("dir")}>
          <span className="ico" style={{ background: "#f0a35a" }}>
            ▤
          </span>
          目录<span className="v">{shortPath(cwd) || "选择"}</span>
        </button>
        <button className="sr" onClick={() => setPicking(picking === "model" ? "" : "model")}>
          <span className="ico" style={{ background: "#8a5fd6" }}>
            ✦
          </span>
          模型
          <span className="v">
            {model ? modelInfo?.displayName : "默认模型"} · {effort || "默认"}
          </span>
        </button>
        {picking === "model" && models && (
          <div className="sr sub pickers">
            <div className="opts">
              {models.models.map((m) => (
                <button key={m.slug} className={"opt" + (m.slug === model ? " on" : "")} onClick={() => (setModel(m.slug), setEffort(""))}>
                  {m.displayName}
                </button>
              ))}
            </div>
            <div className="opts">
              {(modelInfo?.efforts ?? efforts).map((e) => (
                <button key={e} className={"opt" + (e === effort ? " on" : "")} onClick={() => setEffort(e)}>
                  {e}
                </button>
              ))}
            </div>
          </div>
        )}
        <button className="sr" onClick={() => setPicking(picking === "mode" ? "" : "mode")}>
          <span className="ico" style={{ background: "#22a05a" }}>
            ✓
          </span>
          审批<span className="v">{modeChoices.find((m) => m.mode === mode)?.name ?? "默认"}</span>
        </button>
        {picking === "mode" && (
          <div className="sr sub pickers">
            <div className="opts">
              {modeChoices.map((m) => (
                <button key={m.mode} className={"opt" + (m.mode === mode ? " on" : "")} onClick={() => (setMode(m.mode), setPicking(""))}>
                  {m.name}
                </button>
              ))}
            </div>
          </div>
        )}
      </div>
      <div className="hint">第一次在某个目录启动时，Codex 会问是否信任它，需要去终端确认。</div>
    </Sheet>
  );
}

function DirectoryPicker({ remote, machineId, start, onPick, onClose }: { remote: Remote; machineId: string; start: string; onPick: (p: string) => void; onClose: () => void }) {
  const [dir, setDir] = useState<ListDirectoriesResponse>();
  const [error, setError] = useState("");
  const open = async (path: string) => {
    setError("");
    try {
      const r = await remote.machineCommand(machineId, { case: "listDirectories", value: { path } });
      if (r.kind.case === "listDirectories") setDir(r.kind.value);
    } catch (e) {
      setError(errorText(e));
    }
  };
  useEffect(() => {
    void open(start);
  }, []); // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <Sheet onClose={onClose}>
      <div className="ns-head">
        <b className="ellipsis">{dir ? shortPath(dir.path) : "选择目录"}</b>
        <button className="muted" onClick={onClose}>
          取消
        </button>
      </div>
      {error && <p className="error">{error}</p>}
      <div className="dirs">
        {dir?.parent && (
          <button className="sr" onClick={() => void open(dir.parent)}>
            ‹ 上一级
          </button>
        )}
        {dir?.dirs.map((d) => (
          <button key={d} className="sr" onClick={() => void open(dir.path.replace(/\/$/, "") + "/" + d)}>
            ▤ {d}
          </button>
        ))}
        {dir && dir.dirs.length === 0 && <p className="muted small pad">没有子目录</p>}
      </div>
      <button className="big" disabled={!dir} onClick={() => dir && onPick(dir.path)}>
        在这里开始
      </button>
    </Sheet>
  );
}
