// @vitest-environment happy-dom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { create } from "@bufbuild/protobuf";
import { afterEach, expect, it, vi } from "vitest";
import { Agent, ApprovalDecision, ApprovalMode, ApprovalRequestedSchema, EventSchema, InputStatus, SendMode, SessionStatus, ToolCallSchema, ToolKind } from "./gen/remoteagent/v1/event_pb";
import { alwaysText, decisionText, derive, phaseOf, queuedIds } from "./model";
import { DemoRemote, ANDROID, ME } from "./dev/demo";
import { ApprovalActions } from "./views/Dock";
import { Home, Sidebar } from "./views/Home";
import { SessionPage } from "./views/Session";
import { useNewSession } from "./views/NewSession";
import { switchMachine } from "./model";
import { ActiveTool, BackgroundTool, ToolGroup, ToolRow } from "./views/Tools";
import { PendingBubbles } from "./views/Queue";
import type { CommandKind } from "./remote";
import { CommandResultSchema } from "./gen/remoteagent/v1/remote_pb";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined;
let host: HTMLDivElement;
function mount(view: React.ReactNode) {
  host = document.createElement("div"); document.body.append(host);
  root = createRoot(host); act(() => root!.render(view));
}
afterEach(() => { act(() => root?.unmount()); host?.remove(); root = undefined; });
const demo = (params = "") => { const r = DemoRemote.create(new URLSearchParams(params)); r.start(); return r; };
const event = (seq: number, value: object, deviceId = ME) => create(EventSchema, { seq: BigInt(seq), kind: value as never, origin: { deviceId } });

it("tracks steer, queued, paused, held, canceled and cross-device pending inputs", () => {
  const d = derive([
    event(1, { case: "inputState", value: { requestId: "a", status: InputStatus.QUEUED, text: "one", mode: SendMode.STEER } }),
    event(2, { case: "inputState", value: { requestId: "b", status: InputStatus.QUEUED, text: "two", mode: SendMode.QUEUE, paused: true } }, ANDROID),
    event(3, { case: "inputState", value: { requestId: "c", status: InputStatus.QUEUED, text: "three", mode: SendMode.STEER } }),
    event(4, { case: "inputState", value: { requestId: "c", status: InputStatus.HELD_BY_CLI } }),
    event(5, { case: "inputState", value: { requestId: "a", status: InputStatus.CANCELED } }),
  ]);
  expect(queuedIds(d)).toEqual(["b", "c"]);
  expect(d.accepted.get("b")).toMatchObject({ text: "two", mode: SendMode.QUEUE, origin: { deviceId: ANDROID } });
  expect(d.paused.has("b")).toBe(true);
  expect(phaseOf({ status: "sent" }, InputStatus.HELD_BY_CLI)).toBe("queued");
  expect(phaseOf({ status: "sent" }, InputStatus.CANCELED)).toBe("canceled");
});

it("shows pending bubbles in place and folds more than three", () => {
  const r = demo(); const s = r.sessions.get("s-multi")!;
  mount(<PendingBubbles remote={r} session={s} d={derive(r.events(s.id), Agent.CLAUDE)} />);
  expect(host.querySelectorAll(".pending-bubble")).toHaveLength(2);
  expect(host.textContent).toContain("还有 2 条排队");
  act(() => host.querySelector<HTMLButtonElement>(".todo-more")!.click());
  expect(host.querySelectorAll(".pending-bubble")).toHaveLength(4);
  const notes = [...host.querySelectorAll(".pending-meta > span")].map((x) => x.textContent);
  // Few words for what each waits for, numbered while more than one waits.
  expect(notes.some((n) => n?.startsWith("这一轮结束后发 · 第 "))).toBe(true);
  expect(notes).toContain("已暂停");
  expect(notes).toContain("插入这一轮");
  expect(notes.join("\n")).not.toMatch(/排队 ·|等当前工具结束后并入|中断前排着的/);
  expect(host.textContent).toContain("Android");
});

it("calls a message held by the CLI an insertion even when it was queued", () => {
  const r = demo(); const s = r.sessions.get("s-multi")!;
  const d = derive([
    event(1, { case: "inputState", value: { requestId: "held", status: InputStatus.HELD_BY_CLI, text: "one", mode: SendMode.QUEUE } }),
  ]);
  mount(<PendingBubbles remote={r} session={s} d={d} />);
  expect(host.querySelector(".pending-meta > span")?.textContent).toBe("插入这一轮");
});

