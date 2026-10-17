// A suffix in existing event strings; no extra wire field is required.
export const skeletonNote = "\n…（remote-agent：内容过大，请在终端查看/回答）";
export const skeletonMessage = "内容过大，请在终端查看/回答";
export function isSkeleton(value: unknown): boolean {
  if (typeof value === "string") return value.endsWith(skeletonNote);
  if (!value || typeof value !== "object" || value instanceof Uint8Array) return false;
  return Object.values(value).some(isSkeleton);
}
