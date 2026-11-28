import type { IdentityDescriptor } from './descriptor.js';
import { quoteIdentifier as quote } from '../sql-text.js';
import { KeyCodec, SESSION_KEY_SCALAR, exec, rows, textOf, timeOf, timeParam, type SqlParam, type SqlRow, type SqlRunner, type SqlWork } from './sqlstore.js';
import { IdentityStoreError, type StoredUser } from './store.js';

/*
The storage of the OAuth authorization server (#695): the four tables the
loader adds beside the User table, OAuthClient, OAuthGrant, OAuthCode and
OAuthToken, named by the identity descriptor's oauth member. SqlOAuthStore
writes them over the SqlRunner the identity store runs, in both of its
dialects, as SqlIdentityStore writes the user model's: every id outside the
store is its key's wire form (an Identity.UUID's base62), and a list of
text is TEXT[] on Postgres and a JSON array on SQLite.

A code is a row, not memory, so any replica redeems it. Each step the
protocol needs to happen once happens in one statement whose WHERE says so:
an approval writes the code only to a pending request, a redemption marks
only an unused code, and a rotation marks only an unused refresh token, so
two requests that race see one winner.
*/

/** How a client is known: by its Client ID Metadata Document's URL, or by Dynamic Client Registration. */
export type OAuthClientKind = 'metadataDocument' | 'registered';

/** An OAuthClient row. */
export interface StoredOAuthClient {
  readonly id: string;
  /** The client_id it is known by. */
  readonly clientId: string;
  readonly kind: OAuthClientKind;
  readonly name: string;
  readonly uri: string | null;
  readonly redirectUris: readonly string[];
  readonly createdAt: Date;
  readonly refreshedAt: Date | null;
  readonly freshUntil: Date | null;
  readonly lastUsedAt: Date | null;
}

/** A client the store writes. */
export interface OAuthClientRow {
  readonly clientId: string;
  readonly kind: OAuthClientKind;
  readonly name: string;
  readonly uri: string | null;
  readonly redirectUris: readonly string[];
  /** When it was fetched or registered: createdAt of a new row, and refreshedAt of a metadata document. */
  readonly at: Date;
  /** Until when a metadata document's cache headers keep it; null for a registered client. */
  readonly freshUntil: Date | null;
}

/** An OAuthGrant row. */
export interface StoredOAuthGrant {
  readonly id: string;
  readonly userId: string;
  /** The OAuthClient row's id. */
  readonly clientId: string;
  /** Null means every permission its user holds. */
  readonly permissions: readonly string[] | null;
  readonly resource: string;
  readonly createdAt: Date;
  readonly expiresAt: Date;
  readonly lastUsedAt: Date | null;
  readonly revokedAt: Date | null;
}

/** An OAuthCode row: a pending authorization request, or, once approved, its code. */
export interface StoredOAuthRequest {
  readonly id: string;
  /** The OAuthClient row's id. */
  readonly clientId: string;
  readonly userId: string | null;
  readonly grantId: string | null;
  readonly redirectUri: string;
  readonly codeChallenge: string;
  readonly state: string | null;
  readonly scope: string | null;
  readonly resource: string;
  readonly nonce: string;
  /** Whether approval wrote the code. */
  readonly approved: boolean;
  readonly createdAt: Date;
  readonly expiresAt: Date;
  readonly usedAt: Date | null;
}

/** An authorization request the store creates. */
export interface NewOAuthRequest {
  readonly clientId: string;
  readonly redirectUri: string;
  readonly codeChallenge: string;
  readonly state: string | null;
  readonly scope: string | null;
  readonly resource: string;
  readonly nonce: string;
  readonly createdAt: Date;
  readonly expiresAt: Date;
}

/** What approving a request writes: the grant, and the code's hash and end. */
export interface OAuthApproval {
  readonly userId: string;
  readonly permissions: readonly string[] | null;
  readonly createdAt: Date;
  readonly grantExpiresAt: Date;
  readonly codeHash: string;
  readonly codeExpiresAt: Date;
}

export type OAuthTokenKind = 'access' | 'refresh';

