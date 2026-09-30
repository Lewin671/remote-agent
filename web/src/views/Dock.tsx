// What a session waits for, docked above the input: approvals, questions,
// plans and terminal popups. Only the first item is docked; long content
// opens a full page, and the buttons stay in view.

import { useLayoutEffect, useRef, useState, type CSSProperties } from "react";
import type { ApprovalRequested, PlanProposed, QuestionAsked } from "../gen/remoteagent/v1/event_pb";
import { PlanChoice } from "../gen/remoteagent/v1/local_pb";
import { Markdown } from "../markdown";
import { Agent } from "../gen/remoteagent/v1/event_pb";
import { agentName, agentOf, askTitle, minutesLeft, optionLabel, type DockItem } from "../model";
import type { Remote } from "../remote";
import { Banner, NavBar, useNow } from "../ui";
import { deadlineOf, useAnswer, useApproval, usePlan } from "./actions";

export function approvalTitle(a: ApprovalRequested): string {
  return a.command ? "请求执行" : "请求权限 · " + a.toolName;
}

export function approvalText(a: ApprovalRequested): string {
  return a.command || a.toolInputJson || a.toolName;
}

/** A dock item as one line: its type and a summary. */
function summary(item: DockItem, agent: Agent, questionIndex = 0): [string, string] {
  switch (item.kind) {
    case "approval":
      return [approvalTitle(item.approval), approvalText(item.approval).replace(/\s+/g, " ")];
    case "question":
      return [askTitle(agent), item.question.questions[Math.min(questionIndex, item.question.questions.length - 1)]?.text ?? ""];
    case "plan":
      return ["计划待批准", (item.plan?.text ?? "").replace(/\s+/g, " ")];
    case "popup":
      return ["终端里有弹窗", item.title];
  }
}

/** A <pre> cut to its max height, with 展开全文 when it overflows. */
function Cut({ text, onMore, className = "" }: { text: string; onMore?: () => void; className?: string }) {
  const ref = useRef<HTMLPreElement>(null);
  const [cut, setCut] = useState(false);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    // More lines than the block shows is cut however it lays out; the
    // measurement catches long lines that wrap. A block mounted hidden
    // (the dock collapsed while typing) measures nothing, so measure again
    // whenever its size changes, as when it is shown.
    const measure = () => setCut(text.split("\n").length > 4 || el.scrollHeight > el.clientHeight + 2);
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, [text]);
  return (
    <div className={"cutbox " + className}>
      <pre ref={ref}>{text}</pre>
      {cut && onMore && (
        <button className="cutmore" onClick={onMore}>
          展开全文
        </button>
      )}
    </div>
  );
}

/** Approval content shared by the dock, the home card and the detail
 * page: the reason field replaces the buttons while it is open. */
export function ApprovalActions({
  remote,
  sid,
  a,
  agent,
  footless,
  wideRow,
  onReasonOpen,
}: {
  remote: Remote;
  sid: string;
  a: ApprovalRequested;
  agent: Agent;
  footless?: boolean;
  wideRow?: boolean;
  onReasonOpen?: (open: boolean) => void;
}) {
  const now = useNow(1000);
  const ap = useApproval(remote, sid, a);
  const [reasonOpen, setReasonOpen] = useState(false);
  const [reason, setReason] = useState("");
  const deadline = deadlineOf(a);
  const expired = deadline > 0 && now >= deadline;
  const locked = !!ap.busy || expired;
  const open = (v: boolean) => {
    setReasonOpen(v);
    onReasonOpen?.(v);
  };
  const error = ap.error && <Banner kind="err" action="重试" onAction={ap.retry}>没能提交：{ap.error}</Banner>;
  if (reasonOpen)
    return (
      <>
        <textarea
          className="field reason"
          autoFocus
          rows={1}
          placeholder={`拒绝的理由，会告诉 ${agentName(agent)}`}
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          onBlur={() => !reason.trim() && !ap.busy && open(false)}
        />
        <div className="btns">
          <button className="btn danger" disabled={locked || !reason.trim()} onMouseDown={(e) => e.preventDefault()} onClick={() => void ap.resolve(false, reason.trim())}>
            {ap.busy ? "拒绝中…" : "发送理由并拒绝"}
          </button>
        </div>
        {error}
      </>
    );
  const buttons = (
    <div className="btns">
      <button className="btn no" disabled={locked} onClick={() => void ap.resolve(false)}>
        {ap.busy.startsWith("deny") ? "拒绝中…" : "拒绝"}
      </button>
      <button className="btn pri" disabled={locked} onClick={() => void ap.resolve(true)}>
        {ap.busy.startsWith("allow") ? "允许中…" : "允许"}
      </button>
    </div>
  );
  if (wideRow)
    return (
      <>
        <div className="actrow">
          <button className="link" disabled={locked} onClick={() => open(true)}>
            附理由拒绝 ›
          </button>
          {buttons}
        </div>
        {error}
      </>
    );
  return (
    <>
      {buttons}
      {!footless && (
        <div className="foot">
          <span>想告诉 {agentName(agent)} 为什么？</span>
          <button className="link" disabled={locked} onClick={() => open(true)}>
            附理由拒绝 ›
          </button>
        </div>
      )}
      {error}
    </>
  );
}

