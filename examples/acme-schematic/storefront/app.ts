// The storefront service: an implementation of the TypeScript router that
// superschematic generates for the shop-storefront schema. The router owns
// the request pipeline (parameter decoding, the strict body parser, the auth
// gate, the envelopes); this file owns the business logic and says who the
// caller is.
import { Hono } from 'hono';
import { HttpProblem, OperationResult, notFound, type Authenticator, type Principal } from '@superschematic/http-runtime';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { buildRouter, type Implementations } from '@acme/shop-storefront-api';
import { CartStatus, type CartView } from '@acme/shop-storefront-types/types';

// API keys and what their holders may do. `carts` covers carts.read and
// carts.write: a granted permission covers the ones nested under it. A real
// deployment resolves keys against the ApiKey table, as the acme apikey
// provider does for the Go server.
const apiKeys: Record<string, Principal> = {
  'shopper-key': { subject: 'shopper', permissions: ['carts'] },
  'viewer-key': { subject: 'viewer', permissions: ['carts.read'] },
};

/** Resolves the caller from the X-API-Key header; null when the key is unknown. */
export const authenticateApiKey: Authenticator = async ctx => apiKeys[ctx.headers.get('x-api-key') ?? ''] ?? null;

export function storefrontApp(): Hono {
  const carts = new Map<string, CartView>();

  const implementations: Implementations = {
    storefrontProbes: {
      getHealth: async () => ({ status: 'ok' }),
    },
    cart: {
      listCarts: async ({ statuses }) => [...carts.values()].filter(cart => !statuses || statuses.includes(cart.status)),
      getCart: async ({ cartId }) => {
        const cart = carts.get(cartId);
        if (!cart) throw notFound(`Cart ${cartId} does not exist`);
        return cart;
      },
      addCartLine: async ({ cartId, input }) => {
        const existing = carts.get(cartId);
        if (existing && existing.status !== CartStatus.Open) {
          throw new HttpProblem(409, 'The cart is checked out', { code: 'cart_checked_out' });
        }
        const lines = (existing?.lines ?? []).filter(line => line.sku !== input.sku);
        const current = existing?.lines.find(line => line.sku === input.sku)?.quantity ?? 0;
        const cart: CartView = {
          id: cartId,
          status: CartStatus.Open,
          lines: [...lines, { sku: input.sku, quantity: current + input.quantity }],
          updatedAt: new Date(),
        };
        carts.set(cartId, cart);
        return existing ? cart : new OperationResult(cart, 201);
      },
    },
  };

  const app = new Hono();
  app.route(
    '/',
    buildRouter(implementations, {
      authenticate: authenticateApiKey,
      manualRoutes: {
        // The router has already checked carts.read; the handler writes the
        // server-sent event body itself.
        streamCartEvents: async (_c, ctx) =>
          new Response(`event: cart\ndata: ${JSON.stringify(carts.get(ctx.pathParams.cartId) ?? null)}\n\n`, {
            headers: { 'content-type': 'text/event-stream' },
          }),
      },
    })
  );
  app.notFound(notFoundHandler());
  app.onError(errorHandler());
  return app;
}
