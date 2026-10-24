// @vitest-environment happy-dom
// Interaction regressions: the pages rendered against the demo Remote,
// clicked through as a user would.

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ANDROID, DemoRemote } from "../dev/demo";
import { create } from "@bufbuild/protobuf";
import { MachineMetaSchema } from "../gen/remoteagent/v1/remote_pb";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { Agent, ApprovalDecision, ApprovalMode, EventSchema, InputStatus, OriginKind, PlanProposedSchema, SessionStatus, ToolKind } from "../gen/remoteagent/v1/event_pb";
import { derive, dockItems } from "../model";
import { DaemonSettingsSchema } from "../gen/remoteagent/v1/local_pb";
import { CommandResultSchema, type CommandResult } from "../gen/remoteagent/v1/remote_pb";
import { CommandError, type CommandKind, type RequestSlot } from "../remote";
import { ModeSheet, SessionSheet } from "../sheets";
import { Dock, dockContentMax, PlanPage, QuestionPage } from "./Dock";
import { Home, Sidebar, todoList, WideHome } from "./Home";
import { ALL, saveScope } from "../scope";
import { loadProjectConfig } from "../storage";
import { NewBox, NewFlow, ProjectPicker, useNewSession } from "./NewSession";
import { ProjectPage } from "./Project";
import { addProject, ManagePage, sessionCounts } from "./Projects";
import { SessionPage } from "./Session";
import { useRemote } from "../ui";

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

// Commands remain asynchronous; only the demo's presentation delay is skipped.
function createDemo(q: URLSearchParams, pause = async (_ms: number) => undefined): DemoRemote {
  return DemoRemote.create(q, pause);
}

function barrier() {
  let release!: () => void;
  const wait = new Promise<void>((done) => { release = done; });
  return { wait, release };
}

function demo(): DemoRemote {
  const r = createDemo(new URLSearchParams());
  r.start();
  return r;
}

const button = (text: string | RegExp, within: ParentNode = host) =>
  [...within.querySelectorAll("button")].find((b) => (typeof text === "string" ? b.textContent === text : text.test(b.textContent ?? ""))) as HTMLButtonElement | undefined;

/** The round button at the input's right: sends, or starts a new session. */
const sendButton = () => host.querySelector<HTMLButtonElement>('button[aria-label="发送"], button[aria-label="开始新会话"]') ?? undefined;

async function click(b: HTMLElement | undefined) {
  expect(b).toBeTruthy();
  await act(async () => b!.click());
}

/** Opens the project's settings, or an item of a session's ⋯ menu. */
async function fromMenu(item: string | RegExp, within: ParentNode = host) {
  if (item === "新会话设置") return click(within.querySelector<HTMLButtonElement>(".project-summary")!);
  await click(within.querySelector<HTMLButtonElement>(".navbar .more")!);
  await click(button(item));
}

/** A row's value in the new-session settings. */
const setting = (name: string) =>
  [...document.querySelectorAll(".sr")].find((r) => r.querySelector(".lbl")?.textContent === name)?.querySelector(".v")?.textContent ?? "";

it("keeps the context warning and the settings behind ⋯ on a narrow phone", async () => {
  const viewport = (window as unknown as { happyDOM: { setViewport(size: { width: number; height: number }): void } }).happyDOM;
  const r = demo();
  const s = r.sessions.get("s-ctx")!;
  try {
    act(() => viewport.setViewport({ width: 320, height: 740 }));
    mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
    // ⋯ carries the high context as a dot, and its menu the reading; ＋
    // only adds images.
    expect(host.querySelector(".more.context-high")?.getAttribute("aria-label")).toContain("上下文 86%");
    expect(host.querySelector(".cm-plus")?.getAttribute("aria-label")).toBe("添加图片");
    expect(host.querySelector('button[aria-label="发送"]')).toBeTruthy();
    await fromMenu("上下文86%");
    expect(host.textContent).toContain("会话设置");
  } finally {
    act(() => viewport.setViewport({ width: 1024, height: 768 }));
  }
});

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
  expect(button("不允许", card())?.disabled).toBe(false);
});

it("gives an open dock about 60% of the chat column, 240 at the least, within its room", () => {
  // A phone: 700 high, of which the dock may take 470 under its line.
  expect(dockContentMax(470, 700)).toBe(420);
  // A short window: still 240, if the room allows it.
  expect(dockContentMax(300, 320)).toBe(240);
  expect(dockContentMax(220, 320)).toBe(176);
  // Not measured yet: the stylesheet's 240.
  expect(dockContentMax(Infinity, 0)).toBeUndefined();
});

function AskDock({ r, room, drafting = false }: { r: DemoRemote; room?: number; drafting?: boolean }) {
  const s = r.sessions.get("s-ask")!;
  const d = derive(r.events("s-ask"));
  return (
    <Dock
      remote={r}
      sid="s-ask"
      items={dockItems(s, d, r.machines.get(s.machineId))}
      agent={Agent.CODEX}
      drafting={drafting}
      room={room}
      onExpand={() => undefined}
      onDetail={() => undefined}
      onPlan={() => undefined}
      onTerminal={() => undefined}
    />
  );
}

it("starts an item as one line only when it comes while a draft is in the input", async () => {
  // Focus alone (a computer keeps it after sending) leaves a new item open.
  const r = demo();
  mount(<AskDock r={r} />);
  expect(host.querySelector(".pending.open")).toBeTruthy();
  // Typing afterwards does not fold the open one.
  act(() => root!.render(<AskDock r={r} drafting />));
  expect(host.querySelector(".pending.open")).toBeTruthy();
  // With a draft in the input a new item comes as one line, and opens on a tap.
  const r2 = demo();
  act(() => root!.render(<AskDock r={r2} drafting />));
  expect(host.querySelector(".pending:not(.open)")).toBeTruthy();
  await click(host.querySelector<HTMLButtonElement>(".pending-sum")!);
  expect(host.querySelector(".pending.open")).toBeTruthy();
  act(() => root!.render(<AskDock r={r2} drafting />));
  expect(host.querySelector(".pending.open")).toBeTruthy();
});

it("keeps a question's progress across collapsing the dock", async () => {
  const r = demo();
  mount(<AskDock r={r} />);
  await click(button(/^只升小版本/));
  await vi.waitFor(() => expect(host.textContent).toContain("第 2 题，共 2 题"), { timeout: 3000 });
  // The keyboard comes up and leaves no room, then goes away again.
  act(() => root!.render(<AskDock r={r} room={100} />));
  expect(host.querySelector(".pending:not(.open)")).toBeTruthy();
  act(() => root!.render(<AskDock r={r} />));
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
  mount(<AskDock r={r} />);
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
      drafting={false}
      onExpand={() => undefined}
      onDetail={() => undefined}
      onPlan={() => undefined}
      onTerminal={() => undefined}
    />,
  );
  const dock = host.querySelector(".dock")!;
  const scroll = dock.querySelector(".pending-scroll")!;
  expect(scroll.textContent).toContain("rm -rf gen/remoteagent");
  for (const b of ["不允许", "允许"]) {
    const el = button(b, dock)!;
    expect(scroll.contains(el)).toBe(false);
  }
});

/** What the project has stored for new sessions of a CLI. */
const savedSet = (r: DemoRemote, path: string, agent = Agent.CODEX) => r.projectPrefs().settings.find((s) => s.path === path)?.agents.find((a) => a.agent === agent);

it("keeps saved project settings as sessions change", () => {
  const r = demo();
  mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} />);
  // The remembered CLI is the one picked next to the box and the one the summary names.
  const line = () => `${host.querySelector(".launch .pick .on")?.textContent} / ${host.querySelector(".phead .project-summary")?.textContent}`;
  expect(line()).toMatch(/^Codex \/ Codex · /);
  // The Claude Code session in foo becomes the most recent one.
  act(() => {
    r.sessions.get("s-claude")!.updatedMs = BigInt(Date.now() + 60_000);
    (r as unknown as { changed(): void }).changed();
  });
  expect(line()).toMatch(/^Codex \/ Codex · /);
});

it("leaves an unknown approval mode unsaid in the navbar and follows the CLI in a new session", async () => {
  const r = demo();
  // s-run's CLI has not said its mode.
  mount(<SessionPage remote={r} session={r.sessions.get("s-run")!} onBack={() => undefined} wide={false} />);
  expect(host.querySelector(".navbar .title > span")!.textContent).toBe("运行中");
  act(() => root!.unmount());
  host.remove();
  // Neither the project nor the machine says how a new Codex session starts.
  for (const a of r.machines.get("mac")!.meta!.agents) a.defaultModeSource = 0;
  savedSet(r, "/Users/me/Code/foo")!.mode = ApprovalMode.UNSPECIFIED;
  mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} />);
  await vi.waitFor(() => expect(host.querySelector(".project-summary")?.textContent).toMatch(/^Codex · 跟随 CLI 设置/));
  await fromMenu("新会话设置");
  expect(setting("审批")).toBe("跟随 CLI 设置");
  expect(document.body.textContent).not.toContain("未读到");
});

it("shows a failed first listing and lists again", async () => {
  vi.useFakeTimers();
  try {
    const r = createDemo(new URLSearchParams("empty=none"));
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
      drafting={false}
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

it("greys out modes a Claude Code session cannot switch to, and effort for a model without any", async () => {
  const r = demo();
  const s = r.sessions.get("s-claude-idle")!;
  mount(<ModeSheet remote={r} s={s} onClose={() => undefined} />);
  expect(button(/^完全放行/)?.disabled).toBe(true);
  expect(host.textContent).toContain("本会话启动时未启用；需要时新建会话选择");
  // The model list says which models come without auto; until it has come,
  // nothing is greyed out.
  await vi.waitFor(() => expect(button(/^自动/)?.disabled).toBe(true), { timeout: 3000 });
  expect(host.textContent).toContain("当前模型不支持");
  expect(button(/^询问/)?.disabled).toBe(false);
  // acceptEdits is none of the four: nothing is ticked, and it says so.
  expect(host.querySelector(".ck")).toBeNull();
  expect(host.textContent).toContain("当前：accept edits（在终端设置）");
  expect(host.textContent).toContain("不提供切到 accept edits 的按钮");
  act(() => root!.render(<SessionSheet remote={r} s={s} running={false} onClose={() => undefined} />));
  await vi.waitFor(() => expect(host.textContent).toContain("Haiku 4.5 没有推理强度可调"), { timeout: 3000 });
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

it("shows an approval's whole command before its description in the dock", () => {
  const r = createDemo(new URLSearchParams("longcmd=1"));
  r.start();
  const s = r.sessions.get("s-appr")!;
  const dock = (room: number) => (
    <Dock
      remote={r}
      sid="s-appr"
      items={dockItems(s, derive(r.events("s-appr")), r.machines.get(s.machineId))}
      agent={Agent.CODEX}
      drafting={false}
      room={room}
      onExpand={() => undefined}
      onDetail={() => undefined}
      onPlan={() => undefined}
      onTerminal={() => undefined}
    />
  );
  // The forty-line command mounts while the keyboard leaves no room.
  mount(dock(100));
  act(() => root!.render(dock(Infinity)));
  const scroll = host.querySelector(".pending-scroll")!;
  const cmd = scroll.querySelector("pre.cmd")!;
  expect(cmd.textContent!.split("\n")).toHaveLength(40);
  expect(cmd.nextElementSibling?.classList.contains("desc")).toBe(true);
  // Nothing of it is cut away behind a page: it scrolls with the rest.
  expect(button("展开全文")).toBeUndefined();
});

it("puts a card's command first, six whole lines of it, and opens the rest in place", async () => {
  const r = createDemo(new URLSearchParams("longcmd=1"));
  r.start();
  mount(<Home remote={r} go={() => undefined} />);
  const card = () => host.querySelector(".todo")!;
  const cmd = card().querySelector("pre.card-cmd")!;
  expect(cmd.textContent).toContain("set -euo pipefail");
  // Head, then the command, then the description, then the buttons.
  const order = [...card().children].map((e) => e.className || e.tagName);
  expect(order.indexOf("card-cmd")).toBeLessThan(order.indexOf("desc"));
  expect(order.indexOf("desc")).toBeLessThan(order.indexOf("btns decide"));
  // No layout here: the count is the command's own 40 lines.
  const more = button(/^还有 34 行 · 展开/, card())!;
  expect(more).toBeTruthy();
  expect(cmd.classList.contains("open")).toBe(false);
  await click(more);
  expect(cmd.classList.contains("open")).toBe(true);
  await click(button(/^收起/, card()));
  expect(cmd.classList.contains("open")).toBe(false);
});

it("offers no 展开 under a card's command of six lines or fewer", () => {
  const r = demo();
  mount(<Home remote={r} go={() => undefined} />);
  // s-appr's first approval runs five lines.
  const card = host.querySelector(".todo")!;
  expect(card.querySelector("pre.card-cmd")!.textContent).toContain("rm -rf gen/remoteagent");
  expect(card.querySelector(".cmd-more")).toBeNull();
});

function ClaudeAsk({ r }: { r: DemoRemote }) {
  const s = r.sessions.get("s-claude")!;
  return (
    <Dock
      remote={r}
      sid="s-claude"
      items={dockItems(s, derive(r.events("s-claude")), r.machines.get(s.machineId))}
      agent={Agent.CLAUDE}
      drafting={false}
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

it("shows a dock with no room to open as one line that opens its page", async () => {
  const r = demo();
  const s = r.sessions.get("s-appr")!;
  const opened: string[] = [];
  const dock = (room: number) => (
    <Dock
      remote={r}
      sid="s-appr"
      items={dockItems(s, derive(r.events("s-appr")), r.machines.get(s.machineId))}
      agent={Agent.CODEX}
      drafting={false}
      room={room}
      onExpand={() => opened.push("expand")}
      onDetail={(id) => opened.push("detail:" + id)}
      onPlan={() => opened.push("plan")}
      onTerminal={() => opened.push("terminal")}
    />
  );
  mount(dock(Infinity));
  expect(host.querySelector(".pending:not(.open)")).toBeNull();
  // No room: the card needs at least its margin.
  act(() => root!.render(dock(0)));
  const mini = host.querySelector(".pending:not(.open)")!;
  expect(mini.textContent).toContain("请求执行");
  await click(mini.querySelector<HTMLButtonElement>(".pending-sum")!);
  expect(opened).toEqual(["detail:a1"]);
});

it("keeps a question's progress between its page and the dock", async () => {
  const r = demo();
  const q = derive(r.events("s-ask")).questions.get("q1")!;
  mount(<QuestionPage remote={r} sid="s-ask" q={q} agent={Agent.CODEX} title="升级依赖" onBack={() => undefined} onTerminal={() => undefined} />);
  await click(button(/^只升小版本/));
  await vi.waitFor(() => expect(host.textContent).toContain("第 2 题，共 2 题"), { timeout: 3000 });
  act(() => root!.render(<AskDock r={r} />));
  expect(host.textContent).toContain("第 2 题，共 2 题");
  // The one-line form names the question now being asked.
  act(() => root!.render(<AskDock r={r} room={100} />));
  expect(host.querySelector(".pending:not(.open)")!.textContent).toContain("要顺便更新 go.mod 的 Go 版本吗？");
});

it("shortens paths in tool one-liners without showing output", () => {
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
  expect(host.textContent).toContain("退出码 1");
  expect(host.querySelector(".toolout")).toBeNull();
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
  const s = r.sessions.get("s-plan")!;
  mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
  const input = () => host.querySelector<HTMLTextAreaElement>("textarea[aria-label=消息]")!;
  await act(async () => {
    const set = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
    set.call(input(), "顺便看下 lint");
    input().dispatchEvent(new Event("input", { bubbles: true }));
  });
  expect(input().value).toBe("顺便看下 lint");
  // The docked plan is open: to its page, then back.
  await click(button("展开全文"));
  expect(host.querySelector(".navbar")!.textContent).toContain("计划");
  expect(button("清空上下文后执行")).toBeTruthy();
  await click(host.querySelector<HTMLButtonElement>(".navbar .back")!);
  expect(input().value).toBe("顺便看下 lint");
});

it("does not start an empty session when 重试 is tapped after the text was cleared", async () => {
  const r = demo();
  const sent: string[] = [];
  r.machineCommand = async (_m: string, cmd: CommandKind): Promise<CommandResult> => {
    if (cmd?.case === "createSession") sent.push(cmd.value.prompt ?? "");
    throw new CommandError("unavailable", "MacBook 没有回应");
  };
  mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} />);
  const input = () => host.querySelector<HTMLTextAreaElement>("textarea[aria-label=新会话的第一句话]")!;
  const write = (text: string) =>
    act(async () => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input(), text);
      input().dispatchEvent(new Event("input", { bubbles: true }));
    });
  await write("加限流");
  await click(sendButton());
  await vi.waitFor(() => expect(host.textContent).toContain("启动失败"));
  expect(sent).toEqual(["加限流"]);
  // The send button is disabled now; the banner's 重试 is held to the same rule.
  await write("");
  expect(sendButton()?.disabled).toBe(true);
  await click(button("重试"));
  await act(async () => undefined);
  expect(sent).toEqual(["加限流"]);
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
  mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} />);
  const input = () => host.querySelector<HTMLTextAreaElement>("textarea[aria-label=新会话的第一句话]")!;
  await act(async () => {
    const set = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
    set.call(input(), "加限流");
    input().dispatchEvent(new Event("input", { bubbles: true }));
  });
  await click(sendButton());
  await vi.waitFor(() => expect(host.textContent).toContain("不确定是否已经启动"));
  await click(button("重试"));
  await vi.waitFor(() => expect(sent).toHaveLength(2));
  // Retried as the same request; then the daemon no longer knew it.
  expect(sent[1]).toBe(sent[0]);
  await vi.waitFor(() => expect(host.textContent).toContain("请先看会话列表"));
  expect(sendButton()?.disabled).toBe(true);
  expect(sent).toHaveLength(2);
  // Only giving it up allows a new start, under a new request ID.
  await click(button("放弃这次启动"));
  await click(sendButton());
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
    mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} />);
    const type = async (value: string) =>
      act(async () => {
        const input = host.querySelector<HTMLTextAreaElement>("textarea[aria-label=新会话的第一句话]")!;
        Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, value);
        input.dispatchEvent(new Event("input", { bubbles: true }));
      });
    const send = async (n: number) => {
      await click(sendButton());
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
    await click(sendButton());
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
    expect(sendButton()?.disabled).toBe(true);
    await t.type("给登录接口加限流");
    expect(sendButton()?.disabled).toBe(false);
    await t.send(3);
    expect(t.sent[2].id).not.toBe(t.sent[0].id);
    expect(t.sent[2].prompt).toBe("给登录接口加限流");
  });
});

