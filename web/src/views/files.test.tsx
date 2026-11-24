import { t as message } from "../i18n";
// @vitest-environment happy-dom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import {
  AttachmentDraftItemViewSchema,
  AttachmentDraftViewSchema,
  AttachmentItemKind,
  AttachmentItemState,
} from "../gen/remoteagent/core/v1/attachments_pb";
import {
  SessionPendingTrayViewSchema,
  SessionTrayGroupKind,
  SessionTrayGroupViewSchema,
  SessionTrayItemViewSchema,
  SessionTrayRowActivity,
  SessionTrayTone,
} from "../gen/remoteagent/core/v1/session_view_pb";
import { TextSchema } from "../gen/remoteagent/core/v1/text_pb";
import { AttachmentExecutor } from "../attachment-executor";
import { MemoryAttachmentStore } from "../attachment-store";
import { PendingTray } from "./Queue";
import { AttachButton, TrayView, useDropPaste } from "./Attach";
import { FileChip } from "./Files";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined;
let host: HTMLElement;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  vi.restoreAllMocks();
});

function mount(node: React.ReactNode) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(node));
}

function makeDraft(overrides: Parameters<typeof create<typeof AttachmentDraftViewSchema>>[1] = {}) {
  return create(AttachmentDraftViewSchema, {
    canPickImages: true,
    canPickFiles: true,
    canPickGallery: true,
    galleryAllowsVideo: true,
    galleryAcceptMime: "image/*,video/*",
    maxImages: 6,
    maxFiles: 6,
    canToggleOriginal: true,
    ...overrides,
  });
}

it("offers 照片和视频 / 文件 and forwards forceFile=true when selecting under 文件", async () => {
  const onPickFiles = vi.fn();
  const draft = makeDraft();
  mount(<AttachButton draft={draft} blocked="" onPickFiles={onPickFiles} onBlocked={() => undefined} />);
  await act(async () => host.querySelector<HTMLButtonElement>("button")!.click());
  expect([...host.querySelectorAll('[role="menuitem"]')].map((b) => b.textContent)).toEqual([
    message("attachment.photos_videos"),
    message("attachment.file"),
  ]);
  const file = new File(["bytes"], "photo.png", { type: "image/png" });
  const input = host.querySelector<HTMLInputElement>("input:not([accept])")!;
  Object.defineProperty(input, "files", { value: [file], configurable: true });
  await act(async () => input.dispatchEvent(new Event("change", { bubbles: true })));
  expect(onPickFiles).toHaveBeenCalledWith(
    [{ file, name: "photo.png", mimeType: "image/png", sizeBytes: 5 }],
    true,
  );
});

it("does not draw the hidden file inputs again when the button is drawn again", async () => {
  const draft = makeDraft();
  const onPickFiles = vi.fn();
  const onBlocked = vi.fn();
  const types = vi.spyOn(HTMLInputElement.prototype, "type", "set");
  const page = (n: number) => (
    <div data-n={n}>
      <AttachButton draft={draft} blocked="" onPickFiles={onPickFiles} onBlocked={onBlocked} />
    </div>
  );
  mount(page(0));
  const before = types.mock.calls.length;
  for (let i = 1; i <= 5; i++) await act(async () => root!.render(page(i)));
  expect(types.mock.calls.length).toBe(before);
  expect(host.querySelectorAll("input[type=file]")).toHaveLength(2);
});

it("keeps the direct image picker without file capability", async () => {
  const draft = makeDraft({ galleryAllowsVideo: false, canPickFiles: false, galleryAcceptMime: "image/*" });
  mount(<AttachButton draft={draft} blocked="" onPickFiles={() => undefined} onBlocked={() => undefined} />);
  expect(host.querySelector(`[aria-label="${message("image.add")}"]`)).toBeTruthy();
  const input = host.querySelector<HTMLInputElement>("input[accept]")!;
  expect(input.accept).toBe("image/*");
  const click = vi.spyOn(input, "click");
  await act(async () => host.querySelector<HTMLButtonElement>("button")!.click());
  expect(click).toHaveBeenCalledOnce();
  expect(host.querySelector('[role="menu"]')).toBeNull();
});

