// Registering behavior implementations: when the engine opens or later,
// and what registration refuses.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { defineBehavior, type AnyBehaviorImplementation, type BehaviorDeclaration } from '../dist/index.js';
import { counter, counterDeclaration, flag, openBehaviorEngine, testBehaviors } from './behavior-fixtures.ts';
import { cleanup, clone, drivers, openTestEngine } from './helpers.ts';

afterEach(cleanup);

// The core's behaviors, which every engine registers when it opens.
const CORE = ['Comments', 'Revisions', 'Workflow'];

// refusal registers an implementation with a fresh engine and returns the message it is refused with.
function refusal(implementation: AnyBehaviorImplementation): string {
  const engine = openTestEngine();
  try {
    engine.behaviors.register(implementation);
  } catch (error) {
    assert.ok(error instanceof TypeError, `expected a TypeError, got ${String(error)}`);
    assert.deepEqual(engine.behaviors.names(), CORE);
    return error.message;
  }
  assert.fail('registration succeeded');
}

function withDeclaration(change: (declaration: Record<string, unknown>) => void): AnyBehaviorImplementation {
  const declaration = clone(counterDeclaration) as unknown as Record<string, unknown>;
  change(declaration);
  return { ...counter, declaration: declaration as unknown as BehaviorDeclaration };
}

for (const driver of drivers) {
  describe(`behavior registration (${driver})`, () => {
    test('behaviors register when the engine opens, and later', () => {
      const engine = openTestEngine({ driver, behaviors: [counter] });
      assert.deepEqual(engine.behaviors.names(), [...CORE, 'test.Counter']);
      engine.behaviors.register(flag);
      assert.deepEqual(engine.behaviors.names(), [...CORE, 'test.Counter', 'test.Flag']);
      assert.equal(engine.behaviors.has('test.Flag'), true);
      assert.deepEqual(engine.behaviors.declaration('test.Counter'), counterDeclaration);
      assert.ok(Object.isFrozen(engine.behaviors.declaration('test.Counter')?.operations?.[0]));
      assert.equal(engine.behaviors.declaration('test.Missing'), undefined);
    });

    test('a name registers once', () => {
      const engine = openBehaviorEngine({ driver });
      assert.throws(() => engine.behaviors.register(counter), /behavior test\.Counter is already registered with this engine/);
      assert.throws(() => openTestEngine({ driver, behaviors: [counter, counter] }), /already registered/);
    });

    test('registering stores nothing: storage comes with a published schema', () => {
      const engine = openBehaviorEngine({ driver });
      assert.equal(engine.storage.get('SELECT COUNT(*) AS n FROM engine_behaviors')?.n, 0);
      assert.equal(engine.storage.get("SELECT COUNT(*) AS n FROM engine_migrations WHERE owner <> 'engine'")?.n, 0);
    });
  });
}

