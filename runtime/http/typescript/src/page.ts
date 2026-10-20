import { OperationResult } from './envelope.js';
import { HttpProblem, badRequest } from './problem.js';

/*
The page of a @paginated operation (D60). The generated router reads the
request's cursor, limit and sort with readPage, the implementation takes
the PageRequest as args.page and returns a Page, {items, next}, which the
router answers as the envelope's data. A cursor is opaque to the caller:
the unpadded base64url of the compact JSON of the implementation's key,
the position of the last item of a page in the operation's order. A
sorted operation's cursor also names its sort (D60, amended): the router
binds the page's next to the request's sort (bindPage) and readPage
unbinds a sent cursor, so the implementation's keys are the same with a
sort as without. The Go runtime's paging package and the Rust runtime's
page module read and build the same, held to
runtime/http/testdata/page_parity.json. Nothing here imports node:, so it
runs on Workers too.
*/

/**
 * An operation's SortBy argument (D60, amended), as the router carries it:
 * its query parameter, its fields by their JSON names, and @paginated's
 * defaultSort, the order of a request that sends none ("placedAt" or
 * "-placedAt").
 */
export interface SortSpec {
  readonly param: string;
  readonly fields: readonly string[];
  readonly default: string;
}

/** An operation's @paginated limits, and its SortBy argument when it has one, as the router carries them. */
export interface PageSpec {
  readonly defaultLimit: number;
  readonly maxLimit: number;
  readonly sort?: SortSpec;
}

/**
 * The order a page is read in (D60, amended): a field of the item type, by
 * its JSON name, ascending, or descending when desc; ties break by the
 * item's id in the same direction.
 */
export interface PageSort {
  readonly field: string;
  readonly desc: boolean;
}

/**
 * The page a caller asks for: the cursor of the page before, absent for
 * the first page, the limit, its default filled in, and, for a sorted
 * operation, the sort, its default filled in. A sorted operation's router
 * has unbound the cursor from the sort it was returned under, so it is the
 * implementation's own, as encodeCursor wrote it.
 */
export interface PageRequest {
  readonly cursor?: string;
  readonly limit: number;
  readonly sort?: PageSort;
}

/** A page: its items, which may be fewer than its limit, even none, and the cursor of the next page, null after the last. */
export interface Page<T> {
  readonly items: T[];
  readonly next: string | null;
}

/** The longest cursor a router reads, in characters. */
export const PAGE_CURSOR_MAX_LENGTH = 1024;

/** The detail and reason of a cursor the implementation cannot read (decodeCursor). */
export const REFUSED_CURSOR_DETAIL = 'cursor is not one this operation returned';
export const REFUSED_CURSOR_REASON = 'not a cursor this operation returned';

/** The detail and reason of a cursor returned under another sort (D60, amended). */
export const SORTED_CURSOR_DETAIL = 'cursor is not one this sort returned';
export const SORTED_CURSOR_REASON = 'returned under another sort';

/** A sort's text: a field's name, after a "-" descending. */
const SORT_PATTERN = /^-?[A-Za-z_][A-Za-z0-9_]*$/;

const CURSOR_PATTERN = /^[A-Za-z0-9_-]+$/;
/** How deep a cursor's key may not nest its arrays and objects: the Rust runtime's JSON reader refuses the 128th level. */
const MAX_KEY_DEPTH = 128;
/** A lone surrogate, which a JSON \u escape can spell and the Go and Rust readers cannot read. */
const LONE_SURROGATE = /\p{Cs}/u;
const INTEGER_TEXT = /^[+-]?[0-9]+$/;
const ALPHABET = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_';

/** The query a router reads the page from: a URLSearchParams, or each key's values as Hono's queries() gives them. */
export type PageQuery = URLSearchParams | Readonly<Record<string, string | readonly string[] | undefined>>;

function firstValue(query: PageQuery, name: string): string | undefined {
  if (query instanceof URLSearchParams) return query.get(name) ?? undefined;
  const value = query[name];
  return typeof value === 'string' ? value : value?.[0];
}

/** The 400 of a parameter the request carries, worded as params.ts words a string or integer query parameter's, with the broken rule when there is one. */
function refuse(parameter: string, reason: string, validator?: string): never {
  throw badRequest(`Invalid query parameter ${parameter}: ${reason}`, {
    details: {
      location: 'query',
      parameter,
      reason,
      ...(validator !== undefined ? { errors: [{ validator, message: reason }] } : {}),
    },
  });
}

/** The sort as a query writes it: "placedAt", "-placedAt". */
export function sortText(sort: PageSort): string {
  return sort.desc ? `-${sort.field}` : sort.field;
}

