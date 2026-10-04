/*
Runtime check of the generated router for fixture-webhooks-api (run by
TestGeneratedWebhooksRouter with API_DIR set to the materialized package).
Each provider's verifier checks an HMAC-SHA256 of the body in a header, as
Stripe's and GitHub's do, and records that it ran. The assertions cover what
the Go router does with @hmacVerified: buildRouter refuses implementations
without a verifier for each provider, and a provider's verifier runs before
the rate limit, the body limit and the permission check of its routes, the
manual one included. The route reads the body the verifier read, and a
route that is not a webhook runs no verifier.
*/
import { describe, expect, test } from 'bun:test';
import { createHmac } from 'node:crypto';
import { Hono } from 'hono';
import { HttpProblem } from '@superschematic/http-runtime';
import { errorHandler, notFoundHandler, type WebhookVerifier } from '@superschematic/http-runtime/hono';

const apiDir = process.env.API_DIR;
if (!apiDir) throw new Error('API_DIR is required');
const { buildRouter } = await import(`${apiDir}/router.ts`);

const secrets: Record<string, string> = { stripe: 'whsec_stripe', github: 'whsec_github' };
const headers: Record<string, string> = { stripe: 'stripe-signature', github: 'x-hub-signature-256' };

function signatureOf(provider: string, body: string): string {
  return createHmac('sha256', secrets[provider]!).update(body).digest('hex');
}

// The providers whose verifiers ran, in order.
const verified: string[] = [];
// What the implementations received.
const received: unknown[] = [];

function verifierFor(provider: string): WebhookVerifier {
  return async (c, next) => {
    verified.push(provider);
    const body = await c.req.text();
    if (c.req.header(headers[provider]!) !== signatureOf(provider, body)) {
      throw new HttpProblem(401, 'The webhook signature does not match', { code: 'invalid_signature' });
    }
    await next();
  };
}

const implementations = {
  event: {
    getEvent: async (args: { id: string }) => ({ id: args.id, received: true }),
  },
  webhook: {
    receiveStripeEvent: async (args: { input: { id: string } }) => {
      received.push(args);
      return { id: args.input.id, received: true };
    },
    receiveGithubEvent: async (args: { id: string; action: string }) => {
      received.push(args);
      return { id: args.id, received: true };
    },
  },
  webhookVerifiers: { stripe: verifierFor('stripe'), github: verifierFor('github') },
};

function app() {
  const root = new Hono();
  root.route(
    '/',
    buildRouter(implementations, {
      authenticate: async (ctx: { headers: Headers }) =>
        ctx.headers.get('x-user') === 'github-app' ? { subject: 'github-app', permissions: ['webhooks.receive'] } : null,
      bodyLimits: { receiveStripeEvent: 64 },
      manualRoutes: {
        receiveRawGithubEvent: async (c: { req: { text(): Promise<string> }; text(body: string): Response }) => c.text(`raw ${await c.req.text()}`),
      },
    })
  );
  root.notFound(notFoundHandler());
  root.onError(errorHandler());
  return root;
}

async function post(router: Hono, provider: string, path: string, body: string, options: { signed?: boolean; user?: string } = {}) {
  const sent: Record<string, string> = { 'content-type': 'application/json', 'x-real-ip': '198.51.100.20' };
  sent[headers[provider]!] = options.signed === false ? 'forged' : signatureOf(provider, body);
  if (options.user) sent['x-user'] = options.user;
  return router.request(path, { method: 'POST', headers: sent, body });
}

async function expectRefusedSignature(response: Response) {
  expect(response.status).toBe(401);
  expect(await response.json()).toMatchObject({ status: 401, code: 'invalid_signature' });
}

describe('generated fixture-webhooks-api router', () => {
  test('buildRouter throws without a verifier for each provider', () => {
    const { webhookVerifiers: _, ...withoutVerifiers } = implementations;
    expect(() => buildRouter(withoutVerifiers)).toThrow('Implementations.webhookVerifiers for provider github is required');
    expect(() => buildRouter({ ...implementations, webhookVerifiers: { github: verifierFor('github') } })).toThrow(
      'Implementations.webhookVerifiers for provider stripe is required'
    );
  });

  test('a signed Stripe event reaches the implementation with the body the verifier read', async () => {
    const body = JSON.stringify({ id: 'evt_1', type: 'invoice.paid' });
    verified.length = 0;
    const response = await post(app(), 'stripe', '/api/webhooks/stripe', body);
    expect(response.status).toBe(200);
    expect((await response.json()).data).toEqual({ id: 'evt_1', received: true });
    expect(verified).toEqual(['stripe']);
    expect(received.at(-1)).toEqual({ input: { id: 'evt_1', type: 'invoice.paid' } });
  });

  test('the Stripe verifier runs before the rate limit and the body limit', async () => {
    const router = app();
    const body = JSON.stringify({ id: 'evt_2', type: 'invoice.paid' });
    const large = JSON.stringify({ id: 'evt_3', type: 'x'.repeat(100) });
    const before = received.length;
    await expectRefusedSignature(await post(router, 'stripe', '/api/webhooks/stripe', large, { signed: false }));
    await expectRefusedSignature(await post(router, 'stripe', '/api/webhooks/stripe', body, { signed: false }));
    expect(received.length).toBe(before);
    // The refused requests took no token of the one a minute.
    expect((await post(router, 'stripe', '/api/webhooks/stripe', body)).status).toBe(200);
    expect((await post(router, 'stripe', '/api/webhooks/stripe', body)).status).toBe(429);
    await expectRefusedSignature(await post(router, 'stripe', '/api/webhooks/stripe', body, { signed: false }));
    expect((await post(app(), 'stripe', '/api/webhooks/stripe', large)).status).toBe(413);
  });

  test('the GitHub verifier runs before the permission check', async () => {
    const router = app();
    const body = JSON.stringify({ id: 'delivery-1', action: 'opened' });
    await expectRefusedSignature(await post(router, 'github', '/api/webhooks/github', body, { signed: false }));
    const anonymous = await post(router, 'github', '/api/webhooks/github', body);
    expect(anonymous.status).toBe(401);
    expect(await anonymous.json()).toMatchObject({ code: 'unauthorized' });
    const accepted = await post(router, 'github', '/api/webhooks/github', body, { user: 'github-app' });
    expect(accepted.status).toBe(200);
    expect(received.at(-1)).toEqual({ id: 'delivery-1', action: 'opened' });
  });

  test("the manual route is verified before the service's handler, which reads the body", async () => {
    const body = JSON.stringify({ payload: 'ping' });
    verified.length = 0;
    const accepted = await post(app(), 'github', '/api/webhooks/github/raw', body);
    expect(accepted.status).toBe(200);
    expect(await accepted.text()).toBe(`raw ${body}`);
    expect(verified).toEqual(['github']);
    await expectRefusedSignature(await post(app(), 'github', '/api/webhooks/github/raw', body, { signed: false }));
  });

  test('a route that is not a webhook runs no verifier', async () => {
    verified.length = 0;
    const response = await app().request('/api/events/evt_1');
    expect(response.status).toBe(200);
    expect((await response.json()).data).toEqual({ id: 'evt_1', received: true });
    expect(verified).toEqual([]);
  });
});
