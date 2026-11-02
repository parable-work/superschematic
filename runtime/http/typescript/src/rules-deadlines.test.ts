import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import {
  MAX_ATTEMPTS,
  RulesRefusal,
  VETOED_RETRY_SECONDS,
  execute,
  readArmedDeadlines,
  readSubRows,
  runCreate,
  runDeadline,
  runDelete,
  runOperation,
  type CoreAnswers,
  type CoreCaller,
  type CoreChangeSet,
  type CoreCommand,
  type CoreRefusal,
  type CoreStep,
  type CoreTypeSpec,
  type DeadlineClaim,
  type RulesCore,
  type RulesHost,
  type RulesLayout,
  type RulesStore,
} from './rules';
import { bumpVersion, columnTypes, postgresDatabase, rulesDsn, sqliteDatabase, type TestDatabase } from './rules.testing';

// The adapter's sub-table rows and deadlines (D103) over a real database of
// each dialect, bun:sqlite and Postgres, with a core this test writes, so
// the unit's order and the pitfalls of a deadline a worker claims are held
// apart from any behavior's rules: Alarm's use Clock keeps one ring per row
// in a sub-table (alarm_clock_rings) and arms it as a message of the
// table's deadline queue (alarm_deadline_queue), whose tables are written
// by hand as sqlgen writes them. A claim is made with SQL, as a worker's
// claim statement makes one. A guard's veto of a fire, which no ported
// behavior's guard can make, is a core of the test's own that refuses the
// deadline command as vetoed. The Postgres cases are skipped without
// SUPERSCHEMATIC_RULES_TEST_DATABASE_URL.

const layout: RulesLayout = {
  types: {
    Alarm: {
      type: 'Alarm',
      table: 'alarm',
      key: { field: 'id', column: 'id', type: 'string' },
      version: '_version',
      own: [{ field: 'title', column: 'title', type: 'string' }],
      uses: [
        {
          id: 'Clock',
          behavior: 'Clock',
          member: 'clock',
          fields: [],
          operations: [
            { name: 'snooze', writes: true },
            { name: 'clear', writes: true },
            { name: 'peek', writes: false },
          ],
          tables: [
            {
              name: 'rings',
              table: 'alarm_clock_rings',
              row: 'alarm_id',
              key: { name: 'name', field: 'name', column: 'name', type: 'string' },
              fields: [
                { name: 'dueAt', field: 'dueAt', column: 'due_at', type: 'Temporal.DateTime', convert: 'epochMillis' },
                { name: 'fired', field: 'fired', column: 'fired', type: 'boolean' },
              ],
            },
          ],
        },
      ],
      deadlines: { queue: 'AlarmDeadline', table: 'alarm_deadline_queue', rowKey: 'row_key', use: 'use_id', name: 'name', at: 'at', payload: 'payload' },
      spec: { name: 'Alarm', uses: [{ behavior: 'Clock', anchor: 'type' }] },
    },
  },
};

const changed = (changes: Partial<CoreChangeSet>): CoreStep => ({ done: { changed: { rows: [], subRows: [], events: [], deadlines: [], result: null, ...changes } } });

/**
 * Clock, as a core: a create inserts the ring, due a second after the
 * create, and arms it; snooze moves it to params.at and arms it there;
 * clear clears it; peek answers the row's rings as the core reads them;
 * a deadline marks the ring fired, and arms it again a second on when its
 * payload says again; and a delete deletes the row. Every command but a
 * create and peek writes the row, fenced on the version it read.
 */
