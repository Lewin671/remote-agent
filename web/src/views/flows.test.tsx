import { t as message } from "../i18n";
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
import { derive, dockItems, projects, stateOf } from "../model";
import { DaemonSettingsSchema } from "../gen/remoteagent/v1/local_pb";
import { CommandResultSchema, type CommandResult } from "../gen/remoteagent/v1/remote_pb";
import { CommandError, type CommandKind, type RequestSlot } from "../remote";
import { ModeSheet, SessionSheet } from "../sheets";
import { Dock, dockContentMax, PlanPage, QuestionPage } from "./Dock";
import { Home, Sidebar, todoList, WideHome } from "./Home";
import { ALL, saveScope } from "../scope";
import { loadProjectConfig } from "../storage";
import { NewBox, NewFlow, NewProjectSheet, ProjectPicker, useNewSession } from "./NewSession";
import { ProjectPage } from "./Project";
import { addProject, ManagePage, sessionCounts } from "./Projects";
import { SessionPage } from "./Session";
import { ImageStrip, LocalThumbs } from "./Images";
import { ImageFormat, ImageRefSchema, ImageSync } from "../gen/remoteagent/v1/event_pb";
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
const sendButton = () => host.querySelector<HTMLButtonElement>(`button[aria-label="${message("composer.send")}"], button[aria-label="${message("new_session.start")}"]`) ?? undefined;

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
  [...document.querySelectorAll(":is(.sheet, .composer-picker) .mi")].find((r) => r.querySelector(".mt")?.textContent === name)?.querySelector(".mv")?.textContent ?? "";

/** The CLI the new task's sentence names, and picking another there. */
const cliPicked = () => host.querySelector(".tok.cli")?.textContent;
async function pickCli(name: string) {
  await click(host.querySelector<HTMLButtonElement>(".tok.cli")!);
  await click(button(name, document.querySelector(":is(.sheet, .composer-picker)")!));
}
function pickCliNow(name: string) {
  act(() => host.querySelector<HTMLButtonElement>(".tok.cli")!.click());
  act(() => button(name, document.querySelector(":is(.sheet, .composer-picker)")!)!.click());
}

/** The project the wide new task names, and the start's sentence there. */
const startProject = () => host.querySelector(".wstart .sent .tok.p")?.textContent;

it("keeps the context warning and the settings behind ⋯ on a narrow phone", async () => {
  const viewport = (window as unknown as { happyDOM: { setViewport(size: { width: number; height: number }): void } }).happyDOM;
  const r = demo();
  const s = r.sessions.get("s-ctx")!;
  try {
    act(() => viewport.setViewport({ width: 320, height: 740 }));
    mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
    // ⋯ carries the high context as a dot, and its menu the reading; ＋
    // only adds images.
    expect(host.querySelector(".more.context-high")?.getAttribute("aria-label")).toContain("上下文占用 86%");
    expect(host.querySelector(".cm-plus")?.getAttribute("aria-label")).toBe(message("image.add"));
    expect(host.querySelector(`button[aria-label="${message("composer.send")}"]`)).toBeTruthy();
    await fromMenu("上下文占用已用 86%");
    expect(host.textContent).toContain(message("session.settings"));
  } finally {
    act(() => viewport.setViewport({ width: 1024, height: 768 }));
  }
});

it("lets the home card handle one approval after another", async () => {
  const r = demo();
  mount(<Home remote={r} go={() => undefined} />);
  // s-appr waits on two approvals and is listed first: the dark card.
  const card = () => host.querySelector(".tc")!;
  expect(card().textContent).toContain("rm -rf gen/remoteagent");
  await click(button(message("approval.allow"), card()));
  // Submitted, the card is one line of where it stands until the next comes up.
  await vi.waitFor(() => expect(card().textContent).toContain("/etc/hosts"), { timeout: 3000 });
  // The second approval's buttons start fresh, not locked as "允许中…".
  const allow = button(message("approval.allow"), card());
  expect(allow?.disabled).toBe(false);
  expect(button(message("approval.deny"), card())?.disabled).toBe(false);
});

it("names the computer on the home card only where the account has more than one", () => {
  const several = demo();
  expect(several.machines.size).toBeGreaterThan(1);
  mount(<Home remote={several} go={() => undefined} />);
  expect(host.querySelector(".tc .m")?.textContent).toMatch(/ · /);
  act(() => root!.unmount());
  const one = demo();
  const keep = one.sessions.get("s-appr")!.machineId;
  for (const id of [...one.machines.keys()]) if (id !== keep) one.machines.delete(id);
  mount(<Home remote={one} go={() => undefined} />);
  expect(host.querySelector(".tc .m")?.textContent).not.toMatch(/ · /);
});

it("chooses the computer for a new folder with a segmented control, only where there is more than one, and has no 取消", () => {
  const listing = { kind: { case: "listDirectories", value: { path: "/home/x", parent: "", dirs: [] } } } as never;
  const several = demo();
  vi.spyOn(several, "machineCommand").mockResolvedValue(listing);
  mount(<NewProjectSheet remote={several} onDone={() => undefined} onClose={() => undefined} />);
  expect(host.querySelectorAll(".seg.machines button").length).toBe(several.machines.size);
  expect(button(message("common.cancel", {}, "zh-CN"))).toBeUndefined();
  act(() => root!.unmount());
  const one = demo();
  vi.spyOn(one, "machineCommand").mockResolvedValue(listing);
  for (const id of [...one.machines.keys()].slice(1)) one.machines.delete(id);
  mount(<NewProjectSheet remote={one} onDone={() => undefined} onClose={() => undefined} />);
  expect(host.querySelector(".seg.machines")).toBeNull();
  expect(button(message("common.cancel", {}, "zh-CN"))).toBeUndefined();
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
  await vi.waitFor(() => expect(host.textContent).toContain(message("dock.question_progress", {"index":2,"count":2})), { timeout: 3000 });
  // The keyboard comes up and leaves no room, then goes away again.
  act(() => root!.render(<AskDock r={r} room={100} />));
  expect(host.querySelector(".pending:not(.open)")).toBeTruthy();
  act(() => root!.render(<AskDock r={r} />));
  expect(host.textContent).toContain(message("dock.question_progress", {"index":2,"count":2}));
  expect(host.textContent).toContain("要顺便更新 go.mod 的 Go 版本吗？");
});

it("moves to the next question when a retried answer goes through", async () => {
  const r = demo();
  // The first answer's outcome is lost; the retry succeeds.
  let calls = 0;
  const real = r.command.bind(r);
  r.command = async (sid: string, cmd: CommandKind, slot?: RequestSlot): Promise<CommandResult> => {
    if (cmd?.case === "answerQuestion" && calls++ === 0) throw new CommandError("unknown_outcome", "连接超时");
    return real(sid, cmd, slot);
  };
  mount(<AskDock r={r} />);
  await click(button(/^只升小版本/));
  await vi.waitFor(() => expect(button(message("common.retry", {}, "zh-CN"))).toBeTruthy(), { timeout: 3000 });
  await click(button(message("common.retry", {}, "zh-CN")));
  await vi.waitFor(() => expect(host.textContent).toContain(message("dock.question_progress", {"index":2,"count":2})), { timeout: 3000 });
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
  for (const b of ["拒绝", "允许"]) {
    const el = button(b, dock)!;
    expect(scroll.contains(el)).toBe(false);
  }
});

/** What the project has stored for new sessions of a CLI. */
const savedSet = (r: DemoRemote, path: string, agent = Agent.CODEX) => r.projectPrefs().settings.find((s) => s.path === path)?.agents.find((a) => a.agent === agent);

it("keeps saved project settings as sessions change", () => {
  const r = demo();
  mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} />);
  // The remembered CLI is the one the sentence names; the summary does not name it again, nor an approval mode.
  const line = () => `${cliPicked()} / ${host.querySelector(".acc .project-summary")?.textContent}`;
  expect(line()).toMatch(/^Codex \/ gpt-6-astra$/i);
  // The Claude Code session in foo becomes the most recent one.
  act(() => {
    r.sessions.get("s-claude")!.updatedMs = BigInt(Date.now() + 60_000);
    (r as unknown as { changed(): void }).changed();
  });
  expect(line()).toMatch(/^Codex \/ gpt-6-astra$/i);
});

it("leaves an unknown approval mode unsaid in the navbar, and a new session does not say a mode at all", async () => {
  const r = demo();
  const sent: { mode?: number }[] = [];
  r.machineCommand = async (m: string, cmd: CommandKind): Promise<CommandResult> => {
    if (cmd?.case === "createSession") {
      sent.push({ mode: cmd.value.mode });
      throw new CommandError("failed_precondition", "stop here");
    }
    return DemoRemote.prototype.machineCommand.call(r, m, cmd);
  };
  // s-run's CLI has not said its mode.
  mount(<SessionPage remote={r} session={r.sessions.get("s-run")!} onBack={() => undefined} wide={false} />);
  expect(host.querySelector(".navbar .title > span")!.textContent).toBe(message("list_state.running", {}, "zh-CN"));
  act(() => root!.unmount());
  host.remove();
  // Neither the project nor the machine says how a new Codex session starts.
  for (const a of r.machines.get("mac")!.meta!.agents) a.defaultModeSource = 0;
  mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} />);
  await vi.waitFor(() => expect(host.querySelector(".project-summary")?.textContent).toMatch(/^GPT-6-Astra · medium$/));
  await fromMenu("新会话设置");
  expect(document.querySelector(":is(.sheet, .composer-picker)")!.textContent).not.toMatch(/审批|未读到/);
  await click(button(message("common.done", {}, "zh-CN"), document));
  await act(async () => {
    const input = host.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${message("new_session.first_message", {}, "zh-CN")}"]`)!;
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, "加限流");
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await click(sendButton());
  await vi.waitFor(() => expect(sent).toHaveLength(1));
  expect(sent[0]).toMatchObject({ mode: ApprovalMode.UNSPECIFIED });
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
    expect(host.textContent).toContain(message("home.list_failed", {"reason":message("errors.connection_timed_out")}, "zh-CN"));
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
  expect(host.textContent).toContain(message("list_state.agent_asking", {"agent":"Claude Code"}, "zh-CN"));
  expect(button(message("dock.select_then_submit"))?.disabled).toBe(true);
  await click(button(/^按 IP/));
  await click(button(/^按设备/));
  // Ticking sends nothing yet.
  expect(sent).toEqual([]);
  await click(button(message("dock.submit_selected", {"count":2})));
  await vi.waitFor(() => expect(sent).toHaveLength(1));
  expect(sent[0]).toMatchObject({ questionId: "cq", questionIndex: 0, options: [1, 3] });
});

it("greys out modes a Claude Code session cannot switch to, and effort for a model without any", async () => {
  const r = demo();
  const s = r.sessions.get("s-claude-idle")!;
  mount(<ModeSheet remote={r} s={s} onClose={() => undefined} />);
  expect(button(new RegExp("^" + message("modes.full_access")))?.disabled).toBe(true);
  expect(host.textContent).toContain(message("modes.full_unavailable"));
  // The model list says which models come without auto; until it has come,
  // nothing is greyed out.
  await vi.waitFor(() => expect(button(new RegExp("^" + message("modes.auto")))?.disabled).toBe(true), { timeout: 3000 });
  expect(host.textContent).toContain(message("modes.model_unsupported"));
  expect(button(new RegExp("^" + message("modes.ask")))?.disabled).toBe(false);
  // acceptEdits is none of the four: nothing is ticked, and it says so.
  expect(host.querySelector(".ck")).toBeNull();
  expect(host.textContent).toContain(message("modes.current_terminal", {"mode":"accept edits"}));
  expect(host.textContent).toContain("不提供切到 accept edits 的按钮");
  act(() => root!.render(<SessionSheet remote={r} s={s} running={false} onClose={() => undefined} />));
  await vi.waitFor(() => expect(host.textContent).toContain(message("session.no_effort_adjustment", {"model":"Haiku 4.5"})), { timeout: 3000 });
  expect(host.querySelector(".slider")).toBeNull();
});

it("offers no clear-and-implement on a Claude Code plan", () => {
  const r = demo();
  const plan = create(PlanProposedSchema, { text: "1. do it" });
  mount(<PlanPage remote={r} sid="s-claude" plan={plan} title="t" onBack={() => undefined} />);
  expect(button(message("approval.approve"))).toBeTruthy();
  expect(button(message("modes.plan_edit"))).toBeTruthy();
  expect(button(message("dock.plan_clear"))).toBeUndefined();
  act(() => root!.render(<PlanPage remote={r} sid="s-plan" plan={plan} title="t" onBack={() => undefined} />));
  expect(button(message("dock.plan_clear"))).toBeTruthy();
});

it("shows Antigravity session settings as read-only, locks full-access modes, and hides fork", async () => {
  const r = demo();
  const s = r.sessions.get("s-idle")!;
  s.info = {
    ...s.info!,
    agent: Agent.ANTIGRAVITY,
    model: "gemini-3.1-pro-high",
    effort: "high",
    mode: ApprovalMode.ASK,
    fullAccessUnavailable: true,
    contextTokens: 294_000n,
    contextWindow: 1_048_000n,
  };
  r.machineCommand = async (m: string, cmd: CommandKind): Promise<CommandResult> => {
    if (cmd?.case === "listModels" && cmd.value.agent === Agent.ANTIGRAVITY) {
      return create(CommandResultSchema, {
        kind: {
          case: "listModels",
          value: {
            defaultModel: "gemini-3.1-pro-high",
            models: [
              { slug: "gemini-3.1-pro-high", displayName: "Gemini 3.1 Pro (High)", efforts: ["low", "medium", "high"], defaultEffort: "high" },
              { slug: "gemini-3-flash", displayName: "Gemini 3 Flash", efforts: ["low", "medium", "high"], defaultEffort: "high" },
            ],
          },
        },
      });
    }
    return DemoRemote.prototype.machineCommand.call(r, m, cmd);
  };
  mount(<ModeSheet remote={r} s={s} onClose={() => undefined} />);
  await vi.waitFor(() => expect(button(new RegExp("^" + message("modes.auto")))?.disabled).toBe(false), { timeout: 3000 });
  expect(host.textContent).not.toContain(message("modes.account_unsupported"));
  expect(button(new RegExp("^" + message("modes.full_access")))?.disabled).toBe(true);
  expect(host.textContent).toContain(message("modes.full_unavailable"));
  expect(button(new RegExp("^" + message("modes.ask")))?.disabled).toBe(false);

  // When started in full access, all four modes are locked.
  s.info = { ...s.info!, mode: ApprovalMode.FULL_ACCESS, modeLocked: true, fullAccessUnavailable: false };
  act(() => root!.render(<ModeSheet remote={r} s={s} onClose={() => undefined} />));
  for (const label of [/^询问/, /^自动/, /^计划/, /^完全放行/]) {
    expect(button(label)?.disabled).toBe(true);
  }
  expect(host.textContent).toContain(message("modes.locked"));

  // Session settings show context & compact, with read-only model and effort.
  act(() => root!.render(<SessionSheet remote={r} s={s} running={false} onClose={() => undefined} />));
  await vi.waitFor(() => expect(host.textContent).toContain("Gemini 3.1 Pro (High)"), { timeout: 3000 });
  expect(host.textContent).toContain("28%");
  expect(button(message("session.compact"))).toBeTruthy();
  expect(host.textContent).toContain(message("session.model_locked"));
  expect(host.textContent).toContain(message("session.effort_locked"));
  expect(host.querySelector(".efforts")).toBeNull();

  // Session ⋯ menu omits 分叉会话 for Antigravity, and plan dock shows Antigravity's note.
  act(() => root!.render(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />));
  await click(host.querySelector<HTMLButtonElement>(".navbar .more")!);
  expect([...host.querySelectorAll(".menu .mi")].some((b) => /分叉/.test(b.textContent ?? ""))).toBe(false);
  const plan = create(PlanProposedSchema, { text: "1. do it" });
  act(() =>
    root!.render(
      <Dock
        remote={r}
        sid={s.id}
        items={[{ kind: "plan", id: "plan", plan }]}
        agent={Agent.ANTIGRAVITY}
        drafting={false}
        onExpand={() => undefined}
        onDetail={() => undefined}
        onPlan={() => undefined}
        onTerminal={() => undefined}
      />,
    ),
  );
  expect(host.textContent).toContain(message("dock.agy_plan_note"));
});

