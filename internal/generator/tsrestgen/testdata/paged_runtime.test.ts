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

listParts also takes a filter (FilterBy, D106): the route reads the
request's filters, filter[field]=value and filter[field][op]=value, after
the query parameters and before the page, and hands the implementation
args.filter, a member per field the request filters by with each value
read as its field's type. A field or an operator the filter does not
take, and a value its type refuses, are a 400 at filter[field] that never
reaches the implementation.
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
const { catalog, itemKey, items, partKey, parts, recording, received, sortedItems, sortedParts, suppliers, tags } = await import(
  `${apiDir}/paged_implementation.ts`
);

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

describe('a filtered list', () => {
  /** The parts as the wire carries them: a timestamp its text. */
  const wire = (rows: readonly unknown[]) => JSON.parse(JSON.stringify(rows));
  const named = (...names: string[]) => wire(parts.filter((row: { name: string }) => names.includes(row.name)));

  /** Every part a query's filters keep, in the order of their names, and the filter the implementation took. */
  async function kept(query: string) {
    const { seen } = await walk(`/api/parts?limit=5&${query}`);
    return { parts: seen, filter: received.at(-1)?.args.filter };
  }

  test('the operation table carries the filter: each field, its operators and the spec of one of its values', () => {
    const { fields } = operationSpecs.listParts.filter;
    expect(fields.map((field: { name: string }) => field.name)).toEqual([
      'id',
      'name',
      'kind',
      'quantity',
      'weight',
      'price',
      'discontinued',
      'madeAt',
      'retiredOn',
      'supplierId',
    ]);
    const byField = Object.fromEntries(fields.map((field: { name: string }) => [field.name, field]));
    expect(byField.kind).toEqual({
      name: 'kind',
      kind: 'enum',
      operators: ['eq', 'ne', 'in', 'notIn'],
      value: { name: 'filter[kind]', kind: 'enum', required: false, enumValues: ['bolt', 'nut', 'washer'] },
    });
    expect(byField.name.operators).toEqual(['eq', 'ne', 'in', 'notIn', 'contains']);
    expect(byField.quantity.operators).toEqual(['eq', 'ne', 'in', 'notIn', 'gt', 'gte', 'lt', 'lte']);
    expect(byField.retiredOn.operators).toEqual(['eq', 'ne', 'in', 'notIn', 'gt', 'gte', 'lt', 'lte', 'isNull']);
    expect(byField.supplierId).toEqual({
      name: 'supplierId',
      kind: 'uuid',
      operators: ['eq', 'ne', 'in', 'notIn', 'isNull'],
      value: { name: 'filter[supplierId]', kind: 'uuid', required: false },
    });
    expect(byField.madeAt.value).toEqual({ name: 'filter[madeAt]', kind: 'datetime', required: false });
    expect(fields.map((field: { kind: string }) => field.kind)).toEqual(['uuid', 'string', 'enum', 'int', 'float', 'decimal', 'bool', 'datetime', 'date', 'uuid']);
    // The filter is no query parameter of the operation, and no other operation has one.
    expect(operationSpecs.listParts.queryParams).toEqual([]);
    expect('filter' in operationSpecs.listItems).toBe(false);
  });

  test('a request with no filter hands the implementation {} and lists every part', async () => {
    const response = await get('/api/parts?limit=5');
    expect(response.status).toBe(200);
    expect(response.body.data.items).toEqual(wire(parts.slice(0, 5)));
    const { args } = received.at(-1)!;
    expect(args.filter).toEqual({});
    expect(Object.keys(args.filter)).toEqual([]);
    expect(args.page).toEqual({ limit: 5, sort: byName });
  });

  test('equality on a field of each kind, each value read as its type', async () => {
    // A UUID in any of its forms is read as its canonical text.
    expect(await kept(`filter[id]=${parts[2].id}`)).toEqual({ parts: named('Eye bolt'), filter: { id: { eq: parts[2].id } } });
    expect(await kept('filter[id]=00000000-0000-4000-8000-000000000103')).toEqual({
      parts: named('Eye bolt'),
      filter: { id: { eq: parts[2].id } },
    });
    expect(await kept('filter[name]=Hex+nut')).toEqual({ parts: named('Hex nut'), filter: { name: { eq: 'Hex nut' } } });
    expect(await kept('filter[kind]=bolt')).toEqual({ parts: named('Anchor bolt', 'Eye bolt'), filter: { kind: { eq: 'bolt' } } });
    expect(await kept('filter[quantity]=40')).toEqual({ parts: named('Cap nut', 'Flat washer'), filter: { quantity: { eq: 40 } } });
    expect(await kept('filter[weight]=0.5')).toEqual({ parts: named('Anchor bolt'), filter: { weight: { eq: 0.5 } } });
    // A decimal and a date are their canonical text.
    expect(await kept('filter[price]=0.30')).toEqual({ parts: named('Hex nut'), filter: { price: { eq: '0.3' } } });
    expect(await kept('filter[retiredOn]=2026-3-1')).toEqual({ parts: named('Eye bolt'), filter: { retiredOn: { eq: '2026-03-01' } } });
    expect(await kept('filter[discontinued]=true')).toEqual({
      parts: named('Eye bolt', 'Hex nut'),
      filter: { discontinued: { eq: true } },
    });
    expect(await kept(`filter[supplierId]=${suppliers[1]}`)).toEqual({
      parts: named('Flat washer', 'Hex nut'),
      filter: { supplierId: { eq: suppliers[1] } },
    });
    expect(await kept('filter[retiredOn]=2026-03-01')).toEqual({ parts: named('Eye bolt'), filter: { retiredOn: { eq: '2026-03-01' } } });

    // A timestamp is a Date, whatever offset the caller wrote it at.
    for (const text of ['2026-01-10T08:00:00Z', '2026-01-10T09:00:00%2B01:00']) {
      const made = await kept(`filter[madeAt]=${text}`);
      expect(made.parts).toEqual(named('Anchor bolt', 'Flat washer'));
      expect(made.filter.madeAt.eq).toBeInstanceOf(Date);
      expect(made.filter).toEqual({ madeAt: { eq: new Date('2026-01-10T08:00:00Z') } });
    }
    // The brackets may come percent-encoded, as a browser's form sends them.
    expect((await kept('filter%5Bkind%5D=washer')).parts).toEqual(named('Flat washer'));
    expect((await kept('filter[kind][eq]=washer')).filter).toEqual({ kind: { eq: 'washer' } });
  });

  test('in keeps the items whose field is one of its values, each trimmed', async () => {
    expect(await kept('filter[kind][in]=bolt,washer')).toEqual({
      parts: named('Anchor bolt', 'Eye bolt', 'Flat washer'),
      filter: { kind: { in: ['bolt', 'washer'] } },
    });
    expect(await kept('filter[quantity][in]=0,+3+,,12')).toEqual({
      parts: named('Anchor bolt', 'Eye bolt', 'Wing nut'),
      filter: { quantity: { in: [0, 3, 12] } },
    });
    expect(await kept(`filter[id][in]=${parts[0].id},00000000-0000-4000-8000-000000000106`)).toEqual({
      parts: named('Anchor bolt', 'Wing nut'),
      filter: { id: { in: [parts[0].id, parts[5].id] } },
    });
    expect((await kept('filter[discontinued][in]=true,false')).parts).toEqual(wire(parts));
  });

  test('ne and notIn keep the items eq and in leave out, an item whose field is null among them', async () => {
    expect(await kept('filter[kind][ne]=nut')).toEqual({
      parts: named('Anchor bolt', 'Eye bolt', 'Flat washer'),
      filter: { kind: { ne: 'nut' } },
    });
    expect(await kept('filter[quantity][notIn]=0,+3+,,12')).toEqual({
      parts: named('Cap nut', 'Flat washer', 'Hex nut'),
      filter: { quantity: { notIn: [0, 3, 12] } },
    });
    expect((await kept('filter[name][ne]=Hex+nut')).parts).toEqual(named('Anchor bolt', 'Cap nut', 'Eye bolt', 'Flat washer', 'Wing nut'));
    expect(await kept('filter[price][ne]=0.250')).toEqual({
      parts: named('Anchor bolt', 'Eye bolt', 'Flat washer', 'Hex nut', 'Wing nut'),
      filter: { price: { ne: '0.25' } },
    });
    expect((await kept('filter[discontinued][ne]=true')).parts).toEqual(named('Anchor bolt', 'Cap nut', 'Flat washer', 'Wing nut'));
    const made = await kept('filter[madeAt][ne]=2026-01-10T08:00:00Z');
    expect(made.parts).toEqual(named('Cap nut', 'Eye bolt', 'Hex nut', 'Wing nut'));
    expect(made.filter.madeAt.ne).toBeInstanceOf(Date);
    // A part with no retirement date is not retired on that day, and one
    // with no supplier is none of these suppliers'.
    expect(await kept('filter[retiredOn][ne]=2026-03-01')).toEqual({
      parts: named('Anchor bolt', 'Cap nut', 'Flat washer', 'Hex nut', 'Wing nut'),
      filter: { retiredOn: { ne: '2026-03-01' } },
    });
    expect((await kept(`filter[supplierId][notIn]=${suppliers[0]}`)).parts).toEqual(named('Eye bolt', 'Flat washer', 'Hex nut', 'Wing nut'));
    expect((await kept(`filter[supplierId][notIn]=${suppliers[0]},${suppliers[1]}`)).parts).toEqual(named('Eye bolt', 'Wing nut'));
    // isNull says otherwise when the caller wants only the parts that have one.
    expect(await kept(`filter[supplierId][ne]=${suppliers[0]}&filter[supplierId][isNull]=false`)).toEqual({
      parts: named('Flat washer', 'Hex nut'),
      filter: { supplierId: { ne: suppliers[0], isNull: false } },
    });
    // They hold beside eq and in, as any member does.
    expect((await kept('filter[kind][in]=bolt,nut&filter[kind][ne]=nut&filter[quantity][notIn]=12')).parts).toEqual(named('Eye bolt'));
  });

  test('a range on a number, a decimal, a timestamp and a date', async () => {
    expect(await kept('filter[quantity][gte]=7&filter[quantity][lt]=40')).toEqual({
      parts: named('Anchor bolt', 'Hex nut'),
      filter: { quantity: { gte: 7, lt: 40 } },
    });
    expect((await kept('filter[quantity][gt]=12&filter[quantity][lte]=40')).parts).toEqual(named('Cap nut', 'Flat washer'));
    expect((await kept('filter[weight][lt]=0.03')).parts).toEqual(named('Cap nut', 'Flat washer'));
    expect(await kept('filter[price][lte]=0.30')).toEqual({
      parts: named('Cap nut', 'Flat washer', 'Hex nut'),
      filter: { price: { lte: '0.3' } },
    });
    const made = await kept('filter[madeAt][gte]=2026-01-01T00:00:00Z&filter[madeAt][lt]=2026-02-01T00:00:00Z');
    expect(made.parts).toEqual(named('Anchor bolt', 'Flat washer'));
    expect(made.filter).toEqual({ madeAt: { gte: new Date('2026-01-01T00:00:00Z'), lt: new Date('2026-02-01T00:00:00Z') } });
    // A null field passes no comparison.
    expect((await kept('filter[retiredOn][gt]=2025-12-31')).parts).toEqual(named('Eye bolt'));
    expect((await kept('filter[retiredOn][lte]=2026-03-01')).parts).toEqual(named('Eye bolt', 'Hex nut'));
  });

  test('contains keeps the items whose text holds the value, in any case', async () => {
    expect(await kept('filter[name][contains]=NUT')).toEqual({
      parts: named('Cap nut', 'Hex nut', 'Wing nut'),
      filter: { name: { contains: 'NUT' } },
    });
    expect((await kept('filter[name][contains]=+b')).parts).toEqual(named('Anchor bolt', 'Eye bolt'));
    expect((await kept('filter[name][contains]=nut&filter[name][in]=Hex+nut,Eye+bolt')).parts).toEqual(named('Hex nut'));
  });

  test('isNull keeps the items whose field is null, or the others', async () => {
    expect(await kept('filter[retiredOn][isNull]=true')).toEqual({
      parts: named('Anchor bolt', 'Cap nut', 'Flat washer', 'Wing nut'),
      filter: { retiredOn: { isNull: true } },
    });
    expect(await kept('filter[retiredOn][isNull]=false')).toEqual({
      parts: named('Eye bolt', 'Hex nut'),
      filter: { retiredOn: { isNull: false } },
    });
    expect((await kept('filter[supplierId][isNull]=true')).parts).toEqual(named('Eye bolt', 'Wing nut'));
    expect(await kept(`filter[supplierId][isNull]=false&filter[supplierId][in]=${suppliers[0]}`)).toEqual({
      parts: named('Anchor bolt', 'Cap nut'),
      filter: { supplierId: { in: [suppliers[0]], isNull: false } },
    });
  });

  test('an item is kept when it matches every member, in the filter’s field order whatever the query’s', async () => {
    const both = await kept('filter[discontinued]=false&filter[quantity][gt]=0&filter[kind]=nut');
    expect(both.parts).toEqual(named('Cap nut'));
    expect(both.filter).toEqual({ kind: { eq: 'nut' }, quantity: { gt: 0 }, discontinued: { eq: false } });
    expect(Object.keys(both.filter)).toEqual(['kind', 'quantity', 'discontinued']);
    expect((await kept('filter[kind]=washer&filter[discontinued]=true')).parts).toEqual([]);
  });

  test('with a sort and a cursor: every page is asked with the same filter, and each next is bound to the sort', async () => {
    const descending = parseSort('-quantity')!;
    const nuts = sortedParts(descending).filter((row: { kind: string }) => row.kind !== 'bolt');
    expect(nuts.map((row: { name: string }) => row.name)).toEqual(['Flat washer', 'Cap nut', 'Hex nut', 'Wing nut']);
    const before = received.length;
    const path = '/api/parts?sort=-quantity&limit=3&filter[kind][in]=nut,washer';
    const first = await get(path);
    expect(first.body.data).toEqual({
      items: wire(nuts.slice(0, 3)),
      next: bindCursor(encodeCursor(partKey(nuts[2], descending)), descending),
    });
    const last = await get(`${path}&cursor=${first.body.data.next}`);
    expect(last.body.data).toEqual({ items: wire(nuts.slice(3)), next: null });
    expect(received.slice(before).map(({ operation, args }: { operation: string; args: unknown }) => ({ operation, args }))).toEqual([
      { operation: 'listParts', args: { filter: { kind: { in: ['nut', 'washer'] } }, page: { limit: 3, sort: descending } } },
      {
        operation: 'listParts',
        args: {
          filter: { kind: { in: ['nut', 'washer'] } },
          page: { cursor: encodeCursor(partKey(nuts[2], descending)), limit: 3, sort: descending },
        },
      },
    ]);
    // A cursor carries no filter: the next page under another filter is that filter's.
    const other = await get(`/api/parts?sort=-quantity&limit=3&filter[kind]=bolt&cursor=${first.body.data.next}`);
    expect(other.body.data).toEqual({ items: named('Eye bolt'), next: null });
    // The default sort, two to a page.
    const { seen, sizes } = await walk('/api/parts?filter[discontinued]=false');
    expect(seen).toEqual(named('Anchor bolt', 'Cap nut', 'Flat washer', 'Wing nut'));
    expect(sizes).toEqual([2, 2]);
  });
});

