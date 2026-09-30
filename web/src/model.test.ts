import { create } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { describe, expect, it } from "vitest";
import { Agent, ApprovalDecision, ApprovalMode, EventSchema, InputStatus, QuestionAskedSchema, SessionStatus, ToolKind, TurnOutcome, type Event } from "./gen/remoteagent/v1/event_pb";
import { SessionSchema, type Session } from "./gen/remoteagent/v1/local_pb";
import { AgentInfoSchema, MachineMetaSchema } from "./gen/remoteagent/v1/remote_pb";
import {
  agentName,
  agentOf,
  ago,
  askTitle,
  decisionText,
  derive,
  dockItems,
  findModel,
  inheritConfig,
  machineAgents,
  minutesLeft,
  modeOf,
  noAutoMode,
  optionLabel,
  phaseOf,
  plain,
  previewOf,
  projects,
  queueReason,
  stateOf,
  statePriority,
  tailPath,
  timeline,
  titleOf,
  toolsSummary,
  when,
  type MachineLike,
  type SessionLike,
} from "./model";

function events(...kinds: object[]): Event[] {
  return kinds.map((kind, i) => create(EventSchema, { seq: BigInt(i + 1), kind: kind as never }));
}

function sess(info: Partial<Omit<Session, "$typeName">> = {}, extra: Partial<SessionLike> = {}): SessionLike {
  return { id: "s1", machineId: "m1", updatedMs: 1000n, info: create(SessionSchema, { status: SessionStatus.IDLE, cwd: "/Users/me/Code/foo", ...info }), ...extra };
}

const online: MachineLike = { id: "m1", online: true, lastSeenMs: 0n };
const offline: MachineLike = { id: "m1", online: false, lastSeenMs: 0n };

describe("state priority", () => {
  it("shows only the highest state", () => {
    // Everything at once: ended wins, then an offline machine, then the
    // table's order.
    const all = { status: SessionStatus.BLOCKED, pendingApprovals: 1, pendingQuestions: 1, planOpen: true };
    expect(stateOf(sess({ ...all, status: SessionStatus.EXITED }), offline).kind).toBe("ended");
    expect(stateOf(sess(all), offline).kind).toBe("offline");
    expect(stateOf(sess(all), online).kind).toBe("approval");
    expect(stateOf(sess({ ...all, pendingApprovals: 0 }), online).kind).toBe("question");
    expect(stateOf(sess({ ...all, pendingApprovals: 0, pendingQuestions: 0 }), online).kind).toBe("plan");
    expect(stateOf(sess({ status: SessionStatus.BLOCKED }), online).kind).toBe("popup");
    expect(stateOf(sess({ status: SessionStatus.STARTING }), online).kind).toBe("starting");
    expect(stateOf(sess({ status: SessionStatus.RUNNING }), online).kind).toBe("running");
    expect(stateOf(sess(), online).kind).toBe("idle");
    // A machine not listed yet is not known to be off.
    expect(stateOf(sess({ status: SessionStatus.RUNNING })).kind).toBe("running");
  });

  it("writes a state word next to the colour", () => {
    expect(stateOf(sess({ pendingQuestions: 1 }), online)).toEqual({ kind: "question", tone: "wait", text: "等你回答" });
    expect(stateOf(sess({ status: SessionStatus.EXITED })).text).toBe("已结束");
    expect(stateOf(sess(), offline)).toEqual({ kind: "offline", tone: "off", text: "电脑离线" });
    expect(statePriority.slice(0, 2)).toEqual(["ended", "offline"]);
  });

  it("previews by state", () => {
    const d = derive(events({ case: "approvalRequested", value: { approvalId: "a", command: "go test ./...\n-count=1" } }));
    const s = sess({ pendingApprovals: 1, preview: "✓ ls" });
    expect(previewOf(s, stateOf(s, online), { approval: d.approvals.get("a") })).toBe("请求执行 go test ./... -count=1");
    const q = sess({ pendingQuestions: 1 });
    expect(previewOf(q, stateOf(q, online), { question: create(QuestionAskedSchema, {}) })).toBe("等你回答");
    const q2 = create(QuestionAskedSchema, { questions: [{ text: "大版本也一起升吗？" }] });
    expect(previewOf(q, stateOf(q, online), { question: q2 })).toBe("大版本也一起升吗？");
    const idle = sess({ preview: "改了 `a.go`，见 [文档](x)", title: "t" });
    expect(previewOf(idle, stateOf(idle, online))).toBe("改了 a.go，见 文档");
    const fresh = sess();
    expect(previewOf(fresh, stateOf(fresh, online))).toBe("还没发消息");
  });
});

