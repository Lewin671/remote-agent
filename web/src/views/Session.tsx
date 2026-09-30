import { useCallback, useEffect, useLayoutEffect, useRef, useState, type RefObject } from "react";
import type { Event, ToolCall } from "../gen/remoteagent/v1/event_pb";
import { ApprovalDecision, InputStatus, SessionStatus, ToolKind, type Agent } from "../gen/remoteagent/v1/event_pb";
import { Markdown } from "../markdown";
import {
  agentName,
  agentOf,
  ago,
  decisionText,
  dockItems,
  duration,
  eventTime,
  exitReason,
  hhmm,
  machineName,
  modeName,
  phaseOf,
  projectKey,
  projectName,
  queueReason,
  shortPath,
  shortenPath,
  shortenPaths,
  stateOf,
  timeline,
  titleOf,
  toolsSummary,
  type Derived,
  type Item,
  type Outgoing,
} from "../model";
import { errorText, type Remote, type SessionView } from "../remote";
import { ModeSheet, SessionSheet } from "../sheets";
import { ActionSheet, Alert, Banner, Chips, ConnBar, Dot, NavBar, Skeleton, useNow, useRemote } from "../ui";
import { useDerived } from "./actions";
import { ApprovalPage, Dock, PlanPage, QuestionPage } from "./Dock";
import { TerminalView } from "./Terminal";

type Tab = "chat" | "term" | "split";

/** Tracks an element's width, for choices that depend on the space the
 * main area has rather than the window. */
function useWidth<T extends HTMLElement>(): [RefObject<T | null>, number] {
  const ref = useRef<T>(null);
  const [w, setW] = useState(0);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setW(el.clientWidth));
    ro.observe(el);
    setW(el.clientWidth);
    return () => ro.disconnect();
  }, []);
  return [ref, w];
}

/** Tracks an element's height through a callback ref, for an element that
 * comes and goes (the chat column when switching to the terminal). */
function useHeight(): [(el: HTMLElement | null) => void, number] {
  const [h, setH] = useState(0);
  const ro = useRef<ResizeObserver>(undefined);
  const ref = useCallback((el: HTMLElement | null) => {
    ro.current?.disconnect();
    if (!el) return;
    const o = new ResizeObserver(() => setH(el.clientHeight));
    o.observe(el);
    ro.current = o;
    setH(el.clientHeight);
  }, []);
  return [ref, h];
}

