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
(stackconfig.ts), which map a loaded credential onto those sources, and
the JSON-lines logger. ./hono.ts binds it to Hono and ./postgres.ts opens a
database's pg Pool; nothing in this file imports a framework or pg.
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
export { OperationResult, envelope, envelopeResponse, requestIdOf } from './envelope.js';
export type { Envelope, EnvelopeMeta } from './envelope.js';
export { decodeJsonParam, decodeListOfLists, decodeMap, decodeParam, decodeParams } from './params.js';
export type { ParamKind, ParamSpec, ParamLocation, ParamSource, ScalarConstraints } from './params.js';
export { authorize, covers, hasAnyPermission } from './auth.js';
export type { Authenticator, PermissionMatcher, Principal } from './auth.js';
export { authorizeService, serviceAuthenticator } from './serviceauth.js';
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
  SERVICE_AUTHORIZATION_HEADER,
  StackConfigError,
  loadCallers,
  loadCors,
  loadDatabase,
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
  CloudSqlConnection,
  Database,
  Service,
  ServiceCredential,
  ServiceCredentialConfig,
  ServiceCredentialOptions,
  ServiceCredentialSource,
  StackEnv,
} from './stackconfig.js';
export { LOG_LEVELS, createLogger } from './logger.js';
export type { LogFields, LogLevel, Logger, LoggerOptions } from './logger.js';
