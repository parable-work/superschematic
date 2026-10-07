// The value store (D16, amended: a large value is stored once): a
// top-level member whose JSON is longer than the threshold is stored once
// by the SHA-256 of its canonical JSON, and the row, the event and a
// behavior's row (Revisions') keep a ref in its place. Every read puts the
// value back: get, list, an operation's view and context, a field reader,
// another behavior's read, validation (Variants), Search's index and
// before(). An event read returns the refs, as does a get or a list that
// asks for valueRefs; a value is read by its hash only through a schema of
// the namespace the caller may read that references it. A write that
// leaves a large field alone neither hashes nor rewrites it, and an
// operation that never reads the own fields loads none. A value goes when
// its last holder does.
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { afterEach, describe, test } from 'node:test';

import type { Authenticator } from '@superschematic/http-runtime';

import {
  BehaviorError,
  EngineError,
  InstanceValidationError,
  ValueTooLargeError,
  allowAll,
  canonicalJSON,
  defineBehavior,
  openEngine,
  type AccessPolicy,
  type Engine,
  type EngineEvent,
  type EngineOptions,
  type FrozenJSON,
  type Principal,
  type ValueDriver,
} from '../dist/index.js';
import { engineApp } from '../dist/http/index.js';
import { counter, openMetaSchema } from './behavior-fixtures.ts';
import { alice, cleanup, clone, drivers, freshPath, openTestEngine, schemaDocument, stepsDocument, thrown, track } from './helpers.ts';
import { ledger, probe, resetProbe, runnerPrincipal } from './runner-fixtures.ts';

afterEach(() => {
  resetProbe();
  cleanup();
});

/** The tests' threshold: the least an engine takes. */
const THRESHOLD = 1024;

interface Check {
  name: string;
  ok: boolean;
}

/** verify is a VerifyResult of n checks: about 30 bytes of JSON each. */
function verify(n: number, seed = 'check'): { passed: boolean; checks: Check[] } {
  return { passed: true, checks: Array.from({ length: n }, (_, index) => ({ name: `${seed}-${index}`, ok: index % 2 === 0 })) };
}

function hashOf(value: unknown): string {
  return createHash('sha256').update(canonicalJSON(value), 'utf8').digest('hex');
}

function refOf(value: unknown): { $value: string; bytes: number } {
  return { $value: hashOf(value), bytes: Buffer.byteLength(canonicalJSON(value), 'utf8') };
}

// test.Shelf keeps documents in its own table through the value store,
// from a schema-level operation that appends no event, and reads the
// instance's own fields from a field reader, an operation and another
// instance; record writes a result with update(), as Retries' does.
const shelf = defineBehavior({
  declaration: {
    name: 'test.Shelf',
    description: 'Keeps documents through the value store and reads the instance.',
    fields: [{ name: 'seen', description: 'How many checks the result holds, as a field reader sees it.' }],
    operations: [
      {
        name: 'record',
        description: "Writes a result, as an operation's update().",
        paramsSchema: { type: 'object', additionalProperties: false, required: ['result'], properties: { result: {} } },
        resultSchema: true,
        writes: true,
      },
      {
        name: 'inspect',
        description: "How many checks the result holds, as an operation's context sees it.",
        paramsSchema: { type: 'object', additionalProperties: false },
        resultSchema: true,
      },
      {
        name: 'peek',
        description: 'How many checks another step holds, as a read of it sees it.',
        paramsSchema: { type: 'object', additionalProperties: false, required: ['id'], properties: { id: { type: 'string' } } },
        resultSchema: true,
      },
      {
        name: 'keep',
        description: 'Keeps a document under a key.',
        scope: 'schema',
        paramsSchema: {
          type: 'object',
          additionalProperties: false,
          required: ['key', 'doc'],
          properties: { key: { type: 'string' }, doc: {}, fail: { type: 'boolean' } },
        },
        resultSchema: true,
        writes: true,
      },
      {
        name: 'drop',
        description: 'Drops the document under a key.',
        scope: 'schema',
        paramsSchema: { type: 'object', additionalProperties: false, required: ['key'], properties: { key: { type: 'string' } } },
        resultSchema: true,
        writes: true,
      },
      {
        name: 'take',
        description: 'Reads the document under a key.',
        scope: 'schema',
        paramsSchema: {
          type: 'object',
          additionalProperties: false,
          required: ['key'],
          properties: { key: { type: 'string' }, stow: { type: 'boolean' } },
        },
        resultSchema: true,
      },
    ],
  },
  migrations: [
    {
      version: 1,
      name: 'shelf',
      up(sql) {
        sql.run(`CREATE TABLE ${sql.table('shelf')} (
          namespace  TEXT NOT NULL,
          schema     TEXT NOT NULL,
          key        TEXT NOT NULL,
          doc        TEXT NOT NULL,
          value_refs TEXT,
          PRIMARY KEY (namespace, schema, key)
        ) STRICT`);
      },
    },
  ],
  configChange: () => undefined,
  fields: {
    seen: (view) => ((view.data.result as { checks?: unknown[] } | undefined)?.checks ?? []).length,
  },
  operations: {
    record(context, params) {
      context.update({ result: params.result } as FrozenJSON);
      return null;
    },
    inspect(context) {
      return ((context.data.result as { checks?: unknown[] } | undefined)?.checks ?? []).length;
    },
    peek(context, params) {
      const other = context.instances.get(context.schema, params.id as string);
      return ((other?.data.result as { checks?: unknown[] } | undefined)?.checks ?? []).length;
    },
  },
  schemaOperations: {
    keep(context, params) {
      const stowed = context.values.stow(params.key as string, { doc: params.doc } as FrozenJSON);
      context.sql.run(
        `INSERT INTO ${context.sql.table('shelf')} (namespace, schema, key, doc, value_refs) VALUES (?, ?, ?, ?, ?)
         ON CONFLICT (namespace, schema, key) DO UPDATE SET doc = excluded.doc, value_refs = excluded.value_refs`,
        [context.namespace, context.schema, params.key as string, stowed.json, stowed.refs]
      );
      if (params.fail === true) {
        throw new Error('the keep fails after it stowed');
      }
      return null;
    },
    drop(context, params) {
      context.sql.run(`DELETE FROM ${context.sql.table('shelf')} WHERE namespace = ? AND schema = ? AND key = ?`, [
        context.namespace,
        context.schema,
        params.key as string,
      ]);
      context.values.release(params.key as string);
      return null;
    },
    take(context, params) {
      if (params.stow === true) {
        context.values.stow(params.key as string, {});
      }
      const row = context.sql.get(`SELECT doc, value_refs FROM ${context.sql.table('shelf')} WHERE namespace = ? AND schema = ? AND key = ?`, [
        context.namespace,
        context.schema,
        params.key as string,
      ]);
      return row === undefined ? null : context.values.load(String(row.doc), row.value_refs as string | null).doc;
    },
  },
});

