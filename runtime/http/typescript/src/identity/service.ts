import { randomBytes } from 'node:crypto';
import { hasAnyPermission, type PermissionMatcher } from '../auth.js';
import { HttpProblem } from '../problem.js';
import { parseIdentityConfig, type Argon2Params, type IdentityConfig } from './config.js';
import { clearCookie, sessionCookie } from './cookie.js';
import { crossOriginAllowed, type CrossOriginRequest } from './crossorigin.js';
import {
  API_KEY_NOT_HELD_MESSAGE,
  FieldErrors,
  SESSION_ONLY_MESSAGE,
  crossOrigin,
  identityConflict,
  identityForbidden,
  identityNotFound,
  invalidCredentials,
  invalidPermissions,
  noApiKeys,
  notCovered,
  notPending,
  pendingApproval,
  unauthenticated,
} from './errors.js';
import { dummyHash, hashPassword, passwordProblem, verifyPassword } from './password.js';
import { capabilitiesOf, effectivePermissions, narrowRoles, uncovered, uncoveredUser, validPermission, type IdentityRoute } from './permissions.js';
import {
  IdentityStoreError,
  isStoreError,
  type IdentityStore,
  type NewUser,
  type StoredApiKey,
  type StoredRole,
  type StoredUser,
} from './store.js';
import { extractCredential, hashToken, isApiKey, newApiKey, newToken, type Transport } from './token.js';

/*
The user model's logic over a store, a config and a clock (D50), as the Go
runtime's identity.Service has it: signing users in and out, resolving a
request's session or API key to a principal, and managing users, roles,
grants and API keys (D114). Its methods throw HttpProblems with the Go
runtime's statuses and codes (400 bad_request with field errors for a
refused input), which the handlers answer as problem documents.
*/

/** The administration routes' permissions, under the naming key identity_permission_prefix: the prefix, a dot, and one of these. */
export const PERMISSION_USERS_READ = 'users.read';
export const PERMISSION_USERS_WRITE = 'users.write';
export const PERMISSION_ROLES_READ = 'roles.read';
export const PERMISSION_ROLES_WRITE = 'roles.write';

/** identity_permission_prefix's default. */
export const DEFAULT_PERMISSION_PREFIX = 'identity';

/** The bounds of createApiKey's input (D114): the most characters a key's name holds, and the most permissions a key names. */
export const API_KEY_NAME_MAX_LENGTH = 100;
export const API_KEY_MAX_PERMISSIONS = 100;

/** An authenticated caller: the user and their roles, and the session or the API key the request carried. */
export interface IdentityPrincipal {
  readonly id: string;
  readonly login: string;
  readonly name: string;
  /** The user's roles, by name. An API key made with permissions holds them narrowed to those (narrowRoles). */
  readonly roles: readonly StoredRole[];
  /** The roles' permissions, without repeats. */
  readonly permissions: readonly string[];
  /** The session's id, and empty for an API key's principal. */
  readonly sessionId: string;
  /** The API key's id (D114), and empty for a session's principal. */
  readonly apiKeyId: string;
  readonly transport: Transport;
}

// The operations' inputs and results, by the contract's field names
// (ir/identity_routes.go).

export interface LoginInput {
  readonly login: string;
  readonly password: string;
  /** bearer when absent. */
  readonly session?: string;
}

export interface RegisterInput {
  readonly login: string;
  /** The login when absent. */
  readonly name?: string;
  readonly password: string;
  readonly session?: string;
}

export interface SessionUser {
  readonly id: string;
  readonly login: string;
  readonly name: string;
}

export interface LoginResult {
  readonly user: SessionUser;
  readonly expiresAt: string;
  /** Absent for a cookie session, which no script reads. */
  readonly token?: string;
}

/** register's answer under approval (D81): pending and no session, or a session as LoginResult has one. */
export interface RegisterResult {
  readonly pending: boolean;
  readonly user: SessionUser;
  readonly expiresAt?: string;
  readonly token?: string;
}

export interface RoleRef {
  readonly id: string;
  readonly name: string;
}

export interface CurrentUser {
  readonly user: SessionUser;
  readonly roles: readonly RoleRef[];
  readonly permissions: readonly string[];
}

export interface Capabilities {
  readonly operations: Readonly<Record<string, boolean>>;
}

export interface ChangePasswordInput {
  readonly current: string;
  readonly password: string;
}

export interface CreateUserInput {
  readonly login: string;
  readonly name?: string;
  readonly password: string;
}

export interface SetPasswordInput {
  readonly password: string;
}

export interface IdentityUser {
  readonly id: string;
  readonly login: string;
  readonly name: string;
  readonly disabled: boolean;
  readonly pending: boolean;
  readonly roles: readonly RoleRef[];
}

export interface RoleInput {
  readonly name: string;
  readonly permissions?: readonly string[];
}

export interface IdentityRole {
  readonly id: string;
  readonly name: string;
  readonly permissions: readonly string[];
}

/** The contract's CreateApiKeyInput (D114). */
export interface CreateApiKeyInput {
  readonly name: string;
  /** How long the key lasts. Absent means the config's apiKeyMaxTtlSeconds, which also bounds it. */
  readonly expiresInSeconds?: number;
  /** The permissions the key names. Absent means every permission its user holds. */
  readonly permissions?: readonly string[];
}

