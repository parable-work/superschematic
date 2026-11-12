import { afterEach, describe, expect, test } from 'bun:test';
import { Hono } from 'hono';
import { mountManualOperation, mountOperation } from '../hono';
import type { OperationSpec } from '../operation';
import { uuidToDb } from '../uuid';
import {
  API_KEY_LENGTH,
  API_KEY_PREFIX,
  DEFAULT_API_KEY_MAX_TTL_SECONDS,
  IdentityDescriptorError,
  IdentityService,
  effectivePermissions,
  hashPassword,
  hashToken,
  identityAuthenticator,
  identityCors,
  identityHandler,
  identityOperationSpec,
  identityOperations,
  identityPrincipalOf,
  identityRoutes,
  isStoreError,
  mountIdentityOperations,
  mountIdentityRoutes,
  narrowRoles,
  newApiKey,
  parseIdentityConfig,
  parseIdentityDescriptor,
  type IdentityOperationName,
  type IdentityPrincipal,
  type IdentityRoute,
  type IdentityServiceOptions,
  type IdentityStore,
  type IdentityStoreErrorKind,
  type SqlIdentityStore,
  type StoredApiKey,
  type StoredUser,
} from './index';
import { HOUR, MINUTE, SECOND, at, databaseKinds, descriptorJSON, descriptorWith, type TestDatabase } from './fixture.testing';

/*
API keys (D114): the store's rows, authentication by a key, and the five
operations, on each database the store tests run on: the cases the Go
runtime's apikeys_test.go holds it to, in its order.
*/

// The refusals' details, the same text in every runtime.
const SESSION_ONLY_DETAIL = 'An API key does not act on its own account: sign in with a session';
const NOT_HELD_DETAIL = 'An API key names only permissions its user holds';
const NO_API_KEYS_DETAIL = "API keys are not available: this database's types hold no API key table";

/** An id in a key's form that no row has. */
const MISSING = '00000000-0000-4000-8000-000000000000';

const DAY = 24 * HOUR;

const open: TestDatabase[] = [];
afterEach(async () => {
  while (open.length > 0) await open.pop()!.close();
});

async function rejection(promise: Promise<unknown>): Promise<unknown> {
  try {
    await promise;
  } catch (error) {
    return error;
  }
  throw new Error('the promise resolved');
}

/** The status and detail of the problem a promise rejects with. */
async function problemOf(promise: Promise<unknown>): Promise<[status: unknown, detail: unknown]> {
  const refusal = (await rejection(promise)) as { status?: unknown; message?: unknown };
  return [refusal.status, refusal.message];
}

async function expectKind(promise: Promise<unknown>, kind: IdentityStoreErrorKind): Promise<void> {
  const error = await rejection(promise);
  if (!isStoreError(error, kind)) throw new Error(`want a ${kind} refusal, got ${String(error)}`);
}

function createUser(store: IdentityStore, login: string, name: string): Promise<StoredUser> {
  return store.createUser({ login, name, passwordHash: `hash-of-${login}`, at: at() });
}

const hashOf = (n: number) => hashToken(`token-${n}`);

/** Creates user a key that lasts an hour from created. */
function createKey(store: IdentityStore, user: StoredUser, name: string, tokenHash: string, permissions: string[] | null, created: Date): Promise<StoredApiKey> {
  return store.createApiKey({ userId: user.id, name, tokenHash, permissions, createdAt: created, expiresAt: new Date(created.getTime() + HOUR) });
}

const keyNames = (keys: readonly { name: string }[]) => keys.map(key => key.name).join(',');

/** The fixture's descriptor as one written before D114: without its apiKey member. */
const withoutApiKeys = () => descriptorWith(d => delete d.apiKey);

for (const kind of databaseKinds) {
  describe(`API keys in the store on ${kind.name}`, () => {
    test('a key is created with its permissions, found by its token hash, touched once per interval, listed newest first, revoked by its user, and ends with its user', async () => {
      const db = await kind.open();
      open.push(db);
      const s = db.store();
      expect(s.hasApiKeys()).toBe(true);
      const alice = await createUser(s, 'alice@example.com', 'Alice');
      const bob = await createUser(s, 'bob@example.com', 'Bob');

      const all = await createKey(s, alice, 'ci', hashOf(1), null, at());
      expect(all).toEqual({ id: all.id, userId: alice.id, name: 'ci', permissions: null, createdAt: at(), expiresAt: at(HOUR), lastUsedAt: null, revokedAt: null });
      expect(all.id).not.toBe('');
      expect(all.id).not.toContain('-');
      let rec = await s.findApiKey(hashOf(1));
      expect(rec.apiKey).toEqual(all);
      expect(rec.user).toEqual({ id: alice.id, login: 'alice@example.com', name: 'Alice', disabled: false, pending: false, roles: [] });
      await expectKind(s.findApiKey(hashOf(99)), 'not_found');
      // A second key with a token hash already kept is refused.
      await rejection(createKey(s, bob, 'copy', hashOf(1), null, at()));
      await expectKind(s.createApiKey({ userId: 'not a key', name: 'x', tokenHash: hashOf(98), permissions: null, createdAt: at(), expiresAt: at(HOUR) }), 'not_found');

      // Permissions are kept in order; null is all, and an empty list none.
      const some = await createKey(s, alice, 'reader', hashOf(2), ['orders.read', 'issues'], at(MINUTE));
      expect(some.permissions).toEqual(['orders.read', 'issues']);
      expect((await s.findApiKey(hashOf(2))).apiKey.permissions).toEqual(['orders.read', 'issues']);
      await createKey(s, alice, 'nothing', hashOf(3), [], at(2 * MINUTE));
      expect((await s.findApiKey(hashOf(3))).apiKey.permissions).toEqual([]);
      const [kept] = await db.query(`SELECT "permissions" IS NULL AS "is_null" FROM "user_api_key" WHERE "token_hash" = ?`, [hashOf(1)]);
      expect(Boolean(kept!.is_null)).toBe(true);
      if (db.name !== 'postgres') {
        const [row] = await db.query(`SELECT "created_at", "permissions" FROM "user_api_key" WHERE "token_hash" = ?`, [hashOf(2)]);
        expect(row).toEqual({ created_at: '2026-10-08T12:01:00.000Z', permissions: '["orders.read","issues"]' });
      }

      // The first touch writes lastUsedAt; one within the interval does
      // not; one after it does.
      const touch = async (when: Date) => {
        await s.touchApiKey(all.id, when, new Date(when.getTime() - MINUTE));
        return (await s.findApiKey(hashOf(1))).apiKey.lastUsedAt;
      };
      expect(await touch(at(SECOND))).toEqual(at(SECOND));
      expect(await touch(at(30 * SECOND))).toEqual(at(SECOND));
      expect(await touch(at(2 * MINUTE))).toEqual(at(2 * MINUTE));
      await expectKind(s.touchApiKey('not a key', at(), at()), 'not_found');

      // Newest first, then by the database's key descending; an expired
      // key is listed, and another user's is not.
      await s.createApiKey({ userId: alice.id, name: 'expired', tokenHash: hashOf(4), permissions: null, createdAt: at(-2 * HOUR), expiresAt: at(-HOUR) });
      for (let n = 0; n < 4; n++) await createKey(s, alice, 'twin', hashOf(10 + n), null, at(3 * MINUTE));
      const bobs = await createKey(s, bob, "bob's", hashOf(5), null, at(HOUR));
      let listed = await s.listApiKeys(alice.id);
      expect(keyNames(listed)).toBe('twin,twin,twin,twin,nothing,reader,ci,expired');
      // The database's keys descending: the hyphenated UUID the store
      // orders by, not its base62 form.
      const twins = listed.slice(0, 4).map(key => uuidToDb(key.id));
      expect(twins).toEqual([...twins].sort().reverse());
      expect(listed.at(-1)).toMatchObject({ expiresAt: at(-HOUR), userId: alice.id });
      expect(listed[6]).toMatchObject({ name: 'ci', lastUsedAt: at(2 * MINUTE), permissions: null });
      expect(keyNames(await s.listApiKeys(bob.id))).toBe("bob's");
      const carol = await createUser(s, 'carol@example.com', 'Carol');
      expect(await s.listApiKeys(carol.id)).toEqual([]);
      await expectKind(s.listApiKeys('not a key'), 'not_found');

      // A user revokes their own live key, once.
      await s.revokeApiKey(alice.id, some.id, at(10 * MINUTE));
      expect((await s.findApiKey(hashOf(2))).apiKey.revokedAt).toEqual(at(10 * MINUTE));
      const refused: Record<string, [user: string, key: string]> = {
        'a key revoked already': [alice.id, some.id],
        "another user's key": [alice.id, bobs.id],
        'an unknown key': [alice.id, MISSING],
        'an id that is no key': [alice.id, 'not a key'],
        'an unknown user': [MISSING, all.id],
        'an id that is no user': ['not a key', all.id],
      };
      for (const [name, [user, key]] of Object.entries(refused)) {
        expect([name, isStoreError(await rejection(s.revokeApiKey(user, key, at(11 * MINUTE))), 'not_found')]).toEqual([name, true]);
      }
      expect((await s.findApiKey(hashOf(2))).apiKey.revokedAt).toEqual(at(10 * MINUTE));
      expect((await s.findApiKey(hashOf(5))).apiKey.revokedAt).toBeNull();
      expect((await s.findApiKey(hashOf(1))).apiKey.revokedAt).toBeNull();
      listed = await s.listApiKeys(alice.id);
      expect(keyNames(listed)).not.toContain('reader');
      expect(listed).toHaveLength(7);

      // Disabling a user revokes their live keys with their sessions, and
      // enabling brings none back.
      await s.createSession({ userId: alice.id, tokenHash: hashOf(20), createdAt: at(), expiresAt: at(HOUR) });
      await s.setDisabled(alice.id, true, at(20 * MINUTE));
      rec = await s.findApiKey(hashOf(1));
      expect({ disabled: rec.user.disabled, revokedAt: rec.apiKey.revokedAt }).toEqual({ disabled: true, revokedAt: at(20 * MINUTE) });
      expect((await s.findSession(hashOf(20))).session.revokedAt).not.toBeNull();
      expect((await s.findApiKey(hashOf(2))).apiKey.revokedAt).toEqual(at(10 * MINUTE));
      expect((await s.findApiKey(hashOf(5))).apiKey.revokedAt).toBeNull();
      await s.setDisabled(alice.id, false, at(21 * MINUTE));
      rec = await s.findApiKey(hashOf(1));
      expect({ disabled: rec.user.disabled, revokedAt: rec.apiKey.revokedAt }).toEqual({ disabled: false, revokedAt: at(20 * MINUTE) });
      expect(await s.listApiKeys(alice.id)).toEqual([]);

      // A key's user reads as pending while they wait for approval.
      const dave = await s.createUser({ login: 'dave@example.com', name: 'Dave', passwordHash: 'x', at: at(), pending: true });
      await createKey(s, dave, 'early', hashOf(6), null, at());
      expect((await s.findApiKey(hashOf(6))).user.pending).toBe(true);

      // A deleted user's keys go with them.
      await db.query(`DELETE FROM "user" WHERE "email" = ?`, ['bob@example.com']);
      await expectKind(s.findApiKey(hashOf(5)), 'not_found');
      const [left] = await db.query(`SELECT count(*) AS "n" FROM "user_api_key" WHERE "token_hash" = ?`, [hashOf(5)]);
      expect(Number(left!.n)).toBe(0);
    });
  });
}

