/*
The user model end to end (run by TestGeneratedUserRoutesEndToEnd with
API_DIR set to the materialized fixture-user-routes-api package, SDK_DIR to
its generated TypeScript SDK and IDENTITY_DDL to its authDb's SQLite DDL).
The generated router serves the API over bun:sqlite with the identity
service identityService builds, and every call but the cross-origin
refusals goes through the SDK, whose fetch hands each request to the app:
a bearer session from register to logout, the administration routes under
the grant rule, and a cookie session in a browser's place, which keeps the
cookies the app sets and sends the credentials mode the SDK passes.
*/
import { Database } from 'bun:sqlite';
import { readFileSync } from 'node:fs';
import { describe, expect, test } from 'bun:test';
import { Hono } from 'hono';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { bunSqlite, hashPassword, parseIdentityConfig, sqliteIdentityStore } from '@superschematic/http-runtime/identity';
import { identityDescriptor } from '@schemas/fixture-user-model-db-types/identity';
import { SessionTransport } from '@schemas/fixture-user-routes-api-types';

const apiDir = process.env.API_DIR;
const sdkDir = process.env.SDK_DIR;
const ddlPath = process.env.IDENTITY_DDL;
if (!apiDir || !sdkDir || !ddlPath) throw new Error('API_DIR, SDK_DIR and IDENTITY_DDL are required');
const { buildRouter, identityService, operationSpecs } = await import(`${apiDir}/router.ts`);
const { FixtureUserRoutesApiSDK, ApiError } = await import(`${sdkDir}/index.ts`);

const BASE = 'http://api.example.com';
const APP_ORIGIN = 'https://app.example.com';
const ADMIN_PASSWORD = 'admin password';
const GRACE_PASSWORD = 'grace password';

/** A served API: the app, its identity service and store, and the seeded administrator. */
async function serve() {
  const db = new Database(':memory:');
  db.exec('PRAGMA foreign_keys = ON');
  db.exec(readFileSync(ddlPath!, 'utf8'));
  const store = sqliteIdentityStore(bunSqlite(db as never), identityDescriptor);
  const config = parseIdentityConfig({ password: { argon2: { memoryKiB: 64, iterations: 1, parallelism: 1 } }, trustedOrigins: [APP_ORIGIN] });
  const identity = identityService({ store, config });
  const admin = await store.createUser({ login: 'admin@example.com', name: 'Admin', passwordHash: await hashPassword(ADMIN_PASSWORD, config.password.argon2), at: new Date() });
  const role = await store.createRole('admin', ['identity']);
  await store.grantRole(admin.id, role.id, new Date());

  const implementations = {
    greeting: {
      greet: async (_args: unknown, ctx: { principal: { claims: { name: string } } }) => ({ message: `Hello, ${ctx.principal.claims.name}` }),
    },
  };
  const app = new Hono();
  app.route('/', buildRouter(implementations, { identity }));
  app.notFound(notFoundHandler());
  app.onError(errorHandler());
  return { app, store, adminId: admin.id };
}

type Served = Awaited<ReturnType<typeof serve>>;

/** An SDK whose fetch hands each request to the app. */
function sdkFor(served: Served, config: Record<string, unknown> = {}) {
  return new FixtureUserRoutesApiSDK({
    baseUrl: BASE,
    fetch: (input: RequestInfo | URL, init?: RequestInit) => served.app.request(String(input), init),
    ...config,
  });
}

/** The status and problem code a call is refused with. */
async function refusal(call: Promise<unknown>): Promise<[number, string | undefined]> {
  try {
    await call;
  } catch (error) {
    expect(error).toBeInstanceOf(ApiError);
    return [(error as { statusCode: number }).statusCode, (error as { code?: string }).code];
  }
  throw new Error('the call was not refused');
}

/**
 * A browser on APP_ORIGIN: its fetch sends the cookies the app set, Origin
 * and Sec-Fetch-Site as a cross-site request carries them, and keeps the
 * cookies each response sets. It records the credentials mode and the
 * headers of each request.
 */
