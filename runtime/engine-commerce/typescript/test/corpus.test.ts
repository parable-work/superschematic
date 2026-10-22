// The commerce part of the corpus a second implementation of the engine and
// these behaviors replays (D58): the scenarios the engine's recorder
// writes from this suite's files, committed beside the engine's under
// runtime/engine/testdata/scenarios as commerce-<file>.json. This test
// records them again, under Node.js, through the engine's corpus check
// (runtime/engine/typescript/test/corpus.ts), and fails when a committed
// file is not what the recorder writes; UPDATE_ENGINE_CORPUS=1 rewrites
// them. Review the diff: a change in what a behavior returns shows there.
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

import { checkCorpus } from '../../../engine/typescript/test/corpus.ts';

/** The test files whose scenarios the corpus holds. */
export const SCENARIO_FILES = ['calendar', 'holds', 'holds-timers', 'stock'];

test('the committed commerce scenarios are what the recorder writes', { timeout: 600_000 }, () =>
  checkCorpus({ root: fileURLToPath(new URL('..', import.meta.url)), prefix: 'commerce-', scenarios: SCENARIO_FILES, http: [] })
);
