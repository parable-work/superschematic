// fixture-numbering-db's tables end to end (D56, D103's step 4): an invoice
// numbered per customer and per UTC year on its number field,
// INV-2026-0042, and a ticket whose number and reference count apart.
// TestNumberingTablesEndToEnd generates the package, type-checks it,
// applies the generated DDL and runs these cases on each dialect: on
// bun:sqlite, the SQLite create.sql D27 plans and the HTTP runtime's
// adapter over the layout orm.ts embeds (fixtureNumberingDbRules), since
// the generated repositories run on Postgres alone; on Postgres, the
// generated create.sql and the generated repository itself (createOrm). So
// the generator's layout, its DDL, its shared table of counts and the
// adapter are held to one another: a create takes the next number of its
// count, a row of the shared table, which a delete leaves, so a delete
// leaves a gap; a ticket's two Numbering fields count apart; assignNumber
// numbers a row made past the repository in the year of its createdAt, and
// on a numbered row writes nothing; and on Postgres creates that number at
// once each take a number of their own.
import { Database } from 'bun:sqlite';
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { initSync } from '@superschematic/rules';
import { execute, readRow, runCreate, runDelete, sqliteRulesStore, type CoreCaller, type RulesHost } from '@superschematic/http-runtime/d1';
import { openSqlite, type SqliteDatabase } from '@superschematic/http-runtime/sqlite';
import { createOrm, fixtureNumberingDbRules as layout } from './orm';
import { alice, database, dsn } from './harness';

const counts = layout.types.Invoice!.uses.find(use => use.id === 'number')!.shared!.find(table => table.name === 'counts')!;

/** What a case runs through: on Postgres the generated repository, on bun:sqlite the adapter over the generated layout. */
interface Tables {
  readonly dialect: string;
  /** Creates a customer past the core, as any table's row, and resolves to its key. */
  customer(name: string): Promise<string>;
  /** Creates an invoice for a customer through the core and resolves to its key. */
  invoice(customer: string, title: string): Promise<string>;
  /** Creates a ticket through the core and resolves to its key. */
  ticket(subject: string): Promise<string>;
  /** A row's labels, by field, null for none. */
  labels(type: 'Invoice' | 'Ticket', key: string): Promise<Record<string, unknown>>;
  /** Runs a row's number's assignNumber. */
  assignNumber(type: 'Invoice' | 'Ticket', key: string): Promise<unknown>;
  delete(type: 'Invoice' | 'Ticket', key: string): Promise<void>;
  /** A row's version. */
  version(type: 'Invoice' | 'Ticket', key: string): Promise<number>;
  /** A read past the repository and the adapter, its placeholders $1, $2, ... */
  query(text: string, values: readonly unknown[]): Promise<Record<string, unknown>[]>;
  /** A write past the repository and the adapter, its placeholders $1, $2, ... */
  raw(text: string, values: readonly unknown[]): Promise<void>;
  close(): Promise<void>;
}

/** The tables on bun:sqlite: the generated SQLite DDL in a file of the test's own, and the adapter over the generated layout, as alice. */
async function sqliteTables(): Promise<Tables> {
  const dir = mkdtempSync(join(tmpdir(), 'numbering-db-'));
  const file = join(dir, 'numbering.db');
  const ddl = new Database(file, { create: true });
  ddl.exec(readFileSync(new URL('./create.sqlite.sql', import.meta.url), 'utf8'));
  ddl.close();
  const db: SqliteDatabase = openSqlite(file);
  const sql = (text: string) => text.replace(/\$(\d+)/g, '?$1');
  const query = async (text: string, values: readonly unknown[]) => (await db.prepare(sql(text)).bind(...(values as never[])).all()).results;
  const host: RulesHost = { core: initSync(), layout, store: sqliteRulesStore(db) };
  const caller: CoreCaller = { subject: alice, permissions: [] };
  return {
    dialect: 'bun:sqlite',
    async customer(name) {
      const id = crypto.randomUUID();
      await db.prepare('INSERT INTO customer (id, name) VALUES (?1, ?2)').bind(id, name).run();
      return base62(id);
    },
    invoice: (customer, title) => runCreate(host, 'Invoice', { customerId: customer, title }, caller),
    ticket: subject => runCreate(host, 'Ticket', { subject }, caller),
    async labels(type, key) {
      const row = await readRow(host, type, key);
      return Object.fromEntries(layout.types[type]!.uses.map(use => [use.id, row?.owned?.[use.id]?.label ?? null]));
    },
    async assignNumber(type, key) {
      return (await execute(host, type, { caller, now: Date.now(), action: { kind: 'operation', key, useId: 'number', operation: 'assignNumber', params: {} } })).result;
    },
    delete: (type, key) => runDelete(host, type, key, caller),
    async version(type, key) {
      return (await readRow(host, type, key))!.version;
    },
    query,
    async raw(text, values) {
      await db.prepare(sql(text)).bind(...(values as never[])).run();
    },
    async close() {
      db.close();
      rmSync(dir, { recursive: true, force: true });
    },
  };
}

