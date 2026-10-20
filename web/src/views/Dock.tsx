import { isSkeleton, skeletonMessage } from "../skeleton";
// What a session waits for, docked above the input: approvals, questions,
// plans and terminal popups (client-v7 停靠区). Only the first item is
// docked, as one line that opens in place; long content opens a full page,
// and the buttons stay in view.

import { useLayoutEffect, useRef, useState, type CSSProperties, type ReactNode } from "react";
import type { ApprovalRequested, PlanProposed, QuestionAsked } from "../gen/remoteagent/v1/event_pb";
import { PlanChoice } from "../gen/remoteagent/v1/local_pb";
import { approvalTruncated, approvalTruncatedMessage, approvalDisplay } from "../approval-display";
import { Markdown } from "../markdown";
import { Agent } from "../gen/remoteagent/v1/event_pb";
import { agentName, agentOf, askTitle, minutesLeft, optionLabel, type DockItem } from "../model";
import type { Remote } from "../remote";
import { Banner, Exp, NavBar, useNow } from "../ui";
import { deadlineOf, useAnswer, useApproval, usePlan } from "./actions";
import { Check, ChevronRight } from "../icons";

export function approvalTitle(a: ApprovalRequested): string {
  return a.command ? "请求执行" : "请求权限 · " + approvalDisplay(a.toolName);
}

export function approvalText(a: ApprovalRequested): string {
  return approvalDisplay(a.command || a.toolInputJson || a.toolName);
}

/** Lines a card's command shows before 还有 N 行 · 展开. */
export const CARD_LINES = 6;

/** A command on a card: its first CARD_LINES lines, whole lines only and
 * without a scroll of its own, then 还有 N 行 · 展开, which shows all of it
 * in place. N counts the lines as they wrap on screen. */
export function CardCommand({ text }: { text: string }) {
  const ref = useRef<HTMLSpanElement>(null);
  const [open, setOpen] = useState(false);
  const [lines, setLines] = useState(() => text.split("\n").length);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    // Laid out, the text's full height says how many lines it wraps to;
    // without layout (or before it) the text's own lines are the count.
    const measure = () => {
      const line = parseFloat(getComputedStyle(el).lineHeight);
      const shown = line > 0 && el.scrollHeight > 0 ? Math.round(el.scrollHeight / line) : 0;
      setLines(Math.max(shown, text.split("\n").length));
    };
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, [text]);
  const more = lines - CARD_LINES;
  return (
    <>
      {/* The lines are clipped inside the padding, so no part line shows. */}
      <pre className={"card-cmd" + (open ? " open" : "")}>
        <span ref={ref}>{text}</span>
      </pre>
      {(more > 0 || open) && (
        <button className="link cmd-more" aria-expanded={open} onClick={() => setOpen(!open)}>
          {open ? (
            <>
              收起 <Exp up />
            </>
          ) : (
            <>
              还有 {more} 行 · 展开 <Exp />
            </>
          )}
        </button>
      )}
    </>
  );
}

/** Approval content shared by the dock, the home card and the detail
 * page: the reason field replaces the buttons while it is open. */
