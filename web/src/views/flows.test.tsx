// @vitest-environment happy-dom
// Interaction regressions: the pages rendered against the demo Remote,
// clicked through as a user would.

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { DemoRemote } from "../dev/demo";
import { create } from "@bufbuild/protobuf";
import { Agent, ApprovalDecision, ApprovalMode, EventSchema, InputStatus, PlanProposedSchema, SessionStatus, ToolKind } from "../gen/remoteagent/v1/event_pb";
import { derive, dockItems } from "../model";
import { DaemonSettingsSchema } from "../gen/remoteagent/v1/local_pb";
import { CommandResultSchema, type CommandResult } from "../gen/remoteagent/v1/remote_pb";
import { CommandError, type CommandKind, type RequestSlot } from "../remote";
import { ModeSheet, SessionSheet } from "../sheets";
import { Dock, PlanPage, QuestionPage } from "./Dock";
import { Home, todoList, WideHome } from "./Home";
import { loadProjectConfig } from "../storage";
import { useNewSession } from "./NewSession";
import { ProjectPage } from "./Project";
import { ManagePage } from "./Projects";
import { SessionPage } from "./Session";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | undefined;
let host: HTMLElement;

function mount(node: React.ReactNode) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(node));
}

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = undefined;
  localStorage.clear();
});

function demo(): DemoRemote {
  const r = DemoRemote.create(new URLSearchParams());
  r.start();
  return r;
}

const button = (text: string | RegExp, within: ParentNode = host) =>
  [...within.querySelectorAll("button")].find((b) => (typeof text === "string" ? b.textContent === text : text.test(b.textContent ?? ""))) as HTMLButtonElement | undefined;

async function click(b: HTMLElement | undefined) {
  expect(b).toBeTruthy();
  await act(async () => b!.click());
}

it("lets the home card handle one approval after another", async () => {
  const r = demo();
  mount(<Home remote={r} go={() => undefined} />);
  // s-appr waits on two approvals and is listed first.
  const card = () => host.querySelector(".todo")!;
  expect(card().textContent).toContain("rm -rf gen/remoteagent");
  await click(button("允许", card()));
  await vi.waitFor(() => expect(card().textContent).toContain("/etc/hosts"), { timeout: 3000 });
  // The second approval's buttons start fresh, not locked as "允许中…".
  const allow = button("允许", card());
  expect(allow?.disabled).toBe(false);
  expect(button("拒绝", card())?.disabled).toBe(false);
});

function AskDock({ r, collapsed }: { r: DemoRemote; collapsed: boolean }) {
  const s = r.sessions.get("s-ask")!;
  const d = derive(r.events("s-ask"));
  return (
    <Dock
      remote={r}
      sid="s-ask"
      items={dockItems(s, d, r.machines.get(s.machineId))}
      agent={Agent.CODEX}
      collapsed={collapsed}
      onExpand={() => undefined}
      onDetail={() => undefined}
      onPlan={() => undefined}
      onTerminal={() => undefined}
    />
  );
}

it("keeps a question's progress across collapsing the dock", async () => {
  const r = demo();
  mount(<AskDock r={r} collapsed={false} />);
  await click(button(/^只升小版本/));
  await vi.waitFor(() => expect(host.textContent).toContain("第 2 题，共 2 题"), { timeout: 3000 });
  // The keyboard comes up, then goes away again.
  act(() => root!.render(<AskDock r={r} collapsed />));
  expect(host.querySelector(".dock.mini")).toBeTruthy();
  act(() => root!.render(<AskDock r={r} collapsed={false} />));
  expect(host.textContent).toContain("第 2 题，共 2 题");
  expect(host.textContent).toContain("要顺便更新 go.mod 的 Go 版本吗？");
});

it("moves to the next question when a retried answer goes through", async () => {
  const r = demo();
  // The first answer's outcome is lost; the retry succeeds.
  let calls = 0;
  const real = r.command.bind(r);
  r.command = async (sid: string, cmd: CommandKind, slot?: RequestSlot): Promise<CommandResult> => {
    if (cmd?.case === "answerQuestion" && calls++ === 0) throw new CommandError("unknown", "连接超时");
    return real(sid, cmd, slot);
  };
  mount(<AskDock r={r} collapsed={false} />);
  await click(button(/^只升小版本/));
  await vi.waitFor(() => expect(button("重试")).toBeTruthy(), { timeout: 3000 });
  await click(button("重试"));
  await vi.waitFor(() => expect(host.textContent).toContain("第 2 题，共 2 题"), { timeout: 3000 });
  expect(button(/^更新到 1.27/)?.disabled).toBe(false);
});

it("keeps the dock's buttons out of its scrolling content", () => {
  const r = demo();
  const s = r.sessions.get("s-appr")!;
  mount(
    <Dock
      remote={r}
      sid="s-appr"
      items={dockItems(s, derive(r.events("s-appr")), r.machines.get(s.machineId))}
      agent={Agent.CODEX}
      collapsed={false}
      onExpand={() => undefined}
      onDetail={() => undefined}
      onPlan={() => undefined}
      onTerminal={() => undefined}
    />,
  );
  const dock = host.querySelector(".dock")!;
  const scroll = dock.querySelector(".dscroll")!;
  expect(scroll.textContent).toContain("rm -rf gen/remoteagent");
  for (const b of ["拒绝", "允许"]) {
    const el = button(b, dock)!;
    expect(scroll.contains(el)).toBe(false);
  }
});

it("follows the project's latest session as sessions change", () => {
  const r = demo();
  mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} wide={false} />);
  const line = () => host.querySelector(".newbox .cli-pick")!.textContent ?? "";
  expect(line()).not.toContain("Claude Code");
  // The Claude Code session in foo becomes the most recent one.
  act(() => {
    r.sessions.get("s-claude")!.updatedMs = BigInt(Date.now() + 60_000);
    (r as unknown as { changed(): void }).changed();
  });
  expect(line()).toContain("Claude Code");
});

it("shows a failed first listing and lists again", async () => {
  vi.useFakeTimers();
  try {
    const r = DemoRemote.create(new URLSearchParams("empty=none"));
    r.conn = "online";
    let fail = true;
    const empty = { machines: [], sessions: [] };
    (r as unknown as { api: object }).api = {
      listMachines: async () => {
        if (fail) throw new Error("连接超时");
        return empty;
      },
      listRemoteSessions: async () => empty,
    };
    mount(<Home remote={r} go={() => undefined} />);
    await act(async () => r.refresh());
    expect(r.listed).toBe(false);
    expect(host.querySelector(".skels")).toBeNull();
    expect(host.textContent).toContain("没能读取会话列表：连接超时");
    // It tries again by itself.
    fail = false;
    await act(async () => vi.advanceTimersByTimeAsync(2500));
    expect(r.listed).toBe(true);
    expect(host.textContent).not.toContain("没能读取会话列表");
  } finally {
    vi.useRealTimers();
  }
});

it("answers a multi-select question with every ticked option", async () => {
  const r = demo();
  const sent: unknown[] = [];
  const real = r.command.bind(r);
  r.command = async (sid: string, cmd: CommandKind, slot?: RequestSlot): Promise<CommandResult> => {
    if (cmd?.case === "answerQuestion") sent.push(cmd.value);
    return real(sid, cmd, slot);
  };
  const s = r.sessions.get("s-claude")!;
  mount(
    <Dock
      remote={r}
      sid="s-claude"
      items={dockItems(s, derive(r.events("s-claude")), r.machines.get(s.machineId))}
      agent={Agent.CLAUDE}
      collapsed={false}
      onExpand={() => undefined}
      onDetail={() => undefined}
      onPlan={() => undefined}
      onTerminal={() => undefined}
    />,
  );
  expect(host.textContent).toContain("Claude Code 在问你");
  expect(button("选好后提交")?.disabled).toBe(true);
  await click(button(/^按 IP/));
  await click(button(/^按设备/));
  // Ticking sends nothing yet.
  expect(sent).toEqual([]);
  await click(button("提交 2 项"));
  await vi.waitFor(() => expect(sent).toHaveLength(1));
  expect(sent[0]).toMatchObject({ questionId: "cq", questionIndex: 0, options: [1, 3] });
});

it("greys out modes a Claude Code session cannot switch to, and effort for haiku", async () => {
  const r = demo();
  const s = r.sessions.get("s-claude-idle")!;
  mount(<ModeSheet remote={r} s={s} onClose={() => undefined} />);
  expect(button(/^完全放行/)?.disabled).toBe(true);
  expect(host.textContent).toContain("本会话启动时未启用；需要时新建会话选择");
  expect(host.textContent).toContain("当前 Haiku 模型不支持");
  expect(button(/^自动/)?.disabled).toBe(true);
  expect(button(/^询问/)?.disabled).toBe(false);
  // acceptEdits is none of the four: nothing is ticked, and it says so.
  expect(host.querySelector(".ck")).toBeNull();
  expect(host.textContent).toContain("当前：accept edits（在终端设置）");
  expect(host.textContent).toContain("不提供切到 accept edits 的按钮");
  act(() => root!.render(<SessionSheet remote={r} s={s} running={false} onClose={() => undefined} />));
  await vi.waitFor(() => expect(host.textContent).toContain("Haiku 没有推理强度可调"), { timeout: 3000 });
  expect(host.querySelector(".slider")).toBeNull();
});