/** The tables on Postgres: the generated create.sql in a schema of the test's own, and the generated repository, acting as alice. */
async function postgresTables(): Promise<Tables> {
  const db = await database('ts_orm_numbering');
  const orm = createOrm(db.pool, alice);
  const query = async (text: string, values: readonly unknown[]) => (await db.pool.query(text, [...values])).rows;
  const repository = (type: 'Invoice' | 'Ticket') => (type === 'Invoice' ? orm.invoice : orm.ticket) as any;
  return {
    dialect: 'Postgres',
    async customer(name) {
      return (await orm.customer.createOne({ name })).id;
    },
    async invoice(customer, title) {
      return (await orm.invoice.createOne({ customerId: customer, title } as never)).id;
    },
    async ticket(subject) {
      return (await orm.ticket.createOne({ subject })).id;
    },
    async labels(type, key) {
      const row = await repository(type).findOne({ id: { eq: key } });
      return type === 'Invoice' ? { number: row?.number ?? null } : { number: row?.number ?? null, reference: row?.reference ?? null };
    },
    assignNumber: (type, key) => repository(type).number.assignNumber(key, {}),
    delete: (type, key) => repository(type).deleteOne(key),
    async version(type, key) {
      return Number((await repository(type).findOne({ id: { eq: key } }))._version);
    },
    query,
    async raw(text, values) {
      await db.pool.query(text, [...values]);
    },
    close: () => db.drop(),
  };
}

const BASE62 = '0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz';

/** A UUID's base62 wire form from its hyphenated text, as the core names a key. */
function base62(uuid: string): string {
  let n = BigInt(`0x${uuid.replace(/-/g, '')}`);
  let out = '';
  while (n > 0n) {
    out = BASE62[Number(n % 62n)] + out;
    n /= 62n;
  }
  return out.padStart(22, '0');
}

/** A UUID's hyphenated text from its base62 wire form, as its column holds it. */
function uuidText(key: string): string {
  let n = 0n;
  for (const c of key) n = n * 62n + BigInt(BASE62.indexOf(c));
  const hex = n.toString(16).padStart(32, '0');
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

const dialects: { readonly name: string; readonly skip: boolean; readonly open: () => Promise<Tables> }[] = [
  { name: 'bun:sqlite', skip: false, open: sqliteTables },
  { name: 'Postgres', skip: dsn === '', open: postgresTables },
];

const year = new Date().getUTCFullYear();

for (const dialect of dialects) {
  describe.skipIf(dialect.skip)(dialect.name, () => {
    let t: Tables;

    beforeAll(async () => {
      t = await dialect.open();
    });

    afterAll(async () => {
      await t?.close();
    });

    test(`a create takes the next number of its customer's count of the year, a row of the shared table, on ${dialect.name}`, async () => {
      const c1 = await t.customer('Acme');
      const c2 = await t.customer('Initech');
      const first = await t.invoice(c1, 'Desks');
      const second = await t.invoice(c1, 'Chairs');
      const other = await t.invoice(c2, 'Lamps');
      expect(await t.labels('Invoice', first)).toEqual({ number: `INV-${year}-0001` });
      expect(await t.labels('Invoice', second)).toEqual({ number: `INV-${year}-0002` });
      expect(await t.labels('Invoice', other)).toEqual({ number: `INV-${year}-0001` });
      const rows = await t.query(`SELECT "${counts.key.column}" AS scope, "${counts.fields[0]!.column}" AS last, "${counts.version}" AS version FROM "${counts.table}" WHERE "${counts.key.column}" = $1`, [`${c1}/${year}`]);
      expect(rows.map(row => ({ scope: row.scope, last: Number(row.last), version: Number(row.version) }))).toEqual([{ scope: `${c1}/${year}`, last: 2, version: 2 }]);
    });

    test(`a ticket's number and reference count apart, each in its own shared table, on ${dialect.name}`, async () => {
      const one = await t.ticket('Login fails');
      const two = await t.ticket('Slow page');
      expect(await t.labels('Ticket', one)).toEqual({ number: 'T-1', reference: 'REF-00001' });
      expect(await t.labels('Ticket', two)).toEqual({ number: 'T-2', reference: 'REF-00002' });
    });

    test(`a delete leaves the count where it was: a gap, on ${dialect.name}`, async () => {
      const c3 = await t.customer('Globex');
      await t.invoice(c3, 'One');
      const two = await t.invoice(c3, 'Two');
      await t.delete('Invoice', two);
      const three = await t.invoice(c3, 'Three');
      expect(await t.labels('Invoice', three)).toEqual({ number: `INV-${year}-0003` });
    });

    test(`assignNumber numbers a row made past the repository in the year of its createdAt, and on a numbered row writes nothing, on ${dialect.name}`, async () => {
      const c4 = await t.customer('Hooli');
      const id = crypto.randomUUID();
      const made = dialect.name === 'Postgres' ? new Date(Date.UTC(2025, 5, 1)) : new Date(Date.UTC(2025, 5, 1)).toISOString();
      await t.raw('INSERT INTO invoice (id, customer_id, title, created_at) VALUES ($1, $2, $3, $4)', [id, uuidText(c4), 'Old', made]);
      const key = base62(id);
      expect(await t.labels('Invoice', key)).toEqual({ number: null });
      expect(await t.assignNumber('Invoice', key)).toEqual({ seq: 1, label: 'INV-2025-0001' });
      expect(await t.labels('Invoice', key)).toEqual({ number: 'INV-2025-0001' });
      const version = await t.version('Invoice', key);
      expect(await t.assignNumber('Invoice', key)).toEqual({ seq: 1, label: 'INV-2025-0001' });
      expect(await t.version('Invoice', key)).toBe(version);
    });

    test(`creates that number at once take one number each, on ${dialect.name}`, async () => {
      const c5 = await t.customer('Umbrella');
      const keys = await Promise.all(['a', 'b', 'c', 'd'].map(title => t.invoice(c5, title)));
      const labels = (await Promise.all(keys.map(key => t.labels('Invoice', key)))).map(row => row.number).sort();
      expect(labels).toEqual([1, 2, 3, 4].map(n => `INV-${year}-000${n}`));
    });
  });
}