export function SessionPage({ remote, session, onBack, wide }: { remote: Remote; session: SessionView; onBack: () => void; wide: boolean }) {
  useRemote(remote);
  const sid = session.id;
  const events = remote.events(sid);
  const d = useDerived(remote, sid);
  const machine = remote.machines.get(session.machineId);
  const st = stateOf(session, machine);
  const agent = agentOf(session);
  const title = titleOf(session);
  const project = projectName(projectKey(session));
  const mname = machineName(machine);
  const online = remote.conn === "online";
  const ended = st.kind === "ended";
  const inactive = ended || st.kind === "offline";

  const [rootRef, width] = useWidth<HTMLDivElement>();
  // The dock may take 45% of the chat column.
  const [colRef, colHeight] = useHeight();
  const room = colHeight > 0 ? colHeight * 0.45 : Infinity;
  const canSplit = wide && width >= 840;
  const [tabPick, setTab] = useState<Tab>("chat");
  let tab: Tab = tabPick === "split" && !canSplit ? "chat" : tabPick;
  if (inactive && tab !== "chat") tab = "chat";
  const [sheet, setSheet] = useState<"" | "mode" | "model">("");
  const [menu, setMenu] = useState(false);
  const [confirmEnd, setConfirmEnd] = useState(false);
  const [page, setPage] = useState<"" | "plan" | "question" | { approval: string }>("");
  const [typing, setTyping] = useState(false);
  const [toast, setToast] = useState("");
  const composer = useRef<HTMLTextAreaElement>(null);
  // Remounting the input shows a draft put back from the chat.
  const [composerKey, setComposerKey] = useState(0);
  const restore = (text: string) => {
    remote.drafts.set(sid, text);
    setComposerKey((k) => k + 1);
    setTimeout(() => composer.current?.focus());
  };

  const items = dockItems(session, d, machine);
  // Display names for the model chip; the list is cached per machine.
  useEffect(() => {
    if (machine?.online && !inactive) remote.listModels(session.machineId, agent).catch(() => undefined);
  }, [remote, session.machineId, agent, machine?.online, inactive]);
  const outbox = remote.outbox(sid);

  const run = async (label: string, fn: () => Promise<unknown>) => {
    try {
      await fn();
      return true;
    } catch (e) {
      setToast(label + "失败：" + errorText(e));
      return false;
    }
  };

  // Detail pages replace the whole session view.
  if (page === "plan" && d.plan && session.info?.planOpen)
    return <PlanPage remote={remote} sid={sid} plan={d.plan} title={title} onBack={() => setPage("")} />;
  const question = [...d.questions.values()][0];
  if (page === "question" && question)
    return (
      <QuestionPage
        remote={remote}
        sid={sid}
        q={question}
        agent={agent}
        title={title}
        onBack={() => setPage("")}
        onTerminal={() => {
          setPage("");
          setTab(canSplit ? "split" : "term");
        }}
      />
    );
  if (typeof page === "object") {
    const a = d.approvals.get(page.approval);
    if (a) return <ApprovalPage remote={remote} sid={sid} a={a} agent={agent} title={title} onBack={() => setPage("")} />;
  }

  const tabs: [Tab, string][] = [
    ["chat", "聊天"],
    ["term", "终端"],
    ...(canSplit ? ([["split", "并排"]] as [Tab, string][]) : []),
  ];
  const seg = (
    <div className={"seg" + (tab !== "chat" && !wide ? " dark" : "")} role="tablist">
      {tabs.map(([t, name]) => (
        <button key={t} role="tab" aria-selected={t === tab} className={t === tab ? "on" : ""} disabled={t !== "chat" && inactive} onClick={() => setTab(t)}>
          {name}
          {t === "term" && st.kind === "popup" && <span className="n">●</span>}
        </button>
      ))}
    </div>
  );
  const chips = <Chips remote={remote} s={session} onMode={ended ? undefined : () => setSheet("mode")} onModel={ended ? undefined : () => setSheet("model")} disabled={!online || inactive} />;
  const sub = (
    <>
      <Dot tone={st.tone} />
      {st.text} · {project} · {mname}
    </>
  );
  const more = (
    <button className="more" onClick={() => setMenu(true)} aria-label="更多">
      ⋯
    </button>
  );

  const header = wide ? (
    <header className="dh">
      <div className="r1">
        <Dot tone={st.tone} />
        <b>{title}</b>
        <span className="muted">
          {st.text} · {project} · {mname}
        </span>
        <button className="mr" onClick={() => setMenu(true)} aria-label="更多">
          ⋯
        </button>
      </div>
      <div className="r2">
        {chips}
        {seg}
      </div>
    </header>
  ) : (
    <NavBar onBack={onBack} title={title} sub={sub} right={more} className={tab === "term" ? "dark" : ""} />
  );

  const chat = (
    <div className="ccol" ref={colRef}>
      <ChatBody remote={remote} session={session} events={events} d={d} agent={agent} machine={mname} onRestore={restore} />
      {toast && (
        <div className="toast" onClick={() => setToast("")}>
          {toast}
        </div>
      )}
      {!inactive && (
        <Dock
          remote={remote}
          sid={sid}
          items={items}
          agent={agent}
          collapsed={typing}
          room={room}
          onExpand={() => {
            composer.current?.blur();
            setTyping(false);
          }}
          onDetail={(id) => setPage({ approval: id })}
          onPlan={() => setPage("plan")}
          onQuestion={() => setPage("question")}
          onTerminal={() => setTab(canSplit ? "split" : "term")}
        />
      )}
      {!inactive && <Queue remote={remote} session={session} d={d} outbox={outbox} />}
      {!inactive && !wide && !["approval", "question", "popup"].includes(items[0]?.kind ?? "") && <div className="chips">{chips}</div>}
      {inactive ? (
        <EndCard remote={remote} session={session} ended={ended} machineOnline={machine?.online ?? false} agent={agent} mname={mname} lastSeen={machine?.lastSeenMs ?? 0n} />
      ) : (
        <Composer
          key={composerKey}
          ref={composer}
          wide={wide}
          online={online}
          running={st.kind === "running"}
          asking={d.questions.size > 0}
          onFocus={(v) => setTyping(v)}
          onSend={(text) => remote.send(sid, text)}
          draft={remote.drafts.get(sid) ?? ""}
          onDraft={(text) => remote.drafts.set(sid, text)}
          onInterrupt={() => void run("中断", () => remote.command(sid, { case: "interrupt", value: { sessionId: sid } }))}
        />
      )}
    </div>
  );

  return (
    <div className={"session" + (tab === "term" && !wide ? " termmode" : "")} ref={rootRef}>
      {header}
      <ConnBar remote={remote} />
      {!wide && <div className="segrow">{seg}</div>}
      <div className={"panes p-" + tab}>
        {tab !== "term" && chat}
        {tab !== "chat" && (
          <div className="tcol">
            <TerminalView remote={remote} session={session} compact={tab === "split"} wide={wide} running={!ended} />
          </div>
        )}
      </div>
      {menu && (
        <ActionSheet
          info={
            <>
              <span className="mono">{shortPath(session.info?.cwd ?? "")}</span> · {mname}
              <br />
              会话 <span className="mono">{sid}</span>
            </>
          }
          actions={[
            ...(!inactive && d.running ? [{ label: "中断本轮", onClick: () => void run("中断", () => remote.command(sid, { case: "interrupt", value: { sessionId: sid } })) }] : []),
            ...(!ended && machine?.online ? [{ label: "结束会话…", red: true, onClick: () => setConfirmEnd(true) }] : []),
          ]}
          onClose={() => setMenu(false)}
        />
      )}
      {confirmEnd && (
        <Alert
          title="结束这个会话？"
          text={`${mname} 上的 ${agentName(agent)} 会被关掉，聊天记录保留，之后可以恢复。`}
          ok="结束"
          danger
          onCancel={() => setConfirmEnd(false)}
          onOk={() => {
            setConfirmEnd(false);
            void run("结束会话", () => remote.command(sid, { case: "stopSession", value: { sessionId: sid } }));
          }}
        />
      )}
      {sheet === "mode" && <ModeSheet remote={remote} s={session} onClose={() => setSheet("")} />}
      {sheet === "model" && <SessionSheet remote={remote} s={session} running={d.running} onClose={() => setSheet("")} />}
    </div>
  );
}

