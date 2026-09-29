// @vitest-environment happy-dom
// Interaction regressions: the pages rendered against the demo Remote,
// clicked through as a user would.

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { DemoRemote } from "../dev/demo";
import { Agent } from "../gen/remoteagent/v1/event_pb";
import { derive, dockItems } from "../model";
import type { CommandResult } from "../gen/remoteagent/v1/remote_pb";
import { CommandError, type CommandKind, type RequestSlot } from "../remote";
import { ModeSheet, SessionSheet } from "../sheets";
import { Dock, PlanPage } from "./Dock";
import { Home } from "./Home";
import { ProjectPage } from "./Project";

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
  const plan = { $typeName: "remoteagent.v1.PlanProposed", text: "1. do it" } as const;
  mount(<PlanPage remote={r} sid="s-claude" plan={plan} title="t" onBack={() => undefined} />);
  expect(button("批准执行")).toBeTruthy();
  expect(button("清空上下文后执行")).toBeUndefined();
  act(() => root!.render(<PlanPage remote={r} sid="s-plan" plan={plan} title="t" onBack={() => undefined} />));
  expect(button("清空上下文后执行")).toBeTruthy();
});
