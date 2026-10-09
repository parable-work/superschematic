import type { Context, Env, Hono, MiddlewareHandler } from 'hono';
import { envelopeResponse, requestIdOf } from '../envelope.js';
import { mountManualOperation, type RouterRuntimeOptions } from '../hono.js';
import type { OperationAuth, OperationSpec, RequestContext } from '../operation.js';
import { HttpProblem, badRequest, internal, problemResponse } from '../problem.js';
import { identityAuthenticator, identityPrincipalOf } from './authenticator.js';
import { corsAnswer, requestHost } from './crossorigin.js';
import { bodyRefusal, crossOrigin } from './errors.js';
import {
  PERMISSION_ROLES_READ,
  PERMISSION_ROLES_WRITE,
  PERMISSION_USERS_READ,
  PERMISSION_USERS_WRITE,
  type ChangePasswordInput,
  type CreateUserInput,
  type IdentityPrincipal,
  type IdentityService,
  type IssuedSession,
  type LoginInput,
  type RegisterInput,
  type RoleInput,
  type SetPasswordInput,
} from './service.js';

/*
The user model's routes as Hono handlers (D50), one per operation of
ir/identity_routes.go, with the contract's JSON field names. A handler is
(c, ctx?): the generated router mounts each through mountManualOperation,
so the rate limit, the body limit, the service step and the auth gate run
first and ctx carries the principal identityAuthenticator established;
without one, the handler authenticates the request itself. A success is
the {data, meta} envelope with 200: logout, changePassword,
setUserPassword and deleteRole, which the contract types as returning a
boolean, answer data true. A refusal is the problem document. A body is JSON of at most 64 KiB, its members exactly the
input's, each a string (permissions a list of strings) or null for its
zero value, as the Go runtime decodes it.

mountIdentityRoutes mounts them on a Hono app as the Go runtime's
Service.Routes lists them, for a server that does not go through the
generated router; mountIdentityOperations mounts the same routes through
the router runtime's pipeline, each with the operation table entry
identityOperationSpec writes from the contract's rules, for a server whose
router does not generate a table (the engine). identityCors is the
credentialed CORS middleware for the config's trusted origins.
*/

/**
 * Who may call a route: anyone (public), any caller with a session
 * (caller), or a caller holding an administration permission, one of the
 * PERMISSION_* names under the service's prefix.
 */
export type IdentityRule =
  | 'public'
  | 'caller'
  | typeof PERMISSION_USERS_READ
  | typeof PERMISSION_USERS_WRITE
  | typeof PERMISSION_ROLES_READ
  | typeof PERMISSION_ROLES_WRITE;

/**
 * One of the user model's routes: its name, its set, its method, its path
 * under the set's path ({id} and {roleId} its parameters), its rule and,
 * for login, register and changePassword, its rate limit.
 */
export interface IdentityOperation {
  readonly name: IdentityOperationName;
  /** @userAdministration's, under its path (auth/admin by default); otherwise @userSessions', under auth by default. */
  readonly administration: boolean;
  readonly method: 'GET' | 'POST' | 'PUT' | 'DELETE';
  readonly path: string;
  readonly rule: IdentityRule;
  /** The requests per minute the route allows per client; absent for a route the contract does not rate-limit. */
  readonly rateLimitPerMinute?: number;
}

export type IdentityOperationName =
  | 'login'
  | 'logout'
  | 'me'
  | 'capabilities'
  | 'changePassword'
  | 'register'
  | 'createUser'
  | 'listUsers'
  | 'getUser'
  | 'disableUser'
  | 'enableUser'
  | 'setUserPassword'
  | 'listRoles'
  | 'createRole'
  | 'updateRole'
  | 'deleteRole'
  | 'grantRole'
  | 'revokeRole';

/** The requests per minute login, register and changePassword allow per client, as ir/identity_routes.go states them. */
export const LOGIN_RATE_LIMIT = 10;
export const REGISTER_RATE_LIMIT = 5;
export const CHANGE_PASSWORD_RATE_LIMIT = 10;