describe("an unknown session start, after leaving its page and coming back", () => {
  const page = (r: DemoRemote) => <ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} />;
  const input = () => host.querySelector<HTMLTextAreaElement>("textarea[aria-label=新会话的第一句话]")!;
  const type = async (value: string) =>
    act(async () => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input(), value);
      input().dispatchEvent(new Event("input", { bubbles: true }));
    });
  const leave = () => {
    act(() => root!.unmount());
    host.remove();
  };

  it("is retried on the machine and with the images it was sent with (review r2)", async () => {
    const r = demo();
    r.prepareImage = async () => ({ bytes: new Uint8Array(4), format: "png", width: 1, height: 1 });
    const uploads: string[] = [];
    r.uploadImage = async (m) => {
      uploads.push(m);
      return { imageId: `${m}-image-${uploads.length}`, expiresMs: Number.MAX_SAFE_INTEGER };
    };
    const sent: { machine: string; id: string; prompt?: string; imageIds?: string[] }[] = [];
    r.machineCommand = async (m: string, cmd: CommandKind, slot?: RequestSlot): Promise<CommandResult> => {
      if (cmd?.case === "createSession") {
        sent.push({ machine: m, id: slot!.requestId, prompt: cmd.value.prompt, imageIds: cmd.value.imageIds });
        throw new CommandError("unknown", "连接超时");
      }
      return DemoRemote.prototype.machineCommand.call(r, m, cmd);
    };
    // The project's own machine is offline: this start goes to another.
    r.machines.get("mac")!.online = false;
    mount(page(r));
    await click(button("换机器"));
    await click([...document.querySelectorAll<HTMLButtonElement>(".sheet button")].find((b) => b.textContent?.includes("iMac")));
    if (document.querySelector(".sheet")) await click(button("完成", document));
    await act(async () => {
      const file = host.querySelector<HTMLInputElement>("input[type=file]")!;
      Object.defineProperty(file, "files", { value: [new File(["x"], "a.png", { type: "image/png" })], configurable: true });
      file.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await type("看这张图");
    await vi.waitFor(() => expect(sendButton()?.disabled).toBe(false));
    await click(sendButton());
    await vi.waitFor(() => expect(host.textContent).toContain("不确定是否已经启动"));
    expect(sent).toEqual([{ machine: "imac", id: sent[0].id, prompt: "看这张图", imageIds: ["imac-image-1"] }]);
    // The page is left; the project's own machine comes back meanwhile.
    leave();
    r.machines.get("mac")!.online = true;
    mount(page(r));
    await act(async () => undefined);
    // The page shows the start that is pending: its message, its machine, its image as uploaded.
    expect(input().value).toBe("看这张图");
    expect(host.querySelector(".project-summary")!.textContent).toContain("iMac");
    expect(host.querySelectorAll(".att-tray .pic").length).toBe(1);
    expect(uploads).toEqual(["imac"]);
    const tray = r.trays.get("new:/work/remote-agent") ?? [...r.trays.values()][0];
    await act(async () => { tray.items[0].expiresMs = 0; tray.refreshExpired(); });
    expect(uploads).toEqual(["imac", "imac"]);
    expect(tray.imageIds()).toEqual(["imac-image-2"]);
    await click(button("重试"));
    await vi.waitFor(() => expect(sent).toHaveLength(2));
    expect(sent[1]).toEqual(sent[0]);
  });

  it("stays on its way, and nothing else is started in its place (review r2)", async () => {
    const r = demo();
    const sent: { id: string; prompt?: string }[] = [];
    const answers: ((e?: CommandError) => void)[] = [];
    r.machineCommand = async (m: string, cmd: CommandKind, slot?: RequestSlot): Promise<CommandResult> => {
      if (cmd?.case !== "createSession") return DemoRemote.prototype.machineCommand.call(r, m, cmd);
      sent.push({ id: slot!.requestId, prompt: cmd.value.prompt });
      const e = await new Promise<CommandError | undefined>((resolve) => answers.push(resolve));
      throw e ?? new CommandError("unknown", "连接超时");
    };
    mount(page(r));
    await type("加限流");
    await vi.waitFor(() => expect(sendButton()?.disabled).toBe(false));
    await click(sendButton());
    await vi.waitFor(() => expect(sent).toHaveLength(1));
    // Left and reopened while the request is on its way.
    leave();
    mount(page(r));
    expect(input().value).toBe("加限流");
    expect(host.textContent).toContain("上启动…");
    // Another message cannot go while that one is out.
    await type("给登录接口加限流");
    expect(sendButton()?.disabled).toBe(true);
    await click(sendButton());
    expect(sent).toHaveLength(1);
    // Its reply is lost: the page that is open now says so, and the other message is a new start.
    await act(async () => answers[0]());
    await vi.waitFor(() => expect(host.textContent).toContain("上次的启动结果还没有确认"));
    expect(sendButton()?.disabled).toBe(false);
    await click(sendButton());
    await vi.waitFor(() => expect(sent).toHaveLength(2));
    expect(sent[1]).toMatchObject({ prompt: "给登录接口加限流" });
    expect(sent[1].id).not.toBe(sent[0].id);
    // That one's reply is lost too: it is retried as itself.
    await act(async () => answers[1]());
    await vi.waitFor(() => expect(host.textContent).toContain("不确定是否已经启动"));
    await click(button("重试"));
    await vi.waitFor(() => expect(sent).toHaveLength(3));
    expect(sent[2]).toEqual(sent[1]);
  });

  it("takes its message from the page open now once its session has started (review r2)", async () => {
    const r = demo();
    let answer!: () => void;
    let asked = 0;
    r.machineCommand = async (m: string, cmd: CommandKind): Promise<CommandResult> => {
      if (cmd?.case === "createSession") {
        asked++;
        await new Promise<void>((resolve) => (answer = resolve));
      }
      return DemoRemote.prototype.machineCommand.call(r, m, cmd);
    };
    const before = r.sessions.size;
    mount(page(r));
    await type("加限流");
    await vi.waitFor(() => expect(sendButton()?.disabled).toBe(false));
    await click(sendButton());
    await vi.waitFor(() => expect(asked).toBe(1));
    leave();
    mount(page(r));
    expect(input().value).toBe("加限流");
    await act(async () => answer());
    await vi.waitFor(() => expect(r.sessions.size).toBe(before + 1));
    // Nothing is left to send again by mistake, and nothing is unknown.
    await vi.waitFor(() => expect(input().value).toBe(""));
    expect(host.textContent).not.toContain("上启动…");
    expect(host.textContent).not.toContain("启动结果");
    expect(asked).toBe(1);
  });

  it("keeps what was chosen for the next message once its session has started", async () => {
    const r = demo();
    const sent: { machine: string; prompt?: string }[] = [];
    let answer!: () => void;
    r.machineCommand = async (m: string, cmd: CommandKind): Promise<CommandResult> => {
      if (cmd?.case === "createSession") {
        sent.push({ machine: m, prompt: cmd.value.prompt });
        if (sent.length === 1) await new Promise<void>((resolve) => (answer = resolve));
      }
      return DemoRemote.prototype.machineCommand.call(r, m, cmd);
    };
    const before = r.sessions.size;
    mount(page(r));
    await type("加限流");
    await vi.waitFor(() => expect(sendButton()?.disabled).toBe(false));
    await click(sendButton());
    await vi.waitFor(() => expect(sent).toEqual([{ machine: "mac", prompt: "加限流" }]));
    // Left and reopened while the request is on its way; its machine goes
    // offline, and the next message is given another one, for this once.
    leave();
    r.machines.get("mac")!.online = false;
    mount(page(r));
    await type("给登录接口加限流");
    await click(button("换机器"));
    await click([...document.querySelectorAll<HTMLButtonElement>(".sheet button")].find((b) => b.textContent?.includes("iMac")));
    if (document.querySelector(".sheet")) await click(button("完成", document));
    expect(host.querySelector(".project-summary")!.textContent).toContain("iMac");
    // The first session starts, its machine back meanwhile.
    r.machines.get("mac")!.online = true;
    await act(async () => answer());
    await vi.waitFor(() => expect(r.sessions.size).toBe(before + 1));
    // The next message is still there, and still goes where it was told to.
    expect(input().value).toBe("给登录接口加限流");
    expect(host.querySelector(".project-summary")!.textContent).toContain("iMac");
    await vi.waitFor(() => expect(sendButton()?.disabled).toBe(false));
    await click(sendButton());
    await vi.waitFor(() => expect(sent).toHaveLength(2));
    expect(sent[1]).toEqual({ machine: "imac", prompt: "给登录接口加限流" });
    // What went with that one is not held for the message after it.
    await vi.waitFor(() => expect(r.sessions.size).toBe(before + 2));
    await vi.waitFor(() => expect(host.querySelector(".project-summary")!.textContent).not.toContain("iMac"));
  });
});