export function ApprovalActions({
  remote,
  sid,
  a,
  onReasonOpen,
  shared,
}: {
  remote: Remote;
  sid: string;
  a: ApprovalRequested;
  agent: Agent;
  footless?: boolean;
  wideRow?: boolean;
  onReasonOpen?: (open: boolean) => void;
  // The dock's own answering state, so its one-line form can say
  // 等待确认 while the answer is on its way.
  shared?: ReturnType<typeof useApproval>;
}) {
  const now = useNow(1000);
  const own = useApproval(remote, sid, a);
  const ap = shared ?? own;
  const [reasonOpen, setReasonOpen] = useState(() => remote.demoFlag("deny"));
  const reason = remote.approvalReasons.get(a.approvalId) ?? "";
  const setReason = (text: string) => {
    if (text) remote.approvalReasons.set(a.approvalId, text);
    else remote.approvalReasons.delete(a.approvalId);
    remote.touch();
  };
  const deadline = deadlineOf(a);
  const expired = deadline > 0 && now >= deadline;
  const truncated = approvalTruncated(a);
  // An answer whose outcome is unknown holds: only a retry of it may go.
  const locked = !!ap.busy || expired || (!!ap.error && ap.unknown);
  const open = (v: boolean) => {
    setReasonOpen(v);
    onReasonOpen?.(v);
  };
  if (isSkeleton(a)) return <div className="approval-status muted">{skeletonMessage}</div>;
  const error = ap.error && <div className="approval-status red">没送到电脑 <button className="link" onClick={ap.retry}>重试</button></div>;
  if (ap.busy && !ap.error) return <div className="approval-status muted">正在提交…</div>;
  if (ap.error) return error;
  if (reasonOpen)
    return (
      <>
        <textarea
          className="field reason"
          autoFocus
          rows={1}
          placeholder="说明（可选）"
          value={reason}
          onChange={(e) => setReason(e.target.value)}
        />
        <div className="btns decide">
          <button className="btn" onClick={() => open(false)}>取消</button>
          <button className="btn no" disabled={locked} onClick={() => void ap.resolve(false, reason.trim())}>不允许</button>
        </div>
        {error}
      </>
    );
  const buttons = (
    <div className="btns decide">
      <button className="btn no" disabled={locked} onClick={() => open(true)}>
        不允许
      </button>
      <button className="btn pri" disabled={locked || truncated} onClick={() => void ap.resolve(true)}>
        {ap.busy.startsWith("allow") ? "允许中…" : "允许"}
      </button>
    </div>
  );
  const always = remote.machines.get(remote.sessions.get(sid)?.machineId ?? "")?.meta?.capabilities?.approvalAlways;
  return (
    <>
      {truncated && <div className="approval-status red">{approvalTruncatedMessage}</div>}
      {buttons}
      {always && <div className="foot"><button className="link" disabled={locked || truncated} onClick={() => void ap.resolve(true, "", true)}>允许，这类请求以后不再问</button></div>}
      {error}
    </>
  );
}

/** How a docked item draws itself: its one line, whether it is open, and
 * how many more items wait behind it. */
type Frame = { open: boolean; onToggle: () => void; more: number };

/** A waiting item: one line that opens in place into its content, which
 * scrolls, over its actions, which stay in view. Closed, the line is
 * type · summary · 处理; open, it is the only head: type, then what to know
 * about it (还有 N 项, time left), then 收起. `extra` sits before 收起 while
 * open, as the plan's 展开全文. */
function Pending({
  frame,
  label,
  brief,
  meta,
  extra,
  busy,
  body,
  actions,
  reasoning,
}: {
  frame: Frame;
  label: string;
  brief: string;
  meta?: ReactNode;
  extra?: ReactNode;
  busy?: boolean;
  body: ReactNode;
  actions: ReactNode;
  reasoning?: boolean;
}) {
  const { open, more } = frame;
  const info = open ? meta : [brief, more > 0 ? `还有 ${more} 项` : ""].filter(Boolean).join(" · ");
  const keep = (e: { preventDefault(): void }) => e.preventDefault();
  return (
    <div className={"dock pending" + (open ? " open" : "") + (busy ? " submitting" : "") + (reasoning ? " reasoning" : "")}>
      <div className="pending-head">
        <button className="pending-sum" aria-expanded={open} onMouseDown={keep} onClick={frame.onToggle}>
          <span className="pending-dot" />
          <span className="pending-label">{label}</span>
          <span className="pending-brief">{info}</span>
        </button>
        {open && extra}
        {/* The same toggle as the line; the line is the one read aloud. */}
        <button className="pending-action" tabIndex={-1} aria-hidden onMouseDown={keep} onClick={frame.onToggle}>
          {busy ? (
            "等待确认"
          ) : open ? (
            <>
              收起 <Exp up />
            </>
          ) : (
            <>
              处理 <Exp />
            </>
          )}
        </button>
      </div>
      {open && (
        <div className="pending-content">
          <div className="pending-scroll">{body}</div>
          <div className="pending-actions">{actions}</div>
        </div>
      )}
    </div>
  );
}

/** Parts of a head's meta, joined by 「 · 」. */
function metaOf(...parts: ReactNode[]): ReactNode {
  const shown = parts.filter((p) => p !== "" && p !== false && p != null);
  return shown.map((p, i) => (
    <span key={i}>
      {i > 0 && " · "}
      {p}
    </span>
  ));
}

