import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { MAX_ATTEMPTS, RulesRefusal, execute, readRow, readSubRows, runCreate, runDelete, runOperation, runUpdate, type CoreAnswers, type CoreCaller, type CoreCommand, type CoreStep, type CoreTypeSpec, type RulesCore, type RulesHost, type RulesLayout, type RulesStore } from './rules';
import { bumpVersion, columnTypes, core, postgresDatabase, rulesCore, rulesDsn, sqliteDatabase, type TestDatabase } from './rules.testing';

// The adapter over a real database of each dialect, bun:sqlite through
// openSqlite (D1's interface, whose batch is the unit) and Postgres (a
// transaction, whose savepoint is the unit), on a table written by hand as
// sqlgen writes one that composes Workflow on status: the owned columns
// status and status_entered_at beside the own title, and @optimistic's
// _version, with its trigger on Postgres; and tables whose own columns hold
// subjects, a boolean, a number and JSON, one of which implements
// Approvals and keeps its sub-table with no queue (D104). The Postgres
// cases are skipped without SUPERSCHEMATIC_RULES_TEST_DATABASE_URL.

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

/**
 * A table whose own fields hold subjects (D104): asked_of, an own UUID
 * column, and created_by, the audit column the repositories stamp (D87),
 * which the layout lists apart from the own columns, a row read holds among
 * the own fields and no change set writes; withdrawing a question is made
 * by the subject its createdBy holds.
 */
const questionLayout: RulesLayout = {
  types: {
    Question: {
      type: 'Question',
      table: 'question',
      key: { field: 'id', column: 'id', type: 'string' },
      version: '_version',
      own: [
        { field: 'prompt', column: 'prompt', type: 'string' },
        { field: 'askedOf', column: 'asked_of', type: 'Identity.UUID' },
      ],
      audit: [{ field: 'createdBy', column: 'created_by', type: 'Identity.UUID' }],
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
      spec: {
        name: 'Question',
        uses: [{ behavior: 'Workflow', config: { states: ['open', 'withdrawn'], transitions: [{ from: 'open', to: 'withdrawn', by: ['createdBy'] }] }, anchor: { field: 'status' } }],
        fields: [
          { name: 'prompt', type: 'string' },
          { name: 'askedOf', type: 'Identity.UUID' },
          { name: 'createdBy', type: 'Identity.UUID' },
        ],
      },
    },
  },
};

/**
 * A table whose own columns hold a boolean, a number and JSON beside a
 * string (D104), and that implements Approvals, whose sub-table of
 * sign-offs (signOffs, doc_approvals_sign_offs) it keeps with neither a
 * deadline queue nor a change queue, as sqlgen writes one: a surrogate id,
 * the row's key with ON DELETE CASCADE and the unique index of (row,
 * number). The gate on sent excludes the creator, whom the audit
 * created_by holds, and a change of title consumes its sign-offs.
 */