function ApprovalDock({ remote, sid, a, agent, more, onDetail }: { remote: Remote; sid: string; a: ApprovalRequested; agent: Agent; more: number; onDetail: () => void }) {
  const now = useNow(1000);
  const [reasoning, setReasoning] = useState(false);
  const deadline = deadlineOf(a);
  const left = deadline ? minutesLeft(deadline, now) : "";
  return (
    <div className={"dock" + (reasoning ? " reasoning" : "")}>
      {!reasoning && (
        <div className="dscroll">
          <div className="h">
            <b>{approvalTitle(a)}</b>
            <span className="muted">
              {more > 0 && `还有 ${more} 项 · `}
              {left}
              {left && more === 0 && left !== "已超时" && "，超时拒绝"}
            </span>
          </div>
          {a.description && <div className="desc">{a.description}</div>}
          <Cut text={approvalText(a)} onMore={onDetail} />
        </div>
      )}
      <ApprovalActions remote={remote} sid={sid} a={a} agent={agent} onReasonOpen={setReasoning} />
    </div>
  );
}

function QuestionDock({ remote, sid, q, agent, more, onTerminal }: { remote: Remote; sid: string; q: QuestionAsked; agent: Agent; more: number; onTerminal: () => void }) {
  // Which question to answer. The daemon only enters the option while the
  // terminal shows this very question, so a stale choice is refused rather
  // than answering another question.
  // Its answering state, ticks included, is shared with the question page.
  const ans = useAnswer(remote, sid, q.questionId);
  const current = ans.index;
  const setCurrent = ans.setIndex;
  const ticked = ans.ticked;
  const setTicked = (f: number[] | ((t: number[]) => number[])) => ans.setTicked(typeof f === "function" ? f : () => f);
  const n = q.questions.length;
  const index = Math.min(current, n - 1);
  const item = q.questions[index];
  if (!item) return null;
  const multi = item.multiSelect;
  const sent = ans.chosen.index === index ? ans.chosen.options : [];
  const pick = async (options: number[]) => {
    const ok = await ans.answer(index, options, multi);
    if (ok && index < n - 1) {
      ans.done();
      setTicked([]);
      setCurrent(index + 1);
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
  return (
    <div className="dock ask">
      <div className="h">
        <b>{askTitle(agent)}</b>
        <span className="steps">
          {n > 1 && q.questions.map((_, i) => <i key={i} className={i <= index ? "done" : ""} />)}
          <span className="muted">
            {more > 0 && `还有 ${more} 项 · `}
            {n > 1 ? `第 ${index + 1} 题，共 ${n} 题` : item.header}
            {multi && (n > 1 || item.header ? " · " : "") + (multi ? "可多选" : "")}
          </span>
        </span>
      </div>
      <div className="dscroll">
        <div className="q">{item.text}</div>
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
                {multi ? <span className={"box" + (on ? " on" : "")}>{on ? "✓" : ""}</span> : on && <span className="ck">✓</span>}
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
      </div>
      {multi && !(ans.error && ans.unknown) && (
        <div className="btns">
          <button className="btn pri" disabled={!!ans.busy || ticked.length === 0} onClick={() => void pick(ticked)}>
            {ans.busy ? "提交中…" : ticked.length ? `提交 ${ticked.length} 项` : "选好后提交"}
          </button>
        </div>
      )}
      {multi && ans.error && (
        <div className="red small">{ans.unknown ? "不确定提交了没有，重试会发送同一个回答" : `没能提交：${ans.error}。可以改选后再提交`}</div>
      )}
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
            <span>都不合适？</span>
            <button className="link" onClick={onTerminal}>
              其他回答：去终端 ›
            </button>
          </>
        )}
      </div>
    </div>
  );
}

