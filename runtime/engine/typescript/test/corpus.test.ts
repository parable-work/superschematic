// The corpus a second implementation of the engine replays (D58): the
// scenarios and HTTP exchanges the recorder (recorder.ts) writes from a
// curated set of this suite's files, committed under runtime/engine/testdata.
// This test records them again, under Node.js, and fails when a committed
// file is not what the recorder writes; UPDATE_ENGINE_CORPUS=1 rewrites
// them. Review the diff: a change in what the engine returns shows there.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { cpSync, existsSync, mkdtempSync, readFileSync, readdirSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

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

const root = fileURLToPath(new URL('..', import.meta.url));
const testdata = fileURLToPath(new URL('../../testdata/', import.meta.url));

// record runs the curated files under Node.js with ENGINE_RECORD set to a
// fresh directory and returns it.
async function record(): Promise<string> {
  const out = mkdtempSync(join(tmpdir(), 'superschematic-engine-corpus-'));
  const env: Record<string, string | undefined> = { ...process.env, ENGINE_RECORD: out };
  // A test runner's own variables would make the child report to this one.
  delete env.NODE_TEST_CONTEXT;
  delete env.UPDATE_ENGINE_CORPUS;
  const files = [...SCENARIO_FILES, ...HTTP_FILES].map((file) => `test/${file}.test.ts`);
  const output: string[] = [];
  const code = await new Promise<number | null>((resolve, reject) => {
    const child = spawn('node', ['--test', '--test-reporter=dot', ...files], { cwd: root, env, stdio: ['ignore', 'pipe', 'pipe'] });
    child.stdout.on('data', (chunk: Buffer) => output.push(chunk.toString()));
    child.stderr.on('data', (chunk: Buffer) => output.push(chunk.toString()));
    child.on('error', reject);
    child.on('close', resolve);
  });
  assert.equal(code, 0, `recording the corpus failed:\n${output.join('').slice(-4000)}`);
  return out;
}

function jsonFiles(directory: string): string[] {
  return existsSync(directory) ? readdirSync(directory).filter((name) => name.endsWith('.json')).sort() : [];
}

test('the committed scenario and HTTP corpora are what the recorder writes', { timeout: 600_000 }, async () => {
  const out = await record();
  try {
    for (const kind of ['scenarios', 'http']) {
      const written = jsonFiles(join(out, kind));
      const expected = (kind === 'scenarios' ? SCENARIO_FILES : HTTP_FILES).map((file) => `${file}.json`).sort();
      assert.deepEqual(written, expected, `the recorder wrote ${kind} for other files than the curated ones`);
      if (process.env.UPDATE_ENGINE_CORPUS) {
        for (const stale of jsonFiles(join(testdata, kind))) {
          rmSync(join(testdata, kind, stale));
        }
        for (const file of written) {
          cpSync(join(out, kind, file), join(testdata, kind, file));
        }
        continue;
      }
      assert.deepEqual(jsonFiles(join(testdata, kind)), expected, `runtime/engine/testdata/${kind} holds other files than the curated ones; rerun with UPDATE_ENGINE_CORPUS=1`);
      for (const file of written) {
        const committed = readFileSync(join(testdata, kind, file), 'utf8');
        const recorded = readFileSync(join(out, kind, file), 'utf8');
        assert.ok(committed === recorded, `runtime/engine/testdata/${kind}/${file} is stale: the engine now records otherwise; rerun with UPDATE_ENGINE_CORPUS=1 and review the diff`);
      }
    }
  } finally {
    rmSync(out, { recursive: true, force: true });
  }
});
