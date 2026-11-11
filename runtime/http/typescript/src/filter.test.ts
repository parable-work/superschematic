import { describe, expect, test } from 'bun:test';
import { Hono } from 'hono';
import { parseIdentityUUID } from 'superscalar/scalars';
import { errorHandler, mountOperation } from './hono';
import {
  FILTER_OPERATORS,
  HttpProblem,
  MAX_FILTERS,
  MAX_FILTER_IN_VALUES,
  problemBody,
  readFilter,
  readFilters,
  readPage,
  type EqualsFilter,
  type FilterSpec,
  type OperationSpec,
  type OrderedFilter,
  type TextFilter,
} from './index';

/*
The typed read of a list's filters (FilterBy, D106): readFilter decodes
each value the reader found as an item of a query list of its field's type
is, and refuses one its type refuses at filter[field]. The reader itself is
held to the shared vectors in filter-parity.test.ts.
*/

/** A UUID as a caller may write it, and its canonical text, which the scalar library answers. */
const HYPHENATED = '6f9619ff-8b86-4d01-b42d-00cf4fc964ff';
const UUID = parseIdentityUUID(HYPHENATED)!;

/** A field of every kind a filter compares, as a generated router's operation table carries them. */
const spec = {
  fields: [
    { name: 'id', operators: ['eq', 'in'], value: { name: 'filter[id]', kind: 'uuid', required: false } },
    {
      name: 'name',
      operators: ['eq', 'in', 'contains'],
      value: { name: 'filter[name]', kind: 'string', required: false, maxLength: 8 },
    },
    {
      name: 'kind',
      operators: ['eq', 'in'],
      value: { name: 'filter[kind]', kind: 'enum', required: false, enumValues: ['bolt', 'nut', 'washer'] },
    },
    {
      name: 'quantity',
      operators: ['eq', 'in', 'gt', 'gte', 'lt', 'lte'],
      value: { name: 'filter[quantity]', kind: 'integer', required: false },
    },
    {
      name: 'weight',
      operators: ['eq', 'in', 'gt', 'gte', 'lt', 'lte'],
      value: { name: 'filter[weight]', kind: 'number', required: false },
    },
    {
      name: 'price',
      kind: 'decimal',
      operators: ['eq', 'in', 'gt', 'gte', 'lt', 'lte'],
      value: {
        name: 'filter[price]',
        kind: 'string',
        required: false,
        scalar: { name: 'Generic.Decimal', pattern: '^-?\\d+(\\.\\d+)?$' },
      },
    },
    { name: 'discontinued', operators: ['eq', 'in'], value: { name: 'filter[discontinued]', kind: 'boolean', required: false } },
    {
      name: 'madeAt',
      operators: ['eq', 'in', 'gt', 'gte', 'lt', 'lte'],
      value: { name: 'filter[madeAt]', kind: 'datetime', required: false },
    },
    {
      name: 'retiredOn',
      kind: 'date',
      operators: ['eq', 'in', 'gt', 'gte', 'lt', 'lte', 'isNull'],
      value: { name: 'filter[retiredOn]', kind: 'string', required: false, scalar: { name: 'Temporal.Date', maxLength: 40 } },
    },
    {
      name: 'supplierId',
      operators: ['eq', 'in', 'isNull'],
      value: { name: 'filter[supplierId]', kind: 'uuid', required: false },
    },
  ],
} as const satisfies FilterSpec;

function read(query: string) {
  return readFilter(new URLSearchParams(query), spec);
}

/** The problem a route writes for a refused read. */
function problemOf(query: string) {
  try {
    read(query);
  } catch (err) {
    expect(err).toBeInstanceOf(HttpProblem);
    const body = problemBody(err as HttpProblem, 'req-1');
    return { status: body.status, code: body.code, detail: body.detail, details: body.details };
  }
  throw new Error(`expected ${query} refused`);
}

