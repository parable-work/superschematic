// The tester of site.capnp: through its service bindings it asks the site
// Worker with a fallback, SITE, and the one without, BARE, what Cloudflare
// hands the script, a path that names no file of the build, and checks
// the answers: the config, uncached; a directory's index.html; the
// fallback at 200, or 404 without one; and 405 for a POST. A path that
// names a file never reaches the script on Cloudflare, so it is not asked.

export default {
  async test(_controller, env) {
    const failures = [];
    const expect = (what, got, want) => {
      if (JSON.stringify(got) !== JSON.stringify(want)) failures.push(`${what}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
    };
    const ask = async (worker, path, init) => {
      const response = await worker.fetch(new Request(`https://storefront-web.acme.workers.dev${path}`, init));
      return { status: response.status, headers: response.headers, body: await response.text() };
    };
    const index = '<!doctype html><title>storefront</title><div id=app></div>\n';

    const config = await ask(env.SITE, '/__superschematic/config.json');
    expect('the config', [config.status, config.headers.get('cache-control'), config.headers.get('content-type'), JSON.parse(config.body)], [
      200,
      'no-store',
      'application/json',
      JSON.parse(env.WANT_CONFIG),
    ]);
    const head = await ask(env.SITE, '/__superschematic/config.json', { method: 'HEAD' });
    expect('a HEAD of the config', [head.status, head.body], [200, '']);

    expect('the root', [(await ask(env.SITE, '/')).body], [index]);
    expect('a directory', [(await ask(env.SITE, '/docs')).body, (await ask(env.SITE, '/docs/')).body], [
      '<!doctype html><title>docs</title>\n',
      '<!doctype html><title>docs</title>\n',
    ]);
    const route = await ask(env.SITE, '/carts/42?view=full');
    expect('a route of the page, the fallback', [route.status, route.headers.get('content-type'), route.body], [200, 'text/html', index]);
    const routeHead = await ask(env.SITE, '/carts/42', { method: 'HEAD' });
    expect('a HEAD of a route', [routeHead.status, routeHead.body], [200, '']);
    const post = await ask(env.SITE, '/carts/42', { method: 'POST', body: '{}' });
    expect('a POST', [post.status, post.headers.get('allow')], [405, 'GET, HEAD']);

    expect('the root without a fallback', [(await ask(env.BARE, '/')).body], [index]);
    const missing = await ask(env.BARE, '/carts/42');
    expect('a route without a fallback', [missing.status, missing.body], [404, 'not found\n']);
    expect('the config without a fallback', (await ask(env.BARE, '/__superschematic/config.json')).status, 200);

    if (failures.length > 0) throw new Error(`the site's script under workerd:\n${failures.join('\n')}`);
    console.log('the site script under workerd: the config, a directory, the fallback and a 404');
  },
};
