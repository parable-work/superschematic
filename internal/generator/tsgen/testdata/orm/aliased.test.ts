// fixture-aliased-db's tables end to end (D147): a company whose name, a
// create gives it, no other company holds as a name or an alias, with its
// case and its runs of spaces ignored, and a tag read as a slug.
// TestAliasedTablesEndToEnd generates the package, type-checks it, applies
// the generated DDL and runs these cases on each dialect: on bun:sqlite,
// the SQLite create.sql D27 plans and the HTTP runtime's adapter over the
// layout orm.ts embeds (fixtureAliasedDbRules), since the generated
// repositories run on Postgres alone; on Postgres, the generated create.sql
// and the generated repository itself (createOrm). So the generator's
// layout, its DDL, its sub-table of aliases and shared table of names and
// the adapter are held to one another: a name another row holds is
// refused with its key; a rename keeps the old name as an alias; resolve
// finds the row a name or an alias names; a removed alias, and a deleted
// row's names, are freed for another row; and on Postgres a merge moves the
// aliases of the row merged away, its name among them, to the row that
// stays.
import { Database } from 'bun:sqlite';
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { initSync } from '@superschematic/rules';
import { execute, readRow, readSubRows, runCreate, runDelete, sqliteRulesStore, type CoreCaller, type RulesHost } from '@superschematic/http-runtime/d1';
import { openSqlite, type SqliteDatabase } from '@superschematic/http-runtime/sqlite';
import { createOrm, fixtureAliasedDbRules as layout } from './orm';
import { alice, database, dsn } from './harness';

const names = layout.types.Company!.uses.find(use => use.id === 'name')!.shared!.find(table => table.name === 'names')!;

/** A refusal's code and veto, as either path throws it. */
interface Refused {
  readonly code?: string;
  readonly vetoCode?: string;
  readonly holder?: string;
}

/** What a case runs through: on Postgres the generated repository, on bun:sqlite the adapter over the generated layout. */
interface Tables {
  readonly dialect: string;
  /** Creates a company through the core and resolves to its key. */
  company(name: string, city: string): Promise<string>;
  /** Creates a tag through the core and resolves to its key. */
  tag(label: string): Promise<string>;
  /** A company's name. */
  name(key: string): Promise<unknown>;
  /** A company's aliases, as given, in the order of their normalized forms. */
  aliases(key: string): Promise<string[]>;
  /** Runs one of a company's name's operations. */
  invoke(key: string, operation: 'rename' | 'addAlias' | 'removeAlias', params: Record<string, unknown>): Promise<unknown>;
  /** Runs a table's resolve. */
  resolve(type: 'Company' | 'Tag', name: string): Promise<unknown>;
  delete(key: string): Promise<void>;
  /** A read past the repository and the adapter, its placeholders $1, $2, ... */
  query(text: string, values: readonly unknown[]): Promise<Record<string, unknown>[]>;
  close(): Promise<void>;
}

/** The refusal an operation or a create threw, by its code and veto. */
async function refusalOf(run: Promise<unknown>): Promise<Refused> {
  try {
    await run;
  } catch (error) {
    const e = error as { code?: string; details?: { vetoCode?: string; vetoDetails?: { holder?: string } }; refusal?: { code?: string; details?: { vetoCode?: string; vetoDetails?: { holder?: string } } } };
    const refusal = e.refusal ?? e;
    return { code: refusal.code, vetoCode: refusal.details?.vetoCode, holder: refusal.details?.vetoDetails?.holder };
  }
  throw new Error('not refused');
}

