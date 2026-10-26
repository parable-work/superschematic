/*
The framework-agnostic half of the runtime that the TypeScript API generator
(tsrestgen) emits routers against. It is the TypeScript sibling of
runtime/http/go and runtime/http/rust and keeps the same wire contract:

  success  200 application/json          {data, meta: {requestId}}
  failure  application/problem+json      {type, title, status, detail, code?,
                                          requestId, details?}       (RFC 9457)

Everything here is schema-agnostic. The generated package owns the operation
table (paths, parameter specs, strict body parsers, auth requirements); this
module owns how those specs are applied to one request, and the service
caller's verifier and credential sources (D37), which import nothing from
node: so they run on Workers too. It also holds what a generated server's
entrypoint reads (D51): the readers of the config fields a stack derives
(stackconfig.ts), which map a loaded credential onto those sources, the
JSON-lines logger, and the provider-neutral Bucket an API's Deps holds per
bucket it lists (bucket.ts, D54), and the MemoryStore it holds per memory
store it lists, with its typed layer and the shared rate limit
(memorystore.ts, memorystore-ratelimit.ts, D75), whose Redis
implementation is ./redis. ./hono.ts binds it to Hono,
./postgres.ts opens a database's pg Pool and runs a queue's enqueue and
claims (queue.ts, D53), ./worker.ts is a worker's claim loop and ./gcs.ts
opens a bucket on GCS; nothing in this file imports a framework, pg or a
provider's client. On Cloudflare Workers (D64), ./workers.ts reads a
Worker's env and bindings, ./d1.ts is D1's interface, SqlDatabase, which
./sqlite.ts gives over bun:sqlite for stack dev, and ./r2.ts is the Bucket
over an R2 binding.
*/

