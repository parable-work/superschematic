// Standalone CLI test runner for the Schema IR reader (parseSchemaIR):
// verifies superschematic --emit-ir JSON converts into the runtime Schema shape the
// validators and schema-renderer consume. Console output IS the report, so
// no-console is disabled by design (same convention as conformance.cjs).
/* eslint-disable no-console */
const assert = require('node:assert');

const {
  createDefaultScalarParseRegistry,
  parseSchemaIR,
  parseType,
} = require('../dist/runtime/index.js');
const { coerceInt } = require('../dist/runtime/parse/coerce.js');

let failures = 0;

function test(name, fn) {
  try {
    fn();
    console.log(`ok - ${name}`);
  } catch (error) {
    failures += 1;
    // Fail-closed regardless of where the summary gate sits.
    process.exitCode = 1;
    console.error(`FAIL - ${name}`);
    console.error(error && error.stack ? error.stack : String(error));
  }
}

const sampleIR = {
  name: 'web-admin-api',
  kind: 'API',
  imports: [{ package: '@schemas/enums', types: ['TenantTierEnum'] }],
  scalars: {
    'Identity.UserID': {
      name: 'Identity.UserID',
      languagePrimitive: 'string',
      primitive: 'String',
      pattern: '^[0-9A-Za-z]{1,22}$',
    },
    'Generic.Int64': {
      name: 'Generic.Int64',
      languagePrimitive: 'number',
      primitive: 'Int',
    },
    'Finance.Money': {
      name: 'Finance.Money',
      languagePrimitive: 'number',
      primitive: 'Int',
      minimum: 0,
      maximum: 9007199254740991,
      hasCustomParse: true,
    },
  },
  types: {
    UserBase: {
      name: 'UserBase',
      role: 'EmbeddedStruct',
      comment: 'The base info about a user',
      fields: [
        { name: 'id', typeRef: { name: 'Identity.UserID' }, required: true },
        { name: 'email', typeRef: { name: 'Contact.Email' }, required: true },
        { name: 'money', typeRef: { name: 'Finance.Money' } },
        { name: 'staffRoles', typeRef: { name: 'StaffRole', isArray: true } },
        {
          name: 'secretNote',
          typeRef: { name: 'String' },
          uiHidden: true,
          semanticRole: 'metadata_timestamp',
        },
        {
          name: 'occurredAt',
          typeRef: { name: 'Temporal.DateTime' },
          temporalFormat: 'unix_millis',
        },
      ],
    },
    CreateUserInput: {
      name: 'CreateUserInput',
      role: 'APIInput',
      fields: [
        { name: 'email', typeRef: { name: 'Contact.Email' }, required: true },
      ],
    },
  },
  enums: {
    TenantOrderByField: {
      name: 'TenantOrderByField',
      owner: 'web-admin-api',
      values: [{ name: 'CREATED_AT' }, { name: 'NAME', serializedAs: 'name' }],
    },
  },
};

test('splits IR types into runtime types and inputs by role', () => {
  const schema = parseSchemaIR(sampleIR);
  assert.ok(schema.types.UserBase, 'UserBase should be a runtime type');
  assert.strictEqual(schema.types.UserBase.kind, 'object');
  assert.ok(
    schema.inputs.CreateUserInput,
    'CreateUserInput should be a runtime input',
  );
  assert.strictEqual(schema.inputs.CreateUserInput.kind, 'input');
  assert.strictEqual(schema.types.CreateUserInput, undefined);
});

test('normalizes namespaced scalar names to underscore form', () => {
  const schema = parseSchemaIR(sampleIR);
  assert.ok(
    schema.scalars.Identity_UserID,
    'scalar keyed by flat name derived from canonical',
  );
  assert.strictEqual(schema.scalars.Identity_UserID.primitive, 'String');
  assert.strictEqual(schema.scalars.Generic_Int64.primitive, 'Int');

  const idField = schema.types.UserBase.fields.find((f) => f.name === 'id');
  assert.strictEqual(idField.typeRef.name, 'Identity_UserID');
  assert.strictEqual(idField.required, true);
});

