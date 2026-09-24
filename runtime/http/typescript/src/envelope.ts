/*
The success envelope `{data, meta: {requestId}}`. The generated Go,
Rust, and TypeScript SDKs refuse a success body without it, so every generated
route runs its result through here.
*/

export interface EnvelopeMeta {
  requestId: string;
}

export interface Envelope<T> {
  data: T;
  meta: EnvelopeMeta;
}

/**
 * An implementation returns its view directly for a 200, or wraps it here to
 * choose the status (a readiness probe answers 503 with its view) or add
 * response headers. The body is still the envelope.
 */
export class OperationResult<T> {
  constructor(
    readonly data: T,
    readonly status: number = 200,
    readonly headers: Record<string, string> = {}
  ) {}
}

export function envelope<T>(data: T, requestId: string): Envelope<T> {
  return { data, meta: { requestId } };
}

export function envelopeResponse<T>(
  data: T,
  requestId: string,
  status = 200,
  headers: Record<string, string> = {}
): Response {
  return new Response(JSON.stringify(envelope(data, requestId)), {
    status,
    headers: {
      'content-type': 'application/json',
      'cache-control': 'no-store',
      ...headers,
      'x-request-id': requestId,
    },
  });
}

const MAX_REQUEST_ID_LENGTH = 128;

/**
 * The caller's X-Request-Id when it supplied a sane one, otherwise a fresh
 * UUID so traces still correlate (the Rust runtime does the same).
 */
export function requestIdOf(source: Request | Headers): string {
  const headers = source instanceof Headers ? source : source.headers;
  const supplied = headers.get('x-request-id')?.trim();
  if (supplied && supplied.length <= MAX_REQUEST_ID_LENGTH && !/[\r\n]/u.test(supplied)) {
    return supplied;
  }
  return crypto.randomUUID();
}
