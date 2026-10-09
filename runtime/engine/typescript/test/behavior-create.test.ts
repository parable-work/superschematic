// A behavior creates instances (D16, amended): instances.create, wherever
// a writing operation can be invoked, as engine.instances.create would
// for the call's principal: the policy's write, the live version's
// validation, every behavior's initialize and afterChange and the create
// event, in a savepoint of the call's transaction, nesting like an invoke;
// from the runner's work, with the cause its event records.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import {
  BehaviorError,
  EngineError,
  InstanceValidationError,
  allowAll,
  defineBehavior,
  openEngine,
  type AccessPolicy,
  type AccessRequest,
  type BehaviorDeclaration,
  type Engine,
  type EngineOptions,
  type FrozenJSON,
  type InstanceFields,
  type InstanceRecord,
  type Principal,
} from '../dist/index.js';
import { counter, openMetaSchema } from './behavior-fixtures.ts';
import { alice, cleanup, drivers, fieldsOf, freshPath, openTestEngine, schemaDocument, thrown, track } from './helpers.ts';
import { runnerPrincipal, testClock } from './runner-fixtures.ts';

afterEach(() => {
  flags.fieldCreates = false;
  cleanup();
});

const bob: Principal = { subject: 'bob', permissions: [] };

/** What the running test makes the behaviors do beyond their config. */
const flags = { fieldCreates: false };

const spawnParams = {
  type: 'object',
  additionalProperties: false,
  required: ['schema', 'data'],
  properties: { schema: { type: 'string' }, data: { type: 'object' }, id: { type: 'string' }, catch: { type: 'boolean' } },
} as const;

interface SpawnerConfig {
  /** Children a new instance gets, from afterChange. */
  children?: { schema: string; count: number };
}

const spawnerDeclaration: BehaviorDeclaration = {
  name: 'test.Spawner',
  description: 'Creates instances from its operations and its afterChange.',
  configSchema: {
    type: 'object',
    additionalProperties: false,
    properties: {
      children: {
        type: 'object',
        additionalProperties: false,
        required: ['schema', 'count'],
        properties: { schema: { type: 'string' }, count: { type: 'integer', minimum: 1 } },
      },
    },
  },
  fields: [
    { name: 'spawned', description: 'How many creates its spawn operation asked for.' },
    { name: 'probe', description: 'Tries a create when the test sets flags.fieldCreates.' },
  ],
  operations: [
    { name: 'spawn', description: 'Creates an instance.', paramsSchema: spawnParams, resultSchema: true, writes: true },
    { name: 'spawnRead', description: 'Tries a create from a read-only operation.', paramsSchema: spawnParams, resultSchema: true },
    { name: 'guarded', description: 'Its guard tries a create before it runs.', paramsSchema: spawnParams, resultSchema: true, writes: true },
    {
      name: 'spawnMany',
      description: 'Creates count instances of a schema, from no instance.',
      scope: 'schema',
      paramsSchema: {
        type: 'object',
        additionalProperties: false,
        required: ['schema', 'count'],
        properties: { schema: { type: 'string' }, count: { type: 'integer', minimum: 1 } },
      },
      resultSchema: true,
      writes: true,
    },
    { name: 'spawnPeek', description: 'Tries a create from a read-only schema-level operation.', scope: 'schema', paramsSchema: spawnParams, resultSchema: true },
  ],
};

// summary is what spawn returns of a created record: enough to see it is
// the record a create returns, and frozen.
function summary(record: InstanceRecord): Record<string, unknown> {
  return {
    id: record.id,
    namespace: record.namespace,
    schema: record.schema,
    schemaNamespace: record.schemaNamespace,
    seq: record.seq,
    createdBy: record.createdBy,
    data: record.data,
    behaviors: record.behaviors,
    frozen: Object.isFrozen(record) && Object.isFrozen(record.data) && Object.isFrozen(record.behaviors),
  };
}

function spawnOptions(params: FrozenJSON): { id?: string } | undefined {
  return params.id === undefined ? undefined : { id: params.id as string };
}

