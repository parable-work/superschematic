import { afterEach, describe, expect, test } from 'bun:test';
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import * as d1 from './d1';
import { UNREADABLE, type QueueDefinition, type QueueStatements } from './queue-core';
import { enqueueSqlite, enqueueStatement, sqliteColumnValue, sqliteMemberValue, sqliteQueue, utcTimestamp } from './queue-sqlite';
import { ClaimLostError, openSqlite, type SqliteDatabase } from './sqlite';

/** A message of every kind of column a queue's message writes on SQLite. */
interface OrderPlaced {
  orderId: string;
  placedAt?: Date | string;
  priority?: string;
  quantity: number;
  gift?: boolean | null;
  wait?: string | null;
  note?: string | null;
  shipTo: { line1: string; city: string };
  tags?: string[];
  seenAt?: (Date | string)[];
}

const NOW = "strftime('%Y-%m-%dT%H:%M:%fZ', 'now')";
const IN = (seconds: string): string => `strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '+' || (${seconds}) || ' seconds')`;
const UUID =
  "lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (random() & 3), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))";

/**
 * The statements sqlutil.SQLiteQueueStatements writes for a table and its
 * columns, as a types package's queues.ts holds them.
 */
function statements(table: string, columns: string[], keepDone: boolean): QueueStatements {
  const claimed = `WHERE id = ?1 AND claim_token = ?2 AND state = 'claimed'`;
  return {
    insert: `INSERT INTO ${table}`,
    expire: `UPDATE ${table} SET state = 'dead', claim_token = NULL, claim_expires_at = NULL,
  last_error = 'the claim of its last attempt expired before its handler finished', updated_at = ${NOW}
WHERE state = 'claimed' AND claim_expires_at <= ${NOW} AND attempts > ?1`,
    claim: `UPDATE ${table} SET state = 'claimed', attempts = attempts + 1, claim_token = ${UUID},
  claim_expires_at = ${IN('?2')}, updated_at = ${NOW}
WHERE id IN (
  SELECT id FROM ${table}
  WHERE (state = 'ready' AND due_at <= ${NOW}) OR (state = 'claimed' AND claim_expires_at <= ${NOW})
  ORDER BY due_at, id
  LIMIT ?1
)
RETURNING id, claim_token, attempts${columns.map(c => `, ${c}`).join('')}`,
    complete: keepDone
      ? `UPDATE ${table} SET state = 'done', claim_token = NULL, claim_expires_at = NULL, updated_at = ${NOW}\n${claimed}`
      : `DELETE FROM ${table}\n${claimed}`,
    fail: `UPDATE ${table} SET
  state = CASE WHEN attempts > ?3 THEN 'dead' ELSE 'ready' END,
  due_at = CASE WHEN attempts > ?3 THEN due_at ELSE ${IN('COALESCE(MIN(?4 * (1 << MIN(attempts - 1, 40)), ?6), ?4 * (1 << MIN(attempts - 1, 40)))')} END,
  last_error = ?5, claim_token = NULL, claim_expires_at = NULL, updated_at = ${NOW}
${claimed}
RETURNING state`,
    extend: `UPDATE ${table} SET claim_expires_at = ${IN('?3')}, updated_at = ${NOW}\n${claimed}`,
    release: `UPDATE ${table} SET state = 'ready', attempts = MAX(attempts - 1, 0), claim_token = NULL, claim_expires_at = NULL, updated_at = ${NOW}\n${claimed}`,
    prune: `DELETE FROM ${table} WHERE id IN (
  SELECT id FROM ${table}
  WHERE state = 'done' AND updated_at <= strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '-' || (?1) || ' seconds')
  ORDER BY updated_at
  LIMIT ?2
)`,
    due: `SELECT 1 FROM ${table}
WHERE (state = 'ready' AND due_at <= ${NOW}) OR (state = 'claimed' AND claim_expires_at <= ${NOW})
LIMIT 1`,
  };
}

