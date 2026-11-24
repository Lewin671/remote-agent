// Binary image format signature checks and MIME mapping used by shell I/O
// (decrypting fetched display copies and verifying embedded preview bytes).

import { ImageFormat } from "./gen/remoteagent/v1/event_pb";
import { sniffFormat } from "./imagecodec/metadata";

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

/** Whether bytes begin with the signature of format f (delegated to clientcore).
 * Only the formats the daemon re-encodes to are accepted; anything else (an SVG
 * with a script, say) is never turned into a blob: URL. */
export function matchesFormat(b: Uint8Array, f: ImageFormat): boolean {
  const detected = sniffFormat(b);
  switch (f) {
    case ImageFormat.PNG:
      return detected === "png";
    case ImageFormat.JPEG:
      return detected === "jpeg";
    case ImageFormat.WEBP:
      return detected === "webp";
    case ImageFormat.GIF:
      return detected === "gif";
  }
  return false;
}