describe("session titles", () => {
  it("uses the title, else the folder and start time", () => {
    expect(titleOf(sess({ title: "修复 `登录` 超时" }))).toBe("修复 登录 超时");
    const start = new Date(2026, 8, 29, 11, 25).getTime();
    expect(titleOf(sess({ cwd: "/Users/me/Code/billing", createdAt: timestampFromMs(start) }))).toBe("billing · 11:25");
  });
});

describe("dock", () => {
  it("docks in the state table's order", () => {
    const d = derive(
      events(
        { case: "questionAsked", value: { questionId: "q", questions: [{ text: "升级吗？" }] } },
        { case: "approvalRequested", value: { approvalId: "a1", command: "make migrate" } },
        { case: "approvalRequested", value: { approvalId: "a2", command: "rm -rf build" } },
        { case: "planProposed", value: { text: "1. do it" } },
      ),
    );
    const s = sess({ status: SessionStatus.BLOCKED, statusDetail: "Trust this folder?", planOpen: true, pendingApprovals: 2, pendingQuestions: 1 });
    expect(dockItems(s, d, online).map((x) => x.id)).toEqual(["a:a1", "a:a2", "q:q", "plan", "popup"]);
    // Handled items leave; the next one moves up.
    const after = derive(
      events(
        { case: "approvalRequested", value: { approvalId: "a1", command: "make migrate" } },
        { case: "approvalResolved", value: { approvalId: "a1", decision: 1 } },
        { case: "approvalRequested", value: { approvalId: "a2", command: "rm -rf build" } },
      ),
    );
    expect(dockItems(sess({ pendingApprovals: 1 }), after, online).map((x) => x.id)).toEqual(["a:a2"]);
  });

  it("gives way to the status card", () => {
    const d = derive(events({ case: "approvalRequested", value: { approvalId: "a" } }));
    expect(dockItems(sess({ pendingApprovals: 1 }), d, offline)).toEqual([]);
    expect(dockItems(sess({ pendingApprovals: 1, status: SessionStatus.EXITED }), d, online)).toEqual([]);
  });
});

describe("message phases", () => {
  it("follows a message from sending to the CLI", () => {
    expect(phaseOf({ status: "sending" })).toBe("sending");
    expect(phaseOf({ status: "sent" })).toBe("delivered");
    expect(phaseOf({ status: "sent" }, InputStatus.QUEUED)).toBe("queued");
    expect(phaseOf({ status: "sent" }, InputStatus.SUBMITTED)).toBe("done");
    expect(phaseOf({ status: "failed" })).toBe("failed");
    // The daemon's word beats a failed call: it arrived after all.
    expect(phaseOf({ status: "failed" }, InputStatus.QUEUED)).toBe("queued");
    // Typed into the terminal but not confirmed: never offered for retry.
    expect(phaseOf({ status: "sent" }, InputStatus.UNCONFIRMED)).toBe("unconfirmed");
    expect(phaseOf({ status: "sent" }, InputStatus.CANCELED)).toBe("canceled");
  });

  it("says why queued messages wait", () => {
    const none = derive([]);
    const running = derive(events({ case: "turnStarted", value: {} }));
    const asking = derive(events({ case: "turnStarted", value: {} }, { case: "questionAsked", value: { questionId: "q" } }));
    expect(queueReason(sess({ status: SessionStatus.RUNNING }), running)).toBe("本轮结束后发");
    expect(queueReason(sess(), asking)).toBe("答完再发");
    expect(queueReason(sess({ status: SessionStatus.BLOCKED }), none)).toBe("弹窗处理完再发");
    expect(queueReason(sess({ inputWait: "draft" }), none)).toBe("终端里有没发出的草稿，发完或清掉后再发");
    expect(queueReason(sess({ inputWait: "unrecognized" }), running)).toBe("认不出输入框，已暂停");
  });
});

