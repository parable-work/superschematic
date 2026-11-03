// The generated TypeScript SDK calls the generated router in-process: the
// SDK's fetch option hands each request to a Hono app that mounts the
// router over this implementation, as the generated entrypoint does,
// instead of the network. scripts/check.sh runs this after build-all.
import { describe, expect, test } from 'bun:test';
import { Hono } from 'hono';
import { createLogger } from '@superschematic/http-runtime';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { buildRouter } from '@acme/shop-storefront-api';
import { ApiError, AuthenticationError, ShopStorefrontSDK, ValidationError } from '@acme/shop-storefront-sdk';
import { authenticate, create } from './index';

const cartId = '00000000-0000-4000-8000-000000000001';

async function storefrontApp(): Promise<Hono> {
  const deps = { logger: createLogger({ api: 'shop-storefront' }, { write: () => {} }) };
  const app = new Hono();
  app.route('/', buildRouter(await create(deps), { authenticate: await authenticate(deps) }));
  app.notFound(notFoundHandler());
  app.onError(errorHandler());
  return app;
}

async function sdkFor(token?: string) {
  const app = await storefrontApp();
  return new ShopStorefrontSDK({
    baseUrl: 'http://storefront.test',
    auth: { token },
    fetch: (input, init) => app.fetch(new Request(input, init)),
  });
}

describe('the storefront', () => {
  test('a shopper adds lines and reads the cart back', async () => {
    const sdk = await sdkFor('shopper-token');
    await sdk.cart.addCartLine(cartId, { sku: 'green-tea', quantity: 2 });
    const cart = await sdk.cart.addCartLine(cartId, { sku: 'oolong', quantity: 1 });
    expect(cart.total).toEqual({ amountCents: 2 * 450 + 720, currency: 'EUR' });
    expect(cart.updatedAt).toBeInstanceOf(Date);
    expect((await sdk.cart.getCart(cartId)).lines).toHaveLength(2);
  });

  test('the public probe needs no token', async () => {
    expect(await (await sdkFor()).storefrontProbes.getHealth()).toEqual({ status: 'ok' });
  });

  test('the router answers 401 without a caller and 403 without the permission', async () => {
    const anonymous = await (await sdkFor()).cart.getCart(cartId).catch(error => error);
    expect(anonymous).toBeInstanceOf(AuthenticationError);
    const viewer = await (await sdkFor('viewer-token')).cart.addCartLine(cartId, { sku: 'green-tea', quantity: 1 }).catch(error => error);
    expect(viewer).toBeInstanceOf(ApiError);
    expect(viewer.statusCode).toBe(403);
  });

  test('a shopper imports lines from a CSV file and exports the cart as CSV (D81)', async () => {
    const sdk = await sdkFor('shopper-token');
    const csv = new File(['sku,quantity\ngreen-tea,2\noolong,1\n'], 'order.csv', { type: 'text/csv' });
    const cart = await sdk.cart.importCartLines(cartId, {}, { lines: csv });
    expect(cart.lines.map(line => [line.sku, line.quantity])).toEqual([['green-tea', 2], ['oolong', 1]]);
    expect(cart.total).toEqual({ amountCents: 2 * 450 + 720, currency: 'EUR' });

    const exported = await sdk.cart.exportCart(cartId);
    expect(exported.filename).toBe('cart.csv');
    expect(exported.contentType).toBe('text/csv; charset=utf-8');
    expect(await exported.blob.text()).toBe('sku,quantity,unit_price_cents,currency\ngreen-tea,2,450,EUR\noolong,1,720,EUR\n');

    // The export reads back: the quantities add up.
    const again = await sdk.cart.importCartLines(cartId, {}, { lines: new File([exported.blob], 'cart.csv', { type: 'text/csv' }) });
    expect(again.lines.map(line => line.quantity)).toEqual([4, 2]);
  });

  test('the router refuses a file the lines field does not admit, and the import a row it cannot read', async () => {
    const sdk = await sdkFor('shopper-token');
    const sheet = await sdk.cart.importCartLines(cartId, {}, { lines: new File(['x'], 'order.xlsx', { type: 'text/csv' }) }).catch(error => error);
    expect(sheet).toBeInstanceOf(ApiError);
    expect(sheet.statusCode).toBe(400);
    expect(sheet.validationErrors.lines[0].validator).toBe('uploadExtension');
    const unknown = await sdk.cart.importCartLines(cartId, {}, { lines: new File(['sku,quantity\nmatcha,1\n'], 'order.csv', { type: 'text/csv' }) }).catch(error => error);
    expect(unknown.statusCode).toBe(422);
    expect(unknown.code).toBe('unknown_sku');
  });

  test('the SDK checks an input before it sends it', async () => {
    // quantity is Validate<number, { min: 1; max: 99 }>.
    const tooMany = await (await sdkFor('shopper-token')).cart.addCartLine(cartId, { sku: 'green-tea', quantity: 100 }).catch(error => error);
    expect(tooMany).toBeInstanceOf(ValidationError);
  });
});
