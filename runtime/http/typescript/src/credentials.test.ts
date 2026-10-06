import { afterAll, describe, expect, test } from 'bun:test';
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { googleIdTokenSource, serviceAuthenticator, signedTokenSource, tokenFileSource, type Ed25519PrivateJwk, type RequestContext } from './index';
import { base64urlDecode, base64urlEncode } from './serviceauth';

const NOW = 1_767_225_600;
const encoder = new TextEncoder();
const decoded = (segment: string) => new TextDecoder().decode(base64urlDecode(segment));
const unsignedToken = (payload: Record<string, unknown>) =>
  `${base64urlEncode(encoder.encode('{"alg":"RS256"}'))}.${base64urlEncode(encoder.encode(JSON.stringify(payload)))}.c2ln`;

describe('googleIdTokenSource', () => {
  function metadataServer(answer: (call: number) => Response) {
    const calls: { url: string; headers: Record<string, string> }[] = [];
    const fetch = async (url: string, init: { headers: Record<string, string> }) => {
      calls.push({ url, headers: init.headers });
      return answer(calls.length);
    };
    return { calls, fetch };
  }

  test('asks the metadata server for an ID token for the audience', async () => {
    const server = metadataServer(() => new Response(`${unsignedToken({ exp: NOW + 3600 })}\n`));
    const token = await googleIdTokenSource('https://shop-api-abc.a.run.app/?x=1', { fetch: server.fetch, now: () => NOW * 1000 })(false);
    expect(token).toBe(unsignedToken({ exp: NOW + 3600 }));
    expect(server.calls).toEqual([
      {
        url: 'http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/identity?audience=https%3A%2F%2Fshop-api-abc.a.run.app%2F%3Fx%3D1',
        headers: { 'metadata-flavor': 'Google' },
      },
    ]);
  });

  test('caches the token until 5 minutes before exp; fresh fetches another', async () => {
    let now = NOW * 1000;
    const server = metadataServer(call => new Response(unsignedToken({ exp: NOW + 3600, n: call })));
    const source = googleIdTokenSource('https://shop-api.test', { fetch: server.fetch, now: () => now });
    const first = await source(false);
    now += 3299 * 1000;
    expect(await source(false)).toBe(first);
    expect(server.calls.length).toBe(1);
    now += 1000;
    const second = await source(false);
    expect(second).not.toBe(first);
    expect(server.calls.length).toBe(2);
    expect(await source(true)).not.toBe(second);
    expect(server.calls.length).toBe(3);
  });

  test('concurrent callers share one fetch, and a token without exp is not cached', async () => {
    const server = metadataServer(() => new Response(unsignedToken({ sub: 'x' })));
    const source = googleIdTokenSource('https://shop-api.test', { fetch: server.fetch, now: () => NOW * 1000 });
    await Promise.all([source(false), source(false)]);
    expect(server.calls.length).toBe(1);
    await source(false);
    expect(server.calls.length).toBe(2);
  });

  test('takes the host from the options, then GCE_METADATA_HOST', async () => {
    const server = metadataServer(() => new Response(unsignedToken({ exp: NOW + 3600 })));
    await googleIdTokenSource('aud', { fetch: server.fetch, metadataHost: '127.0.0.1:8080' })(false);
    const previous = process.env.GCE_METADATA_HOST;
    process.env.GCE_METADATA_HOST = 'metadata.test:9000';
    try {
      await googleIdTokenSource('aud', { fetch: server.fetch })(false);
    } finally {
      if (previous === undefined) delete process.env.GCE_METADATA_HOST;
      else process.env.GCE_METADATA_HOST = previous;
    }
    expect(server.calls.map(call => new URL(call.url).host)).toEqual(['127.0.0.1:8080', 'metadata.test:9000']);
  });

  test('a refusal from the metadata server rejects', async () => {
    const server = metadataServer(() => new Response('no', { status: 404 }));
    await expect(googleIdTokenSource('aud', { fetch: server.fetch })(false)).rejects.toThrow('the metadata server answered 404 for an ID token');
  });
});