describe("tools", () => {
  it("pairs completions by call ID when the CLI gives one", () => {
    const d = derive(
      events(
        { case: "turnStarted", value: {} },
        { case: "toolStarted", value: { title: "Read a.go", callId: "t1" } },
        { case: "toolStarted", value: { title: "Read b.go", callId: "t2" } },
        { case: "toolStarted", value: { title: "Grep x", callId: "t3" } },
        { case: "toolCall", value: { title: "Read b.go", callId: "t2" } },
      ),
    );
    expect(d.tools.map((t) => t.title)).toEqual(["Read a.go", "Grep x"]);
  });

  it("pairs in order without call IDs", () => {
    const d = derive(
      events(
        { case: "turnStarted", value: {} },
        { case: "toolStarted", value: { title: "go test" } },
        { case: "toolStarted", value: { title: "go vet" } },
        // Codex's completed call has its own ID, which no start carries.
        { case: "toolCall", value: { title: "go test", callId: "call_9" } },
      ),
    );
    expect(d.tools.map((t) => t.title)).toEqual(["go vet"]);
  });

  it("folds runs of completed calls", () => {
    const ok = (title: string, kind = ToolKind.COMMAND) => ({ case: "toolCall", value: { title, kind, status: "completed" } });
    const list = events(
      { case: "userMessage", value: { text: "hi" } },
      ok("ls"),
      { case: "tokenUsage", value: {} }, // draws nothing: does not break the run
      ok("cat a"),
      ok("go vet"),
      { case: "agentMessage", value: { text: "done" } },
      ok("edit a.go", ToolKind.FILE_CHANGE),
      { case: "toolCall", value: { title: "go build", kind: ToolKind.COMMAND, status: "failed", exitCode: 1 } },
      { case: "turnEnded", value: { outcome: TurnOutcome.COMPLETED, durationMs: 252_000n } },
    );
    const items = timeline(list, derive(list));
    expect(items.map((i) => i.kind + (i.kind === "tools" ? i.calls.length : ""))).toEqual(["event", "tools3", "event", "event", "event", "turn"]);
    const tools = items[1];
    expect(tools.kind === "tools" && toolsSummary(tools.calls)).toBe("运行了 3 条命令");
  });
});

describe("agents", () => {
  it("treats sessions without an agent as Codex", () => {
    expect(agentOf(sess())).toBe(Agent.CODEX);
    expect(agentOf(sess({ agent: Agent.CLAUDE }))).toBe(Agent.CLAUDE);
    expect(agentName(agentOf(sess({ agent: Agent.UNSPECIFIED })))).toBe("Codex");
  });

  it("names the CLI in the wording", () => {
    expect(askTitle(Agent.CODEX)).toBe("Codex 在问你");
    expect(askTitle(Agent.CLAUDE)).toBe("Claude Code 在问你");
  });

  it("offers only the CLIs a machine has", () => {
    const meta = (...agents: Agent[]) => create(MachineMetaSchema, { agents: agents.map((agent) => create(AgentInfoSchema, { agent })) });
    expect(machineAgents({ ...online, meta: meta() })).toEqual([Agent.CODEX]);
    expect(machineAgents(online)).toEqual([Agent.CODEX]);
    expect(machineAgents({ ...online, meta: meta(Agent.CODEX, Agent.CLAUDE) })).toEqual([Agent.CODEX, Agent.CLAUDE]);
    expect(machineAgents({ ...online, meta: meta(Agent.CLAUDE) })).toEqual([Agent.CLAUDE]);
  });
});

