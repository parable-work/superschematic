// fixture-workflow-db's tables end to end (D103): an order whose status is a
// Workflow with a permission on a transition, and a shipment with two, its
// status and its paymentStatus, written in orders.schema.ts as field types
// and read through the TypeScript reader. TestWorkflowTablesEndToEnd
// generates the package, type-checks it, applies the generated DDL and runs
// these cases on each dialect: on bun:sqlite, the SQLite create.sql D27
// plans and the HTTP runtime's adapter over the layout orm.ts embeds
// (fixtureWorkflowDbRules), since the generated repositories run on
// Postgres alone; on Postgres, the generated create.sql and the generated
// repository itself (createOrm). So the generator's layout, its DDL and the
// adapter are held to one another: a create initializes each status and
// stamps when it entered it, the typed operation under each field moves it,
// a move the config has no transition for and a transition whose permission
// the caller lacks are refused in the engine's words, an update takes no
// owned field, a raw SQL write of one is never refused, and a write that
// moves the row under a command runs the command again, up to eight times.
// On Postgres the generated repository's upserts insert a new row as a
// create of the core would and update a held row's own fields alone.
import { Database } from 'bun:sqlite';
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import pg from 'pg';
import { initSync } from '@superschematic/rules';
import {
  MAX_ATTEMPTS,
  RulesRefusal,
  execute,
  readRow,
  runCreate,
  runDelete,
  runUpdate,
  sqliteRulesStore,
  type CoreCaller,
  type RulesHost,
  type RulesStore,
} from '@superschematic/http-runtime/d1';
import { openSqlite, type SqliteDatabase } from '@superschematic/http-runtime/sqlite';
import type { OrmPool, OrmQueryable } from '@superschematic/http-runtime/postgres';
import { createOrm, fixtureWorkflowDbRules as layout } from './orm';
import { alice, database, dsn } from './harness';

type TypeName = 'Order' | 'Shipment';

/** A row as a case reads it on either dialect: each field by its IR name, a timestamp as epoch milliseconds and an absent value as null, and _version. */
type Flat = Record<string, unknown>;

/** What a case runs through: on Postgres the generated repository, on bun:sqlite the adapter over the generated layout. */
interface Tables {
  readonly dialect: string;
  create(type: TypeName, own: Record<string, unknown>): Promise<Flat>;
  read(type: TypeName, key: string): Promise<Flat | null>;
  operate(type: TypeName, key: string, use: string, operation: string, params: unknown, caller?: CoreCaller): Promise<unknown>;
  update(type: TypeName, key: string, patch: Record<string, unknown>): Promise<Flat>;
  delete(type: TypeName, key: string): Promise<void>;
  /** A write past the repository and the adapter, its placeholders $1, $2, ... */
  raw(text: string, values: readonly unknown[]): Promise<number>;
  /** The columns a raw read of the row whose own field matches finds, by column. */
  columns(table: string, where: string, value: unknown): Promise<Record<string, unknown>>;
  /** Makes another connection run text after each of the core's next times reads of a row, which moves the row under the command. */
  move(times: number, text: string, values: readonly unknown[]): void;
  /** How many times the core has read a row since the last call. */
  reads(): number;
  close(): Promise<void>;
}

const tableOf = (type: TypeName) => layout.types[type]!;

/** The timestamp fields of a type, by the layout: each own or owned field that converts epochMillis. */
function timestamps(type: TypeName): Set<string> {
  const table = tableOf(type);
  return new Set([...table.own, ...table.uses.flatMap(use => use.fields)].filter(column => column.convert === 'epochMillis').map(column => column.field));
}

const millis = (value: unknown): number | null => (value === null || value === undefined ? null : new Date(value as string).getTime());

/** A row of the generated repository as a Flat. */
function flatRow(type: TypeName, row: Record<string, unknown>): Flat {
  const out: Flat = {};
  const stamps = timestamps(type);
  for (const [field, value] of Object.entries(row)) out[field] = stamps.has(field) ? millis(value) : (value ?? null);
  return out;
}

