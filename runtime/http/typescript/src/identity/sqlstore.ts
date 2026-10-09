import { backend } from 'superscalar/backend';
import { VALID_SCALARS } from 'superscalar/scalars';
import { parseIdentityDescriptor, quoteIdentifier as quote, type IdentityDescriptor } from './descriptor.js';
import { compareCodePoints } from './permissions.js';
import {
  IdentityStoreError,
  type IdentityStore,
  type LoginRecord,
  type NewSession,
  type NewUser,
  type SessionRecord,
  type StoredRole,
  type StoredSession,
  type StoredUser,
} from './store.js';

/*
The identity store over SQL, built from a schema's identity descriptor, in
the two dialects D50 names: Postgres (postgres.ts binds pg) and SQLite
(sqlite.ts binds node:sqlite and bun:sqlite). It writes the statements the
Go runtime's SQLStore writes. It quotes every name the descriptor gives,
finds a login by equality once the login scalar parses it, through
superscalar's binding, and leaves the case rule to the column (CITEXT,
TEXT COLLATE NOCASE). A key is in its scalar's wire form outside the store:
an Identity.UUID key is its base62 form, as the generated types write it,
and hyphenated in the database.

Each operation is a generator that yields its statements and receives
their results, which a dialect's runner drives: the SQLite runner
synchronously, a transaction and all, so no other work on the event loop
runs a statement inside it, and the Postgres runner one awaited statement
at a time, a transaction on a connection of its own. On SQLite a timestamp
is text in UTC to the millisecond (YYYY-MM-DDTHH:MM:SS.sssZ, the DDL's
strftime default) and a role's permissions a JSON array; on Postgres
permissions are TEXT[], read and written as JSON.
*/

export type SqlDialect = 'postgres' | 'sqlite';

/** A value bound to a placeholder. The store binds text and null alone. */
export type SqlParam = string | null;

/** A row a statement returned, keyed by column alias. */
export type SqlRow = Record<string, unknown>;

/** One statement of an operation: rows answers its rows, exec the number of rows it changed. */
export interface SqlStatement {
  readonly kind: 'rows' | 'exec';
  /** The statement, with ? placeholders. */
  readonly sql: string;
  readonly params: readonly SqlParam[];
}

/** An operation: a generator yielding statements and receiving each one's result. */
export type SqlWork<T> = Generator<SqlStatement, T, unknown>;

/** Runs a store's operations in one dialect. */
export interface SqlRunner {
  readonly dialect: SqlDialect;
  /** Drives work, in one transaction when transaction is true. */
  run<T>(work: () => SqlWork<T>, transaction: boolean): Promise<T>;
}

function* rows(sql: string, params: readonly SqlParam[] = []): SqlWork<SqlRow[]> {
  return (yield { kind: 'rows', sql, params }) as SqlRow[];
}

function* exec(sql: string, params: readonly SqlParam[] = []): SqlWork<number> {
  return (yield { kind: 'exec', sql, params }) as number;
}

/** Drives an operation synchronously, each statement's failure thrown into it. */
export function driveSync<T>(work: SqlWork<T>, execute: (statement: SqlStatement) => unknown): T {
  let next = work.next();
  while (!next.done) {
    let result: unknown;
    let failure: { error: unknown } | undefined;
    try {
      result = execute(next.value);
    } catch (error) {
      failure = { error };
    }
    next = failure ? work.throw(failure.error) : work.next(result);
  }
  return next.value;
}

/** Drives an operation, awaiting each statement, each one's failure thrown into it. */
export async function driveAsync<T>(work: SqlWork<T>, execute: (statement: SqlStatement) => Promise<unknown>): Promise<T> {
  let next = work.next();
  while (!next.done) {
    let result: unknown;
    let failure: { error: unknown } | undefined;
    try {
      result = await execute(next.value);
    } catch (error) {
      failure = { error };
    }
    next = failure ? work.throw(failure.error) : work.next(result);
  }
  return next.value;
}

