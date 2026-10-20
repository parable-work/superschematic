// The check the corpus tests run (D58), this suite's corpus.test.ts and the
// work queue's (runtime/engine-workqueue/typescript/test/corpus.test.ts):
// it records a package's curated test files again, under Node.js, with
// ENGINE_RECORD set to a fresh directory, and fails when a committed file
// under runtime/engine/testdata is not what the recorder wrote;
// UPDATE_ENGINE_CORPUS=1 rewrites the package's files. Not a test file of
// the suite: its name does not end in .test.ts.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { cpSync, existsSync, mkdtempSync, readFileSync, readdirSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

/**
 * The prefixes of the files a package beside the engine records into the
 * same directories (recorder.ts's recordPackage): the engine's own check
 * leaves them to that package's.
 */
export const PACKAGE_PREFIXES = ['workqueue-'];

/** What one package's tests record into the corpus. */
export interface CorpusFiles {
  /** The package's directory, where node --test runs its tests. */
  readonly root: string;
  /** The prefix its files take (recordPackage's), or '' for the engine's own. */
  readonly prefix: string;
  /** The test files, without .test.ts, whose scenarios the corpus holds. */
  readonly scenarios: readonly string[];
  /** The test files whose exchanges the HTTP corpus holds. */
  readonly http: readonly string[];
}

const testdata = fileURLToPath(new URL('../../testdata/', import.meta.url));

// record runs the package's curated files under Node.js with ENGINE_RECORD
// set to a fresh directory and returns it.
async function record(corpus: CorpusFiles): Promise<string> {
  const out = mkdtempSync(join(tmpdir(), 'superschematic-engine-corpus-'));
  const env: Record<string, string | undefined> = { ...process.env, ENGINE_RECORD: out };
  // A test runner's own variables would make the child report to this one.
  delete env.NODE_TEST_CONTEXT;
  delete env.UPDATE_ENGINE_CORPUS;
  // The corpus is SQLite's: a backends test records its Postgres pass too
  // when the variable names a database.
  delete env.ENGINE_TEST_PG_URL;
  const files = [...corpus.scenarios, ...corpus.http].map((file) => `test/${file}.test.ts`);
  const output: string[] = [];
  const code = await new Promise<number | null>((resolve, reject) => {
    const child = spawn('node', ['--test', '--test-reporter=dot', ...files], { cwd: corpus.root, env, stdio: ['ignore', 'pipe', 'pipe'] });
    child.stdout.on('data', (chunk: Buffer) => output.push(chunk.toString()));
    child.stderr.on('data', (chunk: Buffer) => output.push(chunk.toString()));
    child.on('error', reject);
    child.on('close', resolve);
  });
  if (code !== 0) {
    rmSync(out, { recursive: true, force: true });
  }
  assert.equal(code, 0, `recording the corpus failed:\n${output.join('').slice(-4000)}`);
  return out;
}

// owns says whether a file of the corpus is the package's: one with its
// prefix, or, for the engine's own, one with no package's prefix.
function owns(corpus: CorpusFiles, file: string): boolean {
  return corpus.prefix === '' ? !PACKAGE_PREFIXES.some((prefix) => file.startsWith(prefix)) : file.startsWith(corpus.prefix);
}

function jsonFiles(directory: string): string[] {
  return existsSync(directory) ? readdirSync(directory).filter((name) => name.endsWith('.json')).sort() : [];
}

/**
 * checkCorpus records the package's curated files and holds the committed
 * ones to them: the package's files in runtime/engine/testdata/scenarios
 * and http must be the curated ones, each as the recorder writes it. With
 * UPDATE_ENGINE_CORPUS set it rewrites the package's files instead, and
 * removes those it no longer records; another package's it leaves.
 */
export async function checkCorpus(corpus: CorpusFiles): Promise<void> {
  const out = await record(corpus);
  try {
    for (const kind of ['scenarios', 'http'] as const) {
      const written = jsonFiles(join(out, kind));
      const expected = corpus[kind].map((file) => `${corpus.prefix}${file}.json`).sort();
      assert.deepEqual(written, expected, `the recorder wrote ${kind} for other files than the curated ones`);
      const committed = jsonFiles(join(testdata, kind)).filter((file) => owns(corpus, file));
      if (process.env.UPDATE_ENGINE_CORPUS) {
        for (const stale of committed) {
          rmSync(join(testdata, kind, stale));
        }
        for (const file of written) {
          cpSync(join(out, kind, file), join(testdata, kind, file));
        }
        continue;
      }
      assert.deepEqual(committed, expected, `runtime/engine/testdata/${kind} holds other files than the curated ones; rerun with UPDATE_ENGINE_CORPUS=1`);
      for (const file of written) {
        const text = readFileSync(join(testdata, kind, file), 'utf8');
        const recorded = readFileSync(join(out, kind, file), 'utf8');
        assert.ok(text === recorded, `runtime/engine/testdata/${kind}/${file} is stale: the engine now records otherwise; rerun with UPDATE_ENGINE_CORPUS=1 and review the diff`);
      }
    }
  } finally {
    rmSync(out, { recursive: true, force: true });
  }
}
