// The repositories of fixture-ledger-db on Postgres (D79): CRUD, filters,
// search, paging, upsert, the acting user, soft delete, a transaction that
// writes rows and enqueues a message, and the value codec over a raw query.
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import {
  NoUserError,
  NotFoundError,
  PageKeyError,
  connectPostgres,
  decodeRow,
  enqueue,
  uuidFromDb,
  uuidToDb,
} from '@superschematic/http-runtime/postgres';
import { accountTable, createOrm, entryTable, type EntryRow, type FixtureLedgerDbOrm } from './orm';
import { entryPostedQueue } from './queues';
import type { EntryKind, GenericInt64, IdentityName, TemporalDate } from './types';
import { alice, aliceHyphenated, bob, database, dsn, recording, type TestDatabase } from './harness';

const name = (text: string) => text as IdentityName;
const day = (text: string) => text as TemporalDate;
const cents = (n: number) => n as GenericInt64;
const debit = 'debit' as EntryKind;
const credit = 'credit' as EntryKind;

let db: TestDatabase;
let orm: FixtureLedgerDbOrm;

beforeAll(async () => {
  db = await database('ts_orm_ledger');
  orm = createOrm(db.pool).withUser(alice);
});

afterAll(async () => {
  await db?.drop();
});

/** An account of alice's, by code. */
async function account(code: string, opened?: string) {
  return orm.account.createOne({ name: name(`Account ${code}`), code, openedOn: opened ? day(opened) : undefined });
}

