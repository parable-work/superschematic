/*
The identity runtime's storage (D50): the tables the core owns (sessions,
credentials, role grants) and the project's user and role tables. Every id
is in its key scalar's wire form: an Identity.UUID key is its base62 form,
as the generated types write it. sqlstore.ts implements it over Postgres
and SQLite from the schema's identity descriptor; a deployment may pass
another.
*/

/** Why a store refused an operation, which the service turns into a problem. */
export type IdentityStoreErrorKind =
  /** No row has the id, token hash or login. */
  | 'not_found'
  /** Another user has the login, in the login column's case rule. */
  | 'login_taken'
  /** Another role has the name. */
  | 'role_name_taken'
  /** The schema has no UserRole table. */
  | 'no_roles'
  /** The login scalar does not parse the login. */
  | 'invalid_login'
  /** The name scalar does not parse a display name the store would write. */
  | 'invalid_name';

export class IdentityStoreError extends Error {
  constructor(
    readonly kind: IdentityStoreErrorKind,
    message: string,
    options?: { cause?: unknown }
  ) {
    super(`identity: ${message}`, options);
    this.name = 'IdentityStoreError';
  }
}

/** Whether error is a store's refusal of kind. */
export function isStoreError(error: unknown, kind: IdentityStoreErrorKind): error is IdentityStoreError {
  return error instanceof IdentityStoreError && error.kind === kind;
}

/** A role row. */
export interface StoredRole {
  readonly id: string;
  readonly name: string;
  readonly permissions: readonly string[];
}

/** A user as the identity routes show one. */
export interface StoredUser {
  readonly id: string;
  readonly login: string;
  readonly name: string;
  /** The user's credential has disabledAt set. */
  readonly disabled: boolean;
  /** The roles the user holds, by name: getUser and listUsers fill them in, other methods leave them empty. */
  readonly roles: readonly StoredRole[];
}

/** A user and the password hash they sign in with. */
export interface LoginRecord {
  readonly user: StoredUser;
  /** The credential's PHC string: empty when the user has no credential, or one with no password. */
  readonly passwordHash: string;
}

/** A session row. */
export interface StoredSession {
  readonly id: string;
  readonly userId: string;
  readonly createdAt: Date;
  readonly expiresAt: Date;
  readonly lastSeenAt: Date | null;
  readonly revokedAt: Date | null;
}

/** A session and its user. */
export interface SessionRecord {
  readonly session: StoredSession;
  readonly user: StoredUser;
}

/** A user a store creates, with their credential. */
export interface NewUser {
  /** The login as the caller gave it; the store parses it with the login scalar. */
  readonly login: string;
  /** The display name, written when the name column is not the login's, once the name scalar parses it. Empty means the parsed login. */
  readonly name: string;
  readonly passwordHash: string;
  readonly at: Date;
}

/** A session a store creates. */
export interface NewSession {
  readonly userId: string;
  readonly tokenHash: string;
  readonly createdAt: Date;
  readonly expiresAt: Date;
}

/**
 * The storage interface. Mutations the user model ties together run in one
 * transaction: setPassword with its revocations, setDisabled with the
 * revocations of a disable, createUser with the credential, and deleteRole
 * with the role's grants. A refusal is an IdentityStoreError.
 */
export interface IdentityStore {
  /** The user whose login equals login once the login scalar parses it, with their password hash. */
  findLogin(login: string): Promise<LoginRecord>;
  /** findLogin by the user's id. */
  findCredential(userId: string): Promise<LoginRecord>;
  /** The user with id and their roles. */
  getUser(id: string): Promise<StoredUser>;
  /** Every user with their roles, by login. */
  listUsers(): Promise<StoredUser[]>;
  /** Creates a user and their credential; the database generates the user's key. */
  createUser(user: NewUser): Promise<StoredUser>;

  /** Writes the user's password hash and passwordChangedAt, and revokes the user's sessions but keepSession (none when empty). */
  setPassword(userId: string, passwordHash: string, at: Date, keepSession: string): Promise<void>;
  /** Replaces the user's password hash when it is still oldHash, keeping passwordChangedAt: the same password at a new cost. */
  rehashPassword(userId: string, oldHash: string, newHash: string): Promise<void>;
  /** Sets the credential's disabledAt to at, or clears it. Disabling also revokes the user's sessions. */
  setDisabled(userId: string, disabled: boolean, at: Date): Promise<void>;

  /** Creates a session; the database generates its key. */
  createSession(session: NewSession): Promise<StoredSession>;
  /** The session whose token hash is tokenHash, and its user. */
  findSession(tokenHash: string): Promise<SessionRecord>;
  /** Sets lastSeenAt to at when it is null or before staleBefore, so concurrent requests write it once. */
  touchSession(sessionId: string, at: Date, staleBefore: Date): Promise<void>;
  /** Sets the session's revokedAt when it is null. */
  revokeSession(sessionId: string, at: Date): Promise<void>;
  /** Revokes every live session of the user but exceptSession (none when empty). */
  revokeUserSessions(userId: string, exceptSession: string, at: Date): Promise<void>;

  /** Whether the schema has a UserRole table. Without one, userRoles answers none and the other role methods refuse no_roles. */
  hasRoles(): boolean;
  /** The roles the user holds, by name. */
  userRoles(userId: string): Promise<StoredRole[]>;
  /** Every role, by name. */
  listRoles(): Promise<StoredRole[]>;
  getRole(id: string): Promise<StoredRole>;
  createRole(name: string, permissions: readonly string[]): Promise<StoredRole>;
  /** Rewrites a role's name and permissions. */
  updateRole(id: string, name: string, permissions: readonly string[]): Promise<StoredRole>;
  /** Deletes a role and its grants. */
  deleteRole(id: string): Promise<void>;
  /** Grants the user the role; a role already held stays granted. A user or role that does not exist is not_found. */
  grantRole(userId: string, roleId: string, at: Date): Promise<void>;
  /** Revokes the role from the user; one not held stays revoked. A user or role that does not exist is not_found. */
  revokeRole(userId: string, roleId: string): Promise<void>;
}