describe('tokenFileSource', () => {
  const directory = mkdtempSync(join(tmpdir(), 'token-file-'));
  afterAll(() => rmSync(directory, { recursive: true, force: true }));

  test('reads the file, trimmed, again when the token is a minute old or fresh is asked', async () => {
    let now = NOW * 1000;
    let contents = 'token-1\n';
    const reads: string[] = [];
    const source = tokenFileSource('/var/run/secrets/tokens/shop-api', {
      now: () => now,
      readFile: async path => {
        reads.push(path);
        return contents;
      },
    });
    expect(await source(false)).toBe('token-1');
    contents = 'token-2';
    now += 59_999;
    expect(await source(false)).toBe('token-1');
    now += 1;
    expect(await source(false)).toBe('token-2');
    contents = 'token-3';
    expect(await source(true)).toBe('token-3');
    expect(reads).toEqual(['/var/run/secrets/tokens/shop-api', '/var/run/secrets/tokens/shop-api', '/var/run/secrets/tokens/shop-api']);
  });

  test('reads a real file by default, and refuses an empty one', async () => {
    const path = join(directory, 'token');
    writeFileSync(path, '  projected-token  \n');
    expect(await tokenFileSource(path)(false)).toBe('projected-token');
    writeFileSync(path, '\n');
    await expect(tokenFileSource(path)(false)).rejects.toThrow(`the token file ${path} is empty`);
    await expect(tokenFileSource(join(directory, 'missing'))(false)).rejects.toThrow();
  });
});

describe('signedTokenSource', () => {
  async function edgeKey(kid: string): Promise<Ed25519PrivateJwk> {
    const pair = (await crypto.subtle.generateKey({ name: 'Ed25519' }, true, ['sign', 'verify'])) as CryptoKeyPair;
    const jwk = await crypto.subtle.exportKey('jwk', pair.privateKey);
    return { kty: 'OKP', crv: 'Ed25519', d: jwk.d!, x: jwk.x!, kid };
  }

  const claims = { issuer: 'orders', subject: 'orders', audience: 'shop-api' };

  test('signs the header and claims in the documented order, and the callee verifies the token', async () => {
    const key = await edgeKey('edge-key-1');
    const token = await signedTokenSource(key, claims, { now: () => NOW * 1000 + 999 })(false);
    const [header, payload] = token.split('.') as [string, string, string];
    expect(decoded(header)).toBe('{"alg":"EdDSA","kid":"edge-key-1","typ":"JWT"}');
    expect(decoded(payload)).toMatch(new RegExp(`^\\{"iss":"orders","sub":"orders","aud":"shop-api","iat":${NOW},"exp":${NOW + 300},"jti":"[A-Za-z0-9_-]{22}"\\}$`, 'u'));
    const authenticate = serviceAuthenticator(
      {
        issuers: [
          {
            issuer: 'orders',
            audience: 'shop-api',
            algorithms: ['EdDSA'],
            keys: [{ kty: 'OKP', crv: 'Ed25519', x: key.x, kid: key.kid }],
            maxLifetimeSeconds: 300,
            callers: { orders: { deployable: 'orders', serves: ['shop-orders'] } },
          },
        ],
      },
      { now: () => NOW * 1000 }
    );
    const headers = new Headers({ 'service-authorization': `Bearer ${token}` });
    expect(await authenticate({ headers } as RequestContext)).toEqual({ deployable: 'orders', serves: ['shop-orders'], subject: 'orders' });
  });

  test('signs again when under a minute remains, or when fresh is asked', async () => {
    let now = NOW * 1000;
    const source = signedTokenSource(await edgeKey('k'), claims, { now: () => now });
    const first = await source(false);
    now += 240_000;
    expect(await source(false)).toBe(first);
    now += 1000;
    const second = await source(false);
    expect(second).not.toBe(first);
    const fresh = await source(true);
    expect(fresh).not.toBe(second);
    expect(JSON.parse(decoded(fresh.split('.')[1]!)).jti).not.toBe(JSON.parse(decoded(second.split('.')[1]!)).jti);
  });

  test('refuses a key that is not an Ed25519 private JWK', async () => {
    const key = await edgeKey('k');
    expect(() => signedTokenSource({ ...key, d: undefined } as unknown as Ed25519PrivateJwk, claims)).toThrow(
      'signedTokenSource: the key must be an Ed25519 private JWK with d, x and kid'
    );
    expect(() => signedTokenSource({ ...key, kid: '' }, claims)).toThrow('with d, x and kid');
    expect(() => signedTokenSource({ ...key, crv: 'X25519' } as unknown as Ed25519PrivateJwk, claims)).toThrow('with d, x and kid');
  });
});