/** An OAuthToken row. */
export interface StoredOAuthToken {
  readonly id: string;
  readonly grantId: string;
  readonly kind: OAuthTokenKind;
  readonly createdAt: Date;
  readonly expiresAt: Date;
  readonly usedAt: Date | null;
  readonly revokedAt: Date | null;
}

/** A token the store creates. */
export interface NewOAuthToken {
  readonly kind: OAuthTokenKind;
  readonly tokenHash: string;
  readonly createdAt: Date;
  readonly expiresAt: Date;
}

/** A token, its grant and the grant's user. */
export interface OAuthTokenRecord {
  readonly token: StoredOAuthToken;
  readonly grant: StoredOAuthGrant;
  readonly user: StoredUser;
}

/** A grant and the client that holds it. */
export interface OAuthGrantRecord {
  readonly grant: StoredOAuthGrant;
  readonly client: StoredOAuthClient;
}

/** The OAuth authorization server's storage. A refusal is an IdentityStoreError: not_found for a row that is not there, or not in the state the step needs. */
export interface OAuthStore {
  /** The client known by clientId. */
  findClient(clientId: string): Promise<StoredOAuthClient>;
  /** The client with the row id id. */
  getClient(id: string): Promise<StoredOAuthClient>;
  /** Writes a metadata document's client, a new row or the one its clientId names, refreshed. */
  saveMetadataClient(client: OAuthClientRow): Promise<StoredOAuthClient>;
  /** Creates a registered client. */
  registerClient(client: OAuthClientRow): Promise<StoredOAuthClient>;
  /** Sets the client's lastUsedAt. */
  touchClient(id: string, at: Date): Promise<void>;
  /**
   * Deletes the registered clients neither used nor made since
   * unusedBefore that hold no grant live at now, and answers how many.
   */
  pruneRegisteredClients(unusedBefore: Date, now: Date): Promise<number>;
  /** Deletes the requests, codes and tokens that ended before endedBefore: none of them authenticates or redeems again. */
  pruneEnded(endedBefore: Date): Promise<void>;

  createRequest(request: NewOAuthRequest): Promise<StoredOAuthRequest>;
  getRequest(id: string): Promise<StoredOAuthRequest>;
  /**
   * Approves a pending request, one with no code that has not ended by
   * now: creates the grant, for the request's client and resource, and
   * writes the code's hash, the user, the grant and the code's end to the
   * request, in one transaction. A request that is not pending is
   * not_found, and nothing is written.
   */
  approveRequest(id: string, approval: OAuthApproval, now: Date): Promise<StoredOAuthGrant>;
  /** Deletes a request that has no code yet; one that has, or none, is not_found. */
  deleteRequest(id: string): Promise<void>;
  /** The request whose code's hash is codeHash; not_found for none. */
  findCode(codeHash: string): Promise<StoredOAuthRequest>;
  /**
   * Marks the code whose hash is codeHash used at at, and answers its
   * request; replay is true when it was used already, and then nothing
   * is written. No code with the hash is not_found.
   */
  redeemCode(codeHash: string, at: Date): Promise<{ readonly request: StoredOAuthRequest; readonly replay: boolean }>;

  /** Creates tokens of the grant. */
  createTokens(grantId: string, tokens: readonly NewOAuthToken[]): Promise<void>;
  /** The token whose hash is tokenHash, its grant and the grant's user. */
  findToken(tokenHash: string): Promise<OAuthTokenRecord>;
  /** Marks an unused, unrevoked refresh token used; false when another use, or a revocation, came first. */
  useRefreshToken(tokenId: string, at: Date): Promise<boolean>;
  /** Sets the token's revokedAt when it is null. */
  revokeToken(tokenId: string, at: Date): Promise<void>;

  getGrant(id: string): Promise<StoredOAuthGrant>;
  /** The user's grants that are neither revoked nor ended by now, each with its client, newest first. */
  listGrants(userId: string, now: Date): Promise<OAuthGrantRecord[]>;
  /**
   * Revokes a live grant and every live token it issued, in one
   * transaction. With userId, a grant that is not the user's is
   * not_found, as is one revoked already.
   */
  revokeGrant(grantId: string, at: Date, userId?: string): Promise<void>;
  /** Sets the grant's lastUsedAt to at when it is null or before staleBefore. */
  touchGrant(grantId: string, at: Date, staleBefore: Date): Promise<void>;
}

