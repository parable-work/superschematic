import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import {
  RulesRefusal,
  execute,
  readArmedDeadlines,
  readLinked,
  readRow,
  readSubRows,
  runChange,
  runCreate,
  runOperation,
  type CoreAction,
  type CoreAnswers,
  type CoreCaller,
  type CoreChangeSet,
  type CoreCommand,
  type CoreStep,
  type CoreTypeSpec,
  type DeadlineClaim,
  type RulesCore,
  type RulesHost,
  type RulesLayout,
  type TableChange,
} from './rules';
import { bumpVersion, columnTypes, postgresDatabase, rulesDsn, sqliteDatabase, type TestDatabase } from './rules.testing';
import { uuidFromDb } from './uuid';

// The adapter's change command (D103) over a real database of each
// dialect, bun:sqlite and Postgres, with a core this test writes, so the
// adapter's part is held apart from any behavior's rules: the conversion
// of a change queue's message as its trigger wrote it into the core's
// change (the key to its wire form, each image by the layout as a row read
// converts its columns), the linked read, the gone row's writes, and the
// claim of a message a worker runs. Ship, whose key is a UUID, keeps a use
// Log with an owned column, a sub-table of entries and a deadline queue,
// and its writes are heard (ship_change_queue); Crew holds a Ship's key in
// shipId, a to-one relation. Both are soft-deletable. The images are made
// with SQL as each dialect's trigger makes them (jsonb_build_object,
// json_object), and a claim with SQL, as a worker's claim statement makes
// one. The Postgres cases are skipped without
// SUPERSCHEMATIC_RULES_TEST_DATABASE_URL.

const layout: RulesLayout = {
  types: {
    Ship: {
      type: 'Ship',
      table: 'ship',
      key: { field: 'id', column: 'id', type: 'Identity.UUID' },
      version: '_version',
      live: { column: 'deleted_at' },
      own: [
        { field: 'name', column: 'name', type: 'string' },
        { field: 'launchedAt', column: 'launched_at', type: 'Temporal.DateTime', convert: 'epochMillis' },
        { field: 'docked', column: 'docked', type: 'boolean' },
        { field: 'cargo', column: 'cargo', type: 'Generic.JSON' },
      ],
      uses: [
        {
          id: 'Log',
          behavior: 'Log',
          member: 'log',
          fields: [{ name: 'state', field: 'logState', column: 'log_state', type: 'string' }],
          operations: [{ name: 'crew', writes: false }],
          tables: [
            {
              name: 'entries',
              table: 'ship_log_entries',
              row: 'ship_id',
              key: { name: 'name', field: 'name', column: 'name', type: 'string' },
              fields: [{ name: 'note', field: 'note', column: 'note', type: 'string' }],
            },
          ],
        },
      ],
      deadlines: { queue: 'ShipDeadline', table: 'ship_deadline_queue', rowKey: 'row_key', use: 'use_id', name: 'name', at: 'at', payload: 'payload' },
      changes: { queue: 'ShipChange', table: 'ship_change_queue', rowKey: 'row_key', write: 'write', at: 'at', before: 'before', after: 'after' },
      spec: { name: 'Ship', uses: [{ behavior: 'Log', anchor: 'type' }] },
    },
    Crew: {
      type: 'Crew',
      table: 'crew',
      key: { field: 'id', column: 'id', type: 'string' },
      version: '_version',
      live: { column: 'deleted_at' },
      own: [
        { field: 'name', column: 'name', type: 'string' },
        { field: 'shipId', column: 'ship_id', type: 'Identity.UUID' },
      ],
      uses: [],
      spec: { name: 'Crew', uses: [], fields: [{ name: 'name', type: 'string' }, { name: 'shipId', type: 'Identity.UUID', relation: { name: 'ship', type: 'Ship' } }] },
    },
  },
};

const changed = (changes: Partial<CoreChangeSet>): CoreStep => ({ done: { changed: { rows: [], subRows: [], events: [], deadlines: [], result: null, ...changes } } });

/** The change actions the core was handed, in order. */
const seen: Extract<CoreAction, { kind: 'change' }>[] = [];