test('a descriptor without an API key table: no key is found, the other key methods refuse, and a disable reads no key table', async () => {
  const db = await databaseKinds[0]!.open();
  open.push(db);
  await db.query(`DROP TABLE "user_api_key"`);
  const s = db.store(withoutApiKeys());
  const alice = await createUser(s, 'alice@example.com', 'Alice');
  expect(s.hasApiKeys()).toBe(false);
  expect(s.descriptor.apiKey).toBeUndefined();
  await expectKind(s.findApiKey(hashOf(1)), 'not_found');
  const refused: Record<string, () => Promise<unknown>> = {
    createApiKey: () => createKey(s, alice, 'ci', hashOf(1), null, at()),
    listApiKeys: () => s.listApiKeys(alice.id),
    revokeApiKey: () => s.revokeApiKey(alice.id, MISSING, at()),
    touchApiKey: () => s.touchApiKey(MISSING, at(), at()),
  };
  for (const [name, call] of Object.entries(refused)) {
    expect([name, isStoreError(await rejection(call()), 'no_api_keys')]).toEqual([name, true]);
  }
  // Disabling revokes the sessions and reads no key table.
  await s.createSession({ userId: alice.id, tokenHash: hashOf(2), createdAt: at(), expiresAt: at(HOUR) });
  await s.setDisabled(alice.id, true, at(MINUTE));
  expect((await s.findSession(hashOf(2))).session.revokedAt).not.toBeNull();
});

test('the descriptor apiKey member is optional, and when present every name of it is there, without a NUL, and no member is unknown', () => {
  expect(parseIdentityDescriptor(descriptorJSON).apiKey).toEqual({
    table: 'user_api_key',
    columns: {
      id: 'id',
      user: 'user_id',
      name: 'name',
      tokenHash: 'token_hash',
      permissions: 'permissions',
      createdAt: 'created_at',
      expiresAt: 'expires_at',
      lastUsedAt: 'last_used_at',
      revokedAt: 'revoked_at',
    },
  });
  const before = parseIdentityDescriptor(withoutApiKeys());
  expect(before.apiKey).toBeUndefined();
  expect('apiKey' in before).toBe(false);
  // The member stays between credential and role, where the DB build writes it.
  expect(Object.keys(parseIdentityDescriptor(descriptorJSON))).toEqual(['version', 'user', 'session', 'credential', 'apiKey', 'role', 'roleGrant']);
  const cases: Record<string, [edit: (d: Record<string, any>) => void, want: string]> = {
    'an empty table': [d => (d.apiKey.table = ''), 'apiKey.table is empty'],
    'an empty column': [d => (d.apiKey.columns.tokenHash = ''), 'apiKey.columns.tokenHash is empty'],
    'a column left out': [d => delete d.apiKey.columns.lastUsedAt, 'apiKey.columns.lastUsedAt must be a string'],
    'a NUL in a name': [d => (d.apiKey.columns.permissions = 'per\u0000missions'), 'apiKey.columns.permissions holds a NUL'],
    'an unknown member': [d => (d.apiKey.type = 'UserApiKey'), 'unknown member apiKey.type'],
    'an unknown column': [d => (d.apiKey.columns.usedAt = 'used_at'), 'unknown member apiKey.columns.usedAt'],
    'a column in another case': [d => (d.apiKey.columns.TokenHash = 'token_hash'), 'unknown member apiKey.columns.TokenHash'],
    'the member in another case': [d => (d.apikey = d.apiKey), 'unknown member apikey'],
  };
  for (const [name, [edit, want]] of Object.entries(cases)) {
    let refusal: unknown;
    try {
      parseIdentityDescriptor(descriptorWith(edit));
    } catch (error) {
      refusal = error;
    }
    expect([name, refusal instanceof IdentityDescriptorError, String((refusal as Error | undefined)?.message).includes(want)]).toEqual([name, true, true]);
  }
});

