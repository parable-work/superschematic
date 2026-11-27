import { afterAll, describe, expect, setSystemTime, test } from 'bun:test';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import type { SqlDatabase, SqlPreparedStatement, SqlValue } from './d1.js';
import { audienceConditions, type Clauses, type Orm, type OrmDefinition } from './orm.js';
import { sqliteOrm, sqliteOrmPool } from './orm-sqlite.js';
import { openSqlite } from './sqlite.js';

/*
The repositories' pool over a SqlDatabase (D182): how it binds a value as
SQLite stores it, runs one statement or one transaction at a time, runs a
transaction as BEGIN IMMEDIATE on openSqlite's connection and as its
statements alone on D1, the audience condition in SQLite's words, and a
row's times, which come from the database's clock alone.
*/

/** A SqlDatabase that records each statement and its values, as D1 would run them, answering no rows. */
function recording(): { db: SqlDatabase; ran: { sql: string; values: SqlValue[] }[] } {
  const ran: { sql: string; values: SqlValue[] }[] = [];
  const statement = (sql: string, values: SqlValue[] = []): SqlPreparedStatement =>
    ({
      bind: (...bound: SqlValue[]) => statement(sql, bound),
      all: async () => {
        ran.push({ sql, values });
        return { results: [], success: true, meta: { duration: 0, changes: 1, last_row_id: 0, changed_db: true } };
      },
    }) as unknown as SqlPreparedStatement;
  return { ran, db: { prepare: sql => statement(sql), batch: async () => [], exec: async () => ({ count: 0, duration: 0 }) } };
}

const dir = mkdtempSync(join(tmpdir(), 'orm-sqlite-test-'));
afterAll(() => rmSync(dir, { recursive: true, force: true }));

describe('sqliteOrmPool', () => {
  test('binds a time as its RFC 3339 text, a list and an object as JSON, and a bigint as a number', async () => {
    const { db, ran } = recording();
    const pool = sqliteOrmPool(db);
    const at = new Date('2026-11-26T10:00:00.123Z');
    await pool.query({ text: 'INSERT INTO t VALUES (?1, ?2, ?3, ?4, ?5, ?6)', values: [at, ['a', 'b'], { kind: 'ref' }, 42n, true, undefined] });
    expect(ran[0]!.values).toEqual(['2026-11-26T10:00:00.123Z', '["a","b"]', '{"kind":"ref"}', 42, true, null]);
  });

  test('runs a transaction on D1, which has none, as its statements alone', async () => {
    const { db, ran } = recording();
    const pool = sqliteOrmPool(db);
    await pool.transaction!('BEGIN ISOLATION LEVEL SERIALIZABLE', async client => {
      await client.query({ text: 'SAVEPOINT sp_1', values: [] });
      await client.query({ text: 'UPDATE t SET a = ?1', values: [1] });
      await client.query({ text: 'RELEASE SAVEPOINT sp_1', values: [] });
    });
    expect(ran.map(r => r.sql)).toEqual(['UPDATE t SET a = ?1']);
  });

  test("runs a transaction on openSqlite's file as BEGIN IMMEDIATE, which a throw rolls back, one at a time", async () => {
    const db = openSqlite(join(dir, 'turns.sqlite'));
    try {
      await db.exec('CREATE TABLE t (n INTEGER)');
      const pool = sqliteOrmPool(db);
      await expect(
        pool.transaction!('BEGIN', async client => {
          await client.query({ text: 'INSERT INTO t VALUES (?1)', values: [1] });
          throw new Error('undone');
        })
      ).rejects.toThrow('undone');
      expect((await pool.query({ text: 'SELECT count(*) AS n FROM t', values: [] })).rows).toEqual([{ n: 0 }]);
      // A statement made while a transaction runs waits for its end, through
      // another pool over the connection too; one made within it, through
      // the pool, runs in it.
      const order: string[] = [];
      let release!: () => void;
      const held = new Promise<void>(resolve => {
        release = resolve;
      });
      const running = pool.transaction!('BEGIN', async client => {
        await client.query({ text: 'INSERT INTO t VALUES (?1)', values: [2] });
        expect((await pool.query({ text: 'SELECT count(*) AS n FROM t', values: [] })).rows).toEqual([{ n: 1 }]);
        await held;
        order.push('transaction');
      });
      const waiting = sqliteOrmPool(db).query({ text: 'SELECT count(*) AS n FROM t', values: [] }).then(result => {
        order.push('statement');
        return result;
      });
      await new Promise(resolve => setTimeout(resolve, 20));
      expect(order).toEqual([]);
      release();
      await running;
      expect((await waiting).rows).toEqual([{ n: 1 }]);
      expect(order).toEqual(['transaction', 'statement']);
    } finally {
      db.close();
    }
  });

  test('answers a statement the rows it changed, or for a read the rows it read', async () => {
    const db = openSqlite(join(dir, 'counts.sqlite'));
    try {
      await db.exec('CREATE TABLE t (n INTEGER)');
      const pool = sqliteOrmPool(db);
      expect((await pool.query({ text: 'INSERT INTO t VALUES (1), (2), (3)', values: [] })).rowCount).toBe(3);
      expect((await pool.query({ text: 'SELECT n FROM t', values: [] })).rowCount).toBe(3);
      expect((await pool.query({ text: 'DELETE FROM t WHERE n > ?1', values: [1] })).rowCount).toBe(2);
    } finally {
      db.close();
    }
  });
});

