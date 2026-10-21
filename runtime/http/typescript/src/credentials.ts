import { base64urlEncode, ed25519Pkcs8FromPem, ed25519Thumbprint, readTextFile, unverifiedPayload } from './serviceauth.js';

/*
Service credential sources (D37, section 9.6 of docs/stack-model.md): what a
server that calls another one sends in Service-Authorization. One per row of
section 9.2's table: a Google ID token from the metadata server (Cloud Run),
a projected service account token read from its file (Kubernetes), and a
token signed with an edge's Ed25519 key (the generic connector and `local`).

Each is a ServiceTokenSource, the shape an SDK's `serviceCredential.token`
takes: it caches its token and gets or signs a new one before expiry, and
`fresh` (asked after a 401 service_unauthorized) skips the cache. Like the
verifier, none imports from node:, so each runs on Node.js, Bun and Workers
wherever what it reads exists.
*/

/** A service credential: a token for Service-Authorization; `fresh` asks for a new one rather than the cached one. */
export type ServiceTokenSource = (fresh: boolean) => Promise<string>;

/** A Google ID token is fetched again this long before it expires. */
const GOOGLE_REFRESH_BEFORE_MS = 5 * 60_000;
/** A token file is read again when the token read from it is older than this. */
const TOKEN_FILE_MAX_AGE_MS = 60_000;
/** A signed token lives this long. */
const SIGNED_LIFETIME_SECONDS = 300;
/** A signed token is signed again when less than this remains. */
const SIGNED_RESIGN_BEFORE_MS = 60_000;

export interface GoogleIdTokenSourceOptions {
  /** The metadata server's host; GCE_METADATA_HOST when set, else metadata.google.internal. */
  readonly metadataHost?: string;
  /** The global fetch by default. */
  readonly fetch?: (url: string, init: { headers: Record<string, string> }) => Promise<Response>;
  /** Clock in milliseconds, for tests; Date.now by default. */
  readonly now?: () => number;
}

/** Shares one fetch between concurrent callers. */
function singleFlight<T>(work: () => Promise<T>): () => Promise<T> {
  let pending: Promise<T> | undefined;
  return () => {
    pending ??= work().finally(() => {
      pending = undefined;
    });
    return pending;
  };
}

function metadataHostOf(options: GoogleIdTokenSourceOptions): string {
  if (options.metadataHost) return options.metadataHost;
  const env = (globalThis as { process?: { env?: Record<string, string | undefined> } }).process?.env;
  return env?.GCE_METADATA_HOST || 'metadata.google.internal';
}

/**
 * A Google ID token whose audience is `audience` (the callee's URL), from
 * the metadata server of the Cloud Run service or VM this runs on. It is
 * cached until 5 minutes before its `exp`.
 */
export function googleIdTokenSource(audience: string, options: GoogleIdTokenSourceOptions = {}): ServiceTokenSource {
  const now = options.now ?? Date.now;
  const request = options.fetch ?? ((url, init) => fetch(url, init));
  let cached: { token: string; refreshAt: number } | undefined;
  const fetchToken = singleFlight(async () => {
    const url = `http://${metadataHostOf(options)}/computeMetadata/v1/instance/service-accounts/default/identity?audience=${encodeURIComponent(audience)}`;
    const response = await request(url, { headers: { 'metadata-flavor': 'Google' } });
    if (!response.ok) throw new Error(`the metadata server answered ${response.status} for an ID token`);
    const token = (await response.text()).trim();
    const exp = unverifiedPayload(token)?.exp;
    // A token whose exp cannot be read is not cached.
    cached = { token, refreshAt: typeof exp === 'number' ? exp * 1000 - GOOGLE_REFRESH_BEFORE_MS : 0 };
    return token;
  });
  return async fresh => {
    if (!fresh && cached && now() < cached.refreshAt) return cached.token;
    return fetchToken();
  };
}

/**
 * An OAuth access token of the account the Cloud Run service or VM this
 * runs on runs as, from its metadata server, as Google's APIs take in
 * Authorization: Bearer, such as Cloud Trace's OTLP endpoint (D67,
 * amended). It is cached until 5 minutes before it expires.
 */
export function googleAccessTokenSource(options: GoogleIdTokenSourceOptions = {}): ServiceTokenSource {
  const now = options.now ?? Date.now;
  const request = options.fetch ?? ((url, init) => fetch(url, init));
  let cached: { token: string; refreshAt: number } | undefined;
  const fetchToken = singleFlight(async () => {
    const url = `http://${metadataHostOf(options)}/computeMetadata/v1/instance/service-accounts/default/token`;
    const response = await request(url, { headers: { 'metadata-flavor': 'Google' } });
    if (!response.ok) throw new Error(`the metadata server answered ${response.status} for an access token`);
    const answer = (await response.json()) as { access_token?: unknown; expires_in?: unknown };
    if (typeof answer.access_token !== 'string' || answer.access_token === '') {
      throw new Error('the metadata server answered no access token');
    }
    const expiresIn = typeof answer.expires_in === 'number' ? answer.expires_in : 0;
    cached = { token: answer.access_token, refreshAt: now() + expiresIn * 1000 - GOOGLE_REFRESH_BEFORE_MS };
    return answer.access_token;
  });
  return async fresh => {
    if (!fresh && cached && now() < cached.refreshAt) return cached.token;
    return fetchToken();
  };
}

export interface TokenFileSourceOptions {
  /** Reads the file; node:fs/promises through process.getBuiltinModule by default. */
  readonly readFile?: (path: string) => Promise<string>;
  /** Clock in milliseconds, for tests; Date.now by default. */
  readonly now?: () => number;
}

