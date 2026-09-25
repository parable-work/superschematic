import { describe, expect, test } from 'bun:test';
import { parseIdentityUUID } from 'superscalar/scalars';
import {
  HttpProblem,
  authorize,
  covers,
  decodeJsonParam,
  decodeListOfLists,
  decodeParam,
  decodeParams,
  envelopeResponse,
  hasAnyPermission,
  problemBody,
  problemResponse,
  requestIdOf,
  type ParamSpec,
} from './index';

describe('problem envelope', () => {
  test('carries the RFC 9457 members, the code, the request id, details and extensions', async () => {
    const problem = new HttpProblem(409, 'Order already shipped', {
      code: 'ORDER_SHIPPED',
      details: { orderId: 'o' },
      extensions: { current: { state: 'SHIPPED' }, status: 999 },
    });
    const body = problemBody(problem, 'req-1');
    expect(body).toEqual({
      type: 'about:blank',
      title: 'Conflict',
      status: 409,
      detail: 'Order already shipped',
      code: 'ORDER_SHIPPED',
      requestId: 'req-1',
      details: { orderId: 'o' },
      current: { state: 'SHIPPED' },
    });
    const response = problemResponse(problem, 'req-1');
    expect(response.status).toBe(409);
    expect(response.headers.get('content-type')).toBe('application/problem+json');
    expect(response.headers.get('x-request-id')).toBe('req-1');
    expect(await response.json()).toEqual(body);
  });

  test('omits code and details when unset and titles unknown statuses Error', () => {
    const body = problemBody(new HttpProblem(299, 'odd'), 'r');
    expect(body).toEqual({ type: 'about:blank', title: 'Error', status: 299, detail: 'odd', requestId: 'r' });
    expect('code' in body).toBe(false);
  });
});

describe('success envelope', () => {
  test('wraps data with meta.requestId and echoes the id as a header', async () => {
    const response = envelopeResponse({ ok: true }, 'req-2', 503, { 'x-extra': 'y' });
    expect(response.status).toBe(503);
    expect(response.headers.get('x-request-id')).toBe('req-2');
    expect(response.headers.get('x-extra')).toBe('y');
    expect(await response.json()).toEqual({ data: { ok: true }, meta: { requestId: 'req-2' } });
  });

  test('takes a sane caller request id and mints one otherwise', () => {
    expect(requestIdOf(new Request('http://x', { headers: { 'x-request-id': ' abc ' } }))).toBe('abc');
    const minted = requestIdOf(new Request('http://x'));
    expect(minted).toMatch(/^[0-9a-f-]{36}$/u);
    expect(requestIdOf(new Request('http://x', { headers: { 'x-request-id': 'x'.repeat(129) } }))).not.toBe('x'.repeat(129));
  });
});