/** The quoted names the statements use. */
interface OAuthNames {
  user: string;
  userKey: string;
  userLogin: string;
  userName: string;
  credential: string;
  credentialUser: string;
  disabledAt: string;
  pendingAt: string;
  client: string;
  c: Record<keyof NonNullable<IdentityDescriptor['oauth']>['client']['columns'], string>;
  grant: string;
  g: Record<keyof NonNullable<IdentityDescriptor['oauth']>['grant']['columns'], string>;
  code: string;
  k: Record<keyof NonNullable<IdentityDescriptor['oauth']>['code']['columns'], string>;
  token: string;
  t: Record<keyof NonNullable<IdentityDescriptor['oauth']>['token']['columns'], string>;
}

function quoted<T extends Record<string, string>>(columns: T): Record<keyof T, string> {
  return Object.fromEntries(Object.entries(columns).map(([member, name]) => [member, quote(name)])) as Record<keyof T, string>;
}

function notFound(what: string): IdentityStoreError {
  return new IdentityStoreError('not_found', `${what} not found`);
}

function listOf(raw: unknown, what: string): string[] | null {
  if (raw === null || raw === undefined) return null;
  if (raw === '') return [];
  let parsed: unknown;
  try {
    parsed = typeof raw === 'string' ? JSON.parse(raw) : raw;
  } catch (error) {
    throw new Error(`identity: read ${what}: ${(error as Error).message}`, { cause: error });
  }
  if (parsed === null) return null;
  if (!Array.isArray(parsed) || parsed.some(item => typeof item !== 'string')) throw new Error(`identity: read ${what}: it is not a list of strings`);
  return parsed as string[];
}

/** The OAuth store over the identity store's runner, from the descriptor's oauth member. */
export class SqlOAuthStore implements OAuthStore {
  private readonly key = new KeyCodec(SESSION_KEY_SCALAR);
  private readonly n: OAuthNames;

  constructor(
    private readonly runner: SqlRunner,
    descriptor: IdentityDescriptor,
    private readonly userKey: KeyCodec
  ) {
    const o = descriptor.oauth;
    if (!o) throw new Error('identity: the descriptor names no OAuth tables');
    this.n = {
      user: quote(descriptor.user.table),
      userKey: quote(descriptor.user.columns.key),
      userLogin: quote(descriptor.user.columns.login),
      userName: quote(descriptor.user.columns.name),
      credential: quote(descriptor.credential.table),
      credentialUser: quote(descriptor.credential.columns.user),
      disabledAt: quote(descriptor.credential.columns.disabledAt),
      pendingAt: quote(descriptor.credential.columns.pendingAt),
      client: quote(o.client.table),
      c: quoted(o.client.columns),
      grant: quote(o.grant.table),
      g: quoted(o.grant.columns),
      code: quote(o.code.table),
      k: quoted(o.code.columns),
      token: quote(o.token.table),
      t: quoted(o.token.columns),
    };
  }

  private run<T>(work: () => SqlWork<T>, transaction = false): Promise<T> {
    return this.runner.run(work, transaction);
  }

  /** A wire key's database form; a value that is no key names no row. */
  private id(wire: string, what: string): string {
    const key = this.key.toDB(wire);
    if (key === undefined) throw notFound(what);
    return key;
  }

  /** The expression that reads a list column as a JSON array, null when the column is. */
  private listRead(alias: string, column: string): string {
    return this.runner.dialect === 'postgres' ? `array_to_json(${alias}.${column})::text` : `${alias}.${column}`;
  }

  /** The expression that writes a list column from a JSON array. */
  private listWrite(): string {
    return this.runner.dialect === 'postgres' ? 'ARRAY(SELECT e FROM json_array_elements_text(CAST(? AS json)) WITH ORDINALITY AS p(e, n) ORDER BY n)' : '?';
  }

  // Clients.

