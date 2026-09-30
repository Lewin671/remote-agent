// Container-level work on image files: recognizing the format, reading the
// EXIF orientation, finding the size, and removing metadata. None of it
// touches pixel data: segments and chunks that carry the image are copied
// byte for byte.

import { malformed, ImageError } from "./error";

export type ImageFormat = "jpeg" | "png" | "webp" | "gif" | "heic" | "avif" | "unknown";

/** The formats stripMetadata and imageSize understand; the ones sent as originals. */
export type PassthroughFormat = "jpeg" | "png" | "webp" | "gif";

const PNG_SIGNATURE = [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a];

function ascii(b: Uint8Array, at: number, n: number): string {
  let s = "";
  for (let i = at; i < at + n && i < b.length; i++) s += String.fromCharCode(b[i]);
  return s;
}

function u16be(b: Uint8Array, at: number): number {
  if (at + 2 > b.length) throw malformed("truncated");
  return (b[at] << 8) | b[at + 1];
}

function u32be(b: Uint8Array, at: number): number {
  if (at + 4 > b.length) throw malformed("truncated");
  return ((b[at] << 24) | (b[at + 1] << 16) | (b[at + 2] << 8) | b[at + 3]) >>> 0;
}

function u32le(b: Uint8Array, at: number): number {
  if (at + 4 > b.length) throw malformed("truncated");
  return (b[at] | (b[at + 1] << 8) | (b[at + 2] << 16) | (b[at + 3] << 24)) >>> 0;
}

function concatParts(parts: Uint8Array[]): Uint8Array {
  let n = 0;
  for (const p of parts) n += p.length;
  const out = new Uint8Array(n);
  let at = 0;
  for (const p of parts) {
    out.set(p, at);
    at += p.length;
  }
  return out;
}

/** Recognizes the format by the file header, never by name or MIME type. */
export function sniffFormat(b: Uint8Array): ImageFormat {
  if (b.length >= 3 && b[0] === 0xff && b[1] === 0xd8 && b[2] === 0xff) return "jpeg";
  if (b.length >= 8 && PNG_SIGNATURE.every((v, i) => b[i] === v)) return "png";
  const gif = ascii(b, 0, 6);
  if (gif === "GIF87a" || gif === "GIF89a") return "gif";
  if (b.length >= 12 && ascii(b, 0, 4) === "RIFF" && ascii(b, 8, 4) === "WEBP") return "webp";
  if (b.length >= 16 && ascii(b, 4, 4) === "ftyp") {
    // ISO BMFF: the major brand, then compatible brands up to the box end.
    const size = u32be(b, 0);
    const end = Math.min(b.length, Math.max(size, 16));
    const brands = [ascii(b, 8, 4)];
    for (let at = 16; at + 4 <= end; at += 4) brands.push(ascii(b, at, 4));
    // AVIF files also list the generic HEIF brand mif1, so check it first.
    if (brands.some((x) => x === "avif" || x === "avis")) return "avif";
    if (brands.some((x) => ["heic", "heix", "hevc", "hevx", "heim", "heis", "mif1", "msf1"].includes(x))) return "heic";
  }
  return "unknown";
}

// ---------------------------------------------------------------- JPEG

export interface JpegSegment {
  marker: number;
  /** Offset of the 0xFF that starts the marker (fill bytes before it skipped). */
  start: number;
  /** End of the marker segment (its length field included). */
  end: number;
  /** For SOS, the end of the entropy-coded data that follows; otherwise equals end. */
  dataEnd: number;
}

export const SOS = 0xda;
export const EOI = 0xd9;

/**
 * Walks every marker segment of a JPEG up to and including EOI, across all
 * scans. Throws on anything that does not parse, including a missing EOI:
 * a truncated file is not something to pass along.
 */
export function* jpegSegments(b: Uint8Array): Generator<JpegSegment> {
  if (sniffFormat(b) !== "jpeg") throw malformed("not a JPEG");
  let pos = 2;
  for (;;) {
    if (pos >= b.length) throw malformed("JPEG ends before EOI");
    if (b[pos] !== 0xff) throw malformed(`JPEG: expected a marker at ${pos}`);
    let m = pos + 1;
    while (m < b.length && b[m] === 0xff) m++; // fill bytes
    if (m >= b.length) throw malformed("JPEG ends before EOI");
    const marker = b[m];
    const start = m - 1;
    if (marker === EOI) {
      yield { marker, start, end: m + 1, dataEnd: m + 1 };
      return;
    }
    if (marker === 0x00 || marker === 0x01 || marker === 0xd8 || (marker >= 0xd0 && marker <= 0xd7)) {
      throw malformed(`JPEG: unexpected marker ${marker.toString(16)} at ${start}`);
    }
    const len = u16be(b, m + 1);
    const end = m + 1 + len;
    if (len < 2 || end > b.length) throw malformed("JPEG segment runs past the end");
    const dataEnd = marker === SOS ? entropyEnd(b, end) : end;
    yield { marker, start, end, dataEnd };
    pos = dataEnd;
  }
}

