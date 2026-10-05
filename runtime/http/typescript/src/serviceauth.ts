import type { OperationServiceCallers, RequestContext } from './operation.js';
import { serviceForbidden, serviceUnauthorized, serviceUnavailable } from './problem.js';

/*
Service callers (D37, section 9.5 of docs/stack-model.md). A deployable that
calls this server sends a short-lived JWT in `Service-Authorization: Bearer
<token>`, beside the end user's own Authorization. A ServiceAuthenticator
turns it into a ServiceCaller: the calling deployable, the APIs it serves and
the credential's subject. It never reads Authorization, and the end-user
Authenticator never reads Service-Authorization.

serviceAuthenticator is the implementation every v1 platform's credential
fits: a Google ID token on Cloud Run, a projected service account token on
Kubernetes, a token signed with an edge's Ed25519 key elsewhere. What is
specific to a platform is data in ServiceAuthConfig (issuers, keys or where
to fetch them, the audience, the claim that names the caller, and the
callers). It verifies with WebCrypto alone, so it runs on Node.js 22.13 and
later, Bun and Cloudflare Workers, and links no cloud library (D6).

The route applies its @requireService or @allowService clause to the caller
with authorizeService, as it applies the user clause with authorize.
*/

/** The calling service, on ctx.serviceCaller. */
export interface ServiceCaller {
  /** The calling deployable's name. */
  readonly deployable: string;
  /** The API services the caller serves; a route's `from` is checked against these. */
  readonly serves: readonly string[];
  /** The credential's subject (the caller's identity at its issuer), for logs. */
  readonly subject: string;
}

/**
 * Establishes the calling service: null when the request carries no service
 * credential. It throws an HttpProblem to refuse: 401 service_unauthorized
 * for a credential that does not verify, 403 service_forbidden for a
 * verified identity that is no caller of this server, 503 when it cannot
 * check one.
 */
export type ServiceAuthenticator = (ctx: RequestContext) => Promise<ServiceCaller | null>;

/** The algorithms a credential may be signed with. A token's `alg` of `Ed25519` (RFC 9864) is read as `EdDSA`. */
export type ServiceAuthAlgorithm = 'RS256' | 'ES256' | 'EdDSA';

/** A public JSON Web Key, as the config or a JWKS lists it. */
export interface PublicJwk {
  readonly kty: string;
  readonly kid?: string;
  readonly crv?: string;
  readonly x?: string;
  readonly y?: string;
  readonly n?: string;
  readonly e?: string;
  readonly [member: string]: unknown;
}

/** The deployable an identity is, and the APIs it serves (none when absent). */
export interface ServiceCallerConfig {
  readonly deployable: string;
  readonly serves?: readonly string[];
}

/** One issuer whose tokens this server accepts. */
export interface ServiceAuthIssuer {
  /** The `iss` claim. */
  readonly issuer: string;
  /** Other spellings of `iss` for the same issuer (Google's `accounts.google.com`). */
  readonly issuerAliases?: readonly string[];
  /** This server's audience: `aud` must contain it. */
  readonly audience: string;
  readonly algorithms: readonly ServiceAuthAlgorithm[];
  /**
   * Where to fetch the issuer's keys. An entry has this or `keys`, not both.
   * A fetched key needs a kid and, when it has a `use`, `sig`; any other is
   * left out, as is one the verifier could not use.
   */
  readonly jwksUrl?: string;
  /** With jwksUrl: a file whose trimmed contents the fetch sends as `Authorization: Bearer` (Kubernetes). */
  readonly jwksBearerTokenFile?: string;
  /** The issuer's public keys, when they are not fetched; each with its own kid. */
  readonly keys?: readonly PublicJwk[];
  /** The claim that names the caller; `sub` by default. */
  readonly subjectClaim?: string;
  /** The longest `exp - iat` accepted; with it, `iat` is required. 0 or absent is no limit. */
  readonly maxLifetimeSeconds?: number;
  /** The callers, by the subject claim's value. An identity not listed is no caller. */
  readonly callers: Readonly<Record<string, ServiceCallerConfig>>;
}

