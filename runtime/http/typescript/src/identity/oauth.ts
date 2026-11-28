import type { AccessLog } from '../access.js';
import { ClientMetadataError, clientMetadataFetcher, isMetadataDocumentUrl, type ClientMetadataFetcher } from './cimd.js';
import type { IdentityConfig } from './config.js';
import {
  OAUTH_TOKEN_NOT_HELD_MESSAGE,
  identityConflict,
  identityForbidden,
  identityNotFound,
  invalidPermissions,
  unauthenticated,
} from './errors.js';
import {
  OAUTH_CONSENT_PATH,
  OFFLINE_ACCESS_SCOPE,
  authorizationServerMetadata,
  isAllowedRedirect,
  isCodeChallenge,
  isLoopbackRedirect,
  protectedResourceMetadata,
  publicBaseOf,
  redirectHost,
  redirectMatches,
  redirectWith,
  resourceOf,
  scopeNames,
  scopesOf,
  verifyCodeChallenge,
  type OAuthMetadataOptions,
} from './oauth-protocol.js';
import type { OAuthStore, StoredOAuthClient, StoredOAuthGrant, StoredOAuthRequest } from './oauthstore.js';
import { effectivePermissions, narrowPermissions, narrowRoles, uncovered, validPermission } from './permissions.js';
import { isStoreError, type IdentityStore, type StoredRole } from './store.js';
import { hashToken, isOAuthRefreshToken, newOAuthAccessToken, newOAuthRefreshToken, newToken } from './token.js';
import type { IdentityPrincipal } from './service.js';

/*
The API's own OAuth 2.1 authorization server (#695), for an API whose
@userSessions({ oauth }) turns it on: Claude's connectors, and any client
that speaks the protocol, sign a person in with the API's logins and hold a
grant that narrows the person's permissions as an API key does.

A grant is a fourth credential beside a session, a password and an API key.
The person gives it to a client on the consent page, with a subset of the
permissions the client asks for. Its access token (oat_...) resolves on
every request to the grant's user, the grant's permissions narrowed against
the user's own at that moment, so a role change, a disabled user or a
pending one counts on the next call. Its refresh token (ort_...) rotates on
every use, and a second use of one revokes the grant (OAuth 2.1 section
4.3.1); a second redemption of a code does the same. A code works once,
for 60 s, and is bound to its client, its redirect, its PKCE challenge
(S256 alone) and its resource.

The server is the identity service's (IdentityService.oauth). Its routes
are oauth-hono.ts's.
*/

/** How long a pending request waits for the person, and how long its code lives once approved. */
export const OAUTH_REQUEST_TTL_SECONDS = 10 * 60;
export const OAUTH_CODE_TTL_SECONDS = 60;
/** How long a registered client lives unused before a registration prunes it. */
export const OAUTH_CLIENT_UNUSED_SECONDS = 90 * 24 * 60 * 60;
/** The bounds of a registration: the most redirects, and the longest name. */
const MAX_REGISTERED_REDIRECTS = 10;
const MAX_CLIENT_NAME = 100;

/** The schema's part of the server, as @userSessions({ oauth }) declares it. Every member is optional. */
export interface OAuthOptions {
  readonly accessTokenTtlSeconds?: number;
  readonly refreshTokenTtlSeconds?: number;
  readonly grantMaxTtlSeconds?: number;
  readonly clients?: { readonly metadataDocuments?: boolean; readonly registration?: boolean };
  /** The project's own consent page, a path from the site's origin or a URL, which takes ?request=<id>; the runtime's page when absent. */
  readonly consentPath?: string;
  /** The session set's path, under which the consent routes are (auth by default). */
  readonly sessionsPath?: string;
  /** Fetches a client's metadata document; clientMetadataFetcher's guarded fetch by default. */
  readonly fetchMetadata?: ClientMetadataFetcher;
}

/** What the server reads of the identity service it belongs to. */
export interface OAuthHost {
  readonly store: IdentityStore;
  readonly config: IdentityConfig;
  readonly now: () => Date;
  readonly random: (size: number) => Uint8Array;
  /** The API's permission names, which the scopes are. */
  readonly permissions: () => readonly string[];
  /** Admits a caller holding an administration permission (PERMISSION_* under the prefix), or throws 401 or 403. */
  readonly requirePermission: (p: IdentityPrincipal, name: string) => void;
  /** Where grants given and revoked are recorded (D156), when the API keeps an access log. */
  readonly accessLog?: AccessLog;
}

/** The contract's OAuthClientInfo. */
export interface OAuthClientInfo {
  readonly id: string;
  readonly name: string;
  readonly uri?: string;
  readonly redirectHost: string;
  readonly loopbackOnly: boolean;
  readonly kind: 'metadataDocument' | 'registered';
}

/** The contract's OAuthRequest. */
export interface OAuthRequestView {
  readonly id: string;
  readonly client: OAuthClientInfo;
  readonly permissions: readonly string[];
  readonly held: readonly string[];
  readonly offlineAccess: boolean;
  readonly resource: string;
  readonly expiresAt: string;
  readonly nonce: string;
}

