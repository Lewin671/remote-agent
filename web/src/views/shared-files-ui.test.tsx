// @vitest-environment happy-dom
import { create } from "@bufbuild/protobuf";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { getCoreHost } from "../core/host";
import { DemoRemote } from "../dev/demo";
import {
  SessionSharedFileItemSchema,
  SharedFileCardStatus,
  SharedFileKind,
  SharedFileWebPreviewKind,
  type SessionSharedFileItem,
} from "../gen/remoteagent/core/v1/session_view_pb";
import { TextSchema } from "../gen/remoteagent/core/v1/text_pb";
import * as i18n from "../i18n";
import { saveBlobAsDownload } from "../shared-files";
import { patchSyncState, demoMachines, demoSessions } from "../test/remote-fixture";
import { SessionPage } from "./Session";
import {
  SharedFileCard,
  SharedFileCards,
  SharedFilePreviewOverlay,
  SharedFileViewContext,
} from "./SharedFiles";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const CHUNK_BYTES = 786_432;

let host: HTMLDivElement | undefined;
let root: Root | undefined;
let wideMode = false;

beforeEach(() => {
  wideMode = false;
  i18n.localeStore.overrideForTesting("zh-CN");
  vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
  Object.defineProperty(window, "matchMedia", {
    configurable: true,
    writable: true,
    value: (query: string) => ({
      matches: query.includes("900px") ? wideMode : false,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    }),
  });
  Object.defineProperty(URL, "createObjectURL", {
    configurable: true,
    writable: true,
    value: vi.fn(() => "blob:mock-url"),
  });
  Object.defineProperty(URL, "revokeObjectURL", {
    configurable: true,
    writable: true,
    value: vi.fn(),
  });
});

const remotes: DemoRemote[] = [];
afterEach(() => {
  if (root) act(() => root?.unmount());
  host?.remove();
  root = undefined;
  host = undefined;
  for (const r of remotes.splice(0)) r.dispose();
  vi.useRealTimers();
  vi.restoreAllMocks();
});

async function mount(node: React.ReactNode) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(node));
}

function makeDemo(): DemoRemote {
  const r = DemoRemote.create(new URLSearchParams("shared-files=1"), async () => {});
  remotes.push(r);
  r.start();
  return r;
}

async function coreSharedFiles(r: DemoRemote, sessionId: string): Promise<SessionSharedFileItem[]> {
  const core = getCoreHost(r);
  core.watchSession(sessionId);
  core.sessionOpen(sessionId, false, false);
  await vi.waitFor(() => {
    const items = core.viewSession(sessionId).timeline?.items ?? [];
    expect(items.some((it) => it.kind.case === "sharedFiles")).toBe(true);
  });
  const vm = core.viewSession(sessionId);
  const out: SessionSharedFileItem[] = [];
  for (const it of vm.timeline?.items ?? []) {
    if (it.kind.case === "sharedFiles") out.push(...it.kind.value.files);
  }
  return out;
}

it("renders Core shared-file card states, middle filename split, and reacts to Core download actions", async () => {
  const r = makeDemo();
  const core = getCoreHost(r);
  const offlineFiles = await coreSharedFiles(r, "s-sf-offline");
  const endedFiles = await coreSharedFiles(r, "s-sf-ended");
  await mount(
    <div>
      <SessionPage remote={r} sessionId="s-sf-states" onBack={() => undefined} wide={false} />
      <SharedFileCards remote={r} sessionId="s-sf-offline" files={offlineFiles} />
      <SharedFileCards remote={r} sessionId="s-sf-ended" files={endedFiles} />
    </div>,
  );

  await vi.waitFor(() => {
    expect(host!.querySelectorAll(".sf-fc").length).toBe(9);
    const cards = [...host!.querySelectorAll<HTMLElement>(".sf-fc")];
    expect(cards[2].querySelector(".sf-ring-play")).toBeTruthy();
    expect(cards[3].querySelector(".sf-pill")?.textContent).toBe("打开");
  });

  const cards = [...host!.querySelectorAll<HTMLElement>(".sf-fc")];
  expect(cards[0].querySelector(".sf-fs")?.textContent).toBe("视频 · 24.3 MB");
  expect(cards[2].querySelector(".sf-fs")?.textContent).toBe("已暂停 · 6.0 / 18.0 MB");
  expect(cards[5].textContent).toContain("2026-Q3-roadmap-final-v2.pdf");
  const splitSpans = cards[5].querySelectorAll(".sf-fn > span");
  expect(splitSpans.length).toBe(2);
  expect(splitSpans[0].textContent! + splitSpans[1].textContent!).toBe("2026-Q3-roadmap-final-v2.pdf");
  expect(cards[7].classList.contains("sf-off")).toBe(true);
  expect(cards[7].getAttribute("aria-disabled")).toBe("true");
  expect(cards[7].textContent).toContain("电脑不在线");
  expect(cards[8].classList.contains("sf-gone")).toBe(true);
  expect(cards[8].getAttribute("aria-disabled")).toBe("true");
  expect(cards[8].textContent).toContain("会话已结束，文件已清理");

  // Core download + pause updates ViewModel reactively.
  let holdResolve!: () => void;
  const hold = new Promise<void>((res) => {
    holdResolve = res;
  });
  vi.spyOn(r, "getSharedFileChunk").mockImplementation(async () => {
    await hold;
    return { data: new Uint8Array(CHUNK_BYTES), totalBytes: 25_480_397n };
  });
  await act(async () => {
    core.sharedFileStartDownload("s-sf-states", "sf-avail", false);
  });
  await vi.waitFor(() => {
    expect(host!.querySelectorAll<HTMLElement>(".sf-fc")[0].querySelector(".sf-ring")).toBeTruthy();
  });
  await act(async () => {
    core.sharedFilePauseDownload("s-sf-states", "sf-avail");
  });
  await vi.waitFor(() => {
    expect(host!.querySelectorAll<HTMLElement>(".sf-fc")[0].textContent).toContain("已暂停");
  });
  holdResolve();
});

