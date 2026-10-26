// The reads of fixture-finances-db on Postgres (D90): a filter through a
// to-one relation and anyOf, select and include, to-one and one-to-many,
// the dashboard's transaction list as one findPage, and the
// TransactionListRow projection read through its repository.
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { createOrm, type FixtureFinancesDbOrm, type LineItemFilter } from './orm';
import type { FinanceSignedMoney, IdentityName, IdentitySlug } from './types';
import { alice, database, dsn, recording, type TestDatabase } from './harness';

const name = (text: string) => text as IdentityName;
const slug = (text: string) => text as IdentitySlug;
const money = (n: number) => n as FinanceSignedMoney;
const day = (d: number) => new Date(Date.UTC(2026, 0, d, 12));

let db: TestDatabase;
let orm: FixtureFinancesDbOrm;
const ids: Record<string, string> = {};

beforeAll(async () => {
  db = await database('ts_orm_finances');
  orm = createOrm(db.pool).withUser(alice);
  const ann = await orm.user.createOne({ email: 'ann@example.com', name: name('Ann'), createdAt: day(1) });
  const ben = await orm.user.createOne({ email: 'ben@example.com', name: name('Ben'), createdAt: day(1) });
  const bank = await orm.provider.createOne({ name: name('First Bank'), slug: slug('first-bank') });
  const shop = await orm.business.createOne({ name: name('Shop'), slug: slug('shop') });
  const checking = await orm.account.createOne({ name: name('Checking'), slug: slug('checking'), providerId: bank.id, ownerId: ann.id });
  const category = async (label: string, parentId?: string) =>
    (await orm.category.createOne({ name: name(label), slug: slug(label.toLowerCase()), color: '#00aa00', parentId: parentId ?? null })).id;
  ids.ann = ann.id;
  ids.ben = ben.id;
  ids.shop = shop.id;
  ids.checking = checking.id;
  ids.food = await category('Food');
  ids.groceries = await category('Groceries', ids.food);
  ids.dining = await category('Dining', ids.food);
  ids.travel = await category('Travel');
  ids.retired = await category('Retired', ids.travel);
  const line = async (d: number, label: string, amount: number, categoryId: string | null, businessId: string | null, userId: string | null) =>
    (await orm.lineItem.createOne({ date: day(d), name: label, amount: money(amount), accountId: checking.id, categoryId, businessId, userId, createdAt: day(d) })).id;
  ids.beans = await line(5, 'Coffee beans', -1250, ids.groceries, shop.id, ann.id);
  ids.lunch = await line(6, 'Lunch', -500, ids.food, null, ben.id);
  ids.train = await line(7, 'Train', -9000, ids.travel, null, ann.id);
  ids.dinner = await line(8, 'Dinner and coffee', -2000, ids.dining, shop.id, ann.id);
  ids.salary = await line(9, 'Salary', 10000, null, null, ann.id);
  ids.old = await line(10, 'Old fare', -300, ids.retired, null, null);
  await orm.note.createOne({ lineItemId: ids.beans, content: 'for the office', createdAt: day(5) });
  const withdrawn = await orm.note.createOne({ lineItemId: ids.beans, content: 'withdrawn', createdAt: day(5) });
  await orm.note.deleteOne(withdrawn.id);
  await orm.category.deleteOne(ids.retired);
});

afterAll(async () => {
  await db?.drop();
});

/** The names of the line items filter finds, by date. */
async function names(filter: LineItemFilter): Promise<string[]> {
  const { rows } = await orm.lineItem.findMany(filter, { orderBy: [{ field: 'date' }] });
  return rows.map(row => row.name);
}