/** The contract's ApproveOAuthRequestInput and DenyOAuthRequestInput. */
export interface ApproveOAuthRequestInput {
  readonly nonce: string;
  readonly permissions?: readonly string[];
}
export interface DenyOAuthRequestInput {
  readonly nonce: string;
}

/** The contract's OAuthDecision. */
export interface OAuthDecision {
  readonly redirectTo: string;
}

/** The contract's ConnectedApp. */
export interface ConnectedApp {
  readonly id: string;
  readonly client: OAuthClientInfo;
  readonly permissions?: readonly string[];
  readonly createdAt: string;
  readonly expiresAt: string;
  readonly lastUsedAt?: string;
}

/** What the authorize endpoint does: send the browser on, or show an error it may not send to the client's redirect. */
export type AuthorizeOutcome =
  | { readonly kind: 'redirect'; readonly location: string }
  | { readonly kind: 'error'; readonly status: number; readonly error: string; readonly description: string };

/** An answer of the token, registration or revocation endpoint: its status and JSON body. */
export interface OAuthAnswer {
  readonly status: number;
  readonly body: Record<string, unknown> | null;
}

/** An RFC 6749 section 5.2 error: the status, the code and its description. */
export class OAuthError extends Error {
  constructor(
    readonly status: number,
    readonly error: string,
    readonly description: string
  ) {
    super(`oauth: ${error}: ${description}`);
    this.name = 'OAuthError';
  }

  answer(): OAuthAnswer {
    return { status: this.status, body: { error: this.error, error_description: this.description } };
  }
}

function invalidGrant(description: string): OAuthError {
  return new OAuthError(400, 'invalid_grant', description);
}

function invalidRequest(description: string): OAuthError {
  return new OAuthError(400, 'invalid_request', description);
}

/** Go's RFC 3339 with the fraction's trailing zeros dropped, as the identity service writes a time. */
function formatDateTime(at: Date): string {
  return at.toISOString().replace(/\.(\d*?)0*Z$/u, (_m, digits: string) => (digits === '' ? 'Z' : `.${digits}Z`));
}

/** The session-only refusal of a caller a token authenticated: an OAuth grant does not act on the person's account. */
function sessionOnly(p: IdentityPrincipal): void {
  if (p.id === '') throw unauthenticated(new Error('the caller is not authenticated'));
  if ((p.apiKeyId ?? '') !== '' || (p.oauthGrantId ?? '') !== '') throw identityForbidden(OAUTH_TOKEN_NOT_HELD_MESSAGE);
}

/**
 * Whom an access token must be for (RFC 8707): the API's resource, its
 * public base's MCP endpoint. With no configured base the request's Host
 * and X-Forwarded-Prefix give it, and the scheme, which a proxy in front
 * may change, is not compared.
 */
export interface OAuthAudience {
  /** The config's publicBaseUrl, or ''. */
  readonly configured: string;
  readonly host: string;
  readonly forwardedPrefix: string | null;
}

/** Whether a grant's resource is the audience's. */
export function audienceHolds(resource: string, audience: OAuthAudience): boolean {
  if (audience.configured !== '') return resource === resourceOf(audience.configured);
  const expected = resourceOf(publicBaseOf('', 'http://x/', audience.host, audience.forwardedPrefix)).replace(/^http:/u, '');
  return resource.replace(/^https?:/u, '') === expected;
}

/** The OAuth authorization server of one API. */
export class OAuthServer {
  readonly accessTokenTtlSeconds: number;
  readonly refreshTokenTtlSeconds: number;
  readonly grantMaxTtlSeconds: number;
  readonly metadataDocuments: boolean;
  readonly registration: boolean;
  readonly consentPath: string | undefined;
  readonly sessionsPath: string;
  private readonly fetchMetadata: ClientMetadataFetcher;

  constructor(
    private readonly host: OAuthHost,
    options: OAuthOptions = {}
  ) {
    this.accessTokenTtlSeconds = options.accessTokenTtlSeconds ?? 60 * 60;
    this.refreshTokenTtlSeconds = options.refreshTokenTtlSeconds ?? 30 * 24 * 60 * 60;
    this.grantMaxTtlSeconds = options.grantMaxTtlSeconds ?? 365 * 24 * 60 * 60;
    for (const [name, value] of Object.entries({
      accessTokenTtlSeconds: this.accessTokenTtlSeconds,
      refreshTokenTtlSeconds: this.refreshTokenTtlSeconds,
      grantMaxTtlSeconds: this.grantMaxTtlSeconds,
    })) {
      if (!Number.isInteger(value) || value < 1) throw new Error(`oauth: ${name} must be a positive whole number of seconds`);
    }
    if (this.accessTokenTtlSeconds >= this.refreshTokenTtlSeconds) throw new Error("oauth: an access token's lifetime must be shorter than a refresh token's");
    if (this.refreshTokenTtlSeconds > this.grantMaxTtlSeconds) throw new Error("oauth: a refresh token's lifetime must be no longer than a grant's");
    this.metadataDocuments = options.clients?.metadataDocuments !== false;
    this.registration = options.clients?.registration !== false;
    if (!this.metadataDocuments && !this.registration) throw new Error('oauth: clients turns off both kinds of client');
    this.consentPath = options.consentPath;
    this.sessionsPath = (options.sessionsPath ?? 'auth').replace(/^\/+|\/+$/gu, '');
    this.fetchMetadata = options.fetchMetadata ?? clientMetadataFetcher();
  }

