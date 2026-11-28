import { describe, expect, test } from 'bun:test';
import { ClientMetadataError, clientMetadataFetcher, readClientMetadata } from './index';

/*
The guarded fetch of a Client ID Metadata Document (#695): what it refuses
before and after the request, with fetch and the resolver stubbed.
*/

const URL_OF = 'https://client.example.com/oauth/metadata.json';
const DOCUMENT = { client_id: URL_OF, client_name: 'Example', redirect_uris: ['https://client.example.com/cb'] };

function fetcher(answer: (url: URL) => Response, addresses: readonly string[] | undefined = ['93.184.216.34']) {
  const seen: string[] = [];
  const fetch = (async (input: URL) => {
    seen.push(input.href);
    return answer(input);
  }) as unknown as typeof globalThis.fetch;
  return { fetch: clientMetadataFetcher({ fetch, resolve: async () => addresses }), seen };
}

async function refusal(promise: Promise<unknown>): Promise<string> {
  try {
    await promise;
  } catch (error) {
    expect(error).toBeInstanceOf(ClientMetadataError);
    return (error as Error).message;
  }
  throw new Error('the fetch succeeded');
}

describe('clientMetadataFetcher', () => {
  test('reads a document and keeps it as its cache headers say', async () => {
    const { fetch } = fetcher(() => new Response(JSON.stringify(DOCUMENT), { headers: { 'cache-control': 'max-age=120' } }));
    expect(await fetch(URL_OF)).toEqual({ clientId: URL_OF, name: 'Example', uri: null, redirectUris: ['https://client.example.com/cb'], ttlSeconds: 120 });
  });

  test('refuses a name that resolves to a private address, before any request', async () => {
    const { fetch, seen } = fetcher(() => new Response(JSON.stringify(DOCUMENT)), ['10.0.0.7']);
    expect(await refusal(fetch(URL_OF))).toContain('private address');
    expect(seen).toEqual([]);
  });

  test('refuses an address literal that is private, and localhost', async () => {
    const { fetch, seen } = fetcher(() => new Response(JSON.stringify(DOCUMENT)));
    expect(await refusal(fetch('https://169.254.169.254/latest/meta-data'))).toContain('private address');
    expect(await refusal(fetch('https://localhost/client.json'))).toContain("own machine");
    expect(await refusal(fetch('https://[::1]/client.json'))).toContain('private address');
    // The URL parser writes an IPv4-mapped address in hexadecimal.
    expect(await refusal(fetch('https://[::ffff:127.0.0.1]/client.json'))).toContain('private address');
    expect(await refusal(fetch('https://[::ffff:a9fe:a9fe]/latest'))).toContain('private address');
    expect(seen).toEqual([]);
  });

  test('follows a redirect on the same host and refuses one to another', async () => {
    const same = fetcher(url =>
      url.pathname === '/old' ? new Response(null, { status: 301, headers: { location: '/oauth/metadata.json' } }) : new Response(JSON.stringify({ ...DOCUMENT, client_id: 'https://client.example.com/old' }))
    );
    expect((await same.fetch('https://client.example.com/old')).clientId).toBe('https://client.example.com/old');
    expect(same.seen).toEqual(['https://client.example.com/old', 'https://client.example.com/oauth/metadata.json']);
    const other = fetcher(() => new Response(null, { status: 302, headers: { location: 'https://elsewhere.example.com/x' } }));
    expect(await refusal(other.fetch(URL_OF))).toContain('another host');
  });

  test('refuses a document larger than 16 KB, one that is not JSON, and a status other than 200', async () => {
    expect(await refusal(fetcher(() => new Response('x'.repeat(16 * 1024 + 1))).fetch(URL_OF))).toContain('larger than');
    expect(await refusal(fetcher(() => new Response('<html>')).fetch(URL_OF))).toContain('not JSON');
    expect(await refusal(fetcher(() => new Response('{}', { status: 404 })).fetch(URL_OF))).toContain('answered 404');
  });

  test('refuses a client_id that is no https URL with a path', async () => {
    const { fetch, seen } = fetcher(() => new Response(JSON.stringify(DOCUMENT)));
    expect(await refusal(fetch('http://client.example.com/meta.json'))).toContain('not an https URL');
    expect(await refusal(fetch('https://client.example.com/'))).toContain('no path');
    expect(seen).toEqual([]);
  });

  test('a runtime with no resolver checks the literal alone', async () => {
    const { fetch } = fetcher(() => new Response(JSON.stringify(DOCUMENT)), undefined);
    expect((await fetch(URL_OF)).name).toBe('Example');
  });
});

describe('readClientMetadata', () => {
  test('refuses a document whose client_id is not its URL, or that names a confidential client or a bad redirect', () => {
    expect(() => readClientMetadata(URL_OF, { ...DOCUMENT, client_id: 'https://other.example.com/m' }, 60)).toThrow('client_id');
    expect(() => readClientMetadata(URL_OF, { ...DOCUMENT, token_endpoint_auth_method: 'private_key_jwt' }, 60)).toThrow('public clients');
    expect(() => readClientMetadata(URL_OF, { ...DOCUMENT, redirect_uris: ['http://client.example.com/cb'] }, 60)).toThrow('loopback');
    expect(() => readClientMetadata(URL_OF, { ...DOCUMENT, redirect_uris: [] }, 60)).toThrow('redirect_uris');
  });

  test('names a client without client_name by its host', () => {
    const { client_name: _, ...unnamed } = DOCUMENT;
    expect(readClientMetadata(URL_OF, unnamed, 60).name).toBe('client.example.com');
  });
});
