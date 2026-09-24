/*
Runtime check of the generated router for fixture-nested-arrays-api with
grid.paint added (run by TestGeneratedNestedArraysRouter with API_DIR set to
the materialized package). Stub implementations echo what they received; the
assertions cover what the router owns for lists of lists: a nested body
round-trips, a null inner list is refused at name[i] and a bad element at
name[i][j], and a list-of-lists response is sent with every inner list an
array.
*/
import { describe, expect, test } from 'bun:test';
import { Hono } from 'hono';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { parseIdentityUUID } from 'superscalar/scalars';

const apiDir = process.env.API_DIR;
if (!apiDir) throw new Error('API_DIR is required');
const { buildRouter, operationSpecs } = await import(`${apiDir}/router.ts`);

const gridId = '00000000-0000-4000-8000-000000000001';
// The router hands implementations the scalar library's canonical form.
const canonicalId = parseIdentityUUID(gridId);

const grid = {
  id: gridId,
  labels: [['a', 'b'], [], ['c']],
  shades: [['light'], ['dark', 'light']],
  polygons: [[{ x: 1, y: 2 }, { x: 3, y: 4 }], []],
  weights: [[0.5], []],
};

// What each implementation received, for the assertions that a refused
// request never reached it.
const received: Record<string, unknown[]> = {};

function record(name: string, args: unknown) {
  (received[name] ??= []).push(args);
}

function app(overrides: { labels?: unknown; polygons?: unknown } = {}) {
  const root = new Hono();
  const implementations = {
    grid: {
      saveGrid: async (args: { input: Record<string, unknown> }) => {
        record('saveGrid', args);
        return { id: gridId, ...args.input };
      },
      replaceLabels: async (args: { id: string; labels: string[][] }) => {
        record('replaceLabels', args);
        return { ...grid, id: args.id, labels: args.labels };
      },
      paint: async (args: { id: string; shades: string[][]; polygons?: unknown[][] }) => {
        record('paint', args);
        return overrides.polygons ?? args.polygons ?? [];
      },
      getGrid: async (args: { id: string }) => ({ ...grid, id: args.id }),
      gridLabels: async (args: { id: string; limit?: number }) => {
        record('gridLabels', args);
        return overrides.labels ?? grid.labels;
      },
    },
  };
  root.route('/', buildRouter(implementations));
  root.notFound(notFoundHandler());
  root.onError(errorHandler());
  return root;
}

async function send(router: Hono, method: string, path: string, body?: unknown) {
  const response = await router.request(path, {
    method,
    headers: { 'content-type': 'application/json' },
    ...(body === undefined ? {} : { body: typeof body === 'string' ? body : JSON.stringify(body) }),
  });
  return { status: response.status, body: await response.json() };
}

