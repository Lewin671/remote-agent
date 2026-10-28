import { describe, expect, it, vi } from "vitest";
import { Tray, type Prepared } from "./attach";

const png = (n = 10): Prepared => ({ bytes: new Uint8Array(n), format: "png", width: 2, height: 1 });
const file = (name: string) => new Blob([name], { type: "image/png" });

function setup(opts: { failUpload?: (n: number) => boolean; slow?: boolean } = {}) {
  const uploads: { machine: string; n: number }[] = [];
  let n = 0;
  const prepare = vi.fn(async (_f: Blob, original: boolean) => png(original ? 100 : 10));
  const upload = vi.fn(async (machine: string, p: Prepared, progress: (d: number, t: number) => void) => {
    const i = ++n;
    uploads.push({ machine, n: p.bytes.length });
    if (opts.slow) await new Promise((r) => setTimeout(r, 30));
    progress(p.bytes.length, p.bytes.length);
    if (opts.failUpload?.(i)) throw new Error("连接超时");
    return { imageId: "id" + i, expiresMs: Number.MAX_SAFE_INTEGER };
  });
  const tray = new Tray(prepare, upload, () => undefined);
  tray.machineId = "mac";
  return { tray, uploads, prepare, upload };
}

describe("images waiting to be sent", () => {
  it("takes at most six, in the order chosen, and says how many were left out", async () => {
    const { tray } = setup();
    tray.add([1, 2, 3, 4].map((i) => file("a" + i)));
    tray.add([5, 6, 7, 8].map((i) => file("b" + i)));
    expect(tray.items.length).toBe(6);
    expect(tray.note).toBe("一条消息最多 6 张，多选的 2 张没有加入");
    await vi.waitFor(() => expect(tray.ready()).toBe(true));
    expect(tray.imageIds()).toEqual(["id1", "id2", "id3", "id4", "id5", "id6"]);
    expect(tray.full()).toBe(true);
  });

  it("is not ready to send while one failed, and retries that one", async () => {
    const { tray } = setup({ failUpload: (i) => i === 2 });
    tray.add([file("a"), file("b")]);
    await vi.waitFor(() => expect(tray.items.every((i) => i.state === "done" || i.state === "failed")).toBe(true));
    expect(tray.ready()).toBe(false);
    expect(tray.items[1]).toMatchObject({ state: "failed", error: "连接超时" });
    tray.retry(tray.items[1].key);
    await vi.waitFor(() => expect(tray.ready()).toBe(true));
    expect(tray.imageIds()).toEqual(["id1", "id3"]);
  });

  it("uploads again to another machine, without choosing the images again", async () => {
    const { tray, uploads, prepare } = setup();
    tray.add([file("a")]);
    await vi.waitFor(() => expect(tray.ready()).toBe(true));
    tray.retarget("vps");
    expect(tray.ready()).toBe(false);
    await vi.waitFor(() => expect(tray.ready()).toBe(true));
    expect(uploads.map((u) => u.machine)).toEqual(["mac", "vps"]);
    expect(prepare).toHaveBeenCalledTimes(1);
  });

  it("prepares the originals again when 原图 is switched, and drops stale results", async () => {
    const { tray, uploads } = setup({ slow: true });
    tray.add([file("a")]);
    tray.setOriginal(true);
    await vi.waitFor(() => expect(tray.ready()).toBe(true));
    // Only the original's upload counts.
    expect(tray.items[0].bytes).toBe(100);
    expect(uploads.at(-1)?.n).toBe(100);
  });

  it("forgets a removed image even if its upload finishes later", async () => {
    const { tray } = setup({ slow: true });
    tray.add([file("a"), file("b")]);
    tray.remove(tray.items[0].key);
    await vi.waitFor(() => expect(tray.ready()).toBe(true));
    expect(tray.items.length).toBe(1);
    expect(tray.imageIds().length).toBe(1);
  });
});

it("tells the machine to drop an image that was removed or replaced", async () => {
  const aborted: string[] = [];
  let n = 0;
  const tray = new Tray(
    async () => png(),
    async (m, _p, _progress, started) => {
      const id = `${m}-${++n}`;
      started?.(() => aborted.push(id));
      return { imageId: id, expiresMs: Number.MAX_SAFE_INTEGER };
    },
    () => undefined,
  );
  tray.machineId = "mac";
  tray.add([file("a"), file("b")]);
  await vi.waitFor(() => expect(tray.ready()).toBe(true));
  tray.remove(tray.items[0].key);
  expect(aborted).toEqual(["mac-1"]);
  tray.retarget("vps");
  await vi.waitFor(() => expect(tray.ready()).toBe(true));
  expect(aborted).toEqual(["mac-1", "mac-2"]);
  // Sent: the images belong to the message, nothing is dropped.
  tray.clear();
  expect(aborted).toEqual(["mac-1", "mac-2"]);
});

