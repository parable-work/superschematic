/*
The memory store (D75): a Redis or Valkey server that the replicas of a
stack's servers, jobs and workers share, and whose loss costs a sample,
never a record. An API's Deps holds one MemoryStore per store its config
lists; the store's generated definitions (memorystoregen) build each name
from typed parts, so a caller never formats one.

This module is the provider-neutral half: the MemoryStore interface the
Deps field holds, its errors, the names, a disabled store and an
in-process store for tests and for a process alone, and the typed layer
the generated definitions drive (Channel, Signal, Key). It imports nothing
from node:, so a Workers bundle holds it; the Redis implementation is the
./redis entry, which only a server whose APIs list a store depends on.

Three rules hold for every implementation:
  - A disabled, closed or unreachable store refuses every call at once with
    MemoryStoreUnavailableError, and the caller takes the path it had
    before the store existed.
  - Every key expires: Increment sets a TTL on the write that creates the
    key, and Claim is a set-if-absent with its TTL.
  - A message is at most MAX_MESSAGE_BYTES encoded, refused before it is
    sent.

The store prepends its prefix to every name: `<store>` or, in a member of a
parameterized environment, `<store>@<member>`, then ':' and the name a Ref
holds.
*/

/** The most bytes one encoded message of a channel holds: 64 KiB. */
export const MAX_MESSAGE_BYTES = 64 * 1024;

/** The most bytes a segment holds: a store's name, a declaration's name and each part's value. */
export const MAX_SEGMENT_BYTES = 128;

/** The longest TTL a key may have: seven days, in milliseconds. */
export const MAX_TTL_MS = 7 * 24 * 60 * 60 * 1000;

/**
 * A channel or a key of a store: the name of its declaration, which a
 * metric or a log line may carry, and its name without the store's prefix
 * (`stock-changed:3f2a…`), which none does.
 */
export interface Ref {
  readonly declaration: string;
  readonly name: string;
}

/** A handler of a channel's raw messages. A rejection or a throw is logged by the store and the message dropped. */
export type MessageHandler = (data: Uint8Array) => void | Promise<void>;

/** A subscription to a channel, which unsubscribe ends. */
export interface Subscription {
  /** Stops delivering the channel's messages to this subscription's handler. Resolves once more than once too. */
  unsubscribe(): Promise<void>;
}

/**
 * A memory store, as an API's Deps holds it (D75). Use it through the
 * store's generated definitions, which build each Ref from typed parts:
 * `shopCache.stockChanged.for(productId).publish(deps.shopCache, signal)`.
 */
export interface MemoryStore {
  /** The prefix of every name the store holds: `<store>` or `<store>@<member>`. */
  readonly name: string;

  /** Whether the store is connected. False for a disabled or closed store. */
  available(): boolean;

  /** Publishes data on the channel ref. Throws MemoryStoreUnavailableError while unavailable, and RangeError past MAX_MESSAGE_BYTES. */
  publish(ref: Ref, data: Uint8Array): Promise<void>;

  /**
   * Delivers each message of the channel ref to handler until the
   * subscription ends. Throws MemoryStoreUnavailableError for a disabled or
   * closed store. While a store that is open is down, the subscription holds
   * and the store subscribes again once it reconnects; messages published
   * while it was down are lost.
   */
  subscribe(ref: Ref, handler: MessageHandler): Promise<Subscription>;

  /** Adds one to the counter ref and returns the new count; the write that creates the key sets its TTL of ttlMs. */
  increment(ref: Ref, ttlMs: number): Promise<number>;

  /** The counter ref's count, 0 when it is absent. */
  count(ref: Ref): Promise<number>;

  /** Sets the claim ref for ttlMs unless it is set: true when this call took it. */
  claim(ref: Ref, ttlMs: number): Promise<boolean>;

  /** Deletes the key ref: a claim given back, or a counter reset. */
  delete(ref: Ref): Promise<void>;

  /** Closes the store; every call after it throws MemoryStoreUnavailableError. Safe to call more than once. */
  close(): Promise<void>;
}

/**
 * The store cannot take the call: it is disabled, closed or not connected,
 * or the call failed on its connection. The caller takes its fallback
 * path.
 */
