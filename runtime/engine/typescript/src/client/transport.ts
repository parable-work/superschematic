/*
One request to the engine's HTTP API, with the credentials the client
holds and the SDKs' retry rule (D15, amended; D37):

- The end user's token goes in Authorization: the token the client holds
  (the configured one, one setToken gave, or the last refresh's), else
  what getToken returns. A call that forwards an end user sends that
  user's token, or no Authorization when the user has none, and never
  refreshes.
- The calling service's token goes in Service-Authorization (or the
  headers its config names) on every request.
- A 401 whose problem code is `service_unauthorized` asks the service
  source for a fresh token once and retries; the end-user refresh cannot
  help, so it does not run. Any other 401 runs the end-user refresh once,
  shared by concurrent calls, and never asks the service source.

Nothing here imports a module of the engine's server side or of Node.js,
so the client runs in a browser or a worker runtime.
*/

import { EngineTransportError, problemFrom, type EngineProblem } from './errors.js';

/** The fetch the client sends requests with. */
export type FetchLike = (input: string, init: RequestInit) => Promise<Response>;

/** The end user's credentials: a bearer token in Authorization. */
export interface EndUserAuth {
  /** The token the client holds until setToken, clearToken or a refresh replaces it. */
  readonly token?: string;
  /** Called on each request while the client holds no token. */
  readonly getToken?: () => string | null | undefined | Promise<string | null | undefined>;
  /** Called on a 401 that is not `service_unauthorized`, once per call; its token is held from then on. */
  readonly refreshToken?: () => Promise<string>;
}

/**
 * The calling service's credential (D37): the shape a generated SDK's
 * `serviceCredential` takes, so the HTTP runtime's sources
 * (`googleIdTokenSource`, `tokenFileSource`, `signedTokenSource`) fill it.
 */
export interface ServiceCredential {
  /** Returns the token; `fresh` asks for a new one rather than a cached one. */
  readonly token: (fresh: boolean) => Promise<string>;
  /** The headers that carry `Bearer <token>`; `Service-Authorization` by default. */
  readonly headers?: readonly string[];
}

/** The end user a call forwards from the request a server is serving: the HTTP runtime's RequestContext is one. */
export interface ForwardedUser {
  readonly bearerToken?: string | null;
}

/** What every call takes. */
export interface CallOptions {
  /** The namespace; the client's by default. */
  readonly namespace?: string;
  /** Aborts the call; its reason is what the call rejects with. */
  readonly signal?: AbortSignal;
  /** Forward this end user instead of the client's own credentials (D37). */
  readonly forward?: ForwardedUser;
}

export interface TransportOptions {
  readonly baseUrl: string;
  readonly auth?: EndUserAuth;
  readonly serviceCredential?: ServiceCredential;
  readonly fetch?: FetchLike;
  readonly timeoutMs?: number;
  readonly headers?: Readonly<Record<string, string>>;
}

/** One request: the path below the base URL, already encoded. */
export interface RequestSpec {
  readonly method: 'GET' | 'POST' | 'PATCH' | 'DELETE';
  readonly path: string;
  readonly query?: ReadonlyArray<readonly [string, string]>;
  /** A JSON body. */
  readonly body?: unknown;
  readonly contentType?: string;
  readonly accept?: string;
  readonly headers?: Readonly<Record<string, string>>;
  readonly options?: CallOptions;
  /** A streamed answer: the timeout covers the wait for its headers, not its body. */
  readonly stream?: boolean;
}

/** The SDKs' default timeout. */
export const DEFAULT_TIMEOUT_MS = 30_000;

/** The header a service credential travels in by default (D37). */
export const SERVICE_AUTHORIZATION_HEADER = 'Service-Authorization';

interface Attempt {
  readonly userRefreshed: boolean;
  readonly serviceRetried: boolean;
  readonly freshServiceToken: boolean;
}

const FIRST_ATTEMPT: Attempt = { userRefreshed: false, serviceRetried: false, freshServiceToken: false };

export class Transport {
  private readonly baseUrl: string;
  private readonly auth: EndUserAuth | undefined;
  private readonly service: ServiceCredential | undefined;
  private readonly fetchImpl: FetchLike;
  private readonly timeoutMs: number;
  private readonly headers: Readonly<Record<string, string>>;
  private token: string | undefined;
  private refreshing: Promise<string> | undefined;

