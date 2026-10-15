// The work queue's part of the corpus a second implementation of the engine
// and these behaviors replays (D58): the scenarios the engine's recorder
// writes from a curated set of this suite's files, committed beside the
// engine's under runtime/engine/testdata/scenarios as workqueue-<file>.json.
// This test records them again, under Node.js, through the engine's corpus
// check (runtime/engine/typescript/test/corpus.ts), and fails when a
// committed file is not what the recorder writes; UPDATE_ENGINE_CORPUS=1
// rewrites them. Review the diff: a change in what a behavior returns shows
// there.
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

import { checkCorpus } from '../../../engine/typescript/test/corpus.ts';

/**
 * The test files whose scenarios the corpus holds: every one that opens an
 * engine through helpers.ts but guidance, whose tests read the describe
 * and tools documents, each scenario past what the recorder writes.
 * worker.test.ts records nothing, and sqlite-corpus.test.ts writes the
 * SQLite corpus; runtime/engine/testdata/scenarios/README.md says why.
 */
export const SCENARIO_FILES = [
  'assignment',
  'blueprint',
  'budget',
  'filters',
  'lease',
  'lease-approvals',
  'lease-branches',
  'lease-direct-on',
  'lease-numbering',
  'lease-timers',
  'package',
  'presence',
  'queue',
  'queue-exclusion',
  'retries',
];

test('the committed work-queue scenarios are what the recorder writes', { timeout: 600_000 }, () =>
  checkCorpus({ root: fileURLToPath(new URL('..', import.meta.url)), prefix: 'workqueue-', scenarios: SCENARIO_FILES, http: [] })
);
