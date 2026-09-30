import { useCallback, useEffect, useLayoutEffect, useRef, useState, type RefObject } from "react";
import type { Event } from "../gen/remoteagent/v1/event_pb";
import { ApprovalDecision, InputStatus, SessionStatus, type Agent } from "../gen/remoteagent/v1/event_pb";
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
  inputFailure,
  machineName,
  modeName,
  phaseOf,
  projectKey,
  projectName,
  shortPath,
  shortenPath,
  shortenPaths,
  stateOf,
  timeline,
  titleOf,
  type Derived,
  type Item,
  type Outgoing,
} from "../model";
import { errorText, type Remote, type SessionView } from "../remote";
import { ModeSheet, SessionSheet } from "../sheets";
import { ActionSheet, Alert, Banner, ConnBar, Dot, NavBar, Skeleton, useRemote } from "../ui";
import { useDerived } from "./actions";
import { elsewhereLabel, originLabel, platformLabel, type OriginContext } from "../devices";
import { DeviceKind } from "../gen/remoteagent/v1/relay_pb";
import { PlanChoice } from "../gen/remoteagent/v1/local_pb";
import { ComposeStatus, Composer } from "./Composer";
import { ApprovalPage, Dock, PlanPage, QuestionPage } from "./Dock";
import { QueuePage } from "./Queue";
import { TerminalView } from "./Terminal";
import { ActiveTool, ToolGroup, ToolPage, ToolRow, toolTitle, type Short } from "./Tools";

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
  const ended = st.kind === "ended";
  const inactive = ended || st.kind === "offline";

  const [rootRef, width] = useWidth<HTMLDivElement>();
  // The dock may take 45% of the chat column.
  const [colRef, colHeight] = useHeight();
  const room = colHeight > 0 ? colHeight * 0.45 : Infinity;
  const canSplit = wide && width >= 840;
  const [tabPick, setTab] = useState<Tab>(() => (remote.termFirst.delete(session.id) ? (wide ? "split" : "term") : "chat"));
  let tab: Tab = tabPick === "split" && !canSplit ? "chat" : tabPick;
  if (inactive && tab !== "chat") tab = "chat";
  const [sheet, setSheet] = useState<"" | "mode" | "model">("");
  const [menu, setMenu] = useState(false);
  const [confirmEnd, setConfirmEnd] = useState(false);
  const [page, setPage] = useState<"" | "plan" | "question" | "queue" | { approval: string } | { tool: bigint | undefined }>("");
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

  const run = async (label: string, fn: () => Promise<unknown>) => {
    try {
      await fn();
      return true;
    } catch (e) {
      setToast(label + "失败：" + errorText(e));
      return false;
    }
  };
  const interrupt = () => void run("中断", () => remote.command(sid, { case: "interrupt", value: { sessionId: sid } }));
  const toTerminal = () => setTab(canSplit ? "split" : "term");
  const agentLabel = agentName(agent);
  const nameTitle = (
    <>
      <span className="tname">{title}</span>
      <span className="agent-id">{agentLabel}</span>
    </>
  );

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
          toTerminal();
        }}
      />
    );
  if (typeof page === "object" && "approval" in page) {
    const a = d.approvals.get(page.approval);
    if (a) return <ApprovalPage remote={remote} sid={sid} a={a} agent={agent} title={title} onBack={() => setPage("")} />;
  }
  if (page === "queue") return <QueuePage remote={remote} session={session} d={d} title={`${title} · ${agentLabel}`} onBack={() => setPage("")} />;
  if (typeof page === "object" && "tool" in page)
    return (
      <ToolPage
        remote={remote}
        d={d}
        seq={page.tool}
        agent={agentLabel}
        sub={`${title} · ${mname}`}
        cwd={shortPath(session.info?.cwd ?? "")}
        onBack={() => setPage("")}
        onTerminal={() => {
          setPage("");
          toTerminal();
        }}
        onInterrupt={interrupt}
      />
    );

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
        <span className="agent-id">{agentLabel}</span>
        {seg}
      </div>
    </header>
  ) : (
    <NavBar onBack={onBack} title={nameTitle} sub={sub} right={more} className={tab === "term" ? "dark" : ""} />
  );

  const chat = (
    <div className="ccol" ref={colRef}>
      <ChatBody
        remote={remote}
        session={session}
        events={events}
        d={d}
        agent={agent}
        machine={mname}
        onRestore={restore}
        onTool={(seq) => setPage({ tool: seq })}
        onTerminal={inactive ? undefined : toTerminal}
      />
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
          onTerminal={toTerminal}
        />
      )}
      {inactive ? (
        <EndCard remote={remote} session={session} ended={ended} machineOnline={machine?.online ?? false} agent={agent} mname={mname} lastSeen={machine?.lastSeenMs ?? 0n} />
      ) : (
        <>
          <ComposeStatus remote={remote} session={session} d={d} onQueue={() => setPage("queue")} onTool={() => setPage({ tool: d.tools[0]?.seq ?? d.unsettled[0]?.seq })} onInterrupt={interrupt} />
          <Composer
            key={composerKey}
            ref={composer}
            remote={remote}
            session={session}
            d={d}
            wide={wide}
            slim={typing && !wide}
            onFocus={(v) => setTyping(v)}
            onSend={(text) => remote.send(sid, text)}
            draft={remote.drafts.get(sid) ?? ""}
            onDraft={(text) => remote.drafts.set(sid, text)}
            onMode={() => setSheet("mode")}
            onModel={() => setSheet("model")}
          />
        </>
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
            ...(!inactive && d.running ? [{ label: "中断本轮", onClick: interrupt }] : []),
            ...(!ended && machine?.online ? [{ label: "结束会话…", red: true, onClick: () => setConfirmEnd(true) }] : []),
          ]}
          onClose={() => setMenu(false)}
        />
      )}
      {confirmEnd && (
        <Alert
          title="结束这个会话？"
          text={`${mname} 上的 ${agentLabel} 会被关掉，聊天记录保留，之后可以恢复。`}
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
  onTool,
  onTerminal,
}: {
  remote: Remote;
  session: SessionView;
  events: Event[];
  d: Derived;
  agent: Agent;
  machine: string;
  onRestore: (text: string) => void;
  onTool: (seq: bigint) => void;
  onTerminal?: () => void;
}) {
  const sid = session.id;
  const scroller = useRef<HTMLDivElement>(null);
  // Back from another view, the chat is where it was left.
  const saved = remote.scroll.get(sid);
  const stick = useRef(saved?.stick ?? true);
  const [unseen, setUnseen] = useState(0);
  const seen = useRef(0);
  const hist = remote.history(sid);
  const items = timeline(events, d);
  const outbox = remote.outbox(sid);
  const prompt = remote.prompts.get(sid);
  const cwd = session.info?.cwd ?? "";
  // Models by the names the chips use: Sonnet, not sonnet or its full ID.
  const nameModel = (id: string) => remote.modelName(session.machineId, agent, id);
  const home = remote.machines.get(session.machineId)?.meta?.home ?? "";
  const short: Short = { text: (title) => shortenPaths(title, cwd, home), path: (path) => shortenPath(path, cwd, home) };

  // Text of a message this page knows, by request ID; v2 daemons say
  // what they accepted from any client.
  const textOf = new Map([...[...d.accepted].map(([id, a]) => [id, a.text] as const), ...outbox.map((o) => [o.sendId, o.text] as const)]);
  const who: Who = {
    ctx: { me: remote.account.deviceId, devices: remote.devices, machine, mine: new Set(outbox.map((o) => o.requestId)) },
    // Name this client on its own messages only when there are others.
    many: [...remote.devices.values()].filter((x) => x.kind === DeviceKind.CLIENT).length > 1,
    self: platformLabel(remote.devices.get(remote.account.deviceId)?.meta),
  };
  const promptId = prompt ? [...d.inputs.keys()].find((id) => !textOf.has(id)) : undefined;
  if (promptId && prompt) textOf.set(promptId, prompt);

  const count = items.length + outbox.length;
  useLayoutEffect(() => {
    const el = scroller.current;
    if (el && saved && !saved.stick) el.scrollTop = saved.top;
  }, []); // eslint-disable-line react-hooks/exhaustive-deps
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
          remote.scroll.set(sid, { top: el.scrollTop, stick: stick.current });
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
          <Row key={it.key} it={it} d={d} short={short} machine={machine} agent={agent} textOf={textOf} onRestore={onRestore} nameModel={nameModel} remote={remote} onTool={onTool} onTerminal={onTerminal} who={who} sid={sid} />
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

function Row({
  it,
  d,
  short,
  machine,
  agent,
  textOf,
  onRestore,
  nameModel,
  remote,
  onTool,
  onTerminal,
  who,
  sid,
}: {
  it: Item;
  d: Derived;
  short: Short;
  machine: string;
  agent: Agent;
  textOf: Map<string, string>;
  onRestore: (text: string) => void;
  nameModel: (id: string) => string;
  remote: Remote;
  onTool: (seq: bigint) => void;
  onTerminal?: () => void;
  who: Who;
  sid: string;
}) {
  if (it.kind === "tools") return <ToolGroup calls={it.calls} short={short} />;
  if (it.kind === "running" || it.kind === "unsettled")
    return <ActiveTool tool={{ ...it.tool, title: toolTitle(it.tool, short) }} d={d} unsettled={it.kind === "unsettled"} aborted={it.kind === "unsettled" && it.aborted} remote={remote} onDetail={() => onTool(it.tool.seq)} onTerminal={onTerminal} />;
  if (it.kind === "turn") return <div className="sep">{duration(it.ms)}</div>;
  const e = it.event;
  const k = e.kind;
  switch (k.case) {
    case "userMessage": {
      const label = originLabel(e.origin, who.ctx);
      const from = label === undefined ? (d.typedLocally.has(e.seq) ? `${machine} 终端` : "") : label === "本客户端" ? (who.many ? `本客户端 · ${who.self}` : "") : label;
      return (
        <div className="me">
          <div>{k.value.text}</div>
          {from && <small>{from}</small>}
        </div>
      );
    }
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
      return <div className="sysline">已中断{otherThanMe(e.origin, who) ? ` · ${originLabel(e.origin, who.ctx) || "另一客户端"}` : ""}</div>;
    case "planAnswered": {
      const choices: Record<number, string> = { [PlanChoice.IMPLEMENT]: "批准执行计划", [PlanChoice.KEEP_PLANNING]: "继续规划", [PlanChoice.CLEAR_AND_IMPLEMENT]: "清空上下文后执行计划" };
      const choice = choices[k.value.choice] ?? "回答了计划";
      return (
        <div className="sysline">
          {choice}
          {otherThanMe(e.origin, who) && ` · ${elsewhereLabel(e.origin, who.ctx)}`}
        </div>
      );
    }
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
          <span className="muted">
            {decisionText(r.decision)}
            {otherThanMe(d.by.get(k.value.approvalId), who) && r.decision !== ApprovalDecision.TERMINAL && ` · ${elsewhereLabel(d.by.get(k.value.approvalId), who.ctx)}`}
          </span>
        </div>
      );
    }
    case "questionAsked": {
      const a = d.answered.get(k.value.questionId);
      const by = d.by.get(k.value.questionId);
      return a ? (
        <div className="sysline">
          回答了 <b>{a.join("、")}</b>
          {otherThanMe(by, who) && ` · ${elsewhereLabel(by, who.ctx)}`}
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
      if (k.value.status === InputStatus.FAILED || k.value.status === InputStatus.NEEDS_TERMINAL) {
        const text = textOf.get(k.value.requestId) ?? "";
        const why = inputFailure(k.value.reason, agent);
        const images = d.accepted.get(k.value.requestId)?.imageIds ?? [];
        const mine = remote.outbox(sid).some((o) => o.sendId === k.value.requestId);
        if (k.value.status === InputStatus.NEEDS_TERMINAL)
          return (
            <div className="me warn">
              <div>{text || "一条消息"}</div>
              <small>
                没能发送：{why}。终端里可能留着这条消息的一部分，不会自动重发
                {onTerminal && <button onClick={onTerminal}>去终端</button>}
              </small>
            </div>
          );
        return (
          <div className="me fail">
            <div>{text || "一条消息"}</div>
            <small>
              没能发送：{why}
              {mine && text && <button onClick={() => remote.send(sid, text, images)}>重试</button>}
              {text && <button onClick={() => onRestore(text)}>放回输入框</button>}
            </small>
          </div>
        );
      }
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
            切换到 <b>{nameModel(c.model)}</b>
            {c.effort && ` · ${c.effort}`}
          </div>
        );
      return (
        <div className="sysline">
          {otherThanMe(e.origin, who) ? `${originLabel(e.origin, who.ctx) || "另一客户端"}将模式切到 ` : "模式切换到 "}
          <b>{modeName(c.mode)}</b>
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
                {what}切到了 <b>{k.value.model ? to.split(k.value.model).join(nameModel(k.value.model)) : to}</b>
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

/** How the chat names who did what (devices.ts). */
interface Who {
  ctx: OriginContext;
  many: boolean; // the account has other clients
  self: string; // this client's kind, e.g. Chrome
}

/** Whether an origin is someone other than this client. */
function otherThanMe(o: Event["origin"], who: Who): boolean {
  const l = originLabel(o, who.ctx);
  return l !== undefined && l !== "本客户端" && l !== "";
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

