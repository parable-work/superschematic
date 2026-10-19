// acme.Rating in the engine, the smoke's steps 20 and 21: an engine with
// acme.Rating defines and publishes shop-ratings' Product, creates one,
// rates it and reads the rating fields; its tools carry acme's invocation
// policy; an engine without it refuses the schema. ACME_META_SCHEMA is the
// `acme-schematic json-schema` output, which declares acme.Rating and acme's
// invocation policy, as a deployment's binary would.
//
// By default the engine is the TypeScript one, with acme's implementation
// in this package (step 20). With SUPERSCHEMATIC_ENGINE_ADDON naming the
// addon of the engine crate acme's binary writes for shop-ratings (its
// napi/ member, step 21), the same cases run on the Rust engine, opened as
// a TypeScript server opens it (openEngineAsync) on that addon, whose
// acme.Rating is the acme-behaviors crate's (crates/behaviors): the
// engine's behaviors name it, and every call is awaited.
import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, test } from 'node:test';

import { EngineError, OperationParamsError, SchemaDocumentError, allowAll, openEngine, openEngineAsync, type Engine } from '@superschematic/engine';

import { behaviors, tools } from '../src/index.ts';

const metaSchemaPath = process.env.ACME_META_SCHEMA;
if (!metaSchemaPath) {
  throw new Error('ACME_META_SCHEMA must name the acme-schematic json-schema output; scripts/smoke.sh sets it');
}
const metaSchema = readFileSync(metaSchemaPath, 'utf8');

// shop-ratings' Product, as the engine takes a schema: one General document
// with a name.
const product = JSON.parse(readFileSync(new URL('../../../ext/testdata/services/shop-ratings/src/product.schema.json', import.meta.url), 'utf8'));
const schema = { kind: 'General', name: 'Product', ...product };

// The project's addon, when the cases run on the Rust engine.
const addon = process.env.SUPERSCHEMATIC_ENGINE_ADDON || undefined;

const shopper = { subject: 'shopper', permissions: [] };
const directories: string[] = [];
const engines: Engine[] = [];

afterEach(() => {
  for (const engine of engines.splice(0)) {
    engine.close();
  }
  for (const directory of directories.splice(0)) {
    rmSync(directory, { recursive: true, force: true });
  }
});

// open opens the TypeScript engine with acme's implementation, or with an
// addon the Rust engine on it, its calls promises the cases await: typed as
// the TypeScript engine, which they are the promises of.
async function open(withAcme: boolean): Promise<Engine> {
  const directory = mkdtempSync(join(tmpdir(), 'acme-engine-'));
  directories.push(directory);
  const path = join(directory, 'shop.db');
  const engine =
    addon === undefined
      ? openEngine({ path, policy: allowAll, metaSchema, behaviors: withAcme ? behaviors : [], tools })
      : ((await openEngineAsync({ path, policy: allowAll, metaSchema, behaviors: withAcme ? ['acme.Rating'] : [], tools })) as unknown as Engine);
  engines.push(engine);
  return engine;
}

test('an engine with acme.Rating publishes shop-ratings and rates a product', async () => {
  const engine = await open(true);
  await engine.schemas.define(shopper, schema);
  assert.deepEqual(await engine.schemas.publish(shopper, 'Product'), { namespace: 'default', name: 'Product', version: 1, published: true });
  assert.deepEqual((await engine.schemas.behaviors(shopper, 'Product')).map(({ name, config }) => ({ name, config })), [
    { name: 'acme.Rating', config: { maxStars: 5 } },
  ]);

  const created = await engine.instances.create(shopper, 'Product', { sku: 'walnut-desk', name: 'Walnut desk' }, { id: 'p1' });
  assert.deepEqual(created.data, { sku: 'walnut-desk', name: 'Walnut desk' });
  assert.deepEqual(created.behaviors, { 'acme.Rating': { ratingCount: 0, ratingAverage: 0 } });

  assert.deepEqual(await engine.instances.invoke(shopper, 'Product', 'p1', 'rate', { stars: 4 }), { ratingCount: 1, ratingAverage: 4 });
  assert.deepEqual(await engine.instances.invoke(shopper, 'Product', 'p1', 'rate', { stars: 5 }), { ratingCount: 2, ratingAverage: 4.5 });
  assert.deepEqual((await engine.instances.get(shopper, 'Product', 'p1'))?.behaviors, { 'acme.Rating': { ratingCount: 2, ratingAverage: 4.5 } });
  assert.deepEqual(await engine.instances.invoke(shopper, 'Product', 'p1', 'ratingSummary'), { ratingCount: 2, ratingAverage: 4.5 });

  // Above the type's maxStars, below the declaration's minimum, and the
  // fields themselves, which an update's own fields cannot name: each is
  // refused and changes nothing.
  assert.equal((await thrown(() => engine.instances.invoke(shopper, 'Product', 'p1', 'rate', { stars: 6 }))).code, 'invalid_argument');
  assert.ok((await thrown(() => engine.instances.invoke(shopper, 'Product', 'p1', 'rate', { stars: 0 }))) instanceof OperationParamsError);
  assert.equal((await thrown(() => engine.instances.update(shopper, 'Product', 'p1', { ratingCount: 9 }))).code, 'invalid_instance');
  assert.equal((await engine.instances.get(shopper, 'Product', 'p1'))?.behaviors['acme.Rating'].ratingCount, 2);

  const events = (await engine.events.read(shopper, { schema: 'Product', instanceId: 'p1' })).events;
  assert.deepEqual(
    events.map((event) => [event.kind, event.change]),
    [
      ['create', { data: { sku: 'walnut-desk', name: 'Walnut desk' }, behaviors: { 'acme.Rating': { ratingCount: 0, ratingAverage: 0 } } }],
      [
        'operation',
        { behavior: 'acme.Rating', operation: 'rate', params: { stars: 4 }, patch: { behaviors: { 'acme.Rating': { ratingCount: 1, ratingAverage: 4 } } } },
      ],
      [
        'operation',
        { behavior: 'acme.Rating', operation: 'rate', params: { stars: 5 }, patch: { behaviors: { 'acme.Rating': { ratingCount: 2, ratingAverage: 4.5 } } } },
      ],
    ]
  );
});

