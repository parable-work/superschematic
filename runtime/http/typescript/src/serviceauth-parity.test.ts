import { describe, expect, test } from 'bun:test';
import { existsSync, readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { Hono } from 'hono';
import {
  serviceAuthenticator,
  type OperationServiceCallers,
  type OperationSpec,
  type RequestContext,
  type ServiceAuthConfig,
  type ServiceCaller,
} from './index';
import { mountOperation } from './hono';

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

interface ParityCorpus {
  readonly users: Readonly<Record<string, ParityUser>>;
  readonly jwks: Readonly<Record<string, unknown>>;
  readonly configs: Readonly<Record<string, ServiceAuthConfig>>;
  readonly vectors: readonly ParityVector[];
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
