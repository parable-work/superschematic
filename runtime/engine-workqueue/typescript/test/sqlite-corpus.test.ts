// The SQLite corpus (sqlite-corpus.ts): the engine file the TypeScript
// engine wrote with every core and work-queue behavior in use, committed as
// SQL text under runtime/engine/testdata/sqlite with the reads expected
// from it, frozen at the cutover (D58). The engine opens it, migrates it
// and reads it as recorded (expected.json); runs the script that wrote it
// on a file of its own, which reads as written.json holds, the same but
// for Comments, whose posts are beside the instance since D77; and the
// text loads into a file that dumps the same text. With
// UPDATE_ENGINE_CORPUS=1 the first two rewrite the reads they hold to.
import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, test } from 'node:test';

import { END, SQLITE_TESTDATA, dumpDatabase, loadDatabase, openCorpusEngine, readCorpus, renderReads, writeCorpus } from './sqlite-corpus.ts';

const sql = readFileSync(join(SQLITE_TESTDATA, 'typescript.sql'), 'utf8');
const update = process.env.UPDATE_ENGINE_CORPUS === '1';

// held holds a file's reads to the recorded ones, or with
// UPDATE_ENGINE_CORPUS=1 records them.
function held(file: string, got: string): boolean {
  const path = join(SQLITE_TESTDATA, file);
  if (update) {
    writeFileSync(path, got);
  }
  return got === readFileSync(path, 'utf8');
}

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

// reads opens the file at path as the corpus's engine, at the script's end, and makes the reads; written names the file the script wrote.
function reads(path: string, written = false): string {
  const engine = openCorpusEngine(path, { now: END });
  try {
    return renderReads(readCorpus(engine), written);
  } finally {
    engine.close();
  }
}

test('the file the TypeScript engine wrote opens in the engine, migrates, and reads as recorded', { timeout: 60_000 }, () => {
  const loaded = join(workDirectory(), 'loaded.db');
  loadDatabase(sql, loaded);
  assert.ok(held('expected.json', reads(loaded)), 'typescript.sql, loaded into a file, reads otherwise than expected.json');
});

test('the engine, running the script on a file of its own, writes one that reads as written.json holds', { timeout: 60_000 }, () => {
  const written = join(workDirectory(), 'written.db');
  writeCorpus(written);
  assert.ok(held('written.json', reads(written, true)), 'the file the engine writes reads otherwise than written.json');
});

test('the text loads into a file that dumps the same text', { timeout: 60_000 }, () => {
  const loaded = join(workDirectory(), 'loaded.db');
  loadDatabase(sql, loaded);
  assert.ok(dumpDatabase(loaded) === sql, 'typescript.sql loaded into a file dumps otherwise');
});