it("keeps a project's model and effort after the page remounts", async () => {
  const project = "/Users/me/Code/ai-bot/remote-agent";
  const page = (r: DemoRemote) => <WideHome remote={r} go={() => undefined} project={project} />;
  const r = demo();
  mount(page(r));
  await act(async () => { await Promise.all([r.listModels("mac", Agent.CODEX), r.settings("mac")]); });
  await fromMenu("新会话设置");
  await click([...document.querySelectorAll<HTMLButtonElement>(".sr")].find((r) => r.textContent?.startsWith("模型")));
  await vi.waitFor(() => expect(button("GPT-6-Sol", document)).toBeDefined());
  await click(button("GPT-6-Sol", document));
  await click(button("low", document));
  // Finish directly on the model page, without returning to project settings.
  await click(button("完成", document));
  expect(document.querySelector(".ns-head")).toBeNull();
  await vi.waitFor(() => expect(savedSet(r, project)).toMatchObject({ model: "gpt-6-sol", effort: "low" }), { timeout: 3000 });
  // Rebuild the page with the saved account preference.
  act(() => root!.unmount());
  host.remove();
  const reloaded = r;
  mount(page(reloaded));
  await act(async () => { await Promise.all([reloaded.listModels("mac", Agent.CODEX), reloaded.settings("mac")]); });
  await fromMenu("新会话设置");
  expect(setting("模型")).toContain("GPT-6-Sol · low");
  await click(button("完成", document));
  const real = reloaded.machineCommand.bind(reloaded);
  let sent: unknown;
  reloaded.machineCommand = async (machine, cmd) => {
    if (cmd?.case === "createSession") sent = cmd.value;
    return real(machine, cmd);
  };
  // An empty message is not sent; the first message starts the session.
  const go = () => host.querySelector<HTMLButtonElement>('button[aria-label="开始新会话"]')!;
  expect(go().disabled).toBe(true);
  await act(async () => {
    const input = host.querySelector<HTMLTextAreaElement>("textarea[aria-label=新会话的第一句话]")!;
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, "看一下");
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await click(go());
  await act(async () => { await vi.waitFor(() => expect(loadProjectConfig(reloaded.account, project)).toBeUndefined()); });
  expect(sent).toMatchObject({ model: "gpt-6-sol", effort: "low", cwd: project });
});

it("keeps project settings by path and carries full access without a new confirmation", async () => {
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
  await vi.waitFor(() => expect(savedSet(r, foo)?.model).toBe("gpt-6-luna"));
  act(() => root!.render(<Box project={shop} />));
  expect(ns!.cfg.model).not.toBe("gpt-6-luna");
  act(() => root!.render(<Box key="reload" project={foo} />));
  expect(ns!.cfg).toMatchObject({ model: "gpt-6-luna", effort: "max", mode: ApprovalMode.FULL_ACCESS });
});

it("keeps the default model while saving a chosen reasoning effort", async () => {
  const r = demo();
  const project = "/Users/me/Code/foo";
  mount(<WideHome remote={r} go={() => undefined} project={project} />);
  await fromMenu("新会话设置");
  await click([...document.querySelectorAll<HTMLButtonElement>(".sr")].find((b) => b.textContent?.startsWith("模型")));
  await click(button(/^默认模型/, document));
  await vi.waitFor(() => expect(button("high", document)).toBeDefined());
  await click(button("high", document));
  expect(document.querySelector(".mi .ck svg")).toBeTruthy();
  await click(button("完成", document));
  await vi.waitFor(() => expect(savedSet(r, project)).toMatchObject({ model: "", effort: "high" }));
  await fromMenu("新会话设置");
  await click([...document.querySelectorAll<HTMLButtonElement>(".sr")].find((b) => b.textContent?.startsWith("模型")));
  expect(document.querySelector(".mi .ck svg")).toBeTruthy();
  await vi.waitFor(() => expect(button("high", document)?.className).toBe("on"));
});

it("names a new project's default model as Codex does, even when /model hides it", async () => {
  const r = demo();
  // The machine's default is a model Codex leaves out of its list.
  (r as unknown as { saved: unknown }).saved = create(DaemonSettingsSchema, { approvalTimeoutSeconds: 600, defaultModel: "gpt-6.1-sol", defaultEffort: "low" });
  r.draftMachines.set("/Users/me/Code/new", "mac");
  mount(<ProjectPage remote={r} pkey="/Users/me/Code/new" go={() => undefined} onBack={() => undefined} />);
  await fromMenu("新会话设置");
  await vi.waitFor(() => expect(setting("模型")).toContain("GPT-6.1-Sol · low"), { timeout: 3000 });
  await click([...document.querySelectorAll<HTMLButtonElement>(".sr")].find((b) => b.textContent?.startsWith("模型")));
  expect(document.querySelector(".mi")?.textContent).toContain("新项目默认设置");
});

it("uses the machine selected for an existing path and excludes its missing Claude", async () => {
  const r = demo();
  const path = "/Users/me/Code/foo";
  await r.changePrefs("settings", path, { id: "old-machine", kind: "settings", path, at: Date.now(), settings: { machineId: "mac", agent: Agent.CLAUDE } });
  r.machines.get("vps")!.meta = create(MachineMetaSchema, { hostname: "iMac", agents: [{ agent: Agent.CODEX }] });
  act(() => addProject(r, "vps", path));
  let ns: ReturnType<typeof useNewSession> | undefined;
  function Box() { ns = useNewSession(r, path, () => undefined); return null; }
  mount(<Box />);
  expect(ns!.cfg.machineId).toBe("vps");
  expect(ns!.cfg.agent).toBe(Agent.CODEX);
  expect(ns!.agents).toEqual([Agent.CODEX]);
  await vi.waitFor(() => expect(r.projectPrefs().settings.find((s) => s.path === path)?.machineId).toBe("vps"));
  r.draftMachines.clear();
  act(() => root!.render(<Box />));
  expect(ns!.cfg.machineId).toBe("vps");
  expect(ns!.cfg.agent).toBe(Agent.CODEX);
});

it("keeps a project added with 新项目… in the lists before its first session", async () => {
  const r = demo();
  const opened: string[] = [];
  const remount = (node: React.ReactNode) => {
    act(() => root?.unmount());
    host.remove();
    mount(node);
  };
  mount(<NewFlow remote={r} go={(to) => opened.push(to)} start="new" onClose={() => undefined} />);
  await vi.waitFor(() => expect(button("Code", document)).toBeTruthy());
  await click(button("Code", document));
  await vi.waitFor(() => expect(button("用 Code 这个目录", document)).toBeTruthy());
  await click(button("用 Code 这个目录", document));
  const added = "/Users/me/Code";
  expect(opened).toEqual(["p/" + encodeURIComponent(added)]);
  // The machine the sheet had selected.
  const machine = r.draftMachines.get(added);
  expect(machine).toBeTruthy();
  await vi.waitFor(() => expect(r.projectPrefs().settings.find((s) => s.path === added)?.machineId).toBe(machine));
  // What a reload starts from: the account's settings, not this page's memory.
  r.draftMachines.clear();
  remount(<ProjectPicker remote={r} onPick={() => undefined} onNew={() => undefined} onClose={() => undefined} />);
  const row = [...document.querySelectorAll<HTMLButtonElement>(".plist .pi")].find((b) => b.querySelector("small")?.textContent === "~/Code");
  expect(row?.querySelector("b")?.textContent).toBe("Code");
  remount(<ProjectPage remote={r} pkey={added} go={() => undefined} onBack={() => undefined} />);
  expect(host.textContent).toContain("这个项目还没有会话");
  // Choosing an existing folder on its current machine keeps its settings.
  const foo = "/Users/me/Code/foo";
  await act(async () => { await vi.waitFor(() => expect(r.prefs.pending.size).toBe(0)); });
  const before = r.projectPrefs().settings.find((s) => s.path === foo);
  remount(<NewFlow remote={r} go={() => undefined} start="new" onClose={() => undefined} />);
  // The preservation assertion is about the same machine, regardless of
  // the machine-name sort order or when the directory listing completes.
  await click(button("MacBook", document));
  await vi.waitFor(() => expect(button("Code", document)).toBeTruthy());
  await click(button("Code", document));
  await vi.waitFor(() => expect(button("foo", document)).toBeTruthy());
  await click(button("foo", document));
  await vi.waitFor(() => expect(button("用 foo 这个目录", document)).toBeTruthy());
  await click(button("用 foo 这个目录", document));
  await act(async () => { await vi.waitFor(() => expect(r.prefs.pending.size).toBe(0)); });
  expect(r.projectPrefs().settings.find((s) => s.path === foo)).toEqual(before);
});

it("shows the effective approval mode in project settings while keeping the default choice", async () => {
  const r = demo();
  // Codex reports its effective mode, with no explicit new-project override.
  (r as unknown as { saved: unknown }).saved = create(DaemonSettingsSchema, { defaultMode: ApprovalMode.UNSPECIFIED });
  r.draftMachines.set("/Users/me/Code/new", "mac");
  mount(<ProjectPage remote={r} pkey="/Users/me/Code/new" go={() => undefined} onBack={() => undefined} />);
  await vi.waitFor(() => expect(host.querySelector(".project-summary")?.textContent).toContain("自动"));
  await fromMenu("新会话设置");
  expect(setting("审批")).toBe("自动");
  await click([...document.querySelectorAll<HTMLButtonElement>(".sr")].find((b) => b.textContent?.startsWith("审批")));
  const defaultOption = [...document.querySelectorAll<HTMLButtonElement>(".mi")].find((b) => b.textContent?.includes("跟随 Codex 自己的配置"));
  expect(defaultOption?.textContent).toContain("默认");
  expect(defaultOption?.querySelector(".ck svg")).toBeTruthy();
});

it("tells how to trust a folder in Claude Code, whose prompt starts on No, exit", () => {
  const r = demo();
  const popup = (agent: Agent) => (
    <Dock
      remote={r}
      sid="s-popup"
      items={[{ kind: "popup", id: "popup", title: "trust folder" }]}
      agent={agent}
      drafting={false}
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

it("shows a running file edit in one line", async () => {
  const r = demo();
  const s = r.sessions.get("s-idle")!;
  s.info = { ...s.info!, cwd: "/Users/me/Code/foo", status: 3 };
  const push = (kind: object) => r.events("s-idle").push(create(EventSchema, { seq: BigInt(r.events("s-idle").length + 1), kind: kind as never }));
  push({ case: "turnStarted", value: {} });
  push({ case: "toolStarted", value: { title: "/Users/me/Code/foo/hello.txt", callId: "w", kind: ToolKind.FILE_CHANGE, itemType: "Write" } });
  mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
  const line = host.querySelector(".tool.single")!;
  expect(line.textContent).toContain("hello.txt");
  expect(host.querySelector(".tool-details")).toBeNull();
});

describe("a message the session ended before sending", () => {
  function setup(ended: boolean) {
    const r = demo();
    const s = r.sessions.get("s-idle")!;
    const push = (kind: object) => r.events("s-idle").push(create(EventSchema, { seq: BigInt(r.events("s-idle").length + 1), kind: kind as never }));
    (r as unknown as { outboxes: Map<string, unknown[]> }).outboxes.set("s-idle", [{ sendId: "m1", requestId: "x", deadline: 0, text: "顺便补一个测试", status: "sent" }]);
    push({ case: "inputState", value: { requestId: "m1", status: InputStatus.QUEUED } });
    push({ case: "inputState", value: { requestId: "m1", status: InputStatus.CANCELED, reason: "exited" } });
    push({ case: "sessionState", value: { status: SessionStatus.EXITED } });
    s.info = { ...s.info!, status: ended ? SessionStatus.EXITED : SessionStatus.IDLE };
    mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
    return r;
  }

  it("says it was not sent, and can go back into the input after resuming", async () => {
    setup(false);
    expect(host.querySelector(".me.fail small")!.textContent).toBe("没发出去：会话已结束放回输入框");
    expect(host.textContent).not.toContain("取消了一条排队的消息");
    expect(host.querySelector(".queued")).toBeNull();
    await click(button("放回输入框"));
    expect(host.querySelector<HTMLTextAreaElement>("textarea[aria-label=消息]")!.value).toBe("顺便补一个测试");
  });

  it("waits as the draft while the session is still ended", async () => {
    const r = setup(true);
    expect(host.textContent).toContain("没发出去：会话已结束");
    expect(host.querySelector(".queued")).toBeNull();
    await click(button("放回输入框"));
    // No input while ended: the text waits as the draft for after resuming.
    expect(r.drafts.get("s-idle")).toBe("顺便补一个测试");
  });
});

it("lists every Claude Code model, and sends none for a model without effort levels", async () => {
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
  await vi.waitFor(() => expect(button(/^Haiku 4\.5/)).toBeTruthy(), { timeout: 3000 });
  expect(button(/^Opus 5\.5/)).toBeTruthy();
  await click(button(/^Haiku 4\.5/));
  // Haiku 4.5 has no effort levels: none is sent.
  await vi.waitFor(() => expect(switched).toHaveLength(1));
  expect(switched[0]).toMatchObject({ model: "claude-haiku-4-5-20251001", effort: "" });
});

it("names models in switch records as the chips do", async () => {
  const r = demo();
  const s = r.sessions.get("s-claude-idle")!;
  const push = (kind: object) => r.events("s-claude-idle").push(create(EventSchema, { seq: BigInt(r.events("s-claude-idle").length + 1), kind: kind as never }));
  push({ case: "turnContext", value: { permissionMode: "default", model: "claude-haiku-4-5-20251001" } });
  push({ case: "settingsChanged", value: { model: "claude-sonnet-5-5", effort: "medium" } });
  push({ case: "turnContext", value: { permissionMode: "default", model: "claude-sonnet-5-5", effort: "medium" } });
  push({ case: "turnContext", value: { permissionMode: "default", model: "claude-opus-5-5", effort: "medium" } });
  // The model list has come, as it has once the session page asks for it.
  await r.listModels("mac", Agent.CLAUDE);
  mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
  const lines = [...host.querySelectorAll(".sysline")].map((l) => l.textContent);
  expect(lines).toContain("切换到 Sonnet 5.5 · medium");
  expect(lines.some((l) => l?.includes("模型切到了 Opus 5.5 · medium"))).toBe(true);
});

describe("a new-session draft", () => {
  const type = async (value: string) =>
    act(async () => {
      const input = host.querySelector<HTMLTextAreaElement>("textarea[aria-label=新会话的第一句话]")!;
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, value);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
  const capture = (r: DemoRemote) => {
    const sent: { cwd?: string; agent?: number; mode?: number }[] = [];
    r.machineCommand = async (m: string, cmd: CommandKind, slot?: RequestSlot): Promise<CommandResult> => {
      if (cmd?.case === "createSession") {
        sent.push({ cwd: cmd.value.cwd, agent: cmd.value.agent, mode: cmd.value.mode });
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
    r.sessions.get("s-run")!.info!.createdAt = timestampFromMs(Date.now() + 86_400_000);
    const sent = capture(r);
    mount(<WideHome remote={r} go={() => undefined} />);
    await vi.waitFor(() => expect(host.textContent).toContain("开始一个任务"));
    const before = host.querySelector(".wide-start-title .chip")!.textContent;
    await type("加限流");
    // Another project gets a newer session meanwhile.
    act(() => { r.sessions.get("s-plan")!.info!.createdAt = timestampFromMs(Date.now() + 172_800_000); });
    bump(r, "s-plan");
    expect(host.querySelector(".wide-start-title .chip")!.textContent).toBe(before);
    await vi.waitFor(() => expect(sendButton()?.disabled).toBe(false));
    await click(sendButton());
    await vi.waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0].cwd).toBe("/Users/me/Code/foo");
  });

  it("keeps the launch settings it was begun with", async () => {
    const r = demo();
    const sent = capture(r);
    mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} />);
    await type("加限流");
    // The project's Claude Code session becomes its latest meanwhile.
    bump(r, "s-claude");
    await fromMenu("新会话设置");
    expect(setting("CLI")).not.toContain("Claude Code");
    await click(button("完成", document));
    await vi.waitFor(() => expect(sendButton()?.disabled).toBe(false));
    await click(sendButton());
    await vi.waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0].agent).toBe(Agent.CODEX);
  });

  it("leaves the mode to the machine's default when that is full access", async () => {
    const r = demo();
    const sent = capture(r);
    // The machine reports Codex's default as full access.
    r.machines.get("mac")!.meta!.agents[0].defaultMode = ApprovalMode.FULL_ACCESS;
    r.draftMachines.set("/Users/me/Code/new", "mac");
    mount(<ProjectPage remote={r} pkey="/Users/me/Code/new" go={() => undefined} onBack={() => undefined} />);
    await vi.waitFor(() => expect(host.querySelector(".project-summary")?.textContent).toContain("完全放行"), { timeout: 3000 });
    await type("加限流");
    await click(sendButton());
    await vi.waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0]).toMatchObject({ mode: ApprovalMode.UNSPECIFIED });
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
    await vi.waitFor(() => expect(card().textContent).toContain("没送到电脑"), { timeout: 3000 });
    expect(button("不允许", card())).toBeUndefined();
    expect(button("允许", card())).toBeUndefined();
    await click(button("重试", card()));
    await vi.waitFor(() => expect(calls.length).toBe(2));
    expect(calls[1]).toEqual(calls[0]);
    expect(calls[1].allow).toBe(true);
  });

  it("keeps a definite failure locked to retrying the same answer", async () => {
    setup(new CommandError("unavailable", "电脑不在线，指令没有发出"));
    await click(button("允许", card()));
    await vi.waitFor(() => expect(card().textContent).toContain("没送到电脑"), { timeout: 3000 });
    expect(button("不允许", card())).toBeUndefined();
    expect(button("允许", card())).toBeUndefined();
    expect(button("重试", card())).toBeTruthy();
  });
});

describe("pending bubbles in the chat (v8)", () => {
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
    const cancel = barrier();
    const command = r.command.bind(r);
    r.command = async (...args) => { await cancel.wait; return command(...args); };
    const s = r.sessions.get("s-run")!;
    const push = (kind: object) => r.events("s-run").push(create(EventSchema, { seq: BigInt(r.events("s-run").length + 1), kind: kind as never }));
    // One more from another client, whose text this page does not know.
    push({ case: "inputState", value: { requestId: "other", status: InputStatus.QUEUED } });
    mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
    const entries = () => [...host.querySelectorAll(".pending-bubble")];
    expect(entries().length).toBe(2);
    expect(entries()[0].textContent).toContain("顺便补一个测试");
    expect(entries()[0].textContent).toContain("插入这一轮");
    expect(entries()[1].textContent).toContain("另一客户端");
    await click(button("取消", entries()[0]));
    expect(entries()[0].textContent).toContain("正在取消…");
    await act(async () => cancel.release());
    await vi.waitFor(() => expect(entries().length).toBe(1), { timeout: 3000 });
    expect(host.textContent).toContain("取消了一条排队的消息");
  });

  it("keeps a message whose cancel failed, and says so", async () => {
    const r = demo();
    const s = r.sessions.get("s-run")!;
    r.command = async () => {
      throw new CommandError("unavailable", "电脑不在线，指令没有发出");
    };
    mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
    await click(button("取消", host.querySelector(".pending-bubble")!));
    await vi.waitFor(() => expect(host.querySelector(".pending-bubble")!.textContent).toContain("取消没成功，仍在排队"));
  });
});

