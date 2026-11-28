import { readCapped } from '../read-capped.js';
import { isAllowedRedirect } from './oauth-protocol.js';
import { parseGoURL } from './url.js';

/*
Client ID Metadata Documents (#695): a client names itself by an https URL,
and the authorization server fetches the JSON document there, whose
client_id is that URL. The fetch is a request the server makes to a host
anyone names, so it is held: https alone, to a name that resolves to no
private, loopback or link-local address, at most 5 s and 16 KB, and no
redirect to another host. Where the runtime cannot resolve a name (a
Worker, which reaches no private network anyway) the address check is the
literal's alone.
*/

/** The most a metadata document holds, and how long its fetch may take. */
export const METADATA_DOCUMENT_MAX_BYTES = 16 * 1024;
export const METADATA_DOCUMENT_TIMEOUT_MS = 5000;
/** How long a document is kept when its response says nothing of caching, and the longest it is kept whatever it says. */
export const METADATA_DOCUMENT_DEFAULT_TTL_SECONDS = 60 * 60;
export const METADATA_DOCUMENT_MAX_TTL_SECONDS = 24 * 60 * 60;
/** The most same-host redirects a fetch follows. */
const MAX_REDIRECTS = 3;
/** The most redirects a document names, and the longest name. */
const MAX_REDIRECT_URIS = 10;
const MAX_CLIENT_NAME = 100;

/** A metadata document, as the server keeps it. */
export interface ClientMetadata {
  readonly clientId: string;
  readonly name: string;
  readonly uri: string | null;
  readonly redirectUris: readonly string[];
  /** How long the document may be kept, from its response's Cache-Control. */
  readonly ttlSeconds: number;
}

/** A metadata document the server refuses, or could not fetch. */
export class ClientMetadataError extends Error {
  constructor(message: string, options?: { cause?: unknown }) {
    super(`oauth: client metadata: ${message}`, options);
    this.name = 'ClientMetadataError';
  }
}

/** Fetches the document a client_id names. */
export type ClientMetadataFetcher = (clientId: string) => Promise<ClientMetadata>;

/**
 * The reason a client_id is no metadata document's URL (draft-ietf-oauth-
 * client-id-metadata-document section 3): https, a host, a path, and no
 * user, fragment, or dot segment; undefined for one that is.
 */
export function metadataDocumentUrlProblem(clientId: string): string | undefined {
  const u = parseGoURL(clientId);
  if (!u || u.scheme !== 'https' || u.host === '' || u.opaque !== '') return 'it is not an https URL';
  if (u.hasUser) return 'it has a user';
  if (u.fragment !== '' || clientId.includes('#')) return 'it has a fragment';
  if (u.path === '' || u.path === '/') return 'it has no path';
  if (u.path.split('/').some(segment => segment === '.' || segment === '..')) return 'it has a dot segment';
  return undefined;
}

/** Whether a client_id names a metadata document: an https URL, which a registered client's id never is. */
export function isMetadataDocumentUrl(clientId: string): boolean {
  return clientId.startsWith('https://');
}

/** Whether an IPv4 address, as four numbers, is private, loopback, link-local or otherwise not a public host. */
function privateIPv4(parts: readonly number[]): boolean {
  const [a, b] = parts as [number, number, number, number];
  return (
    a === 0 ||
    a === 10 ||
    a === 127 ||
    (a === 100 && b >= 64 && b <= 127) ||
    (a === 169 && b === 254) ||
    (a === 172 && b >= 16 && b <= 31) ||
    (a === 192 && b === 168) ||
    (a === 192 && b === 0) ||
    (a === 198 && (b === 18 || b === 19)) ||
    a >= 224
  );
}

function ipv4Of(address: string): number[] | undefined {
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/u.exec(address);
  if (!m) return undefined;
  const parts = m.slice(1).map(Number);
  return parts.every(n => n <= 255) ? parts : undefined;
}

/** An IPv6 address as its eight 16-bit groups, the IPv4 tail form included; undefined for text that is none. */
function ipv6Groups(address: string): number[] | undefined {
  let text = address.toLowerCase().replace(/%.*$/u, '');
  // A dotted IPv4 tail is two groups.
  const tail = /^(.*:)(\d+\.\d+\.\d+\.\d+)$/u.exec(text);
  if (tail) {
    const v4 = ipv4Of(tail[2]!);
    if (!v4) return undefined;
    text = `${tail[1]}${((v4[0]! << 8) | v4[1]!).toString(16)}:${((v4[2]! << 8) | v4[3]!).toString(16)}`;
  }
  const halves = text.split('::');
  if (halves.length > 2) return undefined;
  const part = (half: string) => (half === '' ? [] : half.split(':'));
  const head = part(halves[0]!);
  const rest = halves.length === 2 ? part(halves[1]!) : [];
  const missing = 8 - head.length - rest.length;
  if (halves.length === 1 ? missing !== 0 : missing < 1) return undefined;
  const groups = [...head, ...Array<string>(halves.length === 2 ? missing : 0).fill('0'), ...rest];
  if (groups.some(g => !/^[0-9a-f]{1,4}$/u.test(g))) return undefined;
  return groups.map(g => parseInt(g, 16));
}

