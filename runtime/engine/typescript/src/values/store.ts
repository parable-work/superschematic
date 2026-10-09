/*
The value store (D16, amended: a large value is stored once). A top-level
member of what the engine writes, an instance's own fields in its row, a
change in the event log, or an object a behavior keeps in its own tables,
whose JSON is longer than a threshold (64 KiB by default) is stored once,
under the SHA-256 of its canonical JSON (canonical.ts), and the row, the
event or the behavior's row keeps a ref in its place:

  { "$value": "<64 hex digits>", "bytes": <the canonical JSON's length> }

Beside each such object the engine keeps the JSON pointers of the members
that hold a ref (value_refs), so a value that happens to look like a ref is
never taken for one. Reading the object back puts each value in its place
(fill). An object with no member over the threshold is stored as it was,
with no pointers.

Where the values live is a driver (ValueDriver). The default keeps them in
the engine's own file, in engine_payloads, written in the transaction that
writes the row, so D16's one file holds everything. A driver over other
storage implements the same three synchronous calls. Its writes are
outside the file's transactions, so a rollback cannot undo them: once the
outermost transaction ends, committed or rolled back, the store removes
each value it wrote there that no holder references (Storage.
afterTransaction), and engine.values.sweep() removes what a crash left
between a write and that end, for a driver that lists its hashes (list).

No value is longer than maxBytes (16 MiB of canonical JSON by default):
a write that would store one is refused (ValueTooLargeError,
value_too_large).

Who holds a value is engine_payload_holders: one row per value per holder,
an instance's row, an event, or a row of a behavior's tables, by namespace
and schema. A write records the values its row holds and drops the ones it
no longer holds; the value whose last holder goes is removed, in that
transaction for a driver that takes part in it, after the commit for one
that does not, so a rolled-back write never loses a value. The holders also
say which schemas of a namespace reference a value, which is what a read
of it by hash is allowed against (values.ts).

A value never changes under its hash, so the store keeps the values it
read last parsed and deep-frozen in memory (cacheBytes, 32 MiB by
default), and the engine's own reads share them; what goes to a caller is
a copy. Values come back in canonical form: an object's members sorted.
*/

import { deepFreeze } from '../behaviors/json.js';
import { setMember } from '../instances/patch.js';
import type { Storage } from '../storage/storage.js';
import { ValueTooLargeError } from '../errors.js';
import { canonicalJSON, sha256Hex } from './canonical.js';

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

/**
 * Where the value store keeps values, by hash. Every call is synchronous:
 * the engine calls them inside its write transaction (D16).
 */
export interface ValueDriver {
  /**
   * True when write and remove run in the engine's transaction, so a
   * rollback undoes them: the default driver, over the engine's own file.
   * Otherwise the engine removes a value only after the commit that
   * dropped its last holder, and a rolled-back write can leave a value
   * behind that nothing holds.
   */
  readonly transactional?: boolean;
  /** The canonical JSON stored under a hash, or undefined. */
  read(hash: string): string | undefined;
  /** Stores a value's canonical JSON under its hash; a hash it has already is left as it is. */
  write(hash: string, json: string): void;
  /** Removes the value under a hash, which no holder references any more. */
  remove(hash: string): void;
  /**
   * The hashes it stores after `after`, in order, at most limit of them:
   * what engine.values.sweep() pages through to remove the values no
   * holder references. Optional; a driver without it is not swept.
   */
  list?(after: string, limit: number): string[];
}

/** EngineOptions.values. */
export interface ValueOptions {
  /**
   * A member whose JSON is longer than this many UTF-8 bytes is stored by
   * hash: 65536 by default, at least 1024.
   */
  thresholdBytes?: number;
  /** Where values live; engine_payloads in the engine's file by default. */
  driver?: ValueDriver;
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

/** How many hashes engine.values.sweep() reads from a driver at a time. */
const SWEEP_PAGE = 500;

/** The default driver: engine_payloads, in the engine's file and its transactions. */
export class SqliteValueDriver implements ValueDriver {
  readonly transactional = true;