it("orders the downloaded web menu as save, share… (only when canShare), separator, delete", async () => {
  const r = makeDemo();
  await getCoreHost(r).sharedFiles.whenIdle();
  let img!: SessionSharedFileItem;
  await vi.waitFor(async () => {
    img = (await coreSharedFiles(r, "s-sf-states")).find((f) => f.fileId === "sf-done-img")!;
    expect(img.status).toBe(SharedFileCardStatus.DOWNLOADED);
  });
  await mount(<SharedFileCard remote={r} sessionId="s-sf-states" file={img} />);
  await vi.waitFor(() => {
    expect(getCoreHost(r).sharedFiles.cachedBlob("s-sf-states", "sf-done-img")).toBeTruthy();
  });
  const card = host!.querySelector<HTMLElement>(".sf-fc")!;
  card.getBoundingClientRect = () => new DOMRect(20, 120, 296, 62);
  const items = () => [...host!.querySelectorAll('.sf-ctx-overlay [role="menuitem"]')].map((b) => b.textContent);
  const openClose = async () => {
    await act(async () => {
      card.dispatchEvent(new KeyboardEvent("keydown", { key: "ContextMenu", bubbles: true }));
    });
    const got = items();
    const seps = host!.querySelectorAll(".sf-ctx-overlay .sf-mgap").length;
    await act(async () => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    });
    return { got, seps };
  };

  // No navigator.canShare: no share item.
  const none = await openClose();
  expect(none.got).toEqual(["保存到本机", "删除本机副本"]);

  const canShare = vi.fn(() => true);
  Object.defineProperty(navigator, "canShare", { value: canShare, configurable: true });
  try {
    const yes = await openClose();
    expect(yes.got).toEqual(["保存到本机", "分享…", "删除本机副本"]);
    expect(canShare).toHaveBeenCalled();
    expect([none.seps, yes.seps]).toEqual([1, 1]);
    canShare.mockReturnValue(false);
    expect((await openClose()).got).toEqual(["保存到本机", "删除本机副本"]);
  } finally {
    delete (navigator as unknown as { canShare?: unknown }).canShare;
  }
});

