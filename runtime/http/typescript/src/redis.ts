/*
The memory store on Redis or Valkey (D75), through node-redis's client
(@redis/client), which a server's package depends on only when an API it
serves lists a store. Bun's built-in RedisClient takes a password only in
its URL and reuses it on every reconnect, so it cannot send a fresh access
token on each new connection, which IAM authentication needs.

openRedisMemoryStore never fails for a server that is down: it pings once
within a second and returns, and the store reconnects in the background
with jittered backoff from 100 ms to 5 s, while a health ping every 2 s
keeps available() true to the connection. While the store is unavailable,
every call throws MemoryStoreUnavailableError at once (the client queues
nothing while offline). A call takes at most 500 ms, and one retry covers a
stale connection. The client sends no CLIENT command (no name, no
SETINFO) and asks for no maintenance notifications, which a managed
server refuses. A rediss:// connection verifies the server against the
connection's CA, over TLS 1.2 or later, taking the CA as a trust anchor
even when it is an intermediate rather than a root. With auth iam, each new connection
authenticates as user default with an OAuth access token of the
workload's service account, which the metadata server gives
($GCE_METADATA_HOST overrides its host) and which is fetched again before
it expires. The store holds one subscriber connection, opened with the
first subscription. No log line holds the URL, a token or a name's parts.
*/

import { createClient, ErrorReply } from '@redis/client';
import { isIP } from 'node:net';
import { createSecureContext } from 'node:tls';
import type { Logger } from './logger.js';
import {
  MemoryStoreUnavailableError,
  checkMessage,
  checkTtl,
  memoryStorePrefix,
  type MemoryStore,
  type MessageHandler,
  type Ref,
  type Subscription,
} from './memorystore.js';
import type { MemoryStoreConnection } from './stackconfig.js';

/** The durations that bound the store's calls. Tests shorten them. */
export interface RedisMemoryStoreTimings {
  /** One connection attempt. */
  readonly connectMs: number;
  /** One command, its retry included in neither. */
  readonly opMs: number;
  /** One ping: at open, and each health ping. */
  readonly pingMs: number;
  /** Between health pings while connected. */
  readonly healthIntervalMs: number;
  /** The first delay after a failure, and the cap the doubling delays reach. */
  readonly minBackoffMs: number;
  readonly maxBackoffMs: number;
}

/** The contract's timings: a 1 s connect and ping, 500 ms calls, a 2 s health ping, and backoff from 100 ms to 5 s. */
export const DEFAULT_REDIS_TIMINGS: RedisMemoryStoreTimings = {
  connectMs: 1_000,
  opMs: 500,
  pingMs: 1_000,
  healthIntervalMs: 2_000,
  minBackoffMs: 100,
  maxBackoffMs: 5_000,
};

/** An OAuth access token and when it expires, in Unix milliseconds. */
export interface AccessToken {
  readonly token: string;
  readonly expiresAt: number;
}

/** What openRedisMemoryStore takes beside the connection. */
export interface RedisMemoryStoreOptions {
  /** Where the store logs its connection's changes; nothing by default. */
  readonly logger?: Logger;
  readonly timings?: Partial<RedisMemoryStoreTimings>;
  /** The source of access tokens for auth iam; the metadata server's (metadataTokenSource) by default. */
  readonly tokenSource?: () => Promise<AccessToken>;
}

/** The connection is one no store can be opened on: a CA that holds no certificate, say. */
export class MemoryStoreConfigError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'MemoryStoreConfigError';
  }
}

/** The metadata server's path of the default service account's access token. */
export const METADATA_TOKEN_PATH = '/computeMetadata/v1/instance/service-accounts/default/token';

/** What metadataTokenSource takes. */
export interface MetadataTokenSourceOptions {
  /** The metadata server's host[:port]: $GCE_METADATA_HOST, else metadata.google.internal. */
  readonly host?: string;
  readonly fetch?: typeof fetch;
  /** How long one request may take; 2 s by default. */
  readonly timeoutMs?: number;
}

