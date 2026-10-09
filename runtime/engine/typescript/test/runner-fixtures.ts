// The behaviors the runner tests register: test.Ledger keeps the notes its
// mark operation writes on an instance, and its reactions and schedule do
// what the running test puts in probe; test.Pacer has one schedule, whose
// interval follows the config of each schema that composes it.
import {
  allowAll,
  defineBehavior,
  openEngine,
  type BehaviorDeclaration,
  type Engine,
  type EngineEvent,
  type EngineOptions,
  type FrozenJSON,
  type Principal,
  type ReactionContext,
  type ScheduleContext,
} from '../dist/index.js';
import { openMetaSchema } from './behavior-fixtures.ts';
import { alice, freshPath, schemaDocument, track } from './helpers.ts';

/** The principal the runner acts as in these tests. */
export const runnerPrincipal: Principal = { subject: 'runner', permissions: [] };

export interface LedgerConfig {
  /** The schemas besides its own whose events its reactions hear. */
  readonly watch?: readonly string[];
  /** Whether its watches turns its reactions off on the schema. */
  readonly off?: boolean;
}

export interface PacerConfig {
  /** The interval of its schedule on the schema. */
  readonly everyMs?: number;
}

/** What the running test makes test.Ledger's reactions and schedule, and test.Pacer's, do; nothing when unset. */
export const probe: {
  react?: (context: ReactionContext<LedgerConfig>, event: EngineEvent) => void;
  sweep?: (context: ScheduleContext<LedgerConfig>) => void;
  watches?: (config: LedgerConfig) => readonly string[] | null;
  /** test.Pacer's interval for a config, in place of its everyMs. */
  every?: (config: PacerConfig) => unknown;
  tick?: (context: ScheduleContext<PacerConfig>) => void;
} = {};

export function resetProbe(): void {
  delete probe.react;
  delete probe.sweep;
  delete probe.watches;
  delete probe.every;
  delete probe.tick;
}

export const ledgerDeclaration: BehaviorDeclaration = {
  name: 'test.Ledger',
  description: 'Keeps the notes the runner writes on an instance.',
  configSchema: {
    type: 'object',
    additionalProperties: false,
    properties: { watch: { type: 'array', items: { type: 'string' } }, off: { type: 'boolean' } },
  },
  fields: [{ name: 'notes', description: 'The notes, in the order they were written.' }],
  operations: [
    {
      name: 'mark',
      description: 'Writes a note.',
      paramsSchema: { type: 'object', additionalProperties: false, required: ['note'], properties: { note: { type: 'string' } } },
      resultSchema: { type: 'object', required: ['count'], properties: { count: { type: 'integer' } } },
      writes: true,
    },
    {
      name: 'count',
      description: 'Counts the notes of every instance of the schema.',
      scope: 'schema',
      paramsSchema: { type: 'object', additionalProperties: false },
      resultSchema: { type: 'integer' },
    },
  ],
};

export const ledger = defineBehavior<LedgerConfig>({
  declaration: ledgerDeclaration,
  migrations: [
    {
      version: 1,
      name: 'notes',
      up(sql) {
        sql.run(`CREATE TABLE ${sql.table('notes')} (
          row       INTEGER PRIMARY KEY,
          namespace TEXT NOT NULL,
          schema    TEXT NOT NULL,
          id        TEXT NOT NULL,
          note      TEXT NOT NULL
        ) STRICT`);
      },
    },
  ],
  operations: {
    mark(context, params) {
      const table = context.sql.table('notes');
      context.sql.run(`INSERT INTO ${table} (namespace, schema, id, note) VALUES (?, ?, ?, ?)`, [
        context.namespace,
        context.schema,
        context.id,
        params.note as string,
      ]);
      const row = context.sql.get(`SELECT COUNT(*) AS count FROM ${table} WHERE namespace = ? AND schema = ? AND id = ?`, [
        context.namespace,
        context.schema,
        context.id,
      ]);
      return { count: Number(row?.count) };
    },
  },
  schemaOperations: {
    count(context) {
      const row = context.sql.get(`SELECT COUNT(*) AS count FROM ${context.sql.table('notes')} WHERE namespace = ? AND schema = ?`, [
        context.namespace,
        context.schema,
      ]);
      return Number(row?.count);
    },
  },
  fields: {
    notes: (view) => {
      const rows = view.sql.all(`SELECT note FROM ${view.sql.table('notes')} WHERE namespace = ? AND schema = ? AND id = ? ORDER BY row`, [
        view.namespace,
        view.schema,
        view.id,
      ]);
      return rows.length === 0 ? undefined : rows.map((row) => String(row.note));
    },
  },
  // It keeps nothing an instance needs, so a version may add, remove or
  // change it on a schema with instances.
  configChange: () => undefined,
  afterChange(context, change) {
    if (change.kind === 'delete') {
      context.sql.run(`DELETE FROM ${context.sql.table('notes')} WHERE namespace = ? AND schema = ? AND id = ?`, [
        context.namespace,
        context.schema,
        context.id,
      ]);
    }
  },
  reactions: {
    watches: (config) => (probe.watches ? probe.watches(config) : config.off === true ? null : (config.watch ?? [])),
    react(context, event) {
      probe.react?.(context, event);
    },
  },
  schedules: {
    sweep: {
      everyMs: 60_000,
      run(context) {
        probe.sweep?.(context);
      },
    },
  },
});

