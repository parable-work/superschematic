// The notes server end to end, over a real listening server
// (@hono/node-server on Node.js and on Bun): the schema the server
// publishes, a note created, moved through its workflow (one transition
// refused without the permission, one allowed), commented on, changed by a
// proposal an editor approves, the event log read from a cursor as JSON and
// as a stream, the MCP tools listed and called with the official client, the
// generated client's typed calls, the compatibility rule refusing a draft,
// and a restart that keeps every note and publishes a compatible change.
import assert from 'node:assert/strict';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { after, before, test } from 'node:test';

import { Client, StreamableHTTPClientTransport, type CallToolResult } from '@modelcontextprotocol/client';
import { IncompatibleChangeError, type Engine, type EngineEvent } from '@superschematic/engine';
import { EngineClient, isProblem } from '@superschematic/engine/client';

import { isNoteVeto, notesClient } from '../src/notes.client.ts';
import { listen, notesApp, notesSchema, openNotes, type Listening } from '../src/server.ts';

const directory = mkdtempSync(join(tmpdir(), 'engine-notes-'));
const path = join(directory, 'notes.db');
let engine: Engine;
let server: Listening;
const clients: Client[] = [];

before(async () => {
  engine = openNotes(path);
  server = await listen(notesApp(engine));
});

after(async () => {
  for (const client of clients) {
    await client.close();
  }
  await server.close();
  engine.close();
  rmSync(directory, { recursive: true, force: true });
});

interface Reply {
  status: number;
  etag: string | null;
  /** The envelope's data on success, the problem document on a refusal. */
  body: any;
}

