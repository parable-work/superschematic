import { beforeAll, describe, expect, test } from 'bun:test';
import {
  HttpProblem,
  authorizeService,
  serviceAuthenticator,
  type PublicJwk,
  type RequestContext,
  type ServiceAuthConfig,
  type ServiceAuthenticatorOptions,
} from './index';
import { base64urlEncode } from './serviceauth';

// The clock every token below is minted against, in seconds.
const NOW = 1_767_225_600;
const clock = () => NOW * 1000;

type Alg = 'RS256' | 'ES256' | 'EdDSA';

const SIGN_AS = {
  RS256: { generate: { name: 'RSASSA-PKCS1-v1_5', modulusLength: 2048, publicExponent: new Uint8Array([1, 0, 1]), hash: 'SHA-256' }, sign: { name: 'RSASSA-PKCS1-v1_5' } },
  ES256: { generate: { name: 'ECDSA', namedCurve: 'P-256' }, sign: { name: 'ECDSA', hash: 'SHA-256' } },
  EdDSA: { generate: { name: 'Ed25519' }, sign: { name: 'Ed25519' } },
} as const;

interface TestKey {
  readonly alg: Alg;
  readonly kid: string;
  readonly privateKey: CryptoKey;
  readonly jwk: PublicJwk;
}

async function testKey(alg: Alg, kid: string): Promise<TestKey> {
  const pair = (await crypto.subtle.generateKey(SIGN_AS[alg].generate, true, ['sign', 'verify'])) as CryptoKeyPair;
  const { key_ops: _ops, ext: _ext, alg: _alg, ...jwk } = (await crypto.subtle.exportKey('jwk', pair.publicKey)) as JsonWebKey & { kty: string };
  return { alg, kid, privateKey: pair.privateKey, jwk: { ...jwk, kid } };
}

const encoder = new TextEncoder();
const segment = (value: unknown) => base64urlEncode(encoder.encode(JSON.stringify(value)));

/** Signs header and payload with `key`; the header defaults to the key's alg and kid. */
async function sign(key: TestKey, payload: Record<string, unknown>, header: Record<string, unknown> = {}): Promise<string> {
  const signingInput = `${segment({ alg: key.alg, kid: key.kid, typ: 'JWT', ...header })}.${segment(payload)}`;
  const signature = await crypto.subtle.sign(SIGN_AS[key.alg].sign, key.privateKey, encoder.encode(signingInput));
  return `${signingInput}.${base64urlEncode(new Uint8Array(signature))}`;
}

const ORDERS = { deployable: 'orders', serves: ['shop-orders'] };

let rsa: TestKey;
let ec: TestKey;
let ed: TestKey;
let rotated: TestKey;

beforeAll(async () => {
  rsa = await testKey('RS256', 'rsa-1');
  ec = await testKey('ES256', 'ec-1');
  ed = await testKey('EdDSA', 'ed-1');
  rotated = await testKey('RS256', 'rsa-2');
});

function config(): ServiceAuthConfig {
  return {
    issuers: [
      {
        issuer: 'https://accounts.google.com',
        issuerAliases: ['accounts.google.com'],
        audience: 'https://shop-api.test',
        algorithms: ['RS256'],
        jwksUrl: 'https://keys.test/google',
        callers: { '1001': ORDERS },
      },
      {
        issuer: 'https://kubernetes.test',
        audience: 'shop-api',
        algorithms: ['ES256', 'RS256'],
        keys: [ec.jwk, rsa.jwk],
        callers: { 'system:serviceaccount:shop:orders': ORDERS },
      },
      {
        issuer: 'orders',
        audience: 'shop-api',
        algorithms: ['EdDSA'],
        keys: [ed.jwk],
        maxLifetimeSeconds: 300,
        callers: { orders: ORDERS },
      },
      {
        issuer: 'https://mail.test',
        audience: 'shop-api',
        algorithms: ['EdDSA'],
        keys: [ed.jwk],
        subjectClaim: 'email',
        callers: { 'orders@shop.test': ORDERS },
      },
    ],
  };
}