const docLayout: RulesLayout = {
  types: {
    Doc: {
      type: 'Doc',
      table: 'doc',
      key: { field: 'id', column: 'id', type: 'string' },
      version: '_version',
      own: [
        { field: 'title', column: 'title', type: 'string' },
        { field: 'urgent', column: 'urgent', type: 'boolean' },
        { field: 'attempts', column: 'attempts', type: 'number' },
        { field: 'data', column: 'data', type: 'Generic.JSON' },
      ],
      audit: [{ field: 'createdBy', column: 'created_by', type: 'Identity.UUID' }],
      uses: [
        {
          id: 'Approvals',
          behavior: 'Approvals',
          member: 'approvals',
          fields: [],
          operations: [
            { name: 'signOff', writes: true },
            { name: 'withdrawSignOff', writes: true },
            { name: 'listSignOffs', writes: false },
          ],
          tables: [
            {
              name: 'signOffs',
              table: 'doc_approvals_sign_offs',
              row: 'doc_id',
              key: { name: 'number', field: 'number', column: 'number', type: 'string' },
              fields: [
                { name: 'state', field: 'state', column: 'state', type: 'string' },
                { name: 'subject', field: 'subject', column: 'subject', type: 'string' },
                { name: 'note', field: 'note', column: 'note', type: 'string' },
                { name: 'at', field: 'at', column: 'at', type: 'Temporal.DateTime', convert: 'epochMillis' },
                { name: 'consumedAt', field: 'consumedAt', column: 'consumed_at', type: 'Temporal.DateTime', convert: 'epochMillis' },
              ],
            },
          ],
        },
        {
          id: 'status',
          behavior: 'Workflow',
          anchor: 'status',
          member: 'status',
          fields: [
            { name: 'status', field: 'status', column: 'status', type: 'string', value: true },
            { name: 'enteredAt', field: 'statusEnteredAt', column: 'status_entered_at', type: 'Temporal.DateTime', convert: 'epochMillis' },
          ],
          operations: [{ name: 'transition', writes: true }],
        },
      ],
      spec: {
        name: 'Doc',
        uses: [
          { behavior: 'Approvals', config: { gates: { sent: { count: 1, permission: 'docs.review', resetOn: ['title'] } } }, anchor: 'type' },
          { behavior: 'Workflow', config: { states: ['draft', 'sent'], transitions: [{ from: 'draft', to: 'sent' }] }, anchor: { field: 'status' } },
        ],
        fields: [
          { name: 'title', type: 'string' },
          { name: 'urgent', type: 'boolean' },
          { name: 'attempts', type: 'number' },
          { name: 'data', type: 'Generic.JSON' },
          { name: 'createdBy', type: 'Identity.UUID' },
        ],
      },
    },
  },
};

