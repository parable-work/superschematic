// Retries: attempts counted against per-class caps and a total, narrowed
// or widened by the instance's limitsField; exhaustion at a terminal class,
// a cap or a stuck signature, which moves the status through Workflow only
// from the from states and then holds the instance; kept results, the best
// scoring one with keepBest, written to resultField with an update's
// checks; and the config rules. Real SQLite, a real engine.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import {
  BehaviorVetoError,
  EngineError,
  IncompatibleChangeError,
  InstanceValidationError,
  OperationParamsError,
  SchemaDocumentError,
  type Engine,
  type Principal,
} from '@superschematic/engine';

import { Clock, alice, cleanup, drivers, fenced, jobFlow, openTestEngine, publish, thrown, type BehaviorRef } from './helpers.ts';

afterEach(cleanup);

const worker: Principal = { subject: 'wren', permissions: [] };
const other: Principal = { subject: 'otto', permissions: [] };

const classes = { timeout: { attempts: 3 }, invalid: { attempts: 2 }, rejected: 'terminal' };
const base = { classes, totalAttempts: 4, exhaustedState: 'failed' };

// retriesDocument is a General schema named Job whose instance type has a
// title, an output string of at most 20 characters, and two JSON fields,
// report and caps, and composes the behaviors given.
function retriesDocument(behaviors: readonly BehaviorRef[]): Record<string, unknown> {
  return {
    kind: 'General',
    name: 'Job',
    types: {
      Job: {
        name: 'Job',
        role: 'EmbeddedStruct',
        behaviors,
        fields: [
          { name: 'title', typeRef: { name: 'string' }, required: true },
          { name: 'output', typeRef: { name: 'string' }, validateMaxLength: 20 },
          { name: 'report', typeRef: { name: 'Generic.JSON' } },
          { name: 'caps', typeRef: { name: 'Generic.JSON' } },
        ],
      },
    },
  };
}

interface Attempt {
  failure: string | null;
  score: number | null;
  kept: boolean;
  total: number;
  classAttempts: Record<string, number>;
  exhausted: boolean;
  stuck: boolean;
}