  private clientColumns(alias: string): string {
    const c = this.n.c;
    return (
      `${alias}.${c.id} AS "client_row", ${alias}.${c.clientId} AS "client_id", ${alias}.${c.kind} AS "client_kind", ${alias}.${c.name} AS "client_name", ` +
      `${alias}.${c.uri} AS "client_uri", ${this.listRead(alias, c.redirectUris)} AS "client_redirects", ${alias}.${c.createdAt} AS "client_created", ` +
      `${alias}.${c.refreshedAt} AS "client_refreshed", ${alias}.${c.freshUntil} AS "client_fresh", ${alias}.${c.lastUsedAt} AS "client_used"`
    );
  }

  private clientOf(row: SqlRow): StoredOAuthClient {
    const kind = textOf(row.client_kind);
    return {
      id: this.key.toWire(row.client_row),
      clientId: textOf(row.client_id),
      kind: kind === 'registered' ? 'registered' : 'metadataDocument',
      name: textOf(row.client_name),
      uri: row.client_uri === null || row.client_uri === undefined ? null : textOf(row.client_uri),
      redirectUris: listOf(row.client_redirects, 'a client\'s redirects') ?? [],
      createdAt: timeOf(row.client_created)!,
      refreshedAt: timeOf(row.client_refreshed),
      freshUntil: timeOf(row.client_fresh),
      lastUsedAt: timeOf(row.client_used),
    };
  }

  private *clientWhere(column: string, value: string): SqlWork<StoredOAuthClient> {
    const found = yield* rows(`SELECT ${this.clientColumns('c')} FROM ${this.n.client} c WHERE c.${column} = ?`, [value]);
    if (found.length === 0) throw notFound('OAuth client');
    return this.clientOf(found[0]!);
  }

  findClient(clientId: string): Promise<StoredOAuthClient> {
    return this.run(() => this.clientWhere(this.n.c.clientId, clientId));
  }

  getClient(id: string): Promise<StoredOAuthClient> {
    let key: string;
    try {
      key = this.id(id, 'OAuth client');
    } catch (error) {
      return Promise.reject(error);
    }
    return this.run(() => this.clientWhere(this.n.c.id, key));
  }

  saveMetadataClient(client: OAuthClientRow): Promise<StoredOAuthClient> {
    const store = this;
    const { client: table, c } = this.n;
    return this.run(function* () {
      yield* exec(
        `INSERT INTO ${table} (${c.clientId}, ${c.kind}, ${c.name}, ${c.uri}, ${c.redirectUris}, ${c.createdAt}, ${c.refreshedAt}, ${c.freshUntil}) ` +
          `VALUES (?, ?, ?, ?, ${store.listWrite()}, ?, ?, ?) ON CONFLICT (${c.clientId}) DO UPDATE SET ` +
          `${c.name} = excluded.${c.name}, ${c.uri} = excluded.${c.uri}, ${c.redirectUris} = excluded.${c.redirectUris}, ` +
          `${c.refreshedAt} = excluded.${c.refreshedAt}, ${c.freshUntil} = excluded.${c.freshUntil}`,
        [
          client.clientId,
          'metadataDocument',
          client.name,
          client.uri,
          JSON.stringify(client.redirectUris),
          timeParam(client.at),
          timeParam(client.at),
          client.freshUntil === null ? null : timeParam(client.freshUntil),
        ]
      );
      return yield* store.clientWhere(c.clientId, client.clientId);
    }, true);
  }

  registerClient(client: OAuthClientRow): Promise<StoredOAuthClient> {
    const store = this;
    const { client: table, c } = this.n;
    return this.run(function* () {
      yield* exec(
        `INSERT INTO ${table} (${c.clientId}, ${c.kind}, ${c.name}, ${c.uri}, ${c.redirectUris}, ${c.createdAt}) VALUES (?, ?, ?, ?, ${store.listWrite()}, ?)`,
        [client.clientId, 'registered', client.name, client.uri, JSON.stringify(client.redirectUris), timeParam(client.at)]
      );
      return yield* store.clientWhere(c.clientId, client.clientId);
    }, true);
  }

  touchClient(id: string, at: Date): Promise<void> {
    let key: string;
    try {
      key = this.id(id, 'OAuth client');
    } catch (error) {
      return Promise.reject(error);
    }
    const { client: table, c } = this.n;
    return this.run(function* () {
      yield* exec(`UPDATE ${table} SET ${c.lastUsedAt} = ? WHERE ${c.id} = ?`, [timeParam(at), key]);
    });
  }

