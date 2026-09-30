// Lossless JPEG transforms, like `jpegtran -perfect`: the entropy-coded DCT
// coefficients are decoded, moved and sign-flipped block by block, and
// Huffman-coded again with the original quantization, so every decoded
// pixel is exactly the rotated pixel of the input.
//
// Only baseline and extended sequential Huffman JPEGs with 8-bit samples
// and a single scan holding all components are handled: that is what
// cameras and phones write. Everything else yields undefined and the
// caller re-encodes instead.

import { ImageError } from "./error";
import { malformed } from "./error";
import { EOI, jpegSegments, keepJpegSegment, SOS } from "./metadata";

/** Natural (row-major) index of the k-th coefficient in zigzag order. */
export const ZIGZAG = Uint8Array.from([
  0, 1, 8, 16, 9, 2, 3, 10, 17, 24, 32, 25, 18, 11, 4, 5, 12, 19, 26, 33, 40, 48, 41, 34, 27, 20, 13, 6, 7, 14, 21, 28, 35, 42, 49, 56, 57, 50, 43, 36, 29, 22, 15, 23,
  30, 37, 44, 51, 58, 59, 52, 45, 38, 31, 39, 46, 53, 60, 61, 54, 47, 55, 62, 63,
]);

export interface JpegComponent {
  id: number;
  /** Horizontal and vertical sampling factors. */
  h: number;
  v: number;
  /** Quantization, DC and AC Huffman table slots. */
  tq: number;
  td: number;
  ta: number;
  /** Stored block grid, padding blocks of partial MCUs included. */
  bw: number;
  bh: number;
  /** bw*bh blocks of 64 quantized coefficients in natural order, row-major blocks. */
  coef: Int16Array;
}

export interface JpegCoefficients {
  /** SOF marker: 0xC0 baseline or 0xC1 extended sequential. */
  sof: number;
  width: number;
  height: number;
  components: JpegComponent[];
  /** Quantization tables by slot, natural order, with their precision (0: 8-bit, 1: 16-bit). */
  quant: ({ precision: number; values: Uint16Array } | undefined)[];
  restartInterval: number;
  /** Colour-related segments carried over verbatim (JFIF, ICC profile, Adobe). */
  extra: Uint8Array[];
}

// ------------------------------------------------------------ decoding

interface DecodeTable {
  maxcode: Int32Array;
  mincode: Int32Array;
  valptr: Int32Array;
  values: Uint8Array;
}

function buildDecodeTable(counts: Uint8Array, values: Uint8Array): DecodeTable {
  const maxcode = new Int32Array(17).fill(-1);
  const mincode = new Int32Array(17);
  const valptr = new Int32Array(17);
  let code = 0;
  let k = 0;
  for (let l = 1; l <= 16; l++) {
    valptr[l] = k;
    mincode[l] = code;
    code += counts[l - 1];
    k += counts[l - 1];
    if (code > 1 << l) throw malformed("JPEG Huffman table is over-subscribed");
    if (counts[l - 1]) maxcode[l] = code - 1;
    code <<= 1;
  }
  return { maxcode, mincode, valptr, values };
}

class BitReader {
  private acc = 0;
  private n = 0;
  /** Zero bytes fed in past the end of the data; reading them means the data is truncated. */
  private fake = 0;
  private atMarker = false;
  pos: number;
  private readonly b: Uint8Array;
  private readonly end: number;

  constructor(b: Uint8Array, pos: number, end: number) {
    this.b = b;
    this.pos = pos;
    this.end = end;
  }

  private fill(): void {
    while (this.n <= 24) {
      let byte = 0;
      if (this.atMarker || this.pos >= this.end) {
        this.fake++;
      } else {
        byte = this.b[this.pos];
        if (byte === 0xff) {
          if (this.b[this.pos + 1] === 0x00) {
            this.pos += 2;
          } else {
            this.atMarker = true;
            byte = 0;
            this.fake++;
          }
        } else {
          this.pos++;
        }
      }
      this.acc = (this.acc << 8) | byte;
      this.n += 8;
    }
  }