describe('readFilter', () => {
  test('a request with no filter reads as {}', () => {
    expect(read('')).toEqual({});
    expect(read('limit=5&sort=-name&filters[name]=x&filtered=1')).toEqual({});
  });

  test('a member per field the request filters by, in the spec’s order, and none for the others', () => {
    const filter = read('filter[quantity][gte]=2&filter[kind]=nut&filter[id]=' + HYPHENATED);
    expect(filter).toEqual({ id: { eq: UUID }, kind: { eq: 'nut' }, quantity: { gte: 2 } });
    expect(Object.keys(filter)).toEqual(['id', 'kind', 'quantity']);
    expect('name' in filter).toBe(false);
    // Each member holds the operators the request sent, and no other.
    expect(Object.keys(filter.quantity!)).toEqual(['gte']);
  });

  test('a UUID is its canonical text', () => {
    expect(UUID).not.toBe(HYPHENATED);
    expect(read(`filter[id]=${HYPHENATED}`)).toEqual({ id: { eq: UUID } });
    expect(read(`filter[id][in]=${HYPHENATED.toUpperCase()},${UUID}`)).toEqual({ id: { in: [UUID, UUID] } });
  });

  test('text is its text, and contains the text it holds', () => {
    expect(read('filter[name]=Hex+bolt')).toEqual({ name: { eq: 'Hex bolt' } });
    expect(read('filter[name]=')).toEqual({ name: { eq: '' } });
    expect(read('filter[name][in]=a,+b+,')).toEqual({ name: { in: ['a', 'b'] } });
    // contains is text, never the field's type: no rule of the field holds it.
    expect(read('filter[name][contains]=+a+much+longer+text,+kept+')).toEqual({
      name: { contains: ' a much longer text, kept ' },
    });
  });

  test('an enum is one of its values', () => {
    expect(read('filter[kind][in]=bolt,washer')).toEqual({ kind: { in: ['bolt', 'washer'] } });
  });

  test('a number is a number, with every ordered operator', () => {
    expect(read('filter[quantity]=3&filter[quantity][in]=3,4&filter[quantity][gt]=1&filter[quantity][gte]=2&filter[quantity][lt]=8&filter[quantity][lte]=9')).toEqual({
      quantity: { eq: 3, in: [3, 4], gt: 1, gte: 2, lt: 8, lte: 9 },
    });
    expect(Object.keys(read('filter[quantity][lte]=9&filter[quantity]=3').quantity!)).toEqual(['eq', 'lte']);
    expect(read('filter[weight][lt]=2.5&filter[weight][gte]=-1e2')).toEqual({ weight: { gte: -100, lt: 2.5 } });
  });

  test('a decimal and a date are their canonical text, read by the scalar library', () => {
    expect(read('filter[price][gte]=10.50&filter[price][in]=%2B1.5,-0')).toEqual({ price: { in: ['1.5', '0'], gte: '10.5' } });
    expect(read('filter[retiredOn]=2026-03-01&filter[retiredOn][lt]=2026-3-9')).toEqual({ retiredOn: { eq: '2026-03-01', lt: '2026-03-09' } });
  });

  test('a boolean is a boolean', () => {
    expect(read('filter[discontinued]=true')).toEqual({ discontinued: { eq: true } });
    expect(read('filter[discontinued][in]=false,1')).toEqual({ discontinued: { in: [false, true] } });
  });

  test('a timestamp is a Date', () => {
    const filter = read('filter[madeAt][gte]=2026-01-01T00:00:00Z&filter[madeAt][lt]=2026-02-01T00:00:00%2B01:00');
    const madeAt = filter.madeAt as OrderedFilter<Date>;
    expect(madeAt.gte).toEqual(new Date('2026-01-01T00:00:00Z'));
    expect(madeAt.lt).toEqual(new Date('2026-01-31T23:00:00Z'));
  });

  test('isNull is a boolean, beside the field’s other operators', () => {
    expect(read('filter[supplierId][isNull]=true')).toEqual({ supplierId: { isNull: true } });
    expect(read(`filter[supplierId][isNull]=false&filter[supplierId]=${UUID}`)).toEqual({
      supplierId: { eq: UUID, isNull: false },
    });
  });

  test('the shapes an implementation takes hold what the read answers', () => {
    const filter = read(`filter[id]=${UUID}&filter[quantity][gt]=1&filter[name][contains]=bolt`);
    const id: EqualsFilter<string> | undefined = filter.id as EqualsFilter<string> | undefined;
    const quantity: OrderedFilter<number> | undefined = filter.quantity as OrderedFilter<number> | undefined;
    const name: TextFilter<string> | undefined = filter.name as TextFilter<string> | undefined;
    expect([id?.eq, quantity?.gt, name?.contains]).toEqual([UUID, 1, 'bolt']);
  });

  test('a field named as a member of Object.prototype is an own member', () => {
    const odd: FilterSpec = {
      fields: [{ name: '__proto__', operators: ['eq'], value: { name: 'filter[__proto__]', kind: 'string', required: false } }],
    };
    const filter = readFilter(new URLSearchParams('filter[__proto__]=x'), odd);
    expect(Object.keys(filter)).toEqual(['__proto__']);
    expect(Object.getPrototypeOf(filter)).toBe(Object.prototype);
  });
});

