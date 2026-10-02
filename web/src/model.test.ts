import { create } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { describe, expect, it } from "vitest";
import { Agent, ApprovalDecision, ApprovalMode, EventSchema, InputStage, ToolBackground, InputStatus, OriginKind, QuestionAskedSchema, SessionStatus, ToolKind, TurnOutcome, type Event } from "./gen/remoteagent/v1/event_pb";
import { SessionSchema, type Session } from "./gen/remoteagent/v1/local_pb";
import { AgentInfoSchema, DefaultModeSource, MachineMetaSchema } from "./gen/remoteagent/v1/remote_pb";
import {
  agentName,
  agentOf,
  ago,
  askTitle,
  decisionText,
  derive,
  dockItems,
  findModel,
  modelLabel,
  defaultModeOf,
  inheritConfig,
  modeLabel,
  inputFailure,
  newConfigIssue,
  switchAgent,
  switchMachine,
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
  queuedIds,
  shortenPath,
  shortenPaths,
  stateOf,
  statePriority,
  tailPath,
  timeline,
  titleOf,
  toolsSummary,
  toolClock,
  backgroundNote,
  when,
  type MachineLike,
  type SessionLike,
  contextText,
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
    expect(queueReason(sess({ status: SessionStatus.RUNNING }), running)).toBe("本轮结束后发送");
    expect(queueReason(sess(), asking)).toBe("就绪后发送");
    expect(queueReason(sess({ status: SessionStatus.BLOCKED }), none)).toBe("就绪后发送");
    expect(queueReason(sess({ inputWait: "draft" }), none)).toBe("终端里有没发出的草稿，发完或清掉后再发");
    expect(queueReason(sess({ inputWait: "unrecognized" }), running)).toBe("认不出输入框，已暂停");
  });
});

