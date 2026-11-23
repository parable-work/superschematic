// The notes schema end to end on development mode (D117), as
// test/notes.test.ts runs it on the engine: superschematic-dev serves the
// same schema file, and the test drives it over HTTP, the event stream and
// MCP as the same four callers, under the same policy written as data
// (src/dev.ts). It holds what the engine holds where development mode
// answers alike: the policy and the behaviors' permissions, a post beside
// the note that moves no version of it, a proposal an editor approves, MCP
// tools as the policy lets each caller call them, and a restart that keeps
// every note. It adds what this work brings to development mode: a streamed
// reply whose draft reaches the stream as signals, a caller's typing, and a
// reader's marker and unread count. src/dev.ts lists what differs.
import assert from 'node:assert/strict';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { after, before, test } from 'node:test';

import { Client, StreamableHTTPClientTransport, type CallToolResult } from '@modelcontextprotocol/client';

import { devCallers, startDev, type DevServer } from '../src/dev.ts';
import { notesSchema } from '../src/server.ts';

const directory = mkdtempSync(join(tmpdir(), 'engine-notes-dev-'));
const db = join(directory, 'notes.db');
const policyFile = join(directory, 'policy.json');
let server: DevServer;
const clients: Client[] = [];

before(async () => {
  server = await startDev(db, policyFile);
  // The schema owner publishes the schema, as the engine's server does when
  // it starts.
  assert.equal((await api('dana', 'POST', '/schemas', notesSchema)).status, 200);
  assert.equal((await api('dana', 'POST', '/schemas/Note/publish')).status, 200);
});

after(async () => {
  for (const client of clients) {
    await client.close();
  }
  await server.close();
  rmSync(directory, { recursive: true, force: true });
});

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
      ...(caller === null ? {} : devCallers[caller]),
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

