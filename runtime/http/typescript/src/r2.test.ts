import { describe, expect, test } from 'bun:test';
import { MAX_SIGNED_URL_SECONDS, ObjectNotFoundError } from './bucket';
import {
  isR2BucketBinding,
  openR2Bucket,
  presignV4,
  r2Host,
  r2SecretAccessKey,
  s3Encode,
  type R2BucketBinding,
  type R2ObjectBodyLike,
  type R2ObjectLike,
  type R2PutValue,
} from './r2';

/*
D54's Bucket over an R2 binding (D64, section 18.7): objects through the
binding, which a fake holds in memory as R2 would, and V4 URLs presigned
for R2's S3 API with WebCrypto, held to AWS's published example of a
presigned URL.
*/

/** An r2_bucket binding over a map, which lists in key order and pages by an opaque cursor, as R2 does. */
function fakeR2(): R2BucketBinding & { readonly objects: Map<string, { bytes: Uint8Array; contentType?: string }> } {
  const objects = new Map<string, { bytes: Uint8Array; contentType?: string }>();
  const meta = (key: string): R2ObjectLike => {
    const object = objects.get(key)!;
    return { key, size: object.bytes.length, uploaded: new Date(0), ...(object.contentType ? { httpMetadata: { contentType: object.contentType } } : {}) };
  };
  return {
    objects,
    async put(key: string, value: R2PutValue, options?: { httpMetadata?: { contentType?: string } }) {
      if (value instanceof ReadableStream) throw new TypeError('Provided readable stream must have a known length (request/response body or readable half of FixedLengthStream)');
      const bytes = typeof value === 'string' ? new TextEncoder().encode(value) : value === null ? new Uint8Array() : new Uint8Array(value instanceof ArrayBuffer ? value : value.buffer);
      objects.set(key, { bytes, ...(options?.httpMetadata?.contentType ? { contentType: options.httpMetadata.contentType } : {}) });
      return meta(key);
    },
    async get(key: string): Promise<R2ObjectBodyLike | null> {
      if (!objects.has(key)) return null;
      return { ...meta(key), body: new Response(objects.get(key)!.bytes).body! };
    },
    async head(key: string) {
      return objects.has(key) ? meta(key) : null;
    },
    async delete(keys: string | string[]) {
      for (const key of Array.isArray(keys) ? keys : [keys]) objects.delete(key);
    },
    async list(options = {}) {
      const keys = [...objects.keys()].filter(key => key.startsWith(options.prefix ?? '')).sort();
      const start = options.cursor ? Number(options.cursor) : 0;
      const limit = options.limit ?? 1000;
      const page = keys.slice(start, start + limit);
      const truncated = start + limit < keys.length;
      return { objects: page.map(meta), truncated, ...(truncated ? { cursor: String(start + limit) } : {}) };
    },
  };
}

const credential = { account: '0123456789abcdef0123456789abcdef', accessKeyId: '8f2b4d6e0a1c3e5f7b9d1f3a5c7e9b0d', token: 'r2-token-value' };
const connection = { name: 'shop-stack-shop-media', binding: 'SHOP_MEDIA_BUCKET', r2: credential };
const at = new Date('2026-10-19T12:00:00Z');

