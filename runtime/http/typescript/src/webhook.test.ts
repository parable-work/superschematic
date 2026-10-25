import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { Hono } from 'hono';
import { jwtBodyHashVerifier, mountManualOperation, mountOperation, type JwtBodyHashVerifierOptions, type WebhookJwk, type WebhookVerifier } from './hono';
import type { OperationSpec } from './operation';
import { base64urlEncode } from './serviceauth';

/*
The JWT body-hash webhook verifier (D81). The parity vectors are
runtime/http/testdata/webhook_jwt_parity.json, which the Go runtime writes
(`go test ./webhook -run TestWriteParityVectors -update` in runtime/http/go)
and the Go, TypeScript and Rust runtimes each run; its comment states the
harness. Every key is a P-256 key generated for the run: none is committed.
*/

const CORPUS = fileURLToPath(new URL('../../testdata/webhook_jwt_parity.json', import.meta.url));

interface ParityToken {
  readonly raw?: string;
  readonly header?: unknown;
  readonly claims?: unknown;
  readonly signingInput?: string;
  readonly signed?: string;
  readonly signer?: 'trusted' | 'stranger' | 'none';
  readonly tamper?: 'flip' | 'truncate';
}

interface ParityVector {
  readonly name: string;
  readonly now: number;
  readonly keys: readonly string[];
  readonly lookupFails: boolean;
  readonly token: ParityToken | null;
  readonly body: string;
  readonly want: { readonly status: number; readonly code: string | null };
}

interface ParityFile {
  readonly comment: string;
  readonly verifier: {
    readonly header: string;
    readonly alg: 'ES256';
    readonly maxAgeSeconds: number;
    readonly clockSkewSeconds: number;
    readonly bodyHashClaim: string;
    readonly maxBodyBytes: number;
  };
  readonly vectors: readonly ParityVector[];
}

const corpus = JSON.parse(readFileSync(CORPUS, 'utf8')) as ParityFile;

/** A P-256 key generated for this run. */
interface TestKey {
  readonly jwk: (kid: string) => WebhookJwk;
  readonly sign: (input: string) => Promise<Uint8Array>;
}

async function newKey(): Promise<TestKey> {
  const pair = await crypto.subtle.generateKey({ name: 'ECDSA', namedCurve: 'P-256' }, true, ['sign', 'verify']);
  const exported = await crypto.subtle.exportKey('jwk', pair.publicKey);
  return {
    jwk: kid => ({ kty: 'EC', crv: 'P-256', kid, use: 'sig', alg: 'ES256', x: exported.x!, y: exported.y! }),
    // WebCrypto's ECDSA signature is r||s, a JWS's ES256 form.
    sign: async input => new Uint8Array(await crypto.subtle.sign({ name: 'ECDSA', hash: 'SHA-256' }, pair.privateKey, new TextEncoder().encode(input))),
  };
}

const trusted = await newKey();
const stranger = await newKey();

async function tokenOf(token: ParityToken): Promise<string> {
  if (token.raw !== undefined) return token.raw;
  const input = token.signed || token.signingInput!;
  let signature: Uint8Array = new Uint8Array(0);
  if (token.signer === 'trusted') signature = await trusted.sign(input);
  else if (token.signer === 'stranger') signature = await stranger.sign(input);
  else if (token.signer !== 'none') throw new Error(`unknown signer ${token.signer}`);
  if (token.tamper === 'flip') signature[signature.length - 1] ^= 1;
  else if (token.tamper === 'truncate') signature = signature.slice(0, -1);
  return `${token.signingInput}.${base64urlEncode(signature)}`;
}

const webhookSpec: OperationSpec = {
  name: 'receivePlaidEvent',
  namespace: 'plaid',
  method: 'POST',
  path: '/api/webhooks/plaid',
  pathParams: [],
  queryParams: [],
  bodyParams: [],
  auth: { public: true, required: false, permissions: [] },
  webhookProvider: 'plaid',
  manual: true,
};

