import type { Context, Env, Hono, MiddlewareHandler } from 'hono';
import { bearerAuth } from 'hono/bearer-auth';
import { bodyLimit } from 'hono/body-limit';
import { HTTPException } from 'hono/http-exception';
import { timeout } from 'hono/timeout';
import { authorize, type Authenticator, type PermissionMatcher } from './auth';
import { OperationResult, envelopeResponse, requestIdOf } from './envelope';
import type { OperationSpec, RequestContext } from './operation';
import { decodeJsonParam, decodeParams, type ParamSpec } from './params';
import {
  HttpProblem,
  badRequest,
  gatewayTimeout,
  internal,
  notFound,
  notImplemented,
  payloadTooLarge,
  problemResponse,
  tooManyRequests,
  unauthorized,
} from './problem';
import { MemoryRateLimitStore, clientIpKey, clientIpOf, type RateLimitOptions, type RateLimitStore } from './ratelimit';

/*
The Hono binding of the http runtime. A generated router calls
mountOperation once per @rest operation with the operation's spec and a
handler that forwards decoded arguments to the service's implementation; the
adapter owns the request pipeline:

  request id -> @rateLimit -> [hono/timeout: hono/bearer-auth + permission
  gate -> path/query decoding -> hono/body-limit + JSON parse -> strict
  input parser -> implementation] -> envelope

and turns every failure into the problem envelope. Operations marked
@manualRouteRegistration are mounted through mountManualOperation: the rate
limit, timeout and gate still run, then the service's own handler receives
the Hono context (a streaming response cannot be expressed as a JSON result).

@rateLimit stays in this package (Go uses httprate). @timeout and the body
cap are hono/timeout and hono/body-limit; Bearer extraction is
hono/bearer-auth. The timeout answers 504 when the operation has not
produced a response in time, and AbortSignal.timeout still aborts
ctx.signal so an implementation that honours it stops early.

Streaming, multipart and file uploads are not modelled by this adapter; an
operation that needs them is a manual route.
*/

/** Default JSON body cap for operations without @bodyLimit: 1 MiB, the Go router's convention. */
export const DEFAULT_BODY_LIMIT_BYTES = 1024 * 1024;

const extractedBearer = new WeakMap<Request, string>();

export interface RouterRuntimeOptions {
  /** Establishes the caller on routes that require one. Absent means every such route answers 401. */
  authenticate?: Authenticator;
  /**
   * Decides whether the caller's permissions satisfy an operation's
   * @requirePermission list. Absent means hasAnyPermission: dotted-path
   * coverage with no root permission.
   */
  permissionMatcher?: PermissionMatcher;
  /**
   * Maps a failure that is not an HttpProblem (an upstream SDK error, say) to
   * a problem or a finished response. Anything else, or no mapping, is a 500
   * with code internal_error; the failure itself never reaches the wire.
   */
  onError?: (error: unknown, ctx: RequestContext) => HttpProblem | Response | undefined | Promise<HttpProblem | Response | undefined>;
  /** JSON body cap for operations without @bodyLimit; DEFAULT_BODY_LIMIT_BYTES otherwise. */
  bodyLimitBytes?: number;
  /**
   * How @rateLimit buckets are kept and keyed. Absent means an in-memory
   * store shared by every operation mounted with these options, keyed by
   * client IP.
   */
  rateLimit?: RateLimitOptions;
}

export interface MountOptions {
  /** Mount at this path instead of the spec's (a compatibility alias). */
  path?: string;
  /** JSON body cap for this mount, overriding the spec and the runtime default. */
  bodyLimitBytes?: number;
  /** Requests per minute for this mount, overriding the spec's @rateLimit; 0 disables the limit. */
  rateLimitPerMinute?: number;
  /** Seconds for this mount, overriding the spec's @timeout; 0 disables the timeout. */
  timeoutSeconds?: number;
}

/** The decoded arguments of one request, keyed by wire name. */
export interface DecodedRequest {
  readonly path: Readonly<Record<string, unknown>>;
  readonly query: Readonly<Record<string, unknown>>;
  readonly body: Readonly<Record<string, unknown>>;
  /** The parsed input type, undefined when the operation declares none or the body was optional and absent. */
  readonly input: unknown;
}