/** A config at a low argon2 cost, so a test hashes quickly. */
const TEST_CONFIG = { password: { argon2: { memoryKiB: 64, iterations: 1, parallelism: 1 } }, trustedOrigins: ['https://app.example.com'] };
const ADMIN_PASSWORD = 'admin password';
const USER_PASSWORD = 'user password';

/** The route requirements capabilities answers for. */
const ROUTES: IdentityRoute[] = [
  { operationId: 'OrdersList', requiresAuth: false, permissions: [], requireOwnership: false, serviceOnly: false },
  { operationId: 'AuthMe', requiresAuth: true, permissions: [], requireOwnership: false, serviceOnly: false },
  { operationId: 'OrdersCreate', requiresAuth: true, permissions: ['orders.write'], requireOwnership: false, serviceOnly: false },
  { operationId: 'IdentityListUsers', requiresAuth: true, permissions: ['identity.users.read'], requireOwnership: false, serviceOnly: false },
  { operationId: 'StockSync', requiresAuth: false, permissions: [], requireOwnership: false, serviceOnly: true },
];

const CREATE_ORDER: OperationSpec = {
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
const LIST_ORDERS: OperationSpec = { ...CREATE_ORDER, name: 'listOrders', method: 'GET', auth: { public: false, required: true, permissions: ['orders.read', 'orders.write'] } };

/** A response: its status, its JSON body and the Max-Age of each cookie it sets. */
interface Reply {
  status: number;
  body: Record<string, any>;
  cookies: number[];
}

class Clock {
  now = at();
  advance(ms: number): void {
    this.now = new Date(this.now.getTime() + ms);
  }
}

/** A createApiKey answer. */
type Created = Record<string, any> & { id: string; token: string };

interface Harness {
  db: TestDatabase;
  store: SqlIdentityStore;
  service: IdentityService;
  clock: Clock;
  /** The app do sends to: the harness's own, or one a test mounts. */
  app: Hono;
  /** admin holds the role admin (identity, orders); member holds none. */
  adminId: string;
  memberId: string;
  do(method: string, path: string, body?: unknown, ...headers: string[]): Promise<Reply>;
  login(login: string, password: string): Promise<string>;
  /** Makes the caller an API key and returns createApiKey's answer. */
  createKey(caller: string[], body: Record<string, unknown>): Promise<Created>;
}

interface HarnessOptions {
  config?: unknown;
  /** The store's descriptor; the fixture's when absent. */
  descriptor?: string;
  /** The service's API key options. */
  service?: Pick<IdentityServiceOptions, 'apiKeys' | 'apiKeyAdministration'>;
}

/** Both sets' API key operations turned on. */
const KEYS_ON = { apiKeys: true, apiKeyAdministration: true } as const;

/**
 * A service over a store on db, mounted on a Hono app as mountIdentityRoutes
 * lists its routes, with app routes through the runtime's pipeline behind
 * identityAuthenticator: POST /orders needs orders.write, and GET /orders
 * orders.read or orders.write.
 */
async function harness(db: TestDatabase, options: HarnessOptions = {}): Promise<Harness> {
  const store = db.store(options.descriptor);
  const clock = new Clock();
  const service = new IdentityService({ store, config: options.config ?? TEST_CONFIG, now: () => clock.now, routes: ROUTES, ...options.service });
  const app = new Hono();
  app.use('*', identityCors(service));
  mountIdentityRoutes(app, service, { sessions: { register: true }, administration: {} });
  const runtime = { authenticate: identityAuthenticator(service) };
  mountOperation(app, CREATE_ORDER, async ctx => ctx.principal?.claims?.name ?? null, runtime);
  mountOperation(app, LIST_ORDERS, async ctx => ctx.principal?.claims?.name ?? null, runtime);

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
      const response = await h.app.request(`http://api.example.com${path}`, init);
      const text = await response.text();
      return {
        status: response.status,
        body: /json/u.test(response.headers.get('content-type') ?? '') ? JSON.parse(text) : {},
        cookies: response.headers.getSetCookie().map(cookie => Number(/; Max-Age=(-?\d+)/u.exec(cookie)?.[1] ?? NaN)),
      };
    },
    async login(login, password) {
      const r = await h.do('POST', '/auth/login', { login, password });
      expect(r.status).toBe(200);
      return r.body.data.token as string;
    },
    async createKey(caller, body) {
      const r = await h.do('POST', '/auth/keys', body, ...caller);
      expectReply(r, 200);
      const token = r.body.data.token as string;
      expect([token.length, token.startsWith(API_KEY_PREFIX)]).toEqual([API_KEY_LENGTH, true]);
      return r.body.data as Created;
    },
  };
  return h;
}

function expectReply(r: Reply, status: number, code?: string): void {
  expect({ status: r.status, code: r.body.code }).toEqual({ status, code: code ?? r.body.code });
}

/** Fails unless r is the problem with status, code and detail. */
function expectDetail(r: Reply, status: number, code: string, detail: string): void {
  expect({ status: r.status, code: r.body.code, detail: r.body.detail }).toEqual({ status, code, detail });
}

/** Fails unless r is 400 bad_request refusing field by validator. */
function expectField(r: Reply, field: string, validator: string): void {
  expectReply(r, 400, 'bad_request');
  expect((r.body.errors?.[field] ?? []).map((refusal: { validator: string }) => refusal.validator)).toEqual([validator]);
}

/** The answer of an operation the contract types as returning true. */
function expectDone(r: Reply): void {
  expect({ status: r.status, data: r.body.data, requestId: typeof r.body.meta?.requestId }).toEqual({ status: 200, data: true, requestId: 'string' });
}

const bearer = (token: string) => ['Authorization', `Bearer ${token}`];
const cookie = (token: string) => ['Cookie', `__Host-session=${token}`];
/** The bearer header of a createApiKey answer's token. */
const keyOf = (created: Created) => bearer(created.token);
/** The names of the keys a list answers, in its order. */
const names = (r: Reply) => keyNames(r.body.data as { name: string }[]);