/** The service authenticator's config, the same JSON in the Go, TypeScript and Rust runtimes. */
export interface ServiceAuthConfig {
  readonly issuers: readonly ServiceAuthIssuer[];
  /** Clock skew allowed on `exp`, `nbf` and `iat`; 60 seconds by default. */
  readonly leewaySeconds?: number;
}

export interface ServiceAuthenticatorOptions {
  /** Clock in milliseconds, for tests; Date.now by default. */
  readonly now?: () => number;
  /**
   * Fetches a JWKS and resolves its parsed JSON, sending `bearer` as
   * `Authorization: Bearer` when given; rejects when it cannot. The global
   * fetch by default, with a 10-second timeout.
   */
  readonly fetchKeys?: (url: string, bearer?: string) => Promise<unknown>;
  /**
   * Reads a jwksBearerTokenFile. By default node:fs/promises, reached
   * through process.getBuiltinModule so the module imports nothing from
   * node: (Node.js and Bun have it; elsewhere pass this).
   */
  readonly readFile?: (path: string) => Promise<string>;
}

const SERVICE_AUTHORIZATION = 'service-authorization';
const DEFAULT_LEEWAY_SECONDS = 60;
/** A JWKS is fetched at most this often per URL, whatever prompts the fetch. */
const JWKS_MIN_INTERVAL_MS = 60_000;
/** Keys fetched longer ago than this are fetched again. */
const JWKS_MAX_AGE_MS = 60 * 60_000;
const JWKS_FETCH_TIMEOUT_MS = 10_000;
/** The smallest RSA modulus accepted, as RFC 7518 requires for RS256. */
const MIN_RSA_BITS = 2048;

type KeyKind = 'RSA' | 'EC' | 'OKP';

/** The key each algorithm signs with. A key of another kind never verifies a token, which stops an alg confusion. */
const KIND_OF: Record<ServiceAuthAlgorithm, KeyKind> = { RS256: 'RSA', ES256: 'EC', EdDSA: 'OKP' };

/** Each kind's WebCrypto parameters, and the members it imports. */
const KINDS: Record<
  KeyKind,
  {
    readonly material: (jwk: PublicJwk) => JsonWebKey;
    readonly importAs: RsaHashedImportParams | EcKeyImportParams | Algorithm;
    readonly verifyAs: Algorithm | EcdsaParams;
  }
> = {
  RSA: {
    material: jwk => ({ kty: 'RSA', n: unpadded(jwk.n), e: unpadded(jwk.e) }),
    importAs: { name: 'RSASSA-PKCS1-v1_5', hash: 'SHA-256' },
    verifyAs: { name: 'RSASSA-PKCS1-v1_5' },
  },
  EC: {
    material: jwk => ({ kty: 'EC', crv: 'P-256', x: unpadded(jwk.x), y: unpadded(jwk.y) }),
    importAs: { name: 'ECDSA', namedCurve: 'P-256' },
    verifyAs: { name: 'ECDSA', hash: 'SHA-256' },
  },
  OKP: {
    material: jwk => ({ kty: 'OKP', crv: 'Ed25519', x: unpadded(jwk.x) }),
    importAs: { name: 'Ed25519' },
    verifyAs: { name: 'Ed25519' },
  },
};

function isAlgorithm(value: unknown): value is ServiceAuthAlgorithm {
  return typeof value === 'string' && Object.hasOwn(KIND_OF, value);
}

/**
 * Applies a route's service clause to the caller the service step
 * established, throwing 401 or 403 with the service codes. Resolves true
 * when the caller stands in for the end user (a listed caller on an
 * @allowService route), so the end-user step is skipped; false when the
 * end-user step runs.
 */
export function authorizeService(caller: ServiceCaller | null, service: OperationServiceCallers | undefined): boolean {
  if (!service) return false;
  const listed = caller !== null && (service.from.length === 0 || caller.serves.some(api => service.from.includes(api)));
  if (service.mode === 'allow') return listed;
  if (!caller) throw serviceUnauthorized('Service credential required');
  if (!listed) throw serviceForbidden();
  return false;
}