/** A row as the core reads it (readRow) as a Flat, its owned fields under the IR names the layout gives them. */
function flatCore(type: TypeName, key: string, row: { own: Record<string, unknown>; owned: Record<string, Record<string, unknown>>; version: number }): Flat {
  const table = tableOf(type);
  const out: Flat = { [table.key.field]: key };
  for (const column of table.own) out[column.field] = row.own[column.field] ?? null;
  for (const use of table.uses) for (const field of use.fields) out[field.field] = row.owned[use.id]?.[field.name] ?? null;
  out._version = row.version;
  return out;
}

/** The core's read of a row, as the adapter writes it (readRow): the one statement that ends in its version. */
const isRowRead = (text: string) => text.startsWith('SELECT ') && text.includes(' AS v FROM ');

/** A mover: a write another connection runs after the core's next reads of a row. */
function mover(run: (text: string, values: readonly unknown[]) => Promise<unknown>) {
  let pending = 0;
  let write: { text: string; values: readonly unknown[] } | undefined;
  let reads = 0;
  return {
    arm(times: number, text: string, values: readonly unknown[]) {
      pending = times;
      write = { text, values };
    },
    async read(text: string) {
      if (!isRowRead(text)) return;
      reads++;
      if (pending > 0 && write) {
        pending--;
        await run(write.text, write.values);
      }
    },
    reads() {
      const n = reads;
      reads = 0;
      return n;
    },
  };
}

/** The tables on bun:sqlite: the generated SQLite DDL in a file of the test's own, and the adapter's commands over the generated layout, as the alice the Postgres repository acts as. */
async function sqliteTables(): Promise<Tables> {
  const dir = mkdtempSync(join(tmpdir(), 'workflow-db-'));
  const file = join(dir, 'workflow.db');
  const ddl = new Database(file, { create: true });
  ddl.exec(readFileSync(new URL('./create.sqlite.sql', import.meta.url), 'utf8'));
  ddl.close();
  const db: SqliteDatabase = openSqlite(file);
  const other: SqliteDatabase = openSqlite(file);
  const sql = (text: string) => text.replace(/\$(\d+)/g, '?$1');
  const moves = mover((text, values) => other.prepare(sql(text)).bind(...(values as never[])).run());
  const inner = sqliteRulesStore(db);
  const store: RulesStore = {
    dialect: 'sqlite',
    async query(text, values) {
      const rows = await inner.query(text, values);
      await moves.read(text);
      return rows;
    },
    apply: statements => inner.apply(statements),
    lock: key => inner.lock(key),
  };
  const host: RulesHost = { core: initSync(), layout, store };
  const caller: CoreCaller = { subject: alice, permissions: [] };
  const read = async (type: TypeName, key: string) => {
    const row = await readRow(host, type, key);
    return row === null ? null : flatCore(type, key, row as never);
  };
  return {
    dialect: 'bun:sqlite',
    async create(type, own) {
      const key = await runCreate(host, type, own, caller);
      return (await read(type, key))!;
    },
    read,
    async operate(type, key, use, operation, params, as) {
      const command = { caller: as ?? caller, now: Date.now(), action: { kind: 'operation' as const, key, useId: use, operation, params } };
      return (await execute(host, type, command)).result;
    },
    async update(type, key, patch) {
      await runUpdate(host, type, key, patch, caller);
      return (await read(type, key))!;
    },
    delete: (type, key) => runDelete(host, type, key, caller),
    async raw(text, values) {
      return (await other.prepare(sql(text)).bind(...(values as never[])).run()).meta.changes;
    },
    async columns(table, where, value) {
      return (await db.prepare(`SELECT * FROM "${table}" WHERE "${where}" = ?1`).bind(value as never).first())!;
    },
    move: (times, text, values) => moves.arm(times, text, values),
    reads: () => moves.reads(),
    async close() {
      db.close();
      other.close();
      rmSync(dir, { recursive: true, force: true });
    },
  };
}

