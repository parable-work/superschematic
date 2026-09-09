// Standalone CLI test runner: string length validation must count Unicode
// code points (Go rune count, Python len()), not UTF-16 code units.
/* eslint-disable no-console */
const assert = require('node:assert');

const { parseSchema, validateSchemaInput } = require('../dist/runtime/index.js');

let failures = 0;

function test(name, fn) {
  try {
    fn();
    console.log(`ok - ${name}`);
  } catch (error) {
    failures += 1;
    console.error(`FAIL - ${name}`);
    console.error(error && error.stack ? error.stack : String(error));
  }
}

// 1 code point, 2 UTF-16 units.
const ASTRAL = '\u{1D54F}';

const fieldSchema = parseSchema({
  name: 'length-parity',
  'x-rootType': 'TestInput',
  definitions: {
    TestInput: {
      type: 'object',
      'x-kind': 'input',
      properties: {
        one: { type: 'string', 'x-validateMaxLength': 1 },
        two: { type: 'string', 'x-validateMinLength': 2 },
        pair: { type: 'string', 'x-validateMinLength': 2, 'x-validateMaxLength': 2 },
      },
    },
  },
  'x-platformSchemaVersion': '1',
});

function fieldErrors(data, field) {
  const result = validateSchemaInput(fieldSchema, 'TestInput', data);
  return result[field] || [];
}

test('field maxLength counts code points, not UTF-16 units', () => {
  assert.deepStrictEqual(fieldErrors({ one: ASTRAL }, 'one'), []);
});

test('field minLength still rejects a single code point', () => {
  const errors = fieldErrors({ two: ASTRAL }, 'two');
  assert.strictEqual(errors.length, 1);
  assert.strictEqual(errors[0].validator, 'minLength');
});

test('field length bounds unchanged for ASCII', () => {
  assert.deepStrictEqual(fieldErrors({ pair: 'ab' }, 'pair'), []);
});

const scalarSchema = parseSchema({
  name: 'length-parity-scalar',
  'x-rootType': 'NameInput',
  definitions: {
    NameInput: {
      type: 'object',
      'x-kind': 'input',
      properties: {
        name: { $ref: '#/definitions/scalars/Identity.Name' },
      },
    },
  },
  'x-platformSchemaVersion': '1',
});

test('scalar maxLength counts code points, not UTF-16 units', () => {
  // Identity.Name max 80: 41 astral chars = 41 code points (82 UTF-16 units).
  const result = validateSchemaInput(scalarSchema, 'NameInput', { name: ASTRAL.repeat(41) });
  const errors = result.name || [];
  assert.deepStrictEqual(errors.filter((e) => e.validator === 'maxLength'), []);
});

test('scalar minLength still rejects a single code point', () => {
  const result = validateSchemaInput(scalarSchema, 'NameInput', { name: ASTRAL });
  const errors = result.name || [];
  assert.strictEqual(errors.filter((e) => e.validator === 'minLength').length, 1);
});

process.exit(failures === 0 ? 0 : 1);
