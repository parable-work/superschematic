// Links, the core's typed links: link and unlink, links a create gives
// (every required one among them), the links field, links pinned to a
// revision or a release, with the target's latest and their staleness,
// the delete of a target (refused for a required link, cleared for an
// optional one, as the caller), the schema-level listLinked, reads as the
// caller, and its config rules.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import {
  BehaviorVetoError,
  CreateParamsError,
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
import { Calls, recipeDocument, type Commit } from './branches-fixtures.ts';
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

  // Specs s1 and s2 keep revisions; people p1 and p2; tasks t1 to t3, whose
  // required owner their create gives: p1 owns t1, p2 the others.
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
      engine.instances.create(alice, 'Task', { title: id }, { id, behaviors: { Links: { owner: id === 't1' ? 'p1' : 'p2' } } });
    }
    return engine;
  }

  const link = (engine: Engine, id: string, params: Record<string, unknown>, principal: Principal = alice) =>
    engine.instances.invoke(principal, 'Task', id, 'link', params);
  const linksOf = (engine: Engine, id: string) => engine.instances.get(alice, 'Task', id)?.data.links;

  describe(`Links (${driver})`, () => {
    test('link points a named link at an instance of its schema; links holds them all, by name; link again moves one', () => {
      const engine = world();
      assert.deepEqual(linksOf(engine, 't1'), { owner: { schema: 'Person', id: 'p1' } });
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
      assert.deepEqual(linksOf(engine, 't1'), { owner: { schema: 'Person', id: 'p1' } });
    });

    test("a pinned link records the target's revision and reports when the target has moved past it", () => {
      const engine = world();
      const specOf = (id: string) => (linksOf(engine, id) as { spec?: unknown } | undefined)?.spec;
      assert.deepEqual(link(engine, 't1', { name: 'spec', id: 's1' }), { name: 'spec', schema: 'Spec', id: 's1', revision: 1 });
      assert.deepEqual(specOf('t1'), { schema: 'Spec', id: 's1', revision: 1, latest: 1, stale: false });
      engine.instances.update(alice, 'Spec', 's1', { title: 's1, revised' });
      assert.deepEqual(specOf('t1'), { schema: 'Spec', id: 's1', revision: 1, latest: 2, stale: true });
      assert.equal(engine.instances.get(alice, 'Task', 't1')?.seq, 2, 'read when read: no event on the task');
      link(engine, 't1', { name: 'spec', id: 's1' });
      assert.deepEqual(specOf('t1'), { schema: 'Spec', id: 's1', revision: 2, latest: 2, stale: false });
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
        { path: '/name', message: 'link spec is pinned to a revision, but Spec does not compose Revisions' },
      ]);
      // Revisions added to a schema with instances: s1 has none until it changes.
      publish(engine, schema('Spec', [{ name: 'Revisions' }]));
      const none = thrown(() => link(engine, 't1', { name: 'spec', id: 's1' }), BehaviorVetoError);
      assert.deepEqual([none.behavior, none.action, none.reason, none.vetoCode], ['Links', 'link', 'Spec s1 has no revision to pin yet', 'no_revision']);
      engine.instances.update(alice, 'Spec', 's1', { title: 's1, revised' });
      assert.equal((link(engine, 't1', { name: 'spec', id: 's1' }) as { revision: number }).revision, 1);
    });

    test('unlink clears an optional link; a required one only moves; an unset one is refused', () => {
      const engine = world();
      link(engine, 't1', { name: 'owner', id: 'p1' });
      link(engine, 't1', { name: 'spec', id: 's1' });
      assert.deepEqual(engine.instances.invoke(alice, 'Task', 't1', 'unlink', { name: 'spec' }), { name: 'spec', schema: 'Spec', id: 's1', revision: 1 });
      const required = thrown(() => engine.instances.invoke(alice, 'Task', 't1', 'unlink', { name: 'owner' }), BehaviorVetoError);
      assert.deepEqual([required.reason, required.vetoCode], ['link owner is required: it can be moved with link, not unlinked', 'required_link']);
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
        [refused.behavior, refused.action, refused.message, refused.vetoCode],
        ['Links', 'delete', 'behavior Links vetoes delete of Person p1: an instance of Task links to it through its required link owner', 'required_target']
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
      assert.deepEqual(linksOf(engine, 't3'), { owner: { schema: 'Person', id: 'p2' } });
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
          { id: 't1', revision: 1, latest: 2, stale: true },
          { id: 't2', revision: 2, latest: 2, stale: false },
        ],
        next: null,
      });
      assert.deepEqual(listed({ name: 'spec', id: 's1', stale: true }), { items: [{ id: 't1', revision: 1, latest: 2, stale: true }], next: null });
      assert.deepEqual(listed({ name: 'related', id: 't1' }), { items: [{ id: 't3' }], next: null });
      assert.deepEqual(listed({ name: 'related', id: 't1', stale: true }), { items: [], next: null });
      const first = listed({ name: 'spec', id: 's1', limit: 1 }) as { items: unknown[]; next: string };
      assert.deepEqual(listed({ name: 'spec', id: 's1', cursor: first.next }), { items: [{ id: 't2', revision: 2, latest: 2, stale: false }], next: null });
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
      const change = (links: Record<string, unknown>) => thrown(() => engine.schemas.define(alice, tasks(links)), IncompatibleChangeError).changes.map((c) => c.message);
      // t1 holds no link, so Links comes with no required one.
      assert.match(change({ owner: { schema: 'Person', required: true } })[0], /cannot be added to type Task, which has instances: the instances that exist hold no link owner, which is required/);
      publish(engine, tasks({ owner: { schema: 'Person' } }));
      link(engine, 't1', { name: 'owner', id: 'p1' });
      // pinned may change and optional links may be added; no link becomes
      // required, as no field does: an instance may not hold it.
      assert.match(change({ owner: { schema: 'Person', required: true } })[0], /link owner becomes required, and an instance the live version accepts may not hold it/);
      assert.match(change({ owner: { schema: 'Person' }, boss: { schema: 'Person', required: true } })[0], /link boss is new and required, and an instance the live version accepts holds none/);
      publish(engine, tasks({ owner: { schema: 'Person' }, related: { schema: 'Task', pinned: true } }));
      publish(engine, tasks({ owner: { schema: 'Person' }, related: { schema: 'Task' } }));
      assert.deepEqual(change({ related: { schema: 'Task' } }), [
        'behavior Links on type Task cannot change its config from {"links":{"owner":{"schema":"Person"},"related":{"schema":"Task"}}} to {"links":{"related":{"schema":"Task"}}}: link owner is gone, and its instances may hold it',
      ]);
      assert.match(change({ owner: { schema: 'Task' }, related: { schema: 'Task' } })[0], /link owner points at Person, not Task, in the instances that hold it/);
      const removed = thrown(() => engine.schemas.define(alice, schema('Task', [])), IncompatibleChangeError);
      assert.match(removed.message, /behavior Links cannot be removed from type Task, which has instances: the links and references its instances hold would stay behind/);
    });

    test('with no instance in any namespace, the config may change in any way; the first instance holds it to the rules again', () => {
      const engine = open();
      publish(engine, schema('Person', []));
      publish(engine, tasks({ owner: { schema: 'Person' }, related: { schema: 'Task' } }));
      // A link made required, a new required one, a link gone and a link
      // pointed at another schema: no instance holds a link or lacks one.
      publish(engine, tasks({ owner: { schema: 'Person', required: true }, boss: { schema: 'Person', required: true } }));
      engine.schemas.define(alice, tasks({ owner: { schema: 'Task' }, boss: { schema: 'Person', required: true } }));
      assert.equal(engine.schemas.publish(alice, 'Task').version, 3);
      engine.instances.create(alice, 'Person', { title: 'p1' }, { id: 'p1' });
      engine.instances.create(alice, 'Task', { title: 't1' }, { id: 't1', behaviors: { Links: { boss: 'p1' } } });
      const change = (links: Record<string, unknown>) => thrown(() => engine.schemas.define(alice, tasks(links)), IncompatibleChangeError).changes.map((c) => c.message);
      assert.match(change({ owner: { schema: 'Task', required: true }, boss: { schema: 'Person', required: true } })[0], /link owner becomes required/);
      assert.match(change({ boss: { schema: 'Person', required: true } })[0], /link owner is gone, and its instances may hold it/);
      // Deleting the last instance frees the config again.
      engine.instances.delete(alice, 'Task', 't1');
      publish(engine, tasks({ boss: { schema: 'Person', required: true } }));
    });

    test("a create gives links by name, a target's id or { id, revision }; they hold from its event, as link's do", () => {
      const engine = world();
      engine.instances.update(alice, 'Spec', 's1', { title: 's1, revised' });
      const created = engine.instances.create(
        alice,
        'Task',
        { title: 't4' },
        { id: 't4', behaviors: { Links: { owner: 'p1', spec: { id: 's1', revision: 1 }, related: { id: 't1' } } } }
      );
      const links = {
        owner: { schema: 'Person', id: 'p1' },
        related: { schema: 'Task', id: 't1' },
        spec: { schema: 'Spec', id: 's1', revision: 1, latest: 2, stale: true },
      };
      assert.deepEqual(created.data, { title: 't4', links });
      // One event: the create, which carries the links.
      const events = engine.events.read(alice, { schema: 'Task', instanceId: 't4' }).events;
      assert.deepEqual(
        events.map((event) => [event.kind, event.change]),
        [['create', { title: 't4', links }]]
      );
      // A pinned link gets the target's latest revision when the create names none.
      engine.instances.create(alice, 'Task', { title: 't5' }, { id: 't5', behaviors: { Links: { owner: 'p2', spec: 's1' } } });
      assert.deepEqual((linksOf(engine, 't5') as { spec: unknown }).spec, { schema: 'Spec', id: 's1', revision: 2, latest: 2, stale: false });
      // They are references as link's are: the required owner holds p1, and
      // t1's delete unlinks related on t4.
      assert.equal(thrown(() => engine.instances.delete(alice, 'Person', 'p1'), BehaviorVetoError).behavior, 'Links');
      engine.instances.invoke(alice, 'Task', 't1', 'link', { name: 'owner', id: 'p2' });
      assert.equal(engine.instances.delete(alice, 'Task', 't1'), true);
      assert.equal((linksOf(engine, 't4') as { related?: unknown }).related, undefined);
      assert.deepEqual(engine.events.read(alice, { schema: 'Task', instanceId: 't4' }).events.at(-1)?.change, {
        behavior: 'Links',
        operation: 'unlink',
        params: { name: 'related' },
        patch: { links: { related: null } },
      });
      assert.equal(thrown(() => engine.instances.delete(alice, 'Person', 'p1'), BehaviorVetoError).behavior, 'Links');
    });

    test("a required link is one every create gives; a create's links are held to link's checks, at pointers into its parameters", () => {
      const engine = world();
      const issues = (behaviors: Record<string, unknown> | undefined) =>
        thrown(() => engine.instances.create(alice, 'Task', { title: 't9' }, { id: 't9', ...(behaviors === undefined ? {} : { behaviors }) }), CreateParamsError).issues;
      const required = [{ path: '/behaviors/Links', message: 'link owner is required, so a create of Task gives it' }];
      assert.deepEqual(issues(undefined), required);
      assert.deepEqual(issues({ Links: {} }), required);
      assert.deepEqual(issues({ Links: { owner: 'p1', boss: 'p2' } }), [{ path: '/behaviors/Links/boss', message: 'Task has no link boss (its links: owner, related, spec)' }]);
      assert.deepEqual(issues({ Links: { owner: 'p9' } }), [{ path: '/behaviors/Links/owner', message: 'Person p9 does not exist' }]);
      assert.deepEqual(issues({ Links: { owner: { id: 'p9' } } }), [{ path: '/behaviors/Links/owner/id', message: 'Person p9 does not exist' }]);
      assert.deepEqual(issues({ Links: { owner: { id: 'p1', revision: 1 } } }), [
        { path: '/behaviors/Links/owner/revision', message: 'link owner is not pinned, so it records no revision' },
      ]);
      assert.deepEqual(issues({ Links: { owner: 'p1', spec: { id: 's1', revision: 2 } } }), [
        { path: '/behaviors/Links/spec/revision', message: 'Spec s1 has revisions 1 to 1, not 2' },
      ]);
      // Its createParamsSchema: a name is camelCase, a target an id or { id, revision? }.
      assert.deepEqual(issues({ Links: { owner: 7 } }), [{ path: '/behaviors/Links/owner', message: 'must be string,object' }]);
      assert.deepEqual(
        issues({ Links: { owner: { id: 'p1', pin: 1 } } }).map((issue) => issue.path),
        ['/behaviors/Links/owner']
      );
      assert.ok(issues({ Links: { Owner: 'p1' } }).some((issue) => issue.path === '/behaviors/Links' && /property name/.test(issue.message)));
      // A refused create leaves nothing: no instance, no link, no event.
      assert.equal(engine.instances.get(alice, 'Task', 't9'), undefined);
      assert.equal(engine.events.read(alice, { schema: 'Task', instanceId: 't9' }).events.length, 0);
      assert.equal(engine.instances.delete(alice, 'Spec', 's2'), true);
    });

    test("a create's pinned link to a target with no revision is vetoed; a caller who may not read the target's schema cannot give it", () => {
      const engine = open({ policy: policy(({ schema }) => schema === 'Task') });
      publish(engine, schema('Spec', []));
      publish(engine, schema('Task', [{ name: 'Links', config: { links: { spec: { schema: 'Spec', pinned: true } } } }]));
      engine.instances.create(alice, 'Spec', { title: 's1' }, { id: 's1' });
      // Revisions added to a schema with instances: s1 has none until it changes.
      publish(engine, schema('Spec', [{ name: 'Revisions' }]));
      const none = thrown(() => engine.instances.create(alice, 'Task', { title: 't1' }, { behaviors: { Links: { spec: 's1' } } }), BehaviorVetoError);
      // A create's link is vetoed with link's declared code, and its entry.
      assert.deepEqual(
        [none.behavior, none.action, none.reason, none.vetoCode, none.vetoDetails],
        ['Links', 'create', 'Spec s1 has no revision to pin yet', 'no_revision', { path: '/behaviors/Links/spec' }]
      );
      engine.instances.update(alice, 'Spec', 's1', { title: 's1, revised' });
      const refused = thrown(() => engine.instances.create(bob, 'Task', { title: 't1' }, { behaviors: { Links: { spec: 's1' } } }), EngineError);
      assert.equal(refused.code, 'forbidden');
      assert.equal(engine.instances.list(alice, 'Task').items.length, 0);
      assert.deepEqual(engine.instances.create(bob, 'Task', { title: 't1' }, { id: 't1' }).data, { title: 't1' });
    });

    test('a required link may become optional, and then be unlinked', () => {
      const engine = world();
      publish(engine, tasks({ ...taskLinks, owner: { schema: 'Person' } }));
      engine.instances.invoke(alice, 'Task', 't1', 'unlink', { name: 'owner' });
      assert.equal(linksOf(engine, 't1'), undefined);
      assert.deepEqual(engine.instances.create(alice, 'Task', { title: 't4' }, { id: 't4' }).data, { title: 't4' });
    });

    // A Recipe composes Branches; a Cook task's recipe link pins its
    // release, and soup is released when release() is called.
    function kitchen(): { engine: Engine; release: () => Commit } {
      const engine = open();
      publish(engine, recipeDocument() as unknown as Record<string, unknown>);
      publish(engine, schema('Cook', [{ name: 'Links', config: { links: { recipe: { schema: 'Recipe', pinned: 'release' } } } }]));
      engine.instances.create(alice, 'Recipe', { title: 'Soup' }, { id: 'soup' });
      engine.instances.create(alice, 'Cook', { title: 'c1' }, { id: 'c1' });
      engine.instances.create(alice, 'Cook', { title: 'c2' }, { id: 'c2' });
      const soup = new Calls(engine, 'soup');
      let changes = 0;
      return {
        engine,
        release() {
          changes += 1;
          const commit = soup.change(`change${changes}`, { step: { upsert: [{ instruction: `Step ${changes}`, position: changes }] } });
          const released = engine.instances.get(alice, 'Recipe', 'soup')?.data.release;
          soup.invoke('releaseCommit', { commit: commit.id, version: typeof released === 'number' ? released : 0 });
          return commit;
        },
      };
    }

    const cook = (engine: Engine, id: string, params: Record<string, unknown>) => engine.instances.invoke(alice, 'Cook', id, 'link', params);
    const recipeOf = (engine: Engine, id: string) => (engine.instances.get(alice, 'Cook', id)?.data.links as { recipe?: unknown } | undefined)?.recipe;

    test("a link pinned to a release records the target's release, its latest beside it, and stale once the target is released again", () => {
      const { engine, release } = kitchen();
      const none = thrown(() => cook(engine, 'c1', { name: 'recipe', id: 'soup' }), BehaviorVetoError);
      assert.deepEqual([none.behavior, none.action, none.reason, none.vetoCode], ['Links', 'link', 'Recipe soup has no release to pin yet', 'no_release']);
      release();
      assert.equal(engine.instances.get(alice, 'Recipe', 'soup')?.data.release, 1);
      assert.deepEqual(cook(engine, 'c1', { name: 'recipe', id: 'soup' }), { name: 'recipe', schema: 'Recipe', id: 'soup', release: 1 });
      assert.deepEqual(recipeOf(engine, 'c1'), { schema: 'Recipe', id: 'soup', release: 1, latest: 1, stale: false });
      release();
      assert.deepEqual(recipeOf(engine, 'c1'), { schema: 'Recipe', id: 'soup', release: 1, latest: 2, stale: true });
      assert.equal(engine.instances.get(alice, 'Cook', 'c1')?.seq, 2, 'read when read: no event on the task');
      assert.deepEqual(cook(engine, 'c2', { name: 'recipe', id: 'soup', release: 1 }), { name: 'recipe', schema: 'Recipe', id: 'soup', release: 1 });
      cook(engine, 'c1', { name: 'recipe', id: 'soup' });
      assert.deepEqual(recipeOf(engine, 'c1'), { schema: 'Recipe', id: 'soup', release: 2, latest: 2, stale: false });
      const issues = (params: Record<string, unknown>) => thrown(() => cook(engine, 'c1', params), OperationParamsError).issues;
      assert.deepEqual(issues({ name: 'recipe', id: 'soup', release: 3 }), [{ path: '/release', message: 'Recipe soup has releases 1 to 2, not 3' }]);
      assert.deepEqual(issues({ name: 'recipe', id: 'soup', revision: 1 }), [
        { path: '/revision', message: 'link recipe is pinned to a release, so it records no revision' },
      ]);
      // listLinked gives each pin and the latest, and with stale only the
      // ones the target has been released past.
      const listed = (params: Record<string, unknown>) => engine.instances.invokeSchema(alice, 'Cook', 'listLinked', params);
      assert.deepEqual(listed({ name: 'recipe', id: 'soup' }), {
        items: [
          { id: 'c1', release: 2, latest: 2, stale: false },
          { id: 'c2', release: 1, latest: 2, stale: true },
        ],
        next: null,
      });
      assert.deepEqual(listed({ name: 'recipe', id: 'soup', stale: true }), { items: [{ id: 'c2', release: 1, latest: 2, stale: true }], next: null });
      // unlink returns the pin.
      assert.deepEqual(engine.instances.invoke(alice, 'Cook', 'c2', 'unlink', { name: 'recipe' }), { name: 'recipe', schema: 'Recipe', id: 'soup', release: 1 });
    });

    test("a release pin needs a target schema that composes Branches; a create gives it as { id, release }, held to link's checks", () => {
      const { engine, release } = kitchen();
      publish(engine, schema('Note', []));
      publish(engine, schema('Cook', [{ name: 'Links', config: { links: { recipe: { schema: 'Recipe', pinned: 'release' }, note: { schema: 'Note', pinned: 'release' } } } }]));
      engine.instances.create(alice, 'Note', { title: 'n1' }, { id: 'n1' });
      assert.deepEqual(thrown(() => cook(engine, 'c1', { name: 'note', id: 'n1' }), OperationParamsError).issues, [
        { path: '/name', message: 'link note is pinned to a release, but Note does not compose Branches' },
      ]);
      const vetoed = thrown(() => engine.instances.create(alice, 'Cook', { title: 'c3' }, { behaviors: { Links: { recipe: 'soup' } } }), BehaviorVetoError);
      assert.deepEqual([vetoed.action, vetoed.vetoCode, vetoed.vetoDetails], ['create', 'no_release', { path: '/behaviors/Links/recipe' }]);
      release();
      release();
      const created = engine.instances.create(alice, 'Cook', { title: 'c3' }, { id: 'c3', behaviors: { Links: { recipe: { id: 'soup', release: 1 } } } });
      assert.deepEqual(created.data.links, { recipe: { schema: 'Recipe', id: 'soup', release: 1, latest: 2, stale: true } });
      assert.deepEqual(
        thrown(() => engine.instances.create(alice, 'Cook', { title: 'c4' }, { behaviors: { Links: { recipe: { id: 'soup', release: 3 } } } }), CreateParamsError).issues,
        [{ path: '/behaviors/Links/recipe/release', message: 'Recipe soup has releases 1 to 2, not 3' }]
      );
      // The create tool's parameters take a release, not a revision, for the link.
      const create = engine.tools.describe(alice, 'Cook').operations[0];
      const links = (create.params.properties as Record<string, { properties: Record<string, { properties: Record<string, { properties?: Record<string, unknown> }> }> }>).behaviors.properties.Links;
      assert.deepEqual(Object.keys(links.properties.recipe.properties ?? {}), ['id', 'release']);
      assert.deepEqual(Object.keys(links.properties.note.properties ?? {}), ['id', 'release']);
    });

    test('what a link pins may change: a row pinned to the other kind records no pin of the new kind until it is linked again', () => {
      const engine = world();
      link(engine, 't1', { name: 'spec', id: 's1' });
      publish(engine, tasks({ ...taskLinks, spec: { schema: 'Spec', pinned: 'revision' } }));
      assert.deepEqual((linksOf(engine, 't1') as { spec: unknown }).spec, { schema: 'Spec', id: 's1', revision: 1, latest: 1, stale: false });
      publish(engine, tasks({ ...taskLinks, spec: { schema: 'Spec' } }));
      assert.deepEqual((linksOf(engine, 't1') as { spec: unknown }).spec, { schema: 'Spec', id: 's1' });
      publish(engine, tasks({ ...taskLinks, spec: { schema: 'Spec', pinned: 'release' } }));
      assert.deepEqual((linksOf(engine, 't1') as { spec: unknown }).spec, { schema: 'Spec', id: 's1' });
      assert.deepEqual(thrown(() => link(engine, 't1', { name: 'spec', id: 's1' }), OperationParamsError).issues, [
        { path: '/name', message: 'link spec is pinned to a release, but Spec does not compose Branches' },
      ]);
      // Back to a revision pin, the row's revision reads again.
      publish(engine, tasks(taskLinks));
      assert.deepEqual((linksOf(engine, 't1') as { spec: unknown }).spec, { schema: 'Spec', id: 's1', revision: 1, latest: 1, stale: false });
      const refused = thrown(() => engine.schemas.define(alice, tasks({ ...taskLinks, spec: { schema: 'Spec', pinned: 'tag' } })), SchemaDocumentError);
      assert.ok(refused.issues.some((issue) => issue.path === '/types/Task/behaviors/0/config/links/spec/pinned'));
    });
  });
}
