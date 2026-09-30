import { describe, expect, it } from "vitest";
import {
  CHUNK_BYTES,
  chunkBytes,
  COMPRESSED_BUDGET,
  compressImage,
  ImageError,
  imageSize,
  MAX_CANVAS_PIXELS,
  MAX_EDGE,
  MAX_IMAGES,
  MAX_ORIGINAL_BYTES,
  orientationMatrix,
  prepareOriginal,
  readOrientation,
  sha256Hex,
  sniffFormat,
  stripMetadata,
  type ImageBackend,
} from "./images";
import { jpegSegments } from "./imagecodec/metadata";
import {
  concat,
  exifOrientation,
  exifTiff,
  fixture,
  GIF_FRAME,
  GIF_HEAD,
  gifExtension,
  includes,
  PNG_SIGNATURE,
  pngChunk,
  pngIDAT,
  pngIHDR,
  riff,
  riffChunk,
  SECRET_CAMERA,
  SECRET_GPS,
  segment,
  vp8x,
  withSegments,
} from "./testdata/images/helpers";
import odd420 from "./testdata/images/odd420.jpg?inline";
import prog from "./testdata/images/prog.jpg?inline";
import s420 from "./testdata/images/s420.jpg?inline";
import tinyWebp from "./testdata/images/tiny.webp?inline";

const text = new TextEncoder();
const XMP = "http://ns.adobe.com/xap/1.0/\0<x:xmpmeta>secret-xmp</x:xmpmeta>";

function ftyp(major: string, ...compat: string[]): Uint8Array {
  const body = text.encode("ftyp" + major + "\0\0\0\0" + compat.join(""));
  const size = body.length + 4;
  return concat(Uint8Array.from([0, 0, 0, size]), body, new Uint8Array(32));
}

/** The byte range from the first SOS to EOI: the image itself. */
function scanData(b: Uint8Array): Uint8Array {
  const segs = [...jpegSegments(b)];
  const sos = segs.find((s) => s.marker === 0xda)!;
  return b.subarray(sos.start, segs[segs.length - 1].end);
}

describe("sniffFormat", () => {
  it("recognizes formats by header", () => {
    expect(sniffFormat(fixture(s420))).toBe("jpeg");
    expect(sniffFormat(concat(PNG_SIGNATURE, pngIHDR()))).toBe("png");
    expect(sniffFormat(GIF_HEAD)).toBe("gif");
    expect(sniffFormat(fixture(tinyWebp))).toBe("webp");
    expect(sniffFormat(ftyp("heic", "mif1", "heic"))).toBe("heic");
    expect(sniffFormat(ftyp("mif1", "heic"))).toBe("heic");
    expect(sniffFormat(ftyp("avif", "mif1", "miaf"))).toBe("avif");
    expect(sniffFormat(ftyp("mif1", "avif"))).toBe("avif");
    expect(sniffFormat(ftyp("isom", "mp41"))).toBe("unknown");
    expect(sniffFormat(text.encode("<svg xmlns='http://www.w3.org/2000/svg'/>"))).toBe("unknown");
    expect(sniffFormat(new Uint8Array(0))).toBe("unknown");
  });
});

