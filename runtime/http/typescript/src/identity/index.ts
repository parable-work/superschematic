/*
@superschematic/http-runtime/identity: the TypeScript runtime of the core
user model (D50), the sibling of the Go runtime's identity package and held
to the same vectors (runtime/http/testdata/identity_parity.json). It signs
users in and out, resolves a request's session or API key (D114) to a
principal, and manages users, roles, grants and API keys over the tables
the core owns.

- The config (parseIdentityConfig), argon2id passwords as PHC strings
  (hashPassword, verifyPassword, passwordProblem), session tokens and API
  keys (newToken, newApiKey, hashToken), the credential on a request
  (extractCredential), the session cookie (sessionCookie, clearCookie),
  the cross-origin check (crossOriginAllowed) and credentialed CORS,
  permission names, a principal's permissions, the grant rule (uncovered)
  and its rule for a write to a user (uncoveredUser), what an API key made
  with permissions holds of a role (narrowPermissions, narrowRoles), and
  capabilities over the operation table (routesOf, capabilitiesOf).
- IdentityStore, with SqlIdentityStore over Postgres (postgresIdentityStore,
  over pg) and SQLite (sqliteIdentityStore, over node:sqlite or
  bun:sqlite, and sqlDatabaseIdentityStore, over the SqlDatabase of a file
  a server's repositories share, D116), built from the schema's identity
  descriptor.
- IdentityService, every session, administration and API key operation;
  identityAuthenticator, the router's Authenticator over it; and
  identityRouterOptions, the router options a generated router and the
  engine authenticate every route with.
- The Hono handlers of every route (identityHandler, identityHandlers,
  identityRoutes, mountIdentityRoutes), their operation table entries for
  a server that generates none (identityOperationSpec,
  mountIdentityOperations), identityCors and identityCorsRoutes.
- The API's own OAuth 2.1 authorization server (#695), which
  IdentityServiceOptions.oauth turns on: OAuthServer (IdentityService.oauth),
  its store over the descriptor's OAuth tables (SqlOAuthStore,
  IdentityStore.oauth), its routes and default consent page
  (mountOAuthRoutes), the MCP endpoint's WWW-Authenticate
  (oauthChallenge), the guarded fetch of a Client ID Metadata Document
  (clientMetadataFetcher), and the protocol's pure parts the OAuth vectors
  pin (runtime/http/testdata/oauth_parity.json).

It needs node:crypto's argon2 (Node.js 24.7 or later, or Bun) and
superscalar. It imports no database driver: pg and the SQLite modules are
the caller's, passed in open.
*/

