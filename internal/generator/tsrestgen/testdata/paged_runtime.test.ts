/*
Runtime check of the generated router for fixture-paged-api (D60), run by
TestGeneratedPagedRouter with API_DIR set to the materialized package and
PAGE_PARITY to runtime/http/testdata/page_parity.json. The implementation
is paged_implementation.ts, staged beside the router and type-checked with
it. A paged route reads cursor and limit after the operation's own query
parameters, hands the implementation args.page, and sends the Page it
returns as data, {items, next}, next null after the last page. listItems
also reads its sort (D60, amended) into args.page.sort, unbinds the cursor
from the sort it was returned under, and binds the next it answers to the
sort. A limit, sort or cursor the reader refuses is the 400 the vectors
hold and never reaches the implementation, and a cursor the implementation
cannot read is the vectors' refused-cursor 400.
*/
import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { Hono } from 'hono';
import { bindCursor, encodeCursor, parseSort, type PageSort } from '@superschematic/http-runtime';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';

const apiDir = process.env.API_DIR;
if (!apiDir) throw new Error('API_DIR is required');
const parityPath = process.env.PAGE_PARITY;
if (!parityPath) throw new Error('PAGE_PARITY is required');
const { buildRouter, operationSpecs } = await import(`${apiDir}/router.ts`);
const { catalog, itemKey, items, recording, received, sortedItems, tags } = await import(`${apiDir}/paged_implementation.ts`);

interface Problem {
  readonly status: number;
  readonly code: string;
  readonly detail: string;
  readonly details: unknown;
}

const vectors = JSON.parse(readFileSync(parityPath, 'utf8')) as {
  reads: {
    name: string;
    spec: { defaultLimit: number; maxLimit: number; sort?: { param: string; fields: string[]; default: string } };
    query: string;
    request?: { cursor: string | null; limit: number; sort?: string };
    problem?: Problem;
  }[];
  refused: { name: string; cursor: string; problem: Problem }[];
};

const byName: PageSort = { field: 'name', desc: false };

/** The cursor the route answers for an item under a sort: its key's, bound to the sort. */
function nextOf(row: unknown, sort: PageSort = byName): string {
  return bindCursor(encodeCursor(itemKey(row, sort)), sort);
}

/** A cursor bound to sort as the router binds a next, whatever its key; one that is no canonical base64url stays as it is. */
function boundTo(cursor: string, sort: string): string {
  const key = Buffer.from(cursor, 'base64url');
  if (cursor === '' || key.toString('base64url') !== cursor) return cursor;
  return Buffer.concat([Buffer.from(`{"sort":"${sort}","key":`), key, Buffer.from('}')]).toString('base64url');
}

function app(implementation: unknown) {
  const root = new Hono();
  root.route('/', buildRouter({ catalog: implementation }));
  root.notFound(notFoundHandler());
  root.onError(errorHandler());
  return root;
}

const served = app(catalog);
const recorded = app(recording);

async function get(path: string, through = served) {
  const response = await through.request(path);
  return { status: response.status, body: (await response.json()) as any };
}

/** The 400 a request gets, which never reached the implementation. */
async function refused(path: string, through = served) {
  const before = received.length;
  const response = await get(path, through);
  expect(response.status).toBe(400);
  expect(received.length).toBe(before);
  return response.body;
}

function expectProblem(body: any, problem: Problem) {
  expect({ status: body.status, code: body.code, detail: body.detail, details: body.details }).toEqual(problem);
}

function readVector(name: string) {
  const vector = vectors.reads.find(read => read.name === name);
  if (!vector) throw new Error(`no reader vector ${name}`);
  return vector;
}

/** Every item a paged list holds, following each next from path to the last page, and the size of each page. */
async function walk(path: string) {
  const seen: unknown[] = [];
  const sizes: number[] = [];
  let next: string | null = null;
  do {
    const separator = path.includes('?') ? '&' : '?';
    const response = await get(next === null ? path : `${path}${separator}cursor=${next}`);
    expect(response.status).toBe(200);
    expect(Object.keys(response.body.data).sort()).toEqual(['items', 'next']);
    seen.push(...response.body.data.items);
    sizes.push(response.body.data.items.length);
    next = response.body.data.next;
  } while (next !== null && sizes.length < 10);
  return { seen, sizes };
}