/** The contract's ApiKey (D114): an API key as the lists show one, never its token or its hash. */
export interface IdentityApiKey {
  readonly id: string;
  readonly name: string;
  /** Absent, never null, for a key that holds every permission its user does; a list, an empty one included, otherwise. */
  readonly permissions?: readonly string[];
  readonly createdAt: string;
  readonly expiresAt: string;
  /** Absent until the key's first request. */
  readonly lastUsedAt?: string;
}

/** The contract's CreatedApiKey (D114): createApiKey's answer, the one time the key's token is shown. */
export interface CreatedApiKey {
  readonly id: string;
  readonly name: string;
  /** Absent as IdentityApiKey's is. */
  readonly permissions?: readonly string[];
  readonly createdAt: string;
  readonly expiresAt: string;
  readonly token: string;
}

/** A session login or register created: the result the route answers, and what the HTTP layer sets the cookie from. */
export interface IssuedSession {
  readonly result: LoginResult;
  readonly token: string;
  readonly transport: Transport;
  readonly sessionId: string;
  readonly expiresAt: Date;
}

/** What register did: started a session, or, for a user who waits for approval, none (D81). */
export type Registered =
  | { readonly pending: false; readonly user: SessionUser; readonly session: IssuedSession }
  | { readonly pending: true; readonly user: SessionUser };

/** A Registered as register answers it under approval. */
export function registerResultOf(registered: Registered): RegisterResult {
  if (registered.pending) return { pending: true, user: registered.user };
  const { expiresAt, token } = registered.session.result;
  return { pending: false, user: registered.user, expiresAt, ...(token !== undefined ? { token } : {}) };
}

/**
 * How register admits a user (D81), as the API's @userSessions({
 * registration, defaultRoles, firstUser }) declares it.
 */
export interface RegistrationOptions {
  /** Creates the user pending: register starts no session, and login refuses them with 403 pending_approval until approveUser. */
  readonly approval?: boolean;
  /** The roles each new user is granted, by name, each created with no permissions when no role has the name; a pending user holds them from registration, and they take effect at approval. */
  readonly defaultRoles?: readonly string[];
  /** Makes the registration that finds no role grant the first administrator: active even under approval, and holding the role beside defaultRoles. */
  readonly firstUser?: { readonly role: string; readonly permissions: readonly string[] };
}

/** What the service reads of a request it authenticates: its method, Host and headers. */
export type IdentityRequest = CrossOriginRequest;

export interface IdentityServiceOptions {
  readonly store: IdentityStore;
  /** The identity config's JSON value, or a config parseIdentityConfig returned; absent is every default. */
  readonly config?: unknown;
  /** The clock sessions are created, expired and touched by. */
  readonly now?: () => Date;
  /** Where the random bytes of session tokens and API keys come from (node:crypto's by default), for tests. */
  readonly random?: (size: number) => Uint8Array;
  /** The matcher capabilities and the administration routes admit callers by; hasAnyPermission by default, as the router's. */
  readonly permissionMatcher?: PermissionMatcher;
  /** identity_permission_prefix, the prefix of the administration routes' permissions. */
  readonly permissionPrefix?: string;
  /** The route requirements of every operation of the API, which capabilities answers for (routesOf builds them from an operation table). */
  readonly routes?: readonly IdentityRoute[];
  /** Answers capabilities in place of the routes, for a server whose routes the operation table does not describe (the engine's access policy). */
  readonly capabilities?: (principal: IdentityPrincipal) => Readonly<Record<string, boolean>> | Promise<Readonly<Record<string, boolean>>>;
  /** How register admits users (D81); open registration with no roles when absent. */
  readonly registration?: RegistrationOptions;
  /**
   * Turns on the session set's API key operations (D114), as the API's
   * @userSessions({ apiKeys: true }) declares them: createApiKey,
   * listApiKeys and revokeApiKey, which identityRoutes leaves out without
   * it. A request authenticates with an API key whether they are on or
   * not, whenever the store has the table.
   */
  readonly apiKeys?: boolean;
  /**
   * Turns on the administration set's API key operations (D114), as the
   * API's @userAdministration({ apiKeys: true }) declares them:
   * listUserApiKeys and revokeUserApiKey, which identityRoutes leaves out
   * without it.
   */
  readonly apiKeyAdministration?: boolean;
}

/** Go's RFC 3339 with the fraction's trailing zeros dropped, in UTC: how the Go runtime writes a time. */
export function formatDateTime(at: Date): string {
  return at.toISOString().replace(/\.(\d*?)0*Z$/u, (_m, digits: string) => (digits === '' ? 'Z' : `.${digits}Z`));
}

function sessionUserOf(user: { id: string; login: string; name: string }): SessionUser {
  return { id: user.id, login: user.login, name: user.name };
}

function roleRefs(roles: readonly StoredRole[]): RoleRef[] {
  return roles.map(role => ({ id: role.id, name: role.name }));
}

function identityUserOf(user: StoredUser): IdentityUser {
  return { id: user.id, login: user.login, name: user.name, disabled: user.disabled, pending: user.pending, roles: roleRefs(user.roles) };
}

function identityRoleOf(role: StoredRole): IdentityRole {
  return { id: role.id, name: role.name, permissions: [...role.permissions] };
}