  constructor(options: TransportOptions) {
    if (typeof options.baseUrl !== 'string' || options.baseUrl === '') {
      throw new TypeError('EngineClient: baseUrl is the URL the engine is mounted at');
    }
    const timeoutMs = options.timeoutMs ?? DEFAULT_TIMEOUT_MS;
    if (!Number.isFinite(timeoutMs) || timeoutMs < 0) {
      throw new TypeError(`EngineClient: timeoutMs is a number of milliseconds, 0 for none, got ${String(options.timeoutMs)}`);
    }
    const fetchImpl = options.fetch ?? (typeof globalThis.fetch === 'function' ? globalThis.fetch.bind(globalThis) : undefined);
    if (fetchImpl === undefined) {
      throw new TypeError('EngineClient: no fetch; pass options.fetch');
    }
    this.baseUrl = options.baseUrl.replace(/\/+$/u, '');
    this.auth = options.auth;
    this.service = options.serviceCredential;
    this.fetchImpl = fetchImpl;
    this.timeoutMs = timeoutMs;
    this.headers = options.headers ?? {};
    this.token = options.auth?.token;
  }

  setToken(token: string): void {
    this.token = token;
  }

  clearToken(): void {
    this.token = undefined;
  }

  /**
   * stream answers the response of a request that succeeded, after the
   * retries, with its body unread; a failure status throws its
   * EngineProblem.
   */
  async stream(spec: RequestSpec): Promise<Response> {
    return (await this.attempt({ ...spec, stream: true }, FIRST_ATTEMPT)).response;
  }

  /** json sends a request and unwraps the success envelope's `data`. */
  async json<T>(spec: RequestSpec): Promise<{ data: T; response: Response }> {
    const { response, text = '' } = await this.attempt({ ...spec, stream: false }, FIRST_ATTEMPT);
    let body: unknown;
    try {
      body = text === '' ? undefined : JSON.parse(text);
    } catch {
      throw new EngineTransportError(`${spec.method} ${spec.path} answered ${response.status} with a body that is not JSON`);
    }
    if (typeof body !== 'object' || body === null || !('data' in body) || !('meta' in body)) {
      throw new EngineTransportError(`${spec.method} ${spec.path} answered ${response.status} without the {data, meta} envelope`);
    }
    return { data: (body as { data: T }).data, response };
  }