export class MemoryStoreUnavailableError extends Error {
  /** The store's name, its prefix. */
  readonly store: string;

  constructor(store: string, reason = 'unavailable', options?: { cause?: unknown }) {
    super(`memory store ${store} is ${reason}`, options);
    this.name = 'MemoryStoreUnavailableError';
    this.store = store;
  }
}

/** A name, or a part of one, that is no segment: refused, never escaped. */
export class MemoryStoreNameError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'MemoryStoreNameError';
  }
}

/** The bytes no segment holds, besides the control bytes. */
const REFUSED = new Set([...':*?[]@\\'].map(c => c.charCodeAt(0)));

const encoder = new TextEncoder();

/**
 * Why s is no segment, or undefined when it is one: 1 to 128 bytes of
 * UTF-8, none a control byte (below 0x20, or 0x7f) or one of
 * `: * ? [ ] @ \`.
 */
export function segmentProblem(s: string): string | undefined {
  if (typeof s !== 'string') return `a segment is a string, not ${typeof s}`;
  const bytes = encoder.encode(s);
  if (bytes.length === 0) return `a segment is empty; it holds 1 to ${MAX_SEGMENT_BYTES} bytes`;
  if (bytes.length > MAX_SEGMENT_BYTES) return `a segment is ${bytes.length} bytes; it holds at most ${MAX_SEGMENT_BYTES}`;
  for (let i = 0; i < bytes.length; i++) {
    const b = bytes[i]!;
    if (b < 0x20 || b === 0x7f) return `a segment holds the control byte 0x${b.toString(16).padStart(2, '0')} at ${i}`;
    if (REFUSED.has(b)) return `a segment holds ${JSON.stringify(String.fromCharCode(b))} at ${i}; a segment has no ':', '*', '?', '[', ']', '@' or '\\'`;
  }
  return undefined;
}

/**
 * The name of declaration with parts, each already its wire string: the
 * declaration, then ':' and each part. Throws MemoryStoreNameError for a
 * declaration or a part that is no segment.
 */
export function memoryStoreName(declaration: string, ...parts: readonly string[]): string {
  const problem = segmentProblem(declaration);
  if (problem) throw new MemoryStoreNameError(`declaration ${JSON.stringify(declaration)}: ${problem}`);
  parts.forEach((part, i) => {
    const p = segmentProblem(part);
    if (p) throw new MemoryStoreNameError(`${declaration} part ${i}: ${p}`);
  });
  return parts.length === 0 ? declaration : `${declaration}:${parts.join(':')}`;
}

/** The prefix of a store's names: store, or `store@member`. Throws MemoryStoreNameError for one that is no segment. */
export function memoryStorePrefix(store: string, member?: string): string {
  const problem = segmentProblem(store);
  if (problem) throw new MemoryStoreNameError(`store ${JSON.stringify(store)}: ${problem}`);
  if (member === undefined || member === '') return store;
  const p = segmentProblem(member);
  if (p) throw new MemoryStoreNameError(`member ${JSON.stringify(member)}: ${p}`);
  return `${store}@${member}`;
}

/** Throws RangeError for a TTL that is no whole number of milliseconds from 1 to MAX_TTL_MS. */
export function checkTtl(ttlMs: number, maxMs = MAX_TTL_MS): void {
  if (!Number.isInteger(ttlMs) || ttlMs < 1 || ttlMs > maxMs) {
    throw new RangeError(`a TTL of ${ttlMs} ms is not a whole number of milliseconds from 1 to ${maxMs}`);
  }
}

/** Throws RangeError for a message past MAX_MESSAGE_BYTES. */
export function checkMessage(data: Uint8Array): void {
  if (data.byteLength > MAX_MESSAGE_BYTES) {
    throw new RangeError(`a message of ${data.byteLength} bytes is refused; a memory store message holds at most ${MAX_MESSAGE_BYTES}`);
  }
}

/** A disabled store: available is false, and every call throws MemoryStoreUnavailableError. */
export function disabledMemoryStore(name: string): MemoryStore {
  const refuse = (): Promise<never> => Promise.reject(new MemoryStoreUnavailableError(name, 'disabled'));
  return {
    name,
    available: () => false,
    publish: refuse,
    subscribe: refuse,
    increment: refuse,
    count: refuse,
    claim: refuse,
    delete: refuse,
    close: () => Promise.resolve(),
  };
}

