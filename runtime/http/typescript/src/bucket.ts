import { readCapped } from './read-capped.js';
import { subtleSha256Hex } from './sha256.js';

/*
The provider-neutral interface of a bucket an API lists in its config's
buckets (D54, section 8.9 of docs/stack-model.md), the twin of the Go
runtime's bucket package: private object storage the API's implementation
puts, gets, heads, deletes and lists objects in, and signs URLs for, so
that a browser uploads or downloads an object directly. A put may write
only when no object has the name, and a Bucket service whose config says
contentAddressed gives Deps a ContentBucket, which names each object by
the SHA-256 of its bytes (D142). The generated Deps holds a
Bucket per listed bucket, and the generated entrypoint opens it with the
implementation its provider needs: GCS's, from this package's ./gcs entry,
which reaches the local target's emulator too. Nothing here imports a
provider's client, so the main entry depends on none.
*/

/** What a bucket holds of an object beside its bytes. */
export interface BucketObject {
  /** The object's name in the bucket: a slash-separated path, such as products/7b0e/image.png. */
  readonly name: string;
  /** Its length in bytes. */
  readonly size: number;
  /** Its media type, as put or the signed URL's PUT gave it. */
  readonly contentType?: string;
  /** When it was last written. */
  readonly updated?: Date;
}

/** What put writes: a stream, which put reads to its end, or the bytes or text whole. */
export type BucketBody = ReadableStream<Uint8Array> | Uint8Array | string;

/** What put writes beside the bytes. */
export interface PutOptions {
  /** The object's media type; absent lets the provider pick, application/octet-stream on GCS. */
  readonly contentType?: string;
  /**
   * Writes only when no object has the name (D142): the provider checks
   * and writes in one step, so of two puts of one name one creates the
   * object and the other finds it. The result's created says which.
   */
  readonly ifAbsent?: boolean;
}

/**
 * What put answers: the object the bucket holds under the name, and
 * whether this put wrote it. created is false only for a put with
 * ifAbsent that found an object there, which it left as it was.
 */
export interface PutResult extends BucketObject {
  readonly created: boolean;
}

/** An object get opened: what the bucket holds of it, and its bytes as a stream, which the caller reads or cancels. */
export interface BucketRead {
  readonly object: BucketObject;
  readonly body: ReadableStream<Uint8Array>;
}

/** What list selects. */
export interface ListOptions {
  /** Selects the objects whose names begin with it; absent selects every object. */
  readonly prefix?: string;
  /** A page's nextPageToken, to read the page after it; absent reads the first. */
  readonly pageToken?: string;
  /** Bounds the objects a page holds; DEFAULT_PAGE_SIZE when absent. */
  readonly pageSize?: number;
}

/** The size of a page whose ListOptions set none. */
export const DEFAULT_PAGE_SIZE = 1000;

/** One page of a list. */
export interface ListPage {
  /** The page's objects, in name order. */
  readonly objects: readonly BucketObject[];
  /** Reads the next page; absent on the last. */
  readonly nextPageToken?: string;
}

/** The methods a signed URL allows: GET downloads the object, PUT uploads it. */
export type SignedUrlMethod = 'GET' | 'PUT';

/** The longest a signed URL lives, in seconds: seven days, GCS's limit for a V4 signature. */
export const MAX_SIGNED_URL_SECONDS = 7 * 24 * 60 * 60;

/** What a signed URL allows. */
export interface SignedUrlOptions {
  readonly method: SignedUrlMethod;
  /** How long the URL lives, from now, in seconds: more than none, and at most MAX_SIGNED_URL_SECONDS. */
  readonly expiresInSeconds: number;
  /** For a PUT, the Content-Type the upload must send, which the signature covers; absent leaves it to the uploader. */
  readonly contentType?: string;
}

/**
 * One bucket. Object names are slash-separated paths; a bucket has no
 * directories, and list's prefix selects the names that begin with it.
 */