export { ErrorCode, HttpProblem, problemBody, problemResponse, statusText } from './problem.js';
export type { HttpProblemOptions, ProblemBody } from './problem.js';
export {
  badRequest,
  conflict,
  forbidden,
  gatewayTimeout,
  internal,
  notFound,
  notImplemented,
  payloadTooLarge,
  serviceForbidden,
  serviceUnauthorized,
  serviceUnavailable,
  tooManyRequests,
  unauthorized,
  unprocessableEntity,
} from './problem.js';
export { MemoryRateLimitStore, clientIpKey, clientIpOf, remoteAddressKey } from './ratelimit.js';
export type { RateLimitDecision, RateLimitOptions, RateLimitStore } from './ratelimit.js';
export {
  Channel,
  InProcessMemoryStore,
  Key,
  MAX_MESSAGE_BYTES,
  MAX_SEGMENT_BYTES,
  MAX_TTL_MS,
  MemoryStoreNameError,
  MemoryStoreUnavailableError,
  checkMessage,
  checkTtl,
  disabledMemoryStore,
  integerPart,
  jsonCodec,
  memoryStoreName,
  memoryStorePrefix,
  segmentProblem,
  signal,
  signalChannel,
  signalCodec,
} from './memorystore.js';
export type {
  ChannelSubscribeOptions,
  InProcessMemoryStoreOptions,
  MemoryStore,
  MessageCodec,
  MessageHandler,
  Ref,
  Signal,
  Subscription,
} from './memorystore.js';
export {
  MemoryStoreRateLimitStore,
  RATE_LIMIT_DECLARATION,
  RATE_LIMIT_SLACK_MS,
  RATE_LIMIT_WINDOW_MS,
  memoryStoreRateLimitStore,
  rateLimitKeyName,
  rateLimitWindow,
} from './memorystore-ratelimit.js';
export type { MemoryStoreRateLimitOptions, RateLimitWindowDecision } from './memorystore-ratelimit.js';
export { OperationResult, envelope, envelopeResponse, requestIdOf } from './envelope.js';
export type { Envelope, EnvelopeMeta } from './envelope.js';
export { decodeJsonParam, decodeListOfLists, decodeMap, decodeParam, decodeParams } from './params.js';
export type { ParamKind, ParamSpec, ParamLocation, ParamSource, ScalarConstraints } from './params.js';
export {
  PAGE_CURSOR_MAX_LENGTH,
  REFUSED_CURSOR_DETAIL,
  REFUSED_CURSOR_REASON,
  SORTED_CURSOR_DETAIL,
  SORTED_CURSOR_REASON,
  bindCursor,
  bindPage,
  decodeCursor,
  encodeCursor,
  lastPage,
  morePage,
  pageFromRows,
  parseSort,
  readPage,
  refusedCursor,
  sortText,
  sortValues,
  sortedCursor,
  unbindCursor,
} from './page.js';
export type { Page, PageQuery, PageRequest, PageSort, PageSpec, SortSpec } from './page.js';
export { authorize, covers, hasAnyPermission } from './auth.js';
export type { Authenticator, PermissionMatcher, Principal } from './auth.js';
export { authorizeService, ed25519PublicJwkFromPem, serviceAuthenticator } from './serviceauth.js';
export type {
  PublicJwk,
  ServiceAuthAlgorithm,
  ServiceAuthConfig,
  ServiceAuthIssuer,
  ServiceAuthenticator,
  ServiceAuthenticatorOptions,
  ServiceCaller,
  ServiceCallerConfig,
} from './serviceauth.js';
export { googleIdTokenSource, signedTokenSource, tokenFileSource } from './credentials.js';
export { LOOPBACK_LIFETIME_SECONDS, isLoopbackUrl, loopbackServiceAuth } from './loopback.js';
export type { Loopback, LoopbackOptions } from './loopback.js';
export type {
  Ed25519PrivateJwk,
  GoogleIdTokenSourceOptions,
  ServiceTokenSource,
  SignedTokenClaims,
  SignedTokenSourceOptions,
  TokenFileSourceOptions,
} from './credentials.js';
export type { OperationAuth, OperationInput, OperationServiceCallers, OperationSpec, RequestContext } from './operation.js';
export {
  CALLERS_SUFFIX,
  CORS_SUFFIX,
  CREDENTIAL_SOURCES,
  MEMORY_STORE_AUTHS,
  SERVICE_AUTHORIZATION_HEADER,
  StackConfigError,
  loadBucket,
  loadCallers,
  loadCors,
  loadDatabase,
  loadMemoryStore,
  loadService,
  serviceCredentialFor,
} from './stackconfig.js';
export {
  CORS_ALLOWED_HEADERS,
  CORS_MAX_AGE,
  CORS_PREFLIGHT_VARY,
  CORS_VARY,
  corsDecision,
  corsHandler,
  corsMethods,
  isPreflight,
  matchOperations,
} from './cors.js';
export type { CorsApi, CorsDecision, CorsOperation, CorsPolicy } from './cors.js';
export type {
  BucketConnection,
  MemoryStoreAuth,
  MemoryStoreConnection,
  CloudSqlConnection,
  D1Connection,
  Database,
  DatabaseConnection,
  R2Credential,
  Service,
  ServiceBinding,
  ServiceCredential,
  ServiceCredentialConfig,
  ServiceCredentialOptions,
  ServiceCredentialSource,
  StackEnv,
} from './stackconfig.js';
export { DEFAULT_PAGE_SIZE, MAX_SIGNED_URL_SECONDS, ObjectNotFoundError, checkObjectName, checkSignedUrlOptions } from './bucket.js';
export type {
  Bucket,
  BucketBody,
  BucketObject,
  BucketRead,
  ListOptions,
  ListPage,
  PutOptions,
  SignedUrlMethod,
  SignedUrlOptions,
} from './bucket.js';
export { MAX_SEARCH_TERMS, foldText, likePattern, searchMatches, searchTerms } from './search.js';
export { LOG_LEVELS, createLogger } from './logger.js';
export { SECRET_MASK, SecretValue, revealed } from './secret.js';
export type { Revealable } from './secret.js';
export type { LogFields, LogLevel, Logger, LoggerOptions } from './logger.js';
