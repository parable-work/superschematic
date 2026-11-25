import { afterEach, describe, expect, test } from 'bun:test';
import {
  parseQueueWakes,
  QUEUE_WAKES_VARIABLE,
  queueWaker,
  setProcessQueueWaker,
  wakeAfterEnqueue,
  WAKE_INTERVAL_MS,
  type QueueWake,
  type QueueWakeHost,
} from './queue-wake';

/** A platform's wake and its token endpoint: it records each request, and a test moves its clock. */
function fakeHost(options: { wakeStatus?: number; tokenStatus?: number; expiresIn?: number } = {}) {
  const requests: { url: string; method: string; headers: Record<string, string>; body?: string }[] = [];
  const warnings: { message: string; fields: Readonly<Record<string, unknown>> }[] = [];
  const state = { now: 1_000_000, tokens: 0 };
  const host: QueueWakeHost = {
    fetch: async (url, init) => {
      requests.push({ url, method: init.method, headers: init.headers, ...(init.body === undefined ? {} : { body: init.body }) });
      if (url.endsWith('/token')) {
        state.tokens++;
        const status = options.tokenStatus ?? 200;
        return { ok: status === 200, status, json: async () => ({ access_token: `t-${state.tokens}`, expires_in: options.expiresIn ?? 3600 }) };
      }
      if (url.includes('unreachable')) throw new Error('connection refused');
      const status = options.wakeStatus ?? 200;
      return { ok: status >= 200 && status < 300, status, json: async () => ({}) };
    },
    now: () => state.now,
    warn: (message, fields) => warnings.push({ message, fields }),
  };
  const wakes = () => requests.filter(r => r.method === 'POST');
  return { host, requests, warnings, state, wakes };
}

const token = { url: 'http://metadata.test/token', headers: { 'Metadata-Flavor': 'Fake' } };
const wake = (job: string): QueueWake => ({ url: `https://run.test/jobs/${job}:run`, token });

describe('queueWaker', () => {
  test("an enqueue POSTs its queue's wake with the token its token endpoint gives, and at most once per queue in five seconds", async () => {
    const f = fakeHost();
    const waker = queueWaker(new Map([['OrderPlaced', [wake('fulfil')]], ['StockLow', [wake('reorder')]]]), f.host);
    await Promise.all(Array.from({ length: 20 }, () => waker.enqueued('OrderPlaced')));
    await waker.enqueued('Unwatched');
    expect(f.wakes().map(r => r.url)).toEqual(['https://run.test/jobs/fulfil:run']);
    expect(f.wakes()[0]).toMatchObject({ body: '{}', headers: { authorization: 'Bearer t-1', 'content-type': 'application/json' } });
    expect(f.requests[0]).toMatchObject({ url: token.url, method: 'GET', headers: { 'Metadata-Flavor': 'Fake' } });

    // Another queue's wake goes out beside it.
    await waker.enqueued('StockLow');
    expect(f.wakes().map(r => r.url)).toEqual(['https://run.test/jobs/fulfil:run', 'https://run.test/jobs/reorder:run']);

    // Just under the interval nothing is sent; at the interval the queue is woken again, with the token held.
    f.state.now += WAKE_INTERVAL_MS - 1;
    await waker.enqueued('OrderPlaced');
    expect(f.wakes()).toHaveLength(2);
    f.state.now += 1;
    await waker.enqueued('OrderPlaced');
    expect(f.wakes().map(r => r.url)).toEqual([
      'https://run.test/jobs/fulfil:run',
      'https://run.test/jobs/reorder:run',
      'https://run.test/jobs/fulfil:run',
    ]);
    expect(f.state.tokens).toBe(1);
    expect(WAKE_INTERVAL_MS).toBe(5_000);
    expect(f.warnings).toEqual([]);
  });

  test('a token is asked for again once it is a minute from its end', async () => {
    const f = fakeHost({ expiresIn: 600 });
    const waker = queueWaker(new Map([['OrderPlaced', [wake('fulfil')]]]), f.host);
    await waker.enqueued('OrderPlaced');
    f.state.now += 539_000;
    await waker.enqueued('OrderPlaced');
    expect(f.state.tokens).toBe(1);
    f.state.now += 2_000 + WAKE_INTERVAL_MS;
    await waker.enqueued('OrderPlaced');
    expect(f.state.tokens).toBe(2);
    expect(f.wakes()[2]?.headers.authorization).toBe('Bearer t-2');
  });

  test('a queue two workers drain wakes both, and a wake with no token sends none', async () => {
    const f = fakeHost();
    const waker = queueWaker(new Map([['OrderPlaced', [wake('fulfil'), { url: 'https://run.test/jobs/audit:run' }]]]), f.host);
    await waker.enqueued('OrderPlaced');
    await waker.enqueued('OrderPlaced');
    expect(f.wakes().map(r => r.url).sort()).toEqual(['https://run.test/jobs/audit:run', 'https://run.test/jobs/fulfil:run']);
    expect(f.wakes().find(r => r.url.includes('audit'))?.headers.authorization).toBeUndefined();
  });

  test('a wake that fails is a warning and nothing else: enqueued never rejects', async () => {
    const refused = fakeHost({ wakeStatus: 403 });
    await queueWaker(new Map([['OrderPlaced', [wake('fulfil')]]]), refused.host).enqueued('OrderPlaced');
    expect(refused.warnings).toEqual([
      { message: "the wake of a queue's worker failed: its tick finds the message", fields: { queue: 'OrderPlaced', error: 'the wake answered 403' } },
    ]);

    const noToken = fakeHost({ tokenStatus: 404 });
    await queueWaker(new Map([['OrderPlaced', [wake('fulfil')]]]), noToken.host).enqueued('OrderPlaced');
    expect(noToken.warnings[0]?.fields).toEqual({ queue: 'OrderPlaced', error: 'the token request answered 404' });
    expect(noToken.wakes()).toEqual([]);

    const down = fakeHost();
    await queueWaker(new Map([['OrderPlaced', [{ url: 'https://unreachable.test/jobs/x:run' }]]]), down.host).enqueued('OrderPlaced');
    expect(down.warnings[0]?.fields).toEqual({ queue: 'OrderPlaced', error: 'connection refused' });
  });
});