const google = (claims: Record<string, unknown> = {}) => ({ iss: 'https://accounts.google.com', aud: 'https://shop-api.test', sub: '1001', iat: NOW - 10, exp: NOW + 3590, ...claims });
const kubernetes = (claims: Record<string, unknown> = {}) => ({ iss: 'https://kubernetes.test', aud: ['shop-api'], sub: 'system:serviceaccount:shop:orders', iat: NOW, exp: NOW + 600, ...claims });
const keypair = (claims: Record<string, unknown> = {}) => ({ iss: 'orders', aud: 'shop-api', sub: 'orders', iat: NOW, exp: NOW + 300, ...claims });

function contextWith(authorization: string | undefined): RequestContext {
  const headers = new Headers();
  if (authorization !== undefined) headers.set('Service-Authorization', authorization);
  return { headers } as RequestContext;
}

/** A JWKS endpoint stub that counts its fetches. */
function keyServer(keys: () => PublicJwk[] | Error) {
  const fetches: { url: string; bearer?: string }[] = [];
  const fetchKeys = async (url: string, bearer?: string) => {
    fetches.push({ url, ...(bearer !== undefined ? { bearer } : {}) });
    const listed = keys();
    if (listed instanceof Error) throw listed;
    return { keys: listed };
  };
  return { fetches, fetchKeys };
}

function authenticatorWith(options: ServiceAuthenticatorOptions = {}) {
  return serviceAuthenticator(config(), { now: clock, fetchKeys: keyServer(() => [rsa.jwk]).fetchKeys, ...options });
}

/** The refusal `token` gets: status, code, detail and the reason kept on its cause. */
async function refusalOf(authorization: string, options: ServiceAuthenticatorOptions = {}) {
  try {
    await authenticatorWith(options)(contextWith(authorization));
  } catch (error) {
    if (!(error instanceof HttpProblem)) throw error;
    return { status: error.status, code: error.code, detail: error.message, reason: (error.cause as Error | undefined)?.message };
  }
  throw new Error('the credential was accepted');
}

const invalid = (reason: string) => ({ status: 401, code: 'service_unauthorized', detail: 'Invalid service credential', reason });

