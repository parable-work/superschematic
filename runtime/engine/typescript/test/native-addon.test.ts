// Which Node-API addon opens an engine (D58, src/native/addon.ts): the file
// the engine option addon names, else the one SUPERSCHEMATIC_ENGINE_ADDON
// names, else the core's. A project's engine crate builds an addon of its
// own over its behaviors (scripts/engine-napi.sh --crate), which a host
// names so; here a copy of the checkout's addon in another directory
// stands for one. A named file that does not load is refused, with no
// other addon tried in its place.
import assert from 'node:assert/strict';
import { copyFileSync, mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { after, afterEach, describe, test } from 'node:test';

import { allowAll, openEngine, openEngineAsync, type AsyncEngine, type Engine } from '../dist/index.js';
import { NATIVE_ADDON_ENV, NATIVE_BINARY_NAME, loadAddon, nativeTriple } from '../dist/native/addon.js';
import { alice, cleanup, freshPath, orderDocument } from './helpers.ts';

const opened: Array<Engine | AsyncEngine> = [];
const directory = mkdtempSync(join(tmpdir(), 'engine-addon-'));

afterEach((context) => {
  for (const engine of opened.splice(0)) {
    engine.close();
  }
  delete process.env[NATIVE_ADDON_ENV];
  cleanup(context);
});

after(() => {
  rmSync(directory, { recursive: true, force: true });
});

// A project's addon, as a host names it: a copy of the checkout's, under
// another name in another directory. Each copy is a new file: on Linux a
// copy over one the process has loaded truncates its mapping, relocations
// and all, and the next call into it jumps to an unrelocated address.
let copies = 0;
function projectAddon(): string {
  const checkout = fileURLToPath(new URL(`../native/${NATIVE_BINARY_NAME}.${nativeTriple()}.node`, import.meta.url));
  const copy = join(directory, `project-${copies++}-${process.pid}.node`);
  copyFileSync(checkout, copy);
  return copy;
}

describe('the addon an engine opens on', () => {
  test('is the file the engine option addon names, synchronous and asynchronous', async () => {
    const addon = projectAddon();
    const module = loadAddon(addon);
    assert.equal(module.project?.(), 'superschematic-engine-addon');
    assert.notEqual(module, loadAddon(), 'a named file is loaded on its own, beside the core addon');
    assert.equal(loadAddon(addon), module, 'a file loads once');

    const engine = openEngine({ path: freshPath(), policy: allowAll, addon });
    opened.push(engine);
    engine.schemas.define(alice, orderDocument());
    engine.schemas.publish(alice, 'Order');
    assert.equal((engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' }) as { id: string }).id, 'o1');
    assert.deepEqual(engine.routes(), JSON.parse(module.engineRoutes()));

    const asynchronous = await openEngineAsync({ path: freshPath(), policy: allowAll, addon });
    opened.push(asynchronous);
    await asynchronous.schemas.define(alice, orderDocument());
    assert.deepEqual(await asynchronous.schemas.publish(alice, 'Order'), { namespace: 'default', name: 'Order', version: 1, published: true });
  });

  test(`is the file ${NATIVE_ADDON_ENV} names, for an engine that names none`, () => {
    const addon = projectAddon();
    process.env[NATIVE_ADDON_ENV] = addon;
    assert.equal(loadAddon(), loadAddon(addon));
    const engine = openEngine({ path: freshPath(), policy: allowAll });
    opened.push(engine);
    assert.ok(engine.behaviors.has('Workflow'));
  });

  test('a named file that does not load is refused, and no other addon is tried', async () => {
    const missing = join(directory, 'missing.node');
    assert.throws(() => openEngine({ path: freshPath(), policy: allowAll, addon: missing }), (error: Error) => {
      assert.match(error.message, /cannot load the Node-API addon .*missing\.node, which the engine option addon names/u);
      return true;
    });
    await assert.rejects(openEngineAsync({ path: freshPath(), policy: allowAll, addon: missing }), /missing\.node/u);
    process.env[NATIVE_ADDON_ENV] = missing;
    assert.throws(() => loadAddon(), new RegExp(`missing\\.node, which ${NATIVE_ADDON_ENV} names`, 'u'));
    assert.throws(() => openEngine({ path: freshPath(), policy: allowAll, addon: '' }), TypeError);
  });

  test('a name the addon is not built with is refused, naming what it has', () => {
    assert.throws(
      () => openEngine({ path: freshPath(), policy: allowAll, behaviors: ['acme.Rating'] }),
      (error: Error) => {
        assert.equal(error.name, 'TypeError');
        assert.match(error.message, /superschematic-engine-addon is built with beside the core's \(Lease, Assignment, Queue, Presence, Blueprint, Budget, Retries, Stock, Calendar, Holds, Payments\)/u);
        assert.match(error.message, /not "acme\.Rating"/u);
        return true;
      }
    );
    assert.throws(() => openEngine({ path: freshPath(), policy: allowAll, behaviors: ['Workflow'] }), /not "Workflow"/u);
  });
});