/** A manual webhook route behind verifier that answers the body it reads. */
function echoApp(verifier: WebhookVerifier): Hono {
  const app = new Hono();
  mountManualOperation(app, webhookSpec, async c => c.text(await c.req.text()), {}, { webhookVerifier: verifier });
  return app;
}

function post(app: Hono, body: string, headers: Record<string, string> = {}): Promise<Response> {
  return Promise.resolve(app.request('/api/webhooks/plaid', { method: 'POST', headers: { 'content-type': 'application/json', ...headers }, body }));
}

describe('webhook JWT parity vectors', () => {
  test('the corpus has vectors with distinct names', () => {
    expect(corpus.vectors.length).toBeGreaterThan(0);
    expect(new Set(corpus.vectors.map(vector => vector.name)).size).toBe(corpus.vectors.length);
  });

  for (const vector of corpus.vectors) {
    test(vector.name, async () => {
      const { verifier } = corpus;
      const app = echoApp(
        jwtBodyHashVerifier({
          header: verifier.header,
          alg: verifier.alg,
          maxAge: verifier.maxAgeSeconds,
          clockSkew: verifier.clockSkewSeconds,
          bodyHashClaim: verifier.bodyHashClaim,
          maxBodyBytes: verifier.maxBodyBytes,
          now: () => vector.now * 1000,
          jwks: async kid => {
            if (vector.lookupFails) throw new Error('the key endpoint is down');
            return vector.keys.includes(kid) ? trusted.jwk(kid) : undefined;
          },
        })
      );
      const headers: Record<string, string> = vector.token === null ? {} : { [verifier.header]: await tokenOf(vector.token) };
      const response = await post(app, vector.body, headers);
      const text = await response.text();
      expect({ status: response.status, text: response.status === 200 ? text : undefined }).toEqual({
        status: vector.want.status,
        text: vector.want.status === 200 ? vector.body : undefined,
      });
      if (vector.want.code !== null) expect(JSON.parse(text).code).toBe(vector.want.code);
    });
  }
});

const NOW = 1_767_225_600_000;
const PLAID_BODY = '{\n  "webhook_type": "TRANSACTIONS",\n  "webhook_code": "SYNC_UPDATES_AVAILABLE",\n  "item_id": "item-sandbox-1"\n}';

async function sha256Hex(text: string): Promise<string> {
  const digest = new Uint8Array(await crypto.subtle.digest('SHA-256', new TextEncoder().encode(text)));
  return [...digest].map(byte => byte.toString(16).padStart(2, '0')).join('');
}

/** A Plaid-style token for body under kid, signed by key at nowMs. */
async function plaidToken(key: TestKey, kid: string, nowMs: number, body = PLAID_BODY): Promise<string> {
  const segment = (value: unknown) => base64urlEncode(new TextEncoder().encode(JSON.stringify(value)));
  const input = `${segment({ alg: 'ES256', kid, typ: 'JWT' })}.${segment({ iat: Math.floor(nowMs / 1000), request_body_sha256: await sha256Hex(body) })}`;
  return `${input}.${base64urlEncode(await key.sign(input))}`;
}

function plaidOptions(overrides: Partial<JwtBodyHashVerifierOptions>): JwtBodyHashVerifierOptions {
  return {
    header: 'plaid-verification',
    alg: 'ES256',
    maxAge: '5m',
    bodyHashClaim: 'request_body_sha256',
    jwks: async kid => trusted.jwk(kid),
    now: () => NOW,
    ...overrides,
  };
}

async function status(app: Hono, token: string, body = PLAID_BODY): Promise<number> {
  return (await post(app, body, { 'plaid-verification': token })).status;
}