describe('the codec on SQLite', () => {
  test("reads a duration in the canonical text SQLite holds, as Postgres's INTERVAL text", async () => {
    const { decodeValue } = await import('./pgcodec.js');
    expect(decodeValue('1h30m0s', { interval: true })).toBe('1h30m0s');
    expect(decodeValue('250ms', { interval: true })).toBe('250ms');
    expect(decodeValue('1:30:00', { interval: true })).toBe('1h30m0s');
    expect(() => decodeValue('soon', { interval: true })).toThrow();
  });
});

describe('audienceConditions on SQLite', () => {
  test("compares the caller's ids with the effective audience's JSON array through json_each", () => {
    const args: unknown[] = ['x'];
    const definition = { effective: 'note.effective_audience', agents: 'note.effective_agents', contains: 'EXISTS (SELECT 1 FROM json_each(%s) WHERE json_each.value IN (%s))', placeholder: '?%d' };
    const conditions = audienceConditions(definition, { subject: '0f8b6c4e-1b2a-4c3d-9e8f-123456789abc', kind: 'agent', actingFor: { kind: 'user', subject: 'TKbHsHDO6x9RCjbs5c2u4' } }, args, 1);
    expect(conditions).toEqual([
      "(note.effective_audience IS NULL OR EXISTS (SELECT 1 FROM json_each(note.effective_audience) WHERE json_each.value IN (?2, ?3))) AND note.effective_agents <> 'none'",
    ]);
    expect(args).toEqual(['x', '0f8b6c4e-1b2a-4c3d-9e8f-123456789abc', '0f8b6c4e-1b2a-4c3d-9e8f-123456789abc']);
  });
});

/** The clauses sqlutil writes for SQLite. */
const clauses: Clauses = {
  eq: '%s = ?%d',
  gt: '%s > ?%d',
  gte: '%s >= ?%d',
  lt: '%s < ?%d',
  lte: '%s <= ?%d',
  ilike: "%s LIKE ?%d ESCAPE '\\'",
  in: '%s IN (%s)',
  isNull: '%s IS NULL',
  isNotNull: '%s IS NOT NULL',
  search: "%s LIKE ?%d ESCAPE '\\'",
  exists: 'EXISTS (%s)',
  notExists: 'NOT EXISTS (%s)',
  set: '%s = ?%d',
  setNull: '%s = NULL',
  pageKey: 'page_key.%s',
  placeholder: '?%d',
  default: '',
  or: ' OR ',
  group: '(%s)',
  where: ' WHERE ',
  and: ' AND ',
  orderBy: ' ORDER BY ',
  orderTerm: '%s %s',
  limit: ' LIMIT %d',
  offset: ' OFFSET %d',
  setExcluded: '%s = EXCLUDED.%s',
  excluded: 'EXCLUDED.%s',
  changed: '(%s) IS DISTINCT FROM (%s)',
  never: 'FALSE',
  null: 'NULL',
  notIn: '%s NOT IN (%s)',
};

const noteColumns = '"id", "title", "created_at", "updated_at"';

