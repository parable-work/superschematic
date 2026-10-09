import { afterEach, describe, expect, test } from 'bun:test';
import { DatabaseSync } from 'node:sqlite';
import {
  IdentityDescriptorError,
  hashToken,
  isStoreError,
  nodeSqlite,
  parseIdentityDescriptor,
  postgresIdentityStore,
  sqliteIdentityStore,
  type IdentityStore,
  type PgPool,
  type SqliteClient,
  type IdentityStoreErrorKind,
  type StoredUser,
} from './index';
import { HOUR, MINUTE, SECOND, at, databaseKinds, descriptorJSON, descriptorWith, postgresURL, postgresURLEnv, sqliteDDL, type TestDatabase } from './fixture.testing';

/*
The SQL store against the fixture's tables (runtime/http/testdata/identity),
on SQLite through node:sqlite and bun:sqlite, and on Postgres when
SUPERSCHEMATIC_IDENTITY_TEST_DATABASE_URL names one: the cases the Go
runtime's store tests hold it to.
*/

const MISSING = '00000000-0000-4000-8000-000000000000';

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

async function expectKind(promise: Promise<unknown>, kind: IdentityStoreErrorKind): Promise<void> {
  const error = await rejection(promise);
  if (!isStoreError(error, kind)) throw new Error(`want a ${kind} refusal, got ${String(error)}`);
}

function createUser(store: IdentityStore, login: string, name: string): Promise<StoredUser> {
  return store.createUser({ login, name, passwordHash: `hash-of-${login}`, at: at() });
}

function createSession(store: IdentityStore, user: StoredUser, tokenHash: string) {
  return store.createSession({ userId: user.id, tokenHash, createdAt: at(), expiresAt: at(HOUR) });
}

const hashOf = (n: number) => hashToken(`token-${n}`);

if (!databaseKinds.some(kind => kind.name === 'postgres')) {
  console.log(`${postgresURLEnv} is unset: the identity store runs on SQLite alone`);
}

