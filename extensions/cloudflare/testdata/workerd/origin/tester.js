// The tester of origin.capnp: it asks the site's Worker what a browser on
// the site's origin asks it (D91), through its service binding, as
// Cloudflare hands the script the paths its assets' config runs it first
// for, and checks the answers:
//   - the config names shop-storefront on the site's own origin;
//   - a cookie login to shop-storefront, with Sec-Fetch-Site same-origin
//     and with Origin alone, passes the identity runtime's cross-origin
//     check over its defaults, and the session cookie it sets is the
//     runtime's __Host- cookie, Secure, Path=/ and with no Domain, so the
//     browser keeps it for the site's host alone; a login from another
//     origin is refused;
//   - shop-storefront saw /auth/login, with the site's Host and
//     X-Forwarded-Host, -Proto and -Prefix, as stack dev's proxy passes
//     them; a call with the cookie keeps its query; the API's own path is
//     its /; a redirect within its routes gets its path back; its 404
//     reaches the page as it answered it;
//   - shop-pricing, at a public address of another provider, is fetched
//     there with the path taken off and the query kept;
//   - a path under /api that names no API of the origin is the site's
//     fallback, and a POST to a page of the site is still 405.

export default {
  async test(_controller, env) {
    const failures = [];
    const expect = (what, got, want) => {
      if (JSON.stringify(got) !== JSON.stringify(want)) failures.push(`${what}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
    };
    const site = new URL(env.SITE_URL);
    const ask = async (path, init = {}) => {
      const response = await env.SITE.fetch(new Request(`${env.SITE_URL}${path}`, { redirect: 'manual', ...init }));
      const seen = response.headers.get('x-seen');
      return { status: response.status, headers: response.headers, body: await response.text(), seen: seen && JSON.parse(seen) };
    };
    const index = '<!doctype html><title>storefront</title><div id=app></div>\n';

    const config = await ask('/__superschematic/config.json');
    expect('the config', [config.status, JSON.parse(config.body)], [200, JSON.parse(env.WANT_CONFIG)]);
    expect('shop-storefront in the config', JSON.parse(config.body).apis['shop-storefront'].url, `${env.SITE_URL}/api/shop-storefront`);

    let cookie = '';
    for (const headers of [{ origin: env.SITE_URL, 'sec-fetch-site': 'same-origin' }, { origin: env.SITE_URL }]) {
      const login = await ask('/api/shop-storefront/auth/login', { method: 'POST', headers: { 'content-type': 'application/json', ...headers }, body: '{"email":"ada@acme.dev"}' });
      expect(`a cookie login with ${JSON.stringify(headers)}`, login.status, 204);
      expect('what shop-storefront saw of the login', login.seen, {
        method: 'POST',
        path: '/auth/login',
        search: '',
        host: site.host,
        forwardedHost: site.host,
        forwardedProto: 'https',
        forwardedPrefix: '/api/shop-storefront',
        body: '{"email":"ada@acme.dev"}',
      });
      const setCookie = login.headers.get('set-cookie') ?? '';
      expect('the session cookie', setCookie, '__Host-session=tok-1; Path=/; Max-Age=3600; HttpOnly; Secure; SameSite=Lax');
      cookie = setCookie.split(';')[0];
    }
    const foreign = await ask('/api/shop-storefront/auth/login', { method: 'POST', headers: { origin: 'https://evil.example', 'sec-fetch-site': 'cross-site' }, body: '{}' });
    expect('a login from another origin', foreign.status, 403);

    const me = await ask('/api/shop-storefront/me?view=full', { headers: { cookie } });
    expect('a call with the session cookie', [me.status, me.body, me.seen.path, me.seen.search], [200, '{"name":"Ada"}', '/me', '?view=full']);
    expect('a call without it', (await ask('/api/shop-storefront/me')).status, 401);
    expect("the API's own path", (await ask('/api/shop-storefront')).seen.path, '/');
    const old = await ask('/api/shop-storefront/old');
    expect('a redirect within the API', [old.status, old.headers.get('location')], [303, '/api/shop-storefront/me?from=old']);
    const missing = await ask('/api/shop-storefront/orders/9');
    expect("the API's 404", [missing.status, missing.body], [404, '{"code":"not_found"}']);

    const pricing = await ask('/api/shop-pricing/quotes?sku=tea');
    expect('shop-pricing at its public address', JSON.parse(pricing.body), {
      url: 'https://shop-pricing.other.test/v1/quotes?sku=tea',
      host: 'shop-pricing.other.test',
      forwardedHost: site.host,
      forwardedProto: 'https',
      forwardedPrefix: '/api/shop-pricing',
    });

    const other = await ask('/api/shop-orders/orders');
    expect('a path of no API of the origin', [other.status, other.body], [200, index]);
    expect('a POST to a page', (await ask('/carts/42', { method: 'POST', body: '{}' })).status, 405);

    if (failures.length > 0) throw new Error(`the site's origin under workerd:\n${failures.join('\n')}`);
    console.log("the site's origin under workerd: a cookie login and an API call on one origin, a redirect, a 404 and another provider's API");
  },
};
