import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import {
  RulesRefusal,
  execute,
  readArmedDeadlines,
  readHolding,
  readRow,
  readSubRows,
  runCreate,
  runDelete,
  runOperation,
  runUpdate,
  type CoreAnswers,
  type CoreCaller,
  type CoreChangeSet,
  type CoreCommand,
  type CoreStep,
  type CoreTypeSpec,
  type RulesCore,
  type RulesHost,
  type RulesLayout,
  type RulesStore,
} from './rules';
import { bumpVersion, columnTypes, postgresDatabase, rulesDsn, sqliteDatabase, type TestDatabase } from './rules.testing';

// What a table that keeps a fencing token asks of the adapter (D104), over
// a real database of each dialect, bun:sqlite and Postgres, with a core
// this test writes, so the adapter's part is held apart from any
// behavior's rules: a command's preconditions reach the core as the
// command and ExecuteOptions give them, each use's entry by its use id; a
// read of the rows whose owned field holds a value (holding) is answered
// by the layout's column of that field, in key order, a page at a time;
// owned and sub-table columns of a BIGINT (Generic.Int64), a number, a
// boolean and JSON read back as the core's JSON, a BIGINT pg hands over as
// its text among them; and a change set that clears a deadline and arms it
// again applies both in order. Job's use Fence owns holder, token, ended
// and score, keeps a log of its takes (job_fence_log) and arms expire as a
// message of the table's deadline queue, whose tables are written by hand
// as sqlgen writes them. The Postgres cases are skipped without
// SUPERSCHEMATIC_RULES_TEST_DATABASE_URL.

const layout: RulesLayout = {
  types: {
    Job: {
      type: 'Job',
      table: 'job',
      key: { field: 'id', column: 'id', type: 'string' },
      version: '_version',
      own: [{ field: 'title', column: 'title', type: 'string' }],
      uses: [
        {
          id: 'Fence',
          behavior: 'Fence',
          member: 'fence',
          fields: [
            { name: 'holder', field: 'fenceHolder', column: 'fence_holder', type: 'string' },
            { name: 'token', field: 'fenceToken', column: 'fence_token', type: 'Generic.Int64' },
            { name: 'ended', field: 'fenceEnded', column: 'fence_ended', type: 'Generic.JSON' },
            { name: 'score', field: 'fenceScore', column: 'fence_score', type: 'number' },
          ],
          operations: [
            { name: 'take', writes: true },
            { name: 'held', writes: false },
          ],
          tables: [
            {
              name: 'log',
              table: 'job_fence_log',
              row: 'job_id',
              key: { name: 'number', field: 'number', column: 'number', type: 'string' },
              fields: [
                { name: 'token', field: 'token', column: 'token', type: 'Generic.Int64' },
                { name: 'kept', field: 'kept', column: 'kept', type: 'boolean' },
                { name: 'detail', field: 'detail', column: 'detail', type: 'Generic.JSON' },
                { name: 'score', field: 'score', column: 'score', type: 'number' },
              ],
            },
          ],
        },
      ],
      deadlines: { queue: 'JobDeadline', table: 'job_deadline_queue', rowKey: 'row_key', use: 'use_id', name: 'name', at: 'at', payload: 'payload' },
      spec: { name: 'Job', uses: [{ behavior: 'Fence', anchor: 'type' }] },
    },
  },
};

const changed = (changes: Partial<CoreChangeSet>): CoreStep => ({ done: { changed: { rows: [], subRows: [], events: [], deadlines: [], result: null, ...changes } } });

/** Every command the core was run with, in order: what the adapter handed it. */
const seen: CoreCommand[] = [];

/**
 * Fence, as a core. A create gives the row token 0. take holds the row's
 * token to the command's precondition { Fence: { token } } (refused
 * token_stale for another, as Lease's guard refuses one), then gives the
 * row to the caller at the next token, ended { reason: 'take', at }, a
 * score of 2.5, logs the take under the number of the token, and clears
 * expire and arms it again a second on, in one change set. held answers the
 * page of rows whose holder is the caller (a holding read), limit and after
 * from its params. An update and a delete are held to the precondition too.
 */
