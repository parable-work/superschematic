import { randomBytes } from 'node:crypto';
import type { Context, Env, Hono } from 'hono';
import { mountManualOperation, type RouterRuntimeOptions } from '../hono.js';
import type { OperationSpec } from '../operation.js';
import { requestHost } from './crossorigin.js';
import { bearerChallenge, discoveryPaths, publicBaseOf, OAUTH_AUTHORIZE_PATH, OAUTH_CONSENT_PATH, OAUTH_REGISTER_PATH, OAUTH_REVOKE_PATH, OAUTH_TOKEN_PATH } from './oauth-protocol.js';
import type { OAuthAnswer, OAuthServer } from './oauth.js';
import type { IdentityService } from './service.js';

/*
The OAuth authorization server's routes (#695) on a Hono app, for an API
whose identity service has one (IdentityService.oauth). They are no
operations of the router's table: the protocol takes form-encoded requests
and answers redirects and RFC 6749's errors, so mountOAuthRoutes mounts
them as manual routes of the router's pipeline, each public and
rate-limited per client:

- the discovery documents, at the root of the origin the API is reached
  through: GET /.well-known/oauth-protected-resource (and with the MCP
  endpoint's path after it), /.well-known/oauth-authorization-server and
  /.well-known/openid-configuration (and with the issuer's path after
  them);
- GET and POST /oauth/authorize, POST /oauth/token, POST /oauth/register
  and POST /oauth/revoke, under the API's base;
- GET /oauth/consent, the runtime's default consent page, unless the
  schema names a consentPath.

The consent and connected-app routes are operations
(getOAuthRequest and the rest), which the identity handlers serve under the
session set's path; the page drives them.
*/

/** The requests per minute each endpoint takes from one client. */
export const OAUTH_AUTHORIZE_RATE_LIMIT = 30;
export const OAUTH_TOKEN_RATE_LIMIT = 120;
export const OAUTH_REGISTER_RATE_LIMIT = 10;
export const OAUTH_REVOKE_RATE_LIMIT = 60;
export const OAUTH_DISCOVERY_RATE_LIMIT = 120;

/** The most bytes a form or a registration holds. */
const MAX_BODY_BYTES = 16 * 1024;

/** The API's public base for a request: the config's publicBaseUrl, or the request's own (oauth-protocol.ts's publicBaseOf). */
export function oauthBaseOf(service: IdentityService, request: Request): string {
  return publicBaseOf(service.config.oauth.publicBaseUrl, request.url, requestHost(request), request.headers.get('x-forwarded-prefix'));
}

/**
 * The WWW-Authenticate the MCP endpoint's 401 carries for an API with an
 * OAuth server (RFC 9728 section 5.1), with error="invalid_token" when the
 * request sent a credential; undefined for a service without one, whose
 * 401 carries none.
 */
export function oauthChallenge(service: IdentityService): ((request: Request) => string) | undefined {
  if (!service.oauth) return undefined;
  return request => bearerChallenge(oauthBaseOf(service, request), request.headers.has('authorization'));
}

function server(service: IdentityService): OAuthServer {
  if (!service.oauth) throw new Error('mountOAuthRoutes: the identity service has no OAuth server: pass oauth in its options, as @userSessions({ oauth }) declares');
  return service.oauth;
}

const NO_STORE = { 'cache-control': 'no-store', pragma: 'no-cache' } as const;

function json(answer: OAuthAnswer, headers: Record<string, string> = {}): Response {
  if (answer.body === null) return new Response(null, { status: answer.status, headers: { ...NO_STORE, ...headers } });
  return new Response(JSON.stringify(answer.body), { status: answer.status, headers: { 'content-type': 'application/json', ...NO_STORE, ...headers } });
}

function oauthError(status: number, error: string, description: string, headers: Record<string, string> = {}): Response {
  return json({ status, body: { error, error_description: description } }, headers);
}

async function readCapped(request: Request): Promise<string | undefined> {
  if (!request.body) return '';
  const reader = request.body.getReader();
  const chunks: Uint8Array[] = [];
  let size = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    size += value.byteLength;
    if (size > MAX_BODY_BYTES) {
      await reader.cancel();
      return undefined;
    }
    chunks.push(value);
  }
  return new TextDecoder().decode(Buffer.concat(chunks));
}

/** A form-encoded body's parameters; undefined for a body of another type, or one too large. */
async function formOf(request: Request): Promise<URLSearchParams | undefined> {
  const type = (request.headers.get('content-type') ?? '').split(';')[0]!.trim().toLowerCase();
  if (type !== 'application/x-www-form-urlencoded') return undefined;
  const text = await readCapped(request);
  return text === undefined ? undefined : new URLSearchParams(text);
}