/**
 * Log, as a core: a create inserts the row with its entry first and arms
 * its deadline ping; a change records the action it was handed, writes
 * gone for a delete's, and otherwise reads the row, writes gone when it is
 * not there, refuses one whose name after the write is Refused, and sets
 * Log.state to the write's kind, fenced on the version it read; the
 * operation crew answers the rows of Crew whose shipId holds the row's key,
 * one linked read of the core's page.
 */
const log: RulesCore = {
  run(spec: CoreTypeSpec, command: CoreCommand, answers: CoreAnswers = {}): CoreStep {
    const action = command.action;
    if (action.kind === 'create') {
      const key = action.id!;
      const ref = { type: spec.name, key };
      if (spec.name !== 'Ship') return changed({ rows: [{ kind: 'create', type: spec.name, key, own: action.own ?? {}, owned: {} }] });
      return changed({
        rows: [{ kind: 'create', type: 'Ship', key, own: action.own ?? {}, owned: { Log: { state: 'new' } } }],
        subRows: [{ row: ref, useId: 'Log', table: 'entries', key: 'first', change: { kind: 'insert', values: { note: 'launched' } } }],
        deadlines: [{ kind: 'arm', row: ref, useId: 'Log', name: 'ping', at: command.now + 60_000, payload: null }],
      });
    }
    if (action.kind === 'operation' && action.operation === 'crew') {
      const read = (answers.reads ?? []).find(answered => answered.request.kind === 'linked');
      if (read === undefined) return { read: [{ kind: 'linked', type: 'Crew', field: 'shipId', key: action.key, limit: 500 }] };
      return changed({ result: 'linked' in read.answer ? read.answer.linked : null });
    }
    if (action.kind !== 'change') throw new Error(`log runs no ${action.kind}`);
    if (!seen.includes(action)) seen.push(action);
    if (action.change.write === 'delete') return changed({ rows: [{ kind: 'gone', type: spec.name, key: action.key }] });
    const read = (answers.reads ?? []).find(answered => answered.request.kind === 'row');
    if (read === undefined) return { read: [{ kind: 'row', type: spec.name, key: action.key }] };
    const row = 'row' in read.answer ? read.answer.row : null;
    if (row === null) return changed({ rows: [{ kind: 'gone', type: spec.name, key: action.key }] });
    if (action.change.after?.own?.name === 'Refused') {
      return { done: { refused: { code: 'invalid_instance', status: 422, class: 'InstanceValidationError', message: `${spec.name} ${action.key}: refused` } } };
    }
    return changed({ rows: [{ kind: 'update', type: spec.name, key: action.key, expectedVersion: row.version, own: {}, owned: { Log: { state: action.change.write } } }] });
  },
};

const worker: CoreCaller = { subject: 'ships-api/ShipLog', permissions: [] };

const dialects: { name: string; skip: boolean; open: () => Promise<TestDatabase> }[] = [
  { name: 'bun:sqlite', skip: false, open: async () => sqliteDatabase() },
  { name: 'Postgres', skip: rulesDsn === '', open: postgresDatabase },
];