it("sends a queue request with its mode and retains it across retry", async () => {
  const r = demo(); const sent: CommandKind[] = [];
  r.command = async (_sid, kind) => { sent.push(kind); throw new Error("offline"); };
  const id = r.send("s-run", "next", [], [], SendMode.QUEUE);
  await vi.waitFor(() => expect(r.outbox("s-run").find((o) => o.sendId === id)?.status).toBe("failed"));
  r.retrySend("s-run", id);
  await vi.waitFor(() => expect(sent).toHaveLength(2));
  expect(sent.map((c) => c?.case === "send" ? c.value.mode : undefined)).toEqual([SendMode.QUEUE, SendMode.QUEUE]);
});

it("uses Alt+Enter for queue and Ctrl+Enter for steer while running", async () => {
  const r = demo(); const seen: SendMode[] = [];
  const real = r.command.bind(r);
  r.command = async (sid, kind, slot) => {
    if (kind?.case === "send") { seen.push(kind.value.mode ?? SendMode.UNSPECIFIED); return create(CommandResultSchema); }
    return real(sid, kind, slot);
  };
  mount(<SessionPage remote={r} session={r.sessions.get("s-run")!} onBack={() => undefined} wide={false} />);
  const input = host.querySelector<HTMLTextAreaElement>('textarea[aria-label="消息"]')!;
  const type = (value: string) => act(() => { Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, value); input.dispatchEvent(new Event("input", { bubbles: true })); });
  type("later");
  act(() => input.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Enter", altKey: true })));
  await vi.waitFor(() => expect(seen).toEqual([SendMode.QUEUE]));
  type("now");
  act(() => input.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Enter", ctrlKey: true })));
  await vi.waitFor(() => expect(seen).toEqual([SendMode.QUEUE, SendMode.STEER]));
});

it("sends the CLI always choice and translates known resolved wording", async () => {
  const r = demo(); const seen: CommandKind[] = [];
  r.command = async (_sid, kind) => { seen.push(kind); return create((await import("./gen/remoteagent/v1/remote_pb")).CommandResultSchema); };
  const a = r.events("s-appr").find((e) => e.kind.case === "approvalRequested" && e.kind.value.approvalId === "a1")!.kind;
  if (a.case !== "approvalRequested") throw new Error("fixture");
  mount(<ApprovalActions remote={r} sid="s-appr" a={a.value} agent={Agent.CODEX} />);
  act(() => [...host.querySelectorAll("button")].find((b) => b.textContent?.includes("以后不再问"))!.click());
  await vi.waitFor(() => expect(seen).toHaveLength(1));
  expect(seen[0]).toMatchObject({ case: "resolveApproval", value: { allow: true, always: true } });
  expect(decisionText(ApprovalDecision.ALLOW_ALWAYS)).toContain("以后不再问");
  expect(alwaysText("Yes, and don't ask again for commands that start with `go test`")).toBe("「go test」开头的命令以后不再问");
  expect(alwaysText("Yes, and always allow access to /w from this project")).toBe("本项目访问 /w 以后不再问");
  expect(alwaysText("Yes, and always allow access to /w")).toBe("本项目访问 /w 以后不再问");
  expect(alwaysText("Yes, and accept edits automatically")).toBe("本会话自动接受编辑");
  expect(alwaysText("other wording")).toBe("other wording");
});

it("puts only status and mode below the session title and removes input full-access warning", () => {
  const r = demo(); const s = r.sessions.get("s-idle")!;
  s.info = { ...s.info!, mode: ApprovalMode.FULL_ACCESS, status: SessionStatus.RUNNING };
  mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
  expect(host.querySelector(".navbar .title b")?.textContent).toBe(s.info.title);
  expect(host.querySelector(".navbar .title")?.textContent).toContain("完全放行");
  expect(host.querySelector(".navbar .title")?.textContent).not.toContain("Codex");
  expect(host.querySelector(".compose-card .access-warning")).toBeNull();
});

it("renders tool one-liners and a folded run without output", () => {
  const r = demo(); const short = { text: (x: string) => x, path: (x: string) => x };
  const t = r.events("s-msg").find((e) => e.kind.case === "toolCall")!.kind;
  if (t.case !== "toolCall") throw new Error("fixture");
  mount(<><ToolRow t={t.value} short={short} /><ToolGroup calls={[t.value, t.value]} short={short} /></>);
  expect(host.textContent).toContain("退出码 1");
  expect(host.textContent).toContain("运行了 2 条命令");
  expect(host.querySelector(".toolout")).toBeNull();
});

it("shows waiting approval only on its matching running tool", () => {
  const r = demo();
  const d = derive([]);
  const base = { since: Date.now() - 15_000, callId: "one", kind: ToolKind.COMMAND, itemType: "CommandExecution", seq: 1n };
  const codex = { ...base, title: "go test ./...", input: "", inputDigest: "" };
  d.approvals.set("other", create(ApprovalRequestedSchema, { approvalId: "other", command: "go build ./..." }));
  mount(<ActiveTool tool={codex} d={d} remote={r} />);
  expect(host.querySelector(".tool .muted")?.textContent).toMatch(/^已运行 0:1[0-9]$/);
  d.approvals.set("codex", create(ApprovalRequestedSchema, { approvalId: "codex", command: codex.title }));
  act(() => root!.render(<ActiveTool tool={codex} d={d} remote={r} />));
  expect(host.querySelector(".tool .muted")?.textContent).toBe("等待审批");
  d.approvals.delete("codex");
  act(() => root!.render(<ActiveTool tool={{ ...codex, title: "命令", input: "go test ./..." }} d={d} remote={r} />));
  expect(host.querySelector(".tool .muted")?.textContent).toMatch(/^已运行 0:1[0-9]$/);
  d.approvals.set("codex-input", create(ApprovalRequestedSchema, { approvalId: "codex-input", command: "go test ./..." }));
  act(() => root!.render(<ActiveTool tool={{ ...codex, title: "命令", input: "go test ./..." }} d={d} remote={r} />));
  expect(host.querySelector(".tool .muted")?.textContent).toBe("等待审批");

  const claude = { ...base, title: "npm test", input: '{"command":"npm test"}', inputDigest: "digest-one" };
  d.approvals.set("wrong-digest", create(ApprovalRequestedSchema, { approvalId: "wrong-digest", command: "npm test", inputDigest: "digest-two" }));
  act(() => root!.render(<ActiveTool tool={claude} d={d} remote={r} />));
  expect(host.querySelector(".tool .muted")?.textContent).toMatch(/^已运行 0:1[0-9]$/);
  d.approvals.set("claude", create(ApprovalRequestedSchema, { approvalId: "claude", inputDigest: "digest-one" }));
  act(() => root!.render(<ActiveTool tool={claude} d={d} remote={r} />));
  expect(host.querySelector(".tool .muted")?.textContent).toBe("等待审批");
});

it("shows background status without raw item types", () => {
  const r = demo();
  const short = { text: (x: string) => x, path: (x: string) => x };
  const call = create(ToolCallSchema, { title: "go test", itemType: "ToolCall", kind: ToolKind.COMMAND });
  mount(<BackgroundTool call={call} short={short} remote={r} />);
  expect(host.querySelector(".tool .muted")?.textContent).toBe("后台运行");
  expect(host.textContent).not.toContain("ToolCall");
  act(() => root!.render(<BackgroundTool call={create(ToolCallSchema, { itemType: "CommandExecution", kind: ToolKind.COMMAND })} short={short} remote={r} />));
  expect(host.textContent).toContain("命令");
  expect(host.textContent).not.toContain("CommandExecution");
});

it("caps home running and other projects at five", () => {
  const r = demo("many=1");
  mount(<Home remote={r} go={() => undefined} />);
  expect(host.textContent).toMatch(/还有 \d+ 个进行中/);
  expect(host.textContent).toContain("全部项目");
});

it("lists every running session in the wide sidebar, which scrolls as one column", () => {
  const r = demo("many=1");
  mount(<Sidebar remote={r} go={() => undefined} />);
  expect(host.textContent).not.toMatch(/还有 \d+ (个进行中|项待处理)/);
  expect(host.querySelector(".scroll")!.textContent).toContain("项目 7 的会话");
});

it("uses another machine only for this start when a project's saved machine is offline", () => {
  const r = demo();
  r.machines.get("mac")!.online = false;
  let ns: ReturnType<typeof useNewSession>;
  function Probe() { ns = useNewSession(r, "/Users/me/Code/foo", () => undefined); return null; }
  mount(<Probe />);
  expect(ns!.cfg.machineId).toBe("mac");
  act(() => ns!.setCfg(switchMachine(ns!.cfg, r.machines.get("vps")!), true));
  expect(ns!.cfg.machineId).toBe("vps");
  expect(r.projectPrefs().settings.find((s) => s.path === "/Users/me/Code/foo")?.machineId).toBe("mac");
});
