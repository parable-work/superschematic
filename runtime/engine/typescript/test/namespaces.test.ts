// Namespaces: independent version lines, the lookup through the shared
// namespace, and a name that lives on one side of that lookup only.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { EngineError, Namespaces } from '../dist/index.js';
import { cleanup, drivers, openTestEngine, orderDocument, schemaDocument, thrown } from './helpers.ts';

afterEach(cleanup);

const noteDocument = schemaDocument('Note', [{ name: 'body', typeRef: { name: 'string' }, required: true }]);

test('there is one namespace, default, unless more are configured', () => {
  assert.deepEqual(new Namespaces().names, ['default']);
  const namespaces = new Namespaces({ names: ['east', 'west', 'east'] });
  assert.deepEqual(namespaces.names, ['default', 'east', 'west']);
  assert.equal(namespaces.shared, undefined);
  assert.equal(namespaces.resolve(undefined), 'default');
  assert.deepEqual(namespaces.lookup('east'), ['east']);
});

test('namespace names are checked, and the shared namespace must be one of them', () => {
  for (const name of ['East', '1east', 'east_side', '', 'e'.repeat(64)]) {
    assert.throws(() => new Namespaces({ names: [name] }), /must match/);
  }
  assert.throws(() => new Namespaces({ names: ['east'], shared: 'common' }), /the shared namespace "common" is not one of the namespaces/);
  assert.deepEqual(new Namespaces({ shared: 'default' }).lookup('default'), ['default']);
});

for (const driver of drivers) {
  describe(`namespaces (${driver})`, () => {
    test('a call to a namespace that is not configured is unknown_namespace', () => {
      const engine = openTestEngine({ driver });
      const error = thrown(() => engine.schemas.define(orderDocument(), { namespace: 'east' }), EngineError);
      assert.equal(error.code, 'unknown_namespace');
      assert.equal(thrown(() => engine.schemas.list({ namespace: 'east' }), EngineError).code, 'unknown_namespace');
    });

    test('the same name publishes in two namespaces without sharing versions', () => {
      const engine = openTestEngine({ driver, namespaces: { names: ['east', 'west'] } });
      ['east', 'west', 'east'].forEach((namespace, index) => {
        engine.schemas.define({ ...orderDocument(), description: `release ${index}` }, { namespace });
        engine.schemas.publish('Order', { namespace });
      });
      assert.equal(engine.schemas.live('Order', { namespace: 'east' })?.version, 2);
      assert.equal(engine.schemas.live('Order', { namespace: 'west' })?.version, 1);
      assert.equal(engine.schemas.live('Order', { namespace: 'west' })?.namespace, 'west');
      assert.equal(engine.schemas.live('Order'), undefined);
      assert.deepEqual(engine.schemas.list({ namespace: 'west' }), [
        { namespace: 'west', name: 'Order', liveVersion: 1, hasDraft: false },
      ]);
    });

    describe('with a shared namespace', () => {
      const options = { driver, namespaces: { names: ['east', 'west', 'common'], shared: 'common' } };

      test('a namespace reaches its own schemas, then the shared ones', () => {
        const engine = openTestEngine(options);
        engine.schemas.define(noteDocument, { namespace: 'common' });
        engine.schemas.publish('Note', { namespace: 'common' });
        engine.schemas.define(orderDocument(), { namespace: 'east' });
        engine.schemas.publish('Order', { namespace: 'east' });

        const note = engine.schemas.live('Note', { namespace: 'east' });
        assert.equal(note?.namespace, 'common');
        assert.equal(note?.version, 1);
        assert.deepEqual(engine.schemas.validate('Note', { body: 'hello' }, { namespace: 'east' }), []);
        assert.deepEqual(engine.schemas.list({ namespace: 'east' }), [
          { namespace: 'common', name: 'Note', liveVersion: 1, hasDraft: false },
          { namespace: 'east', name: 'Order', liveVersion: 1, hasDraft: false },
        ]);
        // The shared namespace does not reach the others.
        assert.equal(engine.schemas.live('Order', { namespace: 'common' }), undefined);
        assert.deepEqual(
          engine.schemas.list({ namespace: 'common' }).map((summary) => summary.name),
          ['Note']
        );
      });

      test('a namespace cannot define a name the shared namespace holds', () => {
        const engine = openTestEngine(options);
        engine.schemas.define(noteDocument, { namespace: 'common' });
        const error = thrown(() => engine.schemas.define(noteDocument, { namespace: 'east' }), EngineError);
        assert.equal(error.code, 'name_taken');
        assert.match(error.message, /schema Note is defined in the shared namespace common, which namespace east looks names up in; use another name/);
        assert.equal(engine.schemas.draft('Note', { namespace: 'east' })?.namespace, 'common');
      });

      test('the shared namespace cannot define a name another namespace holds', () => {
        const engine = openTestEngine(options);
        engine.schemas.define(noteDocument, { namespace: 'west' });
        const error = thrown(() => engine.schemas.define(noteDocument, { namespace: 'common' }), EngineError);
        assert.equal(error.code, 'name_taken');
        assert.match(error.message, /schema Note is defined in namespace west, which looks names up in the shared namespace common/);
      });

      test('the default namespace also looks names up in the shared one', () => {
        const engine = openTestEngine(options);
        engine.schemas.define(noteDocument, { namespace: 'common' });
        engine.schemas.publish('Note', { namespace: 'common' });
        assert.equal(engine.schemas.live('Note')?.namespace, 'common');
      });
    });
  });
}