  constructor(private readonly storage: Storage) {}

  read(hash: string): string | undefined {
    const row = this.storage.get('SELECT value FROM engine_payloads WHERE hash = ?', [hash]);
    return row ? String(row.value) : undefined;
  }

  write(hash: string, json: string): void {
    if (!this.storage.get('SELECT 1 AS stored FROM engine_payloads WHERE hash = ?', [hash])) {
      this.storage.run('INSERT INTO engine_payloads (hash, value, bytes) VALUES (?, ?, ?)', [hash, json, Buffer.byteLength(json, 'utf8')]);
    }
  }

  remove(hash: string): void {
    this.storage.run('DELETE FROM engine_payloads WHERE hash = ?', [hash]);
  }

  list(after: string, limit: number): string[] {
    return this.storage.all('SELECT hash FROM engine_payloads WHERE hash > ? ORDER BY hash LIMIT ?', [after, limit]).map((row) => String(row.hash));
  }
}

/** Who holds values: an instance's row, one of its events, or a row of a behavior's tables. */
export interface ValueHolder {
  readonly namespace: string;
  readonly schema: string;
  /** `instance` for an instance's row, `event` for an event, or the name of the behavior whose table holds it. */
  readonly holder: string;
  /** The instance's id; '' for a behavior's row that belongs to no instance. */
  readonly id: string;
  /** '' for an instance's row, an event's cursor, or the behavior's own key for its row. */
  readonly key: string;
}

/** An object as stored: each large member a ref, with the pointers to them and their hashes. */
export interface Stowed {
  readonly value: Record<string, unknown>;
  /** JSON pointers to the members that hold a ref. */
  readonly refs: readonly string[];
  readonly hashes: ReadonlySet<string>;
}

/** A value read by its hash. */
export interface StoredValue {
  readonly hash: string;
  readonly bytes: number;
  readonly value: unknown;
}

const NO_REFS: ReadonlySet<string> = new Set();

export class ValueStore {
  readonly threshold: number;
  readonly maxBytes: number;
  readonly driver: ValueDriver;
  private readonly cache: ValueCache;
  // What a driver outside the file's transactions wrote in the
  // transaction under way, to remove at its end if nothing holds it.
  private written: Set<string> | undefined;

  constructor(
    private readonly storage: Storage,
    options: ValueOptions = {}
  ) {
    this.threshold = checkThreshold(options.thresholdBytes);
    this.maxBytes = checkMaxBytes(options.maxBytes, this.threshold);
    this.driver = checkDriver(options.driver) ?? new SqliteValueDriver(storage);
    this.cache = new ValueCache(checkCacheBytes(options.cacheBytes));
  }

  /**
   * stow returns an object as it is stored: each top-level member whose
   * JSON is longer than the threshold stored by hash and a ref in its
   * place. prefix is the pointer of the object in what is stored (an
   * operation's params are at /params); known lists the pointers of
   * members that already hold a ref, which are kept as they are; inline
   * names members kept as they are whatever their length: the fields an
   * index of the instance type covers, in an instance's row
   * (instances/indexes.ts).
   */
  stow(
    object: Readonly<Record<string, unknown>>,
    prefix = '',
    known: ReadonlySet<string> = NO_REFS,
    inline: ReadonlySet<string> = NO_REFS
  ): Stowed {
    const value: Record<string, unknown> = {};
    const refs: string[] = [];
    const hashes = new Set<string>();
    for (const [key, member] of Object.entries(object)) {
      if (member === undefined) {
        continue;
      }
      const at = `${prefix}${pointerToken(key)}`;
      if (known.has(at)) {
        const hash = refHash(member);
        if (hash === undefined) {
          throw new Error(`value store: ${at} is listed as a ref and holds none`);
        }
        setMember(value, key, member);
        refs.push(at);
        hashes.add(hash);
        continue;
      }
      if (!this.over(member)) {
        setMember(value, key, member);
        continue;
      }
      const json = canonicalJSON(member);
      const bytes = Buffer.byteLength(json, 'utf8');
      if (bytes > this.maxBytes) {
        throw new ValueTooLargeError(at, bytes, this.maxBytes);
      }
      if (inline.has(key)) {
        setMember(value, key, member);
        continue;
      }
      const hash = sha256Hex(json);
      this.write(hash, json);
      setMember(value, key, { [VALUE_REF_KEY]: hash, bytes } satisfies ValueRef);
      refs.push(at);
      hashes.add(hash);
    }
    return { value, refs, hashes };
  }