describe("switching a new session's CLI (v2)", () => {
  it("keeps the draft and starts with the other CLI's own settings", async () => {
    const r = demo();
    const sent: { agent?: number; mode?: number; prompt?: string }[] = [];
    r.machineCommand = async (m: string, cmd: CommandKind, slot?: RequestSlot): Promise<CommandResult> => {
      if (cmd?.case === "createSession") {
        sent.push({ agent: cmd.value.agent, mode: cmd.value.mode, prompt: cmd.value.prompt });
        throw new CommandError("failed_precondition", "stop here");
      }
      void slot;
      return DemoRemote.prototype.machineCommand.call(r, m, cmd);
    };
    mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} />);
    await act(async () => {
      const input = host.querySelector<HTMLTextAreaElement>("textarea[aria-label=新会话的第一句话]")!;
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, "加限流");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    // The switch is next to the box; project settings have no CLI row.
    await click(cliSwitch("Claude Code"));
    expect(host.querySelector(".launch .pick .on")?.textContent).toBe("Claude Code");
    expect(host.querySelector<HTMLTextAreaElement>("textarea[aria-label=新会话的第一句话]")!.value).toBe("加限流");
    // Nothing is stored for Claude Code here: the project's latest Claude Code session says how it starts, not Codex's set.
    await vi.waitFor(() => expect(host.querySelector(".project-summary")?.textContent).toMatch(/^Claude Code · 询问 · MacBook · Sonnet/), { timeout: 3000 });
    await fromMenu("新会话设置");
    expect(setting("CLI")).toBe("");
    await click(button("完成", document));
    await click(sendButton());
    await vi.waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0]).toMatchObject({ agent: Agent.CLAUDE, mode: ApprovalMode.ASK, prompt: "加限流" });
    // Picking is not saving: the project still remembers Codex.
    expect(r.projectPrefs().settings.find((s) => s.path === "/Users/me/Code/foo")?.agent).toBe(Agent.CODEX);
  });

  it("remembers the CLI a session was started with and keeps each CLI's own set", async () => {
    const r = demo();
    const foo = "/Users/me/Code/foo";
    const sent: { agent?: number; model?: string }[] = [];
    r.machineCommand = async (m: string, cmd: CommandKind, slot?: RequestSlot): Promise<CommandResult> => {
      if (cmd?.case === "createSession") sent.push({ agent: cmd.value.agent, model: cmd.value.model });
      void slot;
      return DemoRemote.prototype.machineCommand.call(r, m, cmd);
    };
    const page = () => <ProjectPage remote={r} pkey={foo} go={() => undefined} onBack={() => undefined} />;
    mount(page());
    await vi.waitFor(() => expect(r.prefs.status).toBe("ready"));
    await click(cliSwitch("Claude Code"));
    await act(async () => {
      const input = host.querySelector<HTMLTextAreaElement>("textarea[aria-label=新会话的第一句话]")!;
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, "梳理登录流程");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => { await vi.waitFor(() => expect(sendButton()?.disabled).toBe(false)); });
    await click(sendButton());
    await vi.waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0].agent).toBe(Agent.CLAUDE);
    // The next one starts with Claude Code picked; Codex's set is as it was.
    await vi.waitFor(() => expect(r.projectPrefs().settings.find((s) => s.path === foo)?.agent).toBe(Agent.CLAUDE), { timeout: 3000 });
    expect(savedSet(r, foo)).toMatchObject({ model: "gpt-6-astra", effort: "medium", mode: ApprovalMode.ASK });
    expect(savedSet(r, foo, Agent.CLAUDE)).toBeDefined();
    act(() => root!.unmount());
    host.remove();
    mount(page());
    expect(host.querySelector(".launch .pick .on")?.textContent).toBe("Claude Code");
    // Back on Codex, its stored model is what starts.
    await click(cliSwitch("Codex"));
    await vi.waitFor(() => expect(host.querySelector(".project-summary")?.textContent).toMatch(/^Codex · 询问/));
  });

  it("edits one CLI's set under its tab and picks nothing by looking", async () => {
    const r = demo();
    const foo = "/Users/me/Code/foo";
    mount(<ProjectPage remote={r} pkey={foo} go={() => undefined} onBack={() => undefined} />);
    await vi.waitFor(() => expect(r.prefs.status).toBe("ready"));
    await fromMenu("新会话设置");
    const tab = (name: string) => [...document.querySelectorAll<HTMLButtonElement>(".cli-tabs button")].find((b) => b.textContent === name);
    expect(tab("Codex")?.className).toBe("on");
    expect(setting("上下文")).toBe("");
    await click(tab("Claude Code"));
    expect(document.querySelector('[aria-label="上下文窗口"]')).not.toBeNull();
    await click([...document.querySelectorAll<HTMLButtonElement>(".sr")].find((b) => b.textContent?.startsWith("审批")));
    await click(button(/^自动/, document));
    await click(button("完成", document));
    await vi.waitFor(() => expect(savedSet(r, foo, Agent.CLAUDE)).toMatchObject({ mode: ApprovalMode.AUTO }), { timeout: 3000 });
    expect(savedSet(r, foo)).toMatchObject({ model: "gpt-6-astra", mode: ApprovalMode.ASK });
    expect(r.projectPrefs().settings.find((s) => s.path === foo)?.agent).toBe(Agent.CODEX);
    expect(host.querySelector(".launch .pick .on")?.textContent).toBe("Codex");
  });

  it("shows no switch on a machine with one CLI, and says which CLI runs when the remembered one is missing", async () => {
    const r = demo();
    const foo = "/Users/me/Code/foo";
    await vi.waitFor(() => expect(r.prefs.status).toBe("ready"));
    await r.changePrefs("settings", foo, { id: "t1", kind: "settings", path: foo, at: Date.now(), settings: { machineId: "vps", agent: Agent.CLAUDE } });
    mount(<ProjectPage remote={r} pkey={foo} go={() => undefined} onBack={() => undefined} />);
    expect(host.querySelector(".launch")).toBeNull();
    expect(host.textContent).toContain("VPS-dev 没有 Claude Code，这次用 Codex");
    expect(host.querySelector(".project-summary")?.textContent).toMatch(/^Codex · /);
    // The project still remembers Claude Code.
    expect(r.projectPrefs().settings.find((s) => s.path === foo)?.agent).toBe(Agent.CLAUDE);
    await fromMenu("新会话设置");
    expect(document.querySelector(".cli-tabs")).toBeNull();
  });

  it("puts the switch and the rest of the summary under the box on a wide page", async () => {
    const r = demo();
    mount(<WideHome remote={r} go={() => undefined} project="/Users/me/Code/foo" />);
    expect(host.querySelector(".wide-start-title")?.textContent).toBe("开始一个任务");
    const row = host.querySelector(".launch")!;
    expect(row.previousElementSibling?.className).toContain("compose-card");
    expect(row.querySelector(".pick .on")?.textContent).toBe("Codex");
    expect(row.querySelector(".project-summary")?.textContent).toMatch(/^询问 · MacBook/);
  });

  it("shows what was read before at once when coming back to a CLI", async () => {
    const r = demo();
    mount(<WideHome remote={r} go={() => undefined} project="/Users/me/Code/foo" />);
    const text = () => host.querySelector(".launch .project-summary")!.textContent;
    await vi.waitFor(() => expect(text()).toMatch(/GPT-6-Astra$/));
    await click(button("Claude Code", host.querySelector(".launch")!));
    await vi.waitFor(() => expect(text()).not.toMatch(/GPT|读取/));
    const claude = text();
    // No 读取设置… and no raw model id on the way back, in either direction.
    act(() => button("Codex", host.querySelector(".launch")!)!.click());
    expect(text()).toMatch(/GPT-6-Astra$/);
    act(() => button("Claude Code", host.querySelector(".launch")!)!.click());
    expect(text()).toBe(claude);
  });

  it("names the machine in the summary only when there is more than one", async () => {
    const r = demo();
    mount(<WideHome remote={r} go={() => undefined} project="/Users/me/Code/foo" />);
    const summary = () => host.querySelector(".launch .project-summary")!;
    // The name is its own part, the one that gives way on a long line.
    expect(summary().querySelector(".machine")?.textContent).toBe("MacBook");
    act(() => {
      for (const id of [...r.machines.keys()]) if (id !== "mac") r.machines.delete(id);
      (r as unknown as { changed(): void }).changed();
    });
    expect(summary().querySelector(".machine")).toBeNull();
    // Mode and model; the effort and the window are in the settings.
    expect(summary().textContent).toMatch(/^询问 · gpt-6-astra$/i);
    expect(summary().getAttribute("aria-label")).toBe(`新会话设置：${summary().textContent}`);
  });
});

/** A CLI in the switch next to the new-session box. */
const cliSwitch = (name: string) => [...host.querySelectorAll<HTMLButtonElement>(".launch .pick button")].find((b) => b.textContent === name);

describe("project management (v2)", () => {
  const names = (sel: string) => [...host.querySelectorAll(sel)].map((e) => e.firstChild?.textContent ?? "");

  it("removes a project only after asking, and keeps its waiting sessions", async () => {
    const r = demo();
    mount(<ManagePage remote={r} go={() => undefined} onBack={() => undefined} wide={false} />);
    await vi.waitFor(() => expect(r.prefs.status).toBe("ready"));
    await click(host.querySelector<HTMLButtonElement>('[aria-label="管理 shop"]')!);
    await click(button("移除项目…"));
    // Nothing is removed until the dialog is confirmed.
    expect(host.querySelector(".alert")?.textContent).toContain("移除 shop？");
    await click(button("取消"));
    expect(names(".plist .pi b")).toContain("shop");
    await click(host.querySelector<HTMLButtonElement>('[aria-label="管理 shop"]')!);
    await click(button("移除项目…"));
    await click(button("移除"));
    await vi.waitFor(() => expect(names(".plist .pi b")).not.toContain("shop"), { timeout: 3000 });
    // No undo and no list of removed projects: adding the folder again is the way back.
    expect(host.textContent).not.toContain("撤销");
    expect(host.textContent).not.toContain("已移除");
    // Home leaves it out of the projects, but its plan still waits for the user.
    act(() => root!.render(<Home remote={r} go={() => undefined} />));
    await click(host.querySelector<HTMLButtonElement>(".large .scope")!);
    expect(names(".plist .pi b")).not.toContain("shop");
    expect(names(".plist .pi b")).toContain("foo");
    expect(todoList(r).map((x) => x.id)).toContain("s-plan");
  });

  it("lists a removed project again when its folder is added as a new project", async () => {
    const r = demo();
    await vi.waitFor(() => expect(r.prefs.status).toBe("ready"));
    const shop = "/Users/me/Code/shop";
    await r.changePrefs("hide", shop);
    mount(<ManagePage remote={r} go={() => undefined} onBack={() => undefined} wide={false} />);
    expect(names(".plist .pi b")).not.toContain("shop");
    await click(button(/新项目…/));
    for (const dir of ["Code", "shop"]) {
      await vi.waitFor(() => expect(button(dir, document)).toBeTruthy());
      await click(button(dir, document));
    }
    await vi.waitFor(() => expect(button("用 shop 这个目录", document)).toBeTruthy());
    await click(button("用 shop 这个目录", document));
    await vi.waitFor(() => expect(names(".plist .pi b")).toContain("shop"), { timeout: 3000 });
    expect(r.projectPrefs().hidden.map((h) => h.path)).not.toContain(shop);
  });

  it("lists projects the same way in the scope menu, the picker and 项目管理", async () => {
    const r = demo();
    await vi.waitFor(() => expect(r.prefs.status).toBe("ready"));
    const { ProjectPicker } = await import("./NewSession");
    const seen = () => [...host.querySelectorAll(".plist .fl, .plist .pi b, .plist .pi small, .plist .pi.add")].map((e) => e.textContent).filter((t) => t !== "全部项目" && !/^(\d+ 个待处理|\d+ 个进行中)( · \d+ 个进行中)?$/.test(t ?? ""));
    mount(<ManagePage remote={r} go={() => undefined} onBack={() => undefined} wide={false} />);
    const managed = seen();
    // Counted as the home's sections are: waiting, then running.
    expect(managed.slice(0, 3)).toEqual(["常用", "foo", "~/Code/foo · 2 个待处理 · 4 个进行中"]);
    expect(managed[managed.length - 1]).toBe(" 新项目…");
    act(() => root!.render(<ProjectPicker remote={r} onPick={() => undefined} onNew={() => undefined} onClose={() => undefined} />));
    expect(seen()).toEqual(managed);
    act(() => root!.render(<Home remote={r} go={() => undefined} />));
    await click(host.querySelector<HTMLButtonElement>(".large .scope")!);
    expect(seen()).toEqual(managed);
    // 全部项目 counts the same way, over every project.
    const all = [...host.querySelectorAll(".plist .pi")].find((x) => x.querySelector("b")?.textContent === "全部项目")!;
    expect(all.querySelector("small")!.textContent).toMatch(/^\d+ 个待处理 · \d+ 个进行中$/);
  });

  it("keeps a removed project out of the new-session picker and asks to add it again first", async () => {
    const r = demo();
    await vi.waitFor(() => expect(r.prefs.status).toBe("ready"));
    await r.changePrefs("hide", "/Users/me/Code/shop");
    const { ProjectPicker } = await import("./NewSession");
    mount(<ProjectPicker remote={r} onPick={() => undefined} onNew={() => undefined} onClose={() => undefined} />);
    expect(host.textContent).not.toContain("~/Code/shop");
    act(() => root!.render(<ProjectPage remote={r} pkey="/Users/me/Code/shop" go={() => undefined} onBack={() => undefined} />));
    expect(host.textContent).toContain("这个项目已从列表移除");
    expect(host.querySelector("textarea[aria-label=新会话的第一句话]")).toBeNull();
    // Its sessions are all still there.
    expect(host.textContent).toContain("拆分订单服务");
    await click(button("重新添加"));
    await vi.waitFor(() => expect(host.querySelector("textarea[aria-label=新会话的第一句话]")).toBeTruthy(), { timeout: 3000 });
  });

  it("asks before removing from the project's settings too, then leaves the project", async () => {
    const r = createDemo(new URLSearchParams("project-settings=1"));
    r.start();
    await vi.waitFor(() => expect(r.prefs.status).toBe("ready"));
    const went: string[] = [];
    mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={(to) => went.push(to)} onBack={() => undefined} />);
    await click(button("移除项目…"));
    expect(host.querySelector(".alert")?.textContent).toContain("移除 foo？");
    await click(button("移除"));
    await vi.waitFor(() => expect(r.projectPrefs().hidden.map((h) => h.path)).toContain("/Users/me/Code/foo"), { timeout: 3000 });
    await vi.waitFor(() => expect(went).toEqual([""]));
  });

  it("shows a failed save as not done, and retries the same operation", async () => {
    const retry = barrier();
    let retrying = false;
    const r = createDemo(new URLSearchParams("prefs=fail"), async () => {
      if (retrying) await retry.wait;
    });
    r.start();
    await vi.waitFor(() => expect(r.prefs.status).toBe("ready"));
    mount(<ManagePage remote={r} go={() => undefined} onBack={() => undefined} wide={false} />);
    await click(host.querySelector<HTMLButtonElement>('[aria-label="管理 bar"]')!);
    await click(button("设为常用"));
    await vi.waitFor(() => expect(host.textContent).toContain("没能保存，列表保持原样"), { timeout: 5000 });
    expect(names(".plist .pi b")[0]).toBe("foo");
    expect(host.querySelectorAll(".plist .fl").length).toBe(2);
    const id = r.prefs.failed!.op.id;
    retrying = true;
    await click(button("重试"));
    await vi.waitFor(() => expect(r.prefs.pending.size).toBe(1));
    await act(async () => retry.release());
    await vi.waitFor(() => expect(r.prefs.failed?.op.id).toBe(id), { timeout: 5000 });
  }, 15_000);

  it("pauses changes while disconnected and says when they can be made", async () => {
    const r = createDemo(new URLSearchParams("conn=reconnecting"));
    r.start();
    await vi.waitFor(() => expect(r.prefs.status).toBe("ready"));
    mount(<ManagePage remote={r} go={() => undefined} onBack={() => undefined} wide={false} />);
    await click(host.querySelector<HTMLButtonElement>('[aria-label="管理 bar"]')!);
    expect(button(/设为常用/)!.disabled).toBe(true);
    expect(button(/设为常用/)!.textContent).toContain("连上服务器后才能修改");
    expect(button("移除项目…")!.disabled).toBe(true);
  });
});