/** Every route of the user model, in ir/identity_routes.go's order, with its rule and rate limit. */
export const identityOperations: readonly IdentityOperation[] = [
  { name: 'login', administration: false, method: 'POST', path: 'login', rule: 'public', rateLimitPerMinute: LOGIN_RATE_LIMIT },
  { name: 'logout', administration: false, method: 'POST', path: 'logout', rule: 'caller' },
  { name: 'me', administration: false, method: 'GET', path: 'me', rule: 'caller' },
  { name: 'capabilities', administration: false, method: 'GET', path: 'capabilities', rule: 'caller' },
  { name: 'changePassword', administration: false, method: 'POST', path: 'password', rule: 'caller', rateLimitPerMinute: CHANGE_PASSWORD_RATE_LIMIT },
  { name: 'register', administration: false, method: 'POST', path: 'register', rule: 'public', rateLimitPerMinute: REGISTER_RATE_LIMIT },
  { name: 'createUser', administration: true, method: 'POST', path: 'users', rule: PERMISSION_USERS_WRITE },
  { name: 'listUsers', administration: true, method: 'GET', path: 'users', rule: PERMISSION_USERS_READ },
  { name: 'getUser', administration: true, method: 'GET', path: 'users/{id}', rule: PERMISSION_USERS_READ },
  { name: 'disableUser', administration: true, method: 'POST', path: 'users/{id}/disable', rule: PERMISSION_USERS_WRITE },
  { name: 'enableUser', administration: true, method: 'POST', path: 'users/{id}/enable', rule: PERMISSION_USERS_WRITE },
  { name: 'setUserPassword', administration: true, method: 'PUT', path: 'users/{id}/password', rule: PERMISSION_USERS_WRITE },
  { name: 'listRoles', administration: true, method: 'GET', path: 'roles', rule: PERMISSION_ROLES_READ },
  { name: 'createRole', administration: true, method: 'POST', path: 'roles', rule: PERMISSION_ROLES_WRITE },
  { name: 'updateRole', administration: true, method: 'PUT', path: 'roles/{id}', rule: PERMISSION_ROLES_WRITE },
  { name: 'deleteRole', administration: true, method: 'DELETE', path: 'roles/{id}', rule: PERMISSION_ROLES_WRITE },
  { name: 'grantRole', administration: true, method: 'PUT', path: 'users/{id}/roles/{roleId}', rule: PERMISSION_ROLES_WRITE },
  { name: 'revokeRole', administration: true, method: 'DELETE', path: 'users/{id}/roles/{roleId}', rule: PERMISSION_ROLES_WRITE },
];

/** The default route prefixes of the two sets. */
export const DEFAULT_USER_SESSIONS_PATH = 'auth';
export const DEFAULT_USER_ADMINISTRATION_PATH = 'auth/admin';

/** A handler of one identity route; ManualRouteHandler's shape, so mountManualOperation mounts it. */
export type IdentityHandler<E extends Env = Env> = (c: Context<E>, ctx?: RequestContext) => Promise<Response>;

/** The cap on an identity route's JSON body. */
const MAX_BODY_BYTES = 64 * 1024;

type FieldKind = 'string' | 'strings';

const LOGIN_FIELDS = { login: 'string', password: 'string', session: 'string' } as const;
const REGISTER_FIELDS = { login: 'string', name: 'string', password: 'string', session: 'string' } as const;
const CHANGE_PASSWORD_FIELDS = { current: 'string', password: 'string' } as const;
const CREATE_USER_FIELDS = { login: 'string', name: 'string', password: 'string' } as const;
const SET_PASSWORD_FIELDS = { password: 'string' } as const;
const ROLE_FIELDS = { name: 'string', permissions: 'strings' } as const;