describe('parameter decoding', () => {
  const p = (over: Partial<ParamSpec>): ParamSpec => ({ name: 'x', kind: 'string', required: true, ...over });

  test('decodes each scalar kind', () => {
    expect(decodeParam('query', p({ kind: 'integer' }), ['42'])).toBe(42);
    expect(decodeParam('query', p({ kind: 'number' }), ['4.5'])).toBe(4.5);
    expect(decodeParam('query', p({ kind: 'boolean' }), ['true'])).toBe(true);
    expect(decodeParam('query', p({ kind: 'boolean' }), ['0'])).toBe(false);
    expect(decodeParam('path', p({ kind: 'uuid' }), ['00000000-0000-4000-8000-0000000000AB'])).toBe(
      parseIdentityUUID('00000000-0000-4000-8000-0000000000AB')
    );
    expect(decodeParam('query', p({ kind: 'datetime' }), ['2026-01-02T03:04:05Z'])).toEqual(new Date('2026-01-02T03:04:05Z'));
    expect(decodeParam('query', p({ kind: 'enum', enumValues: ['active', 'suspended'] }), ['active'])).toBe('active');
    expect(decodeParam('query', p({ kind: 'string' }), ['hi'])).toBe('hi');
  });

  test('refuses malformed values with a 400 that names the parameter', () => {
    const cases: Array<[Partial<ParamSpec>, string]> = [
      [{ kind: 'integer' }, '4.5'],
      [{ kind: 'number' }, 'abc'],
      [{ kind: 'boolean' }, 'maybe'],
      [{ kind: 'uuid' }, 'not-a-uuid'],
      [{ kind: 'datetime' }, 'yesterday'],
      [{ kind: 'enum', enumValues: ['a'] }, 'b'],
      [{ kind: 'integer', min: 1 }, '0'],
      [{ kind: 'string', maxLength: 2 }, 'abc'],
      [{ kind: 'string', pattern: '^[a-z]+$' }, 'A1'],
    ];
    for (const [over, raw] of cases) {
      expect(() => decodeParam('query', p(over), [raw])).toThrow(
        expect.objectContaining({ status: 400, code: 'bad_request', details: expect.objectContaining({ parameter: 'x' }) })
      );
    }
  });

  test('handles absence: required refuses, optional yields undefined, defaults apply', () => {
    expect(() => decodeParam('query', p({}), undefined)).toThrow(expect.objectContaining({ status: 400 }));
    expect(decodeParam('query', p({ required: false }), undefined)).toBeUndefined();
    expect(decodeParam('query', p({ required: false, kind: 'integer', defaultValue: '7' }), undefined)).toBe(7);
  });

  test('arrays accept repeated keys and comma lists within list bounds', () => {
    const list = p({ kind: 'uuid', isArray: true, listMin: 1, listMax: 2 });
    const a = '00000000-0000-4000-8000-000000000001';
    const b = '00000000-0000-4000-8000-000000000002';
    expect(decodeParam('query', list, [a, b])).toEqual([parseIdentityUUID(a), parseIdentityUUID(b)]);
    expect(decodeParam('query', list, [`${a},${b}`])).toEqual([parseIdentityUUID(a), parseIdentityUUID(b)]);
    expect(() => decodeParam('query', list, [a, b, a])).toThrow(expect.objectContaining({ status: 400 }));
    expect(() => decodeParam('query', list, [])).toThrow(expect.objectContaining({ status: 400 }));
    expect(decodeParam('query', p({ isArray: true, required: false }), undefined)).toBeUndefined();
  });

  test('decodeParams keys results by wire name and drops absent optionals', () => {
    const decoded = decodeParams('query', [p({ name: 'a', kind: 'integer' }), p({ name: 'b', required: false })], name =>
      name === 'a' ? ['1'] : undefined
    );
    expect(decoded).toEqual({ a: 1 });
  });
});

