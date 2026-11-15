/** Read the original bounded ServerFrame body without re-encoding signed bytes.
 * The delivery itself is at most 64 KiB; its oneof wrapper adds four bytes. */
export function peerFrameBody(raw: Uint8Array<ArrayBuffer>, field: 11 | 12): Uint8Array<ArrayBuffer> | undefined {
  if (raw.length > 65540 || raw[0] !== (field << 3 | 2)) return;
  let length = 0, shift = 0, at = 1;
  for (; at < Math.min(raw.length, 5); at++, shift += 7) {
    const v = raw[at]; length |= (v & 127) << shift;
    if (!(v & 128)) { if (at > 1 && !v || length > 65536 || length !== raw.length - at - 1) return; return raw.slice(at + 1); }
  }
}
