/*
Sends union_requests.json's requests to the generated router for
union-lists-api (run by TestGeneratedRouterAnswersUnionRequests with API_DIR
set to the materialized package, UNION_REQUESTS to the requests and
UNION_RESPONSES to the file it writes). Its implementation answers
drawShape with the shape it received, pay with the payment's kind,
checkout with the payment's kind, a colon and the number of shapes, the
list, grid and map operations with the number of shapes, and probe with
ok. It
writes each request's name, the status and the body the router answered,
which the Go test holds to the requests' expectations.
*/
import { expect, test } from 'bun:test';
import { readFileSync, writeFileSync } from 'node:fs';
import { Hono } from 'hono';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';

const apiDir = process.env.API_DIR;
const requestsPath = process.env.UNION_REQUESTS;
const responsesPath = process.env.UNION_RESPONSES;
if (!apiDir || !requestsPath || !responsesPath) throw new Error('API_DIR, UNION_REQUESTS and UNION_RESPONSES are required');
const { buildRouter } = await import(`${apiDir}/router.ts`);

type Payment = { kind: string };

function app() {
  const root = new Hono();
  const implementations = {
    shape: {
      drawShape: async ({ shape }: { shape: unknown }) => shape,
      pay: async ({ payment }: { payment: Payment }) => payment.kind,
      checkout: async ({ input }: { input: { payment: Payment; shapes?: unknown[] } }) =>
        `${input.payment.kind}:${input.shapes?.length ?? 0}`,
      drawShapes: async ({ shapes }: { shapes: unknown[] }) => `${shapes.length}`,
      shapeGrid: async ({ rows }: { rows: unknown[][] }) => `${rows.reduce((n, row) => n + row.length, 0)}`,
      shapesByKey: async ({ byKey }: { byKey: Record<string, unknown> }) => `${Object.keys(byKey).length}`,
      probe: async () => 'ok',
      listShapes: async () => [],
      shapeRows: async () => [],
      getShape: async () => null,
      shapesByName: async () => ({}),
      shapeGroups: async () => ({}),
      findShape: async () => null,
      findShapes: async () => null,
    },
  };
  root.route('/', buildRouter(implementations));
  root.notFound(notFoundHandler());
  root.onError(errorHandler());
  return root;
}

test('the router answers every union request', async () => {
  const { requests } = JSON.parse(readFileSync(requestsPath, 'utf8')) as {
    requests: { name: string; path: string; body: unknown }[];
  };
  const responses = [];
  for (const request of requests) {
    const response = await app().request(request.path, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify(request.body),
    });
    responses.push({ name: request.name, status: response.status, body: await response.json() });
  }
  expect(responses.length).toBe(requests.length);
  writeFileSync(responsesPath, JSON.stringify(responses));
});