describe('a value its type refuses', () => {
  const refusal = (field: string, reason: string, errors?: unknown) => ({
    status: 400,
    code: 'bad_request',
    detail: `Invalid query parameter filter[${field}]: ${reason}`,
    details: { location: 'query', parameter: `filter[${field}]`, reason, ...(errors === undefined ? {} : { errors }) },
  });
  const typeError = (message: string) => [{ validator: 'type', message }];

  test('is a 400 at filter[field], worded as a query list’s item is', () => {
    expect(problemOf('filter[quantity]=many')).toEqual(refusal('quantity', 'expected an integer', typeError('expected an integer')));
    expect(problemOf('filter[quantity][gte]=1.5')).toEqual(refusal('quantity', 'expected an integer', typeError('expected an integer')));
    expect(problemOf('filter[quantity]=')).toEqual(refusal('quantity', 'expected an integer', typeError('expected an integer')));
    expect(problemOf('filter[weight][lt]=heavy')).toEqual(refusal('weight', 'expected a number', typeError('expected a number')));
    expect(problemOf('filter[kind]=gear')).toEqual(refusal('kind', 'expected one of bolt, nut, washer'));
    expect(problemOf('filter[discontinued]=maybe')).toEqual(refusal('discontinued', 'expected a boolean', typeError('expected a boolean')));
    expect(problemOf('filter[name]=a+name+too+long')).toEqual(
      refusal('name', 'must be at most 8 characters', [{ validator: 'maxLength', message: 'must be at most 8 characters' }])
    );
  });

  test('a date and a decimal are checked as one, which a query parameter of their types is not', () => {
    for (const [query, field, reason] of [
      ['filter[retiredOn]=yesterday', 'retiredOn', 'expected a date'],
      ['filter[retiredOn][gte]=2026-02-30', 'retiredOn', 'expected a date'],
      ['filter[retiredOn][in]=2026-03-01,soon', 'retiredOn', 'expected a date'],
      ['filter[retiredOn]=', 'retiredOn', 'expected a date'],
      ['filter[price]=cheap', 'price', 'expected a decimal number'],
      ['filter[price][lt]=1e3', 'price', 'expected a decimal number'],
      ['filter[price]=', 'price', 'expected a decimal number'],
    ] as const) {
      const problem = problemOf(query);
      expect(problem).toMatchObject({
        status: 400,
        code: 'bad_request',
        detail: `Invalid query parameter filter[${field}]: ${reason}`,
        details: { location: 'query', parameter: `filter[${field}]`, reason },
      });
      expect('path' in (problem.details as object)).toBe(false);
    }
    // The scalar library's own error, when it gives one.
    expect((problemOf('filter[retiredOn]=yesterday').details as { errors: unknown }).errors).toEqual([
      { validator: 'parse', message: 'failed to parse Date: yesterday' },
    ]);
  });

  test('a UUID and a timestamp carry the scalar library’s errors', () => {
    for (const [query, field, reason] of [
      // The scalar library reads a UUID's base62 text too, so a word of
      // letters is one; a hyphen outside the hyphenated form is not.
      ['filter[id]=not-a-uuid', 'id', 'expected a UUID'],
      ['filter[supplierId][in]=' + UUID + ',not-a-uuid', 'supplierId', 'expected a UUID'],
      ['filter[madeAt]=yesterday', 'madeAt', 'expected an RFC 3339 timestamp'],
    ] as const) {
      const problem = problemOf(query);
      expect(problem.detail).toBe(`Invalid query parameter filter[${field}]: ${reason}`);
      expect(problem.details).toMatchObject({ location: 'query', parameter: `filter[${field}]`, reason });
      expect((problem.details as { errors: unknown[] }).errors.length).toBeGreaterThan(0);
      // The parameter names the value; it has no path inside one.
      expect('path' in (problem.details as object)).toBe(false);
    }
  });

  test('any value of an in refuses it', () => {
    expect(problemOf('filter[quantity][in]=1,two,3')).toEqual(refusal('quantity', 'expected an integer', typeError('expected an integer')));
    expect(problemOf('filter[kind][in]=bolt,gear')).toEqual(refusal('kind', 'expected one of bolt, nut, washer'));
  });

  test('the reader’s refusals come first, then the first value in the spec’s order', () => {
    // A field the spec does not name, behind a value its type refuses.
    expect(problemOf('filter[quantity]=many&filter[secret]=x').detail).toStartWith('Invalid query parameter filter[secret]: is not a field');
    expect(problemOf('filter[quantity]=many&filter[kind][gt]=nut').detail).toBe(
      'Invalid query parameter filter[kind]: takes no operator gt; expected one of eq, in'
    );
    expect(problemOf('filter[supplierId][isNull]=yes&filter[id]=not-a-uuid').detail).toBe(
      'Invalid query parameter filter[supplierId]: isNull expects true or false'
    );
    // Two values refused: the spec's first field's, whichever the query sent first.
    expect(problemOf('filter[quantity]=many&filter[kind]=gear').details).toMatchObject({ parameter: 'filter[kind]' });
    expect(problemOf('filter[quantity][lte]=many&filter[quantity]=1.5&filter[quantity][gt]=x').details).toMatchObject({
      parameter: 'filter[quantity]',
    });
  });
});