it("replaces the new-task page in the history with the session it started", async () => {
  const r = demo();
  const go = vi.fn();
  let ns: ReturnType<typeof useNewSession> | undefined;
  function Box() {
    ns = useNewSession(r, "/Users/me/Code/foo", go);
    return null;
  }
  mount(<Box />);
  await act(async () => void (await ns!.create("在 foo 里做")));
  expect(go).toHaveBeenCalledTimes(1);
  expect(go).toHaveBeenCalledWith(expect.stringMatching(/^s\//), true);
});

it("never sends one project's unknown start from another project's box", async () => {
  const r = demo();
  const sent: { cwd?: string; prompt?: string; requestId?: string }[] = [];
  r.machineCommand = async (_m: string, cmd: CommandKind, slot?: RequestSlot): Promise<CommandResult> => {
    if (cmd?.case === "createSession") {
      sent.push({ cwd: cmd.value.cwd, prompt: cmd.value.prompt, requestId: slot?.requestId });
      throw new CommandError("unknown_outcome", "连接超时");
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
  expect(sent[1]).toMatchObject({ cwd: "/Users/me/Code/shop", prompt: message("new_session.location", {"project":" shop 里做"}) });
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
  expect(cmd.nextElementSibling?.classList.contains("y")).toBe(true);
  // Nothing of it is cut away behind a page: it scrolls with the rest.
  expect(button(message("markdown.expand", {}, "zh-CN"))).toBeUndefined();
});

it("puts a card's command first, five whole lines of it, and allows only once it is seen whole", async () => {
  const r = createDemo(new URLSearchParams("longcmd=1"));
  r.start();
  mount(<Home remote={r} go={() => undefined} />);
  const card = () => host.querySelector(".tc")!;
  const cmd = card().querySelector("pre.cmd")!;
  expect(cmd.textContent).toContain("set -euo pipefail");
  // Head, title, where, then the command, then the description, then the buttons.
  const order = [...card().children].map((e) => e.className || e.tagName);
  expect(order.indexOf("cmd clamp")).toBeGreaterThan(order.indexOf("m"));
  expect(order.indexOf("cmd clamp")).toBeLessThan(order.indexOf("y"));
  expect(order.indexOf("y")).toBeLessThan(order.indexOf("btns decide"));
  // No layout here: the count is the command's own 40 lines, five shown.
  expect(card().querySelector(".ml")!.textContent).toBe(message("markdown.ios_hidden_lines", {"count":35}));
  // Not seen whole, it cannot be allowed: 不允许 and 看完整命令.
  expect(button(message("approval.allow"), card())).toBeUndefined();
  expect(button(message("approval.deny"), card())).toBeTruthy();
  await click(button(message("approval.full_command"), card()));
  expect(cmd.classList.contains("clamp")).toBe(false);
  expect(card().querySelector(".ml")!.textContent).toContain(message("markdown.line_count", {"count":40}));
  expect(button(message("approval.allow"), card())?.disabled).toBe(false);
  await click(button(message("markdown.collapse", {}, "zh-CN"), card()));
  expect(cmd.classList.contains("clamp")).toBe(true);
  expect(button(message("approval.allow"), card())).toBeUndefined();
});

it("allows a card's command of five lines or fewer right away", () => {
  const r = demo();
  mount(<Home remote={r} go={() => undefined} />);
  // s-appr's first approval runs five lines.
  const card = host.querySelector(".tc")!;
  expect(card.querySelector("pre.cmd")!.textContent).toContain("rm -rf gen/remoteagent");
  expect(card.querySelector("pre.cmd")!.classList.contains("clamp")).toBe(false);
  expect(card.textContent).not.toContain("没显示");
  expect(button(message("approval.full_command"), card)).toBeUndefined();
  expect(button(message("approval.allow"), card)).toBeTruthy();
});

it("writes a reason in place on 不允许, then shrinks to a line once sent", async () => {
  const r = demo();
  const sent = barrier();
  const command = r.command.bind(r);
  r.command = async (...args) => { await sent.wait; return command(...args); };
  mount(<Home remote={r} go={() => undefined} />);
  const card = () => host.querySelector(".tc")!;
  await click(button(message("approval.deny"), card()));
  const field = card().querySelector<HTMLTextAreaElement>("textarea.inp")!;
  expect(field.placeholder).toBe(message("approval.reason_placeholder"));
  expect([...card().querySelectorAll(".btns button")].map((b) => b.textContent)).toEqual([message("common.cancel", {}, "zh-CN"), message("approval.deny")]);
  await click(button(message("common.cancel", {}, "zh-CN"), card()));
  expect(card().querySelector("textarea")).toBeNull();
  await click(button(message("approval.deny"), card()));
  await click(button(message("approval.deny"), card().querySelector(".btns")!));
  expect(card().classList.contains("line")).toBe(true);
  expect(card().textContent).toContain(message("approval.denied"));
  await act(async () => sent.release());
  await vi.waitFor(() => expect(card().textContent).toContain("/etc/hosts"), { timeout: 3000 });
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
  await click(button(message("dock.submit_selected", {"count":1})));
  await vi.waitFor(() => expect(host.textContent).toContain("没能提交"));
  // Change the answer: the error goes and 提交 comes back.
  await click(button(/^按账户/));
  expect(host.textContent).not.toContain("没能提交");
  await click(button(message("dock.submit_selected", {"count":2})));
  await vi.waitFor(() => expect(sent).toHaveLength(2));
  expect(sent[1]).toEqual([1, 2]);
});

it("retries a multi-select answer of unknown outcome unchanged", async () => {
  const r = demo();
  const { sent, slots } = failingAnswers(r, new CommandError("unknown_outcome", "连接超时"));
  mount(<ClaudeAsk r={r} />);
  await click(button(/^按 IP/));
  await click(button(/^按设备/));
  await click(button(message("dock.submit_selected", {"count":2})));
  await vi.waitFor(() => expect(host.textContent).toContain("不确定提交了没有"));
  // The ticks are locked to what was sent; only 重试 remains.
  expect(button(/^按账户/)?.disabled).toBe(true);
  expect(button(/^提交/)).toBeUndefined();
  await click(button(message("common.retry", {}, "zh-CN")));
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
  expect(mini.textContent).toContain(message("list_state.android_request_command"));
  await click(mini.querySelector<HTMLButtonElement>(".pending-sum")!);
  expect(opened).toEqual(["detail:a1"]);
});

it("keeps a question's progress between its page and the dock", async () => {
  const r = demo();
  const q = derive(r.events("s-ask")).questions.get("q1")!;
  mount(<QuestionPage remote={r} sid="s-ask" q={q} agent={Agent.CODEX} title="升级依赖" onBack={() => undefined} onTerminal={() => undefined} />);
  await click(button(/^只升小版本/));
  await vi.waitFor(() => expect(host.textContent).toContain(message("dock.question_progress", {"index":2,"count":2})), { timeout: 3000 });
  act(() => root!.render(<AskDock r={r} />));
  expect(host.textContent).toContain(message("dock.question_progress", {"index":2,"count":2}));
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
  expect(host.textContent).toContain(message("tools.exit_code", {"code":"1"}));
  expect(host.querySelector(".toolout")).toBeNull();
});

function ClaudeQuestionPage({ r, id = "cq" }: { r: DemoRemote; id?: string }) {
  const q = derive(r.events("s-claude")).questions.get(id)!;
  return <QuestionPage remote={r} sid="s-claude" q={q} agent={Agent.CLAUDE} title="t" onBack={() => undefined} onTerminal={() => undefined} />;
}

it("keeps ticks and a pending answer between the dock and the question page", async () => {
  const r = demo();
  const { sent, slots } = failingAnswers(r, new CommandError("unknown_outcome", "连接超时"));
  mount(<ClaudeAsk r={r} />);
  await click(button(/按 IP/));
  // To the page and back: the tick is still there.
  act(() => root!.render(<ClaudeQuestionPage r={r} />));
  expect(button(/按 IP/)?.getAttribute("aria-checked")).toBe("true");
  await click(button(/按设备/));
  act(() => root!.render(<ClaudeAsk r={r} />));
  expect(button(message("dock.submit_selected", {"count":2}))).toBeTruthy();
  await click(button(message("dock.submit_selected", {"count":2})));
  await vi.waitFor(() => expect(host.textContent).toContain("不确定提交了没有"));
  // The unknown outcome follows to the page, and its retry is the same request.
  act(() => root!.render(<ClaudeQuestionPage r={r} />));
  expect(host.textContent).toContain("不确定提交了没有");
  expect(button(/按账户/)?.disabled).toBe(true);
  await click(button(message("common.retry", {}, "zh-CN")));
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
  await click(button(message("dock.submit_selected", {"count":1})));
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
  await click(button(message("markdown.expand", {}, "zh-CN")));
  expect(host.querySelector(".navbar")!.textContent).toContain(message("modes.plan"));
  expect(button(message("dock.plan_clear"))).toBeTruthy();
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
  const input = () => host.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${message("new_session.first_message", {}, "zh-CN")}"]`)!;
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
  await click(button(message("common.retry", {}, "zh-CN")));
  await act(async () => undefined);
  expect(sent).toEqual(["加限流"]);
});

it("never renews a session start of unknown outcome, even once it expired", async () => {
  const r = demo();
  const sent: string[] = [];
  const answers = [new CommandError("unknown_outcome", "连接超时"), new CommandError("expired", "the command expired before it reached the machine")];
  r.machineCommand = async (_m: string, cmd: CommandKind, slot?: RequestSlot): Promise<CommandResult> => {
    if (cmd?.case === "createSession") {
      sent.push(slot!.requestId);
      const e = answers.shift();
      if (e) throw e;
    }
    throw new CommandError("unavailable", "no");
  };
  mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} />);
  const input = () => host.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${message("new_session.first_message", {}, "zh-CN")}"]`)!;
  await act(async () => {
    const set = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
    set.call(input(), "加限流");
    input().dispatchEvent(new Event("input", { bubbles: true }));
  });
  await click(sendButton());
  await vi.waitFor(() => expect(host.textContent).toContain("不确定是否已经启动"));
  await click(sendButton());
  await vi.waitFor(() => expect(sent).toHaveLength(2));
  // Retried as the same request; then the daemon no longer knew it.
  expect(sent[1]).toBe(sent[0]);
  await vi.waitFor(() => expect(host.textContent).toContain("请先看会话列表"));
  expect(sendButton()?.disabled).toBe(true);
  expect(sent).toHaveLength(2);
  // Only giving it up allows a new start, under a new request ID.
  await click(button(message("new_session.abandon_start", {}, "zh-CN")));
  await click(sendButton());
  await vi.waitFor(() => expect(sent).toHaveLength(3));
  expect(sent[2]).not.toBe(sent[0]);
});

it("abandons a frozen unexpired start and gives the next request a new ID", async () => {
  const r = demo();
  const sent: string[] = [];
  r.machineCommand = async (_m: string, cmd: CommandKind, slot?: RequestSlot): Promise<CommandResult> => {
    if (cmd?.case === "createSession") sent.push(slot!.requestId);
    throw new CommandError("unknown_outcome", "连接超时");
  };
  mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} />);
  const input = host.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${message("new_session.first_message", {}, "zh-CN")}"]`)!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, "加限流");
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await click(sendButton());
  await vi.waitFor(() => expect(button(message("new_session.abandon_start", {}, "zh-CN"))).toBeTruthy());
  expect(sendButton()?.disabled).toBe(false);
  await click(button(message("new_session.abandon_start", {}, "zh-CN")));
  await click(sendButton());
  await vi.waitFor(() => expect(sent).toHaveLength(2));
  expect(sent[1]).not.toBe(sent[0]);
});

it("keeps an answer of unknown outcome unknown when its retry comes back expired", async () => {
  const r = demo();
  const slots: string[] = [];
  const answers = [new CommandError("unknown_outcome", "连接超时"), new CommandError("expired", "expired")];
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
  await click(button(message("dock.submit_selected", {"count":1})));
  await vi.waitFor(() => expect(host.textContent).toContain("不确定提交了没有"));
  await click(button(message("common.retry", {}, "zh-CN")));
  await vi.waitFor(() => expect(slots).toHaveLength(2));
  await vi.waitFor(() => expect(host.textContent).toContain("不确定提交了没有"));
  await click(button(message("common.retry", {}, "zh-CN")));
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
  expect(titles).toContain(message("tools.edit_file", {"path":"~/Code/foo bar/a.go"}));
  expect(titles).toContain(message("tools.edit_file", {"path":"sub dir/b.go"}));
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
  expect(row.textContent).toContain(message("tools.exit_code", {"code":"7"}));
  expect(row.textContent).toContain(message("approval.allowed"));
});

it("keeps an answer of unknown outcome unknown when its retry finds the machine offline", async () => {
  const r = demo();
  const slots: string[] = [];
  const answers = [new CommandError("unknown_outcome", "连接超时"), new CommandError("unavailable", "电脑不在线，指令没有发出")];
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
  await click(button(message("dock.submit_selected", {"count":1})));
  await vi.waitFor(() => expect(host.textContent).toContain("不确定提交了没有"));
  await click(button(message("common.retry", {}, "zh-CN")));
  await vi.waitFor(() => expect(slots).toHaveLength(2));
  await vi.waitFor(() => expect(host.textContent).toContain("不确定提交了没有"));
  await click(button(message("common.retry", {}, "zh-CN")));
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
      const o = outcomes.shift() ?? new CommandError("unknown_outcome", "连接超时");
      if (o === "wait") {
        await new Promise<void>((res) => (release = res));
        throw new CommandError("unknown_outcome", "连接超时");
      }
      throw o;
    };
    mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} />);
    const type = async (value: string) =>
      act(async () => {
        const input = host.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${message("new_session.first_message", {}, "zh-CN")}"]`)!;
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
    const t = setup([new CommandError("unknown_outcome", "连接超时"), new CommandError("expired", "expired")]);
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
  const input = () => host.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${message("new_session.first_message", {}, "zh-CN")}"]`)!;
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
        throw new CommandError("unknown_outcome", "连接超时");
      }
      return DemoRemote.prototype.machineCommand.call(r, m, cmd);
    };
    // The project's own machine is offline: this start goes to another.
    r.machines.get("mac")!.online = false;
    mount(page(r));
    await click(button(message("new_session.change_machine", {}, "zh-CN")));
    await click([...document.querySelectorAll<HTMLButtonElement>(".sheet button")].find((b) => b.textContent?.includes("iMac")));
    if (document.querySelector(".sheet")) await click(button(message("common.done", {}, "zh-CN"), document));
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
    expect(host.querySelector(".tok.m")!.textContent).toContain("iMac");
    expect(host.querySelectorAll(".att-tray .pic").length).toBe(1);
    expect(uploads).toEqual(["imac"]);
    const tray = r.trays.get("new:/work/remote-agent") ?? [...r.trays.values()][0];
    await act(async () => { tray.items[0].expiresMs = 0; tray.refreshExpired(); });
    expect(uploads).toEqual(["imac", "imac"]);
    expect(tray.imageIds()).toEqual(["imac-image-2"]);
    await click(sendButton());
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
      throw e ?? new CommandError("unknown_outcome", "连接超时");
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
    await vi.waitFor(() => expect(host.textContent).toContain(message("new_session.unconfirmed_start", {}, "zh-CN")));
    expect(sendButton()?.disabled).toBe(false);
    await click(sendButton());
    await vi.waitFor(() => expect(sent).toHaveLength(2));
    expect(sent[1]).toMatchObject({ prompt: "给登录接口加限流" });
    expect(sent[1].id).not.toBe(sent[0].id);
    // That one's reply is lost too: it is retried as itself.
    await act(async () => answers[1]());
    await vi.waitFor(() => expect(host.textContent).toContain("不确定是否已经启动"));
    await click(sendButton());
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
    await click(button(message("new_session.change_machine", {}, "zh-CN")));
    await click([...document.querySelectorAll<HTMLButtonElement>(".sheet button")].find((b) => b.textContent?.includes("iMac")));
    if (document.querySelector(".sheet")) await click(button(message("common.done", {}, "zh-CN"), document));
    expect(host.querySelector(".tok.m")!.textContent).toContain("iMac");
    // The first session starts, its machine back meanwhile.
    r.machines.get("mac")!.online = true;
    await act(async () => answer());
    await vi.waitFor(() => expect(r.sessions.size).toBe(before + 1));
    // The next message is still there, and still goes where it was told to.
    expect(input().value).toBe("给登录接口加限流");
    expect(host.querySelector(".tok.m")!.textContent).toContain("iMac");
    await vi.waitFor(() => expect(sendButton()?.disabled).toBe(false));
    await click(sendButton());
    await vi.waitFor(() => expect(sent).toHaveLength(2));
    expect(sent[1]).toEqual({ machine: "imac", prompt: "给登录接口加限流" });
    // What went with that one is not held for the message after it.
    await vi.waitFor(() => expect(r.sessions.size).toBe(before + 2));
    await vi.waitFor(() => expect(host.querySelector(".tok.m")!.textContent).not.toContain("iMac"));
  });
});


