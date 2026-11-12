/*
Runtime check of a filter through a generated router's MCP endpoint
(D106), run by TestGeneratedPagedMCPFilter with API_DIR set to the
materialized fixture-paged-api package, built with outputs.api.mcp on and
listParts a tool, list_parts. The implementation is
paged_implementation.ts. A tools/call's filter argument is an object, a
member per field holding a value, for equality, or an object of operators;
the endpoint runs the call through listParts's route, writing the filter
into its query as the bracket form, so the route's reader reads it, and
refuses it, as it does any caller's.
*/
import { describe, expect, test } from 'bun:test';
import { Hono } from 'hono';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';

const apiDir = process.env.API_DIR;
if (!apiDir) throw new Error('API_DIR is required');
const { buildRouter } = await import(`${apiDir}/router.ts`);
const { mcpTools } = await import(`${apiDir}/mcp.ts`);
const { catalog, parts, received, suppliers } = await import(`${apiDir}/paged_implementation.ts`);

const root = new Hono();
root.route('/', buildRouter({ catalog }));
root.notFound(notFoundHandler());
root.onError(errorHandler());

let ids = 0;
async function mcp(method: string, params?: unknown): Promise<Record<string, any>> {
  const response = await root.request('/api/mcp', {
    method: 'POST',
    headers: { accept: 'application/json, text/event-stream', 'content-type': 'application/json' },
    body: JSON.stringify({ jsonrpc: '2.0', id: ++ids, method, ...(params !== undefined ? { params } : {}) }),
  });
  expect(response.status).toBe(200);
  const text = await response.text();
  expect(text.startsWith('event: message\ndata: ')).toBe(true);
  return JSON.parse(text.slice('event: message\ndata: '.length));
}

const listParts = async (args: unknown) => (await mcp('tools/call', { name: 'list_parts', arguments: args })).result;

/** The names of the parts a call lists, and the arguments its implementation took. */
async function kept(args: unknown) {
  const result = await listParts(args);
  expect(result.isError).toBeUndefined();
  return {
    names: result.structuredContent.items.map((row: { name: string }) => row.name),
    args: received.at(-1)?.args,
  };
}

