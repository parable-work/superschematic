/*
argon2id (RFC 9106, version 0x13) in plain TypeScript, for a runtime whose
node:crypto has no argon2: workerd, under which a Worker serves an API over
the user model through Hyperdrive (D92). It writes the same keys as
node:crypto's argon2 for the same inputs, so a hash a server on Bun wrote
verifies on Workers and back; argon2.test.ts holds the two to each other and
to RFC 9106's vector. password.ts takes node:crypto's where it has one, which
runs off the main thread and faster, and this one elsewhere.

It runs on the calling thread, synchronously, in memoryKiB KiB of one
Uint32Array: a 64-bit word is two 32-bit halves, low first, and BLAKE2b and
the compression's BlaMka multiply work on the halves. A hash keeps its
memory for the next, up to REUSED_MEMORY_KIB, zeroed after each, so an
isolate that signs members in holds one block of memory for it rather than
a new one per sign-in for its collector to find. The lanes are filled
one after another, slice by slice, which is the order the reference
implementation's threads synchronize on, so the result is the same.
*/

/** What argon2id takes. A secret and associated data are RFC 9106's K and X; the identity runtime passes neither. */
export interface Argon2idInput {
  readonly password: Uint8Array;
  readonly salt: Uint8Array;
  readonly memoryKiB: number;
  readonly iterations: number;
  readonly parallelism: number;
  readonly tagLength: number;
  readonly secret?: Uint8Array;
  readonly associatedData?: Uint8Array;
}

const ARGON2_VERSION = 0x13;
const ARGON2ID = 2;
const SYNC_POINTS = 4;
/** One block: 1024 bytes, 128 words of 64 bits, 256 halves. */
const BLOCK_WORDS = 256;
const ADDRESSES_IN_BLOCK = 128;
const TWO_32 = 0x1_0000_0000;

/** The most memory a hash keeps for the next: 64 MiB, above the identity runtime's default cost of 19 MiB. */
export const REUSED_MEMORY_KIB = 64 * 1024;

let reused: Uint32Array | undefined;

/** argon2id's tag of input, synchronously. */
export function argon2id(input: Argon2idInput): Uint8Array {
  const { password, salt, iterations, parallelism: lanes, tagLength } = input;
  const secret = input.secret ?? new Uint8Array(0);
  const associatedData = input.associatedData ?? new Uint8Array(0);
  if (!Number.isInteger(lanes) || lanes < 1 || lanes > 0xff_ffff) throw new RangeError(`argon2id: parallelism ${lanes} is out of range`);
  if (!Number.isInteger(iterations) || iterations < 1) throw new RangeError(`argon2id: iterations ${iterations} is out of range`);
  if (!Number.isInteger(input.memoryKiB) || input.memoryKiB < 8 * lanes) throw new RangeError(`argon2id: memoryKiB ${input.memoryKiB} is under 8 times parallelism`);
  if (!Number.isInteger(tagLength) || tagLength < 4) throw new RangeError(`argon2id: tagLength ${tagLength} is under 4`);
  if (salt.length < 8) throw new RangeError('argon2id: the salt is under 8 bytes');

  // H0, over the cost as given (RFC 9106, section 3.2).
  const h0 = blake2b(
    concat([
      le32(lanes),
      le32(tagLength),
      le32(input.memoryKiB),
      le32(iterations),
      le32(ARGON2_VERSION),
      le32(ARGON2ID),
      le32(password.length),
      password,
      le32(salt.length),
      salt,
      le32(secret.length),
      secret,
      le32(associatedData.length),
      associatedData,
    ]),
    64
  );

  // The memory, rounded down to a multiple of four segments per lane.
  const segmentLength = Math.floor(input.memoryKiB / (SYNC_POINTS * lanes));
  const laneLength = segmentLength * SYNC_POINTS;
  const blocks = laneLength * lanes;
  // Every block is written before it is read, so kept memory needs no
  // clearing first; it is cleared after.
  let memory: Uint32Array;
  if (blocks <= REUSED_MEMORY_KIB) {
    if (!reused || reused.length < blocks * BLOCK_WORDS) reused = new Uint32Array(blocks * BLOCK_WORDS);
    memory = reused.subarray(0, blocks * BLOCK_WORDS);
  } else {
    memory = new Uint32Array(blocks * BLOCK_WORDS);
  }
  try {
    return fill(h0, memory, lanes, laneLength, segmentLength, blocks, iterations, tagLength);
  } finally {
    memory.fill(0);
  }
}