test('maps field directive metadata and array typeRefs', () => {
  const schema = parseSchemaIR(sampleIR);
  const fields = schema.types.UserBase.fields;

  const staffRoles = fields.find((f) => f.name === 'staffRoles');
  assert.strictEqual(staffRoles.typeRef.isArray, true);
  assert.strictEqual(staffRoles.typeRef.name, 'StaffRole');

  const hidden = fields.find((f) => f.name === 'secretNote');
  assert.strictEqual(hidden.uiHidden, true);
  assert.strictEqual(hidden.semanticRole, 'metadata_timestamp');
  assert.strictEqual(hidden.required, false);
  assert.strictEqual(hidden.jsonKey, 'secretNote');

  // Declared epoch unit survives the IR wire form.
  const occurredAt = fields.find((f) => f.name === 'occurredAt');
  assert.strictEqual(occurredAt.temporalFormat, 'unix_millis');
});


test('parses enums with serializedAs and merges builtin scalars', () => {
  const schema = parseSchemaIR(sampleIR);
  assert.deepStrictEqual(
    schema.enums.TenantOrderByField.values.map((v) => v.name),
    ['CREATED_AT', 'NAME'],
  );
  assert.strictEqual(
    schema.enums.TenantOrderByField.values[1].serializedAs,
    'name',
  );

  // Contact.Email is not declared in the sample IR; the builtin scalar
  // registry must still resolve its underscore form.
  assert.ok(schema.scalars.Contact_Email, 'builtin scalars merged into schema');
  assert.deepStrictEqual(schema.imports, [
    { from: '@schemas/enums', types: ['TenantTierEnum'] },
  ]);
});

test('temporal integer units expose core-backed parsers and bounds', () => {
  const registry = createDefaultScalarParseRegistry();
  const schema = parseSchemaIR(sampleIR);
  for (const unit of ['Milliseconds', 'Seconds', 'Minutes', 'Hours', 'Days']) {
    assert.ok(
      registry.has(`Temporal.${unit}`),
      `${unit} parser should be registered`,
    );
    assert.ok(
      registry.has(`Temporal_${unit}`),
      `${unit} flat-name parser should resolve`,
    );
    const scalar = schema.scalars[`Temporal_${unit}`];
    assert.strictEqual(scalar.minimum, -9007199254740991);
    assert.strictEqual(scalar.maximum, 9007199254740991);
    assert.strictEqual(scalar.hasCustomParse, true);
  }
});

test('integer coercion rejects fractions and unsafe JavaScript integers', () => {
  assert.deepStrictEqual(coerceInt(-12, false), [-12, true]);
  assert.deepStrictEqual(coerceInt(' 12 ', false), [12, true]);
  assert.deepStrictEqual(coerceInt(1.5, false), [0, false]);
  assert.deepStrictEqual(coerceInt('1.5', false), [0, false]);
  assert.deepStrictEqual(coerceInt(9007199254740992, false), [0, false]);
  assert.deepStrictEqual(coerceInt('9007199254740992', false), [0, false]);
});

test('custom integer parsers enforce scalar-specific bounds', () => {
  const schema = parseSchemaIR(sampleIR);
  const moneyField = schema.types.UserBase.fields.find((field) => field.name === 'money');
  assert.strictEqual(moneyField.typeRef.name, 'Finance_Money');
  assert.strictEqual(schema.scalars.Finance_Money.hasCustomParse, true);
  const result = parseType(schema, 'UserBase', { money: -1 });

  assert.ok(result.errors.money);
  assert.strictEqual(result.errors.money[0].validator, 'parse');
});

test('rejects non-object input', () => {
  assert.throws(
    () => parseSchemaIR('not an object'),
    /expected object at root/,
  );
});


// Field-level title / x-placeholder metadata.
const fs = require('node:fs');
const path = require('node:path');
const { parseSchema } = require('../dist/runtime/index.js');
const { writeSchemaJson } = require('../dist/runtime/index.js');

const legacyMetadataSchema = JSON.parse(
  fs.readFileSync(path.join(__dirname, '../../testdata/fielddef_legacy.json'), 'utf8')
);

