import { describe, expect, test } from 'bun:test';
import pg from 'pg';
import { connectPostgres } from './postgres';
import { PG_TYPES, decodeRow, encodeParam, parseInt8, postgresTypeParsers, withTypeParsers, type RowField } from './pgcodec';
import { uuidFromDb, uuidToDb } from './uuid';

/** The variable that names a Postgres the pool tests run against; they are skipped without it. */
const dsn = process.env.SUPERSCHEMATIC_HTTP_RUNTIME_TEST_DATABASE_URL ?? '';

const hyphenated = '0f8b6c4e-1b2a-4c3d-9e8f-123456789abc';
const base62 = 'TKbHsHDO6x9RCjbs5c2u4';

describe('a UUID in its two forms', () => {
  test('uuidToDb writes either form hyphenated, uuidFromDb reads either as base62', () => {
    expect(uuidToDb(base62)).toBe(hyphenated);
    expect(uuidToDb(hyphenated.toUpperCase())).toBe(hyphenated);
    expect(uuidFromDb(hyphenated)).toBe(base62);
    expect(uuidFromDb(base62)).toBe(base62);
    expect(uuidToDb('0')).toBe('00000000-0000-0000-0000-000000000000');
    expect(uuidFromDb('00000000-0000-0000-0000-000000000000')).toBe('0');
  });

  test('text that is no UUID is refused', () => {
    expect(() => uuidToDb('not a uuid')).toThrow('is no UUID');
    expect(() => uuidFromDb(42)).toThrow('is no UUID');
    expect(() => uuidToDb('zzzzzzzzzzzzzzzzzzzzzz')).toThrow('is no UUID');
  });
});

describe('the type parsers', () => {
  test('a BIGINT is a number, refused past 2^53 - 1 either way', () => {
    expect(parseInt8('9007199254740991')).toBe(Number.MAX_SAFE_INTEGER);
    expect(parseInt8('-9007199254740991')).toBe(Number.MIN_SAFE_INTEGER);
    expect(() => parseInt8('9007199254740992')).toThrow('past 2^53 - 1');
    expect(() => parseInt8('-9007199254740993')).toThrow(RangeError);
    expect((postgresTypeParsers[PG_TYPES.int8Array] as (text: string) => unknown)('{1,NULL,3}')).toEqual([1, null, 3]);
  });

  test('a NUMERIC and a DATE keep their text', () => {
    expect((postgresTypeParsers[PG_TYPES.numeric] as (text: string) => unknown)('12.50')).toBe('12.50');
    expect((postgresTypeParsers[PG_TYPES.date] as (text: string) => unknown)('2026-10-25')).toBe('2026-10-25');
  });

  test('withTypeParsers falls back to the parsers it wraps, for the binary format too, and changes nothing global', () => {
    const types = withTypeParsers(pg.types);
    expect(types.getTypeParser(PG_TYPES.int8)).toBe(postgresTypeParsers[PG_TYPES.int8]);
    expect(types.getTypeParser(16)).toBe(pg.types.getTypeParser(16));
    expect(types.getTypeParser(PG_TYPES.int8, 'binary')).toBe(pg.types.getTypeParser(PG_TYPES.int8, 'binary'));
    expect(pg.types.getTypeParser(PG_TYPES.int8)).not.toBe(postgresTypeParsers[PG_TYPES.int8]);
  });
});

/** A line item's columns as a types package's ./orm entry describes them. */
const lineItem: readonly RowField[] = [
  { name: 'id', column: 'id', uuid: true },
  { name: 'amountCents', column: 'amount_cents', int64: true },
  { name: 'rate', column: 'rate', numeric: true },
  { name: 'bookedOn', column: 'booked_on', date: true },
  { name: 'postedAt', column: 'posted_at', timestamp: true },
  { name: 'categoryId', column: 'category_id', uuid: true },
  { name: 'tags', column: 'tags', list: true },
  { name: 'ownerIds', column: 'owner_ids', list: true, uuid: true },
  { name: 'metadata', column: 'metadata', json: true },
  { name: 'at', column: 'at', point: true },
  { name: 'took', column: 'took', interval: true },
  { name: 'order', column: '"order"', int64: true },
];