/** Finds the first real marker after entropy-coded data: not a stuffed 0xFF00, not RSTn. */
function entropyEnd(b: Uint8Array, pos: number): number {
  for (let i = pos; i + 1 < b.length; i++) {
    if (b[i] !== 0xff) continue;
    const next = b[i + 1];
    if (next === 0x00 || (next >= 0xd0 && next <= 0xd7)) {
      i++;
      continue;
    }
    return i;
  }
  throw malformed("JPEG scan data runs past the end");
}

function jpegAppId(b: Uint8Array, s: JpegSegment): string {
  return ascii(b, s.start + 4, Math.min(s.end - s.start - 4, 29));
}

/**
 * Which pre-image segments survive stripping. JFIF, ICC profiles and the
 * Adobe marker affect how colors decode and say nothing about the person;
 * every other APPn (Exif, XMP, IPTC/Photoshop, MPF, maker data) and COM goes.
 */
export function keepJpegSegment(b: Uint8Array, s: JpegSegment): boolean {
  const m = s.marker;
  if (m === 0xfe) return false;
  if (m < 0xe0 || m > 0xef) return true;
  const id = jpegAppId(b, s);
  if (m === 0xe0) return id.startsWith("JFIF\0");
  if (m === 0xe2) return id.startsWith("ICC_PROFILE\0");
  if (m === 0xee) return id.startsWith("Adobe");
  return false;
}

function stripJpeg(b: Uint8Array): Uint8Array {
  const parts: Uint8Array[] = [b.subarray(0, 2)];
  let sawScan = false;
  for (const s of jpegSegments(b)) {
    if (s.marker === SOS) sawScan = true;
    if (s.marker === EOI && !sawScan) throw malformed("JPEG has no image data");
    // Anything after EOI (MPF secondary images, gain maps, vendor
    // trailers) is dropped by stopping here.
    if (keepJpegSegment(b, s)) parts.push(b.subarray(s.start, s.dataEnd));
  }
  return concatParts(parts);
}

function jpegExif(b: Uint8Array): Uint8Array | undefined {
  for (const s of jpegSegments(b)) {
    if (s.marker === SOS) return undefined;
    if (s.marker === 0xe1 && ascii(b, s.start + 4, 6) === "Exif\0\0") return b.subarray(s.start + 10, s.end);
  }
  return undefined;
}

// ---------------------------------------------------------------- PNG

interface PngChunk {
  type: string;
  start: number;
  end: number;
}

function* pngChunks(b: Uint8Array): Generator<PngChunk> {
  if (sniffFormat(b) !== "png") throw malformed("not a PNG");
  let pos = 8;
  for (;;) {
    const len = u32be(b, pos);
    const end = pos + 12 + len;
    if (len > 0x7fffffff || end > b.length) throw malformed("PNG chunk runs past the end");
    const type = ascii(b, pos + 4, 4);
    if (pos === 8 && type !== "IHDR") throw malformed("PNG does not start with IHDR");
    yield { type, start: pos, end };
    if (type === "IEND") return;
    pos = end;
  }
}

// Text chunks carry arbitrary key/value metadata (often XMP), tIME the
// edit time; none of them affect how the image looks.
const PNG_DROP = new Set(["tEXt", "zTXt", "iTXt", "eXIf", "tIME"]);

function stripPng(b: Uint8Array): Uint8Array {
  const parts: Uint8Array[] = [b.subarray(0, 8)];
  let sawData = false;
  for (const c of pngChunks(b)) {
    if (c.type === "IDAT") sawData = true;
    if (!PNG_DROP.has(c.type)) parts.push(b.subarray(c.start, c.end));
  }
  if (!sawData) throw malformed("PNG has no IDAT");
  return concatParts(parts);
}

function pngExif(b: Uint8Array): Uint8Array | undefined {
  for (const c of pngChunks(b)) if (c.type === "eXIf") return b.subarray(c.start + 8, c.end - 4);
  return undefined;
}

// ---------------------------------------------------------------- WebP

interface RiffChunk {
  fourcc: string;
  /** Header start. */
  start: number;
  /** Payload start and end (padding excluded). */
  data: number;
  dataEnd: number;
}