function ApprovalDock({
  remote,
  sid,
  a,
  agent,
  frame,
  onReasonOpen,
  reasoning,
}: {
  remote: Remote;
  sid: string;
  a: ApprovalRequested;
  agent: Agent;
  frame: Frame;
  onReasonOpen: (open: boolean) => void;
  reasoning: boolean;
}) {
  const now = useNow(1000);
  const ap = useApproval(remote, sid, a);
  const deadline = deadlineOf(a);
  const left = deadline ? minutesLeft(deadline, now) : "";
  const title = approvalTitle(a);
  if (isSkeleton(a)) return <div className="dock muted">{skeletonMessage}</div>;
  return (
    <Pending
      frame={frame}
      label={title}
      // A permission request's arguments are no summary: its title says it.
      brief={a.command ? approvalDisplay(a.command).replace(/\s+/g, " ") : ""}
      busy={!!ap.busy}
      reasoning={reasoning}
      meta={metaOf(
        frame.more > 0 && `还有 ${frame.more} 项`,
        // The head says the time; what happens at its end is the approval card's to say.
        left,
      )}
      body={
        <>
          <pre className="cmd">{approvalText(a)}</pre>
          {a.description && <div className="desc">{approvalDisplay(a.description)}</div>}
        </>
      }
      actions={<ApprovalActions remote={remote} sid={sid} a={a} agent={agent} shared={ap} onReasonOpen={onReasonOpen} />}
    />
  );
}

/** A question's parts, for the dock and the question page alike. Its
 * answering state, ticks included, lives in the Remote, so both share it. */
function useQuestion(remote: Remote, sid: string, q: QuestionAsked, agent: Agent, onTerminal: () => void) {
  // Which question to answer. The daemon only enters the option while the
  // terminal shows this very question, so a stale choice is refused rather
  // than answering another question.
  const ans = useAnswer(remote, sid, q.questionId, isSkeleton(q));
  const ticked = ans.ticked;
  const setTicked = (f: number[] | ((t: number[]) => number[])) => ans.setTicked(typeof f === "function" ? f : () => f);
  if (isSkeleton(q)) return { label: askTitle(agent), text: skeletonMessage, busy: false, steps: null, progress: "", answers: null, actions: (_page: boolean) => null };
  const n = q.questions.length;
  const index = Math.min(ans.index, n - 1);
  const item = q.questions[index];
  if (!item) return undefined;
  const multi = item.multiSelect;
  const sent = ans.chosen.index === index ? ans.chosen.options : [];
  const pick = async (options: number[]) => {
    const ok = await ans.answer(index, options, multi);
    if (ok && index < n - 1) {
      ans.done();
      setTicked([]);
      ans.setIndex(index + 1);
    }
  };
  // A retry is the same pick: on success it moves on like the first try.
  const retry = () => {
    if (sent.length) void pick(sent);
  };
  // After a definite failure the ticks can change and be submitted again;
  // while the outcome is unknown they stay as sent, and only a retry of
  // that answer may follow.
  const frozen = !!ans.busy || (!!ans.error && ans.unknown);
  const tick = (o: number) => {
    if (ans.error) ans.clearError();
    setTicked((t) => (t.includes(o) ? t.filter((x) => x !== o) : [...t, o].sort((a, b) => a - b)));
  };
  const where = n > 1 ? `第 ${index + 1} 题，共 ${n} 题` : item.header;
  const progress = [where, multi ? "可多选" : ""].filter(Boolean).join(" · ");
  const answers = (
    <div className="answers">
      {item.options.map((o, i) => {
        const { text, recommended } = optionLabel(o.label);
        const on = multi ? (frozen ? sent : ticked).includes(i + 1) : sent.includes(i + 1);
        const status = on && sent.includes(i + 1) && (ans.busy ? "提交中…" : ans.error ? "err" : "");
        return (
          <button
            key={i}
            className={"ans" + (on ? " on" : "") + (multi ? " multi" : "")}
            role={multi ? "checkbox" : undefined}
            aria-checked={multi ? on : undefined}
            disabled={multi ? frozen : !!ans.busy}
            onClick={() => (multi ? tick(i + 1) : void pick([i + 1]))}
          >
            {multi ? <span className={"box" + (on ? " on" : "")}>{on && <Check />}</span> : on && <span className="ck"><Check /></span>}
            <div>
              {text}
              {status === "err" && !multi ? (
                <small className="red">没能提交，终端里的问题可能已经变了</small>
              ) : status && status !== "err" && !multi ? (
                <small>{status}</small>
              ) : (
                o.description && <small>{o.description}</small>
              )}
            </div>
            {recommended && <span className="tag">推荐</span>}
          </button>
        );
      })}
    </div>
  );
  const submit = multi && !(ans.error && ans.unknown) && (
    <div className="btns">
      <button className="btn pri" disabled={!!ans.busy || ticked.length === 0} onClick={() => void pick(ticked)}>
        {ans.busy ? "提交中…" : ticked.length ? `提交 ${ticked.length} 项` : "选好后提交"}
      </button>
    </div>
  );
  const failed = multi && ans.error && <div className="red small">{ans.unknown ? "不确定提交了没有，重试会发送同一个回答" : `没能提交：${ans.error}。可以改选后再提交`}</div>;
  const foot = (page: boolean) => (
    <div className="foot">
      {ans.error && (!multi || ans.unknown) ? (
        <>
          <span />
          <button className="link" onClick={retry}>
            重试
          </button>
        </>
      ) : (
        <>
          <span>{page && multi ? (ticked.length ? `已选 ${ticked.length} 项` : "") : "都不合适？"}</span>
          <button className="link" onClick={onTerminal}>
            其他回答：去终端<ChevronRight />
          </button>
        </>
      )}
    </div>
  );
  return {
    label: askTitle(agent),
    text: item.text,
    busy: !!ans.busy,
    steps: (
      <span className="steps">
        {n > 1 && q.questions.map((_, i) => <i key={i} className={i <= index ? "done" : ""} />)}
        <span className="muted">{progress}</span>
      </span>
    ),
    progress,
    answers,
    actions: (page: boolean) => (
      <>
        {submit}
        {failed}
        {foot(page)}
      </>
    ),
  };
}