for (const kind of databaseKinds) {
  describe(`API keys on ${kind.name}`, () => {
    /** A harness with both sets' API key operations turned on. */
    const keyHarness = async (config?: unknown) => {
      const db = await kind.open();
      open.push(db);
      return harness(db, { config, service: KEYS_ON });
    };

    test('createApiKey answers the token once and the list shows the key without it; a key in Authorization is its user and writes lastUsedAt once per touch interval', async () => {
      const h = await keyHarness();
      const member = bearer(await h.login('member@example.com', USER_PASSWORD));
      expectReply(await h.do('POST', '/auth/keys', { name: 'ci' }), 401, 'unauthorized');
      expectReply(await h.do('GET', '/auth/keys'), 401, 'unauthorized');
      const empty = await h.do('GET', '/auth/keys', undefined, ...member);
      expect([empty.status, empty.body.data]).toEqual([200, []]);

      const created = await h.createKey(member, { name: '  ci runner \n' });
      const { token, id } = created;
      // A key with every permission has no permissions member, and one
      // never used no lastUsedAt.
      const shown = { id, name: 'ci runner', createdAt: '2026-10-08T12:00:00Z', expiresAt: '2027-10-08T12:00:00Z' };
      expect(created).toEqual({ ...shown, token });
      expect(Object.keys(created)).toEqual(['id', 'name', 'createdAt', 'expiresAt', 'token']);
      expect(id).not.toBe('');
      expect(id).not.toContain('-');
      const rec = await h.store.findApiKey(hashToken(token));
      expect({ id: rec.apiKey.id, user: rec.user.id, name: rec.apiKey.name }).toEqual({ id, user: h.memberId, name: 'ci runner' });

      const list = await h.do('GET', '/auth/keys', undefined, ...member);
      expectReply(list, 200);
      expect(list.body.data).toEqual([shown]);
      expect(Object.keys(list.body.data[0])).toEqual(['id', 'name', 'createdAt', 'expiresAt']);

      // The key authenticates as its user, in Authorization alone.
      h.clock.advance(MINUTE);
      const me = await h.do('GET', '/auth/me', undefined, ...bearer(token));
      expectReply(me, 200);
      expect(me.body.data.user).toEqual({ id: h.memberId, login: 'member@example.com', name: 'Member' });
      const p = await h.service.authenticateToken(token, 'bearer');
      expect(p).toMatchObject({ id: h.memberId, apiKeyId: id, sessionId: '', transport: 'bearer' });
      // A cookie's token is no key.
      expect(await problemOf(h.service.authenticateToken(token, 'cookie'))).toEqual([401, 'Authentication required']);
      const inCookie = await h.do('GET', '/auth/me', undefined, ...cookie(token));
      expectReply(inCookie, 401, 'unauthorized');
      expect(inCookie.cookies).toEqual([0]);
      expectReply(await h.do('GET', '/auth/me', undefined, ...bearer(`${token.slice(0, -1)}!`)), 401, 'unauthorized');
      expectReply(await h.do('GET', '/auth/me', undefined, ...bearer(newApiKey())), 401, 'unauthorized');

      // lastUsedAt is the first request's time, and moves once the touch
      // interval has passed.
      const lastUsed = async () => (await h.do('GET', '/auth/keys', undefined, ...member)).body.data.at(-1).lastUsedAt;
      expect(await lastUsed()).toBe('2026-10-08T12:01:00Z');
      h.clock.advance(59 * SECOND);
      expectReply(await h.do('GET', '/auth/me', undefined, ...bearer(token)), 200);
      expect(await lastUsed()).toBe('2026-10-08T12:01:00Z');
      h.clock.advance(2 * SECOND);
      expectReply(await h.do('GET', '/auth/me', undefined, ...bearer(token)), 200);
      expect(await lastUsed()).toBe('2026-10-08T12:02:01Z');

      // Newest first, and each user's own.
      h.clock.advance(MINUTE);
      await h.createKey(member, { name: 'second' });
      expect(names(await h.do('GET', '/auth/keys', undefined, ...member))).toBe('second,ci runner');
      const admin = bearer(await h.login('admin@example.com', ADMIN_PASSWORD));
      expect((await h.do('GET', '/auth/keys', undefined, ...admin)).body.data).toEqual([]);
    });

    test('the idle timeout and the session lifetime end a session and no API key, which ends at its own expiry', async () => {
      const h = await keyHarness({ sessionTtlSeconds: 3600, idleTimeoutSeconds: 600, touchIntervalSeconds: 60, password: TEST_CONFIG.password });
      const me = (credential: string[]) => h.do('GET', '/auth/me', undefined, ...credential);
      const signedIn = bearer(await h.login('member@example.com', USER_PASSWORD));
      const key = keyOf(await h.createKey(signedIn, { name: 'ci' }));

      h.clock.advance(601 * SECOND);
      expectReply(await me(signedIn), 401, 'unauthorized');
      expectReply(await me(key), 200);
      // Unused for longer than the idle timeout, and past a session's
      // lifetime.
      h.clock.advance(2 * HOUR);
      expectReply(await me(key), 200);
      h.clock.advance(364 * DAY);
      expectReply(await me(key), 200);
      h.clock.advance(DAY);
      expectReply(await me(key), 401, 'unauthorized');
    });

    test('a key ends at its expiry, when it is revoked, and when its user is disabled, for good; a password change ends the sessions and not the key', async () => {
      const h = await keyHarness();
      const me = (credential: string[]) => h.do('GET', '/auth/me', undefined, ...credential);
      let member = bearer(await h.login('member@example.com', USER_PASSWORD));
      const admin = bearer(await h.login('admin@example.com', ADMIN_PASSWORD));

      // Its expiry: the key lives while now is before expiresAt.
      const short = await h.createKey(member, { name: 'short', expiresInSeconds: 60 });
      expect(short.expiresAt).toBe('2026-10-08T12:01:00Z');
      h.clock.advance(59 * SECOND);
      expectReply(await me(keyOf(short)), 200);
      h.clock.advance(SECOND);
      expectReply(await me(keyOf(short)), 401, 'unauthorized');
      // An expired key is listed.
      expect(names(await h.do('GET', '/auth/keys', undefined, ...member))).toBe('short');

      // A revocation, which its user makes once.
      const revoked = await h.createKey(member, { name: 'revoked' });
      const adminsOwn = await h.createKey(admin, { name: "admin's" });
      expectReply(await me(keyOf(revoked)), 200);
      const path = `/auth/keys/${revoked.id}`;
      expectReply(await h.do('DELETE', path), 401, 'unauthorized');
      expectDone(await h.do('DELETE', path, undefined, ...member));
      expectReply(await me(keyOf(revoked)), 401, 'unauthorized');
      const missing: Record<string, string> = {
        'a key revoked already': revoked.id,
        "another user's key": adminsOwn.id,
        'an unknown key': MISSING,
        'an id that is no key': 'not-a-key',
      };
      for (const [name, id] of Object.entries(missing)) {
        const r = await h.do('DELETE', `/auth/keys/${id}`, undefined, ...member);
        expect([name, r.status, r.body.code, r.body.detail]).toEqual([name, 404, 'not_found', 'API key not found']);
      }
      expectReply(await me(keyOf(adminsOwn)), 200);
      // A revoked key is not listed.
      expect(names(await h.do('GET', '/auth/keys', undefined, ...member))).toBe('short');

      // A password change ends the user's other sessions, not their keys.
      const lasting = await h.createKey(member, { name: 'lasting' });
      const other = bearer(await h.login('member@example.com', USER_PASSWORD));
      expectDone(await h.do('POST', '/auth/password', { current: USER_PASSWORD, password: 'a new password' }, ...member));
      expectReply(await me(other), 401, 'unauthorized');
      expectReply(await me(keyOf(lasting)), 200);
      expectDone(await h.do('PUT', `/auth/admin/users/${h.memberId}/password`, { password: 'reset password' }, ...admin));
      expectReply(await me(member), 401, 'unauthorized');
      expectReply(await me(keyOf(lasting)), 200);

      // Its user disabled: the keys are revoked with the sessions, and
      // enabling brings none back.
      expectReply(await h.do('POST', `/auth/admin/users/${h.memberId}/disable`, undefined, ...admin), 200);
      expectReply(await me(keyOf(lasting)), 401, 'unauthorized');
      expectReply(await h.do('POST', `/auth/admin/users/${h.memberId}/enable`, undefined, ...admin), 200);
      expectReply(await me(keyOf(lasting)), 401, 'unauthorized');
      member = bearer(await h.login('member@example.com', 'reset password'));
      expect((await h.do('GET', '/auth/keys', undefined, ...member)).body.data).toEqual([]);
      expectReply(await me(keyOf(adminsOwn)), 200);

      // A user who waits for approval is no caller, by a key either.
      const pending = await h.store.createUser({ login: 'pending@example.com', name: '', passwordHash: 'x', at: h.clock.now, pending: true });
      const token = newApiKey();
      await h.store.createApiKey({
        userId: pending.id,
        name: 'early',
        tokenHash: hashToken(token),
        permissions: null,
        createdAt: h.clock.now,
        expiresAt: new Date(h.clock.now.getTime() + HOUR),
      });
      expectReply(await me(bearer(token)), 401, 'unauthorized');
      await h.store.approveUser(pending.id);
      expectReply(await me(bearer(token)), 200);
    });

    test('createApiKey takes a name of 1 to 100 characters once trimmed, a lifetime of 1 to the config longest, and 1 to 100 valid permissions, none twice', async () => {
      const h = await keyHarness({ apiKeyMaxTtlSeconds: 3600, password: TEST_CONFIG.password });
      const admin = bearer(await h.login('admin@example.com', ADMIN_PASSWORD));
      const post = (body: unknown) => h.do('POST', '/auth/keys', body, ...admin);
      let made = 0;
      const create = (body: Record<string, unknown>) => {
        made++;
        return h.createKey(admin, body);
      };

      // Without expiresInSeconds a key lasts the config's hour.
      expect((await create({ name: 'default' })).expiresAt).toBe('2026-10-08T13:00:00Z');
      expect((await create({ name: 'longest', expiresInSeconds: 3600 })).expiresAt).toBe('2026-10-08T13:00:00Z');
      expect((await create({ name: 'shortest', expiresInSeconds: 1 })).expiresAt).toBe('2026-10-08T12:00:01Z');
      expectField(await post({ name: 'x', expiresInSeconds: 3601 }), 'expiresInSeconds', 'max');
      expectField(await post({ name: 'x', expiresInSeconds: 0 }), 'expiresInSeconds', 'min');
      expectField(await post({ name: 'x', expiresInSeconds: -5 }), 'expiresInSeconds', 'min');

      expectField(await post({}), 'name', 'required');
      expectField(await post({ name: '' }), 'name', 'required');
      expectField(await post({ name: ' \t\n ' }), 'name', 'required');
      expectField(await post({ name: 'n'.repeat(101) }), 'name', 'maxLength');
      expectField(await post({ name: '\u00e9'.repeat(101) }), 'name', 'maxLength');
      expect((await create({ name: 'n'.repeat(100) })).name).toBe('n'.repeat(100));
      // Characters are counted, not bytes, and after the trim.
      expect((await create({ name: `  ${'\u00e9'.repeat(100)}  ` })).name).toBe('\u00e9'.repeat(100));
      // A character is a code point, and the trim is of Unicode's
      // White_Space: U+0085 is trimmed and U+FEFF kept.
      expect((await create({ name: '\u{1f511}'.repeat(100) })).name).toBe('\u{1f511}'.repeat(100));
      expectField(await post({ name: '\u{1f511}'.repeat(101) }), 'name', 'maxLength');
      expectField(await post({ name: '\u0085\u00a0\u3000' }), 'name', 'required');
      expect((await create({ name: '\u0085\ufeffbom\u2028' })).name).toBe('\ufeffbom');

      expectField(await post({ name: 'x', permissions: [] }), 'permissions', 'listMin');
      const many = Array.from({ length: 101 }, (_, i) => `orders.p${i}`);
      expectField(await post({ name: 'x', permissions: many }), 'permissions', 'listMax');
      expect((await create({ name: 'hundred', permissions: many.slice(0, 100) })).permissions).toHaveLength(100);
      const invalid: Record<string, [permissions: string[], refused: string[]]> = {
        'a permission of the wrong form': [['orders.read', 'orders read', 'a..b'], ['orders read', 'a..b']],
        'a permission named twice': [['orders.read', 'orders.write', 'orders.read'], ['orders.read']],
        'an empty permission': [[''], ['']],
      };
      for (const [name, [permissions, refused]] of Object.entries(invalid)) {
        const r = await post({ name: 'x', permissions });
        expect([name, r.status, r.body.code, r.body.details]).toEqual([name, 422, 'invalid_permission', { permissions: refused }]);
      }

      // Every refused field is named at once, before any permission is
      // read.
      const several = await post({ name: ' ', expiresInSeconds: 0, permissions: [] });
      expectReply(several, 400, 'bad_request');
      expect(Object.keys(several.body.errors).sort()).toEqual(['expiresInSeconds', 'name', 'permissions']);
      expectField(await post({ name: '', permissions: ['orders read'] }), 'name', 'required');

      // A null optional member is an absent one.
      const nulls = await create({ name: 'nulls', expiresInSeconds: null, permissions: null });
      expect(['permissions' in nulls, nulls.expiresAt]).toEqual([false, '2026-10-08T13:00:00Z']);

      const notTheInput: Record<string, unknown> = {
        'a fractional lifetime': { name: 'x', expiresInSeconds: 1.5 },
        'a lifetime as a string': { name: 'x', expiresInSeconds: '60' },
        'a name that is no string': { name: 5 },
        'permissions as a string': { name: 'x', permissions: 'orders.read' },
        'an unknown member': { name: 'x', ttl: 60 },
        'a member in another case': { Name: 'x' },
        'a body that is a list': [],
      };
      for (const [name, body] of Object.entries(notTheInput)) {
        const r = await post(body);
        expect([name, r.status, r.body.code, r.body.errors]).toEqual([name, 400, 'bad_request', undefined]);
      }
      // A whole number written with a fraction or an exponent is no
      // integer, as Go's decoder reads it.
      for (const lifetime of ['60.0', '6e1']) {
        const response = await h.app.request('http://api.example.com/auth/keys', {
          method: 'POST',
          headers: [admin as [string, string], ['content-type', 'application/json']],
          body: `{"name": "x", "expiresInSeconds": ${lifetime}}`,
        });
        expect([lifetime, response.status, ((await response.json()) as { code: string }).code]).toEqual([lifetime, 400, 'bad_request']);
      }
      // A refused create wrote no row.
      expect((await h.do('GET', '/auth/keys', undefined, ...admin)).body.data).toHaveLength(made);
    });

    test('a key made with permissions holds those alone, each while a role of its user covers it; a key names only what its user holds; one made with none holds all', async () => {
      const h = await keyHarness();
      const admin = bearer(await h.login('admin@example.com', ADMIN_PASSWORD));
      const created = await h.createKey(admin, { name: 'reader', permissions: ['orders.read', 'identity.users.read'] });
      expect(created.permissions).toEqual(['orders.read', 'identity.users.read']);
      const key = keyOf(created);
      expect((await h.do('GET', '/auth/keys', undefined, ...admin)).body.data[0].permissions).toEqual(['orders.read', 'identity.users.read']);

      // The admin's role holds identity and orders: the key holds what it
      // names of them, and its routes follow.
      const holds = async (roles: string[], permissions: string[]) => {
        const me = await h.do('GET', '/auth/me', undefined, ...key);
        expectReply(me, 200);
        expect({ roles: me.body.data.roles.map((role: { name: string }) => role.name), permissions: me.body.data.permissions }).toEqual({ roles, permissions });
      };
      await holds(['admin'], ['orders.read', 'identity.users.read']);
      expect((await h.do('GET', '/auth/capabilities', undefined, ...key)).body.data.operations).toEqual({
        OrdersList: true,
        AuthMe: true,
        OrdersCreate: false,
        IdentityListUsers: true,
      });
      expectReply(await h.do('GET', '/orders', undefined, ...key), 200);
      expectReply(await h.do('POST', '/orders', undefined, ...key), 403, 'forbidden');
      expectReply(await h.do('GET', '/auth/admin/users', undefined, ...key), 200);
      expectReply(await h.do('POST', '/auth/admin/users', { login: 'x@example.com', password: USER_PASSWORD }, ...key), 403, 'forbidden');
      expectReply(await h.do('GET', '/auth/admin/roles', undefined, ...key), 403, 'forbidden');

      // The role loses orders, and the key with it; given back, the key
      // holds it again.
      const roles = await h.store.listRoles();
      expect(roles).toHaveLength(1);
      const rewrite = (...permissions: string[]) => h.store.updateRole(roles[0]!.id, 'admin', permissions);
      await rewrite('identity');
      await holds(['admin'], ['identity.users.read']);
      expectReply(await h.do('GET', '/orders', undefined, ...key), 403, 'forbidden');
      // A role holding less than the key names gives it no part of it.
      await rewrite('identity', 'orders.read.archive');
      await holds(['admin'], ['identity.users.read']);
      await rewrite('billing');
      await holds([], []);
      expectReply(await h.do('GET', '/auth/admin/users', undefined, ...key), 403, 'forbidden');
      await rewrite('identity', 'orders');
      await holds(['admin'], ['orders.read', 'identity.users.read']);

      // A key made without permissions holds all its user does.
      const whole = await h.createKey(admin, { name: 'whole' });
      expect('permissions' in whole).toBe(false);
      expect((await h.do('GET', '/auth/me', undefined, ...keyOf(whole))).body.data.permissions).toEqual(['identity', 'orders']);
      expectReply(await h.do('POST', '/orders', undefined, ...keyOf(whole)), 200);

      // A key names only what its user holds.
      const notHeld = async (caller: string[], permissions: string[], uncovered: string[]) => {
        const r = await h.do('POST', '/auth/keys', { name: 'wide', permissions }, ...caller);
        expectDetail(r, 403, 'forbidden', NOT_HELD_DETAIL);
        expect(r.body.details).toEqual({ permissions: uncovered });
      };
      const member = bearer(await h.login('member@example.com', USER_PASSWORD));
      await notHeld(member, ['orders.read'], ['orders.read']);
      await notHeld(admin, ['orders.read', 'billing.read'], ['billing.read']);
      const reader = await h.store.createRole('reader', ['orders.read']);
      await h.store.grantRole(h.memberId, reader.id, h.clock.now);
      await notHeld(member, ['orders'], ['orders']);
      await notHeld(member, ['orders.read', 'orders.write'], ['orders.write']);
      // A refused create wrote the member no key.
      expect((await h.do('GET', '/auth/keys', undefined, ...member)).body.data).toEqual([]);
      const narrow = await h.createKey(member, { name: 'archive', permissions: ['orders.read.archive'] });
      expect((await h.do('GET', '/auth/me', undefined, ...keyOf(narrow))).body.data.permissions).toEqual(['orders.read.archive']);
      expectReply(await h.do('GET', '/orders', undefined, ...keyOf(narrow)), 403, 'forbidden');
    });

    test('a caller an API key authenticated neither makes, lists nor revokes keys, signs out nor changes the password, while me and capabilities answer it', async () => {
      const h = await keyHarness();
      const member = bearer(await h.login('member@example.com', USER_PASSWORD));
      const first = await h.createKey(member, { name: 'first' });
      const key = keyOf(first);
      const calls: [method: string, path: string, body?: unknown][] = [
        ['POST', '/auth/keys', { name: 'minted' }],
        ['GET', '/auth/keys'],
        ['DELETE', `/auth/keys/${first.id}`],
        ['POST', '/auth/logout'],
        ['POST', '/auth/password', { current: USER_PASSWORD, password: 'a new password' }],
        // The refusal comes before a member of the input is checked.
        ['POST', '/auth/keys', { name: '' }],
        ['POST', '/auth/password', { current: USER_PASSWORD, password: 'short' }],
      ];
      for (const [method, path, body] of calls) {
        const r = await h.do(method, path, body, ...key);
        expect([method, path, r.status, r.body.code, r.body.detail]).toEqual([method, path, 403, 'forbidden', SESSION_ONLY_DETAIL]);
      }

      // A body that is not the operation's input is refused as one first,
      // once its caller is known: with no credential it is not read.
      expectReply(await h.do('POST', '/auth/keys', { title: 'minted' }, ...key), 400, 'bad_request');
      expectReply(await h.do('POST', '/auth/keys', { title: 'minted' }), 401, 'unauthorized');

      // Nothing was done, and the key is a caller still.
      expectReply(await h.do('GET', '/auth/me', undefined, ...key), 200);
      const caps = await h.do('GET', '/auth/capabilities', undefined, ...key);
      expectReply(caps, 200);
      expect(caps.body.data.operations).toEqual({ OrdersList: true, AuthMe: true, OrdersCreate: false, IdentityListUsers: false });
      expect(names(await h.do('GET', '/auth/keys', undefined, ...member))).toBe('first');
      await h.login('member@example.com', USER_PASSWORD);

      // The service refuses the same principal whoever calls it.
      const p = await h.service.authenticateToken(first.token, 'bearer');
      expect(await problemOf(h.service.logout(p))).toEqual([403, SESSION_ONLY_DETAIL]);
      const nobody: IdentityPrincipal = { id: '', login: '', name: '', roles: [], permissions: [], sessionId: '', apiKeyId: '', transport: 'bearer' };
      expect(await problemOf(h.service.createApiKey(nobody, { name: 'x' }))).toEqual([401, 'Authentication required']);
    });

    test('a caller with users.read lists any user keys and one with users.write revokes them, by a session or by a key that holds the permission; no one else does', async () => {
      const h = await keyHarness();
      const admin = bearer(await h.login('admin@example.com', ADMIN_PASSWORD));
      const member = bearer(await h.login('member@example.com', USER_PASSWORD));
      const one = await h.createKey(member, { name: 'one' });
      h.clock.advance(MINUTE);
      const two = await h.createKey(member, { name: 'two' });
      const adminsOwn = await h.createKey(admin, { name: "admin's", permissions: ['identity.users'] });
      const keys = `/auth/admin/users/${h.memberId}/keys`;

      const list = await h.do('GET', keys, undefined, ...admin);
      expectReply(list, 200);
      expect(list.body.data).toEqual([
        { id: two.id, name: 'two', createdAt: '2026-10-08T12:01:00Z', expiresAt: '2027-10-08T12:01:00Z' },
        { id: one.id, name: 'one', createdAt: '2026-10-08T12:00:00Z', expiresAt: '2027-10-08T12:00:00Z' },
      ]);
      expectReply(await h.do('GET', keys, undefined, ...member), 403, 'forbidden');
      expectReply(await h.do('GET', keys), 401, 'unauthorized');
      expectDetail(await h.do('GET', `/auth/admin/users/${MISSING}/keys`, undefined, ...admin), 404, 'not_found', 'User not found');
      expectDetail(await h.do('GET', '/auth/admin/users/not-a-key/keys', undefined, ...admin), 404, 'not_found', 'User not found');
      expect(names(await h.do('GET', `/auth/admin/users/${h.adminId}/keys`, undefined, ...admin))).toBe("admin's");

      expectReply(await h.do('DELETE', `${keys}/${one.id}`, undefined, ...member), 403, 'forbidden');
      expectReply(await h.do('DELETE', `${keys}/${one.id}`), 401, 'unauthorized');
      expectReply(await h.do('GET', '/auth/me', undefined, ...keyOf(one)), 200);
      expectDone(await h.do('DELETE', `${keys}/${one.id}`, undefined, ...admin));
      expectReply(await h.do('GET', '/auth/me', undefined, ...keyOf(one)), 401, 'unauthorized');
      const missing: Record<string, string> = {
        'a key revoked already': `${keys}/${one.id}`,
        "another user's key": `/auth/admin/users/${h.adminId}/keys/${two.id}`,
        'an unknown key': `${keys}/${MISSING}`,
        'an id that is no key': `${keys}/not-a-key`,
        'a key of an unknown user': `/auth/admin/users/${MISSING}/keys/${two.id}`,
        'a key of an id that is none': `/auth/admin/users/not-a-key/keys/${two.id}`,
      };
      for (const [name, path] of Object.entries(missing)) {
        const r = await h.do('DELETE', path, undefined, ...admin);
        expect([name, r.status, r.body.code, r.body.detail]).toEqual([name, 404, 'not_found', 'API key not found']);
      }
      expectReply(await h.do('GET', '/auth/me', undefined, ...keyOf(two)), 200);
      expect(names(await h.do('GET', keys, undefined, ...admin))).toBe('two');

      // users.read lists and does not revoke.
      const auditor = await h.do('POST', '/auth/admin/roles', { name: 'auditor', permissions: ['identity.users.read'] }, ...admin);
      expectReply(auditor, 200);
      expectReply(await h.do('PUT', `/auth/admin/users/${h.memberId}/roles/${auditor.body.data.id}`, undefined, ...admin), 200);
      const adminKeys = `/auth/admin/users/${h.adminId}/keys`;
      expectReply(await h.do('GET', adminKeys, undefined, ...member), 200);
      expectReply(await h.do('DELETE', `${adminKeys}/${adminsOwn.id}`, undefined, ...member), 403, 'forbidden');

      // A key that holds the permission administers as a session does:
      // only the routes of its own account are a session's alone.
      expectReply(await h.do('GET', keys, undefined, ...keyOf(adminsOwn)), 200);
      expectDone(await h.do('DELETE', `${keys}/${two.id}`, undefined, ...keyOf(adminsOwn)));
      expectReply(await h.do('GET', '/auth/me', undefined, ...keyOf(two)), 401, 'unauthorized');
      // A key the member's role does not reach revokes nothing.
      const three = await h.createKey(member, { name: 'three' });
      expectReply(await h.do('DELETE', `${adminKeys}/${adminsOwn.id}`, undefined, ...keyOf(three)), 403, 'forbidden');
    });

    test('over a store whose descriptor names no API key table, a bearer API key is no credential and every key operation is 404, after its caller is admitted', async () => {
      const db = await kind.open();
      open.push(db);
      await db.query(`DROP TABLE "user_api_key"`);
      const h = await harness(db, { descriptor: withoutApiKeys(), service: KEYS_ON });
      expect(h.store.hasApiKeys()).toBe(false);
      expectReply(await h.do('GET', '/auth/me', undefined, ...bearer(newApiKey())), 401, 'unauthorized');

      const member = bearer(await h.login('member@example.com', USER_PASSWORD));
      const admin = bearer(await h.login('admin@example.com', ADMIN_PASSWORD));
      const user = `/auth/admin/users/${h.memberId}`;
      const calls: [method: string, path: string, body: unknown, caller: string[]][] = [
        ['POST', '/auth/keys', { name: 'ci' }, member],
        ['POST', '/auth/keys', { name: '' }, member],
        ['GET', '/auth/keys', undefined, member],
        ['DELETE', `/auth/keys/${MISSING}`, undefined, member],
        ['GET', `${user}/keys`, undefined, admin],
        ['GET', `/auth/admin/users/${MISSING}/keys`, undefined, admin],
        ['DELETE', `${user}/keys/${MISSING}`, undefined, admin],
      ];
      for (const [method, path, body, caller] of calls) {
        const r = await h.do(method, path, body, ...caller);
        expect([method, path, r.status, r.body.code, r.body.detail]).toEqual([method, path, 404, 'not_found', NO_API_KEYS_DETAIL]);
      }
      expectReply(await h.do('GET', '/auth/keys'), 401, 'unauthorized');
      expectReply(await h.do('GET', `${user}/keys`, undefined, ...member), 403, 'forbidden');
      expectReply(await h.do('DELETE', `${user}/keys/${MISSING}`, undefined, ...member), 403, 'forbidden');

      // The user model runs as it did: a disable revokes the sessions and
      // reads no key table.
      expectReply(await h.do('POST', `${user}/disable`, undefined, ...admin), 200);
      expectReply(await h.do('GET', '/auth/me', undefined, ...member), 401, 'unauthorized');
    });
  });
}