  bits(count: number): number {
    if (count === 0) return 0;
    if (this.n < count) this.fill();
    this.n -= count;
    const v = (this.acc >>> this.n) & ((1 << count) - 1);
    this.check();
    return v;
  }

  decode(t: DecodeTable): number {
    if (this.n < 16) this.fill();
    const peek = (this.acc >>> (this.n - 16)) & 0xffff;
    for (let l = 1; l <= 16; l++) {
      const c = peek >>> (16 - l);
      if (c <= t.maxcode[l]) {
        this.n -= l;
        this.check();
        return t.values[t.valptr[l] + c - t.mincode[l]];
      }
    }
    throw malformed("JPEG: invalid Huffman code");
  }

  private check(): void {
    if (this.n < this.fake * 8) throw malformed("JPEG scan data is truncated");
  }

  /** Drops the padding bits and consumes the expected RSTn marker. */
  restart(expected: number): void {
    this.acc = 0;
    this.n = 0;
    this.fake = 0;
    this.atMarker = false;
    const b = this.b;
    if (this.pos + 1 >= this.end || b[this.pos] !== 0xff || b[this.pos + 1] !== 0xd0 + expected) {
      throw malformed("JPEG: missing restart marker");
    }
    this.pos += 2;
  }
}

function extend(v: number, s: number): number {
  return v < 1 << (s - 1) ? v - (1 << s) + 1 : v;
}

/**
 * Decodes the quantized DCT coefficients. Returns undefined for JPEGs this
 * module does not transform (progressive, arithmetic, lossless,
 * hierarchical, 12-bit, multi-scan, DNL); throws ImageError on malformed data.
 */
export function decodeJpegCoefficients(b: Uint8Array): JpegCoefficients | undefined {
  const quant: JpegCoefficients["quant"] = [];
  const dc: (DecodeTable | undefined)[] = [];
  const ac: (DecodeTable | undefined)[] = [];
  const extra: Uint8Array[] = [];
  let restartInterval = 0;
  let frame: Omit<JpegCoefficients, "quant" | "restartInterval" | "extra"> | undefined;
  let scanned = false;

  for (const s of jpegSegments(b)) {
    const m = s.marker;
    const p = s.start + 4; // payload
    if (m === EOI) break;
    if (scanned) {
      // A second scan means a multi-scan sequential file; DNL means the
      // height is only known afterwards. Neither is worth supporting.
      if (m === SOS || m === 0xdc) return undefined;
      continue;
    }
    if (m === 0xdb) {
      for (let at = p; at < s.end; ) {
        const precision = b[at] >> 4;
        const slot = b[at] & 15;
        const size = precision ? 128 : 64;
        if (slot > 3 || precision > 1 || at + 1 + size > s.end) throw malformed("JPEG: bad DQT");
        const values = new Uint16Array(64);
        for (let k = 0; k < 64; k++) values[ZIGZAG[k]] = precision ? (b[at + 1 + 2 * k] << 8) | b[at + 2 + 2 * k] : b[at + 1 + k];
        quant[slot] = { precision, values };
        at += 1 + size;
      }
    } else if (m === 0xc4) {
      for (let at = p; at < s.end; ) {
        const cls = b[at] >> 4;
        const slot = b[at] & 15;
        if (cls > 1 || slot > 3 || at + 17 > s.end) throw malformed("JPEG: bad DHT");
        const counts = b.subarray(at + 1, at + 17);
        let total = 0;
        for (const c of counts) total += c;
        if (at + 17 + total > s.end) throw malformed("JPEG: bad DHT");
        const table = buildDecodeTable(counts, b.slice(at + 17, at + 17 + total));
        (cls ? ac : dc)[slot] = table;
        at += 17 + total;
      }
    } else if (m === 0xdd) {
      restartInterval = (b[p] << 8) | b[p + 1];
    } else if (m === 0xc0 || m === 0xc1) {
      if (frame) throw malformed("JPEG has two frame headers");
      if (b[p] !== 8) return undefined; // 12-bit
      const height = (b[p + 1] << 8) | b[p + 2];
      const width = (b[p + 3] << 8) | b[p + 4];
      const nf = b[p + 5];
      if (height === 0) return undefined; // DNL
      if (width === 0 || nf < 1 || nf > 4 || s.end - p < 6 + 3 * nf) throw malformed("JPEG: bad frame header");
      const components: JpegComponent[] = [];
      for (let i = 0; i < nf; i++) {
        const at = p + 6 + 3 * i;
        const h = b[at + 1] >> 4;
        const v = b[at + 1] & 15;
        if (h < 1 || h > 4 || v < 1 || v > 4 || b[at + 2] > 3) throw malformed("JPEG: bad sampling factors");
        components.push({ id: b[at], h, v, tq: b[at + 2], td: 0, ta: 0, bw: 0, bh: 0, coef: new Int16Array(0) });
      }
      frame = { sof: m, width, height, components };
    } else if (m >= 0xc2 && m <= 0xcf && m !== 0xc4 && m !== 0xc8) {
      return undefined; // progressive, lossless, hierarchical or arithmetic (0xCC: DAC)
    } else if (m >= 0xe0 && m <= 0xef) {
      if (keepJpegSegment(b, s)) extra.push(b.slice(s.start, s.end));
    } else if (m === SOS) {
      if (!frame) throw malformed("JPEG scan before the frame header");
      const ns = b[p];
      if (ns !== frame.components.length) return undefined; // non-interleaved multi-scan
      for (let i = 0; i < ns; i++) {
        const c = frame.components.find((x) => x.id === b[p + 1 + 2 * i]);
        if (!c || frame.components.indexOf(c) !== i) throw malformed("JPEG: scan components do not match the frame");
        c.td = b[p + 2 + 2 * i] >> 4;
        c.ta = b[p + 2 + 2 * i] & 15;
        if (!dc[c.td] || !ac[c.ta]) throw malformed("JPEG: scan uses an undefined Huffman table");
        if (!quant[c.tq]) throw malformed("JPEG: frame uses an undefined quantization table");
      }
      const q = p + 1 + 2 * ns;
      if (b[q] !== 0 || b[q + 1] !== 63 || b[q + 2] !== 0) return undefined;
      decodeScan(b, s.end, s.dataEnd, frame.width, frame.height, frame.components, dc, ac, restartInterval);
      scanned = true;
    }
  }
  if (!frame || !scanned) throw malformed("JPEG has no image data");
  return { ...frame, quant, restartInterval, extra };
}

