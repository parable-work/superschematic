import { afterEach, describe, expect, test } from 'bun:test';
import { Hono } from 'hono';
import { IdentityService, hashPassword, mountIdentityRoutes, type RegistrationOptions, type SqlIdentityStore } from './index';
import { at, databaseKinds, type TestDatabase } from './fixture.testing';

/*
The registration modes of D81 end to end through Hono's app.request, on each
database the store tests run on, as the Go runtime's registration_test.go
holds them: approval-gated registration (202 and no session, a pending
login refused with its own code, the administrator's pending list and
approveUser), default roles, and the first administrator, two of whom at
once make one.
*/

const TEST_CONFIG = { password: { argon2: { memoryKiB: 64, iterations: 1, parallelism: 1 } }, trustedOrigins: ['https://app.example.com'] };
const ADMIN_PASSWORD = 'admin password';
const USER_PASSWORD = 'user password';
const NEW_PASSWORD = 'new user password';

const APPROVAL: RegistrationOptions = { approval: true, defaultRoles: ['member'] };
const FIRST_USER: RegistrationOptions = { approval: true, defaultRoles: ['member'], firstUser: { role: 'admin', permissions: ['identity', 'orders'] } };

interface Reply {
  status: number;
  body: Record<string, any>;
  cookies: string[];
}

interface Harness {
  store: SqlIdentityStore;
  adminId: string;
  memberId: string;
  do(method: string, path: string, body?: unknown, ...headers: string[]): Promise<Reply>;
  login(login: string, password: string): Promise<string>;
}

const open: TestDatabase[] = [];
afterEach(async () => {
  while (open.length > 0) await open.pop()!.close();
});

