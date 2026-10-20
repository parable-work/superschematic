// Numbering, a number a person reads on each instance (D56): its format's
// tokens and padding, the configs it refuses at define, a count with no
// gap a refused create leaves, counts per scope target and per year, a
// list's filters on the number and the label through their indexes,
// assignNumber's backfill, the rule that nothing changes a number, the
// rule for a new version, its guidance, and Revisions beside it.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import {
  BehaviorVetoError,
  EngineError,
  FILTER_SCAN_ROWS,
  IncompatibleChangeError,
  InstanceValidationError,
  SchemaDocumentError,
  type Engine,
  type ListOptions,
} from '../dist/index.js';
import { defineBehavior } from '../dist/testing/index.js';
import { openMetaSchema } from './behavior-fixtures.ts';
import { alice, cleanup, clone, drivers, openTestEngine, schemaDocument, thrown, type TestEngineOptions } from './helpers.ts';
import { testClock, type TestClock } from './runner-fixtures.ts';

afterEach(cleanup);

const MARCH_2026 = Date.UTC(2026, 2, 1);

// customers is the schema an invoice's customer link points at.
const customers = schemaDocument('Customer', [{ name: 'name', typeRef: { name: 'string' }, required: true }]);

// invoice is an Invoice schema whose type composes Links, with a required
// customer and an optional referrer, then the behaviors given.
function invoice(behaviors: unknown[], links = true): Record<string, unknown> {
  const document = schemaDocument('Invoice', [
    { name: 'title', typeRef: { name: 'string' }, required: true },
    { name: 'note', typeRef: { name: 'string' } },
  ]) as { types: { Invoice: Record<string, unknown> } };
  document.types.Invoice.behaviors = [
    ...(links ? [{ name: 'Links', config: { links: { customer: { schema: 'Customer', required: true }, referrer: { schema: 'Customer' } } } }] : []),
    ...behaviors,
  ];
  return document;
}

function numbered(config: unknown, more: unknown[] = []): Record<string, unknown> {
  return invoice([{ name: 'Numbering', config }, ...more]);
}

// test.Refuser refuses a create whose title is "refuse", after every
// behavior listed before it has run its afterChange.
const refuser = defineBehavior({
  declaration: { name: 'test.Refuser', vetoes: [{ code: 'refused' }] },
  afterChange(context, change) {
    if (change.kind === 'create' && context.data.title === 'refuse') {
      throw new BehaviorVetoError('test.Refuser', 'create', context.schema, context.id, { reason: 'refused', code: 'refused' });
    }
  },
});