describe("new sessions follow the project", () => {
  const ctx = { approvalsReviewer: "auto_review", approvalPolicy: "on-request", sandbox: "workspace-write", collaborationMode: "default" };
  const machines = (m1: boolean, m2: boolean) =>
    new Map<string, MachineLike>([
      ["m1", { id: "m1", online: m1, lastSeenMs: 0n }],
      ["m2", { id: "m2", online: m2, lastSeenMs: 0n }],
    ]);

  it("copies the latest session", () => {
    const list = [
      sess({ model: "old", ...ctx }, { id: "a", machineId: "m2", updatedMs: 1n }),
      sess({ model: "gpt-6-astra", effort: "medium", agent: Agent.CLAUDE, ...ctx }, { id: "b", updatedMs: 5n }),
    ];
    const c = inheritConfig(projects(list)[0], machines(true, true));
    expect(c).toMatchObject({ machineId: "m1", agent: Agent.CLAUDE, model: "gpt-6-astra", effort: "medium", mode: ApprovalMode.AUTO, cwd: "/Users/me/Code/foo" });
    expect(c?.from?.id).toBe("b");
  });

  it("does not copy full access or plan", () => {
    const full = sess({ ...ctx, approvalsReviewer: "user", approvalPolicy: "never", sandbox: "danger-full-access" });
    expect(inheritConfig(projects([full])[0], machines(true, true))?.mode).toBe(ApprovalMode.UNSPECIFIED);
    const plan = sess({ ...ctx, collaborationMode: "plan" });
    expect(inheritConfig(projects([plan])[0], machines(true, true))?.mode).toBe(ApprovalMode.UNSPECIFIED);
  });

  it("moves to another machine of the project with its defaults", () => {
    const list = [sess({ model: "x", ...ctx }, { id: "a", machineId: "m2", updatedMs: 1n }), sess({ model: "y", ...ctx }, { id: "b", updatedMs: 5n })];
    expect(inheritConfig(projects(list)[0], machines(false, true))).toMatchObject({ machineId: "m2", model: "", effort: "", mode: ApprovalMode.UNSPECIFIED });
  });

  it("picks a CLI the fallback machine has", () => {
    const meta = (...agents: Agent[]) => create(MachineMetaSchema, { agents: agents.map((agent) => create(AgentInfoSchema, { agent })) });
    const ms = new Map<string, MachineLike>([
      ["m1", { id: "m1", online: false, lastSeenMs: 0n }],
      ["m2", { id: "m2", online: true, lastSeenMs: 0n, meta: meta(Agent.CLAUDE) }],
    ]);
    // The latest session ran Codex on m1, which is offline; m2 only has
    // Claude Code.
    const list = [sess({ ...ctx }, { id: "a", machineId: "m2", updatedMs: 1n }), sess({ ...ctx }, { id: "b", updatedMs: 5n })];
    expect(inheritConfig(projects(list)[0], ms)).toMatchObject({ machineId: "m2", agent: Agent.CLAUDE });
  });

  it("has nothing to copy for a new project", () => {
    expect(inheritConfig(undefined, machines(true, true))).toBeUndefined();
  });
});

describe("approval modes", () => {
  it("prefers Session.mode, for either CLI", () => {
    expect(modeOf(sess({ mode: ApprovalMode.AUTO, approvalsReviewer: "user", approvalPolicy: "on-request" }))?.text).toBe("自动");
    expect(modeOf(sess({ agent: Agent.CLAUDE, mode: ApprovalMode.FULL_ACCESS, permissionMode: "bypassPermissions" }))).toMatchObject({ text: "完全放行", mode: ApprovalMode.FULL_ACCESS });
  });

  it("shows a Claude Code mode outside the four by its own name", () => {
    expect(modeOf(sess({ agent: Agent.CLAUDE, permissionMode: "acceptEdits" }))).toEqual({ cls: "", text: "acceptEdits", mode: ApprovalMode.UNSPECIFIED });
  });

  it("falls back to Codex's approval fields for older daemons", () => {
    expect(modeOf(sess({ approvalsReviewer: "auto_review", approvalPolicy: "on-request", sandbox: "workspace-write", collaborationMode: "default" }))?.text).toBe("自动");
  });

  it("knows haiku has no auto mode", () => {
    expect(noAutoMode(sess({ agent: Agent.CLAUDE, model: "claude-haiku-4-5-20251001" }))).toBe(true);
    expect(noAutoMode(sess({ agent: Agent.CLAUDE, model: "claude-sonnet-5-5" }))).toBe(false);
  });

  it("notes Claude Code mode changes made in the terminal, not model aliases set here", () => {
    const ctx = (permissionMode: string, model = "claude-haiku-4-5-20251001") => ({ case: "turnContext", value: { permissionMode, model } });
    const d = derive(
      events(
        ctx("default"),
        ctx("acceptEdits", ""), // Shift+Tab in the terminal, read off the screen
        { case: "settingsChanged", value: { model: "sonnet" } }, // switched here, by alias
        ctx("acceptEdits", "claude-sonnet-5-5"),
        ctx("plan", "claude-sonnet-5-5"),
      ),
    );
    expect([...d.terminalChanges.entries()]).toEqual([
      [2n, "模式切到了 acceptEdits"],
      [5n, "模式切到了 计划"],
    ]);
  });

  it("records approvals answered in the terminal", () => {
    expect(decisionText(ApprovalDecision.TERMINAL)).toBe("已在终端处理");
  });
});

