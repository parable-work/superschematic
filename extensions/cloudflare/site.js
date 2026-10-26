// The script of a static site's Worker on Cloudflare (docs/stack-model.md,
// section 18.8, D64). The cloudflare extension embeds it, so every site's
// Worker runs these bytes, in every release. Cloudflare serves a file of the
// site's build as a static asset without running it; it runs for a path
// that names no file, and first for every path under /__superschematic/
// and under each API of the site's origin.
//
// It answers a GET or a HEAD of:
//   - /__superschematic/config.json with SITE_CONFIG, the site's config for
//     its environment, which names each API it calls with its public
//     address, never cached;
//   - a directory of the build with its index.html, as stack dev's file
//     server does, since the assets match a path to a file exactly
//     (htmlHandling: none);
//   - any other path with SITE_FALLBACK, the build's single-page fallback,
//     at 200, or 404 when the site has none.
// Any other method gets 405.
//
// A site with an origin (D91) names each API of it in SITE_ORIGIN, a JSON
// list of {path, binding} or {path, url}. A request for an API's path, or
// any path under it, with any method, goes to the API's Worker through the
// service binding, or to the API's public address, with the path taken
// off, the query kept, the browser's Host on the URL a binding carries,
// and X-Forwarded-Host, -Proto, -Prefix and -For set, as stack dev's proxy
// forwards it (D82); a redirect within the API's routes gets the path back.

const CONFIG_PATH = '/__superschematic/config.json';

/** The APIs of the site's origin, read from SITE_ORIGIN once per isolate. */
let origin;

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    const api = originAPI(env, url.pathname);
    if (api) return forward(request, env, url, api);
    if (request.method !== 'GET' && request.method !== 'HEAD') {
      return text(405, 'method not allowed', { allow: 'GET, HEAD' });
    }
    if (url.pathname === CONFIG_PATH) {
      return new Response(request.method === 'HEAD' ? null : env.SITE_CONFIG, {
        headers: { 'content-type': 'application/json', 'cache-control': 'no-store' },
      });
    }
    const index = await asset(env, request, url.pathname.replace(/\/?$/u, '/index.html'));
    if (index) return index;
    if (env.SITE_FALLBACK) {
      const fallback = await asset(env, request, `/${env.SITE_FALLBACK}`);
      if (fallback) return fallback;
    }
    return text(404, 'not found');
  },
};

/** The API of the site's origin whose path holds pathname, or undefined. */
function originAPI(env, pathname) {
  if (!env.SITE_ORIGIN) return undefined;
  origin ??= JSON.parse(env.SITE_ORIGIN);
  return origin.find((api) => pathname === api.path || pathname.startsWith(`${api.path}/`));
}

/** The request forwarded to the API's server, the API's path taken off. */
async function forward(request, env, url, api) {
  const rest = url.pathname.slice(api.path.length) || '/';
  const target = new URL(api.binding ? url.origin : api.url.replace(/\/$/u, '') + rest);
  if (api.binding) target.pathname = rest;
  target.search = url.search;
  const headers = new Headers(request.headers);
  if (!api.binding) headers.delete('host');
  headers.set('x-forwarded-host', url.host);
  headers.set('x-forwarded-proto', url.protocol.slice(0, -1));
  headers.set('x-forwarded-prefix', api.path);
  const client = request.headers.get('cf-connecting-ip');
  if (client) headers.set('x-forwarded-for', client);
  const init = { method: request.method, headers, redirect: 'manual' };
  if (request.method !== 'GET' && request.method !== 'HEAD') init.body = request.body;
  const response = api.binding ? await env[api.binding].fetch(target, init) : await fetch(target, init);
  const location = response.headers.get('location');
  if (location === null) return response;
  const answer = new Response(response.body, response);
  answer.headers.set('location', prefixed(location, api.path, url, target));
  return answer;
}

/**
 * A redirect's location with the API's path put back, when it points
 * within the API's routes: a path-absolute location, or one on the site's
 * host or the server's. Any other location is left as it is.
 */
function prefixed(location, prefix, site, target) {
  let u;
  try {
    u = new URL(location, target);
  } catch {
    return location;
  }
  const pathAbsolute = location.startsWith('/') && !location.startsWith('//');
  if (!pathAbsolute && u.host !== site.host && u.host !== target.host) return location;
  return prefix + u.pathname + u.search + u.hash;
}

/** The site's file at path, as the assets serve it, or undefined when there is none. */
async function asset(env, request, path) {
  const url = new URL(request.url);
  url.pathname = path;
  url.search = '';
  const response = await env.ASSETS.fetch(new Request(url, { method: request.method, headers: request.headers }));
  return response.status === 404 ? undefined : response;
}

/** A plain-text answer. */
function text(status, body, headers = {}) {
  return new Response(`${body}\n`, { status, headers: { 'content-type': 'text/plain; charset=utf-8', ...headers } });
}