function identityApiKeyOf(key: StoredApiKey): IdentityApiKey {
  return {
    id: key.id,
    name: key.name,
    ...(key.permissions !== null ? { permissions: [...key.permissions] } : {}),
    createdAt: formatDateTime(key.createdAt),
    expiresAt: formatDateTime(key.expiresAt),
    ...(key.lastUsedAt !== null ? { lastUsedAt: formatDateTime(key.lastUsedAt) } : {}),
  };
}

// Unicode's White_Space, which Go's strings.TrimSpace trims a key's name
// of. String.prototype.trim differs at two characters: it keeps U+0085 and
// trims U+FEFF.
const SPACE = '[\\t\\n\\v\\f\\r \\u0085\\u00a0\\u1680\\u2000-\\u200a\\u2028\\u2029\\u202f\\u205f\\u3000]';
const OUTER_SPACE = new RegExp(`^${SPACE}+|${SPACE}+$`, 'gu');

/**
 * Refuses a caller an API key authenticated, with 403 forbidden (D114). A
 * key does not act on its own account: it neither makes, lists nor revokes
 * keys, signs out nor changes the password, so a leaked one does none of
 * them.
 */
function sessionOnly(p: IdentityPrincipal): void {
  if ((p.apiKeyId ?? '') !== '') throw identityForbidden(SESSION_ONLY_MESSAGE);
}

/** A login's transport: bearer when absent. */
function transportOf(session: string | undefined, f: FieldErrors): Transport {
  switch (session ?? '') {
    case '':
    case 'bearer':
      return 'bearer';
    case 'cookie':
      return 'cookie';
    default:
      f.add('session', 'enum', 'session must be "bearer" or "cookie"');
      return 'bearer';
  }
}

function checkPasswordField(f: FieldErrors, field: string, password: string): void {
  if (password === '') {
    f.add(field, 'required', `${field} is required`);
    return;
  }
  const problem = passwordProblem(password);
  if (problem !== undefined) f.add(field, 'length', problem);
}

/** Turns a store's not-found, no-roles, no-API-keys and role-name refusals into problems. */
function storeProblem(error: unknown, what: string): unknown {
  if (isStoreError(error, 'not_found')) return identityNotFound(what, error);
  if (isStoreError(error, 'no_roles')) return identityNotFound('The schema has no roles', error);
  if (isStoreError(error, 'no_api_keys')) return noApiKeys(error);
  if (isStoreError(error, 'role_name_taken')) return identityConflict('The role name is taken');
  return error;
}

/**
 * Refuses a registration whose roles a store could not write: an empty
 * role name, or a first administrator without a role or with no
 * permission or one of the wrong form.
 */
function checkRegistration(registration: RegistrationOptions): RegistrationOptions {
  if ((registration.defaultRoles ?? []).some(role => role === '')) throw new Error('identity: a default role has no name');
  const first = registration.firstUser;
  if (first) {
    if (first.role === '' || first.permissions.length === 0) {
      throw new Error('identity: the first administrator needs a role and at least one permission');
    }
    const bad = first.permissions.find(p => !validPermission(p));
    if (bad !== undefined) {
      throw new Error(`identity: the first administrator's permission ${JSON.stringify(bad)} is not dotted segments of letters, digits, '_' and '-'`);
    }
  }
  return {
    approval: registration.approval === true,
    defaultRoles: [...(registration.defaultRoles ?? [])],
    ...(first ? { firstUser: { role: first.role, permissions: [...first.permissions] } } : {}),
  };
}

/** The identity service. */
export class IdentityService {
  readonly config: IdentityConfig;
  readonly store: IdentityStore;
  readonly permissionPrefix: string;
  private readonly now: () => Date;
  private readonly random: (size: number) => Uint8Array;
  private readonly matcher: PermissionMatcher;
  private readonly routes: readonly IdentityRoute[];
  private readonly capabilitiesOf: IdentityServiceOptions['capabilities'];
  private readonly params: Argon2Params;
  private readonly dummy: Promise<string>;
  /** How register admits users. */
  readonly registration: RegistrationOptions;
  /** Whether the session set's API key operations are turned on (IdentityServiceOptions.apiKeys). */
  readonly apiKeys: boolean;
  /** Whether the administration set's API key operations are turned on (IdentityServiceOptions.apiKeyAdministration). */
  readonly apiKeyAdministration: boolean;

  /** Validates the config and starts hashing the dummy password an unknown login verifies against, at the config's cost. */
  constructor(options: IdentityServiceOptions) {
    if (!options.store) throw new Error('identity: IdentityService needs a store');
    this.store = options.store;
    this.config = parseIdentityConfig(options.config ?? {});
    this.now = options.now ?? (() => new Date());
    this.random = options.random ?? randomBytes;
    this.matcher = options.permissionMatcher ?? hasAnyPermission;
    this.permissionPrefix = options.permissionPrefix ?? DEFAULT_PERMISSION_PREFIX;
    if (!validPermission(this.permissionPrefix)) {
      throw new Error(
        `identity: the permission prefix ${JSON.stringify(this.permissionPrefix)} is not dotted segments of letters, digits, '_' and '-'`
      );
    }
    this.routes = [...(options.routes ?? [])];
    this.capabilitiesOf = options.capabilities;
    this.registration = checkRegistration(options.registration ?? {});
    this.apiKeys = options.apiKeys === true;
    this.apiKeyAdministration = options.apiKeyAdministration === true;
    this.params = this.config.password.argon2;
    this.dummy = dummyHash(this.params);
    // A failure surfaces on the first login that needs the hash.
    this.dummy.catch(() => undefined);
  }

