// The core user model in the engine (D50): an engine over SQLite whose file
// holds the authDb's identity tables too, served with engineApp's identity
// option. Users sign in under /auth; permissionPolicy allows what their
// roles grant, a Workflow transition's permission comes from a role, and
// capabilities answers per live schema and action. Then the refusals:
// identity beside authenticate, a malformed bearer header, the cross-origin
// check, and engineMcp authenticating with the same service.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { afterEach, describe, test } from 'node:test';

import { hashPassword, type IdentityService, type SqlIdentityStore } from '@superschematic/http-runtime/identity';
import { Hono } from 'hono';

import { permissionPolicy, type AccessPolicy, type Engine, type Principal } from '../dist/index.js';
import { engineApp, engineCapabilities, engineIdentity, engineIdentityStore, type EngineHttpOptions } from '../dist/http/index.js';
import { engineMcp } from '../dist/mcp/index.js';
import { cleanup, drivers, openTestEngine, schemaDocument } from './helpers.ts';

afterEach(cleanup);

// The identity fixture the HTTP runtime's stores run against: the
// descriptor and the SQLite DDL of fixture-user-model-db.
const fixture = new URL('../../../http/testdata/identity/', import.meta.url);
const descriptor = readFileSync(new URL('fixture-user-model-db.json', fixture), 'utf8');
const ddl = readFileSync(new URL('sqlite/create.sql', fixture), 'utf8');

/** A config at a low argon2 cost, so a test hashes quickly, and the origin a browser app is served from. */
const APP_ORIGIN = 'https://app.example.com';
const CONFIG = { password: { argon2: { memoryKiB: 64, iterations: 1, parallelism: 1 } }, trustedOrigins: [APP_ORIGIN] };
const PASSWORD = 'correct horse';

// A task moves from todo to doing, and only a reviewer moves it to done.
const taskDocument = (() => {
  const document = schemaDocument('Task', [{ name: 'title', typeRef: { name: 'string' }, required: true }]) as {
    types: { Task: Record<string, unknown> };
  };
  document.types.Task.behaviors = [
    {
      name: 'Workflow',
      config: {
        states: ['todo', 'doing', 'done'],
        transitions: [
          { from: 'todo', to: 'doing' },
          { from: 'doing', to: 'done', permission: 'tasks.review' },
        ],
      },
    },
  ];
  return document;
})();

interface Served {
  engine: Engine;
  store: SqlIdentityStore;
  identity: IdentityService;
  app: Hono;
  /** Role ids by name. */
  roles: Record<string, string>;
  /** User ids by login. */
  users: Record<string, string>;
}

/**
 * An engine whose file holds the identity tables, with three users: ada,
 * who may do anything with Task (`Task`) and manage users and roles
 * (`identity`); rob, who may read Task; and rae, who holds no role. The
 * reviewer role, which may review tasks, is granted later.
 */
async function serve(driver: (typeof drivers)[number], options: EngineHttpOptions = {}, policy: AccessPolicy = permissionPolicy()): Promise<Served> {
  const engine = openTestEngine({ driver, policy, namespaces: { names: ['east'] } });
  engine.storage.exec(ddl);
  const store = engineIdentityStore(engine, descriptor);
  const identity = engineIdentity(engine, { store, config: CONFIG });
  const hash = await hashPassword(PASSWORD, identity.config.password.argon2);
  const users: Record<string, string> = {};
  for (const [login, name] of [['ada@example.com', 'Ada'], ['rob@example.com', 'Rob'], ['rae@example.com', 'Rae']]) {
    users[login!] = (await store.createUser({ login: login!, name: name!, passwordHash: hash, at: new Date() })).id;
  }
  const roles: Record<string, string> = {};
  for (const [name, permissions] of [
    ['author', ['Task', 'identity']],
    ['reader', ['Task.read']],
    ['reviewer', ['Task.read', 'tasks.review']],
  ] as const) {
    roles[name] = (await store.createRole(name, [...permissions])).id;
  }
  await store.grantRole(users['ada@example.com']!, roles.author!, new Date());
  await store.grantRole(users['rob@example.com']!, roles.reader!, new Date());
  const app = new Hono();
  app.route('/api', engineApp(engine, { identity, ...options }));
  return { engine, store, identity, app, roles, users };
}

interface Reply {
  status: number;
  headers: Headers;
  body: Record<string, any>;
}

