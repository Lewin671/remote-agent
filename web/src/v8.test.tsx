// @vitest-environment happy-dom
import { act, Profiler } from "react";
import { createRoot, type Root } from "react-dom/client";
import { create } from "@bufbuild/protobuf";
import { afterEach, expect, it, vi } from "vitest";
import { ApprovalMode, EventSchema, InputStatus, SendMode, SessionStatus, ToolKind } from "./gen/remoteagent/v1/event_pb";
import {
  SessionToolCallItemSchema,
  SessionToolGroupItemSchema,
  SessionToolStatus,
} from "./gen/remoteagent/core/v1/session_view_pb";
import { TextSchema } from "./gen/remoteagent/core/v1/text_pb";
import { DemoRemote, ME } from "./dev/demo";
import { ApprovalCard } from "./views/ApprovalCard";
import { Home, Sidebar } from "./views/Home";
import { SessionPage } from "./views/Session";
import { getCoreHost } from "./core/host";
import { NewBox } from "./views/NewSession";
import { patchSyncState, demoMachines } from "./test/remote-fixture";
import { RowState } from "./views/RowState";
import { ToolCallView, ToolGroupView, commandOf } from "./views/Tools";
import { t } from "./i18n";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined;
let host: HTMLDivElement;
const remotes: DemoRemote[] = [];
async function mount(view: React.ReactNode) {
  vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
  host = document.createElement("div"); document.body.append(host);
  root = createRoot(host); await act(async () => root!.render(view));
}
afterEach(() => { act(() => root?.unmount()); host?.remove(); root = undefined; for (const r of remotes.splice(0)) r.dispose(); vi.unstubAllGlobals(); });
const demo = (params = "") => { const r = DemoRemote.create(new URLSearchParams(params)); remotes.push(r); r.start(); return r; };
const event = (seq: number, value: object, deviceId = ME) => create(EventSchema, { seq: BigInt(seq), kind: value as never, origin: { deviceId } });

it("shows pending tray in place and folds more than three", async () => {
  const r = demo();
  (r as unknown as { metas: Map<string, { meta: { name: string } }> }).metas.get(
    (await import("./dev/demo")).ANDROID,
  )!.meta.name = "Android";
  await r.loadDevices();
  await mount(<SessionPage remote={r} sessionId="s-multi" onBack={() => undefined} wide={false} />);
  expect(host.querySelectorAll(".ptray-row")).toHaveLength(2);
  expect(host.textContent).toContain(t("queue.more_items", { count: 2 }));
  await act(async () => host.querySelector<HTMLButtonElement>(".ptray-more")!.click());
  expect(host.querySelectorAll(".ptray-row")).toHaveLength(4);
  const headers = [...host.querySelectorAll(".ptray-gl span")].map((x) => x.textContent ?? "");
  expect(headers).toContain(t("queue.group_steer"));
  expect(headers).toContain(t("queue.group_count", { title: t("queue.group_queue"), count: 2 }));
  expect(headers).toContain(t("queue.group_paused"));
  expect(host.querySelector(".ptray")?.textContent).toContain("Android");
});

