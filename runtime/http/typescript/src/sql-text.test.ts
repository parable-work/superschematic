import { describe, expect, test } from 'bun:test';
import { postgresIdempotencyStore, type IdempotencyPgQueryable } from './idempotency.js';
import { postgresPlaceholders, quoteIdentifier, sqlitePlaceholders } from './sql-text.js';

describe('quoteIdentifier', () => {
  test('quotes a name with each double quote doubled', () => {
    expect(quoteIdentifier('user')).toBe('"user"');
    expect(quoteIdentifier('my "user"')).toBe('"my ""user"""');
    expect(quoteIdentifier('a?b $1')).toBe('"a?b $1"');
  });
});

describe('postgresPlaceholders', () => {
  test('numbers each ? in order', () => {
    expect(postgresPlaceholders('SELECT 1')).toBe('SELECT 1');
    expect(postgresPlaceholders('?, ?, ?')).toBe('$1, $2, $3');
  });

  test('leaves a ? in a quoted name or a literal alone', () => {
    const sql = `SELECT ${quoteIdentifier('a?b')}, 'c?d' FROM ${quoteIdentifier('t"?')} WHERE x = ? AND y = 'it''s ?' AND z = ?`;
    expect(postgresPlaceholders(sql)).toBe(`SELECT "a?b", 'c?d' FROM "t""?" WHERE x = $1 AND y = 'it''s ?' AND z = $2`);
  });

  test('the idempotency store on Postgres numbers no ? in a configured name', async () => {
    const texts: string[] = [];
    const pool: IdempotencyPgQueryable = {
      async query(config) {
        texts.push(config.text);
        return { rows: [], rowCount: 0 };
      },
    };
    const store = postgresIdempotencyStore(pool, {
      table: 'idem?keys',
      columns: { lookup: 'look?up', operation: 'operation', principal: 'principal', key: 'key', requestHash: 'request_hash', state: 'state', status: 'status', body: 'body', startedAt: 'started_at', expiresAt: 'expires_at' },
    });
    await store.abandon('lookup', 1);
    expect(texts).toEqual([`DELETE FROM "idem?keys" WHERE "look?up" = $1 AND "started_at" = $2 AND "state" = 'running'`]);
  });
});

describe('sqlitePlaceholders', () => {
  test('writes each $n as ?n', () => {
    expect(sqlitePlaceholders('WHERE a = $1 AND b = $12')).toBe('WHERE a = ?1 AND b = ?12');
    expect(sqlitePlaceholders('SELECT $')).toBe('SELECT $');
  });

  test('leaves a $n in a quoted name or a literal alone', () => {
    const sql = `UPDATE ${quoteIdentifier('t$1')} SET ${quoteIdentifier('c"$2')} = '$3 it''s $4' WHERE k = $1 AND v = $2`;
    expect(sqlitePlaceholders(sql)).toBe(`UPDATE "t$1" SET "c""$2" = '$3 it''s $4' WHERE k = ?1 AND v = ?2`);
  });
});