it("offers no clear-and-implement on a Claude Code plan", () => {
  const r = demo();
  const plan = create(PlanProposedSchema, { text: "1. do it" });
  mount(<PlanPage remote={r} sid="s-claude" plan={plan} title="t" onBack={() => undefined} />);
  expect(button("批准执行")).toBeTruthy();
  expect(button("清空上下文后执行")).toBeUndefined();
  act(() => root!.render(<PlanPage remote={r} sid="s-plan" plan={plan} title="t" onBack={() => undefined} />));
  expect(button("清空上下文后执行")).toBeTruthy();
});

it("never sends one project's unknown start from another project's box", async () => {
  const r = demo();
  const sent: { cwd?: string; prompt?: string; requestId?: string }[] = [];
  r.machineCommand = async (_m: string, cmd: CommandKind, slot?: RequestSlot): Promise<CommandResult> => {
    if (cmd?.case === "createSession") {
      sent.push({ cwd: cmd.value.cwd, prompt: cmd.value.prompt, requestId: slot?.requestId });
      throw new CommandError("unknown", "连接超时");
    }
    throw new CommandError("unavailable", "no");
  };
  let ns: ReturnType<typeof useNewSession> | undefined;
  function Box({ pkey }: { pkey: string }) {
    ns = useNewSession(r, pkey, () => undefined);
    return null;
  }
  // The wide home keeps one box and switches its project.
  mount(<Box pkey="/Users/me/Code/foo" />);
  await act(async () => void (await ns!.create("在 foo 里做")));
  expect(ns!.frozen).toBe(true);
  act(() => root!.render(<Box pkey="/Users/me/Code/shop" />));
  expect(ns!.frozen).toBe(false);
  await act(async () => void (await ns!.create("在 shop 里做")));
  expect(sent[1]).toMatchObject({ cwd: "/Users/me/Code/shop", prompt: "在 shop 里做" });
  expect(sent[1].requestId).not.toBe(sent[0].requestId);
  // Back in foo, the unknown start is still the one retried.
  act(() => root!.render(<Box pkey="/Users/me/Code/foo" />));
  await act(async () => void (await ns!.create("在 foo 里做")));
  expect(sent[2]).toEqual(sent[0]);
});

it("offers the full command of an approval that arrived while the dock was collapsed", () => {
  const r = demo();
  const s = r.sessions.get("s-appr")!;
  const dock = (collapsed: boolean) => (
    <Dock
      remote={r}
      sid="s-appr"
      items={dockItems(s, derive(r.events("s-appr")), r.machines.get(s.machineId))}
      agent={Agent.CODEX}
      collapsed={collapsed}
      onExpand={() => undefined}
      onDetail={() => undefined}
      onPlan={() => undefined}
      onTerminal={() => undefined}
    />
  );
  // The five-line command mounts while the user is typing.
  mount(dock(true));
  act(() => root!.render(dock(false)));
  expect(button("展开全文")).toBeTruthy();
});

function ClaudeAsk({ r }: { r: DemoRemote }) {
  const s = r.sessions.get("s-claude")!;
  return (
    <Dock
      remote={r}
      sid="s-claude"
      items={dockItems(s, derive(r.events("s-claude")), r.machines.get(s.machineId))}
      agent={Agent.CLAUDE}
      collapsed={false}
      onExpand={() => undefined}
      onDetail={() => undefined}
      onPlan={() => undefined}
      onTerminal={() => undefined}
    />
  );
}

function failingAnswers(r: DemoRemote, first: CommandError) {
  const sent: number[][] = [];
  const slots: string[] = [];
  const real = r.command.bind(r);
  r.command = async (sid: string, cmd: CommandKind, slot?: RequestSlot): Promise<CommandResult> => {
    if (cmd?.case === "answerQuestion") {
      sent.push([...(cmd.value.options ?? [])]);
      slots.push(slot?.requestId ?? "");
      if (sent.length === 1) throw first;
    }
    return real(sid, cmd, slot);
  };
  return { sent, slots };
}

it("lets a multi-select answer be changed and sent again after a definite failure", async () => {
  const r = demo();
  const { sent } = failingAnswers(r, new CommandError("failed_precondition", "问题已经变了"));
  mount(<ClaudeAsk r={r} />);
  await click(button(/^按 IP/));
  await click(button("提交 1 项"));
  await vi.waitFor(() => expect(host.textContent).toContain("没能提交"));
  // Change the answer: the error goes and 提交 comes back.
  await click(button(/^按账户/));
  expect(host.textContent).not.toContain("没能提交");
  await click(button("提交 2 项"));
  await vi.waitFor(() => expect(sent).toHaveLength(2));
  expect(sent[1]).toEqual([1, 2]);
});

it("retries a multi-select answer of unknown outcome unchanged", async () => {
  const r = demo();
  const { sent, slots } = failingAnswers(r, new CommandError("unknown", "连接超时"));
  mount(<ClaudeAsk r={r} />);
  await click(button(/^按 IP/));
  await click(button(/^按设备/));
  await click(button("提交 2 项"));
  await vi.waitFor(() => expect(host.textContent).toContain("不确定提交了没有"));
  // The ticks are locked to what was sent; only 重试 remains.
  expect(button(/^按账户/)?.disabled).toBe(true);
  expect(button(/^提交/)).toBeUndefined();
  await click(button("重试"));
  await vi.waitFor(() => expect(sent).toHaveLength(2));
  expect(sent[1]).toEqual([1, 3]);
  expect(slots[1]).toBe(slots[0]);
});

it("shows a dock with no room for it as one line that opens its page", async () => {
  const r = demo();
  const s = r.sessions.get("s-appr")!;
  const opened: string[] = [];
  const dock = (room: number) => (
    <Dock
      remote={r}
      sid="s-appr"
      items={dockItems(s, derive(r.events("s-appr")), r.machines.get(s.machineId))}
      agent={Agent.CODEX}
      collapsed={false}
      room={room}
      onExpand={() => opened.push("expand")}
      onDetail={(id) => opened.push("detail:" + id)}
      onPlan={() => opened.push("plan")}
      onTerminal={() => opened.push("terminal")}
    />
  );
  mount(dock(Infinity));
  expect(host.querySelector(".dock.mini")).toBeNull();
  // No room: the card needs at least its margin.
  act(() => root!.render(dock(0)));
  const mini = host.querySelector(".dock.mini")!;
  expect(mini.textContent).toContain("请求执行");
  // The full card is still there for measuring, but hidden and inert.
  expect(host.querySelector(".dockwrap")!.classList.contains("offstage")).toBe(true);
  await click(button("展开", mini));
  expect(opened).toEqual(["detail:a1"]);
});

it("keeps a question's progress between its page and the dock", async () => {
  const r = demo();
  const q = derive(r.events("s-ask")).questions.get("q1")!;
  mount(<QuestionPage remote={r} sid="s-ask" q={q} agent={Agent.CODEX} title="升级依赖" onBack={() => undefined} onTerminal={() => undefined} />);
  await click(button(/^只升小版本/));
  await vi.waitFor(() => expect(host.textContent).toContain("第 2 题，共 2 题"), { timeout: 3000 });
  act(() => root!.render(<AskDock r={r} collapsed={false} />));
  expect(host.textContent).toContain("第 2 题，共 2 题");
  // The one-line form names the question now being asked.
  act(() => root!.render(<AskDock r={r} collapsed />));
  expect(host.querySelector(".dock.mini")!.textContent).toContain("要顺便更新 go.mod 的 Go 版本吗？");
});

it("shortens paths in tool titles and keeps the original in the details", async () => {
  const r = demo();
  const s = r.sessions.get("s-run")!;
  s.info = { ...s.info!, cwd: "/Users/me/Code/foo" };
  r.events("s-run").push(
    create(EventSchema, {
      seq: BigInt(r.events("s-run").length + 1),
      kind: { case: "toolCall", value: { title: "cat /Users/me/Code/foo/go.mod /Users/me/.zshrc", kind: ToolKind.COMMAND, status: "failed", exitCode: 1 } },
    }),
  );
  mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
  const row = [...host.querySelectorAll(".tool code")].find((c) => c.textContent?.startsWith("cat "))!;
  expect(row.textContent).toBe("cat go.mod ~/.zshrc");
  await click(row.closest("button")!);
  expect(host.textContent).toContain("cat /Users/me/Code/foo/go.mod /Users/me/.zshrc");
});

function ClaudeQuestionPage({ r, id = "cq" }: { r: DemoRemote; id?: string }) {
  const q = derive(r.events("s-claude")).questions.get(id)!;
  return <QuestionPage remote={r} sid="s-claude" q={q} agent={Agent.CLAUDE} title="t" onBack={() => undefined} onTerminal={() => undefined} />;
}

it("keeps ticks and a pending answer between the dock and the question page", async () => {
  const r = demo();
  const { sent, slots } = failingAnswers(r, new CommandError("unknown", "连接超时"));
  mount(<ClaudeAsk r={r} />);
  await click(button(/按 IP/));
  // To the page and back: the tick is still there.
  act(() => root!.render(<ClaudeQuestionPage r={r} />));
  expect(button(/按 IP/)?.getAttribute("aria-checked")).toBe("true");
  await click(button(/按设备/));
  act(() => root!.render(<ClaudeAsk r={r} />));
  expect(button("提交 2 项")).toBeTruthy();
  await click(button("提交 2 项"));
  await vi.waitFor(() => expect(host.textContent).toContain("不确定提交了没有"));
  // The unknown outcome follows to the page, and its retry is the same request.
  act(() => root!.render(<ClaudeQuestionPage r={r} />));
  expect(host.textContent).toContain("不确定提交了没有");
  expect(button(/按账户/)?.disabled).toBe(true);
  await click(button("重试"));
  await vi.waitFor(() => expect(sent).toHaveLength(2));
  expect(sent[1]).toEqual([1, 3]);
  expect(slots[1]).toBe(slots[0]);
});

