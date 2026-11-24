// Thin shell executor over clientcore/domain/imageparse: feeds small header
// slices to the Go WASM core, executes in-place byte skips (JPEG entropy scan
// and GIF sub-blocks), and splices the resulting byte ranges locally without
// copying whole images across the WASM boundary.

import { getCore } from "../core/runtime";
import { ImageError, malformed } from "./error";

export type ImageFormat = "jpeg" | "png" | "webp" | "gif" | "heic" | "avif" | "unknown";

/** The formats stripMetadata and imageSize understand; the ones sent as originals. */
export type PassthroughFormat = "jpeg" | "png" | "webp" | "gif";

export interface JpegSegment {
  marker: number;
  /** Offset of the 0xFF that starts the marker (fill bytes before it skipped). */
  start: number;
  /** End of the marker segment (its length field included). */
  end: number;
  /** For SOS, the end of the entropy-coded data that follows; otherwise equals end. */
  dataEnd: number;
  /** Whether Core's JPEG stripping rules retain this segment. */
  keep: boolean;
}

export const SOS = 0xda;
export const EOI = 0xd9;

const MODE_INSPECT = 1;
const MODE_STRIP = 2;
const MODE_JPEG_SEGMENTS = 3;

const STATUS_DONE = 0;
const STATUS_NEED_READ = 1;
const STATUS_NEED_SCAN_JPEG_ENTROPY = 2;
const STATUS_NEED_SKIP_GIF_SUB_BLOCKS = 3;
const STATUS_ERROR = 4;

const ERR_UNSUPPORTED = 2;
const INITIAL_HEADER_BYTES = 64 * 1024;
const SNIFF_HEADER_BYTES = 256;

const FORMAT_NAMES: readonly ImageFormat[] = ["unknown", "jpeg", "png", "webp", "gif", "heic", "avif"];

function formatToCode(f: ImageFormat): number {
  const idx = FORMAT_NAMES.indexOf(f);
  return idx >= 0 ? idx : 0;
}

function codeToFormat(code: number): ImageFormat {
  return FORMAT_NAMES[code] ?? "unknown";
}

function u64le(dv: DataView, at: number): number {
  const lo = dv.getUint32(at, true);
  const hi = dv.getUint32(at + 4, true);
  return hi * 0x100000000 + lo;
}

/** In-place scan for the first real JPEG marker after entropy-coded data; -1 on EOF. */
function scanJpegEntropy(b: Uint8Array, pos: number): number {
  let i = pos;
  const n = b.length;
  while (i + 1 < n) {
    i = b.indexOf(0xff, i);
    if (i < 0 || i + 1 >= n) return -1;
    let k = i;
    while (k + 1 < n && b[k + 1] === 0xff) k++;
    if (k + 1 >= n) return -1;
    const next = b[k + 1];
    if (next === 0x00 || (next >= 0xd0 && next <= 0xd7)) {
      i = k + 2;
      continue;
    }
    return i;
  }
  return -1;
}

/** In-place skip over a GIF sub-block chain; returns offset after 0x00 terminator or -1 on EOF. */
function skipGifSubBlocks(b: Uint8Array, pos: number): number {
  let p = pos;
  const n = b.length;
  for (;;) {
    if (p >= n) return -1;
    const len = b[p];
    p += 1 + len;
    if (p > n) return -1;
    if (len === 0) return p;
  }
}

const textDecoder = new TextDecoder();

function runPlan(b: Uint8Array, mode: number, expectedFormat: number): Uint8Array {
  const core = getCore();
  const maxSteps = core.imagePlanMaxSteps(b.length);
  const plan = core.newImagePlan(mode, expectedFormat, b.length);
  try {
    let offset = 0;
    let slice = b.subarray(0, Math.min(INITIAL_HEADER_BYTES, b.length));
    for (let step = 0; step < maxSteps; step++) {
      const res = plan.step(offset, slice);
      const status = res[0];
      if (status === STATUS_DONE) return res;
      if (status === STATUS_ERROR) {
        const errCode = res[1];
        const detail = textDecoder.decode(res.subarray(2));
        if (errCode === ERR_UNSUPPORTED) throw new ImageError("unsupported", detail);
        throw malformed(detail);
      }
      const dv = new DataView(res.buffer, res.byteOffset, res.byteLength);
      if (status === STATUS_NEED_READ) {
        offset = u64le(dv, 1);
        const len = dv.getUint32(9, true);
        slice = b.subarray(offset, Math.min(offset + len, b.length));
      } else if (status === STATUS_NEED_SCAN_JPEG_ENTROPY) {
        const from = u64le(dv, 1);
        const found = scanJpegEntropy(b, from);
        offset = found;
        slice = found >= 0 ? b.subarray(found, Math.min(found + INITIAL_HEADER_BYTES, b.length)) : new Uint8Array(0);
      } else if (status === STATUS_NEED_SKIP_GIF_SUB_BLOCKS) {
        const from = u64le(dv, 1);
        const end = skipGifSubBlocks(b, from);
        offset = end;
        slice = end >= 0 ? b.subarray(end, Math.min(end + INITIAL_HEADER_BYTES, b.length)) : new Uint8Array(0);
      } else {
        throw malformed("unexpected_plan_status");
      }
    }
    throw malformed("unexpected_plan_status");
  } finally {
    plan.free();
  }
}

