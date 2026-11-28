import { describe, expect, test } from 'bun:test';
import { resolveAudience } from './audience-resolve.js';

/** An ORM of one table, Page, whose statement answers batches of keys from rows, as the generated resolve does. */
function fakeOrm(rows: readonly string[], resolve: string | null = 'RESOLVE') {
  const calls: unknown[][] = [];
  const system = {
    async query(text: string, values: unknown[]) {
      expect(text).toBe('RESOLVE');
      calls.push(values);
      const [, after, batch] = values as [string, string | null, number];
      const next = rows.filter(key => after === null || key > after).slice(0, batch);
      return { rows: [{ resolved: String(next.length), last: next.length === 0 ? null : next[next.length - 1] }] };
    },
  };
  return {
    calls,
    orm: { page: { def: { name: 'Page', audience: { effective: 'page.effective_audience', agents: 'page.effective_agents', resolve: resolve ?? undefined } } }, asSystem: () => system },
  };
}

describe('resolveAudience', () => {
  test("resolves a parent's rows a batch at a time, each after the last key of the one before, until none is left", async () => {
    const keys = Array.from({ length: 7 }, (_, i) => `k${i}`);
    const { orm, calls } = fakeOrm(keys);
    expect(await resolveAudience(orm, { childTable: 'Page', parentKey: 'p1' }, { batch: 3 })).toBe(7);
    expect(calls).toEqual([
      ['p1', null, 3],
      ['p1', 'k2', 3],
      ['p1', 'k5', 3],
      ['p1', 'k6', 3],
    ]);
  });

  test('refuses a table whose @audience names no queue, and a batch of no rows', async () => {
    const { orm } = fakeOrm([], null);
    await expect(resolveAudience(orm, { childTable: 'Page', parentKey: 'p1' })).rejects.toThrow('Page is no table of the ORM whose @audience names a queue');
    await expect(resolveAudience(orm, { childTable: 'Folder', parentKey: 'p1' })).rejects.toThrow('Folder is no table');
    await expect(resolveAudience(fakeOrm([]).orm, { childTable: 'Page', parentKey: 'p1' }, { batch: 0 })).rejects.toThrow('the batch is 0');
  });
});