/** The table of a queue on SQLite, as the SQLite model writes it from sqlgen's (D27). */
function createTable(table: string, columns: string): string {
  return `CREATE TABLE ${table} (
  id TEXT DEFAULT (${UUID}) NOT NULL PRIMARY KEY,
  ${columns}
  state TEXT DEFAULT 'ready' NOT NULL,
  attempts INTEGER DEFAULT 0 NOT NULL,
  due_at TEXT DEFAULT (${NOW}) NOT NULL,
  claim_expires_at TEXT,
  claim_token TEXT,
  last_error TEXT,
  created_at TEXT DEFAULT (${NOW}) NOT NULL,
  updated_at TEXT DEFAULT (${NOW}) NOT NULL
)`;
}

const COLUMNS = ['order_id', 'placed_at', 'priority', 'quantity', 'gift', 'wait', 'note', 'ship_to', 'tags', 'seen_at'];

const orderPlaced: QueueDefinition<OrderPlaced> = {
  name: 'OrderPlaced',
  table: 'order_placed_queue',
  retries: 3,
  backoffSeconds: 60,
  maxBackoffSeconds: 200,
  leaseSeconds: 120,
  retainSeconds: 3600,
  fields: [
    { name: 'orderId', column: 'order_id', uuid: true },
    { name: 'placedAt', column: 'placed_at', timestamp: true },
    { name: 'priority', column: 'priority', default: 'low' },
    { name: 'quantity', column: 'quantity' },
    { name: 'gift', column: 'gift', boolean: true },
    { name: 'wait', column: 'wait', interval: true },
    { name: 'note', column: 'note' },
    { name: 'shipTo', column: 'ship_to', json: true },
    { name: 'tags', column: 'tags', list: true },
    { name: 'seenAt', column: 'seen_at', list: true, timestamp: true },
  ],
  statements: statements('order_placed_queue', COLUMNS, true),
  sqliteStatements: statements('order_placed_queue', COLUMNS, true),
};

const ping: QueueDefinition<{ sentAt?: string }> = {
  name: 'Ping',
  table: 'ping_queue',
  retries: 5,
  backoffSeconds: 30,
  maxBackoffSeconds: 3600,
  leaseSeconds: 60,
  retainSeconds: 0,
  fields: [{ name: 'sentAt', column: 'sent_at', timestamp: true }],
  statements: statements('ping_queue', ['sent_at'], false),
  sqliteStatements: statements('ping_queue', ['sent_at'], false),
};

let dir = '';
let open: SqliteDatabase[] = [];

/** A new SQLite file holding the queues' tables and an orders table, opened twice: two connections, as two processes would. */
function database(): [SqliteDatabase, SqliteDatabase] {
  dir = mkdtempSync(join(tmpdir(), 'queue-sqlite-'));
  const path = join(dir, 'queue.sqlite');
  const db = openSqlite(path);
  const other = openSqlite(path);
  open.push(db, other);
  const setup = openSqlite(path);
  for (const ddl of [
    createTable(
      'order_placed_queue',
      `order_id TEXT NOT NULL,
  placed_at TEXT DEFAULT (${NOW}) NOT NULL,
  priority TEXT NOT NULL,
  quantity INTEGER NOT NULL,
  gift INTEGER,
  wait TEXT,
  note TEXT,
  ship_to TEXT NOT NULL,
  tags TEXT DEFAULT '[]' NOT NULL,
  seen_at TEXT DEFAULT '[]' NOT NULL,`
    ),
    createTable('ping_queue', 'sent_at TEXT,'),
    'CREATE TABLE orders (id TEXT NOT NULL PRIMARY KEY)',
  ]) {
    setup.prepare(ddl).run();
  }
  setup.close();
  return [db, other];
}

afterEach(() => {
  for (const db of open) db.close();
  open = [];
  if (dir !== '') rmSync(dir, { recursive: true, force: true });
  dir = '';
});

