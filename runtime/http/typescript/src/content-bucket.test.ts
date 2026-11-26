import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import {
  CONTENT_PREFIX,
  ContentTooLargeError,
  DEFAULT_CONTENT_MAX_BYTES,
  ObjectNotFoundError,
  checkObjectName,
  checkSignedUrlOptions,
  contentBucket,
  contentObjectName,
  type Bucket,
  type BucketBody,
  type BucketObject,
  type PutOptions,
  type PutResult,
} from './index';

/*
A content bucket (D142): each vector of
runtime/http/testdata/content_bucket_parity.json answered as the Go
runtime's bucket.ContentBucket answers it, over a bucket kept in memory
whose put with ifAbsent checks and writes in one step, as a provider does.
*/

/** A Bucket kept in memory. */
function memoryBucket(): Bucket & { readonly objects: Map<string, { bytes: Uint8Array; contentType?: string }>; puts: number; heads: number } {
  const objects = new Map<string, { bytes: Uint8Array; contentType?: string }>();
  const objectOf = (name: string): BucketObject => {
    const held = objects.get(name)!;
    return { name, size: held.bytes.byteLength, ...(held.contentType ? { contentType: held.contentType } : {}) };
  };
  const bucket = {
    name: 'mem',
    objects,
    puts: 0,
    heads: 0,
    async put(name: string, body: BucketBody, options: PutOptions = {}): Promise<PutResult> {
      checkObjectName(name);
      const bytes = typeof body === 'string' ? new TextEncoder().encode(body) : body instanceof Uint8Array ? body : new Uint8Array(await new Response(body).arrayBuffer());
      bucket.puts += 1;
      if (options.ifAbsent && objects.has(name)) return { ...objectOf(name), created: false };
      objects.set(name, { bytes, ...(options.contentType ? { contentType: options.contentType } : {}) });
      return { ...objectOf(name), created: true };
    },
    async head(name: string) {
      bucket.heads += 1;
      return objects.has(name) ? objectOf(name) : undefined;
    },
    async get(name: string) {
      if (!objects.has(name)) throw new ObjectNotFoundError('mem', name);
      return { object: objectOf(name), body: new Response(objects.get(name)!.bytes).body! };
    },
    async delete(name: string) {
      if (!objects.delete(name)) throw new ObjectNotFoundError('mem', name);
    },
    async list() {
      return { objects: [...objects.keys()].sort().map(objectOf) };
    },
    async signedUrl(name: string, options: { method: 'GET' | 'PUT'; expiresInSeconds: number; contentType?: string }) {
      checkSignedUrlOptions(options);
      return `https://mem.example/${name}?method=${options.method}&expires=${options.expiresInSeconds}`;
    },
  };
  return bucket;
}

interface Parity {
  maxBytes: number;
  puts: { name: string; text?: string; bytes?: number[]; hash?: string; size?: number; created?: boolean; object?: string; tooLarge?: boolean }[];
  hashes: { valid: string[]; invalid: string[] };
  missing: { name: string; holds: string[]; asked: string[]; missing: string[] }[];
}

const parity = JSON.parse(readFileSync(new URL('../../testdata/content_bucket_parity.json', import.meta.url), 'utf8')) as Parity;

