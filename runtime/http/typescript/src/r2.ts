import {
  DEFAULT_PAGE_SIZE,
  ObjectNotFoundError,
  checkObjectName,
  checkSignedUrlOptions,
  type Bucket,
  type BucketBody,
  type BucketObject,
  type BucketRead,
  type ListOptions,
  type ListPage,
  type PutOptions,
  type SignedUrlOptions,
} from './bucket.js';
import type { BucketConnection, R2Credential } from './stackconfig.js';

/*
A bucket on R2 (D64, section 18.7 of docs/stack-model.md): D54's Bucket over
a Worker's r2_bucket binding. Objects are put, got, deleted and listed
through the binding. signedUrl presigns a V4 query-string URL for R2's S3
API, https://<account>.r2.cloudflarestorage.com/<bucket>/<object>, or
https://<account>.<jurisdiction>.r2.cloudflarestorage.com/<bucket>/<object>
for a bucket in a jurisdiction (eu, fedramp), with
WebCrypto's HMAC, since a Worker has no identity to sign with: the bucket
edge's account token, scoped to the bucket's objects, is the credential.
R2 takes the token's id as the access key and the hex SHA-256 of its value
as the secret, which this computes once per bucket.

This is the package's ./r2 entry. It imports nothing from node: and
declares the few R2 types it uses, so a Worker bundles it with no
@cloudflare/workers-types.
*/

/** What R2 holds of an object beside its bytes, as the binding gives it. */
export interface R2ObjectLike {
  readonly key: string;
  readonly size: number;
  readonly uploaded: Date;
  readonly httpMetadata?: { readonly contentType?: string };
}

/** An object get read, with its bytes. */
export interface R2ObjectBodyLike extends R2ObjectLike {
  readonly body: ReadableStream<Uint8Array>;
}

/** A page of a list. */
export interface R2ObjectsLike {
  readonly objects: readonly R2ObjectLike[];
  readonly truncated: boolean;
  readonly cursor?: string;
}

/** What R2's put takes. */
export type R2PutValue = ReadableStream | ArrayBuffer | ArrayBufferView | string | null;

/** The part of an r2_bucket binding (R2Bucket) the Bucket calls. */
export interface R2BucketBinding {
  put(key: string, value: R2PutValue, options?: { httpMetadata?: { contentType?: string } }): Promise<R2ObjectLike | null>;
  get(key: string): Promise<R2ObjectBodyLike | null>;
  head(key: string): Promise<R2ObjectLike | null>;
  delete(keys: string | string[]): Promise<void>;
  list(options?: { prefix?: string; cursor?: string; limit?: number; include?: ('httpMetadata' | 'customMetadata')[] }): Promise<R2ObjectsLike>;
}

/** Whether value has the methods of an r2_bucket binding. */
export function isR2BucketBinding(value: unknown): value is R2BucketBinding {
  if (typeof value !== 'object' || value === null) return false;
  const binding = value as Partial<Record<keyof R2BucketBinding, unknown>>;
  return (['put', 'get', 'head', 'delete', 'list'] as const).every(method => typeof binding[method] === 'function');
}

/** The region R2's S3 API signs for. */
export const R2_REGION = 'auto';

/**
 * The host of an account's R2 S3 API: <account>.r2.cloudflarestorage.com,
 * or for a bucket in a jurisdiction, <account>.<jurisdiction>.r2.cloudflarestorage.com
 * (D64, amended).
 */
export function r2Host(account: string, jurisdiction?: string): string {
  return jurisdiction ? `${account}.${jurisdiction}.r2.cloudflarestorage.com` : `${account}.r2.cloudflarestorage.com`;
}

/** What openR2Bucket takes beside the binding and the connection. */
export interface OpenR2BucketOptions {
  /** The clock signedUrl signs with; Date.now by default. */
  readonly now?: () => Date;
}

/**
 * The Bucket the connection names, over binding, the Worker's r2_bucket
 * binding named connection.binding. signedUrl rejects when the connection
 * has no R2 credential.
 */
export function openR2Bucket(binding: R2BucketBinding, connection: BucketConnection, options: OpenR2BucketOptions = {}): Bucket {
  return new R2Bucket(binding, connection.name, connection.r2, options.now ?? (() => new Date()));
}

class R2Bucket implements Bucket {
  private secret: Promise<string> | undefined;

  constructor(
    private readonly binding: R2BucketBinding,
    readonly name: string,
    private readonly credential: R2Credential | undefined,
    private readonly now: () => Date
  ) {}

  async put(name: string, body: BucketBody, options: PutOptions = {}): Promise<BucketObject> {
    checkObjectName(name);
    // R2 takes a stream only when it knows its length, so a stream is read
    // whole first.
    const value = body instanceof ReadableStream ? await new Response(body).arrayBuffer() : body;
    const stored = await this.binding.put(name, value, options.contentType ? { httpMetadata: { contentType: options.contentType } } : undefined);
    if (stored === null) throw new Error(`bucket ${this.name}: R2 stored no object ${JSON.stringify(name)}`);
    return objectOf(stored, options.contentType);
  }

  async get(name: string): Promise<BucketRead> {
    checkObjectName(name);
    const object = await this.binding.get(name);
    if (object === null) throw new ObjectNotFoundError(this.name, name);
    return { object: objectOf(object), body: object.body };
  }

  async delete(name: string): Promise<void> {
    checkObjectName(name);
    // R2's delete answers the same whether or not the object was there.
    if ((await this.binding.head(name)) === null) throw new ObjectNotFoundError(this.name, name);
    await this.binding.delete(name);
  }

