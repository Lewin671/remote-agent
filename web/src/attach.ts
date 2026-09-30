// Images chosen to go with a message (architecture §17 从客户端发图片):
// each is prepared on this device (compressed, or the original with its
// metadata removed) and uploaded to the machine on its own as soon as it
// is chosen. The message can go only once every image is on the machine;
// a failed image stays marked until it is retried or removed, so a
// message never goes out with fewer images than shown.

export interface Prepared {
  bytes: Uint8Array;
  format: "jpeg" | "png" | "webp" | "gif";
  width: number;
  height: number;
}

/** Prepares a chosen file: compressed, or the original when asked. */
export type Prepare = (file: Blob, original: boolean) => Promise<Prepared>;
/** Uploads prepared bytes to a machine; resolves the image ID to send. */
export type Upload = (machineId: string, p: Prepared, progress: (done: number, total: number) => void) => Promise<string>;

export interface TrayItem {
  key: string;
  file: Blob; // as chosen, for preparing again
  state: "preparing" | "uploading" | "done" | "failed";
  progress: number; // 0..1 of the upload
  prepared?: Prepared;
  thumb?: Blob; // what the tray shows: the prepared image once there is one
  imageId?: string;
  machineId?: string; // where imageId is
  error?: string;
  bytes?: number;
}

let keys = 0;

export class Tray {
  items: TrayItem[] = [];
  original = false;
  note = "";
  machineId = "";
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
    const room = Math.max(0, this.max - this.items.length);
    const taken = files.slice(0, room);
    const left = files.length - taken.length;
    this.note = left > 0 ? `一条消息最多 ${this.max} 张，多选的 ${left} 张没有加入` : "";
    for (const file of taken) {
      const item: TrayItem = { key: "t" + ++keys, file, state: "preparing", progress: 0, thumb: file };
      this.items.push(item);
      void this.run(item, true);
    }
    this.onChange();
  }

  remove(key: string) {
    this.items = this.items.filter((i) => i.key !== key);
    this.gen.delete(key);
    this.note = "";
    this.onChange();
  }

  /** 原图 for this message: every image is prepared and sent again. */
  setOriginal(v: boolean) {
    if (v === this.original) return;
    this.original = v;
    for (const i of this.items) void this.run(i, true);
    this.onChange();
  }

  /** Images go to a machine, not a session: a new machine gets them again. */
  retarget(machineId: string) {
    if (machineId === this.machineId) return;
    this.machineId = machineId;
    for (const i of this.items) void this.run(i, !i.prepared);
    this.onChange();
  }

  retry(key: string) {
    const i = this.items.find((x) => x.key === key);
    if (i) void this.run(i, !i.prepared);
  }

  clear() {
    this.items = [];
    this.gen.clear();
    this.note = "";
    this.original = false;
    this.onChange();
  }

  full(): boolean {
    return this.items.length >= this.max;
  }

  /** Every image is on the current machine. */
  ready(): boolean {
    return this.items.length > 0 && this.items.every((i) => i.state === "done" && i.machineId === this.machineId);
  }

  /** Still preparing or uploading. */
  busy(): boolean {
    return this.items.some((i) => i.state === "preparing" || i.state === "uploading");
  }

  failed(): boolean {
    return this.items.some((i) => i.state === "failed");
  }

  imageIds(): string[] {
    return this.items.map((i) => i.imageId!).filter(Boolean);
  }

  totalBytes(): number {
    return this.items.reduce((n, i) => n + (i.bytes ?? 0), 0);
  }

  private async run(item: TrayItem, prepare: boolean) {
    const g = (this.gen.get(item.key) ?? 0) + 1;
    this.gen.set(item.key, g);
    const current = () => this.gen.get(item.key) === g;
    const set = (patch: Partial<TrayItem>) => {
      if (!current()) return;
      Object.assign(item, patch);
      this.onChange();
    };
    const machineId = this.machineId;
    try {
      if (prepare || !item.prepared) {
        set({ state: "preparing", progress: 0, error: undefined, imageId: undefined, machineId: undefined });
        const p = await this.prepare(item.file, this.original);
        if (!current()) return;
        set({ prepared: p, bytes: p.bytes.length, thumb: new Blob([p.bytes as Uint8Array<ArrayBuffer>], { type: "image/" + p.format }) });
      }
      set({ state: "uploading", progress: 0, error: undefined, imageId: undefined, machineId: undefined });
      const id = await this.upload(machineId, item.prepared!, (done, total) => set({ progress: total ? done / total : 0 }));
      set({ state: "done", progress: 1, imageId: id, machineId });
    } catch (e) {
      set({ state: "failed", error: e instanceof Error ? e.message : String(e) });
    }
  }
}
