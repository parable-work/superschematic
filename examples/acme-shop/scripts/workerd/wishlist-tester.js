// The tester of wishlist.capnp: through its service binding to the
// wishlist's Worker, it reads /healthz and /readyz, which pings the D1
// database; keeps two teas for a shopper, one with a note, changes the
// other's note, drops one, and reads the wishlist back after each, every
// row through the Worker's d1 binding. It reads every answer's body whole,
// so no response is left open when the test ends.

const WISHLIST = 'https://shop-wishlist/api/wishlists/ada';

export default {
  async test(_controller, env) {
    const failures = [];
    const expect = (what, got, want) => {
      if (JSON.stringify(got) !== JSON.stringify(want)) failures.push(`${what}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
    };
    // Each answer as its status and its body, read whole.
    const call = async (path, init) => {
      const response = await env.WISHLIST.fetch(new Request(path.startsWith('https://') ? path : `https://shop-wishlist${path}`, init));
      const text = await response.text();
      return { status: response.status, body: text === '' ? null : JSON.parse(text) };
    };
    const keep = (sku, note) =>
      call(`${WISHLIST}/items/${sku}`, { method: 'PUT', headers: { 'content-type': 'application/json' }, body: JSON.stringify(note === undefined ? {} : { note }) });
    const items = answer => (answer.body?.data?.items ?? []).map(item => [item.sku, item.note ?? null]);

    expect('/healthz', (await call('/healthz')).status, 200);
    const ready = await call('/readyz');
    expect('/readyz', [ready.status, ready.body], [200, { status: 'ready' }]);

    const kept = await keep('green-tea', 'for the mornings');
    expect('keep green-tea', kept.status, 200);
    expect('the wishlist after one', items(kept), [['green-tea', 'for the mornings']]);
    expect('keep oolong', (await keep('oolong')).status, 200);
    const changed = await keep('oolong', 'a gift');
    expect('the wishlist after a note', items(changed), [
      ['green-tea', 'for the mornings'],
      ['oolong', 'a gift'],
    ]);
    const dropped = await call(`${WISHLIST}/items/green-tea`, { method: 'DELETE' });
    expect('drop green-tea', dropped.status, 200);
    const read = await call(WISHLIST);
    expect('the wishlist read back', [read.status, items(read)], [200, [['oolong', 'a gift']]]);
    expect('a SKU that is no slug', (await keep('Green%20Tea!')).status, 400);

    if (failures.length > 0) throw new Error(`the wishlist under workerd:\n${failures.join('\n')}`);
    console.log('the wishlist under workerd: oolong, a gift, kept through its d1 binding');
  },
};