/** What an InProcessMemoryStore is built with. */
export interface InProcessMemoryStoreOptions {
  /** The clock in milliseconds, which a test sets to expire keys; Date.now by default. */
  readonly now?: () => number;
  /** Called with a handler's failure, which drops the message. */
  readonly onHandlerError?: (error: unknown, ref: Ref) => void;
}

interface Entry {
  value: number;
  expiresAt: number;
}

/**
 * A memory store in process memory, with the store's semantics: keys
 * expire against the clock (options.now), and a message published reaches
 * every subscription of the channel in this process, after publish
 * resolves. setAvailable(false) stands for an outage: every call throws
 * MemoryStoreUnavailableError until setAvailable(true), and a subscription
 * holds through it.
 */
export class InProcessMemoryStore implements MemoryStore {
  readonly name: string;
  private readonly now: () => number;
  private readonly onHandlerError: (error: unknown, ref: Ref) => void;
  private readonly keys = new Map<string, Entry>();
  private readonly channels = new Map<string, Set<{ ref: Ref; handler: MessageHandler }>>();
  private up = true;
  private closed = false;

  constructor(name: string, options: InProcessMemoryStoreOptions = {}) {
    this.name = name;
    this.now = options.now ?? Date.now;
    this.onHandlerError = options.onHandlerError ?? (() => undefined);
  }

  available(): boolean {
    return this.up && !this.closed;
  }

  /** Stands for the store going down (false) or coming back (true). */
  setAvailable(up: boolean): void {
    this.up = up;
  }

  async publish(ref: Ref, data: Uint8Array): Promise<void> {
    checkMessage(data);
    this.check();
    const subs = this.channels.get(this.full(ref));
    if (!subs) return;
    const copy = data.slice();
    for (const sub of [...subs]) {
      queueMicrotask(() => {
        if (!subs.has(sub)) return;
        try {
          Promise.resolve(sub.handler(copy.slice())).catch(error => this.onHandlerError(error, sub.ref));
        } catch (error) {
          this.onHandlerError(error, sub.ref);
        }
      });
    }
  }

  async subscribe(ref: Ref, handler: MessageHandler): Promise<Subscription> {
    if (this.closed) throw new MemoryStoreUnavailableError(this.name, 'closed');
    const name = this.full(ref);
    let subs = this.channels.get(name);
    if (!subs) {
      subs = new Set();
      this.channels.set(name, subs);
    }
    const sub = { ref, handler };
    subs.add(sub);
    return {
      unsubscribe: async () => {
        subs.delete(sub);
        if (subs.size === 0 && this.channels.get(name) === subs) this.channels.delete(name);
      },
    };
  }

  async increment(ref: Ref, ttlMs: number): Promise<number> {
    checkTtl(ttlMs);
    this.check();
    const name = this.full(ref);
    const entry = this.live(name);
    if (entry) {
      entry.value += 1;
      return entry.value;
    }
    this.keys.set(name, { value: 1, expiresAt: this.now() + ttlMs });
    return 1;
  }

  async count(ref: Ref): Promise<number> {
    this.check();
    return this.live(this.full(ref))?.value ?? 0;
  }

  async claim(ref: Ref, ttlMs: number): Promise<boolean> {
    checkTtl(ttlMs);
    this.check();
    const name = this.full(ref);
    if (this.live(name)) return false;
    this.keys.set(name, { value: 1, expiresAt: this.now() + ttlMs });
    return true;
  }

  async delete(ref: Ref): Promise<void> {
    this.check();
    this.keys.delete(this.full(ref));
  }

  async close(): Promise<void> {
    this.closed = true;
    this.channels.clear();
    this.keys.clear();
  }

  /** The milliseconds the key ref has left, or undefined when it is absent: what PTTL answers, for tests. */
  ttlOf(ref: Ref): number | undefined {
    const entry = this.live(this.full(ref));
    return entry ? entry.expiresAt - this.now() : undefined;
  }

