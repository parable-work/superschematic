// Links' many-valued links (D56): a link with many and a max holds a list
// of targets, in the order they were linked. link adds a target or pins
// again one it holds, unlink removes the one it names, a create gives a
// list, each target once, and a deleted target leaves the lists that hold
// it. A required one keeps at least one target. The table a file's Links
// v2 left is built again, its rows kept. What reads one target of a link,
// Reactions' rules among them, refuses a many-valued link; Rollups and
// revised read across the list.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { Ajv2020 } from 'ajv/dist/2020.js';

import {
  BehaviorVetoError,
  CreateParamsError,
  IncompatibleChangeError,
  OperationParamsError,
  SchemaDocumentError,
  Storage,
  allowAll,
  appliedMigrations,
  type Engine,
  type EngineOptions,
  type SubscriptionStatus,
} from '../dist/index.js';
import { alice, cleanup, drivers, freshPath, openEngine, openTestEngine, schemaDocument, thrown, track } from './helpers.ts';
import { runnerPrincipal } from './runner-fixtures.ts';

afterEach(cleanup);

type BehaviorRef = { name: string; config?: unknown };

function schema(name: string, behaviors: BehaviorRef[]): Record<string, unknown> {
  const document = schemaDocument(name, [{ name: 'title', typeRef: { name: 'string' }, required: true }]) as {
    types: Record<string, Record<string, unknown>>;
  };
  document.types[name].behaviors = behaviors;
  return document;
}

const taskLinks = {
  owner: { schema: 'Person' },
  reviewers: { schema: 'Person', many: true, max: 3 },
  specs: { schema: 'Spec', many: true, max: 2, pinned: true },
};

function tasks(links: Record<string, unknown> = taskLinks): Record<string, unknown> {
  return schema('Task', [{ name: 'Links', config: { links } }]);
}

// A review's approvers are required: it holds at least one.
const reviewLinks = { approvers: { schema: 'Person', many: true, max: 2, required: true } };

function publish(engine: Engine, document: Record<string, unknown>): void {
  engine.schemas.define(alice, document);
  engine.schemas.publish(alice, document.name as string);
}

const flow: BehaviorRef = {
  name: 'Workflow',
  config: {
    states: ['todo', 'doing', 'done'],
    transitions: [
      { from: 'todo', to: 'doing' },
      { from: 'doing', to: 'done' },
    ],
  },
};