/** Reads a sort as a query writes it, "placedAt" or "-placedAt"; undefined for any other text. */
export function parseSort(text: string): PageSort | undefined {
  if (!SORT_PATTERN.test(text)) return undefined;
  return text.startsWith('-') ? { field: text.slice(1), desc: true } : { field: text, desc: false };
}

/** The values a sort argument's parameter takes: each field, then the field after "-". */
export function sortValues(spec: SortSpec): string[] {
  return spec.fields.flatMap(field => [field, `-${field}`]);
}

/**
 * Reads a page request from a query, the router's whole read: the first
 * value of cursor, of limit and of the spec's sort parameter, an empty one
 * as absent. A cursor is 1 to 1024 characters of the unpadded base64url
 * alphabet, the alphabet checked first, so a cursor outside ASCII is
 * refused for its pattern however long it is; a limit a whole number from
 * 1 to the spec's maxLimit, its defaultLimit when absent; a sort one of
 * the spec's values, its default when absent. Last, a sorted operation's
 * cursor is unbound from the sort it was returned under (unbindCursor), so
 * the request's cursor is the implementation's own. A refusal throws the
 * 400 HttpProblem at the parameter, in this order: the cursor's text, the
 * limit, the sort, then the cursor's sort.
 */
export function readPage(query: PageQuery, spec: PageSpec): PageRequest {
  const cursor = firstValue(query, 'cursor');
  if (cursor) {
    if (!CURSOR_PATTERN.test(cursor)) refuse('cursor', 'does not match the required pattern', 'pattern');
    if (cursor.length > PAGE_CURSOR_MAX_LENGTH) {
      refuse('cursor', `must be at most ${PAGE_CURSOR_MAX_LENGTH} characters`, 'maxLength');
    }
  }
  let limit = spec.defaultLimit;
  const text = firstValue(query, 'limit');
  if (text) {
    if (!INTEGER_TEXT.test(text)) refuse('limit', 'expected an integer');
    limit = Number(text);
    if (!Number.isSafeInteger(limit)) refuse('limit', 'integer out of range');
    if (limit < 1) refuse('limit', 'must be at least 1', 'min');
    if (limit > spec.maxLimit) refuse('limit', `must be at most ${spec.maxLimit}`, 'max');
  }
  if (spec.sort === undefined) return cursor ? { cursor, limit } : { limit };
  let sort = parseSort(spec.sort.default);
  if (sort === undefined) throw new TypeError(`readPage: the default sort ${JSON.stringify(spec.sort.default)} is not a field, or - and a field`);
  const sent = firstValue(query, spec.sort.param);
  if (sent) {
    const values = sortValues(spec.sort);
    if (!values.includes(sent)) refuse(spec.sort.param, `expected one of ${values.join(', ')}`);
    sort = parseSort(sent)!;
  }
  return cursor ? { cursor: unbindCursor(cursor, sort), limit, sort } : { limit, sort };
}

/**
 * Why every runtime refuses to read a cursor's key, or undefined: JSON
 * null or an empty object, which name no position; a number that is not
 * finite, which the Go and Rust readers refuse as past float64's range; a
 * lone surrogate, which a \u escape spells and the Go and Rust readers
 * refuse; and arrays and objects nested 128 deep, which the Rust reader
 * refuses.
 */
function keyProblem(key: unknown): string | undefined {
  if (key === null) return 'the key is null, which names no position';
  if (typeof key === 'object' && !Array.isArray(key) && Object.keys(key).length === 0) {
    return 'the key is an empty object, which names no position';
  }
  const unreadable = (value: unknown, depth: number): string | undefined => {
    if (typeof value === 'number') return Number.isFinite(value) ? undefined : 'a number of the key is not finite';
    if (typeof value === 'string') return LONE_SURROGATE.test(value) ? 'a string of the key holds a lone surrogate' : undefined;
    if (value === null || typeof value !== 'object') return undefined;
    if (depth + 1 >= MAX_KEY_DEPTH) return `the key nests ${MAX_KEY_DEPTH} arrays or objects deep`;
    for (const [name, member] of Object.entries(value)) {
      if (!Array.isArray(value) && LONE_SURROGATE.test(name)) return 'a member name of the key holds a lone surrogate';
      const problem = unreadable(member, depth + 1);
      if (problem !== undefined) return problem;
    }
    return undefined;
  };
  return unreadable(key, 0);
}

/**
 * The cursor of a position: the unpadded base64url of the compact JSON of
 * key. A key every router would refuse to read back throws, the
 * implementation's error: one whose cursor is past 1024 characters, about
 * 768 bytes of JSON, and one decodeCursor refuses (null, an empty object,
 * a lone surrogate, arrays and objects nested 128 deep). A key that does
 * not encode as JSON throws a TypeError.
 */