/**
 * Whether an address, IPv4 or IPv6 without brackets, is one the fetch must
 * not reach: private, loopback, link-local, unique-local, unspecified,
 * multicast, documentation, or an IPv4 address of those embedded in IPv6
 * (mapped ::ffff:0:0/96, compatible ::/96, NAT64 64:ff9b::/96), in its
 * dotted or its hexadecimal form. Text that is no address is refused too.
 */
export function isPrivateAddress(address: string): boolean {
  const v4 = ipv4Of(address);
  if (v4) return privateIPv4(v4);
  if (!address.includes(':')) return false;
  const g = ipv6Groups(address);
  if (!g) return true;
  const embedded = () => [g[6]! >> 8, g[6]! & 0xff, g[7]! >> 8, g[7]! & 0xff];
  const zeroTo = (n: number) => g.slice(0, n).every(x => x === 0);
  if (zeroTo(5) && g[5] === 0xffff) return privateIPv4(embedded());
  if (zeroTo(6)) return g[6] === 0 && g[7]! <= 1 ? true : privateIPv4(embedded());
  if (g[0] === 0x64 && g[1] === 0xff9b && g.slice(2, 6).every(x => x === 0)) return privateIPv4(embedded());
  const first = g[0]!;
  return (
    (first & 0xfe00) === 0xfc00 || // fc00::/7, unique local
    (first & 0xffc0) === 0xfe80 || // fe80::/10, link local
    (first & 0xff00) === 0xff00 || // ff00::/8, multicast
    (first === 0x2001 && g[1] === 0xdb8) // documentation
  );
}

/** Resolves a host name to its addresses, or undefined where the runtime has no resolver. */
type Resolver = (hostname: string) => Promise<readonly string[] | undefined>;

// The resolver's module, named in a variable so a bundler leaves the import
// to the runtime: a Worker's bundle has no node:dns to resolve it to.
const DNS_MODULE = 'node:dns/promises';

async function defaultResolver(hostname: string): Promise<readonly string[] | undefined> {
  let dns: { lookup(host: string, options: { all: true; verbatim: true }): Promise<readonly { address: string }[]> };
  try {
    dns = (await import(DNS_MODULE)) as unknown as typeof dns;
  } catch {
    return undefined;
  }
  if (typeof dns?.lookup !== 'function') return undefined;
  let found;
  try {
    found = await dns.lookup(hostname, { all: true, verbatim: true });
  } catch (error) {
    // A name that does not resolve is refused; a runtime whose lookup is
    // not implemented checks the literal alone.
    const code = (error as { code?: string }).code;
    if (code === 'ENOTFOUND' || code === 'EAI_AGAIN' || code === 'ENODATA') throw error;
    return undefined;
  }
  return found.map(entry => entry.address);
}

/** Refuses a URL whose host is, or resolves to, an address the fetch must not reach. */
async function checkHost(url: URL, resolve: Resolver): Promise<void> {
  const hostname = url.hostname.replace(/^\[|\]$/gu, '');
  if (hostname === 'localhost' || hostname.endsWith('.localhost')) throw new ClientMetadataError(`${url.host} is the server's own machine`);
  if (isPrivateAddress(hostname)) throw new ClientMetadataError(`${url.host} is a private address`);
  if (ipv4Of(hostname) || hostname.includes(':')) return;
  let addresses: readonly string[] | undefined;
  try {
    addresses = await resolve(hostname);
  } catch (error) {
    throw new ClientMetadataError(`${url.host} does not resolve`, { cause: error });
  }
  if (addresses === undefined) return;
  if (addresses.length === 0) throw new ClientMetadataError(`${url.host} does not resolve`);
  for (const address of addresses) {
    if (isPrivateAddress(address)) throw new ClientMetadataError(`${url.host} resolves to a private address`);
  }
}

/**
 * How long a response says it may be kept, in seconds: Cache-Control's
 * max-age, 0 for no-store or no-cache, the default without either, and
 * never past the longest.
 */
export function cacheSecondsOf(cacheControl: string | null): number {
  if (!cacheControl) return METADATA_DOCUMENT_DEFAULT_TTL_SECONDS;
  const directives = cacheControl.toLowerCase().split(',').map(d => d.trim());
  if (directives.some(d => d === 'no-store' || d === 'no-cache')) return 0;
  const maxAge = directives.map(d => /^max-age=(\d+)$/u.exec(d)).find(m => m !== null);
  if (!maxAge) return METADATA_DOCUMENT_DEFAULT_TTL_SECONDS;
  return Math.min(Number(maxAge[1]), METADATA_DOCUMENT_MAX_TTL_SECONDS);
}

