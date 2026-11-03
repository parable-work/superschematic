import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { MAX_ATTEMPTS, RulesRefusal, execute, readRow, runCreate, runDelete, runOperation, type CoreAnswers, type CoreCaller, type CoreCommand, type CoreStep, type CoreTypeSpec, type RulesCore, type RulesHost, type RulesLayout, type RulesStore } from './rules';
import { bumpVersion, columnTypes, core, postgresDatabase, rulesCore, rulesDsn, sqliteDatabase, type TestDatabase } from './rules.testing';

// The adapter over a real database of each dialect, bun:sqlite through
// openSqlite (D1's interface, whose batch is the unit) and Postgres (a
// transaction, whose savepoint is the unit), on a table written by hand as
// sqlgen writes one that composes Workflow on status: the owned columns
// status and status_entered_at beside the own title, and @optimistic's
// _version, with its trigger on Postgres. The Postgres cases are skipped
// without SUPERSCHEMATIC_RULES_TEST_DATABASE_URL.

const flow = {
  states: ['draft', 'paid', 'shipped'],
  initial: 'draft',
  transitions: [
    { from: 'draft', to: 'paid' },
    { from: 'paid', to: 'shipped', permission: 'orders.ship' },
  ],
};

const layout: RulesLayout = {
  types: {
    Order: {
      type: 'Order',
      table: 'order',
      key: { field: 'id', column: 'id', type: 'string' },
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

/** A soft-deletable table that composes Workflow on status: its live rows hold NULL in deleted_at. */
const ticketLayout: RulesLayout = {
  types: { Ticket: { ...layout.types.Order!, type: 'Ticket', table: 'ticket', live: { column: 'deleted_at' }, spec: { ...layout.types.Order!.spec, name: 'Ticket' } } },
};

/** A table a rule across rows writes, which composes no behavior. */
const bookingLayout: RulesLayout = {
  types: { Booking: { type: 'Booking', table: 'booking', key: { field: 'id', column: 'id', type: 'string' }, version: '_version', own: [], uses: [], spec: { name: 'Booking', uses: [] } } },
};

/**
 * A core whose create of a Booking is a rule across rows, as two bookings
 * of one room are: it takes the lock Room/r1, reads under it whether
 * Booking has rows, and creates its row only when there is none, refusing
 * conflict otherwise. Only the row it creates is written, so only the lock
 * holds two such commands apart.
 */
const oneBooking: RulesCore = {
  run(_spec: CoreTypeSpec, command: CoreCommand, answers: CoreAnswers = {}): CoreStep {
    if ((answers.locks ?? []).length === 0) return { lock: ['Room/r1'] };
    const read = (answers.reads ?? []).find(answered => answered.request.kind === 'hasRows' && (answered.held ?? 0) > 0);
    if (read === undefined) return { read: [{ kind: 'hasRows', type: 'Booking' }] };
    if ('hasRows' in read.answer && read.answer.hasRows) return { done: { refused: { code: 'conflict', status: 409, class: 'EngineError', message: 'room r1 is booked' } } };
    const id = command.action.kind === 'create' ? (command.action.id ?? null) : null;
    return { done: { changed: { rows: [{ kind: 'create', type: 'Booking', key: id, own: {}, owned: {} }], subRows: [], events: [], deadlines: [], result: null } } };
  },
};

const alice: CoreCaller = { subject: 'alice', permissions: [] };
const shipper: CoreCaller = { subject: 'sam', permissions: ['orders.ship'] };

const dialects: { name: string; skip: boolean; open: () => Promise<TestDatabase> }[] = [
  { name: 'bun:sqlite', skip: rulesCore === undefined, open: async () => sqliteDatabase() },
  { name: 'Postgres', skip: rulesCore === undefined || rulesDsn === '', open: postgresDatabase },
];

for (const dialect of dialects) {
  describe.skipIf(dialect.skip)(`the adapter on ${dialect.name}`, () => {
    let db: TestDatabase;
    let clock = 1_000_000;
    const p = (n: number) => (db.dialect === 'postgres' ? `$${n}` : `?${n}`);

    beforeAll(async () => {
      db = await dialect.open();
      const t = columnTypes(db.dialect);
      await db.exec(`CREATE TABLE "order" (
  id ${t.key},
  title ${t.text} NOT NULL,
  status ${t.text},
  status_entered_at ${t.timestamp},
  _version ${t.version}
)`);
      if (db.dialect === 'postgres') for (const ddl of bumpVersion('order')) await db.exec(ddl);
      await db.exec(`CREATE TABLE ticket (
  id ${t.key},
  title ${t.text} NOT NULL,
  status ${t.text},
  status_entered_at ${t.timestamp},
  deleted_at ${t.timestamp},
  _version ${t.version}
)`);
      if (db.dialect === 'postgres') for (const ddl of bumpVersion('ticket')) await db.exec(ddl);
      await db.exec(`CREATE TABLE booking (id ${t.key}, _version ${t.version})`);
    });
    afterAll(async () => db?.close());

    /** Runs fn on a host over a transaction of the database, at the next tick of the test's clock. */
    const inTransaction = <T>(fn: (host: RulesHost) => Promise<T>, wrap: (store: RulesStore) => RulesStore = s => s): Promise<T> => {
      const now = (clock += 1000);
      return db.transaction(store => fn({ core: core(), layout, store: wrap(store), now: () => now }));
    };

    /** The row as the database holds it. */
    const stored = async (id: string) => {
      const rows = await db.rows(`SELECT title, status, status_entered_at, _version FROM "order" WHERE id = ${p(1)}`, [id]);
      const row = rows[0];
      if (!row) return undefined;
      const at = row.status_entered_at;
      return {
        title: row.title,
        status: row.status,
        enteredAt: at === null ? null : at instanceof Date ? at.getTime() : Date.parse(String(at)),
        version: Number(row._version),
      };
    };

    /** A store whose first moves apply calls each see the row moved by another connection first, as a write committed between the read and the apply. */
    const moving = (id: string, moves: number) => (store: RulesStore): RulesStore => {
      let calls = 0;
      return {
        ...store,
        async apply(statements) {
          if (calls++ < moves) await db.concurrently(`UPDATE "order" SET title = title || '!', _version = _version + 1 WHERE id = ${p(1)}`, [id]);
          return store.apply(statements);
        },
      };
    };

    test('a create initializes the owned columns from the config, at the command time, at version 1', async () => {
      const key = await inTransaction(host => runCreate(host, 'Order', { title: 'Desk' }, alice, { id: 'o1' }));
      const created = clock;
      expect(key).toBe('o1');
      expect(await stored('o1')).toEqual({ title: 'Desk', status: 'draft', enteredAt: created, version: 1 });
      expect(await inTransaction(host => readRow(host, 'Order', 'o1'))).toEqual({
        own: { title: 'Desk' },
        owned: { status: { status: 'draft', enteredAt: created } },
        version: 1,
      });
    });

    test('a transition writes the status and when it was entered, and moves the version on', async () => {
      const result = await inTransaction(host => runOperation(host, 'Order', 'o1', 'status', 'transition', { to: 'paid' }, alice));
      expect(result).toEqual({ from: 'draft', to: 'paid' });
      expect(await stored('o1')).toEqual({ title: 'Desk', status: 'paid', enteredAt: clock, version: 2 });
    });

    test('a veto and a permission the caller lacks refuse, and write nothing', async () => {
      const before = await stored('o1');
      await expect(inTransaction(host => runOperation(host, 'Order', 'o1', 'status', 'transition', { to: 'draft' }, alice))).rejects.toMatchObject({
        status: 409,
        code: 'vetoed',
        class: 'BehaviorVetoError',
      });
      const forbidden = inTransaction(host => runOperation(host, 'Order', 'o1', 'status', 'transition', { to: 'shipped' }, alice));
      await expect(forbidden).rejects.toBeInstanceOf(RulesRefusal);
      await expect(forbidden).rejects.toMatchObject({ status: 403, code: 'forbidden' });
      expect(await stored('o1')).toEqual(before);
    });

    test('a write another connection commits between the read and the apply moves the row, and the command runs again from fresh state', async () => {
      await inTransaction(host => runCreate(host, 'Order', { title: 'Chair' }, alice, { id: 'o2' }));
      const result = await inTransaction(host => execute(host, 'Order', { caller: alice, now: clock, action: { kind: 'operation', key: 'o2', useId: 'status', operation: 'transition', params: { to: 'paid' } } }), moving('o2', 1));
      expect(result.attempts).toBe(2);
      expect(result.result).toEqual({ from: 'draft', to: 'paid' });
      // The other connection's write and the command's: both kept.
      expect(await stored('o2')).toEqual({ title: 'Chair!', status: 'paid', enteredAt: clock, version: 3 });
    });

    test('a row that keeps moving is refused as busy after the last attempt, and the command writes nothing', async () => {
      await inTransaction(host => runCreate(host, 'Order', { title: 'Lamp' }, alice, { id: 'o3' }));
      let thrown: unknown;
      try {
        await inTransaction(host => runOperation(host, 'Order', 'o3', 'status', 'transition', { to: 'paid' }, alice), moving('o3', MAX_ATTEMPTS));
      } catch (error) {
        thrown = error;
      }
      expect(thrown).toBeInstanceOf(RulesRefusal);
      expect((thrown as RulesRefusal).refusal).toMatchObject({ code: 'busy', status: 503, class: 'EngineError' });
      expect((thrown as RulesRefusal).message).toContain('it ran 8 times and nothing was written');
      expect(await stored('o3')).toEqual({ title: `Lamp${'!'.repeat(MAX_ATTEMPTS)}`, status: 'draft', enteredAt: expect.any(Number), version: 1 + MAX_ATTEMPTS });
    });

    test('a raw SQL write of an owned column is accepted, and the core reads what it wrote: the escape hatch', async () => {
      expect(await db.exec(`UPDATE "order" SET status = 'shipped', status_entered_at = NULL WHERE id = ${p(1)}`, ['o1'])).toBe(1);
      expect(await inTransaction(host => readRow(host, 'Order', 'o1'))).toMatchObject({ owned: { status: { status: 'shipped' } } });
      // shipped is terminal: no transition leaves it.
      await expect(inTransaction(host => runOperation(host, 'Order', 'o1', 'status', 'transition', { to: 'paid' }, shipper))).rejects.toMatchObject({
        code: 'vetoed',
        message: 'behavior Workflow vetoes transition of Order o1: shipped is a terminal state: no transition leaves it',
      });
    });

    test('a create of a key the table holds is conflict, a delete takes the row, and a command on a row that is gone is not_found', async () => {
      await expect(inTransaction(host => runCreate(host, 'Order', { title: 'Again' }, alice, { id: 'o2' }))).rejects.toMatchObject({ code: 'conflict', status: 409 });
      await inTransaction(host => runDelete(host, 'Order', 'o2', alice));
      expect(await stored('o2')).toBeUndefined();
      await expect(inTransaction(host => runDelete(host, 'Order', 'o2', alice))).rejects.toMatchObject({ code: 'not_found', status: 404, message: 'Order o2 does not exist' });
    });

    test('a command that fails leaves the caller transaction going on', async () => {
      const after = await inTransaction(async host => {
        await expect(runOperation(host, 'Order', 'o3', 'status', 'transition', { to: 'shipped' }, alice)).rejects.toMatchObject({ code: 'vetoed' });
        return runOperation(host, 'Order', 'o3', 'status', 'transition', { to: 'paid' }, alice);
      });
      expect(after).toEqual({ from: 'draft', to: 'paid' });
    });

    test("a row its table holds deleted is not_found to the core, and a soft delete another commits under a command moves the row to not_found", async () => {
      const onTickets = (store: RulesStore): RulesHost => ({ core: core(), layout: ticketLayout, store, now: () => clock });
      const softDelete = (id: string) => db.concurrently(`UPDATE ticket SET deleted_at = ${p(1)}, _version = _version + 1 WHERE id = ${p(2)}`, [db.dialect === 'postgres' ? new Date() : new Date().toISOString(), id]);
      const ticket = async (id: string) => (await db.rows(`SELECT status, _version FROM ticket WHERE id = ${p(1)}`, [id]))[0];
      await db.transaction(store => runCreate(onTickets(store), 'Ticket', { title: 'Leak' }, alice, { id: 't1' }));
      await softDelete('t1');
      expect(await db.transaction(store => readRow(onTickets(store), 'Ticket', 't1'))).toBeNull();
      await expect(db.transaction(store => runOperation(onTickets(store), 'Ticket', 't1', 'status', 'transition', { to: 'paid' }, alice))).rejects.toMatchObject({ code: 'not_found' });
      expect(await ticket('t1')).toMatchObject({ status: 'draft' });

      await db.transaction(store => runCreate(onTickets(store), 'Ticket', { title: 'Drip' }, alice, { id: 't2' }));
      let applies = 0;
      const deletedUnder = (store: RulesStore): RulesStore => ({
        ...store,
        async apply(statements) {
          if (applies++ === 0) await softDelete('t2');
          return store.apply(statements);
        },
      });
      await expect(db.transaction(store => runOperation(onTickets(deletedUnder(store)), 'Ticket', 't2', 'status', 'transition', { to: 'paid' }, alice))).rejects.toMatchObject({ code: 'not_found' });
      expect(applies).toBe(1);
      expect(await ticket('t2')).toMatchObject({ status: 'draft' });
      expect(Number((await ticket('t2'))!._version)).toBe(2);
    });

    test('two commands that take one lock, each creating its row only when the other has none, write one row: the second runs over the first', async () => {
      const create = (id: string): CoreCommand => ({ caller: alice, now: clock, request: id, action: { kind: 'create', id, own: {} } });
      // The first command pauses after its read under the lock, until the
      // second has run as far as it can.
      let release!: () => void;
      const gate = new Promise<void>(resolve => (release = resolve));
      let hasRead!: () => void;
      const read = new Promise<void>(resolve => (hasRead = resolve));
      let paused = false;
      const pausing = (store: RulesStore): RulesStore => ({
        ...store,
        async query(text, values) {
          const rows = await store.query(text, values);
          if (!paused && text.includes('LIMIT 1')) {
            paused = true;
            hasRead();
            await gate;
          }
          return rows;
        },
      });
      const outcome = (run: Promise<{ attempts: number }>) => run.then(result => `applied after ${result.attempts}`, (error: unknown) => (error instanceof RulesRefusal ? error.refusal.code : String(error)));
      const first = outcome(db.transaction(store => execute({ core: oneBooking, layout: bookingLayout, store: pausing(store) }, 'Booking', create('b1'))));
      await read;
      const second = outcome(db.transaction(store => execute({ core: oneBooking, layout: bookingLayout, store }, 'Booking', create('b2'))));
      // On Postgres the second waits on the advisory lock; on SQLite it
      // runs to its end, since nothing holds it.
      await Promise.race([second, new Promise(resolve => setTimeout(resolve, 200))]);
      release();
      const outcomes = [await first, await second];
      const rows = await db.rows('SELECT id FROM booking ORDER BY id');
      if (db.dialect === 'postgres') {
        expect(outcomes).toEqual(['applied after 1', 'conflict']);
        expect(rows).toEqual([{ id: 'b1' }]);
      } else {
        // The first's batch finds what it read under the lock changed, so it
        // runs again and reads the second's row.
        expect(outcomes).toEqual(['conflict', 'applied after 1']);
        expect(rows).toEqual([{ id: 'b2' }]);
      }
    });

    if (dialect.name === 'Postgres') {
      test("a lock is the transaction's advisory lock on the key, which another connection cannot take until it ends", async () => {
        await db.transaction(async store => {
          await store.lock('Stock/sku-1');
          const [other] = await db.rows('SELECT pg_try_advisory_xact_lock(hashtextextended($1, 0)) AS taken', ['Stock/sku-1']);
          expect(other!.taken).toBe(false);
        });
        const [after] = await db.rows('SELECT pg_try_advisory_xact_lock(hashtextextended($1, 0)) AS taken', ['Stock/sku-1']);
        expect(after!.taken).toBe(true);
      });
    }
  });
}