  pruneRegisteredClients(unusedBefore: Date, now: Date): Promise<number> {
    const { client: table, c, grant, g } = this.n;
    return this.run(function* () {
      return yield* exec(
        `DELETE FROM ${table} WHERE ${c.kind} = 'registered' AND COALESCE(${c.lastUsedAt}, ${c.createdAt}) < ? ` +
          `AND NOT EXISTS (SELECT 1 FROM ${grant} WHERE ${grant}.${g.client} = ${table}.${c.id} AND ${grant}.${g.revokedAt} IS NULL AND ${grant}.${g.expiresAt} > ?)`,
        [timeParam(unusedBefore), timeParam(now)]
      );
    });
  }

  pruneEnded(endedBefore: Date): Promise<void> {
    const { code, k, token, t } = this.n;
    return this.run(function* () {
      yield* exec(`DELETE FROM ${code} WHERE ${k.expiresAt} < ?`, [timeParam(endedBefore)]);
      yield* exec(`DELETE FROM ${token} WHERE ${t.expiresAt} < ?`, [timeParam(endedBefore)]);
    }, true);
  }

  // Requests and codes.

  private requestColumns(): string {
    const k = this.n.k;
    return (
      `r.${k.id} AS "request_id", r.${k.client} AS "request_client", r.${k.user} AS "request_user", r.${k.grant} AS "request_grant", ` +
      `r.${k.redirectUri} AS "redirect_uri", r.${k.codeChallenge} AS "code_challenge", r.${k.state} AS "request_state", r.${k.scope} AS "request_scope", ` +
      `r.${k.resource} AS "request_resource", r.${k.nonce} AS "request_nonce", r.${k.codeHash} AS "code_hash", r.${k.createdAt} AS "created_at", ` +
      `r.${k.expiresAt} AS "expires_at", r.${k.usedAt} AS "used_at"`
    );
  }

  private requestOf(row: SqlRow): StoredOAuthRequest {
    const nullable = (value: unknown) => (value === null || value === undefined ? null : textOf(value));
    return {
      id: this.key.toWire(row.request_id),
      clientId: this.key.toWire(row.request_client),
      userId: row.request_user === null || row.request_user === undefined ? null : this.userKey.toWire(row.request_user),
      grantId: row.request_grant === null || row.request_grant === undefined ? null : this.key.toWire(row.request_grant),
      redirectUri: textOf(row.redirect_uri),
      codeChallenge: textOf(row.code_challenge),
      state: nullable(row.request_state),
      scope: nullable(row.request_scope),
      resource: textOf(row.request_resource),
      nonce: textOf(row.request_nonce),
      approved: row.code_hash !== null && row.code_hash !== undefined,
      createdAt: timeOf(row.created_at)!,
      expiresAt: timeOf(row.expires_at)!,
      usedAt: timeOf(row.used_at),
    };
  }

  private *requestWhere(column: string, value: string): SqlWork<StoredOAuthRequest> {
    const found = yield* rows(`SELECT ${this.requestColumns()} FROM ${this.n.code} r WHERE r.${column} = ?`, [value]);
    if (found.length === 0) throw notFound('OAuth request');
    return this.requestOf(found[0]!);
  }

  createRequest(request: NewOAuthRequest): Promise<StoredOAuthRequest> {
    let client: string;
    try {
      client = this.id(request.clientId, 'OAuth client');
    } catch (error) {
      return Promise.reject(error);
    }
    const store = this;
    const { code: table, k } = this.n;
    return this.run(function* () {
      const inserted = yield* rows(
        `INSERT INTO ${table} (${k.client}, ${k.redirectUri}, ${k.codeChallenge}, ${k.state}, ${k.scope}, ${k.resource}, ${k.nonce}, ${k.createdAt}, ${k.expiresAt}) ` +
          `VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING ${k.id} AS "request_id"`,
        [
          client,
          request.redirectUri,
          request.codeChallenge,
          request.state,
          request.scope,
          request.resource,
          request.nonce,
          timeParam(request.createdAt),
          timeParam(request.expiresAt),
        ]
      );
      return yield* store.requestWhere(k.id, String(inserted[0]!.request_id));
    });
  }

