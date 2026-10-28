// Images chosen to go with a message (architecture §17 从客户端发图片):
// each is prepared on this device (compressed, or the original with its
// metadata removed) and uploaded to the machine on its own as soon as it
// is chosen. The message can go only once every image is on the machine;
// a failed image stays marked until it is retried or removed, so a
// message never goes out with fewer images than shown.

export interface Prepared {
  bytes: Uint8Array;
  format: "jpeg" | "png" | "webp" | "gif" | "file";
  fileName?: string;
  width: number;
  height: number;
}

/** Prepares a chosen file: compressed, or the original when asked. */
export type Prepare = (file: Blob, original: boolean) => Promise<Prepared>;
/** Uploads prepared bytes to a machine; resolves the image ID and its deadline. */
export interface UploadedImage { imageId: string; fileId?: string; expiresMs: number }

export type Upload = (machineId: string, p: Prepared, progress: (done: number, total: number) => void, started?: (abort: () => void) => void) => Promise<UploadedImage>;

export interface TrayItem {
  kind?: "file";
  name?: string;
  fileId?: string;
  key: string;
  file: Blob; // as chosen, for preparing again
  state: "preparing" | "uploading" | "done" | "failed";
  progress: number; // 0..1 of the upload
  prepared?: Prepared;
  preparedOriginal?: boolean; // which way prepared was made
  thumb?: Blob; // what the tray shows: the prepared image once there is one
  imageId?: string;
  expiresMs?: number;
  machineId?: string; // where imageId is
  error?: string;
  bytes?: number;
  // Put back with no bytes of its own (after a refresh, say): its old ID
  // cannot prove the file is still there. It must be chosen again.
  remoteOnly?: boolean;
  // Tells the machine to drop this image's transfer, once one began.
  abort?: () => void;
}

// One decoder across all drafts. A superseded request waits its turn and
// checks its generation before allocating a bitmap.
let fileTransfers = Promise.resolve();
let preparation = Promise.resolve();
async function prepareSerial<T>(current: () => boolean, work: () => Promise<T>): Promise<T | undefined> {
  const previous = preparation;
  let release!: () => void;
  preparation = new Promise<void>((resolve) => { release = resolve; });
  await previous;
  try { return current() ? await work() : undefined; }
  finally { release(); }
}

let keys = 0;

export class Tray {
  items: TrayItem[] = [];
  original = false;
  note = "";
  machineId = "";
  fileSupported = false;
  maxFiles = 6;
  maxFileBytes = 100 * 1024 * 1024;
  // Called when images are first chosen, as typing the first letter is: a
  // new session's draft then holds its project and settings.
  onAdd?: () => void;
  // Each change of what an item should be starts a new generation; results
  // of an older one are dropped.
  private gen = new Map<string, number>();

  constructor(
    private prepare: Prepare,
    private upload: Upload,
    private onChange: () => void,
    public max = 6,
  ) {}

  /** Adds files in the order chosen, up to max in all. */
  add(files: Blob[]) {
    const room = Math.max(0, this.max - this.images().length);
    const taken = files.slice(0, room);
    if (taken.length) this.onAdd?.();
    const left = files.length - taken.length;
    this.note = left > 0 ? `一条消息最多 ${this.max} 张，多选的 ${left} 张没有加入` : "";
    for (const file of taken) {
      const item: TrayItem = { key: "t" + ++keys, file, state: "preparing", progress: 0, thumb: file };
      this.items.push(item);
      void this.run(item, true);
    }
    this.onChange();
  }

  /** Images put back from a message that was not sent. Local bytes are
   * uploaded again: the machine may have cleaned up the old file. All or nothing:
   * false, with nothing added, when they do not all fit. */
  restore(images: { imageId: string; thumb?: Blob; file?: Blob }[], original = false): boolean {
    if (this.images().length + images.length > this.max) return false;
    if (!this.images().length) this.original = original;
    for (const im of images) {
      const item: TrayItem = { key: "t" + ++keys, file: im.file ?? im.thumb ?? new Blob(), state: "done", progress: 1, thumb: im.thumb, imageId: im.imageId, machineId: this.machineId, remoteOnly: !im.file && !im.thumb };
      this.items.push(item);
      void this.run(item, true);
    }
    this.onChange();
    return true;
  }