function splicePlan(src: Uint8Array, done: Uint8Array): Uint8Array {
  const dv = new DataView(done.buffer, done.byteOffset, done.byteLength);
  const count = dv.getUint32(12, true);
  let at = 16;
  let total = 0;
  for (let i = 0; i < count; i++) {
    const kind = done[at++];
    if (kind === 0) {
      const s = u64le(dv, at);
      const e = u64le(dv, at + 8);
      total += e - s;
      at += 16;
    } else {
      const n = dv.getUint32(at, true);
      total += n;
      at += 4 + n;
    }
  }
  const out = new Uint8Array(total);
  at = 16;
  let dst = 0;
  for (let i = 0; i < count; i++) {
    const kind = done[at++];
    if (kind === 0) {
      const s = u64le(dv, at);
      const e = u64le(dv, at + 8);
      out.set(src.subarray(s, e), dst);
      dst += e - s;
      at += 16;
    } else {
      const n = dv.getUint32(at, true);
      out.set(done.subarray(at + 4, at + 4 + n), dst);
      dst += n;
      at += 4 + n;
    }
  }
  return out;
}

/** Recognizes the format by the file header, never by name or MIME type. */
export function sniffFormat(b: Uint8Array): ImageFormat {
  const head = b.length > SNIFF_HEADER_BYTES ? b.subarray(0, SNIFF_HEADER_BYTES) : b;
  return codeToFormat(getCore().sniffImageFormat(head));
}

/** Walks every marker segment of a JPEG up to and including EOI, across all scans. */
export function* jpegSegments(b: Uint8Array): Generator<JpegSegment> {
  const done = runPlan(b, MODE_JPEG_SEGMENTS, formatToCode("jpeg"));
  const dv = new DataView(done.buffer, done.byteOffset, done.byteLength);
  const count = dv.getUint32(12, true);
  let at = 16;
  for (let i = 0; i < count; i++) {
    const marker = done[at];
    const keep = done[at + 1] !== 0;
    const start = u64le(dv, at + 2);
    const end = u64le(dv, at + 10);
    const dataEnd = u64le(dv, at + 18);
    at += 26;
    yield { marker, start, end, dataEnd, keep };
  }
}

/** Whether a JPEG segment survives metadata stripping (decided by Core). */
export function keepJpegSegment(_b: Uint8Array, s: JpegSegment): boolean {
  return s.keep;
}

/**
 * Removes metadata without touching pixel data. Throws ImageError("decode_failed")
 * on malformed files and ImageError("unsupported") for formats not handled.
 */
export function stripMetadata(b: Uint8Array, format: ImageFormat): Uint8Array {
  if (format !== "jpeg" && format !== "png" && format !== "webp" && format !== "gif") {
    throw new ImageError("unsupported", `cannot strip metadata from ${format}`);
  }
  const done = runPlan(b, MODE_STRIP, formatToCode(format));
  return splicePlan(b, done);
}

/**
 * The EXIF orientation (1..8) from a JPEG APP1, PNG eXIf or WebP EXIF
 * chunk; 1 when there is none or it does not parse. Never throws.
 */
export function readOrientation(b: Uint8Array): number {
  try {
    const done = runPlan(b, MODE_INSPECT, 0);
    const o = done[2];
    return o >= 1 && o <= 8 ? o : 1;
  } catch {
    return 1;
  }
}

/** Stored pixel size from the header (before any orientation is applied). */
export function imageSize(b: Uint8Array, format: ImageFormat): { width: number; height: number } {
  if (format !== "jpeg" && format !== "png" && format !== "webp" && format !== "gif") {
    throw new ImageError("unsupported", `cannot read the size of ${format}`);
  }
  const done = runPlan(b, MODE_INSPECT, formatToCode(format));
  if (done[3] === 0) throw malformed("missing_image_header");
  const dv = new DataView(done.buffer, done.byteOffset, done.byteLength);
  return { width: dv.getUint32(4, true), height: dv.getUint32(8, true) };
}
