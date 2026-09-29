// Comments, the core's comments on an instance: comment and its replies,
// listComments a page at a time through the read path, commentCount, the
// access policy's questions, events, delete, and its rule for a new
// version.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import {
  IncompatibleChangeError,
  OperationParamsError,
  type AccessRequest,
  type Engine,
  type EngineOptions,
} from '../dist/index.js';
import { alice, cleanup, drivers, openTestEngine, schemaDocument, thrown } from './helpers.ts';

afterEach(cleanup);

function reviewSchema(behaviors: Array<{ name: string; config?: unknown }> = [{ name: 'Comments' }]): Record<string, unknown> {
  const document = schemaDocument('Review', [{ name: 'title', typeRef: { name: 'string' }, required: true }]) as {
    types: { Review: Record<string, unknown> };
  };
  if (behaviors.length > 0) {
    document.types.Review.behaviors = behaviors;
  }
  return document;
}

const bob = { subject: 'bob', permissions: [] };

for (const driver of drivers) {
  function published(options: Partial<EngineOptions> = {}): Engine {
    const engine = openTestEngine({ driver, ...options });
    engine.schemas.define(alice, reviewSchema());
    engine.schemas.publish(alice, 'Review');
    engine.instances.create(alice, 'Review', { title: 'Q3 plan' }, { id: 'r1' });
    engine.instances.create(alice, 'Review', { title: 'Q4 plan' }, { id: 'r2' });
    return engine;
  }

  describe(`Comments (${driver})`, () => {
    test('comment adds a comment or a reply, numbered per instance, and commentCount counts them', () => {
      let now = 1000;
      const engine = published({ clock: () => now });
      assert.equal(engine.instances.get(alice, 'Review', 'r1')?.data.commentCount, 0);
      assert.deepEqual(engine.instances.invoke(alice, 'Review', 'r1', 'comment', { body: 'Looks right.' }), {
        id: 1,
        body: 'Looks right.',
        createdBy: 'alice',
        createdAt: 1000,
      });
      now = 2000;
      assert.deepEqual(engine.instances.invoke(bob, 'Review', 'r1', 'comment', { body: 'Agreed.', replyTo: 1 }), {
        id: 2,
        replyTo: 1,
        body: 'Agreed.',
        createdBy: 'bob',
        createdAt: 2000,
      });
      assert.equal((engine.instances.invoke(alice, 'Review', 'r2', 'comment', { body: 'First on r2.' }) as { id: number }).id, 1);
      assert.deepEqual(engine.instances.get(alice, 'Review', 'r1')?.data, { title: 'Q3 plan', commentCount: 2 });
      assert.deepEqual(engine.instances.get(alice, 'Review', 'r2')?.data, { title: 'Q4 plan', commentCount: 1 });
      assert.deepEqual(engine.instances.list(alice, 'Review').items.map((item) => item.data.commentCount), [2, 1]);
    });

    test('a reply names a comment of the same instance, and a body has text', () => {
      const engine = published();
      engine.instances.invoke(alice, 'Review', 'r1', 'comment', { body: 'One.' });
      engine.instances.invoke(alice, 'Review', 'r1', 'comment', { body: 'Two.' });
      const missing = thrown(() => engine.instances.invoke(alice, 'Review', 'r2', 'comment', { body: 'Re.', replyTo: 2 }), OperationParamsError);
      assert.deepEqual(missing.issues, [{ path: '/replyTo', message: 'Review r2 has no comment 2' }]);
      for (const params of [{ body: '' }, { body: '  \n ' }, {}, { body: 'x', parentId: 1 }, { body: 'x', replyTo: 0 }]) {
        thrown(() => engine.instances.invoke(alice, 'Review', 'r1', 'comment', params), OperationParamsError);
      }
      assert.equal(engine.instances.get(alice, 'Review', 'r1')?.data.commentCount, 2);
    });

    test('listComments pages oldest first and reads through no write', () => {
      const engine = published();
      for (const body of ['a', 'b', 'c', 'd', 'e']) {
        engine.instances.invoke(alice, 'Review', 'r1', 'comment', { body });
      }
      const seq = engine.instances.get(alice, 'Review', 'r1')?.seq;
      const first = engine.instances.invoke(alice, 'Review', 'r1', 'listComments', { limit: 2 }) as { items: Array<{ body: string }>; next: string };
      assert.deepEqual(first.items.map((item) => item.body), ['a', 'b']);
      const second = engine.instances.invoke(alice, 'Review', 'r1', 'listComments', { limit: 2, cursor: first.next }) as typeof first;
      assert.deepEqual(second.items.map((item) => item.body), ['c', 'd']);
      const last = engine.instances.invoke(alice, 'Review', 'r1', 'listComments', { limit: 2, cursor: second.next }) as { items: unknown[]; next: null };
      assert.deepEqual([last.items.length, last.next], [1, null]);
      assert.equal((engine.instances.invoke(alice, 'Review', 'r1', 'listComments') as { items: unknown[] }).items.length, 5);
      assert.deepEqual(engine.instances.invoke(alice, 'Review', 'r2', 'listComments'), { items: [], next: null });
      const cursor = thrown(() => engine.instances.invoke(alice, 'Review', 'r1', 'listComments', { cursor: 'bogus' }), OperationParamsError);
      assert.deepEqual(cursor.issues, [{ path: '/cursor', message: 'is not a cursor this operation returned' }]);
      thrown(() => engine.instances.invoke(alice, 'Review', 'r1', 'listComments', { limit: 501 }), OperationParamsError);
      // No event, no new sequence.
      assert.equal(engine.instances.get(alice, 'Review', 'r1')?.seq, seq);
      assert.equal(engine.events.read(alice, { schema: 'Review', instanceId: 'r1' }).events.length, 6);
    });

    test('the policy is asked for write to comment and for read to list, with the operation named', () => {
      const asked: AccessRequest[] = [];
      const engine = published({
        policy: (request) => {
          asked.push(request);
          return request.principal.subject === 'alice' || request.action === 'read';
        },
      });
      asked.length = 0;
      engine.instances.invoke(alice, 'Review', 'r1', 'comment', { body: 'Mine.' });
      engine.instances.invoke(bob, 'Review', 'r1', 'listComments');
      assert.equal(thrown(() => engine.instances.invoke(bob, 'Review', 'r1', 'comment', { body: 'Mine too.' }), Error).message, 'bob may not call comment (write) on Review in namespace default');
      assert.deepEqual(
        asked.map(({ principal, action, operation }) => [principal.subject, action, operation]),
        [
          ['alice', 'write', 'comment'],
          ['bob', 'read', 'listComments'],
          ['bob', 'write', 'comment'],
        ]
      );
    });

    test('a comment is an operation event; deleting the instance deletes its comments', () => {
      const engine = published();
      engine.instances.invoke(alice, 'Review', 'r1', 'comment', { body: 'Hello.' });
      assert.deepEqual(engine.events.read(alice, { schema: 'Review', instanceId: 'r1' }).events.at(-1)?.change, {
        behavior: 'Comments',
        operation: 'comment',
        params: { body: 'Hello.' },
        patch: { commentCount: 1 },
      });
      assert.equal(engine.instances.delete(alice, 'Review', 'r1'), true);
      engine.instances.create(alice, 'Review', { title: 'Q3 plan, again' }, { id: 'r1' });
      assert.equal(engine.instances.get(alice, 'Review', 'r1')?.data.commentCount, 0);
      assert.deepEqual(engine.instances.invoke(alice, 'Review', 'r1', 'listComments'), { items: [], next: null });
      assert.equal((engine.instances.invoke(alice, 'Review', 'r1', 'comment', { body: 'Fresh.' }) as { id: number }).id, 1);
    });

    test('Comments joins a schema that has instances, which start with none, and does not leave one', () => {
      const engine = openTestEngine({ driver });
      engine.schemas.define(alice, reviewSchema([]));
      engine.schemas.publish(alice, 'Review');
      engine.instances.create(alice, 'Review', { title: 'Q3 plan' }, { id: 'r1' });
      engine.schemas.define(alice, reviewSchema());
      assert.equal(engine.schemas.publish(alice, 'Review').version, 2);
      assert.deepEqual(engine.instances.get(alice, 'Review', 'r1')?.data, { title: 'Q3 plan', commentCount: 0 });
      engine.instances.invoke(alice, 'Review', 'r1', 'comment', { body: 'Late, but here.' });

      const removed = thrown(() => engine.schemas.define(alice, reviewSchema([])), IncompatibleChangeError);
      assert.match(removed.changes[0].message, /the comments its instances have would stay behind with nothing to delete them/);
    });
  });
}
