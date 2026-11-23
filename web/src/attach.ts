import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { renderText } from "./core/text";
import {
  AttachmentCandidateInputSchema,
  AttachmentCandidateRoute,
  ImageProblemInputSchema,
  ImageProblemKind,
  type AttachmentEvalScreenRequestSchema,
  type AttachmentEvalViewModel,
  type ImageProblemInput,
  type ImageProblemView,
} from "./gen/remoteagent/core/v1/attachments_view_pb";
import type { Text } from "./gen/remoteagent/core/v1/text_pb";
import * as i18n from "./i18n";
import { ImageError } from "./imagecodec/error";

// Images and files chosen to go with a message (architecture §17):
// Core evaluates candidate routing, count/size/filename limits, and image error
// classification via AttachmentEval; Tray owns local preparation, chunked
// upload orchestration, generation invalidation, and draft caching.

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

export type AttachmentEvaluator = (
  req: MessageInitShape<typeof AttachmentEvalScreenRequestSchema>,
) => AttachmentEvalViewModel;

export interface CandidateEntry {
  file?: Blob;
  name: string;
  mimeType: string;
  sizeBytes: number;
  isDirectory?: boolean;
}

export interface TrayItem {
  kind?: "file";
  isVideo?: boolean;
  name?: string;
  displayName?: string;
  formattedSize?: string;
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
  problemInput?: ImageProblemInput;
  problem?: ImageProblemView;
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

function imageProblemInputFromError(e: unknown): ImageProblemInput {
  if (e instanceof ImageError) {
    const explicitKind =
      e.problem === "original_too_large"
        ? ImageProblemKind.ORIGINAL_TOO_LARGE
        : e.problem === "dimensions"
          ? ImageProblemKind.DIMENSIONS
          : ImageProblemKind.DECODE;
    return create(ImageProblemInputSchema, {
      explicitKind,
      rawDiagnostic: e.detail,
    });
  }
  const code =
    e && typeof e === "object" && typeof (e as { code?: unknown }).code === "string"
      ? (e as { code: string }).code
      : "";
  const msg = e instanceof Error ? e.message : String(e ?? "");
  return create(ImageProblemInputSchema, {
    errorCode: code,
    rawDiagnostic: msg,
  });
}

let keys = 0;

export class Tray {
  items: TrayItem[] = [];
  restoring = false;
  original = false;
  note = "";
  sessionId = "";
  machineId = "";
  fileSupported = false;
  maxFiles = 6;
  maxFileBytes = 100 * 1024 * 1024;
  galleryAcceptMime = "image/*";
  originalImageHint = "";
  fileMetadataNotice = "";
  fileBlocked = "";
  failureSummary?: Text;
  evaluate?: AttachmentEvaluator;
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
    evaluate?: AttachmentEvaluator,
  ) {
    this.evaluate = evaluate;
  }

  /** Evaluates whether the gallery or file picker can be opened right now via Core AttachmentEval. */
  checkPickBlocked(forFiles: boolean, fallbackBlocked: string): string {
    if (!this.evaluate) return fallbackBlocked;
    const ev = this.evaluate({
      machineId: this.machineId,
      sessionId: this.sessionId,
      currentImages: this.images().length,
      currentFiles: this.files().length,
    });
    if (forFiles) {
      if (ev.canPickFiles) return "";
      return renderText(ev.filesPickBlockedReason) || fallbackBlocked;
    }
    if (ev.canPickGallery) return "";
    return renderText(ev.galleryBlockedReason) || fallbackBlocked;
  }