/** The HTML of a page the authorize endpoint shows in place of a redirect it may not make. */
function errorPage(status: number, error: string, description: string): Response {
  const escape = (text: string) => text.replace(/[&<>"']/gu, c => `&#${c.charCodeAt(0)};`);
  const body = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Cannot connect</title></head><body><main><h1>Cannot connect</h1><p>${escape(description)}</p><p><code>${escape(error)}</code></p></main></body></html>`;
  return new Response(body, { status, headers: { 'content-type': 'text/html; charset=utf-8', ...pageHeaders('') } });
}

/** The headers of every page the server draws: no framing, no referrer, no cache, and a CSP that admits the page's own nonce'd script and style. */
function pageHeaders(nonce: string): Record<string, string> {
  const sources = nonce === '' ? "'none'" : `'nonce-${nonce}'`;
  return {
    'content-security-policy': `default-src 'none'; script-src ${sources}; style-src ${sources}; connect-src 'self'; img-src 'self' data:; form-action 'none'; frame-ancestors 'none'; base-uri 'none'`,
    'x-frame-options': 'DENY',
    'x-content-type-options': 'nosniff',
    'referrer-policy': 'no-referrer',
    'cache-control': 'no-store',
  };
}

/** The operation table entry of one protocol route: public, manual, rate-limited per client. */
function routeSpec(name: string, method: 'GET' | 'POST', path: string, rateLimitPerMinute: number): OperationSpec {
  return {
    name,
    namespace: 'oauth',
    method,
    path,
    pathParams: [],
    queryParams: [],
    bodyParams: [],
    auth: { public: true, required: false, permissions: [] },
    rateLimitPerMinute,
    bodyLimitBytes: MAX_BODY_BYTES,
    manual: true,
  };
}

/**
 * Mounts the OAuth server's routes on app (the module's comment lists
 * them), each through the router runtime's pipeline with runtime, the
 * router's options: a public route with its own rate limit, which counts
 * per client by runtime.rateLimit.keyOf (the transport's peer address
 * unless the router names another), in the router's store. The routes
 * answer for the public base of each request (oauthBaseOf): the config's
 * publicBaseUrl, or the request's own.
 */
export function mountOAuthRoutes<E extends Env = Env>(app: Hono<E>, service: IdentityService, runtime: RouterRuntimeOptions = {}): void {
  const oauth = server(service);
  const routesPrefix = '/api';
  const mount = (spec: OperationSpec, handler: (c: Context<E>) => Promise<Response> | Response) =>
    mountManualOperation(app, spec, async c => handler(c), runtime);

  // Discovery: each document at every path oauth-protocol.ts's
  // discoveryPaths names for the request's base, and 404 at any other.
  const discovery = (kind: 'protectedResource' | 'authorizationServer') => (c: Context<E>) => {
    const base = oauthBaseOf(service, c.req.raw);
    const path = new URL(c.req.url).pathname.replace(/\/+$/u, '');
    if (!discoveryPaths(base)[kind].includes(path)) return oauthError(404, 'not_found', 'No discovery document is at this path.');
    const body = kind === 'protectedResource' ? oauth.protectedResourceMetadata(base) : oauth.authorizationServerMetadata(base);
    return new Response(JSON.stringify(body), {
      status: 200,
      headers: { 'content-type': 'application/json', 'cache-control': 'public, max-age=300', 'access-control-allow-origin': '*' },
    });
  };
  const discoveryRoutes: [string, 'protectedResource' | 'authorizationServer'][] = [
    ['/.well-known/oauth-protected-resource', 'protectedResource'],
    ['/.well-known/oauth-protected-resource/*', 'protectedResource'],
    ['/.well-known/oauth-authorization-server', 'authorizationServer'],
    ['/.well-known/oauth-authorization-server/*', 'authorizationServer'],
    ['/.well-known/openid-configuration', 'authorizationServer'],
    ['/.well-known/openid-configuration/*', 'authorizationServer'],
  ];
  for (const [path, kind] of discoveryRoutes) mount(routeSpec('discovery', 'GET', path, OAUTH_DISCOVERY_RATE_LIMIT), discovery(kind));

  const authorize = async (c: Context<E>) => {
    let params = new URL(c.req.url).searchParams;
    if (c.req.method === 'POST') {
      const form = await formOf(c.req.raw);
      if (!form) return errorPage(400, 'invalid_request', 'The authorization request is not a form.');
      params = form;
    }
    const outcome = await oauth.authorize(params, oauthBaseOf(service, c.req.raw));
    if (outcome.kind === 'error') return errorPage(outcome.status, outcome.error, outcome.description);
    return new Response(null, { status: 302, headers: { location: outcome.location, 'cache-control': 'no-store', 'referrer-policy': 'no-referrer' } });
  };
  mount(routeSpec('authorize', 'GET', OAUTH_AUTHORIZE_PATH, OAUTH_AUTHORIZE_RATE_LIMIT), authorize);
  mount(routeSpec('authorize', 'POST', OAUTH_AUTHORIZE_PATH, OAUTH_AUTHORIZE_RATE_LIMIT), authorize);

  mount(routeSpec('token', 'POST', OAUTH_TOKEN_PATH, OAUTH_TOKEN_RATE_LIMIT), async c => {
    const form = await formOf(c.req.raw);
    if (!form) return oauthError(400, 'invalid_request', 'The token request is an application/x-www-form-urlencoded body of at most 16 KB.');
    return json(await oauth.token(form));
  });

  mount(routeSpec('revoke', 'POST', OAUTH_REVOKE_PATH, OAUTH_REVOKE_RATE_LIMIT), async c => {
    const form = await formOf(c.req.raw);
    if (!form) return oauthError(400, 'invalid_request', 'The revocation request is an application/x-www-form-urlencoded body of at most 16 KB.');
    return json(await oauth.revoke(form));
  });

  if (oauth.registration) {
    mount(routeSpec('register', 'POST', OAUTH_REGISTER_PATH, OAUTH_REGISTER_RATE_LIMIT), async c => {
      const type = (c.req.header('content-type') ?? '').split(';')[0]!.trim().toLowerCase();
      if (type !== 'application/json') return oauthError(400, 'invalid_client_metadata', 'The registration is a JSON body.');
      const text = await readCapped(c.req.raw);
      if (text === undefined) return oauthError(400, 'invalid_client_metadata', 'The registration is larger than 16 KB.');
      let body: unknown;
      try {
        body = JSON.parse(text);
      } catch {
        return oauthError(400, 'invalid_client_metadata', 'The registration is not JSON.');
      }
      return json(await oauth.register(body));
    });
  }

  if (oauth.consentPath === undefined) {
    mount(routeSpec('consent', 'GET', OAUTH_CONSENT_PATH, OAUTH_AUTHORIZE_RATE_LIMIT), c => {
      const base = oauthBaseOf(service, c.req.raw);
      const nonce = randomBytes(16).toString('base64url');
      const api = `${base}${routesPrefix}/${oauth.sessionsPath}`;
      return new Response(consentPage(nonce, api), { status: 200, headers: { 'content-type': 'text/html; charset=utf-8', ...pageHeaders(nonce) } });
    });
  }
}

/**
 * The runtime's default consent page. It reads the request it is given as
 * ?request=<id> through getOAuthRequest, signs the person in with the API's
 * login when they have no session, shows the client's name, its redirect's
 * host (and a warning when every redirect is to the person's own machine)
 * and the permissions it asks for, each of which the person may untick,
 * and sends approve or deny, whose answer it follows.
 */
export function consentPage(nonce: string, api: string): string {
  const config = JSON.stringify({ api }).replace(/</gu, '\\u003c');
  return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Connect an app</title>
<style nonce="${nonce}">
:root { color-scheme: light dark; --fg: #1d1d1f; --muted: #5f6368; --bg: #fff; --card: #f6f7f9; --line: #d8dbe0; --accent: #2b59c3; --warn: #8a5a00; --warn-bg: #fff4d6; }
@media (prefers-color-scheme: dark) { :root { --fg: #ececf1; --muted: #a0a4ab; --bg: #16171a; --card: #1f2125; --line: #33363c; --accent: #7aa2ff; --warn: #ffcf70; --warn-bg: #3a2f12; } }
* { box-sizing: border-box; }
body { margin: 0; font: 16px/1.5 system-ui, -apple-system, "Segoe UI", sans-serif; color: var(--fg); background: var(--bg); }
main { max-width: 440px; margin: 48px auto; padding: 0 16px; }
.card { background: var(--card); border: 1px solid var(--line); border-radius: 12px; padding: 24px; }
h1 { font-size: 20px; margin: 0 0 8px; }
p { margin: 0 0 16px; color: var(--muted); }
label { display: block; margin: 0 0 12px; }
input[type=text], input[type=password], input[type=email] { width: 100%; padding: 10px 12px; border: 1px solid var(--line); border-radius: 8px; background: var(--bg); color: var(--fg); font: inherit; }
ul { list-style: none; padding: 0; margin: 0 0 16px; }
li label { display: flex; gap: 10px; align-items: baseline; margin: 0 0 6px; }
code { font-size: 14px; }
.warn { background: var(--warn-bg); color: var(--warn); border-radius: 8px; padding: 10px 12px; margin: 0 0 16px; }
.row { display: flex; gap: 12px; }
button { flex: 1; padding: 10px 14px; border-radius: 8px; border: 1px solid var(--line); font: inherit; cursor: pointer; background: var(--bg); color: var(--fg); }
button.primary { background: var(--accent); border-color: var(--accent); color: #fff; }
.error { color: #c5221f; }
[hidden] { display: none !important; }
</style>
</head>
<body>
<main>
<div class="card" id="loading"><p>Loading…</p></div>
<form class="card" id="login" hidden>
<h1>Sign in to continue</h1>
<p>An app asks to connect to your account.</p>
<label>Login <input type="text" id="login-name" autocomplete="username" required></label>
<label>Password <input type="password" id="login-password" autocomplete="current-password" required></label>
<p class="error" id="login-error" hidden></p>
<div class="row"><button class="primary" type="submit">Sign in</button></div>
</form>
<form class="card" id="consent" hidden>
<h1 id="title"></h1>
<p id="who"></p>
<p id="where"></p>
<p class="warn" id="loopback" hidden>Every address this app sends you back to is on your own computer. Connect it only if you started it yourself, such as a command-line tool.</p>
<p>It may, as you:</p>
<ul id="permissions"></ul>
<p id="none" hidden>Nothing beyond what any signed-in user may do.</p>
<p id="offline" hidden>It keeps this access while you are away, until you disconnect it.</p>
<p class="error" id="consent-error" hidden></p>
<div class="row"><button type="button" id="deny">Deny</button><button class="primary" type="submit">Allow</button></div>
</form>
<div class="card" id="failed" hidden><h1>Cannot connect</h1><p id="failed-reason"></p></div>
</main>
<script nonce="${nonce}">
(() => {
  const config = ${config};
  const id = new URLSearchParams(location.search).get('request') || '';
  const $ = name => document.getElementById(name);
  const show = name => { for (const part of ['loading', 'login', 'consent', 'failed']) $(part).hidden = part !== name; };
  const fail = text => { $('failed-reason').textContent = text; show('failed'); };
  const call = async (method, path, body) => {
    const response = await fetch(config.api + path, {
      method,
      credentials: 'same-origin',
      headers: body === undefined ? {} : { 'content-type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    const text = await response.text();
    let json = null;
    try { json = text ? JSON.parse(text) : null; } catch {}
    return { status: response.status, json };
  };
  let request = null;
  const load = async () => {
    show('loading');
    const answer = await call('GET', '/oauth/requests/' + encodeURIComponent(id));
    if (answer.status === 401) { show('login'); $('login-name').focus(); return; }
    if (answer.status !== 200 || !answer.json || !answer.json.data) {
      fail(answer.status === 404 ? 'This request has ended or was answered already. Start again from the app.' : 'This request could not be read.');
      return;
    }
    request = answer.json.data;
    $('title').textContent = request.client.name + ' wants to connect';
    $('who').textContent = request.client.kind === 'metadataDocument'
      ? 'Its identity is vouched for by ' + new URL(request.client.id).host + '.'
      : 'This app registered itself, so its name is its own claim.';
    $('where').textContent = 'You will be sent back to ' + request.client.redirectHost + '.';
    $('loopback').hidden = !request.client.loopbackOnly;
    $('offline').hidden = !request.offlineAccess;
    const list = $('permissions');
    list.replaceChildren();
    for (const permission of request.held) {
      const item = document.createElement('li');
      const label = document.createElement('label');
      const box = document.createElement('input');
      box.type = 'checkbox';
      box.checked = true;
      box.value = permission;
      const name = document.createElement('code');
      name.textContent = permission;
      label.append(box, name);
      item.append(label);
      list.append(item);
    }
    $('none').hidden = request.held.length > 0;
    show('consent');
  };
  $('login').addEventListener('submit', async event => {
    event.preventDefault();
    const answer = await call('POST', '/login', { login: $('login-name').value, password: $('login-password').value, session: 'cookie' });
    if (answer.status !== 200) {
      $('login-error').textContent = answer.status === 429 ? 'Too many attempts; wait a minute.' : 'The login or the password is not right.';
      $('login-error').hidden = false;
      return;
    }
    await load();
  });
  const decide = async verb => {
    const body = { nonce: request.nonce };
    if (verb === 'approve') body.permissions = [...$('permissions').querySelectorAll('input:checked')].map(box => box.value);
    const answer = await call('POST', '/oauth/requests/' + encodeURIComponent(id) + '/' + verb, body);
    if (answer.status !== 200 || !answer.json || !answer.json.data) {
      $('consent-error').textContent = 'That did not work. Start again from the app.';
      $('consent-error').hidden = false;
      return;
    }
    location.assign(answer.json.data.redirectTo);
  };
  $('consent').addEventListener('submit', event => { event.preventDefault(); decide('approve'); });
  $('deny').addEventListener('click', () => decide('deny'));
  if (id === '') fail('The page was opened without a request. Start again from the app.');
  else load().catch(() => fail('This request could not be read.'));
})();
</script>
</body>
</html>
`;
}