/** Sets each component's block grid; shared by the decoder and the transform. */
function layout(width: number, height: number, comps: JpegComponent[]): { mcusX: number; mcusY: number } {
  const hmax = Math.max(...comps.map((c) => c.h));
  const vmax = Math.max(...comps.map((c) => c.v));
  if (comps.length === 1) {
    // A single-component scan is not interleaved: its MCU is one block,
    // and the grid covers just the component, whatever the factors say.
    const c = comps[0];
    c.bw = Math.ceil(Math.ceil((width * c.h) / hmax) / 8);
    c.bh = Math.ceil(Math.ceil((height * c.v) / vmax) / 8);
    return { mcusX: c.bw, mcusY: c.bh };
  }
  const mcusX = Math.ceil(width / (8 * hmax));
  const mcusY = Math.ceil(height / (8 * vmax));
  for (const c of comps) {
    c.bw = mcusX * c.h;
    c.bh = mcusY * c.v;
  }
  if (comps.reduce((n, c) => n + c.h * c.v, 0) > 10) throw malformed("JPEG: more than 10 blocks per MCU");
  return { mcusX, mcusY };
}

function decodeScan(
  b: Uint8Array,
  start: number,
  end: number,
  width: number,
  height: number,
  comps: JpegComponent[],
  dc: (DecodeTable | undefined)[],
  ac: (DecodeTable | undefined)[],
  restartInterval: number,
): void {
  const { mcusX, mcusY } = layout(width, height, comps);
  // The header's size is only a claim: bound what it would allocate before
  // reading a single coefficient (a rotation needs a second grid as big).
  const bytes = comps.reduce((n, c) => n + c.bw * c.bh * 64 * 2, 0);
  if (bytes > MAX_COEFFICIENT_BYTES) throw new ImageError("too_large", `${width}x${height} needs ${bytes} coefficient bytes`, "图片尺寸太大");
  for (const c of comps) c.coef = new Int16Array(c.bw * c.bh * 64);
  const single = comps.length === 1;
  const r = new BitReader(b, start, end);
  const pred = new Int32Array(comps.length);
  const total = mcusX * mcusY;
  let rst = 0;
  for (let mcu = 0; mcu < total; mcu++) {
    if (restartInterval && mcu > 0 && mcu % restartInterval === 0) {
      r.restart(rst);
      rst = (rst + 1) & 7;
      pred.fill(0);
    }
    const mx = mcu % mcusX;
    const my = (mcu - mx) / mcusX;
    for (let ci = 0; ci < comps.length; ci++) {
      const c = comps[ci];
      const dcT = dc[c.td]!;
      const acT = ac[c.ta]!;
      const bh = single ? 1 : c.v;
      const bwn = single ? 1 : c.h;
      for (let by = 0; by < bh; by++) {
        for (let bx = 0; bx < bwn; bx++) {
          const off = ((my * bh + by) * c.bw + mx * bwn + bx) * 64;
          const s = r.decode(dcT);
          if (s > 11) throw malformed("JPEG: bad DC category");
          pred[ci] += s ? extend(r.bits(s), s) : 0;
          c.coef[off] = pred[ci];
          for (let k = 1; k < 64; k++) {
            const rs = r.decode(acT);
            const run = rs >> 4;
            const size = rs & 15;
            if (size === 0) {
              if (run !== 15) break; // EOB
              k += 15; // ZRL
              continue;
            }
            k += run;
            if (k > 63 || size > 10) throw malformed("JPEG: bad AC coefficient");
            c.coef[off + ZIGZAG[k]] = extend(r.bits(size), size);
          }
        }
      }
    }
  }
}

