/*
The value store (runtime/engine/README.md, "The value store"): a
top-level member of an instance, an event or a behavior's row longer than
the threshold is kept once, by the hash of its canonical JSON, and an
event or a read with valueRefs carries its ref, which engine.values.get
reads. The values live in the engine's own storage (engine_payloads).
*/

/** The member of a ref that names its value's hash. */
export const VALUE_REF_KEY = '$value';

/** What a row, an event or a behavior's row keeps in place of a large member. */
export interface ValueRef {
  /** The SHA-256 of the value's canonical JSON (RFC 8785), lowercase hex. */
  readonly $value: string;
  /** The length of the value's canonical JSON in UTF-8 bytes. */
  readonly bytes: number;
}

/** The threshold a member's JSON must exceed to be stored by hash: 64 KiB. */
export const DEFAULT_VALUE_THRESHOLD = 64 * 1024;

/** The least threshold an engine takes: 1 KiB. */
export const MIN_VALUE_THRESHOLD = 1024;

/** A value's hash: 64 lowercase hex digits. */
export const VALUE_HASH = /^[0-9a-f]{64}$/;

/** EngineOptions.values. */
export interface ValueOptions {
  /**
   * A member whose JSON is longer than this many UTF-8 bytes is stored by
   * hash: 65536 by default, at least 1024.
   */
  thresholdBytes?: number;
  /**
   * How many bytes of values, by their canonical JSON, the engine keeps
   * parsed in memory, the ones read last: 32 MiB by default, 0 for none.
   * A value never changes under its hash, so nothing invalidates them.
   */
  cacheBytes?: number;
  /**
   * The longest value the engine stores, in UTF-8 bytes of its canonical
   * JSON: 16 MiB by default, at least thresholdBytes. A write that would
   * store a longer top-level member, in an instance's row, an event or a
   * behavior's row, is refused (ValueTooLargeError, value_too_large).
   */
  maxBytes?: number;
}

/** The default of ValueOptions.cacheBytes: 32 MiB. */
export const DEFAULT_VALUE_CACHE_BYTES = 32 * 1024 * 1024;

/** The default of ValueOptions.maxBytes: 16 MiB. */
export const DEFAULT_VALUE_MAX_BYTES = 16 * 1024 * 1024;

/** A value read by its hash. */
export interface StoredValue {
  readonly hash: string;
  readonly bytes: number;
  readonly value: unknown;
}

/** Where a read of a value looks: a namespace, `default` when absent. */
export interface ValueTarget {
  namespace?: string;
}
