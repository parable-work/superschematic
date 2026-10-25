import { MemoryStoreUnavailableError, memoryStoreName, type MemoryStore, type Ref } from './memorystore.js';
import { MemoryRateLimitStore, type RateLimitDecision, type RateLimitStore } from './ratelimit.js';
import { sha256 } from './sha256.js';

/*
The shared rate limit (D75): an API whose config names a rateLimitStore
counts its @rateLimit routes in that memory store, so every replica draws
on one budget. The rule is the Go runtime's limiter's, httprate's sliding
window: the previous window's count, weighted by the part of it still in
the window, plus this window's. The counters are the reserved rate-limit
declaration's keys, one per service, operation, window and client, whose
client part is the SHA-256 of the limiter's key, so an address is no part
of a name. While the store is unavailable, or a call to it fails, a
request is limited by the in-process store the route had before: each
replica's own budget.
*/

/** The reserved declaration the shared rate limit counts in. */
export const RATE_LIMIT_DECLARATION = 'rate-limit';

/** What a rate-limit counter's TTL adds to two windows: a counter lives 2W + 5s. */
export const RATE_LIMIT_SLACK_MS = 5_000;

/** The window of a @rateLimit, whose limit is per minute. */
export const RATE_LIMIT_WINDOW_MS = 60_000;

/** One decision of the sliding window. */
export interface RateLimitWindowDecision {
  readonly allowed: boolean;
  /** prevCount * (windowMs - elapsed) / windowMs + currCount. */
  readonly estimate: number;
  /** The start of the window now falls in, and of the one before it, in Unix seconds. */
  readonly currWindowStart: number;
  readonly prevWindowStart: number;
  /** The TTL the allowed request's increment of the current counter gives it, 0 when refused. */
  readonly incrementTtlMs: number;
  /** The Retry-After of a refusal, the window's length in whole seconds; 0 when allowed. */
  readonly retryAfterSeconds: number;
}

/** Rounds half away from zero, as Go's math.Round does. */
function round(x: number): number {
  return Math.sign(x) * Math.round(Math.abs(x));
}

/**
 * httprate's sliding window: with curr = floor(nowMs / windowMs) * windowMs
 * and elapsed = nowMs - curr, the estimate is prevCount * (windowMs -
 * elapsed) / windowMs + currCount, and a request is allowed when
 * round(estimate) + 1 <= limit.
 */
export function rateLimitWindow(limit: number, windowMs: number, nowMs: number, prevCount: number, currCount: number): RateLimitWindowDecision {
  const curr = Math.floor(nowMs / windowMs) * windowMs;
  const prev = curr - windowMs;
  const elapsed = nowMs - curr;
  const estimate = (prevCount * (windowMs - elapsed)) / windowMs + currCount;
  const allowed = round(estimate) + 1 <= limit;
  return {
    allowed,
    estimate,
    currWindowStart: Math.floor(curr / 1000),
    prevWindowStart: Math.floor(prev / 1000),
    incrementTtlMs: allowed ? 2 * windowMs + RATE_LIMIT_SLACK_MS : 0,
    retryAfterSeconds: allowed ? 0 : Math.floor(windowMs / 1000),
  };
}

/** The lowercase hex SHA-256 of s's UTF-8. */
function hexSha256(s: string): string {
  let out = '';
  for (const b of sha256(new TextEncoder().encode(s))) out += b.toString(16).padStart(2, '0');
  return out;
}

/**
 * The name, without the store's prefix, of the rate-limit counter of
 * service's operation, for the window starting at windowStart (Unix
 * seconds) and the client whose limiter key is clientKey. Throws
 * MemoryStoreNameError for a service or an operation that is no segment.
 */
export function rateLimitKeyName(service: string, operation: string, windowStart: number, clientKey: string): string {
  return memoryStoreName(RATE_LIMIT_DECLARATION, service, operation, String(windowStart), hexSha256(clientKey));
}

/** What a MemoryStoreRateLimitStore is built with. */
export interface MemoryStoreRateLimitOptions {
  /** The store a request is limited by while the memory store is unavailable or a call fails; an in-process MemoryRateLimitStore by default. */
  readonly fallback?: RateLimitStore;
  /** Called with a failed call to the memory store, before the request falls back. */
  readonly onError?: (error: unknown) => void;
}

/**
 * A RateLimitStore over a memory store (D75): the shared rate limit of
 * service. The router composes the key it takes as
 * `<namespace>.<name>:<client>`, and the operation part is
 * `<namespace>.<name>`, as the Rust runtime names it (`orders.placeOrder`);
 * neither holds a ':'.
 */
export class MemoryStoreRateLimitStore implements RateLimitStore {
  private readonly fallback: RateLimitStore;
  private readonly onError: (error: unknown) => void;

  constructor(
    private readonly store: MemoryStore,
    private readonly service: string,
    options: MemoryStoreRateLimitOptions = {}
  ) {
    this.fallback = options.fallback ?? new MemoryRateLimitStore();
    this.onError = options.onError ?? (() => undefined);
  }

  async take(key: string, limitPerMinute: number, now: number): Promise<RateLimitDecision> {
    if (!this.store.available()) return this.fallback.take(key, limitPerMinute, now);
    try {
      return await this.shared(key, limitPerMinute, now);
    } catch (error) {
      if (!(error instanceof MemoryStoreUnavailableError)) this.onError(error);
      return this.fallback.take(key, limitPerMinute, now);
    }
  }

  private async shared(key: string, limit: number, now: number): Promise<RateLimitDecision> {
    const colon = key.indexOf(':');
    const operation = colon < 0 ? key : key.slice(0, colon);
    const client = colon < 0 ? '' : key.slice(colon + 1);
    const windowMs = RATE_LIMIT_WINDOW_MS;
    const curr = Math.floor(now / windowMs) * windowMs;
    const ref = (start: number): Ref => ({
      declaration: RATE_LIMIT_DECLARATION,
      name: rateLimitKeyName(this.service, operation, Math.floor(start / 1000), client),
    });
    const currRef = ref(curr);
    const [prevCount, currCount] = await Promise.all([this.store.count(ref(curr - windowMs)), this.store.count(currRef)]);
    const decision = rateLimitWindow(limit, windowMs, now, prevCount, currCount);
    if (!decision.allowed) return { allowed: false, retryAfterSeconds: decision.retryAfterSeconds };
    await this.store.increment(currRef, decision.incrementTtlMs);
    return { allowed: true, retryAfterSeconds: 0 };
  }
}

/** The shared rate limit of service over store, falling back to an in-process store of its own. */
export function memoryStoreRateLimitStore(store: MemoryStore, service: string, options?: MemoryStoreRateLimitOptions): MemoryStoreRateLimitStore {
  return new MemoryStoreRateLimitStore(store, service, options);
}
