import { afterAll, describe, expect, test } from 'bun:test';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import type { SqlDatabase, SqlPreparedStatement, SqlValue } from './d1.js';
import { audienceConditions } from './orm.js';
import { sqliteOrmPool } from './orm-sqlite.js';
import { openSqlite } from './sqlite.js';

/*
The repositories' pool over a SqlDatabase (D182): how it binds a value as
SQLite stores it, runs one statement or one transaction at a time, runs a
transaction as BEGIN IMMEDIATE on openSqlite's connection and as its
statements alone on D1, and the audience condition in SQLite's words.
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
