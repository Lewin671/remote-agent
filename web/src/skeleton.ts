import * as i18n from "./i18n";
// A suffix in existing event strings; no extra wire field is required.
export const skeletonNote = "\n…（remote-agent：内容过大，请在终端查看/回答）";
export function skeletonMessage(): string { return i18n.t("session.skeleton"); }
// Events are read-only once they are in; one is asked about each time the
// history is laid out again, which is every time another event comes in.
const known = new WeakMap<object, boolean>();

export function isSkeleton(value: unknown): boolean {
  if (typeof value === "string") return value.endsWith(skeletonNote);
  if (!value || typeof value !== "object" || value instanceof Uint8Array) return false;
  let skeleton = known.get(value);
  if (skeleton === undefined) {
    skeleton = Object.values(value).some(isSkeleton);
    known.set(value, skeleton);
  }
  return skeleton;
}
