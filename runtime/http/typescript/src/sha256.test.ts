import { describe, expect, test } from 'bun:test';
import { createHash } from 'node:crypto';
import { Sha256, sha256, sha256Stream } from './sha256';

/*
SHA-256 whole and a chunk at a time (D154), held to node:crypto's over
lengths around each block's edges and over every split of a message into
chunks of a few sizes.
*/

const hex = (bytes: Uint8Array) => Array.from(bytes, b => b.toString(16).padStart(2, '0')).join('');

describe('Sha256', () => {
  test('hashes a message whole as node:crypto does, at every length around a block edge', () => {
    for (const length of [0, 1, 55, 56, 57, 63, 64, 65, 119, 120, 128, 1000]) {
      const data = Uint8Array.from({ length }, (_, i) => (i * 31 + 7) & 0xff);
      expect(hex(sha256(data))).toBe(createHash('sha256').update(data).digest('hex'));
    }
  });

  test('hashes a message given a chunk at a time as it does whole', () => {
    const data = Uint8Array.from({ length: 777 }, (_, i) => (i * 13) & 0xff);
    const want = createHash('sha256').update(data).digest('hex');
    for (const size of [1, 3, 63, 64, 65, 200]) {
      const hash = new Sha256();
      for (let i = 0; i < data.length; i += size) hash.update(data.subarray(i, i + size));
      expect(hex(hash.digest())).toBe(want);
    }
  });

  test('refuses an update or a digest after its digest', () => {
    const hash = new Sha256().update(new Uint8Array([1]));
    hash.digest();
    expect(() => hash.update(new Uint8Array([2]))).toThrow();
    expect(() => hash.digest()).toThrow();
  });
});

describe('sha256Stream', () => {
  test('hashes a stream as it reads it, and stops past its limit', async () => {
    const data = Uint8Array.from({ length: 5000 }, (_, i) => i & 0xff);
    const stream = () => new Response(data).body!;
    expect(await sha256Stream(stream())).toEqual({ hash: createHash('sha256').update(data).digest('hex'), size: 5000 });
    expect((await sha256Stream(stream(), 100)).hash).toBe('');
  });
});
