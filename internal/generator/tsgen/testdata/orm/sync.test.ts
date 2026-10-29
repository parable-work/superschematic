// The writes of fixture-sync-db on Postgres (D89): a bank sync's upsertMany,
// its result and its update only if changed, under a trigger that enqueues
// on every write; upsertOneResult; findMany without its count; a
// transaction's isolation, read only and savepoints; a raw query read
// through a table's definition; and in on a time and a boolean.
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { NotFoundError } from '@superschematic/http-runtime/postgres';
import { createOrm, lineItemTable, type FixtureSyncDbOrm, type LineItemCreate, type LineItemRow } from './orm';
import { alice, database, dsn, recording, type TestDatabase } from './harness';

let db: TestDatabase;
let orm: FixtureSyncDbOrm;
let accountId: string;

beforeAll(async () => {
  db = await database('ts_orm_sync');
  orm = createOrm(db.pool).withUser(alice);
  accountId = (await orm.bankAccount.createOne({ code: 'chk', name: 'Checking' })).id;
});

afterAll(async () => {
  await db?.drop();
});

/** The messages the trigger enqueued, and none after clear. */
async function messages(): Promise<number> {
  const { rows } = await db.pool.query('SELECT count(*)::int AS n FROM line_item_written_queue');
  return rows[0].n as number;
}

async function clear(): Promise<void> {
  await db.pool.query('DELETE FROM line_item_written_queue');
}

/** n lines of a sync, keyed by the bank's id, prefix and i. */
function lines(prefix: string, n: number): LineItemCreate[] {
  return Array.from({ length: n }, (_, i) => ({
    plaidTransactionId: `${prefix}-${i}`,
    accountId,
    reference: `${prefix}-ref-${i}`,
    name: `Line ${i}`,
    amount: (-100 - i) as never,
    postedAt: new Date(Date.UTC(2026, 9, 1 + (i % 28))) as never,
    tags: [],
  }));
}

const fields = ['name', 'amount', 'postedAt'] as const;