/** Fills the memory from H0, pass by pass, and returns the tag. */
function fill(
  h0: Uint8Array,
  memory: Uint32Array,
  lanes: number,
  laneLength: number,
  segmentLength: number,
  blocks: number,
  iterations: number,
  tagLength: number
): Uint8Array {
  for (let lane = 0; lane < lanes; lane++) {
    for (let column = 0; column < 2; column++) {
      const block = hashPrime(1024, concat([h0, le32(column), le32(lane)]));
      bytesToWords(block, memory, (lane * laneLength + column) * BLOCK_WORDS);
    }
  }

  const state: FillState = { memory, lanes, laneLength, segmentLength, blocks, passes: iterations };
  for (let pass = 0; pass < iterations; pass++) {
    for (let slice = 0; slice < SYNC_POINTS; slice++) {
      for (let lane = 0; lane < lanes; lane++) fillSegment(state, pass, lane, slice);
    }
  }

  // The last block of every lane, XORed, and H' of it.
  const last = memory.slice((laneLength - 1) * BLOCK_WORDS, laneLength * BLOCK_WORDS);
  for (let lane = 1; lane < lanes; lane++) {
    const offset = (lane * laneLength + laneLength - 1) * BLOCK_WORDS;
    for (let i = 0; i < BLOCK_WORDS; i++) last[i] = last[i]! ^ memory[offset + i]!;
  }
  return hashPrime(tagLength, wordsToBytes(last));
}

interface FillState {
  readonly memory: Uint32Array;
  readonly lanes: number;
  readonly laneLength: number;
  readonly segmentLength: number;
  readonly blocks: number;
  readonly passes: number;
}

// The working blocks of the compression, and of argon2id's address blocks.
const blockR = new Uint32Array(BLOCK_WORDS);
const blockTmp = new Uint32Array(BLOCK_WORDS);
const zeroBlock = new Uint32Array(BLOCK_WORDS);
const inputBlock = new Uint32Array(BLOCK_WORDS);
const addressBlock = new Uint32Array(BLOCK_WORDS);

/** Fills one segment: a lane's blocks of one slice of one pass (RFC 9106, section 3.4). */
function fillSegment(s: FillState, pass: number, lane: number, slice: number): void {
  const { memory, laneLength, segmentLength, lanes } = s;
  // argon2id addresses the first half of the first pass independently of the data.
  const independent = pass === 0 && slice < SYNC_POINTS / 2;
  if (independent) {
    inputBlock.fill(0);
    setWord(inputBlock, 0, pass);
    setWord(inputBlock, 1, lane);
    setWord(inputBlock, 2, slice);
    setWord(inputBlock, 3, s.blocks);
    setWord(inputBlock, 4, s.passes);
    setWord(inputBlock, 5, ARGON2ID);
  }
  let start = 0;
  if (pass === 0 && slice === 0) {
    start = 2;
    if (independent) nextAddresses();
  }
  let current = lane * laneLength + slice * segmentLength + start;
  let previous = current % laneLength === 0 ? current + laneLength - 1 : current - 1;
  for (let index = start; index < segmentLength; index++, current++, previous++) {
    if (current % laneLength === 1) previous = current - 1;
    let randLow: number;
    let randHigh: number;
    if (independent) {
      if (index % ADDRESSES_IN_BLOCK === 0) nextAddresses();
      const at = (index % ADDRESSES_IN_BLOCK) * 2;
      randLow = addressBlock[at]!;
      randHigh = addressBlock[at + 1]!;
    } else {
      randLow = memory[previous * BLOCK_WORDS]!;
      randHigh = memory[previous * BLOCK_WORDS + 1]!;
    }
    const refLane = pass === 0 && slice === 0 ? lane : randHigh % lanes;
    const refIndex = indexAlpha(s, pass, slice, index, randLow, refLane === lane);
    fillBlock(memory, previous * BLOCK_WORDS, memory, (laneLength * refLane + refIndex) * BLOCK_WORDS, memory, current * BLOCK_WORDS, pass !== 0);
  }
}

