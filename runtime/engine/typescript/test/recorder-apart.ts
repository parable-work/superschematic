// The tests recorder.test.ts records, with ENGINE_RECORD set, to hold the
// recorder to the scenarios README's rule that two readings of the wall
// clock are two instants: two engines on the wall clock, each served over
// HTTP, whose writes the test makes in turn, each pair as soon as a
// millisecond begins, so that without the recorder holding them apart the
// second engine's write would read the millisecond the first one's did.
// Not a test file of the suite: its name does not end in .test.ts, so
// neither Node.js's runner nor Bun's runs it alone.
import assert from 'node:assert/strict';
import { afterEach, test } from 'node:test';

import type { Authenticator } from '@superschematic/http-runtime';

import { engineApp } from '../dist/http/index.js';
import { alice, cleanup, openTestEngine, schemaDocument } from './helpers.ts';
import { servedApp } from './recorder.ts';

afterEach(cleanup);

const authenticate: Authenticator = async () => ({ subject: 'alice', permissions: ['*'] });

/** How many pairs of writes the test makes. */
const PAIRS = 40;

function served() {
  const engine = openTestEngine();
  engine.schemas.define(alice, schemaDocument('Note', [{ name: 'title', typeRef: { name: 'string' }, required: true }]));
  engine.schemas.publish(alice, 'Note');
  return servedApp(engineApp, engine, { authenticate });
}

async function create(app: ReturnType<typeof served>, id: string): Promise<void> {
  const response = await app.request('/namespaces/default/schemas/Note/instances', {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ id, data: { title: id } }),
  });
  assert.equal(response.status, 201, await response.text());
}

test('two engines on the wall clock, written in turn within a millisecond', async () => {
  const first = served();
  const second = served();
  for (let pair = 1; pair <= PAIRS; pair += 1) {
    const start = Date.now();
    while (Date.now() === start) {
      // The pair starts as a millisecond begins.
    }
    await create(second, `b${pair}`);
    await create(first, `a${pair}`);
  }
});
