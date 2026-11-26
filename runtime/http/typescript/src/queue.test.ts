import { describe, expect, test } from 'bun:test';
import pg from 'pg';
import {
  ClaimLostError,
  enqueue,
  formatDuration,
  intervalText,
  parseArray,
  parseDuration,
  parseInterval,
  parsePoint,
  pointText,
  postgresQueue,
  setProcessQueueWaker,
  type QueueDefinition,
  type QueueQueryConfig,
  type QueueStatements,
} from './queue';
import { runWorker, type WorkerQueue } from './worker';

/** The variable that names a Postgres the queue's database tests run against; they are skipped without it. */
const databaseVariable = 'SUPERSCHEMATIC_HTTP_RUNTIME_TEST_DATABASE_URL';
const dsn = process.env[databaseVariable] ?? '';

interface Address {
  line1: string;
  city: string;
}

/** A message of every kind of column a queue's message writes. */
interface OrderPlaced {
  orderId: string;
  placedAt?: Date;
  priority?: string;
  quantity: number;
  shipOn?: string | null;
  note?: string | null;
  shipTo: Address;
  tags?: string[];
  emails?: string[];
}

/**
 * The statements sqlutil.PostgresQueueStatements writes for a table and its
 * columns, as a types package's queues.ts holds them, for a queue that keeps
 * its done messages.
 */
function statements(table: string, columns: string[]): QueueStatements {
  const claimed = `WHERE id = $1::uuid AND claim_token = $2::uuid AND state = 'claimed'`;
  return {
    insert: `INSERT INTO ${table}`,
    dueIn: 'clock_timestamp() + make_interval(secs => $1)',
    expire: `UPDATE ${table} SET state = 'dead', claim_token = NULL, claim_expires_at = NULL,
  last_error = 'the claim of its last attempt expired before its handler finished', updated_at = now()
WHERE state = 'claimed' AND claim_expires_at <= now() AND attempts > $1`,
    claim: `WITH due AS (
  SELECT id FROM ${table}
  WHERE (state = 'ready' AND due_at <= now()) OR (state = 'claimed' AND claim_expires_at <= now())
  ORDER BY due_at, id
  LIMIT $1
  FOR UPDATE SKIP LOCKED
)
UPDATE ${table} AS q SET state = 'claimed', attempts = q.attempts + 1, claim_token = gen_random_uuid(),
  claim_expires_at = now() + make_interval(secs => $2), updated_at = now()
FROM due WHERE q.id = due.id
RETURNING q.id::text, q.claim_token::text, q.attempts${columns.map(c => `, q.${c}`).join('')}`,
    complete: `UPDATE ${table} SET state = 'done', claim_token = NULL, claim_expires_at = NULL, updated_at = now()\n${claimed}`,
    fail: `UPDATE ${table} SET
  state = CASE WHEN attempts > $3 THEN 'dead' ELSE 'ready' END,
  due_at = CASE WHEN attempts > $3 THEN due_at ELSE now() + make_interval(secs => LEAST($4 * power(2, LEAST(attempts - 1, 40)), $6)) END,
  last_error = $5, claim_token = NULL, claim_expires_at = NULL, updated_at = now()
${claimed}
RETURNING state`,
    extend: `UPDATE ${table} SET claim_expires_at = now() + make_interval(secs => $3), updated_at = now()\n${claimed}`,
    release: `UPDATE ${table} SET state = 'ready', attempts = GREATEST(attempts - 1, 0), claim_token = NULL, claim_expires_at = NULL, updated_at = now()\n${claimed}`,
    prune: `DELETE FROM ${table} WHERE id IN (
  SELECT id FROM ${table}
  WHERE state = 'done' AND updated_at <= now() - make_interval(secs => $1)
  ORDER BY updated_at
  LIMIT $2
  FOR UPDATE SKIP LOCKED
)`,
    slot: `SELECT pg_try_advisory_lock(hashtext('superschematic.drain.${table}'), $1::int)`,
  };
}

const fields = [
  { name: 'orderId', column: 'order_id' },
  { name: 'placedAt', column: 'placed_at' },
  { name: 'priority', column: 'priority', default: 'low' },
  { name: 'quantity', column: 'quantity' },
  { name: 'shipOn', column: 'ship_on' },
  { name: 'note', column: 'note' },
  { name: 'shipTo', column: 'ship_to', json: true },
  { name: 'tags', column: 'tags', list: true },
  { name: 'emails', column: 'emails', list: true },
];

function definition(table = 'order_placed_queue', retries = 2): QueueDefinition<OrderPlaced> {
  return {
    name: 'OrderPlaced',
    table,
    retries,
    backoffSeconds: 60,
    maxBackoffSeconds: 200,
    leaseSeconds: 120,
    retainSeconds: 3600,
    fields,
    statements: statements(table, fields.map(f => f.column)),
  };
}

/** A queryable that records each query and answers with the next of its results. */
function recorder(...results: { rows: unknown[]; rowCount: number | null }[]) {
  const queries: QueueQueryConfig[] = [];
  return {
    queries,
    query: async (config: QueueQueryConfig) => {
      queries.push(config);
      return results.shift() ?? { rows: [], rowCount: 0 };
    },
  };
}

describe('an enqueue wakes its queue (D145)', () => {
  test("it tells the process's waker its queue once its statement has run, and not when the statement fails", async () => {
    const woken: string[] = [];
    const dues: (number | undefined)[] = [];
    setProcessQueueWaker({
      enqueued: async (queue, dueAt) => {
        woken.push(queue);
        dues.push(dueAt);
      },
      settled: async () => {},
    });
    try {
      const client = recorder({ rows: [], rowCount: 1 });
      await enqueue(client, definition(), { orderId: 'o-1', quantity: 1, shipTo: { line1: '1 Main St', city: 'Springfield' } });
      expect(client.queries).toHaveLength(1);
      expect(woken).toEqual(['OrderPlaced']);
      expect(dues).toEqual([undefined]);
      // A delayed message says when it falls due, so a wake can wait for it (D151).
      const before = Date.now();
      await enqueue(recorder({ rows: [], rowCount: 1 }), definition(), { orderId: 'o-4', quantity: 1, shipTo: { line1: '1 Main St', city: 'Springfield' } }, { delaySeconds: 30 });
      expect(dues[1]).toBeGreaterThanOrEqual(before + 30_000);
      expect(dues[1]).toBeLessThanOrEqual(Date.now() + 30_000);
      woken.length = 0;
      woken.push('OrderPlaced');
      const broken = {
        query: async () => {
          throw new Error('the database is down');
        },
      };
      await expect(enqueue(broken, definition(), { orderId: 'o-2', quantity: 1, shipTo: { line1: '1 Main St', city: 'Springfield' } })).rejects.toThrow(
        'the database is down'
      );
      expect(woken).toEqual(['OrderPlaced']);
      // A waker that throws fails no enqueue.
      setProcessQueueWaker({
        enqueued: () => {
          throw new Error('the waker broke');
        },
        settled: async () => {},
      });
      await enqueue(recorder({ rows: [], rowCount: 1 }), definition(), { orderId: 'o-3', quantity: 1, shipTo: { line1: '1 Main St', city: 'Springfield' } });
    } finally {
      setProcessQueueWaker(undefined);
    }
  });
});

