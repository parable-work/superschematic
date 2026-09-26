/*
Runtime check of the generated router for scalar-lists-api (run by
TestGeneratedScalarListsRouter with API_DIR set to the materialized package).
The stub implementation records what it received and echoes it back. The
assertions cover how the router decodes body arguments of a scalar, enum or
Generic.JSON type from their JSON value: a list is its JSON array (a comma
stays inside its element, an empty string is kept), a null element is
refused at name[i] as required and a wrong-type one as type, "5" is not a
number, [] satisfies a required list, list bounds and the scalar's own
pattern, lengths and range apply to each element, and a Generic.JSON
argument is any JSON value but null. A GET list is still read from
comma-separated query values.
*/
import { describe, expect, test } from 'bun:test';
import { Hono } from 'hono';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';

const apiDir = process.env.API_DIR;
if (!apiDir) throw new Error('API_DIR is required');
const { buildRouter, operationSpecs } = await import(`${apiDir}/router.ts`);

// What the implementation received, for the assertions that a refused
// request never reached it.
const received: Record<string, unknown>[] = [];

function app() {
  const root = new Hono();
  const implementations = {
    tag: {
      saveTags: async (args: Record<string, unknown>) => {
        received.push(args);
        return args.labels;
      },
      storeDocument: async (args: Record<string, unknown>) => {
        received.push(args);
        return args.document;
      },
      findTags: async (args: Record<string, unknown>) => {
        received.push(args);
        return args.labels;
      },
    },
  };
  root.route('/', buildRouter(implementations));
  root.notFound(notFoundHandler());
  root.onError(errorHandler());
  return root;
}

async function send(method: string, path: string, body?: unknown) {
  const response = await app().request(path, {
    method,
    headers: { 'content-type': 'application/json' },
    ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
  });
  return { status: response.status, body: await response.json() };
}

const saveTags = (body: unknown) => send('PUT', '/api/posts/p1/tags', body);
const storeDocument = (body: unknown) => send('POST', '/api/documents', body);

/** Sends a body the router must refuse and checks it never reached the implementation. */
async function refused(call: (body: unknown) => Promise<{ status: number; body: any }>, body: unknown) {
  const before = received.length;
  const response = await call(body);
  expect(response.status).toBe(400);
  expect(response.body).toMatchObject({ status: 400, code: 'bad_request' });
  expect(received.length).toBe(before);
  return response.body;
}

/** The detail of a refusal at `path` named by one rule. */
function at(parameter: string, path: string | undefined, validator: string, reason: string) {
  return {
    detail: `Invalid body parameter ${path ?? parameter}: ${reason}`,
    details: { location: 'body', parameter, ...(path !== undefined ? { path } : {}), reason, errors: [{ validator, message: reason }] },
  };
}

// One row per list argument of saveTags: a valid list, and elements of the
// wrong JSON type with the refusal they get.
const lists = [
  { name: 'labels', valid: ['a', 'b'], wrong: [5, { a: 1 }, true, ['a']], message: 'expected a string' },
  { name: 'weights', valid: [1.5, -2, 0], wrong: ['5', true, [1]], message: 'expected a number' },
  { name: 'ranks', valid: [1, 2], wrong: ['2', 1.5, false], message: 'expected an integer' },
  { name: 'shades', valid: ['light', 'dark'], wrong: [1, { shade: 'light' }], message: 'expected a string' },
  { name: 'links', valid: ['https://a.test'], wrong: [42, {}], message: 'expected a string' },
];