  /** The store's OAuth tables; a store without them has no OAuth (404). */
  private get store(): OAuthStore {
    const store = this.host.store.oauth?.();
    if (!store) throw identityNotFound("OAuth is not available: this database's types hold no OAuth tables");
    return store;
  }

  private now(): Date {
    return this.host.now();
  }

  private secret(): string {
    return newToken(this.host.random);
  }

  /** What the discovery documents say, for the API's public base. */
  metadataOptions(base: string): OAuthMetadataOptions {
    return { base, scopes: this.host.permissions(), metadataDocuments: this.metadataDocuments, registration: this.registration };
  }

  protectedResourceMetadata(base: string): Record<string, unknown> {
    return protectedResourceMetadata(this.metadataOptions(base));
  }

  authorizationServerMetadata(base: string): Record<string, unknown> {
    return authorizationServerMetadata(this.metadataOptions(base));
  }

  /** The API's permission names, each a scope. */
  private permissionNames(): string[] {
    return scopesOf(this.host.permissions()).filter(s => s !== OFFLINE_ACCESS_SCOPE);
  }

  // Clients.

  /**
   * A client as the consent page and the connected apps show it. On the
   * consent page the redirect is the request's, the one its code goes to;
   * among connected apps it is the client's first.
   */
  private clientInfo(client: StoredOAuthClient, redirect?: string): OAuthClientInfo {
    const shown = redirect ?? client.redirectUris[0] ?? '';
    return {
      id: client.clientId,
      name: client.name,
      ...(client.uri !== null ? { uri: client.uri } : {}),
      redirectHost: redirectHost(shown),
      loopbackOnly: redirect !== undefined ? isLoopbackRedirect(redirect) : client.redirectUris.length > 0 && client.redirectUris.every(isLoopbackRedirect),
      kind: client.kind,
    };
  }

  /**
   * The client a client_id names: a metadata document's, from the cache
   * while its cache headers keep it and fetched again after, or a
   * registered one. Undefined for one the server does not admit.
   */
  private async resolveClient(clientId: string): Promise<StoredOAuthClient | undefined> {
    const store = this.store;
    let cached: StoredOAuthClient | undefined;
    try {
      cached = await store.findClient(clientId);
    } catch (error) {
      if (!isStoreError(error, 'not_found')) throw error;
    }
    if (!isMetadataDocumentUrl(clientId)) {
      return cached?.kind === 'registered' && this.registration ? cached : undefined;
    }
    if (!this.metadataDocuments) return undefined;
    const now = this.now();
    if (cached && cached.kind === 'metadataDocument' && cached.freshUntil !== null && now.getTime() < cached.freshUntil.getTime()) return cached;
    let metadata;
    try {
      metadata = await this.fetchMetadata(clientId);
    } catch (error) {
      if (error instanceof ClientMetadataError) return undefined;
      throw error;
    }
    return store.saveMetadataClient({
      clientId,
      kind: 'metadataDocument',
      name: metadata.name,
      uri: metadata.uri,
      redirectUris: metadata.redirectUris,
      at: now,
      freshUntil: new Date(now.getTime() + metadata.ttlSeconds * 1000),
    });
  }

  // The authorization endpoint.