describe("a queue's slots (D145)", () => {
  /** A pool whose connections answer the slot statement from the slots taken, and record how each was released. */
  function slotPool(taken: Set<number>) {
    const released: (boolean | undefined)[] = [];
    const asked: unknown[][] = [];
    return {
      released,
      asked,
      query: async () => ({ rows: [], rowCount: 0 }),
      connect: async () => ({
        query: async (config: QueueQueryConfig) => {
          asked.push(config.values);
          const slot = config.values[0] as number;
          if (taken.has(slot)) return { rows: [[false]], rowCount: 1 };
          taken.add(slot);
          return { rows: [[true]], rowCount: 1 };
        },
        release: (destroy?: boolean) => void released.push(destroy),
      }),
    };
  }

  test('slot takes the first free of its slots on a connection of its own, which giving it back ends', async () => {
    const taken = new Set<number>([1]);
    const pool = slotPool(taken);
    const give = await postgresQueue(pool, definition()).slot(3);
    expect(give).toBeDefined();
    expect(pool.asked).toEqual([[1], [2]]);
    expect(pool.released).toEqual([]);
    await give?.();
    // The connection is destroyed, so the session and its lock end.
    expect(pool.released).toEqual([true]);
  });

  test('with every slot taken it resolves to undefined and gives its connection back', async () => {
    const pool = slotPool(new Set([1, 2]));
    expect(await postgresQueue(pool, definition()).slot(2)).toBeUndefined();
    expect(pool.asked).toEqual([[1], [2]]);
    expect(pool.released).toEqual([undefined]);
  });

  test('it refuses a client that is no pool, and a definition written before slots', async () => {
    await expect(postgresQueue(recorder(), definition()).slot(1)).rejects.toThrow('gives no connection of its own');
    // A pg Client's connect resolves to no connection of its own.
    const client = { ...recorder(), connect: async () => undefined };
    await expect(postgresQueue(client, definition()).slot(1)).rejects.toThrow('gives no connection of its own');
    const { slot: _slot, ...before } = definition().statements;
    await expect(postgresQueue(slotPool(new Set()), { ...definition(), statements: before }).slot(1)).rejects.toThrow('holds no slot statement');
  });

  test.skipIf(dsn === '')('on Postgres, two pools take different slots of one queue, a third finds none, and a slot given back is free', async () => {
    const table = `slot_probe_${Date.now()}_queue`;
    const pools = [1, 2, 3].map(() => new pg.Pool({ connectionString: dsn, max: 2 }));
    try {
      const queues = pools.map(pool => postgresQueue(pool, definition(table)));
      const first = await queues[0]!.slot(2);
      const second = await queues[1]!.slot(2);
      expect(first).toBeDefined();
      expect(second).toBeDefined();
      expect(await queues[2]!.slot(2)).toBeUndefined();
      // Another queue's slots are its own.
      const other = await postgresQueue(pools[2]!, definition(`${table}_other`)).slot(1);
      expect(other).toBeDefined();
      await other?.();
      await first?.();
      const again = await queues[2]!.slot(2);
      expect(again).toBeDefined();
      await again?.();
      await second?.();
    } finally {
      await Promise.all(pools.map(pool => pool.end()));
    }
  });
});

describe('parseArray', () => {
  test('reads a one-dimensional array literal, quoted elements and NULL included', () => {
    expect(parseArray('{}')).toEqual([]);
    expect(parseArray('{a,b}')).toEqual(['a', 'b']);
    expect(parseArray('{"a b",NULL,"say \\"hi\\"",c}')).toEqual(['a b', null, 'say "hi"', 'c']);
    expect(parseArray('{""}')).toEqual(['']);
    expect(() => parseArray('a,b')).toThrow('is no Postgres array');
  });
});

