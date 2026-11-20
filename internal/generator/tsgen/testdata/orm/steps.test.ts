// fixture-steps-db's tables end to end (D104's step 4): a batch that
// implements Rollups over the steps that point at it, the rows its steps
// HasMany lists, beside its status Workflow, its done rollup gating done;
// and a step that implements Dependencies beside its own, gating doing and
// done. TestStepsTablesEndToEnd generates the package, type-checks it,
// applies the generated DDL and runs these cases on each dialect: on
// bun:sqlite, the SQLite create.sql D27 plans and the HTTP runtime's
// adapter over the layout orm.ts embeds (fixtureStepsDbRules), since the
// generated repositories run on Postgres alone; on Postgres, the generated
// create.sql and the generated repository itself (createOrm). So the
// generator's layout, its DDL, the blockers' sub-table, the relation's
// column the gate reads the steps by and the adapter are held to one
// another: an edge is a sub-table row; a step's gate holds its start until
// its blocker is done; a cycle is refused; listBlockers pages the edges
// and removeBlocker takes one; and a batch's done waits for every step.
import { Database } from 'bun:sqlite';
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { initSync } from '@superschematic/rules';
import { execute, readSubRows, runCreate, RulesRefusal, sqliteRulesStore, type CoreCaller, type CoreSubRow, type RulesHost } from '@superschematic/http-runtime/d1';
import { openSqlite, type SqliteDatabase } from '@superschematic/http-runtime/sqlite';
import { createOrm, fixtureStepsDbRules as layout } from './orm';
import { alice, database, dsn } from './harness';

const subTable = layout.types.Step!.uses.find(use => use.id === 'Dependencies')!.tables!.find(sub => sub.name === 'blockers')!;

/** What a case runs through: on Postgres the generated repository, on bun:sqlite the adapter over the generated layout. */
interface Tables {
  readonly dialect: string;
  /** Creates a row and resolves to its key. */
  create(type: 'Batch' | 'Step', own: Record<string, unknown>): Promise<string>;
  operate(type: 'Batch' | 'Step', key: string, use: string, operation: string, params: unknown): Promise<unknown>;
  /** The step's blockers' sub-table rows, as the core reads them. */
  blockers(key: string): Promise<CoreSubRow[]>;
  close(): Promise<void>;
}

/** The tables on bun:sqlite: the generated SQLite DDL in a file of the test's own, and the adapter over the generated layout, as alice. */
async function sqliteTables(): Promise<Tables> {
  const dir = mkdtempSync(join(tmpdir(), 'steps-db-'));
  const file = join(dir, 'steps.db');
  const ddl = new Database(file, { create: true });
  ddl.exec(readFileSync(new URL('./create.sqlite.sql', import.meta.url), 'utf8'));
  ddl.close();
  const db: SqliteDatabase = openSqlite(file);
  const host: RulesHost = { core: initSync(), layout, store: sqliteRulesStore(db) };
  const caller: CoreCaller = { subject: alice, permissions: [] };
  return {
    dialect: 'bun:sqlite',
    create: (type, own) => runCreate(host, type, own, caller),
    async operate(type, key, use, operation, params) {
      return (await execute(host, type, { caller, now: Date.now(), action: { kind: 'operation', key, useId: use, operation, params } })).result;
    },
    blockers: key => readSubRows(host, 'Step', key, 'Dependencies', 'blockers'),
    async close() {
      db.close();
      rmSync(dir, { recursive: true, force: true });
    },
  };
}

/** The tables on Postgres: the generated create.sql in a schema of the test's own, and the generated repository, acting as alice. */
async function postgresTables(): Promise<Tables> {
  const db = await database('ts_orm_steps');
  const orm = createOrm(db.pool, alice);
  return {
    dialect: 'Postgres',
    async create(type, own) {
      return type === 'Batch' ? (await orm.batch.createOne(own as never)).id : (await orm.step.createOne(own as never)).id;
    },
    operate: (type, key, use, operation, params) => ((type === 'Batch' ? orm.batch : orm.step) as any)[use === 'Dependencies' ? 'dependencies' : use][operation](key, params),
    async blockers(key) {
      // No repository reads a sub-table (D103): SQL does.
      const rows = (await db.pool.query(`SELECT * FROM "${subTable.table}" WHERE "${subTable.row}" = $1 ORDER BY "${subTable.key.column}"`, [uuidText(key)])).rows;
      return rows.map(row => {
        const values: Record<string, unknown> = {};
        for (const field of subTable.fields) {
          const value = row[field.column];
          if (value === null || value === undefined) continue;
          values[field.name] = field.convert === 'epochMillis' ? (value instanceof Date ? value.getTime() : Number(value)) : value;
        }
        return { key: String(row[subTable.key.column]), values };
      });
    },
    close: () => db.drop(),
  };
}

const BASE62 = '0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz';