it("never sends the compressed bytes when 原图 is on and the original failed (review B7)", async () => {
  const uploaded: number[] = [];
  const tray = new Tray(
    async (_f, original) => {
      if (original) throw new Error("图片超过 20 MB");
      return png(10);
    },
    async (_m, p) => {
      uploaded.push(p.bytes.length);
      return { imageId: "id" + uploaded.length, expiresMs: Number.MAX_SAFE_INTEGER };
    },
    () => undefined,
  );
  tray.machineId = "mac";
  tray.add([file("a")]);
  await vi.waitFor(() => expect(tray.ready()).toBe(true));
  tray.setOriginal(true);
  await vi.waitFor(() => expect(tray.items[0].state).toBe("failed"));
  tray.retry(tray.items[0].key);
  await vi.waitFor(() => expect(tray.items[0].state).toBe("failed"));
  tray.retarget("vps");
  await vi.waitFor(() => expect(tray.items[0].state).toBe("failed"));
  expect(tray.ready()).toBe(false);
  expect(uploaded).toEqual([10]);
});

describe("putting a message's images back (review W2, W4)", () => {
  it("refuses, adding nothing, when they do not all fit", async () => {
    const { tray } = setup();
    tray.add([1, 2, 3, 4, 5].map((i) => file("d" + i)));
    await vi.waitFor(() => expect(tray.ready()).toBe(true));
    expect(tray.restore([{ imageId: "a" }, { imageId: "b" }])).toBe(false);
    expect(tray.items.length).toBe(5);
  });

  it("never prepares again an image it has no bytes for", async () => {
    const { tray, prepare } = setup();
    expect(tray.restore([{ imageId: "remote-1" }])).toBe(true);
    expect(tray.ready()).toBe(false);
    expect(tray.canSwitchOriginal()).toBe(false);
    tray.setOriginal(true);
    expect(tray.original).toBe(false);
    expect(tray.imageIds()).toEqual(["remote-1"]);
    // Another machine cannot get it: it says so rather than sending nothing.
    tray.retarget("vps");
    await vi.waitFor(() => expect(tray.items[0].state).toBe("failed"));
    expect(tray.items[0].error).toContain("重新选择");
    expect(prepare).not.toHaveBeenCalled();
  });
});

it("serializes bitmap preparation across drafts and skips superseded queued work", async () => {
  const releases: (() => void)[] = [];
  const modes: boolean[] = [];
  let active = 0;
  let peak = 0;
  const prepare = vi.fn(async (_f: Blob, original: boolean) => {
    modes.push(original);
    peak = Math.max(peak, ++active);
    await new Promise<void>((resolve) => releases.push(resolve));
    active--;
    return png(original ? 100 : 10);
  });
  const upload = vi.fn(async () => ({ imageId: "id", expiresMs: Number.MAX_SAFE_INTEGER }));
  const first = new Tray(prepare, upload, () => undefined);
  const second = new Tray(prepare, upload, () => undefined);
  first.machineId = second.machineId = "m";
  first.add([file("a"), file("b")]);
  await vi.waitFor(() => expect(prepare).toHaveBeenCalledTimes(1));
  first.setOriginal(true);
  first.setOriginal(false);
  first.setOriginal(true);
  second.add([file("c")]);
  await Promise.resolve();
  expect(active).toBe(1);
  for (let i = 0; i < 4; i++) {
    await vi.waitFor(() => expect(releases.length).toBe(i + 1));
    releases[i]();
  }
  await vi.waitFor(() => expect(first.ready() && second.ready()).toBe(true));
  expect(peak).toBe(1);
  expect(modes).toEqual([false, true, true, false]);
  // The first in-flight decode finished but never uploaded stale bytes.
  expect(upload).toHaveBeenCalledTimes(3);
});

it("continues preparing the next image after a failed decoder", async () => {
  let n = 0;
  const tray = new Tray(async () => { if (++n === 1) throw new Error("bad"); return png(); }, async () => ({ imageId: "ok", expiresMs: Number.MAX_SAFE_INTEGER }), () => undefined);
  tray.machineId = "m";
  tray.add([file("bad"), file("good")]);
  await vi.waitFor(() => expect(tray.items.map((it) => it.state)).toEqual(["failed", "done"]));
});

it("uploads restored local image bytes again instead of trusting an old ID", async () => {
  const { tray, upload } = setup();
  tray.restore([{ imageId: "deleted", thumb: file("image") }]);
  expect(tray.ready()).toBe(false);
  await vi.waitFor(() => expect(tray.ready()).toBe(true));
  expect(upload).toHaveBeenCalledTimes(1);
  expect(tray.imageIds()).toEqual(["id1"]);
});

it("requires reselection when a restored image has only an unverified remote ID", () => {
  const { tray, prepare, upload } = setup();
  tray.restore([{ imageId: "deleted" }]);
  expect(tray.ready()).toBe(false);
  expect(tray.items[0].state).toBe("failed");
  expect(prepare).not.toHaveBeenCalled();
  expect(upload).not.toHaveBeenCalled();
});

