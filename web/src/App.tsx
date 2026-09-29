import { timestampMs } from "@bufbuild/protobuf/wkt";
import { useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import type { ApprovalRequested, Event, QuestionAsked, TokenUsage } from "./gen/remoteagent/v1/event_pb";
import { ApprovalDecision, InputStatus, SessionStatus, ToolKind, TurnOutcome } from "./gen/remoteagent/v1/event_pb";
import { errorText, join, Remote, type CommandKind, type MachineView, type SessionView } from "./remote";
import { forgetAccount } from "./storage";

export function App() {
  const [remote, setRemote] = useState<Remote | null | undefined>(undefined);
  useEffect(() => {
    Remote.load().then(
      (r) => setRemote(r ?? null),
      () => setRemote(null),
    );
  }, []);
  useEffect(() => {
    if (!remote) return;
    remote.start();
    return () => remote.stop();
  }, [remote]);
  if (remote === undefined) return <div className="center muted">加载中…</div>;
  if (remote === null) return <Setup onDone={setRemote} />;
  return <Main remote={remote} />;
}

function Setup({ onDone }: { onDone: (r: Remote) => void }) {
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submit = async () => {
    setBusy(true);
    setError("");
    try {
      await join(text);
      setText("");
      const r = await Remote.load();
      if (r) onDone(r);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="setup">
      <h1>remote-agent</h1>
      <p className="muted">
        在电脑上运行 <code>remote-agent account secret</code>，把输出的账户密钥粘贴到这里。密钥只保存在这个浏览器里，不会发给 server。
      </p>
      <textarea
        value={text}
        onChange={(e) => setText(e.target.value)}
        placeholder="ra1.…"
        rows={4}
        autoComplete="off"
        spellCheck={false}
        aria-label="账户密钥"
      />
      {error && <p className="error">{error}</p>}
      <button className="primary" disabled={busy || !text.trim()} onClick={submit}>
        {busy ? "连接中…" : "连接"}
      </button>
    </div>
  );
}

function useRemote(remote: Remote) {
  useSyncExternalStore(remote.subscribe, remote.snapshot);
}

function Main({ remote }: { remote: Remote }) {
  useRemote(remote);
  const [selected, setSelected] = useState<string | null>(() => new URLSearchParams(location.hash.slice(1)).get("s"));
  useEffect(() => {
    const onHash = () => setSelected(new URLSearchParams(location.hash.slice(1)).get("s"));
    addEventListener("hashchange", onHash);
    return () => removeEventListener("hashchange", onHash);
  }, []);
  const select = (id: string | null) => {
    location.hash = id ? "s=" + id : "";
    setSelected(id);
  };
  const session = selected ? remote.sessions.get(selected) : undefined;
  return (
    <div className={"main" + (session ? " has-session" : "")}>
      <aside className="list">
        <SessionList remote={remote} selected={selected} onSelect={select} />
      </aside>
      <section className="detail">
        {session ? <SessionPage key={session.id} remote={remote} session={session} onBack={() => select(null)} /> : <div className="center muted">选择一个会话</div>}
      </section>
    </div>
  );
}

// --- session list ---

function statusOf(s: SessionView): { cls: string; text: string } {
  const st = s.info?.status ?? SessionStatus.UNSPECIFIED;
  if ((s.info?.pendingApprovals ?? 0) > 0 || (s.info?.pendingQuestions ?? 0) > 0) return { cls: "wait", text: "等你处理" };
  switch (st) {
    case SessionStatus.RUNNING:
      return { cls: "run", text: "运行中" };
    case SessionStatus.IDLE:
      return { cls: "idle", text: "空闲" };
    case SessionStatus.BLOCKED:
      return { cls: "wait", text: "终端里有弹窗：" + (s.info?.statusDetail || "") };
    case SessionStatus.STARTING:
      return { cls: "run", text: "启动中" };
    case SessionStatus.EXITED:
      return { cls: "off", text: "已结束" };
  }
  return { cls: "off", text: "未知" };
}

function titleOf(s: SessionView): string {
  const cwd = s.info?.cwd ?? "";
  return cwd.split("/").filter(Boolean).pop() || s.id.slice(0, 8);
}

function machineName(m?: MachineView): string {
  return m?.meta?.hostname || (m ? m.id.slice(0, 8) : "未知电脑");
}

function SessionList({ remote, selected, onSelect }: { remote: Remote; selected: string | null; onSelect: (id: string) => void }) {
  const [showExited, setShowExited] = useState(false);
  const groups = useMemo(() => {
    const byMachine = new Map<string, SessionView[]>();
    for (const m of remote.machines.values()) byMachine.set(m.id, []);
    for (const s of remote.sessions.values()) {
      if (!showExited && s.info?.status === SessionStatus.EXITED) continue;
      if (!byMachine.has(s.machineId)) byMachine.set(s.machineId, []);
      byMachine.get(s.machineId)!.push(s);
    }
    for (const list of byMachine.values()) list.sort((a, b) => Number(b.updatedMs - a.updatedMs));
    return [...byMachine.entries()];
    // remote mutates in place; its snapshot version drives recomputing.
  }, [remote.snapshot(), showExited]);
  return (
    <>
      <header className="list-head">
        <h2>会话</h2>
        <Conn remote={remote} />
      </header>
      {groups.length === 0 && <p className="muted pad">还没有电脑连上来。在电脑上运行 <code>remote-agent daemon</code>。</p>}
      {groups.map(([mid, list]) => {
        const m = remote.machines.get(mid);
        return (
          <div key={mid} className="machine">
            <div className="machine-name">
              <span className={"dot " + (m?.online ? "idle" : "off")} />
              {machineName(m)}
              {!m?.online && <span className="faint"> · 离线</span>}
            </div>
            {list.length === 0 && <p className="faint pad small">没有会话</p>}
            {list.map((s) => {
              const st = statusOf(s);
              return (
                <button key={s.id} className={"sess " + st.cls + (s.id === selected ? " sel" : "")} onClick={() => onSelect(s.id)}>
                  <span className={"dot " + st.cls} />
                  <span className="sess-main">
                    <b>{titleOf(s)}</b>
                    <span className="pv">{st.text}</span>
                    <span className="mc">
                      {s.info?.model || "codex"} · {s.id.slice(0, 8)}
                    </span>
                  </span>
                </button>
              );
            })}
          </div>
        );
      })}
      <label className="toggle faint small">
        <input type="checkbox" checked={showExited} onChange={(e) => setShowExited(e.target.checked)} /> 显示已结束的会话
      </label>
      <button
        className="link faint small"
        onClick={async () => {
          if (confirm("在这个浏览器里忘掉账户密钥？之后需要重新粘贴。")) {
            await forgetAccount();
            location.reload();
          }
        }}
      >
        退出并删除本机密钥
      </button>
    </>
  );
}

function Conn({ remote }: { remote: Remote }) {
  const text = { connecting: "连接中", online: "已连接", offline: "未连接" }[remote.conn];
  return (
    <span className={"conn " + remote.conn} title={remote.error}>
      {text}
    </span>
  );
}

// --- session page ---

interface Outgoing {
  requestId: string;
  text: string;
  status: "sending" | "queued" | "failed";
  error?: string;
}

function useNow(ms: number) {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), ms);
    return () => clearInterval(t);
  }, [ms]);
  return now;
}