describe('openR2Bucket', () => {
  test('puts, gets, lists and deletes objects through the binding', async () => {
    const r2 = fakeR2();
    expect(isR2BucketBinding(r2)).toBe(true);
    expect(isR2BucketBinding({ get() {} })).toBe(false);
    const bucket = openR2Bucket(r2, connection);
    expect(bucket.name).toBe('shop-stack-shop-media');
    const put = await bucket.put('products/tea.txt', 'tea', { contentType: 'text/plain' });
    expect(put).toEqual({ name: 'products/tea.txt', size: 3, contentType: 'text/plain', updated: new Date(0) });
    // A stream is read whole, since R2 takes only a stream whose length it knows.
    await bucket.put('products/cocoa.txt', new Response('cocoa').body!);
    const read = await bucket.get('products/cocoa.txt');
    expect(read.object.size).toBe(5);
    expect(await new Response(read.body).text()).toBe('cocoa');
    await bucket.put('other.txt', new Uint8Array([1]));
    const first = await bucket.list({ prefix: 'products/', pageSize: 1 });
    expect(first.objects.map(object => object.name)).toEqual(['products/cocoa.txt']);
    const second = await bucket.list({ prefix: 'products/', pageSize: 1, pageToken: first.nextPageToken! });
    expect(second.objects.map(object => object.name)).toEqual(['products/tea.txt']);
    expect(second.nextPageToken).toBeUndefined();
    await bucket.delete('products/tea.txt');
    expect(r2.objects.has('products/tea.txt')).toBe(false);
    await expect(bucket.get('products/tea.txt')).rejects.toBeInstanceOf(ObjectNotFoundError);
    await expect(bucket.delete('products/tea.txt')).rejects.toBeInstanceOf(ObjectNotFoundError);
    await expect(bucket.list({ pageSize: 1001 })).rejects.toThrow('want a whole number from 1 to 1000');
  });

  test('signs a URL for R2 S3 API with the token as its credential', async () => {
    const bucket = openR2Bucket(fakeR2(), connection, { now: () => at });
    const url = new URL(await bucket.signedUrl('products/7b0e/image one.png', { method: 'PUT', expiresInSeconds: 900, contentType: 'image/png' }));
    expect(url.origin).toBe(`https://${r2Host(credential.account)}`);
    expect(url.pathname).toBe('/shop-stack-shop-media/products/7b0e/image%20one.png');
    expect(url.searchParams.get('X-Amz-Algorithm')).toBe('AWS4-HMAC-SHA256');
    expect(url.searchParams.get('X-Amz-Credential')).toBe(`${credential.accessKeyId}/20261019/auto/s3/aws4_request`);
    expect(url.searchParams.get('X-Amz-Date')).toBe('20261019T120000Z');
    expect(url.searchParams.get('X-Amz-Expires')).toBe('900');
    expect(url.searchParams.get('X-Amz-SignedHeaders')).toBe('content-type;host');
    // The same signature, made with the secret the token derives.
    const secret = await r2SecretAccessKey(credential.token);
    const again = await presignV4({
      method: 'PUT',
      host: r2Host(credential.account),
      path: '/shop-stack-shop-media/products/7b0e/image one.png',
      region: 'auto',
      accessKeyId: credential.accessKeyId,
      secretAccessKey: secret,
      date: at,
      expiresInSeconds: 900,
      contentType: 'image/png',
    });
    expect(url.href).toBe(again);
    const get = new URL(await bucket.signedUrl('products/7b0e/image one.png', { method: 'GET', expiresInSeconds: MAX_SIGNED_URL_SECONDS }));
    expect(get.searchParams.get('X-Amz-SignedHeaders')).toBe('host');
    expect(get.searchParams.get('X-Amz-Expires')).toBe('604800');
  });

  test('refuses a URL for more than seven days, and one without a credential', async () => {
    const bucket = openR2Bucket(fakeR2(), connection, { now: () => at });
    await expect(bucket.signedUrl('a', { method: 'GET', expiresInSeconds: MAX_SIGNED_URL_SECONDS + 1 })).rejects.toThrow('at most 604800');
    const unsigned = openR2Bucket(fakeR2(), { name: 'm', binding: 'B' });
    await expect(unsigned.signedUrl('a', { method: 'GET', expiresInSeconds: 60 })).rejects.toThrow('has no R2 credential');
  });
});

describe('presignV4', () => {
  test("signs AWS's example of a presigned URL as AWS does", async () => {
    // "Authenticating Requests: Using Query Parameters (AWS Signature
    // Version 4)", Amazon S3's API reference: GET examplebucket's test.txt
    // for a day from 2013-05-24, as the example account.
    const url = await presignV4({
      method: 'GET',
      host: 'examplebucket.s3.amazonaws.com',
      path: '/test.txt',
      region: 'us-east-1',
      accessKeyId: 'AKIAIOSFODNN7EXAMPLE',
      secretAccessKey: 'wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY',
      date: new Date('2013-05-24T00:00:00Z'),
      expiresInSeconds: 86400,
    });
    expect(url).toBe(
      'https://examplebucket.s3.amazonaws.com/test.txt?X-Amz-Algorithm=AWS4-HMAC-SHA256' +
        '&X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20130524%2Fus-east-1%2Fs3%2Faws4_request' +
        '&X-Amz-Date=20130524T000000Z&X-Amz-Expires=86400&X-Amz-SignedHeaders=host' +
        '&X-Amz-Signature=aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404'
    );
  });

  test('encodes as S3 does', () => {
    expect(s3Encode('a b/c~d_e.f-g*h')).toBe('a%20b%2Fc~d_e.f-g%2Ah');
    expect(s3Encode('/a b/c', true)).toBe('/a%20b/c');
    expect(s3Encode('é')).toBe('%C3%A9');
  });

  test("an R2 secret is the hex SHA-256 of the token's value", async () => {
    expect(await r2SecretAccessKey('abc')).toBe('ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad');
  });
});
