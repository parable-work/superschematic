import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { Hono } from 'hono';
import {
  CLIENT_TOO_OLD,
  HttpProblem,
  clientTooOld,
  clientTooOldProblem,
  compareVersions,
  parseMinClientVersions,
  type ClientTooOld,
  type OperationSpec,
} from './index';
import { mountManualOperation, mountOperation } from './hono';

/*
The oldest version of each client an API serves (D143): each vector of
runtime/http/testdata/client_version_parity.json answered as the Go
runtime's clientversion package and the Rust runtime's client_version
module answer it, and a route mounted with minimums turning away a client
under its own before any other step.
*/

interface Parity {
  versions: { name: string; a: string; b: string; compare: -1 | 0 | 1 | null }[];
  settings: { name: string; setting: string; minimums?: Record<string, string>; refused?: boolean }[];
  decisions: { name: string; setting: string; header: string | null; tooOld: ClientTooOld | null }[];
}

const parity = JSON.parse(readFileSync(new URL('../../testdata/client_version_parity.json', import.meta.url), 'utf8')) as Parity;

describe('client versions (D143)', () => {
  for (const vector of parity.versions) {
    test(`compareVersions: ${vector.name}`, () => {
      expect(compareVersions(vector.a, vector.b) ?? null).toBe(vector.compare);
      // The order is antisymmetric.
      expect(compareVersions(vector.b, vector.a) ?? null).toBe(vector.compare === null ? null : ((0 - vector.compare) as -1 | 0 | 1));
    });
  }

  for (const vector of parity.settings) {
    test(`parseMinClientVersions: ${vector.name}`, () => {
      if (vector.refused) {
        expect(() => parseMinClientVersions(vector.setting)).toThrow(/^MIN_CLIENT_VERSIONS/);
        return;
      }
      expect({ ...parseMinClientVersions(vector.setting).minimums }).toEqual(vector.minimums!);
    });
  }

  for (const vector of parity.decisions) {
    test(`clientTooOld: ${vector.name}`, () => {
      expect(clientTooOld(parseMinClientVersions(vector.setting), vector.header) ?? null).toEqual(vector.tooOld);
    });
  }

  test('an absent setting lists no client', () => {
    expect({ ...parseMinClientVersions(undefined).minimums }).toEqual({});
    expect({ ...parseMinClientVersions(null).minimums }).toEqual({});
    expect(clientTooOld(parseMinClientVersions(undefined), 'cli/0.0.1')).toBeUndefined();
  });

  test('a client named as a member of Object is listed by its own name alone', () => {
    const minimums = parseMinClientVersions('toString=1.0.0');
    expect(clientTooOld(minimums, 'toString/0.9.0')).toEqual({ client: 'toString', version: '0.9.0', minimum: '1.0.0' });
    expect(clientTooOld(minimums, 'constructor/0.9.0')).toBeUndefined();
    expect(clientTooOld(parseMinClientVersions('cli=1.0.0'), 'toString/0.9.0')).toBeUndefined();
  });

  test('the problem is a 426 with the three details', () => {
    const problem = clientTooOldProblem({ client: 'cli', version: '0.4.0', minimum: '0.5.0' });
    expect(problem).toBeInstanceOf(HttpProblem);
    expect([problem.status, problem.code, problem.details]).toEqual([426, CLIENT_TOO_OLD, { client: 'cli', version: '0.4.0', minimum: '0.5.0' }]);
  });
});

const listCarts: OperationSpec = {
  name: 'listCarts',
  namespace: 'carts',
  method: 'GET',
  path: '/api/carts',
  pathParams: [],
  queryParams: [],
  bodyParams: [],
  auth: { public: false, required: true, permissions: ['carts.read'] },
  manual: false,
};

describe('a route of an API with minimum client versions', () => {
  const minClientVersions = parseMinClientVersions('cli=0.5.0');
  let authenticated = 0;
  const options = {
    minClientVersions,
    authenticate: async () => {
      authenticated += 1;
      return { subject: 'u1', permissions: ['carts.read'] };
    },
  };
  const app = new Hono();
  mountOperation(app, listCarts, async () => ['cart'], options);
  mountManualOperation(app, { ...listCarts, name: 'rawCarts', path: '/api/carts/raw', manual: true }, async () => new Response('raw'), options);

  test('answers a client under its minimum 426, before it authenticates anyone', async () => {
    for (const path of ['/api/carts', '/api/carts/raw']) {
      const before = authenticated;
      const response = await app.request(path, { headers: { 'X-Client-Version': 'cli/0.4.0', 'x-request-id': 'req-1' } });
      expect(response.status).toBe(426);
      expect(response.headers.get('content-type')).toBe('application/problem+json');
      expect(await response.json()).toEqual({
        type: 'about:blank',
        title: 'Upgrade Required',
        status: 426,
        detail: 'cli 0.4.0 is older than 0.5.0, the oldest version of cli this API serves. Upgrade cli.',
        code: 'client_too_old',
        requestId: 'req-1',
        details: { client: 'cli', version: '0.4.0', minimum: '0.5.0' },
      });
      expect(authenticated).toBe(before);
    }
  });

  test('serves a client at its minimum, another client, a version it cannot read and a caller that names none', async () => {
    for (const header of ['cli/0.5.0', 'desktop/0.0.1', 'cli/next', undefined]) {
      const response = await app.request('/api/carts', { headers: { authorization: 'Bearer t', ...(header ? { 'X-Client-Version': header } : {}) } });
      expect([header, response.status]).toEqual([header, 200]);
    }
  });

  test('a route mounted without minimums serves every client', async () => {
    const open = new Hono();
    mountOperation(open, { ...listCarts, auth: { public: true, required: false, permissions: [] } }, async () => ['cart']);
    expect((await open.request('/api/carts', { headers: { 'X-Client-Version': 'cli/0.0.1' } })).status).toBe(200);
  });
});