function SessionPage({ remote, session, onBack }: { remote: Remote; session: SessionView; onBack: () => void }) {
  useRemote(remote);
  const events = remote.events(session.id);
  const [outgoing, setOutgoing] = useState<Outgoing[]>([]);
  const [toast, setToast] = useState("");
  const scroller = useRef<HTMLDivElement>(null);
  const stick = useRef(true);

  const derived = useMemo(() => derive(events), [events, events.length]);
  const machine = remote.machines.get(session.machineId);
  const st = statusOf(session);
  const exited = session.info?.status === SessionStatus.EXITED;

  // Drop local copies once the daemon reports on them.
  useEffect(() => {
    setOutgoing((list) => list.filter((o) => !derived.inputs.has(o.requestId) || o.status === "failed"));
  }, [derived]);

  useEffect(() => {
    const el = scroller.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  });

  const run = async (label: string, cmd: CommandKind) => {
    try {
      return await remote.command(session.id, cmd);
    } catch (e) {
      setToast(label + "失败：" + errorText(e));
      return undefined;
    }
  };

  const send = async (text: string) => {
    const requestId = crypto.randomUUID().replace(/-/g, "");
    setOutgoing((l) => [...l, { requestId, text, status: "sending" }]);
    try {
      await remote.command(session.id, { case: "send", value: { sessionId: session.id, text, requestId } });
      setOutgoing((l) => l.map((o) => (o.requestId === requestId ? { ...o, status: "queued" } : o)));
      return true;
    } catch (e) {
      setOutgoing((l) => l.map((o) => (o.requestId === requestId ? { ...o, status: "failed", error: errorText(e) } : o)));
      return false;
    }
  };

  const usage = derived.usage;
  const ctxPct = usage && usage.contextWindow > 0n ? Number((usage.lastTotalTokens * 100n) / usage.contextWindow) : undefined;

  return (
    <div className="session">
      <header className="navbar">
        <button className="back" onClick={onBack} aria-label="返回">
          ‹
        </button>
        <div className="title">
          <b>{titleOf(session)}</b>
          <span>
            <span className={"dot " + st.cls} /> {st.text} · {machineName(machine)}
            {session.info?.model ? " · " + session.info.model : ""}
            {ctxPct !== undefined ? ` · 上下文 ${ctxPct}%` : ""}
          </span>
        </div>
        {!exited && (
          <button
            className="link small"
            onClick={() => {
              if (confirm("结束这个会话？Codex 进程会被关闭。")) void run("结束会话", { case: "stopSession", value: { sessionId: session.id } });
            }}
          >
            结束
          </button>
        )}
      </header>
      {!machine?.online && <div className="banner">这台电脑现在不在线，只能查看历史。</div>}
      <div
        className="timeline"
        ref={scroller}
        onScroll={(e) => {
          const el = e.currentTarget;
          stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
        }}
      >
        {!remote.loaded(session.id) && events.length === 0 && <p className="center muted">加载中…</p>}
        {events.map((e) => (
          <EventRow key={String(e.seq)} e={e} derived={derived} />
        ))}
        {outgoing.map((o) => (
          <div key={o.requestId} className="me pending">
            <div>{o.text}</div>
            <small className={o.status === "failed" ? "error" : ""}>{o.status === "sending" ? "发送中…" : o.status === "queued" ? "已送达电脑" : "发送失败：" + o.error}</small>
          </div>
        ))}
      </div>
      <div className="dock">
        {toast && (
          <div className="toast" onClick={() => setToast("")}>
            {toast}
          </div>
        )}
        {[...derived.approvals.values()].map((a) => (
          <ApprovalCard key={a.approvalId} a={a} run={run} sessionId={session.id} />
        ))}
        {[...derived.questions.values()].map((q) => (
          <QuestionCard key={q.questionId} q={q} run={run} sessionId={session.id} />
        ))}
        {!exited && <Composer running={derived.running} onSend={send} onInterrupt={() => void run("中断", { case: "interrupt", value: { sessionId: session.id } })} />}
      </div>
    </div>
  );
}

