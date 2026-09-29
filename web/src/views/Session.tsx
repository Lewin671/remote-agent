import { useEffect, useMemo, useRef, useState } from "react";
import type { ApprovalRequested, Event, QuestionAsked } from "../gen/remoteagent/v1/event_pb";
import { ApprovalDecision, InputStatus, SessionStatus, ToolKind, TurnOutcome } from "../gen/remoteagent/v1/event_pb";
import { decisionText, derive, hhmm, machineName, optionLabel, projectKey, projectName, shortPath, statusOf, titleOf, type Derived } from "../model";
import { errorText, type CommandKind, type Remote, type SessionView } from "../remote";
import { Chips, Dot, Rich, useNow, useRemote } from "../ui";

interface Outgoing {
  // The Send request ID. The daemon accepts a request ID once, so a retry
  // with it can never deliver the message twice.
  requestId: string;
  text: string;
  status: "sending" | "sent" | "failed";
  error?: string;
}

type Run = (label: string, cmd: CommandKind) => Promise<unknown>;

export function SessionPage({ remote, session, onBack, wide }: { remote: Remote; session: SessionView; onBack: () => void; wide: boolean }) {
  useRemote(remote);
  const events = remote.events(session.id);
  const [outgoing, setOutgoing] = useState<Outgoing[]>([]);
  const [toast, setToast] = useState("");
  const [menu, setMenu] = useState(false);
  const scroller = useRef<HTMLDivElement>(null);
  const stick = useRef(true);

  const derived = useMemo(() => derive(events), [events, events.length]);
  const machine = remote.machines.get(session.machineId);
  const st = statusOf(session);
  const ended = session.info?.status === SessionStatus.EXITED;
  const blocked = session.info?.status === SessionStatus.BLOCKED ? session.info.statusDetail : "";

  // A message is shown locally until Codex records it or gives up on it.
  useEffect(() => {
    setOutgoing((list) =>
      list.filter((o) => {
        const s = derived.inputs.get(o.requestId);
        return o.status === "failed" || s === undefined || s === InputStatus.QUEUED;
      }),
    );
  }, [derived]);

  useEffect(() => {
    const el = scroller.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  });

  const run: Run = async (label, cmd) => {
    try {
      return await remote.command(session.id, cmd);
    } catch (e) {
      setToast(label + "失败：" + errorText(e));
      return undefined;
    }
  };

  const deliver = async (requestId: string, text: string) => {
    setOutgoing((l) => l.map((o) => (o.requestId === requestId ? { ...o, status: "sending", error: undefined } : o)));
    try {
      await remote.command(session.id, { case: "send", value: { sessionId: session.id, text, requestId } });
      setOutgoing((l) => l.map((o) => (o.requestId === requestId ? { ...o, status: "sent" } : o)));
    } catch (e) {
      setOutgoing((l) => l.map((o) => (o.requestId === requestId ? { ...o, status: "failed", error: errorText(e) } : o)));
    }
  };

  const send = (text: string) => {
    const requestId = crypto.randomUUID().replace(/-/g, "");
    setOutgoing((l) => [...l, { requestId, text, status: "sending" }]);
    void deliver(requestId, text);
  };

  // Messages waiting in the daemon's queue, including ones sent elsewhere.
  const queued = [...derived.inputs.entries()].filter(([, s]) => s === InputStatus.QUEUED).map(([id]) => id);
  const localText = new Map(outgoing.map((o) => [o.requestId, o.text]));
  const pending = outgoing.filter((o) => o.status !== "failed" && !queued.includes(o.requestId));
  const failed = outgoing.filter((o) => o.status === "failed");

  return (
    <div className="session">
      <header className={wide ? "dh" : "navbar"}>
        {!wide && (
          <button className="back" onClick={onBack} aria-label="返回">
            ‹
          </button>
        )}
        {wide ? (
          <>
            <Dot tone={st.tone} />
            <b className="ellipsis">{titleOf(session)}</b>
            <span className="muted small nowrap">
              {projectName(projectKey(session))} · {machineName(machine)}
            </span>
            <span className="dh-chips">
              <Chips s={session} />
            </span>
          </>
        ) : (
          <span className="title">
            <b>{titleOf(session)}</b>
            <span>
              <Dot tone={st.tone} />
              {machineName(machine)}
            </span>
          </span>
        )}
        <span className="more-wrap">
          <button className="more" onClick={() => setMenu((m) => !m)} aria-label="更多">
            ⋯
          </button>
          {menu && (
            <div className="menu" onClick={() => setMenu(false)}>
              <div className="menu-info">
                {shortPath(session.info?.cwd ?? "")}
                <br />
                {session.id}
              </div>
              {!ended && (
                <button
                  className="danger"
                  onClick={() => {
                    if (confirm("结束这个会话？Codex 进程会被关闭。")) void run("结束会话", { case: "stopSession", value: { sessionId: session.id } });
                  }}
                >
                  结束会话
                </button>
              )}
            </div>
          )}
        </span>
      </header>

      <div
        className="body chat"
        ref={scroller}
        onScroll={(e) => {
          const el = e.currentTarget;
          stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
        }}
      >
        <div className="sep">
          {events[0] ? hhmm(Number(events[0].time?.seconds ?? 0n) * 1000) + " · " : ""}
          {shortPath(session.info?.cwd ?? "")}
        </div>
        {!remote.loaded(session.id) && events.length === 0 && <p className="sysline">加载中…</p>}
        {events.map((e) => (
          <EventRow key={String(e.seq)} e={e} d={derived} cwd={session.info?.cwd ?? ""} />
        ))}
        {pending.map((o) => (
          <div key={o.requestId} className="me pending">
            <div>{o.text}</div>
            <small>{o.status === "sending" ? "发送中…" : "已送达电脑"}</small>
          </div>
        ))}
        {failed.map((o) => (
          <div key={o.requestId} className="me failed">
            <div>{o.text}</div>
            <small>
              没能确认送达：{o.error}
              <button className="link" onClick={() => void deliver(o.requestId, o.text)}>
                重试
              </button>
              <button className="link muted" onClick={() => setOutgoing((l) => l.filter((x) => x.requestId !== o.requestId))}>
                忽略
              </button>
            </small>
          </div>
        ))}
        {[...derived.approvals.values()].map((a) => (
          <ApprovalCard key={a.approvalId} a={a} run={run} sessionId={session.id} />
        ))}
        {[...derived.questions.values()].map((q) => (
          <QuestionCard key={q.questionId} q={q} run={run} sessionId={session.id} />
        ))}
        {blocked && (
          <div className="banner">
            ⚠ 终端里有弹窗：{blocked}
            <b>去终端处理</b>
          </div>
        )}
        {!machine?.online && <div className="banner">⚠ 这台电脑不在线，只能查看历史</div>}
      </div>

      {toast && (
        <div className="toast" onClick={() => setToast("")}>
          {toast}
        </div>
      )}
      {queued.length > 0 && (
        <div className="queued">
          排队{" "}
          {queued.map((id) => (
            <span key={id}>
              {localText.get(id) ?? "一条消息"}
              <button
                className="x"
                aria-label="取消这条消息"
                onClick={() => void run("取消", { case: "cancelInput", value: { sessionId: session.id, requestId: id } })}
              >
                ✕
              </button>
            </span>
          ))}{" "}
          {blocked ? "已暂停" : derived.questions.size > 0 ? "答完再发" : derived.running ? "本轮结束后发" : ""}
        </div>
      )}
      {!wide && (
        <div className="chips">
          <Chips s={session} />
        </div>
      )}
      {ended ? (
        <div className="comp ended">会话已结束</div>
      ) : (
        <Composer
          running={derived.running}
          wide={wide}
          onSend={send}
          onInterrupt={() => void run("中断", { case: "interrupt", value: { sessionId: session.id } })}
        />
      )}
    </div>
  );
}