it("handles click download/pause/open via Core ViewModel and renders video preview overlay", async () => {
  const r = makeDemo();

  await mount(<SessionPage remote={r} sessionId="s-sf-chat" onBack={() => undefined} wide={false} />);
  await vi.waitFor(() => {
    expect(host!.querySelectorAll(".sf-fc").length).toBe(1);
  });

  // Start slow download, then click again to pause via Core primaryAction.
  let holdResolve!: () => void;
  const hold = new Promise<void>((res) => {
    holdResolve = res;
  });
  vi.spyOn(r, "getSharedFileChunk").mockImplementation(async () => {
    await hold;
    return { data: new Uint8Array(CHUNK_BYTES), totalBytes: 25_480_397n };
  });

  const card = () => host!.querySelector<HTMLElement>(".sf-fc")!;
  await act(async () => card().click());
  await vi.waitFor(() => {
    expect(card().querySelector(".sf-ring")).toBeTruthy();
  });
  await act(async () => card().click());
  await vi.waitFor(() => {
    expect(card().querySelector(".sf-ring-play")).toBeTruthy();
  });
  holdResolve();

  // Clicking a downloaded card delegates to openFile.
  const openFileSpy = vi.fn();
  const vidItem = create(SessionSharedFileItemSchema, {
    fileId: "ui-vid-1",
    displayName: "clip.mp4",
    nameHead: "clip",
    nameTail: ".mp4",
    sizeBytes: 3n,
    sizeText: create(TextSchema, { literal: "3 B" }),
    displayPath: "~/clip.mp4",
    kind: SharedFileKind.VIDEO,
    status: SharedFileCardStatus.DOWNLOADED,
    primaryAction: 4, // OPEN
    primaryEnabled: true,
    primaryLabel: create(TextSchema, { key: "shared_file.action_open" }),
    webPreviewKind: SharedFileWebPreviewKind.VIDEO,
    webPreviewMime: "video/mp4",
  });
  r.sharedFileStore.seedCompleted("s-sf-chat", vidItem.fileId, new Blob(["vid"]));
  await act(async () => {
    root!.render(
      <SharedFileViewContext.Provider value={{ openFile: openFileSpy, onToast: vi.fn() }}>
        <SharedFileCard remote={r} sessionId="s-sf-chat" file={vidItem} />
      </SharedFileViewContext.Provider>,
    );
  });
  await vi.waitFor(() => {
    expect(getCoreHost(r).sharedFiles.cachedBlob("s-sf-chat", vidItem.fileId)).toBeTruthy();
  });
  await act(async () => host!.querySelector<HTMLElement>(".sf-fc")!.click());
  expect(openFileSpy).toHaveBeenCalledTimes(1);

  // Preview overlay renders <video> for video and closes on Escape key.
  const closeSpy = vi.fn();
  await act(async () => {
    root!.render(
      <SharedFilePreviewOverlay
        remote={r}
        sessionId="s-sf-chat"
        wide={false}
        target={{
          file: vidItem,
          blob: new Blob(["vid"]),
          kind: SharedFileWebPreviewKind.VIDEO,
          previewBlob: new Blob(["vid"], { type: "video/mp4" }),
        }}
        onClose={closeSpy}
      />,
    );
  });
  expect(host!.querySelector("video.sf-media-video")).toBeTruthy();
  await act(async () => {
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
  });
  expect(closeSpy).toHaveBeenCalledTimes(1);
});