/** api calls a route under /api/namespaces/default as the caller whose token it is. */
async function api(token: string | null, method: string, route: string, body?: unknown, headers: Record<string, string> = {}): Promise<Reply> {
  const response = await fetch(`${server.url}/api/namespaces/default${route}`, {
    method,
    headers: {
      ...(token === null ? {} : { authorization: `Bearer ${token}` }),
      ...(body === undefined ? {} : { 'content-type': method === 'PATCH' ? 'application/merge-patch+json' : 'application/json' }),
      ...headers,
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const json = (await response.json()) as { data?: unknown };
  return { status: response.status, etag: response.headers.get('etag'), body: response.ok ? json.data : json };
}

/** operation calls a behavior operation on the note, with If-Match when an ETag is given. */
function operation(token: string, name: string, params: unknown, etag?: string): Promise<Reply> {
  return api(token, 'POST', `/schemas/notes/instances/launch/operations/${name}`, params, etag ? { 'if-match': etag } : {});
}

test('the server publishes the notes schema when it starts', async () => {
  const live = await api('alice-token', 'GET', '/schemas/notes');
  assert.equal(live.status, 200);
  assert.equal(live.body.version, 1);
  assert.deepEqual(live.body.document.types.Note.behaviors.map((behavior: { name: string }) => behavior.name), ['Workflow', 'Comments', 'Revisions']);
});

test('a caller needs a token the server knows', async () => {
  assert.equal((await api(null, 'GET', '/schemas/notes')).status, 401);
  assert.equal((await api('mallory-token', 'GET', '/schemas/notes')).status, 401);
});

test('an author creates a note; a reader may not', async () => {
  const refused = await api('carol-token', 'POST', '/schemas/notes/instances', { data: { title: 'Launch' } });
  assert.equal(refused.status, 403);
  assert.equal(refused.body.code, 'forbidden');

  const created = await api('alice-token', 'POST', '/schemas/notes/instances', { id: 'launch', data: { title: 'Launch', tags: ['launch'] } });
  assert.equal(created.status, 201);
  assert.equal(created.etag, '"1"');
  // The behaviors' fields sit beside the note's own: Workflow's status,
  // Comments' commentCount and Revisions' revision and pendingProposals.
  assert.deepEqual(created.body.data, { title: 'Launch', tags: ['launch'], status: 'draft', commentCount: 0, revision: 1, pendingProposals: 0 });
});

test('a transition that names a permission needs it', async () => {
  const toReview = await operation('alice-token', 'transition', { to: 'review' }, '"1"');
  assert.equal(toReview.status, 200);
  assert.deepEqual(toReview.body, { from: 'draft', to: 'review' });
  assert.equal(toReview.etag, '"2"');

  // review -> published names notes.publish, which alice does not hold.
  const refused = await operation('alice-token', 'transition', { to: 'published' }, '"2"');
  assert.equal(refused.status, 403);
  assert.equal(refused.body.code, 'forbidden');

  // A stale ETag is refused before anything runs.
  assert.equal((await operation('bob-token', 'transition', { to: 'published' }, '"1"')).status, 412);

  const published = await operation('bob-token', 'transition', { to: 'published' }, '"2"');
  assert.equal(published.status, 200);
  assert.deepEqual(published.body, { from: 'review', to: 'published' });
  assert.equal(published.etag, '"3"');
});

test('a reader comments and proposes a change; an editor approves it', async () => {
  const comment = await operation('carol-token', 'comment', { body: 'Say when it ships.' });
  assert.equal(comment.status, 200);
  assert.equal(comment.body.id, 1);
  assert.equal(comment.body.createdBy, 'carol');

  // A reader may not change the note herself.
  assert.equal((await api('carol-token', 'PATCH', '/schemas/notes/instances/launch', { body: 'Ships on the first.' })).status, 403);

  const proposal = await operation('carol-token', 'propose', { patch: { body: 'Ships on the first.' }, note: 'Adds the date.' });
  assert.equal(proposal.status, 200);
  assert.equal(proposal.body.id, 1);
  assert.equal(proposal.body.state, 'pending');

  // Approving needs notes.review, which alice does not hold.
  const refused = await operation('alice-token', 'approve', { proposal: 1 });
  assert.equal(refused.status, 403);

  const approved = await operation('bob-token', 'approve', { proposal: 1 });
  assert.equal(approved.status, 200);
  assert.equal(approved.body.state, 'approved');
  assert.equal(approved.body.revision, 2);

  const note = await api('carol-token', 'GET', '/schemas/notes/instances/launch');
  assert.deepEqual(note.body.data, {
    title: 'Launch',
    body: 'Ships on the first.',
    tags: ['launch'],
    status: 'published',
    commentCount: 1,
    revision: 2,
    pendingProposals: 0,
  });
  const revisions = await operation('carol-token', 'listRevisions', {});
  assert.deepEqual(
    revisions.body.items.map(({ revision, data }: { revision: number; data: unknown }) => [revision, data]),
    [
      [1, { title: 'Launch', tags: ['launch'] }],
      [2, { title: 'Launch', body: 'Ships on the first.', tags: ['launch'] }],
    ]
  );
});

/** openStream reads the namespace's event stream after a cursor. */
async function openStream(token: string, after: number) {
  const abort = new AbortController();
  const response = await fetch(`${server.url}/api/namespaces/default/events?after=${after}`, {
    headers: { authorization: `Bearer ${token}`, accept: 'text/event-stream' },
    signal: abort.signal,
  });
  assert.equal(response.status, 200);
  assert.match(response.headers.get('content-type') ?? '', /^text\/event-stream/);
  const reader = response.body!.pipeThrough(new TextDecoderStream()).getReader();
  let buffer = '';
  return {
    /**
     * next returns the next event of the log. Comments (": open",
     * ": keepalive"), the ready event that follows replay and a message
     * with only an id are skipped.
     */
    async next(timeoutMs = 5000): Promise<EngineEvent> {
      const deadline = Date.now() + timeoutMs;
      for (;;) {
        const end = buffer.indexOf('\n\n');
        if (end >= 0) {
          const frame = buffer.slice(0, end);
          buffer = buffer.slice(end + 2);
          const lines = frame.split('\n');
          const data = lines.find((line) => line.startsWith('data: '));
          if (data !== undefined && !lines.some((line) => line.startsWith('event: '))) {
            return JSON.parse(data.slice('data: '.length)) as EngineEvent;
          }
          continue;
        }
        let timer: ReturnType<typeof setTimeout> | undefined;
        const timeout = new Promise<never>((_, reject) => {
          timer = setTimeout(() => reject(new Error('no event within the timeout')), Math.max(0, deadline - Date.now()));
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

test('the event log reads from a cursor, as JSON pages and as a stream', async () => {
  const page = await api('carol-token', 'GET', '/events?after=0');
  assert.equal(page.status, 200);
  const events: EngineEvent[] = page.body.events;
  assert.deepEqual(
    events.map((event) => [event.kind, event.actor, event.kind === 'operation' ? (event.change as { operation: string }).operation : null]),
    [
      ['define', 'engine-notes', null],
      ['publish', 'engine-notes', null],
      ['create', 'alice', null],
      ['operation', 'alice', 'transition'],
      ['operation', 'bob', 'transition'],
      ['operation', 'carol', 'comment'],
      ['operation', 'carol', 'propose'],
      ['operation', 'bob', 'approve'],
    ]
  );
  assert.equal(page.body.more, false);

  // The stream starts after the cursor it is given: here, the create.
  const created = events[2];
  const stream = await openStream('carol-token', created.cursor);
  try {
    const first = await stream.next();
    assert.equal(first.cursor, events[3].cursor);
    assert.deepEqual(first.change, { behavior: 'Workflow', operation: 'transition', params: { to: 'review' }, patch: { status: 'review' } });
    for (const expected of events.slice(4)) {
      assert.equal((await stream.next()).cursor, expected.cursor);
    }
    // Caught up, it sends each event as it commits.
    await operation('alice-token', 'comment', { body: 'Linked the release page.' });
    const live = await stream.next();
    assert.equal(live.kind, 'operation');
    assert.equal(live.actor, 'alice');
    assert.deepEqual((live.change as { patch: unknown }).patch, { commentCount: 2 });
  } finally {
    stream.close();
  }
});

/** mcp connects the official MCP client to the namespace's endpoint as the caller whose token it is. */
async function mcp(token: string): Promise<Client> {
  const client = new Client({ name: 'engine-notes-test', version: '0.0.0' });
  const url = new URL(`${server.url}/api/namespaces/default/mcp`);
  await client.connect(new StreamableHTTPClientTransport(url, { requestInit: { headers: { authorization: `Bearer ${token}` } } }));
  clients.push(client);
  return client;
}

test('the MCP tools: each caller lists what the policy lets them call, and calls go through it', async () => {
  const alice = await mcp('alice-token');
  const names = (await alice.listTools()).tools.map((tool) => tool.name);
  for (const name of ['notes_create', 'notes_get', 'notes_update', 'notes_transition', 'notes_comment', 'notes_propose', 'notes_approve', 'notes_list_revisions']) {
    assert.ok(names.includes(name), `alice lists ${name}`);
  }
  const carol = await mcp('carol-token');
  const carols = (await carol.listTools()).tools.map((tool) => tool.name);
  assert.ok(carols.includes('notes_get') && carols.includes('notes_comment') && carols.includes('notes_propose'));
  for (const name of ['notes_create', 'notes_update', 'notes_delete', 'notes_transition', 'notes_approve']) {
    assert.ok(!carols.includes(name), `carol does not list ${name}`);
  }
  // The schema tools name no schema until they are called, so everyone
  // lists them and the policy answers the call.
  assert.ok(carols.includes('define_schema'));
  const define = (await carol.callTool({ name: 'define_schema', arguments: { document: notesSchema } })) as CallToolResult;
  assert.equal(define.isError, true);
  assert.equal((define.structuredContent as { code: string }).code, 'forbidden');

  const read = (await alice.callTool({ name: 'notes_get', arguments: { id: 'launch' } })) as CallToolResult;
  assert.equal(read.isError, undefined);
  assert.equal((read.structuredContent as { data: { status: string } }).data.status, 'published');

  const proposed = (await carol.callTool({ name: 'notes_propose', arguments: { id: 'launch', params: { patch: { tags: ['launch', 'q3'] } } } })) as CallToolResult;
  assert.equal((proposed.structuredContent as { id: number; state: string }).id, 2);

  // A refusal is a tool error carrying the HTTP API's problem document.
  const refused = (await alice.callTool({ name: 'notes_approve', arguments: { id: 'launch', params: { proposal: 2 } } })) as CallToolResult;
  assert.equal(refused.isError, true);
  const problem = refused.structuredContent as { status: number; code: string; detail: string };
  assert.equal(problem.status, 403);
  assert.equal(problem.code, 'forbidden');

  const bob = await mcp('bob-token');
  const rejected = (await bob.callTool({ name: 'notes_reject', arguments: { id: 'launch', params: { proposal: 2, reason: 'Not yet.' } } })) as CallToolResult;
  assert.equal((rejected.structuredContent as { state: string }).state, 'rejected');
});

test('the generated client types the notes schema: its fields, states, operations and veto codes', async () => {
  // src/notes.client.ts is what `superschematic engine-client` writes for
  // schemas/notes.schema.json: a typo in a field, a state or a parameter
  // fails the typecheck instead of reaching the server.
  const as = (token: string) => notesClient(new EngineClient({ baseUrl: `${server.url}/api`, auth: { token } }));
  const alice = as('alice-token');
  const bob = as('bob-token');

  const note = await alice.create({ title: 'Typed', tags: ['client'] }, { id: 'typed' });
  assert.deepEqual([note.data.status, note.data.commentCount, note.data.revision], ['draft', 0, 1]);
  assert.deepEqual(await alice.transition('typed', { to: 'review' }), { from: 'draft', to: 'review' });
  // review -> published names notes.publish, which alice does not hold.
  await assert.rejects(alice.transition('typed', { to: 'published' }), (error) => isProblem(error, 'forbidden'));
  const published = await bob.operate('typed', 'transition', { to: 'published' });
  assert.deepEqual([published.result.to, published.seq], ['published', 3]);
  await assert.rejects(bob.transition('typed', { to: 'review' }), (error) => isNoteVeto(error, 'Workflow', 'transition_not_allowed'));

  const proposal = await alice.propose('typed', { patch: { body: 'Typed end to end.' }, note: 'Say what it is.' });
  assert.equal(proposal.state, 'pending');
  assert.equal((await bob.approve('typed', { proposal: proposal.id })).state, 'approved');
  const read = await alice.get('typed');
  assert.deepEqual([read.data.body, read.data.revision], ['Typed end to end.', 2]);
});

test('a draft that would break the stored notes is refused', async () => {
  // Only the schema owner may define a draft.
  assert.equal((await api('alice-token', 'POST', '/schemas', notesSchema)).status, 403);

  const withOwner = structuredClone(notesSchema) as { types: { Note: { fields: unknown[] } } };
  withOwner.types.Note.fields.push({ name: 'owner', typeRef: { name: 'string' }, required: true });
  const refused = await api('dana-token', 'POST', '/schemas', withOwner);
  assert.equal(refused.status, 409);
  assert.equal(refused.body.code, 'incompatible_change');
  assert.ok(refused.body.details.changes.length > 0);
});

test('a restart keeps every note, refuses an incompatible schema and publishes a compatible one', async () => {
  await server.close();
  engine.close();

  // Removing a field would drop what stored notes hold: the engine refuses
  // the version and openNotes throws, so the server would not start.
  const withoutTags = structuredClone(notesSchema) as { types: { Note: { fields: Array<{ name: string }> } } };
  withoutTags.types.Note.fields = withoutTags.types.Note.fields.filter((field) => field.name !== 'tags');
  assert.throws(() => openNotes(path, withoutTags), IncompatibleChangeError);

  // A new optional field is allowed, and becomes version 2.
  const withPinned = structuredClone(notesSchema) as { types: { Note: { fields: unknown[] } } };
  withPinned.types.Note.fields.push({ name: 'pinned', description: 'Whether the note shows first.', typeRef: { name: 'boolean' } });
  engine = openNotes(path, withPinned);
  server = await listen(notesApp(engine));

  const live = await api('bob-token', 'GET', '/schemas/notes');
  assert.equal(live.body.version, 2);
  const note = await api('bob-token', 'GET', '/schemas/notes/instances/launch');
  assert.equal(note.body.data.body, 'Ships on the first.');
  assert.equal(note.body.data.commentCount, 2);

  const pinned = await api('bob-token', 'PATCH', '/schemas/notes/instances/launch', { pinned: true }, { 'if-match': note.etag! });
  assert.equal(pinned.status, 200);
  assert.equal(pinned.body.version, 2);
  assert.equal(pinned.body.data.pinned, true);
  assert.equal(pinned.body.data.revision, 3);
});