/**
 * The standard ServiceAuthenticator over a ServiceAuthConfig, verifying in
 * the order every runtime shares (D37):
 *
 *  1. no Service-Authorization header: no caller;
 *  2. not one `Bearer <token>` (the scheme in any case, one space, a token
 *     without whitespace): 401;
 *  3. not three unpadded base64url segments whose first two are JSON
 *     objects: 401;
 *  4. the payload's `iss` selects the entry by issuer or alias; none: 401;
 *  5. the header's `alg` (`Ed25519` read as `EdDSA`) not in the entry's
 *     algorithms, or no `kid`: 401;
 *  6. the key with that kid, static or from the JWKS cache: none fetched
 *     and none cached is 503; kid unknown, or a key of another kind than
 *     `alg` signs with, is 401;
 *  7. the signature over `<header>.<payload>` does not verify: 401;
 *  8. the claims, with the leeway L: `exp` a number and now < exp + L;
 *     `nbf` and `iat`, when present, numbers no later than now + L; `aud`,
 *     a string or a list of strings, holding the entry's audience; with a
 *     maximum lifetime M, `iat` present and exp - iat <= M. Else 401;
 *  9. the subject claim names a caller of the entry: else 403.
 *
 * It throws on a config it cannot use. Each authenticator keeps its own
 * JWKS cache.
 */
export function serviceAuthenticator(config: ServiceAuthConfig, options: ServiceAuthenticatorOptions = {}): ServiceAuthenticator {
  const issuers = issuersOf(config);
  const leeway = config.leewaySeconds ?? DEFAULT_LEEWAY_SECONDS;
  const now = options.now ?? Date.now;
  const keys = new KeySource(options.fetchKeys ?? fetchJwks, options.readFile ?? readTextFile, now);
  return async ctx => {
    const token = serviceTokenOf(ctx.headers);
    if (token === undefined) return null;
    return verify(token, issuers, keys, leeway, now);
  };
}

/** A 401 for a credential that does not verify; the reason stays off the wire, on the problem's cause. */
function invalid(reason: string): never {
  throw serviceUnauthorized('Invalid service credential', { cause: new Error(reason) });
}

/**
 * The token in Service-Authorization: undefined when there is no header; a
 * 401 unless it is `Bearer` (any case), one space and a token without
 * whitespace. Two headers arrive joined by a comma, so are refused.
 */
function serviceTokenOf(headers: Headers): string | undefined {
  const value = headers.get(SERVICE_AUTHORIZATION);
  if (value === null) return undefined;
  const space = value.indexOf(' ');
  const token = value.slice(space + 1);
  if (space < 0 || value.slice(0, space).toLowerCase() !== 'bearer' || token === '' || /[ \t]/u.test(token)) {
    invalid('Service-Authorization is not a Bearer token');
  }
  return token;
}

/** A public key the verifier can use, with its imported CryptoKey once a token has needed it. */
interface UsableKey {
  readonly jwk: PublicJwk;
  readonly kind: KeyKind;
  imported?: Promise<CryptoKey>;
}

interface Issuer {
  readonly config: ServiceAuthIssuer;
  readonly algorithms: ReadonlySet<ServiceAuthAlgorithm>;
  readonly subjectClaim: string;
  /** The static keys by kid; undefined when the keys are fetched. */
  readonly keys?: ReadonlyMap<string, UsableKey>;
}

