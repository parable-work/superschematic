// Links, the core's typed links: link and unlink, the links field, pinned
// links and their staleness, the delete of a target (refused for a
// required link, cleared for an optional one, as the caller), the
// schema-level listLinked, reads as the caller, and its config rules.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import {
  BehaviorVetoError,
  EngineError,
  IncompatibleChangeError,
  OperationParamsError,
  SchemaDocumentError,
  type AccessPolicy,
  type AccessRequest,
  type Engine,
  type EngineOptions,
  type Principal,
} from '../dist/index.js';
import { alice, cleanup, drivers, openTestEngine, schemaDocument, thrown } from './helpers.ts';

afterEach(cleanup);

const bob: Principal = { subject: 'bob', permissions: [] };

function schema(name: string, behaviors: Array<{ name: string; config?: unknown }>): Record<string, unknown> {
  const document = schemaDocument(name, [{ name: 'title', typeRef: { name: 'string' }, required: true }]) as {
    types: Record<string, Record<string, unknown>>;
  };
  document.types[name].behaviors = behaviors;
  return document;
}

const taskLinks = {
  spec: { schema: 'Spec', pinned: true },
  owner: { schema: 'Person', required: true },
  related: { schema: 'Task' },
};

function tasks(links: Record<string, unknown> = taskLinks): Record<string, unknown> {
  return schema('Task', [{ name: 'Links', config: { links } }]);
}

function publish(engine: Engine, document: Record<string, unknown>): void {
  engine.schemas.define(alice, document);
  engine.schemas.publish(alice, document.name as string);
}

function policy(rules: (request: AccessRequest) => boolean): AccessPolicy {
  return (request) => request.principal.subject === 'alice' || rules(request);
}

