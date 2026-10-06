import type { Context, Env, Hono, MiddlewareHandler } from 'hono';
import { bearerAuth } from 'hono/bearer-auth';
import { bodyLimit } from 'hono/body-limit';
import { HTTPException } from 'hono/http-exception';
import { authorize, type Authenticator, type PermissionMatcher } from './auth.js';
import { OperationResult, envelopeResponse, requestIdOf } from './envelope.js';
import type { OperationSpec, RequestContext } from './operation.js';
import { decodeJsonParam, decodeParams } from './params.js';
import {
  HttpProblem,
  badRequest,
  gatewayTimeout,
  internal,
  notFound,
  notImplemented,
  payloadTooLarge,
  problemResponse,
  serviceUnauthorized,
  tooManyRequests,
  unauthorized,
} from './problem.js';
import { MemoryRateLimitStore, clientIpOf, remoteAddressKey, type RateLimitOptions, type RateLimitStore } from './ratelimit.js';
import { authorizeService, type ServiceAuthenticator } from './serviceauth.js';

/*
The Hono binding of the http runtime. A generated router calls
mountOperation once per @rest operation with the operation's spec and a
handler that forwards decoded arguments to the service's implementation; the
adapter owns the request pipeline, in the Go router's order (D36):

  request id -> @hmacVerified -> @rateLimit -> hono/body-limit ->
  service step -> hono/bearer-auth + permission gate -> [@timeout:
  path/query decoding -> JSON parse -> strict input parser ->
  implementation] -> envelope

and turns every failure into the problem envelope. The cheap refusals come
first: a request without a valid signature costs nothing else, one over the
rate limit is not read, and one over the body cap is not authenticated.
hono/body-limit answers 413 to a declared Content-Length over the cap, and
reads a body without one up front, answering 413 once it passes the cap.
Operations marked @manualRouteRegistration are mounted through
mountManualOperation: the webhook verifier, rate limit, body limit (when
declared), service step, gate and timeout still run, then the service's own
handler receives the Hono context (a streaming response cannot be expressed
as a JSON result).

The service step (D37) runs before the end-user step: with a service
authenticator it verifies Service-Authorization on every route and puts the
caller on ctx.serviceCaller, then applies the operation's @requireService
or @allowService clause. A listed caller on an @allowService route stands
in for the end user, so the route skips hono/bearer-auth and the permission
gate.

@rateLimit stays in this package (Go uses httprate); the body cap is
hono/body-limit, and Bearer extraction hono/bearer-auth. The timeout covers
the decoding and the implementation, as the Go router's covers its handler:
it answers 504 when they have not produced a response in time, and aborts
ctx.signal so an implementation that honours it stops early.

Streaming, multipart and file uploads are not modelled by this adapter; an
operation that needs them is a manual route.
*/

/** Default JSON body cap for operations without @bodyLimit: 1 MiB. The Go router has no default; only @bodyLimit caps its routes. */
export const DEFAULT_BODY_LIMIT_BYTES = 1024 * 1024;

export interface RouterRuntimeOptions {
  /** Establishes the caller on routes that require one. Absent means every such route answers 401. */
  authenticate?: Authenticator;
  /**
   * Establishes the calling service from Service-Authorization, on every
   * route, before the end-user step (D37); serviceAuthenticator is the
   * standard one. Absent means a route with a service clause answers 401
   * service_unauthorized, and other routes ignore the header.
   */
  authenticateService?: ServiceAuthenticator;
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
   * the transport's peer address (remoteAddressKey).
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
  /** Checks the request before every other step. Required when the spec names a webhookProvider. */
  webhookVerifier?: WebhookVerifier;
}

/**
 * Checks a webhook's signature (@hmacVerified) before every other step of
 * its route. It is Hono middleware: it answers a request it refuses, or
 * throws an HttpProblem, and calls next() for one it accepts. It may read
 * the body (c.req.text(), c.req.arrayBuffer()); the route reads a copy taken
 * before it ran. It runs before the body limit, so it reads a body of any
 * size.
 */
