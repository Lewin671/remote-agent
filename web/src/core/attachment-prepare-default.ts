import type { PreparedImageOutput } from "../attachment-executor";
import { browserBackend, compressImage, prepareOriginal } from "../images";

export async function prepareImageDefault(
  blob: Blob,
  opts: {
    original: boolean;
    maxOriginalBytes: number;
    maxDimensionPx: number;
    maxCompressedBytes: number;
  },
): Promise<PreparedImageOutput> {
  if (opts.original) {
    const p = await prepareOriginal(blob, opts.maxOriginalBytes);
    return { bytes: p.bytes, format: p.format, width: p.width, height: p.height };
  }
  const p = await compressImage(blob, browserBackend, {
    maxDimensionPx: opts.maxDimensionPx,
    maxCompressedBytes: opts.maxCompressedBytes,
  });
  return { bytes: p.bytes, format: p.format, width: p.width, height: p.height };
}
