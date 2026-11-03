// The repositories of fixture-vectors-db on a Postgres with pgvector (D84):
// a signed amount and a decimal read back as the types hold them, a NUMERIC
// in its column's scale as the canonical text the wire holds, a decimal
// filter compares numbers, a vector crosses as its numbers, and nearest
// finds a row's neighbours by each metric, through a filter and around a
// soft-deleted row, with the statement sqlutil writes; and in a table whose
// names @table and @column give, beside a sealed note (D85). A filtered
// nearest that an hnsw index serves finds k rows with an iterative scan,
// where its first candidates hold none the filter keeps, and its settings
// hold for a transaction of its own (D89).
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { decodeRow, uuidToDb } from '@superschematic/http-runtime/postgres';
import { createOrm, ledgerLineTable, type FixtureVectorsDbOrm, type FixtureVectorsDbTx, type PassageRow } from './orm';
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

  test('a table @table and @column name, with a sealed note beside its vector: nearest orders by the renamed column and opens each note', async () => {
    const sealed = createOrm(db.pool, { user: alice, fieldKeys: 'AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA=' });
    const north = await sealed.chunk.createOne({ text: 'north', embedding: [0, 1, 0], note: 'private' });
    const east = await sealed.chunk.createOne({ text: 'east', embedding: [1, 0, 0] });
    const { rows } = await db.pool.query(`SELECT "chunkVector"::text AS vector, note FROM "NoteChunk" WHERE id = $1`, [uuidToDb(north.id)]);
    expect(rows[0].vector).toBe('[0,1,0]');
    expect(String(rows[0].note).startsWith('v1:')).toBe(true);
    const query = [1, 0.1, 0];
    const near = await sealed.chunk.nearest('embedding', query, { k: 2, metric: 'l2' });
    expect(near.map(n => n.row.text)).toEqual(['east', 'north']);
    expect([near[0]!.row.note, near[1]!.row.note?.reveal(), near[1]!.row.embedding]).toEqual([null, 'private', [0, 1, 0]]);
    expect((await sealed.chunk.nearest('embedding', query, { filter: { text: { in: ['north'] } } })).map(n => n.row.text)).toEqual(['north']);
    await sealed.chunk.deleteOne(east.id);
    const removed = await db.pool.query(`SELECT "removedAt" IS NOT NULL AS removed FROM "NoteChunk" WHERE id = $1`, [uuidToDb(east.id)]);
    expect(removed.rows[0].removed).toBe(true);
    expect((await sealed.chunk.nearest('embedding', query, { k: 1, metric: 'l2' })).map(n => n.row.text)).toEqual(['north']);
    expect((await sealed.chunk.nearest('embedding', query, { k: 1, metric: 'l2', includeDeleted: true })).map(n => n.row.text)).toEqual(['east']);
    // Without the keys, a row whose note is sealed does not open.
    await expect(createOrm(db.pool, alice).chunk.nearest('embedding', query, { k: 1, metric: 'l2' })).rejects.toThrow();
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

  test('a filtered nearest scans the hnsw index on, with iterativeScan, until it has k rows, where its first candidates hold none', async () => {
    // A schema of its own: 1990 common passages near the query and ten rare
    // ones opposite it, which ef_search's 40 candidates never reach.
    const scan = await database('ts_orm_vectors_scan');
    try {
      const recorded = recording(scan.pool);
      const vectors = createOrm(recorded.pool, alice);
      const passages = Array.from({ length: 2000 }, (_, i) => {
        const rare = i % 200 === 0;
        return { title: `p${i}`, topic: rare ? 'rare' : 'common', embedding: [rare ? -1 : 1, Math.sin(i) * 0.3, Math.cos(i) * 0.3] };
      });
      await vectors.passage.createMany(passages);
      const query = [1, 0, 0];
      const options = { k: 5, filter: { topic: { eq: 'rare' } } };
      // The index serves the order: no sequential scan and no sort.
      const indexed = <T>(fn: (tx: FixtureVectorsDbTx) => Promise<T>): Promise<T> =>
        vectors.transaction(async tx => {
          await tx.exec('SET LOCAL enable_seqscan = off');
          await tx.exec('SET LOCAL enable_sort = off');
          return fn(tx);
        });
      expect(await indexed(tx => tx.passage.nearest('embedding', query, options))).toHaveLength(0);
      const relaxed = await indexed(tx => tx.passage.nearest('embedding', query, { ...options, iterativeScan: 'relaxed_order' }));
      expect(relaxed).toHaveLength(5);
      expect(relaxed.every(n => n.row.topic === 'rare')).toBe(true);
      expect(await indexed(tx => tx.passage.nearest('embedding', query, { ...options, iterativeScan: 'strict_order', maxScanTuples: 5000 }))).toHaveLength(5);
      // Outside a transaction, nearest opens one for its settings.
      recorded.statements.length = 0;
      const own = await vectors.passage.nearest('embedding', query, { k: 2, efSearch: 100, iterativeScan: 'relaxed_order', excludeIds: [relaxed[0]!.row.id] });
      expect(own.map(n => n.row.id)).not.toContain(relaxed[0]!.row.id);
      expect(recorded.statements.slice(0, 2)).toEqual([
        'BEGIN',
        "SELECT set_config('hnsw.ef_search', $1, true), set_config('hnsw.iterative_scan', $2, true), set_config('ivfflat.iterative_scan', $3, true)",
      ]);
      expect(recorded.statements[2]).toContain('WHERE passage.id NOT IN ($1) AND passage.deleted_at IS NULL');
      expect(recorded.statements.at(-1)).toBe('COMMIT');
      await expect(vectors.passage.nearest('embedding', query, { efSearch: 0 })).rejects.toThrow(/efSearch is a whole number from 1 to 1000/);
      await expect(vectors.passage.nearest('embedding', query, { iterativeScan: 'sideways' as never })).rejects.toThrow(/iterativeScan is off, relaxed_order, strict_order/);
    } finally {
      await scan.drop();
    }
  });
});