  /** Evaluates a picked/dropped/pasted batch synchronously via Core AttachmentEval and queues accepted items. */
  addCandidates(entries: readonly CandidateEntry[], forceFile: boolean): string {
    if (this.restoring || !entries.length) return "";
    if (!this.evaluate) {
      return "";
    }
    const ev = this.evaluate({
      machineId: this.machineId,
      sessionId: this.sessionId,
      currentImages: this.images().length,
      currentFiles: this.files().length,
      candidates: entries.map((c) =>
        create(AttachmentCandidateInputSchema, {
          name: c.name,
          mimeType: c.mimeType,
          sizeBytes: BigInt(Math.max(0, Math.floor(c.sizeBytes))),
          sizeUnknown: false,
          isDirectory: !!c.isDirectory,
          forceFile,
        }),
      ),
    });

    let added = false;
    ev.decisions.forEach((d, idx) => {
      const entry = entries[idx];
      if (!d.accepted || !entry?.file) return;
      if (d.route === AttachmentCandidateRoute.IMAGE) {
        const item: TrayItem = {
          key: "t" + ++keys,
          file: entry.file,
          state: "preparing",
          progress: 0,
          thumb: entry.file,
        };
        this.items.push(item);
        added = true;
        void this.run(item, true);
      } else if (
        d.route === AttachmentCandidateRoute.VIDEO_FILE ||
        d.route === AttachmentCandidateRoute.FILE
      ) {
        const item: TrayItem = {
          key: "t" + ++keys,
          file: entry.file,
          kind: "file",
          isVideo: d.route === AttachmentCandidateRoute.VIDEO_FILE || d.isVideo,
          name: entry.name,
          displayName: d.displayName || entry.name,
          formattedSize: d.formattedSize,
          bytes: entry.sizeBytes,
          state: "preparing",
          progress: 0,
        };
        this.items.push(item);
        added = true;
        void this.run(item, true);
      }
    });

    if (added) this.onAdd?.();
    const notice = renderText(ev.notice);
    this.note = notice;
    this.onChange();
    return notice;
  }

  /** Convenience wrapper for adding image blobs (delegates to Core AttachmentEval). */
  add(files: Blob[]) {
    this.addCandidates(
      files.map((f, idx) => ({
        file: f,
        name: f instanceof File && f.name ? f.name : `image-${idx + 1}.png`,
        mimeType: f.type || "image/png",
        sizeBytes: f.size,
      })),
      false,
    );
  }

  /** Convenience wrapper for adding explicit file picker selections (forceFile = true). */
  addFiles(files: File[]) {
    this.addCandidates(
      files.map((f) => ({
        file: f,
        name: f.name,
        mimeType: f.type,
        sizeBytes: f.size,
      })),
      true,
    );
  }

