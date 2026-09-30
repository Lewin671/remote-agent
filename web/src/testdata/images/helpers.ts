// Byte builders shared by the image tests.

export function fixture(dataUrl: string): Uint8Array {
  return Uint8Array.from(atob(dataUrl.slice(dataUrl.indexOf(",") + 1)), (c) => c.charCodeAt(0));
}

/** Inserts marker segments right after SOI, as cameras place APP1. */
export function withSegments(jpeg: Uint8Array, ...segs: Uint8Array[]): Uint8Array {
  const parts = [jpeg.subarray(0, 2), ...segs, jpeg.subarray(2)];
  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
  let at = 0;
  for (const p of parts) {
    out.set(p, at);
    at += p.length;
  }
  return out;
}

export function segment(marker: number, payload: Uint8Array | string): Uint8Array {
  const body = typeof payload === "string" ? new TextEncoder().encode(payload) : payload;
  return Uint8Array.from([0xff, marker, (body.length + 2) >> 8, (body.length + 2) & 0xff, ...body]);
}

const enc = new TextEncoder();

export function concat(...parts: Uint8Array[]): Uint8Array {
  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
  let at = 0;
  for (const p of parts) {
    out.set(p, at);
    at += p.length;
  }
  return out;
}

export function includes(hay: Uint8Array, needle: Uint8Array | string): boolean {
  const n = typeof needle === "string" ? enc.encode(needle) : needle;
  outer: for (let i = 0; i + n.length <= hay.length; i++) {
    for (let j = 0; j < n.length; j++) if (hay[i + j] !== n[j]) continue outer;
    return true;
  }
  return false;
}

/** The camera model and GPS latitude seconds planted in exifTiff, to look for after stripping. */
export const SECRET_CAMERA = "SECRETCAM";
export const SECRET_GPS = Uint8Array.from([0, 0, 0x10, 0x92]); // 4242, big-endian

/**
 * A TIFF (EXIF) structure like a phone writes: IFD0 with Make, Orientation
 * and a pointer to a GPS IFD holding a latitude.
 */
export function exifTiff(o: number, order: "II" | "MM" = "MM"): Uint8Array {
  const le = order === "II";
  const t = new DataView(new ArrayBuffer(114));
  const entry = (at: number, tag: number, type: number, count: number, value: number, short = false) => {
    t.setUint16(at, tag, le);
    t.setUint16(at + 2, type, le);
    t.setUint32(at + 4, count, le);
    if (short) t.setUint16(at + 8, value, le);
    else t.setUint32(at + 8, value, le);
  };
  t.setUint8(0, order.charCodeAt(0));
  t.setUint8(1, order.charCodeAt(1));
  t.setUint16(2, 42, le);
  t.setUint32(4, 8, le);
  t.setUint16(8, 3, le);
  entry(10, 0x010f, 2, 10, 50); // Make
  entry(22, 0x0112, 3, 1, o, true); // Orientation
  entry(34, 0x8825, 4, 1, 60); // GPSInfo IFD
  enc.encodeInto(SECRET_CAMERA + "\0", new Uint8Array(t.buffer, 50, 10));
  t.setUint16(60, 2, le);
  entry(62, 1, 2, 2, 0); // GPSLatitudeRef "N"
  t.setUint8(70, 0x4e);
  entry(74, 2, 5, 3, 90); // GPSLatitude, three rationals
  const lat = [31, 1, 14, 1, 4242, 100];
  lat.forEach((v, i) => t.setUint32(90 + 4 * i, v, le));
  return new Uint8Array(t.buffer);
}

/** An Exif APP1 payload. */
export function exifOrientation(o: number, order: "II" | "MM" = "MM"): Uint8Array {
  return concat(enc.encode("Exif\0\0"), exifTiff(o, order));
}

// ---------------------------------------------------------------- PNG

const crcTable = Array.from({ length: 256 }, (_, n) => {
  let c = n;
  for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
  return c >>> 0;
});

function crc32(b: Uint8Array): number {
  let c = 0xffffffff;
  for (const x of b) c = crcTable[(c ^ x) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
}

function u32(v: number): Uint8Array {
  return Uint8Array.from([v >>> 24, (v >>> 16) & 0xff, (v >>> 8) & 0xff, v & 0xff]);
}

export function pngChunk(type: string, data: Uint8Array | string): Uint8Array {
  const d = typeof data === "string" ? enc.encode(data) : data;
  const td = concat(enc.encode(type), d);
  return concat(u32(d.length), td, u32(crc32(td)));
}

/** A 2x1 RGBA PNG (one opaque, one half-transparent pixel), stored without compression. */
export function pngIHDR(): Uint8Array {
  return pngChunk("IHDR", concat(u32(2), u32(1), Uint8Array.from([8, 6, 0, 0, 0])));
}

export function pngIDAT(): Uint8Array {
  const raw = Uint8Array.from([0, 255, 0, 0, 255, 0, 0, 255, 128]);
  let a = 1;
  let b = 0;
  for (const x of raw) {
    a = (a + x) % 65521;
    b = (b + a) % 65521;
  }
  // zlib header, one final stored block, Adler-32.
  const z = concat(Uint8Array.from([0x78, 0x01, 1, raw.length, 0, ~raw.length & 0xff, 0xff]), raw, u32(((b << 16) | a) >>> 0));
  return pngChunk("IDAT", z);
}

export const PNG_SIGNATURE = Uint8Array.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);

// ---------------------------------------------------------------- WebP

export function riffChunk(fourcc: string, data: Uint8Array | string): Uint8Array {
  const d = typeof data === "string" ? enc.encode(data) : data;
  const size = Uint8Array.from([d.length & 0xff, (d.length >> 8) & 0xff, (d.length >> 16) & 0xff, d.length >>> 24]);
  return concat(enc.encode(fourcc), size, d, d.length & 1 ? new Uint8Array(1) : new Uint8Array(0));
}

export function riff(...chunks: Uint8Array[]): Uint8Array {
  const body = concat(enc.encode("WEBP"), ...chunks);
  return concat(enc.encode("RIFF"), Uint8Array.from([body.length & 0xff, (body.length >> 8) & 0xff, (body.length >> 16) & 0xff, body.length >>> 24]), body);
}

/** VP8X for a w x h canvas with the given flags. */
export function vp8x(flags: number, w: number, h: number): Uint8Array {
  const d = new Uint8Array(10);
  d[0] = flags;
  d.set([(w - 1) & 0xff, ((w - 1) >> 8) & 0xff, (w - 1) >> 16], 4);
  d.set([(h - 1) & 0xff, ((h - 1) >> 8) & 0xff, (h - 1) >> 16], 7);
  return riffChunk("VP8X", d);
}

// ---------------------------------------------------------------- GIF

export function gifExtension(label: number, ...blocks: (Uint8Array | string)[]): Uint8Array {
  const parts = blocks.map((b) => {
    const d = typeof b === "string" ? enc.encode(b) : b;
    return concat(Uint8Array.from([d.length]), d);
  });
  return concat(Uint8Array.from([0x21, label]), ...parts, new Uint8Array(1));
}

/** Header and global color table of a 1x1 GIF, and its one frame. */
export const GIF_HEAD = Uint8Array.from([..."GIF89a"].map((c) => c.charCodeAt(0)).concat([1, 0, 1, 0, 0x80, 0, 0, 255, 255, 255, 0, 0, 0]));
export const GIF_FRAME = Uint8Array.from([0x2c, 0, 0, 0, 0, 1, 0, 1, 0, 0, 2, 2, 0x44, 0x01, 0]);
