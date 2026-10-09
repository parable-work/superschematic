import { ErrorCode, HttpProblem, badRequest } from '../problem.js';

/*
The problems the identity runtime answers with, the statuses, codes and
details the Go runtime's identity package answers: RFC 9457 bodies through
the runtime's HttpProblem. A refused input is 400 bad_request with its field
errors in the top-level errors member, as every generated server sends it.
*/

/** The code member of an identity problem's body. */
export const IdentityErrorCode = {
  /** A login, or changePassword's current password, that does not verify, for whatever reason. 401. */
  INVALID_CREDENTIALS: 'invalid_credentials',
  /**
   * A route that needs a caller, without a usable session: none, an
   * unusable Authorization header, or a session that is unknown, expired,
   * revoked or idle, or whose user is disabled. 401.
   */
  UNAUTHORIZED: ErrorCode.UNAUTHORIZED,
  /** The caller lacks the route's permission, or grants what they do not hold. 403. */
  FORBIDDEN: ErrorCode.FORBIDDEN,
  /** A cookie request, or a cookie login, the cross-origin check refuses. 403. */
  CROSS_ORIGIN: 'cross_origin',
  /** No user or role has the id. 404. */
  NOT_FOUND: ErrorCode.NOT_FOUND,
  /** The login or the role name is taken. 409. */
  CONFLICT: ErrorCode.CONFLICT,
  /** A role's permission is not dotted segments of letters, digits, '_' and '-'. 422, with the permissions in details. */
  INVALID_PERMISSION: 'invalid_permission',
} as const;

export function invalidCredentials(cause?: unknown): HttpProblem {
  return new HttpProblem(401, 'Invalid login or password', { code: IdentityErrorCode.INVALID_CREDENTIALS, cause });
}

export function unauthenticated(cause?: unknown, headers?: Record<string, string>): HttpProblem {
  return new HttpProblem(401, 'Authentication required', {
    code: IdentityErrorCode.UNAUTHORIZED,
    cause,
    ...(headers ? { headers } : {}),
  });
}

export function identityForbidden(message: string, details?: unknown): HttpProblem {
  return new HttpProblem(403, message, { code: IdentityErrorCode.FORBIDDEN, ...(details !== undefined ? { details } : {}) });
}

export function crossOrigin(): HttpProblem {
  return new HttpProblem(403, 'Cross-origin request refused', { code: IdentityErrorCode.CROSS_ORIGIN });
}

export function identityNotFound(what: string, cause?: unknown): HttpProblem {
  return new HttpProblem(404, `${what} not found`, { code: IdentityErrorCode.NOT_FOUND, cause });
}

export function identityConflict(message: string): HttpProblem {
  return new HttpProblem(409, message, { code: IdentityErrorCode.CONFLICT });
}

export function invalidPermissions(permissions: readonly string[]): HttpProblem {
  return new HttpProblem(422, "A permission is dotted segments of letters, digits, '_' and '-'", {
    code: IdentityErrorCode.INVALID_PERMISSION,
    details: { permissions: [...permissions] },
  });
}

/** The refusals of an input's fields, answered as one 400. */
export class FieldErrors {
  private readonly errors: Record<string, { validator: string; message: string }[]> = {};

  add(field: string, validator: string, message: string): void {
    (this.errors[field] ??= []).push({ validator, message });
  }

  /** Throws the 400 the fields' refusals are, when there are any. */
  throwIfAny(): void {
    if (Object.keys(this.errors).length > 0) throw badRequest('Validation failed', { extensions: { errors: this.errors } });
  }
}

/** A body that is not the operation's JSON input: 400 bad_request. */
export function bodyRefusal(reason: string): HttpProblem {
  return badRequest(`The body is not the operation's JSON input: ${reason}`);
}
