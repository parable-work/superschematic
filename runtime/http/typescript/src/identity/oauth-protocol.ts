import { createHash } from 'node:crypto';
import { hostnameOf, isLoopbackHost } from './config.js';
import { parseGoURL } from './url.js';

/*
The pure parts of the OAuth authorization server (#695), which the parity
vectors (runtime/http/testdata/oauth_parity.json) pin: PKCE, how a
redirect matches a client's, the API's public base URL and the URLs it
names, and the two discovery documents. None of them reads a store or a
clock.
*/

/** The MCP endpoint's path under an API's base (mcp.ts's MCP_PATH), the protected resource's. */
export const OAUTH_RESOURCE_PATH = '/api/mcp';

/** The well-known paths of the discovery documents, at an origin's root. */
export const PROTECTED_RESOURCE_METADATA_PATH = '/.well-known/oauth-protected-resource';
export const AUTHORIZATION_SERVER_METADATA_PATH = '/.well-known/oauth-authorization-server';
export const OPENID_CONFIGURATION_PATH = '/.well-known/openid-configuration';

/** The protocol endpoints, under the API's base. */
export const OAUTH_AUTHORIZE_PATH = '/oauth/authorize';
export const OAUTH_TOKEN_PATH = '/oauth/token';
export const OAUTH_REGISTER_PATH = '/oauth/register';
export const OAUTH_REVOKE_PATH = '/oauth/revoke';
export const OAUTH_CONSENT_PATH = '/oauth/consent';

/** The scope a client asks for a refresh token's life with, beside the API's permission names. */
export const OFFLINE_ACCESS_SCOPE = 'offline_access';

/**
 * Whether verifier is a PKCE code verifier (RFC 7636 section 4.1): 43 to
 * 128 characters of letters, digits, '-', '.', '_' and '~'.
 */
export function isCodeVerifier(verifier: string): boolean {
  return /^[A-Za-z0-9._~-]{43,128}$/u.test(verifier);
}

/** Whether challenge has an S256 challenge's shape: the 43 characters of base64url a SHA-256 is. */
export function isCodeChallenge(challenge: string): boolean {
  return /^[A-Za-z0-9_-]{43}$/u.test(challenge);
}

/** The S256 challenge of a verifier: base64url, without padding, of its SHA-256. */
export function codeChallengeOf(verifier: string): string {
  return createHash('sha256').update(verifier, 'ascii').digest('base64url');
}

/** Whether verifier answers challenge under S256, the one method the server takes. */
export function verifyCodeChallenge(verifier: string, challenge: string): boolean {
  return isCodeVerifier(verifier) && isCodeChallenge(challenge) && codeChallengeOf(verifier) === challenge;
}

/** A redirect URI's parts, as the matching reads them; undefined for a string that is no absolute http(s) URL. */
interface RedirectParts {
  readonly scheme: string;
  readonly hostname: string;
  readonly port: string;
  /** Everything after the authority: path and query. */
  readonly rest: string;
}

function redirectParts(uri: string): RedirectParts | undefined {
  const u = parseGoURL(uri);
  if (!u || u.opaque !== '' || u.host === '' || u.hasUser || u.fragment !== '' || uri.includes('#')) return undefined;
  if (u.scheme !== 'https' && u.scheme !== 'http') return undefined;
  const hostname = hostnameOf(u.host).toLowerCase();
  const authority = `${u.scheme}://`;
  const afterScheme = uri.slice(authority.length);
  const slash = afterScheme.search(/[/?]/u);
  const rest = slash < 0 ? '' : afterScheme.slice(slash);
  const hostText = slash < 0 ? afterScheme : afterScheme.slice(0, slash);
  const bracket = hostText.lastIndexOf(']');
  const colon = hostText.lastIndexOf(':');
  const port = colon > bracket ? hostText.slice(colon + 1) : '';
  return { scheme: u.scheme, hostname, port, rest };
}

/** Whether uri is a loopback redirect: http on localhost, 127.0.0.1 or [::1], as a command-line client's is (RFC 8252 section 7.3). */
export function isLoopbackRedirect(uri: string): boolean {
  const parts = redirectParts(uri);
  return parts !== undefined && parts.scheme === 'http' && isLoopbackHost(parts.hostname);
}

/**
 * Whether a redirect URI is one a client may name: an https URL, or a
 * loopback one over http, either with no fragment or user.
 */
export function isAllowedRedirect(uri: string): boolean {
  const parts = redirectParts(uri);
  if (!parts) return false;
  return parts.scheme === 'https' || isLoopbackHost(parts.hostname);
}

/**
 * Whether given is one of a client's registered redirects: the same string,
 * or, for a loopback redirect, the same but for the port, which the
 * client's own machine picks when it listens (RFC 8252 section 7.3).
 */
export function redirectMatches(registered: readonly string[], given: string): boolean {
  if (registered.includes(given)) return true;
  const g = redirectParts(given);
  if (!g || g.scheme !== 'http' || !isLoopbackHost(g.hostname)) return false;
  return registered.some(candidate => {
    const r = redirectParts(candidate);
    return r !== undefined && r.scheme === 'http' && r.hostname === g.hostname && r.rest === g.rest;
  });
}

/** The host a redirect sends the browser to, as the consent page shows it; '' for no URL. */
export function redirectHost(uri: string): string {
  const u = parseGoURL(uri);
  return u ? u.host : '';
}

/**
 * The URL an API is reached at from outside: the config's publicBaseUrl
 * when it names one, otherwise the request's own scheme and Host and the
 * X-Forwarded-Prefix a site's proxy names, without a trailing slash.
 */