describe.skipIf(dsn === '')('the finances', () => {
  test('a filter through a to-one relation leaves out a row whose relation is null or soft-deleted', async () => {
    expect(await names({ category: { parentId: { in: [ids.food!] } } })).toEqual(['Coffee beans', 'Dinner and coffee']);
    // Retired is soft-deleted: no live category the filter finds.
    expect(await names({ category: { parentId: { in: [ids.travel!] } } })).toEqual([]);
    // Two relations deep: the category's parent's name.
    expect(await names({ category: { parent: { name: { eq: name('Food') } } } })).toEqual(['Coffee beans', 'Dinner and coffee']);
    // A filter of no member finds the rows with a live category.
    expect(await names({ category: {} })).toEqual(['Coffee beans', 'Lunch', 'Train', 'Dinner and coffee']);
  });

  test('anyOf ORs filters, and a filter of no member keeps every row', async () => {
    const either: LineItemFilter[] = [{ categoryId: { in: [ids.food!] } }, { category: { parentId: { in: [ids.food!] } } }];
    expect(await names({ anyOf: either })).toEqual(['Coffee beans', 'Lunch', 'Dinner and coffee']);
    expect(await names({ anyOf: either, userId: { eq: ids.ann! } })).toEqual(['Coffee beans', 'Dinner and coffee']);
    expect(await names({ anyOf: [{ categoryId: { in: [ids.travel!] } }, {}], amount: { lt: money(0) } })).toHaveLength(5);
    expect(await names({ anyOf: [] })).toHaveLength(6);
  });

  test("the dashboard's transaction list is one findPage, each row with its relations", async () => {
    const recorded = recording(db.pool);
    const reads = createOrm(recorded.pool);
    const read = (after: { id: string } | null) =>
      reads.lineItem.findPage(
        {
          date: { gte: day(1), lt: day(31) },
          anyOf: [{ categoryId: { in: [ids.food!] } }, { category: { parentId: { in: [ids.food!] } } }],
          businessId: { eq: ids.shop! },
          userId: { eq: ids.ann! },
          amount: { between: [money(-5000), money(0)] },
          searchText: 'coffee',
        },
        {
          include: { account: { include: { provider: true } }, category: { include: { parent: true } }, business: true, user: true, notes: true },
          sort: { field: 'date', desc: true },
          limit: 1,
          after,
        }
      );
    const first = await read(null);
    expect(first.rows).toHaveLength(1);
    const dinner = first.rows[0]!;
    expect(dinner.name).toBe('Dinner and coffee');
    expect(dinner.account.name).toBe(name('Checking'));
    expect(dinner.account.provider.name).toBe(name('First Bank'));
    expect(dinner.category?.name).toBe(name('Dining'));
    expect(dinner.category?.parent?.name).toBe(name('Food'));
    expect(dinner.business?.name).toBe(name('Shop'));
    expect(dinner.user?.name).toBe(name('Ann'));
    expect(dinner.notes).toEqual([]);
    expect(first.next).toEqual({ id: ids.dinner });
    // One statement for the page, and one per relation for all of its
    // rows, the one past the page among them: no count.
    expect(recorded.statements).toHaveLength(8);
    expect(recorded.statements.some(text => text.startsWith('SELECT COUNT'))).toBe(false);
    const second = await read(first.next);
    expect(second.rows.map(row => row.name)).toEqual(['Coffee beans']);
    expect(second.next).toBeNull();
    const beans = second.rows[0]!;
    expect(beans.category?.name).toBe(name('Groceries'));
    expect(beans.category?.parent?.name).toBe(name('Food'));
    // The withdrawn note is soft-deleted, so the include leaves it out.
    expect(beans.notes.map(note => note.content)).toEqual(['for the office']);
  });

  test('select reads the members it names, and an include reads the foreign key it needs without keeping it', async () => {
    const recorded = recording(db.pool);
    const reads = createOrm(recorded.pool);
    const row = await reads.lineItem.getOne(ids.beans!, { select: ['id', 'name'], include: { category: { select: ['id', 'name'] } } });
    expect(row).toEqual({ id: ids.beans!, name: 'Coffee beans', category: { id: ids.groceries!, name: name('Groceries') } });
    expect(recorded.statements[0]).toBe('SELECT "line_item"."id", "line_item"."name", "line_item"."category_id" FROM line_item WHERE id = $1');
    expect(recorded.statements[1]).toBe('SELECT "category"."id", "category"."name" FROM category WHERE category.id IN ($1) AND deleted_at IS NULL LIMIT 1');
    const picked = await reads.lineItem.findMany({ userId: { eq: ids.ben! } }, { select: ['date', 'amount'] });
    expect(picked.rows).toEqual([{ date: day(6), amount: money(-500) }]);
    await expect(reads.lineItem.findMany(null, { select: ['vendor' as never] })).rejects.toThrow(/selects its members/);
    await expect(reads.lineItem.findMany(null, { include: { vendor: true } as never })).rejects.toThrow(/includes account, category/);
  });

  test("a to-one include of a soft-deleted or unset target is null, and one-to-many includes map each row's rows", async () => {
    const old = await orm.lineItem.getOne(ids.old!, { include: { category: true } });
    expect(old.categoryId).toBe(ids.retired!);
    expect(old.category).toBeNull();
    const salary = await orm.lineItem.findOne({ name: { eq: 'Salary' } }, { include: { category: true, business: true } });
    expect(salary?.category).toBeNull();
    expect(salary?.business).toBeNull();
    const food = await orm.category.getOne(ids.food!, { include: { categories: { select: ['name'] }, lineItemsByCategory: { select: ['name'] } } });
    expect(food.categories.map(c => c.name).sort()).toEqual([name('Dining'), name('Groceries')]);
    expect(food.lineItemsByCategory.map(l => l.name)).toEqual(['Lunch']);
    const checking = await orm.account.getOne(ids.checking!, { include: { lineItems: true, provider: true } });
    expect(checking.lineItems).toHaveLength(6);
    expect(checking.provider.name).toBe(name('First Bank'));
  });

  test('the TransactionListRow projection reads through findPage, filtered on joined columns', async () => {
    const first = await orm.transactionListRow.findPage(
      { anyOf: [{ categoryId: { in: [ids.food!] } }, { parentCategoryId: { in: [ids.food!] } }], providerName: { eq: name('First Bank') } },
      { sort: { field: 'date', desc: true }, limit: 2 }
    );
    expect(first.rows.map(row => row.name)).toEqual(['Dinner and coffee', 'Lunch']);
    expect(first.rows[0]).toMatchObject({
      accountName: 'Checking',
      providerName: 'First Bank',
      categoryName: 'Dining',
      parentCategoryName: 'Food',
      businessName: 'Shop',
      userName: 'Ann',
    });
    const rest = await orm.transactionListRow.findPage(
      { anyOf: [{ categoryId: { in: [ids.food!] } }, { parentCategoryId: { in: [ids.food!] } }] },
      { sort: { field: 'date', desc: true }, limit: 2, after: first.next }
    );
    expect(rest.rows.map(row => row.name)).toEqual(['Coffee beans']);
    expect(rest.next).toBeNull();
    const salary = await orm.transactionListRow.getOne(ids.salary!);
    expect(salary).toMatchObject({ name: 'Salary', amount: 10000, categoryName: null, parentCategoryName: null });
    const shop = await orm.transactionListRow.findMany({ businessName: { isNull: false } }, { orderBy: [{ field: 'amount' }] });
    expect(shop.total).toBe(2);
    expect(shop.rows.map(row => row.name)).toEqual(['Dinner and coffee', 'Coffee beans']);
    const inTx = await orm.transaction(tx => tx.transactionListRow.findMany(null, { select: ['id'] }));
    expect(inTx.total).toBe(6);
  });
});