/** A message's row: its state, attempts and last error, and the seconds until it is due. */
async function row(db: SqliteDatabase, id: string): Promise<{ state: string; attempts: number; last_error: string | null; due_in: number }> {
  const r = await db
    .prepare(`SELECT state, attempts, last_error, (julianday(due_at) - julianday('now')) * 86400 AS due_in FROM order_placed_queue WHERE id = ?1`)
    .bind(id)
    .first<{ state: string; attempts: number; last_error: string | null; due_in: number }>();
  if (r === null) throw new Error(`no message ${id}`);
  return r;
}

const MESSAGE: OrderPlaced = {
  orderId: '6F1C2F2E-7D1A-4B4A-9D43-3C7A1B2C3D4E',
  placedAt: '2026-10-11T09:15:00.123456+02:00',
  quantity: 9007199254740991,
  gift: false,
  wait: '90m',
  shipTo: { line1: '1 Main St', city: 'Springfield' },
  tags: ['a', 'b "c"', 'x,y'],
  seenAt: [new Date('2026-10-09T12:30:15.250Z')],
};

describe('the queue client on SQLite', () => {
  test('a message enqueued in a batch commits with its writes, or not at all', async () => {
    const [db] = database();
    await db.batch([db.prepare('INSERT INTO orders (id) VALUES (?1)').bind('one'), enqueueStatement(db, orderPlaced, MESSAGE)]);
    await expect(
      db.batch([db.prepare('INSERT INTO orders (id) VALUES (?1)').bind('one'), enqueueStatement(db, orderPlaced, { ...MESSAGE, orderId: '0c7a0e14-6a43-4f0e-8f1b-5d6c7e8f9a0b' })])
    ).rejects.toThrow();
    const columns = await db.prepare('SELECT order_id, placed_at, priority, quantity, gift, wait, note, ship_to, tags, seen_at FROM order_placed_queue').raw();
    expect(columns).toEqual([
      [
        '6f1c2f2e-7d1a-4b4a-9d43-3c7a1b2c3d4e',
        '2026-10-11T07:15:00.123456Z',
        'low',
        9007199254740991,
        0,
        '1h30m0s',
        null,
        '{"line1":"1 Main St","city":"Springfield"}',
        '["a","b \\"c\\"","x,y"]',
        '["2026-10-09T12:30:15.250Z"]',
      ],
    ]);
  });

  test('a claim reads each field as the TypeScript types hold it, and two connections claim different messages', async () => {
    const [db, other] = database();
    await enqueueSqlite(db, orderPlaced, { ...MESSAGE, gift: true, priority: 'high', note: 'ring twice' });
    await enqueueSqlite(db, orderPlaced, { orderId: '0c7a0e14-6a43-4f0e-8f1b-5d6c7e8f9a0b', quantity: -3, shipTo: { line1: '2 Elm St', city: 'Shelbyville' } });
    const queue = sqliteQueue(db, orderPlaced);
    const first = await queue.claim(1);
    const second = await sqliteQueue(other, orderPlaced).claim(5);
    expect(first.length).toBe(1);
    expect(second.length).toBe(1);
    expect(first[0]!.id).not.toBe(second[0]!.id);
    const byOrder = new Map([...first, ...second].map(c => [c.message.orderId, c]));
    const full = byOrder.get('6f1c2f2e-7d1a-4b4a-9d43-3c7a1b2c3d4e')!;
    expect(full.attempt).toBe(1);
    expect(full.message).toEqual({
      orderId: '6f1c2f2e-7d1a-4b4a-9d43-3c7a1b2c3d4e',
      placedAt: new Date('2026-10-11T07:15:00.123Z'),
      priority: 'high',
      quantity: 9007199254740991,
      gift: true,
      wait: '1h30m0s',
      note: 'ring twice',
      shipTo: { line1: '1 Main St', city: 'Springfield' },
      tags: ['a', 'b "c"', 'x,y'],
      seenAt: [new Date('2026-10-09T12:30:15.250Z')],
    });
    const unset = byOrder.get('0c7a0e14-6a43-4f0e-8f1b-5d6c7e8f9a0b')!;
    expect(unset.message.priority).toBe('low');
    expect(unset.message.tags).toEqual([]);
    expect('gift' in unset.message).toBe(false);
    expect(await queue.claim(5)).toEqual([]);
  });

  test('a failure waits the backoff doubled for each attempt, up to the maximum, until the message is dead', async () => {
    const [db] = database();
    await enqueueSqlite(db, orderPlaced, MESSAGE);
    const queue = sqliteQueue(db, orderPlaced);
    for (const [attempt, wait] of [
      [1, 60],
      [2, 120],
      [3, 200],
    ] as const) {
      const [claim] = await queue.claim(1);
      expect(claim!.attempt).toBe(attempt);
      expect(await queue.fail(claim!.id, claim!.token, `try ${attempt}`)).toBe(false);
      const r = await row(db, claim!.id);
      expect(r.state).toBe('ready');
      expect(r.last_error).toBe(`try ${attempt}`);
      expect(r.due_in).toBeGreaterThan(wait - 5);
      expect(r.due_in).toBeLessThanOrEqual(wait + 0.01);
      expect(await queue.claim(1)).toEqual([]);
      await expect(queue.complete(claim!.id, claim!.token)).rejects.toBeInstanceOf(ClaimLostError);
      await db.prepare(`UPDATE order_placed_queue SET due_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '-1 seconds')`).run();
    }
    const [last] = await queue.claim(1);
    expect(await queue.fail(last!.id, last!.token, 'the last')).toBe(true);
    expect((await row(db, last!.id)).state).toBe('dead');
    expect(await queue.claim(1)).toEqual([]);
  });

  test('extend, release and complete change a message while its token claims it, and an expired claim is claimed again', async () => {
    const [db] = database();
    await enqueueSqlite(db, orderPlaced, MESSAGE);
    const queue = sqliteQueue(db, orderPlaced);
    const [claim] = await queue.claim(1, 10);
    await queue.extend(claim!.id, claim!.token, 600);
    const { expires } = (await db
      .prepare(`SELECT (julianday(claim_expires_at) - julianday('now')) * 86400 AS expires FROM order_placed_queue`)
      .first<{ expires: number }>())!;
    expect(expires).toBeGreaterThan(590);
    await queue.release(claim!.id, claim!.token);
    expect((await row(db, claim!.id)).attempts).toBe(0);
    await expect(queue.release(claim!.id, claim!.token)).rejects.toBeInstanceOf(ClaimLostError);

    const [again] = await queue.claim(1);
    expect(again!.token).not.toBe(claim!.token);
    await db.prepare(`UPDATE order_placed_queue SET claim_expires_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '-1 seconds')`).run();
    const [taken] = await queue.claim(1);
    expect(taken!.id).toBe(again!.id);
    expect(taken!.attempt).toBe(2);
    await expect(queue.complete(again!.id, again!.token)).rejects.toBeInstanceOf(ClaimLostError);
    await queue.complete(taken!.id, taken!.token);
    expect((await row(db, taken!.id)).state).toBe('done');
  });

  test("a claim marks dead a message whose last attempt's claim expired", async () => {
    const [db] = database();
    await enqueueSqlite(db, orderPlaced, MESSAGE);
    await db.prepare(`UPDATE order_placed_queue SET state = 'claimed', attempts = 4, claim_token = 'x', claim_expires_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '-1 seconds')`).run();
    expect(await sqliteQueue(db, orderPlaced).claim(1)).toEqual([]);
    const r = await db.prepare('SELECT state, last_error FROM order_placed_queue').first<{ state: string; last_error: string }>();
    expect(r).toEqual({ state: 'dead', last_error: 'the claim of its last attempt expired before its handler finished' });
  });

  test('a prune deletes the done messages past the retention a batch at a time, never a dead one', async () => {
    const [db] = database();
    const queue = sqliteQueue(db, orderPlaced);
    for (let i = 0; i < 7; i++) await enqueueSqlite(db, orderPlaced, MESSAGE);
    const claims = await queue.claim(7);
    for (const c of claims) await queue.complete(c.id, c.token);
    await db.prepare(`UPDATE order_placed_queue SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '-2 hours')`).run();
    await db.prepare(`UPDATE order_placed_queue SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '-1 minutes') WHERE id = ?1`).bind(claims[0]!.id).run();
    await db.prepare(`UPDATE order_placed_queue SET state = 'dead', updated_at = '2000-01-01T00:00:00.000Z' WHERE id = ?1`).bind(claims[1]!.id).run();
    expect(await queue.prune(3)).toBe(3);
    expect(await queue.prune(3)).toBe(2);
    expect(await queue.prune(3)).toBe(0);
    const left = await db.prepare('SELECT id, state FROM order_placed_queue ORDER BY state').all<{ id: string; state: string }>();
    expect(left.results).toEqual([
      { id: claims[1]!.id, state: 'dead' },
      { id: claims[0]!.id, state: 'done' },
    ]);
  });

  test('a queue that keeps no done message deletes it as it completes it', async () => {
    const [db] = database();
    await enqueueSqlite(db, ping, {});
    const queue = sqliteQueue(db, ping);
    const [claim] = await queue.claim(1);
    expect(claim!.message).toEqual({});
    await queue.complete(claim!.id, claim!.token);
    expect(await db.prepare('SELECT COUNT(*) AS n FROM ping_queue').first('n')).toBe(0);
  });

  test('a row the claim cannot read fails alone, and the others reach their handlers', async () => {
    const [db] = database();
    await enqueueSqlite(db, orderPlaced, MESSAGE);
    await db.prepare(`INSERT INTO order_placed_queue (order_id, priority, quantity, ship_to) VALUES ('0c7a0e14-6a43-4f0e-8f1b-5d6c7e8f9a0b', 'low', 1, '{not json')`).run();
    const claims = await sqliteQueue(db, orderPlaced).claim(5);
    expect(claims.map(c => c.message.orderId)).toEqual(['6f1c2f2e-7d1a-4b4a-9d43-3c7a1b2c3d4e']);
    const bad = await db
      .prepare(`SELECT state, attempts, last_error FROM order_placed_queue WHERE order_id = '0c7a0e14-6a43-4f0e-8f1b-5d6c7e8f9a0b'`)
      .first<{ state: string; attempts: number; last_error: string }>();
    expect(bad!.state).toBe('ready');
    expect(bad!.attempts).toBe(1);
    expect(bad!.last_error.startsWith(`${UNREADABLE}: `)).toBe(true);
  });

  test('a base62 UUID goes hyphenated, as Go and Rust write it, and text that is no UUID is refused', async () => {
    const [db] = database();
    await enqueueSqlite(db, orderPlaced, { ...MESSAGE, orderId: '3Nf1lmBDcdDKfsC9ccR4DG' });
    expect(await db.prepare('SELECT order_id FROM order_placed_queue').first('order_id')).toBe('6f1c2f2e-7d1a-4b4a-9d43-3c7a1b2c3d4e');
    expect(() => enqueueStatement(db, orderPlaced, { ...MESSAGE, orderId: 'not a uuid' })).toThrow(/is no UUID/);
  });

  test("a NULL maximum backoff caps nothing, as Postgres's LEAST ignores it", async () => {
    const [db] = database();
    await enqueueSqlite(db, orderPlaced, MESSAGE);
    const queue = sqliteQueue(db, { ...orderPlaced, maxBackoffSeconds: null as unknown as number });
    const [claim] = await queue.claim(1);
    expect(await queue.fail(claim!.id, claim!.token, 'once')).toBe(false);
    const r = await row(db, claim!.id);
    expect(r.state).toBe('ready');
    expect(r.due_in).toBeGreaterThan(55);
  });

  test("a server's read-then-enqueue batches run beside a polling worker in another process, never busy", async () => {
    const [db] = database();
    const file = (db as unknown as { path: string }).path;
    const stop = join(dir, 'stop');
    const script = join(dir, 'worker.ts');
    writeFileSync(
      script,
      `import { openSqlite, sqliteQueue } from ${JSON.stringify(join(import.meta.dir, 'sqlite.ts'))};
import { existsSync } from 'node:fs';
const definition = JSON.parse(process.argv[3]);
const db = openSqlite(process.argv[2]);
const queue = sqliteQueue(db, definition);
let done = 0;
while (!existsSync(process.argv[4])) {
  for (const claim of await queue.claim(10)) {
    await queue.complete(claim.id, claim.token);
    done++;
  }
  await Bun.sleep(1);
}
console.log(done);
db.close();
`
    );
    const worker = Bun.spawn(['bun', script, file, JSON.stringify(orderPlaced), stop], { stdout: 'pipe', stderr: 'pipe' });
    const total = 300;
    for (let i = 0; i < total; i++) {
      await db.batch([db.prepare('SELECT COUNT(*) AS n FROM orders'), db.prepare('INSERT INTO orders (id) VALUES (?1)').bind(`o${i}`), enqueueStatement(db, orderPlaced, MESSAGE)]);
    }
    for (let i = 0; i < 1000 && (await db.prepare(`SELECT COUNT(*) AS n FROM order_placed_queue WHERE state = 'done'`).first('n')) !== total; i++) {
      await Bun.sleep(10);
    }
    writeFileSync(stop, '');
    const [code, out, err] = await Promise.all([worker.exited, new Response(worker.stdout).text(), new Response(worker.stderr).text()]);
    expect(err).toBe('');
    expect(code).toBe(0);
    expect(Number(out.trim())).toBe(total);
  });

  test("a batch that reads before it writes waits for another process's write lock, never failing on a stale snapshot", async () => {
    const [db] = database();
    const file = (db as unknown as { path: string }).path;
    const locked = join(dir, 'locked');
    const script = join(dir, 'writer.ts');
    // Another process holds the write lock with a write pending for 300 ms,
    // as a worker's claim would.
    writeFileSync(
      script,
      `import { Database } from 'bun:sqlite';
import { writeFileSync } from 'node:fs';
const db = new Database(process.argv[2]);
db.run('PRAGMA busy_timeout = 5000');
db.run('BEGIN IMMEDIATE');
db.run("INSERT INTO orders (id) VALUES ('other')");
writeFileSync(process.argv[3], '');
Bun.sleepSync(300);
db.run('COMMIT');
db.close();
`
    );
    const writer = Bun.spawn(['bun', script, file, locked], { stderr: 'pipe' });
    for (let i = 0; i < 500 && !(await Bun.file(locked).exists()); i++) await Bun.sleep(10);
    // A deferred transaction would read a snapshot here, then fail to write
    // once the other commits (SQLITE_BUSY_SNAPSHOT); an IMMEDIATE one waits.
    await db.batch([db.prepare('SELECT COUNT(*) AS n FROM orders'), db.prepare("INSERT INTO orders (id) VALUES ('mine')"), enqueueStatement(db, orderPlaced, MESSAGE)]);
    expect(await writer.exited).toBe(0);
    expect(await db.prepare('SELECT COUNT(*) AS n FROM orders').first('n')).toBe(2);
    expect(await db.prepare("SELECT journal_mode AS m FROM pragma_journal_mode").first('m')).toBe('wal');
  });

  test('the ./d1 entry exports no claims: no worker claims on D1', () => {
    expect('sqliteQueue' in d1).toBe(false);
    expect('enqueueStatement' in d1).toBe(true);
  });

  test('a definition with no statements on SQLite is refused', () => {
    const [db] = database();
    const { sqliteStatements: _, ...postgresOnly } = orderPlaced;
    expect(() => sqliteQueue(db, postgresOnly)).toThrow(/carries no statements on SQLite/);
    expect(() => enqueueStatement(db, postgresOnly, MESSAGE)).toThrow(/carries no statements on SQLite/);
  });
});