  /** Images put back from a message that was not sent. Local bytes are
   * uploaded again: the machine may have cleaned up the old file. All or nothing:
   * false, with nothing added, when they do not all fit. */
  restore(images: { imageId: string; thumb?: Blob; file?: Blob }[], original = false): boolean {
    if (this.restoring || this.images().length + images.length > this.max) return false;
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

  private evalFileMeta(items: readonly { name: string; mimeType: string; sizeBytes: number }[]) {
    if (!this.evaluate || !items.length) return undefined;
    return this.evaluate({
      machineId: this.machineId,
      sessionId: this.sessionId,
      currentImages: 0,
      currentFiles: 0,
      candidates: items.map((f) =>
        create(AttachmentCandidateInputSchema, {
          name: f.name,
          mimeType: f.mimeType,
          sizeBytes: BigInt(Math.max(0, Math.floor(f.sizeBytes))),
          forceFile: true,
        }),
      ),
    });
  }

  restoreFiles(files: { fileId: string; name: string; size: number; file?: Blob }[]): boolean {
    const meta = this.evalFileMeta(
      files.map((f) => ({ name: f.name, mimeType: f.file?.type ?? "", sizeBytes: f.size })),
    );
    if (files.length && !this.fileSupported) {
      this.note =
        renderText(meta?.fileUploadBlockedReason) ||
        this.fileBlocked ||
        i18n.t("attachment.update_required");
      this.onChange();
      return false;
    }
    if (this.files().length + files.length > this.maxFiles) return false;
    files.forEach((f, idx) => {
      const d = meta?.decisions[idx];
      const item: TrayItem = {
        key: "t" + ++keys,
        kind: "file",
        isVideo: d?.isVideo ?? false,
        name: f.name,
        displayName: d?.displayName || f.name,
        formattedSize: d?.formattedSize,
        bytes: f.size,
        fileId: f.fileId,
        file: f.file ?? new Blob(),
        state: "preparing",
        progress: 0,
        remoteOnly: !f.file,
      };
      this.items.push(item);
      void this.run(item, true);
    });
    this.onChange();
    return true;
  }

  remove(key: string) {
    const item = this.items.find((i) => i.key === key);
    this.items = this.items.filter((i) => i.key !== key);
    if (item) this.invalidate(item);
    this.gen.delete(key);
    this.note = "";
    this.syncProblems();
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

  /** A successful request consumes only the items it held when frozen. */
  detach(taken: readonly TrayItem[]) {
    const mine = new Set(taken);
    this.items = this.items.filter((item) => !mine.has(item));
    for (const item of taken) this.gen.delete(item.key);
    this.note = "";
    if (!this.items.length) this.original = false;
    this.syncProblems();
    this.onChange();
  }

  /** After sending: the images are the message's now, nothing to drop. */
  clear() {
    this.items = [];
    this.gen.clear();
    this.note = "";
    this.original = false;
    this.failureSummary = undefined;
    this.onChange();
  }

  full(): boolean {
    return this.images().length >= this.max;
  }

  /** Every image is on the current machine. */
  ready(): boolean {
    return !this.restoring && this.items.length > 0 && (!this.files().length || this.fileSupported) && this.items.every((i) => i.state === "done" && !!(i.kind === "file" ? i.fileId : i.imageId) && i.machineId === this.machineId && (i.expiresMs ?? 0) > Date.now());
  }

  /** A persisted draft owns its source files, not a possibly expired upload. */
  hydrate(items: { file: Blob; name?: string; kind?: "file" }[], original: boolean) {
    this.original = original;
    this.restoring = false;
    const fileItems = items.filter((i) => i.kind === "file");
    const fileMeta = this.evalFileMeta(
      fileItems.map((i) => ({ name: i.name ?? "", mimeType: i.file.type, sizeBytes: i.file.size })),
    );
    let fileIdx = 0;
    this.items = items.map((i) => {
      if (i.kind === "file") {
        const d = fileMeta?.decisions[fileIdx++];
        return {
          ...i,
          key: "t" + ++keys,
          isVideo: d?.isVideo ?? false,
          displayName: d?.displayName || i.name,
          formattedSize: d?.formattedSize,
          state: "preparing",
          progress: 0,
          bytes: i.file.size,
        };
      }
      return { ...i, key: "t" + ++keys, state: "preparing", progress: 0, thumb: i.file, bytes: i.file.size };
    });
    for (const item of this.items) void this.run(item, true);
    this.onChange();
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

  /** Classifies all currently failed images once via Core AttachmentEval and caches the result. */
  private syncProblems() {
    const failed = this.images().filter((i) => i.state === "failed");
    if (!failed.length) {
      this.failureSummary = undefined;
      return;
    }
    if (!this.evaluate) return;
    const ev = this.evaluate({
      machineId: this.machineId,
      sessionId: this.sessionId,
      currentImages: this.images().length,
      currentFiles: this.files().length,
      problems: failed.map(
        (i) =>
          i.problemInput ??
          create(ImageProblemInputSchema, {
            rawDiagnostic: i.error ?? "",
          }),
      ),
    });
    failed.forEach((item, idx) => {
      item.problem = ev.problems[idx];
    });
    this.failureSummary = ev.failureSummary;
  }

  // Invalidate before abort callbacks or either shared queue can yield. An old
  // result can never restore readiness, even on a rapid machine A → B → A.
  private invalidate(item: TrayItem): number {
    const g = (this.gen.get(item.key) ?? 0) + 1;
    this.gen.set(item.key, g);
    const abort = item.abort;
    Object.assign(item, {
      state: "preparing",
      progress: 0,
      error: undefined,
      problemInput: undefined,
      problem: undefined,
      imageId: undefined,
      fileId: undefined,
      expiresMs: undefined,
      machineId: undefined,
      abort: undefined,
    });
    abort?.();
    return g;
  }

  private async run(item: TrayItem, prepare: boolean) {
    const g = this.invalidate(item);
    this.syncProblems();
    this.onChange();
    if (item.remoteOnly) {
      if (item.kind === "file") {
        Object.assign(item, { state: "failed", error: i18n.t("attachment.file_missing") });
      } else {
        Object.assign(item, {
          state: "failed",
          error: "source_missing",
          problemInput: create(ImageProblemInputSchema, {
            explicitKind: ImageProblemKind.SOURCE_MISSING,
          }),
        });
        this.syncProblems();
      }
      this.onChange();
      return;
    }
    const current = () => this.gen.get(item.key) === g;
    const set = (patch: Partial<TrayItem>) => {
      if (!current()) return;
      Object.assign(item, patch);
      if (item.kind !== "file" && patch.state !== undefined) {
        this.syncProblems();
      }
      this.onChange();
    };
    const machineId = this.machineId;
    // At most one 100 MiB file is read/hashed/uploaded at a time across drafts.
    let release: (() => void) | undefined;
    if (item.kind === "file") {
      const previous = fileTransfers;
      fileTransfers = new Promise<void>((resolve) => { release = resolve; });
      await previous;
    }
    try {
      if (!current()) return;
      if (item.kind === "file") {
        const check = this.evalFileMeta([
          { name: item.name ?? "", mimeType: item.file.type, sizeBytes: item.file.size },
        ]);
        if (!this.fileSupported || check?.fileUploadBlockedReason) {
          throw new Error(
            renderText(check?.fileUploadBlockedReason) ||
              this.fileBlocked ||
              i18n.t("attachment.update_required"),
          );
        }
        const decision = check?.decisions[0];
        if (decision && !decision.accepted) {
          throw new Error(renderText(decision.rejectReason) || renderText(check?.notice));
        }
      }
      if (prepare || !this.fresh(item)) {
        // What was prepared the other way is never sent in its place.
        const original = this.original;
        set({ state: "preparing", progress: 0, error: undefined, problemInput: undefined, problem: undefined, imageId: undefined, fileId: undefined, machineId: undefined, prepared: undefined, preparedOriginal: undefined });
        const p = await prepareSerial(current, async () => item.kind === "file" ? { bytes: new Uint8Array(await readAttachment(item.file)), format: "file" as const, fileName: item.name, width: 0, height: 0 } : this.prepare(item.file, original));
        if (!current() || !p) return;
        set({ prepared: p, preparedOriginal: original, bytes: p.bytes.length, thumb: item.kind === "file" ? undefined : new Blob([p.bytes as Uint8Array<ArrayBuffer>], { type: "image/" + p.format }) });
      }
      set({ state: "uploading", progress: 0, error: undefined, problemInput: undefined, problem: undefined, imageId: undefined, fileId: undefined, machineId: undefined });
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
      if (item.kind === "file") {
        set({ state: "failed", error: e instanceof Error ? e.message : String(e) });
      } else {
        set({
          state: "failed",
          error: e instanceof ImageError ? e.problem : e instanceof Error ? e.message : String(e),
          problemInput: imageProblemInputFromError(e),
        });
      }
    } finally { if (item.kind === "file" && current()) item.prepared = undefined; release?.(); }
  }
}

async function readAttachment(file: Blob): Promise<ArrayBuffer> {
  try { return await file.arrayBuffer(); }
  catch { throw new Error(i18n.t("attachment.read_failed")); }
}
