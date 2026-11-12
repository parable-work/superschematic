// fixture-db's pages on Postgres (D60, amended; D79), the counterpart of
// the Go ORM's page_postgres_test.go: findPage by text, a citext column, a
// number and a time, each way, ties of one seatCount broken by id in the
// sort's direction; a filter and a search beside the keyset; a soft-deleted
// member left out unless included; a key that is the row's id alone; a
// column that may be null, its nulls last either way (D112), on Postgres
// through findPage and on SQLite through the statements themselves; and a
// page in a transaction.
import { Database } from 'bun:sqlite';
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { decodeCursor, encodeCursor } from '@superschematic/http-runtime';
import { PageKeyError } from '@superschematic/http-runtime/postgres';
import { createOrm, tenantTable, type FixtureDbOrm, type TenantFilter, type TenantPageKey, type TenantSortField } from './orm';
import type { IdentityUUID } from './types';
import { alice, database, dsn, type TestDatabase } from './harness';

let db: TestDatabase;
let orm: FixtureDbOrm;

/** A tenant's created_at: a day of October 2026 at noon, with microseconds a key keeps. */
const created = (day: number) => `2026-10-${String(day).padStart(2, '0')} 12:00:00.00${day}+00`;

beforeAll(async () => {
  db = await database('ts_orm_page');
  orm = createOrm(db.pool, alice);
  // Five tenants. Three hold five seats, by ids that put Easel, Drill and
  // Anvil in that order; the ids put Bellows before Chisel, as their seats
  // do not. Each was created a day apart.
  for (const [id, name, seats, day] of [
    [3, 'Anvil', 5, 5],
    [4, 'Bellows', 20, 4],
    [5, 'Chisel', 10, 3],
    [2, 'Drill', 5, 2],
    [1, 'Easel', 5, 1],
  ] as const) {
    await db.pool.query(
      "INSERT INTO tenant (id, name, slug, email, status, is_active, seat_count, metadata, created_at) VALUES ($1, $2, $3, $4, 'active', true, $5, '{}', $6)",
      [`00000000-0000-0000-0000-00000000000${id}`, name, name.toLowerCase(), `${name.toLowerCase()}@example.com`, seats, created(day)]
    );
  }
});

afterAll(async () => {
  await db?.drop();
});

/** Every page filter finds in sort's order, limit a page, each next through a cursor, as names in brackets. */
async function walk(filter: TenantFilter | null, sort: string, limit: number): Promise<string> {
  const desc = sort.startsWith('-');
  const field = (desc ? sort.slice(1) : sort) as TenantSortField;
  const pages: string[] = [];
  let after: TenantPageKey | null = null;
  while (pages.length < 10) {
    const page = await orm.tenant.findPage(filter, { sort: { field, desc }, after, limit });
    pages.push(`[${page.rows.map(r => r.name).join(' ')}]`);
    if (!page.next) return pages.join(' ');
    after = decodeCursor<TenantPageKey>(encodeCursor(page.next));
  }
  throw new Error(`findPage by ${sort} read more than 10 pages: ${pages.join(' ')}`);
}