/** Reads the request's body, at most MAX_BODY_BYTES of it. */
async function readBody(request: Request): Promise<string> {
  if (!request.body) return '';
  const reader = request.body.getReader();
  const chunks: Uint8Array[] = [];
  let size = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    size += value.byteLength;
    if (size > MAX_BODY_BYTES) {
      await reader.cancel();
      throw bodyRefusal('http: request body too large');
    }
    chunks.push(value);
  }
  return new TextDecoder().decode(Buffer.concat(chunks));
}

/**
 * Decodes a body as Go's json decoder fills the input's struct: an object
 * (null fills nothing), no unknown member, a string member a string, a
 * list member a list of strings, null leaving a member's zero value.
 */
async function decodeBody<T>(request: Request, fields: Readonly<Record<string, FieldKind>>): Promise<T> {
  const text = await readBody(request);
  if (text.trim() === '') throw bodyRefusal('EOF');
  let value: unknown;
  try {
    value = JSON.parse(text);
  } catch (error) {
    throw bodyRefusal((error as Error).message);
  }
  const out: Record<string, string | string[]> = {};
  for (const [name, kind] of Object.entries(fields)) out[name] = kind === 'string' ? '' : [];
  if (value === null) return out as T;
  if (typeof value !== 'object' || Array.isArray(value)) throw bodyRefusal('the input is a JSON object');
  for (const [name, member] of Object.entries(value as Record<string, unknown>)) {
    const kind = Object.hasOwn(fields, name) ? fields[name] : undefined;
    if (kind === undefined) throw bodyRefusal(`unknown field ${JSON.stringify(name)}`);
    if (member === null) continue;
    if (kind === 'string') {
      if (typeof member !== 'string') throw bodyRefusal(`${name} is a string`);
      out[name] = member;
      continue;
    }
    if (!Array.isArray(member) || member.some(item => item !== null && typeof item !== 'string')) {
      throw bodyRefusal(`${name} is a list of strings`);
    }
    out[name] = member.map(item => (item === null ? '' : (item as string)));
  }
  return out as T;
}

/** A path parameter, decoded once; 400 when it is empty or the path is not percent-encoded UTF-8. */
function pathParam(c: Context, name: string): string {
  try {
    decodeURIComponent(new URL(c.req.url).pathname);
  } catch (error) {
    throw badRequest(`${name} must be percent-encoded UTF-8`, { cause: error });
  }
  const value = c.req.param(name) ?? '';
  if (value === '') throw badRequest(`${name} is required`);
  return value;
}

/** The answer of an operation the contract types as returning true: the envelope with data true. */
function done(requestId: string, headers: Record<string, string> = {}): Response {
  return envelopeResponse(true, requestId, 200, headers);
}

function failure(error: unknown, requestId: string): Response {
  if (error instanceof HttpProblem) return problemResponse(error, requestId);
  return problemResponse(internal(undefined, { cause: error }), requestId);
}

/**
 * The handler of the operation named op, or undefined for a name the
 * contract does not have.
 */
