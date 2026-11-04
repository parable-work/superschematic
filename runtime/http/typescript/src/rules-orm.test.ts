import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import pg from 'pg';
import { NotFoundError, ormOf, type Clauses, type Orm, type OrmDefinition, type TableDefinition, type WriteOptions } from './orm';
import { RulesRefusal, withRulesCore, type RulesCallOptions, type RulesLayout } from './rules';
import { bumpVersion, rulesCore, rulesDsn } from './rules.testing';
import { uuidToDb } from './uuid';

// The generated repositories' runtime over a table that composes Workflow
// on status (D103), as tsgen writes its definition and layout
// (internal/generator/tsgen's fixture-workflow-db golden): createOne,
// updateOne and deleteOne go through the core in a transaction, an upsert
// inserts what the core's create gives a row and updates a row its key
// holds through the core's update, and orm.order.status holds Workflow's
// operations; and over a table that implements Constants and StateRules
// beside its Workflow, with no sub-table and a unique key, whose upserts
// are held to the rules those uses hold an update to (D104). Postgres
// alone, the ORM's one dialect. Skipped without
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
    updateOne(id: string, update: { title?: string }, options?: WriteOptions): Promise<OrderRow>;
    upsertOne(input: { id?: string; title: string }, on: readonly ['id'], update: { title?: string }): Promise<OrderRow>;
    upsertOneResult(input: { id?: string; title: string }, on: readonly ['id'], update: { title?: string }): Promise<Upserted<OrderRow>>;
    upsertMany(inputs: readonly { id?: string; title: string }[], on: readonly ['id'], update: readonly 'title'[]): Promise<Upserted<OrderRow>[]>;
    deleteOne(id: string, options?: WriteOptions): Promise<void>;
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

  test("withBehaviorCaller runs the commands as its caller, kept by withUser's copy and a transaction, a call's caller still first, and null unsets it", async () => {
    const shipper = orm.withBehaviorCaller({ subject: 'sam', permissions: ['orders.ship'] });
    const paid = async () => {
      const row = await shipper.order.createOne({ title: 'Crate' });
      expect(await shipper.order.status.transition(row.id, { to: 'paid' })).toEqual({ from: 'draft', to: 'paid' });
      return row.id;
    };
    const first = await paid();
    await expect(orm.order.status.transition(first, { to: 'shipped' })).rejects.toMatchObject({ code: 'forbidden', status: 403 });
    expect(await shipper.withUser('6fe1XoZ0UJpMNNdUOSMC2X').order.status.transition(first, { to: 'shipped' })).toEqual({ from: 'paid', to: 'shipped' });
    const second = await paid();
    expect(await shipper.transaction(tx => tx.order.status.transition(second, { to: 'shipped' }))).toEqual({ from: 'paid', to: 'shipped' });
    const third = await paid();
    await expect(shipper.order.status.transition(third, { to: 'shipped' }, { caller: { subject: 'ann', permissions: [] } })).rejects.toMatchObject({ code: 'forbidden' });
    await expect(shipper.withBehaviorCaller(null).order.status.transition(third, { to: 'shipped' })).rejects.toMatchObject({ code: 'forbidden' });
    await expect(shipper.transaction(async tx => tx.withBehaviorCaller(null).order.status.transition(third, { to: 'shipped' }))).rejects.toMatchObject({ code: 'forbidden' });
    expect(await orm.order.getOne(third)).toMatchObject({ status: 'paid' });
  });

  test('updateOne writes own fields through the core, and refuses an owned one', async () => {
    const row = await orm.order.updateOne(id, { title: 'Chair' });
    expect(row).toMatchObject({ title: 'Chair', status: 'shipped', _version: 4 });
    await expect(orm.order.updateOne(id, { status: 'draft' } as never)).rejects.toMatchObject({ code: 'invalid_instance', status: 422 });
    await expect(orm.order.updateOne(id, { statusEnteredAt: null } as never)).rejects.toBeInstanceOf(RulesRefusal);
    await expect(orm.order.createOne({ title: 'Lamp', status: 'paid' } as never)).rejects.toMatchObject({ code: 'invalid_instance' });
  });

  test("updateOne, deleteOne and an operation present the caller's preconditions to the core, which checks them before it reads the row and writes nothing when it refuses them", async () => {
    const row = await orm.order.createOne({ title: 'Fence' });
    // Workflow takes no precondition (D104): the core refuses an entry for
    // its use, as it would a stale fencing token's, and nothing is written.
    const refused = { status: 400, code: 'invalid_argument', class: 'PreconditionsError', message: 'preconditions of Order: /status: behavior Workflow takes no precondition' };
    await expect(orm.order.updateOne(row.id, { title: 'Gate' }, { preconditions: { status: { token: 1 } } })).rejects.toMatchObject(refused);
    await expect(orm.order.status.transition(row.id, { to: 'paid' }, { preconditions: { status: { token: 1 } } })).rejects.toMatchObject(refused);
    await expect(orm.order.deleteOne(row.id, { preconditions: { status: { token: 1 } } })).rejects.toMatchObject(refused);
    expect(await orm.order.getOne(row.id)).toMatchObject({ title: 'Fence', status: 'draft', _version: 1 });
    // Checked before the row is read: a key no row holds is refused alike,
    // not NotFoundError.
    await expect(orm.order.updateOne('6fe1XoZ0UJpMNNdUOSMC2X', { title: 'x' }, { preconditions: { status: {} } })).rejects.toMatchObject({ code: 'invalid_argument', class: 'PreconditionsError' });
    // None, or an empty set, presents none.
    expect(await orm.order.updateOne(row.id, { title: 'Gate' }, { preconditions: {} })).toMatchObject({ title: 'Gate', _version: 2 });
  });

  test('a precondition given to a write of a table that composes no behaviors throws before anything is written', async () => {
    const row = await orm.order.createOne({ title: 'Plain' });
    // The same tables, with Order left out of the layout: a plain table.
    const plain = ormOf<Repositories>(pool, { ...definition, rules: { types: { Ticket: rules.types.Ticket! } } }, withRulesCore(null, () => rulesCore!));
    const refusal = "orm: preconditions are a behavior table's (D104): Order composes none";
    await expect(plain.order.updateOne(row.id, { title: 'Fenced' }, { preconditions: { Lease: { token: 1 } } })).rejects.toThrow(refusal);
    await expect(plain.order.deleteOne(row.id, { preconditions: { Lease: { token: 1 } } })).rejects.toThrow(refusal);
    expect(await orm.order.getOne(row.id)).toMatchObject({ title: 'Plain', _version: 1 });
    expect(await plain.order.updateOne(row.id, { title: 'Open' }, { preconditions: {} })).toMatchObject({ title: 'Open' });
  });

  test("an upsert inserts a new row with what the core's create gives it, and updates a held row's own fields through the core, its state kept", async () => {
    const before = Date.now();
    const inserted = await orm.order.upsertOneResult({ title: 'Crate' }, ['id'], { title: 'Crate' });
    expect(inserted.status).toBe('inserted');
    expect(inserted.row).toMatchObject({ title: 'Crate', status: 'draft', _version: 1 });
    expect(inserted.row.statusEnteredAt!.getTime()).toBeGreaterThanOrEqual(before - 1);
    expect(await orm.order.status.transition(inserted.row.id, { to: 'paid' })).toEqual({ from: 'draft', to: 'paid' });
    const entered = (await orm.order.getOne(inserted.row.id)).statusEnteredAt!.getTime();

    // The conflict branch writes nothing, so the core's update moves the
    // version once.
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
    // A use with no operations, as Reactions has none (D103), adds no
    // member, so none can clash.
    const quiet: RulesLayout = { types: { Order: { ...rules.types.Order!, uses: [...rules.types.Order!.uses, { id: 'Reactions', behavior: 'Reactions', member: 'getOne', fields: [], operations: [] }] } } };
    const repositories = ormOf<Repositories>(pool, { ...definition, rules: quiet });
    expect(Object.keys(repositories.order)).not.toContain('reactions');
    expect(typeof repositories.order.getOne).toBe('function');
  });
});

