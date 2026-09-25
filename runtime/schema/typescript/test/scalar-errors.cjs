// Standalone CLI test runner: a scalar value the schema's pattern rejects is
// reported once, as "pattern". The registered scalar validator checks the
// same pattern again, so it only sees values the IR constraints accept.
/* eslint-disable no-console */
const assert = require('node:assert');

const {
  ScalarValidatorRegistry,
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

if (failures > 0) {
  console.error(`scalar-errors: ${failures} failing test(s)`);
} else {
  console.log('scalar-errors: all tests passed');
}
