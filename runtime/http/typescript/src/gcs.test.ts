import { describe, expect, test } from 'bun:test';
import { EMULATOR_SIGNER, emulatorEndpoint, openBucket } from './gcs';
import { ObjectNotFoundError, checkObjectName, checkSignedUrlOptions } from './index';

/*
The GCS implementation of Bucket (D54). The signing and the refusals run
anywhere; the tests that put, get, list and delete run against a
fake-gcs-server whose base URL SUPERSCHEMATIC_TEST_GCS_EMULATOR names, with
a bucket named by SUPERSCHEMATIC_TEST_GCS_BUCKET, test-bucket by default,
which they create:

  docker run --rm -p 127.0.0.1:4443:4443 fsouza/fake-gcs-server:1.56.1 -scheme http -port 4443
  SUPERSCHEMATIC_TEST_GCS_EMULATOR=http://127.0.0.1:4443 bun test src/gcs.test.ts
*/

const emulator = process.env.SUPERSCHEMATIC_TEST_GCS_EMULATOR ?? '';
const bucketName = process.env.SUPERSCHEMATIC_TEST_GCS_BUCKET ?? 'test-bucket';

describe('emulatorEndpoint', () => {
  test('a connection names its emulator, or STORAGE_EMULATOR_HOST does, or none does', () => {
    expect(emulatorEndpoint({ name: 'b', endpoint: 'http://127.0.0.1:24443' }, {})).toBe('http://127.0.0.1:24443');
    expect(emulatorEndpoint({ name: 'b' }, { STORAGE_EMULATOR_HOST: 'localhost:9023' })).toBe('http://localhost:9023');
    expect(emulatorEndpoint({ name: 'b' }, { STORAGE_EMULATOR_HOST: 'https://storage.test/' })).toBe('https://storage.test');
    expect(emulatorEndpoint({ name: 'b' }, {})).toBeUndefined();
  });
});

describe('signed URLs on an emulator', () => {
  test('are V4 URLs for the emulator host and the object path, signed by no account', async () => {
    const bucket = openBucket({ name: 'shop-media', endpoint: 'http://127.0.0.1:24443' }, { env: {} });
    const url = new URL(await bucket.signedUrl('products/a b/image.png', { method: 'PUT', expiresInSeconds: 900, contentType: 'image/png' }));
    expect(url.origin).toBe('http://127.0.0.1:24443');
    expect(url.pathname).toBe('/shop-media/products/a%20b/image.png');
    expect(url.searchParams.get('X-Goog-Algorithm')).toBe('GOOG4-RSA-SHA256');
    expect(url.searchParams.get('X-Goog-Expires')).toBe('900');
    expect(url.searchParams.get('X-Goog-Credential')).toStartWith(`${EMULATOR_SIGNER}/`);
    expect(url.searchParams.get('X-Goog-SignedHeaders')).toBe('content-type;host');
  });

  test('refuse what no provider signs', async () => {
    const bucket = openBucket({ name: 'shop-media', endpoint: 'http://127.0.0.1:24443' }, { env: {} });
    await expect(bucket.signedUrl('a', { method: 'GET', expiresInSeconds: 0 })).rejects.toThrow('expires after 0 seconds');
    await expect(bucket.signedUrl('', { method: 'GET', expiresInSeconds: 60 })).rejects.toThrow("an object's name is empty");
    expect(() => checkSignedUrlOptions({ method: 'GET', expiresInSeconds: 60, contentType: 'image/png' })).toThrow('only an upload sends');
    expect(() => checkObjectName('a\nb')).toThrow('line feed');
  });
});

describe.skipIf(emulator === '')('a bucket on fake-gcs-server', () => {
  test('puts, gets, lists, signs and deletes objects', async () => {
    const created = await fetch(`${emulator}/storage/v1/b?project=test`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: bucketName }),
    });
    expect([200, 409]).toContain(created.status);
    const bucket = openBucket({ name: bucketName, endpoint: emulator }, { env: {} });
    const prefix = `ts-${Date.now()}/`;

    const stored = await bucket.put(`${prefix}a.txt`, new Blob(['streamed bytes']).stream(), { contentType: 'text/plain' });
    expect(stored).toMatchObject({ name: `${prefix}a.txt`, size: 14, contentType: 'text/plain' });
    await bucket.put(`${prefix}b.txt`, 'two');
    await bucket.put(`${prefix}c.txt`, new TextEncoder().encode('three'));

    const read = await bucket.get(`${prefix}a.txt`);
    expect(read.object.contentType).toBe('text/plain');
    expect(await new Response(read.body).text()).toBe('streamed bytes');

    const first = await bucket.list({ prefix, pageSize: 2 });
    expect(first.objects.map(o => o.name)).toEqual([`${prefix}a.txt`, `${prefix}b.txt`]);
    expect(first.nextPageToken).toBeDefined();
    const second = await bucket.list({ prefix, pageSize: 2, pageToken: first.nextPageToken });
    expect(second.objects.map(o => o.name)).toEqual([`${prefix}c.txt`]);
    expect(second.nextPageToken).toBeUndefined();

    const upload = await bucket.signedUrl(`${prefix}upload.png`, { method: 'PUT', expiresInSeconds: 300, contentType: 'image/png' });
    expect((await fetch(upload, { method: 'PUT', headers: { 'Content-Type': 'image/png' }, body: 'png bytes' })).status).toBe(200);
    const download = await bucket.signedUrl(`${prefix}upload.png`, { method: 'GET', expiresInSeconds: 300 });
    const got = await fetch(download);
    expect(got.status).toBe(200);
    expect(await got.text()).toBe('png bytes');

    for (const name of ['a.txt', 'b.txt', 'c.txt', 'upload.png']) await bucket.delete(prefix + name);
    await expect(bucket.get(`${prefix}a.txt`)).rejects.toBeInstanceOf(ObjectNotFoundError);
    await expect(bucket.delete(`${prefix}a.txt`)).rejects.toBeInstanceOf(ObjectNotFoundError);
    expect((await bucket.list({ prefix })).objects).toEqual([]);
  });
});