export type OperationHandler = (ctx: RequestContext, request: DecodedRequest) => Promise<unknown>;

export type ManualRouteHandler<E extends Env = Env> = (c: Context<E>, ctx: RequestContext) => Response | Promise<Response>;

/** `/api/orders/{id}` -> `/api/orders/:id`. */
export function honoPath(path: string): string {
  return path.replace(/\{([^{}]+)\}/gu, ':$1');
}

/**
 * Parses a JSON body the adapter (or the caller) has already capped.
 * Empty input is `undefined` so the caller decides whether a body was required.
 */
export async function parseJsonBody(request: Request): Promise<unknown> {
  const text = await request.text();
  if (text === '') return undefined;
  try {
    return JSON.parse(text);
  } catch {
    throw badRequest('Request body is not valid JSON');
  }
}

/** hono/body-limit that answers the problem envelope on overflow. */
export function jsonBodyLimit(limitBytes: number): MiddlewareHandler {
  return bodyLimit({
    maxSize: limitBytes,
    onError: c => problemResponse(payloadTooLarge(), requestIdOf(c.req.raw)),
  });
}

interface NodeSocketBindingsLike {
  incoming?: { socket?: { remoteAddress?: string } };
}

/**
 * Builds the RequestContext for a Hono request against one operation. With
 * a timeout the context's signal also aborts when it elapses, so the
 * implementation sees one signal for "stop now" whatever the reason.
 */
export function requestContextOf<E extends Env>(c: Context<E>, operation: OperationSpec, abort?: AbortSignal): RequestContext {
  const raw = c.req.raw;
  const url = new URL(raw.url);
  const pathParams: Record<string, string> = {};
  for (const [name, value] of Object.entries(c.req.param() as Record<string, string | undefined>)) {
    if (value !== undefined) pathParams[name] = value;
  }
  const bindings = (c.env ?? {}) as NodeSocketBindingsLike;
  const clientIp = clientIpOf(raw.headers, bindings.incoming?.socket?.remoteAddress);
  const bearerToken = extractedBearer.get(raw);
  return {
    requestId: requestIdOf(raw),
    operation,
    method: raw.method,
    path: url.pathname,
    headers: raw.headers,
    raw,
    signal: abort ? AbortSignal.any([raw.signal, abort]) : raw.signal,
    ...(clientIp !== undefined ? { clientIp } : {}),
    ...(bearerToken !== undefined ? { bearerToken } : {}),
    pathParams,
    query: url.searchParams,
    principal: null,
  };
}

/** The store every mount without an explicit one shares, keyed by the options object it was mounted with. */
const defaultStores = new WeakMap<RouterRuntimeOptions, RateLimitStore>();

function rateLimitStoreOf(options: RouterRuntimeOptions): RateLimitStore {
  if (options.rateLimit?.store) return options.rateLimit.store;
  let store = defaultStores.get(options);
  if (!store) {
    store = new MemoryRateLimitStore();
    defaultStores.set(options, store);
  }
  return store;
}

/** Takes one token for the request, or throws the 429 problem. */
async function admit(ctx: RequestContext, limitPerMinute: number, options: RouterRuntimeOptions): Promise<void> {
  const keyOf = options.rateLimit?.keyOf ?? clientIpKey;
  const now = options.rateLimit?.now ?? Date.now;
  const key = `${ctx.operation.name}:${keyOf(ctx)}`;
  const decision = await rateLimitStoreOf(options).take(key, limitPerMinute, now());
  if (!decision.allowed) throw tooManyRequests(decision.retryAfterSeconds);
}

/** The effective @rateLimit and @timeout of one mount, after the mount's overrides; 0 or absent disables. */
function directivesOf(spec: OperationSpec, mount: MountOptions): { rateLimitPerMinute?: number; timeoutSeconds?: number } {
  const rateLimitPerMinute = mount.rateLimitPerMinute ?? spec.rateLimitPerMinute;
  const timeoutSeconds = mount.timeoutSeconds ?? spec.timeoutSeconds;
  return {
    ...(rateLimitPerMinute && rateLimitPerMinute > 0 ? { rateLimitPerMinute } : {}),
    ...(timeoutSeconds && timeoutSeconds > 0 ? { timeoutSeconds } : {}),
  };
}

