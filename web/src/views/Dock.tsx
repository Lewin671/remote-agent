import * as i18n from "../i18n";
import { isSkeleton, skeletonMessage } from "../skeleton";
// What a session waits for, docked above the input (design-system §6 停靠区):
// approvals, questions, plans and terminal popups, each in the dark card's
// shell. Only the first item is docked, open or folded to one dark line;
// what has no room opens as a page, and the buttons stay in view.

import { useRef, useState, type CSSProperties, type ReactNode } from "react";
import { useGrow, useHeightFlip } from "../motion";
import type { Agent, ApprovalRequested, PlanProposed, QuestionAsked } from "../gen/remoteagent/v1/event_pb";
import { PlanChoice } from "../gen/remoteagent/v1/local_pb";
import { approvalDisplay } from "../approval-display";
import { Markdown } from "../markdown";
import { agentInfo } from "../agents";
import { agentName, agentOf, askTitle, minutesLeft, optionLabel, type DockItem } from "../model";
import type { Remote } from "../remote";
import { Banner, Exp, NavBar, useNow } from "../ui";
import { deadlineOf, useAnswer, usePlan } from "./actions";
import { Check, ChevronRight } from "../icons";
import { ApprovalCard, approvalText, approvalTitle } from "./ApprovalCard";

export { approvalText, approvalTitle };

/** How a docked item draws itself: its one line, whether it is open, and
 * how many more items wait behind it. */
type Frame = { open: boolean; onToggle: () => void; more: number };

/** The head of an open item: 「● 类型　元信息　收起」, with `extra` (the
 * plan's 展开全文) before 收起. */
function OpenHead({ frame, label, meta, extra, busy }: { frame: Frame; label: string; meta?: ReactNode; extra?: ReactNode; busy?: boolean }) {
  const keep = (e: { preventDefault(): void }) => e.preventDefault();
  return (
    <div className="k pending-head">
      <button className="pending-sum" aria-expanded onMouseDown={keep} onClick={frame.onToggle}>
        <i />
        <span className="pending-label">{label}</span>
      </button>
      <span className="pending-brief">{meta}</span>
      {extra}
      {/* The same toggle as the label; the label is the one read aloud. */}
      <button className="pending-action" tabIndex={-1} aria-hidden onMouseDown={keep} onClick={frame.onToggle}>
        {busy ? i18n.t("dock.waiting_confirmation") : i18n.t("markdown.collapse")}
      </button>
    </div>
  );
}

/** A folded item: one dark line 「● 类型 · 摘要 · 处理 ⌄」 that opens it. */
function FoldedLine({ frame, label, brief, busy }: { frame: Frame; label: string; brief: string; busy?: boolean }) {
  const info = [brief, frame.more > 0 ? i18n.t("dock.more", { count: frame.more }) : ""].filter(Boolean).join(" · ");
  return (
    <div className={"dock pending" + (busy ? " submitting" : "")}>
      <div className="tc line">
        <button className="k pending-sum" aria-expanded={false} onMouseDown={(e) => e.preventDefault()} onClick={frame.onToggle}>
          <i />
          <span className="pending-label">{label}</span>
          <span className="pending-brief">{info}</span>
          <span className="pending-action">{busy ? i18n.t("dock.waiting_confirmation") : <>{i18n.t("dock.handle")} <Exp /></>}</span>
        </button>
      </div>
    </div>
  );
}

/** A waiting item in the dark card's shell (design-system §6 停靠区): one
 * line that opens in place into its content, which scrolls, over its
 * actions, which stay in view. */
