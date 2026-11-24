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