describe('a field on SQLite', () => {
  test('a timestamp is RFC 3339 in UTC, its fraction kept', () => {
    expect(utcTimestamp('2026-10-11T09:15:00.123456+02:00')).toBe('2026-10-11T07:15:00.123456Z');
    expect(utcTimestamp('2026-10-11T00:15:00-01:30')).toBe('2026-10-11T01:45:00Z');
    expect(utcTimestamp('2026-10-11T09:15:00.5z')).toBe('2026-10-11T09:15:00.5Z');
    expect(utcTimestamp('2026-10-11T09:15:00+0200')).toBe('2026-10-11T07:15:00Z');
    expect(utcTimestamp(new Date('2026-10-11T09:15:00.5Z'))).toBe('2026-10-11T09:15:00.500Z');
    expect(() => utcTimestamp('yesterday')).toThrow(/no RFC 3339 timestamp/);
  });

  test('each mark converts its column both ways', () => {
    expect(sqliteColumnValue(true, { name: 'b', column: 'b', boolean: true })).toBe(1);
    expect(() => sqliteColumnValue('yes', { name: 'b', column: 'b', boolean: true })).toThrow(/no boolean/);
    expect(sqliteMemberValue(0, { name: 'b', column: 'b', boolean: true })).toBe(false);
    expect(sqliteColumnValue([true, false], { name: 'b', column: 'b', list: true, boolean: true })).toBe('[true,false]');
    expect(sqliteColumnValue(['A1B2C3D4-E5F6-4A7B-8C9D-0E1F2A3B4C5D'], { name: 'u', column: 'u', list: true, uuid: true })).toBe('["a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d"]');
    expect(sqliteColumnValue('1500ms', { name: 'd', column: 'd', interval: true })).toBe('1.5s');
    expect(sqliteMemberValue('250µs', { name: 'd', column: 'd', interval: true })).toBe('250us');
    expect(() => sqliteColumnValue({ lat: 1, lon: 2 }, { name: 'p', column: 'p', point: true })).toThrow(/no storage for a location/);
    expect(sqliteColumnValue(9007199254740991n, { name: 'n', column: 'n' })).toBe(9007199254740991);
    expect(() => sqliteColumnValue(9007199254740992n, { name: 'n', column: 'n' })).toThrow(/out of the range/);
  });
});

