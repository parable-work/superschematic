import { afterEach, describe, expect, test } from 'bun:test';
import { Hono } from 'hono';
import { mountManualOperation, mountOperation } from '../hono';
import type { OperationSpec } from '../operation';
import {
  IdentityService,
  hashPassword,
  identityAuthenticator,
  identityCors,
  identityHandler,
  identityOperationSpec,
  identityOperations,
  identityPrincipalOf,
  identityRouterOptions,
  identityRoutes,
  mountIdentityOperations,
  mountIdentityRoutes,
  routesOf,
  type IdentityRoute,
  type SqlIdentityStore,
} from './index';
import { HOUR, at, databaseKinds, type TestDatabase } from './fixture.testing';

/*
The identity routes end to end through Hono's app.request, on each database
the store tests run on: the cases the Go runtime's handler tests hold it
to. The app mounts the routes as mountIdentityRoutes does, with
identityCors before them, and an app route through the runtime's pipeline
with identityAuthenticator, so the router's permission gate reads the
identity principal unchanged.
*/

/** A config at a low argon2 cost, so a test hashes quickly. */
const TEST_CONFIG = { password: { argon2: { memoryKiB: 64, iterations: 1, parallelism: 1 } }, trustedOrigins: ['https://app.example.com'] };
const ADMIN_PASSWORD = 'admin password';
const USER_PASSWORD = 'user password';
const MISSING = '00000000-0000-4000-8000-000000000000';

/** The route requirements capabilities answers for. */
const ROUTES: IdentityRoute[] = [
  { operationId: 'OrdersList', requiresAuth: false, permissions: [], requireOwnership: false, serviceOnly: false },
  { operationId: 'AuthMe', requiresAuth: true, permissions: [], requireOwnership: false, serviceOnly: false },
  { operationId: 'OrdersCreate', requiresAuth: true, permissions: ['orders.write'], requireOwnership: false, serviceOnly: false },
  { operationId: 'IdentityListUsers', requiresAuth: true, permissions: ['identity.users.read'], requireOwnership: false, serviceOnly: false },
  { operationId: 'StockSync', requiresAuth: false, permissions: [], requireOwnership: false, serviceOnly: true },
];

const ORDERS: OperationSpec = {
  name: 'createOrder',
  namespace: 'orders',
  method: 'POST',
  path: '/orders',
  pathParams: [],
  queryParams: [],
  bodyParams: [],
  auth: { public: false, required: true, permissions: ['orders.write'] },
  manual: false,
};

/** A response: its status, its JSON body, its headers and the cookies it sets. */
interface Reply {
  status: number;
  body: Record<string, any>;
  headers: Headers;
  cookies: { name: string; value: string; maxAge: number; attributes: string }[];
}

class Clock {
  now = at();
  advance(ms: number): void {
    this.now = new Date(this.now.getTime() + ms);
  }
}

interface Harness {
  db: TestDatabase;
  store: SqlIdentityStore;
  service: IdentityService;
  clock: Clock;
  app: Hono;
  adminId: string;
  memberId: string;
  do(method: string, path: string, body?: unknown, ...headers: string[]): Promise<Reply>;
  login(login: string, password: string): Promise<string>;
}

const open: TestDatabase[] = [];
afterEach(async () => {
  while (open.length > 0) await open.pop()!.close();
});

async function harness(db: TestDatabase, config: unknown = TEST_CONFIG): Promise<Harness> {
  const store = db.store();
  const clock = new Clock();
  const service = new IdentityService({ store, config, now: () => clock.now, routes: ROUTES });
  const app = new Hono();
  app.use('*', identityCors(service));
  mountIdentityRoutes(app, service, { sessions: { register: true }, administration: {} });
  mountOperation(app, ORDERS, async ctx => ctx.principal?.claims?.name ?? null, { authenticate: identityAuthenticator(service) });

  const params = service.config.password.argon2;
  const admin = await store.createUser({ login: 'admin@example.com', name: 'Admin', passwordHash: await hashPassword(ADMIN_PASSWORD, params), at: clock.now });
  const member = await store.createUser({ login: 'member@example.com', name: 'Member', passwordHash: await hashPassword(USER_PASSWORD, params), at: clock.now });
  const role = await store.createRole('admin', ['identity', 'orders']);
  await store.grantRole(admin.id, role.id, clock.now);

  const h: Harness = {
    db,
    store,
    service,
    clock,
    app,
    adminId: admin.id,
    memberId: member.id,
    async do(method, path, body, ...headers) {
      const init: RequestInit = { method, headers: [] as [string, string][] };
      for (let i = 0; i < headers.length; i += 2) (init.headers as [string, string][]).push([headers[i]!, headers[i + 1]!]);
      if (body !== undefined) {
        init.body = JSON.stringify(body);
        (init.headers as [string, string][]).push(['content-type', 'application/json']);
      }
      const response = await app.request(`http://api.example.com${path}`, init);
      const text = await response.text();
      return {
        status: response.status,
        body: /json/u.test(response.headers.get('content-type') ?? '') ? JSON.parse(text) : {},
        headers: response.headers,
        cookies: response.headers.getSetCookie().map(cookie => {
          const [pair, ...attributes] = cookie.split('; ');
          const eq = pair!.indexOf('=');
          const maxAge = attributes.find(a => a.startsWith('Max-Age='));
          return { name: pair!.slice(0, eq), value: pair!.slice(eq + 1), maxAge: maxAge ? Number(maxAge.slice(8)) : NaN, attributes: attributes.join('; ') };
        }),
      };
    },
    async login(login, password) {
      const r = await h.do('POST', '/auth/login', { login, password });
      expect(r.status).toBe(200);
      return r.body.data.token as string;
    },
  };
  return h;
}

