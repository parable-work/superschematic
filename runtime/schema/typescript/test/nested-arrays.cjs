// Standalone CLI test runner for arrays of arrays (T[][]) in the schema
// runtime: the IR and JSON readers carry isArrayOfArrays, validation checks
// every innermost element and reports it at field[i][j], list bounds stay
// on the outer list, and an inner list must be an array, never null. Parse,
// serialize, mask and merge reach the innermost elements too.
/* eslint-disable no-console */
const assert = require('node:assert');

const {
  maskType,
  mergeType,
  marshalType,
  parseSchema,
  parseSchemaIR,
  parseType,
  typeToMap,
  validateSchemaType,
  writeSchemaJson,
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

const gridIR = {
  name: 'grid',
  kind: 'General',
  scalars: {
    'Generic.Int64': {
      name: 'Generic.Int64',
      languagePrimitive: 'number',
      primitive: 'Int',
    },
  },
  enums: {
    Shade: {
      name: 'Shade',
      values: [
        { name: 'Light', serializedAs: 'light' },
        { name: 'Dark', serializedAs: 'dark' },
      ],
    },
  },
  types: {
    Point: {
      name: 'Point',
      role: 'EmbeddedStruct',
      fields: [
        { name: 'x', typeRef: { name: 'Generic.Int64' }, required: true },
        { name: 'label', typeRef: { name: 'String' }, secret: true },
      ],
    },
    Grid: {
      name: 'Grid',
      role: 'EmbeddedStruct',
      fields: [
        {
          name: 'counts',
          typeRef: { name: 'Generic.Int64', isArray: true, isArrayOfArrays: true },
          required: true,
          validateListMax: 2,
        },
        {
          name: 'shades',
          typeRef: { name: 'Shade', isArray: true, isArrayOfArrays: true },
        },
        {
          name: 'polygons',
          typeRef: { name: 'Point', isArray: true, isArrayOfArrays: true },
        },
        {
          name: 'tags',
          typeRef: { name: 'Generic.Int64', isArray: true },
        },
      ],
    },
  },
};

const schema = parseSchemaIR(gridIR);

function field(name) {
  return schema.types.Grid.fields.find((f) => f.name === name);
}

// flatten maps ValidationErrors, where a nested object's errors sit under
// its path as an object, to dotted paths with sorted validator names.
function flatten(errors, prefix = '', out = {}) {
  for (const [key, value] of Object.entries(errors)) {
    const path = prefix ? `${prefix}.${key}` : key;
    if (Array.isArray(value)) {
      out[path] = value.map((e) => e.validator).sort();
    } else {
      flatten(value, path, out);
    }
  }
  return out;
}

function verdicts(data) {
  const result = validateSchemaType(schema, 'Grid', data);
  return result === true ? {} : flatten(result);
}

test('IR reader carries isArrayOfArrays and omits it for T[]', () => {
  assert.deepStrictEqual(field('counts').typeRef, {
    name: 'Generic_Int64',
    isArray: true,
    isArrayOfArrays: true,
    elemNonNull: false,
  });
  assert.strictEqual('isArrayOfArrays' in field('tags').typeRef, false);
});

test('IR reader rejects isArrayOfArrays without isArray', () => {
  assert.throws(
    () =>
      parseSchemaIR({
        types: {
          Bad: {
            name: 'Bad',
            role: 'EmbeddedStruct',
            fields: [{ name: 'rows', typeRef: { name: 'String', isArrayOfArrays: true } }],
          },
        },
      }),
    /isArrayOfArrays requires isArray at types\.Bad\.fields\[0\]\.typeRef/,
  );
});

test('ragged, empty outer and empty inner lists are valid', () => {
  assert.deepStrictEqual(verdicts({ counts: [[1, 2, 3], [4]], shades: [['light'], []] }), {});
  assert.deepStrictEqual(verdicts({ counts: [] }), {});
  assert.deepStrictEqual(verdicts({ counts: [[], []], shades: [[]], polygons: [[]] }), {});
});

test('a null or non-list inner list is rejected at field[i]', () => {
  assert.deepStrictEqual(verdicts({ counts: [[1], null], shades: ['light'], polygons: [undefined] }), {
    'counts[1]': ['required'],
    'shades[0]': ['required'],
    'polygons[0]': ['required'],
  });
  const result = validateSchemaType(schema, 'Grid', { counts: [null] });
  assert.strictEqual(result['counts[0]'][0].message, 'counts inner list must be an array.');
});

test('a bad element is reported at field[i][j]', () => {
  assert.deepStrictEqual(
    verdicts({ counts: [[1], [2, 'three']], shades: [['light', 'dark'], ['purple']] }),
    {
      'counts[1][1]': ['type'],
      'shades[1][0]': ['enum'],
    },
  );
});

test('nested object elements report below field[i][j]', () => {
  assert.deepStrictEqual(verdicts({ counts: [], polygons: [[{ x: 1 }], [{ x: 2 }, {}]] }), {
    'polygons[1][1].x': ['required'],
  });
});

test('list bounds apply to the outer list only', () => {
  assert.deepStrictEqual(verdicts({ counts: [[1], [2], [3]] }), { counts: ['listMax'] });
  assert.deepStrictEqual(verdicts({ counts: [[1, 2, 3, 4, 5, 6]] }), {});
});

test('a required T[][] must be present', () => {
  assert.deepStrictEqual(verdicts({}), { counts: ['required'] });
  assert.deepStrictEqual(verdicts({ counts: null }), { counts: ['required'] });
});

test('parse coerces innermost elements and reports at field[i][j]', () => {
  const ok = parseType(schema, 'Grid', { counts: [['1', 2], []] });
  assert.deepStrictEqual(ok.errors, {});
  assert.deepStrictEqual(ok.data.counts, [[1, 2], []]);

  const bad = parseType(schema, 'Grid', { counts: [[1], [2, 'x'], 'row', null] }, { strict: true });
  assert.deepStrictEqual(Object.keys(bad.errors).sort(), ['counts[1][1]', 'counts[2]']);
  assert.strictEqual(bad.errors['counts[1][1]'][0].validator, 'type');
  assert.strictEqual(bad.errors['counts[2]'][0].validator, 'type');
  // A null inner list passes through parse; validation rejects it.
  assert.strictEqual(bad.data.counts[3], null);
});

test('mask clears secrets inside nested object elements', () => {
  const masked = maskType(schema, 'Grid', {
    counts: [],
    polygons: [[{ x: 1, label: 'a' }], [], [{ x: 2, label: 'b' }]],
  });
  assert.deepStrictEqual(masked.polygons, [[{ x: 1, label: null }], [], [{ x: 2, label: null }]]);
});

test('merge keeps stored secrets inside nested object elements', () => {
  const merged = mergeType(
    schema,
    'Grid',
    { counts: [], polygons: [[{ x: 1, label: '' }], [{ x: 9 }]] },
    { counts: [], polygons: [[{ x: 1, label: 'kept' }], [{ x: 2, label: 'also' }]] },
  );
  assert.deepStrictEqual(merged.polygons, [[{ x: 1, label: 'kept' }], [{ x: 9, label: 'also' }]]);
});

test('serialize drops unknown keys in nested object elements and writes T[][]', () => {
  const data = { counts: [[1, 2], []], polygons: [[{ x: 1, extra: true }]] };
  const mapped = typeToMap(schema, 'Grid', data);
  assert.deepStrictEqual(mapped.errors, {});
  assert.deepStrictEqual(mapped.data.polygons, [[{ x: 1 }]]);

  const strict = typeToMap(schema, 'Grid', data, { strict: true });
  assert.deepStrictEqual(flatten(strict.errors), { 'polygons[0][0].extra': ['unknown_field'] });

  const marshaled = marshalType(schema, 'Grid', { counts: [[1, 2], []], polygons: [[{ x: 1 }]] });
  assert.deepStrictEqual(marshaled.errors, {});
  assert.strictEqual(marshaled.json, '{"counts":[[1,2],[]],"polygons":[[{"x":1}]]}');
});

test('the runtime schema JSON form round-trips T[][] and refuses a third level', () => {
  const written = JSON.parse(writeSchemaJson(schema));
  const counts = written.definitions.Grid.properties.counts;
  assert.deepStrictEqual(counts.type, 'array');
  assert.deepStrictEqual(counts.items.type, 'array');
  assert.deepStrictEqual(written.definitions.Grid.properties.tags.items.type, undefined);

  const reread = parseSchema(written);
  const rereadCounts = reread.types.Grid.fields.find((f) => f.name === 'counts');
  assert.strictEqual(rereadCounts.typeRef.isArray, true);
  assert.strictEqual(rereadCounts.typeRef.isArrayOfArrays, true);
  const rereadTags = reread.types.Grid.fields.find((f) => f.name === 'tags');
  assert.strictEqual('isArrayOfArrays' in rereadTags.typeRef, false);

  assert.throws(
    () =>
      parseSchema({
        definitions: {
          Cube: {
            type: 'object',
            'x-kind': 'type',
            properties: {
              cells: { type: 'array', items: { type: 'array', items: { type: 'array', items: { type: 'integer' } } } },
            },
          },
        },
      }),
    /arrays nest at most two levels \(T\[\]\[\]\)/,
  );
});

if (failures > 0) {
  console.error(`${failures} test(s) failed`);
  process.exit(1);
}
console.log('nested-arrays: all tests passed');
