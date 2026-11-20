// fixture-derived-provenance-db's tables end to end (D73, D103's step 4): a ticket a
// model triages under review. TestDerivedTablesEndToEnd generates the
// package, type-checks it, applies the generated DDL and runs these cases
// on each dialect: on bun:sqlite, the SQLite create.sql D27 plans and the
// HTTP runtime's adapter over the layout orm.ts embeds
// (fixtureDerivedProvenanceDbRules); on Postgres, the generated create.sql and the
// generated repository itself (createOrm). So the generator's layout, each
// derived field's schema in it, its DDL, the uses' sub-tables and owned
// column, and the adapter are held to one another: a deriver pulls a
// ticket's stale fields, an operation of the table as a whole that names no
// key, each with the output its field's schema gives; its settle writes
// the values as own fields through the core, Provenance records each with
// the confidence the model gave and proposes the unsure one, which holds
// the ticket's move to assigned until a reviewer accepts it; and a value
// the field's enum refuses counts an attempt and writes no own field.
import { Database } from 'bun:sqlite';
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { initSync } from '@superschematic/rules';
import { execute, readRow, runCreate, sqliteRulesStore, type CoreCaller, type RulesHost } from '@superschematic/http-runtime/d1';
import { openSqlite, type SqliteDatabase } from '@superschematic/http-runtime/sqlite';
import { createOrm, fixtureDerivedProvenanceDbRules as layout } from './orm';
import { alice, database, dsn } from './harness';

const deriver: CoreCaller = { subject: 'deriver', permissions: ['tickets.derive'] };
const reviewer: CoreCaller = { subject: 'rae', permissions: ['tickets.review'] };

/** What a case runs through: on Postgres the generated repository, on bun:sqlite the adapter over the generated layout. */
interface Tables {
  readonly dialect: string;
  /** Creates a ticket through the core as alice and resolves to its key. */
  ticket(subject: string, body: string): Promise<string>;
  /** Runs one of Derived's operations of the table as a whole as the deriver. */
  pull(operation: string, params: Record<string, unknown>): Promise<any>;
  /** Runs an operation of a use of a ticket, as alice or as the reviewer. */
  invoke(key: string, member: 'provenance' | 'status', operation: string, params: Record<string, unknown>, asReviewer?: boolean): Promise<any>;
  /** The ticket's own and owned fields as the store holds them. */
  row(key: string): Promise<{ category: unknown; priority: unknown; inReview: unknown }>;
  close(): Promise<void>;
}

/** The tables on bun:sqlite: the generated SQLite DDL in a file of the test's own, and the adapter over the generated layout. */
async function sqliteTables(): Promise<Tables> {
  const dir = mkdtempSync(join(tmpdir(), 'derived-db-'));
  const file = join(dir, 'derived.db');
  const ddl = new Database(file, { create: true });
  ddl.exec(readFileSync(new URL('./create.sqlite.sql', import.meta.url), 'utf8'));
  ddl.close();
  const db: SqliteDatabase = openSqlite(file);
  const host: RulesHost = { core: initSync(), layout, store: sqliteRulesStore(db) };
  const caller: CoreCaller = { subject: alice, permissions: [] };
  const useIds = { provenance: 'Provenance', status: 'status' };
  return {
    dialect: 'bun:sqlite',
    ticket: (subject, body) => runCreate(host, 'Ticket', { subject, body }, caller),
    async pull(operation, params) {
      const action = { kind: 'tableOperation' as const, useId: 'Derived', operation, params };
      return (await execute(host, 'Ticket', { caller: deriver, now: Date.now(), action })).result;
    },
    async invoke(key, member, operation, params, asReviewer) {
      const action = { kind: 'operation' as const, key, useId: useIds[member], operation, params };
      return (await execute(host, 'Ticket', { caller: asReviewer ? reviewer : caller, now: Date.now(), action })).result;
    },
    async row(key) {
      const row = await readRow(host, 'Ticket', key);
      return { category: row?.own.category ?? null, priority: row?.own.priority ?? null, inReview: row?.owned.Provenance?.inReview ?? null };
    },
    async close() {
      db.close();
      rmSync(dir, { recursive: true, force: true });
    },
  };
}