/** The chat: history, what runs now, and messages on their way. Follows
 * the bottom unless scrolled up, then offers a way back. */
function ChatBody({
  remote,
  session,
  events,
  d,
  agent,
  machine,
  onRestore,
}: {
  remote: Remote;
  session: SessionView;
  events: Event[];
  d: Derived;
  agent: Agent;
  machine: string;
  onRestore: (text: string) => void;
}) {
  const sid = session.id;
  const scroller = useRef<HTMLDivElement>(null);
  const stick = useRef(true);
  const [unseen, setUnseen] = useState(0);
  const seen = useRef(0);
  const hist = remote.history(sid);
  const items = timeline(events, d);
  const outbox = remote.outbox(sid);
  const prompt = remote.prompts.get(sid);
  const cwd = session.info?.cwd ?? "";
  const home = remote.machines.get(session.machineId)?.meta?.home ?? "";
  const short: Short = { text: (title) => shortenPaths(title, cwd, home), path: (path) => shortenPath(path, cwd, home) };

  // Text of a message this page knows, by request ID.
  const textOf = new Map(outbox.map((o) => [o.sendId, o.text]));
  const promptId = prompt ? [...d.inputs.keys()].find((id) => !textOf.has(id)) : undefined;
  if (promptId && prompt) textOf.set(promptId, prompt);

  const count = items.length + outbox.length + d.tools.length;
  useLayoutEffect(() => {
    const el = scroller.current;
    if (!el) return;
    if (stick.current) {
      el.scrollTop = el.scrollHeight;
      seen.current = count;
      setUnseen(0);
    } else if (count > seen.current) setUnseen(count - seen.current);
  }, [count, hist.loaded]);

  const pending = outbox.filter((o) => ["sending", "delivered", "failed"].includes(phaseOf(o, d.inputs.get(o.sendId))));
  const started = session.info?.createdAt ? Number(session.info.createdAt.seconds) * 1000 : events[0] ? eventTime(events[0]) : 0;
  // The first message of a session started here, until the CLI takes it.
  const promptState = promptId ? d.inputs.get(promptId) : undefined;
  const showPrompt = prompt && !events.some((e) => e.kind.case === "userMessage") && promptState !== InputStatus.CANCELED && promptState !== InputStatus.UNCONFIRMED;

  return (
    <div className="chatwrap">
      <div
        className="body chat"
        ref={scroller}
        onScroll={(e) => {
          const el = e.currentTarget;
          stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 60;
          if (stick.current) {
            seen.current = count;
            setUnseen(0);
          }
        }}
      >
        <div className="sep">
          {started ? hhmm(started) + " · " : ""}
          {shortPath(cwd)}
        </div>
        {!hist.loaded && events.length === 0 && !hist.error && <Skeleton />}
        {items.map((it) => (
          <Row key={it.key} it={it} d={d} short={short} machine={machine} agent={agent} textOf={textOf} onRestore={onRestore} />
        ))}
        {hist.broken && <Banner kind="warn">有一条记录读不出来，历史停在这里，后面的内容和状态可能不全</Banner>}
        {hist.error && (
          <Banner kind="err" action="重试" onAction={() => remote.retryEvents(sid)}>
            后面的记录没读到
          </Banner>
        )}
        {showPrompt && (
          <div className="me">
            <div>{prompt}</div>
            <small>{promptState === InputStatus.QUEUED ? `已送达电脑，等 ${agentName(agent)} 就绪后发出` : "已送达电脑"}</small>
          </div>
        )}
        {session.info?.status === SessionStatus.STARTING && (
          <div className="sysline">
            正在 {machine} 上启动 {agentName(agent)}…
          </div>
        )}
        {d.running && d.tools.map((t, i) => <RunningTool key={t.callId || i} title={toolTitle(t, short)} since={t.since} />)}
        {pending.map((o) => (
          <Pending key={o.sendId} remote={remote} sid={sid} o={o} phase={phaseOf(o, d.inputs.get(o.sendId))} />
        ))}
      </div>
      {unseen > 0 && (
        <button
          className="newmsg"
          onClick={() => {
            const el = scroller.current;
            if (el) el.scrollTo({ top: el.scrollHeight, behavior: "smooth" });
            stick.current = true;
            seen.current = count;
            setUnseen(0);
          }}
        >
          ↓ {unseen} 条新消息
        </button>
      )}
    </div>
  );
}

