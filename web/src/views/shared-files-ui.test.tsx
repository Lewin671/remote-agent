// @vitest-environment happy-dom
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  EventSchema,
  SharedFileSchema,
  ToolKind,
  TurnOutcome,
  type Event,
  type SharedFile,
} from "../gen/remoteagent/v1/event_pb";
import { DemoRemote } from "../dev/demo";
import * as i18n from "../i18n";
import { derive, timeline } from "../model";
import {
  DEFAULT_MAX_SHARED_FILE_BYTES,
  DEFAULT_SHARED_FILE_CHUNK_BYTES,
  previewBlobForFile,
} from "../shared-files";
import { SessionPage } from "./Session";
import {
  SharedFileCard,
  SharedFileCards,
  SharedFilePreviewOverlay,
  SharedFileViewContext,
} from "./SharedFiles";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement | undefined;
let root: Root | undefined;
let wideMode = false;

beforeEach(() => {
  wideMode = false;
  i18n.localeStore.overrideForTesting("zh-CN");
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

afterEach(() => {
  if (root) act(() => root?.unmount());
  host?.remove();
  root = undefined;
  host = undefined;
  vi.useRealTimers();
  vi.restoreAllMocks();
});

async function mount(node: React.ReactNode) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(node));
}

function makeEvent(seq: number, kind: MessageInitShape<typeof EventSchema>["kind"]): Event {
  return create(EventSchema, {
    seq: BigInt(seq),
    sessionId: "s-test",
    time: timestampFromMs(1_700_000_000_000 + seq * 1000),
    kind,
  });
}

function makeDemo(): DemoRemote {
  const r = DemoRemote.create(new URLSearchParams("shared-files=1"), async () => {});
  r.start();
  return r;
}

it("splits ToolGroups around completed send_file calls and falls back when capability is missing or call failed", () => {
  const f1 = create(SharedFileSchema, {
    fileId: "f-split-1",
    name: "cut.mp4",
    sizeBytes: 1024n,
    path: "/tmp/cut.mp4",
  });
  const events: Event[] = [
    makeEvent(1, { case: "turnStarted", value: {} }),
    makeEvent(2, {
      case: "toolCall",
      value: { callId: "c1a", kind: ToolKind.COMMAND, title: "ls", status: "completed", exitCode: 0 },
    }),
    makeEvent(3, {
      case: "toolCall",
      value: { callId: "c1b", kind: ToolKind.COMMAND, title: "stat cut.mp4", status: "completed", exitCode: 0 },
    }),
    makeEvent(4, {
      case: "toolCall",
      value: {
        callId: "c2",
        kind: ToolKind.OTHER,
        title: "send_file",
        status: "completed",
        exitCode: 0,
        sharedFiles: [f1],
      },
    }),
    makeEvent(5, {
      case: "toolCall",
      value: { callId: "c3a", kind: ToolKind.COMMAND, title: "pwd", status: "completed", exitCode: 0 },
    }),
    makeEvent(6, {
      case: "toolCall",
      value: { callId: "c3b", kind: ToolKind.COMMAND, title: "echo done", status: "completed", exitCode: 0 },
    }),
    makeEvent(7, { case: "turnEnded", value: { durationMs: 1000n, outcome: TurnOutcome.COMPLETED } }),
  ];

  const withCap = timeline(events, derive(events), { sharedFiles: true });
  expect(withCap.map((x) => x.kind)).toEqual(["tools", "event", "tools", "turn"]);
  expect(withCap[1].kind === "event" && withCap[1].event.kind.case === "toolCall" && withCap[1].event.kind.value.sharedFiles.length).toBe(1);

  const withoutCap = timeline(events, derive(events), { sharedFiles: false });
  expect(withoutCap.map((x) => x.kind)).toEqual(["tools", "turn"]);

  const failedEvents: Event[] = [
    events[0],
    events[1],
    events[2],
    makeEvent(4, {
      case: "toolCall",
      value: {
        callId: "c2",
        kind: ToolKind.OTHER,
        title: "send_file",
        status: "failed",
        exitCode: 1,
        sharedFiles: [f1],
      },
    }),
    events[4],
    events[5],
    events[6],
  ];
  const failedTl = timeline(failedEvents, derive(failedEvents), { sharedFiles: true });
  expect(failedTl.map((x) => x.kind)).toEqual(["tools", "event", "tools", "turn"]);
  // Failed toolCall stays a standalone ToolRow event (not folded into completed tools),
  // and SessionPage Row skips SharedFileCards when status !== "completed" or capability is false.
});

