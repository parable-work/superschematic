// fixture-db's pages on Postgres (D60, amended; D79), the counterpart of
// the Go ORM's page_postgres_test.go: findPage by text, a citext column, a
// number and a time, each way, ties of one seatCount broken by id in the
// sort's direction; a filter and a search beside the keyset; a soft-deleted
// member left out unless included; a key that is the row's id alone; and a
// page in a transaction.
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { decodeCursor, encodeCursor } from '@superschematic/http-runtime';
import { PageKeyError } from '@superschematic/http-runtime/postgres';
import { createOrm, type FixtureDbOrm, type TenantFilter, type TenantPageKey, type TenantSortField } from './orm';
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
      'Tenant sorts by id, createdAt, name, slug, email, seatCount, not "status"'
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

  test('a page reads in a transaction', async () => {
    const names = await orm.transaction(async tx => (await tx.tenant.findPage({ seatCount: { eq: 5 } }, { sort: { field: 'name' }, limit: 2 })).rows.map(r => r.name));
    expect(names).toEqual(['Anvil', 'Drill']);
  });
});
