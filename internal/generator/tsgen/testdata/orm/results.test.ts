// The repositories of fixture-ledger-db on SQLite and D1 and on Postgres,
// result for result (D116): one script of calls, every method of a
// repository among them, runs through the generated repositories on each
// database, over the DDL the build writes for its dialect, and what each
// call answers on bun:sqlite, on node:sqlite and on the D1 stand-in is what
// it answers on Postgres, whose statements the Go ORM's are held to
// (ormgen's table parity tests). It is the script the Rust repositories run
// in TestRepositoryResultsOnSQLiteAndPostgres, with the TypeScript
// repositories' reads beside it: select, include and a filter through a
// relation (D90). TestTypeScriptRepositoryResultsOnSQLiteAndPostgres
// generates the package with both dialects' statements and runs it; without
// the Postgres the three SQLite runs are held to one another alone.
//
// A row's key is named by the script, so every database holds the same
// rows. What a database stamps by its own clock, createdAt, updatedAt and
// deletedAt, is compared as set or unset, and a refusal by its kind and,
// for a unique key, its columns, since each database words its own.
//
// A second group holds what is SQLite's and D1's own: a transaction on a
// file, which takes its turn on the one connection, a nested savepoint and
// a read-only one refused; on D1, no interactive transaction, a batch for a
// write of several statements, which rolls back whole, and the statements
// the stand-in refuses, as D1 does.
import { Database } from 'bun:sqlite';
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import type { SqlDatabase } from '@superschematic/http-runtime/d1';
import { openNodeSqlite } from '@superschematic/http-runtime/node-sqlite';
import { NotFoundError, NoUserError, PageKeyError } from '@superschematic/http-runtime/postgres';
import { openSqlite, type SqliteDatabase } from '@superschematic/http-runtime/sqlite';
import { createD1Orm, createOrm, createSqliteOrm, entryTable, type AccountCreate, type EntryCreate, type FixtureLedgerDbOrm } from './orm';
import { database, dsn, type TestDatabase } from './harness';

const ADA = '11111111-1111-4111-8111-111111111111';

/** The key of the row n of the script. */
const key = (n: number): string => `00000000-0000-4000-8000-0000000000${n.toString(16).padStart(2, '0')}`;

/** What a database stamps by its own clock: set, or unset. */
const STAMPED = new Set(['createdAt', 'updatedAt', 'deletedAt']);

/**
 * value with each stamp as whether it is set, a Date as its ISO text, and
 * an object's members, and a Map's entries, in the order of their names:
 * Postgres's JSONB keeps an object's members in an order of its own, and a
 * read of a key's rows is in no order the statement names.
 */
function compared(value: unknown): unknown {
  if (value instanceof Date) return value.toISOString();
  if (value instanceof Map) return compared(Object.fromEntries([...value.entries()].map(([k, v]) => [String(k), v])));
  if (Array.isArray(value)) return value.map(compared);
  if (value !== null && typeof value === 'object') {
    const out: Record<string, unknown> = {};
    for (const [member, v] of Object.entries(value).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))) {
      out[member] = STAMPED.has(member) ? (v === null || v === undefined ? 'unset' : 'set') : compared(v);
    }
    return out;
  }
  return value === undefined ? null : value;
}

/** A refusal as the dialects share it: its kind, and the columns of a unique key. */
function refusal(error: unknown): unknown {
  if (error instanceof NotFoundError) return 'not found';
  if (error instanceof NoUserError) return 'no user';
  if (error instanceof PageKeyError) return 'page key';
  const e = error as { code?: string; detail?: string; message?: string };
  const message = e.message ?? String(error);
  // Postgres's SQLSTATE, and SQLite's words, of each constraint.
  if (e.code === '23505') return { unique: /Key \((.+?)\)=/u.exec(e.detail ?? '')?.[1]?.split(', ') ?? [] };
  const unique = /UNIQUE constraint failed: (.+)$/u.exec(message);
  if (unique) return { unique: unique[1]!.split(', ').map(column => column.slice(column.indexOf('.') + 1)) };
  if (e.code === '23503' || /FOREIGN KEY constraint failed/u.test(message)) return 'foreign key';
  if (e.code === '23502' || /NOT NULL constraint failed/u.test(message)) return 'not null';
  if (e.code === '23514' || /CHECK constraint failed/u.test(message)) return 'check';
  return { error: message };
}