describe('locations and durations', () => {
  test('a location is a POINT whose x is the longitude and y the latitude', () => {
    expect(pointText({ lat: 37.7749, lon: -122.4194 })).toBe('(-122.4194,37.7749)');
    expect(pointText({ lat: 0, lon: 0 })).toBe('(0,0)');
    expect(parsePoint('(-122.4194,37.7749)')).toEqual({ lat: 37.7749, lon: -122.4194 });
    expect(parsePoint('(1e-7,-90)')).toEqual({ lat: -90, lon: 1e-7 });
    expect(() => pointText({ lat: '1', lon: 2 })).toThrow('is no location');
    expect(() => pointText(null)).toThrow('is no location');
    expect(() => parsePoint('(1,2,3)')).toThrow('is no POINT');
  });

  test("a duration's text reads as its nanoseconds, in every unit and form the scalar takes", () => {
    const cases: [string, bigint][] = [
      ['0', 0n],
      ['0s', 0n],
      ['1h30m0s', 5_400_000_000_000n],
      ['90m', 5_400_000_000_000n],
      ['1.5s', 1_500_000_000n],
      ['-1h2m3.5s', -3_723_500_000_000n],
      ['+500ms', 500_000_000n],
      ['250us', 250_000n],
      ['250µs', 250_000n],
      ['250μs', 250_000n],
      ['1500ns', 1_500n],
      ['.5s', 500_000_000n],
      ['2562047h47m16.854775807s', 9_223_372_036_854_775_807n],
    ];
    for (const [text, nanos] of cases) expect([text, parseDuration(text)]).toEqual([text, nanos]);
    for (const text of ['', '-', '1', '1d', 's', '1h-2m', '1.2.3s']) expect(() => parseDuration(text)).toThrow('is no duration');
  });

  test('a duration writes the interval of its whole microseconds, its sign for the whole', () => {
    expect(intervalText('1h30m0s')).toBe('1:30:00');
    expect(intervalText('26h0m0.000001s')).toBe('26:00:00.000001');
    expect(intervalText('-1h2m3.5s')).toBe('-1:02:03.500000');
    expect(intervalText('1500ns')).toBe('0:00:00.000001');
    expect(intervalText('-1500ns')).toBe('-0:00:00.000001');
    expect(intervalText('999ns')).toBe('0:00:00');
    expect(intervalText('0s')).toBe('0:00:00');
    expect(() => intervalText(90)).toThrow('is no duration');
  });

  test("an interval's text in IntervalStyle postgres reads as its duration, a day 24 hours and a month 30 days", () => {
    const cases: [string, string][] = [
      ['00:00:00', '0s'],
      ['01:30:00', '1h30m0s'],
      ['26:00:00.000001', '26h0m0.000001s'],
      ['-01:02:03.5', '-1h2m3.5s'],
      ['00:00:00.00025', '250us'],
      ['-00:00:00.000001', '-1us'],
      ['00:00:01.5', '1.5s'],
      ['00:01:30', '1m30s'],
      ['00:00:00.5', '500ms'],
      ['1 day', '24h0m0s'],
      ['1 day 02:00:00', '26h0m0s'],
      ['-1 days +02:00:00', '-22h0m0s'],
      ['-2 days', '-48h0m0s'],
      ['1 mon 1 day -01:00:00', '743h0m0s'],
      ['1 year 2 mons', '10080h0m0s'],
    ];
    for (const [text, duration] of cases) expect([text, formatDuration(parseInterval(text))]).toEqual([text, duration]);
    for (const text of ['', 'P1D', '@ 1 hour', '1 week', '3']) expect(() => parseInterval(text)).toThrow('is no interval');
  });

  test('a location and a duration write their columns, and a value of neither is refused, naming its field', async () => {
    const delivery: QueueDefinition<{ dropAt: unknown; wait: unknown; grace?: string }> = {
      ...definition(),
      table: 'delivery_planned_queue',
      fields: [
        { name: 'dropAt', column: 'drop_at', point: true },
        { name: 'wait', column: 'wait', interval: true },
        { name: 'grace', column: 'grace', interval: true },
      ],
    };
    const client = recorder();
    await enqueue(client, delivery, { dropAt: { lat: 37.7749, lon: -122.4194 }, wait: '-1h2m3.5s' });
    expect(client.queries[0]?.values).toEqual(['(-122.4194,37.7749)', '-1:02:03.500000']);
    await expect(enqueue(client, delivery, { dropAt: { lat: 1 }, wait: '1s' })).rejects.toThrow('enqueue on delivery_planned_queue: field dropAt: {"lat":1} is no location');
    await expect(enqueue(client, delivery, { dropAt: { lat: 1, lon: 2 }, wait: '1 day' })).rejects.toThrow('enqueue on delivery_planned_queue: field wait: "1 day" is no duration');
  });
});

describe('enqueue', () => {
  test('writes the members a message sets, a JSON column as its JSON text, and an unset enum as its default', async () => {
    const client = recorder();
    await enqueue(client, definition(), { orderId: 'o-1', quantity: 3, shipTo: { line1: '1 Main St', city: 'Springfield' }, tags: ['gift'], note: null });
    expect(client.queries).toEqual([
      {
        text: 'INSERT INTO order_placed_queue (order_id, priority, quantity, ship_to, tags) VALUES ($1, $2, $3, $4, $5)',
        values: ['o-1', 'low', 3, '{"line1":"1 Main St","city":"Springfield"}', ['gift']],
      },
    ]);
  });

  test('a message that sets no column takes every default', async () => {
    const client = recorder();
    const ping: QueueDefinition<{ sentAt?: Date }> = { ...definition(), fields: [{ name: 'sentAt', column: 'sent_at' }], statements: statements('ping_queue', ['sent_at']) };
    await enqueue(client, ping, {});
    expect(client.queries[0]?.text).toBe('INSERT INTO ping_queue DEFAULT VALUES');
  });

  test('a delay writes due_at first, as the database clock plus its seconds, and numbers the columns after it (D80)', async () => {
    const client = recorder();
    await enqueue(client, definition(), { orderId: 'o-1', quantity: 3, shipTo: { line1: '1 Main St', city: 'Springfield' }, tags: [], note: null }, { delaySeconds: 90.5 });
    expect(client.queries).toEqual([
      {
        text: 'INSERT INTO order_placed_queue (due_at, order_id, priority, quantity, ship_to, tags) VALUES (clock_timestamp() + make_interval(secs => $1), $2, $3, $4, $5, $6)',
        values: [90.5, 'o-1', 'low', 3, '{"line1":"1 Main St","city":"Springfield"}', []],
      },
    ]);
    // A message that sets no column still writes when it is due.
    const ping: QueueDefinition<{ sentAt?: Date }> = { ...definition(), fields: [{ name: 'sentAt', column: 'sent_at' }], statements: statements('ping_queue', ['sent_at']) };
    await enqueue(client, ping, {}, { delaySeconds: 5 });
    expect(client.queries[1]).toEqual({ text: 'INSERT INTO ping_queue (due_at) VALUES (clock_timestamp() + make_interval(secs => $1))', values: [5] });
    // No delay, and a delay of zero, are an enqueue as it always was: due_at takes its column's default.
    await enqueue(client, ping, {}, { delaySeconds: 0 });
    await enqueue(client, ping, {}, {});
    expect(client.queries.slice(2).map(q => q.text)).toEqual(['INSERT INTO ping_queue DEFAULT VALUES', 'INSERT INTO ping_queue DEFAULT VALUES']);
  });

  test('a delay that is negative or no number is refused before anything is written, as is one a definition cannot write', async () => {
    const client = recorder();
    for (const delaySeconds of [-1, Number.NaN, Number.POSITIVE_INFINITY, '5' as unknown as number]) {
      await expect(enqueue(client, definition(), { orderId: 'o-1', quantity: 1 } as OrderPlaced, { delaySeconds })).rejects.toThrow(
        /enqueue on order_placed_queue: delaySeconds is .*; it is a number of seconds, zero or more/,
      );
    }
    const { dueIn: _, ...before } = definition().statements;
    await expect(enqueue(client, { ...definition(), statements: before }, { orderId: 'o-1', quantity: 1 } as OrderPlaced, { delaySeconds: 5 })).rejects.toThrow(
      "enqueue on order_placed_queue: the queue's definition holds no dueIn statement, so it cannot delay a message",
    );
    expect(client.queries).toEqual([]);
  });

  test('names the table when the database refuses the message', async () => {
    const client = {
      query: async () => {
        throw new Error('null value in column "order_id"');
      },
    };
    await expect(enqueue(client, definition(), { quantity: 1 } as OrderPlaced)).rejects.toThrow('enqueue on order_placed_queue: null value in column "order_id"');
  });
});