const clock: RulesCore = {
  run(_spec: CoreTypeSpec, command: CoreCommand, answers: CoreAnswers = {}): CoreStep {
    const action = command.action;
    const ref = (key: string) => ({ type: 'Alarm', key });
    if (action.kind === 'create') {
      const key = action.id!;
      const at = command.now + 1000;
      return changed({
        rows: [{ kind: 'create', type: 'Alarm', key, own: action.own ?? {}, owned: {} }],
        subRows: [{ row: ref(key), useId: 'Clock', table: 'rings', key: 'ring', change: { kind: 'insert', values: { dueAt: at, fired: false } } }],
        deadlines: [{ kind: 'arm', row: ref(key), useId: 'Clock', name: 'ring', at, payload: { n: 1 } }],
      });
    }
    if (!('key' in action)) throw new Error(`clock runs no ${action.kind}`);
    const key = action.key;
    const read = (answers.reads ?? []).find(answered => answered.request.kind === 'row');
    if (read === undefined) return { read: [{ kind: 'row', type: 'Alarm', key }] };
    const row = 'row' in read.answer ? read.answer.row : null;
    if (row === null) return { done: { refused: { code: 'not_found', status: 404, class: 'EngineError', message: `Alarm ${key} does not exist` } } };
    const update = { kind: 'update' as const, type: 'Alarm', key, expectedVersion: row.version, own: {}, owned: {} };
    const ring = (values: Record<string, unknown>) => ({ row: ref(key), useId: 'Clock', table: 'rings', key: 'ring', change: { kind: 'update' as const, values } });
    switch (action.kind) {
      case 'delete':
        return changed({ rows: [{ kind: 'delete', type: 'Alarm', key, expectedVersion: row.version }] });
      case 'deadline': {
        const again = (action.payload as { again?: boolean } | null)?.again === true;
        return changed({
          rows: [update],
          subRows: [ring(again ? { fired: true, dueAt: action.at + 1000 } : { fired: true })],
          deadlines: again ? [{ kind: 'arm', row: ref(key), useId: 'Clock', name: 'ring', at: action.at + 1000, payload: { again: true } }] : [],
        });
      }
      case 'operation': {
        if (action.operation === 'peek') {
          const rings = (answers.reads ?? []).find(answered => answered.request.kind === 'subRows');
          if (rings === undefined) return { read: [{ kind: 'subRows', type: 'Alarm', key, useId: 'Clock', table: 'rings' }] };
          return changed({ result: 'subRows' in rings.answer ? rings.answer.subRows : null });
        }
        if (action.operation === 'snooze') {
          const at = (action.params as { at: number }).at;
          return changed({ rows: [update], subRows: [ring({ dueAt: at, fired: false })], deadlines: [{ kind: 'arm', row: ref(key), useId: 'Clock', name: 'ring', at, payload: { n: 2 } }] });
        }
        if (action.operation === 'clear') return changed({ rows: [update], subRows: [ring({ dueAt: null })], deadlines: [{ kind: 'clear', row: ref(key), useId: 'Clock', name: 'ring' }] });
        throw new Error(`clock has no operation ${action.operation}`);
      }
      default:
        throw new Error(`clock runs no ${action.kind}`);
    }
  },
};

/** The refusal a guard of a behavior Hold makes of a fire, as the core gives a veto. */
const held: CoreRefusal = {
  code: 'vetoed',
  status: 409,
  class: 'BehaviorVetoError',
  message: 'behavior Hold vetoed fireTimer of Ticket t1: held',
  details: { behavior: 'Hold' },
};

/** Clock with a guard that holds every fire: a deadline command is refused as vetoed; the rest run as Clock's. */
const holding: RulesCore = {
  run(spec, command, answers) {
    if (command.action.kind === 'deadline') return { done: { refused: held } };
    return clock.run(spec, command, answers);
  },
};

const alice: CoreCaller = { subject: 'alice', permissions: [] };
const worker: CoreCaller = { subject: 'alarms-api/AlarmClock', permissions: [] };

const dialects: { name: string; skip: boolean; open: () => Promise<TestDatabase> }[] = [
  { name: 'bun:sqlite', skip: false, open: async () => sqliteDatabase() },
  { name: 'Postgres', skip: rulesDsn === '', open: postgresDatabase },
];