it("keeps a project's model and effort after the page remounts", async () => {
  const project = "/Users/me/Code/ai-bot/remote-agent";
  const page = (r: DemoRemote) => <WideHome remote={r} go={() => undefined} project={project} />;
  const r = demo();
  mount(page(r));
  await act(async () => { await Promise.all([r.listModels("mac", Agent.CODEX), r.settings("mac")]); });
  await fromMenu("新会话设置");
  await vi.waitFor(() => expect(button("GPT-6-Sol", document)).toBeDefined());
  await click(button("GPT-6-Sol", document));
  await click(button("low", document));
  // Finish directly on the model page, without returning to project settings.
  await click(button(message("common.done", {}, "zh-CN"), document));
  expect(document.querySelector(":is(.sheet, .composer-picker)")).toBeNull();
  await vi.waitFor(() => expect(savedSet(r, project)).toMatchObject({ model: "gpt-6-sol", effort: "low" }), { timeout: 3000 });
  // Rebuild the page with the saved account preference.
  act(() => root!.unmount());
  host.remove();
  const reloaded = r;
  mount(page(reloaded));
  await act(async () => { await Promise.all([reloaded.listModels("mac", Agent.CODEX), reloaded.settings("mac")]); });
  await fromMenu("新会话设置");
  // The sheet that was closed may still be leaving: look in the last one.
  const sheet = () => [...document.querySelectorAll<HTMLElement>(":is(.sheet, .composer-picker)")].at(-1)!;
  await vi.waitFor(() => expect(button("GPT-6-Sol", sheet())?.querySelector(".ck")).toBeTruthy());
  expect(button("low", sheet())?.className).toBe("on");
  await click(button(message("common.done", {}, "zh-CN"), document));
  const real = reloaded.machineCommand.bind(reloaded);
  let sent: unknown;
  reloaded.machineCommand = async (machine, cmd) => {
    if (cmd?.case === "createSession") sent = cmd.value;
    return real(machine, cmd);
  };
  // An empty message is not sent; the first message starts the session.
  const go = () => host.querySelector<HTMLButtonElement>(`button[aria-label="${message("new_session.start")}"]`)!;
  expect(go().disabled).toBe(true);
  await act(async () => {
    const input = host.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${message("new_session.first_message", {}, "zh-CN")}"]`)!;
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, "看一下");
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await click(go());
  await act(async () => { await vi.waitFor(() => expect(loadProjectConfig(reloaded.account, project)).toBeUndefined()); });
  expect(sent).toMatchObject({ model: "gpt-6-sol", effort: "low", cwd: project });
});

it("keeps project settings by path, and never stores or reads an approval mode there", async () => {
  const r = demo();
  let ns: ReturnType<typeof useNewSession>;
  function Box({ remote = r, project }: { remote?: DemoRemote; project: string }) {
    ns = useNewSession(remote, project, () => undefined);
    return null;
  }
  const foo = "/Users/me/Code/foo";
  const shop = "/Users/me/Code/shop";
  mount(<Box project={foo} />);
  // A mode that comes along (a scheduled task's choice) is held by the page but not stored.
  act(() => ns.setCfg({ ...ns.cfg, model: "gpt-6-luna", effort: "max", mode: ApprovalMode.FULL_ACCESS }));
  expect(ns!.cfg.mode).toBe(ApprovalMode.FULL_ACCESS);
  await vi.waitFor(() => expect(savedSet(r, foo)?.model).toBe("gpt-6-luna"));
  expect(savedSet(r, foo)?.mode).toBe(ApprovalMode.UNSPECIFIED);
  act(() => root!.render(<Box project={shop} />));
  expect(ns!.cfg.model).not.toBe("gpt-6-luna");
  act(() => root!.render(<Box key="reload" project={foo} />));
  expect(ns!.cfg).toMatchObject({ model: "gpt-6-luna", effort: "max", mode: ApprovalMode.UNSPECIFIED });
  // A mode stored by an earlier version is ignored.
  savedSet(r, foo)!.mode = ApprovalMode.FULL_ACCESS;
  act(() => root!.render(<Box key="old" project={foo} />));
  expect(ns!.cfg).toMatchObject({ model: "gpt-6-luna", mode: ApprovalMode.UNSPECIFIED });
});

it("keeps the default model while saving a chosen reasoning effort", async () => {
  const r = demo();
  const project = "/Users/me/Code/foo";
  mount(<WideHome remote={r} go={() => undefined} project={project} />);
  await fromMenu("新会话设置");
  await click(button(/^默认[（模型]/, document));
  await vi.waitFor(() => expect(button("high", document)).toBeDefined());
  await click(button("high", document));
  expect(document.querySelector(".mi .ck svg")).toBeTruthy();
  await click(button(message("common.done", {}, "zh-CN"), document));
  await vi.waitFor(() => expect(savedSet(r, project)).toMatchObject({ model: "", effort: "high" }));
  await fromMenu("新会话设置");
  expect(document.querySelector(".mi .ck svg")).toBeTruthy();
  await vi.waitFor(() => expect(button("high", [...document.querySelectorAll<HTMLElement>(":is(.sheet, .composer-picker)")].at(-1)!)?.className).toBe("on"));
});

it("keeps the effort in sight under a long model list", async () => {
  const r = demo();
  const real = r.listModels.bind(r);
  r.listModels = async (...a: Parameters<DemoRemote["listModels"]>) => {
    const got = await real(...a);
    const extra = Array.from({ length: 8 }, (_, i) => ({ ...got.models[0], slug: `old-${i}`, displayName: `Old ${i}` }));
    return { ...got, models: [...got.models, ...extra] };
  };
  const project = "/Users/me/Code/foo";
  mount(<WideHome remote={r} go={() => undefined} project={project} />);
  await fromMenu("新会话设置");
  await vi.waitFor(() => expect(button(new RegExp("^" + message("models.all")), document)).toBeTruthy(), { timeout: 3000 });
  // The default row, three models and the link: the effort follows right after.
  expect(document.querySelectorAll(".list .mi")).toHaveLength(5);
  expect(button("high", document)).toBeTruthy();
  await click(button(new RegExp("^" + message("models.all")), document));
  expect(button("Old 7", document)).toBeTruthy();
  await click(button("Old 7", document));
  await click(button(message("common.done", {}, "zh-CN"), document));
  await fromMenu("新会话设置");
  // Collapsed again, the picked model is still listed and checked.
  const sheet = () => [...document.querySelectorAll<HTMLElement>(":is(.sheet, .composer-picker)")].at(-1)!;
  await vi.waitFor(() => expect(button(new RegExp("^" + message("models.all")), sheet())).toBeTruthy());
  expect(button("Old 7", sheet())?.querySelector(".ck")).toBeTruthy();
});

it("names a new project's default model as Codex does, even when /model hides it", async () => {
  const r = demo();
  // The machine's default is a model Codex leaves out of its list.
  (r as unknown as { saved: unknown }).saved = create(DaemonSettingsSchema, { approvalTimeoutSeconds: 600, defaultModel: "gpt-6.1-sol", defaultEffort: "low" });
  r.draftMachines.set("/Users/me/Code/new", "mac");
  mount(<ProjectPage remote={r} pkey="/Users/me/Code/new" go={() => undefined} onBack={() => undefined} />);
  await fromMenu("新会话设置");
  await vi.waitFor(() => expect(host.querySelector(".project-summary")?.textContent).toContain("GPT-6.1-Sol"), { timeout: 3000 });
  expect(document.querySelector(".mi")?.textContent).toContain(message("models.default_named", {"model":"GPT-6.1-Sol"}));
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
  await vi.waitFor(() => expect(button(message("projects.use_directory", {"name":"Code"}), document)).toBeTruthy());
  await click(button(message("projects.use_directory", {"name":"Code"}), document));
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
  expect(host.querySelector(".tok.p > span:not(.av)")?.textContent).toBe("Code");
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
  await vi.waitFor(() => expect(button(message("projects.use_directory", {"name":"foo"}), document)).toBeTruthy());
  await click(button(message("projects.use_directory", {"name":"foo"}), document));
  await act(async () => { await vi.waitFor(() => expect(r.prefs.pending.size).toBe(0)); });
  expect(r.projectPrefs().settings.find((s) => s.path === foo)).toEqual(before);
});

it("shows no approval mode in a new task's summary or settings", async () => {
  const r = demo();
  // Codex reports its effective mode, with no explicit new-project override.
  (r as unknown as { saved: unknown }).saved = create(DaemonSettingsSchema, { defaultMode: ApprovalMode.UNSPECIFIED });
  r.draftMachines.set("/Users/me/Code/new", "mac");
  mount(<ProjectPage remote={r} pkey="/Users/me/Code/new" go={() => undefined} onBack={() => undefined} />);
  await vi.waitFor(() => expect(host.querySelector(".project-summary")?.textContent).toMatch(/^GPT-6-Astra · 默认$/));
  expect(host.querySelector(".project-summary")?.textContent).not.toMatch(/自动|询问|完全放行|默认：|审批/);
  await fromMenu("新会话设置");
  expect(document.querySelector(":is(.sheet, .composer-picker)")!.textContent).not.toMatch(/审批|完全放行|移除项目|设为常用/);
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
  expect(host.textContent).toContain(message("dock.trust_folder"));
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
  const line = host.querySelector(".tool.now")!;
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
    expect(host.querySelector(".me.fail small")!.textContent).toBe(message("composer.send_failure", {"reason":"会话已结束放回输入框"}));
    expect(host.textContent).not.toContain(message("queue.canceled_record"));
    expect(host.querySelector(".queued")).toBeNull();
    await click(button(message("composer.restore")));
    expect(host.querySelector<HTMLTextAreaElement>("textarea[aria-label=消息]")!.value).toBe("顺便补一个测试");
  });

  it("waits as the draft while the session is still ended", async () => {
    const r = setup(true);
    expect(host.textContent).toContain(message("session.ended_send_failed"));
    expect(host.querySelector(".queued")).toBeNull();
    await click(button(message("composer.restore")));
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
  expect(lines).toContain(message("session.model_changed", {"value":"Sonnet 5.5 · medium"}));
  expect(lines.some((l) => l?.includes(message("session.terminal_model_change", {"value":"Opus 5.5 · medium"})))).toBe(true);
});

describe("a new-session draft", () => {
  const type = async (value: string) =>
    act(async () => {
      const input = host.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${message("new_session.first_message", {}, "zh-CN")}"]`)!;
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
    await vi.waitFor(() => expect(host.querySelector(".wstart .sent")).not.toBeNull());
    const before = startProject();
    await type("加限流");
    // Another project gets a newer session meanwhile.
    act(() => { r.sessions.get("s-plan")!.info!.createdAt = timestampFromMs(Date.now() + 172_800_000); });
    bump(r, "s-plan");
    expect(startProject()).toBe(before);
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
    expect(setting(message("new_session.cli_picker_label", {}, "zh-CN"))).not.toContain("Claude Code");
    await click(button(message("common.done", {}, "zh-CN"), document));
    await vi.waitFor(() => expect(sendButton()?.disabled).toBe(false));
    await click(sendButton());
    await vi.waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0].agent).toBe(Agent.CODEX);
  });

  it("leaves the mode to the machine's default, full access included, and says none", async () => {
    const r = demo();
    const sent = capture(r);
    // The machine reports Codex's default as full access.
    r.machines.get("mac")!.meta!.agents[0].defaultMode = ApprovalMode.FULL_ACCESS;
    r.draftMachines.set("/Users/me/Code/new", "mac");
    mount(<ProjectPage remote={r} pkey="/Users/me/Code/new" go={() => undefined} onBack={() => undefined} />);
    await vi.waitFor(() => expect(host.querySelector(".project-summary")?.textContent).toMatch(/^GPT-6-Astra · 默认$/), { timeout: 3000 });
    expect(host.querySelector(".project-summary")?.textContent).not.toContain(message("modes.full_access"));
    expect(host.querySelector(".project-summary em")).toBeNull();
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
  const card = () => host.querySelector(".tc")!;

  it("locks both choices and retries only the one made, as the same request", async () => {
    const calls = setup(new CommandError("unknown_outcome", "连接超时"));
    await click(button(message("approval.allow"), card()));
    await vi.waitFor(() => expect(card().textContent).toContain("没送到电脑"), { timeout: 3000 });
    expect(button(message("approval.deny"), card())).toBeUndefined();
    expect(button(message("approval.allow"), card())).toBeUndefined();
    await click(button(message("common.retry", {}, "zh-CN"), card()));
    await vi.waitFor(() => expect(calls.length).toBe(2));
    expect(calls[1]).toEqual(calls[0]);
    expect(calls[1].allow).toBe(true);
  });

  it("keeps a definite failure locked to retrying the same answer", async () => {
    setup(new CommandError("unavailable", "电脑不在线，指令没有发出"));
    await click(button(message("approval.allow"), card()));
    await vi.waitFor(() => expect(card().textContent).toContain("没送到电脑"), { timeout: 3000 });
    expect(button(message("approval.deny"), card())).toBeUndefined();
    expect(button(message("approval.allow"), card())).toBeUndefined();
    expect(button(message("common.retry", {}, "zh-CN"), card())).toBeTruthy();
  });
});

describe("pending bubbles in the chat (v8)", () => {
  it("says what became of a cancel", async () => {
    const { cancelOutcome } = await import("./Queue");
    expect(cancelOutcome(InputStatus.CANCELED, true)).toBe(message("queue.canceled"));
    expect(cancelOutcome(InputStatus.CANCELED, false)).toBe(message("queue.canceled_elsewhere"));
    expect(cancelOutcome(InputStatus.SUBMITTED, true)).toBe(message("queue.already_submitted"));
    expect(cancelOutcome(InputStatus.UNCONFIRMED, true)).toBe(message("queue.already_submitted"));
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
    expect(entries()[0].textContent).toContain(message("queue.insert"));
    expect(entries()[1].textContent).toContain(message("session.origin_other"));
    await click(button(message("common.cancel", {}, "zh-CN"), entries()[0]));
    expect(entries()[0].textContent).toContain(message("queue.canceling"));
    await act(async () => cancel.release());
    await vi.waitFor(() => expect(entries().length).toBe(1), { timeout: 3000 });
    expect(host.textContent).toContain(message("queue.canceled_record"));
  });

  it("keeps a message whose cancel failed, and says so", async () => {
    const r = demo();
    const s = r.sessions.get("s-run")!;
    r.command = async () => {
      throw new CommandError("unavailable", "电脑不在线，指令没有发出");
    };
    mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
    await click(button(message("common.cancel", {}, "zh-CN"), host.querySelector(".pending-bubble")!));
    await vi.waitFor(() => expect(host.querySelector(".pending-bubble")!.textContent).toContain(message("queue.cancel_failed")));
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
      const input = host.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${message("new_session.first_message", {}, "zh-CN")}"]`)!;
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, "加限流");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    // The CLI is picked in the sentence; project settings have no CLI row.
    await pickCli("Claude Code");
    expect(cliPicked()).toBe("Claude Code");
    expect(host.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${message("new_session.first_message", {}, "zh-CN")}"]`)!.value).toBe("加限流");
    // Nothing is stored for Claude Code here: the project's latest Claude Code session says which model it starts with, not Codex's set.
    await vi.waitFor(() => expect(host.querySelector(".project-summary")?.textContent).toMatch(/^Sonnet/), { timeout: 3000 });
    await fromMenu("新会话设置");
    expect(setting(message("new_session.cli_picker_label", {}, "zh-CN"))).toBe("");
    await click(button(message("common.done", {}, "zh-CN"), document));
    await click(sendButton());
    await vi.waitFor(() => expect(sent).toHaveLength(1));
    // The approval mode that session ran in is not carried over: the machine's default applies.
    expect(sent[0]).toMatchObject({ agent: Agent.CLAUDE, mode: ApprovalMode.UNSPECIFIED, prompt: "加限流" });
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
    await pickCli("Claude Code");
    await act(async () => {
      const input = host.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${message("new_session.first_message", {}, "zh-CN")}"]`)!;
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
    expect(savedSet(r, foo, Agent.CLAUDE)).toMatchObject({ mode: ApprovalMode.UNSPECIFIED });
    act(() => root!.unmount());
    host.remove();
    mount(page());
    expect(cliPicked()).toBe("Claude Code");
    // Back on Codex, its stored model is what starts.
    await pickCli("Codex");
    expect(cliPicked()).toBe("Codex");
    await vi.waitFor(() => expect(host.querySelector(".project-summary")?.textContent).toMatch(/^GPT-6-Astra/));
  });

  it("opens the settings of the CLI picked next to the box, and of no other", async () => {
    const r = demo();
    const foo = "/Users/me/Code/foo";
    mount(<ProjectPage remote={r} pkey={foo} go={() => undefined} onBack={() => undefined} />);
    await vi.waitFor(() => expect(r.prefs.status).toBe("ready"));
    expect(cliPicked()).toBe("Codex");
    await fromMenu("新会话设置");
    // No tabs: the sheet is the model page of the one CLI the sentence names.
    expect(document.querySelector(".cli-tabs")).toBeNull();
    expect(document.querySelector(".sheet-head")?.textContent).toContain(message("models.title", {}, "zh-CN"));
    expect(document.querySelector(":is(.sheet, .composer-picker)")!.textContent).not.toContain(message("approval.title"));
    await click(button("GPT-6-Luna", document));
    await click(button(message("common.done", {}, "zh-CN"), document));
    // The model is saved; the project's mode is not touched by it.
    await vi.waitFor(() => expect(savedSet(r, foo)).toMatchObject({ model: "gpt-6-luna", mode: ApprovalMode.UNSPECIFIED }), { timeout: 3000 });
    expect(r.projectPrefs().settings.find((s) => s.path === foo)?.agent).toBe(Agent.CODEX);
  });

  it("shows no switch on a machine with one CLI, and says which CLI runs when the remembered one is missing", async () => {
    const r = demo();
    const foo = "/Users/me/Code/foo";
    await vi.waitFor(() => expect(r.prefs.status).toBe("ready"));
    await r.changePrefs("settings", foo, { id: "t1", kind: "settings", path: foo, at: Date.now(), settings: { machineId: "vps", agent: Agent.CLAUDE } });
    mount(<ProjectPage remote={r} pkey={foo} go={() => undefined} onBack={() => undefined} />);
    expect(host.querySelector(".tok.cli")).toBeNull();
    expect(host.textContent).toContain(message("new_session.cli_fallback", {"name":"VPS-dev","missing":"Claude Code","agent":"Codex"}, "zh-CN"));
    expect(host.querySelector(".project-summary")?.textContent).toMatch(/^Codex · /);
    // The project still remembers Claude Code.
    expect(r.projectPrefs().settings.find((s) => s.path === foo)?.agent).toBe(Agent.CLAUDE);
    await fromMenu("新会话设置");
    expect(document.querySelector(".cli-tabs")).toBeNull();
  });

  it("puts the sentence above the task and the summary under it on a wide page", async () => {
    const r = demo();
    mount(<WideHome remote={r} go={() => undefined} project="/Users/me/Code/foo" />);
    // The project is the page's own, so the sentence names it as words.
    expect(host.querySelector(".wstart .sent")?.textContent).toBe(message("new_session.location", {"project":"MacBook的foo用Codex"}));
    expect(host.querySelector(".wstart .sent .tok.p")).toBeNull();
    const ask = host.querySelector(".wstart textarea.ask")!;
    expect(ask.getAttribute("placeholder")).toBe(message("new_session.prompt", {}, "zh-CN"));
    // The task is written in a box of its own, under the sentence.
    expect(ask.closest(".wbox")?.previousElementSibling?.className).toBe("sent");
    const row = host.querySelector(".wstart .acc")!;
    expect(row.querySelector(".project-summary")?.textContent).toMatch(/^gpt-6-astra$/i);
    expect(row.querySelector(`button[aria-label="${message("new_session.start")}"]`)).not.toBeNull();
  });

  it("shows what was read before at once when coming back to a CLI", async () => {
    const r = demo();
    mount(<WideHome remote={r} go={() => undefined} project="/Users/me/Code/foo" />);
    const text = () => host.querySelector(".acc .project-summary")!.textContent;
    await vi.waitFor(() => expect(text()).toMatch(/GPT-6-Astra · medium$/));
    await pickCli("Claude Code");
    await vi.waitFor(() => expect(text()).not.toMatch(/GPT|读取/));
    const claude = text();
    // No 读取设置… and no raw model id on the way back, in either direction.
    pickCliNow("Codex");
    expect(text()).toMatch(/GPT-6-Astra · medium$/);
    pickCliNow("Claude Code");
    expect(text()).toBe(claude);
  });

  it("names the machine in the sentence only when there is more than one", async () => {
    const r = demo();
    mount(<WideHome remote={r} go={() => undefined} project="/Users/me/Code/foo" />);
    const sentence = () => host.querySelector(".wstart .sent")!.textContent;
    const summary = () => host.querySelector(".acc .project-summary")!;
    expect(host.querySelector(".tok.m")?.getAttribute("aria-label")).toBe(message("new_session.machine_label", {"name":"MacBook"}));
    expect(sentence()).toBe(message("new_session.location", {"project":"MacBook的foo用Codex"}));
    act(() => {
      for (const id of [...r.machines.keys()]) if (id !== "mac") r.machines.delete(id);
      (r as unknown as { changed(): void }).changed();
    });
    expect(host.querySelector(".tok.m")).toBeNull();
    expect(sentence()).toBe(message("new_session.location", {"project":"foo用Codex"}));
    // Before the model list arrives, no effort choice is known yet.
    expect(summary().textContent).toMatch(/^gpt-6-astra$/i);
    expect(summary().getAttribute("aria-label")).toBe(message("models.summary_label", { model: summary().textContent ?? "" }, "zh-CN"));
  });
});


describe("a new task's settings have no approval mode (v2)", () => {
  /** Leaves one computer on the account. */
  const alone = (r: DemoRemote) => {
    for (const id of [...r.machines.keys()]) if (id !== "mac") r.machines.delete(id);
  };

  it("opens straight on the model page when the account has one computer", async () => {
    const r = demo();
    alone(r);
    mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} />);
    await fromMenu("新会话设置");
    // No list of one row first: no 机器, no 审批, no project block, no note.
    const sheet = document.querySelector(":is(.sheet, .composer-picker)")!;
    expect(sheet.querySelector(".sheet-head")?.textContent).toContain(message("models.title", {}, "zh-CN"));
    expect(sheet.querySelector(`.sheet-head button[aria-label="${message("common.back")}"]`)).toBeNull();
    expect(sheet.textContent).toContain("默认（");
    expect(sheet.textContent).not.toMatch(/电脑|审批|移除项目|常用|只影响之后新建/);
    // Choosing a model stores it, and 完成 closes the sheet.
    await click(button("GPT-6-Luna", document));
    await vi.waitFor(() => expect(savedSet(r, "/Users/me/Code/foo")).toMatchObject({ model: "gpt-6-luna" }), { timeout: 3000 });
    await click(button(message("common.done", {}, "zh-CN"), document));
    expect(document.querySelector(":is(.sheet, .composer-picker)")).toBeNull();
  });

  it("opens on the model page with several computers too: the machine is picked in the sentence", async () => {
    const r = demo();
    mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} />);
    await fromMenu("新会话设置");
    expect(document.querySelector(":is(.sheet, .composer-picker) .sheet-head")?.textContent).toContain(message("models.title", {}, "zh-CN"));
    expect(document.querySelector(":is(.sheet, .composer-picker)")!.textContent).not.toMatch(/电脑|审批|移除项目|常用|只影响之后新建/);
    await click(button(message("common.done", {}, "zh-CN"), document));
    // The machine chip opens the list of machines, and picking one closes it.
    await click(host.querySelector<HTMLButtonElement>(".tok.m")!);
    expect(document.querySelector(".sheet .sheet-head")?.textContent).toContain(message("common.machine", {}, "zh-CN"));
    await click(button(/VPS-dev/, document));
    expect(document.querySelector(":is(.sheet, .composer-picker)")).toBeNull();
    expect(host.querySelector(".tok.m")!.textContent).toContain("VPS-dev");
  });

  it("puts Claude Code's context window on the model page", async () => {
    const r = demo();
    alone(r);
    mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} />);
    await pickCli("Claude Code");
    await fromMenu("新会话设置");
    await vi.waitFor(() => expect(document.querySelector(`:is(.sheet, .composer-picker) [role="radiogroup"][aria-label="${message("models.context_window")}"], :is(.sheet, .composer-picker) [aria-label="${message("models.context_window")}"]`)).not.toBeNull());
    await click(button(message("models.standard_window", {}, "zh-CN"), document));
    await vi.waitFor(() => expect(savedSet(r, "/Users/me/Code/foo", Agent.CLAUDE)).toMatchObject({ standardContext: true }), { timeout: 3000 });
  });

  it("sends the new task without a mode, whatever mode the project's last session ran in", async () => {
    const r = demo();
    const sent: { mode?: number }[] = [];
    r.machineCommand = async (m: string, cmd: CommandKind): Promise<CommandResult> => {
      if (cmd?.case === "createSession") {
        sent.push({ mode: cmd.value.mode });
        throw new CommandError("failed_precondition", "stop here");
      }
      return DemoRemote.prototype.machineCommand.call(r, m, cmd);
    };
    // The project's latest Codex session ran in full access, and an old stored set says so too.
    for (const s of r.sessions.values()) if (s.info?.cwd === "/Users/me/Code/foo") s.info.mode = ApprovalMode.FULL_ACCESS;
    savedSet(r, "/Users/me/Code/foo")!.mode = ApprovalMode.FULL_ACCESS;
    mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} />);
    expect(host.querySelector(".project-summary")?.textContent).not.toContain(message("modes.full_access"));
    await act(async () => {
      const input = host.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${message("new_session.first_message", {}, "zh-CN")}"]`)!;
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, "加限流");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await click(sendButton());
    await vi.waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0].mode).toBe(ApprovalMode.UNSPECIFIED);
  });

  it("asks for another model, not another mode, when the machine's default is auto and the model has none", async () => {
    const r = demo();
    alone(r);
    // Claude Code on the machine defaults to auto; its Sonnet has no auto mode.
    const claude = r.machines.get("mac")!.meta!.agents.find((a) => a.agent === Agent.CLAUDE)!;
    claude.defaultMode = ApprovalMode.AUTO;
    claude.defaultModeSource = 2;
    r.draftMachines.set("/Users/me/Code/new", "mac");
    const real = r.machineCommand.bind(r);
    r.machineCommand = async (m: string, cmd: CommandKind): Promise<CommandResult> => {
      if (cmd?.case === "listModels" && cmd.value.agent === Agent.CLAUDE)
        return create(CommandResultSchema, { kind: { case: "listModels", value: { defaultModel: "claude-sonnet-5-5", models: [
          { slug: "claude-sonnet-5-5", displayName: "Sonnet 5.5", efforts: [], autoUnavailable: true },
          { slug: "claude-opus-5-5", displayName: "Opus 5.5", efforts: [] },
        ] } } });
      return real(m, cmd);
    };
    await act(async () => { await r.listModels("mac", Agent.CLAUDE, true); });
    mount(<ProjectPage remote={r} pkey="/Users/me/Code/new" go={() => undefined} onBack={() => undefined} />);
    await pickCli("Claude Code");
    await vi.waitFor(() => expect(host.textContent).toContain("这个模型没有自动模式"), { timeout: 3000 });
    expect(host.textContent).not.toContain("改模式");
    expect(button(message("new_session.change_model", {}, "zh-CN"))).toBeDefined();
    expect(sendButton()?.disabled).toBe(true);
    await click(button(message("new_session.change_model", {}, "zh-CN")));
    expect(document.querySelector(":is(.sheet, .composer-picker) .sheet-head")?.textContent).toContain(message("models.title", {}, "zh-CN"));
    await click(button("Opus 5.5", document));
    await click(button(message("common.done", {}, "zh-CN"), document));
    await vi.waitFor(() => expect(host.textContent).not.toContain("这个模型没有自动模式"));
  });

  it("points to the machine's default settings when the account has no auto mode at all", async () => {
    const r = demo();
    alone(r);
    const claude = r.machines.get("mac")!.meta!.agents.find((a) => a.agent === Agent.CLAUDE)!;
    claude.defaultMode = ApprovalMode.AUTO;
    claude.defaultModeSource = 2;
    r.draftMachines.set("/Users/me/Code/new", "mac");
    const real = r.machineCommand.bind(r);
    r.machineCommand = async (m: string, cmd: CommandKind): Promise<CommandResult> => {
      if (cmd?.case === "listModels" && cmd.value.agent === Agent.CLAUDE)
        return create(CommandResultSchema, { kind: { case: "listModels", value: { defaultModel: "claude-sonnet-5-5", models: [{ slug: "claude-sonnet-5-5", displayName: "Sonnet 5.5", efforts: [], autoUnavailable: true }] } } });
      return real(m, cmd);
    };
    await act(async () => { await r.listModels("mac", Agent.CLAUDE, true); });
    const went: string[] = [];
    mount(<ProjectPage remote={r} pkey="/Users/me/Code/new" go={(to) => went.push(to)} onBack={() => undefined} />);
    await pickCli("Claude Code");
    await vi.waitFor(() => expect(host.textContent).toContain(message("modes.account_unsupported")), { timeout: 3000 });
    expect(host.textContent).toContain("设置 › 这台电脑");
    await click(button(message("new_session.change_defaults", {}, "zh-CN")));
    expect(went).toEqual(["m/mac"]);
  });
});