describe('parseQueueWakes', () => {
  test('reads a JSON object of lists of wakes by queue, and nothing from an unset or empty variable', () => {
    expect(parseQueueWakes(undefined).size).toBe(0);
    expect(parseQueueWakes('  ').size).toBe(0);
    const wakes = parseQueueWakes(
      '{"OrderPlaced":[{"url":"https://run.test/jobs/fulfil:run","token":{"url":"http://metadata.test/token","headers":{"Metadata-Flavor":"Google"}}}],"StockLow":[{"url":"https://run.test/jobs/reorder:run"}]}'
    );
    expect(wakes.get('OrderPlaced')).toEqual([
      { url: 'https://run.test/jobs/fulfil:run', token: { url: 'http://metadata.test/token', headers: { 'Metadata-Flavor': 'Google' } } },
    ]);
    expect(wakes.get('StockLow')).toEqual([{ url: 'https://run.test/jobs/reorder:run' }]);
  });

  test('refuses a value that is no such object', () => {
    expect(() => parseQueueWakes('nope')).toThrow(`${QUEUE_WAKES_VARIABLE} is no JSON`);
    expect(() => parseQueueWakes('[]')).toThrow('is no JSON object of lists of wakes by queue');
    expect(() => parseQueueWakes('{"OrderPlaced":{"url":"u"}}')).toThrow('queue OrderPlaced has no list of wakes');
    expect(() => parseQueueWakes('{"OrderPlaced":[{}]}')).toThrow('a wake of queue OrderPlaced has no url');
    expect(() => parseQueueWakes('{"OrderPlaced":[{"url":"u","token":{}}]}')).toThrow('a wake of queue OrderPlaced names a token with no url');
  });
});

describe('wakeAfterEnqueue', () => {
  afterEach(() => setProcessQueueWaker(undefined));

  test("tells the process's waker, and awaits nothing", () => {
    const seen: string[] = [];
    let settle: (() => void) | undefined;
    setProcessQueueWaker({
      enqueued: queue => {
        seen.push(queue);
        return new Promise<void>(resolve => {
          settle = resolve;
        });
      },
    });
    wakeAfterEnqueue('OrderPlaced');
    expect(seen).toEqual(['OrderPlaced']);
    settle?.();
  });

  test('with no QUEUE_WAKES in the environment it sends nothing, and with a broken one it warns once and sends nothing', () => {
    const saved = process.env[QUEUE_WAKES_VARIABLE];
    const warn = console.warn;
    const warned: string[] = [];
    console.warn = (line: string) => void warned.push(line);
    try {
      delete process.env[QUEUE_WAKES_VARIABLE];
      setProcessQueueWaker(undefined);
      wakeAfterEnqueue('OrderPlaced');
      expect(warned).toEqual([]);
      process.env[QUEUE_WAKES_VARIABLE] = '[]';
      setProcessQueueWaker(undefined);
      wakeAfterEnqueue('OrderPlaced');
      wakeAfterEnqueue('OrderPlaced');
      expect(warned).toHaveLength(1);
      expect(warned[0]).toContain('the wakes of the queues could not be read');
    } finally {
      console.warn = warn;
      if (saved === undefined) delete process.env[QUEUE_WAKES_VARIABLE];
      else process.env[QUEUE_WAKES_VARIABLE] = saved;
    }
  });
});
