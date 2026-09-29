// What a session waits for, docked above the input: approvals, questions,
// plans and terminal popups. Only the first item is docked; long content
// opens a full page, and the buttons stay in view.

import { useLayoutEffect, useRef, useState } from "react";
import type { Agent, ApprovalRequested, PlanProposed, QuestionAsked } from "../gen/remoteagent/v1/event_pb";
import { PlanChoice } from "../gen/remoteagent/v1/local_pb";
import { Markdown } from "../markdown";
import { agentName, askTitle, minutesLeft, optionLabel, type DockItem } from "../model";
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
function summary(item: DockItem, agent: Agent): [string, string] {
  switch (item.kind) {
    case "approval":
      return [approvalTitle(item.approval), approvalText(item.approval).replace(/\s+/g, " ")];
    case "question":
      return [askTitle(agent), item.question.questions[0]?.text ?? ""];
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
    if (el) setCut(el.scrollHeight > el.clientHeight + 2);
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
  const [current, setCurrent] = useState(0);
  const ans = useAnswer(remote, sid, q.questionId);
  const n = q.questions.length;
  const index = Math.min(current, n - 1);
  const item = q.questions[index];
  if (!item) return null;
  const [ci, co] = ans.chosen ? ans.chosen.split(":").map(Number) : [-1, -1];
  const pick = async (option: number) => {
    const ok = await ans.answer(index, option);
    if (ok && index < n - 1) {
      ans.done();
      setCurrent(index + 1);
    }
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
          </span>
        </span>
      </div>
      <div className="dscroll">
      <div className="q">{item.text}</div>
      <div className="answers">
        {item.options.map((o, i) => {
          const { text, recommended } = optionLabel(o.label);
          const on = ci === index && co === i + 1;
          return (
            <button key={i} className={"ans" + (on ? " on" : "")} disabled={!!ans.busy} onClick={() => void pick(i + 1)}>
              {on && <span className="ck">✓</span>}
              <div>
                {text}
                {on && ans.busy ? <small>提交中…</small> : on && ans.error ? <small className="red">没能提交，终端里的问题可能已经变了</small> : o.description && <small>{o.description}</small>}
              </div>
              {recommended && <span className="tag">推荐</span>}
            </button>
          );
        })}
      </div>
      </div>
      <div className="foot">
        {ans.error ? (
          <>
            <span />
            <button className="link" onClick={ans.retry}>
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
      {full && (
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

/** The first waiting item, docked; collapsed to one line while the user
 * types a message. */
export function Dock({
  remote,
  sid,
  items,
  agent,
  collapsed,
  onExpand,
  onDetail,
  onPlan,
  onTerminal,
}: {
  remote: Remote;
  sid: string;
  items: DockItem[];
  agent: Agent;
  collapsed: boolean;
  onExpand: () => void;
  onDetail: (approvalId: string) => void;
  onPlan: () => void;
  onTerminal: () => void;
}) {
  const item = items[0];
  if (!item) return null;
  const more = items.length - 1;
  if (collapsed) {
    const [kind, text] = summary(item, agent);
    const cls = item.kind === "question" ? " ask" : item.kind === "plan" ? " plan" : "";
    return (
      <div className={"dock mini" + cls}>
        <b>{kind}</b>
        <code>{text}</code>
        <button onMouseDown={(e) => e.preventDefault()} onClick={onExpand}>
          展开
        </button>
      </div>
    );
  }
  switch (item.kind) {
    case "approval":
      return <ApprovalDock key={item.id} remote={remote} sid={sid} a={item.approval} agent={agent} more={more} onDetail={() => onDetail(item.approval.approvalId)} />;
    case "question":
      return <QuestionDock key={item.id} remote={remote} sid={sid} q={item.question} agent={agent} more={more} onTerminal={onTerminal} />;
    case "plan":
      return <PlanDock remote={remote} sid={sid} plan={item.plan} more={more} onFull={onPlan} />;
    case "popup":
      return <PopupDock title={item.title} agent={agent} more={more} onTerminal={onTerminal} />;
  }
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