describe('API key routes', () => {
  const sqlite = databaseKinds[0]!;
  const sqliteHarness = async (options?: HarnessOptions) => {
    const db = await sqlite.open();
    open.push(db);
    return harness(db, options);
  };
  const keyPaths = (routes: readonly { method: string; path: string }[]) => routes.filter(r => r.path.includes('/keys')).map(r => `${r.method} ${r.path}`);
  const OWN = ['POST /auth/keys', 'GET /auth/keys', 'DELETE /auth/keys/{id}'];
  const USERS = ['GET /auth/admin/users/{id}/keys', 'DELETE /auth/admin/users/{id}/keys/{keyId}'];
  const KEY_OPERATIONS = ['createApiKey', 'listApiKeys', 'revokeApiKey', 'listUserApiKeys', 'revokeUserApiKey'];

  test('a set has its API key routes only when the service turns them on for it, and a key authenticates whether they are on or not', async () => {
    const h = await sqliteHarness();
    const sets = { sessions: { register: true }, administration: {} };

    expect([h.service.apiKeys, h.service.apiKeyAdministration]).toEqual([false, false]);
    expect(keyPaths(identityRoutes(h.service, sets))).toEqual([]);
    const member = await h.login('member@example.com', USER_PASSWORD);
    const admin = await h.login('admin@example.com', ADMIN_PASSWORD);
    const off = [
      await h.do('POST', '/auth/keys', { name: 'ci' }, ...bearer(member)),
      await h.do('GET', '/auth/keys', undefined, ...bearer(member)),
      await h.do('DELETE', `/auth/keys/${MISSING}`, undefined, ...bearer(member)),
      await h.do('GET', `/auth/admin/users/${h.memberId}/keys`, undefined, ...bearer(admin)),
      await h.do('DELETE', `/auth/admin/users/${h.memberId}/keys/${MISSING}`, undefined, ...bearer(admin)),
    ];
    // The app's own answer, for a route it does not have.
    expect(off.map(r => [r.status, r.body.code])).toEqual(off.map(() => [404, undefined]));

    // Authentication by a key follows the table, not the routes.
    const p = await h.service.authenticateToken(member, 'bearer');
    const created = await h.service.createApiKey(p, { name: 'direct' });
    expectReply(await h.do('GET', '/auth/me', undefined, ...bearer(created.token)), 200);

    const build = (options: Pick<IdentityServiceOptions, 'apiKeys' | 'apiKeyAdministration'>) =>
      new IdentityService({ store: h.store, config: h.service.config, ...options });
    const onlyOwn = build({ apiKeys: true });
    expect([keyPaths(identityRoutes(onlyOwn, sets)), onlyOwn.apiKeys, onlyOwn.apiKeyAdministration]).toEqual([OWN, true, false]);
    const onlyUsers = build({ apiKeyAdministration: true });
    expect([keyPaths(identityRoutes(onlyUsers, sets)), onlyUsers.apiKeys, onlyUsers.apiKeyAdministration]).toEqual([USERS, false, true]);
    const both = build(KEYS_ON);
    expect(keyPaths(identityRoutes(both, sets))).toEqual([...OWN, ...USERS]);
    // Every set with its API keys routes every operation.
    expect(identityRoutes(both, sets)).toHaveLength(identityOperations.length);
    // A set the API does not declare has no route, its keys' included.
    expect(keyPaths(identityRoutes(both, { administration: {} }))).toEqual(USERS);
    expect(keyPaths(identityRoutes(both, { sessions: { register: true } }))).toEqual(OWN);
    // The routes follow each set's path, and login: false leaves them.
    expect(keyPaths(identityRoutes(both, { sessions: { path: 'account', noLogin: true }, administration: { path: '/manage/' } }))).toEqual([
      'POST /account/keys',
      'GET /account/keys',
      'DELETE /account/keys/{id}',
      'GET /manage/users/{id}/keys',
      'DELETE /manage/users/{id}/keys/{keyId}',
    ]);

    for (const op of identityOperations) {
      const isKey = KEY_OPERATIONS.includes(op.name);
      expect([op.name, op.apiKeys === true]).toEqual([op.name, isKey]);
      if (isKey) expect([op.name, op.administration]).toEqual([op.name, op.name === 'listUserApiKeys' || op.name === 'revokeUserApiKey']);
    }
    expect(identityOperations.filter(op => op.apiKeys).map(op => op.name)).toEqual(KEY_OPERATIONS);
  });

  test('a service has a handler for each API key operation whether its options are on or not, as a generated router mounts them one by one', async () => {
    const h = await sqliteHarness();
    // The generated router's way: each operation the schema declares,
    // with its table entry and the runtime's handler, through the
    // pipeline. The handlers read the key's id as {id} under keys, and the
    // user's as {id} and the key's as {keyId} under users.
    const app = new Hono();
    const runtime = { authenticate: identityAuthenticator(h.service) };
    let established = 0;
    const routes: [method: 'GET' | 'POST' | 'DELETE', path: string, operation: IdentityOperationName][] = [
      ['POST', '/api/account/keys', 'createApiKey'],
      ['GET', '/api/account/keys', 'listApiKeys'],
      ['DELETE', '/api/account/keys/{id}', 'revokeApiKey'],
      ['GET', '/api/admin/users/{id}/keys', 'listUserApiKeys'],
      ['DELETE', '/api/admin/users/{id}/keys/{keyId}', 'revokeUserApiKey'],
      ['POST', '/auth/login', 'login'],
    ];
    for (const [method, path, operation] of routes) {
      const handler = identityHandler(h.service, operation);
      expect([operation, typeof handler]).toEqual([operation, 'function']);
      mountManualOperation(
        app,
        identityOperationSpec(h.service, { operation, method, path }),
        async (c, ctx) => {
          if (identityPrincipalOf(ctx?.principal)) established++;
          return handler!(c, ctx);
        },
        runtime
      );
    }
    h.app = app;
    const member = bearer(await h.login('member@example.com', USER_PASSWORD));
    const admin = bearer(await h.login('admin@example.com', ADMIN_PASSWORD));
    const post = async (name: string) => {
      const created = await h.do('POST', '/api/account/keys', { name }, ...member);
      expectReply(created, 200);
      return created.body.data as Created;
    };
    const one = await post('one');
    const two = await post('two');
    // The handler took the principal the router's gate established.
    expect(established).toBe(2);
    expect((await h.do('GET', '/api/account/keys', undefined, ...member)).body.data).toHaveLength(2);
    expectDone(await h.do('DELETE', `/api/account/keys/${one.id}`, undefined, ...member));
    const users = `/api/admin/users/${h.memberId}/keys`;
    expect(names(await h.do('GET', users, undefined, ...admin))).toBe('two');
    // The gate refuses a caller without the permission before the handler runs.
    expectReply(await h.do('GET', users, undefined, ...member), 403, 'forbidden');
    // The user's id and the key's are not read one for the other.
    expectReply(await h.do('DELETE', `/api/admin/users/${two.id}/keys/${h.memberId}`, undefined, ...admin), 404, 'not_found');
    expectDone(await h.do('DELETE', `${users}/${two.id}`, undefined, ...admin));
    expect((await h.do('GET', users, undefined, ...admin)).body.data).toEqual([]);
    // A key is a caller at the gate, and the handler refuses it.
    const three = await post('three');
    expectDetail(await h.do('GET', '/api/account/keys', undefined, ...keyOf(three)), 403, 'forbidden', SESSION_ONLY_DETAIL);
  });

  test('mountIdentityOperations runs the key routes through the pipeline with the contract in their table entries', async () => {
    const h = await sqliteHarness({ service: KEYS_ON });
    const app = new Hono();
    const mounted = mountIdentityOperations(app, h.service, { sessions: { register: true }, administration: {}, rateLimitPerMinute: 600 });
    const row = (spec: OperationSpec) =>
      `${spec.method} ${spec.path} ${spec.auth.public ? 'public' : spec.auth.permissions.join(',') || 'caller'} ${spec.rateLimitPerMinute}`;
    expect(mounted.filter(spec => spec.path.includes('/keys')).map(row)).toEqual([
      'POST /auth/keys caller 10',
      'GET /auth/keys caller 600',
      'DELETE /auth/keys/{id} caller 600',
      'GET /auth/admin/users/{id}/keys identity.users.read 600',
      'DELETE /auth/admin/users/{id}/keys/{keyId} identity.users.write 600',
    ]);
    expect(mounted.find(spec => spec.name === 'revokeUserApiKey')!.pathParams.map(param => param.name)).toEqual(['id', 'keyId']);
    expect(mounted).toHaveLength(identityOperations.length);

    h.app = app;
    const member = bearer(await h.login('member@example.com', USER_PASSWORD));
    const admin = bearer(await h.login('admin@example.com', ADMIN_PASSWORD));
    const one = await h.createKey(member, { name: 'one' });
    const two = await h.createKey(member, { name: 'two' });
    expectDone(await h.do('DELETE', `/auth/keys/${one.id}`, undefined, ...member));
    expect(names(await h.do('GET', `/auth/admin/users/${h.memberId}/keys`, undefined, ...admin))).toBe('two');
    expectDone(await h.do('DELETE', `/auth/admin/users/${h.memberId}/keys/${two.id}`, undefined, ...admin));
    expect((await h.do('GET', '/auth/keys', undefined, ...member)).body.data).toEqual([]);
  });
});