describe("readOrientation", () => {
  const jpeg = fixture(s420);
  it("reads JPEG Exif in both byte orders", () => {
    expect(readOrientation(withSegments(jpeg, segment(0xe1, exifOrientation(6, "MM"))))).toBe(6);
    expect(readOrientation(withSegments(jpeg, segment(0xe1, exifOrientation(3, "II"))))).toBe(3);
    // XMP in an earlier APP1 is not Exif.
    expect(readOrientation(withSegments(jpeg, segment(0xe1, XMP), segment(0xe1, exifOrientation(8, "II"))))).toBe(8);
  });

  it("reads PNG eXIf and WebP EXIF", () => {
    for (const [o, order] of [
      [6, "MM"],
      [7, "II"],
    ] as const) {
      const png = concat(PNG_SIGNATURE, pngIHDR(), pngChunk("eXIf", exifTiff(o, order)), pngIDAT(), pngChunk("IEND", ""));
      expect(readOrientation(png)).toBe(o);
      const webp = riff(vp8x(0x08, 3, 2), riffChunk("VP8L", fixture(tinyWebp).subarray(20)), riffChunk("EXIF", exifTiff(o, order)));
      expect(readOrientation(webp)).toBe(o);
    }
    // Some writers keep the "Exif\0\0" prefix inside the chunk.
    const png = concat(PNG_SIGNATURE, pngIHDR(), pngChunk("eXIf", exifOrientation(5)), pngIDAT(), pngChunk("IEND", ""));
    expect(readOrientation(png)).toBe(5);
  });

  it("falls back to 1", () => {
    expect(readOrientation(jpeg)).toBe(1);
    expect(readOrientation(withSegments(jpeg, segment(0xe1, exifOrientation(9))))).toBe(1);
    expect(readOrientation(withSegments(jpeg, segment(0xe1, exifOrientation(6).subarray(0, 20))))).toBe(1);
    expect(readOrientation(jpeg.subarray(0, 100))).toBe(1);
    expect(readOrientation(GIF_HEAD)).toBe(1);
    expect(readOrientation(new Uint8Array(0))).toBe(1);
  });
});