it("gives the next question call on the page fresh options", async () => {
  const r = demo();
  // A second call waits after the first.
  r.events("s-claude").push(
    create(EventSchema, {
      seq: BigInt(r.events("s-claude").length + 1),
      kind: { case: "questionAsked", value: { questionId: "cq2", questions: [{ text: "还要限流吗？", options: [{ label: "要" }, { label: "不要" }] }] } },
    }),
  );
  mount(<ClaudeQuestionPage r={r} />);
  await click(button(/按 IP/));
  await click(button("提交 1 项"));
  await vi.waitFor(() => expect(button(/按 IP/)?.disabled).toBe(true));
  act(() => root!.render(<ClaudeQuestionPage r={r} id="cq2" />));
  expect(button(/^要/)?.disabled).toBe(false);
});

it("keeps the message being typed when a page opens and closes", async () => {
  const r = demo();
  const s = r.sessions.get("s-appr")!;
  mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
  const input = () => host.querySelector<HTMLTextAreaElement>("textarea[aria-label=消息]")!;
  await act(async () => {
    const set = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
    set.call(input(), "顺便看下 lint");
    input().dispatchEvent(new Event("input", { bubbles: true }));
  });
  expect(input().value).toBe("顺便看下 lint");
  // Open the approval's page, then come back.
  await click(button("展开全文"));
  expect(host.textContent).toContain("审批详情");
  await click(host.querySelector<HTMLButtonElement>(".navbar .back")!);
  expect(input().value).toBe("顺便看下 lint");
});

it("never renews a session start of unknown outcome, even once it expired", async () => {
  const r = demo();
  const sent: string[] = [];
  const answers = [new CommandError("unknown", "连接超时"), new CommandError("deadline_exceeded", "the command expired before it reached the machine")];
  r.machineCommand = async (_m: string, cmd: CommandKind, slot?: RequestSlot): Promise<CommandResult> => {
    if (cmd?.case === "createSession") {
      sent.push(slot!.requestId);
      const e = answers.shift();
      if (e) throw e;
    }
    throw new CommandError("unavailable", "no");
  };
  mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} wide={false} />);
  const input = () => host.querySelector<HTMLTextAreaElement>("textarea[aria-label=新会话的第一句话]")!;
  await act(async () => {
    const set = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
    set.call(input(), "加限流");
    input().dispatchEvent(new Event("input", { bubbles: true }));
  });
  await click(button("↑"));
  await vi.waitFor(() => expect(host.textContent).toContain("不确定是否已经启动"));
  await click(button("重试"));
  await vi.waitFor(() => expect(sent).toHaveLength(2));
  // Retried as the same request; then the daemon no longer knew it.
  expect(sent[1]).toBe(sent[0]);
  await vi.waitFor(() => expect(host.textContent).toContain("请先看会话列表"));
  expect(button("↑")?.disabled).toBe(true);
  expect(sent).toHaveLength(2);
  // Only giving it up allows a new start, under a new request ID.
  await click(button("放弃这次启动"));
  await click(button("↑"));
  await vi.waitFor(() => expect(sent).toHaveLength(3));
  expect(sent[2]).not.toBe(sent[0]);
});

it("keeps an answer of unknown outcome unknown when its retry comes back expired", async () => {
  const r = demo();
  const slots: string[] = [];
  const answers = [new CommandError("unknown", "连接超时"), new CommandError("deadline_exceeded", "expired")];
  r.command = async (_s: string, cmd: CommandKind, slot?: RequestSlot): Promise<CommandResult> => {
    if (cmd?.case === "answerQuestion") {
      slots.push(slot!.requestId);
      const e = answers.shift();
      if (e) throw e;
    }
    return create(CommandResultSchema, {});
  };
  mount(<ClaudeAsk r={r} />);
  await click(button(/按 IP/));
  await click(button("提交 1 项"));
  await vi.waitFor(() => expect(host.textContent).toContain("不确定提交了没有"));
  await click(button("重试"));
  await vi.waitFor(() => expect(slots).toHaveLength(2));
  await vi.waitFor(() => expect(host.textContent).toContain("不确定提交了没有"));
  await click(button("重试"));
  await vi.waitFor(() => expect(slots).toHaveLength(3));
  expect(new Set(slots).size).toBe(1);
});

it("shortens a file edit's whole path, spaces and all", () => {
  const r = demo();
  const s = r.sessions.get("s-idle")!;
  s.info = { ...s.info!, cwd: "/Users/me/Code/foo" };
  const push = (title: string) =>
    r.events("s-idle").push(create(EventSchema, { seq: BigInt(r.events("s-idle").length + 1), kind: { case: "toolCall", value: { title, kind: ToolKind.FILE_CHANGE, status: "failed" } } }));
  push("/Users/me/Code/foo bar/a.go");
  push("/Users/me/Code/foo/sub dir/b.go");
  mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
  const titles = [...host.querySelectorAll(".tool code")].map((c) => c.textContent);
  expect(titles).toContain("编辑 ~/Code/foo bar/a.go");
  expect(titles).toContain("编辑 sub dir/b.go");
});

it("shows a failed call's exit code beside its approval", () => {
  const r = demo();
  const s = r.sessions.get("s-claude-idle")!;
  const input = JSON.stringify({ command: "make test", description: "" });
  const push = (kind: object) => r.events("s-claude-idle").push(create(EventSchema, { seq: BigInt(r.events("s-claude-idle").length + 1), kind: kind as never }));
  push({ case: "approvalRequested", value: { approvalId: "f", toolName: "Bash", command: "make test", toolInputJson: input, inputDigest: "d-make" } });
  push({ case: "approvalResolved", value: { approvalId: "f", decision: ApprovalDecision.ALLOW } });
  push({ case: "toolCall", value: { title: "make test", itemType: "Bash", kind: ToolKind.COMMAND, status: "failed", exitCode: 7, inputDigest: "d-make" } });
  mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
  const row = [...host.querySelectorAll(".tool")].find((t) => t.textContent?.includes("make test"))!;
  expect(row.textContent).toContain("退出码 7");
  expect(row.textContent).toContain("已允许");
});

it("keeps an answer of unknown outcome unknown when its retry finds the machine offline", async () => {
  const r = demo();
  const slots: string[] = [];
  const answers = [new CommandError("unknown", "连接超时"), new CommandError("unavailable", "电脑不在线，指令没有发出")];
  r.command = async (_s: string, cmd: CommandKind, slot?: RequestSlot): Promise<CommandResult> => {
    if (cmd?.case === "answerQuestion") {
      slots.push(slot!.requestId);
      const e = answers.shift();
      if (e) throw e;
    }
    return create(CommandResultSchema, {});
  };
  mount(<ClaudeAsk r={r} />);
  await click(button(/按 IP/));
  await click(button("提交 1 项"));
  await vi.waitFor(() => expect(host.textContent).toContain("不确定提交了没有"));
  await click(button("重试"));
  await vi.waitFor(() => expect(slots).toHaveLength(2));
  await vi.waitFor(() => expect(host.textContent).toContain("不确定提交了没有"));
  await click(button("重试"));
  await vi.waitFor(() => expect(slots).toHaveLength(3));
  expect(new Set(slots).size).toBe(1);
});

