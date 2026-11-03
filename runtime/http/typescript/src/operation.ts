import type { Principal } from './auth.js';
import type { ParamSpec } from './params.js';
import type { ServiceCaller } from './serviceauth.js';

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

/**
 * @requireService or @allowService (D37): which services may call. The
 * service step applies it before the end-user step; see authorizeService.
 */
export interface OperationServiceCallers {
  /**
   * require: only a listed service, then the user clause when there is one.
   * allow: a listed service stands in for the end user and skips the user
   * clause; any other request goes through the user clause.
   */
  readonly mode: 'require' | 'allow';
  /** The API services whose servers are listed; empty lists every caller the server's service config knows. */
  readonly from: readonly string[];
}

/**
 * A copy of value, a JSON object, with only the keys declared lists, which
 * a router hands an input's parser when the input accepts the keys its type
 * does not declare (D87): a @webhook's. Any other value is value itself,
 * which the parser then refuses.
 */
export function declaredKeysOnly(value: unknown, declared: readonly string[]): unknown {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return value;
  const object = value as Record<string, unknown>;
  const kept: Record<string, unknown> = {};
  for (const key of declared) {
    if (Object.prototype.hasOwnProperty.call(object, key)) kept[key] = object[key];
  }
  return kept;
}

export interface OperationInput {
  /** The generated strict parser of the input type (parse<Input>FromJSON). */
  readonly parse: (value: unknown) => unknown;
  readonly required: boolean;
  /**
   * The input type's wire-to-runtime conversion alone (parse<Input>FromJSON),
   * for an input with file parts (D81): the router validates the JSON
   * fields with parse, sets aside the errors at the file fields, which the
   * JSON never holds, and converts with this.
   */
  readonly fromJSON?: (value: unknown) => unknown;
}

/**
 * A file part of a multipart input (D81): a FileUpload field of the input
 * type, or one of a file-upload scalar, sent as a part named as the field.
 */
export interface OperationUpload {
  /** The part's name, the input type's field. */
  readonly name: string;
  readonly required: boolean;
  /** The largest file the part may hold, in bytes. */
  readonly maxBytes: number;
  /** The media types the part's Content-Type may name, without parameters; empty admits any. */
  readonly contentTypes: readonly string[];
  /** The extensions the part's filename may end in, with the dot, matched without case; empty admits any. */
  readonly extensions: readonly string[];
}

/** A FileDownload result (D81): the file the route answers in place of the envelope. */
export interface OperationDownload {
  /** The Content-Type the route answers with. */
  readonly contentType: string;
  /** The filename Content-Disposition offers when the implementation names none. */
  readonly filename?: string;
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
  /**
   * The input's file parts (D81): the body is multipart/form-data, its
   * `data` part the input's other fields as JSON, or JSON alone with no
   * file. Absent for an operation whose input holds no file.
   */
  readonly uploads?: readonly OperationUpload[];
  /** The result is a file (D81): the adapter answers its bytes, not the envelope. */
  readonly download?: OperationDownload;
  /** The result is a list of lists (T[][]): the adapter sends a nullish outer or inner list as []. */
  readonly outputIsArrayOfArrays?: boolean;
  readonly auth: OperationAuth;
  /** @requireService or @allowService; absent when the operation has neither. */
  readonly service?: OperationServiceCallers;
  /** @bodyLimit in bytes; the adapter's default applies when absent. */
  readonly bodyLimitBytes?: number;
  /** @rateLimit: requests per minute per client (the Go runtime's per-route, per-IP limiter); 429 past it. */
  readonly rateLimitPerMinute?: number;
  /** @timeout: seconds the operation may take before the adapter answers 504 (the Go runtime's Timeout middleware). */
  readonly timeoutSeconds?: number;
  /** @hmacVerified: the provider whose webhook verifier checks the request before every other step. */
  readonly webhookProvider?: string;
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
  /** Bearer token extracted by hono/bearer-auth; absent when the request carried none. The adapter sets it before the permission gate. */
  bearerToken?: string;
  /** Aborts when the caller goes away, or when the operation's @timeout elapses. */
  readonly signal: AbortSignal;
  /** The transport's peer address; absent when the runtime reports none. The default @rateLimit key. */
  readonly remoteAddress?: string;
  /**
   * The client IP as a proxy reports it: X-Forwarded-For's first hop, X-Real-IP, then the peer address; absent when
   * unknown. The headers are the client's to write unless a proxy in front of the service sets them.
   */
  readonly clientIp?: string;
  /** Path captures by wire name, each percent-decoded once. */
  readonly pathParams: Readonly<Record<string, string>>;
  readonly query: URLSearchParams;
  /** The authenticated caller, null on public and unauthenticated routes. */
  principal: Principal | null;
  /**
   * The calling service, verified from Service-Authorization on any route
   * (D37); null when the request carried no service credential or the
   * router has no service authenticator.
   */
  serviceCaller: ServiceCaller | null;
}
