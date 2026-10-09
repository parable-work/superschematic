// Queue's exclusion, and what keeps it current: a write to an enclosing
// budget scope reaches only the waiting instances whose fit it can flip,
// counted by the checks and hooks it runs; a daily scope whose
// reservations settle lets its waiting work in at the next day; a scope
// whose own scope link moves wakes what waits under it; a blocker is
// heard only when its status moves; and excludeStale keeps work pinned to
// a superseded revision or release out of claimNext and claim. Real
// SQLite, a real engine, a clock the tests move.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import {
  BehaviorVetoError,
  SchemaDocumentError,
  type AnyBehaviorImplementation,
  type Engine,
  type Principal,
} from '@superschematic/engine';

import { assignment, blueprint, budget, lease, presence, queue, retries } from '../dist/index.js';
import {
  Clock,
  alice,
  cleanup,
  drivers,
  fenced,
  jobFlow,
  jobsDocument,
  openTestEngine,
  publish,
  recipesDocument,
  releaseRecipe,
  thrown,
  type BehaviorRef,
} from './helpers.ts';

afterEach(cleanup);

const worker: Principal = { subject: 'wren', permissions: [] };
const pam: Principal = { subject: 'pam', permissions: ['pools.limit'] };

const T0 = 1_000_000;
const DAY = 86_400_000;

const requeue = { onExpiry: { transition: 'queued', from: ['running'] } };
const claimable = { claim: { from: ['queued'], to: 'running' }, priorityField: 'priority', match: ['topic', 'urgent'] };

// A pool or a run: a title, and the behaviors given.
function scopeDocument(name: string, behaviors: readonly BehaviorRef[]): Record<string, unknown> {
  return {
    kind: 'General',
    name,
    types: { [name]: { name, role: 'EmbeddedStruct', behaviors, fields: [{ name: 'title', typeRef: { name: 'string' }, required: true }] } },
  };
}

/** The work-queue behaviors, with Budget's checkReserve and Queue's afterReferenceChange counted. */
function counting(): { calls: { checks: number; hooks: number }; behaviors: AnyBehaviorImplementation[] } {
  const calls = { checks: 0, hooks: 0 };
  const operations = budget.operations as NonNullable<typeof budget.operations>;
  const checkReserve = operations.checkReserve;
  const hook = queue.afterReferenceChange as NonNullable<typeof queue.afterReferenceChange>;
  return {
    calls,
    behaviors: [
      lease,
      assignment,
      {
        ...queue,
        afterReferenceChange(context, reference, change) {
          calls.hooks += 1;
          hook.call(queue, context, reference, change);
        },
      },
      presence,
      blueprint,
      {
        ...budget,
        operations: {
          ...operations,
          checkReserve(context, params) {
            calls.checks += 1;
            return checkReserve.call(operations, context, params);
          },
        },
      },
      retries,
    ],
  };
}