for (const driver of drivers) {
  function open(options: Partial<EngineOptions> = {}): Engine {
    return openTestEngine({ driver, ...options });
  }

  // People p1 to p4; specs s1 and s2, which keep revisions; tasks t1 to t3,
  // with no link; reviews are published, with none.
  function world(options: Partial<EngineOptions> = {}): Engine {
    const engine = open(options);
    publish(engine, schema('Person', []));
    publish(engine, schema('Spec', [{ name: 'Revisions' }]));
    publish(engine, tasks());
    publish(engine, schema('Review', [{ name: 'Links', config: { links: reviewLinks } }]));
    for (const id of ['p1', 'p2', 'p3', 'p4']) {
      engine.instances.create(alice, 'Person', { title: id }, { id });
    }
    for (const id of ['s1', 's2']) {
      engine.instances.create(alice, 'Spec', { title: id }, { id });
    }
    for (const id of ['t1', 't2', 't3']) {
      engine.instances.create(alice, 'Task', { title: id }, { id });
    }
    return engine;
  }

  const link = (engine: Engine, id: string, params: Record<string, unknown>) => engine.instances.invoke(alice, 'Task', id, 'link', params);
  const unlink = (engine: Engine, id: string, params: Record<string, unknown>) => engine.instances.invoke(alice, 'Task', id, 'unlink', params);
  const linksOf = (engine: Engine, schemaName: string, id: string) => engine.instances.get(alice, schemaName, id)?.behaviors.Links.targets;
  const person = (id: string) => ({ schema: 'Person', id });
  const lastChange = (engine: Engine, schemaName: string, id: string) => engine.events.read(alice, { schema: schemaName, instanceId: id }).events.at(-1)?.change;

  describe(`Links, many-valued (${driver})`, () => {
    test('a many-valued link holds a list in the order its targets were linked; link adds one, or pins again one it holds, up to max', () => {
      const engine = world();
      assert.deepEqual(link(engine, 't1', { name: 'reviewers', id: 'p2' }), { name: 'reviewers', schema: 'Person', id: 'p2' });
      link(engine, 't1', { name: 'reviewers', id: 'p1' });
      link(engine, 't1', { name: 'owner', id: 'p1' });
      assert.deepEqual(linksOf(engine, 'Task', 't1'), { owner: person('p1'), reviewers: [person('p2'), person('p1')] });
      // The event carries the whole list, as a merge patch replaces one.
      link(engine, 't1', { name: 'reviewers', id: 'p3' });
      assert.deepEqual(lastChange(engine, 'Task', 't1'), {
        behavior: 'Links',
        operation: 'link',
        params: { name: 'reviewers', id: 'p3' },
        patch: { behaviors: { Links: { targets: { reviewers: [person('p2'), person('p1'), person('p3')] } } } },
      });
      // A target it holds keeps its place; a new one past max is refused.
      link(engine, 't1', { name: 'reviewers', id: 'p2' });
      const full = thrown(() => link(engine, 't1', { name: 'reviewers', id: 'p4' }), BehaviorVetoError);
      assert.deepEqual([full.behavior, full.action, full.reason, full.vetoCode], ['Links', 'link', 'link reviewers holds 3 targets already, its max', 'too_many']);
      assert.deepEqual((linksOf(engine, 'Task', 't1') as { reviewers: unknown }).reviewers, [person('p2'), person('p1'), person('p3')]);
      // link's other checks hold for a list as for one target.
      assert.deepEqual(thrown(() => link(engine, 't2', { name: 'reviewers', id: 'p9' }), OperationParamsError).issues, [{ path: '/id', message: 'Person p9 does not exist' }]);
      assert.deepEqual(thrown(() => link(engine, 't2', { name: 'reviewers', id: 'p1', revision: 1 }), OperationParamsError).issues, [
        { path: '/revision', message: 'link reviewers is not pinned, so it records no revision' },
      ]);
    });

    test('unlink removes the target it names; a target the link does not hold is not_linked; a link that holds one may name its target', () => {
      const engine = world();
      for (const id of ['p1', 'p2', 'p3']) {
        link(engine, 't1', { name: 'reviewers', id });
      }
      assert.deepEqual(thrown(() => unlink(engine, 't1', { name: 'reviewers' }), OperationParamsError).issues, [
        { path: '/id', message: 'link reviewers holds a list of targets, so unlink names the one it removes' },
      ]);
      assert.deepEqual(unlink(engine, 't1', { name: 'reviewers', id: 'p2' }), { name: 'reviewers', schema: 'Person', id: 'p2' });
      assert.deepEqual(lastChange(engine, 'Task', 't1'), {
        behavior: 'Links',
        operation: 'unlink',
        params: { name: 'reviewers', id: 'p2' },
        patch: { behaviors: { Links: { targets: { reviewers: [person('p1'), person('p3')] } } } },
      });
      const missing = thrown(() => unlink(engine, 't1', { name: 'reviewers', id: 'p2' }), BehaviorVetoError);
      assert.deepEqual([missing.action, missing.reason, missing.vetoCode], ['unlink', 'link reviewers does not hold Person p2', 'not_linked']);
      unlink(engine, 't1', { name: 'reviewers', id: 'p1' });
      unlink(engine, 't1', { name: 'reviewers', id: 'p3' });
      assert.equal(linksOf(engine, 'Task', 't1'), undefined);
      assert.deepEqual((lastChange(engine, 'Task', 't1') as { patch: unknown }).patch, { behaviors: { Links: { targets: null } } });
      assert.equal(thrown(() => unlink(engine, 't1', { name: 'reviewers', id: 'p1' }), BehaviorVetoError).vetoCode, 'not_linked');
      // A link that holds one target is cleared only when it holds the one named.
      link(engine, 't1', { name: 'owner', id: 'p1' });
      assert.equal(thrown(() => unlink(engine, 't1', { name: 'owner', id: 'p2' }), BehaviorVetoError).reason, 'link owner does not hold Person p2');
      assert.deepEqual(unlink(engine, 't1', { name: 'owner', id: 'p1' }), { name: 'owner', schema: 'Person', id: 'p1' });
    });

    test("a pinned many-valued link records each target's revision and reports each one's staleness; pinning again keeps its place", () => {
      const engine = world();
      assert.deepEqual(link(engine, 't1', { name: 'specs', id: 's1' }), { name: 'specs', schema: 'Spec', id: 's1', revision: 1 });
      link(engine, 't1', { name: 'specs', id: 's2' });
      const specsOf = () => (linksOf(engine, 'Task', 't1') as { specs: unknown }).specs;
      engine.instances.update(alice, 'Spec', 's1', { title: 's1, revised' });
      assert.deepEqual(specsOf(), [
        { schema: 'Spec', id: 's1', revision: 1, latest: 2, stale: true },
        { schema: 'Spec', id: 's2', revision: 1, latest: 1, stale: false },
      ]);
      assert.equal(engine.instances.get(alice, 'Task', 't1')?.seq, 3, 'read when read: no event on the task');
      link(engine, 't1', { name: 'specs', id: 's1' });
      assert.deepEqual(specsOf(), [
        { schema: 'Spec', id: 's1', revision: 2, latest: 2, stale: false },
        { schema: 'Spec', id: 's2', revision: 1, latest: 1, stale: false },
      ]);
      link(engine, 't1', { name: 'specs', id: 's1', revision: 1 });
      const listed = (params: Record<string, unknown>) => engine.instances.invokeSchema(alice, 'Task', 'listLinked', params);
      assert.deepEqual(listed({ name: 'specs', id: 's1', stale: true }), { items: [{ id: 't1', revision: 1, latest: 2, stale: true }], next: null });
      assert.deepEqual(unlink(engine, 't1', { name: 'specs', id: 's1' }), { name: 'specs', schema: 'Spec', id: 's1', revision: 1 });
    });

    test('listLinked lists the instances whose many-valued link holds a target, in the order they linked it', () => {
      const engine = world();
      link(engine, 't1', { name: 'reviewers', id: 'p1' });
      link(engine, 't1', { name: 'reviewers', id: 'p2' });
      link(engine, 't2', { name: 'reviewers', id: 'p2' });
      link(engine, 't3', { name: 'reviewers', id: 'p1' });
      const listed = (params: Record<string, unknown>) => engine.instances.invokeSchema(alice, 'Task', 'listLinked', params) as { items: unknown[]; next: string | null };
      assert.deepEqual(listed({ name: 'reviewers', id: 'p2' }), { items: [{ id: 't1' }, { id: 't2' }], next: null });
      const first = listed({ name: 'reviewers', id: 'p1', limit: 1 });
      assert.deepEqual(first.items, [{ id: 't1' }]);
      assert.deepEqual(listed({ name: 'reviewers', id: 'p1', cursor: first.next }), { items: [{ id: 't3' }], next: null });
    });

    test('a deleted target leaves only the lists that hold it, each with an unlink event naming it; a required list refuses only the delete of its last target', () => {
      const engine = world();
      link(engine, 't1', { name: 'reviewers', id: 'p1' });
      link(engine, 't1', { name: 'reviewers', id: 'p2' });
      link(engine, 't2', { name: 'reviewers', id: 'p1' });
      link(engine, 't2', { name: 'owner', id: 'p1' });
      const after = engine.events.read(alice, { limit: 500 }).events.at(-1)?.cursor ?? 0;
      assert.equal(engine.instances.delete(alice, 'Person', 'p1'), true);
      assert.deepEqual(
        engine.events.read(alice, { after }).events.map((event) => [event.kind, event.instanceId, (event.change as { params?: unknown } | null)?.params]),
        [
          ['delete', 'p1', undefined],
          ['operation', 't1', { name: 'reviewers', id: 'p1' }],
          ['operation', 't2', { name: 'reviewers', id: 'p1' }],
          ['operation', 't2', { name: 'owner' }],
        ]
      );
      assert.deepEqual(linksOf(engine, 'Task', 't1'), { reviewers: [person('p2')] });
      assert.equal(linksOf(engine, 'Task', 't2'), undefined);

      const review = engine.instances.create(alice, 'Review', { title: 'r1' }, { id: 'r1', behaviors: { Links: { approvers: ['p2', 'p3'] } } });
      assert.deepEqual(review.behaviors.Links.targets, { approvers: [person('p2'), person('p3')] });
      assert.equal(engine.instances.delete(alice, 'Person', 'p3'), true);
      const refused = thrown(() => engine.instances.delete(alice, 'Person', 'p2'), BehaviorVetoError);
      assert.deepEqual(
        [refused.behavior, refused.message, refused.vetoCode],
        ['Links', 'behavior Links vetoes delete of Person p2: an instance of Review links to it through its required link approvers, as its last target', 'required_target']
      );
      const last = thrown(() => engine.instances.invoke(alice, 'Review', 'r1', 'unlink', { name: 'approvers', id: 'p2' }), BehaviorVetoError);
      assert.deepEqual(
        [last.reason, last.vetoCode],
        ['link approvers is required and Person p2 is its last target: link another before unlinking it', 'required_link']
      );
      // Another target first frees it.
      engine.instances.invoke(alice, 'Review', 'r1', 'link', { name: 'approvers', id: 'p4' });
      assert.equal(engine.instances.delete(alice, 'Person', 'p2'), true);
      assert.deepEqual(linksOf(engine, 'Review', 'r1'), { approvers: [person('p4')] });
      assert.deepEqual(linksOf(engine, 'Task', 't1'), undefined);
    });

    test('a create gives a many-valued link a list, in order, each target once and at most max; it holds from its event', () => {
      const engine = world();
      const created = engine.instances.create(
        alice,
        'Task',
        { title: 't4' },
        { id: 't4', behaviors: { Links: { owner: 'p3', reviewers: ['p2', { id: 'p1' }], specs: [{ id: 's1', revision: 1 }] } } }
      );
      const links = { owner: person('p3'), reviewers: [person('p2'), person('p1')], specs: [{ schema: 'Spec', id: 's1', revision: 1, latest: 1, stale: false }] };
      assert.deepEqual(created.behaviors.Links.targets, links);
      assert.deepEqual(
        engine.events.read(alice, { schema: 'Task', instanceId: 't4' }).events.map((event) => [event.kind, event.change]),
        [['create', { data: { title: 't4' }, behaviors: { Links: { targets: links } } }]]
      );
      // An empty list of an optional link holds none.
      assert.equal(engine.instances.create(alice, 'Task', { title: 't5' }, { id: 't5', behaviors: { Links: { reviewers: [] } } }).behaviors.Links.targets, undefined);

      const issues = (schemaName: string, behaviors: Record<string, unknown> | undefined) =>
        thrown(
          () => engine.instances.create(alice, schemaName, { title: 'x9' }, { id: 'x9', ...(behaviors === undefined ? {} : { behaviors }) }),
          CreateParamsError
        ).issues;
      for (const [given, expected] of [
        [{ reviewers: 'p1' }, [{ path: '/behaviors/Links/reviewers', message: 'link reviewers holds a list of targets, so a create gives a list' }]],
        [{ owner: ['p1'] }, [{ path: '/behaviors/Links/owner', message: 'link owner holds one target, so a create gives one, not a list' }]],
        [{ reviewers: ['p1', 'p2', 'p3', 'p4'] }, [{ path: '/behaviors/Links/reviewers', message: 'link reviewers holds at most 3 targets, and the create gives 4' }]],
        [
          { reviewers: ['p1', { id: 'p1' }] },
          [{ path: '/behaviors/Links/reviewers/1', message: 'link reviewers holds each target once, and the create gives Person p1 again' }],
        ],
        [{ reviewers: ['p1', 'p9'] }, [{ path: '/behaviors/Links/reviewers/1', message: 'Person p9 does not exist' }]],
        [{ reviewers: [{ id: 'p9' }] }, [{ path: '/behaviors/Links/reviewers/0/id', message: 'Person p9 does not exist' }]],
        [{ specs: ['s2', { id: 's1', revision: 2 }] }, [{ path: '/behaviors/Links/specs/1/revision', message: 'Spec s1 has revisions 1 to 1, not 2' }]],
        [{ reviewers: [7] }, [{ path: '/behaviors/Links/reviewers/0', message: 'must be string,object' }]],
      ] as Array<[Record<string, unknown>, unknown]>) {
        assert.deepEqual(issues('Task', { Links: given }), expected, JSON.stringify(given));
      }
      assert.deepEqual(issues('Review', { Links: { approvers: [] } }), [
        { path: '/behaviors/Links/approvers', message: 'link approvers is required, so a create of Review gives at least one target' },
      ]);
      assert.deepEqual(issues('Review', undefined), [{ path: '/behaviors/Links', message: 'link approvers is required, so a create of Review gives it' }]);
      // A refused create leaves nothing.
      assert.equal(engine.instances.get(alice, 'Task', 'x9'), undefined);
      assert.deepEqual(engine.instances.invokeSchema(alice, 'Task', 'listLinked', { name: 'reviewers', id: 'p4' }), { items: [], next: null });
    });

    test("a create's pinned target with no revision is vetoed at its item of the list", () => {
      const engine = open();
      publish(engine, schema('Spec', []));
      publish(engine, schema('Task', [{ name: 'Links', config: { links: { specs: { schema: 'Spec', many: true, max: 2, pinned: true } } } }]));
      engine.instances.create(alice, 'Spec', { title: 's1' }, { id: 's1' });
      engine.instances.create(alice, 'Spec', { title: 's2' }, { id: 's2' });
      // Revisions added to a schema with instances: s1 and s2 have none until they change.
      publish(engine, schema('Spec', [{ name: 'Revisions' }]));
      engine.instances.update(alice, 'Spec', 's1', { title: 's1, revised' });
      const none = thrown(() => engine.instances.create(alice, 'Task', { title: 't1' }, { behaviors: { Links: { specs: ['s1', 's2'] } } }), BehaviorVetoError);
      assert.deepEqual(
        [none.behavior, none.action, none.reason, none.vetoCode, none.vetoDetails],
        ['Links', 'create', 'Spec s2 has no revision to pin yet', 'no_revision', { path: '/behaviors/Links/specs/1' }]
      );
    });

    test('its config: many takes a max from 1 to 100, and max needs many', () => {
      const engine = open();
      const refusal = (link: unknown) =>
        thrown(() => engine.schemas.define(alice, tasks({ reviewers: link })), SchemaDocumentError).issues.map((issue) => issue.path);
      for (const link of [
        { schema: 'Person', many: true },
        { schema: 'Person', many: true, max: 0 },
        { schema: 'Person', many: true, max: 101 },
        { schema: 'Person', max: 3 },
        { schema: 'Person', many: false, max: 3 },
      ]) {
        assert.ok(refusal(link).some((path) => path.startsWith('/types/Task/behaviors/0')), JSON.stringify(link));
      }
      engine.schemas.define(alice, tasks({ reviewers: { schema: 'Person', many: false } }));
    });

    test('while the schema has instances, a link keeps whether it holds a list, and a list its max or more; with none, any change', () => {
      const engine = open();
      publish(engine, schema('Person', []));
      publish(engine, tasks());
      // No instance: a link may become many-valued, or stop being, and max may fall.
      publish(engine, tasks({ ...taskLinks, owner: { schema: 'Person', many: true, max: 2 } }));
      publish(engine, tasks({ ...taskLinks, reviewers: { schema: 'Person', many: true, max: 1 } }));
      publish(engine, tasks());
      engine.instances.create(alice, 'Task', { title: 't1' }, { id: 't1' });
      const change = (links: Record<string, unknown>) => thrown(() => engine.schemas.define(alice, tasks(links)), IncompatibleChangeError).changes.map((c) => c.message);
      assert.match(change({ ...taskLinks, owner: { schema: 'Person', many: true, max: 2 } })[0], /link owner holds one target in the instances that hold it, not a list/);
      assert.match(change({ ...taskLinks, reviewers: { schema: 'Person' } })[0], /link reviewers holds a list of targets in the instances that hold it, not one/);
      assert.match(change({ ...taskLinks, reviewers: { schema: 'Person', many: true, max: 2 } })[0], /link reviewers may hold 3 targets in the instances that hold it, more than 2/);
      assert.match(
        change({ ...taskLinks, approvers: { schema: 'Person', many: true, max: 2, required: true } })[0],
        /link approvers is new and required, and an instance the live version accepts holds none/
      );
      // max may rise, and an optional many-valued link may be added.
      publish(engine, tasks({ ...taskLinks, reviewers: { schema: 'Person', many: true, max: 5 }, watchers: { schema: 'Person', many: true, max: 10 } }));
    });

    test('its guidance and create parameters say which links hold a list, of how many, and the engine still holds a create to them', () => {
      const engine = world();
      const described = (name: string) => engine.tools.describe(alice, name);
      const create = described('Task').operations.find((operation) => operation.name === 'create');
      const links = (create?.params.properties as Record<string, any>).behaviors.properties.Links;
      assert.deepEqual([links.properties.reviewers.type, links.properties.reviewers.maxItems, links.properties.reviewers.minItems], ['array', 3, undefined]);
      assert.deepEqual(Object.keys(links.properties.specs.items.properties), ['id', 'revision']);
      const approvers = ((described('Review').operations.find((operation) => operation.name === 'create')?.params.properties as Record<string, any>).behaviors.properties.Links)
        .properties.approvers;
      assert.deepEqual([approvers.minItems, approvers.maxItems], [1, 2]);

      const summaryOf = (name: string, behavior: string) => described(name).behaviors.find((one) => one.name === behavior)?.summary as string;
      const summary = summaryOf('Task', 'Links');
      assert.match(summary, /by name: owner to Person, reviewers to Person \(a list of at most 3\) and specs to Spec \(pinned to a revision, a list of at most 2\)\./);
      assert.match(summary, /reviewers and specs hold a list of targets in behaviors\.Links\.targets, in the order they were linked: link adds one and unlink removes the one it names\./);
      assert.match(summaryOf('Review', 'Links'), /Every create gives at least one target of approvers, which keeps one: the last target can be neither unlinked nor deleted\./);
      const operation = (name: string, op: string) => described(name).operations.find((one) => one.name === op)?.guidance as { useWhen?: string; doNotUseWhen?: string; errors?: Array<{ code: string }> };
      const codes = (name: string, op: string) => (operation(name, op).errors ?? []).map((error) => error.code);
      assert.deepEqual(codes('Task', 'link'), ['no_revision', 'too_many']);
      assert.deepEqual(codes('Task', 'unlink'), ['not_linked']);
      assert.deepEqual(codes('Review', 'unlink'), ['required_link', 'not_linked']);
      assert.equal(operation('Task', 'unlink').useWhen, "Use to clear owner. Use with the target's id to remove one target of reviewers or specs.");
      assert.equal(operation('Review', 'unlink').doNotUseWhen, 'Do not remove the last target of approvers: a required link keeps one.');
      assert.match(operation('Task', 'create').useWhen as string, /Give reviewers a list of up to 3 and specs a list of up to 2, each target once\./);

      // The narrowed schema accepts what the create accepts.
      const ajv = new Ajv2020({ strict: false, validateFormats: false });
      const narrowed = ajv.compile(links);
      const accepted = (behaviors: Record<string, unknown>): boolean => {
        try {
          engine.instances.create(alice, 'Task', { title: 'Try' }, { behaviors });
          return true;
        } catch (error) {
          assert.ok(error instanceof CreateParamsError, `${JSON.stringify(behaviors)}: ${String(error)}`);
          return false;
        }
      };
      for (const entry of [
        {},
        { reviewers: [] },
        { reviewers: ['p1'] },
        { reviewers: [{ id: 'p1' }, 'p2', 'p3'] },
        { reviewers: 'p1' },
        { reviewers: { id: 'p1' } },
        { reviewers: ['p1', 'p2', 'p3', 'p4'] },
        { reviewers: [{ id: 'p1', revision: 1 }] },
        { owner: ['p1'] },
        { owner: 'p1' },
        { specs: [{ id: 's1', revision: 1 }, 's2'] },
      ]) {
        assert.equal(narrowed(entry), accepted({ Links: entry }), JSON.stringify(entry));
      }
    });

    test('a file Links v2 left: the table is built again keyed by the target, its rows, their order and their references kept', () => {
      const path = freshPath();
      const links = { owner: { schema: 'Person' }, spec: { schema: 'Spec', pinned: true } };
      let engine = track(openEngine({ path, driver, policy: allowAll }));
      publish(engine, schema('Person', []));
      publish(engine, schema('Spec', [{ name: 'Revisions' }]));
      publish(engine, tasks(links));
      for (const id of ['p1', 'p2']) {
        engine.instances.create(alice, 'Person', { title: id }, { id });
      }
      engine.instances.create(alice, 'Spec', { title: 's1' }, { id: 's1' });
      engine.instances.create(alice, 'Task', { title: 't1' }, { id: 't1', behaviors: { Links: { owner: 'p2', spec: 's1' } } });
      engine.instances.create(alice, 'Task', { title: 't2' }, { id: 't2', behaviors: { Links: { owner: 'p1' } } });
      engine.instances.create(alice, 'Task', { title: 't3' }, { id: 't3', behaviors: { Links: { owner: 'p2' } } });
      engine.close();

      // The table as Links' migrations 1 and 2 left it, with the rows the
      // engine wrote, and the ledger without migration 3.
      const storage = Storage.open(path, { driver });
      const columns = 'row, namespace, schema, id, name, target_schema, target_id, revision, created_by, created_at, release';
      const rows = storage.all(`SELECT ${columns} FROM bhv_links__links ORDER BY row`);
      storage.exec('DROP TABLE bhv_links__links');
      storage.exec(`CREATE TABLE bhv_links__links (
        row           INTEGER PRIMARY KEY,
        namespace     TEXT    NOT NULL,
        schema        TEXT    NOT NULL,
        id            TEXT    NOT NULL,
        name          TEXT    NOT NULL,
        target_schema TEXT    NOT NULL,
        target_id     TEXT    NOT NULL,
        revision      INTEGER,
        created_by    TEXT    NOT NULL,
        created_at    INTEGER NOT NULL,
        UNIQUE (namespace, schema, id, name)
      ) STRICT`);
      storage.exec('CREATE INDEX bhv_links__links_by_target ON bhv_links__links (namespace, schema, name, target_id, row)');
      storage.exec('ALTER TABLE bhv_links__links ADD COLUMN release INTEGER');
      for (const row of rows) {
        storage.run(`INSERT INTO bhv_links__links (${columns}) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, Object.values(row));
      }
      storage.run("DELETE FROM engine_migrations WHERE owner = 'Links' AND version = 3");
      storage.close();

      engine = track(openEngine({ path, driver, policy: allowAll }));
      assert.deepEqual(
        appliedMigrations(engine.storage, 'Links').map((migration) => [migration.version, migration.name]),
        [
          [1, 'links'],
          [2, 'release pins'],
          [3, 'many-valued links'],
        ]
      );
      assert.deepEqual(engine.storage.all(`SELECT ${columns} FROM bhv_links__links ORDER BY row`), rows);
      assert.deepEqual(
        engine.storage.all("SELECT type, name FROM sqlite_master WHERE tbl_name = 'bhv_links__links' ORDER BY name"),
        [
          { type: 'table', name: 'bhv_links__links' },
          { type: 'index', name: 'bhv_links__links_by_target' },
          { type: 'index', name: 'sqlite_autoindex_bhv_links__links_1' },
        ]
      );
      assert.deepEqual(linksOf(engine, 'Task', 't1'), { owner: person('p2'), spec: { schema: 'Spec', id: 's1', revision: 1, latest: 1, stale: false } });
      assert.deepEqual(engine.instances.invokeSchema(alice, 'Task', 'listLinked', { name: 'owner', id: 'p2' }), { items: [{ id: 't1' }, { id: 't3' }], next: null });
      // A move of a link that holds one stays in its row.
      link(engine, 't1', { name: 'owner', id: 'p1' });
      assert.deepEqual(engine.instances.invokeSchema(alice, 'Task', 'listLinked', { name: 'owner', id: 'p1' }), { items: [{ id: 't1' }, { id: 't2' }], next: null });
      // The references stay: p1's delete unlinks the owners that point at it.
      assert.equal(engine.instances.delete(alice, 'Person', 'p1'), true);
      assert.deepEqual(linksOf(engine, 'Task', 't1'), { spec: { schema: 'Spec', id: 's1', revision: 1, latest: 1, stale: false } });
      // A many-valued link a new version adds holds a row per target.
      publish(engine, tasks({ ...links, reviewers: { schema: 'Person', many: true, max: 2 } }));
      engine.instances.create(alice, 'Person', { title: 'p3' }, { id: 'p3' });
      link(engine, 't2', { name: 'reviewers', id: 'p2' });
      link(engine, 't2', { name: 'reviewers', id: 'p3' });
      assert.deepEqual(linksOf(engine, 'Task', 't2'), { reviewers: [person('p2'), person('p3')] });
    });
  });

  describe(`what reads one target of a link refuses a many-valued link (${driver})`, () => {
    const prefix = (type: string) => `type ${type}: behavior Reactions config: `;
    const refused = (engine: Engine, document: Record<string, unknown>): string =>
      thrown(() => engine.schemas.define(alice, document), SchemaDocumentError)
        .issues.map((issue) => issue.message)
        .join('; ');
    const reactions = (...rules: unknown[]): BehaviorRef => ({ name: 'Reactions', config: { rules } });
    const projects: BehaviorRef = { name: 'Links', config: { links: { projects: { schema: 'Project', many: true, max: 3 } } } };

    test("Reactions refuses a then.link, and an allTerminal, an anyTerminal or a holds rule's link, that holds a list", () => {
      const engine = open();
      publish(engine, schema('Project', [flow]));
      assert.equal(
        refused(engine, schema('Task', [flow, projects, reactions({ when: { enters: 'doing' }, then: { link: 'projects', transition: 'doing' } })])),
        `${prefix('Task')}rule 1: then.link projects holds a list of targets, and the rule reads the one instance it points at`
      );
      const parents: BehaviorRef = { name: 'Links', config: { links: { parents: { schema: 'Task', many: true, max: 2 } } } };
      assert.equal(
        refused(engine, schema('Task', [flow, parents, reactions({ when: { allTerminal: { schema: 'Task', link: 'parents' } }, then: { transition: 'done' } })])),
        `${prefix('Task')}rule 1: when.allTerminal names link parents of Task, which holds a list of targets, and the rule reads the one instance it points at`
      );
      // A linked schema's live version is read when the schema is defined.
      publish(engine, schema('Task', [flow, projects]));
      assert.equal(
        refused(engine, schema('Project', [flow, reactions({ when: { anyTerminal: { schema: 'Task', link: 'projects', outcomes: ['success'] } }, then: { transition: 'done' } })])),
        `${prefix('Project')}rule 1: when.anyTerminal names link projects of Task, which holds a list of targets, and the rule reads the one instance it points at`
      );
      // Rollups reads across the list; a holds rule on it reads one instance.
      const rollups: BehaviorRef = { name: 'Rollups', config: { rollups: { finished: { schema: 'Task', link: 'projects', function: 'all' } } } };
      engine.schemas.define(alice, schema('Project', [flow, rollups]));
      assert.equal(
        refused(engine, schema('Project', [flow, rollups, reactions({ when: { holds: 'finished' }, then: { transition: 'done' } })])),
        `${prefix('Project')}rule 1: when.holds names rollup finished, whose link projects of Task holds a list of targets, and the rule reads the one instance it points at`
      );
    });

    test('a linked schema whose link holds a list since the rule was defined halts the subscription with what is wrong', () => {
      const engine = open({ runner: { principal: runnerPrincipal, maxAttempts: 1 } });
      publish(engine, schema('Project', [flow, reactions({ when: { allTerminal: { schema: 'Task', link: 'projects' } }, then: { transition: 'done' } })]));
      publish(engine, schema('Task', [flow, projects]));
      engine.instances.create(alice, 'Project', { title: 'p1' }, { id: 'p1' });
      engine.instances.create(alice, 'Task', { title: 't1' }, { id: 't1', behaviors: { Links: { projects: ['p1'] } } });
      engine.runner.runDue();
      const project = engine.runner.status().subscriptions.find((subscription) => subscription.schema === 'Project') as SubscriptionStatus;
      assert.deepEqual(
        [project.state, project.failure?.error],
        ['halted', 'BehaviorError: behavior Reactions: allTerminal names link projects of Task, which holds a list of targets, and the rule reads the one instance it points at']
      );
    });

    test('Rollups counts an instance in every target its list holds, and a revised rule hears each target of the list', () => {
      const engine = open({ runner: { principal: runnerPrincipal } });
      publish(engine, schema('Spec', [{ name: 'Revisions' }]));
      publish(engine, schema('Project', []));
      const specs = { schema: 'Spec', many: true, max: 2, pinned: true };
      const stale: BehaviorRef = {
        name: 'Workflow',
        config: { states: ['ready', 'stale'], transitions: [{ from: 'ready', to: 'stale' }] },
      };
      publish(
        engine,
        schema('Task', [stale, { name: 'Links', config: { links: { projects: { schema: 'Project', many: true, max: 3 }, specs } } }, reactions({ when: { revised: { link: 'specs' } }, then: { transition: 'stale' } })])
      );
      publish(engine, schema('Project', [{ name: 'Rollups', config: { rollups: { tasks: { schema: 'Task', link: 'projects', function: 'count' } } } }]));
      for (const id of ['p1', 'p2']) {
        engine.instances.create(alice, 'Project', { title: id }, { id });
      }
      for (const id of ['s1', 's2']) {
        engine.instances.create(alice, 'Spec', { title: id }, { id });
      }
      engine.instances.create(alice, 'Task', { title: 't1' }, { id: 't1', behaviors: { Links: { projects: ['p1', 'p2'], specs: ['s1', 's2'] } } });
      engine.instances.create(alice, 'Task', { title: 't2' }, { id: 't2', behaviors: { Links: { projects: ['p1'], specs: ['s2'] } } });
      const rollup = (id: string) => (engine.instances.get(alice, 'Project', id)?.behaviors.Rollups.values as { tasks: number }).tasks;
      assert.deepEqual([rollup('p1'), rollup('p2')], [2, 1]);
      engine.runner.runDue();
      engine.instances.update(alice, 'Spec', 's1', { title: 's1, revised' });
      engine.runner.runDue();
      const status = (id: string) => engine.instances.get(alice, 'Task', id)?.behaviors.Workflow.status;
      assert.deepEqual([status('t1'), status('t2')], ['stale', 'ready']);
    });
  });
}
