// The TypeScript side of the acme-shop example: an implementation of the
// router superschematic generates for shop-storefront. The router decodes
// and checks each request, applies the auth gate and writes the envelopes;
// this file holds the business logic and says who the caller is.
import { Hono } from 'hono';
import { HttpProblem, OperationResult, notFound, type Authenticator, type Principal } from '@superschematic/http-runtime';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { buildRouter, type Implementations } from '@acme/shop-storefront-api';
import { CartStatus, Currency, type CartLine, type CartView, type Price } from '@acme/shop-storefront-types/types';

// Bearer tokens and what their holders may do. `carts` covers carts.read and
// carts.write: a granted permission covers the ones nested under it. A real
// service verifies the token with its identity provider instead.
const callers: Record<string, Principal> = {
  'shopper-token': { subject: 'shopper', permissions: ['carts'] },
  'viewer-token': { subject: 'viewer', permissions: ['carts.read'] },
};

export const authenticate: Authenticator = async ctx => callers[ctx.bearerToken ?? ''] ?? null;

// What the storefront charges per unit, by SKU.
const unitPrices: Record<string, Price> = {
  'green-tea': { amountCents: 450, currency: Currency.EUR },
  'oolong': { amountCents: 720, currency: Currency.EUR },
};

function total(lines: CartLine[]): Price {
  const amountCents = lines.reduce((sum, line) => sum + line.unitPrice.amountCents * line.quantity, 0);
  return { amountCents, currency: Currency.EUR };
}

export function storefrontApp(): Hono {
  const carts = new Map<string, CartView>();

  const implementations: Implementations = {
    storefrontProbes: {
      getHealth: async () => ({ status: 'ok' }),
    },
    cart: {
      getCart: async ({ cartId }) => {
        const cart = carts.get(cartId);
        if (!cart) throw notFound(`Cart ${cartId} does not exist`);
        return cart;
      },
      addCartLine: async ({ cartId, input }) => {
        const unitPrice = unitPrices[input.sku];
        if (!unitPrice) throw new HttpProblem(422, `No product has SKU ${input.sku}`, { code: 'unknown_sku' });
        const existing = carts.get(cartId);
        const previous = existing?.lines.find(line => line.sku === input.sku)?.quantity ?? 0;
        const lines = [
          ...(existing?.lines ?? []).filter(line => line.sku !== input.sku),
          { sku: input.sku, quantity: previous + input.quantity, unitPrice },
        ];
        const cart: CartView = { id: cartId, status: CartStatus.Open, lines, total: total(lines), updatedAt: new Date() };
        carts.set(cartId, cart);
        return existing ? cart : new OperationResult(cart, 201);
      },
    },
  };

  const app = new Hono();
  app.route('/', buildRouter(implementations, { authenticate }));
  app.notFound(notFoundHandler());
  app.onError(errorHandler());
  return app;
}