function EventRow({ e, d, cwd }: { e: Event; d: Derived; cwd: string }) {
  const k = e.kind;
  switch (k.case) {
    case "userMessage":
      return (
        <div className="me">
          <div>{k.value.text}</div>
        </div>
      );
    case "agentMessage":
      return (
        <div className={"ai" + (k.value.phase === "commentary" ? " commentary" : "")}>
          <Rich text={k.value.text} />
        </div>
      );
    case "reasoning":
      return (
        <details className="think">
          <summary>▸ 思考</summary>
          <div>{k.value.text}</div>
        </details>
      );
    case "toolCall": {
      const t = k.value;
      const ok = t.status === "completed" && (t.exitCode === undefined || t.exitCode === 0);
      const secs = Number(t.durationMs) / 1000;
      return (
        <details className="tool">
          <summary>
            <span className={ok ? "ok" : "bad"}>{ok ? "✓" : "✕"}</span>
            <code>{t.kind === ToolKind.FILE_CHANGE ? "编辑 " + relativePath(t.title, cwd) : t.title || t.itemType}</code>
            <span className="faint">{!ok && t.exitCode !== undefined ? `退出码 ${t.exitCode}` : secs >= 1 ? `${Math.round(secs)}s` : ""}</span>
          </summary>
          {t.input && t.input !== t.title && <pre>{t.input}</pre>}
          {t.output && (
            <pre>
              {t.output}
              {t.outputTruncated ? "\n…（已截断）" : ""}
            </pre>
          )}
        </details>
      );
    }
    case "turnEnded":
      return k.value.outcome === TurnOutcome.ABORTED ? <div className="sysline">已中断</div> : null;
    case "approvalRequested": {
      const r = d.resolved.get(k.value.approvalId);
      if (!r) return null; // the pending card is shown at the bottom
      const ok = r.decision === ApprovalDecision.ALLOW;
      return (
        <div className="tool">
          <div className="summary">
            <span className={ok ? "ok" : "bad"}>{ok ? "✓" : "✕"}</span>
            <code>{k.value.command || k.value.toolName}</code>
            <span className="faint">{decisionText(r.decision)}</span>
          </div>
        </div>
      );
    }
    case "questionAsked": {
      const a = d.answered.get(k.value.questionId);
      return a ? (
        <div className="sysline">
          回答了 <b>{a.join("、")}</b>
        </div>
      ) : null;
    }
    case "inputState":
      if (k.value.status === InputStatus.CANCELED) return <div className="sysline">取消了一条排队的消息</div>;
      return k.value.status === InputStatus.UNCONFIRMED ? <div className="sysline warn">有一条消息已送到终端，但没能确认 Codex 收到，没有重发</div> : null;
    case "sessionState":
      return k.value.status === SessionStatus.EXITED ? <div className="sysline">会话已结束{k.value.detail ? `（${k.value.detail}）` : ""}</div> : null;
    default:
      return null;
  }
}