function expectReply(r: Reply, status: number, code?: string): void {
  expect({ status: r.status, code: r.body.code }).toEqual({ status, code: code ?? r.body.code });
}

/** The answer of an operation the contract types as returning true. */
function expectDone(r: Reply): void {
  expect({ status: r.status, data: r.body.data, requestId: typeof r.body.meta?.requestId }).toEqual({ status: 200, data: true, requestId: 'string' });
}

const bearer = (token: string) => ['Authorization', `Bearer ${token}`];
const cookie = (token: string) => ['Cookie', `__Host-session=${token}`];

for (const kind of databaseKinds) {
  describe(`identity routes on ${kind.name}`, () => {
    const newHarness = async (config?: unknown) => {
      const db = await kind.open();
      open.push(db);
      return harness(db, config);
    };

    test('a bearer login answers the user, the expiry and a token; me and capabilities answer for the caller', async () => {
      const h = await newHarness();
      const r = await h.do('POST', '/auth/login', { login: 'ADMIN@example.com', password: ADMIN_PASSWORD });
      expectReply(r, 200);
      expect(r.body.data.user).toEqual({ id: h.adminId, login: 'admin@example.com', name: 'Admin' });
      expect(r.body.data.expiresAt).toBe('2026-10-22T12:00:00Z');
      expect(r.body.meta.requestId).toBeString();
      expect(r.cookies).toEqual([]);
      const token = r.body.data.token as string;

      const me = await h.do('GET', '/auth/me', undefined, ...bearer(token));
      expectReply(me, 200);
      expect(me.body.data.permissions).toEqual(['identity', 'orders']);
      expect(me.body.data.roles.map((role: { name: string }) => role.name)).toEqual(['admin']);
      expect(me.body.data.user).toEqual({ id: h.adminId, login: 'admin@example.com', name: 'Admin' });

      const caps = await h.do('GET', '/auth/capabilities', undefined, ...bearer(token));
      expect(caps.body.data.operations).toEqual({ AuthMe: true, IdentityListUsers: true, OrdersCreate: true, OrdersList: true });
      const memberToken = await h.login('member@example.com', USER_PASSWORD);
      const memberCaps = await h.do('GET', '/auth/capabilities', undefined, ...bearer(memberToken));
      expect(memberCaps.body.data.operations).toEqual({ AuthMe: true, IdentityListUsers: false, OrdersCreate: false, OrdersList: true });

      // The router's gate reads the identity principal unchanged.
      const order = await h.do('POST', '/orders', undefined, ...bearer(token));
      expectReply(order, 200);
      expect(order.body.data).toBe('Admin');
      expectReply(await h.do('POST', '/orders', undefined, ...bearer(memberToken)), 403, 'forbidden');
      expectReply(await h.do('POST', '/orders'), 401, 'unauthorized');

      const refused: Record<string, unknown> = {
        'a wrong password': { login: 'admin@example.com', password: 'wrong password' },
        'an unknown login': { login: 'nobody@example.com', password: ADMIN_PASSWORD },
        'a login the scalar refuses': { login: 'admin', password: ADMIN_PASSWORD },
        'the right password for another': { login: 'member@example.com', password: ADMIN_PASSWORD },
        'the password in another case': { login: 'admin@example.com', password: ADMIN_PASSWORD.toUpperCase() },
        'a bearer session asked explicitly': { login: 'admin@example.com', password: 'wrong password', session: 'bearer' },
      };
      for (const [name, body] of Object.entries(refused)) {
        const reply = await h.do('POST', '/auth/login', body);
        expect([name, reply.status, reply.body.code]).toEqual([name, 401, 'invalid_credentials']);
      }
      const invalid: Record<string, unknown> = {
        'a short password': { login: 'admin@example.com', password: 'short' },
        'no login': { password: ADMIN_PASSWORD },
        'an unknown session': { login: 'admin@example.com', password: ADMIN_PASSWORD, session: 'jwt' },
        'an unknown member': { login: 'admin@example.com', password: ADMIN_PASSWORD, remember: true },
        'a body that is a list': [],
        'a number for a string': { login: 1, password: ADMIN_PASSWORD },
      };
      for (const [name, body] of Object.entries(invalid)) {
        const reply = await h.do('POST', '/auth/login', body);
        expect([name, reply.status, reply.body.code]).toEqual([name, 400, 'bad_request']);
      }
      const short = await h.do('POST', '/auth/login', { login: 'admin@example.com', password: 'short' });
      expect(Object.keys(short.body.errors)).toEqual(['password']);
      expectReply(await h.do('POST', '/auth/login', undefined), 400, 'bad_request');
    });

    test('a cookie login sets the cookie and answers no token; logout clears it; a refused cookie is cleared', async () => {
      const h = await newHarness();
      const r = await h.do('POST', '/auth/login', { login: 'admin@example.com', password: ADMIN_PASSWORD, session: 'cookie' }, 'Sec-Fetch-Site', 'same-origin');
      expectReply(r, 200);
      expect(r.body.data.token).toBeUndefined();
      expect(r.cookies).toHaveLength(1);
      const c = r.cookies[0]!;
      expect(c).toMatchObject({ name: '__Host-session', maxAge: 1209600, attributes: 'Path=/; Max-Age=1209600; HttpOnly; Secure; SameSite=Lax' });
      expect(c.value).toHaveLength(43);

      expectReply(await h.do('GET', '/auth/me', undefined, ...cookie(c.value)), 200);
      // A safe method skips the cross-origin check.
      expectReply(await h.do('GET', '/auth/me', undefined, ...cookie(c.value), 'Sec-Fetch-Site', 'cross-site'), 200);

      const out = await h.do('POST', '/auth/logout', undefined, ...cookie(c.value), 'Sec-Fetch-Site', 'same-origin');
      expectReply(out, 200);
      expect(out.body.data).toBe(true);
      expect(out.cookies).toEqual([{ name: '__Host-session', value: '', maxAge: 0, attributes: 'Path=/; Max-Age=0; HttpOnly; Secure; SameSite=Lax' }]);
      const after = await h.do('GET', '/auth/me', undefined, ...cookie(c.value));
      expectReply(after, 401, 'unauthorized');
      expect(after.cookies.map(x => x.maxAge)).toEqual([0]);
      const bad = await h.do('GET', '/auth/me', undefined, ...cookie('not-a-token'));
      expectReply(bad, 401, 'unauthorized');
      expect(bad.cookies.map(x => x.maxAge)).toEqual([0]);
      // Through the router's pipeline too.
      const order = await h.do('POST', '/orders', undefined, ...cookie(c.value));
      expectReply(order, 401, 'unauthorized');
      expect(order.cookies.map(x => x.maxAge)).toEqual([0]);
    });

    test('a cookie login and an unsafe cookie request from another origin are 403 cross_origin, unless it is trusted', async () => {
      const h = await newHarness();
      const cookieLogin = { login: 'admin@example.com', password: ADMIN_PASSWORD, session: 'cookie' };
      expectReply(await h.do('POST', '/auth/login', cookieLogin, 'Sec-Fetch-Site', 'cross-site', 'Origin', 'https://evil.example'), 403, 'cross_origin');
      expectReply(await h.do('POST', '/auth/login', cookieLogin, 'Origin', 'https://evil.example'), 403, 'cross_origin');
      expectReply(
        await h.do('POST', '/auth/register', { login: 'new@example.com', password: USER_PASSWORD, session: 'cookie' }, 'Sec-Fetch-Site', 'cross-site'),
        403,
        'cross_origin'
      );
      // A bearer login from another origin is not checked: it sets no cookie.
      expectReply(await h.do('POST', '/auth/login', { login: 'admin@example.com', password: ADMIN_PASSWORD }, 'Sec-Fetch-Site', 'cross-site'), 200);
      // An older browser's same-host Origin passes.
      expectReply(await h.do('POST', '/auth/login', cookieLogin, 'Origin', 'http://api.example.com'), 200);

      const trusted = await h.do('POST', '/auth/login', cookieLogin, 'Sec-Fetch-Site', 'cross-site', 'Origin', 'https://app.example.com');
      expectReply(trusted, 200);
      expect(trusted.headers.get('access-control-allow-origin')).toBe('https://app.example.com');
      expect(trusted.headers.get('access-control-allow-credentials')).toBe('true');
      const token = trusted.cookies[0]!.value;

      const cross = [...cookie(token), 'Sec-Fetch-Site', 'cross-site', 'Origin', 'https://evil.example'];
      const refused = await h.do('POST', '/auth/logout', undefined, ...cross);
      expectReply(refused, 403, 'cross_origin');
      expect(refused.cookies).toEqual([]);
      expectReply(await h.do('POST', '/orders', undefined, ...cross), 403, 'cross_origin');
      expectReply(await h.do('POST', '/orders', undefined, ...cookie(token), 'Sec-Fetch-Site', 'cross-site', 'Origin', 'https://app.example.com'), 200);

      const bearerToken = await h.login('admin@example.com', ADMIN_PASSWORD);
      expectReply(await h.do('POST', '/orders', undefined, ...bearer(bearerToken), 'Sec-Fetch-Site', 'cross-site', 'Origin', 'https://evil.example'), 200);
    });

    test('an Authorization header that is not a usable bearer token is 401, and the cookie is not read', async () => {
      const h = await newHarness();
      const r = await h.do('POST', '/auth/login', { login: 'admin@example.com', password: ADMIN_PASSWORD, session: 'cookie' });
      const token = r.cookies[0]!.value;
      expectReply(await h.do('GET', '/auth/me', undefined, 'Authorization', 'Basic YWRtaW46cGFzcw==', ...cookie(token)), 401, 'unauthorized');
      expectReply(await h.do('GET', '/auth/me', undefined, ...bearer('A'.repeat(43)), ...cookie(token)), 401, 'unauthorized');
      expectReply(await h.do('GET', '/auth/me', undefined, ...bearer(token), ...cookie('x')), 200);
      // The router's pipeline leaves Authorization to the identity
      // authenticator: a malformed bearer header is its 401, not
      // hono/bearer-auth's 400.
      for (const header of ['Bearer', 'Bearer\tAAAA', 'Bearer a b', `Bearer ${token}=`]) {
        const reply = await h.do('POST', '/orders', undefined, 'Authorization', header, ...cookie(token));
        expect([header, reply.status, reply.body.code]).toEqual([header, 401, 'unauthorized']);
      }
    });

    test('a session ends when it expires, is revoked, goes idle, or its user is disabled', async () => {
      const idle = { sessionTtlSeconds: 3600, idleTimeoutSeconds: 600, touchIntervalSeconds: 60, password: TEST_CONFIG.password };
      const h = await newHarness(idle);
      const me = (token: string) => h.do('GET', '/auth/me', undefined, ...bearer(token));

      const active = await h.login('member@example.com', USER_PASSWORD);
      for (let i = 0; i < 10; i++) {
        h.clock.advance(500_000);
        if ((await me(active)).status === 401) {
          // The session expires an hour after login, at the eighth step.
          expect(h.clock.now.getTime()).toBeGreaterThanOrEqual(at(HOUR).getTime());
          break;
        }
      }
      expectReply(await me(active), 401, 'unauthorized');

      const idleToken = await h.login('member@example.com', USER_PASSWORD);
      h.clock.advance(599_000);
      expectReply(await me(idleToken), 200);
      h.clock.advance(599_000);
      expectReply(await me(idleToken), 200);
      h.clock.advance(600_000);
      expectReply(await me(idleToken), 401, 'unauthorized');

      const revoked = await h.login('member@example.com', USER_PASSWORD);
      expectDone(await h.do('POST', '/auth/logout', undefined, ...bearer(revoked)));
      expectReply(await me(revoked), 401, 'unauthorized');
      expectReply(await h.do('POST', '/auth/logout', undefined, ...bearer(revoked)), 401, 'unauthorized');

      const disabled = await h.login('member@example.com', USER_PASSWORD);
      const admin = await h.login('admin@example.com', ADMIN_PASSWORD);
      expectReply(await h.do('POST', `/auth/admin/users/${h.memberId}/disable`, undefined, ...bearer(admin)), 200);
      expectReply(await me(disabled), 401, 'unauthorized');
      expectReply(await h.do('POST', '/auth/login', { login: 'member@example.com', password: USER_PASSWORD }), 401, 'invalid_credentials');
      expectReply(await h.do('POST', `/auth/admin/users/${h.memberId}/enable`, undefined, ...bearer(admin)), 200);
      // Enabling does not bring a revoked session back.
      expectReply(await me(disabled), 401, 'unauthorized');
      await h.login('member@example.com', USER_PASSWORD);
    });

    test('changePassword needs the current password, keeps the caller session and revokes the others', async () => {
      const h = await newHarness();
      const mine = await h.login('member@example.com', USER_PASSWORD);
      const other = await h.login('member@example.com', USER_PASSWORD);
      const next = 'a new password';
      expectReply(await h.do('POST', '/auth/password', { current: 'not my password', password: next }, ...bearer(mine)), 401, 'invalid_credentials');
      expectReply(await h.do('POST', '/auth/password', { current: USER_PASSWORD, password: 'short' }, ...bearer(mine)), 400, 'bad_request');
      expectReply(await h.do('POST', '/auth/password', { current: USER_PASSWORD, password: next }), 401, 'unauthorized');
      expectDone(await h.do('POST', '/auth/password', { current: USER_PASSWORD, password: next }, ...bearer(mine)));
      expectReply(await h.do('GET', '/auth/me', undefined, ...bearer(mine)), 200);
      expectReply(await h.do('GET', '/auth/me', undefined, ...bearer(other)), 401, 'unauthorized');
      expectReply(await h.do('POST', '/auth/login', { login: 'member@example.com', password: USER_PASSWORD }), 401, 'invalid_credentials');
      await h.login('member@example.com', next);
    });

    test('register creates a user and signs them in; a taken login is 409, a refused login or name a 400 on its field', async () => {
      const h = await newHarness();
      const r = await h.do('POST', '/auth/register', { login: 'New@Example.com', name: 'Newcomer', password: USER_PASSWORD });
      expectReply(r, 200);
      expect(r.body.data.user).toMatchObject({ login: 'new@example.com', name: 'Newcomer' });
      const me = await h.do('GET', '/auth/me', undefined, ...bearer(r.body.data.token));
      expect({ roles: me.body.data.roles, permissions: me.body.data.permissions }).toEqual({ roles: [], permissions: [] });

      const unnamed = await h.do('POST', '/auth/register', { login: 'plain@example.com', password: USER_PASSWORD, session: 'cookie' });
      expectReply(unnamed, 200);
      expect(unnamed.body.data.user.name).toBe('plain@example.com');
      expect(unnamed.cookies).toHaveLength(1);

      expectReply(await h.do('POST', '/auth/register', { login: 'NEW@example.com', password: USER_PASSWORD }), 409, 'conflict');
      const badLogin = await h.do('POST', '/auth/register', { login: 'not an email', password: USER_PASSWORD });
      expectReply(badLogin, 400, 'bad_request');
      expect(Object.keys(badLogin.body.errors)).toEqual(['login']);
      const badName = await h.do('POST', '/auth/register', { login: 'named@example.com', name: 'x'.repeat(81), password: USER_PASSWORD });
      expectReply(badName, 400, 'bad_request');
      expect(badName.body.errors.name[0].validator).toBe('parse');
      expectReply(await h.do('POST', '/auth/login', { login: 'named@example.com', password: USER_PASSWORD }), 401, 'invalid_credentials');
    });

    test('the administration routes need their permissions, and no one grants what they do not hold', async () => {
      const h = await newHarness();
      const admin = bearer(await h.login('admin@example.com', ADMIN_PASSWORD));
      const member = bearer(await h.login('member@example.com', USER_PASSWORD));

      expectReply(await h.do('GET', '/auth/admin/users', undefined, ...member), 403, 'forbidden');
      expectReply(await h.do('GET', '/auth/admin/users'), 401, 'unauthorized');

      const created = await h.do('POST', '/auth/admin/users', { login: 'clerk@example.com', name: 'Clerk', password: USER_PASSWORD }, ...admin);
      expectReply(created, 200);
      const clerkId = created.body.data.id as string;
      expect(created.body.data).toEqual({ id: clerkId, login: 'clerk@example.com', name: 'Clerk', disabled: false, roles: [] });
      expectReply(await h.do('POST', '/auth/admin/users', { login: 'CLERK@example.com', password: USER_PASSWORD }, ...admin), 409, 'conflict');
      expectReply(await h.do('POST', '/auth/admin/users', { login: 'x@example.com', password: USER_PASSWORD }, ...member), 403, 'forbidden');

      const users = await h.do('GET', '/auth/admin/users', undefined, ...admin);
      expect(users.body.data.map((u: { login: string }) => u.login)).toEqual(['admin@example.com', 'clerk@example.com', 'member@example.com']);
      expectReply(await h.do('GET', `/auth/admin/users/${clerkId}`, undefined, ...admin), 200);
      expectReply(await h.do('GET', `/auth/admin/users/${MISSING}`, undefined, ...admin), 404, 'not_found');
      expectReply(await h.do('GET', '/auth/admin/users/not-a-key', undefined, ...admin), 404, 'not_found');

      // A manager may write roles but holds only orders.read.
      const manager = await h.do('POST', '/auth/admin/roles', { name: 'manager', permissions: ['identity.roles', 'orders.read'] }, ...admin);
      expectReply(manager, 200);
      expectReply(await h.do('PUT', `/auth/admin/users/${clerkId}/roles/${manager.body.data.id}`, undefined, ...admin), 200);
      const clerk = bearer(await h.login('clerk@example.com', USER_PASSWORD));

      const denied = await h.do('POST', '/auth/admin/roles', { name: 'writer', permissions: ['orders.read', 'orders.write'] }, ...clerk);
      expectReply(denied, 403, 'forbidden');
      expect(denied.body.details).toEqual({ permissions: ['orders.write'] });
      const reader = await h.do('POST', '/auth/admin/roles', { name: 'reader', permissions: ['orders.read', 'orders.read.archive', 'orders.read'] }, ...clerk);
      expectReply(reader, 200);
      expect(reader.body.data.permissions).toEqual(['orders.read', 'orders.read.archive']);
      const readerId = reader.body.data.id as string;
      expectReply(await h.do('PUT', `/auth/admin/roles/${readerId}`, { name: 'reader', permissions: ['orders'] }, ...clerk), 403, 'forbidden');
      const invalid = await h.do('POST', '/auth/admin/roles', { name: 'bad', permissions: ['orders read', 'a..b'] }, ...admin);
      expectReply(invalid, 422, 'invalid_permission');
      expect(invalid.body.details).toEqual({ permissions: ['orders read', 'a..b'] });
      expectReply(await h.do('POST', '/auth/admin/roles', { name: 'reader', permissions: [] }, ...admin), 409, 'conflict');
      expectReply(await h.do('POST', '/auth/admin/roles', { name: '', permissions: [] }, ...admin), 400, 'bad_request');

      // The admin's role carries orders, which the clerk does not hold.
      const roles = await h.do('GET', '/auth/admin/roles', undefined, ...admin);
      expectReply(roles, 200);
      const adminRoleId = roles.body.data.find((r: { name: string }) => r.name === 'admin').id as string;
      const grantDenied = await h.do('PUT', `/auth/admin/users/${clerkId}/roles/${adminRoleId}`, undefined, ...clerk);
      expectReply(grantDenied, 403, 'forbidden');
      expect(grantDenied.body.details).toEqual({ permissions: ['identity', 'orders'] });
      const granted = await h.do('PUT', `/auth/admin/users/${h.memberId}/roles/${readerId}`, undefined, ...clerk);
      expectReply(granted, 200);
      expect(granted.body.data.roles).toEqual([{ id: readerId, name: 'reader' }]);
      expectReply(await h.do('PUT', `/auth/admin/users/${h.memberId}/roles/${MISSING}`, undefined, ...admin), 404, 'not_found');
      expectReply(await h.do('DELETE', `/auth/admin/users/${h.memberId}/roles/${readerId}`, undefined, ...clerk), 200);

      const updated = await h.do('PUT', `/auth/admin/roles/${readerId}`, { name: 'order reader', permissions: ['orders.read'] }, ...clerk);
      expectReply(updated, 200);
      expect(updated.body.data.name).toBe('order reader');
      const deleted = await h.do('DELETE', `/auth/admin/roles/${readerId}`, undefined, ...clerk);
      expectDone(deleted);
      expectReply(await h.do('DELETE', `/auth/admin/roles/${readerId}`, undefined, ...clerk), 404, 'not_found');
      expectReply(await h.do('GET', '/auth/admin/roles', undefined, ...member), 403, 'forbidden');

      // Setting a user's password revokes their sessions.
      expectDone(await h.do('PUT', `/auth/admin/users/${h.memberId}/password`, { password: 'reset password' }, ...admin));
      expectReply(await h.do('GET', '/auth/me', undefined, ...member), 401, 'unauthorized');
      await h.login('member@example.com', 'reset password');
      expectReply(await h.do('PUT', `/auth/admin/users/${h.memberId}/password`, { password: 'short' }, ...admin), 400, 'bad_request');
      expectReply(await h.do('PUT', `/auth/admin/users/${MISSING}/password`, { password: 'reset password' }, ...admin), 404, 'not_found');

      const disabled = await h.do('POST', `/auth/admin/users/${clerkId}/disable`, undefined, ...admin);
      expectReply(disabled, 200);
      expect(disabled.body.data.disabled).toBe(true);
      expectReply(await h.do('GET', '/auth/me', undefined, ...clerk), 401, 'unauthorized');
    });

    test('a login whose hash has another cost writes it again at the config cost', async () => {
      const h = await newHarness();
      const old = await hashPassword(USER_PASSWORD, { memoryKiB: 32, iterations: 1, parallelism: 1 });
      await h.store.setPassword(h.memberId, old, h.clock.now, '');
      await h.login('member@example.com', USER_PASSWORD);
      expect((await h.store.findLogin('member@example.com')).passwordHash).toStartWith('$argon2id$v=19$m=64,t=1,p=1$');
      await h.login('member@example.com', USER_PASSWORD);
    });

    test('the trusted origin preflight is answered with credentials; another origin passes through without CORS', async () => {
      const h = await newHarness();
      const r = await h.do('OPTIONS', '/auth/login', undefined, 'Origin', 'https://app.example.com', 'Access-Control-Request-Method', 'POST', 'Access-Control-Request-Headers', 'content-type');
      expect(r.status).toBe(204);
      expect(Object.fromEntries(['access-control-allow-origin', 'access-control-allow-credentials', 'access-control-allow-methods', 'access-control-allow-headers', 'access-control-max-age', 'vary'].map(n => [n, r.headers.get(n)]))).toEqual({
        'access-control-allow-origin': 'https://app.example.com',
        'access-control-allow-credentials': 'true',
        'access-control-allow-methods': 'POST',
        'access-control-allow-headers': 'content-type',
        'access-control-max-age': '600',
        vary: 'Origin, Access-Control-Request-Method, Access-Control-Request-Headers',
      });
      const other = await h.do('OPTIONS', '/auth/login', undefined, 'Origin', 'https://evil.example', 'Access-Control-Request-Method', 'POST');
      expect(other.status).not.toBe(204);
      expect(other.headers.get('access-control-allow-origin')).toBeNull();
      expect(other.headers.get('vary')).toBe('Origin');
    });
  });
}

