// The typed client (@superschematic/engine/client) against an engine
// served in process: engineApp mounted under /api, and the client's fetch
// handing each request to the app's fetch, with no network. Schemas,
// instances, operations and schema-level operations, preconditions, the
// typed problems and veto codes, the event log, the tools document, the
// behavior catalog and search, credentials and the SDKs' retry rule. The
// stream and the reconciler are in client-stream.test.ts.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { serviceAuthenticator, serviceUnauthorized, signedTokenSource, type ServiceAuthenticator } from '@superschematic/http-runtime';

import { EngineClient, EngineProblem, EngineTransportError, EngineVeto, isProblem, isVeto, type ServiceCredential } from '../dist/client/index.js';
import { holdDeclaration } from './behavior-fixtures.ts';
import { refused, serve, taskDocument } from './client-fixtures.ts';
import { alice, cleanup, orderDocument, schemaDocument } from './helpers.ts';

afterEach(cleanup);

describe('the client', () => {
  test('defines, publishes, lists, reads and describes schemas', async () => {
    const client = serve().client();
    const draft = await client.schemas.define(orderDocument());
    assert.deepEqual([draft.name, draft.version, draft.publishedAt], ['Order', null, null]);
    assert.equal('canonical' in draft, false);
    assert.deepEqual(await client.schemas.publish('Order'), { namespace: 'default', name: 'Order', version: 1, published: true });
    assert.deepEqual(
      (await client.schemas.list()).map((summary) => [summary.name, summary.liveVersion, summary.hasDraft]),
      [
        ['Order', 1, false],
        ['Task', 1, false],
      ]
    );
    assert.equal((await client.schemas.live('Order')).version, 1);
    assert.equal((await client.schemas.version('Order', 1)).hash, draft.hash);
    await refused(client.schemas.draft('Order'), 404, 'not_found');
    const described = await client.schemas.describe('Task');
    assert.equal(described.instanceType, 'Task');
    assert.deepEqual(
      described.behaviors.map((behavior) => behavior.name),
      ['Workflow', 'Dependencies', 'test.Counter', 'test.Hold']
    );
    assert.deepEqual(described.behaviors[3].vetoes.map((veto) => veto.code), ['stale', 'required', 'refused']);
  });

  test('creates with create parameters, reads, lists, updates against a sequence and deletes instances', async () => {
    const { client: open, requests } = serve();
    const client = open();
    const plan = await client.instances.create('Task', { title: 'Plan' }, { id: 'plan' });
    assert.deepEqual([plan.id, plan.seq, plan.createdBy, plan.data.status, plan.data.blocked], ['plan', 1, 'alice', 'todo', false]);
    const build = await client.instances.create<{ title: string; blocked: boolean }>('Task', { title: 'Build' }, { id: 'build', behaviors: { Dependencies: { blockers: [{ id: 'plan' }] } } });
    assert.equal(build.data.blocked, true);
    assert.equal(requests.at(-1)!.headers.get('content-type'), 'application/json');

    assert.deepEqual((await client.instances.get('Task', 'build')).data.title, 'Build');
    const first = await client.instances.list('Task', { limit: 1 });
    assert.deepEqual([first.items.map((item) => item.id), typeof first.next], [['plan'], 'string']);
    const second = await client.instances.list('Task', { limit: 1, cursor: first.next as string });
    assert.deepEqual(second.items.map((item) => item.id), ['build']);

    const updated = await client.instances.update('Task', 'plan', { title: 'Plan it' }, { expectedSeq: 1 });
    assert.deepEqual([updated.data.title, updated.seq], ['Plan it', 2]);
    assert.equal(requests.at(-1)!.headers.get('if-match'), '"1"');
    assert.equal(requests.at(-1)!.headers.get('content-type'), 'application/merge-patch+json');
    const stale = await refused(client.instances.update('Task', 'plan', { title: 'Again' }, { expectedSeq: 1 }), 412, 'seq_mismatch');
    assert.equal(stale instanceof EngineVeto, false);

    await client.instances.delete('Task', 'build', { expectedSeq: 1 });
    await refused(client.instances.get('Task', 'build'), 404, 'not_found');
    await refused(client.instances.delete('Task', 'build'), 404, 'not_found');
  });

  test('turns refusals into typed problems with their issues at their paths', async () => {
    const served = serve();
    const client = served.client();
    await client.instances.create('Task', { title: 'Plan' }, { id: 'plan' });

    const invalid = await refused(client.instances.create('Task', { title: 7 }), 422, 'invalid_instance');
    assert.deepEqual(invalid.issues.map((issue) => [issue.path, issue.rule]), [['title', 'type']]);
    const readOnly = await refused(client.instances.update('Task', 'plan', { status: 'done' }), 422, 'invalid_instance');
    assert.deepEqual(readOnly.issues.map((issue) => [issue.path, issue.rule]), [['status', 'readOnly']]);

    const params = await refused(client.instances.create('Task', { title: 'X' }, { behaviors: { Dependencies: { blockers: [{ id: '-' }] } } }), 400, 'invalid_argument');
    assert.deepEqual(params.issues.map((issue) => issue.path), ['/behaviors/Dependencies/blockers/0/id']);
    const operation = await refused(client.instances.invoke('Task', 'plan', 'increment', { by: 0 }), 400, 'invalid_argument');
    assert.deepEqual(operation.issues.map((issue) => issue.path), ['/by']);

    const document = await refused(client.schemas.define({ kind: 'General' }), 422, 'invalid_schema');
    assert.ok(document.issues.length > 0);
    const widened = taskDocument([{ name: 'owner', typeRef: { name: 'string' }, required: true }]);
    const incompatible = await refused(client.schemas.define(widened), 409, 'incompatible_change');
    assert.deepEqual(incompatible.changes.map((change) => change.path), ['Task.owner']);

    await refused(served.client({ auth: { token: 'reader' } }).instances.create('Task', { title: 'Mine' }), 403, 'forbidden');
    await refused(served.client({ auth: {} }).schemas.list(), 401, 'unauthorized');
    const unknown = await refused(client.schemas.list({ namespace: 'elsewhere' }), 404, 'unknown_namespace');
    assert.equal(typeof unknown.requestId, 'string');
  });

  test('carries a veto\'s behavior, code and details, and sends preconditions in their header', async () => {
    const { client: open, requests } = serve();
    const client = open();
    await client.instances.create('Task', { title: 'Plan' }, { id: 'plan' });
    await client.instances.create('Task', { title: 'Build' }, { id: 'build', behaviors: { Dependencies: { blockers: [{ id: 'plan' }] } } });
    await client.instances.invoke('Task', 'build', 'transition', { to: 'doing' });

    const blocked = await refused<EngineVeto>(client.instances.invoke('Task', 'build', 'transition', { to: 'done' }), 409, 'vetoed');
    assert.ok(blocked instanceof EngineVeto);
    assert.deepEqual([blocked.behavior, blocked.action, blocked.vetoCode], ['Dependencies', 'transition', 'blocked']);
    assert.ok(isVeto(blocked, 'Dependencies', 'blocked'));
    assert.ok(isVeto(blocked, 'Dependencies', ['cycle', 'blocked']));
    assert.equal(isVeto(blocked, 'Lease'), false);
    assert.equal(isProblem(blocked, 'vetoed'), true);
    const notAllowed = await refused<EngineVeto>(client.instances.invoke('Task', 'plan', 'transition', { to: 'done' }), 409, 'vetoed');
    assert.deepEqual([notAllowed.behavior, notAllowed.vetoCode], ['Workflow', 'transition_not_allowed']);

    // test.Hold fences writes by a generation presented as its precondition.
    await client.instances.invoke('Task', 'plan', 'advance');
    const stale = await refused<EngineVeto>(client.instances.update('Task', 'plan', { title: 'Plan it' }, { preconditions: { 'test.Hold': { generation: 0 } } }), 409, 'vetoed');
    assert.deepEqual([stale.behavior, stale.action, stale.vetoCode, stale.vetoDetails], ['test.Hold', 'update', 'stale', { generation: 0, current: 1 }]);
    assert.equal(requests.at(-1)!.headers.get('preconditions'), '{"test.Hold":{"generation":0}}');
    const fenced = await client.instances.update('Task', 'plan', { title: 'Plan it' }, { preconditions: { 'test.Hold': { generation: 1 } } });
    assert.equal(fenced.data.title, 'Plan it');
    const outcome = await client.instances.operate<{ count: number }>('Task', 'plan', 'increment', {}, { preconditions: { 'test.Hold': { generation: 1 } }, expectedSeq: fenced.seq });
    assert.deepEqual(outcome, { result: { count: 1 }, seq: fenced.seq + 1 });
    const malformed = await refused(client.instances.invoke('Task', 'plan', 'increment', {}, { preconditions: { 'test.Hold': { generation: -1 } } }), 400, 'invalid_argument');
    assert.deepEqual(malformed.issues.map((issue) => issue.path), ['/test.Hold/generation']);
    const unknown = await refused(client.instances.delete('Task', 'plan', { preconditions: { Lease: { token: 1 } } }), 400, 'invalid_argument');
    assert.deepEqual(unknown.issues.map((issue) => issue.path), ['/Lease']);
    await client.instances.delete('Task', 'plan', { preconditions: { 'test.Hold': { generation: 1 } } });
  });

  test('reads the event log after a cursor and from the head, filtered', async () => {
    const client = serve().client();
    await client.instances.create('Task', { title: 'Plan' }, { id: 'plan' });
    await client.instances.invoke('Task', 'plan', 'increment');
    await client.instances.invoke('Task', 'plan', 'increment');
    const all = await client.events.read({ after: 0 });
    assert.deepEqual(
      all.events.map((event) => event.kind),
      ['define', 'publish', 'create', 'operation', 'operation']
    );
    assert.equal(all.more, false);
    const head = await client.events.head();
    assert.equal(head, all.next);
    assert.deepEqual(await client.events.read({ after: 'head' }), { events: [], next: head, more: false });
    const kept = await client.events.read({ after: 0, schema: 'Task', kinds: ['create', 'operation'], exclude: ['increment'] });
    assert.deepEqual(kept.events.map((event) => event.kind), ['create']);
    const paged = await client.events.read({ after: 0, limit: 2 });
    assert.deepEqual([paged.events.length, paged.more, paged.next], [2, true, 2]);
    await refused(client.events.read({ behaviors: ['not a name'] }), 400, 'invalid_argument');
  });

  test('calls schema-level operations, searches, and reads the tools document and the behavior catalog', async () => {
    const served = serve();
    const { engine } = served;
    const client = served.client();
    const notes = schemaDocument('Note', [
      { name: 'title', typeRef: { name: 'string' }, required: true },
      { name: 'body', typeRef: { name: 'string' } },
    ]) as { types: { Note: Record<string, unknown> } };
    notes.types.Note.behaviors = [{ name: 'Search', config: { fields: ['title', 'body'] } }];
    engine.schemas.define(alice, notes as unknown as Record<string, unknown>);
    engine.schemas.publish(alice, 'Note');
    await client.instances.create('Note', { title: 'A standing desk', body: 'Oak.' }, { id: 'desk' });
    await client.instances.create('Note', { title: 'A chair' }, { id: 'chair' });

    const local = await client.instances.invokeSchema<{ items: Array<{ id: string }> }>('Note', 'search', { query: 'desk' });
    assert.deepEqual(local.items.map((hit) => hit.id), ['desk']);
    const across = await client.search({ query: 'chair' });
    assert.deepEqual(across.items.map((hit) => [hit.schema, hit.id]), [['Note', 'chair']]);

    const tools = await client.tools();
    assert.ok(tools.tools.some((tool) => tool.name === 'engine.listSchemas'));
    assert.ok(tools.tools.some((tool) => tool.name === 'task.increment'));

    const behaviors = await client.behaviors.list();
    assert.ok(behaviors.some((behavior) => behavior.name === 'Workflow' && behavior.operations.includes('transition')));
    const hold = await client.behaviors.describe('test.Hold');
    assert.deepEqual(hold.preconditionSchema, holdDeclaration.preconditionSchema);
    await refused(client.behaviors.describe('test.Missing'), 404, 'not_found');
  });

  test('a caller\'s abort rejects with its reason, and an answer that does not come is a transport error', async () => {
    const client = serve().client();
    const controller = new AbortController();
    controller.abort(new Error('changed my mind'));
    await assert.rejects(client.schemas.list({ signal: controller.signal }), /changed my mind/u);
    const silent = new EngineClient({ baseUrl: 'http://engine.test', timeoutMs: 20, fetch: (_input, init) => new Promise((_resolve, reject) => init.signal?.addEventListener('abort', () => reject(init.signal?.reason))) });
    const timedOut = await silent.schemas.list().then(
      () => assert.fail('expected a timeout'),
      (error: unknown) => error
    );
    assert.ok(timedOut instanceof EngineTransportError && timedOut.timedOut);
    const down = new EngineClient({ baseUrl: 'http://engine.test', fetch: async () => Promise.reject(new TypeError('connection refused')) });
    await assert.rejects(down.schemas.list(), (error: unknown) => error instanceof EngineTransportError && !error.timedOut && /connection refused/u.test(error.message));
    // A proxy's page is a problem with the status and no code.
    const proxied = new EngineClient({ baseUrl: 'http://engine.test', fetch: async () => new Response('<html>Bad gateway</html>', { status: 502, statusText: 'Bad Gateway' }) });
    await assert.rejects(proxied.schemas.list(), (error: unknown) => error instanceof EngineProblem && error.status === 502 && error.code === undefined && error.title === 'Bad Gateway');
  });
});