describe("project management (v2)", () => {
  const names = (sel: string) => [...host.querySelectorAll(sel)].map((e) => e.firstChild?.textContent ?? "");

  it("removes a project only after asking, and keeps its waiting sessions", async () => {
    const r = demo();
    mount(<ManagePage remote={r} go={() => undefined} onBack={() => undefined} />);
    await vi.waitFor(() => expect(r.prefs.status).toBe("ready"));
    await click(host.querySelector<HTMLButtonElement>('[aria-label="管理 shop"]')!);
    await click(button(message("projects.remove_action", {}, "zh-CN")));
    // Nothing is removed until the dialog is confirmed.
    expect(host.querySelector(".dialog")?.textContent).toContain(message("projects.remove_title", {"name":"shop"}));
    await click(button(message("common.cancel", {}, "zh-CN")));
    expect(names(".plist .pi b")).toContain("shop");
    await click(host.querySelector<HTMLButtonElement>('[aria-label="管理 shop"]')!);
    await click(button(message("projects.remove_action", {}, "zh-CN")));
    await click(button(message("common.remove", {}, "zh-CN")));
    await vi.waitFor(() => expect(names(".plist .pi b")).not.toContain("shop"), { timeout: 3000 });
    // No undo and no list of removed projects: adding the folder again is the way back.
    expect(host.textContent).not.toContain("撤销");
    expect(host.textContent).not.toContain("已移除");
    // Home leaves it out of the projects, but its plan still waits for the user.
    act(() => root!.render(<Home remote={r} go={() => undefined} />));
    await click(host.querySelector<HTMLButtonElement>(".tbar .scope")!);
    expect(names(".plist .pi b")).not.toContain("shop");
    expect(names(".plist .pi b")).toContain("foo");
    expect(todoList(r).map((x) => x.id)).toContain("s-plan");
  });

  it("lists a removed project again when its folder is added as a new project", async () => {
    const r = demo();
    await vi.waitFor(() => expect(r.prefs.status).toBe("ready"));
    const shop = "/Users/me/Code/shop";
    await r.changePrefs("hide", shop);
    mount(<ManagePage remote={r} go={() => undefined} onBack={() => undefined} />);
    expect(names(".plist .pi b")).not.toContain("shop");
    await click(button(/新项目…/));
    for (const dir of ["Code", "shop"]) {
      await vi.waitFor(() => expect(button(dir, document)).toBeTruthy());
      await click(button(dir, document));
    }
    await vi.waitFor(() => expect(button(message("projects.use_directory", {"name":"shop"}), document)).toBeTruthy());
    await click(button(message("projects.use_directory", {"name":"shop"}), document));
    await vi.waitFor(() => expect(names(".plist .pi b")).toContain("shop"), { timeout: 3000 });
    expect(r.projectPrefs().hidden.map((h) => h.path)).not.toContain(shop);
  });

  it("lists projects the same way in the scope menu, the picker and 项目管理", async () => {
    const r = demo();
    await vi.waitFor(() => expect(r.prefs.status).toBe("ready"));
    const { ProjectPicker } = await import("./NewSession");
    const seen = () => [...host.querySelectorAll(".plist .fl, .plist .pi b, .plist .pi small, .plist .pi.add")].map((e) => e.textContent).filter((t) => t !== message("home.all_projects") && !/^(\d+ 个待处理|\d+ 个进行中)( · \d+ 个进行中)?$/.test(t ?? ""));
    mount(<ManagePage remote={r} go={() => undefined} onBack={() => undefined} />);
    const managed = seen();
    // Counted as the home's sections are: waiting, then running.
    expect(managed.slice(0, 3)).toEqual([message("projects.pinned", {}, "zh-CN"), "foo", "~/Code/foo · 2 个待处理 · 4 个进行中"]);
    expect(managed[managed.length - 1]).toBe(" 新项目…");
    act(() => root!.render(<ProjectPicker remote={r} onPick={() => undefined} onNew={() => undefined} onClose={() => undefined} />));
    expect(seen()).toEqual(managed);
    act(() => root!.render(<Home remote={r} go={() => undefined} />));
    await click(host.querySelector<HTMLButtonElement>(".tbar .scope")!);
    expect(seen()).toEqual(managed);
    // 全部项目 counts the same way, over every project.
    const all = [...host.querySelectorAll(".plist .pi")].find((x) => x.querySelector("b")?.textContent === message("home.all_projects"))!;
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
    expect(host.textContent).toContain(message("projects.removed", {}, "zh-CN"));
    // The draft can be written, but nothing starts until the project is listed again.
    await act(async () => {
      const input = host.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${message("new_session.first_message", {}, "zh-CN")}"]`)!;
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, "加限流");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(sendButton()?.disabled).toBe(true);
    // Its sessions are all still there.
    expect([...r.sessions.values()].some((s) => s.info?.title === "拆分订单服务")).toBe(true);
    await click(button(message("projects.add_again", {}, "zh-CN")));
    await vi.waitFor(() => expect(sendButton()?.disabled).toBe(false), { timeout: 3000 });
  });

  it("leaves removing and pinning to the project lists, not a new task's settings", async () => {
    const r = createDemo(new URLSearchParams("project-settings=1"));
    r.start();
    await vi.waitFor(() => expect(r.prefs.status).toBe("ready"));
    mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} />);
    expect(document.querySelector(":is(.sheet, .composer-picker)")).not.toBeNull();
    expect(button(message("projects.remove_action", {}, "zh-CN"))).toBeUndefined();
    expect(button(/^(设为|取消)常用$/)).toBeUndefined();
  });

  it("shows a failed save as not done, and retries the same operation", async () => {
    const retry = barrier();
    let retrying = false;
    const r = createDemo(new URLSearchParams("prefs=fail"), async () => {
      if (retrying) await retry.wait;
    });
    r.start();
    await vi.waitFor(() => expect(r.prefs.status).toBe("ready"));
    mount(<ManagePage remote={r} go={() => undefined} onBack={() => undefined} />);
    await click(host.querySelector<HTMLButtonElement>('[aria-label="管理 bar"]')!);
    await click(button(message("projects.pin", {}, "zh-CN")));
    await vi.waitFor(() => expect(host.textContent).toContain("没能保存，列表保持原样"), { timeout: 5000 });
    expect(names(".plist .pi b")[0]).toBe("foo");
    expect(host.querySelectorAll(".plist .fl").length).toBe(2);
    const id = r.prefs.failed!.op.id;
    retrying = true;
    await click(button(message("common.retry", {}, "zh-CN")));
    await vi.waitFor(() => expect(r.prefs.pending.size).toBe(1));
    await act(async () => retry.release());
    await vi.waitFor(() => expect(r.prefs.failed?.op.id).toBe(id), { timeout: 5000 });
  }, 15_000);

  it("pauses changes while disconnected and says when they can be made", async () => {
    const r = createDemo(new URLSearchParams("conn=reconnecting"));
    r.start();
    await vi.waitFor(() => expect(r.prefs.status).toBe("ready"));
    mount(<ManagePage remote={r} go={() => undefined} onBack={() => undefined} />);
    await click(host.querySelector<HTMLButtonElement>('[aria-label="管理 bar"]')!);
    expect(button(/设为常用/)!.disabled).toBe(true);
    expect(button(/设为常用/)!.textContent).toContain(message("projects.connect_to_edit", {}, "zh-CN"));
    expect(button(message("projects.remove_action", {}, "zh-CN"))!.disabled).toBe(true);
  });
});