for (const kind of databaseKinds) {
  describe(`identity store on ${kind.name}`, () => {
    const database = async () => {
      const db = await kind.open();
      open.push(db);
      return db;
    };

    test('users: created with their credential, found by a login in any case, listed by login', async () => {
      const db = await database();
      const s = db.store();
      const alice = await createUser(s, ' Alice@Example.COM ', 'Alice');
      expect(alice).toMatchObject({ login: 'alice@example.com', name: 'Alice', disabled: false, roles: [] });
      expect(alice.id).not.toContain('-');
      const bob = await createUser(s, 'bob@example.com', '');
      expect(bob.name).toBe('bob@example.com');

      await expectKind(s.createUser({ login: 'ALICE@example.com', name: 'Again', passwordHash: 'x', at: at() }), 'login_taken');
      await expectKind(s.createUser({ login: 'not an email', name: '', passwordHash: 'x', at: at() }), 'invalid_login');
      // The fixture's display name is an Identity.Name, 2 to 80
      // characters: another is refused by its scalar before any write.
      await expectKind(s.createUser({ login: 'dave@example.com', name: 'd'.repeat(81), passwordHash: 'x', at: at() }), 'invalid_name');
      await expectKind(s.createUser({ login: 'dave@example.com', name: 'D', passwordHash: 'x', at: at() }), 'invalid_name');
      await expectKind(s.findLogin('dave@example.com'), 'not_found');

      const rec = await s.findLogin('ALICE@EXAMPLE.com');
      expect(rec.user.id).toBe(alice.id);
      expect(rec.passwordHash).toBe('hash-of- Alice@Example.COM ');
      await expectKind(s.findLogin('carol@example.com'), 'not_found');
      await expectKind(s.findLogin('carol'), 'invalid_login');
      expect((await s.findCredential(bob.id)).user.login).toBe('bob@example.com');

      expect(await s.getUser(alice.id)).toEqual({ ...alice, roles: [] });
      for (const id of ['not a key', MISSING, '1']) await expectKind(s.getUser(id), 'not_found');

      const users = await s.listUsers();
      expect(users.map(u => u.id)).toEqual([alice.id, bob.id]);
    });

    test('sessions: found by token hash with their user, touched once per interval, revoked alone or with the others', async () => {
      const db = await database();
      const s = db.store();
      const alice = await createUser(s, 'alice@example.com', 'Alice');
      const one = await createSession(s, alice, hashOf(1));
      await createSession(s, alice, hashOf(2));
      const three = await createSession(s, alice, hashOf(3));

      const rec = await s.findSession(hashOf(1));
      expect(rec.session).toEqual({ id: one.id, userId: alice.id, createdAt: at(), expiresAt: at(HOUR), lastSeenAt: null, revokedAt: null });
      expect(rec.user).toMatchObject({ id: alice.id, login: 'alice@example.com', name: 'Alice', disabled: false });
      expect(one.id).not.toContain('-');
      await expectKind(s.findSession(hashOf(9)), 'not_found');

      // The first touch writes lastSeenAt; one within the interval does
      // not; one after it does.
      const touch = async (when: Date) => {
        await s.touchSession(one.id, when, new Date(when.getTime() - MINUTE));
        return (await s.findSession(hashOf(1))).session.lastSeenAt;
      };
      expect(await touch(at(SECOND))).toEqual(at(SECOND));
      expect(await touch(at(30 * SECOND))).toEqual(at(SECOND));
      expect(await touch(at(2 * MINUTE))).toEqual(at(2 * MINUTE));

      await s.revokeSession(one.id, at(3 * MINUTE));
      await s.revokeSession(one.id, at(4 * MINUTE));
      expect((await s.findSession(hashOf(1))).session.revokedAt).toEqual(at(3 * MINUTE));
      await s.revokeUserSessions(alice.id, three.id, at(5 * MINUTE));
      expect((await s.findSession(hashOf(2))).session.revokedAt).toEqual(at(5 * MINUTE));
      expect((await s.findSession(hashOf(3))).session.revokedAt).toBeNull();

      if (db.name !== 'postgres') {
        const [row] = await db.query(`SELECT "created_at" FROM "session" WHERE "token_hash" = ?`, [hashOf(1)]);
        expect(row!.created_at).toBe('2026-10-08T12:00:00.000Z');
      }
    });

    test('credentials: a password change keeps one session, a rehash replaces only the hash it read, a disable revokes', async () => {
      const db = await database();
      const s = db.store();
      const alice = await createUser(s, 'alice@example.com', 'Alice');
      const keep = await createSession(s, alice, hashOf(1));
      await createSession(s, alice, hashOf(2));

      await s.setPassword(alice.id, 'new-hash', at(MINUTE), keep.id);
      expect((await s.findLogin('alice@example.com')).passwordHash).toBe('new-hash');
      expect((await s.findSession(hashOf(1))).session.revokedAt).toBeNull();
      expect((await s.findSession(hashOf(2))).session.revokedAt).toEqual(at(MINUTE));

      await s.rehashPassword(alice.id, 'stale-hash', 'rehashed');
      expect((await s.findLogin('alice@example.com')).passwordHash).toBe('new-hash');
      await s.rehashPassword(alice.id, 'new-hash', 'rehashed');
      expect((await s.findLogin('alice@example.com')).passwordHash).toBe('rehashed');

      await s.setDisabled(alice.id, true, at(2 * MINUTE));
      const disabled = await s.findSession(hashOf(1));
      expect(disabled.user.disabled).toBe(true);
      expect(disabled.session.revokedAt).toEqual(at(2 * MINUTE));
      await s.setDisabled(alice.id, false, at(3 * MINUTE));
      expect((await s.getUser(alice.id)).disabled).toBe(false);

      // A user the project wrote without a credential is disabled all the
      // same, and has no password.
      await db.query(`INSERT INTO "user" ("email", "display_name") VALUES (?, ?)`, ['bare@example.com', 'Bare']);
      const bare = await s.findLogin('bare@example.com');
      expect(bare.passwordHash).toBe('');
      expect(bare.user.disabled).toBe(false);
      await s.setDisabled(bare.user.id, true, at(MINUTE));
      const after = await s.findLogin('bare@example.com');
      expect(after.user.disabled).toBe(true);
      expect(after.passwordHash).toBe('');

      await expectKind(s.setPassword(MISSING, 'h', at(), ''), 'not_found');
      await expectKind(s.setDisabled(MISSING, true, at()), 'not_found');
    });

    test('roles: kept in order, listed by name, a taken name refused; grants idempotent; a deleted role takes its grants', async () => {
      const db = await database();
      const s = db.store();
      expect(s.hasRoles()).toBe(true);
      const alice = await createUser(s, 'alice@example.com', 'Alice');
      const bob = await createUser(s, 'bob@example.com', 'Bob');

      const writer = await s.createRole('writer', ['orders.write', 'orders.read']);
      const admin = await s.createRole('admin', []);
      await expectKind(s.createRole('writer', []), 'role_name_taken');
      expect(await s.getRole(writer.id)).toEqual({ id: writer.id, name: 'writer', permissions: ['orders.write', 'orders.read'] });
      expect((await s.listRoles()).map(r => [r.name, r.permissions])).toEqual([
        ['admin', []],
        ['writer', ['orders.write', 'orders.read']],
      ]);

      await expectKind(s.updateRole(admin.id, 'writer', []), 'role_name_taken');
      expect(await s.updateRole(admin.id, 'administrator', ['identity', 'orders'])).toEqual({
        id: admin.id,
        name: 'administrator',
        permissions: ['identity', 'orders'],
      });
      await s.updateRole(writer.id, 'writer', ['orders.write']);

      for (const [user, role] of [
        [alice, writer],
        [alice, admin],
        [alice, admin],
        [bob, writer],
      ] as const) {
        await s.grantRole(user.id, role.id, at());
      }
      expect((await s.userRoles(alice.id)).map(r => r.name)).toEqual(['administrator', 'writer']);
      expect((await s.getUser(alice.id)).roles.map(r => r.name)).toEqual(['administrator', 'writer']);
      const users = await s.listUsers();
      expect(users.map(u => u.roles.map(r => r.name))).toEqual([['administrator', 'writer'], ['writer']]);

      await s.revokeRole(alice.id, writer.id);
      await s.revokeRole(alice.id, writer.id);
      expect((await s.userRoles(alice.id)).map(r => r.name)).toEqual(['administrator']);

      await s.deleteRole(writer.id);
      expect(await s.userRoles(bob.id)).toEqual([]);
      await expectKind(s.deleteRole(writer.id), 'not_found');

      await expectKind(s.getRole(MISSING), 'not_found');
      await expectKind(s.updateRole(MISSING, 'x', []), 'not_found');
      await expectKind(s.grantRole(alice.id, MISSING, at()), 'not_found');
      await expectKind(s.grantRole(MISSING, admin.id, at()), 'not_found');
      await expectKind(s.revokeRole(MISSING, admin.id), 'not_found');
      await expectKind(s.grantRole(alice.id, 'not a key', at()), 'not_found');
    });

    test('a descriptor without a role table: no roles held, the role methods refuse', async () => {
      const db = await database();
      const s = db.store(
        descriptorWith(d => {
          delete d.role;
          delete d.roleGrant;
        })
      );
      const alice = await createUser(s, 'alice@example.com', 'Alice');
      expect(s.hasRoles()).toBe(false);
      expect(await s.userRoles(alice.id)).toEqual([]);
      expect((await s.getUser(alice.id)).roles).toEqual([]);
      await expectKind(s.listRoles(), 'no_roles');
      await expectKind(s.grantRole(alice.id, alice.id, at()), 'no_roles');
    });

  });
}