export function identityHandler<E extends Env = Env>(service: IdentityService, op: string): IdentityHandler<E> | undefined {
  type Work = (c: Context<E>, requestId: string, ctx: RequestContext | undefined) => Promise<Response>;
  const handle =
    (work: Work): IdentityHandler<E> =>
    async (c, ctx) => {
      const requestId = ctx?.requestId ?? requestIdOf(c.req.raw);
      try {
        return await work(c, requestId, ctx);
      } catch (error) {
        return failure(error, requestId);
      }
    };
  // The caller: the principal the router's gate established, or one
  // authenticated now.
  const principal = async (c: Context<E>, ctx: RequestContext | undefined): Promise<IdentityPrincipal> => {
    const established = identityPrincipalOf(ctx?.principal);
    if (established) return established;
    const raw = c.req.raw;
    return service.authenticate({ method: raw.method, host: requestHost(raw), headers: raw.headers });
  };
  const authed = (work: (c: Context<E>, requestId: string, p: IdentityPrincipal) => Promise<Response>): IdentityHandler<E> =>
    handle(async (c, requestId, ctx) => work(c, requestId, await principal(c, ctx)));
  const ok = (data: unknown, requestId: string, headers: Record<string, string> = {}) => envelopeResponse(data, requestId, 200, headers);
  const issued = (session: IssuedSession, requestId: string) =>
    ok(session.result, requestId, session.transport === 'cookie' ? { 'set-cookie': service.sessionCookie(session.token) } : {});
  // A login that asks for a cookie session passes the cross-origin check,
  // since its cookie would sign the browser in.
  const checkCookieLogin = (c: Context<E>, session: string | undefined) => {
    const raw = c.req.raw;
    if (session === 'cookie' && !service.crossOriginAllowed({ method: raw.method, host: requestHost(raw), headers: raw.headers })) {
      throw crossOrigin();
    }
  };

  switch (op) {
    case 'login':
      return handle(async (c, requestId) => {
        const input = await decodeBody<LoginInput>(c.req.raw, LOGIN_FIELDS);
        checkCookieLogin(c, input.session);
        return issued(await service.login(input), requestId);
      });
    case 'register':
      return handle(async (c, requestId) => {
        const input = await decodeBody<RegisterInput>(c.req.raw, REGISTER_FIELDS);
        checkCookieLogin(c, input.session);
        return issued(await service.register(input), requestId);
      });
    case 'logout':
      return authed(async (_c, requestId, p) => {
        await service.logout(p);
        return done(requestId, p.transport === 'cookie' ? { 'set-cookie': service.clearCookie() } : {});
      });
    case 'me':
      return authed(async (_c, requestId, p) => ok(service.me(p), requestId));
    case 'capabilities':
      return authed(async (_c, requestId, p) => ok(await service.capabilities(p), requestId));
    case 'changePassword':
      return authed(async (c, requestId, p) => {
        await service.changePassword(p, await decodeBody<ChangePasswordInput>(c.req.raw, CHANGE_PASSWORD_FIELDS));
        return done(requestId);
      });
    case 'createUser':
      return authed(async (c, requestId, p) => ok(await service.createUser(p, await decodeBody<CreateUserInput>(c.req.raw, CREATE_USER_FIELDS)), requestId));
    case 'listUsers':
      return authed(async (_c, requestId, p) => ok(await service.listUsers(p), requestId));
    case 'getUser':
      return authed(async (c, requestId, p) => ok(await service.getUser(p, pathParam(c, 'id')), requestId));
    case 'disableUser':
      return authed(async (c, requestId, p) => ok(await service.disableUser(p, pathParam(c, 'id')), requestId));
    case 'enableUser':
      return authed(async (c, requestId, p) => ok(await service.enableUser(p, pathParam(c, 'id')), requestId));
    case 'setUserPassword':
      return authed(async (c, requestId, p) => {
        const id = pathParam(c, 'id');
        await service.setUserPassword(p, id, await decodeBody<SetPasswordInput>(c.req.raw, SET_PASSWORD_FIELDS));
        return done(requestId);
      });
    case 'listRoles':
      return authed(async (_c, requestId, p) => ok(await service.listRoles(p), requestId));
    case 'createRole':
      return authed(async (c, requestId, p) => ok(await service.createRole(p, await decodeBody<RoleInput>(c.req.raw, ROLE_FIELDS)), requestId));
    case 'updateRole':
      return authed(async (c, requestId, p) => {
        const id = pathParam(c, 'id');
        return ok(await service.updateRole(p, id, await decodeBody<RoleInput>(c.req.raw, ROLE_FIELDS)), requestId);
      });
    case 'deleteRole':
      return authed(async (c, requestId, p) => {
        await service.deleteRole(p, pathParam(c, 'id'));
        return done(requestId);
      });
    case 'grantRole':
      return authed(async (c, requestId, p) => ok(await service.grantRole(p, pathParam(c, 'id'), pathParam(c, 'roleId')), requestId));
    case 'revokeRole':
      return authed(async (c, requestId, p) => ok(await service.revokeRole(p, pathParam(c, 'id'), pathParam(c, 'roleId')), requestId));
    default:
      return undefined;
  }
}