describe("who did what (v2)", () => {
  it("names another client or the computer's terminal, never this client", () => {
    const r = demo();
    const s = r.sessions.get("s-multi")!;
    return (async () => {
      await vi.waitFor(() => expect(r.devicesStatus).toBe("ready"));
      mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
      const notes = [...host.querySelectorAll(".me:not(.pending-bubble) small")].map((x) => x.textContent);
      // The first message is this client's own: it carries no name.
      expect(notes).toEqual(["Android · Pixel 9", message("session.origin_terminal", {"name":"VPS-dev"})]);
      // A client that says nothing of itself, with no ID in its name.
      expect(host.textContent).toContain(message("session.mode_changed_by", {"origin":message("security.client"),"value":message("modes.plan")}));
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
    expect(notes).toContain(message("session.model_changed_by", {"origin":"Android · Pixel 9","value":"other-model"}));
    expect(notes).toContain(message("session.model_changed", {"value":"my-model"}));
  });

  it("offers a retry for a message that failed before Enter, and the terminal when it may be half written", async () => {
    const r = demo();
    const s = r.sessions.get("s-msg")!;
    mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
    // Surely not sent: red, with a retry. Maybe sent: orange, to the terminal.
    expect(host.querySelector(".me.fail")!.textContent).toContain(message("composer.images_unrecognized", {"agent":"没发出去：Codex"}));
    const maybe = [...host.querySelectorAll(".me.warn small")].map((x) => x.textContent);
    expect(maybe.some((t) => t?.startsWith("可能没发出去："))).toBe(true);
    expect(host.textContent).not.toMatch(/没能发送|不会自动重发|终端里可能留着/);
    // A connection that timed out leaves it unknown: orange, no red frame.
    const timedOut = [...host.querySelectorAll(".me")].find((x) => x.textContent?.includes("顺便补一个测试"))!;
    expect(timedOut.classList.contains("warn")).toBe(true);
    expect(timedOut.classList.contains("fail")).toBe(false);
    expect(timedOut.querySelector("small")!.textContent).toBe(message("composer.maybe_failure", {"reason":"连接超时放回输入框重试移除此提示"}));
    const failed = [...host.querySelectorAll(".me.fail")].find((x) => x.textContent?.includes("Codex 没认出图片"))!;
    await click(button(message("common.retry", {}, "zh-CN"), failed));
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
      expect(button(message("common.retry", {}, "zh-CN"), bubble)).toBeTruthy();
      expect(button(message("session.dismiss_notice"), bubble)).toBeTruthy();
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
    expect(note()).toBe(message("image.unsynced"));
    await click(button(message("image.back_message")));
    await open("图片 wide.png");
    expect(note()).toBe(message("image.original_on_computer"));
    await click(button(message("image.back_message")));
    await open("图片 old.png");
    await vi.waitFor(() => expect(note()).toBe(message("image.original_expired")), { timeout: 3000 });
    await click(button(message("image.back_message")));
    await open("图片 flaky.png");
    await vi.waitFor(() => expect(note()).toBe(message("image.download_retry")), { timeout: 3000 });
    await click(host.querySelector<HTMLButtonElement>(".viewer .vnote")!);
    await vi.waitFor(() => expect(note()).not.toBe(message("image.download_retry")), { timeout: 3000 });
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
  function setup(fail: (name: string) => boolean, prepare?: DemoRemote["prepareImage"]) {
    const r = demo();
    r.prepareImage = prepare ?? (async (f) => Object.assign({ bytes: new Uint8Array(4), format: "png" as const, width: 1, height: 1 }, { name: (f as File).name }));
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

  it("hands the text to send before clearing the stored draft", async () => {
    const t = setup(() => false);
    act(() => { t.r.drafts.set("s-idle", "keep until outgoing owns it"); t.r.touch(); });
    let atSend: string | undefined;
    const send = vi.spyOn(t.r, "send").mockImplementation(() => {
      atSend = t.r.drafts.get("s-idle");
      return "new-send";
    });
    await click(sendButton());
    expect(send).toHaveBeenCalledOnce();
    expect(atSend).toBe("keep until outgoing owns it");
    expect(t.r.drafts.get("s-idle")).toBe("");
  });

  it("never sends a message with fewer images than shown", async () => {
    const t = setup((n) => n === "b.png");
    await t.pick("a.png", "b.png");
    await vi.waitFor(() => expect(host.textContent).toContain(message("image.the_image_could_not_reach_the_computer_retry_when")));
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
    expect(host.textContent).toContain(message("attachment.images_limit", {"limit":6,"count":2}));
    expect(host.querySelector(".cm-plus")!.classList.contains("off")).toBe(true);
  });

  it("opens each failed picture in the shared viewer and offers only useful actions", async () => {
    const t = setup(() => false, async f => {
      if ((f as File).name === "bad.heic") throw new Error("unsupported image format");
      if ((f as File).name === "large.png") throw new Error("图片尺寸太大");
      return { bytes: new Uint8Array(4), format: "png", width: 900, height: 600 };
    });
    await t.pick("ok.png", "bad.heic", "large.png");
    await vi.waitFor(() => expect(host.textContent).toContain(message("image.attach_failed", {"count":2}, "zh-CN")));
    await click(host.querySelectorAll<HTMLButtonElement>(".draft-open")[1]);
    const dialog = () => document.body.querySelector<HTMLElement>(`[role="dialog"][aria-label="${message("image.view")}"]`)!;
    expect(dialog().textContent).toContain("换成 JPEG 或 PNG 再试");
    expect(button(message("common.retry", {}, "zh-CN"), dialog())).toBeFalsy();
    act(() => dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight" })));
    expect(dialog().textContent).toContain(message("image.image_dimensions_are_too_large_resize_it_and_retry"));
    await click(button(message("common.remove", {}, "zh-CN"), dialog()));
    expect(host.querySelectorAll(".draft-open")).toHaveLength(2);
    expect(dialog().textContent).toContain("换成 JPEG 或 PNG 再试");
    await click(dialog().querySelector<HTMLElement>(`button[aria-label="${message("common.close")}"]`)!);
  });

  it("opens draft image details from a keyboard-preserving attachment menu", async () => {
    const t = setup(() => true);
    act(() => {
      const machine = t.r.machines.get(t.r.sessions.get("s-idle")!.machineId)!;
      machine.meta!.capabilities!.fileUpload = true;
      t.r.touch();
    });
    await t.pick("photo.png");
    await vi.waitFor(() => expect(host.textContent).toContain("连接恢复后重试"));
    const field = host.querySelector<HTMLTextAreaElement>(".draft")!;
    act(() => field.focus());
    await click(host.querySelector<HTMLButtonElement>(`[aria-label="${message("new_session.ios_attach")}"]`)!);
    expect(host.querySelector(".attach-menu")).not.toBeNull();
    expect(document.activeElement).toBe(field);
    const thumb = host.querySelector<HTMLButtonElement>(".draft-open")!;
    for (const type of ["pointerdown", "mousedown"]) {
      const event = new MouseEvent(type, { bubbles: true, cancelable: true });
      act(() => thumb.dispatchEvent(event));
      expect(event.defaultPrevented).toBe(true);
    }
    await click(thumb);
    expect(host.querySelector(".attach-menu")).toBeNull();
    const dialog = document.body.querySelector<HTMLElement>(`[role="dialog"][aria-label="${message("image.view")}"]`)!;
    expect(dialog.textContent).toContain(message("image.the_image_could_not_reach_the_computer_retry_when"));
    expect(button(message("common.retry", {}, "zh-CN"), dialog)).toBeTruthy();
    expect(button(message("common.remove", {}, "zh-CN"), dialog)).toBeTruthy();
    await click(dialog.querySelector<HTMLElement>(`[aria-label="${message("common.close")}"]`)!);
    expect(host.querySelectorAll(".draft-open")).toHaveLength(1);
  });

  it("retries a recoverable upload from its details without dropping its source", async () => {
    let offline = true;
    const t = setup(() => offline);
    await t.pick("photo.png");
    await vi.waitFor(() => expect(host.textContent).toContain("连接恢复后重试"));
    await click(host.querySelector<HTMLElement>(".draft-open")!);
    const dialog = document.body.querySelector<HTMLElement>(`[role="dialog"][aria-label="${message("image.view")}"]`)!;
    expect(button(message("common.retry", {}, "zh-CN"), dialog)).toBeTruthy();
    offline = false;
    await click(button(message("common.retry", {}, "zh-CN"), dialog));
    await vi.waitFor(() => expect(sendButton()?.disabled).toBe(false));
    expect(host.querySelectorAll(".draft-open")).toHaveLength(1);
    await click(dialog.querySelector<HTMLElement>(`button[aria-label="${message("common.close")}"]`)!);
  });

  it("offers no ＋ on a computer whose daemon cannot take images", async () => {
    const r = demo();
    const s = r.sessions.get("s-appr")!; // on VPS-dev, whose image folder has a space in its path
    mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
    await click(host.querySelector<HTMLButtonElement>(".cm-plus")!);
    expect(host.querySelector("input[type=file]")).toBeTruthy();
    expect(host.textContent).toContain(message("attachment.path_spaces", {"name":"VPS-dev"}));
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
  const retry = () => button(message("common.retry", {}, "zh-CN"), failed());
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
    expect(host.querySelector(".pending-meta > span")!.textContent).toBe(message("queue.agent_ready", {"agent":"Codex"}));
    expect(host.textContent).not.toContain(message("queue.insert"));
    expect(host.textContent!.split("给登录接口加限流").length - 1).toBe(1);
  });

  it("shows its real state, not 已送达 after another client canceled it", () => {
    const { push } = setup();
    // The only one waiting: not numbered.
    expect(host.querySelector(".pending-meta > span")!.textContent).toBe(message("queue.insert"));
    push({ case: "inputState", value: { requestId: "p1", status: InputStatus.CANCELED } });
    expect(host.textContent).not.toContain(message("queue.insert"));
    expect(host.textContent).toContain(message("queue.canceled_record"));
  });

  it("is a pending bubble, where it can be canceled", async () => {
    setup();
    const entry = host.querySelector(".pending-bubble")!;
    expect(entry.textContent).toContain("给登录接口加限流");
    expect(button(message("common.cancel", {}, "zh-CN"), entry)).toBeTruthy();
  });

});

describe("a failed message with images (review B6)", () => {
  function setup(text: string, status = InputStatus.FAILED, sources = false) {
    const r = demo();
    const s = r.sessions.get("s-idle")!;
    const blob = new Blob([new Uint8Array(4)], { type: "image/png" });
    (r as unknown as { outboxes: Map<string, unknown[]> }).outboxes.set("s-idle", [{ sendId: "f1", requestId: "x", deadline: 0, text, imageIds: ["i1", "i2"], thumbs: [blob, blob], attachments: sources ? { original: true, files: [blob, blob] } : undefined, status: "sent" }]);
    for (const kind of [
      { case: "inputState", value: { requestId: "f1", status: InputStatus.QUEUED, text, imageIds: ["i1", "i2"] } },
      { case: "inputState", value: { requestId: "f1", status, reason: "image_not_recognized" } },
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

  it("keeps uncertain thumbnails and restores source-backed pictures and text", async () => {
    const t = setup("没认出这两张", InputStatus.NEEDS_TERMINAL, true);
    t.r.prepareImage = async () => ({ bytes: new Uint8Array(4), format: "png", width: 1, height: 1 });
    t.r.uploadImage = async () => ({ imageId: "again", expiresMs: Number.MAX_SAFE_INTEGER });
    const warning = host.querySelector<HTMLElement>(".me.warn")!;
    expect(warning.querySelectorAll(".pic.local")).toHaveLength(2);
    await click(button(message("composer.restore"), warning));
    expect(host.querySelector<HTMLTextAreaElement>("textarea[aria-label=消息]")!.value).toBe("没认出这两张");
    expect(host.querySelectorAll(".draft-open")).toHaveLength(2);
  });

  it("waits for restoration before merging a put-back message and releasing its owner", async () => {
    const t = setup("put back", InputStatus.NEEDS_TERMINAL, true);
    t.r.prepareImage = async () => ({ bytes: new Uint8Array(4), format: "png", width: 1, height: 1 });
    t.r.uploadImage = async () => ({ imageId: "again", expiresMs: Number.MAX_SAFE_INTEGER });
    const tray = t.r.trays.get("s-idle")!;
    let done!: () => void;
    const gate = new Promise<void>(resolve => { done = resolve; });
    vi.spyOn(t.r, "restoreDraft").mockReturnValue(gate);
    await click(button(message("composer.restore"), host.querySelector<HTMLElement>(".me.warn")!));
    expect(tray.items).toHaveLength(0);
    expect(t.r.outbox("s-idle")).toHaveLength(1);
    await act(async () => {
      t.r.drafts.set("s-idle", "typed while reading");
      tray.hydrate([{ file: new Blob(["disk source"]) }], false);
      done();
    });
    expect(tray.items).toHaveLength(3);
    expect(t.r.drafts.get("s-idle")).toBe("typed while reading\n\nput back");
  });

  it("does not offer uncertain attachment recovery without local sources", () => {
    setup("other source", InputStatus.NEEDS_TERMINAL);
    expect(button(message("composer.restore"), host.querySelector<HTMLElement>(".me.warn")!)).toBeFalsy();
  });

  it("puts text and images back into the input, to send them again", async () => {
    const t = setup("看这两张");
    t.r.prepareImage = async () => ({ bytes: new Uint8Array(4), format: "png", width: 1, height: 1 });
    let uploads = 0;
    t.r.uploadImage = async () => ({ imageId: "restored-" + ++uploads, expiresMs: Number.MAX_SAFE_INTEGER });
    await click(button(message("composer.restore"), row()));
    expect(host.querySelector<HTMLTextAreaElement>("textarea[aria-label=消息]")!.value).toBe("看这两张");
    expect(host.querySelectorAll(".att-tray .pic").length).toBe(2);
    await act(async () => { await vi.waitFor(() => expect(host.querySelector<HTMLButtonElement>(`button[aria-label="${message("composer.send")}"]`)!.disabled).toBe(false)); });
    await click(host.querySelector<HTMLButtonElement>(`button[aria-label="${message("composer.send")}"]`)!);
    await vi.waitFor(() => expect(t.sent).toHaveLength(1));
    expect(t.sent[0]).toEqual({ text: "看这两张", imageIds: ["restored-1", "restored-2"] });
  });

  it("can retry or restore a message of images only", async () => {
    const t = setup("");
    expect(button(message("composer.restore"), row())).toBeTruthy();
    await click(button(message("common.retry", {}, "zh-CN"), row()));
    await vi.waitFor(() => expect(t.sent).toHaveLength(1));
    expect(t.sent[0]).toEqual({ text: "", imageIds: ["i1", "i2"] });
  });
});

it("keeps the same proportions, height and four-picture fold before and after confirmation", async () => {
  const blobs = Array.from({ length: 6 }, () => new Blob([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], { type: "image/png" }));
  const sizes = blobs.map(() => ({ width: 900, height: 600 }));
  mount(<LocalThumbs blobs={blobs} sizes={sizes} />);
  const before = [...host.querySelectorAll<HTMLElement>(".pic")].map(p => [p.style.width, p.style.height]);
  expect(before).toHaveLength(4);
  expect(host.querySelector(".more")!.textContent).toBe("+2");
  const refs = sizes.map(size => create(ImageRefSchema, { ...size, sync: ImageSync.SYNCED, previewFormat: ImageFormat.PNG, preview: new Uint8Array([0x89, 0x50, 0x4e, 0x47, 13, 10, 26, 10]) }));
  await act(async () => root!.render(<ImageStrip refs={refs} />));
  expect([...host.querySelectorAll<HTMLElement>(".pic")].map(p => [p.style.width, p.style.height])).toEqual(before);
  expect(host.querySelector(".more")!.textContent).toBe("+2");
});

it("consumes only the new-session request's attachments and keeps later disk drafts", async () => {
  const { IDBKeyRange } = await import("fake-indexeddb");
  const { storedSecurity } = await import("../test/stored-security-fixture");
  const { ComposerDraftStore } = await import("../composer-drafts");
  const f = await storedSecurity();
  vi.stubGlobal("IDBKeyRange", IDBKeyRange);
  try {
    const account = { server: "https://relay.example.com", deviceId: f.memberID } as import("../storage").StoredAccount;
    const disk = new ComposerDraftStore(account);
    const r = demo();
    (r as any).localDrafts = disk;
    r.machines.get("mac")!.meta!.capabilities!.fileUpload = true;
    r.prepareImage = async () => ({ bytes: new Uint8Array([1]), format: "png", width: 1, height: 1 });
    let uploads = 0;
    r.uploadImage = async (_m, p) => ({ imageId: "image-" + ++uploads, fileId: p.format === "file" ? "file-" + uploads : undefined, expiresMs: Date.now() + 100000 });
    const waiting = barrier();
    let sent = false;
    r.machineCommand = async (m: string, cmd: CommandKind) => {
      if (cmd?.case === "createSession") { sent = true; await waiting.wait; }
      return DemoRemote.prototype.machineCommand.call(r, m, cmd);
    };
    const key = "new:/Users/me/Code/foo";
    mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} />);
    const tray = r.trays.get(key)!;
    await vi.waitFor(() => expect(tray.restoring).toBe(false));
    await act(async () => {
      tray.add([new File(["sent image"], "sent.png", { type: "image/png" })]);
      tray.addFiles([new File(["sent file"], "sent.txt")]);
    });
    await vi.waitFor(() => expect(tray.ready()).toBe(true));
    await click(sendButton());
    await vi.waitFor(() => expect(sent).toBe(true));
    await act(async () => {
      tray.add([new File(["next image"], "next.png", { type: "image/png" })]);
      tray.addFiles([new File(["next file"], "next.txt")]);
    });
    await vi.waitFor(() => expect(tray.ready()).toBe(true));
    await act(async () => { waiting.release(); });
    await vi.waitFor(() => expect(tray.items.map(i => (i.file as File).name)).toEqual(["next.png", "next.txt"]));
    await r.persistDraft(key);
    const restored = await new ComposerDraftStore(account).read(key);
    expect(restored?.items.map(i => i.name)).toEqual(["next.png", "next.txt"]);
    expect(await Promise.all(restored!.items.map(i => i.file.text()))).toEqual(["next image", "next file"]);
  } finally { vi.useRealTimers(); vi.unstubAllGlobals(); }
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
    expect(setting(message("new_session.cli_picker_label", {}, "zh-CN"))).not.toContain("Claude Code");
    await click(button(message("common.done", {}, "zh-CN"), document));
  });

  it("keeps its project on the wide home once an image is chosen", async () => {
    const r = demo();
    stub(r);
    mount(<WideHome remote={r} go={() => undefined} />);
    await vi.waitFor(() => expect(host.querySelector(".wstart .sent")).not.toBeNull());
    const before = startProject();
    await pick();
    bump(r, "s-plan");
    expect(startProject()).toBe(before);
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
  expect(note()).toBe(message("image.load_connected"));
  act(() => {
    r.conn = "online";
    r.touch();
  });
  await vi.waitFor(() => expect(note()).toBe(message("image.original_expired")), { timeout: 3000 });
});

it("asks to add a project again when it is removed while its draft is on the wide home (review B12)", async () => {
  const r = demo();
  r.sessions.get("s-run")!.info!.createdAt = timestampFromMs(Date.now() + 86_400_000);
  await vi.waitFor(() => expect(r.prefs.status).toBe("ready"));
  mount(<WideHome remote={r} go={() => undefined} />);
  await vi.waitFor(() => expect(host.querySelector(".wstart .sent")).not.toBeNull());
  await act(async () => {
    const input = host.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${message("new_session.first_message", {}, "zh-CN")}"]`)!;
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, "加限流");
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await act(async () => {
    await r.changePrefs("hide", "/Users/me/Code/foo");
  });
  expect(host.textContent).toContain(message("projects.removed", {}, "zh-CN"));
  expect(host.querySelector<HTMLButtonElement>(`button[aria-label="${message("new_session.start")}"]`)!.disabled).toBe(true);
  expect(host.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${message("new_session.first_message", {}, "zh-CN")}"]`)!.value).toBe("加限流");
  await click(button(message("projects.add_again", {}, "zh-CN")));
  await vi.waitFor(() => expect(host.querySelector<HTMLButtonElement>(`button[aria-label="${message("new_session.start")}"]`)!.disabled).toBe(false), { timeout: 3000 });
});

it("marks a quota window as waiting for an update once its reset time passes (review B13)", async () => {
  vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval", "setTimeout", "clearTimeout"] });
  try {
    const r = demo();
    const { QuotaPage } = await import("./Settings");
    mount(<QuotaPage remote={r} id="mac" go={() => undefined} onBack={() => undefined} />);
    expect(host.textContent).not.toContain(message("quota.awaiting_update"));
    // The demo's 5-hour window resets in three hours; nothing else changes.
    act(() => void vi.advanceTimersByTime(3 * 60 * 60_000 + 60_000));
    expect(host.textContent).toContain(message("quota.awaiting_update"));
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
  await vi.waitFor(() => expect(host.querySelector<HTMLButtonElement>(`button[aria-label="${message("new_session.start")}"]`)!.disabled).toBe(false));
  await click(host.querySelector<HTMLButtonElement>(`button[aria-label="${message("new_session.start")}"]`)!);
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
    create(AgentQuotaSchema, { agent: Agent.ANTIGRAVITY, state: QuotaState.WAITING }),
  ];
  const { QuotaPage } = await import("./Settings");
  mount(<QuotaPage remote={r} id="mac" go={() => undefined} onBack={() => undefined} />);
  expect(host.textContent).not.toContain(message("quota.quota_lookup_is_not_supported_yet"));
  expect(host.textContent).toContain("剩 72%");
  expect(host.textContent).toContain(message("quota.quota_not_available_yet"));
  expect(host.textContent).not.toContain(message("quota.days", { count: 7 }));
  expect(host.textContent).toContain("最近收到");
});

it("renders Antigravity's quota windows grouped by family on the quota page", async () => {
  const r = demo();
  const { AgentQuotaSchema, QuotaState } = await import("../gen/remoteagent/v1/remote_pb");
  const m = r.machines.get("mac")!;
  const now = Date.now();
  if (!m.meta!.agents.some((a) => a.agent === Agent.ANTIGRAVITY)) {
    m.meta!.agents.push({ agent: Agent.ANTIGRAVITY, version: "0.2.3", verified: true, defaultMode: ApprovalMode.ASK, defaultModeSource: 0, defaultModeVaries: false, $typeName: "remoteagent.v1.AgentInfo" });
  }
  m.meta!.quotas = [
    create(AgentQuotaSchema, {
      agent: Agent.ANTIGRAVITY,
      state: QuotaState.OK,
      plan: "Google AI Pro",
      observedMs: BigInt(now),
      windows: [
        { name: "gemini-5h", usedPercent: 18, resetsAtMs: BigInt(now + 3_600_000), windowMinutes: 300n },
        { name: "gemini-weekly", usedPercent: 12, resetsAtMs: BigInt(now + 86_400_000), windowMinutes: 10080n },
        { name: "gemini-argon-wtu-weekly", usedPercent: 35, resetsAtMs: BigInt(now + 64_800_000), windowMinutes: 10080n },
        { name: "3p-5h", usedPercent: 40, resetsAtMs: BigInt(now + 7_200_000), windowMinutes: 300n },
        { name: "3p-weekly", usedPercent: 26, resetsAtMs: BigInt(now + 90_000_000), windowMinutes: 10080n },
      ],
    }),
  ];
  const { QuotaPage } = await import("./Settings");
  mount(<QuotaPage remote={r} id="mac" go={() => undefined} onBack={() => undefined} />);
  const labels = [...host.querySelectorAll(".label")].map((el) => el.textContent);
  expect(labels).toContain("Antigravity · Google AI Pro · Gemini");
  expect(labels).toContain(message("quota.third_party_models_claude_gpt_oss"));
  expect(host.textContent).toContain(message("quota.named_hours", { name: "Gemini", count: 5 }));
  expect(host.textContent).toContain(message("quota.named_days", { name: "Gemini 4 Argon", count: 7 }));
  expect(host.textContent).toContain(message("quota.named_hours", { name: message("quota.third_party_models"), count: 5 }));
  expect(host.textContent).not.toContain("gemini-argon-wtu-weekly");
  expect([...host.querySelectorAll(".gf")].filter((el) => el.textContent?.startsWith("最近收到"))).toHaveLength(1);
  // What is left of each window, the bar drawing what is left.
  expect(host.textContent).toContain("剩 82%");
  const bar = [...host.querySelectorAll<HTMLElement>(".qrow")].find((q) => q.textContent?.includes(message("quota.named_hours", { name: "Gemini", count: 5 })))!.querySelector<HTMLElement>(".bar i")!;
  expect(bar.style.width).toBe("82%");
});

it("switches new-project defaults by CLI tab on multi-CLI machines and omits tabs on single-CLI machines", async () => {
  const r = demo();
  const m = r.machines.get("mac")!;
  if (!m.meta!.agents.some((a) => a.agent === Agent.ANTIGRAVITY)) {
    m.meta!.agents.push({ agent: Agent.ANTIGRAVITY, version: "0.2.3", verified: true, defaultMode: ApprovalMode.ASK, defaultModeSource: 0, defaultModeVaries: false, $typeName: "remoteagent.v1.AgentInfo" });
  }
  r.machineCommand = async (id: string, cmd: CommandKind): Promise<CommandResult> => {
    if (cmd?.case === "listModels" && cmd.value.agent === Agent.ANTIGRAVITY) {
      return create(CommandResultSchema, {
        kind: {
          case: "listModels",
          value: {
            defaultModel: "gemini-3.1-pro-high",
            models: [
              { slug: "gemini-3.1-pro-high", displayName: "Gemini 3.1 Pro (High)", efforts: ["low", "medium", "high"], defaultEffort: "high", autoUnavailable: true },
              { slug: "gpt-oss-120b-medium", displayName: "GPT-OSS 120B (Medium)", efforts: ["medium"], defaultEffort: "medium", autoUnavailable: true },
            ],
          },
        },
      });
    }
    return DemoRemote.prototype.machineCommand.call(r, id, cmd);
  };
  const { MachinePage } = await import("./Settings");
  mount(<MachinePage remote={r} id="mac" go={() => undefined} onBack={() => undefined} />);
  // The machine's page lists every window, each with what is left.
  expect([...host.querySelectorAll(".quota-line")].find((r) => r.textContent?.startsWith("Antigravity"))?.querySelector(".v")?.textContent).toBe(
    "剩 60% · 第三方模型",
  );
  await vi.waitFor(() => expect(host.querySelector(".cli-tabs")).not.toBeNull());
  expect([...host.querySelectorAll(".cli-tabs button")].map((b) => b.textContent)).toEqual(["Codex", "Claude Code", "Antigravity"]);
  await click(button("Antigravity", host.querySelector(".cli-tabs")!));
  await click(button(new RegExp("^" + message("modes.title"))));
  await vi.waitFor(() => expect(button(new RegExp("^" + message("modes.auto")), document)?.disabled).toBe(true));
  expect(document.body.textContent).toContain(message("modes.account_unsupported"));

  // Single-CLI machine: no .cli-tabs, header says 新项目默认设置 · Codex.
  act(() => root!.render(<MachinePage remote={r} id="vps" go={() => undefined} onBack={() => undefined} />));
  await vi.waitFor(() => expect(host.textContent).toContain("新项目默认设置 · Codex"));
  expect(host.querySelector(".cli-tabs")).toBeNull();
});

it("shows and saves the machine's idle close setting, including a value outside the list", async () => {
  const r = demo();
  (r as unknown as { saved: unknown }).saved = create(DaemonSettingsSchema, { approvalTimeoutSeconds: 600, idleCloseMinutes: 90 });
  const { MachinePage } = await import("./Settings");
  mount(<MachinePage remote={r} id="mac" go={() => undefined} onBack={() => undefined} />);
  await vi.waitFor(() => expect(button(/^空闲自动结束会话/)?.textContent).toBe("空闲自动结束会话90 分钟"));
  await click(button(/^空闲自动结束会话/));
  const items = () => [...document.querySelectorAll<HTMLButtonElement>(".sheet .list .mi")];
  expect(items().map((b) => b.textContent)).toEqual([message("common.close", {}, "zh-CN"), "1 小时", "90 分钟", "2 小时", "6 小时（默认）", "12 小时", "24 小时"]);
  expect(items().filter((b) => b.querySelector(".ck")).map((b) => b.textContent)).toEqual(["90 分钟"]);
  expect(document.querySelector(".sheet .hint")?.textContent).toContain("随时可以恢复，聊天记录都在");
  await click(button(message("common.close", {}, "zh-CN"), document));
  await vi.waitFor(() => expect(button(/^空闲自动结束会话/)?.textContent).toBe("空闲自动结束会话关闭"));
  expect((r as unknown as { saved: { idleCloseMinutes: number } }).saved.idleCloseMinutes).toBe(-1);
  await click(button(/^空闲自动结束会话/));
  // Off now: the custom value is gone from the list and 关闭 is checked.
  expect(items().map((b) => b.textContent)).toEqual([message("common.close", {}, "zh-CN"), "1 小时", "2 小时", "6 小时（默认）", "12 小时", "24 小时"]);
  expect(items().filter((b) => b.querySelector(".ck")).map((b) => b.textContent)).toEqual([message("common.close", {}, "zh-CN")]);
  await click(button("12 小时", document));
  await vi.waitFor(() => expect(button(/^空闲自动结束会话/)?.textContent).toBe("空闲自动结束会话12 小时"));
});

it("names idle close values the way the daemon applies them", async () => {
  const { idleCloseLabel } = await import("./Settings");
  expect(idleCloseLabel(-1)).toBe(message("common.close", {}, "zh-CN"));
  expect(idleCloseLabel(-30)).toBe(message("common.close", {}, "zh-CN"));
  expect(idleCloseLabel(0)).toBe("6 小时");
  expect(idleCloseLabel(180)).toBe("3 小时");
  expect(idleCloseLabel(90)).toBe("90 分钟");
});

it("lists installed CLIs in machine picker subtitles and explains fallback when CLI is missing", async () => {
  const r = demo();
  const { ProjectPage } = await import("./Project");
  mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} />);
  await pickCli("Antigravity");
  await click(host.querySelector<HTMLButtonElement>(".tok.m")!);
  await vi.waitFor(() => expect(document.querySelector(".sheet")).not.toBeNull());
  const items = [...document.querySelectorAll<HTMLButtonElement>(".sheet .list .mi")];
  const macBtn = items.find((b) => b.textContent?.includes("MacBook"));
  expect(macBtn?.querySelector(".md")?.textContent).toBe(message("new_session.installed", {"agents":"Codex、Claude Code、Antigravity"}));
  const vpsBtn = items.find((b) => b.textContent?.includes("VPS-dev"));
  expect(vpsBtn?.querySelector(".md")?.textContent).toBe(message("new_session.installed_fallback", {"agents":"Codex","missing":"Antigravity","agent":"Codex"}, "zh-CN"));
});

it("retries a start with a launch override whose reply was lost as the same request (review W1)", async () => {
  const r = demo();
  const sent: { id: string; model?: string }[] = [];
  r.machineCommand = async (m: string, cmd: CommandKind, slot?: RequestSlot): Promise<CommandResult> => {
    if (cmd?.case === "createSession") {
      sent.push({ id: slot!.requestId, model: cmd.value.model });
      throw new CommandError("unknown_outcome", "连接超时");
    }
    return DemoRemote.prototype.machineCommand.call(r, m, cmd);
  };
  let ns: ReturnType<typeof useNewSession> | undefined;
  mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} />);
  // A launch override chosen by the user, mode left to the default.
  await fromMenu("新会话设置");
  await act(async () => { await r.listModels("mac", Agent.CODEX); });
  await click(button("GPT-6-Sol"));
  await click(button(message("common.done", {}, "zh-CN")));
  void ns;
  await act(async () => {
    const input = host.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${message("new_session.first_message", {}, "zh-CN")}"]`)!;
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, "加限流");
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await click(sendButton());
  await vi.waitFor(() => expect(host.textContent).toContain("不确定是否已经启动"));
  await click(sendButton());
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
  await click(button(message("composer.restore"), host.querySelector(".me.fail")!));
  expect(host.querySelector<HTMLTextAreaElement>("textarea[aria-label=消息]")!.value).toBe("看这两张");
  expect(host.querySelector(".att-tray")).toBeNull();
  expect(host.textContent).toContain("图片是另一客户端上传的");
});

it("keeps the wide home default project across background uploads before a draft starts", async () => {
  const r = demo();
  mount(<WideHome remote={r} go={() => undefined} />);
  const before = startProject();
  act(() => {
    r.sessions.get("s-plan")!.updatedMs = BigInt(Date.now() + 60_000);
    (r as unknown as { changed(): void }).changed();
  });
  expect(startProject()).toBe(before);
});

describe("the client-v7 conversation", () => {
  it("docks a waiting item open, keeps it to one line after 收起, and opens the next one", async () => {
    const r = demo();
    const page = () => <SessionPage remote={r} session={r.sessions.get("s-appr")!} onBack={() => undefined} wide={false} />;
    mount(page());
    const dock = () => host.querySelector(".dock.pending")!;
    // What waits for the user is open on arrival, under one head line.
    expect(dock().classList.contains("open")).toBe(true);
    expect(button(message("approval.allow"), dock())).toBeTruthy();
    expect(dock().querySelectorAll(".pending-label")).toHaveLength(1);
    expect(dock().querySelector(".pending-brief")!.textContent).toMatch(/^还有 1 项 · 剩 \d+ 分钟$/);
    expect(dock().querySelector(".pending-brief")!.textContent).not.toContain("rm -rf");
    await click(dock().querySelector<HTMLButtonElement>(".pending-sum")!);
    expect(dock().classList.contains("open")).toBe(false);
    expect(button(message("approval.allow"), dock())).toBeUndefined();
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
    // The command running now is its own block at the end, with the ink ring.
    const tool = host.querySelector(".tool.now")!;
    expect(tool.querySelector(".mark.run")).toBeTruthy();
    expect(tool.querySelector("pre")).toBeNull();
    expect(tool.textContent).toContain("go test");
    // The time says what it is.
    expect(tool.querySelector(".muted")!.textContent).toMatch(/^已运行 \d+:\d\d$/);
    expect(host.querySelector(".compose-status")).toBeNull();
  });

  it("says how long a turn took between turns", () => {
    const r = demo();
    mount(<SessionPage remote={r} session={r.sessions.get("s-ctx")!} onBack={() => undefined} wide={false} />);
    expect([...host.querySelectorAll(".sep:not(.fork)")].map((x) => x.textContent)).toContain(message("session.turn_duration", {"duration":"4 分 12 秒"}));
  });

  it("shows full access in the navbar and the other settings behind ⋯", async () => {
    const r = demo();
    const s = r.sessions.get("s-idle")!;
    s.info = { ...s.info!, mode: ApprovalMode.FULL_ACCESS };
    mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
    expect(host.querySelector(".cbar .access-warning")).toBeNull();
    expect(host.querySelector(".navbar .title")?.textContent).toContain(message("modes.full_access"));
    await click(host.querySelector<HTMLButtonElement>(".navbar .more")!);
    // A list sheet: settings read 「名称 …… 值 ›」, with no session ID and no 取消 card.
    const rows = [...host.querySelectorAll(".menu .mi")];
    const mode = rows.find((x) => x.querySelector(".mt")?.textContent === message("modes.title"))!;
    expect(mode.querySelector(".mv.red")!.textContent).toBe(message("modes.full_access"));
    expect(mode.querySelector(".chev")).toBeTruthy();
    expect(rows.find((x) => x.querySelector(".mt")?.textContent === message("models.title", {}, "zh-CN"))?.querySelector(".mv")?.textContent).toBeTruthy();
    expect(host.querySelector(".menu-info")!.textContent).not.toContain(message("home.sessions_tab", {}, "zh-CN"));
    expect(host.querySelector(".menu")!.textContent).not.toContain(s.id);
    expect(button(message("common.cancel", {}, "zh-CN"), host.querySelector(".menu")!)).toBeUndefined();
    expect(rows.find((x) => x.textContent === message("session.end_action"))?.classList.contains("danger")).toBe(true);
  });

  it("starts sessions only with a first message, in a sheet that stays as it is while typing", async () => {
    const r = demo();
    let closed = 0;
    mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => closed++} />);
    expect(host.textContent).not.toContain("只打开终端");
    // Close at the top left; the title names the project; the box at the bottom holds ＋, the CLI, the model and send.
    const head = host.querySelector(".nt-hd")!;
    expect(head.firstElementChild?.getAttribute("aria-label")).toBe(message("common.close", {}, "zh-CN"));
    expect(head.querySelector(".tok.p > span:not(.av)")?.textContent).toBe("foo");
    expect(cliPicked()).toBe("Codex");
    const ask = host.querySelector<HTMLTextAreaElement>(".pbox textarea[aria-label=新会话的第一句话]")!;
    expect(ask.placeholder).toBe(message("new_session.prompt", {}, "zh-CN"));
    const send = host.querySelector(".pbox .acc")!.lastElementChild!;
    expect(send.getAttribute("aria-label")).toBe(message("new_session.start", {}, "zh-CN"));
    expect(send.classList.contains("off")).toBe(true);
    await click(head.firstElementChild as HTMLButtonElement);
    expect(closed).toBe(1);
  });
});

describe("phone composer pickers", () => {
  it.each(["project", "machine"])("still dismisses the keyboard before the %s sheet, even from a model popover", async (target) => {
    const viewport = Object.assign(new EventTarget(), { height: 700, offsetTop: 0, scale: 1 });
    vi.stubGlobal("visualViewport", viewport);
    vi.stubGlobal("innerHeight", 700);
    const matchMedia = window.matchMedia.bind(window);
    const media = vi.spyOn(window, "matchMedia").mockImplementation((query) => {
      const result = matchMedia(query);
      if (query === "(min-width: 900px)") Object.defineProperty(result, "matches", { value: false });
      return result;
    });
    try {
      const r = demo();
      mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} />);
      const input = host.querySelector<HTMLTextAreaElement>(".ask")!;
      act(() => input.focus());
      viewport.height = 400;
      act(() => viewport.dispatchEvent(new Event("resize")));
      await fromMenu("新会话设置");
      expect(document.activeElement).toBe(input);
      expect(host.querySelector(".model-picker")).not.toBeNull();
      const chip = host.querySelector<HTMLButtonElement>(target === "project" ? ".nt-hd .tok.p" : ".nt-hd .tok.m")!;
      act(() => {
        chip.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, cancelable: true }));
        chip.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, cancelable: true }));
      });
      await click(chip);
      expect(document.activeElement).not.toBe(input);
      expect(host.querySelector(".model-picker")).toBeNull();
      await act(async () => { await new Promise((done) => setTimeout(done, 30)); });
      expect(host.querySelector(".sheet")).toBeNull();
      viewport.height = 700;
      act(() => viewport.dispatchEvent(new Event("resize")));
      await vi.waitFor(() => expect(host.querySelector(".sheet .sheet-head")?.textContent).toContain(target === "project" ? message("projects.choose", {}, "zh-CN") : message("common.machine", {}, "zh-CN")));
    } finally {
      media.mockRestore();
      vi.unstubAllGlobals();
    }
  });

  it.each(["done", "outside", "textarea", "typing", "escape", "same chip", "other chip"])("keeps focus and saves choices on %s", async (close) => {
    const r = demo();
    const path = "/Users/me/Code/foo";
    mount(<ProjectPage remote={r} pkey={path} go={() => undefined} onBack={() => undefined} />);
    await vi.waitFor(() => expect(host.querySelector(".project-summary")?.textContent).toContain("GPT-6-Astra"));
    // Refresh is deliberately unresolved: opening still shows the cached models.
    vi.spyOn(r, "listModels").mockReturnValue(new Promise(() => undefined));
    const input = host.querySelector<HTMLTextAreaElement>(".ask")!;
    input.focus();
    const chip = host.querySelector<HTMLButtonElement>(".project-summary")!;
    const down = new PointerEvent("pointerdown", { bubbles: true, cancelable: true });
    act(() => chip.dispatchEvent(down));
    expect(down.defaultPrevented).toBe(true);
    await click(chip);
    expect(host.querySelector(".overlay")).toBeNull();
    expect(host.querySelector(".composer-picker")?.getAttribute("aria-modal")).toBeNull();
    expect(host.querySelector(".composer-picker")?.textContent).toContain("GPT-6-Luna");
    expect(host.querySelector(".composer-picker")?.textContent).not.toContain("读取");
    const model = button("GPT-6-Luna")!;
    const modelDown = new PointerEvent("pointerdown", { bubbles: true, cancelable: true });
    act(() => model.dispatchEvent(modelDown));
    expect(modelDown.defaultPrevented).toBe(true);
    await click(model);
    await click(button("high"));
    if (close === "done") await click(button(message("common.done", {}, "zh-CN")));
    else if (close === "same chip") await click(chip);
    else if (close === "other chip") await click(host.querySelector<HTMLButtonElement>(".tok.cli")!);
    else if (close === "escape") act(() => window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })));
    else if (close === "typing") act(() => input.dispatchEvent(new InputEvent("beforeinput", { bubbles: true })));
    else {
      const target = close === "textarea" ? input : host.querySelector<HTMLElement>(".nt-body")!;
      const mouse = new MouseEvent("mousedown", { bubbles: true, cancelable: true });
      act(() => {
        target.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, cancelable: true }));
        target.dispatchEvent(mouse);
      });
      if (close === "outside") expect(mouse.defaultPrevented).toBe(true);
      await click(target);
    }
    expect(host.querySelector(".model-picker")).toBeNull();
    expect(document.activeElement).toBe(input);
    await vi.waitFor(() => expect(savedSet(r, path)).toMatchObject({ model: "gpt-6-luna", effort: "high" }));
    if (close === "other chip") {
      expect(host.querySelector(".composer-picker [role=menu]")).not.toBeNull();
      await click(host.querySelector<HTMLButtonElement>(".tok.cli")!);
      expect(host.querySelector(".composer-picker")).toBeNull();
    }
  });

  it("does not focus the input when opened unfocused, and closes on a target change", async () => {
    const r = demo();
    const page = (path: string) => <ProjectPage remote={r} pkey={path} go={() => undefined} onBack={() => undefined} />;
    mount(page("/Users/me/Code/foo"));
    await fromMenu("新会话设置");
    expect(document.activeElement).not.toBe(host.querySelector(".ask"));
    expect(host.querySelector(".composer-picker")).not.toBeNull();
    act(() => root!.render(page("/Users/me/Code/shop")));
    expect(host.querySelector(".composer-picker")).toBeNull();
  });

  it("applies effort immediately, preserves it across models and never saves again on close", async () => {
    const r = demo();
    mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} />);
    await vi.waitFor(() => expect(host.querySelector(".project-summary")?.textContent).toContain("GPT-6-Astra · medium"));
    const save = vi.spyOn(r, "changePrefs");
    await fromMenu("新会话设置");
    await click(button("ultra"));
    expect(host.querySelector(".project-summary")?.textContent).toBe("GPT-6-Astra · ultra");
    expect(host.querySelector(".project-summary")?.getAttribute("aria-expanded")).toBe("true");
    await click(button("GPT-6-Sol"));
    expect(host.querySelector(".project-summary")?.textContent).toBe("GPT-6-Sol · ultra");
    await click(button("GPT-6-Nova"));
    expect(host.querySelector(".project-summary")?.textContent).toBe(`GPT-6-Nova · ${message("models.default", {}, "zh-CN")}`);
    expect(save).toHaveBeenCalledTimes(3);
    await click(button(message("common.done", {}, "zh-CN")));
    expect(save).toHaveBeenCalledTimes(3);
  });

  it("uses a separate all-models page, returns without closing and includes the chosen model", async () => {
    const r = demo();
    const original = r.listModels.bind(r);
    vi.spyOn(r, "listModels").mockImplementation(async (...args) => {
      const catalog = await original(...args);
      return { ...catalog, models: [...catalog.models, ...[1, 2, 3].map(i => ({ ...catalog.models[0], slug: `extra-${i}`, displayName: `Extra ${i}` }))] };
    });
    mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} />);
    await fromMenu("新会话设置");
    await vi.waitFor(() => expect(button(message("models.all", {}, "zh-CN"))).toBeDefined());
    const panel = () => host.querySelector(".new-task-model-panel")!;
    expect(panel().querySelector(".model-controls")).not.toBeNull();
    await click(button(message("models.all", {}, "zh-CN")));
    expect(panel().getAttribute("data-page")).toBe("all");
    expect(panel().querySelector(".model-controls")).toBeNull();
    await click(host.querySelector<HTMLButtonElement>(`[aria-label="${message("models.back", {}, "zh-CN")}"]`)!);
    expect(panel().getAttribute("data-page")).toBe("main");
    await click(button(message("models.all", {}, "zh-CN")));
    await click(button("Extra 3"));
    expect(panel().getAttribute("data-page")).toBe("main");
    expect(button("Extra 3")?.getAttribute("aria-pressed")).toBe("true");
    expect(host.querySelector(".project-summary")?.textContent).toBe("Extra 3 · medium");
  });

  it.each([{ efforts: [] }, { efforts: ["medium"] }])("omits an effort without a choice ($efforts)", async ({ efforts }) => {
    const r = demo();
    const original = r.listModels.bind(r);
    vi.spyOn(r, "listModels").mockImplementation(async (...args) => {
      const catalog = await original(...args);
      return { ...catalog, models: catalog.models.map(m => ({ ...m, efforts, defaultEffort: efforts[0] ?? "" })) };
    });
    mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} />);
    await vi.waitFor(() => expect(host.querySelector(".project-summary")?.textContent).toBe("GPT-6-Astra"));
    expect(host.querySelector(".project-summary")?.getAttribute("aria-label")).toBe(message("models.summary_label", { model: "GPT-6-Astra" }, "zh-CN"));
    await fromMenu("新会话设置");
    expect(host.querySelector(`[aria-label="${message("models.effort", {}, "zh-CN")}"]`)).toBeNull();
    expect(host.querySelector(".new-task-model-panel")?.textContent.includes(message("models.fixed_effort", { effort: "medium" }, "zh-CN"))).toBe(efforts.length === 1);
  });

  it("keeps the wide new task on its existing sheet", async () => {
    const r = demo();
    mount(<WideHome remote={r} go={() => undefined} project="/Users/me/Code/foo" />);
    await fromMenu("新会话设置");
    expect(host.querySelector(".sheet")).not.toBeNull();
    expect(host.querySelector(".composer-picker")).toBeNull();
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
    expect(picked()).toBe(message("session.chat"));
    await click(button(message("session.terminal")));
    expect(picked()).toBe(message("session.terminal"));
    close();
    open(r, "s-ctx");
    expect(picked()).toBe(message("session.terminal"));
    await click(button(message("session.chat")));
    close();
    open(r, "s-idle");
    expect(picked()).toBe(message("session.chat"));
  });

  it("does not take a new session's first terminal for a pick", () => {
    const r = demo();
    r.termFirst.add("s-idle");
    open(r, "s-idle");
    expect(picked()).toBe(message("session.terminal"));
    close();
    open(r, "s-ctx");
    expect(picked()).toBe(message("session.chat"));
  });

  it("shows the chat of an ended session and keeps the pick", () => {
    const r = demo();
    localStorage.setItem("ra-view", "term");
    const ended = [...r.sessions.values()].find((s) => s.info?.status === SessionStatus.EXITED)!;
    open(r, ended.id);
    expect(picked()).toBe(message("session.chat"));
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
  await vi.waitFor(() => expect(button("GPT-6.1-Sol", document)).toBeDefined());
  expect(button(/^默认[（模型]/, document)).toBeDefined();
  await click(button(/^默认[（模型]/, document));
  await vi.waitFor(() => expect(button("ultra", document)).toBeDefined());
  for (const e of ["low", "medium", "high", "xhigh", "max", "ultra"]) expect(button(e, document)).toBeDefined();
  // The summary capsule is named like the model now: pick it in the sheet.
  await click(button("GPT-6.1-Sol", document.querySelector(":is(.sheet, .composer-picker)")!));
  await click(button("high", document));
  await click(button(message("common.done", {}, "zh-CN"), document));
  await vi.waitFor(() => expect(savedSet(r, "/Users/me/Code/foo")).toMatchObject({ model: "gpt-6.1-sol", effort: "high" }), { timeout: 4000 });
  const before = reads;
  await fromMenu("新会话设置");
  await vi.waitFor(() => expect(reads).toBeGreaterThan(before));
});