test('a failed statement rolls its transaction back', async () => {
  const db = new DatabaseSync(':memory:');
  const client = nodeSqlite(db);
  client.exec(sqliteDDL);
  // A client that refuses the credential's insert, the second statement
  // of createUser's transaction.
  const failing: SqliteClient = {
    ...client,
    all: (sql, params) => client.all(sql, params),
    get: (sql, params) => client.get(sql, params),
    exec: sql => client.exec(sql),
    run(sql, params) {
      if (sql.startsWith('INSERT INTO "user_credential"')) throw new Error('refused');
      return client.run(sql, params);
    },
  };
  const s = sqliteIdentityStore(failing, descriptorJSON);
  expect(String(await rejection(createUser(s, 'alice@example.com', 'Alice')))).toContain('refused');
  expect(client.all('SELECT * FROM "user"')).toEqual([]);
  // The connection is out of the transaction: the next one runs.
  await createUser(sqliteIdentityStore(client, descriptorJSON), 'alice@example.com', 'Alice');
  db.close();
});

if (postgresURL !== '') {
  test('a failed statement rolls its transaction back on Postgres', async () => {
    const db = await databaseKinds.find(kind => kind.name === 'postgres')!.open();
    open.push(db);
    const pool = (db as unknown as { pool: PgPool }).pool;
    const failing: PgPool = {
      query: config => pool.query(config),
      async connect() {
        const conn = await pool.connect();
        return {
          release: (err?: Error | boolean) => conn.release(err),
          query: config => (config.text.startsWith('INSERT INTO "user_credential"') ? Promise.reject(new Error('refused')) : conn.query(config)),
        };
      },
    };
    const s = postgresIdentityStore(failing, descriptorJSON);
    expect(String(await rejection(createUser(s, 'alice@example.com', 'Alice')))).toContain('refused');
    expect(await db.query('SELECT * FROM "user"')).toEqual([]);
    await createUser(db.store(), 'alice@example.com', 'Alice');
  });
}