// ------------------------------------------------------------ transform

/**
 * The transform each EXIF orientation needs to display upright, as a
 * transpose followed by flips in output space.
 */
const TRANSFORMS: Record<number, { t: boolean; fx: boolean; fy: boolean }> = {
  1: { t: false, fx: false, fy: false },
  2: { t: false, fx: true, fy: false }, // flip horizontal
  3: { t: false, fx: true, fy: true }, // rotate 180
  4: { t: false, fx: false, fy: true }, // flip vertical
  5: { t: true, fx: false, fy: false }, // transpose
  6: { t: true, fx: true, fy: false }, // rotate 90 clockwise
  7: { t: true, fx: true, fy: true }, // transverse
  8: { t: true, fx: false, fy: true }, // rotate 270 clockwise
};

/**
 * Applies an EXIF orientation to decoded coefficients. Returns undefined
 * when a flipped axis is not a whole number of MCUs: the partial MCU at
 * the edge would have to move to the other side, which no block move can
 * do without re-encoding.
 */
export function transformCoefficients(j: JpegCoefficients, orientation: number): JpegCoefficients | undefined {
  const tf = TRANSFORMS[orientation];
  if (!tf) return undefined;
  const { t, fx, fy } = tf;
  const width = t ? j.height : j.width;
  const height = t ? j.width : j.height;
  const comps: JpegComponent[] = j.components.map((c) => ({
    ...c,
    h: t ? c.v : c.h,
    v: t ? c.h : c.v,
    bw: 0,
    bh: 0,
    coef: new Int16Array(0),
  }));
  const single = comps.length === 1;
  const mcuW = single ? 8 : 8 * Math.max(...comps.map((c) => c.h));
  const mcuH = single ? 8 : 8 * Math.max(...comps.map((c) => c.v));
  if ((fx && width % mcuW) || (fy && height % mcuH)) return undefined;
  layout(width, height, comps);

  // Sign of each output coefficient: mirroring a block negates its odd
  // horizontal (fx) or vertical (fy) frequencies.
  const sign = new Int8Array(64);
  for (let v = 0; v < 8; v++) for (let u = 0; u < 8; u++) sign[v * 8 + u] = (fx && u & 1 ? -1 : 1) * (fy && v & 1 ? -1 : 1);

  comps.forEach((c, ci) => {
    const src = j.components[ci];
    // The transposed grid is exactly the source grid transposed, so every
    // output block has a source block.
    c.coef = new Int16Array(c.bw * c.bh * 64);
    for (let by = 0; by < c.bh; by++) {
      for (let bx = 0; bx < c.bw; bx++) {
        const tx = fx ? c.bw - 1 - bx : bx;
        const ty = fy ? c.bh - 1 - by : by;
        const sx = t ? ty : tx;
        const sy = t ? tx : ty;
        const from = (sy * src.bw + sx) * 64;
        const to = (by * c.bw + bx) * 64;
        for (let v = 0; v < 8; v++) {
          for (let u = 0; u < 8; u++) {
            const s = t ? u * 8 + v : v * 8 + u;
            c.coef[to + v * 8 + u] = sign[v * 8 + u] * src.coef[from + s];
          }
        }
      }
    }
  });

  // A transposed coefficient was quantized with the transposed step.
  const quant = j.quant.map((q) => {
    if (!q || !t) return q;
    const values = new Uint16Array(64);
    for (let v = 0; v < 8; v++) for (let u = 0; u < 8; u++) values[v * 8 + u] = q.values[u * 8 + v];
    return { precision: q.precision, values };
  });

  const extra = j.extra.map((seg) => (t ? swapJfifDensity(seg) : seg));
  return { ...j, width, height, components: comps, quant, extra };
}