it("forwards mixed drops, paste items, and folder entries to Core candidate handler", async () => {
  const onDropEntries = vi.fn();
  function Drop() {
    const drop = useDropPaste(onDropEntries, "", () => undefined);
    return <div {...drop.props}>drop</div>;
  }
  mount(<Drop />);
  const image = new File(["png"], "x.png", { type: "image/png" });
  const csv = new File(["a,b"], "x.csv", { type: "text/csv" });
  const folder = new File([], "folder");
  const dropEvent = new Event("drop", { bubbles: true, cancelable: true });
  Object.defineProperty(dropEvent, "dataTransfer", {
    value: {
      files: [image, csv, folder],
      items: [
        { kind: "file", webkitGetAsEntry: () => ({ isDirectory: false }), getAsFile: () => image },
        { kind: "file", webkitGetAsEntry: () => ({ isDirectory: false }), getAsFile: () => csv },
        { kind: "file", webkitGetAsEntry: () => ({ isDirectory: true }), getAsFile: () => folder },
      ],
    },
  });
  await act(async () => host.firstElementChild!.dispatchEvent(dropEvent));
  expect(onDropEntries).toHaveBeenCalledWith(
    [
      { file: image, name: "x.png", mimeType: "image/png", sizeBytes: 3, isDirectory: false },
      { file: csv, name: "x.csv", mimeType: "text/csv", sizeBytes: 3, isDirectory: false },
      { file: undefined, name: "folder", mimeType: "", sizeBytes: 0, isDirectory: true },
    ],
    false,
  );
  const zip = new File(["zip"], "x.zip");
  const pasteEvent = new Event("paste", { bubbles: true, cancelable: true });
  Object.defineProperty(pasteEvent, "clipboardData", {
    value: { items: [{ kind: "file", getAsFile: () => zip }] },
  });
  await act(async () => host.firstElementChild!.dispatchEvent(pasteEvent));
  expect(onDropEntries).toHaveBeenLastCalledWith(
    [{ file: zip, name: "x.zip", mimeType: "", sizeBytes: 3 }],
    false,
  );
});

it("uses one chip for progress, pause/resume, failed retry/remove and sent files", async () => {
  const file = {
    fileId: "f",
    name: "报告 very-long-name.pdf",
    displayName: "报告 very-long-name.pdf",
    nameHead: "报告 very-long-n",
    nameTail: "ame.pdf",
    sizeText: "1.0 KB",
  };
  const retry = vi.fn();
  const remove = vi.fn();
  const pause = vi.fn();
  const resume = vi.fn();
  mount(
    <FileChip
      file={file}
      state={AttachmentItemState.FAILED}
      error="连接中断"
      canRetry
      canRemove
      onRetry={retry}
      onRemove={remove}
    />,
  );
  expect(host.textContent).toContain("1.0 KB");
  expect(host.textContent).toContain(message("attachment.upload_failed"));
  await act(async () => [...host.querySelectorAll<HTMLButtonElement>("button")].forEach((b) => b.click()));
  expect(retry).toHaveBeenCalledOnce();
  expect(remove).toHaveBeenCalledOnce();

  act(() =>
    root!.render(
      <FileChip
        file={file}
        state={AttachmentItemState.UPLOADING}
        progress={0.4}
        statusText="40%"
        canPause
        onPause={pause}
      />,
    ),
  );
  expect(host.textContent).toContain("40%");
  expect(host.querySelector("progress")?.value).toBe(0.4);
  await act(async () => host.querySelector<HTMLButtonElement>("button")!.click());
  expect(pause).toHaveBeenCalledOnce();

  act(() =>
    root!.render(
      <FileChip
        file={file}
        state={AttachmentItemState.PAUSED}
        statusText={message("attachment.upload_paused")}
        canResume
        onResume={resume}
      />,
    ),
  );
  await act(async () => host.querySelector<HTMLButtonElement>("button")!.click());
  expect(resume).toHaveBeenCalledOnce();
});

