import { describe, expect, test } from 'bun:test';
import { HttpProblem, problemBody } from './problem';
import {
  MAX_ATTEMPTS,
  RulesRefusal,
  busyRefusal,
  execute,
  lazyCore,
  postgresRulesStore,
  readRow,
  runCreate,
  runOperation,
  runUpdate,
  withRulesCore,
  type CoreAnswers,
  type CoreCommand,
  type CoreStep,
  type CoreTypeSpec,
  type RulesCore,
  type RulesHost,
  type RulesLayout,
  type RulesStatement,
  type RulesStore,
} from './rules';
import { rulesCore } from './rules.testing';
import { uuidToDb } from './uuid';

// The adapter's host loop over an in-memory transaction (a store that
// records what it is asked and answers from a row it holds), with the
// core of this checkout and with cores a test writes, so no database is
// needed: rules-store.test.ts runs the same loop against bun:sqlite and
// Postgres, and rules-transcript.test.ts replays the recorded scenarios.

const flow = {
  states: ['draft', 'paid', 'shipped'],
  initial: 'draft',
  transitions: [
    { from: 'draft', to: 'paid' },
    { from: 'paid', to: 'shipped', permission: 'orders.ship' },
  ],
};

const spec: CoreTypeSpec = { name: 'Order', uses: [{ behavior: 'Workflow', config: flow, anchor: { field: 'status' } }] };

/** Order, as the generator writes its layout: status a Workflow, a UUID key. */
const layout: RulesLayout = {
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
      spec,
    },
  },
};

/** A UUID key in its two forms: base62 on the wire and in the core, hyphenated in the column. */
const key = '6fe1XoZ0UJpMNNdUOSMC2X';
const keyColumn = 'db360f43-5530-c407-56da-9cd8e0c971dd';

/** A key no row of a memory store holds. */
const absent = '1fe1XoZ0UJpMNNdUOSMC2X';

interface Recorded {
  readonly queries: { text: string; values: readonly unknown[] }[];
  readonly applied: (readonly RulesStatement[])[];
  readonly locks: string[];
}

/**
 * A transaction in memory: a query of a row answers the row it holds (or
 * none), an existence probe answers whether it holds one, and apply
 * answers what applies says for each attempt, true unless it says
 * otherwise.
 */
function memoryStore(dialect: 'postgres' | 'sqlite', row: Record<string, unknown> | undefined, applies: boolean[] = []): { store: RulesStore; recorded: Recorded } {
  const recorded: Recorded = { queries: [], applied: [], locks: [] };
  const store: RulesStore = {
    dialect,
    async query(text, values) {
      recorded.queries.push({ text, values });
      if (text.includes('LIMIT 1') || text.startsWith('SELECT 1 AS one')) return row ? [{ one: 1 }] : [];
      return row ? [row] : [];
    },
    async apply(statements) {
      recorded.applied.push(statements);
      return applies[recorded.applied.length - 1] ?? true;
    },
    async lock(lockKey) {
      recorded.locks.push(lockKey);
    },
  };
  return { store, recorded };
}

/** Order's row as Postgres reads it: the selected columns under their aliases, a TIMESTAMPTZ a Date, a BIGINT a string. */
const draftRow = { c0: 'Desk', c1: 'draft', c2: new Date(1000), v: '1' };

const alice = { subject: 'alice', permissions: [] };

const transition = (to: string): CoreCommand => ({
  caller: alice,
  now: 2000,
  request: 'r2',
  action: { kind: 'operation', key, useId: 'status', operation: 'transition', params: { to } },
});

