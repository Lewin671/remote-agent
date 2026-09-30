// Preparing images the user sends to an agent (architecture.md §17,
// 「从客户端发图片」). Two ways out:
//
//   compressImage(file)   the default: at most MAX_EDGE px on the long edge,
//                         JPEG (PNG when transparent), under COMPRESSED_BUDGET
//                         so the sealed envelope fits in one 1 MiB command.
//   prepareOriginal(file) "send original": original pixels and quality for
//                         PNG/JPEG/WebP/GIF, up to MAX_ORIGINAL_BYTES, sent in
//                         CHUNK_BYTES pieces (chunkBytes, sha256Hex).
//
// Both turn the image upright by its EXIF orientation and remove metadata
// (location, camera, XMP, text chunks) before anything leaves the device.
//
// Byte-level API (pure, no DOM):
//   sniffFormat(bytes): ImageFormat
//   readOrientation(bytes): number                      1..8, 1 when absent
//   stripMetadata(bytes, format): Uint8Array            throws ImageError
//   imageSize(bytes, format): { width, height }         stored size, before orientation
//   rotateJpegLossless(bytes, orientation): Uint8Array | undefined
//   chunkBytes(bytes, maxChunk): { offset, data }[]
//   sha256Hex(bytes): Promise<string>
//   orientationMatrix(orientation, w, h): canvas transform for drawing upright
//
// Browser API (canvas; takes an optional ImageBackend so tests can fake it):
//   compressImage(file, backend?): Promise<PreparedImage>
//   prepareOriginal(file, backend?): Promise<OriginalImage>
//
// Every refusal is an ImageError whose message is shown to the user as is.

import { ImageError } from "./imagecodec/error";
import { rotateJpegLossless } from "./imagecodec/jpeg";
import { imageSize, readOrientation, sniffFormat, stripMetadata, type ImageFormat, type PassthroughFormat } from "./imagecodec/metadata";

export { ImageError, type ImageErrorCode } from "./imagecodec/error";
export { rotateJpegLossless } from "./imagecodec/jpeg";
export { imageSize, readOrientation, sniffFormat, stripMetadata, type ImageFormat, type PassthroughFormat } from "./imagecodec/metadata";

/** Images per message, the first message of a new session included. */
export const MAX_IMAGES = 6;
/** Long edge of a compressed image. The CLIs shrink to about 2000px themselves. */
export const MAX_EDGE = 2048;
/** Largest original accepted, before and after any re-encoding. */
export const MAX_ORIGINAL_BYTES = 20 * 1024 * 1024;
/** Piece size for sending originals; each piece is sealed into its own envelope. */
export const CHUNK_BYTES = 768 * 1024;
/**
 * Largest compressed image. The sealed envelope adds the header, nonce, GCM
 * tag and Protobuf framing to it and must stay under 1 MiB; 900 KiB leaves
 * over 100 KiB for that.
 */
export const COMPRESSED_BUDGET = 900 * 1024;
/**
 * Largest canvas re-encoded at original size. iOS Safari refuses canvases
 * over 16 Mi pixels (4096 x 4096); larger originals have to be sent compressed.
 */
export const MAX_CANVAS_PIXELS = 4096 * 4096;

/** JPEG qualities tried in turn before the compressed image is scaled down further. */
const JPEG_QUALITIES = [0.85, 0.75, 0.65, 0.55];
/** Each further scaling step when no quality fits. */
const SCALE_STEP = 0.75;
const ORIGINAL_JPEG_QUALITY = 0.95;

export interface PreparedImage {
  bytes: Uint8Array;
  format: "jpeg" | "png";
  /** Size as displayed, orientation applied. */
  width: number;
  height: number;
}

export interface OriginalImage {
  bytes: Uint8Array;
  format: PassthroughFormat;
  width: number;
  height: number;
  /** True when the pixels went through a canvas instead of being kept as sent. */
  reencoded: boolean;
}

// ------------------------------------------------------------ bytes

/**
 * Splits bytes into consecutive pieces of at most maxChunk bytes. The
 * pieces are views into bytes, not copies. Empty input gives no pieces.
 */
export function chunkBytes(bytes: Uint8Array, maxChunk: number): { offset: number; data: Uint8Array }[] {
  if (!(maxChunk >= 1)) throw new RangeError("maxChunk must be positive");
  const out: { offset: number; data: Uint8Array }[] = [];
  for (let offset = 0; offset < bytes.length; offset += maxChunk) {
    out.push({ offset, data: bytes.subarray(offset, offset + maxChunk) });
  }
  return out;
}

/** Lowercase hex SHA-256, as the daemon checks after reassembling chunks. */
export async function sha256Hex(bytes: Uint8Array): Promise<string> {
  const d = new Uint8Array(await crypto.subtle.digest("SHA-256", bytes as Uint8Array<ArrayBuffer>));
  return Array.from(d, (x) => x.toString(16).padStart(2, "0")).join("");
}