function QuestionDock({ remote, sid, q, agent, frame, onTerminal }: { remote: Remote; sid: string; q: QuestionAsked; agent: Agent; frame: Frame; onTerminal: () => void }) {
  const p = useQuestion(remote, sid, q, agent, onTerminal);
  if (!p) return null;
  return (
    <Pending
      frame={frame}
      label={p.label}
      brief={p.text}
      busy={p.busy}
      meta={metaOf(frame.more > 0 && `还有 ${frame.more} 项`, p.steps)}
      body={
        <>
          <div className="q">{p.text}</div>
          {p.answers}
        </>
      }
      actions={p.actions(false)}
    />
  );
}

export function PlanButtons({ remote, sid, full, shared, terminalOnly = false }: { remote: Remote; sid: string; full?: boolean; shared?: ReturnType<typeof usePlan>; terminalOnly?: boolean }) {
  // Claude Code has no "clear the context, then implement".
  const own = usePlan(remote, sid, terminalOnly);
  const plan = shared ?? own;
  const b = plan.busy;
  if (terminalOnly) return <div className="muted">{skeletonMessage}</div>;
  return (
    <>
      <div className="btns decide">
        <button className="btn" disabled={!!b} onClick={() => void plan.answer(PlanChoice.KEEP_PLANNING)}>
          {b === String(PlanChoice.KEEP_PLANNING) ? "提交中…" : "继续规划"}
        </button>
        <button className="btn pri" disabled={!!b} onClick={() => void plan.answer(PlanChoice.IMPLEMENT)}>
          {b === String(PlanChoice.IMPLEMENT) ? "提交中…" : "批准执行"}
        </button>
      </div>
      {full && agentOf(remote.sessions.get(sid)) !== Agent.CLAUDE && (
        <div className="btns decide" style={{ marginTop: 8 }}>
          <button className="btn" disabled={!!b} onClick={() => void plan.answer(PlanChoice.CLEAR_AND_IMPLEMENT)}>
            {b === String(PlanChoice.CLEAR_AND_IMPLEMENT) ? "提交中…" : "清空上下文后执行"}
          </button>
        </div>
      )}
      {plan.error && (
        <Banner kind="err" action="重试" onAction={plan.retry}>
          没能提交：{plan.error}
        </Banner>
      )}
    </>
  );
}

