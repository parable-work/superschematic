// A smoke of superschematic-dev, development mode's server in Rust
// (runtime/engine/rust/rules-dev, D117), over the SQLite file this
// example's TypeScript host writes: the two hosts keep one layout, so
// either serves the other's file. The TypeScript host publishes the notes
// schema and creates a note; superschematic-dev then serves the file, the
// caller named by its headers and the policy of src/dev.ts given as data,
// moves the note through Workflow under that policy, and sends a streamed
// reply's draft and a caller's typing to a reader's stream as signals,
// which the TypeScript host does not publish yet.
import assert from 'node:assert/strict';
import { spawn, type ChildProcess } from 'node:child_process';
import { existsSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { after, test } from 'node:test';

import type { SqliteConnection } from '@superschematic/rules-handler';

import { callers, devPolicy, openNotesDev } from '../src/dev.ts';
import { openStream } from './stream.ts';

/** The repository's root, three directories above this file. */
const repoRoot = join(dirname(fileURLToPath(import.meta.url)), '..', '..', '..');

/**
 * The superschematic-dev binary: SUPERSCHEMATIC_DEV when set, else the debug
 * build in the engine's Cargo workspace (scripts/check.sh builds it).
 */
function devBinary(): string {
  const given = process.env.SUPERSCHEMATIC_DEV;
  if (given !== undefined && given !== '') {
    return given;
  }
  const target = process.env.CARGO_TARGET_DIR ?? join(repoRoot, 'runtime', 'engine', 'rust', 'target');
  return join(target, 'debug', 'superschematic-dev');
}

/** The headers that name a caller to superschematic-dev, by its subject. */
function as(subject: string): Record<string, string> {
  const caller = [...callers.values()].find((known) => known.subject === subject);
  assert.ok(caller !== undefined, `no caller ${subject}`);
  return { 'x-superschematic-subject': caller.subject, 'x-superschematic-permissions': (caller.permissions ?? []).join(',') };
}

/** A SQLite file: bun:sqlite's under Bun, node:sqlite's under Node.js. */
async function open(path: string): Promise<SqliteConnection & { close(): void }> {
  if ((globalThis as { Bun?: unknown }).Bun !== undefined) {
    const { Database } = await import('bun:sqlite' as string);
    return new Database(path, { create: true });
  }
  const { DatabaseSync } = await import('node:sqlite' as string);
  return new DatabaseSync(path);
}

/**
 * serveFile runs superschematic-dev over a SQLite file, with the policy
 * given and no default caller, on a free port, and resolves to its origin
 * and a stop once it serves.
 */
function serveFile(db: string, policyFile: string): Promise<{ url: string; close(): Promise<void> }> {
  const binary = devBinary();
  if (!existsSync(binary)) {
    return Promise.reject(
      new Error(`${binary} is missing: build it with cargo build -p superschematic-rules-dev --features server --bin superschematic-dev, or set SUPERSCHEMATIC_DEV`)
    );
  }
  const child: ChildProcess = spawn(binary, ['--db', db, '--listen', '127.0.0.1:0', '--policy', `@${policyFile}`, '--as', ''], {
    stdio: ['ignore', 'ignore', 'pipe'],
  });
  return new Promise((resolve, reject) => {
    let output = '';
    const failed = (error: Error) => reject(error);
    child.once('error', failed);
    child.once('exit', (code) => reject(new Error(`superschematic-dev exited with ${code}: ${output}`)));
    child.stderr?.setEncoding('utf8');
    child.stderr?.on('data', (chunk: string) => {
      output += chunk;
      const served = /serving (http:\/\/[^/\s]+)/.exec(output);
      if (served === null) {
        return;
      }
      child.removeAllListeners('exit');
      child.removeListener('error', failed);
      resolve({
        url: served[1]!,
        close: () =>
          new Promise<void>((done) => {
            if (child.exitCode !== null) {
              done();
              return;
            }
            child.once('exit', () => done());
            child.kill('SIGTERM');
          }),
      });
    });
  });
}

const directory = mkdtempSync(join(tmpdir(), 'engine-notes-dev-server-'));

after(() => rmSync(directory, { recursive: true, force: true }));

test('superschematic-dev serves the file the TypeScript host wrote, under the same policy', async () => {
  const db = join(directory, 'notes.db');
  const connection = await open(db);
  const dev = await openNotesDev(connection);
  const created = await dev.http(
    new Request('http://notes.dev/namespaces/default/schemas/Note/instances', {
      method: 'POST',
      headers: { authorization: 'Bearer alice-token', 'content-type': 'application/json' },
      body: JSON.stringify({ id: 'launch', data: { title: 'Launch' } }),
    })
  );
  assert.equal(created.status, 201);
  connection.close();

  const policyFile = join(directory, 'policy.json');
  writeFileSync(policyFile, JSON.stringify(devPolicy));
  const server = await serveFile(db, policyFile);
  try {
    const call = async (subject: string | null, method: string, route: string, body?: unknown) => {
      const response = await fetch(`${server.url}/namespaces/default${route}`, {
        method,
        headers: { ...(subject === null ? {} : as(subject)), ...(body === undefined ? {} : { 'content-type': 'application/json' }) },
        body: body === undefined ? undefined : JSON.stringify(body),
      });
      const text = await response.text();
      const json = text === '' ? {} : (JSON.parse(text) as { data?: unknown });
      return { status: response.status, body: response.ok ? (json.data as any) : (json as any) };
    };
    const note = await call('alice', 'GET', '/schemas/Note/instances/launch');
    assert.equal(note.status, 200);
    assert.deepEqual([note.body.title, note.body.workflowStatus], ['Launch', 'draft']);
    assert.equal((await call(null, 'GET', '/schemas/Note/instances/launch')).status, 401);

    const transition = (subject: string, to: string) => call(subject, 'POST', '/schemas/Note/instances/launch/operations/transition', { to });
    assert.deepEqual((await transition('alice', 'review')).body, { from: 'draft', to: 'review' });
    // review -> published names notes.publish, which alice does not hold.
    assert.equal((await transition('alice', 'published')).status, 403);
    assert.deepEqual((await transition('bob', 'published')).body, { from: 'review', to: 'published' });

    // A streamed reply reaches a reader as signals: this host publishes
    // them to the streams open in its process.
    const operation = (subject: string, name: string, params: unknown) =>
      call(subject, 'POST', `/schemas/Note/instances/launch/operations/${name}`, params);
    assert.equal((await operation('carol', 'post', { body: 'Say when it ships.' })).status, 200);
    const stream = await openStream(server.url, as('carol'));
    try {
      assert.equal((await stream.next()).event, 'ready');
      const begun = await operation('bob', 'begin', { thread: 1 });
      assert.deepEqual([begun.body.id, begun.body.state], [2, 'composing']);
      for (const text of ['Ships ', 'on the first.']) {
        assert.equal((await operation('bob', 'appendDraft', { message: 2, text })).status, 200);
      }
      const chunks = [await stream.next(), await stream.next()];
      assert.deepEqual(
        chunks.map(({ event, data }) => [event, data.behavior, data.name, data.from, data.data]),
        [
          ['signal', 'Comments', 'draft', 'bob', { message: 2, offset: 0, text: 'Ships ' }],
          ['signal', 'Comments', 'draft', 'bob', { message: 2, offset: 6, text: 'on the first.' }],
        ]
      );
      // A caller's typing reaches the stream; a draft is no caller's to send.
      assert.equal((await call('carol', 'POST', '/schemas/Note/instances/launch/signals/Comments/typing', { thread: 1 })).status, 204);
      const typed = await stream.next();
      assert.deepEqual([typed.event, typed.data.name, typed.data.from, typed.data.data], ['signal', 'typing', 'carol', { thread: 1 }]);
      assert.equal((await call('carol', 'POST', '/schemas/Note/instances/launch/signals/Comments/draft', {})).status, 403);
      const finished = await operation('bob', 'finish', { message: 2 });
      assert.deepEqual([finished.body.state, finished.body.body], ['posted', 'Ships on the first.']);
    } finally {
      stream.close();
    }
  } finally {
    await server.close();
  }
});
