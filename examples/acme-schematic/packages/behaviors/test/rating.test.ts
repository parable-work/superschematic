// acme.Rating in the engine, the smoke's step 20: an engine with acme's
// implementation defines and publishes shop-ratings' Product, creates one,
// rates it and reads the rating fields; an engine without it refuses the
// schema. ACME_META_SCHEMA is the `acme-schematic json-schema` output, which
// declares acme.Rating, as a deployment's binary would.
import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, test } from 'node:test';

import { EngineError, OperationParamsError, SchemaDocumentError, allowAll, openEngine, type Engine } from '@superschematic/engine';

import { behaviors } from '../src/index.ts';

const metaSchemaPath = process.env.ACME_META_SCHEMA;
if (!metaSchemaPath) {
  throw new Error('ACME_META_SCHEMA must name the acme-schematic json-schema output; scripts/smoke.sh sets it');
}
const metaSchema = readFileSync(metaSchemaPath, 'utf8');

// shop-ratings' Product, as the engine takes a schema: one General document
// with a name.
const product = JSON.parse(readFileSync(new URL('../../../ext/testdata/services/shop-ratings/src/product.schema.json', import.meta.url), 'utf8'));
const schema = { kind: 'General', name: 'Product', ...product };

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

function open(withAcme: boolean): Engine {
  const directory = mkdtempSync(join(tmpdir(), 'acme-engine-'));
  directories.push(directory);
  const engine = openEngine({ path: join(directory, 'shop.db'), policy: allowAll, metaSchema, behaviors: withAcme ? behaviors : [] });
  engines.push(engine);
  return engine;
}

test('an engine with acme.Rating publishes shop-ratings and rates a product', () => {
  const engine = open(true);
  engine.schemas.define(shopper, schema);
  assert.deepEqual(engine.schemas.publish(shopper, 'Product'), { namespace: 'default', name: 'Product', version: 1, published: true });
  assert.deepEqual(engine.schemas.behaviors(shopper, 'Product').map(({ name, config }) => ({ name, config })), [
    { name: 'acme.Rating', config: { maxStars: 5 } },
  ]);

  const created = engine.instances.create(shopper, 'Product', { sku: 'walnut-desk', name: 'Walnut desk' }, { id: 'p1' });
  assert.deepEqual(created.data, { sku: 'walnut-desk', name: 'Walnut desk', ratingCount: 0, ratingAverage: 0 });

  assert.deepEqual(engine.instances.invoke(shopper, 'Product', 'p1', 'rate', { stars: 4 }), { ratingCount: 1, ratingAverage: 4 });
  assert.deepEqual(engine.instances.invoke(shopper, 'Product', 'p1', 'rate', { stars: 5 }), { ratingCount: 2, ratingAverage: 4.5 });
  assert.deepEqual(engine.instances.get(shopper, 'Product', 'p1')?.data, { sku: 'walnut-desk', name: 'Walnut desk', ratingCount: 2, ratingAverage: 4.5 });
  assert.deepEqual(engine.instances.invoke(shopper, 'Product', 'p1', 'ratingSummary'), { ratingCount: 2, ratingAverage: 4.5 });

  // Above the type's maxStars, below the declaration's minimum, and the
  // fields themselves: each is refused and changes nothing.
  assert.equal(thrown(() => engine.instances.invoke(shopper, 'Product', 'p1', 'rate', { stars: 6 })).code, 'invalid_argument');
  assert.ok(thrown(() => engine.instances.invoke(shopper, 'Product', 'p1', 'rate', { stars: 0 })) instanceof OperationParamsError);
  assert.equal(thrown(() => engine.instances.update(shopper, 'Product', 'p1', { ratingCount: 9 })).code, 'invalid_instance');
  assert.equal(engine.instances.get(shopper, 'Product', 'p1')?.data.ratingCount, 2);

  const events = engine.events.read(shopper, { schema: 'Product', instanceId: 'p1' }).events;
  assert.deepEqual(
    events.map((event) => [event.kind, event.change]),
    [
      ['create', { sku: 'walnut-desk', name: 'Walnut desk', ratingCount: 0, ratingAverage: 0 }],
      ['operation', { behavior: 'acme.Rating', operation: 'rate', params: { stars: 4 }, patch: { ratingCount: 1, ratingAverage: 4 } }],
      ['operation', { behavior: 'acme.Rating', operation: 'rate', params: { stars: 5 }, patch: { ratingCount: 2, ratingAverage: 4.5 } }],
    ]
  );
});

test('a new version may raise maxStars and may not lower it', () => {
  const engine = open(true);
  engine.schemas.define(shopper, schema);
  engine.schemas.publish(shopper, 'Product');
  const withMax = (maxStars: number) => ({ ...schema, types: { Product: { ...product.types.Product, behaviors: [{ name: 'acme.Rating', config: { maxStars } }] } } });
  engine.schemas.define(shopper, withMax(10));
  assert.equal(engine.schemas.publish(shopper, 'Product').version, 2);
  const lowered = thrown(() => engine.schemas.define(shopper, withMax(3)));
  assert.equal(lowered.code, 'incompatible_change');
  assert.match(lowered.message, /maxStars cannot fall from 10 to 3/);
});

test('an engine without acme.Rating refuses the schema', () => {
  const engine = open(false);
  const refused = thrown(() => engine.schemas.define(shopper, schema));
  assert.ok(refused instanceof SchemaDocumentError);
  assert.deepEqual(refused.issues, [
    { path: '/types/Product/behaviors/0', message: 'behavior acme.Rating on type Product: no implementation registered' },
  ]);
  assert.deepEqual(engine.schemas.list(shopper), []);
});

function thrown(fn: () => unknown): EngineError {
  try {
    fn();
  } catch (error) {
    assert.ok(error instanceof EngineError, `expected an EngineError, got ${String(error)}`);
    return error;
  }
  assert.fail('expected an EngineError');
}
