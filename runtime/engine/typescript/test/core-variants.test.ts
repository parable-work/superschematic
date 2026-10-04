// Variants, the core's field typed by another field's value (D16,
// amended): while kind holds a value the config lists, result is held to
// that type as strictly as a field of it, unknown keys refused at every
// depth; while kind holds another value or none, result holds none. The
// config is held to the type and the document; a new version keeps each
// value's type and may give a new value one; the types it checks are held
// to the compatibility rule; the describe document and the create and
// update tools show each variant, as JSON Schema a validator agrees with.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { Ajv2020 } from 'ajv/dist/2020.js';

import { IncompatibleChangeError, InstanceValidationError, SchemaDocumentError, type Engine } from '../dist/index.js';
import { alice, cleanup, clone, drivers, openTestEngine, stepsDocument, thrown } from './helpers.ts';

afterEach(cleanup);

interface StepsDocument {
  name: string;
  types: Record<string, { name: string; role: string; behaviors?: Array<{ name: string; config?: Record<string, unknown> }>; fields: Array<Record<string, unknown>> }>;
}

/** steps is the fixture's document, to change. */
function steps(): StepsDocument {
  return clone(stepsDocument()) as unknown as StepsDocument;
}

function publish(engine: Engine, document: StepsDocument): void {
  engine.schemas.define(alice, document as unknown as Record<string, unknown>);
  engine.schemas.publish(alice, document.name);
}

/** variantsOf is the document's Variants config, to change. */
function variantsOf(document: StepsDocument): { field: string; by: string; types: Record<string, string> } {
  return document.types.Step.behaviors?.find((behavior) => behavior.name === 'Variants')?.config as { field: string; by: string; types: Record<string, string> };
}

/** The [path, rule] of each issue a create is refused with, sorted. */
function createIssues(engine: Engine, data: Record<string, unknown>): string[][] {
  return thrown(() => engine.instances.create(alice, 'Step', data), InstanceValidationError)
    .issues.map((issue) => [issue.path, issue.rule])
    .sort();
}

function updateIssues(engine: Engine, id: string, patch: Record<string, unknown>): string[][] {
  return thrown(() => engine.instances.update(alice, 'Step', id, patch), InstanceValidationError)
    .issues.map((issue) => [issue.path, issue.rule])
    .sort();
}

const passed = { passed: true, checks: [{ name: 'lint', ok: true }] };

