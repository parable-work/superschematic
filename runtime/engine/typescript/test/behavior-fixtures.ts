// Behaviors the engine's tests register: a counter, a flag that holds an
// instance still, and a tally that requires the counter and changes it
// through its operation. The flag conflicts with the tally. A hold, which
// fences writes by a generation they present as its precondition and
// vetoes with codes, registers only where a test asks for it.
import { readFileSync } from 'node:fs';

import {
  BehaviorConfigError,
  BehaviorVetoError,
  defineBehavior,
  type BehaviorDeclaration,
  type Engine,
  type EngineOptions,
  type GuardRequest,
} from '../dist/index.js';
import { alice, openTestEngine, schemaDocument, type Field } from './helpers.ts';

/**
 * The core meta-schema with behaviors let through: any name, any config.
 * The core registry's own admits only the core's behaviors, each config
 * held to its declaration; a deployment passes its binary's, which lists
 * what the binary declares. With this one the engine's own checks are
 * what refuse a composition.
 */
export function openMetaSchema(): Record<string, unknown> {
  const metaSchema = JSON.parse(readFileSync(new URL('../../../../ir/typescript/schema-file.json', import.meta.url), 'utf8')) as {
    $defs: {
      TypeDef: { properties: { behaviors: Record<string, unknown> } };
      BehaviorRef: { allOf?: unknown; properties: { name: Record<string, unknown> } };
    };
  };
  delete metaSchema.$defs.TypeDef.properties.behaviors.maxItems;
  delete metaSchema.$defs.BehaviorRef.properties.name.enum;
  delete metaSchema.$defs.BehaviorRef.allOf;
  return metaSchema as unknown as Record<string, unknown>;
}

const noParams = { type: 'object', additionalProperties: false } as const;

export interface CounterConfig {
  start: number;
  limit?: number;
}

export const counterDeclaration: BehaviorDeclaration = {
  name: 'test.Counter',
  description: 'Counts up from a start, to an optional limit.',
  configSchema: {
    type: 'object',
    additionalProperties: false,
    properties: { start: { type: 'integer', minimum: 0 }, limit: { type: 'integer', minimum: 1 } },
  },
  fields: [{ name: 'count', description: 'The count.' }],
  operations: [
    {
      name: 'increment',
      description: 'Adds to the count.',
      paramsSchema: { type: 'object', additionalProperties: false, properties: { by: { type: 'integer', minimum: 1 } } },
      resultSchema: { type: 'object', required: ['count'], additionalProperties: false, properties: { count: { type: 'integer' } } },
      writes: true,
    },
    {
      name: 'history',
      description: 'Lists what each increment added.',
      paramsSchema: noParams,
      resultSchema: { type: 'array', items: { type: 'integer' } },
    },
  ],
};

export const counter = defineBehavior<CounterConfig>({
  declaration: counterDeclaration,
  parseConfig(config) {
    const { start = 0, limit } = config as { start?: number; limit?: number };
    if (limit !== undefined && start > limit) {
      throw new BehaviorConfigError(`start ${start} is past limit ${limit}`);
    }
    return limit === undefined ? { start } : { start, limit };
  },
  configChange(before, after) {
    if (before === undefined) {
      return undefined; // Instances that exist count from 0.
    }
    if (after === undefined) {
      return 'removing it loses every count';
    }
    if (before.start !== after.start) {
      return 'start cannot change';
    }
    if (after.limit !== undefined && (before.limit === undefined || after.limit < before.limit)) {
      return 'a limit can only rise or go';
    }
    return undefined;
  },
  migrations: [
    { version: 1, name: 'count', columns: { count: { type: 'integer', notNull: true, default: 0 } } },
    {
      version: 2,
      name: 'history',
      up(sql) {
        sql.run(
          `CREATE TABLE ${sql.table('history')} (namespace TEXT NOT NULL, schema TEXT NOT NULL, id TEXT NOT NULL, delta INTEGER NOT NULL, at INTEGER NOT NULL) STRICT`
        );
        sql.run(`CREATE INDEX ${sql.table('history_by_instance')} ON ${sql.table('history')} (namespace, schema, id)`);
      },
    },
  ],
  initialize(context) {
    context.columns.set({ count: context.config.start });
  },
  guard(context, request) {
    if (request.kind !== 'operation' || request.operation !== 'increment' || context.config.limit === undefined) {
      return undefined;
    }
    const by = (request.params.by as number | undefined) ?? 1;
    if (Number(context.columns.get().count) + by > context.config.limit) {
      return `the count would pass its limit, ${context.config.limit}`;
    }
    return undefined;
  },
  operations: {
    increment(context, params) {
      const by = (params.by as number | undefined) ?? 1;
      const count = Number(context.columns.get().count) + by;
      context.columns.set({ count });
      context.sql.run(`INSERT INTO ${context.sql.table('history')} (namespace, schema, id, delta, at) VALUES (?, ?, ?, ?, ?)`, [
        context.namespace,
        context.schema,
        context.id,
        by,
        context.now,
      ]);
      return { count };
    },
    history(context) {
      return context.sql
        .all(`SELECT delta FROM ${context.sql.table('history')} WHERE namespace = ? AND schema = ? AND id = ? ORDER BY rowid`, [
          context.namespace,
          context.schema,
          context.id,
        ])
        .map((row) => Number(row.delta));
    },
  },
  fields: {
    count: (context) => context.columns.get().count,
  },
  afterChange(context, change) {
    if (change.kind === 'delete') {
      context.sql.run(`DELETE FROM ${context.sql.table('history')} WHERE namespace = ? AND schema = ? AND id = ?`, [
        context.namespace,
        context.schema,
        context.id,
      ]);
    }
  },
});