type Behaviors = Array<{ name: string; config?: unknown }>;

/** steps is the Variants fixture's Step, with a body field and more behaviors after its own. */
function steps(behaviors: Behaviors = []): Record<string, unknown> {
  const document = clone(stepsDocument()) as { types: { Step: { behaviors: Behaviors; fields: Array<Record<string, unknown>> } } };
  document.types.Step.fields.push({ name: 'body', typeRef: { name: 'string' } });
  document.types.Step.behaviors.push(...behaviors);
  return document as unknown as Record<string, unknown>;
}

const REVISIONS = { name: 'Revisions', config: { review: { permission: 'steps.review' } } };
const SEARCH = { name: 'Search', config: { fields: ['title', 'body'] } };
const reviewer: Principal = { subject: 'rae', permissions: ['steps.review'] };

function open(driver: (typeof drivers)[number], options: Partial<EngineOptions> = {}): Engine {
  return openTestEngine({
    driver,
    metaSchema: openMetaSchema(),
    behaviors: [shelf, counter, ledger],
    runner: { principal: runnerPrincipal },
    ...options,
    values: { thresholdBytes: THRESHOLD, ...options.values },
  });
}

function publish(engine: Engine, document: Record<string, unknown>, namespace?: string): void {
  engine.schemas.define(alice, document, { namespace });
  engine.schemas.publish(alice, document.name as string, { namespace });
}

/** The row of a step as stored: its data, parsed, its value_refs and the length of its data. */
function rowOf(engine: Engine, id: string, namespace = 'default'): { data: Record<string, unknown>; refs: unknown; length: number } {
  const row = engine.storage.get("SELECT data, value_refs, length(data) AS length FROM engine_instances WHERE namespace = ? AND schema = 'Step' AND id = ?", [
    namespace,
    id,
  ]);
  assert.ok(row, `no row of Step ${id}`);
  return { data: JSON.parse(String(row.data)) as Record<string, unknown>, refs: row.value_refs === null ? null : JSON.parse(String(row.value_refs)), length: Number(row.length) };
}

/** The values engine_payloads holds, by hash. */
function payloads(engine: Engine): string[] {
  return engine.storage.all('SELECT hash FROM engine_payloads ORDER BY hash').map((row) => String(row.hash));
}

/** Who holds a value: [namespace, schema, holder, id, key], sorted. */
function holders(engine: Engine, value: unknown): string[][] {
  return engine.storage
    .all('SELECT namespace, schema, holder, id, key FROM engine_payload_holders WHERE hash = ? ORDER BY namespace, schema, holder, id, key', [hashOf(value)])
    .map((row) => [String(row.namespace), String(row.schema), String(row.holder), String(row.id), String(row.key)]);
}