const itemColumns = '"id", "code", "title", "owner", "urgent", "plan", "reviewer", "status", "status_entered_at", "updated_at", "deleted_at", "_version"';

/**
 * A soft-deletable table that implements Constants and StateRules beside
 * its Workflow on status (D104), as the fixture-approvals-db golden lays
 * one out, with no sub-table, so it has upserts: by its unique key, code,
 * and by a unique index over lower(code), an expression.
 */
const itemTable: TableDefinition = {
  ...orderTable,
  name: 'Item',
  table: 'item',
  softDelete: true,
  columns: [
    { name: 'id', column: 'id', field: 'id', qualified: 'item.id', uuid: true, key: true, auto: true, required: true },
    { name: 'code', column: 'code', field: 'code', qualified: 'item.code', required: true },
    { name: 'title', column: 'title', field: 'title', qualified: 'item.title', required: true },
    { name: 'owner', column: 'owner', field: 'owner', qualified: 'item.owner' },
    { name: 'urgent', column: 'urgent', field: 'urgent', qualified: 'item.urgent', boolean: true },
    { name: 'plan', column: 'plan', field: 'plan', qualified: 'item.plan' },
    { name: 'reviewer', column: 'reviewer', field: 'reviewer', qualified: 'item.reviewer' },
    { name: 'status', column: 'status', field: 'status', qualified: 'item.status', required: true },
    { name: 'statusEnteredAt', column: 'status_entered_at', field: 'statusEnteredAt', qualified: 'item.status_entered_at', timestamp: true },
    { name: 'updatedAt', column: 'updated_at', field: 'updatedAt', qualified: 'item.updated_at', timestamp: true, audit: 'updatedAt' },
    { name: 'deletedAt', column: 'deleted_at', field: 'deletedAt', qualified: 'item.deleted_at', timestamp: true, audit: 'deletedAt' },
    { name: '_version', column: '_version', field: '_version', qualified: 'item._version', int64: true, auto: true, required: true, internal: true },
  ],
  statements: {
    ...orderTable.statements,
    columns: itemColumns,
    getOne: `SELECT ${itemColumns} FROM item WHERE id = $1`,
    live: ' AND deleted_at IS NULL',
    liveCondition: 'item.deleted_at IS NULL',
    findOne: 'SELECT %s FROM item%s%s LIMIT 1',
    insert: 'INSERT INTO item (%s) VALUES (%s) RETURNING %s',
    update: 'UPDATE item SET %s WHERE id = $%d',
    returning: ` RETURNING ${itemColumns}`,
    upsert: `INSERT INTO item (%s) VALUES %s ON CONFLICT %s DO UPDATE SET %s%s RETURNING ${itemColumns}, (xmax = 0) AS _inserted`,
    softDelete: 'UPDATE item SET deleted_at = $1 WHERE id = $2 AND deleted_at IS NULL',
    delete: 'DELETE FROM item WHERE id = $1',
  },
  uniqueKeys: {
    id: { conflict: '(id)', keep: 'id = EXCLUDED.id', rows: `SELECT ${itemColumns} FROM item WHERE (id) IN (%s)` },
    code: { conflict: '(code)', keep: 'code = EXCLUDED.code', rows: `SELECT ${itemColumns} FROM item WHERE (code) IN (%s)` },
    lowerCode: { conflict: '((lower(code)))', keep: 'code = EXCLUDED.code', expression: true },
  },
};

