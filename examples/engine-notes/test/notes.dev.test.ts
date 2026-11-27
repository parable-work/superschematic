// The notes schema end to end on development mode (D117):
// @superschematic/rules-handler serves it over one SQLite file (bun:sqlite
// under Bun, node:sqlite under Node.js), behind @hono/node-server on a free
// port, and the test drives it over HTTP, the event stream and MCP as the
// four callers of src/dev.ts, each by its bearer token, under the policy
// written as data. It holds the policy and the behaviors' permissions, a
// post beside the note that moves no version of it, a proposal an editor
// approves, a streamed reply a reader reads midway, a reader's marker and
// unread count, MCP tools as the policy lets each caller call them, and a
// restart that keeps every note. The signals a streamed reply sends are
// test/notes.dev-server.test.ts's, since this host publishes none.
import assert from 'node:assert/strict';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { after, before, test } from 'node:test';

import type { SqliteConnection } from '@superschematic/rules-handler';

import { listen, notesSchema, openNotesDev, type Listening } from '../src/dev.ts';
import { openStream } from './stream.ts';

const directory = mkdtempSync(join(tmpdir(), 'engine-notes-dev-'));
const db = join(directory, 'notes.db');
let connection: SqliteConnection & { close(): void };
let server: Listening;

/** A SQLite file: bun:sqlite's under Bun, node:sqlite's under Node.js. */
async function open(path: string): Promise<SqliteConnection & { close(): void }> {
  if ((globalThis as { Bun?: unknown }).Bun !== undefined) {
    const { Database } = await import('bun:sqlite' as string);
    return new Database(path, { create: true });
  }
  const { DatabaseSync } = await import('node:sqlite' as string);
  return new DatabaseSync(path);
}

/** start opens the file and serves the notes on it. */
async function start(): Promise<void> {
  connection = await open(db);
  server = await listen(await openNotesDev(connection));
}

/** stop stops serving and closes the file. */
async function stop(): Promise<void> {
  await server.close();
  connection.close();
}

before(start);

after(async () => {
  await stop();
  rmSync(directory, { recursive: true, force: true });
});

/** The headers that name a caller of src/dev.ts: its bearer token. */
function as(caller: string): Record<string, string> {
  return { authorization: `Bearer ${caller}-token` };
}

interface Reply {
  status: number;
  etag: string | null;
  /** The envelope's data on success, the problem document on a refusal. */
  body: any;
}

