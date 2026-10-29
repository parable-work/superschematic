// fixture-db's JSON column on Postgres (D79): tenant.metadata, a required
// Generic.JSON, holds each JSON value as it is, a string among them, which
// pg reads already parsed and the codec takes as it is; null is the JSON
// null, since the column may not be SQL NULL. A raw query through
// decodeRow reads it alike, on a pool with connectPostgres's parsers and on
// one with pg's own. Its filter's eq and in compare JSON values as JSONB
// does.
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { connectPostgres, decodeRow, uuidToDb } from '@superschematic/http-runtime/postgres';
import { createOrm, tenantTable, type TenantRow } from './orm';
import type { GenericJSON, IdentityName, IdentitySlug } from './types';
import { alice, database, dsn, type TestDatabase } from './harness';

let db: TestDatabase;

beforeAll(async () => {
  db = await database('ts_orm_json');
});

afterAll(async () => {
  await db?.drop();
});

/** Each JSON value the column holds, and the jsonb_typeof Postgres gives it. */
const values: readonly [GenericJSON, string][] = [
  ['x', 'string'],
  ['1', 'string'],
  [1, 'number'],
  [false, 'boolean'],
  [[1, 'a', null], 'array'],
  [{ a: 1, b: ['c'] }, 'object'],
  [null, 'null'],
];

describe.skipIf(dsn === '')('a JSON column', () => {
  test("a JSON column holds a string, a number, a list, an object and the JSON null as they are, with the pool's parsers on and off", async () => {
    let n = 0;
    for (const typeParsers of [true, false]) {
      const pool = await connectPostgres({ url: dsn }, { typeParsers, pool: { options: `-c search_path=${db.schema},public` } });
      try {
        const orm = createOrm(pool, alice);
        for (const [value, type] of values) {
          const slug = `t${++n}`;
          const created = await orm.tenant.createOne({
            name: `Tenant ${slug}` as IdentityName,
            slug: slug as IdentitySlug,
            email: `${slug}@example.com`,
            metadata: value,
          });
          expect([typeParsers, created.metadata]).toEqual([typeParsers, value]);
          expect((await orm.tenant.getOne(created.id)).metadata).toEqual(value);
          const { rows } = await pool.query('SELECT metadata, jsonb_typeof(metadata) AS type, metadata IS NULL AS sql_null FROM tenant WHERE id = $1', [
            uuidToDb(created.id),
          ]);
          expect([value, rows[0].type, rows[0].sql_null]).toEqual([value, type, false]);
          expect(decodeRow<Pick<TenantRow, 'metadata'>>(rows[0], tenantTable.columns)).toEqual({ metadata: value });
        }
        // An update writes each value alike, null the JSON null.
        const tenant = await orm.tenant.createOne({ name: 'Updated' as IdentityName, slug: `u${++n}` as IdentitySlug, email: `u${n}@example.com`, metadata: {} });
        for (const [value, type] of values) {
          expect((await orm.tenant.updateOne(tenant.id, { metadata: value })).metadata).toEqual(value);
          const { rows } = await pool.query('SELECT jsonb_typeof(metadata) AS type FROM tenant WHERE id = $1', [uuidToDb(tenant.id)]);
          expect([value, rows[0].type]).toEqual([value, type]);
        }
      } finally {
        await pool.end();
      }
    }
  });

  test('eq and in on a JSON column compare JSON values as JSONB does', async () => {
    const orm = createOrm(db.pool, alice);
    const tenant = async (slug: string, metadata: GenericJSON) =>
      (await orm.tenant.createOne({ name: `Filter ${slug}` as IdentityName, slug: slug as IdentitySlug, email: `${slug}@example.com`, metadata })).id;
    const object = await tenant('f-object', { plan: 'team', seats: [1, 2] });
    const text = await tenant('f-text', '1');
    const number = await tenant('f-number', 1);
    const ids = async (filter: Parameters<typeof orm.tenant.findMany>[0]) =>
      (await orm.tenant.findMany({ ...filter, slug: { in: ['f-object' as IdentitySlug, 'f-text' as IdentitySlug, 'f-number' as IdentitySlug] } }, { orderBy: [{ field: 'slug' }] })).rows.map(r => r.id);
    // An object equals one whose keys are in another order; the string "1" is not the number 1.
    expect(await ids({ metadata: { eq: { seats: [1, 2], plan: 'team' } } })).toEqual([object]);
    expect(await ids({ metadata: { eq: '1' } })).toEqual([text]);
    expect(await ids({ metadata: { eq: 1 } })).toEqual([number]);
    expect(await ids({ metadata: { in: [1, '1'] } })).toEqual([number, text]);
    expect(await ids({ metadata: { in: [{ plan: 'solo' }] } })).toEqual([]);
  });
});