/** Item's owner stays who its create named and urgent only turns true, unless the caller holds items.admin; while it is a draft only its reviewer edits its plan. */
const itemRules: RulesLayout = {
  types: {
    Item: {
      type: 'Item',
      table: 'item',
      key: { field: 'id', column: 'id', type: 'Identity.UUID' },
      version: '_version',
      live: { column: 'deleted_at' },
      own: [
        { field: 'code', column: 'code', type: 'string' },
        { field: 'title', column: 'title', type: 'string' },
        { field: 'owner', column: 'owner', type: 'string' },
        { field: 'urgent', column: 'urgent', type: 'boolean' },
        { field: 'plan', column: 'plan', type: 'string' },
        { field: 'reviewer', column: 'reviewer', type: 'string' },
      ],
      uses: [
        { id: 'Constants', behavior: 'Constants', member: 'constants', fields: [], operations: [] },
        { id: 'StateRules', behavior: 'StateRules', member: 'staterules', fields: [], operations: [] },
        { ...rules.types.Order!.uses[0]!, member: 'status' },
      ],
      spec: {
        name: 'Item',
        uses: [
          { behavior: 'Constants', config: { fields: ['owner'], oneWay: { urgent: 'up' }, permission: 'items.admin' }, anchor: 'type' },
          { behavior: 'StateRules', config: { states: { draft: { frozen: ['plan'], by: ['reviewer'] } } }, anchor: 'type' },
          { behavior: 'Workflow', config: { initial: 'draft', states: ['draft', 'done'], transitions: [{ from: 'draft', to: 'done' }] }, anchor: { field: 'status' } },
        ],
        fields: [
          { name: 'code', type: 'string' },
          { name: 'title', type: 'string' },
          { name: 'owner', type: 'string' },
          { name: 'urgent', type: 'boolean' },
          { name: 'plan', type: 'string' },
          { name: 'reviewer', type: 'string' },
        ],
      },
    },
  },
};