it("closes the plus menu after either choice, outside click and Escape", async () => {
  const draft = makeDraft();
  mount(<AttachButton draft={draft} blocked="" onPickFiles={() => undefined} onBlocked={() => undefined} />);
  const plus = () => host.querySelector<HTMLButtonElement>(`button[aria-label="${message("new_session.ios_attach")}"]`)!;
  const menus = () => host.querySelectorAll('[role="menu"]');
  const inputs = [...host.querySelectorAll<HTMLInputElement>('input[type="file"]')];
  const clicks = inputs.map((input) => vi.spyOn(input, "click").mockImplementation(() => undefined));
  for (const label of ["照片和视频", "文件"]) {
    await act(async () => plus().click());
    expect(menus()).toHaveLength(1);
    await act(async () =>
      [...host.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find((b) => b.textContent === label)!.click(),
    );
    expect(menus()).toHaveLength(0);
  }
  expect(clicks.every((c) => c.mock.calls.length === 1)).toBe(true);
  await act(async () => plus().click());
  await act(async () => document.body.click());
  expect(menus()).toHaveLength(0);
  await act(async () => plus().click());
  await act(async () => document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
  expect(menus()).toHaveLength(0);
});

it.each([false, true])("renders attachment indicator in pending tray row (text=%s)", (text) => {
  const item = create(SessionTrayItemViewSchema, {
    requestId: "held",
    group: SessionTrayGroupKind.STEER,
    tone: SessionTrayTone.NEUTRAL,
    activity: SessionTrayRowActivity.IDLE,
    showSpinner: true,
    text: text ? "inspect this" : "",
    attachmentCount: 1,
    fileCount: 1,
    canCancel: true,
    canRestore: true,
  });
  const trayView = create(SessionPendingTrayViewSchema, {
    totalCount: 1,
    orderedItems: [item],
    groups: [
      create(SessionTrayGroupViewSchema, {
        kind: SessionTrayGroupKind.STEER,
        tone: SessionTrayTone.NEUTRAL,
        headerText: create(TextSchema, { key: "queue.group_steer" }),
        totalCount: 1,
        items: [item],
      }),
    ],
  });
  mount(
    <PendingTray
      tray={trayView}
      onCancelItem={vi.fn()}
      onIgnoreItem={vi.fn()}
      onPromoteItem={vi.fn()}
      onRestoreItem={vi.fn()}
      onRetryItem={vi.fn()}
      onResumeItem={vi.fn()}
      onCancelAllPaused={vi.fn()}
      onUndo={vi.fn()}
    />,
  );
  expect(host.querySelectorAll(".ptray-row")).toHaveLength(1);
  expect(host.querySelector(".ptray-t .clip")?.getAttribute("aria-label")).toBe(
    message("queue.attachments_a11y", { count: 1 }),
  );
  if (text) {
    expect(host.querySelector(".ptray-t")?.textContent).toContain("inspect this");
  }
  expect(host.textContent).not.toContain(message("queue.other_message"));
});

it("draws a video among images with Core duration and falls back to filename on playback error", async () => {
  const store = new MemoryAttachmentStore();
  const executor = new AttachmentExecutor({
    store,
    onEvent: () => undefined,
  });
  const imgRef = executor.stagePickedBlob(new File(["png"], "shot.png", { type: "image/png" }));
  const vidRef = executor.stagePickedBlob(new File(["mov"], "clip.mov", { type: "video/quicktime" }));
  const imgItem = create(AttachmentDraftItemViewSchema, {
    itemId: "att-1",
    kind: AttachmentItemKind.IMAGE,
    state: AttachmentItemState.READY,
    displayName: "shot.png",
    sourceRef: imgRef,
    thumbRef: imgRef,
    imageIndex: 0,
    canRemove: true,
  });
  const vidItem = create(AttachmentDraftItemViewSchema, {
    itemId: "att-2",
    kind: AttachmentItemKind.FILE,
    isVideo: true,
    state: AttachmentItemState.READY,
    displayName: "clip.mov",
    durationText: "1:15",
    sourceRef: vidRef,
    canRemove: true,
  });
  const csvItem = create(AttachmentDraftItemViewSchema, {
    itemId: "att-3",
    kind: AttachmentItemKind.FILE,
    isVideo: false,
    state: AttachmentItemState.READY,
    displayName: "data.csv",
    nameHead: "data",
    nameTail: ".csv",
    sizeText: create(TextSchema, { literal: "3 B" }),
    canRemove: true,
  });
  const draft = makeDraft({
    original: true,
    originalTotalSizeText: create(TextSchema, { literal: " (1.2 MB)" }),
    imageCount: 1,
    fileCount: 2,
    imageCountText: create(TextSchema, { literal: "1 / 6" }),
    items: [imgItem, vidItem, csvItem],
    images: [imgItem],
    files: [vidItem, csvItem],
  });
  mount(<TrayView draft={draft} attachments={executor} />);
  await vi.waitFor(() => expect(host.querySelector(".att-tray .video video")).toBeTruthy());
  const thumbs = [...host.querySelectorAll(".att-tray .pic")];
  expect(thumbs).toHaveLength(2);
  expect(thumbs[1].classList.contains("video")).toBe(true);
  expect(thumbs[1].querySelector(".dur")?.textContent).toBe("1:15");
  expect(host.querySelector(".orig")?.textContent).toContain("(1.2 MB)");
  expect([...host.querySelectorAll(".file-chip")].map((c) => c.querySelector(".file-name")?.getAttribute("aria-label"))).toEqual([
    "data.csv",
  ]);
  await act(async () => thumbs[1].querySelector("video")!.dispatchEvent(new Event("error")));
  expect(thumbs[1].querySelector("video")).toBeNull();
  expect(thumbs[1].querySelector(".vname")?.textContent).toBe("clip.mov");
  expect(thumbs[1].querySelector(".dur")).toBeNull();
});

it.each(["same button", "outside", "textarea", "typing", "escape", "choice"])(
  "keeps composer focus when the attachment menu closes by %s",
  async (close) => {
    const draft = makeDraft();
    mount(
      <div className="cbar">
        <textarea />
        <AttachButton draft={draft} blocked="" onPickFiles={() => undefined} onBlocked={() => undefined} />
        <div className="outside">outside</div>
      </div>,
    );
    const field = host.querySelector("textarea")!;
    const plus = host.querySelector<HTMLButtonElement>(".cm-plus")!;
    field.focus();
    const down = new PointerEvent("pointerdown", { bubbles: true, cancelable: true });
    act(() => plus.dispatchEvent(down));
    expect(down.defaultPrevented).toBe(true);
    act(() => plus.click());
    const target =
      close === "same button"
        ? plus
        : close === "textarea"
          ? field
          : close === "choice"
            ? host.querySelector<HTMLElement>('[role="menuitem"]')!
            : host.querySelector<HTMLElement>(".outside")!;
    if (close === "typing") act(() => field.dispatchEvent(new InputEvent("beforeinput", { bubbles: true })));
    else if (close === "escape") act(() => window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })));
    else {
      for (const type of ["pointerdown", "mousedown"]) {
        const event = new MouseEvent(type, { bubbles: true, cancelable: true });
        act(() => target.dispatchEvent(event));
        if (target !== field) expect(event.defaultPrevented).toBe(true);
      }
      act(() => target.click());
    }
    expect(host.querySelector('[role="menu"]')).toBeNull();
    expect(document.activeElement).toBe(field);
  },
);

it("does not focus a closed keyboard when the attachment menu is used", () => {
  const draft = makeDraft();
  mount(
    <div className="cbar">
      <textarea />
      <AttachButton draft={draft} blocked="" onPickFiles={() => undefined} onBlocked={() => undefined} />
    </div>,
  );
  const plus = host.querySelector<HTMLButtonElement>(".cm-plus")!;
  const down = new PointerEvent("pointerdown", { bubbles: true, cancelable: true });
  act(() => plus.dispatchEvent(down));
  expect(down.defaultPrevented).toBe(false);
  act(() => plus.click());
  act(() => plus.click());
  expect(document.activeElement).not.toBe(host.querySelector("textarea"));
});