describe("stripMetadata", () => {
  it("removes JPEG metadata and keeps the image bytes", () => {
    const base = fixture(s420);
    const junk = [
      segment(0xe1, exifOrientation(6)),
      segment(0xe1, XMP),
      segment(0xed, "Photoshop 3.0\x008BIM\x04\x04secret-iptc"),
      segment(0xe2, "MPF\0secret-mpf"),
      segment(0xe0, "JFXX\0\x13thumbnail"),
      segment(0xeb, "vendor-data"),
      segment(0xfe, "secret comment"),
    ];
    const dirty = concat(withSegments(base, ...junk), text.encode("TRAILER after EOI"));
    const out = stripMetadata(dirty, "jpeg");
    expect(out).toEqual(base);
    for (const s of ["Exif", SECRET_CAMERA, "xmpmeta", "Photoshop", "MPF", "JFXX", "vendor", "comment", "TRAILER"]) expect(includes(out, s), s).toBe(false);
    expect(includes(out, SECRET_GPS)).toBe(false);
    expect(readOrientation(out)).toBe(1);
  });

  it("keeps JFIF, ICC profiles and the Adobe marker", () => {
    const base = fixture(s420);
    const icc = segment(0xe2, "ICC_PROFILE\0\x01\x01fake-profile");
    const adobe = segment(0xee, Uint8Array.from([..."Adobe"].map((c) => c.charCodeAt(0)).concat([0, 100, 0, 0, 0, 0, 1])));
    const out = stripMetadata(withSegments(base, segment(0xe1, exifOrientation(6)), icc, adobe), "jpeg");
    expect(out).toEqual(withSegments(base, icc, adobe));
    expect(scanData(out)).toEqual(scanData(base));
  });

  it("walks every scan of a progressive JPEG", () => {
    const p = fixture(prog);
    const out = stripMetadata(withSegments(p, segment(0xfe, "secret comment")), "jpeg");
    expect(out).toEqual(p);
  });

  it("removes PNG text, eXIf and tIME chunks", () => {
    const keep = [pngIHDR(), pngChunk("gAMA", Uint8Array.from([0, 0, 0xb1, 0x8f])), pngIDAT(), pngChunk("IEND", "")];
    const dirty = concat(
      PNG_SIGNATURE,
      keep[0],
      pngChunk("tEXt", "Comment\0secret comment"),
      pngChunk("eXIf", exifTiff(6)),
      pngChunk("iTXt", "XML:com.adobe.xmp\0\0\0\0\0<x:xmpmeta>secret-xmp</x:xmpmeta>"),
      pngChunk("zTXt", "Raw profile\0\0xx"),
      pngChunk("tIME", Uint8Array.from([7, 234, 9, 29, 12, 0, 0])),
      keep[1],
      keep[2],
      keep[3],
      text.encode("TRAILER"),
    );
    expect(readOrientation(dirty)).toBe(6);
    const out = stripMetadata(dirty, "png");
    expect(out).toEqual(concat(PNG_SIGNATURE, ...keep));
    expect(imageSize(out, "png")).toEqual({ width: 2, height: 1 });
  });

  it("removes WebP EXIF and XMP and fixes the header", () => {
    const tiny = fixture(tinyWebp);
    const vp8l = riffChunk("VP8L", tiny.subarray(20, 20 + (tiny[16] | (tiny[17] << 8))));
    // Alpha flag 0x10 stays; EXIF 0x08 and XMP 0x04 go. The XMP is odd-sized, so padded.
    const dirty = riff(vp8x(0x1c, 3, 2), vp8l, riffChunk("EXIF", exifTiff(8, "II")), riffChunk("XMP ", "<x:xmpmeta>odd</x:xmpmeta>"));
    expect(readOrientation(dirty)).toBe(8);
    const out = stripMetadata(dirty, "webp");
    expect(out).toEqual(riff(vp8x(0x10, 3, 2), vp8l));
    expect(new DataView(out.buffer).getUint32(4, true)).toBe(out.length - 8);
    expect(imageSize(out, "webp")).toEqual({ width: 3, height: 2 });
    expect(stripMetadata(tiny, "webp")).toEqual(tiny);
    expect(imageSize(tiny, "webp")).toEqual({ width: 3, height: 2 });
  });

  it("removes GIF comments and non-loop application extensions", () => {
    const loop = gifExtension(0xff, "NETSCAPE2.0", Uint8Array.from([1, 0, 0]));
    const gce = gifExtension(0xf9, Uint8Array.from([0, 0, 0, 0]));
    const dirty = concat(
      GIF_HEAD,
      loop,
      gifExtension(0xfe, "secret comment"),
      gifExtension(0xff, "XMP DataXMP", "<x:xmpmeta>secret-xmp</x:xmpmeta>"),
      gce,
      GIF_FRAME,
      Uint8Array.from([0x3b]),
      text.encode("TRAILER"),
    );
    const out = stripMetadata(dirty, "gif");
    expect(out).toEqual(concat(GIF_HEAD, loop, gce, GIF_FRAME, Uint8Array.from([0x3b])));
    expect(imageSize(out, "gif")).toEqual({ width: 1, height: 1 });
  });

  it("refuses what it cannot parse", () => {
    const jpeg = fixture(s420);
    const png = concat(PNG_SIGNATURE, pngIHDR(), pngIDAT(), pngChunk("IEND", ""));
    const cases: [Uint8Array, "jpeg" | "png" | "webp" | "gif"][] = [
      [jpeg.subarray(0, jpeg.length - 2), "jpeg"], // no EOI
      [jpeg.subarray(0, 300), "jpeg"],
      [withSegments(jpeg, Uint8Array.from([0xff, 0xe1, 0xff, 0xff])), "jpeg"], // segment runs past the end
      [png.subarray(0, png.length - 12), "png"], // no IEND
      [concat(PNG_SIGNATURE, pngIHDR(), pngChunk("IEND", "")), "png"], // no IDAT
      [png, "jpeg"],
      [fixture(tinyWebp).subarray(0, 40), "webp"],
      [concat(GIF_HEAD, GIF_FRAME), "gif"], // no trailer
      [concat(GIF_HEAD, Uint8Array.from([0x3b])), "gif"], // no frame
    ];
    for (const [b, f] of cases) {
      expect(() => stripMetadata(b, f)).toThrow(ImageError);
    }
    expect(() => stripMetadata(ftyp("heic"), "heic")).toThrow(expect.objectContaining({ code: "unsupported" }));
  });
});

