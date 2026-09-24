import { describe, expect, test } from 'bun:test';
import { parseIdentityUUID } from 'superscalar/scalars';
import {
  HttpProblem,
  authorize,
  covers,
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