/** A pool over pool whose connections tell moves of each statement they run. */
function watched(pool: pg.Pool, moves: ReturnType<typeof mover>): OrmPool {
  const watch = (q: OrmQueryable): OrmQueryable => ({
    async query(config) {
      const result = await q.query(config as never);
      await moves.read(config.text);
      return result as never;
    },
  });
  return {
    ...watch(pool as unknown as OrmQueryable),
    async connect() {
      const client = await pool.connect();
      return { ...watch(client as unknown as OrmQueryable), release: (err?: Error | boolean) => client.release(err) };
    },
  };
}

/** The tables on Postgres: the generated create.sql in a schema of the test's own, and the generated repository, acting as alice. */
async function postgresTables(): Promise<Tables> {
  const db = await database('ts_orm_workflow');
  const moves = mover((text, values) => db.pool.query(text, [...values]));
  const orm = createOrm(watched(db.pool, moves), alice);
  const repo = (type: TypeName): any => (type === 'Order' ? orm.order : orm.shipment);
  const read = async (type: TypeName, key: string) => {
    const row = await repo(type).findOne({ id: { eq: key } });
    return row === null ? null : flatRow(type, row);
  };
  return {
    dialect: 'Postgres',
    async create(type, own) {
      return flatRow(type, await repo(type).createOne(own));
    },
    read,
    operate: (type, key, use, operation, params, caller) => repo(type)[use][operation](key, params, caller ? { caller } : undefined),
    async update(type, key, patch) {
      return flatRow(type, await repo(type).updateOne(key, patch));
    },
    delete: (type, key) => repo(type).deleteOne(key),
    async raw(text, values) {
      return (await db.pool.query(text, [...values])).rowCount ?? 0;
    },
    async columns(table, where, value) {
      return (await db.pool.query(`SELECT * FROM "${table}" WHERE "${where}" = $1`, [value])).rows[0];
    },
    move: (times, text, values) => moves.arm(times, text, values),
    reads: () => moves.reads(),
    close: () => db.drop(),
  };
}