describe('credentials', () => {
  // A service credential is accepted when it is `good`; any other does not verify.
  const authenticateService: ServiceAuthenticator = async (ctx) => {
    const header = ctx.headers.get('service-authorization');
    if (header === null) return null;
    if (header !== 'Bearer good') throw serviceUnauthorized();
    return { deployable: 'worker', serves: [], subject: 'sa-worker' };
  };

  /** source records each ask and answers a cached stale token until asked for a fresh one. */
  function source(fresh = 'good', cached = 'stale'): ServiceCredential & { asked: boolean[] } {
    const asked: boolean[] = [];
    return {
      asked,
      token: async (wantsFresh) => {
        asked.push(wantsFresh);
        return wantsFresh ? fresh : cached;
      },
    };
  }

  test('a service_unauthorized 401 asks the service source for a fresh token once and never runs the end-user refresh', async () => {
    const { client, requests } = serve({ authenticateService });
    const refreshes: string[] = [];
    const credential = source();
    const worker = client({ auth: { token: 'alice', refreshToken: async () => (refreshes.push('refresh'), 'alice') }, serviceCredential: credential });
    assert.equal((await worker.schemas.list()).length, 1);
    assert.deepEqual(credential.asked, [false, true]);
    assert.equal(refreshes.length, 0);
    assert.deepEqual(
      requests.map((request) => request.headers.get('service-authorization')),
      ['Bearer stale', 'Bearer good']
    );

    // A fresh token that is refused too ends the call: one retry, no loop.
    const refusedTwice = source('stale', 'stale');
    const stuck = client({ auth: { token: 'alice', refreshToken: async () => (refreshes.push('refresh'), 'alice') }, serviceCredential: refusedTwice });
    await refused(stuck.schemas.list(), 401, 'service_unauthorized');
    assert.deepEqual(refusedTwice.asked, [false, true]);
    assert.equal(refreshes.length, 0);
  });

  test('an end user\'s 401 runs the refresh once, shared by concurrent calls, and never asks the service source for a fresh token', async () => {
    const { client, requests } = serve({ authenticateService });
    const refreshes: string[] = [];
    const credential = source('good', 'good');
    const expired = client({ auth: { token: 'expired', refreshToken: async () => (refreshes.push('refresh'), 'alice') }, serviceCredential: credential });
    const [schemas, again] = await Promise.all([expired.schemas.list(), expired.schemas.list()]);
    assert.deepEqual([schemas.length, again.length], [1, 1]);
    assert.deepEqual(refreshes, ['refresh']);
    assert.ok(credential.asked.every((fresh) => fresh === false));
    assert.deepEqual(
      requests.map((request) => request.headers.get('authorization')),
      ['Bearer expired', 'Bearer expired', 'Bearer alice', 'Bearer alice']
    );
    // The refreshed token is held from then on.
    await expired.schemas.list();
    assert.equal(requests.at(-1)!.headers.get('authorization'), 'Bearer alice');

    // A refresh whose token is refused too ends the call.
    const hopeless = client({ auth: { token: 'expired', refreshToken: async () => (refreshes.push('again'), 'still-expired') } });
    await refused(hopeless.schemas.list(), 401, 'unauthorized');
    assert.deepEqual(refreshes, ['refresh', 'again']);
  });

  test('a forwarded end user is sent as is, never refreshed, and a forward with no user lets the service stand in', async () => {
    const { client, requests, engine } = serve({ authenticateService });
    const refreshes: string[] = [];
    const worker = client({ auth: { token: 'alice', refreshToken: async () => (refreshes.push('refresh'), 'alice') }, serviceCredential: { token: async () => 'good' } });
    const forBob = await worker.instances.create('Task', { title: 'For bob' }, { forward: { bearerToken: 'bob' } });
    assert.equal(forBob.createdBy, 'bob');
    assert.equal(requests.at(-1)!.headers.get('authorization'), 'Bearer bob');
    const alone = await worker.instances.create('Task', { title: 'Alone' }, { forward: {} });
    assert.equal(alone.createdBy, 'service:worker');
    assert.equal(requests.at(-1)!.headers.get('authorization'), null);
    await refused(worker.schemas.list({ forward: { bearerToken: 'nobody' } }), 401, 'unauthorized');
    assert.equal(refreshes.length, 0);
    const [event] = engine.events.read(alice, { schema: 'Task', instanceId: alone.id }).events;
    assert.deepEqual([event.actor, event.service], ['service:worker', 'worker']);
  });

  test('the HTTP runtime\'s signed token source fills the service credential, and the service stands in for an end user', async () => {
    const pair = (await crypto.subtle.generateKey({ name: 'Ed25519' }, true, ['sign', 'verify'])) as { privateKey: Parameters<typeof crypto.subtle.exportKey>[1] };
    const jwk = (await crypto.subtle.exportKey('jwk', pair.privateKey)) as { d: string; x: string };
    const key = { kty: 'OKP', crv: 'Ed25519', d: jwk.d, x: jwk.x, kid: 'edge-1' } as const;
    const verify = serviceAuthenticator({
      issuers: [
        {
          issuer: 'indexer',
          audience: 'jobs-engine',
          algorithms: ['EdDSA'],
          keys: [{ kty: 'OKP', crv: 'Ed25519', x: key.x, kid: key.kid }],
          maxLifetimeSeconds: 300,
          callers: { indexer: { deployable: 'indexer', serves: [] } },
        },
      ],
    });
    const { client } = serve({ authenticateService: verify });
    const credential: ServiceCredential = { token: signedTokenSource(key, { issuer: 'indexer', subject: 'indexer', audience: 'jobs-engine' }) };
    const indexer = client({ auth: {}, serviceCredential: credential });
    const created = await indexer.instances.create('Task', { title: 'Index' });
    assert.equal(created.createdBy, 'service:indexer');
  });

  test('setToken and clearToken replace the token the client holds; getToken answers while it holds none', async () => {
    const { client, requests } = serve();
    let asked = 0;
    const user = client({ auth: { getToken: async () => (asked++, 'bob') } });
    await user.schemas.list();
    user.setToken('alice');
    await user.schemas.list();
    user.clearToken();
    await user.schemas.list();
    assert.deepEqual(
      requests.map((request) => request.headers.get('authorization')),
      ['Bearer bob', 'Bearer alice', 'Bearer bob']
    );
    assert.equal(asked, 2);
  });
});
