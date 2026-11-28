import { describe, expect, test } from 'bun:test';
import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import {
  authorizationServerMetadata,
  bearerChallenge,
  cacheSecondsOf,
  codeChallengeOf,
  discoveryPaths,
  extractCredential,
  hashToken,
  isAllowedRedirect,
  isLoopbackRedirect,
  isPrivateAddress,
  metadataDocumentUrlProblem,
  newOAuthAccessToken,
  newOAuthRefreshToken,
  parseIdentityConfig,
  protectedResourceMetadata,
  publicBaseOf,
  redirectMatches,
  scopeNames,
  verifyCodeChallenge,
} from './index';

/*
The OAuth authorization server's pure parts (#695): the token shapes and
their hashes, the credential an access or a refresh token is on a request,
the config's oauth member, PKCE, how a redirect matches a client's, the
public base and the two discovery documents for it, the MCP endpoint's
challenge, a scope's names, a metadata document's URL, the addresses its
fetch refuses, and how long a document is kept. This test computes every
want from the inputs below and holds runtime/http/testdata/oauth_parity.json
to them; OAUTH_PARITY_WRITE=1 writes the file instead. The Rust and Go
runtimes are to read it as they build the server (#695's Left).
*/

const CORPUS = fileURLToPath(new URL('../../../testdata/oauth_parity.json', import.meta.url));

const tokenBytes = ['00'.repeat(32), 'ff'.repeat(32), Array.from({ length: 32 }, (_, i) => i.toString(16).padStart(2, '0')).join('')];

const access = `oat_${'A'.repeat(43)}`;
const refresh = `ort_${'B'.repeat(43)}`;
const credentials: { name: string; headers: [string, string][] }[] = [
  { name: 'an access token is a bearer credential', headers: [['Authorization', `Bearer ${access}`]] },
  { name: 'a refresh token is refused as a bearer credential', headers: [['Authorization', `Bearer ${refresh}`]] },
  { name: 'an access token one character short is refused', headers: [['Authorization', `Bearer ${access.slice(0, -1)}`]] },
  { name: 'an access token with a character outside base64url is refused', headers: [['Authorization', `Bearer oat_${'A'.repeat(42)}+`]] },
  { name: 'an access token prefix in upper case is refused', headers: [['Authorization', `Bearer OAT_${'A'.repeat(43)}`]] },
  { name: 'an access token in the cookie is refused', headers: [['Cookie', `session=${access}`]] },
];

const configs: { name: string; input: unknown }[] = [
  { name: 'no oauth member', input: {} },
  { name: 'a public base URL under a site', input: { oauth: { publicBaseUrl: 'https://app.example.com/api/notes' } } },
  { name: 'a public base URL on loopback over http', input: { oauth: { publicBaseUrl: 'http://127.0.0.1:4000' } } },
  { name: 'http elsewhere is refused', input: { oauth: { publicBaseUrl: 'http://app.example.com' } } },
  { name: 'a trailing slash is refused', input: { oauth: { publicBaseUrl: 'https://app.example.com/' } } },
  { name: 'an unknown member is refused', input: { oauth: { issuer: 'https://app.example.com' } } },
];

const verifiers = ['dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk', 'a'.repeat(43), 'a'.repeat(128), `${'a'.repeat(42)}~`];
const pkce: { verifier: string; challenge: string }[] = [
  { verifier: 'dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk', challenge: 'E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM' },
  { verifier: 'dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk', challenge: 'E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cN' },
  { verifier: 'a'.repeat(42), challenge: codeChallengeOf('a'.repeat(42)) },
  { verifier: 'a'.repeat(129), challenge: codeChallengeOf('a'.repeat(129)) },
  { verifier: `${'a'.repeat(42)}!`, challenge: codeChallengeOf(`${'a'.repeat(42)}!`) },
  { verifier: 'a'.repeat(43), challenge: 'short' },
];