  getRequest(id: string): Promise<StoredOAuthRequest> {
    let key: string;
    try {
      key = this.id(id, 'OAuth request');
    } catch (error) {
      return Promise.reject(error);
    }
    return this.run(() => this.requestWhere(this.n.k.id, key));
  }

  approveRequest(id: string, approval: OAuthApproval, now: Date): Promise<StoredOAuthGrant> {
    let key: string;
    let user: string;
    try {
      key = this.id(id, 'OAuth request');
      const u = this.userKey.toDB(approval.userId);
      if (u === undefined) throw notFound('user');
      user = u;
    } catch (error) {
      return Promise.reject(error);
    }
    const store = this;
    const { code, k, grant, g } = this.n;
    return this.run(function* () {
      const request = yield* store.requestWhere(k.id, key);
      if (request.approved || now.getTime() >= request.expiresAt.getTime()) throw notFound('OAuth request');
      const params: SqlParam[] = [user, store.key.toDB(request.clientId)!];
      if (approval.permissions !== null) params.push(JSON.stringify(approval.permissions));
      params.push(request.resource, timeParam(approval.createdAt), timeParam(approval.grantExpiresAt));
      const inserted = yield* rows(
        `INSERT INTO ${grant} (${g.user}, ${g.client}, ${g.permissions}, ${g.resource}, ${g.createdAt}, ${g.expiresAt}) ` +
          `VALUES (?, ?, ${approval.permissions === null ? 'NULL' : store.listWrite()}, ?, ?, ?) RETURNING ${g.id} AS "grant_id"`,
        params
      );
      const grantKey = String(inserted[0]!.grant_id);
      const changed = yield* exec(
        `UPDATE ${code} SET ${k.user} = ?, ${k.grant} = ?, ${k.codeHash} = ?, ${k.expiresAt} = ? WHERE ${k.id} = ? AND ${k.codeHash} IS NULL AND ${k.expiresAt} > ?`,
        [user, grantKey, approval.codeHash, timeParam(approval.codeExpiresAt), key, timeParam(now)]
      );
      // Another approval came first: the transaction keeps nothing.
      if (changed === 0) throw notFound('OAuth request');
      return yield* store.grantWhere(grantKey);
    }, true);
  }

  deleteRequest(id: string): Promise<void> {
    let key: string;
    try {
      key = this.id(id, 'OAuth request');
    } catch (error) {
      return Promise.reject(error);
    }
    const { code, k } = this.n;
    return this.run(function* () {
      const changed = yield* exec(`DELETE FROM ${code} WHERE ${k.id} = ? AND ${k.codeHash} IS NULL`, [key]);
      if (changed === 0) throw notFound('OAuth request');
    });
  }

  findCode(codeHash: string): Promise<StoredOAuthRequest> {
    return this.run(() => this.requestWhere(this.n.k.codeHash, codeHash));
  }

  redeemCode(codeHash: string, at: Date): Promise<{ readonly request: StoredOAuthRequest; readonly replay: boolean }> {
    const store = this;
    const { code, k } = this.n;
    return this.run(function* () {
      const changed = yield* exec(`UPDATE ${code} SET ${k.usedAt} = ? WHERE ${k.codeHash} = ? AND ${k.usedAt} IS NULL`, [timeParam(at), codeHash]);
      const request = yield* store.requestWhere(k.codeHash, codeHash);
      return { request, replay: changed === 0 };
    }, true);
  }

  // Tokens.

  createTokens(grantId: string, tokens: readonly NewOAuthToken[]): Promise<void> {
    let key: string;
    try {
      key = this.id(grantId, 'OAuth grant');
    } catch (error) {
      return Promise.reject(error);
    }
    const { token: table, t } = this.n;
    return this.run(function* () {
      for (const token of tokens) {
        yield* exec(`INSERT INTO ${table} (${t.grant}, ${t.kind}, ${t.tokenHash}, ${t.createdAt}, ${t.expiresAt}) VALUES (?, ?, ?, ?, ?)`, [
          key,
          token.kind,
          token.tokenHash,
          timeParam(token.createdAt),
          timeParam(token.expiresAt),
        ]);
      }
    }, true);
  }