describe('list of lists decoding', () => {
  const grid = (over: Partial<ParamSpec> = {}): ParamSpec => ({ name: 'rows', kind: 'string', required: true, isArray: true, isArrayOfArrays: true, ...over });
  const refusedAt = (spec: ParamSpec, value: unknown, path: string, validator: string, message: string) => {
    expect(() => decodeListOfLists('body', spec, value)).toThrow(
      expect.objectContaining({
        status: 400,
        code: 'bad_request',
        message: `Invalid body parameter ${path}: ${message}`,
        details: { location: 'body', parameter: spec.name, path, reason: message, errors: [{ validator, message }] },
      })
    );
  };

  test('keeps ragged and empty inner lists, and an empty outer list', () => {
    expect(decodeListOfLists('body', grid(), [['a', 'b'], [], ['c']])).toEqual([['a', 'b'], [], ['c']]);
    expect(decodeListOfLists('body', grid(), [])).toEqual([]);
  });

  test('the outer list is the parameter: required, an array, within listMin and listMax', () => {
    expect(() => decodeListOfLists('body', grid(), undefined)).toThrow(expect.objectContaining({ status: 400, details: expect.objectContaining({ parameter: 'rows', reason: 'required' }) }));
    expect(() => decodeListOfLists('body', grid(), null)).toThrow(expect.objectContaining({ status: 400 }));
    expect(decodeListOfLists('body', grid({ required: false }), undefined)).toBeUndefined();
    expect(() => decodeListOfLists('body', grid(), 'a,b')).toThrow(
      expect.objectContaining({ details: { location: 'body', parameter: 'rows', reason: 'expected an array', errors: [{ validator: 'type', message: 'expected an array' }] } })
    );
    const bounded = grid({ listMin: 1, listMax: 2 });
    expect(() => decodeListOfLists('body', bounded, [])).toThrow(expect.objectContaining({ details: expect.objectContaining({ reason: 'expected at least 1 values' }) }));
    expect(() => decodeListOfLists('body', bounded, [[], [], []])).toThrow(expect.objectContaining({ details: expect.objectContaining({ reason: 'expected at most 2 values' }) }));
    // Bounds apply to the outer list only.
    expect(decodeListOfLists('body', bounded, [['a', 'b', 'c', 'd']])).toEqual([['a', 'b', 'c', 'd']]);
  });

  test('an inner list is never null and must be an array, reported at name[i]', () => {
    refusedAt(grid(), [['a'], null], 'rows[1]', 'required', 'required field');
    refusedAt(grid(), [['a'], 'b'], 'rows[1]', 'type', 'expected an array');
    refusedAt(grid(), [{ 0: 'a' }], 'rows[0]', 'type', 'expected an array');
  });

  test('elements are never null and arrive as their JSON type, reported at name[i][j]', () => {
    refusedAt(grid(), [['a'], ['b', null]], 'rows[1][1]', 'required', 'required field');
    refusedAt(grid(), [['a', 1]], 'rows[0][1]', 'type', 'expected a string');
    refusedAt(grid({ kind: 'integer' }), [[1, '2']], 'rows[0][1]', 'type', 'expected an integer');
    refusedAt(grid({ kind: 'number' }), [[true]], 'rows[0][0]', 'type', 'expected a number');
    refusedAt(grid({ kind: 'boolean' }), [['true']], 'rows[0][0]', 'type', 'expected a boolean');
  });

  test('elements pass the checks of a T[] element, reported at name[i][j]', () => {
    const shade = grid({ kind: 'enum', enumValues: ['light', 'dark'] });
    expect(decodeListOfLists('body', shade, [['light'], ['dark', 'light']])).toEqual([['light'], ['dark', 'light']]);
    expect(() => decodeListOfLists('body', shade, [['light'], ['dark', 'dim']])).toThrow(
      expect.objectContaining({ message: 'Invalid body parameter rows[1][1]: expected one of light, dark', details: expect.objectContaining({ parameter: 'rows', path: 'rows[1][1]' }) })
    );
    expect(() => decodeListOfLists('body', grid({ kind: 'integer', max: 9 }), [[1], [2, 10]])).toThrow(
      expect.objectContaining({ details: expect.objectContaining({ path: 'rows[1][1]', reason: 'must be at most 9' }) })
    );
    expect(() => decodeListOfLists('body', grid({ kind: 'integer' }), [[1.5]])).toThrow(expect.objectContaining({ details: expect.objectContaining({ path: 'rows[0][0]' }) }));
    expect(() => decodeListOfLists('body', grid({ pattern: '^[a-z]+$' }), [['ok', 'A1']])).toThrow(expect.objectContaining({ details: expect.objectContaining({ path: 'rows[0][1]' }) }));
    const id = '00000000-0000-4000-8000-0000000000AB';
    expect(decodeListOfLists('body', grid({ kind: 'uuid' }), [[id]])).toEqual([[parseIdentityUUID(id)]]);
    expect(decodeListOfLists('body', grid({ kind: 'datetime' }), [['2026-01-02T03:04:05Z']])).toEqual([[new Date('2026-01-02T03:04:05Z')]]);
    expect(decodeListOfLists('body', grid({ kind: 'number' }), [[1.5, -2]])).toEqual([[1.5, -2]]);
    expect(decodeListOfLists('body', grid({ kind: 'boolean' }), [[true, false]])).toEqual([[true, false]]);
  });

  test('object elements go through the generated parser the spec carries', () => {
    const parse = (value: unknown) => {
      const point = value as { x?: unknown };
      if (typeof point.x !== 'number') throw new Error('parsePoint json validation failed');
      return { ...point, parsed: true };
    };
    const points = grid({ kind: 'object', parse });
    expect(decodeListOfLists('body', points, [[{ x: 1 }], []])).toEqual([[{ x: 1, parsed: true }], []]);
    expect(() => decodeListOfLists('body', points, [[{ x: 1 }, { x: 'far' }]])).toThrow(
      expect.objectContaining({ details: { location: 'body', parameter: 'rows', path: 'rows[0][1]', reason: 'does not match the declared type' } })
    );
    refusedAt(points, [[{ x: 1 }], [null]], 'rows[1][0]', 'required', 'required field');
    refusedAt(points, [[[1]]], 'rows[0][0]', 'type', 'expected an object');
  });
});