describe("who did what (v2)", () => {
  it("names another client or the computer's terminal, never this client", () => {
    const r = demo();
    const s = r.sessions.get("s-multi")!;
    return (async () => {
      await vi.waitFor(() => expect(r.devicesStatus).toBe("ready"));
      mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
      const notes = [...host.querySelectorAll(".me small")].map((x) => x.textContent);
      // The first message is this client's own: it carries no name.
      expect(notes).toEqual(["Android · Pixel 9", "VPS-dev 终端"]);
      // A client that says nothing of itself, with no ID in its name.
      expect(host.textContent).toContain("客户端 将模式切到 计划");
      expect(host.querySelector(".record")?.textContent).toContain("已允许 · Android · Pixel 9");
      await click(button(/还有 2 条排队/));
      const entries = [...host.querySelectorAll(".pending-bubble")].map((x) => x.textContent);
      expect(entries[0]).toContain("补充库存回滚测试。");
      expect(entries[1]).toContain("Android · Pixel 9");
      expect(entries[1]).toContain("最后检查 README。");
    })();
  });

  it("names another client on model changes and leaves this client unnamed", async () => {
    const r = demo();
    await vi.waitFor(() => expect(r.devicesStatus).toBe("ready"));
    const s = r.sessions.get("s-idle")!;
    const events = r.events(s.id);
    for (const [deviceId, model] of [[ANDROID, "other-model"], [r.account.deviceId, "my-model"]]) {
      events.push(create(EventSchema, {
        seq: BigInt(events.length + 1), origin: { kind: OriginKind.CLIENT, deviceId },
        kind: { case: "settingsChanged", value: { model } },
      }));
    }
    mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
    const notes = [...host.querySelectorAll(".sysline")].map((x) => x.textContent);
    expect(notes).toContain("Android · Pixel 9 将模型切到 other-model");
    expect(notes).toContain("切换到 my-model");
  });

  it("offers a retry for a message that failed before Enter, and the terminal when it may be half written", async () => {
    const r = demo();
    const s = r.sessions.get("s-msg")!;
    mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
    // Surely not sent: red, with a retry. Maybe sent: orange, to the terminal.
    expect(host.querySelector(".me.fail")!.textContent).toContain("没发出去：Codex 没认出图片");
    const maybe = [...host.querySelectorAll(".me.warn small")].map((x) => x.textContent);
    expect(maybe.some((t) => t?.startsWith("可能没发出去："))).toBe(true);
    expect(host.textContent).not.toMatch(/没能发送|不会自动重发|终端里可能留着/);
    // A connection that timed out leaves it unknown: orange, no red frame.
    const timedOut = [...host.querySelectorAll(".me")].find((x) => x.textContent?.includes("顺便补一个测试"))!;
    expect(timedOut.classList.contains("warn")).toBe(true);
    expect(timedOut.classList.contains("fail")).toBe(false);
    expect(timedOut.querySelector("small")!.textContent).toBe("可能没发出去：连接超时重试移除此提示");
    const failed = [...host.querySelectorAll(".me.fail")].find((x) => x.textContent?.includes("Codex 没认出图片"))!;
    await click(button("重试", failed));
    expect(r.outbox("s-msg").at(-1)?.text).toBe("这两个页面底部的按钮都点不到");
  });
});

describe("a send that failed", () => {
  for (const [unknown, cls, words] of [
    [false, ".me.fail", "没发出去：电脑不在线"],
    [true, ".me.warn", "可能没发出去：连接超时"],
  ] as const)
    it(`says ${words.split("：")[0]} with what is known`, () => {
      const r = demo();
      (r as unknown as { outboxes: Map<string, unknown[]> }).outboxes.set("s-idle", [
        { sendId: "m1", requestId: "x", deadline: 0, text: "顺便补一个测试", status: "failed", error: words.split("：")[1], unknown },
      ]);
      mount(<SessionPage remote={r} session={r.sessions.get("s-idle")!} onBack={() => undefined} wide={false} />);
      const bubble = [...host.querySelectorAll(cls)].find((x) => x.textContent?.includes("顺便补一个测试"))!;
      expect(bubble.querySelector("small")!.textContent).toMatch(new RegExp("^" + words));
      expect(button("重试", bubble)).toBeTruthy();
      expect(button("移除此提示", bubble)).toBeTruthy();
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
      return { imageId: "id-" + name, expiresMs: Number.MAX_SAFE_INTEGER };
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
    expect(sendButton()?.disabled).toBe(true);
    // Removing the failed one lets the rest go, with only their IDs.
    await click(host.querySelectorAll<HTMLButtonElement>(".att-tray .x")[1]);
    await vi.waitFor(() => expect(sendButton()?.disabled).toBe(false));
    await click(sendButton());
    await vi.waitFor(() => expect(t.sent).toHaveLength(1));
    expect(t.sent[0]).toEqual({ text: "", imageIds: ["id-a.png"] });
    expect(host.querySelector(".att-tray")).toBeNull();
  });

  it("takes six at most from one pick and says the rest were left out", async () => {
    const t = setup(() => false);
    await t.pick(...[1, 2, 3, 4, 5, 6, 7, 8].map((i) => `p${i}.png`));
    expect(host.querySelectorAll(".att-tray .pic").length).toBe(6);
    expect(host.textContent).toContain("一条消息最多 6 张，多选的 2 张没有加入");
    expect(host.querySelector(".cm-plus")!.classList.contains("off")).toBe(true);
  });

  it("offers no ＋ on a computer whose daemon cannot take images", async () => {
    const r = demo();
    const s = r.sessions.get("s-appr")!; // on VPS-dev, whose image folder has a space in its path
    mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
    await click(host.querySelector<HTMLButtonElement>(".cm-plus")!);
    expect(host.querySelector("input[type=file]")).toBeTruthy();
    expect(host.textContent).toContain("VPS-dev 上 remote-agent 的目录路径带空格，不能发图片");
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

  it("says it waits for the CLI to come up while the session is starting, and shows it once", () => {
    const { r } = setup();
    act(() => {
      const s = r.sessions.get("s-fresh")!;
      s.info = { ...s.info!, status: SessionStatus.STARTING };
      r.touch();
    });
    expect(host.querySelector(".pending-meta > span")!.textContent).toBe("等 Codex 就绪后发出");
    expect(host.textContent).not.toContain("插入这一轮");
    expect(host.textContent!.split("给登录接口加限流").length - 1).toBe(1);
  });

  it("shows its real state, not 已送达 after another client canceled it", () => {
    const { push } = setup();
    // The only one waiting: not numbered.
    expect(host.querySelector(".pending-meta > span")!.textContent).toBe("插入这一轮");
    push({ case: "inputState", value: { requestId: "p1", status: InputStatus.CANCELED } });
    expect(host.textContent).not.toContain("插入这一轮");
    expect(host.textContent).toContain("取消了一条排队的消息");
  });

  it("is a pending bubble, where it can be canceled", async () => {
    setup();
    const entry = host.querySelector(".pending-bubble")!;
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
    t.r.prepareImage = async () => ({ bytes: new Uint8Array(4), format: "png", width: 1, height: 1 });
    let uploads = 0;
    t.r.uploadImage = async () => ({ imageId: "restored-" + ++uploads, expiresMs: Number.MAX_SAFE_INTEGER });
    await click(button("放回输入框", row()));
    expect(host.querySelector<HTMLTextAreaElement>("textarea[aria-label=消息]")!.value).toBe("看这两张");
    expect(host.querySelectorAll(".att-tray .pic").length).toBe(2);
    await act(async () => { await vi.waitFor(() => expect(host.querySelector<HTMLButtonElement>('button[aria-label="发送"]')!.disabled).toBe(false)); });
    await click(host.querySelector<HTMLButtonElement>('button[aria-label="发送"]')!);
    await vi.waitFor(() => expect(t.sent).toHaveLength(1));
    expect(t.sent[0]).toEqual({ text: "看这两张", imageIds: ["restored-1", "restored-2"] });
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
    r.uploadImage = async () => ({ imageId: "id-a", expiresMs: Number.MAX_SAFE_INTEGER });
  };
  const bump = (r: DemoRemote, sid: string) =>
    act(() => {
      r.sessions.get(sid)!.updatedMs = BigInt(Date.now() + 60_000);
      r.touch();
    });

  it("keeps its launch settings once an image is chosen", async () => {
    const r = demo();
    stub(r);
    mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} />);
    await pick();
    bump(r, "s-claude");
    await fromMenu("新会话设置");
    expect(setting("CLI")).not.toContain("Claude Code");
    await click(button("完成", document));
  });

  it("keeps its project on the wide home once an image is chosen", async () => {
    const r = demo();
    stub(r);
    mount(<WideHome remote={r} go={() => undefined} />);
    await vi.waitFor(() => expect(host.textContent).toContain("开始一个任务"));
    const before = host.querySelector(".wide-start-title .chip")!.textContent;
    await pick();
    bump(r, "s-plan");
    expect(host.querySelector(".wide-start-title .chip")!.textContent).toBe(before);
    expect(host.querySelectorAll(".att-tray .pic").length).toBe(1);
  });
});

it("loads a picture opened offline once the connection is back (review B11)", async () => {
  const r = createDemo(new URLSearchParams("conn=reconnecting"));
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

it("asks to add a project again when it is removed while its draft is on the wide home (review B12)", async () => {
  const r = demo();
  r.sessions.get("s-run")!.info!.createdAt = timestampFromMs(Date.now() + 86_400_000);
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
  await click(button("重新添加"));
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

it("starts a new session with images and no words (Go 61c4bb2)", async () => {
  const r = demo();
  r.prepareImage = async () => ({ bytes: new Uint8Array(4), format: "png", width: 1, height: 1 });
  r.uploadImage = async () => ({ imageId: "id-a", expiresMs: Number.MAX_SAFE_INTEGER });
  const sent: { prompt?: string; imageIds?: string[] }[] = [];
  r.machineCommand = async (m: string, cmd: CommandKind, slot?: RequestSlot): Promise<CommandResult> => {
    if (cmd?.case === "createSession") {
      sent.push({ prompt: cmd.value.prompt, imageIds: cmd.value.imageIds });
      throw new CommandError("failed_precondition", "stop here");
    }
    void slot;
    return DemoRemote.prototype.machineCommand.call(r, m, cmd);
  };
  mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} />);
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
  expect(host.textContent).not.toContain("7 天");
  expect(host.textContent).toContain("最近收到");
});

it("retries a start with a launch override whose reply was lost as the same request (review W1)", async () => {
  const r = demo();
  const sent: { id: string; model?: string }[] = [];
  r.machineCommand = async (m: string, cmd: CommandKind, slot?: RequestSlot): Promise<CommandResult> => {
    if (cmd?.case === "createSession") {
      sent.push({ id: slot!.requestId, model: cmd.value.model });
      throw new CommandError("unknown", "连接超时");
    }
    return DemoRemote.prototype.machineCommand.call(r, m, cmd);
  };
  let ns: ReturnType<typeof useNewSession> | undefined;
  mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} />);
  // A launch override chosen by the user, mode left to the default.
  await fromMenu("新会话设置");
  await click(button(/^模型/));
  await act(async () => { await r.listModels("mac", Agent.CODEX); });
  await click(button("GPT-6-Sol"));
  await click(host.querySelector<HTMLButtonElement>('.sheet .back2')!);
  await click(button("完成"));
  void ns;
  await act(async () => {
    const input = host.querySelector<HTMLTextAreaElement>("textarea[aria-label=新会话的第一句话]")!;
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, "加限流");
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await click(sendButton());
  await vi.waitFor(() => expect(host.textContent).toContain("不确定是否已经启动"));
  await click(button("重试"));
  await vi.waitFor(() => expect(sent).toHaveLength(2));
  expect(sent[1]).toEqual(sent[0]);
  expect(sent[0].model).toBeTruthy();
});

it("does not put another client's images into this input (review W3)", async () => {
  const r = demo();
  const s = r.sessions.get("s-idle")!;
  for (const [kind, origin] of [
    [{ case: "inputState", value: { requestId: "x1", status: InputStatus.QUEUED, text: "看这两张", imageIds: ["i1", "i2"] } }, { kind: OriginKind.CLIENT, deviceId: ANDROID }],
    [{ case: "inputState", value: { requestId: "x1", status: InputStatus.FAILED, reason: "image_not_recognized" } }, undefined],
  ] as const)
    r.events("s-idle").push(create(EventSchema, { seq: BigInt(r.events("s-idle").length + 1), kind: kind as never, origin: origin as never }));
  mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
  await click(button("放回输入框", host.querySelector(".me.fail")!));
  expect(host.querySelector<HTMLTextAreaElement>("textarea[aria-label=消息]")!.value).toBe("看这两张");
  expect(host.querySelector(".att-tray")).toBeNull();
  expect(host.textContent).toContain("图片是另一客户端上传的");
});

it("keeps the wide home default project across background uploads before a draft starts", async () => {
  const r = demo();
  mount(<WideHome remote={r} go={() => undefined} />);
  const before = host.querySelector(".wide-start-title .chip")!.textContent;
  act(() => {
    r.sessions.get("s-plan")!.updatedMs = BigInt(Date.now() + 60_000);
    (r as unknown as { changed(): void }).changed();
  });
  expect(host.querySelector(".wide-start-title .chip")!.textContent).toBe(before);
});

describe("the client-v7 conversation", () => {
  it("docks a waiting item open, keeps it to one line after 收起, and opens the next one", async () => {
    const r = demo();
    const page = () => <SessionPage remote={r} session={r.sessions.get("s-appr")!} onBack={() => undefined} wide={false} />;
    mount(page());
    const dock = () => host.querySelector(".dock.pending")!;
    // What waits for the user is open on arrival, under one head line.
    expect(dock().classList.contains("open")).toBe(true);
    expect(button("允许", dock())).toBeTruthy();
    expect(dock().querySelectorAll(".pending-label")).toHaveLength(1);
    expect(dock().querySelector(".pending-brief")!.textContent).toMatch(/^还有 1 项 · 剩 \d+ 分钟$/);
    expect(dock().querySelector(".pending-brief")!.textContent).not.toContain("rm -rf");
    await click(dock().querySelector<HTMLButtonElement>(".pending-sum")!);
    expect(dock().classList.contains("open")).toBe(false);
    expect(button("允许", dock())).toBeUndefined();
    // Closed, the line says what it is again.
    expect(dock().querySelector(".pending-brief")!.textContent).toContain("rm -rf");
    // It stays closed when the session is opened again.
    act(() => root!.render(<div />));
    act(() => root!.render(page()));
    expect(dock().classList.contains("open")).toBe(false);
    await click(dock().querySelector<HTMLButtonElement>(".pending-sum")!);
    expect(dock().classList.contains("open")).toBe(true);
    await click(dock().querySelector<HTMLButtonElement>(".pending-sum")!);
    expect(dock().classList.contains("open")).toBe(false);
    // The next item comes open.
    const s = r.sessions.get("s-appr")!;
    const items = dockItems(s, derive(r.events("s-appr")), r.machines.get(s.machineId));
    act(() =>
      root!.render(
        <Dock remote={r} sid="s-appr" items={items.slice(1)} agent={Agent.CODEX} drafting={false} onExpand={() => undefined} onDetail={() => undefined} onPlan={() => undefined} onTerminal={() => undefined} />,
      ),
    );
    expect(dock().classList.contains("open")).toBe(true);
    expect(dock().textContent).toContain("/etc/hosts");
    // The last one too says only the time left.
    expect(dock().querySelector(".pending-brief")!.textContent).toMatch(/^剩 \d+ 分钟$/);
  });

  it("shows a running tool as one line without details or a tool peek", async () => {
    const r = demo();
    mount(<SessionPage remote={r} session={r.sessions.get("s-run")!} onBack={() => undefined} wide={false} />);
    const tool = host.querySelector(".tool.single .spin")!.closest(".tool")!;
    expect(tool.querySelector("pre")).toBeNull();
    expect(tool.textContent).toContain("go test");
    // The time says what it is.
    expect(tool.querySelector(".muted")!.textContent).toMatch(/^已运行 \d+:\d\d$/);
    expect(host.querySelector(".compose-status")).toBeNull();
  });

  it("says how long a turn took between turns", () => {
    const r = demo();
    mount(<SessionPage remote={r} session={r.sessions.get("s-ctx")!} onBack={() => undefined} wide={false} />);
    expect([...host.querySelectorAll(".sep:not(.fork)")].map((x) => x.textContent)).toContain("本轮用时 4 分 12 秒");
  });

  it("shows full access in the navbar and the other settings behind ⋯", async () => {
    const r = demo();
    const s = r.sessions.get("s-idle")!;
    s.info = { ...s.info!, mode: ApprovalMode.FULL_ACCESS };
    mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
    expect(host.querySelector(".compose-card .access-warning")).toBeNull();
    expect(host.querySelector(".navbar .title")?.textContent).toContain("完全放行");
    await click(host.querySelector<HTMLButtonElement>(".navbar .more")!);
    // A list sheet: settings read 「名称 …… 值 ›」, with no session ID and no 取消 card.
    const rows = [...host.querySelectorAll(".menu .mi")];
    const mode = rows.find((x) => x.querySelector(".mt")?.textContent === "审批模式")!;
    expect(mode.querySelector(".mv.red")!.textContent).toBe("完全放行");
    expect(mode.querySelector(".chev")).toBeTruthy();
    expect(rows.find((x) => x.querySelector(".mt")?.textContent === "模型")?.querySelector(".mv")?.textContent).toBeTruthy();
    expect(host.querySelector(".menu-info")!.textContent).not.toContain("会话");
    expect(host.querySelector(".menu")!.textContent).not.toContain(s.id);
    expect(button("取消", host.querySelector(".menu")!)).toBeUndefined();
    expect(rows.find((x) => x.textContent === "结束会话…")?.classList.contains("danger")).toBe(true);
  });

  it("starts sessions only with a first message, and keeps the project head in place while typing", async () => {
    const r = demo();
    mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} />);
    expect(host.textContent).not.toContain("只打开终端");
    expect(host.querySelector(".phead")).toBeTruthy();
    await act(async () => host.querySelector<HTMLTextAreaElement>("textarea[aria-label=新会话的第一句话]")!.focus());
    expect(host.querySelector(".phead")).toBeTruthy();
    expect(host.querySelector(".navbar .title")?.textContent ?? "").not.toContain("foo");
    // Scrolled away, the head folds into the bar.
    const body = host.querySelector<HTMLElement>(".body")!;
    body.scrollTop = 200;
    await act(async () => void body.dispatchEvent(new Event("scroll", { bubbles: true })));
    expect(host.querySelector(".navbar .title")!.textContent).toContain("foo");
  });
});

