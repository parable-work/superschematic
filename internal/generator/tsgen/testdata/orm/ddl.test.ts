// The repositories of fixture-ddl-db on Postgres, over DDL that says what
// its schema says (D83, D79): a create that leaves a defaulted field unset
// writes the value the column's DEFAULT gives a raw insert, a value a
// column's CHECK refuses fails the write, deleting a category clears the
// references SET NULL names, a @jsonField value is a JSON object in its
// JSONB column, and an upsert by a unique @index over an expression, or
// with a where, infers that index.
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { uuidToDb } from '@superschematic/http-runtime/postgres';
import { createOrm, type FixtureDdlDbOrm } from './orm';
import type { CategoryKind, GenericInt64 } from './types';
import { database, dsn, type TestDatabase } from './harness';

const personal = 'personal' as CategoryKind;
const business = 'business' as CategoryKind;
const cents = (n: number) => n as GenericInt64;

let db: TestDatabase;
let orm: FixtureDdlDbOrm;

beforeAll(async () => {
  db = await database('ts_orm_ddl');
  orm = createOrm(db.pool);
});

afterAll(async () => {
  await db?.drop();
});

/** The Postgres error a write failed with: its SQLSTATE and the constraint it names. */
async function refusal(write: Promise<unknown>): Promise<{ code?: string; constraint?: string }> {
  try {
    await write;
  } catch (error) {
    const { code, constraint } = error as { code?: string; constraint?: string };
    return { code, constraint };
  }
  throw new Error('the write was not refused');
}

describe.skipIf(dsn === '')('the DDL under the repositories', () => {
  test("a create that leaves a defaulted field unset writes what the column's DEFAULT gives a raw insert", async () => {
    const food = await orm.category.createOne({ label: 'Food', kind: personal });
    expect([food.active, food.sortOrder]).toEqual([false, 0]);
    const { rows } = await db.pool.query(`INSERT INTO category (label, kind) VALUES ('Rent', 'business') RETURNING active, sort_order`);
    expect(rows[0]).toEqual({ active: food.active, sort_order: food.sortOrder });
    const lines = await orm.lineItem.createMany([
      { amountCents: cents(100), postedAt: new Date() },
      { amountCents: cents(200), postedAt: new Date(), memo: 'rent' },
    ]);
    const raw = await db.pool.query(`INSERT INTO line_item (amount_cents) VALUES (300) RETURNING memo`);
    expect(lines.map(l => l.memo)).toEqual([raw.rows[0].memo, 'rent']);
  });

  test("a value a column's CHECK refuses fails the write, naming the constraint", async () => {
    expect(await refusal(orm.category.createOne({ label: 'Bogus', kind: 'bogus' as CategoryKind }))).toEqual({ code: '23514', constraint: 'ck_category_kind' });
    expect(await refusal(orm.category.createOne({ label: 'Many', kind: business, sortOrder: 1001 }))).toEqual({ code: '23514', constraint: 'ck_category_sort_order' });
    const travel = await orm.category.createOne({ label: 'Travel', kind: business });
    expect(await refusal(orm.category.updateOne(travel.id, { label: '' }))).toEqual({ code: '23514', constraint: 'ck_category_label' });
  });

  test('deleting a category clears the references SET NULL names, and keeps their rows', async () => {
    const parent = await orm.category.createOne({ label: 'Household', kind: personal });
    const child = await orm.category.createOne({ label: 'Cleaning', kind: personal, parentId: parent.id });
    const line = await orm.lineItem.createOne({ categoryId: parent.id, amountCents: cents(450), postedAt: new Date() });
    await orm.category.deleteOne(parent.id);
    expect((await orm.category.getOne(child.id)).parentId).toBeNull();
    expect((await orm.lineItem.getOne(line.id)).categoryId).toBeNull();
  });

  test('a @jsonField value is a JSON object in its JSONB column, read back as the object', async () => {
    const merchant = { name: 'Corner Shop', city: 'Leeds' };
    const line = await orm.lineItem.createOne({ amountCents: cents(-350), postedAt: new Date(), merchant });
    expect(line.merchant).toEqual(merchant);
    expect((await orm.lineItem.getOne(line.id)).merchant).toEqual(merchant);
    const typed = async () =>
      (await db.pool.query(`SELECT jsonb_typeof(merchant) AS kind, merchant->>'name' AS name FROM line_item WHERE id = $1`, [uuidToDb(line.id)])).rows[0];
    expect(await typed()).toEqual({ kind: 'object', name: 'Corner Shop' });
    const moved = await orm.lineItem.updateOne(line.id, { merchant: { name: 'Corner Shop', city: null } });
    expect(moved.merchant).toEqual({ name: 'Corner Shop', city: null });
    const [unknown, known] = await orm.lineItem.createMany([
      { amountCents: cents(1), postedAt: new Date() },
      { amountCents: cents(2), postedAt: new Date(), merchant: { name: 'Market', city: null } },
    ]);
    expect([unknown!.merchant, known!.merchant]).toEqual([null, { name: 'Market', city: null }]);
  });

  test('an upsert by a unique @index over an expression, or with a where, infers the index', async () => {
    // label_lower is unique over lower(label): another case of a label is the same key.
    const groceries = await orm.category.upsertOne({ label: 'Groceries', kind: personal }, ['label'], { sortOrder: 5 });
    const again = await orm.category.upsertOne({ label: 'GROCERIES', kind: business }, ['label'], { sortOrder: 7 });
    expect([again.id, again.label, again.kind, again.sortOrder]).toEqual([groceries.id, 'Groceries', personal, 7]);
    // external is unique among the lines that hold an external id.
    const input = { externalId: 'bank-1', amountCents: cents(100), postedAt: new Date() };
    const posted = await orm.lineItem.upsertOne(input, ['externalId'], { amountCents: cents(100) });
    const cleared = await orm.lineItem.upsertOne({ ...input, amountCents: cents(150) }, ['externalId'], { amountCents: cents(150) });
    expect([cleared.id, cleared.amountCents]).toEqual([posted.id, 150]);
    const { rows } = await db.pool.query(`SELECT count(*)::int AS n FROM line_item WHERE external_id = 'bank-1'`);
    expect(rows[0].n).toBe(1);
  });
});