for (const dialect of dialects) {
  describe.skipIf(dialect.skip)(`changes on ${dialect.name}`, () => {
    let db: TestDatabase;
    let now = 1_800_000_000_000;
    const p = (n: number) => (db.dialect === 'postgres' ? `$${n}` : `?${n}`);
    const pg = () => db.dialect === 'postgres';

    beforeAll(async () => {
      db = await dialect.open();
      const t = columnTypes(db.dialect);
      const uuid = pg() ? 'UUID' : 'TEXT';
      const uuidKey = pg() ? 'UUID DEFAULT gen_random_uuid()' : 'TEXT DEFAULT (lower(hex(randomblob(16))))';
      const stamp = pg() ? 'TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP' : "TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))";
      await db.exec(`CREATE TABLE ship (id ${uuid} PRIMARY KEY, name ${t.text}, launched_at ${t.timestamp}, docked ${t.boolean}, cargo ${t.json}, log_state ${t.text}, deleted_at ${t.timestamp}, _version ${t.version})`);
      await db.exec(`CREATE TABLE crew (id ${t.key}, name ${t.text}, ship_id ${uuid}, deleted_at ${t.timestamp}, _version ${t.version})`);
      if (pg()) for (const table of ['ship', 'crew']) for (const ddl of bumpVersion(table)) await db.exec(ddl);
      // No foreign key, so a raw delete of a ship leaves its entries on
      // either dialect, as SQLite's connection does unless it turns them
      // on, and the gone row's writes are what delete them.
      await db.exec(`CREATE TABLE ship_log_entries (ship_id ${uuid} NOT NULL, name TEXT NOT NULL, note TEXT, PRIMARY KEY (ship_id, name))`);
      await db.exec(`CREATE TABLE ship_deadline_queue (
  id ${uuidKey} NOT NULL PRIMARY KEY,
  row_key ${uuid} NOT NULL,
  use_id TEXT NOT NULL,
  name TEXT NOT NULL,
  at ${t.integer} NOT NULL,
  payload ${t.json},
  state TEXT DEFAULT 'ready' NOT NULL,
  attempts INTEGER DEFAULT 0 NOT NULL,
  due_at ${stamp} NOT NULL,
  claim_expires_at ${t.timestamp},
  claim_token ${uuid},
  last_error TEXT,
  updated_at ${stamp} NOT NULL
)`);
      await db.exec(`CREATE TABLE ship_change_queue (
  id ${uuidKey} NOT NULL PRIMARY KEY,
  row_key ${uuid} NOT NULL,
  write TEXT NOT NULL,
  at ${t.integer} NOT NULL,
  before ${t.json},
  after ${t.json},
  state TEXT DEFAULT 'ready' NOT NULL,
  attempts INTEGER DEFAULT 0 NOT NULL,
  due_at ${stamp} NOT NULL,
  claim_expires_at ${t.timestamp},
  claim_token ${uuid},
  last_error TEXT,
  updated_at ${stamp} NOT NULL
)`);
    });
    afterAll(async () => db?.close());

    /** Runs fn on a host over a transaction of the database, at the next tick of the test's clock. */
    const inTransaction = <T>(fn: (host: RulesHost) => Promise<T>): Promise<T> => {
      const at = (now += 1000);
      return db.transaction(store => fn({ core: log, layout, store, now: () => at }));
    };

    /** A ship's image as its trigger writes it: the dialect's JSON object of the image's columns, by column. */
    const image = async (id: string): Promise<unknown> => {
      const columns = ['name', 'launched_at', 'docked', 'cargo', 'log_state'];
      const build = pg() ? 'jsonb_build_object' : 'json_object';
      const [row] = await db.rows(`SELECT ${build}(${columns.map(c => `'${c}', "${c}"`).join(', ')}) AS image FROM ship WHERE id = ${p(1)}`, [id]);
      return row!.image;
    };

    /** Creates a ship through the core: its key in its database form, and in its wire form. */
    const ship = async (name: string): Promise<{ id: string; key: string }> => {
      const id = crypto.randomUUID();
      const key = uuidFromDb(id);
      await inTransaction(host => runCreate(host, 'Ship', { name, launchedAt: Date.UTC(2026, 10, 2, 10, 0, 0, 123), docked: true, cargo: { holds: ['tea', 'salt'] } }, worker, { id: key }));
      return { id, key };
    };

    /** Writes a message of ship_change_queue as the trigger writes one, and claims it as a worker's claim statement does: claimed, under a new token. */
    const claimMessage = async (id: string, write: string, before: unknown, after: unknown): Promise<DeadlineClaim> => {
      const json = (value: unknown) => (value === null ? null : typeof value === 'string' ? value : JSON.stringify(value));
      await db.exec(`INSERT INTO ship_change_queue (row_key, write, at, before, after) VALUES (${p(1)}, ${p(2)}, ${p(3)}, ${p(4)}, ${p(5)})`, [id, write, now, json(before), json(after)]);
      const [row] = await db.rows(`SELECT id FROM ship_change_queue WHERE row_key = ${p(1)} AND state = 'ready'`, [id]);
      const token = crypto.randomUUID();
      expect(await db.exec(`UPDATE ship_change_queue SET state = 'claimed', claim_token = ${p(1)}, attempts = attempts + 1 WHERE id = ${p(2)}`, [token, row!.id])).toBe(1);
      return { id: String(row!.id), token };
    };

    /** The messages of a ship's changes, every state. */
    const messages = async (id: string) => db.rows(`SELECT state, attempts, claim_token FROM ship_change_queue WHERE row_key = ${p(1)}`, [id]);

    test('a change is handed to the core as its trigger wrote it: the key in its wire form, and each image by the layout as a row read converts it', async () => {
      const { id, key } = await ship('Nautilus');
      const before = await image(id);
      await db.exec(`UPDATE ship SET name = ${p(1)}, docked = ${p(2)} WHERE id = ${p(3)}`, ['Argo', pg() ? false : 0, id]);
      const after = await image(id);
      const run = await inTransaction(host => runChange(host, 'Ship', { key: id, write: 'update', at: String(now), before, after }, worker));
      expect(run).toMatchObject({ ran: true, result: { key, attempts: 1 } });
      const handed = seen.at(-1)!;
      expect(handed.key).toBe(key);
      expect(handed.change.write).toBe('update');
      expect(handed.change.at).toBe(now);
      // Each image is what a row read gives of the same row.
      const read = await inTransaction(host => readRow(host, 'Ship', key));
      expect(handed.change.after).toEqual({ own: { ...read!.own }, owned: { Log: { state: 'new' } } });
      expect(handed.change.after!.own).toMatchObject({ name: 'Argo', launchedAt: Date.UTC(2026, 10, 2, 10, 0, 0, 123), docked: false });
      expect(handed.change.before!.own).toMatchObject({ name: 'Nautilus', docked: true });
      expect(handed.change.before!.own!.cargo).toEqual(read!.own!.cargo);
      // The run wrote the row: Log.state is the write's kind.
      expect((await inTransaction(host => readRow(host, 'Ship', key)))!.owned).toEqual({ Log: { state: 'update' } });
    });

    test("a timestamp with an offset, a boolean's 1 or 0, JSON text, a hyphenated UUID, a null and an absent column convert as their columns read", async () => {
      const { id, key } = await ship('Calypso');
      const at = Date.UTC(2026, 10, 2, 10, 0, 0, 123);
      const after = {
        name: 'Calypso',
        // Postgres writes a timestamp with the session's offset, and its
        // microseconds; SQLite writes ISO text in UTC.
        launched_at: pg() ? '2026-11-02T15:30:00.123456+05:30' : new Date(at).toISOString(),
        docked: pg() ? true : 1,
        cargo: null,
        log_state: 'new',
        unknown: 'passed over',
      };
      const insert = await inTransaction(host => runChange(host, 'Ship', { key: id, write: 'insert', at: now, after: pg() ? after : JSON.stringify(after) }, worker));
      expect(insert.ran).toBe(true);
      expect(seen.at(-1)!).toEqual({
        kind: 'change',
        key,
        change: { write: 'insert', at: now, after: { own: { name: 'Calypso', launchedAt: at, docked: true }, owned: { Log: { state: 'new' } } } },
      });
      // SQLite's json_object writes a boolean column's 0 or 1; a true or
      // false is taken as either.
      const sqliteTrue = await inTransaction(host => runChange(host, 'Ship', { key, write: 'update', at: now, before: { docked: pg() ? false : 0 }, after: { docked: true, name: null } }, worker));
      expect(sqliteTrue.ran).toBe(true);
      expect(seen.at(-1)!.change).toEqual({ write: 'update', at: now, before: { own: { docked: false }, owned: {} }, after: { own: { docked: true }, owned: {} } });
      // A key in its wire form is taken as it is.
      expect(seen.at(-1)!.key).toBe(key);
    });

    test('a write of another kind, or a time that is no number, refuses the change as internal', async () => {
      const { id } = await ship('Endeavour');
      const refused = async (change: TableChange) => {
        try {
          await inTransaction(host => runChange(host, 'Ship', change, worker));
        } catch (error) {
          expect(error).toBeInstanceOf(RulesRefusal);
          return (error as RulesRefusal).refusal;
        }
        throw new Error('the change was not refused');
      };
      expect(await refused({ key: id, write: 'truncate', at: now })).toEqual({
        code: 'internal',
        status: 500,
        class: 'Error',
        message: `rules: Ship's change of ${uuidFromDb(id)} is a write of truncate, which is no insert, update or delete`,
      });
      expect((await refused({ key: id, write: 'update', at: 'soon' })).code).toBe('internal');
      // A claim of a change of a table that keeps no change queue.
      await expect(inTransaction(host => runChange(host, 'Crew', { key: 'c0', write: 'insert', at: now }, worker, { claim: { id: crypto.randomUUID(), token: crypto.randomUUID() } }))).rejects.toMatchObject({
        refusal: { code: 'internal', message: "rules: Crew keeps no change queue in the behaviors' layout, and a worker's claim runs a change of it" },
      });
    });

    test('the linked read pages the rows whose field holds a key in key order, a soft-deleted row left out; a type the layout lacks has none', async () => {
      const { id, key } = await ship('Beagle');
      const other = await ship('Bounty');
      for (const [crew, name, of, deleted] of [
        ['c1', 'Ann', id, false],
        ['c2', 'Ben', id, false],
        ['c3', 'Cy', id, true],
        ['c4', 'Di', id, false],
        ['c5', 'Ed', id, false],
        ['c6', 'Flo', other.id, false],
      ] as const) {
        await db.exec(`INSERT INTO crew (id, name, ship_id, deleted_at) VALUES (${p(1)}, ${p(2)}, ${p(3)}, ${p(4)})`, [crew, name, of, deleted ? (pg() ? new Date() : new Date().toISOString()) : null]);
      }
      const row = (crew: string, name: string) => ({ key: crew, row: { own: { name, shipId: key }, owned: {}, version: 1 } });
      const first = await inTransaction(host => readLinked(host, 'Crew', 'shipId', key, undefined, 2));
      expect(first).toEqual({ rows: [row('c1', 'Ann'), row('c2', 'Ben')], more: true });
      const second = await inTransaction(host => readLinked(host, 'Crew', 'shipId', key, 'c2', 2));
      expect(second).toEqual({ rows: [row('c4', 'Di'), row('c5', 'Ed')], more: false });
      // Through the core, one read of its page.
      expect(await inTransaction(host => runOperation(host, 'Ship', key, 'Log', 'crew', {}, worker))).toEqual({ rows: [row('c1', 'Ann'), row('c2', 'Ben'), row('c4', 'Di'), row('c5', 'Ed')], more: false });
      expect(await inTransaction(host => readLinked(host, 'Nobody', 'shipId', key, undefined, 500))).toEqual({ rows: [], more: false });
      await expect(inTransaction(host => readLinked(host, 'Crew', 'captain', key, undefined, 500))).rejects.toMatchObject({
        refusal: { code: 'internal', message: "rules: Crew has no own field captain in the behaviors' layout" },
      });
    });

    test("a gone row's deadlines and sub-table rows go; a soft-deleted row keeps its sub-table rows, and a row that came back keeps everything", async () => {
      const entries = (id: string) => db.rows(`SELECT name FROM ship_log_entries WHERE ship_id = ${p(1)}`, [id]);
      const armed = (key: string) => inTransaction(host => readArmedDeadlines(host, 'Ship', key));
      // Gone past the adapter: a raw delete, which leaves the row's entries
      // and its deadline's message.
      const gone = await ship('Erebus');
      await db.exec(`DELETE FROM ship WHERE id = ${p(1)}`, [gone.id]);
      expect(await entries(gone.id)).toHaveLength(1);
      const run = await inTransaction(host => runChange(host, 'Ship', { key: gone.id, write: 'update', at: now, before: { name: 'Erebus' }, after: { name: 'Erebus' } }, worker));
      expect(run.ran && run.result.changes.rows).toEqual([{ kind: 'gone', type: 'Ship', key: gone.key }]);
      expect(await entries(gone.id)).toEqual([]);
      expect(await db.rows(`SELECT 1 FROM ship_deadline_queue WHERE row_key = ${p(1)}`, [gone.id])).toEqual([]);
      // Soft-deleted: no live row, so its deadlines go, and a row of the
      // key, so its sub-table rows stay.
      const soft = await ship('Terror');
      await db.exec(`UPDATE ship SET deleted_at = ${p(1)} WHERE id = ${p(2)}`, [pg() ? new Date() : new Date().toISOString(), soft.id]);
      await inTransaction(host => runChange(host, 'Ship', { key: soft.id, write: 'update', at: now }, worker));
      expect(await entries(soft.id)).toHaveLength(1);
      expect(await db.rows(`SELECT 1 FROM ship_deadline_queue WHERE row_key = ${p(1)}`, [soft.id])).toEqual([]);
      // Came back: a gone the core wrote for a row the table holds live
      // again deletes nothing.
      const back = await ship('Discovery');
      await inTransaction(host => runChange(host, 'Ship', { key: back.id, write: 'delete', at: now, before: { name: 'Discovery' } }, worker));
      expect(await entries(back.id)).toHaveLength(1);
      expect(await armed(back.key)).toHaveLength(1);
    });

    test('a change a worker claimed runs, and its unit completes the message', async () => {
      const { id, key } = await ship('Fram');
      const after = await image(id);
      const claim = await claimMessage(id, 'update', null, after);
      const run = await inTransaction(host => runChange(host, 'Ship', { key: id, write: 'update', at: now, after }, worker, { claim }));
      expect(run).toMatchObject({ ran: true, result: { key } });
      expect(await messages(id)).toEqual([]);
    });

    test('a claim another worker took over is lost: its token spends nothing, and nothing is applied', async () => {
      const { id, key } = await ship('Gjoa');
      const claim = await claimMessage(id, 'update', null, await image(id));
      await db.exec(`UPDATE ship_change_queue SET claim_token = ${p(1)} WHERE id = ${p(2)}`, [crypto.randomUUID(), claim.id]);
      expect(await inTransaction(host => runChange(host, 'Ship', { key: id, write: 'update', at: now }, worker, { claim }))).toEqual({ ran: false, why: 'claim lost' });
      expect((await inTransaction(host => readRow(host, 'Ship', key)))!.owned).toEqual({ Log: { state: 'new' } });
      expect(await messages(id)).toHaveLength(1);
    });

    test('a refused change applies nothing and leaves its message claimed, for the worker to fail', async () => {
      const { id, key } = await ship('Hood');
      const claim = await claimMessage(id, 'update', null, { name: 'Refused' });
      await expect(inTransaction(host => runChange(host, 'Ship', { key: id, write: 'update', at: now, after: { name: 'Refused' } }, worker, { claim }))).rejects.toMatchObject({
        refusal: { code: 'invalid_instance', message: `Ship ${key}: refused` },
      });
      expect((await messages(id)).map(row => row.state)).toEqual(['claimed']);
      expect((await inTransaction(host => readRow(host, 'Ship', key)))!.owned).toEqual({ Log: { state: 'new' } });
    });

    test("a change without a claim spends nothing, and a transcript's change runs through execute as any command", async () => {
      const { id, key } = await ship('Investigator');
      await db.exec(`INSERT INTO ship_change_queue (row_key, write, at) VALUES (${p(1)}, 'update', ${p(2)})`, [id, now]);
      expect((await inTransaction(host => runChange(host, 'Ship', { key, write: 'update', at: now }, worker))).ran).toBe(true);
      expect(await messages(id)).toHaveLength(1);
      const applied = await inTransaction(host => execute(host, 'Ship', { caller: worker, now, request: 'r1', action: { kind: 'change', key, change: { write: 'insert', at: now } } }));
      expect(applied).toMatchObject({ key, attempts: 1, result: null });
      expect((await inTransaction(host => readRow(host, 'Ship', key)))!.owned).toEqual({ Log: { state: 'insert' } });
      expect(await inTransaction(host => readSubRows(host, 'Ship', key, 'Log', 'entries'))).toEqual([{ key: 'first', values: { note: 'launched' } }]);
    });
  });
}