describe('a refused filter', () => {
  const fieldNames = 'id, name, kind, quantity, weight, price, discontinued, madeAt, retiredOn, supplierId';

  /** The 400 of a filter parameter, as filter_parity.json's vectors hold it. */
  const refusal = (parameter: string, reason: string, errors?: unknown): Problem => ({
    status: 400,
    code: 'bad_request',
    detail: `Invalid query parameter ${parameter}: ${reason}`,
    details: { location: 'query', parameter, reason, ...(errors === undefined ? {} : { errors }) },
  });

  test('a field the filter does not name', async () => {
    const reason = `is not a field this operation filters by; expected one of ${fieldNames}`;
    expectProblem(await refused('/api/parts?filter[sku]=x'), refusal('filter[sku]', reason));
    expectProblem(await refused('/api/parts?filter[Kind]=nut'), refusal('filter[Kind]', reason));
    // An operation without a filter leaves filter keys alone, as any key it does not declare.
    expect((await get('/api/items?filter[sku]=x')).status).toBe(200);
  });

  test('an operator the field does not take', async () => {
    expectProblem(await refused('/api/parts?filter[kind][gt]=nut'), refusal('filter[kind]', 'takes no operator gt; expected one of eq, ne, in, notIn'));
    expectProblem(
      await refused('/api/parts?filter[quantity][contains]=4'),
      refusal('filter[quantity]', 'takes no operator contains; expected one of eq, ne, in, notIn, gt, gte, lt, lte')
    );
    expectProblem(
      await refused('/api/parts?filter[name][isNull]=true'),
      refusal('filter[name]', 'takes no operator isNull; expected one of eq, ne, in, notIn, contains')
    );
    expectProblem(await refused('/api/parts?filter[kind][neq]=nut'), refusal('filter[kind]', 'takes no operator neq; expected one of eq, ne, in, notIn'));
  });

  test('a key that is no filter, an operator sent twice, and an operator’s own value', async () => {
    const malformed = 'expected filter[field] or filter[field][operator]';
    expectProblem(await refused('/api/parts?filter=nut'), refusal('filter', malformed));
    expectProblem(await refused('/api/parts?filter[kind][in][0]=nut'), refusal('filter[kind][in][0]', malformed));
    expectProblem(await refused('/api/parts?filter[kind]=nut&filter[kind][eq]=bolt'), refusal('filter[kind]', 'holds eq more than once'));
    expectProblem(await refused('/api/parts?filter[kind][in]=,'), refusal('filter[kind]', 'in names no value'));
    expectProblem(await refused('/api/parts?filter[kind][notIn]=,'), refusal('filter[kind]', 'notIn names no value'));
    expectProblem(await refused('/api/parts?filter[name][contains]='), refusal('filter[name]', 'contains needs text'));
    expectProblem(await refused('/api/parts?filter[retiredOn][isNull]=1'), refusal('filter[retiredOn]', 'isNull expects true or false'));
    const many = Array.from({ length: 51 }, (_, i) => `filter[name]=n${i}`).join('&');
    expectProblem(
      await refused(`/api/parts?${many}`),
      refusal('filter', 'must hold at most 50 filters', [{ validator: 'max', message: 'must hold at most 50 filters' }])
    );
  });

  test('a value its field’s type refuses, at filter[field]', async () => {
    const type = (message: string) => [{ validator: 'type', message }];
    expectProblem(await refused('/api/parts?filter[quantity]=many'), refusal('filter[quantity]', 'expected an integer', type('expected an integer')));
    expectProblem(await refused('/api/parts?filter[quantity][gte]=1.5'), refusal('filter[quantity]', 'expected an integer', type('expected an integer')));
    expectProblem(await refused('/api/parts?filter[quantity][in]=1,two'), refusal('filter[quantity]', 'expected an integer', type('expected an integer')));
    expectProblem(await refused('/api/parts?filter[weight][lt]=heavy'), refusal('filter[weight]', 'expected a number', type('expected a number')));
    expectProblem(await refused('/api/parts?filter[kind]=gear'), refusal('filter[kind]', 'expected one of bolt, nut, washer'));
    expectProblem(await refused('/api/parts?filter[kind][in]=nut,gear'), refusal('filter[kind]', 'expected one of bolt, nut, washer'));
    expectProblem(
      await refused('/api/parts?filter[discontinued]=maybe'),
      refusal('filter[discontinued]', 'expected a boolean', type('expected a boolean'))
    );
    // A UUID, a timestamp, a date and a decimal carry the scalar library's
    // own errors: a date and a decimal are checked as one, which a query
    // parameter of their types is not.
    for (const [path, parameter, reason] of [
      ['/api/parts?filter[retiredOn]=yesterday', 'filter[retiredOn]', 'expected a date'],
      ['/api/parts?filter[retiredOn][gte]=2026-02-30', 'filter[retiredOn]', 'expected a date'],
      ['/api/parts?filter[price]=cheap', 'filter[price]', 'expected a decimal number'],
      ['/api/parts?filter[price][in]=1.5,1e3', 'filter[price]', 'expected a decimal number'],
      ['/api/parts?filter[id]=not-a-uuid', 'filter[id]', 'expected a UUID'],
      ['/api/parts?filter[supplierId][in]=not-a-uuid', 'filter[supplierId]', 'expected a UUID'],
      ['/api/parts?filter[madeAt]=yesterday', 'filter[madeAt]', 'expected an RFC 3339 timestamp'],
      ['/api/parts?filter[madeAt][gte]=2026-01-10T08:00', 'filter[madeAt]', 'expected an RFC 3339 timestamp'],
    ] as const) {
      const body = await refused(path);
      expect(body.detail).toBe(`Invalid query parameter ${parameter}: ${reason}`);
      expect(body.details).toMatchObject({ location: 'query', parameter, reason });
      expect(body.details.errors.length).toBeGreaterThan(0);
    }
    // The scalar library reads a UUID's base62 text too, so a word of
    // letters and digits is a UUID, of no part.
    const word = await get('/api/parts?filter[id]=nope');
    expect(word.status).toBe(200);
    expect(word.body.data).toEqual({ items: [], next: null });
  });

  test('a request with several faults is refused for its filter’s keys, then its filter’s values, then its page', async () => {
    // The order every server refuses in: the operation's own query
    // parameters (listParts declares none), the filter reader's 400, a
    // filter value's, then the page's.
    for (const [query, parameter] of [
      ['filter[sku]=x&limit=0', 'filter[sku]'],
      ['limit=0&filter[sku]=x', 'filter[sku]'],
      ['filter[madeAt]=yesterday&limit=0', 'filter[madeAt]'],
      ['filter[quantity]=many&sort=price', 'filter[quantity]'],
      ['cursor=!&filter[kind]=gear', 'filter[kind]'],
      // The reader's refusal of one field before a value's of another.
      ['filter[madeAt]=yesterday&filter[sku]=x&limit=0', 'filter[sku]'],
      ['filter[id]=not-a-uuid&filter[retiredOn][isNull]=maybe', 'filter[retiredOn]'],
      // A filter that reads leaves the page's refusal.
      ['filter[kind]=nut&limit=0', 'limit'],
      ['filter[kind]=nut&sort=price', 'sort'],
    ] as const) {
      expect((await refused(`/api/parts?${query}`)).details).toMatchObject({ location: 'query', parameter });
    }
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