describe('behavior registration refuses', () => {
  test('operations that are not exactly the declared ones', () => {
    const { history: _history, ...missing } = counter.operations ?? {};
    assert.match(
      refusal({ ...counter, operations: missing }),
      /^behavior test\.Counter cannot register: its declaration names operations it does not implement: history$/
    );
    assert.match(
      refusal({ ...counter, operations: { ...counter.operations, decrement: () => ({ count: 0 }) } }),
      /it implements operations its declaration does not name: decrement/
    );
    assert.match(refusal({ ...counter, operations: { ...counter.operations, history: 'nope' } } as never), /operations\.history is a function/);
  });

  test('schema-level operations that are not exactly the declared ones, apart from the instance ones', () => {
    // history declared schema-level: its handler belongs in schemaOperations.
    const moved = withDeclaration((d) => ((d.operations as Array<Record<string, unknown>>)[1].scope = 'schema'));
    const message = refusal(moved);
    assert.match(message, /it implements operations its declaration does not name: history/);
    assert.match(message, /its declaration names schemaOperations it does not implement: history/);
    const { history, ...instanceOnly } = counter.operations ?? {};
    assert.doesNotThrow(() => openBehaviorEngine({ behaviors: [{ ...moved, operations: instanceOnly, schemaOperations: { history } } as never] }));
    assert.match(refusal({ ...counter, schemaOperations: { tally: () => 0 } }), /it implements schemaOperations its declaration does not name: tally/);
    assert.match(refusal({ ...counter, guardReference: 'no' } as never), /guardReference is a function/);
    assert.match(refusal({ ...counter, afterReferenceChange: 7 } as never), /afterReferenceChange is a function/);
  });

  test('fields that are not exactly the declared ones', () => {
    assert.match(refusal({ ...counter, fields: {} }), /its declaration names fields it does not implement: count/);
    assert.match(refusal({ ...counter, fields: { ...counter.fields, total: () => 1 } }), /it implements fields its declaration does not name: total/);
    assert.match(refusal({ ...counter, fields: undefined }), /names fields it does not implement: count/);
  });

  test('a declaration the engine cannot take', () => {
    const cases: Array<[(declaration: Record<string, unknown>) => void, RegExp]> = [
      [(d) => (d.name = 'acme.shop.Counter'), /name "acme\.shop\.Counter" must match/],
      [(d) => (d.name = 'test.counter'), /name "test\.counter" must match/],
      [(d) => (d.colour = 'red'), /the declaration has the unknown key "colour"/],
      [
        (d) => ((d.operations as Array<Record<string, unknown>>)[0].name = 'create'),
        /operation create has the name of an operation every schema has \(create, get, list, update, delete\)/,
      ],
      [(d) => ((d.operations as Array<Record<string, unknown>>)[1].name = 'increment'), /operation increment is declared twice/],
      [(d) => ((d.operations as Array<Record<string, unknown>>)[0].name = 'Increment'), /"Increment" is not camelCase/],
      [
        (d) => delete ((d.operations as Array<Record<string, unknown>>)[0].paramsSchema as Record<string, unknown>).additionalProperties,
        /operation increment paramsSchema must set "additionalProperties": false/,
      ],
      [(d) => ((d.operations as Array<Record<string, unknown>>)[0].paramsSchema = { type: 'array' }), /paramsSchema must be an object schema/],
      [(d) => delete (d.operations as Array<Record<string, unknown>>)[0].resultSchema, /operation increment has no resultSchema/],
      [(d) => ((d.operations as Array<Record<string, unknown>>)[0].writes = 'yes'), /operation increment writes is a boolean/],
      [(d) => ((d.operations as Array<Record<string, unknown>>)[0].scope = 'type'), /operation increment scope "type" is not "instance" or "schema"/],
      [(d) => ((d.fields as Array<Record<string, unknown>>)[0].name = 'the count'), /fields\[0\]\.name "the count" is not an identifier/],
      [(d) => (d.fields = [{ name: 'count' }, { name: 'count' }]), /field count is declared twice/],
      [(d) => (d.requires = ['test.Counter']), /requires names the behavior itself/],
      [(d) => ((d.requires = ['test.Flag']), (d.conflicts = ['test.Flag'])), /it both requires and conflicts with test\.Flag/],
      [(d) => (d.configSchema = { type: 'object', properties: { start: { type: 'nonsense' } } }), /configSchema does not compile/],
    ];
    for (const [change, expected] of cases) {
      assert.match(refusal(withDeclaration(change)), expected);
    }
  });

  test('malformed migrations, columns and hooks', () => {
    const migrations = counter.migrations ?? [];
    assert.match(refusal({ ...counter, migrations: [migrations[1], migrations[0]] }), /position 1 holds version 2/);
    assert.match(refusal({ ...counter, migrations: [{ version: 1, name: '' }] }), /migration 1 needs a name/);
    assert.match(
      refusal({ ...counter, migrations: [{ version: 1, name: 'count', columns: { count: { type: 'integer', notNull: true } } }] }),
      /column count is NOT NULL, so it needs a default for the instances that exist/
    );
    assert.match(refusal({ ...counter, migrations: [{ version: 1, name: 'count', columns: { Count: { type: 'integer' } } }] }), /a column name matches/);
    assert.match(
      refusal({ ...counter, migrations: [{ version: 1, name: 'count', columns: { count: { type: 'integer', default: 'zero' } } }] }),
      /a default of "zero" does not fit type integer/
    );
    assert.match(
      refusal({
        ...counter,
        migrations: [migrations[0], { version: 2, name: 'again', columns: { count: { type: 'integer' } } }],
      }),
      /migration 2 adds column count, which an earlier migration added/
    );
    assert.match(refusal({ ...counter, guard: 'no' } as never), /guard is a function/);
    assert.match(refusal({ declaration: 'nope' } as never), /a behavior declaration is a JSON object/);
  });

  test('every problem is named at once', () => {
    const message = refusal({ ...counter, fields: {}, operations: {} });
    assert.match(message, /fields it does not implement: count/);
    assert.match(message, /operations it does not implement: increment, history/);
  });

  test('defineBehavior returns the implementation it is given', () => {
    const implementation = { declaration: { name: 'test.Nothing' } };
    assert.equal(defineBehavior(implementation), implementation);
    const engine = openTestEngine({ behaviors: [...testBehaviors, implementation] });
    assert.deepEqual(engine.behaviors.names(), [...CORE, 'test.Counter', 'test.Flag', 'test.Nothing', 'test.Tally']);
  });
});