describe('serviceAuthenticator', () => {
  test('no Service-Authorization header is no caller; an empty one is refused', async () => {
    expect(await authenticatorWith()(contextWith(undefined))).toBeNull();
    expect(await refusalOf('')).toEqual(invalid('Service-Authorization is not a Bearer token'));
  });

  test('reads only Service-Authorization, never Authorization', async () => {
    const headers = new Headers({ authorization: `Bearer ${await sign(rsa, google())}` });
    expect(await authenticatorWith()({ headers } as RequestContext)).toBeNull();
  });

  test('accepts RS256 from a JWKS, ES256 and RS256 from static keys, and EdDSA under either name', async () => {
    const authenticate = authenticatorWith();
    const callerOf = async (token: string) => authenticate(contextWith(`Bearer ${token}`));
    expect(await callerOf(await sign(rsa, google()))).toEqual({ deployable: 'orders', serves: ['shop-orders'], subject: '1001' });
    expect(await callerOf(await sign(rsa, google({ iss: 'accounts.google.com' })))).toMatchObject({ subject: '1001' });
    expect(await callerOf(await sign(ec, kubernetes()))).toMatchObject({ deployable: 'orders', subject: 'system:serviceaccount:shop:orders' });
    expect(await callerOf(await sign(rsa, kubernetes()))).toMatchObject({ deployable: 'orders' });
    expect(await callerOf(await sign(ed, keypair()))).toMatchObject({ subject: 'orders' });
    expect(await callerOf(await sign(ed, keypair(), { alg: 'Ed25519' }))).toMatchObject({ subject: 'orders' });
    expect(await callerOf(await sign(ed, { iss: 'https://mail.test', aud: 'shop-api', email: 'orders@shop.test', exp: NOW + 60 }))).toMatchObject({
      subject: 'orders@shop.test',
    });
  });

  test('the scheme is Bearer in any case, with exactly one token', async () => {
    const token = await sign(rsa, google());
    expect(await authenticatorWith()(contextWith(`bEaReR ${token}`))).toMatchObject({ subject: '1001' });
    expect(await refusalOf(`Basic ${token}`)).toEqual(invalid('Service-Authorization is not a Bearer token'));
    expect(await refusalOf('Bearer')).toEqual(invalid('Service-Authorization is not a Bearer token'));
    expect(await refusalOf('Bearer ')).toEqual(invalid('Service-Authorization is not a Bearer token'));
    expect(await refusalOf(`Bearer  ${token}`)).toEqual(invalid('Service-Authorization is not a Bearer token'));
    expect(await refusalOf(`Bearer ${token} extra`)).toEqual(invalid('Service-Authorization is not a Bearer token'));
    expect(await refusalOf(`Bearer ${token}\tx`)).toEqual(invalid('Service-Authorization is not a Bearer token'));
    // Two headers arrive joined by a comma.
    const twice = new Headers();
    twice.append('Service-Authorization', `Bearer ${token}`);
    twice.append('Service-Authorization', `Bearer ${token}`);
    await expect(authenticatorWith()({ headers: twice } as RequestContext)).rejects.toMatchObject({ status: 401, code: 'service_unauthorized' });
  });

  test('refuses a token that is not three base64url JSON segments', async () => {
    const token = await sign(rsa, google());
    const [header, payload, signature] = token.split('.');
    expect(await refusalOf(`Bearer ${header}.${payload}`)).toEqual(invalid('not a compact JWS'));
    expect(await refusalOf(`Bearer ${token}.x`)).toEqual(invalid('not a compact JWS'));
    expect(await refusalOf(`Bearer ${header}=.${payload}.${signature}`)).toEqual(invalid('a segment is not base64url JSON'));
    expect(await refusalOf(`Bearer ${header}.${payload}.${signature}+`)).toEqual(invalid('a segment is not base64url JSON'));
    expect(await refusalOf(`Bearer ${segment([1])}.${payload}.${signature}`)).toEqual(invalid('a segment is not base64url JSON'));
    expect(await refusalOf(`Bearer ${header}.${base64urlEncode(encoder.encode('not json'))}.${signature}`)).toEqual(invalid('a segment is not base64url JSON'));
  });

  test('refuses an issuer no entry names', async () => {
    expect(await refusalOf(`Bearer ${await sign(rsa, google({ iss: 'https://evil.test' }))}`)).toEqual(invalid('unknown issuer'));
    expect(await refusalOf(`Bearer ${await sign(rsa, google({ iss: undefined }))}`)).toEqual(invalid('unknown issuer'));
  });

  test("refuses an algorithm the issuer's entry does not list, none and HS256 among them", async () => {
    const payload = segment(google());
    const none = `${segment({ alg: 'none', kid: rsa.kid })}.${payload}.`;
    expect(await refusalOf(`Bearer ${none}`)).toEqual(invalid('algorithm not accepted'));
    // HS256 keyed with the RSA public key's bytes, the classic confusion.
    const hmacKey = await crypto.subtle.importKey('raw', encoder.encode(JSON.stringify(rsa.jwk)), { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']);
    const hsInput = `${segment({ alg: 'HS256', kid: rsa.kid })}.${payload}`;
    const hs = `${hsInput}.${base64urlEncode(new Uint8Array(await crypto.subtle.sign('HMAC', hmacKey, encoder.encode(hsInput))))}`;
    expect(await refusalOf(`Bearer ${hs}`)).toEqual(invalid('algorithm not accepted'));
    expect(await refusalOf(`Bearer ${await sign(ec, google(), { kid: rsa.kid })}`)).toEqual(invalid('algorithm not accepted'));
    expect(await refusalOf(`Bearer ${await sign(ed, google(), { alg: 'Ed25519', kid: rsa.kid })}`)).toEqual(invalid('algorithm not accepted'));
  });

  test('refuses a token without a kid, or whose kid no key has', async () => {
    expect(await refusalOf(`Bearer ${await sign(ed, keypair(), { kid: undefined })}`)).toEqual(invalid('no kid'));
    expect(await refusalOf(`Bearer ${await sign(ed, keypair(), { kid: '' })}`)).toEqual(invalid('no kid'));
    expect(await refusalOf(`Bearer ${await sign(ed, keypair(), { kid: 'ed-unknown' })}`)).toEqual(invalid('unknown kid'));
  });

  test("refuses a key whose type does not fit the token's algorithm", async () => {
    // The Kubernetes entry accepts ES256 and RS256; its RSA key signs nothing as ES256.
    expect(await refusalOf(`Bearer ${await sign(ec, kubernetes(), { kid: rsa.kid })}`)).toEqual(invalid('key does not fit the algorithm'));
  });

  test('refuses a signature that does not verify', async () => {
    const token = await sign(rsa, google());
    const forged = await sign(rotated, google(), { kid: rsa.kid });
    const [header, payload] = token.split('.');
    expect(await refusalOf(`Bearer ${forged}`)).toEqual(invalid('bad signature'));
    expect(await refusalOf(`Bearer ${header}.${payload}.${forged.split('.')[2]}`)).toEqual(invalid('bad signature'));
    const tampered = `${header}.${segment(google({ sub: 'someone-else' }))}.${token.split('.')[2]}`;
    expect(await refusalOf(`Bearer ${tampered}`)).toEqual(invalid('bad signature'));
    const ecToken = await sign(ec, kubernetes());
    expect(await refusalOf(`Bearer ${ecToken.slice(0, -4)}`)).toEqual(invalid('ES256 signature is not 64 bytes'));
  });

  test('checks exp, nbf and iat with 60 seconds of leeway', async () => {
    const at = async (claims: Record<string, unknown>) => {
      const token = await sign(rsa, google(claims));
      try {
        return await authenticatorWith()(contextWith(`Bearer ${token}`));
      } catch (error) {
        return (error as HttpProblem).cause instanceof Error ? ((error as HttpProblem).cause as Error).message : error;
      }
    };
    expect(await at({ exp: NOW - 59 })).toMatchObject({ subject: '1001' });
    expect(await at({ exp: NOW - 60 })).toBe('expired');
    expect(await at({ exp: undefined })).toBe('no exp');
    expect(await at({ exp: String(NOW + 60) })).toBe('exp is not a number');
    expect(await at({ nbf: NOW + 60 })).toMatchObject({ subject: '1001' });
    expect(await at({ nbf: NOW + 61 })).toBe('not yet valid (nbf)');
    expect(await at({ iat: NOW + 60 })).toMatchObject({ subject: '1001' });
    expect(await at({ iat: NOW + 61 })).toBe('not yet valid (iat)');
    // A claim that is present must be a number: null is not absent.
    expect(await at({ nbf: null })).toBe('nbf is not a number');
    expect(await at({ iat: null })).toBe('iat is not a number');
    expect(await at({ exp: null })).toBe('exp is not a number');
  });

  test('the leeway is configurable', async () => {
    const authenticate = serviceAuthenticator({ ...config(), leewaySeconds: 0 }, { now: clock });
    await expect(authenticate(contextWith(`Bearer ${await sign(ed, keypair({ exp: NOW }))}`))).rejects.toMatchObject({ status: 401 });
    expect(await authenticate(contextWith(`Bearer ${await sign(ed, keypair({ exp: NOW + 1 }))}`))).toMatchObject({ subject: 'orders' });
  });

  test('aud is a string or a list of strings that holds the audience', async () => {
    const authenticate = authenticatorWith();
    expect(await authenticate(contextWith(`Bearer ${await sign(rsa, google({ aud: ['https://other.test', 'https://shop-api.test'] }))}`))).toMatchObject({
      subject: '1001',
    });
    expect(await refusalOf(`Bearer ${await sign(rsa, google({ aud: 'https://other.test' }))}`)).toEqual(invalid('wrong audience'));
    expect(await refusalOf(`Bearer ${await sign(rsa, google({ aud: ['https://shop-api.test', 7] }))}`)).toEqual(invalid('wrong audience'));
    expect(await refusalOf(`Bearer ${await sign(rsa, google({ aud: undefined }))}`)).toEqual(invalid('wrong audience'));
  });

  test('maxLifetimeSeconds requires iat and bounds exp - iat', async () => {
    expect(await authenticatorWith()(contextWith(`Bearer ${await sign(ed, keypair({ exp: NOW + 300 }))}`))).toMatchObject({ subject: 'orders' });
    expect(await refusalOf(`Bearer ${await sign(ed, keypair({ exp: NOW + 301 }))}`)).toEqual(invalid('lifetime too long'));
    expect(await refusalOf(`Bearer ${await sign(ed, keypair({ iat: undefined }))}`)).toEqual(invalid('no iat'));
    const unbounded = serviceAuthenticator(
      { issuers: [{ ...config().issuers[2]!, maxLifetimeSeconds: 0 }] },
      { now: clock }
    );
    expect(await unbounded(contextWith(`Bearer ${await sign(ed, keypair({ iat: undefined, exp: NOW + 86_400 }))}`))).toMatchObject({ subject: 'orders' });
  });

  test('a verified identity the entry does not list is 403 service_forbidden', async () => {
    const forbidden = { status: 403, code: 'service_forbidden', detail: 'Service not permitted', reason: 'identity is no caller of this server' };
    expect(await refusalOf(`Bearer ${await sign(rsa, google({ sub: '2002' }))}`)).toEqual(forbidden);
    expect(await refusalOf(`Bearer ${await sign(rsa, google({ sub: 'constructor' }))}`)).toEqual(forbidden);
    expect(await refusalOf(`Bearer ${await sign(rsa, google({ sub: 1001 }))}`)).toEqual(forbidden);
    // The claim another entry names the caller by is not read.
    expect(await refusalOf(`Bearer ${await sign(ed, { iss: 'https://mail.test', aud: 'shop-api', sub: 'orders@shop.test', exp: NOW + 60 })}`)).toEqual(forbidden);
  });

  test('a caller is a copy: the implementation cannot change the config through it', async () => {
    const caller = await authenticatorWith()(contextWith(`Bearer ${await sign(rsa, google())}`));
    expect(caller?.serves).not.toBe(ORDERS.serves);
  });

  test('a caller that serves no API has an empty serves list', async () => {
    const job = serviceAuthenticator({ issuers: [{ ...config().issuers[2]!, callers: { orders: { deployable: 'nightly-job' } } }] }, { now: clock });
    expect(await job(contextWith(`Bearer ${await sign(ed, keypair())}`))).toEqual({ deployable: 'nightly-job', serves: [], subject: 'orders' });
  });
});

describe('serviceAuthenticator JWKS cache', () => {
  test('fetches once, then reads the cache', async () => {
    const server = keyServer(() => [rsa.jwk]);
    const authenticate = serviceAuthenticator(config(), { now: clock, fetchKeys: server.fetchKeys });
    const token = await sign(rsa, google());
    await Promise.all([authenticate(contextWith(`Bearer ${token}`)), authenticate(contextWith(`Bearer ${token}`))]);
    await authenticate(contextWith(`Bearer ${token}`));
    expect(server.fetches).toEqual([{ url: 'https://keys.test/google' }]);
  });

  test('an unknown kid fetches again at most once a minute', async () => {
    let now = NOW * 1000;
    let listed = [rsa.jwk];
    const server = keyServer(() => listed);
    const authenticate = serviceAuthenticator(config(), { now: () => now, fetchKeys: server.fetchKeys });
    const current = await sign(rsa, google());
    const next = await sign(rotated, google());
    expect(await authenticate(contextWith(`Bearer ${current}`))).toMatchObject({ subject: '1001' });
    listed = [rsa.jwk, rotated.jwk];
    // Fetched under a minute ago: the new kid is unknown until the next fetch.
    await expect(authenticate(contextWith(`Bearer ${next}`))).rejects.toMatchObject({ status: 401, code: 'service_unauthorized' });
    expect(server.fetches.length).toBe(1);
    now += 60_000;
    expect(await authenticate(contextWith(`Bearer ${next}`))).toMatchObject({ subject: '1001' });
    expect(server.fetches.length).toBe(2);
    // A kid unknown after that fetch stays unknown for the next minute.
    const stranger = await sign(rotated, google(), { kid: 'rsa-3' });
    await expect(authenticate(contextWith(`Bearer ${stranger}`))).rejects.toMatchObject({ status: 401 });
    await expect(authenticate(contextWith(`Bearer ${stranger}`))).rejects.toMatchObject({ status: 401 });
    expect(server.fetches.length).toBe(2);
  });

  test('keys older than an hour are fetched again; a failed fetch keeps them', async () => {
    let now = NOW * 1000;
    let failing = false;
    const server = keyServer(() => (failing ? new Error('connection refused') : [rsa.jwk]));
    const authenticate = serviceAuthenticator(config(), { now: () => now, fetchKeys: server.fetchKeys });
    const token = await sign(rsa, google({ exp: NOW + 7200 }));
    await authenticate(contextWith(`Bearer ${token}`));
    now += 59 * 60_000;
    await authenticate(contextWith(`Bearer ${token}`));
    expect(server.fetches.length).toBe(1);
    now += 60_000;
    failing = true;
    expect(await authenticate(contextWith(`Bearer ${token}`))).toMatchObject({ subject: '1001' });
    expect(server.fetches.length).toBe(2);
    // The failed fetch is retried a minute later, not on every request.
    expect(await authenticate(contextWith(`Bearer ${token}`))).toMatchObject({ subject: '1001' });
    expect(server.fetches.length).toBe(2);
    now += 60_000;
    failing = false;
    await authenticate(contextWith(`Bearer ${token}`));
    expect(server.fetches.length).toBe(3);
  });

  test('no keys and a failed fetch is 503 service_unavailable, until a fetch a minute later succeeds', async () => {
    let now = NOW * 1000;
    let failing = true;
    const server = keyServer(() => (failing ? new Error('connection refused') : [rsa.jwk]));
    const authenticate = serviceAuthenticator(config(), { now: () => now, fetchKeys: server.fetchKeys });
    const token = await sign(rsa, google());
    const refusal = await authenticate(contextWith(`Bearer ${token}`)).catch((error: unknown) => error);
    expect(refusal).toBeInstanceOf(HttpProblem);
    expect(refusal).toMatchObject({ status: 503, code: 'service_unavailable', message: 'Service credential could not be checked' });
    expect(((refusal as HttpProblem).cause as Error).message).toBe('connection refused');
    failing = false;
    // A key endpoint that is down is not asked again on every request.
    await expect(authenticate(contextWith(`Bearer ${token}`))).rejects.toMatchObject({ status: 503 });
    expect(server.fetches.length).toBe(1);
    now += 60_000;
    expect(await authenticate(contextWith(`Bearer ${token}`))).toMatchObject({ subject: '1001' });
    expect(server.fetches.length).toBe(2);
  });

  test('a fetched key without a kid, for another use, that cannot be used, or with a kid already seen is left out', async () => {
    const listed: PublicJwk[] = [
      { ...rsa.jwk, kid: undefined },
      { ...rsa.jwk, use: 'enc' },
      { ...rotated.jwk, d: 'private' },
      { ...rotated.jwk, kid: 'rsa-3', kty: 'oct' },
      { ...rotated.jwk, kid: 'rsa-1' },
      { ...rsa.jwk, kid: 'rsa-1', use: 'sig' },
    ];
    const authenticate = serviceAuthenticator(config(), { now: clock, fetchKeys: keyServer(() => listed).fetchKeys });
    // rsa-1 is the first usable key with that kid, which rotated's key holds; rsa-2 carries d.
    expect(await authenticate(contextWith(`Bearer ${await sign(rotated, google(), { kid: 'rsa-1' })}`))).toMatchObject({ subject: '1001' });
    await expect(authenticate(contextWith(`Bearer ${await sign(rsa, google())}`))).rejects.toMatchObject({ status: 401 });
    await expect(authenticate(contextWith(`Bearer ${await sign(rotated, google())}`))).rejects.toMatchObject({ status: 401 });
  });

  test('a response that is not a JWKS is a failed fetch', async () => {
    const authenticate = serviceAuthenticator(config(), { now: clock, fetchKeys: async () => ({ keys: 'nope' }) });
    await expect(authenticate(contextWith(`Bearer ${await sign(rsa, google())}`))).rejects.toMatchObject({ status: 503 });
  });

  test('jwksBearerTokenFile sends the file, trimmed, as the fetch bearer', async () => {
    const server = keyServer(() => [rsa.jwk]);
    const read: string[] = [];
    const kubernetesJwks: ServiceAuthConfig = {
      issuers: [
        {
          issuer: 'https://kubernetes.default.svc',
          audience: 'shop-api',
          algorithms: ['RS256'],
          jwksUrl: 'https://kubernetes.default.svc/openid/v1/jwks',
          jwksBearerTokenFile: '/var/run/secrets/kubernetes.io/serviceaccount/token',
          callers: { 'system:serviceaccount:shop:orders': ORDERS },
        },
      ],
    };
    const authenticate = serviceAuthenticator(kubernetesJwks, {
      now: clock,
      fetchKeys: server.fetchKeys,
      readFile: async path => {
        read.push(path);
        return 'own-token\n';
      },
    });
    const token = await sign(rsa, kubernetes({ iss: 'https://kubernetes.default.svc' }));
    expect(await authenticate(contextWith(`Bearer ${token}`))).toMatchObject({ deployable: 'orders' });
    expect(read).toEqual(['/var/run/secrets/kubernetes.io/serviceaccount/token']);
    expect(server.fetches).toEqual([{ url: 'https://kubernetes.default.svc/openid/v1/jwks', bearer: 'own-token' }]);
  });
});

describe('serviceAuthenticator config', () => {
  const entry = () => ({ issuer: 'orders', audience: 'shop-api', algorithms: ['EdDSA'], keys: [ed.jwk], callers: {} });
  const refused = (issuers: unknown[], extra: Record<string, unknown> = {}) => () => serviceAuthenticator({ issuers, ...extra } as unknown as ServiceAuthConfig);

  test('refuses a config it cannot use', () => {
    expect(refused([{ ...entry(), jwksUrl: 'https://keys.test' }])).toThrow('service auth config: issuers[0] needs jwksUrl or keys, not both');
    expect(refused([{ ...entry(), keys: undefined }])).toThrow('issuers[0] needs jwksUrl or keys, not both');
    expect(refused([{ ...entry(), keys: [] }])).toThrow('issuers[0] needs jwksUrl or keys, not both');
    expect(refused([{ ...entry(), algorithms: ['HS256'] }])).toThrow('issuers[0].algorithms has "HS256"; accepted are RS256, ES256 and EdDSA');
    expect(refused([{ ...entry(), algorithms: ['none'] }])).toThrow('accepted are RS256, ES256 and EdDSA');
    expect(refused([{ ...entry(), algorithms: [] }])).toThrow('issuers[0].algorithms is required');
    expect(refused([{ ...entry(), audience: '' }])).toThrow('issuers[0].audience is required');
    expect(refused([{ ...entry(), issuer: '' }])).toThrow('issuers[0].issuer is required');
    expect(refused([{ ...entry(), jwksBearerTokenFile: '/token' }])).toThrow('issuers[0].jwksBearerTokenFile is read with jwksUrl only');
    expect(refused([{ ...entry(), callers: undefined }])).toThrow('issuers[0].callers is required');
    expect(refused([{ ...entry(), callers: { orders: { serves: [] } } }])).toThrow('issuers[0].callers["orders"] needs a deployable, and the APIs it serves as a list');
    expect(refused([{ ...entry(), callers: { orders: { deployable: 'orders', serves: 'shop-orders' } } }])).toThrow('the APIs it serves as a list');
    expect(refused([entry(), { ...entry(), issuer: 'billing', issuerAliases: ['orders'] }])).toThrow('issuer "orders" is listed twice');
    expect(refused([entry()], { leewaySeconds: -1 })).toThrow('leewaySeconds must be a number, 0 or more');
    expect(refused([{ ...entry(), maxLifetimeSeconds: -1 }])).toThrow('issuers[0].maxLifetimeSeconds must be a number, 0 or more');
  });

  test('refuses a static key without a kid of its own, or one the verifier cannot use', async () => {
    const small = await crypto.subtle.generateKey(
      { name: 'RSASSA-PKCS1-v1_5', modulusLength: 1024, publicExponent: new Uint8Array([1, 0, 1]), hash: 'SHA-256' },
      true,
      ['sign', 'verify']
    );
    const smallJwk = (await crypto.subtle.exportKey('jwk', small.publicKey)) as PublicJwk;
    const withKeys = (keys: unknown[]) => refused([{ ...entry(), keys }]);
    expect(withKeys([{ ...ed.jwk, kid: undefined }])).toThrow('issuers[0].keys[0] has no kid');
    expect(withKeys([ed.jwk, ed.jwk])).toThrow('issuers[0].keys[1]: kid "ed-1" repeats');
    expect(withKeys([{ ...ed.jwk, d: 'private' }])).toThrow('issuers[0].keys[0] ("ed-1"): the key carries a private member (d)');
    expect(withKeys([{ ...smallJwk, kid: 'small' }])).toThrow('the RSA modulus has fewer than 2048 bits');
    expect(withKeys([{ ...rsa.jwk, e: 'Ag' }])).toThrow('the RSA exponent is not an odd integer from 3 to 2^31-1');
    expect(withKeys([{ ...ec.jwk, crv: 'P-384' }])).toThrow('the EC curve "P-384" is not P-256');
    expect(withKeys([{ ...ec.jwk, x: 'AAAA' }])).toThrow('the P-256 coordinates are not 32 bytes each');
    expect(withKeys([{ ...ed.jwk, crv: 'X25519' }])).toThrow('the OKP curve "X25519" is not Ed25519');
    expect(withKeys([{ kty: 'oct', k: 'c2VjcmV0', kid: 'hmac' }])).toThrow('the key type "oct" is not supported');
    expect(withKeys([{ kid: 'blank' }])).toThrow('the key has no kty');
    // Padding on a key member is tolerated.
    expect(() => serviceAuthenticator({ issuers: [{ ...entry(), keys: [{ ...ed.jwk, x: `${ed.jwk.x}=` }] }] } as unknown as ServiceAuthConfig)).not.toThrow();
  });

  test('an empty issuer list is a config: every credential is refused', async () => {
    const authenticate = serviceAuthenticator({ issuers: [] }, { now: clock });
    await expect(authenticate(contextWith(`Bearer ${await sign(ed, keypair())}`))).rejects.toMatchObject({ status: 401 });
  });
});

describe('authorizeService', () => {
  const orders = { deployable: 'orders', serves: ['shop-orders'], subject: 'orders' };
  const billing = { deployable: 'billing', serves: ['shop-billing'], subject: 'billing' };

  test('no service clause: the end-user step runs, whoever called', () => {
    expect(authorizeService(null, undefined)).toBe(false);
    expect(authorizeService(orders, undefined)).toBe(false);
  });

  const thrown = (run: () => unknown) => {
    try {
      run();
    } catch (error) {
      return error;
    }
    throw new Error('nothing was thrown');
  };

  test('require: a missing caller is 401, an unlisted one 403, a listed one goes on to the end-user step', () => {
    const require = { mode: 'require', from: ['shop-orders'] } as const;
    expect(thrown(() => authorizeService(null, require))).toMatchObject({ status: 401, code: 'service_unauthorized', message: 'Service credential required' });
    expect(thrown(() => authorizeService(billing, require))).toMatchObject({ status: 403, code: 'service_forbidden', message: 'Service not permitted' });
    expect(authorizeService(orders, require)).toBe(false);
    expect(authorizeService(billing, { mode: 'require', from: [] })).toBe(false);
  });

  test('allow: a listed caller skips the end-user step; anyone else goes through it', () => {
    const allow = { mode: 'allow', from: ['shop-orders'] } as const;
    expect(authorizeService(orders, allow)).toBe(true);
    expect(authorizeService(billing, allow)).toBe(false);
    expect(authorizeService(null, allow)).toBe(false);
    expect(authorizeService(billing, { mode: 'allow', from: [] })).toBe(true);
  });
});