function ApprovalCard({ a, run, sessionId }: { a: ApprovalRequested; run: Run; sessionId: string }) {
  const now = useNow(1000);
  const [busy, setBusy] = useState(false);
  const [reasonOpen, setReasonOpen] = useState(false);
  const [reason, setReason] = useState("");
  const deadline = a.deadline ? Number(a.deadline.seconds) * 1000 : 0;
  const expired = deadline > 0 && now >= deadline;
  const resolve = async (allow: boolean) => {
    setBusy(true);
    await run(allow ? "允许" : "拒绝", { case: "resolveApproval", value: { sessionId, approvalId: a.approvalId, allow, message: allow ? "" : reason } });
    setBusy(false);
  };
  return (
    <div className="appr">
      <div className="h">
        <b>{a.command ? "请求执行" : "请求权限 · " + a.toolName}</b>
        <span className="muted">{expired ? "已超时" : deadline ? `${hhmm(deadline)} 后拒绝` : ""}</span>
      </div>
      {a.description && <p className="desc">{a.description}</p>}
      <pre>{a.command || a.toolInputJson}</pre>
      {reasonOpen && <input autoFocus placeholder="拒绝的理由，会告诉 Codex" value={reason} onChange={(e) => setReason(e.target.value)} />}
      <div className="btns">
        <button className="btn no" disabled={busy || expired} onClick={() => (reasonOpen || !reason ? void resolve(false) : setReasonOpen(true))}>
          拒绝
        </button>
        <button className="btn pri" disabled={busy || expired} onClick={() => void resolve(true)}>
          允许
        </button>
      </div>
      {!reasonOpen && (
        <div className="foot">
          想告诉 Codex 为什么？<b onClick={() => setReasonOpen(true)}>附理由拒绝 ›</b>
        </div>
      )}
    </div>
  );
}

