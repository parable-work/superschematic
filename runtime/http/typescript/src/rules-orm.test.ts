import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import pg from 'pg';
import { NotFoundError, ormOf, type Clauses, type Orm, type OrmDefinition, type TableDefinition } from './orm';
import { RulesRefusal, withRulesCore, type RulesCallOptions, type RulesLayout } from './rules';
import { bumpVersion, rulesCore, rulesDsn } from './rules.testing';
import { uuidToDb } from './uuid';

// The generated repositories' runtime over a table that composes Workflow
// on status (D103), as tsgen writes its definition and layout
// (internal/generator/tsgen's fixture-workflow-db golden): createOne,
// updateOne and deleteOne go through the core in a transaction, an upsert
// inserts what the core's create gives a row, and orm.order.status holds
// Workflow's operations. Skipped without
// SUPERSCHEMATIC_RULES_TEST_DATABASE_URL; the generated package's own
// end-to-end test runs the generated definition itself.

/** The clauses tsgen writes for Postgres, which an upsert's statements are built from. */
const clauses: Clauses = {
  eq: '%s = $%d',
  gt: '%s > $%d',
  gte: '%s >= $%d',
  lt: '%s < $%d',
  lte: '%s <= $%d',
  ilike: '%s ILIKE $%d',
  in: '%s IN (%s)',
  isNull: '%s IS NULL',
  isNotNull: '%s IS NOT NULL',
  search: "%s ILIKE $%d ESCAPE '\\'",
  exists: 'EXISTS (%s)',
  notExists: 'NOT EXISTS (%s)',
  set: '%s = $%d',
  setNull: '%s = NULL',
  pageKey: 'page_key.%s',
  placeholder: '$%d',
  default: 'DEFAULT',
  or: ' OR ',
  group: '(%s)',
  where: ' WHERE ',
  and: ' AND ',
  orderBy: ' ORDER BY ',
  orderTerm: '%s %s',
  limit: ' LIMIT %d',
  offset: ' OFFSET %d',
  setExcluded: '%s = EXCLUDED.%s',
  excluded: 'EXCLUDED.%s',
  changed: '(%s) IS DISTINCT FROM (%s)',
  never: 'FALSE',
  null: 'NULL',
  notIn: '%s NOT IN (%s)',
};

const columnList = '"id", "title", "status", "status_entered_at", "updated_at", "_version"';

const orderTable: TableDefinition = {
  name: 'Order',
  table: '"order"',
  key: 'id',
  columns: [
    { name: 'id', column: 'id', field: 'id', qualified: '"order".id', uuid: true, key: true, auto: true, required: true },
    { name: 'title', column: 'title', field: 'title', qualified: '"order".title', required: true },
    { name: 'status', column: 'status', field: 'status', qualified: '"order".status', required: true },
    { name: 'statusEnteredAt', column: 'status_entered_at', field: 'statusEnteredAt', qualified: '"order".status_entered_at', timestamp: true },
    { name: 'updatedAt', column: 'updated_at', field: 'updatedAt', qualified: '"order".updated_at', timestamp: true, audit: 'updatedAt' },
    { name: '_version', column: '_version', field: '_version', qualified: '"order"._version', int64: true, auto: true, required: true, internal: true },
  ],
  statements: {
    columns: columnList,
    qualifiedColumns: '',
    getOne: `SELECT ${columnList} FROM "order" WHERE id = $1`,
    getMany: '',
    live: '',
    liveWhere: '',
    liveCondition: '',
    findOne: '',
    findMany: '',
    count: '',
    page: '',
    pageAfterKey: '',
    pageAfter: '',
    pageOrder: '',
    pageTiebreak: '',
    pageKeyExists: '',
    insert: 'INSERT INTO "order" (%s) VALUES (%s) RETURNING %s',
    insertMany: '',
    update: 'UPDATE "order" SET %s WHERE id = $%d',
    returning: ` RETURNING ${columnList}`,
    updateMany: '',
    updatedAt: 'updated_at = $%d',
    updatedBy: 'updated_by = $%d',
    upsert: `INSERT INTO "order" (%s) VALUES %s ON CONFLICT %s DO UPDATE SET %s%s RETURNING ${columnList}, (xmax = 0) AS _inserted`,
    softDelete: '',
    softDeleteMany: '',
    delete: 'DELETE FROM "order" WHERE id = $1',
    deleteMany: '',
  },
  uniqueKeys: {
    id: { conflict: '(id)', keep: 'id = EXCLUDED.id', rows: `SELECT ${columnList} FROM "order" WHERE (id) IN (%s)` },
  },
};

