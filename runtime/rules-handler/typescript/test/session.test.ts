// rules-dev's store session (rules-dev/tests/store.rs's
// the_same_requests_answer_alike_over_memory_and_over_sqlite), each request
// with the context it ran with there, through this host over bun:sqlite in
// memory and in a file: every answer is the one
// runtime/engine/testdata/rules-dev/session.json records, which
// superschematic-rules-handler-wasm's tests/session.rs holds to the Rust
// host's answers over memory.
import { afterAll, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { serve, type Caller, type HandlerRequest, type Store } from '../dist/index.js';
import { checkout, handler, scratch, sqlite } from './support.ts';

interface Session {
  caller: Caller;
  now: number;
  steps: { request: HandlerRequest; context: { request: string; id: string }; answer: unknown }[];
}

const session = JSON.parse(readFileSync(join(checkout, 'runtime/engine/testdata/rules-dev/session.json'), 'utf8')) as Session;
const files = scratch('session');
afterAll(files.remove);

function run(store: Store): unknown[] {
  return session.steps.map(step => {
    const outcome = serve(handler, store, step.request, { caller: session.caller, now: session.now, ...step.context });
    return outcome.ok ? { ok: outcome.response } : { refused: outcome.refusal };
  });
}

test('the session is the one rules-dev runs', () => {
  expect(session.steps.length).toBeGreaterThanOrEqual(26);
  const refused = session.steps.flatMap((step, n) => {
    const answer = step.answer as { refused?: { code: string } };
    return answer.refused === undefined ? [] : [[n, answer.refused.code]];
  });
  expect(refused).toEqual([
    [5, 'conflict'],
    [9, 'invalid_instance'],
    [13, 'forbidden'],
    [20, 'not_found'],
    [22, 'conflict'],
    [25, 'not_found'],
    [38, 'invalid_instance'],
  ]);
});

for (const [name, open] of [
  ['bun:sqlite in memory', () => sqlite()],
  ['a bun:sqlite file', () => sqlite(join(files.dir, 'session.db'))],
] as const) {
  test(`the same requests answer as the Rust host's over ${name}`, () => {
    const answers = run(open());
    answers.forEach((answer, n) => {
      expect({ n, answer }).toEqual({ n, answer: session.steps[n]!.answer });
    });
  });
}