interface Derived {
  approvals: Map<string, ApprovalRequested>;
  resolved: Map<string, { decision: ApprovalDecision; message: string }>;
  questions: Map<string, QuestionAsked>;
  answered: Map<string, string[]>;
  inputs: Map<string, InputStatus>;
  running: boolean;
  usage?: TokenUsage;
}

/** Pending approvals and questions, input states and the running turn. */
function derive(events: Event[]): Derived {
  const d: Derived = { approvals: new Map(), resolved: new Map(), questions: new Map(), answered: new Map(), inputs: new Map(), running: false };
  for (const e of events) {
    const k = e.kind;
    switch (k.case) {
      case "approvalRequested":
        d.approvals.set(k.value.approvalId, k.value);
        break;
      case "approvalResolved":
        d.approvals.delete(k.value.approvalId);
        d.resolved.set(k.value.approvalId, { decision: k.value.decision, message: k.value.message });
        break;
      case "questionAsked":
        if (!k.value.async) d.questions.set(k.value.questionId, k.value);
        break;
      case "questionAnswered":
        d.questions.delete(k.value.questionId);
        d.answered.set(
          k.value.questionId,
          k.value.answers.flatMap((a) => a.answers),
        );
        break;
      case "turnStarted":
        d.running = true;
        break;
      case "turnEnded":
        d.running = false;
        d.questions.clear();
        break;
      case "inputState":
        d.inputs.set(k.value.requestId, k.value.status);
        break;
      case "tokenUsage":
        d.usage = k.value;
        break;
      case "sessionState":
        if (k.value.status === SessionStatus.EXITED) {
          d.running = false;
          d.approvals.clear();
          d.questions.clear();
        }
        break;
    }
  }
  return d;
}