/** A UUID's two forms, its column's hyphenated text and its base62 wire form: dana's, and erin's. */
const DANA = { column: '0f8fad5b-d9cb-469f-a165-70867728950e', wire: 'TMXp8QPR6yGvKuV2GZQ10' };
const ERIN = { column: '7c9e6679-7425-40de-944b-e07fc1f90ae7', wire: '3n9R8RlRyqVJBUQUv6toL1' };

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
      const uuid = db.dialect === 'postgres' ? 'UUID' : 'TEXT';
      await db.exec(`CREATE TABLE question (
  id ${t.key},
  prompt ${t.text} NOT NULL,
  asked_of ${uuid},
  created_by ${uuid},
  status ${t.text},
  status_entered_at ${t.timestamp},
  _version ${t.version}
)`);
      if (db.dialect === 'postgres') for (const ddl of bumpVersion('question')) await db.exec(ddl);
      await db.exec(`CREATE TABLE doc (
  id ${t.key},
  title ${t.text} NOT NULL,
  urgent ${t.boolean},
  attempts ${t.number},
  data ${t.json},
  created_by ${uuid},
  status ${t.text},
  status_entered_at ${t.timestamp},
  _version ${t.version}
)`);
      if (db.dialect === 'postgres') for (const ddl of bumpVersion('doc')) await db.exec(ddl);
      await db.exec(`CREATE TABLE doc_approvals_sign_offs (
  id ${db.dialect === 'postgres' ? 'UUID DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY' : 'INTEGER PRIMARY KEY'},
  doc_id ${t.text} NOT NULL REFERENCES doc (id) ON DELETE CASCADE,
  number ${t.text} NOT NULL,
  state ${t.text},
  subject ${t.text},
  note ${t.text},
  at ${t.timestamp},
  consumed_at ${t.timestamp}
)`);
      await db.exec('CREATE UNIQUE INDEX doc_approvals_sign_offs_row_key ON doc_approvals_sign_offs (doc_id, number)');
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

    test('a row read holds the audit fields the layout names among its own fields, a UUID in base62', async () => {
      const onQuestions = (store: RulesStore): RulesHost => ({ core: core(), layout: questionLayout, store, now: () => clock });
      // Written past the adapter, as the repositories' create stamps
      // created_by with its hyphenated text.
      await db.exec(`INSERT INTO question (id, prompt, asked_of, created_by, status) VALUES (${p(1)}, ${p(2)}, ${p(3)}, ${p(4)}, 'open')`, ['q1', 'Which table?', ERIN.column, DANA.column]);
      await db.exec(`INSERT INTO question (id, prompt, status) VALUES (${p(1)}, ${p(2)}, 'open')`, ['q2', 'Which chair?']);
      expect(await db.transaction(store => readRow(onQuestions(store), 'Question', 'q1'))).toEqual({
        own: { prompt: 'Which table?', askedOf: ERIN.wire, createdBy: DANA.wire },
        owned: { status: { status: 'open' } },
        version: 1,
      });
      // A NULL audit column is absent, as any column with no value is.
      expect(await db.transaction(store => readRow(onQuestions(store), 'Question', 'q2'))).toEqual({ own: { prompt: 'Which chair?' }, owned: { status: { status: 'open' } }, version: 1 });
    });

    test('a by that names createdBy moves the row for the subject its audit column holds, refuses another, and its write leaves the audit column as it was', async () => {
      const onQuestions = (store: RulesStore): RulesHost => ({ core: core(), layout: questionLayout, store, now: () => clock });
      const withdraw = (caller: CoreCaller) => db.transaction(store => runOperation(onQuestions(store), 'Question', 'q1', 'status', 'transition', { to: 'withdrawn' }, caller));
      await expect(withdraw({ subject: ERIN.wire, permissions: [] })).rejects.toMatchObject({
        status: 409,
        code: 'vetoed',
        details: { vetoCode: 'not_principal', vetoDetails: { from: 'open', to: 'withdrawn', by: ['createdBy'] } },
      });
      // The caller's subject in either of a UUID's forms is the one the column holds.
      expect(await withdraw({ subject: DANA.column, permissions: [] })).toEqual({ from: 'open', to: 'withdrawn' });
      const [row] = await db.rows(`SELECT created_by, status, _version FROM question WHERE id = ${p(1)}`, ['q1']);
      expect({ createdBy: String(row!.created_by), status: row!.status, version: Number(row!._version) }).toEqual({ createdBy: DANA.column, status: 'withdrawn', version: 2 });
    });

    /** A host over a transaction of the database with docLayout, at the test's clock. */
    const onDocs = (store: RulesStore): RulesHost => ({ core: core(), layout: docLayout, store, now: () => (clock += 1000) });
    const readDoc = (id: string) => db.transaction(store => readRow(onDocs(store), 'Doc', id));

    test("own columns read back as the core's JSON: a boolean, from SQLite's 1 and 0 too, a number, and a JSON column's object, list and string", async () => {
      const data = { steps: [1, 'two', { three: 3 }], done: false, none: null };
      await db.transaction(store => runCreate(onDocs(store), 'Doc', { title: 'Plan', urgent: true, attempts: 2.5, data }, alice, { id: 'd1' }));
      expect(await readDoc('d1')).toEqual({
        own: { title: 'Plan', urgent: true, attempts: 2.5, data },
        owned: { status: { status: 'draft', enteredAt: expect.any(Number) } },
        version: 1,
      });
      const [raw] = await db.rows(`SELECT urgent FROM doc WHERE id = ${p(1)}`, ['d1']);
      expect(raw!.urgent).toBe(db.dialect === 'postgres' ? true : 1);
      await db.transaction(store => runUpdate(onDocs(store), 'Doc', 'd1', { urgent: false, attempts: 3, data: ['a', 2] }, alice));
      expect((await readDoc('d1'))?.own).toEqual({ title: 'Plan', urgent: false, attempts: 3, data: ['a', 2] });
      // A JSON string is a string, "5" among them, never the number its
      // text spells, and a JSON number a number.
      await db.transaction(store => runUpdate(onDocs(store), 'Doc', 'd1', { data: '5' }, alice));
      expect((await readDoc('d1'))?.own).toMatchObject({ data: '5' });
      await db.transaction(store => runUpdate(onDocs(store), 'Doc', 'd1', { data: 5, urgent: null }, alice));
      expect((await readDoc('d1'))?.own).toEqual({ title: 'Plan', attempts: 3, data: 5 });
    });

    test('a sub-table of a table with no deadline or change queue: a sign-off inserts its row, a read-only operation reads the rows and writes nothing, an update consumes them, a withdrawal deletes one, and a delete of the row deletes them', async () => {
      const reviewer: CoreCaller = { subject: 'erin', permissions: ['docs.review'] };
      const run = (operation: string, params: unknown, caller: CoreCaller) => db.transaction(store => runOperation(onDocs(store), 'Doc', 'd2', 'Approvals', operation, params, caller));
      const version = async () => Number((await db.rows(`SELECT _version FROM doc WHERE id = ${p(1)}`, ['d2']))[0]!._version);
      const subRows = () => db.transaction(store => readSubRows(onDocs(store), 'Doc', 'd2', 'Approvals', 'signOffs'));
      await db.transaction(store => runCreate(onDocs(store), 'Doc', { title: 'Memo' }, alice, { id: 'd2' }));
      // The audit column the repositories stamp, written past the adapter:
      // the creator the gate excludes, whom a row read holds as createdBy.
      await db.exec(`UPDATE doc SET created_by = ${p(1)} WHERE id = ${p(2)}`, [DANA.column, 'd2']);
      await expect(run('signOff', { state: 'sent' }, { subject: DANA.wire, permissions: ['docs.review'] })).rejects.toMatchObject({
        code: 'vetoed',
        details: { vetoCode: 'creator_cannot_sign' },
      });

      const before = await version();
      expect(await run('signOff', { state: 'sent', note: 'Fine' }, reviewer)).toMatchObject({ state: 'sent', subject: 'erin', note: 'Fine', signedBy: ['erin'], count: 1 });
      expect(await version()).toBe(before + 1);
      expect(await subRows()).toEqual([{ key: '0000000001', values: { state: 'sent', subject: 'erin', note: 'Fine', at: expect.any(Number) } }]);
      // A read-only operation reads the sub-table rows and writes nothing.
      expect(await run('listSignOffs', {}, reviewer)).toEqual({ items: [{ state: 'sent', subject: 'erin', note: 'Fine', at: expect.any(Number) }], next: null });
      expect(await version()).toBe(before + 1);

      // An update of the field the gate's resetOn names consumes its
      // sign-off: an update of the sub-table row.
      await db.transaction(store => runUpdate(onDocs(store), 'Doc', 'd2', { title: 'Memo, revised' }, alice));
      expect((await subRows())[0]!.values).toEqual({ state: 'sent', subject: 'erin', note: 'Fine', at: expect.any(Number), consumedAt: expect.any(Number) });
      // The next sign-off takes the next number, and its withdrawal deletes its row.
      expect(await run('signOff', { state: 'sent' }, reviewer)).toMatchObject({ state: 'sent', subject: 'erin', signedBy: ['erin'], count: 1 });
      expect((await subRows()).map(row => row.key)).toEqual(['0000000001', '0000000002']);
      expect(await run('withdrawSignOff', { state: 'sent' }, reviewer)).toMatchObject({ state: 'sent', subject: 'erin' });
      expect((await subRows()).map(row => row.key)).toEqual(['0000000001']);

      // A delete of the row deletes its sub-table rows.
      await db.transaction(store => runDelete(onDocs(store), 'Doc', 'd2', alice));
      expect(await readDoc('d2')).toBeNull();
      expect(await db.rows(`SELECT number FROM doc_approvals_sign_offs WHERE doc_id = ${p(1)}`, ['d2'])).toEqual([]);
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
