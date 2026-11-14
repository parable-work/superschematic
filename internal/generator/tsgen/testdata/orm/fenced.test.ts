// fixture-fenced-db's @fenced fields on Postgres (D119): the repositories
// advance each counter themselves, by one, in a write of a column the
// fence covers, and every counter is read back at its exact value after
// every write. create.sql holds the fences' triggers, so each write here
// is one they allow, and a write that skips a value, or keeps the one it
// holds, is refused.
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { NotFoundError, uuidToDb } from '@superschematic/http-runtime/postgres';
import { createOrm, type FixtureFencedDbOrm } from './orm';
import type { GenericInt64, IdentityUUID } from './types';
import { alice, bob, database, dsn, recording, type TestDatabase } from './harness';

let db: TestDatabase;
let orm: FixtureFencedDbOrm;

const n = (value: number) => value as GenericInt64;

beforeAll(async () => {
  db = await database('ts_orm_fenced');
  orm = createOrm(db.pool, alice);
  const { rows } = await db.pool.query(
    "SELECT count(*)::int AS n FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid JOIN pg_namespace s ON s.oid = c.relnamespace WHERE s.nspname = $1 AND NOT t.tgisinternal AND t.tgname LIKE '%_fence'",
    [db.schema]
  );
  // A trigger for each of the four fences.
  expect(rows[0].n).toBe(4);
});

afterAll(async () => {
  await db?.drop();
});

/** The delivery's counters as the table holds them. */
async function counters(id: IdentityUUID): Promise<[number, number]> {
  const { rows } = await db.pool.query('SELECT plan_version::int AS plan, run_version::int AS run FROM delivery WHERE id = $1', [uuidToDb(id)]);
  return [rows[0].plan, rows[0].run];
}

async function version(id: IdentityUUID): Promise<number> {
  return (await db.pool.query('SELECT version::int AS version FROM parcel WHERE id = $1', [uuidToDb(id)])).rows[0].version;
}

async function revision(id: IdentityUUID): Promise<{ revision: number; deleted: boolean }> {
  const [row] = (await db.pool.query('SELECT revision::int AS revision, deleted_at FROM depot WHERE id = $1', [uuidToDb(id)])).rows;
  return { revision: row.revision, deleted: row.deleted_at !== null };
}