const spawner = defineBehavior<SpawnerConfig>({
  declaration: spawnerDeclaration,
  migrations: [{ version: 1, name: 'spawned', columns: { spawned: { type: 'integer', notNull: true, default: 0 } } }],
  guard(view, request) {
    if (request.kind === 'operation' && request.operation === 'guarded') {
      view.instances.create(request.params.schema as string, request.params.data as FrozenJSON, spawnOptions(request.params));
    }
    return undefined;
  },
  operations: {
    spawn(context, params) {
      context.columns.set({ spawned: Number(context.columns.get().spawned) + 1 });
      const create = () => context.instances.create(params.schema as string, params.data as FrozenJSON, spawnOptions(params));
      if (params.catch !== true) {
        return summary(create());
      }
      try {
        return summary(create());
      } catch (error) {
        return { threw: error instanceof Error ? error.name : String(error), message: error instanceof Error ? error.message : '' };
      }
    },
    spawnRead: (context, params) => summary(context.instances.create(params.schema as string, params.data as FrozenJSON, spawnOptions(params))),
    guarded: () => 'ran',
  },
  schemaOperations: {
    spawnMany(context, params) {
      const ids: string[] = [];
      for (let n = 1; n <= (params.count as number); n += 1) {
        ids.push(context.instances.create(params.schema as string, { title: `batch ${n}` }).id);
      }
      return ids;
    },
    spawnPeek: (context, params) => summary(context.instances.create(params.schema as string, params.data as FrozenJSON, spawnOptions(params))),
  },
  fields: {
    spawned: (view) => view.columns.get().spawned,
    probe: (view) => (flags.fieldCreates ? view.instances.create('Task', { title: 'from a field' }).id : undefined),
  },
  afterChange(context, change) {
    const children = context.config.children;
    if (change.kind !== 'create' || children === undefined) {
      return;
    }
    for (let n = 1; n <= children.count; n += 1) {
      context.instances.create(children.schema, { title: `${String(context.data.title)} ${n}` }, { id: `${context.id}-${n}` });
    }
  },
});

// test.Child marks a task born in afterChange; its initialize refuses a
// title that starts with boom and, for a depth, creates a task one less
// deep under the task's id.
const child = defineBehavior({
  declaration: { name: 'test.Child', description: 'Marks new tasks, and nests them.', fields: [{ name: 'born', description: 'Whether afterChange saw the create.' }] },
  migrations: [{ version: 1, name: 'born', columns: { born: { type: 'integer', notNull: true, default: 0 } } }],
  initialize(context) {
    if (String(context.data.title).startsWith('boom')) {
      throw new EngineError('invalid_argument', `${String(context.data.title)} refuses to start`);
    }
    const depth = context.data.depth;
    if (typeof depth === 'number' && depth > 0) {
      context.instances.create(context.schema, { title: 'nest', depth: depth - 1 }, { id: `${context.id}.${depth - 1}` });
    }
  },
  afterChange(context, change) {
    if (change.kind === 'create') {
      context.columns.set({ born: 1 });
    }
  },
  fields: { born: (view) => view.columns.get().born },
});

// test.Planner plans a task for each instance a caller creates, from a
// reaction, and restocks on a schedule after counting its schema's
// instances.
const planner = defineBehavior({
  declaration: { name: 'test.Planner', description: 'Creates tasks from the runner.' },
  reactions: {
    react(context, event) {
      if (event.kind === 'create' && event.cause === undefined) {
        context.instances.create('Task', { title: `plan for ${String(event.instanceId)}` }, { id: `plan-${String(event.instanceId)}` });
      }
    },
  },
  schedules: {
    restock: {
      everyMs: 60_000,
      run(context) {
        const shelves = Number(context.sql.get(`SELECT COUNT(*) AS n FROM ${context.sql.instances()}`)?.n);
        context.instances.create('Task', { title: `restock ${shelves}` }, { id: `stock-${context.now}` });
      },
    },
  },
});

function document(name: string, behaviors: Array<{ name: string; config?: unknown }>): Record<string, unknown> {
  const doc = schemaDocument(name, [
    { name: 'title', typeRef: { name: 'string' }, required: true },
    { name: 'depth', typeRef: { name: 'Generic.Int64' } },
  ]) as { types: Record<string, Record<string, unknown>> };
  doc.types[name].behaviors = behaviors;
  return doc;
}

function publish(engine: Engine, name: string, behaviors: Array<{ name: string; config?: unknown }>, namespace?: string): void {
  const target = namespace === undefined ? {} : { namespace };
  engine.schemas.define(alice, document(name, behaviors), target);
  engine.schemas.publish(alice, name, target);
}

const taskBehaviors = [{ name: 'test.Counter', config: { start: 7 } }, { name: 'test.Child' }];

/** A task's fields as a read returns them once it is born: its count from 7. */
function task(title: string, data: Record<string, unknown> = {}): InstanceFields {
  return { data: { title, ...data }, behaviors: { 'test.Counter': { count: 7 }, 'test.Child': { born: 1 } } };
}