/** A service with registration over db, and, when seed is true, an admin holding admin (identity, orders) and a member holding none. */
async function harness(db: TestDatabase, registration: RegistrationOptions, seed = true): Promise<Harness> {
  const store = db.store();
  const now = at();
  const service = new IdentityService({ store, config: TEST_CONFIG, now: () => now, registration });
  const app = new Hono();
  mountIdentityRoutes(app, service, { sessions: { register: true }, administration: {} });
  let adminId = '';
  let memberId = '';
  if (seed) {
    const params = service.config.password.argon2;
    const admin = await store.createUser({ login: 'admin@example.com', name: 'Admin', passwordHash: await hashPassword(ADMIN_PASSWORD, params), at: now });
    const member = await store.createUser({ login: 'member@example.com', name: 'Member', passwordHash: await hashPassword(USER_PASSWORD, params), at: now });
    const role = await store.createRole('admin', ['identity', 'orders']);
    await store.grantRole(admin.id, role.id, now);
    adminId = admin.id;
    memberId = member.id;
  }
  const h: Harness = {
    store,
    adminId,
    memberId,
    async do(method, path, body, ...headers) {
      const init: RequestInit = { method, headers: [] as [string, string][] };
      for (let i = 0; i < headers.length; i += 2) (init.headers as [string, string][]).push([headers[i]!, headers[i + 1]!]);
      if (body !== undefined) {
        init.body = JSON.stringify(body);
        (init.headers as [string, string][]).push(['content-type', 'application/json']);
      }
      const response = await app.request(`http://api.example.com${path}`, init);
      const text = await response.text();
      return { status: response.status, body: text === '' ? {} : JSON.parse(text), cookies: response.headers.getSetCookie() };
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

const bearer = (token: string) => ['Authorization', `Bearer ${token}`];
const names = (roles: { name: string }[]) => roles.map(role => role.name);

for (const kind of databaseKinds) {
  describe(`registration on ${kind.name}`, () => {
    const newHarness = async (registration: RegistrationOptions, seed = true) => {
      const db = await kind.open();
      open.push(db);
      return harness(db, registration, seed);
    };

    test('under approval register answers 202 with no session, login is refused as pending, and approval lets the user in with the default roles', async () => {
      const h = await newHarness(APPROVAL);
      for (const session of ['cookie', 'bearer']) {
        const login = `${session}@example.com`;
        const r = await h.do('POST', '/auth/register', { login, name: 'New', password: NEW_PASSWORD, session }, 'Sec-Fetch-Site', 'same-origin');
        expectReply(r, 202);
        expect(r.cookies).toEqual([]);
        expect(r.body.data).toEqual({ pending: true, user: { id: r.body.data.user.id, login, name: 'New' } });
      }

      expectReply(await h.do('POST', '/auth/login', { login: 'bearer@example.com', password: NEW_PASSWORD }), 403, 'pending_approval');
      expectReply(await h.do('POST', '/auth/login', { login: 'bearer@example.com', password: 'wrong password' }), 401, 'invalid_credentials');

      const admin = bearer(await h.login('admin@example.com', ADMIN_PASSWORD));
      const pending = await h.do('GET', '/auth/admin/users?pending=true', undefined, ...admin);
      expectReply(pending, 200);
      expect(pending.body.data.map((u: { login: string }) => u.login)).toEqual(['bearer@example.com', 'cookie@example.com']);
      for (const user of pending.body.data) {
        expect({ pending: user.pending, disabled: user.disabled, roles: names(user.roles) }).toEqual({ pending: true, disabled: false, roles: ['member'] });
      }
      expect((await h.do('GET', '/auth/admin/users?pending=false', undefined, ...admin)).body.data).toHaveLength(2);
      expect((await h.do('GET', '/auth/admin/users', undefined, ...admin)).body.data).toHaveLength(4);
      expectReply(await h.do('GET', '/auth/admin/users?pending=yes', undefined, ...admin), 400, 'bad_request');

      const id = pending.body.data[0].id as string;
      const member = bearer(await h.login('member@example.com', USER_PASSWORD));
      expectReply(await h.do('POST', `/auth/admin/users/${id}/approve`, undefined, ...member), 403, 'forbidden');
      expectReply(await h.do('POST', `/auth/admin/users/${id}/approve`), 401, 'unauthorized');
      const approved = await h.do('POST', `/auth/admin/users/${id}/approve`, undefined, ...admin);
      expectReply(approved, 200);
      expect(approved.body.data.pending).toBe(false);
      expectReply(await h.do('POST', `/auth/admin/users/${id}/approve`, undefined, ...admin), 409, 'not_pending');
      expectReply(await h.do('POST', `/auth/admin/users/${h.adminId}/approve`, undefined, ...admin), 409, 'not_pending');
      expectReply(await h.do('POST', '/auth/admin/users/1aB2cD3eF4gH5iJ6kL7mN8/approve', undefined, ...admin), 404, 'not_found');

      const me = await h.do('GET', '/auth/me', undefined, ...bearer(await h.login('bearer@example.com', NEW_PASSWORD)));
      expect(names(me.body.data.roles)).toEqual(['member']);
      expectReply(await h.do('POST', '/auth/login', { login: 'cookie@example.com', password: NEW_PASSWORD }), 403, 'pending_approval');
    });

    test("approval needs the caller to cover the roles register granted the user", async () => {
      const h = await newHarness(APPROVAL);
      await h.store.createRole('member', ['orders.read']);
      const clerk = await h.store.createRole('clerk', ['identity.users']);
      await h.store.grantRole(h.memberId, clerk.id, at());
      expectReply(await h.do('POST', '/auth/register', { login: 'new@example.com', password: NEW_PASSWORD }), 202);
      const admin = bearer(await h.login('admin@example.com', ADMIN_PASSWORD));
      const id = (await h.do('GET', '/auth/admin/users?pending=true', undefined, ...admin)).body.data[0].id as string;
      const r = await h.do('POST', `/auth/admin/users/${id}/approve`, undefined, ...bearer(await h.login('member@example.com', USER_PASSWORD)));
      expectReply(r, 403, 'forbidden');
      expect(r.body.details.permissions).toEqual(['orders.read']);
    });

    test('open registration signs the user in with the default roles, creating one no role has the name of', async () => {
      const h = await newHarness({ defaultRoles: ['member', 'reader'] });
      await h.store.createRole('reader', ['orders.read']);
      const r = await h.do('POST', '/auth/register', { login: 'new@example.com', password: NEW_PASSWORD });
      expectReply(r, 200);
      expect(r.body.data.pending).toBeUndefined();
      const me = await h.do('GET', '/auth/me', undefined, ...bearer(r.body.data.token));
      expect(names(me.body.data.roles)).toEqual(['member', 'reader']);
      expect(me.body.data.permissions).toEqual(['orders.read']);
    });

    test('the first registrant of a database with no grant is the administrator, signed in; the next waits for them', async () => {
      const h = await newHarness(FIRST_USER, false);
      const first = await h.do('POST', '/auth/register', { login: 'ada@example.com', password: "ada's password" });
      expectReply(first, 200);
      expect(first.body.data.pending).toBe(false);
      expect(first.body.data.expiresAt).toBeString();
      const token = first.body.data.token as string;
      const me = await h.do('GET', '/auth/me', undefined, ...bearer(token));
      expect(names(me.body.data.roles)).toEqual(['admin', 'member']);
      expect(me.body.data.permissions).toEqual(['identity', 'orders']);

      expectReply(await h.do('POST', '/auth/register', { login: 'bob@example.com', password: "bob's password" }), 202);
      expectReply(await h.do('POST', '/auth/login', { login: 'bob@example.com', password: "bob's password" }), 403, 'pending_approval');
      const bob = (await h.do('GET', '/auth/admin/users?pending=true', undefined, ...bearer(token))).body.data;
      expect(bob).toHaveLength(1);
      expectReply(await h.do('POST', `/auth/admin/users/${bob[0].id}/approve`, undefined, ...bearer(token)), 200);
      await h.login('bob@example.com', "bob's password");
    });

    test('a database with a grant has its administrator, so no registrant becomes one', async () => {
      const h = await newHarness(FIRST_USER);
      expectReply(await h.do('POST', '/auth/register', { login: 'eve@example.com', password: "eve's password" }), 202);
    });

    test('registrations that race on an empty database make one administrator', async () => {
      const h = await newHarness(FIRST_USER, false);
      const replies = await Promise.all(
        Array.from({ length: 8 }, (_, i) => h.do('POST', '/auth/register', { login: `racer${i}@example.com`, password: "racer's password" }))
      );
      expect(replies.filter(r => r.status === 200)).toHaveLength(1);
      expect(replies.filter(r => r.status === 202)).toHaveLength(7);
      const users = await h.store.listUsers();
      expect(users.filter(u => u.roles.some(role => role.name === 'admin'))).toHaveLength(1);
    });
  });
}

test('a registration a store cannot write is refused when the service is built', () => {
  const store = {} as SqlIdentityStore;
  expect(() => new IdentityService({ store, registration: { firstUser: { role: 'admin', permissions: [] } } })).toThrow(/needs a role and at least one permission/u);
  expect(() => new IdentityService({ store, registration: { firstUser: { role: 'admin', permissions: ['a..b'] } } })).toThrow(/is not dotted segments/u);
  expect(() => new IdentityService({ store, registration: { defaultRoles: [''] } })).toThrow(/a default role has no name/u);
});
