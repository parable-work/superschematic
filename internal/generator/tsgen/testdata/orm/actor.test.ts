// fixture-actor-db's Note on Postgres (D87): an ORM opened with no user acts
// as the caller of the operation it runs in, which a router binds with
// withCaller, and as no one outside one; withUser, as and asSystem act as
// whom they name wherever they run; asSystem acts as the DB service's
// system actor; and requireUpdatedBy refuses an update with no user.
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { currentCaller, withCaller } from '@superschematic/http-runtime/hono';
import { DEFAULT_SYSTEM_ACTOR, NoUserError, ormOf, systemActorOf, uuidFromDb, uuidToDb } from '@superschematic/http-runtime/postgres';
import { createOrm, fixtureActorDbOrm, type FixtureActorDbOrm, type FixtureActorDbRepositories } from './orm';
import { alice, aliceHyphenated, bob, database, dsn, type TestDatabase } from './harness';

let db: TestDatabase;
let orm: FixtureActorDbOrm;

beforeAll(async () => {
  db = await database('ts_orm_actor');
  orm = createOrm(db.pool);
});

afterAll(async () => {
  await db?.drop();
});

describe.skipIf(dsn === '')('the acting user', () => {
  test('an ORM opened with no user acts as the caller of the operation it runs in, and as no one outside one', async () => {
    await expect(orm.note.createOne({ text: "nobody's" })).rejects.toBeInstanceOf(NoUserError);
    expect(orm.actingUser).toBeUndefined();
    const created = await withCaller({ subject: alice }, async () => {
      expect(currentCaller()).toBe(alice);
      expect(orm.actingUser).toBe(alice);
      // The caller carries through what the operation awaits and starts.
      await new Promise(resolve => setTimeout(resolve, 1));
      return orm.note.createOne({ text: "alice's" });
    });
    expect(created).toMatchObject({ text: "alice's", createdBy: alice, updatedBy: alice });
    const updated = await withCaller({ subject: aliceHyphenated }, () => orm.note.updateOne(created.id, { text: "alice's edit" }));
    expect(updated.updatedBy).toBe(alice);
    const edited = await withCaller({ subject: bob }, () => orm.transaction(tx => tx.note.updateOne(created.id, { text: "bob's edit" })));
    expect(edited).toMatchObject({ createdBy: alice, updatedBy: bob });
    // Outside an operation, an update with no user leaves the nullable
    // updatedBy as it was.
    const untouched = await orm.note.updateOne(created.id, { text: "nobody's edit" });
    expect(untouched.updatedBy).toBe(bob);
    // A caller whose subject is no UUID, a custom authenticator's, binds no one.
    await withCaller({ subject: 'api-key-7' }, async () => {
      expect(orm.actingUser).toBeUndefined();
      await expect(orm.note.createOne({ text: 'a key holder\'s' })).rejects.toBeInstanceOf(NoUserError);
    });
    await withCaller(null, async () => expect(orm.actingUser).toBeUndefined());
  });

  test('withUser, as and asSystem act as whom they name wherever they run, and a transaction as its ORM', async () => {
    await withCaller({ subject: alice }, async () => {
      expect(orm.withUser(bob).actingUser).toBe(bob);
      expect(orm.as({ subject: bob }).actingUser).toBe(bob);
      expect(orm.as(null).actingUser).toBeUndefined();
      expect(orm.withUser(null).actingUser).toBeUndefined();
      await expect(orm.withUser(null).note.createOne({ text: 'nobody, explicitly' })).rejects.toBeInstanceOf(NoUserError);
      const byBob = await orm.withUser(bob).transaction(tx => tx.note.createOne({ text: "bob's, in alice's request" }));
      expect(byBob).toMatchObject({ createdBy: bob, updatedBy: bob });
      const bySystem = await orm.transaction(tx => tx.asSystem().note.createOne({ text: "the system's, in alice's request" }));
      expect(bySystem.createdBy).toBe(systemActorOf(fixtureActorDbOrm).id);
    });
    expect(createOrm(db.pool, alice).actingUser).toBe(alice);
    expect(createOrm(db.pool, null).actingUser).toBeUndefined();
    await withCaller({ subject: bob }, async () => {
      expect(createOrm(db.pool, alice).actingUser).toBe(alice);
      expect(createOrm(db.pool, { user: null }).actingUser).toBeUndefined();
      expect(createOrm(db.pool, {}).actingUser).toBe(bob);
    });
  });

  test("asSystem acts as the DB service's system actor, the default one unless it names another", async () => {
    // A job's or a worker's write, outside any operation.
    const system = uuidFromDb(DEFAULT_SYSTEM_ACTOR.id);
    expect(systemActorOf(fixtureActorDbOrm)).toEqual({ id: system, name: 'system' });
    const note = await orm.asSystem().note.createOne({ text: "a worker's" });
    expect(note).toMatchObject({ createdBy: system, updatedBy: system });
    await orm.asSystem().note.deleteOne(note.id);
    const { rows } = await db.pool.query('SELECT deleted_by::text AS deleted_by FROM note WHERE id = $1', [uuidToDb(note.id)]);
    expect(rows[0].deleted_by).toBe(DEFAULT_SYSTEM_ACTOR.id);

    const named = { id: '3f2c1a9e-7b4d-4e8a-9c61-0d5e2f7a8b10', name: 'jobs' };
    const jobs = ormOf<FixtureActorDbRepositories>(db.pool, { ...fixtureActorDbOrm, systemActor: named });
    expect(systemActorOf({ ...fixtureActorDbOrm, systemActor: named })).toEqual({ id: uuidFromDb(named.id), name: 'jobs' });
    const byJobs = await jobs.asSystem().note.createOne({ text: "the jobs' note" });
    expect(byJobs.createdBy).toBe(uuidFromDb(named.id));
    expect(() => ormOf(db.pool, { ...fixtureActorDbOrm, systemActor: { id: 'the jobs', name: 'jobs' } })).toThrow('is no UUID');
  });

  test('requireUpdatedBy refuses an update with no user that would leave updatedBy as it was', async () => {
    const strict = ormOf<FixtureActorDbRepositories>(db.pool, { ...fixtureActorDbOrm, requireUpdatedBy: true });
    const note = await strict.withUser(alice).note.createOne({ text: "alice's" });
    await expect(strict.note.updateOne(note.id, { text: "nobody's edit" })).rejects.toBeInstanceOf(NoUserError);
    await expect(strict.note.updateMany({ id: { eq: note.id } }, { text: "nobody's edits" })).rejects.toBeInstanceOf(NoUserError);
    await expect(strict.note.upsertOne({ id: note.id, text: 'upserted' }, ['id'], { text: 'upserted' })).rejects.toBeInstanceOf(NoUserError);
    const edited = await withCaller({ subject: bob }, () => strict.note.updateOne(note.id, { text: "bob's edit" }));
    expect(edited.updatedBy).toBe(bob);
    expect((await strict.asSystem().note.updateOne(note.id, { text: "the system's edit" })).updatedBy).toBe(systemActorOf(fixtureActorDbOrm).id);
  });
});

