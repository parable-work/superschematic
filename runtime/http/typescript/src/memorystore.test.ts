import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import {
  Channel,
  InProcessMemoryStore,
  Key,
  MAX_MESSAGE_BYTES,
  MemoryRateLimitStore,
  MemoryStoreNameError,
  MemoryStoreRateLimitStore,
  MemoryStoreUnavailableError,
  disabledMemoryStore,
  jsonCodec,
  memoryStoreName,
  memoryStorePrefix,
  rateLimitKeyName,
  rateLimitWindow,
  signal,
  signalChannel,
  type MemoryStore,
  type RateLimitStore,
} from './index';

/*
The memory store parity vectors (D75), which ir writes (`cd ir && go test
-run TestWriteMemoryStoreParityVectors -update-memorystore-parity`) and the
Go, TypeScript and Rust runtimes each run: names, and the shared rate
limit's window and key names.
*/

const CORPUS = fileURLToPath(new URL('../../testdata/memorystore_parity.json', import.meta.url));

interface NameVector {
  readonly name: string;
  readonly declaration: string;
  readonly parts: readonly string[];
  readonly store?: string;
  readonly member?: string;
  readonly want: string | null;
  readonly fullName?: string;
}

interface WindowVector {
  readonly name: string;
  readonly limit: number;
  readonly windowMs: number;
  readonly nowMs: number;
  readonly prevCount: number;
  readonly currCount: number;
  readonly want: ReturnType<typeof rateLimitWindow>;
}

interface KeyVector {
  readonly name: string;
  readonly service: string;
  readonly operation: string;
  readonly windowStart: number;
  readonly clientKey: string;
  readonly want: string | null;
}

const corpus = JSON.parse(readFileSync(CORPUS, 'utf8')) as {
  readonly names: readonly NameVector[];
  readonly rateLimit: { readonly windows: readonly WindowVector[]; readonly keys: readonly KeyVector[] };
};

/** The name and the full name of v, or null where either is refused. */
function nameOf(v: NameVector): { name: string | null; fullName: string | null } {
  let name: string | null;
  try {
    name = memoryStoreName(v.declaration, ...v.parts);
  } catch (error) {
    expect(error).toBeInstanceOf(MemoryStoreNameError);
    return { name: null, fullName: null };
  }
  if (v.store === undefined) return { name, fullName: null };
  try {
    return { name, fullName: `${memoryStorePrefix(v.store, v.member)}:${name}` };
  } catch (error) {
    expect(error).toBeInstanceOf(MemoryStoreNameError);
    return { name: null, fullName: null };
  }
}

describe('memory store parity vectors', () => {
  test('the corpus has every section', () => {
    expect(corpus.names.length).toBeGreaterThan(0);
    expect(corpus.rateLimit.windows.length).toBeGreaterThan(0);
    expect(corpus.rateLimit.keys.length).toBeGreaterThan(0);
  });

  for (const v of corpus.names) {
    test(`names: ${v.name}`, () => {
      const got = nameOf(v);
      expect(got.name).toBe(v.want);
      if (v.fullName !== undefined) expect(got.fullName).toBe(v.fullName);
    });
  }

  for (const v of corpus.rateLimit.windows) {
    test(`rateLimit.windows: ${v.name}`, () => {
      expect(rateLimitWindow(v.limit, v.windowMs, v.nowMs, v.prevCount, v.currCount)).toEqual(v.want);
    });
  }

  for (const v of corpus.rateLimit.keys) {
    test(`rateLimit.keys: ${v.name}`, () => {
      let got: string | null;
      try {
        got = rateLimitKeyName(v.service, v.operation, v.windowStart, v.clientKey);
      } catch (error) {
        expect(error).toBeInstanceOf(MemoryStoreNameError);
        got = null;
      }
      expect(got).toBe(v.want);
    });
  }
});

/** A clock a test moves. */
function clock(start = 1_761_350_400_000): { now: () => number; advance: (ms: number) => void } {
  let t = start;
  return { now: () => t, advance: ms => void (t += ms) };
}

const ref = (name: string) => ({ declaration: name.split(':')[0]!, name });