  findToken(tokenHash: string): Promise<OAuthTokenRecord> {
    const store = this;
    const n = this.n;
    const { t, g } = n;
    return this.run(function* () {
      const found = yield* rows(
        `SELECT o.${t.id} AS "token_id", o.${t.kind} AS "token_kind", o.${t.createdAt} AS "token_created", o.${t.expiresAt} AS "token_expires", ` +
          `o.${t.usedAt} AS "token_used", o.${t.revokedAt} AS "token_revoked", ${store.grantColumns('g')}, ` +
          `u.${n.userLogin} AS "user_login", u.${n.userName} AS "user_name", c.${n.disabledAt} AS "disabled_at", c.${n.pendingAt} AS "pending_at" ` +
          `FROM ${n.token} o JOIN ${n.grant} g ON g.${g.id} = o.${t.grant} JOIN ${n.user} u ON u.${n.userKey} = g.${g.user} ` +
          `LEFT JOIN ${n.credential} c ON c.${n.credentialUser} = g.${g.user} WHERE o.${t.tokenHash} = ?`,
        [tokenHash]
      );
      if (found.length === 0) throw notFound('OAuth token');
      const row = found[0]!;
      const grant = store.grantOf(row);
      return {
        token: {
          id: store.key.toWire(row.token_id),
          grantId: grant.id,
          kind: textOf(row.token_kind) === 'refresh' ? 'refresh' : 'access',
          createdAt: timeOf(row.token_created)!,
          expiresAt: timeOf(row.token_expires)!,
          usedAt: timeOf(row.token_used),
          revokedAt: timeOf(row.token_revoked),
        },
        grant,
        user: {
          id: grant.userId,
          login: textOf(row.user_login),
          name: textOf(row.user_name),
          disabled: row.disabled_at !== null && row.disabled_at !== undefined,
          pending: row.pending_at !== null && row.pending_at !== undefined,
          roles: [],
        },
      };
    });
  }

  useRefreshToken(tokenId: string, at: Date): Promise<boolean> {
    let key: string;
    try {
      key = this.id(tokenId, 'OAuth token');
    } catch (error) {
      return Promise.reject(error);
    }
    const { token: table, t } = this.n;
    return this.run(function* () {
      const changed = yield* exec(
        `UPDATE ${table} SET ${t.usedAt} = ? WHERE ${t.id} = ? AND ${t.kind} = 'refresh' AND ${t.usedAt} IS NULL AND ${t.revokedAt} IS NULL`,
        [timeParam(at), key]
      );
      return changed === 1;
    });
  }

  revokeToken(tokenId: string, at: Date): Promise<void> {
    let key: string;
    try {
      key = this.id(tokenId, 'OAuth token');
    } catch (error) {
      return Promise.reject(error);
    }
    const { token: table, t } = this.n;
    return this.run(function* () {
      yield* exec(`UPDATE ${table} SET ${t.revokedAt} = ? WHERE ${t.id} = ? AND ${t.revokedAt} IS NULL`, [timeParam(at), key]);
    });
  }

  // Grants.

  private grantColumns(alias: string): string {
    const g = this.n.g;
    return (
      `${alias}.${g.id} AS "grant_id", ${alias}.${g.user} AS "grant_user", ${alias}.${g.client} AS "grant_client", ` +
      `${this.listRead(alias, g.permissions)} AS "grant_permissions", ${alias}.${g.resource} AS "grant_resource", ` +
      `${alias}.${g.createdAt} AS "grant_created", ${alias}.${g.expiresAt} AS "grant_expires", ${alias}.${g.lastUsedAt} AS "grant_used", ` +
      `${alias}.${g.revokedAt} AS "grant_revoked"`
    );
  }

  private grantOf(row: SqlRow): StoredOAuthGrant {
    const id = this.key.toWire(row.grant_id);
    return {
      id,
      userId: this.userKey.toWire(row.grant_user),
      clientId: this.key.toWire(row.grant_client),
      permissions: listOf(row.grant_permissions, `OAuth grant ${id}'s permissions`),
      resource: textOf(row.grant_resource),
      createdAt: timeOf(row.grant_created)!,
      expiresAt: timeOf(row.grant_expires)!,
      lastUsedAt: timeOf(row.grant_used),
      revokedAt: timeOf(row.grant_revoked),
    };
  }

