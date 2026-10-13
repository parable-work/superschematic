// The SQLite corpus (sqlite-corpus.ts): an engine file today's engine wrote
// with every core and work-queue behavior in use, committed as SQL text
// under runtime/engine/testdata/sqlite with the reads expected from it.
// This test writes it again and fails when the committed files are not what
// the engine writes and reads now; UPDATE_ENGINE_CORPUS=1 rewrites them.
// Review the diff: a change to the engine's tables or to what a behavior
// stores shows there, and a second implementation must open both files.
import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';

import { END, SQLITE_TESTDATA, dumpDatabase, loadDatabase, openCorpusEngine, readCorpus, renderReads, writeCorpus, writeTestdata } from './sqlite-corpus.ts';

test('the committed SQLite file and its reads are what the engine writes and reads, and the text loads into a file that reads the same', () => {
  const update = Boolean(process.env.UPDATE_ENGINE_CORPUS);
  const { sql, reads } = writeTestdata(update ? SQLITE_TESTDATA : '');
  if (update) {
    return;
  }
  assert.ok(readFileSync(join(SQLITE_TESTDATA, 'typescript.sql'), 'utf8') === sql, 'runtime/engine/testdata/sqlite/typescript.sql is stale; rerun with UPDATE_ENGINE_CORPUS=1 and review the diff');
  assert.ok(readFileSync(join(SQLITE_TESTDATA, 'expected.json'), 'utf8') === reads, 'runtime/engine/testdata/sqlite/expected.json is stale; rerun with UPDATE_ENGINE_CORPUS=1 and review the diff');

  const work = mkdtempSync(join(tmpdir(), 'superschematic-engine-sqlite-'));
  try {
    // The file the engine wrote reads as its dump loaded does.
    const written = join(work, 'written.db');
    writeCorpus(written);
    const engine = openCorpusEngine(written, { now: END });
    try {
      assert.ok(renderReads(readCorpus(engine)) === reads, 'the file the engine wrote reads otherwise than its dump loaded into a file');
    } finally {
      engine.close();
    }
    // Loaded and dumped again, the text is the same.
    const loaded = join(work, 'loaded.db');
    loadDatabase(sql, loaded);
    assert.ok(dumpDatabase(loaded) === sql, 'typescript.sql loaded into a file dumps otherwise');
  } finally {
    rmSync(work, { recursive: true, force: true });
  }
});
