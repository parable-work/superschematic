/*
Runtime check of the generated router for object-lists-api (run by
TestGeneratedObjectListsRouter with API_DIR set to the materialized package).
The stub implementation records what it received and echoes the outline; the
assertions cover what the router owns for a Point[] body argument: valid
points reach the implementation as parsed objects, the list rules apply to
the list, a null element, a non-object element and an element with a bad
field are refused at name[i], and a Point[] response is sent as returned.
*/
import { describe, expect, test } from 'bun:test';
import { Hono } from 'hono';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';

const apiDir = process.env.API_DIR;
if (!apiDir) throw new Error('API_DIR is required');
const { buildRouter, operationSpecs } = await import(`${apiDir}/router.ts`);

// What the implementation received, for the assertions that a refused
// request never reached it.
const received: unknown[] = [];

function app() {
  const root = new Hono();
  const implementations = {
    drawing: {
      saveOutline: async (args: { id: string; points: unknown[]; markers?: unknown[]; shades?: string[] }) => {
        received.push(args);
        return args.points;
      },
    },
  };
  root.route('/', buildRouter(implementations));
  root.notFound(notFoundHandler());
  root.onError(errorHandler());
  return root;
}

async function save(body: unknown) {
  const response = await app().request('/api/drawings/d1/outline', {
    method: 'PUT',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(body),
  });
  return { status: response.status, body: await response.json() };
}

/** Sends a body the router must refuse and checks it never reached the implementation. */
async function refused(body: unknown) {
  const before = received.length;
  const response = await save(body);
  expect(response.status).toBe(400);
  expect(response.body).toMatchObject({ status: 400, code: 'bad_request' });
  expect(received.length).toBe(before);
  return response.body;
}

describe('generated object-lists-api router', () => {
  test('operation table marks the object lists with their parser', () => {
    const [points, markers, shades] = operationSpecs.saveOutline.bodyParams;
    expect(points).toMatchObject({ name: 'points', kind: 'object', required: true, isArray: true, listMax: 4 });
    expect(typeof points.parse).toBe('function');
    expect('isArrayOfArrays' in points).toBe(false);
    expect(markers).toMatchObject({ name: 'markers', kind: 'object', required: false, isArray: true });
    expect(shades).toEqual({ name: 'shades', kind: 'enum', required: false, isArray: true, enumValues: ['light', 'dark'] });
  });

  test('valid points reach the implementation as objects, and the Point[] response keeps them', async () => {
    const outline = [{ x: 1, y: 2 }, { x: 3.5, y: -4 }];
    const saved = await save({ points: outline, markers: [{ x: 0, y: 0 }], shades: ['light', 'dark'] });
    expect(saved.status).toBe(200);
    expect(received.at(-1)).toEqual({ id: 'd1', points: outline, markers: [{ x: 0, y: 0 }], shades: ['light', 'dark'] });
    const args = received.at(-1) as { points: unknown[]; markers: unknown[] };
    expect(args.points.every(point => typeof point === 'object' && point !== null && !Array.isArray(point))).toBe(true);
    expect(typeof args.markers[0]).toBe('object');
    expect(saved.body.data).toEqual(outline);
  });

  test('required means present: an empty list is valid, an absent or null one is refused', async () => {
    const empty = await save({ points: [] });
    expect(empty.status).toBe(200);
    expect(received.at(-1)).toEqual({ id: 'd1', points: [] });
    expect(empty.body.data).toEqual([]);
    for (const points of [undefined, null]) {
      const body = await refused({ points });
      expect(body.details).toEqual({ location: 'body', parameter: 'points', reason: 'required' });
    }
  });

  test('an optional list may be absent or null', async () => {
    for (const markers of [undefined, null]) {
      const saved = await save({ points: [{ x: 1, y: 1 }], markers });
      expect(saved.status).toBe(200);
      expect(received.at(-1)).toEqual({ id: 'd1', points: [{ x: 1, y: 1 }] });
    }
  });

  test('listMax bounds the list, not its elements', async () => {
    const point = { x: 1, y: 1 };
    const body = await refused({ points: [point, point, point, point, point] });
    expect(body.details).toEqual({ location: 'body', parameter: 'points', reason: 'expected at most 4 values' });
    const notList = await refused({ points: { x: 1, y: 1 } });
    expect(notList.details).toEqual({ location: 'body', parameter: 'points', reason: 'expected an array', errors: [{ validator: 'type', message: 'expected an array' }] });
  });

  test('a null element is refused at name[i] as required, in a required and an optional list', async () => {
    const body = await refused({ points: [{ x: 1, y: 2 }, null] });
    expect(body).toMatchObject({
      detail: 'Invalid body parameter points[1]: required field',
      details: {
        location: 'body',
        parameter: 'points',
        path: 'points[1]',
        reason: 'required field',
        errors: [{ validator: 'required', message: 'required field' }],
      },
    });
    const optional = await refused({ points: [], markers: [null] });
    expect(optional.details).toMatchObject({ parameter: 'markers', path: 'markers[0]', errors: [{ validator: 'required', message: 'required field' }] });
  });

  test('a non-object element is refused at name[i] as a type error', async () => {
    for (const element of ['p', 5, true, [1, 2]]) {
      const body = await refused({ points: [{ x: 1, y: 2 }, element] });
      expect(body).toMatchObject({
        detail: 'Invalid body parameter points[1]: expected an object',
        details: {
          location: 'body',
          parameter: 'points',
          path: 'points[1]',
          reason: 'expected an object',
          errors: [{ validator: 'type', message: 'expected an object' }],
        },
      });
    }
  });

  test('an element with a bad field is refused at name[i] by the generated strict parser', async () => {
    const nullField = await refused({ points: [{ x: 1, y: 2 }, { x: 1, y: null }] });
    expect(nullField).toMatchObject({ detail: 'Invalid body parameter points[1]: does not match the declared type' });
    expect(nullField.details).toEqual({ location: 'body', parameter: 'points', path: 'points[1]', reason: 'does not match the declared type' });
    const missingField = await refused({ points: [{ x: 1 }] });
    expect(missingField.details).toMatchObject({ parameter: 'points', path: 'points[0]' });
    const unknownField = await refused({ points: [], markers: [{ x: 1, y: 2, z: 3 }] });
    expect(unknownField.details).toMatchObject({ parameter: 'markers', path: 'markers[0]' });
  });

  test('an enum list is decoded as before', async () => {
    const saved = await save({ points: [], shades: ['dark'] });
    expect(saved.status).toBe(200);
    expect(received.at(-1)).toEqual({ id: 'd1', points: [], shades: ['dark'] });
    const body = await refused({ points: [], shades: ['dim'] });
    expect(body.details).toMatchObject({ location: 'body', parameter: 'shades', reason: 'expected one of light, dark' });
  });
});
