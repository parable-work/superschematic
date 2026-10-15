// The corpus a second implementation of the engine replays (D58): the
// scenarios and HTTP exchanges the recorder (recorder.ts) writes from a
// curated set of this suite's files, committed under runtime/engine/testdata.
// This test records them again, under Node.js (corpus.ts), and fails when
// a committed file is not what the recorder writes; UPDATE_ENGINE_CORPUS=1
// rewrites them. Review the diff: a change in what the engine returns shows
// there. The work queue's files beside them (workqueue-*.json) are its
// suite's to check.
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

import { checkCorpus } from './corpus.ts';

/**
 * The test files whose scenarios the corpus holds: every core behavior's,
 * and the engine's own instances, events, namespaces, registry, unique
 * fields, filters, access, normalization and compatibility rule. The
 * behavior API's files define their behaviors in TypeScript, which the
 * TypeScript suite runs through the addon's bridge, and the tools files
 * are mostly describe documents; runtime/engine/testdata/scenarios/README.md
 * says what each leaves out and why.
 */
export const SCENARIO_FILES = [
  'access',
  'compat',
  'core-approvals',
  'core-behaviors',
  'core-branches',
  'core-branches-sweep',
  'core-comments',
  'core-constants',
  'core-dependencies',
  'core-links',
  'core-links-many',
  'core-numbering',
  'core-reactions',
  'core-reactions-changes',
  'core-reactions-holds-revised',
  'core-revisions',
  'core-rollups',
  'core-search',
  'core-search-vectors',
  'core-staterules',
  'core-timers',
  'core-variants',
  'core-workflow',
  'events',
  'instances',
  'list-filters',
  'namespaces',
  'normalize',
  'registry',
  'unique',
];

/** The test files whose exchanges the HTTP corpus holds: the routes, the behaviors' operations over HTTP, the event stream and MCP. */
export const HTTP_FILES = ['http', 'mcp', 'operations', 'stream'];

test('the committed scenario and HTTP corpora are what the recorder writes', { timeout: 600_000 }, () =>
  checkCorpus({ root: fileURLToPath(new URL('..', import.meta.url)), prefix: '', scenarios: SCENARIO_FILES, http: HTTP_FILES })
);