async function readDocument(response: Response): Promise<string> {
  const bytes = await readCapped(response.body, METADATA_DOCUMENT_MAX_BYTES);
  if (!bytes) throw new ClientMetadataError(`the document is larger than ${METADATA_DOCUMENT_MAX_BYTES} bytes`);
  return new TextDecoder().decode(bytes);
}

/**
 * Reads a metadata document's JSON for the client_id it was fetched from:
 * its client_id equals that URL, its redirect_uris are 1 to 10 allowed
 * redirects, its token_endpoint_auth_method is none or absent, and its
 * client_name and client_uri, when present, are a short string and an
 * https URL.
 */
export function readClientMetadata(clientId: string, json: unknown, ttlSeconds: number): ClientMetadata {
  if (typeof json !== 'object' || json === null || Array.isArray(json)) throw new ClientMetadataError('the document is not a JSON object');
  const doc = json as Record<string, unknown>;
  if (doc.client_id !== clientId) throw new ClientMetadataError('its client_id is not the URL it was fetched from');
  const redirects = doc.redirect_uris;
  if (!Array.isArray(redirects) || redirects.length === 0 || redirects.length > MAX_REDIRECT_URIS) {
    throw new ClientMetadataError(`its redirect_uris must list 1 to ${MAX_REDIRECT_URIS} URLs`);
  }
  for (const uri of redirects) {
    if (typeof uri !== 'string' || !isAllowedRedirect(uri)) throw new ClientMetadataError(`its redirect ${JSON.stringify(uri)} is not https, or http on a loopback host`);
  }
  const method = doc.token_endpoint_auth_method;
  if (method !== undefined && method !== 'none') throw new ClientMetadataError('its token_endpoint_auth_method is not none: the server admits public clients alone');
  let name = new URL(clientId).host;
  if (doc.client_name !== undefined) {
    if (typeof doc.client_name !== 'string' || doc.client_name.trim() === '') throw new ClientMetadataError('its client_name is not a string');
    name = [...doc.client_name.trim()].slice(0, MAX_CLIENT_NAME).join('');
  }
  let uri: string | null = null;
  if (doc.client_uri !== undefined) {
    if (typeof doc.client_uri !== 'string' || !doc.client_uri.startsWith('https://')) throw new ClientMetadataError('its client_uri is not an https URL');
    uri = doc.client_uri;
  }
  return { clientId, name, uri, redirectUris: [...(redirects as string[])], ttlSeconds };
}

/** What fetchClientMetadata takes beside the client_id, for tests. */
export interface ClientMetadataFetchOptions {
  readonly fetch?: typeof fetch;
  readonly resolve?: Resolver;
}

/**
 * Fetches and reads the metadata document a client_id names, with the
 * guard this module's comment states.
 */
export function clientMetadataFetcher(options: ClientMetadataFetchOptions = {}): ClientMetadataFetcher {
  const doFetch = options.fetch ?? fetch;
  const resolve = options.resolve ?? defaultResolver;
  return async clientId => {
    const problem = metadataDocumentUrlProblem(clientId);
    if (problem) throw new ClientMetadataError(`${JSON.stringify(clientId)} is no metadata document's URL: ${problem}`);
    let url = new URL(clientId);
    const origin = url.host;
    const signal = AbortSignal.timeout(METADATA_DOCUMENT_TIMEOUT_MS);
    for (let hops = 0; ; hops++) {
      await checkHost(url, resolve);
      let response: Response;
      try {
        response = await doFetch(url, { redirect: 'manual', signal, headers: { accept: 'application/json' } });
      } catch (error) {
        throw new ClientMetadataError(`fetching ${url.href} failed`, { cause: error });
      }
      if (response.status >= 300 && response.status < 400) {
        await response.body?.cancel();
        const location = response.headers.get('location');
        if (!location || hops >= MAX_REDIRECTS) throw new ClientMetadataError(`${url.href} redirects too often, or nowhere`);
        const next = new URL(location, url);
        if (next.protocol !== 'https:' || next.host !== origin) throw new ClientMetadataError(`${url.href} redirects to another host`);
        url = next;
        continue;
      }
      if (response.status !== 200) {
        await response.body?.cancel();
        throw new ClientMetadataError(`${url.href} answered ${response.status}`);
      }
      const text = await readDocument(response);
      let json: unknown;
      try {
        json = JSON.parse(text);
      } catch (error) {
        throw new ClientMetadataError('the document is not JSON', { cause: error });
      }
      return readClientMetadata(clientId, json, cacheSecondsOf(response.headers.get('cache-control')));
    }
  };
}