describe("an unknown session start", () => {
  function setup(outcomes: (CommandError | "wait")[]) {
    const r = demo();
    const sent: { id: string; prompt: string; agent: number }[] = [];
    let release: (() => void) | undefined;
    r.machineCommand = async (_m: string, cmd: CommandKind, slot?: RequestSlot): Promise<CommandResult> => {
      if (cmd?.case !== "createSession") throw new CommandError("unavailable", "no");
      sent.push({ id: slot!.requestId, prompt: cmd.value.prompt ?? "", agent: cmd.value.agent ?? 0 });
      const o = outcomes.shift() ?? new CommandError("unknown", "连接超时");
      if (o === "wait") {
        await new Promise<void>((res) => (release = res));
        throw new CommandError("unknown", "连接超时");
      }
      throw o;
    };
    mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} wide={false} />);
    const type = async (value: string) =>
      act(async () => {
        const input = host.querySelector<HTMLTextAreaElement>("textarea[aria-label=新会话的第一句话]")!;
        Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, value);
        input.dispatchEvent(new Event("input", { bubbles: true }));
      });
    const send = async (n: number) => {
      await click(button("↑"));
      await vi.waitFor(() => expect(sent).toHaveLength(n));
      await vi.waitFor(() => expect(host.textContent).toContain("不确定是否已经启动"));
    };
    return { r, sent, type, send, release: () => release?.() };
  }

  it("is retried as the same request when only whitespace changed", async () => {
    const t = setup([]);
    await t.type("加限流");
    await t.send(1);
    await t.type("加限流 ");
    await t.send(2);
    expect(t.sent[1].id).toBe(t.sent[0].id);
  });

  it("is not given up by typing while it is on its way", async () => {
    const t = setup(["wait"]);
    await t.type("加限流");
    await click(button("↑"));
    await vi.waitFor(() => expect(t.sent).toHaveLength(1));
    await t.type("加限流\n");
    await act(async () => t.release());
    await vi.waitFor(() => expect(host.textContent).toContain("不确定是否已经启动"));
    await t.send(2);
    expect(t.sent[1].id).toBe(t.sent[0].id);
  });

  it("stays the same start while the page's settings follow another session", async () => {
    const t = setup([]);
    await t.type("加限流");
    await t.send(1);
    // The Claude Code session becomes the project's latest meanwhile.
    act(() => {
      t.r.sessions.get("s-claude")!.updatedMs = BigInt(Date.now() + 60_000);
      (t.r as unknown as { changed(): void }).changed();
    });
    await t.send(2);
    expect(t.sent[1]).toEqual(t.sent[0]);
  });

  it("gives way to a different start, even once it expired", async () => {
    const t = setup([new CommandError("unknown", "连接超时"), new CommandError("deadline_exceeded", "expired")]);
    await t.type("加限流");
    await t.send(1);
    await t.send(2);
    await vi.waitFor(() => expect(host.textContent).toContain("请先看会话列表"));
    expect(button("↑")?.disabled).toBe(true);
    await t.type("给登录接口加限流");
    expect(button("↑")?.disabled).toBe(false);
    await t.send(3);
    expect(t.sent[2].id).not.toBe(t.sent[0].id);
    expect(t.sent[2].prompt).toBe("给登录接口加限流");
  });
});


it("keeps a project's model and effort after Done and a page reload", async () => {
  const project = "/Users/me/Code/ai-bot/remote-agent";
  const page = (r: DemoRemote) => <ProjectPage remote={r} pkey={project} go={() => undefined} onBack={() => undefined} wide />;
  const r = demo();
  mount(page(r));
  await act(async () => { await Promise.all([r.listModels("mac", Agent.CODEX), r.settings("mac")]); });
  await click(host.querySelector<HTMLButtonElement>(".compose-card .ctl.model")!);
  await click(button("GPT-6-Sol"));
  await click(button("low"));
  await click(host.querySelector<HTMLButtonElement>('button[aria-label="返回"]')!);
  expect(host.querySelector(".compose-card .ctl.model")?.textContent).toContain("GPT-6-Sol · low");
  // Rebuild both the page and Remote, as a reload would; old sessions
  // still have Astra, so inheritance cannot make this pass by accident.
  act(() => root!.unmount());
  host.remove();
  const reloaded = demo();
  mount(page(reloaded));
  await act(async () => { await Promise.all([reloaded.listModels("mac", Agent.CODEX), reloaded.settings("mac")]); });
  expect(host.querySelector(".compose-card .ctl.model")?.textContent).toContain("GPT-6-Sol · low");
  const real = reloaded.machineCommand.bind(reloaded);
  let sent: unknown;
  reloaded.machineCommand = async (machine, cmd) => {
    if (cmd?.case === "createSession") sent = cmd.value;
    return real(machine, cmd);
  };
  // An empty message is not sent; 只打开终端 starts without one.
  expect(host.querySelector<HTMLButtonElement>('button[aria-label="开始新会话"]')!.disabled).toBe(true);
  await click(button("只打开终端 ›"));
  await act(async () => { await vi.waitFor(() => expect(loadProjectConfig(reloaded.account, project)).toBeUndefined()); });
  expect(sent).toMatchObject({ model: "gpt-6-sol", effort: "low", cwd: project });
});

it("isolates saved launch choices by project and account and reconfirms full access", async () => {
  const r = demo();
  let ns: ReturnType<typeof useNewSession>;
  function Box({ remote = r, project }: { remote?: DemoRemote; project: string }) {
    ns = useNewSession(remote, project, () => undefined);
    return null;
  }
  const foo = "/Users/me/Code/foo";
  const shop = "/Users/me/Code/shop";
  mount(<Box project={foo} />);
  act(() => ns.setCfg({ ...ns.cfg, model: "gpt-6-luna", effort: "max", mode: ApprovalMode.FULL_ACCESS }));
  expect(ns!.cfg.mode).toBe(ApprovalMode.FULL_ACCESS);
  act(() => root!.render(<Box project={shop} />));
  expect(ns!.cfg.model).not.toBe("gpt-6-luna");
  act(() => root!.render(<Box key="reload" remote={demo()} project={foo} />));
  expect(ns!.cfg).toMatchObject({ model: "gpt-6-luna", effort: "max", mode: ApprovalMode.UNSPECIFIED });
  const another = demo();
  another.account.deviceId = "another-device";
  act(() => root!.render(<Box key="another" remote={another} project={foo} />));
  expect(ns!.cfg.model).not.toBe("gpt-6-luna");
});

it("names a new project's default model as Codex does, even when /model hides it", async () => {
  const r = demo();
  // The machine's default is a model Codex leaves out of its list.
  (r as unknown as { saved: unknown }).saved = create(DaemonSettingsSchema, { approvalTimeoutSeconds: 600, defaultModel: "gpt-6.1-sol", defaultEffort: "low" });
  r.draftMachines.set("/Users/me/Code/new", "mac");
  mount(<ProjectPage remote={r} pkey="/Users/me/Code/new" go={() => undefined} onBack={() => undefined} wide={false} />);
  await vi.waitFor(() => expect(host.querySelector(".newbox .cfg-model")!.textContent).toContain("GPT-6.1-Sol · low"), { timeout: 3000 });
});

it("tells how to trust a folder in Claude Code, whose prompt starts on No, exit", () => {
  const r = demo();
  const popup = (agent: Agent) => (
    <Dock
      remote={r}
      sid="s-popup"
      items={[{ kind: "popup", id: "popup", title: "trust folder" }]}
      agent={agent}
      collapsed={false}
      onExpand={() => undefined}
      onDetail={() => undefined}
      onPlan={() => undefined}
      onTerminal={() => undefined}
    />
  );
  mount(popup(Agent.CLAUDE));
  expect(host.textContent).toContain("默认选中的是 No, exit。按 ↓ 选中 Yes, I trust this folder 再按 ⏎");
  act(() => root!.render(popup(Agent.CODEX)));
  expect(host.textContent).not.toContain("No, exit");
});

it("titles a running file edit as it will be titled once done", () => {
  const r = demo();
  const s = r.sessions.get("s-idle")!;
  s.info = { ...s.info!, cwd: "/Users/me/Code/foo", status: 3 };
  const push = (kind: object) => r.events("s-idle").push(create(EventSchema, { seq: BigInt(r.events("s-idle").length + 1), kind: kind as never }));
  push({ case: "turnStarted", value: {} });
  push({ case: "toolStarted", value: { title: "/Users/me/Code/foo/hello.txt", callId: "w", kind: ToolKind.FILE_CHANGE, itemType: "Write" } });
  mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
  // Shown as soon as it starts, as a card of its own.
  const running = [...host.querySelectorAll(".active-tool pre")].map((c) => c.textContent);
  expect(running).toContain("编辑 hello.txt");
});

describe("a message the session ended before sending", () => {
  function setup(ended: boolean, cancelEvent: boolean) {
    const r = demo();
    const s = r.sessions.get("s-idle")!;
    const push = (kind: object) => r.events("s-idle").push(create(EventSchema, { seq: BigInt(r.events("s-idle").length + 1), kind: kind as never }));
    (r as unknown as { outboxes: Map<string, unknown[]> }).outboxes.set("s-idle", [{ sendId: "m1", requestId: "x", deadline: 0, text: "顺便补一个测试", status: "sent" }]);
    push({ case: "inputState", value: { requestId: "m1", status: InputStatus.QUEUED } });
    if (cancelEvent) push({ case: "inputState", value: { requestId: "m1", status: InputStatus.CANCELED, reason: "exited" } });
    push({ case: "sessionState", value: { status: SessionStatus.EXITED } });
    s.info = { ...s.info!, status: ended ? SessionStatus.EXITED : SessionStatus.IDLE };
    mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
    return r;
  }

  it("says it was not sent, and can go back into the input after resuming", async () => {
    setup(false, true);
    expect(host.textContent).toContain("会话结束，没有发出");
    expect(host.textContent).not.toContain("取消了一条排队的消息");
    expect(host.querySelector(".queued")).toBeNull();
    await click(button("放回输入框"));
    expect(host.querySelector<HTMLTextAreaElement>("textarea[aria-label=消息]")!.value).toBe("顺便补一个测试");
  });

  it("is not left queued for ever when an older daemon recorded nothing", async () => {
    const r = setup(true, false);
    expect(host.textContent).toContain("会话结束，没有发出");
    expect(host.querySelector(".queued")).toBeNull();
    await click(button("放回输入框"));
    // No input while ended: the text waits as the draft for after resuming.
    expect(r.drafts.get("s-idle")).toBe("顺便补一个测试");
  });
});

