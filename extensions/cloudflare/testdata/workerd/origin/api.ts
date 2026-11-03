// A stand-in for an API of a site's origin under workerd (D91): the
// TypeScript identity runtime's own cross-origin check, session cookie and
// config, bundled from the runtime's source, over its defaults: no trusted
// origin and no cookie domain. A cookie login answers 204 with the
// session cookie, a route needs the cookie, /old redirects within the
// API's routes, and any other path is a JSON 404. Each answer carries what
// the API saw of the request, as X-Seen, so the tester can read the path,
// the query, the Host and the forwarded headers the site's Worker passed.
import { parseIdentityConfig } from '../../../../../runtime/http/typescript/src/identity/config.ts';
import { sessionCookie } from '../../../../../runtime/http/typescript/src/identity/cookie.ts';
import { crossOriginAllowed, requestHost } from '../../../../../runtime/http/typescript/src/identity/crossorigin.ts';

const config = parseIdentityConfig({});

export default {
  async fetch(request: Request): Promise<Response> {
    const url = new URL(request.url);
    const host = requestHost(request);
    const seen = JSON.stringify({
      method: request.method,
      path: url.pathname,
      search: url.search,
      host,
      forwardedHost: request.headers.get('x-forwarded-host'),
      forwardedProto: request.headers.get('x-forwarded-proto'),
      forwardedPrefix: request.headers.get('x-forwarded-prefix'),
      body: request.method === 'POST' ? await request.text() : null,
    });
    const answer = (status: number, body: string | null, headers: Record<string, string> = {}) =>
      new Response(body, { status, headers: { 'x-seen': seen, ...headers } });
    if (url.pathname === '/auth/login' && request.method === 'POST') {
      if (!crossOriginAllowed(config.trustedOrigins, { method: request.method, host, headers: request.headers })) {
        return answer(403, '{"code":"cross_origin"}', { 'content-type': 'application/json' });
      }
      return answer(204, null, { 'set-cookie': sessionCookie(config, 'tok-1', 3600) });
    }
    if (url.pathname === '/me') {
      const cookie = request.headers.get('cookie') ?? '';
      if (!cookie.split(/;\s*/u).includes(`${config.cookie.name}=tok-1`)) {
        return answer(401, '{"code":"unauthenticated"}', { 'content-type': 'application/json' });
      }
      return answer(200, '{"name":"Ada"}', { 'content-type': 'application/json' });
    }
    if (url.pathname === '/old') {
      return answer(303, null, { location: '/me?from=old' });
    }
    return answer(404, '{"code":"not_found"}', { 'content-type': 'application/json' });
  },
};
