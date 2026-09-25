// Standalone CLI test runner: a scalar value the schema's pattern rejects is
// reported once, as "pattern". The registered scalar validator checks the
// same pattern again, so it only sees values the IR constraints accept.
/* eslint-disable no-console */
const assert = require('node:assert');

const {
  ScalarValidatorRegistry,
  loadType,
  parseSchemaIR,
  validateSchemaType,
} = require('../dist/runtime/index.js');

let failures = 0;

function test(name, fn) {
  try {
    fn();
    console.log(`ok - ${name}`);
  } catch (error) {
    failures += 1;
    process.exitCode = 1;
    console.error(`FAIL - ${name}`);
    console.error(error && error.stack ? error.stack : String(error));
  }
}

const schema = parseSchemaIR({
  name: 'scalar-errors',
  kind: 'General',
  scalars: {
    'Mystery.Code': {
      name: 'Mystery.Code',
      primitive: 'String',
      languagePrimitive: 'string',
      pattern: '^[a-z]+$',
    },
  },
  types: {
    Holder: {
      name: 'Holder',
      role: 'EmbeddedStruct',
      fields: [
        { name: 'code', typeRef: { name: 'Mystery.Code' } },
        { name: 'codes', typeRef: { name: 'Mystery.Code', isArray: true } },
      ],
    },
  },
});

const calls = [];
const scalarRegistry = new ScalarValidatorRegistry({
  'Mystery.Code': (value) => {
    calls.push(value);
    return [{ validator: 'pattern', message: `core rejects ${value}` }];
  },
});

function verdicts(data) {
  const result = validateSchemaType(schema, 'Holder', data, { scalarRegistry });
  return result === true ? {} : result;
}

test('a value the pattern rejects is reported once and not re-checked', () => {
  calls.length = 0;
  const result = verdicts({ code: 'NOT-A-CODE', codes: ['ok', 'NOT-A-CODE'] });
  assert.deepStrictEqual(
    result.code.map((e) => e.validator),
    ['pattern']
  );
  assert.deepStrictEqual(
    result['codes[1]'].map((e) => e.validator),
    ['pattern']
  );
  assert.deepStrictEqual(calls, ['ok']);
});

test('a value the pattern accepts goes to the registered validator', () => {
  calls.length = 0;
  const result = verdicts({ code: 'abc' });
  assert.deepStrictEqual(result.code, [{ validator: 'pattern', message: 'core rejects abc' }]);
  assert.deepStrictEqual(calls, ['abc']);
});

// A scalar whose json_schema type mapping is "any" holds any JSON value but
// null, whatever its name and its String primitive: no type or pattern
// check, and no registered string validator, sees the value.
const anyJSONSchema = parseSchemaIR({
  name: 'scalar-errors-any-json',
  kind: 'General',
  scalars: {
    'Mystery.Blob': {
      name: 'Mystery.Blob',
      primitive: 'String',
      languagePrimitive: 'string',
      pattern: '^[a-z]+$',
      typeMappings: { json_schema: 'any' },
    },
  },
  types: {
    Doc: {
      name: 'Doc',
      role: 'EmbeddedStruct',
      fields: [
        { name: 'body', typeRef: { name: 'Mystery.Blob' }, required: true },
        { name: 'parts', typeRef: { name: 'Mystery.Blob', isArray: true } },
      ],
    },
  },
});
const blobCalls = [];
const blobRegistry = new ScalarValidatorRegistry({
  'Mystery.Blob': (value) => {
    blobCalls.push(value);
    return [{ validator: 'pattern', message: 'a string validator ran' }];
  },
});

function anyJSONVerdicts(data) {
  const result = validateSchemaType(anyJSONSchema, 'Doc', data, { scalarRegistry: blobRegistry });
  if (result === true) return {};
  return Object.fromEntries(Object.entries(result).map(([k, v]) => [k, v.map((e) => e.validator)]));
}

test('an any-JSON scalar accepts every JSON value but null', () => {
  blobCalls.length = 0;
  for (const value of [{ k: 1, none: null }, [1, 'two', null], 'NOT JSON', '', 42, false]) {
    assert.deepStrictEqual(anyJSONVerdicts({ body: value, parts: [value] }), {}, JSON.stringify(value));
  }
  assert.deepStrictEqual(blobCalls, []);
  assert.deepStrictEqual(anyJSONVerdicts({ body: null, parts: [1, null] }), {
    body: ['required'],
    'parts[1]': ['required'],
  });
  assert.deepStrictEqual(anyJSONVerdicts({}), { body: ['required'] });
});

test('an any-JSON scalar refuses a value JSON cannot carry', () => {
  const cyclic = {};
  cyclic.self = cyclic;
  for (const value of [Number.NaN, { k: undefined }, () => 1, new Date(0), cyclic]) {
    assert.deepStrictEqual(anyJSONVerdicts({ body: value }), { body: ['type'] });
  }
});

test('loading an any-JSON scalar keeps the value as it is', () => {
  const payload = '{"body": {"k": [1, null]}, "parts": [[1], "s", 2, true]}';
  for (const strict of [false, true]) {
    const result = loadType(anyJSONSchema, 'Doc', payload, { strict });
    assert.deepStrictEqual(result.errors, {});
    assert.deepStrictEqual(result.data, JSON.parse(payload));
  }
});

if (failures > 0) {
  console.error(`scalar-errors: ${failures} failing test(s)`);
} else {
  console.log('scalar-errors: all tests passed');
}
