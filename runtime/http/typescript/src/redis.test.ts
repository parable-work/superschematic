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
SUPERSCHEMATIC_TEST_MEMORY_STORE_TLS_CHAIN is a rediss:// URL of a server
that requires no password and whose certificate an intermediate CA signs,
which a root signs; the server sends its own certificate alone, and
SUPERSCHEMATIC_TEST_MEMORY_STORE_TLS_CHAIN_CA is the intermediate's PEM
file, which the store trusts as an anchor, as gcp's managed server CA is
given.
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
const CHAIN = testStore('SUPERSCHEMATIC_TEST_MEMORY_STORE_TLS_CHAIN');
const CHAIN_CA = process.env.SUPERSCHEMATIC_TEST_MEMORY_STORE_TLS_CHAIN_CA;

/** Short timings, so a test of an outage takes a second or two. */
const FAST: Partial<RedisMemoryStoreTimings> = { healthIntervalMs: 100, minBackoffMs: 20, maxBackoffMs: 200, receiveIdleMs: 200 };

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

async function until(check: () => boolean | Promise<boolean>, ms = 5_000): Promise<void> {
  const deadline = Date.now() + ms;
  while (!(await check())) {
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

/**
 * A TCP proxy to target that a test stops and starts, to stand for an
 * outage, or black-holes, to stand for a flow a network drops without
 * closing: every connection open then, and every connection accepted
 * while black-holed, drops what it carries from then on and never closes,
 * as a peer whose state is gone; resume forwards new connections again.
 */
function proxy(target: URL): { port: () => number; start: () => Promise<void>; stop: () => Promise<void>; blackhole: () => void; resume: () => void } {
  let server: Server | undefined;
  let port = 0;
  let holing = false;
  const sockets = new Set<Socket>();
  const pairs = new Set<{ dead: boolean }>();
  return {
    port: () => port,
    start: () =>
      new Promise(resolve => {
        server = createServer(client => {
          const upstream = createConnection({ host: target.hostname, port: Number(target.port || 6379) });
          sockets.add(client).add(upstream);
          const pair = { dead: holing };
          pairs.add(pair);
          client.on('data', chunk => void (pair.dead || upstream.write(chunk)));
          upstream.on('data', chunk => void (pair.dead || client.write(chunk)));
          const drop = () => {
            pairs.delete(pair);
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
    blackhole: () => {
      holing = true;
      for (const pair of pairs) pair.dead = true;
    },
    resume: () => {
      holing = false;
    },
  };
}

describe.skipIf(!PLAIN)('on a server (SUPERSCHEMATIC_TEST_MEMORY_STORE)', () => {
  const url = PLAIN!;
  const stores: MemoryStore[] = [];
  const open = async (store = 'shop-cache', member?: string, logger?: Logger) => {
    const s = await openRedisMemoryStore({ store, url, auth: 'none', ...(member ? { member } : {}) }, { timings: FAST });
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

  /** A store through p, with short pings, that the suite closes. */
  const through = async (p: ReturnType<typeof proxy>) => {
    const s = await openRedisMemoryStore(
      { store: 'shop-cache', url: `redis://127.0.0.1:${p.port()}${new URL(url).pathname}`, auth: 'none' },
      { timings: { ...FAST, pingMs: 300 } }
    );
    stores.push(s);
    return s;
  };
  const numsub = async (name: string) => {
    if (!admin.isOpen) await admin.connect();
    const reply = (await admin.sendCommand(['PUBSUB', 'NUMSUB', name])) as unknown[];
    return Number(reply[1]);
  };

  /** Rejects when p has not settled after ms. */
  const bounded = <T>(p: Promise<T>, ms: number) =>
    Promise.race([p, new Promise<never>((_, reject) => setTimeout(() => reject(new Error(`still pending after ${ms} ms`)), ms))]);

  test('replaces a half-open connection: available and heard again once the server is reachable', async () => {
    const p = proxy(new URL(url));
    await p.start();
    const store = await through(p);
    const publisher = await open();
    const ch = signalChannel({ declaration: 'stock-changed', name: `stock-changed:${unique()}` });
    let heard = 0;
    await ch.subscribe(store, () => void heard++);
    await until(() => {
      void ch.publish(publisher, signal);
      return heard >= 1;
    });

    p.blackhole();
    await until(() => !store.available());
    // The connections opened meanwhile die in their handshake.
    await Bun.sleep(500);
    // The flows are dead for good, but the server is reachable again on a
    // new connection.
    p.resume();
    await until(() => store.available());
    const ref = { declaration: 'views', name: `views:${unique()}` };
    expect(await store.increment(ref, 60_000)).toBe(1);
    // The subscriber's ping finds its connection dead, and the new one
    // subscribes again.
    const before = heard;
    await until(() => {
      void ch.publish(publisher, signal);
      return heard > before;
    });
    await p.stop();
  }, 30_000);

  test('close resolves within a bound on a half-open connection', async () => {
    const p = proxy(new URL(url));
    await p.start();
    const store = await through(p);
    await signalChannel({ declaration: 'x', name: `x:${unique()}` }).subscribe(store, () => undefined);
    await Bun.sleep(100);
    p.blackhole();
    // A health ping and a subscriber ping are left pending on the dead flow.
    await Bun.sleep(400);
    const t = Date.now();
    await bounded(store.close(), 2_000);
    expect(Date.now() - t).toBeLessThan(1_500);
    await p.stop();
  }, 30_000);

  test('a subscribe whose channel another subscription leaves while its SUBSCRIBE is in flight still hears', async () => {
    const store = await open();
    const publisher = await open();
    // The subscriber connection is up, so the next SUBSCRIBE goes out.
    const up = signalChannel({ declaration: 'x', name: `x:${unique()}` });
    let ready = false;
    await up.subscribe(store, () => void (ready = true));
    await until(() => {
      void up.publish(publisher, signal);
      return ready;
    });
    const ch = signalChannel({ declaration: 'stock-changed', name: `stock-changed:${unique()}` });
    let first = 0;
    const pending = ch.subscribe(store, () => void first++);
    const second = await ch.subscribe(store, () => undefined);
    await second.unsubscribe();
    await pending;
    await until(() => {
      void ch.publish(publisher, signal);
      return first >= 1;
    });
  }, 15_000);

  test('an unsubscribe while the subscriber connection is down leaves the channel once it returns', async () => {
    const p = proxy(new URL(url));
    await p.start();
    const store = await through(p);
    const publisher = await open();
    const kept = signalChannel({ declaration: 'kept', name: `kept:${unique()}` });
    let heard = 0;
    await kept.subscribe(store, () => void heard++);
    const ch = signalChannel({ declaration: 'stock-changed', name: `stock-changed:${unique()}` });
    const full = `shop-cache:${ch.ref.name}`;
    for (let round = 0; round < 3; round++) {
      const sub = await ch.subscribe(store, () => undefined);
      await until(async () => (await numsub(full)) === 1);
      await p.stop();
      await sub.unsubscribe();
      // A reconnect fails meanwhile, which drops what the client queued.
      await Bun.sleep(150);
      await p.start();
      const before = heard;
      await until(() => {
        void kept.publish(publisher, signal);
        return heard > before;
      });
      await until(async () => (await numsub(full)) === 0);
    }
    await p.stop();
  }, 30_000);
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
});

describe.skipIf(!(CHAIN && CHAIN_CA))('a CA that is an intermediate (SUPERSCHEMATIC_TEST_MEMORY_STORE_TLS_CHAIN)', () => {
  test('the store trusts it as an anchor', async () => {
    const ca = readFileSync(CHAIN_CA!, 'utf8');
    const store = await openRedisMemoryStore({ store: 'shop-cache', url: CHAIN!, ca, auth: 'none' }, { timings: FAST });
    try {
      expect(store.available()).toBe(true);
      const ref = { declaration: 'views', name: `views:${unique()}` };
      expect(await store.increment(ref, 60_000)).toBe(1);
    } finally {
      await store.close();
    }
  });

  test.skipIf(!TLS)('a server it does not sign never becomes available', async () => {
    const ca = readFileSync(CHAIN_CA!, 'utf8');
    const store = await openRedisMemoryStore({ store: 'shop-cache', url: TLS!, ca, auth: 'none' }, { timings: FAST });
    try {
      await Bun.sleep(500);
      expect(store.available()).toBe(false);
    } finally {
      await store.close();
    }
  });
});