async function call(app: Hono, method: string, path: string, options: { token?: string; body?: unknown; headers?: Record<string, string> } = {}): Promise<Reply> {
  const headers = new Headers(options.headers);
  if (options.token !== undefined) {
    headers.set('authorization', `Bearer ${options.token}`);
  }
  let body: string | undefined;
  if (options.body !== undefined) {
    body = JSON.stringify(options.body);
    headers.set('content-type', 'application/json');
  }
  const response = await app.request(`http://engine.example.com/api${path}`, { method, headers, body });
  const text = await response.text();
  // A path no route has is the mounting app's own 404, which is text.
  return { status: response.status, headers: response.headers, body: /json/u.test(response.headers.get('content-type') ?? '') ? JSON.parse(text) : { text } };
}

/** The status and code of a reply, or its status and data. */
const outcome = (reply: Reply) => [reply.status, reply.body.code ?? reply.body.data];

async function login(served: Served, login: string): Promise<string> {
  const reply = await call(served.app, 'POST', '/auth/login', { body: { login, password: PASSWORD } });
  assert.equal(reply.status, 200, JSON.stringify(reply.body));
  return reply.body.data.token;
}

const TASKS = '/namespaces/default/schemas/Task/instances';

for (const driver of drivers) {
  describe(`the user model in the engine (${driver})`, () => {
    test('a user signs in, defines and publishes a schema, creates an instance, transitions it, reads capabilities and signs out', async () => {
      const served = await serve(driver);
      const ada = await login(served, 'ada@example.com');
      const me = await call(served.app, 'GET', '/auth/me', { token: ada });
      assert.deepEqual(me.body.data, {
        user: { id: served.users['ada@example.com'], login: 'ada@example.com', name: 'Ada' },
        roles: [{ id: served.roles.author, name: 'author' }],
        permissions: ['Task', 'identity'],
      });

      // permissionPolicy allows what the role grants: Task covers
      // Task.define, Task.publish and Task.write.
      assert.equal((await call(served.app, 'POST', '/namespaces/default/schemas', { token: ada, body: taskDocument })).status, 200);
      assert.equal((await call(served.app, 'POST', '/namespaces/default/schemas/Task/publish', { token: ada })).status, 200);
      const created = await call(served.app, 'POST', TASKS, { token: ada, body: { id: 't1', data: { title: 'Write the docs' } } });
      assert.equal(created.status, 201, JSON.stringify(created.body));
      assert.deepEqual([created.body.data.createdBy, created.body.data.data.status], [served.users['ada@example.com'], 'todo']);

      const rob = await login(served, 'rob@example.com');
      assert.deepEqual(outcome(await call(served.app, 'POST', TASKS, { token: rob, body: { data: { title: 'Not his' } } })), [403, 'forbidden']);
      assert.equal((await call(served.app, 'GET', `${TASKS}/t1`, { token: rob })).status, 200);

      // A Workflow transition's permission is a role's like any other.
      const transition = (token: string, to: string) => call(served.app, 'POST', `${TASKS}/t1/operations/transition`, { token, body: { to } });
      assert.deepEqual(outcome(await transition(ada, 'doing')), [200, { from: 'todo', to: 'doing' }]);
      const rae = await login(served, 'rae@example.com');
      // rae holds no role: permissionPolicy refuses her the write.
      assert.deepEqual(outcome(await transition(rae, 'done')), [403, 'forbidden']);
      // ada may write Task, but the transition needs tasks.review.
      const refused = await transition(ada, 'done');
      assert.deepEqual(outcome(refused), [403, 'forbidden']);
      assert.match(refused.body.detail, /the transition needs permission tasks\.review/);
      // A grant reaches the next request of a session already signed in.
      const granted = await call(served.app, 'PUT', `/auth/admin/users/${served.users['rae@example.com']}/roles/${served.roles.reviewer}`, { token: ada });
      assert.equal(granted.status, 403, 'ada does not hold tasks.review, so she may not grant it');
      await served.store.grantRole(served.users['ada@example.com']!, served.roles.reviewer!, new Date());
      assert.deepEqual(outcome(await transition(ada, 'done')), [200, { from: 'doing', to: 'done' }]);

      // Capabilities: each live schema the caller may read, by namespace,
      // schema and action.
      assert.deepEqual((await call(served.app, 'GET', '/auth/capabilities', { token: ada })).body.data, {
        operations: { 'default/Task.define': true, 'default/Task.publish': true, 'default/Task.read': true, 'default/Task.write': true },
      });
      assert.deepEqual((await call(served.app, 'GET', '/auth/capabilities', { token: rob })).body.data, {
        operations: { 'default/Task.define': false, 'default/Task.publish': false, 'default/Task.read': true, 'default/Task.write': false },
      });
      assert.deepEqual((await call(served.app, 'GET', '/auth/capabilities', { token: rae })).body.data, { operations: {} });

      assert.deepEqual(outcome(await call(served.app, 'POST', '/auth/logout', { token: ada })), [200, true]);
      assert.deepEqual(outcome(await call(served.app, 'GET', '/auth/me', { token: ada })), [401, 'unauthorized']);
      assert.deepEqual(outcome(await call(served.app, 'GET', `${TASKS}/t1`, { token: ada })), [401, 'unauthorized']);
    });

    test('the session routes: the administration routes when the store has roles, register when asked, the contract rate limits', async () => {
      const served = await serve(driver, { rateLimit: { now: () => 0 } });
      const ada = await login(served, 'ada@example.com');
      const roles = await call(served.app, 'GET', '/auth/admin/roles', { token: ada });
      assert.deepEqual(roles.body.data.map((role: { name: string }) => role.name), ['author', 'reader', 'reviewer']);
      assert.equal((await call(served.app, 'POST', '/auth/register', { body: { login: 'new@example.com', password: PASSWORD } })).status, 404);
      for (let i = 1; i < 10; i++) {
        await call(served.app, 'POST', '/auth/login', { body: { login: 'ada@example.com', password: 'wrong password' } });
      }
      const limited = await call(served.app, 'POST', '/auth/login', { body: { login: 'ada@example.com', password: PASSWORD } });
      assert.deepEqual([limited.status, limited.body.code], [429, 'too_many_requests']);

      const open = await serve(driver, { identityRoutes: { register: true, administration: false } });
      const registered = await call(open.app, 'POST', '/auth/register', { body: { login: 'New@Example.com', password: PASSWORD } });
      assert.deepEqual([registered.status, registered.body.data.user.login], [200, 'new@example.com']);
      const token = registered.body.data.token;
      assert.equal((await call(open.app, 'GET', '/auth/admin/roles', { token })).status, 404);
    });

    test('identity beside authenticate is refused, and a malformed bearer header is 401', async () => {
      const served = await serve(driver);
      const authenticate = async (): Promise<Principal | null> => null;
      assert.throws(() => engineApp(served.engine, { identity: served.identity, authenticate }), /engineApp: options.authenticate is refused beside options.identity/);
      assert.throws(() => engineMcp(served.engine, { identity: served.identity, authenticate }), /engineMcp: options.authenticate is refused beside options.identity/);
      // The identity service reads the header itself, through the engine's
      // wrapper: hono/bearer-auth would answer this one 400.
      assert.deepEqual(outcome(await call(served.app, 'GET', '/namespaces/default/schemas', { headers: { authorization: 'Bearer' } })), [401, 'unauthorized']);
      assert.deepEqual(outcome(await call(served.app, 'GET', '/namespaces/default/schemas')), [401, 'unauthorized']);
    });

    test('a cookie session: the engine routes read the cookie, and the cross-origin check refuses another origin', async () => {
      const served = await serve(driver);
      const browser = { origin: APP_ORIGIN, 'sec-fetch-site': 'cross-site' };
      const evil = { origin: 'https://evil.example', 'sec-fetch-site': 'cross-site' };
      const body = { login: 'ada@example.com', password: PASSWORD, session: 'cookie' };
      assert.deepEqual(outcome(await call(served.app, 'POST', '/auth/login', { body, headers: evil })), [403, 'cross_origin']);
      const signedIn = await call(served.app, 'POST', '/auth/login', { body, headers: browser });
      assert.equal(signedIn.status, 200);
      assert.equal(signedIn.body.data.token, undefined);
      assert.equal(signedIn.headers.get('access-control-allow-origin'), APP_ORIGIN);
      const cookie = signedIn.headers.getSetCookie()[0]!.split('; ')[0]!;
      assert.match(cookie, /^__Host-session=/);

      const schemas = await call(served.app, 'GET', '/namespaces/default/schemas', { headers: { ...browser, cookie } });
      assert.deepEqual([schemas.status, schemas.headers.get('access-control-allow-credentials')], [200, 'true']);
      assert.deepEqual(outcome(await call(served.app, 'POST', '/namespaces/default/schemas', { body: taskDocument, headers: { ...evil, cookie } })), [403, 'cross_origin']);
      assert.equal((await call(served.app, 'POST', '/namespaces/default/schemas', { body: taskDocument, headers: { ...browser, cookie } })).status, 200);
    });

    test('engineMcp authenticates with the identity service', async () => {
      const served = await serve(driver);
      served.app.route('/api', engineMcp(served.engine, { identity: served.identity }));
      const initialize = (headers: Record<string, string>) =>
        served.app.request('http://engine.example.com/api/namespaces/default/mcp', {
          method: 'POST',
          headers: { 'content-type': 'application/json', accept: 'application/json, text/event-stream', ...headers },
          body: JSON.stringify({
            jsonrpc: '2.0',
            id: 1,
            method: 'initialize',
            params: { protocolVersion: '2025-06-18', capabilities: {}, clientInfo: { name: 'test', version: '1' } },
          }),
        });
      assert.equal((await initialize({})).status, 401);
      assert.equal((await initialize({ authorization: 'Bearer' })).status, 401);
      const ada = await login(served, 'ada@example.com');
      assert.equal((await initialize({ authorization: `Bearer ${ada}` })).status, 200);
    });
  });
}