/** The tables on Postgres: the generated create.sql in a schema of the test's own, and the generated repository, acting as alice. */
async function postgresTables(): Promise<Tables> {
  const db = await database('ts_orm_derived');
  const orm = createOrm(db.pool, alice);
  const deriving = orm.withBehaviorCaller(deriver);
  const reviewing = orm.withBehaviorCaller(reviewer);
  return {
    dialect: 'Postgres',
    async ticket(subject, body) {
      return (await orm.ticket.createOne({ subject, body } as never)).id;
    },
    pull: (operation, params) => (deriving.ticket.derived as any)[operation](params),
    invoke: (key, member, operation, params, asReviewer) => ((asReviewer ? reviewing : orm).ticket[member] as any)[operation](key, params),
    async row(key) {
      const row = (await orm.ticket.findOne({ id: { eq: key } } as never)) as any;
      return { category: row?.category ?? null, priority: row?.priority ?? null, inReview: row?.provenanceInReview ?? null };
    },
    close: () => db.drop(),
  };
}

const dialects: { readonly name: string; readonly skip: boolean; readonly open: () => Promise<Tables> }[] = [
  { name: 'bun:sqlite', skip: false, open: sqliteTables },
  { name: 'Postgres', skip: dsn === '', open: postgresTables },
];

for (const dialect of dialects) {
  describe.skipIf(dialect.skip)(dialect.name, () => {
    let t: Tables;

    beforeAll(async () => {
      t = await dialect.open();
    });

    afterAll(async () => {
      await t?.close();
    });

    test(`a deriver pulls a ticket's stale fields and settles them as own fields, the unsure one proposed until a reviewer accepts it, on ${dialect.name}`, async () => {
      const key = await t.ticket('Card declined', 'Refused at checkout.');
      expect(await t.row(key)).toEqual({ category: null, priority: null, inReview: false });
      const pulled = await t.pull('staleDerivations', {});
      const mine = pulled.items.filter((item: any) => item.id === key);
      expect(mine.map((item: any) => [item.field, item.attempt, item.inputs])).toEqual([
        ['category', 1, { subject: 'Card declined', body: 'Refused at checkout.' }],
        ['priority', 1, { subject: 'Card declined', body: 'Refused at checkout.' }],
      ]);
      expect(mine[0].output).toEqual({ description: 'A Category value', enum: ['billing', 'bug', 'other', null], type: ['string', 'null'] });
      const hash = (field: string) => mine.find((item: any) => item.field === field).sourceHash;
      const settled = await t.pull('settleDerivations', {
        items: [
          { id: key, field: 'category', sourceHash: hash('category'), value: 'billing', confidence: 0.95 },
          { id: key, field: 'priority', sourceHash: hash('priority'), value: 'high', confidence: 0.6 },
        ],
      });
      expect(settled).toEqual({ settled: 2, failed: 0, skipped: [] });
      expect(await t.row(key)).toEqual({ category: 'billing', priority: 'high', inReview: true });
      await expect(t.invoke(key, 'status', 'transition', { to: 'assigned' })).rejects.toThrow(/priority is in review/);
      expect(await t.invoke(key, 'provenance', 'accept', { fields: ['priority'] }, true)).toEqual({ accepted: ['priority'], inReview: false });
      expect(await t.invoke(key, 'status', 'transition', { to: 'assigned' })).toEqual({ from: 'new', to: 'assigned' });
      const again = await t.pull('staleDerivations', {});
      expect(again.items.filter((item: any) => item.id === key)).toEqual([]);
    });

    test(`a value the field's enum refuses counts an attempt and writes no own field, on ${dialect.name}`, async () => {
      const key = await t.ticket('Login', 'Locked out.');
      const pulled = await t.pull('staleDerivations', { field: 'category' });
      const item = pulled.items.find((item: any) => item.id === key);
      const settled = await t.pull('settleDerivations', { items: [{ id: key, field: 'category', sourceHash: item.sourceHash, value: 'payments' }] });
      expect(settled.skipped).toEqual([
        { id: key, field: 'category', reason: 'invalid', issues: [{ path: 'category', rule: 'enum', message: 'Category must be one of the allowed values.' }] },
      ]);
      expect((await t.row(key)).category).toBeNull();
      const next = await t.pull('staleDerivations', { field: 'category' });
      expect(next.items.find((item: any) => item.id === key).attempt).toBe(2);
    });
  });
}