function Pending({ remote, sid, o, phase }: { remote: Remote; sid: string; o: Outgoing; phase: string }) {
  if (phase === "failed")
    return (
      <div className="me fail">
        <div>{o.text}</div>
        <small>
          没能确认送达：{o.error}
          <button onClick={() => remote.retrySend(sid, o.sendId)}>重试</button>
          <button onClick={() => remote.dismissSend(sid, o.sendId)}>移除此提示</button>
        </small>
      </div>
    );
  return (
    <div className={"me" + (phase === "sending" ? " pending" : "")}>
      <div>{o.text}</div>
      <small>{phase === "sending" ? "◌ 发送中…" : "已送达电脑"}</small>
    </div>
  );
}

/** Shortens the absolute paths in a tool title (see shortenPaths), or a
 * whole path such as a file edit's title, spaces and all. */
type Short = { text: (title: string) => string; path: (path: string) => string };
const asIs: Short = { text: (x) => x, path: (x) => x };

/** A tool row's title, the same while it runs and once done: a file
 * edit reads "编辑 <path>". */
function toolTitle(t: Pick<ToolCall, "kind" | "title" | "itemType">, short: Short): string {
  // A file edit's title is one path, which may hold spaces.
  return t.kind === ToolKind.FILE_CHANGE ? "编辑 " + short.path(t.title) : short.text(t.title || t.itemType);
}