describe('engineCapabilities', () => {
  test('lists each live schema a namespace reaches that the caller may read, its keys in code-point order', () => {
    // The owner may do anything; the viewer may read everything but east's
    // Secret, and write only in east.
    const policy: AccessPolicy = ({ principal, action, namespace, schema }) =>
      principal.subject === 'owner' ||
      (!(namespace === 'east' && schema === 'Secret') && (action === 'read' || (action === 'write' && namespace === 'east')));
    const engine = openTestEngine({ policy, namespaces: { names: ['east', 'common'], shared: 'common' } });
    const owner: Principal = { subject: 'owner', permissions: [] };
    const note = (name: string) => schemaDocument(name, [{ name: 'body', typeRef: { name: 'string' } }]);
    for (const [name, namespace] of [['Note', 'default'], ['Secret', 'east'], ['Shared', 'common']]) {
      engine.schemas.define(owner, note(name!), { namespace });
      engine.schemas.publish(owner, name!, { namespace });
    }
    // A draft that was never published has no live version.
    engine.schemas.define(owner, note('Draft'));

    const answer = engineCapabilities(engine, { subject: 'viewer', permissions: [] });
    const actions = (namespace: string, schema: string, write: boolean) => ({
      [`${namespace}/${schema}.define`]: false,
      [`${namespace}/${schema}.publish`]: false,
      [`${namespace}/${schema}.read`]: true,
      [`${namespace}/${schema}.write`]: write,
    });
    assert.deepEqual(answer, {
      ...actions('common', 'Shared', false),
      ...actions('default', 'Note', false),
      ...actions('default', 'Shared', false),
      ...actions('east', 'Shared', true),
    });
    assert.deepEqual(Object.keys(answer), Object.keys(answer).sort());
    // default and east reach common's Shared too: five schemas, four actions each.
    assert.equal(Object.keys(engineCapabilities(engine, owner)).length, 5 * 4);
  });
});