it("writes a development build's commit once in the settings", async () => {
  const { versionText } = await import("./Settings");
  expect(versionText("0.8.2-dev.6098093", "60980937bd64665f")).toBe("0.8.2-dev.6098093");
  expect(versionText("0.8.2", "60980937bd64665f")).toBe("0.8.2 · 6098093");
  expect(versionText("0.8.2")).toBe("0.8.2");
});

describe("the session view", () => {
  const picked = () => host.querySelector('[role="tab"][aria-selected="true"]')?.textContent;
  const open = (r: DemoRemote, sid: string) => mount(<SessionPage remote={r} session={r.sessions.get(sid)!} onBack={() => undefined} wide={false} />);
  const close = () => {
    act(() => root?.unmount());
    host.remove();
  };

  it("opens every session in the view last picked on this device", async () => {
    const r = demo();
    open(r, "s-idle");
    expect(picked()).toBe("聊天");
    await click(button("终端"));
    expect(picked()).toBe("终端");
    close();
    open(r, "s-ctx");
    expect(picked()).toBe("终端");
    await click(button("聊天"));
    close();
    open(r, "s-idle");
    expect(picked()).toBe("聊天");
  });

  it("does not take a new session's first terminal for a pick", () => {
    const r = demo();
    r.termFirst.add("s-idle");
    open(r, "s-idle");
    expect(picked()).toBe("终端");
    close();
    open(r, "s-ctx");
    expect(picked()).toBe("聊天");
  });

  it("shows the chat of an ended session and keeps the pick", () => {
    const r = demo();
    localStorage.setItem("ra-view", "term");
    const ended = [...r.sessions.values()].find((s) => s.info?.status === SessionStatus.EXITED)!;
    open(r, ended.id);
    expect(picked()).toBe("聊天");
    expect(localStorage.getItem("ra-view")).toBe("term");
  });

  it("starts the chat with one line, the time and the folder", () => {
    const r = demo();
    open(r, "s-idle");
    expect(host.textContent).not.toContain("会话从这里开始");
    expect(host.querySelector(".chat .sysline")?.textContent).toMatch(/· ~\/Code\//);
  });
});


it("refreshes the model picker and lists GPT-6.1-Sol alongside the default with its efforts", async () => {
  const r = demo();
  // Prime the old page-lifetime catalog, then let the daemon report a new one.
  await r.listModels("mac", Agent.CODEX);
  const original = r.machineCommand.bind(r);
  let reads = 0;
  r.machineCommand = async (machine, cmd) => {
    if (cmd?.case === "listModels") {
      reads++;
      return create(CommandResultSchema, { kind: { case: "listModels", value: {
        defaultModel: "gpt-6.1-sol",
        models: [{ slug: "gpt-6.1-sol", displayName: "GPT-6.1-Sol", defaultEffort: "low", efforts: ["low", "medium", "high", "xhigh", "max", "ultra"] }],
      } } });
    }
    return original(machine, cmd);
  };
  (r as unknown as { saved: unknown }).saved = create(DaemonSettingsSchema, { defaultModel: "gpt-6.1-sol", defaultEffort: "low" });
  mount(<WideHome remote={r} go={() => undefined} project="/Users/me/Code/foo" />);
  await fromMenu("新会话设置");
  await click([...document.querySelectorAll<HTMLButtonElement>(".sr")].find((b) => b.textContent?.startsWith("模型")));
  await vi.waitFor(() => expect(button("GPT-6.1-Sol", document)).toBeDefined());
  expect(button(/^默认模型/, document)).toBeDefined();
  await click(button(/^默认模型/, document));
  await vi.waitFor(() => expect(button("ultra", document)).toBeDefined());
  for (const e of ["low", "medium", "high", "xhigh", "max", "ultra"]) expect(button(e, document)).toBeDefined();
  await click(button("GPT-6.1-Sol", document));
  await click(button("high", document));
  await click(button("完成", document));
  await vi.waitFor(() => expect(savedSet(r, "/Users/me/Code/foo")).toMatchObject({ model: "gpt-6.1-sol", effort: "high" }), { timeout: 4000 });
  const before = reads;
  await fromMenu("新会话设置");
  await click([...document.querySelectorAll<HTMLButtonElement>(".sr")].find((b) => b.textContent?.startsWith("模型")));
  await vi.waitFor(() => expect(reads).toBeGreaterThan(before));
});

it("renames a session from ⋯, keeps what was typed when saving fails, and goes back to the automatic title", async () => {
  const r = demo();
  const s = r.sessions.get("s-run")!;
  const was = s.info!.title;
  mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
  const input = () => host.querySelector<HTMLInputElement>(".alert.rename input")!;
  const write = (text: string) =>
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input(), text);
      input().dispatchEvent(new Event("input", { bubbles: true }));
    });

  await fromMenu("重命名");
  expect(input().value).toBe(was);
  write("x".repeat(70));
  expect(input().value).toHaveLength(60);
  write("  登录超时：限流和重试 ");

  const command = vi.spyOn(r, "command");
  command.mockRejectedValueOnce(new CommandError("unavailable", "MacBook 不在线"));
  await click(button("保存"));
  await vi.waitFor(() => expect(host.querySelector(".alert.rename")?.textContent).toContain("没能保存：MacBook 不在线"));
  expect(input().value).toBe("  登录超时：限流和重试 ");

  await click(button("保存"));
  expect(command).toHaveBeenLastCalledWith(s.id, { case: "renameSession", value: { sessionId: s.id, title: "登录超时：限流和重试" } });
  await vi.waitFor(() => expect(host.querySelector(".alert.rename")).toBeNull(), { timeout: 3000 });
  expect(r.sessions.get("s-run")!.info!.title).toBe("登录超时：限流和重试");

  // Saving the same title sends nothing; an empty one clears it.
  command.mockClear();
  await fromMenu("重命名");
  await click(button("保存"));
  expect(command).not.toHaveBeenCalled();
  expect(host.querySelector(".alert.rename")).toBeNull();
  await fromMenu("重命名");
  write("");
  await click(button("保存"));
  expect(command).toHaveBeenLastCalledWith(s.id, { case: "renameSession", value: { sessionId: s.id, title: "" } });
});

