/*
The framework-agnostic half of the runtime that the TypeScript API generator
(tsrestgen) emits routers against. It is the TypeScript sibling of
runtime/http/go and runtime/http/rust and keeps the same wire contract:

  success  200 application/json          {data, meta: {requestId}}
  failure  application/problem+json      {type, title, status, detail, code?,
                                          requestId, details?}       (RFC 9457)

Everything here is schema-agnostic. The generated package owns the operation
table (paths, parameter specs, strict body parsers, auth requirements); this
module owns how those specs are applied to one request. ./hono.ts binds it to
Hono; nothing in this file imports a framework.
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
  serviceUnavailable,
  tooManyRequests,
  unauthorized,
  unprocessableEntity,
} from './problem.js';
export { MemoryRateLimitStore, clientIpKey, clientIpOf } from './ratelimit.js';
export type { RateLimitDecision, RateLimitOptions, RateLimitStore } from './ratelimit.js';
export { OperationResult, envelope, envelopeResponse, requestIdOf } from './envelope.js';
export type { Envelope, EnvelopeMeta } from './envelope.js';
export { decodeJsonParam, decodeListOfLists, decodeMap, decodeParam, decodeParams } from './params.js';
export type { ParamKind, ParamSpec, ParamLocation, ParamSource, ScalarConstraints } from './params.js';
export { authorize, covers, hasAnyPermission } from './auth.js';
export type { Authenticator, PermissionMatcher, Principal } from './auth.js';
export type { OperationAuth, OperationInput, OperationSpec, RequestContext } from './operation.js';