describe("messages the session ended before sending", () => {
  it("are neither queued nor canceled by the user", () => {
    const list = events(
      { case: "inputState", value: { requestId: "a", status: InputStatus.QUEUED } },
      { case: "inputState", value: { requestId: "b", status: InputStatus.QUEUED } },
      { case: "inputState", value: { requestId: "c", status: InputStatus.QUEUED } },
      { case: "inputState", value: { requestId: "b", status: InputStatus.CANCELED } }, // the user's own cancel
      { case: "inputState", value: { requestId: "a", status: InputStatus.CANCELED, reason: "exited" } },
      { case: "inputState", value: { requestId: "c", status: InputStatus.CANCELED, reason: "exited" } },
      { case: "sessionState", value: { status: SessionStatus.EXITED } },
    );
    const d = derive(list);
    expect([...d.unsent].sort()).toEqual(["a", "c"]);
    const rows = timeline(list, d).map((it) => (it.kind === "event" && it.event.kind.case === "inputState" ? `${it.event.kind.value.requestId}:${it.event.kind.value.status}` : it.kind));
    // Each unsent message shows once, where it was queued; b's cancel as before.
    expect(rows).toEqual([`a:${InputStatus.QUEUED}`, `c:${InputStatus.QUEUED}`, `b:${InputStatus.CANCELED}`, "event"]);
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

describe("tools while they run (v2)", () => {
  const started = (callId: string, title: string, itemType = "Bash") => ({ case: "toolStarted", value: { title, callId, itemType, kind: ToolKind.COMMAND } });
  const done = (callId: string, title: string, ok = true) => ({ case: "toolCall", value: { title, callId, kind: ToolKind.COMMAND, status: ok ? "completed" : "failed", exitCode: ok ? 0 : 1 } });
  const shape = (list: Event[]) =>
    timeline(list, derive(list, Agent.CLAUDE)).map((i) =>
      i.kind === "event" ? (i.event.kind.case === "toolCall" ? "call:" + i.event.kind.value.title : i.event.kind.case) : i.kind === "tools" ? "tools" + i.calls.length : i.kind + ":" + ("tool" in i ? i.tool.title : ""),
    );

  it("shows a tool as soon as it starts, where it started", () => {
    const list = events({ case: "userMessage", value: { text: "run" } }, { case: "turnStarted", value: {} }, started("t1", "go test ./..."), { case: "agentMessage", value: { text: "waiting" } });
    expect(shape(list)).toEqual(["userMessage", "running:go test ./...", "agentMessage"]);
  });

  it("puts a parallel call's result where it started and keeps each state apart", () => {
    const list = events(
      { case: "turnStarted", value: {} },
      started("t1", "go test ./..."),
      started("t2", "go build ./..."),
      started("t3", "pnpm lint"),
      done("t2", "go build ./..."),
      done("t3", "pnpm lint", false),
    );
    expect(shape(list)).toEqual(["running:go test ./...", "call:go build ./...", "call:pnpm lint"]);
  });

  it("never folds a running tool into a run of finished ones", () => {
    const list = events(
      { case: "turnStarted", value: {} },
      started("a", "ls"),
      done("a", "ls"),
      started("b", "cat x"),
      done("b", "cat x"),
      started("c", "go test"),
      started("d", "go vet"),
      done("d", "go vet"),
    );
    expect(shape(list)).toEqual(["tools2", "running:go test", "call:go vet"]);
  });

  it("keeps a tool that got no result when the turn ended as unsettled, not as done", () => {
    const list = events({ case: "turnStarted", value: {} }, started("t1", "sleep 600 &"), { case: "turnEnded", value: { outcome: TurnOutcome.ABORTED } });
    const d = derive(list, Agent.CLAUDE);
    expect(d.tools).toEqual([]);
    expect(shape(list)).toEqual(["unsettled:sleep 600 &", "turnEnded"]);
    expect(d.unsettled.map((t) => t.title)).toEqual(["sleep 600 &"]);
    // The next turn starts afresh.
    const next = derive(events(...list.map((e) => e.kind), { case: "turnStarted", value: {} }), Agent.CLAUDE);
    expect(next.unsettled).toEqual([]);
  });

  it("counts from the daemon's start time and freezes while disconnected", () => {
    const t0 = 1_000_000;
    expect(toolClock(t0, t0 + 132_000, undefined)).toEqual({ ms: 132_000, frozen: false });
    // Lost the server at +60s: the clock stops there rather than running on.
    expect(toolClock(t0, t0 + 132_000, t0 + 60_000)).toEqual({ ms: 60_000, frozen: true });
    // An unknown start is not guessed.
    expect(toolClock(0, t0, undefined)).toBeUndefined();
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
  const ctx = { mode: ApprovalMode.AUTO };
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

  it("keeps full access but treats plan as a passing state", () => {
    const full = sess({ mode: ApprovalMode.FULL_ACCESS });
    expect(inheritConfig(projects([full])[0], machines(true, true))?.mode).toBe(ApprovalMode.FULL_ACCESS);
    const plan = sess({ mode: ApprovalMode.PLAN });
    expect(inheritConfig(projects([plan])[0], machines(true, true))?.mode).toBe(ApprovalMode.UNSPECIFIED);
  });

  it("keeps the last machine until the user picks another", () => {
    const list = [sess({ model: "x", ...ctx }, { id: "a", machineId: "m2", updatedMs: 1n }), sess({ model: "y", ...ctx }, { id: "b", updatedMs: 5n })];
    expect(inheritConfig(projects(list)[0], machines(false, true))).toMatchObject({ machineId: "m1", model: "y" });
  });

  it("keeps the last CLI with an offline machine", () => {
    const meta = (...agents: Agent[]) => create(MachineMetaSchema, { agents: agents.map((agent) => create(AgentInfoSchema, { agent })) });
    const ms = new Map<string, MachineLike>([
      ["m1", { id: "m1", online: false, lastSeenMs: 0n }],
      ["m2", { id: "m2", online: true, lastSeenMs: 0n, meta: meta(Agent.CLAUDE) }],
    ]);
    // The latest session ran Codex on m1, which is offline; m2 only has
    // Claude Code.
    const list = [sess({ ...ctx }, { id: "a", machineId: "m2", updatedMs: 1n }), sess({ ...ctx }, { id: "b", updatedMs: 5n })];
    expect(inheritConfig(projects(list)[0], ms)).toMatchObject({ machineId: "m1", agent: Agent.CODEX });
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
    expect(modeOf(sess({ agent: Agent.CLAUDE, permissionMode: "acceptEdits" }))).toEqual({ cls: "", text: "accept edits", mode: ApprovalMode.UNSPECIFIED });
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
      [2n, "模式切到了 accept edits"],
      [5n, "模式切到了 计划"],
    ]);
  });

  it("records approvals answered in the terminal", () => {
    expect(decisionText(ApprovalDecision.TERMINAL)).toBe("已在终端处理");
  });
});

describe("model labels", () => {
  const list = [
    { slug: "gpt-6-sol", displayName: "GPT-6-Sol" },
    { slug: "sonnet", displayName: "Sonnet" },
  ];
  it("names a model by its display name, the list's or Codex's", () => {
    expect(modelLabel(list, "gpt-6-sol")).toBe("GPT-6-Sol");
    // The machine's default, hidden from /model and so from the list.
    expect(modelLabel(list, "gpt-6.1-sol")).toBe("GPT-6.1-Sol");
    expect(modelLabel(list, "claude-sonnet-5-5", Agent.CLAUDE)).toBe("Sonnet");
    expect(modelLabel(list, "claude-opus-9", Agent.CLAUDE)).toBe("claude-opus-9");
    // The list has not come yet: the ID as is.
    expect(modelLabel(undefined, "gpt-6.1-sol")).toBe("gpt-6.1-sol");
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

describe("paths in tool titles", () => {
  // [title, cwd, home, shown]. The same cases are the reference for the
  // Android client.
  const cases: [string, string, string, string][] = [
    ["/Users/me/Code/foo/auth/login.go", "/Users/me/Code/foo", "/Users/me", "auth/login.go"],
    ["/private/tmp/ra/proj/a.txt", "/tmp/ra/proj", "/Users/me", "a.txt"],
    ["/tmp/ra/proj/a.txt", "/private/tmp/ra/proj", "/Users/me", "a.txt"],
    ["cd /Users/me/Code/foo && ls", "/Users/me/Code/foo", "/Users/me", "cd . && ls"],
    ["cat /Users/me/.zshrc", "/Users/me/Code/foo", "/Users/me", "cat ~/.zshrc"],
    ["ls /Users/me/Code/foo-bar/x", "/Users/me/Code/foo", "/Users/me", "ls ~/Code/foo-bar/x"],
    ["go build -o=/Users/me/Code/foo/bin/app ./cmd", "/Users/me/Code/foo/", "/Users/me", "go build -o=bin/app ./cmd"],
    ["diff /Users/me/Code/foo/a.go /Users/me/Code/bar/a.go", "/Users/me/Code/foo", "/Users/me", "diff a.go ~/Code/bar/a.go"],
    ['sed -n 1,5p "/Users/me/Code/foo/main.go"', "/Users/me/Code/foo", "/Users/me", 'sed -n 1,5p "main.go"'],
    ["Read /Users/me/Code/foo/auth/login.go:58", "/Users/me/Code/foo", "/Users/me", "Read auth/login.go:58"],
    ["cat /etc/hosts /private/etc/hosts", "/Users/me/Code/foo", "/Users/me", "cat /etc/hosts /private/etc/hosts"],
    ["curl https://example.com/Users/me/x", "/Users/me/Code/foo", "/Users/me", "curl https://example.com/Users/me/x"],
    ["grep -rn 'a/b' src/Users/me", "/Users/me/Code/foo", "/Users/me", "grep -rn 'a/b' src/Users/me"],
    ["tail /home/me/app/log.txt", "/srv/app", "/home/me", "tail ~/app/log.txt"],
    ["tail /home/me/app/log.txt", "/srv/app", "", "tail ~/app/log.txt"],
    ["cat /Users/me/notes.md", "", "/Users/me", "cat ~/notes.md"],
    ["/Users/me", "/Users/me/Code/foo", "/Users/me", "~"],
    // A folder matches only when a /, the token's end or :line follows it.
    ["Read /Users/me/Code/foo[archive]/a", "/Users/me/Code/foo", "/Users/me", "Read ~/Code/foo[archive]/a"],
    ['cat "/Users/me/Code/foo bar/a"', "/Users/me/Code/foo", "/Users/me", 'cat "~/Code/foo bar/a"'],
    ['cat "/Users/me/Code/foo/a b.go"', "/Users/me/Code/foo", "/Users/me", 'cat "a b.go"'],
    ["ls /Users/me/Code/foo.bak/x", "/Users/me/Code/foo", "/Users/me", "ls ~/Code/foo.bak/x"],
    ["(cd /Users/me/Code/foo/sub)", "/Users/me/Code/foo", "/Users/me", "(cd sub)"],
    ["cat /Users/me/Code/foo", "/Users/me/Code/foo", "/Users/me", "cat ."],
    ["echo /Users/meow/x", "/Users/me/Code/foo", "/Users/me", "echo /Users/meow/x"],
    ["Read /Users/me/Code/foo[x]", "/Users/me/Code/foo", "", "Read ~/Code/foo[x]"],
    ["tail /home/me/app:12", "/srv/app", "/home/me", "tail ~/app:12"],
  ];
  it.each(cases)("shows %s in %s as expected", (title, cwd, home, shown) => {
    expect(shortenPaths(title, cwd, home)).toBe(shown);
  });
});

describe("whole paths (file edit titles)", () => {
  // [path, cwd, home, shown]: the whole title is one path, spaces and all.
  const cases: [string, string, string, string][] = [
    ["/Users/me/Code/foo bar/a.go", "/Users/me/Code/foo", "/Users/me", "~/Code/foo bar/a.go"],
    ["/Users/me/Code/foo/sub dir/b.go", "/Users/me/Code/foo", "/Users/me", "sub dir/b.go"],
    ["/private/tmp/p/x y.txt", "/tmp/p", "/Users/me", "x y.txt"],
    ["/etc/x y", "/Users/me/Code/foo", "/Users/me", "/etc/x y"],
  ];
  it.each(cases)("shows %s in %s as expected", (path, cwd, home, shown) => {
    expect(shortenPath(path, cwd, home)).toBe(shown);
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

  it("notes nothing when a mode-only context carries the switched model", () => {
    // Since e54e35f the daemon's contexts after a model switch from here
    // carry the new model, a mode-only one included.
    const ctx = (permissionMode: string, model: string) => ({ case: "turnContext", value: { permissionMode, model } });
    const d = derive(
      events(
        ctx("default", "claude-haiku-4-5-20251001"),
        { case: "settingsChanged", value: { model: "sonnet", effort: "medium" } },
        ctx("acceptEdits", "claude-sonnet-5-5"), // mode changed before the next reply
        ctx("acceptEdits", "claude-sonnet-5-5"),
        ctx("acceptEdits", "claude-opus-5-5"), // then changed at the computer
      ),
      Agent.CLAUDE,
    );
    expect([...d.terminalChanges.entries()]).toEqual([
      [3n, "模式切到了 accept edits"],
      [5n, "模型切到了 claude-opus-5-5"],
    ]);
  });

  it("notes a switch back made in the terminal after a switch from here", () => {
    const ctx = (model: string) => ({ case: "turnContext", value: { approvalsReviewer: "user", approvalPolicy: "on-request", model, effort: "medium" } });
    const d = derive(
      events(
        ctx("gpt-6-astra"),
        { case: "settingsChanged", value: { model: "gpt-6-sol", effort: "medium" } },
        ctx("gpt-6-astra"), // switched back at the computer before the next turn
        ctx("gpt-6-sol"), // and to Sol again there
      ),
    );
    expect([...d.terminalChanges.entries()]).toEqual([
      [3n, "模型切到了 gpt-6-astra · medium"],
      [4n, "模型切到了 gpt-6-sol · medium"],
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

describe("approval records", () => {
  const approval = (approvalId: string, command: string, inputDigest: string) => ({
    case: "approvalRequested",
    value: { approvalId, toolName: "Bash", command, toolInputJson: JSON.stringify({ command }), inputDigest },
  });
  const call = (title: string, callId: string, inputDigest: string, status = "completed") => ({
    case: "toolCall",
    value: { title, callId, itemType: "Bash", inputDigest, kind: ToolKind.COMMAND, status },
  });
  const rows = (list: Event[]) => {
    const d = derive(list, Agent.CLAUDE);
    return timeline(list, d).map((it) => {
      if (it.kind !== "event") return it.kind;
      const k = it.event.kind;
      if (k.case === "approvalRequested") return `record ${k.value.command} ${decisionText(d.resolved.get(k.value.approvalId)!.decision)}`;
      if (k.case === "toolCall") return `tool ${k.value.title}${it.decision ? " " + decisionText(it.decision) : ""}`;
      return k.case;
    });
  };

  it("show on the tool call they were for, only when it is surely that call", () => {
    // Claude Code runs tools in parallel: approvals wait, an unrelated call
    // finishes first, then the approved ones, out of order.
    const list = events(
      { case: "turnStarted", value: {} },
      approval("A", "mkdir x", "dA"),
      approval("B", "rm y", "dB"),
      approval("C", "npm test", "dC"),
      approval("D", "npm test", "dC"),
      { case: "approvalResolved", value: { approvalId: "B", decision: ApprovalDecision.DENY } },
      { case: "approvalResolved", value: { approvalId: "A", decision: ApprovalDecision.ALLOW } },
      { case: "approvalResolved", value: { approvalId: "C", decision: ApprovalDecision.ALLOW } },
      { case: "approvalResolved", value: { approvalId: "D", decision: ApprovalDecision.ALLOW } },
      { case: "toolCall", value: { title: "Read a.go", callId: "r", itemType: "Read", inputDigest: "dR", kind: ToolKind.OTHER, status: "completed" } },
      call("rm y", "d", "", "failed"), // no digest
      call("mkdir x", "m", "dA"),
      call("npm test", "t1", "dC"),
      call("npm test", "t2", "dC"),
    );
    expect(rows(list)).toEqual([
      // B has no call it can surely be tied to, and C and D two each.
      "record rm y 已拒绝",
      "record npm test 已允许",
      "record npm test 已允许",
      "tool Read a.go",
      "tool rm y",
      // A's call carries A's result, and A's own row goes.
      "tool mkdir x 已允许",
      "tools",
    ]);
  });

  it("match by the daemon's digests only, never one worked out here", () => {
    const list = events(
      // Input that decodes differently here than in Go: only the daemon's
      // digest says which call it is.
      { case: "approvalRequested", value: { approvalId: "A", toolName: "Bash", command: "a", toolInputJson: '{"command":"a","COMMAND":"b"}', inputDigest: "db" } },
      { case: "approvalResolved", value: { approvalId: "A", decision: ApprovalDecision.ALLOW } },
      // A request without a digest, next to a call
      // whose digest its input would give.
      { case: "approvalRequested", value: { approvalId: "B", toolName: "Bash", command: "ls", toolInputJson: '{"command":"ls"}' } },
      { case: "approvalResolved", value: { approvalId: "B", decision: ApprovalDecision.ALLOW } },
      call("b", "1", "db"),
      // sha256("ls"): what digesting B's input here would have given.
      call("ls", "2", "c7b68ac37f364473e922936708e7f43c293dd07b295171566c07ff5fe024fab9"),
    );
    expect(rows(list)).toEqual(["record ls 已允许", "tool b 已允许", "tool ls"]);
  });

  it("stay rows of their own for Codex, whose calls have no digest", () => {
    const list = events(
      { case: "approvalRequested", value: { approvalId: "A", toolName: "shell", command: "mkdir x", toolInputJson: "" } },
      { case: "approvalResolved", value: { approvalId: "A", decision: ApprovalDecision.ALLOW } },
      { case: "toolCall", value: { title: "mkdir x", kind: ToolKind.COMMAND, status: "completed" } },
    );
    expect(rows(list)).toEqual(["record mkdir x 已允许", "tool mkdir x"]);
  });
});

describe("switching a new session's CLI or machine (v2)", () => {
  const base = { machineId: "m1", cwd: "/Users/me/Code/foo", agent: Agent.CODEX, model: "gpt-6-sol", effort: "high", mode: ApprovalMode.AUTO };

  it("keeps where it starts and takes the new CLI's defaults", () => {
    expect(switchAgent(base, Agent.CLAUDE)).toEqual({ ...base, agent: Agent.CLAUDE, model: "", effort: "", mode: ApprovalMode.UNSPECIFIED });
    // The same CLI again changes nothing.
    expect(switchAgent(base, Agent.CODEX)).toEqual(base);
  });

  it("never carries full access over to another CLI", () => {
    // It was confirmed for Codex; Claude Code's start asks again, or uses its own default.
    expect(switchAgent({ ...base, mode: ApprovalMode.FULL_ACCESS }, Agent.CLAUDE).mode).toBe(ApprovalMode.UNSPECIFIED);
  });

  it("takes a CLI the new machine has, and its defaults", () => {
    const claudeOnly: MachineLike = { id: "m2", online: true, lastSeenMs: 0n, meta: create(MachineMetaSchema, { agents: [create(AgentInfoSchema, { agent: Agent.CLAUDE })] }) };
    expect(switchMachine(base, claudeOnly)).toEqual({ ...base, machineId: "m2", agent: Agent.CLAUDE, model: "", effort: "", mode: ApprovalMode.UNSPECIFIED });
    const codex: MachineLike = { id: "m3", online: true, lastSeenMs: 0n };
    expect(switchMachine(base, codex)).toMatchObject({ machineId: "m3", agent: Agent.CODEX, model: "", mode: ApprovalMode.UNSPECIFIED });
  });

  it("refuses a mode the model cannot run in", () => {
    expect(newConfigIssue(Agent.CLAUDE, "haiku", ApprovalMode.AUTO)).toBe("Haiku 没有自动模式，换一种审批模式再开始");
    expect(newConfigIssue(Agent.CLAUDE, "claude-haiku-4-5", ApprovalMode.AUTO)).not.toBe("");
    expect(newConfigIssue(Agent.CLAUDE, "sonnet", ApprovalMode.AUTO)).toBe("");
    expect(newConfigIssue(Agent.CODEX, "haiku", ApprovalMode.AUTO)).toBe("");
    expect(newConfigIssue(Agent.CLAUDE, "haiku", ApprovalMode.ASK)).toBe("");
  });
});

describe("messages the computer accepted (v2)", () => {
  it("keeps what was accepted, whoever sent it, for the shared queue", () => {
    const d = derive(
      events(
        { case: "inputState", value: { requestId: "a", status: InputStatus.QUEUED, stage: InputStage.WAITING, text: "补测试", imageIds: ["i1", "i2"] } },
        { case: "inputState", value: { requestId: "a", status: InputStatus.QUEUED, stage: InputStage.PASTING_IMAGES } },
      ),
    );
    expect(d.accepted.get("a")).toMatchObject({ text: "补测试", imageIds: ["i1", "i2"] });
    expect(d.stages.get("a")).toBe(InputStage.PASTING_IMAGES);
    expect(queuedIds(d)).toEqual(["a"]);
  });

  it("shows a message that failed before Enter once, and says why", () => {
    const list = events(
      { case: "inputState", value: { requestId: "a", status: InputStatus.QUEUED, text: "看图" } },
      { case: "inputState", value: { requestId: "a", status: InputStatus.QUEUED, stage: InputStage.PASTING_IMAGES } },
      { case: "inputState", value: { requestId: "a", status: InputStatus.FAILED, reason: "image_not_recognized" } },
      { case: "inputState", value: { requestId: "b", status: InputStatus.QUEUED, text: "再看" } },
      { case: "inputState", value: { requestId: "b", status: InputStatus.NEEDS_TERMINAL, reason: "input_changed" } },
    );
    const d = derive(list);
    expect(phaseOf({ status: "sent" }, d.inputs.get("a"))).toBe("notSent");
    expect(phaseOf({ status: "sent" }, d.inputs.get("b"))).toBe("needsTerminal");
    expect(queuedIds(d)).toEqual([]);
    const rows = timeline(list, d).map((i) => (i.kind === "event" && i.event.kind.case === "inputState" ? `${i.event.kind.value.requestId}:${i.event.kind.value.status}` : i.kind));
    expect(rows).toEqual([`a:${InputStatus.FAILED}`, `b:${InputStatus.NEEDS_TERMINAL}`]);
    expect(inputFailure("image_not_recognized", Agent.CLAUDE)).toBe("Claude Code 没认出图片");
    expect(inputFailure("input_changed", Agent.CODEX)).toBe("终端输入框里有别的内容");
    expect(inputFailure("", Agent.CODEX)).toBe("没有写进终端");
  });

  it("tells a message typed at the computer by its origin when the daemon gives one", () => {
    const d = derive(
      events(
        { case: "inputState", value: { requestId: "r", status: InputStatus.SUBMITTED } },
        { case: "userMessage", value: { text: "from a client", requestId: "r" }, },
      ).concat(
        create(EventSchema, { seq: 3n, origin: { kind: OriginKind.LOCAL }, kind: { case: "userMessage", value: { text: "typed" } } }),
        create(EventSchema, { seq: 4n, origin: { kind: OriginKind.CLIENT, deviceId: "x" }, kind: { case: "userMessage", value: { text: "sent" } } }),
      ),
    );
    expect([...d.typedLocally]).toEqual([3n]);
  });
});

it("leaves the CLI's image placeholders out of titles and previews", () => {
  expect(titleOf(sess({ title: "[Image #1] 图里是什么图案？" }))).toBe("图里是什么图案？");
  expect(plain("[Image #1][Image #2] 看这两张")).toBe("看这两张");
  expect(plain("比较 [Image #1] 和 [Image #2]")).toBe("比较 图 1 和 图 2");
});

describe("a CLI's effective default mode (Go 0f6ad8d)", () => {
  const machine = (info: object) => ({ id: "m1", online: true, lastSeenMs: 0n, meta: create(MachineMetaSchema, { agents: [create(AgentInfoSchema, info as never)] }) });
  const claudeFull = machine({ agent: Agent.CLAUDE, defaultMode: ApprovalMode.FULL_ACCESS, defaultModeSource: DefaultModeSource.CLI_CONFIG, defaultModeVaries: true });
  const codexAuto = machine({ agent: Agent.CODEX, defaultMode: ApprovalMode.AUTO, defaultModeSource: DefaultModeSource.DAEMON_SETTINGS });
  const unread = machine({ agent: Agent.CODEX }); // the CLI's config could not be read
  const acceptEdits = machine({ agent: Agent.CLAUDE, defaultMode: ApprovalMode.UNSPECIFIED, defaultModeSource: DefaultModeSource.CLI_CONFIG });

  it("names the mode a start will really use", () => {
    expect(modeLabel(ApprovalMode.UNSPECIFIED, defaultModeOf(claudeFull, Agent.CLAUDE), Agent.CLAUDE)).toBe("默认：完全放行（项目设置可能不同）");
    expect(modeLabel(ApprovalMode.UNSPECIFIED, defaultModeOf(codexAuto, Agent.CODEX), Agent.CODEX)).toBe("默认：自动");
    expect(modeLabel(ApprovalMode.UNSPECIFIED, defaultModeOf(unread, Agent.CODEX), Agent.CODEX)).toBe("默认模式（不确定，可能是完全放行）");
    expect(modeLabel(ApprovalMode.UNSPECIFIED, defaultModeOf(acceptEdits, Agent.CLAUDE), Agent.CLAUDE)).toBe("默认：Claude Code 自己的模式");
    expect(modeLabel(ApprovalMode.ASK, defaultModeOf(claudeFull, Agent.CLAUDE), Agent.CLAUDE)).toBe("询问");
  });
});

describe("Codex call IDs and background calls (Go 60492f9, 1e4ed59)", () => {
  const started = (callId: string, title: string) => ({ case: "toolStarted", value: { title, callId, kind: ToolKind.COMMAND } });
  const call = (callId: string, title: string, extra: object = {}) => ({ case: "toolCall", value: { title, callId, kind: ToolKind.COMMAND, status: "completed", exitCode: 0, ...extra } });
  const shape = (list: Event[]) =>
    timeline(list, derive(list, Agent.CODEX)).map((i) => (i.kind === "event" ? (i.event.kind.case === "toolCall" ? "call:" + i.event.kind.value.callId : i.event.kind.case) : i.kind === "tools" ? "tools" + i.calls.length : i.kind + ":" + ("tool" in i ? i.tool.callId : "call" in i ? i.call.callId : "")));

  it("pairs by call ID and takes a #2 continuation as a result of its own, not an orphan", () => {
    const list = events({ case: "turnStarted", value: {} }, started("c1", "go test"), started("c2", "go vet"), call("c2", "go vet"), call("c1", "go test"), call("c1#2", "go test"));
    const d = derive(list, Agent.CODEX);
    expect(d.tools).toEqual([]);
    expect(shape(list)).toEqual(["tools3"]);
    const ended = derive(events(...list.map((e) => e.kind), { case: "turnEnded", value: { outcome: TurnOutcome.COMPLETED } }), Agent.CODEX);
    expect(ended.unsettled).toEqual([]);
  });

  it("shows a call that went on in the background as started, status unknown, with why", () => {
    const list = events(
      { case: "turnStarted", value: {} },
      started("c1", "npm run dev"),
      call("c1", "npm run dev", { status: "running", exitCode: undefined, background: ToolBackground.STILL_RUNNING }),
      { case: "turnEnded", value: { outcome: TurnOutcome.COMPLETED } },
    );
    const d = derive(list, Agent.CODEX);
    expect(shape(list)[0]).toBe("background:c1");
    expect(d.background.map((t) => t.callId)).toEqual(["c1"]);
    expect(backgroundNote(ToolBackground.STILL_RUNNING)).toContain("这一轮结束时它仍在运行");
    expect(backgroundNote(ToolBackground.TIMED_OUT)).toContain("超时");
    expect(backgroundNote(ToolBackground.BY_USER)).toContain("Ctrl+B");
    expect(backgroundNote(ToolBackground.REQUESTED)).toContain("要求");
  });
});

it("writes a model's context window, and nothing when it is unknown", () => {
  expect(contextText(1_000_000n)).toBe("1M 上下文");
  expect(contextText(200_000n)).toBe("200k 上下文");
  expect(contextText(0n)).toBe("");
  expect(contextText(undefined)).toBe("");
});
