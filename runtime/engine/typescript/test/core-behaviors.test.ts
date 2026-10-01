// The core's behaviors with no extension linked (D10, D16): an engine with
// only its own behaviors and the core meta-schema runs the document the
// core binary builds in the CLI smoke (fixture-behaviors-json), which
// composes all three on one type; they register when the engine opens,
// under names no deployment can take.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { EngineError, defineBehavior, type Principal } from '../dist/index.js';
import { alice, cleanup, documentsDocument, drivers, openTestEngine } from './helpers.ts';

afterEach(cleanup);

// The General schema-file document `superschematic build --emit-ir` loads
// with no extension linked (make cli-smoke).
const documents = documentsDocument();

const writer: Principal = { subject: 'wes', permissions: [] };
const reviewer: Principal = { subject: 'rae', permissions: ['documents.review'] };
const publisher: Principal = { subject: 'pat', permissions: ['documents'] };

for (const driver of drivers) {
  describe(`the core's behaviors with no extension (${driver})`, () => {
    test('an engine registers Workflow, Comments and Revisions when it opens, and no one else can take their names', () => {
      const engine = openTestEngine({ driver });
      assert.deepEqual(engine.behaviors.names(), ['Comments', 'Revisions', 'Workflow']);
      assert.deepEqual(engine.behaviors.declaration('Workflow')?.fields, [{ name: 'status', description: 'The state the instance is in.' }]);
      const impostor = defineBehavior({ declaration: { name: 'Workflow' } });
      assert.throws(() => engine.behaviors.register(impostor), /behavior Workflow is already registered with this engine/);
      assert.throws(() => openTestEngine({ driver, behaviors: [impostor] }), /already registered/);
    });

    test('it runs the document the core binary builds, with all three composed on one type', () => {
      let now = 1000;
      const engine = openTestEngine({ driver, clock: () => (now += 1) });
      engine.schemas.define(alice, documents);
      assert.deepEqual(engine.schemas.publish(alice, 'documents'), { namespace: 'default', name: 'documents', version: 1, published: true });
      assert.deepEqual(
        engine.schemas.behaviors(alice, 'documents').map(({ name }) => name),
        ['Workflow', 'Comments', 'Revisions']
      );

      const created = engine.instances.create(writer, 'documents', { title: 'Launch plan' }, { id: 'doc-1' });
      assert.deepEqual(created.data, { title: 'Launch plan', status: 'draft', commentCount: 0, revision: 1 });

      engine.instances.invoke(writer, 'documents', 'doc-1', 'comment', { body: 'First pass is up.' });
      engine.instances.invoke(writer, 'documents', 'doc-1', 'transition', { to: 'review' });
      engine.instances.invoke(writer, 'documents', 'doc-1', 'propose', { patch: { body: 'The plan, in full.' }, note: 'Adds the body.' });
      assert.equal(thrown(() => engine.instances.invoke(writer, 'documents', 'doc-1', 'approve', { proposal: 1 })).code, 'forbidden');
      engine.instances.invoke(reviewer, 'documents', 'doc-1', 'approve', { proposal: 1 });
      engine.instances.invoke(reviewer, 'documents', 'doc-1', 'comment', { body: 'Approved the body.', replyTo: 1 });
      assert.equal(thrown(() => engine.instances.invoke(reviewer, 'documents', 'doc-1', 'transition', { to: 'published' })).code, 'forbidden');
      engine.instances.invoke(publisher, 'documents', 'doc-1', 'transition', { to: 'published' });

      const read = engine.instances.get(alice, 'documents', 'doc-1');
      assert.deepEqual(read?.data, { title: 'Launch plan', body: 'The plan, in full.', status: 'published', commentCount: 2, revision: 2 });
      assert.equal(read?.seq, 7);

      const events = engine.events.read(alice, { schema: 'documents', instanceId: 'doc-1' }).events;
      assert.deepEqual(
        events.map((event) => [event.kind, event.actor, event.kind === 'operation' ? (event.change as { patch: unknown }).patch : event.change]),
        [
          ['create', 'wes', { title: 'Launch plan', status: 'draft', commentCount: 0, revision: 1 }],
          ['operation', 'wes', { commentCount: 1 }],
          ['operation', 'wes', { status: 'review' }],
          ['operation', 'wes', {}],
          ['operation', 'rae', { body: 'The plan, in full.', revision: 2 }],
          ['operation', 'rae', { commentCount: 2 }],
          ['operation', 'pat', { status: 'published' }],
        ]
      );
      // The log replays to the instance a read returns.
      let replayed: Record<string, unknown> = {};
      for (const event of events) {
        const patch = event.kind === 'create' ? (event.change as Record<string, unknown>) : (event.change as { patch: Record<string, unknown> }).patch;
        replayed = { ...replayed, ...patch };
      }
      assert.deepEqual(replayed, read?.data);

      const revisions = engine.instances.invoke(alice, 'documents', 'doc-1', 'listRevisions') as { items: Array<{ revision: number; data: unknown; proposal?: number }> };
      assert.deepEqual(
        revisions.items.map(({ revision, data, proposal }) => ({ revision, data, proposal })),
        [
          { revision: 1, data: { title: 'Launch plan' }, proposal: undefined },
          { revision: 2, data: { title: 'Launch plan', body: 'The plan, in full.' }, proposal: 1 },
        ]
      );
      const comments = engine.instances.invoke(alice, 'documents', 'doc-1', 'listComments') as { items: Array<{ replyTo?: number; createdBy: string }> };
      assert.deepEqual(comments.items.map(({ replyTo, createdBy }) => [replyTo, createdBy]), [
        [undefined, 'wes'],
        [1, 'rae'],
      ]);

      // A new version adds an optional field and a state; the instance keeps its history.
      const next = JSON.parse(JSON.stringify(documents)) as {
        types: { Document: { fields: unknown[]; behaviors: Array<{ config?: { states: string[]; transitions: unknown[] } }> } };
      };
      next.types.Document.fields.push({ name: 'summary', typeRef: { name: 'string' } });
      const flow = next.types.Document.behaviors[0].config as { states: string[]; transitions: unknown[] };
      flow.states.push('retracted');
      flow.transitions.push({ from: 'published', to: 'retracted' });
      engine.schemas.define(alice, next);
      assert.equal(engine.schemas.publish(alice, 'documents').version, 2);
      engine.instances.invoke(writer, 'documents', 'doc-1', 'transition', { to: 'retracted' });
      engine.instances.update(writer, 'documents', 'doc-1', { summary: 'Withdrawn.' });
      assert.deepEqual(engine.instances.get(alice, 'documents', 'doc-1')?.data, {
        title: 'Launch plan',
        body: 'The plan, in full.',
        summary: 'Withdrawn.',
        status: 'retracted',
        commentCount: 2,
        revision: 3,
      });
    });
  });
}

// thrown runs fn and returns the EngineError it throws.
function thrown(fn: () => unknown): EngineError {
  try {
    fn();
  } catch (error) {
    assert.ok(error instanceof EngineError, `expected an EngineError, got ${String(error)}`);
    return error;
  }
  assert.fail('expected an EngineError');
}