/** Aborts ctx.signal when hono/timeout wins the race; Hono's timer does not abort the handler. */
function deadlineOf(timeoutSeconds: number | undefined): { signal: AbortSignal; abort: () => void; clear: () => void } | undefined {
  if (!timeoutSeconds) return undefined;
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutSeconds * 1000);
  return {
    signal: controller.signal,
    abort: () => controller.abort(),
    clear: () => clearTimeout(timer),
  };
}

function timeoutMiddleware(timeoutSeconds: number): MiddlewareHandler {
  return timeout(timeoutSeconds * 1000, c => {
    return new HTTPException(504, { res: problemResponse(gatewayTimeout(), requestIdOf(c.req.raw)) });
  });
}

/**
 * Extracts a Bearer token with hono/bearer-auth when the header is present.
 * Missing Authorization is left to authenticate + authorize (401), so a
 * custom Authenticator that does not use Bearer still works.
 */
function bearerMiddleware(): MiddlewareHandler {
  const parse = bearerAuth({
    verifyToken: async (token, c) => {
      extractedBearer.set(c.req.raw, token);
      return true;
    },
  });
  return async (c, next) => {
    if (!c.req.header('authorization')) return next();
    return parse(c, next);
  };
}

/** Runs Hono middleware then `work`, so mount keeps the original `app.on(method, path, handler)` types. */
async function through<E extends Env>(c: Context<E>, middleware: MiddlewareHandler[], work: () => Promise<Response>): Promise<Response> {
  let response: Response | undefined;
  const dispatch = async (index: number): Promise<void> => {
    const current = middleware[index];
    if (!current) {
      response = await work();
      return;
    }
    const result = await current(c, () => dispatch(index + 1));
    if (result instanceof Response && response === undefined) {
      response = result;
    }
  };
  await dispatch(0);
  if (!response) throw new Error('route produced no response');
  return response;
}

function hasBody(spec: OperationSpec): boolean {
  return spec.method !== 'GET' && (spec.input !== undefined || spec.bodyParams.length > 0);
}

async function establishCaller(ctx: RequestContext, options: RouterRuntimeOptions): Promise<void> {
  const { auth } = ctx.operation;
  if (auth.public || !auth.required) return;
  ctx.principal = options.authenticate ? await options.authenticate(ctx) : null;
  authorize(ctx.principal, auth, options.permissionMatcher);
}

function problemFromHttpException(error: HTTPException): HttpProblem {
  if (error.status === 413) return payloadTooLarge();
  if (error.status === 401) return unauthorized();
  if (error.status === 504 || error.status === 408) return gatewayTimeout();
  if (error.status === 400) return badRequest(error.message || 'Bad Request');
  return new HttpProblem(error.status, error.message || 'Request failed');
}

function httpExceptionResponse(error: HTTPException, requestId: string): Response {
  const prepared = error.getResponse();
  if (prepared && prepared.headers.get('content-type') === 'application/problem+json') {
    return prepared;
  }
  return problemResponse(problemFromHttpException(error), requestId);
}

async function failureResponse(error: unknown, ctx: RequestContext, options: RouterRuntimeOptions): Promise<Response> {
  if (error instanceof HTTPException) {
    return httpExceptionResponse(error, ctx.requestId);
  }
  if (error instanceof HttpProblem) {
    const response = problemResponse(error, ctx.requestId);
    const retryAfter = (error.details as { retryAfterSeconds?: unknown } | undefined)?.retryAfterSeconds;
    if (error.status === 429 && typeof retryAfter === 'number') response.headers.set('retry-after', String(retryAfter));
    return response;
  }
  if (options.onError) {
    const mapped = await options.onError(error, ctx);
    if (mapped instanceof Response) return mapped;
    if (mapped instanceof HttpProblem) return problemResponse(mapped, ctx.requestId);
  }
  return problemResponse(internal(undefined, { cause: error }), ctx.requestId);
}