function Pending({
  frame,
  label,
  brief,
  meta,
  extra,
  busy,
  body,
  actions,
}: {
  frame: Frame;
  label: string;
  brief: string;
  meta?: ReactNode;
  extra?: ReactNode;
  busy?: boolean;
  body: ReactNode;
  actions: ReactNode;
}) {
  if (!frame.open) return <FoldedLine frame={frame} label={label} brief={brief} busy={busy} />;
  return (
    <div className={"dock pending open" + (busy ? " submitting" : "")}>
      <div className="tc">
        <OpenHead frame={frame} label={label} meta={meta} extra={extra} busy={busy} />
        <div className="pending-content">
          <div className="pending-scroll">{body}</div>
          <div className="pending-actions">{actions}</div>
        </div>
      </div>
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
  frame,
  onReasonOpen,
  reasoning,
}: {
  remote: Remote;
  sid: string;
  a: ApprovalRequested;
  frame: Frame;
  onReasonOpen: (open: boolean) => void;
  reasoning: boolean;
}) {
  const now = useNow(1000);
  const deadline = deadlineOf(a);
  const left = deadline ? minutesLeft(deadline, now) : "";
  const title = approvalTitle(a);
  if (isSkeleton(a)) return <div className="dock pending"><div className="tc line"><div className="k">{skeletonMessage()}</div></div></div>;
  // A permission request's arguments are no summary: its title says it.
  if (!frame.open) return <FoldedLine frame={frame} label={title} brief={a.command ? approvalDisplay(a.command).replace(/\s+/g, " ") : ""} />;
  // Answered, the card itself shrinks to its one line of where it stands.
  return (
    <div className={"dock pending open" + (reasoning ? " reasoning" : "")}>
      <ApprovalCard
        remote={remote}
        sid={sid}
        a={a}
        docked
        onDenying={onReasonOpen}
        // The head says the time; what happens at its end is the home card's to say.
        head={<OpenHead frame={frame} label={title} meta={metaOf(frame.more > 0 && i18n.t("dock.more", { count: frame.more }), left)} />}
      />
    </div>
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
  if (isSkeleton(q)) return { label: askTitle(agent), text: skeletonMessage(), busy: false, progress: "", answers: null, actions: (_page: boolean) => null };
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
  const where = n > 1 ? i18n.t("dock.question_progress", { index: index + 1, count: n }) : item.header;
  const progress = [where, multi ? i18n.t("dock.multiselect") : ""].filter(Boolean).join(" · ");
  const answers = (
    <div className="answers">
      {item.options.map((o, i) => {
        const { text, recommended } = optionLabel(o.label);
        const on = multi ? (frozen ? sent : ticked).includes(i + 1) : sent.includes(i + 1);
        const status = on && sent.includes(i + 1) && (ans.busy ? "sending" : ans.error ? "err" : "");
        return (
          <button
            key={i}
            className={"opt" + (on ? " on" : "") + (multi ? " multi" : "")}
            role={multi ? "checkbox" : undefined}
            aria-checked={multi ? on : undefined}
            disabled={multi ? frozen : !!ans.busy}
            onClick={() => (multi ? tick(i + 1) : void pick([i + 1]))}
          >
            {multi && <span className={"box" + (on ? " on" : "")}>{on && <Check />}</span>}
            <div>
              {text}
              {status === "err" && !multi ? (
                <small className="err">{i18n.t("dock.question_changed")}</small>
              ) : status && status !== "err" && !multi ? (
                <small>{status === "sending" ? i18n.t("dock.submitting") : ""}</small>
              ) : (
                o.description && <small>{o.description}</small>
              )}
            </div>
            {recommended && <span className="tag">{i18n.t("dock.recommended")}</span>}
            {!multi && on && <Check className="ck" />}
          </button>
        );
      })}
    </div>
  );
  const submit = multi && !(ans.error && ans.unknown) && (
    <div className="btns">
      <button className="btn pri" disabled={!!ans.busy || ticked.length === 0} onClick={() => void pick(ticked)}>
        {ans.busy ? i18n.t("dock.submitting") : ticked.length ? i18n.t("dock.submit_selected", { count: ticked.length }) : i18n.t("dock.select_then_submit")}
      </button>
    </div>
  );
  const failed = multi && ans.error && <div className="ml red">{ans.unknown ? i18n.t("dock.answer_unknown") : i18n.t("dock.submit_failed_edit", { reason: ans.error })}</div>;
  const foot = (page: boolean) => (
    <div className="st">
      {ans.error && (!multi || ans.unknown) ? (
        <button className="lk" onClick={retry}>
          {i18n.t("common.retry")}</button>
      ) : (
        <>
          <span>{page && multi ? (ticked.length ? i18n.t("dock.selected", { count: ticked.length }) : "") : i18n.t("dock.none_fit")}</span>
          <button className="lk" onClick={onTerminal}>
            {i18n.t("dock.other_terminal")}<ChevronRight />
          </button>
        </>
      )}
    </div>
  );
  return {
    label: askTitle(agent),
    text: item.text,
    busy: !!ans.busy,
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
      meta={metaOf(frame.more > 0 && i18n.t("dock.more", { count: frame.more }), p.progress)}
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

function PlanButtons({ remote, sid, full, shared, terminalOnly = false }: { remote: Remote; sid: string; full?: boolean; shared?: ReturnType<typeof usePlan>; terminalOnly?: boolean }) {
  // Claude Code has no "clear the context, then implement".
  const own = usePlan(remote, sid, terminalOnly);
  const plan = shared ?? own;
  const b = plan.busy;
  const entry = agentInfo(agentOf(remote.sessions.get(sid)));
  if (terminalOnly) return <div className="muted">{skeletonMessage()}</div>;
  return (
    <>
      {/* The full page offers Codex's third choice above the other two. */}
      {full && entry.planClearContext && (
        <div className="btns decide">
          <button className="btn" disabled={!!b} onClick={() => void plan.answer(PlanChoice.CLEAR_AND_IMPLEMENT)}>
            {b === String(PlanChoice.CLEAR_AND_IMPLEMENT) ? i18n.t("dock.submitting") : i18n.t("dock.plan_clear")}
          </button>
        </div>
      )}
      <div className="btns decide">
        <button className="btn" disabled={!!b} onClick={() => void plan.answer(PlanChoice.KEEP_PLANNING)}>
          {b === String(PlanChoice.KEEP_PLANNING) ? i18n.t("dock.submitting") : entry.planKeepLabel}
        </button>
        <button className="btn pri" disabled={!!b} onClick={() => void plan.answer(PlanChoice.IMPLEMENT)}>
          {b === String(PlanChoice.IMPLEMENT) ? i18n.t("dock.submitting") : entry.planApproveLabel}
        </button>
      </div>
      {plan.error && (
        <Banner kind="err" action={i18n.t("common.retry")} onAction={plan.retry}>
          {i18n.t("dock.submit_failed", { reason: plan.error })}
        </Banner>
      )}
    </>
  );
}

function PlanDock({ remote, sid, plan, frame, onFull }: { remote: Remote; sid: string; plan?: PlanProposed; frame: Frame; onFull: () => void }) {
  const state = usePlan(remote, sid, isSkeleton(plan));
  if (isSkeleton(plan)) return <div className="dock muted">{skeletonMessage()}</div>;
  return (
    <Pending
      frame={frame}
      label={i18n.t("list_state.plan")}
      brief=""
      busy={!!state.busy}
      meta={metaOf(frame.more > 0 && i18n.t("dock.more", { count: frame.more }))}
      extra={
        plan && (
          <button className="lk pending-full" onClick={onFull}>
            {i18n.t("markdown.expand")}<ChevronRight />
          </button>
        )
      }
      body={<div className="plan">{plan ? <Markdown text={plan.text} /> : i18n.t("dock.plan_unread")}</div>}
      actions={
        <>
          <PlanButtons remote={remote} sid={sid} shared={state} terminalOnly={isSkeleton(plan)} />
          {!isSkeleton(plan) && agentInfo(agentOf(remote.sessions.get(sid))).planNote && (
            <div className="ml">{agentInfo(agentOf(remote.sessions.get(sid))).planNote}</div>
          )}
        </>
      }
    />
  );
}

function formatTrustHint(text: string) {
  const parts = text.split(/(No, exit|Yes, I trust this folder)/g);
  return parts.map((part, i) =>
    part === "No, exit" || part === "Yes, I trust this folder" ? <b key={i}>{part}</b> : part,
  );
}

function PopupDock({ title, agent, frame, onTerminal }: { title: string; agent: Agent; frame: Frame; onTerminal: () => void }) {
  return (
    <Pending
      frame={frame}
      label={i18n.t("list_state.popup")}
      brief={title}
      meta={metaOf(frame.more > 0 && i18n.t("dock.more", { count: frame.more }), i18n.t("dock.agent_waiting", { agent: agentName(agent) }))}
      body={
        title && (
          <>
            <div className="q mono">{title}</div>
            {/* Claude Code's trust prompt starts on "No, exit": Enter alone
                ends the session (claude-code-notes §1). Trusting stays the
                user's choice, made in the terminal. */}
            {agentInfo(agent).trustFolderHint && /trust/i.test(title) && (
              <div className="y">
                {formatTrustHint(agentInfo(agent).trustFolderHint!)}
              </div>
            )}
          </>
        )
      }
      actions={
        <div className="btns">
          <button className="btn pri" onClick={onTerminal}>
            {i18n.t("dock.handle_terminal")}</button>
        </div>
      }
    />
  );
}

/** The least room, in pixels, an item needs to open in the dock: its line,
 * some of its content and its actions. With less it opens as a page. */
const DOCK_MIN_ROOM = 200;

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
 * line that opens as a page. An item that comes while the input holds a
 * draft (drafting: text or images not sent) starts as one line, so as not
 * to push the message aside; the input merely having focus does not count,
 * as on a computer it keeps it after every message. */
export function Dock({
  remote,
  sid,
  items,
  agent,
  drafting,
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
  drafting: boolean;
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
  if (!remote.dockSeen.has(key)) {
    remote.dockSeen.add(key);
    if (drafting) remote.dockClosed.add(key);
  }
  const open = !remote.dockClosed.has(key);
  // Typing a reason keeps it open whatever the room or the keyboard.
  const shown = open && (reasoning || room >= DOCK_MIN_ROOM);
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
    <DockBox style={style}>
      {card}
    </DockBox>
  );
}

/** What holds the dock: it opens to the card's height as it comes (rising
 * 12px, fading in) and closes up as it goes, so the chat above gives way
 * and takes the room back frame by frame; opening, folding and the next
 * item taking over ease its height. */
function DockBox({ style, children }: { style: CSSProperties; children: ReactNode }) {
  const ref = useRef<HTMLDivElement>(null);
  useHeightFlip(ref);
  useGrow(ref, { rise: true });
  return (
    <div className="mo-box" ref={ref} style={style}>
      {children}
    </div>
  );
}

/** A question on its own page, for when the dock has no room for it: the
 * dark card fills the page, its question and options scroll, the buttons
 * stay at the bottom. */
export function QuestionPage({ remote, sid, q, agent, title, onBack, onTerminal }: { remote: Remote; sid: string; q: QuestionAsked; agent: Agent; title: string; onBack: () => void; onTerminal: () => void }) {
  const p = useQuestion(remote, sid, q, agent, onTerminal);
  return (
    <div className="page dpage">
      <NavBar onBack={onBack} title={i18n.t("dock.answer_title")} sub={`${title} · ${agentName(agent)}`} />
      {p && (
        <div className="tc">
          <div className="k">
            <i />
            {p.label}
            {p.progress && <span>{p.progress}</span>}
          </div>
          <div className="pending-content">
            <div className="pending-scroll">
              <div className="q">{p.text}</div>
              {p.answers}
            </div>
            <div className="pending-actions">{p.actions(true)}</div>
          </div>
        </div>
      )}
    </div>
  );
}

/** An approval on its own page, for when the dock has no room: the dark
 * card at the bottom of a paper page, its command scrolling over the
 * buttons, which stay above the keyboard while a reason is written. */
export function ApprovalPage({ remote, sid, a, title, onBack }: { remote: Remote; sid: string; a: ApprovalRequested; agent: Agent; title: string; onBack: () => void }) {
  const now = useNow(1000);
  const deadline = deadlineOf(a);
  if (isSkeleton(a)) return <div className="page dpage"><NavBar onBack={onBack} title={i18n.t("approval.title")} sub={title} /><div className="tc"><div className="y">{skeletonMessage()}</div></div></div>;
  return (
    <div className="page dpage">
      <NavBar onBack={onBack} title={i18n.t("approval.details")} sub={`${title}${deadline ? " · " + minutesLeft(deadline, now) : ""}`} />
      <ApprovalCard remote={remote} sid={sid} a={a} docked />
    </div>
  );
}

/** The whole plan on a paper page, its choices fixed at the bottom. */
export function PlanPage({ remote, sid, plan, title, onBack }: { remote: Remote; sid: string; plan: PlanProposed; title: string; onBack: () => void }) {
  if (isSkeleton(plan)) return <div className="page"><NavBar onBack={onBack} title={i18n.t("dock.plan_full")} sub={title} /><div className="body chat muted">{skeletonMessage()}</div></div>;
  return (
    <div className="page">
      <NavBar onBack={onBack} title={i18n.t("dock.plan_full")} sub={i18n.t("dock.plan_title", { title })} />
      <div className="body chat">
        <div className="ai">
          <Markdown text={plan.text} />
        </div>
      </div>
      <div className="pfoot">
        <PlanButtons remote={remote} sid={sid} full terminalOnly={isSkeleton(plan)} />
      </div>
    </div>
  );
}
