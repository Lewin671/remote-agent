/** Streaming FIPS 180-4 SHA-256 hasher so large Blobs/OPFS Files can be verified in fixed-size slices
 * without loading the entire file into RAM (WebCrypto `subtle.digest` is one-shot only). */

const K = new Uint32Array([
  0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
  0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
  0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
  0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
  0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
  0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
  0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
  0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
]);

export class PartialTruncatedError extends Error {
  constructor() {
    super("partial file truncated during hashing");
    this.name = "PartialTruncatedError";
  }
}

export class StreamingSha256 {
  private readonly h = new Uint32Array([
    0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a,
    0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19,
  ]);
  private readonly w = new Uint32Array(64);
  private readonly buf = new Uint8Array(64);
  private bufLen = 0;
  private totalBytes = 0;

  update(chunk: Uint8Array): void {
    let off = 0;
    const len = chunk.length;
    this.totalBytes += len;
    if (this.bufLen > 0) {
      const need = 64 - this.bufLen;
      if (len < need) {
        this.buf.set(chunk, this.bufLen);
        this.bufLen += len;
        return;
      }
      this.buf.set(chunk.subarray(0, need), this.bufLen);
      this.compress(this.buf, 0);
      this.bufLen = 0;
      off = need;
    }
    while (off + 64 <= len) {
      this.compress(chunk, off);
      off += 64;
    }
    if (off < len) {
      this.buf.set(chunk.subarray(off), 0);
      this.bufLen = len - off;
    }
  }

  digest(): Uint8Array {
    const bitLen = BigInt(this.totalBytes) * 8n;
    const padLen = this.bufLen < 56 ? 56 - this.bufLen : 120 - this.bufLen;
    const tail = new Uint8Array(padLen + 8);
    tail[0] = 0x80;
    const view = new DataView(tail.buffer);
    view.setUint32(padLen, Number((bitLen >> 32n) & 0xffffffffn), false);
    view.setUint32(padLen + 4, Number(bitLen & 0xffffffffn), false);
    this.update(tail);

    const out = new Uint8Array(32);
    const outView = new DataView(out.buffer);
    for (let i = 0; i < 8; i++) outView.setUint32(i * 4, this.h[i], false);
    return out;
  }

  private compress(block: Uint8Array, offset: number): void {
    const w = this.w;
    for (let i = 0; i < 16; i++) {
      const j = offset + i * 4;
      w[i] = ((block[j] << 24) | (block[j + 1] << 16) | (block[j + 2] << 8) | block[j + 3]) >>> 0;
    }
    for (let i = 16; i < 64; i++) {
      const w15 = w[i - 15];
      const s0 = ((w15 >>> 7) | (w15 << 25)) ^ ((w15 >>> 18) | (w15 << 14)) ^ (w15 >>> 3);
      const w2 = w[i - 2];
      const s1 = ((w2 >>> 17) | (w2 << 15)) ^ ((w2 >>> 19) | (w2 << 13)) ^ (w2 >>> 10);
      w[i] = (w[i - 16] + s0 + w[i - 7] + s1) >>> 0;
    }

    let a = this.h[0], b = this.h[1], c = this.h[2], d = this.h[3];
    let e = this.h[4], f = this.h[5], g = this.h[6], h = this.h[7];
    for (let i = 0; i < 64; i++) {
      const S1 = ((e >>> 6) | (e << 26)) ^ ((e >>> 11) | (e << 21)) ^ ((e >>> 25) | (e << 7));
      const ch = (e & f) ^ (~e & g);
      const temp1 = (h + S1 + ch + K[i] + w[i]) >>> 0;
      const S0 = ((a >>> 2) | (a << 30)) ^ ((a >>> 13) | (a << 19)) ^ ((a >>> 22) | (a << 10));
      const maj = (a & b) ^ (a & c) ^ (b & c);
      const temp2 = (S0 + maj) >>> 0;
      h = g; g = f; f = e; e = (d + temp1) >>> 0;
      d = c; c = b; b = a; a = (temp1 + temp2) >>> 0;
    }
    this.h[0] = (this.h[0] + a) >>> 0;
    this.h[1] = (this.h[1] + b) >>> 0;
    this.h[2] = (this.h[2] + c) >>> 0;
    this.h[3] = (this.h[3] + d) >>> 0;
    this.h[4] = (this.h[4] + e) >>> 0;
    this.h[5] = (this.h[5] + f) >>> 0;
    this.h[6] = (this.h[6] + g) >>> 0;
    this.h[7] = (this.h[7] + h) >>> 0;
  }
}

export const HASH_SLICE_BYTES = 1024 * 1024;

/** Computes SHA-256 of the first `exactBytes` of `blob` by reading fixed-size slices sequentially. */
export async function hashBlobStreaming(blob: Blob, exactBytes: number, sliceBytes = HASH_SLICE_BYTES): Promise<Uint8Array> {
  const hasher = new StreamingSha256();
  if (exactBytes === 0) return hasher.digest();
  let offset = 0;
  while (offset < exactBytes) {
    const end = Math.min(exactBytes, offset + sliceBytes);
    const buf = await blob.slice(offset, end).arrayBuffer();
    if (buf.byteLength !== end - offset) throw new PartialTruncatedError();
    hasher.update(new Uint8Array(buf));
    offset = end;
  }
  return hasher.digest();
}