it("lists every Claude Code model after switching away from haiku", async () => {
  const r = demo();
  const s = r.sessions.get("s-claude-idle")!;
  s.info = { ...s.info!, model: "claude-sonnet-5-5", effort: "medium" };
  const switched: unknown[] = [];
  const real = r.command.bind(r);
  r.command = async (sid: string, cmd: CommandKind, slot?: RequestSlot) => {
    if (cmd?.case === "setModel") switched.push(cmd.value);
    return real(sid, cmd, slot);
  };
  mount(<SessionSheet remote={r} s={s} running={false} onClose={() => undefined} />);
  await vi.waitFor(() => expect(button(/^Haiku/)).toBeTruthy(), { timeout: 3000 });
  expect(button(/^Opus/)).toBeTruthy();
  await click(button(/^Haiku/));
  // Haiku has no effort levels: none is sent.
  await vi.waitFor(() => expect(switched).toHaveLength(1));
  expect(switched[0]).toMatchObject({ model: "haiku", effort: "" });
});

it("names models in switch records as the chips do", async () => {
  const r = demo();
  const s = r.sessions.get("s-claude-idle")!;
  const push = (kind: object) => r.events("s-claude-idle").push(create(EventSchema, { seq: BigInt(r.events("s-claude-idle").length + 1), kind: kind as never }));
  push({ case: "turnContext", value: { permissionMode: "default", model: "claude-haiku-4-5-20251001" } });
  push({ case: "settingsChanged", value: { model: "sonnet", effort: "medium" } });
  push({ case: "turnContext", value: { permissionMode: "default", model: "claude-sonnet-5-5", effort: "medium" } });
  push({ case: "turnContext", value: { permissionMode: "default", model: "claude-opus-5-5", effort: "medium" } });
  // The model list has come, as it has once the session page asks for it.
  await r.listModels("mac", Agent.CLAUDE);
  mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
  const lines = [...host.querySelectorAll(".sysline")].map((l) => l.textContent);
  expect(lines).toContain("切换到 Sonnet · medium");
  expect(lines.some((l) => l?.includes("模型切到了 Opus · medium"))).toBe(true);
});

describe("a new-session draft", () => {
  const type = async (value: string) =>
    act(async () => {
      const input = host.querySelector<HTMLTextAreaElement>("textarea[aria-label=新会话的第一句话]")!;
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, value);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
  const capture = (r: DemoRemote) => {
    const sent: { cwd?: string; agent?: number; mode?: number; confirm?: boolean }[] = [];
    r.machineCommand = async (m: string, cmd: CommandKind, slot?: RequestSlot): Promise<CommandResult> => {
      if (cmd?.case === "createSession") {
        sent.push({ cwd: cmd.value.cwd, agent: cmd.value.agent, mode: cmd.value.mode, confirm: cmd.value.confirmFullAccess });
        throw new CommandError("failed_precondition", "stop here");
      }
      void slot;
      return DemoRemote.prototype.machineCommand.call(r, m, cmd);
    };
    return sent;
  };
  const bump = (r: DemoRemote, sid: string) =>
    act(() => {
      r.sessions.get(sid)!.updatedMs = BigInt(Date.now() + 60_000);
      (r as unknown as { changed(): void }).changed();
    });

  it("keeps the project it was begun in on the wide home", async () => {
    const r = demo();
    const sent = capture(r);
    mount(<WideHome remote={r} go={() => undefined} />);
    await vi.waitFor(() => expect(host.textContent).toContain("开始一个任务"));
    const before = host.querySelector(".wide-project-tools .chip")!.textContent;
    await type("加限流");
    // Another project becomes the latest meanwhile.
    bump(r, "s-plan");
    expect(host.querySelector(".wide-project-tools .chip")!.textContent).toBe(before);
    await vi.waitFor(() => expect(button("↑")?.disabled).toBe(false));
    await click(button("↑"));
    await vi.waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0].cwd).toBe("/Users/me/Code/foo");
  });

  it("keeps the launch settings it was begun with", async () => {
    const r = demo();
    const sent = capture(r);
    mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} wide={false} />);
    await type("加限流");
    // The project's Claude Code session becomes its latest meanwhile.
    bump(r, "s-claude");
    expect(host.querySelector(".newbox .cli-pick")!.textContent).not.toContain("Claude Code");
    await vi.waitFor(() => expect(button("↑")?.disabled).toBe(false));
    await click(button("↑"));
    await vi.waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0].agent).toBe(Agent.CODEX);
  });

  it("asks before starting in full access that is only the machine's default", async () => {
    const r = demo();
    const sent = capture(r);
    // The machine reports Codex's default as full access.
    r.machines.get("mac")!.meta!.agents[0].defaultMode = ApprovalMode.FULL_ACCESS;
    r.draftMachines.set("/Users/me/Code/new", "mac");
    mount(<ProjectPage remote={r} pkey="/Users/me/Code/new" go={() => undefined} onBack={() => undefined} wide={false} />);
    await vi.waitFor(() => expect(host.querySelector(".newbox .cfg-main")!.textContent).toContain("完全放行"), { timeout: 3000 });
    await type("加限流");
    await click(button("↑"));
    expect(host.textContent).toContain("切到完全放行？");
    expect(sent).toHaveLength(0);
    await click(host.querySelector<HTMLButtonElement>(".alert .btn.danger")!);
    await vi.waitFor(() => expect(sent).toHaveLength(1));
    // Left to the default, sent as confirmed.
    expect(sent[0]).toMatchObject({ mode: ApprovalMode.UNSPECIFIED, confirm: true });
  });
});

describe("approvals whose answer was not confirmed (v2)", () => {
  function setup(error: CommandError) {
    const r = demo();
    const calls: { allow: boolean; requestId: string }[] = [];
    const real = r.command.bind(r);
    r.command = async (sid: string, cmd: CommandKind, slot?: RequestSlot): Promise<CommandResult> => {
      if (cmd?.case === "resolveApproval") {
        calls.push({ allow: !!cmd.value.allow, requestId: slot?.requestId ?? "" });
        if (calls.length === 1) throw error;
      }
      return real(sid, cmd, slot);
    };
    mount(<Home remote={r} go={() => undefined} />);
    return calls;
  }
  const card = () => host.querySelector(".todo")!;

  it("locks both choices and retries only the one made, as the same request", async () => {
    const calls = setup(new CommandError("unknown", "连接超时"));
    await click(button("允许", card()));
    await vi.waitFor(() => expect(card().textContent).toContain("未收到电脑确认：连接超时"), { timeout: 3000 });
    expect(button("拒绝", card())?.disabled).toBe(true);
    expect(button("允许", card())?.disabled).toBe(true);
    expect(button("附理由拒绝 ›", card())?.disabled).toBe(true);
    await click(button("重试", card()));
    await vi.waitFor(() => expect(calls.length).toBe(2));
    expect(calls[1]).toEqual(calls[0]);
    expect(calls[1].allow).toBe(true);
  });

  it("lets a definite failure be answered again either way", async () => {
    setup(new CommandError("unavailable", "电脑不在线，指令没有发出"));
    await click(button("允许", card()));
    await vi.waitFor(() => expect(card().textContent).toContain("没能提交"), { timeout: 3000 });
    expect(button("拒绝", card())?.disabled).toBe(false);
    expect(button("允许", card())?.disabled).toBe(false);
  });
});

describe("the queue in full (v2)", () => {
  it("says what became of a cancel", async () => {
    const { cancelOutcome } = await import("./Queue");
    expect(cancelOutcome(InputStatus.CANCELED, true)).toBe("已取消");
    expect(cancelOutcome(InputStatus.CANCELED, false)).toBe("上一条已在另一客户端取消");
    expect(cancelOutcome(InputStatus.SUBMITTED, true)).toBe("上一条已送入终端，不能取消");
    expect(cancelOutcome(InputStatus.UNCONFIRMED, true)).toBe("上一条已送入终端，不能取消");
    expect(cancelOutcome(InputStatus.SUBMITTED, false)).toBe("");
  });

  it("lists every queued message with its sender, and cancels one", async () => {
    const r = demo();
    const s = r.sessions.get("s-run")!;
    const push = (kind: object) => r.events("s-run").push(create(EventSchema, { seq: BigInt(r.events("s-run").length + 1), kind: kind as never }));
    // One more from another client, whose text this page does not know.
    push({ case: "inputState", value: { requestId: "other", status: InputStatus.QUEUED } });
    mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
    await click(button(/^排队 2/));
    const entries = () => [...host.querySelectorAll(".queue-entry")];
    expect(entries().length).toBe(2);
    expect(entries()[0].textContent).toContain("顺便补一个测试");
    expect(entries()[0].textContent).toContain("本客户端");
    expect(entries()[1].textContent).toContain("另一客户端");
    await click(button("取消", entries()[0]));
    expect(entries()[0].textContent).toContain("正在取消…");
    await vi.waitFor(() => expect(entries().length).toBe(1), { timeout: 3000 });
    expect(host.querySelector(".toast")?.textContent).toBe("已取消");
  });

  it("keeps a message whose cancel failed, and says so", async () => {
    const r = demo();
    const s = r.sessions.get("s-run")!;
    r.command = async () => {
      throw new CommandError("unavailable", "电脑不在线，指令没有发出");
    };
    mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
    await click(button(/^排队 1/));
    await click(button("取消", host.querySelector(".queue-entry")!));
    await vi.waitFor(() => expect(host.querySelector(".queue-entry")!.textContent).toContain("取消未成功，消息仍在队列中"));
  });
});