it("uses Alt+Enter for queue and Ctrl+Enter for steer while running", async () => {
  const r = demo(); const seen: SendMode[] = [];
  const orig = r.command.bind(r);
  vi.spyOn(r, "command").mockImplementation((sid, cmd, slot) => {
    if (cmd?.case === "send") seen.push(cmd.value.mode ?? SendMode.UNSPECIFIED);
    return orig(sid, cmd, slot);
  });
  await mount(<SessionPage remote={r} sessionId="s-run" onBack={() => undefined} wide={false} />);
  const input = host.querySelector<HTMLTextAreaElement>('textarea[aria-label="消息"]')!;
  const type = (value: string) => act(() => { Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, value); input.dispatchEvent(new Event("input", { bubbles: true })); });
  type("later");
  await act(async () => input.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Enter", altKey: true })));
  expect(seen).toEqual([SendMode.QUEUE]);
  type("now");
  await act(async () => input.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Enter", ctrlKey: true })));
  expect(seen).toEqual([SendMode.QUEUE, SendMode.STEER]);
});

it("offers always-allow button and emits onResolve when clicked", async () => {
  const onResolve = vi.fn();
  await mount(
    <ApprovalCard
      kind="命令"
      body="go test ./..."
      canAlwaysAllow
      alwaysLabel="「go test」开头的命令以后不再问"
      onResolve={onResolve}
      docked
    />,
  );
  const btn = [...host.querySelectorAll("button")].find((b) => b.textContent?.includes("以后不再问"))!;
  expect(btn).toBeTruthy();
  await act(async () => btn.click());
  expect(onResolve).toHaveBeenCalledWith(true, "", true);
});

it("puts only status and mode below the session title and removes input full-access warning", async () => {
  const r = demo();
  const s = (r as unknown as { demoSessions: Map<string, { id: string; info?: { title: string; mode: ApprovalMode; status: SessionStatus } }> }).demoSessions.get("s-idle")!;
  s.info = { ...s.info!, mode: ApprovalMode.FULL_ACCESS, status: SessionStatus.RUNNING };
  r.syncDemoToCore();
  await mount(<SessionPage remote={r} sessionId="s-idle" onBack={() => undefined} wide={false} />);
  expect(host.querySelector(".navbar .title b")?.textContent).toBe(s.info.title);
  expect(host.querySelector(".navbar .title")?.textContent).toContain("完全放行");
  expect(host.querySelector(".navbar .title")?.textContent).not.toContain("Codex");
  expect(host.querySelector(".compose-card .access-warning")).toBeNull();
});

it("renders tool one-liners and a folded run without output", async () => {
  const call = create(SessionToolCallItemSchema, {
    callId: "c1",
    toolKind: ToolKind.COMMAND,
    title: "ls -la",
    status: SessionToolStatus.ERROR,
    hasExitCode: true,
    exitCode: 1,
  });
  const grp = create(SessionToolGroupItemSchema, {
    summary: create(TextSchema, { literal: "运行了 2 条命令" }),
    calls: [call, create(SessionToolCallItemSchema, { ...call, callId: "c2" })],
  });
  await mount(<><ToolCallView item={call} /><ToolGroupView group={grp} /></>);
  expect(host.textContent).toContain("退出码 1");
  expect(host.textContent).toContain("运行了 2 条命令");
  expect(host.querySelector(".toolout")).toBeNull();
});

it("shows waiting approval when awaitingApproval is true on a running tool", async () => {
  const running = create(SessionToolCallItemSchema, {
    callId: "one",
    toolKind: ToolKind.COMMAND,
    title: "go test ./...",
    status: SessionToolStatus.RUNNING,
    sinceMs: BigInt(Date.now() - 15_000),
  });
  await mount(<ToolCallView item={running} />);
  expect(host.querySelector(".tool .muted")?.textContent).toMatch(/^已运行 0:1[0-9]$/);
  await act(async () => root!.render(<ToolCallView item={running} awaitingApproval />));
  expect(host.querySelector(".tool .muted")?.textContent).toBe("等待审批");
});

it("shows background status without raw item types", async () => {
  const bg = create(SessionToolCallItemSchema, {
    callId: "bg-1",
    toolKind: ToolKind.COMMAND,
    title: "go test",
    status: SessionToolStatus.BACKGROUND,
  });
  await mount(<ToolCallView item={bg} />);
  expect(host.querySelector(".tool .muted")?.textContent).toBe("后台运行");
  expect(host.textContent).not.toContain("ToolCall");
  const unnamed = create(SessionToolCallItemSchema, {
    callId: "bg-2",
    toolKind: ToolKind.COMMAND,
    title: t("tools.command"),
    status: SessionToolStatus.BACKGROUND,
  });
  await act(async () => root!.render(<ToolCallView item={unnamed} />));
  expect(host.textContent).toContain("命令");
  expect(host.textContent).not.toContain("CommandExecution");
});

it("caps home running and other projects at five", async () => {
  const r = demo("many=1");
  await mount(<Home remote={r} go={() => undefined} />);
  expect(host.querySelector(".more-row")?.textContent).toMatch(/^还有 \d+ 个$/);
  expect(host.querySelectorAll(".sess")).toHaveLength(5);
  expect(host.querySelectorAll(".pr:not(.all)").length).toBeLessThanOrEqual(3);
  expect(host.textContent).toContain("全部项目");
});

it("lists every running session in the wide sidebar, which scrolls as one column", async () => {
  const r = demo("many=1");
  await mount(<Sidebar remote={r} go={() => undefined} />);
  expect(host.querySelector(".more-row, .pr.all")).toBeNull();
  expect(host.querySelector(".scroll")!.textContent).toContain("项目 7 的会话");
});

it("keeps saved machine when offline and offers another machine for this start", async () => {
  const r = demo();
  const nextMachines = new Map(demoMachines(r));
  const mac = nextMachines.get("mac")!;
  nextMachines.set("mac", { ...mac, online: false });
  patchSyncState(r, { machines: nextMachines });
  await mount(<NewBox remote={r} projectKey="/Users/me/Code/foo" go={() => undefined} />);
  const core = getCoreHost(r);
  expect(core.viewNewSession("/Users/me/Code/foo").config?.machineId).toBe("mac");
  expect(r.projectPrefs().settings.find((s) => s.path === "/Users/me/Code/foo")?.machineId).toBe("mac");
  const input = host.querySelector("textarea")!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, "用另一台");
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  const useVps = [...host.querySelectorAll<HTMLButtonElement>(".offline-draft .mi")].find((b) =>
    b.textContent?.includes("VPS-dev"),
  );
  expect(useVps).toBeTruthy();
  await act(async () => useVps!.click());
  expect(core.viewNewSession("/Users/me/Code/foo").config?.machineId).toBe("vps");
});

