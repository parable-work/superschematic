import { randomBytes } from 'node:crypto';
import { hasAnyPermission, type PermissionMatcher } from '../auth.js';
import { HttpProblem } from '../problem.js';
import { parseIdentityConfig, type Argon2Params, type IdentityConfig } from './config.js';
import { clearCookie, sessionCookie } from './cookie.js';
import { crossOriginAllowed, type CrossOriginRequest } from './crossorigin.js';
import {
  FieldErrors,
  crossOrigin,
  identityConflict,
  identityForbidden,
  identityNotFound,
  invalidCredentials,
  invalidPermissions,
  unauthenticated,
} from './errors.js';
import { dummyHash, hashPassword, passwordProblem, verifyPassword } from './password.js';
import { capabilitiesOf, effectivePermissions, uncovered, validPermission, type IdentityRoute } from './permissions.js';
import { IdentityStoreError, isStoreError, type IdentityStore, type StoredRole, type StoredUser } from './store.js';
import { extractCredential, hashToken, newToken, type Transport } from './token.js';

/*
The user model's logic over a store, a config and a clock (D50), as the Go
runtime's identity.Service has it: signing users in and out, resolving a
request's session to a principal, and managing users, roles and grants. Its
methods throw HttpProblems with the Go runtime's statuses and codes (400
bad_request with field errors for a refused input), which the handlers
answer as problem documents.
*/

/** The administration routes' permissions, under the naming key identity_permission_prefix: the prefix, a dot, and one of these. */
export const PERMISSION_USERS_READ = 'users.read';
export const PERMISSION_USERS_WRITE = 'users.write';
export const PERMISSION_ROLES_READ = 'roles.read';
export const PERMISSION_ROLES_WRITE = 'roles.write';

/** identity_permission_prefix's default. */
export const DEFAULT_PERMISSION_PREFIX = 'identity';