/** The next 128 addresses of a data-independent segment: G(0, G(0, input)), the input's counter advanced first. */
function nextAddresses(): void {
  const counter = inputBlock[12]! + 1;
  inputBlock[12] = counter;
  inputBlock[13] = inputBlock[13]! + Math.floor(counter / TWO_32);
  fillBlock(zeroBlock, 0, inputBlock, 0, addressBlock, 0, false);
  fillBlock(zeroBlock, 0, addressBlock, 0, addressBlock, 0, false);
}

/** The column of the reference block within its lane (RFC 9106, section 3.4.1.2). */
function indexAlpha(s: FillState, pass: number, slice: number, index: number, randLow: number, sameLane: boolean): number {
  const { laneLength, segmentLength } = s;
  let area: number;
  if (pass === 0) {
    if (slice === 0) area = index - 1;
    else if (sameLane) area = slice * segmentLength + index - 1;
    else area = slice * segmentLength + (index === 0 ? -1 : 0);
  } else if (sameLane) {
    area = laneLength - segmentLength + index - 1;
  } else {
    area = laneLength - segmentLength + (index === 0 ? -1 : 0);
  }
  const x = mulHigh(randLow, randLow);
  const relative = area - 1 - mulHigh(area, x);
  const start = pass !== 0 && slice !== SYNC_POINTS - 1 ? (slice + 1) * segmentLength : 0;
  return (start + relative) % laneLength;
}

/** The high 32 bits of the 64-bit product of two 32-bit integers. */
function mulHigh(a: number, b: number): number {
  const a0 = a & 0xffff;
  const a1 = a >>> 16;
  const b0 = b & 0xffff;
  const b1 = b >>> 16;
  const lowLow = a0 * b0;
  const lowHigh = a0 * b1;
  const highLow = a1 * b0;
  const carry = (lowLow >>> 16) + (lowHigh & 0xffff) + (highLow & 0xffff);
  return (a1 * b1 + (lowHigh >>> 16) + (highLow >>> 16) + (carry >>> 16)) >>> 0;
}

/**
 * The compression G: next = P(prev XOR ref) XOR prev XOR ref, XORed into
 * what next held on a pass after the first (version 0x13).
 */
function fillBlock(
  prevMemory: Uint32Array,
  prev: number,
  refMemory: Uint32Array,
  ref: number,
  nextMemory: Uint32Array,
  next: number,
  withXor: boolean
): void {
  const r = blockR;
  const t = blockTmp;
  for (let i = 0; i < BLOCK_WORDS; i++) {
    const v = refMemory[ref + i]! ^ prevMemory[prev + i]!;
    r[i] = v;
    t[i] = withXor ? v ^ nextMemory[next + i]! : v;
  }
  // Rows: each 16 consecutive words.
  for (let row = 0; row < 8; row++) {
    const b = row * 32;
    permute(r, b, b + 2, b + 4, b + 6, b + 8, b + 10, b + 12, b + 14, b + 16, b + 18, b + 20, b + 22, b + 24, b + 26, b + 28, b + 30);
  }
  // Columns: each pair of words of every row.
  for (let column = 0; column < 8; column++) {
    const b = column * 4;
    permute(r, b, b + 2, b + 32, b + 34, b + 64, b + 66, b + 96, b + 98, b + 128, b + 130, b + 160, b + 162, b + 192, b + 194, b + 224, b + 226);
  }
  for (let i = 0; i < BLOCK_WORDS; i++) nextMemory[next + i] = t[i]! ^ r[i]!;
}

