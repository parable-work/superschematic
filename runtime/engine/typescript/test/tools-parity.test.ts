// The engine's JSON Schema of a schema's fields against the Go side's:
// runtime/engine/testdata/tool_parameters_parity.json, which
// internal/generator/toolsutil writes (TestEngineToolParametersParity) from
// what the SDK generators build for the same schema-file document. The
// document covers every scalar of the pinned catalog, enums, nested and
// recursive types, lists and lists of lists, field rules and the
// document's own scalars; the cases cover the core's vendor keys, a
// distribution's and none.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { afterEach, describe, test } from 'node:test';

import {
  DEFAULT_INVOCATION_POLICY,
  DEFAULT_TOOL_KEYS,
  MCP_HANDLE,
  MCP_HANDLE_MAX_LENGTH,
  kebabCase,
  resolveToolOptions,
  snakeCase,
  typeArguments,
  type DescribedOperation,
  type ToolKeys,
} from '../dist/index.js';
import { alice, cleanup, openTestEngine } from './helpers.ts';

afterEach(cleanup);

interface ParityCase {
  name: string;
  keys: ToolKeys;
  parameters: Record<string, unknown> & { properties: Record<string, unknown> };
  inputSchemaDigest: string;
}

interface ParityCorpus {
  document: Record<string, unknown>;
  instanceType: string;
  cases: ParityCase[];
  defaults: { keys: ToolKeys; invocationPolicy: { key: string; values: string[]; default: string } };
  keys: Record<string, string[]>;
  names: Array<{ input: string; kebab: string; snake: string }>;
  handles: Verdict[];
  policyKeys: Verdict[];
  policyValues: Verdict[];
}

interface Verdict {
  value: string;
  valid: boolean;
}

function accepts(options: Parameters<typeof resolveToolOptions>[0]): boolean {
  try {
    resolveToolOptions(options);
    return true;
  } catch (error) {
    assert.ok(error instanceof TypeError);
    return false;
  }
}

const corpus = JSON.parse(readFileSync(new URL('../../testdata/tool_parameters_parity.json', import.meta.url), 'utf8')) as ParityCorpus;

/** The argument schema without the keys a tool writes at its root. */
function withoutVendorKeys(parameters: Record<string, unknown>, keys: ToolKeys): Record<string, unknown> {
  const out = { ...parameters };
  for (const { key } of keys.parameters) {
    delete out[key];
  }
  return out;
}

describe('the Go vectors', () => {
  test('cover what the engine must match', () => {
    assert.equal(corpus.cases.length, 3);
    const properties = Object.values(corpus.cases[0].parameters.properties) as Array<{ type: unknown; items?: { items?: unknown } }>;
    assert.ok(properties.length > 70, 'the fixture has a field per catalog scalar');
    // Generic.JSON is any JSON value but null; an optional one also takes null.
    const anyJSON = (types: string[]) => properties.some((property) => JSON.stringify(property.type) === JSON.stringify(types));
    assert.ok(anyJSON(['object', 'array', 'string', 'number', 'boolean']), 'a required Generic.JSON');
    assert.ok(anyJSON(['object', 'array', 'string', 'number', 'boolean', 'null']), 'an optional Generic.JSON');
    assert.ok(properties.some((property) => property.items?.items !== undefined), 'a list of lists');
  });

  test("the engine's defaults are the Go defaults", () => {
    assert.deepEqual({ ...DEFAULT_TOOL_KEYS, parameters: [...DEFAULT_TOOL_KEYS.parameters] }, corpus.defaults.keys);
    assert.deepEqual(
      { key: DEFAULT_INVOCATION_POLICY.key, values: [...DEFAULT_INVOCATION_POLICY.values], default: DEFAULT_INVOCATION_POLICY.default },
      corpus.defaults.invocationPolicy
    );
  });
});

describe('names and the options the registry checks', () => {
  test("a tool's name and handle take the Go case conversions", () => {
    assert.ok(corpus.names.length > 0);
    for (const { input, kebab, snake } of corpus.names) {
      assert.deepEqual([kebabCase(input), snakeCase(input)], [kebab, snake], input);
    }
  });

  test('a derived handle is one @mcp would take exactly when Go says so', () => {
    for (const { value, valid } of corpus.handles) {
      assert.equal(MCP_HANDLE.test(value) && value.length <= MCP_HANDLE_MAX_LENGTH, valid, JSON.stringify(value));
    }
  });

  test('an invocation policy key and value are refused exactly when Go refuses them', () => {
    for (const { value, valid } of corpus.policyKeys) {
      assert.equal(accepts({ invocationPolicy: { key: value, values: ['auto'], default: 'auto' } }), valid, JSON.stringify(value));
    }
    for (const { value, valid } of corpus.policyValues) {
      assert.equal(accepts({ invocationPolicy: { key: 'review', values: [value], default: value } }), valid, JSON.stringify(value));
    }
  });
});

describe('typeArguments', () => {
  for (const parity of corpus.cases) {
    test(`writes the SDK generators' arguments and digest with ${parity.name}`, () => {
      const got = typeArguments(corpus.document as never, corpus.instanceType, parity.keys);
      assert.deepEqual(got.parameters, parity.parameters);
      assert.equal(got.inputSchemaDigest, parity.inputSchemaDigest);
    });
  }
});

describe('a published schema', () => {
  for (const parity of corpus.cases) {
    test(`describes its instance and takes create's data as the Go vectors do, with ${parity.name}`, () => {
      const engine = openTestEngine({ tools: { keys: parity.keys } });
      engine.schemas.define(alice, corpus.document);
      engine.schemas.publish(alice, 'Shelf');
      const want = withoutVendorKeys(parity.parameters, parity.keys);

      const described = engine.tools.describe(alice, 'Shelf');
      assert.deepEqual(described.instance, want);

      const create = described.operations.find((operation) => operation.name === 'create') as DescribedOperation;
      const params = create.params as { properties: { data: Record<string, unknown> } };
      const { description, ...data } = params.properties.data;
      assert.equal(description, 'Shelf object');
      assert.deepEqual(data, want);
      for (const { key, value } of parity.keys.parameters) {
        assert.deepEqual(create.params[key], value, `${key} sits at the root of the tool's arguments`);
      }

      // The tools document writes the same arguments, and the keys of
      // ir.ToolManifest and ir.ToolManifestTool in their order.
      const manifest = engine.tools.manifest(alice);
      assert.deepEqual(Object.keys(manifest), corpus.keys.ToolManifest);
      const tool = manifest.tools.find((candidate) => candidate.name === 'shelf.create');
      assert.ok(tool);
      assert.deepEqual(tool.parameters, create.params);
      for (const entry of manifest.tools) {
        assert.deepEqual(Object.keys(entry), corpus.keys.ToolManifestTool, entry.name);
        assert.deepEqual(Object.keys(entry.guidance), corpus.keys.ToolOperationGuidance);
        if (entry.replay) {
          assert.deepEqual(Object.keys(entry.replay), corpus.keys.ToolReplayContract);
        }
        const returnKeys = Object.keys(entry.returns);
        assert.deepEqual(returnKeys, corpus.keys.ToolReturnSchema.filter((key) => returnKeys.includes(key)), entry.name);
      }
    });
  }
});