export function publicBaseOf(configured: string, requestUrl: string, host: string, forwardedPrefix: string | null | undefined): string {
  if (configured !== '') return configured;
  const scheme = new URL(requestUrl).protocol;
  const prefix = (forwardedPrefix ?? '').trim();
  const path = /^\/[A-Za-z0-9._~!$&'()*+,;=:@%/-]*$/u.test(prefix) ? prefix.replace(/\/+$/u, '') : '';
  return `${scheme}//${host}${path}`;
}

/** The path of a URL: '' for its root. */
export function pathOf(url: string): string {
  const path = new URL(url).pathname;
  return path === '/' ? '' : path.replace(/\/+$/u, '');
}

/** What the discovery documents say of a server. */
export interface OAuthMetadataOptions {
  /** The API's public base, publicBaseOf's answer: the issuer. */
  readonly base: string;
  /** The API's permission names, which the scopes are, sorted, offline_access after them. */
  readonly scopes: readonly string[];
  /** Whether a client may name itself by a metadata document's URL. */
  readonly metadataDocuments: boolean;
  /** Whether a client may register. */
  readonly registration: boolean;
}

/** The scopes a server takes: its permission names, each once in code-point order, and offline_access. */
export function scopesOf(permissions: readonly string[]): string[] {
  const names = [...new Set(permissions.filter(p => p !== OFFLINE_ACCESS_SCOPE))].sort((a, b) => (a < b ? -1 : a > b ? 1 : 0));
  return [...names, OFFLINE_ACCESS_SCOPE];
}

/** The protected resource's URL: the API's MCP endpoint, under its base. */
export function resourceOf(base: string): string {
  return `${base}${OAUTH_RESOURCE_PATH}`;
}

/** The protected resource metadata (RFC 9728) of the API's MCP endpoint. */
export function protectedResourceMetadata(options: OAuthMetadataOptions): Record<string, unknown> {
  return {
    resource: resourceOf(options.base),
    authorization_servers: [options.base],
    scopes_supported: scopesOf(options.scopes),
    bearer_methods_supported: ['header'],
  };
}

/** The authorization server metadata (RFC 8414) of the API. */
export function authorizationServerMetadata(options: OAuthMetadataOptions): Record<string, unknown> {
  const { base } = options;
  return {
    issuer: base,
    authorization_endpoint: `${base}${OAUTH_AUTHORIZE_PATH}`,
    token_endpoint: `${base}${OAUTH_TOKEN_PATH}`,
    ...(options.registration ? { registration_endpoint: `${base}${OAUTH_REGISTER_PATH}` } : {}),
    revocation_endpoint: `${base}${OAUTH_REVOKE_PATH}`,
    response_types_supported: ['code'],
    response_modes_supported: ['query'],
    grant_types_supported: ['authorization_code', 'refresh_token'],
    code_challenge_methods_supported: ['S256'],
    token_endpoint_auth_methods_supported: ['none'],
    revocation_endpoint_auth_methods_supported: ['none'],
    client_id_metadata_document_supported: options.metadataDocuments,
    authorization_response_iss_parameter_supported: true,
    scopes_supported: scopesOf(options.scopes),
  };
}

/**
 * The paths a discovery document answers at, under an origin's root: the
 * bare well-known path and, RFC 9728's and RFC 8414's insertion, the
 * well-known path with the resource's or the issuer's path after it. The
 * OpenID alias also answers with the issuer's path before it, as OpenID
 * Connect discovery appends.
 */
export function discoveryPaths(base: string): { readonly protectedResource: string[]; readonly authorizationServer: string[] } {
  const issuerPath = pathOf(base);
  const resourcePath = pathOf(resourceOf(base));
  const unique = (paths: string[]) => [...new Set(paths)];
  return {
    protectedResource: unique([PROTECTED_RESOURCE_METADATA_PATH, `${PROTECTED_RESOURCE_METADATA_PATH}${resourcePath}`]),
    authorizationServer: unique([
      AUTHORIZATION_SERVER_METADATA_PATH,
      `${AUTHORIZATION_SERVER_METADATA_PATH}${issuerPath}`,
      OPENID_CONFIGURATION_PATH,
      `${OPENID_CONFIGURATION_PATH}${issuerPath}`,
      `${issuerPath}${OPENID_CONFIGURATION_PATH}`,
    ]),
  };
}

/**
 * The WWW-Authenticate value of the MCP endpoint's 401 (RFC 9728 section
 * 5.1): the protected resource metadata's URL and, when a token was sent
 * and refused, error="invalid_token".
 */
export function bearerChallenge(base: string, invalidToken: boolean): string {
  const metadata = `${new URL(base).origin}${PROTECTED_RESOURCE_METADATA_PATH}${pathOf(resourceOf(base))}`;
  return `Bearer resource_metadata="${metadata}"${invalidToken ? ', error="invalid_token"' : ''}`;
}

/**
 * The scope string's names (RFC 6749 section 3.3): split on spaces, each
 * once, in the order given; undefined for one that holds anything but
 * printable ASCII other than '"' and '\'.
 */
export function scopeNames(scope: string): string[] | undefined {
  if (!/^[\x21\x23-\x5b\x5d-\x7e ]*$/u.test(scope)) return undefined;
  return [...new Set(scope.split(' ').filter(name => name !== ''))];
}

/** A redirect to uri with params added to its query. */
export function redirectWith(uri: string, params: Readonly<Record<string, string | null | undefined>>): string {
  const url = new URL(uri);
  for (const [name, value] of Object.entries(params)) {
    if (value !== null && value !== undefined) url.searchParams.append(name, value);
  }
  return url.toString();
}
