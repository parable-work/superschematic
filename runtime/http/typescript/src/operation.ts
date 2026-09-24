import type { Principal } from './auth';
import type { ParamSpec } from './params';

/*
The operation table the generated router carries. One OperationSpec per @rest
operation: the schema is the source of every field here, and the adapter reads
nothing else about an operation.
*/

export interface OperationAuth {
  /** @publicRoute: intentionally unauthenticated. */
  readonly public: boolean;
  /** @auth, @requirePermission or @requireOwnership: a principal is required (401 otherwise). */
  readonly required: boolean;
  /** @requirePermission: the principal must cover one of these (403 otherwise). */
  readonly permissions: readonly string[];
}

export interface OperationInput {
  /** The generated strict parser of the input type (parse<Input>FromJSON). */
  readonly parse: (value: unknown) => unknown;
  readonly required: boolean;
}

export interface OperationSpec {
  /** Operation name as declared in the schema (camelCase). */
  readonly name: string;
  /** Kebab-case namespace derived from the operation class name. */
  readonly namespace: string;
  readonly method: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE';
  /** Full route path with `{param}` placeholders, `/api/...` like the Go and Rust routers. */
  readonly path: string;
  readonly pathParams: readonly ParamSpec[];
  readonly queryParams: readonly ParamSpec[];
  /** Undecorated scalar arguments of a non-GET operation, read from the JSON body object. */
  readonly bodyParams: readonly ParamSpec[];
  readonly input?: OperationInput;
  readonly auth: OperationAuth;
  /** @bodyLimit in bytes; the adapter's default applies when absent. */
  readonly bodyLimitBytes?: number;
  /** @rateLimit: requests per minute per client (the Go runtime's per-route, per-IP limiter); 429 past it. */
  readonly rateLimitPerMinute?: number;
  /** @timeout: seconds the operation may take before the adapter answers 504 (the Go runtime's Timeout middleware). */
  readonly timeoutSeconds?: number;
  /** @manualRouteRegistration: the service supplies the handler through the router options. */
  readonly manual: boolean;
}

/** What an implementation learns about the request beside its decoded arguments. */
export interface RequestContext {
  readonly requestId: string;
  readonly operation: OperationSpec;
  readonly method: string;
  /** The request URL's path as received (the route pattern is operation.path). */
  readonly path: string;
  readonly headers: Headers;
  readonly raw: Request;
  /** Bearer token extracted by hono/bearer-auth; absent when the request carried none. */
  readonly bearerToken?: string;
  /** Aborts when the caller goes away, or when the operation's @timeout elapses. */
  readonly signal: AbortSignal;
  /** The client IP as the Go runtime reports it (X-Forwarded-For, X-Real-IP, then the socket); absent when unknown. */
  readonly clientIp?: string;
  /** Raw path captures by wire name. */
  readonly pathParams: Readonly<Record<string, string>>;
  readonly query: URLSearchParams;
  /** The authenticated caller, null on public and unauthenticated routes. */
  principal: Principal | null;
}
