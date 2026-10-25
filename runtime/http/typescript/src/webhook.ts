import type { MiddlewareHandler } from 'hono';
import { HttpProblem, badRequest, payloadTooLarge, serviceUnavailable } from './problem.js';
import { parseDuration } from './queue-core.js';
import { base64urlDecode } from './serviceauth.js';

/*
jwtBodyHashVerifier (D81): the webhook verifier of a provider that signs each
request with a JWT carrying the SHA-256 of its raw body, as Plaid does (the
Plaid-Verification header, ES256, iat and request_body_sha256). A generated
router takes it as an @hmacVerified provider's verifier. It runs the checks
of the Go (webhook.JWTBodyHashVerifier) and Rust (JwtBodyHashVerifier)
verifiers, in their order, so a request gets the same answer from each:

  1. The header holds a compact JWS: three base64url segments, the first two
     JSON objects. Its alg is the configured one (never none) and its kid a
     non-empty string.
  2. The key with that kid, held or looked up (below). One that cannot be
     looked up, with none held: 503 service_unavailable.
  3. The signature verifies with the key, a P-256 key whose use, when set,
     is sig and whose alg, when set, is the configured one.
  4. iat is a number, at most maxAge in the past and clockSkew in the
     future.
  5. The body, read up to maxBodyBytes (413 payload_too_large past it), has
     the SHA-256 whose lowercase hex the bodyHashClaim holds, compared in
     constant time.

Any other refusal is 401 invalid_signature; its detail names no reason, which
is the problem's cause. The route reads the body after it, from the copy the
router takes.

Keys are held by kid. A held key serves for an hour; then the next request
that needs it looks it up again, and a lookup that fails keeps it. A kid not
held is looked up at once, but the provider is asked at most once a minute,
whatever prompts it, so a flood of random kids costs one lookup a minute.
One lookup runs at a time: a request whose key is held does not wait for it,
and one whose kid is not held waits, then looks again. A function answers
for one kid; a JWKS URL's set replaces every key held.

It verifies with WebCrypto and imports nothing from node:, so it runs on
Node.js, Bun and Cloudflare Workers.
*/

/** The algorithms jwtBodyHashVerifier accepts: ECDSA over P-256 with SHA-256, whose JWS signature is r||s. */
export type JwtWebhookAlgorithm = 'ES256';

/** A provider's public key, as its key endpoint answers it (Plaid's JWKPublicKey fits). */
export interface WebhookJwk {
  readonly kty: string;
  readonly crv?: string;
  readonly x?: string;
  readonly y?: string;
  readonly kid?: string;
  readonly use?: string;
  readonly alg?: string;
  readonly d?: string;
}

/**
 * Looks a provider's public key up by kid, as Plaid's
 * /webhook_verification_key/get does: resolves the key, null or undefined
 * when the provider has none (or has expired it, Plaid's expired_at), and
 * rejects when it cannot be asked.
 */
export type WebhookKeyLookup = (kid: string) => Promise<WebhookJwk | null | undefined>;

export interface JwtBodyHashVerifierOptions {
  /** The header that carries the compact JWS, such as Plaid's `plaid-verification`. */
  readonly header: string;
  /** The provider's keys: a lookup by kid, or the URL of its JWKS, fetched whole. */
  readonly jwks: WebhookKeyLookup | string | URL;
  /** The algorithm the token header must name. */
  readonly alg: JwtWebhookAlgorithm;
  /** The oldest iat accepted: a duration's text (`5m`, `300s`) or seconds. */
  readonly maxAge: string | number;
  /** The claim that holds the lowercase hex SHA-256 of the raw body, such as `request_body_sha256`. */
  readonly bodyHashClaim: string;
  /** How far in the future an iat may be: a duration's text or seconds; 60 seconds by default. */
  readonly clockSkew?: string | number;
  /** The largest body read; 1 MiB (the router's default body cap) by default. */
  readonly maxBodyBytes?: number;
  /** Clock in milliseconds, for tests; Date.now by default. */
  readonly now?: () => number;
  /** Fetches a JWKS URL and resolves its parsed JSON; the global fetch with a 10-second timeout by default. */
  readonly fetchKeys?: (url: string) => Promise<unknown>;
}