/** The tables on bun:sqlite: the generated SQLite DDL in a file of the test's own, and the adapter over the generated layout, as alice. */
async function sqliteTables(): Promise<Tables> {
  const dir = mkdtempSync(join(tmpdir(), 'aliased-db-'));
  const file = join(dir, 'aliased.db');
  const ddl = new Database(file, { create: true });
  ddl.exec(readFileSync(new URL('./create.sqlite.sql', import.meta.url), 'utf8'));
  ddl.close();
  const db: SqliteDatabase = openSqlite(file);
  const sql = (text: string) => text.replace(/\$(\d+)/g, '?$1');
  const query = async (text: string, values: readonly unknown[]) => (await db.prepare(sql(text)).bind(...(values as never[])).all()).results;
  const host: RulesHost = { core: initSync(), layout, store: sqliteRulesStore(db) };
  const caller: CoreCaller = { subject: alice, permissions: [] };
  const run = async (type: string, action: Record<string, unknown>) => (await execute(host, type, { caller, now: Date.now(), action: action as never })).result;
  return {
    dialect: 'bun:sqlite',
    company: (name, city) => runCreate(host, 'Company', { name, city }, caller),
    tag: label => runCreate(host, 'Tag', { label }, caller),
    async name(key) {
      return (await readRow(host, 'Company', key))?.owned?.name?.name ?? null;
    },
    async aliases(key) {
      return (await readSubRows(host, 'Company', key, 'name', 'aliases')).map(row => String(row.values.alias));
    },
    invoke: (key, operation, params) => run('Company', { kind: 'operation', key, useId: 'name', operation, params }),
    resolve: (type, name) => run(type, { kind: 'tableOperation', useId: type === 'Company' ? 'name' : 'label', operation: 'resolve', params: { name } }),
    delete: key => runDelete(host, 'Company', key, caller),
    query,
    async close() {
      db.close();
      rmSync(dir, { recursive: true, force: true });
    },
  };
}

/** The tables on Postgres: the generated create.sql in a schema of the test's own, and the generated repository, acting as alice. */
async function postgresTables(): Promise<Tables & { merge(from: string, into: string): Promise<unknown>; contact(company: string, email: string): Promise<string>; contactCompany(key: string): Promise<unknown> }> {
  const db = await database('ts_orm_aliased');
  const orm = createOrm(db.pool, alice);
  const query = async (text: string, values: readonly unknown[]) => (await db.pool.query(text, [...values])).rows;
  return {
    dialect: 'Postgres',
    async company(name, city) {
      return (await orm.company.createOne({ name, city })).id;
    },
    async tag(label) {
      return (await orm.tag.createOne({ label })).id;
    },
    async name(key) {
      return (await orm.company.findOne({ id: { eq: key } }))?.name ?? null;
    },
    async aliases(key) {
      const rows = await query('SELECT alias FROM company_name_aliases WHERE company_id = $1::uuid ORDER BY normalized', [uuidText(key)]);
      return rows.map(row => String(row.alias));
    },
    invoke: (key, operation, params) => (orm.company.name as any)[operation](key, params),
    resolve: (type, name) => (type === 'Company' ? orm.company.name.resolve({ name }) : orm.tag.label.resolve({ name })),
    delete: key => orm.company.deleteOne(key as never),
    async merge(from, into) {
      return orm.company.mergeInto({ from: from as never, into: into as never });
    },
    async contact(company, email) {
      return (await orm.contact.createOne({ companyId: company as never, email })).id;
    },
    async contactCompany(key) {
      return (await orm.contact.findOne({ id: { eq: key as never } }))?.companyId ?? null;
    },
    query,
    close: () => db.drop(),
  };
}

const BASE62 = '0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz';