it("expires at the exact deadline and uploads fresh bytes without preparing them again", async () => {
  vi.useFakeTimers();
  try {
    vi.setSystemTime(1000);
    let uploads = 0;
    const prepared = vi.fn(async () => png());
    const tray = new Tray(prepared, async () => ({ imageId: "image-" + ++uploads, expiresMs: Date.now() + 100 }), () => undefined);
    tray.machineId = "m";
    tray.add([file("a")]);
    // The serial preparation and upload consist only of microtasks.
    for (let n = 0; n < 10; n++) await Promise.resolve();
    expect(tray.ready()).toBe(true);
    vi.setSystemTime(1099);
    expect(tray.ready()).toBe(true);
    vi.setSystemTime(1100);
    expect(tray.ready()).toBe(false);
    tray.refreshExpired();
    expect(tray.ready()).toBe(false);
    for (let n = 0; n < 10; n++) await Promise.resolve();
    expect(tray.ready()).toBe(true);
    expect(tray.imageIds()).toEqual(["image-2"]);
    expect(prepared).toHaveBeenCalledTimes(1);
  } finally { vi.useRealTimers(); }
});

it("puts the original option and source file back, then prepares in that mode", async () => {
  const { tray, prepare } = setup();
  const source = file("original-source");
  tray.restore([{ imageId: "gone", file: source, thumb: file("compressed-preview") }], true);
  await vi.waitFor(() => expect(tray.ready()).toBe(true));
  expect(tray.original).toBe(true);
  expect(prepare).toHaveBeenCalledWith(source, true);
  expect(tray.totalBytes()).toBe(100);
});

it("sends files as-is, keeps image and file limits independent, and frees upload buffers", async () => {
  const prepare = vi.fn(async () => png());
  const seen: Prepared[] = [];
  const tray = new Tray(prepare, async (_m, p) => { seen.push(p); return { imageId: p.format === "file" ? "" : "img", fileId: p.fileName, expiresMs: Number.MAX_SAFE_INTEGER }; }, () => undefined);
  tray.fileSupported = true; tray.machineId = "mac";
  tray.add(Array.from({ length: 6 }, () => file("img")));
  const raw = new File(["EXIF\x00data"], "photo.png", { type: "image/png" });
  tray.addFiles([raw, ...Array.from({ length: 6 }, (_, i) => new File(["x"], `data${i}.csv`))]);
  await vi.waitFor(() => expect(tray.ready()).toBe(true));
  expect(tray.images()).toHaveLength(6); expect(tray.files()).toHaveLength(6);
  expect(tray.note).toContain("最多 6 个文件"); expect(prepare).toHaveBeenCalledTimes(6);
  const p = seen.find((p) => p.fileName === "photo.png")!;
  expect(p.format).toBe("file"); expect(new TextDecoder().decode(p.bytes)).toBe("EXIF\x00data");
  expect(tray.files().every((f) => !f.prepared)).toBe(true);
  expect(tray.imageIds()).toHaveLength(6); expect(tray.fileIds()).toHaveLength(6);
  tray.setOriginal(true);
  await vi.waitFor(() => expect(tray.ready()).toBe(true));
  expect(seen.filter((p) => p.fileName)).toHaveLength(6);
});

it("rejects unsupported, oversized and long-named files before reading any bytes", () => {
  const { tray, upload } = setup();
  const source = new File(["data"], "x.csv");
  const read = vi.spyOn(source, "arrayBuffer");
  tray.addFiles([source]); expect(tray.note).toContain("不支持发送文件");
  tray.fileSupported = true; tray.maxFileBytes = 3;
  tray.addFiles([source]); expect(tray.note).toContain("超过单个文件");
  tray.addFiles([new File([], "中".repeat(86))]); expect(tray.note).toContain("255 字节");
  expect(tray.items).toHaveLength(0); expect(read).not.toHaveBeenCalled(); expect(upload).not.toHaveBeenCalled();
});

it("retries failed files, reuploads after expiry and machine switch, and restores only from local bytes", async () => {
  let n = 0;
  const machines: string[] = [];
  const tray = new Tray(async () => png(), async (m) => {
    machines.push(m); if (++n === 1) throw new Error("网络中断");
    return { imageId: "", fileId: "f" + n, expiresMs: Date.now() + 60000 };
  }, () => undefined);
  tray.fileSupported = true; tray.machineId = "mac";
  const source = new File(["x,y"], "data.csv"); tray.addFiles([source]);
  await vi.waitFor(() => expect(tray.failed()).toBe(true)); expect(tray.ready()).toBe(false);
  tray.retry(tray.items[0].key); await vi.waitFor(() => expect(tray.ready()).toBe(true));
  tray.items[0].expiresMs = 0; tray.refreshExpired(); expect(tray.ready()).toBe(false);
  await vi.waitFor(() => expect(tray.fileIds()).toEqual(["f3"]));
  tray.retarget("other"); await vi.waitFor(() => expect(tray.ready()).toBe(true));
  expect(machines).toEqual(["mac", "mac", "mac", "other"]);
  tray.clear(); expect(tray.restoreFiles([{ fileId: "old", name: "data.csv", size: 3, file: source }])).toBe(true);
  await vi.waitFor(() => expect(tray.fileIds()).toEqual(["f5"]));
  tray.clear(); tray.restoreFiles([{ fileId: "gone", name: "data.csv", size: 3 }]);
  await vi.waitFor(() => expect(tray.failed()).toBe(true)); expect(tray.items[0].error).toContain("重新选择");
});