const itemDefinition: OrmDefinition = { clauses, defaultLimit: 100, historyActorCTE: '', clearHistoryActor: '', tables: { item: itemTable }, rules: itemRules };

interface ItemRow {
  id: string;
  code: string;
  title: string;
  owner: string | null;
  urgent: boolean | null;
  plan: string | null;
  reviewer: string | null;
  status: string;
  updatedAt: Date;
  _version: number;
}

interface ItemInput {
  code: string;
  title: string;
  owner?: string;
  urgent?: boolean;
  plan?: string;
  reviewer?: string;
}

type ItemUpdate = Partial<Omit<ItemInput, 'code'>>;

type ItemKey = readonly ['code'] | readonly ['lowerCode'];

interface ItemRepositories {
  readonly item: {
    deleteOne(id: string): Promise<void>;
    upsertOne(input: ItemInput, on: ItemKey, update: ItemUpdate, options?: { onlyIfChanged?: boolean }): Promise<ItemRow>;
    upsertOneResult(input: ItemInput, on: ItemKey, update: ItemUpdate, options?: { onlyIfChanged?: boolean }): Promise<Upserted<ItemRow>>;
    upsertMany(inputs: readonly ItemInput[], on: ItemKey, update: readonly (keyof ItemUpdate)[], options?: { onlyIfChanged?: boolean }): Promise<Upserted<ItemRow>[]>;
  };
}