function PlanDock({ remote, sid, plan, frame, onFull }: { remote: Remote; sid: string; plan?: PlanProposed; frame: Frame; onFull: () => void }) {
  const state = usePlan(remote, sid, isSkeleton(plan));
  if (isSkeleton(plan)) return <div className="dock muted">{skeletonMessage}</div>;
  return (
    <Pending
      frame={frame}
      label="计划待批准"
      brief=""
      busy={!!state.busy}
      meta={metaOf(frame.more > 0 && `还有 ${frame.more} 项`)}
      extra={
        plan && (
          <button className="link pending-full" onClick={onFull}>
            展开全文<ChevronRight />
          </button>
        )
      }
      body={<div className="plan-body">{plan ? <Markdown text={plan.text} /> : <span className="muted">计划的内容还没读到，可以去终端看</span>}</div>}
      actions={
        <>
          <PlanButtons remote={remote} sid={sid} shared={state} terminalOnly={isSkeleton(plan)} />
          {/* Claude Code leaves plan mode for manual approval of edits. */}
          {!isSkeleton(plan) && agentOf(remote.sessions.get(sid)) === Agent.CLAUDE && (
            <div className="foot">
              <span>批准后使用询问模式</span>
            </div>
          )}
        </>
      }
    />
  );
}

function PopupDock({ title, agent, frame, onTerminal }: { title: string; agent: Agent; frame: Frame; onTerminal: () => void }) {
  return (
    <Pending
      frame={frame}
      label="终端里有弹窗"
      brief={title}
      meta={metaOf(frame.more > 0 && `还有 ${frame.more} 项`, `${agentName(agent)} 在等你`)}
      body={
        title && (
          <>
            <div className="q light">{title}</div>
            {/* Claude Code's trust prompt starts on "No, exit": Enter alone
                ends the session (claude-code-notes §1). Trusting stays the
                user's choice, made in the terminal. */}
            {agent === Agent.CLAUDE && /trust/i.test(title) && (
              <div className="desc">
                默认选中的是 <b>No, exit</b>。按 ↓ 选中 <b>Yes, I trust this folder</b> 再按 ⏎
              </div>
            )}
          </>
        )
      }
      actions={
        <div className="btns">
          <button className="btn pri" onClick={onTerminal}>
            去终端处理
          </button>
        </div>
      }
    />
  );
}

/** The least room, in pixels, an item needs to open in the dock: its line,
 * some of its content and its actions. With less it opens as a page. */
export const DOCK_MIN_ROOM = 200;

/** What an open dock's content may take: about 60% of the chat column's
 * height, 240 at the least, and never more than the room under its line. */
export function dockContentMax(room: number, height: number): number | undefined {
  if (!Number.isFinite(room)) return undefined;
  return Math.max(0, Math.min(room - 44, Math.max(240, Math.round(height * 0.6))));
}

/** Which docked item a 收起 was for: the item, and for those whose ID a
 * session reuses (its plan, its popup) what they say, so that a new one
 * opens again. */
function dockKey(sid: string, item: DockItem): string {
  const what = item.kind === "plan" ? (item.plan?.text ?? "") : item.kind === "popup" ? item.title : "";
  return `${sid}|${item.id}|${what}`;
}

/** The first waiting item, docked above the input (client-v7 停靠区). It
 * comes open: what waits for the user is what they came to see. 收起
 * keeps that item to one line (remembered by its ID while the page is
 * loaded); the next item comes open again. It opens up to room pixels (what
 * the input and a little chat leave); with no room for that it stays one
 * line that opens as a page. It stays one line while the user types,
 * unless a reason is being written. */