test('the server serves the published notes schema, and a caller needs to name themselves', async () => {
  const live = await api('alice', 'GET', '/schemas/Note');
  assert.equal(live.status, 200);
  assert.equal(live.body.version, 1);
  assert.equal((await api(null, 'GET', '/schemas/Note')).status, 401);
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

/** openStream reads the namespace's event stream with signals beside its events. */
async function openStream(caller: string) {
  const abort = new AbortController();
  const response = await fetch(`${server.url}/namespaces/default/events?after=head&signals=1&schema=Note`, {
    headers: { ...devCallers[caller], accept: 'text/event-stream' },
    signal: abort.signal,
  });
  assert.equal(response.status, 200);
  const reader = response.body!.pipeThrough(new TextDecoderStream()).getReader();
  let buffer = '';
  return {
    /** next returns the next message's event name and data, comments skipped. */
    async next(timeoutMs = 5000): Promise<{ event: string | undefined; data: any }> {
      const deadline = Date.now() + timeoutMs;
      for (;;) {
        const end = buffer.indexOf('\n\n');
        if (end >= 0) {
          const frame = buffer.slice(0, end);
          buffer = buffer.slice(end + 2);
          const lines = frame.split('\n');
          const data = lines.find((line) => line.startsWith('data: '));
          if (data === undefined) {
            continue;
          }
          const event = lines.find((line) => line.startsWith('event: '))?.slice('event: '.length);
          return { event, data: JSON.parse(data.slice('data: '.length)) };
        }
        let timer: ReturnType<typeof setTimeout> | undefined;
        const timeout = new Promise<never>((_, reject) => {
          timer = setTimeout(() => reject(new Error('no message within the timeout')), Math.max(0, deadline - Date.now()));
        });
        try {
          const { done, value } = await Promise.race([reader.read(), timeout]);
          if (done) {
            throw new Error('the stream ended');
          }
          buffer += value;
        } finally {
          clearTimeout(timer);
        }
      }
    },
    close: () => abort.abort(),
  };
}

test('a streamed reply reaches a reader as signals, and the draft finishes into a message', async () => {
  // A reader opens the stream before it reads, so it misses no chunk.
  const stream = await openStream('carol');
  try {
    assert.equal((await stream.next()).event, 'ready');
    const begun = await operation('bob', 'begin', { thread: 1 });
    assert.deepEqual([begun.body.id, begun.body.state, begun.body.body], [2, 'composing', '']);
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
    // A reader reading midway gets the draft so far, and who is composing.
    const midway = await api('carol', 'GET', '/schemas/Note/instances/launch');
    assert.deepEqual(midway.body.commentsComposing, ['bob']);
    assert.equal((await operation('carol', 'getMessage', { message: 2 })).body.body, 'Ships on the first.');

    // A caller's typing reaches the stream; a draft is no caller's to send.
    const typing = await api('carol', 'POST', '/schemas/Note/instances/launch/signals/Comments/typing', { thread: 1 });
    assert.equal(typing.status, 204);
    const typed = await stream.next();
    assert.deepEqual([typed.event, typed.data.name, typed.data.from, typed.data.data], ['signal', 'typing', 'carol', { thread: 1 }]);
    assert.equal((await api('carol', 'POST', '/schemas/Note/instances/launch/signals/Comments/draft', {})).status, 403);

    const finished = await operation('bob', 'finish', { message: 2 });
    assert.deepEqual([finished.body.state, finished.body.body, finished.body.thread], ['posted', 'Ships on the first.', 1]);
  } finally {
    stream.close();
  }
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

/** mcp connects the official MCP client to the namespace's endpoint as a caller of src/dev.ts. */
async function mcp(caller: string): Promise<Client> {
  const client = new Client({ name: 'engine-notes-dev-test', version: '0.0.0' });
  const url = new URL(`${server.url}/namespaces/default/mcp`);
  await client.connect(new StreamableHTTPClientTransport(url, { requestInit: { headers: { ...devCallers[caller] } } }));
  clients.push(client);
  return client;
}

test('the MCP tools: each caller lists what the policy lets them call, and calls go through it', async () => {
  const alice = await mcp('alice');
  const names = (await alice.listTools()).tools.map((tool) => tool.name);
  for (const name of ['note_create', 'note_get', 'note_update', 'note_transition', 'note_post', 'note_propose', 'note_approve', 'note_list_revisions']) {
    assert.ok(names.includes(name), `alice lists ${name}`);
  }
  const carol = await mcp('carol');
  const carols = (await carol.listTools()).tools.map((tool) => tool.name);
  assert.ok(carols.includes('note_get') && carols.includes('note_post') && carols.includes('note_propose') && carols.includes('note_mark_read'));
  for (const name of ['note_create', 'note_update', 'note_delete', 'note_transition', 'note_approve']) {
    assert.ok(!carols.includes(name), `carol does not list ${name}`);
  }
  const read = (await alice.callTool({ name: 'note_get', arguments: { id: 'launch' } })) as CallToolResult;
  assert.equal((read.structuredContent as { workflowStatus: string }).workflowStatus, 'published');
  const proposed = (await carol.callTool({ name: 'note_propose', arguments: { id: 'launch', params: { patch: { tags: ['launch', 'q3'] } } } })) as CallToolResult;
  assert.equal((proposed.structuredContent as { id: number }).id, 2);
  // A refusal is a tool error carrying the HTTP API's problem document.
  const refused = (await alice.callTool({ name: 'note_approve', arguments: { id: 'launch', params: { proposal: 2 } } })) as CallToolResult;
  assert.equal(refused.isError, true);
  assert.equal((refused.structuredContent as { code: string }).code, 'forbidden');
});

test('a restart keeps every note, publishes a compatible change, and refuses one the stored notes break', async () => {
  for (const client of clients.splice(0)) {
    await client.close();
  }
  await server.close();
  server = await startDev(db, policyFile);

  const note = await api('bob', 'GET', '/schemas/Note/instances/launch');
  assert.equal(note.body.body, 'Ships on the first.');
  assert.equal(note.body.commentsMessages, 3);

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
