import { loadLocalData, saveLocalData } from "./device-security-store";
import { newRequestID } from "./crypto";
import type { StoredAccount } from "./storage";
import type { Tray } from "./attach";

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
  constructor(private account: StoredAccount) {}
  private key(key: string) { return "composer/" + JSON.stringify(key); }

  async read(key: string): Promise<RestoredDraft | undefined> {
    const name = this.key(key);
    const bytes = await loadLocalData(this.account.server, this.account.deviceId, name);
    if (!bytes) return;
    const draft: Draft = JSON.parse(new TextDecoder().decode(bytes));
    if (typeof draft.text !== "string" || typeof draft.original !== "boolean" || !Array.isArray(draft.sources)) throw new Error("草稿格式不正确");
    const items = await Promise.all(draft.sources.map(async (source) => {
      if (!source.id || typeof source.type !== "string") throw new Error("草稿格式不正确");
      const data = await loadLocalData(this.account.server, this.account.deviceId, name + "/" + source.id);
      if (!data) throw new Error("草稿图片没有本机副本");
      const blob = new Blob([data as Uint8Array<ArrayBuffer>], { type: source.type });
      const file = source.name ? new File([blob], source.name, { type: source.type }) : blob;
      this.ids.set(file, source.id);
      return { file, name: source.name, kind: source.kind };
    }));
    this.kept.set(key, new Set(draft.sources.map(s => s.id)));
    return { text: draft.text, original: draft.original, items };
  }

  save(key: string, text: string, tray?: Tray): Promise<void> {
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
    const work = (this.writes.get(key) ?? Promise.resolve()).catch(() => undefined).then(async () => {
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
    void work.catch(() => { if (this.signatures.get(key) === signature) this.signatures.delete(key); });
    return work;
  }
}

/** Existing callers use a map; both edits and clearing after a send persist. */
export class DraftTexts extends Map<string, string> {
  constructor(private changed: (key: string) => void) { super(); }
  override set(key: string, value: string) { const old = this.get(key); super.set(key, value); if (old !== value) this.changed(key); return this; }
  override delete(key: string) { const removed = super.delete(key); if (removed) this.changed(key); return removed; }
  restore(key: string, value: string) { super.set(key, value); }
}