/** JFIF stores the pixel aspect as X and Y density; a transpose swaps them. */
function swapJfifDensity(seg: Uint8Array): Uint8Array {
  if (seg[1] !== 0xe0 || seg.length < 16) return seg;
  const out = seg.slice();
  out.set(seg.subarray(12, 14), 14);
  out.set(seg.subarray(14, 16), 12);
  return out;
}

// ------------------------------------------------------------ encoding

class ByteWriter {
  buf = new Uint8Array(1 << 16);
  n = 0;

  private grow(extra: number): void {
    if (this.n + extra <= this.buf.length) return;
    let size = this.buf.length * 2;
    while (size < this.n + extra) size *= 2;
    const next = new Uint8Array(size);
    next.set(this.buf.subarray(0, this.n));
    this.buf = next;
  }

  byte(v: number): void {
    this.grow(1);
    this.buf[this.n++] = v;
  }

  bytes(v: ArrayLike<number>): void {
    this.grow(v.length);
    this.buf.set(v, this.n);
    this.n += v.length;
  }

  u16(v: number): void {
    this.byte(v >> 8);
    this.byte(v & 0xff);
  }

  segment(marker: number, body: number[]): void {
    this.byte(0xff);
    this.byte(marker);
    this.u16(body.length + 2);
    this.bytes(body);
  }

  result(): Uint8Array {
    return this.buf.slice(0, this.n);
  }
}

class BitWriter {
  private acc = 0;
  private n = 0;
  private readonly w: ByteWriter;

  constructor(w: ByteWriter) {
    this.w = w;
  }

  put(code: number, size: number): void {
    this.acc = (this.acc << size) | (code & ((1 << size) - 1));
    this.n += size;
    while (this.n >= 8) {
      this.n -= 8;
      const byte = (this.acc >>> this.n) & 0xff;
      this.w.byte(byte);
      if (byte === 0xff) this.w.byte(0); // stuffing
    }
    this.acc &= (1 << this.n) - 1;
  }

  /** Pads the last byte with 1-bits, as the standard asks before a marker. */
  flush(): void {
    if (this.n > 0) this.put((1 << (8 - this.n)) - 1, 8 - this.n);
  }
}

function category(v: number): number {
  return v === 0 ? 0 : 32 - Math.clz32(v < 0 ? -v : v);
}

/**
 * Visits every Huffman symbol of the scan in coding order. The encoder
 * runs it twice: once to count symbol frequencies, once to emit.
 */