it("renames a session from ⋯, keeps what was typed when saving fails, and goes back to the automatic title", async () => {
  const r = demo();
  const s = r.sessions.get("s-run")!;
  const was = s.info!.title;
  mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
  const input = () => host.querySelector<HTMLInputElement>(".dialog.rename input")!;
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
  await click(button(message("common.save", {}, "zh-CN")));
  await vi.waitFor(() => expect(host.querySelector(".dialog.rename")?.textContent).toContain(message("new_session.machine_offline", {"name":message("common.ios_save_failed", {"reason":"MacBook"})}, "zh-CN")));
  expect(input().value).toBe("  登录超时：限流和重试 ");

  await click(button(message("common.save", {}, "zh-CN")));
  expect(command).toHaveBeenLastCalledWith(s.id, { case: "renameSession", value: { sessionId: s.id, title: "登录超时：限流和重试" } });
  await vi.waitFor(() => expect(host.querySelector(".dialog.rename")).toBeNull(), { timeout: 3000 });
  expect(r.sessions.get("s-run")!.info!.title).toBe("登录超时：限流和重试");

  // Saving the same title sends nothing; an empty one clears it.
  command.mockClear();
  await fromMenu("重命名");
  await click(button(message("common.save", {}, "zh-CN")));
  expect(command).not.toHaveBeenCalled();
  expect(host.querySelector(".dialog.rename")).toBeNull();
  await fromMenu("重命名");
  write("");
  await click(button(message("common.save", {}, "zh-CN")));
  expect(command).toHaveBeenLastCalledWith(s.id, { case: "renameSession", value: { sessionId: s.id, title: "" } });
});

