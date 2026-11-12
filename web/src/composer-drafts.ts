import { loadLocalData, saveLocalData, removeLocalData, CorruptLocalContent } from "./device-security-store";
import { newRequestID } from "./crypto";
import type { StoredAccount } from "./storage";
import type { Tray } from "./attach";
import type { Outgoing } from "./model";

interface Source { id: string; type: string; name?: string; kind?: "file" }
interface Draft { text: string; original: boolean; sources: Source[] }
export interface RestoredDraft { text: string; original: boolean; items: { file: Blob; name?: string; kind?: "file" }[] }

/** Only source bytes change when a photo is chosen. Typing rewrites the small
 * sealed manifest, never every photo. Writes stay in user-action order. */
export class ComposerDraftStore {
  private ids = new WeakMap<Blob, string>();
  private kept = new Map<string, Set<string>>();
  private signatures = new Map<string, string>();
  private writes = new Map<string, Promise<void>>();
  private revisions = new Map<string, number>();
  private tail: Promise<void> = Promise.resolve();
  constructor(private account: StoredAccount) {}
  private key(key: string) { return "composer/" + JSON.stringify(key); }

  private async readStored(key: string) {
    const name = this.key(key), revision = this.revisions.get(key) ?? 0;
    try {
      const bytes = await loadLocalData(this.account.server, this.account.deviceId, name);
      if (!bytes) return;
      const draft: Draft = JSON.parse(new TextDecoder().decode(bytes));
      if (!draft || typeof draft.text !== "string" || typeof draft.original !== "boolean" || !Array.isArray(draft.sources)) throw new CorruptLocalContent();
      const items = await Promise.all(draft.sources.map(async source => {
        if (!source || typeof source.id !== "string" || !source.id || typeof source.type !== "string" || source.name !== undefined && typeof source.name !== "string") return;
        try {
          const data = await loadLocalData(this.account.server, this.account.deviceId, name + "/" + source.id);
          if (!data) return;
          const blob = new Blob([data as Uint8Array<ArrayBuffer>], { type: source.type });
          const file = source.name ? new File([blob], source.name, { type: source.type }) : blob;
          this.ids.set(file, source.id);
          return { file, name: source.name, kind: source.kind };
        } catch (error) { if (!(error instanceof CorruptLocalContent)) throw error; }
      }));
      if ((this.revisions.get(key) ?? 0) !== revision) return;
      this.kept.set(key, new Set(draft.sources.flatMap(s => typeof s?.id === "string" ? [s.id] : [])));
      return { draft, items, revision };
    } catch (error) {
      if (!(error instanceof CorruptLocalContent || error instanceof SyntaxError)) throw error;
      await this.remove(key);
    }
  }

  async read(key: string): Promise<RestoredDraft | undefined> {
    const saved = await this.readStored(key);
    if (!saved || (this.revisions.get(key) ?? 0) !== saved.revision) return;
    const items = saved.items.filter((item): item is NonNullable<typeof item> => !!item);
    if (items.length !== saved.items.length) {
      // One damaged source must not discard the text or any other image.
      void this.save(key, saved.draft.text, { original: saved.draft.original, items: items.map((item, i) => ({ key: String(i), ...item, state: "done", progress: 1 })) }).catch(() => undefined);
    }
    return { text: saved.draft.text, original: saved.draft.original, items };
  }

  remove(key: string): Promise<void> {
    this.revisions.set(key, (this.revisions.get(key) ?? 0) + 1);
    this.signatures.delete(key);
    const work = this.tail.catch(() => undefined).then(async () => {
      await removeLocalData(this.account.server, this.account.deviceId, this.key(key));
      this.kept.delete(key);
    });
    this.tail = work;
    this.writes.set(key, work);
    return work;
  }

  removeSession(sid: string): Promise<void> {
    return Promise.all([this.remove(sid), this.remove("out:" + sid)]).then(() => undefined);
  }