function events(engine: Engine, id: string): EngineEvent[] {
  return engine.events.read(alice, { schema: 'Step', instanceId: id }).events;
}

/** A value driver over a Map that counts its calls, outside the engine's transactions, as a driver over other storage is. */
class MapDriver implements ValueDriver {
  readonly transactional = false;
  readonly values = new Map<string, string>();
  reads = 0;
  writes = 0;
  removes = 0;

  read(hash: string): string | undefined {
    this.reads += 1;
    return this.values.get(hash);
  }

  write(hash: string, json: string): void {
    this.writes += 1;
    if (!this.values.has(hash)) {
      this.values.set(hash, json);
    }
  }

  remove(hash: string): void {
    this.removes += 1;
    this.values.delete(hash);
  }
}

for (const driver of drivers) {
  describe(`the value store (${driver})`, () => {
    test('a field whose JSON is longer than the threshold is stored once by hash, and the row keeps a ref in its place', () => {
      const engine = open(driver);
      publish(engine, steps());
      const result = verify(60);
      assert.ok(canonicalJSON(result).length > THRESHOLD);
      const created = engine.instances.create(alice, 'Step', { title: 'Check', kind: 'verify', result }, { id: 's1' });
      assert.deepEqual(created.data.result, result);
      assert.equal(created.valueRefs, undefined);

      const row = rowOf(engine, 's1');
      assert.deepEqual(row.data, { title: 'Check', kind: 'verify', result: refOf(result) });
      assert.deepEqual(row.refs, ['/result']);
      assert.ok(row.length < 200, `the row holds ${row.length} characters`);
      assert.deepEqual(payloads(engine), [hashOf(result)]);
      assert.equal(engine.storage.get('SELECT value FROM engine_payloads')?.value, canonicalJSON(result));
    });

    test('the threshold counts UTF-8 bytes: a field at it stays in the row, one byte past it goes to the store', () => {
      const engine = open(driver);
      publish(engine, steps());
      const cases: Array<[string, string, boolean]> = [
        ['at', 'a'.repeat(THRESHOLD - 2), false],
        ['past', 'a'.repeat(THRESHOLD - 1), true],
        ['at-wide', '\u00e9'.repeat((THRESHOLD - 2) / 2), false],
        ['past-wide', `${'\u00e9'.repeat((THRESHOLD - 2) / 2)}a`, true],
      ];
      for (const [id, body, stored] of cases) {
        engine.instances.create(alice, 'Step', { title: id, kind: 'note', body }, { id });
        assert.deepEqual(rowOf(engine, id).refs, stored ? ['/body'] : null, id);
        assert.equal(engine.instances.get(alice, 'Step', id)?.data.body, body);
      }
    });

    test('the default threshold is 64 KiB, and an engine refuses a threshold under 1 KiB', () => {
      const engine = track(openEngine({ path: freshPath(), driver, policy: allowAll }));
      assert.equal(engine.values.thresholdBytes, 64 * 1024);
      publish(engine, clone(stepsDocument()));
      const under = verify(1900);
      const over = verify(2300);
      assert.ok(canonicalJSON(under).length < 64 * 1024 && canonicalJSON(over).length > 64 * 1024);
      engine.instances.create(alice, 'Step', { title: 'Under', kind: 'verify', result: under }, { id: 'under' });
      engine.instances.create(alice, 'Step', { title: 'Over', kind: 'verify', result: over }, { id: 'over' });
      assert.deepEqual(rowOf(engine, 'under').refs, null);
      assert.deepEqual(rowOf(engine, 'over').refs, ['/result']);
      assert.deepEqual(engine.instances.get(alice, 'Step', 'over')?.data.result, over);
      for (const thresholdBytes of [1023, 0, 1.5]) {
        assert.throws(() => openEngine({ path: freshPath(), driver, policy: allowAll, values: { thresholdBytes } }), /thresholdBytes is an integer of at least 1024/);
      }
    });

    test('reads put the value back: get, list, an operation, a field reader and a read of another instance; valueRefs returns the refs', () => {
      const engine = open(driver);
      publish(engine, steps([{ name: 'test.Shelf' }]));
      const result = verify(60);
      engine.instances.create(alice, 'Step', { title: 'Big', kind: 'verify', result }, { id: 's1' });
      engine.instances.create(alice, 'Step', { title: 'Small', kind: 'verify', result: verify(2) }, { id: 's2' });

      const got = engine.instances.get(alice, 'Step', 's1');
      assert.deepEqual(got?.data.result, result);
      assert.equal(got?.data.seen, 60);
      // A caller's record is its own to change.
      (got?.data.result as { checks: Check[] }).checks.length = 0;
      assert.deepEqual(engine.instances.get(alice, 'Step', 's1')?.data.result, result);

      const page = engine.instances.list(alice, 'Step');
      assert.deepEqual(page.items[0].data.result, result);
      assert.equal(page.items[0].valueRefs, undefined);
      assert.equal(engine.instances.invoke(alice, 'Step', 's1', 'inspect'), 60);
      assert.equal(engine.instances.invoke(alice, 'Step', 's2', 'peek', { id: 's1' }), 60);

      const refs = engine.instances.list(alice, 'Step', { valueRefs: true });
      assert.deepEqual(refs.items[0].data.result, refOf(result));
      assert.deepEqual(refs.items[0].valueRefs, ['/result']);
      assert.equal(refs.items[0].data.seen, 60);
      assert.deepEqual(refs.items[1].data.result, verify(2));
      assert.equal(refs.items[1].valueRefs, undefined);
      const one = engine.instances.get(alice, 'Step', 's1', { valueRefs: true });
      assert.deepEqual(one?.data.result, refOf(result));
      assert.deepEqual(one?.valueRefs, ['/result']);
    });

    test('validation sees the value: Variants holds a large result to its type, and an update of another field validates the whole instance', () => {
      const engine = open(driver);
      publish(engine, steps());
      const broken = verify(60);
      delete (broken.checks[59] as Partial<Check>).ok;
      const refused = thrown(() => engine.instances.create(alice, 'Step', { title: 'Check', kind: 'verify', result: broken }), InstanceValidationError);
      assert.deepEqual(
        refused.issues.map((issue) => issue.path),
        ['result.checks[59].ok']
      );

      const result = verify(60);
      engine.instances.create(alice, 'Step', { title: 'Check', kind: 'verify', result }, { id: 's1' });
      assert.equal(engine.instances.update(alice, 'Step', 's1', { title: 'Renamed' }).data.title, 'Renamed');
      assert.deepEqual(engine.instances.get(alice, 'Step', 's1')?.data.result, result);
      // A merge into the large result is checked as the whole result.
      const merged = thrown(() => engine.instances.update(alice, 'Step', 's1', { result: { approved: true } }), InstanceValidationError);
      assert.ok(merged.issues.some((issue) => issue.path === 'result.approved'), JSON.stringify(merged.issues));
      assert.deepEqual(engine.instances.get(alice, 'Step', 's1')?.data.result, result);
    });

    test("Search indexes a large field at a write, and at a publish that adds Search to a schema with instances", () => {
      const engine = open(driver);
      publish(engine, steps([SEARCH]));
      const body = `${'lorem ipsum '.repeat(120)}zephyrine`;
      engine.instances.create(alice, 'Step', { title: 'Long', kind: 'note', body }, { id: 's1' });
      assert.deepEqual(rowOf(engine, 's1').refs, ['/body']);
      const hits = (query: string, namespace?: string) =>
        (engine.instances.invokeSchema(alice, 'Step', 'search', { query }, { namespace }) as { items: Array<{ id: string }> }).items.map((hit) => hit.id);
      assert.deepEqual(hits('zephyrine'), ['s1']);
      engine.instances.update(alice, 'Step', 's1', { title: 'Longer' });
      assert.deepEqual(hits('zephyrine longer'), ['s1']);

      const later = open(driver);
      publish(later, steps());
      later.instances.create(alice, 'Step', { title: 'Long', kind: 'note', body }, { id: 's1' });
      publish(later, steps([SEARCH]));
      assert.deepEqual(
        (later.instances.invokeSchema(alice, 'Step', 'search', { query: 'zephyrine' }) as { items: Array<{ id: string }> }).items.map((hit) => hit.id),
        ['s1']
      );
    });

    test("the log keeps refs: a create's instance, an update's patch, an operation's params and patch; before() puts the values back", () => {
      const engine = open(driver);
      publish(engine, steps([{ name: 'test.Shelf' }, { name: 'test.Ledger' }]));
      // third differs from second in every member, so the operation's
      // patch of the result is the whole of third, as its params are.
      const [first, second, third] = [verify(60, 'a'), verify(60, 'b'), { ...verify(60, 'c'), passed: false }];
      engine.instances.create(alice, 'Step', { title: 'Check', kind: 'verify', result: first }, { id: 's1' });
      engine.instances.update(alice, 'Step', 's1', { result: second });
      engine.instances.invoke(alice, 'Step', 's1', 'record', { result: third });

      const [create, update, operation] = events(engine, 's1');
      assert.deepEqual(create.change, { title: 'Check', kind: 'verify', result: refOf(first), seen: 60 });
      assert.deepEqual(create.valueRefs, ['/result']);
      assert.deepEqual(update.change, { result: refOf(second) });
      assert.deepEqual(update.valueRefs, ['/result']);
      assert.deepEqual(operation.change, { behavior: 'test.Shelf', operation: 'record', params: { result: refOf(third) }, patch: { result: refOf(third) } });
      assert.deepEqual(operation.valueRefs, ['/params/result', '/patch/result']);
      const longest = Number(engine.storage.get("SELECT max(length(change)) AS longest FROM engine_events WHERE schema = 'Step' AND instance_id IS NOT NULL")?.longest);
      assert.ok(longest < 400, `the longest change holds ${longest} characters`);
      // The value the operation's params and patch name is stored once.
      assert.deepEqual(payloads(engine), [first, second, third].map(hashOf).sort());

      // A reaction gets the event as the log keeps it; before() has the values.
      const seen: Array<[string, unknown, unknown]> = [];
      probe.react = (context, event) => {
        if (event.cause === undefined) {
          seen.push([event.kind, event.valueRefs, (context.before(event) as { result?: unknown } | undefined)?.result]);
        }
      };
      engine.runner.runDue();
      assert.deepEqual(seen, [
        ['create', ['/result'], undefined],
        ['update', ['/result'], first],
        ['operation', ['/params/result', '/patch/result'], second],
      ]);
    });

    test("Revisions keeps refs in its revisions and proposals, and reads them back whole", () => {
      const engine = open(driver);
      publish(engine, steps([REVISIONS]));
      const [first, second, third] = [verify(60, 'a'), verify(60, 'b'), { ...verify(60, 'c'), passed: false }];
      engine.instances.create(alice, 'Step', { title: 'Check', kind: 'verify', result: first }, { id: 's1' });
      engine.instances.update(alice, 'Step', 's1', { title: 'Renamed' });
      engine.instances.update(alice, 'Step', 's1', { result: second });
      const proposal = engine.instances.invoke(alice, 'Step', 's1', 'propose', { patch: { result: third } }) as { patch: { result: unknown } };
      assert.deepEqual(proposal.patch.result, third);

      const revisions = (engine.instances.invoke(alice, 'Step', 's1', 'listRevisions') as { items: Array<{ data: { result: unknown } }> }).items;
      assert.deepEqual(
        revisions.map((revision) => revision.data.result),
        [first, first, second]
      );
      const rows = engine.storage.all('SELECT data, value_refs FROM bhv_revisions__revisions ORDER BY revision');
      assert.deepEqual(
        rows.map((row) => [(JSON.parse(String(row.data)) as { result: unknown }).result, row.value_refs]),
        [
          [refOf(first), '["/result"]'],
          [refOf(first), '["/result"]'],
          [refOf(second), '["/result"]'],
        ]
      );
      const stored = engine.storage.get('SELECT patch, value_refs FROM bhv_revisions__proposals');
      assert.deepEqual(JSON.parse(String(stored?.patch)), { result: refOf(third) });
      assert.equal(stored?.value_refs, '["/result"]');
      assert.deepEqual(
        (engine.instances.invoke(alice, 'Step', 's1', 'listProposals') as { items: Array<{ patch: unknown }> }).items.map((item) => item.patch),
        [{ result: third }]
      );

      engine.instances.invoke(reviewer, 'Step', 's1', 'approve', { proposal: 1 });
      assert.deepEqual(engine.instances.get(alice, 'Step', 's1')?.data.result, third);
      assert.deepEqual(
        (engine.instances.invoke(alice, 'Step', 's1', 'listRevisions') as { items: Array<{ data: { result: unknown } }> }).items.map((revision) => revision.data.result),
        [first, first, second, third]
      );
      // Each value is stored once, whoever holds it; the fourth is the
      // propose event's params.patch, a value of its own.
      assert.deepEqual(payloads(engine), [first, second, third, { result: third }].map(hashOf).sort());
      assert.deepEqual(holders(engine, first), [
        ['default', 'Step', 'Revisions', 's1', 'revision 1'],
        ['default', 'Step', 'Revisions', 's1', 'revision 2'],
        ['default', 'Step', 'event', 's1', String(events(engine, 's1')[0].cursor)],
      ]);

      // A delete releases the revisions' holds; the log still holds the values.
      engine.instances.delete(alice, 'Step', 's1');
      assert.deepEqual(
        engine.storage.all("SELECT DISTINCT holder FROM engine_payload_holders ORDER BY holder").map((row) => String(row.holder)),
        ['event']
      );
      assert.equal(payloads(engine).length, 4);
    });

    test('one value written twice, to two instances and in two namespaces, is stored once', () => {
      const engine = open(driver, { namespaces: { names: ['east', 'west'] } });
      const result = verify(60);
      for (const namespace of ['east', 'west']) {
        publish(engine, steps(), namespace);
        engine.instances.create(alice, 'Step', { title: 'One', kind: 'verify', result }, { id: 's1', namespace });
        // The same value with its members in another order: one canonical JSON.
        engine.instances.create(alice, 'Step', { title: 'Two', kind: 'verify', result: { checks: clone(result.checks), passed: result.passed } }, { id: 's2', namespace });
      }
      engine.instances.update(alice, 'Step', 's1', { result: clone(result) }, { namespace: 'east' });
      assert.deepEqual(payloads(engine), [hashOf(result)]);
      assert.deepEqual(
        holders(engine, result).filter((holder) => holder[2] === 'instance'),
        [
          ['east', 'Step', 'instance', 's1', ''],
          ['east', 'Step', 'instance', 's2', ''],
          ['west', 'Step', 'instance', 's1', ''],
          ['west', 'Step', 'instance', 's2', ''],
        ]
      );
    });

    test('a write that leaves the large field alone neither rewrites nor loads it: an operation that writes only its columns, as a heartbeat does', () => {
      const values = new MapDriver();
      const engine = open(driver, { values: { driver: values, cacheBytes: 0 } });
      publish(engine, steps([{ name: 'test.Counter' }]));
      const result = verify(60);
      engine.instances.create(alice, 'Step', { title: 'Check', kind: 'verify', result }, { id: 's1' });
      const stored = rowOf(engine, 's1');
      assert.deepEqual([values.reads, values.writes], [0, 1]);

      for (let beat = 0; beat < 3; beat += 1) {
        engine.instances.invoke(alice, 'Step', 's1', 'increment');
      }
      assert.deepEqual(rowOf(engine, 's1'), stored);
      assert.deepEqual([values.reads, values.writes], [0, 1]);
      // An update of another field reuses the ref: nothing is hashed or written again.
      engine.instances.update(alice, 'Step', 's1', { title: 'Renamed' });
      assert.deepEqual(rowOf(engine, 's1').data.result, refOf(result));
      assert.equal(values.writes, 1);
      const got = engine.instances.get(alice, 'Step', 's1');
      assert.equal(got?.data.count, 3);
      assert.deepEqual(got?.data.result, result);
    });

    test('the engine keeps the values it read last in memory, up to cacheBytes', () => {
      const values = new MapDriver();
      const engine = open(driver, { values: { driver: values } });
      publish(engine, steps());
      engine.instances.create(alice, 'Step', { title: 'Check', kind: 'verify', result: verify(60) }, { id: 's1' });
      engine.instances.get(alice, 'Step', 's1');
      engine.instances.get(alice, 'Step', 's1');
      engine.instances.list(alice, 'Step');
      assert.equal(values.reads, 1);
    });

    test('a value is read by its hash only through a schema of the namespace the caller may read that references it', () => {
      const policy: AccessPolicy = ({ principal, action, namespace, schema }) =>
        principal.subject === 'alice' || (principal.subject === 'reader' && action === 'read' && namespace === 'default' && schema === 'Step') || (principal.subject === 'stranger' && schema === 'Other');
      const reader: Principal = { subject: 'reader', permissions: [] };
      const stranger: Principal = { subject: 'stranger', permissions: [] };
      const engine = open(driver, { policy, namespaces: { names: ['east'] } });
      publish(engine, steps());
      publish(engine, schemaDocument('Other', [{ name: 'title', typeRef: { name: 'string' } }]));
      publish(engine, steps(), 'east');
      const [held, moved, eastern] = [verify(60, 'held'), verify(60, 'moved'), verify(60, 'east')];
      engine.instances.create(alice, 'Step', { title: 'Check', kind: 'verify', result: held }, { id: 's1' });
      engine.instances.create(alice, 'Step', { title: 'East', kind: 'verify', result: eastern }, { id: 'e1', namespace: 'east' });

      assert.deepEqual(engine.values.get(alice, hashOf(held)), { hash: hashOf(held), bytes: refOf(held).bytes, value: held });
      assert.deepEqual(engine.values.get(reader, hashOf(held)).value, held);
      const unknown = createHash('sha256').update('nothing').digest('hex');
      const refusals = [
        // Knowing the hash grants nothing: a schema the caller may not read
        // references it, and the refusal is a missing value's.
        [stranger, hashOf(held), 'default'],
        [alice, unknown, 'default'],
        // A value another namespace holds is not this one's, whoever asks.
        [alice, hashOf(eastern), 'default'],
        [alice, hashOf(held), 'east'],
        [reader, hashOf(eastern), 'east'],
      ] as const;
      for (const [principal, hash, namespace] of refusals) {
        const error = thrown(() => engine.values.get(principal, hash, { namespace }), EngineError);
        assert.equal(error.code, 'not_found', `${principal.subject} ${namespace}`);
        assert.equal(error.message, `namespace ${namespace} holds no value ${hash} that ${principal.subject} may read`);
      }
      assert.deepEqual(engine.values.get(alice, hashOf(eastern), { namespace: 'east' }).value, eastern);
      assert.equal(thrown(() => engine.values.get(alice, 'ABC'), EngineError).code, 'invalid_argument');

      // A value only the log holds now is still read through its schema.
      engine.instances.update(alice, 'Step', 's1', { result: moved });
      assert.deepEqual(engine.values.get(reader, hashOf(held)).value, held);
      engine.instances.delete(alice, 'Step', 's1');
      assert.deepEqual(engine.values.get(reader, hashOf(moved)).value, moved);
    });

    test('the route and the MCP tool read a value by its hash, and a list or a get with valueRefs returns refs', async () => {
      const authenticate: Authenticator = async (ctx) =>
        ctx.bearerToken === undefined ? null : { subject: ctx.bearerToken, permissions: [] };
      const policy: AccessPolicy = ({ principal, schema }) => principal.subject === 'alice' || (principal.subject === 'reader' && schema === 'Step');
      const engine = open(driver, { policy });
      publish(engine, steps());
      publish(engine, schemaDocument('Other', [{ name: 'title', typeRef: { name: 'string' } }]));
      const result = verify(60);
      engine.instances.create(alice, 'Step', { title: 'Check', kind: 'verify', result }, { id: 's1' });
      const app = engineApp(engine, { authenticate });
      const get = async (path: string, token: string) => {
        const response = await app.request(path, { headers: { authorization: `Bearer ${token}` } });
        return { status: response.status, body: (await response.json()) as { data?: unknown; code?: string } };
      };
      const hash = hashOf(result);
      const read = await get(`/namespaces/default/values/${hash}`, 'reader');
      assert.deepEqual([read.status, read.body.data], [200, { hash, bytes: refOf(result).bytes, value: result }]);
      const refused = await get(`/namespaces/default/values/${hash}`, 'stranger');
      assert.deepEqual([refused.status, refused.body.code], [404, 'not_found']);
      assert.equal((await get('/namespaces/default/values/abc', 'reader')).status, 400);

      const listed = await get('/namespaces/default/schemas/Step/instances?valueRefs=true', 'reader');
      const item = (listed.body.data as { items: Array<{ data: { result: unknown }; valueRefs?: string[] }> }).items[0];
      assert.deepEqual([item.data.result, item.valueRefs], [refOf(result), ['/result']]);
      const one = await get('/namespaces/default/schemas/Step/instances/s1?valueRefs=true', 'reader');
      assert.deepEqual((one.body.data as { data: { result: unknown } }).data.result, refOf(result));
      const whole = await get('/namespaces/default/schemas/Step/instances/s1', 'reader');
      assert.deepEqual((whole.body.data as { data: { result: unknown } }).data.result, result);

      assert.ok(engine.tools.manifest(alice).tools.some((tool) => tool.name === 'engine.getValue' && tool.mcp.hidden === false));
      assert.deepEqual(engine.tools.call({ subject: 'reader', permissions: [] }, 'get_value', { hash }), { hash, bytes: refOf(result).bytes, value: result });
      assert.equal(thrown(() => engine.tools.call({ subject: 'stranger', permissions: [] }, 'get_value', { hash }), EngineError).code, 'not_found');
      assert.deepEqual(
        (engine.tools.call(alice, 'step_list', { valueRefs: true }) as { items: Array<{ valueRefs?: string[] }> }).items[0].valueRefs,
        ['/result']
      );
      assert.deepEqual((engine.tools.call(alice, 'step_get', { id: 's1', valueRefs: true }) as { data: { result: unknown } }).data.result, refOf(result));
      assert.equal(thrown(() => engine.tools.call(alice, 'step_get', { id: 's1', valueRefs: 'yes' }), EngineError).code, 'invalid_argument');
    });

    test('a value goes when its last holder does: a row of a behavior that no event holds, and not while the log holds one', () => {
      const engine = open(driver);
      publish(engine, steps([{ name: 'test.Shelf' }]));
      const [x, y, z] = [verify(60, 'x'), verify(60, 'y'), verify(60, 'z')];
      const keep = (key: string, doc: unknown, fail = false) => engine.instances.invokeSchema(alice, 'Step', 'keep', { key, doc, fail });
      const drop = (key: string) => engine.instances.invokeSchema(alice, 'Step', 'drop', { key });

      keep('a', x);
      keep('b', x);
      assert.deepEqual(payloads(engine), [hashOf(x)]);
      assert.deepEqual(holders(engine, x), [
        ['default', 'Step', 'test.Shelf', '', 'a'],
        ['default', 'Step', 'test.Shelf', '', 'b'],
      ]);
      assert.deepEqual(engine.instances.invokeSchema(alice, 'Step', 'take', { key: 'a' }), x);
      drop('a');
      assert.deepEqual(payloads(engine), [hashOf(x)]);
      drop('b');
      assert.deepEqual(payloads(engine), []);

      // Stowing again for a key drops what it held before.
      keep('a', x);
      keep('a', y);
      assert.deepEqual(payloads(engine), [hashOf(y)]);
      // A write that fails leaves what it stowed and dropped as it was.
      assert.throws(() => keep('a', z, true), /the keep fails after it stowed/);
      assert.deepEqual(payloads(engine), [hashOf(y)]);
      assert.deepEqual(engine.instances.invokeSchema(alice, 'Step', 'take', { key: 'a' }), y);
      // A read-only operation stows nothing.
      assert.throws(() => engine.instances.invokeSchema(alice, 'Step', 'take', { key: 'a', stow: true }), BehaviorError);

      // An instance's old value stays while an event holds it.
      engine.instances.create(alice, 'Step', { title: 'Check', kind: 'verify', result: x }, { id: 's1' });
      engine.instances.update(alice, 'Step', 's1', { result: z });
      assert.deepEqual(payloads(engine), [x, y, z].map(hashOf).sort());
      assert.deepEqual(
        holders(engine, x).map((holder) => holder[2]),
        ['event']
      );
      engine.instances.delete(alice, 'Step', 's1');
      assert.deepEqual(
        holders(engine, z).map((holder) => holder[2]),
        ['event']
      );
      assert.deepEqual(payloads(engine), [x, y, z].map(hashOf).sort());
    });

    test('a driver outside the transaction gets a value removed only after the commit that dropped its last holder, and none a rollback left', () => {
      const values = new MapDriver();
      const engine = open(driver, { values: { driver: values } });
      publish(engine, steps([{ name: 'test.Shelf' }]));
      const [x, y] = [verify(60, 'x'), verify(60, 'y')];
      engine.instances.invokeSchema(alice, 'Step', 'keep', { key: 'a', doc: x });
      // The failed keep stowed y and dropped x, then rolled back: x stays,
      // and y, which the driver wrote and nothing holds, goes at the end of
      // the transaction.
      assert.throws(() => engine.instances.invokeSchema(alice, 'Step', 'keep', { key: 'a', doc: y, fail: true }));
      assert.deepEqual([...values.values.keys()], [hashOf(x)]);
      assert.equal(values.removes, 1);
      assert.deepEqual(engine.instances.invokeSchema(alice, 'Step', 'take', { key: 'a' }), x);
      engine.instances.invokeSchema(alice, 'Step', 'keep', { key: 'a', doc: y });
      assert.deepEqual([...values.values.keys()], [hashOf(y)]);
      assert.equal(values.removes, 2);
    });

    test('values.sweep removes what a crash left in a driver that lists its hashes, and refuses a driver that does not', () => {
      const values = new MapDriver();
      const listing = Object.assign(values, {
        list: (after: string, limit: number) => [...values.values.keys()].filter((hash) => hash > after).sort().slice(0, limit),
      });
      const engine = open(driver, { values: { driver: listing } });
      publish(engine, steps([{ name: 'test.Shelf' }]));
      const x = verify(60, 'x');
      engine.instances.invokeSchema(alice, 'Step', 'keep', { key: 'a', doc: x });
      // A value a crash left between the driver's write and the end of its
      // transaction: stored, held by nothing.
      values.values.set('0'.repeat(64), '"left behind"');
      assert.deepEqual(engine.values.sweep(), { removed: 1 });
      assert.deepEqual([...values.values.keys()], [hashOf(x)]);
      assert.deepEqual(engine.values.sweep(), { removed: 0 });
      // The default driver lists its hashes too, and leaves none behind.
      const plain = open(driver);
      assert.deepEqual(plain.values.sweep(), { removed: 0 });
      const unlisted = open(driver, { values: { driver: new MapDriver() } });
      assert.throws(() => unlisted.values.sweep(), /needs a driver that lists its hashes/);
    });

    test('a value longer than values.maxBytes is refused with value_too_large, where it would be stored', () => {
      assert.throws(() => open(driver, { values: { thresholdBytes: 2048, maxBytes: 1024 } }), /values.maxBytes is an integer of at least values.thresholdBytes \(2048\)/);
      const engine = open(driver, { values: { thresholdBytes: 1024, maxBytes: 4096 } });
      assert.equal(engine.values.maxBytes, 4096);
      publish(engine, steps([{ name: 'test.Shelf' }]));
      const refused = thrown(() => engine.instances.create(alice, 'Step', { title: 'big', kind: 'verify', result: verify(500, 'x') }), ValueTooLargeError);
      assert.deepEqual([refused.code, refused.path, refused.maxBytes], ['value_too_large', '/result', 4096]);
      assert.ok(refused.bytes > 4096);
      assert.equal(engine.instances.list(alice, 'Step').items.length, 0);
      // One under the most is stored, by hash.
      engine.instances.create(alice, 'Step', { title: 'fits', kind: 'verify', result: verify(60, 'x') }, { id: 'fits' });
      // So is an object a behavior stows, at its pointer in the object.
      const kept = thrown(() => engine.instances.invokeSchema(alice, 'Step', 'keep', { key: 'a', doc: verify(500, 'y') }), ValueTooLargeError);
      assert.equal(kept.path, '/doc');
      assert.equal(engine.instances.get(alice, 'Step', 'fits')?.data.title, 'fits');
    });
  });
}
