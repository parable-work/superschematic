// The app of the telemetry fixture: shop-storefront as a generated fetch.ts
// and scheduled.ts wire trace export on Workers (D67, D64 amended), over
// the runtime's built ./workers-telemetry entry. At module scope it starts
// the telemetry from the Worker's bindings; its Hono app runs the
// middleware first, and its call to shop-pricing goes through the service
// binding by way of the telemetry's traced fetch; each request hands the
// flush to waitUntil, and the scheduled handler runs a job's span and
// flushes. The same bundle runs as two Workers: one whose bindings turn
// export on, and one with none, which exports nothing.
import { env } from 'cloudflare:workers';
import { Hono } from 'hono';
import { createLogger } from '../../../../dist/index.js';
import { startWorkersTelemetry } from '../../../../dist/workers-telemetry.js';

interface Fetcher {
  fetch(request: Request): Promise<Response>;
}

interface Context {
  waitUntil(promise: Promise<unknown>): void;
}

const logger = createLogger({ stack: 'tel-stack', server: 'shop-storefront' });
const telemetry = startWorkersTelemetry(env, { service: 'shop-storefront', stack: 'tel-stack', kind: 'server', logger });
const pricing = telemetry.traced((input, init) => (env.PRICING as Fetcher).fetch(new Request(input, init)));

const app = new Hono();
app.use(telemetry.middleware());
app.get('/healthz', c => c.json({ status: 'ok' }));
app.get('/api/quotes/:sku', async c => {
  const answer = await pricing(`https://tel-stack-staging-shop-pricing/api/prices/${c.req.param('sku')}?coupon=secret`);
  return c.json({ sku: c.req.param('sku'), enabled: telemetry.enabled, pricing: await answer.json() });
});

export default {
  async fetch(request: Request, _env: unknown, ctx: Context): Promise<Response> {
    try {
      return await app.fetch(request);
    } finally {
      ctx.waitUntil(telemetry.flush());
    }
  },
  async scheduled(_controller: unknown, _env: unknown, ctx: Context): Promise<void> {
    await telemetry.job('SweepCarts', async () => undefined);
    ctx.waitUntil(telemetry.flush());
  },
};