describe('the operation table', () => {
  test('carries each paged operation its limits, and a sorted one its sort', () => {
    expect(operationSpecs.listItems.page).toEqual({
      defaultLimit: 2,
      maxLimit: 5,
      sort: { param: 'sort', fields: ['name', 'sku'], default: 'name' },
    });
    expect(operationSpecs.listTags.page).toEqual({ defaultLimit: 20, maxLimit: 100 });
    expect('page' in operationSpecs.getItem).toBe(false);
    expect('page' in operationSpecs.placeBasket).toBe(false);
    // cursor, limit and sort are the page's, not query parameters of the operation.
    expect(operationSpecs.listItems.queryParams.map((param: { name: string }) => param.name)).toEqual(['ids', 'inStock']);
  });
});

describe('a paged list', () => {
  test('answers the first page in data, with the next cursor', async () => {
    const response = await get('/api/items');
    expect(response.status).toBe(200);
    expect(response.body.data).toEqual({
      items: items.slice(0, 2),
      next: nextOf(items[1]),
    });
    expect(Object.keys(response.body.meta)).toEqual(['requestId']);
    expect(received.at(-1)).toEqual({ operation: 'listItems', args: { page: { limit: 2, sort: byName } } });
  });

  test('follows each next to the last page, whose next is null', async () => {
    const { seen, sizes } = await walk('/api/items');
    expect(seen).toEqual([...items]);
    expect(sizes).toEqual([2, 2, 1]);
    const last = await get(`/api/items?cursor=${nextOf(items[3])}`);
    expect(last.body.data).toEqual({ items: [items[4]], next: null });
  });

  test('hands the implementation the cursor, unbound, and the limit asked for', async () => {
    const own = encodeCursor(itemKey(items[0], byName));
    const response = await get(`/api/items?limit=3&cursor=${nextOf(items[0])}`);
    expect(response.status).toBe(200);
    expect(response.body.data.items).toEqual(items.slice(1, 4));
    expect(received.at(-1)?.args.page).toEqual({ cursor: own, limit: 3, sort: byName });
  });

  test('sorts by each field either way, ties broken by id, and binds each next to the sort', async () => {
    const names = (rows: readonly { name: string }[]) => rows.map(row => row.name);
    for (const [text, want] of [
      ['name', ['Anvil', 'Bucket', 'Chisel', 'Drill', 'Easel']],
      ['-name', ['Easel', 'Drill', 'Chisel', 'Bucket', 'Anvil']],
      ['sku', ['Bucket', 'Anvil', 'Drill', 'Easel', 'Chisel']],
      ['-sku', ['Chisel', 'Easel', 'Drill', 'Anvil', 'Bucket']],
    ] as const) {
      const sort = parseSort(text)!;
      expect(names(sortedItems(sort))).toEqual([...want]);
      const before = received.length;
      const { seen, sizes } = await walk(`/api/items?sort=${text}`);
      expect(names(seen as { name: string }[])).toEqual([...want]);
      expect(sizes).toEqual([2, 2, 1]);
      for (const { args } of received.slice(before)) expect(args.page.sort).toEqual(sort);
    }
    const first = await get('/api/items?sort=-sku');
    const descending = parseSort('-sku')!;
    expect(first.body.data.next).toBe(nextOf(sortedItems(descending)[1], descending));
    expect(Buffer.from(first.body.data.next, 'base64url').toString()).toStartWith('{"sort":"-sku","key":{"sku":"sku-4","id":');
  });

  test('a limit up to the max', async () => {
    const all = await get('/api/items?limit=5');
    expect(all.body.data).toEqual({ items: [...items], next: null });
    const { sizes } = await walk('/api/items?limit=4');
    expect(sizes).toEqual([4, 1]);
  });

  test('the same filters apply on every page', async () => {
    const { seen, sizes } = await walk('/api/items?inStock=true&limit=1');
    expect(seen).toEqual(items.filter((row: { inStock: boolean }) => row.inStock));
    expect(sizes).toEqual([1, 1, 1]);
    const ids = [items[4].id, items[0].id, items[2].id].join(',');
    expect((await walk(`/api/items?ids=${ids}`)).seen).toEqual([items[0], items[2], items[4]]);
  });

  test('pages a primitive', async () => {
    const first = await get('/api/tags?limit=2');
    expect(first.body.data).toEqual({ items: tags.slice(0, 2), next: encodeCursor(tags[1]) });
    const last = await get(`/api/tags?limit=2&cursor=${first.body.data.next}`);
    expect(last.body.data).toEqual({ items: tags.slice(2), next: null });
    expect((await get('/api/tags')).body.data).toEqual({ items: [...tags], next: null });
  });

  test('an operation that does not page answers its result as before', async () => {
    const response = await get(`/api/items/${items[2].id}`);
    expect(response.status).toBe(200);
    expect(response.body.data).toEqual(items[2]);
  });
});