it.each([InputStatus.FAILED, InputStatus.NEEDS_TERMINAL])("does not claim delivery of failed startup input %s", async (status) => {
  const r = demo();
  const demoS = (r as unknown as { demoSessions: Map<string, { id: string; lastSeq: bigint }> }).demoSessions.get("s-idle")!;
  const text = "核实图片中的内容";
  const evs = (r as unknown as { evs: Map<string, ReturnType<typeof create<typeof EventSchema>>[]> }).evs;
  const list = evs.get("s-idle")!;
  list.splice(0, list.length,
    event(1, { case: "inputState", value: { requestId: "first", status: InputStatus.QUEUED, text, imageIds: ["image"] } }),
    event(2, { case: "inputState", value: { requestId: "first", status, reason: "input_changed" } }),
  );
  demoS.lastSeq = 2n;
  r.syncDemoToCore(true);
  await mount(<SessionPage remote={r} sessionId="s-idle" onBack={() => undefined} wide={false} />);
  const row = host.querySelector<HTMLElement>(".ptray-row")!;
  expect(row).toBeTruthy();
  expect(row.textContent).toContain("核实图片中的内容");
  await act(async () => row.querySelector<HTMLElement>(".ptray-hit")!.click());
  expect(host.textContent).toContain(t("composer.restore"));
  expect(host.textContent).not.toContain("已送达电脑");
  expect(host.querySelectorAll(".me")).toHaveLength(0);
});

it("expands original tool text, collapses it and copies the entire command", async () => {
  const text = "cat <<'EOF'\n" + "a very long line\n".repeat(20) + "EOF\n";
  const copy = vi.fn().mockResolvedValue(undefined);
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: copy } });
  const tItem = create(SessionToolCallItemSchema, {
    callId: "one",
    toolKind: ToolKind.COMMAND,
    title: "short preview",
    rawInput: JSON.stringify({ CommandLine: text }),
    status: SessionToolStatus.OK,
  });
  await mount(<ToolCallView item={tItem} />);
  const row = host.querySelector<HTMLElement>('[role="button"]')!;
  expect(row.getAttribute("aria-expanded")).toBe("false");
  expect(host.querySelector(".tool-command")).toBeNull();
  act(() => row.click());
  expect(row.getAttribute("aria-expanded")).toBe("true");
  expect(host.querySelector(".tool-command code")?.textContent).toBe(text);
  await act(async () => host.querySelector<HTMLButtonElement>(".cp")!.click());
  expect(copy).toHaveBeenCalledWith(text);
  expect(row.getAttribute("aria-expanded")).toBe("true");
  expect(host.querySelector(".cp")?.getAttribute("aria-label")).toBe("已复制");
  act(() => row.click());
  expect(host.querySelector(".tool-command")).toBeNull();
});

it("keeps independent group commands expanded through viewport remounts", async () => {
  const calls = ["one", "two"].map((callId) =>
    create(SessionToolCallItemSchema, { callId, toolKind: ToolKind.COMMAND, title: `echo /work/${callId}\nsecond line`, status: SessionToolStatus.OK }),
  );
  const grp = create(SessionToolGroupItemSchema, {
    summary: create(TextSchema, { literal: "运行了 2 条命令" }),
    calls,
  });
  const states = new Map<string, boolean>();
  const view = () => <RowState id="group" states={states}><ToolGroupView group={grp} /></RowState>;
  await mount(view());
  act(() => host.querySelector<HTMLButtonElement>(".tgrp")!.click());
  act(() => host.querySelector<HTMLElement>('.grpbody [role="button"]')!.click());
  expect(host.querySelectorAll(".tool-command")).toHaveLength(1);
  expect(host.querySelector(".tool-command code")?.textContent).toBe(calls[0]!.title);
  act(() => root!.render(null));
  act(() => root!.render(view()));
  expect(host.querySelectorAll(".tool-command")).toHaveLength(1);
  expect(host.querySelectorAll('[aria-expanded="false"]')).toHaveLength(1);
});