export function PlanButtons({ remote, sid, full }: { remote: Remote; sid: string; full?: boolean }) {
  // Claude Code has no "clear the context, then implement".
  const plan = usePlan(remote, sid);
  const b = plan.busy;
  return (
    <>
      <div className="btns">
        <button className="btn" disabled={!!b} onClick={() => void plan.answer(PlanChoice.KEEP_PLANNING)}>
          {b === String(PlanChoice.KEEP_PLANNING) ? "提交中…" : "继续规划…"}
        </button>
        <button className="btn pri" disabled={!!b} onClick={() => void plan.answer(PlanChoice.IMPLEMENT)}>
          {b === String(PlanChoice.IMPLEMENT) ? "提交中…" : "批准执行"}
        </button>
      </div>
      {full && agentOf(remote.sessions.get(sid)) !== Agent.CLAUDE && (
        <div className="btns" style={{ marginTop: 8 }}>
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

function PlanDock({ remote, sid, plan, more, onFull }: { remote: Remote; sid: string; plan?: PlanProposed; more: number; onFull: () => void }) {
  return (
    <div className="dock plan">
      <div className="h">
        <b>计划待批准</b>
        <span>
          {more > 0 && <span className="muted">还有 {more} 项 · </span>}
          {plan && (
            <button className="link" onClick={onFull}>
              展开全文 ›
            </button>
          )}
        </span>
      </div>
      <div className="dscroll">
        <div className="plan-body">{plan ? <Markdown text={plan.text} /> : <span className="muted">计划的内容还没读到，可以去终端看</span>}</div>
      </div>
      <PlanButtons remote={remote} sid={sid} />
    </div>
  );
}

function PopupDock({ title, agent, more, onTerminal }: { title: string; agent: Agent; more: number; onTerminal: () => void }) {
  return (
    <div className="dock">
      <div className="h">
        <b>终端里有弹窗</b>
        <span className="muted">
          {more > 0 && `还有 ${more} 项 · `}
          {agentName(agent)} 在等你
        </span>
      </div>
      {title && (
        <div className="dscroll">
          <div className="q light">{title}</div>
        </div>
      )}
      <div className="btns">
        <button className="btn pri" onClick={onTerminal}>
          去终端处理
        </button>
      </div>
    </div>
  );
}

/** Room the dock needs: its controls, at least 44 px of its content (less
 * if there is less), and its bottom margin. */
export function dockNeed(card: number, content: number, contentScroll: number): number {
  return card - content + Math.min(44, contentScroll) + 6;
}

/** The first waiting item, docked. It gets at most room pixels (45% of
 * the chat column). A card that cannot fit its controls and a little of
 * its content in that shows as one line whose 展开 opens it as a page, so
 * the input stays in view; so does any card while the user types. */
export function Dock({
  remote,
  sid,
  items,
  agent,
  collapsed,
  room = Infinity,
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
  onExpand: () => void;
  onDetail: (approvalId: string) => void;
  onPlan: () => void;
  onQuestion?: () => void;
  onTerminal: () => void;
}) {
  const wrap = useRef<HTMLDivElement>(null);
  const [need, setNeed] = useState(0);
  const [controls, setControls] = useState(0);
  const [reasoning, setReasoning] = useState(false);
  const item = items[0];
  useLayoutEffect(() => {
    const card = wrap.current?.firstElementChild as HTMLElement | null;
    if (!card) return;
    const measure = () => {
      const scroll = card.querySelector<HTMLElement>(":scope > .dscroll");
      const content = scroll?.offsetHeight ?? 0;
      setControls(card.offsetHeight - content);
      setNeed(dockNeed(card.offsetHeight, content, scroll?.scrollHeight ?? 0));
      setReasoning(card.classList.contains("reasoning"));
    };
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(measure);
    ro.observe(card);
    const scroll = card.querySelector(":scope > .dscroll");
    if (scroll) ro.observe(scroll);
    // The card's class changes when a reason is being typed.
    const mo = new MutationObserver(measure);
    mo.observe(card, { attributes: true, attributeFilter: ["class"], childList: true, subtree: true });
    return () => {
      ro.disconnect();
      mo.disconnect();
    };
  }, [item?.id]);
  if (!item) return null;
  const more = items.length - 1;
  let full;
  switch (item.kind) {
    case "approval":
      full = <ApprovalDock key={item.id} remote={remote} sid={sid} a={item.approval} agent={agent} more={more} onDetail={() => onDetail(item.approval.approvalId)} />;
      break;
    case "question":
      full = <QuestionDock key={item.id} remote={remote} sid={sid} q={item.question} agent={agent} more={more} onTerminal={onTerminal} />;
      break;
    case "plan":
      full = <PlanDock key={item.id} remote={remote} sid={sid} plan={item.plan} more={more} onFull={onPlan} />;
      break;
    case "popup":
      full = <PopupDock key={item.id} title={item.title} agent={agent} more={more} onTerminal={onTerminal} />;
      break;
  }
  // Typing a reason keeps the card above the keyboard, whatever the room.
  const compact = !reasoning && need > room;
  const oneLine = compact || (collapsed && !reasoning);
  const page = () => {
    if (item.kind === "approval") onDetail(item.approval.approvalId);
    else if (item.kind === "plan") onPlan();
    else if (item.kind === "question") onQuestion?.();
    else onTerminal();
  };
  const [kind, text] = summary(item, agent, item.kind === "question" ? (remote.answerState(item.question.questionId).index) : 0);
  const cls = item.kind === "question" ? " ask" : item.kind === "plan" ? " plan" : "";
  // The full card stays mounted, measurable but out of sight, while the
  // one-line form shows: a question's progress or a half-typed reason
  // survives, and the dock knows when the card fits again.
  const style = { "--content-max": Number.isFinite(room) ? `${Math.max(44, room - controls - 6)}px` : "none" } as CSSProperties;
  return (
    <>
      {oneLine && (
        <div className={"dock mini" + cls}>
          <b>{kind}</b>
          <code>{text}</code>
          <button onMouseDown={(e) => e.preventDefault()} onClick={compact ? page : onExpand}>
            展开
          </button>
        </div>
      )}
      <div className={"dockwrap" + (oneLine ? " offstage" : "")} ref={wrap} style={style} aria-hidden={oneLine || undefined}>
        {full}
      </div>
    </>
  );
}

/** A question on its own page, for when the dock has no room for it. */
export function QuestionPage({ remote, sid, q, agent, title, onBack, onTerminal }: { remote: Remote; sid: string; q: QuestionAsked; agent: Agent; title: string; onBack: () => void; onTerminal: () => void }) {
  return (
    <div className="page">
      <NavBar onBack={onBack} title="回答提问" sub={title} />
      <div className="body pad qpage">
        <QuestionDock key={q.questionId} remote={remote} sid={sid} q={q} agent={agent} more={0} onTerminal={onTerminal} />
      </div>
    </div>
  );
}

/** An approval on its own page: the whole command, scrollable, with the
 * buttons docked at the bottom. */
export function ApprovalPage({ remote, sid, a, agent, title, onBack }: { remote: Remote; sid: string; a: ApprovalRequested; agent: Agent; title: string; onBack: () => void }) {
  const now = useNow(1000);
  const [reasoning, setReasoning] = useState(false);
  const deadline = deadlineOf(a);
  return (
    <div className="page">
      <NavBar onBack={onBack} title="审批详情" sub={`${title}${deadline ? " · " + minutesLeft(deadline, now) : ""}`} />
      <div className="body pad detail">
        <div className="h-kind">{approvalTitle(a)}</div>
        {a.description && <div className="desc">{a.description}</div>}
        <pre className="cmd">{approvalText(a)}</pre>
      </div>
      <div className={"dock" + (reasoning ? " reasoning" : "")}>
        <ApprovalActions remote={remote} sid={sid} a={a} agent={agent} onReasonOpen={setReasoning} />
      </div>
    </div>
  );
}

/** The whole plan, with every choice. */
export function PlanPage({ remote, sid, plan, title, onBack }: { remote: Remote; sid: string; plan: PlanProposed; title: string; onBack: () => void }) {
  return (
    <div className="page">
      <NavBar onBack={onBack} title="计划" sub={title} />
      <div className="body pad detail ai">
        <Markdown text={plan.text} />
      </div>
      <div className="dock plan">
        <PlanButtons remote={remote} sid={sid} full />
      </div>
    </div>
  );
}
