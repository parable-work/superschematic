// Standalone CLI test runner for the cross-language validation corpus,
// runtime/schema/testdata/validation_parity.json. The generated-validator
// parity harness (internal/generator/parity) writes it: the matrix schema's
// IR and one vector table with the expected verdicts. The Go and Python
// runtime suites assert the same rows.
//
// The Python runtime reads the schema JSON form, not the IR, so this runner
// also keeps validation_parity.document.json equal to writeSchemaJson of the
// corpus IR, and validates every vector through both readers. Set
// UPDATE_PARITY_DOCUMENT=1 to rewrite the document after the corpus changes.
/* eslint-disable no-console */
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');

const {
  parseSchema,
  parseSchemaIR,
  validateSchemaType,
  writeSchemaJson,
} = require('../dist/runtime/index.js');

const testdata = path.join(__dirname, '../../testdata');
const corpus = JSON.parse(fs.readFileSync(path.join(testdata, 'validation_parity.json'), 'utf8'));
const documentPath = path.join(testdata, 'validation_parity.document.json');

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

// flatten maps ValidationErrors, where a nested object's errors sit under
// its path as an object, to dotted paths with sorted validator names.
function flatten(errors, prefix = '', out = {}) {
  for (const [key, value] of Object.entries(errors)) {
    const at = prefix ? `${prefix}.${key}` : key;
    if (Array.isArray(value)) {
      out[at] = value.map((e) => e.validator).sort();
    } else {
      flatten(value, at, out);
    }
  }
  return out;
}

function verdicts(schema, vector) {
  const result = validateSchemaType(schema, vector.type, vector.payload);
  return result === true ? {} : flatten(result);
}

const fromIR = parseSchemaIR(corpus.schema);
const document = `${JSON.stringify(JSON.parse(writeSchemaJson(fromIR)), null, 2)}\n`;

test('the schema JSON document matches the corpus IR', () => {
  if (process.env.UPDATE_PARITY_DOCUMENT === '1') {
    fs.writeFileSync(documentPath, document);
  }
  const committed = fs.existsSync(documentPath) ? fs.readFileSync(documentPath, 'utf8') : '';
  assert.strictEqual(
    committed,
    document,
    'validation_parity.document.json is stale; run UPDATE_PARITY_DOCUMENT=1 bun run test'
  );
});

const fromDocument = parseSchema(JSON.parse(document));

for (const vector of corpus.vectors) {
  test(`${vector.name} (IR reader)`, () => {
    assert.deepStrictEqual(verdicts(fromIR, vector), vector.want);
  });
  test(`${vector.name} (JSON reader)`, () => {
    assert.deepStrictEqual(verdicts(fromDocument, vector), vector.want);
  });
}

if (failures > 0) {
  console.error(`validation-parity: ${failures} failing test(s)`);
} else {
  console.log('validation-parity: all tests passed');
}