describe("models", () => {
  const list = [{ slug: "opus" }, { slug: "sonnet" }, { slug: "haiku" }, { slug: "gpt-6-astra" }];
  it("finds Claude's alias for the full model ID a session reports", () => {
    expect(findModel(list, "claude-sonnet-5-5")?.slug).toBe("sonnet");
    expect(findModel(list, "claude-haiku-4-5-20251001")?.slug).toBe("haiku");
    expect(findModel(list, "sonnet")?.slug).toBe("sonnet");
  });
  it("matches Codex slugs exactly", () => {
    expect(findModel(list, "gpt-6-astra")?.slug).toBe("gpt-6-astra");
    expect(findModel(list, "gpt-6-sol")).toBeUndefined();
    expect(findModel(list, "")).toBeUndefined();
  });
});

describe("text", () => {
  it("makes one-line previews", () => {
    expect(plain("改了 `a.go`，见 [文档](x)")).toBe("改了 a.go，见 文档");
    expect(plain("## 结论\n- 第一点\n1. 第二点")).toBe("结论 第一点 第二点");
    expect(optionLabel("Go (Recommended)")).toEqual({ text: "Go", recommended: true });
    expect(optionLabel("Python")).toEqual({ text: "Python", recommended: false });
  });

  it("formats times like the mockup", () => {
    const now = new Date(2026, 8, 29, 9, 41).getTime();
    expect(when(now - 20_000, now)).toBe("刚刚");
    expect(when(now - 3 * 60_000, now)).toBe("3 分钟前");
    expect(when(new Date(2026, 8, 29, 8, 12).getTime(), now)).toBe("8:12");
    expect(when(new Date(2026, 8, 28, 20, 0).getTime(), now)).toBe("昨天");
    expect(when(new Date(2026, 8, 27, 20, 0).getTime(), now)).toBe("9月27日");
    expect(ago(new Date(2026, 8, 29, 7, 30).getTime(), now)).toBe("2 小时前");
    expect(minutesLeft(now + 7.5 * 60_000, now)).toBe("剩 8 分钟");
    expect(minutesLeft(now - 1, now)).toBe("已超时");
  });

  it("keeps the end of long paths", () => {
    expect(tailPath("/Users/me/Code/ai-bot/remote-agent", 24)).toBe("…/ai-bot/remote-agent");
    expect(tailPath("/Users/me/Code/foo")).toBe("~/Code/foo");
  });
});

