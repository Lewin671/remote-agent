// @vitest-environment happy-dom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { create } from "@bufbuild/protobuf";
import { afterEach, expect, it, vi } from "vitest";
import { Agent, ApprovalDecision, ApprovalMode, EventSchema, InputStatus, SendMode, SessionStatus } from "./gen/remoteagent/v1/event_pb";
import { alwaysText, decisionText, derive, phaseOf, queuedIds } from "./model";
import { DemoRemote, ANDROID, ME } from "./dev/demo";
import { ApprovalActions } from "./views/Dock";
import { Home } from "./views/Home";
import { SessionPage } from "./views/Session";
import { useNewSession } from "./views/NewSession";
import { switchMachine } from "./model";
import { ToolGroup, ToolRow } from "./views/Tools";
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
  expect(host.textContent).toContain("已暂停");
  expect(host.textContent).toContain("等当前工具结束后并入");
  expect(host.textContent).toContain("Android");
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

it("caps home running and other projects at five", () => {
  const r = demo("many=1");
  mount(<Home remote={r} go={() => undefined} />);
  expect(host.textContent).toMatch(/还有 \d+ 个进行中/);
  expect(host.textContent).toContain("全部项目");
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
