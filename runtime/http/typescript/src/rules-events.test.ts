import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import {
  MAX_ATTEMPTS,
  RulesRefusal,
  readQueuedEvents,
  runCreate,
  runDelete,
  runOperation,
  spendEvent,
  type CoreAnswers,
  type CoreCaller,
  type CoreChangeSet,
  type CoreCommand,
  type CoreOutboxEvent,
  type CoreStep,
  type CoreTypeSpec,
  type DeadlineClaim,
  type RulesCore,
  type RulesHost,
  type RulesLayout,
  type RulesStore,
} from './rules';
import { bumpVersion, columnTypes, postgresDatabase, rulesDsn, sqliteDatabase, type TestDatabase } from './rules.testing';
import { uuidFromDb, uuidToDb } from './uuid';

// A behavior's own events (D104) over a real database of each dialect,
// bun:sqlite and Postgres, with a core this test writes, so the adapter's
// part is held apart from any behavior's rules: Bell, whose key is a UUID,
// keeps a use Chime whose commands emit events, which the adapter writes
// to the table's event queue (bell_event_queue) in the command's unit, as
// sqlgen writes the queue; Mute keeps no event queue, so an event of one
// of its rows refuses the command. A claim is made with SQL, as a worker's
// claim statement makes one. The Postgres cases are skipped without
// SUPERSCHEMATIC_RULES_TEST_DATABASE_URL.

const layout: RulesLayout = {
  types: {
    Bell: {
      type: 'Bell',
      table: 'bell',
      key: { field: 'id', column: 'id', type: 'Identity.UUID' },
      version: '_version',
      own: [{ field: 'title', column: 'title', type: 'string' }],
      uses: [
        {
          id: 'Chime',
          behavior: 'Chime',
          member: 'chime',
          fields: [],
          operations: [
            { name: 'ring', writes: true },
            { name: 'refuse', writes: true },
            { name: 'muffle', writes: true },
          ],
        },
      ],
      events: { queue: 'BellEvent', table: 'bell_event_queue', rowKey: 'row_key', use: 'use_id', kind: 'kind', at: 'at', payload: 'payload' },
      spec: { name: 'Bell', uses: [{ behavior: 'Chime', anchor: 'type' }] },
    },
    Mute: {
      type: 'Mute',
      table: 'mute',
      key: { field: 'id', column: 'id', type: 'string' },
      version: '_version',
      own: [],
      uses: [],
      spec: { name: 'Mute', uses: [] },
    },
  },
};

const changed = (changes: Partial<CoreChangeSet>): CoreStep => ({ done: { changed: { rows: [], subRows: [], events: [], deadlines: [], result: null, ...changes } } });

/**
 * Chime, as a core: a create inserts the row and emits hung, its payload
 * the row's title; ring writes the row and emits rang once for each of
 * params.times, the nth with { n }, and a last one with a null payload when
 * params.quiet says so; refuse is vetoed; muffle emits an event of the Mute
 * row params.mute names, whose table keeps no event queue; and a delete
 * deletes the row and emits gone. Every command but a create writes the
 * row, fenced on the version it read.
 */
const chime: RulesCore = {
  run(_spec: CoreTypeSpec, command: CoreCommand, answers: CoreAnswers = {}): CoreStep {
    const action = command.action;
    const event = (key: string, kind: string, payload: unknown): CoreOutboxEvent => ({ row: { type: 'Bell', key }, kind, useId: 'Chime', payload });
    if (action.kind === 'create') {
      const key = action.id!;
      return changed({ rows: [{ kind: 'create', type: 'Bell', key, own: action.own ?? {}, owned: {} }], events: [event(key, 'hung', { title: action.own?.title ?? null })] });
    }
    if (!('key' in action)) throw new Error(`chime runs no ${action.kind}`);
    const key = action.key;
    const read = (answers.reads ?? []).find(answered => answered.request.kind === 'row');
    if (read === undefined) return { read: [{ kind: 'row', type: 'Bell', key }] };
    const row = 'row' in read.answer ? read.answer.row : null;
    if (row === null) return { done: { refused: { code: 'not_found', status: 404, class: 'EngineError', message: `Bell ${key} does not exist` } } };
    const update = { kind: 'update' as const, type: 'Bell', key, expectedVersion: row.version, own: {}, owned: {} };
    if (action.kind === 'delete') return changed({ rows: [{ kind: 'delete', type: 'Bell', key, expectedVersion: row.version }], events: [event(key, 'gone', null)] });
    if (action.kind !== 'operation') throw new Error(`chime runs no ${action.kind}`);
    const params = (action.params ?? {}) as { times?: number; quiet?: boolean; mute?: string };
    switch (action.operation) {
      case 'ring': {
        const events = Array.from({ length: params.times ?? 1 }, (_, i) => event(key, 'rang', { n: i + 1 }));
        if (params.quiet) events.push(event(key, 'rang', null));
        return changed({ rows: [update], events, result: { rang: events.length } });
      }
      case 'refuse':
        return { done: { refused: { code: 'vetoed', status: 409, class: 'BehaviorVetoError', message: `behavior Chime vetoes refuse of Bell ${key}: muted` } } };
      case 'muffle':
        return changed({ rows: [update], events: [{ row: { type: 'Mute', key: params.mute! }, kind: 'muffled', useId: 'Chime', payload: {} }] });
      default:
        throw new Error(`chime has no operation ${action.operation}`);
    }
  },
};