describe.skipIf(dsn === '')('pages', () => {
  test("pages by text, a number and a time, each way, ties broken by the key in the sort's direction", async () => {
    for (const [sort, limit, want] of [
      ['name', 2, '[Anvil Bellows] [Chisel Drill] [Easel]'],
      ['-name', 2, '[Easel Drill] [Chisel Bellows] [Anvil]'],
      ['slug', 3, '[Anvil Bellows Chisel] [Drill Easel]'],
      ['-email', 4, '[Easel Drill Chisel Bellows] [Anvil]'],
      ['seatCount', 2, '[Easel Drill] [Anvil Chisel] [Bellows]'],
      ['-seatCount', 2, '[Bellows Chisel] [Anvil Drill] [Easel]'],
      ['createdAt', 2, '[Easel Drill] [Chisel Bellows] [Anvil]'],
      ['-createdAt', 3, '[Anvil Bellows Chisel] [Drill Easel]'],
      ['id', 2, '[Easel Drill] [Anvil Bellows] [Chisel]'],
      ['-id', 2, '[Chisel Bellows] [Anvil Drill] [Easel]'],
      ['name', 5, '[Anvil Bellows Chisel Drill Easel]'],
      ['-seatCount', 0, '[Bellows Chisel Anvil Drill Easel]'],
    ] as const) {
      expect(`${sort}: ${await walk(null, sort, limit)}`).toBe(`${sort}: ${want}`);
    }
    await expect(orm.tenant.findPage(null, { sort: { field: 'status' as TenantSortField } })).rejects.toThrow(
      'Tenant sorts by id, createdAt, updatedAt, name, slug, email, seatCount, not "status"'
    );
    await expect(orm.tenant.findPage(null, { limit: -1 })).rejects.toThrow('1 or more');
  });

  test('a filter and a search sit beside the keyset, and a deleted member is out of every page unless included', async () => {
    expect(await walk({ seatCount: { eq: 5 } }, '-name', 2)).toBe('[Easel Drill] [Anvil]');
    expect(await walk({ searchText: 'EL' }, 'name', 2)).toBe('[Bellows Chisel] [Easel]');
    expect(await walk({ searchText: 'el', seatCount: { lte: 10 } }, '-seatCount', 1)).toBe('[Chisel] [Easel]');
    expect(await walk({ seatCount: { gt: 20 } }, 'name', 2)).toBe('[]');
    // Anvil's members, one deleted.
    for (const [member, day, deleted] of [
      ['Ann', 1, false],
      ['Bo', 2, true],
      ['Cy', 3, false],
    ] as const) {
      await db.pool.query('INSERT INTO tenant_user (tenant_id, display_name, created_at, deleted_at) VALUES ($1, $2, $3, $4)', [
        '00000000-0000-0000-0000-000000000003',
        member,
        created(day),
        deleted ? created(4) : null,
      ]);
    }
    const members = async (desc: boolean, includeDeleted: boolean) => {
      const out: string[] = [];
      let after = null;
      do {
        const page = await orm.tenantUser.findPage(null, { sort: { field: 'createdAt', desc }, after, limit: 1, includeDeleted });
        out.push(...page.rows.map(r => r.displayName ?? ''));
        after = page.next;
      } while (after);
      return out.join(' ');
    };
    expect(await members(false, false)).toBe('Ann Cy');
    expect(await members(true, false)).toBe('Cy Ann');
    expect(await members(false, true)).toBe('Ann Bo Cy');
  });

  test("a key is the row's id alone, which places a page however the row changed since", async () => {
    const first = await orm.tenant.findPage(null, { sort: { field: 'name' }, limit: 2 });
    // The id is base62, as the wire holds it.
    expect(first.next).toEqual({ id: '4' as IdentityUUID });
    const after = async (filter: TenantFilter | null, field: TenantSortField, desc: boolean, key: TenantPageKey) =>
      (await orm.tenant.findPage(filter, { sort: { field, desc }, after: key, limit: 10 })).rows.map(r => r.name).join(' ');
    expect(await after(null, 'seatCount', true, first.next!)).toBe('Chisel Anvil Drill Easel');
    expect(await after(null, 'createdAt', false, first.next!)).toBe('Anvil');
    expect(await after(null, 'id', false, first.next!)).toBe('Chisel');
    // A row the filter no longer finds still places the page.
    const page = await orm.tenant.findPage({ seatCount: { eq: 5 } }, { sort: { field: 'name', desc: true }, limit: 2 });
    expect(page.rows.map(r => r.name)).toEqual(['Easel', 'Drill']);
    await db.pool.query("UPDATE tenant SET seat_count = 6 WHERE name = 'Drill'");
    expect(await after({ seatCount: { eq: 5 } }, 'name', true, page.next!)).toBe('Anvil');
    await db.pool.query("UPDATE tenant SET seat_count = 5 WHERE name = 'Drill'");
    // A key of no row places no page in an order by a sort field; by id the id alone places it.
    const gone = { id: '9' as IdentityUUID };
    await expect(after(null, 'name', false, gone)).rejects.toBeInstanceOf(PageKeyError);
    await expect(after({ seatCount: { eq: 5 } }, 'createdAt', false, gone)).rejects.toBeInstanceOf(PageKeyError);
    await expect(after(null, 'name', false, {} as TenantPageKey)).rejects.toBeInstanceOf(PageKeyError);
    expect(await after(null, 'id', true, gone)).toBe('Chisel Bellows Anvil Drill Easel');
    // A key of the last row is an empty page, not a refusal.
    expect(await after(null, 'name', true, { id: '3' as IdentityUUID })).toBe('');
  });

  test('a column that may be null sorts its nulls last either way, and a page ends on any row without dropping or repeating one', async () => {
    // Chisel was updated last, Easel and Bellows at one time before it,
    // and Anvil and Drill never: the two that tie read in id order, Easel's
    // 1 before Bellows' 4, and the two that hold a null come last, Drill's
    // 2 before Anvil's 3, each pair turned round going down, where the
    // nulls are still last.
    for (const [name, day] of [['Chisel', 12], ['Easel', 11], ['Bellows', 11]] as const) {
      await db.pool.query('UPDATE tenant SET updated_at = $1 WHERE name = $2', [created(day), name]);
    }
    for (const [sort, limit, want] of [
      ['updatedAt', 1, '[Easel] [Bellows] [Chisel] [Drill] [Anvil]'],
      ['updatedAt', 2, '[Easel Bellows] [Chisel Drill] [Anvil]'],
      ['updatedAt', 3, '[Easel Bellows Chisel] [Drill Anvil]'],
      ['updatedAt', 4, '[Easel Bellows Chisel Drill] [Anvil]'],
      ['updatedAt', 5, '[Easel Bellows Chisel Drill Anvil]'],
      ['-updatedAt', 1, '[Chisel] [Bellows] [Easel] [Anvil] [Drill]'],
      ['-updatedAt', 2, '[Chisel Bellows] [Easel Anvil] [Drill]'],
      ['-updatedAt', 3, '[Chisel Bellows Easel] [Anvil Drill]'],
      ['-updatedAt', 4, '[Chisel Bellows Easel Anvil] [Drill]'],
    ] as const) {
      expect(`${sort} by ${limit}: ${await walk(null, sort, limit)}`).toBe(`${sort} by ${limit}: ${want}`);
    }
    // A filter and a search hold beside it.
    expect(await walk({ updatedAt: { isNull: false } }, '-updatedAt', 1)).toBe('[Chisel] [Bellows] [Easel]');
    expect(await walk({ updatedAt: { isNull: true } }, 'updatedAt', 1)).toBe('[Drill] [Anvil]');
    expect(await walk({ updatedAt: { isNull: true } }, '-updatedAt', 1)).toBe('[Anvil] [Drill]');
    expect(await walk({ searchText: 'l' }, '-updatedAt', 1)).toBe('[Chisel] [Bellows] [Easel] [Anvil] [Drill]');
    // A key of no row places no page, whichever the column holds.
    const gone = { id: '9' as IdentityUUID };
    await expect(orm.tenant.findPage(null, { sort: { field: 'updatedAt' }, after: gone })).rejects.toBeInstanceOf(PageKeyError);
    await expect(orm.tenant.findPage({ updatedAt: { isNull: true } }, { sort: { field: 'updatedAt', desc: true }, after: gone })).rejects.toBeInstanceOf(PageKeyError);
  });

  test('a page reads in a transaction', async () => {
    const names = await orm.transaction(async tx => (await tx.tenant.findPage({ seatCount: { eq: 5 } }, { sort: { field: 'name' }, limit: 2 })).rows.map(r => r.name));
    expect(names).toEqual(['Anvil', 'Drill']);
  });
});