describe('InProcessMemoryStore', () => {
  test('a counter expires its TTL after the write that creates it, and later writes keep it', async () => {
    const c = clock();
    const store = new InProcessMemoryStore('shop-cache', { now: c.now });
    const views = ref('views:p1');
    expect(await store.count(views)).toBe(0);
    expect(await store.increment(views, 1_000)).toBe(1);
    c.advance(600);
    expect(await store.increment(views, 1_000)).toBe(2);
    expect(store.ttlOf(views)).toBe(400);
    c.advance(400);
    expect(await store.count(views)).toBe(0);
    expect(await store.increment(views, 1_000)).toBe(1);
  });

  test('a claim is taken once until it expires or is deleted', async () => {
    const c = clock();
    const store = new InProcessMemoryStore('shop-cache', { now: c.now });
    const receipt = ref('receipt-sent:o1');
    expect(await store.claim(receipt, 600_000)).toBe(true);
    expect(await store.claim(receipt, 600_000)).toBe(false);
    await store.delete(receipt);
    expect(await store.claim(receipt, 600_000)).toBe(true);
    c.advance(600_000);
    expect(await store.claim(receipt, 600_000)).toBe(true);
  });

  test('a TTL outside 1 ms to seven days is refused', async () => {
    const store = new InProcessMemoryStore('shop-cache');
    await expect(store.increment(ref('views:p1'), 0)).rejects.toBeInstanceOf(RangeError);
    await expect(store.claim(ref('views:p1'), 8 * 24 * 3600 * 1000)).rejects.toBeInstanceOf(RangeError);
  });

  test('a message reaches every subscription of its channel and no other', async () => {
    const store = new InProcessMemoryStore('shop-cache');
    const got: string[] = [];
    const a = await store.subscribe(ref('stock-changed:p1'), data => void got.push(`a ${new TextDecoder().decode(data)}`));
    await store.subscribe(ref('stock-changed:p1'), data => void got.push(`b ${new TextDecoder().decode(data)}`));
    await store.subscribe(ref('stock-changed:p2'), () => void got.push('other'));
    await store.publish(ref('stock-changed:p1'), new TextEncoder().encode('{}'));
    await Bun.sleep(0);
    expect(got.sort()).toEqual(['a {}', 'b {}']);
    await a.unsubscribe();
    await store.publish(ref('stock-changed:p1'), new TextEncoder().encode('{}'));
    await Bun.sleep(0);
    expect(got.length).toBe(3);
  });

  test('a message past 64 KiB is refused before it is sent', async () => {
    const store = new InProcessMemoryStore('shop-cache');
    await expect(store.publish(ref('stock-changed:p1'), new Uint8Array(MAX_MESSAGE_BYTES + 1))).rejects.toBeInstanceOf(RangeError);
    await store.publish(ref('stock-changed:p1'), new Uint8Array(MAX_MESSAGE_BYTES));
  });

  test('while down every call is refused at once, and a subscription holds through it', async () => {
    const store = new InProcessMemoryStore('shop-cache');
    let heard = 0;
    await store.subscribe(ref('stock-changed:p1'), () => void heard++);
    store.setAvailable(false);
    expect(store.available()).toBe(false);
    for (const call of [
      () => store.publish(ref('stock-changed:p1'), new Uint8Array()),
      () => store.increment(ref('views:p1'), 1000),
      () => store.count(ref('views:p1')),
      () => store.claim(ref('receipt-sent:o1'), 1000),
      () => store.delete(ref('receipt-sent:o1')),
    ]) {
      await expect(call()).rejects.toBeInstanceOf(MemoryStoreUnavailableError);
    }
    store.setAvailable(true);
    await store.publish(ref('stock-changed:p1'), new Uint8Array());
    await Bun.sleep(0);
    expect(heard).toBe(1);
    await store.close();
    expect(store.available()).toBe(false);
    await expect(store.subscribe(ref('stock-changed:p1'), () => undefined)).rejects.toBeInstanceOf(MemoryStoreUnavailableError);
  });

  test('keys carry the store prefix, so two stores in one process do not meet', async () => {
    const a = new InProcessMemoryStore(memoryStorePrefix('shop-cache'));
    const b = new InProcessMemoryStore(memoryStorePrefix('shop-cache', 'pr-12'));
    expect(b.name).toBe('shop-cache@pr-12');
    await a.increment(ref('views:p1'), 1000);
    expect(await b.count(ref('views:p1'))).toBe(0);
  });
});

describe('disabledMemoryStore', () => {
  test('refuses every call and is never available', async () => {
    const store = disabledMemoryStore('shop-cache');
    expect(store.available()).toBe(false);
    await expect(store.publish(ref('a'), new Uint8Array())).rejects.toBeInstanceOf(MemoryStoreUnavailableError);
    await expect(store.subscribe(ref('a'), () => undefined)).rejects.toBeInstanceOf(MemoryStoreUnavailableError);
    await expect(store.increment(ref('a'), 1)).rejects.toBeInstanceOf(MemoryStoreUnavailableError);
    await expect(store.count(ref('a'))).rejects.toBeInstanceOf(MemoryStoreUnavailableError);
    await expect(store.claim(ref('a'), 1)).rejects.toBeInstanceOf(MemoryStoreUnavailableError);
    await expect(store.delete(ref('a'))).rejects.toBeInstanceOf(MemoryStoreUnavailableError);
    await store.close();
  });
});