/**
 * The access tokens of the workload's service account, from the metadata
 * server: `GET http://<host>/computeMetadata/v1/instance/service-accounts/default/token`
 * with `Metadata-Flavor: Google`.
 */
export function metadataTokenSource(options: MetadataTokenSourceOptions = {}): () => Promise<AccessToken> {
  const host = options.host ?? (process.env.GCE_METADATA_HOST || 'metadata.google.internal');
  const doFetch = options.fetch ?? fetch;
  const timeoutMs = options.timeoutMs ?? 2_000;
  return async () => {
    let response: Response;
    try {
      response = await doFetch(`http://${host}${METADATA_TOKEN_PATH}`, {
        headers: { 'Metadata-Flavor': 'Google' },
        signal: AbortSignal.timeout(timeoutMs),
      });
    } catch (error) {
      throw new Error(`the metadata server gave no access token: ${error instanceof Error ? error.message : String(error)}`);
    }
    if (!response.ok) throw new Error(`the metadata server answered ${response.status} for an access token`);
    const body = (await response.json()) as { access_token?: unknown; expires_in?: unknown };
    if (typeof body.access_token !== 'string' || body.access_token === '') throw new Error('the metadata server answered no access_token');
    const seconds = typeof body.expires_in === 'number' && body.expires_in > 0 ? body.expires_in : 300;
    return { token: body.access_token, expiresAt: Date.now() + seconds * 1000 };
  };
}

/** How long before a token expires the store fetches another for a new connection. */
const TOKEN_REFRESH_MARGIN_MS = 5 * 60 * 1000;

/** d spread by up to 20% either way, so replicas do not retry in step. */
function jitter(d: number): number {
  const spread = d / 5;
  return Math.max(1, Math.round(d - spread + Math.random() * 2 * spread));
}

/** Rejects with a timeout after ms, or settles as p does. */
function within<T>(p: Promise<T>, ms: number, what: string): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeout = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new Error(`${what} took more than ${ms} ms`)), ms);
  });
  return Promise.race([p, timeout]).finally(() => clearTimeout(timer));
}

const noLogger: Logger = {
  debug: () => undefined,
  info: () => undefined,
  warn: () => undefined,
  error: () => undefined,
  child: () => noLogger,
};

/** What the store uses of node-redis's client. */
type Client = ReturnType<typeof createClient>;

/**
 * Opens the memory store connection names. It refuses only a connection it
 * cannot use (MemoryStoreConfigError): a server that is down gives a store
 * that is unavailable and reconnects in the background.
 */
export async function openRedisMemoryStore(connection: MemoryStoreConnection, options: RedisMemoryStoreOptions = {}): Promise<MemoryStore> {
  const store = new RedisMemoryStore(connection, options);
  await store.start();
  return store;
}

class RedisMemoryStore implements MemoryStore {
  readonly name: string;
  private readonly timings: RedisMemoryStoreTimings;
  private readonly logger: Logger;
  private readonly client: Client;
  private readonly clientOptions: Parameters<typeof createClient>[0];
  private readonly secrets: string[] = [];
  private up = false;
  private closed = false;
  private healthTimer: ReturnType<typeof setTimeout> | undefined;
  private backoff: number;
  private subscriber: Client | undefined;
  private readonly channels = new Map<string, { declaration: string; handlers: Set<MessageHandler>; listener: (message: Buffer) => void }>();

  constructor(
    private readonly connection: MemoryStoreConnection,
    options: RedisMemoryStoreOptions
  ) {
    try {
      this.name = memoryStorePrefix(connection.store, connection.member);
    } catch (error) {
      throw new MemoryStoreConfigError(`memory store connection: ${error instanceof Error ? error.message : String(error)}`);
    }
    this.timings = { ...DEFAULT_REDIS_TIMINGS, ...options.timings };
    this.backoff = this.timings.minBackoffMs;
    this.secrets.push(connection.url);
    this.logger = (options.logger ?? noLogger).child({ memoryStore: this.name });
    this.clientOptions = this.options(options);
    this.client = createClient(this.clientOptions);
    this.watchClient(this.client, 'store');
  }

