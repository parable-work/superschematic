/*
Runtime check of the generated router for map-args-api (run by
TestGeneratedMapArgsRouter with API_DIR set to the materialized package).
The stub implementation records what it received. The assertions cover how
the router decodes a map body argument (Record<string, T>, or
Record<string, T[]>) from its JSON value, with the rules the Go router's
map arguments follow: a map is a JSON object and {} is a value, absent or
null is refused when required, a value is never null and passes the checks
of a list element at name[key] (the enum's values, the argument's own
constraints, an object value's parser), a map of lists checks each list at
name[key] and its elements at name[key][i], and list bounds do not bound a
map.
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
    post: {
      nameThings: async (args: Record<string, unknown>) => {
        received.push(args);
        return true;
      },
    },
  };
  root.route('/', buildRouter(implementations));
  root.notFound(notFoundHandler());
  root.onError(errorHandler());
  return root;
}

async function nameThings(body: unknown) {
  const response = await app().request('/api/posts/p1/names', {
    method: 'PUT',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(body),
  });
  return { status: response.status, body: await response.json() };
}

/** Sends a body the router must refuse and checks it never reached the implementation. */
async function refused(body: unknown) {
  const before = received.length;
  const response = await nameThings(body);
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

describe('generated map-args-api router', () => {
  test('operation table: a map argument says isMap, with its value kind and constraints', () => {
    const [shadeByName, linksByLocale, pointByName, weightByName] = operationSpecs.nameThings.bodyParams;
    expect(shadeByName).toEqual({ name: 'shadeByName', kind: 'enum', required: true, isMap: true, enumValues: ['light', 'dark'] });
    expect(linksByLocale).toMatchObject({ name: 'linksByLocale', kind: 'string', required: false, isArray: true, isMap: true, pattern: '^https://' });
    expect(linksByLocale.listMax).toBeUndefined();
    expect(pointByName).toMatchObject({ name: 'pointByName', kind: 'object', required: false, isMap: true });
    expect(weightByName).toEqual({ name: 'weightByName', kind: 'number', required: false, isMap: true, max: 1 });
  });

  test('a map is a JSON object whose values reach the implementation', async () => {
    const body = {
      shadeByName: { a: 'light', b: 'dark' },
      linksByLocale: { en: ['https://a.test', 'https://b.test'], fr: [] },
      pointByName: { p: { x: 1, y: 2 } },
      weightByName: { w: 0.5 },
    };
    const named = await nameThings(body);
    expect(named.status).toBe(200);
    expect(named.body.data).toBe(true);
    expect(received.at(-1)).toEqual({ id: 'p1', ...body });
    const empty = await nameThings({ shadeByName: {} });
    expect(empty.status).toBe(200);
    expect(received.at(-1)).toEqual({ id: 'p1', shadeByName: {} });
  });

  test('required means present, and the map must be a JSON object', async () => {
    for (const shadeByName of [undefined, null]) {
      const body = await refused({ shadeByName });
      expect(body.details).toEqual({ location: 'body', parameter: 'shadeByName', reason: 'required' });
    }
    for (const shadeByName of ['light', ['light'], 5]) {
      expect(await refused({ shadeByName })).toMatchObject(at('shadeByName', undefined, 'type', 'expected an object'));
    }
    const optional = await nameThings({ shadeByName: {}, linksByLocale: null, pointByName: undefined });
    expect(optional.status).toBe(200);
    expect(received.at(-1)).toEqual({ id: 'p1', shadeByName: {} });
  });

  test('a value is never null and passes the checks of a list element at name[key]', async () => {
    expect(await refused({ shadeByName: { a: 'light', b: null } })).toMatchObject(at('shadeByName', 'shadeByName[b]', 'required', 'required field'));
    expect(await refused({ shadeByName: { a: 5 } })).toMatchObject(at('shadeByName', 'shadeByName[a]', 'type', 'expected a string'));
    const dim = await refused({ shadeByName: { a: 'dim' } });
    expect(dim.details).toMatchObject({ location: 'body', parameter: 'shadeByName', path: 'shadeByName[a]', reason: 'expected one of light, dark' });
    expect(await refused({ shadeByName: {}, weightByName: { w: '0.5' } })).toMatchObject(at('weightByName', 'weightByName[w]', 'type', 'expected a number'));
    expect(await refused({ shadeByName: {}, weightByName: { w: 2 } })).toMatchObject(at('weightByName', 'weightByName[w]', 'max', 'must be at most 1'));
    const point = await refused({ shadeByName: {}, pointByName: { p: { x: -1, y: 0 } } });
    expect(point.details).toEqual({ location: 'body', parameter: 'pointByName', path: 'pointByName[p]', reason: 'does not match the declared type' });
    expect(await refused({ shadeByName: {}, pointByName: { p: 'origin' } })).toMatchObject(at('pointByName', 'pointByName[p]', 'type', 'expected an object'));
  });

  test('a map of lists checks each list at name[key] and its elements at name[key][i]; list bounds do not bound it', async () => {
    const two = await nameThings({ shadeByName: {}, linksByLocale: { en: ['https://a.test', 'https://b.test'] } });
    expect(two.status).toBe(200);
    expect(await refused({ shadeByName: {}, linksByLocale: { en: null } })).toMatchObject(at('linksByLocale', 'linksByLocale[en]', 'required', 'required field'));
    expect(await refused({ shadeByName: {}, linksByLocale: { en: 'https://a.test' } })).toMatchObject(
      at('linksByLocale', 'linksByLocale[en]', 'type', 'expected an array')
    );
    expect(await refused({ shadeByName: {}, linksByLocale: { en: ['https://a.test', null] } })).toMatchObject(
      at('linksByLocale', 'linksByLocale[en][1]', 'required', 'required field')
    );
    expect(await refused({ shadeByName: {}, linksByLocale: { en: ['not a url'] } })).toMatchObject(
      at('linksByLocale', 'linksByLocale[en][0]', 'pattern', 'is not a valid Network.Url')
    );
    expect(await refused({ shadeByName: {}, linksByLocale: { en: ['http://a.test'] } })).toMatchObject(
      at('linksByLocale', 'linksByLocale[en][0]', 'pattern', 'does not match the required pattern')
    );
  });
});
