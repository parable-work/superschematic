// The script of a static site's Worker on Cloudflare (docs/stack-model.md,
// section 18.8, D64). The cloudflare extension embeds it, so every site's
// Worker runs these bytes, in every release. Cloudflare serves a file of the
// site's build as a static asset without running it; it runs for a path
// that names no file, and first for every path under /__superschematic/.
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

const CONFIG_PATH = '/__superschematic/config.json';

export default {
  async fetch(request, env) {
    if (request.method !== 'GET' && request.method !== 'HEAD') {
      return text(405, 'method not allowed', { allow: 'GET, HEAD' });
    }
    const url = new URL(request.url);
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