it("supports phone long-press blurred overlay, wide right-click menu, keyboard ContextMenu, delete/cancel, and header-only disabled menus", async () => {
  const r = makeDemo();
  const core = getCoreHost(r);
  await core.sharedFiles.whenIdle();
  let statesFiles: SessionSharedFileItem[] = [];
  await vi.waitFor(async () => {
    statesFiles = await coreSharedFiles(r, "s-sf-states");
    expect(statesFiles.find((f) => f.fileId === "sf-done-img")?.status).toBe(SharedFileCardStatus.DOWNLOADED);
    expect(statesFiles.find((f) => f.fileId === "sf-paused")?.status).toBe(SharedFileCardStatus.PAUSED);
  });
  const fDone = statesFiles.find((f) => f.fileId === "sf-done-img")!;
  const fPaused = statesFiles.find((f) => f.fileId === "sf-paused")!;
  const fOff = (await coreSharedFiles(r, "s-sf-offline"))[0]!;

  await mount(
    <div>
      <SharedFileCard remote={r} sessionId="s-sf-states" file={fDone} />
      <SharedFileCard remote={r} sessionId="s-sf-states" file={fPaused} />
      <SharedFileCard remote={r} sessionId="s-sf-offline" file={fOff} />
    </div>,
  );
  await vi.waitFor(() => {
    expect(core.sharedFiles.cachedBlob("s-sf-states", fDone.fileId)).toBeTruthy();
  });

  vi.useFakeTimers();

  const cards = [...host!.querySelectorAll<HTMLElement>(".sf-fc")];
  cards.forEach((c) => {
    c.getBoundingClientRect = () => new DOMRect(20, 120, 296, 62);
  });

  // Phone long-press opens .sf-ctx-overlay with ~ home path, machine source, save & delete (no confirm).
  await act(async () => {
    cards[0].dispatchEvent(
      new PointerEvent("pointerdown", { bubbles: true, pointerType: "touch", clientX: 50, clientY: 140 }),
    );
    await vi.advanceTimersByTimeAsync(500);
  });
  const overlay = host!.querySelector(".sf-ctx-overlay");
  expect(overlay).toBeTruthy();
  expect(overlay!.textContent).toContain("~/Movies/daocheng/cover.png");
  expect(overlay!.textContent).toContain("来自 MacBook");
  expect(overlay!.textContent).toContain("保存到本机");

  const delBtn = [...overlay!.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find((b) =>
    b.textContent?.includes("删除本机副本"),
  )!;
  expect(delBtn).toBeTruthy();
  await act(async () => {
    delBtn.click();
  });
  await core.sharedFiles.whenIdle();
  expect((await coreSharedFiles(r, "s-sf-states")).find((f) => f.fileId === fDone.fileId)?.status).toBe(
    SharedFileCardStatus.AVAILABLE,
  );
  expect(await r.sharedFileStore.getCompletedBlob("s-sf-states", fDone.fileId)).toBeUndefined();
  expect(host!.querySelector(".sf-ctx-overlay")).toBeNull();

  // Paused card keyboard ContextMenu -> Cancel download discards partial bytes.
  await act(async () => {
    cards[1].dispatchEvent(new KeyboardEvent("keydown", { key: "ContextMenu", bubbles: true }));
  });
  const pausedOverlay = host!.querySelector(".sf-ctx-overlay")!;
  expect(pausedOverlay.textContent).toContain("继续下载");
  const cancelBtn = [...pausedOverlay.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find((b) =>
    b.textContent?.includes("取消下载"),
  )!;
  expect(cancelBtn.classList.contains("red")).toBe(true);
  await act(async () => {
    cancelBtn.click();
  });
  await core.sharedFiles.whenIdle();
  const afterCancel = (await coreSharedFiles(r, "s-sf-states")).find((f) => f.fileId === fPaused.fileId)!;
  expect(afterCancel.status).toBe(SharedFileCardStatus.AVAILABLE);
  expect(await r.sharedFileStore.readPartialBlob("s-sf-states", fPaused.fileId)).toBeUndefined();

  // Offline card opens header-only menu with 0 menuitems.
  await act(async () => {
    cards[2].dispatchEvent(new KeyboardEvent("keydown", { key: "F10", shiftKey: true, bubbles: true }));
  });
  const offOverlay = host!.querySelector(".sf-ctx-overlay")!;
  expect(offOverlay.textContent).toContain("~/Movies/daocheng/out/daocheng-final.mp4");
  expect(offOverlay.querySelectorAll('[role="menuitem"]').length).toBe(0);
  await act(async () => {
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
  });
  expect(host!.querySelector(".sf-ctx-overlay")).toBeNull();

  // Wide mode right-click uses Sheet .menu instead of .sf-ctx-overlay.
  wideMode = true;
  await act(async () => {
    root!.render(<SharedFileCard remote={r} sessionId="s-sf-offline" file={fOff} />);
  });
  const wideCard = host!.querySelector<HTMLElement>(".sf-fc")!;
  wideCard.getBoundingClientRect = () => new DOMRect(400, 200, 296, 62);
  await act(async () => {
    wideCard.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true }));
  });
  expect(host!.querySelector(".sf-ctx-overlay")).toBeNull();
  const sheetMenu = document.body.querySelector(".menu");
  expect(sheetMenu).toBeTruthy();
  expect(sheetMenu!.textContent).toContain("~/Movies/daocheng/out/daocheng-final.mp4");
  expect(sheetMenu!.textContent).toContain("来自 Mac mini");
});

it("wires SessionPage to trigger non-media download on PDF click without inline preview", async () => {
  const r = DemoRemote.create(new URLSearchParams("shared-files=1"), async () => {});
  remotes.push(r);
  r.sharedFileStore.seedCompleted("s-sf-states", "sf-pdf", new Blob([new Uint8Array(4_404_019)]));
  r.start();
  const s = demoSessions(r).get("s-sf-states")!;
  expect(s).toBeTruthy();

  const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});

  await mount(<SessionPage remote={r} sessionId={s.id} onBack={() => undefined} wide={false} />);

  await vi.waitFor(() => {
    const btn = [...host!.querySelectorAll<HTMLElement>(".sf-fc")].find((el) =>
      el.textContent?.includes("2026-Q3-roadmap-final-v2.pdf"),
    );
    expect(btn?.querySelector(".sf-pill")?.textContent).toBe("打开");
    expect(getCoreHost(r).sharedFiles.cachedBlob("s-sf-states", "sf-pdf")).toBeTruthy();
  });

  const pdfCard = [...host!.querySelectorAll<HTMLElement>(".sf-fc")].find((el) =>
    el.textContent?.includes("2026-Q3-roadmap-final-v2.pdf"),
  )!;
  await act(async () => pdfCard.click());
  expect(clickSpy).toHaveBeenCalledTimes(1);
  expect(host!.querySelector(".viewer")).toBeNull();
  expect(host!.querySelector(".lightbox")).toBeNull();

  // Turning off machine sharedFiles capability falls back to standard ToolRow.
  const nextMachines = new Map(demoMachines(r));
  const mac = nextMachines.get("mac")!;
  mac.meta!.capabilities!.sharedFiles = false;
  patchSyncState(r, { machines: nextMachines });
  await act(async () => {
    root!.render(<SessionPage remote={r} sessionId={s.id} onBack={() => undefined} wide={false} />);
  });
  expect(host!.querySelector(".sf-fc")).toBeNull();
});