/** The problem code of a token that is missing or does not verify. */
export const INVALID_SIGNATURE_CODE = 'invalid_signature';
const INVALID_SIGNATURE_DETAIL = 'The webhook signature does not match';
const KEY_UNAVAILABLE_DETAIL = "The webhook's signing key could not be fetched";
const DEFAULT_CLOCK_SKEW_SECONDS = 60;
const DEFAULT_MAX_BODY_BYTES = 1024 * 1024;
/** A held key older than this is looked up again. */
const KEY_MAX_AGE_MS = 60 * 60_000;
/** The provider is asked at most this often, whatever prompts it. */
const LOOKUP_MIN_INTERVAL_MS = 60_000;
const JWKS_FETCH_TIMEOUT_MS = 10_000;

function refused(reason: string): never {
  throw new HttpProblem(401, INVALID_SIGNATURE_DETAIL, { code: INVALID_SIGNATURE_CODE, cause: new Error(reason) });
}

function configError(message: string): never {
  throw new Error(`jwtBodyHashVerifier: ${message}`);
}

/** A duration option as seconds: a duration's text, or a number of seconds. */
function secondsOf(value: string | number, name: string): number {
  if (typeof value === 'number') {
    if (!Number.isFinite(value) || value < 0) configError(`${name} must be a duration or a number of seconds, not ${value}`);
    return value;
  }
  let nanos: bigint;
  try {
    nanos = parseDuration(value);
  } catch {
    configError(`${name} ${JSON.stringify(value)} is no duration, such as "5m" or "300s"`);
  }
  if (nanos < 0n) configError(`${name} must not be negative`);
  return Number(nanos) / 1e9;
}

/**
 * The verifier of a provider that signs each webhook with a JWT carrying the
 * SHA-256 of the raw body (D81), for Implementations.webhookVerifiers. It
 * throws on a config it cannot run, at startup.
 *
 * ```ts
 * webhookVerifiers: {
 *   plaid: jwtBodyHashVerifier({
 *     header: 'plaid-verification',
 *     jwks: async kid => {
 *       const { key } = (await plaid.webhookVerificationKeyGet({ key_id: kid })).data;
 *       return key.expired_at === null ? key : null;
 *     },
 *     alg: 'ES256',
 *     maxAge: '5m',
 *     bodyHashClaim: 'request_body_sha256',
 *   }),
 * }
 * ```
 */
export function jwtBodyHashVerifier(options: JwtBodyHashVerifierOptions): MiddlewareHandler {
  if (typeof options.header !== 'string' || options.header.trim() === '') {
    configError('header is required (the header that carries the token, such as plaid-verification)');
  }
  if (options.alg !== 'ES256') configError(`alg ${JSON.stringify(options.alg)} is not supported; use ES256`);
  if (typeof options.bodyHashClaim !== 'string' || options.bodyHashClaim === '') {
    configError('bodyHashClaim is required (the claim that holds the body\'s SHA-256, such as request_body_sha256)');
  }
  const maxAge = secondsOf(options.maxAge, 'maxAge');
  if (maxAge <= 0) configError('maxAge must be positive');
  const clockSkew = options.clockSkew === undefined ? DEFAULT_CLOCK_SKEW_SECONDS : secondsOf(options.clockSkew, 'clockSkew');
  const maxBodyBytes = options.maxBodyBytes ?? DEFAULT_MAX_BODY_BYTES;
  if (!Number.isSafeInteger(maxBodyBytes) || maxBodyBytes <= 0) configError('maxBodyBytes must be a positive integer');
  const now = options.now ?? Date.now;
  const alg = options.alg;
  const claim = options.bodyHashClaim;
  const header = options.header;
  const keys = keyCacheOf(options, now);

  return async (c, next) => {
    const request = c.req.raw;
    const token = request.headers.get(header);
    if (!token) refused(`no ${header} header`);
    const segments = token.split('.');
    if (segments.length !== 3) refused('the token is not a compact JWS');
    const [encodedHeader, encodedClaims, encodedSignature] = segments as [string, string, string];
    const jose = jsonSegment(encodedHeader);
    const claims = jsonSegment(encodedClaims);
    const signature = base64urlDecode(encodedSignature);
    if (!jose || !claims || !signature) refused('a segment of the token is not base64url JSON');
    if (jose.alg !== alg) refused(`the token's alg ${JSON.stringify(jose.alg)} is not ${alg}`);
    const kid = jose.kid;
    if (typeof kid !== 'string' || kid === '') refused('the token has no kid');

    const key = await keys.key(kid);
    if (!(await verifiesES256(key, new TextEncoder().encode(`${encodedHeader}.${encodedClaims}`), signature))) {
      refused(`the signature does not verify with key ${JSON.stringify(kid)}`);
    }

    const iat = claims.iat;
    if (typeof iat !== 'number' || !Number.isFinite(iat)) refused('the token has no numeric iat');
    const seconds = now() / 1000;
    if (iat > seconds + clockSkew) refused("the token's iat is in the future");
    if (seconds - iat > maxAge) refused("the token's iat is older than the maximum age");
    const claimed = claims[claim];
    if (typeof claimed !== 'string') refused(`the token has no ${claim} claim`);

    const body = await readBody(request, maxBodyBytes);
    const digest = hex(new Uint8Array(await crypto.subtle.digest('SHA-256', body)));
    if (!equalText(claimed, digest)) refused(`the body's SHA-256 is not the token's ${claim}`);
    await next();
  };
}

