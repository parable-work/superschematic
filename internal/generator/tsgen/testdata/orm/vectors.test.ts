// The repositories of fixture-vectors-db on a Postgres with pgvector (D84):
// a signed amount and a decimal read back as the types hold them, a NUMERIC
// in its column's scale as the canonical text the wire holds, a decimal
// filter compares numbers, a vector crosses as its numbers, and nearest
// finds a row's neighbours by each metric, through a filter and around a
// soft-deleted row, with the statement sqlutil writes.
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { decodeRow, uuidToDb } from '@superschematic/http-runtime/postgres';
import { createOrm, ledgerLineTable, type FixtureVectorsDbOrm, type PassageRow } from './orm';
import type { FinanceCurrency, FinanceSignedMoney, GenericDecimal } from './types';
import { alice, database, dsn, recording, type TestDatabase } from './harness';

const usd = 'USD' as FinanceCurrency;
const money = (n: number) => n as FinanceSignedMoney;
const decimal = (s: string) => s as GenericDecimal;

let db: TestDatabase;
let orm: FixtureVectorsDbOrm;

beforeAll(async () => {
  db = await database('ts_orm_vectors');
  orm = createOrm(db.pool, alice);
});

afterAll(async () => {
  await db?.drop();
});

/** The passages nearest finds, by title, nearest first. */
const titles = (neighbours: readonly { row: PassageRow }[]): string[] => neighbours.map(n => n.row.title);

describe.skipIf(dsn === '')('decimals and vectors under the repositories', () => {
  test('a signed amount, a decimal in its column scale and an exact one read back as the wire holds them', async () => {
    const quantity = decimal('-12345678901234567890.123456789');
    const debit = await orm.ledgerLine.createOne({ amount: money(-1250), currency: usd, rate: decimal('1.50'), quantity, bookedAt: new Date() });
    expect([debit.amount, debit.rate, debit.confidence, debit.quantity]).toEqual([-1250, '1.5', null, quantity]);
    const { rows } = await db.pool.query(`SELECT id, amount, rate, rate::text AS stored, quantity FROM ledger_line WHERE id = $1`, [uuidToDb(debit.id)]);
    expect(rows[0].stored).toBe('1.5000');
    // A raw query through the codec reads what the repository does.
    expect(decodeRow(rows[0], ledgerLineTable.columns)).toEqual({ id: debit.id, amount: -1250, rate: '1.5', quantity });
    const top = await orm.ledgerLine.createOne({ amount: money(9007199254740991), currency: usd, rate: decimal('1234.5678'), confidence: decimal('0.75'), bookedAt: new Date() });
    expect(await orm.ledgerLine.getOne(top.id)).toMatchObject({ amount: 9007199254740991, rate: '1234.5678', confidence: '0.75' });
  });

  test('a decimal filter compares numbers, each value bound as its text', async () => {
    const { rows } = await orm.ledgerLine.findMany({ rate: { gte: decimal('1.50'), lt: decimal('1000') } });
    expect(rows.map(r => r.rate)).toEqual(['1.5']);
    const exact = await orm.ledgerLine.findOne({ rate: { eq: decimal('1234.56780') } });
    expect(exact?.rate).toBe('1234.5678');
  });

  test('a vector reads and writes as its numbers, and nearest finds the neighbours by each metric', async () => {
    const ids: Record<string, string> = {};
    for (const [title, topic, embedding] of [
      ['east', 'maps', [1, 0, 0]],
      ['north-east', 'maps', [0.7, 0.7, 0]],
      ['north', 'maps', [0, 1, 0]],
      ['up', 'sky', [0, 0, 1]],
      ['far east', 'maps', [10, 0.5, 0]],
    ] as const) {
      const created = await orm.passage.createOne({ title, topic, embedding: [...embedding] });
      expect([created.embedding, created.titleEmbedding]).toEqual([[...embedding], null]);
      ids[title] = created.id;
    }
    const { rows } = await db.pool.query(`SELECT embedding::text AS text FROM passage WHERE id = $1`, [uuidToDb(ids['north-east']!)]);
    expect(rows[0].text).toBe('[0.7,0.7,0]');
    const updated = await orm.passage.updateOne(ids.up!, { titleEmbedding: [0.25, -0.5, 1e-7], rates: [decimal('1.5'), decimal('-0.125')] });
    expect(updated.titleEmbedding).toEqual([0.25, -0.5, 1e-7]);
    expect(updated.rates).toEqual(['1.5', '-0.125']);

    const query = [1, 0.1, 0];
    // By cosine, far east points nearer the query's way than east does,
    // however far it is.
    const cosine = await orm.passage.nearest('embedding', query, { k: 3 });
    expect(titles(cosine)).toEqual(['far east', 'east', 'north-east']);
    expect(cosine.map(n => n.distance)).toEqual([...cosine.map(n => n.distance)].sort((a, b) => a - b));
    const l2 = await orm.passage.nearest('embedding', query, { k: 2, metric: 'l2' });
    expect(titles(l2)).toEqual(['east', 'north-east']);
    expect(l2[0]!.distance).toBeCloseTo(0.1, 6);
    const ip = await orm.passage.nearest('embedding', query, { k: 1, metric: 'ip' });
    expect(titles(ip)).toEqual(['far east']);
    expect(ip[0]!.distance).toBeLessThan(-10);
    expect(titles(await orm.passage.nearest('embedding', query, { filter: { topic: { eq: 'sky' } } }))).toEqual(['up']);
    await orm.passage.deleteOne(ids.east!);
    expect(titles(await orm.passage.nearest('embedding', query, { k: 1, metric: 'l2' }))).toEqual(['north-east']);
    expect(titles(await orm.passage.nearest('embedding', query, { k: 1, metric: 'l2', includeDeleted: true }))).toEqual(['east']);
    // A row whose vector is null has no distance and is left out.
    expect(titles(await orm.passage.nearest('titleEmbedding', [0.25, -0.5, 0]))).toEqual(['up']);
    await expect(orm.passage.nearest('embedding', [1, 2])).rejects.toThrow('3 dimensions, not 2');

    const wide = Array.from({ length: 1024 }, (_, i) => (i === 7 ? 1 : 0));
    const seven = await orm.textEmbedding.createOne({ text: 'seven', embedding: wide });
    expect((await orm.textEmbedding.getOne(seven.id)).embedding).toEqual(wide);
    expect((await orm.textEmbedding.nearest('embedding', wide, { k: 1 })).map(n => n.row.text)).toEqual(['seven']);
  });

  test('nearest runs the statement sqlutil writes: the filter, the live rows, the vector last, and the order of its distance', async () => {
    const recorded = recording(db.pool);
    const traced = createOrm(recorded.pool, alice);
    await traced.passage.nearest('titleEmbedding', [1, 0, 0], { k: 2, metric: 'l2', filter: { topic: { eq: 'sky' } } });
    expect(recorded.statements).toHaveLength(1);
    expect(recorded.statements[0]).toBe(
      'SELECT "passage"."id", "passage"."title", "passage"."topic", "passage"."embedding", "passage"."title_embedding", "passage"."rates", "passage"."deleted_at", ' +
        'passage.title_embedding <-> $2 AS _distance FROM passage WHERE passage.topic = $1 AND passage.deleted_at IS NULL AND passage.title_embedding IS NOT NULL ' +
        'ORDER BY passage.title_embedding <-> $2 LIMIT 2'
    );
  });
});