/** A UUID's hyphenated text from its base62 wire form, as its column holds it. */
function uuidText(key: string): string {
  let n = 0n;
  for (const c of key) n = n * 62n + BigInt(BASE62.indexOf(c));
  const hex = n.toString(16).padStart(32, '0');
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

async function refusal(call: Promise<unknown>): Promise<RulesRefusal> {
  const error = await call.then(
    () => undefined,
    (e: unknown) => e
  );
  expect(error).toBeInstanceOf(RulesRefusal);
  return error as RulesRefusal;
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
      await t.close();
    });

    const move = (type: 'Batch' | 'Step', key: string, to: string) => t.operate(type, key, 'status', 'transition', { to });

    test(`addBlocker records the edge in the sub-table, and the gate holds the step's start until its blocker is done, on ${dialect.name}`, async () => {
      const batch = await t.create('Batch', { title: 'release' });
      const first = await t.create('Step', { title: 'build', batchId: batch });
      const second = await t.create('Step', { title: 'ship', batchId: batch });
      expect(await t.operate('Step', second, 'Dependencies', 'addBlocker', { id: first })).toEqual({ schema: 'Step', id: first, status: 'todo', open: true });
      const edges = await t.blockers(second);
      expect(edges.map(edge => ({ key: edge.key, blockerType: edge.values.blockerType, blockerKey: edge.values.blockerKey, addedBy: edge.values.addedBy }))).toEqual([
        { key: '0000000001', blockerType: 'Step', blockerKey: first, addedBy: alice },
      ]);
      const held = await refusal(move('Step', second, 'doing'));
      expect(held.refusal.details).toMatchObject({ behavior: 'Dependencies', vetoCode: 'blocked', vetoDetails: { blockers: [{ schema: 'Step', id: first, status: 'todo' }] } });
      await move('Step', first, 'doing');
      await move('Step', first, 'done');
      expect(await move('Step', second, 'doing')).toEqual({ from: 'todo', to: 'doing' });
    });

    test(`an edge that would close a cycle is refused, on ${dialect.name}`, async () => {
      const batch = await t.create('Batch', { title: 'loop' });
      const [a, b, c] = [await t.create('Step', { title: 'a', batchId: batch }), await t.create('Step', { title: 'b', batchId: batch }), await t.create('Step', { title: 'c', batchId: batch })];
      await t.operate('Step', b, 'Dependencies', 'addBlocker', { id: a });
      await t.operate('Step', c, 'Dependencies', 'addBlocker', { id: b });
      const cycle = await refusal(t.operate('Step', a, 'Dependencies', 'addBlocker', { id: c }));
      expect(cycle.refusal.details).toMatchObject({ vetoCode: 'cycle' });
      expect(cycle.refusal.message).toBe(`behavior Dependencies vetoes addBlocker of Step ${a}: Step ${c} is blocked by Step ${a}, directly or through others: the edge would close a cycle`);
      expect(await t.blockers(a)).toEqual([]);
    });

    test(`listBlockers pages the edges in the order they were added, and removeBlocker takes one, on ${dialect.name}`, async () => {
      const batch = await t.create('Batch', { title: 'pages' });
      const [a, b, c] = [await t.create('Step', { title: 'a', batchId: batch }), await t.create('Step', { title: 'b', batchId: batch }), await t.create('Step', { title: 'c', batchId: batch })];
      await t.operate('Step', c, 'Dependencies', 'addBlocker', { id: b });
      await t.operate('Step', c, 'Dependencies', 'addBlocker', { id: a });
      const page = (await t.operate('Step', c, 'Dependencies', 'listBlockers', { limit: 1 })) as { items: { id: string }[]; next: string | null };
      expect(page.items.map(item => item.id)).toEqual([b]);
      const rest = (await t.operate('Step', c, 'Dependencies', 'listBlockers', { cursor: page.next })) as { items: { id: string }[]; next: string | null };
      expect(rest).toEqual({ items: [{ schema: 'Step', id: a, status: 'todo', open: true }] as never, next: null });
      expect(await t.operate('Step', c, 'Dependencies', 'removeBlocker', { id: b })).toEqual({ schema: 'Step', id: b });
      expect((await t.blockers(c)).map(edge => edge.values.blockerKey)).toEqual([a]);
    });

    test(`a batch's done waits for every step that points at it, not_held with the counts, on ${dialect.name}`, async () => {
      const batch = await t.create('Batch', { title: 'gate' });
      const [a, b] = [await t.create('Step', { title: 'a', batchId: batch, estimate: 3 }), await t.create('Step', { title: 'b', batchId: batch, estimate: 4 })];
      const waiting = await refusal(move('Batch', batch, 'done'));
      expect(waiting.refusal.details).toMatchObject({ behavior: 'Rollups', vetoCode: 'not_held', vetoDetails: { rollup: 'done', to: 'done', over: false, linked: 2, counted: 0 } });
      for (const step of [a, b]) {
        await move('Step', step, 'doing');
        await move('Step', step, 'done');
      }
      expect(await move('Batch', batch, 'done')).toEqual({ from: 'running', to: 'done' });
    });
  });
}
