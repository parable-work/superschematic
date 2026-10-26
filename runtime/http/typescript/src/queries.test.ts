import { describe, expect, test } from 'bun:test';
import { PageKeyError } from './orm';
import { queriesOf, type QueryDefinition } from './queries';

// A declared query's runtime (D86) over a runner that records what it is
// sent and answers rows as pg would, with no database: each parameter
// through encodeParam, each row through decodeRow, an order's fragment
// from its options alone, and a page's key after the query's parameters.

const hyphenated = '0f8b6c4e-1b2a-4c3d-9e8f-123456789abc';
const base62 = 'TKbHsHDO6x9RCjbs5c2u4';

const definitions: Record<string, QueryDefinition> = {
  lines: {
    text: "SELECT id, amount, rate, embedding, meta, at FROM line WHERE id = ANY($1) AND name LIKE '%%' || $2 ORDER BY %s",
    params: [
      { name: 'ids', column: 'ids', list: true, uuid: true, required: true },
      { name: 'prefix', column: 'prefix' },
    ],
    row: [
      { name: 'id', column: 'id', uuid: true, required: true },
      { name: 'amount', column: 'amount', int64: true, required: true },
      { name: 'rate', column: 'rate', numeric: true },
      { name: 'embedding', column: 'embedding', vector: true },
      { name: 'meta', column: 'meta', json: true },
      { name: 'at', column: '"at"', timestamp: true, required: true },
    ],
    orderBy: { param: 'order', options: { amount: 'amount DESC', id: 'id' }, default: 'id' },
  },
  near: {
    text: 'SELECT id FROM line ORDER BY embedding <=> $1 LIMIT 1',
    params: [{ name: 'vector', column: 'vector', vector: true, required: true }],
    row: [{ name: 'id', column: 'id', uuid: true, required: true }],
  },
  page: {
    text: 'SELECT id, amount FROM line',
    params: [],
    row: [
      { name: 'id', column: 'id', uuid: true, required: true },
      { name: 'amount', column: 'amount', int64: true, required: true },
    ],
    page: {
      first: 'SELECT * FROM (SELECT id, amount FROM line\n) AS q ORDER BY q.amount ASC, q.id ASC LIMIT %d',
      after: 'SELECT * FROM (SELECT id, amount FROM line\n) AS q WHERE (q.amount, q.id) > ($1, $2) ORDER BY q.amount ASC, q.id ASC LIMIT %d',
      key: ['amount', 'id'],
    },
  },
};

/** A runner that records each statement and answers rows from answer. */
function recorder(answer: (text: string) => Record<string, unknown>[]) {
  const sent: { text: string; values: unknown[] }[] = [];
  const run = async (text: string, values: unknown[]) => {
    sent.push({ text, values });
    return { rows: answer(text) };
  };
  return { sent, queries: queriesOf(definitions, run, 2) };
}

describe('declared queries', () => {
  test('each parameter binds through the codec, the order fills its fragment, and each row decodes', async () => {
    const { sent, queries } = recorder(() => [
      { id: hyphenated, amount: '12', rate: '1.50', embedding: '[0.5,-1]', meta: { a: 1 }, at: '2026-01-02T03:04:05Z' },
    ]);
    const rows = await queries.lines!({ ids: [base62], prefix: null, order: 'amount' });
    expect(sent).toEqual([
      { text: "SELECT id, amount, rate, embedding, meta, at FROM line WHERE id = ANY($1) AND name LIKE '%' || $2 ORDER BY amount DESC", values: [[hyphenated], null] },
    ]);
    expect(rows).toEqual([{ id: base62, amount: 12, rate: '1.5', embedding: [0.5, -1], meta: { a: 1 }, at: new Date('2026-01-02T03:04:05Z') }]);
    await queries.lines!({ ids: [], prefix: 'a', order: null });
    expect(sent[1]!.text.endsWith('ORDER BY id')).toBe(true);
  });

  test('an order no option names is refused before a statement is sent', async () => {
    const { sent, queries } = recorder(() => []);
    await expect(queries.lines!({ ids: [], prefix: null, order: 'amount; DROP TABLE line' })).rejects.toThrow(
      'orm: query lines orders by amount, id, not "amount; DROP TABLE line"'
    );
    expect(sent).toEqual([]);
  });

  test('a vector binds as its text and a bad value names the parameter', async () => {
    const { sent, queries } = recorder(() => [{ id: hyphenated }]);
    await queries.near!({ vector: [1, 0.5] });
    expect(sent[0]!.values).toEqual(['[1,0.5]']);
    await expect(queries.near!({ vector: 'x' })).rejects.toThrow('orm: query near: parameter vector:');
  });

  test('a page reads a row more than it holds, and the key of its last row binds after the parameters', async () => {
    const all = [
      { id: hyphenated, amount: '1' },
      { id: '00000000-0000-0000-0000-000000000002', amount: '2' },
      { id: '00000000-0000-0000-0000-000000000003', amount: '3' },
    ];
    const { sent, queries } = recorder(text => (text.includes('WHERE') ? all.slice(2) : all));
    const first = (await queries.page!({}, {})) as { rows: { id: string; amount: number }[]; next: Record<string, unknown> | null };
    expect(sent[0]!.text.endsWith('LIMIT 3')).toBe(true);
    expect(first.rows.map(r => r.amount)).toEqual([1, 2]);
    expect(first.next).toEqual({ amount: 2, id: first.rows[1]!.id });
    const last = (await queries.page!({}, { after: first.next, limit: 2 })) as { rows: unknown[]; next: unknown };
    expect(sent[1]!.values).toEqual([2, '00000000-0000-0000-0000-000000000002']);
    expect(last.next).toBeNull();
    await expect(queries.page!({}, { after: { amount: 2 } })).rejects.toBeInstanceOf(PageKeyError);
    await expect(queries.page!({}, { limit: -1 })).rejects.toThrow('orm: a page of query page holds 1 or more rows, not -1');
  });
});