for (const driver of drivers) {
  function open(options: Partial<EngineOptions> = {}): Engine {
    return openTestEngine({ driver, ...options });
  }

  // Specs s1 and s2 keep revisions; people p1 and p2; tasks t1 to t3.
  function world(options: Partial<EngineOptions> = {}): Engine {
    const engine = open(options);
    publish(engine, schema('Spec', [{ name: 'Revisions' }]));
    publish(engine, schema('Person', []));
    publish(engine, tasks());
    for (const id of ['s1', 's2']) {
      engine.instances.create(alice, 'Spec', { title: id }, { id });
    }
    for (const id of ['p1', 'p2']) {
      engine.instances.create(alice, 'Person', { title: id }, { id });
    }
    for (const id of ['t1', 't2', 't3']) {
      engine.instances.create(alice, 'Task', { title: id }, { id });
    }
    return engine;
  }

  const link = (engine: Engine, id: string, params: Record<string, unknown>, principal: Principal = alice) =>
    engine.instances.invoke(principal, 'Task', id, 'link', params);
  const linksOf = (engine: Engine, id: string) => engine.instances.get(alice, 'Task', id)?.data.links;

  describe(`Links (${driver})`, () => {
    test('link points a named link at an instance of its schema; links holds them all, by name; link again moves one', () => {
      const engine = world();
      assert.equal(linksOf(engine, 't1'), undefined);
      assert.deepEqual(link(engine, 't1', { name: 'owner', id: 'p1' }), { name: 'owner', schema: 'Person', id: 'p1' });
      assert.deepEqual(link(engine, 't1', { name: 'related', id: 't2' }), { name: 'related', schema: 'Task', id: 't2' });
      assert.deepEqual(linksOf(engine, 't1'), { owner: { schema: 'Person', id: 'p1' }, related: { schema: 'Task', id: 't2' } });
      assert.deepEqual(engine.events.read(alice, { schema: 'Task', instanceId: 't1' }).events.at(-1)?.change, {
        behavior: 'Links',
        operation: 'link',
        params: { name: 'related', id: 't2' },
        patch: { links: { related: { schema: 'Task', id: 't2' } } },
      });
      link(engine, 't1', { name: 'owner', id: 'p2' });
      assert.deepEqual(linksOf(engine, 't1'), { owner: { schema: 'Person', id: 'p2' }, related: { schema: 'Task', id: 't2' } });
      // Moved, the link no longer holds p1, which can go.
      assert.equal(engine.instances.delete(alice, 'Person', 'p1'), true);
    });

    test('link refuses a name the config lacks, a missing target, and a revision for a link that is not pinned', () => {
      const engine = world();
      const issues = (params: Record<string, unknown>) => thrown(() => link(engine, 't1', params), OperationParamsError).issues;
      assert.deepEqual(issues({ name: 'boss', id: 'p1' }), [{ path: '/name', message: 'Task has no link boss (its links: owner, related, spec)' }]);
      assert.deepEqual(issues({ name: 'owner', id: 'p9' }), [{ path: '/id', message: 'Person p9 does not exist' }]);
      assert.deepEqual(issues({ name: 'owner', id: 'p1', revision: 1 }), [{ path: '/revision', message: 'link owner is not pinned, so it records no revision' }]);
      assert.deepEqual(issues({ name: 'owner', id: 'p1', extra: 1 }).map((issue) => issue.path), ['']);
      assert.equal(linksOf(engine, 't1'), undefined);
    });

    test("a pinned link records the target's revision and reports when the target has moved past it", () => {
      const engine = world();
      assert.deepEqual(link(engine, 't1', { name: 'spec', id: 's1' }), { name: 'spec', schema: 'Spec', id: 's1', revision: 1 });
      assert.deepEqual(linksOf(engine, 't1'), { spec: { schema: 'Spec', id: 's1', revision: 1, stale: false } });
      engine.instances.update(alice, 'Spec', 's1', { title: 's1, revised' });
      assert.deepEqual(linksOf(engine, 't1'), { spec: { schema: 'Spec', id: 's1', revision: 1, stale: true } });
      assert.equal(engine.instances.get(alice, 'Task', 't1')?.seq, 2, 'read when read: no event on the task');
      link(engine, 't1', { name: 'spec', id: 's1' });
      assert.deepEqual(linksOf(engine, 't1'), { spec: { schema: 'Spec', id: 's1', revision: 2, stale: false } });
      assert.deepEqual(link(engine, 't1', { name: 'spec', id: 's1', revision: 1 }), { name: 'spec', schema: 'Spec', id: 's1', revision: 1 });
      assert.equal((linksOf(engine, 't1') as { spec: { stale: boolean } }).spec.stale, true);
      assert.deepEqual(thrown(() => link(engine, 't1', { name: 'spec', id: 's1', revision: 3 }), OperationParamsError).issues, [
        { path: '/revision', message: 'Spec s1 has revisions 1 to 2, not 3' },
      ]);
    });

    test('a pinned link needs a target schema that composes Revisions, and a target that has a revision', () => {
      const engine = open();
      publish(engine, schema('Spec', []));
      publish(engine, schema('Task', [{ name: 'Links', config: { links: { spec: { schema: 'Spec', pinned: true } } } }]));
      engine.instances.create(alice, 'Spec', { title: 's1' }, { id: 's1' });
      engine.instances.create(alice, 'Task', { title: 't1' }, { id: 't1' });
      assert.deepEqual(thrown(() => link(engine, 't1', { name: 'spec', id: 's1' }), OperationParamsError).issues, [
        { path: '/name', message: 'link spec is pinned, but Spec does not compose Revisions' },
      ]);
      // Revisions added to a schema with instances: s1 has none until it changes.
      publish(engine, schema('Spec', [{ name: 'Revisions' }]));
      const none = thrown(() => link(engine, 't1', { name: 'spec', id: 's1' }), BehaviorVetoError);
      assert.deepEqual([none.behavior, none.action, none.reason], ['Links', 'link', 'Spec s1 has no revision to pin yet']);
      engine.instances.update(alice, 'Spec', 's1', { title: 's1, revised' });
      assert.equal((link(engine, 't1', { name: 'spec', id: 's1' }) as { revision: number }).revision, 1);
    });

    test('unlink clears an optional link; a required one only moves; an unset one is refused', () => {
      const engine = world();
      link(engine, 't1', { name: 'owner', id: 'p1' });
      link(engine, 't1', { name: 'spec', id: 's1' });
      assert.deepEqual(engine.instances.invoke(alice, 'Task', 't1', 'unlink', { name: 'spec' }), { name: 'spec', schema: 'Spec', id: 's1', revision: 1 });
      const required = thrown(() => engine.instances.invoke(alice, 'Task', 't1', 'unlink', { name: 'owner' }), BehaviorVetoError);
      assert.equal(required.reason, 'link owner is required: it can be moved with link, not unlinked');
      assert.deepEqual(thrown(() => engine.instances.invoke(alice, 'Task', 't1', 'unlink', { name: 'related' }), OperationParamsError).issues, [
        { path: '/name', message: 'Task t1 has no link related' },
      ]);
      assert.deepEqual(linksOf(engine, 't1'), { owner: { schema: 'Person', id: 'p1' } });
    });

    test("the delete of a required link's target is refused, whoever the caller; an optional link's target clears the link, with an event", () => {
      const engine = world({ policy: policy(({ schema }) => schema === 'Person' || schema === 'Task') });
      link(engine, 't1', { name: 'owner', id: 'p1' });
      link(engine, 't1', { name: 'related', id: 't2' });
      link(engine, 't3', { name: 'related', id: 't2' });
      const refused = thrown(() => engine.instances.delete(bob, 'Person', 'p1'), BehaviorVetoError);
      assert.deepEqual(
        [refused.behavior, refused.action, refused.message],
        ['Links', 'delete', 'behavior Links vetoes delete of Person p1: an instance of Task links to it through its required link owner']
      );
      const after = engine.events.read(alice, { limit: 500 }).events.at(-1)?.cursor ?? 0;
      assert.equal(engine.instances.delete(bob, 'Task', 't2'), true);
      assert.deepEqual(
        engine.events.read(alice, { after }).events.map((event) => [event.kind, event.instanceId, event.actor, (event.change as { operation?: string } | null)?.operation]),
        [
          ['delete', 't2', 'bob', undefined],
          ['operation', 't1', 'bob', 'unlink'],
          ['operation', 't3', 'bob', 'unlink'],
        ]
      );
      assert.deepEqual(linksOf(engine, 't1'), { owner: { schema: 'Person', id: 'p1' } });
      assert.equal(linksOf(engine, 't3'), undefined);
      // Moving the required link frees p1.
      link(engine, 't1', { name: 'owner', id: 'p2' });
      assert.equal(engine.instances.delete(bob, 'Person', 'p1'), true);
    });

    test('the delete of an optional target acts as the caller: one who may not write the linking schema cannot delete it', () => {
      const engine = world({ policy: policy(({ schema, action }) => schema === 'Spec' || action === 'read') });
      link(engine, 't1', { name: 'spec', id: 's1' });
      const refused = thrown(() => engine.instances.delete(bob, 'Spec', 's1'), EngineError);
      assert.deepEqual([refused.code, refused.message], ['forbidden', 'bob may not call unlink (write) on Task in namespace default']);
      assert.equal(engine.instances.delete(bob, 'Spec', 's2'), true);
    });

    test('listLinked is schema-level: the instances whose link points at a target, with each pinned revision, and only the stale ones', () => {
      const engine = world();
      link(engine, 't1', { name: 'spec', id: 's1' });
      engine.instances.update(alice, 'Spec', 's1', { title: 'v2' });
      link(engine, 't2', { name: 'spec', id: 's1' });
      link(engine, 't3', { name: 'spec', id: 's2' });
      link(engine, 't3', { name: 'related', id: 't1' });
      const listed = (params: Record<string, unknown>) => engine.instances.invokeSchema(alice, 'Task', 'listLinked', params);
      assert.deepEqual(listed({ name: 'spec', id: 's1' }), {
        items: [
          { id: 't1', revision: 1, stale: true },
          { id: 't2', revision: 2, stale: false },
        ],
        next: null,
      });
      assert.deepEqual(listed({ name: 'spec', id: 's1', stale: true }), { items: [{ id: 't1', revision: 1, stale: true }], next: null });
      assert.deepEqual(listed({ name: 'related', id: 't1' }), { items: [{ id: 't3' }], next: null });
      assert.deepEqual(listed({ name: 'related', id: 't1', stale: true }), { items: [], next: null });
      const first = listed({ name: 'spec', id: 's1', limit: 1 }) as { items: unknown[]; next: string };
      assert.deepEqual(listed({ name: 'spec', id: 's1', cursor: first.next }), { items: [{ id: 't2', revision: 2, stale: false }], next: null });
      assert.equal(thrown(() => listed({ name: 'boss', id: 'p1' }), OperationParamsError).code, 'invalid_argument');
      assert.equal(thrown(() => engine.instances.invoke(alice, 'Task', 't1', 'listLinked', { name: 'spec', id: 's1' }), EngineError).code, 'not_found');
    });

    test("pinned links are read as the caller: without read on the target's schema, links cannot be read", () => {
      const engine = world({ policy: policy(({ schema }) => schema === 'Task' || schema === 'Person') });
      link(engine, 't1', { name: 'spec', id: 's1' });
      link(engine, 't2', { name: 'owner', id: 'p1' });
      assert.equal(thrown(() => engine.instances.get(bob, 'Task', 't1'), EngineError).code, 'forbidden');
      assert.deepEqual(engine.instances.get(bob, 'Task', 't2')?.data.links, { owner: { schema: 'Person', id: 'p1' } });
      assert.equal(thrown(() => link(engine, 't3', { name: 'spec', id: 's1' }, bob), EngineError).code, 'forbidden');
    });

    test('its config: links by camelCase name, each to a schema; it may be added to a schema with instances, not removed', () => {
      const engine = open();
      // The core meta-schema holds the config to the declaration's configSchema.
      const refusal = (config: unknown) =>
        thrown(() => engine.schemas.define(alice, schema('Task', [{ name: 'Links', config }])), SchemaDocumentError).issues.map((issue) => issue.path);
      assert.ok(refusal({}).includes('/types/Task/behaviors/0/config'));
      assert.ok(refusal({ links: {} }).includes('/types/Task/behaviors/0/config/links'));
      assert.ok(refusal({ links: { Spec: { schema: 'Spec' } } }).includes('/types/Task/behaviors/0/config/links'));
      assert.ok(refusal({ links: { spec: {} } }).includes('/types/Task/behaviors/0/config/links/spec'));
      assert.ok(refusal({ links: { spec: { schema: 'Spec', weak: true } } }).includes('/types/Task/behaviors/0/config/links/spec'));

      publish(engine, schema('Person', []));
      publish(engine, schema('Task', []));
      engine.instances.create(alice, 'Person', { title: 'p1' }, { id: 'p1' });
      engine.instances.create(alice, 'Task', { title: 't1' }, { id: 't1' });
      publish(engine, tasks({ owner: { schema: 'Person' } }));
      link(engine, 't1', { name: 'owner', id: 'p1' });
      // required and pinned may change, and links may be added.
      publish(engine, tasks({ owner: { schema: 'Person', required: true }, related: { schema: 'Task' } }));
      assert.equal(thrown(() => engine.instances.invoke(alice, 'Task', 't1', 'unlink', { name: 'owner' }), BehaviorVetoError).behavior, 'Links');
      const change = (links: Record<string, unknown>) => thrown(() => engine.schemas.define(alice, tasks(links)), IncompatibleChangeError).changes.map((c) => c.message);
      assert.deepEqual(change({ related: { schema: 'Task' } }), [
        'behavior Links on type Task cannot change its config from {"links":{"owner":{"required":true,"schema":"Person"},"related":{"schema":"Task"}}} to {"links":{"related":{"schema":"Task"}}}: link owner is gone, and its instances may hold it',
      ]);
      assert.match(change({ owner: { schema: 'Task' }, related: { schema: 'Task' } })[0], /link owner points at Person, not Task, in the instances that hold it/);
      const removed = thrown(() => engine.schemas.define(alice, schema('Task', [])), IncompatibleChangeError);
      assert.match(removed.message, /behavior Links cannot be removed from type Task, which has instances: the links and references its instances hold would stay behind/);
    });
  });
}