/** An authenticated caller: the user and their roles, and the session the request carried. */
export interface IdentityPrincipal {
  readonly id: string;
  readonly login: string;
  readonly name: string;
  /** The user's roles, by name. */
  readonly roles: readonly StoredRole[];
  /** The roles' permissions, without repeats. */
  readonly permissions: readonly string[];
  readonly sessionId: string;
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

/** A session login or register created: the result the route answers, and what the HTTP layer sets the cookie from. */
export interface IssuedSession {
  readonly result: LoginResult;
  readonly token: string;
  readonly transport: Transport;
  readonly sessionId: string;
  readonly expiresAt: Date;
}

/** What the service reads of a request it authenticates: its method, Host and headers. */
export type IdentityRequest = CrossOriginRequest;

export interface IdentityServiceOptions {
  readonly store: IdentityStore;
  /** The identity config's JSON value, or a config parseIdentityConfig returned; absent is every default. */
  readonly config?: unknown;
  /** The clock sessions are created, expired and touched by. */
  readonly now?: () => Date;
  /** Where session tokens' random bytes come from (node:crypto's by default), for tests. */
  readonly random?: (size: number) => Uint8Array;
  /** The matcher capabilities and the administration routes admit callers by; hasAnyPermission by default, as the router's. */
  readonly permissionMatcher?: PermissionMatcher;
  /** identity_permission_prefix, the prefix of the administration routes' permissions. */
  readonly permissionPrefix?: string;
  /** The route requirements of every operation of the API, which capabilities answers for (routesOf builds them from an operation table). */
  readonly routes?: readonly IdentityRoute[];
  /** Answers capabilities in place of the routes, for a server whose routes the operation table does not describe (the engine's access policy). */
  readonly capabilities?: (principal: IdentityPrincipal) => Readonly<Record<string, boolean>> | Promise<Readonly<Record<string, boolean>>>;
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
  return { id: user.id, login: user.login, name: user.name, disabled: user.disabled, roles: roleRefs(user.roles) };
}

function identityRoleOf(role: StoredRole): IdentityRole {
  return { id: role.id, name: role.name, permissions: [...role.permissions] };
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

/** Turns a store's not-found, no-roles and role-name refusals into problems. */
function storeProblem(error: unknown, what: string): unknown {
  if (isStoreError(error, 'not_found')) return identityNotFound(what, error);
  if (isStoreError(error, 'no_roles')) return identityNotFound('The schema has no roles', error);
  if (isStoreError(error, 'role_name_taken')) return identityConflict('The role name is taken');
  return error;
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
   * which. A hash at an older cost is written again at the config's.
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
    if (verified.rehash) {
      const hash = await hashPassword(input.password, this.params);
      await this.store.rehashPassword(rec.user.id, rec.passwordHash, hash);
    }
    return this.issue(rec.user, transport);
  }

  /** Creates a user with a password and signs them in, as login does. A taken login is 409 conflict. */
  async register(input: RegisterInput): Promise<IssuedSession> {
    const f = new FieldErrors();
    const transport = transportOf(input.session, f);
    if (input.login === '') f.add('login', 'required', 'login is required');
    checkPasswordField(f, 'password', input.password);
    f.throwIfAny();
    const user = await this.createStoredUser(input.login, input.name ?? '', input.password);
    return this.issue(user, transport);
  }

  private async createStoredUser(login: string, name: string, password: string): Promise<StoredUser> {
    const hash = await hashPassword(password, this.params);
    try {
      return await this.store.createUser({ login, name, passwordHash: hash, at: this.now() });
    } catch (error) {
      if (error instanceof IdentityStoreError && (error.kind === 'invalid_login' || error.kind === 'invalid_name')) {
        const f = new FieldErrors();
        const cause = error.cause instanceof Error ? error.cause.message : error.message;
        f.add(error.kind === 'invalid_login' ? 'login' : 'name', 'parse', cause);
        f.throwIfAny();
      }
      if (isStoreError(error, 'login_taken')) throw identityConflict('The login is taken');
      throw error;
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
   * Resolves a request's session to its principal. The credential is the
   * Authorization bearer token, or the session cookie without an
   * Authorization header. A cookie request with a method other than GET,
   * HEAD or OPTIONS passes the cross-origin check first (403
   * cross_origin). Then the session must exist, be neither revoked,
   * expired nor idle, and belong to a user who is not disabled; every other
   * outcome is 401 unauthorized, and a refused cookie's 401 carries the
   * Set-Cookie that clears it.
   */
  async authenticate(request: IdentityRequest): Promise<IdentityPrincipal> {
    const credential = extractCredential(request.headers, this.cookieName);
    const clear = credential.transport === 'cookie' ? { 'set-cookie': this.clearCookie() } : undefined;
    if (credential.outcome === 'none') throw unauthenticated(new Error('the request carries no session'));
    if (credential.outcome === 'invalid') {
      throw unauthenticated(new Error(`the ${credential.transport} credential is not a session token`), clear);
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

  /** Resolves a session token to its principal, as authenticate does once it has read the token from a request. */
  async authenticateToken(token: string, transport: Transport): Promise<IdentityPrincipal> {
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
      transport,
    };
  }

  /** Revokes the caller's session. */
  async logout(p: IdentityPrincipal): Promise<void> {
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

  /** Sets the caller's password once current verifies (401 invalid_credentials otherwise), and revokes the user's other sessions. */
  async changePassword(p: IdentityPrincipal, input: ChangePasswordInput): Promise<void> {
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

  /** Lists every user, by login (users.read). */
  async listUsers(p: IdentityPrincipal): Promise<IdentityUser[]> {
    this.require(p, PERMISSION_USERS_READ);
    return (await this.store.listUsers()).map(identityUserOf);
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

  /** Disables a user and revokes their sessions (users.write). */
  disableUser(p: IdentityPrincipal, id: string): Promise<IdentityUser> {
    return this.setDisabled(p, id, true);
  }

  /** Enables a disabled user (users.write). */
  enableUser(p: IdentityPrincipal, id: string): Promise<IdentityUser> {
    return this.setDisabled(p, id, false);
  }

  private async setDisabled(p: IdentityPrincipal, id: string, disabled: boolean): Promise<IdentityUser> {
    this.require(p, PERMISSION_USERS_WRITE);
    try {
      await this.store.setDisabled(id, disabled, this.now());
    } catch (error) {
      throw storeProblem(error, 'User');
    }
    return this.storedUser(id);
  }

  /** Sets a user's password and revokes their sessions (users.write). */
  async setUserPassword(p: IdentityPrincipal, id: string, input: SetPasswordInput): Promise<void> {
    this.require(p, PERMISSION_USERS_WRITE);
    const f = new FieldErrors();
    checkPasswordField(f, 'password', input.password);
    f.throwIfAny();
    const hash = await hashPassword(input.password, this.params);
    try {
      await this.store.setPassword(id, hash, this.now(), '');
    } catch (error) {
      throw storeProblem(error, 'User');
    }
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
}