describe('postgresQueue', () => {
  test("a claim marks expired last attempts dead first, then claims, and reads each row's columns into the message", async () => {
    const placedAt = new Date('2026-10-12T09:30:00Z');
    const client = recorder(
      { rows: [], rowCount: 0 },
      {
        rows: [['m-1', 't-1', 2, 'o-1', placedAt, 'high', 3, '2026-10-13', null, '{"line1":"1 Main St","city":"Springfield"}', ['gift'], '{a@example.com,"b c@example.com"}']],
        rowCount: 1,
      }
    );
    const claims = await postgresQueue(client, definition()).claim(4);
    expect(client.queries.map(q => q.values)).toEqual([[2], [4, 120]]);
    expect(client.queries[1]?.rowMode).toBe('array');
    expect(claims).toEqual([
      {
        id: 'm-1',
        token: 't-1',
        attempt: 2,
        message: {
          orderId: 'o-1',
          placedAt,
          priority: 'high',
          quantity: 3,
          shipOn: '2026-10-13',
          shipTo: { line1: '1 Main St', city: 'Springfield' },
          tags: ['gift'],
          emails: ['a@example.com', 'b c@example.com'],
        },
      },
    ]);
  });

  test("a JSON column is decoded once, by the column's type: a JSON string reads as the string, and JSON of another kind than its type's fails the row alone", async () => {
    const detailFields = [
      { name: 'detail', column: 'detail', json: true },
      { name: 'shipTo', column: 'ship_to', json: true, shape: 'object' as const },
      { name: 'lines', column: 'lines', json: true, shape: 'list' as const },
    ];
    const def: QueueDefinition<{ detail?: unknown; shipTo?: Address; lines?: unknown[] }> = {
      ...definition(),
      fields: detailFields,
      statements: statements('order_placed_queue', detailFields.map(f => f.column)),
    } as QueueDefinition<{ detail?: unknown; shipTo?: Address; lines?: unknown[] }>;
    const client = recorder(
      { rows: [], rowCount: 0 },
      {
        rows: [
          ['m-1', 't-1', 1, '"1"', '{"line1":"1 Main St","city":"Springfield"}', '[1]'],
          ['m-2', 't-2', 1, '"x"', 'null', '[]'],
          ['m-3', 't-3', 1, '1', '"no address"', '[]'],
          ['m-4', 't-4', 1, '{}', '{"line1":"2 Side St","city":"York"}', '{"a":1}'],
        ],
        rowCount: 4,
      },
      { rows: [{ state: 'ready' }], rowCount: 1 },
      { rows: [{ state: 'ready' }], rowCount: 1 }
    );
    const claims = await postgresQueue(client, def).claim(4);
    expect(claims.map(c => [c.id, c.message])).toEqual([
      ['m-1', { detail: '1', shipTo: { line1: '1 Main St', city: 'Springfield' }, lines: [1] }],
      ['m-2', { detail: 'x', shipTo: null, lines: [] }],
    ]);
    // pg would decode json (114) and jsonb (3802) itself: the claim takes their text.
    const types = client.queries[1]?.types;
    expect((types?.getTypeParser(3802, 'text') as (text: string) => unknown)('"x"')).toBe('"x"');
    expect((types?.getTypeParser(114, 'text') as (text: string) => unknown)('"1"')).toBe('"1"');
    expect(client.queries.slice(2).map(q => [q.values[0], q.values[4]])).toEqual([
      ['m-3', 'the message could not be read: message m-3 of order_placed_queue: field shipTo: holds a string, where its type is an object'],
      ['m-4', 'the message could not be read: message m-4 of order_placed_queue: field lines: holds an object, where its type is a list'],
    ]);
  });

  test('a claim for no message asks nothing', async () => {
    const client = recorder();
    expect(await postgresQueue(client, definition()).claim(0)).toEqual([]);
    expect(client.queries).toEqual([]);
  });

  test('a change the token no longer claims is a lost claim, and fail reports whether the message is dead', async () => {
    const queue = postgresQueue(recorder({ rows: [], rowCount: 0 }), definition());
    await expect(queue.complete('m-1', 't-1')).rejects.toBeInstanceOf(ClaimLostError);
    const failing = recorder({ rows: [{ state: 'dead' }], rowCount: 1 }, { rows: [], rowCount: 0 });
    const q = postgresQueue(failing, definition());
    expect(await q.fail('m-1', 't-1', 'boom')).toBe(true);
    expect(failing.queries[0]?.values).toEqual(['m-1', 't-1', 2, 60, 'boom', 200]);
    await expect(q.fail('m-1', 't-1', 'boom')).rejects.toThrow('fail message m-1 on order_placed_queue: the claim on the message is lost');
  });

  test('a prune deletes up to its limit of the messages done for the retention, and asks nothing for none', async () => {
    const client = recorder({ rows: [], rowCount: 2 }, { rows: [], rowCount: null });
    const queue = postgresQueue(client, definition());
    expect(await queue.prune(0)).toBe(0);
    expect(client.queries).toEqual([]);
    expect(await queue.prune(1000)).toBe(2);
    expect(client.queries[0]).toEqual({ text: definition().statements.prune, values: [3600, 1000] });
    expect(await queue.prune(1000)).toBe(0);
    const failing = postgresQueue(
      {
        query: async () => {
          throw new Error('the database is down');
        },
      },
      definition()
    );
    await expect(failing.prune(10)).rejects.toThrow('prune order_placed_queue: the database is down');
  });
});