describe("bytes", () => {
  it("splits into chunks that cover everything once", () => {
    const b = Uint8Array.from({ length: 2500 }, (_, i) => i & 0xff);
    const chunks = chunkBytes(b, 1000);
    expect(chunks.map((c) => [c.offset, c.data.length])).toEqual([
      [0, 1000],
      [1000, 1000],
      [2000, 500],
    ]);
    expect(concat(...chunks.map((c) => c.data))).toEqual(b);
    expect(chunkBytes(b.subarray(0, 2000), 1000)).toHaveLength(2);
    expect(chunkBytes(new Uint8Array(0), 1000)).toEqual([]);
    expect(() => chunkBytes(b, 0)).toThrow();
    // The largest original goes in 27 pieces.
    expect(Math.ceil(MAX_ORIGINAL_BYTES / CHUNK_BYTES)).toBe(27);
  });

  it("hashes with SHA-256", async () => {
    expect(await sha256Hex(text.encode("abc"))).toBe("ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad");
  });

  it("has the limits architecture.md sets", () => {
    expect(MAX_IMAGES).toBe(6);
    expect(MAX_EDGE).toBe(2048);
    expect(MAX_ORIGINAL_BYTES).toBe(20 * 1024 * 1024);
    // Room for header, nonce, tag and Protobuf framing under the 1 MiB envelope.
    expect(COMPRESSED_BUDGET).toBeLessThanOrEqual(1024 * 1024 - 64 * 1024);
    expect(CHUNK_BYTES).toBeLessThanOrEqual(COMPRESSED_BUDGET);
  });
});

describe("orientationMatrix", () => {
  // Where source point (x, y) of a w x h image lands once shown upright.
  const upright: Record<number, (x: number, y: number, w: number, h: number) => [number, number]> = {
    1: (x, y) => [x, y],
    2: (x, y, w) => [w - x, y],
    3: (x, y, w, h) => [w - x, h - y],
    4: (x, y, _w, h) => [x, h - y],
    5: (x, y) => [y, x],
    6: (x, y, _w, h) => [h - y, x], // clockwise: top-left goes to top-right
    7: (x, y, w, h) => [h - y, w - x],
    8: (x, y, w) => [y, w - x], // counter-clockwise: top-left goes to bottom-left
  };
  it("draws each orientation upright", () => {
    const [w, h] = [40, 30];
    for (let o = 1; o <= 8; o++) {
      const [a, b, c, d, e, f] = orientationMatrix(o, w, h);
      for (const [x, y] of [
        [0, 0],
        [w, 0],
        [0, h],
        [7, 3],
      ]) {
        expect([a * x + c * y + e, b * x + d * y + f], `o${o} (${x},${y})`).toEqual(upright[o](x, y, w, h));
      }
    }
  });
});

// ------------------------------------------------------------ canvas paths

interface Call {
  op: string;
  args: unknown[];
}

/**
 * A backend that records what it is asked to do. Decoded images have the
 * given size; encode returns bytes of the size sizeOf picks.
 */
function fakeBackend(width: number, height: number, opts: { alpha?: boolean; sizeOf?: (type: string, quality: number | undefined, w: number, h: number) => number } = {}) {
  const calls: Call[] = [];
  const decoded: Uint8Array[] = [];
  let closed = 0;
  const backend: ImageBackend = {
    async decode(blob) {
      decoded.push(new Uint8Array(await blob.arrayBuffer()));
      calls.push({ op: "decode", args: [] });
      return { width, height, close: () => void closed++ };
    },
    draw(_img, orientation, w, h) {
      calls.push({ op: "draw", args: [orientation, w, h] });
      const turned = orientation >= 5;
      return {
        width: turned ? h : w,
        height: turned ? w : h,
        hasAlpha: () => {
          calls.push({ op: "hasAlpha", args: [] });
          return !!opts.alpha;
        },
        async encode(type, quality) {
          calls.push({ op: "encode", args: [type, quality] });
          return new Uint8Array(opts.sizeOf ? opts.sizeOf(type, quality, w, h) : 1000);
        },
      };
    },
  };
  return { backend, calls, decoded, closed: () => closed };
}

