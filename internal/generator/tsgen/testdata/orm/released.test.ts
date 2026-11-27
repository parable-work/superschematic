// The released rows of a graph member (D139) on Postgres, in
// fixture-version-graph-db with a root that is soft-deleted: the ORM holds
// the repository of PartReleased as its reads alone, and those reads leave
// out the rows of a soft-deleted root, unless they include deleted rows,
// while the rows stay in the table, so a restore of the root reads them
// again. TestReleasedRowsOfASoftDeletedRootOnPostgres runs it.
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { NotFoundError, READ_METHODS, uuidToDb } from '@superschematic/http-runtime/postgres';
import { createOrm, type FixtureVersionGraphDbOrm } from './orm';
import type { IdentityUUID } from './types';
import { alice, aliceHyphenated, database, dsn, type TestDatabase } from './harness';

let db: TestDatabase;
let orm: FixtureVersionGraphDbOrm;

beforeAll(async () => {
  db = await database('ts_orm_released');
  orm = createOrm(db.pool, alice);
});

afterAll(async () => {
  await db?.drop();
});

/** A recipe, a tagged commit of it and the released parts a release of that commit writes. */
async function released(title: string, ...headings: string[]): Promise<IdentityUUID> {
  const recipe = await orm.recipe.createOne({ title });
  const root = uuidToDb(recipe.id);
  const ref = (await db.pool.query("INSERT INTO recipe_ref (root_id, name, created_by, updated_by) VALUES ($1, 'main', $2, $2) RETURNING id", [root, aliceHyphenated])).rows[0].id;
  const commit = (
    await db.pool.query("INSERT INTO recipe_commit (root_id, ref_id, schema_epoch, content_hash, \"sequence\", created_by) VALUES ($1, $2, 1, 'hash', 1, $3) RETURNING id", [root, ref, aliceHyphenated])
  ).rows[0].id;
  for (const [i, heading] of headings.entries()) {
    await db.pool.query('INSERT INTO part_released (recipe_id, entity_key, rank, heading, commit_id, "sequence") VALUES ($1, gen_random_uuid(), $2, $3, $4, 1)', [
      root,
      String.fromCharCode('F'.charCodeAt(0) + i),
      heading,
      commit,
    ]);
  }
  return recipe.id;
}

const headings = (rows: readonly { heading: string }[]) => rows.map(row => row.heading).sort();

describe.skipIf(dsn === '')('released rows', () => {
  test("the ORM holds a released table's reads alone, on the pool and in a transaction", async () => {
    const reads = [...READ_METHODS].sort();
    expect(Object.keys(orm.partReleased).sort()).toEqual(reads);
    const repository = orm.partReleased as unknown as Record<string, unknown>;
    for (const write of ['createOne', 'createMany', 'updateOne', 'updateMany', 'upsertOne', 'upsertMany', 'deleteOne', 'deleteMany', 'hardDeleteOne']) {
      expect(repository[write]).toBeUndefined();
    }
    await orm.transaction(async tx => {
      expect(Object.keys(tx.partReleased).sort()).toEqual(reads);
      expect((tx.partReleased as unknown as Record<string, unknown>).createOne).toBeUndefined();
    });
    // Part, the member, keeps its writes.
    expect(typeof orm.part.createOne).toBe('function');
  });

  test("a soft-deleted root's released rows are out of every read unless it includes deleted rows, and a restore reads them again", async () => {
    const bread = await released('Bread', 'Mix', 'Knead', 'Bake');
    await released('Soup', 'Boil');
    const ofBread = { recipeId: { eq: bread } };
    const before = await orm.partReleased.findMany(ofBread);
    expect(before.total).toBe(3);
    const part = before.rows[0]!;

    // Bread is soft-deleted through its repository: the rows stay in the
    // table, and every read leaves them out; Soup's stay.
    await orm.recipe.deleteOne(bread);
    expect((await db.pool.query('SELECT count(*)::int AS n FROM part_released WHERE recipe_id = $1', [uuidToDb(bread)])).rows[0].n).toBe(3);
    const after = await orm.partReleased.findMany(null);
    expect([after.total, headings(after.rows)]).toEqual([1, ['Boil']]);
    expect((await orm.partReleased.findMany(ofBread)).total).toBe(0);
    await expect(orm.partReleased.getOne(part.id)).rejects.toBeInstanceOf(NotFoundError);
    expect((await orm.partReleased.getManyByIds([part.id])).size).toBe(0);
    expect(await orm.partReleased.findOne(ofBread)).toBeNull();
    expect(headings((await orm.partReleased.findPage(null, { limit: 10 })).rows)).toEqual(['Boil']);
    // A read that includes deleted rows reads them, as it reads a
    // soft-deleted row.
    expect(headings((await orm.partReleased.findMany(ofBread, { includeDeleted: true })).rows)).toEqual(['Bake', 'Knead', 'Mix']);
    expect((await orm.partReleased.findPage(ofBread, { includeDeleted: true })).rows).toHaveLength(3);
    // A filter through the relation reads the live roots alone too.
    expect((await orm.partReleased.findMany({ recipe: { title: { eq: 'Bread' } } }, { includeDeleted: true })).total).toBe(0);
    expect(await orm.transaction(async tx => (await tx.partReleased.findMany(ofBread)).total)).toBe(0);

    // A restore of Bread reads the same rows again.
    await db.pool.query('UPDATE recipe SET deleted_at = NULL, deleted_by = NULL WHERE id = $1', [uuidToDb(bread)]);
    expect(headings((await orm.partReleased.findMany(ofBread)).rows)).toEqual(['Bake', 'Knead', 'Mix']);
    expect((await orm.partReleased.getOne(part.id)).id).toBe(part.id);
    expect((await orm.partReleased.getManyByIds([part.id])).size).toBe(1);
    expect((await orm.partReleased.findMany(null)).total).toBe(4);
  });
});
