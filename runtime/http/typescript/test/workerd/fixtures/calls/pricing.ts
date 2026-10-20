// The callee of the calls fixture: shop-pricing, an internal Worker that
// only a service binding reaches (D64, section 18.10 of
// docs/stack-model.md). It reads its callers field from its bindings when
// it loads, as a generated fetch.ts does, and its one route verifies the
// caller's signed token against it and applies the operation's service
// clause, @requireService({ from: [ShopStorefront] }), as a generated
// router does: no credential is 401, a token that does not verify is 401.
import { env } from 'cloudflare:workers';
import { Hono } from 'hono';
import { authorizeService, loadCallers, serviceAuthenticator } from '../../../../dist/index.js';
import { errorHandler } from '../../../../dist/hono.js';
import { stackEnvOf } from '../../../../dist/workers.js';

const authenticate = serviceAuthenticator(loadCallers('SHOP_PRICING_CALLERS', stackEnvOf(env)));

const app = new Hono();
app.get('/quotes/:sku', async c => {
  const caller = await authenticate({ headers: c.req.raw.headers } as Parameters<typeof authenticate>[0]);
  authorizeService(caller, { mode: 'require', from: ['shop-storefront'] });
  return c.json({ sku: c.req.param('sku'), cents: 250, caller: caller?.deployable, serves: caller?.serves });
});
app.onError(errorHandler());

export default {
  fetch(request: Request): Response | Promise<Response> {
    return app.fetch(request);
  },
};
