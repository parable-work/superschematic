import { afterAll, describe, expect, test } from 'bun:test';
import * as nodeCrypto from 'node:crypto';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { argon2id, blake2b } from './argon2';
import { DEFAULT_ARGON2_ITERATIONS, DEFAULT_ARGON2_MEMORY_KIB, DEFAULT_ARGON2_PARALLELISM } from './config';
import { hashPasswordWithSalt, portableArgon2idDeriver, setArgon2idDeriver, verifyPassword } from './password';
import type { Argon2Params } from './config';

/*
The portable argon2id (D92), which a Worker hashes passwords with: it writes
RFC 9106's vector, node:crypto's keys for the same inputs, and the identity
parity corpus's hashes, which the Go runtime wrote, so a user moves between a
server on Bun and one on Workers with the same password hash.
*/

function hex(bytes: Uint8Array): string {
  return Buffer.from(bytes).toString('hex');
}

function filled(length: number, byte: number): Uint8Array {
  return new Uint8Array(length).fill(byte);
}

interface NodeArgon2Options {
  message: Uint8Array;
  nonce: Uint8Array;
  parallelism: number;
  tagLength: number;
  memory: number;
  passes: number;
  secret?: Uint8Array;
  associatedData?: Uint8Array;
}

type NodeArgon2 = (algorithm: 'argon2id', options: NodeArgon2Options, callback: (error: Error | null, key: Uint8Array) => void) => void;

const nodeArgon2 = (nodeCrypto as unknown as { argon2?: NodeArgon2 }).argon2;

function nodeKey(options: NodeArgon2Options): Promise<Uint8Array> {
  return new Promise((resolve, reject) => nodeArgon2!('argon2id', options, (error, key) => (error ? reject(error) : resolve(key))));
}

describe('blake2b', () => {
  test("writes RFC 7693's BLAKE2b-512 of abc, and the empty input's", () => {
    expect(hex(blake2b(new TextEncoder().encode('abc'), 64))).toBe(
      'ba80a53f981c4d0d6a2797b69f12f6e94c212f14685ac4b74b12bb6fdbffa2d17d87c5392aab792dc252d5de4533cc9518d38aa8dbf1925ab92386edd4009923'
    );
    expect(hex(blake2b(new Uint8Array(0), 64))).toBe(
      '786a02f742015903c6c6fd852552d272912f4740e15847618a86e217f71f5419d25e1031afee585313896444934eb04b903a685b1448b755d56f701afe9be2ce'
    );
  });

  test("matches node:crypto's blake2b512 over inputs across block boundaries", () => {
    for (const length of [1, 127, 128, 129, 255, 256, 257, 1000]) {
      const input = new Uint8Array(length).map((_, i) => (i * 31 + 7) & 0xff);
      expect(hex(blake2b(input, 64))).toBe(nodeCrypto.createHash('blake2b512').update(input).digest('hex'));
    }
  });
});

describe('argon2id', () => {
  test("writes RFC 9106's argon2id vector, with a secret and associated data", () => {
    const tag = argon2id({
      password: filled(32, 0x01),
      salt: filled(16, 0x02),
      secret: filled(8, 0x03),
      associatedData: filled(12, 0x04),
      memoryKiB: 32,
      iterations: 3,
      parallelism: 4,
      tagLength: 32,
    });
    expect(hex(tag)).toBe('0d640df58d78766c08c037a34a8b53c9d01ef0452d75b65eb52520e96b01e659');
  });

  test.skipIf(typeof nodeArgon2 !== 'function')("writes node:crypto's keys across costs, lanes, lengths and inputs", async () => {
    const cases: NodeArgon2Options[] = [];
    let seed = 1;
    const next = (n: number): number => {
      seed = (seed * 1103515245 + 12345) % 2147483648;
      return seed % n;
    };
    for (let i = 0; i < 24; i++) {
      const parallelism = 1 + next(4);
      cases.push({
        message: nodeCrypto.randomBytes(next(40)),
        nonce: nodeCrypto.randomBytes(8 + next(24)),
        parallelism,
        tagLength: [4, 16, 32, 64, 65, 100, 1024][next(7)]!,
        memory: 8 * parallelism + next(600),
        passes: 1 + next(3),
        ...(i % 6 === 0 ? { secret: nodeCrypto.randomBytes(8), associatedData: nodeCrypto.randomBytes(12) } : {}),
      });
    }
    for (const c of cases) {
      const portable = argon2id({
        password: c.message,
        salt: c.nonce,
        memoryKiB: c.memory,
        iterations: c.passes,
        parallelism: c.parallelism,
        tagLength: c.tagLength,
        ...(c.secret ? { secret: c.secret } : {}),
        ...(c.associatedData ? { associatedData: c.associatedData } : {}),
      });
      expect({ ...c, key: hex(portable) }).toEqual({ ...c, key: hex(await nodeKey(c)) });
    }
  });

  test.skipIf(typeof nodeArgon2 !== 'function')("writes node:crypto's key at the identity runtime's default cost, and says how long it took", async () => {
    const params: Argon2Params = { memoryKiB: DEFAULT_ARGON2_MEMORY_KIB, iterations: DEFAULT_ARGON2_ITERATIONS, parallelism: DEFAULT_ARGON2_PARALLELISM };
    const password = new TextEncoder().encode('correct horse battery staple');
    const salt = nodeCrypto.randomBytes(16);
    const started = performance.now();
    const portable = argon2id({ password, salt, memoryKiB: params.memoryKiB, iterations: params.iterations, parallelism: params.parallelism, tagLength: 32 });
    const took = performance.now() - started;
    const native = await nodeKey({ message: password, nonce: salt, parallelism: params.parallelism, tagLength: 32, memory: params.memoryKiB, passes: params.iterations });
    expect(hex(portable)).toBe(hex(native));
    console.log(`portable argon2id at m=${params.memoryKiB} KiB, t=${params.iterations}, p=${params.parallelism}: ${took.toFixed(0)} ms`);
  });
});

describe('the parity corpus through the portable argon2id', () => {
  const corpus = JSON.parse(readFileSync(fileURLToPath(new URL('../../../testdata/identity_parity.json', import.meta.url)), 'utf8')) as {
    hashes: { name: string; password: string; salt: string; params: Argon2Params; phc: string }[];
    verify: { name: string; phc: string; password: string; current: Argon2Params; want: unknown }[];
  };
  setArgon2idDeriver(portableArgon2idDeriver);
  afterAll(() => setArgon2idDeriver(undefined));

  for (const c of corpus.hashes) {
    test(`hashes ${c.name}`, async () => {
      setArgon2idDeriver(portableArgon2idDeriver);
      expect(await hashPasswordWithSalt(c.password, new Uint8Array(Buffer.from(c.salt, 'base64')), c.params)).toBe(c.phc);
    });
  }
  for (const c of corpus.verify) {
    test(`verifies ${c.name}`, async () => {
      setArgon2idDeriver(portableArgon2idDeriver);
      expect(await verifyPassword(c.phc, c.password, c.current)).toEqual(c.want as never);
    });
  }
});