/** The queue's table as sqlgen writes it, in a schema of the test's own. */
function createTable(table: string): string {
  return `CREATE TABLE ${table} (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  order_id UUID NOT NULL,
  placed_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  priority TEXT NOT NULL,
  quantity BIGINT NOT NULL,
  ship_on DATE,
  note TEXT,
  ship_to TEXT NOT NULL,
  tags TEXT[] DEFAULT '{}' NOT NULL,
  emails CITEXT[] DEFAULT '{}' NOT NULL,
  state TEXT DEFAULT 'ready' NOT NULL,
  attempts INTEGER DEFAULT 0 NOT NULL,
  due_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  claim_expires_at TIMESTAMPTZ,
  claim_token UUID,
  last_error TEXT,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL
)`;
}

/** A queue's table whose message holds a location, a POINT, and a duration, an INTERVAL, each required and optional, as sqlgen writes it. */
const deliveryTable = `CREATE TABLE delivery_planned_queue (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  drop_at POINT NOT NULL,
  wait INTERVAL NOT NULL,
  meet_at POINT,
  grace INTERVAL,
  state TEXT DEFAULT 'ready' NOT NULL,
  attempts INTEGER DEFAULT 0 NOT NULL,
  due_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  claim_expires_at TIMESTAMPTZ,
  claim_token UUID,
  last_error TEXT,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL
)`;

interface DeliveryPlanned {
  dropAt: { lat: number; lon: number };
  wait: string;
  meetAt?: { lat: number; lon: number };
  grace?: string;
}

