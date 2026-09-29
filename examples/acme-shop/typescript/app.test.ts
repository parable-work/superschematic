// The generated TypeScript SDK calls the generated router in-process: the
// SDK's fetch option hands each request to the Hono app instead of the
// network. scripts/check.sh runs this after build-all.
import { describe, expect, test } from 'bun:test';
import { ApiError, AuthenticationError, ShopStorefrontSDK, ValidationError } from '@acme/shop-storefront-sdk';
import { storefrontApp } from './app';

const cartId = '00000000-0000-4000-8000-000000000001';

function sdkFor(token?: string) {
  const app = storefrontApp();
  return new ShopStorefrontSDK({
    baseUrl: 'http://storefront.test',
    auth: { token },
    fetch: (input, init) => app.fetch(new Request(input, init)),
  });
}

describe('the storefront', () => {
  test('a shopper adds lines and reads the cart back', async () => {
    const sdk = sdkFor('shopper-token');
    await sdk.cart.addCartLine(cartId, { sku: 'green-tea', quantity: 2 });
    const cart = await sdk.cart.addCartLine(cartId, { sku: 'oolong', quantity: 1 });
    expect(cart.total).toEqual({ amountCents: 2 * 450 + 720, currency: 'EUR' });
    expect(cart.updatedAt).toBeInstanceOf(Date);
    expect((await sdk.cart.getCart(cartId)).lines).toHaveLength(2);
  });

  test('the public probe needs no token', async () => {
    expect(await sdkFor().storefrontProbes.getHealth()).toEqual({ status: 'ok' });
  });

  test('the router answers 401 without a caller and 403 without the permission', async () => {
    const anonymous = await sdkFor().cart.getCart(cartId).catch(error => error);
    expect(anonymous).toBeInstanceOf(AuthenticationError);
    const viewer = await sdkFor('viewer-token').cart.addCartLine(cartId, { sku: 'green-tea', quantity: 1 }).catch(error => error);
    expect(viewer).toBeInstanceOf(ApiError);
    expect(viewer.statusCode).toBe(403);
  });

  test('the SDK checks an input before it sends it', async () => {
    // quantity is Validate<number, { min: 1; max: 99 }>.
    const tooMany = await sdkFor('shopper-token').cart.addCartLine(cartId, { sku: 'green-tea', quantity: 100 }).catch(error => error);
    expect(tooMany).toBeInstanceOf(ValidationError);
  });
});
