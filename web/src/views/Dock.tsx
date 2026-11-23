import * as i18n from "../i18n";
// What a session waits for, docked above the input (design-system §6 停靠区):
// approvals, questions, plans and terminal popups, each in the dark card's
// shell. Only the first item is docked, open or folded to one dark line;
// what has no room opens as a page, and the buttons stay in view.

import { create } from "@bufbuild/protobuf";
import { useEffect, useRef, useState, type CSSProperties, type ReactNode } from "react";
import { approvalDisplay } from "../approval-display";
import { renderText } from "../core/text";
import {
  SessionCommandStatus,
  SessionDockKind,
  type SessionApprovalDockView,
  type SessionDockView,
  type SessionHeaderView,
  type SessionPlanDockView,
  type SessionQuestionDockView,
  type SessionTerminalPopupDockView,
} from "../gen/remoteagent/core/v1/session_view_pb";
import { PlanChoice } from "../gen/remoteagent/v1/local_pb";
import {
  CommandSchema,
  type Command,
} from "../gen/remoteagent/v1/remote_pb";
import { Check, ChevronRight } from "../icons";
import { Markdown } from "../markdown";
import { useGrow, useHeightFlip } from "../motion";
import { Banner, Exp, NavBar } from "../ui";
import { ApprovalCard } from "./ApprovalCard";

type Frame = { open: boolean; onToggle: () => void; more: number };

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

function metaOf(...parts: ReactNode[]): ReactNode {
  const shown = parts.filter((p) => p !== "" && p !== false && p != null);
  return shown.map((p, i) => (
    <span key={i}>
      {i > 0 && " · "}
      {p}
    </span>
  ));
}

function makeApprovalCommand(sessionId: string, dock: SessionDockView, a: SessionApprovalDockView, allow: boolean, reason: string, always: boolean): Command {
  return create(CommandSchema, {
    kind: {
      case: "resolveApproval",
      value: {
        sessionId,
        approvalId: a.approvalId,
        allow,
        message: reason,
        always,
        observedEventSeq: dock.observedSeq,
      },
    },
  });
}

function ApprovalDock({
  sessionId,
  dock,
  approval,
  canSend,
  frame,
  defaultDenying,
  reason,
  onReasonChange,
  onReasonOpen,
  reasoning,
  onSubmitCommand,
  onAbandonCommand,
}: {
  sessionId: string;
  dock: SessionDockView;
  approval: SessionApprovalDockView;
  canSend: boolean;
  frame: Frame;
  defaultDenying?: boolean;
  reason?: string;
  onReasonChange?: (v: string) => void;
  onReasonOpen: (open: boolean) => void;
  reasoning: boolean;
  onSubmitCommand: (slotKey: string, cmd: Command) => void;
  onAbandonCommand: (slotKey: string) => void;
}) {
  const lastCmd = useRef<Command | undefined>(undefined);
  const title = renderText(dock.title) || i18n.t("approval.title");
  if (dock.isSkeleton || approval.isSkeleton) {
    return (
      <div className="dock pending">
        <div className="tc line">
          <div className="k">{i18n.t("session.skeleton")}</div>
        </div>
      </div>
    );
  }
  const timeText = renderText(dock.timeLeftPlain);
  const cmd = dock.command;
  const slotKey = cmd?.slotKey ?? "";
  if (!frame.open) {
    return <FoldedLine frame={frame} label={title} brief={approvalDisplay(approval.commandPreview).replace(/\s+/g, " ")} busy={!!cmd?.busy} />;
  }
  const submit = (allow: boolean, why: string, always: boolean) => {
    const built = makeApprovalCommand(sessionId, dock, approval, allow, why, always);
    lastCmd.current = built;
    onSubmitCommand(slotKey, built);
  };
  const retry = () => {
    const built = lastCmd.current ?? makeApprovalCommand(sessionId, dock, approval, true, "", false);
    onSubmitCommand(slotKey, built);
  };
  const body = approval.body || approval.commandPreview || approval.toolName;
  return (
    <div className={"dock pending open" + (reasoning ? " reasoning" : "") + (cmd?.busy ? " submitting" : "")}>
      <ApprovalCard
        approvalId={approval.approvalId}
        kind={title}
        body={body}
        description={approval.description}
        truncated={approval.bodyTruncated}
        isSkeleton={approval.isSkeleton}
        canAlwaysAllow={approval.canAlwaysAllow}
        alwaysLabel={renderText(approval.alwaysLabel)}
        denyReasonPlaceholder={renderText(approval.denyReasonPlaceholder)}
        timeText={timeText}
        locked={!canSend}
        command={cmd}
        reason={reason}
        onReasonChange={onReasonChange}
        defaultDenying={defaultDenying}
        docked
        onDenying={onReasonOpen}
        onResolve={submit}
        onRetry={retry}
        onAbandon={slotKey ? () => onAbandonCommand(slotKey) : undefined}
        head={<OpenHead frame={frame} label={title} meta={metaOf(frame.more > 0 && i18n.t("dock.more", { count: frame.more }), timeText)} busy={!!cmd?.busy} />}
      />
    </div>
  );
}