  /**
   * Starts an authorization: reads the request's parameters, stores the
   * pending request, and sends the browser to the consent page. A client
   * the server does not admit, or a redirect that is not the client's, is
   * an error the page shows, since the browser may not be sent there; any
   * other refusal goes back to the client's redirect with error, state and
   * iss.
   */
  async authorize(params: URLSearchParams, base: string): Promise<AuthorizeOutcome> {
    const one = (name: string): string | undefined => {
      const values = params.getAll(name);
      return values.length === 1 ? values[0] : values.length === 0 ? undefined : '\u0000';
    };
    const clientId = one('client_id');
    if (clientId === undefined || clientId === '' || clientId === '\u0000') {
      return { kind: 'error', status: 400, error: 'invalid_request', description: 'The request names no client_id.' };
    }
    const client = await this.resolveClient(clientId);
    if (!client) {
      return { kind: 'error', status: 400, error: 'invalid_client', description: 'The client is not one this server admits: its metadata document could not be read, or it is not registered.' };
    }
    const redirectUri = one('redirect_uri');
    if (redirectUri === undefined || redirectUri === '\u0000' || !redirectMatches(client.redirectUris, redirectUri)) {
      return { kind: 'error', status: 400, error: 'invalid_request', description: "The redirect_uri is not one of the client's." };
    }
    const state = one('state');
    const back = (error: string, description: string): AuthorizeOutcome => ({
      kind: 'redirect',
      location: redirectWith(redirectUri, { error, error_description: description, state: state === '\u0000' ? undefined : state, iss: base }),
    });
    if (state === '\u0000') return back('invalid_request', 'The request names state more than once.');
    if (one('response_type') !== 'code') return back('unsupported_response_type', 'The server answers response_type code alone.');
    const challenge = one('code_challenge');
    if (challenge === undefined || challenge === '\u0000' || !isCodeChallenge(challenge)) return back('invalid_request', 'The request needs a PKCE code_challenge.');
    if (one('code_challenge_method') !== 'S256') return back('invalid_request', 'The server takes code_challenge_method S256 alone.');
    const resource = resourceOf(base);
    const named = params.getAll('resource');
    if (named.length > 1 || (named.length === 1 && named[0] !== resource)) return back('invalid_target', `The server's resource is ${resource}.`);
    const scope = one('scope');
    if (scope === '\u0000') return back('invalid_scope', 'The request names scope more than once.');
    if (scope !== undefined) {
      const names = scopeNames(scope);
      const known = new Set([...this.permissionNames(), OFFLINE_ACCESS_SCOPE]);
      if (!names || names.some(name => !known.has(name))) return back('invalid_scope', 'The scope names a permission this API does not have.');
    }
    const now = this.now();
    await this.pruneEnded(now);
    await this.store.touchClient(client.id, now);
    const request = await this.store.createRequest({
      clientId: client.id,
      redirectUri,
      codeChallenge: challenge,
      state: state ?? null,
      scope: scope ?? null,
      resource,
      nonce: this.secret(),
      createdAt: now,
      expiresAt: new Date(now.getTime() + OAUTH_REQUEST_TTL_SECONDS * 1000),
    });
    return { kind: 'redirect', location: this.consentUrl(base, request.id) };
  }

  private prunedAt = 0;

  /**
   * Deletes the requests, codes and tokens that ended a day ago or more,
   * at most once a minute: an authorization anyone may start writes a
   * row, which this keeps from piling up.
   */
  private async pruneEnded(now: Date): Promise<void> {
    if (now.getTime() - this.prunedAt < 60 * 1000) return;
    this.prunedAt = now.getTime();
    await this.store.pruneEnded(new Date(now.getTime() - 24 * 60 * 60 * 1000));
  }

