// @vitest-environment happy-dom
// Interaction regressions: the pages rendered against the demo Remote,
// clicked through as a user would.

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { DemoRemote } from "../dev/demo";
import { create } from "@bufbuild/protobuf";
import { Agent, ApprovalDecision, ApprovalMode, EventSchema, PlanProposedSchema, ToolKind } from "../gen/remoteagent/v1/event_pb";
import { derive, dockItems } from "../model";
import { CommandResultSchema, type CommandResult } from "../gen/remoteagent/v1/remote_pb";
import { CommandError, type CommandKind, type RequestSlot } from "../remote";
import { ModeSheet, SessionSheet } from "../sheets";
import { Dock, PlanPage, QuestionPage } from "./Dock";
import { Home } from "./Home";
import { loadProjectConfig } from "../storage";
import { useNewSession } from "./NewSession";
import { ProjectPage } from "./Project";
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
  const line = () => host.querySelector(".newbox .cfg em")!.textContent ?? "";
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
  expect(host.textContent).toContain("启动时没有允许完全放行");
  expect(button(/^自动/)?.disabled).toBe(true);
  expect(button(/^询问/)?.disabled).toBe(false);
  // acceptEdits is none of the four: nothing is ticked, and it says so.
  expect(host.querySelector(".ck")).toBeNull();
  expect(host.textContent).toContain("acceptEdits");
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
  await click(button("更改"));
  await click(button(/^模型/));
  await act(async () => { await Promise.all([r.listModels("mac", Agent.CODEX), r.settings("mac")]); });
  await click(button("GPT-6-Sol"));
  await click(button("low"));
  await click(host.querySelector<HTMLButtonElement>('button[aria-label="返回"]')!);
  await click(button("完成"));
  expect(host.querySelector(".cfg")?.textContent).toContain("GPT-6-Sol low");
  // Rebuild both the page and Remote, as a reload would; old sessions
  // still have Astra, so inheritance cannot make this pass by accident.
  act(() => root!.unmount());
  host.remove();
  const reloaded = demo();
  mount(page(reloaded));
  await act(async () => { await Promise.all([reloaded.listModels("mac", Agent.CODEX), reloaded.settings("mac")]); });
  expect(host.querySelector(".cfg")?.textContent).toContain("GPT-6-Sol low");
  const real = reloaded.machineCommand.bind(reloaded);
  let sent: unknown;
  reloaded.machineCommand = async (machine, cmd) => {
    if (cmd?.case === "createSession") sent = cmd.value;
    return real(machine, cmd);
  };
  await click(host.querySelector<HTMLButtonElement>('button[aria-label="开始新会话"]')!);
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