for (const driver of drivers) {
  function open(clock: TestClock = testClock(MARCH_2026), options: Partial<TestEngineOptions> = {}): Engine {
    const engine = openTestEngine({ driver, clock, ...options });
    engine.schemas.define(alice, customers);
    engine.schemas.publish(alice, 'Customer');
    for (const id of ['c1', 'c2']) {
      engine.instances.create(alice, 'Customer', { name: id }, { id });
    }
    return engine;
  }

  function publish(engine: Engine, document: Record<string, unknown>): number {
    engine.schemas.define(alice, document);
    return engine.schemas.publish(alice, document.name as string).version;
  }

  function create(engine: Engine, id: string, customer = 'c1', title = id): Record<string, unknown> | undefined {
    return engine.instances.create(alice, 'Invoice', { title }, { id, behaviors: { Links: { customer } } }).behaviors.Numbering;
  }

  function numberOf(engine: Engine, id: string): unknown {
    return engine.instances.get(alice, 'Invoice', id)?.behaviors.Numbering;
  }

  function every(engine: Engine, options: ListOptions): { ids: string[]; pages: number[] } {
    const ids: string[] = [];
    const pages: number[] = [];
    let cursor: string | undefined;
    do {
      const page = engine.instances.list(alice, 'Invoice', { ...options, ...(cursor === undefined ? {} : { cursor }) });
      ids.push(...page.items.map((item) => item.id));
      pages.push(page.items.length);
      cursor = page.next ?? undefined;
    } while (cursor !== undefined);
    return { ids, pages };
  }

  describe(`Numbering (${driver})`, () => {
    test('a create numbers the instance, and its event carries the number; the format pads {seq:N}, prints a longer number in full, and the year it was created in', () => {
      const clock = testClock(MARCH_2026);
      const engine = open(clock);
      publish(engine, numbered({ format: 'INV-{yyyy}-{seq:4}' }));
      assert.deepEqual(create(engine, 'i1'), { seq: 1, label: 'INV-2026-0001' });
      assert.deepEqual(create(engine, 'i2'), { seq: 2, label: 'INV-2026-0002' });
      assert.deepEqual(numberOf(engine, 'i2'), { seq: 2, label: 'INV-2026-0002' });
      const events = engine.events.read(alice, { schema: 'Invoice', instanceId: 'i1' }).events;
      assert.deepEqual(events.map((event) => event.kind), ['create']);
      assert.deepEqual((events[0].change as { behaviors: Record<string, unknown> }).behaviors.Numbering, { seq: 1, label: 'INV-2026-0001' });

      // {yy} is the year's last two digits, {seq:1} pads to one digit and
      // prints 10 in full, and {seq} pads nothing.
      clock.now = Date.UTC(2005, 6, 4);
      const padded = open(clock);
      publish(padded, numbered({ format: '{yy}/{seq:1}:{yyyy}' }));
      const labels: string[] = [];
      padded.storage.transaction(() => {
        for (let index = 1; index <= 11; index += 1) {
          labels.push(String(create(padded, `i${index}`)?.label));
        }
      });
      assert.deepEqual([labels[0], labels[8], labels[9], labels[10]], ['05/1:2005', '05/9:2005', '05/10:2005', '05/11:2005']);
      const plain = open(clock);
      publish(plain, numbered({ format: 'No. {seq}' }));
      assert.deepEqual(create(plain, 'i1'), { seq: 1, label: 'No. 1' });
    });

    test('a define refuses a brace outside its tokens, a {seq} not there once, a yearly reset without the year, and a scope that is not a required single link of Links', () => {
      const engine = open();
      const refusals: Array<[unknown, string, boolean?]> = [
        [{ format: 'INV-{n}' }, 'format "INV-{n}" has {n}, which is not one of its tokens: {seq}, {seq:N} (N from 1 to 10), {yyyy} and {yy}'],
        [{ format: '{seq:0}' }, 'format "{seq:0}" has {seq:0}, which is not one of its tokens: {seq}, {seq:N} (N from 1 to 10), {yyyy} and {yy}'],
        [{ format: '{seq:04}' }, 'format "{seq:04}" has {seq:04}, which is not one of its tokens: {seq}, {seq:N} (N from 1 to 10), {yyyy} and {yy}'],
        [{ format: '{seq:11}' }, 'format "{seq:11}" has {seq:11}, which is not one of its tokens: {seq}, {seq:N} (N from 1 to 10), {yyyy} and {yy}'],
        [{ format: '{YYYY}-{seq}' }, 'format "{YYYY}-{seq}" has {YYYY}, which is not one of its tokens: {seq}, {seq:N} (N from 1 to 10), {yyyy} and {yy}'],
        [{ format: 'INV-{seq' }, 'format "INV-{seq" has a "{" that is not part of a token; its tokens are {seq}, {seq:N} (N from 1 to 10), {yyyy} and {yy}'],
        [{ format: '{{seq}}' }, 'format "{{seq}}" has a "{" that is not part of a token; its tokens are {seq}, {seq:N} (N from 1 to 10), {yyyy} and {yy}'],
        [{ format: 'INV}{seq}' }, 'format "INV}{seq}" has a "}" that is not part of a token; its tokens are {seq}, {seq:N} (N from 1 to 10), {yyyy} and {yy}'],
        [{ format: 'INV-{yyyy}' }, 'format "INV-{yyyy}" has no {seq}: it prints the number with {seq}, or {seq:N} padded to N digits, once'],
        [{ format: '{seq}-{seq:3}' }, 'format "{seq}-{seq:3}" has the number 2 times: it prints it with {seq} or {seq:N} once, so two numbers never print one label'],
        [
          { format: 'INV-{seq}', reset: 'yearly' },
          'reset yearly counts each year from 1, so format "INV-{seq}" prints the year, {yyyy} or {yy}, or two years would print one label',
        ],
        [{ format: '{seq}', scope: { link: 'ghost' } }, "scope.link ghost is not a link of the type's Links config (its links: customer, referrer)"],
        [{ format: '{seq}', scope: { link: 'referrer' } }, 'scope.link referrer is not required, so an instance may hold no target to count by; make it required'],
      ];
      for (const [config, message] of refusals) {
        const error = thrown(() => engine.schemas.define(alice, numbered(config)), SchemaDocumentError);
        assert.deepEqual(error.issues, [{ path: '/types/Invoice/behaviors/1/config', message: `type Invoice: behavior Numbering config: ${message}` }], message);
      }
      const unlinked = thrown(() => engine.schemas.define(alice, invoice([{ name: 'Numbering', config: { format: '{seq}', scope: { link: 'customer' } } }], false)), SchemaDocumentError);
      assert.deepEqual(unlinked.issues, [
        { path: '/types/Invoice/behaviors/0/config', message: 'type Invoice: behavior Numbering config: scope.link customer is a link of Links, which the type does not list' },
      ]);
      // What the declaration's config schema refuses, the loader refuses first.
      for (const config of [{}, { format: '' }, { format: '{seq}', reset: 'monthly' }, { format: '{seq}', scope: {} }, { format: '{seq}', start: 100 }]) {
        const error = thrown(() => engine.schemas.define(alice, numbered(config)), SchemaDocumentError);
        assert.match(error.issues[0].path, /^\/types\/Invoice\/behaviors\/1/);
      }
      assert.deepEqual(engine.schemas.list(alice).map((schema) => schema.name), ['Customer']);
    });

    test("a scope link that holds many targets is refused, read from the Links config as the type gives it", () => {
      // An open meta-schema lets a Links config through that names many,
      // so Numbering's own check is what refuses it.
      const engine = openTestEngine({ driver, metaSchema: openMetaSchema() });
      const document = invoice([{ name: 'Numbering', config: { format: '{seq}', scope: { link: 'customer' } } }]) as {
        types: { Invoice: { behaviors: Array<{ config: { links?: Record<string, Record<string, unknown>> } }> } };
      };
      (document.types.Invoice.behaviors[0].config.links as Record<string, Record<string, unknown>>).customer.many = true;
      const error = thrown(() => engine.schemas.define(alice, document), SchemaDocumentError);
      assert.ok(
        error.issues.some(
          (issue) =>
            issue.path === '/types/Invoice/behaviors/1/config' &&
            issue.message ===
              'type Invoice: behavior Numbering config: scope.link customer holds many targets (many: true), so an instance has no one target to count by; name a link that holds one'
        ),
        JSON.stringify(error.issues)
      );
    });

    test('the count has no gap a refused create leaves: a create refused after the number was taken rolls it back; a delete leaves a gap', () => {
      const engine = open(testClock(MARCH_2026), { metaSchema: openMetaSchema(), behaviors: [refuser] });
      publish(engine, numbered({ format: 'INV-{seq:3}' }, [{ name: 'test.Refuser' }]));
      assert.deepEqual(create(engine, 'i1'), { seq: 1, label: 'INV-001' });
      const refused = thrown(() => create(engine, 'gone', 'c1', 'refuse'), BehaviorVetoError);
      assert.deepEqual([refused.behavior, refused.vetoCode], ['test.Refuser', 'refused']);
      // An invalid create and a create Links refuses take none either.
      thrown(() => engine.instances.create(alice, 'Invoice', { note: 'no title' }, { behaviors: { Links: { customer: 'c1' } } }), InstanceValidationError);
      assert.equal(thrown(() => create(engine, 'lost', 'ghost'), EngineError).code, 'invalid_argument');
      assert.deepEqual(create(engine, 'i2'), { seq: 2, label: 'INV-002' });
      engine.instances.delete(alice, 'Invoice', 'i2');
      assert.deepEqual(create(engine, 'i3'), { seq: 3, label: 'INV-003' });
      // The deleted instance's label went with it; the count did not.
      assert.deepEqual(every(engine, { where: { 'Numbering.label': 'INV-002' } }).ids, []);
    });

    test('a scope counts per target of its link, so a label is unique only within a target; moving the link keeps the number', () => {
      const engine = open();
      publish(engine, numbered({ format: 'C-{seq}', scope: { link: 'customer' } }));
      assert.deepEqual(create(engine, 'a1', 'c1'), { seq: 1, label: 'C-1' });
      assert.deepEqual(create(engine, 'a2', 'c1'), { seq: 2, label: 'C-2' });
      assert.deepEqual(create(engine, 'b1', 'c2'), { seq: 1, label: 'C-1' });
      assert.deepEqual(every(engine, { where: { 'Numbering.label': 'C-1' } }).ids, ['a1', 'b1']);
      engine.instances.invoke(alice, 'Invoice', 'a2', 'link', { name: 'customer', id: 'c2' });
      assert.deepEqual(numberOf(engine, 'a2'), { seq: 2, label: 'C-2' });
      // c2's count is its own: a2 was numbered in c1's.
      assert.deepEqual(create(engine, 'b2', 'c2'), { seq: 2, label: 'C-2' });
      assert.deepEqual(create(engine, 'a3', 'c1'), { seq: 3, label: 'C-3' });
    });

    test('a yearly count starts again at 1 each UTC year, per scope target too', () => {
      const clock = testClock(Date.UTC(2026, 11, 31, 23, 59, 59, 999));
      const engine = open(clock);
      publish(engine, numbered({ format: 'INV-{yyyy}-{seq:4}', scope: { link: 'customer' }, reset: 'yearly' }));
      assert.equal(create(engine, 'i1', 'c1')?.label, 'INV-2026-0001');
      assert.equal(create(engine, 'i2', 'c1')?.label, 'INV-2026-0002');
      assert.equal(create(engine, 'i3', 'c2')?.label, 'INV-2026-0001');
      clock.now = Date.UTC(2027, 0, 1);
      assert.deepEqual(create(engine, 'i4', 'c1'), { seq: 1, label: 'INV-2027-0001' });
      assert.deepEqual(create(engine, 'i5', 'c1'), { seq: 2, label: 'INV-2027-0002' });
      assert.deepEqual(create(engine, 'i6', 'c2'), { seq: 1, label: 'INV-2027-0001' });

      // Without a reset the count runs on across the year; the label shows the year.
      const other = open(clock);
      clock.now = Date.UTC(2026, 11, 31);
      publish(other, numbered({ format: '{yy}-{seq}' }));
      assert.equal(create(other, 'j1')?.label, '26-1');
      clock.now = Date.UTC(2027, 0, 1);
      assert.equal(create(other, 'j2')?.label, '27-2');
    });

    test('a list finds an instance by Numbering.label and Numbering.seq, each through its index', () => {
      const engine = open();
      publish(engine, numbered({ format: 'INV-{yyyy}-{seq:4}' }));
      const total = FILTER_SCAN_ROWS + 10;
      engine.storage.transaction(() => {
        for (let index = 1; index <= total; index += 1) {
          create(engine, `i${String(index).padStart(5, '0')}`);
        }
      });
      // Without an index, the page would scan FILTER_SCAN_ROWS instances
      // and find none; through it, one page holds each match.
      assert.deepEqual(every(engine, { where: { 'Numbering.label': `INV-2026-${FILTER_SCAN_ROWS + 5}` } }), { ids: ['i01005'], pages: [1] });
      assert.deepEqual(every(engine, { where: { 'Numbering.seq': [FILTER_SCAN_ROWS + 7, 3] } }), { ids: ['i00003', 'i01007'], pages: [2] });
      assert.deepEqual(every(engine, { where: { 'Numbering.label': 'INV-2026-9999' } }), { ids: [], pages: [0] });
      assert.match(thrown(() => engine.instances.list(alice, 'Invoice', { where: { 'Numbering.seq': '3' } }), EngineError).message, /where\.Numbering\.seq is an integer/);
    });

    test('assignNumber numbers an instance made before the schema composed Numbering, in the year it was created, and changes nothing on one numbered already', () => {
      const clock = testClock(Date.UTC(2025, 5, 1));
      const engine = open(clock);
      publish(engine, invoice([]));
      create(engine, 'o1');
      create(engine, 'o2');
      clock.now = MARCH_2026;
      create(engine, 'o3');
      // Numbering joins a schema with instances, which hold no number yet.
      assert.equal(publish(engine, numbered({ format: 'INV-{yyyy}-{seq:3}', reset: 'yearly' })), 2);
      assert.deepEqual(engine.instances.get(alice, 'Invoice', 'o1')?.behaviors.Numbering, {});
      assert.deepEqual(every(engine, { where: { 'Numbering.seq': null } }).ids, ['o1', 'o2', 'o3']);
      assert.deepEqual(create(engine, 'o4'), { seq: 1, label: 'INV-2026-001' });

      // The backfill, in creation order.
      assert.deepEqual(engine.instances.invoke(alice, 'Invoice', 'o1', 'assignNumber'), { seq: 1, label: 'INV-2025-001' });
      assert.deepEqual(engine.instances.invoke(alice, 'Invoice', 'o2', 'assignNumber'), { seq: 2, label: 'INV-2025-002' });
      assert.deepEqual(engine.instances.invoke(alice, 'Invoice', 'o3', 'assignNumber'), { seq: 2, label: 'INV-2026-002' });
      const events = engine.events.read(alice, { schema: 'Invoice', instanceId: 'o1' }).events;
      assert.deepEqual(events.at(-1)?.change, {
        behavior: 'Numbering',
        operation: 'assignNumber',
        params: {},
        patch: { behaviors: { Numbering: { seq: 1, label: 'INV-2025-001' } } },
      });
      assert.deepEqual(every(engine, { where: { 'Numbering.seq': null } }).ids, []);

      // On one numbered already it changes nothing: no event, the same seq.
      const before = engine.instances.get(alice, 'Invoice', 'o4');
      assert.deepEqual(engine.instances.invoke(alice, 'Invoice', 'o4', 'assignNumber'), { seq: 1, label: 'INV-2026-001' });
      const after = engine.instances.get(alice, 'Invoice', 'o4');
      assert.deepEqual([after?.seq, after?.updatedAt], [before?.seq, before?.updatedAt]);
      assert.equal(engine.events.read(alice, { schema: 'Invoice', instanceId: 'o4' }).events.length, 1);
    });

    test('nothing changes a number once set: an update, a link moved, assignNumber again', () => {
      const engine = open();
      publish(engine, numbered({ format: 'INV-{seq}' }));
      create(engine, 'i1');
      const refused = thrown(() => engine.instances.update(alice, 'Invoice', 'i1', { Numbering: { seq: 9 } }), InstanceValidationError);
      assert.match(refused.message, /Numbering/);
      engine.instances.update(alice, 'Invoice', 'i1', { note: 'paid' });
      engine.instances.invoke(alice, 'Invoice', 'i1', 'link', { name: 'referrer', id: 'c2' });
      engine.instances.invoke(alice, 'Invoice', 'i1', 'assignNumber');
      assert.deepEqual(numberOf(engine, 'i1'), { seq: 1, label: 'INV-1' });
      const patches = engine.events
        .read(alice, { schema: 'Invoice', instanceId: 'i1' })
        .events.slice(1)
        .map((event) => (event.kind === 'operation' ? (event.change as { patch: { behaviors?: Record<string, unknown> } }).patch.behaviors : undefined));
      assert.ok(patches.every((behaviors) => behaviors?.Numbering === undefined), JSON.stringify(patches));
    });

    test('while the schema has instances, the format, the scope and the reset stay, and so does Numbering; with none, anything may change and the count starts again', () => {
      const engine = open();
      publish(engine, numbered({ format: 'INV-{yyyy}-{seq}', scope: { link: 'customer' } }));
      create(engine, 'i1');
      const changes: Array<[unknown, RegExp]> = [
        [{ format: 'INV-{seq}', scope: { link: 'customer' } }, /the new config changes the format from "INV-\{yyyy\}-\{seq\}" to "INV-\{seq\}"/],
        [{ format: 'INV-{yyyy}-{seq}' }, /the new config changes the scope from link customer to none/],
        [{ format: 'INV-{yyyy}-{seq}', scope: { link: 'customer' }, reset: 'yearly' }, /the new config changes the reset from none to yearly/],
      ];
      for (const [config, message] of changes) {
        const error = thrown(() => publish(engine, numbered(config)), IncompatibleChangeError);
        assert.deepEqual(error.changes.map((change) => change.path), ['Invoice.behaviors.Numbering']);
        assert.match(error.changes[0].message, message);
      }
      const removed = thrown(() => publish(engine, invoice([])), IncompatibleChangeError);
      assert.match(removed.changes[0].message, /the instances would lose their numbers, and Numbering added again would count from 1/);

      // A new version that keeps the config keeps the counts.
      const next = clone(numbered({ format: 'INV-{yyyy}-{seq}', scope: { link: 'customer' } })) as { types: { Invoice: Record<string, unknown> } };
      next.types.Invoice.description = 'An invoice to a customer.';
      assert.equal(publish(engine, next), 2);
      assert.equal(create(engine, 'i2')?.label, 'INV-2026-2');

      // With none, the format may change, and the count starts at 1.
      for (const id of ['i1', 'i2']) {
        engine.instances.delete(alice, 'Invoice', id);
      }
      assert.equal(publish(engine, numbered({ format: 'N-{seq:2}' })), 3);
      assert.deepEqual(create(engine, 'i3'), { seq: 1, label: 'N-01' });
      engine.instances.delete(alice, 'Invoice', 'i3');
      // Numbering may go, and comes back counting from 1.
      assert.equal(publish(engine, invoice([])), 4);
      create(engine, 'i4');
      assert.equal(publish(engine, numbered({ format: 'N-{seq:2}' })), 5);
      assert.deepEqual(numberOf(engine, 'i4'), {});
      assert.deepEqual(create(engine, 'i5'), { seq: 1, label: 'N-01' });
      assert.deepEqual(engine.instances.invoke(alice, 'Invoice', 'i4', 'assignNumber'), { seq: 2, label: 'N-02' });
    });

    test('its guidance prints an example of the format, says what it counts by, and how a list finds an instance by its number', () => {
      const engine = open();
      publish(engine, numbered({ format: 'INV-{yyyy}-{seq:4}', scope: { link: 'customer' }, reset: 'yearly' }));
      const described = engine.tools.describe(alice, 'Invoice');
      assert.equal(
        described.behaviors.find((behavior) => behavior.name === 'Numbering')?.summary,
        'Each Invoice gets a number at its create, in a count per target of its customer link, starting again at 1 each UTC year, printed as INV-{yyyy}-{seq:4}: number 42, made in 2026, prints INV-2026-0042. A label is unique among one customer target\'s instances. A refused create takes no number, a delete leaves a gap, and nothing changes a number once set.'
      );
      const guidance = (operation: string) => described.operations.find((candidate) => candidate.name === operation)?.guidance;
      assert.match(guidance('list')?.useWhen ?? '', /where: \{ "Numbering\.label": "INV-2026-0042" \} finds an instance by the number a person reads, and "Numbering\.seq" by its number\. Each customer target counts from 1/);
      assert.match(guidance('create')?.success ?? '', /holds behaviors\.Numbering\.seq, the next number of its count, and label, the number as INV-\{yyyy\}-\{seq:4\} prints it\. It is counted in the count of the customer target the create gives\./);
      assert.match(guidance('assignNumber')?.useWhen ?? '', /Use on an instance created before Invoice composed Numbering/);
      assert.match(guidance('assignNumber')?.success ?? '', /counted as its create would have counted them, by its customer target, in the year it was created/);
      assert.match(guidance('update')?.doNotUseWhen ?? '', /Do not use to change a number/);
      assert.match(guidance('link')?.success ?? '', /Moving customer keeps the instance's number/);

      publish(engine, numbered({ format: 'No. {seq}' }));
      assert.equal(
        engine.tools.describe(alice, 'Invoice').behaviors.find((behavior) => behavior.name === 'Numbering')?.summary,
        'Each Invoice gets a number at its create, in one count for Invoice, printed as No. {seq}: number 42 prints No. 42. A label is unique. A refused create takes no number, a delete leaves a gap, and nothing changes a number once set.'
      );
    });

    test('beside Revisions: an approved proposal keeps the number, and a revision holds the own fields alone', () => {
      const engine = open();
      publish(engine, numbered({ format: 'INV-{seq}' }, [{ name: 'Revisions', config: { review: { permission: 'invoices.review' } } }]));
      create(engine, 'i1');
      engine.instances.invoke(alice, 'Invoice', 'i1', 'propose', { patch: { note: 'net 30' } });
      engine.instances.invoke({ subject: 'rae', permissions: ['invoices.review'] }, 'Invoice', 'i1', 'approve', { proposal: 1 });
      const read = engine.instances.get(alice, 'Invoice', 'i1');
      assert.deepEqual([read?.data.note, read?.behaviors.Numbering, read?.behaviors.Revisions.revision], ['net 30', { seq: 1, label: 'INV-1' }, 2]);
      const revisions = engine.instances.invoke(alice, 'Invoice', 'i1', 'listRevisions') as { items: Array<{ data: unknown }> };
      assert.deepEqual(revisions.items.map((item) => item.data), [{ title: 'i1' }, { title: 'i1', note: 'net 30' }]);
      assert.deepEqual(create(engine, 'i2'), { seq: 2, label: 'INV-2' });
    });
  });
}
