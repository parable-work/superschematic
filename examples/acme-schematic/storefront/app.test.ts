// Drives the generated shop-storefront router through the storefront app
// over HTTP. scripts/smoke.sh runs it after build-all.
import { describe, expect, test } from 'bun:test';
import { storefrontApp } from './app';

const cartId = '00000000-0000-4000-8000-000000000001';
const shopper = { 'x-api-key': 'shopper-key', 'content-type': 'application/json' };
const viewer = { 'x-api-key': 'viewer-key' };

describe('the generated storefront router', () => {
  test('the public probe answers without a caller', async () => {
    const response = await storefrontApp().request('/api/health', { headers: { 'x-request-id': 'probe-1' } });
    expect(response.status).toBe(200);
    expect(await response.json()).toEqual({ data: { status: 'ok' }, meta: { requestId: 'probe-1' } });
  });

  test('the gate answers 401 to an unknown key and 403 without the permission', async () => {
    const app = storefrontApp();
    const anonymous = await app.request(`/api/carts/${cartId}`);
    expect(anonymous.status).toBe(401);
    expect(anonymous.headers.get('content-type')).toBe('application/problem+json');
    expect(await anonymous.json()).toMatchObject({ status: 401, code: 'unauthorized' });
    const forbidden = await app.request(`/api/carts/${cartId}/lines`, {
      method: 'POST',
      headers: { ...viewer, 'content-type': 'application/json' },
      body: JSON.stringify({ sku: 'tea', quantity: 1 }),
    });
    expect(forbidden.status).toBe(403);
    expect(await forbidden.json()).toMatchObject({ status: 403, code: 'forbidden' });
  });

  test('a write goes through the generated body parser, then the implementation', async () => {
    const app = storefrontApp();
    const created = await app.request(`/api/carts/${cartId}/lines`, { method: 'POST', headers: shopper, body: JSON.stringify({ sku: 'tea', quantity: 2 }) });
    expect(created.status).toBe(201);
    const again = await app.request(`/api/carts/${cartId}/lines`, { method: 'POST', headers: shopper, body: JSON.stringify({ sku: 'tea', quantity: 1 }) });
    expect(again.status).toBe(200);
    expect((await again.json()).data).toMatchObject({ status: 'open', lines: [{ sku: 'tea', quantity: 3 }] });
    // quantity is Validate<number, { min: 1; max: 99 }> and sku is required.
    const tooFew = await app.request(`/api/carts/${cartId}/lines`, { method: 'POST', headers: shopper, body: JSON.stringify({ sku: 'tea', quantity: 0 }) });
    expect(tooFew.status).toBe(400);
    expect(await tooFew.json()).toMatchObject({ status: 400, code: 'bad_request' });
    const noSku = await app.request(`/api/carts/${cartId}/lines`, { method: 'POST', headers: shopper, body: JSON.stringify({ quantity: 1 }) });
    expect(noSku.status).toBe(400);
  });

  test('path and query parameters are decoded before the implementation runs', async () => {
    const app = storefrontApp();
    await app.request(`/api/carts/${cartId}/lines`, { method: 'POST', headers: shopper, body: JSON.stringify({ sku: 'tea', quantity: 1 }) });
    const found = await app.request(`/api/carts/${cartId}`, { headers: viewer });
    expect(found.status).toBe(200);
    const missing = await app.request('/api/carts/00000000-0000-4000-8000-000000000002', { headers: viewer });
    expect(missing.status).toBe(404);
    expect((await app.request('/api/carts/not-a-uuid', { headers: viewer })).status).toBe(400);
    const open = await app.request('/api/carts?statuses=open', { headers: viewer });
    expect((await open.json()).data).toHaveLength(1);
    const checkedOut = await app.request('/api/carts?statuses=checked_out', { headers: viewer });
    expect((await checkedOut.json()).data).toHaveLength(0);
    expect((await app.request('/api/carts?statuses=lost', { headers: viewer })).status).toBe(400);
  });

  test('the manual event-stream route is gated, then handed to the app', async () => {
    const app = storefrontApp();
    expect((await app.request(`/api/carts/${cartId}/events`)).status).toBe(401);
    const stream = await app.request(`/api/carts/${cartId}/events`, { headers: viewer });
    expect(stream.status).toBe(200);
    expect(stream.headers.get('content-type')).toBe('text/event-stream');
    expect(await stream.text()).toBe('event: cart\ndata: null\n\n');
  });

  test('an unknown path answers the problem envelope', async () => {
    const response = await storefrontApp().request('/api/nothing');
    expect(response.status).toBe(404);
    expect(await response.json()).toMatchObject({ status: 404, code: 'not_found' });
  });
});
