import { afterAll, describe, expect, test } from 'bun:test';
import { createClient } from '@redis/client';
import { readFileSync } from 'node:fs';
import { createConnection, createServer, type Server, type Socket } from 'node:net';
import { MemoryStoreUnavailableError, signal, signalChannel, Key, type Logger, type LogFields, type MemoryStore } from './index';
import { MemoryStoreConfigError, metadataTokenSource, openRedisMemoryStore, type RedisMemoryStoreTimings } from './redis';

/*
The Redis implementation of the memory store (D75). The tests that need a
server read SUPERSCHEMATIC_TEST_MEMORY_STORE, a redis:// URL of a loopback
host whose database is not 0, so a test never touches a running stack
dev's keys; unset, they skip. The TLS and IAM tests read
SUPERSCHEMATIC_TEST_MEMORY_STORE_TLS, a rediss:// URL of a loopback server
that requires a password, its CA's PEM file at
SUPERSCHEMATIC_TEST_MEMORY_STORE_CA, and its password at
SUPERSCHEMATIC_TEST_MEMORY_STORE_PASSWORD, which the test changes and
restores. A fake metadata server gives the access tokens.
*/

/** Why url may not be a test's server, or undefined: a loopback host, and a database other than 0. */
function testStoreProblem(url: string): string | undefined {
  let u: URL;
  try {
    u = new URL(url);
  } catch {
    return 'it does not parse';
  }
  if (u.protocol !== 'redis:' && u.protocol !== 'rediss:') return 'it is no redis:// or rediss:// URL';
  const host = u.hostname.replace(/^\[(.*)\]$/u, '$1');
  if (!['127.0.0.1', 'localhost', '::1'].includes(host)) return `its host ${host} is not loopback`;
  const db = u.pathname.replace(/^\//u, '');
  if (db === '' || db === '0' || !/^[0-9]+$/u.test(db)) return 'it names database 0, which a running stack dev uses; name another';
  return undefined;
}

function testStore(variable: string): string | undefined {
  const url = process.env[variable];
  if (!url) return undefined;
  const problem = testStoreProblem(url);
  if (problem) throw new Error(`${variable}: ${problem}`);
  return url;
}

const PLAIN = testStore('SUPERSCHEMATIC_TEST_MEMORY_STORE');
const TLS = testStore('SUPERSCHEMATIC_TEST_MEMORY_STORE_TLS');
const TLS_CA = process.env.SUPERSCHEMATIC_TEST_MEMORY_STORE_CA;
const TLS_PASSWORD = process.env.SUPERSCHEMATIC_TEST_MEMORY_STORE_PASSWORD;

/** Short timings, so a test of an outage takes a second or two. */
const FAST: Partial<RedisMemoryStoreTimings> = { healthIntervalMs: 100, minBackoffMs: 20, maxBackoffMs: 200 };

/** A logger that keeps every line, to check what a line holds. */
function recording(): Logger & { lines: { level: string; msg: string; fields: LogFields }[] } {
  const lines: { level: string; msg: string; fields: LogFields }[] = [];
  const make = (base: LogFields): Logger => ({
    debug: (msg, fields) => void lines.push({ level: 'debug', msg, fields: { ...base, ...fields } }),
    info: (msg, fields) => void lines.push({ level: 'info', msg, fields: { ...base, ...fields } }),
    warn: (msg, fields) => void lines.push({ level: 'warn', msg, fields: { ...base, ...fields } }),
    error: (msg, fields) => void lines.push({ level: 'error', msg, fields: { ...base, ...fields } }),
    child: fields => make({ ...base, ...fields }),
  });
  return Object.assign(make({}), { lines });
}

async function until(check: () => boolean, ms = 5_000): Promise<void> {
  const deadline = Date.now() + ms;
  while (!check()) {
    if (Date.now() > deadline) throw new Error('timed out');
    await Bun.sleep(20);
  }
}

const unique = () => `t${Date.now().toString(36)}${Math.random().toString(36).slice(2, 8)}`;

describe('the test server guard', () => {
  test('a test server is loopback and not database 0', () => {
    expect(testStoreProblem('redis://127.0.0.1:6379/1')).toBeUndefined();
    expect(testStoreProblem('redis://[::1]:6379/3')).toBeUndefined();
    expect(testStoreProblem('redis://127.0.0.1:6379/0')).toContain('database 0');
    expect(testStoreProblem('redis://127.0.0.1:6379')).toContain('database 0');
    expect(testStoreProblem('redis://10.1.2.3:6379/1')).toContain('not loopback');
    expect(testStoreProblem('http://127.0.0.1/1')).toContain('no redis');
  });
});

describe('openRedisMemoryStore', () => {
  test('refuses a connection it cannot use', async () => {
    const base = { store: 'shop-cache', url: 'rediss://127.0.0.1:1', auth: 'none' as const };
    await expect(openRedisMemoryStore({ ...base, ca: 'not a certificate' })).rejects.toBeInstanceOf(MemoryStoreConfigError);
    await expect(openRedisMemoryStore({ ...base, url: 'redis://127.0.0.1:1', auth: 'iam' })).rejects.toBeInstanceOf(MemoryStoreConfigError);
    await expect(openRedisMemoryStore({ ...base, store: 'shop:cache' })).rejects.toBeInstanceOf(MemoryStoreConfigError);
    await expect(openRedisMemoryStore({ ...base, url: 'redis://user:secret@127.0.0.1:1' })).rejects.toThrow(/user or a password/);
  });

  test('opens a store on a server that is down within a second, which refuses every call at once', async () => {
    const logger = recording();
    const started = Date.now();
    const url = 'redis://127.0.0.1:1/1';
    const store = await openRedisMemoryStore({ store: 'shop-cache', url, auth: 'none' }, { logger, timings: FAST });
    try {
      expect(Date.now() - started).toBeLessThan(1_500);
      expect(store.available()).toBe(false);
      const t = Date.now();
      await expect(store.count({ declaration: 'views', name: 'views:p1' })).rejects.toBeInstanceOf(MemoryStoreUnavailableError);
      await expect(store.publish({ declaration: 'a', name: 'a' }, new Uint8Array())).rejects.toBeInstanceOf(MemoryStoreUnavailableError);
      expect(Date.now() - t).toBeLessThan(50);
      expect(logger.lines.some(l => l.msg === 'memory store unavailable at open; reconnecting in the background')).toBe(true);
      expect(JSON.stringify(logger.lines)).not.toContain(url);
    } finally {
      await store.close();
    }
    await expect(store.count({ declaration: 'views', name: 'views:p1' })).rejects.toBeInstanceOf(MemoryStoreUnavailableError);
  });
});

describe('metadataTokenSource', () => {
  test('asks the metadata server for the default account token, with Metadata-Flavor', async () => {
    const seen: string[] = [];
    const server = Bun.serve({
      port: 0,
      fetch: request => {
        seen.push(`${new URL(request.url).pathname} ${request.headers.get('metadata-flavor')}`);
        if (request.headers.get('metadata-flavor') !== 'Google') return new Response('no', { status: 403 });
        return Response.json({ access_token: 'tok-1', expires_in: 3599, token_type: 'Bearer' });
      },
    });
    try {
      const previous = process.env.GCE_METADATA_HOST;
      process.env.GCE_METADATA_HOST = `127.0.0.1:${server.port}`;
      try {
        const token = await metadataTokenSource()();
        expect(token.token).toBe('tok-1');
        expect(token.expiresAt).toBeGreaterThan(Date.now() + 3_500_000);
      } finally {
        if (previous === undefined) delete process.env.GCE_METADATA_HOST;
        else process.env.GCE_METADATA_HOST = previous;
      }
      expect(seen).toEqual(['/computeMetadata/v1/instance/service-accounts/default/token Google']);
      await expect(metadataTokenSource({ host: '127.0.0.1:1', timeoutMs: 200 })()).rejects.toThrow(/no access token/);
    } finally {
      server.stop(true);
    }
  });
});

/** A TCP proxy to target that a test stops and starts, to stand for an outage. */
function proxy(target: URL): { port: () => number; start: () => Promise<void>; stop: () => Promise<void> } {
  let server: Server | undefined;
  let port = 0;
  const sockets = new Set<Socket>();
  return {
    port: () => port,
    start: () =>
      new Promise(resolve => {
        server = createServer(client => {
          const upstream = createConnection({ host: target.hostname, port: Number(target.port || 6379) });
          sockets.add(client).add(upstream);
          client.pipe(upstream).pipe(client);
          const drop = () => {
            client.destroy();
            upstream.destroy();
          };
          client.on('error', drop).on('close', drop);
          upstream.on('error', drop).on('close', drop);
        });
        server.listen(port, '127.0.0.1', () => {
          port = (server!.address() as { port: number }).port;
          resolve();
        });
      }),
    stop: () =>
      new Promise(resolve => {
        for (const s of sockets) s.destroy();
        sockets.clear();
        server?.close(() => resolve());
      }),
  };
}

describe.skipIf(!PLAIN)('on a server (SUPERSCHEMATIC_TEST_MEMORY_STORE)', () => {
  const url = PLAIN!;
  const stores: MemoryStore[] = [];
  const open = async (store = 'shop-cache', member?: string, logger?: Logger) => {
    const s = await openRedisMemoryStore({ store, url, auth: 'none', ...(member ? { member } : {}) }, { timings: FAST, ...(logger ? { logger } : {}) });
    stores.push(s);
    return s;
  };
  const admin = createClient({ url, disableClientInfo: true });
  afterAll(async () => {
    await Promise.allSettled(stores.map(s => s.close()));
    if (admin.isOpen) await admin.close();
  });

  test('counts with a TTL set by the write that creates the key, claims once, and deletes', async () => {
    if (!admin.isOpen) await admin.connect();
    const store = await open();
    expect(store.available()).toBe(true);
    const name = `views:${unique()}`;
    const ref = { declaration: 'views', name };
    expect(await store.count(ref)).toBe(0);
    expect(await store.increment(ref, 60_000)).toBe(1);
    await admin.pExpire(`shop-cache:${name}`, 30_000);
    expect(await store.increment(ref, 60_000)).toBe(2);
    const pttl = await admin.pTTL(`shop-cache:${name}`);
    expect(pttl).toBeGreaterThan(29_000);
    expect(pttl).toBeLessThanOrEqual(30_000);
    expect(await store.count(ref)).toBe(2);
    const claim = { declaration: 'receipt-sent', name: `receipt-sent:${unique()}` };
    expect(await store.claim(claim, 600_000)).toBe(true);
    expect(await store.claim(claim, 600_000)).toBe(false);
    expect(await admin.pTTL(`shop-cache:${claim.name}`)).toBeGreaterThan(599_000);
    await store.delete(claim);
    expect(await store.claim(claim, 600_000)).toBe(true);
  });

  test('a message reaches the subscriptions of every replica, and none past 64 KiB is sent', async () => {
    const [a, b] = [await open(), await open()];
    const ch = signalChannel({ declaration: 'stock-changed', name: `stock-changed:${unique()}` });
    let heardA = 0;
    let heardB = 0;
    const subA = await ch.subscribe(a, () => void heardA++);
    await ch.subscribe(b, () => void heardB++);
    await ch.subscribe(b, () => void heardB++);
    await Bun.sleep(100);
    await ch.publish(a, signal);
    await until(() => heardA === 1 && heardB === 2);
    await subA.unsubscribe();
    await ch.publish(b, signal);
    await until(() => heardB === 4);
    expect(heardA).toBe(1);
    await expect(a.publish(ch.ref, new Uint8Array(64 * 1024 + 1))).rejects.toBeInstanceOf(RangeError);
  });

  test('the store prefix keeps a member and another store apart', async () => {
    if (!admin.isOpen) await admin.connect();
    const name = `views:${unique()}`;
    const ref = { declaration: 'views', name };
    await (await open('shop-cache', 'pr-12')).increment(ref, 60_000);
    expect(await (await open('shop-cache')).count(ref)).toBe(0);
    expect(await admin.get(`shop-cache@pr-12:${name}`)).toBe('1');
  });

  test('sends no CLIENT command', async () => {
    if (!admin.isOpen) await admin.connect();
    const calls = async () => {
      const info = await admin.info('commandstats');
      return Number(/cmdstat_client:calls=(\d+)/u.exec(info)?.[1] ?? 0);
    };
    const before = await calls();
    const store = await open();
    const ch = signalChannel({ declaration: 'x', name: `x:${unique()}` });
    await ch.subscribe(store, () => undefined);
    await new Key({ declaration: 'k', name: `k:${unique()}` }, 1000).increment(store);
    await Bun.sleep(200);
    expect(await calls()).toBe(before);
  });

  test('goes unavailable with the server, refuses calls at once, and reconnects and resubscribes when it returns', async () => {
    const p = proxy(new URL(url));
    await p.start();
    const logger = recording();
    const db = new URL(url).pathname;
    const store = await openRedisMemoryStore({ store: 'shop-cache', url: `redis://127.0.0.1:${p.port()}${db}`, auth: 'none' }, { timings: FAST, logger });
    stores.push(store);
    const publisher = await open();
    expect(store.available()).toBe(true);
    const ch = signalChannel({ declaration: 'stock-changed', name: `stock-changed:${unique()}` });
    let heard = 0;
    await ch.subscribe(store, () => void heard++);
    await Bun.sleep(100);
    await ch.publish(publisher, signal);
    await until(() => heard === 1);

    await p.stop();
    await until(() => !store.available());
    const t = Date.now();
    await expect(store.count({ declaration: 'v', name: 'v:1' })).rejects.toBeInstanceOf(MemoryStoreUnavailableError);
    expect(Date.now() - t).toBeLessThan(50);

    await p.start();
    await until(() => store.available());
    // The subscriber connection subscribes again once it is back.
    await until(() => {
      void ch.publish(publisher, signal);
      return heard >= 2;
    });
    expect(logger.lines.map(l => l.msg)).toContain('memory store unavailable');
    expect(logger.lines.map(l => l.msg)).toContain('memory store connected');
    await p.stop();
  });
});

describe.skipIf(!(TLS && TLS_CA && TLS_PASSWORD))('over TLS with IAM tokens (SUPERSCHEMATIC_TEST_MEMORY_STORE_TLS)', () => {
  const url = TLS!;
  const ca = TLS_CA ? readFileSync(TLS_CA, 'utf8') : '';
  const password = TLS_PASSWORD!;
  const adminOf = (pass: string) =>
    createClient({ url, password: pass, disableClientInfo: true, socket: { tls: true, ca, host: new URL(url).hostname, port: Number(new URL(url).port) } });

  test('authenticates each new connection with a fresh token from the metadata server, and verifies the server', async () => {
    let token = 'tok-1';
    let asked = 0;
    const metadata = Bun.serve({
      port: 0,
      fetch: request => {
        if (request.headers.get('metadata-flavor') !== 'Google') return new Response('no', { status: 403 });
        asked++;
        // Within the refresh margin, so each new connection fetches again.
        return Response.json({ access_token: token, expires_in: 60, token_type: 'Bearer' });
      },
    });
    const admin = adminOf(password);
    await admin.connect();
    await admin.configSet('requirepass', 'tok-1');
    const logger = recording();
    const store = await openRedisMemoryStore(
      { store: 'shop-cache', url, ca, auth: 'iam' },
      { logger, timings: FAST, tokenSource: metadataTokenSource({ host: `127.0.0.1:${metadata.port}` }) }
    );
    try {
      expect(store.available()).toBe(true);
      const ref = { declaration: 'views', name: `views:${unique()}` };
      expect(await store.increment(ref, 60_000)).toBe(1);
      const before = asked;
      expect(before).toBeGreaterThan(0);

      // The password turns and every connection drops: the next connection
      // fetches the new token, since the one it holds is near its expiry.
      token = 'tok-2';
      await admin.configSet('requirepass', 'tok-2');
      await admin.sendCommand(['CLIENT', 'KILL', 'TYPE', 'normal', 'SKIPME', 'yes']);
      await until(() => asked > before && store.available());
      expect(await store.count(ref)).toBe(1);
      const text = JSON.stringify(logger.lines);
      for (const secret of ['tok-1', 'tok-2', url]) expect(text).not.toContain(secret);
    } finally {
      await store.close();
      await admin.configSet('requirepass', password).catch(() => undefined);
      await admin.close();
      metadata.stop(true);
    }
  });

  test('a server the CA does not sign never becomes available', async () => {
    const otherCa = readFileSync(TLS_CA!.replace(/ca\.pem$/u, 'server.pem'), 'utf8');
    const store = await openRedisMemoryStore({ store: 'shop-cache', url, ca: otherCa, auth: 'iam' }, { timings: FAST, tokenSource: async () => ({ token: password, expiresAt: Date.now() + 3_600_000 }) });
    try {
      await Bun.sleep(500);
      expect(store.available()).toBe(false);
    } finally {
      await store.close();
    }
  });
});