  /**
   * stowChange stows an instance as the log holds it, or a change of one:
   * a merge patch of what a read returns, { data, behaviors }. data's own
   * fields keep the refs known already (pointers from the change's root,
   * prefix included), and each behavior's fields go under its name, a
   * large one by hash as an own field is. With whole, the change is the
   * instance and holds both parts; otherwise a part that is undefined or
   * changes nothing is left out.
   */
  stowChange(
    prefix: string,
    data: Readonly<Record<string, unknown>> | undefined,
    behaviors: Readonly<Record<string, Readonly<Record<string, unknown>>>> | undefined,
    options: { readonly whole?: boolean; readonly known?: ReadonlySet<string> } = {}
  ): Stowed {
    const parts: Record<string, Stowed> = {};
    if (data !== undefined && (options.whole === true || Object.keys(data).length > 0)) {
      parts.data = this.stow(data, `${prefix}/data`, options.known);
    }
    if (behaviors !== undefined && (options.whole === true || Object.keys(behaviors).length > 0)) {
      const entries: Record<string, Stowed> = {};
      for (const [name, fields] of Object.entries(behaviors)) {
        entries[name] = this.stow(fields, `${prefix}/behaviors${pointerToken(name)}`, options.known);
      }
      parts.behaviors = joinStowed({}, entries);
    }
    return joinStowed({}, parts);
  }

  /**
   * hold records that a holder references exactly these values: it adds
   * the new ones and drops the ones it no longer holds, and removes a
   * dropped value nothing else holds. Call it in the write's transaction.
   */
  hold(holder: ValueHolder, hashes: ReadonlySet<string>): void {
    const held = this.storage
      .all(
        `SELECT hash FROM engine_payload_holders INDEXED BY engine_payload_holders_holder
         WHERE namespace = ? AND schema = ? AND holder = ? AND id = ? AND key = ?`,
        [holder.namespace, holder.schema, holder.holder, holder.id, holder.key]
      )
      .map((row) => String(row.hash));
    const dropped = held.filter((hash) => !hashes.has(hash));
    for (const hash of hashes) {
      if (!held.includes(hash)) {
        this.storage.run(
          'INSERT INTO engine_payload_holders (hash, namespace, schema, holder, id, key) VALUES (?, ?, ?, ?, ?, ?)',
          [hash, holder.namespace, holder.schema, holder.holder, holder.id, holder.key]
        );
      }
    }
    for (const hash of dropped) {
      this.storage.run(
        'DELETE FROM engine_payload_holders WHERE hash = ? AND namespace = ? AND schema = ? AND holder = ? AND id = ? AND key = ?',
        [hash, holder.namespace, holder.schema, holder.holder, holder.id, holder.key]
      );
    }
    this.collect(dropped);
  }

  /**
   * release drops every value a holder holds: its row's, or with key
   * undefined, every row's of that holder on the instance id. A value
   * nothing else holds is removed.
   */
  release(holder: Omit<ValueHolder, 'key'> & { readonly key?: string }): void {
    const where = 'namespace = ? AND schema = ? AND holder = ? AND id = ?' + (holder.key === undefined ? '' : ' AND key = ?');
    const params = [holder.namespace, holder.schema, holder.holder, holder.id, ...(holder.key === undefined ? [] : [holder.key])];
    const hashes = this.storage
      .all(`SELECT DISTINCT hash FROM engine_payload_holders INDEXED BY engine_payload_holders_holder WHERE ${where}`, params)
      .map((row) => String(row.hash));
    if (hashes.length === 0) {
      return;
    }
    this.storage.run(`DELETE FROM engine_payload_holders WHERE ${where}`, params);
    this.collect(hashes);
  }

