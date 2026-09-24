/*
RFC 9457 Problem Details, as the Go runtime's response package writes them and
the generated Go, Rust, and TypeScript SDKs read them: `code` beside the
standard members carries the application error code, `requestId` the
correlation id, and `details` structured user-safe context. Extension members
are allowed by the RFC and kept at the top level.
*/

/** Codes for the refusals the runtime itself raises; the Rust runtime uses the same spellings. */
export const ErrorCode = {
  BAD_REQUEST: 'bad_request',
  UNAUTHORIZED: 'unauthorized',
  FORBIDDEN: 'forbidden',
  NOT_FOUND: 'not_found',
  CONFLICT: 'conflict',
  PAYLOAD_TOO_LARGE: 'payload_too_large',
  UNPROCESSABLE_ENTITY: 'unprocessable_entity',
  TOO_MANY_REQUESTS: 'too_many_requests',
  INTERNAL_ERROR: 'internal_error',
  NOT_IMPLEMENTED: 'not_implemented',
  SERVICE_UNAVAILABLE: 'service_unavailable',
  GATEWAY_TIMEOUT: 'gateway_timeout',
} as const;

const STATUS_TEXT: Record<number, string> = {
  400: 'Bad Request',
  401: 'Unauthorized',
  402: 'Payment Required',
  403: 'Forbidden',
  404: 'Not Found',
  405: 'Method Not Allowed',
  406: 'Not Acceptable',
  408: 'Request Timeout',
  409: 'Conflict',
  410: 'Gone',
  412: 'Precondition Failed',
  413: 'Request Entity Too Large',
  415: 'Unsupported Media Type',
  422: 'Unprocessable Entity',
  423: 'Locked',
  428: 'Precondition Required',
  429: 'Too Many Requests',
  500: 'Internal Server Error',
  501: 'Not Implemented',
  502: 'Bad Gateway',
  503: 'Service Unavailable',
  504: 'Gateway Timeout',
};

/** The reason phrase Go's http.StatusText gives a status, or "Error". */
export function statusText(status: number): string {
  return STATUS_TEXT[status] ?? 'Error';
}

export interface ProblemBody {
  type: string;
  title: string;
  status: number;
  detail: string;
  code?: string;
  requestId?: string;
  details?: unknown;
  [extension: string]: unknown;
}

export interface HttpProblemOptions {
  /** Application or runtime error code, serialized as the `code` member. */
  code?: string;
  /** Structured, user-safe context; crosses the wire verbatim as `details`. */
  details?: unknown;
  /** Extra top-level members (RFC 9457 extensions). Standard members win on collision. */
  extensions?: Record<string, unknown>;
  /** Underlying failure, kept off the wire. */
  cause?: unknown;
}

/** An HTTP refusal an implementation can throw; the adapter turns it into the envelope. */
export class HttpProblem extends Error {
  readonly status: number;
  /** The same status under the name the SDK's ApiError uses. */
  readonly statusCode: number;
  readonly code: string | undefined;
  readonly details: unknown;
  readonly extensions: Record<string, unknown> | undefined;

  constructor(status: number, detail: string, options: HttpProblemOptions = {}) {
    super(detail, options.cause !== undefined ? { cause: options.cause } : undefined);
    this.name = 'HttpProblem';
    this.status = status;
    this.statusCode = status;
    this.code = options.code;
    this.details = options.details;
    this.extensions = options.extensions;
  }
}

export function problemBody(problem: HttpProblem, requestId: string): ProblemBody {
  return {
    ...(problem.extensions ?? {}),
    type: 'about:blank',
    title: statusText(problem.status),
    status: problem.status,
    detail: problem.message,
    ...(problem.code ? { code: problem.code } : {}),
    requestId,
    ...(problem.details !== undefined ? { details: problem.details } : {}),
  };
}

export function problemResponse(problem: HttpProblem, requestId: string): Response {
  return new Response(JSON.stringify(problemBody(problem, requestId)), {
    status: problem.status,
    headers: {
      'content-type': 'application/problem+json',
      'cache-control': 'no-store',
      'x-request-id': requestId,
    },
  });
}

/** Options of the status helpers; a `code` here replaces the helper's default runtime code. */
type Extra = HttpProblemOptions;

export function badRequest(detail = 'Bad Request', extra: Extra = {}): HttpProblem {
  return new HttpProblem(400, detail, { code: ErrorCode.BAD_REQUEST, ...extra });
}

export function unauthorized(detail = 'Authentication required', extra: Extra = {}): HttpProblem {
  return new HttpProblem(401, detail, { code: ErrorCode.UNAUTHORIZED, ...extra });
}

export function forbidden(detail = 'Insufficient permissions', extra: Extra = {}): HttpProblem {
  return new HttpProblem(403, detail, { code: ErrorCode.FORBIDDEN, ...extra });
}

export function notFound(detail = 'Not Found', extra: Extra = {}): HttpProblem {
  return new HttpProblem(404, detail, { code: ErrorCode.NOT_FOUND, ...extra });
}

export function conflict(detail = 'Conflict', extra: Extra = {}): HttpProblem {
  return new HttpProblem(409, detail, { code: ErrorCode.CONFLICT, ...extra });
}

export function payloadTooLarge(detail = 'Request body exceeds the accepted size', extra: Extra = {}): HttpProblem {
  return new HttpProblem(413, detail, { code: ErrorCode.PAYLOAD_TOO_LARGE, ...extra });
}

export function unprocessableEntity(detail = 'Unprocessable Entity', extra: Extra = {}): HttpProblem {
  return new HttpProblem(422, detail, { code: ErrorCode.UNPROCESSABLE_ENTITY, ...extra });
}

export function internal(detail = 'An unexpected error occurred', extra: Extra = {}): HttpProblem {
  return new HttpProblem(500, detail, { code: ErrorCode.INTERNAL_ERROR, ...extra });
}

export function notImplemented(detail = 'Not Implemented', extra: Extra = {}): HttpProblem {
  return new HttpProblem(501, detail, { code: ErrorCode.NOT_IMPLEMENTED, ...extra });
}

export function serviceUnavailable(detail = 'Service Unavailable', extra: Extra = {}): HttpProblem {
  return new HttpProblem(503, detail, { code: ErrorCode.SERVICE_UNAVAILABLE, ...extra });
}

/** @rateLimit exhausted; the Go runtime answers the same status and title. `retryAfterSeconds` travels as `details`. */
export function tooManyRequests(retryAfterSeconds: number, detail = 'Too Many Requests', extra: Extra = {}): HttpProblem {
  return new HttpProblem(429, detail, { code: ErrorCode.TOO_MANY_REQUESTS, details: { retryAfterSeconds }, ...extra });
}

/** @timeout elapsed before the operation answered; the Go runtime's Timeout middleware answers the same status. */
export function gatewayTimeout(detail = 'Gateway Timeout', extra: Extra = {}): HttpProblem {
  return new HttpProblem(504, detail, { code: ErrorCode.GATEWAY_TIMEOUT, ...extra });
}
