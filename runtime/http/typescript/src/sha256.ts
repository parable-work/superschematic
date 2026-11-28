/*
SHA-256 (FIPS 180-4), synchronous, for the RFC 7638 thumbprint of a key the
callers field holds as a PEM (D60): loadCallers is synchronous, WebCrypto's
digest is not, and nothing here imports from node:, so a Workers bundle of
the runtime stays clean. Sha256 hashes a stream a chunk at a time, as an
attachment's upload is checked against the hash it declared (D154), which
WebCrypto's digest, taking its input whole, cannot.
*/

const K = Uint32Array.of(
  0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
  0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
  0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
  0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
  0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
  0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
  0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
  0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
);

function rotr(x: number, n: number): number {
  return (x >>> n) | (x << (32 - n));
}

/** A SHA-256 over input given a chunk at a time: update each chunk, then digest once. */
export class Sha256 {
  private readonly h = Uint32Array.of(0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19);
  private readonly w = new Uint32Array(64);
  private readonly block = new Uint8Array(64);
  private filled = 0;
  private length = 0;
  private done = false;

  /** Adds data to the input. */
  update(data: Uint8Array): this {
    if (this.done) throw new Error('sha256: update after digest');
    this.length += data.length;
    let offset = 0;
    if (this.filled > 0) {
      const take = Math.min(64 - this.filled, data.length);
      this.block.set(data.subarray(0, take), this.filled);
      this.filled += take;
      offset = take;
      if (this.filled < 64) return this;
      this.compress(this.block, 0);
      this.filled = 0;
    }
    for (; offset + 64 <= data.length; offset += 64) this.compress(data, offset);
    if (offset < data.length) {
      this.block.set(data.subarray(offset), 0);
      this.filled = data.length - offset;
    }
    return this;
  }

  /** The digest of the input: the message, a 1 bit, zeros, and its length in bits as 64 bits, to a multiple of 64 bytes. */
  digest(): Uint8Array {
    if (this.done) throw new Error('sha256: digest twice');
    const bits = this.length * 8;
    const tail = new Uint8Array(this.filled + 9 > 64 ? 128 : 64);
    tail.set(this.block.subarray(0, this.filled));
    tail[this.filled] = 0x80;
    const view = new DataView(tail.buffer);
    view.setUint32(tail.length - 8, Math.floor(bits / 2 ** 32));
    view.setUint32(tail.length - 4, bits >>> 0);
    for (let offset = 0; offset < tail.length; offset += 64) this.compress(tail, offset);
    this.done = true;
    const out = new Uint8Array(32);
    const outView = new DataView(out.buffer);
    for (let i = 0; i < 8; i++) outView.setUint32(i * 4, this.h[i]!);
    return out;
  }

  private compress(data: Uint8Array, offset: number): void {
    const w = this.w;
    const h = this.h;
    for (let i = 0; i < 16; i++) {
      const at = offset + i * 4;
      w[i] = ((data[at]! << 24) | (data[at + 1]! << 16) | (data[at + 2]! << 8) | data[at + 3]!) >>> 0;
    }
    for (let i = 16; i < 64; i++) {
      const w15 = w[i - 15]!;
      const w2 = w[i - 2]!;
      const s0 = rotr(w15, 7) ^ rotr(w15, 18) ^ (w15 >>> 3);
      const s1 = rotr(w2, 17) ^ rotr(w2, 19) ^ (w2 >>> 10);
      w[i] = (w[i - 16]! + s0 + w[i - 7]! + s1) >>> 0;
    }
    let a = h[0]!;
    let b = h[1]!;
    let c = h[2]!;
    let d = h[3]!;
    let e = h[4]!;
    let f = h[5]!;
    let g = h[6]!;
    let hh = h[7]!;
    for (let i = 0; i < 64; i++) {
      const t1 = (hh + (rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25)) + ((e & f) ^ (~e & g)) + K[i]! + w[i]!) >>> 0;
      const t2 = ((rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22)) + ((a & b) ^ (a & c) ^ (b & c))) >>> 0;
      hh = g;
      g = f;
      f = e;
      e = (d + t1) >>> 0;
      d = c;
      c = b;
      b = a;
      a = (t1 + t2) >>> 0;
    }
    h[0] = h[0]! + a;
    h[1] = h[1]! + b;
    h[2] = h[2]! + c;
    h[3] = h[3]! + d;
    h[4] = h[4]! + e;
    h[5] = h[5]! + f;
    h[6] = h[6]! + g;
    h[7] = h[7]! + hh;
  }
}

/** The SHA-256 digest of data. */
export function sha256(data: Uint8Array): Uint8Array {
  return new Sha256().update(data).digest();
}

/** bytes in lowercase hex, two digits a byte. */
export function hex(bytes: Uint8Array | ArrayBuffer): string {
  return Array.from(bytes instanceof Uint8Array ? bytes : new Uint8Array(bytes), byte => byte.toString(16).padStart(2, '0')).join('');
}

function bytesOf(data: Uint8Array | string): Uint8Array {
  return typeof data === 'string' ? new TextEncoder().encode(data) : data;
}

/** The SHA-256 of data, a string as its UTF-8, in lowercase hex, through Sha256: synchronous, for short input. */
export function sha256Hex(data: Uint8Array | string): string {
  return hex(sha256(bytesOf(data)));
}

/**
 * The SHA-256 of data, a string as its UTF-8, in lowercase hex, through
 * WebCrypto's digest, which runs natively: for input as large as a body or
 * an object, which Sha256 would spend CPU time on.
 */
export async function subtleSha256Hex(data: Uint8Array | string): Promise<string> {
  return hex(await crypto.subtle.digest('SHA-256', bytesOf(data) as Uint8Array<ArrayBuffer>));
}

/** A Worker's native streaming digest (workerd's crypto.DigestStream), where the runtime has one. */
type DigestStreamClass = new (algorithm: string) => WritableStream<Uint8Array> & { readonly digest: Promise<ArrayBuffer> };

/**
 * The SHA-256 of a stream, in lowercase hex, and its length, read a chunk
 * at a time (D154): through workerd's native DigestStream on Workers, whose
 * CPU time a large file would otherwise spend, else through Sha256. It
 * stops reading past limit bytes, when it gives the length it reached and
 * no hash.
 */
export async function sha256Stream(body: ReadableStream<Uint8Array>, limit = Number.MAX_SAFE_INTEGER): Promise<{ hash: string; size: number }> {
  const DigestStream = (globalThis.crypto as unknown as { DigestStream?: DigestStreamClass } | undefined)?.DigestStream;
  const reader = body.getReader();
  const native = DigestStream ? new DigestStream('SHA-256') : undefined;
  const writer = native?.getWriter();
  const portable = native ? undefined : new Sha256();
  let size = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    size += value.byteLength;
    if (size > limit) {
      await reader.cancel().catch(() => undefined);
      await writer?.abort().catch(() => undefined);
      return { hash: '', size };
    }
    if (writer) await writer.write(value);
    else portable!.update(value);
  }
  if (writer) {
    await writer.close();
    return { hash: hex(new Uint8Array(await native!.digest)), size };
  }
  return { hash: hex(portable!.digest()), size };
}