describe('the typed layer', () => {
  test('a signal encodes as {} and any message decodes to it', async () => {
    const store = new InProcessMemoryStore('shop-cache');
    const ch = signalChannel(ref('stock-changed:p1'));
    const heard: unknown[] = [];
    await ch.subscribe(store, m => void heard.push(m));
    await ch.publish(store, signal);
    await store.publish(ch.ref, new TextEncoder().encode('{"from":"a later release"}'));
    await store.publish(ch.ref, new Uint8Array([0xff]));
    await Bun.sleep(0);
    expect(heard).toEqual([signal, signal, signal]);
  });

  test('a message that does not decode is dropped and counted', async () => {
    const store = new InProcessMemoryStore('shop-cache');
    const ch = new Channel(
      ref('stock-samples:p1'),
      jsonCodec(value => {
        if (typeof (value as { onHand?: unknown }).onHand !== 'number') throw new Error('no onHand');
        return value as { onHand: number };
      })
    );
    const heard: number[] = [];
    let failures = 0;
    await ch.subscribe(store, m => void heard.push(m.onHand), { onDecodeFailure: () => void failures++ });
    await ch.publish(store, { onHand: 3 });
    await store.publish(ch.ref, new TextEncoder().encode('not json'));
    await store.publish(ch.ref, new TextEncoder().encode('{"onHand":"3"}'));
    await Bun.sleep(0);
    expect(heard).toEqual([3]);
    expect(ch.decodeFailures).toBe(2);
    expect(failures).toBe(2);
  });

  test('a key counts and claims through the store with its TTL', async () => {
    const store = new InProcessMemoryStore('shop-cache');
    const key = new Key(ref('views:p1'), 5000);
    expect(await key.increment(store)).toBe(1);
    expect(await key.count(store)).toBe(1);
    await key.delete(store);
    expect(await key.claim(store)).toBe(true);
    expect(store.ttlOf(key.ref)).toBe(5000);
    expect(() => new Key(ref('views:p1'), 0)).toThrow(RangeError);
  });
});

/** A store whose calls fail as a connection that dropped would. */
function failing(base: MemoryStore): MemoryStore {
  return { ...base, available: () => true, count: () => Promise.reject(new Error('read ECONNRESET')) };
}

describe('MemoryStoreRateLimitStore', () => {
  const now = 1_761_350_430_000; // 30 s into a window

  test('every replica draws on one budget', async () => {
    const shared = new InProcessMemoryStore('shop-cache', { now: () => now });
    const replicas = [new MemoryStoreRateLimitStore(shared, 'shop-api'), new MemoryStoreRateLimitStore(shared, 'shop-api')];
    const decisions = [];
    for (let i = 0; i < 4; i++) decisions.push(await replicas[i % 2]!.take('getProduct:203.0.113.7', 3, now));
    expect(decisions.map(d => d.allowed)).toEqual([true, true, true, false]);
    expect(decisions[3]!.retryAfterSeconds).toBe(60);
    // Another client, and another operation, have budgets of their own.
    expect((await replicas[0]!.take('getProduct:203.0.113.8', 3, now)).allowed).toBe(true);
    expect((await replicas[0]!.take('listProducts:203.0.113.7', 3, now)).allowed).toBe(true);
    const name = rateLimitKeyName('shop-api', 'getProduct', 1_761_350_400, '203.0.113.7');
    expect(await shared.count({ declaration: 'rate-limit', name })).toBe(3);
    expect(shared.ttlOf({ declaration: 'rate-limit', name })).toBe(125_000);
  });

  test('the previous window weighs in by the part of it still in the window', async () => {
    const shared = new InProcessMemoryStore('shop-cache', { now: () => now });
    const prev = rateLimitKeyName('shop-api', 'getProduct', 1_761_350_340, '2001:db8::1');
    for (let i = 0; i < 30; i++) await shared.increment({ declaration: 'rate-limit', name: prev }, 125_000);
    const limiter = new MemoryStoreRateLimitStore(shared, 'shop-api');
    // 30 * 0.5 + 0 = 15, over a limit of 10.
    expect((await limiter.take('getProduct:2001:db8::1', 10, now)).allowed).toBe(false);
    expect((await limiter.take('getProduct:2001:db8::1', 16, now)).allowed).toBe(true);
  });

  test('while the store is unavailable, or a call fails, each replica limits in process', async () => {
    const calls: string[] = [];
    const fallback: RateLimitStore = {
      take: (key, limit, at) => {
        calls.push(key);
        return new MemoryRateLimitStore().take(key, limit, at);
      },
    };
    const errors: unknown[] = [];
    const down = new MemoryStoreRateLimitStore(disabledMemoryStore('shop-cache'), 'shop-api', { fallback });
    expect((await down.take('getProduct:a', 1, now)).allowed).toBe(true);
    const broken = new MemoryStoreRateLimitStore(failing(new InProcessMemoryStore('shop-cache')), 'shop-api', {
      fallback,
      onError: e => void errors.push(e),
    });
    expect((await broken.take('getProduct:b', 1, now)).allowed).toBe(true);
    expect(calls).toEqual(['getProduct:a', 'getProduct:b']);
    expect(errors.length).toBe(1);
  });

  test('an operation that is no segment falls back', async () => {
    const limiter = new MemoryStoreRateLimitStore(new InProcessMemoryStore('shop-cache'), 'shop-api', { onError: () => undefined });
    expect((await limiter.take('get*Product:a', 1, now)).allowed).toBe(true);
    expect((await limiter.take('get*Product:a', 1, now)).allowed).toBe(false);
  });
});
