import { afterEach, describe, expect, test } from 'bun:test';
import { Hono } from 'hono';
import { mountOperation } from '../hono';
import { mountMcp } from '../mcp';
import type { OperationSpec } from '../operation';
import {
  ClientMetadataError,
  IdentityService,
  OAUTH_ACCESS_TOKEN_PREFIX,
  OAUTH_REFRESH_TOKEN_PREFIX,
  codeChallengeOf,
  hashPassword,
  identityAuthenticator,
  mountIdentityRoutes,
  mountOAuthRoutes,
  oauthChallenge,
  type ClientMetadata,
  type IdentityRoute,
  type SqlIdentityStore,
} from './index';
import { MINUTE, at, databaseKinds, type TestDatabase } from './fixture.testing';

/*
The OAuth authorization server (#695) end to end, on each database the
store tests run on: discovery, an authorization through sign-in and
consent, the code's exchange, a call with the access token, refresh
rotation and reuse, a replayed code, revocation, registration, and the MCP
endpoint's 401.
*/

const PASSWORD = 'correct horse battery';
const CONFIG = {
  cookie: { secure: false },
  password: { argon2: { memoryKiB: 64, iterations: 1, parallelism: 1 } },
  oauth: { publicBaseUrl: 'https://app.example.com/api/notes' },
};
const BASE = 'https://app.example.com/api/notes';
const RESOURCE = `${BASE}/api/mcp`;
const CLAUDE_CODE = 'https://claude.ai/oauth/claude-code-client-metadata';
const CALLBACK = 'http://localhost:51234/callback';

const ROUTES: IdentityRoute[] = [
  { operationId: 'NotesList', requiresAuth: true, permissions: ['notes.read'], requireOwnership: false, serviceOnly: false },
  { operationId: 'NotesCreate', requiresAuth: true, permissions: ['notes.write'], requireOwnership: false, serviceOnly: false },
];

const LIST_NOTES: OperationSpec = {
  name: 'listNotes',
  namespace: 'notes',
  method: 'GET',
  path: '/api/notes',
  pathParams: [],
  queryParams: [],
  bodyParams: [],
  auth: { public: false, required: true, permissions: ['notes.read'] },
  manual: false,
};
const CREATE_NOTE: OperationSpec = { ...LIST_NOTES, name: 'createNote', method: 'POST', auth: { public: false, required: true, permissions: ['notes.write'] } };

class Clock {
  now = at();
  advance(ms: number): void {
    this.now = new Date(this.now.getTime() + ms);
  }
}

const open: TestDatabase[] = [];
afterEach(async () => {
  while (open.length > 0) await open.pop()!.close();
});

interface Reply {
  status: number;
  headers: Headers;
  body: any;
}

interface Harness {
  store: SqlIdentityStore;
  service: IdentityService;
  clock: Clock;
  userId: string;
  roleId: string;
  fetched: string[];
  request(method: string, path: string, init?: { body?: string; type?: string; headers?: Record<string, string> }): Promise<Reply>;
  form(path: string, params: Record<string, string>): Promise<Reply>;
  json(method: string, path: string, body: unknown, bearer: string): Promise<Reply>;
  /** Runs an authorization through consent with the person's session, and answers the code and the verifier. */
  authorize(options?: { scope?: string; permissions?: string[]; clientId?: string; redirect?: string }): Promise<{ code: string; verifier: string; state: string }>;
  tokens(code: string, verifier: string, clientId?: string, redirect?: string): Promise<Reply>;
  session(): Promise<string>;
}