test('an API key is its prefix and 43 characters of base64url, 47 in all, two differ, and its hash is the whole text', () => {
  const a = newApiKey();
  const b = newApiKey();
  expect([API_KEY_PREFIX, API_KEY_LENGTH]).toEqual(['key_', 47]);
  expect(a).not.toBe(b);
  expect(a).toMatch(/^key_[A-Za-z0-9_-]{43}$/u);
  expect(hashToken(a)).toHaveLength(64);
  expect(hashToken(a)).not.toBe(hashToken(a.slice(API_KEY_PREFIX.length)));
  // A key from too few random bytes is refused.
  expect(() => newApiKey(() => new Uint8Array(5))).toThrow();
});

test('a key made with permissions holds each role narrowed to them, in the roles order, and no role it holds nothing of', () => {
  const roles = [
    { id: '1', name: 'clerk', permissions: ['orders', 'stock.read'] },
    { id: '2', name: 'auditor', permissions: ['audit.read'] },
    { id: '3', name: 'reader', permissions: ['orders.read'] },
  ];
  const narrowed = narrowRoles(roles, ['orders.read', 'stock', 'orders.read']);
  expect(narrowed).toEqual([
    { id: '1', name: 'clerk', permissions: ['orders.read'] },
    { id: '3', name: 'reader', permissions: ['orders.read'] },
  ]);
  expect(effectivePermissions(narrowed)).toEqual(['orders.read']);
  expect(narrowRoles(roles, ['billing'])).toEqual([]);
  expect(narrowRoles(roles, [])).toEqual([]);
  // The roles it was given are not rewritten.
  expect(roles[0]!.permissions).toEqual(['orders', 'stock.read']);
});

test('the config API key lifetime is 365 days unless set, and must be positive', () => {
  expect(parseIdentityConfig({ sessionTtlSeconds: 60, apiKeyMaxTtlSeconds: 7200 }).apiKeyMaxTtlSeconds).toBe(7200);
  expect([parseIdentityConfig({}).apiKeyMaxTtlSeconds, DEFAULT_API_KEY_MAX_TTL_SECONDS]).toEqual([365 * 24 * 60 * 60, 365 * 24 * 60 * 60]);
  expect(() => parseIdentityConfig({ apiKeyMaxTtlSeconds: 0 })).toThrow('apiKeyMaxTtlSeconds must be positive, not 0');
});