  /**
   * fill returns a copy of a stored object with each ref its pointers name
   * replaced by its value. Only the objects along each pointer are copied.
   * The values are deep-frozen and shared with the cache, unless copy is
   * true: what the engine hands a caller, who may change it, is a copy.
   */
  fill<T>(stored: T, refs: readonly string[] | undefined, copy = false): T {
    if (refs === undefined || refs.length === 0) {
      return stored;
    }
    const top: Record<string, unknown> = { root: stored };
    const copied = new Set<object>();
    for (const at of refs) {
      let parent = top;
      let key = 'root';
      for (const token of parsePointer(at)) {
        const child = parent[key];
        if (typeof child !== 'object' || child === null || Array.isArray(child)) {
          throw new Error(`value store: ${at} does not name a member of the stored object`);
        }
        let next = child as Record<string, unknown>;
        if (!copied.has(next)) {
          next = { ...next };
          copied.add(next);
          setMember(parent, key, next);
        }
        parent = next;
        key = token;
      }
      const hash = refHash(parent[key]);
      if (hash === undefined) {
        throw new Error(`value store: ${at} holds no ref`);
      }
      const value = this.cached(hash);
      if (value === undefined) {
        throw new Error(`value store: no value ${hash}, which ${at} of a stored object refers to`);
      }
      setMember(parent, key, copy ? structuredClone(value.value) : value.value);
    }
    return top.root as T;
  }

  /** read returns the value stored under a hash, a copy the caller may change, or undefined. */
  read(hash: string): StoredValue | undefined {
    const value = this.cached(hash);
    return value === undefined ? undefined : { hash, bytes: value.bytes, value: structuredClone(value.value) };
  }

  /**
   * sweep removes every value the driver stores that no holder
   * references: what a crash left between a non-transactional driver's
   * write and the end of its transaction. It needs a driver that lists
   * its hashes (ValueDriver.list) and no open transaction, and returns how
   * many it removed. The default driver writes in the file's transactions
   * and leaves none.
   */
  sweep(): { removed: number } {
    if (typeof this.driver.list !== 'function') {
      throw new TypeError('values.sweep needs a driver that lists its hashes (ValueDriver.list)');
    }
    if (this.storage.inTransaction) {
      throw new Error('values.sweep runs outside any transaction');
    }
    let removed = 0;
    let after = '';
    for (;;) {
      const hashes = this.driver.list(after, SWEEP_PAGE);
      for (const hash of hashes) {
        if (!this.storage.get('SELECT 1 AS held FROM engine_payload_holders WHERE hash = ? LIMIT 1', [hash])) {
          this.driver.remove(hash);
          this.cache.delete(hash);
          removed += 1;
        }
      }
      if (hashes.length < SWEEP_PAGE) {
        return { removed };
      }
      after = hashes[hashes.length - 1];
    }
  }

  // write stores a value through the driver. A driver outside the file's
  // transactions keeps it whatever the transaction does, so the store
  // notes it, and once the outermost transaction ends removes it unless a
  // holder references it then: a write that rolled back leaves no value
  // behind.
  private write(hash: string, json: string): void {
    this.driver.write(hash, json);
    if (this.driver.transactional === true || !this.storage.inTransaction) {
      return;
    }
    if (this.written === undefined) {
      this.written = new Set();
      this.storage.afterTransaction(() => {
        const written = this.written ?? new Set<string>();
        this.written = undefined;
        for (const each of written) {
          if (!this.storage.get('SELECT 1 AS held FROM engine_payload_holders WHERE hash = ? LIMIT 1', [each])) {
            this.driver.remove(each);
            this.cache.delete(each);
          }
        }
      });
    }
    this.written.add(hash);
  }