describe('the list_parts tool', () => {
  test('is the one tool, and takes the filter as an object beside the sort and the page', async () => {
    expect(mcpTools.map((tool: { name: string }) => tool.name)).toEqual(['list_parts']);
    expect(mcpTools[0].arguments).toEqual([
      { key: 'sort', in: 'query', name: 'sort' },
      { key: 'filter', in: 'filter', name: 'filter' },
      { key: 'cursor', in: 'query', name: 'cursor' },
      { key: 'limit', in: 'query', name: 'limit' },
    ]);
    const [listed] = (await mcp('tools/list')).result.tools;
    const filter = listed.inputSchema.properties.filter;
    expect(filter.type).toBe('object');
    expect(Object.keys(filter.properties).sort()).toEqual(
      ['discontinued', 'id', 'kind', 'madeAt', 'name', 'price', 'quantity', 'retiredOn', 'supplierId', 'weight']
    );
  });

  test('a call without a filter lists every part', async () => {
    const all = await kept({ limit: 5 });
    expect(all.names).toEqual(parts.slice(0, 5).map((row: { name: string }) => row.name));
    expect(all.args.filter).toEqual({});
  });

  test('a member that holds a value is equality, read as its field’s type', async () => {
    expect(await kept({ filter: { kind: 'nut', discontinued: false }, limit: 5 })).toEqual({
      names: ['Cap nut', 'Wing nut'],
      args: { filter: { kind: { eq: 'nut' }, discontinued: { eq: false } }, page: { limit: 5, sort: { field: 'name', desc: false } } },
    });
    expect((await kept({ filter: { quantity: 40 } })).args.filter).toEqual({ quantity: { eq: 40 } });
    expect((await kept({ filter: { id: '00000000-0000-4000-8000-000000000103' } })).args.filter).toEqual({ id: { eq: parts[2].id } });
    const made = await kept({ filter: { madeAt: '2026-01-10T08:00:00Z' } });
    expect(made.names).toEqual(['Anchor bolt', 'Flat washer']);
    expect(made.args.filter.madeAt.eq).toEqual(new Date('2026-01-10T08:00:00Z'));
  });

  test('a member that holds operators: in, a range, contains and isNull', async () => {
    expect(await kept({ filter: { kind: { in: ['bolt', 'washer'] } }, limit: 5 })).toMatchObject({
      names: ['Anchor bolt', 'Eye bolt', 'Flat washer'],
      args: { filter: { kind: { in: ['bolt', 'washer'] } } },
    });
    expect(await kept({ filter: { quantity: { gte: 7, lt: 40 } }, limit: 5 })).toMatchObject({
      names: ['Anchor bolt', 'Hex nut'],
      args: { filter: { quantity: { gte: 7, lt: 40 } } },
    });
    expect(await kept({ filter: { name: { contains: 'NUT' }, price: { lte: '0.30' } }, limit: 5 })).toMatchObject({
      names: ['Cap nut', 'Hex nut'],
      args: { filter: { name: { contains: 'NUT' }, price: { lte: '0.3' } } },
    });
    expect(await kept({ filter: { retiredOn: { isNull: false } }, limit: 5 })).toMatchObject({
      names: ['Eye bolt', 'Hex nut'],
      args: { filter: { retiredOn: { isNull: false } } },
    });
    expect(await kept({ filter: { supplierId: { isNull: false, in: [suppliers[0]] } } })).toMatchObject({
      names: ['Anchor bolt', 'Cap nut'],
      args: { filter: { supplierId: { in: [suppliers[0]], isNull: false } } },
    });
  });

  test('with a sort and a cursor, each page is asked with the same filter', async () => {
    const args = { sort: '-quantity', limit: 3, filter: { kind: { in: ['nut', 'washer'] } } };
    const first = await listParts(args);
    expect(first.structuredContent.items.map((row: { name: string }) => row.name)).toEqual(['Flat washer', 'Cap nut', 'Hex nut']);
    const last = await listParts({ ...args, cursor: first.structuredContent.next });
    expect(last.structuredContent).toMatchObject({ items: [{ name: 'Wing nut' }], next: null });
    expect(received.at(-1)?.args.filter).toEqual({ kind: { in: ['nut', 'washer'] } });
  });

  test("a field, an operator or a value the route refuses is the route's 400 at filter[field]", async () => {
    const before = received.length;
    for (const [filter, parameter, reason] of [
      [{ sku: 'x' }, 'filter[sku]', 'is not a field this operation filters by; expected one of id, name, kind, quantity, weight, price, discontinued, madeAt, retiredOn, supplierId'],
      [{ kind: { gt: 'nut' } }, 'filter[kind]', 'takes no operator gt; expected one of eq, ne, in, notIn'],
      [{ name: { isNull: true } }, 'filter[name]', 'takes no operator isNull; expected one of eq, ne, in, notIn, contains'],
      [{ quantity: 'many' }, 'filter[quantity]', 'expected an integer'],
      [{ kind: { in: ['nut', 'gear'] } }, 'filter[kind]', 'expected one of bolt, nut, washer'],
      [{ madeAt: 'yesterday' }, 'filter[madeAt]', 'expected an RFC 3339 timestamp'],
      [{ id: 'not-a-uuid' }, 'filter[id]', 'expected a UUID'],
      [{ retiredOn: 'yesterday' }, 'filter[retiredOn]', 'expected a date'],
      [{ price: { gt: 'cheap' } }, 'filter[price]', 'expected a decimal number'],
    ] as const) {
      const result = await listParts({ filter });
      expect(result.isError).toBe(true);
      expect(result.structuredContent).toMatchObject({
        status: 400,
        code: 'bad_request',
        detail: `Invalid query parameter ${parameter}: ${reason}`,
        details: { location: 'query', parameter, reason },
      });
    }
    // A filter with no bracket form, or whose form the route would read
    // as another filter, is refused before the route.
    for (const [filter, detail] of [
      [{ retiredOn: null }, 'list_parts: filter.retiredOn is a value, for equality, or an object of operators'],
      [{ name: { in: ['Hex nut', 'Nut, wing'] } }, 'list_parts: filter.name.in[1] must not be blank or hold a comma or a space at either end'],
      [{ name: { contains: '' } }, 'list_parts: filter.name.contains must not be empty'],
    ] as const) {
      const shaped = await listParts({ filter });
      expect(shaped.isError).toBe(true);
      expect(shaped.structuredContent).toMatchObject({ status: 400, code: 'bad_request', detail });
    }
    expect(received.length).toBe(before);
  });
});