describe('generated scalar-lists-api router', () => {
  test('operation table: body arguments keep their kinds, Generic.JSON is json, a JSON object or array scalar is jsonObject or jsonArray, a scalar carries its constraints', () => {
    const [labels, weights, ranks, shades, links] = operationSpecs.saveTags.bodyParams;
    expect(labels).toEqual({ name: 'labels', kind: 'string', required: true, isArray: true, listMax: 3 });
    expect(weights).toEqual({ name: 'weights', kind: 'number', required: false, isArray: true });
    expect(ranks).toEqual({ name: 'ranks', kind: 'integer', required: false, isArray: true, scalar: { name: 'Ordering.Rank', min: 1, max: Number.MAX_SAFE_INTEGER } });
    expect(shades).toEqual({ name: 'shades', kind: 'enum', required: false, isArray: true, enumValues: ['light', 'dark'] });
    expect(links).toMatchObject({ name: 'links', kind: 'string', isArray: true, listMin: 1, listMax: 2, pattern: '^https://', scalar: { name: 'Network.Url', maxLength: 2048 } });
    expect(operationSpecs.storeDocument.bodyParams.map((param: { kind: string }) => param.kind)).toEqual([
      'json',
      'json',
      'json',
      'json',
      'jsonObject',
      'jsonArray',
    ]);
    expect(operationSpecs.findTags.queryParams).toEqual([{ name: 'labels', kind: 'string', required: true, isArray: true }]);
  });

  test('valid lists reach the implementation as their JSON values', async () => {
    for (const list of lists) {
      const body = { labels: [], [list.name]: list.valid };
      const saved = await saveTags(body);
      expect(saved.status).toBe(200);
      expect(received.at(-1)).toEqual({ id: 'p1', ...body });
    }
  });

  test('a list element is one JSON value: a comma stays inside it and an empty string is kept', async () => {
    const saved = await saveTags({ labels: ['a,b', '', ' c '] });
    expect(saved.status).toBe(200);
    expect(received.at(-1)).toEqual({ id: 'p1', labels: ['a,b', '', ' c '] });
    expect(saved.body.data).toEqual(['a,b', '', ' c ']);
    const title = await saveTags({ labels: [], title: 'x, y' });
    expect(received.at(-1)).toEqual({ id: 'p1', labels: [], title: 'x, y' });
    expect(title.status).toBe(200);
  });

  test('a null element is refused at name[i] as required, in every list', async () => {
    for (const list of lists) {
      const body = await refused(saveTags, { labels: [], [list.name]: [list.valid[0], null] });
      expect(body).toMatchObject(at(list.name, `${list.name}[1]`, 'required', 'required field'));
    }
  });

  test('an element of the wrong JSON type is refused at name[i] as type; "5" is not a number', async () => {
    for (const list of lists) {
      for (const element of list.wrong) {
        const body = await refused(saveTags, { labels: [], [list.name]: [list.valid[0], element] });
        expect(body).toMatchObject(at(list.name, `${list.name}[1]`, 'type', list.message));
      }
    }
    expect(await refused(saveTags, { labels: [], priority: '5' })).toMatchObject(at('priority', undefined, 'type', 'expected a number'));
    expect(await refused(saveTags, { labels: [], title: 5 })).toMatchObject(at('title', undefined, 'type', 'expected a string'));
  });

  test('required means present: [] satisfies a required list, absent or null is refused', async () => {
    const empty = await saveTags({ labels: [] });
    expect(empty.status).toBe(200);
    expect(received.at(-1)).toEqual({ id: 'p1', labels: [] });
    for (const labels of [undefined, null]) {
      const body = await refused(saveTags, { labels });
      expect(body.details).toEqual({ location: 'body', parameter: 'labels', reason: 'required' });
    }
    const optional = await saveTags({ labels: [], weights: null, shades: undefined });
    expect(optional.status).toBe(200);
    expect(received.at(-1)).toEqual({ id: 'p1', labels: [] });
  });

  test('listMin and listMax bound the list', async () => {
    const over = await refused(saveTags, { labels: ['a', 'b', 'c', 'd'] });
    expect(over.details).toEqual({ location: 'body', parameter: 'labels', reason: 'expected at most 3 values' });
    const under = await refused(saveTags, { labels: [], links: [] });
    expect(under.details).toEqual({ location: 'body', parameter: 'links', reason: 'expected at least 1 values' });
    const tooMany = await refused(saveTags, { labels: [], links: ['https://a.test', 'https://b.test', 'https://c.test'] });
    expect(tooMany.details).toEqual({ location: 'body', parameter: 'links', reason: 'expected at most 2 values' });
    const notList = await refused(saveTags, { labels: 'a,b' });
    expect(notList.details).toEqual({ location: 'body', parameter: 'labels', reason: 'expected an array', errors: [{ validator: 'type', message: 'expected an array' }] });
  });

  test("the scalar's own pattern, length and range apply to each element, one error named by its rule", async () => {
    expect(await refused(saveTags, { labels: [], links: ['https://a.test', 'not a url'] })).toMatchObject(
      at('links', 'links[1]', 'pattern', 'is not a valid Network.Url')
    );
    expect(await refused(saveTags, { labels: [], links: [`https://${'a'.repeat(2050)}.test`] })).toMatchObject(
      at('links', 'links[0]', 'maxLength', 'must be at most 2048 characters')
    );
    expect(await refused(saveTags, { labels: [], ranks: [2, 0] })).toMatchObject(at('ranks', 'ranks[1]', 'min', 'must be at least 1'));
    // The argument's own pattern applies after the scalar's.
    expect(await refused(saveTags, { labels: [], links: ['http://a.test'] })).toMatchObject(
      at('links', 'links[0]', 'pattern', 'does not match the required pattern')
    );
  });

  test('an enum element outside the enum is refused at name[i]', async () => {
    const body = await refused(saveTags, { labels: [], shades: ['light', 'dim'] });
    expect(body.details).toMatchObject({ location: 'body', parameter: 'shades', path: 'shades[1]', reason: 'expected one of light, dark' });
  });

  test('a Generic.JSON argument is any JSON value but null, and reaches the implementation as that value', async () => {
    for (const document of [{ a: [1, null] }, [1, 'a'], 'text', '', 0, false]) {
      const stored = await storeDocument({ document });
      expect(stored.status).toBe(200);
      expect(received.at(-1)).toEqual({ document });
      expect(stored.body.data).toEqual(document);
    }
    for (const document of [undefined, null]) {
      const body = await refused(storeDocument, { document });
      expect(body.details).toEqual({ location: 'body', parameter: 'document', reason: 'required' });
    }
    const noNote = await storeDocument({ document: 1, note: null });
    expect(noNote.status).toBe(200);
    expect(received.at(-1)).toEqual({ document: 1 });
  });

  test('a Generic.JSON list takes any JSON element but null, at both list depths', async () => {
    const extras = [1, 'a', { b: 2 }, [3, null], true];
    const grid = [[{ a: 1 }, 'x'], []];
    const stored = await storeDocument({ document: 1, extras, grid });
    expect(stored.status).toBe(200);
    expect(received.at(-1)).toEqual({ document: 1, extras, grid });
    expect(await refused(storeDocument, { document: 1, extras: [1, null] })).toMatchObject(at('extras', 'extras[1]', 'required', 'required field'));
    expect(await refused(storeDocument, { document: 1, grid: [[null]] })).toMatchObject(at('grid', 'grid[0][0]', 'required', 'required field'));
    expect(await refused(storeDocument, { document: 1, grid: [null] })).toMatchObject(at('grid', 'grid[0]', 'required', 'required field'));
    expect(await refused(storeDocument, { document: 1, grid: [{}] })).toMatchObject(at('grid', 'grid[0]', 'type', 'expected an array'));
  });

  test('a JSON object or array scalar takes that object or array, as the generated types send it', async () => {
    const labels = { region: 'eu' };
    const embeddings = [[0.5, 1], []];
    const stored = await storeDocument({ document: 1, labels, embeddings });
    expect(stored.status).toBe(200);
    expect(received.at(-1)).toEqual({ document: 1, labels, embeddings });
    expect(await refused(storeDocument, { document: 1, labels: '{"region":"eu"}' })).toMatchObject(
      at('labels', undefined, 'type', 'expected an object')
    );
    expect(await refused(storeDocument, { document: 1, labels: ['eu'] })).toMatchObject(at('labels', undefined, 'type', 'expected an object'));
    expect(await refused(storeDocument, { document: 1, embeddings: [[1], '[2]'] })).toMatchObject(
      at('embeddings', 'embeddings[1]', 'type', 'expected an array')
    );
    expect(await refused(storeDocument, { document: 1, embeddings: [null] })).toMatchObject(
      at('embeddings', 'embeddings[0]', 'required', 'required field')
    );
  });

  test('a GET list still reads repeated keys and comma-separated query values', async () => {
    const found = await send('GET', '/api/posts/tags?labels=a,b&labels=c');
    expect(found.status).toBe(200);
    expect(received.at(-1)).toEqual({ labels: ['a', 'b', 'c'] });
    const missing = await send('GET', '/api/posts/tags');
    expect(missing.status).toBe(400);
    expect(missing.body.details).toEqual({ location: 'query', parameter: 'labels', reason: 'required' });
  });
});
