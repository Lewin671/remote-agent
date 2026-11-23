import * as i18n from "./i18n";

// A suffix in existing event strings; still needed by Queue.tsx until
// SessionTrayItemView carries an explicit is_skeleton flag from Core.
export const skeletonNote = "\n…（remote-agent：内容过大，请在终端查看/回答）";
export function skeletonMessage(): string { return i18n.t("session.skeleton"); }

export function isSkeleton(value: string): boolean {
  return value.endsWith(skeletonNote);
}