const ticketColumns = '"id", "title", "status", "status_entered_at", "deleted_at", "_version"';

/** A soft-deletable table that composes Workflow on status. */
const ticketTable: TableDefinition = {
  ...orderTable,
  name: 'Ticket',
  table: 'ticket',
  softDelete: true,
  columns: [
    { name: 'id', column: 'id', field: 'id', qualified: 'ticket.id', uuid: true, key: true, auto: true, required: true },
    { name: 'title', column: 'title', field: 'title', qualified: 'ticket.title', required: true },
    { name: 'status', column: 'status', field: 'status', qualified: 'ticket.status', required: true },
    { name: 'statusEnteredAt', column: 'status_entered_at', field: 'statusEnteredAt', qualified: 'ticket.status_entered_at', timestamp: true },
    { name: 'deletedAt', column: 'deleted_at', field: 'deletedAt', qualified: 'ticket.deleted_at', timestamp: true, audit: 'deletedAt' },
    { name: '_version', column: '_version', field: '_version', qualified: 'ticket._version', int64: true, auto: true, required: true, internal: true },
  ],
  statements: {
    ...orderTable.statements,
    columns: ticketColumns,
    getOne: `SELECT ${ticketColumns} FROM ticket WHERE id = $1`,
    live: ' AND deleted_at IS NULL',
    softDelete: 'UPDATE ticket SET deleted_at = $1 WHERE id = $2 AND deleted_at IS NULL',
  },
};

const flow = {
  initial: 'draft',
  states: ['draft', 'paid', 'shipped'],
  transitions: [
    { from: 'draft', to: 'paid' },
    { from: 'paid', permission: 'orders.ship', to: 'shipped' },
  ],
};

const rules: { types: Record<string, RulesLayout['types'][string]> } = {
  types: {
    Order: {
      type: 'Order',
      table: 'order',
      key: { field: 'id', column: 'id', type: 'Identity.UUID' },
      version: '_version',
      own: [{ field: 'title', column: 'title', type: 'string' }],
      uses: [
        {
          id: 'status',
          behavior: 'Workflow',
          anchor: 'status',
          fields: [
            { name: 'status', field: 'status', column: 'status', type: 'string', value: true },
            { name: 'enteredAt', field: 'statusEnteredAt', column: 'status_entered_at', type: 'Temporal.DateTime', convert: 'epochMillis' },
          ],
          operations: [{ name: 'transition', writes: true }],
        },
      ],
      spec: { name: 'Order', uses: [{ behavior: 'Workflow', config: flow, anchor: { field: 'status' } }] },
    },
  },
};

rules.types.Ticket = { ...rules.types.Order!, type: 'Ticket', table: 'ticket', live: { column: 'deleted_at' }, spec: { ...rules.types.Order!.spec, name: 'Ticket' } };

const definition: OrmDefinition = { clauses, defaultLimit: 100, historyActorCTE: '', clearHistoryActor: '', tables: { order: orderTable, ticket: ticketTable }, rules };

interface OrderRow {
  id: string;
  title: string;
  status: string;
  statusEnteredAt: Date | null;
  updatedAt: Date;
  _version: number;
}

interface TicketRow {
  id: string;
  status: string;
  deletedAt: Date | null;
  _version: number;
}

interface Upserted<Row> {
  row: Row;
  status: 'inserted' | 'updated' | 'unchanged';
}