describe.skipIf(rulesCore === undefined)('the host loop on the core', () => {
  const host = (store: RulesStore): RulesHost => ({ core: rulesCore!, layout, store, now: () => 5000, request: () => 'req' });

  test('a create inserts the key, the own fields and the owned columns Workflow initializes, fenced to one row', async () => {
    const { store, recorded } = memoryStore('postgres', undefined);
    const created = await runCreate(host(store), 'Order', { title: 'Desk' }, alice, { id: key });
    expect(created).toBe(key);
    expect(recorded.queries).toEqual([{ text: 'SELECT 1 AS one FROM "order" WHERE "id" = $1', values: [keyColumn] }]);
    expect(recorded.applied).toEqual([
      [
        {
          text: 'INSERT INTO "order" ("id", "title", "status", "status_entered_at") VALUES ($1, $2, $3, $4)',
          values: [keyColumn, 'Desk', 'draft', new Date(5000)],
          expect: 1,
        },
      ],
    ]);
  });

  test('a create with no id gets a UUID key in its wire form, and its own columns as the caller encoded them', async () => {
    const { store, recorded } = memoryStore('sqlite', undefined);
    const created = await runCreate(host(store), 'Order', { title: 'Desk' }, alice, { ownColumns: { title: 'Desk', created_by: 'u1' } });
    expect(created).toMatch(/^[0-9A-Za-z]{1,22}$/);
    const insert = recorded.applied[0]![0]!;
    expect(insert.text).toBe('INSERT INTO "order" ("id", "title", "created_by", "status", "status_entered_at") VALUES (?1, ?2, ?3, ?4, ?5)');
    expect(insert.values.slice(1)).toEqual(['Desk', 'u1', 'draft', new Date(5000).toISOString()]);
  });

  test('an operation reads its row by its key, converting each column to the core, and writes it held to its version', async () => {
    const { store, recorded } = memoryStore('postgres', draftRow);
    const h = host(store);
    expect(await readRow(h, 'Order', key)).toEqual({ own: { title: 'Desk' }, owned: { status: { status: 'draft', enteredAt: 1000 } }, version: 1 });
    recorded.queries.length = 0;
    const result = await execute(h, 'Order', transition('paid'), { ownColumns: { updated_at: new Date(7) } });
    expect(result).toEqual({
      changes: {
        rows: [{ kind: 'update', type: 'Order', key, expectedVersion: 1, own: {}, owned: { status: { status: 'paid', enteredAt: 2000 } } }],
        subRows: [],
        events: [],
        deadlines: [],
        result: { from: 'draft', to: 'paid' },
      },
      result: { from: 'draft', to: 'paid' },
      key,
      attempts: 1,
    });
    expect(recorded.queries).toEqual([
      { text: 'SELECT "title" AS c0, "status" AS c1, "status_entered_at" AS c2, "_version" AS v FROM "order" WHERE "id" = $1', values: [keyColumn] },
    ]);
    expect(recorded.applied).toEqual([
      [
        {
          text: 'UPDATE "order" SET "updated_at" = $1, "status" = $2, "status_entered_at" = $3, "_version" = "_version" + 1 WHERE "id" = $4 AND "_version" = $5',
          values: [new Date(7), 'paid', new Date(2000), keyColumn, 1],
          expect: 1,
        },
      ],
    ]);
  });

  test('a row that moved runs the command again from fresh state, and one that keeps moving is refused as busy', async () => {
    const once = memoryStore('postgres', draftRow, [false]);
    const result = await execute(host(once.store), 'Order', transition('paid'));
    expect(result.attempts).toBe(2);
    // Each attempt read the row afresh.
    expect(once.recorded.queries).toHaveLength(2);

    const always = memoryStore('postgres', draftRow, Array(MAX_ATTEMPTS).fill(false));
    let thrown: unknown;
    try {
      await execute(host(always.store), 'Order', transition('paid'));
    } catch (error) {
      thrown = error;
    }
    expect(thrown).toBeInstanceOf(RulesRefusal);
    const refusal = (thrown as RulesRefusal).refusal;
    expect(refusal).toEqual(busyRefusal('Order', { ...transition('paid') }, MAX_ATTEMPTS));
    expect(refusal.message).toBe(
      `transition of Order ${key}: the row kept moving while the command ran; it ran 8 times and nothing was written, so it may be tried again`
    );
    expect(always.recorded.applied).toHaveLength(MAX_ATTEMPTS);
  });

  test("a refusal is the core's whole, thrown as the runtime's problem, and writes nothing", async () => {
    const { store, recorded } = memoryStore('postgres', draftRow);
    let thrown: unknown;
    try {
      await runOperation(host(store), 'Order', key, 'status', 'transition', { to: 'shipped' }, alice);
    } catch (error) {
      thrown = error;
    }
    expect(thrown).toBeInstanceOf(RulesRefusal);
    expect(thrown).toBeInstanceOf(HttpProblem);
    const problem = thrown as RulesRefusal;
    expect(problem.status).toBe(409);
    expect(problem.code).toBe('vetoed');
    expect(problem.class).toBe('BehaviorVetoError');
    expect(problem.message).toBe(`behavior Workflow vetoes transition of Order ${key}: no transition leads from draft to shipped; from draft it can move to paid`);
    expect(problem.details).toEqual(problem.refusal.details);
    expect((problem.details as { vetoCode: string }).vetoCode).toBe('transition_not_allowed');
    // Served, the problem keeps the engine's class beside its code.
    const body = problemBody(problem, 'req-1');
    expect(body).toMatchObject({ status: 409, code: 'vetoed', class: 'BehaviorVetoError', details: problem.details });
    expect(recorded.applied).toEqual([]);
  });

  test('an update that names an owned field is refused: the anchor in the core words, another owned field in the same shape', async () => {
    const { store } = memoryStore('postgres', draftRow);
    const refusedWith = async (patch: Record<string, unknown>) => {
      try {
        await runUpdate(host(store), 'Order', key, patch, alice);
      } catch (error) {
        return (error as RulesRefusal).refusal;
      }
      throw new Error('not refused');
    };
    expect(await refusedWith({ status: 'paid' })).toEqual({
      code: 'invalid_instance',
      status: 422,
      class: 'InstanceValidationError',
      message: "Order: status: status is behavior Workflow's field: only its operations change it",
      details: { issues: [{ path: 'status', rule: 'owned', message: "status is behavior Workflow's field: only its operations change it" }] },
    });
    expect(await refusedWith({ statusEnteredAt: null })).toEqual({
      code: 'invalid_instance',
      status: 422,
      class: 'InstanceValidationError',
      message: "Order: statusEnteredAt: statusEnteredAt is behavior Workflow's field: only its operations change it",
      details: { issues: [{ path: 'statusEnteredAt', rule: 'owned', message: "statusEnteredAt is behavior Workflow's field: only its operations change it" }] },
    });
  });

  test('a create of a key the table holds is refused as conflict before anything is applied', async () => {
    const { store, recorded } = memoryStore('postgres', draftRow);
    await expect(runCreate(host(store), 'Order', { title: 'Desk' }, alice, { id: key })).rejects.toMatchObject({
      refusal: { code: 'conflict', status: 409, class: 'EngineError', message: `Order ${key} already exists` },
    });
    expect(recorded.applied).toEqual([]);
  });

  test('a type the layout lacks is refused as internal', async () => {
    const { store } = memoryStore('postgres', draftRow);
    await expect(execute(host(store), 'Invoice', transition('paid'))).rejects.toMatchObject({
      refusal: { code: 'internal', status: 500, class: 'Error', message: 'rules: the behaviors\' layout has no type Invoice; it holds Order' },
    });
  });

  test('a SQLite row reads its ISO text as epoch milliseconds, a NULL column as absent, and its version as a number', async () => {
    const { store } = memoryStore('sqlite', { c0: 'Desk', c1: 'paid', c2: null, v: 3 });
    expect(await readRow(host(store), 'Order', key)).toEqual({ own: { title: 'Desk' }, owned: { status: { status: 'paid' } }, version: 3 });
    const iso = memoryStore('sqlite', { c0: null, c1: null, c2: new Date(1234).toISOString(), v: 1 });
    expect(await readRow(host(iso.store), 'Order', key)).toEqual({ own: {}, owned: { status: { enteredAt: 1234 } }, version: 1 });
  });

  test("a layout's spec the core refuses refuses every command on its type, checked once", async () => {
    const broken: RulesLayout = { types: { Order: { ...layout.types.Order!, spec: { name: 'Order', uses: [{ behavior: 'Workflow', config: { states: [] }, anchor: { field: 'status' } }] } } } };
    let checks = 0;
    const counting: RulesCore = { run: (s, c, a) => rulesCore!.run(s, c, a), check: specs => (checks++, rulesCore!.check!(specs)) };
    const { store } = memoryStore('postgres', draftRow);
    const h: RulesHost = { core: counting, layout: broken, store };
    for (let i = 0; i < 2; i++) {
      await expect(execute(h, 'Order', transition('paid'))).rejects.toMatchObject({ refusal: { code: 'invalid_schema', status: 422 } });
    }
    expect(checks).toBe(1);
  });
});