describe('a queue with a key on SQLite (D80)', () => {
  test("enqueueStatement ends with the queue's conflict clause, so one message waits per key", async () => {
    const [db] = database();
    const setup = [
      "CREATE TABLE stock_checked_queue (id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))), sku TEXT NOT NULL, quantity INTEGER NOT NULL, state TEXT NOT NULL DEFAULT 'ready')",
      "CREATE UNIQUE INDEX uq_stock_checked_queue_ready_key ON stock_checked_queue (sku) WHERE state = 'ready'",
    ];
    for (const ddl of setup) await db.prepare(ddl).run();
    const sqlite = statements('stock_checked_queue', ['sku', 'quantity'], false);
    const definition: QueueDefinition<{ sku: string; quantity: number }> = {
      name: 'StockChecked',
      table: 'stock_checked_queue',
      retries: 5,
      backoffSeconds: 30,
      maxBackoffSeconds: 3600,
      leaseSeconds: 60,
      retainSeconds: 0,
      fields: [
        { name: 'sku', column: 'sku' },
        { name: 'quantity', column: 'quantity' },
      ],
      statements: sqlite,
      sqliteStatements: { ...sqlite, conflict: "ON CONFLICT (sku) WHERE state = 'ready' DO NOTHING" },
    };
    for (const quantity of [1, 2, 3]) await db.batch([enqueueStatement(db, definition, { sku: 'SKU-1', quantity })]);
    expect(await db.prepare('SELECT sku, quantity FROM stock_checked_queue').raw()).toEqual([['SKU-1', 1]]);
  });
});