/**
 * The canvas transform [a, b, c, d, e, f] that draws a w x h image stored
 * with the given EXIF orientation upright. For orientations 5..8 the canvas
 * must be h x w.
 */
export function orientationMatrix(orientation: number, w: number, h: number): [number, number, number, number, number, number] {
  switch (orientation) {
    case 2:
      return [-1, 0, 0, 1, w, 0];
    case 3:
      return [-1, 0, 0, -1, w, h];
    case 4:
      return [1, 0, 0, -1, 0, h];
    case 5:
      return [0, 1, 1, 0, 0, 0];
    case 6:
      return [0, 1, -1, 0, h, 0];
    case 7:
      return [0, -1, -1, 0, h, w];
    case 8:
      return [0, -1, 1, 0, 0, w];
    default:
      return [1, 0, 0, 1, 0, 0];
  }
}

// ------------------------------------------------------------ canvas

/** A decoded image, as createImageBitmap returns it. */
export interface DecodedImage {
  readonly width: number;
  readonly height: number;
  close(): void;
}

/** A drawn canvas ready to encode. */
export interface Surface {
  readonly width: number;
  readonly height: number;
  /** Whether any pixel is not fully opaque. */
  hasAlpha(): boolean;
  encode(type: "image/jpeg" | "image/png", quality?: number): Promise<Uint8Array>;
}

/** The DOM parts compressImage and prepareOriginal use; replaceable in tests. */
export interface ImageBackend {
  decode(data: Blob): Promise<DecodedImage>;
  /**
   * Draws img scaled to w x h, then turned upright by orientation; the
   * surface is h x w for orientations 5..8.
   */
  draw(img: DecodedImage, orientation: number, w: number, h: number): Surface;
}

type Canvas = OffscreenCanvas | HTMLCanvasElement;

function newCanvas(w: number, h: number): Canvas {
  if (typeof OffscreenCanvas !== "undefined") return new OffscreenCanvas(w, h);
  const c = document.createElement("canvas");
  c.width = w;
  c.height = h;
  return c;
}

async function canvasBytes(c: Canvas, type: string, quality?: number): Promise<Uint8Array> {
  const blob =
    c instanceof HTMLCanvasElement
      ? await new Promise<Blob | null>((resolve) => c.toBlob(resolve, type, quality))
      : await c.convertToBlob({ type, quality });
  // Browsers fall back to PNG for types they cannot write, and give null
  // for canvases too large to encode.
  if (!blob || blob.type !== type) throw new ImageError("decode_failed", `canvas did not encode ${type}`);
  return new Uint8Array(await blob.arrayBuffer());
}

export const browserBackend: ImageBackend = {
  async decode(data) {
    try {
      return await createImageBitmap(data, { imageOrientation: "from-image" });
    } catch (e) {
      throw new ImageError("decode_failed", `createImageBitmap: ${e}`);
    }
  },
  draw(img, orientation, w, h) {
    const turned = orientation >= 5;
    const canvas = newCanvas(turned ? h : w, turned ? w : h);
    const ctx = canvas.getContext("2d") as CanvasRenderingContext2D | OffscreenCanvasRenderingContext2D | null;
    if (!ctx) throw new ImageError("decode_failed", "no 2d canvas context");
    ctx.imageSmoothingQuality = "high";
    ctx.setTransform(...orientationMatrix(orientation, w, h));
    ctx.drawImage(img as ImageBitmap, 0, 0, w, h);
    return {
      width: canvas.width,
      height: canvas.height,
      hasAlpha() {
        const px = ctx.getImageData(0, 0, canvas.width, canvas.height).data;
        for (let i = 3; i < px.length; i += 4) if (px[i] < 255) return true;
        return false;
      },
      encode: (type, quality) => canvasBytes(canvas, type, quality),
    };
  },
};

/**
 * What to hand the browser's decoder, and which orientation to apply on
 * top. For formats we can parse, metadata is stripped first and the
 * orientation applied by us, so the result does not depend on whether a
 * browser honours EXIF in PNG or WebP. Other formats (HEIC, AVIF) go in
 * as they are and the browser applies what it knows.
 */
function decodeSource(bytes: Uint8Array, format: ImageFormat): { blob: Blob; orientation: number } {
  if (format === "jpeg" || format === "png" || format === "webp" || format === "gif") {
    try {
      const stripped = stripMetadata(bytes, format);
      return { blob: new Blob([stripped as Uint8Array<ArrayBuffer>]), orientation: readOrientation(bytes) };
    } catch {
      // Something browsers may still decode; let them apply the orientation.
    }
  }
  return { blob: new Blob([bytes as Uint8Array<ArrayBuffer>]), orientation: 1 };
}

async function readBlob(file: Blob): Promise<Uint8Array> {
  return new Uint8Array(await file.arrayBuffer());
}