export const flagDeclaration: BehaviorDeclaration = {
  name: 'test.Flag',
  description: 'Holds an instance still while it is flagged.',
  conflicts: ['test.Tally'],
  fields: [
    { name: 'flagged', description: 'Whether the instance is flagged.' },
    { name: 'flagReason', description: 'Why; absent when it is not flagged.' },
  ],
  operations: [
    {
      name: 'flag',
      paramsSchema: { type: 'object', additionalProperties: false, required: ['reason'], properties: { reason: { type: 'string', minLength: 1 } } },
      resultSchema: { type: 'boolean' },
      writes: true,
      invocationPolicy: 'ask',
    },
    { name: 'unflag', paramsSchema: noParams, resultSchema: { type: 'boolean' }, writes: true },
  ],
};

export const flag = defineBehavior({
  declaration: flagDeclaration,
  migrations: [
    {
      version: 1,
      name: 'flag',
      columns: { flagged: { type: 'integer', notNull: true, default: 0 }, reason: { type: 'text' } },
    },
  ],
  // While flagged, only the flag's own operations run.
  guard(context, request) {
    const { flagged, reason } = context.columns.get();
    if (flagged !== 1 || (request.kind === 'operation' && request.behavior === 'test.Flag')) {
      return undefined;
    }
    return `it is flagged: ${String(reason)}`;
  },
  operations: {
    flag(context, params) {
      context.columns.set({ flagged: 1, reason: params.reason as string });
      return true;
    },
    unflag(context) {
      context.columns.set({ flagged: 0, reason: null });
      return false;
    },
  },
  fields: {
    flagged: (context) => context.columns.get().flagged === 1,
    flagReason: (context) => context.columns.get().reason,
  },
});

export const tallyDeclaration: BehaviorDeclaration = {
  name: 'test.Tally',
  description: 'Counts the updates of an instance and bumps its counter.',
  requires: ['test.Counter'],
  fields: [{ name: 'edits', description: 'How many updates the instance has had.' }],
  operations: [
    {
      name: 'bump',
      description: 'Increments the counter, once or more, through its operation.',
      paramsSchema: { type: 'object', additionalProperties: false, properties: { times: { type: 'integer', minimum: 1 } } },
      resultSchema: { type: 'object', required: ['count'], properties: { count: { type: 'integer' } } },
      writes: true,
    },
    {
      name: 'tryBump',
      description: 'Increments the counter unless a guard vetoes it, and counts the attempt.',
      paramsSchema: noParams,
      resultSchema: { type: 'object', required: ['vetoed'], properties: { vetoed: { type: 'boolean' } } },
      writes: true,
    },
  ],
};

export const tally = defineBehavior({
  declaration: tallyDeclaration,
  migrations: [{ version: 1, name: 'edits', columns: { edits: { type: 'integer', notNull: true, default: 0 } } }],
  operations: {
    bump(context, params) {
      let result: unknown;
      for (let time = 0; time < ((params.times as number | undefined) ?? 1); time += 1) {
        result = context.call('test.Counter', 'increment', { by: 1 });
      }
      return result;
    },
    tryBump(context) {
      context.columns.set({ edits: Number(context.columns.get().edits) + 100 });
      try {
        context.call('test.Counter', 'increment', { by: 1 });
        return { vetoed: false };
      } catch (error) {
        if (error instanceof BehaviorVetoError) {
          return { vetoed: true };
        }
        throw error;
      }
    },
  },
  fields: {
    edits: (context) => context.columns.get().edits,
  },
  afterChange(context, change) {
    if (change.kind === 'update') {
      context.columns.set({ edits: Number(context.columns.get().edits) + 1 });
    }
  },
});

export const testBehaviors = [counter, flag, tally];

