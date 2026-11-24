import { describe, expect, it } from "vitest";
import vectors from "../../testdata/images/vectors.json";
import { ImageError, imageSize, readOrientation, sniffFormat, stripMetadata, type ImageFormat } from "./images";

function fromBase64(s: string): Uint8Array {
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

const textEncoder = new TextEncoder();
function containsBytes(hay: Uint8Array, needle: string): boolean {
  const n = textEncoder.encode(needle);
  outer: for (let i = 0; i + n.length <= hay.length; i++) {
    for (let j = 0; j < n.length; j++) if (hay[i + j] !== n[j]) continue outer;
    return true;
  }
  return false;
}

describe("shared image parse & strip vectors (testdata/images/vectors.json)", () => {
  for (const tc of vectors.cases) {
    it(tc.name, () => {
      const input = fromBase64(tc.inputBase64);
      const fmt = sniffFormat(input);
      expect(fmt).toBe(tc.format);
      expect(readOrientation(input)).toBe(tc.orientation);

      if (tc.width !== undefined && tc.height !== undefined) {
        expect(imageSize(input, fmt)).toEqual({ width: tc.width, height: tc.height });
      } else if (tc.stripError === "malformed") {
        expect(() => imageSize(input, fmt as ImageFormat)).toThrow(ImageError);
      }

      if (tc.stripError) {
        if (tc.stripError === "unsupported") {
          expect(() => stripMetadata(input, fmt as ImageFormat)).toThrow(expect.objectContaining({ code: "unsupported" }));
        } else {
          expect(() => stripMetadata(input, fmt as ImageFormat)).toThrow(ImageError);
        }
      } else if (tc.expectedStrippedBase64) {
        const expected = fromBase64(tc.expectedStrippedBase64);
        const stripped = stripMetadata(input, fmt);
        expect(stripped).toEqual(expected);
        expect(readOrientation(stripped)).toBe(1);
        for (const bad of tc.forbiddenSubstrings ?? []) {
          expect(containsBytes(stripped, bad), `leaked ${bad}`).toBe(false);
        }
      }
    });
  }
});

describe("generated large-file NeedRead & window continuation across web shell", () => {
  function makeTiffOrient(orient: number, totalSize: number): Uint8Array {
    const b = new Uint8Array(Math.max(32, totalSize));
    b.set([0x4d, 0x4d, 0, 42, 0, 0, 0, 8, 0, 1, 0x01, 0x12, 0, 3, 0, 0, 0, 1, 0, orient, 0, 0]);
    return b;
  }

  function makePngChunk(type: string, payload: Uint8Array): Uint8Array {
    const out = new Uint8Array(12 + payload.length);
    const dv = new DataView(out.buffer);
    dv.setUint32(0, payload.length, false);
    for (let i = 0; i < 4; i++) out[4 + i] = type.charCodeAt(i);
    out.set(payload, 8);
    return out;
  }

  function makeRiffChunk(fourcc: string, payload: Uint8Array): Uint8Array {
    const pad = payload.length & 1;
    const out = new Uint8Array(8 + payload.length + pad);
    const dv = new DataView(out.buffer);
    for (let i = 0; i < 4; i++) out[i] = fourcc.charCodeAt(i);
    dv.setUint32(4, payload.length, true);
    out.set(payload, 8);
    return out;
  }

  function concatBytes(...parts: Uint8Array[]): Uint8Array {
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

  it("handles >64KB JPEG fill bytes, full-length APP1 with fill, and >64KB scan + post-scan COM", { timeout: 10_000 }, () => {
    const body = Uint8Array.from([
      0xff, 0xc0, 0, 11, 8, 0, 12, 0, 16, 1, 1, 0x11, 0,
      0xff, 0xda, 0, 8, 1, 1, 0, 0, 63, 0,
    ]);
    const scan = new Uint8Array(128 * 1024);
    scan.fill(0x22);
    const postCom = new Uint8Array(4 + 60000);
    postCom.set([0xff, 0xfe, (60002 >> 8) & 0xff, 60002 & 0xff]);
    const eoi = Uint8Array.from([0xff, 0xd9]);

    // 1. 70 KB 0xFF fill bytes before SOF
    const fillJpeg = concatBytes(Uint8Array.from([0xff, 0xd8]), new Uint8Array(70_000).fill(0xff), body, scan, postCom, eoi);
    expect(imageSize(fillJpeg, "jpeg")).toEqual({ width: 16, height: 12 });
    const fillStripped = stripMetadata(fillJpeg, "jpeg");
    expect(fillStripped).toEqual(concatBytes(Uint8Array.from([0xff, 0xd8]), body, scan, eoi));

    // 2. 5 B fill + full-length APP1 (segLen=65535) with EXIF orient=3
    const app1 = concatBytes(Uint8Array.from([0xff, 0xe1, 0xff, 0xff]), textEncoder.encode("Exif\0\0"), makeTiffOrient(3, 65535 - 2 - 6));
    const app1Jpeg = concatBytes(Uint8Array.from([0xff, 0xd8, 0xff, 0xff, 0xff, 0xff, 0xff]), app1, body, scan, postCom, eoi);
    expect(readOrientation(app1Jpeg)).toBe(3);
    expect(imageSize(app1Jpeg, "jpeg")).toEqual({ width: 16, height: 12 });
    const app1Stripped = stripMetadata(app1Jpeg, "jpeg");
    expect(app1Stripped).toEqual(concatBytes(Uint8Array.from([0xff, 0xd8]), body, scan, eoi));
    expect(readOrientation(app1Stripped)).toBe(1);
  });

  it("handles >64KB PNG eXIf and multi-chunk IDAT past initial window", { timeout: 10_000 }, () => {
    const sig = Uint8Array.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
    const ihdr = makePngChunk("IHDR", Uint8Array.from([0, 0, 0, 10, 0, 0, 0, 20, 8, 6, 0, 0, 0]));
    const exif = makePngChunk("eXIf", makeTiffOrient(6, 70_000));
    const idat = makePngChunk("IDAT", new Uint8Array(8 * 1024));
    const idats: Uint8Array[] = [];
    for (let i = 0; i < 24; i++) idats.push(idat);
    const txt = makePngChunk("tEXt", textEncoder.encode("Comment\0SECRET"));
    const iend = makePngChunk("IEND", new Uint8Array(0));
    const png = concatBytes(sig, ihdr, exif, ...idats, txt, iend);

    expect(readOrientation(png)).toBe(6);
    expect(imageSize(png, "png")).toEqual({ width: 10, height: 20 });
    const stripped = stripMetadata(png, "png");
    expect(stripped).toEqual(concatBytes(sig, ihdr, ...idats, iend));
    expect(readOrientation(stripped)).toBe(1);
  });

  it("handles >64KB WebP payload and trailing >64KB EXIF chunk", { timeout: 10_000 }, () => {
    const vp8xPayload = new Uint8Array(10);
    vp8xPayload[0] = 0x1c;
    vp8xPayload[4] = 9;
    vp8xPayload[7] = 19;
    const vp8lPayload = new Uint8Array(128 * 1024);
    vp8lPayload[0] = 0x2f;
    const chunks = concatBytes(
      makeRiffChunk("VP8X", vp8xPayload),
      makeRiffChunk("VP8L", vp8lPayload),
      makeRiffChunk("EXIF", makeTiffOrient(8, 70_000)),
      makeRiffChunk("XMP ", textEncoder.encode("<x:xmpmeta>SECRET</x:xmpmeta>")),
    );
    const webp = new Uint8Array(12 + chunks.length);
    webp.set(textEncoder.encode("RIFF"), 0);
    new DataView(webp.buffer).setUint32(4, 4 + chunks.length, true);
    webp.set(textEncoder.encode("WEBP"), 8);
    webp.set(chunks, 12);

    expect(readOrientation(webp)).toBe(8);
    expect(imageSize(webp, "webp")).toEqual({ width: 10, height: 20 });
    const stripped = stripMetadata(webp, "webp");
    expect(readOrientation(stripped)).toBe(1);
    expect(stripped[20]).toBe(0x10);
    expect(containsBytes(stripped, "SECRET")).toBe(false);
  });

  it("handles >64KB GIF comment and frame sub-blocks past initial window", { timeout: 10_000 }, () => {
    const head = concatBytes(textEncoder.encode("GIF89a"), Uint8Array.from([1, 0, 1, 0, 0, 0, 0]));
    // 70 KB comment sub-blocks (255 B each)
    const commentParts: Uint8Array[] = [Uint8Array.from([0x21, 0xfe])];
    const blk = new Uint8Array(256);
    blk[0] = 255;
    blk.fill(0x41, 1);
    for (let i = 0; i < 280; i++) commentParts.push(blk);
    commentParts.push(Uint8Array.from([0]));
    const frameParts: Uint8Array[] = [Uint8Array.from([0x2c, 0, 0, 0, 0, 1, 0, 1, 0, 0, 2])];
    for (let i = 0; i < 320; i++) frameParts.push(blk);
    frameParts.push(Uint8Array.from([0]));
    const frame = concatBytes(...frameParts);
    const gif = concatBytes(head, ...commentParts, frame, Uint8Array.from([0x3b]));

    expect(imageSize(gif, "gif")).toEqual({ width: 1, height: 1 });
    const stripped = stripMetadata(gif, "gif");
    expect(stripped).toEqual(concatBytes(head, frame, Uint8Array.from([0x3b])));
  });
});