  private async attempt(spec: RequestSpec, attempt: Attempt): Promise<{ response: Response; text?: string }> {
    const headers = new Headers(this.headers);
    headers.set('accept', spec.accept ?? 'application/json');
    for (const [name, value] of Object.entries(spec.headers ?? {})) {
      headers.set(name, value);
    }
    let body: string | undefined;
    if (spec.body !== undefined) {
      body = JSON.stringify(spec.body);
      headers.set('content-type', spec.contentType ?? 'application/json');
    }
    const forward = spec.options?.forward;
    try {
      if (forward !== undefined) {
        headers.delete('authorization');
        if (forward.bearerToken) {
          headers.set('authorization', `Bearer ${forward.bearerToken}`);
        }
      } else {
        const token = this.token ?? (await this.auth?.getToken?.()) ?? undefined;
        if (token) {
          headers.set('authorization', `Bearer ${token}`);
        }
      }
      if (this.service !== undefined) {
        const token = await this.service.token(attempt.freshServiceToken);
        for (const name of this.service.headers?.length ? this.service.headers : [SERVICE_AUTHORIZATION_HEADER]) {
          headers.delete(name);
          if (token) {
            headers.set(name, `Bearer ${token}`);
          }
        }
      }
    } catch (error) {
      throw new EngineTransportError(`getting a credential for ${spec.method} ${spec.path} failed: ${messageOf(error)}`, { cause: error });
    }

    const callerSignal = spec.options?.signal;
    callerSignal?.throwIfAborted();
    const timeout = this.timeoutMs > 0 ? new AbortController() : undefined;
    const timer = timeout !== undefined ? setTimeout(() => timeout.abort(), this.timeoutMs) : undefined;
    const signal = anySignal([callerSignal, timeout?.signal]);
    let response: Response;
    try {
      response = await this.fetchImpl(this.url(spec), { method: spec.method, headers, body, signal });
    } catch (error) {
      clearTimeout(timer);
      if (callerSignal?.aborted) {
        throw callerSignal.reason;
      }
      if (timeout?.signal.aborted) {
        throw new EngineTransportError(`${spec.method} ${spec.path} got no answer within ${this.timeoutMs} ms`, { cause: error, timedOut: true });
      }
      throw new EngineTransportError(`${spec.method} ${spec.path} failed: ${messageOf(error)}`, { cause: error });
    }
    if (response.ok && spec.stream) {
      clearTimeout(timer);
      return { response };
    }
    // A body is read under the same timeout as its headers.
    let text = '';
    try {
      text = await response.text();
    } catch (error) {
      throw abortedOr(callerSignal, timeout?.signal, error, `reading the ${response.status} answer of ${spec.method} ${spec.path}`);
    } finally {
      clearTimeout(timer);
    }
    if (response.ok) {
      return { response, text };
    }
    let parsed: unknown = text;
    try {
      parsed = text === '' ? undefined : JSON.parse(text);
    } catch {
      // A proxy's page: the problem is built from the status.
    }
    const retryAfter = retryAfterOf(response.headers.get('retry-after'));
    const problem: EngineProblem = problemFrom(response.status, response.statusText, parsed, retryAfter);

    if (response.status === 401 && problem.code === 'service_unauthorized') {
      // The service credential was refused: a fresh one, once. The
      // end-user refresh cannot help, so it does not run.
      if (this.service !== undefined && !attempt.serviceRetried) {
        return this.attempt(spec, { ...attempt, serviceRetried: true, freshServiceToken: true });
      }
      throw problem;
    }
    if (response.status === 401 && forward === undefined && !attempt.userRefreshed && this.auth?.refreshToken !== undefined) {
      try {
        await this.refresh();
      } catch {
        this.token = undefined;
        throw problem;
      }
      return this.attempt(spec, { ...attempt, userRefreshed: true, freshServiceToken: false });
    }
    throw problem;
  }

  // Concurrent calls that meet a 401 share one refresh.
  private refresh(): Promise<string> {
    const refreshToken = this.auth?.refreshToken;
    if (refreshToken === undefined) {
      return Promise.reject(new Error('no refreshToken'));
    }
    this.refreshing ??= refreshToken()
      .then((token) => {
        this.token = token;
        return token;
      })
      .finally(() => {
        this.refreshing = undefined;
      });
    return this.refreshing;
  }

  private url(spec: RequestSpec): string {
    const target = `${this.baseUrl}${spec.path}`;
    if (spec.query === undefined || spec.query.length === 0) {
      return target;
    }
    const search = new URLSearchParams();
    for (const [name, value] of spec.query) {
      search.append(name, value);
    }
    return `${target}?${search.toString()}`;
  }
}

/** anySignal is a signal that aborts when any of the given ones does. */
export function anySignal(signals: ReadonlyArray<AbortSignal | undefined>): AbortSignal | undefined {
  const present = signals.filter((signal): signal is AbortSignal => signal !== undefined);
  if (present.length <= 1) {
    return present[0];
  }
  const any = (AbortSignal as { any?: (signals: AbortSignal[]) => AbortSignal }).any;
  if (typeof any === 'function') {
    return any(present);
  }
  const merged = new AbortController();
  for (const signal of present) {
    if (signal.aborted) {
      merged.abort(signal.reason);
      break;
    }
    signal.addEventListener('abort', () => merged.abort(signal.reason), { once: true });
  }
  return merged.signal;
}

/** The sequence an ETag names: `"3"` is 3. */
export function seqOf(response: Response): number | undefined {
  const match = /^"([0-9]{1,15})"$/u.exec(response.headers.get('etag')?.trim() ?? '');
  return match ? Number(match[1]) : undefined;
}

function retryAfterOf(header: string | null): number | undefined {
  if (header === null || !/^[0-9]{1,9}$/u.test(header.trim())) {
    return undefined;
  }
  return Number(header.trim());
}

function abortedOr(signal: AbortSignal | undefined, timeout: AbortSignal | undefined, error: unknown, what: string): unknown {
  if (signal?.aborted) {
    return signal.reason;
  }
  return new EngineTransportError(`${what} failed: ${messageOf(error)}`, { cause: error, timedOut: timeout?.aborted ?? false });
}

function messageOf(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