  /** schemasHolding lists the schemas of a namespace whose rows, events or behaviors' rows hold a value, by name. */
  schemasHolding(hash: string, namespace: string): string[] {
    return this.storage
      .all('SELECT DISTINCT schema FROM engine_payload_holders WHERE hash = ? AND namespace = ? ORDER BY schema', [hash, namespace])
      .map((row) => String(row.schema));
  }

  // over reports whether a member's JSON is longer than the threshold:
  // at most 3 UTF-8 bytes to a UTF-16 code unit, at least 1.
  private over(member: unknown): boolean {
    if (typeof member !== 'object' && typeof member !== 'string') {
      return false;
    }
    const text = JSON.stringify(member);
    if (text.length > this.threshold) {
      return true;
    }
    return text.length * 3 > this.threshold && Buffer.byteLength(text, 'utf8') > this.threshold;
  }

  // cached is a value, parsed and deep-frozen, from the cache or the driver.
  private cached(hash: string): CachedValue | undefined {
    const hit = this.cache.get(hash);
    if (hit !== undefined) {
      return hit;
    }
    const json = this.driver.read(hash);
    if (json === undefined) {
      return undefined;
    }
    const value: CachedValue = { value: deepFreeze(JSON.parse(json) as unknown), bytes: Buffer.byteLength(json, 'utf8') };
    this.cache.set(hash, value);
    return value;
  }

  // collect removes each value no holder references any more: in the
  // write's transaction for a transactional driver, after its commit
  // otherwise, asking again then.
  private collect(hashes: readonly string[]): void {
    const unheld = (hash: string): boolean => !this.storage.get('SELECT 1 AS held FROM engine_payload_holders WHERE hash = ? LIMIT 1', [hash]);
    for (const hash of new Set(hashes)) {
      if (!unheld(hash)) {
        continue;
      }
      if (this.driver.transactional === true || !this.storage.inTransaction) {
        this.driver.remove(hash);
        this.cache.delete(hash);
      } else {
        this.storage.afterCommit(() => {
          if (unheld(hash)) {
            this.driver.remove(hash);
            this.cache.delete(hash);
          }
        });
      }
    }
  }
}

/** refHash is the hash a ref names, or undefined for a value that is no ref. */
export function refHash(value: unknown): string | undefined {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    return undefined;
  }
  const hash = (value as Record<string, unknown>)[VALUE_REF_KEY];
  return typeof hash === 'string' && VALUE_HASH.test(hash) ? hash : undefined;
}

/** refsText is the stored form of a list of pointers: JSON text, or null for none. */
export function refsText(refs: readonly string[]): string | null {
  return refs.length === 0 ? null : JSON.stringify(refs);
}

/** refsOf reads a value_refs column: the pointers, or undefined for none. */
export function refsOf(text: unknown): string[] | undefined {
  if (text === null || text === undefined) {
    return undefined;
  }
  const refs = JSON.parse(String(text)) as unknown;
  return Array.isArray(refs) && refs.length > 0 ? (refs as string[]) : undefined;
}

/** joinStowed puts stowed parts together under one object, at the keys given. */
export function joinStowed(base: Record<string, unknown>, parts: Readonly<Record<string, Stowed>>): Stowed {
  const value: Record<string, unknown> = { ...base };
  const refs: string[] = [];
  const hashes = new Set<string>();
  for (const [key, part] of Object.entries(parts)) {
    setMember(value, key, part.value);
    refs.push(...part.refs);
    for (const hash of part.hashes) {
      hashes.add(hash);
    }
  }
  return { value, refs, hashes };
}

// pointerToken is one JSON pointer token, its leading slash included.
function pointerToken(key: string): string {
  return `/${key.replace(/~/g, '~0').replace(/\//g, '~1')}`;
}

function parsePointer(at: string): string[] {
  if (!at.startsWith('/')) {
    throw new Error(`value store: ${at} is not a JSON pointer`);
  }
  return at
    .slice(1)
    .split('/')
    .map((token) => token.replace(/~1/g, '/').replace(/~0/g, '~'));
}

interface CachedValue {
  /** Deep-frozen. */
  readonly value: unknown;
  readonly bytes: number;
}