function time(e: Event): string {
  if (!e.time) return "";
  return new Date(timestampMs(e.time)).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

function EventRow({ e, derived }: { e: Event; derived: Derived }) {
  const k = e.kind;
  switch (k.case) {
    case "userMessage":
      return (
        <div className="me">
          <div>{k.value.text}</div>
          <small>{time(e)}</small>
        </div>
      );
    case "agentMessage":
      return <div className={"ai" + (k.value.phase === "commentary" ? " commentary" : "")}>{k.value.text}</div>;
    case "reasoning":
      return <div className="think">{k.value.text}</div>;
    case "toolCall": {
      const t = k.value;
      const ok = t.status === "completed" && (t.exitCode === undefined || t.exitCode === 0);
      const icon = { [ToolKind.COMMAND]: "$", [ToolKind.FILE_CHANGE]: "✎", [ToolKind.MCP]: "⚙", [ToolKind.WEB_SEARCH]: "⌕" }[t.kind as number] ?? "•";
      return (
        <details className="tool">
          <summary>
            <span className="ic">{icon}</span>
            <code>{t.title || t.itemType}</code>
            <span className={ok ? "ok" : "bad"}>{ok ? "✓" : t.exitCode !== undefined ? `退出码 ${t.exitCode}` : t.status}</span>
          </summary>
          {t.input && <pre>{t.input}</pre>}
          {t.output && (
            <pre>
              {t.output}
              {t.outputTruncated ? "\n…（已截断）" : ""}
            </pre>
          )}
        </details>
      );
    }
    case "turnStarted":
      return <div className="sep">{time(e)} 开始</div>;
    case "turnEnded":
      return <div className="sep">{k.value.outcome === TurnOutcome.ABORTED ? "已中断" : "本轮结束"}</div>;
    case "approvalRequested": {
      const r = derived.resolved.get(k.value.approvalId);
      return (
        <div className="note">
          请求权限：<code>{k.value.command || k.value.toolName}</code>
          {r ? " · " + decisionText(r.decision) : " · 等待审批"}
        </div>
      );
    }
    case "questionAsked": {
      const a = derived.answered.get(k.value.questionId);
      return (
        <div className="note">
          提问：{k.value.questions.map((q) => q.text).join(" / ")}
          {a ? " · 回答：" + a.join("、") : ""}
        </div>
      );
    }
    case "inputState":
      if (k.value.status === InputStatus.UNCONFIRMED) return <div className="note warn">有一条消息已送到终端，但没能确认 Codex 收到，没有重发。</div>;
      return null;
    case "sessionState":
      if (k.value.status === SessionStatus.EXITED) return <div className="sep">会话已结束{k.value.detail ? `（${k.value.detail}）` : ""}</div>;
      if (k.value.status === SessionStatus.BLOCKED) return <div className="note warn">终端里有弹窗需要处理：{k.value.detail}</div>;
      return null;
    default:
      return null;
  }
}

function decisionText(d: ApprovalDecision): string {
  switch (d) {
    case ApprovalDecision.ALLOW:
      return "已允许";
    case ApprovalDecision.DENY:
      return "已拒绝";
    case ApprovalDecision.TIMEOUT:
      return "超时，已拒绝";
    case ApprovalDecision.CANCELED:
      return "已取消";
  }
  return "";
}

type Run = (label: string, cmd: CommandKind) => Promise<unknown>;

function ApprovalCard({ a, run, sessionId }: { a: ApprovalRequested; run: Run; sessionId: string }) {
  const now = useNow(1000);
  const [busy, setBusy] = useState(false);
  const [reason, setReason] = useState("");
  const deadline = a.deadline ? timestampMs(a.deadline) : 0;
  const left = Math.max(0, Math.floor((deadline - now) / 1000));
  const expired = deadline > 0 && left === 0;
  const resolve = async (allow: boolean) => {
    setBusy(true);
    await run(allow ? "允许" : "拒绝", { case: "resolveApproval", value: { sessionId, approvalId: a.approvalId, allow, message: allow ? "" : reason } });
    setBusy(false);
  };
  return (
    <div className="appr">
      <div className="h">
        <b>请求权限 · {a.toolName}</b>
        <span className={expired ? "error" : "muted"}>{expired ? "已超时" : `剩余 ${Math.floor(left / 60)}:${String(left % 60).padStart(2, "0")}`}</span>
      </div>
      {a.description && <p>{a.description}</p>}
      {a.command ? <pre>{a.command}</pre> : a.toolInputJson && <pre>{a.toolInputJson}</pre>}
      <input placeholder="拒绝理由（可选）" value={reason} onChange={(e) => setReason(e.target.value)} />
      <div className="row">
        <button className="danger" disabled={busy || expired} onClick={() => resolve(false)}>
          拒绝
        </button>
        <button className="primary" disabled={busy || expired} onClick={() => resolve(true)}>
          允许
        </button>
      </div>
    </div>
  );
}

function QuestionCard({ q, run, sessionId }: { q: QuestionAsked; run: Run; sessionId: string }) {
  const [busy, setBusy] = useState(false);
  const first = q.questions[0];
  if (!first) return null;
  return (
    <div className="appr question">
      <div className="h">
        <b>{first.header || "Codex 在提问"}</b>
        {q.questions.length > 1 && <span className="muted">共 {q.questions.length} 题，先回答第 1 题</span>}
      </div>
      <p>{first.text}</p>
      <div className="options">
        {first.options.map((o, i) => (
          <button
            key={i}
            disabled={busy}
            onClick={async () => {
              setBusy(true);
              await run("回答", { case: "answerQuestion", value: { sessionId, questionId: q.questionId, option: i + 1 } });
              setBusy(false);
            }}
          >
            <b>
              {i + 1}. {o.label}
            </b>
            {o.description && <span>{o.description}</span>}
          </button>
        ))}
      </div>
    </div>
  );
}

function Composer({ running, onSend, onInterrupt }: { running: boolean; onSend: (text: string) => Promise<boolean>; onInterrupt: () => void }) {
  const [text, setText] = useState("");
  const submit = async () => {
    const t = text.trim();
    if (!t) return;
    // Slash commands typed as text are sent to the model, and menus they
    // open are not visible here.
    if (t.startsWith("/") && !confirm("以 / 开头的内容会作为普通消息发给模型，而不是执行斜杠命令。仍然发送？")) return;
    setText("");
    if (!(await onSend(t))) setText(t);
  };
  return (
    <div className="composer">
      <textarea
        value={text}
        rows={1}
        placeholder="给 Codex 发消息"
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
            e.preventDefault();
            void submit();
          }
        }}
      />
      {running && (
        <button className="danger" onClick={onInterrupt} title="中断当前这一轮">
          中断
        </button>
      )}
      <button className="primary" disabled={!text.trim()} onClick={submit}>
        发送
      </button>
    </div>
  );
}
