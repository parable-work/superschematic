/*
The memory store on Redis or Valkey (D75), through node-redis's client
(@redis/client), which a server's package depends on only when an API it
serves lists a store. Bun's built-in RedisClient takes a password only in
its URL and reuses it on every reconnect, so it cannot send a fresh access
token on each new connection, which IAM authentication needs.

openRedisMemoryStore never fails for a server that is down: it pings once
within a second and returns, and the store reconnects in the background
with jittered backoff from 100 ms to 5 s, while a health ping every 2 s
keeps available() true to the connection. A connection that stays open
but answers no ping, as a flow a network drops without closing, is
destroyed and a new one opened, rather than held until the kernel gives
up on it. While the store is unavailable,
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
first subscription, which pings after 15 s with nothing heard; one that
does not answer is replaced, and the new one subscribes to every channel
the store holds. close() gives the commands in flight 500 ms, then
destroys the connections. No log line holds the URL, a token or a name's
parts.
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
  /** How long the subscriber connection hears nothing before it pings. */
  readonly receiveIdleMs: number;
}

/** The contract's timings: a 1 s connect and ping, 500 ms calls, a 2 s health ping, backoff from 100 ms to 5 s, and a subscriber ping after 15 s idle. */
export const DEFAULT_REDIS_TIMINGS: RedisMemoryStoreTimings = {
  connectMs: 1_000,
  opMs: 500,
  pingMs: 1_000,
  healthIntervalMs: 2_000,
  minBackoffMs: 100,
  maxBackoffMs: 5_000,
  receiveIdleMs: 15_000,
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

/**
 * Rejects with a timeout after ms, or settles as p does. node-redis keeps a
 * command it gave up on pending until its connection answers or is
 * destroyed: the health and subscriber pings destroy one that does not.
 */
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

/** Destroys a client the store no longer uses, which may already be closed. */
function discard(client: Client): void {
  try {
    client.destroy();
  } catch {
    // Already closed.
  }
}

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
  /** The command connection, which the health ping replaces when it is half-open. */
  private client: Client;
  private readonly clientOptions: Parameters<typeof createClient>[0];
  private readonly secrets: string[] = [];
  private up = false;
  private closed = false;
  private healthTimer: ReturnType<typeof setTimeout> | undefined;
  private backoff: number;
  /** When each client's socket connected, until its handshake ends: a handshake no reply ends is stuck. */
  private readonly handshakes = new WeakMap<Client, number>();
  private subscriber: Client | undefined;
  private subscriberTimer: ReturnType<typeof setTimeout> | undefined;
  /** When the subscriber connection last heard a message or a ping's reply. */
  private heardAt = 0;
  /** What to subscribe to: each channel's handlers, by full name. The subscriber connection follows it. */
  private readonly channels = new Map<string, { declaration: string; handlers: Set<MessageHandler> }>();
  /** The one listener node-redis holds for every channel, so it holds at most one per channel. */
  private readonly listener = (message: Buffer, channel: Buffer): void => this.dispatch(channel.toString(), message);

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
    this.client = this.newClient('store');
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

  /**
   * A client of the connection, not yet connected, whose events change the
   * store's state while it is the store's command or subscriber client. Its
   * errors, which node-redis emits on every failed reconnect, are logged by
   * the store's state alone.
   */
  private newClient(role: 'store' | 'subscriber'): Client {
    const client = createClient(this.clientOptions);
    const current = () => !this.closed && client === (role === 'store' ? this.client : this.subscriber);
    client.on('connect', () => this.handshakes.set(client, Date.now()));
    client.on('error', (error: unknown) => {
      this.handshakes.delete(client);
      if (!current()) return;
      if (role === 'store') this.markDown(error);
      else this.logger.debug('memory store subscriber connection failed; reconnecting', { error: this.scrub(error) });
    });
    client.on('ready', () => {
      this.handshakes.delete(client);
      if (!current()) return;
      if (role === 'store') this.setUp(true);
      else this.resubscribe(client);
    });
    return client;
  }

  /** Whether client's socket connected and its handshake has had no reply for longer than a connect and a ping. */
  private stuck(client: Client): boolean {
    const since = this.handshakes.get(client);
    return since !== undefined && Date.now() - since > this.timings.connectMs + this.timings.pingMs;
  }

  /** Replaces the command connection with a new one, which connects in the background. */
  private replaceClient(): void {
    const old = this.client;
    this.client = this.newClient('store');
    discard(old);
    this.client.connect().catch(() => undefined);
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
    const first = channel === undefined;
    if (!channel) {
      channel = { declaration: ref.declaration, handlers: new Set() };
      this.channels.set(name, channel);
    }
    // The handler is held before the SUBSCRIBE goes out, so an unsubscribe
    // of another handler meanwhile leaves the channel to it.
    channel.handlers.add(handler);
    const held = channel;
    if (first) await this.subscribeChannel(name);
    return {
      unsubscribe: async () => {
        if (!held.handlers.delete(handler) || held.handlers.size > 0 || this.channels.get(name) !== held) return;
        this.channels.delete(name);
        await this.unsubscribeChannel(name);
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
    // A token of this call's own, which SET ... NX GET answers when the
    // claim is already held: a retry of a claim the first attempt took
    // reads its own token back, and still holds the claim.
    const token = crypto.randomUUID();
    const reply = await this.call(client => client.sendCommand(['SET', name, token, 'PX', String(ttlMs), 'NX', 'GET']));
    return reply === null || String(reply) === token;
  }

  async delete(ref: Ref): Promise<void> {
    await this.call(client => client.del(this.full(ref)));
  }

  async close(): Promise<void> {
    if (this.closed) return;
    this.closed = true;
    this.up = false;
    clearTimeout(this.healthTimer);
    clearTimeout(this.subscriberTimer);
    this.channels.clear();
    const clients = [this.client, this.subscriber].filter((c): c is Client => c !== undefined);
    // A client's close waits for every command in flight, which a
    // half-open connection never answers: they have opMs, then the
    // connections are destroyed.
    const closing = Promise.allSettled(clients.map(c => (c.isOpen ? c.close() : Promise.resolve())));
    await within(closing, this.timings.opMs, 'closing the memory store').catch(() => undefined);
    for (const c of clients) discard(c);
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

  /**
   * Subscribes to name on the subscriber connection, which opens on first
   * use. A connection that is not ready subscribes to every channel once it
   * is (resubscribe). The channel stays in the map whatever SUBSCRIBE
   * answers: a SUBSCRIBE with no reply in time replaces the connection,
   * whose successor subscribes to it.
   */
  private async subscribeChannel(name: string): Promise<void> {
    if (!this.subscriber) {
      this.openSubscriber();
      return;
    }
    const subscriber = this.subscriber;
    if (!subscriber.isReady) return;
    try {
      await within(subscriber.subscribe(name, this.listener, true), this.timings.opMs, 'SUBSCRIBE');
    } catch (error) {
      if (error instanceof ErrorReply) {
        this.logger.warn('memory store server refused SUBSCRIBE', { error: this.scrub(error) });
        return;
      }
      this.logger.debug('memory store subscribe failed; replacing the subscriber connection', { error: this.scrub(error) });
      if (!this.closed && subscriber === this.subscriber && subscriber.isReady && this.channels.has(name)) this.replaceSubscriber();
    }
  }

  /**
   * Unsubscribes from name. The command goes to node-redis whether or not
   * the connection is up: node-redis drops its listener only once the
   * server confirms, and resubscribes to what it holds on a reconnect. A
   * failed reconnect drops the command it queued, so resubscribe leaves
   * every channel the map no longer holds.
   */
  private async unsubscribeChannel(name: string): Promise<void> {
    const subscriber = this.subscriber;
    if (!subscriber) return;
    const done = subscriber.unsubscribe(name, this.listener, true).catch(() => undefined);
    if (subscriber.isReady) await within(done, this.timings.opMs, 'UNSUBSCRIBE').catch(() => undefined);
  }

  /** Opens a subscriber connection, which subscribes to every channel once it is ready, and starts its idle ping. */
  private openSubscriber(): void {
    const subscriber = this.newClient('subscriber');
    this.subscriber = subscriber;
    this.heardAt = Date.now();
    subscriber.connect().catch(() => undefined);
    this.scheduleSubscriberPing();
  }

  /** Replaces the subscriber connection with a new one. */
  private replaceSubscriber(): void {
    const old = this.subscriber;
    this.openSubscriber();
    if (old) discard(old);
  }

  /**
   * Makes what a ready subscriber connection holds the map's channels:
   * node-redis has subscribed again to what it held, which may lack a
   * channel subscribed while it was down, or hold one whose UNSUBSCRIBE a
   * failed reconnect dropped.
   */
  private resubscribe(subscriber: Client): void {
    const held = subscriber.getPubSubListeners('CHANNELS');
    for (const name of [...held.keys()]) {
      if (!this.channels.has(name)) subscriber.unsubscribe(name, this.listener, true).catch(() => undefined);
    }
    for (const name of this.channels.keys()) {
      // A channel node-redis marks as unsubscribing, whose UNSUBSCRIBE is
      // queued or was dropped, is subscribed again after it.
      if (held.get(name)?.unsubscribing !== false) {
        subscriber.subscribe(name, this.listener, true).catch(error => this.logger.debug('memory store resubscribe failed', { error: this.scrub(error) }));
      }
    }
  }

  /** The subscriber connection's next idle check: by default, receiveIdleMs after it last heard. */
  private scheduleSubscriberPing(wait = Math.max(1, this.heardAt + this.timings.receiveIdleMs - Date.now())): void {
    clearTimeout(this.subscriberTimer);
    if (this.closed || !this.subscriber) return;
    this.subscriberTimer = setTimeout(() => void this.pingSubscriber(), wait);
    (this.subscriberTimer as { unref?: () => void }).unref?.();
  }

  /**
   * Pings a subscriber connection that has heard nothing for receiveIdleMs,
   * and replaces one that claims to be ready and does not answer, or whose
   * handshake is stuck. node-redis would hold it until the kernel gives up
   * on the socket, and every message published meanwhile would be lost.
   */
  private async pingSubscriber(): Promise<void> {
    const subscriber = this.subscriber;
    if (this.closed || !subscriber) return;
    if (Date.now() - this.heardAt >= this.timings.receiveIdleMs) {
      let dead = false;
      if (subscriber.isReady) {
        try {
          await within(subscriber.ping(), this.timings.pingMs, 'the subscriber ping');
          this.heardAt = Date.now();
        } catch (error) {
          // A connection that failed is reconnecting already.
          dead = subscriber.isReady;
          this.logger.debug('memory store subscriber ping failed', { error: this.scrub(error) });
        }
      } else {
        dead = this.stuck(subscriber);
      }
      if (this.closed || subscriber !== this.subscriber) return;
      if (dead) {
        this.replaceSubscriber();
        return;
      }
      // One that is reconnecting is checked again at the health interval.
      if (!subscriber.isReady) {
        this.scheduleSubscriberPing(this.timings.healthIntervalMs);
        return;
      }
    }
    this.scheduleSubscriberPing();
  }

  private dispatch(name: string, message: Buffer): void {
    this.heardAt = Date.now();
    const channel = this.channels.get(name);
    if (!channel) return;
    const { declaration, handlers } = channel;
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
    const client = this.client;
    try {
      if (!client.isReady) {
        if (this.stuck(client)) this.replaceClient();
        throw new Error('the connection is not ready');
      }
      await within(client.ping(), this.timings.pingMs, 'the health ping');
    } catch (e) {
      error = e ?? new Error('the health ping failed');
      // A connection that still claims to be ready and gave no reply is
      // half-open: node-redis would hold it, and the ping pending on it,
      // until the kernel gives up on the socket. A new one replaces it.
      if (!this.closed && client === this.client && client.isReady) this.replaceClient();
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
