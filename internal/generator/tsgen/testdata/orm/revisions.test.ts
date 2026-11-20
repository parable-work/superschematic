// fixture-revisions-db's tables end to end (D16, D103's step 4): a policy
// whose every change is kept, with a review step, and a note whose history
// is kept alone. TestRevisionsTablesEndToEnd generates the package,
// type-checks it, applies the generated DDL and runs these cases on each
// dialect: on bun:sqlite, the SQLite create.sql D27 plans and the HTTP
// runtime's adapter over the layout orm.ts embeds (fixtureRevisionsDbRules);
// on Postgres, the generated create.sql and the generated repository
// itself (createOrm), whose operations stamp updatedAt. So the generator's
// layout, its DDL, the use's sub-tables and the adapter are held to one
// another: a create keeps revision 1 and each change of the own fields the
// next, a row of the sub-table revisions; an approval writes the
// proposal's patch to the row, beside the audit stamp its caller encoded,
// and records the revision it made; and an approval without the review
// permission is refused, its proposal left pending.
import { Database } from 'bun:sqlite';
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { initSync } from '@superschematic/rules';
import { execute, readRow, runCreate, runUpdate, sqliteRulesStore, type CoreCaller, type RulesHost } from '@superschematic/http-runtime/d1';
import { openSqlite, type SqliteDatabase } from '@superschematic/http-runtime/sqlite';
import { createOrm, fixtureRevisionsDbRules as layout } from './orm';
import { alice, database, dsn } from './harness';

const reviewer: CoreCaller = { subject: 'rae', permissions: ['policies.review'] };

/** What a case runs through: on Postgres the generated repository, on bun:sqlite the adapter over the generated layout. */
interface Tables {
  readonly dialect: string;
  /** Creates a policy through the core and resolves to its key. */
  policy(title: string): Promise<string>;
  /** Updates a policy's own fields through the core. */
  update(key: string, patch: Record<string, unknown>): Promise<void>;
  /** Runs one of the policy's Revisions operations as alice, or as the reviewer. */
  invoke(key: string, operation: string, params: Record<string, unknown>, asReviewer?: boolean): Promise<any>;
  /** The policy's own fields as the store holds them. */
  own(key: string): Promise<{ title: unknown; body: unknown; updatedAt: number }>;
  /** Sets the updatedAt of the policy of a title to the start of 2000 past the repository, where the store keeps the stamp. */
  age(title: string): Promise<void>;
  close(): Promise<void>;
}

/** The tables on bun:sqlite: the generated SQLite DDL in a file of the test's own, and the adapter over the generated layout. */
async function sqliteTables(): Promise<Tables> {
  const dir = mkdtempSync(join(tmpdir(), 'revisions-db-'));
  const file = join(dir, 'revisions.db');
  const ddl = new Database(file, { create: true });
  ddl.exec(readFileSync(new URL('./create.sqlite.sql', import.meta.url), 'utf8'));
  ddl.close();
  const db: SqliteDatabase = openSqlite(file);
  const host: RulesHost = { core: initSync(), layout, store: sqliteRulesStore(db) };
  const caller: CoreCaller = { subject: alice, permissions: [] };
  return {
    dialect: 'bun:sqlite',
    policy: title => runCreate(host, 'Policy', { title }, caller),
    async update(key, patch) {
      await runUpdate(host, 'Policy', key, patch, caller);
    },
    async invoke(key, operation, params, asReviewer) {
      const action = { kind: 'operation' as const, key, useId: 'Revisions', operation, params };
      return (await execute(host, 'Policy', { caller: asReviewer ? reviewer : caller, now: Date.now(), action })).result;
    },
    async own(key) {
      // The layout gives the core the table's own fields, not its audit
      // stamps, which the adapter's caller encodes; a revision holds none.
      const row = await readRow(host, 'Policy', key);
      return { title: row?.own.title, body: row?.own.body ?? null, updatedAt: Number.NaN };
    },
    async age() {},
    async close() {
      db.close();
      rmSync(dir, { recursive: true, force: true });
    },
  };
}

/** The tables on Postgres: the generated create.sql in a schema of the test's own, and the generated repository, acting as alice. */
async function postgresTables(): Promise<Tables> {
  const db = await database('ts_orm_revisions');
  const orm = createOrm(db.pool, alice);
  const reviewing = orm.withBehaviorCaller(reviewer);
  return {
    dialect: 'Postgres',
    async policy(title) {
      return (await orm.policy.createOne({ title } as never)).id;
    },
    async update(key, patch) {
      await orm.policy.updateOne(key as never, patch as never);
    },
    invoke: (key, operation, params, asReviewer) => ((asReviewer ? reviewing : orm).policy.revisions as any)[operation](key, params),
    async own(key) {
      const row = (await orm.policy.findOne({ id: { eq: key } } as never)) as any;
      return { title: row?.title, body: row?.body ?? null, updatedAt: new Date(row?.updatedAt).getTime() };
    },
    async age(title) {
      await db.pool.query(`UPDATE policy SET updated_at = '2000-01-01T00:00:00Z' WHERE title = $1`, [title]);
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

    test(`a create keeps revision 1 and each change of the own fields the next, rows of the use's sub-table, on ${dialect.name}`, async () => {
      const key = await t.policy('Leave');
      await t.update(key, { body: 'Ten days.' });
      const page = await t.invoke(key, 'listRevisions', {});
      expect(page.items.map((item: any) => [item.revision, item.data.title, item.data.body ?? null, item.createdBy])).toEqual([
        [1, 'Leave', null, alice],
        [2, 'Leave', 'Ten days.', alice],
      ]);
      expect(page.next).toBeNull();
    });

    test(`an approval writes the proposal's patch to the row beside its audit stamp and records the revision it made, on ${dialect.name}`, async () => {
      const key = await t.policy('Travel');
      await t.age('Travel');
      const proposal = await t.invoke(key, 'propose', { patch: { body: 'Economy only.' }, note: 'Costs.' });
      expect([proposal.id, proposal.state, proposal.base]).toEqual([1, 'pending', 1]);
      expect((await t.own(key)).body).toBeNull();
      const approved = await t.invoke(key, 'approve', { proposal: 1 }, true);
      expect([approved.state, approved.reviewedBy, approved.revision]).toEqual(['approved', 'rae', 2]);
      const after = await t.own(key);
      expect([after.title, after.body]).toEqual(['Travel', 'Economy only.']);
      // The repository stamps updatedAt on the approval's write, beside the
      // patch's own fields.
      if (dialect.name === 'Postgres') expect(after.updatedAt).toBeGreaterThan(Date.parse('2001-01-01T00:00:00Z'));
      const revision = await t.invoke(key, 'getRevision', { revision: 2 });
      expect([revision.data.body, revision.proposal, revision.createdBy]).toEqual(['Economy only.', 1, 'rae']);
    });

    test(`an approval without the review permission is refused and its proposal stays pending, on ${dialect.name}`, async () => {
      const key = await t.policy('Remote');
      await t.invoke(key, 'propose', { patch: { body: 'Two days a week.' } });
      await expect(t.invoke(key, 'approve', { proposal: 1 })).rejects.toThrow(/needs permission policies.review/);
      const pending = await t.invoke(key, 'listProposals', { state: 'pending' });
      expect(pending.items.map((item: any) => item.id)).toEqual([1]);
      expect((await t.own(key)).body).toBeNull();
    });
  });
}