  private full(ref: Ref): string {
    return `${this.name}:${ref.name}`;
  }

  private live(name: string): Entry | undefined {
    const entry = this.keys.get(name);
    if (entry && entry.expiresAt <= this.now()) {
      this.keys.delete(name);
      return undefined;
    }
    return entry;
  }

  private check(): void {
    if (this.closed) throw new MemoryStoreUnavailableError(this.name, 'closed');
    if (!this.up) throw new MemoryStoreUnavailableError(this.name, 'not connected');
  }
}

/**
 * The message of a signal channel, whose name is the whole message: it
 * encodes as `{}`, and any message decodes to it.
 */
export type Signal = Readonly<Record<string, never>>;

/** The one Signal value. */
export const signal: Signal = Object.freeze({});

/** How a channel's messages encode and decode. decode throws for a message that is not one. */
export interface MessageCodec<T> {
  encode(message: T): Uint8Array;
  decode(data: Uint8Array): T;
}

/** What Channel.subscribe takes beside the handler. */
export interface ChannelSubscribeOptions {
  /** Called for a message that does not decode, which is dropped and counted (Channel.decodeFailures). */
  readonly onDecodeFailure?: (error: unknown) => void;
}

const decoder = new TextDecoder('utf-8', { fatal: true });

/** The codec of a signal channel. */
export const signalCodec: MessageCodec<Signal> = {
  encode: () => encoder.encode('{}'),
  decode: () => signal,
};

/**
 * The codec of a JSON channel: a message is the UTF-8 JSON of the payload,
 * and parse turns the parsed JSON into T, throwing for a value that is not
 * one.
 */
export function jsonCodec<T>(parse: (value: unknown) => T): MessageCodec<T> {
  return {
    encode: message => encoder.encode(JSON.stringify(message)),
    decode: data => parse(JSON.parse(decoder.decode(data)) as unknown),
  };
}

/**
 * One channel of a store, with its message type: what a generated
 * definition's `for` returns. A message that does not decode is dropped and
 * counted, never delivered.
 */
export class Channel<T> {
  /** The messages this channel's subscriptions dropped since it was built: each did not decode. */
  decodeFailures = 0;

  constructor(
    readonly ref: Ref,
    private readonly codec: MessageCodec<T>
  ) {}

  /** Publishes message. Throws MemoryStoreUnavailableError while the store is unavailable, RangeError past MAX_MESSAGE_BYTES. */
  publish(store: MemoryStore, message: T): Promise<void> {
    return store.publish(this.ref, this.codec.encode(message));
  }

  /** Delivers each message that decodes to handler until the subscription ends. */
  subscribe(store: MemoryStore, handler: (message: T) => void | Promise<void>, options: ChannelSubscribeOptions = {}): Promise<Subscription> {
    return store.subscribe(this.ref, data => {
      let message: T;
      try {
        message = this.codec.decode(data);
      } catch (error) {
        this.decodeFailures++;
        options.onDecodeFailure?.(error);
        return;
      }
      return handler(message);
    });
  }
}

/** A channel whose messages are signals. */
export function signalChannel(ref: Ref): Channel<Signal> {
  return new Channel(ref, signalCodec);
}

/**
 * One key of a store, with its TTL: what a generated definition's `for` or
 * `forTTL` returns. A counter counts (increment, count) and a claim lets one
 * caller do a piece of work once (claim, delete).
 */
export class Key {
  constructor(
    readonly ref: Ref,
    /** The key's TTL in milliseconds, from the write that creates it. */
    readonly ttlMs: number
  ) {
    checkTtl(ttlMs);
  }

  /** Adds one and returns the new count. */
  increment(store: MemoryStore): Promise<number> {
    return store.increment(this.ref, this.ttlMs);
  }

  /** The count, 0 when the key is absent. */
  count(store: MemoryStore): Promise<number> {
    return store.count(this.ref);
  }

  /** Takes the claim for the TTL: true when this call took it. */
  claim(store: MemoryStore): Promise<boolean> {
    return store.claim(this.ref, this.ttlMs);
  }

  /** Deletes the key: gives a claim back, or resets a counter. */
  delete(store: MemoryStore): Promise<void> {
    return store.delete(this.ref);
  }
}