export interface QuestionDraftState {
  key: string;
  ticked: number[];
  chosen: { index: number; options: number[] };
  dirty: boolean;
}

function useQuestionState(
  sessionId: string,
  dock: SessionDockView,
  q: SessionQuestionDockView,
  canSend: boolean,
  onTerminal: () => void,
  onSubmitCommand: (slotKey: string, cmd: Command) => void,
  onAbandonCommand: (slotKey: string) => void,
  questionDraft?: QuestionDraftState,
  onQuestionDraftChange?: (next: QuestionDraftState) => void,
) {
  const draftKey = `${q.questionId}:${q.activeIndex}`;
  const matched = questionDraft?.key === draftKey ? questionDraft : undefined;
  const [localDraft, setLocalDraft] = useState<QuestionDraftState>(() => ({
    key: draftKey,
    ticked: [],
    chosen: { index: -1, options: [] },
    dirty: false,
  }));
  const current = matched ?? (localDraft.key === draftKey ? localDraft : { key: draftKey, ticked: [], chosen: { index: -1, options: [] }, dirty: false });
  const update = (next: Partial<QuestionDraftState>) => {
    const merged: QuestionDraftState = { ...current, ...next, key: draftKey };
    if (onQuestionDraftChange) onQuestionDraftChange(merged);
    else setLocalDraft(merged);
  };
  const { ticked, chosen, dirty } = current;
  const lastCmd = useRef<Command | undefined>(undefined);

  useEffect(() => {
    lastCmd.current = undefined;
  }, [q.questionId, q.activeIndex]);

  const label = renderText(dock.title) || i18n.t("dock.answer_title");
  const cmd = dock.command;
  const slotKey = cmd?.slotKey ?? "";
  const busy = !!cmd?.busy;
  const hasError = !!cmd && (cmd.status === SessionCommandStatus.FAILED || cmd.status === SessionCommandStatus.UNKNOWN);
  const frozen = busy || !!cmd?.frozen || !canSend;

  const step = q.question;
  if (dock.isSkeleton || !step) {
    return {
      label,
      text: i18n.t("session.skeleton"),
      busy: false,
      progress: "",
      answers: null,
      actions: (_page: boolean) => null,
    };
  }

  const multi = step.multiSelect;
  const sent = chosen.index === q.activeIndex ? chosen.options : [];
  const errMsg = renderText(cmd?.errorText) || renderText(cmd?.statusText);

  const pick = (options: number[]) => {
    update({ chosen: { index: q.activeIndex, options }, dirty: false });
    const built = create(CommandSchema, {
      kind: {
        case: "answerQuestion",
        value: {
          sessionId,
          questionId: q.questionId,
          option: multi ? 0 : (options[0] ?? 0),
          questionIndex: q.activeIndex,
          options: multi ? options : [],
          observedEventSeq: dock.observedSeq,
        },
      },
    });
    lastCmd.current = built;
    onSubmitCommand(slotKey, built);
  };

  const retry = () => {
    const built = lastCmd.current ?? create(CommandSchema, {
      kind: {
        case: "answerQuestion",
        value: {
          sessionId,
          questionId: q.questionId,
          option: multi ? 0 : (sent[0] ?? 1),
          questionIndex: q.activeIndex,
          options: multi ? (sent.length ? sent : [1]) : [],
          observedEventSeq: dock.observedSeq,
        },
      },
    });
    update({ dirty: false });
    onSubmitCommand(slotKey, built);
  };

  const tick = (optNum: number) => {
    const base = hasError && !dirty && sent.length > 0 ? sent : ticked;
    const nextTicked = base.includes(optNum) ? base.filter((x) => x !== optNum) : [...base, optNum].sort((a, b) => a - b);
    update({ ticked: nextTicked, dirty: true });
  };

  const showError = hasError && !dirty;
  const shownTicks = frozen || showError ? sent : ticked;
  const progress = [renderText(q.progressText) || step.header, multi ? i18n.t("dock.multiselect") : ""].filter(Boolean).join(" · ");
  const answers = (
    <div className="answers">
      {step.options.map((o, idx) => {
        const num = o.optionNum || idx + 1;
        const on = multi ? shownTicks.includes(num) : sent.includes(num);
        const status = on && sent.includes(num) && (busy ? "sending" : showError ? "err" : "");
        return (
          <button
            key={num}
            className={"opt" + (on ? " on" : "") + (multi ? " multi" : "")}
            role={multi ? "checkbox" : undefined}
            aria-checked={multi ? on : undefined}
            disabled={multi ? frozen : busy || !canSend || (hasError && !cmd?.canRetry && !!cmd?.frozen)}
            onClick={() => (multi ? tick(num) : pick([num]))}
          >
            {multi && <span className={"box" + (on ? " on" : "")}>{on && <Check />}</span>}
            <div>
              {o.label}
              {status === "err" && !multi ? (
                <small className="err">{errMsg || i18n.t("dock.question_changed")}</small>
              ) : status === "sending" && !multi ? (
                <small>{i18n.t("dock.submitting")}</small>
              ) : (
                o.description && <small>{o.description}</small>
              )}
            </div>
            {o.recommended && <span className="tag">{i18n.t("dock.recommended")}</span>}
            {!multi && on && <Check className="ck" />}
          </button>
        );
      })}
    </div>
  );

  const submit = multi && !(showError && cmd?.frozen) && (
    <div className="btns">
      <button className="btn pri" disabled={busy || !canSend || ticked.length === 0} onClick={() => pick(ticked)}>
        {busy ? i18n.t("dock.submitting") : ticked.length ? i18n.t("dock.submit_selected", { count: ticked.length }) : i18n.t("dock.select_then_submit")}
      </button>
    </div>
  );

  const failed = showError && (multi || sent.length === 0) && <div className="ml red">{errMsg}</div>;

  const foot = (page: boolean) => (
    <div className="st">
      {showError && cmd?.canRetry ? (
        <>
          <button className="lk" onClick={retry}>
            {i18n.t("common.retry")}
          </button>
          {cmd.canAbandon && slotKey && (
            <button className="lk" onClick={() => onAbandonCommand(slotKey)}>
              {i18n.t("common.cancel")}
            </button>
          )}
        </>
      ) : (
        <>
          <span>{page && multi ? (ticked.length ? i18n.t("dock.selected", { count: ticked.length }) : "") : i18n.t("dock.none_fit")}</span>
          <button className="lk" onClick={onTerminal}>
            {i18n.t("dock.other_terminal")}
            <ChevronRight />
          </button>
        </>
      )}
    </div>
  );

  return {
    label,
    text: step.questionText,
    busy,
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

function QuestionDock({
  sessionId,
  dock,
  q,
  canSend,
  frame,
  questionDraft,
  onQuestionDraftChange,
  onTerminal,
  onSubmitCommand,
  onAbandonCommand,
}: {
  sessionId: string;
  dock: SessionDockView;
  q: SessionQuestionDockView;
  canSend: boolean;
  frame: Frame;
  questionDraft?: QuestionDraftState;
  onQuestionDraftChange?: (next: QuestionDraftState) => void;
  onTerminal: () => void;
  onSubmitCommand: (slotKey: string, cmd: Command) => void;
  onAbandonCommand: (slotKey: string) => void;
}) {
  const p = useQuestionState(sessionId, dock, q, canSend, onTerminal, onSubmitCommand, onAbandonCommand, questionDraft, onQuestionDraftChange);
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

function makePlanCommand(sessionId: string, dock: SessionDockView, plan: SessionPlanDockView, choice: PlanChoice): Command {
  return create(CommandSchema, {
    kind: {
      case: "answerPlan",
      value: {
        sessionId,
        choice,
        observedEventSeq: dock.observedSeq,
        observedScreenHash: plan.screenHash,
      },
    },
  });
}

function PlanButtons({
  sessionId,
  dock,
  plan,
  canSend,
  full,
  onSubmitCommand,
  onAbandonCommand,
}: {
  sessionId: string;
  dock: SessionDockView;
  plan: SessionPlanDockView;
  canSend: boolean;
  full?: boolean;
  onSubmitCommand: (slotKey: string, cmd: Command) => void;
  onAbandonCommand: (slotKey: string) => void;
}) {
  const [pickedChoice, setPickedChoice] = useState<PlanChoice | undefined>(undefined);
  const lastCmd = useRef<Command | undefined>(undefined);
  const cmd = dock.command;
  const slotKey = cmd?.slotKey ?? "";
  const busy = !!cmd?.busy;
  const hasError = !!cmd && (cmd.status === SessionCommandStatus.FAILED || cmd.status === SessionCommandStatus.UNKNOWN);
  const errMsg = renderText(cmd?.errorText) || renderText(cmd?.statusText);

  if (dock.isSkeleton || plan.choices.length === 0) return <div className="muted">{i18n.t("session.skeleton")}</div>;

  const locked = busy || !canSend || !!cmd?.frozen;
  const answer = (choice: PlanChoice) => {
    setPickedChoice(choice);
    const built = makePlanCommand(sessionId, dock, plan, choice);
    lastCmd.current = built;
    onSubmitCommand(slotKey, built);
  };
  const retry = () => {
    const built = lastCmd.current ?? makePlanCommand(sessionId, dock, plan, pickedChoice ?? PlanChoice.IMPLEMENT);
    onSubmitCommand(slotKey, built);
  };

  const clearChoice = full ? plan.choices.find((c) => c.choice === PlanChoice.CLEAR_AND_IMPLEMENT) : undefined;
  const mainChoices = plan.choices.filter((c) => c.choice !== PlanChoice.CLEAR_AND_IMPLEMENT);
  // Display keep planning before implement to match Web layout order.
  const sortedMain = [...mainChoices].sort((a, b) => {
    if (a.choice === b.choice) return 0;
    return a.choice === PlanChoice.KEEP_PLANNING ? -1 : 1;
  });

  return (
    <>
      {clearChoice && (
        <div className="btns decide">
          <button className="btn" disabled={locked} onClick={() => answer(clearChoice.choice)}>
            {busy && pickedChoice === clearChoice.choice ? i18n.t("dock.submitting") : renderText(clearChoice.label)}
          </button>
        </div>
      )}
      <div className="btns decide">
        {sortedMain.map((c) => (
          <button
            key={c.choice}
            className={"btn" + (c.recommended ? " pri" : "")}
            disabled={locked}
            onClick={() => answer(c.choice)}
          >
            {busy && pickedChoice === c.choice ? i18n.t("dock.submitting") : renderText(c.label)}
          </button>
        ))}
      </div>
      {hasError && (
        <Banner
          kind="err"
          action={cmd?.canRetry ? i18n.t("common.retry") : cmd?.canAbandon && slotKey ? i18n.t("common.cancel") : undefined}
          onAction={cmd?.canRetry ? retry : cmd?.canAbandon && slotKey ? () => onAbandonCommand(slotKey) : undefined}
        >
          {errMsg}
        </Banner>
      )}
    </>
  );
}

function PlanDock({
  sessionId,
  dock,
  plan,
  canSend,
  frame,
  onFull,
  onSubmitCommand,
  onAbandonCommand,
}: {
  sessionId: string;
  dock: SessionDockView;
  plan: SessionPlanDockView;
  canSend: boolean;
  frame: Frame;
  onFull: () => void;
  onSubmitCommand: (slotKey: string, cmd: Command) => void;
  onAbandonCommand: (slotKey: string) => void;
}) {
  const hasContent = !!plan.markdown || plan.choices.length > 0;
  if (dock.isSkeleton || !hasContent) return <div className="dock muted">{i18n.t("session.skeleton")}</div>;
  const note = renderText(plan.note);
  const title = renderText(dock.title) || i18n.t("list_state.plan");
  return (
    <Pending
      frame={frame}
      label={i18n.t("list_state.plan")}
      brief={title !== i18n.t("list_state.plan") ? title : ""}
      busy={!!dock.command?.busy}
      meta={metaOf(frame.more > 0 && i18n.t("dock.more", { count: frame.more }))}
      extra={
        plan.markdown ? (
          <button className="lk pending-full" onClick={onFull}>
            {i18n.t("markdown.expand")}
            <ChevronRight />
          </button>
        ) : undefined
      }
      body={<div className="plan">{plan.markdown ? <Markdown text={plan.markdown} /> : renderText(dock.summary) || i18n.t("dock.plan_unread")}</div>}
      actions={
        <>
          <PlanButtons
            sessionId={sessionId}
            dock={dock}
            plan={plan}
            canSend={canSend}
            onSubmitCommand={onSubmitCommand}
            onAbandonCommand={onAbandonCommand}
          />
          {note && <div className="ml">{note}</div>}
        </>
      }
    />
  );
}

function formatTrustHint(text: string) {
  const parts = text.split(/(No, exit|Yes, I trust this folder)/g);
  return parts.map((part, i) => (part === "No, exit" || part === "Yes, I trust this folder" ? <b key={i}>{part}</b> : part));
}

function PopupDock({
  dock,
  popup,
  agentName,
  frame,
  onTerminal,
}: {
  dock: SessionDockView;
  popup: SessionTerminalPopupDockView;
  agentName: string;
  frame: Frame;
  onTerminal: () => void;
}) {
  const title = renderText(popup.popupTitle) || renderText(dock.title);
  const hint = renderText(popup.hint) || (popup.isTrustFolder ? i18n.t("dock.trust_folder") : "");
  return (
    <Pending
      frame={frame}
      label={i18n.t("list_state.popup")}
      brief={title}
      meta={metaOf(frame.more > 0 && i18n.t("dock.more", { count: frame.more }), i18n.t("dock.agent_waiting", { agent: agentName }))}
      body={
        title ? (
          <>
            <div className="q mono">{title}</div>
            {hint && <div className="y">{formatTrustHint(hint)}</div>}
          </>
        ) : undefined
      }
      actions={
        <div className="btns">
          <button className="btn pri" disabled={!popup.canGoTerminal} onClick={onTerminal}>
            {i18n.t("dock.handle_terminal")}
          </button>
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

function dockKey(sid: string, dock: SessionDockView): string {
  const what =
    dock.detail.case === "plan"
      ? dock.detail.value.markdown
      : dock.detail.case === "terminalPopup"
        ? renderText(dock.detail.value.popupTitle)
        : "";
  return `${sid}|${dock.itemId}|${what}`;
}

export interface DockProps {
  sessionId: string;
  dock?: SessionDockView;
  header?: SessionHeaderView;
  drafting?: boolean;
  room?: number;
  height?: number;
  seenKeys?: Set<string>;
  closedKeys?: Set<string>;
  defaultDenying?: boolean;
  reason?: string;
  onReasonChange?: (approvalId: string, v: string) => void;
  questionDraft?: QuestionDraftState;
  onQuestionDraftChange?: (next: QuestionDraftState) => void;
  onExpand?: () => void;
  onDenyingChange?: (denying: boolean) => void;
  onOpenChange?: (open: boolean) => void;
  onDetail?: (approvalId: string) => void;
  onPlan?: () => void;
  onQuestion?: () => void;
  onTerminal: () => void;
  onSubmitCommand: (slotKey: string, cmd: Command) => void;
  onAbandonCommand: (slotKey: string) => void;
}

export function Dock({
  sessionId,
  dock,
  header,
  drafting = false,
  room = Infinity,
  height = 0,
  seenKeys,
  closedKeys,
  defaultDenying,
  reason,
  onReasonChange,
  questionDraft,
  onQuestionDraftChange,
  onExpand,
  onDenyingChange,
  onOpenChange,
  onDetail,
  onPlan,
  onQuestion,
  onTerminal,
  onSubmitCommand,
  onAbandonCommand,
}: DockProps) {
  const [, redraw] = useState(0);
  const localSeen = useRef<Set<string>>(new Set());
  const localClosed = useRef<Set<string>>(new Set());
  const [reasoning, setReasoning] = useState(false);
  const [externalNote, setExternalNote] = useState(false);
  const prevApprovalId = useRef("");
  const localResolveAt = useRef(0);

  const hasDock = !!dock?.hasItem;
  const key = hasDock ? dockKey(sessionId, dock!) : "";
  if (hasDock && !((seenKeys ?? localSeen.current).has(key))) {
    (seenKeys ?? localSeen.current).add(key);
    if (drafting) (closedKeys ?? localClosed.current).add(key);
  }
  const open = hasDock && !((closedKeys ?? localClosed.current).has(key));
  const shown = open && (reasoning || room >= DOCK_MIN_ROOM);
  const activeApprovalId = hasDock && dock!.kind === SessionDockKind.APPROVAL && dock!.detail.case === "approval" ? dock!.detail.value.approvalId : "";
  const isDenyingNow = shown && !!activeApprovalId && reasoning;

  useEffect(() => {
    if (prevApprovalId.current && prevApprovalId.current !== activeApprovalId) {
      if (reasoning && Date.now() - localResolveAt.current > 2000) {
        setExternalNote(true);
        setReasoning(false);
        const timer = setTimeout(() => setExternalNote(false), 1200);
        prevApprovalId.current = activeApprovalId;
        return () => clearTimeout(timer);
      }
      setReasoning(false);
    }
    prevApprovalId.current = activeApprovalId;
  }, [activeApprovalId, reasoning]);

  useEffect(() => {
    onDenyingChange?.(isDenyingNow);
    return () => onDenyingChange?.(false);
  }, [isDenyingNow, onDenyingChange]);

  useEffect(() => {
    onOpenChange?.(shown);
    return () => onOpenChange?.(false);
  }, [shown, onOpenChange]);

  const closed = closedKeys ?? localClosed.current;

  if (!hasDock) {
    if (externalNote) {
      return (
        <DockBox style={dockContentMax(room, height) === undefined ? {} : ({ "--content-max": `${dockContentMax(room, height)}px` } as CSSProperties)}>
          <div className="dock pending">
            <div className="tc line" role="status">
              <div className="k"><i />{i18n.t("approval.handled_in_terminal")}</div>
            </div>
          </div>
        </DockBox>
      );
    }
    return null;
  }

  const openAsPage = () => {
    if (dock.kind === SessionDockKind.APPROVAL && dock.detail.case === "approval") onDetail?.(dock.detail.value.approvalId);
    else if (dock.kind === SessionDockKind.PLAN) onPlan?.();
    else if (dock.kind === SessionDockKind.QUESTION) onQuestion?.();
    else onTerminal();
  };

  const frame: Frame = {
    open: shown,
    more: dock.moreCount,
    onToggle: () => {
      if (shown) closed.add(key);
      else if (room < DOCK_MIN_ROOM) return openAsPage();
      else {
        closed.delete(key);
        onExpand?.();
      }
      redraw((n) => n + 1);
    },
  };

  const max = dockContentMax(room, height);
  const style = (max === undefined ? {} : { "--content-max": `${max}px` }) as CSSProperties;
  const canSend = header?.capabilities?.canSend ?? true;

  let card: ReactNode = null;
  switch (dock.detail.case) {
    case "approval": {
      const a = dock.detail.value;
      card = (
        <ApprovalDock
          key={dock.itemId}
          sessionId={sessionId}
          dock={dock}
          approval={a}
          canSend={canSend}
          frame={frame}
          defaultDenying={defaultDenying}
          reason={reason}
          onReasonChange={onReasonChange ? (v) => onReasonChange(a.approvalId, v) : undefined}
          reasoning={reasoning}
          onReasonOpen={setReasoning}
          onSubmitCommand={(sk, cmd) => {
            localResolveAt.current = Date.now();
            onSubmitCommand(sk, cmd);
          }}
          onAbandonCommand={onAbandonCommand}
        />
      );
      break;
    }
    case "question":
      card = (
        <QuestionDock
          key={dock.itemId}
          sessionId={sessionId}
          dock={dock}
          q={dock.detail.value}
          canSend={canSend}
          frame={frame}
          questionDraft={questionDraft}
          onQuestionDraftChange={onQuestionDraftChange}
          onTerminal={onTerminal}
          onSubmitCommand={onSubmitCommand}
          onAbandonCommand={onAbandonCommand}
        />
      );
      break;
    case "plan":
      card = (
        <PlanDock
          key={dock.itemId}
          sessionId={sessionId}
          dock={dock}
          plan={dock.detail.value}
          canSend={canSend}
          frame={frame}
          onFull={() => onPlan?.()}
          onSubmitCommand={onSubmitCommand}
          onAbandonCommand={onAbandonCommand}
        />
      );
      break;
    case "terminalPopup":
      card = (
        <PopupDock
          key={dock.itemId}
          dock={dock}
          popup={dock.detail.value}
          agentName={header?.agentName ?? ""}
          frame={frame}
          onTerminal={onTerminal}
        />
      );
      break;
  }

  if (!card) return null;
  return (
    <DockBox style={style}>
      {externalNote && (
        <div className="dock pending">
          <div className="tc line" role="status">
            <div className="k"><i />{i18n.t("approval.handled_in_terminal")}</div>
          </div>
        </div>
      )}
      {card}
    </DockBox>
  );
}

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

export function QuestionPage({
  sessionId,
  dock,
  q,
  canSend = true,
  agentName,
  title,
  questionDraft,
  onQuestionDraftChange,
  onBack,
  onTerminal,
  onSubmitCommand,
  onAbandonCommand,
}: {
  sessionId: string;
  dock: SessionDockView;
  q: SessionQuestionDockView;
  canSend?: boolean;
  agentName: string;
  title: string;
  questionDraft?: QuestionDraftState;
  onQuestionDraftChange?: (next: QuestionDraftState) => void;
  onBack: () => void;
  onTerminal: () => void;
  onSubmitCommand: (slotKey: string, cmd: Command) => void;
  onAbandonCommand: (slotKey: string) => void;
}) {
  const p = useQuestionState(sessionId, dock, q, canSend, onTerminal, onSubmitCommand, onAbandonCommand, questionDraft, onQuestionDraftChange);
  return (
    <div className="page dpage">
      <NavBar onBack={onBack} title={i18n.t("dock.answer_title")} sub={`${title} · ${agentName}`} />
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
    </div>
  );
}

export function ApprovalPage({
  sessionId,
  dock,
  approval,
  canSend = true,
  title,
  reason,
  onReasonChange,
  onBack,
  onSubmitCommand,
  onAbandonCommand,
}: {
  sessionId: string;
  dock: SessionDockView;
  approval: SessionApprovalDockView;
  canSend?: boolean;
  title: string;
  reason?: string;
  onReasonChange?: (v: string) => void;
  onBack: () => void;
  onSubmitCommand: (slotKey: string, cmd: Command) => void;
  onAbandonCommand: (slotKey: string) => void;
}) {
  const lastCmd = useRef<Command | undefined>(undefined);
  const timeText = renderText(dock.timeLeftPlain);
  if (dock.isSkeleton || approval.isSkeleton) {
    return (
      <div className="page dpage">
        <NavBar onBack={onBack} title={i18n.t("approval.title")} sub={title} />
        <div className="tc">
          <div className="y">{i18n.t("session.skeleton")}</div>
        </div>
      </div>
    );
  }
  const slotKey = dock.command?.slotKey ?? "";
  return (
    <div className="page dpage">
      <NavBar onBack={onBack} title={i18n.t("approval.details")} sub={`${title}${timeText ? " · " + timeText : ""}`} />
      <ApprovalCard
        kind={renderText(dock.title) || i18n.t("approval.title")}
        body={approval.body || approval.commandPreview || approval.toolName}
        description={approval.description}
        truncated={approval.bodyTruncated}
        canAlwaysAllow={approval.canAlwaysAllow}
        alwaysLabel={renderText(approval.alwaysLabel)}
        denyReasonPlaceholder={renderText(approval.denyReasonPlaceholder)}
        timeText={timeText}
        locked={!canSend}
        command={dock.command}
        reason={reason}
        onReasonChange={onReasonChange}
        docked
        onResolve={(allow, why, always) => {
          const built = makeApprovalCommand(sessionId, dock, approval, allow, why, always);
          lastCmd.current = built;
          onSubmitCommand(slotKey, built);
        }}
        onRetry={() => {
          const built = lastCmd.current ?? makeApprovalCommand(sessionId, dock, approval, true, "", false);
          onSubmitCommand(slotKey, built);
        }}
        onAbandon={slotKey ? () => onAbandonCommand(slotKey) : undefined}
      />
    </div>
  );
}

export function PlanPage({
  sessionId,
  dock,
  plan,
  canSend = true,
  title,
  onBack,
  onSubmitCommand,
  onAbandonCommand,
}: {
  sessionId: string;
  dock: SessionDockView;
  plan: SessionPlanDockView;
  canSend?: boolean;
  title: string;
  onBack: () => void;
  onSubmitCommand: (slotKey: string, cmd: Command) => void;
  onAbandonCommand: (slotKey: string) => void;
}) {
  if (dock.isSkeleton || (!plan.markdown && plan.choices.length === 0)) {
    return (
      <div className="page">
        <NavBar onBack={onBack} title={i18n.t("dock.plan_full")} sub={title} />
        <div className="body chat muted">{i18n.t("session.skeleton")}</div>
      </div>
    );
  }
  return (
    <div className="page">
      <NavBar onBack={onBack} title={i18n.t("dock.plan_full")} sub={i18n.t("dock.plan_title", { title })} />
      <div className="body chat">
        <div className="ai">
          <Markdown text={plan.markdown} />
        </div>
      </div>
      <div className="pfoot">
        <PlanButtons
          sessionId={sessionId}
          dock={dock}
          plan={plan}
          canSend={canSend}
          full
          onSubmitCommand={onSubmitCommand}
          onAbandonCommand={onAbandonCommand}
        />
      </div>
    </div>
  );
}