it("sends a fork whose answer was lost again as the same request, and gives it up only once it expired", async () => {
  const r = demo();
  const row = () => [...host.querySelectorAll<HTMLButtonElement>(".menu .mi")].find((b) => /分叉/.test(b.textContent ?? ""))!;
  const s = r.sessions.get("s-idle")!;
  mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
  await click(host.querySelector<HTMLButtonElement>(".navbar .more")!);

  // The computer may have forked: the row says so and promises no second fork.
  const command = vi.spyOn(r, "command");
  command.mockRejectedValueOnce(new CommandError("unknown", "不确定电脑有没有执行"));
  await click(row());
  await vi.waitFor(() => expect(row().textContent).toBe("分叉会话上次的分叉结果不确定。再点会重发同一个请求，电脑收到过就不会再分叉一次"));
  const slot = command.mock.calls[0][2];
  expect(slot).toBeTruthy();

  // Closing the menu and opening it again does not forget it.
  await click(host.querySelector<HTMLElement>(".overlay .dim")!);
  await click(host.querySelector<HTMLButtonElement>(".navbar .more")!);
  expect(row().textContent).toContain("上次的分叉结果不确定");

  // The retry is the same request; one that comes back expired is not sent again.
  command.mockRejectedValueOnce(new CommandError("deadline_exceeded", "过期了"));
  await click(row());
  await vi.waitFor(() => expect(row().textContent).toMatch(/^放弃上次的分叉/));
  expect(command.mock.calls[1][2]).toBe(slot);
  await click(row());
  expect(command).toHaveBeenCalledTimes(2);
  expect(row().textContent).toBe("分叉会话");

  // Given up: the next fork is a new request.
  location.hash = "";
  await click(row());
  expect(command.mock.calls[2][2]).not.toBe(slot);
  await vi.waitFor(() => expect(location.hash).toMatch(/^#\/s\/s-fork-/), { timeout: 3000 });
});

it("keeps the route opened after a fork was sent when its reply arrives after unmount", async () => {
  const r = demo();
  const s = r.sessions.get("s-idle")!;
  const original = "#/s/" + s.id;
  location.hash = original;
  let reply!: (result: CommandResult) => void;
  vi.spyOn(r, "command").mockImplementationOnce(() => new Promise((resolve) => { reply = resolve; }));
  mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
  await fromMenu("分叉会话");
  await click(host.querySelector<HTMLElement>(".overlay .dim")!);
  act(() => root?.unmount());
  root = undefined;
  location.hash = "#/devices";
  try {
    await act(async () => {
      reply(create(CommandResultSchema, { kind: { case: "forkSession", value: { session: { id: "fork-late" } } } }));
    });
    expect(r.created.has("fork-late")).toBe(true);
    expect(location.hash).toBe("#/devices");
  } finally {
    location.hash = "";
  }
});

it("forks a session from ⋯: says why it cannot yet, reports a failure in the menu, and opens the fork", async () => {
  const r = demo();
  const row = () => [...host.querySelectorAll<HTMLButtonElement>(".menu .mi")].find((b) => /分叉/.test(b.textContent ?? ""))!;

  // A running session shows the item grayed out, with when it will work.
  mount(<SessionPage remote={r} session={r.sessions.get("s-run")!} onBack={() => undefined} wide={false} />);
  await click(host.querySelector<HTMLButtonElement>(".navbar .more")!);
  expect(row().disabled).toBe(true);
  expect(row().textContent).toBe("分叉会话这一轮结束后可用");
  act(() => root?.unmount());
  host.remove();

  const s = r.sessions.get("s-idle")!;
  mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
  await click(host.querySelector<HTMLButtonElement>(".navbar .more")!);
  expect(row().disabled).toBe(false);

  // A failure stays in the menu, under the item, which can be tried again.
  const forkCommand = r.command.bind(r);
  const command = vi.spyOn(r, "command");
  command.mockRejectedValueOnce(new CommandError("unavailable", "MacBook 没有回应"));
  await click(row());
  await vi.waitFor(() => expect(row().textContent).toBe("分叉会话没能分叉：MacBook 没有回应"));
  expect(row().disabled).toBe(false);

  // While it forks the row says so and the others wait; then the fork opens.
  location.hash = "";
  let reply!: Promise<CommandResult>;
  const forkGate = barrier();
  command.mockImplementationOnce((...args) => (reply = forkGate.wait.then(() => forkCommand(...args))));
  await click(row());
  expect(command).toHaveBeenLastCalledWith(s.id, { case: "forkSession", value: { sessionId: s.id } }, expect.anything());
  expect(row().textContent).toBe("正在分叉…");
  expect(button("重命名")!.disabled).toBe(true);
  // The hash changes synchronously in the command continuation; React
  // commits setMenu(false) separately. Await the reply inside act so the
  // complete UI outcome is committed before either assertion.
  await act(async () => { forkGate.release(); await reply; });
  expect(location.hash).toMatch(/^#\/s\/s-fork-/);
  expect(host.querySelector(".menu")).toBeNull();

  // The fork carries the conversation and says where it came from.
  const id = decodeURIComponent(location.hash.slice(4));
  expect(r.created.has(id)).toBe(true);
  await vi.waitFor(() => expect(r.sessions.get(id)).toBeTruthy(), { timeout: 3000 });
  const fork = r.sessions.get(id)!;
  expect(fork.info!.forkedFrom).toBe(s.id);
  act(() => root?.unmount());
  host.remove();
  mount(<SessionPage remote={r} session={fork} onBack={() => undefined} wide={false} />);
  await vi.waitFor(() => expect(host.querySelector(".sep.fork")).toBeTruthy());
  expect(host.querySelector(".ccol")?.textContent).toContain("已补充 12 个接口的说明");
  const link = host.querySelector<HTMLAnchorElement>(".sep.fork a")!;
  expect(link.textContent).toBe("补充接口文档");
  expect(link.getAttribute("href")).toBe("#/s/s-idle");
});

describe("the home timeline and its scope (client-v27)", () => {
  const texts = (sel: string) => [...host.querySelectorAll(sel)].map((e) => e.textContent ?? "");
  const foo = "/Users/me/Code/foo";
  const type = async (input: HTMLInputElement, value: string) =>
    act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });

  it("lists every project's sessions under all projects, naming the project on each row", () => {
    const r = demo();
    mount(<Home remote={r} go={() => undefined} />);
    expect(host.querySelector(".large .scope b")?.textContent).toBe("全部项目");
    expect(host.textContent).not.toContain("常用项目");
    // More than one project, CLI and machine: every live row has its third line.
    const live = [...host.querySelectorAll(".sess")];
    expect(live.length).toBeGreaterThan(0);
    for (const row of live) expect(row.querySelector(".mc")?.textContent).toMatch(/ · /);
    // Ended sessions are one line each, under a date group, with their project.
    expect(texts(".old span:first-child")).toContain("补充 README");
    expect(texts(".old span:last-child").every((x) => x.includes(" · "))).toBe(true);
    expect(host.querySelector(".old .dot")).toBeNull();
  });

  it("keeps a project's rows to what differs, and still shows what waits elsewhere", async () => {
    const r = demo();
    const went: string[] = [];
    mount(<Home remote={r} go={(x) => went.push(x)} />);
    await click(host.querySelector<HTMLButtonElement>(".large .scope")!);
    expect(document.querySelector(".sheet")?.textContent).toContain("看哪个项目");
    await click([...document.querySelectorAll<HTMLButtonElement>(".plist .pi")].find((b) => b.querySelector("b")?.textContent === "foo"));
    expect(document.querySelector(".sheet")).toBeNull();
    expect(host.querySelector(".large .scope b")?.textContent).toBe("foo");
    // Live rows are foo's only, and do not name the project.
    const live = texts(".sess .row1 b");
    expect(live).toContain("修复登录超时");
    expect(live).not.toContain("整理错误码");
    expect(texts(".sess .mc").some((x) => x.includes("foo"))).toBe(false);
    expect(texts(".old span:last-child").some((x) => x.includes("foo"))).toBe(false);
    // 需要处理 is the account's: bar's question is there, with its project.
    const bar = [...host.querySelectorAll(".todo")].find((c) => c.textContent?.includes("升级依赖"));
    expect(bar?.querySelector(".m")?.textContent).toContain("bar");
    // The new button goes straight to the project.
    await click(host.querySelector<HTMLButtonElement>('button[aria-label="新会话"]')!);
    expect(went).toEqual(["p/" + encodeURIComponent(foo)]);
    expect(r.focusProject).toBe(foo);
  });

  it("falls back to all projects when the saved project leaves the list", async () => {
    const r = demo();
    await vi.waitFor(() => expect(r.prefs.status).toBe("ready"));
    saveScope("/Users/me/Code/shop");
    mount(<Home remote={r} go={() => undefined} />);
    expect(host.querySelector(".large .scope b")?.textContent).toBe("shop");
    await act(async () => { await r.changePrefs("hide", "/Users/me/Code/shop"); });
    expect(host.querySelector(".large .scope b")?.textContent).toBe("全部项目");
  });

  it("filters by title without regard to case, keeping the groups", async () => {
    const r = demo();
    saveScope(ALL);
    mount(<Home remote={r} go={() => undefined} />);
    const box = host.querySelector<HTMLInputElement>('input[aria-label="按标题筛选"]')!;
    expect(box).toBeTruthy();
    await type(box, "readme");
    expect(texts(".old span:first-child")).toEqual(["补充 README"]);
    expect(host.querySelectorAll(".sess, .todo")).toHaveLength(0);
    expect(host.querySelector(".tail")?.textContent).toBe("1 个会话的标题里有「readme」");
    await type(box, "没有这个标题");
    expect(host.querySelector(".tail")?.textContent).toBe("没有标题里有「没有这个标题」的会话");
    await click(host.querySelector<HTMLButtonElement>('button[aria-label="清空筛选"]')!);
    expect(texts(".sess .row1 b")).toContain("修复登录超时");
  });

  it("is the sidebar on a wide window, where picking a project sends the main area home", async () => {
    const r = demo();
    const went: string[] = [];
    mount(<Sidebar remote={r} go={(x) => went.push(x)} selected="s-run" project="/Users/me/Code/bar" />);
    expect(host.querySelector(".sess.sel .row1 b")?.textContent).toBe("修复登录超时");
    // Waiting sessions are rows here, not cards.
    expect(host.querySelector(".todo")).toBeNull();
    await click(host.querySelector<HTMLButtonElement>(".sh .scope")!);
    expect(host.querySelector(".pop")).toBeTruthy();
    await click([...host.querySelectorAll<HTMLButtonElement>(".pop .pi")].find((b) => b.querySelector("b")?.textContent === "billing"));
    expect(host.querySelector(".pop")).toBeNull();
    expect(went).toEqual([""]);
    expect(host.querySelector(".sh .scope b")?.textContent).toBe("billing");
    // The session on screen stays listed though it is not billing's.
    expect(host.querySelector(".sess.sel .row1 b")?.textContent).toBe("修复登录超时");
    expect(host.querySelector(".sess.sel .mc")?.textContent).toContain("foo");
  });

  it("fetches further pages when shown past what is held, and all of them to filter", async () => {
    const r = demo();
    saveScope(ALL);
    let pages = 0;
    let all = 0;
    r.moreSessions = true;
    r.loadMoreSessions = async () => (pages++, false);
    r.loadAllSessions = async () => void all++;
    mount(<Home remote={r} go={() => undefined} />);
    // No machine counts more ended sessions than are held: nothing to fetch.
    expect(pages).toBe(0);
    expect(all).toBe(0);
    // One does: what is shown reaches past the pages held, so the next is asked for.
    const m = [...r.machines.values()][0];
    m.meta = create(MachineMetaSchema, { hostname: "MacBook", projects: [{ path: "/Users/me/Code/foo", ended: 90 }] });
    act(() => (r as unknown as { changed(): void }).changed());
    expect(pages).toBeGreaterThan(0);
    expect(host.querySelector(".tail")?.textContent).toMatch(/^共 \d+ 个，滚到底加载更早的$/);
    await type(host.querySelector<HTMLInputElement>('input[aria-label="按标题筛选"]')!, "readme");
    expect(all).toBeGreaterThan(0);
    expect(host.querySelector(".tail")?.textContent).toBe("正在加载更早的会话…");
    r.moreSessions = false;
    act(() => (r as unknown as { changed(): void }).changed());
    expect(host.querySelector(".tail")?.textContent).toBe("1 个会话的标题里有「readme」");
  });

  it("makes a project the scope from project management", async () => {
    const r = demo();
    const went: string[] = [];
    mount(<ManagePage remote={r} go={(x) => went.push(x)} onBack={() => undefined} wide={false} />);
    await click(host.querySelector<HTMLButtonElement>('[aria-label="看 shop 的会话"]')!);
    expect(went).toEqual([""]);
    act(() => root!.render(<Home remote={r} go={() => undefined} />));
    expect(host.querySelector(".large .scope b")?.textContent).toBe("shop");
  });

  it("starts in the scope's project on the wide home, and names the project only under all projects", () => {
    const r = demo();
    mount(<WideHome remote={r} go={() => undefined} />);
    expect(host.querySelector(".wide-start-title")?.textContent).toMatch(/^在 .+ 里开始一个任务$/);
    act(() => saveScope(foo));
    expect(host.querySelector(".wide-start-title")?.textContent).toBe("开始一个任务");
  });
});

it("fetches a CLI's earlier sessions before a session starts on it, and before its settings can be changed", async () => {
  const { ProjectSummarySchema } = await import("../gen/remoteagent/v1/remote_pb");
  const r = demo();
  const project = "/Users/me/Code/ai-bot/remote-agent";
  // Its one session held here ran Codex; its machine counts more ended ones than are held.
  const held = [...r.sessions.values()].filter((s) => s.info?.cwd === project);
  expect(held.every((s) => s.info?.agent !== Agent.CLAUDE)).toBe(true);
  const m = r.machines.get(held[0].machineId)!;
  m.meta!.projects.push(create(ProjectSummarySchema, { path: project, ended: 9 }));
  let pages = 0;
  r.moreSessions = true;
  r.loadMoreSessions = async () => (pages++, false);
  mount(<WideHome remote={r} go={() => undefined} project={project} />);
  const start = () => host.querySelector<HTMLButtonElement>('button[aria-label="开始新会话"]')!;
  const input = host.querySelector<HTMLTextAreaElement>("textarea[aria-label=新会话的第一句话]")!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, "对一下三月的账");
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  // A Codex session within the pages is held: Codex is settled, nothing is fetched for it.
  expect(pages).toBe(0);
  await vi.waitFor(() => expect(start().disabled).toBe(false), { timeout: 3000 });
  // Claude Code has no session held, and there are ended ones not fetched.
  await click(cliSwitch("Claude Code"));
  expect(pages).toBeGreaterThan(0);
  expect(start().disabled).toBe(true);
  expect(host.textContent).toContain("正在读取这个项目上次的设置…");
  // Its settings are not offered for change meanwhile.
  await click(host.querySelector<HTMLButtonElement>(".project-summary")!);
  expect(document.querySelector(".sheet")?.textContent).toContain("正在读取这个项目上次的设置…");
  // Every page is in and none was Claude Code's: it starts with the machine's defaults.
  r.moreSessions = false;
  act(() => (r as unknown as { changed(): void }).changed());
  expect(document.querySelector(".sheet")?.textContent).not.toContain("正在读取这个项目上次的设置…");
  await vi.waitFor(() => expect(start().disabled).toBe(false), { timeout: 3000 });
});