function walkSymbols(
  j: JpegCoefficients,
  onBlockSymbol: (ci: number, isDc: boolean, symbol: number, extra: number, extraBits: number) => void,
  onRestart: (index: number) => void,
): void {
  const comps = j.components;
  const single = comps.length === 1;
  const mcusX = single ? comps[0].bw : comps[0].bw / comps[0].h;
  const mcusY = single ? comps[0].bh : comps[0].bh / comps[0].v;
  const pred = new Int32Array(comps.length);
  let rst = 0;
  for (let mcu = 0; mcu < mcusX * mcusY; mcu++) {
    if (j.restartInterval && mcu > 0 && mcu % j.restartInterval === 0) {
      onRestart(rst);
      rst = (rst + 1) & 7;
      pred.fill(0);
    }
    const mx = mcu % mcusX;
    const my = (mcu - mx) / mcusX;
    for (let ci = 0; ci < comps.length; ci++) {
      const c = comps[ci];
      const bh = single ? 1 : c.v;
      const bwn = single ? 1 : c.h;
      for (let by = 0; by < bh; by++) {
        for (let bx = 0; bx < bwn; bx++) {
          const off = ((my * bh + by) * c.bw + mx * bwn + bx) * 64;
          const diff = c.coef[off] - pred[ci];
          pred[ci] = c.coef[off];
          const s = category(diff);
          onBlockSymbol(ci, true, s, diff < 0 ? diff - 1 : diff, s);
          let run = 0;
          for (let k = 1; k < 64; k++) {
            const v = c.coef[off + ZIGZAG[k]];
            if (v === 0) {
              run++;
              continue;
            }
            for (; run > 15; run -= 16) onBlockSymbol(ci, false, 0xf0, 0, 0);
            const size = category(v);
            onBlockSymbol(ci, false, (run << 4) | size, v < 0 ? v - 1 : v, size);
            run = 0;
          }
          if (run > 0) onBlockSymbol(ci, false, 0x00, 0, 0);
        }
      }
    }
  }
}

/**
 * Optimal length-limited Huffman code for the given symbol frequencies,
 * as in libjpeg's jpeg_gen_optimal_table (JPEG Annex K.2): a reserved
 * pseudo-symbol keeps any code from being all ones, and lengths over 16
 * are folded back into the tree.
 */
function optimalTable(counts: Float64Array): { bits: number[]; values: number[] } {
  const freq = new Float64Array(257);
  freq.set(counts);
  freq[256] = 1;
  const codesize = new Int32Array(257);
  const others = new Int32Array(257).fill(-1);
  for (;;) {
    let c1 = -1;
    let c2 = -1;
    let v = Infinity;
    for (let i = 0; i <= 256; i++) if (freq[i] && freq[i] <= v) (v = freq[i]), (c1 = i);
    v = Infinity;
    for (let i = 0; i <= 256; i++) if (freq[i] && freq[i] <= v && i !== c1) (v = freq[i]), (c2 = i);
    if (c2 < 0) break;
    freq[c1] += freq[c2];
    freq[c2] = 0;
    codesize[c1]++;
    while (others[c1] >= 0) codesize[(c1 = others[c1])]++;
    others[c1] = c2;
    codesize[c2]++;
    while (others[c2] >= 0) codesize[(c2 = others[c2])]++;
  }
  const bits = new Array<number>(33).fill(0);
  for (let i = 0; i <= 256; i++) if (codesize[i]) bits[codesize[i]]++;
  for (let i = 32; i > 16; i--) {
    while (bits[i] > 0) {
      let j = i - 2;
      while (bits[j] === 0) j--;
      bits[i] -= 2;
      bits[i - 1]++;
      bits[j + 1] += 2;
      bits[j]--;
    }
  }
  let i = 16;
  while (bits[i] === 0) i--;
  bits[i]--; // drop the pseudo-symbol, which has the longest code
  const values: number[] = [];
  for (let len = 1; len <= 32; len++) for (let s = 0; s < 256; s++) if (codesize[s] === len) values.push(s);
  return { bits: bits.slice(1, 17), values };
}

function encodeTable(bits: number[], values: number[]): { code: Int32Array; size: Int8Array } {
  const code = new Int32Array(256);
  const size = new Int8Array(256);
  let c = 0;
  let k = 0;
  for (let l = 1; l <= 16; l++) {
    for (let i = 0; i < bits[l - 1]; i++) {
      code[values[k]] = c++;
      size[values[k]] = l;
      k++;
    }
    c <<= 1;
  }
  return { code, size };
}