describe('JSON body parameters of an object type', () => {
  const parse = (value: unknown) => {
    const point = value as { x?: unknown };
    if (typeof point.x !== 'number') throw new Error('parsePoint json validation failed');
    return { ...point, parsed: true };
  };
  const points = (over: Partial<ParamSpec> = {}): ParamSpec => ({ name: 'points', kind: 'object', required: true, isArray: true, parse, ...over });
  const refusedAt = (spec: ParamSpec, value: unknown, path: string, validator: string, message: string) => {
    expect(() => decodeJsonParam('body', spec, value)).toThrow(
      expect.objectContaining({
        status: 400,
        code: 'bad_request',
        message: `Invalid body parameter ${path}: ${message}`,
        details: { location: 'body', parameter: spec.name, path, reason: message, errors: [{ validator, message }] },
      })
    );
  };

  test('a T[] element goes through the parser the spec carries and stays an object', () => {
    expect(decodeJsonParam('body', points(), [{ x: 1 }, { x: 2 }])).toEqual([{ x: 1, parsed: true }, { x: 2, parsed: true }]);
    expect(decodeJsonParam('body', points(), [])).toEqual([]);
  });

  test('the list is the parameter: required means present, listMin and listMax bound it', () => {
    expect(() => decodeJsonParam('body', points(), undefined)).toThrow(expect.objectContaining({ details: { location: 'body', parameter: 'points', reason: 'required' } }));
    expect(() => decodeJsonParam('body', points(), null)).toThrow(expect.objectContaining({ details: { location: 'body', parameter: 'points', reason: 'required' } }));
    expect(decodeJsonParam('body', points({ required: false }), undefined)).toBeUndefined();
    expect(decodeJsonParam('body', points({ required: false }), null)).toBeUndefined();
    expect(() => decodeJsonParam('body', points(), { x: 1 })).toThrow(
      expect.objectContaining({ details: { location: 'body', parameter: 'points', reason: 'expected an array', errors: [{ validator: 'type', message: 'expected an array' }] } })
    );
    const bounded = points({ listMin: 1, listMax: 2 });
    expect(() => decodeJsonParam('body', bounded, [])).toThrow(expect.objectContaining({ details: expect.objectContaining({ reason: 'expected at least 1 values' }) }));
    expect(() => decodeJsonParam('body', bounded, [{ x: 1 }, { x: 2 }, { x: 3 }])).toThrow(expect.objectContaining({ details: expect.objectContaining({ reason: 'expected at most 2 values' }) }));
  });

  test('an element is never null, must be an object and must parse, reported at name[i]', () => {
    refusedAt(points(), [{ x: 1 }, null], 'points[1]', 'required', 'required field');
    refusedAt(points({ required: false }), [null], 'points[0]', 'required', 'required field');
    refusedAt(points(), [{ x: 1 }, 'p'], 'points[1]', 'type', 'expected an object');
    refusedAt(points(), [[1, 2]], 'points[0]', 'type', 'expected an object');
    expect(() => decodeJsonParam('body', points(), [{ x: 1 }, { x: 'far' }])).toThrow(
      expect.objectContaining({ message: 'Invalid body parameter points[1]: does not match the declared type', details: { location: 'body', parameter: 'points', path: 'points[1]', reason: 'does not match the declared type' } })
    );
  });

  test('a single object value is parsed, and refused without a path', () => {
    const origin = points({ name: 'origin', isArray: false });
    expect(decodeJsonParam('body', origin, { x: 1 })).toEqual({ x: 1, parsed: true });
    expect(() => decodeJsonParam('body', origin, null)).toThrow(expect.objectContaining({ details: { location: 'body', parameter: 'origin', reason: 'required' } }));
    expect(() => decodeJsonParam('body', origin, 'p')).toThrow(
      expect.objectContaining({ details: { location: 'body', parameter: 'origin', reason: 'expected an object', errors: [{ validator: 'type', message: 'expected an object' }] } })
    );
    expect(() => decodeJsonParam('body', origin, { x: 'far' })).toThrow(
      expect.objectContaining({ details: { location: 'body', parameter: 'origin', reason: 'does not match the declared type' } })
    );
  });

  test('a list of lists goes through decodeListOfLists', () => {
    const polygons = points({ name: 'polygons', isArrayOfArrays: true });
    expect(decodeJsonParam('body', polygons, [[{ x: 1 }], []])).toEqual([[{ x: 1, parsed: true }], []]);
    refusedAt(polygons, [[{ x: 1 }], null], 'polygons[1]', 'required', 'required field');
  });
});