const fence: RulesCore = {
  run(_spec: CoreTypeSpec, command: CoreCommand, answers: CoreAnswers = {}): CoreStep {
    seen.push(command);
    const action = command.action;
    const ref = (key: string) => ({ type: 'Job', key });
    if (action.kind === 'create') {
      return changed({ rows: [{ kind: 'create', type: 'Job', key: action.id!, own: action.own ?? {}, owned: { Fence: { token: 0 } } }] });
    }
    if (!('key' in action)) throw new Error(`fence runs no ${action.kind}`);
    const key = action.key;
    if (action.kind === 'operation' && action.operation === 'held') {
      const { limit, after } = action.params as { limit: number; after?: string };
      const read = (answers.reads ?? []).find(answered => answered.request.kind === 'holding');
      if (read === undefined) return { read: [{ kind: 'holding', type: 'Job', useId: 'Fence', field: 'holder', value: command.caller.subject, limit, ...(after !== undefined ? { after } : {}) }] };
      return changed({ result: 'linked' in read.answer ? read.answer.linked : null });
    }
    const read = (answers.reads ?? []).find(answered => answered.request.kind === 'row');
    if (read === undefined) return { read: [{ kind: 'row', type: 'Job', key }] };
    const row = 'row' in read.answer ? read.answer.row : null;
    if (row === null) return { done: { refused: { code: 'not_found', status: 404, class: 'EngineError', message: `Job ${key} does not exist` } } };
    const token = (row.owned?.Fence?.token as number | undefined) ?? 0;
    const presented = (command.preconditions?.Fence as { token?: number } | undefined)?.token;
    if (presented !== token) {
      return {
        done: {
          refused: {
            code: 'vetoed',
            status: 409,
            class: 'BehaviorVetoError',
            message: `behavior Fence vetoes ${action.kind === 'operation' ? action.operation : action.kind} of Job ${key}: token ${String(presented)} is stale: the row is at token ${token}`,
            details: { behavior: 'Fence', vetoCode: 'token_stale', vetoDetails: { token: presented ?? null, current: token } },
          },
        },
      };
    }
    switch (action.kind) {
      case 'update':
        return changed({ rows: [{ kind: 'update', type: 'Job', key, expectedVersion: row.version, own: action.patch, owned: {} }] });
      case 'delete':
        return changed({ rows: [{ kind: 'delete', type: 'Job', key, expectedVersion: row.version }] });
      case 'operation': {
        const next = token + 1;
        const at = command.now + 1000;
        return changed({
          rows: [{ kind: 'update', type: 'Job', key, expectedVersion: row.version, own: {}, owned: { Fence: { holder: command.caller.subject, token: next, ended: { reason: 'take', at: command.now }, score: 2.5 } } }],
          subRows: [{ row: ref(key), useId: 'Fence', table: 'log', key: String(next).padStart(10, '0'), change: { kind: 'insert', values: { token: next, kept: next % 2 === 1, detail: { by: command.caller.subject, steps: [next] }, score: 0.5 } } }],
          deadlines: [
            { kind: 'clear', row: ref(key), useId: 'Fence', name: 'expire' },
            { kind: 'arm', row: ref(key), useId: 'Fence', name: 'expire', at, payload: null },
          ],
          result: { token: next },
        });
      }
      default:
        throw new Error(`fence runs no ${action.kind}`);
    }
  },
};

const wren: CoreCaller = { subject: 'wren', permissions: [] };
const otto: CoreCaller = { subject: 'otto', permissions: [] };

const dialects: { name: string; skip: boolean; open: () => Promise<TestDatabase> }[] = [
  { name: 'bun:sqlite', skip: false, open: async () => sqliteDatabase() },
  { name: 'Postgres', skip: rulesDsn === '', open: postgresDatabase },
];

