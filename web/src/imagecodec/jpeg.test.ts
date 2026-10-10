import { describe, expect, it } from "vitest";
import arith from "../testdata/images/arith.jpg?inline";
import gray from "../testdata/images/gray.jpg?inline";
import odd420 from "../testdata/images/odd420.jpg?inline";
import odd420o6 from "../testdata/images/odd420-o6.jpg?inline";
import prog from "../testdata/images/prog.jpg?inline";
import restart from "../testdata/images/restart.jpg?inline";
import s420 from "../testdata/images/s420.jpg?inline";
import s420o2 from "../testdata/images/s420-o2.jpg?inline";
import s420o3 from "../testdata/images/s420-o3.jpg?inline";
import s420o4 from "../testdata/images/s420-o4.jpg?inline";
import s420o5 from "../testdata/images/s420-o5.jpg?inline";
import s420o6 from "../testdata/images/s420-o6.jpg?inline";
import s420o7 from "../testdata/images/s420-o7.jpg?inline";
import s420o8 from "../testdata/images/s420-o8.jpg?inline";
import s422 from "../testdata/images/s422.jpg?inline";
import s422o2 from "../testdata/images/s422-o2.jpg?inline";
import s422o3 from "../testdata/images/s422-o3.jpg?inline";
import s422o4 from "../testdata/images/s422-o4.jpg?inline";
import s422o5 from "../testdata/images/s422-o5.jpg?inline";
import s422o6 from "../testdata/images/s422-o6.jpg?inline";
import s422o7 from "../testdata/images/s422-o7.jpg?inline";
import s422o8 from "../testdata/images/s422-o8.jpg?inline";
import s444 from "../testdata/images/s444.jpg?inline";
import { ImageError } from "./error";
import { decodeJpegCoefficients, rotateJpegLossless, type JpegCoefficients } from "./jpeg";
import { jpegSegments, readOrientation, stripMetadata } from "./metadata";
import { exifOrientation, fixture, segment, withSegments } from "../testdata/images/helpers";

function decode(b: Uint8Array): JpegCoefficients {
  const j = decodeJpegCoefficients(b);
  if (!j) throw new Error("not decodable");
  return j;
}

/**
 * Where each output block and coefficient comes from, written out per
 * orientation in pixel terms, independently of the module's
 * transpose-then-flip formulation.
 */
function expectedSource(o: number, X: number, Y: number, u: number, v: number, BW: number, BH: number) {
  const odd = (n: number) => (n & 1 ? -1 : 1);
  switch (o) {
    case 1:
      return { sx: X, sy: Y, su: u, sv: v, sign: 1 };
    case 2:
      return { sx: BW - 1 - X, sy: Y, su: u, sv: v, sign: odd(u) };
    case 3:
      return { sx: BW - 1 - X, sy: BH - 1 - Y, su: u, sv: v, sign: odd(u) * odd(v) };
    case 4:
      return { sx: X, sy: BH - 1 - Y, su: u, sv: v, sign: odd(v) };
    case 5:
      return { sx: Y, sy: X, su: v, sv: u, sign: 1 };
    case 6: // clockwise: output column X is source row BH-1-X
      return { sx: Y, sy: BH - 1 - X, su: v, sv: u, sign: odd(u) };
    case 7:
      return { sx: BW - 1 - Y, sy: BH - 1 - X, su: v, sv: u, sign: odd(u) * odd(v) };
    case 8: // counter-clockwise: output row Y is source column BW-1-Y
      return { sx: BW - 1 - Y, sy: X, su: v, sv: u, sign: odd(v) };
  }
  throw new Error("bad orientation");
}

