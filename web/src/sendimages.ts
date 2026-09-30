// How a chosen file is prepared for sending (images.ts): compressed by
// default, or the original with its metadata removed. Either way it is
// turned upright first and carries no EXIF, location or text chunks.

import type { Prepared } from "./attach";
import { compressImage, prepareOriginal } from "./images";

export async function prepareForSending(file: Blob, original: boolean): Promise<Prepared> {
  const p = original ? await prepareOriginal(file) : await compressImage(file);
  return { bytes: p.bytes, format: p.format, width: p.width, height: p.height };
}