/**
 * The token in a file, trimmed: a Kubernetes projected service account
 * token, which the kubelet replaces before it expires. The file is read
 * again when the token is a minute old.
 */
export function tokenFileSource(path: string, options: TokenFileSourceOptions = {}): ServiceTokenSource {
  const now = options.now ?? Date.now;
  const readFile = options.readFile ?? readTextFile;
  let cached: { token: string; readAt: number } | undefined;
  const read = singleFlight(async () => {
    const readAt = now();
    const token = (await readFile(path)).trim();
    if (token === '') throw new Error(`the token file ${path} is empty`);
    cached = { token, readAt };
    return token;
  });
  return async fresh => {
    if (!fresh && cached && now() - cached.readAt < TOKEN_FILE_MAX_AGE_MS) return cached.token;
    return read();
  };
}

/** An Ed25519 private key as a JWK; `kid` is its RFC 7638 thumbprint, which the callee's config lists. */
export interface Ed25519PrivateJwk {
  readonly kty: 'OKP';
  readonly crv: 'Ed25519';
  readonly d: string;
  readonly x: string;
  readonly kid: string;
}

export interface SignedTokenClaims {
  /** `iss`: the caller's deployable. */
  readonly issuer: string;
  /** `sub`: the caller's deployable. */
  readonly subject: string;
  /** `aud`: the callee's. */
  readonly audience: string;
}

export interface SignedTokenSourceOptions {
  /** Clock in milliseconds, for tests; Date.now by default. */
  readonly now?: () => number;
}

/** A signing key with the kid its tokens carry. */
interface SigningKey {
  readonly key: CryptoKey;
  readonly kid: string;
}

/**
 * A compact JWS signed with an edge's Ed25519 key: header `{"alg":"EdDSA",
 * "kid","typ":"JWT"}`, claims `iss`, `sub`, `aud`, `iat`, `exp` (5 minutes
 * after `iat`) and a random `jti`, in that order. It is signed again when
 * less than a minute remains. The key is an Ed25519 private JWK, as the
 * local target's key pair gives it, or the text of its PKCS#8 PEM
 * (`-----BEGIN PRIVATE KEY-----`, RFC 8410), as the generic connector's key
 * node gives it, whose kid is the key's RFC 7638 thumbprint (D63). Throws
 * at once on a key that is neither.
 */
export function signedTokenSource(privateKey: Ed25519PrivateJwk | string, claims: SignedTokenClaims, options: SignedTokenSourceOptions = {}): ServiceTokenSource {
  let load: () => Promise<SigningKey>;
  if (typeof privateKey === 'string') {
    let der: Uint8Array<ArrayBuffer>;
    try {
      der = ed25519Pkcs8FromPem(privateKey);
    } catch (error) {
      throw new Error(`signedTokenSource: the key must be an Ed25519 private JWK with d, x and kid, or an Ed25519 private key's PKCS#8 PEM: ${(error as Error).message}`);
    }
    load = async () => {
      // The PEM holds the seed alone: the public key, whose thumbprint is
      // the kid, comes from the imported key's JWK.
      const key = await crypto.subtle.importKey('pkcs8', der, { name: 'Ed25519' }, true, ['sign']);
      const { x } = await crypto.subtle.exportKey('jwk', key);
      if (typeof x !== 'string') throw new Error('signedTokenSource: the PKCS#8 key exports no public key');
      return { key, kid: ed25519Thumbprint(x) };
    };
  } else {
    const jwk = privateKey as unknown as Record<string, unknown>;
    if (jwk.kty !== 'OKP' || jwk.crv !== 'Ed25519' || typeof jwk.d !== 'string' || typeof jwk.x !== 'string' || typeof jwk.kid !== 'string' || jwk.kid === '') {
      throw new Error('signedTokenSource: the key must be an Ed25519 private JWK with d, x and kid, or an Ed25519 private key\'s PKCS#8 PEM');
    }
    load = async () => ({
      key: await crypto.subtle.importKey('jwk', { kty: 'OKP', crv: 'Ed25519', d: privateKey.d, x: privateKey.x }, { name: 'Ed25519' }, false, ['sign']),
      kid: privateKey.kid,
    });
  }
  const now = options.now ?? Date.now;
  const encoder = new TextEncoder();
  let signing: Promise<SigningKey> | undefined;
  let cached: { token: string; expiresAt: number } | undefined;
  const sign = async (): Promise<string> => {
    signing ??= load();
    const { key, kid } = await signing;
    const iat = Math.floor(now() / 1000);
    const exp = iat + SIGNED_LIFETIME_SECONDS;
    const jti = base64urlEncode(crypto.getRandomValues(new Uint8Array(16)));
    const header = base64urlEncode(encoder.encode(JSON.stringify({ alg: 'EdDSA', kid, typ: 'JWT' })));
    const payload = base64urlEncode(
      encoder.encode(JSON.stringify({ iss: claims.issuer, sub: claims.subject, aud: claims.audience, iat, exp, jti }))
    );
    const signature = await crypto.subtle.sign({ name: 'Ed25519' }, key, encoder.encode(`${header}.${payload}`));
    const token = `${header}.${payload}.${base64urlEncode(new Uint8Array(signature))}`;
    cached = { token, expiresAt: exp * 1000 };
    return token;
  };
  return async fresh => {
    if (!fresh && cached && cached.expiresAt - now() >= SIGNED_RESIGN_BEFORE_MS) return cached.token;
    return sign();
  };
}