  images(): TrayItem[] { return this.items.filter((i) => i.kind !== "file"); }
  files(): TrayItem[] { return this.items.filter((i) => i.kind === "file"); }
  fileIds(): string[] { return this.files().map((i) => i.fileId!).filter(Boolean); }

  addFiles(files: File[]) {
    if (!this.fileSupported) { this.note = "这台电脑不支持发送文件，请更新电脑上的 remote-agent"; this.onChange(); return; }
    const notes: string[] = [];
    let added = false;
    for (const file of files) {
      if (this.files().length >= this.maxFiles) { notes.push(`一条消息最多 ${this.maxFiles} 个文件`); break; }
      if (file.size > this.maxFileBytes) { notes.push(`「${file.name}」超过单个文件 ${Math.round(this.maxFileBytes / 1024 / 1024)} MB 的上限`); continue; }
      if (!file.name || new TextEncoder().encode(file.name).length > 255) { notes.push("文件名不能超过 255 字节，请重命名后选择"); continue; }
      const item: TrayItem = { key: "t" + ++keys, file, kind: "file", name: file.name, bytes: file.size, state: "preparing", progress: 0 };
      this.items.push(item); added = true; void this.run(item, true);
    }
    if (added) this.onAdd?.();
    this.note = notes.join("；"); this.onChange();
  }

  restoreFiles(files: { fileId: string; name: string; size: number; file?: Blob }[]): boolean {
    if (this.files().length + files.length > this.maxFiles) return false;
    for (const f of files) {
      const item: TrayItem = { key: "t" + ++keys, kind: "file", name: f.name, bytes: f.size, fileId: f.fileId, file: f.file ?? new Blob(), state: "preparing", progress: 0, remoteOnly: !f.file };
      this.items.push(item); void this.run(item, true);
    }
    this.onChange(); return true;
  }

  remove(key: string) {
    this.items.find((i) => i.key === key)?.abort?.();
    this.items = this.items.filter((i) => i.key !== key);
    this.gen.delete(key);
    this.note = "";
    this.onChange();
  }

  /** Whether 原图 can change: not while an image has no bytes to prepare. */
  canSwitchOriginal(): boolean {
    return !this.images().some((i) => i.remoteOnly);
  }

  /** 原图 for this message: every image is prepared and sent again. */
  setOriginal(v: boolean) {
    if (v === this.original || !this.canSwitchOriginal()) return;
    this.original = v;
    for (const i of this.images()) void this.run(i, true);
    this.onChange();
  }

  /** Images go to a machine, not a session: a new machine gets them again. */
  retarget(machineId: string) {
    if (machineId === this.machineId) return;
    this.machineId = machineId;
    for (const i of this.items) void this.run(i, !this.fresh(i));
    this.onChange();
  }

  retry(key: string) {
    const i = this.items.find((x) => x.key === key);
    if (i) void this.run(i, !this.fresh(i));
  }

  /** Whether an item's prepared bytes are the kind 原图 now asks for. */
  private fresh(i: TrayItem): boolean {
    return !!i.prepared && i.preparedOriginal === this.original;
  }

  /** After sending: the images are the message's now, nothing to drop. */
  clear() {
    this.items = [];
    this.gen.clear();
    this.note = "";
    this.original = false;
    this.onChange();
  }

  full(): boolean {
    return this.images().length >= this.max;
  }

  /** Every image is on the current machine. */
  ready(): boolean {
    return this.items.length > 0 && (!this.files().length || this.fileSupported) && this.items.every((i) => i.state === "done" && i.machineId === this.machineId && (i.expiresMs ?? 0) > Date.now());
  }