async function harness(db: TestDatabase, options: { registration?: boolean } = {}): Promise<Harness> {
  const store = db.store();
  const clock = new Clock();
  const fetched: string[] = [];
  const fetchMetadata = async (clientId: string): Promise<ClientMetadata> => {
    fetched.push(clientId);
    if (clientId !== CLAUDE_CODE) throw new ClientMetadataError('no such document');
    return { clientId, name: 'Claude Code', uri: 'https://claude.ai', redirectUris: ['http://localhost/callback', 'http://127.0.0.1/callback'], ttlSeconds: 3600 };
  };
  const service = new IdentityService({
    store,
    config: CONFIG,
    now: () => clock.now,
    routes: ROUTES,
    oauth: { fetchMetadata, clients: { registration: options.registration ?? true } },
    oauthAdministration: true,
  });
  const app = new Hono();
  mountIdentityRoutes(app, service, { sessions: {}, administration: {}, basePath: '/api' });
  mountOAuthRoutes(app, service);
  const runtime = { authenticate: identityAuthenticator(service) };
  mountOperation(app, LIST_NOTES, async ctx => ctx.principal?.permissions ?? null, runtime);
  mountOperation(app, CREATE_NOTE, async ctx => ctx.principal?.permissions ?? null, runtime);
  mountMcp(app, {}, [], runtime, { api: 'notes', version: '1', requiresAuth: true }, { challenge: oauthChallenge(service) });

  const params = service.config.password.argon2;
  const user = await store.createUser({ login: 'ada@example.com', name: 'Ada', passwordHash: await hashPassword(PASSWORD, params), at: clock.now });
  const role = await store.createRole('writer', ['notes']);
  await store.grantRole(user.id, role.id, clock.now);

  const h: Harness = {
    store,
    service,
    clock,
    userId: user.id,
    roleId: role.id,
    fetched,
    async request(method, path, init = {}) {
      const headers: Record<string, string> = { ...(init.headers ?? {}) };
      if (init.type) headers['content-type'] = init.type;
      const response = await app.request(`http://api.internal${path}`, { method, headers, ...(init.body !== undefined ? { body: init.body } : {}) });
      const text = await response.text();
      let body: any = text;
      if (/json/u.test(response.headers.get('content-type') ?? '')) body = JSON.parse(text);
      return { status: response.status, headers: response.headers, body };
    },
    form(path, params) {
      return h.request('POST', path, { body: new URLSearchParams(params).toString(), type: 'application/x-www-form-urlencoded' });
    },
    json(method, path, body, bearer) {
      return h.request(method, path, { ...(body !== undefined ? { body: JSON.stringify(body), type: 'application/json' } : {}), headers: { authorization: `Bearer ${bearer}` } });
    },
    async session() {
      const r = await h.json('POST', '/api/auth/login', { login: 'ada@example.com', password: PASSWORD }, '');
      expect(r.status).toBe(200);
      return r.body.data.token as string;
    },
    async authorize(o = {}) {
      const verifier = 'v'.repeat(43) + Math.random().toString(36).slice(2);
      const state = `state-${Math.random().toString(36).slice(2)}`;
      const query = new URLSearchParams({
        response_type: 'code',
        client_id: o.clientId ?? CLAUDE_CODE,
        redirect_uri: o.redirect ?? CALLBACK,
        code_challenge: codeChallengeOf(verifier),
        code_challenge_method: 'S256',
        state,
        resource: RESOURCE,
        ...(o.scope !== undefined ? { scope: o.scope } : {}),
      });
      const started = await h.request('GET', `/oauth/authorize?${query}`);
      expect(started.status).toBe(302);
      const consent = new URL(started.headers.get('location')!);
      expect(`${consent.origin}${consent.pathname}`).toBe(`${BASE}/oauth/consent`);
      const id = consent.searchParams.get('request')!;
      const session = await h.session();
      const read = await h.json('GET', `/api/auth/oauth/requests/${id}`, undefined, session);
      expect(read.status).toBe(200);
      const approved = await h.json('POST', `/api/auth/oauth/requests/${id}/approve`, { nonce: read.body.data.nonce, ...(o.permissions ? { permissions: o.permissions } : {}) }, session);
      expect(approved.status).toBe(200);
      const back = new URL(approved.body.data.redirectTo);
      expect(back.searchParams.get('state')).toBe(state);
      expect(back.searchParams.get('iss')).toBe(BASE);
      return { code: back.searchParams.get('code')!, verifier, state };
    },
    tokens(code, verifier, clientId = CLAUDE_CODE, redirect = CALLBACK) {
      return h.form('/oauth/token', { grant_type: 'authorization_code', code, code_verifier: verifier, redirect_uri: redirect, client_id: clientId, resource: RESOURCE });
    },
  };
  return h;
}