describe('generated fixture-nested-arrays-api router', () => {
  test('operation table marks the list-of-lists arguments and responses', () => {
    expect(Object.keys(operationSpecs).sort()).toEqual(['getGrid', 'gridLabels', 'paint', 'replaceLabels', 'saveGrid']);
    expect(operationSpecs.replaceLabels.bodyParams).toEqual([{ name: 'labels', kind: 'string', required: true, isArray: true, isArrayOfArrays: true }]);
    expect(operationSpecs.paint.bodyParams[0]).toMatchObject({ name: 'shades', kind: 'enum', isArrayOfArrays: true, enumValues: ['light', 'dark'] });
    expect(operationSpecs.paint.bodyParams[1]).toMatchObject({ name: 'polygons', kind: 'object', required: false, isArrayOfArrays: true });
    expect(operationSpecs.gridLabels.outputIsArrayOfArrays).toBe(true);
    expect(operationSpecs.paint.outputIsArrayOfArrays).toBe(true);
    expect('outputIsArrayOfArrays' in operationSpecs.getGrid).toBe(false);
  });

  test('an input type with lists of lists round-trips through the generated parser', async () => {
    const input = { labels: grid.labels, shades: grid.shades, polygons: grid.polygons, weights: grid.weights };
    const saved = await send(app(), 'POST', '/api/grids', input);
    expect(saved.status).toBe(200);
    expect(saved.body.data).toEqual({ id: gridId, ...input });
    const empty = await send(app(), 'POST', '/api/grids', { labels: [], shades: [[]], polygons: [] });
    expect(empty.status).toBe(200);
    expect(empty.body.data).toEqual({ id: gridId, labels: [], shades: [[]], polygons: [] });
    // The input type's own validator refuses a null inner list; the router
    // answers 400 without reaching the implementation.
    const before = received.saveGrid?.length ?? 0;
    const nullRow = await send(app(), 'POST', '/api/grids', { ...input, labels: [['a'], null] });
    expect(nullRow.status).toBe(400);
    expect(nullRow.body).toMatchObject({ status: 400, code: 'bad_request' });
    expect(received.saveGrid?.length ?? 0).toBe(before);
  });

  test('a string[][] body argument round-trips, ragged and empty rows included', async () => {
    const replaced = await send(app(), 'PUT', `/api/grids/${gridId}/labels`, { labels: [[], ['x', 'y'], ['z']] });
    expect(replaced.status).toBe(200);
    expect(replaced.body.data.labels).toEqual([[], ['x', 'y'], ['z']]);
    const none = await send(app(), 'PUT', `/api/grids/${gridId}/labels`, { labels: [] });
    expect(none.status).toBe(200);
    expect(none.body.data.labels).toEqual([]);
  });

  test('a null or non-list inner list is refused at name[i]', async () => {
    const before = received.replaceLabels?.length ?? 0;
    const nullRow = await send(app(), 'PUT', `/api/grids/${gridId}/labels`, { labels: [['x'], null, []] });
    expect(nullRow.status).toBe(400);
    expect(nullRow.body).toMatchObject({
      status: 400,
      code: 'bad_request',
      detail: 'Invalid body parameter labels[1]: required field',
      details: {
        location: 'body',
        parameter: 'labels',
        path: 'labels[1]',
        reason: 'required field',
        errors: [{ validator: 'required', message: 'required field' }],
      },
    });
    const scalarRow = await send(app(), 'PUT', `/api/grids/${gridId}/labels`, { labels: [['x'], 'y'] });
    expect(scalarRow.status).toBe(400);
    expect(scalarRow.body.details).toEqual({
      location: 'body',
      parameter: 'labels',
      path: 'labels[1]',
      reason: 'expected an array',
      errors: [{ validator: 'type', message: 'expected an array' }],
    });
    expect(received.replaceLabels?.length ?? 0).toBe(before);
  });

  test('a bad element is refused at name[i][j]', async () => {
    const number = await send(app(), 'PUT', `/api/grids/${gridId}/labels`, { labels: [['x'], ['y', 5]] });
    expect(number.status).toBe(400);
    expect(number.body.details).toEqual({
      location: 'body',
      parameter: 'labels',
      path: 'labels[1][1]',
      reason: 'expected a string',
      errors: [{ validator: 'type', message: 'expected a string' }],
    });
    const nullElement = await send(app(), 'PUT', `/api/grids/${gridId}/labels`, { labels: [[null]] });
    expect(nullElement.body.details).toMatchObject({ path: 'labels[0][0]', errors: [{ validator: 'required', message: 'required field' }] });
    const badEnum = await send(app(), 'PUT', `/api/grids/${gridId}/paint`, { shades: [['light'], ['dark', 'dim']] });
    expect(badEnum.status).toBe(400);
    expect(badEnum.body).toMatchObject({
      detail: 'Invalid body parameter shades[1][1]: expected one of light, dark',
      details: { location: 'body', parameter: 'shades', path: 'shades[1][1]' },
    });
    const badPoint = await send(app(), 'PUT', `/api/grids/${gridId}/paint`, { shades: [], polygons: [[{ x: 1, y: 2 }], [{ x: 'far' }]] });
    expect(badPoint.status).toBe(400);
    expect(badPoint.body.details).toEqual({ location: 'body', parameter: 'polygons', path: 'polygons[1][0]', reason: 'does not match the declared type' });
    const unknownField = await send(app(), 'PUT', `/api/grids/${gridId}/paint`, { shades: [], polygons: [[{ x: 1, y: 2, z: 3 }]] });
    expect(unknownField.status).toBe(400);
    expect(unknownField.body.details).toMatchObject({ path: 'polygons[0][0]' });
  });

  test('a missing required list of lists is refused; an optional one may be absent', async () => {
    const missing = await send(app(), 'PUT', `/api/grids/${gridId}/labels`, {});
    expect(missing.status).toBe(400);
    expect(missing.body.details).toMatchObject({ location: 'body', parameter: 'labels', reason: 'required' });
    const painted = await send(app(), 'PUT', `/api/grids/${gridId}/paint`, { shades: [['dark'], []] });
    expect(painted.status).toBe(200);
    expect(received.paint?.at(-1)).toEqual({ id: canonicalId, shades: [['dark'], []] });
    expect(painted.body.data).toEqual([]);
  });

  test('enum and object elements reach the implementation decoded, and a Point[][] response keeps its shape', async () => {
    const painted = await send(app(), 'PUT', `/api/grids/${gridId}/paint`, { shades: [['light', 'dark'], []], polygons: [[{ x: 5, y: 6 }], []] });
    expect(painted.status).toBe(200);
    expect(received.paint?.at(-1)).toEqual({ id: canonicalId, shades: [['light', 'dark'], []], polygons: [[{ x: 5, y: 6 }], []] });
    expect(painted.body.data).toEqual([[{ x: 5, y: 6 }], []]);
    const nullRows = await send(app({ polygons: [[{ x: 1, y: 2 }], null] }), 'PUT', `/api/grids/${gridId}/paint`, { shades: [] });
    expect(nullRows.body.data).toEqual([[{ x: 1, y: 2 }], []]);
  });

  test('a bare string[][] response is sent with every inner list an array', async () => {
    const labels = await send(app(), 'GET', `/api/grids/${gridId}/labels?limit=2`);
    expect(labels.status).toBe(200);
    expect(labels.body.data).toEqual([['a', 'b'], [], ['c']]);
    expect(received.gridLabels?.at(-1)).toEqual({ id: canonicalId, limit: 2 });
    const withNulls = await send(app({ labels: [['a'], null, undefined, []] }), 'GET', `/api/grids/${gridId}/labels`);
    expect(withNulls.body.data).toEqual([['a'], [], [], []]);
  });

  test('an object response carries its lists of lists as returned', async () => {
    const fetched = await send(app(), 'GET', `/api/grids/${gridId}`);
    expect(fetched.status).toBe(200);
    expect(fetched.body).toEqual({ data: { ...grid, id: canonicalId }, meta: { requestId: expect.any(String) } });
  });
});