export function encodeCursor(key: unknown): string {
  const text = JSON.stringify(key);
  if (text === undefined) throw new TypeError('encodeCursor: the key does not encode as JSON');
  const problem = keyProblem(JSON.parse(text));
  if (problem !== undefined) throw new RangeError(`encodeCursor: ${problem}, which no router reads back`);
  const out = encodeBase64url(new TextEncoder().encode(text));
  if (out.length > PAGE_CURSOR_MAX_LENGTH) {
    throw new RangeError(`encodeCursor: its cursor is ${out.length} characters, past the ${PAGE_CURSOR_MAX_LENGTH} a router reads`);
  }
  return out;
}

/** The 400 of a cursor the implementation cannot read, which it may throw itself for one that decodes but names no position of its order. */
export function refusedCursor(): HttpProblem {
  return badRequest(REFUSED_CURSOR_DETAIL, {
    details: { location: 'query', parameter: 'cursor', reason: REFUSED_CURSOR_REASON },
  });
}

/** The unpadded base64url of bytes. */
function encodeBase64url(bytes: Uint8Array): string {
  let out = '';
  for (let i = 0; i < bytes.length; i += 3) {
    const n = (bytes[i]! << 16) | ((bytes[i + 1] ?? 0) << 8) | (bytes[i + 2] ?? 0);
    const chars = Math.min(bytes.length - i, 3) + 1;
    for (let j = 0; j < chars; j++) out += ALPHABET[(n >> (18 - 6 * j)) & 63];
  }
  return out;
}

/** The bytes of a canonical unpadded base64url text, or null. */
function decodeBase64url(text: string): Uint8Array | null {
  if (text.length % 4 === 1) return null;
  const out = new Uint8Array(Math.floor((text.length * 3) / 4));
  let bits = 0;
  let count = 0;
  let at = 0;
  for (const char of text) {
    const value = ALPHABET.indexOf(char);
    if (value < 0) return null;
    bits = (bits << 6) | value;
    count += 6;
    if (count >= 8) {
      count -= 8;
      out[at++] = (bits >> count) & 0xff;
      bits &= (1 << count) - 1;
    }
  }
  // The bits past the last byte are zero in the canonical text.
  return bits === 0 ? out : null;
}

/**
 * Reads a cursor back into the implementation's key. A cursor that is not
 * of the alphabet, not the canonical unpadded base64url of UTF-8 JSON, or
 * whose JSON names no position or holds what another runtime cannot read
 * (null, an empty object, a number past float64's range, a lone surrogate,
 * arrays and objects nested 128 deep), is refused with the 400 every
 * runtime answers, at cursor: the caller starts again. The key's JSON is
 * not checked against K; an implementation that finds no position in it
 * throws refusedCursor() itself.
 */
export function decodeCursor<K>(cursor: string): K {
  if (cursor.length === 0 || cursor.length > PAGE_CURSOR_MAX_LENGTH || !CURSOR_PATTERN.test(cursor)) throw refusedCursor();
  const bytes = decodeBase64url(cursor);
  if (bytes === null) throw refusedCursor();
  let text: string;
  try {
    // A byte-order mark is kept, so JSON.parse refuses it as the Go and Rust decoders do.
    text = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(bytes);
  } catch {
    throw refusedCursor();
  }
  let key: unknown;
  try {
    key = JSON.parse(text);
  } catch {
    throw refusedCursor();
  }
  if (keyProblem(key) !== undefined) throw refusedCursor();
  return key as K;
}

/** The last page: items and no next. */
export function lastPage<T>(items: T[]): Page<T> {
  return { items, next: null };
}

/** A page with a next page after it. */
export function morePage<T>(items: T[], next: string): Page<T> {
  return { items, next };
}

/**
 * The page of rows an implementation read for a request: it reads up to
 * limit + 1 rows in the operation's order, after the request's cursor, and
 * pageFromRows keeps limit of them and, when there were more, sets next to
 * the cursor of the last one kept, encodeCursor(key(row)), which throws
 * for a key no router reads back. limit is the request's; one below 1 is
 * taken as 1.
 */
export function pageFromRows<T>(rows: readonly T[], limit: number, key: (row: T) => unknown): Page<T> {
  const keep = Math.max(limit, 1);
  if (rows.length <= keep) return lastPage([...rows]);
  const items = rows.slice(0, keep);
  return morePage(items, encodeCursor(key(items[keep - 1]!)));
}

/** The 400 of a cursor returned under another sort (D60, amended). */
export function sortedCursor(): HttpProblem {
  return badRequest(SORTED_CURSOR_DETAIL, {
    details: { location: 'query', parameter: 'cursor', reason: SORTED_CURSOR_REASON },
  });
}

/** The bytes a bound cursor's JSON starts with, follows its sort with, and ends with: {"sort":"-placedAt","key":K}. */
const BOUND_PREFIX = new TextEncoder().encode('{"sort":"');
const BOUND_KEY = new TextEncoder().encode('","key":');
const BOUND_SUFFIX = new TextEncoder().encode('}');