/** A core that answers each run with the next step of steps, recording the answers it was given. */
function scripted(steps: CoreStep[]): { core: RulesCore; answers: CoreAnswers[] } {
  const answers: CoreAnswers[] = [];
  let at = 0;
  return {
    answers,
    core: {
      run(_spec, _command, given) {
        answers.push(JSON.parse(JSON.stringify(given ?? {})));
        return steps[Math.min(at++, steps.length - 1)]!;
      },
    },
  };
}

describe('the host loop on any core', () => {
  const changed = (extra: Record<string, unknown>): CoreStep =>
    ({ done: { changed: { rows: [], subRows: [], events: [], deadlines: [], result: null, ...extra } } }) as CoreStep;

  test('a lock is taken once and each read records the locks held when it was made', async () => {
    const { core, answers } = scripted([{ read: [{ kind: 'row', type: 'Order', key }] }, { lock: ['Stock/s1'] }, { read: [{ kind: 'row', type: 'Order', key }] }, changed({ result: 'ok' })]);
    const { store, recorded } = memoryStore('postgres', draftRow);
    const result = await execute({ core, layout, store }, 'Order', transition('paid'));
    expect(result.result).toBe('ok');
    expect(recorded.locks).toEqual(['Stock/s1']);
    expect(answers.map(a => ({ held: a.reads?.map(r => r.held), locks: a.locks }))).toEqual([
      { held: [], locks: [] },
      { held: [0], locks: [] },
      { held: [0], locks: ['Stock/s1'] },
      // The read asked again replaces the one made before the lock.
      { held: [1], locks: ['Stock/s1'] },
    ]);
  });

  test("a store whose locks are fenced gets first in the unit a check of each read made under a lock, a soft-deletable table's live rows alone", async () => {
    const steps: CoreStep[] = [
      { read: [{ kind: 'spec', type: 'Order' }] },
      { lock: ['Stock/s1'] },
      { read: [{ kind: 'row', type: 'Order', key }, { kind: 'row', type: 'Order', key: absent }, { kind: 'hasRows', type: 'Order' }, { kind: 'spec', type: 'Order' }] },
      changed({ rows: [{ kind: 'update', type: 'Order', key, expectedVersion: 1, own: { title: 'Chair' }, owned: {} }] }),
    ];
    const live: RulesLayout = { types: { Order: { ...layout.types.Order!, live: { column: 'deleted_at' } } } };
    const { store, recorded } = memoryStore('sqlite', draftRow);
    const { core } = scripted(steps);
    // The memory store answers every row read with draftRow; absent is
    // read by a key no row of the store's holds.
    const fenced: RulesStore = {
      ...store,
      locks: 'fenced',
      async query(text, values) {
        return values[0] === uuidToDb(absent) ? [] : store.query(text, values);
      },
    };
    await execute({ core, layout: live, store: fenced }, 'Order', transition('paid'));
    expect(recorded.locks).toEqual(['Stock/s1']);
    const [unit] = recorded.applied;
    expect(unit!.slice(0, 3)).toEqual([
      { text: 'EXISTS (SELECT 1 FROM "order" WHERE "id" = ?1 AND "_version" = ?2 AND "deleted_at" IS NULL)', values: [keyColumn, 1], check: true },
      { text: 'NOT EXISTS (SELECT 1 FROM "order" WHERE "id" = ?1 AND "deleted_at" IS NULL)', values: [uuidToDb(absent)], check: true },
      { text: 'EXISTS (SELECT 1 FROM "order" WHERE "deleted_at" IS NULL)', values: [], check: true },
    ]);
    expect(unit![3]).toMatchObject({ text: expect.stringMatching(/^UPDATE "order" SET .* WHERE "id" = \?\d+ AND "_version" = \?\d+ AND "deleted_at" IS NULL$/), expect: 1 });
    expect(unit).toHaveLength(4);

    // A store that holds its locks gets no check.
    const held = memoryStore('postgres', draftRow);
    await execute({ core: scripted(steps).core, layout: live, store: held.store }, 'Order', transition('paid'));
    expect(held.recorded.applied[0]!.every(statement => !statement.check)).toBe(true);
  });

  test('a read of whether a type has rows and of its spec answer from the store and the layout', async () => {
    const { core, answers } = scripted([{ read: [{ kind: 'hasRows', type: 'Order' }, { kind: 'spec', type: 'Order' }, { kind: 'spec', type: 'Ghost' }] }, changed({})]);
    const { store } = memoryStore('postgres', draftRow);
    await execute({ core, layout, store }, 'Order', transition('paid'));
    expect(answers[1]!.reads!.map(r => r.answer)).toEqual([{ hasRows: true }, { spec }, { spec: null }]);
  });

  test('a read, a step or a write of a kind the adapter does not take refuses the command as internal', async () => {
    const { store } = memoryStore('postgres', draftRow);
    const refusalOf = async (steps: CoreStep[]) => {
      try {
        await execute({ core: scripted(steps).core, layout, store }, 'Order', transition('paid'));
      } catch (error) {
        return (error as RulesRefusal).refusal;
      }
      throw new Error('not refused');
    };
    expect((await refusalOf([{ read: [{ kind: 'count', type: 'Order' } as never] }])).message).toContain('answers no read of this kind');
    expect((await refusalOf([{ wait: 1 } as never])).message).toContain("the core's step is of a kind");
    expect((await refusalOf([changed({ rows: [{ kind: 'touch', type: 'Order', key }] })])).message).toContain('takes no row write of this kind');
    const events = await refusalOf([changed({ events: [{ row: { type: 'Order', key }, kind: 'paid', useId: 'status', payload: {} }] })]);
    expect(events).toEqual({
      code: 'internal',
      status: 500,
      class: 'Error',
      message: "rules: transition of Order writes outbox events, which the adapter takes once each has a table of its own (D103's order of the work, step 3)",
    });
    expect((await refusalOf([changed({ subRows: [{}] })])).message).toContain('writes sub-table rows');
    expect((await refusalOf([changed({ deadlines: [{}] })])).message).toContain('writes deadlines');
    expect((await refusalOf([{ read: [{ kind: 'hasRows', type: 'Order' }] }])).message).toContain('still asked for reads or locks after 256 rounds');
  });

  test('a command gets the host request id once, for every attempt', async () => {
    const seen: (string | undefined)[] = [];
    const core: RulesCore = {
      run(_spec, command) {
        seen.push(command.request);
        return { done: { changed: { rows: [{ kind: 'delete', type: 'Order', key, expectedVersion: 1 }], subRows: [], events: [], deadlines: [], result: null } } };
      },
    };
    let requests = 0;
    const { store } = memoryStore('postgres', draftRow, [false, true]);
    const { request: _, ...unnamed } = transition('paid');
    await execute({ core, layout, store, request: () => `r${++requests}` }, 'Order', unnamed);
    expect(seen).toEqual(['r1', 'r1']);
  });
});