/** BLAKE2b's round on 16 words, with BlaMka's multiplication in each G; each argument is a word's low half's index. */
function permute(
  v: Uint32Array,
  v0: number,
  v1: number,
  v2: number,
  v3: number,
  v4: number,
  v5: number,
  v6: number,
  v7: number,
  v8: number,
  v9: number,
  v10: number,
  v11: number,
  v12: number,
  v13: number,
  v14: number,
  v15: number
): void {
  blamkaG(v, v0, v4, v8, v12);
  blamkaG(v, v1, v5, v9, v13);
  blamkaG(v, v2, v6, v10, v14);
  blamkaG(v, v3, v7, v11, v15);
  blamkaG(v, v0, v5, v10, v15);
  blamkaG(v, v1, v6, v11, v12);
  blamkaG(v, v2, v7, v8, v13);
  blamkaG(v, v3, v4, v9, v14);
}

/**
 * GB: BLAKE2b's G with each addition x + y + 2 * lo(x) * lo(y), modulo 2^64,
 * on the four words' halves held in locals. lo(x) * lo(y) is 64 bits: its
 * low half is Math.imul's, its high half the sum of the 16-bit parts'
 * products, each exact in a double.
 */
function blamkaG(v: Uint32Array, a: number, b: number, c: number, d: number): void {
  let al = v[a]!;
  let ah = v[a + 1]!;
  let bl = v[b]!;
  let bh = v[b + 1]!;
  let cl = v[c]!;
  let ch = v[c + 1]!;
  let dl = v[d]!;
  let dh = v[d + 1]!;
  let pl: number;
  let ph: number;
  let s: number;
  let t: number;

  // a = a + b + 2 * lo(a) * lo(b); d = rotr64(d ^ a, 32)
  pl = Math.imul(al, bl) >>> 0;
  ph = mulHigh(al, bl);
  s = al + bl + ((pl << 1) >>> 0);
  ah = (ah + bh + ph * 2 + (pl >>> 31) + ((s / TWO_32) | 0)) >>> 0;
  al = s >>> 0;
  t = (dl ^ al) >>> 0;
  dl = (dh ^ ah) >>> 0;
  dh = t;

  // c = c + d + 2 * lo(c) * lo(d); b = rotr64(b ^ c, 24)
  pl = Math.imul(cl, dl) >>> 0;
  ph = mulHigh(cl, dl);
  s = cl + dl + ((pl << 1) >>> 0);
  ch = (ch + dh + ph * 2 + (pl >>> 31) + ((s / TWO_32) | 0)) >>> 0;
  cl = s >>> 0;
  t = bl ^ cl;
  s = bh ^ ch;
  bl = ((t >>> 24) | (s << 8)) >>> 0;
  bh = ((s >>> 24) | (t << 8)) >>> 0;

  // a = a + b + 2 * lo(a) * lo(b); d = rotr64(d ^ a, 16)
  pl = Math.imul(al, bl) >>> 0;
  ph = mulHigh(al, bl);
  s = al + bl + ((pl << 1) >>> 0);
  ah = (ah + bh + ph * 2 + (pl >>> 31) + ((s / TWO_32) | 0)) >>> 0;
  al = s >>> 0;
  t = dl ^ al;
  s = dh ^ ah;
  dl = ((t >>> 16) | (s << 16)) >>> 0;
  dh = ((s >>> 16) | (t << 16)) >>> 0;

  // c = c + d + 2 * lo(c) * lo(d); b = rotr64(b ^ c, 63)
  pl = Math.imul(cl, dl) >>> 0;
  ph = mulHigh(cl, dl);
  s = cl + dl + ((pl << 1) >>> 0);
  ch = (ch + dh + ph * 2 + (pl >>> 31) + ((s / TWO_32) | 0)) >>> 0;
  cl = s >>> 0;
  t = bl ^ cl;
  s = bh ^ ch;
  bl = ((t << 1) | (s >>> 31)) >>> 0;
  bh = ((s << 1) | (t >>> 31)) >>> 0;

  v[a] = al;
  v[a + 1] = ah;
  v[b] = bl;
  v[b + 1] = bh;
  v[c] = cl;
  v[c + 1] = ch;
  v[d] = dl;
  v[d + 1] = dh;
}