it("web security: saves always as application/octet-stream, previews only Core-approved types with Core MIME, never opens a new tab", async () => {
  const r = makeDemo();
  const openSpy = vi.spyOn(window, "open").mockImplementation(() => null);
  const created = URL.createObjectURL as unknown as ReturnType<typeof vi.fn>;
  const clicked: HTMLAnchorElement[] = [];
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
    clicked.push(this);
  });
  const lastBlobType = () => (created.mock.calls.at(-1)?.[0] as Blob).type;

  // Direct save: the blob's own type (even image/svg+xml or text/html) is never kept.
  created.mockClear();
  saveBlobAsDownload(new Blob(["<svg onload=alert(1)>"], { type: "image/svg+xml" }), "evil.svg");
  expect(lastBlobType()).toBe("application/octet-stream");
  expect(clicked.at(-1)?.download).toBe("evil.svg");
  expect(clicked.at(-1)?.getAttribute("target")).toBeNull();
  saveBlobAsDownload(new Blob(["<html>"], { type: "text/html" }), "page.html");
  expect(lastBlobType()).toBe("application/octet-stream");

  // Image overlay "save" goes through the octet-stream path, not the image/* preview blob.
  const png = create(SessionSharedFileItemSchema, {
    fileId: "sec-png",
    displayName: "cover.png",
    sizeBytes: 3n,
    sizeText: create(TextSchema, { literal: "3 B" }),
    displayPath: "~/cover.png",
    kind: SharedFileKind.IMAGE,
    status: SharedFileCardStatus.DOWNLOADED,
    webPreviewKind: SharedFileWebPreviewKind.IMAGE,
    webPreviewMime: "image/png",
  });
  await mount(
    <SharedFilePreviewOverlay
      remote={r}
      sessionId="s-sf-chat"
      wide={false}
      target={{
        file: png,
        blob: new Blob(["png"]),
        kind: SharedFileWebPreviewKind.IMAGE,
        previewBlob: new Blob(["png"], { type: "image/png" }),
      }}
      onClose={() => undefined}
    />,
  );
  const saveBtn = [...host!.querySelectorAll("button")].find((b) => b.textContent === i18n.t("common.save"))!;
  expect(saveBtn).toBeTruthy();
  const before = clicked.length;
  created.mockClear();
  await act(async () => saveBtn.click());
  expect(clicked.length).toBe(before + 1);
  expect(lastBlobType()).toBe("application/octet-stream");
  expect(clicked.at(-1)?.getAttribute("target")).toBeNull();

  // Video overlay: media element source is a blob of Core's preview MIME; saving is octet-stream.
  const vid = create(SessionSharedFileItemSchema, {
    fileId: "sec-vid",
    displayName: "clip.mp4",
    sizeBytes: 3n,
    sizeText: create(TextSchema, { literal: "3 B" }),
    displayPath: "~/clip.mp4",
    kind: SharedFileKind.VIDEO,
    status: SharedFileCardStatus.DOWNLOADED,
    webPreviewKind: SharedFileWebPreviewKind.VIDEO,
    webPreviewMime: "video/mp4",
  });
  created.mockClear();
  await act(async () => {
    root!.render(
      <SharedFilePreviewOverlay
        remote={r}
        sessionId="s-sf-chat"
        wide={false}
        target={{
          file: vid,
          blob: new Blob(["v"], { type: "text/html" }),
          kind: SharedFileWebPreviewKind.VIDEO,
          previewBlob: new Blob(["v"], { type: "video/mp4" }),
        }}
        onClose={() => undefined}
      />,
    );
  });
  expect(lastBlobType()).toBe("video/mp4");
  const vsave = [...host!.querySelectorAll("button")].find((b) => b.textContent === "保存到本机")!;
  created.mockClear();
  await act(async () => vsave.click());
  expect(lastBlobType()).toBe("application/octet-stream");

  expect(openSpy).not.toHaveBeenCalled();
  expect(clicked.every((a) => a.getAttribute("target") === null)).toBe(true);
});
