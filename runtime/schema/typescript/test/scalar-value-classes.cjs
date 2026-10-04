// Standalone CLI test runner: the package exports each builtin scalar's
// value class (BUILTIN_SCALAR_VALUE_CLASSES), keyed as BUILTIN_SCALARS is,
// from both its entry points. internal/tools/scalarcatalog writes it with
// the graph descriptor's rule, and its Go test holds every class to that
// rule; this checks what a TypeScript reader gets.
/* eslint-disable no-console */
const assert = require('node:assert');

const root = require('../dist/index.js');
const { BUILTIN_SCALARS, BUILTIN_SCALAR_VALUE_CLASSES } = require('../dist/runtime/index.js');

// The classes a single value of a scalar can have (D19): every class but
// enum, which only an enum's field has.
const CLASSES = new Set(['string', 'integer', 'number', 'boolean', 'uuid', 'dateTime', 'date', 'time', 'duration', 'json']);

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

test('the package root exports the value classes the runtime entry point does', () => {
  assert.strictEqual(root.BUILTIN_SCALAR_VALUE_CLASSES, BUILTIN_SCALAR_VALUE_CLASSES);
});

test('each class is a builtin scalar\'s, by the key BUILTIN_SCALARS uses, and one a single value can have', () => {
  const keys = Object.keys(BUILTIN_SCALAR_VALUE_CLASSES);
  assert.ok(keys.length > 0, 'no scalar has a class');
  for (const key of keys) {
    assert.ok(Object.prototype.hasOwnProperty.call(BUILTIN_SCALARS, key), `${key} is not a key of BUILTIN_SCALARS`);
    assert.ok(CLASSES.has(BUILTIN_SCALAR_VALUE_CLASSES[key]), `${key} has class ${BUILTIN_SCALAR_VALUE_CLASSES[key]}`);
  }
});

test('a scalar whose values are JSON is json, and a UUID is a uuid', () => {
  assert.strictEqual(BUILTIN_SCALAR_VALUE_CLASSES.Generic_JSON, 'json');
  assert.strictEqual(BUILTIN_SCALAR_VALUE_CLASSES.Identity_UUID, 'uuid');
});

if (failures > 0) {
  console.error(`${failures} failure(s)`);
}
