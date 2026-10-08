// The work-queue behaviors' filters: a list keeps the instances assigned
// to a principal or to none (Assignment.assignee), whose lease a
// principal holds or that are free (Lease.holder), and whose retries are
// exhausted (Retries.exhausted), each by its qualified name, as its field
// reads and through an index on its column; the list tool says so.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { FILTER_SCAN_ROWS, type Engine, type Principal } from '@superschematic/engine';

import { Clock, alice, cleanup, drivers, fenced, jobFlow, jobsDocument, openTestEngine, publish } from './helpers.ts';

afterEach(cleanup);

const wren: Principal = { subject: 'wren', permissions: [] };
const otto: Principal = { subject: 'otto', permissions: [] };

const retries = { name: 'Retries', config: { classes: { flaky: { attempts: 1 } }, totalAttempts: 3, exhaustedState: 'failed' } };

for (const driver of drivers) {
  // world opens an engine whose Job composes Workflow, Lease, Assignment
  // and Retries, with jobs j1 to j4.
  function world(count = 4): { engine: Engine; clock: Clock } {
    const clock = new Clock();
    const engine = openTestEngine({ driver, clock: clock.now });
    publish(engine, jobsDocument([{ name: 'Workflow', config: jobFlow }, { name: 'Lease', config: { ttlMs: 60_000 } }, { name: 'Assignment' }, retries]));
    engine.storage.transaction(() => {
      for (let index = 1; index <= count; index += 1) {
        engine.instances.create(alice, 'Job', { title: `j${index}` }, { id: `j${index}` });
      }
    });
    return { engine, clock };
  }

  const ids = (engine: Engine, where: Record<string, unknown>): string[] => engine.instances.list(alice, 'Job', { where }).items.map((item) => item.id);

  describe(`work-queue filters (${driver})`, () => {
    test('a list keeps the instances assigned to a principal, or to none, as the assignee field reads', () => {
      const { engine } = world();
      engine.instances.invoke(wren, 'Job', 'j1', 'assign', { to: 'wren' });
      engine.instances.invoke(otto, 'Job', 'j2', 'assign', { to: 'otto' });
      engine.instances.invoke(wren, 'Job', 'j3', 'assign', { to: 'wren' });
      engine.instances.invoke(wren, 'Job', 'j3', 'unassign');
      assert.deepEqual(ids(engine, { 'Assignment.assignee': 'wren' }), ['j1']);
      assert.deepEqual(ids(engine, { 'Assignment.assignee': null }), ['j3', 'j4']);
      // My work, or work no one has: what a principal picks from.
      assert.deepEqual(ids(engine, { 'Assignment.assignee': [null, 'wren'] }), ['j1', 'j3', 'j4']);
      for (const item of engine.instances.list(alice, 'Job', { where: { 'Assignment.assignee': null } }).items) {
        assert.deepEqual(item.behaviors.Assignment, {});
      }
    });

    test('a list keeps the instances whose lease a principal holds, a lapsed one until its expiry is applied, or the free ones', () => {
      const { engine, clock } = world();
      engine.instances.invoke(wren, 'Job', 'j1', 'acquire', {});
      engine.instances.invoke(otto, 'Job', 'j2', 'acquire', {});
      const { token } = engine.instances.invoke(wren, 'Job', 'j3', 'acquire', {}) as { token: number };
      engine.instances.invoke(wren, 'Job', 'j3', 'release', {}, fenced(token));
      assert.deepEqual(ids(engine, { 'Lease.holder': 'wren' }), ['j1']);
      assert.deepEqual(ids(engine, { 'Lease.holder': null }), ['j3', 'j4']);
      // Lapsed, a lease is still its holder's, in the field as in the
      // filter, until the runner's sweep or an acquire applies its expiry.
      clock.advance(60_001);
      assert.deepEqual(ids(engine, { 'Lease.holder': ['wren', 'otto'] }), ['j1', 'j2']);
      const lapsed = engine.instances.get(alice, 'Job', 'j1')?.behaviors.Lease;
      assert.deepEqual([lapsed?.holder, lapsed?.active], ['wren', false]);
      engine.instances.invoke(alice, 'Job', 'j1', 'expire', {});
      assert.deepEqual(ids(engine, { 'Lease.holder': null }), ['j1', 'j3', 'j4']);
      // A free instance's holder field is absent, as the null filter keeps it.
      assert.equal(Object.hasOwn(engine.instances.get(alice, 'Job', 'j1')?.behaviors.Lease ?? {}, 'holder'), false);
    });

    test('a list keeps the instances whose retries are exhausted, or the others', () => {
      const { engine } = world();
      const attempt = engine.instances.invoke(alice, 'Job', 'j2', 'recordAttempt', { failure: 'flaky' }) as { exhausted: boolean };
      assert.equal(attempt.exhausted, true);
      assert.deepEqual(ids(engine, { 'Retries.exhausted': true }), ['j2']);
      assert.deepEqual(ids(engine, { 'Retries.exhausted': false, 'Workflow.status': 'queued' }), ['j1', 'j3', 'j4']);
      assert.equal(engine.instances.get(alice, 'Job', 'j2')?.behaviors.Retries?.exhausted, true);
    });

    test('each reads through an index on its column: a page holds every match among more instances than one scan reads', () => {
      const total = FILTER_SCAN_ROWS + 200;
      const { engine } = world(total);
      const late = (back: number) => `j${total - back}`;
      engine.instances.invoke(wren, 'Job', 'j1', 'assign', { to: 'wren' });
      engine.instances.invoke(wren, 'Job', late(0), 'assign', { to: 'wren' });
      engine.instances.invoke(otto, 'Job', 'j2', 'acquire', {});
      engine.instances.invoke(otto, 'Job', late(1), 'acquire', {});
      engine.instances.invoke(alice, 'Job', 'j3', 'recordAttempt', { failure: 'flaky' });
      engine.instances.invoke(alice, 'Job', late(2), 'recordAttempt', { failure: 'flaky' });
      for (const [where, expected] of [
        [{ 'Assignment.assignee': 'wren' }, ['j1', late(0)]],
        [{ 'Lease.holder': 'otto' }, ['j2', late(1)]],
        [{ 'Retries.exhausted': true }, ['j3', late(2)]],
      ] as const) {
        const page = engine.instances.list(alice, 'Job', { where });
        assert.deepEqual([page.items.map((item) => item.id), page.next], [expected, null], JSON.stringify(where));
      }
    });

    test("the list tool's where takes each, null among its values, and each behavior says so", () => {
      const { engine } = world();
      const list = engine.tools.describe(alice, 'Job').operations.find((operation) => operation.name === 'list');
      const where = (list?.params.properties as Record<string, { properties: Record<string, { anyOf: Array<Record<string, unknown>> }> }>).where;
      assert.deepEqual(Object.keys(where.properties), [
        'title',
        'priority',
        'timeLimitMs',
        'topic',
        'urgent',
        'Workflow.status',
        'Lease.holder',
        'Assignment.assignee',
        'Retries.exhausted',
      ]);
      assert.deepEqual(where.properties['Lease.holder'].anyOf[0], {
        type: ['string', 'null'],
        description: 'The principal that holds its lease, a lapsed one included until its expiry is applied; null for a free instance.',
      });
      assert.deepEqual(where.properties['Assignment.assignee'].anyOf[0], {
        type: ['string', 'null'],
        description: 'The principal the instance is assigned to; absent when it is unassigned.',
      });
      assert.deepEqual(where.properties['Retries.exhausted'].anyOf[0], { type: ['boolean', 'null'], description: 'Whether its retries are exhausted.' });
      const useWhen = list?.guidance.useWhen ?? '';
      assert.match(useWhen, /where: \{ "Lease\.holder": <subject> \} lists the Job instances whose lease a principal holds, a lapsed one until the runner expires it, and "Lease\.holder": null the free ones\./);
      assert.match(
        useWhen,
        /where: \{ "Assignment\.assignee": <subject> \} lists the Job instances assigned to a principal, "Assignment\.assignee": null the unassigned ones, and \[null, <subject>\] both\./
      );
      assert.match(useWhen, /where: \{ "Retries\.exhausted": true \} lists the Job instances whose retries are exhausted, and false the ones that may run again\./);
    });
  });
}