describe('decodeRow', () => {
  test('reads each column as the types hold it, a NULL as null, and leaves out a column the row does not hold', () => {
    const row = decodeRow(
      {
        id: hyphenated,
        amount_cents: '-1250',
        rate: 1.5,
        booked_on: '2026-10-25',
        posted_at: '2026-10-25 09:30:00.123+00',
        category_id: null,
        tags: '{groceries,"weekly shop"}',
        owner_ids: [hyphenated],
        metadata: { plaid: { pending: false } },
        at: { x: -0.1276, y: 51.5072 },
        took: '01:30:00',
        order: 3,
      },
      lineItem
    );
    expect(row).toEqual({
      id: base62,
      amountCents: -1250,
      rate: '1.5',
      bookedOn: '2026-10-25',
      postedAt: new Date('2026-10-25T09:30:00.123Z'),
      categoryId: null,
      tags: ['groceries', 'weekly shop'],
      ownerIds: [base62],
      metadata: { plaid: { pending: false } },
      at: { lat: 51.5072, lon: -0.1276 },
      took: '1h30m0s',
      order: 3,
    });
    expect(decodeRow({ id: hyphenated }, lineItem)).toEqual({ id: base62 });
  });

  test("reads pg's own forms: a DATE at local midnight, a parsed INTERVAL, a POINT's text", () => {
    const row = decodeRow(
      { booked_on: new Date(2026, 9, 25), took: { hours: 1, minutes: 30, milliseconds: 1.5 }, at: '(-0.1276,51.5072)' },
      lineItem
    );
    expect(row).toEqual({ bookedOn: '2026-10-25', took: '1h30m0.0015s', at: { lat: 51.5072, lon: -0.1276 } });
  });

  test('names the field of a value it cannot read', () => {
    expect(() => decodeRow({ amount_cents: '9007199254740993' }, lineItem)).toThrow('column amount_cents, field amountCents');
    expect(() => decodeRow({ id: 'not-a-uuid' }, lineItem)).toThrow('column id, field id');
  });
});

/** The JSON values a column may hold, each as pg reads it from JSONB and the jsonb_typeof Postgres gives it. */
const jsonValues: readonly [unknown, string][] = [
  ['x', 'string'],
  ['1', 'string'],
  [1, 'number'],
  [true, 'boolean'],
  [[1, 'a', null], 'array'],
  [{ a: 1, b: ['c'] }, 'object'],
];

describe('a JSON column', () => {
  test('reads JSON or JSONB as the value pg parsed, a string among them, and TEXT that holds JSON as its parsed text', () => {
    for (const [value] of jsonValues) {
      expect(decodeRow({ doc: value }, [{ name: 'doc', column: 'doc', json: true }])).toEqual({ doc: value });
      expect(decodeRow({ doc: JSON.stringify(value) }, [{ name: 'doc', column: 'doc', jsonText: true }])).toEqual({ doc: value });
    }
    // The JSON string "1" stays a string, and "x" is no JSON text to parse again.
    expect(decodeRow({ doc: '1' }, [{ name: 'doc', column: 'doc', json: true }])).toEqual({ doc: '1' });
    expect(decodeRow({ doc: null }, [{ name: 'doc', column: 'doc', json: true }])).toEqual({ doc: null });
  });

  test('binds every JSON value as its JSON text, and null as the JSON null only in a required column', () => {
    expect(encodeParam('x', { json: true })).toBe('"x"');
    expect(encodeParam('1', { json: true })).toBe('"1"');
    expect(encodeParam(1, { json: true })).toBe('1');
    expect(encodeParam([1, 'a'], { json: true })).toBe('[1,"a"]');
    expect(encodeParam({ a: 1 }, { json: true })).toBe('{"a":1}');
    expect(encodeParam(null, { json: true })).toBeNull();
    expect(encodeParam(null, { json: true, required: true })).toBe('null');
    expect(encodeParam(undefined, { json: true, required: true })).toBeNull();
    expect(encodeParam('x', { jsonText: true })).toBe('"x"');
  });

  test.skipIf(dsn === '')('holds a string, a number, a list, an object, the JSON null and SQL NULL, read alike with the parsers on and off', async () => {
    for (const typeParsers of [true, false]) {
      const pool = await connectPostgres({ url: dsn }, { typeParsers });
      const client = await pool.connect();
      try {
        await client.query('CREATE TEMPORARY TABLE codec_json (n int PRIMARY KEY, doc jsonb NOT NULL, maybe jsonb, text_doc text)');
        const doc: RowField = { name: 'doc', column: 'doc', json: true, required: true };
        const maybe: RowField = { name: 'maybe', column: 'maybe', json: true };
        const textDoc: RowField = { name: 'textDoc', column: 'text_doc', jsonText: true };
        const values: readonly [unknown, string][] = [...jsonValues, [null, 'null']];
        for (const [i, [value]] of values.entries()) {
          await client.query('INSERT INTO codec_json (n, doc, maybe, text_doc) VALUES ($1, $2, $3, $4)', [
            i,
            encodeParam(value, doc),
            encodeParam(value, maybe),
            encodeParam(value, textDoc),
          ]);
        }
        const { rows } = await client.query(
          'SELECT n, doc, jsonb_typeof(doc) AS doc_type, maybe, maybe IS NULL AS maybe_sql_null, text_doc FROM codec_json ORDER BY n'
        );
        for (const [i, [value, type]] of values.entries()) {
          const row = rows[i];
          expect([typeParsers, row.doc_type]).toEqual([typeParsers, type]);
          expect(decodeRow(row, [doc, maybe, textDoc])).toEqual({ doc: value, maybe: value, textDoc: value });
          // null in the required column is the JSON null; in the nullable one, SQL NULL.
          expect(row.maybe_sql_null).toBe(value === null);
        }
      } finally {
        client.release();
        await pool.end();
      }
    }
  });
});

