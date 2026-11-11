import type { Remote } from "./remote";
import type { ScheduleTrigger } from "./schedules";

/** Task drafts being written, per page state. They live apart from the
 * pages (which are fetched when first opened) so that the app can ask
 * whether any would be lost without having fetched them. */
export const scheduleDrafts = new WeakMap<
  Remote,
  Map<string, { project: string; name: string; trigger: ScheduleTrigger; notifyFinish?: boolean; dirty?: boolean }>
>();

export function hasScheduleDrafts(remote: Remote): boolean {
  return [...(scheduleDrafts.get(remote)?.values() ?? [])].some((draft) => !!draft.name || !!draft.dirty);
}