describe.skipIf(dsn === '')('the ledger', () => {
  test('createOne and getOne read a row as the types hold it, its key base62 and its user stamped', async () => {
    const cash = await account('cash', '2026-10-01');
    expect(cash.id).toMatch(/^[0-9A-Za-z]{1,22}$/);
    expect(cash.createdBy).toBe(alice);
    expect(cash.updatedBy).toBe(alice);
    expect(cash.createdAt).toBeInstanceOf(Date);
    expect(cash.openedOn).toBe(day('2026-10-01'));
    const entry = await orm.entry.createOne({
      accountId: cash.id,
      amountCents: cents(-1250),
      bookedOn: day('2026-10-25'),
      tags: ['groceries', 'weekly shop'],
      metadata: { plaid: { pending: false } },
    });
    expect(entry).toMatchObject({
      accountId: cash.id,
      transferId: null,
      amountCents: -1250,
      bookedOn: '2026-10-25',
      kind: 'debit',
      memo: null,
      tags: ['groceries', 'weekly shop'],
      metadata: { plaid: { pending: false } },
      deletedAt: null,
      createdBy: alice,
    });
    expect(await orm.entry.getOne(entry.id)).toEqual(entry);
    // Either form of a key reads the row.
    expect((await orm.account.getOne(uuidToDb(cash.id) as never)).id).toBe(cash.id);
    await expect(orm.entry.getOne(uuidFromDb('00000000-0000-4000-8000-000000000000') as never)).rejects.toBeInstanceOf(NotFoundError);
  });

  test('a relation loads in a batch through getManyByIds, keyed by base62', async () => {
    const a = await account('batch-a');
    const b = await account('batch-b');
    for (const owner of [a, b, a]) {
      await orm.entry.createOne({ accountId: owner.id, amountCents: cents(100), bookedOn: day('2026-10-02'), tags: [] });
    }
    const { rows } = await orm.entry.findMany({ accountId: { in: [a.id, b.id] } });
    const accounts = await orm.account.getManyByIds(rows.map(r => r.accountId));
    expect(accounts.size).toBe(2);
    for (const row of rows) expect(accounts.get(row.accountId)?.code).toMatch(/^batch-/);
  });

  test('a write that stamps a user needs one, as the Go ORM refuses with ErrNoUserInContext', async () => {
    const nobody = createOrm(db.pool);
    await expect(nobody.account.createOne({ name: name('Nobody'), code: 'nobody' })).rejects.toBeInstanceOf(NoUserError);
    const owner = await account('no-user');
    const entry = await orm.entry.createOne({ accountId: owner.id, amountCents: cents(1), bookedOn: day('2026-10-03'), tags: [] });
    await expect(nobody.entry.deleteOne(entry.id)).rejects.toBeInstanceOf(NoUserError);
    // as(principal) acts as the principal's subject, and as no one without one.
    expect(orm.as({ subject: bob }).actingUser).toBe(bob);
    expect(orm.as(null).actingUser).toBeUndefined();
  });

  test('createMany writes one INSERT, DEFAULT for a column one row leaves unset that another sets', async () => {
    const owner = await account('many');
    const recorded = recording(db.pool);
    const rows = await createOrm(recorded.pool, alice).entry.createMany([
      { accountId: owner.id, amountCents: cents(5), bookedOn: day('2026-10-04'), tags: [], memo: 'first' },
      { accountId: owner.id, amountCents: cents(6), bookedOn: day('2026-10-04'), tags: ['x'], kind: credit },
    ]);
    expect(recorded.statements).toHaveLength(1);
    expect(recorded.statements[0]).toMatch(/^INSERT INTO entry \(/);
    expect(recorded.statements[0]).toContain('DEFAULT');
    expect(rows.map(r => [r.memo, r.kind])).toEqual([
      ['first', 'debit'],
      [null, 'credit'],
    ]);
  });

  test('the filters mirror the Go ORM: equality, in, ranges, null, a key, a relation, a reference and a search', async () => {
    const owner = await account('filters');
    const other = await account('filters-other');
    const make = (amount: number, booked: string, memo: string | null, kind = debit, transferId: string | null = null) =>
      orm.entry.createOne({ accountId: owner.id, amountCents: cents(amount), bookedOn: day(booked), memo, kind, transferId: transferId as never, tags: [] });
    await make(-500, '2026-09-01', 'Coffee at the corner');
    await make(2500, '2026-09-15', 'Salary', credit);
    await make(-1200, '2026-09-20', null, debit, other.id);
    await make(-80, '2026-10-01', 'coffee beans');
    const amounts = async (filter: Parameters<typeof orm.entry.findMany>[0]) =>
      (await orm.entry.findMany({ accountId: { eq: owner.id }, ...filter }, { orderBy: [{ field: 'amountCents' }] })).rows.map(r => r.amountCents);
    expect(await amounts({ amountCents: { lt: cents(0) } })).toEqual([-1200, -500, -80]);
    expect(await amounts({ amountCents: { between: [cents(-600), cents(0)] } })).toEqual([-500, -80]);
    expect(await amounts({ amountCents: { in: [cents(2500), cents(-80)] } })).toEqual([-80, 2500]);
    expect(await amounts({ bookedOn: { gte: day('2026-09-15'), lt: day('2026-10-01') } })).toEqual([-1200, 2500]);
    expect(await amounts({ kind: { eq: credit } })).toEqual([2500]);
    expect(await amounts({ kind: { in: [] } })).toEqual([-1200, -500, -80, 2500]);
    expect(await amounts({ memo: { isNull: true } })).toEqual([-1200]);
    expect(await amounts({ memo: { ilike: 'COFFEE' } })).toEqual([-500, -80]);
    expect(await amounts({ transferId: { eq: other.id } })).toEqual([-1200]);
    expect(await amounts({ transferId: { isNull: false } })).toEqual([-1200]);
    // The search's words each match, in any case.
    expect(await amounts({ searchText: 'coffee corner' })).toEqual([-500]);
    expect(await amounts({ searchText: '   ' })).toEqual([-1200, -500, -80, 2500]);
    // A reference: the accounts some entry transfers to.
    const transferredTo = await orm.account.findMany({ referencedByEntryTransfer: { exists: true }, code: { in: ['filters', 'filters-other'] } });
    expect(transferredTo.rows.map(r => r.code)).toEqual(['filters-other']);
  });

  test('findMany counts every row the filter finds, and reads a limit after an offset, in order', async () => {
    for (let i = 0; i < 5; i++) await account(`count-${i}`);
    const { rows, total } = await orm.account.findMany({ code: { ilike: 'count-' } }, { orderBy: [{ field: 'code', desc: true }], limit: 2, offset: 1 });
    expect(total).toBe(5);
    expect(rows.map(r => r.code)).toEqual(['count-3', 'count-2']);
    const first = await orm.account.findOne({ code: { ilike: 'count-' } }, { orderBy: [{ field: 'code' }] });
    expect(first?.code).toBe('count-0');
    expect(await orm.account.findOne({ code: { eq: 'no-such' } })).toBeNull();
  });

  test('findPage reads keyset pages by a sort field, ties broken by the key, and refuses a key that names no row', async () => {
    const owner = await account('pages');
    for (const amount of [30, 10, 20, 10, 40]) {
      await orm.entry.createOne({ accountId: owner.id, amountCents: cents(amount), bookedOn: day('2026-10-05'), tags: [] });
    }
    const filter = { accountId: { eq: owner.id } };
    const seen: number[] = [];
    let after = null;
    do {
      const page = await orm.entry.findPage(filter, { sort: { field: 'amountCents', desc: true }, limit: 2, after });
      seen.push(...page.rows.map(r => r.amountCents));
      after = page.next;
    } while (after);
    expect(seen).toEqual([40, 30, 20, 10, 10]);
    const byKey = await orm.entry.findPage(filter, { limit: 10 });
    const ids = byKey.rows.map(r => r.id);
    expect([...ids].sort((a, b) => (uuidToDb(a) < uuidToDb(b) ? -1 : 1))).toEqual(ids);
    expect(byKey.next).toBeNull();
    // A key whose row is gone places no page in an order by another field.
    const gone = await orm.entry.createOne({ accountId: owner.id, amountCents: cents(5), bookedOn: day('2026-10-05'), tags: [] });
    await orm.entry.hardDeleteOne(gone.id);
    await expect(orm.entry.findPage(filter, { sort: { field: 'amountCents' }, after: { id: gone.id } })).rejects.toBeInstanceOf(PageKeyError);
    await expect(orm.entry.findPage(filter, { after: { id: '' as never } })).rejects.toBeInstanceOf(PageKeyError);
    await expect(orm.entry.findPage(filter, { sort: { field: 'memo' as never } })).rejects.toThrow('sorts by');
  });

  test('updateOne stamps updatedAt and updatedBy, sets a column null with null, and refuses a key no live row holds', async () => {
    const owner = await account('update');
    const entry = await orm.entry.createOne({ accountId: owner.id, amountCents: cents(1), bookedOn: day('2026-10-06'), tags: [], memo: 'old' });
    const updated = await orm.withUser(bob).entry.updateOne(entry.id, { memo: null, amountCents: cents(2), tags: ['new'] });
    expect(updated.memo).toBeNull();
    expect(updated.amountCents).toBe(2);
    expect(updated.tags).toEqual(['new']);
    expect(updated.updatedBy).toBe(bob);
    expect(updated.createdBy).toBe(alice);
    // updatedAt is the ORM's clock, the insert's the database's default, so the two are not compared.
    expect(updated.updatedAt).toBeInstanceOf(Date);
    expect(await orm.entry.updateMany({ accountId: { eq: owner.id } }, { kind: credit })).toBe(1);
    // An update that sets no field still stamps updatedAt, as the Go ORM's does.
    expect((await orm.entry.updateOne(entry.id, {})).updatedBy).toBe(alice);
    await orm.entry.deleteOne(entry.id);
    await expect(orm.entry.updateOne(entry.id, { memo: 'x' })).rejects.toBeInstanceOf(NotFoundError);
  });

  test('upsertOne inserts, then updates the row that holds its unique key, and an empty update leaves it as it is', async () => {
    const owner = await account('upsert');
    const input = { accountId: owner.id, externalId: 'plaid-1', amountCents: cents(100), bookedOn: day('2026-10-07'), tags: [] };
    const inserted = await orm.entry.upsertOne(input, ['account', 'externalId'], { amountCents: cents(100) });
    const updated = await orm.entry.upsertOne({ ...input, amountCents: cents(150) }, ['account', 'externalId'], { amountCents: cents(150), memo: 'pending cleared' });
    expect(updated.id).toBe(inserted.id);
    expect(updated.amountCents).toBe(150);
    expect(updated.memo).toBe('pending cleared');
    const kept = await orm.entry.upsertOne({ ...input, amountCents: cents(999) }, ['account', 'externalId'], {});
    expect(kept).toEqual(updated);
    // The account's code is unique too.
    const again = await orm.account.upsertOne({ name: name('Renamed'), code: 'upsert' }, ['code'], { name: name('Renamed') });
    expect([again.id, again.name]).toEqual([owner.id, 'Renamed']);
    // A deleted row does not hold a live key: the partial index admits a new row,
    // and an upsert by the key of the deleted row itself is refused.
    await orm.entry.deleteOne(inserted.id);
    const fresh = await orm.entry.upsertOne(input, ['account', 'externalId'], {});
    expect(fresh.id).not.toBe(inserted.id);
    await expect(orm.entry.upsertOne({ ...input, id: inserted.id, externalId: 'plaid-2' }, ['id'], { memo: 'x' })).rejects.toBeInstanceOf(NotFoundError);
    await expect(orm.entry.upsertOne(input, ['memo'] as never, {})).rejects.toThrow('no unique key');
  });

  test('a soft delete stamps deletedAt and deletedBy, and reads leave the row out unless they include deleted rows', async () => {
    const owner = await account('soft');
    const keep = await orm.entry.createOne({ accountId: owner.id, amountCents: cents(1), bookedOn: day('2026-10-08'), tags: [] });
    const drop = await orm.entry.createOne({ accountId: owner.id, amountCents: cents(2), bookedOn: day('2026-10-08'), tags: [] });
    await orm.withUser(bob).entry.deleteOne(drop.id);
    await expect(orm.entry.getOne(drop.id)).rejects.toBeInstanceOf(NotFoundError);
    await expect(orm.entry.deleteOne(drop.id)).rejects.toBeInstanceOf(NotFoundError);
    const filter = { accountId: { eq: owner.id } };
    expect((await orm.entry.findMany(filter)).rows.map(r => r.id)).toEqual([keep.id]);
    const all = await orm.entry.findMany(filter, { includeDeleted: true, orderBy: [{ field: 'amountCents' }] });
    expect(all.rows.map(r => [r.id, r.deletedBy])).toEqual([
      [keep.id, null],
      [drop.id, bob],
    ]);
    expect(all.rows[1]!.deletedAt).toBeInstanceOf(Date);
    expect((await orm.entry.getManyByIds([keep.id, drop.id])).size).toBe(1);
    expect((await orm.entry.findPage(filter, { includeDeleted: true })).rows).toHaveLength(2);
    expect(await orm.entry.deleteMany(filter)).toBe(1);
    expect((await orm.entry.findMany(filter, { includeDeleted: true })).total).toBe(2);
    await orm.entry.hardDeleteOne(drop.id);
    expect((await orm.entry.findMany(filter, { includeDeleted: true })).total).toBe(1);
    // A table with no deletedAt removes its rows.
    const gone = await account('soft-gone');
    await orm.account.deleteOne(gone.id);
    await expect(orm.account.getOne(gone.id)).rejects.toBeInstanceOf(NotFoundError);
  });

  test('a transaction writes rows and enqueues their message together, and rolls both back when it throws', async () => {
    const owner = await account('tx');
    const queued = async () => Number((await db.pool.query('SELECT count(*) FROM entry_posted_queue')).rows[0].count);
    const before = await queued();
    const posted = await orm.transaction(async tx => {
      const entry = await tx.entry.createOne({ accountId: owner.id, amountCents: cents(-4200), bookedOn: day('2026-10-09'), tags: [] });
      await enqueue(tx, entryPostedQueue, { entryId: entry.id, amountCents: entry.amountCents });
      const { rows } = await tx.query<{ n: number }>('SELECT count(*)::int AS n FROM entry WHERE account_id = $1', [uuidToDb(owner.id)]);
      expect(rows[0]!.n).toBe(1);
      expect(await tx.exec('UPDATE entry SET memo = $1 WHERE id = $2', ['raw', uuidToDb(entry.id)])).toBe(1);
      return entry;
    });
    expect(await queued()).toBe(before + 1);
    expect((await orm.entry.getOne(posted.id)).memo).toBe('raw');
    const { rows } = await db.pool.query('SELECT entry_id::text AS id FROM entry_posted_queue WHERE entry_id = $1', [uuidToDb(posted.id)]);
    expect(rows).toHaveLength(1);
    await expect(
      orm.transaction(async tx => {
        const entry = await tx.entry.createOne({ accountId: owner.id, amountCents: cents(1), bookedOn: day('2026-10-09'), tags: [] });
        await enqueue(tx, entryPostedQueue, { entryId: entry.id, amountCents: entry.amountCents });
        throw new Error('the import failed');
      })
    ).rejects.toThrow('the import failed');
    expect(await queued()).toBe(before + 1);
    expect((await orm.entry.findMany({ accountId: { eq: owner.id } })).total).toBe(1);
    // A transaction acts as the ORM's user, and as another with withUser.
    const byBob = await orm.transaction(tx => tx.withUser(bob).account.createOne({ name: name('Bob'), code: 'tx-bob' }));
    expect(byBob.createdBy).toBe(bob);
  });

  test("a nullable JSON column's null is SQL NULL, and a JSON string in it stays a string", async () => {
    const owner = await account('json');
    for (const metadata of [null, '1', 'x', { plaid: { pending: false } }] as const) {
      const entry = await orm.entry.createOne({ accountId: owner.id, amountCents: cents(1), bookedOn: day('2026-10-11'), tags: [], metadata });
      expect([metadata, (await orm.entry.getOne(entry.id)).metadata]).toEqual([metadata, metadata]);
      const { rows } = await db.pool.query('SELECT metadata IS NULL AS sql_null FROM entry WHERE id = $1', [uuidToDb(entry.id)]);
      expect([metadata, rows[0].sql_null]).toEqual([metadata, metadata === null]);
    }
  });

  test('a field of a @jsonField type, a TEXT column, holds its JSON text and reads back as its value', async () => {
    const address = { line1: '1 Tea St', city: 'Leeds' };
    const posted = await orm.account.createOne({ name: name('Posted'), code: 'posted', mailingAddress: address });
    expect(posted.mailingAddress).toEqual(address);
    expect((await orm.account.getOne(posted.id)).mailingAddress).toEqual(address);
    const moved = await orm.account.updateOne(posted.id, { mailingAddress: { line1: '2 Tea St', city: 'York' } });
    expect(moved.mailingAddress).toEqual({ line1: '2 Tea St', city: 'York' });
    const { rows } = await db.pool.query('SELECT mailing_address FROM account WHERE id = $1', [uuidToDb(posted.id)]);
    expect(JSON.parse(rows[0].mailing_address)).toEqual({ line1: '2 Tea St', city: 'York' });
    expect(decodeRow(rows[0], accountTable.columns)).toEqual({ mailingAddress: { line1: '2 Tea St', city: 'York' } });
    expect((await orm.account.updateOne(posted.id, { mailingAddress: null })).mailingAddress).toBeNull();
  });

  test("a raw query on connectPostgres's pool decodes through decodeRow to wire-form ids and numeric amounts", async () => {
    const pool = await connectPostgres({ url: dsn }, { pool: { options: `-c search_path=${db.schema},public` } });
    try {
      const owner = await account('raw');
      const entry = await orm.entry.createOne({ accountId: owner.id, amountCents: cents(-9007199254740991), bookedOn: day('2026-10-10'), tags: [] });
      const { rows } = await pool.query('SELECT id, amount_cents, booked_on FROM entry WHERE id = $1', [uuidToDb(entry.id)]);
      expect(rows[0].amount_cents).toBe(-9007199254740991);
      const decoded = decodeRow<Pick<EntryRow, 'id' | 'amountCents' | 'bookedOn'>>(rows[0], entryTable.columns);
      expect(decoded).toEqual({ id: entry.id, amountCents: entry.amountCents, bookedOn: day('2026-10-10') });
      // The acting user, a principal's base62 subject, matches its rows through uuidToDb.
      const mine = await pool.query('SELECT count(*)::int AS n FROM account WHERE created_by = $1', [uuidToDb(alice)]);
      expect(mine.rows[0].n).toBeGreaterThan(0);
      expect(uuidToDb(alice)).toBe(aliceHyphenated);
      await expect(pool.query('SELECT 9007199254740993::bigint AS n')).rejects.toThrow('past 2^53 - 1');
    } finally {
      await pool.end();
    }
  });
});