/** Every operation's handler, by name. */
export function identityHandlers<E extends Env = Env>(service: IdentityService): Record<IdentityOperationName, IdentityHandler<E>> {
  return Object.fromEntries(identityOperations.map(op => [op.name, identityHandler<E>(service, op.name)!])) as Record<
    IdentityOperationName,
    IdentityHandler<E>
  >;
}

/** The sets an API declares: @userSessions' and @userAdministration's options, each absent when the API does not declare it. */
export interface IdentityRoutesOptions {
  /** @userSessions: path (auth by default), noLogin ({ login: false }: me and capabilities alone), register ({ register: true }). */
  readonly sessions?: { readonly path?: string; readonly noLogin?: boolean; readonly register?: boolean };
  /** @userAdministration: path (auth/admin by default). */
  readonly administration?: { readonly path?: string };
}

/** One route to mount: the operation, its method, its full path from "/" with {id} and {roleId}, and its handler. */
export interface IdentityRouteEntry<E extends Env = Env> {
  readonly operation: IdentityOperationName;
  readonly method: IdentityOperation['method'];
  readonly path: string;
  readonly handler: IdentityHandler<E>;
}

/**
 * The routes of the sets an API declares, as the Go runtime's
 * Service.Routes lists them: the session routes without login, logout and
 * changePassword under noLogin, with register only under register, and
 * the administration routes. Paths start with "/"; the caller adds its
 * server's own prefix.
 */
export function identityRoutes<E extends Env = Env>(service: IdentityService, options: IdentityRoutesOptions): IdentityRouteEntry<E>[] {
  const { sessions, administration } = options;
  const out: IdentityRouteEntry<E>[] = [];
  for (const op of identityOperations) {
    let base: string;
    if (op.administration) {
      if (!administration) continue;
      base = administration.path || DEFAULT_USER_ADMINISTRATION_PATH;
    } else {
      if (!sessions) continue;
      if (sessions.noLogin && (op.name === 'login' || op.name === 'logout' || op.name === 'changePassword' || op.name === 'register')) continue;
      if (!sessions.register && op.name === 'register') continue;
      base = sessions.path || DEFAULT_USER_SESSIONS_PATH;
    }
    out.push({ operation: op.name, method: op.method, path: `/${base.replace(/^\/+|\/+$/gu, '')}/${op.path}`, handler: identityHandler<E>(service, op.name)! });
  }
  return out;
}

/** Mounts identityRoutes on app, each path after basePath ("" by default; "/api" under the generated routers' prefix). */
export function mountIdentityRoutes<E extends Env = Env>(
  app: Hono<E>,
  service: IdentityService,
  options: IdentityRoutesOptions & { readonly basePath?: string }
): void {
  const base = (options.basePath ?? '').replace(/\/+$/u, '');
  for (const route of identityRoutes<E>(service, options)) {
    app.on(route.method, `${base}${route.path}`.replace(/\{([^{}]+)\}/gu, ':$1'), c => route.handler(c));
  }
}

/** What identityOperationSpec and mountIdentityOperations put in an entry beside the contract. */
export interface IdentityOperationSpecOptions {
  /** Prefixes each path ("" by default). */
  readonly basePath?: string;
  /** The entries' namespace: identity by default. */
  readonly namespace?: string;
  /** Requests per minute per client on a route the contract does not rate-limit; absent or 0 for none. */
  readonly rateLimitPerMinute?: number;
  /** Seconds a route may take before it answers 504; absent or 0 for none. */
  readonly timeoutSeconds?: number;
}