export const holdDeclaration: BehaviorDeclaration = {
  name: 'test.Hold',
  description: 'Numbers the instance by a generation, which a write presents as its precondition to fence itself.',
  configSchema: { type: 'object', additionalProperties: false, properties: { require: { type: 'boolean' } } },
  fields: [{ name: 'generation', description: 'The generation a write presents.' }],
  operations: [
    {
      name: 'advance',
      description: 'Moves the instance to its next generation.',
      paramsSchema: noParams,
      resultSchema: { type: 'object', required: ['generation'], properties: { generation: { type: 'integer' } } },
      writes: true,
    },
    {
      name: 'forward',
      description: 'Advances another instance, presenting the generation given as its precondition.',
      paramsSchema: { type: 'object', additionalProperties: false, required: ['id'], properties: { id: { type: 'string' }, generation: { type: 'integer' } } },
      resultSchema: { type: 'object', required: ['generation'], properties: { generation: { type: 'integer' } } },
      writes: true,
    },
    {
      name: 'refuse',
      description: 'Refuses with a veto that carries the code and details given.',
      paramsSchema: { type: 'object', additionalProperties: false, properties: { code: { type: 'string' }, details: {} } },
      resultSchema: { type: 'null' },
      writes: true,
    },
    {
      name: 'peek',
      description: 'Reads the generation.',
      paramsSchema: noParams,
      resultSchema: { type: 'object', required: ['generation'], properties: { generation: { type: 'integer' } } },
    },
  ],
  preconditionSchema: {
    type: 'object',
    additionalProperties: false,
    required: ['generation'],
    properties: { generation: { type: 'integer', minimum: 0 } },
  },
  vetoes: [
    { code: 'stale', description: 'The generation presented is not the instance\'s.' },
    { code: 'required', description: 'With require, a write presents no generation.' },
    { code: 'refused' },
  ],
};

/** Every request test.Hold's guard is asked, in order; a test empties it. */
export const holdRequests: GuardRequest[] = [];

// A write that presents a generation other than the instance's is
// refused; with require, so is one that presents none. A behavior's own
// requests (caller) never present one, and a create, which has nothing
// to fence yet, is let through unrecorded.
export const hold = defineBehavior<{ require?: boolean }>({
  declaration: holdDeclaration,
  migrations: [{ version: 1, name: 'generation', columns: { generation: { type: 'integer', notNull: true, default: 0 } } }],
  guard(view, request) {
    if (request.kind === 'create') {
      return undefined;
    }
    holdRequests.push(request);
    if (request.kind === 'operation' && !request.writes) {
      return undefined;
    }
    const current = Number(view.columns.get().generation);
    const presented = request.precondition?.generation as number | undefined;
    if (presented !== undefined && presented !== current) {
      return { reason: `generation ${presented} is stale: the instance is at ${current}`, code: 'stale', details: { generation: presented, current } };
    }
    if (view.config.require === true && presented === undefined && (request.kind === 'delete' || request.caller === undefined)) {
      return { reason: 'a write presents the generation', code: 'required' };
    }
    return undefined;
  },
  operations: {
    advance(context) {
      const generation = Number(context.columns.get().generation) + 1;
      context.columns.set({ generation });
      return { generation };
    },
    forward(context, params) {
      const generation = params.generation as number | undefined;
      return context.instances.invoke(
        context.schema,
        params.id as string,
        'advance',
        {},
        generation === undefined ? undefined : { preconditions: { 'test.Hold': { generation } } }
      );
    },
    refuse(context, params) {
      throw new BehaviorVetoError('test.Hold', 'refuse', context.schema, context.id, {
        reason: 'refused as asked',
        ...(params.code === undefined ? {} : { code: params.code as string }),
        ...(params.details === undefined ? {} : { details: params.details as Record<string, unknown> }),
      });
    },
    peek: (context) => ({ generation: Number(context.columns.get().generation) }),
  },
  fields: {
    generation: (view) => view.columns.get().generation,
  },
});

/** An Item schema whose instance type composes behaviors: [{ name, config? }]. */
export function itemDocument(behaviors: Array<{ name: string; config?: unknown }>, fields: Field[] = []): Record<string, unknown> {
  const document = schemaDocument('Item', [{ name: 'title', typeRef: { name: 'string' }, required: true }, ...fields]) as {
    types: { Item: Record<string, unknown> };
  };
  document.types.Item.behaviors = behaviors;
  return document;
}

/** openBehaviorEngine opens an engine with the test behaviors and the open meta-schema. */
export function openBehaviorEngine(options: Partial<EngineOptions> = {}): Engine {
  return openTestEngine({ metaSchema: openMetaSchema(), behaviors: testBehaviors, ...options });
}

/** publishItem defines and publishes an Item schema that composes behaviors. */
export function publishItem(engine: Engine, behaviors: Array<{ name: string; config?: unknown }>, fields: Field[] = []): void {
  engine.schemas.define(alice, itemDocument(behaviors, fields));
  engine.schemas.publish(alice, 'Item');
}

/** columnsOf lists the columns of engine_instances. */
export function columnsOf(engine: Engine): string[] {
  return engine.storage.all("SELECT name FROM pragma_table_info('engine_instances')").map((row) => String(row.name));
}

/** tablesOf lists the tables and indexes whose names start with prefix. */
export function tablesOf(engine: Engine, prefix: string): string[] {
  return engine.storage
    .all("SELECT name FROM sqlite_master WHERE name LIKE ? ESCAPE '\\' ORDER BY name", [`${prefix.replace(/_/g, '\\_')}%`])
    .map((row) => String(row.name));
}