/** v[x] = rotr64(v[x] XOR v[y], n), for n of 16, 24, 32 or 63. */
function rotate(v: Uint32Array, x: number, y: number, n: number): void {
  const low = v[x]! ^ v[y]!;
  const high = v[x + 1]! ^ v[y + 1]!;
  if (n === 32) {
    v[x] = high;
    v[x + 1] = low;
  } else if (n === 63) {
    v[x] = (low << 1) | (high >>> 31);
    v[x + 1] = (high << 1) | (low >>> 31);
  } else {
    v[x] = (low >>> n) | (high << (32 - n));
    v[x + 1] = (high >>> n) | (low << (32 - n));
  }
}

/** H', argon2's hash of any length (RFC 9106, section 3.3). */
function hashPrime(length: number, input: Uint8Array): Uint8Array {
  const prefixed = concat([le32(length), input]);
  if (length <= 64) return blake2b(prefixed, length);
  const out = new Uint8Array(length);
  const whole = Math.ceil(length / 32) - 2;
  let v = blake2b(prefixed, 64);
  out.set(v.subarray(0, 32), 0);
  for (let i = 1; i < whole; i++) {
    v = blake2b(v, 64);
    out.set(v.subarray(0, 32), i * 32);
  }
  out.set(blake2b(v, length - 32 * whole), whole * 32);
  return out;
}

// BLAKE2b (RFC 7693), unkeyed, over a whole input at once: argon2 hashes a
// few short inputs with it.

const BLAKE2B_IV = new Uint32Array([
  0xf3bcc908, 0x6a09e667, 0x84caa73b, 0xbb67ae85, 0xfe94f82b, 0x3c6ef372, 0x5f1d36f1, 0xa54ff53a, 0xade682d1, 0x510e527f, 0x2b3e6c1f,
  0x9b05688c, 0xfb41bd6b, 0x1f83d9ab, 0x137e2179, 0x5be0cd19,
]);

const SIGMA = [
  [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15],
  [14, 10, 4, 8, 9, 15, 13, 6, 1, 12, 0, 2, 11, 7, 5, 3],
  [11, 8, 12, 0, 5, 2, 15, 13, 10, 14, 3, 6, 7, 1, 9, 4],
  [7, 9, 3, 1, 13, 12, 11, 14, 2, 6, 5, 10, 4, 0, 15, 8],
  [9, 0, 5, 7, 2, 4, 10, 15, 14, 1, 11, 12, 6, 8, 3, 13],
  [2, 12, 6, 10, 0, 11, 8, 3, 4, 13, 7, 5, 15, 14, 1, 9],
  [12, 5, 1, 15, 14, 13, 4, 10, 0, 7, 6, 3, 9, 2, 8, 11],
  [13, 11, 7, 14, 12, 1, 3, 9, 5, 0, 15, 4, 8, 6, 2, 10],
  [6, 15, 14, 9, 11, 3, 0, 8, 12, 2, 13, 7, 1, 4, 10, 5],
  [10, 2, 8, 4, 7, 6, 1, 5, 15, 11, 9, 14, 3, 12, 13, 0],
  [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15],
  [14, 10, 4, 8, 9, 15, 13, 6, 1, 12, 0, 2, 11, 7, 5, 3],
];

/** BLAKE2b of input, outLength bytes, 1 to 64. */
export function blake2b(input: Uint8Array, outLength: number): Uint8Array {
  if (!Number.isInteger(outLength) || outLength < 1 || outLength > 64) throw new RangeError(`blake2b: output length ${outLength} is not 1 to 64`);
  const h = BLAKE2B_IV.slice();
  h[0] = h[0]! ^ 0x01010000 ^ outLength;
  const v = new Uint32Array(32);
  const m = new Uint32Array(32);
  const block = new Uint8Array(128);
  const blocks = Math.max(1, Math.ceil(input.length / 128));
  for (let i = 0; i < blocks; i++) {
    const last = i === blocks - 1;
    block.fill(0);
    block.set(input.subarray(i * 128, Math.min(input.length, (i + 1) * 128)));
    for (let j = 0; j < 32; j++) m[j] = readLE32(block, j * 4);
    const counter = last ? input.length : (i + 1) * 128;
    compress(h, v, m, counter, last);
  }
  const out = new Uint8Array(64);
  for (let i = 0; i < 16; i++) writeLE32(out, i * 4, h[i]!);
  return out.slice(0, outLength);
}

