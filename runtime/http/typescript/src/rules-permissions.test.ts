import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import type { RulesPermissions } from './rules';
import { readPermissions } from './rules-permissions';
import { postgresDatabase, rulesDsn, sqliteDatabase, type TestDatabase } from './rules.testing';
import { base62UUID, uuidValue } from './uuid';

/*
The core's permissions read (D200) through the adapter, as Approvals'
recheckPermission makes it: a user holds the permissions of every role a
grant gives them, each once; a user with no grant, a disabled one and a
pending one hold none; a subject no user row holds, or that is no UUID, is
left out; a layout that names no identity tables answers no subject. The
Go and Rust adapters' tests hold them to the same tables and answers.
*/

const ANN = '0e5c7d2a-4b1f-4c3e-9a8d-7f6e5d4c3b2a';
const BEN = '1f6d8e3b-5c2a-4d4f-8b9e-8a7f6e5d4c3b';
const CAM = '2a7e9f4c-6d3b-4e5a-9caf-9b8a7f6e5d4c';
const DOV = '3b8fa05d-7e4c-4f6b-8db0-ac9b8a7f6e5d';
const EVE = '4c9ab16e-8f5d-4a7c-9ec1-bdac9b8a7f6e';
const NOBODY = '5dabc27f-9a6e-4b8d-8fd2-cebdac9b8a7f';

/** Where fixture-approvals-identity-db's layout names its identity tables (behaviorlayout.Permissions). */
const identity: RulesPermissions = {
  users: 'account',
  user: { field: 'id', column: 'id', type: 'Identity.UUID' },
  credentials: 'user_credential',
  credentialUser: 'user_id',
  disabledAt: 'disabled_at',
  pendingAt: 'pending_at',
  grants: 'user_role_grant',
  grantUser: 'user_id',
  grantRole: 'role_id',
  roles: 'role',
  roleKey: 'id',
  rolePermissions: 'permissions',
};

const dialects: { name: string; skip: boolean; open: () => Promise<TestDatabase> }[] = [
  { name: 'bun:sqlite', skip: false, open: async () => sqliteDatabase() },
  { name: 'Postgres', skip: rulesDsn === '', open: postgresDatabase },
];

for (const dialect of dialects) {
  describe.skipIf(dialect.skip)(`the permissions read on ${dialect.name}`, () => {
    let db: TestDatabase;
    const p = (n: number) => (db.dialect === 'postgres' ? `$${n}` : `?${n}`);

    beforeAll(async () => {
      db = await dialect.open();
      const postgres = db.dialect === 'postgres';
      const uuid = postgres ? 'UUID' : 'TEXT';
      const ts = postgres ? 'TIMESTAMPTZ' : 'TEXT';
      await db.exec(`CREATE TABLE "account" (id ${uuid} PRIMARY KEY, email TEXT)`);
      await db.exec(`CREATE TABLE "user_credential" (id ${uuid} PRIMARY KEY, user_id ${uuid}, disabled_at ${ts}, pending_at ${ts})`);
      await db.exec(`CREATE TABLE "role" (id ${uuid} PRIMARY KEY, name TEXT, permissions ${postgres ? 'TEXT[]' : 'TEXT'})`);
      await db.exec(`CREATE TABLE "user_role_grant" (id ${uuid} PRIMARY KEY, user_id ${uuid}, role_id ${uuid})`);
      for (const [i, user] of [ANN, BEN, CAM, DOV, EVE].entries()) {
        await db.exec(`INSERT INTO "account" (id, email) VALUES (${p(1)}, ${p(2)})`, [user, `user${i}@example.com`]);
      }
      const at = postgres ? new Date('2026-01-02T03:04:05Z') : '2026-01-02T03:04:05Z';
      for (const [i, [user, disabled, pending]] of ([[ANN, null, null], [BEN, null, null], [CAM, at, null], [DOV, null, at]] as const).entries()) {
        await db.exec(`INSERT INTO "user_credential" (id, user_id, disabled_at, pending_at) VALUES (${p(1)}, ${p(2)}, ${p(3)}, ${p(4)})`, [`00000000-0000-4000-8000-00000000000${i}`, user, disabled, pending]);
      }
      const list = (items: string[]) => (postgres ? items : JSON.stringify(items));
      const reviewer = '00000000-0000-4000-8000-0000000000a1';
      const lead = '00000000-0000-4000-8000-0000000000a2';
      await db.exec(`INSERT INTO "role" (id, name, permissions) VALUES (${p(1)}, 'reviewer', ${p(2)})`, [reviewer, list(['changes.read', 'changes.approve'])]);
      await db.exec(`INSERT INTO "role" (id, name, permissions) VALUES (${p(1)}, 'lead', ${p(2)})`, [lead, list(['changes.approve', 'changes.lead'])]);
      for (const [i, [user, role]] of [[ANN, reviewer], [ANN, lead], [CAM, reviewer], [DOV, reviewer], [EVE, lead]].entries()) {
        await db.exec(`INSERT INTO "user_role_grant" (id, user_id, role_id) VALUES (${p(1)}, ${p(2)}, ${p(3)})`, [`00000000-0000-4000-8000-0000000000b${i}`, user, role]);
      }
    });
    afterAll(async () => db?.close());

    test("each subject's permissions through its roles' grants, none while disabled or pending, and a subject no user row holds left out", async () => {
      const ann = base62UUID(uuidValue(ANN)!);
      const held = await db.transaction(store => readPermissions(store, identity, [ann, BEN, CAM, DOV, EVE, NOBODY, 'no-uuid']));
      expect(held).toEqual({
        [ann]: ['changes.read', 'changes.approve', 'changes.lead'],
        [BEN]: [],
        [CAM]: [],
        [DOV]: [],
        [EVE]: ['changes.approve', 'changes.lead'],
      });
    });

    test('a layout that names no identity tables answers no subject', async () => {
      expect(await db.transaction(store => readPermissions(store, undefined, [ANN]))).toEqual({});
    });
  });
}
