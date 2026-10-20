import { describe, expect, test } from 'bun:test';
import { loopbackServiceAuth, serviceAuthenticator, SERVICE_AUTHORIZATION_HEADER, type RequestContext } from './index';

/*
The loopback key (section 9.9 of docs/stack-model.md): the parity vectors
hold its issuer, its token and the URL rule to Go's and Rust's; these check
what a server does with a key it generates at random.
*/

const request = (token: string) => ({ headers: new Headers({ 'service-authorization': `Bearer ${token}` }) }) as RequestContext;

describe('loopbackServiceAuth', () => {
  test('a random key admits the server as itself, and another key is refused', async () => {
    const serves = ['shop-api', 'shop-orders'];
    const loopback = await loopbackServiceAuth('Monolith', serves);
    serves.push('changed');
    const authenticate = serviceAuthenticator(loopback.accept({ issuers: [] }));
    expect(await authenticate(request(await loopback.token(false)))).toEqual({
      deployable: 'Monolith',
      serves: ['shop-api', 'shop-orders'],
      subject: 'Monolith',
    });

    const earlier = await loopbackServiceAuth('Monolith', serves);
    expect(earlier.issuer.keys?.[0]?.kid).not.toBe(loopback.issuer.keys?.[0]?.kid);
    await expect(authenticate(request(await earlier.token(false)))).rejects.toMatchObject({ status: 401, code: 'service_unauthorized' });
  });

  test('the credential goes to a loopback URL alone', async () => {
    const loopback = await loopbackServiceAuth('Monolith', []);
    expect(loopback.credentialFor('http://127.0.0.1:8080')).toBe(loopback.credential);
    expect(loopback.credential.headers).toEqual([SERVICE_AUTHORIZATION_HEADER]);
    expect(loopback.credentialFor('https://shop-api-abc.a.run.app')).toBeUndefined();
  });

  test('a field that names the server is refused, and the field is not changed', async () => {
    const loopback = await loopbackServiceAuth('Monolith', []);
    const field = { issuers: [loopback.issuer] };
    expect(() => loopback.accept(field)).toThrow(/the server's own loopback issuer/u);
    const empty = { issuers: [] };
    expect(loopback.accept(empty).issuers).toHaveLength(1);
    expect(empty.issuers).toHaveLength(0);
    await expect(loopbackServiceAuth('', [])).rejects.toThrow(/deployable/u);
  });
});