/** The auth requirement of a rule: none for a public route, a caller, or a caller holding the permission under the service's prefix. */
function authOf(service: IdentityService, rule: IdentityRule): OperationAuth {
  if (rule === 'public') return { public: true, required: false, permissions: [] };
  return { public: false, required: true, permissions: rule === 'caller' ? [] : [service.permission(rule)] };
}

/**
 * The operation table entry of one identity route, for a server whose
 * router does not generate a table (the engine): the route's method and
 * path after basePath, its {id} and {roleId} as path parameters, the
 * contract's rule as its auth requirement, and the contract's rate limit
 * where it has one. It is a manual entry with no input: the handler
 * decodes the body and the path itself.
 */
export function identityOperationSpec(
  service: IdentityService,
  route: Pick<IdentityRouteEntry, 'operation' | 'method' | 'path'>,
  options: IdentityOperationSpecOptions = {}
): OperationSpec {
  const op = identityOperations.find(candidate => candidate.name === route.operation);
  if (!op) throw new Error(`identity: ${JSON.stringify(route.operation)} is not an operation of the user model`);
  const path = `${(options.basePath ?? '').replace(/\/+$/u, '')}${route.path}`;
  const rateLimitPerMinute = op.rateLimitPerMinute ?? options.rateLimitPerMinute;
  return {
    name: op.name,
    namespace: options.namespace ?? 'identity',
    method: route.method,
    path,
    pathParams: [...path.matchAll(/\{([^{}]+)\}/gu)].map(match => ({ name: match[1]!, kind: 'string', required: true })),
    queryParams: [],
    bodyParams: [],
    auth: authOf(service, op.rule),
    ...(rateLimitPerMinute ? { rateLimitPerMinute } : {}),
    ...(options.timeoutSeconds ? { timeoutSeconds: options.timeoutSeconds } : {}),
    manual: true,
  };
}

/**
 * Mounts identityRoutes on app through the router runtime's pipeline, each
 * with its identityOperationSpec, so the request id, the rate limit, the
 * auth gate and the timeout run before the handler, and the gate
 * authenticates with identityAuthenticator(service) whatever runtime
 * names. It returns the entries it mounted.
 */
export function mountIdentityOperations<E extends Env = Env>(
  app: Hono<E>,
  service: IdentityService,
  options: IdentityRoutesOptions & IdentityOperationSpecOptions,
  runtime: RouterRuntimeOptions = {}
): OperationSpec[] {
  // One options object for every mount, so the routes share the runtime's
  // default rate-limit store.
  const router: RouterRuntimeOptions = { ...runtime, authenticate: identityAuthenticator(service) };
  const specs: OperationSpec[] = [];
  for (const route of identityRoutes<E>(service, options)) {
    const spec = identityOperationSpec(service, route, options);
    mountManualOperation(app, spec, route.handler, router);
    specs.push(spec);
  }
  return specs;
}

/**
 * Credentialed CORS for the config's trusted origins, as Hono middleware: a
 * trusted origin's request gets Access-Control-Allow-Origin and
 * Access-Control-Allow-Credentials, its preflight is answered here with
 * 204, and any other request passes through without CORS headers. Every
 * request with an Origin gets Vary: Origin.
 */
export function identityCors<E extends Env = Env>(service: IdentityService | { readonly config: { readonly trustedOrigins: readonly string[] } }): MiddlewareHandler<E> {
  const trusted = [...service.config.trustedOrigins];
  return async (c, next) => {
    const answer = corsAnswer(trusted, c.req.method, c.req.raw.headers);
    if (answer.preflight) {
      const headers = new Headers();
      for (const [name, value] of answer.headers) headers.append(name, value);
      return new Response(null, { status: 204, headers });
    }
    await next();
    for (const [name, value] of answer.headers) {
      if (name === 'vary') c.res.headers.append(name, value);
      else c.res.headers.set(name, value);
    }
  };
}