describe('a route that reads a filter', () => {
  // A list as a generated router mounts it: the adapter decodes the
  // operation's own query parameters, then the handler reads the filter
  // and then the page.
  const listParts = {
    name: 'listParts',
    namespace: 'catalog',
    method: 'GET',
    path: '/api/parts',
    pathParams: [],
    queryParams: [{ name: 'inStock', kind: 'boolean', required: false }],
    bodyParams: [],
    auth: { public: true, required: false, permissions: [] },
    manual: false,
  } as const satisfies OperationSpec;
  const app = new Hono();
  mountOperation(app, listParts, async (ctx, request) => ({
    inStock: request.query.inStock ?? null,
    filter: readFilter(ctx.query, spec),
    page: readPage(ctx.query, { defaultLimit: 2, maxLimit: 5 }),
  }));
  app.onError(errorHandler());
  const get = async (query: string) => {
    const response = await app.request(`/api/parts?${query}`);
    return { status: response.status, body: (await response.json()) as any };
  };

  test('answers the filter beside the query parameters and the page', async () => {
    const { status, body } = await get('inStock=true&filter[kind][in]=nut,bolt&filter%5Bquantity%5D%5Bgte%5D=2&limit=3');
    expect(status).toBe(200);
    expect(body.data).toEqual({ inStock: true, filter: { kind: { in: ['nut', 'bolt'] }, quantity: { gte: 2 } }, page: { limit: 3 } });
  });

  test('refuses its own query parameters first, then the filter’s keys, then its values, then the page', async () => {
    for (const [query, parameter] of [
      ['limit=0&filter[quantity]=many&filter[secret]=x&inStock=maybe', 'inStock'],
      ['limit=0&filter[quantity]=many&filter[secret]=x', 'filter[secret]'],
      ['limit=0&filter[quantity]=many', 'filter[quantity]'],
      ['limit=0&filter[madeAt]=yesterday', 'filter[madeAt]'],
      ['limit=0&filter[quantity]=3', 'limit'],
    ] as const) {
      const { status, body } = await get(query);
      expect(status).toBe(400);
      expect(body.code).toBe('bad_request');
      expect(body.details).toMatchObject({ location: 'query', parameter });
    }
  });
});

describe('readFilters', () => {
  const issues = { fields: [{ name: 'title', operators: ['eq', 'in', 'contains'] }] } as const;

  test('reads keys in the order of their UTF-8 bytes, not of their UTF-16 code units', () => {
    // U+FF5E's code unit, 0xFF5E, is past a surrogate's, 0xD83D, and its
    // UTF-8 bytes, EF BD 9E, are before U+1F600's, F0 9F 98 80.
    const first = 'filter[～]';
    const second = 'filter[\u{1f600}]';
    expect([second, first].sort()).toEqual([second, first]);
    for (const keys of [
      [first, second],
      [second, first],
    ]) {
      const query = new URLSearchParams(keys.map(key => [key, 'x']));
      expect(() => readFilters(query, issues)).toThrow(`Invalid query parameter ${first}: is not a field`);
    }
  });

  test('an in trims what Go’s strings.TrimSpace trims', () => {
    // U+0085 is White_Space, which String.prototype.trim keeps; U+FEFF is
    // not, which it trims.
    const query = new URLSearchParams([['filter[title][in]', '\u0085a , b　,﻿c']]);
    expect(readFilters(query, issues)).toEqual([{ field: 'title', operator: 'in', values: ['a', 'b', '﻿c'] }]);
  });

  test('its limits are the shared ones', () => {
    expect([MAX_FILTERS, MAX_FILTER_IN_VALUES]).toEqual([50, 100]);
    expect(FILTER_OPERATORS).toEqual(['eq', 'in', 'gt', 'gte', 'lt', 'lte', 'contains', 'isNull']);
  });
});