function* webpChunks(b: Uint8Array): Generator<RiffChunk> {
  if (sniffFormat(b) !== "webp") throw malformed("not a WebP");
  const end = 8 + u32le(b, 4);
  if (end > b.length) throw malformed("WebP RIFF size runs past the end");
  let pos = 12;
  if (pos >= end) throw malformed("WebP has no chunks");
  while (pos < end) {
    if (pos + 8 > end) throw malformed("WebP chunk header runs past the end");
    const fourcc = ascii(b, pos, 4);
    const size = u32le(b, pos + 4);
    const dataEnd = pos + 8 + size;
    if (dataEnd > end) throw malformed("WebP chunk runs past the end");
    if (pos === 12 && fourcc !== "VP8 " && fourcc !== "VP8L" && fourcc !== "VP8X") {
      throw malformed("WebP does not start with VP8, VP8L or VP8X");
    }
    yield { fourcc, start: pos, data: pos + 8, dataEnd };
    pos = dataEnd + (size & 1);
  }
}

// VP8X flag bits announcing EXIF and XMP chunks.
const VP8X_EXIF = 0x08;
const VP8X_XMP = 0x04;

function stripWebp(b: Uint8Array): Uint8Array {
  const parts: Uint8Array[] = [];
  for (const c of webpChunks(b)) {
    if (c.fourcc === "EXIF" || c.fourcc === "XMP ") continue;
    // Copy the chunk and re-add its pad byte ourselves, so a file whose
    // last odd-sized chunk lacks padding still comes out well-formed.
    const chunk = b.slice(c.start, c.dataEnd);
    if (c.fourcc === "VP8X") {
      if (c.dataEnd - c.data < 10) throw malformed("WebP VP8X chunk too short");
      chunk[8] &= ~(VP8X_EXIF | VP8X_XMP);
    }
    parts.push(chunk);
    if ((c.dataEnd - c.data) & 1) parts.push(new Uint8Array(1));
  }
  const body = concatParts(parts);
  const out = new Uint8Array(12 + body.length);
  out.set(b.subarray(0, 12));
  new DataView(out.buffer).setUint32(4, out.length - 8, true);
  out.set(body, 12);
  return out;
}

function webpExif(b: Uint8Array): Uint8Array | undefined {
  for (const c of webpChunks(b)) if (c.fourcc === "EXIF") return b.subarray(c.data, c.dataEnd);
  return undefined;
}

// ---------------------------------------------------------------- GIF

/** Skips a chain of data sub-blocks, returning the offset after the terminator. */
function gifSubBlocks(b: Uint8Array, pos: number): number {
  for (;;) {
    if (pos >= b.length) throw malformed("GIF data runs past the end");
    const n = b[pos];
    pos += 1 + n;
    if (n === 0) return pos;
  }
}

// Application extensions that control looping; everything else in an
// application extension (XMP, vendor data) is metadata.
const GIF_KEEP_APPS = new Set(["NETSCAPE2.0", "ANIMEXTS1.0"]);

function stripGif(b: Uint8Array): Uint8Array {
  if (sniffFormat(b) !== "gif" || b.length < 13) throw malformed("not a GIF");
  const flags = b[10];
  let pos = 13 + (flags & 0x80 ? 3 << ((flags & 7) + 1) : 0);
  if (pos > b.length) throw malformed("GIF color table runs past the end");
  const parts: Uint8Array[] = [b.subarray(0, pos)];
  let frames = 0;
  for (;;) {
    if (pos >= b.length) throw malformed("GIF ends before the trailer");
    const intro = b[pos];
    if (intro === 0x3b) {
      if (frames === 0) throw malformed("GIF has no image");
      parts.push(b.subarray(pos, pos + 1));
      return concatParts(parts);
    }
    if (intro === 0x21) {
      if (pos + 2 > b.length) throw malformed("GIF extension runs past the end");
      const label = b[pos + 1];
      const end = gifSubBlocks(b, pos + 2);
      let keep = label !== 0xfe;
      if (label === 0xff) keep = b[pos + 2] === 11 && GIF_KEEP_APPS.has(ascii(b, pos + 3, 11));
      if (keep) parts.push(b.subarray(pos, end));
      pos = end;
    } else if (intro === 0x2c) {
      if (pos + 11 > b.length) throw malformed("GIF image descriptor runs past the end");
      const f = b[pos + 9];
      const lzw = pos + 10 + (f & 0x80 ? 3 << ((f & 7) + 1) : 0);
      // lzw is the LZW minimum code size byte; the image sub-blocks follow.
      const end = gifSubBlocks(b, lzw + 1);
      parts.push(b.subarray(pos, end));
      frames++;
      pos = end;
    } else {
      throw malformed(`GIF: unknown block ${intro.toString(16)} at ${pos}`);
    }
  }
}