async function decode(ctx: RequestContext, spec: OperationSpec): Promise<DecodedRequest> {
  const path = decodeParams('path', spec.pathParams, name => {
    const value = ctx.pathParams[name];
    return value === undefined ? undefined : [decodeURIComponent(value)];
  });
  const query = decodeParams('query', spec.queryParams, name => {
    const values = ctx.query.getAll(name);
    return values.length > 0 ? values : undefined;
  });
  if (!hasBody(spec)) {
    return { path, query, body: {}, input: undefined };
  }
  const raw = await parseJsonBody(ctx.raw);
  let input: unknown;
  if (spec.input) {
    if (raw === undefined) {
      if (spec.input.required) throw badRequest('Request body is required');
    } else {
      try {
        input = spec.input.parse(raw);
      } catch (error) {
        throw badRequest('Request body does not match the declared input', { cause: error });
      }
    }
  }
  let body: Record<string, unknown> = {};
  if (spec.bodyParams.length > 0) {
    if (raw === undefined && spec.bodyParams.some(param => param.required)) {
      throw badRequest('Request body is required');
    }
    if (raw !== undefined && (typeof raw !== 'object' || raw === null || Array.isArray(raw))) {
      throw badRequest('Request body must be a JSON object');
    }
    const object = (raw ?? {}) as Record<string, unknown>;
    // A list of lists and an object-typed parameter (T, T[] or T[][]) are
    // decoded from their JSON value; the other body parameters go through
    // the string decoding that path and query use.
    const fromJson = (param: ParamSpec) => param.isArrayOfArrays === true || param.kind === 'object';
    body = decodeParams('body', spec.bodyParams.filter(param => !fromJson(param)), name => {
      const value = object[name];
      if (value === undefined || value === null) return undefined;
      return Array.isArray(value) ? value.map(item => String(item)) : [String(value)];
    });
    for (const param of spec.bodyParams.filter(fromJson)) {
      const value = decodeJsonParam('body', param, object[param.name]);
      if (value !== undefined) body[param.name] = value;
    }
  }
  return { path, query, body, input };
}

/** A list-of-lists result with every nullish list as []: an inner list is never null on the wire. */
function listOfListsResult(data: unknown): unknown {
  if (data === undefined || data === null) return [];
  return Array.isArray(data) ? data.map((row: unknown) => row ?? []) : data;
}

function successResponse(result: unknown, requestId: string, spec: OperationSpec): Response {
  if (result instanceof Response) return result;
  const shape = spec.outputIsArrayOfArrays ? listOfListsResult : (data: unknown) => data;
  if (result instanceof OperationResult) {
    return envelopeResponse(shape(result.data), requestId, result.status, result.headers);
  }
  return envelopeResponse(shape(result) ?? null, requestId);
}

function routeMiddleware(spec: OperationSpec, timeoutSeconds: number | undefined, bodyLimitBytes: number | undefined): MiddlewareHandler[] {
  const middleware: MiddlewareHandler[] = [];
  if (timeoutSeconds) middleware.push(timeoutMiddleware(timeoutSeconds));
  if (bodyLimitBytes !== undefined) middleware.push(jsonBodyLimit(bodyLimitBytes));
  if (!spec.auth.public && spec.auth.required) middleware.push(bearerMiddleware());
  return middleware;
}

/**
 * Mounts one generated operation: the full pipeline around `handler`, which
 * receives the decoded request and returns the implementation's result (a
 * view, an OperationResult, or a finished Response).
 */
export function mountOperation<E extends Env>(
  app: Hono<E>,
  spec: OperationSpec,
  handler: OperationHandler,
  options: RouterRuntimeOptions = {},
  mount: MountOptions = {}
): void {
  const limitBytes = mount.bodyLimitBytes ?? spec.bodyLimitBytes ?? options.bodyLimitBytes ?? DEFAULT_BODY_LIMIT_BYTES;
  const { rateLimitPerMinute, timeoutSeconds } = directivesOf(spec, mount);
  const middleware = routeMiddleware(spec, timeoutSeconds, hasBody(spec) ? limitBytes : undefined);
  app.on(spec.method, honoPath(mount.path ?? spec.path), async c => {
    const deadline = deadlineOf(timeoutSeconds);
    try {
      const response = await through(c, middleware, async () => {
        const ctx = requestContextOf(c, spec, deadline?.signal);
        try {
          if (rateLimitPerMinute) await admit(ctx, rateLimitPerMinute, options);
          await establishCaller(ctx, options);
          const decoded = await decode(ctx, spec);
          return successResponse(await handler(ctx, decoded), ctx.requestId, spec);
        } catch (error) {
          return failureResponse(error, ctx, options);
        }
      });
      if (deadline?.signal.aborted) {
        return problemResponse(gatewayTimeout(), requestIdOf(c.req.raw));
      }
      return response;
    } catch (error) {
      deadline?.abort();
      return failureResponse(error, requestContextOf(c, spec, deadline?.signal), options);
    } finally {
      deadline?.clear();
    }
  });
}