// The statements both ORMs page by, as fixture-db's types carry them, on
// SQLite: the keyset and the order of a column that may be null read the
// same rows there as on Postgres, though SQLite orders a null before
// every value where Postgres orders it after (D112). No repository runs
// on SQLite; this holds the statements' SQL to both.
describe('the page statements of a column that may be null, on SQLite', () => {
  const s = tenantTable.statements;
  const fill = (format: string, ...values: (string | number)[]) => {
    let at = 0;
    return format.replace(/%[sd]/gu, () => String(values[at++]));
  };
  const sqlite = new Database(':memory:');
  sqlite.run('CREATE TABLE tenant (id TEXT PRIMARY KEY, name TEXT NOT NULL, updated_at TEXT)');
  for (const [id, name, updated] of [
    ['3', 'Anvil', null],
    ['4', 'Bellows', '2026-10-11'],
    ['5', 'Chisel', '2026-10-12'],
    ['2', 'Drill', null],
    ['1', 'Easel', '2026-10-11'],
  ] as const) {
    sqlite.run('INSERT INTO tenant VALUES (?1, ?2, ?3)', [id, name, updated]);
  }
  /** The names after the row whose id is key, or of every row, in the order of updated_at. */
  const page = (desc: boolean, key?: string): string => {
    const [comparison, direction] = desc ? ['<', 'DESC'] : ['>', 'ASC'];
    const column = 'tenant.updated_at';
    const pageKey = 'page_key.updated_at';
    let where = '';
    if (key !== undefined) {
      const afterKey = fill(s.pageAfterKey, comparison, '?1');
      const after = fill(s.pageAfter, column, comparison, pageKey, '?1', '?1');
      where = ` WHERE ${fill(s.pageAfterNulls!, pageKey, '?1', column, afterKey, column, after)}`;
    }
    const order = fill(s.pageOrderNulls!, column, direction) + fill(s.pageTiebreak, direction);
    const sql = fill(s.page, 'tenant.name', where, order, 10);
    const rows = (key === undefined ? sqlite.query(sql).all() : sqlite.query(sql).all(key)) as { name: string }[];
    return rows.map(row => row.name).join(' ');
  };

  test('the nulls are last either way, and the rows after each key are the rest of the order', () => {
    for (const [desc, want, ids] of [
      [false, ['Easel', 'Bellows', 'Chisel', 'Drill', 'Anvil'], ['1', '4', '5', '2', '3']],
      [true, ['Chisel', 'Bellows', 'Easel', 'Anvil', 'Drill'], ['5', '4', '1', '3', '2']],
    ] as const) {
      expect(page(desc)).toBe(want.join(' '));
      ids.forEach((id, at) => {
        expect(`after ${id}: ${page(desc, id)}`).toBe(`after ${id}: ${want.slice(at + 1).join(' ')}`);
      });
      // A key of no row places none, so the repository can tell it from the last page.
      expect(page(desc, '9')).toBe('');
    }
  });
});