export interface Bucket {
  /** The bucket's name with its provider. */
  readonly name: string;
  /**
   * Writes the object name from body, replacing any object of that name,
   * or with ifAbsent leaving one that is there, and returns what the
   * bucket holds under the name. The object is whole once put resolves,
   * and absent if it rejects.
   */
  put(name: string, body: BucketBody, options?: PutOptions): Promise<PutResult>;
  /** Returns what the bucket holds of the object name, without its bytes, or undefined when there is no such object (D142). */
  head(name: string): Promise<BucketObject | undefined>;
  /** Opens the object name for reading. Rejects with ObjectNotFoundError when there is no such object. */
  get(name: string): Promise<BucketRead>;
  /** Removes the object name. Rejects with ObjectNotFoundError when there is no such object. */
  delete(name: string): Promise<void>;
  /** Returns a page of the objects whose names begin with the prefix, in name order. */
  list(options?: ListOptions): Promise<ListPage>;
  /** Returns a URL that lets whoever holds it, with no other credential, GET the object or PUT it until it expires. */
  signedUrl(name: string, options: SignedUrlOptions): Promise<string>;
}

/** What get and delete reject with when there is no such object. */
export class ObjectNotFoundError extends Error {
  readonly bucket: string;
  readonly object: string;

  constructor(bucket: string, object: string, options?: ErrorOptions) {
    super(`bucket ${bucket} has no object ${JSON.stringify(object)}`, options);
    this.name = 'ObjectNotFoundError';
    this.bucket = bucket;
    this.object = object;
  }
}

/** The largest object a content bucket puts when its Bucket service names none: 32 MiB. */
export const DEFAULT_CONTENT_MAX_BYTES = 32 * 1024 * 1024;

/** The prefix of a content bucket's object names, before the hash. */
export const CONTENT_PREFIX = 'sha256/';

/** What a content bucket's put answers. */
export interface ContentPut {
  /** The SHA-256 of the bytes, 64 lowercase hex digits: what names them from then on. */
  readonly hash: string;
  /** Their length in bytes. */
  readonly size: number;
  /** Whether this put wrote the object; false when the bucket already held these bytes. */
  readonly created: boolean;
}

/**
 * A bucket that names each object by its content (D142): the Deps member
 * of a Bucket service whose config says contentAddressed. An object is
 * `sha256/<hex>`, the SHA-256 of its bytes, so the same bytes are stored
 * once however often they are put, and an object never changes. It has no
 * delete; raw is the plain interface for the caller that needs one.
 */
export interface ContentBucket {
  /** The bucket's name with its provider. */
  readonly name: string;
  /** The largest object put accepts, in bytes. */
  readonly maxBytes: number;
  /**
   * Reads body whole, up to maxBytes, hashes it and writes it under its
   * hash if no object has that name. Rejects with ContentTooLargeError for
   * a longer body, having written nothing.
   */
  put(body: BucketBody, options?: { readonly contentType?: string }): Promise<ContentPut>;
  /** Whether the bucket holds the bytes of hash. */
  has(hash: string): Promise<boolean>;
  /** The hashes of hashes the bucket does not hold, each once, in the order given. */
  missing(hashes: readonly string[]): Promise<string[]>;
  /** Opens the bytes of hash for reading. Rejects with ObjectNotFoundError when the bucket does not hold them. */
  get(hash: string): Promise<BucketRead>;
  /** Returns a URL that lets whoever holds it GET the bytes of hash until it expires. It signs no upload: an object is written only by put, which hashes it. */
  signedUrl(hash: string, options: { readonly expiresInSeconds: number }): Promise<string>;
  /** The plain bucket, whose names are sha256/<hex>. */
  readonly raw: Bucket;
}

/** What a content bucket's put rejects a body longer than its maxBytes with. */
export class ContentTooLargeError extends Error {
  readonly bucket: string;
  readonly maxBytes: number;

  constructor(bucket: string, maxBytes: number) {
    super(`content bucket ${bucket} puts at most ${maxBytes} bytes, and the body is longer`);
    this.name = 'ContentTooLargeError';
    this.bucket = bucket;
    this.maxBytes = maxBytes;
  }
}

const CONTENT_HASH = /^[0-9a-f]{64}$/u;

/** The object name of a content hash. Throws for anything but 64 lowercase hex digits, so no caller's text reaches a name. */
export function contentObjectName(hash: string): string {
  if (typeof hash !== 'string' || !CONTENT_HASH.test(hash)) {
    throw new Error(`a content hash is 64 lowercase hex digits, the SHA-256 of the bytes, not ${JSON.stringify(hash)}`);
  }
  return CONTENT_PREFIX + hash;
}

