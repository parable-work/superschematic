import type { HeaderSource } from './token.js';
import { parseGoURL } from './url.js';

/*
The cross-origin check on a request the session cookie authenticates and on
a login or register asking for a cookie session (D50). It is Go's
net/http.CrossOriginProtection with the config's trusted origins, the rule
the Go runtime runs from the standard library:

1. GET, HEAD and OPTIONS are allowed.
2. With a Sec-Fetch-Site header: same-origin and none are allowed; any
   other value is allowed only when Origin is a trusted origin, compared
   as strings, exactly.
3. Without one: no Origin is allowed; an Origin whose host (with its port,
   if any) is the request's Host is allowed, whatever its scheme; a trusted
   Origin is allowed; anything else, null included, is refused.

A bearer request skips it: no browser attaches a bearer token on its own.
The credentialed CORS answer is for the trusted origins alone.
*/

/** What the cross-origin check reads of a request. */
export interface CrossOriginRequest {
  readonly method: string;
  /** The request's Host header (Go's Request.Host): host and port, as the client sent it. */
  readonly host: string;
  readonly headers: HeaderSource;
}

function firstHeader(headers: HeaderSource, name: string): string {
  if (headers instanceof Headers) {
    // A Headers object joins a repeated header; Go reads the first value.
    const value = headers.get(name);
    if (value === null) return '';
    const comma = value.indexOf(',');
    return comma < 0 ? value : value.slice(0, comma);
  }
  for (const [key, value] of headers) {
    if (key.toLowerCase() === name) return value;
  }
  return '';
}

/** Runs the check: true when the request passes. */
export function crossOriginAllowed(trustedOrigins: readonly string[], request: CrossOriginRequest): boolean {
  switch (request.method) {
    case 'GET':
    case 'HEAD':
    case 'OPTIONS':
      return true;
  }
  const origin = firstHeader(request.headers, 'origin');
  const trusted = origin !== '' && trustedOrigins.includes(origin);
  switch (firstHeader(request.headers, 'sec-fetch-site')) {
    case '':
      break;
    case 'same-origin':
    case 'none':
      return true;
    default:
      return trusted;
  }
  if (origin === '') return true;
  const parsed = parseGoURL(origin);
  if (parsed && parsed.host === request.host) return true;
  return trusted;
}

/** The request's Host as Go's Request.Host has it: the Host header, else the URL's host. */
export function requestHost(request: Request): string {
  const host = request.headers.get('host');
  if (host !== null && host !== '') return host;
  try {
    return new URL(request.url).host;
  } catch {
    return '';
  }
}

/** What credentialed CORS answers a request: headers to add, and whether it is a preflight answered with 204. */
export interface CorsAnswer {
  readonly headers: readonly (readonly [string, string])[];
  readonly preflight: boolean;
}

/**
 * Credentialed CORS for the trusted origins, as the Go runtime's CORS
 * middleware answers it. A request whose Origin is trusted gets
 * Access-Control-Allow-Origin (the origin) and
 * Access-Control-Allow-Credentials: true, and its preflight (OPTIONS with
 * Access-Control-Request-Method) is answered with 204, the method and
 * headers it asks for, and a ten-minute Max-Age. Any other request gets no
 * CORS headers, so a browser keeps a page of another origin from reading
 * the answer. Every request with an Origin gets Vary: Origin.
 */
export function corsAnswer(trustedOrigins: readonly string[], method: string, headers: Headers): CorsAnswer {
  const origin = headers.get('origin') ?? '';
  if (origin === '') return { headers: [], preflight: false };
  const out: [string, string][] = [['vary', 'Origin']];
  if (!trustedOrigins.includes(origin)) return { headers: out, preflight: false };
  out.push(['access-control-allow-origin', origin], ['access-control-allow-credentials', 'true']);
  const requested = headers.get('access-control-request-method') ?? '';
  if (method !== 'OPTIONS' || requested === '') return { headers: out, preflight: false };
  out.push(['vary', 'Access-Control-Request-Method'], ['vary', 'Access-Control-Request-Headers'], ['access-control-allow-methods', requested]);
  const requestedHeaders = headers.get('access-control-request-headers') ?? '';
  if (requestedHeaders !== '') out.push(['access-control-allow-headers', requestedHeaders]);
  out.push(['access-control-max-age', '600']);
  return { headers: out, preflight: true };
}