  async list(options: ListOptions = {}): Promise<ListPage> {
    const limit = options.pageSize ?? DEFAULT_PAGE_SIZE;
    if (!Number.isInteger(limit) || limit < 1 || limit > DEFAULT_PAGE_SIZE) {
      throw new Error(`a page holds ${limit} objects; want a whole number from 1 to ${DEFAULT_PAGE_SIZE}`);
    }
    const page = await this.binding.list({
      ...(options.prefix ? { prefix: options.prefix } : {}),
      ...(options.pageToken ? { cursor: options.pageToken } : {}),
      limit,
      include: ['httpMetadata'],
    });
    return {
      objects: page.objects.map(object => objectOf(object)),
      ...(page.truncated && page.cursor ? { nextPageToken: page.cursor } : {}),
    };
  }

  async signedUrl(name: string, options: SignedUrlOptions): Promise<string> {
    checkObjectName(name);
    checkSignedUrlOptions(options);
    const credential = this.credential;
    if (!credential) {
      throw new Error(`bucket ${this.name}: its connection has no R2 credential, which a signed URL needs (D64)`);
    }
    this.secret ??= r2SecretAccessKey(credential.token);
    const secret = await this.secret.catch(error => {
      this.secret = undefined;
      throw error;
    });
    return presignV4({
      method: options.method,
      host: r2Host(credential.account, credential.jurisdiction),
      path: `/${this.name}/${name}`,
      region: R2_REGION,
      accessKeyId: credential.accessKeyId,
      secretAccessKey: secret,
      date: this.now(),
      expiresInSeconds: options.expiresInSeconds,
      ...(options.contentType !== undefined ? { contentType: options.contentType } : {}),
    });
  }
}

function objectOf(object: R2ObjectLike, contentType?: string): BucketObject {
  const type = object.httpMetadata?.contentType ?? contentType;
  return {
    name: object.key,
    size: object.size,
    ...(type ? { contentType: type } : {}),
    updated: object.uploaded,
  };
}

const encoder = new TextEncoder();

function hex(bytes: ArrayBuffer): string {
  return Array.from(new Uint8Array(bytes), byte => byte.toString(16).padStart(2, '0')).join('');
}

async function sha256Hex(text: string): Promise<string> {
  return hex(await crypto.subtle.digest('SHA-256', encoder.encode(text)));
}

async function hmac(key: BufferSource, data: string): Promise<ArrayBuffer> {
  const imported = await crypto.subtle.importKey('raw', key, { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']);
  return crypto.subtle.sign('HMAC', imported, encoder.encode(data));
}

/** R2's S3 secret access key for an account token: the hex SHA-256 of its value. */
export function r2SecretAccessKey(token: string): Promise<string> {
  return sha256Hex(token);
}

/** S3's URI encoding: every byte but A-Z, a-z, 0-9, -, ., _ and ~ as %XX, and / too unless keepSlash. */
export function s3Encode(value: string, keepSlash = false): string {
  let out = '';
  for (const byte of encoder.encode(value)) {
    const c = String.fromCharCode(byte);
    if (/[A-Za-z0-9\-._~]/u.test(c) || (keepSlash && c === '/')) out += c;
    else out += `%${byte.toString(16).toUpperCase().padStart(2, '0')}`;
  }
  return out;
}

/** What presignV4 signs. */
export interface PresignV4Request {
  readonly method: 'GET' | 'PUT';
  /** The host the URL names, which the signature covers. */
  readonly host: string;
  /** The path, unencoded, beginning with /. */
  readonly path: string;
  readonly region: string;
  readonly accessKeyId: string;
  readonly secretAccessKey: string;
  /** When the signature is made. */
  readonly date: Date;
  readonly expiresInSeconds: number;
  /** The Content-Type a PUT must send, which the signature then covers. */
  readonly contentType?: string;
}

/**
 * A V4 presigned URL for S3's API (AWS Signature Version 4, query-string
 * authentication) with an unsigned payload: what R2's S3 API checks. Only
 * host, and content-type when given, are signed headers.
 */
export async function presignV4(request: PresignV4Request): Promise<string> {
  const stamp = request.date.toISOString().replace(/[-:]/gu, '').replace(/\.\d{3}/u, '');
  const day = stamp.slice(0, 8);
  const scope = `${day}/${request.region}/s3/aws4_request`;
  const headers: [string, string][] = [['host', request.host]];
  if (request.contentType !== undefined) headers.unshift(['content-type', request.contentType]);
  const signedHeaders = headers.map(([name]) => name).join(';');
  const query: [string, string][] = [
    ['X-Amz-Algorithm', 'AWS4-HMAC-SHA256'],
    ['X-Amz-Credential', `${request.accessKeyId}/${scope}`],
    ['X-Amz-Date', stamp],
    ['X-Amz-Expires', String(request.expiresInSeconds)],
    ['X-Amz-SignedHeaders', signedHeaders],
  ];
  const canonicalQuery = query
    .map(([name, value]) => [s3Encode(name), s3Encode(value)] as const)
    .sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
    .map(([name, value]) => `${name}=${value}`)
    .join('&');
  const canonicalPath = s3Encode(request.path, true);
  const canonicalRequest = [
    request.method,
    canonicalPath,
    canonicalQuery,
    headers.map(([name, value]) => `${name}:${value.trim()}\n`).join(''),
    signedHeaders,
    'UNSIGNED-PAYLOAD',
  ].join('\n');
  const stringToSign = ['AWS4-HMAC-SHA256', stamp, scope, await sha256Hex(canonicalRequest)].join('\n');
  let key: BufferSource = encoder.encode(`AWS4${request.secretAccessKey}`);
  for (const part of [day, request.region, 's3', 'aws4_request']) key = await hmac(key, part);
  const signature = hex(await hmac(key, stringToSign));
  return `https://${request.host}${canonicalPath}?${canonicalQuery}&X-Amz-Signature=${signature}`;
}