async function verify(token: string, issuers: ReadonlyMap<string, Issuer>, keys: KeySource, leeway: number, now: () => number): Promise<ServiceCaller> {
  const segments = token.split('.');
  if (segments.length !== 3) invalid('not a compact JWS');
  const [encodedHeader, encodedPayload, encodedSignature] = segments as [string, string, string];
  const header = jsonSegment(encodedHeader);
  const payload = jsonSegment(encodedPayload);
  const signature = base64urlDecode(encodedSignature);
  if (!header || !payload || !signature) invalid('a segment is not base64url JSON');

  const iss = payload.iss;
  const issuer = typeof iss === 'string' ? issuers.get(iss) : undefined;
  if (!issuer) invalid('unknown issuer');
  const alg = header.alg === 'Ed25519' ? 'EdDSA' : header.alg;
  if (!isAlgorithm(alg) || !issuer.algorithms.has(alg)) invalid('algorithm not accepted');
  const kid = header.kid;
  if (typeof kid !== 'string' || kid === '') invalid('no kid');

  const key = issuer.keys ? (issuer.keys.get(kid) ?? invalid('unknown kid')) : await keys.find(issuer.config, kid);
  if (key.kind !== KIND_OF[alg]) invalid('key does not fit the algorithm');
  if (alg === 'ES256' && signature.length !== 64) invalid('ES256 signature is not 64 bytes');
  const kind = KINDS[key.kind];
  let verified = false;
  try {
    key.imported ??= crypto.subtle.importKey('jwk', kind.material(key.jwk), kind.importAs, false, ['verify']);
    verified = await crypto.subtle.verify(kind.verifyAs, await key.imported, signature, new TextEncoder().encode(`${encodedHeader}.${encodedPayload}`));
  } catch {
    verified = false;
  }
  if (!verified) invalid('bad signature');

  checkClaims(payload, issuer.config, leeway, Math.floor(now() / 1000));

  const subject = payload[issuer.subjectClaim];
  if (typeof subject !== 'string' || !Object.hasOwn(issuer.config.callers, subject)) {
    throw serviceForbidden(undefined, { cause: new Error('identity is no caller of this server') });
  }
  const caller = issuer.config.callers[subject]!;
  return { deployable: caller.deployable, serves: [...(caller.serves ?? [])], subject };
}

/** A NumericDate claim: undefined when absent, a 401 when present and not a number (null included). */
function numericClaim(payload: Record<string, unknown>, name: string): number | undefined {
  if (!Object.hasOwn(payload, name)) return undefined;
  const value = payload[name];
  if (typeof value !== 'number' || !Number.isFinite(value)) invalid(`${name} is not a number`);
  return value;
}

function checkClaims(payload: Record<string, unknown>, issuer: ServiceAuthIssuer, leeway: number, now: number): void {
  const exp = numericClaim(payload, 'exp');
  if (exp === undefined) invalid('no exp');
  if (now >= exp + leeway) invalid('expired');
  const nbf = numericClaim(payload, 'nbf');
  if (nbf !== undefined && now + leeway < nbf) invalid('not yet valid (nbf)');
  const iat = numericClaim(payload, 'iat');
  if (iat !== undefined && now + leeway < iat) invalid('not yet valid (iat)');
  const aud = payload.aud;
  const audiences = typeof aud === 'string' ? [aud] : Array.isArray(aud) && aud.every(member => typeof member === 'string') ? (aud as string[]) : [];
  if (!audiences.includes(issuer.audience)) invalid('wrong audience');
  if (issuer.maxLifetimeSeconds) {
    if (iat === undefined) invalid('no iat');
    if (exp - iat > issuer.maxLifetimeSeconds) invalid('lifetime too long');
  }
}

interface RemoteKeys {
  /** By kid; undefined until a fetch succeeds. */
  keys?: ReadonlyMap<string, UsableKey>;
  fetchedAt: number;
  attemptedAt: number;
  error?: unknown;
  pending?: Promise<void>;
}

/**
 * The keys of issuers that publish a JWKS, cached per URL. Keys stay until
 * a fetch replaces them; a failed fetch keeps what is cached. A lookup
 * fetches when nothing is cached, when the keys are an hour old, or when
 * the kid is unknown, and at most once a minute per URL. One fetch runs at
 * a time: a lookup whose kid is cached uses the cached key meanwhile, and
 * one whose kid is not waits for the fetch.
 */
class KeySource {
  private readonly remotes = new Map<string, RemoteKeys>();

  constructor(
    private readonly fetchKeys: (url: string, bearer?: string) => Promise<unknown>,
    private readonly readFile: (path: string) => Promise<string>,
    private readonly now: () => number
  ) {}