describe('JSON body parameters of a scalar, enum or JSON type', () => {
  const p = (over: Partial<ParamSpec>): ParamSpec => ({ name: 'x', kind: 'string', required: true, ...over });
  const refusedAt = (spec: ParamSpec, value: unknown, path: string | undefined, validator: string, message: string) => {
    expect(() => decodeJsonParam('body', spec, value)).toThrow(
      expect.objectContaining({
        status: 400,
        code: 'bad_request',
        message: `Invalid body parameter ${path ?? spec.name}: ${message}`,
        details: { location: 'body', parameter: spec.name, ...(path !== undefined ? { path } : {}), reason: message, errors: [{ validator, message }] },
      })
    );
  };

  test('a list element is one JSON value: never split on commas, an empty string kept', () => {
    expect(decodeJsonParam('body', p({ isArray: true }), ['a,b', '', ' c '])).toEqual(['a,b', '', ' c ']);
    expect(decodeJsonParam('body', p({ isArray: true }), [])).toEqual([]);
    expect(decodeJsonParam('body', p({ kind: 'number', isArray: true }), [1.5, -2, 0])).toEqual([1.5, -2, 0]);
    expect(decodeJsonParam('body', p({ kind: 'integer', isArray: true }), [1, 2.0])).toEqual([1, 2]);
    expect(decodeJsonParam('body', p({ kind: 'boolean', isArray: true }), [true, false])).toEqual([true, false]);
    expect(decodeJsonParam('body', p({ kind: 'enum', enumValues: ['light', 'dark'], isArray: true }), ['dark'])).toEqual(['dark']);
  });

  test('a list element is never null and must arrive as its JSON type, reported at name[i]', () => {
    refusedAt(p({ isArray: true }), ['a', null], 'x[1]', 'required', 'required field');
    refusedAt(p({ isArray: true, required: false }), [null], 'x[0]', 'required', 'required field');
    refusedAt(p({ isArray: true }), ['a', 5], 'x[1]', 'type', 'expected a string');
    refusedAt(p({ isArray: true }), [{ a: 1 }], 'x[0]', 'type', 'expected a string');
    refusedAt(p({ kind: 'number', isArray: true }), [1, '5'], 'x[1]', 'type', 'expected a number');
    refusedAt(p({ kind: 'integer', isArray: true }), [1.5], 'x[0]', 'type', 'expected an integer');
    refusedAt(p({ kind: 'integer', isArray: true }), ['5'], 'x[0]', 'type', 'expected an integer');
    refusedAt(p({ kind: 'boolean', isArray: true }), ['true'], 'x[0]', 'type', 'expected a boolean');
    refusedAt(p({ kind: 'enum', enumValues: ['light'], isArray: true }), [1], 'x[0]', 'type', 'expected a string');
    expect(() => decodeJsonParam('body', p({ kind: 'integer', isArray: true }), [2 ** 60])).toThrow(
      expect.objectContaining({ details: expect.objectContaining({ path: 'x[0]', reason: 'integer out of range' }) })
    );
    expect(() => decodeJsonParam('body', p({ kind: 'enum', enumValues: ['light'], isArray: true }), ['dim'])).toThrow(
      expect.objectContaining({ details: expect.objectContaining({ path: 'x[0]', reason: 'expected one of light' }) })
    );
  });

  test('a single value must arrive as its JSON type; absent or null is required or undefined, or its default', () => {
    expect(decodeJsonParam('body', p({}), 'a,b')).toBe('a,b');
    refusedAt(p({}), 5, undefined, 'type', 'expected a string');
    refusedAt(p({ kind: 'number' }), '5', undefined, 'type', 'expected a number');
    refusedAt(p({ kind: 'boolean' }), 1, undefined, 'type', 'expected a boolean');
    expect(() => decodeJsonParam('body', p({}), null)).toThrow(expect.objectContaining({ details: { location: 'body', parameter: 'x', reason: 'required' } }));
    expect(decodeJsonParam('body', p({ required: false }), null)).toBeUndefined();
    expect(decodeJsonParam('body', p({ required: false, kind: 'integer', defaultValue: '7' }), undefined)).toBe(7);
    expect(decodeJsonParam('body', p({ required: false, isArray: true, defaultValue: 'a,b' }), null)).toEqual(['a', 'b']);
    expect(decodeJsonParam('body', p({ required: false, kind: 'object', parse: value => value, defaultValue: '{}' }), undefined)).toBeUndefined();
  });

  test('a JSON value is any JSON value but null, and a null list element is required', () => {
    const json = p({ kind: 'json' });
    for (const value of [{ a: [1, null] }, [1, 'a'], 'text', '', 0, false]) {
      expect(decodeJsonParam('body', json, value)).toEqual(value);
    }
    expect(() => decodeJsonParam('body', json, null)).toThrow(expect.objectContaining({ details: { location: 'body', parameter: 'x', reason: 'required' } }));
    expect(decodeJsonParam('body', p({ kind: 'json', required: false }), null)).toBeUndefined();
    const list = p({ kind: 'json', isArray: true });
    expect(decodeJsonParam('body', list, [1, 'a', { b: 2 }, [3, null], true])).toEqual([1, 'a', { b: 2 }, [3, null], true]);
    refusedAt(list, [1, null], 'x[1]', 'required', 'required field');
    refusedAt(p({ kind: 'json', isArray: true, isArrayOfArrays: true }), [[{}], null], 'x[1]', 'required', 'required field');
    refusedAt(p({ kind: 'json', isArray: true, isArrayOfArrays: true }), [[{}, null]], 'x[0][1]', 'required', 'required field');
  });

  test("a scalar's own constraints apply to every value, each failure one error named by its rule", () => {
    const url = { name: 'Network.Url', maxLength: 24, pattern: '^https?://[a-z.]+$' };
    const urls = p({ isArray: true, scalar: url, listMin: 1, listMax: 2 });
    expect(decodeJsonParam('body', urls, ['https://a.test'])).toEqual(['https://a.test']);
    refusedAt(urls, ['https://a.test', 'not a url'], 'x[1]', 'pattern', 'is not a valid Network.Url');
    refusedAt(urls, [`https://${'a'.repeat(20)}.test`], 'x[0]', 'maxLength', 'must be at most 24 characters');
    expect(() => decodeJsonParam('body', urls, [])).toThrow(expect.objectContaining({ details: expect.objectContaining({ reason: 'expected at least 1 values' }) }));
    // Lengths count code points: one emoji is one character, as in the schema runtimes.
    refusedAt(p({ scalar: { name: 'Identity.Name', minLength: 2 } }), '\u{1F600}', undefined, 'minLength', 'must be at least 2 characters');
    const ranks = p({ kind: 'integer', isArray: true, scalar: { name: 'Ordering.Rank', min: 1 } });
    refusedAt(ranks, [2, -1], 'x[1]', 'min', 'must be at least 1');
    refusedAt(p({ kind: 'number', scalar: { name: 'Generic.Probability', min: 0, max: 1 } }), 1.5, undefined, 'max', 'must be at most 1');
    // The argument's own constraints apply after the scalar's, and name their rule too.
    refusedAt(p({ isArray: true, scalar: url, pattern: '^https://' }), ['http://a.test'], 'x[0]', 'pattern', 'does not match the required pattern');
    refusedAt(p({ maxLength: 2 }), 'abc', undefined, 'maxLength', 'must be at most 2 characters');
    // A path or query value is checked against its scalar too.
    expect(() => decodeParam('query', p({ scalar: url }), ['nope'])).toThrow(
      expect.objectContaining({ details: expect.objectContaining({ parameter: 'x', reason: 'is not a valid Network.Url', errors: [{ validator: 'pattern', message: 'is not a valid Network.Url' }] }) })
    );
  });
});