const alice: CoreCaller = { subject: 'alice', permissions: [] };

const dialects: { name: string; skip: boolean; open: () => Promise<TestDatabase> }[] = [
  { name: 'bun:sqlite', skip: false, open: async () => sqliteDatabase() },
  { name: 'Postgres', skip: rulesDsn === '', open: postgresDatabase },
];

/** A UUID key in its wire form, base62, from a fresh UUID. */
const freshKey = () => uuidFromDb(crypto.randomUUID());

for (const dialect of dialects) {
  describe.skipIf(dialect.skip)(`a behavior's own events on ${dialect.name}`, () => {
    let db: TestDatabase;
    let now = 1_700_000_000_000;
    const p = (n: number) => (db.dialect === 'postgres' ? `$${n}` : `?${n}`);

    beforeAll(async () => {
      db = await dialect.open();
      const t = columnTypes(db.dialect);
      const pg = db.dialect === 'postgres';
      const stamp = pg ? 'TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP' : "TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))";
      await db.exec(`CREATE TABLE bell (id ${pg ? 'UUID PRIMARY KEY' : 'TEXT PRIMARY KEY'}, title ${t.text}, _version ${t.version})`);
      if (pg) for (const ddl of bumpVersion('bell')) await db.exec(ddl);
      await db.exec(`CREATE TABLE mute (id ${t.key}, _version ${t.version})`);
      await db.exec(`CREATE TABLE bell_event_queue (
  id ${pg ? 'UUID DEFAULT gen_random_uuid()' : "TEXT DEFAULT (lower(hex(randomblob(16))))"} NOT NULL PRIMARY KEY,
  row_key ${pg ? 'UUID' : 'TEXT'} NOT NULL,
  use_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  at ${t.integer} NOT NULL,
  payload ${t.json},
  state TEXT DEFAULT 'ready' NOT NULL,
  attempts INTEGER DEFAULT 0 NOT NULL,
  due_at ${stamp} NOT NULL,
  claim_expires_at ${t.timestamp},
  claim_token ${pg ? 'UUID' : 'TEXT'},
  last_error TEXT,
  created_at ${stamp} NOT NULL,
  updated_at ${stamp} NOT NULL
)`);
    });
    afterAll(async () => db?.close());

    /** Runs fn on a host over a transaction of the database, at the next tick of the test's clock. */
    const inTransaction = <T>(fn: (host: RulesHost) => Promise<T>, wrap: (store: RulesStore) => RulesStore = s => s): Promise<T> => {
      const at = (now += 1000);
      return db.transaction(store => fn({ core: chime, layout, store: wrap(store), now: () => at }));
    };

    /** The queued events of every Bell row, as the adapter reads them back. */
    const queued = () => inTransaction(host => readQueuedEvents(host, 'Bell'));

    /** The queued events of one Bell row. */
    const queuedOf = async (key: string) => (await queued()).filter(event => event.key === key);

    /** Claims the first ready message of a row, as a worker's claim statement does: claimed, under a new token. */
    const claim = async (key: string): Promise<DeadlineClaim> => {
      const [row] = await db.rows(`SELECT id FROM bell_event_queue WHERE row_key = ${p(1)} AND state = 'ready' ORDER BY created_at, id LIMIT 1`, [rowKey(key)]);
      const token = crypto.randomUUID();
      expect(await db.exec(`UPDATE bell_event_queue SET state = 'claimed', claim_token = ${p(1)}, attempts = attempts + 1 WHERE id = ${p(2)}`, [token, row!.id])).toBe(1);
      return { id: String(row!.id), token };
    };

    /** A Bell key as its column holds it: the UUID's hyphenated text. */
    const rowKey = (key: string) => uuidToDb(key);

    test("an event is written to its row's table's event queue in the command's unit, with the command's time and its payload, and stays when the row is deleted", async () => {
      const key = freshKey();
      await inTransaction(host => runCreate(host, 'Bell', { title: 'Matins' }, alice, { id: key }));
      const created = now;
      expect(await queuedOf(key)).toEqual([{ key, useId: 'Chime', kind: 'hung', at: created, payload: { title: 'Matins' } }]);
      // The message's key column holds the UUID's text, as the row's does,
      // and a new message is ready and due.
      const [message] = await db.rows(`SELECT row_key, state, attempts FROM bell_event_queue WHERE kind = 'hung' AND row_key = ${p(1)}`, [rowKey(key)]);
      expect({ rowKey: String(message!.row_key), state: message!.state, attempts: Number(message!.attempts) }).toEqual({ rowKey: rowKey(key), state: 'ready', attempts: 0 });
      await inTransaction(host => runOperation(host, 'Bell', key, 'Chime', 'ring', { times: 1, quiet: true }, alice));
      const rang = now;
      await inTransaction(host => runDelete(host, 'Bell', key, alice));
      const deleted = now;
      expect(await db.rows(`SELECT id FROM bell WHERE id = ${p(1)}`, [rowKey(key)])).toEqual([]);
      expect(await queuedOf(key)).toEqual([
        { key, useId: 'Chime', kind: 'hung', at: created, payload: { title: 'Matins' } },
        { key, useId: 'Chime', kind: 'rang', at: rang, payload: { n: 1 } },
        { key, useId: 'Chime', kind: 'rang', at: rang, payload: null },
        { key, useId: 'Chime', kind: 'gone', at: deleted, payload: null },
      ]);
    });

    test('a refused or moved command writes no event', async () => {
      const key = freshKey();
      await inTransaction(host => runCreate(host, 'Bell', { title: 'Lauds' }, alice, { id: key }));
      const before = await queuedOf(key);
      await expect(inTransaction(host => runOperation(host, 'Bell', key, 'Chime', 'refuse', {}, alice))).rejects.toMatchObject({ refusal: { code: 'vetoed' } });
      expect(await queuedOf(key)).toEqual(before);
      // A row another moves under the command runs it again: the attempt
      // that did not apply writes nothing, the one that did its events once.
      const times = 2;
      let applies = 0;
      const movingOnce = (store: RulesStore): RulesStore => ({
        ...store,
        async apply(statements) {
          if (applies++ === 0) await db.concurrently(`UPDATE bell SET _version = _version + 1 WHERE id = ${p(1)}`, [rowKey(key)]);
          return store.apply(statements);
        },
      });
      await inTransaction(host => runOperation(host, 'Bell', key, 'Chime', 'ring', { times }, alice), movingOnce);
      expect(applies).toBe(2);
      const rang = now;
      expect(await queuedOf(key)).toEqual([...before, ...Array.from({ length: times }, (_, i) => ({ key, useId: 'Chime', kind: 'rang', at: rang, payload: { n: i + 1 } }))]);
      // One whose row keeps moving is busy, and writes none.
      const moving = (store: RulesStore): RulesStore => ({
        ...store,
        async apply(statements) {
          await db.concurrently(`UPDATE bell SET _version = _version + 1 WHERE id = ${p(1)}`, [rowKey(key)]);
          return store.apply(statements);
        },
      });
      const after = await queuedOf(key);
      let thrown: unknown;
      try {
        await inTransaction(host => runOperation(host, 'Bell', key, 'Chime', 'ring', { times: 1 }, alice), moving);
      } catch (error) {
        thrown = error;
      }
      expect(thrown).toBeInstanceOf(RulesRefusal);
      expect((thrown as RulesRefusal).refusal).toMatchObject({ code: 'busy', message: expect.stringContaining(`it ran ${MAX_ATTEMPTS} times and nothing was written`) });
      expect(await queuedOf(key)).toEqual(after);
    });

    test('a change set with an event on a table with no event queue is refused as internal, naming the table and the use', async () => {
      const key = freshKey();
      await inTransaction(host => runCreate(host, 'Bell', { title: 'Vespers' }, alice, { id: key }));
      const [row] = await db.rows(`SELECT _version FROM bell WHERE id = ${p(1)}`, [rowKey(key)]);
      const before = await queued();
      let thrown: unknown;
      try {
        await inTransaction(host => runOperation(host, 'Bell', key, 'Chime', 'muffle', { mute: 'm1' }, alice));
      } catch (error) {
        thrown = error;
      }
      expect(thrown).toBeInstanceOf(RulesRefusal);
      expect((thrown as RulesRefusal).refusal).toEqual({
        code: 'internal',
        status: 500,
        class: 'Error',
        message: "rules: muffle of Bell emits muffled of Chime on Mute m1, and Mute's table mute keeps no event queue in the behaviors' layout: a behavior whose declaration lists its events gives its table one (D104)",
      });
      // Nothing of the unit is applied: the row's version is where it was,
      // and no message is queued.
      const [after] = await db.rows(`SELECT _version FROM bell WHERE id = ${p(1)}`, [rowKey(key)]);
      expect(Number(after!._version)).toBe(Number(row!._version));
      expect(await queued()).toEqual(before);
      // A spend of a type whose table keeps no event queue is refused too.
      await expect(inTransaction(host => spendEvent(host, 'Mute', { id: crypto.randomUUID(), token: crypto.randomUUID() }))).rejects.toMatchObject({
        refusal: { code: 'internal', message: "rules: Mute keeps no event queue in the behaviors' layout, and a worker's claim spends an event of it" },
      });
      expect(await inTransaction(host => readQueuedEvents(host, 'Mute'))).toEqual([]);
    });

    test('spendEvent spends a claimed message and a lost claim spends nothing', async () => {
      const key = freshKey();
      await inTransaction(host => runCreate(host, 'Bell', { title: 'Compline' }, alice, { id: key }));
      const held = await claim(key);
      // Another worker took the message over after this claim's lease: the
      // claim is lost, and the message stays.
      const other = crypto.randomUUID();
      await db.exec(`UPDATE bell_event_queue SET claim_token = ${p(1)} WHERE id = ${p(2)}`, [other, held.id]);
      expect(await inTransaction(host => spendEvent(host, 'Bell', held))).toEqual({ ran: false, why: 'claim lost' });
      expect(await queuedOf(key)).toHaveLength(1);
      // The claim that holds it spends it, and a second spend finds nothing.
      expect(await inTransaction(host => spendEvent(host, 'Bell', { id: held.id, token: other }))).toEqual({ ran: true });
      expect(await queuedOf(key)).toEqual([]);
      expect(await inTransaction(host => spendEvent(host, 'Bell', { id: held.id, token: other }))).toEqual({ ran: false, why: 'claim lost' });
      // Without a claim nothing is spent, and it runs.
      await inTransaction(host => runOperation(host, 'Bell', key, 'Chime', 'ring', { times: 1 }, alice));
      expect(await inTransaction(host => spendEvent(host, 'Bell'))).toEqual({ ran: true });
      expect(await queuedOf(key)).toHaveLength(1);
      // A spend that the transaction rolls back spends nothing.
      const again = await claim(key);
      await expect(
        db.transaction(async store => {
          expect(await spendEvent({ core: chime, layout, store }, 'Bell', again)).toEqual({ ran: true });
          throw new Error('the handler failed');
        })
      ).rejects.toThrow('the handler failed');
      if (db.dialect === 'postgres') expect(await queuedOf(key)).toHaveLength(1);
    });

    test("readQueuedEvents reads one row's events in the order written", async () => {
      const key = freshKey();
      const other = freshKey();
      await inTransaction(host => runCreate(host, 'Bell', { title: 'Prime' }, alice, { id: key }));
      await inTransaction(host => runCreate(host, 'Bell', { title: 'Terce' }, alice, { id: other }));
      const kinds: string[] = ['hung'];
      // One command's events in its change set's order, and each command's
      // after the one before, another row's between them: on Postgres each
      // insert's clock_timestamp() orders them, and on SQLite, whose inserts
      // share a millisecond, their order.
      const times = [3, 1, 2];
      for (const n of times) {
        await inTransaction(host => runOperation(host, 'Bell', key, 'Chime', 'ring', { times: n }, alice));
        await inTransaction(host => runOperation(host, 'Bell', other, 'Chime', 'ring', { times: 1 }, alice));
        for (let i = 1; i <= n; i++) kinds.push(`rang ${i}`);
      }
      const events = await queuedOf(key);
      expect(events.map(event => (event.kind === 'rang' ? `rang ${(event.payload as { n: number }).n}` : event.kind))).toEqual(kinds);
      expect(events.every(event => event.key === key && event.useId === 'Chime')).toBe(true);
      expect((await queuedOf(other)).map(event => event.kind)).toEqual(['hung', ...times.map(() => 'rang')]);
    });
  });
}
