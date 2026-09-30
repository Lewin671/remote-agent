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
    return "id" + i;
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
