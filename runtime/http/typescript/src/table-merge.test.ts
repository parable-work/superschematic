import { describe, expect, test } from 'bun:test';
import { HttpProblem, tableMerge, type RequestContext, type TableOperation } from './index';

/*
A merge's handler (D147), over a fake of the repositories: it reads both
live rows by their keys, the path's {id} and {into}, folds the one into
the other with the repository's mergeInto in the route's transaction, and
answers the view of the row that stays; a key that holds no live row is
404, and the repository's MergeError (a row into itself, or into a row
under it) is 409.
*/

const columns = [
  { name: 'id', column: 'id', required: true },
  { name: 'name', column: '"name"', required: true },
];

class MergeError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'MergeError';
  }
}

/** The fake repositories: a Label table of rows by key, whose calls it records. */
function fake(rows: Record<string, { id: string; name: string }>, refuse?: Error) {
  const calls: unknown[][] = [];
  const repository = {
    def: { columns },
    async findOne(filter: { id: { eq: string } }) {
      calls.push(['findOne', filter.id.eq]);
      return rows[filter.id.eq] ?? null;
    },
    async findPage() {
      throw new Error('a merge lists nothing');
    },
    async createOne() {
      throw new Error('a merge creates nothing');
    },
    async updateOne() {
      throw new Error('a merge updates nothing');
    },
    async deleteOne() {
      throw new Error('a merge deletes through mergeInto');
    },
    async mergeInto(input: { from: string; into: string }) {
      calls.push(['mergeInto', input.from, input.into]);
      if (refuse) throw refuse;
      rows[input.into] = { ...rows[input.into]!, name: `${rows[input.into]!.name} (merged)` };
      delete rows[input.from];
      return { repointed: {}, deleted: {} };
    },
  };
  const orm = {
    as: () => orm,
    withBehaviorCaller: () => orm,
    transaction: <T>(fn: (tx: Record<string, unknown>) => Promise<T>) => fn({ label: repository }),
  };
  return { orm, calls };
}

const ctx = { principal: { subject: 'sam', permissions: ['labels.write'] }, serviceCaller: null } as unknown as RequestContext;
const merge: TableOperation = { kind: 'merge', type: 'Label', repository: 'label', key: 'id', view: ['id', 'name'] };

describe('tableMerge', () => {
  test('folds the row of the key into the row of into and answers the view of the row that stays', async () => {
    const { orm, calls } = fake({ a: { id: 'a', name: 'defect' }, b: { id: 'b', name: 'bug' } });
    const view = await tableMerge(orm, merge, ctx, { path: { id: 'a', into: 'b' }, query: {} });
    expect(view).toEqual({ id: 'b', name: 'bug (merged)' });
    expect(calls).toEqual([['findOne', 'a'], ['findOne', 'b'], ['mergeInto', 'a', 'b'], ['findOne', 'b']]);
  });

  test('a key that holds no live row is 404, and nothing merges', async () => {
    const { orm, calls } = fake({ b: { id: 'b', name: 'bug' } });
    const refused = await tableMerge(orm, merge, ctx, { path: { id: 'gone', into: 'b' }, query: {} }).catch(error => error);
    expect(refused).toBeInstanceOf(HttpProblem);
    expect((refused as HttpProblem).status).toBe(404);
    expect(calls.some(call => call[0] === 'mergeInto')).toBe(false);
  });

  test("the repository's MergeError is 409", async () => {
    const { orm } = fake({ a: { id: 'a', name: 'x' }, b: { id: 'b', name: 'y' } }, new MergeError('Label b references a through parent'));
    const refused = await tableMerge(orm, merge, ctx, { path: { id: 'a', into: 'b' }, query: {} }).catch(error => error);
    expect(refused).toBeInstanceOf(HttpProblem);
    expect((refused as HttpProblem).status).toBe(409);
    expect((refused as HttpProblem).message).toContain('references a through parent');
  });
});