/** The refusal a call threw, which a case holds to the engine's. */
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
      await t?.close();
    });

    test(`a create runs the core: each status starts in its initial state, stamped when it entered it, at version 1, on ${dialect.name}`, async () => {
      const before = Date.now() - 1000;
      const order = await t.create('Order', { title: 'Desk' });
      expect(order).toMatchObject({ title: 'Desk', status: 'draft', _version: 1 });
      expect(order.statusEnteredAt as number).toBeGreaterThanOrEqual(before);
      const shipment = await t.create('Shipment', { reference: 'S-1' });
      expect(shipment).toMatchObject({ reference: 'S-1', status: 'pending', paymentStatus: 'unpaid', _version: 1 });
      expect(shipment.paymentStatusEnteredAt as number).toBeGreaterThanOrEqual(before);
      // The columns are where D103's layout rule puts them: the value field
      // in the field's own column, every other field after it.
      const columns = await t.columns('shipment', 'reference', 'S-1');
      expect(columns).toMatchObject({ status: 'pending', payment_status: 'unpaid' });
      expect(Number(columns._version)).toBe(1);
      expect(millis(columns.status_entered_at)).toBe(shipment.statusEnteredAt as number);
      expect(millis(columns.payment_status_entered_at)).toBe(shipment.paymentStatusEnteredAt as number);
    });

    test(`a field's typed operation moves it along a transition and bumps the version; the other field stays, on ${dialect.name}`, async () => {
      const order = await t.create('Order', { title: 'Chair' });
      expect(await t.operate('Order', order.id as string, 'status', 'transition', { to: 'paid' })).toEqual({ from: 'draft', to: 'paid' });
      const paid = await t.read('Order', order.id as string);
      expect(paid).toMatchObject({ title: 'Chair', status: 'paid', _version: 2 });
      expect(paid!.statusEnteredAt as number).toBeGreaterThanOrEqual(order.statusEnteredAt as number);

      const shipment = await t.create('Shipment', { reference: 'S-2' });
      expect(await t.operate('Shipment', shipment.id as string, 'paymentStatus', 'transition', { to: 'paid' })).toEqual({ from: 'unpaid', to: 'paid' });
      expect(await t.read('Shipment', shipment.id as string)).toMatchObject({
        status: 'pending',
        statusEnteredAt: shipment.statusEnteredAt,
        paymentStatus: 'paid',
        _version: 2,
      });
    });

    test(`a move the config has no transition for is vetoed in the engine's words, and the row stays, on ${dialect.name}`, async () => {
      const order = await t.create('Order', { title: 'Lamp' });
      const key = order.id as string;
      await t.operate('Order', key, 'status', 'transition', { to: 'paid' });
      const vetoed = await refusal(t.operate('Order', key, 'status', 'transition', { to: 'draft' }));
      expect(vetoed.refusal).toEqual({
        code: 'vetoed',
        status: 409,
        class: 'BehaviorVetoError',
        message: `behavior Workflow vetoes transition of Order ${key}: no transition leads from paid to draft; from paid it can move to shipped`,
        details: {
          behavior: 'Workflow',
          use: 'status',
          action: 'transition',
          reason: 'no transition leads from paid to draft; from paid it can move to shipped',
          vetoCode: 'transition_not_allowed',
          vetoDetails: { from: 'paid', to: 'draft', allowed: ['shipped'] },
        },
      });
      expect(vetoed).toMatchObject({ status: 409, code: 'vetoed' });
      expect(await t.read('Order', key)).toMatchObject({ status: 'paid', _version: 2 });
    });

    test(`a transition that names a permission is forbidden to a caller who lacks it, in the engine's words, and runs for one who holds it, on ${dialect.name}`, async () => {
      const order = await t.create('Order', { title: 'Shelf' });
      const key = order.id as string;
      await t.operate('Order', key, 'status', 'transition', { to: 'paid' });
      const forbidden = await refusal(t.operate('Order', key, 'status', 'transition', { to: 'shipped' }));
      expect(forbidden.refusal).toEqual({
        code: 'forbidden',
        status: 403,
        class: 'EngineError',
        message: `${alice} may not move Order ${key}'s status from paid to shipped: the transition needs permission orders.ship`,
      });
      expect(await t.read('Order', key)).toMatchObject({ status: 'paid', _version: 2 });
      const sam: CoreCaller = { subject: 'sam', permissions: ['orders.ship'] };
      expect(await t.operate('Order', key, 'status', 'transition', { to: 'shipped' }, sam)).toEqual({ from: 'paid', to: 'shipped' });
      expect(await t.read('Order', key)).toMatchObject({ status: 'shipped', _version: 3 });
    });

    test(`an update takes no owned field: it writes the own fields, and one that names an owned field is refused, on ${dialect.name}`, async () => {
      const order = await t.create('Order', { title: 'Stool' });
      const key = order.id as string;
      expect(await t.update('Order', key, { title: 'Bar stool' })).toMatchObject({ title: 'Bar stool', status: 'draft', statusEnteredAt: order.statusEnteredAt, _version: 2 });
      // OrderUpdate has no status member (the package's tsc check holds
      // that); one passed anyway is the core's to refuse.
      const owned = await refusal(t.update('Order', key, { status: 'shipped' }));
      expect(owned.refusal).toMatchObject({ code: 'invalid_instance', status: 422, class: 'InstanceValidationError', details: { issues: [{ path: 'status', rule: 'owned' }] } });
      const stamp = await refusal(t.update('Order', key, { statusEnteredAt: Date.now() }));
      expect(stamp.refusal).toMatchObject({ code: 'invalid_instance', status: 422, details: { issues: [{ path: 'statusEnteredAt', rule: 'owned' }] } });
      expect(await t.read('Order', key)).toMatchObject({ title: 'Bar stool', status: 'draft', _version: 2 });
      await t.delete('Order', key);
      expect(await t.read('Order', key)).toBeNull();
    });

    test(`a raw SQL write of an owned column is never refused, and moves the row's version, on ${dialect.name}`, async () => {
      const order = await t.create('Order', { title: 'Rug' });
      // openSqlite counts the bump trigger's own write among the changes.
      expect(await t.raw(`UPDATE "order" SET status = $1 WHERE title = $2`, ['shipped', 'Rug'])).toBeGreaterThan(0);
      expect(await t.read('Order', order.id as string)).toMatchObject({ status: 'shipped', _version: 2 });
      // The core goes on from what the row holds: shipped is terminal.
      const vetoed = await refusal(t.operate('Order', order.id as string, 'status', 'transition', { to: 'paid' }));
      expect(vetoed.refusal.details).toMatchObject({ vetoCode: 'terminal_state' });
    });

    test(`a write that moves the row under a command runs the command again from fresh state, on ${dialect.name}`, async () => {
      const order = await t.create('Order', { title: 'Mirror' });
      const key = order.id as string;
      t.reads();
      t.move(1, `UPDATE "order" SET title = $1 WHERE title = $2`, ['Wall mirror', 'Mirror']);
      expect(await t.operate('Order', key, 'status', 'transition', { to: 'paid' })).toEqual({ from: 'draft', to: 'paid' });
      // Read twice: the first run's write found the row moved and wrote
      // nothing, and the second ran on the row as the other write left it.
      expect(t.reads()).toBe(2);
      expect(await t.read('Order', key)).toMatchObject({ title: 'Wall mirror', status: 'paid', _version: 3 });
    });

    test(`a row that moves under every run is refused as busy after ${MAX_ATTEMPTS} runs, and nothing is written, on ${dialect.name}`, async () => {
      const order = await t.create('Order', { title: 'Vase' });
      const key = order.id as string;
      t.reads();
      t.move(MAX_ATTEMPTS, `UPDATE "order" SET placed_at = NULL WHERE title = $1`, ['Vase']);
      const busy = await refusal(t.operate('Order', key, 'status', 'transition', { to: 'paid' }));
      expect(busy.refusal).toEqual({
        code: 'busy',
        status: 503,
        class: 'EngineError',
        message: `transition of Order ${key}: the row kept moving while the command ran; it ran ${MAX_ATTEMPTS} times and nothing was written, so it may be tried again`,
      });
      expect(t.reads()).toBe(MAX_ATTEMPTS);
      expect(await t.read('Order', key)).toMatchObject({ status: 'draft', _version: 1 + MAX_ATTEMPTS });
    });
  });
}