export {
  DEFAULT_API_KEY_MAX_TTL_SECONDS,
  DEFAULT_ARGON2_ITERATIONS,
  DEFAULT_ARGON2_MEMORY_KIB,
  DEFAULT_ARGON2_PARALLELISM,
  DEFAULT_SAME_SITE,
  DEFAULT_SESSION_TTL_SECONDS,
  DEFAULT_TOUCH_INTERVAL_SECONDS,
  HOST_COOKIE_NAME,
  IdentityConfigError,
  PLAIN_COOKIE_NAME,
  SECURE_COOKIE_NAME,
  parseIdentityConfig,
  parseIdentityConfigJSON,
} from './config.js';
export type { Argon2Params, IdentityConfig, IdentityConfigInput, SameSite } from './config.js';
export { KEY_BYTES, PASSWORD_SCALAR, SALT_BYTES, hashPassword, hashPasswordWithSalt, isPassword, passwordProblem, verifyPassword } from './password.js';
export type { PasswordVerification } from './password.js';
export {
  API_KEY_LENGTH,
  API_KEY_PREFIX,
  OAUTH_ACCESS_TOKEN_PREFIX,
  OAUTH_REFRESH_TOKEN_PREFIX,
  OAUTH_TOKEN_LENGTH,
  TOKEN_BYTES,
  TOKEN_LENGTH,
  extractCredential,
  hashToken,
  isApiKey,
  isOAuthAccessToken,
  isOAuthRefreshToken,
  isToken,
  newApiKey,
  newOAuthAccessToken,
  newOAuthRefreshToken,
  newToken,
} from './token.js';
export type { CredentialOutcome, HeaderSource, Transport } from './token.js';
export { clearCookie, sessionCookie } from './cookie.js';
export { corsAnswer, crossOriginAllowed, requestHost } from './crossorigin.js';
export type { CorsAnswer, CrossOriginRequest } from './crossorigin.js';
export {
  capabilitiesOf,
  effectivePermissions,
  narrowPermissions,
  narrowRoles,
  operationIdOf,
  routeAdmits,
  routesOf,
  uncovered,
  uncoveredUser,
  validPermission,
} from './permissions.js';
export type { IdentityRoute } from './permissions.js';
export { DESCRIPTOR_VERSION, IdentityDescriptorError, parseIdentityDescriptor } from './descriptor.js';
export { quoteIdentifier } from '../sql-text.js';
export type { IdentityDescriptor } from './descriptor.js';
export { IdentityStoreError, isStoreError } from './store.js';
export type {
  ApiKeyRecord,
  IdentityStore,
  IdentityStoreErrorKind,
  LoginRecord,
  NewApiKeyRow,
  NewSession,
  NewUser,
  SessionRecord,
  StoredApiKey,
  StoredRole,
  StoredSession,
  StoredUser,
} from './store.js';
export { SqlIdentityStore } from './sqlstore.js';
export type { SqlDialect, SqlParam, SqlRow, SqlRunner, SqlStatement, SqlWork } from './sqlstore.js';
export { postgresIdentityStore, postgresRunner } from './postgres.js';
export type { PgPool, PgQueryConfig, PgQueryable } from './postgres.js';
export { bunSqlite, nodeSqlite, sqlDatabaseIdentityStore, sqlDatabaseRunner, sqliteIdentityStore, sqliteRunner } from './sqlite.js';
export type { SqliteClient, SqliteModuleDatabase, SqliteModuleStatement, SqliteValue } from './sqlite.js';
export { IdentityErrorCode } from './errors.js';
export {
  API_KEY_MAX_PERMISSIONS,
  API_KEY_NAME_MAX_LENGTH,
  DEFAULT_PERMISSION_PREFIX,
  IdentityService,
  PERMISSION_ROLES_READ,
  PERMISSION_ROLES_WRITE,
  PERMISSION_USERS_READ,
  PERMISSION_USERS_WRITE,
  formatDateTime,
  registerResultOf,
} from './service.js';
export type {
  Capabilities,
  ChangePasswordInput,
  CreateApiKeyInput,
  CreateUserInput,
  CreatedApiKey,
  CurrentUser,
  IdentityApiKey,
  IdentityPrincipal,
  IdentityRequest,
  IdentityRole,
  IdentityServiceOptions,
  IdentityUser,
  IssuedSession,
  LoginInput,
  LoginResult,
  RegisterInput,
  RegisterResult,
  Registered,
  RegistrationOptions,
  RoleInput,
  RoleRef,
  SessionUser,
  SetPasswordInput,
} from './service.js';
export { identityAuthenticator, identityPrincipalOf, identityRouterOptions, principalOf } from './authenticator.js';
export {
  CHANGE_PASSWORD_RATE_LIMIT,
  CREATE_API_KEY_RATE_LIMIT,
  DEFAULT_USER_ADMINISTRATION_PATH,
  DEFAULT_USER_SESSIONS_PATH,
  LOGIN_RATE_LIMIT,
  REGISTER_RATE_LIMIT,
  identityCors,
  identityCorsRoutes,
  identityHandler,
  identityHandlers,
  identityOperationSpec,
  identityOperations,
  identityRoutes,
  mountIdentityOperations,
  mountIdentityRoutes,
} from './hono.js';
export type {
  CorsRoute,
  IdentityHandler,
  IdentityOperation,
  IdentityOperationName,
  IdentityOperationSpecOptions,
  IdentityRouteEntry,
  IdentityRoutesOptions,
  IdentityRule,
} from './hono.js';
export { OAUTH_CLIENT_UNUSED_SECONDS, OAUTH_CODE_TTL_SECONDS, OAUTH_REQUEST_TTL_SECONDS, OAuthError, OAuthServer } from './oauth.js';
export type {
  ApproveOAuthRequestInput,
  AuthorizeOutcome,
  ConnectedApp,
  DenyOAuthRequestInput,
  OAuthAnswer,
  OAuthClientInfo,
  OAuthDecision,
  OAuthHost,
  OAuthOptions,
  OAuthRequestView,
} from './oauth.js';
export { SqlOAuthStore } from './oauthstore.js';
export type {
  NewOAuthRequest,
  NewOAuthToken,
  OAuthApproval,
  OAuthClientKind,
  OAuthClientRow,
  OAuthGrantRecord,
  OAuthStore,
  OAuthTokenKind,
  OAuthTokenRecord,
  StoredOAuthClient,
  StoredOAuthGrant,
  StoredOAuthRequest,
  StoredOAuthToken,
} from './oauthstore.js';
export {
  OAUTH_AUTHORIZE_RATE_LIMIT,
  OAUTH_DISCOVERY_RATE_LIMIT,
  OAUTH_REGISTER_RATE_LIMIT,
  OAUTH_REVOKE_RATE_LIMIT,
  OAUTH_TOKEN_RATE_LIMIT,
  consentPage,
  mountOAuthRoutes,
  oauthBaseOf,
  oauthChallenge,
} from './oauth-hono.js';
export {
  AUTHORIZATION_SERVER_METADATA_PATH,
  OAUTH_AUTHORIZE_PATH,
  OAUTH_CONSENT_PATH,
  OAUTH_REGISTER_PATH,
  OAUTH_RESOURCE_PATH,
  OAUTH_REVOKE_PATH,
  OAUTH_TOKEN_PATH,
  OFFLINE_ACCESS_SCOPE,
  OPENID_CONFIGURATION_PATH,
  PROTECTED_RESOURCE_METADATA_PATH,
  authorizationServerMetadata,
  bearerChallenge,
  codeChallengeOf,
  discoveryPaths,
  isAllowedRedirect,
  isCodeChallenge,
  isCodeVerifier,
  isLoopbackRedirect,
  protectedResourceMetadata,
  publicBaseOf,
  redirectMatches,
  resourceOf,
  scopeNames,
  scopesOf,
  verifyCodeChallenge,
} from './oauth-protocol.js';
export type { OAuthMetadataOptions } from './oauth-protocol.js';
export {
  ClientMetadataError,
  cacheSecondsOf,
  clientMetadataFetcher,
  isPrivateAddress,
  metadataDocumentUrlProblem,
  readClientMetadata,
} from './cimd.js';
export type { ClientMetadata, ClientMetadataFetchOptions, ClientMetadataFetcher } from './cimd.js';