/** What a call answered, as the dialects are compared. */
async function answer<T>(call: () => Promise<T>, shown: (answered: T) => unknown = value => value): Promise<unknown> {
  try {
    return { ok: compared(shown(await call())) };
  } catch (error) {
    return { refused: refusal(error) };
  }
}

const nothing = (): null => null;

/** The script: each call by name, with what it answered. */
async function script(orm: FixtureLedgerDbOrm): Promise<[string, unknown][]> {
  const calls: [string, unknown][] = [];
  const push = async (name: string, call: () => Promise<unknown>, shown?: (answered: never) => unknown) => {
    calls.push([name, await answer(call, shown as (answered: unknown) => unknown)]);
  };
  const accounts = orm.account;
  const entries = orm.entry;
  const account = (n: number, name: string, code: string, openedOn: string | null = null): AccountCreate => ({ id: key(n), name, code, openedOn });
  const entry = (n: number, of: number, amountCents: number, bookedOn: string, extra: Partial<EntryCreate> = {}): EntryCreate => ({
    id: key(n),
    accountId: key(of),
    amountCents,
    bookedOn,
    tags: [],
    ...extra,
  });
  const [cash, bank, card] = [1, 2, 3];

  // Accounts: creates, reads, a search, pages in both orders.
  await push('createAccount', () => accounts.createOne(account(cash, 'Cash', 'cash')));
  await push('createAccounts', () => accounts.createMany([account(bank, 'Bank', 'bank', '2026-10-01'), account(card, 'Card', 'card')]));
  await push('duplicateAccount', () => accounts.createOne(account(9, 'Again', 'cash')));
  await push('getAccount', () => accounts.getOne(key(cash)));
  await push('getNoAccount', () => accounts.getOne(key(99)));
  await push('findAccount', () => accounts.findOne({ code: { eq: 'bank' } }));
  await push('findAccounts', () =>
    accounts.findMany(
      { name: { ilike: 'a' }, searchText: 'ca', referencedByEntryAccount: { exists: false } },
      { orderBy: [{ field: 'code', desc: true }], limit: 10, offset: 1 }
    )
  );
  const first = await accounts.findPage(null, { sort: { field: 'name' }, limit: 2 }).catch(() => null);
  await push('pageAccounts', () => accounts.findPage(null, { sort: { field: 'name' }, limit: 2 }));
  await push('pageAccountsAfter', () => accounts.findPage(null, { sort: { field: 'name' }, limit: 2, after: first?.next ?? null }));
  const listed = { code: { in: ['bank', 'card'] } };
  await push('pageAccountsByKey', () => accounts.findPage(listed, { sort: { field: 'id', desc: true }, limit: 1 }));
  await push('pageAccountsByOpening', () => accounts.findPage(null, { sort: { field: 'openedOn' }, limit: 2 }));
  await push('pageAfterNoRow', () => accounts.findPage(null, { sort: { field: 'name' }, after: { id: key(99) } }));

  // Accounts: updates of one row and of many, and upserts that update,
  // insert and leave a row as it was.
  await push('updateAccount', () => accounts.updateOne(key(cash), { name: 'Cash box' }));
  await push('updateNoAccount', () => accounts.updateOne(key(99), { name: 'None' }));
  await push('updateNothing', () => accounts.updateOne(key(cash), {}));
  await push('updateAccounts', () => accounts.updateMany(listed, { openedOn: '2026-10-02' }));
  await push('upsertAccount', () => accounts.upsertOneResult(account(8, 'Cash', 'cash'), ['code'], { name: 'Cash again' }));
  await push('upsertAccountUnchanged', () => accounts.upsertOneResult(account(8, 'Cash', 'cash'), ['code'], { name: 'Cash again' }, { onlyIfChanged: true }));
  await push('upsertNewAccount', () => accounts.upsertOneResult(account(4, 'Loan', 'loan', '2026-09-30'), ['code'], { name: 'Loan again' }));
  await push('upsertAccountKeep', () => accounts.upsertOneResult(account(8, 'Cash', 'cash'), ['code'], {}));

  // Entries: creates, the filters of each kind of column, the negated
  // ones, and pages across a column's nulls.
  await push('createEntry', () =>
    entries.createOne(entry(11, cash, -1250, '2026-10-25', { memo: 'Coffee', tags: ['food'], reviewedAt: new Date('2026-10-25T09:15:00Z') }))
  );
  await push('createEntries', () =>
    entries.createMany([
      entry(12, bank, 500, '2026-10-26', { externalId: 'x1', kind: 'credit' }),
      entry(13, card, -75, '2026-10-27', { transferId: key(cash), memo: 'Transfer' }),
    ])
  );
  await push('entryOfNoAccount', () => entries.createOne(entry(19, 99, 1, '2026-10-28')));
  await push('getEntry', () => entries.getOne(key(11)));
  await push('findEntry', () =>
    entries.findOne({
      amountCents: { between: [-2000, 0] },
      bookedOn: { gte: '2026-10-01' },
      kind: { in: ['debit'] },
      memo: { isNull: false },
      accountId: { eq: key(cash) },
      searchText: 'coffee',
    })
  );
  const byAmount = { orderBy: [{ field: 'amountCents' as const }] };
  await push('findEntries', () => entries.findMany({ reviewedAt: { isNull: true }, transferId: { isNull: true }, amountCents: { in: [500, -1250] } }, byAmount));
  await push('findEntriesReviewed', () => entries.findMany({ reviewedAt: { lte: new Date('2026-10-25T11:15:00+02:00') } }, { ...byAmount, count: false }));
  await push('findEntriesNegated', () =>
    entries.findMany(
      {
        amountCents: { ne: 1, notIn: [500] },
        bookedOn: { ne: '2026-10-26' },
        kind: { ne: 'credit' },
        memo: { ne: 'Transfer', notIn: ['Refund', 'Fee'] },
        transferId: { ne: key(bank), notIn: [key(card)] },
      },
      byAmount
    )
  );
  await push('findEntriesAnyOf', () =>
    entries.findMany({ anyOf: [{ memo: { eq: 'Coffee' } }, { amountCents: { eq: 500 } }], allOf: [{ account: { code: { in: ['cash', 'bank'] } } }] }, byAmount)
  );
  // A JSON column's filter compares JSON values, as Postgres's JSONB
  // does: an object whose members come in another order, and a number
  // written another way, are the value the row holds.
  await push('createEntriesWithMetadata', () =>
    entries.createMany([
      entry(14, cash, -900, '2026-10-28', { metadata: { tip: 1, where: { city: 'Oslo', floor: 2 }, tags: ['a', 'b'] } }),
      entry(15, bank, 300, '2026-10-28', { metadata: [1, 2.5, 'x'] }),
      entry(16, bank, 200, '2026-10-29', { metadata: 'receipt' }),
    ])
  );
  const same = JSON.parse('{"where": {"floor": 2.0, "city": "Oslo"}, "tags": ["a", "b"], "tip": 1.0}');
  await push('findEntriesByMetadata', () => entries.findMany({ metadata: { eq: same } }, byAmount));
  await push('findEntriesByOtherMetadata', () => entries.findMany({ metadata: { ne: same } }, byAmount));
  await push('findEntriesByMetadataIn', () => entries.findMany({ metadata: { in: [[1.0, 2.5, 'x'], 'receipt', { tip: 2 }] } }, byAmount));
  await push('findEntriesByMetadataNotIn', () => entries.findMany({ metadata: { notIn: [same, 'receipt'] } }, byAmount));
  await push('findEntriesByMetadataOrder', () => entries.findMany({ metadata: { eq: [2.5, 1, 'x'] } }, byAmount));
  for (const [name, desc] of [
    ['pageEntriesByMemo', false],
    ['pageEntriesByMemoDescending', true],
  ] as const) {
    const options = { sort: { field: 'memo' as const, desc }, limit: 2 };
    const page = await entries.findPage(null, options).catch(() => null);
    await push(name, () => entries.findPage(null, options));
    await push(name, () => entries.findPage(null, { ...options, after: page?.next ?? null }));
  }

  // The TypeScript repositories' reads (D90): select, a to-one include and
  // a one-to-many include, and the relation's rows in one statement.
  await push('selectEntries', () => entries.findMany({ accountId: { eq: key(bank) } }, { ...byAmount, select: ['id', 'amountCents'] }));
  await push('includeAccount', () => entries.findMany({ amountCents: { lt: 0 } }, { ...byAmount, select: ['id'], include: { account: { select: ['code'] }, transfer: true } }));
  // A one-to-many include's rows are in no order a read names: compared by key.
  const byId = (rows: readonly { id: string }[]) => [...rows].sort((a, b) => (a.id < b.id ? -1 : 1));
  await push('includeEntries', () => accounts.getOne(key(bank), { include: { entriesByAccount: { select: ['id', 'kind'] } } }), row => {
    const r = row as { entriesByAccount: { id: string }[] };
    return { ...r, entriesByAccount: byId(r.entriesByAccount) };
  });
  await push('getManyByIds', () => accounts.getManyByIds([key(cash), key(card), key(99)]));

  // Entries: an update that clears a column, an update of many, and
  // upserts on a key of two columns.
  await push('updateEntry', () => entries.updateOne(key(11), { memo: null, amountCents: -1300, transferId: key(bank), tags: ['food', 'out'] }));
  await push('updateEntries', () => entries.updateMany({ accountId: { in: [key(bank), key(card)] } }, { kind: 'credit' }));
  const upsert = entry(18, bank, 600, '2026-10-26', { externalId: 'x1', kind: 'credit' });
  await push('upsertEntry', () => entries.upsertOneResult(upsert, ['account', 'externalId'], { amountCents: 600 }));
  await push('upsertEntryKeep', () => entries.upsertOneResult(upsert, ['account', 'externalId'], {}));
  await push('upsertNewEntry', () => entries.upsertOneResult(entry(17, card, 42, '2026-10-30', { externalId: 'x2' }), ['account', 'externalId'], { amountCents: 43 }));

  // Deletes: one that stamps the row, which a read then leaves out, one
  // that removes it, and an account's with its entries and without any.
  await push('deleteEntry', () => entries.deleteOne(key(11)), nothing);
  await push('getDeletedEntry', () => entries.getOne(key(11)));
  await push('deleteEntryAgain', () => entries.deleteOne(key(11)), nothing);
  await push('findDeleted', () => entries.findMany({ accountId: { eq: key(cash) } }, { ...byAmount, includeDeleted: true }));
  await push('upsertDeletedEntry', () => entries.upsertOneResult(entry(11, cash, 1, '2026-10-25'), ['id'], { amountCents: 1 }));
  await push('hardDeleteEntry', () => entries.hardDeleteOne(key(11)), nothing);
  await push('hardDeleteEntryAgain', () => entries.hardDeleteOne(key(11)), nothing);
  await push('deleteManyEntries', () => entries.deleteMany({ amountCents: { eq: 42 } }));
  await push('deleteAccountOfEntries', () => accounts.deleteOne(key(card)), nothing);
  await push('deleteAccount', () => accounts.deleteOne(key(4)), nothing);
  await push('noUser', () => orm.withUser(null).entry.createOne(entry(20, cash, 5, '2026-10-31')));
  await push('allAccounts', () => accounts.findMany(null, { orderBy: [{ field: 'code' }] }));
  await push('allEntries', () => entries.findMany(null, { orderBy: [{ field: 'amountCents', desc: true }], includeDeleted: true }));
  // A raw query's rows, read through the table's definition as the
  // repository reads them (D89), in SQL both dialects run.
  await push('rawRows', () => orm.query('SELECT id, amount_cents, booked_on, tags, metadata FROM entry WHERE amount_cents = 600', [], { decode: entryTable }), result => (result as { rows: unknown[] }).rows);
  return calls;
}