const claude = 'https://claude.ai/api/mcp/auth_callback';
const redirects: { registered: string[]; given: string }[] = [
  { registered: [claude], given: claude },
  { registered: [claude], given: `${claude}/` },
  { registered: [claude], given: `${claude}?x=1` },
  { registered: [claude], given: 'https://CLAUDE.ai/api/mcp/auth_callback' },
  { registered: ['http://localhost/callback'], given: 'http://localhost:51234/callback' },
  { registered: ['http://localhost/callback'], given: 'http://localhost/callback' },
  { registered: ['http://127.0.0.1/callback'], given: 'http://127.0.0.1:8080/callback' },
  { registered: ['http://[::1]/callback'], given: 'http://[::1]:8080/callback' },
  { registered: ['http://localhost/callback'], given: 'http://127.0.0.1:8080/callback' },
  { registered: ['http://localhost/callback'], given: 'http://localhost:8080/callback/other' },
  { registered: ['http://localhost/callback'], given: 'http://localhost:8080/callback?x=1' },
  { registered: ['https://localhost/callback'], given: 'https://localhost:8443/callback' },
  { registered: ['http://localhost:3000/callback'], given: 'http://localhost:3001/callback' },
  { registered: ['http://app.example.com/callback'], given: 'http://app.example.com:8080/callback' },
];

const redirectKinds = [
  claude,
  'http://localhost:51234/callback',
  'http://127.0.0.1/callback',
  'http://[::1]:9/cb',
  'http://app.example.com/cb',
  'https://app.example.com/cb#frag',
  'https://me@app.example.com/cb',
  'myapp://callback',
  'not a url',
];

const bases: { configured: string; url: string; host: string; prefix: string | null }[] = [
  { configured: 'https://app.example.com/api/notes', url: 'http://10.0.0.5:8080/oauth/token', host: '10.0.0.5:8080', prefix: '/api/notes' },
  { configured: '', url: 'http://localhost:4000/oauth/token', host: 'localhost:4000', prefix: null },
  { configured: '', url: 'http://localhost:4000/oauth/token', host: 'localhost:4000', prefix: '/api/notes' },
  { configured: '', url: 'https://api.example.com/oauth/token', host: 'api.example.com', prefix: '/api/notes/' },
  { configured: '', url: 'https://api.example.com/oauth/token', host: 'api.example.com', prefix: 'javascript:alert(1)' },
];

const metadata = [
  { base: 'https://app.example.com/api/notes', scopes: ['notes.write', 'notes.read', 'notes.read'], metadataDocuments: true, registration: true },
  { base: 'https://api.example.com', scopes: [], metadataDocuments: true, registration: false },
  { base: 'http://localhost:4000', scopes: ['identity.users.read'], metadataDocuments: false, registration: true },
];

const scopes = ['', 'notes.read', 'notes.read  notes.write', 'notes.read notes.read offline_access', 'notes.read "x"', 'café'];

const metadataUrls = [
  'https://claude.ai/oauth/claude-code-client-metadata',
  'https://example.com/client.json',
  'http://example.com/client.json',
  'https://example.com',
  'https://example.com/',
  'https://example.com/a/../client.json',
  'https://example.com/client.json#x',
  'https://me@example.com/client.json',
];

const addresses = [
  '93.184.216.34',
  '10.0.0.1',
  '127.0.0.1',
  '169.254.169.254',
  '172.16.0.1',
  '172.32.0.1',
  '192.168.1.1',
  '100.64.0.1',
  '0.0.0.0',
  '224.0.0.1',
  '::1',
  '::',
  'fe80::1',
  'fd00::1',
  '::ffff:10.0.0.1',
  '::ffff:93.184.216.34',
  '::ffff:7f00:1',
  '::ffff:a9fe:a9fe',
  '::ffff:a00:1',
  '::ffff:5db8:d822',
  '64:ff9b::a00:1',
  '::7f00:1',
  '0:0:0:0:0:ffff:127.0.0.1',
  '2001:db8::1',
  'not:an:address::x',
  '2606:4700::6810:84e5',
];