for (const kind of databaseKinds) {
  describe(`oauth on ${kind.name}`, () => {
    const setup = async (options?: { registration?: boolean }) => {
      const db = await kind.open();
      open.push(db);
      return harness(db, options);
    };

    test('discovery answers for the public base at the root and at the inserted paths', async () => {
      const h = await setup();
      for (const path of ['/.well-known/oauth-protected-resource', '/.well-known/oauth-protected-resource/api/notes/api/mcp']) {
        const r = await h.request('GET', path);
        expect(r.status).toBe(200);
        expect(r.body).toEqual({
          resource: RESOURCE,
          authorization_servers: [BASE],
          scopes_supported: ['notes.read', 'notes.write', 'offline_access'],
          bearer_methods_supported: ['header'],
        });
      }
      for (const path of ['/.well-known/oauth-authorization-server/api/notes', '/.well-known/openid-configuration']) {
        const r = await h.request('GET', path);
        expect(r.status).toBe(200);
        expect(r.body.issuer).toBe(BASE);
        expect(r.body.code_challenge_methods_supported).toEqual(['S256']);
        expect(r.body.token_endpoint_auth_methods_supported).toEqual(['none']);
        expect(r.body.client_id_metadata_document_supported).toBe(true);
        expect(r.body.registration_endpoint).toBe(`${BASE}/oauth/register`);
      }
      expect((await h.request('GET', '/.well-known/oauth-protected-resource/elsewhere')).status).toBe(404);
    });

    test('the MCP endpoint answers 401 with where to sign in', async () => {
      const h = await setup();
      const bare = await h.request('POST', '/api/mcp', { body: '{}', type: 'application/json' });
      expect(bare.status).toBe(401);
      expect(bare.headers.get('www-authenticate')).toBe(`Bearer resource_metadata="https://app.example.com/.well-known/oauth-protected-resource/api/notes/api/mcp"`);
      const bad = await h.request('POST', '/api/mcp', { body: '{}', type: 'application/json', headers: { authorization: `Bearer ${OAUTH_ACCESS_TOKEN_PREFIX}${'a'.repeat(43)}` } });
      expect(bad.status).toBe(401);
      expect(bad.headers.get('www-authenticate')).toContain('error="invalid_token"');
    });

    test('an authorization through consent gives tokens that call as the person, narrowed', async () => {
      const h = await setup();
      const { code, verifier } = await h.authorize({ scope: 'notes.read notes.write offline_access', permissions: ['notes.read'] });
      const r = await h.tokens(code, verifier);
      expect(r.status).toBe(200);
      expect(r.headers.get('cache-control')).toBe('no-store');
      expect(r.body.token_type).toBe('Bearer');
      expect(r.body.expires_in).toBe(3600);
      expect(r.body.scope).toBe('notes.read offline_access');
      expect(r.body.access_token.startsWith(OAUTH_ACCESS_TOKEN_PREFIX)).toBe(true);
      expect(r.body.refresh_token.startsWith(OAUTH_REFRESH_TOKEN_PREFIX)).toBe(true);
      const list = await h.json('GET', '/api/notes', undefined, r.body.access_token);
      expect(list.status).toBe(200);
      expect(list.body.data).toEqual(['notes.read']);
      const create = await h.json('POST', '/api/notes', {}, r.body.access_token);
      expect(create.status).toBe(403);
      // A token acts on nothing of the person's own account.
      expect((await h.json('GET', '/api/auth/oauth/grants', undefined, r.body.access_token)).status).toBe(403);
      // The role's loss counts on the next call.
      await h.store.revokeRole(h.userId, h.roleId);
      expect((await h.json('GET', '/api/notes', undefined, r.body.access_token)).status).toBe(403);
      expect(h.fetched).toEqual([CLAUDE_CODE]);
    });

    test('a request with no scope gives every permission the person holds, as they change', async () => {
      const h = await setup();
      const { code, verifier } = await h.authorize();
      const r = await h.tokens(code, verifier);
      expect(r.status).toBe(200);
      expect(r.body.scope).toBe('notes.read notes.write offline_access');
      expect((await h.json('POST', '/api/notes', {}, r.body.access_token)).status).toBe(200);
    });

    test('a code works once, and its replay revokes the grant', async () => {
      const h = await setup();
      const { code, verifier } = await h.authorize();
      const first = await h.tokens(code, verifier);
      expect(first.status).toBe(200);
      const again = await h.tokens(code, verifier);
      expect({ status: again.status, error: again.body.error }).toEqual({ status: 400, error: 'invalid_grant' });
      expect((await h.json('GET', '/api/notes', undefined, first.body.access_token)).status).toBe(401);
    });

    test('a code is bound to its verifier, redirect, client and resource, and ends in 60 s', async () => {
      const h = await setup();
      const a = await h.authorize();
      expect((await h.tokens(a.code, 'w'.repeat(43))).body.error).toBe('invalid_grant');
      // A refused binding spends nothing: the code still redeems with its own.
      expect((await h.tokens(a.code, a.verifier)).status).toBe(200);
      const b = await h.authorize();
      expect((await h.tokens(b.code, b.verifier, CLAUDE_CODE, 'http://localhost:9/callback')).body.error).toBe('invalid_grant');
      const c = await h.authorize();
      const wrongTarget = await h.form('/oauth/token', {
        grant_type: 'authorization_code',
        code: c.code,
        code_verifier: c.verifier,
        redirect_uri: CALLBACK,
        client_id: CLAUDE_CODE,
        resource: 'https://elsewhere.example.com/api/mcp',
      });
      expect(wrongTarget.body.error).toBe('invalid_target');
      const d = await h.authorize();
      h.clock.advance(61 * 1000);
      expect((await h.tokens(d.code, d.verifier)).body.error).toBe('invalid_grant');
    });

    test('a refresh token rotates, and its second use revokes the grant', async () => {
      const h = await setup();
      const { code, verifier } = await h.authorize();
      const first = await h.tokens(code, verifier);
      const refreshed = await h.form('/oauth/token', { grant_type: 'refresh_token', refresh_token: first.body.refresh_token, client_id: CLAUDE_CODE });
      expect(refreshed.status).toBe(200);
      expect(refreshed.body.refresh_token).not.toBe(first.body.refresh_token);
      expect((await h.json('GET', '/api/notes', undefined, refreshed.body.access_token)).status).toBe(200);
      const reused = await h.form('/oauth/token', { grant_type: 'refresh_token', refresh_token: first.body.refresh_token, client_id: CLAUDE_CODE });
      expect({ status: reused.status, error: reused.body.error }).toEqual({ status: 400, error: 'invalid_grant' });
      expect((await h.json('GET', '/api/notes', undefined, refreshed.body.access_token)).status).toBe(401);
      const after = await h.form('/oauth/token', { grant_type: 'refresh_token', refresh_token: refreshed.body.refresh_token, client_id: CLAUDE_CODE });
      expect(after.body.error).toBe('invalid_grant');
    });

    test('an access token ends with its lifetime, and a disabled user ends every grant', async () => {
      const h = await setup();
      const a = await h.authorize();
      const first = await h.tokens(a.code, a.verifier);
      h.clock.advance(60 * MINUTE);
      expect((await h.json('GET', '/api/notes', undefined, first.body.access_token)).status).toBe(401);
      const refreshed = await h.form('/oauth/token', { grant_type: 'refresh_token', refresh_token: first.body.refresh_token, client_id: CLAUDE_CODE });
      expect(refreshed.status).toBe(200);
      await h.store.setDisabled(h.userId, true, h.clock.now);
      expect((await h.json('GET', '/api/notes', undefined, refreshed.body.access_token)).status).toBe(401);
      expect((await h.form('/oauth/token', { grant_type: 'refresh_token', refresh_token: refreshed.body.refresh_token, client_id: CLAUDE_CODE })).body.error).toBe(
        'invalid_grant'
      );
    });

    test('an access token is held to the resource it was given for', async () => {
      const h = await setup();
      const { code, verifier } = await h.authorize();
      const tokens = await h.tokens(code, verifier);
      const other = new IdentityService({
        store: h.store,
        config: { ...CONFIG, oauth: { publicBaseUrl: 'https://app.example.com/api/billing' } },
        now: () => h.clock.now,
        routes: ROUTES,
        oauth: {},
      });
      const headers = new Headers({ authorization: `Bearer ${tokens.body.access_token}` });
      await expect(other.authenticate({ method: 'GET', host: 'api.internal', headers })).rejects.toMatchObject({ status: 401 });
      expect((await h.service.authenticate({ method: 'GET', host: 'api.internal', headers })).oauthGrantId).toBeTruthy();
    });

    test('the consent page names the redirect the request goes to, not the client\'s first', async () => {
      const h = await setup();
      const registered = await h.request('POST', '/oauth/register', {
        body: JSON.stringify({ client_name: 'Claude', redirect_uris: ['https://claude.ai/api/mcp/auth_callback', 'https://evil.example/cb'] }),
        type: 'application/json',
      });
      const query = new URLSearchParams({
        response_type: 'code',
        client_id: registered.body.client_id,
        redirect_uri: 'https://evil.example/cb',
        code_challenge: codeChallengeOf('x'.repeat(43)),
        code_challenge_method: 'S256',
      });
      const id = new URL((await h.request('GET', `/oauth/authorize?${query}`)).headers.get('location')!).searchParams.get('request')!;
      const read = await h.json('GET', `/api/auth/oauth/requests/${id}`, undefined, await h.session());
      expect([read.body.data.client.redirectHost, read.body.data.client.kind]).toEqual(['evil.example', 'registered']);
    });

    test('a registration prunes unused clients, but none that holds a live grant', async () => {
      const h = await setup();
      const make = async (name: string) =>
        (
          await h.request('POST', '/oauth/register', {
            body: JSON.stringify({ client_name: name, redirect_uris: ['https://app.example.org/cb'] }),
            type: 'application/json',
          })
        ).body.client_id as string;
      const held = await make('Held');
      const idle = await make('Idle');
      const { code, verifier } = await h.authorize({ clientId: held, redirect: 'https://app.example.org/cb' });
      expect((await h.tokens(code, verifier, held, 'https://app.example.org/cb')).status).toBe(200);
      h.clock.advance(91 * 24 * 60 * MINUTE);
      await make('Later');
      const oauthStore = h.store.oauth()!;
      expect((await oauthStore.findClient(held)).name).toBe('Held');
      await expect(oauthStore.findClient(idle)).rejects.toMatchObject({ kind: 'not_found' });
    });

    test('the person lists their connected apps and revokes one', async () => {
      const h = await setup();
      const { code, verifier } = await h.authorize({ permissions: ['notes.read'] });
      const tokens = await h.tokens(code, verifier);
      const session = await h.session();
      const list = await h.json('GET', '/api/auth/oauth/grants', undefined, session);
      expect(list.status).toBe(200);
      expect(list.body.data).toHaveLength(1);
      expect(list.body.data[0].client).toEqual({
        id: CLAUDE_CODE,
        name: 'Claude Code',
        uri: 'https://claude.ai',
        redirectHost: 'localhost',
        loopbackOnly: true,
        kind: 'metadataDocument',
      });
      expect(list.body.data[0].permissions).toEqual(['notes.read']);
      const revoked = await h.json('DELETE', `/api/auth/oauth/grants/${list.body.data[0].id}`, undefined, session);
      expect(revoked.status).toBe(200);
      expect((await h.json('GET', '/api/notes', undefined, tokens.body.access_token)).status).toBe(401);
      expect((await h.json('GET', '/api/auth/oauth/grants', undefined, session)).body.data).toEqual([]);
      expect((await h.json('DELETE', `/api/auth/oauth/grants/${list.body.data[0].id}`, undefined, session)).status).toBe(404);
    });

    test('revocation of a refresh token ends the grant', async () => {
      const h = await setup();
      const { code, verifier } = await h.authorize();
      const tokens = await h.tokens(code, verifier);
      const r = await h.form('/oauth/revoke', { token: tokens.body.refresh_token, client_id: CLAUDE_CODE });
      expect(r.status).toBe(200);
      expect((await h.json('GET', '/api/notes', undefined, tokens.body.access_token)).status).toBe(401);
      expect((await h.form('/oauth/revoke', { token: 'not-a-token' })).status).toBe(200);
    });

    test('a denial sends the browser back with access_denied', async () => {
      const h = await setup();
      const query = new URLSearchParams({
        response_type: 'code',
        client_id: CLAUDE_CODE,
        redirect_uri: CALLBACK,
        code_challenge: codeChallengeOf('x'.repeat(43)),
        code_challenge_method: 'S256',
        state: 's1',
      });
      const id = new URL((await h.request('GET', `/oauth/authorize?${query}`)).headers.get('location')!).searchParams.get('request')!;
      const session = await h.session();
      const read = await h.json('GET', `/api/auth/oauth/requests/${id}`, undefined, session);
      expect(read.body.data.held).toEqual(['notes.read', 'notes.write']);
      expect((await h.json('POST', `/api/auth/oauth/requests/${id}/deny`, { nonce: 'wrong' }, session)).status).toBe(409);
      const denied = await h.json('POST', `/api/auth/oauth/requests/${id}/deny`, { nonce: read.body.data.nonce }, session);
      const back = new URL(denied.body.data.redirectTo);
      expect([back.searchParams.get('error'), back.searchParams.get('state')]).toEqual(['access_denied', 's1']);
      expect((await h.json('GET', `/api/auth/oauth/requests/${id}`, undefined, session)).status).toBe(404);
    });

    test('authorize refuses an unknown client and a foreign redirect on its own page, and the rest at the redirect', async () => {
      const h = await setup();
      const base = { response_type: 'code', code_challenge: codeChallengeOf('x'.repeat(43)), code_challenge_method: 'S256' };
      const unknown = await h.request('GET', `/oauth/authorize?${new URLSearchParams({ ...base, client_id: 'https://evil.example.com/client', redirect_uri: CALLBACK })}`);
      expect(unknown.status).toBe(400);
      expect(unknown.headers.get('content-security-policy')).toContain("frame-ancestors 'none'");
      const foreign = await h.request('GET', `/oauth/authorize?${new URLSearchParams({ ...base, client_id: CLAUDE_CODE, redirect_uri: 'https://evil.example.com/cb' })}`);
      expect(foreign.status).toBe(400);
      const plain = await h.request(
        'GET',
        `/oauth/authorize?${new URLSearchParams({ ...base, client_id: CLAUDE_CODE, redirect_uri: CALLBACK, code_challenge_method: 'plain', state: 'z' })}`
      );
      expect(plain.status).toBe(302);
      const back = new URL(plain.headers.get('location')!);
      expect([back.origin, back.searchParams.get('error'), back.searchParams.get('state'), back.searchParams.get('iss')]).toEqual([
        'http://localhost:51234',
        'invalid_request',
        'z',
        BASE,
      ]);
      const scope = await h.request('GET', `/oauth/authorize?${new URLSearchParams({ ...base, client_id: CLAUDE_CODE, redirect_uri: CALLBACK, scope: 'admin.everything' })}`);
      expect(new URL(scope.headers.get('location')!).searchParams.get('error')).toBe('invalid_scope');
    });

    test('a registered client connects, and registration admits public clients alone', async () => {
      const h = await setup();
      const refused = await h.request('POST', '/oauth/register', {
        body: JSON.stringify({ redirect_uris: ['https://app.example.org/cb'], token_endpoint_auth_method: 'client_secret_basic' }),
        type: 'application/json',
      });
      expect([refused.status, refused.body.error]).toEqual([400, 'invalid_client_metadata']);
      const custom = await h.request('POST', '/oauth/register', { body: JSON.stringify({ redirect_uris: ['myapp://cb'] }), type: 'application/json' });
      expect([custom.status, custom.body.error]).toEqual([400, 'invalid_redirect_uri']);
      const r = await h.request('POST', '/oauth/register', {
        body: JSON.stringify({ client_name: 'Notes Desktop', redirect_uris: ['https://app.example.org/cb'] }),
        type: 'application/json',
      });
      expect(r.status).toBe(201);
      expect(r.body.token_endpoint_auth_method).toBe('none');
      const { code, verifier } = await h.authorize({ clientId: r.body.client_id, redirect: 'https://app.example.org/cb' });
      const tokens = await h.tokens(code, verifier, r.body.client_id, 'https://app.example.org/cb');
      expect(tokens.status).toBe(200);
      // A code is its own client's.
      const other = await h.authorize();
      expect((await h.tokens(other.code, other.verifier, r.body.client_id)).body.error).toBe('invalid_grant');
    });

    test('without registration the endpoint is gone and the metadata names none', async () => {
      const h = await setup({ registration: false });
      expect((await h.request('POST', '/oauth/register', { body: '{"redirect_uris":["https://a.example/cb"]}', type: 'application/json' })).status).toBe(404);
      const meta = await h.request('GET', '/.well-known/oauth-authorization-server/api/notes');
      expect(meta.body.registration_endpoint).toBeUndefined();
    });

    test('an administrator lists and revokes a user’s grants', async () => {
      const h = await setup();
      const admin = await h.store.createUser({
        login: 'root@example.com',
        name: 'Root',
        passwordHash: await hashPassword(PASSWORD, h.service.config.password.argon2),
        at: h.clock.now,
      });
      const role = await h.store.createRole('admin', ['identity']);
      await h.store.grantRole(admin.id, role.id, h.clock.now);
      await h.authorize();
      const session = (await h.json('POST', '/api/auth/login', { login: 'root@example.com', password: PASSWORD }, '')).body.data.token as string;
      const list = await h.json('GET', `/api/auth/admin/users/${h.userId}/oauth/grants`, undefined, session);
      expect(list.status).toBe(200);
      expect(list.body.data).toHaveLength(1);
      const revoked = await h.json('DELETE', `/api/auth/admin/users/${h.userId}/oauth/grants/${list.body.data[0].id}`, undefined, session);
      expect(revoked.status).toBe(200);
      expect((await h.json('GET', `/api/auth/admin/users/${h.userId}/oauth/grants`, undefined, session)).body.data).toEqual([]);
    });

    test('the default consent page is held to a strict CSP and no framing', async () => {
      const h = await setup();
      const page = await h.request('GET', '/oauth/consent?request=x');
      expect(page.status).toBe(200);
      const csp = page.headers.get('content-security-policy') ?? '';
      expect(csp).toContain("frame-ancestors 'none'");
      expect(csp).toContain("default-src 'none'");
      expect(page.headers.get('x-frame-options')).toBe('DENY');
      expect(page.body).toContain(`${BASE}/api/auth`);
    });
  });
}