it("renders all 8 card states, middle filename split, and shared state for duplicate fileId", async () => {
  const r = makeDemo();
  const maxBytes = DEFAULT_MAX_SHARED_FILE_BYTES;
  const files = [
    "sf-avail",
    "sf-dl",
    "sf-paused",
    "sf-done-vid",
    "sf-done-img",
    "sf-pdf",
    "sf-fail",
    "sf-large",
  ].map((id) => {
    const evs = r.events("s-sf-states");
    for (const e of evs) {
      if (e.kind.case === "toolCall") {
        const hit = e.kind.value.sharedFiles.find((f) => f.fileId === id);
        if (hit) return hit;
      }
    }
    throw new Error("missing fixture file " + id);
  });

  await mount(
    <div>
      <SharedFileCards
        remote={r}
        sessionId="s-sf-states"
        files={files}
        machineName="Mac"
        machineHome="/Users/me"
        maxBytes={maxBytes}
      />
      <SharedFileCard
        remote={r}
        sessionId="s-sf-offline"
        file={create(SharedFileSchema, { fileId: "sf-off-1", name: "a.mp4", sizeBytes: 100n, path: "/a.mp4" })}
        machineName="Mac mini"
        maxBytes={maxBytes}
      />
      <SharedFileCard
        remote={r}
        sessionId="s-sf-ended"
        file={create(SharedFileSchema, { fileId: "sf-gone-1", name: "b.mp4", sizeBytes: 100n, path: "/b.mp4" })}
        machineName="Mac"
        maxBytes={maxBytes}
      />
      <SharedFileCard
        remote={r}
        sessionId="s-sf-states"
        file={files[0]}
        machineName="Mac"
        maxBytes={maxBytes}
      />
    </div>,
  );

  const cards = [...host!.querySelectorAll<HTMLElement>(".sf-fc")];
  expect(cards.length).toBe(11);

  // Check expected state texts.
  expect(cards[0].querySelector(".sf-fs")?.textContent).toBe("视频 · 24.3 MB");
  expect(cards[1].querySelector(".sf-ring")).toBeTruthy();
  expect(cards[1].textContent).toContain("剩 12 秒");
  expect(cards[2].querySelector(".sf-ring-play")).toBeTruthy();
  expect(cards[2].textContent).toContain("已暂停");
  expect(cards[3].querySelector(".sf-pill")?.textContent).toBe("打开");
  expect(cards[5].textContent).toContain("2026-Q3-roadmap-final-v2.pdf");
  const splitSpans = cards[5].querySelectorAll(".sf-fn > span");
  expect(splitSpans.length).toBe(2);
  expect(splitSpans[0].textContent! + splitSpans[1].textContent!).toBe("2026-Q3-roadmap-final-v2.pdf");
  expect(cards[6].classList.contains("sf-fail")).toBe(true);
  expect(cards[6].querySelector(".sf-fs")?.textContent).toBe("没下载下来，连接断了");
  expect(cards[6].querySelector(".sf-pill")?.textContent).toBe("重试");
  expect(cards[7].classList.contains("sf-off")).toBe(true);
  expect(cards[7].getAttribute("aria-disabled")).toBe("true");
  expect(cards[7].textContent).toContain("文件超过 100 MB，请在电脑端直接查看");
  expect(cards[8].classList.contains("sf-off")).toBe(true);
  expect(cards[8].getAttribute("aria-disabled")).toBe("true");
  expect(cards[8].textContent).toContain("电脑不在线");
  expect(cards[9].classList.contains("sf-gone")).toBe(true);
  expect(cards[9].getAttribute("aria-disabled")).toBe("true");
  expect(cards[9].textContent).toContain("会话已结束，文件已清理");

  // Duplicate fileId card shares seeded/mutated state.
  await act(async () => {
    r.sharedFiles.seedState("s-sf-states", "sf-avail", { status: "paused", downloadedBytes: 1234 });
  });
  expect(cards[0].textContent).toContain("已暂停");
  expect(cards[10].textContent).toContain("已暂停");
});

