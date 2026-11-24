// Binary image format signature checks and MIME mapping used by shell I/O
// (decrypting fetched display copies and verifying embedded preview bytes).

import { ImageFormat } from "./gen/remoteagent/v1/event_pb";

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
      return at(0, 0x47, 0x49, 0x46, 0x38, 0x37, 0x61) || at(0, 0x47, 0x49, 0x46, 0x38, 0x39, 0x61);
  }
  return false;
}
