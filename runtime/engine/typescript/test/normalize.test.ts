// A write stores what the schema's parse makes of it (D16, amended): each
// scalar value in its scalar's canonical form, at every depth, on a
// create, an update, a behavior's update() and instances.create; a
// create's absent fields take their defaults, an update's do not. What a
// read returns, what the log records, what a unique index compares and
// what a value's hash is are the stored form, and a lookup's key and a
// list's where are normalized to compare with it.
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { afterEach, describe, test } from 'node:test';

import { InstanceValidationError, UniqueConflictError, canonicalJSON, defineBehavior, type Engine, type FrozenJSON } from '../dist/index.js';
import { openMetaSchema } from './behavior-fixtures.ts';
import { alice, cleanup, drivers, openTestEngine, schemaDocument, thrown } from './helpers.ts';

afterEach(cleanup);

const emailParams = { type: 'object', additionalProperties: false, required: ['email'], properties: { email: { type: 'string' } } } as const;

// test.Mailer changes the email through update(), and creates a contact
// as a behavior does.
const mailer = defineBehavior({
  declaration: {
    name: 'test.Mailer',
    operations: [
      { name: 'setEmail', paramsSchema: emailParams, resultSchema: true, writes: true },
      { name: 'invite', scope: 'schema', paramsSchema: emailParams, resultSchema: true, writes: true },
    ],
  },
  operations: {
    setEmail(context, params) {
      return context.update({ email: params.email } as FrozenJSON);
    },
  },
  schemaOperations: {
    invite(context, params) {
      return context.instances.create('Contact', { name: 'Invited', email: params.email } as FrozenJSON, { id: 'invited' }).data as FrozenJSON;
    },
  },
});

function contactDocument(): Record<string, unknown> {
  const document = schemaDocument(
    'Contact',
    [
      { name: 'name', typeRef: { name: 'string' }, required: true },
      { name: 'email', typeRef: { name: 'Contact.Email' }, unique: true },
      { name: 'color', typeRef: { name: 'Design.Color' } },
      { name: 'labels', typeRef: { name: 'Generic.StringMap' } },
      { name: 'seenAt', typeRef: { name: 'Temporal.DateTime' } },
      { name: 'score', typeRef: { name: 'number' }, default: '2.5' },
      { name: 'active', typeRef: { name: 'boolean' }, default: 'true' },
      { name: 'tier', typeRef: { name: 'Tier' }, default: 'free' },
      { name: 'address', typeRef: { name: 'Address' } },
    ],
    {
      enums: { Tier: { name: 'Tier', values: [{ name: 'FREE', serializedAs: 'free' }, { name: 'PAID', serializedAs: 'paid' }] } },
      types: {
        Address: {
          name: 'Address',
          role: 'EmbeddedStruct',
          fields: [
            { name: 'city', typeRef: { name: 'string' }, required: true },
            { name: 'country', typeRef: { name: 'string' }, default: 'NZ' },
            { name: 'contact', typeRef: { name: 'Contact.Email' } },
          ],
        },
      },
    }
  ) as { types: { Contact: Record<string, unknown> } };
  document.types.Contact.behaviors = [{ name: 'test.Mailer' }];
  return document as unknown as Record<string, unknown>;
}