  /** The node-redis options of the connection, which every connection of the store shares. */
  private options(options: RedisMemoryStoreOptions): Parameters<typeof createClient>[0] {
    const c = this.connection;
    let url: URL;
    try {
      url = new URL(c.url);
    } catch {
      throw new MemoryStoreConfigError('memory store connection: the URL does not parse');
    }
    if (url.username !== '' || url.password !== '') throw new MemoryStoreConfigError('memory store connection: the URL holds a user or a password');
    const tls = url.protocol === 'rediss:';
    if (!tls && url.protocol !== 'redis:') throw new MemoryStoreConfigError(`memory store connection: the URL's scheme is ${url.protocol}, not redis: or rediss:`);
    if (c.ca !== undefined && !tls) throw new MemoryStoreConfigError('memory store connection: a CA is set for a redis:// URL; a CA verifies a rediss:// server');
    if (c.auth === 'iam' && !tls) throw new MemoryStoreConfigError('memory store connection: auth iam needs a rediss:// URL; an access token travels only over TLS');
    if (c.auth !== 'iam' && c.auth !== 'none') throw new MemoryStoreConfigError('memory store connection: auth is neither iam nor none');
    if (c.ca !== undefined) {
      try {
        createSecureContext({ ca: c.ca });
      } catch {
        throw new MemoryStoreConfigError('memory store connection: the CA holds no PEM certificate');
      }
    }
    const host = url.hostname.replace(/^\[(.*)\]$/u, '$1');
    const port = url.port === '' ? 6379 : Number(url.port);
    const db = url.pathname.replace(/^\//u, '');
    const database = db === '' ? 0 : Number(db);
    if (!Number.isInteger(database) || database < 0) throw new MemoryStoreConfigError('memory store connection: the URL names no database number');
    const { connectMs, maxBackoffMs, minBackoffMs } = this.timings;
    const reconnectStrategy = (retries: number): number => jitter(Math.min(maxBackoffMs, minBackoffMs * 2 ** Math.min(retries, 16)));
    const socket = tls
      ? {
          host,
          port,
          tls: true as const,
          // The CA may be the first certificate of a managed server's CA
          // chain rather than a self-signed root (gcp gives
          // managedServerCas[0].caCerts[0].certificates[0]): it is trusted
          // as an anchor, as Go's and rustls's verifiers trust it.
          ...(c.ca !== undefined ? { ca: c.ca, allowPartialTrustChain: true } : {}),
          ...(isIP(host) === 0 ? { servername: host } : {}),
          minVersion: 'TLSv1.2' as const,
          connectTimeout: connectMs,
          reconnectStrategy,
        }
      : { host, port, connectTimeout: connectMs, reconnectStrategy };
    let credentialsProvider: NonNullable<Parameters<typeof createClient>[0]>['credentialsProvider'];
    if (c.auth === 'iam') {
      const source = options.tokenSource ?? metadataTokenSource();
      let current: AccessToken | undefined;
      credentialsProvider = {
        type: 'async-credentials-provider',
        credentials: async () => {
          // Each new connection authenticates; a token near its expiry is
          // fetched again first.
          if (current === undefined || current.expiresAt - Date.now() < TOKEN_REFRESH_MARGIN_MS) {
            current = await source();
            this.secrets.push(current.token);
            if (this.secrets.length > 8) this.secrets.splice(1, 1);
          }
          return { username: 'default', password: current.token };
        },
      };
    }
    return {
      socket,
      database,
      // A managed server refuses CLIENT: no name, no SETINFO, and no
      // maintenance notifications.
      disableClientInfo: true,
      maintNotifications: 'disabled',
      // Offline, a call fails at once rather than wait in a queue.
      disableOfflineQueue: true,
      ...(credentialsProvider ? { credentialsProvider } : {}),
    };
  }

  /** Logs a client's errors, which node-redis emits on every failed reconnect, by the store's state alone. */
  private watchClient(client: Client, role: 'store' | 'subscriber'): void {
    client.on('error', (error: unknown) => {
      if (role === 'store') this.markDown(error);
      else this.logger.debug('memory store subscriber connection failed; reconnecting', { error: this.scrub(error) });
    });
    if (role === 'store') {
      client.on('ready', () => {
        if (!this.closed) this.setUp(true);
      });
    }
  }

  /** Connects, pings once within pingMs, and starts the health ping. */
  async start(): Promise<void> {
    const connecting = this.client.connect().then(
      () => this.client.ping(),
      () => undefined
    );
    let up = false;
    try {
      await within(connecting, this.timings.pingMs, 'the first ping');
      up = this.client.isReady;
    } catch {
      up = false;
    }
    this.setUp(up);
    if (!up) this.logger.warn('memory store unavailable at open; reconnecting in the background');
    this.schedule(up ? this.timings.healthIntervalMs : jitter(this.backoff));
  }

  available(): boolean {
    return this.up && !this.closed;
  }

  async publish(ref: Ref, data: Uint8Array): Promise<void> {
    checkMessage(data);
    await this.call(client => client.sendCommand(['PUBLISH', this.full(ref), Buffer.from(data)]));
  }

  async subscribe(ref: Ref, handler: MessageHandler): Promise<Subscription> {
    if (this.closed) throw new MemoryStoreUnavailableError(this.name, 'closed');
    const name = this.full(ref);
    let channel = this.channels.get(name);
    if (!channel) {
      const handlers = new Set<MessageHandler>();
      channel = {
        declaration: ref.declaration,
        handlers,
        listener: (message: Buffer) => this.dispatch(ref.declaration, handlers, message),
      };
      this.channels.set(name, channel);
      await this.subscribeChannel(name, channel.listener);
    }
    channel.handlers.add(handler);
    const held = channel;
    return {
      unsubscribe: async () => {
        if (!held.handlers.delete(handler) || held.handlers.size > 0 || this.channels.get(name) !== held) return;
        this.channels.delete(name);
        const subscriber = this.subscriber;
        if (subscriber?.isReady) {
          await within(subscriber.unsubscribe(name, held.listener, true), this.timings.opMs, 'UNSUBSCRIBE').catch(() => undefined);
        }
      },
    };
  }

  async increment(ref: Ref, ttlMs: number): Promise<number> {
    checkTtl(ttlMs);
    const name = this.full(ref);
    const replies = await this.call(client => client.multi().incr(name).pExpire(name, ttlMs, 'NX').exec());
    return Number(replies[0]);
  }

  async count(ref: Ref): Promise<number> {
    const reply = await this.call(client => client.get(this.full(ref)));
    return reply === null ? 0 : Number(reply);
  }

  async claim(ref: Ref, ttlMs: number): Promise<boolean> {
    checkTtl(ttlMs);
    const name = this.full(ref);
    const reply = await this.call(client => client.sendCommand(['SET', name, '1', 'PX', String(ttlMs), 'NX']));
    return reply !== null;
  }

  async delete(ref: Ref): Promise<void> {
    await this.call(client => client.del(this.full(ref)));
  }

  async close(): Promise<void> {
    if (this.closed) return;
    this.closed = true;
    this.up = false;
    clearTimeout(this.healthTimer);
    this.channels.clear();
    await Promise.allSettled([this.client, this.subscriber].filter((c): c is Client => c !== undefined).map(c => (c.isOpen ? c.close() : Promise.resolve())));
    for (const c of [this.client, this.subscriber]) {
      try {
        c?.destroy();
      } catch {
        // Already closed.
      }
    }
  }

  private full(ref: Ref): string {
    return `${this.name}:${ref.name}`;
  }

  /** Runs a command, once more on a connection failure, refusing at once while the store is unavailable. */
  private async call<T>(run: (client: Client) => Promise<T>): Promise<T> {
    if (this.closed) throw new MemoryStoreUnavailableError(this.name, 'closed');
    if (!this.up) throw new MemoryStoreUnavailableError(this.name, 'not connected');
    let last: unknown;
    for (let attempt = 0; attempt < 2; attempt++) {
      try {
        return await within(run(this.client), this.timings.opMs, 'a memory store call');
      } catch (error) {
        // A reply that is an error is the server's answer, not the
        // connection's failure.
        if (error instanceof ErrorReply) throw error;
        last = error;
        if (this.closed || !this.client.isReady) break;
      }
    }
    this.markDown(last);
    throw new MemoryStoreUnavailableError(this.name, 'not connected', { cause: new Error(this.scrub(last)) });
  }

  /** Opens the subscriber connection on first use, and subscribes name on it. */
  private async subscribeChannel(name: string, listener: (message: Buffer) => void): Promise<void> {
    if (!this.subscriber) {
      const subscriber = this.client.duplicate();
      this.subscriber = subscriber;
      this.watchClient(subscriber, 'subscriber');
      subscriber.on('ready', () => {
        // node-redis subscribes again to what it holds; a channel it was
        // offline for is subscribed here.
        for (const [channel, held] of this.channels) {
          subscriber.subscribe(channel, held.listener, true).catch(() => undefined);
        }
      });
      subscriber.connect().catch(() => undefined);
    }
    const subscriber = this.subscriber;
    if (!subscriber.isReady) return;
    try {
      await within(subscriber.subscribe(name, listener, true), this.timings.opMs, 'SUBSCRIBE');
    } catch (error) {
      this.logger.debug('memory store subscribe deferred until the subscriber connection returns', { error: this.scrub(error) });
    }
  }

  private dispatch(declaration: string, handlers: Set<MessageHandler>, message: Buffer): void {
    const data = new Uint8Array(message.buffer, message.byteOffset, message.byteLength);
    for (const handler of [...handlers]) {
      try {
        Promise.resolve(handler(data.slice())).catch(error => this.logger.error('memory store handler failed; the message is dropped', { declaration, error: this.scrub(error) }));
      } catch (error) {
        this.logger.error('memory store handler failed; the message is dropped', { declaration, error: this.scrub(error) });
      }
    }
  }

  /** The next health ping, after ms. */
  private schedule(ms: number): void {
    if (this.closed) return;
    clearTimeout(this.healthTimer);
    this.healthTimer = setTimeout(() => void this.health(), ms);
    (this.healthTimer as { unref?: () => void }).unref?.();
  }

  private async health(): Promise<void> {
    if (this.closed) return;
    let error: unknown;
    try {
      if (!this.client.isReady) throw new Error('the connection is not ready');
      await within(this.client.ping(), this.timings.pingMs, 'the health ping');
    } catch (e) {
      error = e ?? new Error('the health ping failed');
    }
    if (this.closed) return;
    if (error === undefined) {
      this.setUp(true);
      this.backoff = this.timings.minBackoffMs;
      this.schedule(this.timings.healthIntervalMs);
    } else {
      this.markDown(error);
      const wait = jitter(this.backoff);
      this.backoff = Math.min(2 * this.backoff, this.timings.maxBackoffMs);
      this.schedule(wait);
    }
  }

  /** Records the connection's state and logs each change. */
  private setUp(up: boolean, cause?: unknown): void {
    const was = this.up;
    this.up = up;
    if (up && !was) this.logger.info('memory store connected');
    if (!up && was) this.logger.warn('memory store unavailable', cause === undefined ? {} : { error: this.scrub(cause) });
  }

  /** A failed call or connection: the store is down until a ping answers, which runs at once. */
  private markDown(cause: unknown): void {
    if (!this.up || this.closed) return;
    this.setUp(false, cause);
    this.backoff = this.timings.minBackoffMs;
    this.schedule(jitter(this.backoff));
  }

  /** error's message without the URL or a token. */
  private scrub(error: unknown): string {
    let message = error instanceof Error ? error.message : String(error);
    for (const secret of this.secrets) {
      if (secret !== '') message = message.split(secret).join('[redacted]');
    }
    return message;
  }
}
