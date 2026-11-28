// fixture-role-events-db's role grant event (#625), end to end on Postgres
// and on SQLite: Role, a UserRole table, declares an @enqueueOnChange of its
// grants, which the loader copies onto UserRoleGrant, so granting a role
// and revoking it each enqueue a RoleGrantChanged whatever writes the
// grant: here the generated repositories, as the identity store's SQL
// would. The role table's own declaration enqueues a RoleChanged on a
// rename, and a grant writes none of it.
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import type { FixtureRoleEventsDbOrm } from './orm';
import { available, featureDatabase, ormEntry, type FeatureDatabase } from './dialect';

describe.skipIf(!available)('a role grant event', () => {
  let db: FeatureDatabase;
  let orm: FixtureRoleEventsDbOrm;

  beforeAll(async () => {
    db = await featureDatabase('ts_orm_role_events');
    orm = (await ormEntry<FixtureRoleEventsDbOrm>()).createOrm(db.source);
  });

  afterAll(async () => {
    await db?.drop();
  });

  const grantsQueued = async () => (await db.raw('SELECT COUNT(*) AS n FROM role_grant_changed_queue')).rows[0]!.n;
  const rolesQueued = async () => (await db.raw('SELECT COUNT(*) AS n FROM role_changed_queue')).rows[0]!.n;

  test('a grant of a role and its revocation each enqueue the role grant event', async () => {
    const sys = (orm as any).asSystem();
    const user = (await sys.user.createOne({ email: 'ann@example.test' })).id;
    const role = (await sys.role.createOne({ name: 'reviewer', permissions: ['changes.review'] })).id;
    const grant = (await sys.userRoleGrant.createOne({ userId: user, roleId: role, grantedAt: new Date().toISOString() })).id;
    expect(Number(await grantsQueued())).toBe(1);
    await sys.userRoleGrant.deleteOne(grant);
    expect(Number(await grantsQueued())).toBe(2);
    expect(Number(await rolesQueued())).toBe(0);
  });

  test("the role table's own declaration enqueues on a rename, and not on a grant", async () => {
    const sys = (orm as any).asSystem();
    const role = (await sys.role.findOne({ name: { eq: 'reviewer' } })).id;
    await sys.role.updateOne(role, { name: 'approver' });
    expect(Number(await rolesQueued())).toBe(1);
    expect(Number(await grantsQueued())).toBe(2);
  });
});
