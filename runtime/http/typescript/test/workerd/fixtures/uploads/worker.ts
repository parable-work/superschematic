// The uploads fixture (D154): a direct upload to R2 through a signed URL,
// over the runtime's built entries on workerd.
//
// R2's S3 API is the one part of the path workerd does not serve, so a
// stand-in here plays it from the specification (AWS Signature Version 4,
// query-string authentication, unsigned payload), written with WebCrypto
// apart from the runtime's signer: it refuses a PUT whose method, path,
// query, signed headers or expiry differ from what the URL was signed for,
// and stores the bytes of one it accepts through the r2_bucket binding, as
// R2 does. The test handler then:
//   - signs an attachment's upload URL, as orm.attachments.begin does: a
//     PUT with the file's content type, for an object under attachments/;
//   - sends the PUT a browser sends, which the stand-in accepts, and one
//     with another content type, which it refuses;
//   - reads the object back through the binding and hashes it as it
//     streams, as orm.attachments.complete does, with workerd's native
//     DigestStream, and holds it to the SHA-256 the upload declared.
import { env } from 'cloudflare:workers';
import { loadBucket, sha256Stream } from '../../../../dist/index.js';
import { openR2Bucket, type R2BucketBinding } from '../../../../dist/r2.js';
import { binding, stackEnvOf } from '../../../../dist/workers.js';

const vars = stackEnvOf(env);
const files = loadBucket('RECORDS_FILES_BUCKET', vars);
const token = 'r2-token-value';

const encoder = new TextEncoder();

function hex(bytes: ArrayBuffer | Uint8Array): string {
  return Array.from(new Uint8Array(bytes), b => b.toString(16).padStart(2, '0')).join('');
}

async function hmac(key: BufferSource, data: string): Promise<ArrayBuffer> {
  const imported = await crypto.subtle.importKey('raw', key, { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']);
  return crypto.subtle.sign('HMAC', imported, encoder.encode(data));
}

async function sha256Hex(text: string | Uint8Array): Promise<string> {
  return hex(await crypto.subtle.digest('SHA-256', typeof text === 'string' ? encoder.encode(text) : text));
}

/** RFC 3986's encoding, as S3's canonical query takes it. */
function encode(value: string): string {
  return encodeURIComponent(value).replace(/[!'()*]/g, c => `%${c.charCodeAt(0).toString(16).toUpperCase()}`);
}

/** R2's S3 API, standing in: a presigned PUT checked as S3 checks it, then stored through the binding. */
async function s3(request: Request, r2: R2BucketBinding, bucketName: string): Promise<Response> {
  const url = new URL(request.url);
  const q = url.searchParams;
  if (q.get('X-Amz-Algorithm') !== 'AWS4-HMAC-SHA256') return new Response('unsigned', { status: 403 });
  const signed = q.get('X-Amz-SignedHeaders')!.split(';');
  const headerOf = (name: string) => (name === 'host' ? url.host : (request.headers.get(name) ?? ''));
  const query = [...q.entries()]
    .filter(([k]) => k !== 'X-Amz-Signature')
    .map(([k, v]) => [encode(k), encode(v)] as const)
    .sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
    .map(([k, v]) => `${k}=${v}`)
    .join('&');
  const canonical = [request.method, url.pathname, query, signed.map(h => `${h}:${headerOf(h).trim()}\n`).join(''), signed.join(';'), 'UNSIGNED-PAYLOAD'].join('\n');
  const stamp = q.get('X-Amz-Date')!;
  const scope = q.get('X-Amz-Credential')!.split('/').slice(1).join('/');
  const toSign = ['AWS4-HMAC-SHA256', stamp, scope, await sha256Hex(canonical)].join('\n');
  let key: ArrayBuffer | Uint8Array = encoder.encode(`AWS4${await sha256Hex(token)}`);
  for (const part of [stamp.slice(0, 8), 'auto', 's3', 'aws4_request']) key = await hmac(key, part);
  if (hex(await hmac(key, toSign)) !== q.get('X-Amz-Signature')) return new Response('SignatureDoesNotMatch', { status: 403 });
  const signedAt = Date.parse(`${stamp.slice(0, 4)}-${stamp.slice(4, 6)}-${stamp.slice(6, 8)}T${stamp.slice(9, 11)}:${stamp.slice(11, 13)}:${stamp.slice(13, 15)}Z`);
  if (Date.now() > signedAt + Number(q.get('X-Amz-Expires')) * 1000) return new Response('expired', { status: 403 });
  const [, bucket, ...object] = url.pathname.split('/');
  if (request.method !== 'PUT' || bucket !== bucketName) return new Response('no such bucket', { status: 404 });
  const contentType = request.headers.get('content-type') ?? undefined;
  await r2.put(decodeURIComponent(object.join('/')), await request.arrayBuffer(), contentType ? { httpMetadata: { contentType } } : undefined);
  return new Response(null, { status: 200 });
}

const handler = {
  async fetch(): Promise<Response> {
    return new Response('not found', { status: 404 });
  },

  async test(): Promise<void> {
    const failures: string[] = [];
    const expect = (what: string, got: unknown, want: unknown) => {
      if (JSON.stringify(got) !== JSON.stringify(want)) failures.push(`${what}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
    };
    if (!files.binding) throw new Error('the bucket names no binding');
    const r2 = binding(env, files.binding, 'r2') as R2BucketBinding;
    const bucket = openR2Bucket(r2, files);
    const bytes = encoder.encode('The plan, in brief.\n'.repeat(4096));
    const declared = await sha256Hex(bytes);
    const name = `attachments/${crypto.randomUUID()}`;

    // begin: the upload's URL, a PUT with the file's content type.
    const url = await bucket.signedUrl(name, { method: 'PUT', expiresInSeconds: 3600, contentType: 'text/plain' });
    expect('the URL is R2 S3 API', new URL(url).host, '0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com');
    // The browser's PUT, and one with another content type.
    const refused = await s3(new Request(url, { method: 'PUT', headers: { 'content-type': 'text/html' }, body: bytes }), r2, files.name);
    expect('a PUT of another content type', refused.status, 403);
    const put = await s3(new Request(url, { method: 'PUT', headers: { 'content-type': 'text/plain' }, body: bytes }), r2, files.name);
    expect('the browser PUT', put.status, 200);

    // complete: the bytes read back through the binding, hashed as they stream.
    const read = await bucket.get(name);
    expect('the object', [read.object.size, read.object.contentType], [bytes.length, 'text/plain']);
    expect('workerd has a native DigestStream', typeof (crypto as unknown as { DigestStream?: unknown }).DigestStream, 'function');
    expect('the hash', await sha256Stream(read.body, bytes.length), { hash: declared, size: bytes.length });
    const short = await bucket.get(name);
    expect('a stream past its limit', (await sha256Stream(short.body, 10)).hash, '');

    if (failures.length > 0) throw new Error(`uploads fixture:\n${failures.join('\n')}`);
    console.log('uploads fixture: ok');
  },
};

export default handler;