/** The SQLite DDL the build writes, beside the test. */
const sqliteDDL = readFileSync(new URL('./create.sqlite.sql', import.meta.url), 'utf8');

/** A directory of the test's own, removed after. */
const scratch = mkdtempSync(join(tmpdir(), 'orm-results-'));
afterAll(() => rmSync(scratch, { recursive: true, force: true }));

let files = 0;

/** A new SQLite file with the build's DDL applied, as the migration runner applies it, opened through open. */
async function sqliteFile(open: (path: string) => SqliteDatabase): Promise<SqliteDatabase> {
  const path = join(scratch, `ledger-${++files}.sqlite`);
  const ddl = new Database(path, { create: true });
  ddl.exec(sqliteDDL);
  ddl.close();
  return open(path);
}

/** Whether a statement is one of the transaction's, which D1 refuses: it has no interactive transaction. */
const TRANSACTION_STATEMENT = /^\s*(BEGIN|COMMIT|END|ROLLBACK|SAVEPOINT|RELEASE)\b/iu;

/**
 * The D1 stand-in: a D1 binding's interface over a SQLite file (openSqlite
 * keeps D1's bind, its BLOBs and its atomic batch), refusing, as D1 does,
 * every statement of an interactive transaction, so a repository that
 * opened one on D1 fails here.
 */