/** A policy that records every question and lets alice do anything; bob gets what rules allow. */
function recording(rules: (request: AccessRequest) => boolean = () => true): { policy: AccessPolicy; asked: AccessRequest[] } {
  const asked: AccessRequest[] = [];
  const policy: AccessPolicy = (request) => {
    asked.push(request);
    return request.principal.subject === 'alice' || rules(request);
  };
  return { policy, asked };
}

for (const driver of drivers) {
  function open(options: Partial<EngineOptions> = {}): Engine {
    let made = 0;
    return openTestEngine({
      driver,
      metaSchema: openMetaSchema(),
      behaviors: [counter, spawner, child],
      ids: () => `gen-${(made += 1)}`,
      ...options,
    });
  }

  // A Note spawns; a Task counts from 7 and is born.
  function world(options: Partial<EngineOptions> = {}): Engine {
    const engine = open(options);
    publish(engine, 'Note', [{ name: 'test.Spawner' }]);
    publish(engine, 'Task', taskBehaviors);
    engine.instances.create(alice, 'Note', { title: 'First' }, { id: 'n1' });
    return engine;
  }

  function lastCursor(engine: Engine): number {
    return engine.runner.status().head;
  }

  function eventsAfter(engine: Engine, after: number): unknown[] {
    return engine.events
      .read(alice, { after })
      .events.map((event) => [
        event.kind,
        event.schema,
        event.instanceId,
        event.kind === 'operation' ? (event.change as { operation: string }).operation : event.actor,
        ...(event.cause === undefined ? [] : [event.cause]),
      ]);
  }

  function tasks(engine: Engine): string[] {
    return engine.instances.list(alice, 'Task').items.map((item) => item.id);
  }

  function spawned(engine: Engine): unknown {
    return engine.instances.get(alice, 'Note', 'n1')?.behaviors['test.Spawner']?.spawned;
  }

  describe(`a behavior creates instances (${driver})`, () => {
    test('an operation creates one as its caller would: validated, every initialize and afterChange, its event, deep-frozen', () => {
      const engine = world();
      const from = lastCursor(engine);
      assert.deepEqual(engine.instances.invoke(bob, 'Note', 'n1', 'spawn', { schema: 'Task', data: { title: 'Paint' }, id: 't1' }), {
        id: 't1',
        namespace: 'default',
        schema: 'Task',
        schemaNamespace: 'default',
        seq: 1,
        createdBy: 'bob',
        data: { title: 'Paint' },
        behaviors: { 'test.Counter': { count: 7 }, 'test.Child': { born: 1 } },
        frozen: true,
      });
      assert.deepEqual(fieldsOf(engine.instances.get(alice, 'Task', 't1')), task('Paint'));
      assert.deepEqual(eventsAfter(engine, from), [
        ['create', 'Task', 't1', 'bob'],
        ['operation', 'Note', 'n1', 'spawn'],
      ]);
      // Without an id, the engine's id generator names it.
      assert.equal((engine.instances.invoke(alice, 'Note', 'n1', 'spawn', { schema: 'Task', data: { title: 'Sand' } }) as { id: string }).id, 'gen-1');
      assert.equal(spawned(engine), 2);
    });

    test("a create the engine refuses refuses the call: invalid data, a behavior field's name as an own field, a taken or malformed id, no such schema", () => {
      const engine = world();
      engine.instances.invoke(alice, 'Note', 'n1', 'spawn', { schema: 'Task', data: { title: 'Paint' }, id: 't1' });
      const from = lastCursor(engine);
      const spawn = (data: Record<string, unknown>, extra: Record<string, unknown> = {}) =>
        engine.instances.invoke(alice, 'Note', 'n1', 'spawn', { schema: 'Task', data, ...extra });
      assert.equal(thrown(() => spawn({}), InstanceValidationError).code, 'invalid_instance');
      assert.deepEqual(
        thrown(() => spawn({ title: 'Paint', count: 3 }), InstanceValidationError).issues.map((issue) => [issue.path, issue.rule]),
        [['count', 'unknown']]
      );
      const taken = thrown(() => spawn({ title: 'Again' }, { id: 't1' }), EngineError);
      assert.deepEqual([taken.code, taken.message], ['conflict', 'Task t1 already exists in namespace default']);
      assert.equal(thrown(() => spawn({ title: 'Odd' }, { id: 'not an id' }), EngineError).code, 'invalid_argument');
      const missing = thrown(() => engine.instances.invoke(alice, 'Note', 'n1', 'spawn', { schema: 'Nothing', data: { title: 'x' } }), EngineError);
      assert.deepEqual([missing.code, missing.message], ['not_found', 'schema Nothing has no live version in namespace default']);
      assert.deepEqual(eventsAfter(engine, from), []);
      assert.equal(spawned(engine), 1);
    });

    test("afterChange creates a new instance's children, which commit with it and roll back with it", () => {
      const engine = world();
      publish(engine, 'Folder', [{ name: 'test.Spawner', config: { children: { schema: 'Task', count: 2 } } }]);
      const from = lastCursor(engine);
      engine.instances.create(alice, 'Folder', { title: 'Kitchen' }, { id: 'f1' });
      assert.deepEqual(eventsAfter(engine, from), [
        ['create', 'Task', 'f1-1', 'alice'],
        ['create', 'Task', 'f1-2', 'alice'],
        ['create', 'Folder', 'f1', 'alice'],
      ]);
      assert.deepEqual(fieldsOf(engine.instances.get(alice, 'Task', 'f1-2')), task('Kitchen 2'));

      const failing = thrown(() => engine.instances.create(alice, 'Folder', { title: 'boom' }, { id: 'f2' }), EngineError);
      assert.deepEqual([failing.code, failing.message], ['invalid_argument', 'boom 1 refuses to start']);
      assert.equal(engine.instances.get(alice, 'Folder', 'f2'), undefined);
      assert.deepEqual(tasks(engine), ['f1-1', 'f1-2']);
    });

    test('initialize creates instances that nest, and creates count toward the nesting depth', () => {
      const engine = world();
      engine.instances.create(alice, 'Task', { title: 'nest', depth: 3 }, { id: 't0' });
      assert.deepEqual(tasks(engine), ['t0', 't0.2', 't0.2.1', 't0.2.1.0']);
      assert.deepEqual(fieldsOf(engine.instances.get(alice, 'Task', 't0.2.1.0')), task('nest', { depth: 0 }));
      const deep = thrown(() => engine.instances.create(alice, 'Task', { title: 'nest', depth: 20 }, { id: 'd0' }), BehaviorError);
      assert.equal(deep.message, 'behavior test.Child: instances.create: calls nest more than 16 deep');
      assert.equal(tasks(engine).length, 4);
    });

    test('a guard, a field reader, a read-only operation and a read-only schema-level one cannot create; a writing schema-level one can', () => {
      const engine = world();
      const params = { schema: 'Task', data: { title: 'Paint' } };
      const refusal =
        "behavior test.Spawner: a read cannot create an instance of Task; initialize, afterChange, afterReferenceChange, a writing operation and the runner's work can";
      assert.equal(thrown(() => engine.instances.invoke(alice, 'Note', 'n1', 'spawnRead', params), BehaviorError).message, refusal);
      assert.equal(thrown(() => engine.instances.invokeSchema(alice, 'Note', 'spawnPeek', params), BehaviorError).message, refusal);
      assert.equal(thrown(() => engine.instances.invoke(alice, 'Note', 'n1', 'guarded', params), BehaviorError).message, refusal);
      flags.fieldCreates = true;
      assert.equal(thrown(() => engine.instances.get(alice, 'Note', 'n1'), BehaviorError).message, refusal);
      flags.fieldCreates = false;
      assert.deepEqual(tasks(engine), []);

      assert.deepEqual(engine.instances.invokeSchema(alice, 'Note', 'spawnMany', { schema: 'Task', count: 2 }), ['gen-1', 'gen-2']);
      assert.deepEqual(fieldsOf(engine.instances.get(alice, 'Task', 'gen-2')), task('batch 2'));
    });

    test('a failure the behavior catches rolls back the create alone; one it does not rolls back the whole call', () => {
      const engine = world();
      const from = lastCursor(engine);
      assert.deepEqual(engine.instances.invoke(alice, 'Note', 'n1', 'spawn', { schema: 'Task', data: { title: 'boom' }, id: 'b1', catch: true }), {
        threw: 'EngineError',
        message: 'boom refuses to start',
      });
      assert.equal(spawned(engine), 1);
      assert.equal(engine.instances.get(alice, 'Task', 'b1'), undefined);
      assert.deepEqual(eventsAfter(engine, from), [['operation', 'Note', 'n1', 'spawn']]);

      const after = lastCursor(engine);
      const failure = thrown(() => engine.instances.invoke(alice, 'Note', 'n1', 'spawn', { schema: 'Task', data: { title: 'boom' }, id: 'b1' }), EngineError);
      assert.equal(failure.message, 'boom refuses to start');
      assert.equal(spawned(engine), 1);
      assert.deepEqual(eventsAfter(engine, after), []);
      assert.deepEqual(tasks(engine), []);
    });

    test('it asks the policy for write on the schema, as the caller; a refusal is forbidden', () => {
      const { policy, asked } = recording((request) => !(request.action === 'write' && request.schema === 'Task'));
      const engine = world({ policy });
      asked.length = 0;
      const refused = thrown(() => engine.instances.invoke(bob, 'Note', 'n1', 'spawn', { schema: 'Task', data: { title: 'Paint' }, id: 't1' }), EngineError);
      assert.deepEqual([refused.code, refused.message], ['forbidden', 'bob may not write Task in namespace default']);
      assert.deepEqual(asked, [
        { principal: bob, action: 'write', namespace: 'default', schema: 'Note', operation: 'spawn' },
        { principal: bob, action: 'write', namespace: 'default', schema: 'Task' },
      ]);
      assert.equal(spawned(engine), 0);
      assert.equal((engine.instances.invoke(alice, 'Note', 'n1', 'spawn', { schema: 'Task', data: { title: 'Paint' }, id: 't1' }) as { createdBy: string }).createdBy, 'alice');
    });

    test('creating an instance whose write runs up the call is a cycle; another of the schema is not', () => {
      const engine = world();
      const cycle = thrown(() => engine.instances.invoke(alice, 'Note', 'n1', 'spawn', { schema: 'Note', data: { title: 'Again' }, id: 'n1' }), BehaviorError);
      assert.equal(cycle.message, 'behavior test.Spawner: creating Note n1 is a cycle: a write of Note n1 is still running up this call');
      assert.equal((engine.instances.invoke(alice, 'Note', 'n1', 'spawn', { schema: 'Note', data: { title: 'Second' }, id: 'n2' }) as { id: string }).id, 'n2');
    });

    test("a schema is looked up in the namespace, then the shared one; the instance is the call's namespace's own", () => {
      const engine = open({ namespaces: { names: ['east', 'west', 'library'], shared: 'library' } });
      publish(engine, 'Task', taskBehaviors, 'library');
      publish(engine, 'Note', [{ name: 'test.Spawner' }], 'east');
      engine.instances.create(alice, 'Note', { title: 'East' }, { id: 'n1', namespace: 'east' });
      const made = engine.instances.invoke(alice, 'Note', 'n1', 'spawn', { schema: 'Task', data: { title: 'Paint' }, id: 't1' }, { namespace: 'east' });
      assert.deepEqual([(made as { namespace: string }).namespace, (made as { schemaNamespace: string }).schemaNamespace], ['east', 'library']);
      assert.equal(engine.instances.get(alice, 'Task', 't1', { namespace: 'west' }), undefined);
    });
  });

  describe(`the runner's work creates instances (${driver})`, () => {
    test('a reaction and a schedule create as the runner, and the create events record their cause', () => {
      const clock = testClock(0);
      const engine = track(
        openEngine({
          path: freshPath(),
          driver,
          policy: allowAll,
          clock,
          metaSchema: openMetaSchema(),
          behaviors: [counter, child, planner],
          runner: { principal: runnerPrincipal },
        })
      );
      publish(engine, 'Task', taskBehaviors);
      publish(engine, 'Shelf', [{ name: 'test.Planner' }]);
      const shelf = engine.instances.create(alice, 'Shelf', { title: 'Top' }, { id: 's1' });
      const from = lastCursor(engine);

      assert.deepEqual(engine.runner.runDue(), { handled: 1, skipped: 0, failed: 0, scheduled: 0 });
      const created = engine.events.read(alice, { schema: 'Shelf', instanceId: shelf.id }).events[0];
      assert.deepEqual(eventsAfter(engine, from), [['create', 'Task', 'plan-s1', 'runner', { behavior: 'test.Planner', event: created.cursor, depth: 1 }]]);
      assert.deepEqual(fieldsOf(engine.instances.get(alice, 'Task', 'plan-s1')), task('plan for s1'));

      clock.now = 60_000;
      const before = lastCursor(engine);
      assert.equal(engine.runner.runDue().scheduled, 1);
      assert.deepEqual(eventsAfter(engine, before), [['create', 'Task', 'stock-60000', 'runner', { behavior: 'test.Planner', schedule: 'restock', depth: 1 }]]);
      assert.equal(engine.instances.get(alice, 'Task', 'stock-60000')?.data.title, 'restock 1');
    });
  });
}