  async find(issuer: ServiceAuthIssuer, kid: string): Promise<UsableKey> {
    const url = issuer.jwksUrl!;
    let remote = this.remotes.get(url);
    if (!remote) {
      remote = { fetchedAt: 0, attemptedAt: -Infinity };
      this.remotes.set(url, remote);
    }
    for (;;) {
      const now = this.now();
      const known = remote.keys?.get(kid);
      if (known && now - remote.fetchedAt < JWKS_MAX_AGE_MS) return known;
      if (remote.pending) {
        if (known) return known;
        await remote.pending;
        continue;
      }
      if (now - remote.attemptedAt < JWKS_MIN_INTERVAL_MS) {
        if (known) return known;
        if (!remote.keys) throw this.unavailable(remote);
        invalid('unknown kid');
      }
      await this.refresh(remote, issuer, now);
      if (!remote.keys) throw this.unavailable(remote);
      // Look again: the fetch may have brought the kid, or not.
    }
  }

  private unavailable(remote: RemoteKeys) {
    return serviceUnavailable('Service credential could not be checked', { cause: remote.error });
  }

  private async refresh(remote: RemoteKeys, issuer: ServiceAuthIssuer, now: number): Promise<void> {
    remote.attemptedAt = now;
    const pending = this.fetch(remote, issuer, now);
    remote.pending = pending;
    try {
      await pending;
    } finally {
      if (remote.pending === pending) remote.pending = undefined;
    }
  }

  private async fetch(remote: RemoteKeys, issuer: ServiceAuthIssuer, now: number): Promise<void> {
    try {
      const bearer = issuer.jwksBearerTokenFile ? (await this.readFile(issuer.jwksBearerTokenFile)).trim() : '';
      remote.keys = fetchedKeys(await this.fetchKeys(issuer.jwksUrl!, bearer || undefined));
      remote.fetchedAt = now;
      remote.error = undefined;
    } catch (error) {
      remote.error = error;
    }
  }
}

/**
 * The usable keys of a JWKS document, by kid. A key without a kid, with a
 * `use` other than `sig`, that the verifier cannot use, or whose kid an
 * earlier key has, is left out.
 */
function fetchedKeys(document: unknown): Map<string, UsableKey> {
  if (!isObject(document) || !Array.isArray(document.keys)) throw new Error('the key set has no keys list');
  const keys = new Map<string, UsableKey>();
  for (const jwk of document.keys) {
    if (!isObject(jwk) || !isNonEmptyString(jwk.kid) || keys.has(jwk.kid) || (jwk.use !== undefined && jwk.use !== '' && jwk.use !== 'sig')) continue;
    try {
      keys.set(jwk.kid, { jwk: jwk as PublicJwk, kind: keyKindOf(jwk as PublicJwk) });
    } catch {
      // Not a key this verifier can use.
    }
  }
  return keys;
}

/** A JWK member's bytes; `=` padding, which RFC 7518 leaves out, is tolerated. */
function keyBytes(jwk: PublicJwk, name: string): Uint8Array {
  const value = jwk[name];
  if (!isNonEmptyString(value)) throw new Error(`the key has no ${name}`);
  const bytes = base64urlDecode(unpadded(value)!);
  if (!bytes) throw new Error(`the key's ${name} is not base64url`);
  return bytes;
}

function unpadded(value: string | undefined): string | undefined {
  return value?.replace(/=+$/u, '');
}

function bitLength(bytes: Uint8Array): number {
  const start = bytes.findIndex(byte => byte !== 0);
  if (start < 0) return 0;
  return (bytes.length - start - 1) * 8 + (32 - Math.clz32(bytes[start]!));
}

/**
 * The kind of a public key the verifier can use, or an Error saying why it
 * cannot: a private member, an RSA modulus under 2048 bits or an exponent
 * that is not an odd integer from 3 to 2^31-1, an EC key not on P-256 or an
 * OKP key not Ed25519, or coordinates of the wrong size. A P-256 point off
 * the curve passes here and fails WebCrypto's import, which refuses the
 * token.
 */
