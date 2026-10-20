// A Worker built as a generated fetch.ts builds one (D64, section 18.5 of
// docs/stack-model.md), over the runtime's built entries, which
// scripts/workerd-test.mjs bundles for workerd and runs with `workerd test`
// beside the stand-ins of the bindings workerd does not provide alone.
//
// At module scope it reads its config's strings from the env, so a refused
// config would fail the upload. On the first request of the isolate it
// awaits its secret, takes its D1, R2 and service bindings by the names its
// config gives, and builds a Hono app, once. A request for a cart checks
// the shopper's address through superscalar's workerd build, reads the
// cart from D1, asks the pricing Worker through the service binding with a
// token signed with the edge's key, and signs a URL for the cart's image in
// R2. Its test handler asks for a cart, for one with an address that is no
// address, and for /readyz, and checks the answers.
import { env } from 'cloudflare:workers';
import { Hono } from 'hono';
import { parseContactEmail } from 'superscalar';
import { createLogger, envelopeResponse, loadBucket, loadDatabase, loadService, serviceCredentialFor } from '../../../../dist/index.js';
import { ping } from '../../../../dist/d1.js';
import { openR2Bucket } from '../../../../dist/r2.js';
import { binding, perIsolate, requestIP, serviceFetch, stackEnvOf, stackEnvWithSecrets } from '../../../../dist/workers.js';

const vars = stackEnvOf(env);
const database = loadDatabase('SHOP_DB_DATABASE', vars);
const pricing = loadService('PRICING_SERVICE', vars);
const media = loadBucket('SHOP_MEDIA_BUCKET', vars);
if (!('d1' in database) || !pricing.binding || !pricing.credential || !media.binding) {
  throw new Error('the fixture config names no D1, service or R2 binding');
}
const d1 = database.d1;
const pricingBinding = pricing.binding;
const pricingCredential = pricing.credential;
const mediaBinding = media.binding;

const logger = createLogger({ service: 'storefront' });
let builds = 0;

const app = perIsolate(async () => {
  builds++;
  const config = await stackEnvWithSecrets(env, ['STOREFRONT_API_KEY']);
  const db = binding(env, d1.binding, 'd1');
  const callPricing = serviceFetch(binding(env, pricingBinding.name, 'service'));
  const credential = serviceCredentialFor(pricingCredential);
  const bucket = openR2Bucket(binding(env, mediaBinding, 'r2'), media, { now: () => new Date('2026-10-19T12:00:00Z') });
  const hono = new Hono<{ Bindings: { requestIP: typeof requestIP } }>();
  hono.get('/readyz', async () => {
    await ping(db);
    return envelopeResponse('ready', 'readyz');
  });
  hono.get('/carts/:email', async c => {
    const email = parseContactEmail(c.req.param('email'));
    if (email === null) return c.json({ error: 'not an email address' }, 400);
    const cents = await db.prepare('SELECT cents FROM cart WHERE email = ?1').bind(email).first<number>('cents');
    const token = await credential.token(false);
    const quote = await callPricing(`${pricing.url}/quote?cents=${cents ?? 0}`, {
      headers: Object.fromEntries(credential.headers.map(header => [header, `Bearer ${token}`])),
    });
    const image = new URL(await bucket.signedUrl(`carts/${email}.png`, { method: 'GET', expiresInSeconds: 60 }));
    logger.info('cart', { email });
    return c.json({
      email,
      cents,
      quote: await quote.json(),
      image: `${image.origin}${image.pathname}`,
      signed: image.searchParams.get('X-Amz-Signature')?.length,
      apiKey: config.STOREFRONT_API_KEY === 'sk_test',
      ip: c.env.requestIP(c.req.raw)?.address,
      builds,
    });
  });
  return hono;
});

const handler = {
  async fetch(request: Request): Promise<Response> {
    return (await app()).fetch(request, { requestIP });
  },

  async test(): Promise<void> {
    const failures: string[] = [];
    const expect = (what: string, got: unknown, want: unknown) => {
      if (JSON.stringify(got) !== JSON.stringify(want)) failures.push(`${what}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
    };
    const ready = await handler.fetch(new Request('https://storefront/readyz'));
    expect('/readyz status', ready.status, 200);
    expect('/readyz body', ((await ready.json()) as { data: unknown }).data, 'ready');

    const cart = await handler.fetch(new Request('https://storefront/carts/%20Ada@Example.COM%20', { headers: { 'CF-Connecting-IP': '203.0.113.7' } }));
    expect('/carts status', cart.status, 200);
    expect('/carts body', await cart.json(), {
      email: 'ada@example.com',
      cents: 1250,
      quote: { caller: 'Storefront', serves: ['shop-storefront'], cents: 2500 },
      image: 'https://0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com/shop-media/carts/ada%40example.com.png',
      signed: 64,
      apiKey: true,
      ip: '203.0.113.7',
      builds: 1,
    });

    const refused = await handler.fetch(new Request('https://storefront/carts/not-an-address'));
    expect('/carts of no address', refused.status, 400);
    if (failures.length > 0) throw new Error(`storefront fixture:\n${failures.join('\n')}`);
    console.log('storefront fixture: ok');
  },
};

export default handler;
