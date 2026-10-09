import { afterAll, describe, expect, test } from 'bun:test';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import {
  CALLERS_SUFFIX,
  StackConfigError,
  loadCallers,
  loadDatabase,
  loadService,
  serviceAuthenticator,
  serviceCredentialFor,
  type RequestContext,
  type Service,
  type ServiceAuthConfig,
} from './index';

/*
What the parity vectors (stackconfig-parity.test.ts) do not cover: the
readers' default environment, and serviceCredentialFor, which maps a loaded
credential onto the runtime's sources.
*/

const NOW = 1_767_225_600;

interface Vector {
  readonly name: string;
  readonly want: unknown;
}

const vectors = (
  JSON.parse(readFileSync(fileURLToPath(new URL('../../testdata/stackconfig_parity.json', import.meta.url)), 'utf8')) as {
    readonly vectors: readonly Vector[];
  }
).vectors;

function wantOf(name: string): unknown {
  const vector = vectors.find(v => v.name === name);
  if (!vector) throw new Error(`no vector ${name}`);
  return vector.want;
}

describe('the readers', () => {
  const saved = { ...process.env };
  afterAll(() => {
    for (const name of Object.keys(process.env)) if (!(name in saved)) delete process.env[name];
  });

  test('read process.env by default', () => {
    process.env.ORDERS_DB_DATABASE_URL = 'postgres://orders';
    process.env.PAYMENTS_SERVICE_URL = 'http://payments';
    process.env[`PAYMENTS${CALLERS_SUFFIX}_ISSUERS`] = '0';
    expect(loadDatabase('ORDERS_DB_DATABASE')).toEqual({ url: 'postgres://orders' });
    expect(loadService('PAYMENTS_SERVICE')).toEqual({ url: 'http://payments' });
    expect(loadCallers(`PAYMENTS${CALLERS_SUFFIX}`)).toEqual({ issuers: [] });
  });

  test('a refusal is a StackConfigError that lists each problem', () => {
    try {
      loadService('SHOP_API_SERVICE', { SHOP_API_SERVICE_URL: 'u', SHOP_API_SERVICE_CREDENTIAL_SOURCE: 'signed-token' });
      throw new Error('loaded');
    } catch (err) {
      expect(err).toBeInstanceOf(StackConfigError);
      expect((err as StackConfigError).name).toBe('StackConfigError');
      expect((err as StackConfigError).problems).toEqual([
        'required environment variable SHOP_API_SERVICE_CREDENTIAL_AUDIENCE is not set: a signed-token credential reads it',
        'required environment variable SHOP_API_SERVICE_CREDENTIAL_ISSUER is not set: a signed-token credential reads it',
        'required environment variable SHOP_API_SERVICE_CREDENTIAL_KEY is not set: a signed-token credential reads it',
      ]);
    }
  });

  test('never put a value in a message', () => {
    const secret = '{"kty":"OKP","d":"private"}';
    expect(() =>
      loadService('SHOP_API_SERVICE', { SHOP_API_SERVICE_URL: 'u', SHOP_API_SERVICE_CREDENTIAL_SOURCE: 'token-file', SHOP_API_SERVICE_CREDENTIAL_KEY: secret })
    ).toThrow(/^(?!.*private)/su);
  });
});

describe('serviceCredentialFor', () => {
  const directory = mkdtempSync(join(tmpdir(), 'stackconfig-'));
  afterAll(() => rmSync(directory, { recursive: true, force: true }));

  test('no credential is none', () => {
    expect(serviceCredentialFor(undefined)).toBeUndefined();
  });

  test('a signed token is signed with the key as the issuer, for the audience, and the callee verifies it', async () => {
    // The vectors' signed-token endpoint and local callers field share an
    // edge's key pair, as the local target writes them.
    const endpoint = wantOf('service: a signed token') as Service;
    const config = wantOf('callers: a local issuer with keys') as ServiceAuthConfig;
    const credential = serviceCredentialFor(endpoint.credential!, { signedToken: { now: () => NOW * 1000 } });
    expect(credential.headers).toEqual(['Service-Authorization']);
    const token = await credential.token(false);
    const authenticate = serviceAuthenticator(config, { now: () => NOW * 1000 });
    const headers = new Headers({ 'service-authorization': `Bearer ${token}` });
    expect(await authenticate({ headers } as RequestContext)).toEqual({ deployable: 'Orders', serves: ['shop-orders'], subject: 'Orders' });
  });

  test('a signed-token key that is not a JWK is refused at once', () => {
    expect(() => serviceCredentialFor({ source: 'signed-token', audience: 'a', issuer: 'i', key: 'key' })).toThrow(
      'serviceCredentialFor: the signed-token key is not a JWK'
    );
    expect(() => serviceCredentialFor({ source: 'signed-token', audience: 'a', issuer: 'i', key: '{"kty":"RSA"}' })).toThrow(
      'the key must be an Ed25519 private JWK'
    );
  });

  test('a token file is read from its path', async () => {
    const path = join(directory, 'token');
    writeFileSync(path, 'projected-token\n');
    const credential = serviceCredentialFor({ source: 'token-file', tokenFile: path });
    expect(await credential.token(false)).toBe('projected-token');
  });

  test('a Google ID token is asked for the audience, and travels in the credential headers', async () => {
    const endpoint = wantOf('service: a Google ID token in two headers') as Service;
    const urls: string[] = [];
    const credential = serviceCredentialFor(endpoint.credential!, {
      googleIdToken: {
        fetch: async url => {
          urls.push(url);
          return new Response('id-token');
        },
      },
    });
    expect(credential.headers).toEqual(['Service-Authorization', 'X-Serverless-Authorization']);
    expect(await credential.token(false)).toBe('id-token');
    expect(urls).toEqual([
      'http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/identity?audience=https%3A%2F%2Fshop-api-abc-ue.a.run.app',
    ]);
  });
});