function keyKindOf(jwk: PublicJwk): KeyKind {
  if (jwk.d !== undefined && jwk.d !== '') throw new Error('the key carries a private member (d)');
  switch (jwk.kty) {
    case 'RSA': {
      if (bitLength(keyBytes(jwk, 'n')) < MIN_RSA_BITS) throw new Error(`the RSA modulus has fewer than ${MIN_RSA_BITS} bits`);
      const exponent = keyBytes(jwk, 'e').reduce((value, byte) => value * 256n + BigInt(byte), 0n);
      if (exponent < 3n || exponent > 2n ** 31n - 1n || exponent % 2n === 0n) throw new Error('the RSA exponent is not an odd integer from 3 to 2^31-1');
      return 'RSA';
    }
    case 'EC':
      if (jwk.crv !== 'P-256') throw new Error(`the EC curve ${JSON.stringify(jwk.crv)} is not P-256`);
      if (keyBytes(jwk, 'x').length !== 32 || keyBytes(jwk, 'y').length !== 32) throw new Error('the P-256 coordinates are not 32 bytes each');
      return 'EC';
    case 'OKP':
      if (jwk.crv !== 'Ed25519') throw new Error(`the OKP curve ${JSON.stringify(jwk.crv)} is not Ed25519`);
      if (keyBytes(jwk, 'x').length !== 32) throw new Error('the Ed25519 key is not 32 bytes');
      return 'OKP';
    case undefined:
    case '':
      throw new Error('the key has no kty');
    default:
      throw new Error(`the key type ${JSON.stringify(jwk.kty)} is not supported`);
  }
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

async function fetchJwks(url: string, bearer?: string): Promise<unknown> {
  const headers: Record<string, string> = { accept: 'application/json' };
  if (bearer) headers.authorization = `Bearer ${bearer}`;
  const response = await fetch(url, { headers, signal: AbortSignal.timeout(JWKS_FETCH_TIMEOUT_MS) });
  if (!response.ok) throw new Error(`GET ${url}: status ${response.status}`);
  return response.json();
}

interface FsPromisesLike {
  readFile(path: string, encoding: 'utf8'): Promise<string>;
}

/**
 * Reads a text file with node:fs/promises, reached through
 * process.getBuiltinModule so that nothing here imports from node: and a
 * Workers bundle of this module stays clean.
 */
export async function readTextFile(path: string): Promise<string> {
  const getBuiltinModule = (globalThis as { process?: { getBuiltinModule?: (id: string) => unknown } }).process?.getBuiltinModule;
  const fs = getBuiltinModule?.('node:fs/promises') as FsPromisesLike | undefined;
  if (!fs) throw new Error(`cannot read ${path}: this runtime has no node:fs; pass readFile`);
  return fs.readFile(path, 'utf8');
}

const BASE64URL = /^[A-Za-z0-9_-]*$/u;

/** Unpadded base64url to bytes; undefined when the text is not that. */
export function base64urlDecode(text: string): Uint8Array<ArrayBuffer> | undefined {
  if (!BASE64URL.test(text) || text.length % 4 === 1) return undefined;
  const binary = atob(text.replace(/-/gu, '+').replace(/_/gu, '/') + '='.repeat((4 - (text.length % 4)) % 4));
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index++) bytes[index] = binary.charCodeAt(index);
  return bytes;
}

/** Bytes to unpadded base64url. */
export function base64urlEncode(bytes: Uint8Array): string {
  let binary = '';
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary).replace(/\+/gu, '-').replace(/\//gu, '_').replace(/=+$/u, '');
}

/** A JWS header or payload: a JSON object, or undefined. */
function jsonSegment(segment: string): Record<string, unknown> | undefined {
  const bytes = base64urlDecode(segment);
  if (!bytes) return undefined;
  try {
    const value: unknown = JSON.parse(new TextDecoder().decode(bytes));
    return isObject(value) ? value : undefined;
  } catch {
    return undefined;
  }
}

/** The payload of a compact JWS, unverified; undefined when it is not one. For a credential source's own token. */
export function unverifiedPayload(token: string): Record<string, unknown> | undefined {
  const segments = token.split('.');
  return segments.length === 3 ? jsonSegment(segments[1]!) : undefined;
}

function configError(message: string): never {
  throw new Error(`service auth config: ${message}`);
}

function isNonEmptyString(value: unknown): value is string {
  return typeof value === 'string' && value !== '';
}