/** Turns a statement written with ? placeholders into Postgres's $n, leaving quoted names and literals alone. */
export function postgresPlaceholders(sql: string): string {
  let out = '';
  let n = 0;
  let quoteChar = '';
  for (const c of sql) {
    if (quoteChar !== '') {
      if (c === quoteChar) quoteChar = '';
    } else if (c === '"' || c === "'") {
      quoteChar = c;
    } else if (c === '?') {
      out += `$${++n}`;
      continue;
    }
    out += c;
  }
  return out;
}

// Keys. A key of a scalar whose SQL type is UUID (Identity.UUID and
// Identity.UserID, as superscalar's metadata types them) is base62 on the
// wire and hyphenated in the database; any other is the same text in
// both, parsed by its scalar on the way in when superscalar knows it.

const UUID_KEY_SCALARS = new Set(['Identity.UUID', 'Identity.UserID']);

/** The key of every table the loader adds, AutoGenerate<Identity.UUID>. */
const SESSION_KEY_SCALAR = 'Identity.UUID';

const BASE62 = '0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz';
const HYPHENATED_UUID = /^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$/u;
const MAX_UUID = (1n << 128n) - 1n;

function hyphenate(n: bigint): string {
  const hex = n.toString(16).padStart(32, '0');
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

/** A UUID's value from its hyphenated form (36 characters) or its base62 form (1 to 22), as superscalar's ParseUUID reads it. */
function uuidValue(key: string): bigint | undefined {
  if (key.length === 36 && HYPHENATED_UUID.test(key)) return BigInt(`0x${key.replaceAll('-', '')}`);
  if (key.length < 1 || key.length > 22) return undefined;
  let n = 0n;
  for (const c of key) {
    const digit = BASE62.indexOf(c);
    if (digit < 0) return undefined;
    n = n * 62n + BigInt(digit);
  }
  return n > MAX_UUID ? undefined : n;
}

function base62(n: bigint): string {
  if (n === 0n) return '0';
  let out = '';
  while (n > 0n) {
    out = BASE62[Number(n % 62n)] + out;
    n /= 62n;
  }
  return out;
}

class KeyCodec {
  private readonly uuid: boolean;
  private readonly known: boolean;

  constructor(private readonly scalar: string) {
    this.uuid = UUID_KEY_SCALARS.has(scalar);
    this.known = VALID_SCALARS.includes(scalar);
  }

  /** A wire key's database form, or undefined for a value that is not a key of the scalar, which names no row. */
  toDB(key: string): string | undefined {
    if (this.uuid) {
      const n = uuidValue(key);
      return n === undefined ? undefined : hyphenate(n);
    }
    if (this.known) {
      try {
        return backend.parse(this.scalar, key);
      } catch {
        return undefined;
      }
    }
    return key === '' ? undefined : key;
  }

  /** A database key's wire form. */
  toWire(key: unknown): string {
    const text = String(key);
    if (this.uuid && HYPHENATED_UUID.test(text)) return base62(BigInt(`0x${text.replaceAll('-', '')}`));
    return text;
  }
}

// Timestamps.

/** A timestamp as a statement binds it: UTC to the millisecond, the text SQLite keeps and Postgres reads. */
function timeParam(at: Date): string {
  return at.toISOString();
}

/** A timestamp a row holds: a Date (pg's own parse of TIMESTAMPTZ) or text in RFC 3339, Postgres's or SQLite's layout. */
function timeOf(value: unknown): Date | null {
  if (value === null || value === undefined) return null;
  if (value instanceof Date) return value;
  const text = String(value);
  const m = /^(\d{4}-\d{2}-\d{2})[T ](\d{2}:\d{2}:\d{2})(\.\d+)?(Z|[+-]\d{2}(?::?\d{2})?)?$/u.exec(text);
  if (!m) throw new Error(`identity: cannot read ${JSON.stringify(text)} as a timestamp`);
  const fraction = m[3] ? m[3].slice(0, 4).padEnd(4, '0') : '';
  let zone = m[4] ?? 'Z';
  if (/^[+-]\d{2}$/u.test(zone)) zone += ':00';
  else if (/^[+-]\d{4}$/u.test(zone)) zone = `${zone.slice(0, 3)}:${zone.slice(3)}`;
  const date = new Date(`${m[1]}T${m[2]}${fraction}${zone}`);
  if (Number.isNaN(date.getTime())) throw new Error(`identity: cannot read ${JSON.stringify(text)} as a timestamp`);
  return date;
}

function textOf(value: unknown): string {
  return value === null || value === undefined ? '' : String(value);
}

/** The quoted table and column names the statements use. */
interface Names {
  user: string;
  userKey: string;
  userLogin: string;
  userName: string;
  session: string;
  sessionId: string;
  sessionUser: string;
  tokenHash: string;
  createdAt: string;
  expiresAt: string;
  lastSeenAt: string;
  revokedAt: string;
  credential: string;
  credentialUser: string;
  passwordHash: string;
  passwordChangedAt: string;
  disabledAt: string;
  role: string;
  roleKey: string;
  roleName: string;
  rolePermissions: string;
  grant: string;
  grantUser: string;
  grantRole: string;
  grantedAt: string;
}

/** A role read with the database key of a user who holds it. */
interface GrantedRole {
  user: string;
  role: StoredRole;
}

/** The identity store over a SqlRunner. postgresIdentityStore and sqliteIdentityStore build one. */
export class SqlIdentityStore implements IdentityStore {
  readonly descriptor: IdentityDescriptor;
  private readonly runner: SqlRunner;
  private readonly userKey: KeyCodec;
  private readonly roleKey: KeyCodec;
  private readonly sessionKey = new KeyCodec(SESSION_KEY_SCALAR);
  private readonly loginScalar: string;
  private readonly nameScalar: string;
  private readonly nameIsLogin: boolean;
  private readonly t: Names;

  constructor(runner: SqlRunner, descriptor: string | unknown) {
    if (runner.dialect !== 'postgres' && runner.dialect !== 'sqlite') {
      throw new Error(`identity: unknown dialect ${JSON.stringify(runner.dialect)} (postgres or sqlite)`);
    }
    const d = parseIdentityDescriptor(descriptor);
    if (!VALID_SCALARS.includes(d.user.loginScalar)) {
      throw new Error(`identity: the login scalar ${JSON.stringify(d.user.loginScalar)} is not one superscalar knows`);
    }
    this.descriptor = d;
    this.runner = runner;
    this.userKey = new KeyCodec(d.user.keyScalar);
    this.roleKey = new KeyCodec(d.role?.keyScalar ?? d.user.keyScalar);
    this.loginScalar = d.user.loginScalar;
    this.nameScalar = d.user.nameScalar;
    this.nameIsLogin = d.user.columns.name === d.user.columns.login;
    const { user: u, session: s, credential: c, role: r, roleGrant: g } = d;
    this.t = {
      user: quote(u.table),
      userKey: quote(u.columns.key),
      userLogin: quote(u.columns.login),
      userName: quote(u.columns.name),
      session: quote(s.table),
      sessionId: quote(s.columns.id),
      sessionUser: quote(s.columns.user),
      tokenHash: quote(s.columns.tokenHash),
      createdAt: quote(s.columns.createdAt),
      expiresAt: quote(s.columns.expiresAt),
      lastSeenAt: quote(s.columns.lastSeenAt),
      revokedAt: quote(s.columns.revokedAt),
      credential: quote(c.table),
      credentialUser: quote(c.columns.user),
      passwordHash: quote(c.columns.passwordHash),
      passwordChangedAt: quote(c.columns.passwordChangedAt),
      disabledAt: quote(c.columns.disabledAt),
      role: r ? quote(r.table) : '',
      roleKey: r ? quote(r.columns.key) : '',
      roleName: r ? quote(r.columns.name) : '',
      rolePermissions: r ? quote(r.columns.permissions) : '',
      grant: g ? quote(g.table) : '',
      grantUser: g ? quote(g.columns.user) : '',
      grantRole: g ? quote(g.columns.role) : '',
      grantedAt: g ? quote(g.columns.grantedAt) : '',
    };
  }

  get dialect(): SqlDialect {
    return this.runner.dialect;
  }

  private run<T>(work: () => SqlWork<T>, transaction = false): Promise<T> {
    return this.runner.run(work, transaction);
  }

  /** The login scalar's parse of login. */
  private parseLogin(login: string): string {
    try {
      return backend.parse(this.loginScalar, login);
    } catch (error) {
      throw new IdentityStoreError('invalid_login', `the login is not a ${this.loginScalar}: ${(error as Error).message}`, { cause: error });
    }
  }

  private notFound(what: string): IdentityStoreError {
    return new IdentityStoreError('not_found', `${what} not found`);
  }

  private noRoles(): IdentityStoreError {
    return new IdentityStoreError('no_roles', 'the schema has no roles');
  }

  /** The expression that reads a role's permissions as a JSON array. */
  private permissionsRead(alias: string): string {
    const column = `${alias}.${this.t.rolePermissions}`;
    return this.dialect === 'postgres' ? `array_to_json(${column})::text` : column;
  }

  /** The expression that writes a role's permissions from a JSON array. */
  private permissionsWrite(): string {
    return this.dialect === 'postgres' ? 'ARRAY(SELECT e FROM json_array_elements_text(CAST(? AS json)) WITH ORDINALITY AS p(e, n) ORDER BY n)' : '?';
  }

  private *exists(table: string, column: string, key: string): SqlWork<boolean> {
    return (yield* rows(`SELECT 1 AS "one" FROM ${table} WHERE ${column} = ?`, [key])).length > 0;
  }

  /** Selects a user's key, login, name, disabledAt and password hash, the credential joined when there is one. */
  private userSelect(): string {
    const t = this.t;
    return (
      `SELECT u.${t.userKey} AS "user_key", u.${t.userLogin} AS "user_login", u.${t.userName} AS "user_name", ` +
      `c.${t.disabledAt} AS "disabled_at", c.${t.passwordHash} AS "password_hash" ` +
      `FROM ${t.user} u LEFT JOIN ${t.credential} c ON c.${t.credentialUser} = u.${t.userKey}`
    );
  }

  private loginRecordOf(row: SqlRow): LoginRecord {
    return {
      user: {
        id: this.userKey.toWire(row.user_key),
        login: textOf(row.user_login),
        name: textOf(row.user_name),
        disabled: row.disabled_at !== null && row.disabled_at !== undefined,
        roles: [],
      },
      passwordHash: textOf(row.password_hash),
    };
  }

  private *findUser(column: string, value: string): SqlWork<LoginRecord> {
    const found = yield* rows(`${this.userSelect()} WHERE u.${column} = ?`, [value]);
    if (found.length === 0) throw this.notFound('user');
    return this.loginRecordOf(found[0]!);
  }

  findLogin(login: string): Promise<LoginRecord> {
    let parsed: string;
    try {
      parsed = this.parseLogin(login);
    } catch (error) {
      return Promise.reject(error);
    }
    return this.run(() => this.findUser(this.t.userLogin, parsed));
  }

  findCredential(userId: string): Promise<LoginRecord> {
    const key = this.userKey.toDB(userId);
    if (key === undefined) return Promise.reject(this.notFound('user'));
    return this.run(() => this.findUser(this.t.userKey, key));
  }

  getUser(id: string): Promise<StoredUser> {
    const key = this.userKey.toDB(id);
    if (key === undefined) return Promise.reject(this.notFound('user'));
    const store = this;
    return this.run(function* () {
      const rec = yield* store.findUser(store.t.userKey, key);
      const roles = yield* store.rolesOfUser(key);
      return { ...rec.user, roles };
    });
  }

  listUsers(): Promise<StoredUser[]> {
    const store = this;
    const t = this.t;
    return this.run(function* () {
      const users = (yield* rows(store.userSelect())).map(row => ({ ...store.loginRecordOf(row).user, roles: [] as StoredRole[] }));
      if (store.hasRoles()) {
        const granted = yield* store.queryRoles(
          `SELECT g.${t.grantUser} AS "grant_user", r.${t.roleKey} AS "role_key", r.${t.roleName} AS "role_name", ${store.permissionsRead('r')} AS "role_permissions" ` +
            `FROM ${t.grant} g JOIN ${t.role} r ON r.${t.roleKey} = g.${t.grantRole}`,
          [],
          true
        );
        const byUser = new Map<string, StoredRole[]>();
        for (const g of granted) {
          const held = byUser.get(g.user) ?? [];
          held.push(g.role);
          byUser.set(g.user, held);
        }
        for (const user of users) {
          const held = byUser.get(user.id);
          if (held) user.roles = sortRoles(held);
        }
      }
      return users.sort((a, b) => compareCodePoints(a.login, b.login));
    });
  }

  createUser(user: NewUser): Promise<StoredUser> {
    let login: string;
    try {
      login = this.parseLogin(user.login);
    } catch (error) {
      return Promise.reject(error);
    }
    const named = user.name !== '' && !this.nameIsLogin;
    let name = named ? user.name : login;
    if (named && VALID_SCALARS.includes(this.nameScalar)) {
      // The name column has its scalar's bounds; a name outside them is the
      // caller's, not a failed write.
      try {
        name = backend.parse(this.nameScalar, name);
      } catch (error) {
        return Promise.reject(
          new IdentityStoreError('invalid_name', `the name is not a ${this.nameScalar}: ${(error as Error).message}`, { cause: error })
        );
      }
    }
    const store = this;
    const t = this.t;
    let columns = t.userLogin;
    let values = '?';
    const params: SqlParam[] = [login];
    if (!this.nameIsLogin) {
      columns += `, ${t.userName}`;
      values += ', ?';
      params.push(name);
    }
    return this.run(function* () {
      const inserted = yield* rows(
        `INSERT INTO ${t.user} (${columns}) VALUES (${values}) ON CONFLICT (${t.userLogin}) DO NOTHING RETURNING ${t.userKey} AS "user_key"`,
        params
      );
      if (inserted.length === 0) throw new IdentityStoreError('login_taken', 'the login is taken');
      const key = textOf(inserted[0]!.user_key);
      yield* exec(`INSERT INTO ${t.credential} (${t.credentialUser}, ${t.passwordHash}, ${t.passwordChangedAt}) VALUES (?, ?, ?)`, [
        key,
        user.passwordHash,
        timeParam(user.at),
      ]);
      return { id: store.userKey.toWire(key), login, name, disabled: false, roles: [] };
    }, true);
  }

  setPassword(userId: string, passwordHash: string, at: Date, keepSession: string): Promise<void> {
    const key = this.userKey.toDB(userId);
    if (key === undefined) return Promise.reject(this.notFound('user'));
    let keep = '';
    if (keepSession !== '') {
      const k = this.sessionKey.toDB(keepSession);
      if (k === undefined) return Promise.reject(this.notFound('session'));
      keep = k;
    }
    const store = this;
    const t = this.t;
    return this.run(function* () {
      if (!(yield* store.exists(t.user, t.userKey, key))) throw store.notFound('user');
      yield* exec(
        `INSERT INTO ${t.credential} (${t.credentialUser}, ${t.passwordHash}, ${t.passwordChangedAt}) VALUES (?, ?, ?)` +
          ` ON CONFLICT (${t.credentialUser}) DO UPDATE SET ${t.passwordHash} = excluded.${t.passwordHash}, ${t.passwordChangedAt} = excluded.${t.passwordChangedAt}`,
        [key, passwordHash, timeParam(at)]
      );
      yield* store.revokeSessionsOf(key, keep, at);
    }, true);
  }

  rehashPassword(userId: string, oldHash: string, newHash: string): Promise<void> {
    const key = this.userKey.toDB(userId);
    if (key === undefined) return Promise.reject(this.notFound('user'));
    const t = this.t;
    return this.run(function* () {
      yield* exec(`UPDATE ${t.credential} SET ${t.passwordHash} = ? WHERE ${t.credentialUser} = ? AND ${t.passwordHash} = ?`, [newHash, key, oldHash]);
    });
  }

  setDisabled(userId: string, disabled: boolean, at: Date): Promise<void> {
    const key = this.userKey.toDB(userId);
    if (key === undefined) return Promise.reject(this.notFound('user'));
    const store = this;
    const t = this.t;
    return this.run(function* () {
      if (!(yield* store.exists(t.user, t.userKey, key))) throw store.notFound('user');
      if (!disabled) {
        yield* exec(`UPDATE ${t.credential} SET ${t.disabledAt} = NULL WHERE ${t.credentialUser} = ?`, [key]);
        return;
      }
      // A user without a credential gets one with no password, which
      // matches none, so the user still reads as disabled.
      yield* exec(
        `INSERT INTO ${t.credential} (${t.credentialUser}, ${t.passwordHash}, ${t.passwordChangedAt}, ${t.disabledAt}) VALUES (?, '', ?, ?)` +
          ` ON CONFLICT (${t.credentialUser}) DO UPDATE SET ${t.disabledAt} = excluded.${t.disabledAt}`,
        [key, timeParam(at), timeParam(at)]
      );
      yield* store.revokeSessionsOf(key, '', at);
    }, true);
  }

  createSession(session: NewSession): Promise<StoredSession> {
    const key = this.userKey.toDB(session.userId);
    if (key === undefined) return Promise.reject(this.notFound('user'));
    const store = this;
    const t = this.t;
    return this.run(function* () {
      const inserted = yield* rows(
        `INSERT INTO ${t.session} (${t.sessionUser}, ${t.tokenHash}, ${t.createdAt}, ${t.expiresAt}) VALUES (?, ?, ?, ?) RETURNING ${t.sessionId} AS "session_id"`,
        [key, session.tokenHash, timeParam(session.createdAt), timeParam(session.expiresAt)]
      );
      return {
        id: store.sessionKey.toWire(inserted[0]!.session_id),
        userId: session.userId,
        createdAt: session.createdAt,
        expiresAt: session.expiresAt,
        lastSeenAt: null,
        revokedAt: null,
      };
    });
  }

  findSession(tokenHash: string): Promise<SessionRecord> {
    const store = this;
    const t = this.t;
    return this.run(function* () {
      const found = yield* rows(
        `SELECT s.${t.sessionId} AS "session_id", s.${t.sessionUser} AS "user_key", s.${t.createdAt} AS "created_at", s.${t.expiresAt} AS "expires_at", ` +
          `s.${t.lastSeenAt} AS "last_seen_at", s.${t.revokedAt} AS "revoked_at", u.${t.userLogin} AS "user_login", u.${t.userName} AS "user_name", ` +
          `c.${t.disabledAt} AS "disabled_at" ` +
          `FROM ${t.session} s JOIN ${t.user} u ON u.${t.userKey} = s.${t.sessionUser} ` +
          `LEFT JOIN ${t.credential} c ON c.${t.credentialUser} = s.${t.sessionUser} ` +
          `WHERE s.${t.tokenHash} = ?`,
        [tokenHash]
      );
      if (found.length === 0) throw store.notFound('session');
      const row = found[0]!;
      const userId = store.userKey.toWire(row.user_key);
      return {
        session: {
          id: store.sessionKey.toWire(row.session_id),
          userId,
          createdAt: timeOf(row.created_at)!,
          expiresAt: timeOf(row.expires_at)!,
          lastSeenAt: timeOf(row.last_seen_at),
          revokedAt: timeOf(row.revoked_at),
        },
        user: {
          id: userId,
          login: textOf(row.user_login),
          name: textOf(row.user_name),
          disabled: row.disabled_at !== null && row.disabled_at !== undefined,
          roles: [],
        },
      };
    });
  }

  touchSession(sessionId: string, at: Date, staleBefore: Date): Promise<void> {
    const id = this.sessionKey.toDB(sessionId);
    if (id === undefined) return Promise.reject(this.notFound('session'));
    const t = this.t;
    return this.run(function* () {
      yield* exec(`UPDATE ${t.session} SET ${t.lastSeenAt} = ? WHERE ${t.sessionId} = ? AND (${t.lastSeenAt} IS NULL OR ${t.lastSeenAt} < ?)`, [
        timeParam(at),
        id,
        timeParam(staleBefore),
      ]);
    });
  }

  revokeSession(sessionId: string, at: Date): Promise<void> {
    const id = this.sessionKey.toDB(sessionId);
    if (id === undefined) return Promise.reject(this.notFound('session'));
    const t = this.t;
    return this.run(function* () {
      yield* exec(`UPDATE ${t.session} SET ${t.revokedAt} = ? WHERE ${t.sessionId} = ? AND ${t.revokedAt} IS NULL`, [timeParam(at), id]);
    });
  }

  revokeUserSessions(userId: string, exceptSession: string, at: Date): Promise<void> {
    const key = this.userKey.toDB(userId);
    if (key === undefined) return Promise.reject(this.notFound('user'));
    let keep = '';
    if (exceptSession !== '') {
      const k = this.sessionKey.toDB(exceptSession);
      if (k === undefined) return Promise.reject(this.notFound('session'));
      keep = k;
    }
    const store = this;
    return this.run(function* () {
      yield* store.revokeSessionsOf(key, keep, at);
    });
  }

  /** Revokes the live sessions of the user with the database key key, but the session with the database key keep. */
  private *revokeSessionsOf(key: string, keep: string, at: Date): SqlWork<void> {
    const t = this.t;
    let sql = `UPDATE ${t.session} SET ${t.revokedAt} = ? WHERE ${t.sessionUser} = ? AND ${t.revokedAt} IS NULL`;
    const params: SqlParam[] = [timeParam(at), key];
    if (keep !== '') {
      sql += ` AND ${t.sessionId} <> ?`;
      params.push(keep);
    }
    yield* exec(sql, params);
  }

  hasRoles(): boolean {
    return this.descriptor.role !== undefined;
  }

  /** Reads roles: key, name and permissions, and the holder's key when withUser. */
  private *queryRoles(sql: string, params: readonly SqlParam[], withUser: boolean): SqlWork<GrantedRole[]> {
    const out: GrantedRole[] = [];
    for (const row of yield* rows(sql, params)) {
      const name = textOf(row.role_name);
      let permissions: string[] = [];
      const raw = row.role_permissions;
      if (raw !== null && raw !== undefined && raw !== '') {
        const parsed: unknown = typeof raw === 'string' ? JSON.parse(raw) : raw;
        if (parsed !== null) {
          if (!Array.isArray(parsed) || parsed.some(p => typeof p !== 'string')) {
            throw new Error(`identity: role ${name}'s permissions are not a list of strings`);
          }
          permissions = parsed as string[];
        }
      }
      out.push({
        user: withUser ? this.userKey.toWire(row.grant_user) : '',
        role: { id: this.roleKey.toWire(row.role_key), name, permissions },
      });
    }
    return out;
  }

  private roleSelect(): string {
    const t = this.t;
    return `SELECT r.${t.roleKey} AS "role_key", r.${t.roleName} AS "role_name", ${this.permissionsRead('r')} AS "role_permissions" FROM ${t.role} r`;
  }

  /** The roles the user with the database key key holds, by name. */
  private *rolesOfUser(key: string): SqlWork<StoredRole[]> {
    if (!this.hasRoles()) return [];
    const t = this.t;
    const granted = yield* this.queryRoles(
      `SELECT r.${t.roleKey} AS "role_key", r.${t.roleName} AS "role_name", ${this.permissionsRead('r')} AS "role_permissions" ` +
        `FROM ${t.grant} g JOIN ${t.role} r ON r.${t.roleKey} = g.${t.grantRole} WHERE g.${t.grantUser} = ?`,
      [key],
      false
    );
    return sortRoles(granted.map(g => g.role));
  }

  userRoles(userId: string): Promise<StoredRole[]> {
    if (!this.hasRoles()) return Promise.resolve([]);
    const key = this.userKey.toDB(userId);
    if (key === undefined) return Promise.reject(this.notFound('user'));
    return this.run(() => this.rolesOfUser(key));
  }

  listRoles(): Promise<StoredRole[]> {
    if (!this.hasRoles()) return Promise.reject(this.noRoles());
    const store = this;
    return this.run(function* () {
      return sortRoles((yield* store.queryRoles(store.roleSelect(), [], false)).map(g => g.role));
    });
  }

  private *roleByKey(key: string): SqlWork<StoredRole> {
    const found = yield* this.queryRoles(`${this.roleSelect()} WHERE r.${this.t.roleKey} = ?`, [key], false);
    if (found.length === 0) throw this.notFound('role');
    return found[0]!.role;
  }

  getRole(id: string): Promise<StoredRole> {
    if (!this.hasRoles()) return Promise.reject(this.noRoles());
    const key = this.roleKey.toDB(id);
    if (key === undefined) return Promise.reject(this.notFound('role'));
    return this.run(() => this.roleByKey(key));
  }

  createRole(name: string, permissions: readonly string[]): Promise<StoredRole> {
    if (!this.hasRoles()) return Promise.reject(this.noRoles());
    const store = this;
    const t = this.t;
    return this.run(function* () {
      const inserted = yield* rows(
        `INSERT INTO ${t.role} (${t.roleName}, ${t.rolePermissions}) VALUES (?, ${store.permissionsWrite()}) ON CONFLICT (${t.roleName}) DO NOTHING RETURNING ${t.roleKey} AS "role_key"`,
        [name, JSON.stringify(permissions)]
      );
      if (inserted.length === 0) throw new IdentityStoreError('role_name_taken', 'the role name is taken');
      return { id: store.roleKey.toWire(inserted[0]!.role_key), name, permissions: [...permissions] };
    });
  }

  updateRole(id: string, name: string, permissions: readonly string[]): Promise<StoredRole> {
    if (!this.hasRoles()) return Promise.reject(this.noRoles());
    const key = this.roleKey.toDB(id);
    if (key === undefined) return Promise.reject(this.notFound('role'));
    const store = this;
    const t = this.t;
    return this.run(function* () {
      if (!(yield* store.exists(t.role, t.roleKey, key))) throw store.notFound('role');
      const taken = yield* rows(`SELECT 1 AS "one" FROM ${t.role} WHERE ${t.roleName} = ? AND ${t.roleKey} <> ?`, [name, key]);
      if (taken.length > 0) throw new IdentityStoreError('role_name_taken', 'the role name is taken');
      yield* exec(`UPDATE ${t.role} SET ${t.roleName} = ?, ${t.rolePermissions} = ${store.permissionsWrite()} WHERE ${t.roleKey} = ?`, [
        name,
        JSON.stringify(permissions),
        key,
      ]);
      return yield* store.roleByKey(key);
    }, true);
  }

  deleteRole(id: string): Promise<void> {
    if (!this.hasRoles()) return Promise.reject(this.noRoles());
    const key = this.roleKey.toDB(id);
    if (key === undefined) return Promise.reject(this.notFound('role'));
    const store = this;
    const t = this.t;
    return this.run(function* () {
      yield* exec(`DELETE FROM ${t.grant} WHERE ${t.grantRole} = ?`, [key]);
      if ((yield* exec(`DELETE FROM ${t.role} WHERE ${t.roleKey} = ?`, [key])) === 0) throw store.notFound('role');
    }, true);
  }

  grantRole(userId: string, roleId: string, at: Date): Promise<void> {
    const t = this.t;
    return this.changeGrant(userId, roleId, function* (user, role) {
      yield* exec(
        `INSERT INTO ${t.grant} (${t.grantUser}, ${t.grantRole}, ${t.grantedAt}) VALUES (?, ?, ?) ON CONFLICT (${t.grantUser}, ${t.grantRole}) DO NOTHING`,
        [user, role, timeParam(at)]
      );
    });
  }

  revokeRole(userId: string, roleId: string): Promise<void> {
    const t = this.t;
    return this.changeGrant(userId, roleId, function* (user, role) {
      yield* exec(`DELETE FROM ${t.grant} WHERE ${t.grantUser} = ? AND ${t.grantRole} = ?`, [user, role]);
    });
  }

  /** Runs change in a transaction once the user and the role are found, with their database keys. */
  private changeGrant(userId: string, roleId: string, change: (user: string, role: string) => SqlWork<void>): Promise<void> {
    if (!this.hasRoles()) return Promise.reject(this.noRoles());
    const user = this.userKey.toDB(userId);
    if (user === undefined) return Promise.reject(this.notFound('user'));
    const role = this.roleKey.toDB(roleId);
    if (role === undefined) return Promise.reject(this.notFound('role'));
    const store = this;
    const t = this.t;
    return this.run(function* () {
      if (!(yield* store.exists(t.user, t.userKey, user))) throw store.notFound('user');
      if (!(yield* store.exists(t.role, t.roleKey, role))) throw store.notFound('role');
      yield* change(user, role);
    }, true);
  }
}

/** Orders roles by name, in byte order, the same in every dialect and database collation. */
function sortRoles(roles: StoredRole[]): StoredRole[] {
  return roles.sort((a, b) => compareCodePoints(a.name, b.name));
}