function d1StandIn(db: SqliteDatabase): SqlDatabase & { refused: string[] } {
  const refused: string[] = [];
  const refuse = (sql: string): void => {
    if (TRANSACTION_STATEMENT.test(sql)) {
      refused.push(sql);
      throw new Error(`D1_ERROR: To execute a transaction, use the batch API, not ${sql.trim().split(/\s/u)[0]} (D1 stand-in)`);
    }
  };
  return {
    refused,
    prepare(sql) {
      refuse(sql);
      return db.prepare(sql);
    },
    batch: statements => db.batch(statements),
    exec(sql) {
      for (const line of sql.split('\n')) refuse(line);
      return db.exec(sql);
    },
  };
}

/** The script on each SQLite target, and on Postgres when the database is set. */
const targets: { name: string; run: () => Promise<[string, unknown][]> }[] = [
  {
    name: 'bun:sqlite',
    run: async () => script(createSqliteOrm(await sqliteFile(openSqlite), ADA)),
  },
  {
    name: 'node:sqlite',
    run: async () => script(createSqliteOrm(await sqliteFile(openNodeSqlite), ADA)),
  },
  {
    name: 'D1',
    run: async () => {
      const stand = d1StandIn(await sqliteFile(openSqlite));
      const calls = await script(createD1Orm(stand, ADA));
      // No call of the script opened a transaction on D1.
      expect(stand.refused).toEqual([]);
      return calls;
    },
  },
];

