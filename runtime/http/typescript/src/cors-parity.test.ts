import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { corsDecision, corsHandler, corsMethods, matchOperations, type CorsPolicy } from './index';

/*
The CORS parity vectors (D55, section 8.10 of docs/stack-model.md), which
the Go runtime writes (`go test ./cors -run TestWriteParityVectors
-update` in runtime/http/go) and both runtimes decide with their own CORS
check. Each vector holds an API's policy, a browser's request, and the
decision: whether the server answers it itself, and the headers.
*/

const CORPUS = fileURLToPath(new URL('../../testdata/cors_parity.json', import.meta.url));

interface ParityVector {
  readonly name: string;
  readonly policy: CorsPolicy;
  readonly request: { readonly method: string; readonly headers: Readonly<Record<string, string>> };
  readonly want: { readonly preflight: boolean; readonly headers: Readonly<Record<string, string>> };
}

const corpus = JSON.parse(readFileSync(CORPUS, 'utf8')) as { readonly vectors: readonly ParityVector[] };

describe('cors parity vectors', () => {
  test('the corpus has vectors with distinct names', () => {
    expect(corpus.vectors.length).toBeGreaterThan(0);
    expect(new Set(corpus.vectors.map(vector => vector.name)).size).toBe(corpus.vectors.length);
  });

  for (const vector of corpus.vectors) {
    test(vector.name, async () => {
      expect(corsDecision(vector.policy, vector.request.method, vector.request.headers)).toEqual(vector.want);

      // The handler answers a preflight itself, 204 with the headers, and
      // hands any other request on, adding them to the answer.
      let reached = false;
      const handler = corsHandler([{ policy: vector.policy }], async () => {
        reached = true;
        return new Response('ok', { headers: { 'Content-Type': 'text/plain' } });
      });
      const response = await handler(new Request('http://shop-api.acme.dev/api/products', { method: vector.request.method, headers: vector.request.headers }));
      expect(response.status).toBe(vector.want.preflight ? 204 : 200);
      expect(reached).toBe(!vector.want.preflight);
      for (const [name, value] of Object.entries(vector.want.headers)) expect(response.headers.get(name)).toBe(value);
      expect(response.headers.has('Access-Control-Allow-Origin')).toBe('Access-Control-Allow-Origin' in vector.want.headers);
    });
  }
});

describe('corsHandler with several APIs', () => {
  const operations = [
    { method: 'GET', path: '/api/products' },
    { method: 'POST', path: '/api/carts/{cartId}/lines' },
  ];

  test('the methods of the operations, each once', () => {
    expect(corsMethods([...operations, { method: 'get', path: '/api/products/{id}' }])).toEqual(['GET', 'POST']);
  });

  test('a path parameter takes one segment', () => {
    const match = matchOperations(operations);
    expect(match('POST', '/api/carts/42/lines')).toBe(true);
    expect(match('POST', '/api/carts/42/43/lines')).toBe(false);
    expect(match('GET', '/api/carts/42/lines')).toBe(false);
  });

  test('a request is the first API whose routes take it, a preflight by the method it asks for', async () => {
    const handler = corsHandler(
      [
        { policy: { origins: ['https://a.dev'], methods: ['GET'] }, match: matchOperations([operations[0]!]) },
        { policy: { origins: ['https://b.dev'], methods: ['POST'] }, match: matchOperations([operations[1]!]) },
      ],
      () => new Response('ok')
    );
    const preflight = (path: string, origin: string, method: string) =>
      handler(new Request(`http://server${path}`, { method: 'OPTIONS', headers: { Origin: origin, 'Access-Control-Request-Method': method } }));
    expect((await preflight('/api/products', 'https://a.dev', 'GET')).headers.get('Access-Control-Allow-Origin')).toBe('https://a.dev');
    expect((await preflight('/api/carts/1/lines', 'https://a.dev', 'POST')).headers.get('Access-Control-Allow-Origin')).toBeNull();
    expect((await preflight('/api/carts/1/lines', 'https://b.dev', 'POST')).headers.get('Access-Control-Allow-Methods')).toBe('POST');
    const neither = await preflight('/api/orders', 'https://a.dev', 'GET');
    expect(neither.status).toBe(200);
    expect(neither.headers.get('Access-Control-Allow-Origin')).toBeNull();
  });
});
