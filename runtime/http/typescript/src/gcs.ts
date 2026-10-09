import { generateKeyPairSync } from 'node:crypto';
import { Readable } from 'node:stream';
import { pipeline } from 'node:stream/promises';
import { Storage } from '@google-cloud/storage';
import type { Bucket as GcsBucketHandle, File, FileMetadata } from '@google-cloud/storage';
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
import type { BucketConnection, StackEnv } from './stackconfig.js';

/*
A bucket on GCS (D54, section 8.9 of docs/stack-model.md): the
implementation of Bucket over @google-cloud/storage, which a generated
TypeScript server whose APIs list a bucket depends on. This is the
package's ./gcs entry, so the main entry never imports Google's client;
@google-cloud/storage is an optional peer dependency, as pg is for
./postgres.

A connection with an endpoint, or a process with STORAGE_EMULATOR_HOST
set, reaches an emulator that serves GCS's API in its place, such as the
local target's fake-gcs-server, with no credential. On GCS the client takes
the application default credentials: on Cloud Run, the workload's own
account.

A signed URL is a V4 one. On GCS, the client signs it as the workload's
account through IAM's signBlob, which needs the account to hold
roles/iam.serviceAccountTokenCreator on itself; the gcp target grants it.
Against an emulator, it signs a V4 URL for the emulator's host with an RSA
key the process generates once, which nothing checks: fake-gcs-server takes
a PUT at an object's path only when it carries a V4 signature's parameters,
and checks no signature. The Go entrypoint's buckets.go does the same.
*/

/** The account a signed URL for an emulator names: no account, since nothing checks the signature. */
export const EMULATOR_SIGNER = 'emulator@superschematic.local';

/** The project an emulator's client names; fake-gcs-server keeps one. */
export const EMULATOR_PROJECT = 'local';

export interface OpenBucketOptions {
  /** The environment STORAGE_EMULATOR_HOST is read from; process.env by default. */
  readonly env?: StackEnv;
}

function processEnv(): StackEnv {
  return (globalThis as { process?: { env?: StackEnv } }).process?.env ?? {};
}

/** The endpoint of the emulator a connection reaches: its endpoint, or STORAGE_EMULATOR_HOST's, with http:// when it names no scheme; undefined for GCS itself. */
export function emulatorEndpoint(connection: BucketConnection, env: StackEnv = processEnv()): string | undefined {
  const endpoint = connection.endpoint ?? env.STORAGE_EMULATOR_HOST ?? '';
  if (endpoint === '') return undefined;
  return (endpoint.includes('://') ? endpoint : `http://${endpoint}`).replace(/\/+$/u, '');
}

/** The process's clients, by endpoint: GCS's under '', and each emulator's. */
const clients = new Map<string, Storage>();

/** The key the process signs an emulator's URLs with, made when first needed. */
let emulatorKey: string | undefined;

function storageFor(endpoint: string | undefined): Storage {
  const key = endpoint ?? '';
  let client = clients.get(key);
  if (!client) {
    if (endpoint === undefined) {
      client = new Storage();
    } else {
      emulatorKey ??= generateKeyPairSync('rsa', { modulusLength: 2048 }).privateKey.export({ type: 'pkcs8', format: 'pem' }) as string;
      client = new Storage({
        apiEndpoint: endpoint,
        projectId: EMULATOR_PROJECT,
        credentials: { client_email: EMULATOR_SIGNER, private_key: emulatorKey },
      });
    }
    clients.set(key, client);
  }
  return client;
}

/**
 * Opens the bucket a connection names, the value of a bucket field a
 * stack derives (loadBucket), on GCS or on the emulator its endpoint names.
 * Opening makes no request; the first operation does.
 */
export function openBucket(connection: BucketConnection, options: OpenBucketOptions = {}): Bucket {
  const endpoint = emulatorEndpoint(connection, options.env);
  return new GcsBucket(connection.name, storageFor(endpoint).bucket(connection.name));
}

function isNotFound(error: unknown): boolean {
  return (error as { code?: unknown } | null)?.code === 404;
}

function objectOf(metadata: FileMetadata): BucketObject {
  return {
    name: metadata.name ?? '',
    size: Number(metadata.size ?? 0),
    ...(metadata.contentType ? { contentType: metadata.contentType } : {}),
    ...(metadata.updated ? { updated: new Date(metadata.updated) } : {}),
  };
}

function readableOf(body: BucketBody): Readable {
  if (typeof body === 'string' || body instanceof Uint8Array) return Readable.from([Buffer.from(body)]);
  return Readable.fromWeb(body as Parameters<typeof Readable.fromWeb>[0]);
}

/** A bucket on GCS, or on an emulator that serves its API. */
class GcsBucket implements Bucket {
  readonly name: string;
  readonly #handle: GcsBucketHandle;

  constructor(name: string, handle: GcsBucketHandle) {
    this.name = name;
    this.#handle = handle;
  }

  #file(name: string): File {
    checkObjectName(name);
    return this.#handle.file(name);
  }

  async put(name: string, body: BucketBody, options: PutOptions = {}): Promise<BucketObject> {
    const file = this.#file(name);
    const contentType = options.contentType;
    // One request, a multipart upload, as the Go client sends an object
    // smaller than its chunk: a large object goes to the bucket from the
    // browser, through a signed URL, rather than through the server.
    await pipeline(readableOf(body), file.createWriteStream({ resumable: false, ...(contentType ? { contentType, metadata: { contentType } } : {}) }));
    return objectOf(file.metadata);
  }

  async get(name: string): Promise<BucketRead> {
    const file = this.#file(name);
    let metadata: FileMetadata;
    try {
      [metadata] = await file.getMetadata();
    } catch (error) {
      if (isNotFound(error)) throw new ObjectNotFoundError(this.name, name, { cause: error });
      throw error;
    }
    // The read takes the generation the metadata named, so a write between
    // the two mixes no two objects.
    const read = metadata.generation !== undefined ? this.#handle.file(name, { generation: String(metadata.generation) }) : file;
    return { object: objectOf(metadata), body: Readable.toWeb(read.createReadStream()) as unknown as ReadableStream<Uint8Array> };
  }

  async delete(name: string): Promise<void> {
    try {
      await this.#file(name).delete();
    } catch (error) {
      if (isNotFound(error)) throw new ObjectNotFoundError(this.name, name, { cause: error });
      throw error;
    }
  }

  async list(options: ListOptions = {}): Promise<ListPage> {
    const pageSize = options.pageSize ?? DEFAULT_PAGE_SIZE;
    if (!Number.isInteger(pageSize) || pageSize <= 0) throw new Error(`a page holds ${pageSize} objects; want a whole number above 0`);
    const [files, next] = await this.#handle.getFiles({
      autoPaginate: false,
      maxResults: pageSize,
      ...(options.prefix ? { prefix: options.prefix } : {}),
      ...(options.pageToken ? { pageToken: options.pageToken } : {}),
    });
    const token = (next as { pageToken?: string } | null | undefined)?.pageToken;
    return { objects: files.map(file => objectOf(file.metadata)), ...(token ? { nextPageToken: token } : {}) };
  }

  async signedUrl(name: string, options: SignedUrlOptions): Promise<string> {
    checkSignedUrlOptions(options);
    const [url] = await this.#file(name).getSignedUrl({
      version: 'v4',
      action: options.method === 'GET' ? 'read' : 'write',
      expires: Date.now() + options.expiresInSeconds * 1000,
      ...(options.contentType !== undefined ? { contentType: options.contentType } : {}),
    });
    return url;
  }
}