for (const driver of drivers) {
  describe(`Variants (${driver})`, () => {
    test('a result is held to the type its kind picks, strictly at every depth, and a kind the config does not list holds none', () => {
      const engine = openTestEngine({ driver });
      publish(engine, steps());
      assert.deepEqual(engine.instances.create(alice, 'Step', { title: 'Check', kind: 'verify', result: passed }, { id: 's1' }).data.result, passed);
      assert.deepEqual(createIssues(engine, { title: 'Check', kind: 'verify', result: { approved: true } }), [
        ['result.approved', 'unknown'],
        ['result.passed', 'required'],
      ]);
      assert.deepEqual(createIssues(engine, { title: 'Check', kind: 'verify', result: { passed: true, checks: [{ name: 'lint', ok: 'yes', by: 'ci' }] } }), [
        ['result.checks[0].by', 'unknown'],
        ['result.checks[0].ok', 'type'],
      ]);
      assert.deepEqual(createIssues(engine, { title: 'Look', kind: 'review', result: passed }), [
        ['result.approved', 'required'],
        ['result.checks', 'unknown'],
        ['result.passed', 'unknown'],
      ]);
      assert.deepEqual(
        thrown(() => engine.instances.create(alice, 'Step', { title: 'Check', kind: 'verify', result: 'done' }), InstanceValidationError).issues,
        [{ path: 'result', rule: 'type', message: 'a VerifyResult is a JSON object' }]
      );
      const unlisted = thrown(() => engine.instances.create(alice, 'Step', { title: 'Note', kind: 'note', result: { text: 'hi' } }), InstanceValidationError);
      assert.equal(unlisted.code, 'invalid_instance');
      assert.deepEqual(unlisted.issues, [
        { path: 'result', rule: 'variant', message: 'result holds a value while kind is "note"; result has a type only while kind is "review", "verify", and no value otherwise' },
      ]);
      // No result, or null, is none.
      engine.instances.create(alice, 'Step', { title: 'Note', kind: 'note' }, { id: 's2' });
      engine.instances.create(alice, 'Step', { title: 'Note', kind: 'note', result: null }, { id: 's3' });
      engine.instances.create(alice, 'Step', { title: 'Look', kind: 'review' }, { id: 's4' });
      assert.deepEqual(engine.instances.create(alice, 'Step', { title: 'Look', kind: 'review', result: { approved: false, notes: 'Too long.' } }).data.result, {
        approved: false,
        notes: 'Too long.',
      });
      // schemas.validate checks a value against the version's own rules; a behavior's validate judges a write.
      assert.deepEqual(engine.schemas.validate(alice, 'Step', { title: 'Note', kind: 'note', result: { text: 'hi' } }), []);
    });

    test('an update is held to it as merged, and a kind that changes needs a result of its new type', () => {
      const engine = openTestEngine({ driver });
      publish(engine, steps());
      engine.instances.create(alice, 'Step', { title: 'Check', kind: 'verify', result: passed }, { id: 's1' });
      // A patch merges into the result; a list in it replaces the list.
      assert.deepEqual(updateIssues(engine, 's1', { result: { checks: [{ name: 'test', ok: 1 }] } }), [['result.checks[0].ok', 'type']]);
      assert.deepEqual(engine.instances.update(alice, 'Step', 's1', { result: { passed: false } }).data.result, { passed: false, checks: [{ name: 'lint', ok: true }] });
      assert.deepEqual(updateIssues(engine, 's1', { result: { passed: null } }), [['result.passed', 'required']]);
      assert.equal(engine.instances.update(alice, 'Step', 's1', { result: null }).data.result, undefined);

      // Without Constants, kind may change, and the result goes with it.
      const open = steps();
      open.types.Step.behaviors = open.types.Step.behaviors?.filter((behavior) => behavior.name !== 'Constants');
      const other = openTestEngine({ driver });
      publish(other, open);
      other.instances.create(alice, 'Step', { title: 'Check', kind: 'verify', result: passed }, { id: 's1' });
      assert.deepEqual(updateIssues(other, 's1', { kind: 'review' }), [
        ['result.approved', 'required'],
        ['result.checks', 'unknown'],
        ['result.passed', 'unknown'],
      ]);
      assert.deepEqual(updateIssues(other, 's1', { kind: 'note' }), [['result', 'variant']]);
      // The patch merges into the result, so the new kind's result removes the old one's members.
      assert.deepEqual(updateIssues(other, 's1', { kind: 'review', result: { approved: true } }), [
        ['result.checks', 'unknown'],
        ['result.passed', 'unknown'],
      ]);
      assert.deepEqual(other.instances.update(alice, 'Step', 's1', { kind: 'review', result: { approved: true, passed: null, checks: null } }).data.result, {
        approved: true,
      });
      assert.equal(other.instances.update(alice, 'Step', 's1', { kind: 'note', result: null }).data.kind, 'note');
    });

    test('the config is held to the type and the document: two own fields of the right kinds, and types of the document for values of kind', () => {
      const engine = openTestEngine({ driver });
      const refused = (change: (document: StepsDocument) => void): string => {
        const document = steps();
        change(document);
        return thrown(() => engine.schemas.define(alice, document as unknown as Record<string, unknown>), SchemaDocumentError).issues.map((issue) => issue.message).join('; ');
      };
      const config = (document: StepsDocument) => variantsOf(document);
      assert.equal(
        refused((document) => (config(document).field = 'nope')),
        'type Step: behavior Variants config: field: nope is not a field of Step (its fields: title, kind, result)'
      );
      assert.match(refused((document) => (config(document).by = 'nope')), /by: nope is not a field of Step/);
      assert.match(refused((document) => (config(document).field = 'kind')), /field and by both name kind: a field cannot be typed by its own value/);
      assert.match(
        refused((document) => (config(document).field = 'title')),
        /field: Step\.title is not an open JSON object; Variants types a Generic\.JSON field, or one of a scalar whose values are JSON objects/
      );
      for (const typeRef of [{ name: 'VerifyResult' }, { name: 'Generic.JSON', isArray: true }]) {
        assert.match(
          refused((document) => {
            document.types.Step.fields.push({ name: 'other', typeRef });
            config(document).field = 'other';
          }),
          /field: Step\.other is not an open JSON object/
        );
      }
      assert.match(
        refused((document) => {
          document.types.Step.fields.push({ name: 'meta', typeRef: { name: 'Generic.JSON' } });
          config(document).by = 'meta';
        }),
        /by: Step\.meta is not a string or enum field/
      );
      assert.match(
        refused((document) => {
          document.types.Step.fields.push({ name: 'rank', typeRef: { name: 'number' } });
          config(document).by = 'rank';
        }),
        /by: Step\.rank is not a string or enum field/
      );
      for (const type of ['Nope', 'Step']) {
        assert.match(
          refused((document) => (config(document).types.verify = type)),
          new RegExp(`types: "verify" names ${type}, which is not a type of the schema document besides Step \\(its types: Check, ReviewResult, VerifyResult\\)`)
        );
      }
      assert.match(refused((document) => (config(document).types.deploy = 'VerifyResult')), /types: "deploy" is not a value of kind \(its values: verify, review, note\)/);
      // A type it checks holds only field types the schema runtime validates.
      assert.equal(
        refused((document) => {
          document.types.Tally = { name: 'Tally', role: 'EmbeddedStruct', fields: [{ name: 'counts', typeRef: { name: 'number', isMap: true } }] };
          config(document).types.verify = 'Tally';
        }),
        'field Tally.counts is a map, which the schema runtime does not validate'
      );
      // A string field picks by any value.
      const byTitle = steps();
      Object.assign(variantsOf(byTitle), { by: 'title', types: { Check: 'VerifyResult' } });
      publish(engine, byTitle);
      engine.instances.create(alice, 'Step', { title: 'Check', kind: 'verify', result: passed });
      assert.deepEqual(createIssues(engine, { title: 'Other', kind: 'verify', result: passed }), [['result', 'variant']]);
    });

    test('a new version keeps field, by and each value\'s type, and may give another value one; Variants comes off a schema with instances, not on', () => {
      const engine = openTestEngine({ driver });
      const document = steps();
      publish(engine, document);
      engine.instances.create(alice, 'Step', { title: 'Check', kind: 'verify', result: passed }, { id: 's1' });
      engine.instances.create(alice, 'Step', { title: 'Note', kind: 'note' }, { id: 's2' });
      const change = (edit: (next: StepsDocument) => void): string => {
        const next = clone(document);
        edit(next);
        return thrown(() => engine.schemas.define(alice, next as unknown as Record<string, unknown>), IncompatibleChangeError).message;
      };
      assert.match(
        change((next) => delete variantsOf(next).types.review),
        /behavior Variants on type Step cannot change its config .*: types keeps "review": an instance whose kind is "review" may hold a ReviewResult, which a version without it refuses/
      );
      assert.match(
        change((next) => (variantsOf(next).types.verify = 'ReviewResult')),
        /types: "verify" stays VerifyResult: an instance's result was checked against it, not against ReviewResult/
      );
      assert.match(change((next) => (variantsOf(next).by = 'title')), /field and by stay result and kind/);

      // A note gains a type: no stored note holds a result.
      document.types.NoteResult = { name: 'NoteResult', role: 'EmbeddedStruct', fields: [{ name: 'text', typeRef: { name: 'string' }, required: true }] };
      variantsOf(document).types.note = 'NoteResult';
      publish(engine, document);
      assert.deepEqual(engine.instances.update(alice, 'Step', 's2', { result: { text: 'hi' } }).data.result, { text: 'hi' });

      // Off: result is open JSON again. On again: refused, the stored results were never checked.
      const plain = clone(document);
      plain.types.Step.behaviors = plain.types.Step.behaviors?.filter((behavior) => behavior.name !== 'Variants');
      publish(engine, plain);
      engine.instances.create(alice, 'Step', { title: 'Note', kind: 'note', result: { anything: 1 } });
      assert.match(
        thrown(() => engine.schemas.define(alice, document as unknown as Record<string, unknown>), IncompatibleChangeError).message,
        /behavior Variants cannot be added to type Step, which has instances: the instances already hold values of result that no type checked/
      );
    });

    test('a type it checks is held to the compatibility rule as a type a field reaches, while both versions check it; a type nothing reaches stays free', () => {
      const engine = openTestEngine({ driver });
      const document = steps();
      document.types.Spare = { name: 'Spare', role: 'EmbeddedStruct', fields: [{ name: 'note', typeRef: { name: 'string' } }] };
      publish(engine, document);
      const change = (edit: (next: StepsDocument) => void): string[] => {
        const next = clone(document);
        edit(next);
        try {
          engine.schemas.define(alice, next as unknown as Record<string, unknown>);
          return [];
        } catch (error) {
          assert.ok(error instanceof IncompatibleChangeError, String(error));
          return error.changes.map((item) => item.message);
        }
      };
      assert.deepEqual(
        change((next) => next.types.VerifyResult.fields.push({ name: 'score', typeRef: { name: 'number' }, required: true })),
        ['field VerifyResult.score is added as required']
      );
      assert.deepEqual(
        change((next) => (next.types.Check.fields[1] = { name: 'ok', typeRef: { name: 'string' }, required: true })),
        ['field Check.ok changes type from boolean to string']
      );
      assert.deepEqual(
        change((next) => next.types.ReviewResult.fields.pop()),
        ['field ReviewResult.notes is removed']
      );
      // A new optional field, or any change to a type nothing reaches, is allowed.
      assert.deepEqual(
        change((next) => next.types.VerifyResult.fields.push({ name: 'score', typeRef: { name: 'number' } })),
        []
      );
      assert.deepEqual(
        change((next) => next.types.Spare.fields.push({ name: 'size', typeRef: { name: 'number' }, required: true })),
        []
      );
      // A version without Variants checks VerifyResult no more, so it may change it.
      assert.deepEqual(
        change((next) => {
          next.types.Step.behaviors = next.types.Step.behaviors?.filter((behavior) => behavior.name !== 'Variants');
          next.types.VerifyResult.fields.push({ name: 'score', typeRef: { name: 'number' }, required: true });
        }),
        []
      );
    });

    test('the describe document and the create and update tools show each variant: an if/then per value of kind and one for every other value', () => {
      const engine = openTestEngine({ driver });
      publish(engine, steps());
      const described = engine.tools.describe(alice, 'Step');
      const verify = {
        type: ['object', 'null'],
        description: 'VerifyResult object',
        additionalProperties: false,
        properties: {
          checks: {
            type: ['array', 'null'],
            description: 'Array of Check values',
            items: {
              type: 'object',
              description: 'Check object',
              additionalProperties: false,
              properties: {
                name: { type: 'string', description: 'A string value' },
                ok: { type: 'boolean', description: 'A boolean value (true or false)' },
              },
              required: ['name', 'ok'],
            },
          },
          passed: { type: 'boolean', description: 'A boolean value (true or false)' },
        },
        required: ['passed'],
      };
      // One per value, in the order the stored config holds them: its keys sorted.
      const rules = described.instance.allOf as Array<Record<string, any>>;
      assert.equal(rules.length, 3);
      assert.equal(rules[0].description, 'While kind is "review", result is a ReviewResult');
      assert.deepEqual(rules[0].then.properties.result.required, ['approved']);
      assert.deepEqual(rules[1], {
        description: 'While kind is "verify", result is a VerifyResult',
        if: { properties: { kind: { const: 'verify' } }, required: ['kind'] },
        then: { properties: { result: verify } },
      });
      assert.deepEqual(rules[2], {
        description: 'While kind holds another value or none, result holds none',
        if: { not: { properties: { kind: { enum: ['review', 'verify'] } }, required: ['kind'] } },
        then: { properties: { result: { type: 'null' } } },
      });
      // create's data carries the same; update's patch the patch form: nothing required, and kind in the patch.
      const params = (name: string) => described.operations.find((operation) => operation.name === name)?.params as { properties: Record<string, any> };
      assert.deepEqual(params('create').properties.data.allOf, rules);
      const patch = params('update').properties.patch.allOf as Array<Record<string, any>>;
      assert.deepEqual(patch[1].if, rules[1].if);
      assert.equal(patch[1].then.properties.result.required, undefined);
      // A list is replaced whole, so its items keep what they require.
      assert.deepEqual(patch[1].then.properties.result.properties.checks.items.required, ['name', 'ok']);
      assert.deepEqual(patch[2].if, { properties: { kind: { not: { enum: ['review', 'verify'] } } }, required: ['kind'] });
      assert.deepEqual(engine.tools.manifest(alice).tools.find((tool) => tool.name === 'step.create')?.parameters, params('create'));
      // The read results carry it too.
      assert.deepEqual(((described.operations.find((operation) => operation.name === 'get')?.result as Record<string, any>).properties.data.allOf), rules);

      // A validator of the instance's JSON Schema agrees with the engine.
      const ajv = new Ajv2020({ strict: false, validateFormats: false });
      const valid = ajv.compile(described.instance);
      const samples: Array<[Record<string, unknown>, boolean]> = [
        [{ title: 't', kind: 'verify', result: passed }, true],
        [{ title: 't', kind: 'verify', result: { approved: true } }, false],
        [{ title: 't', kind: 'verify', result: { passed: true, checks: [{ name: 'lint', ok: true, by: 'ci' }] } }, false],
        [{ title: 't', kind: 'review', result: { approved: true } }, true],
        [{ title: 't', kind: 'note', result: { text: 'hi' } }, false],
        [{ title: 't', kind: 'note' }, true],
        [{ title: 't', kind: 'verify', result: null }, true],
      ];
      for (const [data, accepted] of samples) {
        assert.equal(valid(data), accepted, JSON.stringify(data));
        let engineAccepted = true;
        try {
          engine.instances.create(alice, 'Step', data);
        } catch (error) {
          assert.ok(error instanceof InstanceValidationError);
          engineAccepted = false;
        }
        assert.equal(engineAccepted, accepted, JSON.stringify(data));
      }
    });
  });
}