describe('jwtBodyHashVerifier', () => {
  test('a config it cannot run throws at startup', () => {
    const bad: Array<[Partial<JwtBodyHashVerifierOptions>, string]> = [
      [{ header: '' }, 'header is required'],
      [{ alg: 'RS256' as 'ES256' }, 'alg "RS256" is not supported; use ES256'],
      [{ alg: 'none' as 'ES256' }, 'alg "none" is not supported'],
      [{ bodyHashClaim: '' }, 'bodyHashClaim is required'],
      [{ maxAge: 0 }, 'maxAge must be positive'],
      [{ maxAge: '5 minutes' }, 'maxAge "5 minutes" is no duration'],
      [{ clockSkew: -1 }, 'clockSkew must be a duration or a number of seconds'],
      [{ maxBodyBytes: 0 }, 'maxBodyBytes must be a positive integer'],
      [{ jwks: undefined as unknown as string }, 'jwks must be a key lookup by kid or the URL of a JWKS'],
    ];
    for (const [overrides, message] of bad) {
      expect(() => jwtBodyHashVerifier(plaidOptions(overrides))).toThrow(message);
    }
  });

  test("maxAge and clockSkew take a duration's text or seconds", async () => {
    const token = await plaidToken(trusted, 'k1', NOW - 120_000);
    for (const [maxAge, want] of [['5m', 200], [300, 200], ['1m30s', 401], [90, 401]] as const) {
      expect(await status(echoApp(jwtBodyHashVerifier(plaidOptions({ maxAge }))), token)).toBe(want);
    }
    const ahead = await plaidToken(trusted, 'k1', NOW + 90_000);
    expect(await status(echoApp(jwtBodyHashVerifier(plaidOptions({}))), ahead)).toBe(401);
    expect(await status(echoApp(jwtBodyHashVerifier(plaidOptions({ clockSkew: '2m' }))), ahead)).toBe(200);
  });

  test('the route reads the input the verifier read, through mountOperation', async () => {
    const app = new Hono();
    const spec: OperationSpec = {
      ...webhookSpec,
      manual: false,
      input: { parse: value => value, required: true },
    };
    mountOperation(app, spec, async (_ctx, request) => ({ received: request.input }), {}, { webhookVerifier: jwtBodyHashVerifier(plaidOptions({})) });
    const response = await post(app, PLAID_BODY, { 'plaid-verification': await plaidToken(trusted, 'k1', NOW) });
    expect(response.status).toBe(200);
    expect((await response.json()).data).toEqual({ received: JSON.parse(PLAID_BODY) });
  });

  test('a held key serves without a lookup', async () => {
    const asked: string[] = [];
    const app = echoApp(jwtBodyHashVerifier(plaidOptions({ jwks: async kid => (asked.push(kid), trusted.jwk(kid)) })));
    for (let i = 0; i < 3; i++) expect(await status(app, await plaidToken(trusted, 'k1', NOW))).toBe(200);
    expect(asked).toEqual(['k1']);
  });

  test('the provider is asked about a kid not held at most once a minute', async () => {
    let now = NOW;
    const asked: string[] = [];
    const app = echoApp(
      jwtBodyHashVerifier(
        plaidOptions({
          now: () => now,
          jwks: async kid => (asked.push(kid), kid === 'k1' ? trusted.jwk(kid) : null),
        })
      )
    );
    expect(await status(app, await plaidToken(trusted, 'k1', now))).toBe(200);
    now += 60_000;
    for (const kid of ['r1', 'r2', 'r3', 'r4']) expect(await status(app, await plaidToken(trusted, kid, now))).toBe(401);
    // The held kid verifies through the flood.
    expect(await status(app, await plaidToken(trusted, 'k1', now))).toBe(200);
    expect(asked).toEqual(['k1', 'r1']);
    now += 60_000;
    await status(app, await plaidToken(trusted, 'r5', now));
    expect(asked).toEqual(['k1', 'r1', 'r5']);
  });

  test('a held key an hour old is looked up again; a failed lookup keeps it, no key drops it', async () => {
    let now = NOW;
    let answer: 'key' | 'down' | 'gone' = 'key';
    let lookups = 0;
    const app = echoApp(
      jwtBodyHashVerifier(
        plaidOptions({
          now: () => now,
          jwks: async kid => {
            lookups++;
            if (answer === 'down') throw new Error('down');
            return answer === 'gone' ? undefined : trusted.jwk(kid);
          },
        })
      )
    );
    expect(await status(app, await plaidToken(trusted, 'k1', now))).toBe(200);
    now += 60 * 60_000;
    answer = 'down';
    expect([await status(app, await plaidToken(trusted, 'k1', now)), lookups]).toEqual([200, 2]);
    now += 60_000;
    answer = 'gone';
    expect([await status(app, await plaidToken(trusted, 'k1', now)), lookups]).toEqual([401, 3]);
  });

  test('requests for a kid not held share the running lookup', async () => {
    let lookups = 0;
    let release: () => void = () => {};
    const released = new Promise<void>(resolve => (release = resolve));
    const app = echoApp(
      jwtBodyHashVerifier(
        plaidOptions({
          jwks: async kid => {
            lookups++;
            await released;
            return trusted.jwk(kid);
          },
        })
      )
    );
    const token = await plaidToken(trusted, 'k1', NOW);
    const pending = Promise.all([0, 1, 2, 3].map(() => status(app, token)));
    await new Promise(resolve => setTimeout(resolve, 10));
    release();
    expect(await pending).toEqual([200, 200, 200, 200]);
    expect(lookups).toBe(1);
  });

  test('a JWKS URL is fetched whole, and again at most once a minute', async () => {
    let now = NOW;
    const other = await newKey();
    const fetched: string[] = [];
    const app = echoApp(
      jwtBodyHashVerifier(
        plaidOptions({
          now: () => now,
          jwks: new URL('https://keys.example/jwks.json'),
          fetchKeys: async url => {
            fetched.push(url);
            return { keys: [trusted.jwk('k1'), other.jwk('k2'), { kty: 'RSA', kid: 'rsa', n: 'AQAB', e: 'AQAB' }] };
          },
        })
      )
    );
    expect(await status(app, await plaidToken(trusted, 'k1', now))).toBe(200);
    expect(await status(app, await plaidToken(other, 'k2', now))).toBe(200);
    expect(await status(app, await plaidToken(trusted, 'k3', now))).toBe(401);
    expect(await status(app, await plaidToken(trusted, 'rsa', now))).toBe(401);
    expect(fetched).toEqual(['https://keys.example/jwks.json']);
    now += 60_000;
    await status(app, await plaidToken(trusted, 'k3', now));
    expect(fetched.length).toBe(2);
  });

  test('a key it cannot use is no key', async () => {
    const changes: Array<Partial<WebhookJwk>> = [{ crv: 'P-384' }, { d: 'AAAA' }, { use: 'enc' }, { alg: 'RS256' }, { x: 'AAAA' }];
    for (const change of changes) {
      const app = echoApp(jwtBodyHashVerifier(plaidOptions({ jwks: async kid => ({ ...trusted.jwk(kid), ...change }) })));
      expect(await status(app, await plaidToken(trusted, 'k1', NOW))).toBe(401);
    }
  });

  test('two of the header, which Headers joins, are refused as Go and Rust refuse them', async () => {
    const token = await plaidToken(trusted, 'k1', NOW);
    const headers = new Headers({ 'content-type': 'application/json' });
    headers.append('plaid-verification', token);
    headers.append('plaid-verification', token);
    const app = echoApp(jwtBodyHashVerifier(plaidOptions({})));
    expect((await app.request('/api/webhooks/plaid', { method: 'POST', headers, body: PLAID_BODY })).status).toBe(401);
  });

  test('a refusal is the 401 problem, whose detail names no reason', async () => {
    const response = await post(echoApp(jwtBodyHashVerifier(plaidOptions({}))), PLAID_BODY, { 'plaid-verification': 'forged' });
    expect(response.status).toBe(401);
    expect(response.headers.get('content-type')).toBe('application/problem+json');
    expect(await response.json()).toMatchObject({ status: 401, code: 'invalid_signature', detail: 'The webhook signature does not match' });
  });
});