describe.skipIf(rulesCore === undefined || rulesDsn === '')('an upsert of a table that implements Constants and StateRules, on Postgres', () => {
  const schema = `rules_orm_upsert_${process.pid}_${Date.now()}`;
  let admin: pg.Pool;
  let pool: pg.Pool;
  let orm: Orm<ItemRepositories>;

  beforeAll(async () => {
    admin = new pg.Pool({ connectionString: rulesDsn, max: 1 });
    await admin.query(`CREATE SCHEMA ${schema}`);
    pool = new pg.Pool({ connectionString: rulesDsn, max: 4, options: `-c search_path=${schema}` });
    await pool.query(`CREATE TABLE item (
  id UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
  code TEXT NOT NULL UNIQUE,
  title TEXT NOT NULL,
  owner TEXT,
  urgent BOOLEAN,
  plan TEXT,
  reviewer TEXT,
  status TEXT,
  status_entered_at TIMESTAMPTZ,
  updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
  deleted_at TIMESTAMPTZ,
  _version BIGINT DEFAULT 1 NOT NULL
)`);
    await pool.query('CREATE UNIQUE INDEX item_lower_code ON item ((lower(code)))');
    for (const ddl of bumpVersion('item')) await pool.query(ddl);
    orm = ormOf<ItemRepositories>(pool, itemDefinition, withRulesCore(null, () => rulesCore!));
  });
  afterAll(async () => {
    await pool?.end();
    await admin?.query(`DROP SCHEMA ${schema} CASCADE`);
    await admin?.end();
  });

  /** The row that holds code as the table holds it, read past the ORM; undefined when none does. */
  const stored = async (code: string) => (await pool.query('SELECT title, owner, urgent, plan, reviewer, status, updated_at, deleted_at, _version FROM item WHERE code = $1', [code])).rows[0];

  /** An input whose key, k1, the table holds after the first test. */
  const held: ItemInput = { code: 'k1', title: 'Kettle' };

  test("an upsert of a new key inserts what the core's create gives the row, and one that sets no field leaves the held row as it is", async () => {
    const inserted = await orm.item.upsertOneResult({ code: 'k1', title: 'Kettle', owner: 'ann', urgent: false, plan: 'boil', reviewer: 'rita' }, ['code'], { title: 'Kettle' });
    expect(inserted.status).toBe('inserted');
    expect(inserted.row).toMatchObject({ code: 'k1', title: 'Kettle', owner: 'ann', urgent: false, plan: 'boil', reviewer: 'rita', status: 'draft', _version: 1 });
    const same = await orm.item.upsertOneResult(held, ['code'], {});
    expect(same.status).toBe('unchanged');
    expect(same.row).toMatchObject({ id: inserted.row.id, title: 'Kettle', owner: 'ann', plan: 'boil' });
  });

  test('an upsert of a held key that changes a constant is refused and leaves the row as it was; a caller with the permission changes it, moving its version once', async () => {
    const before = await stored('k1');
    await expect(orm.item.upsertOne(held, ['code'], { owner: 'bob' })).rejects.toMatchObject({
      code: 'invalid_instance',
      status: 422,
      refusal: { details: { issues: [{ path: 'owner', rule: 'constant' }] } },
    });
    await expect(orm.item.upsertOne(held, ['code'], { title: 'Pot', owner: 'bob' })).rejects.toBeInstanceOf(RulesRefusal);
    expect(await stored('k1')).toEqual(before);
    const keeper = orm.withBehaviorCaller({ subject: 'root', permissions: ['items.admin'] });
    const changed = await keeper.item.upsertOneResult(held, ['code'], { owner: 'bob' });
    expect(changed).toMatchObject({ status: 'updated', row: { owner: 'bob', title: 'Kettle', _version: Number(before._version) + 1 } });
    expect(await stored('k1')).toMatchObject({ owner: 'bob', _version: String(Number(before._version) + 1) });
  });

  test('an upsert that moves a one-way field forward is updated and saved, and one that moves it back is refused and leaves the row as it was', async () => {
    const earlier = await stored('k1');
    const forward = await orm.item.upsertOneResult(held, ['code'], { urgent: true });
    expect(forward).toMatchObject({ status: 'updated', row: { urgent: true, _version: Number(earlier._version) + 1 } });
    const before = await stored('k1');
    expect(before.urgent).toBe(true);
    await expect(orm.item.upsertOne(held, ['code'], { urgent: false })).rejects.toMatchObject({
      code: 'vetoed',
      refusal: { details: { behavior: 'Constants', vetoCode: 'one_way', vetoDetails: { field: 'urgent', direction: 'up' } } },
    });
    expect(await stored('k1')).toEqual(before);
  });

  test('an upsert that changes a frozen field is refused for a caller its by does not name, and saved, stamped, for the subject it names', async () => {
    const before = await stored('k1');
    const frozen = { code: 'vetoed', refusal: { details: { behavior: 'StateRules', vetoCode: 'frozen_fields', vetoDetails: { state: 'draft', fields: ['plan'], by: ['reviewer'] } } } };
    await expect(orm.item.upsertOne(held, ['code'], { plan: 'steep' })).rejects.toMatchObject(frozen);
    await expect(orm.withBehaviorCaller({ subject: 'sam', permissions: [] }).item.upsertOne(held, ['code'], { plan: 'steep' })).rejects.toMatchObject(frozen);
    expect(await stored('k1')).toEqual(before);
    const saved = await orm.withBehaviorCaller({ subject: 'rita', permissions: [] }).item.upsertOneResult(held, ['code'], { plan: 'steep' });
    expect(saved).toMatchObject({ status: 'updated', row: { plan: 'steep', status: 'draft', _version: Number(before._version) + 1 } });
    expect(saved.row.updatedAt.getTime()).toBeGreaterThan(before.updated_at.getTime());
  });

  test('an upsert only if changed whose fields hold its values already is unchanged at its version; one that differs runs through the core', async () => {
    const before = await stored('k1');
    const same = await orm.item.upsertOneResult(held, ['code'], { title: 'Kettle', urgent: true }, { onlyIfChanged: true });
    expect(same.status).toBe('unchanged');
    expect(same.row).toMatchObject({ title: 'Kettle', urgent: true, _version: Number(before._version) });
    expect(await stored('k1')).toEqual(before);
    await expect(orm.item.upsertOne(held, ['code'], { title: 'Pot', urgent: false }, { onlyIfChanged: true })).rejects.toMatchObject({ code: 'vetoed', refusal: { details: { vetoCode: 'one_way' } } });
    expect(await stored('k1')).toEqual(before);
    const differs = await orm.item.upsertOneResult(held, ['code'], { title: 'Teapot', urgent: true }, { onlyIfChanged: true });
    expect(differs).toMatchObject({ status: 'updated', row: { title: 'Teapot', urgent: true, _version: Number(before._version) + 1 } });
  });

  test("upsertMany updates each held row through the core with its input's fields, in the inputs' order, as the repository's caller", async () => {
    const before = await stored('k1');
    const results = await orm.withBehaviorCaller({ subject: 'rita', permissions: [] }).item.upsertMany(
      [
        { code: 'k2', title: 'Cup', owner: 'cy', plan: 'fill', reviewer: 'rita' },
        { code: 'k1', title: 'Kettle', plan: 'pour' },
      ],
      ['code'],
      ['title', 'plan']
    );
    expect(results.map(result => [result.status, result.row.code, result.row.title, result.row.owner, result.row.plan, result.row.status, result.row._version])).toEqual([
      ['inserted', 'k2', 'Cup', 'cy', 'fill', 'draft', 1],
      ['updated', 'k1', 'Kettle', 'bob', 'pour', 'draft', Number(before._version) + 1],
    ]);
    const after = await stored('k1');
    const unchanged = await orm.item.upsertMany([{ code: 'k1', title: 'Kettle', plan: 'pour' }], ['code'], ['title', 'plan'], { onlyIfChanged: true });
    expect(unchanged.map(result => [result.status, result.row._version])).toEqual([['unchanged', Number(after._version)]]);
    expect(await stored('k1')).toEqual(after);
  });

  test("an upsertMany whose second input's held row would change a constant is refused and writes none of the batch", async () => {
    const before = await stored('k1');
    await expect(
      orm.item.upsertMany(
        [
          { code: 'k3', title: 'Jug', owner: 'jo' },
          { code: 'k1', title: 'Kettle', owner: 'zed' },
        ],
        ['code'],
        ['owner']
      )
    ).rejects.toMatchObject({ code: 'invalid_instance', refusal: { details: { issues: [{ path: 'owner', rule: 'constant' }] } } });
    expect(await stored('k3')).toBeUndefined();
    expect(await stored('k1')).toEqual(before);
  });

  test('an upsert of a key a soft-deleted row holds throws NotFoundError and writes nothing', async () => {
    const acting = orm.withUser('6fe1XoZ0UJpMNNdUOSMC2X');
    const row = await acting.item.upsertOne({ code: 'k4', title: 'Lid' }, ['code'], {});
    await acting.item.deleteOne(row.id);
    const gone = await stored('k4');
    expect(gone.deleted_at).toBeInstanceOf(Date);
    await expect(acting.item.upsertOne({ code: 'k4', title: 'Lid' }, ['code'], { title: 'Cap' })).rejects.toBeInstanceOf(NotFoundError);
    await expect(acting.item.upsertOne({ code: 'k4', title: 'Lid' }, ['code'], { title: 'Cap' }, { onlyIfChanged: true })).rejects.toBeInstanceOf(NotFoundError);
    await expect(acting.item.upsertMany([{ code: 'k5', title: 'Tin' }, { code: 'k4', title: 'Cap' }], ['code'], ['title'])).rejects.toBeInstanceOf(NotFoundError);
    await expect(acting.item.upsertOne({ code: 'k4', title: 'Lid' }, ['code'], {})).rejects.toBeInstanceOf(NotFoundError);
    expect(await stored('k4')).toEqual(gone);
    expect(await stored('k5')).toBeUndefined();
  });

  test('an upsert on a key over an expression whose update sets a field is refused before anything is written; one that sets none keeps the row as it is', async () => {
    const before = await stored('k1');
    await expect(orm.item.upsertOne({ code: 'K1', title: 'Kettle' }, ['lowerCode'], { title: 'Urn' })).rejects.toThrow(
      'orm: upsertOne of Item on lowerCode, a unique index over an expression: an upsert of a table that composes behaviors updates the row the key holds through the core (D103), and needs a key over columns to find it; upsert on another key'
    );
    await expect(orm.item.upsertMany([{ code: 'K1', title: 'Urn' }], ['lowerCode'], ['title'])).rejects.toThrow('a unique index over an expression');
    expect(await stored('k1')).toEqual(before);
    expect(await stored('K1')).toBeUndefined();
    // The key's keep, as the generator writes it, sets its first column to
    // the input's value, so the read of k1 is the last of these tests.
    const [{ id }] = (await pool.query("SELECT id FROM item WHERE code = 'k1'")).rows;
    const kept = await orm.item.upsertOneResult({ code: 'K1', title: 'Kettle' }, ['lowerCode'], {});
    expect(kept.status).toBe('unchanged');
    expect(uuidToDb(kept.row.id)).toBe(id);
  });
});
