// The account's project list preferences: pinned projects in the order
// they were pinned, and projects removed from the list (architecture §8
// 项目列表偏好). They live in one sealed object on the server, replaced
// only if its revision is the one a change was based on. A change is an
// operation (pin, unpin, hide, restore) that is redone on whatever is
// stored when another client wrote first; the stored copy is never
// overwritten with this client's older picture of it.

import { compareProjects } from "./model";
import { create } from "@bufbuild/protobuf";
import { ProjectPrefSchema, ProjectPrefsSchema, type ProjectPref, type ProjectPrefs } from "./gen/remoteagent/v1/remote_pb";

export type PrefKind = "pin" | "unpin" | "hide" | "unhide";

export interface PrefOp {
  // Random, so a client whose reply was lost can tell whether its write
  // went in (ProjectPrefs.recent_ops).
  id: string;
  kind: PrefKind;
  path: string;
  at: number; // ms
}

export interface PrefsSnapshot {
  revision: bigint; // 0 while the object does not exist
  prefs: ProjectPrefs;
}

export const RECENT_OPS = 64;

export function emptyPrefs(): ProjectPrefs {
  return create(ProjectPrefsSchema, { schema: 1 });
}

/** prefs with op done. Each operation says what it wants, not "toggle",
 * so doing it again, or on a copy another client changed, means the same:
 * pinning adds at the end if absent; hiding also unpins, and restoring
 * does not pin again. */
export function applyOp(prefs: ProjectPrefs, op: PrefOp): ProjectPrefs {
  const entry = () => create(ProjectPrefSchema, { path: op.path, sinceMs: BigInt(op.at) });
  const has = (list: ProjectPref[]) => list.some((p) => p.path === op.path);
  const without = (list: ProjectPref[]) => list.filter((p) => p.path !== op.path);
  let pinned = [...prefs.pinned];
  let hidden = [...prefs.hidden];
  switch (op.kind) {
    case "pin":
      hidden = without(hidden);
      if (!has(pinned)) pinned.push(entry());
      break;
    case "unpin":
      pinned = without(pinned);
      break;
    case "hide":
      pinned = without(pinned);
      if (!has(hidden)) hidden.push(entry());
      break;
    case "unhide":
      hidden = without(hidden);
      break;
  }
  const recentOps = [...prefs.recentOps.filter((id) => id !== op.id), op.id].slice(-RECENT_OPS);
  return create(ProjectPrefsSchema, { schema: 1, pinned, hidden, recentOps });
}

/** Whether op is already in prefs: a write whose reply was lost. */
export function applied(prefs: ProjectPrefs, op: PrefOp): boolean {
  return prefs.recentOps.includes(op.id);
}

/** Where the object is stored: read it, or write a new copy if the
 * stored revision is still expected. */
export interface PrefsStore {
  get(): Promise<PrefsSnapshot>;
  put(expected: bigint, next: ProjectPrefs): Promise<{ applied: boolean; current: PrefsSnapshot }>;
}

export class PrefsConflict extends Error {}

/** Writes op on top of the latest copy. On a conflict the operation is
 * redone on the copy the server returned, keeping the other client's
 * change; after a failure whose outcome is unknown, the stored copy is
 * read again and op counts as done if it is in it. from is the copy the
 * page has, used for the first try. */
export async function commitOp(store: PrefsStore, op: PrefOp, from?: PrefsSnapshot, tries = 6): Promise<PrefsSnapshot> {
  let cur = from ?? (await store.get());
  let failures = 0;
  for (let i = 0; i < tries; i++) {
    if (applied(cur.prefs, op)) return cur;
    let r;
    try {
      r = await store.put(cur.revision, applyOp(cur.prefs, op));
    } catch (e) {
      // The write may or may not have gone in: look before trying again,
      // once; after that the user decides (retry is the same operation).
      if (++failures > 1) throw e;
      cur = await store.get();
      continue;
    }
    if (r.applied) return r.current;
    cur = r.current;
  }
  throw new PrefsConflict("项目列表一直在被别的客户端修改，没能保存");
}

/** Projects as the lists group them: pinned first, in the order pinned;
 * the rest by latest session creation; removed ones apart. Sessions of removed
 * projects are untouched: lists of waiting and running sessions do not
 * look at this. */
export function groupProjects<P extends { key: string; latestCreatedMs: number }>(list: P[], prefs: ProjectPrefs): { pinned: P[]; others: P[]; hidden: P[] } {
  const hidden = new Set(prefs.hidden.map((p) => p.path));
  const byKey = new Map(list.map((p) => [p.key, p]));
  const pinned = prefs.pinned.map((p) => byKey.get(p.path)).filter((p): p is P => !!p && !hidden.has(p.key));
  const pinnedKeys = new Set(pinned.map((p) => p.key));
  const others = list.filter((p) => !pinnedKeys.has(p.key) && !hidden.has(p.key)).sort(compareProjects);
  return { pinned, others, hidden: list.filter((p) => hidden.has(p.key)) };
}

export function isHidden(prefs: ProjectPrefs, path: string): boolean {
  return prefs.hidden.some((p) => p.path === path);
}

export function isPinned(prefs: ProjectPrefs, path: string): boolean {
  return prefs.pinned.some((p) => p.path === path) && !isHidden(prefs, path);
}
