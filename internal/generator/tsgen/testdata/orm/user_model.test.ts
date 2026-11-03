// fixture-user-model-db's identity tables on Postgres (D50, D79), the
// counterpart of the Go ORM's user_model_postgres_test.go: a login that
// differs from another only in case is the same login and finds the same
// user; a session's token hash, a user's credential and a user's grant of a
// role are each unique; deleting a role deletes its grants and deleting a
// user its sessions, credential and grants; and a principal's base62 subject
// finds its user row through uuidToDb (D79's codec).
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { decodeRow, uuidToDb } from '@superschematic/http-runtime/postgres';
import { createOrm, userTable, type FixtureUserModelDbOrm, type UserRow } from './orm';
import type { ContactEmail, CryptoSHA256, IdentityName } from './types';
import { database, dsn, type TestDatabase } from './harness';

let db: TestDatabase;
let orm: FixtureUserModelDbOrm;

const email = (text: string) => text as ContactEmail;
const display = (text: string) => text as IdentityName;
const hash = (pair: string) => pair.repeat(32) as CryptoSHA256;
const later = new Date(Date.now() + 14 * 24 * 3600 * 1000);

beforeAll(async () => {
  db = await database('ts_orm_user_model');
  orm = createOrm(db.pool);
});

afterAll(async () => {
  await db?.drop();
});

/** The unique violation a write raised: its constraint. */
async function violation(write: Promise<unknown>): Promise<string> {
  try {
    await write;
  } catch (error) {
    const { code, constraint } = error as { code?: string; constraint?: string };
    if (code === '23505') return constraint ?? '';
    throw error;
  }
  throw new Error('the write did not violate a unique constraint');
}

async function count(table: string, userId: string): Promise<number> {
  const { rows } = await db.pool.query(`SELECT count(*)::int AS n FROM "${table}" WHERE user_id = $1`, [uuidToDb(userId)]);
  return rows[0].n;
}

describe.skipIf(dsn === '')('the user model', () => {
  test('a login that differs only in case is the same login, and the identity tables keep their keys unique', async () => {
    const alice = await orm.user.createOne({ email: email('alice@example.com'), displayName: display('Alice') });
    expect(await violation(orm.user.createOne({ email: email('Alice@Example.COM'), displayName: display('Alice again') }))).toBe('user_email_key');
    const found = await orm.user.findOne({ email: { eq: email('ALICE@EXAMPLE.COM') } });
    expect(found?.id).toBe(alice.id);
    const bob = await orm.user.createOne({ email: email('bob@example.com'), displayName: display('Bob') });
    const session = await orm.session.createOne({ userId: alice.id, tokenHash: hash('ab'), expiresAt: later });
    expect([session.revokedAt, session.lastSeenAt]).toEqual([null, null]);
    expect(session.createdAt).toBeInstanceOf(Date);
    expect(await violation(orm.session.createOne({ userId: bob.id, tokenHash: hash('ab'), expiresAt: later }))).toBe('session_token_hash_key');
    const phc = '$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhhc2g';
    const credential = await orm.userCredential.createOne({ userId: alice.id, passwordHash: phc, passwordChangedAt: new Date() });
    expect([credential.passwordHash, credential.disabledAt]).toEqual([phc, null]);
    expect(await violation(orm.userCredential.createOne({ userId: alice.id, passwordHash: phc, passwordChangedAt: new Date() }))).toBe(
      'user_credential_user_id_key'
    );
    // The credential is unique per user, so an upsert keyed by its user replaces it.
    const replaced = await orm.userCredential.upsertOne({ userId: alice.id, passwordHash: phc, passwordChangedAt: new Date() }, ['user'], {
      passwordHash: `${phc}x`,
    });
    expect([replaced.id, replaced.passwordHash]).toEqual([credential.id, `${phc}x`]);
    const admin = await orm.role.createOne({ name: 'admin', permissions: ['identity.users.read', 'identity.users.write'] });
    expect(admin.permissions).toEqual(['identity.users.read', 'identity.users.write']);
    expect(await violation(orm.role.createOne({ name: 'admin', permissions: [] }))).toBe('role_name_key');
    await orm.userRoleGrant.createOne({ userId: alice.id, roleId: admin.id, grantedAt: new Date() });
    expect(await violation(orm.userRoleGrant.createOne({ userId: alice.id, roleId: admin.id, grantedAt: new Date() }))).toBe('uq_user_role_grant_user_role');
  });

  test('deleting a user deletes its sessions, credential and grants, and deleting a role its grants', async () => {
    const carol = await orm.user.createOne({ email: email('carol@example.com'), displayName: display('Carol') });
    const dave = await orm.user.createOne({ email: email('dave@example.com'), displayName: display('Dave') });
    const editor = await orm.role.createOne({ name: 'editor', permissions: ['posts.write'] });
    const viewer = await orm.role.createOne({ name: 'viewer', permissions: ['posts.read'] });
    await orm.userRoleGrant.createMany([
      { userId: carol.id, roleId: editor.id, grantedAt: new Date() },
      { userId: carol.id, roleId: viewer.id, grantedAt: new Date() },
      { userId: dave.id, roleId: editor.id, grantedAt: new Date() },
    ]);
    await orm.session.createOne({ userId: carol.id, tokenHash: hash('cd'), expiresAt: later });
    await orm.session.createOne({ userId: dave.id, tokenHash: hash('ef'), expiresAt: later });
    await orm.userCredential.createOne({ userId: carol.id, passwordHash: 'x', passwordChangedAt: new Date() });
    await orm.role.deleteOne(editor.id);
    expect([await count('user_role_grant', carol.id), await count('user_role_grant', dave.id)]).toEqual([1, 0]);
    await orm.user.deleteOne(carol.id);
    for (const table of ['session', 'user_credential', 'user_role_grant']) expect(await count(table, carol.id)).toBe(0);
    expect(await count('session', dave.id)).toBe(1);
  });

  test("a principal's base62 subject matches its user row through uuidToDb", async () => {
    const erin = await orm.user.createOne({ email: email('erin@example.com'), displayName: display('Erin') });
    // ctx.principal.subject is the user's id in its wire form, base62.
    const principal = { subject: erin.id as string, permissions: [] };
    const { rows } = await db.pool.query('SELECT id, email, display_name, created_at FROM "user" WHERE id = $1', [uuidToDb(principal.subject)]);
    expect(rows).toHaveLength(1);
    const row = decodeRow<UserRow>(rows[0], userTable.columns);
    expect(row.id).toBe(erin.id);
    expect(row.email).toBe(email('erin@example.com'));
    // The ORM acting as the principal stamps nothing here, the table having no actor columns, but reads alike.
    expect((await orm.as(principal).user.getOne(erin.id)).displayName).toBe(display('Erin'));
  });
});