/** Writes coefficients as a sequential Huffman JPEG with optimized tables. */
export function encodeJpegCoefficients(j: JpegCoefficients): Uint8Array {
  const comps = j.components;
  // Frequencies per table slot, so components sharing a table keep sharing it.
  const dcFreq = new Map<number, Float64Array>();
  const acFreq = new Map<number, Float64Array>();
  for (const c of comps) {
    if (!dcFreq.has(c.td)) dcFreq.set(c.td, new Float64Array(256));
    if (!acFreq.has(c.ta)) acFreq.set(c.ta, new Float64Array(256));
  }
  walkSymbols(
    j,
    (ci, isDc, sym) => (isDc ? dcFreq.get(comps[ci].td)! : acFreq.get(comps[ci].ta)!)[sym]++,
    () => {},
  );

  const w = new ByteWriter();
  w.bytes([0xff, 0xd8]);
  const jfif = j.extra.filter((s) => s[1] === 0xe0);
  for (const s of [...jfif, ...j.extra.filter((s) => s[1] !== 0xe0)]) w.bytes(s);

  for (const slot of [...new Set(comps.map((c) => c.tq))].sort()) {
    const q = j.quant[slot]!;
    const body = [(q.precision << 4) | slot];
    for (let k = 0; k < 64; k++) {
      const v = q.values[ZIGZAG[k]];
      if (q.precision) body.push(v >> 8, v & 0xff);
      else body.push(v);
    }
    w.segment(0xdb, body);
  }

  const sof = [8, j.height >> 8, j.height & 0xff, j.width >> 8, j.width & 0xff, comps.length];
  for (const c of comps) sof.push(c.id, (c.h << 4) | c.v, c.tq);
  w.segment(j.sof, sof);

  const dcEnc = new Map<number, ReturnType<typeof encodeTable>>();
  const acEnc = new Map<number, ReturnType<typeof encodeTable>>();
  const dht: number[] = [];
  for (const [cls, freqs, enc] of [
    [0, dcFreq, dcEnc],
    [1, acFreq, acEnc],
  ] as const) {
    for (const [slot, f] of freqs) {
      const { bits, values } = optimalTable(f);
      dht.push((cls << 4) | slot, ...bits, ...values);
      enc.set(slot, encodeTable(bits, values));
    }
  }
  w.segment(0xc4, dht);
  if (j.restartInterval) w.segment(0xdd, [j.restartInterval >> 8, j.restartInterval & 0xff]);

  const sos = [comps.length];
  for (const c of comps) sos.push(c.id, (c.td << 4) | c.ta);
  sos.push(0, 63, 0);
  w.segment(SOS, sos);

  const bw = new BitWriter(w);
  walkSymbols(
    j,
    (ci, isDc, sym, extra, extraBits) => {
      const t = isDc ? dcEnc.get(comps[ci].td)! : acEnc.get(comps[ci].ta)!;
      bw.put(t.code[sym], t.size[sym]);
      if (extraBits) bw.put(extra, extraBits);
    },
    (index) => {
      bw.flush();
      w.bytes([0xff, 0xd0 + index]);
    },
  );
  bw.flush();
  w.bytes([0xff, EOI]);
  return w.result();
}

/**
 * Losslessly applies an EXIF orientation (1..8) to a JPEG, like
 * `jpegtran -perfect -copy none`. The result has no metadata at all (only
 * the JFIF, ICC profile and Adobe segments are kept), so it carries no
 * orientation. Returns undefined when the transform cannot be perfect or
 * the coding is not supported; throws ImageError on malformed data.
 */
/** Largest coefficient grid decoded: about 64 megapixels of 4:2:0. */
export const MAX_COEFFICIENT_BYTES = 192 * 1024 * 1024;

export function rotateJpegLossless(b: Uint8Array, orientation: number): Uint8Array | undefined {
  if (!TRANSFORMS[orientation]) return undefined;
  const j = decodeJpegCoefficients(b);
  if (!j) return undefined;
  const out = transformCoefficients(j, orientation);
  return out && encodeJpegCoefficients(out);
}