/** The key cache over options.jwks: a lookup by kid, or a JWKS URL fetched whole. */
function keyCacheOf(options: JwtBodyHashVerifierOptions, now: () => number): KeyCache {
  const { jwks, alg } = options;
  if (typeof jwks === 'function') {
    return new KeyCache(
      async kid => {
        const jwk = await jwks(kid);
        return new Map(jwk && usable(jwk, alg) ? [[kid, jwk]] : []);
      },
      false,
      now
    );
  }
  if (typeof jwks === 'string' || jwks instanceof URL) {
    const url = String(jwks);
    const fetchKeys = options.fetchKeys ?? fetchJwks;
    return new KeyCache(async () => keySet(await fetchKeys(url), alg), true, now);
  }
  configError('jwks must be a key lookup by kid or the URL of a JWKS');
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

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

/** Whether a key is one the verifier can use: public, EC P-256 with 32-byte coordinates, for signing with alg. */
function usable(jwk: WebhookJwk, alg: JwtWebhookAlgorithm): boolean {
  const coordinate = (value: string | undefined) => value !== undefined && base64urlDecode(value.replace(/=+$/u, ''))?.length === 32;
  return (
    (jwk.d === undefined || jwk.d === '') &&
    jwk.kty === 'EC' &&
    jwk.crv === 'P-256' &&
    coordinate(jwk.x) &&
    coordinate(jwk.y) &&
    (jwk.use === undefined || jwk.use === '' || jwk.use === 'sig') &&
    (jwk.alg === undefined || jwk.alg === '' || jwk.alg === alg)
  );
}

/** The usable keys of a JWKS document by kid; of two keys with one kid, the first is kept. */
function keySet(document: unknown, alg: JwtWebhookAlgorithm): Map<string, WebhookJwk> {
  if (!isObject(document) || !Array.isArray(document.keys)) throw new Error('the key set has no keys list');
  const keys = new Map<string, WebhookJwk>();
  for (const jwk of document.keys) {
    if (!isObject(jwk) || typeof jwk.kid !== 'string' || jwk.kid === '' || keys.has(jwk.kid)) continue;
    if (usable(jwk as unknown as WebhookJwk, alg)) keys.set(jwk.kid, jwk as unknown as WebhookJwk);
  }
  return keys;
}

async function fetchJwks(url: string): Promise<unknown> {
  const response = await fetch(url, { headers: { accept: 'application/json' }, signal: AbortSignal.timeout(JWKS_FETCH_TIMEOUT_MS) });
  if (!response.ok) throw new Error(`GET ${url}: status ${response.status}`);
  return response.json();
}

interface HeldKey {
  readonly jwk: WebhookJwk;
  readonly fetchedAt: number;
  imported?: Promise<CryptoKey>;
}

/** The ES256 check: a 64-byte r||s signature, which WebCrypto's ECDSA takes as is. */
async function verifiesES256(key: HeldKey, input: Uint8Array<ArrayBuffer>, signature: Uint8Array<ArrayBuffer>): Promise<boolean> {
  if (signature.length !== 64) return false;
  try {
    key.imported ??= crypto.subtle.importKey(
      'jwk',
      { kty: 'EC', crv: 'P-256', x: key.jwk.x?.replace(/=+$/u, ''), y: key.jwk.y?.replace(/=+$/u, '') },
      { name: 'ECDSA', namedCurve: 'P-256' },
      false,
      ['verify']
    );
    return await crypto.subtle.verify({ name: 'ECDSA', hash: 'SHA-256' }, await key.imported, signature, input);
  } catch {
    // A point off the curve fails the import.
    return false;
  }
}

/**
 * The provider's keys by kid. A lookup resolves the keys it found: for a
 * function the kid's, or none, which drops a held one; for a key set every
 * key, which replaces those held.
 */
class KeyCache {
  private held = new Map<string, HeldKey>();
  private attempted: number | undefined;
  private inflight: Promise<void> | undefined;

  constructor(
    private readonly lookup: (kid: string) => Promise<ReadonlyMap<string, WebhookJwk>>,
    private readonly replaces: boolean,
    private readonly now: () => number
  ) {}

  async key(kid: string): Promise<HeldKey> {
    for (;;) {
      const now = this.now();
      const held = this.held.get(kid);
      if (held && now - held.fetchedAt < KEY_MAX_AGE_MS) return held;
      if (this.inflight) {
        if (held) return held;
        await this.inflight;
        continue;
      }
      if (this.attempted !== undefined && now - this.attempted < LOOKUP_MIN_INTERVAL_MS) {
        if (held) return held;
        refused(`no key ${JSON.stringify(kid)}, and the provider was asked less than a minute ago`);
      }
      this.attempted = now;
      let failure: { readonly error: unknown } | undefined;
      this.inflight = this.lookup(kid)
        .then(
          found => this.store(kid, found, now),
          (error: unknown) => {
            failure = { error };
          }
        )
        .finally(() => {
          this.inflight = undefined;
        });
      await this.inflight;
      if (failure && !this.held.has(kid)) throw serviceUnavailable(KEY_UNAVAILABLE_DETAIL, { cause: failure.error });
      // Look again: the lookup may have brought the kid, or not.
    }
  }

  private store(kid: string, found: ReadonlyMap<string, WebhookJwk>, fetchedAt: number): void {
    if (this.replaces) {
      this.held = new Map([...found].map(([id, jwk]) => [id, { jwk, fetchedAt }]));
      return;
    }
    const jwk = found.get(kid);
    if (jwk) this.held.set(kid, { jwk, fetchedAt });
    else this.held.delete(kid);
  }
}

/** The body, read up to limit bytes; a 413 past it. */
async function readBody(request: Request, limit: number): Promise<Uint8Array<ArrayBuffer>> {
  const declared = request.headers.get('content-length');
  if (declared !== null && /^\d+$/u.test(declared) && Number(declared) > limit) throw payloadTooLarge();
  if (request.body === null) return new Uint8Array(0);
  const chunks: Uint8Array[] = [];
  let size = 0;
  try {
    const reader = request.body.getReader();
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      size += value.byteLength;
      if (size > limit) {
        await reader.cancel();
        throw payloadTooLarge();
      }
      chunks.push(value);
    }
  } catch (error) {
    if (error instanceof HttpProblem) throw error;
    throw badRequest('The request body could not be read', { cause: error });
  }
  const body = new Uint8Array(size);
  let offset = 0;
  for (const chunk of chunks) {
    body.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return body;
}

function hex(bytes: Uint8Array): string {
  let text = '';
  for (const byte of bytes) text += byte.toString(16).padStart(2, '0');
  return text;
}

/** Whether two strings are equal, in time that depends on their length alone. */
function equalText(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  let difference = 0;
  for (let index = 0; index < a.length; index++) difference |= a.charCodeAt(index) ^ b.charCodeAt(index);
  return difference === 0;
}
