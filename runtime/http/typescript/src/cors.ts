/*
The cross-origin requests a static site's browser makes to the APIs it
calls (D55, section 8.10 of docs/stack-model.md), the twin of the Go
runtime's cors package. Each API a site calls has a CORS field in a stack,
the origins of the sites that call it, which loadCors reads; its server
answers CORS for those origins, with credentials, the Authorization header
and the methods the API's operations use. It answers no other origin
itself: a preflight from one reaches the API's routes, whose identity
runtime answers its config's trusted origins (D55, amended). The vectors
in runtime/http/testdata/cors_parity.json hold both runtimes to one
decision. Nothing here imports from node: or a framework, so it runs
wherever a fetch handler does.
*/

/** The request headers a site's browser may send an API: what the generated SDKs send, the end user's token among them. */
export const CORS_ALLOWED_HEADERS: readonly string[] = ['Accept', 'Authorization', 'Content-Type'];

/** How long, in seconds, a browser may keep a preflight's answer. */
export const CORS_MAX_AGE = 600;

/** The Vary header of an answer to a request with an Origin, and of a preflight's. */
export const CORS_VARY = 'Origin';
export const CORS_PREFLIGHT_VARY = 'Origin, Access-Control-Request-Method, Access-Control-Request-Headers';

/** One API's CORS: the origins of the sites that call it, and the methods of its operations, in upper case. */
export interface CorsPolicy {
  readonly origins: readonly string[];
  readonly methods: readonly string[];
}

/**
 * What a request's CORS check decides: whether it is a preflight, whether
 * the server answers it itself with 204 and the headers alone, which it
 * does for a preflight from an origin the policy lists, and the headers
 * the answer carries. Any other request reaches the API with the headers,
 * a preflight from an origin the policy does not list included, so a CORS
 * check of the API's own, the identity runtime's for its trusted origins,
 * answers it.
 */
export interface CorsDecision {
  readonly preflight: boolean;
  readonly answer: boolean;
  readonly headers: Readonly<Record<string, string>>;
}

/** Reads a request header from a Headers or a plain record of canonical names. */
type HeaderSource = Headers | Readonly<Record<string, string | undefined>>;

function headerOf(headers: HeaderSource, name: string): string {
  if (headers instanceof Headers) return headers.get(name) ?? '';
  for (const [key, value] of Object.entries(headers)) {
    if (key.toLowerCase() === name.toLowerCase()) return value ?? '';
  }
  return '';
}

/** Whether a request with method and headers is a CORS preflight: OPTIONS with an Origin and an Access-Control-Request-Method. */
export function isPreflight(method: string, headers: HeaderSource): boolean {
  return method === 'OPTIONS' && headerOf(headers, 'Origin') !== '' && headerOf(headers, 'Access-Control-Request-Method') !== '';
}

/**
 * Decides a request's CORS under policy, as Go's Policy.Decide does. A
 * request with no Origin gets no header. One with an Origin the policy
 * lists gets Access-Control-Allow-Origin with that origin and
 * Access-Control-Allow-Credentials; its preflight is the server's to
 * answer, and gets the methods, the headers and the max age too. One with
 * an Origin the policy does not list gets Vary alone and is not the
 * server's to answer, a preflight included: the API answers it, and unless
 * a CORS check of its own allows the origin, the browser refuses the
 * answer.
 */
export function corsDecision(policy: CorsPolicy, method: string, headers: HeaderSource): CorsDecision {
  const preflight = isPreflight(method, headers);
  const origin = headerOf(headers, 'Origin');
  const out: Record<string, string> = {};
  if (origin === '') return { preflight, answer: false, headers: out };
  out['Vary'] = preflight ? CORS_PREFLIGHT_VARY : CORS_VARY;
  if (!policy.origins.includes(origin)) return { preflight, answer: false, headers: out };
  out['Access-Control-Allow-Origin'] = origin;
  out['Access-Control-Allow-Credentials'] = 'true';
  if (preflight) {
    out['Access-Control-Allow-Methods'] = policy.methods.join(', ');
    out['Access-Control-Allow-Headers'] = CORS_ALLOWED_HEADERS.join(', ');
    out['Access-Control-Max-Age'] = String(CORS_MAX_AGE);
  }
  return { preflight, answer: preflight, headers: out };
}

/** An API a server serves, with its policy: match reports whether its routes take a method on a path, and none takes every request. */
export interface CorsApi {
  readonly policy: CorsPolicy;
  readonly match?: (method: string, path: string) => boolean;
}

/** The method and path of an operation of a generated API package's operationSpecs, its path parameters in braces. */
export interface CorsOperation {
  readonly method: string;
  readonly path: string;
}

/**
 * Whether one of operations takes method on path: a `{param}` segment
 * takes any one segment. The methods of an API's CorsPolicy are those of
 * its operations (corsMethods).
 */
export function matchOperations(operations: readonly CorsOperation[]): (method: string, path: string) => boolean {
  const routes = operations.map(op => ({
    method: op.method.toUpperCase(),
    pattern: new RegExp(`^${op.path.split('/').map(part => (/^\{[^}]+\}$/u.test(part) ? '[^/]+' : part.replace(/[.*+?^${}()|[\]\\]/gu, '\\$&'))).join('/')}$`, 'u'),
  }));
  return (method, path) => routes.some(route => route.method === method.toUpperCase() && route.pattern.test(path));
}

/** The methods of operations, upper case, each once, in order. */
export function corsMethods(operations: readonly CorsOperation[]): string[] {
  const out: string[] = [];
  for (const op of operations) {
    const method = op.method.toUpperCase();
    if (!out.includes(method)) out.push(method);
  }
  return out;
}

/**
 * Answers CORS for the APIs a server serves around next, a fetch handler,
 * as Go's Handler does. A request with an Origin is the first API's whose
 * match takes it, by the method a preflight asks for in place of OPTIONS:
 * a preflight from an origin its policy lists is answered 204 with the
 * policy's headers, and any other request it takes reaches next, whose
 * answer carries them, a preflight from an origin the policy does not list
 * included, which the API's routes answer, through the identity runtime's
 * CORS for a trusted origin. A request no API takes reaches next as it is.
 */
export function corsHandler<A extends unknown[]>(
  apis: readonly CorsApi[],
  next: (request: Request, ...rest: A) => Response | Promise<Response>
): (request: Request, ...rest: A) => Promise<Response> {
  return async (request, ...rest) => {
    if (!request.headers.get('Origin')) return next(request, ...rest);
    const method = isPreflight(request.method, request.headers) ? request.headers.get('Access-Control-Request-Method')! : request.method;
    const path = new URL(request.url).pathname;
    const api = apis.find(candidate => !candidate.match || candidate.match(method, path));
    if (!api) return next(request, ...rest);
    const decision = corsDecision(api.policy, request.method, request.headers);
    if (decision.answer) return new Response(null, { status: 204, headers: decision.headers });
    const response = await next(request, ...rest);
    const answer = new Response(response.body, response);
    for (const [name, value] of Object.entries(decision.headers)) answer.headers.set(name, value);
    return answer;
  };
}