/** Reads body whole, or rejects with ContentTooLargeError past maxBytes, cancelling a stream it stops reading. */
async function readWhole(body: BucketBody, bucket: string, maxBytes: number): Promise<Uint8Array> {
  if (typeof body === 'string') body = new TextEncoder().encode(body);
  if (body instanceof Uint8Array) {
    if (body.byteLength > maxBytes) throw new ContentTooLargeError(bucket, maxBytes);
    return body;
  }
  const whole = await readCapped(body, maxBytes);
  if (!whole) throw new ContentTooLargeError(bucket, maxBytes);
  return whole;
}

/** How many heads missing runs at once. */
const MISSING_CONCURRENCY = 16;

/**
 * The content bucket over bucket (D142), which the generated entrypoint
 * builds for a Bucket service whose config says contentAddressed, with
 * its maxBytes.
 */
export function contentBucket(bucket: Bucket, options: { readonly maxBytes?: number } = {}): ContentBucket {
  const maxBytes = options.maxBytes ?? DEFAULT_CONTENT_MAX_BYTES;
  if (!Number.isSafeInteger(maxBytes) || maxBytes < 1) {
    throw new Error(`a content bucket's maxBytes is a whole number of bytes above 0, not ${maxBytes}`);
  }
  const has = async (hash: string): Promise<boolean> => (await bucket.head(contentObjectName(hash))) !== undefined;
  return {
    name: bucket.name,
    maxBytes,
    raw: bucket,
    async put(body, put = {}) {
      const bytes = await readWhole(body, bucket.name, maxBytes);
      const hash = await subtleSha256Hex(bytes);
      const result = await bucket.put(CONTENT_PREFIX + hash, bytes, { contentType: put.contentType, ifAbsent: true });
      return { hash, size: bytes.byteLength, created: result.created };
    },
    has,
    async missing(hashes) {
      const asked = [...new Set(hashes)];
      for (const hash of asked) contentObjectName(hash);
      const held: boolean[] = new Array<boolean>(asked.length).fill(false);
      let next = 0;
      const worker = async (): Promise<void> => {
        for (let index = next++; index < asked.length; index = next++) held[index] = await has(asked[index]!);
      };
      await Promise.all(Array.from({ length: Math.min(MISSING_CONCURRENCY, asked.length) }, worker));
      return asked.filter((_, index) => !held[index]);
    },
    get: hash => bucket.get(contentObjectName(hash)),
    signedUrl: (hash, signed) => bucket.signedUrl(contentObjectName(hash), { method: 'GET', expiresInSeconds: signed.expiresInSeconds }),
  };
}

/** Throws for options no provider signs: another method, an expiry out of range, and a content type on a GET. */
export function checkSignedUrlOptions(options: SignedUrlOptions): void {
  if (options.method !== 'GET' && options.method !== 'PUT') {
    throw new Error(`a signed URL's method is ${JSON.stringify(options.method)}; want GET or PUT`);
  }
  const seconds = options.expiresInSeconds;
  if (!Number.isInteger(seconds) || seconds <= 0 || seconds > MAX_SIGNED_URL_SECONDS) {
    throw new Error(`a signed URL expires after ${seconds} seconds; want a whole number above 0 and at most ${MAX_SIGNED_URL_SECONDS}`);
  }
  if (options.method === 'GET' && options.contentType !== undefined) {
    throw new Error('a signed URL for a GET names a content type, which only an upload sends');
  }
}

/** Throws for an object name no provider stores: an empty one, one longer than 1024 bytes, or one with a carriage return, a line feed or a NUL. */
export function checkObjectName(name: string): void {
  if (name === '') throw new Error("an object's name is empty");
  const bytes = new TextEncoder().encode(name).length;
  if (bytes > 1024) throw new Error(`an object's name is ${bytes} bytes; at most 1024`);
  if (/[\r\n\0]/u.test(name)) throw new Error(`object name ${JSON.stringify(name)} holds a carriage return, a line feed or a NUL`);
}