it("abandons an unexpired unknown fork and starts with a new ID", async () => {
  const r = demo();
  const rows = () => [...host.querySelectorAll<HTMLButtonElement>(".menu .mi")].filter((b) => /分叉/.test(b.textContent ?? ""));
  const row = () => rows()[0]!;
  const abandon = () => rows().find((b) => /^放弃上次的分叉/.test(b.textContent ?? ""))!;
  const s = r.sessions.get("s-idle")!;
  mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
  await click(host.querySelector<HTMLButtonElement>(".navbar .more")!);
  const command = vi.spyOn(r, "command");
  command.mockRejectedValueOnce(new CommandError("unknown_outcome", "不确定电脑有没有执行"));
  await click(row());
  // Frozen but not expired: the same request can still be retried, or given up.
  await vi.waitFor(() => expect(abandon()).toBeTruthy());
  expect(row().textContent).toContain(message("session.fork_unknown"));
  const slot = command.mock.calls[0][2];
  expect(slot).toBeTruthy();
  await click(host.querySelector<HTMLElement>(".overlay .dim")!);
  await click(host.querySelector<HTMLButtonElement>(".navbar .more")!);
  expect(rows()).toHaveLength(2);
  await click(abandon());
  expect(command).toHaveBeenCalledTimes(1);
  expect(rows()).toHaveLength(1);
  expect(row().textContent).toBe(message("session.fork"));
  location.hash = "";
  await click(row());
  expect(command.mock.calls[1][2]?.requestId).not.toBe(slot?.requestId);
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
  expect(row().textContent).toBe(message("session.forking"));
  expect(button(message("session.rename"))!.disabled).toBe(true);
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

describe("the home's one sentence and its bottom row (client-v58)", () => {
  const hl = () => host.querySelector(".hl")?.textContent;
  const sub = () => host.querySelector(".hl-sub")?.textContent;
  const drop = (r: DemoRemote, keep: (s: ReturnType<typeof todoList>[number]) => boolean) =>
    act(() => {
      for (const s of [...r.sessions.values()]) if (!keep(s)) r.sessions.delete(s.id);
      r.touch();
    });

  it("says how many things wait, the number in orange, and counts running and idle apart", () => {
    const r = demo();
    mount(<Home remote={r} go={() => undefined} />);
    const n = todoList(r).length;
    expect(hl()).toBe(`${n} 件事等你`);
    expect(host.querySelector(".hl em")?.textContent).toBe(String(n));
    expect(sub()).toMatch(/^\d+ 个会话在运行，\d+ 个空闲$/);
    // The tab's badge is the same count; 新任务 is the bottom right's ink button.
    expect(host.querySelector(".tabs .on .count")?.textContent).toBe(String(n));
    expect(host.querySelector<HTMLButtonElement>(".bbar .fab")?.textContent).toBe(message("home.new_task", {}, "zh-CN"));
    expect(host.querySelector<HTMLButtonElement>(".bbar .fab")?.disabled).toBe(false);
    // No tab for settings: the gear on the top right.
    expect([...host.querySelectorAll(".tabs button")].map((b) => b.textContent)).toEqual([`会话${n}`, message("home.schedules_tab", {}, "zh-CN")]);
    expect(host.querySelector(`.tbar [aria-label="${message("common.settings")}"]`)).toBeTruthy();
  });

  it("says what runs once nothing waits, then that nothing waits, then that there is no session", () => {
    const r = demo();
    saveScope(ALL);
    mount(<Home remote={r} go={() => undefined} />);
    drop(r, (s) => !todoList(r).includes(s));
    expect(hl()).toMatch(/^\d+ 个会话在运行$/);
    expect(sub()).toMatch(/^另有 \d+ 个空闲$/);
    expect(host.querySelector(".tc, .pl")).toBeNull();
    drop(r, (s) => stateOf(s, r.machines.get(s.machineId)).tone !== "run");
    expect(hl()).toBe(message("home.nothing_waiting", {}, "zh-CN"));
    expect(sub()).toMatch(/^\d+ 个会话空闲$/);
    drop(r, () => false);
    expect(hl()).toBe(message("home.no_sessions", {}, "zh-CN"));
    expect(host.querySelector(".home-note")?.textContent).toContain("点右下角的「新任务」开始第一个会话");
  });

  it("names the stretch of the way that is down, keeps what waits, and grays its buttons", () => {
    const r = createDemo(new URLSearchParams("conn=nonetwork"));
    r.start();
    mount(<Home remote={r} go={() => undefined} />);
    expect(hl()).toBe(message("connection.no_network", {}, "zh-CN"));
    expect(sub()).toBe(message("connection.previous_below", {}, "zh-CN"));
    // What waits stays on top, with its count on the tab, but cannot be handled now.
    const card = host.querySelector(".tc")!;
    expect(card.querySelector(".k span")?.textContent).toBe(message("list_state.unlinked", {}, "zh-CN"));
    expect(button(message("approval.deny"), card)?.disabled).toBe(true);
    expect(button(message("approval.allow"), card)?.disabled).toBe(true);
    expect(host.querySelector(".tabs .count")?.textContent).toBe(String(todoList(r).length));
    expect(host.querySelector<HTMLButtonElement>(".bbar .fab")?.disabled).toBe(true);
    // Sessions not ended are not said to run: a faint dot and 未连接.
    for (const row of host.querySelectorAll(".sess")) {
      expect(row.querySelector(".mark.off")).toBeTruthy();
      expect(row.querySelector(".pv")?.textContent).toMatch(/^未连接/);
    }
  });

  it("says the server is out of reach, with the reason behind 详情", async () => {
    const r = createDemo(new URLSearchParams("conn=noserver"));
    r.start();
    mount(<Home remote={r} go={() => undefined} />);
    expect(hl()).toBe(message("connection.no_server", {}, "zh-CN"));
    expect(sub()).toBe("正在重试，下面是上次的内容详情");
    await click(button(message("common.details", {}, "zh-CN")));
    expect(sub()).toContain("连不上服务器：连接超时。也可能是这台设备的网络不通");
    expect(button(message("common.retry", {}, "zh-CN"))).toBeTruthy();
  });
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
    expect(host.querySelector(".tbar .scope b")?.textContent).toBe(message("home.all_projects", {}, "zh-CN"));
    expect(host.textContent).not.toContain("常用项目");
    // More than one project, CLI and machine: every live row has its third line.
    const live = [...host.querySelectorAll(".sess")];
    expect(live.length).toBeGreaterThan(0);
    for (const row of live) expect(row.querySelector(".mc")?.textContent).toMatch(/ · /);
    // Ended sessions are one line each, under a date group, with their project.
    expect(texts(".old .sm > span:first-child")).toContain("补充 README");
    expect(texts(".old .sm > span:last-child").every((x) => x.includes(" · "))).toBe(true);
    expect(host.querySelector(".old .mark")).toBeNull();
  });

  it("keeps a project's rows to what differs, and still shows what waits elsewhere", async () => {
    const r = demo();
    const went: string[] = [];
    mount(<Home remote={r} go={(x) => went.push(x)} />);
    await click(host.querySelector<HTMLButtonElement>(".tbar .scope")!);
    expect(document.querySelector(":is(.sheet, .composer-picker)")?.textContent).toContain(message("home.choose_scope", {}, "zh-CN"));
    await click([...document.querySelectorAll<HTMLButtonElement>(".plist .pi")].find((b) => b.querySelector("b")?.textContent === "foo"));
    expect(document.querySelector(":is(.sheet, .composer-picker)")).toBeNull();
    expect(host.querySelector(".tbar .scope b")?.textContent).toBe("foo");
    // Live rows are foo's only, and do not name the project.
    const live = texts(".sess .row1 b");
    expect(live).toContain("修复登录超时");
    expect(live).not.toContain("整理错误码");
    expect(texts(".sess .mc").some((x) => x.includes("foo"))).toBe(false);
    expect(texts(".old .sm > span:last-child").some((x) => x.includes("foo"))).toBe(false);
    // What waits is the account's: bar's question is there, with its project.
    const bar = [...host.querySelectorAll(".pr")].find((c) => c.textContent?.includes("升级依赖"));
    expect(bar?.querySelector(".nb > span")?.textContent).toContain("bar · ");
    // 新任务 goes straight to the project.
    await click(button(message("home.new_task", {}, "zh-CN")));
    expect(went).toEqual(["p/" + encodeURIComponent(foo)]);
    expect(r.focusProject).toBe(foo);
  });

  it("falls back to all projects when the saved project leaves the list", async () => {
    const r = demo();
    await vi.waitFor(() => expect(r.prefs.status).toBe("ready"));
    saveScope("/Users/me/Code/shop");
    mount(<Home remote={r} go={() => undefined} />);
    expect(host.querySelector(".tbar .scope b")?.textContent).toBe("shop");
    await act(async () => { await r.changePrefs("hide", "/Users/me/Code/shop"); });
    expect(host.querySelector(".tbar .scope b")?.textContent).toBe(message("home.all_projects", {}, "zh-CN"));
  });

  it("filters by title without regard to case, keeping the groups", async () => {
    const r = demo();
    saveScope(ALL);
    mount(<Home remote={r} go={() => undefined} />);
    const box = host.querySelector<HTMLInputElement>(`input[aria-label="${message("home.filter_title")}"]`)!;
    expect(box).toBeTruthy();
    await type(box, "readme");
    expect(texts(".old .sm > span:first-child")).toEqual(["补充 README"]);
    expect(host.querySelectorAll(".sess, .pr, .tc")).toHaveLength(0);
    expect(host.querySelector(".tail")?.textContent).toBe(message("home.matches", {"count":1,"query":"readme"}, "zh-CN"));
    await type(box, "没有这个标题");
    expect(host.querySelector(".tail")?.textContent).toBe(message("home.no_matches", {"query":"没有这个标题"}, "zh-CN"));
    await click(host.querySelector<HTMLButtonElement>(`button[aria-label="${message("home.clear_filter")}"]`)!);
    expect(texts(".sess .row1 b")).toContain("修复登录超时");
  });

  it("is the sidebar on a wide window, where picking a project sends the main area home", async () => {
    const r = demo();
    const went: string[] = [];
    mount(<Sidebar remote={r} go={(x) => went.push(x)} selected="s-run" project="/Users/me/Code/bar" />);
    expect(host.querySelector(".sess.sel .row1 b")?.textContent).toBe("修复登录超时");
    // Waiting sessions are rows here, not cards.
    expect(host.querySelector(".tc, .pr")).toBeNull();
    await click(host.querySelector<HTMLButtonElement>(".side-top .scp")!);
    expect(host.querySelector(".pop")).toBeTruthy();
    await click([...host.querySelectorAll<HTMLButtonElement>(".pop .pi")].find((b) => b.querySelector("b")?.textContent === "billing"));
    expect(host.querySelector(".pop")).toBeNull();
    expect(went).toEqual([""]);
    expect(host.querySelector(".side-top .scp b")?.textContent).toBe("billing");
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
    await type(host.querySelector<HTMLInputElement>(`input[aria-label="${message("home.filter_title")}"]`)!, "readme");
    expect(all).toBeGreaterThan(0);
    expect(host.querySelector(".tail")?.textContent).toBe(message("home.loading_earlier", {}, "zh-CN"));
    r.moreSessions = false;
    act(() => (r as unknown as { changed(): void }).changed());
    expect(host.querySelector(".tail")?.textContent).toBe(message("home.matches", {"count":1,"query":"readme"}, "zh-CN"));
  });

  it("makes a project the scope from project management", async () => {
    const r = demo();
    const went: string[] = [];
    mount(<ManagePage remote={r} go={(x) => went.push(x)} onBack={() => undefined} />);
    await click(host.querySelector<HTMLButtonElement>('[aria-label="看 shop 的会话"]')!);
    expect(went).toEqual([""]);
    act(() => root!.render(<Home remote={r} go={() => undefined} />));
    expect(host.querySelector(".tbar .scope b")?.textContent).toBe("shop");
  });

  it("starts in the scope's project on the wide home, and names the project only under all projects", () => {
    const r = demo();
    mount(<WideHome remote={r} go={() => undefined} />);
    expect(startProject()).toBeTruthy();
    act(() => saveScope(foo));
    expect(startProject()).toBeUndefined();
    expect(host.querySelector(".wstart .sent b")?.textContent).toBe("foo");
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
  const start = () => host.querySelector<HTMLButtonElement>(`button[aria-label="${message("new_session.start")}"]`)!;
  const input = host.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${message("new_session.first_message", {}, "zh-CN")}"]`)!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, "对一下三月的账");
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  // A Codex session within the pages is held: Codex is settled, nothing is fetched for it.
  expect(pages).toBe(0);
  await vi.waitFor(() => expect(start().disabled).toBe(false), { timeout: 3000 });
  // Claude Code has no session held, and there are ended ones not fetched.
  await pickCli("Claude Code");
  expect(pages).toBeGreaterThan(0);
  expect(start().disabled).toBe(true);
  expect(host.textContent).toContain(message("new_session.reading_previous", {}, "zh-CN"));
  // Its settings are not offered for change meanwhile.
  await click(host.querySelector<HTMLButtonElement>(".project-summary")!);
  expect(document.querySelector(":is(.sheet, .composer-picker)")?.textContent).toContain(message("new_session.reading_previous", {}, "zh-CN"));
  // Every page is in and none was Claude Code's: it starts with the machine's defaults.
  r.moreSessions = false;
  act(() => (r as unknown as { changed(): void }).changed());
  expect(document.querySelector(":is(.sheet, .composer-picker)")?.textContent).not.toContain(message("new_session.reading_previous", {}, "zh-CN"));
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
  r.machineCommand = async (_m: string, _c: CommandKind, slot?: RequestSlot) => { slots.push(slot!.requestId); throw new CommandError("unknown_outcome", "连接超时"); };
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
  await click(button(message("composer.restore")));
  expect(host.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${message("composer.message")}"]`)!.value).toBe("draft\n\nrestore");
});

it("keeps an approval reason across dock and detail mounts", async () => {
  const r = demo();
  const a = derive(r.events("s-appr")).approvals.values().next().value!;
  const { ApprovalCard } = await import("./ApprovalCard");
  const actions = <ApprovalCard remote={r} sid="s-appr" a={a} docked />;
  mount(actions);
  await click(button(message("approval.deny")));
  await reviewType(host.querySelector("textarea")!, "reason");
  act(() => root!.render(null));
  act(() => root!.render(actions));
  await click(button(message("approval.deny")));
  expect(host.querySelector<HTMLTextAreaElement>("textarea")!.value).toBe("reason");
});

it("shows failure and retry when resuming a paused message", async () => {
  const r = demo();
  const { PendingBubbles } = await import("./Queue");
  const d = derive([create(EventSchema, { kind: { case: "inputState", value: { requestId: "paused", status: InputStatus.QUEUED, paused: true, text: "message" } } })]);
  r.command = async () => { throw new CommandError("unavailable", "电脑不在线"); };
  function Page() { useRemote(r); return <PendingBubbles remote={r} session={r.sessions.get("s-idle")!} d={d} />; }
  mount(<Page />);
  await click(button(message("composer.send")));
  expect(host.textContent).toContain(message("queue.resume_failed"));
  expect(button(message("common.retry", {}, "zh-CN"))).toBeTruthy();
});

it("offers more ended matches while filtering titles", async () => {
  const r = demo();
  const ended = [...r.sessions.values()].find(s => s.info?.status === SessionStatus.EXITED)!;
  r.sessions.clear();
  for (let i = 0; i < 40; i++) r.sessions.set("match" + i, { ...ended, id: "match" + i, info: { ...ended.info!, title: "match " + i } });
  mount(<Home remote={r} go={() => undefined} />);
  await reviewType(host.querySelector<HTMLInputElement>(`input[aria-label="${message("home.filter_title")}"]`)!, "match");
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
  const { ApprovalCard } = await import("./ApprovalCard");
  mount(<ApprovalCard remote={r} sid="s-appr" a={a} docked />);
  await click(button(message("approval.deny")));
  await reviewType(host.querySelector<HTMLTextAreaElement>("textarea.inp")!, "retained reason");
  const writeText = vi.fn(async () => undefined);
  vi.spyOn(navigator.clipboard, "writeText").mockImplementation(writeText);
  act(() => {
    r.events("s-appr").push(create(EventSchema, { seq: BigInt(r.events("s-appr").length + 1), kind: { case: "approvalResolved", value: { approvalId: a.approvalId, decision: ApprovalDecision.ALLOW } } }));
    r.touch();
    root!.render(<SessionPage remote={r} session={r.sessions.get("s-appr")!} onBack={() => undefined} wide={false} />);
  });
  expect(host.textContent).toContain("retained reason");
  await click(button(message("common.copy", {}, "zh-CN")));
  expect(writeText).toHaveBeenCalledWith("retained reason");
});

it("shows the pending start and its original message when returning to the project", async () => {
  const r = demo();
  const slots: string[] = [];
  const original = r.machineCommand.bind(r);
  r.machineCommand = async (machine: string, cmd: CommandKind, slot?: RequestSlot) => {
    if (cmd?.case === "createSession") { slots.push(slot!.requestId); throw new CommandError("unknown_outcome", "连接超时"); }
    return original(machine, cmd);
  };
  const page = <ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} />;
  mount(page);
  await reviewType(host.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${message("new_session.first_message", {}, "zh-CN")}"]`)!, "task");
  await act(async () => { await vi.waitFor(() => expect(sendButton()?.disabled).toBe(false)); });
  await click(sendButton());
  await act(async () => { await vi.waitFor(() => expect(host.textContent).toContain("不确定是否已经启动")); });
  act(() => root!.render(null));
  act(() => root!.render(page));
  expect(host.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${message("new_session.first_message", {}, "zh-CN")}"]`)!.value).toBe("task");
  expect(host.textContent).toContain("不确定是否已经启动");
  await click(sendButton());
  expect(slots).toHaveLength(2);
  expect(slots[1]).toBe(slots[0]);
});

it("keeps unsent new-session text by project across navigation and layout changes", async () => {
  const r = demo();
  const a = "/Users/me/Code/foo";
  const b = "/Users/me/Code/shop";
  const input = () => host.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${message("new_session.first_message", {}, "zh-CN")}"]`)!;
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
  const input = () => host.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${message("new_session.first_message", {}, "zh-CN")}"]`)!;
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
  await reviewType(host.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${message("composer.message")}"]`)!, "选择 A");
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
  await vi.waitFor(() => expect(host.textContent).toContain(message("attachment.ios_image_missing")));
  expect(host.querySelector(".me.fail")!.textContent).not.toContain(message("common.retry", {}, "zh-CN"));
  await click(button(message("composer.restore")));
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
    expect(host.textContent).toContain(message("home.add_via_manager", {"name":"Android · Pixel 9"}, "zh-CN"));
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
    expect(names).toEqual(["Chrome · macOS", "Android · Pixel 9", message("security.client")]);
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
    // A computer is a card: its name, then 在线 under it, then a quota line for each CLI.
    const mac = [...host.querySelectorAll(".mach-card")].find((x) => x.querySelector(".mach-name b")?.textContent === "MacBook")!;
    expect(mac.querySelector(".s")!.textContent).toBe(message("new_session.location", {"project":"线"}));
    const codex = [...mac.querySelectorAll(".qt")].find((x) => x.querySelector("b")?.textContent === "Codex")!;
    expect(codex.querySelector(".n")!.textContent).toMatch(/^剩 \d+%$|^已用尽$|^暂未获取额度$/);
    expect(named("Chrome · macOS").querySelector(".ico svg")).toBeTruthy();
    expect(named("Android · Pixel 9").querySelector(".ico svg")).toBeTruthy();
  });
});

it("keeps task-owned approvals in attention and counts while omitting idle task sessions", () => {
 const r=demo(); const s=r.sessions.get("s-plan")!;s.info!.taskOwned=true;
 expect(todoList(r).map(s=>s.id)).toContain(s.id);
 expect(sessionCounts(r,[s])).toBe(message("projects.pending_count", {"count":1}, "zh-CN"));
 expect(projects(r.sessions.values(),r.machines.values()).find(p=>p.key===s.info!.cwd)?.sessions.map(x=>x.id)).toContain(s.id);
 s.info!.planOpen=false;s.info!.pendingApprovals=0;s.info!.pendingQuestions=0;s.info!.status=SessionStatus.IDLE;
 expect(sessionCounts(r,[s])).toBe("");
 expect(todoList(r).map(s=>s.id)).not.toContain(s.id);
});

it("sends files-only messages and restores a missing file from its original File", async () => {
  const r = demo(); const s = r.sessions.get("s-idle")!;
  r.machines.get(s.machineId)!.meta!.capabilities!.fileUpload = true;
  let uploads = 0;
  r.uploadImage = async (_m, p) => ({ imageId: "", fileId: (p as { fileName?: string }).fileName! + ++uploads, expiresMs: Number.MAX_SAFE_INTEGER });
  const sent: CommandKind[] = [];
  r.command = async (_sid, cmd) => { sent.push(cmd); throw new CommandError("not_found", "file id is not on this machine; upload it again"); };
  mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
  const source = new File(["a,b\n1,2"], "data.csv", { type: "text/csv" });
  await act(async () => {
    const picker = host.querySelector<HTMLInputElement>('input[type=file]:not([accept])')!;
    Object.defineProperty(picker, "files", { value: [source], configurable: true });
    picker.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await vi.waitFor(() => expect(sendButton()?.disabled).toBe(false));
  await click(sendButton());
  await vi.waitFor(() => expect(host.textContent).toContain(message("attachment.ios_missing")));
  expect(sent[0]).toMatchObject({ case: "send", value: { text: "", imageIds: [], fileIds: ["data.csv1"] } });
  expect(host.querySelector(".me.fail .file-chip")?.textContent).toContain("data.csv");
  await click(button(message("composer.restore")));
  const tray = r.trays.get(s.id)!;
  await vi.waitFor(() => expect(tray.fileIds()).toEqual(["data.csv2"]));
  expect(tray.files()[0].file).toBe(source);
  expect(tray.imageIds()).toEqual([]); expect(r.outbox(s.id)).toEqual([]);
});

it("creates a session with files-only and retains file IDs for an unknown retry", async () => {
  const r = demo(); const machine = r.machines.get("mac") ?? [...r.machines.values()].find((m) => m.meta?.capabilities?.imageUpload)!;
  machine.meta!.capabilities!.fileUpload = true;
  r.prepareImage = async () => ({ bytes: new Uint8Array(1), format: "png", width: 1, height: 1 });
  r.uploadImage = async () => ({ imageId: "", fileId: "file-first", expiresMs: Number.MAX_SAFE_INTEGER });
  const calls: { cmd: CommandKind; slot?: RequestSlot }[] = [];
  const real = r.machineCommand.bind(r);
  r.machineCommand = async (m: string, cmd: CommandKind, slot?: RequestSlot) => {
    if (cmd?.case !== "createSession") return real(m, cmd);
    calls.push({ cmd, slot });
    if (calls.length === 1) throw new CommandError("unknown_outcome", "连接中断");
    return create(CommandResultSchema, { kind: { case: "createSession", value: { session: { id: "new-files" } } } });
  };
  // The default demo project selects the image-capable machine.
  const go = vi.fn();
  mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={go} onBack={() => undefined} />);
  const picker = host.querySelector<HTMLInputElement>('input[type=file]:not([accept])')!;
  await act(async () => { Object.defineProperty(picker, "files", { value: [new File(["x"], "a.csv")], configurable: true }); picker.dispatchEvent(new Event("change", { bubbles: true })); });
  await vi.waitFor(() => expect(sendButton()?.disabled).toBe(false));
  await click(sendButton());
  await vi.waitFor(() => expect(calls).toHaveLength(1));
  expect(calls[0].cmd).toMatchObject({ case: "createSession", value: { prompt: "", fileIds: ["file-first"] } });
  await vi.waitFor(() => expect(host.textContent).toContain("不确定是否已经启动"));
  await click(sendButton());
  await vi.waitFor(() => expect(calls).toHaveLength(2));
  expect(calls[1].slot?.requestId).toBe(calls[0].slot?.requestId);
  expect(calls[1].cmd).toEqual(calls[0].cmd);
});

it("requires reselecting a new-session first file when its local source has been released", async () => {
  const r = demo(); const s = r.sessions.get("s-idle")!;
  r.machines.get(s.machineId)!.meta!.capabilities!.fileUpload = true;
  const file = { fileId: "first-file", name: "first.csv", sizeBytes: 7n, path: "/uploads/s/files/first/first.csv" };
  for (const kind of [
    { case: "inputState", value: { requestId: "first", status: InputStatus.QUEUED, text: "read first", files: [file] } },
    { case: "inputState", value: { requestId: "first", status: InputStatus.FAILED, reason: "write_failed" } },
  ]) r.events(s.id).push(create(EventSchema, { seq: BigInt(r.events(s.id).length + 1), origin: { kind: OriginKind.CLIENT, deviceId: r.account.deviceId }, kind: kind as never }));
  const upload = vi.spyOn(r, "uploadImage");
  mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
  await click(button(message("composer.restore"), host.querySelector(".me.fail")!));
  const tray = r.trays.get(s.id)!;
  expect(tray.files()[0].state).toBe("failed");
  expect(tray.files()[0].remoteOnly).toBe(true);
  expect(host.textContent).toContain(message("attachment.file_missing"));
  expect(sendButton()?.disabled).toBe(true);
  expect(upload).not.toHaveBeenCalled();
});

it("shows a folder-only rejection in an empty new-session tray", async () => {
  const r = demo();
  mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={() => undefined} onBack={() => undefined} />);
  const input = host.querySelector("textarea")!;
  const folder = new File([], "folder");
  const drop = new Event("drop", { bubbles: true, cancelable: true });
  Object.defineProperty(drop, "dataTransfer", { value: { files: [folder], items: [{ kind: "file", webkitGetAsEntry: () => ({ isDirectory: true }), getAsFile: () => folder }] } });
  await act(async () => input.dispatchEvent(drop));
  expect(host.textContent).toContain(message("attachment.zip_folder"));
  expect(host.querySelectorAll(".file-chip, .tthumb")).toHaveLength(0);
});

it("rejects restoring a failed file message before changing draft or dismissing it after downgrade", async () => {
  const r = demo(); const s = r.sessions.get("s-idle")!;
  const machine = r.machines.get(s.machineId)!; machine.meta!.capabilities!.fileUpload = true;
  const file = { fileId: "failed-file", name: "failed.csv", sizeBytes: 4n, path: "/uploads/s/files/failed/failed.csv", file: new File(["data"], "failed.csv") };
  r.command = async () => { throw new CommandError("unavailable", "offline"); };
  const id = r.send(s.id, "keep this text", [], [], undefined, undefined, [file]);
  await vi.waitFor(() => expect(r.outbox(s.id)[0]?.status).toBe("failed"));
  r.drafts.set(s.id, "existing draft");
  machine.meta!.capabilities!.fileUpload = false;
  const upload = vi.spyOn(r, "uploadImage");
  mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
  await click(button(message("composer.restore"), host.querySelector(".me.fail")!));
  expect(host.textContent).toContain(message("session.update_computer"));
  expect(r.drafts.get(s.id)).toBe("existing draft");
  expect(r.outbox(s.id).some(o => o.sendId === id)).toBe(true);
  expect(r.trays.get(s.id)?.items).toHaveLength(0);
  expect(upload).not.toHaveBeenCalled();
});

it.each(["retap", "outside", "typing", "escape"])("keeps the session draft focused and unsent when closing the send menu by %s", async (close) => {
  const r = demo();
  mount(<SessionPage remote={r} session={r.sessions.get("s-run")!} onBack={() => undefined} wide={false} />);
  const input = host.querySelector<HTMLTextAreaElement>(".draft")!;
  await act(async () => {
    input.focus();
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, "Keep this draft");
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  const send = sendButton()!;
  const down = new PointerEvent("pointerdown", { bubbles: true, cancelable: true });
  act(() => send.dispatchEvent(down));
  expect(down.defaultPrevented).toBe(true);
  act(() => send.dispatchEvent(new PointerEvent("pointercancel", { bubbles: true })));
  // The native context menu is the alternate route to the same long-press menu.
  act(() => send.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true })));
  await click(send); // release click from the original long press must not send
  expect(host.querySelector(".send-menu")).not.toBeNull();
  if (close === "retap") { act(() => send.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, cancelable: true }))); await click(send); }
  else if (close === "outside") {
    const mouse = new MouseEvent("mousedown", { bubbles: true, cancelable: true });
    act(() => document.body.dispatchEvent(mouse));
    expect(mouse.defaultPrevented).toBe(true);
    act(() => document.body.click());
  } else if (close === "typing") act(() => input.dispatchEvent(new InputEvent("beforeinput", { bubbles: true })));
  else act(() => window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })));
  expect(host.querySelector(".send-menu")).toBeNull();
  expect(input.value).toBe("Keep this draft");
  expect(document.activeElement).toBe(input);
});