  /** The full name of an administration permission (PERMISSION_USERS_READ, ...) under the service's prefix. */
  permission(name: string): string {
    return `${this.permissionPrefix}.${name}`;
  }

  /** The session cookie's name. */
  get cookieName(): string {
    return this.config.cookie.name;
  }

  /** The Set-Cookie value a cookie login answers for token: the session's whole lifetime. */
  sessionCookie(token: string): string {
    return sessionCookie(this.config, token, this.config.sessionTtlSeconds);
  }

  /** The Set-Cookie value that clears the session cookie. */
  clearCookie(): string {
    return clearCookie(this.config);
  }

  /** Runs the cross-origin check with the config's trusted origins. */
  crossOriginAllowed(request: CrossOriginRequest): boolean {
    return crossOriginAllowed(this.config.trustedOrigins, request);
  }

  private async verifyDummy(password: string): Promise<void> {
    await verifyPassword(await this.dummy, password, this.params);
  }

  /**
   * Signs a user in: verifies the password and creates a session. Every
   * failure, an unknown login, a login the scalar refuses, a user without a
   * password and a disabled user included, is 401 invalid_credentials after
   * a verify of the same cost, so neither the answer nor its timing tells
   * which. A user whose password verifies and who waits for approval is
   * 403 pending_approval (D81). A hash at an older cost is written again
   * at the config's.
   */
  async login(input: LoginInput): Promise<IssuedSession> {
    const f = new FieldErrors();
    const transport = transportOf(input.session, f);
    if (input.login === '') f.add('login', 'required', 'login is required');
    checkPasswordField(f, 'password', input.password);
    f.throwIfAny();

    let rec;
    try {
      rec = await this.store.findLogin(input.login);
    } catch (error) {
      if (isStoreError(error, 'not_found') || isStoreError(error, 'invalid_login')) {
        await this.verifyDummy(input.password);
        throw invalidCredentials(error);
      }
      throw error;
    }
    if (rec.passwordHash === '') {
      await this.verifyDummy(input.password);
      throw invalidCredentials();
    }
    const verified = await verifyPassword(rec.passwordHash, input.password, this.params);
    if (!verified.ok) throw invalidCredentials();
    if (rec.user.disabled) throw invalidCredentials();
    if (rec.user.pending) throw pendingApproval();
    if (verified.rehash) {
      const hash = await hashPassword(input.password, this.params);
      await this.store.rehashPassword(rec.user.id, rec.passwordHash, hash);
    }
    return this.issue(rec.user, transport);
  }

  /**
   * Creates a user with a password, with the registration's default
   * roles, and signs them in, as login does. Under approval the user waits
   * for an administrator and no session starts, unless they are the first
   * administrator (D81). A taken login is 409 conflict.
   */
  async register(input: RegisterInput): Promise<Registered> {
    const f = new FieldErrors();
    const transport = transportOf(input.session, f);
    if (input.login === '') f.add('login', 'required', 'login is required');
    checkPasswordField(f, 'password', input.password);
    f.throwIfAny();
    const { approval, defaultRoles, firstUser } = this.registration;
    const user = await this.createStoredUser(input.login, input.name ?? '', input.password, {
      pending: approval === true,
      roles: defaultRoles ?? [],
      ...(firstUser ? { first: firstUser } : {}),
    });
    if (user.pending) return { pending: true, user: sessionUserOf(user) };
    return { pending: false, user: sessionUserOf(user), session: await this.issue(user, transport) };
  }

  private async createStoredUser(
    login: string,
    name: string,
    password: string,
    registration: Pick<NewUser, 'pending' | 'roles' | 'first'> = {}
  ): Promise<StoredUser> {
    const hash = await hashPassword(password, this.params);
    try {
      return await this.store.createUser({ login, name, passwordHash: hash, at: this.now(), ...registration });
    } catch (error) {
      if (error instanceof IdentityStoreError && (error.kind === 'invalid_login' || error.kind === 'invalid_name')) {
        const f = new FieldErrors();
        const cause = error.cause instanceof Error ? error.cause.message : error.message;
        f.add(error.kind === 'invalid_login' ? 'login' : 'name', 'parse', cause);
        f.throwIfAny();
      }
      if (isStoreError(error, 'login_taken')) throw identityConflict('The login is taken');
      throw storeProblem(error, 'Role');
    }
  }

  /** Creates a session for user. */
  private async issue(user: { id: string; login: string; name: string }, transport: Transport): Promise<IssuedSession> {
    const token = newToken(this.random);
    const now = this.now();
    const expiresAt = new Date(now.getTime() + this.config.sessionTtlSeconds * 1000);
    const session = await this.store.createSession({ userId: user.id, tokenHash: hashToken(token), createdAt: now, expiresAt });
    const result: LoginResult = {
      user: sessionUserOf(user),
      expiresAt: formatDateTime(expiresAt),
      ...(transport === 'bearer' ? { token } : {}),
    };
    return { result, token, transport, sessionId: session.id, expiresAt };
  }