  private *grantWhere(key: string): SqlWork<StoredOAuthGrant> {
    const found = yield* rows(`SELECT ${this.grantColumns('g')} FROM ${this.n.grant} g WHERE g.${this.n.g.id} = ?`, [key]);
    if (found.length === 0) throw notFound('OAuth grant');
    return this.grantOf(found[0]!);
  }

  getGrant(id: string): Promise<StoredOAuthGrant> {
    let key: string;
    try {
      key = this.id(id, 'OAuth grant');
    } catch (error) {
      return Promise.reject(error);
    }
    return this.run(() => this.grantWhere(key));
  }

  listGrants(userId: string, now: Date): Promise<OAuthGrantRecord[]> {
    const user = this.userKey.toDB(userId);
    if (user === undefined) return Promise.reject(notFound('user'));
    const store = this;
    const { grant, g, client, c } = this.n;
    return this.run(function* () {
      const found = yield* rows(
        `SELECT ${store.grantColumns('g')}, ${store.clientColumns('c')} FROM ${grant} g JOIN ${client} c ON c.${c.id} = g.${g.client} ` +
          `WHERE g.${g.user} = ? AND g.${g.revokedAt} IS NULL AND g.${g.expiresAt} > ? ORDER BY g.${g.createdAt} DESC, g.${g.id} DESC`,
        [user, timeParam(now)]
      );
      return found.map(row => ({ grant: store.grantOf(row), client: store.clientOf(row) }));
    });
  }

  revokeGrant(grantId: string, at: Date, userId?: string): Promise<void> {
    let key: string;
    let user: string | undefined;
    try {
      key = this.id(grantId, 'OAuth grant');
      if (userId !== undefined) {
        user = this.userKey.toDB(userId);
        if (user === undefined) throw notFound('OAuth grant');
      }
    } catch (error) {
      return Promise.reject(error);
    }
    const store = this;
    const { grant, g } = this.n;
    return this.run(function* () {
      const params: SqlParam[] = [timeParam(at), key];
      let sql = `UPDATE ${grant} SET ${g.revokedAt} = ? WHERE ${g.id} = ? AND ${g.revokedAt} IS NULL`;
      if (user !== undefined) {
        sql += ` AND ${g.user} = ?`;
        params.push(user);
      }
      const changed = yield* exec(sql, params);
      if (changed === 0 && user !== undefined) throw notFound('OAuth grant');
      yield* store.revokeTokensOf(key, at);
    }, true);
  }

  touchGrant(grantId: string, at: Date, staleBefore: Date): Promise<void> {
    let key: string;
    try {
      key = this.id(grantId, 'OAuth grant');
    } catch (error) {
      return Promise.reject(error);
    }
    const { grant, g } = this.n;
    return this.run(function* () {
      yield* exec(`UPDATE ${grant} SET ${g.lastUsedAt} = ? WHERE ${g.id} = ? AND (${g.lastUsedAt} IS NULL OR ${g.lastUsedAt} < ?)`, [
        timeParam(at),
        key,
        timeParam(staleBefore),
      ]);
    });
  }

  /** Revokes the live tokens of the grant with the database key key. */
  private *revokeTokensOf(key: string, at: Date): SqlWork<void> {
    const { token, t } = this.n;
    yield* exec(`UPDATE ${token} SET ${t.revokedAt} = ? WHERE ${t.grant} = ? AND ${t.revokedAt} IS NULL`, [timeParam(at), key]);
  }

  /** Revokes every live grant of the user with the database key user, and their tokens: a disable's revocation, in its transaction. */
  *revokeUserGrants(user: string, at: Date): SqlWork<void> {
    const { grant, g, token, t } = this.n;
    yield* exec(
      `UPDATE ${token} SET ${t.revokedAt} = ? WHERE ${t.revokedAt} IS NULL AND ${t.grant} IN (SELECT ${g.id} FROM ${grant} WHERE ${g.user} = ? AND ${g.revokedAt} IS NULL)`,
      [timeParam(at), user]
    );
    yield* exec(`UPDATE ${grant} SET ${g.revokedAt} = ? WHERE ${g.user} = ? AND ${g.revokedAt} IS NULL`, [timeParam(at), user]);
  }
}