export const pacer = defineBehavior<PacerConfig>({
  declaration: {
    name: 'test.Pacer',
    description: 'Ticks on each schema at the interval its config gives.',
    // No minimum: the runner, not the config, holds the interval to its rule.
    configSchema: { type: 'object', additionalProperties: false, properties: { everyMs: { type: 'integer' } } },
  },
  configChange: () => undefined,
  schedules: {
    tick: {
      everyMs: (config) => (probe.every ? (probe.every(config) as number) : (config.everyMs ?? 60_000)),
      run(context) {
        probe.tick?.(context);
      },
    },
  },
});

/** A schema of one string field whose instance type composes test.Pacer with a config. */
export function pacerDocument(name: string, config: PacerConfig): Record<string, unknown> {
  const document = schemaDocument(name, [{ name: 'title', typeRef: { name: 'string' }, required: true }]) as {
    types: Record<string, Record<string, unknown>>;
  };
  document.types[name].behaviors = [{ name: 'test.Pacer', config }];
  return document;
}

/** A schema of one string field whose instance type composes test.Ledger with a config. */
export function ledgerDocument(name: string, config: LedgerConfig = {}): Record<string, unknown> {
  const document = schemaDocument(name, [{ name: 'title', typeRef: { name: 'string' }, required: true }]) as {
    types: Record<string, Record<string, unknown>>;
  };
  document.types[name].behaviors = [{ name: 'test.Ledger', ...(Object.keys(config).length === 0 ? {} : { config }) }];
  return document;
}

/** A plain schema of one string field. */
export function plainDocument(name: string): Record<string, unknown> {
  return schemaDocument(name, [{ name: 'title', typeRef: { name: 'string' }, required: true }]);
}

/** A clock a test moves by hand. */
export interface TestClock {
  now: number;
  (): number;
}

export function testClock(start = 1_000_000): TestClock {
  const clock = (() => clock.now) as TestClock;
  clock.now = start;
  return clock;
}

/** openRunnerEngine opens an engine with test.Ledger, a test clock and the runner principal; the test cleans it up. */
export function openRunnerEngine(options: Partial<EngineOptions> = {}): Engine {
  return track(
    openEngine({
      path: freshPath(),
      policy: allowAll,
      metaSchema: openMetaSchema(),
      behaviors: [ledger],
      runner: { principal: runnerPrincipal },
      ...options,
    })
  );
}

/** publish defines and publishes a document as alice. */
export function publish(engine: Engine, document: Record<string, unknown>): void {
  engine.schemas.define(alice, document);
  engine.schemas.publish(alice, String(document.name));
}

/** mark is a reaction that writes a note of the event on the instance it names. */
export function mark(context: ReactionContext<LedgerConfig>, event: EngineEvent, note = `${event.kind} ${event.cursor}`): void {
  context.instances.invoke(event.schema, event.instanceId as string, 'mark', { note } as FrozenJSON);
}

/** settle lets the event loop run its pending immediates, which is when a started runner works. */
export async function settle(turns = 5): Promise<void> {
  for (let turn = 0; turn < turns; turn += 1) {
    await new Promise<void>((resolve) => setImmediate(resolve));
  }
}

/** waitFor lets the event loop turn until done() holds, at most turns times, and fails after. */
export async function waitFor(done: () => boolean, turns = 100): Promise<void> {
  for (let turn = 0; turn < turns; turn += 1) {
    if (done()) {
      return;
    }
    await new Promise<void>((resolve) => setImmediate(resolve));
  }
  throw new Error(`waitFor: still not done after ${turns} turns of the event loop`);
}