function compress(h: Uint32Array, v: Uint32Array, m: Uint32Array, counter: number, last: boolean): void {
  for (let i = 0; i < 16; i++) {
    v[i] = h[i]!;
    v[i + 16] = BLAKE2B_IV[i]!;
  }
  v[24] = v[24]! ^ counter;
  v[25] = v[25]! ^ Math.floor(counter / TWO_32);
  if (last) {
    v[28] = ~v[28]!;
    v[29] = ~v[29]!;
  }
  for (const s of SIGMA) {
    blake2bG(v, m, 0, 8, 16, 24, s[0]!, s[1]!);
    blake2bG(v, m, 2, 10, 18, 26, s[2]!, s[3]!);
    blake2bG(v, m, 4, 12, 20, 28, s[4]!, s[5]!);
    blake2bG(v, m, 6, 14, 22, 30, s[6]!, s[7]!);
    blake2bG(v, m, 0, 10, 20, 30, s[8]!, s[9]!);
    blake2bG(v, m, 2, 12, 22, 24, s[10]!, s[11]!);
    blake2bG(v, m, 4, 14, 16, 26, s[12]!, s[13]!);
    blake2bG(v, m, 6, 8, 18, 28, s[14]!, s[15]!);
  }
  for (let i = 0; i < 16; i++) h[i] = h[i]! ^ v[i]! ^ v[i + 16]!;
}

/** BLAKE2b's G on the words at a, b, c and d (low halves' indices), mixing in message words x and y. */
function blake2bG(v: Uint32Array, m: Uint32Array, a: number, b: number, c: number, d: number, x: number, y: number): void {
  add(v, a, v[b]!, v[b + 1]!);
  add(v, a, m[x * 2]!, m[x * 2 + 1]!);
  rotate(v, d, a, 32);
  add(v, c, v[d]!, v[d + 1]!);
  rotate(v, b, c, 24);
  add(v, a, v[b]!, v[b + 1]!);
  add(v, a, m[y * 2]!, m[y * 2 + 1]!);
  rotate(v, d, a, 16);
  add(v, c, v[d]!, v[d + 1]!);
  rotate(v, b, c, 63);
}

/** v[x] += (low, high), modulo 2^64. */
function add(v: Uint32Array, x: number, low: number, high: number): void {
  const sum = v[x]! + low;
  v[x + 1] = v[x + 1]! + high + (sum >= TWO_32 ? 1 : 0);
  v[x] = sum;
}

function setWord(block: Uint32Array, word: number, value: number): void {
  block[word * 2] = value >>> 0;
  block[word * 2 + 1] = Math.floor(value / TWO_32);
}

function le32(n: number): Uint8Array {
  const out = new Uint8Array(4);
  writeLE32(out, 0, n >>> 0);
  return out;
}

function readLE32(bytes: Uint8Array, at: number): number {
  return (bytes[at]! | (bytes[at + 1]! << 8) | (bytes[at + 2]! << 16) | (bytes[at + 3]! << 24)) >>> 0;
}

function writeLE32(bytes: Uint8Array, at: number, n: number): void {
  bytes[at] = n & 0xff;
  bytes[at + 1] = (n >>> 8) & 0xff;
  bytes[at + 2] = (n >>> 16) & 0xff;
  bytes[at + 3] = (n >>> 24) & 0xff;
}

function bytesToWords(bytes: Uint8Array, words: Uint32Array, at: number): void {
  for (let i = 0; i < BLOCK_WORDS; i++) words[at + i] = readLE32(bytes, i * 4);
}

function wordsToBytes(words: Uint32Array): Uint8Array {
  const out = new Uint8Array(words.length * 4);
  for (let i = 0; i < words.length; i++) writeLE32(out, i * 4, words[i]!);
  return out;
}

function concat(parts: readonly Uint8Array[]): Uint8Array {
  let length = 0;
  for (const p of parts) length += p.length;
  const out = new Uint8Array(length);
  let at = 0;
  for (const p of parts) {
    out.set(p, at);
    at += p.length;
  }
  return out;
}