describe('a content bucket (D142)', () => {
  test('puts each body of the shared vectors under its hash, once', async () => {
    const raw = memoryBucket();
    const content = contentBucket(raw, { maxBytes: parity.maxBytes });
    for (const vector of parity.puts) {
      const body = vector.bytes ? new Uint8Array(vector.bytes) : vector.text!;
      const before = raw.objects.size;
      if (vector.tooLarge) {
        await expect(content.put(body)).rejects.toBeInstanceOf(ContentTooLargeError);
        expect(raw.objects.size).toBe(before);
        continue;
      }
      expect([vector.name, await content.put(body, { contentType: 'text/plain' })]).toEqual([vector.name, { hash: vector.hash!, size: vector.size!, created: vector.created! }]);
      const stored = raw.objects.get(vector.object!)!;
      const bytes = typeof body === 'string' ? new TextEncoder().encode(body) : body;
      expect([...stored.bytes]).toEqual([...bytes]);
      expect(stored.contentType).toBe('text/plain');
      const read = await content.get(vector.hash!);
      expect([...new Uint8Array(await new Response(read.body).arrayBuffer())]).toEqual([...bytes]);
    }
  });

  test('reads a stream whole, and cancels one that runs past maxBytes having written nothing', async () => {
    const raw = memoryBucket();
    const content = contentBucket(raw, { maxBytes: 8 });
    const chunks = (parts: string[], onCancel?: () => void) =>
      new ReadableStream<Uint8Array>({
        pull(controller) {
          const part = parts.shift();
          if (part === undefined) controller.close();
          else controller.enqueue(new TextEncoder().encode(part));
        },
        cancel: onCancel,
      });
    expect(await content.put(chunks(['he', 'llo']))).toEqual({ hash: '2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824', size: 5, created: true });
    let cancelled = false;
    await expect(content.put(chunks(['12345', '6789', 'never read'], () => (cancelled = true)))).rejects.toBeInstanceOf(ContentTooLargeError);
    expect(cancelled).toBe(true);
    expect(raw.objects.size).toBe(1);
  });

  test('names a hash, and refuses what is none in every method', async () => {
    const content = contentBucket(memoryBucket());
    for (const hash of parity.hashes.valid) expect(contentObjectName(hash)).toBe(CONTENT_PREFIX + hash);
    for (const hash of parity.hashes.invalid) {
      expect(() => contentObjectName(hash)).toThrow(/content hash/);
      await expect(content.has(hash)).rejects.toThrow(/content hash/);
      await expect(Promise.resolve().then(() => content.get(hash))).rejects.toThrow(/content hash/);
      await expect(Promise.resolve().then(() => content.signedUrl(hash, { expiresInSeconds: 60 }))).rejects.toThrow(/content hash/);
      await expect(content.missing([parity.hashes.valid[0]!, hash])).rejects.toThrow(/content hash/);
    }
  });

  for (const vector of parity.missing) {
    test(`missing: ${vector.name}`, async () => {
      const content = contentBucket(memoryBucket());
      for (const text of vector.holds) await content.put(text);
      expect(await content.missing(vector.asked)).toEqual(vector.missing);
    });
  }

  test('has, get, a signed GET, the default limit, and raw', async () => {
    const raw = memoryBucket();
    const content = contentBucket(raw);
    expect([content.maxBytes, DEFAULT_CONTENT_MAX_BYTES, content.raw, content.name]).toEqual([32 * 1024 * 1024, 32 * 1024 * 1024, raw, 'mem']);
    const absent = '0'.repeat(64);
    expect(await content.has(absent)).toBe(false);
    await expect(content.get(absent)).rejects.toBeInstanceOf(ObjectNotFoundError);
    const { hash } = await content.put('hello');
    expect(await content.has(hash)).toBe(true);
    expect(await content.signedUrl(hash, { expiresInSeconds: 300 })).toBe(`https://mem.example/sha256/${hash}?method=GET&expires=300`);
    await expect(content.signedUrl(hash, { expiresInSeconds: 0 })).rejects.toThrow(/expires/);
    expect(() => contentBucket(raw, { maxBytes: 0 })).toThrow(/maxBytes/);
    expect(() => contentBucket(raw, { maxBytes: 1.5 })).toThrow(/maxBytes/);
  });

  test('of many puts of the same bytes at once, one creates the object', async () => {
    const raw = memoryBucket();
    const content = contentBucket(raw);
    const results = await Promise.all(Array.from({ length: 32 }, () => content.put('raced')));
    expect(results.filter(result => result.created).length).toBe(1);
    expect(raw.objects.size).toBe(1);
  });

  test('asks about many hashes a few at a time', async () => {
    const raw = memoryBucket();
    let running = 0;
    let most = 0;
    const head = raw.head.bind(raw);
    raw.head = async (name: string) => {
      running += 1;
      most = Math.max(most, running);
      await new Promise(resolve => setTimeout(resolve, 1));
      running -= 1;
      return head(name);
    };
    const hashes = Array.from({ length: 100 }, (_, index) => index.toString(16).padStart(64, '0'));
    expect(await contentBucket(raw).missing(hashes)).toEqual(hashes);
    expect(most).toBeLessThanOrEqual(16);
    expect(most).toBeGreaterThan(1);
  });
});