interface Repositories {
  readonly ticket: {
    createOne(input: { title: string }): Promise<TicketRow>;
    getOne(id: string): Promise<TicketRow>;
    updateOne(id: string, update: { title?: string }): Promise<TicketRow>;
    deleteOne(id: string): Promise<void>;
    readonly status: { transition(key: string, params: { to: string }, options?: RulesCallOptions): Promise<{ from: string; to: string }> };
  };
  readonly order: {
    createOne(input: { id?: string; title: string }): Promise<OrderRow>;
    createMany(inputs: readonly { id?: string; title: string }[]): Promise<OrderRow[]>;
    getOne(id: string): Promise<OrderRow>;
    updateOne(id: string, update: { title?: string }): Promise<OrderRow>;
    upsertOne(input: { id?: string; title: string }, on: readonly ['id'], update: { title?: string }): Promise<OrderRow>;
    upsertOneResult(input: { id?: string; title: string }, on: readonly ['id'], update: { title?: string }): Promise<Upserted<OrderRow>>;
    upsertMany(inputs: readonly { id?: string; title: string }[], on: readonly ['id'], update: readonly 'title'[]): Promise<Upserted<OrderRow>[]>;
    deleteOne(id: string): Promise<void>;
    readonly status: { transition(key: string, params: { to: string }, options?: RulesCallOptions): Promise<{ from: string; to: string }> };
  };
}