/** The entry's static keys by kid; each must have a kid of its own and be a key the verifier can use. */
function staticKeys(keys: readonly PublicJwk[], at: string): Map<string, UsableKey> {
  const usable = new Map<string, UsableKey>();
  keys.forEach((jwk, index) => {
    const value: unknown = jwk;
    if (!isObject(value) || !isNonEmptyString(jwk.kid)) configError(`${at}.keys[${index}] has no kid`);
    if (usable.has(jwk.kid)) configError(`${at}.keys[${index}]: kid ${JSON.stringify(jwk.kid)} repeats`);
    try {
      usable.set(jwk.kid, { jwk, kind: keyKindOf(jwk) });
    } catch (error) {
      configError(`${at}.keys[${index}] (${JSON.stringify(jwk.kid)}): ${(error as Error).message}`);
    }
  });
  return usable;
}

/** Checks the config and indexes its issuers by `issuer` and every alias. */
function issuersOf(config: ServiceAuthConfig): Map<string, Issuer> {
  if (!isObject(config) || !Array.isArray(config.issuers)) configError('issuers must be a list');
  const leeway = config.leewaySeconds;
  if (leeway !== undefined && !(typeof leeway === 'number' && leeway >= 0)) configError('leewaySeconds must be a number, 0 or more');
  const issuers = new Map<string, Issuer>();
  config.issuers.forEach((entry, index) => {
    const at = `issuers[${index}]`;
    const value: unknown = entry;
    if (!isObject(value) || !isNonEmptyString(entry.issuer)) configError(`${at}.issuer is required`);
    if (!isNonEmptyString(entry.audience)) configError(`${at}.audience is required`);
    if (!Array.isArray(entry.algorithms) || entry.algorithms.length === 0) configError(`${at}.algorithms is required`);
    for (const alg of entry.algorithms) {
      if (!isAlgorithm(alg)) configError(`${at}.algorithms has ${JSON.stringify(alg)}; accepted are RS256, ES256 and EdDSA`);
    }
    if (entry.keys !== undefined && !Array.isArray(entry.keys)) configError(`${at}.keys must be a list of JWKs`);
    if (entry.jwksUrl !== undefined && typeof entry.jwksUrl !== 'string') configError(`${at}.jwksUrl must be a URL`);
    // An empty jwksUrl or keys list is none, as Go's zero values read.
    const fetched = entry.jwksUrl !== undefined && entry.jwksUrl !== '';
    const listed = entry.keys !== undefined && entry.keys.length > 0;
    if (fetched === listed) configError(`${at} needs jwksUrl or keys, not both`);
    if (entry.jwksBearerTokenFile && !fetched) configError(`${at}.jwksBearerTokenFile is read with jwksUrl only`);
    if (entry.subjectClaim !== undefined && typeof entry.subjectClaim !== 'string') configError(`${at}.subjectClaim must be a claim name`);
    const max = entry.maxLifetimeSeconds;
    if (max !== undefined && !(typeof max === 'number' && max >= 0)) configError(`${at}.maxLifetimeSeconds must be a number, 0 or more`);
    if (!isObject(entry.callers)) configError(`${at}.callers is required`);
    for (const [subject, caller] of Object.entries(entry.callers)) {
      const serves: unknown = isObject(caller) ? caller.serves : undefined;
      if (!isObject(caller) || !isNonEmptyString(caller.deployable) || !(serves === undefined || (Array.isArray(serves) && serves.every(api => typeof api === 'string')))) {
        configError(`${at}.callers[${JSON.stringify(subject)}] needs a deployable, and the APIs it serves as a list`);
      }
    }
    const issuer: Issuer = {
      config: entry,
      algorithms: new Set(entry.algorithms),
      subjectClaim: entry.subjectClaim || 'sub',
      ...(listed ? { keys: staticKeys(entry.keys!, at) } : {}),
    };
    for (const name of [entry.issuer, ...(entry.issuerAliases ?? [])]) {
      if (!isNonEmptyString(name)) configError(`${at}.issuerAliases must be issuer names`);
      if (issuers.has(name)) configError(`issuer ${JSON.stringify(name)} is listed twice`);
      issuers.set(name, issuer);
    }
  });
  return issuers;
}