let postgres: TestDatabase | undefined;
beforeAll(async () => {
  if (dsn !== '') postgres = await database('orm_results');
});
afterAll(async () => {
  await postgres?.drop();
});

describe('the repositories on SQLite and D1 answer as on Postgres', () => {
  test('every call of the script answers on bun:sqlite, node:sqlite and D1 as on Postgres', async () => {
    const reference = postgres ? await script(createOrm(postgres.pool, ADA)) : undefined;
    const answers = new Map<string, [string, unknown][]>();
    for (const target of targets) answers.set(target.name, await target.run());
    const against = reference ?? answers.get('bun:sqlite')!;
    const differing: string[] = [];
    for (const [name, calls] of answers) {
      expect(calls.length).toBe(against.length);
      calls.forEach(([call, answered], i) => {
        const [, wanted] = against[i]!;
        if (JSON.stringify(answered) !== JSON.stringify(wanted)) {
          differing.push(`${call} on ${name}:\n  ${name}: ${JSON.stringify(answered)}\n  ${reference ? 'Postgres' : 'bun:sqlite'}: ${JSON.stringify(wanted)}`);
        }
      });
    }
    if (differing.length > 0) console.log(differing.join('\n'));
    expect(differing.length).toBe(0);
    // The script is no run of refusals: most calls answer, and each kind
    // of refusal is there.
    const refused = against.filter(([, answered]) => (answered as { refused?: unknown }).refused !== undefined).length;
    expect(refused).toBeGreaterThanOrEqual(10);
    expect(refused * 3).toBeLessThan(against.length);
    console.log(
      `${reference ? 'SQLite, D1 and Postgres' : 'bun:sqlite, node:sqlite and D1 (no Postgres)'} answered ${against.length} calls alike, ${refused} of them refusals`
    );
  });
});

