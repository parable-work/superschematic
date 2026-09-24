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

export { ErrorCode, HttpProblem, problemBody, problemResponse, statusText } from './problem';
export type { HttpProblemOptions, ProblemBody } from './problem';
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
} from './problem';
export { MemoryRateLimitStore, clientIpKey, clientIpOf } from './ratelimit';
export type { RateLimitDecision, RateLimitOptions, RateLimitStore } from './ratelimit';
export { OperationResult, envelope, envelopeResponse, requestIdOf } from './envelope';
export type { Envelope, EnvelopeMeta } from './envelope';
export { decodeListOfLists, decodeParam, decodeParams } from './params';
export type { ParamKind, ParamSpec, ParamLocation, ParamSource } from './params';
export { authorize, covers, hasAnyPermission } from './auth';
export type { Authenticator, PermissionMatcher, Principal } from './auth';
export type { OperationAuth, OperationInput, OperationSpec, RequestContext } from './operation';