describe.skipIf(dsn === '')('fences', () => {
  test('an update advances the fence that covers a column it sets by one, leaves the other, and advances none for uncovered columns alone', async () => {
    const ada = await orm.courier.createOne({ name: 'Ada' });
    const { id } = await orm.delivery.createOne({ address: '1 Main St' });
    expect(await counters(id)).toEqual([0, 0]);
    const update = async (change: Parameters<typeof orm.delivery.updateOne>[1]) => {
      const row = await orm.delivery.updateOne(id, change);
      const stored = await counters(id);
      expect([row.planVersion, row.runVersion]).toEqual(stored);
      return { row, stored };
    };

    expect((await update({ address: '2 Main St' })).stored).toEqual([1, 0]);
    // note and updatedAt are no fence's: the update stamps, and bumps none.
    const noted = await update({ note: 'ring twice' });
    expect(noted.stored).toEqual([1, 0]);
    expect((await update({ note: null })).stored).toEqual([1, 0]);
    const again = await update({ note: 'ring once' });
    expect(again.stored).toEqual([1, 0]);
    expect(new Date(again.row.updatedAt).getTime()).toBeGreaterThan(new Date(noted.row.updatedAt).getTime());

    expect((await update({ attempts: n(1) })).stored).toEqual([1, 1]);
    expect((await update({ address: '3 Main St', proof: 'signed' })).stored).toEqual([2, 2]);
    // Two covered columns of one fence advance it once.
    expect((await update({ attempts: n(2), proof: null })).stored).toEqual([2, 3]);
    // A value the row already holds counts: the statement cannot know.
    expect((await update({ address: '3 Main St' })).stored).toEqual([3, 3]);
    // A relation is its key's column.
    expect((await update({ courierId: ada.id })).stored).toEqual([4, 3]);
    expect((await update({ courierId: null })).stored).toEqual([5, 3]);
  });

  test('an update that sets a fence itself is written as it stands, and an updateMany that filters on the fence is a compare-and-set', async () => {
    const { id } = await orm.delivery.createOne({ address: '1 Main St' });
    // The caller read 0 and writes 1; runVersion is still advanced for it.
    await orm.delivery.updateOne(id, { address: '2 Main St', planVersion: n(1) });
    expect(await counters(id)).toEqual([1, 0]);
    await orm.delivery.updateOne(id, { planVersion: n(2), attempts: n(1) });
    expect(await counters(id)).toEqual([2, 1]);
    // A value that skips one is written as it stands too, and refused.
    await expect(orm.delivery.updateOne(id, { address: '3 Main St', planVersion: n(4) })).rejects.toMatchObject({ constraint: 'delivery_plan_version_fence' });
    expect(await counters(id)).toEqual([2, 1]);
    expect((await orm.delivery.getOne(id)).address).toBe('2 Main St');

    const at = { id: { eq: id }, planVersion: { eq: n(2) } };
    expect(await orm.delivery.updateMany(at, { address: '4 Main St', planVersion: n(3) })).toBe(1);
    expect(await counters(id)).toEqual([3, 1]);
    // The second writer read 2 as well: it finds no row.
    expect(await orm.delivery.updateMany(at, { address: '5 Main St', planVersion: n(3) })).toBe(0);
    expect(await counters(id)).toEqual([3, 1]);
    expect((await orm.delivery.getOne(id)).address).toBe('4 Main St');
  });

  test("updateMany and an upsert's update advance as updateOne does, and an upsert that inserts or sets no field advances nothing", async () => {
    const ada = await orm.courier.createOne({ name: 'Ada' });
    const { id } = await orm.delivery.createOne({ address: '1 Main St', note: 'mine' });
    const { id: other } = await orm.delivery.createOne({ address: '9 Side St', note: 'mine' });
    const mine = { id: { in: [id, other] } };

    expect(await orm.delivery.updateMany(mine, { note: 'fragile' })).toBe(2);
    expect([await counters(id), await counters(other)]).toEqual([[0, 0], [0, 0]]);
    expect(await orm.delivery.updateMany(mine, { attempts: n(5) })).toBe(2);
    expect([await counters(id), await counters(other)]).toEqual([[0, 1], [0, 1]]);
    expect(await orm.delivery.updateMany(mine, { courierId: ada.id, proof: 'left at door' })).toBe(2);
    expect([await counters(id), await counters(other)]).toEqual([[1, 2], [1, 2]]);

    const held = { id, address: 'ignored' };
    const upsert = async (change: Parameters<typeof orm.delivery.upsertOneResult>[2], onlyIfChanged = false) => {
      const result = await orm.delivery.upsertOneResult(held, ['id'], change, { onlyIfChanged });
      expect([result.row.planVersion, result.row.runVersion]).toEqual(await counters(id));
      return [result.status, ...(await counters(id))];
    };
    expect(await upsert({ note: 'gate code 12' })).toEqual(['updated', 1, 2]);
    expect(await upsert({ proof: 'photo' })).toEqual(['updated', 1, 3]);
    expect(await upsert({ address: '2 Main St', attempts: n(6) })).toEqual(['updated', 2, 4]);
    expect(await upsert({})).toEqual(['unchanged', 2, 4]);
    expect(await upsert({ address: '2 Main St' }, true)).toEqual(['unchanged', 2, 4]);
    expect(await upsert({ address: '3 Main St' }, true)).toEqual(['updated', 3, 4]);
    expect(await upsert({ proof: 'signed', runVersion: n(5) })).toEqual(['updated', 3, 5]);

    // upsertMany sets the fields it names of each row a key holds, and
    // inserts the others as given.
    const fresh = bob as IdentityUUID;
    const inputs = (address: string) => [
      { id, address, note: 'bell broken' },
      { id: fresh, address: '1 New St', note: 'new' },
    ];
    const many = await orm.delivery.upsertMany(inputs('4 Main St'), ['id'], ['address']);
    expect(many.map(result => [result.status, result.row.planVersion, result.row.runVersion])).toEqual([
      ['updated', 4, 5],
      ['inserted', 0, 0],
    ]);
    expect([await counters(id), await counters(fresh)]).toEqual([[4, 5], [0, 0]]);
    await orm.delivery.upsertMany(inputs('ignored'), ['id'], ['note']);
    expect([await counters(id), await counters(fresh)]).toEqual([[4, 5], [0, 0]]);
    // Only if changed leaves the new row, which holds its input's values.
    await orm.delivery.upsertMany(inputs('5 Main St'), ['id'], ['address', 'proof', 'courierId'], { onlyIfChanged: true });
    expect([await counters(id), await counters(fresh)]).toEqual([[5, 6], [0, 0]]);
    // A field list that names the fence writes the input's value of it.
    await orm.delivery.upsertMany([{ id, address: '6 Main St', planVersion: n(6) }], ['id'], ['address', 'planVersion']);
    expect(await counters(id)).toEqual([6, 6]);
  });

  test('a fence with no covers advances on every update', async () => {
    const { id } = await orm.parcel.createOne({ trackingCode: 'TRK-1', weightGrams: n(500) });
    expect(await version(id)).toBe(0);
    expect((await orm.parcel.updateOne(id, { note: 'fragile' })).version).toBe(1);
    expect((await orm.parcel.updateOne(id, { weightGrams: n(510), note: null })).version).toBe(2);
    expect(await version(id)).toBe(2);

    const byCode = { trackingCode: 'TRK-1', weightGrams: n(520) };
    expect(await orm.parcel.upsertOneResult(byCode, ['trackingCode'], { weightGrams: n(520) })).toMatchObject({ status: 'updated', row: { id, version: 3 } });
    expect(await orm.parcel.upsertOneResult(byCode, ['trackingCode'], {})).toMatchObject({ status: 'unchanged', row: { id, version: 3 } });
    expect(await version(id)).toBe(3);
    expect(await orm.parcel.updateMany({ id: { eq: id } }, { note: 'this side up' })).toBe(1);
    expect(await version(id)).toBe(4);
    await orm.parcel.updateOne(id, { note: 'heavy', version: n(5) });
    expect(await version(id)).toBe(5);
    const batch = await orm.parcel.upsertMany([{ trackingCode: 'TRK-1', weightGrams: n(530) }, { trackingCode: 'TRK-2', weightGrams: n(90) }], ['trackingCode'], ['weightGrams']);
    expect(batch.map(result => [result.status, result.row.version])).toEqual([
      ['updated', 6],
      ['inserted', 0],
    ]);
    expect(await version(id)).toBe(6);
    await expect(orm.parcel.updateOne(id, { note: 'light', version: n(6) })).rejects.toMatchObject({ constraint: 'parcel_version_fence' });
    expect(await version(id)).toBe(6);
  });

  test('a soft delete advances the fence that covers deletedAt, and a write of capacity alone does not', async () => {
    const [north, south, east] = await orm.depot.createMany([
      { name: 'North', capacity: n(10) },
      { name: 'South', capacity: n(10) },
      { name: 'East', capacity: n(10) },
    ]);
    await orm.depot.updateOne(north!.id, { capacity: n(12) });
    expect(await revision(north!.id)).toEqual({ revision: 0, deleted: false });
    await orm.depot.updateOne(north!.id, { name: 'North Yard', capacity: n(14) });
    expect(await revision(north!.id)).toEqual({ revision: 1, deleted: false });
    await orm.depot.deleteOne(north!.id);
    expect(await revision(north!.id)).toEqual({ revision: 2, deleted: true });
    await expect(orm.depot.deleteOne(north!.id)).rejects.toBeInstanceOf(NotFoundError);
    expect(await revision(north!.id)).toEqual({ revision: 2, deleted: true });
    expect(await orm.depot.deleteMany({ name: { in: ['South', 'North Yard'] } })).toBe(1);
    expect(await revision(south!.id)).toEqual({ revision: 1, deleted: true });
    expect(await revision(north!.id)).toEqual({ revision: 2, deleted: true });
    expect(await revision(east!.id)).toEqual({ revision: 0, deleted: false });
  });

  test("the bump follows an update's assignments and its stamp, once for each fence, and a table with no fence is written as before", async () => {
    const { pool, statements } = recording(db.pool);
    const recorded = createOrm(pool, alice);
    const ada = await recorded.courier.createOne({ name: 'Ada' });
    const { id } = await recorded.delivery.createOne({ address: '1 Main St' });
    const returning = ' RETURNING "id", "address", "courier_id", "plan_version", "attempts", "proof", "run_version", "note", "updated_at"';
    statements.length = 0;
    await recorded.delivery.updateOne(id, { address: '2 Main St', courierId: ada.id });
    await recorded.delivery.updateOne(id, { note: 'ring twice' });
    await recorded.delivery.updateOne(id, { address: '3 Main St', proof: 'signed', note: null });
    await recorded.delivery.updateOne(id, { address: '4 Main St', planVersion: n(3), attempts: n(1) });
    await recorded.delivery.updateMany({ id: { eq: id }, planVersion: { eq: n(3) } }, { attempts: n(2) });
    await recorded.delivery.upsertOne({ id, address: 'ignored' }, ['id'], { proof: 'photo' });
    await recorded.delivery.upsertMany([{ id, address: '5 Main St' }], ['id'], ['address']);
    await recorded.courier.updateOne(ada.id, { name: 'Ada L.' });
    expect(statements).toEqual([
      'UPDATE delivery SET address = $1, courier_id = $2, updated_at = $3, plan_version = delivery.plan_version + 1 WHERE id = $4' + returning,
      'UPDATE delivery SET note = $1, updated_at = $2 WHERE id = $3' + returning,
      'UPDATE delivery SET address = $1, proof = $2, note = NULL, updated_at = $3, plan_version = delivery.plan_version + 1, run_version = delivery.run_version + 1 WHERE id = $4' + returning,
      'UPDATE delivery SET address = $1, plan_version = $2, attempts = $3, updated_at = $4, run_version = delivery.run_version + 1 WHERE id = $5' + returning,
      'UPDATE delivery SET attempts = $1, updated_at = $2, run_version = delivery.run_version + 1 WHERE delivery.id = $3 AND delivery.plan_version = $4',
      // An insert writes each default the input leaves unset, the counters' 0 among them.
      'INSERT INTO delivery (id, address, plan_version, attempts, run_version) VALUES ($1, $2, $3, $4, $5) ON CONFLICT (id) DO UPDATE SET proof = $6, updated_at = $7, run_version = delivery.run_version + 1' + returning + ', (xmax = 0) AS _inserted',
      'INSERT INTO delivery (id, address, plan_version, attempts, run_version) VALUES ($1, $2, $3, $4, $5) ON CONFLICT (id) DO UPDATE SET address = EXCLUDED.address, updated_at = $6, plan_version = delivery.plan_version + 1' + returning + ', (xmax = 0) AS _inserted',
      'UPDATE courier SET "name" = $1 WHERE id = $2 RETURNING "id", "name"',
    ]);
    expect(await counters(id)).toEqual([4, 4]);
  });
});