describe("switching a new session's CLI (v2)", () => {
  it("keeps the draft, and asks again when the new CLI's default is full access", async () => {
    const r = demo();
    // The machine reports Claude Code's own default as full access.
    const claude = r.machines.get("mac")!.meta!.agents[1];
    claude.defaultMode = ApprovalMode.FULL_ACCESS;
    claude.defaultModeSource = 2;
    const sent: { agent?: number; mode?: number; prompt?: string; confirm?: boolean }[] = [];
    r.machineCommand = async (m: string, cmd: CommandKind, slot?: RequestSlot): Promise<CommandResult> => {
      if (cmd?.case === "createSession") {
        sent.push({ agent: cmd.value.agent, mode: cmd.value.mode, prompt: cmd.value.prompt, confirm: cmd.value.confirmFullAccess });
        throw new CommandError("failed_precondition", "stop here");
      }
      void slot;
      return DemoRemote.prototype.machineCommand.call(r, m, cmd);
    };
    mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} wide={false} />);
    await act(async () => {
      const input = host.querySelector<HTMLTextAreaElement>("textarea[aria-label=新会话的第一句话]")!;
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, "加限流");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await click(host.querySelector<HTMLButtonElement>(".cli-pick")!);
    await click(button(/^Claude Code/));
    expect(host.querySelector(".cli-pick")!.textContent).toContain("Claude Code");
    expect(host.querySelector<HTMLTextAreaElement>("textarea[aria-label=新会话的第一句话]")!.value).toBe("加限流");
    await vi.waitFor(() => expect(host.querySelector(".newbox .cfg-main")!.textContent).toContain("完全放行"), { timeout: 3000 });
    await click(button("↑"));
    expect(host.textContent).toContain("切到完全放行？");
    expect(sent).toHaveLength(0);
    await click(host.querySelector<HTMLButtonElement>(".alert .btn.danger")!);
    await vi.waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0]).toMatchObject({ agent: Agent.CLAUDE, mode: ApprovalMode.UNSPECIFIED, confirm: true, prompt: "加限流" });
  });
});

describe("project management (v2)", () => {
  const names = (sel: string) => [...host.querySelectorAll(sel)].map((e) => e.firstChild?.textContent ?? "");

  it("removes a project from the list, keeps its waiting sessions, and undoes", async () => {
    const r = demo();
    mount(<ManagePage remote={r} go={() => undefined} onBack={() => undefined} wide={false} />);
    await vi.waitFor(() => expect(r.prefs.status).toBe("ready"));
    await click(host.querySelector<HTMLButtonElement>('[aria-label="管理 shop"]')!);
    await click(button("从项目列表移除"));
    await vi.waitFor(() => expect(host.querySelector(".toast")?.textContent).toContain("已移除 shop"), { timeout: 3000 });
    expect(names(".manage-row .pn")).not.toContain("shop");
    expect(host.textContent).toContain("shop 的会话和历史仍然保留");
    await click(button("撤销"));
    await vi.waitFor(() => expect(names(".manage-row .pn")).toContain("shop"), { timeout: 3000 });
    // Removed again, Home leaves it out of the projects, but its plan
    // still waits for the user.
    await r.changePrefs("hide", "/Users/me/Code/shop");
    act(() => root!.render(<Home remote={r} go={() => undefined} />));
    expect(names(".proj .pn")).not.toContain("shop");
    expect(names(".proj .pn")).toContain("foo");
    expect(todoList(r).map((x) => x.id)).toContain("s-plan");
  });

  it("keeps a removed project out of the new-session picker and asks to restore it first", async () => {
    const r = demo();
    await vi.waitFor(() => expect(r.prefs.status).toBe("ready"));
    await r.changePrefs("hide", "/Users/me/Code/shop");
    const { ProjectPicker } = await import("./NewSession");
    mount(<ProjectPicker remote={r} onPick={() => undefined} onNew={() => undefined} onClose={() => undefined} />);
    expect(host.textContent).not.toContain("~/Code/shop");
    expect(host.textContent).toContain("常用项目");
    act(() => root!.render(<ProjectPage remote={r} pkey="/Users/me/Code/shop" go={() => undefined} onBack={() => undefined} wide={false} />));
    expect(host.textContent).toContain("这个项目已从列表移除");
    expect(host.querySelector("textarea[aria-label=新会话的第一句话]")).toBeNull();
    // Its sessions are all still there.
    expect(host.textContent).toContain("拆分订单服务");
    await click(button("恢复到项目列表"));
    await vi.waitFor(() => expect(host.querySelector("textarea[aria-label=新会话的第一句话]")).toBeTruthy(), { timeout: 3000 });
  });

  it("shows a failed save as not done, and retries the same operation", async () => {
    const r = DemoRemote.create(new URLSearchParams("prefs=fail"));
    r.start();
    await vi.waitFor(() => expect(r.prefs.status).toBe("ready"));
    mount(<ManagePage remote={r} go={() => undefined} onBack={() => undefined} wide={false} />);
    await click(host.querySelector<HTMLButtonElement>('[aria-label="管理 bar"]')!);
    await click(button("设为常用"));
    await vi.waitFor(() => expect(host.textContent).toContain("没能保存，列表保持原样"), { timeout: 5000 });
    expect(host.querySelector(".label")?.textContent).not.toContain("2");
    const id = r.prefs.failed!.op.id;
    await click(button("重试"));
    await vi.waitFor(() => expect(r.prefs.pending.size).toBe(1));
    await vi.waitFor(() => expect(r.prefs.failed?.op.id).toBe(id), { timeout: 5000 });
  }, 15_000);

  it("pauses changes while disconnected and says the list is cached", async () => {
    const r = DemoRemote.create(new URLSearchParams("conn=reconnecting"));
    r.start();
    await vi.waitFor(() => expect(r.prefs.status).toBe("ready"));
    mount(<ManagePage remote={r} go={() => undefined} onBack={() => undefined} wide={false} />);
    expect(host.textContent).toContain("当前显示缓存 · 连上后才能管理");
    await click(host.querySelector<HTMLButtonElement>('[aria-label="管理 bar"]')!);
    expect(button("设为常用")).toBeUndefined();
  });
});

describe("who did what (v2)", () => {
  it("names verified clients, and says another client or the terminal otherwise", () => {
    const r = demo();
    const s = r.sessions.get("s-multi")!;
    return (async () => {
      await vi.waitFor(() => expect(r.devicesStatus).toBe("ready"));
      mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
      const notes = [...host.querySelectorAll(".me small")].map((x) => x.textContent);
      expect(notes).toEqual(["本客户端 · Chrome", "Android · b71c", "VPS-dev 本地终端"]);
      expect(host.textContent).toContain("另一客户端将模式切到 计划");
      expect(host.querySelector(".record")?.textContent).toContain("已允许 · Android · b71c");
      await click(button(/^排队 2/));
      const entries = [...host.querySelectorAll(".queue-entry")].map((x) => x.textContent);
      expect(entries[0]).toContain("本客户端");
      expect(entries[0]).toContain("补充库存回滚测试。");
      expect(entries[1]).toContain("Android · b71c");
      expect(entries[1]).toContain("最后检查 README。");
    })();
  });

  it("offers a retry for a message that failed before Enter, and the terminal when it may be half written", async () => {
    const r = demo();
    const s = r.sessions.get("s-msg")!;
    mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
    expect(host.textContent).toContain("没能发送：Codex 没认出图片");
    expect(host.textContent).toContain("终端里可能留着这条消息的一部分");
    const failed = [...host.querySelectorAll(".me.fail")].find((x) => x.textContent?.includes("Codex 没认出图片"))!;
    await click(button("重试", failed));
    expect(r.outbox("s-msg").at(-1)?.text).toBe("这两个页面底部的按钮都点不到");
  });
});

describe("pictures in a session (v2)", () => {
  it("numbers a message's pictures, chips placeholders among the words, and opens the viewer", async () => {
    const r = demo();
    const s = r.sessions.get("s-img-claude")!;
    mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
    const first = host.querySelector(".me")!;
    expect([...first.querySelectorAll(".pic .n")].map((n) => n.textContent)).toEqual(["1", "2"]);
    expect(first.textContent).toContain("电脑上，图 2手机上");
    expect(first.textContent).not.toContain("[Image #");
    // Seven pictures: four in a row, the rest folded.
    expect(host.textContent).toContain("+3");
    await click(host.querySelector<HTMLButtonElement>(".pic")!);
    expect(host.querySelector(".viewer")?.textContent).toContain("1 / 14");
  });

  it("says what the viewer can show for each state of the display copy", async () => {
    const r = demo();
    const s = r.sessions.get("s-img-claude")!;
    // Tests have no canvas to draw the demo's previews: give them bytes.
    for (const e of r.events("s-img-claude"))
      if (e.kind.case === "toolCall" || e.kind.case === "userMessage")
        for (const ref of e.kind.value.images) if (ref.sync !== 3) ref.preview = Uint8Array.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
    mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
    const open = async (label: string) => {
      await click(host.querySelector<HTMLButtonElement>(`.pic[aria-label="${label}"]`)!);
    };
    const note = () => host.querySelector(".viewer .vnote")?.textContent;
    await open("图片没有同步");
    expect(note()).toBe("图片没有同步");
    await click(button("回到消息"));
    await open("图片 wide.png");
    expect(note()).toBe("只有预览，原图在电脑上");
    await click(button("回到消息"));
    await open("图片 old.png");
    await vi.waitFor(() => expect(note()).toBe("大图已过期，只剩预览"), { timeout: 3000 });
    await click(button("回到消息"));
    await open("图片 flaky.png");
    await vi.waitFor(() => expect(note()).toBe("没能下载 · 点按重试"), { timeout: 3000 });
    await click(host.querySelector<HTMLButtonElement>(".viewer .vnote")!);
    await vi.waitFor(() => expect(note()).not.toBe("没能下载 · 点按重试"), { timeout: 3000 });
  });

  it("shows only a count from a daemon older than v2", () => {
    const r = demo();
    const s = r.sessions.get("s-idle")!;
    r.events("s-idle").push(create(EventSchema, { seq: 3n, kind: { case: "userMessage", value: { text: "看图", imageCount: 2 } } }));
    mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
    expect(host.textContent).toContain("2 张图片（电脑端版本较旧，这里看不到）");
  });
});