// ---------------------------------------------------------------- public

/**
 * Removes metadata without touching pixel data (see the per-format rules
 * above each strip function). Throws ImageError("decode_failed") on
 * anything it cannot parse, and ImageError("unsupported") for formats it
 * does not handle; it never returns a guess.
 */
export function stripMetadata(b: Uint8Array, format: ImageFormat): Uint8Array {
  switch (format) {
    case "jpeg":
      return stripJpeg(b);
    case "png":
      return stripPng(b);
    case "webp":
      return stripWebp(b);
    case "gif":
      return stripGif(b);
    default:
      throw new ImageError("unsupported", `cannot strip metadata from ${format}`);
  }
}

/** The orientation from a TIFF (EXIF) structure, 1 when absent or invalid. */
function tiffOrientation(t: Uint8Array): number {
  // Some writers keep the JPEG-style "Exif\0\0" prefix in PNG/WebP chunks.
  if (ascii(t, 0, 6) === "Exif\0\0") t = t.subarray(6);
  const order = ascii(t, 0, 2);
  if (order !== "II" && order !== "MM") return 1;
  const le = order === "II";
  const v = new DataView(t.buffer, t.byteOffset, t.byteLength);
  if (v.getUint16(2, le) !== 42) return 1;
  const ifd = v.getUint32(4, le);
  const n = v.getUint16(ifd, le);
  for (let i = 0; i < n; i++) {
    const e = ifd + 2 + 12 * i;
    if (v.getUint16(e, le) !== 0x0112) continue;
    // Orientation is one SHORT, stored in the first two bytes of the value field.
    if (v.getUint16(e + 2, le) !== 3 || v.getUint32(e + 4, le) < 1) return 1;
    const o = v.getUint16(e + 8, le);
    return o >= 1 && o <= 8 ? o : 1;
  }
  return 1;
}

/**
 * The EXIF orientation (1..8) from a JPEG APP1, PNG eXIf or WebP EXIF
 * chunk; 1 when there is none or it does not parse. Never throws.
 */
export function readOrientation(b: Uint8Array): number {
  try {
    let tiff: Uint8Array | undefined;
    switch (sniffFormat(b)) {
      case "jpeg":
        tiff = jpegExif(b);
        break;
      case "png":
        tiff = pngExif(b);
        break;
      case "webp":
        tiff = webpExif(b);
        break;
    }
    return tiff ? tiffOrientation(tiff) : 1;
  } catch {
    return 1;
  }
}

/** Stored pixel size from the header (before any orientation is applied). */
export function imageSize(b: Uint8Array, format: ImageFormat): { width: number; height: number } {
  switch (format) {
    case "jpeg":
      for (const s of jpegSegments(b)) {
        const m = s.marker;
        if (m >= 0xc0 && m <= 0xcf && m !== 0xc4 && m !== 0xc8 && m !== 0xcc) {
          return { width: u16be(b, s.start + 7), height: u16be(b, s.start + 5) };
        }
        if (m === SOS) break;
      }
      throw malformed("JPEG has no frame header");
    case "png":
      return { width: u32be(b, 16), height: u32be(b, 20) };
    case "gif":
      if (b.length < 10) throw malformed("GIF too short");
      return { width: b[6] | (b[7] << 8), height: b[8] | (b[9] << 8) };
    case "webp":
      for (const c of webpChunks(b)) {
        const d = c.data;
        const n = c.dataEnd - d;
        if (c.fourcc === "VP8X" && n >= 10) {
          return { width: 1 + (b[d + 4] | (b[d + 5] << 8) | (b[d + 6] << 16)), height: 1 + (b[d + 7] | (b[d + 8] << 8) | (b[d + 9] << 16)) };
        }
        if (c.fourcc === "VP8L" && n >= 5 && b[d] === 0x2f) {
          const bits = u32le(b, d + 1);
          return { width: 1 + (bits & 0x3fff), height: 1 + ((bits >>> 14) & 0x3fff) };
        }
        if (c.fourcc === "VP8 " && n >= 10 && b[d + 3] === 0x9d && b[d + 4] === 0x01 && b[d + 5] === 0x2a) {
          return { width: (b[d + 6] | (b[d + 7] << 8)) & 0x3fff, height: (b[d + 8] | (b[d + 9] << 8)) & 0x3fff };
        }
      }
      throw malformed("WebP has no image header");
    default:
      throw new ImageError("unsupported", `cannot read the size of ${format}`);
  }
}
