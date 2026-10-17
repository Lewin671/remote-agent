// @vitest-environment happy-dom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { create, fromJson, type JsonValue } from "@bufbuild/protobuf";
import { expect, it, vi } from "vitest";
import corpus from "../../testdata/projection/skeletons.json";
import { isSkeleton, skeletonMessage, skeletonNote } from "./skeleton";
import { Agent, EventSchema, ApprovalRequestedSchema, QuestionAskedSchema, PlanProposedSchema } from "./gen/remoteagent/v1/event_pb";
import { derive, timeline } from "./model";
import { DemoRemote } from "./dev/demo";
import { ApprovalPage, QuestionPage, PlanPage } from "./views/Dock";
import { CommandResultSchema } from "./gen/remoteagent/v1/remote_pb";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

it("keeps all 20 skeleton kinds visible and reducible", () => {
  const events = corpus.events.map(e => fromJson(EventSchema, e as unknown as JsonValue));
  expect(events).toHaveLength(20);
  expect(corpus.note).toBe(skeletonNote);
  expect(corpus.message).toBe(skeletonMessage);
  for (const agent of [Agent.CODEX, Agent.CLAUDE]) {
    const rows = timeline(events, derive(events, agent));
    expect(rows).toHaveLength(20);
    expect(rows.every(row => row.kind === "event" && isSkeleton(row.event))).toBe(true);
  }
});

it.each(["approval", "question", "empty-question", "plan"])("a %s skeleton offers no remote answer, including refusal", async kind => {
  const remote = DemoRemote.create(new URLSearchParams()); remote.start();
  const command = vi.spyOn(remote, "command").mockResolvedValue(create(CommandResultSchema));
  const host = document.createElement("div"); document.body.append(host); const root = createRoot(host);
  const props = { remote, sid: "s-appr", title: "session", onBack: () => undefined };
  try {
    const view = kind === "approval" ? <ApprovalPage {...props} agent={Agent.CODEX} a={create(ApprovalRequestedSchema, { approvalId: "a", description: skeletonNote })} />
      : kind === "plan" ? <PlanPage {...props} plan={create(PlanProposedSchema, { text: skeletonNote, callId: "plan" })} />
      : <QuestionPage {...props} agent={Agent.CODEX} onTerminal={() => undefined} q={create(QuestionAskedSchema, { questionId: kind === "empty-question" ? "prefix…#0123456789abcdef" + skeletonNote : "q", questions: kind === "empty-question" ? [] : [{ id: "body", text: skeletonNote, options: [{ label: "unsafe choice" }] }] })} />;
    act(() => root.render(view));
    expect(host.textContent).toContain(skeletonMessage);
    for (const label of ["允许", "不允许", "批准执行", "继续规划", "unsafe choice", "清空上下文后执行"]) expect([...host.querySelectorAll("button")].some(b => b.textContent === label)).toBe(false);
    expect(command).not.toHaveBeenCalled();
  } finally { act(() => root.unmount()); host.remove(); remote.stop(); }
});