/**
 * Mounts a @manualRouteRegistration operation: the rate limit, timeout and
 * auth gate run, then the service's handler owns the request and response.
 * A timeout covers the handler's return of a Response; a streaming body it
 * has started is not cut. Without a handler the route answers 501 so a
 * forgotten hook is visible, not a 404.
 */
export function mountManualOperation<E extends Env>(
  app: Hono<E>,
  spec: OperationSpec,
  handler: ManualRouteHandler<E> | undefined,
  options: RouterRuntimeOptions = {},
  mount: MountOptions = {}
): void {
  const limitBytes = mount.bodyLimitBytes ?? spec.bodyLimitBytes;
  const { rateLimitPerMinute, timeoutSeconds } = directivesOf(spec, mount);
  const middleware = routeMiddleware(spec, timeoutSeconds, limitBytes);
  app.on(spec.method, honoPath(mount.path ?? spec.path), async c => {
    const deadline = deadlineOf(timeoutSeconds);
    try {
      const response = await through(c, middleware, async () => {
        const ctx = requestContextOf(c, spec, deadline?.signal);
        try {
          if (rateLimitPerMinute) await admit(ctx, rateLimitPerMinute, options);
          await establishCaller(ctx, options);
          if (!handler) throw notImplemented(`${spec.name} has no manual route handler`);
          return handler(c, ctx);
        } catch (error) {
          return failureResponse(error, ctx, options);
        }
      });
      if (deadline?.signal.aborted) {
        return problemResponse(gatewayTimeout(), requestIdOf(c.req.raw));
      }
      return response;
    } catch (error) {
      deadline?.abort();
      return failureResponse(error, requestContextOf(c, spec, deadline?.signal), options);
    } finally {
      deadline?.clear();
    }
  });
}

/** A notFound handler for the application root: the problem envelope with code not_found. */
export function notFoundHandler<E extends Env>(): (c: Context<E>) => Response {
  return c => problemResponse(notFound(), requestIdOf(c.req.raw));
}

/**
 * An onError handler for the application root, for failures raised outside a
 * generated route (hand-mounted routes, middleware). Same mapping as the
 * generated routes minus the operation context.
 */
export function errorHandler<E extends Env>(
  map?: (error: unknown, requestId: string) => HttpProblem | Response | undefined
): (error: Error, c: Context<E>) => Response {
  return (error, c) => {
    const requestId = requestIdOf(c.req.raw);
    if (error instanceof HTTPException) return httpExceptionResponse(error, requestId);
    if (error instanceof HttpProblem) return problemResponse(error, requestId);
    const mapped = map?.(error, requestId);
    if (mapped instanceof Response) return mapped;
    if (mapped instanceof HttpProblem) return problemResponse(mapped, requestId);
    return problemResponse(internal(undefined, { cause: error }), requestId);
  };
}

interface NodeBindingsLike {
  incoming?: { once(event: 'aborted', listener: () => void): unknown };
  outgoing?: { writableEnded: boolean; once(event: 'close', listener: () => void): unknown };
}

/**
 * An AbortSignal that fires when the client goes away. On @hono/node-server
 * the socket is reachable through the bindings (`incoming`/`outgoing`) and
 * covers what the fetch Request's own signal misses for a streaming
 * response; elsewhere it is the request signal alone.
 */
export function disconnectSignal<E extends Env>(c: Context<E>): AbortSignal {
  const disconnected = new AbortController();
  const bindings = (c.env ?? {}) as NodeBindingsLike;
  const abort = () => {
    if (!bindings.outgoing || !bindings.outgoing.writableEnded) {
      disconnected.abort(new DOMException('Client disconnected', 'AbortError'));
    }
  };
  bindings.incoming?.once('aborted', abort);
  bindings.outgoing?.once('close', abort);
  return AbortSignal.any([c.req.raw.signal, disconnected.signal]);
}
