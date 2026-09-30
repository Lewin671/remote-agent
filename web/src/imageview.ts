// Images in a session as the chat shows them (architecture §17): the
// preview each event carries, the "[Image #N]" placeholders in message
// text, the session's pictures in order for the viewer, and checking that
// decrypted bytes are the format they claim before they are shown.

import type { Event, ImageRef } from "./gen/remoteagent/v1/event_pb";
import { ImageFormat, ImageSource } from "./gen/remoteagent/v1/event_pb";

export function mimeOf(f: ImageFormat): string {
  switch (f) {
    case ImageFormat.PNG:
      return "image/png";
    case ImageFormat.JPEG:
      return "image/jpeg";
    case ImageFormat.WEBP:
      return "image/webp";
    case ImageFormat.GIF:
      return "image/gif";
  }
  return "";
}

export function extOf(f: ImageFormat): string {
  return { [ImageFormat.PNG]: "png", [ImageFormat.JPEG]: "jpg", [ImageFormat.WEBP]: "webp", [ImageFormat.GIF]: "gif" }[f as number] ?? "img";
}

/** Whether bytes begin with the signature of format f. Only the formats
 * the daemon re-encodes to are accepted; anything else (an SVG with a
 * script, say) is never turned into a blob: URL. */
export function matchesFormat(b: Uint8Array, f: ImageFormat): boolean {
  const at = (i: number, ...xs: number[]) => xs.every((x, j) => b[i + j] === x);
  switch (f) {
    case ImageFormat.PNG:
      return at(0, 0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a);
    case ImageFormat.JPEG:
      return at(0, 0xff, 0xd8, 0xff);
    case ImageFormat.WEBP:
      return at(0, 0x52, 0x49, 0x46, 0x46) && at(8, 0x57, 0x45, 0x42, 0x50);
    case ImageFormat.GIF:
      return at(0, 0x47, 0x49, 0x46, 0x38);
  }
  return false;
}

export type Segment = { text: string } | { image: number };

/** A message's text with the CLI's "[Image #N]" placeholders: those
 * before any words go (the images show above the bubble, numbered), and
 * those among the words become "图 N" chips. Only numbers of images the
 * message has count; text like "[Image #9]" typed by the user stays. */
export function splitPlaceholders(text: string, refs: Pick<ImageRef, "cliIndex">[]): Segment[] {
  const known = new Set(refs.map((r) => r.cliIndex).filter((n) => n > 0));
  const out: Segment[] = [];
  const re = /\[Image #(\d+)\]/g;
  let last = 0;
  let lead = true;
  for (let m = re.exec(text); m; m = re.exec(text)) {
    const n = Number(m[1]);
    if (!known.has(n)) continue;
    const before = text.slice(last, m.index);
    if (before.trim()) lead = false;
    if (!lead) {
      if (before) out.push({ text: before });
      out.push({ image: n });
    }
    last = m.index + m[0].length;
  }
  const rest = lead ? text.slice(last).replace(/^\s+/, "") : text.slice(last);
  if (rest) out.push({ text: rest });
  return out;
}

/** One picture in the session, with where it appeared. */
export interface SessionImage {
  ref: ImageRef;
  seq: bigint; // the event it is in
  time: number;
  kind: "user" | "tool" | "generated";
  prompt?: string; // a generated image's rewritten prompt
}

/** Every picture in the session, in order: what the viewer pages
 * through. */
export function sessionImages(events: Event[]): SessionImage[] {
  const out: SessionImage[] = [];
  for (const e of events) {
    const time = e.time ? Number(e.time.seconds) * 1000 : 0;
    const k = e.kind;
    const refs = k.case === "userMessage" || k.case === "toolCall" || k.case === "imageGenerated" ? k.value.images : [];
    for (const ref of refs)
      out.push({
        ref,
        seq: e.seq,
        time,
        kind: k.case === "imageGenerated" || ref.source === ImageSource.GENERATED ? "generated" : k.case === "toolCall" || ref.source === ImageSource.TOOL ? "tool" : "user",
        prompt: k.case === "imageGenerated" ? k.value.revisedPrompt : undefined,
      });
  }
  return out;
}

/** The size to lay a preview out at before it loads, from the image's
 * own proportions, so nothing jumps when it arrives. */
export function aspectOf(ref: Pick<ImageRef, "previewWidth" | "previewHeight" | "width" | "height" | "displayWidth" | "displayHeight">): number {
  for (const [w, h] of [
    [ref.previewWidth, ref.previewHeight],
    [ref.displayWidth, ref.displayHeight],
    [ref.width, ref.height],
  ])
    if (w > 0 && h > 0) return w / h;
  return 4 / 3;
}

/** The pixel size to write for an image: as the CLI had it, or the size
 * before it shrank it when known. */
export function sizeText(ref: Pick<ImageRef, "width" | "height" | "originalWidth" | "originalHeight">): string {
  const w = ref.originalWidth || ref.width;
  const h = ref.originalHeight || ref.height;
  return w && h ? `${w} × ${h}` : "";
}

/** "2 张截图" and the like, for a folded run of tool calls. */
export function imagesNote(n: number): string {
  return n ? ` · ${n} 张截图` : "";
}

export function baseName(path: string): string {
  return path.split("/").pop() || path;
}