it("handles click transitions, auto-open only while staying in session, and opens image/video/non-media safely", async () => {
  const r = makeDemo();
  const fVid = create(SharedFileSchema, {
    fileId: "ui-vid-1",
    name: "clip.mp4",
    sizeBytes: 100n,
    path: "/Users/me/clip.mp4",
  });
  vi.spyOn(r, "getSharedFileChunk").mockImplementation(async (_sid, _id, offset) => ({
    data: offset === 0n ? new Uint8Array(DEFAULT_SHARED_FILE_CHUNK_BYTES) : new Uint8Array(0),
    totalBytes: 100n,
  }));

  const opened: SharedFile[] = [];
  r.sharedFiles.setCurrentSession("s-sf-chat");
  r.sharedFiles.setAutoOpenHandler((_sid, file) => opened.push(file));

  const openFileSpy = vi.fn();
  await mount(
    <SharedFileViewContext.Provider value={{ openFile: openFileSpy, onToast: vi.fn() }}>
      <SharedFileCard
        remote={r}
        sessionId="s-sf-chat"
        file={fVid}
        machineName="Mac"
        machineHome="/Users/me"
        maxBytes={104_857_600n}
      />
    </SharedFileViewContext.Provider>,
  );

  const card = host!.querySelector<HTMLElement>(".sf-fc")!;
  await act(async () => card.click());
  await vi.waitFor(() => {
    expect(r.sharedFiles.stateOf("s-sf-chat", fVid).status).toBe("downloaded");
  });
  expect(opened.map((f) => f.fileId)).toEqual(["ui-vid-1"]);

  // Clicking downloaded card delegates to openFile.
  await act(async () => card.click());
  expect(openFileSpy).toHaveBeenCalledTimes(1);

  // Switching session before completion suppresses auto-open.
  const fVid2 = create(SharedFileSchema, {
    fileId: "ui-vid-2",
    name: "clip2.mp4",
    sizeBytes: 100n,
    path: "/Users/me/clip2.mp4",
  });
  let resolveChunk!: (v: { data: Uint8Array<ArrayBuffer>; totalBytes: bigint }) => void;
  vi.spyOn(r, "getSharedFileChunk").mockImplementation(
    () =>
      new Promise((res) => {
        resolveChunk = res;
      }),
  );
  await act(async () => {
    void r.sharedFiles.startDownload("s-sf-chat", fVid2, { autoOpenIfStaying: true });
  });
  expect(r.sharedFiles.stateOf("s-sf-chat", fVid2).status).toBe("downloading");
  r.sharedFiles.setCurrentSession("s-other");
  await act(async () => resolveChunk({ data: new Uint8Array(DEFAULT_SHARED_FILE_CHUNK_BYTES), totalBytes: 100n }));
  await vi.waitFor(() => {
    expect(r.sharedFiles.stateOf("s-sf-chat", fVid2).status).toBe("downloaded");
  });
  expect(opened.map((f) => f.fileId)).toEqual(["ui-vid-1"]);

  // Pause & cancel transitions on in-flight card.
  let holdResolve!: () => void;
  const hold = new Promise<void>((res) => {
    holdResolve = res;
  });
  const fSlow = create(SharedFileSchema, {
    fileId: "ui-slow",
    name: "slow.zip",
    sizeBytes: 1_600_000n,
    path: "/Users/me/slow.zip",
  });
  vi.spyOn(r, "getSharedFileChunk").mockImplementation(async () => {
    await hold;
    return { data: new Uint8Array(DEFAULT_SHARED_FILE_CHUNK_BYTES), totalBytes: 1_600_000n };
  });

  await act(async () => {
    root!.render(
      <SharedFileViewContext.Provider value={{ openFile: openFileSpy, onToast: vi.fn() }}>
        <SharedFileCard
          remote={r}
          sessionId="s-sf-chat"
          file={fSlow}
          machineName="Mac"
          machineHome="/Users/me"
          maxBytes={104_857_600n}
        />
      </SharedFileViewContext.Provider>,
    );
  });
  await act(async () => host!.querySelector<HTMLElement>(".sf-fc")!.click());
  expect(r.sharedFiles.stateOf("s-sf-chat", fSlow).status).toBe("downloading");
  await act(async () => host!.querySelector<HTMLElement>(".sf-fc")!.click());
  expect(r.sharedFiles.stateOf("s-sf-chat", fSlow).status).toBe("paused");
  holdResolve();

  // Security whitelist: image/video preview allowed, HTML/SVG/PDF rejected for inline preview -> octet-stream download.
  expect(previewBlobForFile(new Blob(["<img>"]), "evil.svg")).toBeUndefined();
  expect(previewBlobForFile(new Blob(["<html>"]), "page.html")).toBeUndefined();
  expect(previewBlobForFile(new Blob(["%PDF"]), "doc.pdf")).toBeUndefined();
  expect(previewBlobForFile(new Blob(["png"]), "photo.png")?.kind).toBe("image");
  expect(previewBlobForFile(new Blob(["mp4"]), "movie.mp4")?.kind).toBe("video");
  expect(previewBlobForFile(new Blob(["mp3"]), "track.mp3")?.kind).toBe("audio");

  // Preview overlay renders <video> for video and escapes on Escape key.
  const closeSpy = vi.fn();
  await act(async () => {
    root!.render(
      <SharedFilePreviewOverlay
        remote={r}
        sessionId="s-sf-chat"
        agentName="Claude"
        machineHome="/Users/me"
        wide={false}
        target={{
          file: fVid,
          blob: new Blob(["vid"]),
          kind: "video",
          previewBlob: new Blob(["vid"], { type: "video/mp4" }),
          path: fVid.path,
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
  vi.useFakeTimers();
  const r = makeDemo();
  const maxBytes = 104_857_600n;
  const fDone = create(SharedFileSchema, {
    fileId: "sf-menu-done",
    name: "cover.png",
    sizeBytes: 100n,
    path: "/Users/me/cover.png",
  });
  r.sharedFiles.seedDownloaded("s-sf-chat", "sf-menu-done", new Blob(["png"], { type: "image/png" }));

  const fPaused = create(SharedFileSchema, {
    fileId: "sf-menu-paused",
    name: "part.zip",
    sizeBytes: 1000n,
    path: "/Users/me/part.zip",
  });
  r.sharedFiles.seedState("s-sf-chat", "sf-menu-paused", { status: "paused", downloadedBytes: 400 });

  const fOff = create(SharedFileSchema, {
    fileId: "sf-off-1",
    name: "off.mp4",
    sizeBytes: 100n,
    path: "/Users/me/off.mp4",
  });

  await mount(
    <div>
      <SharedFileCard
        remote={r}
        sessionId="s-sf-chat"
        file={fDone}
        machineName="MacBook Pro"
        machineHome="/Users/me"
        maxBytes={maxBytes}
      />
      <SharedFileCard
        remote={r}
        sessionId="s-sf-chat"
        file={fPaused}
        machineName="MacBook Pro"
        machineHome="/Users/me"
        maxBytes={maxBytes}
      />
      <SharedFileCard
        remote={r}
        sessionId="s-sf-offline"
        file={fOff}
        machineName="Mac mini"
        machineHome="/Users/me"
        maxBytes={maxBytes}
      />
    </div>,
  );

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
  expect(overlay!.textContent).toContain("~/cover.png");
  expect(overlay!.textContent).toContain("来自 MacBook Pro");
  expect(overlay!.textContent).toContain("保存到本机");

  const delBtn = [...overlay!.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find((b) =>
    b.textContent?.includes("删除本机副本"),
  )!;
  expect(delBtn).toBeTruthy();
  await act(async () => {
    delBtn.click();
  });
  await vi.waitFor(() => {
    expect(r.sharedFiles.stateOf("s-sf-chat", fDone).status).toBe("available");
  });
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
  await act(async () => {
    cancelBtn.click();
  });
  await vi.waitFor(() => {
    expect(r.sharedFiles.stateOf("s-sf-chat", fPaused).status).toBe("available");
    expect(r.sharedFiles.stateOf("s-sf-chat", fPaused).downloadedBytes).toBe(0);
  });

  // Offline card opens header-only menu with 0 menuitems.
  await act(async () => {
    cards[2].dispatchEvent(new KeyboardEvent("keydown", { key: "F10", shiftKey: true, bubbles: true }));
  });
  const offOverlay = host!.querySelector(".sf-ctx-overlay")!;
  expect(offOverlay.textContent).toContain("~/off.mp4");
  expect(offOverlay.querySelectorAll('[role="menuitem"]').length).toBe(0);
  await act(async () => {
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
  });
  expect(host!.querySelector(".sf-ctx-overlay")).toBeNull();

  // Wide mode right-click uses Sheet .menu instead of .sf-ctx-overlay.
  wideMode = true;
  await act(async () => {
    root!.render(
      <SharedFileCard
        remote={r}
        sessionId="s-sf-offline"
        file={fOff}
        machineName="Mac mini"
        machineHome="/Users/me"
        maxBytes={maxBytes}
      />,
    );
  });
  const wideCard = host!.querySelector<HTMLElement>(".sf-fc")!;
  wideCard.getBoundingClientRect = () => new DOMRect(400, 200, 296, 62);
  await act(async () => {
    wideCard.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true }));
  });
  expect(host!.querySelector(".sf-ctx-overlay")).toBeNull();
  const sheetMenu = document.body.querySelector(".menu");
  expect(sheetMenu).toBeTruthy();
  expect(sheetMenu!.textContent).toContain("~/off.mp4");
  expect(sheetMenu!.textContent).toContain("来自 Mac mini");
});

it("wires SessionPage to trigger non-media download on PDF click without inline preview", async () => {
  vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
  const r = makeDemo();
  const s = r.sessions.get("s-sf-states")!;
  expect(s).toBeTruthy();

  const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
  const pdf = r.events("s-sf-states")
    .flatMap((e) => (e.kind.case === "toolCall" ? e.kind.value.sharedFiles : []))
    .find((f) => f.fileId === "sf-pdf")!;
  r.sharedFiles.seedDownloaded("s-sf-states", pdf.fileId, new Blob(["%PDF-1.4"]));

  await mount(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);

  await vi.waitFor(() => {
    const btn = [...host!.querySelectorAll<HTMLElement>(".sf-fc")].find((el) =>
      el.textContent?.includes("2026-Q3-roadmap-final-v2.pdf"),
    );
    expect(btn).toBeTruthy();
  });

  const pdfCard = [...host!.querySelectorAll<HTMLElement>(".sf-fc")].find((el) =>
    el.textContent?.includes("2026-Q3-roadmap-final-v2.pdf"),
  )!;
  await act(async () => pdfCard.click());
  expect(clickSpy).toHaveBeenCalledTimes(1);
  expect(host!.querySelector(".viewer")).toBeNull();
  expect(host!.querySelector(".lightbox")).toBeNull();

  // Turning off machine sharedFiles capability falls back to standard ToolRow.
  r.machines.get("mac")!.meta!.capabilities!.sharedFiles = false;
  await act(async () => {
    root!.render(<SessionPage remote={r} session={s} onBack={() => undefined} wide={false} />);
  });
  expect(host!.querySelector(".sf-fc")).toBeNull();
});
