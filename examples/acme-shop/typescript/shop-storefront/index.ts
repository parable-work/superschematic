// The implementation of the shop-storefront API, which shop-stack's
// generated shop-storefront server builds from its Deps
// (docs/stack-model.md, section 8.6). The router superschematic generates
// decodes and checks each request, applies the auth gate and writes the
// envelopes; this package holds the business logic and says who the
// caller is.
import { HttpProblem, OperationResult, notFound, type Principal } from '@superschematic/http-runtime';
import type {
  AuthenticatorFactory,
  CartImplementation,
  Constructor,
  Deps,
  StorefrontProbesImplementation,
} from '@acme/shop-storefront-api';
import { CartStatus, Currency, type CartLine, type CartView, type Price } from '@acme/shop-storefront-types/types';

// Bearer tokens and what their holders may do. `carts` covers carts.read and
// carts.write: a granted permission covers the ones nested under it. A real
// service verifies the token with its identity provider instead.
const callers: Record<string, Principal> = {
  'shopper-token': { subject: 'shopper', permissions: ['carts'] },
  'viewer-token': { subject: 'viewer', permissions: ['carts.read'] },
};

/** Establishes the end user from the bearer token, on the routes that require one. */
export const authenticate: AuthenticatorFactory = () => async ctx => callers[ctx.bearerToken ?? ''] ?? null;

// What the storefront charges per unit, by SKU.
const unitPrices: Record<string, Price> = {
  'green-tea': { amountCents: 450, currency: Currency.EUR },
  'oolong': { amountCents: 720, currency: Currency.EUR },
};

function total(lines: CartLine[]): Price {
  const amountCents = lines.reduce((sum, line) => sum + line.unitPrice.amountCents * line.quantity, 0);
  return { amountCents, currency: Currency.EUR };
}

/** Builds the storefront from its Deps: its carts live in memory, for as long as the server runs. */
export const create: Constructor = deps => {
  const carts = new Map<string, CartView>();
  return {
    storefrontProbes: storefrontProbes(),
    cart: cart(deps, carts),
  };
};

function storefrontProbes(): StorefrontProbesImplementation {
  return {
    getHealth: async () => ({ status: 'ok' }),
  };
}

function cart(deps: Deps, carts: Map<string, CartView>): CartImplementation {
  return {
    getCart: async ({ cartId }) => {
      const cart = carts.get(cartId);
      if (!cart) throw notFound(`Cart ${cartId} does not exist`);
      return cart;
    },
    addCartLine: async ({ cartId, input }, ctx) => {
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
      if (!existing) deps.logger.info('cart opened', { cartId, shopper: ctx.principal?.subject });
      return existing ? cart : new OperationResult(cart, 201);
    },
  };
}