describe.skipIf(rulesCore === undefined || rulesDsn === '')('a repository of a table that composes behaviors, on Postgres', () => {
  const schema = `rules_orm_${process.pid}_${Date.now()}`;
  let admin: pg.Pool;
  let pool: pg.Pool;
  let orm: Orm<Repositories>;

  beforeAll(async () => {
    admin = new pg.Pool({ connectionString: rulesDsn, max: 1 });
    await admin.query(`CREATE SCHEMA ${schema}`);
    pool = new pg.Pool({ connectionString: rulesDsn, max: 4, options: `-c search_path=${schema}` });
    await pool.query(`CREATE TABLE "order" (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  title TEXT NOT NULL,
  status TEXT,
  status_entered_at TIMESTAMPTZ,
  updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  _version BIGINT DEFAULT 1 NOT NULL
)`);
    for (const ddl of bumpVersion('order')) await pool.query(ddl);
    await pool.query(`CREATE TABLE ticket (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  title TEXT NOT NULL,
  status TEXT,
  status_entered_at TIMESTAMPTZ,
  deleted_at TIMESTAMPTZ,
  _version BIGINT DEFAULT 1 NOT NULL
)`);
    for (const ddl of bumpVersion('ticket')) await pool.query(ddl);
    orm = ormOf<Repositories>(pool, definition, withRulesCore(null, () => rulesCore!));
  });
  afterAll(async () => {
    await pool?.end();
    await admin?.query(`DROP SCHEMA ${schema} CASCADE`);
    await admin?.end();
  });

  let id: string;

  test("createOne runs Workflow's initializer: the row starts in the initial state, its key made in its wire form", async () => {
    const before = Date.now();
    const row = await orm.order.createOne({ title: 'Desk' });
    id = row.id;
    expect(row.id).toMatch(/^[0-9A-Za-z]{1,22}$/);
    expect(row).toMatchObject({ title: 'Desk', status: 'draft', _version: 1 });
    expect(row.statusEnteredAt!.getTime()).toBeGreaterThanOrEqual(before - 1);
  });

  test('createMany runs a create of the core for each row, in one transaction', async () => {
    const rows = await orm.order.createMany([{ title: 'Bench' }, { title: 'Cabinet' }]);
    expect(rows.map(row => [row.title, row.status, row._version])).toEqual([
      ['Bench', 'draft', 1],
      ['Cabinet', 'draft', 1],
    ]);
  });

  test("a use's operations sit under its id, run as the acting user holding no permissions unless the call names its caller", async () => {
    const before = await orm.order.getOne(id);
    expect(await orm.order.status.transition(id, { to: 'paid' })).toEqual({ from: 'draft', to: 'paid' });
    const paid = await orm.order.getOne(id);
    expect(paid).toMatchObject({ status: 'paid', _version: 2 });
    expect(paid.updatedAt.getTime()).toBeGreaterThanOrEqual(before.updatedAt.getTime());
    await expect(orm.order.status.transition(id, { to: 'shipped' })).rejects.toMatchObject({ code: 'forbidden', status: 403 });
    expect(await orm.order.status.transition(id, { to: 'shipped' }, { caller: { subject: 'sam', permissions: ['orders.ship'] } })).toEqual({ from: 'paid', to: 'shipped' });
  });

  test('updateOne writes own fields through the core, and refuses an owned one', async () => {
    const row = await orm.order.updateOne(id, { title: 'Chair' });
    expect(row).toMatchObject({ title: 'Chair', status: 'shipped', _version: 4 });
    await expect(orm.order.updateOne(id, { status: 'draft' } as never)).rejects.toMatchObject({ code: 'invalid_instance', status: 422 });
    await expect(orm.order.updateOne(id, { statusEnteredAt: null } as never)).rejects.toBeInstanceOf(RulesRefusal);
    await expect(orm.order.createOne({ title: 'Lamp', status: 'paid' } as never)).rejects.toMatchObject({ code: 'invalid_instance' });
  });

  test("an upsert inserts a new row with what the core's create gives it, and updates a held row's own fields alone, its state kept", async () => {
    const before = Date.now();
    const inserted = await orm.order.upsertOneResult({ title: 'Crate' }, ['id'], { title: 'Crate' });
    expect(inserted.status).toBe('inserted');
    expect(inserted.row).toMatchObject({ title: 'Crate', status: 'draft', _version: 1 });
    expect(inserted.row.statusEnteredAt!.getTime()).toBeGreaterThanOrEqual(before - 1);
    expect(await orm.order.status.transition(inserted.row.id, { to: 'paid' })).toEqual({ from: 'draft', to: 'paid' });
    const entered = (await orm.order.getOne(inserted.row.id)).statusEnteredAt!.getTime();

    const updated = await orm.order.upsertOneResult({ id: inserted.row.id, title: 'Crate' }, ['id'], { title: 'Box' });
    expect(updated.status).toBe('updated');
    expect(updated.row).toMatchObject({ id: inserted.row.id, title: 'Box', status: 'paid', _version: 3 });
    expect(updated.row.statusEnteredAt!.getTime()).toBe(entered);
    expect(await orm.order.upsertOne({ id: inserted.row.id, title: 'Crate' }, ['id'], { title: 'Bin' })).toMatchObject({ title: 'Bin', status: 'paid', _version: 4 });

    const many = await orm.order.upsertMany([{ id: inserted.row.id, title: 'Tub' }, { title: 'Tray' }], ['id'], ['title']);
    expect(many.map(result => [result.status, result.row.title, result.row.status, result.row._version])).toEqual([
      ['updated', 'Tub', 'paid', 5],
      ['inserted', 'Tray', 'draft', 1],
    ]);
    expect(many[1]!.row.statusEnteredAt).toBeInstanceOf(Date);
  });

  test('an upsert that names an owned field is refused, in its input as createOne refuses it and in its update, and writes nothing', async () => {
    const repo = orm.order as any;
    await expect(repo.upsertOne({ title: 'Lamp', status: 'paid' }, ['id'], { title: 'Lamp' })).rejects.toMatchObject({ code: 'invalid_instance' });
    await expect(repo.upsertOne({ title: 'Lamp' }, ['id'], { status: 'paid' })).rejects.toThrow(
      'orm: upsertOne of Order sets the fields an update sets, not "status", which behavior Workflow on status owns (D103): only its operations change it'
    );
    await expect(repo.upsertMany([{ title: 'Lamp' }], ['id'], ['statusEnteredAt'])).rejects.toThrow(
      'orm: upsertMany of Order sets the fields an update sets, not "statusEnteredAt", which behavior Workflow on status owns (D103): only its operations change it'
    );
    const [{ lamps }] = (await pool.query(`SELECT COUNT(*)::int AS lamps FROM "order" WHERE title = 'Lamp'`)).rows;
    expect(lamps).toBe(0);
  });

  test('an update and a delete of a key no row holds throw NotFoundError, and deleteOne takes the row through the core', async () => {
    const ghost = '6fe1XoZ0UJpMNNdUOSMC2X';
    await expect(orm.order.updateOne(ghost, { title: 'x' })).rejects.toBeInstanceOf(NotFoundError);
    await expect(orm.order.deleteOne(ghost)).rejects.toBeInstanceOf(NotFoundError);
    await orm.order.deleteOne(id);
    await expect(orm.order.getOne(id)).rejects.toBeInstanceOf(NotFoundError);
  });

  test("a command runs in the caller's transaction: a refusal leaves it going on, and its rollback undoes the command", async () => {
    let kept: string | undefined;
    await expect(
      orm.transaction(async tx => {
        const row = await tx.order.createOne({ title: 'Shelf' });
        kept = row.id;
        await expect(tx.order.status.transition(row.id, { to: 'shipped' })).rejects.toMatchObject({ code: 'vetoed' });
        expect(await tx.order.status.transition(row.id, { to: 'paid' })).toEqual({ from: 'draft', to: 'paid' });
        throw new Error('roll back');
      })
    ).rejects.toThrow('roll back');
    await expect(orm.order.getOne(kept!)).rejects.toBeInstanceOf(NotFoundError);
  });

  test('a raw SQL write of an owned column is never refused', async () => {
    const row = await orm.order.createOne({ title: 'Stool' });
    expect(await orm.exec(`UPDATE "order" SET status = 'shipped' WHERE id = $1`, [uuidToDb(row.id)])).toBe(1);
    expect((await orm.order.getOne(row.id)).status).toBe('shipped');
  });

  test("deleteOne of a soft-deletable table stamps the live row deleted through the core, held to its version", async () => {
    const acting = orm.withUser('6fe1XoZ0UJpMNNdUOSMC2X');
    const ticket = await acting.ticket.createOne({ title: 'Leak' });
    await acting.ticket.deleteOne(ticket.id);
    await expect(acting.ticket.getOne(ticket.id)).rejects.toBeInstanceOf(NotFoundError);
    await expect(acting.ticket.deleteOne(ticket.id)).rejects.toBeInstanceOf(NotFoundError);
    const [row] = (await pool.query('SELECT status, deleted_at, _version FROM ticket WHERE id = $1', [uuidToDb(ticket.id)])).rows;
    expect(row.status).toBe('draft');
    expect(row.deleted_at).toBeInstanceOf(Date);
    expect(Number(row._version)).toBe(2);
  });

  test('an operation and an update of a soft-deleted row throw NotFoundError and write nothing, inside a transaction whose caller goes on', async () => {
    const acting = orm.withUser('6fe1XoZ0UJpMNNdUOSMC2X');
    const ticket = await acting.ticket.createOne({ title: 'Drip' });
    await acting.ticket.deleteOne(ticket.id);
    const stored = async () => (await pool.query('SELECT title, status, deleted_at, _version FROM ticket WHERE id = $1', [uuidToDb(ticket.id)])).rows[0];
    const gone = await stored();
    await expect(acting.ticket.status.transition(ticket.id, { to: 'paid' })).rejects.toBeInstanceOf(NotFoundError);
    await acting.transaction(async tx => {
      await expect(tx.ticket.status.transition(ticket.id, { to: 'paid' })).rejects.toBeInstanceOf(NotFoundError);
      await expect(tx.ticket.updateOne(ticket.id, { title: 'Changed' })).rejects.toBeInstanceOf(NotFoundError);
    });
    expect(await stored()).toEqual(gone);
    expect(gone).toMatchObject({ title: 'Drip', status: 'draft' });
    expect(Number(gone._version)).toBe(2);
  });

  test('an ORM opened with no core refuses a write of the table, and a use named as a member of the repository is refused', async () => {
    const bare = ormOf<Repositories>(pool, definition);
    await expect(bare.order.createOne({ title: 'Desk' })).rejects.toThrow('opened with no core');
    const clash: RulesLayout = { types: { Order: { ...rules.types.Order!, uses: [{ ...rules.types.Order!.uses[0]!, id: 'getOne' }] } } };
    expect(() => ormOf(pool, { ...definition, rules: clash })).toThrow("Order's behavior Workflow on getOne would be the repository's own member getOne");
  });
});