it("revokes every blob: URL made for a picture once nothing shows it", async () => {
  const made: string[] = [];
  const revoked: string[] = [];
  const create_ = vi.spyOn(URL, "createObjectURL").mockImplementation(() => {
    const u = "blob:test/" + made.length;
    made.push(u);
    return u;
  });
  const revoke = vi.spyOn(URL, "revokeObjectURL").mockImplementation((u) => void revoked.push(u));
  try {
    const { Pic } = await import("./Images");
    const { ImageRefSchema, ImageFormat, ImageSync } = await import("../gen/remoteagent/v1/event_pb");
    const ref = create(ImageRefSchema, { sync: ImageSync.SYNCED, previewFormat: ImageFormat.PNG, preview: Uint8Array.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]), width: 10, height: 10 });
    mount(<Pic image={ref} />);
    expect(made.length).toBe(1);
    expect(host.querySelector("img")?.getAttribute("src")).toBe(made[0]);
    act(() => root!.unmount());
    root = undefined;
    expect(revoked).toEqual(made);
    // A preview that is not the format it claims is never given a URL.
    const liar = create(ImageRefSchema, { sync: ImageSync.SYNCED, previewFormat: ImageFormat.PNG, preview: new TextEncoder().encode("<svg/>") });
    mount(<Pic image={liar} />);
    expect(made.length).toBe(1);
  } finally {
    create_.mockRestore();
    revoke.mockRestore();
  }
});

describe("sending images (v2)", () => {
  function setup(fail: (name: string) => boolean) {
    const r = demo();
    r.prepareImage = async (f) => Object.assign({ bytes: new Uint8Array(4), format: "png" as const, width: 1, height: 1 }, { name: (f as File).name });
    r.uploadImage = async (_m, p) => {
      const name = (p as { name?: string }).name ?? "";
      if (fail(name)) throw new CommandError("unavailable", "连接超时");
      return "id-" + name;
    };
    const sent: { text?: string; imageIds?: string[] }[] = [];
    const real = r.command.bind(r);
    r.command = async (sid: string, cmd: CommandKind, slot?: RequestSlot) => {
      if (cmd?.case === "send") sent.push({ text: cmd.value.text, imageIds: cmd.value.imageIds });
      return real(sid, cmd, slot);
    };
    const s = r.sessions.get("s-idle")!;
    mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
    const pick = async (...names: string[]) =>
      act(async () => {
        const input = host.querySelector<HTMLInputElement>("input[type=file]")!;
        Object.defineProperty(input, "files", { value: names.map((n) => new File(["x"], n, { type: "image/png" })), configurable: true });
        input.dispatchEvent(new Event("change", { bubbles: true }));
      });
    return { r, sent, pick };
  }

  it("never sends a message with fewer images than shown", async () => {
    const t = setup((n) => n === "b.png");
    await t.pick("a.png", "b.png");
    await vi.waitFor(() => expect(host.textContent).toContain("1 张图没能传到电脑"));
    const send = () => host.querySelector<HTMLButtonElement>('button[aria-label="发送"]')!;
    expect(send().disabled).toBe(true);
    // Removing the failed one lets the rest go, with only their IDs.
    await click(host.querySelectorAll<HTMLButtonElement>(".att-tray .x")[1]);
    await vi.waitFor(() => expect(send().disabled).toBe(false));
    await click(send());
    await vi.waitFor(() => expect(t.sent).toHaveLength(1));
    expect(t.sent[0]).toEqual({ text: "", imageIds: ["id-a.png"] });
    expect(host.querySelector(".att-tray")).toBeNull();
  });

  it("takes six at most from one pick and says the rest were left out", async () => {
    const t = setup(() => false);
    await t.pick(...[1, 2, 3, 4, 5, 6, 7, 8].map((i) => `p${i}.png`));
    expect(host.querySelectorAll(".att-tray .pic").length).toBe(6);
    expect(host.textContent).toContain("一条消息最多 6 张，多选的 2 张没有加入");
    expect(host.querySelector(".ctl.att")!.classList.contains("off")).toBe(true);
  });

  it("offers no ＋ on a computer whose daemon cannot take images", async () => {
    const r = demo();
    const s = r.sessions.get("s-appr")!; // on VPS-dev, an older daemon
    mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
    await click(host.querySelector<HTMLButtonElement>(".ctl.att")!);
    expect(host.querySelector("input[type=file]")).toBeTruthy();
    expect(host.textContent).toContain("VPS-dev 上的 remote-agent 版本较旧，不能发图片");
  });
});

it("keeps a submitted message in view until the CLI records it", async () => {
  const r = demo();
  const s = r.sessions.get("s-idle")!;
  const push = (kind: object) => r.events("s-idle").push(create(EventSchema, { seq: BigInt(r.events("s-idle").length + 1), kind: kind as never }));
  (r as unknown as { outboxes: Map<string, unknown[]> }).outboxes.set("s-idle", [{ sendId: "m1", requestId: "x", deadline: 0, text: "运行中插一句", status: "sent" }]);
  push({ case: "inputState", value: { requestId: "m1", status: InputStatus.QUEUED, text: "运行中插一句" } });
  push({ case: "inputState", value: { requestId: "m1", status: InputStatus.SUBMITTED } });
  mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
  expect(host.textContent).toContain("运行中插一句");
  expect(host.textContent).toContain("已送入终端");
  act(() => {
    push({ case: "userMessage", value: { text: "运行中插一句", requestId: "m1" } });
    r.touch();
  });
  expect([...host.querySelectorAll(".me")].filter((m) => m.textContent?.includes("运行中插一句")).length).toBe(1);
  expect(host.textContent).not.toContain("已送入终端");
});

it("sends a failed message again only once, however often retry is pressed", async () => {
  const r = demo();
  const s = r.sessions.get("s-msg")!;
  mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
  const before = r.outbox("s-msg").length;
  const failed = () => [...host.querySelectorAll(".me.fail")].find((x) => x.textContent?.includes("Codex 没认出图片"))!;
  const retry = () => button("重试", failed());
  await act(async () => {
    retry()!.click();
    retry()?.click();
  });
  expect(r.outbox("s-msg").length).toBe(before + 1);
  // Once replaced, the failed row offers no second retry.
  expect(retry()?.disabled ?? true).toBe(true);
});

describe("the first message of a session started here (review B5, B9)", () => {
  function setup() {
    const r = demo();
    const s = r.sessions.get("s-fresh")!;
    r.prompts.set("s-fresh", "给登录接口加限流");
    const push = (kind: object) =>
      act(() => {
        r.events("s-fresh").push(create(EventSchema, { seq: BigInt(r.events("s-fresh").length + 1), kind: kind as never }));
        r.touch();
      });
    push({ case: "inputState", value: { requestId: "p1", status: InputStatus.QUEUED, text: "给登录接口加限流" } });
    mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
    return { r, push };
  }

  it("shows its real state, not 已送达 after another client canceled it", () => {
    const { push } = setup();
    expect(host.textContent).toContain("已送达电脑，等 Codex 就绪后发出");
    push({ case: "inputState", value: { requestId: "p1", status: InputStatus.CANCELED } });
    expect(host.textContent).not.toContain("已送达电脑");
    expect(host.textContent).toContain("取消了一条排队的消息");
  });

  it("is in the shared queue, where it can be canceled", async () => {
    setup();
    await click(button(/^排队 1/));
    const entry = host.querySelector(".queue-entry")!;
    expect(entry.textContent).toContain("给登录接口加限流");
    expect(button("取消", entry)).toBeTruthy();
  });

});

