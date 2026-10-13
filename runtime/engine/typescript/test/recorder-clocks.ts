// The tests recorder.test.ts records, with ENGINE_RECORD set, to hold the
// recorder to the scenarios README's rule for times: an engine on a test's
// fixed clock of 7, whose instance's seq reaches 7, beside an engine on the
// wall clock; and a fixed clock that reads today beside the wall clock,
// which the recorder refuses. Not a test file of the suite: its name does
// not end in .test.ts, so neither Node.js's runner nor Bun's runs it alone.
import assert from 'node:assert/strict';
import { afterEach, test } from 'node:test';

import type { Engine } from '../dist/index.js';
import { alice, cleanup, openTestEngine, schemaDocument } from './helpers.ts';

afterEach(cleanup);

function opened(clock?: () => number): Engine {
  const engine = openTestEngine(clock === undefined ? {} : { clock });
  engine.schemas.define(alice, schemaDocument('Note', [{ name: 'title', typeRef: { name: 'string' }, required: true }]));
  engine.schemas.publish(alice, 'Note');
  return engine;
}

test('a fixed clock of 7 beside the wall clock', () => {
  const fixed = opened(() => 7);
  fixed.instances.create(alice, 'Note', { title: 'one' }, { id: 'n1' });
  for (const title of ['two', 'three', 'four', 'five', 'six', 'seven']) {
    fixed.instances.update(alice, 'Note', 'n1', { title });
  }
  const note = fixed.instances.get(alice, 'Note', 'n1');
  assert.deepEqual([note?.seq, note?.createdAt, note?.updatedAt], [7, 7, 7]);

  const wall = opened();
  wall.instances.create(alice, 'Note', { title: 'one' }, { id: 'n1' });
});

test('a fixed clock that reads today beside the wall clock', () => {
  opened(() => Date.now()).instances.create(alice, 'Note', { title: 'one' }, { id: 'n1' });
  opened().instances.create(alice, 'Note', { title: 'one' }, { id: 'n1' });
});