  /** Expired uploads have local bytes, but need a new transfer before sending. */
  refreshExpired() {
    for (const i of this.items) {
      if (i.state === "done" && (i.expiresMs ?? 0) <= Date.now()) void this.run(i, !this.fresh(i));
    }
  }

  /** Still preparing or uploading. */
  busy(): boolean {
    return this.items.some((i) => i.state === "preparing" || i.state === "uploading");
  }

  failed(): boolean {
    return this.items.some((i) => i.state === "failed");
  }

  imageIds(): string[] {
    return this.images().map((i) => i.imageId!).filter(Boolean);
  }

  totalBytes(): number {
    return this.images().reduce((n, i) => n + (i.bytes ?? 0), 0);
  }

  private async run(item: TrayItem, prepare: boolean) {
    if (item.remoteOnly) {
      this.gen.set(item.key, (this.gen.get(item.key) ?? 0) + 1);
      Object.assign(item, { state: "failed", error: item.kind === "file" ? "这个文件没有本机副本，请移除后重新选择" : "这张图没有本机副本，请移除后重新选择" });
      this.onChange();
      return;
    }
    const g = (this.gen.get(item.key) ?? 0) + 1;
    this.gen.set(item.key, g);
    const current = () => this.gen.get(item.key) === g;
    const set = (patch: Partial<TrayItem>) => {
      if (!current()) return;
      Object.assign(item, patch);
      this.onChange();
    };
    const machineId = this.machineId;
    // What an earlier generation left on a machine is not needed now.
    item.abort?.();
    item.abort = undefined;
    // At most one 100 MiB file is read/hashed/uploaded at a time across drafts.
    let release: (() => void) | undefined;
    if (item.kind === "file") {
      const previous = fileTransfers;
      fileTransfers = new Promise<void>((resolve) => { release = resolve; });
      await previous;
    }
    try {
      if (!current()) return;
      if (item.kind === "file" && !this.fileSupported) throw new Error("这台电脑不支持发送文件，请更新电脑上的 remote-agent");
      if (item.kind === "file" && item.file.size > this.maxFileBytes) throw new Error(`单个文件不能超过 ${Math.round(this.maxFileBytes / 1024 / 1024)} MB`);
      if (prepare || !this.fresh(item)) {
        // What was prepared the other way is never sent in its place.
        const original = this.original;
        set({ state: "preparing", progress: 0, error: undefined, imageId: undefined, fileId: undefined, machineId: undefined, prepared: undefined, preparedOriginal: undefined });
        const p = await prepareSerial(current, async () => item.kind === "file" ? { bytes: new Uint8Array(await readAttachment(item.file)), format: "file" as const, fileName: item.name, width: 0, height: 0 } : this.prepare(item.file, original));
        if (!current() || !p) return;
        set({ prepared: p, preparedOriginal: original, bytes: p.bytes.length, thumb: item.kind === "file" ? undefined : new Blob([p.bytes as Uint8Array<ArrayBuffer>], { type: "image/" + p.format }) });
      }
      set({ state: "uploading", progress: 0, error: undefined, imageId: undefined, fileId: undefined, machineId: undefined });
      const uploaded = await this.upload(
        machineId,
        item.prepared!,
        (done, total) => set({ progress: total ? done / total : 0 }),
        (abort) => {
          // A superseded generation drops its transfer at once.
          if (current()) item.abort = abort;
          else abort();
        },
      );
      set({ state: "done", progress: 1, imageId: uploaded.imageId, fileId: uploaded.fileId, expiresMs: uploaded.expiresMs, machineId, ...(item.kind === "file" ? { prepared: undefined } : {}) });
    } catch (e) {
      set({ state: "failed", error: e instanceof Error ? e.message : String(e) });
    } finally { if (item.kind === "file" && current()) item.prepared = undefined; release?.(); }
  }
}

async function readAttachment(file: Blob): Promise<ArrayBuffer> {
 try { return await file.arrayBuffer(); }
 catch { throw new Error("无法读取这个文件；文件夹不能直接发送，请压缩后选择，或重新选择可读取的文件"); }
}