describe('identity routes', () => {
  const sqlite = databaseKinds[0]!;

  test('every operation of the contract has a handler, and the routes follow the sets options', async () => {
    const db = await sqlite.open();
    open.push(db);
    const { service } = await harness(db);
    for (const op of identityOperations) expect(identityHandler(service, op.name)).toBeFunction();
    expect(identityHandler(service, 'refresh')).toBeUndefined();
    const paths = (routes: { method: string; path: string }[]) => routes.map(r => `${r.method} ${r.path}`).join(', ');
    expect(paths(identityRoutes(service, { sessions: { path: 'account', noLogin: true } }))).toBe('GET /account/me, GET /account/capabilities');
    const plain = paths(identityRoutes(service, { sessions: {} }));
    expect(plain).toContain('POST /auth/login');
    expect(plain).not.toContain('register');
    expect(identityRoutes(service, { sessions: { register: true }, administration: {} })).toHaveLength(identityOperations.length);
    expect(paths(identityRoutes(service, { administration: { path: '/staff/' } }))).toStartWith('POST /staff/users, GET /staff/users');
    expect(identityOperations.find(op => op.name === 'grantRole')!.path).toBe('users/{id}/roles/{roleId}');
  });

  test('mounted through mountManualOperation, a handler takes the principal the router gate established', async () => {
    const db = await sqlite.open();
    open.push(db);
    const h = await harness(db);
    let authenticated = 0;
    const authenticate = identityAuthenticator(h.service);
    const counting = Object.assign(
      async (ctx: Parameters<typeof authenticate>[0]) => {
        authenticated++;
        return authenticate(ctx);
      },
      { readsAuthorization: true }
    );
    const app = new Hono();
    const spec = (name: string, method: OperationSpec['method'], path: string, auth: OperationSpec['auth']): OperationSpec => ({
      name,
      namespace: 'account',
      method,
      path,
      pathParams: [],
      queryParams: [],
      bodyParams: [],
      auth,
      manual: true,
    });
    const specs = [
      spec('login', 'POST', '/api/auth/login', { public: true, required: false, permissions: [] }),
      spec('me', 'GET', '/api/auth/me', { public: false, required: true, permissions: [] }),
      spec('listUsers', 'GET', '/api/auth/admin/users', { public: false, required: true, permissions: ['identity.users.read'] }),
    ];
    for (const s of specs) mountManualOperation(app, s, identityHandler(h.service, s.name), { authenticate: counting });

    const login = await app.request('/api/auth/login', { method: 'POST', body: JSON.stringify({ login: 'member@example.com', password: USER_PASSWORD }) });
    const token = ((await login.json()) as { data: { token: string } }).data.token;
    const me = await app.request('/api/auth/me', { headers: { authorization: `Bearer ${token}` } });
    expect(me.status).toBe(200);
    expect(authenticated).toBe(1);
    // The gate refuses before the handler runs.
    const users = await app.request('/api/auth/admin/users', { headers: { authorization: `Bearer ${token}` } });
    expect([users.status, ((await users.json()) as { code: string }).code]).toEqual([403, 'forbidden']);
    const bad = await app.request('/api/auth/me', { headers: { authorization: 'Bearer' } });
    expect([bad.status, ((await bad.json()) as { code: string }).code]).toEqual([401, 'unauthorized']);
  });

  test('identityOperationSpec writes the contract: public login and register, a caller elsewhere, the administration permissions, the rate limits', async () => {
    const db = await sqlite.open();
    open.push(db);
    const { service } = await harness(db);
    const specs = identityRoutes(service, { sessions: { register: true }, administration: {} }).map(route =>
      identityOperationSpec(service, route, { basePath: '/api/', rateLimitPerMinute: 600, timeoutSeconds: 5 })
    );
    const row = (spec: OperationSpec) =>
      `${spec.method} ${spec.path} ${spec.auth.public ? 'public' : spec.auth.permissions.join(',') || 'caller'} ${spec.rateLimitPerMinute}`;
    expect(specs.map(row)).toEqual([
      'POST /api/auth/login public 10',
      'POST /api/auth/logout caller 600',
      'GET /api/auth/me caller 600',
      'GET /api/auth/capabilities caller 600',
      'POST /api/auth/password caller 10',
      'POST /api/auth/register public 5',
      'POST /api/auth/admin/users identity.users.write 600',
      'GET /api/auth/admin/users identity.users.read 600',
      'GET /api/auth/admin/users/{id} identity.users.read 600',
      'POST /api/auth/admin/users/{id}/disable identity.users.write 600',
      'POST /api/auth/admin/users/{id}/enable identity.users.write 600',
      'PUT /api/auth/admin/users/{id}/password identity.users.write 600',
      'GET /api/auth/admin/roles identity.roles.read 600',
      'POST /api/auth/admin/roles identity.roles.write 600',
      'PUT /api/auth/admin/roles/{id} identity.roles.write 600',
      'DELETE /api/auth/admin/roles/{id} identity.roles.write 600',
      'PUT /api/auth/admin/users/{id}/roles/{roleId} identity.roles.write 600',
      'DELETE /api/auth/admin/users/{id}/roles/{roleId} identity.roles.write 600',
    ]);
    const grant = specs.find(spec => spec.name === 'grantRole')!;
    expect(grant).toMatchObject({ namespace: 'identity', manual: true, timeoutSeconds: 5 });
    expect(grant.input).toBeUndefined();
    expect(grant.pathParams.map(param => param.name)).toEqual(['id', 'roleId']);
    expect(identityOperationSpec(service, { operation: 'login', method: 'POST', path: '/auth/login' }).rateLimitPerMinute).toBe(10);
    expect(identityOperationSpec(service, { operation: 'me', method: 'GET', path: '/auth/me' }).rateLimitPerMinute).toBeUndefined();
    expect(() => identityOperationSpec(service, { operation: 'refresh' as never, method: 'POST', path: '/auth/refresh' })).toThrow(/not an operation/);
  });

  test('mountIdentityOperations runs each route through the pipeline: the gate, the rate limit, the request id', async () => {
    const db = await sqlite.open();
    open.push(db);
    const h = await harness(db);
    const app = new Hono();
    const mounted = mountIdentityOperations(app, h.service, { sessions: {}, administration: {}, basePath: '/api' }, { rateLimit: { now: () => 0 } });
    expect(mounted.map(spec => spec.name)).not.toContain('register');
    const login = (password: string) =>
      app.request('/api/auth/login', { method: 'POST', body: JSON.stringify({ login: 'member@example.com', password }), headers: { 'x-request-id': 'req-1' } });
    const first = await login(USER_PASSWORD);
    expect(first.status).toBe(200);
    expect(((await first.json()) as { meta: { requestId: string } }).meta.requestId).toBe('req-1');
    const token = ((await (await login(USER_PASSWORD)).json()) as { data: { token: string } }).data.token;
    // The gate refuses the member before the handler runs, with the router's problem.
    const users = await app.request('/api/auth/admin/users', { headers: { authorization: `Bearer ${token}` } });
    expect([users.status, ((await users.json()) as { code: string }).code]).toEqual([403, 'forbidden']);
    const me = await app.request('/api/auth/me', { headers: { authorization: `Bearer ${token}` } });
    expect(((await me.json()) as { data: { user: { id: string } } }).data.user.id).toBe(h.memberId);
    for (let i = 2; i < 10; i++) await login('wrong password');
    const limited = await login(USER_PASSWORD);
    expect([limited.status, limited.headers.get('retry-after')]).toEqual([429, '6']);
  });

  test('identityRouterOptions authenticates with the identity service, and refuses no service or an authenticate beside it', async () => {
    const db = await sqlite.open();
    open.push(db);
    const { service } = await harness(db);
    const options = identityRouterOptions({ identity: service, bodyLimitBytes: 10 });
    expect(options.bodyLimitBytes).toBe(10);
    expect(options.identity).toBe(service);
    expect(options.authenticate.readsAuthorization).toBe(true);
    expect(() => identityRouterOptions({} as { identity?: IdentityService })).toThrow(/buildRouter: options.identity is required/);
    expect(() => identityRouterOptions({ identity: service, authenticate: async () => null }, 'engineApp')).toThrow(
      /engineApp: options.authenticate is refused beside options.identity/
    );
  });

  test('the authenticator returns no principal for a request with no credential, and the identity principal otherwise', async () => {
    const db = await sqlite.open();
    open.push(db);
    const h = await harness(db);
    const authenticate = identityAuthenticator(h.service);
    expect(authenticate.readsAuthorization).toBe(true);
    const context = (headers: Record<string, string>) =>
      ({ method: 'GET', headers: new Headers(headers), raw: new Request('http://api.example.com/x', { headers }) }) as never;
    expect(await authenticate(context({}))).toBeNull();
    const token = await h.login('admin@example.com', ADMIN_PASSWORD);
    const principal = await authenticate(context({ authorization: `Bearer ${token}` }));
    expect(principal).toMatchObject({ subject: h.adminId, permissions: ['identity', 'orders'] });
    expect(principal!.claims).toMatchObject({ login: 'admin@example.com', name: 'Admin', transport: 'bearer' });
    expect(identityPrincipalOf(principal)).toMatchObject({ id: h.adminId, transport: 'bearer' });
    expect(identityPrincipalOf({ subject: h.adminId, permissions: [] })).toBeUndefined();
  });

  test('routesOf reads the generated operation table by the router rule', () => {
    const table = {
      listOrders: { ...ORDERS, name: 'listOrders', method: 'GET', auth: { public: true, required: false, permissions: ['ignored'] } },
      createOrder: ORDERS,
      syncStock: { ...ORDERS, name: 'syncStock', namespace: 'stock-admin', service: { mode: 'require', from: [] } },
      importStock: { ...ORDERS, name: 'importStock', namespace: 'stock-admin', service: { mode: 'allow', from: [] } },
    } satisfies Record<string, OperationSpec>;
    expect(routesOf(table)).toEqual([
      { operationId: 'OrdersListOrdersHandler', requiresAuth: false, permissions: [], requireOwnership: false, serviceOnly: false },
      { operationId: 'OrdersCreateOrderHandler', requiresAuth: true, permissions: ['orders.write'], requireOwnership: false, serviceOnly: false },
      { operationId: 'StockAdminSyncStockHandler', requiresAuth: true, permissions: ['orders.write'], requireOwnership: false, serviceOnly: true },
      { operationId: 'StockAdminImportStockHandler', requiresAuth: true, permissions: ['orders.write'], requireOwnership: false, serviceOnly: false },
    ]);
  });

  test('a login for an account that does not exist verifies a hash at the config cost', async () => {
    const db = await sqlite.open();
    open.push(db);
    const h = await harness(db, { password: { argon2: { memoryKiB: 16384, iterations: 2, parallelism: 1 } } });
    const fastest = async (body: unknown) => {
      let best = Infinity;
      for (let i = 0; i < 3; i++) {
        const start = performance.now();
        expectReply(await h.do('POST', '/auth/login', body), 401, 'invalid_credentials');
        best = Math.min(best, performance.now() - start);
      }
      return best;
    };
    const wrong = await fastest({ login: 'member@example.com', password: 'wrong password' });
    const unknown = await fastest({ login: 'nobody@example.com', password: 'wrong password' });
    const refused = await fastest({ login: 'nobody', password: 'wrong password' });
    expect(unknown).toBeGreaterThan(wrong / 3);
    expect(refused).toBeGreaterThan(wrong / 3);
  });
});