  /** Where the browser consents to request id: the project's page, or the runtime's. */
  consentUrl(base: string, id: string): string {
    const page = this.consentPath;
    let url: URL;
    if (page === undefined) url = new URL(`${base}${OAUTH_CONSENT_PATH}`);
    else if (/^https?:\/\//u.test(page)) url = new URL(page);
    else url = new URL(page.startsWith('/') ? page : `/${page}`, new URL(base).origin);
    url.searchParams.set('request', id);
    return url.toString();
  }

  // The consent routes.

  /** The scope's permission names, or null when the request named no scope: every permission the person holds. */
  private askedPermissions(request: StoredOAuthRequest): string[] | null {
    if (request.scope === null) return null;
    return (scopeNames(request.scope) ?? []).filter(name => name !== OFFLINE_ACCESS_SCOPE);
  }

  /** The pending request id, or 404: one that is approved, ended or not there. */
  private async pending(id: string): Promise<StoredOAuthRequest> {
    let request;
    try {
      request = await this.store.getRequest(id);
    } catch (error) {
      if (isStoreError(error, 'not_found')) throw identityNotFound('OAuth request', error);
      throw error;
    }
    if (request.approved || this.now().getTime() >= request.expiresAt.getTime()) throw identityNotFound('OAuth request');
    return request;
  }

  /** What a pending request asks for, as the consent page shows it. The caller is signed in with a session. */
  async getRequest(p: IdentityPrincipal, id: string): Promise<OAuthRequestView> {
    sessionOnly(p);
    const request = await this.pending(id);
    const client = await this.store.getClient(request.clientId);
    const asked = this.askedPermissions(request) ?? this.permissionNames();
    return {
      id: request.id,
      client: this.clientInfo(client, request.redirectUri),
      permissions: asked,
      held: narrowPermissions(p.permissions, asked),
      offlineAccess: request.scope === null || (scopeNames(request.scope) ?? []).includes(OFFLINE_ACCESS_SCOPE),
      resource: request.resource,
      expiresAt: formatDateTime(request.expiresAt),
      nonce: request.nonce,
    };
  }

  private checkNonce(request: StoredOAuthRequest, nonce: string): void {
    if (nonce === '' || nonce !== request.nonce) throw identityConflict("The nonce is not the request's");
  }

  /**
   * Approves a pending request: makes the grant, with the permissions
   * given or every one the request asks for that the person holds, and the
   * code, and answers the client's redirect with code, state and iss.
   */
  async approve(p: IdentityPrincipal, id: string, input: ApproveOAuthRequestInput, base: string): Promise<OAuthDecision> {
    sessionOnly(p);
    const request = await this.pending(id);
    this.checkNonce(request, input.nonce);
    const asked = this.askedPermissions(request);
    let permissions: string[] | null;
    if (input.permissions !== undefined && input.permissions !== null) {
      const refused = input.permissions.filter(permission => !validPermission(permission));
      if (refused.length > 0) throw invalidPermissions(refused);
      const notAsked = asked === null ? [] : input.permissions.filter(permission => !asked.includes(permission));
      const notHeld = uncovered(p.permissions, input.permissions);
      if (notAsked.length > 0 || notHeld.length > 0) {
        throw identityForbidden('A grant gives only permissions its request asks for and its user holds', { permissions: [...new Set([...notAsked, ...notHeld])] });
      }
      permissions = [...new Set(input.permissions)];
    } else {
      permissions = asked === null ? null : narrowPermissions(p.permissions, asked);
    }
    const code = this.secret();
    const now = this.now();
    let grant: StoredOAuthGrant;
    try {
      grant = await this.store.approveRequest(
        id,
        {
          userId: p.id,
          permissions,
          createdAt: now,
          grantExpiresAt: new Date(now.getTime() + this.grantMaxTtlSeconds * 1000),
          codeHash: hashToken(code),
          codeExpiresAt: new Date(now.getTime() + OAUTH_CODE_TTL_SECONDS * 1000),
        },
        now
      );
    } catch (error) {
      if (isStoreError(error, 'not_found')) throw identityNotFound('OAuth request', error);
      throw error;
    }
    await this.host.accessLog?.record({ kind: 'oauthGrant', principal: p.id, principalKind: 'user', credential: grant.id, operation: 'approveOAuthRequest' });
    return { redirectTo: redirectWith(request.redirectUri, { code, state: request.state, iss: base }) };
  }

  /** Denies a pending request, and answers the client's redirect with access_denied. */
  async deny(p: IdentityPrincipal, id: string, input: DenyOAuthRequestInput, base: string): Promise<OAuthDecision> {
    sessionOnly(p);
    const request = await this.pending(id);
    this.checkNonce(request, input.nonce);
    try {
      await this.store.deleteRequest(id);
    } catch (error) {
      if (isStoreError(error, 'not_found')) throw identityNotFound('OAuth request', error);
      throw error;
    }
    return {
      redirectTo: redirectWith(request.redirectUri, { error: 'access_denied', error_description: 'The person denied the request.', state: request.state, iss: base }),
    };
  }

  // The token endpoint.

  /** The scope a token answer names: the grant's permissions, or every one the API names, and offline_access. */
  private scopeOf(grant: StoredOAuthGrant): string {
    return [...(grant.permissions ?? this.permissionNames()), OFFLINE_ACCESS_SCOPE].join(' ');
  }

  /** Issues an access token and a refresh token of grant. */
  private async issueTokens(grant: StoredOAuthGrant): Promise<OAuthAnswer> {
    const now = this.now();
    // A client whose grant refreshes is in use, so no prune takes it.
    await this.store.touchClient(grant.clientId, now);
    const access = newOAuthAccessToken(this.host.random);
    const refresh = newOAuthRefreshToken(this.host.random);
    const refreshEnds = Math.min(now.getTime() + this.refreshTokenTtlSeconds * 1000, grant.expiresAt.getTime());
    const accessEnds = Math.min(now.getTime() + this.accessTokenTtlSeconds * 1000, grant.expiresAt.getTime());
    await this.store.createTokens(grant.id, [
      { kind: 'access', tokenHash: hashToken(access), createdAt: now, expiresAt: new Date(accessEnds) },
      { kind: 'refresh', tokenHash: hashToken(refresh), createdAt: now, expiresAt: new Date(refreshEnds) },
    ]);
    return {
      status: 200,
      body: {
        access_token: access,
        token_type: 'Bearer',
        expires_in: Math.max(1, Math.floor((accessEnds - now.getTime()) / 1000)),
        refresh_token: refresh,
        scope: this.scopeOf(grant),
      },
    };
  }

  /** A grant that is neither revoked nor ended, or invalid_grant. */
  private async liveGrant(grantId: string | null): Promise<StoredOAuthGrant> {
    if (grantId === null) throw invalidGrant('The code has no grant.');
    let grant;
    try {
      grant = await this.store.getGrant(grantId);
    } catch (error) {
      if (isStoreError(error, 'not_found')) throw invalidGrant('The grant is gone.');
      throw error;
    }
    if (grant.revokedAt !== null) throw invalidGrant('The grant is revoked.');
    if (this.now().getTime() >= grant.expiresAt.getTime()) throw invalidGrant('The grant has ended.');
    return grant;
  }

  /**
   * The token endpoint: authorization_code with its PKCE verifier,
   * redirect and resource, or refresh_token. Every refusal is RFC 6749's
   * JSON error.
   */
  async token(form: URLSearchParams): Promise<OAuthAnswer> {
    try {
      const one = (name: string): string | undefined => {
        const values = form.getAll(name);
        if (values.length > 1) throw invalidRequest(`The request names ${name} more than once.`);
        return values[0];
      };
      const grantType = one('grant_type');
      const clientId = one('client_id');
      if (form.has('client_secret')) throw new OAuthError(401, 'invalid_client', 'The server admits public clients alone, which hold no secret.');
      switch (grantType) {
        case 'authorization_code':
          return await this.redeem(one('code'), one('code_verifier'), one('redirect_uri'), clientId, form.getAll('resource'));
        case 'refresh_token':
          return await this.refresh(one('refresh_token'), clientId);
        case undefined:
          throw invalidRequest('The request names no grant_type.');
        default:
          throw new OAuthError(400, 'unsupported_grant_type', 'The server takes authorization_code and refresh_token.');
      }
    } catch (error) {
      if (error instanceof OAuthError) return error.answer();
      throw error;
    }
  }

  private async redeem(
    code: string | undefined,
    verifier: string | undefined,
    redirectUri: string | undefined,
    clientId: string | undefined,
    resources: readonly string[]
  ): Promise<OAuthAnswer> {
    if (!code || !verifier || !redirectUri || !clientId) throw invalidRequest('The request needs code, code_verifier, redirect_uri and client_id.');
    const now = this.now();
    const codeHash = hashToken(code);
    let request;
    try {
      request = await this.store.findCode(codeHash);
    } catch (error) {
      if (isStoreError(error, 'not_found')) throw invalidGrant('The code is not one this server made.');
      throw error;
    }
    const replayed = async () => {
      // A code redeemed twice: whoever holds it, its grant goes.
      if (request.grantId !== null) await this.revokeGrantRow(request.grantId, request.userId, now, 'redeemCode');
      return invalidGrant('The code was redeemed already; its grant is revoked.');
    };
    if (request.usedAt !== null) throw await replayed();
    // Every binding is checked before the code is spent, so a request
    // without its client, redirect or verifier spends nothing.
    if (now.getTime() >= request.expiresAt.getTime()) throw invalidGrant('The code has ended.');
    const client = await this.store.getClient(request.clientId);
    if (client.clientId !== clientId) throw invalidGrant("The code is another client's.");
    if (redirectUri !== request.redirectUri) throw invalidGrant("The redirect_uri is not the authorization request's.");
    if (!verifyCodeChallenge(verifier, request.codeChallenge)) throw invalidGrant('The code_verifier does not answer the code_challenge.');
    if (resources.length > 1 || (resources.length === 1 && resources[0] !== request.resource)) {
      throw new OAuthError(400, 'invalid_target', `The code is for ${request.resource}.`);
    }
    let redeemed;
    try {
      redeemed = await this.store.redeemCode(codeHash, now);
    } catch (error) {
      if (isStoreError(error, 'not_found')) throw invalidGrant('The code is not one this server made.');
      throw error;
    }
    if (redeemed.replay) throw await replayed();
    return this.issueTokens(await this.liveGrant(request.grantId));
  }

  private async refresh(token: string | undefined, clientId: string | undefined): Promise<OAuthAnswer> {
    if (!token || !clientId) throw invalidRequest('The request needs refresh_token and client_id.');
    if (!isOAuthRefreshToken(token)) throw invalidGrant('The refresh token is not one this server made.');
    const now = this.now();
    let record;
    try {
      record = await this.store.findToken(hashToken(token));
    } catch (error) {
      if (isStoreError(error, 'not_found')) throw invalidGrant('The refresh token is not one this server made.');
      throw error;
    }
    const { token: row, grant, user } = record;
    if (row.kind !== 'refresh') throw invalidGrant('The token is not a refresh token.');
    const client = await this.store.getClient(grant.clientId);
    if (client.clientId !== clientId) throw invalidGrant("The refresh token is another client's.");
    if (row.usedAt !== null) {
      // A rotated refresh token used again: someone else holds a copy.
      await this.revokeGrantRow(grant.id, grant.userId, now, 'refreshToken');
      throw invalidGrant('The refresh token was used already; its grant is revoked.');
    }
    if (row.revokedAt !== null) throw invalidGrant('The refresh token is revoked.');
    if (now.getTime() >= row.expiresAt.getTime()) throw invalidGrant('The refresh token has ended.');
    if (user.disabled || user.pending) throw invalidGrant("The grant's user cannot sign in.");
    const live = await this.liveGrant(grant.id);
    if (!(await this.store.useRefreshToken(row.id, now))) {
      await this.revokeGrantRow(grant.id, grant.userId, now, 'refreshToken');
      throw invalidGrant('The refresh token was used already; its grant is revoked.');
    }
    return this.issueTokens(live);
  }

  private async revokeGrantRow(grantId: string, userId: string | null, at: Date, operation: string): Promise<void> {
    await this.store.revokeGrant(grantId, at);
    await this.host.accessLog?.record({ kind: 'oauthRevoke', ...(userId !== null ? { principal: userId, principalKind: 'user' as const } : {}), credential: grantId, operation });
  }

  // Revocation (RFC 7009).

  /**
   * Revokes a token: an access token alone, or a refresh token and the
   * grant that issued it, with every token of the grant. A token the
   * server did not make, or another client's, is answered as one revoked,
   * as RFC 7009 says.
   */
  async revoke(form: URLSearchParams): Promise<OAuthAnswer> {
    const token = form.get('token');
    if (!token) return invalidRequest('The request names no token.').answer();
    const clientId = form.get('client_id');
    let record;
    try {
      record = await this.store.findToken(hashToken(token));
    } catch (error) {
      if (isStoreError(error, 'not_found')) return { status: 200, body: null };
      throw error;
    }
    if (clientId !== null) {
      const client = await this.store.getClient(record.grant.clientId);
      if (client.clientId !== clientId) return { status: 200, body: null };
    }
    const now = this.now();
    if (record.token.kind === 'refresh') await this.revokeGrantRow(record.grant.id, record.grant.userId, now, 'revokeToken');
    else await this.store.revokeToken(record.token.id, now);
    return { status: 200, body: null };
  }

  // Dynamic Client Registration (RFC 7591).

  /**
   * Registers a public client: redirect_uris, 1 to 10 of https or loopback
   * URLs; token_endpoint_auth_method none or absent; grant_types and
   * response_types within what the server takes; client_name and
   * client_uri optional. Each registration prunes the registered clients
   * unused for 90 days.
   */
  async register(body: unknown): Promise<OAuthAnswer> {
    if (!this.registration) return { status: 404, body: { error: 'invalid_request', error_description: 'The server takes no registrations.' } };
    const refuse = (description: string) => ({ status: 400, body: { error: 'invalid_client_metadata', error_description: description } });
    if (typeof body !== 'object' || body === null || Array.isArray(body)) return refuse('The registration is not a JSON object.');
    const doc = body as Record<string, unknown>;
    const redirects = doc.redirect_uris;
    if (!Array.isArray(redirects) || redirects.length === 0 || redirects.length > MAX_REGISTERED_REDIRECTS) {
      return { status: 400, body: { error: 'invalid_redirect_uri', error_description: `redirect_uris must list 1 to ${MAX_REGISTERED_REDIRECTS} URLs.` } };
    }
    for (const uri of redirects) {
      if (typeof uri !== 'string' || !isAllowedRedirect(uri)) {
        return { status: 400, body: { error: 'invalid_redirect_uri', error_description: `${JSON.stringify(uri)} is not https, or http on a loopback host.` } };
      }
    }
    if (doc.token_endpoint_auth_method !== undefined && doc.token_endpoint_auth_method !== 'none') {
      return refuse('token_endpoint_auth_method must be none: the server admits public clients alone.');
    }
    const within = (value: unknown, allowed: readonly string[]) =>
      value === undefined || (Array.isArray(value) && value.every(item => typeof item === 'string' && allowed.includes(item)));
    if (!within(doc.grant_types, ['authorization_code', 'refresh_token'])) return refuse('grant_types may name authorization_code and refresh_token alone.');
    if (!within(doc.response_types, ['code'])) return refuse('response_types may name code alone.');
    let name = 'Unnamed client';
    if (doc.client_name !== undefined) {
      if (typeof doc.client_name !== 'string' || doc.client_name.trim() === '') return refuse('client_name must be a string.');
      name = [...doc.client_name.trim()].slice(0, MAX_CLIENT_NAME).join('');
    }
    let uri: string | null = null;
    if (doc.client_uri !== undefined) {
      if (typeof doc.client_uri !== 'string' || !doc.client_uri.startsWith('https://')) return refuse('client_uri must be an https URL.');
      uri = doc.client_uri;
    }
    const now = this.now();
    await this.store.pruneRegisteredClients(new Date(now.getTime() - OAUTH_CLIENT_UNUSED_SECONDS * 1000), now);
    const client = await this.store.registerClient({
      clientId: `dcr_${this.secret()}`,
      kind: 'registered',
      name,
      uri,
      redirectUris: redirects as string[],
      at: now,
      freshUntil: null,
    });
    return {
      status: 201,
      body: {
        client_id: client.clientId,
        client_id_issued_at: Math.floor(now.getTime() / 1000),
        client_name: client.name,
        ...(client.uri !== null ? { client_uri: client.uri } : {}),
        redirect_uris: [...client.redirectUris],
        grant_types: ['authorization_code', 'refresh_token'],
        response_types: ['code'],
        token_endpoint_auth_method: 'none',
      },
    };
  }

  // Access tokens.

  /**
   * Resolves an access token to its principal: the token must exist, be an
   * access token neither revoked nor ended, of a grant neither revoked nor
   * ended, whose user is neither disabled nor pending; every other outcome
   * is 401. The grant's permissions are narrowed against the user's roles
   * as they are now (narrowRoles), as an API key's are. Its lastUsedAt is
   * written as an API key's is. With an audience, a token given for
   * another resource, another API's over the same authDb, is 401 too.
   */
  async authenticate(token: string, audience?: OAuthAudience): Promise<IdentityPrincipal> {
    const store = this.host.store.oauth?.();
    if (!store) throw unauthenticated(new Error('the server keeps no OAuth tables'));
    let record;
    try {
      record = await store.findToken(hashToken(token));
    } catch (error) {
      if (isStoreError(error, 'not_found')) throw unauthenticated(new Error('no OAuth token has the token'));
      throw error;
    }
    const now = this.now().getTime();
    const { token: row, grant, user } = record;
    if (row.kind !== 'access') throw unauthenticated(new Error('the token is not an access token'));
    if (row.revokedAt !== null) throw unauthenticated(new Error('the access token is revoked'));
    if (now >= row.expiresAt.getTime()) throw unauthenticated(new Error('the access token is expired'));
    if (audience && !audienceHolds(grant.resource, audience)) throw unauthenticated(new Error("the access token is another resource's"));
    if (grant.revokedAt !== null) throw unauthenticated(new Error('the grant is revoked'));
    if (now >= grant.expiresAt.getTime()) throw unauthenticated(new Error('the grant has ended'));
    if (user.disabled) throw unauthenticated(new Error('the user is disabled'));
    if (user.pending) throw unauthenticated(new Error('the user waits for approval'));
    const interval = this.host.config.touchIntervalSeconds * 1000;
    if (grant.lastUsedAt === null || now - grant.lastUsedAt.getTime() >= interval) {
      await store.touchGrant(grant.id, new Date(now), new Date(now - interval));
    }
    const held: StoredRole[] = await this.host.store.userRoles(user.id);
    const roles = grant.permissions === null ? held : narrowRoles(held, grant.permissions);
    return {
      id: user.id,
      login: user.login,
      name: user.name,
      roles,
      permissions: effectivePermissions(roles),
      sessionId: '',
      apiKeyId: '',
      oauthGrantId: grant.id,
      transport: 'bearer',
    };
  }

  // Connected apps.

  private async connectedApps(userId: string): Promise<ConnectedApp[]> {
    const records = await this.store.listGrants(userId, this.now());
    return records.map(({ grant, client }) => ({
      id: grant.id,
      client: this.clientInfo(client),
      ...(grant.permissions !== null ? { permissions: [...grant.permissions] } : {}),
      createdAt: formatDateTime(grant.createdAt),
      expiresAt: formatDateTime(grant.expiresAt),
      ...(grant.lastUsedAt !== null ? { lastUsedAt: formatDateTime(grant.lastUsedAt) } : {}),
    }));
  }

  /** The caller's live grants, newest first. The caller is signed in with a session. */
  async listGrants(p: IdentityPrincipal): Promise<ConnectedApp[]> {
    sessionOnly(p);
    return this.connectedApps(p.id);
  }

  /** Revokes a grant of the caller's and its tokens; one not theirs, or revoked already, is 404. */
  async revokeGrant(p: IdentityPrincipal, grantId: string): Promise<void> {
    sessionOnly(p);
    await this.revokeOwned(p, p.id, grantId, 'revokeOAuthGrant');
  }

  /** A user's live grants (users.read); a user that does not exist is 404. */
  async listUserGrants(p: IdentityPrincipal, userId: string, permission: string): Promise<ConnectedApp[]> {
    this.host.requirePermission(p, permission);
    try {
      await this.host.store.getUser(userId);
    } catch (error) {
      if (isStoreError(error, 'not_found')) throw identityNotFound('User', error);
      throw error;
    }
    return this.connectedApps(userId);
  }

  /** Revokes a user's grant (users.write); one not theirs, or revoked already, is 404. */
  async revokeUserGrant(p: IdentityPrincipal, userId: string, grantId: string, permission: string): Promise<void> {
    this.host.requirePermission(p, permission);
    await this.revokeOwned(p, userId, grantId, 'revokeUserOAuthGrant');
  }

  private async revokeOwned(p: IdentityPrincipal, userId: string, grantId: string, operation: string): Promise<void> {
    try {
      await this.store.revokeGrant(grantId, this.now(), userId);
    } catch (error) {
      if (isStoreError(error, 'not_found')) throw identityNotFound('OAuth grant', error);
      throw error;
    }
    await this.host.accessLog?.record({ kind: 'oauthRevoke', principal: p.id, principalKind: 'user', credential: grantId, operation });
  }
}