function ToolRow({ t, short, decision }: { t: ToolCall; short: Short; decision?: ApprovalDecision }) {
  const [open, setOpen] = useState(false);
  const ok = t.status !== "failed" && (t.exitCode === undefined || t.exitCode === 0);
  const secs = Number(t.durationMs) / 1000;
  const out = t.output.replace(/\n+$/, "");
  const lines = out ? out.split("\n") : [];
  // The details keep the original: the tool's input, or else the title as
  // the CLI wrote it when paths were shortened for the row.
  const raw = t.input && t.input !== t.title ? t.input : toolTitle(t, asIs) !== toolTitle(t, short) ? t.title || t.itemType : "";
  return (
    <>
      <button className="tool" onClick={() => setOpen((o) => !o)} aria-expanded={open}>
        <span className={ok ? "ok" : "x"}>{ok ? "✓" : "✕"}</span>
        <code>{toolTitle(t, short)}</code>
        {!ok && t.exitCode !== undefined ? (
          // A failed call keeps its exit code, with the approval beside it.
          <span className="exit">
            退出码 {t.exitCode}
            {decision !== undefined && <span className="muted"> · {decisionText(decision)}</span>}
          </span>
        ) : decision !== undefined ? (
          <span className="muted">{decisionText(decision)}</span>
        ) : secs >= 1 ? (
          <span className="muted">{Math.round(secs)}s</span>
        ) : null}
      </button>
      {open && raw && <div className="toolout">{raw}</div>}
      {(open || !ok) && lines.length > 0 && (
        <div className="toolout">
          {open ? out : lines[0]}
          {open && t.outputTruncated && <span className="more">{"\n"}…（已截断）</span>}
          {!open && lines.length > 1 && (
            <span className="more" onClick={() => setOpen(true)}>
              {"\n"}… 还有 {lines.length - 1} 行，展开
            </span>
          )}
        </div>
      )}
    </>
  );
}

function ToolGroup({ calls, short }: { calls: ToolCall[]; short: Short }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button className="tool tgrp" onClick={() => setOpen((o) => !o)} aria-expanded={open}>
        <span className="ok">✓</span>
        <b>{toolsSummary(calls)}</b>
        <span className="muted">{open ? "▾" : "▸"}</span>
      </button>
      {open && (
        <div className="grpbody">
          {calls.map((t, i) => (
            <ToolRow key={i} t={t} short={short} />
          ))}
        </div>
      )}
    </>
  );
}

