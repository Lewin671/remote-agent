import { t as message } from "./i18n";
// @vitest-environment happy-dom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { create } from "@bufbuild/protobuf";
import { expect, it, vi } from "vitest";
import corpus from "../../testdata/projection/skeletons.json";
import { skeletonMessage, skeletonNote } from "./skeleton";
import {
  SessionApprovalDockViewSchema,
  SessionDockKind,
  SessionDockViewSchema,
  SessionPlanDockViewSchema,
  SessionQuestionDockViewSchema,
} from "./gen/remoteagent/core/v1/session_view_pb";
import { ApprovalPage, QuestionPage, PlanPage } from "./views/Dock";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

it("keeps skeleton constants aligned with testdata corpus", () => {
  expect(corpus.events).toHaveLength(20);
  expect(corpus.note).toBe(skeletonNote);
  expect(corpus.message).toBe(skeletonMessage());
});

it.each(["approval", "question", "empty-question", "plan"])("a %s skeleton offers no remote answer, including refusal", async kind => {
  const onSubmitCommand = vi.fn();
  const onAbandonCommand = vi.fn();
  const host = document.createElement("div"); document.body.append(host); const root = createRoot(host);
  try {
    const view = kind === "approval" ? (() => {
      const approval = create(SessionApprovalDockViewSchema, { approvalId: "a", isSkeleton: true });
      const dock = create(SessionDockViewSchema, {
        hasItem: true,
        kind: SessionDockKind.APPROVAL,
        itemId: "a",
        detail: { case: "approval", value: approval },
      });
      return <ApprovalPage sessionId="s-appr" dock={dock} approval={approval} title="session" onBack={() => undefined} onSubmitCommand={onSubmitCommand} onAbandonCommand={onAbandonCommand} />;
    })() : kind === "plan" ? (() => {
      const plan = create(SessionPlanDockViewSchema, { markdown: "", choices: [] });
      const dock = create(SessionDockViewSchema, {
        hasItem: true,
        kind: SessionDockKind.PLAN,
        itemId: "plan",
        detail: { case: "plan", value: plan },
      });
      return <PlanPage sessionId="s-appr" dock={dock} plan={plan} title="session" onBack={() => undefined} onSubmitCommand={onSubmitCommand} onAbandonCommand={onAbandonCommand} />;
    })() : (() => {
      const q = create(SessionQuestionDockViewSchema, {
        questionId: kind === "empty-question" ? "prefix…#0123456789abcdef" + skeletonNote : "q",
        question: kind === "empty-question" ? undefined : { questionText: skeletonMessage(), options: [] },
      });
      const dock = create(SessionDockViewSchema, {
        hasItem: true,
        kind: SessionDockKind.QUESTION,
        itemId: q.questionId,
        detail: { case: "question", value: q },
      });
      return <QuestionPage sessionId="s-appr" dock={dock} q={q} agentName="Codex" title="session" onBack={() => undefined} onTerminal={() => undefined} onSubmitCommand={onSubmitCommand} onAbandonCommand={onAbandonCommand} />;
    })();
    act(() => root.render(view));
    expect(host.textContent).toContain(skeletonMessage());
    for (const label of [message("approval.allow"), message("approval.deny"), message("approval.approve_execute"), message("dock.plan_keep"), "unsafe choice", message("dock.plan_clear")]) expect([...host.querySelectorAll("button")].some(b => b.textContent === label)).toBe(false);
    expect(onSubmitCommand).not.toHaveBeenCalled();
  } finally { act(() => root.unmount()); host.remove(); }
});
