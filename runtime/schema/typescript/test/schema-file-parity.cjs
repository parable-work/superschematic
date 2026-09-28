// The strict schema-file loader against the Go data-form reader, over
// runtime/schema/testdata/schema_file_parity.json.
// internal/loader/schemafile (TestSchemaFileParityCorpus) writes it: each
// payload with the Go reader's verdict and, for an accepted one, the
// decoded document as ir.CanonicalJSON writes it, and JSON texts with
// ir.CanonicalJSON's output. The loader must reach the same verdict and
// write the same bytes. The "core" vectors load against the default
// meta-schema, @superschematic/schema-ir/schema-file.json; the "extended"
// ones against the meta-schema of a registry that adds a kind, extension
// decorators, documents and its own invocation policy,
// schema_file_parity.meta-schema.json.
/* eslint-disable no-console */
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');

const {
  SchemaFileError,
  SchemaFileLoader,
  canonicalJSON,
  loadSchemaFile,
} = require('../dist/runtime/index.js');

const testdata = path.join(__dirname, '../../testdata');
const corpus = JSON.parse(fs.readFileSync(path.join(testdata, 'schema_file_parity.json'), 'utf8'));
const extendedMetaSchemaText = fs.readFileSync(path.join(testdata, 'schema_file_parity.meta-schema.json'), 'utf8');
const extendedMetaSchema = JSON.parse(extendedMetaSchemaText);

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

for (const vector of corpus.canonical) {
  test(`canonical: ${vector.name}`, () => {
    if (vector.error !== undefined) {
      assert.throws(() => canonicalJSON(vector.input), SyntaxError, `Go refuses it: ${vector.error}`);
      return;
    }
    assert.strictEqual(canonicalJSON(vector.input), vector.output);
  });
}

const loaders = {
  core: new SchemaFileLoader(),
  extended: new SchemaFileLoader({ metaSchema: extendedMetaSchema }),
};

for (const vector of corpus.vectors) {
  test(`${vector.registry}: ${vector.name}`, () => {
    const loader = loaders[vector.registry];
    assert.ok(loader, `unknown registry ${vector.registry}`);
    if (!vector.accept) {
      assert.throws(
        () => loader.load(vector.input, vector.name),
        (error) => error instanceof SchemaFileError && error.source === vector.name,
        `Go refuses it: ${vector.error}`
      );
      return;
    }
    const loaded = loader.load(vector.input, vector.name);
    assert.strictEqual(loaded.canonical, vector.canonical);
    assert.deepStrictEqual(loaded.document, JSON.parse(vector.canonical));
  });
}

test('loadSchemaFile uses the core meta-schema by default', () => {
  const vector = corpus.vectors.find((v) => v.registry === 'core' && v.accept);
  assert.strictEqual(loadSchemaFile(vector.input).canonical, vector.canonical);
  const extended = corpus.vectors.find((v) => v.registry === 'extended' && v.name === "the registry's policy value");
  assert.throws(() => loadSchemaFile(extended.input), SchemaFileError);
  assert.strictEqual(
    loadSchemaFile(extended.input, { metaSchema: extendedMetaSchemaText }).canonical,
    extended.canonical
  );
});

test('a repeated key names its JSON pointer', () => {
  const cases = [
    ['{"name": "Item", "role": "EmbeddedStruct", "fields": [], "fields": []}', '/fields', 'fields'],
    [
      '{"types": {"a/b~c": {"name": "a/b~c", "role": "EmbeddedStruct", "fields": [{"name": "x", "typeRef": {"name": "String"}, "extensions": {}, "extensions": {}}]}}}',
      '/types/a~1b~0c/fields/0/extensions',
      'extensions',
    ],
    ['{"documents": {"d": [{"k": [1, {"a": 1, "b": 2, "a": 3}]}]}}', '/documents/d/0/k/1/a', 'a'],
    ['{"name": "Item", "role": "EmbeddedStruct", "description": "x", "descr\\u0069ption": "x"}', '/description', 'description'],
    // Go reads each half of a surrogate pair alone as U+FFFD.
    [
      '{"name": "Item", "role": "EmbeddedStruct", "implements": [{"name": "Named", "configArgs": {"\\ud800": 1, "\\udc00": 2}}]}',
      '/implements/0/configArgs/\ufffd',
      '\ufffd',
    ],
  ];
  for (const [input, path, key] of cases) {
    assert.throws(
      () => loaders.core.load(input, 'payload'),
      (error) =>
        error instanceof SchemaFileError &&
        error.issues.length === 1 &&
        error.issues[0].path === path &&
        error.issues[0].message === `repeated object key ${JSON.stringify(key)}`,
      input
    );
  }
  // A string that holds a repeated key, and the same key in sibling objects
  // and array elements.
  const loaded = loaders.core.load(
    '{"types": {"A": {"name": "A", "role": "EmbeddedStruct", "description": "{\\"name\\": 1, \\"name\\": 2}", "implements": [{"name": "N", "configArgs": {"k": ["k", "k"]}}, {"name": "M", "configArgs": {"k": 1}}]}, "B": {"name": "B", "role": "EmbeddedStruct"}}}'
  );
  assert.deepStrictEqual(Object.keys(loaded.document.types), ['A', 'B']);
});

test('a meta-schema without the invocation policy default is refused', () => {
  const metaSchema = JSON.parse(extendedMetaSchemaText);
  delete metaSchema.$defs.OperationMCP.properties.review.default;
  assert.throws(() => new SchemaFileLoader({ metaSchema }), /invocation policy/);
});

if (failures === 0) {
  console.log('schema-file-parity: all tests passed');
}
