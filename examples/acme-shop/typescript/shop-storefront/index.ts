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

/** The header of a cart's CSV, as exportCart writes it and importCartLines reads it. */
const CSV_HEADER = 'sku,quantity,unit_price_cents,currency';

function cart(deps: Deps, carts: Map<string, CartView>): CartImplementation {
  /** Adds quantity of sku to a cart, opening it when it is new: the cart, and whether it opened. */
  const add = (cartId: CartView['id'], sku: CartLine['sku'], quantity: number): { cart: CartView; opened: boolean } => {
    const unitPrice = unitPrices[sku];
    if (!unitPrice) throw new HttpProblem(422, `No product has SKU ${sku}`, { code: 'unknown_sku' });
    const existing = carts.get(cartId);
    const previous = existing?.lines.find(line => line.sku === sku)?.quantity ?? 0;
    const lines = [...(existing?.lines ?? []).filter(line => line.sku !== sku), { sku, quantity: previous + quantity, unitPrice }];
    const cart: CartView = { id: cartId, status: CartStatus.Open, lines, total: total(lines), updatedAt: new Date() };
    carts.set(cartId, cart);
    return { cart, opened: !existing };
  };
  return {
    getCart: async ({ cartId }) => {
      const cart = carts.get(cartId);
      if (!cart) throw notFound(`Cart ${cartId} does not exist`);
      return cart;
    },
    exportCart: async ({ cartId }) => {
      const cart = carts.get(cartId);
      if (!cart) throw notFound(`Cart ${cartId} does not exist`);
      const rows = cart.lines.map(line => `${line.sku},${line.quantity},${line.unitPrice.amountCents},${line.unitPrice.currency}`);
      // The router answers it as text/csv under cart.csv, the schema's name.
      return { body: [CSV_HEADER, ...rows].join('\n') + '\n' };
    },
    addCartLine: async ({ cartId, input }, ctx) => {
      const { cart, opened } = add(cartId, input.sku, input.quantity);
      if (opened) deps.logger.info('cart opened', { cartId, shopper: ctx.principal?.subject });
      return opened ? new OperationResult(cart, 201) : cart;
    },
    importCartLines: async ({ cartId, files }, ctx) => {
      // A header, then sku,quantity rows; a further column, such as the
      // unit price exportCart writes, is the storefront's own to set.
      const [, ...rows] = (await files.lines.text()).split(/\r?\n/u).filter(row => row.trim() !== '');
      if (rows.length === 0) throw new HttpProblem(422, `${files.lines.name} has no lines`, { code: 'no_lines' });
      const lines = rows.map((row, index) => {
        const [sku = '', quantity = ''] = row.split(',').map(cell => cell.trim());
        const count = Number(quantity);
        if (!Number.isInteger(count) || count < 1 || count > 99) {
          throw new HttpProblem(422, `Row ${index + 2} of ${files.lines.name} has quantity ${quantity}, not 1 to 99`, { code: 'bad_quantity' });
        }
        if (!unitPrices[sku]) throw new HttpProblem(422, `No product has SKU ${sku}`, { code: 'unknown_sku' });
        // A SKU the storefront prices is a slug.
        return { sku: sku as CartLine['sku'], quantity: count };
      });
      // Every row is checked before the cart changes.
      let cart = carts.get(cartId);
      for (const line of lines) cart = add(cartId, line.sku, line.quantity).cart;
      deps.logger.info('cart lines imported', { cartId, lines: lines.length, file: files.lines.name, shopper: ctx.principal?.subject });
      return cart!;
    },
  };
}