function checkTransform(input: JpegCoefficients, out: JpegCoefficients, o: number) {
  const t = o >= 5;
  expect([out.width, out.height]).toEqual(t ? [input.height, input.width] : [input.width, input.height]);
  input.components.forEach((src, ci) => {
    const c = out.components[ci];
    expect([c.h, c.v]).toEqual(t ? [src.v, src.h] : [src.h, src.v]);
    expect([c.bw, c.bh]).toEqual(t ? [src.bh, src.bw] : [src.bw, src.bh]);
    const q = input.quant[src.tq]!.values;
    const qo = out.quant[c.tq]!.values;
    for (let Y = 0; Y < c.bh; Y++) {
      for (let X = 0; X < c.bw; X++) {
        for (let v = 0; v < 8; v++) {
          for (let u = 0; u < 8; u++) {
            const e = expectedSource(o, X, Y, u, v, src.bw, src.bh);
            const want = e.sign * src.coef[(e.sy * src.bw + e.sx) * 64 + e.sv * 8 + e.su];
            const got = c.coef[(Y * c.bw + X) * 64 + v * 8 + u];
            if (got !== want) throw new Error(`o${o} comp ${ci} block ${X},${Y} coef ${u},${v}: ${got} != ${want}`);
            // Dequantized values match too, so the pixels are the same.
            expect(qo[v * 8 + u]).toBe(q[e.sv * 8 + e.su]);
          }
        }
      }
    }
  });
}

/**
 * Compares the blocks that hold image pixels. Blocks entirely inside the
 * padding of a partial MCU are never displayed, and encoders fill them
 * as they like; jpegtran fills them differently from this module.
 */
function sameCoefficients(a: JpegCoefficients, b: JpegCoefficients) {
  expect([a.width, a.height]).toEqual([b.width, b.height]);
  const hmax = Math.max(...a.components.map((c) => c.h));
  const vmax = Math.max(...a.components.map((c) => c.v));
  a.components.forEach((c, i) => {
    const d = b.components[i];
    expect([c.h, c.v, c.bw, c.bh]).toEqual([d.h, d.v, d.bw, d.bh]);
    expect(a.quant[c.tq]!.values).toEqual(b.quant[d.tq]!.values);
    const visibleW = Math.ceil((a.width * c.h) / hmax / 8);
    const visibleH = Math.ceil((a.height * c.v) / vmax / 8);
    for (let y = 0; y < visibleH; y++) {
      for (let x = 0; x < visibleW; x++) {
        const at = (y * c.bw + x) * 64;
        expect(c.coef.subarray(at, at + 64), `comp ${i} block ${x},${y}`).toEqual(d.coef.subarray(at, at + 64));
      }
    }
  });
}

function markers(b: Uint8Array): number[] {
  return [...jpegSegments(b)].map((s) => s.marker);
}

