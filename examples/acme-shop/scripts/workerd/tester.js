// The tester of storefront.capnp: through its service binding to the
// storefront's Worker, it reads /healthz and /readyz, refuses a cart
// without a token, puts two green teas in a cart as a shopper, from the
// address Cloudflare would give in CF-Connecting-IP, and reads the cart
// back as a viewer. Every request is the same isolate's, which holds the
// carts.

const CART = 'https://shop-storefront/api/carts/00000000-0000-4000-8000-000000000064';

export default {
  async test(_controller, env) {
    const failures = [];
    const expect = (what, got, want) => {
      if (JSON.stringify(got) !== JSON.stringify(want)) failures.push(`${what}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
    };
    const call = (path, init) => env.STOREFRONT.fetch(new Request(path.startsWith('https://') ? path : `https://shop-storefront${path}`, init));

    expect('/healthz', (await call('/healthz')).status, 200);
    const ready = await call('/readyz');
    expect('/readyz', [ready.status, await ready.json()], [200, { status: 'ready' }]);
    expect('a cart without a token', (await call(CART)).status, 401);

    const added = await call(`${CART}/lines`, {
      method: 'POST',
      headers: { authorization: 'Bearer shopper-token', 'content-type': 'application/json', 'CF-Connecting-IP': '203.0.113.7' },
      body: JSON.stringify({ sku: 'green-tea', quantity: 2 }),
    });
    const addedBody = await added.text();
    expect('add a cart line', added.status, 201);

    const read = await call(CART, { headers: { authorization: 'Bearer viewer-token' } });
    const cart = (await read.json()).data;
    expect('read the cart', read.status, 200);
    expect('the cart', { lines: cart?.lines?.map(line => [line.sku, line.quantity]), total: cart?.total }, {
      lines: [['green-tea', 2]],
      total: { amountCents: 900, currency: 'EUR' },
    });
    expect('an unknown SKU', (await call(`${CART}/lines`, {
      method: 'POST',
      headers: { authorization: 'Bearer shopper-token', 'content-type': 'application/json' },
      body: JSON.stringify({ sku: 'matcha', quantity: 1 }),
    })).status, 422);

    if (failures.length > 0) throw new Error(`the storefront under workerd:\n${failures.join('\n')}\n(the cart line answered ${addedBody})`);
    console.log(`the storefront under workerd: a cart of two green teas, ${cart.total.amountCents} cents`);
  },
};