for (const driver of drivers) {
  // world opens an engine whose Job composes Workflow, Lease and Retries
  // with the config given, with job j1 queued.
  function world(config: Record<string, unknown>, data: Record<string, unknown> = {}, behaviors: BehaviorRef[] = [{ name: 'Lease' }]) {
    const engine = openTestEngine({ driver, clock: new Clock().now });
    publish(engine, retriesDocument([{ name: 'Workflow', config: jobFlow }, ...behaviors, { name: 'Retries', config }]));
    engine.instances.create(alice, 'Job', { title: 'Build', ...data }, { id: 'j1' });
    return engine;
  }

  const invoke = (engine: Engine, who: Principal, operation: string, params: Record<string, unknown> = {}, id = 'j1') =>
    engine.instances.invoke(who, 'Job', id, operation, params);
  const attempt = (engine: Engine, params: Record<string, unknown> = {}, id = 'j1', who: Principal = worker) =>
    invoke(engine, who, 'recordAttempt', params, id) as Attempt;
  const dataOf = (engine: Engine, id = 'j1') => engine.instances.get(alice, 'Job', id)?.data as Record<string, unknown>;
  const veto = (fn: () => unknown) => thrown(fn, BehaviorVetoError);
  const counts = (timeout: number, invalid: number, rejected: number) => ({ timeout, invalid, rejected });

  describe(`Retries: attempts (${driver})`, () => {
    test('a success is kept, its result written to resultField, and counts nothing; score is optional', () => {
      const engine = world({ ...base, resultField: 'report' });
      assert.deepEqual(dataOf(engine).retries, { total: 0, classAttempts: counts(0, 0, 0), bestScore: null, exhausted: false, stuck: false });
      assert.deepEqual(attempt(engine, { score: 0.5, result: { ok: true } }), {
        failure: null,
        score: 0.5,
        kept: true,
        total: 0,
        classAttempts: counts(0, 0, 0),
        exhausted: false,
        stuck: false,
      });
      assert.deepEqual(dataOf(engine).report, { ok: true });
      assert.deepEqual(attempt(engine, { result: { ok: 'again' } }).score, null);
      assert.deepEqual([dataOf(engine).report, (dataOf(engine).retries as { bestScore: number }).bestScore], [{ ok: 'again' }, 0.5]);
    });

    test('a failure names a class of the config, a result needs resultField, and each attempt is one event of its caller', () => {
      const engine = world(base);
      assert.deepEqual(thrown(() => attempt(engine, { failure: 'oom' }), OperationParamsError).issues, [
        { path: '/failure', message: 'oom is not a failure class of Job (its classes: invalid, rejected, timeout)' },
      ]);
      assert.equal(thrown(() => attempt(engine, { failure: '' }), OperationParamsError).code, 'invalid_argument');
      assert.deepEqual(thrown(() => attempt(engine, { result: 'done' }), OperationParamsError).issues, [
        { path: '/result', message: 'the config names no resultField to keep a result in' },
      ]);
      const seq = engine.instances.get(alice, 'Job', 'j1')?.seq as number;
      attempt(engine, { failure: 'timeout', signature: 'deadline' });
      attempt(engine, { failure: 'invalid' }, 'j1', other);
      const events = engine.events.read(alice, { schema: 'Job', instanceId: 'j1' }).events.slice(-2);
      assert.deepEqual(
        events.map((event) => [event.seq, event.actor, (event.change as { operation: string; params: unknown }).params]),
        [
          [seq + 1, 'wren', { failure: 'timeout', signature: 'deadline' }],
          [seq + 2, 'otto', { failure: 'invalid' }],
        ]
      );
    });

    test('a terminal class exhausts the instance at once: its status moves to exhaustedState, and it is not taken again', () => {
      const engine = world(base);
      invoke(engine, worker, 'acquire');
      invoke(engine, worker, 'transition', { to: 'running' });
      assert.deepEqual(attempt(engine, { failure: 'rejected' }), {
        failure: 'rejected',
        score: null,
        kept: false,
        total: 1,
        classAttempts: counts(0, 0, 1),
        exhausted: true,
        stuck: false,
      });
      assert.equal(dataOf(engine).status, 'failed');
      engine.instances.invoke(worker, 'Job', 'j1', 'release', {}, fenced(1 as number));
      assert.equal(veto(() => invoke(engine, other, 'acquire')).reason, 'its retries are exhausted, so it is not taken again');
      assert.equal(veto(() => attempt(engine, { failure: 'timeout' })).reason, 'its retries are exhausted');
    });

    test('failures count against their class and the total; the total, or a class with no room, exhausts', () => {
      const engine = world(base);
      engine.instances.create(alice, 'Job', { title: 'Second' }, { id: 'j2' });
      for (let n = 1; n <= 3; n += 1) {
        assert.deepEqual(attempt(engine, { failure: 'timeout' }), { ...attempt0(n), classAttempts: counts(n, 0, 0) });
      }
      // timeout is at its cap, but invalid has room; the total reaches its cap.
      assert.deepEqual(attempt(engine, { failure: 'invalid' }), {
        failure: 'invalid',
        score: null,
        kept: false,
        total: 4,
        classAttempts: counts(3, 1, 0),
        exhausted: true,
        stuck: false,
      });
      assert.equal(dataOf(engine).status, 'failed');
      const fresh = world(base);
      for (let n = 0; n < 3; n += 1) {
        attempt(fresh, { failure: 'timeout' });
      }
      // A failure of a class with no room left is not counted, and exhausts.
      assert.deepEqual(attempt(fresh, { failure: 'timeout' }), { ...attempt0(3), exhausted: true, classAttempts: counts(3, 0, 0) });

      // A class that reaches its cap while no other class has room exhausts.
      const wide = world({ ...base, totalAttempts: 10 });
      attempt(wide, { failure: 'invalid' });
      assert.equal(attempt(wide, { failure: 'invalid' }).exhausted, false);
      attempt(wide, { failure: 'timeout' });
      attempt(wide, { failure: 'timeout' });
      assert.deepEqual(attempt(wide, { failure: 'timeout' }), { ...attempt0(5), exhausted: true, classAttempts: counts(3, 2, 0) });
    });

    test('interleaved failures never pass a class cap or the total', () => {
      const engine = world({ ...base, totalAttempts: 6 });
      // A fixed pseudo-random sequence of classes, over many instances.
      let seed = 7;
      const next = () => {
        seed = (seed * 1103515245 + 12345) % 2147483648;
        return seed;
      };
      const names = ['timeout', 'invalid'];
      for (let instance = 0; instance < 20; instance += 1) {
        const id = `p${instance}`;
        engine.instances.create(alice, 'Job', { title: id }, { id });
        let last: Attempt | undefined;
        for (let step = 0; step < 12 && last?.exhausted !== true; step += 1) {
          last = attempt(engine, { failure: names[next() % 2] }, id);
          assert.ok(last.classAttempts.timeout <= 3 && last.classAttempts.invalid <= 2 && last.total <= 6, JSON.stringify(last));
          assert.equal(last.total, last.classAttempts.timeout + last.classAttempts.invalid);
        }
        assert.equal(last?.exhausted, true, `${id} exhausted`);
      }
    });

    test("limitsField narrows or widens the caps for the instance, and ignores what is not a cap of a class with one", () => {
      const config = { ...base, limitsField: 'caps' };
      const narrowed = world(config, { caps: { timeout: 1 } });
      assert.equal(attempt(narrowed, { failure: 'timeout' }).exhausted, false);
      assert.deepEqual(attempt(narrowed, { failure: 'timeout' }), { ...attempt0(1), exhausted: true, classAttempts: counts(1, 0, 0) });

      const total = world(config, { caps: { totalAttempts: 2, timeout: 5 } });
      attempt(total, { failure: 'timeout' });
      assert.deepEqual(attempt(total, { failure: 'timeout' }), { ...attempt0(2), exhausted: true, classAttempts: counts(2, 0, 0) });

      // Unknown and terminal classes, and values that are no cap, change nothing.
      const ignored = world(config, { caps: { oom: 1, rejected: 5, timeout: -1, invalid: 1.5, totalAttempts: 0 } });
      for (let n = 1; n <= 3; n += 1) {
        assert.equal(attempt(ignored, { failure: 'timeout' }).exhausted, false);
      }
      assert.equal(attempt(ignored, { failure: 'invalid' }).exhausted, true);

      // A cap of 0 makes the first failure of that class exhaust; nothing is exhausted before it, and other classes count as before.
      const zero = world(config, { caps: { timeout: 0 } });
      assert.equal((dataOf(zero).retries as { exhausted: boolean }).exhausted, false);
      assert.equal((invoke(zero, worker, 'acquire') as { token: number }).token, 1);
      assert.deepEqual(attempt(zero, { failure: 'invalid' }), { ...attempt0(1), failure: 'invalid', classAttempts: counts(0, 1, 0) });
      assert.deepEqual(attempt(zero, { failure: 'timeout' }), { ...attempt0(1), exhausted: true, classAttempts: counts(0, 1, 0) });
    });

    test('a config whose classes are all terminal exhausts nothing before the first failure', () => {
      const engine = world({ classes: { rejected: 'terminal', crashed: 'terminal' }, totalAttempts: 3, exhaustedState: 'failed' });
      assert.deepEqual(dataOf(engine).retries, { total: 0, classAttempts: { crashed: 0, rejected: 0 }, bestScore: null, exhausted: false, stuck: false });
      assert.equal((invoke(engine, worker, 'acquire') as { token: number }).token, 1);
      invoke(engine, worker, 'transition', { to: 'running' });
      assert.equal(attempt(engine, {}).exhausted, false);
      assert.equal(attempt(engine, { failure: 'crashed' }).exhausted, true);
      assert.equal(dataOf(engine).status, 'failed');
    });
  });

  describe(`Retries: kept results and stuck failures (${driver})`, () => {
    test('with keepBest, a failure is kept only when it beats the best by minDelta and loses no neverRegress predicate', () => {
      const engine = world({ ...base, totalAttempts: 10, resultField: 'report', keepBest: { minDelta: 0.1, neverRegress: ['compiles'] } });
      const failed = (score: number, compiles: boolean, v: number) => attempt(engine, { failure: 'timeout', score, predicates: { compiles }, result: { v } });
      assert.equal(failed(0.5, true, 1).kept, true);
      assert.equal(failed(0.55, true, 2).kept, false);
      // A higher score that loses a predicate the kept result had is recorded, not kept.
      assert.deepEqual(failed(0.9, false, 3), { ...attempt0(3), score: 0.9, classAttempts: counts(3, 0, 0) });
      assert.deepEqual([dataOf(engine).report, (dataOf(engine).retries as { bestScore: number }).bestScore], [{ v: 1 }, 0.5]);
      // A predicate the attempt does not report counts as failed.
      assert.equal(attempt(engine, { failure: 'invalid', score: 0.9, result: { v: 4 } }).kept, false);
      assert.equal(attempt(engine, { failure: 'invalid', score: 0.9, predicates: { compiles: true }, result: { v: 5 } }).kept, true);
      assert.deepEqual([dataOf(engine).report, (dataOf(engine).retries as { bestScore: number }).bestScore], [{ v: 5 }, 0.9]);
    });

    test("without keepBest only a success's result is kept, so no failure is measured against it", () => {
      const engine = world({ ...base, resultField: 'report' });
      assert.equal(attempt(engine, { score: 0.2, result: { v: 'success' } }).kept, true);
      assert.equal(attempt(engine, { failure: 'timeout', score: 0.9, result: { v: 'failure' } }).kept, false);
      assert.deepEqual([dataOf(engine).report, (dataOf(engine).retries as { bestScore: number }).bestScore], [{ v: 'success' }, 0.2]);
    });

    test('stuckAfter: the same signature in a row, none kept, exhausts as stuck; another signature, a kept attempt or a success ends the streak', () => {
      const stuck = world({ ...base, totalAttempts: 10, stuckAfter: 2 });
      invoke(stuck, worker, 'acquire');
      invoke(stuck, worker, 'transition', { to: 'running' });
      assert.equal(attempt(stuck, { failure: 'timeout', signature: 'a' }).stuck, false);
      assert.deepEqual(attempt(stuck, { failure: 'invalid', signature: 'a' }), { ...attempt0(2), failure: 'invalid', classAttempts: counts(1, 1, 0), exhausted: true, stuck: true });
      assert.equal(dataOf(stuck).status, 'failed');
      assert.equal(veto(() => attempt(stuck, {})).reason, 'its retries are exhausted, stuck on one failure');

      const wide = { timeout: { attempts: 5 }, invalid: { attempts: 5 } };
      const reset = world({ ...base, classes: wide, totalAttempts: 10, stuckAfter: 2, keepBest: {} });
      attempt(reset, { failure: 'timeout', signature: 'a' });
      assert.equal(attempt(reset, { failure: 'timeout', signature: 'b' }).stuck, false);
      // A kept attempt with the same signature ends the streak.
      assert.equal(attempt(reset, { failure: 'invalid', signature: 'b', score: 1 }).kept, true);
      assert.equal(attempt(reset, { failure: 'invalid', signature: 'b', score: 0.5 }).stuck, false);
      attempt(reset, {});
      assert.equal(attempt(reset, { failure: 'timeout', signature: 'b' }).stuck, false);
      assert.equal(attempt(reset, { failure: 'invalid', signature: 'b' }).stuck, true);
    });

    test('a result is written through update(), so one the live version refuses fails the attempt and records nothing', () => {
      const engine = world({ ...base, resultField: 'output' });
      const seq = engine.instances.get(alice, 'Job', 'j1')?.seq;
      const refused = thrown(() => attempt(engine, { result: 'x'.repeat(21) }), InstanceValidationError);
      assert.equal(refused.code, 'invalid_instance');
      assert.equal(thrown(() => attempt(engine, { result: 42 }), InstanceValidationError).code, 'invalid_instance');
      assert.equal(engine.instances.get(alice, 'Job', 'j1')?.seq, seq);
      assert.equal(engine.storage.get('SELECT COUNT(*) AS n FROM bhv_retries__attempts')?.n, 0);
      attempt(engine, { result: 'built' });
      assert.equal(dataOf(engine).output, 'built');
    });
  });

  describe(`Retries: exhaustion and access (${driver})`, () => {
    test('exhaustion moves the status only from the from states, so finished work stays finished', () => {
      const engine = world(base);
      invoke(engine, worker, 'acquire');
      invoke(engine, worker, 'transition', { to: 'running' });
      invoke(engine, worker, 'transition', { to: 'done' });
      assert.equal(attempt(engine, { failure: 'rejected' }).exhausted, true);
      assert.equal(dataOf(engine).status, 'done');

      const narrow = world({ ...base, from: ['running'] });
      assert.equal(attempt(narrow, { failure: 'rejected' }).exhausted, true);
      assert.equal(dataOf(narrow).status, 'queued');
      // Exhausted, its status moves only into exhaustedState.
      assert.equal(veto(() => invoke(narrow, alice, 'transition', { to: 'running' })).reason, 'its retries are exhausted, so its status moves only to failed');
      invoke(narrow, alice, 'transition', { to: 'failed' });
      assert.equal(dataOf(narrow).status, 'failed');
    });

    test('recordAttempt needs the permission the config names', () => {
      const engine = world({ ...base, permission: 'jobs.work' });
      const refused = thrown(() => attempt(engine, { failure: 'timeout' }), EngineError);
      assert.deepEqual([refused.code, refused.message], ['forbidden', 'wren may not record an attempt of Job j1: it needs permission jobs.work']);
      assert.equal(attempt(engine, { failure: 'timeout' }, 'j1', { subject: 'wren', permissions: ['jobs.work'] }).total, 1);
    });

    test("while a lease is active, Lease's guard keeps recordAttempt to its holder", () => {
      const engine = world(base);
      invoke(engine, worker, 'acquire');
      assert.equal(veto(() => attempt(engine, { failure: 'timeout' }, 'j1', other)).behavior, 'Lease');
      assert.equal(attempt(engine, { failure: 'timeout' }).total, 1);
    });
  });

  describe(`Retries: claimed through Queue (${driver})`, () => {
    // queued opens an engine whose Job composes Workflow, Lease, Queue and
    // Retries with the config given, with jobs j1 and j2 queued, j1 the older.
    function queued(config: Record<string, unknown>) {
      const engine = openTestEngine({ driver, clock: new Clock().now });
      publish(
        engine,
        retriesDocument([
          { name: 'Workflow', config: jobFlow },
          { name: 'Lease', config: { onExpiry: { transition: 'queued', from: ['running'] } } },
          { name: 'Queue', config: { claim: { from: ['queued'], to: 'running' } } },
          { name: 'Retries', config },
        ])
      );
      for (const id of ['j1', 'j2']) {
        engine.instances.create(alice, 'Job', { title: id }, { id });
      }
      return engine;
    }
    const claimNext = (engine: Engine, who: Principal = worker) =>
      (engine.instances.invokeSchema(who, 'Job', 'claimNext', {}) as { claimed: { id: string; token: number } | null }).claimed;

    test('an exhausted instance still in a claimable state is not claimed, and claimNext moves on to the next', () => {
      const engine = queued({ ...base, from: ['running'] });
      assert.equal(attempt(engine, { failure: 'rejected' }).exhausted, true);
      assert.equal(dataOf(engine).status, 'queued');
      assert.equal(veto(() => invoke(engine, worker, 'claim')).reason, 'its retries are exhausted, so it is not taken again');
      assert.equal(claimNext(engine)?.id, 'j2');
      assert.deepEqual([dataOf(engine).status, (dataOf(engine).lease as { holder: string | null }).holder], ['queued', null]);
      assert.equal(claimNext(engine, other), null);
    });

    test('a claimed job that fails until its caps run out moves to exhaustedState and out of the queue', () => {
      const engine = queued(base);
      for (let n = 1; n <= 3; n += 1) {
        const claimed = claimNext(engine);
        assert.equal(claimed?.id, 'j1');
        assert.equal(attempt(engine, { failure: 'timeout' }).exhausted, false);
        engine.instances.invoke(worker, 'Job', 'j1', 'release', {}, fenced(claimed?.token as number));
        assert.equal(dataOf(engine).status, 'queued');
      }
      const last = claimNext(engine);
      assert.deepEqual(attempt(engine, { failure: 'invalid' }), { ...attempt0(4), failure: 'invalid', classAttempts: counts(3, 1, 0), exhausted: true });
      assert.equal(dataOf(engine).status, 'failed');
      engine.instances.invoke(worker, 'Job', 'j1', 'release', {}, fenced(last?.token as number));
      assert.equal(dataOf(engine).status, 'failed');
      assert.equal(claimNext(engine)?.id, 'j2');
    });
  });

  describe(`Retries: its config (${driver})`, () => {
    function refusal(engine: Engine, config: Record<string, unknown>): string {
      return thrown(() => engine.schemas.define(alice, retriesDocument([{ name: 'Workflow', config: jobFlow }, { name: 'Retries', config }])), SchemaDocumentError).message;
    }

    test("parseConfig holds the config to the type's fields and its Workflow's states", () => {
      const engine = openTestEngine({ driver });
      assert.match(refusal(engine, { ...base, exhaustedState: 'dead' }), /behavior Retries config: exhaustedState "dead" is not a state of the type's Workflow \(queued, running, done, failed\)/);
      assert.match(refusal(engine, { ...base, from: ['paused'] }), /from names "paused", which is not a state of the type's Workflow/);
      assert.match(refusal(engine, { ...base, from: ['failed'] }), /from names exhaustedState "failed", which exhaustion cannot move the status from/);
      assert.match(refusal(engine, { ...base, from: ['done'] }), /from: no transition of the type's Workflow leads from "done" to "failed"/);
      assert.match(refusal(engine, { ...base, resultField: 'result' }), /resultField "result" is not a field of Job \(its fields: title, output, report, caps\)/);
      assert.match(refusal(engine, { ...base, limitsField: 'title' }), /limitsField "title" is not an object field of Job/);
      // The core meta-schema holds the shape first, Workflow among the requirements.
      assert.ok(
        thrown(() => engine.schemas.define(alice, retriesDocument([{ name: 'Workflow', config: jobFlow }, { name: 'Retries', config: { classes, exhaustedState: 'failed' } }])), SchemaDocumentError).issues.some(
          (issue) => issue.path === '/types/Job/behaviors/1/config'
        )
      );
      assert.match(
        thrown(() => engine.schemas.define(alice, retriesDocument([{ name: 'Retries', config: base }])), SchemaDocumentError).message,
        /requires.*Workflow/
      );
      publish(engine, retriesDocument([{ name: 'Workflow', config: jobFlow }, { name: 'Retries', config: { ...base, limitsField: 'caps', resultField: 'report', from: ['running'] } }]));
    });

    test('it can be added to a schema that has instances, which start with no attempts, and cannot be removed from one', () => {
      const engine = openTestEngine({ driver });
      publish(engine, retriesDocument([{ name: 'Workflow', config: jobFlow }]));
      engine.instances.create(alice, 'Job', { title: 'Before' }, { id: 'j1' });
      publish(engine, retriesDocument([{ name: 'Workflow', config: jobFlow }, { name: 'Retries', config: base }]));
      assert.equal(attempt(engine, { failure: 'timeout' }).total, 1);
      publish(engine, retriesDocument([{ name: 'Workflow', config: jobFlow }, { name: 'Retries', config: { ...base, totalAttempts: 8 } }]));
      const refused = thrown(() => engine.schemas.define(alice, retriesDocument([{ name: 'Workflow', config: jobFlow }])), IncompatibleChangeError);
      assert.match(refused.message, /behavior Retries cannot be removed from type Job, which has instances: the attempts its instances recorded would stay behind/);
    });
  });

  // attempt0 is a counted timeout failure's result with total n, not kept and not exhausted.
  function attempt0(n: number): Attempt {
    return { failure: 'timeout', score: null, kept: false, total: n, classAttempts: counts(n, 0, 0), exhausted: false, stuck: false };
  }
}
