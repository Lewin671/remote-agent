// @vitest-environment happy-dom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { Agent, EventSchema, InputStatus, OriginKind, SendMode, SentFileSchema } from "../gen/remoteagent/v1/event_pb";
import { derive } from "../model";
import { LiveSnapshotSchema } from "../gen/remoteagent/v1/remote_pb";
import { DemoRemote } from "../dev/demo";
import { Remote } from "../remote";
import { PendingBubbles } from "./Queue";
import { Tray } from "../attach";
import { AttachButton, useDropPaste } from "./Attach";
import { FileChip, withoutFileLines } from "./Files";
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined;
let host: HTMLElement;
afterEach(() => { act(() => root?.unmount()); host?.remove(); vi.restoreAllMocks(); });
function mount(node: React.ReactNode) { host = document.createElement("div"); document.body.append(host); root = createRoot(host); act(() => root!.render(node)); }
function tray() {
  const t = new Tray(async () => ({ bytes: new Uint8Array(1), format: "png", width: 1, height: 1 }), async (_m, p) => ({ imageId: p.format === "file" ? "" : "i", fileId: p.fileName, expiresMs: Number.MAX_SAFE_INTEGER }), () => undefined);
  t.machineId = "mac"; t.fileSupported = true; return t;
}
it("offers 图片/文件, and selecting an image under 文件 never compresses it", async () => {
  const t = tray(); const prepare = vi.spyOn(t as unknown as { prepare: () => Promise<unknown> }, "prepare");
  mount(<AttachButton tray={t} blocked="" onBlocked={() => undefined} />);
  await act(async () => host.querySelector<HTMLButtonElement>("button")!.click());
  expect([...host.querySelectorAll('[role="menuitem"]')].map((b) => b.textContent)).toEqual(["图片", "文件"]);
  const file = new File(["bytes"], "photo.png", { type: "image/png" });
  const input = host.querySelector<HTMLInputElement>('input:not([accept])')!;
  Object.defineProperty(input, "files", { value: [file], configurable: true });
  await act(async () => input.dispatchEvent(new Event("change", { bubbles: true })));
  await vi.waitFor(() => expect(t.ready()).toBe(true));
  expect(t.fileIds()).toEqual(["photo.png"]); expect(t.imageIds()).toEqual([]); expect(prepare).not.toHaveBeenCalled();
});
it("keeps the old direct image picker without file capability", async () => {
  const t = tray(); t.fileSupported = false; mount(<AttachButton tray={t} blocked="" onBlocked={() => undefined} />);
  const click = vi.spyOn(host.querySelector<HTMLInputElement>('input[accept]')!, "click");
  await act(async () => host.querySelector<HTMLButtonElement>("button")!.click());
  expect(click).toHaveBeenCalledOnce(); expect(host.querySelector('[role="menu"]')).toBeNull();
});
it("routes mixed drops and non-image paste into independent trays", async () => {
  const t = tray();
  function Drop() { const drop = useDropPaste(t, "", () => undefined); return <div {...drop.props}>drop</div>; }
  mount(<Drop />);
  const image = new File(["png"], "x.png", { type: "image/png" });
  const csv = new File(["a,b"], "x.csv", { type: "text/csv" });
  const drop = new Event("drop", { bubbles: true, cancelable: true }); Object.defineProperty(drop, "dataTransfer", { value: { files: [image, csv] } });
  await act(async () => host.firstElementChild!.dispatchEvent(drop));
  const paste = new Event("paste", { bubbles: true, cancelable: true }); Object.defineProperty(paste, "clipboardData", { value: { items: [{ kind: "file", getAsFile: () => new File(["zip"], "x.zip") }] } });
  await act(async () => host.firstElementChild!.dispatchEvent(paste));
  await vi.waitFor(() => expect(t.ready()).toBe(true));
  expect(t.images()).toHaveLength(1); expect(t.files().map((f) => f.name)).toEqual(["x.csv", "x.zip"]);
});
it("uses one chip for progress, failed retry/remove and sent files", async () => {
  const file = create(SentFileSchema, { fileId: "f", name: "报告 very-long-name.pdf", sizeBytes: 1024n });
  const retry = vi.fn(); const remove = vi.fn();
  mount(<FileChip file={file} state="failed" error="连接中断" onRetry={retry} onRemove={remove} />);
  expect(host.textContent).toContain("1.0 KB"); expect(host.textContent).toContain("上传失败");
  await act(async () => [...host.querySelectorAll<HTMLButtonElement>("button")].forEach((b) => b.click()));
  expect(retry).toHaveBeenCalledOnce(); expect(remove).toHaveBeenCalledOnce();
  act(() => root!.render(<FileChip file={file} state="uploading" progress={0.4} />));
  expect(host.textContent).toContain("40%"); expect(host.querySelector("progress")?.value).toBe(0.4);
});
it("hides exact file lines and their separator only, preserving surrounding text", () => {
  const files = [{ path: "/uploads/s/files/id/data.csv" }];
  expect(withoutFileLines("hello\n\n@" + files[0].path, files)).toBe("hello");
  expect(withoutFileLines(files[0].path, files)).toBe("");
  const raw = "mention " + files[0].path + "\n" + files[0].path + " extra\n\nkeep";
  expect(withoutFileLines(raw, files)).toBe(raw);
});
it("preserves files-only shared queue metadata through later stage updates", () => {
  const file = create(SentFileSchema, { fileId: "f", name: "a.csv", path: "/uploads/s/files/f/a.csv", sizeBytes: 9n });
  const d = derive([
    create(EventSchema, { seq: 1n, origin: { kind: OriginKind.CLIENT, deviceId: "other" }, kind: { case: "inputState", value: { requestId: "r", status: InputStatus.QUEUED, files: [file] } } }),
    create(EventSchema, { seq: 2n, kind: { case: "inputState", value: { requestId: "r", status: InputStatus.QUEUED, stage: 4 } } }),
  ], Agent.CODEX);
  expect(d.accepted.get("r")?.files).toEqual([file]); expect(d.accepted.get("r")?.text).toBe("");
});

