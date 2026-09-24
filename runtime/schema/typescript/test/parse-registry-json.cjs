// A JSON-shaped custom-parse scalar (Generic.StringMap) parses to the core's
// canonical JSON text. Its generated superscalar parse function returns the
// decoded map, so an adapter that stringified that result would hand the
// walker "[object Object]".
const assert = require('node:assert');

const { BUILTIN_SCALARS } = require('../dist/runtime/builtin-scalars.generated.js');
const { createDefaultScalarParseRegistry } = require('../dist/runtime/parse/registry.js');

assert.strictEqual(BUILTIN_SCALARS.Generic_StringMap.hasCustomParse, true, 'Generic.StringMap is custom-parse in the catalog');

const parse = createDefaultScalarParseRegistry().get('Generic.StringMap');
assert.strictEqual(typeof parse, 'function', 'Generic.StringMap has a registered parser');

const [canonical, errors] = parse('{ "tier": "gold", "region": "eu" }');
assert.deepStrictEqual(errors, []);
assert.strictEqual(canonical, '{"region":"eu","tier":"gold"}');

const [empty, emptyErrors] = parse('{}');
assert.deepStrictEqual(emptyErrors, []);
assert.strictEqual(empty, '{}');

for (const invalid of ['{"region":null}', '{"region":1}', 'not json', '["eu"]']) {
  const [unchanged, rejected] = parse(invalid);
  assert.strictEqual(unchanged, invalid, `${invalid} is returned unchanged`);
  assert.strictEqual(rejected.length, 1, `${invalid} is rejected`);
  assert.strictEqual(rejected[0].validator, 'parse');
}

console.log('parse-registry-json: Generic.StringMap parses to canonical JSON text');
