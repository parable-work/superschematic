import type { RequestContext } from './operation';

/*
@rateLimit, with the Go runtime's key semantics: one limiter per route, keyed
by the client IP (middleware.RateLimit over httprate.LimitBy(keyByClientIP)).
The adapter composes the key as `<operation>:<client ip>` so one store can
serve every operation, and refuses with 429 Too Many Requests.

The default store is a per-process token bucket. A deployment with several
replicas that must share a budget supplies a RateLimitStore backed by
something shared (Redis, say); the interface is the one method the adapter
calls.
*/

export interface RateLimitDecision {
  readonly allowed: boolean;
  /** Whole seconds until a token is available again; 0 when allowed. Becomes the Retry-After header. */
  readonly retryAfterSeconds: number;
}

export interface RateLimitStore {
  /**
   * Takes one token from `key`'s bucket, sized `limitPerMinute` and refilled
   * at that rate. `now` is the adapter's clock in milliseconds. A store may
   * answer synchronously or with a promise.
   */
  take(key: string, limitPerMinute: number, now: number): RateLimitDecision | Promise<RateLimitDecision>;
}

export interface RateLimitOptions {
  /** Where buckets live; a MemoryRateLimitStore per router by default. */
  readonly store?: RateLimitStore;
  /**
   * The bucket a request draws from within its operation; the client IP by
   * default (X-Forwarded-For's first hop, X-Real-IP, then the socket), which
   * is the Go runtime's key. Return the principal's subject to limit per
   * caller instead; the operation name is always prepended.
   */
  readonly keyOf?: (ctx: RequestContext) => string;
  /** Clock in milliseconds, for tests. */
  readonly now?: () => number;
}

interface Bucket {
  tokens: number;
  refilledAt: number;
}

/** Token buckets in process memory: `limitPerMinute` capacity, refilled continuously at that rate. */
export class MemoryRateLimitStore implements RateLimitStore {
  private readonly buckets = new Map<string, Bucket>();

  /** `maxKeys` bounds memory: past it, buckets idle for a minute (full again) are dropped. */
  constructor(private readonly maxKeys = 10_000) {}

  take(key: string, limitPerMinute: number, now: number): RateLimitDecision {
    if (!(limitPerMinute >= 1)) return { allowed: false, retryAfterSeconds: 60 };
    const perMs = limitPerMinute / 60_000;
    let bucket = this.buckets.get(key);
    if (!bucket) {
      if (this.buckets.size >= this.maxKeys) this.sweep(now);
      bucket = { tokens: limitPerMinute, refilledAt: now };
      this.buckets.set(key, bucket);
    } else if (now > bucket.refilledAt) {
      bucket.tokens = Math.min(limitPerMinute, bucket.tokens + (now - bucket.refilledAt) * perMs);
      bucket.refilledAt = now;
    }
    if (bucket.tokens >= 1) {
      bucket.tokens -= 1;
      return { allowed: true, retryAfterSeconds: 0 };
    }
    return { allowed: false, retryAfterSeconds: Math.max(1, Math.ceil((1 - bucket.tokens) / perMs / 1000)) };
  }

  /** Buckets currently tracked, for tests and diagnostics. */
  get size(): number {
    return this.buckets.size;
  }

  private sweep(now: number): void {
    for (const [key, bucket] of this.buckets) {
      if (now - bucket.refilledAt >= 60_000) this.buckets.delete(key);
    }
  }
}

/**
 * The client IP the Go runtime's rate limiter keys by: the first
 * X-Forwarded-For hop, then X-Real-IP, then the transport's peer address.
 */
export function clientIpOf(headers: Headers, remoteAddress?: string): string | undefined {
  const forwarded = headers.get('x-forwarded-for');
  if (forwarded) {
    const first = forwarded.split(',')[0]?.trim();
    if (first) return first;
  }
  const real = headers.get('x-real-ip')?.trim();
  if (real) return real;
  const remote = remoteAddress?.trim();
  return remote ? remote : undefined;
}

/** The default bucket key: the client IP, `unknown` when the transport reports none. */
export function clientIpKey(ctx: RequestContext): string {
  return ctx.clientIp ?? 'unknown';
}