  /**
   * Resolves a request's session or API key to its principal. The
   * credential is the Authorization bearer token, a session's or an API
   * key, or the session cookie without an Authorization header. A cookie
   * request with a method other than GET, HEAD or OPTIONS passes the
   * cross-origin check first (403 cross_origin). Then the session must
   * exist, be neither revoked, expired nor idle, and belong to a user who
   * is not disabled; every other outcome is 401 unauthorized, and a refused
   * cookie's 401 carries the Set-Cookie that clears it. An API key is held
   * to the same rules but the idle one, and its lastUsedAt is written as
   * lastSeenAt is (D114).
   */
  async authenticate(request: IdentityRequest): Promise<IdentityPrincipal> {
    const credential = extractCredential(request.headers, this.cookieName);
    const clear = credential.transport === 'cookie' ? { 'set-cookie': this.clearCookie() } : undefined;
    if (credential.outcome === 'none') throw unauthenticated(new Error('the request carries no session'));
    if (credential.outcome === 'invalid') {
      throw unauthenticated(new Error(`the ${credential.transport} credential is not a token the transport carries`), clear);
    }
    if (credential.transport === 'cookie' && !this.crossOriginAllowed(request)) throw crossOrigin();
    try {
      return await this.authenticateToken(credential.token, credential.transport);
    } catch (error) {
      if (clear && error instanceof HttpProblem && error.status === 401) {
        throw unauthenticated(error, clear);
      }
      throw error;
    }
  }

  /**
   * Resolves a session token to its principal, as authenticate does once it
   * has read the token from a request. A token that is an API key is
   * resolved as one with the bearer transport, the only one that carries it
   * (D114).
   */
  async authenticateToken(token: string, transport: Transport): Promise<IdentityPrincipal> {
    if (transport === 'bearer' && isApiKey(token)) return this.authenticateApiKey(token);
    let rec;
    try {
      rec = await this.store.findSession(hashToken(token));
    } catch (error) {
      if (isStoreError(error, 'not_found')) throw unauthenticated(new Error('no session has the token'));
      throw error;
    }
    const now = this.now().getTime();
    const { session, user } = rec;
    if (session.revokedAt !== null) throw unauthenticated(new Error('the session is revoked'));
    if (now >= session.expiresAt.getTime()) throw unauthenticated(new Error('the session is expired'));
    if (user.disabled) throw unauthenticated(new Error('the user is disabled'));
    if (user.pending) throw unauthenticated(new Error('the user waits for approval'));
    const lastSeen = (session.lastSeenAt ?? session.createdAt).getTime();
    const idle = this.config.idleTimeoutSeconds * 1000;
    if (idle > 0 && now - lastSeen >= idle) throw unauthenticated(new Error('the session is idle'));
    const interval = this.config.touchIntervalSeconds * 1000;
    if (session.lastSeenAt === null || now - session.lastSeenAt.getTime() >= interval) {
      await this.store.touchSession(session.id, new Date(now), new Date(now - interval));
    }
    const roles = await this.store.userRoles(user.id);
    return {
      id: user.id,
      login: user.login,
      name: user.name,
      roles,
      permissions: effectivePermissions(roles),
      sessionId: session.id,
      apiKeyId: '',
      transport,
    };
  }

  /**
   * Resolves an API key to its principal (D114). In this order, the key
   * must exist, be neither revoked nor expired, and belong to a user who is
   * neither disabled nor pending; every other outcome is the 401
   * unauthorized a session's is. No idle timeout ends a key. Its lastUsedAt
   * is written when it is null or older than the touch interval, as a
   * session's lastSeenAt is. A key made with permissions holds its user's
   * roles narrowed to them (narrowRoles); one made with none holds them
   * whole, as a session does.
   */
  private async authenticateApiKey(token: string): Promise<IdentityPrincipal> {
    let rec;
    try {
      rec = await this.store.findApiKey(hashToken(token));
    } catch (error) {
      if (isStoreError(error, 'not_found')) throw unauthenticated(new Error('no API key has the token'));
      throw error;
    }
    const now = this.now().getTime();
    const { apiKey: key, user } = rec;
    if (key.revokedAt !== null) throw unauthenticated(new Error('the API key is revoked'));
    if (now >= key.expiresAt.getTime()) throw unauthenticated(new Error('the API key is expired'));
    if (user.disabled) throw unauthenticated(new Error('the user is disabled'));
    if (user.pending) throw unauthenticated(new Error('the user waits for approval'));
    const interval = this.config.touchIntervalSeconds * 1000;
    if (key.lastUsedAt === null || now - key.lastUsedAt.getTime() >= interval) {
      await this.store.touchApiKey(key.id, new Date(now), new Date(now - interval));
    }
    const held = await this.store.userRoles(user.id);
    const roles = key.permissions === null ? held : narrowRoles(held, key.permissions);
    return {
      id: user.id,
      login: user.login,
      name: user.name,
      roles,
      permissions: effectivePermissions(roles),
      sessionId: '',
      apiKeyId: key.id,
      transport: 'bearer',
    };
  }

  /** Revokes the caller's session. An API key's principal has none to revoke and is 403 forbidden. */
  async logout(p: IdentityPrincipal): Promise<void> {
    sessionOnly(p);
    if (p.sessionId === '') throw unauthenticated(new Error('the caller has no session'));
    await this.store.revokeSession(p.sessionId, this.now());
  }