const cacheControls = [null, 'max-age=600', 'public, max-age=86400', 'max-age=999999', 'no-store', 'no-cache, max-age=600', 'private'];

function vectors() {
  return {
    comment:
      "The OAuth authorization server's pure parts (#695). tokens: an access token's and a refresh token's text from bytes (hex), and the hash a row keeps of each. credentials: what extractCredential reads of headers with the cookie named session. config: the identity config's oauth member read as the identity_parity.json config section reads a config, want null for a refusal. pkce: whether a verifier answers a challenge under S256, and the challenge of each verifier in verifiers. redirects: whether given is one of registered, the port ignored on loopback. redirectKinds: whether a URI is a redirect a client may name, and whether it is loopback. publicBases: the API's public base from the config and the request. metadata: the protected resource and authorization server metadata, the discovery paths, and the MCP endpoint's challenge without and with a refused token. scopes: a scope's names, null for one refused. metadataUrls: why a client_id is no metadata document's URL, null for one that is. privateAddresses: whether a metadata document's fetch refuses an address. cacheSeconds: how long a document is kept for a Cache-Control. runtime/http/typescript's src/identity/oauth-parity.test.ts writes the wants with OAUTH_PARITY_WRITE=1.",
    tokens: tokenBytes.map(hex => {
      const random = () => new Uint8Array(Buffer.from(hex, 'hex'));
      const accessToken = newOAuthAccessToken(random);
      const refreshToken = newOAuthRefreshToken(random);
      return { bytes: hex, access: accessToken, accessHash: hashToken(accessToken), refresh: refreshToken, refreshHash: hashToken(refreshToken) };
    }),
    credentials: credentials.map(c => ({ ...c, want: extractCredential(c.headers, 'session') })),
    config: configs.map(c => {
      let want: unknown = null;
      try {
        want = parseIdentityConfig(c.input).oauth;
      } catch {
        want = null;
      }
      return { ...c, want };
    }),
    verifiers: verifiers.map(verifier => ({ verifier, challenge: codeChallengeOf(verifier) })),
    pkce: pkce.map(c => ({ ...c, want: verifyCodeChallenge(c.verifier, c.challenge) })),
    redirects: redirects.map(c => ({ ...c, want: redirectMatches(c.registered, c.given) })),
    redirectKinds: redirectKinds.map(uri => ({ uri, allowed: isAllowedRedirect(uri), loopback: isLoopbackRedirect(uri) })),
    publicBases: bases.map(c => ({ ...c, want: publicBaseOf(c.configured, c.url, c.host, c.prefix) })),
    metadata: metadata.map(options => ({
      options,
      protectedResource: protectedResourceMetadata(options),
      authorizationServer: authorizationServerMetadata(options),
      paths: discoveryPaths(options.base),
      challenge: bearerChallenge(options.base, false),
      invalidTokenChallenge: bearerChallenge(options.base, true),
    })),
    scopes: scopes.map(scope => ({ scope, want: scopeNames(scope) ?? null })),
    metadataUrls: metadataUrls.map(url => ({ url, want: metadataDocumentUrlProblem(url) ?? null })),
    privateAddresses: addresses.map(address => ({ address, want: isPrivateAddress(address) })),
    cacheSeconds: cacheControls.map(cacheControl => ({ cacheControl, want: cacheSecondsOf(cacheControl) })),
  };
}

describe('oauth parity', () => {
  test('the vectors are current', () => {
    const want = `${JSON.stringify(vectors(), null, 2)}\n`;
    if (process.env.OAUTH_PARITY_WRITE === '1') writeFileSync(CORPUS, want);
    expect(readFileSync(CORPUS, 'utf8')).toBe(want);
  });

  test('the RFC 7636 example verifier answers its challenge', () => {
    expect(codeChallengeOf('dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk')).toBe('E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM');
  });
});
