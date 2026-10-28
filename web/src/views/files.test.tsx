// @vitest-environment happy-dom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { Agent, EventSchema, InputStatus, OriginKind, SentFileSchema } from "../gen/remoteagent/v1/event_pb";
import { derive } from "../model";
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