test("the engine's tools carry the invocation policy acme's binary registers", async () => {
  // The meta-schema's mcp record has one key beside its own: the policy's,
  // with its values and default.
  const record = (JSON.parse(metaSchema) as { $defs: { OperationMCP: { properties: Record<string, { enum?: string[]; default?: string }> } } }).$defs.OperationMCP
    .properties;
  const own = ['_meta', 'description', 'handle', 'hidden', 'hiddenReason', 'icon', 'name'];
  const [key] = Object.keys(record).filter((name) => !own.includes(name));
  assert.deepEqual(tools.invocationPolicy, { key, values: record[key].enum, default: record[key].default });

  const engine = await open(true);
  await engine.schemas.define(shopper, schema);
  await engine.schemas.publish(shopper, 'Product');
  const policies = (await engine.tools.manifest(shopper)).tools
    .filter((tool) => tool.name.startsWith('product.'))
    .map((tool) => [tool.name, tool.mcp.hidden ? undefined : tool.mcp.confirm]);
  assert.deepEqual(policies, [
    ['product.create', 'never'],
    ['product.get', 'never'],
    ['product.list', 'never'],
    ['product.update', 'never'],
    ['product.delete', 'never'],
    ['product.rate', 'always'],
    ['product.ratingSummary', 'never'],
  ]);
  const rate = (await engine.tools.manifest(shopper)).tools.find((tool) => tool.name === 'product.rate');
  assert.equal(rate?.parameters['x-acme-arguments'], 1);
});

test('a new version may raise maxStars and may not lower it', async () => {
  const engine = await open(true);
  await engine.schemas.define(shopper, schema);
  await engine.schemas.publish(shopper, 'Product');
  const withMax = (maxStars: number) => ({ ...schema, types: { Product: { ...product.types.Product, behaviors: [{ name: 'acme.Rating', config: { maxStars } }] } } });
  await engine.schemas.define(shopper, withMax(10));
  assert.equal((await engine.schemas.publish(shopper, 'Product')).version, 2);
  const lowered = await thrown(() => engine.schemas.define(shopper, withMax(3)));
  assert.equal(lowered.code, 'incompatible_change');
  assert.match(lowered.message, /maxStars cannot fall from 10 to 3/);
});

test('an engine without acme.Rating refuses the schema', async () => {
  const engine = await open(false);
  const refused = await thrown(() => engine.schemas.define(shopper, schema));
  assert.ok(refused instanceof SchemaDocumentError);
  assert.deepEqual(refused.issues, [
    { path: '/types/Product/behaviors/0', message: 'behavior acme.Rating on type Product: no implementation registered' },
  ]);
  assert.deepEqual(await engine.schemas.list(shopper), []);
});

// thrown is the EngineError fn throws, or the one its promise rejects with.
async function thrown(fn: () => unknown): Promise<EngineError> {
  try {
    await fn();
  } catch (error) {
    assert.ok(error instanceof EngineError, `expected an EngineError, got ${String(error)}`);
    return error;
  }
  assert.fail('expected an EngineError');
}