describe("events", () => {
  it("tells terminal-typed messages apart", () => {
    const d = derive(
      events(
        { case: "userMessage", value: { text: "typed at the computer" } },
        { case: "inputState", value: { requestId: "r", status: InputStatus.SUBMITTED } },
        { case: "userMessage", value: { text: "sent from the phone" } },
      ),
    );
    expect([...d.typedLocally]).toEqual([1n]);
  });

  it("matches a message to its submitted state in either CLI's order", () => {
    // Claude Code records the message, then its submitted state.
    const claude = derive(
      events(
        { case: "inputState", value: { requestId: "a", status: InputStatus.QUEUED } },
        { case: "turnStarted", value: {} },
        { case: "userMessage", value: { text: "sent from the phone" } },
        { case: "inputState", value: { requestId: "a", status: InputStatus.SUBMITTED } },
        { case: "turnEnded", value: {} },
        { case: "userMessage", value: { text: "typed at the computer" } },
      ),
      Agent.CLAUDE,
    );
    expect([...claude.typedLocally]).toEqual([6n]);
    // A turn started at the computer takes a message sent from here: the
    // enqueue record confirms it before the message itself is recorded.
    const queued = derive(
      events(
        { case: "turnStarted", value: {} },
        { case: "userMessage", value: { text: "typed at the computer" } },
        { case: "inputState", value: { requestId: "q", status: InputStatus.QUEUED } },
        { case: "inputState", value: { requestId: "q", status: InputStatus.SUBMITTED } },
        { case: "userMessage", value: { text: "sent from the phone" } },
      ),
      Agent.CLAUDE,
    );
    expect([...queued.typedLocally]).toEqual([2n]);
    // Codex marks it submitted first; a message typed just before stays
    // the computer's.
    const codex = derive(
      events(
        { case: "userMessage", value: { text: "typed at the computer" } },
        { case: "inputState", value: { requestId: "b", status: InputStatus.SUBMITTED } },
        { case: "userMessage", value: { text: "sent from the phone" } },
      ),
      Agent.CODEX,
    );
    expect([...codex.typedLocally]).toEqual([1n]);
  });

  it("does not take leaving plan mode after a plan for a terminal switch", () => {
    const ctx = (permissionMode: string) => ({ case: "turnContext", value: { permissionMode } });
    const d = derive(events(ctx("default"), ctx("plan"), { case: "planProposed", value: { text: "1." } }, ctx("default"), ctx("plan"), ctx("default")));
    // Entering plan (2) and leaving it with no plan (6) were switches;
    // leaving after the plan (4) was the plan's answer.
    expect([...d.terminalChanges.keys()]).toEqual([2n, 5n, 6n]);
  });

  it("notes mode changes made in the terminal", () => {
    const ctx = (reviewer: string, collaboration = "default", model = "m", effort = "low") => ({
      case: "turnContext",
      value: { approvalsReviewer: reviewer, approvalPolicy: "on-request", sandbox: "workspace-write", collaborationMode: collaboration, model, effort },
    });
    const d = derive(
      events(
        ctx("user"),
        ctx("auto_review"), // switched in the terminal
        { case: "settingsChanged", value: { mode: ApprovalMode.ASK } },
        ctx("user"), // switched from the client: already recorded
        ctx("user", "plan", "m", "medium"), // plan mode's own effort is not a model change
      ),
    );
    expect([...d.terminalChanges.entries()]).toEqual([
      [2n, "模式切到了 自动"],
      [5n, "模式切到了 计划"],
    ]);
  });

  it("notes a model changed in the terminal after a switch from here", () => {
    const ctx = (model: string, effort: string) => ({ case: "turnContext", value: { approvalsReviewer: "user", approvalPolicy: "on-request", model, effort } });
    const d = derive(
      events(
        ctx("gpt-6-astra", "medium"),
        { case: "settingsChanged", value: { model: "gpt-6-sol", effort: "high" } },
        // Changed again at the computer before the next turn.
        ctx("gpt-6-luna", "low"),
        { case: "settingsChanged", value: { model: "gpt-6-sol", effort: "high" } },
        ctx("gpt-6-sol", "high"), // as chosen here: nothing to note
      ),
    );
    expect([...d.terminalChanges.entries()]).toEqual([[3n, "模型切到了 gpt-6-luna · low"]]);
  });

  it("does not take a mode-only context's old model for a terminal switch", () => {
    const ctx = (permissionMode: string, model: string) => ({ case: "turnContext", value: { permissionMode, model } });
    const d = derive(
      events(
        ctx("default", "claude-haiku-4-5-20251001"),
        { case: "settingsChanged", value: { model: "sonnet", effort: "medium" } },
        // The mode changed before the next reply; the context still names
        // the model it had.
        ctx("acceptEdits", "claude-haiku-4-5-20251001"),
        ctx("acceptEdits", "claude-sonnet-5-5"), // the switch, confirmed
        ctx("acceptEdits", "claude-opus-5-5"), // then changed at the computer
      ),
      Agent.CLAUDE,
    );
    expect([...d.terminalChanges.entries()]).toEqual([
      [3n, "模式切到了 acceptEdits"],
      [5n, "模型切到了 claude-opus-5-5"],
    ]);
  });

  it("records context use around a compaction", () => {
    const d = derive(
      events(
        { case: "tokenUsage", value: { lastTotalTokens: 86n, contextWindow: 100n } },
        { case: "settingsChanged", value: { compacted: true } },
        { case: "tokenUsage", value: { lastTotalTokens: 14n, contextWindow: 100n } },
      ),
    );
    expect(d.compactions.get(2n)).toEqual({ before: 86, after: 14 });
  });
});