describe.skipIf(dsn === '')('on Postgres', () => {
  test('a JSON value reads back as it was enqueued from a jsonb, a json and a text column, a JSON string among them', async () => {
    const schema = `queue_runtime_json_${Date.now()}`;
    const admin = new pg.Pool({ connectionString: dsn, max: 1 });
    await admin.query(`CREATE SCHEMA ${schema}`);
    const pool = new pg.Pool({ connectionString: dsn, max: 2, options: `-c search_path=${schema},public` });
    try {
      await pool.query(`CREATE TABLE note_queue (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  seq INTEGER NOT NULL,
  as_jsonb JSONB NOT NULL,
  as_json JSON NOT NULL,
  as_text TEXT NOT NULL,
  state TEXT DEFAULT 'ready' NOT NULL,
  attempts INTEGER DEFAULT 0 NOT NULL,
  due_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  claim_expires_at TIMESTAMPTZ,
  claim_token UUID,
  last_error TEXT,
  created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL
)`);
      const noteFields = [
        { name: 'seq', column: 'seq' },
        { name: 'asJsonb', column: 'as_jsonb', json: true },
        { name: 'asJson', column: 'as_json', json: true },
        { name: 'asText', column: 'as_text', json: true },
      ];
      interface Note {
        seq: number;
        asJsonb: unknown;
        asJson: unknown;
        asText: unknown;
      }
      const def: QueueDefinition<Note> = {
        ...definition('note_queue'),
        name: 'Note',
        fields: noteFields,
        statements: statements('note_queue', noteFields.map(f => f.column)),
      } as unknown as QueueDefinition<Note>;
      const values: unknown[] = ['1', 'x', '', '{"a":1}', 1, 2.5, true, false, { a: '1', b: [1, 'x'] }, ['1', 2]];
      for (const [seq, value] of values.entries()) {
        await enqueue(pool, def, { seq, asJsonb: value, asJson: value, asText: value });
      }
      const claims = await postgresQueue(pool, def).claim(values.length + 1);
      expect(claims.map(c => c.message).sort((a, b) => a.seq - b.seq)).toEqual(
        values.map((value, seq) => ({ seq, asJsonb: value, asJson: value, asText: value }))
      );
    } finally {
      await pool.end();
      await admin.query(`DROP SCHEMA ${schema} CASCADE`);
      await admin.end();
    }
  });

  test('a location crosses as a POINT and a duration as an INTERVAL, and each reads back as the TypeScript types hold it', async () => {
    const schema = `queue_runtime_delivery_${Date.now()}`;
    const admin = new pg.Pool({ connectionString: dsn, max: 1 });
    await admin.query(`CREATE SCHEMA ${schema}`);
    const pool = new pg.Pool({ connectionString: dsn, max: 2, options: `-c search_path=${schema},public` });
    try {
      await pool.query(deliveryTable);
      const deliveryFields = [
        { name: 'dropAt', column: 'drop_at', point: true },
        { name: 'wait', column: 'wait', interval: true },
        { name: 'meetAt', column: 'meet_at', point: true },
        { name: 'grace', column: 'grace', interval: true },
      ];
      const def: QueueDefinition<DeliveryPlanned> = {
        ...definition('delivery_planned_queue'),
        name: 'DeliveryPlanned',
        fields: deliveryFields,
        statements: statements('delivery_planned_queue', deliveryFields.map(f => f.column)),
      };
      const full: DeliveryPlanned = { dropAt: { lat: 37.7749, lon: -122.4194 }, wait: '26h0m0.0000015s', meetAt: { lat: -33.8688, lon: 151.2093 }, grace: '-1m30.25s' };
      await enqueue(pool, def, full);
      await enqueue(pool, def, { dropAt: { lat: 0, lon: 0 }, wait: '-1500ns' });
      const { rows } = await pool.query<{ x: number; y: number; wait: string; meet: string | null; grace: string | null }>(
        'SELECT (drop_at)[0] AS x, (drop_at)[1] AS y, wait::text AS wait, meet_at::text AS meet, grace::text AS grace FROM delivery_planned_queue ORDER BY wait DESC'
      );
      expect(rows).toEqual([
        { x: -122.4194, y: 37.7749, wait: '26:00:00.000001', meet: '(151.2093,-33.8688)', grace: '-00:01:30.25' },
        { x: 0, y: 0, wait: '-00:00:00.000001', meet: null, grace: null },
      ]);

      const queue = postgresQueue(pool, def);
      const claims = await queue.claim(5);
      expect(claims.map(c => c.message).sort((a, b) => a.wait.localeCompare(b.wait))).toEqual([
        { dropAt: { lat: 0, lon: 0 }, wait: '-1us' },
        { ...full, wait: '26h0m0.000001s' },
      ]);
      for (const claim of claims) await queue.release(claim.id, claim.token);

      // An interval with days and months reads as their hours.
      await pool.query(`UPDATE delivery_planned_queue SET wait = '1 mon 1 day -01:00:00', grace = '-2 days'`);
      const again = await queue.claim(5);
      expect(again.map(c => [c.message.wait, c.message.grace])).toEqual([
        ['743h0m0s', '-48h0m0s'],
        ['743h0m0s', '-48h0m0s'],
      ]);
    } finally {
      await pool.end();
      await admin.query(`DROP SCHEMA ${schema} CASCADE`);
      await admin.end();
    }
  });

  test('a worker in drain mode drains a real queue while it holds its slot, a second start finds the slot taken and stops, and the first stops once the queue is idle (D145)', async () => {
    const schema = `queue_runtime_drain_${Date.now()}`;
    const admin = new pg.Pool({ connectionString: dsn, max: 1 });
    await admin.query(`CREATE SCHEMA ${schema}`);
    await admin.query('CREATE EXTENSION IF NOT EXISTS citext WITH SCHEMA public');
    const options = `-c search_path=${schema},public`;
    const pool = new pg.Pool({ connectionString: dsn, max: 4, options });
    const other = new pg.Pool({ connectionString: dsn, max: 2, options });
    try {
      // The slot is named for the table, so the table's name is the test's own.
      const table = `drained_${Date.now()}_queue`;
      await pool.query(createTable(table));
      const def = definition(table);
      const orders = ['6f1c2b8e-1d5a-4a8e-9a43-0f3a2c1e7b91', '6f1c2b8e-1d5a-4a8e-9a43-0f3a2c1e7b92', '6f1c2b8e-1d5a-4a8e-9a43-0f3a2c1e7b93'];
      for (const orderId of orders) {
        await enqueue(pool, def, { orderId, quantity: 1, shipTo: { line1: '1 Main St', city: 'Springfield' } });
      }
      const handled: string[] = [];
      /** The queue a generated worker entrypoint builds over postgresQueue's claims, whose handler waits for hold. */
      const workerQueue = (from: pg.Pool, hold: Promise<void>): WorkerQueue => {
        const queue = postgresQueue(from, def);
        return {
          name: queue.name,
          leaseSeconds: queue.leaseSeconds,
          claim: async (n, leaseSeconds) =>
            (await queue.claim(n, leaseSeconds)).map(claim => ({
              id: claim.id,
              token: claim.token,
              attempt: claim.attempt,
              handle: async () => {
                await hold;
                handled.push(claim.message.orderId);
              },
            })),
          extend: (id, token, leaseSeconds) => queue.extend(id, token, leaseSeconds),
          complete: (id, token) => queue.complete(id, token),
          fail: (id, token, cause) => queue.fail(id, token, cause),
          release: (id, token) => queue.release(id, token),
          prune: limit => queue.prune(limit),
          slot: slots => queue.slot(slots),
        };
      };
      const count = async (state: string): Promise<number> => {
        const { rows } = await pool.query<{ n: number }>(`SELECT count(*)::int AS n FROM ${table} WHERE state = $1`, [state]);
        return rows[0]?.n ?? 0;
      };
      const until = async (what: string, cond: () => Promise<boolean>): Promise<void> => {
        const deadline = Date.now() + 10_000;
        while (!(await cond())) {
          if (Date.now() > deadline) throw new Error(`timed out waiting for ${what}`);
          await new Promise(resolve => setTimeout(resolve, 20));
        }
      };
      const never = new AbortController().signal;
      let letGo: () => void = () => {};
      const hold = new Promise<void>(resolve => {
        letGo = resolve;
      });
      const first = runWorker(workerQueue(pool, hold), { signal: never, concurrency: 3, idleMs: 20, drain: { idleMs: 300, slots: 1 } });
      await until("the first worker's claims", async () => (await count('claimed')) === 3);

      // A second start of the worker, as an overlapping wake makes, finds the
      // one slot taken: it stops at once, whatever its idle time.
      const began = Date.now();
      await runWorker(workerQueue(other, Promise.resolve()), { signal: never, idleMs: 20, drain: { idleMs: 60_000, slots: 1 } });
      expect(Date.now() - began).toBeLessThan(10_000);
      expect(handled).toEqual([]);

      letGo();
      await first;
      expect([...handled].sort()).toEqual(orders);
      expect(await count('done')).toBe(3);
      expect(await count('ready')).toBe(0);

      // The first gave its slot back as it stopped: a later start takes it.
      let give: (() => Promise<void>) | undefined;
      await until('the slot to be free', async () => (give = await postgresQueue(other, def).slot(1)) !== undefined);
      await give?.();
    } finally {
      await pool.end();
      await other.end();
      await admin.query(`DROP SCHEMA ${schema} CASCADE`);
      await admin.end();
    }
  });

  test('nextDue reads the seconds until the first ready message that waits falls due, and none for one due already or a queue that is empty (D145)', async () => {
    const schema = `queue_next_due_${Date.now()}`;
    const admin = new pg.Pool({ connectionString: dsn, max: 1 });
    await admin.query(`CREATE SCHEMA ${schema}`);
    await admin.query('CREATE EXTENSION IF NOT EXISTS citext WITH SCHEMA public');
    const pool = new pg.Pool({ connectionString: dsn, max: 2, options: `-c search_path=${schema},public` });
    try {
      await pool.query(createTable('order_placed_queue'));
      const table = 'order_placed_queue';
      const def = { ...definition(), statements: { ...definition().statements, nextDue: `SELECT EXTRACT(EPOCH FROM min(due_at) - clock_timestamp())::float8 FROM ${table} WHERE state = 'ready' AND due_at > clock_timestamp()` } };
      const queue = postgresQueue(pool, def);
      expect(await queue.nextDue()).toBeUndefined();
      const message = { orderId: '6f1c2b8e-1d5a-4a8e-9a43-0f3a2c1e7b9d', quantity: 1, shipTo: { line1: '1 Main St', city: 'Springfield' } };
      await enqueue(pool, def, message);
      expect(await queue.nextDue()).toBeUndefined();
      await enqueue(pool, def, message, { delaySeconds: 90 });
      await enqueue(pool, def, message, { delaySeconds: 30 });
      const seconds = await queue.nextDue();
      expect(seconds).toBeGreaterThan(25);
      expect(seconds).toBeLessThanOrEqual(30);
      // A definition written before the statement reads none.
      const { nextDue: _, ...before } = def.statements;
      expect(await postgresQueue(pool, { ...def, statements: before }).nextDue()).toBeUndefined();
    } finally {
      await pool.end();
      await admin.query(`DROP SCHEMA ${schema} CASCADE`);
      await admin.end();
    }
  });

  test("a delayed enqueue late in a long transaction is due its delay after the statement, not after the transaction's start (D80)", async () => {
    const schema = `queue_due_in_${Date.now()}`;
    const admin = new pg.Pool({ connectionString: dsn, max: 1 });
    await admin.query(`CREATE SCHEMA ${schema}`);
    await admin.query('CREATE EXTENSION IF NOT EXISTS citext WITH SCHEMA public');
    const pool = new pg.Pool({ connectionString: dsn, max: 2, options: `-c search_path=${schema},public` });
    try {
      await pool.query(createTable('order_placed_queue'));
      const client = await pool.connect();
      try {
        await client.query('BEGIN');
        // The transaction has run for a second and a half when it enqueues
        // with a delay of two seconds: the message is due three and a half
        // seconds after the transaction began, not two.
        await client.query('SELECT pg_sleep(1.5)');
        await enqueue(client, definition(), { orderId: '6f1c2b8e-1d5a-4a8e-9a43-0f3a2c1e7b9d', quantity: 1, shipTo: { line1: '1 Main St', city: 'Springfield' } }, { delaySeconds: 2 });
        const { rows } = await client.query<{ late: boolean }>(`SELECT due_at >= now() + interval '3 seconds' AS late FROM order_placed_queue`);
        expect(rows).toEqual([{ late: true }]);
        await client.query('COMMIT');
      } finally {
        client.release();
      }
    } finally {
      await pool.end();
      await admin.query(`DROP SCHEMA ${schema} CASCADE`);
      await admin.end();
    }
  });

  test('enqueue commits with its transaction, claims take different messages, and fail, extend, release and an expired claim behave as the Go ORM', async () => {
    const schema = `queue_runtime_${Date.now()}`;
    const admin = new pg.Pool({ connectionString: dsn, max: 1 });
    await admin.query(`CREATE SCHEMA ${schema}`);
    await admin.query('CREATE EXTENSION IF NOT EXISTS citext WITH SCHEMA public');
    const pool = new pg.Pool({ connectionString: dsn, max: 4, options: `-c search_path=${schema},public` });
    try {
      await pool.query(createTable('order_placed_queue'));
      const def = definition();
      const queue = postgresQueue(pool, def);
      const message: OrderPlaced = {
        orderId: '6f1c2b8e-1d5a-4a8e-9a43-0f3a2c1e7b9d',
        placedAt: new Date('2026-10-12T09:30:00.123Z'),
        quantity: 9_007_199_254_740_991,
        shipOn: '2026-10-13',
        shipTo: { line1: '1 Main St', city: 'Springfield' },
        tags: ['gift', 'fragile'],
        emails: ['A@Example.com'],
      };

      // A transaction that rolls back enqueues nothing; one that commits, once.
      for (const outcome of ['ROLLBACK', 'COMMIT']) {
        const client = await pool.connect();
        try {
          await client.query('BEGIN');
          await enqueue(client, def, message);
          await client.query(outcome);
        } finally {
          client.release();
        }
      }
      const [claim, ...more] = await queue.claim(5);
      expect(more).toEqual([]);
      expect(claim?.attempt).toBe(1);
      expect(claim?.message).toEqual({ ...message, priority: 'low' });
      expect(await queue.claim(5)).toEqual([]);

      // Extend, then fail: due again after the backoff, its error kept.
      await queue.extend(claim!.id, claim!.token);
      expect(await queue.fail(claim!.id, claim!.token, 'try later')).toBe(false);
      await expect(queue.complete(claim!.id, claim!.token)).rejects.toBeInstanceOf(ClaimLostError);
      const due = async () => pool.query(`UPDATE order_placed_queue SET due_at = now() - interval '1 second' WHERE state = 'ready'`);
      await due();

      // Two claims at once take different messages.
      const client = await pool.connect();
      try {
        await client.query('BEGIN');
        await enqueue(client, def, { ...message, orderId: '0c7a0e14-6a43-4f0e-8f1b-5d6c7e8f9a0b' });
        await client.query('COMMIT');
      } finally {
        client.release();
      }
      const [a, b] = await Promise.all([queue.claim(1), queue.claim(1)]);
      expect(a).toHaveLength(1);
      expect(b).toHaveLength(1);
      expect(a![0]!.id).not.toBe(b![0]!.id);
      const retried = [...a!, ...b!].find(c => c.id === claim!.id)!;
      const other = [...a!, ...b!].find(c => c.id !== claim!.id)!;
      expect(retried.attempt).toBe(2);

      // Release gives the message back without counting the attempt.
      await queue.release(other.id, other.token);
      const [again] = await queue.claim(1);
      expect(again?.id).toBe(other.id);
      expect(again?.attempt).toBe(1);
      await queue.complete(again!.id, again!.token);

      // The third failure is past the two retries: dead.
      expect(await queue.fail(retried.id, retried.token, 'still failing')).toBe(false);
      await due();
      const [third] = await queue.claim(1);
      expect(third?.attempt).toBe(3);
      expect(await queue.fail(third!.id, third!.token, 'fatal')).toBe(true);
      const { rows } = await pool.query<{ state: string; last_error: string }>('SELECT state, last_error FROM order_placed_queue ORDER BY state');
      expect(rows).toEqual([
        { state: 'dead', last_error: 'fatal' },
        { state: 'done', last_error: null as unknown as string },
      ]);

      // A claim that expired is taken back, and one whose last attempt
      // expired is dead.
      await enqueue(pool, def, message);
      const [expiring] = await queue.claim(1, 0.05);
      await new Promise(resolve => setTimeout(resolve, 100));
      const [takenBack] = await queue.claim(1, 0.05);
      expect(takenBack?.id).toBe(expiring?.id);
      expect(takenBack?.attempt).toBe(2);
      await expect(queue.release(expiring!.id, expiring!.token)).rejects.toBeInstanceOf(ClaimLostError);
      await pool.query(`UPDATE order_placed_queue SET attempts = 3 WHERE id = $1`, [takenBack!.id]);
      await new Promise(resolve => setTimeout(resolve, 100));
      expect(await queue.claim(1)).toEqual([]);
      const dead = await pool.query<{ state: string }>('SELECT state FROM order_placed_queue WHERE id = $1', [takenBack!.id]);
      expect(dead.rows[0]?.state).toBe('dead');

      // Each failure doubles the backoff, up to the maximum: 60s, 120s,
      // then 200s, under 240.
      const patient = postgresQueue(pool, definition('order_placed_queue', 5));
      await pool.query('DELETE FROM order_placed_queue');
      await enqueue(pool, def, message);
      for (const [attempt, wait] of [
        [1, 60],
        [2, 120],
        [3, 200],
      ] as const) {
        await pool.query(`UPDATE order_placed_queue SET due_at = now() WHERE state = 'ready'`);
        const [failing] = await patient.claim(1);
        expect(failing?.attempt).toBe(attempt);
        expect(await patient.fail(failing!.id, failing!.token, 'later')).toBe(false);
        const { rows: due } = await pool.query<{ seconds: number }>(
          'SELECT extract(epoch FROM due_at - now())::float8 AS seconds FROM order_placed_queue WHERE id = $1',
          [failing!.id]
        );
        expect(due[0]!.seconds).toBeGreaterThan(wait - 5);
        expect(due[0]!.seconds).toBeLessThanOrEqual(wait);
      }

      // A prune deletes the messages done past the retention, a batch at a
      // time, and neither one done within it nor a dead one.
      await pool.query('DELETE FROM order_placed_queue');
      for (let i = 0; i < 4; i++) await enqueue(pool, def, message);
      const claims = await queue.claim(4);
      for (const c of claims.slice(0, 2)) {
        await pool.query(`UPDATE order_placed_queue SET state = 'done', updated_at = now() - interval '2 hours' WHERE id = $1`, [c.id]);
      }
      await queue.complete(claims[2]!.id, claims[2]!.token);
      await pool.query(`UPDATE order_placed_queue SET state = 'dead', updated_at = now() - interval '30 days' WHERE id = $1`, [claims[3]!.id]);
      expect(await queue.prune(1)).toBe(1);
      expect(await queue.prune(1)).toBe(1);
      expect(await queue.prune(1)).toBe(0);
      const { rows: left } = await pool.query<{ state: string }>('SELECT state FROM order_placed_queue ORDER BY state');
      expect(left.map(r => r.state)).toEqual(['dead', 'done']);

      // A queue that keeps no done message deletes it as it completes.
      const none: QueueDefinition<OrderPlaced> = {
        ...def,
        retainSeconds: 0,
        statements: { ...def.statements, complete: `DELETE FROM order_placed_queue\nWHERE id = $1::uuid AND claim_token = $2::uuid AND state = 'claimed'` },
      };
      const keepsNone = postgresQueue(pool, none);
      await enqueue(pool, none, message);
      const [once] = await keepsNone.claim(1);
      await keepsNone.complete(once!.id, once!.token);
      const { rowCount } = await pool.query('SELECT 1 FROM order_placed_queue WHERE id = $1', [once!.id]);
      expect(rowCount).toBe(0);
      await expect(keepsNone.complete(once!.id, once!.token)).rejects.toBeInstanceOf(ClaimLostError);
    } finally {
      await pool.end();
      await admin.query(`DROP SCHEMA ${schema} CASCADE`);
      await admin.end();
    }
  });
});