it("does not collapse while selecting, and expands full paths and running/background rows", async () => {
  const fileCall = create(SessionToolCallItemSchema, {
    callId: "file",
    toolKind: ToolKind.FILE_CHANGE,
    title: "/work/long/path/file.txt",
    rawInput: '{"file_path":"/work/long/path/file.txt"}',
    status: SessionToolStatus.OK,
  });
  const runningCall = create(SessionToolCallItemSchema, {
    callId: "run",
    toolKind: ToolKind.COMMAND,
    title: "exec",
    rawInput: '{"command":"sleep 20\\necho done"}',
    status: SessionToolStatus.RUNNING,
    sinceMs: BigInt(Date.now()),
  });
  const bgCall = create(SessionToolCallItemSchema, {
    callId: "background",
    toolKind: ToolKind.COMMAND,
    title: "exec",
    status: SessionToolStatus.BACKGROUND,
  });
  await mount(<><ToolCallView item={fileCall} /><ToolCallView item={runningCall} /><ToolCallView item={bgCall} /></>);
  const rows = host.querySelectorAll<HTMLElement>('[role="button"]');
  for (const row of rows) act(() => row.click());
  expect([...host.querySelectorAll(".tool-command code")].map((x) => x.textContent)).toEqual([fileCall.title, "sleep 20\necho done", "exec"]);
  const selection = vi.spyOn(window, "getSelection").mockReturnValue({ toString: () => "selected" } as Selection);
  act(() => rows[0]!.click());
  expect(rows[0]!.getAttribute("aria-expanded")).toBe("true");
  act(() => rows[0]!.dispatchEvent(new Event("pointerdown", { bubbles: true })));
  selection.mockReturnValue({ toString: () => "" } as Selection);
  act(() => rows[0]!.click());
  expect(rows[0]!.getAttribute("aria-expanded")).toBe("true");
  selection.mockRestore();
});

it("keeps raw shell quoting and falls back to the original title for unknown structured input", () => {
  expect(commandOf({ toolKind: ToolKind.COMMAND, title: '\"printf\"', rawInput: '\"printf\"' })).toBe('\"printf\"');
  expect(commandOf({ toolKind: ToolKind.COMMAND, title: "original", rawInput: "{}" })).toBe("original");
  expect(commandOf({ toolKind: ToolKind.COMMAND, title: "preview", rawInput: '"printf"' })).toBe('"printf"');
  expect(commandOf({ toolKind: ToolKind.COMMAND, title: "preview", rawInput: "true" })).toBe("true");
});

it("only renders the command being toggled within shared row state", async () => {
  const first = vi.fn(), sibling = vi.fn();
  const calls = ["one", "two"].map((callId) => create(SessionToolCallItemSchema, { callId, toolKind: ToolKind.COMMAND, title: callId, status: SessionToolStatus.OK }));
  await mount(<RowState id="row" states={new Map()}>
    <Profiler id="first" onRender={first}><ToolCallView item={calls[0]!} /></Profiler>
    <Profiler id="sibling" onRender={sibling}><ToolCallView item={calls[1]!} /></Profiler>
  </RowState>);
  const row = host.querySelector<HTMLElement>(".command-row")!;
  act(() => row.click());
  act(() => row.click());
  expect(first).toHaveBeenCalledTimes(3);
  expect(sibling).toHaveBeenCalledTimes(1);
});

it("does not synchronously measure tool rerenders, disclosures or running timer ticks", async () => {
  vi.useFakeTimers();
  const bounds = vi.spyOn(Element.prototype, "getBoundingClientRect");
  const call = create(SessionToolCallItemSchema, { callId: "one", toolKind: ToolKind.COMMAND, title: "preview", rawInput: "long command\n".repeat(40), status: SessionToolStatus.OK });
  const call2 = create(SessionToolCallItemSchema, { ...call, callId: "two" });
  const grp = create(SessionToolGroupItemSchema, { summary: create(TextSchema, { literal: "运行了 2 条命令" }), calls: [call, call2] });
  const running = create(SessionToolCallItemSchema, { callId: "run", toolKind: ToolKind.COMMAND, title: "exec", rawInput: "sleep 20", status: SessionToolStatus.RUNNING, sinceMs: BigInt(Date.now()) });
  const view = () => <><ToolCallView item={call} /><ToolGroupView group={grp} /><ToolCallView item={running} /></>;
  try {
    await mount(view());
    bounds.mockClear();
    await act(async () => root!.render(view()));
    act(() => host.querySelector<HTMLElement>(".command-row")!.click());
    act(() => host.querySelector<HTMLButtonElement>(".tgrp")!.click());
    act(() => host.querySelector<HTMLElement>(".grpbody .command-row")!.click());
    act(() => vi.advanceTimersByTime(1000));
    expect(host.querySelector(".tool.now")?.textContent).toContain("0:01");
    expect(bounds).not.toHaveBeenCalled();
  } finally { bounds.mockRestore(); vi.useRealTimers(); }
});