/** A table with createdAt and updatedAt, its statements as sqlutil writes them on SQLite. */
const clockDefinition: OrmDefinition = {
  dialect: 'sqlite',
  clauses,
  defaultLimit: 100,
  historyActorCTE: '',
  clearHistoryActor: '',
  tables: {
    note: {
      name: 'Note',
      table: 'note',
      key: 'id',
      columns: [
        { name: 'id', column: 'id', field: 'id', qualified: 'note.id', uuid: true, key: true, auto: true, required: true },
        { name: 'title', column: 'title', field: 'title', qualified: 'note.title', required: true },
        { name: 'createdAt', column: 'created_at', field: 'createdAt', qualified: 'note.created_at', timestamp: true, audit: 'createdAt', required: true },
        { name: 'updatedAt', column: 'updated_at', field: 'updatedAt', qualified: 'note.updated_at', timestamp: true, audit: 'updatedAt', required: true },
      ],
      statements: {
        columns: noteColumns,
        qualifiedColumns: 'note.id, note.title, note.created_at, note.updated_at',
        getOne: `SELECT ${noteColumns} FROM note WHERE id = ?1`,
        getMany: `SELECT ${noteColumns} FROM note WHERE id IN (%s)`,
        live: '',
        liveWhere: '',
        liveCondition: '',
        findOne: 'SELECT %s FROM note%s%s LIMIT 1',
        findMany: 'SELECT %s FROM note%s%s%s',
        count: 'SELECT COUNT(*) FROM note%s',
        page: 'SELECT %s FROM note%s%s LIMIT %d',
        pageAfterKey: 'note.id %s %s',
        pageAfter: '(%s, note.id) %s ((SELECT %s FROM note AS page_key WHERE page_key.id = %s), %s)',
        pageOrder: ' ORDER BY %s %s',
        pageTiebreak: ', note.id %s',
        pageKeyExists: 'SELECT EXISTS (SELECT 1 FROM note WHERE note.id = ?1)',
        insert: 'INSERT INTO note (%s) VALUES (%s) RETURNING %s',
        insertMany: 'INSERT INTO note (%s) VALUES %s RETURNING %s',
        update: 'UPDATE note SET %s WHERE id = ?%d',
        returning: ` RETURNING ${noteColumns}`,
        updateMany: 'UPDATE note SET %s%s',
        updatedAt: "updated_at = strftime('%%Y-%%m-%%dT%%H:%%M:%%fZ', 'now')",
        updatedBy: 'updated_by = ?%d',
        upsert: '',
        softDelete: '',
        softDeleteMany: '',
        delete: 'DELETE FROM note WHERE id = ?1',
        deleteMany: 'DELETE FROM note%s',
      },
    },
  },
};

interface NoteRow {
  id: string;
  title: string;
  createdAt: Date;
  updatedAt: Date;
}

interface ClockRepositories {
  readonly note: {
    createOne(input: { title: string }): Promise<NoteRow>;
    getOne(id: string): Promise<NoteRow>;
    updateOne(id: string, update: { title?: string }): Promise<NoteRow>;
    updateMany(filter: { id?: { eq: string } } | null, update: { title?: string }): Promise<number>;
  };
}

describe("a row's times on SQLite", () => {
  test("an update stamps updatedAt from the database's clock, as the insert's default stamps createdAt, so a host clock behind it stamps no row earlier than its insert", async () => {
    const db = openSqlite(join(dir, 'clock.sqlite'));
    try {
      const now = "(strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))";
      const uuid = "(lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (random() & 3), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6))))";
      await db.exec(`CREATE TABLE note (id TEXT DEFAULT ${uuid} NOT NULL PRIMARY KEY, title TEXT NOT NULL, created_at TEXT DEFAULT ${now} NOT NULL, updated_at TEXT DEFAULT ${now} NOT NULL)`);
      const orm = sqliteOrm<ClockRepositories>(db, clockDefinition) as Orm<ClockRepositories>;
      // The host's clock an hour behind the database's, which SQLite reads
      // from the system, not from the host's Date.
      setSystemTime(new Date(Date.now() - 60 * 60 * 1000));
      const created = await orm.note.createOne({ title: 'Draft' });
      expect(created.updatedAt.getTime()).toBe(created.createdAt.getTime());
      const updated = await orm.note.updateOne(created.id, { title: 'Final' });
      expect(updated.updatedAt.getTime()).toBeGreaterThanOrEqual(created.createdAt.getTime());
      expect(await orm.note.updateMany({ id: { eq: created.id } }, { title: 'Filed' })).toBe(1);
      const filed = await orm.note.getOne(created.id);
      expect(filed.updatedAt.getTime()).toBeGreaterThanOrEqual(updated.updatedAt.getTime());
      // The stamp is SQLite's default's form, which compares as text in order.
      const { rows } = await sqliteOrmPool(db).query({ text: 'SELECT updated_at FROM note', values: [] });
      expect(rows[0]!.updated_at).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/);
    } finally {
      setSystemTime();
      db.close();
    }
  });
});