for (const driver of drivers) {
  describe(`a write stores what the schema's parse makes of it (${driver})`, () => {
    function open(options: Record<string, unknown> = {}): Engine {
      const engine = openTestEngine({ driver, metaSchema: openMetaSchema(), behaviors: [mailer], ...options });
      engine.schemas.define(alice, contactDocument());
      engine.schemas.publish(alice, 'Contact');
      return engine;
    }

    test("a create stores each scalar's canonical form and fills the defaults, and a read and the create event return them", () => {
      const engine = open();
      const created = engine.instances.create(
        alice,
        'Contact',
        { name: 'Ada', email: 'Ada@Example.COM', color: 'red', labels: '{"b":"2","a":"1"}', address: { city: 'Wellington', contact: 'Office@Example.COM' } },
        { id: 'ada' }
      );
      const stored = {
        name: 'Ada',
        email: 'ada@example.com',
        color: '#FF0000FF',
        labels: { a: '1', b: '2' },
        address: { city: 'Wellington', contact: 'office@example.com', country: 'NZ' },
        score: 2.5,
        active: true,
        tier: 'free',
      };
      assert.deepEqual(created.data, stored);
      assert.deepEqual(engine.instances.get(alice, 'Contact', 'ada')?.data, stored);
      const event = engine.events.read(alice, { schema: 'Contact', instanceId: 'ada' }).events[0];
      // test.Mailer declares no field, so the create's behaviors hold no entry.
      assert.deepEqual(event.change, { data: stored, behaviors: {} });
    });

    test('an update normalizes its patch and fills no default; one whose normalized patch changes nothing writes nothing', () => {
      const engine = open();
      engine.instances.create(alice, 'Contact', { name: 'Ada', email: 'ada@example.com' }, { id: 'ada' });
      const updated = engine.instances.update(alice, 'Contact', 'ada', { color: '#abc', score: null, address: { city: 'Auckland' } });
      // The removed score stays removed, and the new address takes no default.
      assert.equal(updated.data.color, '#AABBCCFF');
      assert.equal(updated.data.score, undefined);
      assert.deepEqual(updated.data.address, { city: 'Auckland' });
      const events = engine.events.read(alice, { schema: 'Contact', instanceId: 'ada' }).events;
      assert.deepEqual(events.at(-1)?.change, { data: { color: '#AABBCCFF', score: null, address: { city: 'Auckland' } } });
      // The same email in capitals is no change.
      assert.equal(engine.instances.update(alice, 'Contact', 'ada', { email: 'ADA@EXAMPLE.COM' }).seq, updated.seq);
      assert.equal(engine.events.read(alice, { schema: 'Contact', instanceId: 'ada' }).events.length, events.length);
    });

    test("a behavior's update() and instances.create store the canonical form too", () => {
      const engine = open();
      engine.instances.create(alice, 'Contact', { name: 'Ada' }, { id: 'ada' });
      assert.equal((engine.instances.invoke(alice, 'Contact', 'ada', 'setEmail', { email: 'Ada@Example.COM' }) as { email: string }).email, 'ada@example.com');
      assert.equal(engine.instances.get(alice, 'Contact', 'ada')?.data.email, 'ada@example.com');
      const invited = engine.instances.invokeSchema(alice, 'Contact', 'invite', { email: 'Grace@Example.COM' }) as Record<string, unknown>;
      assert.equal(invited.email, 'grace@example.com');
      assert.equal(invited.score, 2.5);
    });

    test("a unique field compares the canonical form, and a lookup's key and a list's where are normalized to match it", () => {
      const engine = open();
      engine.instances.create(alice, 'Contact', { name: 'Ada', email: 'Ada@Example.COM' }, { id: 'ada' });
      thrown(() => engine.instances.create(alice, 'Contact', { name: 'Ada again', email: 'ada@EXAMPLE.com' }), UniqueConflictError);
      assert.equal(engine.instances.lookup(alice, 'Contact', { email: 'ADA@example.com' })?.id, 'ada');
      assert.deepEqual(
        engine.instances.list(alice, 'Contact', { where: { email: ['nobody@example.com', 'ADA@EXAMPLE.COM'] } }).items.map((item) => item.id),
        ['ada']
      );
    });

    test("a value a scalar's parser refuses is refused, and a value of the wrong type is the version's type issue", () => {
      const engine = open();
      const refused = thrown(() => engine.instances.create(alice, 'Contact', { name: 'Ada', seenAt: '2026-02-30T00:00:00Z' }), InstanceValidationError);
      assert.deepEqual(
        refused.issues.map((issue) => issue.path),
        ['seenAt']
      );
      const typed = thrown(() => engine.instances.create(alice, 'Contact', { name: 'Ada', score: '3' }), InstanceValidationError);
      assert.deepEqual(typed.issues.map((issue) => [issue.path, issue.rule]), [['score', 'type']]);
    });

    test("schemas.validate answers as a create would: the value as stored, its defaults filled", () => {
      const engine = open();
      assert.deepEqual(engine.schemas.validate(alice, 'Contact', { name: 'Ada', color: 'red' }), []);
      assert.deepEqual([...new Set(engine.schemas.validate(alice, 'Contact', { name: 'Ada', color: 'not a color at all' }).map((issue) => issue.path))], ['color']);
    });

    test('a large value is stored by the hash of its canonical form', () => {
      const engine = open({ values: { thresholdBytes: 1024 } });
      const labels: Record<string, string> = {};
      for (let at = 99; at >= 0; at -= 1) {
        labels[`label-${String(at).padStart(3, '0')}`] = 'x'.repeat(20);
      }
      engine.instances.create(alice, 'Contact', { name: 'Ada', labels: JSON.stringify(labels) }, { id: 'ada' });
      const sorted = Object.fromEntries(Object.entries(labels).sort(([a], [b]) => (a < b ? -1 : 1)));
      const hash = createHash('sha256').update(canonicalJSON(sorted), 'utf8').digest('hex');
      const ref = engine.instances.get(alice, 'Contact', 'ada', { valueRefs: true })?.data.labels as { $value: string };
      assert.equal(ref.$value, hash);
      assert.deepEqual(engine.values.get(alice, hash).value, sorted);
    });
  });
}