  save(key: string, text: string, tray?: Pick<Tray, "items" | "original">): Promise<void> {
    const items = (tray?.items ?? []).filter(i => !i.remoteOnly);
    const sources = items.map(i => {
      let id = this.ids.get(i.file);
      if (!id) { id = newRequestID(); this.ids.set(i.file, id); }
      return { id, type: i.file.type, name: i.name ?? (i.file instanceof File ? i.file.name : undefined), kind: i.kind };
    });
    const draft: Draft = { text, original: tray?.original ?? false, sources };
    const signature = JSON.stringify(draft);
    if (this.signatures.get(key) === signature) return this.writes.get(key) ?? Promise.resolve();
    this.signatures.set(key, signature);
    const work = this.tail.catch(() => undefined).then(async () => {
      const name = this.key(key), old = this.kept.get(key) ?? new Set<string>();
      const values = new Map<string, Uint8Array | undefined>();
      for (let i = 0; i < sources.length; i++) {
        if (!old.has(sources[i].id)) values.set(name + "/" + sources[i].id, new Uint8Array(await items[i].file.arrayBuffer()));
      }
      const ids = new Set(sources.map(s => s.id));
      for (const id of old) if (!ids.has(id)) values.set(name + "/" + id, undefined);
      values.set(name, !text && !sources.length ? undefined : new TextEncoder().encode(signature));
      await saveLocalData(this.account.server, this.account.deviceId, values);
      this.kept.set(key, ids);
    });
    this.writes.set(key, work);
    this.tail = work;
    void work.catch(() => { if (this.signatures.get(key) === signature) this.signatures.delete(key); });
    return work;
  }

  flushed(key: string): Promise<void> { return this.writes.get(key) ?? Promise.resolve(); }

  /** Keep ownership while a send is pending or uncertain. Moving the visible
   * draft into the outbox must not leave a restart with neither copy. */
  saveOutbox(sid: string, outgoing: Outgoing[]): Promise<void> {
    const blobs: Blob[] = [];
    const source = (blob?: Blob) => { if (!blob) return undefined; blobs.push(blob); return blobs.length - 1; };
    const records = outgoing.map(o => ({
      sendId: o.sendId, requestId: o.requestId, deadline: o.deadline, text: o.text, mode: o.mode,
      imageIds: o.imageIds, imageSizes: o.imageSizes, status: o.status, error: o.error, unknown: o.unknown,
      thumbs: o.thumbs?.map(source),
      attachments: o.attachments && { original: o.attachments.original, sources: o.attachments.files.map(source) },
      files: o.files?.map(f => ({ ...f, sizeBytes: f.sizeBytes.toString(), file: source(f.file) })),
    }));
    return this.save("out:" + sid, outgoing.length ? JSON.stringify(records) : "", {
      original: false,
      items: blobs.map((file, i) => ({ key: String(i), file, state: "done", progress: 1 })),
    });
  }

  async readOutbox(sid: string): Promise<Outgoing[]> {
    const key = "out:" + sid, kept = await this.readStored(key);
    if (!kept?.draft.text || (this.revisions.get(key) ?? 0) !== kept.revision) return [];
    let records: unknown;
    try { records = JSON.parse(kept.draft.text); } catch { await this.remove(key); return []; }
    if (!Array.isArray(records)) { await this.remove(key); return []; }
    const source = (i: number) => {
      if (!Number.isInteger(i) || !kept.items[i]) throw new CorruptLocalContent();
      return kept.items[i]!.file;
    };
    const result: Outgoing[] = [];
    for (const o of records) {
      try {
        if (!o || typeof o.sendId !== "string" || !o.sendId || typeof o.requestId !== "string" || !o.requestId || typeof o.text !== "string" || !Number.isFinite(o.deadline) || !["sending", "sent", "failed"].includes(o.status) || o.imageIds !== undefined && (!Array.isArray(o.imageIds) || o.imageIds.some((id: unknown) => typeof id !== "string")) || o.attachments !== undefined && typeof o.attachments.original !== "boolean") throw new CorruptLocalContent();
        result.push({
          ...o,
          status: o.status === "sending" ? "failed" : o.status,
          unknown: o.status === "sending" || o.unknown,
          error: o.status === "sending" ? "连接中途断了" : o.error,
          thumbs: o.thumbs?.map(source),
          attachments: o.attachments && { original: o.attachments.original, files: o.attachments.sources.map(source) },
          files: o.files?.map((f: { sizeBytes: string; file?: number }) => ({ ...f, sizeBytes: BigInt(f.sizeBytes), file: f.file === undefined ? undefined : source(f.file) })),
        });
      } catch { /* Discard this damaged message, not the rest of its session. */ }
    }
    if (result.length !== records.length) void this.saveOutbox(sid, result).catch(() => undefined);
    return result;
  }
}

/** Existing callers use a map; both edits and clearing after a send persist. */
export class DraftTexts extends Map<string, string> {
  constructor(private changed: (key: string) => void) { super(); }
  override set(key: string, value: string) { const old = this.get(key); super.set(key, value); if (old !== value) this.changed(key); return this; }
  override delete(key: string) { const removed = super.delete(key); if (removed) this.changed(key); return removed; }
  restore(key: string, value: string) { super.set(key, value); }
}