async function reviewType(el: HTMLInputElement | HTMLTextAreaElement, text: string) {
  await act(async () => {
    const proto = el.tagName === "TEXTAREA" ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(el, text);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

it("retains an unknown start across page unmounts", async () => {
  const r = demo();
  const slots: string[] = [];
  r.machineCommand = async (_m: string, _c: CommandKind, slot?: RequestSlot) => { slots.push(slot!.requestId); throw new CommandError("unknown", "连接超时"); };
  let ns!: ReturnType<typeof useNewSession>;
  function Box() { ns = useNewSession(r, "/Users/me/Code/foo", () => undefined); return null; }
  mount(<Box />);
  await act(async () => { await ns.create("task"); });
  act(() => root!.render(null));
  act(() => root!.render(<Box />));
  expect(ns.frozen).toBe(true);
  await act(async () => { await ns.create("task"); });
  expect(slots[1]).toBe(slots[0]);
});

it("holds saved launch settings while a draft is written", async () => {
  const r = demo();
  const path = "/Users/me/Code/foo";
  let ns!: ReturnType<typeof useNewSession>;
  function Box() { ns = useNewSession(r, path, () => undefined); return null; }
  mount(<Box />);
  const before = { ...ns.cfg };
  act(() => ns.latch());
  const saved = r.projectPrefs().settings.find(s => s.path === path)!;
  saved.machineId = "imac";
  saved.agent = Agent.CLAUDE;
  saved.agents[0].model = "changed";
  act(() => root!.render(<Box />));
  expect(ns.cfg).toEqual(before);
});

it("keeps the remembered CLI after a temporary fallback succeeds", async () => {
  const r = demo();
  const path = "/Users/me/Code/foo";
  const saved = r.projectPrefs().settings.find(s => s.path === path)!;
  saved.agent = Agent.CLAUDE;
  r.machines.get(saved.machineId)!.meta!.agents = r.machines.get(saved.machineId)!.meta!.agents.filter(a => a.agent === Agent.CODEX);
  let ns!: ReturnType<typeof useNewSession>;
  function Box() { ns = useNewSession(r, path, () => undefined); return null; }
  mount(<Box />);
  expect(ns.missing).toBe(Agent.CLAUDE);
  await act(async () => { await ns.create("task"); });
  await act(async () => { await vi.waitFor(() => expect(r.prefs.pending.size).toBe(0)); });
  expect(r.projectPrefs().settings.find(s => s.path === path)!.agent).toBe(Agent.CLAUDE);
});

it("appends a restored message to the current draft", async () => {
  const r = demo();
  r.drafts.set("s-idle", "draft");
  r.events("s-idle").push(create(EventSchema, { seq: BigInt(r.events("s-idle").length + 1), kind: { case: "inputState", value: { requestId: "restore", status: InputStatus.QUEUED, text: "restore" } } }));
  r.events("s-idle").push(create(EventSchema, { seq: BigInt(r.events("s-idle").length + 1), kind: { case: "inputState", value: { requestId: "restore", status: InputStatus.FAILED, reason: "image_not_recognized" } } }));
  mount(<SessionPage remote={r} session={r.sessions.get("s-idle")!} onBack={() => undefined} wide={false} />);
  await click(button("放回输入框"));
  expect(host.querySelector<HTMLTextAreaElement>('textarea[aria-label="消息"]')!.value).toBe("draft\n\nrestore");
});

it("keeps an approval reason across dock and detail mounts", async () => {
  const r = demo();
  const a = derive(r.events("s-appr")).approvals.values().next().value!;
  const { ApprovalActions } = await import("./Dock");
  const actions = <ApprovalActions remote={r} sid="s-appr" a={a} agent={Agent.CODEX} />;
  mount(actions);
  await click(button("不允许"));
  await reviewType(host.querySelector("textarea")!, "reason");
  act(() => root!.render(null));
  act(() => root!.render(actions));
  await click(button("不允许"));
  expect(host.querySelector<HTMLTextAreaElement>("textarea")!.value).toBe("reason");
});

it("shows failure and retry when resuming a paused message", async () => {
  const r = demo();
  const { PendingBubbles } = await import("./Queue");
  const d = derive([create(EventSchema, { kind: { case: "inputState", value: { requestId: "paused", status: InputStatus.QUEUED, paused: true, text: "message" } } })]);
  r.command = async () => { throw new CommandError("unavailable", "电脑不在线"); };
  function Page() { useRemote(r); return <PendingBubbles remote={r} session={r.sessions.get("s-idle")!} d={d} />; }
  mount(<Page />);
  await click(button("发送"));
  expect(host.textContent).toContain("发送没成功，仍已暂停");
  expect(button("重试")).toBeTruthy();
});

it("offers more ended matches while filtering titles", async () => {
  const r = demo();
  const ended = [...r.sessions.values()].find(s => s.info?.status === SessionStatus.EXITED)!;
  r.sessions.clear();
  for (let i = 0; i < 40; i++) r.sessions.set("match" + i, { ...ended, id: "match" + i, info: { ...ended.info!, title: "match " + i } });
  mount(<Home remote={r} go={() => undefined} />);
  await reviewType(host.querySelector<HTMLInputElement>('input[aria-label="按标题筛选"]')!, "match");
  expect(host.querySelectorAll(".old").length).toBe(30);
  await click(host.querySelector<HTMLButtonElement>("button.tail")!);
  expect(host.querySelectorAll(".old").length).toBe(40);
});

it("fills only missing project settings after a first start races with another client", async () => {
  const r = demo();
  const path = "/Users/me/Code/shop";
  let ns!: ReturnType<typeof useNewSession>;
  function Box() { ns = useNewSession(r, path, () => undefined); return null; }
  mount(<Box />);
  let finish!: (result: CommandResult) => void;
  const original = r.machineCommand.bind(r);
  r.machineCommand = async (machine: string, cmd: CommandKind): Promise<CommandResult> => cmd?.case === "createSession" ? new Promise<CommandResult>(resolve => { finish = resolve; }) : original(machine, cmd);
  let start!: Promise<boolean>;
  act(() => { start = ns.create("task"); });
  await act(async () => { await r.changePrefs("settings", path, { id: "other-client", kind: "settings", path, at: Date.now(), settings: { machineId: "imac", sets: [{ agent: Agent.CODEX, model: "gpt-6-sol", effort: "high", mode: ApprovalMode.AUTO, standardContext: false }] } }); });
  await act(async () => { finish(create(CommandResultSchema, { kind: { case: "createSession", value: { session: { id: "started" } } } })); await start; });
  await act(async () => { await vi.waitFor(() => expect(r.prefs.pending.size).toBe(0)); });
  expect(r.projectPrefs().settings.find(s => s.path === path)).toMatchObject({ machineId: "imac", agents: [{ model: "gpt-6-sol", effort: "high", mode: ApprovalMode.AUTO }] });
});

it.each([['removed', 'high'], ['gpt-6-astra', 'removed']])("falls back from unavailable model or effort (%s, %s) without changing saved settings", async (model, effort) => {
  const r = demo();
  const path = "/Users/me/Code/foo";
  const saved = savedSet(r, path)!;
  saved.model = model;
  saved.effort = effort;
  let ns!: ReturnType<typeof useNewSession>;
  function Box() { ns = useNewSession(r, path, () => undefined); return null; }
  mount(<Box />);
  await act(async () => { await r.listModels("mac", Agent.CODEX); });
  act(() => root!.render(<Box />));
  expect(ns.cfg).toMatchObject({ model: "", effort: "" });
  expect(ns.configFallback).not.toBe("");
  const calls: CommandKind[] = [];
  r.machineCommand = async (_m, cmd) => { calls.push(cmd); throw new CommandError("failed_precondition", "failed"); };
  await act(async () => { await ns.create("task"); });
  expect(calls[0]).toMatchObject({ case: "createSession", value: { model: "", effort: "" } });
  expect(savedSet(r, path)).toMatchObject({ model, effort });
});

it("offers a retained refusal reason for copying after another client resolves the approval", async () => {
  const r = demo();
  const a = derive(r.events("s-appr")).approvals.values().next().value!;
  const { ApprovalActions } = await import("./Dock");
  mount(<ApprovalActions remote={r} sid="s-appr" a={a} agent={Agent.CODEX} />);
  await click(button("不允许"));
  await reviewType(host.querySelector<HTMLTextAreaElement>("textarea.reason")!, "retained reason");
  const writeText = vi.fn(async () => undefined);
  vi.spyOn(navigator.clipboard, "writeText").mockImplementation(writeText);
  act(() => {
    r.events("s-appr").push(create(EventSchema, { seq: BigInt(r.events("s-appr").length + 1), kind: { case: "approvalResolved", value: { approvalId: a.approvalId, decision: ApprovalDecision.ALLOW } } }));
    r.touch();
    root!.render(<SessionPage remote={r} session={r.sessions.get("s-appr")!} onBack={() => undefined} wide={false} />);
  });
  expect(host.textContent).toContain("retained reason");
  await click(button("复制"));
  expect(writeText).toHaveBeenCalledWith("retained reason");
});

it("shows the pending start and its original message when returning to the project", async () => {
  const r = demo();
  const slots: string[] = [];
  const original = r.machineCommand.bind(r);
  r.machineCommand = async (machine: string, cmd: CommandKind, slot?: RequestSlot) => {
    if (cmd?.case === "createSession") { slots.push(slot!.requestId); throw new CommandError("unknown", "连接超时"); }
    return original(machine, cmd);
  };
  const page = <ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} />;
  mount(page);
  await reviewType(host.querySelector<HTMLTextAreaElement>('textarea[aria-label="新会话的第一句话"]')!, "task");
  await act(async () => { await vi.waitFor(() => expect(sendButton()?.disabled).toBe(false)); });
  await click(sendButton());
  await act(async () => { await vi.waitFor(() => expect(host.textContent).toContain("不确定是否已经启动")); });
  act(() => root!.render(null));
  act(() => root!.render(page));
  expect(host.querySelector<HTMLTextAreaElement>('textarea[aria-label="新会话的第一句话"]')!.value).toBe("task");
  expect(host.textContent).toContain("不确定是否已经启动");
  await click(button("重试"));
  expect(slots).toHaveLength(2);
  expect(slots[1]).toBe(slots[0]);
});

it("keeps unsent new-session text by project across navigation and layout changes", async () => {
  const r = demo();
  const a = "/Users/me/Code/foo";
  const b = "/Users/me/Code/shop";
  const input = () => host.querySelector<HTMLTextAreaElement>('textarea[aria-label="新会话的第一句话"]')!;
  const page = (p: string) => <WideHome remote={r} go={() => undefined} project={p} />;
  mount(<ProjectPage remote={r} pkey={a} go={() => undefined} onBack={() => undefined} />);
  await reviewType(input(), "  尚未发送的需求 A  ");
  act(() => root!.render(<p>设置</p>));
  act(() => root!.render(page(a)));
  expect(input().value).toBe("  尚未发送的需求 A  ");
  // WideHome reuses NewBox when its project changes.
  act(() => root!.render(page(b)));
  expect(input().value).toBe("");
  await reviewType(input(), "需求 B");
  act(() => root!.render(page(a)));
  expect(input().value).toBe("  尚未发送的需求 A  ");
  act(() => root!.render(<ProjectPage remote={r} pkey={b} go={() => undefined} onBack={() => undefined} />));
  expect(input().value).toBe("需求 B");
});

it("consumes only the new-session text sent by a successful start while its page is gone", async () => {
  const r = demo();
  const path = "/Users/me/Code/foo";
  let ns!: ReturnType<typeof useNewSession>;
  function Box() { useRemote(r); ns = useNewSession(r, path, () => undefined); return <NewBox ns={ns} name="foo" />; }
  const input = () => host.querySelector<HTMLTextAreaElement>('textarea[aria-label="新会话的第一句话"]')!;
  let finish!: (result: CommandResult) => void;
  const original = r.machineCommand.bind(r);
  r.machineCommand = async (machine, cmd) => cmd?.case === "createSession"
    ? new Promise<CommandResult>(resolve => { finish = resolve; }) : original(machine, cmd);
  mount(<Box />);
  await reviewType(input(), "  first task  ");
  let start!: Promise<boolean>;
  act(() => { start = ns.create("first task"); });
  // A later draft must survive the first request's success, even unmounted.
  await reviewType(input(), "next task");
  act(() => root!.render(null));
  await act(async () => { finish(create(CommandResultSchema, { kind: { case: "createSession", value: { session: { id: "new-1" } } } })); await start; });
  act(() => root!.render(<Box />));
  expect(input().value).toBe("next task");
  act(() => { start = ns.create("next task"); });
  act(() => root!.render(null));
  await act(async () => { finish(create(CommandResultSchema, { kind: { case: "createSession", value: { session: { id: "new-2" } } } })); await start; });
  act(() => root!.render(<Box />));
  expect(input().value).toBe("");
});

it("shows async questions as ordinary chat with options and accepts a typed reply", async () => {
  const r = demo();
  const s = r.sessions.get("s-idle")!;
  const es = r.events(s.id);
  es.push(create(EventSchema, { seq: BigInt(es.length + 1), kind: { case: "questionAsked", value: {
    questionId: "async", async: true, questions: [
      { text: "请选择测试方式。", options: [{ label: "A", description: "完整测试" }, { label: "B", description: "快速测试" }] },
      { text: "还有什么要求？" },
    ],
  } } }));
  const command = vi.spyOn(r, "command").mockResolvedValue(create(CommandResultSchema));
  mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
  const messages = [...host.querySelectorAll(".ai")].map(el => el.textContent).join("\n");
  expect(messages).toContain("请选择测试方式。");
  expect(messages).toContain("A");
  expect(messages).toContain("完整测试");
  expect(messages).toContain("B");
  expect(messages).toContain("快速测试");
  expect(messages).toContain("还有什么要求？");
  expect(derive(es).questions.size).toBe(0);
  expect(host.querySelector(".dock")).toBeNull();
  await reviewType(host.querySelector<HTMLTextAreaElement>('textarea[aria-label="消息"]')!, "选择 A");
  await click(sendButton());
  await vi.waitFor(() => expect(command).toHaveBeenCalled());
  expect(command.mock.calls[0][1]).toMatchObject({ case: "send", value: { text: "选择 A" } });
});

it("puts a daemon-rejected image message back with its text, original option and source", async () => {
  const r = demo();
  r.prepareImage = async (_f, original) => ({ bytes: new Uint8Array(original ? 20 : 4), format: "png", width: 1, height: 1 });
  let uploads = 0;
  r.uploadImage = async () => ({ imageId: "id-" + ++uploads, expiresMs: Number.MAX_SAFE_INTEGER });
  r.command = async () => { throw new CommandError("not_found", "image id is not on this machine; upload it again"); };
  mount(<SessionPage remote={r} session={r.sessions.get("s-idle")!} onBack={() => undefined} wide={false} />);
  const source = new File(["source"], "a.png", { type: "image/png" });
  await act(async () => {
    const picker = host.querySelector<HTMLInputElement>("input[type=file]")!;
    Object.defineProperty(picker, "files", { value: [source], configurable: true });
    picker.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await click(host.querySelector<HTMLButtonElement>('[role="switch"].orig')!);
  await act(async () => {
    const input = host.querySelector<HTMLTextAreaElement>("textarea.draft")!;
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, "看原图");
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await click(sendButton());
  await vi.waitFor(() => expect(host.textContent).toContain("图片不在电脑上"));
  expect(host.querySelector(".me.fail")!.textContent).not.toContain("重试");
  await click(button("放回输入框"));
  const tray = r.trays.get("s-idle")!;
  expect(r.drafts.get("s-idle")).toBe("看原图");
  expect(tray.original).toBe(true);
  expect(tray.items[0].file).toBe(source);
  expect(tray.totalBytes()).toBe(20);
  expect(r.outbox("s-idle")).toEqual([]);
  expect(uploads).toBe(3);
});

describe("adding devices on the current authority", () => {
  const hint = "要添加电脑或客户端，在管理端（Android · Pixel 9）上打开「设置 › 添加设备」。";

  it("says where to add a device in settings, with no 添加设备 to tap", async () => {
    const r = demo();
    const go = vi.fn();
    const { Settings } = await import("./Settings");
    mount(<Settings remote={r} go={go} />);
    expect(button(/添加设备|添加电脑或客户端/)).toBeUndefined();
    expect(host.textContent).toContain(hint);
  });

  it("says the same on the devices page", async () => {
    const r = demo();
    const { DevicesPage } = await import("./Settings");
    mount(<DevicesPage remote={r} go={() => undefined} onBack={() => undefined} />);
    expect(button(/添加设备|添加电脑或客户端/)).toBeUndefined();
    expect(host.textContent).toContain(hint);
  });

  it("tells an account with no computer where to add one, without a button", () => {
    const r = createDemo(new URLSearchParams("empty=none"));
    r.start();
    const go = vi.fn();
    mount(<Home remote={r} go={go} />);
    expect(host.textContent).toContain("还没有电脑。在管理端（Android · Pixel 9）上打开「设置 › 添加设备」，把电脑加进来。");
    expect(button(/添加设备/)).toBeUndefined();
    expect(go).not.toHaveBeenCalledWith("add");
  });
});

describe("client names", () => {
  it("calls an unnamed client by what it runs on and asks for no name", async () => {
    const r = demo();
    await vi.waitFor(() => expect(r.devicesStatus).toBe("ready"));
    const { DevicesPage } = await import("./Settings");
    mount(<DevicesPage remote={r} go={() => undefined} onBack={() => undefined} />);
    const names = [...host.querySelectorAll(".row.client .client-info")].map((x) => x.firstChild?.textContent);
    expect(names).toEqual(["Chrome · macOS", "Android · Pixel 9", "客户端"]);
    expect(document.body.textContent).not.toContain("起个名字");
  });
});

describe("devices in settings", () => {
  it("draws clients with an icon as computers are, and ends every computer's row with its status", async () => {
    const r = demo();
    await vi.waitFor(() => expect(r.devicesStatus).toBe("ready"));
    const { Settings } = await import("./Settings");
    mount(<Settings remote={r} go={() => undefined} />);
    const rows = [...host.querySelectorAll<HTMLButtonElement>(".grp .row")].filter((x) => x.querySelector(".ico"));
    const named = (name: string) => rows.find((x) => x.querySelector(".row-name")?.firstChild?.textContent === name)!;
    // MacBook has a quota: on a second line, with 在线 still at the end.
    const mac = named("MacBook");
    expect(mac.querySelector(".row-name small")!.textContent).toContain("Codex");
    expect(mac.querySelector(".row-name")!.textContent).not.toContain("在线");
    expect(mac.querySelector(".v")!.textContent).toBe("在线");
    expect(named("Chrome · macOS").querySelector(".ico svg")).toBeTruthy();
    expect(named("Android · Pixel 9").querySelector(".ico svg")).toBeTruthy();
  });
});

it("keeps task-owned approvals in attention and counts while omitting idle task sessions", () => {
 const r=demo(); const s=r.sessions.get("s-plan")!;s.info!.taskOwned=true;
 expect(todoList(r).map(s=>s.id)).toContain(s.id);
 expect(sessionCounts(r,[s])).toBe("1 个待处理");
 s.info!.planOpen=false;s.info!.pendingApprovals=0;s.info!.pendingQuestions=0;s.info!.status=SessionStatus.IDLE;
 expect(sessionCounts(r,[s])).toBe("");
 expect(todoList(r).map(s=>s.id)).not.toContain(s.id);
});