export type WebhookVerifier = MiddlewareHandler;

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
 * implementation sees one signal for "stop now" whatever the reason. `raw`
 * is the Hono request when it is read: the route builds the context before
 * the body limit, and the webhook verifier and hono/body-limit each hand
 * the route a fresh copy of the request.
 */
export function requestContextOf<E extends Env>(c: Context<E>, operation: OperationSpec, abort?: AbortSignal): RequestContext {
  const raw = c.req.raw;
  const url = new URL(raw.url);
  const pathParams: Record<string, string> = {};
  for (const [name, value] of Object.entries(c.req.param() as Record<string, string | undefined>)) {
    if (value !== undefined) pathParams[name] = value;
  }
  const bindings = (c.env ?? {}) as NodeSocketBindingsLike;
  const remoteAddress = bindings.incoming?.socket?.remoteAddress?.trim() || undefined;
  const clientIp = clientIpOf(raw.headers, remoteAddress);
  return {
    requestId: requestIdOf(raw),
    operation,
    method: raw.method,
    path: url.pathname,
    headers: raw.headers,
    get raw() {
      return c.req.raw;
    },
    signal: abort ? AbortSignal.any([raw.signal, abort]) : raw.signal,
    ...(remoteAddress !== undefined ? { remoteAddress } : {}),
    ...(clientIp !== undefined ? { clientIp } : {}),
    pathParams,
    query: url.searchParams,
    principal: null,
    serviceCaller: null,
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
  const keyOf = options.rateLimit?.keyOf ?? remoteAddressKey;
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

/**
 * Runs `work` under the route's @timeout: past it, aborts `deadline` (and so
 * ctx.signal) and throws the 504 problem. The work itself cannot be
 * stopped; an implementation that honours ctx.signal stops early.
 */
async function withTimeout(timeoutSeconds: number | undefined, deadline: AbortController, work: () => Promise<Response>): Promise<Response> {
  if (!timeoutSeconds) return work();
  let timer: ReturnType<typeof setTimeout> | undefined;
  const elapsed = new Promise<never>((_resolve, reject) => {
    timer = setTimeout(() => {
      deadline.abort();
      reject(gatewayTimeout());
    }, timeoutSeconds * 1000);
  });
  try {
    return await Promise.race([work(), elapsed]);
  } finally {
    clearTimeout(timer);
  }
}

/** The Bearer scheme, which hono/bearer-auth parses (and answers 400 when malformed). */
const BEARER_SCHEME = /^bearer(?:\s|$)/iu;

/**
 * Reads a Bearer token with hono/bearer-auth into ctx.bearerToken. A request
 * without an Authorization header, or with another scheme (Basic, an API
 * key scheme), goes on to authenticate + authorize untouched, so an
 * Authenticator that does not use Bearer still sees it.
 */
function bearerMiddleware(contextOf: () => RequestContext): MiddlewareHandler {
  const parse = bearerAuth({
    verifyToken: async token => {
      contextOf().bearerToken = token;
      return true;
    },
  });
  return async (c, next) => {
    const header = c.req.header('authorization');
    if (!header || !BEARER_SCHEME.test(header.trim())) return next();
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

/**
 * The service step: establishes ctx.serviceCaller, then applies the route's
 * service clause. Resolves true when the caller stands in for the end user,
 * so the end-user step is skipped.
 */
async function establishServiceCaller(ctx: RequestContext, options: RouterRuntimeOptions): Promise<boolean> {
  const { service } = ctx.operation;
  if (options.authenticateService) ctx.serviceCaller = await options.authenticateService(ctx);
  else if (service) throw serviceUnauthorized('Service credential required');
  return authorizeService(ctx.serviceCaller, service);
}

/** The end-user step. */
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

/**
 * Refuses a request whose path does not decode once the route has captured
 * a path parameter. Hono decodes each capture once, but leaves an escape it
 * cannot decode (bad hex, or bytes that are not UTF-8) as text, so `%ZZ`
 * and `%25ZZ` would otherwise reach the implementation as the same value.
 */
function refuseUndecodablePath(ctx: RequestContext): void {
  if (Object.keys(ctx.pathParams).length === 0) return;
  try {
    decodeURIComponent(ctx.path);
  } catch (error) {
    throw badRequest('The request path is not valid percent-encoding', { cause: error, details: { location: 'path' } });
  }
}

async function decode(ctx: RequestContext, spec: OperationSpec): Promise<DecodedRequest> {
  // ctx.pathParams is already decoded: decoding it again would turn a
  // value such as `100%` into a URIError and `a%2541` into `aA`.
  refuseUndecodablePath(ctx);
  const path = decodeParams('path', spec.pathParams, name => {
    const value = ctx.pathParams[name];
    return value === undefined ? undefined : [value];
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
        throw inputRefusal(error);
      }
    }
  }
  const body: Record<string, unknown> = {};
  if (spec.bodyParams.length > 0) {
    if (raw === undefined && spec.bodyParams.some(param => param.required)) {
      throw badRequest('Request body is required');
    }
    if (raw !== undefined && (typeof raw !== 'object' || raw === null || Array.isArray(raw))) {
      throw badRequest('Request body must be a JSON object');
    }
    const object = (raw ?? {}) as Record<string, unknown>;
    // Every body parameter is decoded from its JSON value, never through
    // the string decoding of path and query values: a list element is not
    // split on commas, and a value of the wrong JSON type is refused.
    for (const param of spec.bodyParams) {
      const value = decodeJsonParam('body', param, object[param.name]);
      if (value !== undefined) body[param.name] = value;
    }
  }
  return { path, query, body, input };
}

/**
 * The 400 of a body the input type refuses, as every generated server
 * answers it: `details` says where (`body`) and why, from the generated
 * parser's ParseError (`expected an object`, `unknown fields: a, b`,
 * `validation failed`), and the top-level `errors` member holds an
 * undeclared key's or a broken rule's field errors, keyed by path, as the
 * SDKs read them.
 */
function inputRefusal(error: unknown): HttpProblem {
  const parsed = (typeof error === 'object' && error !== null ? error : {}) as { reason?: unknown; errors?: unknown };
  const reason = typeof parsed.reason === 'string' ? parsed.reason : 'does not match the declared type';
  const errors = typeof parsed.errors === 'object' && parsed.errors !== null ? parsed.errors : undefined;
  return badRequest('Request body does not match the declared input', {
    cause: error,
    details: { location: 'body', reason },
    ...(errors !== undefined ? { extensions: { errors } } : {}),
  });
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

/**
 * The mount's webhook verifier. An @hmacVerified operation is never mounted
 * without one, so a missing verifier fails at startup, not on a request.
 */
function webhookVerifierOf(spec: OperationSpec, mount: MountOptions): WebhookVerifier | undefined {
  if (spec.webhookProvider !== undefined && !mount.webhookVerifier) {
    throw new Error(`${spec.name} is @hmacVerified({ provider: '${spec.webhookProvider}' }) and was mounted without a webhook verifier`);
  }
  return mount.webhookVerifier;
}

/**
 * Runs the webhook verifier. It may read the body to check the signature,
 * so the rest of the route reads a copy of the request taken before it ran.
 */
function webhookMiddleware(verifier: WebhookVerifier): MiddlewareHandler {
  return async (c, next) => {
    const unread = c.req.raw.clone();
    return verifier(c, async () => {
      c.req.raw = unread;
      await next();
    });
  };
}

/** The steps of one mount, after its overrides; undefined skips a step. */
interface RouteSteps {
  readonly webhookVerifier: WebhookVerifier | undefined;
  readonly rateLimitPerMinute: number | undefined;
  readonly bodyLimitBytes: number | undefined;
  readonly timeoutSeconds: number | undefined;
}

/**
 * Runs one route: the webhook verifier, the rate limit, the body limit, the
 * service step and the permission gate, in the Go router's order, then
 * `work` (decoding and the implementation, or a manual handler) under the
 * timeout. Every refusal is the problem envelope. One RequestContext serves
 * every step, so the callers the steps establish reach the implementation.
 */
async function runRoute<E extends Env>(
  c: Context<E>,
  spec: OperationSpec,
  options: RouterRuntimeOptions,
  steps: RouteSteps,
  work: (ctx: RequestContext) => Promise<Response>
): Promise<Response> {
  const deadline = new AbortController();
  let built: RequestContext | undefined;
  // Built by the first step that needs it, after the verifier has handed
  // the route its copy of the request.
  const contextOf = (): RequestContext => (built ??= requestContextOf(c, spec, steps.timeoutSeconds ? deadline.signal : undefined));
  const refusing =
    (step: (ctx: RequestContext) => Promise<void>): MiddlewareHandler =>
    async (_c, next) => {
      const ctx = contextOf();
      try {
        await step(ctx);
      } catch (error) {
        return failureResponse(error, ctx, options);
      }
      await next();
    };
  const middleware: MiddlewareHandler[] = [];
  if (steps.webhookVerifier) middleware.push(webhookMiddleware(steps.webhookVerifier));
  const { rateLimitPerMinute } = steps;
  if (rateLimitPerMinute) middleware.push(refusing(ctx => admit(ctx, rateLimitPerMinute, options)));
  if (steps.bodyLimitBytes !== undefined) middleware.push(jsonBodyLimit(steps.bodyLimitBytes));
  // A service caller that stands in for the end user skips the end-user step: no Bearer parse, no gate.
  let standsIn = false;
  if (options.authenticateService || spec.service) {
    middleware.push(
      refusing(async ctx => {
        standsIn = await establishServiceCaller(ctx, options);
      })
    );
  }
  if (!spec.auth.public && spec.auth.required) {
    const bearer = bearerMiddleware(contextOf);
    middleware.push(async (current, next) => (standsIn ? next() : bearer(current, next)));
    middleware.push(
      refusing(async ctx => {
        if (!standsIn) await establishCaller(ctx, options);
      })
    );
  }
  try {
    return await through(c, middleware, async () => {
      const ctx = contextOf();
      try {
        return await withTimeout(steps.timeoutSeconds, deadline, () => work(ctx));
      } catch (error) {
        return failureResponse(error, ctx, options);
      }
    });
  } catch (error) {
    deadline.abort();
    return failureResponse(error, contextOf(), options);
  }
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
  const steps: RouteSteps = {
    webhookVerifier: webhookVerifierOf(spec, mount),
    rateLimitPerMinute,
    bodyLimitBytes: hasBody(spec) ? limitBytes : undefined,
    timeoutSeconds,
  };
  app.on(spec.method, honoPath(mount.path ?? spec.path), c =>
    runRoute(c, spec, options, steps, async ctx => successResponse(await handler(ctx, await decode(ctx, spec)), ctx.requestId, spec))
  );
}

/**
 * Mounts a @manualRouteRegistration operation: the webhook verifier, rate
 * limit, body limit (when the spec or the mount declares one), service step
 * and auth gate run, then the service's handler owns the request and
 * response, under the timeout. A timeout covers the handler's return of a
 * Response; a streaming body it has started is not cut. Without a handler
 * the route answers 501 so a forgotten hook is visible, not a 404.
 */
export function mountManualOperation<E extends Env>(
  app: Hono<E>,
  spec: OperationSpec,
  handler: ManualRouteHandler<E> | undefined,
  options: RouterRuntimeOptions = {},
  mount: MountOptions = {}
): void {
  const { rateLimitPerMinute, timeoutSeconds } = directivesOf(spec, mount);
  const steps: RouteSteps = {
    webhookVerifier: webhookVerifierOf(spec, mount),
    rateLimitPerMinute,
    bodyLimitBytes: mount.bodyLimitBytes ?? spec.bodyLimitBytes,
    timeoutSeconds,
  };
  app.on(spec.method, honoPath(mount.path ?? spec.path), c =>
    runRoute(c, spec, options, steps, async ctx => {
      if (!handler) throw notImplemented(`${spec.name} has no manual route handler`);
      refuseUndecodablePath(ctx);
      return handler(c, ctx);
    })
  );
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