describe('permissionPolicy', () => {
  const request = (permissions: string[], action: 'read' | 'write' | 'define' | 'publish', schema = 'Task') => ({
    principal: { subject: 'u', permissions },
    action,
    namespace: 'default',
    schema,
  });

  test('allows an action to a caller holding <schema>.<action>, or a permission that covers it', () => {
    const policy = permissionPolicy();
    assert.equal(policy(request(['Task.write'], 'write')), true);
    assert.equal(policy(request(['Task'], 'publish')), true);
    assert.equal(policy(request(['Task.read'], 'write')), false);
    assert.equal(policy(request(['Tas'], 'read')), false);
    assert.equal(policy(request(['Task.write'], 'write', 'Note')), false);
    assert.equal(policy(request([], 'read')), false);
    assert.equal(permissionPolicy({ permissionMatcher: () => true })(request([], 'define')), true);
    assert.throws(() => permissionPolicy({ permissionMatcher: 'root' as never }), /permissionMatcher is a function/);
  });

  test("in an engine it matches with the engine's permissionMatcher, unless it was given its own", () => {
    const rooted = (held: readonly string[]) => held.includes('root');
    const root: Principal = { subject: 'root', permissions: ['root'] };
    const author: Principal = { subject: 'author', permissions: ['Note'] };
    const note = schemaDocument('Note', [{ name: 'body', typeRef: { name: 'string' } }]);

    const engine = openTestEngine({ policy: permissionPolicy(), permissionMatcher: rooted });
    engine.schemas.define(root, note);
    assert.throws(() => engine.schemas.define(author, note), /author may not define Note/);

    const own = openTestEngine({ policy: permissionPolicy({ permissionMatcher: () => false }), permissionMatcher: rooted });
    assert.throws(() => own.schemas.define(root, note), /root may not define Note/);

    const plain = openTestEngine({ policy: permissionPolicy() });
    plain.schemas.define(author, note);
    assert.throws(() => plain.schemas.define(root, note), /root may not define Note/);
  });
});
