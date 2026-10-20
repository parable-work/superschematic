// The SQLite corpus (sqlite-corpus.ts): the engine file the TypeScript
// engine wrote with every core and work-queue behavior in use, committed as
// SQL text under runtime/engine/testdata/sqlite with the reads expected
// from it, frozen at the cutover (D58). The engine opens it, migrates it
// and reads it as recorded; runs the script that wrote it on a file of its
// own, which reads the same; and the text loads into a file that dumps the
// same text.
import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, test } from 'node:test';

import { END, SQLITE_TESTDATA, dumpDatabase, loadDatabase, openCorpusEngine, readCorpus, renderReads, writeCorpus } from './sqlite-corpus.ts';

const sql = readFileSync(join(SQLITE_TESTDATA, 'typescript.sql'), 'utf8');
const expected = readFileSync(join(SQLITE_TESTDATA, 'expected.json'), 'utf8');

const directories: string[] = [];

afterEach(() => {
  for (const directory of directories.splice(0)) {
    rmSync(directory, { recursive: true, force: true });
  }
});

function workDirectory(): string {
  const directory = mkdtempSync(join(tmpdir(), 'superschematic-engine-sqlite-'));
  directories.push(directory);
  return directory;
}

// reads opens the file at path as the corpus's engine, at the script's end, and makes the reads.
function reads(path: string): string {
  const engine = openCorpusEngine(path, { now: END });
  try {
    return renderReads(readCorpus(engine));
  } finally {
    engine.close();
  }
}

test('the file the TypeScript engine wrote opens in the engine, migrates, and reads as recorded', { timeout: 60_000 }, () => {
  const loaded = join(workDirectory(), 'loaded.db');
  loadDatabase(sql, loaded);
  assert.ok(reads(loaded) === expected, 'typescript.sql, loaded into a file, reads otherwise than expected.json');
});

test("the engine, running the script on a file of its own, writes one that reads as the TypeScript engine's did", { timeout: 60_000 }, () => {
  const written = join(workDirectory(), 'written.db');
  writeCorpus(written);
  assert.ok(reads(written) === expected, 'the file the engine writes reads otherwise than expected.json');
});

test('the text loads into a file that dumps the same text', { timeout: 60_000 }, () => {
  const loaded = join(workDirectory(), 'loaded.db');
  loadDatabase(sql, loaded);
  assert.ok(dumpDatabase(loaded) === sql, 'typescript.sql loaded into a file dumps otherwise');
});