function Row({
  it,
  d,
  short,
  machine,
  agent,
  textOf,
  onRestore,
}: {
  it: Item;
  d: Derived;
  short: Short;
  machine: string;
  agent: Agent;
  textOf: Map<string, string>;
  onRestore: (text: string) => void;
}) {
  if (it.kind === "tools") return <ToolGroup calls={it.calls} short={short} />;
  if (it.kind === "turn") return <div className="sep">{duration(it.ms)}</div>;
  const e = it.event;
  const k = e.kind;
  switch (k.case) {
    case "userMessage":
      return (
        <div className="me">
          <div>{k.value.text}</div>
          {d.typedLocally.has(e.seq) && <small>{machine} 终端</small>}
        </div>
      );
    case "agentMessage":
      return (
        <div className={"ai" + (k.value.phase === "commentary" ? " commentary" : "")}>
          <Markdown text={k.value.text} />
        </div>
      );
    case "reasoning":
      return (
        <details className="think">
          <summary>▸ 思考</summary>
          <div className="thinktext">{k.value.text}</div>
        </details>
      );
    case "toolCall":
      return <ToolRow t={k.value} short={short} decision={it.decision} />;
    case "turnEnded":
      return <div className="sysline">已中断</div>;
    case "approvalRequested": {
      const r = d.resolved.get(k.value.approvalId);
      if (!r) return null;
      const ok = r.decision === ApprovalDecision.ALLOW;
      // Canceled, or answered in the CLI's own prompt: what happened shows
      // in the tool call that follows.
      const canceled = r.decision === ApprovalDecision.CANCELED || r.decision === ApprovalDecision.TERMINAL;
      return (
        <div className="record">
          <span className={canceled ? "muted" : ok ? "ok" : "x"}>{canceled ? "–" : ok ? "✓" : "✕"}</span>
          <code>{k.value.command || k.value.toolName}</code>
          <span className="muted">{decisionText(r.decision)}</span>
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
    case "inputState": {
      if (d.unsent.has(k.value.requestId)) {
        // The session ended before writing it. Not sent again by itself:
        // the text can go back into the input, to send after resuming.
        const text = textOf.get(k.value.requestId);
        return text ? (
          <div className="me warn unsent">
            <div>{text}</div>
            <small>
              会话结束，没有发出
              <button onClick={() => onRestore(text)}>放回输入框</button>
            </small>
          </div>
        ) : (
          <div className="sysline warn">有一条消息会话结束，没有发出</div>
        );
      }
      if (k.value.status === InputStatus.CANCELED) return <div className="sysline">取消了一条排队的消息</div>;
      const text = textOf.get(k.value.requestId);
      const note = `已送到终端，但没能确认 ${agentName(agent)} 收到，不会自动重发`;
      return text ? (
        <div className="me warn">
          <div>{text}</div>
          <small>{note}</small>
        </div>
      ) : (
        <div className="sysline warn">有一条消息{note}</div>
      );
    }
    case "settingsChanged": {
      const c = k.value;
      if (c.compacted) {
        const x = d.compactions.get(e.seq);
        return (
          <div className="sysline">
            上下文已压缩{x?.before !== undefined && x.after !== undefined ? ` · ${x.before}% → ${x.after}%` : ""}
          </div>
        );
      }
      if (c.model)
        return (
          <div className="sysline">
            切换到 <b>{c.model}</b>
            {c.effort && ` · ${c.effort}`}
          </div>
        );
      return (
        <div className="sysline">
          模式切换到 <b>{modeName(c.mode)}</b>
        </div>
      );
    }
    case "turnContext":
      return (
        <div className="sysline">
          {machine} 终端把
          {(d.terminalChanges.get(e.seq) ?? "").split("，").map((c, i) => {
            const [what, to] = c.split("切到了 ");
            return (
              <span key={i}>
                {i > 0 && "，"}
                {what}切到了 <b>{to}</b>
              </span>
            );
          })}
        </div>
      );
    case "planProposed":
      return (
        <details className="think">
          <summary>▸ 计划</summary>
          <div className="ai">
            <Markdown text={k.value.text} />
          </div>
        </details>
      );
    case "sessionState": {
      const t = eventTime(e);
      return <div className="sep">{t ? `${new Date(t).getMonth() + 1}月${new Date(t).getDate()}日 ${hhmm(t)} ` : ""}结束</div>;
    }
  }
  return null;
}

function RunningTool({ title, since }: { title: string; since: number }) {
  const now = useNow(1000);
  const secs = since ? Math.max(0, Math.floor((now - since) / 1000)) : 0;
  return (
    <div className="tool">
      <span className="sp">◌</span>
      <code>{title}</code>
      {secs > 0 && <span className="muted">{secs}s</span>}
    </div>
  );
}

/** Messages waiting in the daemon's queue, each cancellable, and why
 * they wait. */
function Queue({ remote, session, d, outbox }: { remote: Remote; session: SessionView; d: Derived; outbox: Outgoing[] }) {
  const [canceling, setCanceling] = useState<Set<string>>(new Set());
  const sid = session.id;
  const text = new Map(outbox.map((o) => [o.sendId, o.text]));
  const prompt = remote.prompts.get(sid);
  // Queued, and not left behind by a session that ended.
  const ids = [...d.inputs.entries()].filter(([id, s]) => s === InputStatus.QUEUED && !d.unsent.has(id)).map(([id]) => id);
  // The first message of a session started here shows in the chat.
  const shown = prompt ? ids.filter((id) => text.has(id)) : ids;
  if (shown.length === 0) return null;
  const reason = queueReason(session, d);
  const cancel = async (id: string) => {
    setCanceling((s) => new Set(s).add(id));
    try {
      await remote.command(sid, { case: "cancelInput", value: { sessionId: sid, requestId: id } });
    } catch {
      setCanceling((s) => {
        const n = new Set(s);
        n.delete(id);
        return n;
      });
    }
  };
  return (
    <div className="queued">
      {!reason.startsWith("认不出") && shown.length === 1 && <span className="lbl">排队</span>}
      {shown.map((id) => (
        <span key={id} className={"q" + (canceling.has(id) ? " canceling" : "")}>
          <em>{text.get(id) ?? "一条消息"}</em>
          <button aria-label="取消这条消息" disabled={canceling.has(id)} onClick={() => void cancel(id)}>
            ✕
          </button>
        </span>
      ))}
      {reason && <span className="lbl">{shown.length > 1 ? `${reason.replace(/发$/, "按顺序发")}` : reason}</span>}
    </div>
  );
}

function Composer({
  ref,
  wide,
  online,
  running,
  asking,
  onFocus,
  onSend,
  onInterrupt,
  draft,
  onDraft,
}: {
  ref: RefObject<HTMLTextAreaElement | null>;
  draft: string;
  onDraft: (text: string) => void;
  wide: boolean;
  online: boolean;
  running: boolean;
  asking: boolean;
  onFocus: (typing: boolean) => void;
  onSend: (text: string) => void;
  onInterrupt: () => void;
}) {
  // The draft is the session's, not this input's: it survives opening an
  // approval, plan or question page and coming back.
  const [text, setTextState] = useState(draft);
  const setText = (v: string) => {
    setTextState(v);
    onDraft(v);
  };
  const [slash, setSlash] = useState(false);
  const t = text.trim();
  // While the server is away, nothing is queued behind the user's back:
  // the text stays here until they send it themselves.
  const canSend = !!t && online;
  const submit = (force = false) => {
    if (!canSend) return;
    // Slash commands typed as text go to the model; menus they would open
    // are not visible here.
    if (t.startsWith("/") && !force) return setSlash(true);
    setText("");
    onSend(t);
  };
  return (
    <div className="comp">
      <textarea
        ref={ref}
        className={"in" + (t ? " typed" : "")}
        value={text}
        rows={1}
        placeholder={asking ? "排队发送，回答后继续" : wide ? "消息 · ⌘↵ 发送" : "消息"}
        onChange={(e) => setText(e.target.value)}
        onFocus={() => onFocus(true)}
        onBlur={() => onFocus(false)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
            e.preventDefault();
            submit();
          }
        }}
        aria-label="消息"
      />
      {running && !t ? (
        <button className="stop" onClick={onInterrupt} disabled={!online} aria-label="中断本轮" title="中断本轮">
          ■
        </button>
      ) : (
        <button className="go" disabled={!canSend} onMouseDown={(e) => e.preventDefault()} onClick={() => submit()} aria-label="发送">
          ↑
        </button>
      )}
      {slash && (
        <Alert
          title="作为普通消息发送？"
          text="以 / 开头的内容会作为普通消息发给模型，而不是执行斜杠命令。"
          ok="发送"
          onCancel={() => setSlash(false)}
          onOk={() => {
            setSlash(false);
            submit(true);
          }}
        />
      )}
    </div>
  );
}