const ops = (calls: Call[]) => calls.map((c) => [c.op, ...c.args]);

describe("compressImage", () => {
  it("turns a phone photo upright, fits it in MAX_EDGE and strips it first", async () => {
    const photo = withSegments(fixture(s420), segment(0xe1, exifOrientation(6)));
    const f = fakeBackend(4032, 3024);
    const out = await compressImage(new Blob([photo as Uint8Array<ArrayBuffer>]), f.backend);
    expect(out).toMatchObject({ format: "jpeg", width: 1536, height: 2048 });
    expect(ops(f.calls)).toEqual([
      ["decode"],
      ["draw", 6, 2048, 1536],
      ["encode", "image/jpeg", 0.85],
    ]);
    // The browser never sees the EXIF, so it cannot rotate a second time.
    expect(includes(f.decoded[0], "Exif")).toBe(false);
    expect(f.closed()).toBe(1);
  });

  it("lowers quality, then size, until it fits the budget", async () => {
    const f = fakeBackend(1000, 500, { sizeOf: (_t, q, w) => (w > 600 || q! > 0.6 ? COMPRESSED_BUDGET + 1 : COMPRESSED_BUDGET) });
    const out = await compressImage(new Blob([fixture(s420) as Uint8Array<ArrayBuffer>]), f.backend);
    expect(out).toMatchObject({ format: "jpeg", width: 563, height: 281 });
    expect(ops(f.calls)).toEqual([
      ["decode"],
      ["draw", 1, 1000, 500],
      ["encode", "image/jpeg", 0.85],
      ["encode", "image/jpeg", 0.75],
      ["encode", "image/jpeg", 0.65],
      ["encode", "image/jpeg", 0.55],
      ["draw", 1, 750, 375],
      ["encode", "image/jpeg", 0.85],
      ["encode", "image/jpeg", 0.75],
      ["encode", "image/jpeg", 0.65],
      ["encode", "image/jpeg", 0.55],
      ["draw", 1, 563, 281],
      ["encode", "image/jpeg", 0.85],
      ["encode", "image/jpeg", 0.75],
      ["encode", "image/jpeg", 0.65],
      ["encode", "image/jpeg", 0.55],
    ]);
  });

  it("keeps transparency as PNG", async () => {
    const png = concat(PNG_SIGNATURE, pngIHDR(), pngIDAT(), pngChunk("IEND", ""));
    const f = fakeBackend(300, 200, { alpha: true });
    const out = await compressImage(new Blob([png as Uint8Array<ArrayBuffer>]), f.backend);
    expect(out).toMatchObject({ format: "png", width: 300, height: 200 });
    expect(ops(f.calls)).toEqual([["decode"], ["draw", 1, 300, 200], ["hasAlpha"], ["encode", "image/png", undefined]]);
  });

  it("hands formats it cannot parse to the browser as they are", async () => {
    const heic = ftyp("heic", "mif1");
    const f = fakeBackend(3000, 4000);
    const out = await compressImage(new Blob([heic as Uint8Array<ArrayBuffer>]), f.backend);
    expect(f.decoded[0]).toEqual(heic);
    expect(out).toMatchObject({ format: "jpeg", width: 1536, height: 2048 });
    expect(f.calls[1]).toEqual({ op: "draw", args: [1, 1536, 2048] });
  });
});