describe('the Postgres store', () => {
  /** A connection that records each statement, answering each with rowCounts in turn (1 unless said). */
  function connection(rowCounts: (number | Error)[] = []) {
    const statements: string[] = [];
    let at = 0;
    return {
      statements,
      queryable: {
        async query(config: { text: string; values: unknown[] }) {
          statements.push(config.text);
          if (/^(SAVEPOINT|RELEASE|ROLLBACK)/.test(config.text) || config.text.startsWith('SELECT')) return { rows: [], rowCount: 0 };
          const count = rowCounts[at++] ?? 1;
          if (count instanceof Error) throw count;
          return { rows: [], rowCount: count };
        },
      },
    };
  }
  const update = (n: number): RulesStatement => ({ text: `UPDATE t SET v = ${n}`, values: [], expect: 1 });

  test('applies a unit in a savepoint it releases', async () => {
    const { queryable, statements } = connection();
    expect(await postgresRulesStore(queryable).apply([update(1), update(2)])).toBe(true);
    expect(statements).toEqual(['SAVEPOINT rules_apply', 'UPDATE t SET v = 1', 'UPDATE t SET v = 2', 'RELEASE SAVEPOINT rules_apply']);
  });

  test('rolls the unit back and answers false when a statement changes other than the rows it expects', async () => {
    const { queryable, statements } = connection([1, 0]);
    expect(await postgresRulesStore(queryable).apply([update(1), update(2), update(3)])).toBe(false);
    expect(statements).toEqual(['SAVEPOINT rules_apply', 'UPDATE t SET v = 1', 'UPDATE t SET v = 2', 'ROLLBACK TO SAVEPOINT rules_apply', 'RELEASE SAVEPOINT rules_apply']);
  });

  test('rolls the unit back and throws a failure of the database', async () => {
    const { queryable, statements } = connection([new Error('deadlock detected')]);
    await expect(postgresRulesStore(queryable).apply([update(1)])).rejects.toThrow('deadlock detected');
    expect(statements).toEqual(['SAVEPOINT rules_apply', 'UPDATE t SET v = 1', 'ROLLBACK TO SAVEPOINT rules_apply', 'RELEASE SAVEPOINT rules_apply']);
  });

  test('checks a condition as a select of it, and rolls the unit back when it is false', async () => {
    const statements: string[] = [];
    const queryable = {
      async query(config: { text: string; values: unknown[] }) {
        statements.push(config.text);
        if (config.text.startsWith('SELECT (')) return { rows: [{ held: config.values[0] === 'kept' }], rowCount: 1 };
        return { rows: [], rowCount: 1 };
      },
    };
    const check = (key: string): RulesStatement => ({ text: 'EXISTS (SELECT 1 FROM t WHERE id = $1)', values: [key], check: true });
    expect(await postgresRulesStore(queryable).apply([check('kept'), update(1)])).toBe(true);
    expect(await postgresRulesStore(queryable).apply([check('moved'), update(2)])).toBe(false);
    expect(statements).toEqual([
      'SAVEPOINT rules_apply',
      'SELECT (EXISTS (SELECT 1 FROM t WHERE id = $1)) AS held',
      'UPDATE t SET v = 1',
      'RELEASE SAVEPOINT rules_apply',
      'SAVEPOINT rules_apply',
      'SELECT (EXISTS (SELECT 1 FROM t WHERE id = $1)) AS held',
      'ROLLBACK TO SAVEPOINT rules_apply',
      'RELEASE SAVEPOINT rules_apply',
    ]);
  });

  test("takes a lock as the transaction's advisory lock on the key's hash, and applies an empty unit without a savepoint", async () => {
    const { queryable, statements } = connection();
    const store = postgresRulesStore(queryable);
    await store.lock('Stock/sku-1');
    expect(await store.apply([])).toBe(true);
    expect(statements).toEqual(['SELECT pg_advisory_xact_lock(hashtextextended($1, 0))']);
  });
});

describe('the repositories', () => {
  test('lazyCore makes its core on first use, once', () => {
    let made = 0;
    const inner: RulesCore = { run: () => ({ read: [] }) };
    const lazy = lazyCore(() => (made++, inner));
    expect(made).toBe(0);
    lazy.run(spec, transition('paid'));
    lazy.run(spec, transition('paid'));
    expect(made).toBe(1);
    expect(lazy.check?.([spec])).toEqual([null]);
  });

  test('withRulesCore keeps the options createOrm takes and the core they name', () => {
    const named: RulesCore = { run: () => ({ read: [] }) };
    expect(withRulesCore('u1', () => named).user).toBe('u1');
    expect(withRulesCore(null, () => named).user).toBeNull();
    expect(withRulesCore({ user: 'u2', rules: named }, () => ({ run: () => ({ lock: [] }) })).rules).toBe(named);
    expect(withRulesCore(undefined, () => named).rules).not.toBe(named);
  });
});