  /** The caller's user, roles and permissions. */
  me(p: IdentityPrincipal): CurrentUser {
    return { user: sessionUserOf(p), roles: roleRefs(p.roles), permissions: [...p.permissions] };
  }

  /** For each operation of the API an end user may call, whether its route admits the caller. */
  async capabilities(p: IdentityPrincipal): Promise<Capabilities> {
    if (this.capabilitiesOf) return { operations: await this.capabilitiesOf(p) };
    return { operations: capabilitiesOf(this.routes, p.permissions, this.matcher) };
  }

  /**
   * Sets the caller's password once current verifies (401
   * invalid_credentials otherwise), and revokes the user's other sessions.
   * The user's API keys live on. An API key's principal is 403 forbidden.
   */
  async changePassword(p: IdentityPrincipal, input: ChangePasswordInput): Promise<void> {
    sessionOnly(p);
    const f = new FieldErrors();
    checkPasswordField(f, 'current', input.current);
    checkPasswordField(f, 'password', input.password);
    f.throwIfAny();
    let rec;
    try {
      rec = await this.store.findCredential(p.id);
    } catch (error) {
      if (isStoreError(error, 'not_found')) throw unauthenticated(error);
      throw error;
    }
    const stored = rec.passwordHash === '' ? await this.dummy : rec.passwordHash;
    const verified = await verifyPassword(stored, input.current, this.params);
    if (!verified.ok || rec.passwordHash === '') throw invalidCredentials();
    const hash = await hashPassword(input.password, this.params);
    await this.store.setPassword(p.id, hash, this.now(), p.sessionId);
  }

  /** Refuses a caller the matcher does not give the administration permission name. */
  private require(p: IdentityPrincipal, name: string): void {
    if (p.id === '') throw unauthenticated(new Error('the caller is not authenticated'));
    if (!this.matcher(p.permissions, [this.permission(name)])) throw identityForbidden('Insufficient permissions');
  }

  /** Creates a user with a password (users.write). */
  async createUser(p: IdentityPrincipal, input: CreateUserInput): Promise<IdentityUser> {
    this.require(p, PERMISSION_USERS_WRITE);
    const f = new FieldErrors();
    if (input.login === '') f.add('login', 'required', 'login is required');
    checkPasswordField(f, 'password', input.password);
    f.throwIfAny();
    return identityUserOf(await this.createStoredUser(input.login, input.name ?? '', input.password));
  }

  /**
   * Lists the users, by login (users.read): every one when pending is
   * absent, else those who wait for approval when it is true and the
   * others when it is false (D81).
   */
  async listUsers(p: IdentityPrincipal, pending?: boolean): Promise<IdentityUser[]> {
    this.require(p, PERMISSION_USERS_READ);
    const users = await this.store.listUsers();
    return users.filter(user => pending === undefined || user.pending === pending).map(identityUserOf);
  }

  /** One user (users.read). */
  async getUser(p: IdentityPrincipal, id: string): Promise<IdentityUser> {
    this.require(p, PERMISSION_USERS_READ);
    return this.storedUser(id);
  }

  private async storedUser(id: string): Promise<IdentityUser> {
    try {
      return identityUserOf(await this.store.getUser(id));
    } catch (error) {
      throw storeProblem(error, 'User');
    }
  }

  /** Disables a user and revokes their sessions and their API keys (users.write). The caller's permissions must cover each of the user's. */
  disableUser(p: IdentityPrincipal, id: string): Promise<IdentityUser> {
    return this.setDisabled(p, id, true);
  }

  /**
   * Enables a disabled user (users.write), which brings back neither a
   * session nor an API key the disable revoked. The caller's permissions
   * must cover each of the user's.
   */
  enableUser(p: IdentityPrincipal, id: string): Promise<IdentityUser> {
    return this.setDisabled(p, id, false);
  }

  private async setDisabled(p: IdentityPrincipal, id: string, disabled: boolean): Promise<IdentityUser> {
    this.require(p, PERMISSION_USERS_WRITE);
    await this.covers(p, id);
    try {
      await this.store.setDisabled(id, disabled, this.now());
    } catch (error) {
      throw storeProblem(error, 'User');
    }
    return this.storedUser(id);
  }

  /**
   * Approves a user who waits for approval, who can then sign in
   * (users.write, D81). The caller's permissions must cover each of the
   * user's, the roles register granted them included; a user who is not
   * pending is 409 not_pending.
   */
  async approveUser(p: IdentityPrincipal, id: string): Promise<IdentityUser> {
    this.require(p, PERMISSION_USERS_WRITE);
    await this.covers(p, id);
    try {
      await this.store.approveUser(id);
    } catch (error) {
      if (isStoreError(error, 'not_pending')) throw notPending();
      throw storeProblem(error, 'User');
    }
    return this.storedUser(id);
  }

  /**
   * Sets a user's password and revokes their sessions (users.write). The
   * user's API keys live on. The caller's permissions must cover each of
   * the user's.
   */
  async setUserPassword(p: IdentityPrincipal, id: string, input: SetPasswordInput): Promise<void> {
    this.require(p, PERMISSION_USERS_WRITE);
    const f = new FieldErrors();
    checkPasswordField(f, 'password', input.password);
    f.throwIfAny();
    await this.covers(p, id);
    const hash = await hashPassword(input.password, this.params);
    try {
      await this.store.setPassword(id, hash, this.now(), '');
    } catch (error) {
      throw storeProblem(error, 'User');
    }
  }