describe("prepareOriginal", () => {
  const blob = (b: Uint8Array) => new Blob([b as Uint8Array<ArrayBuffer>]);

  it("refuses files over 20 MB", async () => {
    const f = fakeBackend(1, 1);
    await expect(prepareOriginal(blob(new Uint8Array(MAX_ORIGINAL_BYTES + 1)), f.backend)).rejects.toMatchObject({ code: "too_large", message: "图片超过 20 MB" });
  });

  it("strips an upright JPEG without touching its pixels", async () => {
    const f = fakeBackend(1, 1);
    const base = fixture(s420);
    const out = await prepareOriginal(blob(withSegments(base, segment(0xe1, exifOrientation(1)), segment(0xfe, "note"))), f.backend);
    expect(out).toEqual({ bytes: base, format: "jpeg", width: 48, height: 32, reencoded: false });
    expect(f.calls).toEqual([]);
  });

  it("rotates a JPEG losslessly when it can", async () => {
    const f = fakeBackend(1, 1);
    const out = await prepareOriginal(blob(withSegments(fixture(s420), segment(0xe1, exifOrientation(6)))), f.backend);
    expect(out).toMatchObject({ format: "jpeg", width: 32, height: 48, reencoded: false });
    expect(readOrientation(out.bytes)).toBe(1);
    expect(includes(out.bytes, SECRET_CAMERA)).toBe(false);
    expect(f.calls).toEqual([]);
  });

  it("re-encodes a JPEG at full quality when rotation cannot be lossless", async () => {
    const f = fakeBackend(40, 32);
    const out = await prepareOriginal(blob(withSegments(fixture(odd420), segment(0xe1, exifOrientation(8)))), f.backend);
    expect(out).toMatchObject({ format: "jpeg", width: 32, height: 40, reencoded: true });
    expect(ops(f.calls)).toEqual([["decode"], ["draw", 8, 40, 32], ["encode", "image/jpeg", 1]]);
    expect(includes(f.decoded[0], "Exif")).toBe(false);
  });

  it("re-encodes a rotated PNG losslessly as PNG, and only strips an upright one", async () => {
    const plain = concat(PNG_SIGNATURE, pngIHDR(), pngIDAT(), pngChunk("IEND", ""));
    const rotated = concat(PNG_SIGNATURE, pngIHDR(), pngChunk("eXIf", exifTiff(6)), pngIDAT(), pngChunk("IEND", ""));
    let f = fakeBackend(2, 1);
    expect(await prepareOriginal(blob(rotated), f.backend)).toMatchObject({ format: "png", width: 1, height: 2, reencoded: true });
    expect(ops(f.calls)).toEqual([["decode"], ["draw", 6, 2, 1], ["encode", "image/png", undefined]]);
    f = fakeBackend(2, 1);
    const withText = concat(PNG_SIGNATURE, pngIHDR(), pngChunk("tEXt", "a\0b"), pngIDAT(), pngChunk("IEND", ""));
    expect(await prepareOriginal(blob(withText), f.backend)).toEqual({ bytes: plain, format: "png", width: 2, height: 1, reencoded: false });
  });

  it("converts HEIC to JPEG at the original size", async () => {
    const f = fakeBackend(4032, 3024);
    const out = await prepareOriginal(blob(ftyp("heic", "mif1")), f.backend);
    expect(out).toMatchObject({ format: "jpeg", width: 4032, height: 3024, reencoded: true });
    expect(ops(f.calls)).toEqual([["decode"], ["draw", 1, 4032, 3024], ["hasAlpha"], ["encode", "image/jpeg", 0.95]]);
  });

  it("refuses originals too large for a canvas", async () => {
    const f = fakeBackend(8064, 6048);
    expect(8064 * 6048).toBeGreaterThan(MAX_CANVAS_PIXELS);
    await expect(prepareOriginal(blob(ftyp("heic")), f.backend)).rejects.toMatchObject({ code: "too_large" });
    expect(f.closed()).toBe(1);
  });

  it("never passes a malformed file through", async () => {
    const f = fakeBackend(48, 32);
    const b = fixture(s420);
    const out = await prepareOriginal(blob(b.subarray(0, b.length - 2)), f.backend);
    expect(out).toMatchObject({ format: "jpeg", reencoded: true });
  });
});
