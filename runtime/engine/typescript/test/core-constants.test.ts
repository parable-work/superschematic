// Constants, the core's fields set at create (D16, amended): an update that
// changes a listed field is invalid_instance with the rule constant at the
// field, a caller's update and an operation's update() alike, unless the
// caller holds the config's permission; a field the create leaves absent
// stays absent; absent and null are one; validateUpdate() reports it,
// which Revisions' propose asks; the config names the type's own fields,
// and any change to it is allowed, the behavior's adding and removing
// included.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { InstanceValidationError, SchemaDocumentError, type Engine, type Principal } from '../dist/index.js';
import { alice, cleanup, documentsDocument, drivers, openTestEngine, schemaDocument, stepsDocument, thrown } from './helpers.ts';

afterEach(cleanup);

const renamer: Principal = { subject: 'rita', permissions: ['steps.rename'] };
const reviewer: Principal = { subject: 'rae', permissions: ['documents.review'] };

/** A run: a key, a kind, a title, labels and an owner, whose behaviors are the ones given. */
function runDocument(behaviors: Array<{ name: string; config?: unknown }>): Record<string, unknown> {
  const document = schemaDocument('runs', [
    { name: 'key', typeRef: { name: 'string' }, required: true },
    { name: 'kind', typeRef: { name: 'string' } },
    { name: 'title', typeRef: { name: 'string' } },
    { name: 'labels', typeRef: { name: 'string', isArray: true } },
    { name: 'owner', typeRef: { name: 'Owner' } },
  ], {
    types: { Owner: { name: 'Owner', role: 'EmbeddedStruct', fields: [{ name: 'team', typeRef: { name: 'string' }, required: true }, { name: 'lead', typeRef: { name: 'string' } }] } },
  }) as { types: { runs: Record<string, unknown> } };
  document.types.runs.behaviors = behaviors;
  return document;
}

function publish(engine: Engine, document: Record<string, unknown>): void {
  engine.schemas.define(alice, document);
  engine.schemas.publish(alice, document.name as string);
}

/** The [path, rule] of each issue an update is refused with. */
function refusal(engine: Engine, principal: Principal, id: string, patch: Record<string, unknown>): string[][] {
  return thrown(() => engine.instances.update(principal, 'runs', id, patch), InstanceValidationError).issues.map((issue) => [issue.path, issue.rule]);
}

const keep = (fields: string[], permission?: string) => ({ name: 'Constants', config: { fields, ...(permission ? { permission } : {}) } });

