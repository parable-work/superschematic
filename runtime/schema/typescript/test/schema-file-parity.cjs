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

test('a meta-schema without the invocation policy default is refused', () => {
  const metaSchema = JSON.parse(extendedMetaSchemaText);
  delete metaSchema.$defs.OperationMCP.properties.review.default;
  assert.throws(() => new SchemaFileLoader({ metaSchema }), /invocation policy/);
});

if (failures === 0) {
  console.log('schema-file-parity: all tests passed');
}