/** The input's place when the session cannot take messages: ended, or the
 * computer is offline. */
function EndCard({
  remote,
  session,
  ended,
  machineOnline,
  agent,
  mname,
  lastSeen,
}: {
  remote: Remote;
  session: SessionView;
  ended: boolean;
  machineOnline: boolean;
  agent: Agent;
  mname: string;
  lastSeen: bigint;
}) {
  const [resuming, setResuming] = useState(false);
  const [error, setError] = useState("");
  const status = session.info?.status;
  useEffect(() => {
    if (status !== SessionStatus.EXITED) setResuming(false);
  }, [status]);
  if (!ended)
    return (
      <div className="endcard">
        <div className="h">
          <Dot tone="off" />
          {mname} 不在线
        </div>
        <div className="d">{lastSeen ? `${ago(lastSeen)}断开。` : ""}电脑上的会话可能还在跑，重新连上后这里会自动更新，现在只能看历史。</div>
      </div>
    );
  const resume = async () => {
    setResuming(true);
    setError("");
    try {
      await remote.command(session.id, { case: "resumeSession", value: { sessionId: session.id } });
    } catch (e) {
      setError(errorText(e));
      setResuming(false);
    }
  };
  if (resuming)
    return (
      <div className="endcard">
        <div className="h">
          <Dot tone="run" />
          正在恢复…
        </div>
        <div className="d">
          在 {mname} 上重新打开 {agentName(agent)}
        </div>
      </div>
    );
  if (error)
    return (
      <div className="endcard">
        <div className="h">
          <Dot tone="off" />
          恢复失败
        </div>
        <div className="d">{error}</div>
        <div className="btns">
          <button className="btn" onClick={() => void resume()} disabled={!machineOnline || remote.conn !== "online"}>
            再试一次
          </button>
        </div>
      </div>
    );
  return (
    <div className="endcard">
      <div className="h">
        <Dot tone="off" />
        会话已结束
      </div>
      {machineOnline ? (
        <>
          <div className="d">
            {exitReason(session.info?.statusDetail ?? "")}恢复会在 {mname} 上用同样的设置重新打开它，聊天记录都在。
          </div>
          <div className="btns">
            <button className="btn pri" onClick={() => void resume()} disabled={remote.conn !== "online"}>
              恢复会话
            </button>
          </div>
        </>
      ) : (
        <div className="d">{mname} 不在线，连上后才能恢复。</div>
      )}
    </div>
  );
}