describe('a refused page', () => {
  test('a limit past the max is the reader vector, before the implementation', async () => {
    expectProblem(await refused('/api/items?limit=6'), readVector('a limit past a small max').problem!);
    expectProblem(await refused('/api/tags?limit=101'), readVector('a limit past the max').problem!);
  });

  test('a sort not offered, and a cursor of another sort, are the reader vectors', async () => {
    expectProblem(await refused('/api/items?sort=price'), readVector("a list's sort not offered").problem!);
    const next = nextOf(items[1]);
    const sorted = readVector("a list's cursor of another sort").problem!;
    expectProblem(await refused(`/api/items?sort=-name&cursor=${next}`), sorted);
    expectProblem(await refused(`/api/items?sort=sku&cursor=${next}`), sorted);
    // The implementation's own cursor names no sort.
    const own = encodeCursor(itemKey(items[1], byName));
    expectProblem(await refused(`/api/items?cursor=${own}`), readVector("a list's key cursor no sort is bound to").problem!);
  });

  test("the operation's own query parameters are read first", async () => {
    const body = await refused('/api/items?inStock=maybe&limit=0');
    expect(body.details).toMatchObject({ location: 'query', parameter: 'inStock' });
  });

  test("a forged cursor is the vectors' refused cursor", async () => {
    // The refused cursors the router's pattern admits reach the
    // implementation, bound to the items' sort for theirs, whose
    // decodeCursor refuses them with the vector's 400; the others are the
    // reader's (above and below).
    const forged = vectors.refused.filter(vector => /^[A-Za-z0-9_-]{1,1024}$/.test(vector.cursor));
    expect(forged.map(vector => vector.name)).toContain('base64url of text that is not JSON');
    for (const vector of forged) {
      for (const path of [`/api/items?cursor=${boundTo(vector.cursor, 'name')}`, `/api/tags?cursor=${vector.cursor}`]) {
        const response = await get(path);
        expect(response.status).toBe(400);
        expectProblem(response.body, vector.problem);
      }
    }
    const problem = forged[0]!.problem;
    expect(problem.detail).toBe('cursor is not one this operation returned');
    // JSON that names no position of the order, and another list's cursor:
    // the implementation refuses each with refusedCursor().
    for (const path of [
      `/api/items?cursor=${bindCursor(encodeCursor({ position: 3 }), byName)}`,
      `/api/items?cursor=${bindCursor(encodeCursor('green'), byName)}`,
      `/api/tags?cursor=${encodeCursor({ id: items[0].id })}`,
    ]) {
      const response = await get(path);
      expect(response.status).toBe(400);
      expectProblem(response.body, problem);
    }
  });
});

describe('every reader vector over HTTP', () => {
  // Each vector runs on the route whose limits and sort are its spec's.
  const routes = [
    { path: '/api/items', operation: 'listItems', spec: operationSpecs.listItems.page },
    { path: '/api/tags', operation: 'listTags', spec: operationSpecs.listTags.page },
  ];
  const runs = vectors.reads.flatMap(vector => {
    const route = routes.find(r => Bun.deepEquals(r.spec, vector.spec));
    return route ? [{ vector, route }] : [];
  });

  test('both routes run vectors', () => {
    expect(new Set(runs.map(run => run.route.path))).toEqual(new Set(['/api/items', '/api/tags']));
  });

  for (const { vector, route } of runs) {
    test(`${vector.name} (${route.path})`, async () => {
      const path = vector.query === '' ? route.path : `${route.path}?${vector.query}`;
      if (vector.problem) {
        expectProblem(await refused(path, recorded), vector.problem);
        return;
      }
      const response = await get(path, recorded);
      expect(response.status).toBe(200);
      expect(response.body.data).toEqual({ items: [], next: null });
      const want = vector.request!;
      expect(received.at(-1)).toEqual({
        operation: route.operation,
        args: {
          page: {
            ...(want.cursor === null ? {} : { cursor: want.cursor }),
            limit: want.limit,
            ...(want.sort === undefined ? {} : { sort: parseSort(want.sort) }),
          },
        },
      });
    });
  }
});
