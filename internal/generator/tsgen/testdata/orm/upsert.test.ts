// fixture-db's upserts of a versioned table on Postgres (D79, D89): an
// upsert of a held slug whose update sets no field updates nothing, so the
// tenant keeps its version, its stamp, its row version and its history,
// alone, many at once or in a transaction; one that sets a field moves the
// version once and records one history row.
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { uuidToDb } from '@superschematic/http-runtime/postgres';
import { createOrm, type FixtureDbOrm } from './orm';
import type { IdentityName, IdentitySlug, IdentityUUID } from './types';
import { alice, database, dsn, type TestDatabase } from './harness';

let db: TestDatabase;
let orm: FixtureDbOrm;

beforeAll(async () => {
  db = await database('ts_orm_upsert');
  orm = createOrm(db.pool, alice);
});

afterAll(async () => {
  await db?.drop();
});

/** The tenant id names as the table holds it: its version, its stamp, its row version, and its history rows. */
async function stored(id: IdentityUUID) {
  const [row] = (await db.pool.query('SELECT _version, updated_at, xmin::text AS xmin FROM tenant WHERE id = $1', [uuidToDb(id)])).rows;
  const [{ count }] = (await db.pool.query('SELECT count(*)::int AS count FROM tenant_history WHERE id = $1', [uuidToDb(id)])).rows;
  return { ...row, history: count };
}

describe.skipIf(dsn === '')('upserts', () => {
  test('an upsert that sets no field leaves the row it holds as it is, its version and history all; one that sets a field moves them once', async () => {
    const anvil = await orm.tenant.createOne({ name: 'Anvil' as IdentityName, slug: 'anvil' as IdentitySlug, email: 'anvil@example.com', metadata: {} });
    const before = await stored(anvil.id);
    expect(before).toMatchObject({ _version: '1', history: 1 });
    const input = { name: 'Other' as IdentityName, slug: 'anvil' as IdentitySlug, email: 'other@example.com', metadata: {} };

    const one = await orm.tenant.upsertOneResult(input, ['slug'], {});
    expect(one.status).toBe('unchanged');
    expect(one.row).toMatchObject({ id: anvil.id, name: 'Anvil', email: 'anvil@example.com', _version: 1 });
    expect(await stored(anvil.id)).toEqual(before);

    const many = await orm.tenant.upsertMany([input, { ...input, slug: 'bellows' as IdentitySlug }], ['slug'], []);
    expect(many.map(result => [result.status, result.row.slug, result.row._version])).toEqual([
      ['unchanged', 'anvil', 1],
      ['inserted', 'bellows', 1],
    ]);
    expect(await stored(anvil.id)).toEqual(before);

    const inside = await orm.transaction(async tx => tx.tenant.upsertOneResult(input, ['slug'], {}));
    expect(inside).toMatchObject({ status: 'unchanged', row: { id: anvil.id, _version: 1 } });
    expect(await stored(anvil.id)).toEqual(before);

    const renamed = await orm.tenant.upsertOneResult(input, ['slug'], { name: 'Anvil and Hammer' as IdentityName });
    expect(renamed).toMatchObject({ status: 'updated', row: { id: anvil.id, name: 'Anvil and Hammer', _version: 2 } });
    expect(await stored(anvil.id)).toMatchObject({ _version: '2', history: 2 });
  });
});