function startsWith(bytes: Uint8Array, prefix: Uint8Array, at = 0): boolean {
  if (bytes.length - at < prefix.length) return false;
  return prefix.every((byte, i) => bytes[at + i] === byte);
}

/**
 * The cursor of the implementation's cursor under sort (D60, amended): the
 * unpadded base64url of exactly the bytes {"sort":"-placedAt","key":K}, K
 * the key's JSON as the implementation's cursor holds it, byte for byte,
 * so a key's 1.0 stays 1.0. A cursor decodeCursor would refuse, a sort
 * that is no sort's text, and a bound cursor past 1024 characters throw a
 * RangeError, the implementation's error.
 */
export function bindCursor(cursor: string, sort: PageSort): string {
  try {
    decodeCursor(cursor);
  } catch {
    throw new RangeError(`bindCursor: ${JSON.stringify(cursor)} is not a cursor encodeCursor writes`);
  }
  const text = sortText(sort);
  if (!SORT_PATTERN.test(text)) throw new RangeError(`bindCursor: the sort ${JSON.stringify(text)} is not a field, or - and a field`);
  // The key's bytes as the cursor holds them, which decodeCursor read.
  const key = decodeBase64url(cursor)!;
  const head = new TextEncoder().encode(`{"sort":"${text}","key":`);
  const bytes = new Uint8Array(head.length + key.length + BOUND_SUFFIX.length);
  bytes.set(head);
  bytes.set(key, head.length);
  bytes.set(BOUND_SUFFIX, head.length + key.length);
  const bound = encodeBase64url(bytes);
  if (bound.length > PAGE_CURSOR_MAX_LENGTH) {
    throw new RangeError(
      `bindCursor: its cursor under ${text} is ${bound.length} characters, past the ${PAGE_CURSOR_MAX_LENGTH} a router reads`
    );
  }
  return bound;
}

/**
 * The implementation's cursor of one bound under sort (D60, amended): the
 * key's JSON of {"sort":"-placedAt","key":K}, read by its bytes, encoded
 * again as encodeCursor would. A cursor bound under another sort throws
 * the 400 sortedCursor() at cursor; one that is not of the alphabet, not
 * canonical unpadded base64url, or not those bytes around a key, names no
 * sort and throws refusedCursor(). The key itself is the implementation's
 * decodeCursor's to read.
 */
export function unbindCursor(cursor: string, sort: PageSort): string {
  if (cursor.length === 0 || cursor.length > PAGE_CURSOR_MAX_LENGTH || !CURSOR_PATTERN.test(cursor)) throw refusedCursor();
  const bytes = decodeBase64url(cursor);
  if (bytes === null || !startsWith(bytes, BOUND_PREFIX)) throw refusedCursor();
  const end = bytes.indexOf(0x22, BOUND_PREFIX.length);
  if (end < 0) throw refusedCursor();
  // Bytes past ASCII fail the pattern, whatever they spell.
  const text = String.fromCharCode(...bytes.subarray(BOUND_PREFIX.length, end));
  if (!SORT_PATTERN.test(text)) throw refusedCursor();
  if (!startsWith(bytes, BOUND_KEY, end)) throw refusedCursor();
  const keyAt = end + BOUND_KEY.length;
  const keyEnd = bytes.length - BOUND_SUFFIX.length;
  if (keyEnd <= keyAt || !startsWith(bytes, BOUND_SUFFIX, keyEnd)) throw refusedCursor();
  if (text !== sortText(sort)) throw sortedCursor();
  return encodeBase64url(bytes.subarray(keyAt, keyEnd));
}

function isPage(value: unknown): value is Page<unknown> {
  return typeof value === 'object' && value !== null && Array.isArray((value as Page<unknown>).items) && 'next' in value;
}

/**
 * An implementation's outcome with its page's next bound to the request's
 * sort (bindCursor), which the router answers: a Page, or an
 * OperationResult holding one, as a new value; a Response, an outcome that
 * holds no page, a page without a next, and any outcome of a request with
 * no sort, as it is. It throws bindCursor's RangeError, the
 * implementation's error.
 */
export function bindPage<T>(outcome: T, request: PageRequest): T {
  const sort = request.sort;
  if (sort === undefined) return outcome;
  const bound = (page: Page<unknown>): Page<unknown> =>
    page.next === null ? page : { ...page, next: bindCursor(page.next, sort) };
  if (outcome instanceof OperationResult) {
    const result = outcome as OperationResult<unknown>;
    if (!isPage(result.data) || result.data.next === null) return outcome;
    return new OperationResult(bound(result.data), result.status, result.headers) as T;
  }
  if (!isPage(outcome) || outcome.next === null) return outcome;
  return bound(outcome) as T;
}