describe('a queue with a key (D80)', () => {
  /** A unique violation, as pg reports one. */
  const keyHeld = Object.assign(new Error('duplicate key value violates unique constraint "uq_order_placed_queue_claimed_key"'), { code: '23505' });

  /** A queryable that throws error on its first query and answers the rest with results. */
  function throwsOnce(error: unknown, ...results: { rows: unknown[]; rowCount: number | null }[]) {
    const queries: QueueQueryConfig[] = [];
    return {
      queries,
      query: async (config: QueueQueryConfig) => {
        queries.push(config);
        if (queries.length === 1) throw error;
        return results.shift() ?? { rows: [], rowCount: 0 };
      },
    };
  }

  const conflict = `ON CONFLICT (order_id) WHERE state = 'ready' DO NOTHING`;
  const keyed = (): QueueDefinition<OrderPlaced> => {
    const def = definition();
    return { ...def, statements: { ...def.statements, conflict } };
  };

  test("an enqueue ends with the queue's conflict clause, and one without it with its VALUES", async () => {
    const db = recorder();
    await enqueue(db, keyed(), { orderId: 'a0000000-0000-4000-8000-00000000000a', quantity: 1, shipTo: { line1: '1 Main St', city: 'Springfield' } });
    expect(db.queries[0]!.text).toEndWith(`VALUES ($1, $2, $3, $4) ${conflict}`);
    const plain = recorder();
    await enqueue(plain, definition(), { orderId: 'a0000000-0000-4000-8000-00000000000a', quantity: 1, shipTo: { line1: '1 Main St', city: 'Springfield' } });
    expect(plain.queries[0]!.text).toEndWith('VALUES ($1, $2, $3, $4)');
  });

  test('a claim that meets a key another claim holds takes nothing, and the next claims again', async () => {
    // The expire, then the claim that meets the key.
    const pool = {
      queries: [] as QueueQueryConfig[],
      query: async (config: QueueQueryConfig) => {
        pool.queries.push(config);
        if (config.text === keyed().statements.claim) throw keyHeld;
        return { rows: [], rowCount: 0 };
      },
    };
    expect(await postgresQueue(pool, keyed()).claim(5)).toEqual([]);
    expect(pool.queries.map(q => q.text)).toEqual([keyed().statements.expire, keyed().statements.claim]);
    // Another error still fails the claim.
    const broken = {
      query: async () => {
        throw new Error('the database is down');
      },
    };
    await expect(postgresQueue(broken, keyed()).claim(5)).rejects.toThrow('claim on order_placed_queue: the database is down');
  });

  test('a fail or a release that meets a concurrent enqueue of its key runs once more', async () => {
    const failing = throwsOnce(keyHeld, { rows: [{ state: 'done' }], rowCount: 1 });
    expect(await postgresQueue(failing, keyed()).fail('id', 'token', 'it broke')).toBe(false);
    expect(failing.queries).toHaveLength(2);
    const releasing = throwsOnce(keyHeld, { rows: [], rowCount: 1 });
    await postgresQueue(releasing, keyed()).release('id', 'token');
    expect(releasing.queries).toHaveLength(2);
    // Twice is a failure the worker logs.
    const twice = {
      query: async () => {
        throw keyHeld;
      },
    };
    await expect(postgresQueue(twice, keyed()).release('id', 'token')).rejects.toThrow('duplicate key value');
  });
});