describe("a failed message with images (review B6)", () => {
  function setup(text: string) {
    const r = demo();
    const s = r.sessions.get("s-idle")!;
    const blob = new Blob([new Uint8Array(4)], { type: "image/png" });
    (r as unknown as { outboxes: Map<string, unknown[]> }).outboxes.set("s-idle", [{ sendId: "f1", requestId: "x", deadline: 0, text, imageIds: ["i1", "i2"], thumbs: [blob, blob], status: "sent" }]);
    for (const kind of [
      { case: "inputState", value: { requestId: "f1", status: InputStatus.QUEUED, text, imageIds: ["i1", "i2"] } },
      { case: "inputState", value: { requestId: "f1", status: InputStatus.FAILED, reason: "image_not_recognized" } },
    ])
      r.events("s-idle").push(create(EventSchema, { seq: BigInt(r.events("s-idle").length + 1), kind: kind as never }));
    const sent: { text?: string; imageIds?: string[] }[] = [];
    const real = r.command.bind(r);
    r.command = async (sid: string, cmd: CommandKind, slot?: RequestSlot) => {
      if (cmd?.case === "send") sent.push({ text: cmd.value.text, imageIds: cmd.value.imageIds });
      return real(sid, cmd, slot);
    };
    mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
    return { r, sent };
  }
  const row = () => host.querySelector(".me.fail")!;

  it("puts text and images back into the input, to send them again", async () => {
    const t = setup("看这两张");
    await click(button("放回输入框", row()));
    expect(host.querySelector<HTMLTextAreaElement>("textarea[aria-label=消息]")!.value).toBe("看这两张");
    expect(host.querySelectorAll(".att-tray .pic").length).toBe(2);
    await click(host.querySelector<HTMLButtonElement>('button[aria-label="发送"]')!);
    await vi.waitFor(() => expect(t.sent).toHaveLength(1));
    expect(t.sent[0]).toEqual({ text: "看这两张", imageIds: ["i1", "i2"] });
  });

  it("can retry or restore a message of images only", async () => {
    const t = setup("");
    expect(button("放回输入框", row())).toBeTruthy();
    await click(button("重试", row()));
    await vi.waitFor(() => expect(t.sent).toHaveLength(1));
    expect(t.sent[0]).toEqual({ text: "", imageIds: ["i1", "i2"] });
  });
});

describe("an image-only new-session draft (review B8)", () => {
  const pick = () =>
    act(async () => {
      const input = host.querySelector<HTMLInputElement>("input[type=file]")!;
      Object.defineProperty(input, "files", { value: [new File(["x"], "a.png", { type: "image/png" })], configurable: true });
      input.dispatchEvent(new Event("change", { bubbles: true }));
    });
  const stub = (r: DemoRemote) => {
    r.prepareImage = async () => ({ bytes: new Uint8Array(4), format: "png", width: 1, height: 1 });
    r.uploadImage = async () => "id-a";
  };
  const bump = (r: DemoRemote, sid: string) =>
    act(() => {
      r.sessions.get(sid)!.updatedMs = BigInt(Date.now() + 60_000);
      r.touch();
    });

  it("keeps its launch settings once an image is chosen", async () => {
    const r = demo();
    stub(r);
    mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} wide={false} />);
    await pick();
    bump(r, "s-claude");
    expect(host.querySelector(".newbox .cli-pick")!.textContent).not.toContain("Claude Code");
  });

  it("keeps its project on the wide home once an image is chosen", async () => {
    const r = demo();
    stub(r);
    mount(<WideHome remote={r} go={() => undefined} />);
    await vi.waitFor(() => expect(host.textContent).toContain("开始一个任务"));
    const before = host.querySelector(".wide-project-tools .chip")!.textContent;
    await pick();
    bump(r, "s-plan");
    expect(host.querySelector(".wide-project-tools .chip")!.textContent).toBe(before);
    expect(host.querySelectorAll(".att-tray .pic").length).toBe(1);
  });
});

it("loads a picture opened offline once the connection is back (review B11)", async () => {
  const r = DemoRemote.create(new URLSearchParams("conn=reconnecting"));
  r.start();
  const s = r.sessions.get("s-img-claude")!;
  for (const e of r.events("s-img-claude"))
    if (e.kind.case === "toolCall" || e.kind.case === "userMessage") for (const ref of e.kind.value.images) if (ref.sync !== 3) ref.preview = Uint8Array.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
  mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
  await click(host.querySelector<HTMLButtonElement>('.pic[aria-label="图片 old.png"]')!);
  const note = () => host.querySelector(".viewer .vnote")?.textContent;
  expect(note()).toBe("连上后加载");
  act(() => {
    r.conn = "online";
    r.touch();
  });
  await vi.waitFor(() => expect(note()).toBe("大图已过期，只剩预览"), { timeout: 3000 });
});

it("asks to restore a project removed while its draft is on the wide home (review B12)", async () => {
  const r = demo();
  await vi.waitFor(() => expect(r.prefs.status).toBe("ready"));
  mount(<WideHome remote={r} go={() => undefined} />);
  await vi.waitFor(() => expect(host.textContent).toContain("开始一个任务"));
  await act(async () => {
    const input = host.querySelector<HTMLTextAreaElement>("textarea[aria-label=新会话的第一句话]")!;
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, "加限流");
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await act(async () => {
    await r.changePrefs("hide", "/Users/me/Code/foo");
  });
  expect(host.textContent).toContain("这个项目已从列表移除");
  expect(host.querySelector<HTMLButtonElement>('button[aria-label="开始新会话"]')!.disabled).toBe(true);
  expect(host.querySelector<HTMLTextAreaElement>("textarea[aria-label=新会话的第一句话]")!.value).toBe("加限流");
  await click(button("恢复到项目列表"));
  await vi.waitFor(() => expect(host.querySelector<HTMLButtonElement>('button[aria-label="开始新会话"]')!.disabled).toBe(false), { timeout: 3000 });
});

it("marks a quota window as waiting for an update once its reset time passes (review B13)", async () => {
  vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval", "setTimeout", "clearTimeout"] });
  try {
    const r = demo();
    const { QuotaPage } = await import("./Settings");
    mount(<QuotaPage remote={r} id="mac" go={() => undefined} onBack={() => undefined} />);
    expect(host.textContent).not.toContain("等待更新");
    // The demo's 5-hour window resets in three hours; nothing else changes.
    act(() => void vi.advanceTimersByTime(3 * 60 * 60_000 + 60_000));
    expect(host.textContent).toContain("等待更新");
  } finally {
    vi.useRealTimers();
  }
});

it("asks and sends again confirmed when the daemon finds the folder starts in full access", async () => {
  const r = demo();
  const sent: { id: string; confirm?: boolean }[] = [];
  r.machineCommand = async (m: string, cmd: CommandKind, slot?: RequestSlot): Promise<CommandResult> => {
    if (cmd?.case === "createSession") {
      sent.push({ id: slot!.requestId, confirm: cmd.value.confirmFullAccess });
      if (!cmd.value.confirmFullAccess) throw new CommandError("failed_precondition", "codex starts in full access here by default; confirm it or choose another mode");
      throw new CommandError("failed_precondition", "stop here");
    }
    return DemoRemote.prototype.machineCommand.call(r, m, cmd);
  };
  mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} wide={false} />);
  await act(async () => {
    const input = host.querySelector<HTMLTextAreaElement>("textarea[aria-label=新会话的第一句话]")!;
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, "加限流");
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await click(button("↑"));
  await vi.waitFor(() => expect(host.textContent).toContain("切到完全放行？"));
  await click(host.querySelector<HTMLButtonElement>(".alert .btn.danger")!);
  await vi.waitFor(() => expect(sent).toHaveLength(2));
  expect(sent[1].confirm).toBe(true);
  expect(sent[1].id).not.toBe(sent[0].id);
});

it("starts a new session with images and no words (Go 61c4bb2)", async () => {
  const r = demo();
  r.prepareImage = async () => ({ bytes: new Uint8Array(4), format: "png", width: 1, height: 1 });
  r.uploadImage = async () => "id-a";
  const sent: { prompt?: string; imageIds?: string[] }[] = [];
  r.machineCommand = async (m: string, cmd: CommandKind, slot?: RequestSlot): Promise<CommandResult> => {
    if (cmd?.case === "createSession") {
      sent.push({ prompt: cmd.value.prompt, imageIds: cmd.value.imageIds });
      throw new CommandError("failed_precondition", "stop here");
    }
    void slot;
    return DemoRemote.prototype.machineCommand.call(r, m, cmd);
  };
  mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} wide={false} />);
  await act(async () => {
    const input = host.querySelector<HTMLInputElement>("input[type=file]")!;
    Object.defineProperty(input, "files", { value: [new File(["x"], "a.png", { type: "image/png" })], configurable: true });
    input.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await vi.waitFor(() => expect(host.querySelector<HTMLButtonElement>('button[aria-label="开始新会话"]')!.disabled).toBe(false));
  await click(host.querySelector<HTMLButtonElement>('button[aria-label="开始新会话"]')!);
  await vi.waitFor(() => expect(sent).toHaveLength(1));
  expect(sent[0]).toEqual({ prompt: "", imageIds: ["id-a"] });
});

it("shows Claude Code's collected quota on the quota page (Go ed1a14d)", async () => {
  const r = demo();
  const { AgentQuotaSchema, QuotaState } = await import("../gen/remoteagent/v1/remote_pb");
  const m = r.machines.get("mac")!;
  const now = Date.now();
  m.meta!.quotas = [
    create(AgentQuotaSchema, { agent: Agent.CODEX, state: QuotaState.WAITING }),
    create(AgentQuotaSchema, { agent: Agent.CLAUDE, state: QuotaState.OK, observedMs: BigInt(now), windows: [{ name: "five_hour", usedPercent: 28, resetsAtMs: BigInt(now + 3_600_000), windowMinutes: 300n }] }),
  ];
  const { QuotaPage } = await import("./Settings");
  mount(<QuotaPage remote={r} id="mac" go={() => undefined} onBack={() => undefined} />);
  expect(host.textContent).not.toContain("暂不支持读取额度");
  expect(host.textContent).toContain("已用 28%");
  expect(host.textContent).toContain("暂未获取额度");
  expect(host.textContent).toContain("7 天未获取");
});