function browser(served: Served) {
  const jar = new Map<string, string>();
  const sent: { credentials: RequestCredentials | undefined; headers: Headers }[] = [];
  const responses: Response[] = [];
  const fetch = async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const headers = new Headers(init.headers);
    sent.push({ credentials: init.credentials, headers: new Headers(headers) });
    headers.set('origin', APP_ORIGIN);
    headers.set('sec-fetch-site', 'cross-site');
    if (init.credentials === 'include' && jar.size > 0) headers.set('cookie', [...jar].map(([name, value]) => `${name}=${value}`).join('; '));
    const response = await served.app.request(String(input), { ...init, headers });
    for (const cookie of response.headers.getSetCookie()) {
      const [pair, ...attributes] = cookie.split('; ');
      const eq = pair!.indexOf('=');
      const [name, value] = [pair!.slice(0, eq), pair!.slice(eq + 1)];
      if (value === '' || attributes.includes('Max-Age=0')) jar.delete(name);
      else jar.set(name, value);
    }
    responses.push(response);
    return response;
  };
  return { jar, sent, responses, fetch };
}

describe('generated fixture-user-routes-api router with the identity runtime', () => {
  test('the operation table holds the user model with its rate limits, and buildRouter refuses an authenticate', async () => {
    expect(operationSpecs.login).toMatchObject({ manual: true, rateLimitPerMinute: 10, auth: { public: true } });
    expect(operationSpecs.register.rateLimitPerMinute).toBe(5);
    expect(operationSpecs.changePassword.rateLimitPerMinute).toBe(10);
    expect(operationSpecs.grantRole.auth.permissions).toEqual(['identity.roles.write']);
    const identity = identityService({ store: (await serve()).store });
    expect(() => buildRouter({ greeting: { greet: async () => ({ message: '' }) } }, {})).toThrow(/options.identity is required/);
    expect(() => buildRouter({ greeting: { greet: async () => ({ message: '' }) } }, { identity, authenticate: async () => null })).toThrow(
      /options.authenticate is refused beside options.identity/
    );
  });

  test('a bearer session: register, login, me, capabilities, a protected route, changePassword and logout', async () => {
    const served = await serve();
    const anonymous = sdkFor(served);
    expect(await refusal(anonymous.greeting.greet())).toEqual([401, 'unauthorized']);

    const registered = await anonymous.account.register({ login: 'Grace@Example.com', name: 'Grace', password: GRACE_PASSWORD });
    expect(registered.user).toMatchObject({ login: 'grace@example.com', name: 'Grace' });
    expect(registered.token).toBeString();
    expect(registered.expiresAt).toBeInstanceOf(Date);
    expect(await refusal(anonymous.account.login({ login: 'grace@example.com', password: 'not her password' }))).toEqual([401, 'invalid_credentials']);

    const login = await anonymous.account.login({ login: 'GRACE@example.com', password: GRACE_PASSWORD });
    const grace = sdkFor(served, { auth: { token: login.token } });
    const other = sdkFor(served, { auth: { token: registered.token } });
    expect(await grace.account.me()).toEqual({ user: { id: registered.user.id, login: 'grace@example.com', name: 'Grace' }, roles: [], permissions: [] });

    // Capabilities answers for every operation of the API, by operation id.
    const { operations } = await grace.account.capabilities();
    expect(Object.keys(operations)).toHaveLength(Object.keys(operationSpecs).length);
    expect(operations).toMatchObject({
      GreetingGreetHandler: true,
      AccountMeHandler: true,
      AccountLoginHandler: true,
      AccountAdminListUsersHandler: false,
      AccountAdminGrantRoleHandler: false,
    });

    expect(await grace.greeting.greet()).toEqual({ message: 'Hello, Grace' });

    // changePassword keeps the caller's session and ends the others.
    expect(await grace.account.changePassword({ current: GRACE_PASSWORD, password: 'grace new password' })).toBe(true);
    expect(await refusal(other.account.me())).toEqual([401, 'unauthorized']);
    expect((await grace.account.me()).user.login).toBe('grace@example.com');
    expect(await refusal(anonymous.account.login({ login: 'grace@example.com', password: GRACE_PASSWORD }))).toEqual([401, 'invalid_credentials']);

    expect(await grace.account.logout()).toBe(true);
    expect(await refusal(grace.account.me())).toEqual([401, 'unauthorized']);
    expect(await refusal(grace.greeting.greet())).toEqual([401, 'unauthorized']);
    // A malformed bearer header is the identity runtime's 401, not a 400.
    expect(await refusal(sdkFor(served, { auth: { token: 'not a token' } }).greeting.greet())).toEqual([401, 'unauthorized']);
  });

  test('the administration routes need their permissions, and no one grants what they do not hold', async () => {
    const served = await serve();
    const anonymous = sdkFor(served);
    const { token: adminToken } = await anonymous.account.login({ login: 'admin@example.com', password: ADMIN_PASSWORD });
    const admin = sdkFor(served, { auth: { token: adminToken } });

    const grace = await admin.accountAdmin.createUser({ login: 'grace@example.com', name: 'Grace', password: GRACE_PASSWORD });
    expect(grace).toMatchObject({ login: 'grace@example.com', disabled: false, roles: [] });
    const viewer = await admin.accountAdmin.createRole({ name: 'viewer', permissions: ['identity.users.read'] });
    // The administrator holds identity, which covers identity.users.read,
    // and not greetings.read.
    expect(await refusal(admin.accountAdmin.createRole({ name: 'greeter', permissions: ['greetings.read'] }))).toEqual([403, 'forbidden']);
    const auditor = await served.store.createRole('auditor', ['audit.read']);
    try {
      await admin.accountAdmin.grantRole(grace.id, auditor.id);
      throw new Error('the grant was not refused');
    } catch (error) {
      expect((error as { statusCode: number }).statusCode).toBe(403);
      expect((error as { response: { details: unknown } }).response.details).toEqual({ permissions: ['audit.read'] });
    }
    const granted = await admin.accountAdmin.grantRole(grace.id, viewer.id);
    expect(granted.roles).toEqual([{ id: viewer.id, name: 'viewer' }]);

    const { token } = await anonymous.account.login({ login: 'grace@example.com', password: GRACE_PASSWORD });
    const asGrace = sdkFor(served, { auth: { token } });
    expect((await asGrace.account.me()).permissions).toEqual(['identity.users.read']);
    const users = await asGrace.accountAdmin.listUsers();
    expect(users.map((user: { login: string }) => user.login)).toEqual(['admin@example.com', 'grace@example.com']);
    // The router's gate refuses a route the caller's roles do not cover.
    expect(await refusal(asGrace.accountAdmin.grantRole(grace.id, viewer.id))).toEqual([403, 'forbidden']);
    expect((await asGrace.account.capabilities()).operations).toMatchObject({
      AccountAdminListUsersHandler: true,
      AccountAdminGetUserHandler: true,
      AccountAdminCreateUserHandler: false,
      AccountAdminGrantRoleHandler: false,
    });

    // Disabling a user ends their sessions.
    await admin.accountAdmin.disableUser(grace.id);
    expect(await refusal(asGrace.account.me())).toEqual([401, 'unauthorized']);
  });

  test('a cookie session through the SDK: the browser keeps the cookie, the SDK sends no Authorization', async () => {
    const served = await serve();
    await served.store.createUser({ login: 'grace@example.com', name: 'Grace', passwordHash: await hashPassword(GRACE_PASSWORD, { memoryKiB: 64, iterations: 1, parallelism: 1 }), at: new Date() });
    const tab = browser(served);
    // A token a bearer login stored would not take the cookie's place.
    const stored = new Map([['auth_token', 'a stale token']]);
    (globalThis as { window?: unknown }).window = { localStorage: { getItem: (key: string) => stored.get(key) ?? null, setItem() {}, removeItem() {} } };
    try {
      const sdk = sdkFor(served, { credentials: 'include', fetch: tab.fetch });
      const login = await sdk.account.login({ login: 'grace@example.com', password: GRACE_PASSWORD, session: SessionTransport.Cookie });
      expect(login.token).toBeUndefined();
      expect(login.user.name).toBe('Grace');
      expect([...tab.jar.keys()]).toEqual(['__Host-session']);
      expect(tab.responses[0]!.headers.get('access-control-allow-origin')).toBe(APP_ORIGIN);
      expect(tab.responses[0]!.headers.get('access-control-allow-credentials')).toBe('true');

      expect((await sdk.account.me()).user.login).toBe('grace@example.com');
      expect(await sdk.greeting.greet()).toEqual({ message: 'Hello, Grace' });
      expect(await sdk.account.logout()).toBe(true);
      expect(tab.jar.size).toBe(0);
      expect(await refusal(sdk.account.me())).toEqual([401, 'unauthorized']);

      expect(tab.sent.map(request => request.credentials)).toEqual(['include', 'include', 'include', 'include', 'include']);
      expect(tab.sent.filter(request => request.headers.has('authorization'))).toEqual([]);
    } finally {
      delete (globalThis as { window?: unknown }).window;
    }
  });

  test('the cross-origin check refuses a cookie login and an unsafe cookie request from another origin', async () => {
    const served = await serve();
    await served.store.createUser({ login: 'grace@example.com', name: 'Grace', passwordHash: await hashPassword(GRACE_PASSWORD, { memoryKiB: 64, iterations: 1, parallelism: 1 }), at: new Date() });
    const body = JSON.stringify({ login: 'grace@example.com', password: GRACE_PASSWORD, session: 'cookie' });
    const from = (origin: string) => ({ origin, 'sec-fetch-site': 'cross-site', 'content-type': 'application/json' });

    const evilLogin = await fetchProblem(served, '/api/auth/login', { method: 'POST', body, headers: from('https://evil.example') });
    expect(evilLogin).toEqual([403, 'cross_origin', null]);

    const login = await served.app.request(`${BASE}/api/auth/login`, { method: 'POST', body, headers: from(APP_ORIGIN) });
    expect(login.status).toBe(200);
    const cookie = login.headers.getSetCookie()[0]!.split('; ')[0]!;

    // A GET passes the check from any origin, without CORS for it; a POST
    // from another origin is refused.
    const read = await served.app.request(`${BASE}/api/auth/me`, { headers: { ...from('https://evil.example'), cookie } });
    expect([read.status, read.headers.get('access-control-allow-origin')]).toEqual([200, null]);
    expect(await fetchProblem(served, '/api/auth/logout', { method: 'POST', headers: { ...from('https://evil.example'), cookie } })).toEqual([
      403,
      'cross_origin',
      null,
    ]);
    // The session is still live: the refused logout did not run.
    expect((await served.app.request(`${BASE}/api/auth/me`, { headers: { cookie } })).status).toBe(200);

    // A preflight from the trusted origin is answered with credentials.
    const preflight = await served.app.request(`${BASE}/api/auth/logout`, {
      method: 'OPTIONS',
      headers: { origin: APP_ORIGIN, 'access-control-request-method': 'POST' },
    });
    expect([preflight.status, preflight.headers.get('access-control-allow-credentials')]).toEqual([204, 'true']);
  });
});

/** A raw request's status, problem code and Access-Control-Allow-Origin. */
async function fetchProblem(served: Served, path: string, init: RequestInit): Promise<[number, string, string | null]> {
  const response = await served.app.request(`${BASE}${path}`, init);
  const body = (await response.json()) as { code: string };
  return [response.status, body.code, response.headers.get('access-control-allow-origin')];
}