describe("rotateJpegLossless", () => {
  const oracles = {
    s420: [s420, s420, s420o2, s420o3, s420o4, s420o5, s420o6, s420o7, s420o8],
    s422: [s422, s422, s422o2, s422o3, s422o4, s422o5, s422o6, s422o7, s422o8],
  };
  for (const [name, files] of Object.entries(oracles)) {
    for (let o = 1; o <= 8; o++) {
      it(`matches jpegtran -perfect for ${name} orientation ${o}`, () => {
        const out = rotateJpegLossless(fixture(files[0]), o)!;
        expect(out).toBeDefined();
        sameCoefficients(decode(out), decode(fixture(files[o])));
      });
    }
  }

  const cases = { s420, s422, s444, gray, restart };
  for (const [name, file] of Object.entries(cases)) {
    it(`moves every coefficient of ${name} where each orientation says`, () => {
      const input = decode(fixture(file));
      for (let o = 1; o <= 8; o++) {
        const out = rotateJpegLossless(fixture(file), o)!;
        checkTransform(input, decode(out), o);
        // A plain baseline file: SOF0, one scan, no metadata.
        const m = markers(out);
        expect(m.filter((x) => x === 0xc0)).toHaveLength(1);
        expect(m.filter((x) => x === 0xda)).toHaveLength(1);
        expect(m.filter((x) => x === 0xe1 || x === 0xfe)).toHaveLength(0);
      }
    });
  }

  it("keeps restart intervals", () => {
    const input = fixture(restart);
    expect(decode(input).restartInterval).toBe(1);
    const out = rotateJpegLossless(input, 6)!;
    expect(decode(out).restartInterval).toBe(1);
    // 4x3 MCUs of 16px: 11 restart markers, cycling through RST0..RST7.
    let rst = 0;
    for (let i = 0; i + 1 < out.length; i++) if (out[i] === 0xff && out[i + 1] >= 0xd0 && out[i + 1] <= 0xd7) rst++;
    expect(rst).toBe(11);
  });

  it("reads fill bytes before restart markers", () => {
    const input = fixture(restart);
    const scan = [...jpegSegments(input)].find((s) => s.marker === 0xda)!;
    // In the scan's data 0xFF is followed by 0x00 or a restart marker.
    const filled: number[] = [...input.subarray(0, scan.end)];
    for (let i = scan.end; i < input.length; i++) {
      if (input[i] === 0xff && input[i + 1] >= 0xd0 && input[i + 1] <= 0xd7) filled.push(0xff, 0xff);
      filled.push(input[i]);
    }
    const bytes = Uint8Array.from(filled);
    expect(bytes.length).toBe(input.length + 22);
    expect(markers(bytes)).toEqual(markers(input));
    expect(stripMetadata(bytes, "jpeg")).toEqual(bytes);
    expect(decode(bytes).components.map((c) => c.coef)).toEqual(decode(input).components.map((c) => c.coef));
    expect(rotateJpegLossless(bytes, 6)).toEqual(rotateJpegLossless(input, 6));
  });

  it("rotates 90 when only the unmoved edge is partial, like jpegtran", () => {
    // 40x32 in 16px MCUs: the partial column stays at the bottom after a
    // clockwise turn, but would have to move to the top for 270 or a flip.
    const input = fixture(odd420);
    sameCoefficients(decode(rotateJpegLossless(input, 6)!), decode(fixture(odd420o6)));
    expect(rotateJpegLossless(input, 5)).toBeDefined();
    expect(rotateJpegLossless(input, 4)).toBeDefined();
    for (const o of [2, 3, 7, 8]) expect(rotateJpegLossless(input, o)).toBeUndefined();
  });

  it("refuses what it cannot do perfectly", () => {
    expect(rotateJpegLossless(fixture(prog), 6)).toBeUndefined();
    expect(rotateJpegLossless(fixture(arith), 6)).toBeUndefined();
    expect(rotateJpegLossless(fixture(s420), 0)).toBeUndefined();
    expect(rotateJpegLossless(fixture(s420), 9)).toBeUndefined();
    // 12-bit precision in the frame header.
    const twelve = fixture(s420).slice();
    const sof = [...jpegSegments(twelve)].find((s) => s.marker === 0xc0)!;
    twelve[sof.start + 4] = 12;
    expect(rotateJpegLossless(twelve, 6)).toBeUndefined();
  });

  it("throws on broken data instead of guessing", () => {
    const b = fixture(s420);
    expect(() => rotateJpegLossless(b.subarray(0, b.length - 200), 6)).toThrow(ImageError);
    const garbled = b.slice();
    const sos = [...jpegSegments(garbled)].find((s) => s.marker === 0xda)!;
    garbled.fill(0xfe, sos.end, sos.dataEnd); // not a valid prefix code
    expect(() => rotateJpegLossless(garbled, 6)).toThrow(ImageError);
  });

  it("drops the EXIF orientation it applied", () => {
    const input = withSegments(fixture(s420), segment(0xe1, exifOrientation(6)));
    expect(readOrientation(input)).toBe(6);
    const out = rotateJpegLossless(input, 6)!;
    expect(readOrientation(out)).toBe(1);
    expect(markers(out)).not.toContain(0xe1);
    expect(markers(out)[0]).toBe(0xe0); // JFIF kept first
  });
});