describe.skipIf(dsn === '')('a bank sync', () => {
  test('a sync of 500 lines is one statement, which reports each line inserted, then updated or unchanged, and an unchanged line keeps its stamp and enqueues nothing', async () => {
    const { pool, statements } = recording(db.pool);
    const sync = createOrm(pool).withUser(alice);
    const batch = lines('txn', 500);
    await clear();
    const first = await sync.lineItem.upsertMany(batch, ['plaidTransactionId'], fields, { onlyIfChanged: true });
    expect(statements.filter(s => s.startsWith('INSERT'))).toHaveLength(1);
    expect(first.map(r => r.status)).toEqual(batch.map(() => 'inserted'));
    expect(first.map(r => r.row.plaidTransactionId)).toEqual(batch.map(l => l.plaidTransactionId));
    expect(await messages()).toBe(500);

    // A sync again, three lines of which the bank changed.
    await clear();
    statements.length = 0;
    const changed = (i: number) => i % 200 === 7;
    const again = batch.map((l, i) => (changed(i) ? { ...l, amount: ((l.amount as number) - 1) as never } : l));
    const second = await sync.lineItem.upsertMany(again, ['plaidTransactionId'], fields, { onlyIfChanged: true });
    expect(statements[0]).toBe('BEGIN');
    expect(statements.at(-1)).toBe('COMMIT');
    const upsert = statements.find(s => s.startsWith('INSERT'))!;
    expect(upsert).toContain('IS DISTINCT FROM (EXCLUDED."name", EXCLUDED.amount, EXCLUDED.posted_at)');
    // One statement writes the batch, and one reads back the lines it left.
    expect(statements.filter(s => s.startsWith('INSERT'))).toHaveLength(1);
    expect(statements.filter(s => s.startsWith('SELECT'))).toHaveLength(1);
    second.forEach((r, i) => {
      expect(r.row.plaidTransactionId).toBe(batch[i]!.plaidTransactionId!);
      expect(r.status).toBe(changed(i) ? 'updated' : 'unchanged');
      if (changed(i)) {
        expect(r.row.amount).toBe((batch[i]!.amount as number) - 1);
        expect(r.row.updatedAt.getTime()).toBeGreaterThanOrEqual(first[i]!.row.updatedAt.getTime());
      } else {
        expect(r.row).toEqual(first[i]!.row);
      }
    });
    expect(await messages()).toBe(3);

    // Without onlyIfChanged every line is written, and enqueues.
    await clear();
    const third = await sync.lineItem.upsertMany(again, ['plaidTransactionId'], fields);
    expect(third.every(r => r.status === 'updated')).toBe(true);
    expect(await messages()).toBe(500);
  });

  test('a batch takes a statement per batch in one transaction, a line without the key is inserted, and a failed batch rolls the others back', async () => {
    const { pool, statements } = recording(db.pool);
    const sync = createOrm(pool).withUser(alice);
    const batch = [...lines('chunk', 4), { ...lines('manual', 1)[0]!, plaidTransactionId: null }];
    const first = await sync.lineItem.upsertMany(batch, ['plaidTransactionId'], fields, { batch: 2 });
    expect(statements.filter(s => s.startsWith('INSERT'))).toHaveLength(3);
    expect(statements[0]).toBe('BEGIN');
    expect(first.map(r => r.status)).toEqual(['inserted', 'inserted', 'inserted', 'inserted', 'inserted']);
    expect(first[4]!.row.plaidTransactionId).toBeNull();
    expect(first[4]!.row.reference).toBe('manual-ref-0');
    // A line without the bank's id has no key to hold, so it is inserted
    // again, and the unique reference refuses it: nothing is written.
    const before = (await orm.lineItem.findMany(null, { limit: 1000 })).total;
    const renamed = batch.map(l => ({ ...l, name: `${l.name} again` }));
    await expect(sync.lineItem.upsertMany(renamed, ['plaidTransactionId'], fields, { batch: 2 })).rejects.toThrow(/line_item/);
    expect((await orm.lineItem.findMany(null, { limit: 1000 })).total).toBe(before);
    expect((await orm.lineItem.findOne({ plaidTransactionId: { eq: 'chunk-0' } }))!.name).toBe('Line 0');
    // The same lines by the account and reference update each.
    const byReference = await sync.lineItem.upsertMany(renamed, ['account', 'reference'], ['name']);
    expect(byReference.map(r => r.status)).toEqual(['updated', 'updated', 'updated', 'updated', 'updated']);
    expect(byReference.map(r => r.row.name)).toEqual(renamed.map(l => l.name));
    await expect(sync.lineItem.upsertMany(batch, ['plaidTransactionId'], ['id' as never])).rejects.toThrow(/not "id"/);
    await expect(sync.lineItem.upsertMany(batch, ['plaidTransactionId'], fields, { batch: 0 })).rejects.toThrow(/1 or more/);
  });

  test('upsertOneResult says what it did, and only if changed compares a location as its text and reads back a line it left', async () => {
    const { pool, statements } = recording(db.pool);
    const sync = createOrm(pool).withUser(alice);
    const input = { ...lines('one', 1)[0]!, location: { lat: 51.5, lon: -0.125 } as never, details: { merchant: 'Cafe' } };
    const update = { name: input.name, location: input.location, details: input.details };
    const inserted = await sync.lineItem.upsertOneResult(input, ['plaidTransactionId'], update, { onlyIfChanged: true });
    expect(inserted.status).toBe('inserted');
    statements.length = 0;
    const unchanged = await sync.lineItem.upsertOneResult(input, ['plaidTransactionId'], update, { onlyIfChanged: true });
    expect(unchanged).toEqual({ row: inserted.row, status: 'unchanged' });
    expect(statements.find(s => s.startsWith('INSERT'))).toContain(
      'WHERE line_item.deleted_at IS NULL AND (line_item."name", CAST(line_item.location AS POINT)::text, line_item.details) IS DISTINCT FROM ($12, CAST($13 AS POINT)::text, $14)'
    );
    expect(statements.filter(s => s.startsWith('SELECT'))).toHaveLength(1);
    const moved = await sync.lineItem.upsertOneResult(input, ['plaidTransactionId'], { ...update, location: { lat: 51.5, lon: 0 } as never }, { onlyIfChanged: true });
    expect(moved.status).toBe('updated');
    expect(moved.row.location).toEqual({ lat: 51.5, lon: 0 });
    // An update that sets no field leaves the line as it is, and so does
    // one only if changed whose fields hold their values.
    expect((await sync.lineItem.upsertOneResult(input, ['plaidTransactionId'], {})).status).toBe('unchanged');
    expect((await sync.lineItem.upsertOneResult(input, ['plaidTransactionId'], {}, { onlyIfChanged: true })).status).toBe('unchanged');
    expect((await sync.lineItem.upsertOne(input, ['plaidTransactionId'], { details: null }, { onlyIfChanged: true })).details).toBeNull();
    // A deleted line holds no key of a live index, so the upsert inserts.
    await orm.lineItem.deleteOne(moved.row.id);
    const after = await sync.lineItem.upsertOneResult(input, ['plaidTransactionId'], update, { onlyIfChanged: true });
    expect(after.status).toBe('inserted');
    expect(after.row.id).not.toBe(moved.row.id);
    // The primary key holds a deleted line too: an upsert of it fails.
    await expect(sync.lineItem.upsertOneResult({ ...input, id: moved.row.id }, ['id'], update, { onlyIfChanged: true })).rejects.toBeInstanceOf(NotFoundError);
  });

  test('findMany with count false reads the rows without counting them, and in on a time and a boolean keeps the rows that hold one', async () => {
    const { pool, statements } = recording(db.pool);
    const sync = createOrm(pool).withUser(alice);
    await sync.lineItem.upsertMany(lines('find', 3), ['plaidTransactionId'], fields);
    const posted = [new Date(Date.UTC(2026, 9, 1)), new Date(Date.UTC(2026, 9, 3))];
    statements.length = 0;
    const found = await sync.lineItem.findMany({ plaidTransactionId: { in: ['find-0', 'find-1', 'find-2'] }, postedAt: { in: posted as never }, pending: { in: [false] } }, { count: false });
    expect(found.total).toBeUndefined();
    expect(found.rows.map(r => r.plaidTransactionId).sort()).toEqual(['find-0', 'find-2']);
    expect(statements).toHaveLength(1);
    expect(statements[0]).toContain('line_item.posted_at IN ($4, $5) AND line_item.pending IN ($6)');
    expect((await sync.lineItem.findMany({ pending: { in: [true] } })).total).toBe(0);
  });

  test('a transaction begins at its isolation, read only or not, and a nested one is a savepoint a throw rolls back to', async () => {
    const { pool, statements } = recording(db.pool);
    const sync = createOrm(pool).withUser(alice);
    const level = await sync.transaction(async tx => (await tx.query<{ level: string }>('SHOW transaction_isolation')).rows[0]!, { isolation: 'serializable', readOnly: true });
    expect(level).toEqual({ transaction_isolation: 'serializable' } as never);
    expect(statements[0]).toBe('BEGIN ISOLATION LEVEL SERIALIZABLE READ ONLY');
    await expect(sync.transaction(tx => tx.bankAccount.createOne({ code: 'read-only', name: 'Nope' }), { readOnly: true })).rejects.toThrow(/read-only/);
    await expect(sync.transaction(async () => undefined, { isolation: 'snapshot' as never })).rejects.toThrow(/isolation/);
    statements.length = 0;
    await sync.transaction(async tx => {
      await tx.bankAccount.createOne({ code: 'kept', name: 'Kept' });
      await expect(
        tx.transaction(async inner => {
          await inner.bankAccount.createOne({ code: 'dropped', name: 'Dropped' });
          await inner.transaction(async innermost => {
            await innermost.bankAccount.createOne({ code: 'deeper', name: 'Deeper' });
          });
          await inner.bankAccount.createOne({ code: 'dropped', name: 'Again' });
        })
      ).rejects.toThrow(/duplicate key/);
      // The transaction goes on after the savepoint's failed statement.
      await tx.bankAccount.createOne({ code: 'after', name: 'After' });
    });
    expect(statements.filter(s => /SAVEPOINT/.test(s))).toEqual([
      'SAVEPOINT sp_1',
      'SAVEPOINT sp_2',
      'RELEASE SAVEPOINT sp_2',
      'ROLLBACK TO SAVEPOINT sp_1',
      'RELEASE SAVEPOINT sp_1',
    ]);
    const codes = (await orm.bankAccount.findMany({ code: { in: ['kept', 'dropped', 'deeper', 'after'] } })).rows.map(r => r.code).sort();
    expect(codes).toEqual(['after', 'kept']);
  });

  test('a raw query reads its rows through a table definition as the repository does, on the ORM and in a transaction', async () => {
    await orm.lineItem.upsertMany(lines('raw', 2), ['plaidTransactionId'], fields);
    const want = await orm.lineItem.findOne({ plaidTransactionId: { eq: 'raw-1' } });
    const read = await orm.query<LineItemRow>('SELECT * FROM line_item WHERE plaid_transaction_id = $1', ['raw-1'], { decode: lineItemTable });
    expect(read.rows).toEqual([want!]);
    const some = await orm.transaction(tx => tx.query<Pick<LineItemRow, 'id' | 'amount'>>('SELECT id, amount FROM line_item WHERE plaid_transaction_id = $1', ['raw-0'], { decode: lineItemTable }));
    expect(some.rows[0]!.id).toMatch(/^[0-9A-Za-z]{1,22}$/);
    expect(Object.keys(some.rows[0]!).sort()).toEqual(['amount', 'id']);
    // Without decode a raw row is pg's.
    const raw = await orm.query<{ id: string }>('SELECT id FROM line_item WHERE plaid_transaction_id = $1', ['raw-0']);
    expect(raw.rows[0]!.id).toMatch(/^[0-9a-f-]{36}$/);
    expect(await orm.exec('UPDATE line_item SET pending = true WHERE plaid_transaction_id = $1', ['raw-0'])).toBe(1);
  });
});
