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
  type QueueDefinition,
  type QueueQueryConfig,
  type QueueStatements,
} from './queue';

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