function metadataFields(schema) {
  const input = schema.inputs.ConnectorAuthInput;
  assert.ok(input, 'ConnectorAuthInput parsed');
  const byName = {};
  for (const field of input.fields) byName[field.name] = field;
  return byName;
}

test('parseSchema picks up field title and x-placeholder', () => {
  const schema = parseSchema(legacyMetadataSchema);
  const fields = metadataFields(schema);
  assert.strictEqual(fields.client_domain.title, 'My Domain name');
  assert.strictEqual(fields.client_domain.placeholder, 'acme');
  assert.strictEqual(fields.client_domain.validatePattern, '^[A-Za-z0-9][A-Za-z0-9-]*$');
});

test('writeSchemaJson round-trips title and x-placeholder to legacy keys', () => {
  const schema = parseSchema(legacyMetadataSchema);
  const written = JSON.parse(writeSchemaJson(schema));
  const prop = written.definitions.ConnectorAuthInput.properties.client_domain;
  assert.strictEqual(prop.title, 'My Domain name');
  assert.strictEqual(prop['x-placeholder'], 'acme');
  assert.strictEqual(prop['x-validatePattern'], '^[A-Za-z0-9][A-Za-z0-9-]*$');
});

test('writeSchemaJson omits title and x-placeholder when unset', () => {
  const schema = parseSchema(legacyMetadataSchema);
  const written = JSON.parse(writeSchemaJson(schema));
  const prop = written.definitions.ConnectorAuthInput.properties.clientSecret;
  assert.ok(!('title' in prop), 'no title key on metadata-less field');
  assert.ok(!('x-placeholder' in prop), 'no x-placeholder key on metadata-less field');
});

test('parseSchemaIR picks up wire-form title and placeholder (shared Go fixture)', () => {
  const fixturePath = path.join(__dirname, '../../testdata/fielddef_wire.json');
  const wire = JSON.parse(fs.readFileSync(fixturePath, 'utf8'));
  const schema = parseSchemaIR(wire);
  const fields = metadataFields(schema);
  assert.strictEqual(fields.client_domain.title, 'My Domain name');
  assert.strictEqual(fields.client_domain.placeholder, 'acme');
  assert.strictEqual(fields.client_domain.validatePattern, '^[A-Za-z0-9][A-Za-z0-9-]*$');
  assert.strictEqual(fields.clientSecret.title, undefined);
  assert.strictEqual(fields.clientSecret.placeholder, undefined);
});


test('legacy parseSchema rejects x-validateScalar loudly', () => {
  assert.throws(
    () =>
      parseSchema({
        'x-rootType': 'ConnectorAuthInput',
        definitions: {
          ConnectorAuthInput: {
            type: 'object',
            'x-kind': 'input',
            properties: {
              subdomain: {
                $ref: '#/definitions/scalars/Network.DnsLabel',
                'x-validateScalar': true,
              },
            },
          },
        },
      }),
    /x-validateScalar requires the wire schema form/,
  );
});
test('x-temporal-format survives the legacy JSON reader/writer round trip', () => {
  const schema = parseSchema({
    name: 'epoch-format',
    'x-rootType': 'Record',
    'x-platformSchemaVersion': '1',
    definitions: {
      Record: {
        type: 'object',
        'x-kind': 'type',
        properties: {
          event_at: {
            $ref: '#/definitions/scalars/Temporal.DateTime',
            'x-semantic-role': 'event_time',
            'x-temporal-format': 'unix',
          },
        },
      },
    },
  });
  const eventAt = schema.types.Record.fields.find((f) => f.name === 'event_at');
  assert.strictEqual(eventAt.temporalFormat, 'unix');

  const written = JSON.parse(writeSchemaJson(schema));
  assert.strictEqual(
    written.definitions.Record.properties.event_at['x-temporal-format'],
    'unix',
  );
});

if (failures > 0) {
  console.error(`${failures} test(s) failed`);
  process.exit(1);
}
console.log('ir-reader: all tests passed');
