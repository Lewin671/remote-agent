import { Inflate } from "fflate";

/** Inflates raw DEFLATE, up to limit bytes out. Feed bounded slices so that
 * a rejected slice can overshoot by at most its DEFLATE expansion (1032x). */
export function inflateBounded(packed: Uint8Array, limit: number): Uint8Array {
  const chunks: Uint8Array[] = [];
  let size = 0;
  const inflate = new Inflate((chunk) => {
    size += chunk.length;
    if (size > limit) throw new RangeError("payload is too large");
    chunks.push(chunk);
  });
  const slice = 16 << 10;
  for (let at = 0; at < packed.length || at === 0; at += slice)
    inflate.push(packed.subarray(at, at + slice), at + slice >= packed.length);
  const result = new Uint8Array(size);
  let at = 0;
  for (const chunk of chunks) {
    result.set(chunk, at);
    at += chunk.length;
  }
  return result;
}