describe('permission gate', () => {
  test('mirrors the Go session runtime: exact and dotted prefix cover, no root permission', () => {
    expect(covers('reports', 'reports.export')).toBe(true);
    expect(covers('report', 'reports.export')).toBe(false);
    expect(hasAnyPermission(['admin'], ['orders.read'])).toBe(false);
    expect(hasAnyPermission(['orders.read'], ['orders.read'])).toBe(true);
    expect(hasAnyPermission(['order.user'], ['order.user.view'])).toBe(true);
    expect(hasAnyPermission(['order.user.view'], ['order.user'])).toBe(false);
    expect(hasAnyPermission(['orders.write'], ['orders.read', 'orders.write'])).toBe(true);
    expect(hasAnyPermission([], [])).toBe(true);
    expect(hasAnyPermission([], ['x'])).toBe(false);
  });

  test('authorize: public and unauthenticated routes pass, missing principal is 401, uncovered permission is 403', () => {
    expect(() => authorize(null, { public: true, required: false, permissions: [] })).not.toThrow();
    expect(() => authorize(null, { public: false, required: false, permissions: [] })).not.toThrow();
    expect(() => authorize(null, { public: false, required: true, permissions: [] })).toThrow(expect.objectContaining({ status: 401, code: 'unauthorized' }));
    const user = { subject: 'u', permissions: ['orders.read'] };
    expect(() => authorize(user, { public: false, required: true, permissions: ['orders.write'] })).toThrow(expect.objectContaining({ status: 403, code: 'forbidden' }));
    expect(() => authorize(user, { public: false, required: true, permissions: ['orders.read'] })).not.toThrow();
  });

  test('authorize takes a PermissionMatcher for a project-specific vocabulary', () => {
    const root = { subject: 'r', permissions: ['root'] };
    const rootCoversAll = (held: readonly string[], required: readonly string[]) => held.includes('root') || hasAnyPermission(held, required);
    const auth = { public: false, required: true, permissions: ['orders.write'] };
    expect(() => authorize(root, auth)).toThrow(expect.objectContaining({ status: 403 }));
    expect(() => authorize(root, auth, rootCoversAll)).not.toThrow();
  });
});