test('a SQLite operation runs whole, with no other operation between its statements', async () => {
  const db = new DatabaseSync(':memory:');
  const client = nodeSqlite(db);
  client.exec(sqliteDDL);
  const s = sqliteIdentityStore(client, descriptorJSON);
  const users = await Promise.all(Array.from({ length: 20 }, (_, i) => createUser(s, `user${i}@example.com`, `User ${i}`)));
  const role = await s.createRole('reader', []);
  await Promise.all(users.flatMap(u => [s.grantRole(u.id, role.id, at()), s.setPassword(u.id, 'h', at(), ''), s.setDisabled(u.id, true, at())]));
  expect((await s.listUsers()).every(u => u.disabled && u.roles.length === 1)).toBe(true);
  db.close();
});

test('the store refuses a descriptor it cannot read', () => {
  const cases: Record<string, string> = {
    'another version': descriptorWith(d => (d.version = 2)),
    'an empty name': descriptorWith(d => (d.session.table = '')),
    'a NUL in a name': descriptorWith(d => (d.user.table = 'us\u0000er')),
    'an unknown member': descriptorWith(d => (d.extra = {})),
    'an unknown nested member': descriptorWith(d => (d.user.columns.extra = 'x')),
    'a role without its grant': descriptorWith(d => delete d.roleGrant),
    'no name scalar': descriptorWith(d => delete d.user.nameScalar),
    'no role key scalar': descriptorWith(d => delete d.role.keyScalar),
    'not JSON': '{',
  };
  for (const [name, descriptor] of Object.entries(cases)) {
    expect(() => parseIdentityDescriptor(descriptor), name).toThrow(IdentityDescriptorError);
  }
  const db = new DatabaseSync(':memory:');
  expect(() => sqliteIdentityStore(nodeSqlite(db), descriptorWith(d => (d.user.loginScalar = 'Contact.Pager')))).toThrow(/superscalar/u);
  db.close();
});

test('with the name the login, a user is created with the login column alone', async () => {
  const db = new DatabaseSync(':memory:');
  const client = nodeSqlite(db);
  client.exec(sqliteDDL.replace('"display_name" TEXT NOT NULL', '"display_name" TEXT'));
  const s = sqliteIdentityStore(
    client,
    descriptorWith(d => {
      d.user.columns.name = 'email';
      d.user.nameScalar = 'Contact.Email';
    })
  );
  const u = await createUser(s, 'Alice@Example.com', 'Ignored');
  expect(u.name).toBe('alice@example.com');
  expect(client.get('SELECT "display_name" FROM "user"')).toEqual({ display_name: null });
  await createSession(s, u, hashOf(1));
  expect((await s.findSession(hashOf(1))).user.name).toBe('alice@example.com');
  db.close();
});

test('a role key of another scalar uses its own codec', async () => {
  const db = new DatabaseSync(':memory:');
  const client = nodeSqlite(db);
  client.exec(sqliteDDL);
  const s = sqliteIdentityStore(client, descriptorWith(d => (d.role.keyScalar = 'string')));
  const role = await s.createRole('reader', []);
  // A string key is the database's text as it is: the hyphenated UUID the
  // DDL's default writes.
  expect(role.id).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-/u);
  expect((await s.getRole(role.id)).name).toBe('reader');
  db.close();
});