describe.skipIf(dsn === '')('upserts through the generated repository, on Postgres', () => {
  let db: Awaited<ReturnType<typeof database>>;
  let orm: ReturnType<typeof createOrm>;

  beforeAll(async () => {
    db = await database('ts_orm_workflow_upsert');
    orm = createOrm(db.pool, alice);
  });

  afterAll(async () => {
    await db?.drop();
  });

  test("an upsert by a unique key inserts a new row with what the core's create gives it, and updates a held row's own fields, its states kept", async () => {
    const before = Date.now() - 1000;
    const inserted = await orm.shipment.upsertOneResult({ reference: 'U-1' }, ['reference'], { reference: 'U-1' });
    expect(inserted.status).toBe('inserted');
    expect(inserted.row).toMatchObject({ reference: 'U-1', status: 'pending', paymentStatus: 'unpaid', _version: 1 });
    expect(millis(inserted.row.paymentStatusEnteredAt)!).toBeGreaterThanOrEqual(before);
    expect(await orm.shipment.paymentStatus.transition(inserted.row.id, { to: 'paid' })).toEqual({ from: 'unpaid', to: 'paid' });

    const many = await orm.shipment.upsertMany([{ reference: 'U-1' }, { reference: 'U-2' }], ['reference'], ['reference']);
    expect(many.map(result => [result.status, result.row.reference, result.row.status, result.row.paymentStatus, result.row._version])).toEqual([
      ['updated', 'U-1', 'pending', 'paid', 3],
      ['inserted', 'U-2', 'pending', 'unpaid', 1],
    ]);
  });
});