for (const dialect of dialects) {
  describe.skipIf(dialect.skip)(`preconditions, holding reads and BIGINT columns on ${dialect.name}`, () => {
    let db: TestDatabase;
    let now = 2_000_000_000;
    const p = (n: number) => (db.dialect === 'postgres' ? `$${n}` : `?${n}`);

    beforeAll(async () => {
      db = await dialect.open();
      const t = columnTypes(db.dialect);
      const pg = db.dialect === 'postgres';
      await db.exec(`CREATE TABLE job (
  id ${t.key},
  title ${t.text},
  fence_holder ${t.text},
  fence_token ${t.integer},
  fence_ended ${t.json},
  fence_score ${t.number},
  _version ${t.version}
)`);
      if (pg) for (const ddl of bumpVersion('job')) await db.exec(ddl);
      await db.exec(`CREATE TABLE job_fence_log (
  id ${pg ? 'UUID DEFAULT gen_random_uuid()' : 'TEXT DEFAULT (lower(hex(randomblob(16))))'} NOT NULL PRIMARY KEY,
  job_id TEXT NOT NULL REFERENCES job (id) ON DELETE CASCADE,
  number TEXT NOT NULL,
  token ${t.integer},
  kept ${t.boolean},
  detail ${t.json},
  score ${t.number},
  UNIQUE (job_id, number)
)`);
      await db.exec(`CREATE TABLE job_deadline_queue (
  id ${pg ? 'UUID DEFAULT gen_random_uuid()' : 'TEXT DEFAULT (lower(hex(randomblob(16))))'} NOT NULL PRIMARY KEY,
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
      await db.exec(`CREATE UNIQUE INDEX job_deadline_queue_live ON job_deadline_queue (row_key, use_id, name) WHERE state IN ('ready', 'claimed')`);
    });
    afterAll(async () => db?.close());

    /** Runs fn on a host over a transaction of the database, at the next tick of the test's clock. */
    const inTransaction = <T>(fn: (host: RulesHost) => Promise<T>, wrap: (store: RulesStore) => RulesStore = s => s): Promise<T> => {
      const at = (now += 1000);
      return db.transaction(store => fn({ core: fence, layout, store: wrap(store), now: () => at }));
    };

    /** The preconditions of the last command the core was run with. */
    const lastPreconditions = () => seen.at(-1)?.preconditions;

    test("an operation's, an update's and a delete's preconditions reach the core as given, each use's entry by its use id", async () => {
      await inTransaction(host => runCreate(host, 'Job', { title: 'Build' }, wren, { id: 'j1' }));
      expect(lastPreconditions()).toBeUndefined();
      expect(await inTransaction(host => runOperation(host, 'Job', 'j1', 'Fence', 'take', {}, wren, { preconditions: { Fence: { token: 0 } } }))).toEqual({ token: 1 });
      expect(lastPreconditions()).toEqual({ Fence: { token: 0 } });
      await inTransaction(host => runUpdate(host, 'Job', 'j1', { title: 'Build, again' }, wren, { preconditions: { Fence: { token: 1 } } }));
      expect(lastPreconditions()).toEqual({ Fence: { token: 1 } });
      expect((await inTransaction(host => readRow(host, 'Job', 'j1')))?.own).toEqual({ title: 'Build, again' });
      // A command that carries its own, as a transcript's does, keeps them;
      // the options' entry for the same use goes in place of the command's.
      const take: CoreCommand = { caller: wren, now, action: { kind: 'operation', key: 'j1', useId: 'Fence', operation: 'take', params: {} }, preconditions: { Fence: { token: 7 }, Other: { n: 1 } } };
      expect((await inTransaction(host => execute(host, 'Job', take, { preconditions: { Fence: { token: 1 } } }))).result).toEqual({ token: 2 });
      expect(lastPreconditions()).toEqual({ Fence: { token: 1 }, Other: { n: 1 } });
      // Empty options add none.
      await expect(inTransaction(host => runDelete(host, 'Job', 'j1', wren, { preconditions: {} }))).rejects.toMatchObject({ code: 'vetoed', details: { vetoCode: 'token_stale' } });
      expect(lastPreconditions()).toBeUndefined();
    });

    test('a refusal of a precondition writes nothing, and the one the row is at goes through, a delete among them', async () => {
      const before = await db.rows(`SELECT _version FROM job WHERE id = ${p(1)}`, ['j1']);
      await expect(inTransaction(host => runUpdate(host, 'Job', 'j1', { title: 'Late' }, otto, { preconditions: { Fence: { token: 1 } } }))).rejects.toBeInstanceOf(RulesRefusal);
      expect(await db.rows(`SELECT _version FROM job WHERE id = ${p(1)}`, ['j1'])).toEqual(before);
      await inTransaction(host => runCreate(host, 'Job', { title: 'Scrap' }, wren, { id: 'j9' }));
      await inTransaction(host => runDelete(host, 'Job', 'j9', wren, { preconditions: { Fence: { token: 0 } } }));
      expect(await inTransaction(host => readRow(host, 'Job', 'j9'))).toBeNull();
    });

    test("owned columns of a BIGINT, a number and JSON read back as the core's JSON numbers and values, and a sub-table's BIGINT, boolean, JSON and number columns too", async () => {
      expect(await inTransaction(host => readRow(host, 'Job', 'j1'))).toEqual({
        own: { title: 'Build, again' },
        owned: { Fence: { holder: 'wren', token: 2, ended: { reason: 'take', at: expect.any(Number) }, score: 2.5 } },
        version: 4,
      });
      // pg hands a BIGINT over as its text to a pool that does not parse it.
      const [raw] = await db.rows(`SELECT fence_token FROM job WHERE id = ${p(1)}`, ['j1']);
      expect(raw!.fence_token).toBe(db.dialect === 'postgres' ? '2' : 2);
      expect(await inTransaction(host => readSubRows(host, 'Job', 'j1', 'Fence', 'log'))).toEqual([
        { key: '0000000001', values: { token: 1, kept: true, detail: { by: 'wren', steps: [1] }, score: 0.5 } },
        { key: '0000000002', values: { token: 2, kept: false, detail: { by: 'wren', steps: [2] }, score: 0.5 } },
      ]);
    });

    test('a change set that clears a deadline and arms it again applies both in order: one live message, at the new arming', async () => {
      const armedBefore = await inTransaction(host => readArmedDeadlines(host, 'Job', 'j1'));
      expect(armedBefore).toEqual([{ useId: 'Fence', name: 'expire', at: expect.any(Number), payload: null }]);
      await inTransaction(host => runOperation(host, 'Job', 'j1', 'Fence', 'take', {}, wren, { preconditions: { Fence: { token: 2 } } }));
      const at = now + 1000;
      expect(await inTransaction(host => readArmedDeadlines(host, 'Job', 'j1'))).toEqual([{ useId: 'Fence', name: 'expire', at, payload: null }]);
      expect(Number((await db.rows(`SELECT count(*) AS n FROM job_deadline_queue WHERE row_key = ${p(1)}`, ['j1']))[0]!.n)).toBe(1);
    });

    test('a holding read answers the rows whose owned field holds the value, in key order, a page at a time, a type the layout lacks with none', async () => {
      for (const [id, who] of [
        ['j2', otto],
        ['j3', wren],
        ['j4', otto],
      ] as const) {
        await inTransaction(host => runCreate(host, 'Job', { title: id }, who, { id }));
        await inTransaction(host => runOperation(host, 'Job', id, 'Fence', 'take', {}, who, { preconditions: { Fence: { token: 0 } } }));
      }
      const held = (caller: CoreCaller, params: { limit: number; after?: string }) => inTransaction(host => runOperation(host, 'Job', 'j1', 'Fence', 'held', params, caller)) as Promise<{ rows: { key: string; row: { owned: Record<string, Record<string, unknown>> } }[]; more: boolean }>;
      const page = await held(wren, { limit: 500 });
      expect(page.more).toBe(false);
      expect(page.rows.map(row => row.key)).toEqual(['j1', 'j3']);
      expect(page.rows[1]!.row.owned.Fence).toMatchObject({ holder: 'wren', token: 1 });
      const first = await held(otto, { limit: 1 });
      expect({ keys: first.rows.map(row => row.key), more: first.more }).toEqual({ keys: ['j2'], more: true });
      const next = await held(otto, { limit: 1, after: 'j2' });
      expect({ keys: next.rows.map(row => row.key), more: next.more }).toEqual({ keys: ['j4'], more: false });
      expect(await inTransaction(host => readHolding(host, 'Gone', 'Fence', 'holder', 'wren', undefined, 10))).toEqual({ rows: [], more: false });
      await expect(inTransaction(host => readHolding(host, 'Job', 'Fence', 'owner', 'wren', undefined, 10))).rejects.toMatchObject({ code: 'internal', message: "rules: Job's use Fence (Fence) has no field owner in the behaviors' layout" });
      await expect(inTransaction(host => readHolding(host, 'Job', 'Lock', 'holder', 'wren', undefined, 10))).rejects.toMatchObject({ code: 'internal', message: "rules: Job has no use Lock in the behaviors' layout" });
    });

    if (dialect.name === 'bun:sqlite') {
      test('a holding read under a lock is refused as internal on SQLite, which has no check of it for the batch', async () => {
        const locking: RulesCore = {
          run(spec, command, answers = {}) {
            if ((answers.locks ?? []).length === 0) return { lock: ['Job/all'] };
            return fence.run(spec, command, answers);
          },
        };
        await expect(
          db.transaction(store => runOperation({ core: locking, layout, store, now: () => now }, 'Job', 'j1', 'Fence', 'held', { limit: 10 }, wren))
        ).rejects.toMatchObject({ code: 'internal', message: 'rules: a read of the rows whose owned field holds a value under a lock has no check on SQLite or D1 yet (D104)' });
      });
    }
  });
}