  /**
   * Refuses a write to the user with id unless the caller's permissions
   * cover every permission the user holds through their roles
   * (uncoveredUser): 404 for no such user, 403 listing the ones not
   * covered. Whoever sets a user's password signs in as them, and whoever
   * enables or disables them gives or takes their permissions, so no one
   * does either to a user who holds what they do not.
   */
  private async covers(p: IdentityPrincipal, id: string): Promise<void> {
    let user: StoredUser;
    try {
      user = await this.store.getUser(id);
    } catch (error) {
      throw storeProblem(error, 'User');
    }
    const missing = uncoveredUser(p.permissions, user.roles);
    if (missing.length > 0) throw notCovered(missing);
  }

  /** Lists every role, by name (roles.read). */
  async listRoles(p: IdentityPrincipal): Promise<IdentityRole[]> {
    this.require(p, PERMISSION_ROLES_READ);
    try {
      return (await this.store.listRoles()).map(identityRoleOf);
    } catch (error) {
      throw storeProblem(error, 'Role');
    }
  }

  /**
   * Validates a role's input and returns its permissions without repeats: a
   * name is required (400), every permission has a permission's form (422),
   * and the caller covers each (403: no one grants what they do not hold).
   */
  private checkRole(p: IdentityPrincipal, input: RoleInput): string[] {
    const f = new FieldErrors();
    if (input.name === '') f.add('name', 'required', 'name is required');
    f.throwIfAny();
    const invalid: string[] = [];
    const permissions: string[] = [];
    const seen = new Set<string>();
    for (const permission of input.permissions ?? []) {
      if (!validPermission(permission)) {
        invalid.push(permission);
        continue;
      }
      if (!seen.has(permission)) {
        seen.add(permission);
        permissions.push(permission);
      }
    }
    if (invalid.length > 0) throw invalidPermissions(invalid);
    const missing = uncovered(p.permissions, permissions);
    if (missing.length > 0) throw identityForbidden('No one grants a permission they do not hold', { permissions: missing });
    return permissions;
  }

  /** Creates a role (roles.write). The caller's permissions must cover each of the role's. */
  async createRole(p: IdentityPrincipal, input: RoleInput): Promise<IdentityRole> {
    this.require(p, PERMISSION_ROLES_WRITE);
    const permissions = this.checkRole(p, input);
    try {
      return identityRoleOf(await this.store.createRole(input.name, permissions));
    } catch (error) {
      throw storeProblem(error, 'Role');
    }
  }

  /** Rewrites a role's name and permissions (roles.write). The caller's permissions must cover each of the new ones. */
  async updateRole(p: IdentityPrincipal, id: string, input: RoleInput): Promise<IdentityRole> {
    this.require(p, PERMISSION_ROLES_WRITE);
    const permissions = this.checkRole(p, input);
    try {
      return identityRoleOf(await this.store.updateRole(id, input.name, permissions));
    } catch (error) {
      throw storeProblem(error, 'Role');
    }
  }

  /** Deletes a role and its grants (roles.write). */
  async deleteRole(p: IdentityPrincipal, id: string): Promise<void> {
    this.require(p, PERMISSION_ROLES_WRITE);
    try {
      await this.store.deleteRole(id);
    } catch (error) {
      throw storeProblem(error, 'Role');
    }
  }

  /** Grants a user a role (roles.write). The caller's permissions must cover each of the role's. */
  async grantRole(p: IdentityPrincipal, userId: string, roleId: string): Promise<IdentityUser> {
    this.require(p, PERMISSION_ROLES_WRITE);
    let role: StoredRole;
    try {
      role = await this.store.getRole(roleId);
    } catch (error) {
      throw storeProblem(error, 'Role');
    }
    const missing = uncovered(p.permissions, role.permissions);
    if (missing.length > 0) throw identityForbidden('No one grants a permission they do not hold', { permissions: missing });
    try {
      await this.store.grantRole(userId, roleId, this.now());
    } catch (error) {
      throw storeProblem(error, 'User or role');
    }
    return this.storedUser(userId);
  }

  /** Revokes a role from a user (roles.write). */
  async revokeRole(p: IdentityPrincipal, userId: string, roleId: string): Promise<IdentityUser> {
    this.require(p, PERMISSION_ROLES_WRITE);
    try {
      await this.store.revokeRole(userId, roleId);
    } catch (error) {
      throw storeProblem(error, 'User or role');
    }
    return this.storedUser(userId);
  }

  /**
   * Admits the caller of an API key operation of the session set (D114): an
   * authenticated one (401) signed in with a session, not an API key (403),
   * over a store that has API keys (404).
   */
  private ownApiKeys(p: IdentityPrincipal): void {
    if (p.id === '') throw unauthenticated(new Error('the caller is not authenticated'));
    sessionOnly(p);
    this.haveApiKeys();
  }

  /** Throws the 404 of an API key operation over a store whose descriptor names no API key table. */
  private haveApiKeys(): void {
    if (!this.store.hasApiKeys()) throw noApiKeys();
  }