function QuestionCard({ q, run, sessionId }: { q: QuestionAsked; run: Run; sessionId: string }) {
  const [busy, setBusy] = useState(false);
  // Which question to answer. The daemon only enters the option while the
  // terminal shows this very question, so a stale choice is refused rather
  // than answering another question.
  const [current, setCurrent] = useState(0);
  const multi = q.questions.length > 1;
  const index = Math.min(current, q.questions.length - 1);
  const item = q.questions[index];
  if (!item) return null;
  return (
    <div className="appr ask">
      <div className="h">
        <b>Codex 在问你</b>
        <span className="muted">
          {multi ? (
            <>
              <button className="link" disabled={index === 0} onClick={() => setCurrent(index - 1)} aria-label="上一题">
                ‹
              </button>
              {index + 1}/{q.questions.length}
              <button className="link" disabled={index === q.questions.length - 1} onClick={() => setCurrent(index + 1)} aria-label="下一题">
                ›
              </button>
            </>
          ) : (
            item.header
          )}
        </span>
      </div>
      <div className="q">{item.text}</div>
      {item.options.map((o, i) => {
        const { text, recommended } = optionLabel(o.label);
        return (
          <button
            key={i}
            className={"ans" + (recommended ? " rec" : "")}
            disabled={busy}
            onClick={async () => {
              setBusy(true);
              const res = await run("回答", { case: "answerQuestion", value: { sessionId, questionId: q.questionId, option: i + 1, questionIndex: index } });
              if (res && index < q.questions.length - 1) setCurrent(index + 1);
              setBusy(false);
            }}
          >
            {text}
            {recommended && "（推荐）"}
            {o.description && <small>{o.description}</small>}
          </button>
        );
      })}
      <div className="foot">{multi ? "终端上显示的必须是这一题，否则不会作答。" : "都不合适？去终端里补充。"}</div>
    </div>
  );
}

function Composer({ running, wide, onSend, onInterrupt }: { running: boolean; wide: boolean; onSend: (text: string) => void; onInterrupt: () => void }) {
  const [text, setText] = useState("");
  const submit = () => {
    const t = text.trim();
    if (!t) return;
    // Slash commands typed as text are sent to the model, and menus they
    // open are not visible here.
    if (t.startsWith("/") && !confirm("以 / 开头的内容会作为普通消息发给模型，而不是执行斜杠命令。仍然发送？")) return;
    // A message that fails stays in the timeline with a retry button;
    // putting it back here would invite sending it a second time.
    setText("");
    onSend(t);
  };
  return (
    <div className="comp">
      <textarea
        className="in"
        value={text}
        rows={1}
        placeholder={wide ? "消息 · ⌘↵ 发送" : "消息"}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
            e.preventDefault();
            submit();
          }
        }}
      />
      {running && !text.trim() ? (
        <button className="stop" onClick={onInterrupt} aria-label="中断" title="中断当前这一轮">
          ■
        </button>
      ) : (
        <button className="go" disabled={!text.trim()} onClick={submit} aria-label="发送">
          ↑
        </button>
      )}
    </div>
  );
}

/** path relative to the session directory; macOS reports /tmp as /private/tmp. */
function relativePath(path: string, cwd: string): string {
  const norm = (p: string) => p.replace(/^\/private\//, "/");
  const p = norm(path);
  const base = norm(cwd).replace(/\/$/, "") + "/";
  return p.startsWith(base) ? p.slice(base.length) : shortPath(path);
}