/** A UUID's hyphenated text from its base62 wire form, as its column holds it; a hyphenated one as it is. */
function uuidText(key: string): string {
  if (key.includes('-')) return key;
  let n = 0n;
  for (const c of key) n = n * 62n + BigInt(BASE62.indexOf(c));
  const hex = n.toString(16).padStart(32, '0');
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
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

    test(`a name another row holds, as its name or an alias, is refused with its key, on ${dialect.name}`, async () => {
      const acme = await t.company('Acme', 'Oslo');
      await t.invoke(acme, 'addAlias', { alias: 'Acme Corp' });
      const refused = await refusalOf(t.company('ACME', 'Bergen'));
      expect(refused.code).toBe('vetoed');
      expect(refused.vetoCode).toBe('name_taken');
      expect(uuidText(String(refused.holder))).toBe(uuidText(acme));
      expect((await refusalOf(t.company('  acme   corp ', 'Bergen'))).vetoCode).toBe('name_taken');
      const globex = await t.company('Globex', 'Bergen');
      expect((await refusalOf(t.invoke(globex, 'rename', { name: 'acme corp' }))).vetoCode).toBe('name_taken');
      expect((await refusalOf(t.invoke(globex, 'addAlias', { alias: 'acme' }))).vetoCode).toBe('name_taken');
      const rows = await t.query(`SELECT "${names.key.column}" AS normalized FROM "${names.table}" ORDER BY 1`, []);
      expect(rows.map(row => row.normalized)).toEqual(['acme', 'acme corp', 'globex']);
    });

    test(`rename keeps the old name as an alias, and resolve finds the row a name or an alias names, on ${dialect.name}`, async () => {
      const initech = await t.company('Initech', 'Austin');
      expect(await t.invoke(initech, 'rename', { name: 'Initech Group' })).toEqual({ name: 'Initech Group', alias: 'Initech' });
      expect(await t.name(initech)).toBe('Initech Group');
      expect(await t.aliases(initech)).toEqual(['Initech']);
      const byAlias = (await t.resolve('Company', 'INITECH')) as { row: { key: string; name: string; alias: boolean } };
      expect(uuidText(byAlias.row.key)).toBe(uuidText(initech));
      expect(byAlias.row).toMatchObject({ name: 'Initech Group', alias: true });
      expect(((await t.resolve('Company', 'initech  group')) as { row: { alias: boolean } }).row.alias).toBe(false);
      expect(await t.resolve('Company', 'Hooli')).toEqual({ row: null });
      expect(await t.invoke(initech, 'rename', { name: 'Initech Group' })).toEqual({ name: 'Initech Group', unchanged: true });
    });

    test(`a removed alias, and a deleted row's names, are freed for another row, on ${dialect.name}`, async () => {
      const umbrella = await t.company('Umbrella', 'Raccoon City');
      await t.invoke(umbrella, 'addAlias', { alias: 'Umbrella Corp' });
      expect((await refusalOf(t.invoke(umbrella, 'removeAlias', { alias: 'umbrella' }))).vetoCode).toBe('is_name');
      expect(await t.invoke(umbrella, 'removeAlias', { alias: 'UMBRELLA CORP' })).toEqual({ alias: 'Umbrella Corp' });
      const corp = await t.company('Umbrella Corp', 'Paris');
      await t.delete(umbrella);
      await t.invoke(corp, 'addAlias', { alias: 'Umbrella' });
      const found = (await t.resolve('Company', 'umbrella')) as { row: { key: string } };
      expect(uuidText(found.row.key)).toBe(uuidText(corp));
    });

    test(`a tag reads its label as a slug, on ${dialect.name}`, async () => {
      await t.tag('Q3 Plan');
      expect((await refusalOf(t.tag('q3-plan'))).vetoCode).toBe('name_taken');
      expect(((await t.resolve('Tag', 'Q3_PLAN')) as { row: { name: string } }).row.name).toBe('Q3 Plan');
    });

    test.skipIf(dialect.name !== 'Postgres')(`a merge moves the aliases of the row merged away, its name among them, to the row that stays, on ${dialect.name}`, async () => {
      const p = t as Awaited<ReturnType<typeof postgresTables>>;
      const from = await p.company('Hooli', 'Palo Alto');
      await p.invoke(from, 'addAlias', { alias: 'Hooli XYZ' });
      const into = await p.company('Nucleus', 'Palo Alto');
      await p.invoke(into, 'addAlias', { alias: 'Nucleus Inc' });
      const contact = await p.contact(from, 'gavin@hooli.example');
      const result = (await p.merge(from, into)) as { repointed: Record<string, number> };
      expect(result.repointed).toEqual({ CompanyNameAliases: 1, CompanyNameNames: 2, Contact: 1 });
      expect(await p.aliases(into)).toEqual(['Hooli', 'Hooli XYZ', 'Nucleus Inc']);
      expect(uuidText(String(await p.contactCompany(contact)))).toBe(uuidText(into));
      for (const name of ['hooli', 'Hooli XYZ', 'nucleus inc', 'NUCLEUS']) {
        const found = (await p.resolve('Company', name)) as { row: { key: string } };
        expect(uuidText(found.row.key)).toBe(uuidText(into));
      }
      expect((await refusalOf(p.company('Hooli', 'Elsewhere'))).vetoCode).toBe('name_taken');
    });
  });
}
