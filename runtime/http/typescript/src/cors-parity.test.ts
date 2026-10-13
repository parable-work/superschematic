import { describe, expect, test } from 'bun:test';
import { Hono } from 'hono';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { corsDecision, corsHandler, corsMethods, matchOperations, type CorsPolicy } from './index';
import { identityCorsRoutes } from './identity/index';

/*
The CORS parity vectors (D55, section 8.10 of docs/stack-model.md), which
the Go runtime writes (`go test ./cors -run TestWriteParityVectors
-update` in runtime/http/go) and both runtimes decide with their own CORS
check. Each vector holds an API's policy, a browser's request, and the
decision: whether it is a preflight, whether the server answers it itself,
and the headers.
*/

const CORPUS = fileURLToPath(new URL('../../testdata/cors_parity.json', import.meta.url));

interface ParityVector {
  readonly name: string;
  readonly policy: CorsPolicy;
  readonly request: { readonly method: string; readonly headers: Readonly<Record<string, string>> };
  readonly want: { readonly preflight: boolean; readonly answer: boolean; readonly headers: Readonly<Record<string, string>> };
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

      // The handler answers a listed origin's preflight itself, 204 with
      // the headers, and hands any other request on, adding them to the
      // answer.
      let reached = false;
      const handler = corsHandler([{ policy: vector.policy }], async () => {
        reached = true;
        return new Response('ok', { headers: { 'Content-Type': 'text/plain' } });
      });
      const response = await handler(new Request('http://shop-api.acme.dev/api/products', { method: vector.request.method, headers: vector.request.headers }));
      expect(response.status).toBe(vector.want.answer ? 204 : 200);
      expect(reached).toBe(!vector.want.answer);
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
    const unlisted = await preflight('/api/carts/1/lines', 'https://a.dev', 'POST');
    expect(unlisted.status).toBe(200);
    expect(unlisted.headers.get('Access-Control-Allow-Origin')).toBeNull();
    expect((await preflight('/api/carts/1/lines', 'https://b.dev', 'POST')).headers.get('Access-Control-Allow-Methods')).toBe('POST');
    const neither = await preflight('/api/orders', 'https://a.dev', 'GET');
    expect(neither.status).toBe(200);
    expect(neither.headers.get('Access-Control-Allow-Origin')).toBeNull();
  });
});

describe('corsHandler around an identity API', () => {
  /*
  An API that a site calls and whose identity config trusts another
  origin, as a generated entrypoint wires them (D55, amended): corsHandler
  around the server's app, and identityCorsRoutes on the API's routes.
  corsHandler answers the site's preflight with its policy, and hands the
  trusted origin's on, which the identity runtime answers with the method
  and headers it asks for. An origin neither lists gets no
  Access-Control-Allow-Origin from either: its preflight reaches the app,
  which finds no route for it.
  */
  const SITE = 'https://shop-web.acme.dev';
  const TRUSTED = 'https://admin.acme.dev';
  const OTHER = 'https://evil.example';
  const operations = [
    { method: 'GET', path: '/api/products' },
    { method: 'POST', path: '/api/auth/login' },
  ];
  const app = new Hono();
  const cors = identityCorsRoutes(app, { config: { trustedOrigins: [TRUSTED] } });
  for (const op of operations) {
    cors(op);
    app.on(op.method, op.path, c => c.json({ ok: true }));
  }
  const handler = corsHandler([{ policy: { origins: [SITE], methods: corsMethods(operations) }, match: matchOperations(operations) }], (request: Request) =>
    app.fetch(request)
  );
  const send = (method: string, path: string, origin: string, requested?: string) =>
    handler(
      new Request(`http://shop-api.acme.dev${path}`, {
        method,
        headers: {
          Origin: origin,
          ...(requested === undefined ? {} : { 'Access-Control-Request-Method': requested, 'Access-Control-Request-Headers': 'content-type' }),
        },
      })
    );
  const answer = (response: Response) => ({
    status: response.status,
    origin: response.headers.get('Access-Control-Allow-Origin'),
    credentials: response.headers.get('Access-Control-Allow-Credentials'),
    methods: response.headers.get('Access-Control-Allow-Methods'),
    headers: response.headers.get('Access-Control-Allow-Headers'),
  });

  test("the site's preflight gets the site's answer", async () => {
    expect(answer(await send('OPTIONS', '/api/auth/login', SITE, 'POST'))).toEqual({
      status: 204,
      origin: SITE,
      credentials: 'true',
      methods: 'GET, POST',
      headers: 'Accept, Authorization, Content-Type',
    });
  });

  test("the trusted origin's preflight reaches the identity runtime, which answers it", async () => {
    expect(answer(await send('OPTIONS', '/api/auth/login', TRUSTED, 'POST'))).toEqual({
      status: 204,
      origin: TRUSTED,
      credentials: 'true',
      methods: 'POST',
      headers: 'content-type',
    });
  });

  test('each origin allowed gets its requests answered with its origin, and another none', async () => {
    for (const origin of [SITE, TRUSTED]) {
      expect(answer(await send('GET', '/api/products', origin))).toMatchObject({ status: 200, origin, credentials: 'true' });
    }
    const other = await send('GET', '/api/products', OTHER);
    expect(answer(other)).toMatchObject({ status: 200, origin: null, credentials: null });
    expect(other.headers.get('Vary')).toBe('Origin');
    expect(answer(await send('OPTIONS', '/api/auth/login', OTHER, 'POST'))).toMatchObject({ status: 404, origin: null, credentials: null });
  });
});
