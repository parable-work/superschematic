/*
The provider-neutral interface of a bucket an API lists in its config's
buckets (D54, section 8.9 of docs/stack-model.md), the twin of the Go
runtime's bucket package: private object storage the API's implementation
puts, gets, deletes and lists objects in, and signs URLs for, so that a
browser uploads or downloads an object directly. The generated Deps holds a
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
  /** Writes the object name from body, replacing any object of that name, and returns what the provider stored. The object is whole once put resolves, and absent if it rejects. */
  put(name: string, body: BucketBody, options?: PutOptions): Promise<BucketObject>;
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