export function Dock({
  remote,
  sid,
  items,
  agent,
  collapsed,
  room = Infinity,
  height = 0,
  onExpand,
  onDetail,
  onPlan,
  onQuestion,
  onTerminal,
}: {
  remote: Remote;
  sid: string;
  items: DockItem[];
  agent: Agent;
  collapsed: boolean;
  room?: number;
  // The chat column's height, which the open content takes a share of.
  height?: number;
  onExpand: () => void;
  onDetail: (approvalId: string) => void;
  onPlan: () => void;
  onQuestion?: () => void;
  onTerminal: () => void;
}) {
  const [, redraw] = useState(0);
  const [reasoning, setReasoning] = useState(false);
  const item = items[0];
  if (!item) return null;
  const key = dockKey(sid, item);
  const open = !remote.dockClosed.has(key);
  // Typing a reason keeps it open whatever the room or the keyboard.
  const shown = open && (reasoning || (!collapsed && room >= DOCK_MIN_ROOM));
  const page = () => {
    if (item.kind === "approval") onDetail(item.approval.approvalId);
    else if (item.kind === "plan") onPlan();
    else if (item.kind === "question") onQuestion?.();
    else onTerminal();
  };
  const frame: Frame = {
    open: shown,
    more: items.length - 1,
    onToggle: () => {
      if (shown) remote.dockClosed.add(key);
      else if (room < DOCK_MIN_ROOM) return page();
      else {
        remote.dockClosed.delete(key);
        onExpand();
      }
      redraw((n) => n + 1);
    },
  };
  const max = dockContentMax(room, height);
  const style = (max === undefined ? {} : { "--content-max": `${max}px` }) as CSSProperties;
  let card;
  switch (item.kind) {
    case "approval":
      card = (
        <ApprovalDock
          key={item.id}
          remote={remote}
          sid={sid}
          a={item.approval}
          agent={agent}
          frame={frame}
          reasoning={reasoning}
          onReasonOpen={setReasoning}
        />
      );
      break;
    case "question":
      card = <QuestionDock key={item.id} remote={remote} sid={sid} q={item.question} agent={agent} frame={frame} onTerminal={onTerminal} />;
      break;
    case "plan":
      card = <PlanDock key={item.id} remote={remote} sid={sid} plan={item.plan} frame={frame} onFull={onPlan} />;
      break;
    case "popup":
      card = <PopupDock key={item.id} title={item.title} agent={agent} frame={frame} onTerminal={onTerminal} />;
      break;
  }
  return (
    <div className="dockwrap" style={style}>
      {card}
    </div>
  );
}

/** A question on its own page, for when the dock has no room for it: the
 * question and options scroll, the buttons stay at the bottom. */
export function QuestionPage({ remote, sid, q, agent, title, onBack, onTerminal }: { remote: Remote; sid: string; q: QuestionAsked; agent: Agent; title: string; onBack: () => void; onTerminal: () => void }) {
  const p = useQuestion(remote, sid, q, agent, onTerminal);
  return (
    <div className="page">
      <NavBar onBack={onBack} title="回答提问" sub={`${title} · ${agentName(agent)}`} />
      {p && (
        <>
          <div className="question-full">
            <div className="muted small">{p.progress}</div>
            <div className="q">{p.text}</div>
            {p.answers}
          </div>
          <div className="dock ask">{p.actions(true)}</div>
        </>
      )}
    </div>
  );
}

/** An approval on its own page: the whole command, scrollable, with the
 * buttons docked at the bottom. */
export function ApprovalPage({ remote, sid, a, agent, title, onBack }: { remote: Remote; sid: string; a: ApprovalRequested; agent: Agent; title: string; onBack: () => void }) {
  const now = useNow(1000);
  const [reasoning, setReasoning] = useState(false);
  const deadline = deadlineOf(a);
  if (isSkeleton(a)) return <div className="page"><NavBar onBack={onBack} title="审批" sub={title} /><div className="body detail muted">{skeletonMessage}</div></div>;
  return (
    <div className="page">
      <NavBar onBack={onBack} title="审批详情" sub={`${title}${deadline ? " · " + minutesLeft(deadline, now) : ""}`} />
      <div className="body detail">
        {/* A command speaks for itself; other requests name the tool. */}
        {!a.command && <div className="h-kind">{approvalTitle(a)}</div>}
        <pre className="cmd">{approvalText(a)}</pre>
        {a.description && <div className="desc">{approvalDisplay(a.description)}</div>}
      </div>
      <div className={"dock" + (reasoning ? " reasoning" : "")}>
        <ApprovalActions remote={remote} sid={sid} a={a} agent={agent} onReasonOpen={setReasoning} />
      </div>
    </div>
  );
}

/** The whole plan, with every choice. */
export function PlanPage({ remote, sid, plan, title, onBack }: { remote: Remote; sid: string; plan: PlanProposed; title: string; onBack: () => void }) {
  if (isSkeleton(plan)) return <div className="page"><NavBar onBack={onBack} title="计划" sub={title} /><div className="body detail muted">{skeletonMessage}</div></div>;
  return (
    <div className="page">
      <NavBar onBack={onBack} title="计划" sub={title} />
      <div className="body detail ai">
        <Markdown text={plan.text} />
      </div>
      <div className="dock plan">
        <PlanButtons remote={remote} sid={sid} full terminalOnly={isSkeleton(plan)} />
      </div>
    </div>
  );
}