for (const driver of drivers) {
  const create = (engine: Engine, id: string, data: Record<string, unknown> = {}, behaviors?: Record<string, unknown>) =>
    engine.instances.create(alice, 'Job', { title: id, ...data }, { id, ...(behaviors === undefined ? {} : { behaviors }) });
  const claimNext = (engine: Engine, who: Principal = worker) =>
    (engine.instances.invokeSchema(who, 'Job', 'claimNext', {}) as { claimed: { id: string; token: number } | null }).claimed;
  const count = (engine: Engine) => (engine.instances.invokeSchema(worker, 'Job', 'countClaimable', {}) as { count: number }).count;
  const cursorOf = (engine: Engine) => engine.events.read(alice, { limit: 500 }).events.at(-1)?.cursor ?? 0;
  const refreshes = (engine: Engine, after: number) =>
    engine.events
      .read(alice, { schema: 'Job', after, limit: 500 })
      .events.filter((event) => (event.change as { operation?: string } | null)?.operation === 'refresh')
      .map((event) => event.instanceId as string);
  const excludedUntil = (engine: Engine, id: string) =>
    Number(engine.storage.get(`SELECT bhv_queue__excluded_until AS until FROM engine_instances WHERE schema = 'Job' AND id = ?`, [id])?.until);

  describe(`Queue: a write to a scope reaches only what it can let in or keep out (${driver})`, () => {
    // pooled opens an engine whose Job draws cpu from the Pool its pool link
    // points at: 10 a claim, or its timeLimitMs when it holds one; p1's
    // limit is 20. Two jobs at the head of the order, and 20 that claim 10
    // and 20 that claim 5 behind them.
    function pooled(behaviors: readonly AnyBehaviorImplementation[]) {
      const engine = openTestEngine({ driver, clock: new Clock(T0).now, behaviors });
      publish(engine, scopeDocument('Pool', [{ name: 'Budget', config: { meters: { cpu: { limit: 20 } }, limitPermission: 'pools.limit' } }]));
      publish(
        engine,
        jobsDocument([
          { name: 'Workflow', config: jobFlow },
          { name: 'Lease', config: requeue },
          { name: 'Links', config: { links: { pool: { schema: 'Pool' } } } },
          { name: 'Queue', config: { ...claimable, maxCandidates: 2 } },
          { name: 'Budget', config: { meters: { cpu: { scope: 'pool', reserve: 10, reserveField: 'timeLimitMs' } } } },
        ])
      );
      engine.instances.create(alice, 'Pool', { title: 'Shared' }, { id: 'p1' });
      const pool = { Links: { pool: 'p1' } };
      create(engine, 'a1', { priority: 9 }, pool);
      create(engine, 'a2', { priority: 9 }, pool);
      for (let at = 1; at <= 20; at += 1) {
        create(engine, `big${at}`, { priority: 5 }, pool);
        create(engine, `small${at}`, { priority: 1, timeLimitMs: 5 }, pool);
      }
      return engine;
    }

    test('with 40 instances waiting under one scope, a write that crosses no amount runs no check and no hook, and one that does reaches only those it crosses', () => {
      const { calls, behaviors } = counting();
      const engine = pooled(behaviors);
      assert.equal(claimNext(engine)?.id, 'a1');
      const second = claimNext(engine) as { id: string; token: number };
      assert.equal(second.id, 'a2');
      // p1's 20 are reserved: every one of the 40 waits.
      assert.equal(count(engine), 0);

      const reset = () => {
        calls.checks = 0;
        calls.hooks = 0;
        return cursorOf(engine);
      };
      const smalls = Array.from({ length: 20 }, (_, at) => `small${at + 1}`);
      const bigs = Array.from({ length: 20 }, (_, at) => `big${at + 1}`);
      // Usage inside a1's reservation moves p1's used and reserved, not its
      // remaining: no waiting instance is asked anything.
      let from = reset();
      engine.instances.invoke(worker, 'Job', 'a1', 'recordUsage', { meter: 'cpu', amount: 4 }, fenced(1));
      assert.deepEqual([calls.checks, calls.hooks, refreshes(engine, from)], [0, 0, []]);
      // Usage past it takes the remaining from 0 to -6, and a2's release
      // to 4: below 5, so across no amount of the others. a2, back in the
      // queue, checks itself in its own write and waits too; its own
      // reference hears its own settlement, which its write settles.
      engine.instances.invoke(worker, 'Job', 'a1', 'recordUsage', { meter: 'cpu', amount: 12 }, fenced(1));
      engine.instances.invoke(worker, 'Job', 'a2', 'release', {}, fenced(second.token));
      assert.deepEqual([calls.checks, calls.hooks, refreshes(engine, from)], [1, 1, []]);
      assert.equal(count(engine), 0);

      // A limit of 25 leaves 9: across 5, not 10, so the 20 that claim 5
      // hear it and the rest do not. Each that hears it checks once to see
      // its fit moved, and once more in the refresh that lets it in.
      from = reset();
      engine.instances.invoke(pam, 'Pool', 'p1', 'setLimit', { meter: 'cpu', limit: 25 });
      assert.deepEqual([calls.checks, calls.hooks], [40, 20]);
      assert.deepEqual(refreshes(engine, from).sort(), [...smalls].sort());
      assert.equal(count(engine), 20);
      // 30 leaves 14: across 10, so the 20 that claim 10 and a2 come in.
      from = reset();
      engine.instances.invoke(pam, 'Pool', 'p1', 'setLimit', { meter: 'cpu', limit: 30 });
      assert.deepEqual([calls.checks, calls.hooks], [42, 21]);
      assert.deepEqual(refreshes(engine, from).sort(), ['a2', ...bigs].sort());
      assert.equal(count(engine), 41);
      // With every one of the 41 waiting on it, a write that crosses no amount still asks none of them.
      from = reset();
      engine.instances.invoke(worker, 'Job', 'a1', 'recordUsage', { meter: 'cpu', amount: 1 }, fenced(1));
      assert.deepEqual([calls.checks, calls.hooks, refreshes(engine, from)], [0, 0, []]);
    });

    test('each waiting instance hears the values of its scopes the answer turns on, at its own amount', () => {
      const engine = pooled([lease, assignment, queue, presence, blueprint, budget, retries]);
      const heard = (id: string) =>
        engine.storage
          .all(`SELECT target_id, key, hears, crosses FROM engine_references WHERE source_id = ? AND behavior = 'Queue' ORDER BY rowid`, [id])
          .map((row) => ({ ...row }));
      assert.deepEqual(heard('big1'), [
        { target_id: 'p1', key: 'budget /behaviors/Budget/meters/cpu/remaining 10', hears: '/behaviors/Budget/meters/cpu/remaining', crosses: 10 },
      ]);
      assert.deepEqual(heard('small1'), [
        { target_id: 'p1', key: 'budget /behaviors/Budget/meters/cpu/remaining 5', hears: '/behaviors/Budget/meters/cpu/remaining', crosses: 5 },
      ]);
      // A claimed instance is no candidate, and hears nothing.
      claimNext(engine);
      assert.deepEqual(heard('a1'), []);
    });
  });

  describe(`Queue: a daily scope and a scope's own scope (${driver})`, () => {
    test("a daily scope's settled reservations let its waiting work in at the next day, with no change then", () => {
      const clock = new Clock(T0);
      const engine = openTestEngine({ driver, clock: clock.now });
      publish(engine, scopeDocument('Pool', [{ name: 'Budget', config: { meters: { cpu: { limit: 30, reset: 'daily' } } } }]));
      publish(
        engine,
        jobsDocument([
          { name: 'Workflow', config: jobFlow },
          { name: 'Lease', config: requeue },
          { name: 'Links', config: { links: { pool: { schema: 'Pool' } } } },
          { name: 'Queue', config: { ...claimable, maxCandidates: 1 } },
          { name: 'Budget', config: { meters: { cpu: { scope: 'pool', reserve: 10 } } } },
        ])
      );
      engine.instances.create(alice, 'Pool', { title: 'Shared' }, { id: 'p1' });
      for (const [id, priority] of [['a1', 9], ['a2', 8], ['a3', 7], ['w', 1]] as const) {
        create(engine, id, { priority }, { Links: { pool: 'p1' } });
      }
      const tokens = [claimNext(engine), claimNext(engine), claimNext(engine)].map((claimed) => claimed?.token);
      assert.deepEqual(tokens, [1, 1, 1]);
      // 30 reserved: w fits neither today nor tomorrow, so only a change lets it in.
      assert.equal(excludedUntil(engine, 'w'), Number.MAX_SAFE_INTEGER);
      // a1 uses 15: the pool has used 15 and holds 20. w still does not fit
      // today, but 20 held and its 10 fit a fresh day: it waits for the day.
      const from = cursorOf(engine);
      engine.instances.invoke(worker, 'Job', 'a1', 'recordUsage', { meter: 'cpu', amount: 15 }, fenced(1));
      assert.deepEqual(refreshes(engine, from), ['w']);
      assert.equal(excludedUntil(engine, 'w'), DAY);
      assert.equal(claimNext(engine), null);
      clock.ms = DAY;
      assert.equal(claimNext(engine)?.id, 'w');
    });

    test("a scope whose own scope link moves wakes what waits under it", () => {
      const engine = openTestEngine({ driver, clock: new Clock(T0).now });
      publish(engine, scopeDocument('Pool', [{ name: 'Budget', config: { meters: { cpu: { limit: 5 } }, limitPermission: 'pools.limit' } }]));
      publish(
        engine,
        scopeDocument('Run', [
          { name: 'Links', config: { links: { pool: { schema: 'Pool' } } } },
          { name: 'Budget', config: { meters: { cpu: { scope: 'pool' } } } },
        ])
      );
      publish(
        engine,
        jobsDocument([
          { name: 'Workflow', config: jobFlow },
          { name: 'Lease', config: requeue },
          { name: 'Links', config: { links: { run: { schema: 'Run' } } } },
          { name: 'Queue', config: claimable },
          { name: 'Budget', config: { meters: { cpu: { scope: 'run', reserve: 10 } } } },
        ])
      );
      engine.instances.create(alice, 'Pool', { title: 'Small' }, { id: 'p1' });
      engine.instances.create(alice, 'Pool', { title: 'Large' }, { id: 'p2' });
      engine.instances.invoke(pam, 'Pool', 'p2', 'setLimit', { meter: 'cpu', limit: 100 });
      engine.instances.create(alice, 'Run', { title: 'Nightly' }, { id: 'r1', behaviors: { Links: { pool: 'p1' } } });
      create(engine, 'j1', {}, { Links: { run: 'r1' } });
      assert.equal(count(engine), 0);
      // r1 has no limit: its remaining never moves, but its pool link does.
      const from = cursorOf(engine);
      engine.instances.invoke(alice, 'Run', 'r1', 'link', { name: 'pool', id: 'p2' });
      assert.deepEqual(refreshes(engine, from), ['j1']);
      assert.equal(claimNext(engine)?.id, 'j1');
    });
  });

  describe(`Queue: a blocker is heard when its status moves (${driver})`, () => {
    test("a blocker's change that leaves its status refreshes no dependent; one that moves it does", () => {
      const engine = openTestEngine({ driver });
      const stepFlow = { states: ['todo', 'done'], transitions: [{ from: 'todo', to: 'done' }] };
      publish(engine, scopeDocument('Step', [{ name: 'Workflow', config: stepFlow }]));
      publish(
        engine,
        jobsDocument([
          { name: 'Workflow', config: jobFlow },
          { name: 'Lease', config: requeue },
          { name: 'Queue', config: claimable },
          { name: 'Dependencies', config: { schemas: ['Job', 'Step'] } },
        ])
      );
      engine.instances.create(alice, 'Step', { title: 'Approve' }, { id: 's1' });
      for (const id of ['j1', 'j2']) {
        create(engine, id, {}, { Dependencies: { blockers: [{ schema: 'Step', id: 's1' }] } });
      }
      let from = cursorOf(engine);
      engine.instances.update(alice, 'Step', 's1', { title: 'Approve the plan' });
      assert.deepEqual(refreshes(engine, from), []);
      from = cursorOf(engine);
      engine.instances.invoke(alice, 'Step', 's1', 'transition', { to: 'done' });
      assert.deepEqual(refreshes(engine, from), ['j1', 'j2']);
      assert.equal(count(engine), 2);
    });
  });

  describe(`Queue: excludeStale (${driver})`, () => {
    // specced opens an engine whose Job pins its spec link to a Spec's
    // revision and is kept out while the spec has moved past it.
    function specced(extra: readonly BehaviorRef[] = [], behaviors?: readonly AnyBehaviorImplementation[]) {
      const engine = openTestEngine({ driver, clock: new Clock(T0).now, ...(behaviors === undefined ? {} : { behaviors }) });
      publish(engine, scopeDocument('Spec', [{ name: 'Revisions' }]));
      publish(
        engine,
        jobsDocument([
          { name: 'Workflow', config: jobFlow },
          { name: 'Lease', config: requeue },
          { name: 'Links', config: { links: { spec: { schema: 'Spec', pinned: true }, design: { schema: 'Spec' } } } },
          { name: 'Queue', config: { ...claimable, excludeStale: ['spec'] } },
          ...extra,
        ])
      );
      engine.instances.create(alice, 'Spec', { title: 'v1' }, { id: 'sp1' });
      return engine;
    }

    test('an instance whose pinned link the target has moved past is no candidate and cannot be claimed, until it is linked again', () => {
      const engine = specced();
      create(engine, 'j1', { priority: 9 }, { Links: { spec: 'sp1' } });
      create(engine, 'j2', { priority: 1 });
      assert.equal(count(engine), 2);
      // The spec's next revision crosses the one j1 pins past it: j1 is refreshed out, as the writer.
      let from = cursorOf(engine);
      engine.instances.update(alice, 'Spec', 'sp1', { title: 'v2' });
      assert.deepEqual(refreshes(engine, from), ['j1']);
      assert.equal(excludedUntil(engine, 'j1'), Number.MAX_SAFE_INTEGER);
      assert.equal(count(engine), 1);
      const refused = thrown(() => engine.instances.invoke(worker, 'Job', 'j1', 'claim', {}), BehaviorVetoError);
      assert.deepEqual(
        [refused.behavior, refused.reason, refused.vetoCode, refused.vetoDetails],
        ['Queue', 'its link spec is pinned to a revision its target has moved past', 'stale_link', { links: ['spec'] }]
      );
      // Stale, it hears the spec no more: another revision refreshes nothing.
      from = cursorOf(engine);
      engine.instances.update(alice, 'Spec', 'sp1', { title: 'v3' });
      assert.deepEqual(refreshes(engine, from), []);
      // Pinned again, to revision 3, it is back, and hears the next one.
      engine.instances.invoke(alice, 'Job', 'j1', 'link', { name: 'spec', id: 'sp1' });
      assert.deepEqual(
        engine.storage.all(`SELECT key, hears, crosses FROM engine_references WHERE source_id = 'j1' AND behavior = 'Queue'`).map((row) => ({ ...row })),
        [{ key: 'stale spec', hears: '/behaviors/Revisions/revision', crosses: 4 }]
      );
      assert.equal(claimNext(engine)?.id, 'j1');
    });

    test("the delete of a pinned link's target lets go of it, whichever reference hears the delete first", () => {
      const engine = specced();
      create(engine, 'j1', {}, { Links: { spec: 'sp1' } });
      // Linked again to the same revision: Links records its reference
      // anew, after Queue's, so Queue's hears the delete first.
      engine.instances.invoke(alice, 'Job', 'j1', 'link', { name: 'spec', id: 'sp1' });
      assert.deepEqual(
        engine.storage.all(`SELECT behavior FROM engine_references WHERE source_id = 'j1' ORDER BY rowid`).map((row) => row.behavior),
        ['Queue', 'Links']
      );
      assert.equal(engine.instances.delete(alice, 'Spec', 'sp1'), true);
      assert.deepEqual(engine.instances.get(alice, 'Job', 'j1')?.behaviors.Links, {});
      assert.deepEqual(engine.storage.all(`SELECT key FROM engine_references WHERE source_id = 'j1'`), []);
      assert.equal(claimNext(engine)?.id, 'j1');
    });

    test('a pin to an earlier revision is stale from its create; an unpinned link and a link not named are never stale', () => {
      const engine = specced();
      engine.instances.update(alice, 'Spec', 'sp1', { title: 'v2' });
      create(engine, 'old', { priority: 9 }, { Links: { spec: { id: 'sp1', revision: 1 } } });
      create(engine, 'loose', { priority: 5 }, { Links: { design: 'sp1' } });
      assert.equal(excludedUntil(engine, 'old'), Number.MAX_SAFE_INTEGER);
      assert.equal(claimNext(engine)?.id, 'loose');
      assert.equal(claimNext(engine), null);
    });

    test('with Budget beside it, a stale link keeps the instance out without a budget check, and it hears nothing more', () => {
      const { calls, behaviors } = counting();
      const engine = specced([{ name: 'Budget', config: { meters: { cpu: { limit: 100, reserve: 10 } } } }], behaviors);
      create(engine, 'j1', {}, { Links: { spec: 'sp1' } });
      assert.equal(calls.checks, 1);
      engine.instances.update(alice, 'Spec', 'sp1', { title: 'v2' });
      assert.deepEqual([calls.checks, calls.hooks], [1, 1]);
      assert.equal(excludedUntil(engine, 'j1'), Number.MAX_SAFE_INTEGER);
      assert.deepEqual(
        engine.storage.all(`SELECT key FROM engine_references WHERE source_id = 'j1' AND behavior = 'Queue'`).map((row) => row.key),
        []
      );
    });

    test('parseConfig holds excludeStale to pinned links of the type', () => {
      const engine = openTestEngine({ driver });
      publish(engine, scopeDocument('Spec', [{ name: 'Revisions' }]));
      const refusal = (behaviors: BehaviorRef[]) =>
        thrown(() => engine.schemas.define(alice, jobsDocument([{ name: 'Workflow', config: jobFlow }, { name: 'Lease', config: requeue }, ...behaviors])), SchemaDocumentError)
          .message;
      const queueOn = (excludeStale: string[]): BehaviorRef => ({ name: 'Queue', config: { ...claimable, excludeStale } });
      const linked: BehaviorRef = { name: 'Links', config: { links: { spec: { schema: 'Spec', pinned: true }, design: { schema: 'Spec' } } } };
      assert.match(refusal([queueOn(['spec'])]), /behavior Queue config: excludeStale names links of Links, which the type does not list/);
      assert.match(refusal([linked, queueOn(['plan'])]), /excludeStale names "plan", which is not a link of the type's Links config \(its links: spec, design\)/);
      assert.match(refusal([linked, queueOn(['design'])]), /excludeStale names "design", a link that is not pinned, so it is never stale/);
      engine.schemas.define(alice, jobsDocument([{ name: 'Workflow', config: jobFlow }, { name: 'Lease', config: requeue }, linked, queueOn(['spec'])]));
      const released: BehaviorRef = { name: 'Links', config: { links: { spec: { schema: 'Spec', pinned: 'release' } } } };
      engine.schemas.define(alice, jobsDocument([{ name: 'Workflow', config: jobFlow }, { name: 'Lease', config: requeue }, released, queueOn(['spec'])]));
    });

    test('a link pinned to a release keeps its instance out once the target is released again, hearing its release', () => {
      const engine = openTestEngine({ driver, clock: new Clock(T0).now });
      publish(engine, recipesDocument());
      publish(
        engine,
        jobsDocument([
          { name: 'Workflow', config: jobFlow },
          { name: 'Lease', config: requeue },
          { name: 'Links', config: { links: { recipe: { schema: 'Recipe', pinned: 'release' } } } },
          { name: 'Queue', config: { ...claimable, excludeStale: ['recipe'] } },
        ])
      );
      engine.instances.create(alice, 'Recipe', { title: 'Soup' }, { id: 'soup' });
      releaseRecipe(engine, 'soup');
      create(engine, 'j1', { priority: 9 }, { Links: { recipe: 'soup' } });
      create(engine, 'j2', { priority: 1 });
      assert.deepEqual(
        engine.storage.all(`SELECT key, hears, crosses FROM engine_references WHERE source_id = 'j1' AND behavior = 'Queue'`).map((row) => ({ ...row })),
        [{ key: 'stale recipe', hears: '/behaviors/Branches/release', crosses: 2 }]
      );
      // The recipe's next release crosses the one j1 pins past it: j1 is refreshed out.
      const from = cursorOf(engine);
      assert.equal(releaseRecipe(engine, 'soup'), 2);
      assert.deepEqual(refreshes(engine, from), ['j1']);
      assert.equal(excludedUntil(engine, 'j1'), Number.MAX_SAFE_INTEGER);
      const refused = thrown(() => engine.instances.invoke(worker, 'Job', 'j1', 'claim', {}), BehaviorVetoError);
      assert.deepEqual(
        [refused.reason, refused.vetoCode, refused.vetoDetails],
        ['its link recipe is pinned to a release its target has moved past', 'stale_link', { links: ['recipe'] }]
      );
      assert.equal(claimNext(engine)?.id, 'j2');
      // Pinned again, to release 2, it is back.
      engine.instances.invoke(alice, 'Job', 'j1', 'link', { name: 'recipe', id: 'soup' });
      assert.equal(claimNext(engine)?.id, 'j1');
    });
  });
}