/** api calls a route under /namespaces/default as a caller of src/dev.ts, or as none. */
async function api(caller: string | null, method: string, route: string, body?: unknown): Promise<Reply> {
  const response = await fetch(`${server.url}/namespaces/default${route}`, {
    method,
    headers: {
      ...(caller === null ? {} : as(caller)),
      ...(body === undefined ? {} : { 'content-type': method === 'PATCH' ? 'application/merge-patch+json' : 'application/json' }),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await response.text();
  const json = text === '' ? {} : (JSON.parse(text) as { data?: unknown });
  return { status: response.status, etag: response.headers.get('etag'), body: response.ok ? json.data : json };
}

/** operation calls a behavior operation on the note. */
function operation(caller: string, name: string, params: unknown): Promise<Reply> {
  return api(caller, 'POST', `/schemas/Note/instances/launch/operations/${name}`, params);
}

test('the server serves the published notes schema, and a caller needs a token it knows', async () => {
  const live = await api('alice', 'GET', '/schemas/Note');
  assert.equal(live.status, 200);
  assert.equal(live.body.version, 1);
  assert.equal((await api(null, 'GET', '/schemas/Note')).status, 401);
  assert.equal((await api('mallory', 'GET', '/schemas/Note')).status, 401);
});

test('an author creates a note; a reader may not', async () => {
  const refused = await api('carol', 'POST', '/schemas/Note/instances', { data: { title: 'Launch' } });
  assert.equal(refused.status, 403);
  assert.equal(refused.body.code, 'forbidden');

  const created = await api('alice', 'POST', '/schemas/Note/instances', { id: 'launch', data: { title: 'Launch', tags: ['launch'] } });
  assert.equal(created.status, 201);
  assert.equal(created.etag, '"1"');
  assert.deepEqual(
    [created.body.title, created.body.tags, created.body.workflowStatus, created.body.revisionsRevision],
    ['Launch', ['launch'], 'draft', 1]
  );
});

test('a transition that names a permission needs it', async () => {
  assert.deepEqual((await operation('alice', 'transition', { to: 'review' })).body, { from: 'draft', to: 'review' });
  // review -> published names notes.publish, which alice does not hold.
  const refused = await operation('alice', 'transition', { to: 'published' });
  assert.equal(refused.status, 403);
  assert.equal(refused.body.code, 'forbidden');
  assert.deepEqual((await operation('bob', 'transition', { to: 'published' })).body, { from: 'review', to: 'published' });
  assert.equal((await api('bob', 'GET', '/schemas/Note/instances/launch')).etag, '"3"');
});

test('a reader comments and proposes a change; an editor approves it', async () => {
  // A post is beside the note: it leaves the note's version as it was.
  const comment = await operation('carol', 'post', { body: 'Say when it ships.' });
  assert.equal(comment.status, 200);
  assert.deepEqual([comment.body.id, comment.body.author, comment.body.state], [1, 'carol', 'posted']);
  assert.equal((await api('carol', 'GET', '/schemas/Note/instances/launch')).etag, '"3"');

  // A reader may not change the note herself.
  assert.equal((await api('carol', 'PATCH', '/schemas/Note/instances/launch', { body: 'Ships on the first.' })).status, 403);
  const proposal = await operation('carol', 'propose', { patch: { body: 'Ships on the first.' }, note: 'Adds the date.' });
  assert.deepEqual([proposal.body.id, proposal.body.state], [1, 'pending']);
  // Approving needs notes.review, which alice does not hold.
  assert.equal((await operation('alice', 'approve', { proposal: 1 })).status, 403);
  const approved = await operation('bob', 'approve', { proposal: 1 });
  assert.deepEqual([approved.body.state, approved.body.revision], ['approved', 2]);

  const note = await api('carol', 'GET', '/schemas/Note/instances/launch');
  assert.equal(note.body.body, 'Ships on the first.');
  assert.deepEqual(
    [note.body.commentsMessages, note.body.commentsLastMessageAt, note.body.commentsThreads, note.body.commentsComposing],
    [1, comment.body.postedAt, { open: 1, resolved: 0 }, []]
  );
  const revisions = await operation('carol', 'listRevisions', {});
  assert.deepEqual(
    revisions.body.items.map(({ revision }: { revision: number }) => revision),
    [1, 2]
  );
});

test('a streamed reply: a reader reading midway gets the draft so far, and the draft finishes into a message', async () => {
  // The stream is open to a reader, and ready once it has caught up. This
  // host publishes no signals: the draft's chunks reach a stream of
  // superschematic-dev's (test/notes.dev-server.test.ts).
  const stream = await openStream(server.url, as('carol'));
  try {
    assert.equal((await stream.next()).event, 'ready');
  } finally {
    stream.close();
  }
  const begun = await operation('bob', 'begin', { thread: 1 });
  assert.deepEqual([begun.body.id, begun.body.state, begun.body.body], [2, 'composing', '']);
  for (const text of ['Ships ', 'on the first.']) {
    assert.equal((await operation('bob', 'appendDraft', { message: 2, text })).status, 200);
  }
  // A reader reading midway gets the draft so far, and who is composing.
  const midway = await api('carol', 'GET', '/schemas/Note/instances/launch');
  assert.deepEqual(midway.body.commentsComposing, ['bob']);
  assert.equal((await operation('carol', 'getMessage', { message: 2 })).body.body, 'Ships on the first.');
  // A draft is no caller's signal to send.
  assert.equal((await api('carol', 'POST', '/schemas/Note/instances/launch/signals/Comments/draft', {})).status, 403);

  const finished = await operation('bob', 'finish', { message: 2 });
  assert.deepEqual([finished.body.state, finished.body.body, finished.body.thread], ['posted', 'Ships on the first.', 1]);
});

test('a reader marks the conversation read, and unread counts what came after', async () => {
  // A conversation alice never read is unread for nobody.
  assert.equal((await api('alice', 'GET', '/schemas/Note/instances/launch')).body.commentsUnread, 0);
  // alice may read the note, so she may mark it read.
  const marked = await operation('alice', 'markRead', { upTo: 2 });
  assert.deepEqual(marked.body, { readUpTo: 1, unread: 0 });
  await operation('carol', 'post', { body: 'Anything else?' });
  const note = await api('alice', 'GET', '/schemas/Note/instances/launch');
  assert.equal(note.body.commentsUnread, 1);
  assert.equal((await api('carol', 'GET', '/schemas/Note/instances/launch')).body.commentsUnread, 0);
  const listed = await operation('alice', 'listMessages', {});
  assert.equal(listed.body.readUpTo, 1);
  // carol follows the thread she posted in.
  assert.deepEqual((await operation('carol', 'follow', { thread: 1, on: false })).body, { thread: 1, following: false });
});

let rpcId = 0;

/**
 * mcp sends one JSON-RPC request to the namespace's MCP endpoint as a
 * caller of src/dev.ts, as a client of the 2025 revisions does over the
 * stateless streamable HTTP transport, and resolves to its result or its
 * error from the one message event the endpoint answers.
 */
async function mcp(caller: string, method: string, params: Record<string, unknown> = {}): Promise<{ result?: any; error?: any }> {
  const response = await fetch(`${server.url}/namespaces/default/mcp`, {
    method: 'POST',
    headers: {
      ...as(caller),
      'content-type': 'application/json',
      accept: 'application/json, text/event-stream',
      'mcp-protocol-version': '2025-11-25',
    },
    body: JSON.stringify({ jsonrpc: '2.0', id: ++rpcId, method, params }),
  });
  assert.equal(response.status, 200);
  const data = (await response.text()).split('\n').find((line) => line.startsWith('data: '));
  assert.ok(data !== undefined, 'the endpoint answers one message event');
  return JSON.parse(data.slice('data: '.length));
}

/** tools lists the names of the tools a caller may call. */
async function tools(caller: string): Promise<string[]> {
  const listed = await mcp(caller, 'tools/list');
  return (listed.result.tools as { name: string }[]).map((tool) => tool.name);
}

test('the MCP tools: each caller lists what the policy lets them call, and calls go through it', async () => {
  const initialized = await mcp('alice', 'initialize', {
    protocolVersion: '2025-11-25',
    capabilities: {},
    clientInfo: { name: 'engine-notes-dev-test', version: '0.0.0' },
  });
  assert.deepEqual([initialized.result.protocolVersion, initialized.result.serverInfo.name], ['2025-11-25', 'engine-notes']);

  const alices = await tools('alice');
  for (const name of ['note_create', 'note_get', 'note_update', 'note_transition', 'note_post', 'note_propose', 'note_approve', 'note_list_revisions']) {
    assert.ok(alices.includes(name), `alice lists ${name}`);
  }
  const carols = await tools('carol');
  assert.ok(carols.includes('note_get') && carols.includes('note_post') && carols.includes('note_propose') && carols.includes('note_mark_read'));
  for (const name of ['note_create', 'note_update', 'note_delete', 'note_transition', 'note_approve']) {
    assert.ok(!carols.includes(name), `carol does not list ${name}`);
  }
  const read = await mcp('alice', 'tools/call', { name: 'note_get', arguments: { id: 'launch' } });
  assert.equal(read.result.structuredContent.workflowStatus, 'published');
  const proposed = await mcp('carol', 'tools/call', { name: 'note_propose', arguments: { id: 'launch', params: { patch: { tags: ['launch', 'q3'] } } } });
  assert.equal(proposed.result.structuredContent.id, 2);
  // A refusal is a tool error carrying the HTTP API's problem document.
  const refused = await mcp('alice', 'tools/call', { name: 'note_approve', arguments: { id: 'launch', params: { proposal: 2 } } });
  assert.equal(refused.result.isError, true);
  assert.equal(refused.result.structuredContent.code, 'forbidden');
  // A tool the caller is not offered is an invalid-params error.
  const unknown = await mcp('carol', 'tools/call', { name: 'note_approve', arguments: { id: 'launch', params: { proposal: 2 } } });
  assert.equal(unknown.error.code, -32602);
});

test('a restart keeps every note, publishes a compatible change, and refuses one the stored notes break', async () => {
  await stop();
  await start();

  const note = await api('bob', 'GET', '/schemas/Note/instances/launch');
  assert.equal(note.body.body, 'Ships on the first.');
  assert.equal(note.body.commentsMessages, 3);
  // The restart published nothing: Note is live at the version it was.
  assert.equal((await api('alice', 'GET', '/schemas/Note')).body.version, 1);

  // A new optional field becomes version 2.
  const withPinned = structuredClone(notesSchema) as { types: { Note: { fields: unknown[] } } };
  withPinned.types.Note.fields.push({ name: 'pinned', description: 'Whether the note shows first.', typeRef: { name: 'boolean' } });
  assert.equal((await api('dana', 'POST', '/schemas', withPinned)).status, 200);
  const published = await api('dana', 'POST', '/schemas/Note/publish');
  assert.equal(published.status, 200);
  assert.equal(published.body.version, 2);
  const pinned = await api('bob', 'PATCH', '/schemas/Note/instances/launch', { pinned: true });
  assert.equal(pinned.status, 200);
  assert.equal(pinned.body.pinned, true);

  // A required field the stored note lacks: the draft is taken, and its
  // publish refused while a note does not hold it.
  const withOwner = structuredClone(withPinned) as { types: { Note: { fields: unknown[] } } };
  withOwner.types.Note.fields.push({ name: 'owner', typeRef: { name: 'string' }, required: true });
  assert.equal((await api('alice', 'POST', '/schemas', withOwner)).status, 403);
  assert.equal((await api('dana', 'POST', '/schemas', withOwner)).status, 200);
  const refused = await api('dana', 'POST', '/schemas/Note/publish');
  assert.equal(refused.status, 409);
  assert.equal(refused.body.code, 'conflict');
  assert.ok(refused.body.details.rows.length > 0);
});