describe('a transaction on a SQLite file, and none on D1', () => {
  const opens = [
    ['bun:sqlite', openSqlite],
    ['node:sqlite', openNodeSqlite],
  ] as const;

  for (const [name, open] of opens) {
    test(`a transaction keeps its writes, rolls them back when it throws, and nests a savepoint, on ${name}`, async () => {
      const orm = createSqliteOrm(await sqliteFile(open), ADA);
      await orm.transaction(async tx => {
        await tx.account.createOne({ id: key(1), name: 'Cash', code: 'cash' });
        await tx.entry.createOne({ id: key(11), accountId: key(1), amountCents: 5, bookedOn: '2026-10-25', tags: [] });
      });
      await expect(
        orm.transaction(async tx => {
          await tx.account.createOne({ id: key(2), name: 'Bank', code: 'bank' });
          throw new Error('stop');
        })
      ).rejects.toThrow('stop');
      await orm.transaction(async tx => {
        await tx.account.updateOne(key(1), { name: 'Cash box' });
        await expect(
          tx.transaction(async inner => {
            await inner.account.createOne({ id: key(3), name: 'Card', code: 'card' });
            throw new Error('inner');
          })
        ).rejects.toThrow('inner');
      });
      const { rows } = await orm.account.findMany(null, { orderBy: [{ field: 'code' }] });
      expect(rows.map(row => [row.code, row.name])).toEqual([['cash', 'Cash box']]);
      expect((await orm.entry.findMany(null)).total).toBe(1);
      // Every SQLite transaction is serializable, and none is read only.
      await orm.transaction(async () => undefined, { isolation: 'serializable' });
      await expect(orm.transaction(async () => undefined, { readOnly: true })).rejects.toThrow('cannot begin read only');
    });

    test(`two transactions on one file take turns, and a write outside one waits for it, on ${name}`, async () => {
      const db = await sqliteFile(open);
      const orm = createSqliteOrm(db, ADA);
      const other = createSqliteOrm(db, ADA);
      await orm.account.createOne({ id: key(1), name: 'Cash', code: 'cash' });
      const order: string[] = [];
      let release!: () => void;
      const held = new Promise<void>(resolve => {
        release = resolve;
      });
      const first = orm.transaction(async tx => {
        order.push('first begins');
        await tx.account.updateOne(key(1), { name: 'First' });
        await held;
        order.push('first ends');
      });
      // Each waits for the first to end: the connection holds one
      // transaction, and its writes would otherwise be the first's.
      const second = other.transaction(async tx => {
        order.push('second begins');
        await tx.account.updateOne(key(1), { name: 'Second' });
      });
      const outside = orm.account.updateOne(key(1), { code: 'till' }).then(() => order.push('outside'));
      await new Promise(resolve => setTimeout(resolve, 50));
      release();
      await Promise.all([first, second, outside]);
      expect(order).toEqual(['first begins', 'first ends', 'second begins', 'outside']);
      expect(await orm.account.getOne(key(1))).toMatchObject({ name: 'Second', code: 'till' });
    });

    test(`upsertMany is not built on SQLite, and says so, on ${name}`, async () => {
      const orm = createSqliteOrm(await sqliteFile(open), ADA);
      await expect(orm.account.upsertMany([{ id: key(1), name: 'Cash', code: 'cash' }], ['code'], ['name'])).rejects.toThrow('an upsert of many rows on SQLite and D1 is not built (D89)');
    });
  }

  test('on D1 a transaction is refused, and createMany and an upsert each go as one batch, which rolls back whole', async () => {
    const stand = d1StandIn(await sqliteFile(openSqlite));
    const orm = createD1Orm(stand, ADA);
    await expect(orm.transaction(async () => undefined)).rejects.toThrow('D1 has no interactive transaction');
    await orm.account.createOne({ id: key(1), name: 'Cash', code: 'cash' });
    // The second row's account is no row: the batch writes neither.
    await expect(
      orm.entry.createMany([
        { id: key(11), accountId: key(1), amountCents: 5, bookedOn: '2026-10-25', tags: [] },
        { id: key(12), accountId: key(99), amountCents: 6, bookedOn: '2026-10-25', tags: [] },
      ])
    ).rejects.toThrow('FOREIGN KEY constraint failed');
    expect((await orm.entry.findMany(null)).total).toBe(0);
    expect(await orm.account.upsertOneResult({ id: key(2), name: 'Bank', code: 'bank' }, ['code'], { name: 'Bank again' })).toMatchObject({ status: 'inserted' });
    expect(await orm.account.upsertOneResult({ id: key(3), name: 'Bank', code: 'bank' }, ['code'], { name: 'Bank again' })).toMatchObject({ status: 'updated', row: { name: 'Bank again' } });
    expect(stand.refused).toEqual([]);
  });
});