  /**
   * Makes the caller an API key (D114) and answers its token, this once.
   * The caller is signed in with a session. The name, trimmed of
   * surrounding white space, is 1 to API_KEY_NAME_MAX_LENGTH characters;
   * expiresInSeconds, when present, is 1 to the config's
   * apiKeyMaxTtlSeconds, which is also the lifetime without it;
   * permissions, when present, are 1 to API_KEY_MAX_PERMISSIONS of them
   * (400). Each has a permission's form and is named once (422), and the
   * caller holds a permission covering it (403: a key names only what its
   * user holds).
   */
  async createApiKey(p: IdentityPrincipal, input: CreateApiKeyInput): Promise<CreatedApiKey> {
    this.ownApiKeys(p);
    const f = new FieldErrors();
    const name = input.name.replace(OUTER_SPACE, '');
    // Characters are code points, as Go counts a string's runes.
    const length = [...name].length;
    if (length === 0) f.add('name', 'required', 'name is required');
    else if (length > API_KEY_NAME_MAX_LENGTH) f.add('name', 'maxLength', `name must be at most ${API_KEY_NAME_MAX_LENGTH} characters`);
    let seconds = this.config.apiKeyMaxTtlSeconds;
    const given = input.expiresInSeconds ?? undefined;
    if (given !== undefined) {
      if (!Number.isInteger(given)) f.add('expiresInSeconds', 'integer', 'expiresInSeconds must be an integer');
      else if (given < 1) f.add('expiresInSeconds', 'min', 'expiresInSeconds must be at least 1');
      else if (given > seconds) f.add('expiresInSeconds', 'max', `expiresInSeconds must be at most ${seconds}`);
      else seconds = given;
    }
    const named = input.permissions ?? null;
    if (named !== null) {
      if (named.length === 0) f.add('permissions', 'listMin', 'permissions must contain at least 1 items');
      else if (named.length > API_KEY_MAX_PERMISSIONS) {
        f.add('permissions', 'listMax', `permissions must contain at most ${API_KEY_MAX_PERMISSIONS} items`);
      }
    }
    f.throwIfAny();
    // A permission that has not a permission's form, and each one that
    // repeats an earlier one, in the order given.
    const refused: string[] = [];
    const seen = new Set<string>();
    for (const permission of named ?? []) {
      if (!validPermission(permission) || seen.has(permission)) refused.push(permission);
      seen.add(permission);
    }
    if (refused.length > 0) throw invalidPermissions(refused);
    const missing = uncovered(p.permissions, named ?? []);
    if (missing.length > 0) throw identityForbidden(API_KEY_NOT_HELD_MESSAGE, { permissions: missing });
    const token = newApiKey(this.random);
    const now = this.now();
    let key: StoredApiKey;
    try {
      key = await this.store.createApiKey({
        userId: p.id,
        name,
        tokenHash: hashToken(token),
        permissions: named === null ? null : [...named],
        createdAt: now,
        expiresAt: new Date(now.getTime() + seconds * 1000),
      });
    } catch (error) {
      if (isStoreError(error, 'not_found')) throw unauthenticated(error);
      throw storeProblem(error, 'User');
    }
    return {
      id: key.id,
      name: key.name,
      ...(key.permissions !== null ? { permissions: [...key.permissions] } : {}),
      createdAt: formatDateTime(key.createdAt),
      expiresAt: formatDateTime(key.expiresAt),
      token,
    };
  }

  /**
   * Lists the caller's API keys that are not revoked, expired ones
   * included, newest first (D114). The caller is signed in with a session.
   */
  async listApiKeys(p: IdentityPrincipal): Promise<IdentityApiKey[]> {
    this.ownApiKeys(p);
    return this.storedApiKeys(p.id);
  }

  private async storedApiKeys(userId: string): Promise<IdentityApiKey[]> {
    try {
      return (await this.store.listApiKeys(userId)).map(identityApiKeyOf);
    } catch (error) {
      throw storeProblem(error, 'User');
    }
  }

  /**
   * Revokes the caller's API key with keyId (D114). A key that does not
   * exist, is another user's or is revoked already is 404. The caller is
   * signed in with a session.
   */
  async revokeApiKey(p: IdentityPrincipal, keyId: string): Promise<void> {
    this.ownApiKeys(p);
    try {
      await this.store.revokeApiKey(p.id, keyId, this.now());
    } catch (error) {
      throw storeProblem(error, 'API key');
    }
  }

  /** Lists a user's API keys as listApiKeys lists the caller's (users.read, D114). A user that does not exist is 404. */
  async listUserApiKeys(p: IdentityPrincipal, userId: string): Promise<IdentityApiKey[]> {
    this.require(p, PERMISSION_USERS_READ);
    this.haveApiKeys();
    try {
      await this.store.getUser(userId);
    } catch (error) {
      throw storeProblem(error, 'User');
    }
    return this.storedApiKeys(userId);
  }

  /**
   * Revokes a user's API key with keyId (users.write, D114). A key that
   * does not exist, is another user's or is revoked already is 404.
   */
  async revokeUserApiKey(p: IdentityPrincipal, userId: string, keyId: string): Promise<void> {
    this.require(p, PERMISSION_USERS_WRITE);
    this.haveApiKeys();
    try {
      await this.store.revokeApiKey(userId, keyId, this.now());
    } catch (error) {
      throw storeProblem(error, 'API key');
    }
  }
}