// ValueCache keeps the values read last, parsed, up to a total of bytes;
// a Map iterates in insertion order, so the first key is the oldest.
class ValueCache {
  private readonly values = new Map<string, CachedValue>();
  private size = 0;

  constructor(private readonly capacity: number) {}

  get(hash: string): CachedValue | undefined {
    const value = this.values.get(hash);
    if (value !== undefined) {
      this.values.delete(hash);
      this.values.set(hash, value);
    }
    return value;
  }

  set(hash: string, value: CachedValue): void {
    if (value.bytes > this.capacity) {
      return;
    }
    this.delete(hash);
    this.values.set(hash, value);
    this.size += value.bytes;
    for (const [oldest, cached] of this.values) {
      if (this.size <= this.capacity) {
        break;
      }
      this.values.delete(oldest);
      this.size -= cached.bytes;
    }
  }

  delete(hash: string): void {
    const value = this.values.get(hash);
    if (value !== undefined) {
      this.values.delete(hash);
      this.size -= value.bytes;
    }
  }
}

function checkDriver(driver: ValueDriver | undefined): ValueDriver | undefined {
  if (
    driver !== undefined &&
    (typeof driver !== 'object' ||
      driver === null ||
      typeof driver.read !== 'function' ||
      typeof driver.write !== 'function' ||
      typeof driver.remove !== 'function' ||
      (driver.list !== undefined && typeof driver.list !== 'function'))
  ) {
    throw new TypeError('values.driver is a ValueDriver: { read, write, remove, list? }');
  }
  return driver;
}

function checkCacheBytes(cacheBytes: number | undefined): number {
  if (cacheBytes === undefined) {
    return DEFAULT_VALUE_CACHE_BYTES;
  }
  if (!Number.isSafeInteger(cacheBytes) || cacheBytes < 0) {
    throw new TypeError(`values.cacheBytes is a non-negative integer, got ${String(cacheBytes)}`);
  }
  return cacheBytes;
}

function checkMaxBytes(maxBytes: number | undefined, threshold: number): number {
  if (maxBytes === undefined) {
    return Math.max(DEFAULT_VALUE_MAX_BYTES, threshold);
  }
  if (!Number.isSafeInteger(maxBytes) || maxBytes < threshold) {
    throw new TypeError(`values.maxBytes is an integer of at least values.thresholdBytes (${threshold}), got ${String(maxBytes)}`);
  }
  return maxBytes;
}

function checkThreshold(threshold: number | undefined): number {
  if (threshold === undefined) {
    return DEFAULT_VALUE_THRESHOLD;
  }
  if (!Number.isSafeInteger(threshold) || threshold < MIN_VALUE_THRESHOLD) {
    throw new TypeError(`values.thresholdBytes is an integer of at least ${MIN_VALUE_THRESHOLD}, got ${String(threshold)}`);
  }
  return threshold;
}

/** checkValueOptions throws TypeError for options a value store refuses; Engine.open calls it before it opens the file. */
export function checkValueOptions(options: ValueOptions = {}): void {
  checkMaxBytes(options.maxBytes, checkThreshold(options.thresholdBytes));
  checkDriver(options.driver);
  checkCacheBytes(options.cacheBytes);
}

// One value store per storage handle, as one notifier is (events/
// notifier.ts): the instance store, the event log, the runner and the
// behaviors' contexts reach it through the storage they share. The engine
// binds its own when it opens; a storage no engine opened gets the
// default.
const stores = new WeakMap<Storage, ValueStore>();

/** bindValues makes a storage handle's value store; Engine.open calls it. */
export function bindValues(storage: Storage, options: ValueOptions = {}): ValueStore {
  const store = new ValueStore(storage, options);
  stores.set(storage, store);
  return store;
}

/** valuesOf returns a storage handle's value store, the default one if none was bound. */
export function valuesOf(storage: Storage): ValueStore {
  let store = stores.get(storage);
  if (!store) {
    store = new ValueStore(storage);
    stores.set(storage, store);
  }
  return store;
}
