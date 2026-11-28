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
node: so they run on Workers too, and the handlers' helpers of a generated
router's behavior operations (behavior-routes.ts, D103), which build the
behaviors' core's caller and answer its refusals, importing nothing of the
repositories, and the handlers of its table operations (table-routes.ts,
D106), which read and write a table's rows through the repositories they
are handed, and the handlers of its graph operations (graph-routes.ts,
D138), which run a version graph's operations through the graph's facade
they are handed. It also holds what a generated server's
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
export type { PrincipalRateLimitStore, RateLimitDecision, RateLimitOptions, RateLimitStore } from './ratelimit.js';
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
export { FILTER_OPERATORS, FILTER_PARAM, MAX_FILTERS, MAX_FILTER_IN_VALUES, readFilter, readFilters } from './filter.js';
export type {
  EqualsFilter,
  FieldFilter,
  FilterFieldSpec,
  FilterFields,
  FilterKind,
  FilterOperator,
  FilterSpec,
  OrderedFilter,
  QueryFilter,
  RequestFilter,
  TextFilter,
} from './filter.js';
export { authorize, covers, hasAnyPermission, requestAuthenticator } from './auth.js';
export type { ActingFor, Assurance, Authenticator, GrantReach, PermissionMatcher, Principal, PrincipalKind } from './auth.js';
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
export { behaviorCaller, behaviorOperation, behaviorParams, behaviorPreconditions, principalCaller } from './behavior-routes.js';
export type { BehaviorCaller } from './behavior-routes.js';
export { tableCreate, tableDelete, tableFilter, tableList, tableMerge, tableRead, tableUpdate } from './table-routes.js';
export type { TableOperation, TableOperationKind, TableRequest } from './table-routes.js';
export {
  GRAPH_PRIMARY_NAME,
  graphArrange,
  graphBranch,
  graphCommit,
  graphCompose,
  graphDiff,
  graphDiscard,
  graphEntityHistory,
  graphHistory,
  graphMaterialize,
  graphMerge,
  graphRebase,
  graphRelease,
  graphReleased,
  graphRevert,
  graphSave,
  graphSeal,
} from './graph-routes.js';
export type { GraphFacade, GraphKind, GraphOperation, GraphOperationName, GraphRelation, GraphRequest, GraphSet } from './graph-routes.js';
export { declaredKeysOnly } from './operation.js';
export type {
  OperationAuth,
  OperationDownload,
  OperationInput,
  OperationServiceCallers,
  OperationSpec,
  OperationUpload,
  RequestContext,
} from './operation.js';
export {
  BODY_NOT_MULTIPART,
  DATA_PART,
  DEFAULT_PART_CONTENT_TYPE,
  UploadInputError,
  checkUpload,
  decodeUploads,
  mediaTypeOf,
  parseUploadFields,
  uploadBodyLimitBytes,
} from './upload.js';
export type { UploadInput, UploadedFiles } from './upload.js';
export {
  contentDisposition,
  downloadFilename,
  downloadResponse,
  entityTag,
  ifNoneMatchNames,
  notModified,
  parseHttpDate,
  sandboxPolicy,
} from './download.js';
export type { DownloadRequest, FileDownload } from './download.js';
export {
  CLIENT_TOO_OLD,
  CLIENT_VERSION_HEADER,
  MIN_CLIENT_VERSIONS_SETTING,
  clientTooOld,
  clientTooOldProblem,
  compareVersions,
  loadMinClientVersions,
  parseMinClientVersions,
} from './client-version.js';
export type { ClientTooOld, MinClientVersions } from './client-version.js';
export {
  DEFAULT_IDEMPOTENCY_IN_FLIGHT_SECONDS,
  DEFAULT_IDEMPOTENCY_TTL_SECONDS,
  IDEMPOTENCY_KEY_HEADER,
  IDEMPOTENT_REPLAY_HEADER,
  IdempotencyCode,
  idempotencyLookup,
  idempotencyRequestHash,
  isIdempotencyKey,
  memoryIdempotencyStore,
  postgresIdempotencyStore,
  runIdempotent,
  sqlIdempotencyStore,
  sqliteIdempotencyStore,
} from './idempotency.js';
export type {
  IdempotencyAttempt,
  IdempotencyBegin,
  IdempotencyPgQueryable,
  IdempotencySql,
  IdempotencyStore,
  IdempotencyTable,
  IdempotentRequest,
  OperationIdempotency,
} from './idempotency.js';
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
  CORS_EXPOSED_HEADERS,
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
  HyperdriveConnection,
  R2Credential,
  Service,
  ServiceBinding,
  ServiceCredential,
  ServiceCredentialConfig,
  ServiceCredentialOptions,
  ServiceCredentialSource,
  StackEnv,
} from './stackconfig.js';
export {
  CONTENT_PREFIX,
  ContentTooLargeError,
  DEFAULT_CONTENT_MAX_BYTES,
  DEFAULT_PAGE_SIZE,
  MAX_SIGNED_URL_SECONDS,
  ObjectNotFoundError,
  checkObjectName,
  checkSignedUrlOptions,
  contentBucket,
  contentObjectName,
} from './bucket.js';
// A stream's SHA-256 as it is read, which an attachment's upload is
// checked against (D154): workerd's native digest on Workers.
export { Sha256, sha256Stream } from './sha256.js';
export type {
  Bucket,
  BucketBody,
  BucketObject,
  BucketRead,
  ContentBucket,
  ContentPut,
  ListOptions,
  ListPage,
  PutOptions,
  PutResult,
  SignedUrlMethod,
  SignedUrlOptions,
} from './bucket.js';
export { MAX_SEARCH_TERMS, foldText, likePattern, searchMatches, searchTerms } from './search.js';
export { LOG_LEVELS, createLogger } from './logger.js';
export { SECRET_MASK, SecretValue, revealed } from './secret.js';
export type { Revealable } from './secret.js';
export type { LogFields, LogLevel, Logger, LoggerOptions } from './logger.js';
