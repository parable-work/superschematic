// The caller of the calls fixture: shop-storefront, built as a generated
// fetch.ts builds a server on Workers (D64, sections 18.2, 18.5, 18.7 and
// 18.10 of docs/stack-model.md), over the runtime's built entries.
//
// When it loads, it reads its endpoint of shop-pricing and its bucket from
// its string bindings. On an isolate's first request it takes the service
// binding and the r2_bucket binding by the names those values give, and
// builds its Hono app once. Its routes:
//   - /quotes/:sku calls shop-pricing through the binding with a token
//     signed with the edge's key, which the callee accepts;
//   - /unsigned/:sku calls it through the same binding with no credential,
//     and /forged/:sku with a token signed by a key the callee does not
//     know, which the callee refuses;
//   - /self calls an API the server serves itself in process, as fetch.ts
//     does: the client's fetch hands the request to the server's own app;
//   - /images/:name puts an object through the binding, reads it back,
//     lists it and signs a PUT URL for it with the edge's R2 credential.
// Its test handler calls each and checks the answers.
import { env } from 'cloudflare:workers';
import { Hono } from 'hono';
import { loadBucket, loadService, serviceCredentialFor, signedTokenSource } from '../../../../dist/index.js';
import { openR2Bucket } from '../../../../dist/r2.js';
import { binding, perIsolate, requestIP, serviceFetch, stackEnvOf } from '../../../../dist/workers.js';

const vars = stackEnvOf(env);
const pricing = loadService('SHOP_PRICING_SERVICE', vars);
const media = loadBucket('SHOP_MEDIA_BUCKET', vars);

/** What answers a request, once the isolate has built it. */
type Handle = (request: Request) => Response | Promise<Response>;

/** The server's own app, once built: where a call in process goes. */
let self: Handle | undefined;

/** A fetch that hands each request to the server's own app. */
const inProcess = (async (input: RequestInfo | URL, init?: RequestInit) => {
  if (self === undefined) throw new Error('the server called an API it serves before the isolate built it');
  return self(new Request(input, init));
}) as typeof fetch;

function bindingName(field: string, name: string | undefined): string {
  if (!name) throw new Error(`${field} names no binding`);
  return name;
}

/** A PKCS#8 PEM of a fresh Ed25519 key, which the callee does not know. */
async function strangerKey(): Promise<string> {
  const pair = (await crypto.subtle.generateKey({ name: 'Ed25519' }, true, ['sign', 'verify'])) as CryptoKeyPair;
  const der = new Uint8Array((await crypto.subtle.exportKey('pkcs8', pair.privateKey)) as ArrayBuffer);
  const base64 = btoa(String.fromCharCode(...der));
  return `-----BEGIN PRIVATE KEY-----\n${base64}\n-----END PRIVATE KEY-----\n`;
}

const server = perIsolate(async (): Promise<Handle> => {
  const callPricing = serviceFetch(binding(env, bindingName('SHOP_PRICING_SERVICE', pricing.binding?.name), 'service'));
  const credential = serviceCredentialFor(pricing.credential);
  if (!credential) throw new Error('the endpoint names no credential');
  const bucket = openR2Bucket(binding(env, bindingName('SHOP_MEDIA_BUCKET', media.binding), 'r2'), media, {
    now: () => new Date('2026-10-20T12:00:00Z'),
  });
  const quote = async (sku: string, headers: Record<string, string>) => {
    const answer = await callPricing(`${pricing.url}/quotes/${sku}`, { headers });
    return { status: answer.status, body: await answer.json() };
  };
  const bearer = async (token: string) => Object.fromEntries(credential.headers.map(header => [header, `Bearer ${token}`]));

  const app = new Hono();
  app.get('/healthz', c => c.json({ status: 'ok' }));
  app.get('/quotes/:sku', async c => c.json(await quote(c.req.param('sku'), await bearer(await credential.token(false)))));
  app.get('/unsigned/:sku', async c => c.json(await quote(c.req.param('sku'), {})));
  app.get('/forged/:sku', async c => {
    const forged = signedTokenSource(await strangerKey(), { issuer: 'shop-storefront', subject: 'shop-storefront', audience: 'shop-pricing' });
    return c.json(await quote(c.req.param('sku'), await bearer(await forged(false))));
  });
  app.get('/self', async c => {
    const answer = await inProcess('https://shop-storefront/healthz');
    return c.json({ status: answer.status, body: await answer.json() });
  });
  app.put('/images/:name', async c => {
    const name = `images/${c.req.param('name')}`;
    await bucket.put(name, new Uint8Array(await c.req.arrayBuffer()), { contentType: 'image/png' });
    const read = await bucket.get(name);
    const page = await bucket.list({ prefix: 'images/' });
    const signed = new URL(await bucket.signedUrl(name, { method: 'PUT', expiresInSeconds: 600, contentType: 'image/png' }));
    return c.json({
      read: await new Response(read.body).text(),
      contentType: read.object.contentType,
      listed: page.objects.map(object => object.name),
      url: `${signed.origin}${signed.pathname}`,
      credential: signed.searchParams.get('X-Amz-Credential'),
      expires: signed.searchParams.get('X-Amz-Expires'),
      signature: signed.searchParams.get('X-Amz-Signature')?.length,
    });
  });
  self = request => app.fetch(request, { requestIP });
  return self;
});

const handler = {
  async fetch(request: Request): Promise<Response> {
    return (await server())(request);
  },

  async test(): Promise<void> {
    const failures: string[] = [];
    const expect = (what: string, got: unknown, want: unknown) => {
      if (JSON.stringify(got) !== JSON.stringify(want)) failures.push(`${what}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
    };
    const get = async (path: string, init?: RequestInit) => {
      const answer = await handler.fetch(new Request(`https://shop-storefront${path}`, init));
      expect(`${path} status`, answer.status, 200);
      return answer.json();
    };

    expect('a signed call', await get('/quotes/tea'), {
      status: 200,
      body: { sku: 'tea', cents: 250, caller: 'shop-storefront', serves: ['shop-storefront'] },
    });
    const unsigned = (await get('/unsigned/tea')) as { status: number; body: { code?: string; detail?: string } };
    expect('an unsigned call', [unsigned.status, unsigned.body.detail], [401, 'Service credential required']);
    const forged = (await get('/forged/tea')) as { status: number; body: { code?: string; detail?: string } };
    expect('a call signed by another key', [forged.status, forged.body.detail], [401, 'Invalid service credential']);
    expect('a call in process', await get('/self'), { status: 200, body: { status: 'ok' } });
    expect('the bucket', await get('/images/tea.png', { method: 'PUT', body: 'a tea' }), {
      read: 'a tea',
      contentType: 'image/png',
      listed: ['images/tea.png'],
      url: 'https://0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com/calls-stack-shop-media/images/tea.png',
      credential: '8f2b4d6e0a1c3e5f7b9d1f3a5c7e9b0d/20261020/auto/s3/aws4_request',
      expires: '600',
      signature: 64,
    });
    if (failures.length > 0) throw new Error(`calls fixture:\n${failures.join('\n')}`);
    console.log('calls fixture: ok');
  },
};

export default handler;