it("hands a new session to its chat with the first prompt before its listing arrives", async () => {
  const r = demo();
  const project = "/example/handoff";
  const go = vi.fn();
  let ns!: ReturnType<typeof useNewSession>;
  function StartProbe() {
    ns = useNewSession(r, project, go, "mac");
    return null;
  }
  mount(<StartProbe />);
  await act(async () => { await Promise.all([r.listModels("mac", Agent.CODEX), r.settings("mac")]); });
  const info = create((await import("../gen/remoteagent/v1/local_pb")).SessionSchema, { id: "new-handoff", cwd: project, agent: Agent.CODEX, status: SessionStatus.STARTING });
  const command = r.machineCommand.bind(r);
  vi.spyOn(r, "machineCommand").mockImplementation((m, cmd, ...rest) => cmd?.case === "createSession"
    ? Promise.resolve(create(CommandResultSchema, { kind: { case: "createSession", value: { session: info } } })) : command(m, cmd, ...rest));
  await act(async () => { await ns.create("第一句话"); });
  expect(go).toHaveBeenCalledWith("s/new-handoff", true);
  expect(r.sessions.has(info.id)).toBe(false);
  expect(r.openingSessions.get(info.id)?.info).toEqual(info);
  await act(async () => root!.render(<SessionPage remote={r} session={r.openingSessions.get(info.id)!} onBack={() => {}} wide={false} />));
  expect(host.querySelector(".session")).toBeTruthy();
  expect(host.querySelector(".me")?.textContent).toContain("第一句话");
  expect(host.querySelector("textarea")).toBeTruthy();
});

it("shows 默认 for an unchosen effort, then sends the explicitly selected level", async () => {
  const r = demo();
  const project = "/example/default-effort";
  vi.spyOn(r, "settings").mockResolvedValue(create(DaemonSettingsSchema));
  const original = r.listModels.bind(r);
  vi.spyOn(r, "listModels").mockImplementation(async (...args) => {
    const result = await original(...args);
    return { ...result, defaultModel: "gpt-6-sol", models: result.models.map(m => ({ ...m, defaultEffort: "low" })) };
  });
  mount(<ProjectPage remote={r} pkey={project} go={() => {}} onBack={() => {}} />);
  await vi.waitFor(() => expect(host.querySelector(".project-summary")?.textContent).toBe(`GPT-6-Sol · ${message("models.default", {}, "zh-CN")}`));
  await fromMenu("新会话设置");
  expect(host.querySelector(".model-controls .fl")?.textContent).toBe(message("models.effort_default", {}, "zh-CN"));
  expect(host.querySelector(`[aria-label="${message("models.effort", {}, "zh-CN")}"] button.on`)).toBeNull();
  await click(button("GPT-6-Astra"));
  expect(host.querySelector(".project-summary")?.textContent).toBe(`GPT-6-Astra · ${message("models.default", {}, "zh-CN")}`);
  await click(button("high"));
  expect(host.querySelector(".project-summary")?.textContent).toBe("GPT-6-Astra · high");
  await click(button(message("common.done", {}, "zh-CN")));
  const real = r.machineCommand.bind(r);
  const sent: unknown[] = [];
  vi.spyOn(r, "machineCommand").mockImplementation((m, cmd, ...rest) => {
    if (cmd?.case === "createSession") sent.push(cmd.value);
    return real(m, cmd, ...rest);
  });
  await act(async () => {
    const input = host.querySelector<HTMLTextAreaElement>("textarea")!;
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, "测试强度");
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await click(sendButton());
  expect(sent).toMatchObject([{ effort: "high", model: "gpt-6-astra" }]);
});

it("keeps a named quota warning next to its card before the received time", async () => {
  const r = demo();
  const { AgentQuotaSchema, QuotaState } = await import("../gen/remoteagent/v1/remote_pb");
  const now = Date.now();
  r.machines.get("mac")!.meta!.quotas = [create(AgentQuotaSchema, { agent: Agent.CLAUDE, state: QuotaState.OK, observedMs: BigInt(now), windows: [{ name: "seven_day", usedPercent: 97, windowMinutes: 10080n, resetsAtMs: BigInt(now + 3_600_000) }] })];
  const { QuotaPage } = await import("./Settings");
  mount(<QuotaPage remote={r} id="mac" go={() => undefined} onBack={() => undefined} />);
  const text = host.textContent!;
  expect(text).toContain(message("quota.warning", {"name":"Claude Code","window":message("quota.days", { count: 7 })}));
  expect(text.indexOf("Claude Code · 7 天额度即将用尽")).toBeLessThan(text.indexOf("最近收到"));
  expect(host.querySelector(".qrow.warn .bar i")?.getAttribute("style")).toContain("3%");
});