it("hides labeled file lines without hiding ordinary File: sentences", () => {
  const files = [{ path: "/uploads/s/files/id/x.png" }];
  for (const prefix of ["File: ", "File: @"]) {
    expect(withoutFileLines("\n" + prefix + files[0].path, files)).toBe("");
    expect(withoutFileLines("hello\n\n" + prefix + files[0].path, files)).toBe("hello");
    expect(withoutFileLines(prefix + files[0].path + " more", files)).toBe(prefix + files[0].path + " more");
  }
});

it("closes the plus menu after either choice, outside pointerdown and Escape", async () => {
  const t = tray(); mount(<AttachButton tray={t} blocked="" onBlocked={() => undefined} />);
  const plus = () => host.querySelector<HTMLButtonElement>('button[aria-label="添加附件"]')!;
  const menus = () => host.querySelectorAll('[role="menu"]');
  const inputs = [...host.querySelectorAll<HTMLInputElement>('input[type="file"]')];
  const clicks = inputs.map((input) => vi.spyOn(input, "click").mockImplementation(() => undefined));
  for (const label of ["图片", "文件"]) {
    await act(async () => plus().click());
    expect(menus()).toHaveLength(1);
    await act(async () => [...host.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find((b) => b.textContent === label)!.click());
    expect(menus()).toHaveLength(0);
  }
  expect(clicks.every((c) => c.mock.calls.length === 1)).toBe(true);
  await act(async () => plus().click());
  await act(async () => document.body.dispatchEvent(new Event("pointerdown", { bubbles: true })));
  expect(menus()).toHaveLength(0);
  await act(async () => plus().click());
  await act(async () => document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
  expect(menus()).toHaveLength(0);
});

it("rejects dropped folders before reading or uploading zero-byte entries", async () => {
  const t = tray(); const blocked = vi.fn();
  function Drop() { const drop = useDropPaste(t, "", blocked); return <div {...drop.props}>drop</div>; }
  mount(<Drop />);
  const folder = new File([], "folder");
  const file = new File([], "empty.txt");
  const event = new Event("drop", { bubbles: true, cancelable: true });
  Object.defineProperty(event, "dataTransfer", { value: { files: [folder, file], items: [
    { kind: "file", webkitGetAsEntry: () => ({ isDirectory: true }), getAsFile: () => folder },
    { kind: "file", webkitGetAsEntry: () => ({ isDirectory: false }), getAsFile: () => file },
  ] } });
  await act(async () => host.firstElementChild!.dispatchEvent(event));
  expect(blocked).toHaveBeenCalledWith("不能直接发送文件夹，请先压缩成 ZIP 再选择文件");
  await vi.waitFor(() => expect(t.ready()).toBe(true));
  expect(t.files().map((f) => f.name)).toEqual(["empty.txt"]);
});

it("hides the exact quoted Claude reference and preserves surrounding text", () => {
 const path="/uploads/s/files/id/笔记";
 expect(withoutFileLines(`File: @"${path}" (file)`,[{path}])).toBe("");
 expect(withoutFileLines(`hello\n\nFile: @"${path}" (file)`,[{path}])).toBe("hello");
 expect(withoutFileLines(`File: @"${path}" (file) extra`,[{path}])).toContain("extra");
});

// No QUEUED event is available in recent history. The authoritative live
// observation must supply all metadata for the shared held-input bubble.
it.each([Agent.CODEX, Agent.CLAUDE].flatMap((agent) => [false, true].map((text) => ({ agent, text }))))("renders file chips from a held-only live snapshot: %j", ({ agent, text }) => {
  const remote = DemoRemote.create(new URLSearchParams());
  const session = remote.sessions.get("s-run")!;
  session.info = { ...session.info!, agent };
  const file = create(SentFileSchema, { fileId: "held-file", name: "held.csv", sizeBytes: 9n, path: "/uploads/s/files/held-file/held.csv" });
  const held = create(EventSchema, { origin: { kind: OriginKind.CLIENT, deviceId: "other" }, kind: { case: "inputState", value: { requestId: "held", status: InputStatus.HELD_BY_CLI, text: text ? "inspect this" : "", imageIds: text ? ["image"] : [], files: [file], mode: SendMode.QUEUE } } });
  (remote as any).liveSnapshots.set(session.id, create(LiveSnapshotSchema, { actions: [held, create(EventSchema, { kind: { case: "inputState", value: { requestId: "held", status: InputStatus.HELD_BY_CLI, stage: 4 } } })] }));
  const d = Remote.prototype.currentDerived.call(remote, session.id, derive([], agent));
  mount(<PendingBubbles remote={remote} session={session} d={d} />);
  expect(host.querySelectorAll(".file-chip")).toHaveLength(1);
  expect(host.querySelector(".file-name")?.getAttribute("aria-label")).toBe("held.csv");
  expect(host.textContent).toContain("9 B");
  expect(host.querySelector(".pending-text")?.textContent).toBe(text ? "inspect this" : "");
  expect(host.textContent).not.toContain("另一客户端的消息");
  expect(d.accepted.get("held")).toMatchObject({ text: text ? "inspect this" : "", imageIds: text ? ["image"] : [], files: [file], mode: SendMode.QUEUE, origin: { deviceId: "other" } });
});

it("keeps original queued content when a held live snapshot carries truncated text", () => {
  const remote = DemoRemote.create(new URLSearchParams());
  const session = remote.sessions.get("s-run")!;
  const file = create(SentFileSchema, { fileId: "f", name: "original.csv", sizeBytes: 9n });
  const original = create(EventSchema, { origin: { kind: OriginKind.CLIENT, deviceId: "other" }, kind: { case: "inputState", value: { requestId: "r", status: InputStatus.QUEUED, text: "complete original message", files: [file], mode: SendMode.QUEUE } } });
  const historical = derive([original]);
  (remote as any).liveSnapshots.set(session.id, create(LiveSnapshotSchema, { actions: [create(EventSchema, { kind: { case: "inputState", value: { requestId: "r", status: InputStatus.HELD_BY_CLI, text: "complete… [内容已截断]", files: [file], mode: SendMode.STEER } } })] }));
  const d = Remote.prototype.currentDerived.call(remote, session.id, historical);
  expect(d.accepted.get("r")).toMatchObject({ text: "complete original message", files: [file], origin: { deviceId: "other" }, mode: SendMode.STEER });
  expect(historical.accepted.get("r")?.mode).toBe(SendMode.QUEUE);
});