/**
 * The default way to send: decode, turn upright, fit the long edge into
 * MAX_EDGE, and encode as PNG when any pixel is transparent, JPEG
 * otherwise. JPEG steps down in quality, then both step down in size,
 * until the result fits COMPRESSED_BUDGET. Canvas output has no metadata.
 */
export async function compressImage(file: Blob, backend: ImageBackend = browserBackend): Promise<PreparedImage> {
  const bytes = await readBlob(file);
  const format = sniffFormat(bytes);
  const { blob, orientation } = decodeSource(bytes, format);
  const img = await backend.decode(blob);
  try {
    let scale = Math.min(1, MAX_EDGE / Math.max(img.width, img.height));
    let transparent: boolean | undefined;
    for (;;) {
      const w = Math.max(1, Math.round(img.width * scale));
      const h = Math.max(1, Math.round(img.height * scale));
      const surface = backend.draw(img, orientation, w, h);
      // JPEG has no alpha channel; skip reading back its pixels.
      transparent ??= format !== "jpeg" && surface.hasAlpha();
      const done = (out: Uint8Array, f: PreparedImage["format"]): PreparedImage | undefined =>
        out.length <= COMPRESSED_BUDGET ? { bytes: out, format: f, width: surface.width, height: surface.height } : undefined;
      if (transparent) {
        const r = done(await surface.encode("image/png"), "png");
        if (r) return r;
      } else {
        for (const q of JPEG_QUALITIES) {
          const r = done(await surface.encode("image/jpeg", q), "jpeg");
          if (r) return r;
        }
      }
      if (w <= 1 && h <= 1) throw new ImageError("too_large", "cannot compress under the budget");
      scale *= SCALE_STEP;
    }
  } finally {
    img.close();
  }
}

/** Re-encodes at the original size, upright. */
async function reencode(
  backend: ImageBackend,
  bytes: Uint8Array,
  format: ImageFormat,
  mode: "png" | "jpeg" | "auto",
  jpegQuality: number,
): Promise<OriginalImage> {
  const { blob, orientation } = decodeSource(bytes, format);
  const img = await backend.decode(blob);
  try {
    if (img.width * img.height > MAX_CANVAS_PIXELS) {
      throw new ImageError("too_large", `${img.width}x${img.height} is over the canvas limit`, "图片尺寸太大，请用压缩方式发送");
    }
    const surface = backend.draw(img, orientation, img.width, img.height);
    const png = mode === "png" || (mode === "auto" && surface.hasAlpha());
    const out = png ? await surface.encode("image/png") : await surface.encode("image/jpeg", jpegQuality);
    if (out.length > MAX_ORIGINAL_BYTES) throw new ImageError("too_large", `re-encoded to ${out.length} bytes`);
    return { bytes: out, format: png ? "png" : "jpeg", width: surface.width, height: surface.height, reencoded: true };
  } finally {
    img.close();
  }
}

/**
 * "Send original": keeps the pixels and quality whenever possible.
 *
 * - JPEG, no orientation: metadata stripped, scan data untouched.
 * - JPEG with an orientation: rotated losslessly when possible; otherwise
 *   re-encoded upright at quality 1.0.
 * - PNG, WebP, GIF: metadata stripped. PNG and WebP with an orientation are
 *   re-encoded upright as PNG, which keeps every pixel (lossy WebP grows,
 *   animated WebP keeps only its first frame; both are rare with EXIF).
 * - HEIC, AVIF and anything else the browser decodes: re-encoded at the
 *   original size as JPEG 0.95, or PNG when transparent.
 */
export async function prepareOriginal(file: Blob, backend: ImageBackend = browserBackend): Promise<OriginalImage> {
  if (file.size > MAX_ORIGINAL_BYTES) throw new ImageError("too_large", `${file.size} bytes`);
  const bytes = await readBlob(file);
  const format = sniffFormat(bytes);
  if (format !== "jpeg" && format !== "png" && format !== "webp" && format !== "gif") {
    return reencode(backend, bytes, format, "auto", ORIGINAL_JPEG_QUALITY);
  }
  let stripped: Uint8Array;
  try {
    stripped = stripMetadata(bytes, format);
  } catch {
    // Malformed in a way browsers may tolerate; never pass it through as is.
    return reencode(backend, bytes, format, format === "jpeg" ? "jpeg" : "png", 1);
  }
  const orientation = format === "gif" ? 1 : readOrientation(bytes);
  if (orientation === 1) {
    return { bytes: stripped, format, ...imageSize(stripped, format), reencoded: false };
  }
  if (format === "jpeg") {
    let rotated: Uint8Array | undefined;
    try {
      rotated = rotateJpegLossless(stripped, orientation);
    } catch {
      rotated = undefined;
    }
    if (rotated && rotated.length <= MAX_ORIGINAL_BYTES) return { bytes: rotated, format, ...imageSize(rotated, format), reencoded: false };
    return reencode(backend, bytes, format, "jpeg", 1);
  }
  return reencode(backend, bytes, format, "png", 1);
}