describe('encodeParam', () => {
  test('binds each value as its column takes it', () => {
    expect(encodeParam(base62, { uuid: true })).toBe(hyphenated);
    expect(encodeParam([base62, hyphenated], { uuid: true, list: true })).toEqual([hyphenated, hyphenated]);
    expect(encodeParam({ plaid: true }, { json: true })).toBe('{"plaid":true}');
    expect(encodeParam({ lat: 51.5, lon: -0.12 }, { point: true })).toBe('(-0.12,51.5)');
    expect(encodeParam('1h30m0s', { interval: true })).toBe('1:30:00');
    expect(encodeParam(42n, { int64: true })).toBe(42);
    expect(encodeParam(null, { uuid: true })).toBeNull();
    expect(encodeParam(undefined, {})).toBeNull();
    const at = new Date('2026-10-25T09:30:00Z');
    expect(encodeParam(at, { timestamp: true })).toBe(at);
  });
});

describe('a pool connectPostgres opens', () => {
  test('carries the type parsers, unless typeParsers is false', async () => {
    const pool = await connectPostgres({ url: 'postgres://127.0.0.1:9/none' });
    const plain = await connectPostgres({ url: 'postgres://127.0.0.1:9/none' }, { typeParsers: false });
    try {
      const types = (pool as unknown as { options: { types?: pg.CustomTypesConfig } }).options.types;
      expect(types?.getTypeParser(PG_TYPES.int8, 'text')).toBe(postgresTypeParsers[PG_TYPES.int8] as never);
      expect((plain as unknown as { options: { types?: unknown } }).options.types).toBeUndefined();
    } finally {
      await pool.end();
      await plain.end();
    }
  });

  test.skipIf(dsn === '')('reads a BIGINT as a number, a NUMERIC as its text and a DATE as its text, and refuses a BIGINT past 2^53 - 1', async () => {
    const pool = await connectPostgres({ url: dsn });
    const plain = await connectPostgres({ url: dsn }, { typeParsers: false });
    try {
      const { rows } = await pool.query(`SELECT 42::bigint AS n, ARRAY[1, 2]::bigint[] AS ns, 12.50::numeric AS rate, DATE '2026-10-25' AS d`);
      expect(rows).toEqual([{ n: 42, ns: [1, 2], rate: '12.50', d: '2026-10-25' }]);
      await expect(pool.query('SELECT 9007199254740993::bigint AS n')).rejects.toThrow('past 2^53 - 1');
      // The pool still answers after a refused row.
      expect((await pool.query('SELECT 1::bigint AS n')).rows).toEqual([{ n: 1 }]);
      expect((await plain.query('SELECT 42::bigint AS n')).rows).toEqual([{ n: '42' }]);
    } finally {
      await pool.end();
      await plain.end();
    }
  });
});