for (const driver of drivers) {
  describe(`Constants (${driver})`, () => {
    test('an update that changes a listed field is refused at the field, whoever makes it; other fields stay open', () => {
      const engine = openTestEngine({ driver });
      publish(engine, runDocument([keep(['key', 'labels', 'owner'])]));
      engine.instances.create(alice, 'runs', { key: 'r1', title: 'First', labels: ['a'], owner: { team: 'core' } }, { id: 'r1' });
      const refused = thrown(() => engine.instances.update(alice, 'runs', 'r1', { key: 'r2' }), InstanceValidationError);
      assert.equal(refused.code, 'invalid_instance');
      assert.deepEqual(refused.issues, [
        { path: 'key', rule: 'constant', message: 'key is a constant of runs: its create sets it and nothing changes it after' },
      ]);
      // A change inside a list or an object is a change; every one is an issue.
      assert.deepEqual(refusal(engine, alice, 'r1', { labels: ['a', 'b'], owner: { lead: 'ann' }, key: 'r9' }), [
        ['key', 'constant'],
        ['labels', 'constant'],
        ['owner', 'constant'],
      ]);
      assert.deepEqual(refusal(engine, renamer, 'r1', { labels: null }), [['labels', 'constant']]);
      // The same value is no change, and the fields it does not list stay open.
      const updated = engine.instances.update(alice, 'runs', 'r1', { key: 'r1', owner: { team: 'core' }, title: 'Renamed' });
      assert.deepEqual([updated.seq, updated.data.title], [2, 'Renamed']);
      assert.deepEqual(engine.instances.get(alice, 'runs', 'r1')?.data, { key: 'r1', title: 'Renamed', labels: ['a'], owner: { team: 'core' } });
    });

    test('a field the create leaves absent stays absent, and absent and null are one value', () => {
      const engine = openTestEngine({ driver });
      publish(engine, runDocument([keep(['key', 'kind'])]));
      engine.instances.create(alice, 'runs', { key: 'r1' }, { id: 'r1' });
      assert.deepEqual(refusal(engine, alice, 'r1', { kind: 'build' }), [['kind', 'constant']]);
      engine.instances.create(alice, 'runs', { key: 'r2', kind: null }, { id: 'r2' });
      assert.deepEqual(refusal(engine, alice, 'r2', { kind: 'build' }), [['kind', 'constant']]);
      // Removing a null, or setting one where there is none, changes nothing.
      assert.equal(engine.instances.update(alice, 'runs', 'r2', { kind: null }).data.kind, undefined);
      assert.equal(engine.instances.update(alice, 'runs', 'r1', { kind: null, title: 'x' }).data.title, 'x');
    });

    test("a caller with the config's permission may change them, set them and remove them; any other caller may not", () => {
      const engine = openTestEngine({ driver });
      publish(engine, runDocument([keep(['key', 'kind'], 'steps.rename')]));
      engine.instances.create(alice, 'runs', { key: 'r1' }, { id: 'r1' });
      assert.deepEqual(
        thrown(() => engine.instances.update(alice, 'runs', 'r1', { key: 'r2' }), InstanceValidationError).issues,
        [{ path: 'key', rule: 'constant', message: 'key is a constant of runs: its create sets it and nothing changes it after; only a caller with steps.rename may change it' }]
      );
      assert.deepEqual(engine.instances.update(renamer, 'runs', 'r1', { key: 'r2', kind: 'build' }).data, { key: 'r2', kind: 'build' });
      assert.deepEqual(engine.instances.update(renamer, 'runs', 'r1', { kind: null }).data, { key: 'r2' });
    });

    test("an operation's update() is held to it too, and validateUpdate() reports it: Revisions refuses the proposal, and the approval of one made before", () => {
      const engine = openTestEngine({ driver });
      const document = documentsDocument() as { types: { Document: { behaviors: unknown[] } } };
      publish(engine, document as unknown as Record<string, unknown>);
      engine.instances.create(alice, 'documents', { title: 'Plan' }, { id: 'd1' });
      engine.instances.invoke(alice, 'documents', 'd1', 'propose', { patch: { title: 'Plan B' } });
      // A later version keeps the title, and the instance already holds one.
      document.types.Document.behaviors.push(keep(['title']));
      publish(engine, document as unknown as Record<string, unknown>);
      const proposed = thrown(() => engine.instances.invoke(alice, 'documents', 'd1', 'propose', { patch: { title: 'Plan C', body: 'x' } }), InstanceValidationError);
      assert.deepEqual(
        proposed.issues.map((issue) => [issue.path, issue.rule]),
        [['title', 'constant']]
      );
      const approved = thrown(() => engine.instances.invoke(reviewer, 'documents', 'd1', 'approve', { proposal: 1 }), InstanceValidationError);
      assert.deepEqual(
        approved.issues.map((issue) => [issue.path, issue.rule]),
        [['title', 'constant']]
      );
      assert.equal(engine.instances.get(alice, 'documents', 'd1')?.data.title, 'Plan');
      engine.instances.invoke(alice, 'documents', 'd1', 'propose', { patch: { body: 'The plan.' } });
      engine.instances.invoke(reviewer, 'documents', 'd1', 'approve', { proposal: 2 });
      assert.equal(engine.instances.get(alice, 'documents', 'd1')?.data.body, 'The plan.');
    });

    test("its fields are the type's own; a new version may change them and the permission, and add or remove it on a schema with instances", () => {
      const engine = openTestEngine({ driver });
      const refused = (fields: string[]) => thrown(() => engine.schemas.define(alice, runDocument([keep(fields)])), SchemaDocumentError).issues;
      assert.deepEqual(refused(['key', 'nope']), [
        { path: '/types/runs/behaviors/0/config', message: 'type runs: behavior Constants config: fields: nope is not a field of runs (its fields: key, kind, title, labels, owner)' },
      ]);
      assert.match(refused([])[0].message, /must NOT have fewer than 1 items/);
      // status is Workflow's, not the type's own.
      const flow = { name: 'Workflow', config: { states: ['open', 'closed'], transitions: [{ from: 'open', to: 'closed' }] } };
      assert.match(thrown(() => engine.schemas.define(alice, runDocument([flow, keep(['status'])])), SchemaDocumentError).message, /fields: status is not a field of runs/);

      publish(engine, runDocument([]));
      engine.instances.create(alice, 'runs', { key: 'r1', kind: 'build' }, { id: 'r1' });
      publish(engine, runDocument([keep(['key'])]));
      assert.deepEqual(refusal(engine, alice, 'r1', { key: 'r2' }), [['key', 'constant']]);
      publish(engine, runDocument([keep(['key', 'kind'], 'steps.rename')]));
      assert.deepEqual(refusal(engine, alice, 'r1', { kind: 'test' }), [['kind', 'constant']]);
      publish(engine, runDocument([keep(['kind'])]));
      assert.equal(engine.instances.update(alice, 'runs', 'r1', { key: 'r2' }).data.key, 'r2');
      publish(engine, runDocument([]));
      assert.equal(engine.instances.update(alice, 'runs', 'r1', { kind: 'test' }).data.kind, 'test');
      assert.equal(engine.schemas.live(alice, 'runs')?.version, 5);
    });

    test("the document the core binary builds keeps a step's kind", () => {
      const engine = openTestEngine({ driver });
      publish(engine, stepsDocument());
      engine.instances.create(alice, 'Step', { title: 'Check', kind: 'verify' }, { id: 's1' });
      assert.deepEqual(
        thrown(() => engine.instances.update(alice, 'Step', 's1', { kind: 'note' }), InstanceValidationError).issues.map((issue) => [issue.path, issue.rule]),
        [['kind', 'constant']]
      );
      // Constants writes no JSON Schema: the instance shows only what Variants holds it to.
      const described = engine.tools.describe(alice, 'Step');
      assert.deepEqual(engine.schemas.behaviors(alice, 'Step').map((behavior) => behavior.name), ['Constants', 'Variants']);
      assert.equal((described.instance.allOf as unknown[]).length, 3);
    });
  });
}
