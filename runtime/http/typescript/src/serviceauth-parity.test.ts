import { describe, expect, test } from 'bun:test';
import { existsSync, readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { Hono } from 'hono';
import {
  ed25519PublicJwkFromPem,
  serviceAuthenticator,
  signedTokenSource,
  type Ed25519PrivateJwk,
  type OperationServiceCallers,
  type OperationSpec,
  type RequestContext,
  type ServiceAuthConfig,
  type ServiceCaller,
} from './index';
import { mountOperation } from './hono';
import { base64urlDecode, isPem } from './serviceauth';
import { sha256 } from './sha256';

/*
The service auth parity vectors (D37, section 9.8 of docs/stack-model.md),
which the Go runtime writes (`go test ./serviceauth -run
TestWriteParityVectors -update` in runtime/http/go) and the Go, TypeScript
and Rust runtimes each run through their own gate. Each vector mounts one
route with the vector's service clause and user clause, a service
authenticator over the named config (none when it is null) with the
vector's clock and a JWKS fetcher that serves the file's `jwks`, and an
end-user authenticator that looks the Authorization bearer up in `users`.
It compares the status, the problem code, the service caller and end user
the implementation saw (null unless it ran) and whether the end-user
authenticator ran; not bodies.
*/

const CORPUS = fileURLToPath(new URL('../../testdata/serviceauth_parity.json', import.meta.url));

interface ParityUser {
  readonly subject: string;
  readonly permissions: readonly string[];
}

interface ParityVector {
  readonly name: string;
  readonly now: number;
  readonly config: string | null;
  readonly route: {
    readonly service: OperationServiceCallers | null;
    readonly user: { readonly required: boolean; readonly permissions: readonly string[] };
  };
  readonly headers: Readonly<Record<string, string>>;
  readonly want: {
    readonly status: number;
    readonly code: string | null;
    readonly caller: ServiceCaller | null;
    readonly user: string | null;
    readonly userAuthenticated: boolean;
  };
}

/** A vector of the signed-token source (D63); the file's comment states the harness. */
interface ParitySigning {
  readonly name: string;
  readonly key: string;
  readonly publicKey: string;
  readonly issuer: string;
  readonly audience: string;
  readonly now: number;
  readonly want: { readonly header: string; readonly kid: string; readonly claims: Readonly<Record<string, unknown>> } | null;
}

interface ParityCorpus {
  readonly users: Readonly<Record<string, ParityUser>>;
  readonly jwks: Readonly<Record<string, unknown>>;
  readonly configs: Readonly<Record<string, ServiceAuthConfig>>;
  readonly vectors: readonly ParityVector[];
  readonly signing: readonly ParitySigning[];
}

/** The token the signed-token source signs for a vector, or the Error it refuses the key with. */
async function signFor(vector: ParitySigning): Promise<string | Error> {
  try {
    const key = isPem(vector.key) ? vector.key : (JSON.parse(vector.key) as Ed25519PrivateJwk);
    const claims = { issuer: vector.issuer, subject: vector.issuer, audience: vector.audience };
    return await signedTokenSource(key, claims, { now: () => vector.now * 1000 })(false);
  } catch (error) {
    return error as Error;
  }
}

async function run(corpus: ParityCorpus, vector: ParityVector) {
  let seen: { caller: ServiceCaller | null; user: string | null } = { caller: null, user: null };
  let userAuthenticated = false;
  const config = vector.config === null ? undefined : corpus.configs[vector.config];
  if (vector.config !== null && !config) throw new Error(`vector ${vector.name} names config ${vector.config}, which the file does not have`);
  const verify = config
    ? serviceAuthenticator(config, {
        now: () => vector.now * 1000,
        fetchKeys: async url => {
          if (!Object.hasOwn(corpus.jwks, url)) throw new Error(`${url} is unreachable`);
          return corpus.jwks[url];
        },
      })
    : undefined;
  const spec: OperationSpec = {
    name: 'parity',
    namespace: 'parity',
    method: 'GET',
    path: '/api/parity',
    pathParams: [],
    queryParams: [],
    bodyParams: [],
    auth: { public: false, required: vector.route.user.required, permissions: vector.route.user.permissions },
    ...(vector.route.service ? { service: vector.route.service } : {}),
    manual: false,
  };
  const app = new Hono();
  const implementation = async (ctx: RequestContext) => {
    seen = { caller: ctx.serviceCaller, user: ctx.principal?.subject ?? null };
    return null;
  };
  mountOperation(app, spec, implementation, {
    authenticate: async ctx => {
      userAuthenticated = true;
      const found = ctx.bearerToken !== undefined && Object.hasOwn(corpus.users, ctx.bearerToken) ? corpus.users[ctx.bearerToken] : undefined;
      return found ? { subject: found.subject, permissions: found.permissions } : null;
    },
    ...(verify ? { authenticateService: verify } : {}),
  });
  const response = await app.request('/api/parity', { headers: vector.headers });
  const body = (await response.json()) as { code?: string };
  return { status: response.status, code: response.ok ? null : (body.code ?? null), ...seen, userAuthenticated };
}

const corpus = existsSync(CORPUS) ? (JSON.parse(readFileSync(CORPUS, 'utf8')) as ParityCorpus) : undefined;

describe('service auth parity vectors', () => {
  if (!corpus) {
    test.skip(`skipped: ${CORPUS} is absent; the Go runtime writes it`, () => {});
    return;
  }

  test('the file has vectors', () => {
    expect(corpus.vectors.length).toBeGreaterThan(0);
  });

  for (const vector of corpus.vectors) {
    test(vector.name, async () => {
      expect(await run(corpus, vector)).toEqual({
        status: vector.want.status,
        code: vector.want.code,
        caller: vector.want.caller,
        user: vector.want.user,
        userAuthenticated: vector.want.userAuthenticated,
      });
    });
  }
});

describe('signed-token source parity vectors', () => {
  if (!corpus) {
    test.skip(`skipped: ${CORPUS} is absent; the Go runtime writes it`, () => {});
    return;
  }

  test('the file has signing vectors', () => {
    expect(corpus.signing.length).toBeGreaterThan(0);
  });

  for (const vector of corpus.signing) {
    test(vector.name, async () => {
      const token = await signFor(vector);
      if (vector.want === null) {
        expect(token).toBeInstanceOf(Error);
        return;
      }
      if (token instanceof Error) throw token;
      const [header, payload] = token.split('.') as [string, string, string];
      expect(header).toBe(vector.want.header);
      const { jti, ...claims } = JSON.parse(new TextDecoder().decode(base64urlDecode(payload))) as Record<string, unknown>;
      expect(claims).toEqual({ ...vector.want.claims });
      expect(base64urlDecode(jti as string)?.length).toBe(16);
      const key = ed25519PublicJwkFromPem(vector.publicKey);
      expect(key.kid).toBe(vector.want.kid);
      const authenticate = serviceAuthenticator(
        {
          issuers: [
            {
              issuer: vector.issuer,
              audience: vector.audience,
              algorithms: ['EdDSA'],
              keys: [key],
              maxLifetimeSeconds: 300,
              callers: { [vector.issuer]: { deployable: vector.issuer, serves: ['shop-orders'] } },
            },
          ],
        },
        { now: () => vector.now * 1000 }
      );
      const caller = await authenticate({ headers: new Headers({ 'service-authorization': `Bearer ${token}` }) } as RequestContext);
      expect(caller?.deployable).toBe(vector.issuer);
    });
  }
});

describe('sha256', () => {
  test('digests the FIPS 180-4 examples', () => {
    const hex = (text: string) => Array.from(sha256(new TextEncoder().encode(text)), byte => byte.toString(16).padStart(2, '0')).join('');
    expect(hex('')).toBe('e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855');
    expect(hex('abc')).toBe('ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad');
    expect(hex('abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq')).toBe('248d6a61d20638b8e5c026930c3e6039a33ce45964ff2167f6ecedd419db06c1');
    expect(hex('a'.repeat(1000))).toBe('41edece42d63e8d9bf515a9ba6932e1c20cbc9f5a5d134645adb5db1b9737ea3');
  });
});