for (const dialect of dialects) {
  describe.skipIf(dialect.skip)(`sub-table rows and deadlines on ${dialect.name}`, () => {
    let db: TestDatabase;
    let now = 1_000_000_000;
    const p = (n: number) => (db.dialect === 'postgres' ? `$${n}` : `?${n}`);

    beforeAll(async () => {
      db = await dialect.open();
      const t = columnTypes(db.dialect);
      const pg = db.dialect === 'postgres';
      await db.exec(`CREATE TABLE alarm (id ${t.key}, title ${t.text}, _version ${t.version})`);
      if (pg) for (const ddl of bumpVersion('alarm')) await db.exec(ddl);
      await db.exec(`CREATE TABLE alarm_clock_rings (
  id ${pg ? 'UUID DEFAULT gen_random_uuid()' : "TEXT DEFAULT (lower(hex(randomblob(16))))"} NOT NULL PRIMARY KEY,
  alarm_id TEXT NOT NULL REFERENCES alarm (id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  due_at ${t.timestamp},
  fired ${t.boolean},
  UNIQUE (alarm_id, name)
)`);
      await db.exec(`CREATE TABLE alarm_deadline_queue (
  id ${pg ? 'UUID DEFAULT gen_random_uuid()' : "TEXT DEFAULT (lower(hex(randomblob(16))))"} NOT NULL PRIMARY KEY,
  row_key TEXT NOT NULL,
  use_id TEXT NOT NULL,
  name TEXT NOT NULL,
  at ${t.integer} NOT NULL,
  payload ${t.json},
  state TEXT DEFAULT 'ready' NOT NULL,
  attempts INTEGER DEFAULT 0 NOT NULL,
  due_at ${pg ? 'TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP' : "TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))"} NOT NULL,
  claim_expires_at ${t.timestamp},
  claim_token ${pg ? 'UUID' : 'TEXT'},
  last_error TEXT,
  updated_at ${pg ? 'TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP' : "TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))"} NOT NULL
)`);
      await db.exec(`CREATE UNIQUE INDEX alarm_deadline_queue_live ON alarm_deadline_queue (row_key, use_id, name) WHERE state IN ('ready', 'claimed')`);
    });
    afterAll(async () => db?.close());

    /** Runs fn on a host over a transaction of the database, at the next tick of the test's clock, on Clock unless another core is named. */
    const inTransaction = <T>(fn: (host: RulesHost) => Promise<T>, wrap: (store: RulesStore) => RulesStore = s => s, core: RulesCore = clock): Promise<T> => {
      const at = (now += 1000);
      return db.transaction(store => fn({ core, layout, store: wrap(store), now: () => at }));
    };

    /** The messages of a row's deadlines, every state, with their due time as epoch milliseconds. */
    const messages = async (key: string) =>
      (await db.rows(`SELECT id, name, at, state, due_at, payload FROM alarm_deadline_queue WHERE row_key = ${p(1)} ORDER BY at`, [key])).map(row => ({
        id: String(row.id),
        name: row.name,
        at: Number(row.at),
        state: row.state,
        dueAt: row.due_at instanceof Date ? row.due_at.getTime() : Date.parse(String(row.due_at)),
      }));

    /** Claims a row's live message as a worker's claim statement does: claimed, under a new token. */
    const claim = async (key: string): Promise<DeadlineClaim> => {
      const [row] = await db.rows(`SELECT id FROM alarm_deadline_queue WHERE row_key = ${p(1)} AND state = 'ready'`, [key]);
      const token = crypto.randomUUID();
      expect(await db.exec(`UPDATE alarm_deadline_queue SET state = 'claimed', claim_token = ${p(1)}, attempts = attempts + 1 WHERE id = ${p(2)}`, [token, row!.id])).toBe(1);
      return { id: String(row!.id), token };
    };

    const rings = (key: string) => inTransaction(host => readSubRows(host, 'Alarm', key, 'Clock', 'rings'));
    const armed = (key: string) => inTransaction(host => readArmedDeadlines(host, 'Alarm', key));

    test('a create inserts its sub-table row and arms its deadline as a message due when it falls due', async () => {
      await inTransaction(host => runCreate(host, 'Alarm', { title: 'Wake' }, alice, { id: 'a1' }));
      const at = now + 1000;
      expect(await rings('a1')).toEqual([{ key: 'ring', values: { dueAt: at, fired: false } }]);
      expect(await armed('a1')).toEqual([{ useId: 'Clock', name: 'ring', at, payload: { n: 1 } }]);
      expect(await messages('a1')).toEqual([{ id: expect.any(String), name: 'ring', at, state: 'ready', dueAt: at }]);
    });

    test('a read of sub-table rows answers them in key order, each value converted and a NULL absent', async () => {
      await db.exec(`INSERT INTO alarm_clock_rings (alarm_id, name, fired) VALUES (${p(1)}, ${p(2)}, ${p(3)})`, ['a1', 'early', db.dialect === 'postgres' ? true : 1]);
      const result = await inTransaction(host => runOperation(host, 'Alarm', 'a1', 'Clock', 'peek', {}, alice));
      expect(result).toEqual([
        { key: 'early', values: { fired: true } },
        { key: 'ring', values: { dueAt: expect.any(Number), fired: false } },
      ]);
      await db.exec(`DELETE FROM alarm_clock_rings WHERE name = 'early'`);
    });

    test('a re-arm replaces the live message, and a clear deletes it', async () => {
      const at = now + 60_000;
      await inTransaction(host => runOperation(host, 'Alarm', 'a1', 'Clock', 'snooze', { at }, alice));
      expect(await messages('a1')).toEqual([{ id: expect.any(String), name: 'ring', at, state: 'ready', dueAt: at }]);
      expect(await rings('a1')).toEqual([{ key: 'ring', values: { dueAt: at, fired: false } }]);
      await inTransaction(host => runOperation(host, 'Alarm', 'a1', 'Clock', 'clear', {}, alice));
      expect(await messages('a1')).toEqual([]);
      expect(await rings('a1')).toEqual([{ key: 'ring', values: { fired: false } }]);
    });

    test('a due deadline a worker claimed runs, and its unit completes the message', async () => {
      await inTransaction(host => runCreate(host, 'Alarm', { title: 'Tea' }, alice, { id: 'a2' }));
      const [message] = await messages('a2');
      const held = await claim('a2');
      const run = await inTransaction(host => runDeadline(host, 'Alarm', { key: 'a2', useId: 'Clock', name: 'ring', at: message!.at, payload: { n: 1 } }, worker, { claim: held }));
      expect(run).toMatchObject({ ran: true, result: { key: 'a2', attempts: 1 } });
      expect(await messages('a2')).toEqual([]);
      expect(await rings('a2')).toEqual([{ key: 'ring', values: { dueAt: message!.at, fired: true } }]);
    });

    test('a deadline re-armed while a worker holds its claim never fires on its old arming: the claim is lost and nothing is applied', async () => {
      await inTransaction(host => runCreate(host, 'Alarm', { title: 'Bread' }, alice, { id: 'a3' }));
      const [message] = await messages('a3');
      const held = await claim('a3');
      const later = now + 3_600_000;
      await inTransaction(host => runOperation(host, 'Alarm', 'a3', 'Clock', 'snooze', { at: later }, alice));
      const run = await inTransaction(host => runDeadline(host, 'Alarm', { key: 'a3', useId: 'Clock', name: 'ring', at: message!.at, payload: { n: 1 } }, worker, { claim: held }));
      expect(run).toEqual({ ran: false, why: 'claim lost' });
      expect(await rings('a3')).toEqual([{ key: 'ring', values: { dueAt: later, fired: false } }]);
      expect(await messages('a3')).toEqual([{ id: expect.any(String), name: 'ring', at: later, state: 'ready', dueAt: later }]);
    });

    test('a deadline the fired command arms again survives the completion of the message the worker claimed', async () => {
      await inTransaction(host => runCreate(host, 'Alarm', { title: 'Kettle' }, alice, { id: 'a4' }));
      const [message] = await messages('a4');
      const held = await claim('a4');
      const run = await inTransaction(host => runDeadline(host, 'Alarm', { key: 'a4', useId: 'Clock', name: 'ring', at: message!.at, payload: { again: true } }, worker, { claim: held }));
      expect(run.ran).toBe(true);
      const again = message!.at + 1000;
      expect(await messages('a4')).toEqual([{ id: expect.not.stringMatching(new RegExp(`^${held.id}$`)), name: 'ring', at: again, state: 'ready', dueAt: again }]);
      expect(await armed('a4')).toEqual([{ useId: 'Clock', name: 'ring', at: again, payload: { again: true } }]);
    });

    test('a claim another worker took over after its lease is lost: its token spends nothing', async () => {
      await inTransaction(host => runCreate(host, 'Alarm', { title: 'Oven' }, alice, { id: 'a5' }));
      const [message] = await messages('a5');
      const first = await claim('a5');
      const second = crypto.randomUUID();
      await db.exec(`UPDATE alarm_deadline_queue SET claim_token = ${p(1)} WHERE id = ${p(2)}`, [second, first.id]);
      const due = { key: 'a5', useId: 'Clock', name: 'ring', at: message!.at, payload: { n: 1 } };
      expect(await inTransaction(host => runDeadline(host, 'Alarm', due, worker, { claim: first }))).toEqual({ ran: false, why: 'claim lost' });
      expect(await rings('a5')).toEqual([{ key: 'ring', values: { dueAt: message!.at, fired: false } }]);
      expect(await inTransaction(host => runDeadline(host, 'Alarm', due, worker, { claim: { id: first.id, token: second } }))).toMatchObject({ ran: true });
      expect(await messages('a5')).toEqual([]);
    });

    test('a row moved under a claimed deadline runs it again from fresh state while the claim holds', async () => {
      await inTransaction(host => runCreate(host, 'Alarm', { title: 'Rice' }, alice, { id: 'a6' }));
      const [message] = await messages('a6');
      const held = await claim('a6');
      let applies = 0;
      const moving = (store: RulesStore): RulesStore => ({
        ...store,
        async apply(statements) {
          if (applies++ === 0) await db.concurrently(`UPDATE alarm SET title = title || '!', _version = _version + 1 WHERE id = ${p(1)}`, ['a6']);
          return store.apply(statements);
        },
      });
      const run = await inTransaction(host => runDeadline(host, 'Alarm', { key: 'a6', useId: 'Clock', name: 'ring', at: message!.at, payload: null }, worker, { claim: held }), moving);
      expect(run).toMatchObject({ ran: true, result: { attempts: 2 } });
      expect(await messages('a6')).toEqual([]);
    });

    test('a deadline run without a claim is refused unless it is armed at its time and due, and spends the arming it ran', async () => {
      await inTransaction(host => runCreate(host, 'Alarm', { title: 'Eggs' }, alice, { id: 'a7' }));
      const [message] = await messages('a7');
      const at = message!.at;
      const deadline = (when: number, armedAt: number): CoreCommand => ({ caller: worker, now: when, action: { kind: 'deadline', key: 'a7', useId: 'Clock', name: 'ring', at: armedAt, payload: { n: 1 } } });
      await expect(inTransaction(host => execute(host, 'Alarm', deadline(at + 5, at + 1)))).rejects.toMatchObject({
        refusal: { code: 'not_found', status: 404, class: 'EngineError', message: `Alarm a7 has no deadline Clock.ring armed at ${at + 1}` },
      });
      await expect(inTransaction(host => execute(host, 'Alarm', deadline(at - 1, at)))).rejects.toMatchObject({
        refusal: { code: 'invalid_argument', status: 400, class: 'EngineError', message: `deadline Clock.ring of Alarm a7 falls due at ${at}, after ${at - 1}` },
      });
      expect(await inTransaction(host => execute(host, 'Alarm', deadline(at, at)))).toMatchObject({ key: 'a7', attempts: 1 });
      expect(await messages('a7')).toEqual([]);
      await expect(inTransaction(host => execute(host, 'Alarm', deadline(at, at)))).rejects.toMatchObject({ refusal: { code: 'not_found' } });
    });

    test('a deadline runs at its time when the clock is behind it, so a worker whose clock lags the database never spends it as not due', async () => {
      await inTransaction(host => runCreate(host, 'Alarm', { title: 'Soup' }, alice, { id: 'a8' }));
      const [message] = await messages('a8');
      const behind = (store: RulesStore): RulesHost => ({ core: clock, layout, store, now: () => message!.at - 500 });
      const run = await db.transaction(store => runDeadline(behind(store), 'Alarm', { key: 'a8', useId: 'Clock', name: 'ring', at: message!.at, payload: null }, worker));
      expect(run.ran).toBe(true);
      expect(await messages('a8')).toEqual([]);
    });

    test("deleting the row deletes its deadlines' messages and its sub-table rows", async () => {
      await inTransaction(host => runCreate(host, 'Alarm', { title: 'Milk' }, alice, { id: 'a9' }));
      await inTransaction(host => runDelete(host, 'Alarm', 'a9', alice));
      expect(await messages('a9')).toEqual([]);
      expect(await db.rows(`SELECT name FROM alarm_clock_rings WHERE alarm_id = ${p(1)}`, ['a9'])).toEqual([]);
    });

    test("a row a raw SQL delete took leaves its message, which a worker's run spends as gone", async () => {
      await inTransaction(host => runCreate(host, 'Alarm', { title: 'Jam' }, alice, { id: 'a10' }));
      const [message] = await messages('a10');
      const held = await claim('a10');
      await db.exec(`DELETE FROM alarm_clock_rings WHERE alarm_id = ${p(1)}`, ['a10']);
      await db.exec(`DELETE FROM alarm WHERE id = ${p(1)}`, ['a10']);
      const run = await inTransaction(host => runDeadline(host, 'Alarm', { key: 'a10', useId: 'Clock', name: 'ring', at: message!.at, payload: null }, worker, { claim: held }));
      expect(run).toEqual({ ran: false, why: 'gone', refusal: { code: 'not_found', status: 404, class: 'EngineError', message: 'Alarm a10 does not exist' } });
      expect(await messages('a10')).toEqual([]);
    });

    test('a vetoed fire is released, due again in a minute, counting no attempt', async () => {
      await inTransaction(host => runCreate(host, 'Alarm', { title: 'Tide' }, alice, { id: 'a12' }));
      const [message] = await messages('a12');
      const ringBefore = await rings('a12');
      const [before] = await db.rows(`SELECT attempts FROM alarm_deadline_queue WHERE id = ${p(1)}`, [message!.id]);
      const claimed = await claim('a12');
      const due = { key: 'a12', useId: 'Clock', name: 'ring', at: message!.at, payload: { n: 1 } };
      const run = await inTransaction(host => runDeadline(host, 'Alarm', due, worker, { claim: claimed }), undefined, holding);
      expect(run).toEqual({ ran: false, why: 'vetoed', refusal: held });
      // Ready again, its attempt not counted, no claim on it, and due a
      // minute on by the database's clock.
      const seconds = db.dialect === 'postgres' ? 'EXTRACT(EPOCH FROM (due_at - now()))' : "(julianday(due_at) - julianday('now')) * 86400";
      const [after] = await db.rows(`SELECT state, attempts, claim_token, claim_expires_at, ${seconds} AS wait FROM alarm_deadline_queue WHERE id = ${p(1)}`, [claimed.id]);
      expect(after).toMatchObject({ state: 'ready', claim_token: null, claim_expires_at: null });
      expect(Number(after!.attempts)).toBe(Number(before!.attempts));
      expect(Number(after!.wait)).toBeGreaterThanOrEqual(VETOED_RETRY_SECONDS - 1);
      expect(Number(after!.wait)).toBeLessThanOrEqual(VETOED_RETRY_SECONDS + 1);
      // The deadline stays armed, its sub-table row as it was.
      expect(await rings('a12')).toEqual(ringBefore);
      expect(await armed('a12')).toEqual([{ useId: 'Clock', name: 'ring', at: message!.at, payload: { n: 1 } }]);
      // The claim released, a second release of it finds no message it holds.
      expect(await inTransaction(host => runDeadline(host, 'Alarm', due, worker, { claim: claimed }), undefined, holding)).toEqual({ ran: false, why: 'claim lost' });
      // Without a claim, a veto is the refusal it was.
      await expect(inTransaction(host => execute(host, 'Alarm', { caller: worker, now: message!.at, action: { kind: 'deadline', ...due } }), undefined, holding)).rejects.toMatchObject({ refusal: held });
    });

    test('a sub-table row write that changes no row moves the unit, which a raw SQL write alone can make, until the command is busy', async () => {
      await inTransaction(host => runCreate(host, 'Alarm', { title: 'Salt' }, alice, { id: 'a11' }));
      await db.exec(`DELETE FROM alarm_clock_rings WHERE alarm_id = ${p(1)}`, ['a11']);
      let thrown: unknown;
      try {
        await inTransaction(host => runOperation(host, 'Alarm', 'a11', 'Clock', 'snooze', { at: now + 10_000 }, alice));
      } catch (error) {
        thrown = error;
      }
      expect(thrown).toBeInstanceOf(RulesRefusal);
      expect((thrown as RulesRefusal).refusal).toMatchObject({ code: 'busy', message: expect.stringContaining(`it ran ${MAX_ATTEMPTS} times and nothing was written`) });
      const [row] = await db.rows(`SELECT _version FROM alarm WHERE id = ${p(1)}`, ['a11']);
      expect(Number(row!._version)).toBe(1);
    });
  });
}
