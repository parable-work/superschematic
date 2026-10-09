// Standalone CLI test runner: parse fills an absent field's default as the
// value its text stands for, for the IR's number, boolean and string as for
// the GraphQL names (D14, amended: the loader checks defaults and examples
// by the validators' rules).
/* eslint-disable no-console */
const assert = require('node:assert');

const { parseSchemaIR, parseType } = require('../dist/runtime/index.js');

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

const field = (name, type, fallback) => ({ name, typeRef: { name: type }, default: fallback });

const schema = parseSchemaIR({
  name: 'parse-defaults',
  kind: 'General',
  types: {
    Settings: {
      name: 'Settings',
      fields: [
        field('ratio', 'number', '2.5'),
        field('enabled', 'boolean', 'false'),
        field('label', 'string', '7'),
        field('count', 'Int', '3'),
        field('on', 'Boolean', 'true'),
      ],
    },
  },
});

test("the IR's number, boolean and string defaults fill as a number, a boolean and the text", () => {
  const { data, errors } = parseType(schema, 'Settings', {}, { strict: true });
  assert.deepStrictEqual(errors, {});
  assert.deepStrictEqual(data, { ratio: 2.5, enabled: false, label: '7', count: 3, on: true });
});

test('a value given is kept, and no default fills it', () => {
  const { data } = parseType(schema, 'Settings', { ratio: 1, enabled: true, label: 'x', count: 9, on: false }, { strict: true });
  assert.deepStrictEqual(data, { ratio: 1, enabled: true, label: 'x', count: 9, on: false });
});

if (failures > 0) {
  console.error(`${failures} test(s) failed`);
}
