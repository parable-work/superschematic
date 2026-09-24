// Standalone CLI test runner for the list rules of T[] and T[][] in the
// validate phase: a required list means present, not non-empty; listMin and
// listMax bound the outer list; a field's own constraints apply to every
// element and every innermost element; an element and an inner list are
// never null; a non-list inner value is a type error.
/* eslint-disable no-console */
const assert = require('node:assert');

const { parseSchemaIR, validateSchemaType } = require('../dist/runtime/index.js');

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

const list = { name: 'String', isArray: true };
const grid = { name: 'String', isArray: true, isArrayOfArrays: true };
const schema = parseSchemaIR({
  name: 'list-rules',
  kind: 'General',
  scalars: { Code: { name: 'Code', primitive: 'String', languagePrimitive: 'string' } },
  types: {
    Rules: {
      name: 'Rules',
      role: 'EmbeddedStruct',
      fields: [
        { name: 'tags', typeRef: list, required: true, validateListMin: 1, validateListMax: 3, validateMaxLength: 4 },
        { name: 'rows', typeRef: grid, required: true },
        {
          name: 'cells',
          typeRef: grid,
          validateListMin: 1,
          validateListMax: 2,
          validateMinLength: 2,
          validatePattern: '^[a-z]+$',
        },
        { name: 'codes', typeRef: { name: 'Code', isArray: true }, validateMaxLength: 4 },
        { name: 'codeGrid', typeRef: { name: 'Code', isArray: true, isArrayOfArrays: true }, validateMaxLength: 4 },
        { name: 'scores', typeRef: { name: 'Float', isArray: true }, validateMin: 0, validateMax: 1 },
      ],
    },
  },
});

function verdicts(data) {
  const result = validateSchemaType(schema, 'Rules', data);
  if (result === true) {
    return {};
  }
  const out = {};
  for (const [key, errors] of Object.entries(result)) {
    out[key] = errors.map((e) => e.validator).sort();
  }
  return out;
}

const base = (extra) => ({ tags: ['a'], rows: [], ...extra });

test('a required list is present, not non-empty', () => {
  assert.deepStrictEqual(verdicts({ tags: [], rows: [] }), { tags: ['listMin'] });
  assert.deepStrictEqual(verdicts(base({})), {});
  assert.deepStrictEqual(verdicts({}), { tags: ['required'], rows: ['required'] });
  assert.deepStrictEqual(verdicts({ tags: 'a', rows: [] }), { tags: ['required'] });
});

test('listMin and listMax bound the list, and the outer list of T[][]', () => {
  assert.deepStrictEqual(verdicts(base({ tags: ['a', 'b', 'c', 'd'] })), { tags: ['listMax'] });
  assert.deepStrictEqual(verdicts(base({ cells: [] })), { cells: ['listMin'] });
  assert.deepStrictEqual(verdicts(base({ cells: [[], [], []] })), { cells: ['listMax'] });
  assert.deepStrictEqual(verdicts(base({ cells: [['ab', 'cd', 'ef', 'gh']] })), {});
});

test("a field's constraints apply to every element and innermost element", () => {
  assert.deepStrictEqual(verdicts(base({ tags: ['ok', 'toolong'] })), { 'tags[1]': ['maxLength'] });
  assert.deepStrictEqual(verdicts(base({ cells: [['ab'], ['x', 'AB']] })), {
    'cells[1][0]': ['minLength'],
    'cells[1][1]': ['pattern'],
  });
  assert.deepStrictEqual(verdicts(base({ codes: ['abcde'], codeGrid: [['ab', 'abcde']] })), {
    'codes[0]': ['maxLength'],
    'codeGrid[0][1]': ['maxLength'],
  });
  assert.deepStrictEqual(verdicts(base({ scores: [0.5, -1, 2] })), { 'scores[1]': ['min'], 'scores[2]': ['max'] });
});

test('an element is never null, in a required or an optional list', () => {
  assert.deepStrictEqual(verdicts(base({ tags: ['a', null], codes: [null], codeGrid: [[null]] })), {
    'tags[1]': ['required'],
    'codes[0]': ['required'],
    'codeGrid[0][0]': ['required'],
  });
});

test('a null inner list is required and a non-list one a type error', () => {
  const result = validateSchemaType(schema, 'Rules', base({ rows: [null, 'a', []] }));
  assert.deepStrictEqual(result['rows[0]'], [{ validator: 'required', message: 'required field' }]);
  assert.deepStrictEqual(result['rows[1]'], [{ validator: 'type', message: 'expected an array' }]);
  assert.strictEqual(result['rows[2]'], undefined);
});

if (failures > 0) {
  console.error(`${failures} test(s) failed`);
  process.exit(1);
}
console.log('list-rules: all tests passed');
